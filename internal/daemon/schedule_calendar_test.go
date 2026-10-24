package daemon

import (
	"github.com/Lewin671/remote-agent/internal/schedule"
	"testing"
	"time"
)

func TestScheduleUntil(t *testing.T) {
	for _, typ := range []string{"interval", "after_turn", "calendar"} {
		t.Run(typ, func(t *testing.T) {
			h := scheduleHarness(t)
			defer h.s.log.Close()
			now := time.Now().UTC()
			until := now.Add(time.Minute)
			tr := schedule.Trigger{Type: typ, Until: &until}
			switch typ {
			case "interval":
				tr.IntervalSeconds = 30
			case "after_turn":
				tr.Repeat = true
				tr.DelaySeconds = 30
			case "calendar":
				tr.Time = now.Add(24 * time.Hour).Format("15:04")
				tr.Timezone = "UTC"
			}
			task := createTask(t, h, taskSpec(tr), "")
			h.d.tickSchedules(until.Add(time.Second))
			got := readTask(t, h.d, task.ID)
			if got.Dispatches != 0 || got.Status != "completed" || got.NextRunAt != nil {
				t.Fatalf("expired task: %+v", got)
			}
			// nextSchedule must also stop instead of scheduling past the bound.
			task.Spec.Trigger.Until = &until
			h.d.nextSchedule(task, until)
			if task.Status != "completed" {
				t.Fatalf("next beyond until: %+v", task)
			}
		})
	}
}
