package cx

import (
	"bufio"
	"errors"
	"fmt"
	"io"
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
		choice, err := readTUILine(reader, a.Stdout, "\n[w] Web UI, task number, [a] add, [h] history, [b] background, [q] back: ")
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
			err = writeWakeHistory(a.Stdout, state.History)
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

func (a *App) addWakeInteractive(reader *bufio.Reader) error {
	profiles, err := a.profileNames()
	if err != nil {
		return err
	}
	if len(profiles) == 0 {
		return errors.New("no profiles; create one with 'cx login <name>' first")
	}
	if _, err := fmt.Fprintln(a.Stdout, "\nNew wake-up task (q cancels). Choose a profile:"); err != nil {
		return err
	}
	for i, profile := range profiles {
		if _, err := fmt.Fprintf(a.Stdout, "  %d. %s\n", i+1, profile); err != nil {
			return err
		}
	}
	profile, err := a.wakeInput(reader, "Profile number: ")
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(profile)
	if err != nil || n < 1 || n > len(profiles) {
		return errors.New("invalid profile selection")
	}
	kind, err := a.wakeInput(reader, "[1] once, [2] daily, [3] weekdays, [4] interval: ")
	if err != nil {
		return err
	}
	if kind != "1" && kind != "2" && kind != "3" && kind != "4" {
		return errors.New("invalid schedule selection")
	}
	tz, err := a.wakeInput(reader, "Timezone [Local], e.g. Asia/Shanghai: ")
	if err != nil {
		return err
	}
	if tz == "" {
		tz = "Local"
	}
	schedule := wakeSchedule{Timezone: tz}
	now := a.Now()
	switch kind {
	case "1":
		value, err := a.wakeInput(reader, "When? HH:MM, YYYY-MM-DD HH:MM, RFC3339, or +30m: ")
		if err != nil {
			return err
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
		value, err := a.wakeInput(reader, "Request times, comma-separated HH:MM (e.g. 07:00,12:01,17:02): ")
		if err != nil {
			return err
		}
		schedule.Kind, schedule.Times = "daily", wakeTimes(value)
		if kind == "3" {
			schedule.Kind = "weekdays"
		}
	case "4":
		value, err := a.wakeInput(reader, "Interval [5h1m]: ")
		if err != nil {
			return err
		}
		if value == "" {
			value = "5h1m"
		}
		schedule.Kind, schedule.Every = "interval", value
	}
	model, err := a.wakeInput(reader, "Model ID [Codex CLI default]: ")
	if err != nil {
		return err
	}
	task, err := a.newWakeTask(profiles[n-1], model, schedule, now)
	if err != nil {
		return err
	}
	if err := a.writeWakePreview(task); err != nil {
		return err
	}
	confirm, err := a.wakeInput(reader, "Save and enable this task? [y/N]: ")
	if err != nil {
		return err
	}
	if !strings.EqualFold(confirm, "y") && !strings.EqualFold(confirm, "yes") {
		return nil
	}
	if err := a.saveNewWakeTask(task); err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.Stdout, "Saved. Choose [b] in the manager to enable background checks, or keep 'cx wake serve' running.")
	return err
}

func (a *App) wakeTaskMenu(reader *bufio.Reader, task wakeTask) error {
	if err := a.writeWakePreview(task); err != nil {
		return err
	}
	action, err := readTUILine(reader, a.Stdout, "[r] run now (sends a request), [p] pause/resume, [d] delete, [q] back: ")
	if err != nil {
		return err
	}
	switch strings.ToLower(action) {
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
