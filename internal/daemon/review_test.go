package daemon

import (
	"bytes"
	"errors"
	"github.com/Lewin671/remote-agent/internal/claude"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/tail"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// G6: an image named twice is refused before a session is started.
func TestCreateRefusesDuplicateImages(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.d.uploads
	st := completeUpload(t, u, "local")
	_, err := h.d.createBy(&pb.CreateSessionRequest{Cwd: h.dir, Prompt: "x", ImageIds: []string{st.ImageId, st.ImageId}}, originLocal, false, false, "the terminal's PATH")
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
	l, err := holdLease(c, injectBase)
	if err != nil {
		t.Fatal(err)
	}
	defer l.end()
	if h.s.clearPasted(c, l, input{Images: imgs}, []int32{1}, "GPT-6-Sol medium · /w", false, false) {
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
	synctest.Test(t, testRestartWaitsForLateConversion)
}

func testRestartWaitsForLateConversion(t *testing.T) {
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
	synctest.Test(t, testRestartWithEmptyBoxFails)
}

func testRestartWithEmptyBoxFails(t *testing.T) {
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
	synctest.Test(t, testNoWritesAfterTheHoldLapses)
}

func testNoWritesAfterTheHoldLapses(t *testing.T) {
	h := newHarness(t, Config{})
	imgs := imagesFor(t, h.dir, "a.png")
	h.s.term = failingHoldTerm{h.term}
	h.term.onWrite = func(f *fakeTerm, b string) {
		if strings.Contains(b, imgs[0].Path) {
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
	annotateLogged(h.s, first)
	if first.Origin.GetDeviceId() != "dev" {
		t.Fatalf("first: %v", first.Origin)
	}
	h.s.finishInput(in, pb.InputStatus_INPUT_STATUS_SUBMITTED, "")
	second := msg()
	annotateLogged(h.s, second)
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
	annotateLogged(h.s, third)
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

// G13: an upload failing on the file system is logged without the path.
func TestUploadErrorsLogNoPaths(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	err := uploadErr(&os.PathError{Op: "open", Path: "/home/me/.remote-agent/uploads/secret.png", Err: os.ErrPermission})
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "secret.png") || strings.Contains(err.Error(), "secret.png") {
		t.Fatalf("path logged: %s", buf.String())
	}
}

// A client starting a session whose default here is full access must
// confirm it; the machine metadata says what the default is.
// The default a session would get is reported, full access included; it
// needs no confirmation (architecture §7), so creating is not refused.
func TestClaudeDefaultModeReported(t *testing.T) {
	h := newHarness(t, Config{ClaudeBin: "/bin/true"})
	claudeHome := t.TempDir()
	h.d.agents[pb.Agent_AGENT_CLAUDE] = claude.New("/bin/true", claudeHome)
	os.WriteFile(filepath.Join(claudeHome, "settings.json"), []byte(`{"permissions":{"defaultMode":"bypassPermissions"}}`), 0o600)
	var info *pb.AgentInfo
	for _, a := range h.d.MachineMeta().Agents {
		if a.Agent == pb.Agent_AGENT_CLAUDE {
			info = a
		}
	}
	if info == nil || info.DefaultMode != pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS || info.DefaultModeSource != pb.DefaultModeSource_DEFAULT_MODE_SOURCE_CLI_CONFIG || !info.DefaultModeVaries {
		t.Fatalf("agent info %v", info)
	}
	// An unreadable configuration is reported as unknown.
	os.WriteFile(filepath.Join(claudeHome, "settings.json"), []byte(`{broken`), 0o600)
	if m, src, _ := h.d.defaultMode(h.d.agents[pb.Agent_AGENT_CLAUDE], ""); src != pb.DefaultModeSource_DEFAULT_MODE_SOURCE_UNSPECIFIED || m != pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		t.Fatalf("broken settings: %v %v", m, src)
	}
}

func TestCodexDefaultMode(t *testing.T) {
	home := t.TempDir()
	a := codex.New("/bin/true", home, filepath.Join(home, "history.jsonl"))
	if m, known, _ := a.DefaultMode(""); !known || m != pb.ApprovalMode_APPROVAL_MODE_ASK {
		t.Fatalf("no config: %v %v", m, known)
	}
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = \"x\"\nprofile = \"yolo\"\n\n[profiles.yolo]\napproval_policy = \"never\"\nsandbox_mode = \"danger-full-access\" # all\n"), 0o600)
	if m, known, _ := a.DefaultMode(""); !known || m != pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS {
		t.Fatalf("profile: %v %v", m, known)
	}
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("approvals_reviewer = \"auto_review\"\n"), 0o600)
	if m, _, _ := a.DefaultMode(""); m != pb.ApprovalMode_APPROVAL_MODE_AUTO {
		t.Fatalf("auto: %v", m)
	}
}

