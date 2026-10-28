package daemon

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/paths"
)

// Every operation uses real temporary files. Fail before each I/O boundary,
// or panic immediately after it to simulate losing the caller/undo on a crash.
// Restart/GC then uses a fresh uploads instance, with no in-memory intent.
func TestUploadTransitionFaultMatrix(t *testing.T) {
	move := []string{"move-intent", "move-directory", "move-rename", "move-destination", "move-source"}
	returning := append(append([]string{}, move...), "move-clear")
	deletion := []string{"delete-intent", "delete-bytes", "delete-directory", "delete-part", "delete-source", "delete-record", "delete-index"}
	cases := []struct {
		name  string
		steps []string
	}{
		{"begin", []string{"begin-directory", "begin-create", "begin-bytes", "begin-record"}},
		{"chunk", []string{"chunk-write", "chunk-bytes", "chunk-record"}},
		{"complete", []string{"complete-directory", "complete-bytes", "complete-destination", "complete-record", "complete-unlink", "complete-source"}},
		{"take", append(append([]string{}, move...), "claim-reference")},
		{"untake", returning}, {"release", returning}, {"returning", returning}, {"reconcile", move},
		{"abort", deletion}, {"expiry", deletion}, {"exit", deletion},
		{"cancel", deletion}, {"lru", deletion},
	}
	for _, image := range []bool{false, true} {
		kind := "file"
		if image {
			kind = "image"
		}
		for _, tc := range cases {
			if !image && (tc.name == "cancel" || tc.name == "lru") {
				continue
			}
			for _, stage := range tc.steps {
				for _, crash := range []bool{false, true} {
					for _, gc := range []bool{false, true} {
						mode := "io"
						if crash {
							mode = "crash"
						}
						recovery := "restart"
						if gc {
							recovery = "gc"
						}
						t.Run(strings.Join([]string{kind, tc.name, stage, mode, recovery}, "/"), func(t *testing.T) {
							t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
							u := newUploads()
							data := []byte("exact original file bytes")
							if image {
								data = testPNG(t, 3, 2, color.RGBA{1, 2, 3, 255})
							}
							sum := sha256.Sum256(data)
							req := &pb.BeginImageUploadRequest{TransferId: tid, TotalBytes: int64(len(data)), Sha256: sum[:]}
							if image {
								req.Format = pb.ImageFormat_IMAGE_FORMAT_PNG
							} else {
								req.FileName = "matrix.bin"
							}
							begin := func() {
								if _, err := u.begin(req, "phone"); err != nil {
									t.Fatal(err)
								}
							}
							chunk := func() {
								if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "phone"); err != nil {
									t.Fatal(err)
								}
							}
							if tc.name != "begin" {
								begin()
							}
							if tc.name != "begin" && tc.name != "chunk" {
								chunk()
							}
							var ims, files []string
							if tc.name != "begin" && tc.name != "chunk" && tc.name != "complete" {
								st, err := u.complete(tid, "phone")
								if err != nil {
									t.Fatal(err)
								}
								if image {
									ims = []string{st.ImageId}
								} else {
									files = []string{st.FileId}
								}
							}
							var taken messageUploads
							claimed := tc.name == "untake" || tc.name == "release" || tc.name == "returning" || tc.name == "exit" || tc.name == "cancel" || tc.name == "lru"
							if claimed {
								var err error
								taken, err = u.takeUploads(ims, files, "phone", "s", "r")
								if err != nil {
									t.Fatal(err)
								}
							}
							if claimed || tc.name == "take" || tc.name == "reconcile" {
								dir := paths.SessionDir("s")
								if err := os.MkdirAll(dir, 0700); err != nil {
									t.Fatal(err)
								}
								m := meta{ID: "s", Exited: tc.name == "exit", Starting: tc.name == "release"}
								if err := m.save(dir); err != nil {
									t.Fatal(err)
								}
								tr, _ := u.load(tid)
								// Preserve image bytes independently of queue restoration in the move
								// matrix; cancellation/GC of unexposed images is exercised below.
								tr.Exposed = true
								if tc.name == "lru" {
									tr.Refs = nil
									tr.Embedded = true
									tr.AccessMs = time.Now().Add(-time.Hour).UnixMilli()
								}
								if tc.name == "untake" || tc.name == "cancel" {
									tr.Exposed = false
								}
								if tc.name == "returning" {
									tr.Returning = true
									tr.Refs = nil
								}
								if tc.name == "reconcile" {
									tr.Session = "s"
								}
								if err := u.save(tr); err != nil {
									t.Fatal(err)
								}
							}
							hit := false
							injected := errors.New("injected upload I/O error")
							crashToken := new(int)
							u.beforeUploadStep = func(s string) error {
								if s == stage && !crash {
									hit = true
									return injected
								}
								return nil
							}
							dstSynced, srcSynced, recordSynced := false, false, false
							u.afterUploadStep = func(s string) {
								switch s {
								case "move-intent":
									dstSynced, srcSynced = false, false
								case "move-rename":
									tr, err := u.load(tid)
									if err != nil || tr.Session != "s" {
										t.Fatal("rename before durable owner", err)
									}
								case "move-destination":
									dstSynced = true
								case "move-source":
									srcSynced = true
								case "move-clear":
									if !dstSynced || !srcSynced {
										t.Fatal("owner cleared before both directory syncs")
									}
								case "complete-record":
									recordSynced = true
								case "complete-unlink":
									if !recordSynced {
										t.Fatal("original removed before completion committed")
									}
								}
								if s == stage && crash {
									hit = true
									panic(crashToken)
								}
							}
							func() {
								defer func() {
									if p := recover(); p != nil && p != crashToken {
										panic(p)
									}
								}()
								switch tc.name {
								case "begin":
									_, _ = u.begin(req, "phone")
								case "chunk":
									_, _ = u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "phone")
								case "complete":
									_, _ = u.complete(tid, "phone")
								case "take":
									_, _ = u.takeUploads(ims, files, "phone", "s", "r")
								case "untake":
									if image {
										u.untake(taken.Images, "s", "r")
									} else {
										u.untakeFiles(taken.Files, "s", "r")
									}
								case "release":
									_ = u.release("s")
								case "returning", "reconcile":
									u.reconcile()
								case "abort":
									_ = u.abort(tid, "phone")
								case "expiry":
									u.gc(time.Now().Add(unusedTTL + time.Second))
								case "cancel":
									u.releaseRefs(taken.Images, "s", "r")
								case "lru":
									_, _ = u.usage(true, map[string]bool{})
								case "exit":
									u.dropSession("s")
								}
							}()
							if !hit {
								t.Fatal("transition never reached fault boundary")
							}
							// No rejected operation can have lost both copies of non-deleted bytes.
							if tc.name != "abort" && tc.name != "expiry" && tc.name != "exit" && tc.name != "cancel" && tc.name != "lru" && tc.name != "begin" {
								tr, err := u.load(tid)
								if err != nil {
									t.Fatal(err)
								}
								if tr.artifactID() == "" {
									got, err := os.ReadFile(u.partial(tid) + ".part")
									if err != nil || int64(len(got)) < tr.Persisted || !bytes.Equal(got, data[:len(got)]) {
										t.Fatal("receiving durable prefix lost", err)
									}
								} else {
									a, ea := os.ReadFile(u.pending(tr.relativeName()))
									b, eb := os.ReadFile(tr.sessionPath(u))
									if ea != nil && eb != nil {
										t.Fatal("both completed copies lost", ea, eb)
									}
									if !image && !bytes.Equal(a, data) && !bytes.Equal(b, data) {
										t.Fatal("original file changed")
									}
									if tr.Returning && tr.Session == "" {
										t.Fatal("return intent lost owner")
									}
								}
							}
							// Replace the entire uploads object; a failed pre-intent write is retried
							// by its caller (Starting metadata for release, or the client's request).
							u = newUploads()
							if gc {
								u.gc(time.Now())
							} else {
								u.reconcile()
							}
							switch tc.name {
							case "begin":
								begin()
								chunk()
							case "chunk":
								chunk()
							case "complete":
								if _, err := u.complete(tid, "phone"); err != nil {
									t.Fatal("completion retry", err)
								}
							case "untake", "release", "returning":
								if err := u.release("s"); err != nil {
									t.Fatal(err)
								}
							case "abort":
								if err := u.abort(tid, "phone"); err != nil {
									t.Fatal(err)
								}
							case "expiry":
								u.gc(time.Now().Add(unusedTTL + time.Second))
							case "cancel":
								u.releaseRefs(taken.Images, "s", "r")
							case "lru":
								_, _ = u.usage(true, map[string]bool{})
							case "exit":
								u.dropSession("s")
							}
							deleted := tc.name == "abort" || tc.name == "expiry" || tc.name == "exit" || tc.name == "cancel" || tc.name == "lru"
							if deleted {
								if _, err := os.Stat(u.partial(tid) + ".json"); !os.IsNotExist(err) {
									t.Fatal("delete did not finish", err)
								}
							} else {
								tr, err := u.load(tid)
								if err != nil {
									t.Fatal(err)
								}
								if tr.Returning || tr.Deleting {
									t.Fatal("recovery left an intent", tr)
								}
								path := u.partial(tid) + ".part"
								if tr.artifactID() != "" {
									path = u.pending(tr.relativeName())
									if tr.Session != "" {
										path = tr.sessionPath(u)
									}
								}
								got, err := os.ReadFile(path)
								if err != nil || len(got) == 0 || (!image && !bytes.Equal(got, data)) {
									t.Fatal("recovery changed/lost bytes", err)
								}
								if tc.name == "untake" || tc.name == "release" || tc.name == "returning" {
									if tr.Session != "" {
										t.Fatal("rejected upload remains claimed")
									}
									if err := u.checkUploads(ims, files, "phone"); err != nil {
										t.Fatal("returned bytes unavailable", err)
									}
								}
							}
						})
					}
				}
			}
		}
	}
}

