package cx

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type wakeTask struct {
	ID              string       `json:"id"`
	Profile         string       `json:"profile"`
	Model           string       `json:"model,omitempty"`
	ReasoningEffort string       `json:"reasoning_effort,omitempty"`
	Enabled         bool         `json:"enabled"`
	Schedule        wakeSchedule `json:"schedule"`
	CreatedAt       time.Time    `json:"created_at"`
	NextRun         time.Time    `json:"next_run,omitempty"`
	LastRun         time.Time    `json:"last_run,omitempty"`
	LastStatus      string       `json:"last_status,omitempty"`
}

type wakeRecord struct {
	ID         string    `json:"id"`
	TaskID     string    `json:"task_id"`
	Profile    string    `json:"profile"`
	Trigger    string    `json:"trigger"`
	DueAt      time.Time `json:"due_at"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Status     string    `json:"status"`
	Message    string    `json:"message,omitempty"`
}

type wakeState struct {
	Version int          `json:"version"`
	Tasks   []wakeTask   `json:"tasks"`
	History []wakeRecord `json:"history"`
}

func (a *App) wakeDir() string { return filepath.Join(a.Paths.DataDir, "wake") }

// Locks are separate from state.json because atomic replacement changes its inode.
// An execution lock may span a model request; the state lock never does.
func (a *App) lockWake(name string, nonblock bool) (*os.File, error) {
	if err := os.MkdirAll(a.wakeDir(), 0o700); err != nil {
		return nil, fmt.Errorf("create wake-up storage: %w", err)
	}
	if err := os.Chmod(a.wakeDir(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(a.wakeDir(), name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	mode := syscall.LOCK_EX
	if nonblock {
		mode |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), mode); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func unlockWake(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

func (a *App) accessWakeState(write bool, fn func(*wakeState) error) error {
	lock, err := a.lockWake("state", false)
	if err != nil {
		return err
	}
	defer unlockWake(lock)
	path := filepath.Join(a.wakeDir(), "state.json")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read wake-up state: %w", err)
	}
	state := wakeState{Version: 1}
	if err == nil {
		state = wakeState{}
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("parse wake-up state: %w", err)
		}
		if err := validateWakeState(state); err != nil {
			return err
		}
	}
	if err := fn(&state); err != nil {
		return err
	}
	if !write {
		return nil
	}
	if err := validateWakeState(state); err != nil {
		return err
	}
	if len(state.History) > 100 {
		state.History = state.History[len(state.History)-100:]
	}
	data, err = json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o600)
}

func validateWakeState(state wakeState) error {
	if state.Version != 1 {
		return fmt.Errorf("unsupported wake-up state version %d", state.Version)
	}
	seen := make(map[string]bool)
	for _, task := range state.Tasks {
		if !validProfileName(task.ID) || seen[task.ID] || !validProfileName(task.Profile) || task.CreatedAt.IsZero() {
			return errors.New("wake-up state contains an invalid or duplicate task")
		}
		seen[task.ID] = true
		if err := task.Schedule.validate(); err != nil {
			return fmt.Errorf("wake-up task %s: %w", task.ID, err)
		}
		if task.Enabled && task.NextRun.IsZero() {
			return fmt.Errorf("wake-up task %s has no next run", task.ID)
		}
	}
	return nil
}

func (a *App) readWakeState() (wakeState, error) {
	var result wakeState
	err := a.accessWakeState(false, func(s *wakeState) error { result = *s; return nil })
	return result, err
}

func wakeID() (string, error) {
	var bytes [6]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}
