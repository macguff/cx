package cx

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

func (a *App) switchProfile(args []string) error {
	if len(args) != 0 {
		return ExitError{Code: 2, Err: errors.New("switch does not accept arguments")}
	}
	profiles, err := a.profileNames()
	if err != nil {
		return err
	}
	switch len(profiles) {
	case 0:
		return errors.New("no profiles; create one with 'cx login <name>'")
	case 1:
		return fmt.Errorf("only one profile exists (%s); create another with 'cx login <name>'", profiles[0])
	}

	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if len(profiles) == 2 {
		for _, profile := range profiles {
			if profile != cfg.Current {
				if cfg.Current == "" || !containsProfile(profiles, cfg.Current) {
					break
				}
				return a.use([]string{profile})
			}
		}
		return errors.New("no current profile; select one with 'cx use <name>'")
	}

	reader := bufio.NewReader(a.Stdin)
	for {
		if _, err := fmt.Fprintln(a.Stdout, "Select a profile:"); err != nil {
			return err
		}
		for i, profile := range profiles {
			marker := " "
			if profile == cfg.Current {
				marker = "*"
			}
			if _, err := fmt.Fprintf(a.Stdout, "%s %d. %s\n", marker, i+1, profile); err != nil {
				return err
			}
		}
		choice, err := readTUILine(reader, a.Stdout, "Profile number, or [q] quit: ")
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if strings.EqualFold(choice, "q") || strings.EqualFold(choice, "quit") {
			return nil
		}
		selected, err := strconv.Atoi(choice)
		if err != nil || selected < 1 || selected > len(profiles) {
			if _, writeErr := fmt.Fprintln(a.Stderr, "Invalid selection."); writeErr != nil {
				return writeErr
			}
			continue
		}
		return a.use([]string{profiles[selected-1]})
	}
}

func (a *App) profileNames() ([]string, error) {
	entries, err := os.ReadDir(a.Paths.Profiles)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	profiles := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && validProfileName(entry.Name()) {
			profiles = append(profiles, entry.Name())
		}
	}
	sort.Strings(profiles)
	return profiles, nil
}

func containsProfile(profiles []string, target string) bool {
	for _, profile := range profiles {
		if profile == target {
			return true
		}
	}
	return false
}
