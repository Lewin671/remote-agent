package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

// Deliberately compares only the automatic plan, independently of execution
// records, revisions and manual requests. Every manual lifecycle boundary uses it.
func automaticPlan(t *schedule.Task) string {
	b, _ := json.Marshal([]any{t.Status, t.Reason, t.NextRunAt, t.Dispatches, t.LastCalendarDate})
	return string(b)
}
func requestManual(t *testing.T, h *harness, id string) {
	t.Helper()
	if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "run", TaskId: id}, originLocal); err != nil {
		t.Fatal(err)
	}
}
func manualFixture(t *testing.T, typ string) (*harness, *schedule.Task) {
	t.Helper()
	h := scheduleHarness(t)
	due := time.Now().Add(time.Hour)
	tr := schedule.Trigger{Type: typ, MaxRuns: 1}
	switch typ {
	case "at":
		tr.At = &due
	case "calendar":
		tr.Time, tr.Timezone = "23:59", "UTC"
	case "interval":
		tr.At = &due
		tr.IntervalSeconds = 3600
	case "after_turn":
		tr.DelaySeconds = 3600
	}
	task := createTask(t, h, taskSpec(tr), "")
	if typ == "calendar" {
		h.d.tasks[task.ID].NextRunAt = &due
	}
	return h, h.d.tasks[task.ID]
}
func TestManualExecutionLifecycleMatrix(t *testing.T) {
	for _, typ := range []string{"at", "interval", "calendar", "after_turn"} {
		for _, ending := range []string{"completed", "failed", "interrupted", "uncertain", "cancelled", "skipped", "dispatch_rejected", "restart_dispatching", "restart_queued", "input_paused"} {
			t.Run(typ+"/"+ending, func(t *testing.T) {
				h, task := manualFixture(t, typ)
				defer func() { h.s.log.Close() }()
				before := automaticPlan(task)
				requestManual(t, h, task.ID)
				if automaticPlan(task) != before || task.ManualRequest == nil {
					t.Fatal("manual request changed automatic plan")
				}
				requestID := task.ManualRequest.RequestID
				now := time.Now()
				if ending == "skipped" {
					task.ManualRequest.RequestedAt = now.Add(-48 * time.Hour)
				}
				snapshots := h.d.prepareScheduledDispatches(now)
				if automaticPlan(h.d.tasks[task.ID]) != before {
					t.Fatal("manual preparation changed automatic plan")
				}
				if ending == "skipped" {
					got := readTask(t, h.d, task.ID)
					if len(snapshots) != 0 || got.ManualRequest != nil || len(got.Runs) != 1 || !got.Runs[0].Manual || got.Runs[0].Status != "skipped" {
						t.Fatalf("not skipped: %+v", got)
					}
					return
				}
				if len(snapshots) != 1 || !snapshots[0].Active.Manual || snapshots[0].Active.RequestID != requestID {
					t.Fatal("manual identity not dispatched")
				}
				snapshot := snapshots[0]
				switch ending {
				case "restart_dispatching":
					if err := h.d.loadSchedules(); err != nil {
						t.Fatal(err)
					}
				case "dispatch_rejected":
					h.d.finishScheduledDispatch(context.Background(), snapshot, now, "could_not_start_session", nil)
				default:
					h.d.queueScheduledSession(context.Background(), snapshot, now, h.s)
					if automaticPlan(h.d.tasks[task.ID]) != before {
						t.Fatal("manual queue changed automatic plan")
					}
					if ending == "restart_queued" {
						restartSession(t, h)
					}
					if ending == "input_paused" {
						h.s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: requestID, Status: pb.InputStatus_INPUT_STATUS_QUEUED, Paused: true}}})
						h.d.tickSchedules(now)
						if automaticPlan(h.d.tasks[task.ID]) != before || !h.d.tasks[task.ID].Active.InputPaused {
							t.Fatal("manual input pause changed plan")
						}
					}
					if ending == "restart_queued" || ending == "input_paused" || ending == "completed" {
						run := readTask(t, h.d, task.ID).Active
						completeTask(h, run, now)
						h.d.tickSchedules(now)
					} else {
						h.d.endScheduledRun(h.d.tasks[task.ID], now, ending, "manual result")
					}
				}
				got := readTask(t, h.d, task.ID)
				if automaticPlan(got) != before || got.Active != nil || got.ManualRequest != nil || len(got.Runs) != 1 || !got.Runs[0].Manual {
					t.Fatalf("manual lifecycle corrupted plan: %+v", got)
				}
				if ending == "restart_dispatching" && got.Runs[0].Status != "uncertain" {
					t.Fatal("restart retried manual dispatch")
				}
			})
		}
	}
}

