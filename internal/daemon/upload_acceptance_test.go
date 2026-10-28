package daemon

import (
	"bytes"
	"connectrpc.com/connect"
	"errors"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/tmux"
)

// Extend the transfer fault matrix through actual send/create acceptance.
// Each panic skips caller rollback; recovery reloads only durable state.
func TestUploadClaimAcceptanceCrashMatrix(t *testing.T) {
	stages := []string{"move-intent", "move-directory", "move-rename", "move-destination", "move-source", "claim-reference", "queue-commit", "queue-visible", "acceptance-record"}
	for _, image := range []bool{false, true} {
		for _, create := range []bool{false, true} {
			path := "send"
			if create {
				path = "create"
			}
			points := append([]string{}, stages...)
			if create {
				points = append(points, "create-cli", "create-cli-id", "create-cli-status", "create-starting-event")
			}
			for _, stage := range points {
				for _, after := range []bool{false, true} {
					kind := "file"
					if image {
						kind = "image"
					}
					where := "before"
					if after {
						where = "after"
					}
					t.Run(strings.Join([]string{kind, path, stage, where}, "/"), func(t *testing.T) {
						r := newRestoreHarness(t)
						r.d.cfg.PollInterval = time.Hour
						r.d.agents[pb.Agent_AGENT_CODEX] = codex.New("unused", "", "")
						u := r.d.uploads
						var ims, files []string
						var tr *transfer
						if image {
							ims = []string{storageUpload(t, u)}
							tr = u.byImage(ims[0])
						} else {
							files = []string{uploadFile(t, u, "claim.bin", []byte("original"))}
							tr = u.byFile(files[0])
						}
						want, err := os.ReadFile(u.pending(tr.relativeName()))
						if err != nil {
							t.Fatal(err)
						}
						var s *session
						if !create {
							sid := "00000000000000aa"
							r.save(meta{ID: sid}, true)
							if err := r.restore(); err != nil {
								t.Fatal(err)
							}
							s = r.session(sid)
							s.stop()
							<-s.done
						}
						hit := false
						token := new(int)
						registered := false
						r.d.syncStateDir = func(dir string) error {
							if stage == "queue-visible" && !after {
								hit = true
								panic(token)
							}
							if err := syncDir(dir); err != nil {
								return err
							}
							if stage == "queue-visible" && after {
								hit = true
								panic(token)
							}
							return nil
						}
						u.beforeUploadStep = func(step string) error {
							if step == stage && !after {
								hit = true
								panic(token)
							}
							return nil
						}
						u.afterUploadStep = func(step string) {
							if step == "create-cli-id" {
								registered = true
							}
							if step == stage && after {
								hit = true
								panic(token)
							}
						}
						func() {
							defer func() {
								if p := recover(); p != nil && p != token {
									panic(p)
								}
							}()
							if create {
								_, _ = r.d.createBy(&pb.CreateSessionRequest{Cwd: t.TempDir(), Agent: pb.Agent_AGENT_CODEX, Prompt: "first", ImageIds: ims, FileIds: files}, &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "phone"}, false, false, "test PATH")
							} else {
								_, _ = s.enqueueInput("send", "request", nil, nil, originLocal, pb.SendMode_SEND_MODE_QUEUE, nil, func(request string) (messageUploads, error) {
									return u.takeUploads(ims, files, "phone", s.meta.ID, request)
								})
							}
						}()
						if !hit {
							t.Fatal("crash boundary not reached")
						}
						// The create API's local origin owns these fixtures too. No real tmux
						// process runs; registration reproduces which CLI startup can recognize.
						entries, err := os.ReadDir(paths.SessionsDir())
						if err != nil {
							t.Fatal(err)
						}
						sid := ""
						for _, e := range entries {
							if e.IsDir() {
								sid = e.Name()
							}
						}
						if sid == "" {
							t.Fatal("session metadata missing")
						}
						if create && registered {
							m, err := loadMeta(paths.SessionDir(sid))
							if err != nil {
								t.Fatal(err)
							}
							r.infos = []tmux.SessionInfo{{Name: m.Tmux, SessionID: sid, PanePID: 1}}
							if err := os.WriteFile(r.alive, []byte(m.Tmux+"\t"+sid+"\t1\n"), 0600); err != nil {
								t.Fatal(err)
							}
						}
						if s != nil {
							_ = s.log.Close()
						}
						r.d.sessions = map[string]*session{}
						// Rebuild upload state, finish moves, then restore queue/history using
						// the production startup order and actual session restoration routine.
						recovered := newUploads()
						r.d.uploads = recovered
						recovered.reconcile()
						if err := r.restore(); err != nil {
							t.Fatal(err)
						}
						accepted := stage == "queue-commit" && after || stage == "queue-visible" || stage == "acceptance-record"
						got, err := recovered.load(tr.ID)
						if err != nil {
							t.Fatal("claim lost its record", err)
						}
						if accepted {
							if got.Session != sid {
								t.Fatal("accepted upload lost owner", got)
							}
							data, err := os.ReadFile(got.sessionPath(recovered))
							if err != nil || !bytes.Equal(data, want) {
								t.Fatal("accepted bytes lost", err)
							}
						} else {
							if got.Session != "" || got.Returning {
								t.Fatalf("never-accepted upload remains claimed: %+v", got)
							}
							data, err := os.ReadFile(recovered.pending(got.relativeName()))
							if err != nil || !bytes.Equal(data, want) {
								t.Fatal("rejected bytes not returned", err)
							}
							if err := recovered.checkUploads(ims, files, "phone"); err != nil {
								t.Fatal("unaccepted upload cannot be reused", err)
							}
						}
					})
				}
			}
		}
	}
}

