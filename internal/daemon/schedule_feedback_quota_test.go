package daemon

import (
	"context"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"strings"
	"testing"
	"time"
)

func TestRelaySessionLimitFailsScheduledExecutionDurably(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := startScheduledTurn(t, h)
	if err := h.d.SessionPublicationFailed(h.s.meta.ID, "relay_session_limit"); err != nil {
		t.Fatal(err)
	}
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	got := readTask(t, h.d, task.ID)
	if got.Status != "failed" || got.Reason != "relay_session_limit" || got.Active != nil || got.NextRunAt != nil || len(got.Runs) != 1 || got.Runs[0].Status != "failed" || got.Runs[0].Reason != "relay_session_limit" {
		t.Fatalf("quota not surfaced: %+v", got)
	}
	h.d.tickSchedules(time.Now().Add(time.Hour))
	if readTask(t, h.d, task.ID).Dispatches != 1 {
		t.Fatal("quota failure kept dispatching")
	}
}

func TestPublicationRefusalBeforeAndAfterAdmission(t *testing.T) {
	for _, order := range []string{"before", "queued", "running"} {
		t.Run(order, func(t *testing.T) {
			h := scheduleHarness(t)
			defer h.s.log.Close()
			task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
			now := time.Now().Add(time.Second)
			snapshots := h.d.prepareScheduledDispatches(now)
			snapshot := snapshots[0]
			h.d.tasks[task.ID].Spec.Target.Type = "new_session"
			h.s.meta.ScheduledTaskID, h.s.meta.ScheduledRunID = task.ID, snapshot.Active.ID
			if order != "before" {
				h.d.queueScheduledSession(context.Background(), snapshot, now, h.s)
			}
			if order == "running" {
				h.s.emit(&pb.Event{TurnId: "timed", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}},
					withOrigin(&pb.Event{TurnId: "timed", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{RequestId: snapshot.Active.RequestID}}}, &pb.Origin{ScheduledTaskId: task.ID, ScheduledRunId: snapshot.Active.ID}))
				// The native CLI has consumed it; it is no longer a cancellable queue item.
				h.s.mu.Lock()
				h.s.queue.Queue = nil
				h.s.mu.Unlock()
			}
			if err := h.d.SessionPublicationFailed(h.s.meta.ID, "relay_session_limit"); err != nil {
				t.Fatal(err)
			}
			if order == "before" {
				h.d.queueScheduledSession(context.Background(), snapshot, now, h.s)
			}
			got := readTask(t, h.d, task.ID)
			if got.Active != nil || len(got.Runs) != 1 || got.Runs[0].Reason != "relay_session_limit" || got.Runs[0].SessionID != h.s.meta.ID {
				t.Fatalf("refusal lost: %+v", got)
			}
			if len(h.s.queue.Queue) != 0 {
				t.Fatal("invisible scheduled input still queued")
			}
			if order == "running" && !strings.Contains(strings.Join(h.term.written(), ""), string(h.s.ag.Interrupt())) {
				t.Fatal("invisible turn was not interrupted")
			}
			if order != "running" && len(h.term.written()) != 0 {
				t.Fatal("refusal wrote hidden work")
			}
			if err := h.d.loadSchedules(); err != nil {
				t.Fatal(err)
			}
			if readTask(t, h.d, task.ID).Runs[0].Reason != "relay_session_limit" {
				t.Fatal("failure not durable")
			}
		})
	}
}
