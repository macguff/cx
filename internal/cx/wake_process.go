package cx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type wakeProcess struct {
	Version   int       `json:"version"`
	PID       int       `json:"pid"`
	Token     string    `json:"token"`
	StartedAt time.Time `json:"started_at"`
	Running   bool      `json:"-"`
	WebUI     bool      `json:"web_ui,omitempty"`
}

func (a *App) wakeProcessPath() string { return filepath.Join(a.wakeDir(), "worker.json") }
func (a *App) wakeStopPath() string    { return filepath.Join(a.wakeDir(), "worker.stop") }

func (a *App) wakeProcessState() (wakeProcess, bool, error) {
	var process wakeProcess
	data, err := os.ReadFile(a.wakeProcessPath())
	if errors.Is(err, os.ErrNotExist) {
		return process, false, nil
	}
	if err != nil {
		return process, false, err
	}
	if err := json.Unmarshal(data, &process); err != nil {
		return process, true, fmt.Errorf("read background process state: %w", err)
	}
	if process.Version != 1 || process.PID < 0 || !validProfileName(process.Token) {
		return process, true, errors.New("invalid background process state")
	}
	// A PID can be reused. Only the lifetime lock establishes that our worker is
	// alive; stopping uses a token-scoped control file, never signals to that PID.
	lock, err := a.lockWake("worker", true)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		process.Running = true
		return process, true, nil
	}
	if err != nil {
		return process, true, err
	}
	unlockWake(lock)
	return process, true, nil
}

func (a *App) saveWakeProcess(process wakeProcess) error {
	data, err := json.Marshal(process)
	if err != nil {
		return err
	}
	return writeFileAtomic(a.wakeProcessPath(), append(data, '\n'), 0o600)
}

func (a *App) wakeProcessService(action string) error {
	process, configured, err := a.wakeProcessState()
	if err != nil {
		return err
	}
	switch action {
	case "status":
		state := "stopped"
		if process.Running {
			state = fmt.Sprintf("running (PID %d)", process.PID)
		}
		_, err := fmt.Fprintf(a.Stdout, "Background process: %s\nLog: %s\nNo automatic restart after reboot; run 'cx wake install' to start it again.\n", state, filepath.Join(a.wakeDir(), "scheduler.log"))
		return err
	case "uninstall":
		if process.Running {
			if err := writeFileAtomic(a.wakeStopPath(), []byte(process.Token), 0o600); err != nil {
				return err
			}
			deadline := time.Now().Add(5 * time.Second)
			for process.Running {
				if time.Now().After(deadline) {
					return errors.New("background process has not stopped yet; inspect 'cx wake status' and scheduler.log")
				}
				time.Sleep(25 * time.Millisecond)
				process, _, err = a.wakeProcessState()
				if err != nil {
					return err
				}
			}
		}
		for _, path := range []string{a.wakeProcessPath(), a.wakeStopPath()} {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if _, err := fmt.Fprintln(a.Stdout, "Background process stopped and removed. Tasks and history are kept."); err != nil {
			return err
		}
		if _, err := os.Stat(a.wakeServicePath()); err == nil {
			_, err := fmt.Fprintln(a.Stdout, "Existing systemd unit files remain. When the user manager is available, run 'cx wake uninstall' again to remove them.")
			return err
		}
		return nil
	case "install":
		if configured && process.Running {
			if !process.WebUI {
				if _, err := fmt.Fprintln(a.Stdout, "Upgrading the background process to include the Web UI; tasks and history are kept."); err != nil {
					return err
				}
				if err := a.wakeProcessService("uninstall"); err != nil {
					return err
				}
				return a.startWakeProcess()
			}
			_, err := fmt.Fprintf(a.Stdout, "Background process is already running (PID %d).\n", process.PID)
			return err
		}
	}
	return a.startWakeProcess()
}

func (a *App) startWakeProcess() error {
	executable, overrides, err := a.wakeInstallation()
	if err != nil {
		return err
	}
	logPath := filepath.Join(a.wakeDir(), "scheduler.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	if err := log.Chmod(0o600); err != nil {
		return err
	}
	token, err := wakeID()
	if err != nil {
		return err
	}
	if err := a.saveWakeProcess(wakeProcess{Version: 1, Token: token}); err != nil {
		return err
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, overridden := overrides[key]; !overridden {
			env = append(env, entry)
		}
	}
	for _, key := range sortedWakeEnvKeys(overrides) {
		env = append(env, key+"="+overrides[key])
	}
	cmd := exec.Command(executable, "wake", "service", "_worker", token)
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = env, a.wakeDir(), log, log
	// Start a new session with no terminal or stdin. No systemd, shell, nohup,
	// cron daemon, or user D-Bus session is needed for this backend.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(a.wakeProcessPath())
		return fmt.Errorf("start background process: %w", err)
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		process, _, err := a.wakeProcessState()
		if err != nil {
			return err
		}
		if process.Running && process.Token == token && process.PID == cmd.Process.Pid {
			_, err := fmt.Fprintf(a.Stdout, "Background process started (PID %d); checks every 15 seconds.\nLog: %s\nNo automatic restart after reboot; run 'cx wake install' to start it again.\n", process.PID, logPath)
			return err
		}
		select {
		case err := <-finished:
			return fmt.Errorf("background process stopped during startup (%v); inspect %s", err, logPath)
		case <-timeout.C:
			_ = writeFileAtomic(a.wakeStopPath(), []byte(token), 0o600)
			return fmt.Errorf("background process did not confirm startup; stop requested; inspect %s", logPath)
		case <-ticker.C:
		}
	}
}

func (a *App) wakeWorkerStopRequested(token string) (bool, error) {
	data, err := os.ReadFile(a.wakeStopPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return string(data) == token, err
}

func (a *App) runWakeWorker(token string) error {
	lock, err := a.lockWake("worker", true)
	if err != nil {
		return fmt.Errorf("lock background process: %w", err)
	}
	defer unlockWake(lock)
	process, configured, err := a.wakeProcessState()
	if err != nil {
		return err
	}
	if !configured || process.Token != token {
		return errors.New("background process startup token does not match its configuration")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if requested, err := a.wakeWorkerStopRequested(token); err != nil {
		return err
	} else if requested {
		return nil
	}
	process.PID, process.StartedAt, process.WebUI = os.Getpid(), a.Now(), true
	if err := a.saveWakeProcess(process); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				requested, err := a.wakeWorkerStopRequested(token)
				if err != nil {
					_, _ = fmt.Fprintln(a.Stderr, "Background process control:", err)
				}
				if requested || err != nil {
					cancel()
					return
				}
			}
		}
	}()
	err = a.serveWake(ctx)
	cancel()
	<-done
	return err
}
