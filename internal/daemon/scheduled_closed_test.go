package daemon

import (
	"context"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestScheduledClosedCauseSurvivesRestart(t *testing.T) {
	for _, scheduled := range []bool{true, false} {
		t.Run(map[bool]string{true: "automatic", false: "manual"}[scheduled], func(t *testing.T) {
			h := scheduleHarness(t)
			defer func() { h.s.log.Close() }()
			h.s.meta.TaskOwned = true           // Ownership alone never identifies an automatic close.
			h.s.meta.TaskCLIClosed = !scheduled // Also true on a previously resumed CLI.
			h.s.scheduledClose = scheduled
			if err := h.s.killIf(context.Background(), scheduled); err != nil {
				t.Fatal(err)
			}
			check := func() {
				t.Helper()
				var exited *pb.SessionState
				for _, e := range h.events() {
					if state := e.GetSessionState(); state.GetStatus() == pb.SessionStatus_SESSION_STATUS_EXITED {
						exited = state
					}
				}
				if exited == nil || exited.Detail != "stopped" || exited.ScheduledClosed != scheduled {
					t.Fatalf("scheduled=%v: exit=%v", scheduled, exited)
				}
			}
			check()
			m, err := loadMeta(h.s.dir)
			if err != nil {
				t.Fatal(err)
			}
			if m.ScheduledClosed != scheduled {
				t.Fatalf("persisted exit cause=%v", m.ScheduledClosed)
			}
			h.reopen()
			check()
			if h.s.scheduledClosed != scheduled {
				t.Fatal("replay lost exit cause")
			}
		})
	}
}

func TestScheduledClosedRecoversMetadataBeforeExitEvent(t *testing.T) {
	h := scheduleHarness(t)
	defer func() { h.s.log.Close() }()
	// Simulate a crash after session.json was saved but before EXITED was appended.
	h.s.meta.Exited, h.s.meta.TaskCLIClosed, h.s.meta.ScheduledClosed = true, true, true
	if err := h.s.meta.save(h.s.dir); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	events := h.events()
	state := events[len(events)-1].GetSessionState()
	if state.GetStatus() != pb.SessionStatus_SESSION_STATUS_EXITED || state.GetDetail() != "stopped" || !state.GetScheduledClosed() {
		t.Fatalf("recovered state=%v", state)
	}
	before := len(events)
	h.reopen()
	if len(h.events()) != before {
		t.Fatal("restart duplicated an already recorded exit")
	}
}
