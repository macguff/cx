package cx

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

var errWakeCancelled = errors.New("cancelled")

func (a *App) wakeMenu(reader *bufio.Reader) (result error) {
	defer func() {
		if errors.Is(result, io.EOF) {
			result = nil
		}
	}()
	for {
		state, err := a.readWakeState()
		if err != nil {
			return err
		}
		if err := a.writeWakeTasks(state.Tasks); err != nil {
			return err
		}
		choice, err := readTUILine(reader, a.Stdout, "\nTask number, [a] add, [c] accounts/quota, [h] history, [b] background, [p] pause all, [r] resume all, [w] web, [q] back: ")
		if err != nil {
			return err
		}
		switch strings.ToLower(choice) {
		case "q", "quit", "exit":
			return nil
		case "a", "add", "n":
			err = a.addWakeInteractive(reader)
		case "w", "web":
			err = a.wakeWebCommand(nil)
		case "h", "history":
			filter, e := readTUILine(reader, a.Stdout, "Filter by profile or status (success, failed, missed, running, interrupted) [all]: ")
			if e != nil {
				err = e
				break
			}
			var records []wakeRecord
			for _, record := range state.History {
				if filter == "" || strings.EqualFold(filter, record.Profile) || strings.EqualFold(filter, record.Status) {
					records = append(records, record)
				}
			}
			err = writeWakeHistory(a.Stdout, records)
		case "c", "accounts":
			err = a.wakeAccountsMenu(reader)
		case "p", "r":
			for _, task := range state.Tasks {
				action := "pause"
				if strings.ToLower(choice) == "r" {
					action = "resume"
				}
				if action == "pause" && !task.Enabled || action == "resume" && (task.Enabled || task.Schedule.Kind == "once" && task.NextRun.IsZero()) {
					continue
				}
				if e := a.changeWakeTask(task.ID, action); e != nil {
					err = errors.Join(err, e)
				}
			}
		case "b", "background":
			var action string
			action, err = readTUILine(reader, a.Stdout, "Background scheduler: [i] install/enable, [s] status, [u] uninstall, [q] back: ")
			if err == nil {
				switch strings.ToLower(action) {
				case "i":
					err = a.wakeService([]string{"install"})
				case "s":
					err = a.wakeService([]string{"status"})
				case "u":
					err = a.wakeService([]string{"uninstall"})
				case "q":
				default:
					err = errors.New("invalid background selection")
				}
			}
		default:
			n, parseErr := strconv.Atoi(choice)
			if parseErr != nil || n < 1 || n > len(state.Tasks) {
				err = errors.New("invalid task selection")
			} else {
				err = a.wakeTaskMenu(reader, state.Tasks[n-1])
			}
		}
		if err != nil && !errors.Is(err, errWakeCancelled) {
			if errors.Is(err, io.EOF) {
				return err
			}
			if _, err := fmt.Fprintln(a.Stderr, "Wake-up:", err); err != nil {
				return err
			}
		}
	}
}

func (a *App) wakeInput(reader *bufio.Reader, prompt string) (string, error) {
	value, err := readTUILine(reader, a.Stdout, prompt)
	if err == nil && strings.EqualFold(value, "q") {
		return "", errWakeCancelled
	}
	return value, err
}

