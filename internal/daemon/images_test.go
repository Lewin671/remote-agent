package daemon

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"google.golang.org/protobuf/proto"
)

func testPNG(t *testing.T, w, h int, c color.Color) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImageStore(t *testing.T) {
	dir := t.TempDir()
	st := openImageStore(dir)
	red := testPNG(t, 800, 400, color.RGBA{255, 0, 0, 255})
	ref := &pb.ImageRef{Source: pb.ImageSource_IMAGE_SOURCE_USER, CliIndex: 1}
	st.process(agent.FoundImage{Ref: ref, Data: "data:image/png;base64," + base64.StdEncoding.EncodeToString(red)}, "")
	if ref.ImageId == "" || ref.Sync != pb.ImageSync_IMAGE_SYNC_SYNCED || ref.Width != 800 || ref.Height != 400 ||
		ref.Format != pb.ImageFormat_IMAGE_FORMAT_PNG || ref.Bytes != int64(len(red)) || ref.PreviewWidth != 320 || len(ref.Preview) == 0 ||
		ref.DisplayWidth != 800 || ref.CliIndex != 1 {
		t.Fatalf("ref %v", ref)
	}
	// The same picture again reuses the ID; a restart keeps it.
	st = openImageStore(dir)
	again := &pb.ImageRef{}
	st.process(agent.FoundImage{Ref: again, Data: base64.StdEncoding.EncodeToString(red)}, "")
	if again.ImageId != ref.ImageId || !bytes.Equal(again.Preview, ref.Preview) {
		t.Fatalf("not deduplicated: %v", again)
	}
	u := st.upload(ref.ImageId)
	payload := &pb.ImagePayload{}
	if u.Done || u.Failed || proto.Unmarshal(u.Payload, payload) != nil || payload.Width != 800 || payload.Format != pb.ImageFormat_IMAGE_FORMAT_JPEG {
		t.Fatalf("upload %+v %v", u, payload)
	}
	st.uploaded(ref.ImageId, true, time.Now().Add(30*24*time.Hour).UnixMilli())
	if !st.upload(ref.ImageId).Done {
		t.Fatal("not done")
	}
	if _, err := os.Stat(st.displayPath(ref.ImageId)); !os.IsNotExist(err) {
		t.Fatal("display copy kept after upload")
	}
	// Near its expiry an image is not reused: it gets a new ID.
	st.mu.Lock()
	st.byID[ref.ImageId].ExpiresMs = time.Now().Add(time.Hour).UnixMilli()
	st.mu.Unlock()
	late := &pb.ImageRef{}
	st.process(agent.FoundImage{Ref: late, Data: base64.StdEncoding.EncodeToString(red)}, "")
	if late.ImageId == "" || late.ImageId == ref.ImageId {
		t.Fatalf("expiring image reused: %v", late.ImageId)
	}

	// What does not decode keeps only what is known.
	bad := &pb.ImageRef{Path: "/x.png"}
	st.process(agent.FoundImage{Ref: bad, Data: base64.StdEncoding.EncodeToString([]byte("not an image"))}, "")
	if bad.Sync != pb.ImageSync_IMAGE_SYNC_NAME_ONLY || bad.ImageId != "" || bad.Path != "/x.png" {
		t.Fatalf("bad %v", bad)
	}
	// A failed upload leaves the preview.
	blue := &pb.ImageRef{}
	st.process(agent.FoundImage{Ref: blue, Data: base64.StdEncoding.EncodeToString(testPNG(t, 10, 10, color.RGBA{0, 0, 255, 255}))}, "")
	st.uploaded(blue.ImageId, false, 0)
	blue2 := &pb.ImageRef{}
	st.process(agent.FoundImage{Ref: blue2, Data: base64.StdEncoding.EncodeToString(testPNG(t, 10, 10, color.RGBA{0, 0, 255, 255}))}, "")
	if blue2.Sync != pb.ImageSync_IMAGE_SYNC_PREVIEW_ONLY || len(blue2.Preview) == 0 {
		t.Fatalf("failed image ref %v", blue2)
	}
	if fi, err := os.Stat(filepath.Join(dir, "images", "key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v", err)
	}
}
