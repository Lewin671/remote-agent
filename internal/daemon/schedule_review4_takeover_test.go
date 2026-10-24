package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestAcceptedHumanQueueOwnsSessionWithoutQueuedEvent(t *testing.T) {
	origins := []*pb.Origin{originLocal, {Kind: pb.OriginKind_ORIGIN_KIND_CLIENT}, originSystem, {Kind: pb.OriginKind_ORIGIN_KIND_UNSPECIFIED}, nil}
	for _, origin := range origins {
		for _, fault := range []string{"event", "directory-sync", "none"} {
			t.Run(origin.GetKind().String()+fault, func(t *testing.T) {
				h, _, snapshot, _ := unpublishedRun(t, false)
				if err := h.s.meta.save(h.s.dir); err != nil {
					t.Fatal(err)
				}
				defer func() { h.s.log.Close() }()
				h.d.queueScheduledSession(t.Context(), snapshot, snapshot.Active.StartedAt, h.s)
				if err := h.d.SessionPublicationFailed(h.s.meta.ID, "relay_session_limit"); err != nil {
					t.Fatal(err)
				}
				if len(h.d.scheduledClosures()) != 1 {
					t.Fatal("missing quota closer")
				}
				h.s.scheduledTask, h.s.scheduledRun = "current-task", "current-run"
				if fault == "event" {
					h.s.log.Close()
				}
				if fault == "directory-sync" {
					h.d.syncStateDir = failStateSync()
				}
				_, err := h.s.enqueueMode("accepted input", "accepted-input", nil, origin, pb.SendMode_SEND_MODE_QUEUE)
				if !queueAccepted(err) || fault != "none" && err == nil {
					t.Fatalf("not durably accepted: %v", err)
				}
				human := humanOrigin(origin)
				if h.s.info().TaskOwned == human {
					t.Fatal("canonical accepted queue did not determine ownership")
				}
				stored, err := loadMeta(h.s.dir)
				if err != nil {
					t.Fatal(err)
				}
				if stored.TaskOwned == human {
					t.Fatal("canonical takeover was not persisted")
				}
				if h.s.scheduledTask != "current-task" || h.s.scheduledRun != "current-run" {
					t.Fatal("queued human input released current model restrictions")
				}
				if fault == "event" {
					for _, e := range h.events() {
						if e.GetInputState().GetRequestId() == "accepted-input" {
							t.Fatal("test unexpectedly had a QUEUED event")
						}
					}
					repairEventLog(t, h)
				}
				h.d.syncStateDir = nil
				if human {
					if err := h.s.killIf(context.Background(), true); err != nil {
						t.Fatal(err)
					}
					if !h.term.Alive() {
						t.Fatal("automatic quota closure swallowed accepted human input")
					}
				}
			})
		}
	}
}

func TestCanonicalHumanQueueRepairsOwnershipOnRestore(t *testing.T) {
	for _, held := range []bool{false, true} {
		for _, origin := range []*pb.Origin{originLocal, {Kind: pb.OriginKind_ORIGIN_KIND_CLIENT}} {
			t.Run(origin.Kind.String()+map[bool]string{false: "queued", true: "held"}[held], func(t *testing.T) {
				h := scheduleHarness(t)
				defer func() { h.s.log.Close() }()
				h.s.meta.TaskOwned = true
				h.s.meta.ScheduledTaskID = "task"
				if err := h.s.meta.save(h.s.dir); err != nil {
					t.Fatal(err)
				}
				pending := input{RequestID: "accepted-before-restart", Text: "human", Origin: storeOrigin(origin)}
				if held {
					h.s.queue.Held = []input{pending}
				} else {
					h.s.queue.Queue = []input{pending}
				}
				// Simulate the durable queue / missing event and metadata boundary,
				// without calling the acceptance projection being tested on restore.
				if err := h.s.queue.save(h.s.dir); err != nil {
					t.Fatal(err)
				}
				restartSession(t, h)
				persisted, err := loadMeta(h.s.dir)
				if err != nil {
					t.Fatal(err)
				}
				if h.s.info().TaskOwned || persisted.TaskOwned {
					t.Fatal("restart lost canonical human takeover")
				}
			})
		}
	}
}

func TestRejectedHumanQueueDoesNotTakeOwnership(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	h.s.meta.TaskOwned = true
	if err := h.s.meta.save(h.s.dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.s.dir, "queue.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := h.s.send("rejected", "rejected")
	if err == nil || queueAccepted(err) || !h.s.info().TaskOwned || len(h.s.queue.Queue) != 0 {
		t.Fatal("rejected input took over task session")
	}
}
