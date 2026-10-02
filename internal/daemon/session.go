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
	"regexp"
	"strings"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/claude"
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
	// images are the session's images and their display copies.
	images *imageStore

	readMu sync.Mutex // serializes rollout reads
	ctlMu  sync.Mutex // serializes use of the PTY host: inject, answer, interrupt
	parser agent.Parser
	reader *tail.Reader

	mu      sync.Mutex
	meta    meta
	running bool // a turn is in progress
	// turnEnds counts the turns that ended; aborted is set when the last
	// one was interrupted, until the waiting input is paused.
	turnEnds uint64
	aborted  bool
	// A queue message waits until turnEnds reaches gate: the turn in
	// which the last message was written has ended (sendmode.go).
	gate        uint64
	gateAt      time.Time
	context     *pb.TurnContext
	usage       *pb.TokenUsage
	title       string
	preview     string
	ordering    sessionOrdering
	screen      agent.Screen
	prevScreen  agent.Screen
	sawComposer bool
	// readySince is when the session last left STARTING.
	readySince time.Time
	planOpen   bool
	driving    bool // a control operation is using the terminal's menus
	// seenModes are the permission modes the screen has shown, by the
	// model family in use then: which modes are on offer depends on it.
	seenModes map[string]map[string]bool
	// Why queued input is not being written, and since when.
	waitReason string
	waitSince  time.Time
	warned     bool
	status     pb.SessionStatus
	detail     string
	approvals  map[string]*approval
	questions  map[string]*pb.QuestionAsked
	// answeredBy and interruptedBy remember who answered a question or
	// interrupted the turn through remote-agent, for the transcript
	// record that follows.
	answeredBy    map[string]*pb.Origin
	interruptedBy *pb.Origin
	queue         *queueState
	// recent are messages written lately, for matching late records.
	recent []input
	wake   chan struct{} // pokes the injector
	done   chan struct{} // closed when the session's goroutines stop
	stop   context.CancelFunc
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
	// tool and digest (agent.InputDigest) identify the tool call the
	// request is for.
	tool, digest string
}

type decision struct {
	allow   bool
	message string
	// terminal: the request was answered in the terminal; the hook's
	// output no longer matters.
	terminal bool
	// origin is who answered through remote-agent.
	origin *pb.Origin
}

