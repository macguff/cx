package cx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWakeStoppedProcessDoesNotTrustReusedPID(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.Stdout = io.Discard
	lock, err := a.lockWake("service", false)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockWake(lock)
	if err := a.saveWakeProcess(wakeProcess{Version: 1, PID: os.Getpid(), Token: "old-process"}); err != nil {
		t.Fatal(err)
	}
	process, configured, err := a.wakeProcessState()
	if err != nil || !configured || process.Running {
		t.Fatalf("a live but reused PID was treated as our worker: %#v, %v", process, err)
	}
	if err := a.wakeProcessService("uninstall"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.wakeProcessPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stopped process metadata was not removed")
	}
}

// Exercise the real detached executable and its control files. The temporary
// profile store has only a future task; systemctl and codex are fake commands.
func TestWakeProcessFallbackLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the process fallback is used on Linux")
	}
	root := t.TempDir()
	executable := filepath.Join(root, "cx")
	build := exec.Command("go", "build", "-o", executable, "../../cmd/cx")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, output)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{
		"systemctl": "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CX_WAKE_TEST_PROBES\"\nprintf '%s\\n' 'Failed to connect to bus: No such file or directory' >&2\nexit 1\n",
		"codex":     "#!/bin/sh\nprintf called > \"$CX_WAKE_TEST_REQUEST\"\nexit 1\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configBase, dataBase := filepath.Join(root, "config"), filepath.Join(root, "data")
	env := make([]string, 0, len(os.Environ())+5)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "PATH" && key != "XDG_CONFIG_HOME" && key != "XDG_DATA_HOME" {
			env = append(env, entry)
		}
	}
	env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "XDG_CONFIG_HOME="+configBase, "XDG_DATA_HOME="+dataBase,
		"CX_WAKE_TEST_PROBES="+filepath.Join(root, "probes"), "CX_WAKE_TEST_REQUEST="+filepath.Join(root, "request"), "CX_WAKE_WEB_ADDR=127.0.0.1:0")
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Env = env
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	profile := filepath.Join(dataBase, "cx", "profiles", "work")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := run("wake", "add", "work", "--after", "1h"); err != nil {
		t.Fatalf("create future task: %s, %v", output, err)
	}
	statePath := filepath.Join(dataBase, "cx", "wake", "state.json")
	originalState, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	workerPath := filepath.Join(dataBase, "cx", "wake", "worker.json")
	readWorker := func() wakeProcess {
		t.Helper()
		var worker wakeProcess
		data, err := os.ReadFile(workerPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &worker); err != nil {
			t.Fatal(err)
		}
		return worker
	}
	t.Cleanup(func() {
		if output, err := run("wake", "uninstall"); err != nil {
			t.Errorf("clean up test worker: %s, %v", output, err)
		}
	})
	if output, err := run("wake", "install"); err != nil || !strings.Contains(output, "systemd is unavailable") || !strings.Contains(output, "Background process started") {
		log, _ := os.ReadFile(filepath.Join(dataBase, "cx", "wake", "scheduler.log"))
		t.Fatalf("failed to fall back from a missing bus: %s, %v\n%s", output, err, log)
	}
	first := readWorker()
	if first.PID < 2 {
		t.Fatal("worker never acknowledged startup")
	}
	if _, err := os.Stat(filepath.Join(configBase, "systemd", "user", "cx-wake.timer")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a timer file was installed despite an unavailable manager")
	}
	if output, err := run("wake", "status"); err != nil || !strings.Contains(output, "running (PID "+strconv.Itoa(first.PID)) {
		t.Fatalf("worker did not survive the install command exiting: %s, %v", output, err)
	}
	if output, err := run("wake", "install"); err != nil || !strings.Contains(output, "already running") || readWorker().PID != first.PID {
		t.Fatalf("reinstall created a duplicate worker: %s, %v", output, err)
	}
	if output, err := run("wake", "list"); err != nil || !strings.Contains(output, "process running") {
		t.Fatalf("menu hint did not recognize the running backend: %s, %v", output, err)
	}
	if probes, err := os.ReadFile(filepath.Join(root, "probes")); err != nil || string(probes) != "--user show --property=Version\n" {
		t.Fatalf("process management still depends on the unavailable bus: %q, %v", probes, err)
	}
	// Stop the worker unexpectedly, then verify status and an explicit restart.
	process, err := os.FindProcess(first.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		output, err := run("wake", "status")
		if err != nil {
			t.Fatalf("status after worker exit: %s, %v", output, err)
		}
		if strings.Contains(output, "process: stopped") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not stop")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if output, err := run("wake", "install"); err != nil || !strings.Contains(output, "Background process started") {
		t.Fatalf("restart stopped worker: %s, %v", output, err)
	}
	second := readWorker()
	if second.PID == first.PID || second.Token == first.Token {
		t.Fatal("worker restart did not establish a new lifetime")
	}
	// A stale stop request from a previous lifetime must not stop the new worker.
	stopPath := filepath.Join(dataBase, "cx", "wake", "worker.stop")
	if err := os.WriteFile(stopPath, []byte(first.Token), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if output, err := run("wake", "status"); err != nil || !strings.Contains(output, "running (PID "+strconv.Itoa(second.PID)) {
		t.Fatalf("stale stop request killed a restarted worker: %s, %v", output, err)
	}
	if output, err := run("wake", "web"); err != nil || !strings.Contains(output, "http://127.0.0.1:") || !strings.Contains(output, "/#token=") {
		t.Fatalf("Web UI access link is unavailable: %s, %v", output, err)
	}
	// Replace the executable without changing its path. The next web command
	// must detect the old server by build identity and restart it automatically.
	build = exec.Command("go", "build", "-ldflags", "-X github.com/macguff/cx/internal/cx.BuildVersion=upgrade-test", "-o", executable, "../../cmd/cx")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build updated fixture: %v\n%s", err, output)
	}
	execution, err := os.OpenFile(filepath.Join(dataBase, "cx", "wake", "execution.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(execution.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if output, err := run("wake", "web"); err == nil || !strings.Contains(output, "正在执行") || readWorker().Token != second.Token {
		t.Fatalf("upgrade disturbed a running request: %s, %v", output, err)
	}
	_ = syscall.Flock(int(execution.Fd()), syscall.LOCK_UN)
	_ = execution.Close()
	if output, err := run("wake", "web"); err != nil || !strings.Contains(output, "Updating the background scheduler") || !strings.Contains(output, "/#token=") {
		t.Fatalf("automatic web upgrade failed: %s, %v", output, err)
	}
	if updated := readWorker(); updated.Token == second.Token {
		t.Fatal("old worker was not replaced")
	} else {
		second = updated
	}
	if output, err := run("wake", "web"); err != nil || readWorker().Token != second.Token {
		t.Fatalf("matching worker was restarted unnecessarily: %s, %v", output, err)
	}
	// Simulate a previously installed worker that predates the Web UI. Install
	// must replace that worker while leaving its task/history file intact.
	second.WebUI = false
	legacyMetadata, _ := json.Marshal(second)
	if err := writeFileAtomic(workerPath, legacyMetadata, 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := run("wake", "install"); err != nil || !strings.Contains(output, "Upgrading") {
		t.Fatalf("legacy worker was not upgraded: %s, %v", output, err)
	}
	upgraded := readWorker()
	if !upgraded.WebUI || upgraded.Token == second.Token {
		t.Fatal("legacy process kept running without the Web UI")
	}
	if output, err := run("wake", "web"); err != nil || !strings.Contains(output, "/#token=") {
		t.Fatalf("upgraded Web UI did not start: %s, %v", output, err)
	}
	if output, err := run("wake", "uninstall"); err != nil || !strings.Contains(output, "stopped and removed") {
		t.Fatalf("uninstall process without systemd: %s, %v", output, err)
	}
	if _, err := os.Stat(workerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("worker metadata was left behind")
	}
	if state, err := os.ReadFile(statePath); err != nil || string(state) != string(originalState) {
		t.Fatalf("service lifecycle modified tasks/history: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "request")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("service test invoked Codex unexpectedly")
	}
}
