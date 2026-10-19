package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"image/color"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/paths"
)

const tid = "0123456789abcdef0123456789abcdef"

func beginUpload(t *testing.T, u *uploads, data []byte, device string) *pb.ImageUploadStatus {
	t.Helper()
	sum := sha256.Sum256(data)
	st, err := u.begin(&pb.BeginImageUploadRequest{TransferId: tid, Format: pb.ImageFormat_IMAGE_FORMAT_PNG, TotalBytes: int64(len(data)), Sha256: sum[:]}, device)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestUploads(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	data := testPNG(t, 300, 200, color.RGBA{0, 200, 0, 255})
	st := beginUpload(t, u, data, "phone")
	if st.State != pb.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING || st.MaxChunkBytes != maxUploadChunk {
		t.Fatalf("begin %v", st)
	}
	// Beginning again is a no-op; another device cannot touch it.
	beginUpload(t, u, data, "phone")
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "laptop"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("other device: %v", err)
	}
	half := len(data) / 2
	if st, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data[:half]}, "phone"); err != nil || st.PersistedBytes != int64(half) {
		t.Fatalf("chunk 1: %v %v", st, err)
	}
	// A retried chunk is fine, a different one at the same place is not,
	// and nothing may skip ahead.
	if st, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data[:half]}, "phone"); err != nil || st.PersistedBytes != int64(half) {
		t.Fatalf("retry: %v %v", st, err)
	}
	bad := bytes.Clone(data[:half])
	bad[0] ^= 1
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: bad}, "phone"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("changed chunk: %v", err)
	}
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Offset: int64(half + 1), Data: data[half+1:]}, "phone"); connect.CodeOf(err) != connect.CodeOutOfRange {
		t.Fatalf("gap: %v", err)
	}
	if _, err := u.complete(tid, "phone"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("early complete: %v", err)
	}
	// Overlapping the end of what arrived is fine.
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Offset: int64(half - 10), Data: data[half-10:]}, "phone"); err != nil {
		t.Fatal(err)
	}
	st, err := u.complete(tid, "phone")
	if err != nil || st.State != pb.ImageUploadState_IMAGE_UPLOAD_STATE_COMPLETE || st.ImageId == "" || st.Width != 300 {
		t.Fatalf("complete %v %v", st, err)
	}
	if again, _ := u.complete(tid, "phone"); again.ImageId != st.ImageId {
		t.Fatal("completing again gave another image")
	}
	if err := u.check([]string{st.ImageId}, "laptop"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("other device's image: %v", err)
	}
	imgs, err := u.take([]string{st.ImageId}, "phone", "sess1", "r1")
	if err != nil || len(imgs) != 1 || filepath.Dir(imgs[0].Path) != filepath.Join(u.dir, "sess1") || filepath.Ext(imgs[0].Path) != ".png" {
		t.Fatalf("take %v %v", imgs, err)
	}
	if fi, err := os.Stat(imgs[0].Path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file %v", err)
	}
	// A resend in the same session may name it again; another may not.
	if _, err := u.take([]string{st.ImageId}, "phone", "sess1", "r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.take([]string{st.ImageId}, "phone", "sess2", "r1"); err == nil {
		t.Fatal("image taken by a second session")
	}
	u.dropSession("sess1")
	if _, err := os.Stat(imgs[0].Path); !os.IsNotExist(err) {
		t.Fatal("session images kept")
	}
}

func TestUploadChecks(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	// Not what the hash says.
	data := []byte("\x89PNG\r\n\x1a\nnot really")
	beginUpload(t, u, data, "d")
	u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: append([]byte("x"), data[1:]...)}, "d")
	if _, err := u.complete(tid, "d"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("hash: %v", err)
	}
	// Right hash, but it does not decode.
	beginUpload(t, u, data, "d")
	u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "d")
	if _, err := u.complete(tid, "d"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("decode: %v", err)
	}
	if _, err := u.status(context.Background(), tid, "d"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal("failed transfer kept")
	}
	for _, req := range []*pb.BeginImageUploadRequest{
		{TransferId: "short", Format: pb.ImageFormat_IMAGE_FORMAT_PNG, TotalBytes: 1, Sha256: make([]byte, 32)},
		{TransferId: tid, Format: pb.ImageFormat_IMAGE_FORMAT_UNSPECIFIED, TotalBytes: 1, Sha256: make([]byte, 32)},
		{TransferId: tid, Format: pb.ImageFormat_IMAGE_FORMAT_PNG, TotalBytes: maxUploadBytes + 1, Sha256: make([]byte, 32)},
	} {
		if _, err := u.begin(req, "d"); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("begin %v: %v", req, err)
		}
	}
	// Unfinished transfers go after an hour, unused images after a day.
	good := testPNG(t, 10, 10, color.RGBA{1, 2, 3, 255})
	beginUpload(t, u, good, "d")
	u.gc(time.Now().Add(2 * time.Hour))
	if _, err := u.status(context.Background(), tid, "d"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal("stale transfer kept")
	}
	beginUpload(t, u, good, "d")
	u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: good}, "d")
	st, _ := u.complete(tid, "d")
	u.gc(time.Now().Add(23 * time.Hour))
	if err := u.check([]string{st.ImageId}, "d"); err != nil {
		t.Fatalf("image dropped early: %v", err)
	}
	u.gc(time.Now().Add(25 * time.Hour))
	if err := u.check([]string{st.ImageId}, "d"); err == nil {
		t.Fatal("unused image kept")
	}
	if entries, _ := os.ReadDir(filepath.Join(u.dir, "pending")); len(entries) != 0 {
		t.Fatalf("%d files left", len(entries))
	}
}

