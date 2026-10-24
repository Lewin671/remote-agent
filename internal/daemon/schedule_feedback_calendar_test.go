package daemon

import (
	"context"
	"encoding/json"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"testing"
	"time"
)

func calendarTime(t *testing.T, stamp string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestCalendarCatchupOccupiesOccurrenceDateOnly(t *testing.T) {
	for _, late := range []int64{86400, 60} {
		for _, restart := range []bool{false, true} {
			t.Run(time.Duration(late).String()+map[bool]string{true: "restart", false: "live"}[restart], func(t *testing.T) {
				h := scheduleHarness(t)
				defer h.s.log.Close()
				spec := taskSpec(schedule.Trigger{Type: "calendar", Time: "23:59", Timezone: "Asia/Shanghai"})
				spec.LateSeconds = late
				task := createTask(t, h, spec, "")
				due := calendarTime(t, "2026-10-20T23:59:00+08:00")
				now := calendarTime(t, "2026-10-21T00:03:00+08:00")
				h.d.tasks[task.ID].NextRunAt = &due
				if err := h.d.saveSchedulesLocked(); err != nil {
					t.Fatal(err)
				}
				if restart {
					if err := h.d.loadSchedules(); err != nil {
						t.Fatal(err)
					}
				}
				h.d.tickSchedules(now)
				got := readTask(t, h.d, task.ID)
				if late == 86400 {
					if got.Active == nil || got.LastCalendarDate != "2026-10-20" {
						t.Fatalf("wrong reservation: %+v", got)
					}
					completeTask(h, got.Active, now)
					h.d.tickSchedules(now)
				} else if got.LastCalendarDate != "" {
					t.Fatal("skipped run consumed a date")
				}
				got = readTask(t, h.d, task.ID)
				want := calendarTime(t, "2026-10-21T23:59:00+08:00")
				if got.NextRunAt == nil || !got.NextRunAt.Equal(want) {
					t.Fatalf("today lost: %+v", got)
				}
			})
		}
	}
}

func TestCalendarManualRunPreservesAutomaticOccurrence(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "calendar", Time: "08:00", Timezone: "Asia/Shanghai"}), "")
	now := time.Now()
	due := now.Add(time.Hour)
	h.d.tasks[task.ID].NextRunAt = &due
	_, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "run", TaskId: task.ID}, originLocal)
	if err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(now.Add(time.Second))
	got := readTask(t, h.d, task.ID)
	if got.Active == nil || !got.Active.Manual || got.LastCalendarDate != "" {
		t.Fatalf("manual occupied calendar: %+v", got)
	}
	completeTask(h, got.Active, now.Add(time.Second))
	h.d.tickSchedules(now.Add(time.Second))
	got = readTask(t, h.d, task.ID)
	if got.NextRunAt == nil || !got.NextRunAt.Equal(due) {
		t.Fatalf("manual changed automatic cursor: %+v", got)
	}
	h.d.tickSchedules(due)
	if readTask(t, h.d, task.ID).Active == nil {
		t.Fatal("automatic occurrence lost after manual run")
	}
}

func TestCalendarDeadlineAndEditBoundaries(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	now := time.Now()
	expired := now.Add(-time.Minute)
	spec := taskSpec(schedule.Trigger{Type: "calendar", Time: "23:59", Timezone: "UTC", Until: &expired})
	call := func(op, id string, sp schedule.Spec) error {
		b, _ := json.Marshal(sp)
		_, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: op, TaskId: id, SessionId: h.s.meta.ID, SpecJson: b}, originLocal)
		return err
	}
	if call("create", "", spec) == nil {
		t.Fatal("created with past until")
	}
	spec.Trigger.Until = nil
	task := createTask(t, h, spec, "")
	spec.Trigger.Until = &expired
	if call("update", task.ID, spec) == nil {
		t.Fatal("updated with past until")
	}
	h.d.tasks[task.ID].Spec.Trigger.Until = &expired
	if call("run", task.ID, spec) == nil {
		t.Fatal("ran after until")
	}
	// A changed clock on an occupied date immediately advertises the next date.
	task = h.d.tasks[task.ID]
	task.Spec.Trigger.Until = nil
	task.LastCalendarDate = calendarDate(task.Spec.Trigger, now)
	if err := h.d.prepareSchedule(context.Background(), task, now); err != nil {
		t.Fatal(err)
	}
	if task.NextRunAt == nil || calendarDate(task.Spec.Trigger, *task.NextRunAt) <= task.LastCalendarDate {
		t.Fatalf("stale next occurrence: %+v", task)
	}
}
