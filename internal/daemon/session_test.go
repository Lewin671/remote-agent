package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/eventlog"
)

const (
	idleScreen  = "\x1b[1m›\x1b[0m \x1b[2mAsk Codex to do anything\x1b[0m\n  GPT-6-Sol medium · /w\n"
	draftScreen = "\x1b[1m›\x1b[0m local draft\n  GPT-6-Sol medium · /w\n"
	trustScreen = "  Trust this folder? Codex can read, edit, and run files here.\n\x1b[1;7m› 1. Trust and continue\n\x1b[0m  2. Quit\n"
)

// fakeTerm stands in for tmux and the PTY host. Writes of pasted text are
// recorded and, like Codex, appended to history.jsonl.
type fakeTerm struct {
	mu      sync.Mutex
	screen  string
	alive   bool
	writes  []string
	held    bool
	history string
	ops     []string // hold, release and write, in order
	// failWrites records a write but reports an error, like a lost reply.
	failWrites bool
	// onWrite, if set, lets a test react to keys like Codex would; it runs
	// with the lock held and may change the screen.
	onWrite func(f *fakeTerm, b string)
}

func (f *fakeTerm) Capture() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.screen, nil
}
func (f *fakeTerm) Alive() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.alive }
func (f *fakeTerm) Kill() error { f.mu.Lock(); defer f.mu.Unlock(); f.alive = false; return nil }
func (f *fakeTerm) Control() (control, error) {
	return &fakeControl{f}, nil
}
func (f *fakeTerm) setScreen(s string) { f.mu.Lock(); f.screen = s; f.mu.Unlock() }
func (f *fakeTerm) written() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

type fakeControl struct{ f *fakeTerm }

func (c *fakeControl) Hold(time.Duration) error {
	c.f.mu.Lock()
	c.f.held = true
	c.f.mu.Unlock()
	return nil
}
func (c *fakeControl) Release() error { c.f.mu.Lock(); c.f.held = false; c.f.mu.Unlock(); return nil }
func (c *fakeControl) Close() error   { return nil }
func (c *fakeControl) Write(b []byte) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if !c.f.held && bytes.HasPrefix(b, []byte("\x1b[200~")) {
		panic("pasted without holding local input")
	}
	c.f.writes = append(c.f.writes, string(b))
	c.f.ops = append(c.f.ops, "write")
	if c.f.failWrites {
		return errors.New("reply lost")
	}
	if c.f.onWrite != nil {
		c.f.onWrite(c.f, string(b))
	}
	if text, ok := strings.CutPrefix(string(b), "\x1b[200~"); ok {
		text = strings.TrimSuffix(text, "\x1b[201~\r")
		line, _ := json.Marshal(map[string]string{"session_id": "x", "text": text})
		fh, err := os.OpenFile(c.f.history, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fh.Write(append(line, '\n'))
			fh.Close()
		}
	}
	return nil
}

type harness struct {
	t    *testing.T
	d    *Daemon
	s    *session
	term *fakeTerm
	dir  string
}

func newHarness(t *testing.T, cfg Config) *harness {
	// Keep settings and other state out of the real ~/.remote-agent.
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	t.Helper()
	dir := t.TempDir()
	cfg.HistoryPath = filepath.Join(dir, "history.jsonl")
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 10 * time.Millisecond
	}
	if cfg.ScreenInterval == 0 {
		cfg.ScreenInterval = 10 * time.Millisecond
	}
	if cfg.RetryInterval == 0 {
		cfg.RetryInterval = 10 * time.Millisecond
	}
	if cfg.SubmitTimeout == 0 {
		cfg.SubmitTimeout = time.Second
	}
	d := New(cfg)
	d.ctx = context.Background()
	term := &fakeTerm{screen: idleScreen, alive: true, history: cfg.HistoryPath}
	h := &harness{t: t, d: d, term: term, dir: dir}
	h.open(meta{ID: "0123456789abcdef", Cwd: dir, Tmux: "ra-01234567", CreatedAt: time.Now()})
	return h
}