// completeUpload uploads a small PNG for device and returns its status.
func completeUpload(t *testing.T, u *uploads, device string) *pb.ImageUploadStatus {
	t.Helper()
	data := testPNG(t, 20, 10, color.RGBA{9, 9, 9, 255})
	beginUpload(t, u, data, device)
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, device); err != nil {
		t.Fatal(err)
	}
	st, err := u.complete(tid, device)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// G10: which session an image moved to is on disk before the move, and a
// move cut short by a crash is finished on start; if the ownership cannot
// be saved, nothing moves and the call fails.
func TestImageMovesAreRecoverable(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	st := completeUpload(t, u, "d")
	name := st.ImageId + ".png"

	os.Chmod(filepath.Join(u.dir, "partial"), 0o500)
	_, err := u.take([]string{st.ImageId}, "d", "s1", "r1")
	os.Chmod(filepath.Join(u.dir, "partial"), 0o700)
	if err == nil {
		t.Skip("directory writable despite chmod")
	}
	if _, err := os.Stat(u.pending(name)); err != nil {
		t.Fatal("image moved although its owner was not saved")
	}

	// A crash after the ownership was saved, before the file moved.
	u.mu.Lock()
	tr := u.byImage(st.ImageId)
	tr.Session = "s1"
	u.save(tr)
	u.mu.Unlock()
	u.reconcile()
	if _, err := os.Stat(filepath.Join(u.sessionDir("s1"), name)); err != nil {
		t.Fatalf("move not finished: %v", err)
	}
	if imgs, err := u.take([]string{st.ImageId}, "d", "s1", "r1"); err != nil || len(imgs) != 1 {
		t.Fatalf("take after recovery: %v", err)
	}
}

func TestUploadExpiryWithoutGC(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	data := testPNG(t, 10, 10, color.RGBA{1, 2, 3, 255})
	beginUpload(t, u, data, "d")
	tr, err := u.load(tid)
	if err != nil {
		t.Fatal(err)
	}
	tr.UpdatedMs = time.Now().Add(-partialTTL).UnixMilli()
	if err := u.save(tr); err != nil {
		t.Fatal(err)
	}
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "d"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expired transfer revived: %v", err)
	}
	st := completeUpload(t, u, "d")
	tr, err = u.load(tid)
	if err != nil {
		t.Fatal(err)
	}
	tr.CompletedMs = time.Now().Add(-unusedTTL).UnixMilli()
	if err := u.save(tr); err != nil {
		t.Fatal(err)
	}
	if _, err := u.take([]string{st.ImageId}, "d", "s1", "r1"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expired image accepted: %v", err)
	}
	if _, err := os.Stat(u.pending(st.ImageId + ".png")); !os.IsNotExist(err) {
		t.Fatalf("expired image bytes remain: %v", err)
	}
}

func TestUsedUploadsSurviveGCAndExitedCleanupRetries(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	st := completeUpload(t, u, "d")
	imgs, err := u.take([]string{st.ImageId}, "d", "s1", "r1")
	if err != nil {
		t.Fatal(err)
	}
	dir := paths.SessionDir("s1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := &meta{ID: "s1"}
	if err := m.save(dir); err != nil {
		t.Fatal(err)
	}
	u.gc(time.Now().Add(48 * time.Hour))
	if _, err := u.take([]string{st.ImageId}, "d", "s1", "r1"); err != nil {
		t.Fatalf("used image ownership expired: %v", err)
	}
	if _, err := u.take([]string{st.ImageId}, "other", "s1", "r1"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("used ownership lost: %v", err)
	}
	// Simulate a crash after saving Exited, before deleting uploads.
	m.Exited = true
	if err := m.save(dir); err != nil {
		t.Fatal(err)
	}
	u = newUploads()
	u.gc(time.Now())
	for _, path := range []string{imgs[0].Path, u.partial(tid) + ".json"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("exited upload remains: %s: %v", path, err)
		}
	}
}

