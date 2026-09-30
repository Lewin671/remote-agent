package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/tail"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Errors returned by session operations; the RPC layer maps them to codes.
var (
	errExited       = errors.New("session has exited")
	errNotFound     = agent.ErrNotFound
	errExpired      = errors.New("the approval has timed out")
	errNotAnswering = errors.New("the question is not showing in the terminal")
	errHeadChanged  = errors.New("the queue head changed")
	errWriting      = errors.New("the message is being written to the terminal and can no longer be canceled")
)

// holdFor bounds how long local input is held while remote input is
// injected. The screen check and the write take a few milliseconds.
const holdFor = 3 * time.Second

// settle gives the CLI time to draw the keys that arrived just before the
// hold, so that the screen check sees them.
const settle = 80 * time.Millisecond

// session is one CLI session managed by the daemon.
type session struct {
	d    *Daemon
	ag   agent.Adapter
	dir  string
	term terminal
	log  *eventlog.Log

	readMu sync.Mutex // serializes rollout reads
	ctlMu  sync.Mutex // serializes use of the PTY host: inject, answer, interrupt
	parser agent.Parser
	reader *tail.Reader

	mu          sync.Mutex
	meta        meta
	running     bool // a turn is in progress
	context     *pb.TurnContext
	usage       *pb.TokenUsage
	title       string
	preview     string
	screen      agent.Screen
	prevScreen  agent.Screen
	sawComposer bool
	planOpen    bool
	driving     bool // a control operation is using the terminal's menus
	// Why queued input is not being written, and since when.
	waitReason string
	waitSince  time.Time
	warned     bool
	status     pb.SessionStatus
	detail     string
	approvals  map[string]*approval
	questions  map[string]*pb.QuestionAsked
	queue      *queueState
	wake       chan struct{} // pokes the injector
	done       chan struct{} // closed when the session's goroutines stop
	stop       context.CancelFunc
}

type approval struct {
	id       string
	decision chan decision
	// claimed is set, under s.mu, by whoever settles the approval first:
	// a reply, the timeout, a canceled hook, or the session exiting.
	claimed bool
	// deadline is when the approval times out. A reply after it is refused
	// even if the timer has not fired yet.
	deadline time.Time
	// key is text that identifies the request's prompt on the screen, such
	// as the start of the command; seen is set once the prompt showed.
	key  string
	seen bool
	// tool and input are the tool call the request is for, as its
	// ToolCall event will show them.
	tool, input string
}

type decision struct {
	allow   bool
	message string
	// terminal: the request was answered in the terminal; the hook's
	// output no longer matters.
	terminal bool
}

func newSession(d *Daemon, dir string, m meta, term terminal, log *eventlog.Log) *session {
	q, err := loadQueue(dir)
	if err != nil {
		slog.Warn("load input queue; starting empty", "session", m.ID, "err", err)
		q = &queueState{}
	}
	ag := d.adapter(m.Agent)
	s := &session{
		d:         d,
		ag:        ag,
		dir:       dir,
		term:      term,
		log:       log,
		parser:    ag.NewParser(),
		meta:      m,
		approvals: map[string]*approval{},
		questions: map[string]*pb.QuestionAsked{},
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
		status:    pb.SessionStatus_SESSION_STATUS_STARTING,
		queue:     q,
	}
	// Rebuild derived state from the events already logged.
	log.Each(func(e *pb.Event) {
		s.observe(e)
		s.parser.Replay(e)
	})
	if m.Exited {
		s.status = pb.SessionStatus_SESSION_STATUS_EXITED
	}
	if m.RolloutPath != "" {
		path, off := log.Checkpoint()
		if path != m.RolloutPath {
			off = 0
		}
		s.reader = tail.NewReader(m.RolloutPath, off)
	}
	return s
}

