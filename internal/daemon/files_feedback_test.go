package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/paths"
)

func TestFilePasteAlwaysHasTextPrefix(t *testing.T) {
	f := queuedFile{ID: strings.Repeat("a", 32), Name: "x.png", Path: "/uploads/s/files/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/x.png"}
	for _, kind := range []pb.Agent{pb.Agent_AGENT_CODEX, pb.Agent_AGENT_CLAUDE} {
		want := "File: " + f.Path
		if kind == pb.Agent_AGENT_CLAUDE {
			want = `File: @"` + f.Path + `" (file)`
		}
		for _, text := range []string{"", "user text"} {
			got := composeFiles(text, []queuedFile{f}, kind)
			expected := want
			if text != "" {
				expected = text + "\n\n" + want
			}
			if got != expected {
				t.Fatalf("composition %q != %q", got, expected)
			}
			in := input{Text: text, Files: []queuedFile{f}, Composed: got}
			if !in.matches(got) || in.matches("[Image #1]") {
				t.Fatal("file text confirmation changed")
			}
			in.Images = []queuedImage{{ID: "image", Path: "/image.png"}}
			in.Indexes = []int32{2}
			if !in.matches("[Image #2]" + got) {
				t.Fatal("mixed no-text confirmation changed")
			}
			if titleFrom(fileMessageText(&pb.UserMessage{Text: got, Files: sentFiles([]queuedFile{f})})) != titleFrom(text) {
				t.Fatal("file label leaked into title")
			}
		}
	}
}
func TestSafeFileNamePreservesCombiningMarks(t *testing.T) {
	for _, name := range []string{"नमस्ते.txt", "สวัสดี.log", "عَرَبِيّ.csv"} {
		if got := safeFileName(name); got != name {
			t.Errorf("%q became %q", name, got)
		}
	}
}
func TestFailedReuseDoesNotReturnAcceptedFileToPending(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.d.uploads
	id := uploadFile(t, u, "old.txt", []byte("read later"))
	old, err := u.takeUploads(nil, []string{id}, "phone", h.s.meta.ID, "accepted")
	if err != nil {
		t.Fatal(err)
	}
	u.releaseFiles(old.Files, h.s.meta.ID, "accepted")
	if err := os.Mkdir(filepath.Join(h.dir, "queue.json"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = h.s.enqueueInput("reuse", "rejected", nil, nil, originLocal, pb.SendMode_SEND_MODE_QUEUE, nil, func(request string) (messageUploads, error) {
		return u.takeUploads(nil, []string{id}, "phone", h.s.meta.ID, request)
	})
	if err == nil {
		t.Fatal("queue save unexpectedly worked")
	}
	tr := u.byFile(id)
	if tr == nil || tr.Session != h.s.meta.ID || tr.Refs["rejected"] {
		t.Fatalf("accepted file moved/retained ref: %+v", tr)
	}
	if _, err := os.Stat(old.Files[0].Path); err != nil {
		t.Fatal("accepted path gone", err)
	}
	if err := os.MkdirAll(paths.SessionDir(h.s.meta.ID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := h.s.meta.save(paths.SessionDir(h.s.meta.ID)); err != nil {
		t.Fatal(err)
	}
	u.gc(time.Now().Add(48 * time.Hour))
	if _, err := os.Stat(old.Files[0].Path); err != nil {
		t.Fatal("GC removed accepted file", err)
	}
}
func TestTakeRollbackRemovesRefsFromPreviouslyOwnedTransfers(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	a := uploadFile(t, u, "old.txt", []byte("old"))
	b := uploadFile(t, u, "new.txt", []byte("new"))
	if _, err := u.takeUploads(nil, []string{a}, "phone", "s", "accepted"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("destination fsync failure")
	u.syncDir = func(dir string) error {
		if filepath.Base(dir) == b {
			return failure
		}
		return syncDir(dir)
	}
	if _, err := u.takeUploads(nil, []string{a, b}, "phone", "s", "rejected"); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if tr := u.byFile(a); tr.Refs["rejected"] || !tr.Refs["accepted"] || tr.Session != "s" {
		t.Fatalf("old references changed: %+v", tr)
	}
	if tr := u.byFile(b); tr.Session != "" || len(tr.Refs) != 0 {
		t.Fatalf("new claim left behind: %+v", tr)
	}
}
func TestFileCompletionSyncsDirectoryAncestryAndKeepsPartialOnSyncFailure(t *testing.T) {
	for _, failAt := range []string{"", "pending", "uploads"} {
		t.Run(failAt, func(t *testing.T) {
			t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
			u := newUploads()
			data := []byte("durable")
			sum := sha256.Sum256(data)
			_, err := u.begin(&pb.BeginImageUploadRequest{TransferId: tid, FileName: "durable.bin", TotalBytes: int64(len(data)), Sha256: sum[:]}, "phone")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "phone"); err != nil {
				t.Fatal(err)
			}
			var synced []string
			failure := errors.New("fsync failed")
			u.syncDir = func(dir string) error {
				synced = append(synced, dir)
				if filepath.Base(dir) == failAt {
					return failure
				}
				return syncDir(dir)
			}
			st, err := u.complete(tid, "phone")
			if failAt != "" {
				if !errors.Is(err, failure) {
					t.Fatal(err)
				}
				if _, err := os.Stat(u.partial(tid) + ".part"); err != nil {
					t.Fatal("original removed before durable completion", err)
				}
				tr, err := u.load(tid)
				if err != nil || tr.FileID != "" {
					t.Fatalf("completion committed: %v %v", tr, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				for _, dir := range []string{filepath.Join(u.dir, "pending", st.FileId), filepath.Join(u.dir, "pending"), u.dir, filepath.Dir(u.dir)} {
					if !slices.Contains(synced, dir) {
						t.Fatal("directory not synced", dir)
					}
				}
			}
		})
	}
}
func TestFileMoveSyncFailureRollsBackBeforeAcceptance(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	id := uploadFile(t, u, "a.bin", []byte("a"))
	count := 0
	failure := errors.New("new file entry not durable")
	u.syncDir = func(dir string) error {
		if filepath.Base(dir) == id && strings.Contains(dir, "/s/files/") {
			count++
			if count == 2 {
				return failure
			}
		}
		return syncDir(dir)
	}
	if _, err := u.takeUploads(nil, []string{id}, "phone", "s", "r"); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	tr := u.byFile(id)
	if tr.Session != "" || tr.Refs["r"] {
		t.Fatalf("failed move accepted: %+v", tr)
	}
	if _, err := os.Stat(u.pending(tr.relativeName())); err != nil {
		t.Fatal("rollback lost bytes", err)
	}
}
func TestFileHashDoesNotHoldUploadsLockAndCannotCompleteReplacedPartial(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	data := []byte("same")
	sum := sha256.Sum256(data)
	req := &pb.BeginImageUploadRequest{TransferId: tid, FileName: "a.bin", TotalBytes: 4, Sha256: sum[:]}
	_, err := u.begin(req, "phone")
	if err != nil {
		t.Fatal(err)
	}
	_, err = u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "phone")
	if err != nil {
		t.Fatal(err)
	}
	hashing := make(chan struct{})
	resume := make(chan struct{})
	u.hashFile = func(w io.Writer, r io.Reader) (int64, error) { close(hashing); <-resume; return io.Copy(w, r) }
	done := make(chan error, 1)
	go func() { _, err := u.complete(tid, "phone"); done <- err }()
	<-hashing
	otherDone := make(chan error, 1)
	go func() {
		if err := u.abort(tid, "phone"); err != nil {
			otherDone <- err
			return
		}
		_, err := u.begin(req, "phone")
		otherDone <- err
	}()
	select {
	case err := <-otherDone:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		close(resume)
		<-done
		t.Fatal("file hashing blocked other upload operations")
	}
	close(resume)
	if err := <-done; connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale hash completion: %v", err)
	}
	st, err := u.status(context.Background(), tid, "phone")
	if err != nil || st.PersistedBytes != 0 || st.FileId != "" {
		t.Fatalf("replacement changed: %v %v", st, err)
	}
}
func TestRemoveAndReleaseCleanNestedEmptyFileDirectories(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	id := uploadFile(t, u, "a.bin", []byte("a"))
	tr := u.byFile(id)
	if err := u.abort(tr.ID, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(u.pending(id)); !os.IsNotExist(err) {
		t.Fatal("empty pending ID directory remains")
	}
	id = uploadFile(t, u, "a.bin", []byte("a"))
	if _, err := u.takeUploads(nil, []string{id}, "phone", "s", "r"); err != nil {
		t.Fatal(err)
	}
	u.release("s")
	if _, err := os.Stat(u.sessionDir("s")); !os.IsNotExist(err) {
		t.Fatal("empty session directory remains", err)
	}
	if _, err := os.Stat(u.pending(u.byFile(id).relativeName())); err != nil {
		t.Fatal("release lost file", err)
	}
}
func TestRestoreHeldRefsMatchesRuntimeForFilesAndImages(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.d.uploads
	imageID := storageUpload(t, u)
	fileID := uploadFile(t, u, "a.bin", []byte("a"))
	all, err := u.takeUploads([]string{imageID}, []string{fileID}, "phone", h.s.meta.ID, "held")
	if err != nil {
		t.Fatal(err)
	}
	if err := u.expose(all.Images); err != nil {
		t.Fatal(err)
	}
	in := input{RequestID: "held", Images: all.Images, Files: all.Files}
	h.s.queue.Queue = []input{in}
	h.s.hold(in)
	if tr := u.byFile(fileID); !tr.Refs["held"] {
		t.Fatal("runtime dropped held file ref")
	}
	records, err := u.referenceRecords()
	if err != nil {
		t.Fatal(err)
	}
	u.restoreRefs(h.s, records[h.s.meta.ID])
	if tr := u.byImage(imageID); len(tr.Refs) != 0 {
		t.Fatal("restored held image ref differs from runtime", tr.Refs)
	}
	if tr := u.byFile(fileID); !tr.Refs["held"] {
		t.Fatal("held file ref lost")
	}
	if !h.s.dropHeld("held", pb.InputStatus_INPUT_STATUS_CANCELED, "", originLocal) {
		t.Fatal("held cancellation failed")
	}
	if tr := u.byFile(fileID); len(tr.Refs) != 0 {
		t.Fatal("settled held reference remains")
	}
}

func TestImageAndFileOnlyQueueSeparatesFileLinesFromPlaceholder(t *testing.T) {
	h := newHarness(t, Config{})
	files := []queuedFile{{ID: strings.Repeat("f", 32), Name: "x.png", Path: "/uploads/s/files/ffffffffffffffffffffffffffffffff/x.png"}}
	images := []queuedImage{{ID: "image", Path: "/image.png"}}
	if _, err := h.s.enqueueInput("", "r", images, files, originLocal, pb.SendMode_SEND_MODE_STEER, nil, nil); err != nil {
		t.Fatal(err)
	}
	in := h.s.queue.Queue[0]
	if in.Composed != "\n\nFile: "+files[0].Path {
		t.Fatalf("no image/file separator: %q", in.Composed)
	}
	in.Indexes = []int32{1}
	recorded := "[Image #1]" + in.Composed
	if !in.matches(recorded) {
		t.Fatal("separated mixed message not confirmed")
	}
	u := &uploads{dir: "/uploads"}
	if got := u.recognizeFiles("s", recorded); len(got) != 1 || got[0].FileId != files[0].ID {
		t.Fatal("file not recognized after image", got)
	}
}
func TestUntakeReusedUnexposedImageKeepsAcceptedPath(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	id := storageUpload(t, u)
	first, err := u.take([]string{id}, "phone", "s", "first")
	if err != nil {
		t.Fatal(err)
	}
	tr := u.byImage(id)
	tr.Refs = nil
	u.save(tr)
	second, err := u.take([]string{id}, "phone", "s", "second")
	if err != nil {
		t.Fatal(err)
	}
	u.untake(second, "s", "second")
	if _, err := os.Stat(first[0].Path); err != nil {
		t.Fatal("failed reuse moved an accepted image", err)
	}
	if tr := u.byImage(id); tr.Session != "s" || tr.Refs["second"] {
		t.Fatal("image acceptance changed", tr)
	}
}

func TestFileCompletionRecordSyncFailureKeepsBothDurableCopies(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	data := []byte("durable")
	sum := sha256.Sum256(data)
	_, err := u.begin(&pb.BeginImageUploadRequest{TransferId: tid, FileName: "a.bin", TotalBytes: int64(len(data)), Sha256: sum[:]}, "phone")
	if err != nil {
		t.Fatal(err)
	}
	_, err = u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "phone")
	if err != nil {
		t.Fatal(err)
	}
	u.syncDir = func(dir string) error {
		if filepath.Base(dir) == "partial" {
			return errors.New("metadata fsync failure")
		}
		return syncDir(dir)
	}
	if _, err = u.complete(tid, "phone"); !fileReplaced(err) {
		t.Fatalf("completion did not report uncertain metadata durability: %v", err)
	}
	tr, err := u.load(tid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(u.pending(tr.relativeName())); err != nil {
		t.Fatal("committed record lost destination", err)
	}
	if _, err := os.Stat(u.partial(tid) + ".part"); err != nil {
		t.Fatal("partial removed before metadata sync", err)
	}
	u.syncDir = nil
	st, err := u.complete(tid, "phone")
	if err != nil || st.FileId != tr.FileID {
		t.Fatalf("durable completion retry: %v %v", st, err)
	}
	if _, err := os.Stat(u.partial(tid) + ".part"); !os.IsNotExist(err) {
		t.Fatal("successful retry left partial")
	}
}
