package cx

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

const wakeNotice = "Wake-ups send a real Codex request. Server quota windows and reset times remain server-controlled."

func (a *App) wake(args []string) error {
	if len(args) == 0 {
		return a.wakeInteractive(bufio.NewReader(a.Stdin))
	}
	switch args[0] {
	case "help", "-h", "--help":
		_, err := fmt.Fprint(a.Stdout, `cx wake - schedule small requests through the official Codex CLI

  cx wake                         Open the interactive wake-up manager
  cx wake web                     Enable Web and print the local dashboard link
  cx wake web off                 Disable Web; keep the scheduler running
  cx wake add <profile> <schedule> [--tz Area/City] [--model model]
  cx wake list                    List tasks, next runs, and last results
  cx wake run <id>                Run a task now without moving its schedule
  cx wake pause <id>              Pause future executions
  cx wake resume <id>             Resume at the next future slot
  cx wake remove <id>             Remove a task; keep its execution history
  cx wake history                 Show the last 100 executions
  cx wake serve                   Run the scheduler in this terminal (Ctrl-C stops it)
  cx wake tick                    Check due tasks once (for cron or service use)
  cx wake install                 Enable background checks (systemd, process, launchd)
  cx wake status                  Inspect the installed background scheduler
  cx wake uninstall               Disable and remove the background scheduler

Choose exactly one schedule:
  --at HH:MM                      Once, at the next occurrence of this local time
  --at 'YYYY-MM-DD HH:MM'          Once, at a specific local date and time
  --at RFC3339                    Once, at a timestamp with an explicit offset
  --after 30m                     Once, after a delay
  --daily 07:00,12:01,17:02        Every day at these times
  --weekdays 07:00                Monday through Friday
  --every 5h1m                    Fixed interval, anchored when the task is saved

Examples:
  cx wake add work --weekdays 07:00 --tz Asia/Shanghai
  cx wake add personal --after 2h --model <model-id>

The default timezone is Local (this computer's timezone). The default model is
the Codex CLI default; use --model to choose another model. Only saved profile
authentication is used. Profile config, project instructions, and session history
are excluded from the small, ephemeral wake-up request.

On Linux, install checks whether the user systemd manager is reachable. If it is
unavailable (for example, "Failed to connect to bus"), cx starts a detached
background process. This process survives closing the terminal, but must be
started again with 'cx wake install' after a reboot. Status and uninstall
work with either backend. macOS uses a LaunchAgent.

Background scheduling requires an awake computer and network access. Checks run
every 15 seconds. Tasks over 5 minutes late are recorded as missed, without
sending a request. Failed or interrupted requests are not retried automatically.
Pausing/removing a task does not cancel a request already in progress.
`+"\n"+wakeNotice+"\n")
		return err
	case "add":
		return a.addWake(args[1:])
	case "web":
		return a.wakeWebCommand(args[1:])
	case "list", "history":
		if len(args) != 1 {
			return wakeUsageError("list and history do not accept arguments")
		}
		state, err := a.readWakeState()
		if err != nil {
			return err
		}
		if args[0] == "history" {
			return writeWakeHistory(a.Stdout, state.History)
		}
		return a.writeWakeTasks(state.Tasks)
	case "pause", "resume", "remove":
		if len(args) != 2 {
			return wakeUsageError(args[0] + " requires exactly one task ID")
		}
		return a.changeWakeTask(args[1], args[0])
	case "run", "tick", "serve":
		if (args[0] == "run" && len(args) != 2) || (args[0] != "run" && len(args) != 1) {
			return wakeUsageError("use 'cx wake run <id>', 'cx wake tick', or 'cx wake serve'")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if args[0] == "run" {
			return a.executeWake(ctx, args[1])
		}
		if args[0] == "tick" {
			return a.executeWake(ctx, "")
		}
		return a.serveWake(ctx)
	case "install", "status", "uninstall":
		return a.wakeService(args)
	case "service": // Compatibility with earlier versions; help uses the short commands.
		return a.wakeService(args[1:])
	default:
		return wakeUsageError(fmt.Sprintf("unknown wake-up command %q; run 'cx wake help'", args[0]))
	}
}

func wakeUsageError(message string) error { return ExitError{Code: 2, Err: errors.New(message)} }

func (a *App) addWake(args []string) error {
	if len(args) == 0 {
		return wakeUsageError("add requires a profile and schedule; run 'cx wake help'")
	}
	flags := flag.NewFlagSet("cx wake add", flag.ContinueOnError)
	flags.SetOutput(a.Stderr)
	at := flags.String("at", "", "one-time HH:MM, local datetime, or RFC3339")
	after := flags.String("after", "", "one-time delay, for example 30m")
	daily := flags.String("daily", "", "comma-separated HH:MM times")
	weekdays := flags.String("weekdays", "", "Monday-Friday HH:MM times")
	every := flags.String("every", "", "fixed interval, for example 5h1m")
	tz := flags.String("tz", "Local", "IANA timezone")
	model := flags.String("model", "", "Codex model ID (default: CLI default)")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return ExitError{Code: 2, Err: err}
	}
	if flags.NArg() != 0 {
		return wakeUsageError("unexpected arguments after schedule")
	}
	count := 0
	for _, value := range []string{*at, *after, *daily, *weekdays, *every} {
		if value != "" {
			count++
		}
	}
	if count != 1 {
		return wakeUsageError("choose exactly one of --at, --after, --daily, --weekdays, or --every")
	}
	now := a.Now()
	schedule := wakeSchedule{Timezone: *tz}
	switch {
	case *at != "":
		parsed, err := parseWakeAt(*at, *tz, now)
		if err != nil {
			return wakeUsageError(err.Error())
		}
		schedule.Kind, schedule.At = "once", parsed
	case *after != "":
		d, err := time.ParseDuration(*after)
		if err != nil || d < time.Minute || d > 365*24*time.Hour {
			return wakeUsageError("delay must be between 1m and 8760h")
		}
		schedule.Kind, schedule.At = "once", now.Add(d)
	case *daily != "":
		schedule.Kind, schedule.Times = "daily", wakeTimes(*daily)
	case *weekdays != "":
		schedule.Kind, schedule.Times = "weekdays", wakeTimes(*weekdays)
	default:
		schedule.Kind, schedule.Every = "interval", *every
	}
	task, err := a.newWakeTask(args[0], *model, schedule, now)
	if err != nil {
		return err
	}
	if err := a.saveNewWakeTask(task); err != nil {
		return err
	}
	if err := a.writeWakePreview(task); err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.Stdout, "Saved. Enable background checks with 'cx wake install', or keep 'cx wake serve' running.")
	return err
}