func TestAcceptanceWitnessSurvivesMarkerFailureAndCancellation(t *testing.T) {
	for _, image := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "image"}[image], func(t *testing.T) {
			h := newHarness(t, Config{})
			u := h.d.uploads
			var ims, files []string
			var tr *transfer
			if image {
				ims = []string{storageUpload(t, u)}
				tr = u.byImage(ims[0])
			} else {
				files = []string{uploadFile(t, u, "accepted.bin", []byte("accepted"))}
				tr = u.byFile(files[0])
			}
			failRecords := false
			u.beforeUploadStep = func(stage string) error {
				if stage == "acceptance-record" {
					failRecords = true
				}
				return nil
			}
			u.syncDir = func(dir string) error {
				if failRecords && dir == filepath.Dir(u.partial(tr.ID)) {
					return errors.New("upload metadata fsync failed")
				}
				return syncDir(dir)
			}
			// Lose the QUEUED notification too; the queue's witness must be enough.
			h.s.log.Close()
			var beforeMarker []byte
			_, err := h.s.enqueueInput("file", "r", nil, nil, originLocal, pb.SendMode_SEND_MODE_QUEUE, nil, func(request string) (messageUploads, error) {
				all, err := u.takeUploads(ims, files, "phone", h.s.meta.ID, request)
				if err == nil {
					beforeMarker, err = os.ReadFile(u.partial(tr.ID) + ".json")
				}
				return all, err
			})
			if !queueAccepted(err) {
				t.Fatal("queue not accepted", err)
			}
			// Repair the event writer. Only a cancellation (without file metadata)
			// will be in history; there was never a durable QUEUED notification.
			log, err := eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			h.s.log = log
			if err := h.s.cancelInput("r"); err != nil {
				t.Fatal(err)
			}
			if len(h.s.queue.Queue) != 0 {
				t.Fatal("cancellation did not leave queue")
			}
			disk, err := loadQueue(h.dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(disk.PendingUploads) != 1 || disk.PendingUploads[0] != tr.artifactID() {
				t.Fatal("cancellation lost durable acceptance witness", disk.PendingUploads)
			}
			// A true marker was visible after the failed fsync, but not durable. Model
			// its rollback to the last committed claim, with its old unaccepted refs.
			if err := writeFileSync(u.partial(tr.ID)+".json", beforeMarker); err != nil {
				t.Fatal(err)
			}
			fresh := newUploads()
			fresh.reconcile()
			records, err := fresh.referenceRecords()
			if err != nil {
				t.Fatal(err)
			}
			fresh.restoreRefs(&session{meta: h.s.meta, queue: disk, log: log}, records[h.s.meta.ID])
			got, err := fresh.load(tr.ID)
			if image {
				if connect.CodeOf(err) != connect.CodeNotFound {
					t.Fatal("accepted cancelled image was returned instead of deleted", got, err)
				}
			} else {
				if err != nil || !got.Accepted || got.Session != h.s.meta.ID || len(got.Refs) != 0 {
					t.Fatal("previously accepted file was lost", got, err)
				}
				if _, err := os.Stat(got.sessionPath(fresh)); err != nil {
					t.Fatal(err)
				}
			}
			// Retire the bridge only after marker durability, and persist its removal
			// on the following write; it cannot grow with past settled messages.
			h.d.uploads = fresh
			h.s.queue = disk
			for range 2 {
				h.s.mu.Lock()
				saved, err := h.s.saveQueueLocked()
				h.s.mu.Unlock()
				if !saved || err != nil {
					t.Fatal(err)
				}
			}
			disk, err = loadQueue(h.dir)
			if err != nil || len(disk.PendingUploads) != 0 {
				t.Fatal("durable witness was never retired", err)
			}
		})
	}
}