func TestManualPendingControlsAndAdmission(t *testing.T) {
	for _, typ := range []string{"at", "interval", "calendar", "after_turn"} {
		for _, op := range []string{"pause", "cancel"} {
			t.Run(typ+op, func(t *testing.T) {
				h, task := manualFixture(t, typ)
				defer h.s.log.Close()
				requestManual(t, h, task.ID)
				if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: op, TaskId: task.ID}, originLocal); err != nil {
					t.Fatal(err)
				}
				if readTask(t, h.d, task.ID).ManualRequest != nil {
					t.Fatal("control retained pending manual request")
				}
				if got := h.d.prepareScheduledDispatches(time.Now()); len(got) != 0 {
					t.Fatal("control dispatched cleared manual request")
				}
			})
		}
		for _, status := range []string{"uncertain", "uncertain-late", "failed", "completed", "cancelled"} {
			t.Run(typ+status, func(t *testing.T) {
				h, task := manualFixture(t, typ)
				defer h.s.log.Close()
				task.Status = strings.TrimSuffix(status, "-late")
				if strings.HasSuffix(status, "-late") {
					h.d.lateRuns = map[string]string{task.ID: "late-original"}
				}
				late := h.d.lateRuns[task.ID]
				before := automaticPlan(task)
				if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "run", TaskId: task.ID}, originLocal); err == nil || !strings.Contains(err.Error(), "resume or update") {
					t.Fatalf("bad admission error: %v", err)
				}
				if task.ManualRequest != nil || automaticPlan(task) != before || h.d.lateRuns[task.ID] != late {
					t.Fatal("rejected manual request lost original plan/recovery")
				}
			})
		}
	}
}

func TestManualAndAutomaticSerialize(t *testing.T) {
	for _, manualFirst := range []bool{true, false} {
		for _, late := range []bool{true, false} {
			t.Run(map[bool]string{true: "manual-first", false: "automatic-first"}[manualFirst]+map[bool]string{true: "skip", false: "catchup"}[late], func(t *testing.T) {
				h, task := manualFixture(t, "calendar")
				defer h.s.log.Close()
				now := time.Now()
				due := now.Add(time.Minute)
				task.NextRunAt = &due
				task.Spec.LateSeconds = 60
				if manualFirst {
					requestManual(t, h, task.ID)
					h.d.tickSchedules(now)
				} else {
					h.d.tickSchedules(due)
					requestManual(t, h, task.ID)
				}
				first := readTask(t, h.d, task.ID).Active
				if first == nil || first.Manual != manualFirst {
					t.Fatal("wrong first execution")
				}
				at := due.Add(time.Second)
				if late {
					at = due.Add(2 * time.Minute)
				}
				// A pending manual request can also expire while the automatic run owns Active.
				if !manualFirst {
					h.d.tasks[task.ID].ManualRequest.RequestedAt = due
				}
				h.d.tickSchedules(at)
				if readTask(t, h.d, task.ID).Active.ID != first.ID {
					t.Fatal("overlapping execution")
				}
				completeTask(h, first, at)
				h.d.tickSchedules(at)
				got := readTask(t, h.d, task.ID)
				if late {
					if got.Active != nil || len(got.Runs) != 2 || got.Runs[1].Status != "skipped" || got.Runs[1].Manual == manualFirst {
						t.Fatalf("missing serialized skip: %+v", got)
					}
				} else {
					if got.Active == nil || got.Active.Manual == manualFirst {
						t.Fatalf("waiting execution lost: %+v", got)
					}
					completeTask(h, got.Active, at)
					h.d.tickSchedules(at)
				}
				got = readTask(t, h.d, task.ID)
				want := int64(1)
				if manualFirst && late {
					want = 0
				}
				if got.Dispatches != want {
					t.Fatalf("manual consumed max_runs: %+v", got)
				}
			})
		}
	}
}

