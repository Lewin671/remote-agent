package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/tail"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Errors returned by session operations; the RPC layer maps them to codes.
var (
	errExited       = errors.New("session has exited")
	errNotFound     = errors.New("not found")
	errNotAnswering = errors.New("the question is not showing in the terminal")
)

// holdFor bounds how long local input is held while remote input is
// injected. The screen check and the write take a few milliseconds.
const holdFor = 3 * time.Second

// settle gives the CLI time to draw the keys that arrived just before the
// hold, so that the screen check sees them.
const settle = 80 * time.Millisecond

// session is one Codex session managed by the daemon.
type session struct {
	d    *Daemon
	dir  string
	term terminal
	log  *eventlog.Log

	readMu sync.Mutex // serializes rollout reads
	parser *codex.Parser
	reader *tail.Reader

	mu          sync.Mutex
	meta        meta
	running     bool // a turn is in progress
	context     *pb.TurnContext
	screen      codex.Screen
	prevScreen  codex.Screen
	sawComposer bool
	status      pb.SessionStatus
	detail      string
	approvals   map[string]*approval
	questions   map[string]*pb.QuestionAsked
	queue       []input
	seen        map[string]bool // request IDs already accepted
	wake        chan struct{}   // pokes the injector
	done        chan struct{}   // closed when the session's goroutines stop
	stop        context.CancelFunc
}

type input struct {
	text      string
	requestID string
}

type approval struct {
	id       string
	decision chan decision
}

type decision struct {
	allow   bool
	message string
}

func newSession(d *Daemon, dir string, m meta, term terminal, log *eventlog.Log) *session {
	s := &session{
		d:         d,
		dir:       dir,
		term:      term,
		log:       log,
		parser:    codex.NewParser(),
		meta:      m,
		approvals: map[string]*approval{},
		questions: map[string]*pb.QuestionAsked{},
		seen:      map[string]bool{},
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
		status:    pb.SessionStatus_SESSION_STATUS_STARTING,
	}
	// Rebuild derived state from the events already logged.
	log.Each(s.observe)
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
	for _, e := range evs {
		if e.Time == nil {
			e.Time = now
		}
	}
	if err := s.log.Append(s.meta.ID, evs, "", 0); err != nil {
		slog.Error("append events", "session", s.meta.ID, "err", err)
		return
	}
	s.mu.Lock()
	for _, e := range evs {
		s.observe(e)
	}
	s.mu.Unlock()
}

// start runs the session's background loops until it exits or ctx ends.
func (s *session) start(ctx context.Context) {
	ctx, s.stop = context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.pollLoop(ctx) }()
	go func() { defer wg.Done(); s.injectLoop(ctx) }()
	go func() { wg.Wait(); close(s.done) }()
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
			return
		}
		s.mu.Lock()
		for _, e := range evs {
			s.observe(e)
		}
		s.mu.Unlock()
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
	scr := codex.AnalyzeScreen(styled)
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
	status, detail := s.status, s.detail
	next, nextDetail := status, detail
	switch {
	case scr.Popup != "" && scr.Popup != "question":
		next, nextDetail = pb.SessionStatus_SESSION_STATUS_BLOCKED, scr.Popup
	case scr.ComposerReady || scr.QuestionOpen || s.sawComposer:
		next, nextDetail = pb.SessionStatus_SESSION_STATUS_IDLE, ""
	}
	s.mu.Unlock()
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
	pending := s.approvals
	s.approvals = map[string]*approval{}
	s.queue = nil
	s.mu.Unlock()
	for _, a := range pending {
		select {
		case a.decision <- decision{allow: false, message: "remote-agent: session exited"}:
		default:
		}
	}
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
		QueuedInputs:     int32(len(s.queue)),
	}
	if s.context != nil {
		out.Model, out.Effort = s.context.Model, s.context.Effort
	}
	return out
}

// send queues text for injection. It returns the queue position.
func (s *session) send(text, requestID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.meta.Exited {
		return 0, errExited
	}
	if requestID != "" {
		if s.seen[requestID] {
			return 0, nil
		}
		s.seen[requestID] = true
	}
	s.queue = append(s.queue, input{text: text, requestID: requestID})
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return len(s.queue), nil
}

// injectLoop submits queued input one message at a time, in order.
func (s *session) injectLoop(ctx context.Context) {
	hist := newHistory(s.d.cfg.HistoryPath)
	retry := time.NewTimer(time.Hour)
	defer retry.Stop()
	for {
		s.mu.Lock()
		var next *input
		if len(s.queue) > 0 && !s.meta.Exited {
			next = &s.queue[0]
		}
		s.mu.Unlock()
		if next != nil {
			ok, err := s.inject(ctx, hist, next.text)
			if err != nil {
				slog.Warn("inject", "session", s.meta.ID, "err", err)
			}
			if ok {
				s.mu.Lock()
				if len(s.queue) > 0 {
					s.queue = s.queue[1:]
				}
				s.mu.Unlock()
				continue
			}
			retry.Reset(s.d.cfg.RetryInterval)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-retry.C:
		}
	}
}

