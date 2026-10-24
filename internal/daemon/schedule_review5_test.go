package daemon

import (
	"encoding/json"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func TestFailedAtDefinitionUpdateRetriesSameOccurrence(t *testing.T) {
	for _, titleOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "title"}[titleOnly], func(t *testing.T) {
			h := scheduleHarness(t)
			defer h.s.log.Close()
			now := time.Now()
			task := createTask(t, h, taskSpec(schedule.Trigger{Type: "at", At: &now}), "")
			h.d.tickSchedules(now.Add(time.Second))
			first := readTask(t, h.d, task.ID).Active
			if first == nil {
				t.Fatal("first occurrence not dispatched")
			}
			h.d.endScheduledRun(h.d.tasks[task.ID], now.Add(time.Second), "failed", "execution_failed")
			failed := readTask(t, h.d, task.ID)
			spec := failed.Spec
			if titleOnly {
				spec.Title = "Retry check"
			}
			raw, _ := json.Marshal(spec)
			if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "update", TaskId: task.ID, ExpectedRevision: failed.Revision, SpecJson: raw}, originLocal); err != nil {
				t.Fatal(err)
			}
			h.d.tickSchedules(now.Add(2 * time.Second))
			got := readTask(t, h.d, task.ID)
			if got.Active == nil || got.Active.Manual || got.Active.ID == first.ID || !got.Active.ScheduledAt.Equal(first.ScheduledAt) || got.Dispatches != 2 {
				t.Fatalf("updated one-shot silently consumed instead of retried: %+v", got)
			}
			completeTask(h, got.Active, now.Add(3*time.Second))
			h.d.tickSchedules(now.Add(3 * time.Second))
			got = readTask(t, h.d, task.ID)
			if got.Status != "completed" || len(got.Runs) != 2 || got.Runs[1].Status != "completed" {
				t.Fatalf("retry result lost: %+v", got)
			}
		})
	}
}

func TestFailedOneShotRetryOutcome(t *testing.T) {
	for _, typ := range []string{"at", "after_turn"} {
		for _, outcome := range []string{"completed", "failed", "interrupted"} {
			for _, control := range []string{"none", "pause", "cancel"} {
				t.Run(typ+"/"+outcome+"/"+control, func(t *testing.T) {
					h := scheduleHarness(t)
					defer func() { h.s.log.Close() }()
					now := time.Now()
					tr := schedule.Trigger{Type: typ, MaxRuns: 1}
					if typ == "at" {
						tr.At = &now
					}
					task := createTask(t, h, taskSpec(tr), "")
					h.d.tickSchedules(now.Add(time.Second))
					h.d.endScheduledRun(h.d.tasks[task.ID], now, "failed", "original_failure")
					requestManual(t, h, task.ID)
					h.d.tickSchedules(now.Add(time.Second))
					// Replay the accepted run rather than inferring retry intent from task state.
					restartSession(t, h)
					got := readTask(t, h.d, task.ID)
					if got.Active == nil || !got.Active.RetryFailed {
						t.Fatal("retry intent lost on restart")
					}
					run := got.Active
					if control != "none" {
						if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: control, TaskId: task.ID}, originLocal); err != nil {
							t.Fatal(err)
						}
					}
					before := automaticPlan(readTask(t, h.d, task.ID))
					if err := h.s.emit(&pb.Event{TurnId: "retry", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}, &pb.Event{TurnId: "retry", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "Retry check", RequestId: run.RequestID}}}); err != nil {
						t.Fatal(err)
					}
					if outcome == "failed" {
						if err := h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: run.RequestID, Status: pb.InputStatus_INPUT_STATUS_FAILED}}}); err != nil {
							t.Fatal(err)
						}
					} else {
						end := pb.TurnOutcome_TURN_OUTCOME_COMPLETED
						if outcome == "interrupted" {
							end = pb.TurnOutcome_TURN_OUTCOME_ABORTED
						}
						if err := h.s.emit(&pb.Event{TurnId: "retry", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: end}}}); err != nil {
							t.Fatal(err)
						}
					}
					h.d.tickSchedules(now.Add(2 * time.Second))
					got = readTask(t, h.d, task.ID)
					if got.Active != nil || len(got.Runs) != 2 || got.Runs[1].Status != outcome || !got.Runs[1].RetryFailed || got.Dispatches != 1 {
						t.Fatalf("retry result lost: %+v", got)
					}
					if outcome == "completed" && control == "none" {
						if got.Status != "completed" || got.Reason != "explicit_retry_succeeded" || got.NextRunAt != nil {
							t.Fatalf("successful retry still failed: %+v", got)
						}
						if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "run", TaskId: task.ID}, originLocal); err == nil {
							t.Fatal("completed task accepted another retry")
						}
					} else if automaticPlan(got) != before {
						t.Fatalf("non-success retry or explicit control changed plan: %+v", got)
					}
					restartSession(t, h)
					persisted := readTask(t, h.d, task.ID)
					if automaticPlan(persisted) != automaticPlan(got) || persisted.Runs[1].Status != outcome || !persisted.Runs[1].RetryFailed {
						t.Fatal("retry result not durable")
					}
				})
			}
		}
	}
}