func TestManualPauseResumeDoesNotReplayTrial(t *testing.T) {
	h, task := manualFixture(t, "calendar")
	defer h.s.log.Close()
	requestManual(t, h, task.ID)
	for _, op := range []string{"pause", "resume"} {
		if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: op, TaskId: task.ID}, originLocal); err != nil {
			t.Fatal(err)
		}
	}
	got := readTask(t, h.d, task.ID)
	due := *got.NextRunAt
	h.d.tickSchedules(due)
	got = readTask(t, h.d, task.ID)
	if got.Active == nil || got.Active.Manual || got.Dispatches != 1 {
		t.Fatalf("resumed trial displaced automatic occurrence: %+v", got)
	}
	completeTask(h, got.Active, due)
	h.d.tickSchedules(due)
	if got = readTask(t, h.d, task.ID); got.Active != nil || got.Status != "completed" || got.Dispatches != 1 {
		t.Fatalf("duplicate occurrence: %+v", got)
	}
}

func TestManualPermissionRejectionDoesNotPausePlan(t *testing.T) {
	for _, typ := range []string{"interval", "calendar", "at", "after_turn"} {
		h := scopedHarness(t, "ask")
		tr := schedule.Trigger{Type: typ, DelaySeconds: 3600}
		due := time.Now().Add(time.Hour)
		switch typ {
		case "interval":
			tr.IntervalSeconds = 3600
		case "calendar":
			tr.DelaySeconds = 0
			tr.Time, tr.Timezone = "23:59", "UTC"
		case "at":
			tr.DelaySeconds = 0
			tr.At = &due
		}
		spec := taskSpec(tr)
		raw, err := scopedSchedule(t, h, "create", "", &spec)
		if err != nil {
			t.Fatal(err)
		}
		task := decodeScopedTask(t, raw)
		if _, err := scopedSchedule(t, h, "run", task.ID, nil); err != nil {
			t.Fatal(err)
		}
		before := automaticPlan(h.d.tasks[task.ID])
		setScopedMode(h, "full")
		h.d.tickSchedules(time.Now())
		got := readTask(t, h.d, task.ID)
		if automaticPlan(got) != before || got.Active != nil || got.ManualRequest != nil || len(got.Runs) != 1 || !got.Runs[0].Manual || got.Runs[0].Reason != "agent_schedule_permissions_changed" {
			t.Fatalf("manual rejection changed plan: %+v", got)
		}
	}
}

func TestManualControlMatrix(t *testing.T) {
	for _, typ := range []string{"at", "interval", "calendar", "after_turn"} {
		for _, started := range []bool{false, true} {
			for _, control := range []string{"pause", "pause_resume", "cancel"} {
				t.Run(typ+map[bool]string{false: "pending", true: "started"}[started]+control, func(t *testing.T) {
					h, task := manualFixture(t, typ)
					defer h.s.log.Close()
					requestManual(t, h, task.ID)
					if started {
						h.d.tickSchedules(time.Now())
					}
					op := control
					if control == "pause_resume" {
						op = "pause"
					}
					if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: op, TaskId: task.ID}, originLocal); err != nil {
						t.Fatal(err)
					}
					if control == "pause_resume" {
						if _, err := h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "resume", TaskId: task.ID}, originLocal); err != nil {
							t.Fatal(err)
						}
					}
					got := readTask(t, h.d, task.ID)
					plan := automaticPlan(got)
					if got.ManualRequest != nil {
						t.Fatal("control retained pending manual request")
					}
					if started {
						if got.Active == nil || !got.Active.Manual {
							t.Fatal("control removed started manual execution")
						}
						completeTask(h, got.Active, time.Now())
						h.d.tickSchedules(time.Now())
						if got = readTask(t, h.d, task.ID); got.Active != nil || automaticPlan(got) != plan || len(got.Runs) != 1 || !got.Runs[0].Manual {
							t.Fatalf("manual completion changed controlled plan: %+v", got)
						}
					} else {
						h.d.tickSchedules(time.Now())
						if readTask(t, h.d, task.ID).Active != nil {
							t.Fatal("cleared manual request dispatched")
						}
					}
				})
			}
		}
	}
}
