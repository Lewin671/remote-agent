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
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "calendar", Time: "08:00", Timezone: "Asia/Shanghai", MaxRuns: 1}), "")
	now := time.Now()
	due := now.Add(time.Hour)
	h.d.tasks[task.ID].NextRunAt = &due
	_, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "run", TaskId: task.ID}, originLocal)
	if err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(now.Add(time.Second))
	got := readTask(t, h.d, task.ID)
	if got.Active == nil || !got.Active.Manual || got.LastCalendarDate != "" || got.Dispatches != 0 {
		t.Fatalf("manual occupied calendar: %+v", got)
	}
	completeTask(h, got.Active, now.Add(time.Second))
	h.d.tickSchedules(now.Add(time.Second))
	got = readTask(t, h.d, task.ID)
	if got.NextRunAt == nil || !got.NextRunAt.Equal(due) {
		t.Fatalf("manual changed automatic cursor: %+v", got)
	}
	h.d.tickSchedules(due)
	got = readTask(t, h.d, task.ID)
	if got.Active == nil {
		t.Fatal("automatic occurrence lost after manual run")
	}
	completeTask(h, got.Active, due)
	h.d.tickSchedules(due)
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "run", TaskId: task.ID}, originLocal); err == nil {
		t.Fatal("completed task accepted manual request")
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

func TestRecurringResumeAndManualFailures(t *testing.T) {
	for _, typ := range []string{"interval", "calendar"} {
		for _, limit := range []int64{0, 1} {
			h := scheduleHarness(t)
			tr := schedule.Trigger{Type: typ, MaxRuns: limit}
			if typ == "calendar" {
				tr.Time, tr.Timezone = "08:00", "UTC"
			} else {
				tr.IntervalSeconds = 300
			}
			spec := taskSpec(tr)
			task := createTask(t, h, spec, "")
			state := h.d.tasks[task.ID]
			state.Status, state.Reason, state.NextRunAt = "paused", "execution_interrupted", nil
			state.Dispatches = 1
			_, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "resume", TaskId: task.ID}, originLocal)
			if err != nil {
				t.Fatal(err)
			}
			got := readTask(t, h.d, task.ID)
			if limit == 0 && (got.Status != "scheduled" || got.NextRunAt == nil || !got.NextRunAt.After(time.Now())) {
				t.Fatalf("recurring resume lost: %+v", got)
			}
			if limit == 1 && (got.Status != "completed" || got.Reason != "run_limit_reached") {
				t.Fatalf("limit ignored: %+v", got)
			}
			h.s.log.Close()
		}
	}
	for _, status := range []string{"completed", "failed", "interrupted", "uncertain", "cancelled"} {
		for _, initial := range []string{"scheduled", "paused"} {
			t.Run(status+initial, func(t *testing.T) {
				h := scheduleHarness(t)
				defer h.s.log.Close()
				task := createTask(t, h, taskSpec(schedule.Trigger{Type: "calendar", Time: "08:00", Timezone: "UTC"}), "")
				state := h.d.tasks[task.ID]
				due := time.Now().Add(time.Hour)
				state.Status, state.Reason, state.NextRunAt = initial, "original", &due
				if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "run", TaskId: task.ID}, originLocal); err != nil {
					t.Fatal(err)
				}
				h.d.tickSchedules(time.Now().Add(time.Second))
				state = h.d.tasks[task.ID]
				if state.Active == nil {
					t.Fatal("trial missing")
				}
				h.d.endScheduledRun(state, time.Now(), status, "trial result")
				if state.Status != initial || state.Reason != "original" || state.NextRunAt == nil || !state.NextRunAt.Equal(due) || state.Dispatches != 0 {
					t.Fatalf("trial changed automatic state: %+v", state)
				}
				if state.Runs[len(state.Runs)-1].Status != status {
					t.Fatal("trial result lost")
				}
			})
		}
	}
}

