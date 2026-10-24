package daemon

import (
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
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

func TestCalendarCatchupChoosesEarlierFoldAndNoBacklog(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "calendar", Time: "01:30", Timezone: "America/New_York"}), "")
	old, _ := time.Parse(time.RFC3339, "2026-10-28T01:30:00-04:00")
	now, _ := time.Parse(time.RFC3339, "2026-11-01T01:45:00-05:00")
	want, _ := time.Parse(time.RFC3339, "2026-11-01T01:30:00-04:00")
	h.d.tasks[task.ID].NextRunAt = &old
	h.d.tickSchedules(now)
	got := readTask(t, h.d, task.ID)
	if got.Active == nil || !got.Active.ScheduledAt.Equal(want) || got.Dispatches != 1 {
		t.Fatalf("wrong catch-up: %+v", got)
	}
	completeTask(h, got.Active, now)
	h.d.tickSchedules(now)
	got = readTask(t, h.d, task.ID)
	next, _ := time.Parse(time.RFC3339, "2026-11-02T01:30:00-05:00")
	if got.NextRunAt == nil || !got.NextRunAt.Equal(next) {
		t.Fatalf("wrong next date: %+v", got)
	}
}

func TestScheduleCreationRejectsUnknownTimezone(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	raw := []byte(`{"title":"Test","message":"Check","trigger":{"type":"calendar","time":"08:00","timezone":"Unknown/Place"},"target":{"type":"resume_session","session":"current"}}`)
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "create", SessionId: h.s.meta.ID, SpecJson: raw}, originLocal); err == nil {
		t.Fatal("created with unknown timezone")
	}
}