func (h *harness) open(m meta) {
	h.t.Helper()
	log, err := eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
	if err != nil {
		h.t.Fatal(err)
	}
	h.s = newSession(h.d, h.dir, m, h.term, log)
}

func (h *harness) events() []*pb.Event {
	evs, _ := h.s.log.Since(0)
	return evs
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSendWaitsForLocalDraftAndPopups(t *testing.T) {
	h := newHarness(t, Config{})
	h.term.setScreen(draftScreen)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.s.start(ctx)

	if _, err := h.s.send("first", "r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.send("first", "r1"); err != nil { // retry of the same request
		t.Fatal(err)
	}
	if _, err := h.s.send("second", "r2"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if w := h.term.written(); len(w) != 0 {
		t.Fatalf("wrote over a local draft: %q", w)
	}
	h.term.setScreen(trustScreen)
	time.Sleep(100 * time.Millisecond)
	if w := h.term.written(); len(w) != 0 {
		t.Fatalf("wrote into a popup: %q", w)
	}
	eventually(t, "blocked state", func() bool {
		info := h.s.info()
		return info.Status == pb.SessionStatus_SESSION_STATUS_BLOCKED && info.StatusDetail == "trust folder"
	})

	h.term.setScreen(idleScreen)
	eventually(t, "both messages", func() bool { return len(h.term.written()) == 2 })
	w := h.term.written()
	if w[0] != string(codex.PasteBytes("first")) || w[1] != string(codex.PasteBytes("second")) {
		t.Fatalf("writes %q", w)
	}
	// A written message leaves the queue once it is settled.
	eventually(t, "queue drained", func() bool { return h.s.info().QueuedInputs == 0 })
}

func (h *harness) writeRollout(lines ...string) {
	h.t.Helper()
	path := filepath.Join(h.dir, "rollout.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		f.WriteString(l + "\n")
	}
}

func turnContextLine(reviewer string) string {
	return `{"timestamp":"2026-09-29T10:00:00Z","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-6-sol","effort":"high","approvals_reviewer":"` + reviewer + `"}}`
}

func permissionPayload(h *harness) *codex.HookPayload {
	return &codex.HookPayload{
		HookEventName:  "PermissionRequest",
		SessionID:      "codex-1",
		TurnID:         "t1",
		TranscriptPath: filepath.Join(h.dir, "rollout.jsonl"),
		ToolName:       "Bash",
		ToolInput:      json.RawMessage(`{"command":"touch ~/x","description":"May I?"}`),
	}
}

func TestPermissionWithAutoReviewAbstains(t *testing.T) {
	h := newHarness(t, Config{ApprovalTimeout: time.Minute})
	h.writeRollout(turnContextLine("auto_review"))
	out, err := h.s.hook(context.Background(), permissionPayload(h))
	if err != nil || out != nil {
		t.Fatalf("hook = %q, %v; want no output", out, err)
	}
	for _, e := range h.events() {
		if e.GetApprovalRequested() != nil {
			t.Fatal("auto_review request was forwarded")
		}
	}
}

func TestPermissionAllowDenyTimeout(t *testing.T) {
	h := newHarness(t, Config{ApprovalTimeout: 200 * time.Millisecond})
	h.writeRollout(turnContextLine("user"))

	type result struct {
		out []byte
		err error
	}
	ask := func() (string, chan result) {
		ch := make(chan result, 1)
		go func() {
			out, err := h.s.hook(context.Background(), permissionPayload(h))
			ch <- result{out, err}
		}()
		var id string
		eventually(t, "approval request", func() bool {
			h.s.mu.Lock()
			defer h.s.mu.Unlock()
			for k := range h.s.approvals {
				id = k
			}
			return id != ""
		})
		return id, ch
	}

	id, ch := ask()
	if err := h.s.resolve(id, true, ""); err != nil {
		t.Fatal(err)
	}
	if r := <-ch; r.err != nil || !strings.Contains(string(r.out), `"behavior":"allow"`) {
		t.Fatalf("allow: %s %v", r.out, r.err)
	}
	if err := h.s.resolve(id, true, ""); err == nil {
		t.Fatal("resolving twice should fail")
	}

	id, ch = ask()
	h.s.resolve(id, false, "no")
	if r := <-ch; !strings.Contains(string(r.out), `"behavior":"deny","message":"no"`) {
		t.Fatalf("deny: %s", r.out)
	}

	_, ch = ask()
	if r := <-ch; !strings.Contains(string(r.out), `"behavior":"deny"`) || !strings.Contains(string(r.out), "no answer within") {
		t.Fatalf("timeout: %s", r.out)
	}

	var decisions []pb.ApprovalDecision
	for _, e := range h.events() {
		if r := e.GetApprovalResolved(); r != nil {
			decisions = append(decisions, r.Decision)
		}
		if r := e.GetApprovalRequested(); r != nil && (r.Command != "touch ~/x" || r.Description != "May I?" || r.Deadline == nil) {
			t.Errorf("request event %v", r)
		}
	}
	want := []pb.ApprovalDecision{
		pb.ApprovalDecision_APPROVAL_DECISION_ALLOW,
		pb.ApprovalDecision_APPROVAL_DECISION_DENY,
		pb.ApprovalDecision_APPROVAL_DECISION_TIMEOUT,
	}
	if len(decisions) != 3 || decisions[0] != want[0] || decisions[1] != want[1] || decisions[2] != want[2] {
		t.Fatalf("decisions %v", decisions)
	}
}

// Canceling the hook call (Codex gave up on the hook) records a canceled
// approval rather than leaving it pending.
func TestPermissionCanceled(t *testing.T) {
	h := newHarness(t, Config{ApprovalTimeout: time.Minute})
	h.writeRollout(turnContextLine("user"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.s.hook(ctx, permissionPayload(h)); close(done) }()
	eventually(t, "approval request", func() bool { return h.s.info().PendingApprovals == 1 })
	cancel()
	<-done
	evs := h.events()
	last := evs[len(evs)-1].GetApprovalResolved()
	if last == nil || last.Decision != pb.ApprovalDecision_APPROVAL_DECISION_CANCELED {
		t.Fatalf("last event %v", evs[len(evs)-1])
	}
	if h.s.info().PendingApprovals != 0 {
		t.Fatal("approval still pending")
	}
}

// After a restart, reading resumes after the last consumed rollout line:
// no event is repeated and sequence numbers continue.
func TestRestartContinuesSequence(t *testing.T) {
	h := newHarness(t, Config{})
	h.writeRollout(
		`{"timestamp":"2026-09-29T10:00:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		`{"timestamp":"2026-09-29T10:00:01Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"AgentMessage","id":"m1","content":[{"text":"one"}]}}}`,
		`{"timestamp":"2026-09-29T10:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant"}}`,
	)
	h.s.bindRollout("codex-1", filepath.Join(h.dir, "rollout.jsonl"))
	h.s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{Status: pb.SessionStatus_SESSION_STATUS_IDLE}}})
	if n := len(h.events()); n != 3 {
		t.Fatalf("got %d events before restart", n)
	}
	if !h.s.running {
		t.Fatal("turn should be running")
	}
	m := h.s.meta
	h.s.log.Close()

	h.writeRollout(
		`{"timestamp":"2026-09-29T10:00:02Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","duration_ms":2000}}`,
	)
	h.open(m)
	if !h.s.running {
		t.Fatal("running state not rebuilt from the log")
	}
	h.s.readRollout()
	evs := h.events()
	var kinds []string
	for i, e := range evs {
		if e.Seq != uint64(i+1) {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
		kinds = append(kinds, string(e.ProtoReflect().WhichOneof(e.ProtoReflect().Descriptor().Oneofs().ByName("kind")).Name()))
	}
	if got := strings.Join(kinds, ","); got != "turn_started,agent_message,session_state,turn_ended" {
		t.Fatalf("events after restart: %s", got)
	}
	if h.s.running {
		t.Fatal("turn should have ended")
	}
}

func TestStaleApprovalsCanceledOnRestart(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.emit(&pb.Event{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "a1"}}})
	h.s.emit(&pb.Event{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "a2"}}})
	h.s.emit(&pb.Event{Kind: &pb.Event_ApprovalResolved{ApprovalResolved: &pb.ApprovalResolved{ApprovalId: "a2"}}})
	m := h.s.meta
	h.s.log.Close()
	h.open(m)
	h.s.cancelStaleApprovals()
	evs := h.events()
	last := evs[len(evs)-1].GetApprovalResolved()
	if len(evs) != 4 || last.ApprovalId != "a1" || last.Decision != pb.ApprovalDecision_APPROVAL_DECISION_CANCELED {
		t.Fatalf("events %v", evs)
	}
}