func newSession(d *Daemon, dir string, m meta, term terminal, log *eventlog.Log) *session {
	q, err := loadQueue(dir)
	if err != nil {
		slog.Warn("load input queue; starting empty", "session", m.ID, "err", errClass(err))
		q = &queueState{}
	}
	ag := d.adapter(m.Agent)
	s := &session{
		d:          d,
		ag:         ag,
		dir:        dir,
		term:       term,
		log:        log,
		parser:     ag.NewParser(),
		meta:       m,
		approvals:  map[string]*approval{},
		questions:  map[string]*pb.QuestionAsked{},
		answeredBy: map[string]*pb.Origin{},
		wake:       make(chan struct{}, 1),
		done:       make(chan struct{}),
		status:     pb.SessionStatus_SESSION_STATUS_STARTING,
		queue:      q,
		images:     openImageStore(dir),
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
	s.ordering.observe(e)
	switch k := e.Kind.(type) {
	case *pb.Event_TurnStarted:
		s.running = true
	case *pb.Event_TurnEnded:
		s.running = false
		s.turnEnds++
		if k.TurnEnded.Outcome == pb.TurnOutcome_TURN_OUTCOME_ABORTED {
			s.aborted = true
		}
	case *pb.Event_TurnContext:
		s.context = k.TurnContext
	case *pb.Event_TokenUsage:
		s.usage = k.TokenUsage
		at := time.Now()
		if e.Time != nil {
			at = e.Time.AsTime()
		}
		s.d.noteLimits(k.TokenUsage, at)
	case *pb.Event_SettingsChanged:
		s.context = s.ag.ApplySettings(s.context, k.SettingsChanged)
	case *pb.Event_UserMessage:
		if s.title == "" {
			s.title = titleFrom(k.UserMessage.Text)
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
		if s.status == pb.SessionStatus_SESSION_STATUS_STARTING && k.SessionState.Status != pb.SessionStatus_SESSION_STATUS_STARTING {
			s.readySince = time.Now()
		}
		s.status, s.detail = k.SessionState.Status, k.SessionState.Detail
		s.planOpen = k.SessionState.PlanOpen
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
	s.emitLocked(settings, evs...)
}

// emitLocked logs and applies events; with settings among them, the
// caller holds readMu.
func (s *session) emitLocked(settings bool, evs ...*pb.Event) {
	now := timestamppb.Now()
	for _, e := range evs {
		if e.Time == nil {
			e.Time = now
		}
	}
	if err := s.log.Append(s.meta.ID, evs, "", 0); err != nil {
		slog.Error("append events", "session", s.meta.ID, "err", errClass(err))
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

// modeFacts tells the adapter what is known of the modes on offer.
func (s *session) modeFacts() agent.ModeFacts {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := agent.ModeFacts{FullAccess: !s.meta.NoFullAccess}
	if s.context != nil {
		f.Model = s.context.Model
	}
	for m := range s.seenModes[agent.ModelFamily(f.Model)] {
		f.Seen = append(f.Seen, m)
	}
	return f
}

// noteScreenMode records a permission mode seen on the screen as the
// session's context. The context is copied, changed and logged under the
// lock settings events are logged under, so that it cannot carry a model
// from before a switch recorded meanwhile.
func (s *session) noteScreenMode(mode string) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	s.mu.Lock()
	if s.context != nil && s.context.PermissionMode == mode {
		s.mu.Unlock()
		return
	}
	c := &pb.TurnContext{}
	if s.context != nil {
		c = proto.CloneOf(s.context)
	}
	c.PermissionMode = mode
	s.mu.Unlock()
	s.emitLocked(true, &pb.Event{Kind: &pb.Event_TurnContext{TurnContext: c}})
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
		slog.Warn("read rollout", "session", s.meta.ID, "err", errClass(err))
		return
	}
	if truncated {
		slog.Warn("rollout shrank; reading it again from the start", "session", s.meta.ID)
	}
	for _, l := range lines {
		evs, err := s.parser.Parse(l.Data)
		if err != nil {
			slog.Warn("parse rollout line", "session", s.meta.ID, "offset", l.End, "err", errClass(err))
		}
		found := s.parser.Images()
		for _, f := range found {
			s.images.process(f, r.Path)
			if f.Ref.ImageId != "" {
				s.d.uploads.touchPath(f.Ref.Path)
			}
		}
		if len(found) > 0 {
			_, _ = s.d.trimImageCache(defaultImageCacheQuota)
		}
		consume := s.annotate(evs)
		if err := s.log.Append(s.meta.ID, evs, r.Path, l.End); err != nil {
			slog.Error("append events", "session", s.meta.ID, "err", errClass(err))
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
		// Only now that the events are logged are the records they were
		// matched to used up; a failed write reads them again.
		consume()
		for _, e := range evs {
			s.observe(e)
		}
		s.mu.Unlock()
		for _, e := range evs {
			if m := e.GetUserMessage(); m != nil {
				s.received(m.RequestId)
				for _, ref := range m.Images {
					if ref.UploadId != "" && ref.ImageId != "" {
						s.d.uploads.embedded(ref.UploadId)
					}
				}
			}
			if e.GetTurnEnded() != nil {
				// Claude Code: a request still waiting was answered in
				// the terminal (or interrupted there) since the turn is
				// over. Codex (0.159.2) may end a turn while an escalated
				// command still waits for approval in the background; its
				// hook is still waiting too, and settles the request.
				if s.ag.Kind() == pb.Agent_AGENT_CLAUDE {
					s.retractApprovals()
				}
				s.afterTurn()
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
	planChanged := s.planOpen != scr.PlanOpen
	s.planOpen = scr.PlanOpen
	retract := s.watchApprovals(scr, styled)
	if scr.Mode != "" {
		family := ""
		if s.context != nil {
			family = agent.ModelFamily(s.context.Model)
		}
		if s.seenModes == nil {
			s.seenModes = map[string]map[string]bool{}
		}
		if s.seenModes[family] == nil {
			s.seenModes[family] = map[string]bool{}
		}
		s.seenModes[family][scr.Mode] = true
	}
	modeSeen := ""
	if scr.Mode != "" && (s.context == nil || s.context.PermissionMode != scr.Mode) {
		// Switched in the terminal; the transcript records it only with
		// the next message.
		modeSeen = scr.Mode
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
	if modeSeen != "" {
		s.noteScreenMode(modeSeen)
	}
	// RUNNING is derived from turn events; the state event only records
	// startup, popups and exit.
	if next != status || nextDetail != detail || planChanged {
		s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{Status: next, Detail: nextDetail, PlanOpen: scr.PlanOpen}}})
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
	// Messages still queued are not sent after a resume; each is
	// settled here, never dropped without a word.
	left := s.queue.Queue
	s.queue.Queue = nil
	if err := s.queue.save(s.dir); err != nil {
		slog.Error("save input queue", "session", m.ID, "err", errClass(err))
	}
	s.mu.Unlock()
	if err := m.save(s.dir); err != nil {
		slog.Error("save session", "session", m.ID, "err", errClass(err))
	}
	if len(left) > 0 {
		s.readRollout()
	}
	for _, in := range left {
		switch {
		case in.Inflight == nil:
			s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{
				RequestId: in.RequestID, Status: pb.InputStatus_INPUT_STATUS_CANCELED, Reason: "exited",
			}}})
		case s.loggedUserMessage(in):
			s.emitInput(in.RequestID, pb.InputStatus_INPUT_STATUS_SUBMITTED)
		default:
			// Written, maybe submitted: as when confirming times out.
			s.emitInput(in.RequestID, pb.InputStatus_INPUT_STATUS_UNCONFIRMED)
		}
	}
	s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{
		Status: pb.SessionStatus_SESSION_STATUS_EXITED, Detail: detail,
	}}})
	// Images sent for the session are kept while it runs: Codex reads
	// them when a message is submitted, and the model may look at a path
	// again later.
	s.d.uploads.dropSession(m.ID)
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
		if s.meta.StandardContext && out.ContextWindow > claude.StandardWindow {
			out.ContextWindow = claude.StandardWindow
		}
	}
	out.Title, out.Preview = s.title, s.preview
	out.AttentionSince = s.ordering.attentionSince(out)
	out.EndedAt = s.ordering.endedAt
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

