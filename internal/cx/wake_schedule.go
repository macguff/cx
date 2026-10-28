package cx

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type wakeSchedule struct {
	Kind     string    `json:"kind"`
	At       time.Time `json:"at,omitempty"`
	Times    []string  `json:"times,omitempty"`
	Every    string    `json:"every,omitempty"`
	Timezone string    `json:"timezone"`
}

func (s wakeSchedule) location() (*time.Location, error) {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", s.Timezone, err)
	}
	return loc, nil
}

func (s wakeSchedule) validate() error {
	if _, err := s.location(); err != nil {
		return err
	}
	switch s.Kind {
	case "once":
		if s.At.IsZero() {
			return errors.New("one-time wake-up requires a time")
		}
	case "daily", "weekdays":
		if len(s.Times) == 0 {
			return errors.New("schedule requires at least one HH:MM time")
		}
		seen := make(map[string]bool)
		for _, value := range s.Times {
			parsed, err := time.Parse("15:04", value)
			if err != nil || parsed.Format("15:04") != value || seen[value] {
				return fmt.Errorf("invalid or duplicate time %q; use HH:MM", value)
			}
			seen[value] = true
		}
	case "interval":
		d, err := time.ParseDuration(s.Every)
		if err != nil || d < time.Minute || d > 365*24*time.Hour {
			return errors.New("interval must be between 1m and 8760h (for example, 5h or 5h1m)")
		}
	default:
		return fmt.Errorf("unknown wake-up schedule %q", s.Kind)
	}
	return nil
}

// next returns a time strictly after 'after'. Intervals stay anchored to creation
// time; late executions and manual runs do not move the recurring schedule.
func (s wakeSchedule) next(after, anchor time.Time) time.Time {
	switch s.Kind {
	case "once":
		if s.At.After(after) {
			return s.At
		}
		return time.Time{}
	case "interval":
		d, _ := time.ParseDuration(s.Every) // validated when loading or creating tasks
		first := anchor.Add(d)
		if first.After(after) {
			return first
		}
		return after.Add(d - after.Sub(anchor)%d)
	default:
		loc, _ := s.location()
		year, month, day := after.In(loc).Date()
		var next time.Time
		for offset := 0; offset < 9; offset++ {
			date := time.Date(year, month, day+offset, 12, 0, 0, 0, loc)
			if s.Kind == "weekdays" && (date.Weekday() == time.Saturday || date.Weekday() == time.Sunday) {
				continue
			}
			for _, clock := range s.Times {
				t, _ := time.Parse("15:04", clock)
				candidate := time.Date(date.Year(), date.Month(), date.Day(), t.Hour(), t.Minute(), 0, 0, loc)
				// Skip nonexistent wall times during a daylight-saving transition.
				if candidate.Format("15:04") != clock || !candidate.After(after) {
					continue
				}
				if next.IsZero() || candidate.Before(next) {
					next = candidate
				}
			}
			if !next.IsZero() {
				return next
			}
		}
	}
	return time.Time{}
}

// latestDue coalesces an offline backlog into its most recent slot. A computer
// that resumes at today's scheduled time can still run today, without replaying
// yesterday's requests or shifting the interval anchor.
func (s wakeSchedule) latestDue(now, anchor time.Time) time.Time {
	switch s.Kind {
	case "once":
		return s.At
	case "interval":
		d, _ := time.ParseDuration(s.Every)
		return now.Add(-now.Sub(anchor) % d)
	default:
		loc, _ := s.location()
		year, month, day := now.In(loc).Date()
		for offset := 0; offset < 9; offset++ {
			date := time.Date(year, month, day-offset, 12, 0, 0, 0, loc)
			if s.Kind == "weekdays" && (date.Weekday() == time.Saturday || date.Weekday() == time.Sunday) {
				continue
			}
			var latest time.Time
			for _, clock := range s.Times {
				t, _ := time.Parse("15:04", clock)
				candidate := time.Date(date.Year(), date.Month(), date.Day(), t.Hour(), t.Minute(), 0, 0, loc)
				if candidate.Format("15:04") == clock && !candidate.After(now) && (latest.IsZero() || candidate.After(latest)) {
					latest = candidate
				}
			}
			if !latest.IsZero() {
				return latest
			}
		}
	}
	return time.Time{}
}

func (s wakeSchedule) String() string {
	switch s.Kind {
	case "once":
		return "once"
	case "interval":
		return "every " + s.Every
	default:
		return s.Kind + " " + strings.Join(s.Times, ",") + " (" + s.Timezone + ")"
	}
}

func wakeTimes(value string) []string {
	values := strings.Split(value, ",")
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
	}
	sort.Strings(values)
	return values
}

func parseWakeAt(value, timezone string, now time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timezone %q", timezone)
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", value, loc); err == nil && t.Format("2006-01-02 15:04") == value {
		return t, nil
	}
	s := wakeSchedule{Kind: "daily", Times: []string{value}, Timezone: timezone}
	if err := s.validate(); err != nil {
		return time.Time{}, errors.New("use HH:MM, 'YYYY-MM-DD HH:MM', or RFC3339 for --at")
	}
	return s.next(now, now), nil
}
