package schedule

import (
	"testing"
	"time"
)

func TestCalendarNext(t *testing.T) {
	for _, tc := range []struct {
		zone, clock, after, want string
		days                     []int
	}{
		{"America/New_York", "02:30", "2026-03-08T00:00:00-05:00", "2026-03-08T03:00:00-04:00", nil},
		{"America/New_York", "01:30", "2026-11-01T00:00:00-04:00", "2026-11-01T01:30:00-04:00", nil},
		{"America/New_York", "01:30", "2026-11-01T01:45:00-04:00", "2026-11-02T01:30:00-05:00", nil},
		{"America/Los_Angeles", "01:30", "2027-11-07T00:00:00-07:00", "2027-11-07T01:30:00-07:00", nil},
		{"Europe/Berlin", "02:30", "2027-10-31T00:00:00+02:00", "2027-10-31T02:30:00+02:00", nil},
		{"Asia/Shanghai", "08:00", "2026-10-24T07:59:59+08:00", "2026-10-24T08:00:00+08:00", nil},
		{"Asia/Shanghai", "08:00", "2026-10-23T08:00:00+08:00", "2026-10-26T08:00:00+08:00", []int{1, 2, 3, 4, 5}},
	} {
		t.Run(tc.zone+tc.after, func(t *testing.T) {
			tr := Trigger{Type: "calendar", Time: tc.clock, Timezone: tc.zone, Weekdays: tc.days}
			if err := tr.ValidateCalendar(); err != nil {
				t.Fatal(err)
			}
			after, _ := time.Parse(time.RFC3339, tc.after)
			want, _ := time.Parse(time.RFC3339, tc.want)
			if got := tr.NextCalendar(after); !got.Equal(want) {
				t.Fatalf("got %s want %s", got, want)
			}
		})
	}
}

func TestCalendarRejectsInvalid(t *testing.T) {
	for _, tr := range []Trigger{
		{Type: "calendar", Time: "8:00", Timezone: "Asia/Shanghai"},
		{Type: "calendar", Time: "24:00", Timezone: "Asia/Shanghai"},
		{Type: "calendar", Time: "08:00", Timezone: "Unknown/Place"},
		{Type: "calendar", Time: "08:00", Timezone: "Local"},
		{Type: "calendar", Time: "08:00", Timezone: "Asia/Shanghai", Weekdays: []int{0}},
		{Type: "interval", Time: "08:00"},
		{Type: "interval", IntervalSeconds: 60, JitterMinutes: 5},
		{Type: "calendar", Time: "08:00", Timezone: "Asia/Shanghai", JitterMinutes: -1},
		{Type: "calendar", Time: "23:30", Timezone: "Asia/Shanghai", JitterMinutes: 30},
	} {
		if tr.ValidateCalendar() == nil {
			t.Fatalf("accepted %+v", tr)
		}
	}
}

// Guards the jitter contract the daemon relies on: every path resolves one
// instant per date, inside the window and on that local date, and the minute
// actually moves between days.
func TestCalendarJitter(t *testing.T) {
	tr := Trigger{Type: "calendar", Time: "10:10", Timezone: "Asia/Shanghai", JitterMinutes: 50}
	if err := tr.ValidateCalendar(); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation(tr.Timezone)
	minutes := map[int]bool{}
	after := time.Date(2026, 11, 21, 23, 0, 0, 0, loc)
	for i := 0; i < 60; i++ {
		got := tr.NextCalendar(after)
		local := got.In(loc)
		base := time.Date(local.Year(), local.Month(), local.Day(), 10, 10, 0, 0, loc)
		if got.Before(base) || got.After(base.Add(50*time.Minute)) {
			t.Fatalf("occurrence %s outside window from %s", got, base)
		}
		if latest := tr.LatestCalendar(got.Add(time.Minute)); !latest.Equal(got) {
			t.Fatalf("latest %s, next %s", latest, got)
		}
		if again := tr.CalendarOn(got); !again.Equal(got) {
			t.Fatalf("date resolves to %s and %s", again, got)
		}
		minutes[local.Hour()*60+local.Minute()] = true
		after = got
	}
	if len(minutes) < 10 {
		t.Fatalf("only %d distinct start minutes in 60 days", len(minutes))
	}
	tr.JitterMinutes = 0
	if got := tr.NextCalendar(after); got.In(loc).Format("15:04") != "10:10" {
		t.Fatalf("no jitter moved the time: %s", got)
	}
}