func TestUnstartedKeepsMetadataUntilReturnsFinish(t *testing.T) {
	for _, stage := range []string{"move-intent", "move-destination", "move-source", "move-clear"} {
		t.Run(stage, func(t *testing.T) {
			t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
			u := newUploads()
			id := uploadFile(t, u, "return.bin", []byte("keep"))
			if _, err := u.takeUploads(nil, []string{id}, "phone", "s", "r"); err != nil {
				t.Fatal(err)
			}
			dir := paths.SessionDir("s")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			m := &meta{ID: "s", Starting: true}
			if err := m.save(dir); err != nil {
				t.Fatal(err)
			}
			u.beforeUploadStep = func(s string) error {
				if s == stage {
					return errors.New("return failed")
				}
				return nil
			}
			d := &Daemon{uploads: u}
			if !d.unstarted(m, dir, false) {
				t.Fatal("unfinished session restored as accepted")
			}
			if _, err := loadMeta(dir); err != nil {
				t.Fatal("lost retry metadata", err)
			}
			d.uploads = newUploads()
			d.uploads.gc(time.Now())
			if !d.unstarted(m, dir, false) {
				t.Fatal("unfinished session not retried")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("metadata not removed after durable return")
			}
			if err := d.uploads.checkUploads(nil, []string{id}, "phone"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionCleanupDoesNotDeleteUnresolvedReturns(t *testing.T) {
	for _, image := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "image"}[image], func(t *testing.T) {
			t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
			u := newUploads()
			var ims, files []string
			if image {
				ims = []string{storageUpload(t, u)}
			} else {
				files = []string{uploadFile(t, u, "keep.bin", []byte("keep"))}
			}
			if _, err := u.takeUploads(ims, files, "phone", "s", "r"); err != nil {
				t.Fatal(err)
			}
			tr := u.byFile(firstID(files))
			if image {
				tr = u.byImage(ims[0])
			}
			tr.Returning = true
			if err := u.save(tr); err != nil {
				t.Fatal(err)
			}
			u.beforeUploadStep = func(stage string) error {
				if stage == "move-rename" {
					return errors.New("rename failed")
				}
				return nil
			}
			u.dropSession("s")
			if _, err := os.Stat(tr.sessionPath(u)); err != nil {
				t.Fatal("cleanup deleted a rejected upload", err)
			}
			u = newUploads()
			u.gc(time.Now())
			if err := u.checkUploads(ims, files, "phone"); err != nil {
				t.Fatal("return not recovered", err)
			}
		})
	}
}
func firstID(ids []string) string {
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

func TestImageCompletionRetainsOriginalAfterRecordSyncFailure(t *testing.T) {
	for _, gc := range []bool{false, true} {
		t.Run(map[bool]string{false: "restart", true: "gc"}[gc], func(t *testing.T) {
			t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
			u := newUploads()
			data := testPNG(t, 3, 2, color.RGBA{1, 2, 3, 255})
			beginUpload(t, u, data, "phone")
			if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "phone"); err != nil {
				t.Fatal(err)
			}
			u.syncDir = func(dir string) error {
				if dir == filepath.Dir(u.partial(tid)) {
					return errors.New("image metadata fsync failed")
				}
				return syncDir(dir)
			}
			if _, err := u.complete(tid, "phone"); !fileReplaced(err) {
				t.Fatal("expected visible but unsynced completion", err)
			}
			tr, err := u.load(tid)
			if err != nil || tr.ImageID == "" {
				t.Fatal("completion record missing", err)
			}
			for _, path := range []string{u.partial(tid) + ".part", u.pending(tr.relativeName())} {
				if _, err := os.Stat(path); err != nil {
					t.Fatal("lost recovery copy", err)
				}
			}
			u = newUploads()
			if gc {
				u.gc(time.Now())
			} else {
				u.reconcile()
			}
			if _, err := os.Stat(u.partial(tid) + ".part"); !os.IsNotExist(err) {
				t.Fatal("durable original not released", err)
			}
			if _, err := os.Stat(u.pending(tr.relativeName())); err != nil {
				t.Fatal("completed image lost", err)
			}
		})
	}
}
