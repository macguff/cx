package cx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	wakeGrace   = 5 * time.Minute
	wakeTimeout = 90 * time.Second
	wakePrompt  = "Reply with exactly OK. Do not use tools, read files, or perform any other action."
)

func (a *App) serveWake(ctx context.Context) error {
	stopWeb, webErrors, err := a.startWakeWeb(ctx)
	if err != nil {
		return err
	}
	defer stopWeb()
	if _, err := fmt.Fprintln(a.Stdout, "Wake-up scheduler running; Ctrl-C stops it. "+wakeNotice); err != nil {
		return err
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if err := a.executeWake(ctx, ""); err != nil && ctx.Err() == nil {
			if _, err := fmt.Fprintln(a.Stderr, "Wake-up check:", err); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-webErrors:
			return fmt.Errorf("Web UI stopped: %w", err)
		case <-ticker.C:
		}
	}
}

// One executor per data directory prevents duplicate scheduled/manual requests.
// Claiming a slot is persisted BEFORE contacting Codex: a crash never replays it.
func (a *App) executeWake(ctx context.Context, manualID string) error {
	lock, err := a.lockWake("execution", true)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		if manualID == "" {
			return nil
		}
		return errors.New("another wake-up is running; try again after it finishes")
	}
	if err != nil {
		return err
	}
	defer unlockWake(lock)
	state, err := a.readWakeState()
	if err != nil {
		return err
	}
	for _, r := range state.History {
		if r.Status == "running" {
			if err := a.recoverWakeRuns(); err != nil {
				return err
			}
			break
		}
	}
	var ids []string
	if manualID != "" {
		ids = []string{manualID}
	} else {
		sort.SliceStable(state.Tasks, func(i, j int) bool { return state.Tasks[i].NextRun.Before(state.Tasks[j].NextRun) })
		for _, task := range state.Tasks {
			if task.Enabled && !task.NextRun.After(a.Now()) {
				ids = append(ids, task.ID)
			}
		}
	}
	var failures []error
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		task, record, err := a.claimWake(id, manualID != "")
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if record.ID == "" { // paused/removed by another command after the snapshot
			continue
		}
		if record.Status == "running" {
			runCtx, cancel := context.WithTimeout(ctx, wakeTimeout)
			err := a.sendWakeRequest(runCtx, task)
			cancel()
			record.Status, record.Message = "success", "Codex completed a turn; quota reset not verified"
			if err != nil {
				record.Status, record.Message = "failed", err.Error()
				if ctx.Err() != nil {
					record.Status, record.Message = "interrupted", "request cancelled; not retried"
				}
				failures = append(failures, fmt.Errorf("task %s: %s", id, record.Message))
			}
			record.FinishedAt = a.Now()
			if err := a.finishWake(record); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(a.Stdout, "%s · %s · %s: %s\n", record.TaskID, record.Profile, record.Status, record.Message); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}

func (a *App) claimWake(id string, manual bool) (wakeTask, wakeRecord, error) {
	var claimed wakeTask
	var record wakeRecord
	err := a.accessWakeState(true, func(s *wakeState) error {
		for i := range s.Tasks {
			t := &s.Tasks[i]
			if t.ID != id {
				continue
			}
			now := a.Now()
			if !manual && (!t.Enabled || t.NextRun.After(now)) {
				return nil
			}
			runID, err := wakeID()
			if err != nil {
				return err
			}
			record = wakeRecord{ID: runID, TaskID: t.ID, Profile: t.Profile, Trigger: "scheduled", DueAt: t.NextRun, StartedAt: now, Status: "running"}
			if manual {
				record.Trigger, record.DueAt = "manual", now
			} else {
				if latest := t.Schedule.latestDue(now, t.CreatedAt); latest.After(record.DueAt) {
					record.DueAt = latest
				}
				if now.Sub(record.DueAt) > wakeGrace {
					record.Status, record.Message, record.FinishedAt = "missed", "over 5 minutes late; no request sent", now
				}
				t.NextRun = t.Schedule.next(now, t.CreatedAt)
				if t.NextRun.IsZero() {
					t.Enabled = false
				}
			}
			t.LastRun, t.LastStatus = now, record.Status
			claimed = *t
			s.History = append(s.History, record)
			return nil
		}
		if manual {
			return fmt.Errorf("wake-up task %q does not exist", id)
		}
		return nil
	})
	return claimed, record, err
}

