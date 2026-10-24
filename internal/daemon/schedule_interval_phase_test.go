package daemon

import (
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func TestIntervalResumePreservesPhase(t *testing.T) {
	first := calendarTime(t, "2026-10-20T07:17:00Z")
	for _, explicit := range []bool{true, false} {
		for _, cursor := range []string{"future", "past", "nil"} {
			for _, manual := range []bool{false, true} {
				t.Run(map[bool]string{true: "at", false: "delay"}[explicit]+cursor+map[bool]string{true: "manual-history", false: "no-history"}[manual], func(t *testing.T) {
					now := first.Add(27 * time.Hour)
					tr := schedule.Trigger{Type: "interval", IntervalSeconds: 86400, DelaySeconds: 3600}
					if explicit {
						tr.At = &first
						tr.DelaySeconds = 0
					}
					task := &schedule.Task{CreatedAt: first.Add(-time.Hour), Spec: schedule.Spec{Trigger: tr}, Status: "paused"}
					want := first.Add(48 * time.Hour)
					switch cursor {
					case "future":
						want = want.Add(24 * time.Hour)
						task.NextRunAt = &want
					case "past":
						past := first.Add(24 * time.Hour)
						task.NextRunAt = &past
					}
					if manual {
						task.Runs = []schedule.Run{{Manual: true, ScheduledAt: first.Add(72 * time.Hour), Status: "failed"}}
					}
					(&Daemon{}).nextSchedule(task, now)
					if task.Status != "scheduled" || task.NextRunAt == nil || !task.NextRunAt.Equal(want) {
						t.Fatalf("phase lost: %+v", task)
					}
				})
			}
		}
	}
	// On an exact phase point, an unconsumed first occurrence may run now.
	for _, consumed := range []bool{false, true} {
		task := &schedule.Task{Spec: schedule.Spec{Trigger: schedule.Trigger{Type: "interval", At: &first, IntervalSeconds: 86400}}}
		if consumed {
			task.Runs = []schedule.Run{{ScheduledAt: first, Status: "completed"}}
		}
		(&Daemon{}).nextSchedule(task, first)
		want := first
		if consumed {
			want = first.Add(24 * time.Hour)
		}
		if task.NextRunAt == nil || !task.NextRunAt.Equal(want) {
			t.Fatal("exact phase repeated or skipped")
		}
	}
}

func TestIntervalInitialDelayPhaseSurvivesEditAndRestart(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 86400, DelaySeconds: 3600}), "")
	if task.Spec.Trigger.At != nil || task.IntervalPhase == nil || !task.IntervalPhase.Equal(task.CreatedAt.Add(time.Hour)) {
		t.Fatal("initial delay was not persisted as phase")
	}
	due := *task.NextRunAt
	for _, op := range []string{"pause", "resume"} {
		if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: op, TaskId: task.ID}, originLocal); err != nil {
			t.Fatal(err)
		}
	}
	if got := readTask(t, h.d, task.ID); got.NextRunAt == nil || !got.NextRunAt.Equal(due) {
		t.Fatal("future phase shifted on resume")
	}
	// Replacing a definition without an explicit timestamp establishes a fresh
	// phase from that edit, instead of the task's older creation time.
	state := h.d.tasks[task.ID]
	state.Spec.Trigger.At = nil
	state.IntervalPhase = nil
	edited := time.Now().Add(2 * time.Hour)
	if err := h.d.prepareSchedule(t.Context(), state, edited); err != nil {
		t.Fatal(err)
	}
	if err := h.d.saveSchedulesLocked(); err != nil {
		t.Fatal(err)
	}
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	state = h.d.tasks[task.ID]
	state.NextRunAt = nil
	h.d.nextSchedule(state, edited.Add(2*time.Hour))
	want := edited.Add(25 * time.Hour)
	if state.NextRunAt == nil || !state.NextRunAt.Equal(want) {
		t.Fatalf("edited phase lost: %+v", state)
	}
}
