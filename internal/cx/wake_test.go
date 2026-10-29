package cx

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newWakeTestApp(t *testing.T) (*App, *time.Time, *int) {
	t.Helper()
	a, _, _ := newTestApp(t)
	now := wakeTime(t, "2026-10-28T00:00:00Z")
	a.Now = func() time.Time { return now }
	profile, err := a.ensureProfile("work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "auth.json"), []byte("private-auth-sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	a.WakeRun = func(_ context.Context, _ string, _ []string, _ []string, _ string, stdout io.Writer) error {
		calls++
		_, err := fmt.Fprintln(stdout, `{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":1}}`)
		return err
	}
	return a, &now, &calls
}

func onlyWakeTask(t *testing.T, a *App) wakeTask {
	t.Helper()
	s, err := a.readWakeState()
	if err != nil || len(s.Tasks) != 1 {
		t.Fatalf("read one task: %#v, %v", s, err)
	}
	return s.Tasks[0]
}

func TestWakeCLIValidatesSchedulesAndDoesNotLaunch(t *testing.T) {
	a, _, calls := newWakeTestApp(t)
	for _, args := range [][]string{
		{"work"}, {"work", "--daily", "07:00", "--every", "5h"},
		{"work", "--at", "2020-01-01T00:00:00Z"}, {"work", "--daily", "25:00"},
		{"work", "--every", "0s"}, {"work", "--after", "-1h"},
		{"work", "--daily", "07:00", "extra"}, {"missing", "--every", "5h"},
		{"../work", "--every", "5h"}, {"work", "--every", "5h", "--tz", "bad-zone"},
	} {
		if err := a.addWake(args); err == nil {
			t.Errorf("accepted invalid command: %#v", args)
		}
	}
	if err := a.Run([]string{"wake", "add", "work", "--weekdays", "17:02,07:00,12:01", "--tz", "Asia/Shanghai", "--model", "test-model"}); err != nil {
		t.Fatal(err)
	}
	task := onlyWakeTask(t, a)
	if task.Profile != "work" || task.Model != "test-model" || strings.Join(task.Schedule.Times, ",") != "07:00,12:01,17:02" {
		t.Fatalf("unexpected saved task: %#v", task)
	}
	if *calls != 0 {
		t.Fatal("adding a task sent a Codex request")
	}
	if cfg, err := a.loadConfig(); err != nil || cfg.Current != "" {
		t.Fatalf("adding a task changed the current profile: %#v, %v", cfg, err)
	}
	for path, want := range map[string]os.FileMode{a.wakeDir(): 0o700, filepath.Join(a.wakeDir(), "state.json"): 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("permissions on %s: %v, %v", path, info, err)
		}
	}
}

