package daemon

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/eventlog"
)

func TestFailedReverseMoveRetainsOwnerUntilRestartRecovery(t *testing.T) {
	for _, image := range []bool{false, true} {
		for _, afterRename := range []bool{false, true} {
			name := "file"
			if image {
				name = "image"
			}
			if afterRename {
				name += "/after-rename"
			} else {
				name += "/before-rename"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
				u := newUploads()
				var imageIDs, fileIDs []string
				id := ""
				if image {
					id = storageUpload(t, u)
					imageIDs = []string{id}
				} else {
					id = uploadFile(t, u, "recover.bin", []byte("recoverable"))
					fileIDs = []string{id}
				}
				tr := u.byFile(id)
				if image {
					tr = u.byImage(id)
				}
				original := u.pending(tr.relativeName())
				sessionPath := filepath.Join(u.sessionDir("s"), filepath.Base(original))
				if !image {
					sessionPath = filepath.Join(u.sessionDir("s"), "files", tr.relativeName())
				}
				forwardSyncs, reverseSyncs := 0, 0
				failed := false
				failure := errors.New("injected upload directory sync failure")
				u.syncDir = func(dir string) error {
					if dir == filepath.Dir(sessionPath) && !failed {
						forwardSyncs++
						if forwardSyncs == 2 {
							failed = true
							return failure
						}
					}
					if failed && dir == filepath.Dir(original) {
						reverseSyncs++
						if !afterRename || reverseSyncs >= 2 {
							return failure
						}
					}
					return syncDir(dir)
				}
				if _, err := u.takeUploads(imageIDs, fileIDs, "phone", "s", "rejected"); !errors.Is(err, failure) {
					t.Fatal(err)
				}
				retained, err := u.load(tr.ID)
				if err != nil {
					t.Fatal(err)
				}
				if retained.Session != "s" || !retained.Returning || len(retained.Refs) != 0 {
					t.Fatalf("lost recoverable ownership: %+v", retained)
				}
				where := sessionPath
				if afterRename {
					where = original
				}
				if _, err := os.Stat(where); err != nil {
					t.Fatal("rollback lost bytes", err)
				}
				// A restart with the fault still present must not let queue
				// reference restoration remove an unexposed returning image.
				log, err := eventlog.Open(filepath.Join(t.TempDir(), "events.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				u.reconcile()
				u.restoreRefs(&session{meta: meta{ID: "s"}, queue: &queueState{}, log: log}, []*transfer{retained})
				log.Close()
				u.gc(time.Now())
				if _, err := os.Stat(where); err != nil {
					t.Fatal("GC deleted an unresolved return", err)
				}
				restarted := &uploads{dir: u.dir, quota: maxUploadStorage}
				restarted.reconcile()
				restored, err := restarted.load(tr.ID)
				if err != nil {
					t.Fatal(err)
				}
				if restored.Session != "" || restored.Returning || len(restored.Refs) != 0 {
					t.Fatalf("return not reconciled: %+v", restored)
				}
				if _, err := os.Stat(original); err != nil {
					t.Fatal("recovery lost pending bytes", err)
				}
				if err := restarted.checkUploads(imageIDs, fileIDs, "phone"); err != nil {
					t.Fatal("recovered upload cannot be accepted", err)
				}
			})
		}
	}
}

func TestGCResyncsCompletionRecordBeforeDeletingRetainedPart(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	data := []byte("durable")
	sum := sha256.Sum256(data)
	_, err := u.begin(&pb.BeginImageUploadRequest{TransferId: tid, FileName: "durable.bin", TotalBytes: int64(len(data)), Sha256: sum[:]}, "phone")
	if err != nil {
		t.Fatal(err)
	}
	_, err = u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "phone")
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	u.syncDir = func(dir string) error {
		if filepath.Base(dir) == "partial" {
			attempts++
			return errors.New("metadata sync failure")
		}
		return syncDir(dir)
	}
	if _, err := u.complete(tid, "phone"); !fileReplaced(err) {
		t.Fatal(err)
	}
	before := attempts
	u.gc(time.Now())
	if attempts <= before {
		t.Fatal("GC did not retry metadata durability")
	}
	if _, err := os.Stat(u.partial(tid) + ".part"); err != nil {
		t.Fatal("GC removed the only retained original before sync", err)
	}
	synced := false
	u.syncDir = func(dir string) error {
		if filepath.Base(dir) == "partial" && !synced {
			if _, err := os.Stat(u.partial(tid) + ".part"); err != nil {
				t.Fatal("partial unlinked before metadata sync", err)
			}
			synced = true
		}
		return syncDir(dir)
	}
	u.gc(time.Now())
	if !synced {
		t.Fatal("completion record not synced")
	}
	if _, err := os.Stat(u.partial(tid) + ".part"); !os.IsNotExist(err) {
		t.Fatal("durable original not released")
	}
}

