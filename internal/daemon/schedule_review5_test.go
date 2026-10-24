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