func TestWakeTickUsesOfficialCLIAndPreservesProfile(t *testing.T) {
	t.Setenv("CODEX_HOME", "/wrong-profile")
	t.Setenv("OPENAI_API_KEY", "wrong-openai-key")
	t.Setenv("CODEX_API_KEY", "wrong-codex-key")
	t.Setenv("OPENAI_BASE_URL", "https://wrong-provider.invalid")
	a, now, calls := newWakeTestApp(t)
	profile := filepath.Join(a.Paths.Profiles, "work")
	config := filepath.Join(profile, "config.toml")
	before := []byte(profileConfig + "# preserve custom settings\n")
	if err := os.WriteFile(config, before, 0o600); err != nil {
		t.Fatal(err)
	}
	var workspace string
	a.WakeRun = func(ctx context.Context, path string, args, env []string, dir string, stdout io.Writer) error {
		*calls++
		workspace = dir
		if path != "/usr/bin/codex" || envValue(env, "CODEX_HOME") != profile {
			t.Fatalf("wrong Codex or profile: %s, %#v", path, env)
		}
		for _, key := range []string{"CODEX_API_KEY", "OPENAI_API_KEY", "OPENAI_BASE_URL"} {
			if envValue(env, key) != "" {
				t.Fatalf("inherited %s overrides profile", key)
			}
		}
		joined := strings.Join(args, "|")
		for _, want := range []string{"exec|--ignore-user-config|--ephemeral|--json", "--sandbox|read-only", `approval_policy="never"`, `cli_auth_credentials_store="file"`, "project_doc_max_bytes=0", "--model|test-model", "--|" + wakePrompt} {
			if !strings.Contains(joined, want) {
				t.Fatalf("missing %s from %s", want, joined)
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 || !strings.HasPrefix(dir, a.wakeDir()+string(os.PathSeparator)) {
			t.Fatalf("not an isolated empty workspace: %s, %v", dir, err)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > wakeTimeout {
			t.Fatal("missing bounded request timeout")
		}
		_, _ = io.WriteString(stdout, "{\"type\":\"turn.com")
		_, err = io.WriteString(stdout, "pleted\"}\n")
		return err
	}
	if err := a.addWake([]string{"work", "--after", "1m", "--model", "test-model"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Run([]string{"wake", "tick"}); err != nil || *calls != 0 {
		t.Fatalf("ran before due: %v, calls %d", err, *calls)
	}
	*now = now.Add(time.Minute)
	for i := 0; i < 2; i++ {
		if err := a.Run([]string{"wake", "tick"}); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := a.readWakeState()
	if *calls != 1 || s.Tasks[0].Enabled || len(s.History) != 1 || s.History[0].Status != "success" {
		t.Fatalf("expected one completed request: calls %d, %#v", *calls, s)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temporary workspace was not removed")
	}
	if after, _ := os.ReadFile(config); string(after) != string(before) {
		t.Fatal("wake-up changed profile configuration")
	}
	if auth, _ := os.ReadFile(filepath.Join(profile, "auth.json")); string(auth) != "private-auth-sentinel" {
		t.Fatal("wake-up changed credentials")
	}
	if _, err := os.Stat(filepath.Join(profile, "sessions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wake-up polluted session history")
	}
}

func TestWakeMissedSlotsDoNotBackfill(t *testing.T) {
	for _, schedule := range [][]string{{"--after", "1m"}, {"--every", "5h"}, {"--daily", "07:00", "--tz", "UTC"}} {
		t.Run(strings.Join(schedule, " "), func(t *testing.T) {
			a, now, calls := newWakeTestApp(t)
			if err := a.addWake(append([]string{"work"}, schedule...)); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(48 * time.Hour)
			if err := a.executeWake(context.Background(), ""); err != nil {
				t.Fatal(err)
			}
			s, _ := a.readWakeState()
			if *calls != 0 || len(s.History) != 1 || s.History[0].Status != "missed" {
				t.Fatalf("backfilled missed slot: calls %d, %#v", *calls, s)
			}
			if s.Tasks[0].Enabled && !s.Tasks[0].NextRun.After(*now) {
				t.Fatal("recurrence was not advanced past backlog")
			}
		})
	}
}

func TestWakeManualAndPauseResumeKeepIntervalAnchor(t *testing.T) {
	a, now, calls := newWakeTestApp(t)
	if err := a.addWake([]string{"work", "--every", "5h"}); err != nil {
		t.Fatal(err)
	}
	task := onlyWakeTask(t, a)
	*now = now.Add(time.Hour)
	if err := a.executeWake(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	if !onlyWakeTask(t, a).NextRun.Equal(task.NextRun) || *calls != 1 {
		t.Fatal("manual run moved recurring schedule")
	}
	if err := a.changeWakeTask(task.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(7 * time.Hour)
	if err := a.executeWake(context.Background(), ""); err != nil || *calls != 1 {
		t.Fatal("paused task ran")
	}
	if err := a.changeWakeTask(task.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	if got := onlyWakeTask(t, a).NextRun; !got.Equal(wakeTime(t, "2026-10-28T10:00:00Z")) {
		t.Fatalf("resume lost anchor: %s", got)
	}
	if err := a.changeWakeTask(task.ID, "remove"); err != nil {
		t.Fatal(err)
	}
	s, _ := a.readWakeState()
	if len(s.Tasks) != 0 || len(s.History) != 1 {
		t.Fatal("removal discarded history or retained task")
	}
}

func TestWakeResumeCoalescesBacklogAndRunsCurrentSlot(t *testing.T) {
	for _, schedule := range [][]string{{"--every", "5h"}, {"--daily", "00:00,05:00", "--tz", "UTC"}} {
		a, now, calls := newWakeTestApp(t)
		if err := a.addWake(append([]string{"work"}, schedule...)); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(125 * time.Hour)
		if err := a.executeWake(context.Background(), ""); err != nil {
			t.Fatal(err)
		}
		if err := a.executeWake(context.Background(), ""); err != nil {
			t.Fatal(err)
		}
		s, _ := a.readWakeState()
		if *calls != 1 || len(s.History) != 1 || !s.History[0].DueAt.Equal(*now) || s.History[0].Status != "success" {
			t.Fatalf("current slot lost or backlog replayed: %d calls, %#v", *calls, s)
		}
	}
}

func TestWakeFailuresAreRecordedWithoutRetryOrRawOutput(t *testing.T) {
	for _, mode := range []string{"exit", "incomplete", "turn-failed", "signed-out"} {
		t.Run(mode, func(t *testing.T) {
			a, now, calls := newWakeTestApp(t)
			if err := a.addWake([]string{"work", "--after", "1m"}); err != nil {
				t.Fatal(err)
			}
			if mode == "signed-out" {
				if err := os.Remove(filepath.Join(a.Paths.Profiles, "work", "auth.json")); err != nil {
					t.Fatal(err)
				}
			}
			a.WakeRun = func(_ context.Context, _ string, _, _ []string, _ string, out io.Writer) error {
				*calls++
				_, _ = io.WriteString(out, "private-output-sentinel\n")
				if mode == "exit" {
					return errors.New("private-error-sentinel")
				}
				if mode == "turn-failed" {
					_, _ = io.WriteString(out, "{\"type\":\"turn.failed\"}\n{\"type\":\"turn.completed\"}\n")
				}
				return nil
			}
			*now = now.Add(time.Minute)
			if err := a.executeWake(context.Background(), ""); err == nil {
				t.Fatal("failed request reported success")
			}
			if err := a.executeWake(context.Background(), ""); err != nil {
				t.Fatal(err)
			}
			s, _ := a.readWakeState()
			if len(s.History) != 1 || s.History[0].Status != "failed" || *calls > 1 {
				t.Fatalf("bad failure handling: %#v, %d calls", s, *calls)
			}
			data, _ := os.ReadFile(filepath.Join(a.wakeDir(), "state.json"))
			if strings.Contains(string(data), "private-") {
				t.Fatal("raw CLI output or auth was saved")
			}
		})
	}
}

func TestWakeCrashRecoveryDoesNotReplayClaimedSlot(t *testing.T) {
	a, now, calls := newWakeTestApp(t)
	if err := a.addWake([]string{"work", "--after", "1m"}); err != nil {
		t.Fatal(err)
	}
	task := onlyWakeTask(t, a)
	*now = now.Add(time.Minute)
	if _, _, err := a.claimWake(task.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := a.executeWake(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	s, _ := a.readWakeState()
	if *calls != 0 || s.History[0].Status != "interrupted" || s.Tasks[0].LastStatus != "interrupted" {
		t.Fatalf("claimed slot replayed or left running: %#v", s)
	}
}

func TestWakeConcurrentExecutorsAndPauseDuringRequest(t *testing.T) {
	a, now, _ := newWakeTestApp(t)
	a.Stdout, a.Stderr = io.Discard, io.Discard
	if err := a.addWake([]string{"work", "--every", "5h"}); err != nil {
		t.Fatal(err)
	}
	task := onlyWakeTask(t, a)
	*now = now.Add(5 * time.Hour)
	started, release := make(chan struct{}), make(chan struct{})
	a.WakeRun = func(_ context.Context, _ string, _, _ []string, _ string, out io.Writer) error {
		close(started)
		<-release
		_, err := io.WriteString(out, "{\"type\":\"turn.completed\"}\n")
		return err
	}
	finished := make(chan error, 1)
	go func() { finished <- a.executeWake(context.Background(), "") }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	b := NewWithPaths(a.Paths)
	b.Now, b.Stdout, b.Stderr = a.Now, io.Discard, io.Discard
	if err := b.executeWake(context.Background(), ""); err != nil {
		t.Error(err)
	}
	if err := b.executeWake(context.Background(), task.ID); err == nil {
		t.Error("concurrent manual execution was accepted")
	}
	if err := b.changeWakeTask(task.ID, "pause"); err != nil {
		t.Error("state lock was held during request:", err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	s, _ := b.readWakeState()
	if s.Tasks[0].Enabled || len(s.History) != 1 || s.History[0].Status != "success" {
		t.Fatalf("duplicate request or pause overwritten: %#v", s)
	}
}

func TestWakeStateConcurrentChangesAndCorruption(t *testing.T) {
	a, _, _ := newWakeTestApp(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, err := a.newWakeTask("work", "", wakeSchedule{Kind: "interval", Every: "5h", Timezone: "UTC"}, a.Now())
			if err != nil {
				t.Error(err)
				return
			}
			if err := a.saveNewWakeTask(task); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	s, err := a.readWakeState()
	if err != nil || len(s.Tasks) != 12 {
		t.Fatalf("lost concurrent updates: %d tasks, %v", len(s.Tasks), err)
	}
	for _, invalid := range []string{`{`, `{}`, `{"version":2}`} {
		path := filepath.Join(a.wakeDir(), "state.json")
		if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := a.addWake([]string{"work", "--every", "5h"}); err == nil {
			t.Fatal("overwrote corrupt or unsupported state")
		}
		if got, _ := os.ReadFile(path); string(got) != invalid {
			t.Fatal("corrupt state was modified")
		}
	}
}

func TestWakeTUIUsesSharedReaderAndNeverSendsOnSave(t *testing.T) {
	a, _, calls := newWakeTestApp(t)
	a.Stdin = strings.NewReader("w\na\n1\n3\nAsia/Shanghai\n07:00,12:01,17:02\n\n\ny\n1\np\nh\n\nq\nq\n")
	if err := a.Run([]string{"tui"}); err != nil {
		t.Fatal(err)
	}
	task := onlyWakeTask(t, a)
	if task.Enabled || task.Schedule.Kind != "weekdays" || *calls != 0 {
		t.Fatalf("interactive task not saved/paused correctly: %#v, %d calls", task, *calls)
	}
	for _, input := range []string{"a\nq\nq\n", "a\n1\n1\nUTC\n+30m\n\n"} {
		a.Stdin = strings.NewReader(input)
		if err := a.Run([]string{"wake"}); err != nil {
			t.Fatal(err)
		}
		_ = onlyWakeTask(t, a)
	}
}

func TestWakeTUIEditsTaskAndShowsQuota(t *testing.T) {
	a, now, calls := newWakeTestApp(t)
	if err := a.addWake([]string{"work", "--daily", "05:00", "--tz", "UTC"}); err != nil {
		t.Fatal(err)
	}
	old := onlyWakeTask(t, a)
	if err := a.changeWakeTask(old.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	old = onlyWakeTask(t, a)
	input := "\n\n\n06:00,11:01,17:10\ngpt-6.1-terra\nlow\ny\n"
	if err := a.editWakeInteractive(bufio.NewReader(strings.NewReader(input)), &old); err != nil {
		t.Fatal(err)
	}
	updated := onlyWakeTask(t, a)
	if updated.ID != old.ID || updated.Enabled || updated.Model != "gpt-6.1-terra" || updated.ReasoningEffort != "low" || strings.Join(updated.Schedule.Times, ",") != "06:00,11:01,17:10" || *calls != 0 {
		t.Fatalf("edit lost task settings: %#v", updated)
	}
	quota := map[string]wakeQuota{"work": {UpdatedAt: *now, Buckets: map[string]wakeQuotaBucket{"codex": {Primary: &wakeQuotaWindow{UsedPercent: 30, WindowDurationMins: 300, ResetsAt: now.Add(time.Hour).Unix()}}}}}
	data, _ := json.Marshal(quota)
	if err := os.WriteFile(filepath.Join(a.wakeDir(), "quotas.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a.Stdout = &out
	if err := a.wakeAccountsMenu(bufio.NewReader(strings.NewReader("q\n"))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "30% used") || !strings.Contains(out.String(), "300 min") {
		t.Fatalf("quota missing from menu: %s", out.String())
	}
}

func TestWakeHeadlessServeDoesNotStartWeb(t *testing.T) {
	t.Setenv("CX_WAKE_NO_WEB", "1")
	a, _, _ := newWakeTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.serveWake(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.wakeWebPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("headless scheduler created web metadata")
	}
}

func TestRunWakeCommandCancelsChildren(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 30 &\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := runWakeCommand(ctx, path, nil, os.Environ(), dir, io.Discard); err == nil {
		t.Fatal("cancelled command succeeded")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("child process outlived request timeout")
	}
}
