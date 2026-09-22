package cx

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanProfileUsageMatchesRolloutAccounting(t *testing.T) {
	profile := t.TempDir()
	sessions := filepath.Join(profile, "sessions", "2026", "09", "21")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	events := []any{
		usageContext("gpt-5.6"),
		usageEvent("2026-09-21T01:00:00Z", usageFields(100, 80, 10, 4), usageFields(100, 80, 10, 4)),
		usageEvent("2026-09-21T01:01:00Z", usageFields(100, 80, 10, 4), usageFields(100, 80, 10, 4)),
		usageEvent("2026-09-21T02:00:00Z", usageFields(200, 160, 20, 8), nil),
		usageEvent("2026-09-21T03:00:00Z", usageFields(50, 20, 5, 2), nil),
	}
	writeUsageEvents(t, filepath.Join(sessions, "rollout-a.jsonl"), events)
	writeUsageEvents(t, filepath.Join(sessions, "rollout-copy.jsonl"), events)

	summary, err := scanProfileUsage(profile, usageAll, time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 3 || summary.Input != 250 || summary.Cached != 180 || summary.Output != 25 || summary.Reasoning != 10 || summary.Total != 275 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if summary.Files != 2 {
		t.Fatalf("files = %d, want 2", summary.Files)
	}
}

func TestScanProfileUsageFiltersByLocalDayWindow(t *testing.T) {
	oldLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = oldLocal })

	profile := t.TempDir()
	sessions := filepath.Join(profile, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	events := []any{
		usageContext("gpt-5.6-luna"),
		usageEvent("2026-08-01T12:00:00Z", usageFields(100, 0, 10, 0), usageFields(100, 0, 10, 0)),
		usageEvent("2026-09-15T00:00:00Z", usageFields(200, 0, 20, 0), usageFields(100, 0, 10, 0)),
	}
	writeUsageEvents(t, filepath.Join(sessions, "rollout-a.jsonl"), events)
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

	seven, err := scanProfileUsage(profile, usage7Days, now)
	if err != nil {
		t.Fatal(err)
	}
	if seven.Requests != 1 || seven.Total != 110 {
		t.Fatalf("7-day summary: %+v", seven)
	}
	all, err := scanProfileUsage(profile, usageAll, now)
	if err != nil {
		t.Fatal(err)
	}
	if all.Requests != 2 || all.Total != 220 {
		t.Fatalf("all-time summary: %+v", all)
	}
}

func TestScanProfileUsageFiltersByFiveHourWindow(t *testing.T) {
	profile := t.TempDir()
	sessions := filepath.Join(profile, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	writeUsageEvents(t, filepath.Join(sessions, "rollout-a.jsonl"), []any{
		usageEvent("2026-09-21T08:00:00Z", usageFields(100, 0, 10, 0), usageFields(100, 0, 10, 0)),
		usageEvent("2026-09-21T03:00:00Z", usageFields(200, 0, 20, 0), usageFields(100, 0, 10, 0)),
	})
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	summary, err := scanProfileUsage(profile, usage5Hours, now)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 1 || summary.Total != 110 {
		t.Fatalf("5-hour summary: %+v", summary)
	}
}

func TestScanProfileUsageClampsNestedTokenCounts(t *testing.T) {
	profile := t.TempDir()
	sessions := filepath.Join(profile, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	writeUsageEvents(t, filepath.Join(sessions, "rollout-a.jsonl"), []any{
		usageEvent("2026-09-21T01:00:00Z", usageFields(10, 20, 5, 9), usageFields(10, 20, 5, 9)),
	})
	summary, err := scanProfileUsage(profile, usageAll, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Cached != 10 || summary.Reasoning != 5 || summary.Total != 15 {
		t.Fatalf("clamped summary: %+v", summary)
	}
}

func TestFormatTokenHuman(t *testing.T) {
	for value, want := range map[int64]string{
		999:           "999",
		1_000:         "1K",
		11_387_733:    "11.39M",
		1_000_000_000: "1G",
	} {
		if got := formatTokenHuman(value); got != want {
			t.Errorf("formatTokenHuman(%d) = %q, want %q", value, got, want)
		}
	}
}

func TestScanProfileUsageGroupsByModel(t *testing.T) {
	profile := t.TempDir()
	sessions := filepath.Join(profile, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	writeUsageEvents(t, filepath.Join(sessions, "rollout-a.jsonl"), []any{
		usageContext("gpt-5.6"),
		usageEvent("2026-09-21T01:00:00Z", usageFields(100, 20, 10, 4), usageFields(100, 20, 10, 4)),
		usageContext("gpt-5.6-luna"),
		usageEvent("2026-09-21T02:00:00Z", usageFields(300, 40, 30, 8), usageFields(200, 20, 20, 4)),
	})
	models, months, err := scanProfileUsageByModelAndMonth(profile, usageAll, time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got := models["gpt-5.6-sol"].Total; got != 110 {
		t.Fatalf("Sol total = %d, want 110", got)
	}
	if got := models["gpt-5.6-luna"].Total; got != 220 {
		t.Fatalf("Luna total = %d, want 220", got)
	}
	if got := months["2026-09"].Total; got != 330 {
		t.Fatalf("September total = %d, want 330", got)
	}
}

func TestMonthlyUsageSeriesAndTrend(t *testing.T) {
	series := monthlyUsageSeries(map[string]tokenUsage{
		"2026-01": {Total: 100},
		"2026-03": {Total: 300},
	}, time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC))
	if len(series) != 3 || series[1].Month != "2026-02" || series[1].Usage.Total != 0 {
		t.Fatalf("unexpected monthly series: %+v", series)
	}
	if got := monthlySparkline(series); got != "▃▁█" {
		t.Fatalf("sparkline = %q, want %q", got, "▃▁█")
	}
	if got := monthlyChange(100, 150); got != "+50.0%" {
		t.Fatalf("monthly change = %q", got)
	}
	if got := monthlyChange(0, 100); got != "new" {
		t.Fatalf("change from zero = %q", got)
	}
}

func usageContext(model string) map[string]any {
	return map[string]any{"type": "turn_context", "payload": map[string]any{"model": model}}
}

func usageFields(input, cached, output, reasoning int64) map[string]int64 {
	return map[string]int64{
		"input_tokens": input, "cached_input_tokens": cached,
		"output_tokens": output, "reasoning_output_tokens": reasoning,
	}
}

func usageEvent(timestamp string, total, last map[string]int64) map[string]any {
	info := map[string]any{"total_token_usage": total}
	if last != nil {
		info["last_token_usage"] = last
	}
	return map[string]any{
		"timestamp": timestamp, "type": "event_msg",
		"payload": map[string]any{"type": "token_count", "info": info},
	}
}

func writeUsageEvents(t *testing.T, path string, events []any) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
}
