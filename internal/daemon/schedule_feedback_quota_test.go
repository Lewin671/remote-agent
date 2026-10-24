package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"github.com/Lewin671/remote-agent/internal/tail"
)

func unpublishedRun(t *testing.T, manual bool) (*harness, *schedule.Task, *schedule.Task, string) {
	t.Helper()
	h := scheduleHarness(t)
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600, DelaySeconds: 3600}), "")
	if manual {
		requestManual(t, h, task.ID)
	} else {
		now := time.Now()
		h.d.tasks[task.ID].NextRunAt = &now
	}
	before := automaticPlan(h.d.tasks[task.ID])
	snapshot := h.d.prepareScheduledDispatches(time.Now())[0]
	h.d.tasks[task.ID].Spec.Target.Type = "new_session"
	h.s.meta.TaskOwned = true
	h.s.meta.ScheduledTaskID, h.s.meta.ScheduledRunID = task.ID, snapshot.Active.ID
	if err := h.d.saveSchedulesLocked(); err != nil {
		t.Fatal(err)
	}
	return h, task, snapshot, before
}
func TestPublicationRefusalUsesScheduledClosure(t *testing.T) {
	for _, manual := range []bool{false, true} {
		for _, order := range []string{"before", "restarted-before", "queued", "running", "finished"} {
			t.Run(map[bool]string{false: "automatic", true: "manual"}[manual]+order, func(t *testing.T) {
				h, task, snapshot, before := unpublishedRun(t, manual)
				defer h.s.log.Close()
				if order != "before" && order != "restarted-before" {
					h.d.queueScheduledSession(context.Background(), snapshot, time.Now(), h.s)
				}
				if order == "running" {
					h.s.emit(&pb.Event{TurnId: "native", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}, withOrigin(&pb.Event{TurnId: "native", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{RequestId: snapshot.Active.RequestID}}}, &pb.Origin{ScheduledTaskId: task.ID, ScheduledRunId: snapshot.Active.ID}))
				}
				if order == "finished" {
					completeTask(h, snapshot.Active, time.Now())
					h.d.tickSchedules(time.Now())
				}
				if order == "restarted-before" {
					if err := h.d.loadSchedules(); err != nil {
						t.Fatal(err)
					}
				}
				if err := h.d.SessionPublicationFailed(h.s.meta.ID, "relay_session_limit"); err != nil {
					t.Fatal(err)
				}
				if order == "before" || order == "restarted-before" {
					h.d.queueScheduledSession(context.Background(), snapshot, time.Now(), h.s)
				}
				got := readTask(t, h.d, task.ID)
				if got.Active != nil || len(got.Runs) != 1 || got.Runs[0].ID != snapshot.Active.ID || got.Runs[0].SessionID != h.s.meta.ID || got.Runs[0].Status != "failed" || got.Runs[0].Reason != "relay_session_limit" {
					t.Fatalf("quota result lost: %+v", got)
				}
				if manual {
					if automaticPlan(got) != before {
						t.Fatal("manual quota changed automatic plan")
					}
				} else if got.Status != "failed" || got.NextRunAt != nil {
					t.Fatal("automatic quota did not stop plan")
				}
				if len(h.term.written()) != 0 {
					t.Fatal("quota used dedicated control writes")
				}
				revision := got.Revision
				if err := h.d.SessionPublicationFailed(h.s.meta.ID, "relay_session_limit"); err != nil {
					t.Fatal(err)
				}
				if readTask(t, h.d, task.ID).Revision != revision {
					t.Fatal("duplicate quota report changed result")
				}
				if err := h.d.loadSchedules(); err != nil {
					t.Fatal(err)
				}
				closers := h.d.scheduledClosures()
				if len(closers) != 1 {
					t.Fatal("rejected new session did not enter automatic closure")
				}
				if err := closers[0].killIf(context.Background(), true); err != nil {
					t.Fatal(err)
				}
				if h.term.Alive() || !h.s.meta.TaskCLIClosed || !h.s.meta.Exited {
					t.Fatal("rejected CLI not closed by normal lifecycle")
				}
				if len(h.term.written()) != 0 {
					t.Fatal("closure sent interrupt instead of closing CLI")
				}
			})
		}
	}
}

func TestRelayQuotaIgnoresResumeSession(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := startScheduledTurn(t, h)
	// Even retained creation provenance must not turn a resume into this path.
	h.s.meta.ScheduledTaskID, h.s.meta.ScheduledRunID = task.ID, task.Active.ID
	before := readTask(t, h.d, task.ID)
	if err := h.d.SessionPublicationFailed(h.s.meta.ID, "relay_session_limit"); err != nil {
		t.Fatal(err)
	}
	got := readTask(t, h.d, task.ID)
	if got.Revision != before.Revision || got.Active == nil || got.Active.ID != before.Active.ID || len(got.Runs) != 0 || len(h.d.scheduledClosures()) != 0 || !h.term.Alive() {
		t.Fatal("resume_session entered quota cleanup")
	}
}

func TestQuotaClosureReusesReadAndTakeoverSafety(t *testing.T) {
	for _, human := range []bool{false, true} {
		t.Run(map[bool]string{false: "unread", true: "takeover"}[human], func(t *testing.T) {
			h, _, snapshot, _ := unpublishedRun(t, false)
			defer h.s.log.Close()
			h.d.queueScheduledSession(context.Background(), snapshot, time.Now(), h.s)
			h.s.emit(&pb.Event{TurnId: "native", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
			if err := h.d.SessionPublicationFailed(h.s.meta.ID, "relay_session_limit"); err != nil {
				t.Fatal(err)
			}
			if len(h.d.scheduledClosures()) != 1 {
				t.Fatal("missing closer")
			}
			if human {
				if _, err := h.s.send("human takes over", "human"); err != nil {
					t.Fatal(err)
				}
			} else {
				h.s.reader = tail.NewReader(h.dir, 0)
			}
			if err := h.s.killIf(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if !h.term.Alive() || len(h.term.written()) != 0 {
				t.Fatal("quota bypassed unread transcript or takeover safety")
			}
			if len(h.d.scheduledClosures()) != 0 {
				t.Fatal("quota bypassed takeover or closure backoff")
			}
		})
	}
}

func TestQuotaClosureDoesNotBlockScheduleLoop(t *testing.T) {
	h, _, snapshot, _ := unpublishedRun(t, false)
	defer h.s.log.Close()
	h.d.queueScheduledSession(context.Background(), snapshot, time.Now(), h.s)
	h.s.emit(&pb.Event{TurnId: "native", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}, withOrigin(&pb.Event{TurnId: "native", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{RequestId: snapshot.Active.RequestID}}}, &pb.Origin{ScheduledTaskId: snapshot.ID, ScheduledRunId: snapshot.Active.ID}))
	h.s.ctlMu.Lock()
	var once sync.Once
	release := func() { once.Do(h.s.ctlMu.Unlock) }
	defer release()
	reported := make(chan error, 1)
	go func() { reported <- h.d.SessionPublicationFailed(h.s.meta.ID, "relay_session_limit") }()
	select {
	case err := <-reported:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("quota report waited for terminal control")
	}
	other := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600, DelaySeconds: 2}), "")
	stop := h.d.startScheduleLoop(context.Background())
	defer func() { release(); stop() }()
	eventually(t, "scheduler advances while quota closer waits for ctlMu", func() bool { return readTask(t, h.d, other.ID).Dispatches == 1 })
}
