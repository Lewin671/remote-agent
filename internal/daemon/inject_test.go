package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// composerScreen is Codex's screen with text in the input box.
func composerScreen(text string) string {
	if text == "" {
		return idleScreen
	}
	return "\x1b[1m›\x1b[0m " + text + "\n\n  GPT-6-Sol medium · /w\n"
}

// fakeCodexImages makes the fake terminal act like Codex taking pasted
// image paths: each path in accept becomes the next "[Image #N]"; other
// paths show as text. Ctrl-U empties the box, and submitting records the
// text with its placeholders in history.jsonl.
func fakeCodexImages(t *testing.T, h *harness, accept map[string]bool) {
	box := ""
	n := 0
	h.term.onWrite = func(f *fakeTerm, b string) {
		switch {
		case b == "\x15":
			box = ""
		case strings.HasPrefix(b, "\x1b[200~") && strings.HasSuffix(b, "\x1b[201~"):
			path := strings.TrimSuffix(strings.TrimPrefix(b, "\x1b[200~"), "\x1b[201~")
			sep := ""
			if box != "" {
				sep = " "
			}
			if accept[path] {
				n++
				box += sep + "[Image #" + string(rune('0'+n)) + "]"
			} else {
				box += sep + path
			}
		case strings.HasSuffix(b, "\x1b[201~\r"):
			text := strings.TrimSuffix(strings.TrimPrefix(b, "\x1b[200~"), "\x1b[201~\r")
			line, _ := json.Marshal(map[string]string{"session_id": "x", "text": box + " " + text})
			fh, _ := os.OpenFile(f.history, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			fh.Write(append(line, '\n'))
			fh.Close()
			box, n = "", 0
		default:
			box += b // keys typed locally
		}
		f.screen = composerScreen(box)
	}
}

func imagesFor(t *testing.T, dir string, names ...string) []queuedImage {
	var out []queuedImage
	for i, name := range names {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("img"), 0o600)
		out = append(out, queuedImage{ID: strings.Repeat(string(rune('a'+i)), 32), Path: p})
	}
	return out
}

func inputStates(h *harness, id string) []*pb.InputState {
	var out []*pb.InputState
	for _, e := range h.events() {
		if st := e.GetInputState(); st != nil && st.RequestId == id {
			out = append(out, st)
		}
	}
	return out
}

func runInjector(t *testing.T, h *harness) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.s.status = pb.SessionStatus_SESSION_STATUS_IDLE
	go h.s.injectLoop(ctx)
}

func TestSendWithImages(t *testing.T) {
	h := newHarness(t, Config{})
	imgs := imagesFor(t, h.dir, "a.png", "b.png")
	fakeCodexImages(t, h, map[string]bool{imgs[0].Path: true, imgs[1].Path: true})
	origin := &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "dev", RequestId: "r1"}
	if _, err := h.s.enqueue("what are these", "r1", imgs, origin); err != nil {
		t.Fatal(err)
	}
	runInjector(t, h)
	eventually(t, "submitted", func() bool {
		st := inputStates(h, "r1")
		return len(st) > 0 && st[len(st)-1].Status == pb.InputStatus_INPUT_STATUS_SUBMITTED
	})
	w := h.term.written()
	if len(w) != 3 || !strings.Contains(w[0], imgs[0].Path) || !strings.Contains(w[1], imgs[1].Path) || !strings.HasSuffix(w[2], "what are these\x1b[201~\r") {
		t.Fatalf("writes %q", w)
	}
	st := inputStates(h, "r1")
	if st[0].Text != "what are these" || len(st[0].ImageIds) != 2 || st[0].Stage != pb.InputStage_INPUT_STAGE_WAITING {
		t.Fatalf("queued state %v", st[0])
	}
	var stages []pb.InputStage
	for _, s := range st {
		stages = append(stages, s.Stage)
	}
	want := []pb.InputStage{pb.InputStage_INPUT_STAGE_WAITING, pb.InputStage_INPUT_STAGE_PASTING_IMAGES, pb.InputStage_INPUT_STAGE_IMAGES_RECOGNIZED, pb.InputStage_INPUT_STAGE_SUBMITTING, 0}
	if len(stages) != len(want) {
		t.Fatalf("stages %v", stages)
	}
	for _, e := range h.events() {
		if e.GetInputState().GetStatus() == pb.InputStatus_INPUT_STATUS_QUEUED && e.GetInputState().Text != "" && e.Origin.GetDeviceId() != "dev" {
			t.Fatal("queued state without its origin")
		}
	}
}

