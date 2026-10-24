package daemon

import (
	"encoding/json"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func TestScheduleQuotaAndCreationDedupe(t *testing.T) {
	h := scheduleHarness(t)
	for i := 0; i < maxNonTerminalTasks-1; i++ {
		id := fmt.Sprintf("task-%d", i)
		h.d.tasks[id] = &schedule.Task{ID: id, Revision: 1, Status: "paused"}
	}
	spec := taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600})
	task := createTask(t, h, spec, "last-slot")
	raw, _ := json.Marshal(spec)
	request := &pb.ScheduleRequest{Operation: "create", SessionId: h.s.meta.ID, SpecJson: raw, RequestId: "extra"}
	if _, err := h.d.scheduleCommand(request, originLocal); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("over-quota creation: %v", err)
	}
	request.RequestId = "last-slot"
	data, err := h.d.scheduleCommand(request, originLocal)
	if err != nil {
		t.Fatalf("quota broke retry: %v", err)
	}
	var duplicate schedule.Task
	if err := json.Unmarshal(data, &duplicate); err != nil || duplicate.ID != task.ID {
		t.Fatalf("retry identity: %v", err)
	}
	if len(h.d.tasks) != maxNonTerminalTasks {
		t.Fatal("rejected request retained a task")
	}
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "cancel", TaskId: task.ID}, originLocal); err != nil {
		t.Fatal(err)
	}
	createTask(t, h, spec, "freed-slot")
	h.d.tasks[task.ID].Status = "completed"
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "run", TaskId: task.ID}, originLocal); err == nil {
		t.Fatalf("completed task accepted manual run: %v", err)
	}
	if readTask(t, h.d, task.ID).Status != "completed" {
		t.Fatal("rejected reactivation changed task")
	}
}