func (a *App) finishWake(record wakeRecord) error {
	return a.accessWakeState(true, func(s *wakeState) error {
		for i := range s.History {
			if s.History[i].ID == record.ID {
				s.History[i] = record
			}
		}
		for i := range s.Tasks {
			if s.Tasks[i].ID == record.TaskID {
				s.Tasks[i].LastStatus = record.Status
			}
		}
		return nil
	})
}

func (a *App) recoverWakeRuns() error {
	return a.accessWakeState(true, func(s *wakeState) error {
		for i := range s.History {
			if s.History[i].Status == "running" {
				s.History[i].Status, s.History[i].Message = "interrupted", "previous executor stopped; not retried"
				s.History[i].FinishedAt = a.Now()
			}
		}
		for i := range s.Tasks {
			if s.Tasks[i].LastStatus == "running" {
				s.Tasks[i].LastStatus = "interrupted"
			}
		}
		return nil
	})
}

func (a *App) sendWakeRequest(ctx context.Context, task wakeTask) error {
	profile, err := a.existingProfile(task.Profile)
	if err != nil {
		return err
	}
	info, err := os.Stat(filepath.Join(profile, "auth.json"))
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("profile is not signed in; run 'cx login %s'", task.Profile)
	}
	codex, err := a.LookPath("codex")
	if err != nil {
		return errors.New("codex executable was not found in PATH")
	}
	dir, err := os.MkdirTemp(a.wakeDir(), "request-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	args := []string{"exec", "--ignore-user-config", "--ephemeral", "--json", "--color", "never", "--sandbox", "read-only", "--skip-git-repo-check",
		"-c", `approval_policy="never"`, "-c", `cli_auth_credentials_store="file"`, "-c", "project_doc_max_bytes=0", "-C", dir}
	if task.Model != "" {
		args = append(args, "--model", task.Model)
	}
	if task.ReasoningEffort != "" {
		value, _ := json.Marshal(task.ReasoningEffort)
		args = append(args, "-c", "model_reasoning_effort="+string(value))
	}
	args = append(args, "--", wakePrompt)
	events := &wakeEvents{}
	err = a.WakeRun(ctx, codex, args, wakeEnvironment(os.Environ(), profile), dir, events)
	if !events.discard {
		events.line(events.pending)
	}
	if ctx.Err() != nil {
		return errors.New("Codex request cancelled or timed out; not retried")
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("Codex exited with status %d; check profile login, model access, network, and CLI version", exitErr.ExitCode())
		}
		return errors.New("could not run Codex; check the installed CLI and profile login")
	}
	if !events.completed || events.failed {
		return errors.New("Codex did not report a completed turn; check profile login, quota, model access, and network")
	}
	return nil
}

// Environment keys must not override the chosen profile's saved authentication.
func wakeEnvironment(env []string, profile string) []string {
	clean := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL":
			continue
		}
		clean = append(clean, entry)
	}
	return withCodexHome(clean, profile)
}

// Keep only completion flags, never raw CLI output. Oversized events are ignored
// so an unexpected response cannot grow memory or leak into saved history.
type wakeEvents struct {
	pending   []byte
	discard   bool
	completed bool
	failed    bool
}

func (w *wakeEvents) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		part, rest, found := bytes.Cut(p, []byte{'\n'})
		if len(w.pending)+len(part) > 64*1024 {
			w.discard = true
		}
		if !w.discard {
			w.pending = append(w.pending, part...)
		}
		if !found {
			break
		}
		if !w.discard {
			w.line(w.pending)
		}
		w.pending, w.discard, p = w.pending[:0], false, rest
	}
	return n, nil
}

func (w *wakeEvents) line(line []byte) {
	var event struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(line, &event) == nil {
		switch event.Type {
		case "turn.completed":
			w.completed = true
		case "turn.failed":
			w.failed = true
		}
	}
}

func runWakeCommand(ctx context.Context, path string, args, env []string, dir string, stdout io.Writer) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, stdout, io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Also terminate children rather than leaving a timed-out node/CLI alive.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd.Run()
}
