package cx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type wakeWebProfile struct {
	Name       string     `json:"name"`
	Current    bool       `json:"current"`
	SignedIn   bool       `json:"signed_in"`
	Tokens     int64      `json:"tokens"`
	Requests   int64      `json:"requests"`
	Activity   []int64    `json:"activity"`
	LastLocal  time.Time  `json:"last_local"`
	UsageError string     `json:"usage_error,omitempty"`
	Quota      *wakeQuota `json:"quota,omitempty"`
}

type wakeWebTask struct {
	wakeTask
	Slots []time.Time `json:"slots"`
}

func (s *wakeHTTP) profiles(now time.Time) ([]wakeWebProfile, error) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.usage == nil || now.Sub(s.usageAt) >= 15*time.Second {
		names, err := s.app.profileNames()
		if err != nil {
			return nil, err
		}
		cfg, err := s.app.loadConfig()
		if err != nil {
			return nil, err
		}
		profiles := make([]wakeWebProfile, 0, len(names))
		for _, name := range names {
			path := filepath.Join(s.app.Paths.Profiles, name)
			info, authErr := os.Stat(filepath.Join(path, "auth.json"))
			profile := wakeWebProfile{Name: name, Current: name == cfg.Current, SignedIn: authErr == nil && info.Mode().IsRegular(), Activity: make([]int64, 30)}
			var records []usageRecord
			usage, _, _, err := scanProfileUsageDetailedWithRecords(path, usage5Hours, now, false, false, &records)
			if err != nil {
				profile.UsageError = err.Error()
			} else {
				profile.Tokens, profile.Requests = usage.Total, usage.Requests
				for _, record := range records {
					index := int(record.Timestamp.Sub(now.Add(-5*time.Hour)) / (10 * time.Minute))
					if index >= 0 && index < 30 {
						profile.Activity[index] += record.Tokens.Input + record.Tokens.Output
					}
					if record.Timestamp.After(profile.LastLocal) {
						profile.LastLocal = record.Timestamp
					}
				}
			}
			profiles = append(profiles, profile)
		}
		s.usage, s.usageAt = profiles, now
	}
	quotas, err := s.app.readWakeQuotas()
	if err != nil {
		return nil, err
	}
	profiles := append([]wakeWebProfile{}, s.usage...)
	for i := range profiles {
		if quota, ok := quotas[profiles[i].Name]; ok {
			profiles[i].Quota = &quota
		}
	}
	return profiles, nil
}

func webWakeTask(task wakeTask, now time.Time) wakeWebTask {
	result := wakeWebTask{wakeTask: task, Slots: make([]time.Time, 0, 3)}
	next := task.NextRun
	if next.IsZero() || !next.After(now) {
		next = task.Schedule.next(now, task.CreatedAt)
	}
	for i := 0; i < 3 && !next.IsZero(); i++ {
		result.Slots = append(result.Slots, next)
		next = task.Schedule.next(next, task.CreatedAt)
	}
	return result
}

