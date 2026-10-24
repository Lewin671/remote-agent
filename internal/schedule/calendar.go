package schedule

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

func (t Trigger) ValidateCalendar() error {
	if t.Type != "calendar" {
		if t.Time != "" || t.Timezone != "" || len(t.Weekdays) != 0 {
			return errors.New("calendar fields require a calendar trigger")
		}
		return nil
	}
	if len(t.Time) != 5 {
		return errors.New("calendar time must be HH:MM")
	}
	if _, err := time.Parse("15:04", t.Time); err != nil {
		return errors.New("calendar time must be HH:MM")
	}
	if t.Timezone == "" || t.Timezone == "Local" {
		return errors.New("calendar requires an IANA timezone")
	}
	if _, err := time.LoadLocation(t.Timezone); err != nil {
		return errors.New("unknown calendar timezone")
	}
	for _, day := range t.Weekdays {
		if day < 1 || day > 7 {
			return errors.New("weekdays must be 1 through 7")
		}
	}
	if t.At != nil || t.DelaySeconds != 0 || t.IntervalSeconds != 0 || t.Repeat {
		return errors.New("calendar does not accept at, delay, interval or repeat")
	}
	return nil
}

// CalendarOn resolves a local date to one instant. Scan actual instants in UTC
// order: a fold chooses its first occurrence, and a gap chooses the first
// existing wall minute after the requested time. No reliance on time.Date's
// unspecified choice for ambiguous or nonexistent local times.
func (t Trigger) CalendarOn(date time.Time) time.Time {
	loc, err := time.LoadLocation(t.Timezone)
	if err != nil {
		return time.Time{}
	}
	y, m, d := date.In(loc).Date()
	var hour, minute int
	if _, err := fmt.Sscanf(t.Time, "%d:%d", &hour, &minute); err != nil {
		return time.Time{}
	}
	weekday := int(time.Date(y, m, d, 12, 0, 0, 0, loc).Weekday())
	if weekday == 0 {
		weekday = 7
	}
	if len(t.Weekdays) > 0 && !slices.Contains(t.Weekdays, weekday) {
		return time.Time{}
	}
	base := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	for at, end := base.Add(-24*time.Hour), base.Add(48*time.Hour); at.Before(end); at = at.Add(time.Minute) {
		wall := at.In(loc)
		wy, wm, wd := wall.Date()
		if wy == y && wm == m && wd == d && wall.Hour()*60+wall.Minute() >= hour*60+minute {
			return at
		}
	}
	return time.Time{} // A local date can be skipped entirely (e.g. dateline changes).
}

// NextCalendar returns the first occurrence strictly after after.
func (t Trigger) NextCalendar(after time.Time) time.Time {
	loc, err := time.LoadLocation(t.Timezone)
	if err != nil {
		return time.Time{}
	}
	date := after.In(loc)
	// An entire local date may be absent; two weeks covers a skipped weekday.
	for i := 0; i < 15; i++ {
		at := t.CalendarOn(date)
		if !at.IsZero() && at.After(after) {
			return at
		}
		y, m, d := date.Date()
		date = time.Date(y, m, d+1, 12, 0, 0, 0, loc)
	}
	return time.Time{}
}

// LatestCalendar catches up only the most recent occurrence, never a backlog.
func (t Trigger) LatestCalendar(now time.Time) time.Time {
	loc, err := time.LoadLocation(t.Timezone)
	if err != nil {
		return time.Time{}
	}
	date := now.In(loc)
	for i := 0; i < 15; i++ {
		at := t.CalendarOn(date)
		if !at.IsZero() && !at.After(now) {
			return at
		}
		y, m, d := date.Date()
		date = time.Date(y, m, d-1, 12, 0, 0, 0, loc)
	}
	return time.Time{}
}