// observe updates derived state from an event. Callers hold s.mu or are
// the only goroutine touching the session.
func (s *session) observe(e *pb.Event) {
	switch k := e.Kind.(type) {
	case *pb.Event_TurnStarted:
		s.running = true
	case *pb.Event_TurnEnded:
		s.running = false
	case *pb.Event_TurnContext:
		s.context = k.TurnContext
	case *pb.Event_TokenUsage:
		s.usage = k.TokenUsage
		s.d.noteLimits(k.TokenUsage)
	case *pb.Event_SettingsChanged:
		s.context = s.ag.ApplySettings(s.context, k.SettingsChanged)
	case *pb.Event_UserMessage:
		if s.title == "" {
			s.title = summary(k.UserMessage.Text, 60)
		}
	case *pb.Event_AgentMessage:
		s.preview = summary(k.AgentMessage.Text, 120)
	case *pb.Event_ToolCall:
		s.preview = "✓ " + summary(k.ToolCall.Title, 110)
	case *pb.Event_ToolStarted:
		s.preview = "正在执行 " + summary(k.ToolStarted.Title, 110)
	case *pb.Event_QuestionAsked:
		if !k.QuestionAsked.Async {
			s.questions[k.QuestionAsked.QuestionId] = k.QuestionAsked
		}
	case *pb.Event_QuestionAnswered:
		delete(s.questions, k.QuestionAnswered.QuestionId)
	case *pb.Event_SessionState:
		s.status, s.detail = k.SessionState.Status, k.SessionState.Detail
		if s.status != pb.SessionStatus_SESSION_STATUS_STARTING {
			s.sawComposer = true
		}
	}
	if _, ok := e.Kind.(*pb.Event_TurnEnded); ok {
		// Questions of a finished turn can no longer be answered.
		clear(s.questions)
	}
}

// emit appends daemon-originated events.
func (s *session) emit(evs ...*pb.Event) {
	now := timestamppb.Now()
	settings := false
	for _, e := range evs {
		if e.Time == nil {
			e.Time = now
		}
		if e.GetSettingsChanged() != nil || e.GetTurnContext() != nil {
			settings = true
		}
	}
	if settings {
		// Settings changed through remote-agent or seen on the screen: the
		// parser must know them before it reads further, and the log must
		// have them in the order the parser learned them, or a context
		// parsed just before could be logged after them with the old
		// model. readRollout parses and logs under the same lock.
		s.readMu.Lock()
		defer s.readMu.Unlock()
	}
	if err := s.log.Append(s.meta.ID, evs, "", 0); err != nil {
		slog.Error("append events", "session", s.meta.ID, "err", err)
		return
	}
	s.mu.Lock()
	for _, e := range evs {
		if settings && (e.GetSettingsChanged() != nil || e.GetTurnContext() != nil) {
			s.parser.Replay(e)
		}
		s.observe(e)
	}
	s.mu.Unlock()
}

// start runs the session's background loops until it exits or ctx ends.
func (s *session) start(ctx context.Context) {
	ctx, s.stop = context.WithCancel(ctx)
	done := s.done
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.pollLoop(ctx) }()
	go func() { defer wg.Done(); s.injectLoop(ctx) }()
	go func() { wg.Wait(); close(done) }()
}

func (s *session) pollLoop(ctx context.Context) {
	rollout := time.NewTicker(s.d.cfg.PollInterval)
	defer rollout.Stop()
	screen := time.NewTicker(s.d.cfg.ScreenInterval)
	defer screen.Stop()
	s.checkScreen()
	for {
		select {
		case <-ctx.Done():
			return
		case <-rollout.C:
			s.readRollout()
		case <-screen.C:
			if !s.checkScreen() {
				s.readRollout()
				return
			}
		}
	}
}

