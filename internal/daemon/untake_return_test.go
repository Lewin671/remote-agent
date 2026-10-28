package daemon

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUntakePersistsReturnIntentAndRecoversIOFailures(t *testing.T) {
	for _, release := range []bool{false, true} {
		for _, image := range []bool{false, true} {
			for _, fault := range []string{"mkdir", "rename", "intent-sync", "pending-sync", "session-sync"} {
				for _, recovery := range []string{"restart", "gc"} {
					kind := "file"
					if image {
						kind = "image"
					}
					if release {
						kind += "/release"
					}
					t.Run(kind+"/"+fault+"/"+recovery, func(t *testing.T) {
						t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
						u := newUploads()
						var images, files []string
						var tr *transfer
						if image {
							id := storageUpload(t, u)
							images = []string{id}
							tr = u.byImage(id)
						} else {
							id := uploadFile(t, u, "rejected.bin", []byte("rejected upload"))
							files = []string{id}
							tr = u.byFile(id)
						}
						all, err := u.takeUploads(images, files, "phone", "s", "rejected")
						if err != nil {
							t.Fatal(err)
						}
						tr, err = u.load(tr.ID)
						if err != nil {
							t.Fatal(err)
						}
						src, dst := tr.sessionPath(u), u.pending(tr.relativeName())
						want, err := os.ReadFile(src)
						if err != nil {
							t.Fatal(err)
						}
						clearFault := func() {}
						switch fault {
						case "mkdir":
							if err := os.Remove(filepath.Dir(dst)); err != nil && !os.IsNotExist(err) {
								t.Fatal(err)
							}
							if err := os.WriteFile(filepath.Dir(dst), []byte("blocked directory"), 0600); err != nil {
								t.Fatal(err)
							}
							clearFault = func() {
								if err := os.Remove(filepath.Dir(dst)); err != nil {
									t.Fatal(err)
								}
							}
						case "rename":
							if err := os.MkdirAll(dst, 0700); err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(filepath.Join(dst, "block"), []byte("block rename"), 0600); err != nil {
								t.Fatal(err)
							}
							clearFault = func() {
								if err := os.RemoveAll(dst); err != nil {
									t.Fatal(err)
								}
							}
						default:
							pendingSyncs := 0
							u.syncDir = func(dir string) error {
								fail := fault == "intent-sync" && dir == filepath.Dir(u.partial(tr.ID))
								if dir == filepath.Dir(dst) {
									pendingSyncs++
									fail = fail || fault == "pending-sync" && pendingSyncs == 2
								}
								fail = fail || fault == "session-sync" && dir == filepath.Dir(src)
								if fail {
									return errors.New("injected untake fsync failure")
								}
								return syncDir(dir)
							}
						}
						if release {
							u.release("s")
						} else if image {
							u.untake(all.Images, "s", "rejected")
						} else {
							u.untakeFiles(all.Files, "s", "rejected")
						}
						retained, err := u.load(tr.ID)
						if err != nil {
							t.Fatal(err)
						}
						if retained.Session != "s" || !retained.Returning || len(retained.Refs) != 0 {
							t.Fatalf("rejected upload lost durable return intent: %+v", retained)
						}
						at := src
						if fault == "pending-sync" || fault == "session-sync" {
							at = dst
						}
						got, err := os.ReadFile(at)
						if err != nil || !bytes.Equal(got, want) {
							t.Fatalf("bytes lost during rejected return: %v", err)
						}
						clearFault()
						restarted := &uploads{dir: u.dir, quota: maxUploadStorage}
						if recovery == "restart" {
							restarted.reconcile()
						} else {
							restarted.gc(time.Now())
						}
						returned, err := restarted.load(tr.ID)
						if err != nil {
							t.Fatal(err)
						}
						if returned.Session != "" || returned.Returning || len(returned.Refs) != 0 {
							t.Fatalf("return not recovered: %+v", returned)
						}
						got, err = os.ReadFile(dst)
						if err != nil || !bytes.Equal(got, want) {
							t.Fatalf("pending bytes not recovered: %v", err)
						}
						if err := restarted.checkUploads(images, files, "phone"); err != nil {
							t.Fatal("recovered attachment is not reusable", err)
						}
					})
				}
			}
		}
	}

}
