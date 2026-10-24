package daemon

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func TestIntervalDefinitionRoundTripDoesNotRedispatch(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, scoped := range []bool{false, true} {
			for _, tail := range []string{"none", "manual", "skipped"} {
				t.Run(map[bool]string{true: "explicit", false: "implicit"}[explicit]+map[bool]string{true: "mcp", false: "client"}[scoped]+tail, func(t *testing.T) {
					h := scopedHarness(t, "ask")
					tr := schedule.Trigger{Type: "interval", IntervalSeconds: 3600}
					if explicit {
						first := time.Now().Add(-5 * time.Second).In(time.FixedZone("example-offset", 8*3600))
						tr.At = &first
					}
					expected, _ := json.Marshal(tr)
					spec := taskSpec(tr)
					var task *schedule.Task
					if scoped {
						raw, err := scopedSchedule(t, h, "create", "", &spec)
						if err != nil {
							t.Fatal(err)
						}
						task = decodeScopedTask(t, raw)
					} else {
						task = createTask(t, h, spec, "")
					}
					actual, _ := json.Marshal(task.Spec.Trigger)
					if !bytes.Equal(expected, actual) {
						t.Fatalf("definition changed on create: %s != %s", actual, expected)
					}
					now := time.Now().Add(time.Second)
					h.d.tickSchedules(now)
					run := readTask(t, h.d, task.ID).Active
					if run == nil {
						t.Fatal("no first automatic run")
					}
					completeTask(h, run, now)
					h.d.tickSchedules(now)
					state := h.d.tasks[task.ID]
					if tail != "none" {
						state.Runs = appendRun(state.Runs, schedule.Run{ID: "later-record", Manual: tail == "manual", Status: map[string]string{"manual": "completed", "skipped": "skipped"}[tail], ScheduledAt: now})
					}
					if err := h.d.saveSchedulesLocked(); err != nil {
						t.Fatal(err)
					}
					restartSession(t, h)
					setScopedMode(h, "ask")
					h.s.emit(withOrigin(&pb.Event{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "edit title"}}}, originLocal))
					got := readTask(t, h.d, task.ID)
					phase := *got.IntervalPhase
					copy := got.Spec
					copy.Title = "Edited title"
					if scoped {
						if _, err := scopedSchedule(t, h, "update", task.ID, &copy); err != nil {
							t.Fatal(err)
						}
					} else {
						raw, _ := json.Marshal(copy)
						if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "update", TaskId: task.ID, ExpectedRevision: got.Revision, SpecJson: raw}, &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT}); err != nil {
							t.Fatal(err)
						}
					}
					got = readTask(t, h.d, task.ID)
					actual, _ = json.Marshal(got.Spec.Trigger)
					want := run.ScheduledAt.Add(time.Hour)
					if !bytes.Equal(expected, actual) || got.IntervalPhase == nil || !got.IntervalPhase.Equal(phase) || got.NextRunAt == nil || !got.NextRunAt.Equal(want) {
						t.Fatalf("roundtrip rewound plan: %+v", got)
					}
					if jobs := h.d.prepareScheduledDispatches(now.Add(time.Second)); len(jobs) != 0 || readTask(t, h.d, task.ID).Dispatches != 1 {
						t.Fatal("title edit redispatched consumed occurrence")
					}
				})
			}
		}
	}
}

func TestAutomaticDispatchRejectsConsumedOccurrence(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	first := time.Now()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", At: &first, IntervalSeconds: 3600}), "")
	h.d.tickSchedules(first.Add(time.Second))
	run := readTask(t, h.d, task.ID).Active
	completeTask(h, run, first.Add(time.Second))
	h.d.tickSchedules(first.Add(time.Second))
	// A stale cursor must be safe independently of update's phase calculation.
	state := h.d.tasks[task.ID]
	state.NextRunAt = &run.ScheduledAt
	jobs := h.d.prepareScheduledDispatches(first.Add(2 * time.Second))
	if len(jobs) != 0 || state.Active != nil || state.Dispatches != 1 || state.NextRunAt == nil || !state.NextRunAt.After(run.ScheduledAt) {
		t.Fatal("consumed cursor dispatched twice")
	}
}
