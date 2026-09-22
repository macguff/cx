package cx

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type tuiProfile struct {
	Name     string
	Current  bool
	LoggedIn bool
	Usage    tokenUsage
	UsageErr string
}

func (a *App) tui(args []string) error {
	if len(args) != 0 {
		return ExitError{Code: 2, Err: errors.New("tui does not accept arguments")}
	}

	reader := bufio.NewReader(a.Stdin)
	window := usage30Days
	for {
		profiles, err := a.tuiProfiles(window, time.Now())
		if err != nil {
			return err
		}
		if err := writeTUITable(a.Stdout, profiles, window); err != nil {
			return err
		}

		prompt := "Select a profile number, [n] new profile, [r] range, [q] quit: "
		if len(profiles) == 0 {
			prompt = "[n] create and sign in, [q] quit: "
		}
		choice, err := readTUILine(reader, a.Stdout, prompt)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		switch strings.ToLower(choice) {
		case "q", "quit", "exit":
			return nil
		case "r", "range":
			window = nextUsageWindow(window)
			continue
		case "n", "new":
			name, err := readTUILine(reader, a.Stdout, "Profile name: ")
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			if err := a.login([]string{name}); err != nil {
				if _, writeErr := fmt.Fprintf(a.Stderr, "Unable to create profile: %v\n", err); writeErr != nil {
					return writeErr
				}
			}
			continue
		}

		selected, err := strconv.Atoi(choice)
		if err != nil || selected < 1 || selected > len(profiles) {
			if _, writeErr := fmt.Fprintln(a.Stderr, "Invalid selection."); writeErr != nil {
				return writeErr
			}
			continue
		}
		profile := profiles[selected-1]
		if !profile.LoggedIn {
			if _, err := fmt.Fprintf(a.Stdout, "%s is not signed in. Starting the official Codex login flow.\n", profile.Name); err != nil {
				return err
			}
			if err := a.login([]string{profile.Name}); err != nil {
				if _, writeErr := fmt.Fprintf(a.Stderr, "Login failed: %v\n", err); writeErr != nil {
					return writeErr
				}
				continue
			}
		}
		if err := a.use([]string{profile.Name}); err != nil {
			return err
		}
		return a.execCodex(profile.Name, nil)
	}
}

func (a *App) tuiProfiles(window usageWindow, now time.Time) ([]tuiProfile, error) {
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
	profiles := make([]tuiProfile, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validProfileName(entry.Name()) {
			continue
		}
		profilePath := filepath.Join(a.Paths.Profiles, entry.Name())
		_, authErr := os.Stat(filepath.Join(profilePath, "auth.json"))
		usage, usageErr := scanProfileUsage(profilePath, window, now)
		usageError := ""
		if usageErr != nil {
			usageError = usageErr.Error()
		}
		profiles = append(profiles, tuiProfile{
			Name: entry.Name(), Current: entry.Name() == cfg.Current,
			LoggedIn: authErr == nil, Usage: usage, UsageErr: usageError,
		})
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	return profiles, nil
}

func writeTUITable(out io.Writer, profiles []tuiProfile, window usageWindow) error {
	if _, err := fmt.Fprintf(out, "\ncx accounts · %s\n\n", window.label()); err != nil {
		return err
	}
	if len(profiles) == 0 {
		_, err := fmt.Fprintln(out, "No profiles yet.")
		return err
	}
	if _, err := fmt.Fprintln(out, " #  PROFILE               STATUS          TOTAL          INPUT         CACHED         OUTPUT       REQUESTS"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "──  ────────────────────  ──────────────  ─────────────  ─────────────  ─────────────  ─────────────  ────────"); err != nil {
		return err
	}
	var combined tokenUsage
	for i, profile := range profiles {
		marker := " "
		if profile.Current {
			marker = "*"
		}
		status := "not signed in"
		if profile.LoggedIn {
			status = "signed in"
		}
		if profile.UsageErr != "" {
			if _, err := fmt.Fprintf(out, "%s%-2d %-20s  %-14s  %-13s  %s\n", marker, i+1, truncateTUI(profile.Name, 20), status, "unavailable", profile.UsageErr); err != nil {
				return err
			}
			continue
		}
		combined.add(profile.Usage)
		if _, err := fmt.Fprintf(out, "%s%-2d %-20s  %-14s  %13s  %13s  %13s  %13s  %8s\n",
			marker, i+1, truncateTUI(profile.Name, 20), status,
			formatTokenCount(profile.Usage.Total), formatTokenCount(profile.Usage.Input),
			formatTokenCount(profile.Usage.Cached), formatTokenCount(profile.Usage.Output),
			formatTokenCount(profile.Usage.Requests)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(out, "\nCombined: %s tokens · %s requests\n", formatTokenCount(combined.Total), formatTokenCount(combined.Requests))
	return err
}

func readTUILine(reader *bufio.Reader, out io.Writer, prompt string) (string, error) {
	if _, err := io.WriteString(out, prompt); err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if errors.Is(err, io.EOF) && value == "" {
		return "", io.EOF
	}
	return value, nil
}

func nextUsageWindow(window usageWindow) usageWindow {
	switch window {
	case usage7Days:
		return usage30Days
	case usage30Days:
		return usageAll
	default:
		return usage7Days
	}
}

func truncateTUI(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}
