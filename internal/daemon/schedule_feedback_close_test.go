package daemon

import (
	"context"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"github.com/Lewin671/remote-agent/internal/tail"
	"testing"
	"time"
)

func TestScheduledClosureWaitsForReadableTranscript(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	h.s.meta.TaskOwned = true
	h.s.scheduledClose = true
	h.s.reader = tail.NewReader(h.dir, 0) // A directory cannot be consumed as a transcript.
	if err := h.s.killIf(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if !h.term.Alive() {
		t.Fatal("closed with unread user records")
	}
}

type scheduledKillBarrier struct {
	terminal
	entered, release chan struct{}
}

func (b *scheduledKillBarrier) Kill() error { close(b.entered); <-b.release; return nil }
func TestScheduledCloseAndHumanAdmissionHaveOneOrder(t *testing.T) {
	t.Run("input first", func(t *testing.T) {
		h := resumableHarness(t)
		defer h.s.log.Close()
		h.s.meta.TaskOwned = true
		h.s.meta.ScheduledTaskID = "task"
		h.s.scheduledClose = true
		if _, err := h.s.send("human input", "human"); err != nil {
			t.Fatal(err)
		}
		if h.s.info().TaskOwned {
			t.Fatal("accepted human input did not take over")
		}
		if err := h.s.killIf(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		if !h.term.Alive() {
			t.Fatal("closed after accepting human input")
		}
	})
	t.Run("close first", func(t *testing.T) {
		h := resumableHarness(t)
		defer h.s.log.Close()
		h.s.meta.TaskOwned = true
		h.s.meta.ScheduledTaskID = "task"
		h.s.scheduledClose = true
		barrier := &scheduledKillBarrier{terminal: h.term, entered: make(chan struct{}), release: make(chan struct{})}
		h.s.term = barrier
		closing := make(chan error, 1)
		go func() { closing <- h.s.killIf(context.Background(), true) }()
		<-barrier.entered
		input := make(chan error, 1)
		go func() { _, err := h.s.send("human reply", "human"); input <- err }()
		select {
		case err := <-input:
			t.Fatalf("input did not wait for close/resume: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
		close(barrier.release)
		if err := <-closing; err != nil {
			t.Fatal(err)
		}
		if err := <-input; err != nil {
			t.Fatalf("input rejected instead of resumed: %v", err)
		}
		if h.s.info().TaskOwned || h.s.meta.Exited {
			t.Fatal("reply did not restore and take over session")
		}
	})
}
func TestScheduledCloseBacksOffUntilNewEvent(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
	h.d.tasks[task.ID].Spec.Target.Type = "new_session"
	h.d.tasks[task.ID].Runs = []schedule.Run{{SessionID: h.s.meta.ID, Status: "failed"}}
	h.s.meta.TaskOwned = true
	h.s.scheduledClose = true
	h.s.reader = tail.NewReader(h.dir, 0)
	h.s.killIf(context.Background(), true)
	if len(h.d.scheduledClosures()) != 0 {
		t.Fatal("closure immediately retried after failed read")
	}
	h.s.emit(&pb.Event{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "new record"}}})
	if len(h.d.scheduledClosures()) != 1 {
		t.Fatal("committed event did not wake closure")
	}
}

func TestUncertainRunNeverClosesAutomatically(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "after_turn"}), "")
	tsk := h.d.tasks[task.ID]
	tsk.Spec.Target.Type = "new_session"
	h.s.meta.TaskOwned = true
	h.s.scheduledClose = true // even a prior end marker cannot override uncertainty
	tsk.Runs = []schedule.Run{{SessionID: h.s.meta.ID, Status: "uncertain"}}
	if len(h.d.scheduledClosures()) != 0 {
		t.Fatal("closed uncertain historical run")
	}
	tsk.Runs = nil
	tsk.Active = &schedule.Run{SessionID: h.s.meta.ID, Status: "uncertain"}
	if len(h.d.scheduledClosures()) != 0 {
		t.Fatal("closed uncertain active run")
	}
}