type wakeTaskInput struct {
	Profile         string `json:"profile"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	Kind            string `json:"kind"`
	Times           string `json:"times"`
	At              string `json:"at"`
	Every           string `json:"every"`
	Timezone        string `json:"timezone"`
	Enabled         *bool  `json:"enabled"`
}

func (s *wakeHTTP) parseTask(input wakeTaskInput) (wakeTask, error) {
	schedule := wakeSchedule{Kind: input.Kind, Timezone: input.Timezone}
	if schedule.Timezone == "" {
		schedule.Timezone = "Local"
	}
	switch input.Kind {
	case "daily", "weekdays":
		schedule.Times = wakeTimes(input.Times)
	case "interval":
		schedule.Every = input.Every
	case "once":
		at, err := parseWakeAt(strings.ReplaceAll(input.At, "T", " "), schedule.Timezone, s.app.Now())
		// RFC3339 also contains T; keep its explicit timezone intact.
		if _, explicitErr := time.Parse(time.RFC3339, input.At); explicitErr == nil {
			at, err = time.Parse(time.RFC3339, input.At)
		}
		if err != nil {
			return wakeTask{}, err
		}
		schedule.At = at
	}
	task, err := s.app.newWakeTask(input.Profile, input.Model, schedule, s.app.Now())
	if input.ReasoningEffort != "" && input.ReasoningEffort != "low" && input.ReasoningEffort != "medium" && input.ReasoningEffort != "high" && input.ReasoningEffort != "xhigh" && input.ReasoningEffort != "max" && input.ReasoningEffort != "ultra" {
		return wakeTask{}, errors.New("无效的推理强度")
	}
	task.ReasoningEffort = input.ReasoningEffort
	if input.Enabled != nil {
		task.Enabled = *input.Enabled
	}
	return task, err
}

func (s *wakeHTTP) api(w http.ResponseWriter, r *http.Request) {
	fail := func(code int, err error) { writeWakeJSON(w, code, map[string]string{"error": err.Error()}) }
	app := *s.app
	app.Stdout, app.Stderr = io.Discard, io.Discard
	path := r.URL.Path
	switch {
	case path == "/api/health" && r.Method == http.MethodGet:
		writeWakeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": BuildVersion, "build_id": s.buildID})
	case path == "/api/state" && r.Method == http.MethodGet:
		state, err := app.readWakeState()
		if err != nil {
			fail(http.StatusInternalServerError, err)
			return
		}
		now := app.Now()
		profiles, err := s.profiles(now)
		if err != nil {
			fail(http.StatusInternalServerError, err)
			return
		}
		tasks := make([]wakeWebTask, 0, len(state.Tasks))
		for _, task := range state.Tasks {
			tasks = append(tasks, webWakeTask(task, now))
		}
		history := state.History
		if history == nil {
			history = []wakeRecord{}
		}
		writeWakeJSON(w, http.StatusOK, map[string]any{"now": now, "profiles": profiles, "tasks": tasks, "history": history, "scheduler": "running", "version": BuildVersion})
	case (path == "/api/tasks" || path == "/api/preview") && r.Method == http.MethodPost:
		var input wakeTaskInput
		if err := decodeWakeJSON(w, r, &input); err != nil {
			fail(http.StatusBadRequest, err)
			return
		}
		task, err := s.parseTask(input)
		if err != nil {
			fail(http.StatusBadRequest, err)
			return
		}
		if path == "/api/preview" {
			writeWakeJSON(w, http.StatusOK, webWakeTask(task, app.Now()))
			return
		}
		if err := app.saveNewWakeTask(task); err != nil {
			fail(http.StatusInternalServerError, err)
			return
		}
		writeWakeJSON(w, http.StatusCreated, webWakeTask(task, app.Now()))
	case strings.HasPrefix(path, "/api/tasks/"):
		id := strings.TrimPrefix(path, "/api/tasks/")
		if !validProfileName(id) {
			fail(http.StatusBadRequest, errors.New("无效的计划 ID"))
			return
		}
		switch r.Method {
		case http.MethodDelete:
			if err := app.changeWakeTask(id, "remove"); err != nil {
				fail(http.StatusNotFound, err)
				return
			}
			writeWakeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		case http.MethodPut:
			var input wakeTaskInput
			if err := decodeWakeJSON(w, r, &input); err != nil {
				fail(http.StatusBadRequest, err)
				return
			}
			task, err := s.parseTask(input)
			if err != nil {
				fail(http.StatusBadRequest, err)
				return
			}
			err = app.accessWakeState(true, func(state *wakeState) error {
				for i, old := range state.Tasks {
					if old.ID != id {
						continue
					}
					if old.LastStatus == "running" {
						return errors.New("这项计划正在执行，请完成后再编辑")
					}
					task.ID, task.LastRun, task.LastStatus = id, old.LastRun, old.LastStatus
					if input.Enabled == nil {
						task.Enabled = old.Enabled
					}
					state.Tasks[i] = task
					return nil
				}
				return errors.New("计划不存在")
			})
			if err != nil {
				fail(http.StatusConflict, err)
				return
			}
			writeWakeJSON(w, http.StatusOK, webWakeTask(task, app.Now()))
		case http.MethodPost:
			var action struct {
				Action string `json:"action"`
			}
			if err := decodeWakeJSON(w, r, &action); err != nil {
				fail(http.StatusBadRequest, err)
				return
			}
			var err error
			switch action.Action {
			case "pause", "resume":
				err = app.changeWakeTask(id, action.Action)
			case "run":
				err = app.executeWake(r.Context(), id)
			default:
				fail(http.StatusBadRequest, errors.New("无效的计划操作"))
				return
			}
			if err != nil {
				fail(http.StatusConflict, err)
				return
			}
			writeWakeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	case strings.HasPrefix(path, "/api/profiles/") && r.Method == http.MethodPost:
		parts := strings.Split(strings.TrimPrefix(path, "/api/profiles/"), "/")
		if len(parts) != 2 || !validProfileName(parts[0]) {
			fail(http.StatusBadRequest, errors.New("无效的账号"))
			return
		}
		var input struct {
			Model string `json:"model"`
		}
		if err := decodeWakeJSON(w, r, &input); err != nil {
			fail(http.StatusBadRequest, err)
			return
		}
		if _, err := app.existingProfile(parts[0]); err != nil {
			fail(http.StatusNotFound, err)
			return
		}
		switch parts[1] {
		case "quota":
			quota, err := app.refreshWakeQuota(r.Context(), parts[0])
			if err != nil {
				fail(http.StatusBadGateway, err)
				return
			}
			writeWakeJSON(w, http.StatusOK, quota)
		case "wake":
			if err := app.wakeProfileNow(r.Context(), parts[0], input.Model); err != nil {
				fail(http.StatusConflict, err)
				return
			}
			writeWakeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func (a *App) wakeProfileNow(ctx context.Context, name, model string) error {
	task, err := a.newWakeTask(name, model, wakeSchedule{Kind: "once", Timezone: "Local", At: a.Now().Add(time.Minute)}, a.Now())
	if err != nil {
		return err
	}
	lock, err := a.lockWake("execution", true)
	if err != nil {
		return errors.New("已有唤醒请求正在执行，请稍后重试")
	}
	defer unlockWake(lock)
	if err := a.recoverWakeRuns(); err != nil {
		return err
	}
	id, err := wakeID()
	if err != nil {
		return err
	}
	record := wakeRecord{ID: id, Profile: name, Trigger: "manual", DueAt: a.Now(), StartedAt: a.Now(), Status: "running"}
	if err := a.accessWakeState(true, func(s *wakeState) error { s.History = append(s.History, record); return nil }); err != nil {
		return err
	}
	runCtx, cancel := context.WithTimeout(ctx, wakeTimeout)
	defer cancel()
	err = a.sendWakeRequest(runCtx, task)
	record.Status, record.Message = "success", "Codex completed a turn; quota reset not verified"
	if err != nil {
		record.Status, record.Message = "failed", err.Error()
		if ctx.Err() != nil {
			record.Status = "interrupted"
		}
	}
	record.FinishedAt = a.Now()
	if saveErr := a.finishWake(record); saveErr != nil {
		return saveErr
	}
	if err != nil {
		return fmt.Errorf("唤醒失败：%w", err)
	}
	return nil
}
