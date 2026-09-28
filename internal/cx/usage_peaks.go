package cx

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// USD per million text tokens at the standard API rate:
// https://developers.openai.com/api/docs/pricing
// These rates estimate relative usage; Codex subscription limits do not have a
// published USD conversion.
var modelPrices = map[string]struct{ Input, Cached, Output float64 }{
	"gpt-6-astra":   {10, 1, 50},
	"gpt-6-sol":     {2, .2, 10},
	"gpt-6-luna":    {.1, .01, .5},
	"gpt-5.6-sol":   {4, .4, 20},
	"gpt-5.6-terra": {2, .2, 12},
	"gpt-5.6-luna":  {.2, .02, 1.2},
	"gpt-5.3-codex": {1.75, .175, 14},
}

type pricedWindow struct {
	Start  time.Time
	End    time.Time
	USD    float64
	Tokens int64
}

func recordCost(record usageRecord) (float64, bool) {
	price, ok := modelPrices[record.Model]
	if !ok {
		return 0, false
	}
	u := record.Tokens
	return (float64(u.Input-u.Cached)*price.Input + float64(u.Cached)*price.Cached + float64(u.Output)*price.Output) / 1_000_000, true
}

func peakUsage(records []usageRecord) ([]pricedWindow, []pricedWindow, []string) {
	sort.Slice(records, func(i, j int) bool { return records[i].Timestamp.Before(records[j].Timestamp) })
	type pricedRecord struct {
		usageRecord
		USD float64
	}
	priced := make([]pricedRecord, 0, len(records))
	unknown := make(map[string]struct{})
	for _, record := range records {
		cost, ok := recordCost(record)
		if !ok {
			unknown[record.Model] = struct{}{}
			continue
		}
		priced = append(priced, pricedRecord{record, cost})
	}

	weekly := make(map[string]pricedWindow)
	candidates := make([]pricedWindow, 0, len(priced))
	left := 0
	var cost float64
	var tokens int64
	for _, event := range priced {
		cost += event.USD
		tokens += event.Tokens.Input + event.Tokens.Output
		for priced[left].Timestamp.Before(event.Timestamp.Add(-5 * time.Hour)) {
			cost -= priced[left].USD
			tokens -= priced[left].Tokens.Input + priced[left].Tokens.Output
			left++
		}
		window := pricedWindow{Start: event.Timestamp.Add(-5 * time.Hour), End: event.Timestamp, USD: cost, Tokens: tokens}
		candidates = append(candidates, window)
		week := weekStart(event.Timestamp).Format("2006-01-02")
		if previous, ok := weekly[week]; !ok || window.USD > previous.USD {
			weekly[week] = window
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].USD != candidates[j].USD {
			return candidates[i].USD > candidates[j].USD
		}
		return candidates[i].End.Before(candidates[j].End)
	})
	top := make([]pricedWindow, 0, 5)
	for _, candidate := range candidates {
		if len(top) == 5 {
			break
		}
		overlaps := false
		for _, selected := range top {
			if candidate.Start.Before(selected.End) && selected.Start.Before(candidate.End) {
				overlaps = true
				break
			}
		}
		if !overlaps {
			top = append(top, candidate)
		}
	}
	weeks := make([]string, 0, len(weekly))
	for week := range weekly {
		weeks = append(weeks, week)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(weeks)))
	weeklyPeaks := make([]pricedWindow, 0, len(weeks))
	for _, week := range weeks {
		weeklyPeaks = append(weeklyPeaks, weekly[week])
	}
	unknownModels := make([]string, 0, len(unknown))
	for model := range unknown {
		unknownModels = append(unknownModels, model)
	}
	sort.Strings(unknownModels)
	return top, weeklyPeaks, unknownModels
}

func weekStart(stamp time.Time) time.Time {
	local := stamp.In(time.Local)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	daysSinceMonday := (int(day.Weekday()) + 6) % 7
	return day.AddDate(0, 0, -daysSinceMonday)
}

func formatPeak(window pricedWindow) string {
	return fmt.Sprintf("%s to %s  $%.4f  %s tokens", window.Start.In(time.Local).Format("2006-01-02 15:04"), window.End.In(time.Local).Format("2006-01-02 15:04"), window.USD, formatTokenHuman(window.Tokens))
}

func formatUnknownModels(models []string) string {
	return strings.Join(models, ", ")
}
