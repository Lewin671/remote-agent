package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func queuedManualBehindAutomatic(t *testing.T) (*harness, *schedule.Task, *schedule.Run) {
	t.Helper()
	h := scheduleHarness(t)
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600}), "")
	h.d.tickSchedules(time.Now().Add(time.Second))
	run := readTask(t, h.d, task.ID).Active
	requestManual(t, h, task.ID)
	return h, task, run
}
func markUnconfirmed(t *testing.T, h *harness, run *schedule.Run) {
	t.Helper()
	if err := h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: run.RequestID, Status: pb.InputStatus_INPUT_STATUS_UNCONFIRMED}}}); err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(time.Now().Add(time.Second))
}
func TestPendingManualWaitsForAutomaticConfirmation(t *testing.T) {
	for _, status := range []string{"uncertain", "paused", "scheduled", "uncertain-without-cursor"} {
		t.Run(status, func(t *testing.T) {
			h, task, run := queuedManualBehindAutomatic(t)
			defer h.s.log.Close()
			markUnconfirmed(t, h, run)
			state := h.d.tasks[task.ID]
			if state.Active != nil || state.ManualRequest == nil || len(state.Runs) != 1 {
				t.Fatal("manual overlapped unconfirmed automatic input")
			}
			if status == "uncertain-without-cursor" {
				delete(h.d.lateRuns, task.ID)
			} else {
				state.Status = status
			}
			request := state.ManualRequest.RequestID
			before := automaticPlan(state)
			for i := 0; i < 2; i++ {
				h.d.tickSchedules(time.Now().Add(2 * time.Second))
			}
			got := readTask(t, h.d, task.ID)
			if got.Active != nil || got.ManualRequest == nil || got.ManualRequest.RequestID != request || len(got.Runs) != 1 || automaticPlan(got) != before {
				t.Fatalf("manual did not wait: %+v", got)
			}
		})
	}
}
func TestPendingManualSkipsWhenAutomaticStops(t *testing.T) {
	for _, status := range []string{"failed", "completed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			h, task, _ := queuedManualBehindAutomatic(t)
			defer h.s.log.Close()
			state := h.d.tasks[task.ID]
			request := state.ManualRequest.RequestID
			if status == "completed" {
				state.Spec.Trigger.MaxRuns = 1
			}
			h.d.endScheduledRun(state, time.Now(), status, "automatic stopped")
			before := automaticPlan(state)
			h.d.tickSchedules(time.Now())
			got := readTask(t, h.d, task.ID)
			if got.Active != nil || got.ManualRequest != nil || len(got.Runs) != 2 || automaticPlan(got) != before {
				t.Fatalf("stopped task dispatched manual request: %+v", got)
			}
			skipped := got.Runs[1]
			if !skipped.Manual || skipped.RequestID != request || skipped.Status != "skipped" || skipped.Reason != "task_"+status+"_before_manual_dispatch" {
				t.Fatalf("unclear discarded request: %+v", skipped)
			}
		})
	}
}
func TestLateConfirmationFindsRunByID(t *testing.T) {
	for _, tail := range []string{"manual", "skipped"} {
		t.Run(tail, func(t *testing.T) {
			h, task, run := queuedManualBehindAutomatic(t)
			defer h.s.log.Close()
			markUnconfirmed(t, h, run)
			state := h.d.tasks[task.ID]
			state.Runs = appendRun(state.Runs, schedule.Run{ID: "later-record", Manual: tail == "manual", Status: map[string]string{"manual": "completed", "skipped": "skipped"}[tail], ScheduledAt: time.Now()})
			if err := h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: run.RequestID, Status: pb.InputStatus_INPUT_STATUS_SUBMITTED}}}); err != nil {
				t.Fatal(err)
			}
			completeTask(h, run, time.Now())
			h.d.tickSchedules(time.Now())
			got := readTask(t, h.d, task.ID)
			if got.Active == nil || !got.Active.Manual || got.ManualRequest != nil || h.d.lateRuns[task.ID] != "" {
				t.Fatalf("late automatic result never released manual slot: %+v", got)
			}
			completed, kept := false, false
			for _, r := range got.Runs {
				if r.ID == run.ID && r.Status == "completed" {
					completed = true
				}
				if r.ID == "later-record" {
					kept = true
				}
			}
			if !completed || !kept || len(got.Runs) != 2 || got.Dispatches != 1 {
				t.Fatalf("run-ID matching lost history: %+v", got.Runs)
			}
		})
	}
}
func TestLateConfirmationRollbackKeepsOriginalRunID(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task, run := unconfirmedTask(t, h, time.Now().Add(time.Second))
	state := h.d.tasks[task.ID]
	state.Runs = appendRun(state.Runs, schedule.Run{ID: "unrelated-tail", Status: "skipped"})
	if err := h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: run.RequestID, Status: pb.InputStatus_INPUT_STATUS_SUBMITTED}}}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(paths.Dir(), "schedules.json")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "pause", TaskId: task.ID}, originLocal); err == nil {
		t.Fatal("expected persistence failure")
	}
	if h.d.lateRuns[task.ID] != run.ID || h.d.tasks[task.ID].Active != nil || len(h.d.tasks[task.ID].Runs) != 2 {
		t.Fatal("rollback switched late recovery to last unrelated run")
	}
}