// inject tries once to submit text. It returns false, leaving the input
// queued, when the terminal is not ready: a popup is open or the local
// user has an unsent draft, which is never overwritten or merged.
func (s *session) inject(ctx context.Context, hist *history, text string) (bool, error) {
	c, err := s.term.Control()
	if err != nil {
		return false, err
	}
	defer c.Close()
	if err := c.Hold(holdFor); err != nil {
		return false, err
	}
	released := false
	defer func() {
		if !released {
			_ = c.Release()
		}
	}()
	time.Sleep(settle)
	styled, err := s.term.Capture()
	if err != nil {
		return false, err
	}
	scr := codex.AnalyzeScreen(styled)
	if !scr.ComposerReady || !scr.DraftEmpty {
		return false, nil
	}
	hist.skip()
	if err := c.Write(codex.PasteBytes(text)); err != nil {
		return false, err
	}
	// Local keys typed from now on reach Codex after the Enter, in the
	// fresh input box.
	_ = c.Release()
	released = true

	deadline := time.Now().Add(s.d.cfg.SubmitTimeout)
	for time.Now().Before(deadline) {
		if hist.submitted(text) {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	// The keys were delivered, but Codex did not record the message.
	// Sending again could submit it twice, so drop it.
	return true, fmt.Errorf("no submission of the injected message within %s", s.d.cfg.SubmitTimeout)
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
	c, err := s.term.Control()
	if err != nil {
		return false, err
	}
	defer c.Close()
	return true, c.Write([]byte{0x1b})
}

// answer picks option (1-based) of the question that is showing.
func (s *session) answer(questionID string, option int) error {
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
	if option < 1 || option > 9 {
		return fmt.Errorf("option must be between 1 and 9")
	}
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
	styled, err := s.term.Capture()
	if err != nil {
		return err
	}
	if !codex.AnalyzeScreen(styled).QuestionOpen {
		return errNotAnswering
	}
	return c.Write([]byte(strconv.Itoa(option)))
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

// bindRollout records where the Codex session writes its rollout.
func (s *session) bindRollout(codexSessionID, path string) {
	if path == "" {
		return
	}
	s.mu.Lock()
	if s.meta.RolloutPath == path {
		s.mu.Unlock()
		return
	}
	s.meta.RolloutPath = path
	if codexSessionID != "" {
		s.meta.CodexSessionID = codexSessionID
	}
	s.reader = tail.NewReader(path, 0)
	s.parser = codex.NewParser()
	m := s.meta
	s.mu.Unlock()
	if err := m.save(s.dir); err != nil {
		slog.Error("save session", "session", m.ID, "err", err)
	}
	s.readRollout()
}

// hook handles one Codex hook call and returns what the hook prints.
func (s *session) hook(ctx context.Context, p *codex.HookPayload) ([]byte, error) {
	switch p.HookEventName {
	case "SessionStart":
		s.bindRollout(p.SessionID, p.TranscriptPath)
		return nil, nil
	case "PermissionRequest":
		s.bindRollout(p.SessionID, p.TranscriptPath)
		return s.permission(ctx, p)
	}
	return nil, nil
}

// permission decides a PermissionRequest. With auto_review on, the hook
// stays silent and Codex's reviewer decides. Otherwise the request waits
// for a remote answer and is denied when the time runs out.
func (s *session) permission(ctx context.Context, p *codex.HookPayload) ([]byte, error) {
	s.readRollout()
	s.mu.Lock()
	reviewer := ""
	if s.context != nil {
		reviewer = s.context.ApprovalsReviewer
	}
	exited := s.meta.Exited
	s.mu.Unlock()
	if exited {
		return codex.PermissionDecision(false, "remote-agent: session exited"), nil
	}
	if reviewer == "auto_review" {
		return nil, nil
	}

	a := &approval{id: newID(4), decision: make(chan decision, 1)}
	timeout := s.d.cfg.ApprovalTimeout
	req := &pb.ApprovalRequested{
		ApprovalId:    a.id,
		ToolName:      p.ToolName,
		ToolInputJson: string(p.ToolInput),
		Deadline:      timestamppb.New(time.Now().Add(timeout)),
	}
	var shell codex.ShellInput
	if json.Unmarshal(p.ToolInput, &shell) == nil {
		req.Command, req.Description = shell.Command, shell.Description
	}
	s.mu.Lock()
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
	select {
	case d := <-a.decision:
		if d.allow {
			resolved(pb.ApprovalDecision_APPROVAL_DECISION_ALLOW, d.message)
		} else {
			resolved(pb.ApprovalDecision_APPROVAL_DECISION_DENY, d.message)
		}
		return codex.PermissionDecision(d.allow, d.message), nil
	case <-timer.C:
		msg := fmt.Sprintf("remote-agent: no answer within %s, denied", timeout)
		resolved(pb.ApprovalDecision_APPROVAL_DECISION_TIMEOUT, msg)
		return codex.PermissionDecision(false, msg), nil
	case <-ctx.Done():
		resolved(pb.ApprovalDecision_APPROVAL_DECISION_CANCELED, "")
		return nil, ctx.Err()
	}
}

// resolve answers a pending approval.
func (s *session) resolve(id string, allow bool, message string) error {
	s.mu.Lock()
	a, ok := s.approvals[id]
	if ok {
		delete(s.approvals, id)
	}
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("approval %s: %w", id, errNotFound)
	}
	a.decision <- decision{allow: allow, message: message}
	return nil
}

func newID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
