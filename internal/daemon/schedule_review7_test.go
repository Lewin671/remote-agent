package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func editScheduleToResume(t *testing.T, h *harness, id string) {
	t.Helper()
	task := readTask(t, h.d, id)
	spec := task.Spec
	spec.Target = schedule.Target{Type: "resume_session", Session: h.s.meta.ID}
	spec.Trigger = schedule.Trigger{Type: "interval", IntervalSeconds: 3600, DelaySeconds: 3600}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.d.scheduleCommand(&pb.ScheduleRequest{Operation: "update", TaskId: id, ExpectedRevision: task.Revision, SpecJson: raw}, originLocal); err != nil {
		t.Fatal(err)
	}
}

func reloadScheduleCreation(t *testing.T, h *harness) {
	t.Helper()
	if err := h.s.meta.save(h.s.dir); err != nil {
		t.Fatal(err)
	}
	m, err := loadMeta(h.s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.s.log.Close(); err != nil {
		t.Fatal(err)
	}
	h.open(*m)
	h.d.sessions[m.ID] = h.s
	if err = h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
}

func closeRejectedCreation(t *testing.T, h *harness) {
	t.Helper()
	for i := 0; i < 3; i++ {
		closers := h.d.scheduledClosures()
		if len(closers) != 1 || closers[0] != h.s || !h.s.scheduledCloseRejected {
			t.Fatal("edited definition removed the original rejected CLI from cleanup")
		}
	}
	if err := h.s.killIf(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if h.term.Alive() || !h.s.meta.TaskCLIClosed || !h.s.meta.Exited {
		t.Fatal("old unpublished CLI survived cleanup")
	}
	if len(h.term.written()) != 0 {
		t.Fatal("cleanup used dedicated control writes")
	}
}

func TestRejectedCreationSurvivesTargetEdit(t *testing.T) {
	for _, manual := range []bool{false, true} {
		for _, running := range []bool{false, true} {
			for _, restart := range []bool{false, true} {
				t.Run(map[bool]string{false: "automatic", true: "manual"}[manual]+map[bool]string{false: "/queued", true: "/running"}[running]+map[bool]string{false: "/live", true: "/restart"}[restart], func(t *testing.T) {
					h, task, snapshot, _ := unpublishedRun(t, manual)
					defer func() { h.s.log.Close() }()
					h.d.queueScheduledSession(context.Background(), snapshot, time.Now(), h.s)
					if running {
						if err := h.s.emit(&pb.Event{TurnId: "waiting", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}, &pb.Event{TurnId: "waiting", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{RequestId: snapshot.Active.RequestID}}}); err != nil {
							t.Fatal(err)
						}
					}
					if err := h.d.SessionPublicationFailed(h.s.meta.ID, "relay_session_limit"); err != nil {
						t.Fatal(err)
					}
					// A closer has been selected but has not acquired lifecycle ownership yet.
					if len(h.d.scheduledClosures()) != 1 {
						t.Fatal("missing initial quota closer")
					}
					editScheduleToResume(t, h, task.ID)
					if restart {
						reloadScheduleCreation(t, h)
					}
					closeRejectedCreation(t, h)
					got := readTask(t, h.d, task.ID)
					if got.Spec.Target.Type != "resume_session" || len(got.Runs) != 1 || got.Runs[0].ID != snapshot.Active.ID || got.Runs[0].Reason != "relay_session_limit" {
						t.Fatalf("cleanup changed new definition or old result: %+v", got)
					}
				})
			}
		}
	}
}

func TestLatePublicationRefusalAfterTargetEdit(t *testing.T) {
	for _, manual := range []bool{false, true} {
		for _, restart := range []bool{false, true} {
			t.Run(map[bool]string{false: "automatic", true: "manual"}[manual]+map[bool]string{false: "/live", true: "/restart"}[restart], func(t *testing.T) {
				h, task, snapshot, _ := unpublishedRun(t, manual)
				defer func() { h.s.log.Close() }()
				h.d.queueScheduledSession(context.Background(), snapshot, time.Now(), h.s)
				completeTask(h, snapshot.Active, time.Now())
				h.d.tickSchedules(time.Now())
				if readTask(t, h.d, task.ID).Active != nil {
					t.Fatal("old run did not finish")
				}
				editScheduleToResume(t, h, task.ID)
				if restart {
					reloadScheduleCreation(t, h)
				}
				// A background continuation may still keep the unpublished CLI busy.
				if err := h.s.emit(&pb.Event{TurnId: "background", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}); err != nil {
					t.Fatal(err)
				}
				if err := h.d.SessionPublicationFailed(h.s.meta.ID, "relay_session_limit"); err != nil {
					t.Fatal(err)
				}
				got := readTask(t, h.d, task.ID)
				if got.Spec.Target.Type != "resume_session" || len(got.Runs) != 1 || got.Runs[0].ID != snapshot.Active.ID || got.Runs[0].Status != "failed" || got.Runs[0].Reason != "relay_session_limit" {
					t.Fatalf("late quota report lost after target edit: %+v", got)
				}
				if err := h.d.SessionPublicationFailed(h.s.meta.ID, "relay_session_limit"); err != nil {
					t.Fatal(err)
				}
				if readTask(t, h.d, task.ID).Revision != got.Revision {
					t.Fatal("duplicate late report changed revision")
				}
				closeRejectedCreation(t, h)
			})
		}
	}
}

func TestClosureUsesCreationRunIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, creation, later string
		busy, close           bool
	}{
		{"creation uncertain later completed", "uncertain", "completed", false, false},
		{"creation completed later uncertain", "completed", "uncertain", false, true},
		{"later rejection cannot force busy creator closed", "completed", "failed", true, false},
		{"later completion cannot end creator", "queued", "completed", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, task, snapshot, _ := unpublishedRun(t, false)
			defer h.s.log.Close()
			state := h.d.tasks[task.ID]
			state.Active = nil
			state.Runs = []schedule.Run{
				{ID: snapshot.Active.ID, SessionID: h.s.meta.ID, Status: tc.creation},
				{ID: "later-resume-run", SessionID: h.s.meta.ID, Status: tc.later, Reason: "relay_session_limit"},
			}
			editScheduleToResume(t, h, task.ID)
			h.s.running = tc.busy
			if tc.creation == "uncertain" {
				h.s.scheduledClose = true
			}
			closers := h.d.scheduledClosures()
			if (len(closers) == 1) != tc.close || h.s.scheduledCloseRejected {
				t.Fatalf("another execution overrode creator cleanup: close=%d rejected=%v", len(closers), h.s.scheduledCloseRejected)
			}
		})
	}
}

func TestPreDeliveryRestrictionUsesCreationIdentity(t *testing.T) {
	h, task, snapshot, _ := unpublishedRun(t, false)
	defer h.s.log.Close()
	h.d.queueScheduledSession(context.Background(), snapshot, time.Now(), h.s)
	// Probe the selector's data dependency directly. The public update command
	// separately rejects replacement while Active exists.
	h.d.scheduleMu.Lock()
	defer h.d.scheduleMu.Unlock()
	h.d.tasks[task.ID].Spec.Target = schedule.Target{Type: "resume_session", Session: h.s.meta.ID}
	if !h.d.sessionHasScheduledRunLocked(h.s.meta.ID) {
		t.Fatal("creation restriction depends on mutable task target")
	}
	h.s.meta.ScheduledRunID = "different-creation-run"
	if h.d.sessionHasScheduledRunLocked(h.s.meta.ID) {
		t.Fatal("unrelated creation run restricted this session")
	}
	h.s.meta.ScheduledRunID = snapshot.Active.ID
	h.s.meta.ScheduledTaskID = "different-creation-task"
	if h.d.sessionHasScheduledRunLocked(h.s.meta.ID) {
		t.Fatal("unrelated creation task restricted this session")
	}
}
