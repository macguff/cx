package cx

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPeakUsagePricesModelsAndFindsWeeklyWindows(t *testing.T) {
	oldLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = oldLocal })
	stamp := func(value string) time.Time {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	records := []usageRecord{
		{Timestamp: stamp("2026-09-21T01:00:00Z"), Model: "gpt-5.6-sol", Tokens: tokenFields{Input: 1_000_000, Cached: 500_000, Output: 100_000}}, // $4.20
		{Timestamp: stamp("2026-09-21T05:00:00Z"), Model: "gpt-5.6-luna", Tokens: tokenFields{Input: 1_000_000, Output: 1_000_000}},               // $1.40
		{Timestamp: stamp("2026-09-21T06:01:00Z"), Model: "gpt-6-sol", Tokens: tokenFields{Input: 1_000_000}},                                     // $2.00; first event excluded
		{Timestamp: stamp("2026-09-28T02:00:00Z"), Model: "gpt-6-astra", Tokens: tokenFields{Output: 200_000}},                                    // $10.00
		{Timestamp: stamp("2026-09-28T03:00:00Z"), Model: "unknown", Tokens: tokenFields{Output: 1_000_000}},
	}
	top, weeks, unknown := peakUsage(records)
	if len(top) != 2 || len(weeks) != 2 || len(unknown) != 1 || unknown[0] != "unknown" {
		t.Fatalf("unexpected peaks: top=%+v weeks=%+v unknown=%v", top, weeks, unknown)
	}
	if math.Abs(top[0].USD-10) > 1e-9 || math.Abs(top[1].USD-5.6) > 1e-9 {
		t.Fatalf("top cost = %.4f, %.4f; want 10, 5.6", top[0].USD, top[1].USD)
	}
	if weekStart(weeks[0].End).Format("2006-01-02") != "2026-09-28" || math.Abs(weeks[1].USD-5.6) > 1e-9 {
		t.Fatalf("weekly peaks: %+v", weeks)
	}
}

func TestPeakUsageUsesDeduplicatedRolloutEvents(t *testing.T) {
	profile := t.TempDir()
	sessions := filepath.Join(profile, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	events := []any{
		usageContext("gpt-5.6-sol"),
		usageEvent("2026-09-21T01:00:00Z", usageFields(1_000_000, 0, 0, 0), usageFields(1_000_000, 0, 0, 0)),
	}
	writeUsageEvents(t, filepath.Join(sessions, "rollout-a.jsonl"), events)
	writeUsageEvents(t, filepath.Join(sessions, "rollout-copy.jsonl"), events)
	var records []usageRecord
	_, _, _, err := scanProfileUsageDetailedWithRecords(profile, usageAll, time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC), false, false, &records)
	if err != nil {
		t.Fatal(err)
	}
	top, _, _ := peakUsage(records)
	if len(records) != 1 || len(top) != 1 || math.Abs(top[0].USD-4) > 1e-9 {
		t.Fatalf("deduplicated records=%+v peaks=%+v", records, top)
	}
}