func TestUploadGCSweepsOrphans(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	st := completeUpload(t, u, "d")
	stale := time.Now().Add(-25 * time.Hour)
	orphans := []string{u.pending("orphan.png"), u.pending("orphan.png.tmp"), u.partial("orphan.part"), u.partial("orphan.json.tmp"), u.partial("broken.json")}
	for _, path := range orphans {
		if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stale, stale); err != nil {
			t.Fatal(err)
		}
	}
	fresh := u.pending("fresh.png")
	if err := os.WriteFile(fresh, []byte("fresh"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A crash after saving complete can leave the original partial copy.
	if err := os.WriteFile(u.partial(tid)+".part", []byte("redundant"), 0o600); err != nil {
		t.Fatal(err)
	}
	u.gc(time.Now())
	for _, path := range append(orphans, u.partial(tid)+".part") {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("orphan remains: %s: %v", path, err)
		}
	}
	for _, path := range []string{fresh, u.pending(st.ImageId + ".png")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("fresh or referenced file removed: %v", err)
		}
	}
}

func TestCompleteSaveFailureLeavesNoOrphan(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	data := testPNG(t, 10, 10, color.RGBA{1, 2, 3, 255})
	beginUpload(t, u, data, "d")
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "d"); err != nil {
		t.Fatal(err)
	}
	// Force the atomic record write to fail after the image write succeeds.
	partialDir := filepath.Join(u.dir, "partial")
	if err := os.Chmod(partialDir, 0o500); err != nil {
		t.Fatal(err)
	}
	_, err := u.complete(tid, "d")
	if chmodErr := os.Chmod(partialDir, 0o700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if err == nil {
		t.Skip("directory writable despite chmod")
	}
	entries, err := os.ReadDir(filepath.Join(u.dir, "pending"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("untracked image left: %v %v", entries, err)
	}
	if _, err := u.complete(tid, "d"); err != nil {
		t.Fatalf("cannot retry complete: %v", err)
	}
}

func TestUploadCreatesMissingFoldersAndRetriesDoNotExtendExpiry(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing", "home")
	t.Setenv("REMOTE_AGENT_HOME", home)
	u := newUploads()
	u.reconcile()
	u.gc(time.Now()) // first run: none of the folders exists yet
	data := testPNG(t, 10, 10, color.RGBA{1, 2, 3, 255})
	beginUpload(t, u, data, "d")
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "d"); err != nil {
		t.Fatal(err)
	}
	tr, err := u.load(tid)
	if err != nil {
		t.Fatal(err)
	}
	tr.UpdatedMs = time.Now().Add(-30 * time.Minute).UnixMilli()
	if err := u.save(tr); err != nil {
		t.Fatal(err)
	}
	st, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "d")
	if err != nil || st.ExpiresMs != tr.expiresMs() {
		t.Fatalf("duplicate extended expiry: %v %v", st, err)
	}
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid}, "d"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty chunk accepted: %v", err)
	}
	st, err = u.complete(tid, "d")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.take([]string{st.ImageId}, "d", "s1", "r1"); err != nil {
		t.Fatal(err)
	}
}

func TestUploadQuotaReservesIncomingBytesAndRecovers(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	u.quota = 1 << 20
	req := &pb.BeginImageUploadRequest{TransferId: tid, Format: pb.ImageFormat_IMAGE_FORMAT_PNG, TotalBytes: 768 << 10, Sha256: make([]byte, 32)}
	if _, err := u.begin(req, "d"); err != nil {
		t.Fatal(err)
	}
	// Nothing has arrived yet, but its declared bytes are reserved.
	req.TransferId = "1123456789abcdef0123456789abcdef"
	if _, err := u.begin(req, "d"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("unbounded reservation: %v", err)
	}
	if err := u.abort(tid, "d"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.begin(req, "d"); err != nil {
		t.Fatalf("abort did not free quota: %v", err)
	}
	if err := u.abort(req.TransferId, "d"); err != nil {
		t.Fatal(err)
	}
	// Session originals without a transfer record still count.
	sdir := u.sessionDir("orphan")
	if err := os.MkdirAll(sdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdir, "image.png"), make([]byte, 768<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := u.begin(req, "d"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("orphan original ignored: %v", err)
	}
	u.dropSession("orphan")
	if _, err := u.begin(req, "d"); err != nil {
		t.Fatalf("session cleanup did not free quota: %v", err)
	}
}

func TestExitedUploadRecordCleanedWithoutImageFolder(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	st := completeUpload(t, u, "d")
	if _, err := u.take([]string{st.ImageId}, "d", "s1", "r1"); err != nil {
		t.Fatal(err)
	}
	dir := paths.SessionDir("s1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (&meta{ID: "s1", Exited: true}).save(dir); err != nil {
		t.Fatal(err)
	}
	// Crash after the folder is gone but before the transfer record unlink.
	if err := os.RemoveAll(u.sessionDir("s1")); err != nil {
		t.Fatal(err)
	}
	u.gc(time.Now())
	if _, err := u.status(context.Background(), tid, "d"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("exited record left: %v", err)
	}
}
