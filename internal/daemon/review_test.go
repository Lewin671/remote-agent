package daemon

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// G1: a device ID the server vouches for is no proof: images can only be
// uploaded and used by commands signed by the device, while unsigned text
// messages still work.
func TestImagesNeedAVerifiedDevice(t *testing.T) {
	h := newHarness(t, Config{})
	h.d.sessions[h.s.meta.ID] = h.s
	hd := &handler{d: h.d}
	unverified := agent.WithOrigin(context.Background(), &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_UNVERIFIED_CLIENT, DeviceId: "0123456789abcdef0123456789abcdef"})
	_, err := hd.BeginImageUpload(unverified, connect.NewRequest(&pb.BeginImageUploadRequest{
		TransferId: tid, Format: pb.ImageFormat_IMAGE_FORMAT_PNG, TotalBytes: 10, Sha256: make([]byte, 32),
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("unsigned upload: %v", err)
	}
	_, err = hd.Send(unverified, connect.NewRequest(&pb.SendRequest{SessionId: h.s.meta.ID, Text: "x", ImageIds: []string{tid}}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("unsigned send with images: %v", err)
	}
	if _, err := hd.Send(unverified, connect.NewRequest(&pb.SendRequest{SessionId: h.s.meta.ID, Text: "plain", RequestId: "r9"})); err != nil {
		t.Fatalf("unsigned text: %v", err)
	}
}

// G6: an image named twice is refused before a session is started.
func TestCreateRefusesDuplicateImages(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.d.uploads
	st := completeUpload(t, u, "local")
	_, err := h.d.createBy(&pb.CreateSessionRequest{Cwd: h.dir, Prompt: "x", ImageIds: []string{st.ImageId, st.ImageId}}, originLocal)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("duplicate images: %v", err)
	}
	if len(h.d.sessions) != 0 {
		t.Fatal("a session was created")
	}
}

// G2: a placeholder on the first line and the user's draft on the next is
// not the message's to clear: Ctrl-U would delete a line of the draft.
func TestClearLeavesMultilineDrafts(t *testing.T) {
	h := newHarness(t, Config{})
	imgs := imagesFor(t, h.dir, "a.png")
	h.term.setScreen("\x1b[1m›\x1b[0m [Image #1]\n  my draft\n\n  GPT-6-Sol medium · /w\n")
	c, _ := h.term.Control()
	c.Hold(time.Second)
	if h.s.clearPasted(c, input{Images: imgs}, []int32{1}, "GPT-6-Sol medium · /w", false, false) {
		t.Fatal("cleared a box holding a draft line")
	}
	if w := h.term.written(); len(w) != 0 {
		t.Fatalf("wrote %q", w)
	}
}

// G5: after a restart the CLI may still be converting a pasted image, so
// an empty input box proves nothing at first: a placeholder that shows up
// later makes it the terminal's to sort out, and nothing is deleted.
func TestRestartWaitsForLateConversion(t *testing.T) {
	defer func(w time.Duration) { conversionWindow = w }(conversionWindow)
	conversionWindow = 1500 * time.Millisecond
	h := newHarness(t, Config{})
	imgs := imagesFor(t, h.dir, "a.png")
	h.s.queue.Queue = []input{{Text: "one", RequestID: "r1", Images: imgs, Stage: pb.InputStage_INPUT_STAGE_PASTING_IMAGES}}
	h.s.queue.save(h.dir)
	h.term.setScreen(idleScreen)
	go func() {
		time.Sleep(700 * time.Millisecond)
		h.term.setScreen(composerScreen("[Image #5]"))
	}()
	runInjector(t, h)
	eventually(t, "settled", func() bool {
		st := inputStates(h, "r1")
		return len(st) > 0 && st[len(st)-1].Status != pb.InputStatus_INPUT_STATUS_QUEUED
	})
	st := inputStates(h, "r1")
	if s := st[len(st)-1]; s.Status != pb.InputStatus_INPUT_STATUS_NEEDS_TERMINAL {
		t.Fatalf("status %v", s.Status)
	}
	if w := h.term.written(); len(w) != 0 {
		t.Fatalf("writes %q", w)
	}
}

// With the box empty for the whole window, the message failed cleanly.
func TestRestartWithEmptyBoxFails(t *testing.T) {
	defer func(w time.Duration) { conversionWindow = w }(conversionWindow)
	conversionWindow = 300 * time.Millisecond
	h := newHarness(t, Config{})
	imgs := imagesFor(t, h.dir, "a.png")
	h.s.queue.Queue = []input{{Text: "one", RequestID: "r1", Images: imgs, Stage: pb.InputStage_INPUT_STAGE_IMAGES_RECOGNIZED}}
	h.s.queue.save(h.dir)
	runInjector(t, h)
	eventually(t, "failed", func() bool {
		st := inputStates(h, "r1")
		return len(st) > 0 && st[len(st)-1].Status == pb.InputStatus_INPUT_STATUS_FAILED
	})
}