// readRollout appends events for rollout lines written since the last read.
func (s *session) readRollout() {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	s.mu.Lock()
	r := s.reader
	s.mu.Unlock()
	if r == nil {
		return
	}
	lines, truncated, err := r.Next()
	if err != nil {
		slog.Warn("read rollout", "session", s.meta.ID, "err", err)
		return
	}
	if truncated {
		slog.Warn("rollout shrank; reading it again from the start", "session", s.meta.ID, "path", r.Path)
	}
	for _, l := range lines {
		evs, err := s.parser.Parse(l.Data)
		if err != nil {
			slog.Warn("parse rollout line", "session", s.meta.ID, "offset", l.End, "err", err)
		}
		if err := s.log.Append(s.meta.ID, evs, r.Path, l.End); err != nil {
			slog.Error("append events", "session", s.meta.ID, "err", err)
			// Read this line and the rest of the batch again next time.
			path, off := s.log.Checkpoint()
			if path != r.Path {
				off = 0
			}
			s.mu.Lock()
			s.reader = tail.NewReader(r.Path, off)
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		for _, e := range evs {
			s.observe(e)
		}
		s.mu.Unlock()
		for _, e := range evs {
			if e.GetTurnEnded() != nil {
				// A request still waiting was answered in the terminal
				// (or interrupted there) since the turn is over.
				s.retractApprovals()
			}
			if c := e.GetToolCall(); c != nil {
				// Its call ran: allowed in the terminal before the
				// status check saw the prompt.
				s.retractFor(c)
			}
		}
	}
}

// checkScreen updates the session status from the screen. It returns false
// once the session has exited.
func (s *session) checkScreen() bool {
	if !s.term.Alive() {
		s.markExited("")
		return false
	}
	styled, err := s.term.Capture()
	if err != nil {
		return true
	}
	scr := s.ag.Analyze(styled)
	s.mu.Lock()
	s.screen = scr
	// Act only on a screen seen twice in a row, so that frames drawn
	// while the TUI starts up or switches views do not flip the status.
	stable := scr == s.prevScreen
	s.prevScreen = scr
	if !stable {
		s.mu.Unlock()
		return true
	}
	if s.driving {
		// Menus opened by a control operation are not the user's popups.
		s.mu.Unlock()
		return true
	}
	s.planOpen = scr.PlanOpen
	retract := s.watchApprovals(scr, styled)
	var modeSeen *pb.TurnContext
	if scr.Mode != "" && (s.context == nil || s.context.PermissionMode != scr.Mode) {
		// Switched in the terminal; the transcript records it only with
		// the next message.
		modeSeen = &pb.TurnContext{}
		if s.context != nil {
			modeSeen = proto.CloneOf(s.context)
		}
		modeSeen.PermissionMode = scr.Mode
	}
	status, detail := s.status, s.detail
	next, nextDetail := status, detail
	switch {
	case scr.PlanOpen:
		next, nextDetail = pb.SessionStatus_SESSION_STATUS_IDLE, ""
	case scr.PermissionOpen && len(s.approvals) > 0:
		// Claude Code's own prompt for a request a client is asked about.
		next, nextDetail = pb.SessionStatus_SESSION_STATUS_IDLE, ""
	case scr.Popup != "" && scr.Popup != "question":
		next, nextDetail = pb.SessionStatus_SESSION_STATUS_BLOCKED, scr.Popup
	case scr.ComposerReady || scr.QuestionOpen || s.sawComposer:
		next, nextDetail = pb.SessionStatus_SESSION_STATUS_IDLE, ""
	}
	s.mu.Unlock()
	for _, a := range retract {
		a.decision <- decision{terminal: true}
	}
	if modeSeen != nil {
		s.emit(&pb.Event{Kind: &pb.Event_TurnContext{TurnContext: modeSeen}})
	}
	// RUNNING is derived from turn events; the state event only records
	// startup, popups and exit.
	if next != status || nextDetail != detail {
		s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{Status: next, Detail: nextDetail}}})
	}
	return true
}

func (s *session) markExited(detail string) {
	s.mu.Lock()
	if s.meta.Exited {
		s.mu.Unlock()
		return
	}
	s.meta.Exited = true
	m := s.meta
	for id, a := range s.approvals {
		if !a.claimed {
			a.claimed = true
			a.decision <- decision{allow: false, message: "remote-agent: session exited"}
		}
		delete(s.approvals, id)
	}
	s.queue.Queue = nil
	if err := s.queue.save(s.dir); err != nil {
		slog.Error("save input queue", "session", m.ID, "err", err)
	}
	s.mu.Unlock()
	if err := m.save(s.dir); err != nil {
		slog.Error("save session", "session", m.ID, "err", err)
	}
	s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{
		Status: pb.SessionStatus_SESSION_STATUS_EXITED, Detail: detail,
	}}})
	if s.stop != nil {
		s.stop()
	}
}

