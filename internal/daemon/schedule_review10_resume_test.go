package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func closedOwnedSession(t *testing.T) *harness {
	t.Helper()
	h := resumableHarness(t)
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	task := createTask(t, h, taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600, DelaySeconds: 3600}), "")
	h.s.meta.ScheduledTaskID, h.s.meta.ScheduledRunID = task.ID, "creation-run"
	h.s.meta.TaskOwned, h.s.meta.TaskCLIClosed, h.s.meta.Exited = true, true, true
	h.s.scheduledClose = true
	h.d.tasks[task.ID].Runs = []schedule.Run{{ID: "creation-run", SessionID: h.s.meta.ID, Status: "completed"}}
	if err := h.s.meta.save(h.s.dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if h.s.stop != nil {
			h.s.stop()
			select {
			case <-h.s.done:
			case <-time.After(5 * time.Second):
				t.Error("resumed loops did not stop")
			}
		}
		h.s.log.Close()
	})
	return h
}

func TestImplicitResumeWaitsForDurableInputOwnership(t *testing.T) {
	for _, origin := range []*pb.Origin{originLocal, {Kind: pb.OriginKind_ORIGIN_KIND_CLIENT}} {
		for _, fault := range []string{"image", "quota", "queue", "accepted", "event", "directory-sync"} {
			t.Run(origin.Kind.String()+"/"+fault, func(t *testing.T) {
				h := closedOwnedSession(t)
				if fault == "quota" {
					h.d.inputMu.Lock()
					h.d.inputBytes = maxDaemonInputBytes
					h.d.inputMu.Unlock()
				}
				if fault == "queue" {
					if err := os.Mkdir(filepath.Join(h.s.dir, "queue.json"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				// The callback runs under mu: inspect the cached metadata without relocking.
				take := func(string) ([]queuedImage, error) {
					if !h.s.meta.TaskOwned {
						return nil, errors.New("ownership claimed before input admission")
					}
					switch fault {
					case "image":
						return nil, errors.New("expired upload")
					case "event":
						h.s.log.Close()
					case "directory-sync":
						h.d.syncStateDir = failStateSync()
					}
					return nil, nil
				}
				_, err := h.s.enqueueInputContext(context.Background(), "Human reply", "human-reply", nil, origin, pb.SendMode_SEND_MODE_QUEUE, nil, take)
				if h.s.stop != nil {
					h.s.stop()
					select {
					case <-h.s.done:
					case <-time.After(5 * time.Second):
						t.Fatal("resumed workers did not stop")
					}
				}
				accepted := fault == "accepted" || fault == "event" || fault == "directory-sync"
				if queueAccepted(err) != accepted || fault != "accepted" && err == nil {
					t.Fatalf("wrong admission result: %v", err)
				}
				if fault == "event" {
					repairEventLog(t, h)
				}
				h.d.syncStateDir = nil
				h.s.mu.Lock()
				owned, closed, seen := h.s.meta.TaskOwned, h.s.meta.TaskCLIClosed, h.s.queue.seen("human-reply")
				h.s.mu.Unlock()
				if owned == accepted || seen != accepted {
					t.Fatalf("ownership did not follow durable queue: owned=%v accepted=%v seen=%v err=%v", owned, accepted, seen, err)
				}
				if !accepted && closed {
					t.Fatal("rejected implicit resume was not rearmed for cleanup")
				}
				persisted, err := loadMeta(h.s.dir)
				if err != nil {
					t.Fatal(err)
				}
				if persisted.TaskOwned == accepted {
					t.Fatal("ownership result was not persisted")
				}
				closers := h.d.scheduledClosures()
				if accepted {
					if len(closers) != 0 {
						t.Fatal("accepted human input remained in automatic cleanup")
					}
				} else {
					if len(closers) != 1 {
						t.Fatal("rejected implicit resume leaked its CLI")
					}
					if err := closers[0].killIf(context.Background(), true); err != nil {
						t.Fatal(err)
					}
					h.s.mu.Lock()
					exited := h.s.meta.Exited
					h.s.mu.Unlock()
					if !exited {
						t.Fatal("failed-input lifetime was not closed")
					}
				}
			})
		}
	}
}

func TestExplicitResumeStillClaimsTaskSession(t *testing.T) {
	h := closedOwnedSession(t)
	if _, err := h.d.resumeContextBy(context.Background(), h.s.meta.ID, originLocal); err != nil {
		t.Fatal(err)
	}
	if h.s.info().TaskOwned || len(h.d.scheduledClosures()) != 0 {
		t.Fatal("explicit resume did not retain its takeover semantics")
	}
}
