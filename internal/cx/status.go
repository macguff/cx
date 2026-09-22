package cx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type statusProfile struct {
	Name     string
	Current  bool
	Auth     string
	FiveHour tokenUsage
	SevenDay tokenUsage
	AllTime  tokenUsage
	UsageErr string
}

func (a *App) status(args []string) error {
	if len(args) != 0 {
		return ExitError{Code: 2, Err: errors.New("status does not accept arguments")}
	}
	profiles, err := a.statusProfiles(time.Now())
	if err != nil {
		return err
	}
	return writeStatus(a.Stdout, profiles)
}

func (a *App) statusProfiles(now time.Time) ([]statusProfile, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(a.Paths.Profiles)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	profiles := make([]statusProfile, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validProfileName(entry.Name()) {
			continue
		}
		profilePath := filepath.Join(a.Paths.Profiles, entry.Name())
		profile := statusProfile{
			Name: entry.Name(), Current: entry.Name() == cfg.Current,
			Auth: a.officialLoginStatus(profilePath),
		}
		var usageErr error
		if profile.FiveHour, usageErr = scanProfileUsage(profilePath, usage5Hours, now); usageErr == nil {
			profile.SevenDay, usageErr = scanProfileUsage(profilePath, usage7Days, now)
		}
		if usageErr == nil {
			profile.AllTime, usageErr = scanProfileUsage(profilePath, usageAll, now)
		}
		if usageErr != nil {
			profile.UsageErr = usageErr.Error()
		}
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	return profiles, nil
}

func writeStatus(out io.Writer, profiles []statusProfile) error {
	if _, err := fmt.Fprintln(out, "cx status · local usage estimates"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "Server-side limits and reset times are not available through the public Codex CLI."); err != nil {
		return err
	}
	if len(profiles) == 0 {
		_, err := fmt.Fprintln(out, "\nNo profiles yet. Create one with: cx login <name>")
		return err
	}
	for _, profile := range profiles {
		currentLabel := ""
		if profile.Current {
			currentLabel = " (current)"
		}
		if _, err := fmt.Fprintf(out, "\nProfile: %s%s\n", profile.Name, currentLabel); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "  Auth: %s\n", profile.Auth); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "  Limits: run 'cx run %s', then enter '/status'\n", profile.Name); err != nil {
			return err
		}
		if profile.UsageErr != "" {
			if _, err := fmt.Fprintf(out, "  Usage unavailable: %s\n", profile.UsageErr); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(out, "  Local usage:\n    %-14s %10s tokens (%s)\n    %-14s %10s tokens (%s)\n    %-14s %10s tokens (%s)\n",
			usage5Hours.label()+":", formatTokenHuman(profile.FiveHour.Total), formatTokenCount(profile.FiveHour.Total),
			usage7Days.label()+":", formatTokenHuman(profile.SevenDay.Total), formatTokenCount(profile.SevenDay.Total),
			usageAll.label()+":", formatTokenHuman(profile.AllTime.Total), formatTokenCount(profile.AllTime.Total)); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) officialLoginStatus(profilePath string) string {
	if _, err := os.Stat(filepath.Join(profilePath, "auth.json")); errors.Is(err, os.ErrNotExist) {
		return "not signed in"
	} else if err != nil {
		return "unavailable (cannot inspect credential file)"
	}
	codex, err := a.LookPath("codex")
	if err != nil {
		return "unavailable (codex executable not found)"
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := a.Runner.Run(codex, []string{"login", "status"}, withCodexHome(os.Environ(), profilePath), strings.NewReader(""), &stdout, &stderr); err != nil {
		return "unavailable (official login check failed)"
	}
	status := officialStatusLine(stdout.String(), stderr.String())
	if status == "" {
		return "signed in (official check passed)"
	}
	return status
}

func officialStatusLine(outputs ...string) string {
	for _, output := range outputs {
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "WARNING:") {
				return strings.Join(strings.Fields(line), " ")
			}
		}
	}
	return ""
}