// Info summarizes the session.
func (s *session) info() *pb.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.status
	if status == pb.SessionStatus_SESSION_STATUS_IDLE && s.running {
		status = pb.SessionStatus_SESSION_STATUS_RUNNING
	}
	out := &pb.Session{
		Id:               s.meta.ID,
		Cwd:              s.meta.Cwd,
		TmuxSession:      s.meta.Tmux,
		Status:           status,
		StatusDetail:     s.detail,
		CodexSessionId:   s.meta.CodexSessionID,
		RolloutPath:      s.meta.RolloutPath,
		CreatedAt:        timestamppb.New(s.meta.CreatedAt),
		LastSeq:          s.log.LastSeq(),
		PendingApprovals: int32(len(s.approvals)),
		PendingQuestions: int32(len(s.questions)),
		QueuedInputs:     int32(len(s.queue.Queue)),
		PlanOpen:         s.planOpen,
	}
	if len(s.queue.Queue) > 0 {
		out.InputWait = s.waitReason
		if s.waitReason == "unrecognized" && !s.warned {
			out.InputWait = "" // not yet: it may be a passing frame
		}
	}
	out.Agent = s.ag.Kind()
	if c := s.context; c != nil {
		out.Model, out.Effort = c.Model, c.Effort
		out.ApprovalsReviewer, out.ApprovalPolicy, out.Sandbox, out.CollaborationMode = c.ApprovalsReviewer, c.ApprovalPolicy, c.Sandbox, c.CollaborationMode
		out.PermissionMode = c.PermissionMode
	}
	out.Mode = s.ag.Mode(s.context)
	out.FullAccessUnavailable = s.meta.NoFullAccess
	if u := s.usage; u != nil {
		out.ContextTokens, out.ContextWindow = u.LastTotalTokens, u.ContextWindow
	}
	out.Title, out.Preview = s.title, s.preview
	return out
}

// summary is the first non-empty line of text, at most n characters.
func summary(text string, n int) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if r := []rune(line); len(r) > n {
				return string(r[:n-1]) + "…"
			}
			return line
		}
	}
	return ""
}

// send queues text for injection and saves the queue before returning,
// so an accepted message survives a daemon restart. It returns the queue
// position; a retried request ID is accepted once.
func (s *session) send(text, requestID string) (int, error) {
	s.mu.Lock()
	if s.meta.Exited {
		s.mu.Unlock()
		return 0, errExited
	}
	if requestID == "" {
		requestID = newID(8)
	} else if s.queue.seen(requestID) {
		s.mu.Unlock()
		return 0, nil
	}
	prevQueue, prevSeen := s.queue.Queue, s.queue.Seen
	s.queue.Queue = append(s.queue.Queue[:len(s.queue.Queue):len(s.queue.Queue)], input{Text: text, RequestID: requestID})
	s.queue.remember(requestID)
	if err := s.queue.save(s.dir); err != nil {
		s.queue.Queue, s.queue.Seen = prevQueue, prevSeen
		s.mu.Unlock()
		return 0, fmt.Errorf("save input queue: %w", err)
	}
	pos := len(s.queue.Queue)
	s.mu.Unlock()
	s.emitInput(requestID, pb.InputStatus_INPUT_STATUS_QUEUED)
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return pos, nil
}

func (s *session) emitInput(requestID string, status pb.InputStatus) {
	s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: requestID, Status: status}}})
}