func TestSkippedManualTrialRestoresAutomaticDispatch(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	spec := taskSpec(schedule.Trigger{Type: "calendar", Time: "08:00", Timezone: "UTC", MaxRuns: 1})
	spec.LateSeconds = 60
	task := createTask(t, h, spec, "")
	due := time.Now().Add(time.Hour)
	state := h.d.tasks[task.ID]
	state.NextRunAt = &due
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "run", TaskId: task.ID}, originLocal); err != nil {
		t.Fatal(err)
	}
	late := time.Now().Add(-2 * time.Minute)
	state.ManualRequest.RequestedAt = late
	h.d.tickSchedules(time.Now())
	got := readTask(t, h.d, task.ID)
	if got.ManualRequest != nil || len(got.Runs) != 1 || !got.Runs[0].Manual || got.Runs[0].Status != "skipped" || got.NextRunAt == nil || !got.NextRunAt.Equal(due) {
		t.Fatalf("skipped trial leaked flag: %+v", got)
	}
	h.d.tickSchedules(due)
	got = readTask(t, h.d, task.ID)
	if got.Active == nil || got.Active.Manual || got.Dispatches != 1 || got.LastCalendarDate != calendarDate(spec.Trigger, due) {
		t.Fatalf("automatic treated as manual: %+v", got)
	}
	completeTask(h, got.Active, due)
	h.d.tickSchedules(due)
	if readTask(t, h.d, task.ID).Status != "completed" {
		t.Fatal("automatic cap bypassed")
	}
}

func TestManualTrialFailureBoundaries(t *testing.T) {
	for _, ending := range []string{"dispatch_failed", "input_cancelled", "unconfirmed", "interrupted", "user_paused", "user_cancelled"} {
		t.Run(ending, func(t *testing.T) {
			h := scheduleHarness(t)
			defer h.s.log.Close()
			now := time.Now()
			due := now.Add(time.Hour)
			task := createTask(t, h, taskSpec(schedule.Trigger{Type: "calendar", Time: "08:00", Timezone: "UTC"}), "")
			h.d.tasks[task.ID].NextRunAt = &due
			if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "run", TaskId: task.ID}, originLocal); err != nil {
				t.Fatal(err)
			}
			if ending == "dispatch_failed" {
				snapshot := h.d.prepareScheduledDispatches(now.Add(time.Second))[0]
				h.d.finishScheduledDispatch(context.Background(), snapshot, now, "could_not_start_session", nil)
			} else {
				h.d.tickSchedules(now.Add(time.Second))
				run := readTask(t, h.d, task.ID).Active
				if ending == "interrupted" {
					h.s.emit(&pb.Event{TurnId: "timed", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}, &pb.Event{TurnId: "timed", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{RequestId: run.RequestID}}})
					endTimed(t, h, pb.TurnOutcome_TURN_OUTCOME_ABORTED)
				} else {
					h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: run.RequestID, Status: pb.InputStatus_INPUT_STATUS_QUEUED, Paused: true}}})
					h.d.tickSchedules(now.Add(time.Second))
					if ending == "user_paused" || ending == "user_cancelled" {
						op := "pause"
						if ending == "user_cancelled" {
							op = "cancel"
						}
						if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: op, TaskId: task.ID}, originLocal); err != nil {
							t.Fatal(err)
						}
					}
					status := pb.InputStatus_INPUT_STATUS_CANCELED
					if ending == "unconfirmed" {
						status = pb.InputStatus_INPUT_STATUS_UNCONFIRMED
					}
					h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: run.RequestID, Status: status}}})
				}
				h.d.tickSchedules(now.Add(time.Second))
			}
			got := readTask(t, h.d, task.ID)
			want := "scheduled"
			reason := ""
			if ending == "user_paused" {
				want, reason = "paused", "user_paused"
			}
			if ending == "user_cancelled" {
				if got.Active != nil || got.Status != "cancelled" || got.Reason != "user_cancelled" || got.NextRunAt != nil {
					t.Fatalf("manual result undid explicit cancellation: %+v", got)
				}
				return
			}
			if got.Active != nil || got.Status != want || got.Reason != reason || got.NextRunAt == nil || !got.NextRunAt.Equal(due) || len(got.Runs) != 1 {
				t.Fatalf("manual ending changed automatic plan: %+v", got)
			}
			if h.d.lateRuns[task.ID] != "" {
				t.Fatal("uncertain manual trial can reclaim automatic state")
			}
		})
	}
}
