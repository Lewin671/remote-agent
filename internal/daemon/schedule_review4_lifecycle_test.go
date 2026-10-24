package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/schedule"
)

func TestScheduledResumeRechecksAfterAutomaticClose(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		t.Run(map[bool]string{false: "already closed", true: "close owns lifecycle"}[waiting], func(t *testing.T) {
			h := resumableHarness(t)
			defer func() {
				if h.s.stop != nil {
					h.s.stop()
					select {
					case <-h.s.done:
					case <-time.After(5 * time.Second):
						t.Error("resumed workers did not stop")
					}
				}
				h.s.log.Close()
			}()
			if err := h.d.loadSchedules(); err != nil {
				t.Fatal(err)
			}
			h.s.meta.TaskOwned = true
			h.s.meta.ScheduledTaskID = "original-task"
			h.s.scheduledClose = true
			task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600}), "")
			now := time.Now().Add(time.Second)
			snapshot := h.d.prepareScheduledDispatches(now)[0]
			selected, err := h.d.launchScheduledSession(context.Background(), snapshot)
			if err != nil || selected != h.s {
				t.Fatalf("could not select resume target: %v", err)
			}
			// Native termination finishes after target lookup, before admission.
			// Like the existing lifecycle harness, the terminal adapter remains
			// available for the next fake tmux generation.
			barrier := &scheduledKillBarrier{terminal: h.term, entered: make(chan struct{}), release: make(chan struct{})}
			h.s.term = barrier
			closed := make(chan error, 1)
			go func() { closed <- h.s.killIf(context.Background(), true) }()
			<-barrier.entered
			admitted := make(chan struct{})
			if waiting {
				go func() {
					defer close(admitted)
					h.d.queueScheduledSession(context.Background(), snapshot, now, selected)
				}()
				select {
				case <-admitted:
					t.Fatal("admission bypassed automatic close")
				default:
				}
			}
			close(barrier.release)
			if err := <-closed; err != nil {
				t.Fatal(err)
			}
			if !waiting {
				h.d.queueScheduledSession(context.Background(), snapshot, now, selected)
			} else {
				select {
				case <-admitted:
				case <-time.After(5 * time.Second):
					t.Fatal("resume/admission deadlocked")
				}
			}
			got := readTask(t, h.d, task.ID)
			h.s.mu.Lock()
			exited, seen, closedLifetime := h.s.meta.Exited, h.s.queue.seen(snapshot.Active.RequestID), h.s.meta.TaskCLIClosed
			h.s.mu.Unlock()
			if got.Active == nil || got.Active.Status != "queued" || got.Active.RequestID != snapshot.Active.RequestID || exited || !seen || !closedLifetime {
				t.Fatalf("close won race and lost scheduled input: %+v exited=%v seen=%v", got, exited, seen)
			}
			// The original close request cannot close the resumed CLI again.
			if err := h.s.killIf(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			h.s.mu.Lock()
			exited = h.s.meta.Exited
			h.s.mu.Unlock()
			if exited {
				t.Fatal("old automatic close killed the resumed generation")
			}
		})
	}
}