// cancelInput removes a queued message unless it is already being
// written.
func (s *session) cancelInput(requestID string) error {
	s.mu.Lock()
	idx := -1
	for i, in := range s.queue.Queue {
		if in.RequestID == requestID {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("queued message %s: %w", requestID, errNotFound)
	}
	if s.queue.Queue[idx].Inflight != nil {
		s.mu.Unlock()
		return errWriting
	}
	prev := s.queue.Queue
	q := append([]input{}, prev[:idx]...)
	s.queue.Queue = append(q, prev[idx+1:]...)
	if err := s.queue.save(s.dir); err != nil {
		s.queue.Queue = prev
		s.mu.Unlock()
		return fmt.Errorf("save input queue: %w", err)
	}
	s.mu.Unlock()
	s.emitInput(requestID, pb.InputStatus_INPUT_STATUS_CANCELED)
	return nil
}

// unrecognizedAfter is how long the input box may go unrecognized, with
// no popup in front of it, before it is reported: likely a Codex update
// changed how it draws the screen.
const unrecognizedAfter = 20 * time.Second

// noteWait records why queued input cannot be written now.
func (s *session) noteWait(scr agent.Screen) {
	reason := ""
	switch {
	case scr.ComposerReady && scr.DraftEmpty:
	case scr.ComposerReady:
		reason = "draft"
	case scr.Popup != "":
		reason = "popup"
	default:
		reason = "unrecognized"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if reason != s.waitReason {
		s.waitReason, s.waitSince, s.warned = reason, time.Now(), false
	}
	if reason == "unrecognized" && !s.warned && time.Since(s.waitSince) > unrecognizedAfter {
		s.warned = true
		// Only the version and the fact are logged, never the screen.
		slog.Warn("cannot recognize the CLI's input box; queued input waits. A CLI update may have changed its screen; run `remote-agent doctor`",
			"session", s.meta.ID, "agent", s.ag.Name(), "version", s.ag.Version())
	}
}

// injectLoop submits queued input one message at a time, in order.
func (s *session) injectLoop(ctx context.Context) {
	retry := time.NewTimer(time.Hour)
	defer retry.Stop()
	for {
		s.mu.Lock()
		var next input
		ok := len(s.queue.Queue) > 0 && !s.meta.Exited
		if ok {
			next = s.queue.Queue[0]
		}
		s.mu.Unlock()
		if ok {
			if next.Inflight == nil {
				written, err := s.inject(next)
				if err != nil {
					slog.Warn("inject", "session", s.meta.ID, "err", err)
				}
				if !written {
					retry.Reset(s.d.cfg.RetryInterval)
					goto wait
				}
				s.mu.Lock()
				if len(s.queue.Queue) > 0 {
					next = s.queue.Queue[0]
				}
				s.mu.Unlock()
			}
			// The message was written, possibly by a previous daemon.
			// Settle it without ever writing it again.
			if !s.settle(ctx, next) {
				return // ctx done; it stays inflight for the next daemon
			}
			continue
		}
	wait:
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-retry.C:
		}
	}
}

// settle waits for evidence that the written head message was submitted,
// records the outcome and removes it from the queue. It returns false if
// ctx ended first.
func (s *session) settle(ctx context.Context, in input) bool {
	hist := s.newHistory(*in.Inflight)
	deadline := time.Now().Add(s.d.cfg.SubmitTimeout)
	status := pb.InputStatus_INPUT_STATUS_UNCONFIRMED
	for time.Now().Before(deadline) {
		s.readRollout()
		s.mu.Lock()
		cliID := s.meta.CodexSessionID
		s.mu.Unlock()
		if hist.submitted(in.Text, cliID) || s.loggedUserMessage(in.InflightSeq, in.Text) {
			status = pb.InputStatus_INPUT_STATUS_SUBMITTED
			break
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
	if status == pb.InputStatus_INPUT_STATUS_UNCONFIRMED {
		slog.Warn("injected message not confirmed; not sending it again", "session", s.meta.ID, "request", in.RequestID)
	}
	s.popInput()
	s.emitInput(in.RequestID, status)
	return true
}

// loggedUserMessage reports whether a user message with text was logged
// after seq.
func (s *session) loggedUserMessage(seq uint64, text string) bool {
	want := normalizeText(text)
	evs, _ := s.log.Since(seq)
	for _, e := range evs {
		if m := e.GetUserMessage(); m != nil && normalizeText(m.Text) == want {
			return true
		}
	}
	return false
}

func (s *session) popInput() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue.Queue) > 0 {
		s.queue.Queue = s.queue.Queue[1:]
	}
	if err := s.queue.save(s.dir); err != nil {
		slog.Error("save input queue", "session", s.meta.ID, "err", err)
	}
}

// markInflight records, before the head message is written, where
// history.jsonl and the event log stood.
func (s *session) markInflight(offset int64, requestID string) error {
	seq := s.log.LastSeq()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue.Queue) == 0 || s.queue.Queue[0].RequestID != requestID {
		return errHeadChanged
	}
	s.queue.Queue[0].Inflight = &offset
	s.queue.Queue[0].InflightSeq = seq
	if err := s.queue.save(s.dir); err != nil {
		s.queue.Queue[0].Inflight, s.queue.Queue[0].InflightSeq = nil, 0
		return err
	}
	return nil
}

// inject tries once to write text. It returns false, leaving the input
// queued, when the terminal is not ready: a popup is open or the local
// user has an unsent draft, which is never overwritten or merged.
func (s *session) inject(in input) (bool, error) {
	s.ctlMu.Lock()
	defer s.ctlMu.Unlock()
	c, err := s.term.Control()
	if err != nil {
		return false, err
	}
	defer c.Close()
	if err := c.Hold(holdFor); err != nil {
		return false, err
	}
	// Local keys typed after the release reach Codex after the Enter, in
	// the fresh input box.
	defer c.Release()
	time.Sleep(settle)
	styled, err := s.term.Capture()
	if err != nil {
		return false, err
	}
	scr := s.ag.Analyze(styled)
	if !scr.ComposerReady || !scr.DraftEmpty {
		s.noteWait(scr)
		return false, nil
	}
	// While Codex starts, its input box can flash before a startup dialog
	// covers it; wait until the status check has seen a settled screen.
	s.mu.Lock()
	starting := s.status == pb.SessionStatus_SESSION_STATUS_STARTING
	s.mu.Unlock()
	if starting {
		return false, nil
	}
	s.noteWait(scr)
	hist := s.newHistory(-1)
	if err := s.markInflight(hist.offset(), in.RequestID); err != nil {
		if errors.Is(err, errHeadChanged) {
			return false, nil // canceled meanwhile; the loop picks the new head
		}
		return false, err
	}
	if err := c.Write(s.ag.Paste(in.Text)); err != nil {
		// A failed reply does not prove the bytes were not written, so
		// the message is only settled from Codex's records, never resent.
		return true, err
	}
	return true, nil
}

