package cx

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxRolloutLineBytes = 16 << 20

type tokenUsage struct {
	Input     int64
	Cached    int64
	Output    int64
	Reasoning int64
	Total     int64
	Requests  int64
	Files     int64
	Malformed int64
}

func (u *tokenUsage) add(other tokenUsage) {
	u.Input += other.Input
	u.Cached += other.Cached
	u.Output += other.Output
	u.Reasoning += other.Reasoning
	u.Total += other.Total
	u.Requests += other.Requests
	u.Files += other.Files
	u.Malformed += other.Malformed
}

type tokenFields struct {
	Input     int64 `json:"input_tokens"`
	Cached    int64 `json:"cached_input_tokens"`
	Output    int64 `json:"output_tokens"`
	Reasoning int64 `json:"reasoning_output_tokens"`
}

func (u tokenFields) normalized() tokenFields {
	u.Input = max(u.Input, 0)
	u.Cached = min(max(u.Cached, 0), u.Input)
	u.Output = max(u.Output, 0)
	u.Reasoning = min(max(u.Reasoning, 0), u.Output)
	return u
}

func (u tokenFields) equal(other tokenFields) bool {
	return u == other
}

type rolloutEvent struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type  string `json:"type"`
		Model string `json:"model"`
		Info  *struct {
			Model string       `json:"model"`
			Total *tokenFields `json:"total_token_usage"`
			Last  *tokenFields `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

type usageWindow string

const (
	usage5Hours usageWindow = "5h"
	usage7Days  usageWindow = "7d"
	usage30Days usageWindow = "30d"
	usageAll    usageWindow = "all"
)

func parseUsageWindow(value string) usageWindow {
	switch usageWindow(value) {
	case usage7Days, usage30Days, usageAll:
		return usageWindow(value)
	default:
		return usage30Days
	}
}

func (w usageWindow) label() string {
	switch w {
	case usage5Hours:
		return "Last 5 hours"
	case usage7Days:
		return "Last 7 days"
	case usageAll:
		return "All time"
	default:
		return "Last 30 days"
	}
}

func (w usageWindow) includes(timestamp time.Time, now time.Time) bool {
	if w == usage5Hours {
		return !timestamp.After(now) && !timestamp.Before(now.Add(-5*time.Hour))
	}
	localNow := now.In(time.Local)
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.Local)
	if !timestamp.Before(today.AddDate(0, 0, 1)) {
		return false
	}
	if w == usageAll {
		return true
	}
	days := 30
	if w == usage7Days {
		days = 7
	}
	start := today.AddDate(0, 0, -(days - 1))
	return !timestamp.Before(start)
}

func scanProfileUsage(profile string, window usageWindow, now time.Time) (tokenUsage, error) {
	summary, _, _, err := scanProfileUsageDetailed(profile, window, now, false, false)
	return summary, err
}

func scanProfileUsageByModel(profile string, window usageWindow, now time.Time) (map[string]tokenUsage, error) {
	_, models, _, err := scanProfileUsageDetailed(profile, window, now, true, false)
	return models, err
}

func scanProfileUsageByModelAndMonth(profile string, window usageWindow, now time.Time) (map[string]tokenUsage, map[string]tokenUsage, error) {
	_, models, months, err := scanProfileUsageDetailed(profile, window, now, true, true)
	return models, months, err
}

func scanProfileUsageDetailed(profile string, window usageWindow, now time.Time, collectModels, collectMonths bool) (tokenUsage, map[string]tokenUsage, map[string]tokenUsage, error) {
	root := filepath.Join(profile, "sessions")
	if info, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return tokenUsage{}, map[string]tokenUsage{}, map[string]tokenUsage{}, nil
	} else if err != nil {
		return tokenUsage{}, nil, nil, fmt.Errorf("inspect sessions: %w", err)
	} else if !info.IsDir() {
		return tokenUsage{}, nil, nil, errors.New("sessions path is not a directory")
	}

	var summary tokenUsage
	var models map[string]tokenUsage
	if collectModels {
		models = make(map[string]tokenUsage)
	}
	var months map[string]tokenUsage
	if collectMonths {
		months = make(map[string]tokenUsage)
	}
	seen := make(map[string]struct{})
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(entry.Name(), "rollout-") || filepath.Ext(entry.Name()) != ".jsonl" {
			return nil
		}
		fileUsage, err := scanRollout(path, window, now, seen, models, months)
		if err != nil {
			return err
		}
		summary.add(fileUsage)
		summary.Files++
		return nil
	})
	if err != nil {
		return tokenUsage{}, nil, nil, fmt.Errorf("scan sessions: %w", err)
	}
	return summary, models, months, nil
}

func scanRollout(path string, window usageWindow, now time.Time, seen map[string]struct{}, models, months map[string]tokenUsage) (tokenUsage, error) {
	file, err := os.Open(path)
	if err != nil {
		return tokenUsage{}, err
	}
	defer file.Close()

	var summary tokenUsage
	model := "unknown"
	var previous *tokenFields
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxRolloutLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, []byte(`"token_count"`)) && !bytes.Contains(line, []byte(`"turn_context"`)) {
			continue
		}
		var event rolloutEvent
		if err := json.Unmarshal(line, &event); err != nil {
			summary.Malformed++
			continue
		}
		if event.Type == "turn_context" {
			if event.Payload.Model != "" {
				model = normalizeUsageModel(event.Payload.Model)
			}
			continue
		}
		if event.Type != "event_msg" || event.Payload.Type != "token_count" || event.Payload.Info == nil {
			continue
		}

		info := event.Payload.Info
		priorTotal := previous
		var total *tokenFields
		if info.Total != nil {
			value := info.Total.normalized()
			total = &value
			if previous != nil && value.equal(*previous) {
				continue
			}
			previous = total
		}

		var usage tokenFields
		hasUsage := false
		if info.Last != nil {
			usage = info.Last.normalized()
			hasUsage = true
		} else if total != nil {
			usage = *total
			if priorTotal != nil {
				usage.Input -= priorTotal.Input
				usage.Cached -= priorTotal.Cached
				usage.Output -= priorTotal.Output
				usage.Reasoning -= priorTotal.Reasoning
			}
			if usage.Input < 0 || usage.Cached < 0 || usage.Output < 0 || usage.Reasoning < 0 {
				usage = *total
			}
			usage = usage.normalized()
			hasUsage = true
		}
		if !hasUsage || usage.Input == 0 && usage.Output == 0 {
			continue
		}

		stamp, err := time.Parse(time.RFC3339Nano, event.Timestamp)
		if err != nil {
			summary.Malformed++
			continue
		}
		observedModel := normalizeUsageModel(firstNonEmpty(info.Model, event.Payload.Model, model))
		fingerprint := usageFingerprint(stamp, observedModel, total, usage)
		if _, duplicate := seen[fingerprint]; duplicate {
			continue
		}
		seen[fingerprint] = struct{}{}
		if !window.includes(stamp, now) {
			continue
		}
		summary.Input += usage.Input
		summary.Cached += usage.Cached
		summary.Output += usage.Output
		summary.Reasoning += usage.Reasoning
		summary.Total += usage.Input + usage.Output
		summary.Requests++
		if models != nil {
			modelUsage := models[observedModel]
			modelUsage.Input += usage.Input
			modelUsage.Cached += usage.Cached
			modelUsage.Output += usage.Output
			modelUsage.Reasoning += usage.Reasoning
			modelUsage.Total += usage.Input + usage.Output
			modelUsage.Requests++
			models[observedModel] = modelUsage
		}
		if months != nil {
			month := stamp.In(time.Local).Format("2006-01")
			monthUsage := months[month]
			monthUsage.Input += usage.Input
			monthUsage.Cached += usage.Cached
			monthUsage.Output += usage.Output
			monthUsage.Reasoning += usage.Reasoning
			monthUsage.Total += usage.Input + usage.Output
			monthUsage.Requests++
			months[month] = monthUsage
		}
	}
	if err := scanner.Err(); err != nil {
		return tokenUsage{}, err
	}
	return summary, nil
}

func usageFingerprint(timestamp time.Time, model string, total *tokenFields, usage tokenFields) string {
	totalValue := "null"
	if total != nil {
		totalValue = fmt.Sprintf("%d,%d,%d,%d", total.Input, total.Cached, total.Output, total.Reasoning)
	}
	return fmt.Sprintf("%s|%s|%s|%d,%d,%d,%d", timestamp.UTC().Format(time.RFC3339Nano), model, totalValue, usage.Input, usage.Cached, usage.Output, usage.Reasoning)
}

func normalizeUsageModel(model string) string {
	if model == "gpt-5.6" {
		return "gpt-5.6-sol"
	}
	if model == "" {
		return "unknown"
	}
	return model
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "unknown"
}

func formatTokenCount(value int64) string {
	negative := value < 0
	if negative {
		value = -value
	}
	raw := strconv.FormatInt(value, 10)
	for i := len(raw) - 3; i > 0; i -= 3 {
		raw = raw[:i] + "," + raw[i:]
	}
	if negative {
		return "-" + raw
	}
	return raw
}

func formatTokenHuman(value int64) string {
	negative := value < 0
	abs := value
	if negative {
		abs = -abs
	}
	unit := ""
	divisor := float64(1)
	switch {
	case abs >= 1_000_000_000:
		unit, divisor = "G", 1_000_000_000
	case abs >= 1_000_000:
		unit, divisor = "M", 1_000_000
	case abs >= 1_000:
		unit, divisor = "K", 1_000
	default:
		return formatTokenCount(value)
	}
	formatted := strconv.FormatFloat(float64(abs)/divisor, 'f', 2, 64)
	formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	if negative {
		formatted = "-" + formatted
	}
	return formatted + unit
}