// titleFrom is a session title from its first message: the first
// clause of the first line, at most titleMax columns wide, where a CJK
// character takes two columns. A clause ends
// at CJK punctuation, or at ASCII punctuation followed by a space, so
// "v0.8.0" and "main.go" stay whole; a break in the first titleMin
// characters is skipped so that "嗯，" alone is not a title. The CLI's
// "[Image #N]" placeholders before the words are not part of it, so a
// message of images alone gives no title and the next message names
// the session.
func titleFrom(text string) string {
	r := []rune(summary(leadingImagesRE.ReplaceAllString(text, ""), 1<<20))
	for i := titleMin; i < len(r); i++ {
		if strings.ContainsRune("，。；：！？、", r[i]) ||
			strings.ContainsRune(",.;:!?", r[i]) && i+1 < len(r) && r[i+1] == ' ' {
			r = r[:i]
			break
		}
	}
	w := 0
	for i, c := range r {
		if w++; c >= 0x2e80 {
			w++
		}
		if w > titleMax {
			return string(r[:i]) + "…"
		}
	}
	return string(r)
}

var leadingImagesRE = regexp.MustCompile(`^(?:\s*\[Image #\d+\])+\s*`)

const (
	titleMin = 4
	titleMax = 40
)

// cancelInput removes a queued message unless it is already being
// written.
func (s *session) cancelInput(requestID string) error {
	return s.cancelInputBy(requestID, originLocal)
}

// cancelInputBy is cancelInput on behalf of origin.
func (s *session) cancelInputBy(requestID string, origin *pb.Origin) error {
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
		// It may be held by the CLI, still short of the model.
		return s.cancelHeld(requestID, origin)
	}
	if in := s.queue.Queue[idx]; in.Inflight != nil || in.Stage > pb.InputStage_INPUT_STAGE_WAITING {
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
	s.d.uploads.releaseRefs(prev[idx].Images, s.meta.ID, requestID)
	s.emit(withOrigin(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{
		RequestId: requestID, Status: pb.InputStatus_INPUT_STATUS_CANCELED,
	}}}, origin))
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

// interrupt presses Esc if a turn is running.
func (s *session) interrupt() (bool, error) { return s.interruptBy(originLocal) }

// interruptBy is interrupt on behalf of origin.
func (s *session) interruptBy(origin *pb.Origin) (bool, error) {
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
	// Known before the key goes out: the transcript may record the end of
	// the turn before the write returns.
	s.mu.Lock()
	prev := s.interruptedBy
	s.interruptedBy = origin
	s.mu.Unlock()
	if err := c.Write(s.ag.Interrupt()); err != nil {
		s.mu.Lock()
		if s.interruptedBy == origin {
			s.interruptedBy = prev
		}
		s.mu.Unlock()
		return true, err
	}
	return true, nil
}

// answer picks options (1-based) of the question that is showing. With
// index set, that must be the index-th question of the call.
func (s *session) answer(questionID string, options []int32, index *int32) error {
	return s.answerBy(questionID, options, index, originLocal)
}

// answerBy is answer on behalf of origin.
func (s *session) answerBy(questionID string, options []int32, index *int32, origin *pb.Origin) error {
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
	// Known before the keys go out: the transcript may record the answer
	// before Answer returns.
	s.mu.Lock()
	prev, had := s.answeredBy[questionID]
	s.answeredBy[questionID] = origin
	s.mu.Unlock()
	if err := s.ag.Answer(heldTerm{s.term, c}, q, index, options); err != nil {
		s.mu.Lock()
		if s.answeredBy[questionID] == origin {
			if had {
				s.answeredBy[questionID] = prev
			} else {
				delete(s.answeredBy, questionID)
			}
		}
		s.mu.Unlock()
		return err
	}
	return nil
}