// interrupt presses Esc if a turn is running.
func (s *session) interrupt() (bool, error) {
	s.readRollout()
	s.mu.Lock()
	running, exited := s.running, s.meta.Exited
	s.mu.Unlock()
	if exited {
		return false, errExited
	}
	if !running {
		return false, nil
	}
	s.ctlMu.Lock()
	defer s.ctlMu.Unlock()
	// The turn may have ended while waiting for the lock; Esc in an idle
	// input box does something else.
	s.readRollout()
	s.mu.Lock()
	running = s.running
	s.mu.Unlock()
	if !running {
		return false, nil
	}
	c, err := s.term.Control()
	if err != nil {
		return false, err
	}
	defer c.Close()
	return true, c.Write(s.ag.Interrupt())
}

// answer picks options (1-based) of the question that is showing. With
// index set, that must be the index-th question of the call.
func (s *session) answer(questionID string, options []int32, index *int32) error {
	s.readRollout()
	s.mu.Lock()
	_, ok := s.questions[questionID]
	exited := s.meta.Exited
	s.mu.Unlock()
	if exited {
		return errExited
	}
	if !ok {
		return fmt.Errorf("question %s: %w", questionID, errNotFound)
	}
	if len(options) == 0 {
		return fmt.Errorf("no option given")
	}
	for _, o := range options {
		if o < 1 || o > 9 {
			return fmt.Errorf("option must be between 1 and 9")
		}
	}
	s.ctlMu.Lock()
	defer s.ctlMu.Unlock()
	c, err := s.term.Control()
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Hold(holdFor); err != nil {
		return err
	}
	defer c.Release()
	time.Sleep(settle)
	// The local user may have answered in the meantime, and another
	// question may be showing now: check both after taking the hold.
	s.readRollout()
	s.mu.Lock()
	q, ok := s.questions[questionID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("question %s was already answered: %w", questionID, errNotFound)
	}
	styled, err := s.term.Capture()
	if err != nil {
		return err
	}
	items := q.Questions
	if index != nil {
		if *index < 0 || int(*index) >= len(items) {
			return fmt.Errorf("question index %d out of range", *index)
		}
		items = items[*index : *index+1]
	}
	if !s.ag.Analyze(styled).QuestionOpen || !showsQuestion(styled, items) {
		return errNotAnswering
	}
	return s.ag.Answer(heldTerm{s.term, c}, q, index, options)
}

func (s *session) kill() error {
	s.mu.Lock()
	exited := s.meta.Exited
	s.mu.Unlock()
	if exited {
		return nil
	}
	if err := s.term.Kill(); err != nil && s.term.Alive() {
		return err
	}
	s.markExited("stopped")
	return nil
}

// bindRollout records where the CLI writes the session's transcript.
func (s *session) bindRollout(cliSessionID, path string) {
	if path == "" {
		return
	}
	s.readMu.Lock() // the parser and reader are replaced
	s.mu.Lock()
	if s.meta.RolloutPath == path {
		s.mu.Unlock()
		s.readMu.Unlock()
		return
	}
	s.meta.RolloutPath = path
	if cliSessionID != "" {
		s.meta.CodexSessionID = cliSessionID
	}
	s.reader = tail.NewReader(path, 0)
	p := s.ag.NewParser()
	// It needs what is known so far, such as a model switched to before
	// the transcript was found.
	s.log.Each(p.Replay)
	s.parser = p
	m := s.meta
	s.mu.Unlock()
	s.readMu.Unlock()
	if err := m.save(s.dir); err != nil {
		slog.Error("save session", "session", m.ID, "err", err)
	}
	s.readRollout()
}