func TestUncertainQueueRenameDoesNotPromoteUploadAcceptance(t *testing.T) {
	for _, image := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "image"}[image], func(t *testing.T) {
			h := newHarness(t, Config{})
			u := h.d.uploads
			if err := h.s.queue.save(h.dir); err != nil {
				t.Fatal(err)
			}
			old, err := os.ReadFile(filepath.Join(h.dir, "queue.json"))
			if err != nil {
				t.Fatal(err)
			}
			var ims, files []string
			var tr *transfer
			if image {
				ims = []string{storageUpload(t, u)}
				tr = u.byImage(ims[0])
			} else {
				files = []string{uploadFile(t, u, "uncertain.bin", []byte("uncertain"))}
				tr = u.byFile(files[0])
			}
			h.d.syncStateDir = func(string) error { return errors.New("queue fsync failed") }
			_, err = h.s.enqueueInput("uncertain", "r", nil, nil, originLocal, pb.SendMode_SEND_MODE_QUEUE, nil, func(request string) (messageUploads, error) {
				return u.takeUploads(ims, files, "phone", h.s.meta.ID, request)
			})
			if !queueAccepted(err) || !fileReplaced(err) {
				t.Fatal("expected an uncertain visible queue", err)
			}
			got, err := u.load(tr.ID)
			if err != nil || got.Accepted {
				t.Fatal("uncertain queue prematurely marked accepted", got, err)
			}
			// Crash may restore the old directory entry. There is then no durable
			// queue/history witness, so neither file nor image may remain claimed.
			if err := writeFileSync(filepath.Join(h.dir, "queue.json"), old); err != nil {
				t.Fatal(err)
			}
			disk, err := loadQueue(h.dir)
			if err != nil {
				t.Fatal(err)
			}
			fresh := newUploads()
			fresh.reconcile()
			records, err := fresh.referenceRecords()
			if err != nil {
				t.Fatal(err)
			}
			fresh.restoreRefs(&session{meta: h.s.meta, queue: disk, log: h.s.log}, records[h.s.meta.ID])
			if err := fresh.checkUploads(ims, files, "phone"); err != nil {
				t.Fatal("unaccepted claim not returned", err)
			}
		})
	}
}

