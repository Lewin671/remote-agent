package daemon

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/tmux"
)

func TestStartupPreservesUploadsUntilSessionLivenessIsRestored(t *testing.T) {
	for _, image := range []bool{false, true} {
		for _, state := range []string{"alive", "gone", "list-error"} {
			kind := "file"
			if image {
				kind = "image"
			}
			t.Run(kind+"/"+state, func(t *testing.T) {
				r := newRestoreHarness(t)
				r.d.cfg.PollInterval = time.Hour
				const sid = "00000000000000aa"
				dir := r.save(meta{ID: sid, Exited: true}, state == "alive")
				u := r.d.uploads
				var ims, files []string
				if image {
					ims = []string{storageUpload(t, u)}
				} else {
					files = []string{uploadFile(t, u, "keep.bin", []byte("still in use"))}
				}
				taken, err := u.takeUploads(ims, files, "phone", sid, "accepted")
				if err != nil {
					t.Fatal(err)
				}
				path := ""
				if image {
					path = taken.Images[0].Path
					if err := u.expose(taken.Images); err != nil {
						t.Fatal(err)
					}
				} else {
					path = taken.Files[0].Path
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				// Run uses this exact order: transfer reconciliation precedes tmux
				// discovery. Exited on disk alone cannot authorize byte deletion here.
				u.reconcile()
				if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
					t.Fatalf("reconcile deleted attachments before checking liveness: %v", err)
				}
				if state == "list-error" {
					failure := errors.New("tmux listing failed")
					if err := r.d.restoreWithSessionList(func() ([]tmux.SessionInfo, error) { return nil, failure }); !errors.Is(err, failure) {
						t.Fatal(err)
					}
					if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
						t.Fatal("failed restoration deleted attachments", err)
					}
					return
				}
				if err := r.restore(); err != nil {
					t.Fatal(err)
				}
				restored, err := loadMeta(dir)
				if err != nil {
					t.Fatal(err)
				}
				if restored.Exited != (state == "gone") {
					t.Fatal("session liveness not restored", restored.Exited)
				}
				// The production image GC starts only after restore returns. Both
				// originals and ownership records must survive a supported reattach.
				u.gc(time.Now())
				if state == "alive" {
					if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
						t.Fatal("reattached session lost bytes", err)
					}
					tr := u.byFile(firstID(files))
					if image {
						tr = u.byImage(ims[0])
					}
					if tr == nil || tr.Session != sid || tr.Deleting {
						t.Fatal("reattached session lost ownership", tr)
					}
				} else if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("confirmed exited session was not cleaned", err)
				}
			})
		}
	}
}

func TestLiveUploadProtectionDoesNotDependOnReadableHistory(t *testing.T) {
	for _, image := range []bool{false, true} {
		for _, failure := range []string{"history", "metadata"} {
			kind := "file"
			if image {
				kind = "image"
			}
			t.Run(kind+"/"+failure, func(t *testing.T) {
				r := newRestoreHarness(t)
				r.d.cfg.PollInterval = time.Hour
				const sid = "00000000000000aa"
				dir := r.save(meta{ID: sid, Exited: true}, true)
				u := r.d.uploads
				var ims, files []string
				if image {
					ims = []string{storageUpload(t, u)}
				} else {
					files = []string{uploadFile(t, u, "keep.bin", []byte("keep"))}
				}
				all, err := u.takeUploads(ims, files, "phone", sid, "accepted")
				if err != nil {
					t.Fatal(err)
				}
				path := ""
				if image {
					path = all.Images[0].Path
					if err := u.expose(all.Images); err != nil {
						t.Fatal(err)
					}
				} else {
					path = all.Files[0].Path
				}
				if failure == "history" {
					if err := os.Mkdir(filepath.Join(dir, "events.jsonl"), 0700); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), nil, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(dir, 0500); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
					if f, err := os.CreateTemp(dir, "writable-check"); err == nil {
						f.Close()
						_ = os.Remove(f.Name())
						t.Skip("directory writable despite chmod")
					}
				}
				u.reconcile()
				err = r.restore()
				if failure == "metadata" {
					if err == nil {
						t.Fatal("startup continued after failing to persist live session correction")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					m, err := loadMeta(dir)
					if err != nil || m.Exited {
						t.Fatal("history failure left a live session marked exited", err)
					}
					// Startup may skip unreadable history, but storage cleanup must honor
					// the independently established live-session state on disk.
					u.gc(time.Now())
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatal("live session lost upload", err)
				}
			})
		}
	}
}