// hook handles one hook call and returns what the hook prints.
func (s *session) hook(ctx context.Context, p *agent.Hook) ([]byte, error) {
	switch p.HookEventName {
	case "SessionStart":
		s.bindRollout(p.SessionID, p.TranscriptPath)
		return nil, nil
	case "PermissionRequest":
		s.bindRollout(p.SessionID, p.TranscriptPath)
		return s.permission(ctx, p)
	}
	if evs := s.ag.HookEvents(p); len(evs) > 0 {
		s.bindRollout(p.SessionID, p.TranscriptPath)
		s.readRollout()
		s.readMu.Lock()
		turn := p.TurnID
		if turn == "" {
			turn = p.PromptID
		}
		var fresh []*pb.Event
		for _, e := range evs {
			// The transcript may have had it first.
			if s.parser.Reported(e) {
				continue
			}
			e.TurnId = turn
			s.parser.Replay(e)
			fresh = append(fresh, e)
		}
		s.readMu.Unlock()
		if len(fresh) > 0 {
			s.emit(fresh...)
		}
	}
	return nil, nil
}

// permission decides a PermissionRequest. When the adapter says the CLI
// decides on its own (Codex's auto_review), the hook stays silent.
// Otherwise the request waits for a remote answer and is denied when the
// time runs out.
func (s *session) permission(ctx context.Context, p *agent.Hook) ([]byte, error) {
	s.readRollout()
	s.mu.Lock()
	tc := s.context
	exited := s.meta.Exited
	s.mu.Unlock()
	if exited {
		return agent.PermissionDecision(false, "remote-agent: session exited"), nil
	}
	if !s.ag.ForwardPermission(p, tc) {
		return nil, nil
	}

	timeout := s.d.approvalTimeout()
	a := &approval{id: newID(4), decision: make(chan decision, 1), deadline: time.Now().Add(timeout)}
	req := &pb.ApprovalRequested{
		ApprovalId:    a.id,
		ToolName:      p.ToolName,
		ToolInputJson: string(p.ToolInput),
		Deadline:      timestamppb.New(a.deadline),
	}
	var shell agent.ShellInput
	if json.Unmarshal(p.ToolInput, &shell) == nil {
		req.Command, req.Description = shell.Command, shell.Description
	}
	a.key, a.tool, a.input = promptKey(p.ToolInput), p.ToolName, callInput(p.ToolName, p.ToolInput)
	s.mu.Lock()
	if s.meta.Exited {
		s.mu.Unlock()
		return agent.PermissionDecision(false, "remote-agent: session exited"), nil
	}
	s.approvals[a.id] = a
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.approvals, a.id)
		s.mu.Unlock()
	}()
	s.emit(&pb.Event{TurnId: p.TurnID, Kind: &pb.Event_ApprovalRequested{ApprovalRequested: req}})

	resolved := func(d pb.ApprovalDecision, msg string) {
		s.emit(&pb.Event{TurnId: p.TurnID, Kind: &pb.Event_ApprovalResolved{ApprovalResolved: &pb.ApprovalResolved{
			ApprovalId: a.id, Decision: d, Message: msg,
		}}})
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var d decision
	select {
	case d = <-a.decision:
	case <-timer.C:
		if !s.claim(a) {
			d = <-a.decision // a reply won the race; honor it
			break
		}
		msg := fmt.Sprintf("remote-agent: no answer within %s, denied", timeout)
		resolved(pb.ApprovalDecision_APPROVAL_DECISION_TIMEOUT, msg)
		return agent.PermissionDecision(false, msg), nil
	case <-ctx.Done():
		if !s.claim(a) {
			d = <-a.decision
			break
		}
		resolved(pb.ApprovalDecision_APPROVAL_DECISION_CANCELED, "")
		return nil, ctx.Err()
	}
	if d.terminal {
		resolved(pb.ApprovalDecision_APPROVAL_DECISION_TERMINAL, "")
		return nil, nil
	}
	if d.allow {
		resolved(pb.ApprovalDecision_APPROVAL_DECISION_ALLOW, d.message)
	} else {
		resolved(pb.ApprovalDecision_APPROVAL_DECISION_DENY, d.message)
	}
	return agent.PermissionDecision(d.allow, denialFor(d)), nil
}

// denialFor is what the CLI is told about an answer. A bare deny reaches the
// model as "denied by a hook", which it tends to blame on its settings, so a
// denial says it was the user's and carries their reason.
func denialFor(d decision) string {
	if d.allow {
		return d.message
	}
	if d.message == "" {
		return "The user denied this request from remote-agent."
	}
	return "The user denied this request from remote-agent: " + d.message
}