func (a *App) wakeAccountsMenu(reader *bufio.Reader) error {
	web := &wakeHTTP{app: a}
	for {
		profiles, err := web.profiles(a.Now())
		if err != nil {
			return err
		}
		if len(profiles) == 0 {
			_, err = fmt.Fprintln(a.Stdout, "No profiles; use cx login <name> first.")
			return err
		}
		for i, profile := range profiles {
			if _, err := fmt.Fprintf(a.Stdout, "\n%d. %s · local 5h: %d requests, %d tokens\n", i+1, profile.Name, profile.Requests, profile.Tokens); err != nil {
				return err
			}
			if profile.UsageError != "" {
				fmt.Fprintln(a.Stdout, "  Local usage:", profile.UsageError)
			}
			if profile.Quota == nil || profile.Quota.UpdatedAt.IsZero() {
				fmt.Fprintln(a.Stdout, "  Quota: not available; refresh to query Codex")
			} else {
				q := profile.Quota
				fmt.Fprintf(a.Stdout, "  Quota snapshot: %s\n", q.UpdatedAt.Local().Format(time.RFC3339))
				keys := make([]string, 0, len(q.Buckets))
				for k := range q.Buckets {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, key := range keys {
					bucket := q.Buckets[key]
					for _, window := range []*wakeQuotaWindow{bucket.Primary, bucket.Secondary} {
						if window == nil {
							continue
						}
						duration, reset := "unknown duration", "unknown reset"
						if window.WindowDurationMins > 0 {
							duration = fmt.Sprintf("%d min", window.WindowDurationMins)
						}
						if window.ResetsAt > 0 {
							reset = time.Unix(window.ResetsAt, 0).Local().Format(time.RFC3339)
						}
						fmt.Fprintf(a.Stdout, "  %s · %s · %.0f%% used · reset %s\n", key, duration, window.UsedPercent, reset)
					}
				}
			}
			if profile.Quota != nil && profile.Quota.Error != "" {
				fmt.Fprintln(a.Stdout, "  Last refresh error:", profile.Quota.Error)
			}
		}
		choice, err := readTUILine(reader, a.Stdout, "\nAccount number, [a] refresh all quotas, [q] back: ")
		if err != nil {
			return err
		}
		if strings.EqualFold(choice, "q") {
			return nil
		}
		if strings.EqualFold(choice, "a") {
			for _, profile := range profiles {
				if _, err := a.refreshWakeQuota(context.Background(), profile.Name); err != nil {
					fmt.Fprintf(a.Stderr, "%s: %s\n", profile.Name, err)
				}
			}
			continue
		}
		n, err := strconv.Atoi(choice)
		if err != nil || n < 1 || n > len(profiles) {
			fmt.Fprintln(a.Stderr, "Invalid account selection")
			continue
		}
		profile := profiles[n-1]
		action, err := readTUILine(reader, a.Stdout, "[r] refresh quota, [w] wake now, [q] back: ")
		if err != nil {
			return err
		}
		switch strings.ToLower(action) {
		case "r":
			_, err = a.refreshWakeQuota(context.Background(), profile.Name)
		case "w":
			confirm, e := readTUILine(reader, a.Stdout, "Send a real Codex request? [y/N]: ")
			if e != nil {
				return e
			}
			if strings.EqualFold(confirm, "y") || strings.EqualFold(confirm, "yes") {
				err = a.wakeProfileNow(context.Background(), profile.Name, "")
			}
		case "q":
			continue
		default:
			err = errors.New("invalid account action")
		}
		if err != nil {
			fmt.Fprintln(a.Stderr, err)
		}
	}
}

func (a *App) addWakeInteractive(reader *bufio.Reader) error {
	return a.editWakeInteractive(reader, nil)
}

func (a *App) editWakeInteractive(reader *bufio.Reader, old *wakeTask) error {
	profiles, err := a.profileNames()
	if err != nil {
		return err
	}
	if len(profiles) == 0 {
		return errors.New("no profiles; create one with 'cx login <name>' first")
	}
	if _, err := fmt.Fprintln(a.Stdout, "\nWake-up task (q cancels). Choose a profile:"); err != nil {
		return err
	}
	for i, profile := range profiles {
		if _, err := fmt.Fprintf(a.Stdout, "  %d. %s\n", i+1, profile); err != nil {
			return err
		}
	}
	defaultProfile := 1
	if old != nil {
		for i, name := range profiles {
			if name == old.Profile {
				defaultProfile = i + 1
			}
		}
	}
	profile, err := a.wakeInput(reader, fmt.Sprintf("Profile number [%d]: ", defaultProfile))
	if err != nil {
		return err
	}
	if profile == "" {
		profile = strconv.Itoa(defaultProfile)
	}
	n, err := strconv.Atoi(profile)
	if err != nil || n < 1 || n > len(profiles) {
		return errors.New("invalid profile selection")
	}
	defaultKind := "2"
	if old != nil {
		defaultKind = map[string]string{"once": "1", "daily": "2", "weekdays": "3", "interval": "4"}[old.Schedule.Kind]
	}
	kind, err := a.wakeInput(reader, "[1] once, [2] daily, [3] weekdays, [4] interval ["+defaultKind+"]: ")
	if err != nil {
		return err
	}
	if kind == "" {
		kind = defaultKind
	}
	if kind != "1" && kind != "2" && kind != "3" && kind != "4" {
		return errors.New("invalid schedule selection")
	}
	defaultTZ := "Local"
	if old != nil {
		defaultTZ = old.Schedule.Timezone
	}
	tz, err := a.wakeInput(reader, "Timezone ["+defaultTZ+"], e.g. Asia/Shanghai: ")
	if err != nil {
		return err
	}
	if tz == "" {
		tz = defaultTZ
	}
	schedule := wakeSchedule{Timezone: tz}
	now := a.Now()
	switch kind {
	case "1":
		defaultAt := ""
		if old != nil && old.Schedule.Kind == "once" {
			defaultAt = old.Schedule.At.Format(time.RFC3339)
		}
		value, err := a.wakeInput(reader, "When? HH:MM, YYYY-MM-DD HH:MM, RFC3339, or +30m ["+defaultAt+"]: ")
		if err != nil {
			return err
		}
		if value == "" {
			value = defaultAt
		}
		schedule.Kind = "once"
		if strings.HasPrefix(value, "+") {
			d, err := time.ParseDuration(strings.TrimPrefix(value, "+"))
			if err != nil || d < time.Minute || d > 365*24*time.Hour {
				return errors.New("delay must be between 1m and 8760h")
			}
			schedule.At = now.Add(d)
		} else {
			schedule.At, err = parseWakeAt(value, tz, now)
			if err != nil {
				return err
			}
		}
	case "2", "3":
		defaultTimes := "05:00,11:01,17:10"
		if old != nil && (old.Schedule.Kind == "daily" || old.Schedule.Kind == "weekdays") {
			defaultTimes = strings.Join(old.Schedule.Times, ",")
		}
		value, err := a.wakeInput(reader, "Request times, comma-separated HH:MM ["+defaultTimes+"]: ")
		if err != nil {
			return err
		}
		if value == "" {
			value = defaultTimes
		}
		schedule.Kind, schedule.Times = "daily", wakeTimes(value)
		if kind == "3" {
			schedule.Kind = "weekdays"
		}
	case "4":
		defaultEvery := "5h1m"
		if old != nil && old.Schedule.Kind == "interval" {
			defaultEvery = old.Schedule.Every
		}
		value, err := a.wakeInput(reader, "Interval ["+defaultEvery+"]: ")
		if err != nil {
			return err
		}
		if value == "" {
			value = defaultEvery
		}
		schedule.Kind, schedule.Every = "interval", value
	}
	defaultModel, defaultEffort := "gpt-6.1-terra", "low"
	if old != nil {
		defaultModel, defaultEffort = old.Model, old.ReasoningEffort
	}
	model, err := a.wakeInput(reader, "Model ID ["+defaultModel+"] (- for CLI default): ")
	if err != nil {
		return err
	}
	if model == "" {
		model = defaultModel
	}
	if model == "-" {
		model = ""
	}
	effort, err := a.wakeInput(reader, "Reasoning effort low/medium/high/xhigh/max/ultra ["+defaultEffort+"] (- for CLI default): ")
	if err != nil {
		return err
	}
	if effort == "" {
		effort = defaultEffort
	}
	if effort == "-" {
		effort = ""
	}
	if effort != "" && effort != "low" && effort != "medium" && effort != "high" && effort != "xhigh" && effort != "max" && effort != "ultra" {
		return errors.New("invalid reasoning effort")
	}
	task, err := a.newWakeTask(profiles[n-1], model, schedule, now)
	if err != nil {
		return err
	}
	task.ReasoningEffort = effort
	if old != nil {
		task.ID, task.Enabled = old.ID, old.Enabled
	}
	if err := a.writeWakePreview(task); err != nil {
		return err
	}
	confirm, err := a.wakeInput(reader, "Save this task? [y/N]: ")
	if err != nil {
		return err
	}
	if !strings.EqualFold(confirm, "y") && !strings.EqualFold(confirm, "yes") {
		return nil
	}
	if old != nil {
		err = a.updateWakeTask(task, nil)
	} else {
		err = a.saveNewWakeTask(task)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.Stdout, "Saved. Choose [b] in the manager to enable background checks, or keep 'cx wake serve' running.")
	return err
}

func (a *App) wakeTaskMenu(reader *bufio.Reader, task wakeTask) error {
	if err := a.writeWakePreview(task); err != nil {
		return err
	}
	action, err := readTUILine(reader, a.Stdout, "[e] edit, [r] run now (sends a request), [p] pause/resume, [d] delete, [q] back: ")
	if err != nil {
		return err
	}
	switch strings.ToLower(action) {
	case "e":
		return a.editWakeInteractive(reader, &task)
	case "r":
		return a.wake([]string{"run", task.ID})
	case "p":
		action := "resume"
		if task.Enabled {
			action = "pause"
		}
		return a.changeWakeTask(task.ID, action)
	case "d":
		return a.changeWakeTask(task.ID, "remove")
	case "q":
		return nil
	default:
		return errors.New("invalid task action")
	}
}