func TestAcceptedFileSurvivesWithoutCurrentQueueReference(t *testing.T) {
	for _, proof := range []string{"marker", "queued-history", "user-history", "held", "reused-request", "unreadable-queue"} {
		t.Run(proof, func(t *testing.T) {
			h := newHarness(t, Config{})
			u := h.d.uploads
			id := uploadFile(t, u, "kept.bin", []byte("keep"))
			all, err := u.takeUploads(nil, []string{id}, "phone", h.s.meta.ID, "reused")
			if err != nil {
				t.Fatal(err)
			}
			tr := u.byFile(id)
			q := &queueState{}
			switch proof {
			case "marker":
				if pending := u.confirmAcceptance(h.s.meta.ID, []string{id}); len(pending) != 0 {
					t.Fatal(pending)
				}
			case "held":
				q.Held = []input{{RequestID: "held", Files: all.Files}}
			case "queued-history", "reused-request":
				fid := id
				if proof == "reused-request" {
					fid = newID(16)
				}
				if err := h.s.log.Append(h.s.meta.ID, []*pb.Event{{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: "reused", Status: pb.InputStatus_INPUT_STATUS_QUEUED, Files: []*pb.SentFile{{FileId: fid}}}}}}, "", 0); err != nil {
					t.Fatal(err)
				}
			case "user-history":
				if err := h.s.log.Append(h.s.meta.ID, []*pb.Event{{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Files: sentFiles(all.Files)}}}}, "", 0); err != nil {
					t.Fatal(err)
				}
			}
			records, err := u.referenceRecords()
			if err != nil {
				t.Fatal(err)
			}
			u.restoreRefs(&session{meta: h.s.meta, queue: q, queueLost: proof == "unreadable-queue", log: h.s.log}, records[h.s.meta.ID])
			got, err := u.load(tr.ID)
			if err != nil {
				t.Fatal(err)
			}
			if proof == "reused-request" {
				if got.Session != "" {
					t.Fatal("another file's accepted request ID protected a new claim")
				}
			} else if got.Session != h.s.meta.ID || (!got.Accepted && proof != "unreadable-queue") {
				t.Fatal("accepted/unknown file returned", got)
			}
		})
	}
}

func TestAcceptanceWitnessSurvivesUnreadableUploadRecord(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.d.uploads
	id := uploadFile(t, u, "accepted.bin", []byte("keep"))
	tr := u.byFile(id)
	var claim []byte
	u.afterUploadStep = func(stage string) {
		if stage == "queue-commit" {
			if err := os.WriteFile(u.partial(tr.ID)+".json", []byte("unreadable record"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.s.log.Close() // No QUEUED artifact evidence can be recorded.
	_, err := h.s.enqueueInput("file", "r", nil, nil, originLocal, pb.SendMode_SEND_MODE_QUEUE, nil, func(request string) (messageUploads, error) {
		all, err := u.takeUploads(nil, []string{id}, "phone", h.s.meta.ID, request)
		if err == nil {
			claim, err = os.ReadFile(u.partial(tr.ID) + ".json")
		}
		return all, err
	})
	if !queueAccepted(err) {
		t.Fatal(err)
	}
	log, err := eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	h.s.log = log
	if err := h.s.cancelInput("r"); err != nil {
		t.Fatal(err)
	}
	disk, err := loadQueue(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(disk.PendingUploads) != 1 || disk.PendingUploads[0] != id {
		t.Fatal("an unreadable record retired its acceptance witness", disk.PendingUploads)
	}
	// Restore the last readable record: lack of marker cannot undo the queue's
	// durable acceptance, even though both current queue and history omit files.
	if err := writeFileSync(u.partial(tr.ID)+".json", claim); err != nil {
		t.Fatal(err)
	}
	fresh := newUploads()
	records, err := fresh.referenceRecords()
	if err != nil {
		t.Fatal(err)
	}
	fresh.restoreRefs(&session{meta: h.s.meta, queue: disk, log: log}, records[h.s.meta.ID])
	got, err := fresh.load(tr.ID)
	if err != nil || !got.Accepted || got.Session != h.s.meta.ID {
		t.Fatal("accepted file lost after record repair", got, err)
	}
}

func TestDurableAcceptanceDoesNotKeepRewritingMarkers(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.d.uploads
	id := uploadFile(t, u, "kept.bin", []byte("keep"))
	writes := 0
	u.afterUploadStep = func(stage string) {
		if stage == "acceptance-record" {
			writes++
		}
	}
	_, err := h.s.enqueueInput("file", "r", nil, nil, originLocal, pb.SendMode_SEND_MODE_QUEUE, nil, func(request string) (messageUploads, error) {
		return u.takeUploads(nil, []string{id}, "phone", h.s.meta.ID, request)
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		h.s.mu.Lock()
		saved, err := h.s.saveQueueLocked()
		h.s.mu.Unlock()
		if !saved || err != nil {
			t.Fatal(err)
		}
	}
	if writes != 1 {
		t.Fatalf("durable marker rewritten on unrelated queue saves: %d", writes)
	}
	disk, err := loadQueue(h.dir)
	if err != nil || len(disk.PendingUploads) != 0 {
		t.Fatal("settled acceptance witness still in queue", err)
	}
}