// claim settles a for the caller unless someone else already did.
func (s *session) claim(a *approval) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.claimed {
		return false
	}
	a.claimed = true
	delete(s.approvals, a.id)
	return true
}

// resolve answers a pending approval. Success means the answer is the one
// the CLI receives.
func (s *session) resolve(id string, allow bool, message string) error {
	s.mu.Lock()
	a, ok := s.approvals[id]
	if !ok || a.claimed {
		s.mu.Unlock()
		return fmt.Errorf("approval %s: %w", id, errNotFound)
	}
	if !time.Now().Before(a.deadline) {
		s.mu.Unlock()
		return fmt.Errorf("approval %s: %w", id, errExpired)
	}
	a.claimed = true
	delete(s.approvals, id)
	s.mu.Unlock()
	a.decision <- decision{allow: allow, message: message}
	return nil
}

// watchApprovals follows the CLI's own permission prompts for requests a
// client is asked about. Claude Code shows its prompt while the hook waits
// (claude-code-notes §6); a prompt that showed and is gone was answered in
// the terminal. It returns those approvals, claimed, for the caller to
// settle once s.mu is released. Callers hold s.mu.
func (s *session) watchApprovals(scr agent.Screen, styled string) []*approval {
	if len(s.approvals) == 0 {
		return nil
	}
	plain := normalizeText(agent.StripANSI(styled))
	var out []*approval
	for id, a := range s.approvals {
		if a.claimed {
			continue
		}
		shown := scr.PermissionOpen && (a.key == "" || strings.Contains(plain, a.key))
		switch {
		case shown:
			a.seen = true
		case a.seen:
			a.claimed = true
			delete(s.approvals, id)
			out = append(out, a)
		}
	}
	return out
}

// retractApprovals settles every waiting approval as answered in the
// terminal.
func (s *session) retractApprovals() {
	s.mu.Lock()
	var out []*approval
	for id, a := range s.approvals {
		if !a.claimed {
			a.claimed = true
			out = append(out, a)
		}
		delete(s.approvals, id)
	}
	s.mu.Unlock()
	for _, a := range out {
		a.decision <- decision{terminal: true}
	}
}

// retractFor settles as answered in the terminal the waiting approval
// whose tool call has completed: the one request for the same tool with
// the same complete input. Several such requests, as parallel identical
// calls, cannot be told apart, and none is settled.
func (s *session) retractFor(c *pb.ToolCall) {
	s.mu.Lock()
	var match *approval
	n := 0
	for _, a := range s.approvals {
		if !a.claimed && a.tool != "" && a.tool == c.ItemType && a.input != "" && a.input == c.Input {
			match = a
			n++
		}
	}
	if n != 1 {
		s.mu.Unlock()
		return
	}
	match.claimed = true
	delete(s.approvals, match.id)
	s.mu.Unlock()
	match.decision <- decision{terminal: true}
}

// callInput is a tool call's input as its ToolCall event carries it: a
// shell command, or the input as compact JSON; empty when too long to
// compare.
func callInput(tool string, raw []byte) string {
	if tool == "Bash" {
		var in agent.ShellInput
		if json.Unmarshal(raw, &in) != nil {
			return ""
		}
		return in.Command
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	b, _ := json.Marshal(v)
	if len(b) > 16*1024 {
		return ""
	}
	return string(b)
}

// promptKey is text a permission prompt shows for a tool call: the start
// of its command, or the name of the file it changes.
func promptKey(input []byte) string {
	var in struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	key := in.Command
	if key == "" && in.FilePath != "" {
		key = filepath.Base(in.FilePath)
	}
	key = normalizeText(key)
	if r := []rune(key); len(r) > 24 {
		key = string(r[:24])
	}
	return key
}

// showsQuestion reports whether the screen shows one of the questions. The
// start of the text is enough and survives line wrapping.
func showsQuestion(styled string, questions []*pb.Question) bool {
	screen := normalizeText(agent.StripANSI(styled))
	for _, item := range questions {
		words := strings.Fields(item.Text)
		prefix := []rune(strings.Join(words, " "))
		if len(prefix) > 24 {
			prefix = prefix[:24]
		}
		if len(prefix) > 0 && strings.Contains(screen, string(prefix)) {
			return true
		}
	}
	return false
}

func newID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
