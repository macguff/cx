package cx

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type modelUsageProfile struct {
	Name       string
	Current    bool
	FiveHour   map[string]tokenUsage
	SevenDay   map[string]tokenUsage
	AllTime    map[string]tokenUsage
	Monthly    map[string]tokenUsage
	AsOf       time.Time
	UsageError string
}

type monthlyUsagePoint struct {
	Month string
	Usage tokenUsage
}

func (a *App) usage(args []string) error {
	if len(args) != 0 {
		return ExitError{Code: 2, Err: errors.New("usage does not accept arguments")}
	}
	profiles, err := a.modelUsageProfiles(time.Now())
	if err != nil {
		return err
	}
	return writeModelUsage(a.Stdout, profiles)
}

func (a *App) modelUsageProfiles(now time.Time) ([]modelUsageProfile, error) {
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
	profiles := make([]modelUsageProfile, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validProfileName(entry.Name()) {
			continue
		}
		profilePath := filepath.Join(a.Paths.Profiles, entry.Name())
		profile := modelUsageProfile{Name: entry.Name(), Current: entry.Name() == cfg.Current, AsOf: now}
		var usageErr error
		if profile.FiveHour, usageErr = scanProfileUsageByModel(profilePath, usage5Hours, now); usageErr == nil {
			profile.SevenDay, usageErr = scanProfileUsageByModel(profilePath, usage7Days, now)
		}
		if usageErr == nil {
			profile.AllTime, profile.Monthly, usageErr = scanProfileUsageByModelAndMonth(profilePath, usageAll, now)
		}
		if usageErr != nil {
			profile.UsageError = usageErr.Error()
		}
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	return profiles, nil
}

func writeModelUsage(out io.Writer, profiles []modelUsageProfile) error {
	if _, err := fmt.Fprintln(out, "cx usage · local token usage by model"); err != nil {
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
		if profile.UsageError != "" {
			if _, err := fmt.Fprintf(out, "  Usage unavailable: %s\n", profile.UsageError); err != nil {
				return err
			}
			continue
		}
		models := modelNames(profile)
		if len(models) == 0 {
			if _, err := fmt.Fprintln(out, "  No local usage recorded."); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintln(out, "  MODEL                         5 HOURS       7 DAYS      ALL TIME"); err != nil {
			return err
		}
		for _, model := range models {
			if _, err := fmt.Fprintf(out, "  %-27s %10s   %10s   %10s\n", truncateTUI(model, 27),
				formatTokenHuman(profile.FiveHour[model].Total),
				formatTokenHuman(profile.SevenDay[model].Total),
				formatTokenHuman(profile.AllTime[model].Total)); err != nil {
				return err
			}
		}
		months := monthlyUsageSeries(profile.Monthly, profile.AsOf)
		if len(months) > 0 {
			if _, err := fmt.Fprintln(out, "\n  MONTHLY TOTALS"); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(out, "  MONTH             TOTAL          EXACT      CHANGE"); err != nil {
				return err
			}
			for i, month := range months {
				change := "-"
				if i > 0 {
					change = monthlyChange(months[i-1].Usage.Total, month.Usage.Total)
				}
				if _, err := fmt.Fprintf(out, "  %-10s %10s   %12s   %9s\n", month.Month,
					formatTokenHuman(month.Usage.Total), formatTokenCount(month.Usage.Total), change); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(out, "  Trend: %s  (%s to %s)\n", monthlySparkline(months), months[0].Month, months[len(months)-1].Month); err != nil {
				return err
			}
		}
	}
	return nil
}

func monthlyUsageSeries(values map[string]tokenUsage, now time.Time) []monthlyUsagePoint {
	if len(values) == 0 {
		return nil
	}
	var first time.Time
	for month := range values {
		parsed, err := time.ParseInLocation("2006-01", month, time.Local)
		if err != nil {
			continue
		}
		if first.IsZero() || parsed.Before(first) {
			first = parsed
		}
	}
	if first.IsZero() {
		return nil
	}
	localNow := now.In(time.Local)
	last := time.Date(localNow.Year(), localNow.Month(), 1, 0, 0, 0, 0, time.Local)
	if first.After(last) {
		last = first
	}
	points := make([]monthlyUsagePoint, 0)
	for month := first; !month.After(last); month = month.AddDate(0, 1, 0) {
		label := month.Format("2006-01")
		points = append(points, monthlyUsagePoint{Month: label, Usage: values[label]})
	}
	return points
}

func monthlyChange(previous, current int64) string {
	if previous == 0 {
		if current == 0 {
			return "0%"
		}
		return "new"
	}
	change := (float64(current) - float64(previous)) / float64(previous) * 100
	return fmt.Sprintf("%+.1f%%", change)
}

func monthlySparkline(months []monthlyUsagePoint) string {
	if len(months) == 0 {
		return ""
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	result := make([]rune, len(months))
	var maximum int64
	for _, month := range months {
		if month.Usage.Total > maximum {
			maximum = month.Usage.Total
		}
	}
	if maximum == 0 {
		for i := range result {
			result[i] = blocks[0]
		}
		return string(result)
	}
	for i, month := range months {
		level := int(month.Usage.Total * int64(len(blocks)-1) / maximum)
		result[i] = blocks[level]
	}
	return string(result)
}

func modelNames(profile modelUsageProfile) []string {
	seen := make(map[string]struct{})
	for _, values := range []map[string]tokenUsage{profile.FiveHour, profile.SevenDay, profile.AllTime} {
		for model := range values {
			seen[model] = struct{}{}
		}
	}
	models := make([]string, 0, len(seen))
	for model := range seen {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}