func TestSendWithImagesStopsWhenOneIsNotTaken(t *testing.T) {
	h := newHarness(t, Config{})
	imgs := imagesFor(t, h.dir, "a.png", "b.heic")
	fakeCodexImages(t, h, map[string]bool{imgs[0].Path: true})
	h.s.enqueue("two", "r1", imgs, originLocal)
	runInjector(t, h)
	eventually(t, "failed", func() bool {
		st := inputStates(h, "r1")
		return len(st) > 0 && st[len(st)-1].Status == pb.InputStatus_INPUT_STATUS_FAILED
	})
	st := inputStates(h, "r1")
	if r := st[len(st)-1].Reason; r != "image_not_recognized" {
		t.Fatalf("reason %q", r)
	}
	for _, w := range h.term.written() {
		if strings.Contains(w, "\r") {
			t.Fatalf("Enter written: %q", w)
		}
	}
	if w := h.term.written(); w[len(w)-1] != "\x15" {
		t.Fatalf("input box not cleared: %q", w)
	}
	if len(h.s.queue.Queue) != 0 {
		t.Fatal("message left queued")
	}
}

// When the input box holds something that is not ours, nothing is deleted
// and the message needs the terminal.
func TestSendWithImagesLeavesForeignText(t *testing.T) {
	h := newHarness(t, Config{})
	imgs := imagesFor(t, h.dir, "a.png", "b.png")
	fakeCodexImages(t, h, map[string]bool{imgs[0].Path: true, imgs[1].Path: true})
	inner := h.term.onWrite
	h.term.onWrite = func(f *fakeTerm, b string) {
		inner(f, b)
		if strings.Contains(b, imgs[0].Path) {
			inner(f, "typed") // the hold lapsed: local keys got in
		}
	}
	h.s.enqueue("two", "r1", imgs, originLocal)
	runInjector(t, h)
	eventually(t, "needs terminal", func() bool {
		st := inputStates(h, "r1")
		return len(st) > 0 && st[len(st)-1].Status == pb.InputStatus_INPUT_STATUS_NEEDS_TERMINAL
	})
	for _, w := range h.term.written() {
		if w == "\x15" || strings.Contains(w, "\r") {
			t.Fatalf("wrote %q into a box that is not ours", w)
		}
	}
}

// After a restart in the middle of pasting, the hold was released and the
// user may have typed or pasted meanwhile: nothing is deleted, the message
// is not written again and needs the terminal.
func TestRestartWhilePastingImages(t *testing.T) {
	h := newHarness(t, Config{})
	imgs := imagesFor(t, h.dir, "a.png", "b.png")
	h.s.queue.Queue = []input{{Text: "two", RequestID: "r1", Images: imgs, Stage: pb.InputStage_INPUT_STAGE_PASTING_IMAGES}}
	h.s.queue.save(h.dir)
	fakeCodexImages(t, h, nil)
	// The first image was taken, the daemon stopped before saving it.
	h.term.setScreen(composerScreen("[Image #1]"))
	runInjector(t, h)
	eventually(t, "needs terminal", func() bool {
		st := inputStates(h, "r1")
		return len(st) > 0 && st[len(st)-1].Status == pb.InputStatus_INPUT_STATUS_NEEDS_TERMINAL && st[len(st)-1].Reason == "restarted"
	})
	if w := h.term.written(); len(w) != 0 {
		t.Fatalf("writes %q", w)
	}
}

// A hold is renewed while images are pasted.
func TestLeaseRenews(t *testing.T) {
	f := &fakeTerm{}
	c := &countingControl{fakeControl: fakeControl{f}}
	l, err := holdLease(c, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(holdFor + holdFor/2)
	l.end()
	if c.holds < 3 || f.held {
		t.Fatalf("%d holds, held %v", c.holds, f.held)
	}
}

type countingControl struct {
	fakeControl
	holds int
}

func (c *countingControl) Hold(d time.Duration) error { c.holds++; return c.fakeControl.Hold(d) }

// A session that ends between the Enter and the injector's next look at the
// queue has settled the message already: the injector does not settle it
// again from the copy it took before the message was marked as written.
func TestExitWhileWritingDoesNotSettleTwice(t *testing.T) {
	h := newHarness(t, Config{SubmitTimeout: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.s.status = pb.SessionStatus_SESSION_STATUS_IDLE
	h.term.history = filepath.Join(h.dir, "elsewhere.jsonl")
	exited := make(chan struct{})
	h.term.onWrite = func(f *fakeTerm, b string) {
		// The CLI goes away as the Enter arrives.
		go func() { h.s.markExited("stopped"); cancel(); close(exited) }()
		<-exited
	}
	if _, err := h.s.send("last words", "r1"); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("injector panicked: %v", r)
			}
		}()
		h.s.injectLoop(ctx)
	}()
	if st := inputStates(h, "r1"); st[len(st)-1].Status != pb.InputStatus_INPUT_STATUS_UNCONFIRMED {
		t.Fatalf("states %v", st)
	}
}
