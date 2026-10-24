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

func TestNonCompletedScheduledFollowups(t *testing.T) {
	for _, status := range []string{"interrupted", "failed"} {
		for _, terminal := range []bool{false, true} {
			for _, boundary := range []string{"client", "local", "resume", "exit", "scheduled"} {
				t.Run(status+map[bool]string{false: "/paused", true: "/cancelled"}[terminal]+"/"+boundary, func(t *testing.T) {
					h := scheduleHarness(t)
					defer func() { h.s.log.Close() }()
					task := startScheduledTurn(t, h)
					if status == "interrupted" {
						endTimed(t, h, pb.TurnOutcome_TURN_OUTCOME_ABORTED)
					} else if err := h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: task.Active.RequestID, Status: pb.InputStatus_INPUT_STATUS_FAILED}}}); err != nil {
						t.Fatal(err)
					}
					h.d.tickSchedules(time.Now())
					if terminal {
						if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "cancel", TaskId: task.ID}, originLocal); err != nil {
							t.Fatal(err)
						}
					}
					restartSession(t, h)
					before := readTask(t, h.d, task.ID)
					revision := h.d.scheduleRevision
					if len(before.Runs) != 1 || before.Runs[0].Status != status || !hasPendingScheduledFollowups(before) {
						t.Fatalf("ended context no longer tracked: %+v", before)
					}
					for i := 1; i <= 2; i++ {
						if err := h.s.emit(&pb.Event{TurnId: "background", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}); err != nil {
							t.Fatal(err)
						}
						if err := notifyTest(t, h, "background result", ""); err != nil {
							t.Fatal(err)
						}
						if err := h.s.emit(&pb.Event{TurnId: "background", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "## Background summary"}}}, &pb.Event{TurnId: "background", Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED}}}); err != nil {
							t.Fatal(err)
						}
						h.d.tickSchedules(time.Now())
						got := readTask(t, h.d, task.ID)
						r := got.Runs[0]
						if r.ID != task.Active.ID || r.Status != status || !r.Notified || r.NotificationCount != i || r.NotificationText != "background result" || r.FinalSummary != "Background summary" || automaticPlan(got) != automaticPlan(before) {
							t.Fatalf("background result lost or changed original outcome: %+v", got)
						}
						if got.Revision <= before.Revision || h.d.scheduleRevision <= revision {
							t.Fatal("followup did not publish visible revisions")
						}
						revision = h.d.scheduleRevision
						before = got
						h.d.tickSchedules(time.Now())
						if h.d.scheduleRevision != revision || readTask(t, h.d, task.ID).Runs[0].NotificationCount != i {
							t.Fatal("followup replay double counted or republished")
						}
						restartSession(t, h)
						if got := readTask(t, h.d, task.ID); got.Revision != before.Revision || got.Runs[0].NotificationCount != i || got.Runs[0].FinalSummary != "Background summary" {
							t.Fatal("followup result not durable")
						}
					}
					var closeEvent *pb.Event
					switch boundary {
					case "client", "local":
						origin := originLocal
						if boundary == "client" {
							origin = &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT}
						}
						closeEvent = withOrigin(&pb.Event{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "human takes over"}}}, origin)
					case "resume":
						closeEvent = withOrigin(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{Status: pb.SessionStatus_SESSION_STATUS_STARTING, Detail: "resumed"}}}, originLocal)
					case "exit":
						closeEvent = &pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{Status: pb.SessionStatus_SESSION_STATUS_EXITED}}}
					case "scheduled":
						closeEvent = withOrigin(&pb.Event{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "next scheduled input"}}}, &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_SYSTEM, ScheduledTaskId: "another-task", ScheduledRunId: "another-run"})
					}
					// Even a late event naming the old ID cannot reopen a closed context.
					if err := h.s.emit(closeEvent, &pb.Event{Kind: &pb.Event_ScheduledNotification{ScheduledNotification: &pb.ScheduledNotification{TaskId: task.ID, RunId: task.Active.ID, Text: "after context ended"}}}, &pb.Event{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "unrelated final"}}}); err != nil {
						t.Fatal(err)
					}
					h.d.tickSchedules(time.Now())
					got := readTask(t, h.d, task.ID)
					if !got.Runs[0].FollowupsClosed || hasPendingScheduledFollowups(got) || got.Runs[0].NotificationCount != 2 || got.Runs[0].FinalSummary != "Background summary" || got.Revision != before.Revision || h.d.scheduleRevision != revision {
						t.Fatalf("context boundary did not stop tracking: %+v", got)
					}
					restartSession(t, h)
					if hasPendingScheduledFollowups(readTask(t, h.d, task.ID)) {
						t.Fatal("closed context reopened on restart")
					}
				})
			}
		}
	}
}
