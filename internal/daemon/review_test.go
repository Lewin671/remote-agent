package daemon

import (
	"context"
	"errors"
	"strings"
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

// failingHoldTerm is a terminal whose PTY host stops renewing holds after
// the first: the hold lapses a few seconds later.
type failingHoldTerm struct{ *fakeTerm }

func (f failingHoldTerm) Control() (control, error) {
	return &failingHold{fakeControl: fakeControl{f.fakeTerm}}, nil
}

type failingHold struct {
	fakeControl
	n int
}

func (c *failingHold) Hold(d time.Duration) error {
	c.n++
	if c.n > 1 {
		return errors.New("stuck")
	}
	return c.fakeControl.Hold(d)
}

// G4: once the hold may have lapsed, nothing more is written: not the
// text and Enter, not Ctrl-U.
func TestNoWritesAfterTheHoldLapses(t *testing.T) {
	h := newHarness(t, Config{})
	imgs := imagesFor(t, h.dir, "a.png")
	h.s.term = failingHoldTerm{h.term}
	pasted := time.Time{}
	h.term.onWrite = func(f *fakeTerm, b string) {
		if strings.Contains(b, imgs[0].Path) {
			pasted = time.Now()
			go func() {
				// Converted slowly, after the hold lapsed.
				time.Sleep(holdFor)
				f.mu.Lock()
				f.screen = composerScreen("[Image #1]")
				f.mu.Unlock()
			}()
		}
	}
	h.s.enqueue("late", "r1", imgs, originLocal)
	runInjector(t, h)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st := inputStates(h, "r1")
		if len(st) > 0 && st[len(st)-1].Status != pb.InputStatus_INPUT_STATUS_QUEUED {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = pasted
	for _, w := range h.term.written() {
		if strings.Contains(w, "\r") || w == "\x15" {
			t.Fatalf("wrote %q after the hold lapsed", w)
		}
	}
	st := inputStates(h, "r1")
	if s := st[len(st)-1]; s.Status != pb.InputStatus_INPUT_STATUS_NEEDS_TERMINAL {
		t.Fatalf("status %v", s.Status)
	}
}

// G7: a written message is matched to one record of it only; a later
// message with the same text typed locally is the local user's.
func TestOriginMatchesOnce(t *testing.T) {
	h := newHarness(t, Config{})
	remote := &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "dev"}
	in := input{Text: "yes", RequestID: "r1", Origin: storeOrigin(remote)}
	off := int64(0)
	in.Inflight = &off
	h.s.queue.Queue = []input{in}
	msg := func() *pb.Event {
		return &pb.Event{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "yes"}}}
	}
	first := msg()
	h.s.annotate([]*pb.Event{first})
	if first.Origin.GetDeviceId() != "dev" {
		t.Fatalf("first: %v", first.Origin)
	}
	h.s.popInput("r1")
	second := msg()
	h.s.annotate([]*pb.Event{second})
	if second.Origin.GetKind() != pb.OriginKind_ORIGIN_KIND_LOCAL {
		t.Fatalf("second: %v", second.Origin)
	}
	// Two remembered messages with the same text from different devices:
	// which one this is cannot be told.
	h.s.recent = []input{
		{Text: "ok", RequestID: "a", Origin: storeOrigin(&pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "d1"}), Inflight: &off},
		{Text: "ok", RequestID: "b", Origin: storeOrigin(&pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "d2"}), Inflight: &off},
	}
	third := &pb.Event{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "ok"}}}
	h.s.annotate([]*pb.Event{third})
	if third.Origin.GetDeviceId() != "" || third.GetUserMessage().RequestId != "" {
		t.Fatalf("ambiguous: %v %q", third.Origin, third.GetUserMessage().RequestId)
	}
}

// G8: the transcript may record the interrupt before the write returns;
// its origin is known by then.
func TestInterruptOriginIsKnownBeforeTheKey(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.running = true
	remote := &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "dev"}
	ended := &pb.Event{Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_ABORTED}}}
	h.term.onWrite = func(f *fakeTerm, b string) {
		if b == "\x1b" {
			h.s.annotate([]*pb.Event{ended})
		}
	}
	if ok, err := h.s.interruptBy(remote); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ended.Origin.GetDeviceId() != "dev" {
		t.Fatalf("origin %v", ended.Origin)
	}
}