func TestReleaseVanishedFileDoesNotLeavePermanentSessionOwnership(t *testing.T) {
	for _, returning := range []bool{false, true} {
		t.Run(map[bool]string{false: "claimed", true: "returning"}[returning], func(t *testing.T) {
			t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
			u := newUploads()
			id := uploadFile(t, u, "vanished.bin", []byte("gone"))
			all, err := u.takeUploads(nil, []string{id}, "phone", "unfinished", "first")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(all.Files[0].Path); err != nil {
				t.Fatal(err)
			}
			if returning {
				tr := u.byFile(id)
				tr.Returning = true
				if err := u.save(tr); err != nil {
					t.Fatal(err)
				}
			}
			u.release("unfinished")
			tr := u.byFile(id)
			if tr == nil || tr.Session != "" || tr.Returning || len(tr.Refs) != 0 {
				t.Fatalf("vanished file retains owner: %+v", tr)
			}
			u.gc(time.Now().Add(unusedTTL + time.Second))
			if _, err := os.Stat(u.partial(tr.ID) + ".json"); !os.IsNotExist(err) {
				t.Fatal("unused missing file record never expires")
			}
		})
	}
}
func TestPendingReturnKeepsExactFilename(t *testing.T) {
	// Exercise the directory path form as well as the image filename form.
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	id := uploadFile(t, u, "报告+数据.csv", []byte("a,b"))
	all, err := u.takeUploads(nil, []string{id}, "phone", "s", "r")
	if err != nil {
		t.Fatal(err)
	}
	tr := u.byFile(id)
	tr.Returning = true
	tr.Refs = map[string]bool{"rejected-before-crash": true}
	if err := u.save(tr); err != nil {
		t.Fatal(err)
	}
	u.reconcile()
	restored := u.byFile(id)
	if restored.Session != "" || len(restored.Refs) != 0 || !strings.HasSuffix(u.pending(restored.relativeName()), "报告+数据.csv") {
		t.Fatal("filename changed during recovery")
	}
	if _, err := os.Stat(all.Files[0].Path); !os.IsNotExist(err) {
		t.Fatal("session copy remains after return")
	}
}

func TestRetryCannotAcceptWhileRejectedClaimIsStillReturning(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	id := uploadFile(t, u, "retry.bin", []byte("retry"))
	old, err := u.takeUploads(nil, []string{id}, "phone", "first", "rejected")
	if err != nil {
		t.Fatal(err)
	}
	tr := u.byFile(id)
	tr.Returning = true
	tr.Refs = nil
	if err := u.save(tr); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("pending directory unavailable")
	u.syncDir = func(dir string) error {
		if dir == filepath.Dir(u.pending(tr.relativeName())) {
			return failure
		}
		return syncDir(dir)
	}
	if _, err := u.takeUploads(nil, []string{id}, "phone", "first", "retry"); !errors.Is(err, failure) {
		t.Fatal("unresolved return was accepted", err)
	}
	if _, err := os.Stat(old.Files[0].Path); err != nil {
		t.Fatal(err)
	}
	u.syncDir = nil
	next, err := u.takeUploads(nil, []string{id}, "phone", "next", "retry")
	if err != nil {
		t.Fatal(err)
	}
	tr = u.byFile(id)
	if tr.Session != "next" || tr.Returning || !next.Files[0].NewClaim {
		t.Fatalf("retry retained old return intent: %+v", tr)
	}
	u.gc(time.Now())
	if _, err := os.Stat(next.Files[0].Path); err != nil {
		t.Fatal("GC reversed an accepted retry", err)
	}
}
