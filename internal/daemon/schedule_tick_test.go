package daemon

import (
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func TestScheduleTickLeavesInertTerminalTasksUntouched(t *testing.T) {
	h := scheduleHarness(t)
	for _, status := range []string{"completed", "cancelled"} {
		// Cloning is unnecessary here; opaque text must not participate in detecting changes.
		h.d.tasks[status] = &schedule.Task{ID: status, Revision: 1, Status: status, Spec: schedule.Spec{Title: "raw\xff"}}
	}
	h.d.syncStateDir = func(string) error { t.Error("idle terminal task caused a write"); return nil }
	h.d.tickSchedules(time.Now())
	for _, task := range h.d.tasks {
		if task.Revision != 1 {
			t.Fatalf("idle task changed: %+v", task)
		}
	}
}

func TestScheduleTickPersistsLateCursorWithoutConfirmation(t *testing.T) {
	h := scheduleHarness(t)
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600}), "")
	now := time.Now().UTC()
	h.d.tickSchedules(now.Add(time.Second))
	task = readTask(t, h.d, task.ID)
	request := task.Active.RequestID
	if err := h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: request, Status: pb.InputStatus_INPUT_STATUS_UNCONFIRMED}}}); err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(now.Add(2 * time.Second))
	task = readTask(t, h.d, task.ID)
	if task.Active != nil || len(task.Runs) != 1 {
		t.Fatalf("not uncertain: %+v", task)
	}
	revision := task.Revision
	if err := h.s.emit(&pb.Event{Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}); err != nil {
		t.Fatal(err)
	}
	seq := h.s.log.LastSeq()
	h.d.tickSchedules(now.Add(3 * time.Second))
	task = readTask(t, h.d, task.ID)
	if task.Runs[0].AfterSeq != seq || task.Revision != revision+1 {
		t.Fatalf("late cursor not saved: %+v", task)
	}
	h.d.tickSchedules(now.Add(4 * time.Second))
	if got := readTask(t, h.d, task.ID); got.Revision != task.Revision {
		t.Fatalf("idle late task changed: %+v", got)
	}
}
