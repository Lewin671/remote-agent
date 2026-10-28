package daemon

import (
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// The first Complete pauses outside u.mu. A concurrent Complete may finish
// before Abort, exercising both the receiving and already-complete reloads.
func TestHashReloadRejectsDeletionTombstone(t *testing.T) {
	for _, completed := range []bool{false, true} {
		for _, syncFailure := range []bool{false, true} {
			for _, gc := range []bool{false, true} {
				name := "receiving"
				if completed {
					name = "complete"
				}
				if syncFailure {
					name += "/dir-sync"
				} else {
					name += "/unlink"
				}
				if gc {
					name += "/gc"
				} else {
					name += "/restart"
				}
				t.Run(name, func(t *testing.T) {
					t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
					u := newUploads()
					data := []byte("original bytes")
					sum := sha256.Sum256(data)
					if _, err := u.begin(&pb.BeginImageUploadRequest{TransferId: tid, FileName: "race.bin", TotalBytes: int64(len(data)), Sha256: sum[:]}, "phone"); err != nil {
						t.Fatal(err)
					}
					if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "phone"); err != nil {
						t.Fatal(err)
					}
					hashing, resume := make(chan struct{}), make(chan struct{})
					var calls atomic.Int32
					u.hashFile = func(w io.Writer, r io.Reader) (int64, error) {
						if calls.Add(1) == 1 {
							close(hashing)
							<-resume
						}
						return io.Copy(w, r)
					}
					type result struct {
						status *pb.ImageUploadStatus
						err    error
					}
					done := make(chan result, 1)
					go func() { st, err := u.complete(tid, "phone"); done <- result{st, err} }()
					<-hashing
					// Always release the worker before temporary state is removed, even if
					// setup or the regression assertion fails. No sleeps order the race.
					joined := false
					defer func() {
						if !joined {
							close(resume)
							<-done
						}
					}()
					if completed {
						if _, err := u.complete(tid, "phone"); err != nil {
							t.Fatal(err)
						}
					}
					stage := "delete-part"
					if completed {
						stage = "delete-bytes"
					}
					if syncFailure {
						stage = "delete-source"
						if completed {
							stage = "delete-directory"
						}
					}
					fault := errors.New("injected abort I/O failure")
					u.beforeUploadStep = func(s string) error {
						if s == stage {
							return fault
						}
						return nil
					}
					if err := u.abort(tid, "phone"); !errors.Is(err, fault) {
						t.Fatal("abort did not reach fault", err)
					}
					tombstone, err := u.load(tid)
					if err != nil || !tombstone.Deleting {
						t.Fatal("abort did not persist deletion", err)
					}
					close(resume)
					got := <-done
					joined = true
					if got.status != nil || connect.CodeOf(got.err) != connect.CodeNotFound {
						t.Fatalf("hash completion accepted deleted transfer: status=%v err=%v", got.status, got.err)
					}
					after, err := u.load(tid)
					if err != nil {
						t.Fatal(err)
					}
					if !after.Deleting || after.FileID != tombstone.FileID || after.CompletedMs != tombstone.CompletedMs {
						t.Fatal("hash completion changed deletion tombstone")
					}
					recovered := newUploads()
					if gc {
						recovered.gc(time.Now())
					} else {
						recovered.reconcile()
					}
					if _, err := os.Stat(u.partial(tid) + ".json"); !os.IsNotExist(err) {
						t.Fatal("deletion was not recovered", err)
					}
				})
			}
		}
	}
}

func TestRestoreRefsReloadsSnapshotBeforeWriting(t *testing.T) {
	for _, image := range []bool{false, true} {
		for _, changed := range []string{"deleting", "returning", "removed"} {
			kind := "file"
			if image {
				kind = "image"
			}
			t.Run(kind+"/"+changed, func(t *testing.T) {
				h := newHarness(t, Config{})
				u := h.d.uploads
				var ims, files []string
				if image {
					ims = []string{storageUpload(t, u)}
				} else {
					files = []string{uploadFile(t, u, "snapshot.bin", []byte("keep"))}
				}
				all, err := u.takeUploads(ims, files, "phone", h.s.meta.ID, "r")
				if err != nil {
					t.Fatal(err)
				}
				h.s.queue.Queue = []input{{RequestID: "r", Images: all.Images, Files: all.Files}}
				snapshots, err := u.referenceRecords()
				if err != nil {
					t.Fatal(err)
				}
				old := snapshots[h.s.meta.ID][0]
				current, err := u.load(old.ID)
				if err != nil {
					t.Fatal(err)
				}
				switch changed {
				case "deleting":
					u.beforeUploadStep = func(stage string) error {
						if stage == "delete-bytes" {
							return errors.New("unlink failed")
						}
						return nil
					}
					u.dropSession(h.s.meta.ID)
				case "returning":
					current.Returning = true
					current.Refs = nil
					if err := u.save(current); err != nil {
						t.Fatal(err)
					}
				case "removed":
					u.dropSession(h.s.meta.ID)
				}
				// The snapshot predates deletion/return and still contains its old refs.
				u.restoreRefs(h.s, snapshots[h.s.meta.ID])
				got, err := u.load(old.ID)
				if changed == "removed" {
					if connect.CodeOf(err) != connect.CodeNotFound {
						t.Fatal("restored a deleted record", got, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if changed == "deleting" && !got.Deleting {
					t.Fatal("stale snapshot cleared deletion intent")
				}
				if changed == "returning" && (!got.Returning || len(got.Refs) != 0) {
					t.Fatal("stale snapshot changed return intent")
				}
				recovered := newUploads()
				recovered.reconcile()
				if changed == "deleting" {
					if _, err := os.Stat(u.partial(old.ID) + ".json"); !os.IsNotExist(err) {
						t.Fatal("deletion not recovered", err)
					}
				} else {
					if err := recovered.checkUploads(ims, files, "phone"); err != nil {
						t.Fatal("return not recovered", err)
					}
				}
			})
		}
	}
}