func TestInterruptOnlyWhileRunning(t *testing.T) {
	h := newHarness(t, Config{})
	if ok, err := h.s.interrupt(); ok || err != nil {
		t.Fatalf("idle interrupt = %v, %v", ok, err)
	}
	h.s.emit(&pb.Event{Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
	if ok, err := h.s.interrupt(); !ok || err != nil {
		t.Fatalf("running interrupt = %v, %v", ok, err)
	}
	if w := h.term.written(); len(w) != 1 || w[0] != "\x1b" {
		t.Fatalf("writes %q", w)
	}
}

func TestExitMarksSession(t *testing.T) {
	h := newHarness(t, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.s.start(ctx)
	h.term.Kill()
	select {
	case <-h.s.done:
	case <-time.After(3 * time.Second):
		t.Fatal("session loops did not stop")
	}
	if h.s.info().Status != pb.SessionStatus_SESSION_STATUS_EXITED {
		t.Fatal("not marked exited")
	}
	if _, err := h.s.send("x", ""); err == nil {
		t.Fatal("send to exited session should fail")
	}
	m, err := loadMeta(h.dir)
	if err != nil || !m.Exited {
		t.Fatalf("meta not saved: %+v %v", m, err)
	}
}

func TestSessionEnv(t *testing.T) {
	env := sessionEnv(map[string]string{"PATH": "/bin", "TMUX": "x", "TERM": "xterm", "HTTP_PROXY": "a"}, map[string]string{"HTTP_PROXY": "b"})
	if env["PATH"] != "/bin" || env["HTTP_PROXY"] != "b" || env["TMUX"] != "" || env["TERM"] != "" {
		t.Fatalf("env %v", env)
	}
	if got := shellQuote("/opt/my dir/ra's"); got != `'/opt/my dir/ra'\''s'` {
		t.Fatalf("quote %s", got)
	}
	if got := shellQuote("/usr/local/bin/remote-agent"); got != "/usr/local/bin/remote-agent" {
		t.Fatalf("quote %s", got)
	}
}

func TestInfoSummarizesTheSession(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.emit(
		&pb.Event{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "\n  修复登录超时\n详细说明"}}},
		&pb.Event{Kind: &pb.Event_TurnContext{TurnContext: &pb.TurnContext{Model: "m", Effort: "high", ApprovalsReviewer: "auto_review", Sandbox: "workspace-write"}}},
		&pb.Event{Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{Title: "go test ./..."}}},
		&pb.Event{Kind: &pb.Event_TokenUsage{TokenUsage: &pb.TokenUsage{LastTotalTokens: 1000, ContextWindow: 10000}}},
		&pb.Event{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "second"}}},
	)
	info := h.s.info()
	if info.Title != "修复登录超时" || info.Preview != "✓ go test ./..." {
		t.Fatalf("title %q preview %q", info.Title, info.Preview)
	}
	if info.ApprovalsReviewer != "auto_review" || info.Effort != "high" || info.ContextTokens != 1000 || info.ContextWindow != 10000 {
		t.Fatalf("info %v", info)
	}
	if s := summary(strings.Repeat("长", 200), 60); len([]rune(s)) != 60 || !strings.HasSuffix(s, "…") {
		t.Fatalf("summary %q", s)
	}
}
