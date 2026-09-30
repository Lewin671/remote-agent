package daemon

import (
	"bytes"
	"crypto/sha256"
	"image/color"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
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
	imgs, err := u.take([]string{st.ImageId}, "phone", "sess1")
	if err != nil || len(imgs) != 1 || filepath.Dir(imgs[0].Path) != filepath.Join(u.dir, "sess1") || filepath.Ext(imgs[0].Path) != ".png" {
		t.Fatalf("take %v %v", imgs, err)
	}
	if fi, err := os.Stat(imgs[0].Path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file %v", err)
	}
	// A resend in the same session may name it again; another may not.
	if _, err := u.take([]string{st.ImageId}, "phone", "sess1"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.take([]string{st.ImageId}, "phone", "sess2"); err == nil {
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
	if _, err := u.status(tid, "d"); connect.CodeOf(err) != connect.CodeNotFound {
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
	if _, err := u.status(tid, "d"); connect.CodeOf(err) != connect.CodeNotFound {
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
