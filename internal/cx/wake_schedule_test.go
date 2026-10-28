package cx

import (
	"testing"
	"time"
)

func wakeTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestWakeNextSlots(t *testing.T) {
	tests := []struct {
		name, kind, zone, after, anchor, at, every, want string
		times                                            []string
	}{
		{name: "daily multiple times", kind: "daily", zone: "Asia/Shanghai", times: []string{"07:00", "12:01", "17:02"}, after: "2026-10-28T12:01:00+08:00", want: "2026-10-28T17:02:00+08:00"},
		{name: "daily next day", kind: "daily", zone: "Asia/Shanghai", times: []string{"07:00"}, after: "2026-10-28T07:00:01+08:00", want: "2026-10-29T07:00:00+08:00"},
		{name: "weekend", kind: "weekdays", zone: "Asia/Shanghai", times: []string{"07:00"}, after: "2026-10-30T07:00:00+08:00", want: "2026-11-02T07:00:00+08:00"},
		{name: "time zone", kind: "daily", zone: "America/New_York", times: []string{"09:00"}, after: "2026-10-28T07:00:00Z", want: "2026-10-28T09:00:00-04:00"},
		{name: "DST spring skips missing time", kind: "daily", zone: "America/New_York", times: []string{"02:30"}, after: "2026-03-08T00:00:00-05:00", want: "2026-03-09T02:30:00-04:00"},
		{name: "interval first", kind: "interval", zone: "UTC", every: "5h1m", after: "2026-10-28T00:00:00Z", want: "2026-10-28T05:01:00Z"},
		{name: "interval anchored after late run", kind: "interval", zone: "UTC", every: "5h", anchor: "2026-10-28T00:00:00Z", after: "2026-10-28T10:04:00Z", want: "2026-10-28T15:00:00Z"},
		{name: "interval no backlog", kind: "interval", zone: "UTC", every: "5h", anchor: "2026-10-01T00:00:00Z", after: "2026-10-28T00:00:00Z", want: "2026-10-28T02:00:00Z"},
		{name: "one time", kind: "once", zone: "UTC", at: "2026-10-28T07:00:00Z", after: "2026-10-28T00:00:00Z", want: "2026-10-28T07:00:00Z"},
		{name: "one time consumed", kind: "once", zone: "UTC", at: "2026-10-28T07:00:00Z", after: "2026-10-28T07:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := wakeSchedule{Kind: tt.kind, Timezone: tt.zone, Times: tt.times, Every: tt.every}
			if tt.at != "" {
				s.At = wakeTime(t, tt.at)
			}
			if err := s.validate(); err != nil {
				t.Fatal(err)
			}
			after := wakeTime(t, tt.after)
			anchor := after
			if tt.anchor != "" {
				anchor = wakeTime(t, tt.anchor)
			}
			got := s.next(after, anchor)
			if tt.want == "" {
				if !got.IsZero() {
					t.Fatalf("next = %s, want no future slot", got)
				}
			} else if !got.Equal(wakeTime(t, tt.want)) {
				t.Fatalf("next = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestWakeScheduleValidation(t *testing.T) {
	for _, s := range []wakeSchedule{
		{Kind: "interval", Every: "0s", Timezone: "UTC"},
		{Kind: "interval", Every: "-5h", Timezone: "UTC"},
		{Kind: "interval", Every: "30s", Timezone: "UTC"},
		{Kind: "interval", Every: "9000h", Timezone: "UTC"},
		{Kind: "daily", Times: []string{"25:00"}, Timezone: "UTC"},
		{Kind: "daily", Times: []string{"7:00"}, Timezone: "UTC"},
		{Kind: "daily", Times: []string{"07:00", "07:00"}, Timezone: "UTC"},
		{Kind: "daily", Timezone: "UTC"},
		{Kind: "daily", Times: []string{"07:00"}, Timezone: "Mars/Olympus"},
		{Kind: "once", Timezone: "UTC"},
		{Kind: "other", Timezone: "UTC"},
	} {
		if err := s.validate(); err == nil {
			t.Errorf("accepted invalid schedule: %#v", s)
		}
	}
}

func TestParseWakeAt(t *testing.T) {
	now := wakeTime(t, "2026-10-28T14:00:00+08:00")
	for _, tt := range []struct{ input, want string }{
		{"07:00", "2026-10-29T07:00:00+08:00"},
		{"2026-10-30 07:00", "2026-10-30T07:00:00+08:00"},
		{"2026-10-30T00:00:00Z", "2026-10-30T08:00:00+08:00"},
	} {
		got, err := parseWakeAt(tt.input, "Asia/Shanghai", now)
		if err != nil || !got.Equal(wakeTime(t, tt.want)) {
			t.Fatalf("parse %q = %s, %v; want %s", tt.input, got, err, tt.want)
		}
	}
	for _, value := range []string{"tomorrow", "24:00", "2026-02-30 07:00"} {
		if _, err := parseWakeAt(value, "UTC", now); err == nil {
			t.Errorf("accepted invalid time %q", value)
		}
	}
	if _, err := parseWakeAt("2026-03-08 02:30", "America/New_York", now); err == nil {
		t.Fatal("accepted nonexistent local time")
	}
}
