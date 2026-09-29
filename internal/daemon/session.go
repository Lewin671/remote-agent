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
	"strings"
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
	ctlMu  sync.Mutex // serializes use of the PTY host: inject, answer, interrupt
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
	queue       *queueState
	wake        chan struct{} // pokes the injector
	done        chan struct{} // closed when the session's goroutines stop
	stop        context.CancelFunc
}

type approval struct {
	id       string
	decision chan decision
	// claimed is set, under s.mu, by whoever settles the approval first:
	// a reply, the timeout, a canceled hook, or the session exiting.
	claimed bool
}

type decision struct {
	allow   bool
	message string
}

func newSession(d *Daemon, dir string, m meta, term terminal, log *eventlog.Log) *session {
	q, err := loadQueue(dir)
	if err != nil {
		slog.Warn("load input queue; starting empty", "session", m.ID, "err", err)
		q = &queueState{}
	}
	s := &session{
		d:         d,
		dir:       dir,
		term:      term,
		log:       log,
		parser:    codex.NewParser(),
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
		if q := e.GetQuestionAsked(); q != nil {
			s.parser.RememberQuestion(q.QuestionId)
		}
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
	}
	if s.context != nil {
		out.Model, out.Effort = s.context.Model, s.context.Effort
	}
	return out
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
				written, err := s.inject(next.Text)
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
	hist := newHistory(s.d.cfg.HistoryPath, *in.Inflight)
	deadline := time.Now().Add(s.d.cfg.SubmitTimeout)
	status := pb.InputStatus_INPUT_STATUS_UNCONFIRMED
	for time.Now().Before(deadline) {
		s.readRollout()
		s.mu.Lock()
		codexID := s.meta.CodexSessionID
		s.mu.Unlock()
		if hist.submitted(in.Text, codexID) || s.loggedUserMessage(in.InflightSeq, in.Text) {
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
func (s *session) markInflight(offset int64) error {
	seq := s.log.LastSeq()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue.Queue) == 0 {
		return errors.New("input queue is empty")
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
func (s *session) inject(text string) (bool, error) {
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
	scr := codex.AnalyzeScreen(styled)
	if !scr.ComposerReady || !scr.DraftEmpty {
		return false, nil
	}
	hist := newHistory(s.d.cfg.HistoryPath, -1)
	if err := s.markInflight(hist.r.Offset()); err != nil {
		return false, err
	}
	if err := c.Write(codex.PasteBytes(text)); err != nil {
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
	if !codex.AnalyzeScreen(styled).QuestionOpen || !showsQuestion(styled, q) {
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
	if s.meta.Exited {
		s.mu.Unlock()
		return codex.PermissionDecision(false, "remote-agent: session exited"), nil
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
		return codex.PermissionDecision(false, msg), nil
	case <-ctx.Done():
		if !s.claim(a) {
			d = <-a.decision
			break
		}
		resolved(pb.ApprovalDecision_APPROVAL_DECISION_CANCELED, "")
		return nil, ctx.Err()
	}
	if d.allow {
		resolved(pb.ApprovalDecision_APPROVAL_DECISION_ALLOW, d.message)
	} else {
		resolved(pb.ApprovalDecision_APPROVAL_DECISION_DENY, d.message)
	}
	return codex.PermissionDecision(d.allow, d.message), nil
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
// Codex receives.
func (s *session) resolve(id string, allow bool, message string) error {
	s.mu.Lock()
	a, ok := s.approvals[id]
	if !ok || a.claimed {
		s.mu.Unlock()
		return fmt.Errorf("approval %s: %w", id, errNotFound)
	}
	a.claimed = true
	delete(s.approvals, id)
	s.mu.Unlock()
	a.decision <- decision{allow: allow, message: message}
	return nil
}

// showsQuestion reports whether the screen shows one of q's questions. The
// start of the text is enough and survives line wrapping.
func showsQuestion(styled string, q *pb.QuestionAsked) bool {
	screen := normalizeText(codex.StripANSI(styled))
	for _, item := range q.Questions {
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
