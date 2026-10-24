package daemon

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func TestFailedOneShotResumeRequiresExplicitRetry(t *testing.T) {
	for _, typ := range []string{"at", "after_turn"} {
		t.Run(typ, func(t *testing.T) {
			h := scheduleHarness(t)
			defer h.s.log.Close()
			now := time.Now()
			tr := schedule.Trigger{Type: typ, MaxRuns: 1}
			if typ == "at" {
				tr.At = &now
			}
			task := createTask(t, h, taskSpec(tr), "")
			h.d.tickSchedules(now.Add(time.Second))
			run := readTask(t, h.d, task.ID).Active
			if err := h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: run.RequestID, Status: pb.InputStatus_INPUT_STATUS_FAILED}}}); err != nil {
				t.Fatal(err)
			}
			h.d.tickSchedules(now.Add(time.Second))
			failed := readTask(t, h.d, task.ID)
			before := automaticPlan(failed)
			_, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "resume", TaskId: task.ID}, originLocal)
			if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "run") || !strings.Contains(err.Error(), "update") {
				t.Fatalf("misleading one-shot resume: %v", err)
			}
			got := readTask(t, h.d, task.ID)
			if automaticPlan(got) != before || got.Revision != failed.Revision {
				t.Fatal("rejected resume completed or changed task")
			}
			requestManual(t, h, task.ID)
			if req := h.d.tasks[task.ID].ManualRequest; req == nil || !req.RetryFailed {
				t.Fatal("explicit failed retry lacks request intent")
			}
			h.d.tickSchedules(now.Add(2 * time.Second))
			got = readTask(t, h.d, task.ID)
			if got.Active == nil || !got.Active.Manual || automaticPlan(got) != before {
				t.Fatal("advertised immediate retry was not executable")
			}
			completeTask(h, got.Active, now.Add(3*time.Second))
			h.d.tickSchedules(now.Add(3 * time.Second))
			got = readTask(t, h.d, task.ID)
			if automaticPlan(got) != before || got.Active != nil || len(got.Runs) != 2 || !got.Runs[1].Manual || got.Runs[1].Status != "completed" || got.Dispatches != 1 {
				t.Fatalf("explicit retry changed automatic plan: %+v", got)
			}
		})
	}
}

func TestOneShotPendingBeforeFailureIsNotExplicitRetry(t *testing.T) {
	for _, typ := range []string{"at", "after_turn"} {
		h := scheduleHarness(t)
		now := time.Now()
		tr := schedule.Trigger{Type: typ}
		if typ == "at" {
			tr.At = &now
		}
		task := createTask(t, h, taskSpec(tr), "")
		h.d.tickSchedules(now.Add(time.Second))
		requestManual(t, h, task.ID)
		h.d.endScheduledRun(h.d.tasks[task.ID], now, "failed", "automatic failed")
		h.d.tickSchedules(now)
		got := readTask(t, h.d, task.ID)
		if got.Active != nil || got.ManualRequest != nil || len(got.Runs) != 2 || got.Runs[1].Status != "skipped" || got.Runs[1].Reason != "task_failed_before_manual_dispatch" {
			t.Fatalf("old pending request became a failed-task retry: %+v", got)
		}
		h.s.log.Close()
	}
}

func TestFailedRecurringRunStillRequiresResume(t *testing.T) {
	for _, tr := range []schedule.Trigger{{Type: "interval", IntervalSeconds: 3600}, {Type: "calendar", Time: "23:59", Timezone: "UTC"}, {Type: "after_turn", Repeat: true, DelaySeconds: 60}} {
		h := scheduleHarness(t)
		task := createTask(t, h, taskSpec(tr), "")
		h.d.tasks[task.ID].Status = "failed"
		if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "run", TaskId: task.ID}, originLocal); err == nil {
			t.Fatal("recurring failure accepted one-shot retry")
		}
		if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "resume", TaskId: task.ID}, originLocal); err != nil {
			t.Fatal(err)
		}
		if readTask(t, h.d, task.ID).Status != "scheduled" {
			t.Fatal("recurring resume no longer schedules")
		}
		h.s.log.Close()
	}
}