func (a *App) newWakeTask(profile, model string, schedule wakeSchedule, now time.Time) (wakeTask, error) {
	if _, err := a.existingProfile(profile); err != nil {
		return wakeTask{}, err
	}
	if strings.ContainsAny(model, "\r\n\t\x00") || len(model) > 200 {
		return wakeTask{}, wakeUsageError("invalid model ID")
	}
	if err := schedule.validate(); err != nil {
		return wakeTask{}, wakeUsageError(err.Error())
	}
	next := schedule.next(now, now)
	if next.IsZero() {
		return wakeTask{}, wakeUsageError("wake-up time must be in the future")
	}
	id, err := wakeID()
	if err != nil {
		return wakeTask{}, err
	}
	return wakeTask{ID: id, Profile: profile, Model: model, Enabled: true, Schedule: schedule, CreatedAt: now, NextRun: next}, nil
}

func (a *App) saveNewWakeTask(task wakeTask) error {
	return a.accessWakeState(true, func(s *wakeState) error {
		s.Tasks = append(s.Tasks, task)
		return nil
	})
}

func (a *App) changeWakeTask(id, action string) error {
	err := a.accessWakeState(true, func(s *wakeState) error {
		for i := range s.Tasks {
			task := &s.Tasks[i]
			if task.ID != id {
				continue
			}
			switch action {
			case "remove":
				s.Tasks = append(s.Tasks[:i], s.Tasks[i+1:]...)
			case "pause":
				task.Enabled = false
			case "resume":
				if task.Enabled {
					return nil
				}
				next := task.Schedule.next(a.Now(), task.CreatedAt)
				if next.IsZero() {
					return errors.New("this one-time task has expired; create a new task with a future time")
				}
				task.Enabled, task.NextRun = true, next
			}
			return nil
		}
		return fmt.Errorf("wake-up task %q does not exist", id)
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.Stdout, "Task %s: %s\n", id, action)
	return err
}

func (a *App) writeWakeTasks(tasks []wakeTask) error {
	if _, err := fmt.Fprintln(a.Stdout, "\ncx wake-ups\n\n"+wakeNotice); err != nil {
		return err
	}
	w := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "#\tID\tPROFILE\tSTATE\tSCHEDULE\tNEXT REQUEST\tLAST RESULT"); err != nil {
		return err
	}
	for i, t := range tasks {
		state, next := "paused", "-"
		if t.Enabled {
			state, next = "enabled", formatWakeTime(t.NextRun, t.Schedule)
		} else if t.Schedule.Kind == "once" && t.NextRun.IsZero() {
			state = "finished"
		}
		status := t.LastStatus
		if status == "" {
			status = "never run"
		}
		if _, err := fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", i+1, t.ID, t.Profile, state, t.Schedule, next, status); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if len(tasks) == 0 {
		if _, err := fmt.Fprintln(a.Stdout, "No wake-up tasks yet."); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(a.Stdout, "Background: "+a.wakeServiceHint())
	return err
}

func (a *App) writeWakePreview(task wakeTask) error {
	model := task.Model
	if model == "" {
		model = "Codex CLI default"
	}
	if task.ReasoningEffort != "" {
		model += " · " + task.ReasoningEffort
	}
	if _, err := fmt.Fprintf(a.Stdout, "\nTask %s · profile %s · model %s\nSchedule: %s\n", task.ID, task.Profile, model, task.Schedule); err != nil {
		return err
	}
	next := task.NextRun
	for i := 0; i < 3 && !next.IsZero(); i++ {
		if _, err := fmt.Fprintf(a.Stdout, "  Request: %s  |  +5h reference: %s\n", formatWakeTime(next, task.Schedule), formatWakeTime(next.Add(5*time.Hour), task.Schedule)); err != nil {
			return err
		}
		next = task.Schedule.next(next, task.CreatedAt)
	}
	_, err := fmt.Fprintln(a.Stdout, "The +5h reference is a planning aid, not a confirmed quota reset.")
	return err
}

func formatWakeTime(t time.Time, schedule wakeSchedule) string {
	if t.IsZero() {
		return "-"
	}
	loc, _ := schedule.location()
	return t.In(loc).Format("2006-01-02 15:04:05 MST (-07:00)")
}

func writeWakeHistory(out io.Writer, records []wakeRecord) error {
	if len(records) == 0 {
		_, err := fmt.Fprintln(out, "No wake-up history yet.")
		return err
	}
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if _, err := fmt.Fprintf(out, "%s  %s  %s  %s  %s  %s\n", r.StartedAt.Local().Format(time.RFC3339), r.TaskID, r.Profile, r.Trigger, r.Status, r.Message); err != nil {
			return err
		}
	}
	return nil
}
