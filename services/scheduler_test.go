package services

import (
	"testing"
	"time"

	"cloudnest/internal/dto"
)

func TestScheduleDue(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 5, 10, 10, 0, 0, 0, loc)
	yesterday := now.AddDate(0, 0, -1)
	today := now
	at := func(day time.Time, hour, minute int) int64 {
		return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, loc).Unix()
	}
	base := func() dto.Schedule {
		return dto.Schedule{
			Enabled:     true,
			SourcePaths: []string{`C:\Users\me\Documents`},
			Time:        "09:00",
		}
	}

	tests := []struct {
		name string
		sch  dto.Schedule
		now  time.Time
		want bool
	}{
		{
			name: "disabled",
			sch:  func() dto.Schedule { s := base(); s.Enabled = false; return s }(),
			now:  now,
			want: false,
		},
		{
			name: "empty sources",
			sch:  func() dto.Schedule { s := base(); s.SourcePaths = nil; return s }(),
			now:  now,
			want: false,
		},
		{
			name: "empty time",
			sch:  func() dto.Schedule { s := base(); s.Time = ""; return s }(),
			now:  now,
			want: false,
		},
		{
			name: "invalid time",
			sch:  func() dto.Schedule { s := base(); s.Time = "25:99"; return s }(),
			now:  now,
			want: false,
		},
		{
			name: "invalid format",
			sch:  func() dto.Schedule { s := base(); s.Time = "nine"; return s }(),
			now:  now,
			want: false,
		},
		{
			name: "after scheduled time never run",
			sch:  base(),
			now:  now,
			want: true,
		},
		{
			name: "before scheduled time never run",
			sch:  base(),
			now:  now.Add(-2 * time.Hour), // 08:00; point is yesterday 09:00
			want: true,
		},
		{
			name: "already run today",
			sch:  func() dto.Schedule { s := base(); s.LastRun = at(today, 9, 30); return s }(),
			now:  now,
			want: false,
		},
		{
			name: "ran yesterday before today's point while clock is early",
			sch:  func() dto.Schedule { s := base(); s.LastRun = at(yesterday, 9, 30); return s }(),
			now:  now.Add(-2 * time.Hour),
			want: false,
		},
		{
			name: "missed yesterday's point",
			sch:  func() dto.Schedule { s := base(); s.LastRun = at(yesterday, 8, 0); return s }(),
			now:  now.Add(-2 * time.Hour),
			want: true,
		},
		{
			name: "exactly at scheduled time, not yet run",
			sch:  func() dto.Schedule { s := base(); s.LastRun = at(yesterday, 10, 0); return s }(),
			now:  time.Date(2026, 5, 10, 9, 0, 0, 0, loc),
			want: true,
		},
		{
			name: "exactly at scheduled time, already run",
			sch:  func() dto.Schedule { s := base(); s.LastRun = at(today, 9, 0); return s }(),
			now:  time.Date(2026, 5, 10, 9, 0, 0, 0, loc),
			want: false,
		},
		{
			name: "whitespace around time is accepted",
			sch:  func() dto.Schedule { s := base(); s.Time = " 09:00 "; return s }(),
			now:  now,
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scheduleDue(tc.sch, tc.now); got != tc.want {
				t.Fatalf("scheduleDue(%+v, %s) = %v, want %v", tc.sch, tc.now, got, tc.want)
			}
		})
	}
}

func TestLastScheduledPoint(t *testing.T) {
	loc := time.UTC
	tests := []struct {
		name  string
		value string
		now   time.Time
		want  time.Time
		ok    bool
	}{
		{
			name:  "today when the time has passed",
			value: "09:00",
			now:   time.Date(2026, 1, 2, 10, 15, 0, 0, loc),
			want:  time.Date(2026, 1, 2, 9, 0, 0, 0, loc),
			ok:    true,
		},
		{
			name:  "yesterday when the time is still ahead",
			value: "09:00",
			now:   time.Date(2026, 1, 2, 8, 15, 0, 0, loc),
			want:  time.Date(2026, 1, 1, 9, 0, 0, 0, loc),
			ok:    true,
		},
		{
			name:  "exactly at the time stays today",
			value: "09:00",
			now:   time.Date(2026, 1, 2, 9, 0, 0, 0, loc),
			want:  time.Date(2026, 1, 2, 9, 0, 0, 0, loc),
			ok:    true,
		},
		{
			name:  "midnight",
			value: "00:00",
			now:   time.Date(2026, 1, 2, 0, 0, 0, 0, loc),
			want:  time.Date(2026, 1, 2, 0, 0, 0, 0, loc),
			ok:    true,
		},
		{
			name:  "invalid time",
			value: "24:00",
			now:   time.Date(2026, 1, 2, 10, 0, 0, 0, loc),
			ok:    false,
		},
		{
			name:  "empty time",
			value: "",
			now:   time.Date(2026, 1, 2, 10, 0, 0, 0, loc),
			ok:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lastScheduledPoint(tc.value, tc.now)
			if ok != tc.ok {
				t.Fatalf("lastScheduledPoint(%q) ok = %v, want %v", tc.value, ok, tc.ok)
			}
			if ok && !got.Equal(tc.want) {
				t.Fatalf("lastScheduledPoint(%q) = %s, want %s", tc.value, got, tc.want)
			}
		})
	}
}
