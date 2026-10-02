package daemon

import (
	"crypto/sha256"
	"encoding/base64"
	"image/color"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/paths"
)

func storageUpload(t *testing.T, u *uploads) string {
	t.Helper()
	b := testPNG(t, 20, 10, color.RGBA{1, 2, 3, 255})
	sum := sha256.Sum256(b)
	id := newID(16)
	if _, err := u.begin(&pb.BeginImageUploadRequest{TransferId: id, Format: pb.ImageFormat_IMAGE_FORMAT_PNG, TotalBytes: int64(len(b)), Sha256: sum[:]}, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: id, Data: b}, "phone"); err != nil {
		t.Fatal(err)
	}
	st, err := u.complete(id, "phone")
	if err != nil {
		t.Fatal(err)
	}
	return st.ImageId
}

func TestCancelReleasesOnlyTheLastImageReference(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	id := storageUpload(t, u)
	first, err := u.take([]string{id}, "phone", "s", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := u.take([]string{id}, "phone", "s", "second")
	if err != nil {
		t.Fatal(err)
	}
	u.releaseRefs(first, "s", "first")
	if _, err := os.Stat(first[0].Path); err != nil {
		t.Fatal("cancelling one message removed another's image", err)
	}
	u.releaseRefs(second, "s", "second")
	if _, err := os.Stat(first[0].Path); !os.IsNotExist(err) {
		t.Fatal("cancelled image remains", err)
	}
}

func TestOriginalLRUProtectsExecutionAndUnconfirmedImages(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	var images [][]queuedImage
	for _, session := range []string{"oldest", "newer", "running", "unconfirmed", "queued"} {
		id := storageUpload(t, u)
		imgs, err := u.take([]string{id}, "phone", session, "r")
		if err != nil {
			t.Fatal(err)
		}
		if err := u.expose(imgs); err != nil {
			t.Fatal(err)
		}
		if session != "unconfirmed" {
			u.embedded(id)
		}
		if session != "queued" {
			u.releaseRefs(imgs, session, "r")
		}
		tr := u.byImage(id)
		tr.AccessMs = time.Now().Add(-time.Hour).UnixMilli() + int64(len(images))
		if err := u.save(tr); err != nil {
			t.Fatal(err)
		}
		images = append(images, imgs)
	}
	pins := map[string]bool{"running": true}
	usage, err := u.usage(false, pins)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ReclaimableBytes == 0 || usage.ProtectedBytes == 0 {
		t.Fatalf("wrong classification: %v", usage)
	}
	u.quota = usage.UploadBytes
	u.mu.Lock()
	err = u.makeRoom(1, pins)
	u.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(images[0][0].Path); !os.IsNotExist(err) {
		t.Fatal("oldest was not evicted", err)
	}
	for _, i := range []int{1, 2, 3, 4} {
		if _, err := os.Stat(images[i][0].Path); err != nil {
			t.Fatalf("image %d evicted too soon: %v", i, err)
		}
	}
	u.quota = 1
	u.mu.Lock()
	err = u.makeRoom(1, pins)
	u.mu.Unlock()
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatal("protected bytes should refuse upload", err)
	}
	for _, i := range []int{2, 3, 4} {
		if _, err := os.Stat(images[i][0].Path); err != nil {
			t.Fatalf("protected image %d evicted: %v", i, err)
		}
	}
}

func TestRestartReconcilesCancelledAndEmbeddedImageRecords(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	id := storageUpload(t, u)
	imgs, err := u.take([]string{id}, "phone", "s", "cancelled")
	if err != nil {
		t.Fatal(err)
	}
	dir := paths.SessionDir("s")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	log, err := eventlog.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	// Queue removal saved, crash before reference removal.
	u.restoreRefs(&session{meta: meta{ID: "s"}, queue: &queueState{}, log: log})
	if _, err := os.Stat(imgs[0].Path); !os.IsNotExist(err) {
		t.Fatal("cancelled original survived recovery", err)
	}
	id = storageUpload(t, u)
	imgs, err = u.take([]string{id}, "phone", "s", "sent")
	if err != nil {
		t.Fatal(err)
	}
	if err := u.expose(imgs); err != nil {
		t.Fatal(err)
	}
	ev := &pb.Event{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Images: []*pb.ImageRef{{UploadId: id, ImageId: "embedded"}}}}}
	if err := log.Append("s", []*pb.Event{ev}, "", 0); err != nil {
		t.Fatal(err)
	}
	// Event durable, crash before embedded acknowledgement and reference release.
	u.restoreRefs(&session{meta: meta{ID: "s"}, queue: &queueState{}, log: log})
	tr := u.byImage(id)
	if tr == nil || !tr.Embedded || len(tr.Refs) != 0 {
		t.Fatalf("lost embedded witness: %v", tr)
	}
}

func TestImageCacheLRUAndPreviewRegeneration(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	d := New(Config{})
	dir := paths.SessionDir("s")
	st := openImageStore(dir)
	d.sessions["s"] = &session{images: st}
	found := imageFound(t, 100, 80)
	st.process(found, "")
	id := found.Ref.ImageId
	if id == "" {
		t.Fatal("no image")
	}
	// Upload is complete; a cache purge cannot undo its server state.
	st.uploaded(id, true, time.Now().Add(30*24*time.Hour).UnixMilli())
	if n, err := d.trimImageCache(0); err != nil || n != 0 {
		t.Fatalf("trim: %d %v", n, err)
	}
	if !st.upload(id).Done {
		t.Fatal("evicting preview forgot the uploaded image")
	}
	next := imageFound(t, 100, 80)
	st.process(next, "")
	if len(next.Ref.Preview) == 0 {
		t.Fatal("a repeat appearance could not rebuild its preview")
	}
	// Pending display evicted while offline must become preview-only.
	fresh := imageFound(t, 130, 80)
	st.process(fresh, "")
	if _, err := d.trimImageCache(0); err != nil {
		t.Fatal(err)
	}
	if !st.upload(fresh.Ref.ImageId).Failed {
		t.Fatal("evicted pending display still advertised as available")
	}
}

func imageFound(t *testing.T, w, h int) agent.FoundImage {
	t.Helper()
	b := testPNG(t, w, h, color.RGBA{3, 4, 5, 255})
	return agent.FoundImage{Ref: &pb.ImageRef{}, Data: base64.StdEncoding.EncodeToString(b)}
}

func TestImageQuotaSurvivesRestart(t *testing.T) {
	h := newHarness(t, Config{})
	chosen := int64(5 << 30)
	if _, err := h.d.updateSettings(&pb.DaemonSettings{ImageUploadQuotaBytes: chosen}); err != nil {
		t.Fatal(err)
	}
	if n := New(Config{}).settings().ImageUploadQuotaBytes; n != chosen {
		t.Fatalf("restart lost quota: %d", n)
	}
	if _, err := h.d.updateSettings(&pb.DaemonSettings{ImageUploadQuotaBytes: -1}); err == nil {
		t.Fatal("negative quota accepted")
	}
}