// annotate records who caused parsed events, as far as the daemon knows:
// the question it answered or the turn it interrupted for a client, the
// message it wrote. It returns what uses the matched records up, to be
// run, with s.mu held, once the events are logged.
func (s *session) annotate(evs []*pb.Event) (consume func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var done []func()
	for _, e := range evs {
		switch k := e.Kind.(type) {
		case *pb.Event_QuestionAnswered:
			id := k.QuestionAnswered.QuestionId
			o := s.answeredBy[id]
			if o == nil {
				o = originLocal
			}
			withOrigin(e, o)
			done = append(done, func() { delete(s.answeredBy, id) })
		case *pb.Event_TurnEnded:
			if k.TurnEnded.Outcome == pb.TurnOutcome_TURN_OUTCOME_ABORTED && s.interruptedBy != nil {
				withOrigin(e, s.interruptedBy)
			}
			done = append(done, func() {
				s.interruptedBy = nil
				clear(s.answeredBy)
			})
		case *pb.Event_UserMessage:
			if f := s.annotateUserMessage(e, k.UserMessage); f != nil {
				done = append(done, f)
			}
		}
	}
	return func() {
		for _, f := range done {
			f()
		}
	}
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
		slog.Error("save session", "session", m.ID, "err", errClass(err))
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
	case "StatusLine":
		s.d.statusLine(s.meta.ID, p.Status, time.Now())
		return nil, nil
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
	if s.ag.Kind() == pb.Agent_AGENT_CLAUDE {
		// The digest the daemon matches the call with, so clients need
		// not compute it themselves.
		req.InputDigest = agent.InputDigest(p.ToolName, p.ToolInput)
	}
	var shell agent.ShellInput
	if json.Unmarshal(p.ToolInput, &shell) == nil {
		req.Command, req.Description = shell.Command, shell.Description
	}
	a.key, a.tool, a.digest = promptKey(p.ToolInput), p.ToolName, agent.InputDigest(p.ToolName, p.ToolInput)
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

	resolved := func(d pb.ApprovalDecision, msg string, o *pb.Origin) {
		s.emit(withOrigin(&pb.Event{TurnId: p.TurnID, Kind: &pb.Event_ApprovalResolved{ApprovalResolved: &pb.ApprovalResolved{
			ApprovalId: a.id, Decision: d, Message: msg,
		}}}, o))
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
		resolved(pb.ApprovalDecision_APPROVAL_DECISION_TIMEOUT, msg, originSystem)
		return agent.PermissionDecision(false, msg), nil
	case <-ctx.Done():
		if !s.claim(a) {
			d = <-a.decision
			break
		}
		resolved(pb.ApprovalDecision_APPROVAL_DECISION_CANCELED, "", originSystem)
		return nil, ctx.Err()
	}
	if d.terminal {
		resolved(pb.ApprovalDecision_APPROVAL_DECISION_TERMINAL, "", originLocal)
		return nil, nil
	}
	if d.allow {
		resolved(pb.ApprovalDecision_APPROVAL_DECISION_ALLOW, d.message, d.origin)
	} else {
		resolved(pb.ApprovalDecision_APPROVAL_DECISION_DENY, d.message, d.origin)
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
	return s.resolveBy(id, allow, false, message, originLocal)
}

// resolveBy is resolve on behalf of origin.
func (s *session) resolveBy(id string, allow, always bool, message string, origin *pb.Origin) error {
	// Native permission prompts expose no stable tool request ID. Screen
	// text (especially command prefixes or basenames) cannot prove which
	// request would receive a permanent grant. Leave it pending for the
	// user to decide in the native terminal; never silently grant once.
	if allow && always {
		return fmt.Errorf("permanent approvals must be answered in the native terminal: %w", agent.ErrUnsupported)
	}
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
	a.decision <- decision{allow: allow, message: message, origin: origin}
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

// retractFor settles as answered in the terminal a waiting approval whose
// tool call has completed: a request for the same tool with the same
// complete input, compared by digest however long it is. Identical
// requests are the same question, so one completion settles one of them,
// the oldest, as Claude Code shows its prompts in turn. A call that
// completed before its request was registered is not looked for: that
// card stays until the turn ends, and answering it does nothing
// (architecture §16).
func (s *session) retractFor(c *pb.ToolCall) {
	if c.InputDigest == "" {
		return
	}
	s.mu.Lock()
	var match *approval
	for _, a := range s.approvals {
		if !a.claimed && a.tool == c.ItemType && a.digest == c.InputDigest && (match == nil || a.deadline.Before(match.deadline)) {
			match = a
		}
	}
	if match == nil {
		s.mu.Unlock()
		return
	}
	match.claimed = true
	delete(s.approvals, match.id)
	s.mu.Unlock()
	match.decision <- decision{terminal: true}
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