// V3: a second interrupt that fails leaves the first one's origin.
func TestFailedInterruptKeepsEarlierOrigin(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.running = true
	a := &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "a"}
	b := &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "b"}
	if _, err := h.s.interruptBy(a); err != nil {
		t.Fatal(err)
	}
	h.term.failWrites = true
	if _, err := h.s.interruptBy(b); err == nil {
		t.Fatal("write did not fail")
	}
	if h.s.interruptedBy.GetDeviceId() != "a" {
		t.Fatalf("origin %v", h.s.interruptedBy)
	}
}

// V2: when logging a parsed user message fails, it is read again; its
// origin must still be found then.
func TestOriginSurvivesAFailedLogWrite(t *testing.T) {
	h := newHarness(t, Config{})
	path := filepath.Join(h.dir, "rollout.jsonl")
	os.WriteFile(path, []byte(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"UserMessage","id":"u","content":[{"type":"text","text":"hi"}]}}}`+"\n"), 0o600)
	h.s.reader = tail.NewReader(path, 0)
	off := int64(0)
	h.s.queue.Queue = []input{{Text: "hi", RequestID: "r1", Inflight: &off, Origin: storeOrigin(&pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "dev"})}}
	h.s.log.Close()
	h.s.readRollout() // fails to log
	log, err := eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	h.s.log = log
	h.s.readRollout()
	for _, e := range h.events() {
		if m := e.GetUserMessage(); m != nil {
			if e.Origin.GetDeviceId() != "dev" || m.RequestId != "r1" {
				t.Fatalf("origin %v request %q", e.Origin, m.RequestId)
			}
			return
		}
	}
	t.Fatal("user message not logged")
}

// annotateLogged annotates as readRollout does after a successful write.
func annotateLogged(s *session, evs ...*pb.Event) {
	consume := s.annotate(evs)
	s.mu.Lock()
	consume()
	s.mu.Unlock()
}

// V1: a creation that stopped before its CLI started gives its first
// message's images back instead of deleting them with the session.
func TestUnstartedCreationReturnsImages(t *testing.T) {
	h := newHarness(t, Config{})
	st := completeUpload(t, h.d.uploads, "local")
	id := "fedcba9876543210"
	dir := t.TempDir()
	m := meta{ID: id, Tmux: "ra-fedcba98", Starting: true}
	m.save(dir)
	if _, err := h.d.uploads.take([]string{st.ImageId}, "local", id, "r1"); err != nil {
		t.Fatal(err)
	}
	if !h.d.unstarted(&m, dir, false) {
		t.Fatal("unstarted creation kept")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("session directory kept")
	}
	if err := h.d.uploads.check([]string{st.ImageId}, "local"); err != nil {
		t.Fatalf("image not back: %v", err)
	}
	// A creation whose CLI did start is kept.
	m2 := meta{ID: id, Tmux: "ra-fedcba98", Starting: true}
	dir2 := t.TempDir()
	m2.save(dir2)
	if h.d.unstarted(&m2, dir2, true) {
		t.Fatal("started creation dropped")
	}
	if loaded, _ := loadMeta(dir2); loaded.Starting {
		t.Fatal("starting flag left")
	}
}

// slowCaptureTerm is a terminal whose n-th screen capture takes a while,
// and whose PTY host stops renewing holds.
type slowCaptureTerm struct {
	failingHoldTerm
	n, slow int
	delay   time.Duration
}

func (f *slowCaptureTerm) Capture() (string, error) {
	f.n++
	if f.n == f.slow {
		time.Sleep(f.delay)
	}
	return f.fakeTerm.Capture()
}

// The hold is checked after the last look at the screen, not before it: a
// capture that hangs until the hold has lapsed is followed by no write.
func TestNoEnterAfterTheLastCaptureOutlivesTheHold(t *testing.T) {
	h := newHarness(t, Config{})
	// The second capture is the check right before the text and Enter.
	h.s.term = &slowCaptureTerm{failingHoldTerm: failingHoldTerm{h.term}, slow: 2, delay: holdFor}
	if _, err := h.s.send("late", "r1"); err != nil {
		t.Fatal(err)
	}
	h.s.status = pb.SessionStatus_SESSION_STATUS_IDLE
	outcome, err := h.s.inject(h.s.queue.Queue[0])
	if err != nil || outcome != notWritten {
		t.Fatalf("outcome %v, %v", outcome, err)
	}
	if w := h.term.written(); len(w) != 0 {
		t.Fatalf("wrote %q after the hold lapsed", w)
	}
	if in := h.s.queue.Queue[0]; in.Inflight != nil || in.Stage != pb.InputStage_INPUT_STAGE_WAITING {
		t.Fatalf("left marked as written: %+v", in)
	}
}
