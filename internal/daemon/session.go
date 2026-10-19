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
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
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
	errNotAnswering = agent.ErrNotAnswering
	errHeadChanged  = errors.New("the queue head changed")
	errWriting      = errors.New("the message is being written to the terminal and can no longer be canceled")
	// errQueueUnreadable: see session.queueLost.
	errQueueUnreadable = errors.New("this session's saved input queue cannot be read; it accepts no messages until the file is repaired or removed and the daemon restarted")
)

// holdFor bounds how long local input is held while remote input is
// injected. The screen check and the write take a few milliseconds.
const holdFor = 3 * time.Second

// settle gives the CLI time to draw the keys that arrived just before the
// hold, so that the screen check sees them.
const settle = 80 * time.Millisecond

const (
	// pty-host allows the CLI 5s after SIGHUP/SIGTERM and its process group
	// another 2s after SIGTERM. The extra second allows for scheduling and
	// socket teardown without approaching the 60s remote-command deadline.
	ptyHostExitTimeout  = 8 * time.Second
	ptyHostPollInterval = 50 * time.Millisecond
)

// session is one CLI session managed by the daemon.
type session struct {
	d    *Daemon
	ag   agent.Adapter
	dir  string
	term terminal
	log  *eventlog.Log
	// images are the session's images and their display copies.
	images *imageStore

	lifecycleMu sync.Mutex // serializes stop/resume/fork/terminal attachment for this session
	readMu      sync.Mutex // serializes rollout reads
	ctlMu       sync.Mutex // serializes use of the PTY host: inject, answer, interrupt
	parser      agent.Parser
	reader      *tail.Reader
	// parserReplayPending keeps a failed rollout append from reusing the
	// parser that already consumed its line. It is rebuilt from the event
	// log before that line is tried again.
	parserReplayPending bool
	parserReplayPath    string
	parserReplayOffset  int64
	// copied tells the lines of a fork's transcript that repeat its
	// source's records, which the fork has as events already; nil if none.
	copied func(line []byte) bool

	mu                  sync.Mutex
	meta                meta
	running             bool   // a turn is in progress
	turnID              string // the turn that still needs an outcome
	turnPrompt          string // first user message, for a CLI-restored prompt
	turnOutput          bool   // the turn has produced assistant or tool output
	restoredPromptSince time.Time
	// turnEnds counts the turns that ended; aborted is set when the last
	// one was interrupted, until the waiting input is paused.
	turnEnds uint64
	aborted  bool
	// A queue message waits until turnEnds reaches gate: the turn in
	// which the last message was written has ended (sendmode.go).
	gate         uint64
	gateAt       time.Time
	context      *pb.TurnContext
	usage        *pb.TokenUsage
	title        string
	preview      string
	ordering     sessionOrdering
	answerSafety pendingAnswerSafety
	screen       agent.Screen
	prevScreen   agent.Screen
	sawComposer  bool
	// readySince is when the session last left STARTING.
	readySince time.Time
	// shellStart is when this daemon started the session through the
	// user's login shell, while the shell has not yet reported the CLI it
	// started; zero once it has, and for every other session.
	shellStart time.Time
	planOpen   bool
	driving    bool // a control operation is using the terminal's menus
	// seenModes are the permission modes the screen has shown, by the
	// model family in use then: which modes are on offer depends on it.
	seenModes map[string]map[string]bool
	// Why queued input is not being written, and since when.
	waitReason    string
	waitSince     time.Time
	warned        bool
	status        pb.SessionStatus
	detail        string
	storageFault  string
	storageWarnAt time.Time
	parseHealth   parseHealth
	parseWarnAt   time.Time
	// Retain a transcript reset until its diagnostic watermark invalidation
	// is durable, even if the file grows again while storage is unavailable.
	parseHealthResetPending bool
	approvals               map[string]*approval
	staleApprovals          []string
	questions               map[string]*pb.QuestionAsked
	// answeredBy and interruptedBy remember who answered a question or
	// interrupted the turn through remote-agent, for the transcript
	// record that follows.
	answeredBy    map[string]*pb.Origin
	interruptedBy *pb.Origin
	queue         *queueState
	// queueLost: queue.json was there but could not be read. It may hold
	// accepted messages and the request IDs that keep a retry from being
	// sent twice, so the session takes no input: with the queue empty,
	// nothing then saves over the file.
	queueLost bool
	// recent are messages written lately, for matching late records.
	recent []input
	// unproven are the remembered messages that were reported
	// UNCONFIRMED: a record of one, coming late, still confirms it.
	unproven map[string]bool
	wake     chan struct{} // pokes the injector
	done     chan struct{} // closed when the session's goroutines stop
	stop     context.CancelFunc
}

type approval struct {
	turnID   string
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
	origin   *pb.Origin
	recorded bool // the remote reply was committed before claiming the approval
}

func newSession(d *Daemon, dir string, m meta, term terminal, log *eventlog.Log) (*session, error) {
	q, err := loadQueue(dir)
	if err != nil {
		slog.Error("load input queue; the session accepts no input until it can be read", "session", m.ID, "err", errClass(err))
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
		unproven:   map[string]bool{},
		wake:       make(chan struct{}, 1),
		done:       make(chan struct{}),
		status:     pb.SessionStatus_SESSION_STATUS_STARTING,
		queue:      q,
		queueLost:  err != nil,
		images:     openImageStore(dir),
	}
	if h, err := loadParseHealth(dir); err != nil {
		// Diagnostics are not recovery state. A damaged or unreadable file
		// must not hide a live CLI whose metadata, events and queue are valid.
		// Keep the zero value; the next diagnostic save replaces a bad file.
		slog.Warn("cannot load parse diagnostics; starting with empty diagnostics", "session", m.ID, "category", parseErrorCategory(err))
	} else {
		s.parseHealth = h
	}
	if m.ForkedFrom != "" {
		s.copied = ag.Copied(dir)
	}
	// Rebuild derived state from the events already logged.
	recentIDs, err := recentWrittenIDs(log)
	if err != nil {
		return nil, fmt.Errorf("find recent submitted inputs: %w", err)
	}
	seen := newLogged(q, recentIDs)
	if err := log.Each(func(e *pb.Event) {
		s.observe(e)
		s.parser.Replay(e)
		seen.note(e)
	}); err != nil {
		return nil, fmt.Errorf("restore session events: %w", err)
	}
	// Clear the previous runtime snapshot before recovery can encounter a new
	// commit failure of its own. Persisted parse diagnostics are kept.
	s.clearStorageDiagnostic()
	s.staleApprovals = slices.Clone(seen.approvalOrder)
	s.dropSettled(seen)
	s.restorePause(seen)
	s.restoreReceived(seen)
	s.restoreRecent(seen)
	if m.Exited {
		s.status = pb.SessionStatus_SESSION_STATUS_EXITED
	}
	if m.RolloutPath != "" {
		path, off := log.Checkpoint()
		if path != m.RolloutPath {
			off = 0
		}
		off = s.rolloutOffset(m.RolloutPath, off)
		if off > 0 {
			if err := s.parser.Restore(m.RolloutPath, off); err != nil {
				slog.Warn("restore the parser from the rollout", "session", m.ID, "err", errClass(err))
			}
		}
		s.reader = tail.NewReader(m.RolloutPath, off)
	}
	return s, nil
}

// observe updates derived state from an event. Callers hold s.mu or are
// the only goroutine touching the session.
func (s *session) observe(e *pb.Event) {
	s.ordering.observe(e)
	s.answerSafety.observe(e)
	switch k := e.Kind.(type) {
	case *pb.Event_TurnStarted:
		s.running = true
		s.turnID = e.TurnId
		s.turnPrompt, s.turnOutput = "", false
		s.restoredPromptSince = time.Time{}
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
		if s.running && e.TurnId == s.turnID && s.turnPrompt == "" {
			s.turnPrompt = k.UserMessage.Text
		}
		if s.title == "" {
			s.title = titleFrom(k.UserMessage.Text)
		}
	case *pb.Event_AgentMessage:
		s.turnOutput = true
		s.preview = summary(k.AgentMessage.Text, 120)
	case *pb.Event_ToolCall:
		s.turnOutput = true
		s.preview = "✓ " + summary(k.ToolCall.Title, 110)
	case *pb.Event_ToolStarted:
		s.turnOutput = true
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

// emit appends daemon-originated events. Background observers may discard
// its error: appendLocked still exposes the session storage fault and logs
// it with a rate limit; no event projection advances on failure.
func (s *session) emit(evs ...*pb.Event) error {
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
	return s.emitLocked(settings, evs...)
}

// emitLocked logs and applies events; with settings among them, the
// caller holds readMu.
func (s *session) emitLocked(settings bool, evs ...*pb.Event) error {
	now := timestamppb.Now()
	for _, e := range evs {
		if e.Time == nil {
			e.Time = now
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.appendLocked(evs, "", 0); err != nil {
		return err
	}
	for _, e := range evs {
		if settings && (e.GetSettingsChanged() != nil || e.GetTurnContext() != nil) {
			s.parser.Replay(e)
		}
	}
	return nil
}

// modeFacts tells the adapter what is known of the modes on offer.
func (s *session) modeFacts() agent.ModeFacts {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := agent.ModeFacts{FullAccess: !s.meta.NoFullAccess}
	if s.context != nil {
		f.Model = s.context.Model
	}
	for m := range s.seenModes[f.Model] {
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
	_ = s.emitLocked(true, &pb.Event{Kind: &pb.Event_TurnContext{TurnContext: c}})
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
	if !s.checkShellStart(ctx) {
		s.readRollout()
		return
	}
	if !s.checkScreenContext(ctx) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-rollout.C:
			s.readRollout()
		case <-screen.C:
			if !s.checkShellStart(ctx) {
				s.readRollout()
				return
			}
			if !s.checkScreenContext(ctx) {
				s.readRollout()
				return
			}
		}
	}
}

// noteShellLaunch arms the startup watchdog of a login-shell launch: the
// shell must report the CLI it started within cfg.ShellStartTimeout, or
// the session is ended with the reason. Without it a shell whose
// configuration waits for input would leave the session starting forever.
func (s *session) noteShellLaunch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.meta.Shell {
		s.shellStart = time.Now()
	}
}

// checkShellStart reads what a login-shell launch reported and learns the
// CLI it started. It ends the session when the shell has not got that far
// within the timeout, and reports false then.
func (s *session) checkShellStart(ctx context.Context) bool {
	s.mu.Lock()
	started, starting := s.shellStart, s.status == pb.SessionStatus_SESSION_STATUS_STARTING && !s.meta.Exited
	s.mu.Unlock()
	if started.IsZero() {
		return true
	}
	if bin, _, ok := shellReport(s.dir); ok {
		s.mu.Lock()
		s.shellStart = time.Time{}
		s.mu.Unlock()
		if bin != "" {
			// The CLI the sessions actually run, for version checks and
			// the installed list.
			s.d.learnCLI(s.ag.Kind(), bin)
		}
		return true
	}
	if !starting {
		// The screen shows the CLI, so the shell got there; its report
		// just never arrived. Nothing left to watch.
		s.mu.Lock()
		s.shellStart = time.Time{}
		s.mu.Unlock()
		return true
	}
	// Past the timeout the shell has neither started the CLI nor failed
	// to find it: its configuration is stuck (or the shell never ran the
	// script).
	if time.Since(started) < s.d.cfg.ShellStartTimeout {
		return true
	}
	detail := fmt.Sprintf("the login shell did not start the CLI within %s", s.d.cfg.ShellStartTimeout)
	slog.Warn("ending a session whose login shell never started the CLI",
		"session", s.meta.ID, "agent", s.ag.Name(), "timeout", s.d.cfg.ShellStartTimeout)
	if err := s.term.Kill(); err != nil && s.term.Alive() {
		slog.Warn("cannot end a session whose login shell is stuck", "session", s.meta.ID, "err", errClass(err))
		return true // try again on the next poll
	}
	if err := s.waitForPTYHostGone(ctx); err != nil {
		slog.Warn("pty-host still running after ending a stuck session", "session", s.meta.ID, "err", errClass(err))
	}
	_ = s.markExited(detail)
	return false
}

// readRollout appends events for rollout lines written since the last read.
func (s *session) readRollout() bool {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	return s.readRolloutLocked()
}

// readRolloutLocked reports whether all available records were persisted.
// Callers hold readMu, including when settling a turn after the final read.
func (s *session) readRolloutLocked() bool {
	s.cancelStaleApprovals()
	// A backlog comes in batches of bounded size (tail.MaxBatch).
	for {
		more, ok := s.readBatch()
		if !more || !ok {
			return ok
		}
	}
}

// readBatch appends events for the next rollout lines and reports whether
// there may be more. Callers hold readMu.
func (s *session) readBatch() (more, ok bool) {
	s.mu.Lock()
	r := s.reader
	pending, replayPath, replayOffset := s.parserReplayPending, s.parserReplayPath, s.parserReplayOffset
	resetHealth := s.parseHealthResetPending
	if pending {
		replayOffset = s.rolloutOffset(replayPath, replayOffset)
	}
	s.mu.Unlock()
	if pending {
		p := s.ag.NewParser()
		if err := s.log.Each(p.Replay); err != nil {
			slog.Error("replay session events after append failure", "session", s.meta.ID, "err", errClass(err))
			return false, false
		}
		if replayOffset > 0 {
			if err := p.Restore(replayPath, replayOffset); err != nil {
				slog.Warn("restore parser after append failure", "session", s.meta.ID, "err", errClass(err))
				return false, false
			}
		}
		s.mu.Lock()
		s.parser = p
		s.reader = tail.NewReader(replayPath, replayOffset)
		s.parserReplayPending = false
		s.parserReplayPath, s.parserReplayOffset = "", 0
		r = s.reader
		s.mu.Unlock()
	}
	if r == nil {
		return false, true
	}
	if resetHealth {
		if err := s.resetParseWatermark(r.Path); err != nil {
			return false, false
		}
	}
	lines, truncated, err := r.Next()
	if truncated {
		// The same pathname now names a new byte sequence. Invalidate the
		// diagnostic watermark before comparing any of its positions.
		if resetErr := s.resetParseWatermark(r.Path); resetErr != nil {
			s.mu.Lock()
			s.reader = tail.NewReader(r.Path, 0)
			s.mu.Unlock()
			return false, false
		}
		slog.Warn("rollout reset; reading it again from the start", "session", s.meta.ID)
	}
	if err != nil {
		slog.Warn("read rollout", "session", s.meta.ID, "err", errClass(err))
		return false, false
	}
	for _, l := range lines {
		var evs []*pb.Event
		err = l.Err
		if err != nil {
			// The tail reader could not compact a complete oversized line.
		} else if s.copied != nil && s.copied(l.Data) {
			evs = nil
		} else {
			evs, err = s.parser.Parse(l.Data)
		}
		if err != nil {
			// Parser errors are nonmutating. Keep pending state belonging to
			// earlier valid records (such as images not emitted yet), rather
			// than replacing it with an event-log projection that lacks it.
			if diagnosticErr := s.noteParseFailure(r.Path, l.Start, l.End, l.Data, err); diagnosticErr != nil {
				s.mu.Lock()
				s.reader = tail.NewReader(r.Path, l.Start)
				s.mu.Unlock()
				return false, false
			}
			evs = nil
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
		evs = s.confirmLate(evs)
		s.mu.Lock()
		appendErr := s.appendLocked(evs, r.Path, l.End)
		if appendErr == nil {
			// Annotation is consumed only after commit, under the same lock
			// as the projection. A failed append will match this line again.
			consume()
			for _, e := range evs {
				if st := e.GetInputState(); st != nil {
					delete(s.unproven, st.RequestId)
				}
			}
		}
		s.mu.Unlock()
		if appendErr != nil {
			// Read this line and the rest of the batch again next time,
			// with a parser that has not seen them: parsing changed its
			// state (a turn it ended would not end again), so it is
			// rebuilt from the log, as after a restart, with what only the
			// rollout before that position tells (Restore).
			path, off := s.log.Checkpoint()
			if path != r.Path {
				off = 0
			}
			s.mu.Lock()
			off = s.rolloutOffset(r.Path, off)
			s.reader = tail.NewReader(r.Path, off)
			s.parserReplayPending = true
			s.parserReplayPath = r.Path
			s.parserReplayOffset = off
			s.mu.Unlock()
			return false, false
		}

		if len(evs) > 0 {
			// The log takes writes: an outcome an earlier failure left
			// owing is logged now.
			s.settleReceived()
		}
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
	return len(lines) > 0, true
}

// checkScreen updates the session status from the screen. It returns false
// once the session has exited.
func (s *session) checkScreen() bool {
	return s.checkScreenContext(context.Background())
}

func (s *session) checkScreenContext(ctx context.Context) bool {
	if !s.term.Alive() {
		if err := s.waitForPTYHostGone(ctx); err != nil {
			if ctx.Err() != nil {
				return false
			}
			slog.Warn("pty-host still running after its tmux session ended", "session", s.meta.ID, "err", errClass(err))
		}
		s.markExited(s.shellExitDetail())
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
		s.restoredPromptSince = time.Time{}
		s.mu.Unlock()
		return true
	}
	if s.driving {
		// Menus opened by a control operation are not the user's popups.
		s.restoredPromptSince = time.Time{}
		s.mu.Unlock()
		return true
	}
	planChanged := s.planOpen != scr.PlanOpen
	retract := s.watchApprovals(scr, styled)
	if scr.Mode != "" {
		model := ""
		if s.context != nil {
			model = s.context.Model
		}
		if s.seenModes == nil {
			s.seenModes = map[string]map[string]bool{}
		}
		if s.seenModes[model] == nil {
			s.seenModes[model] = map[string]bool{}
		}
		s.seenModes[model][scr.Mode] = true
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
		_ = s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{Status: next, Detail: nextDetail, PlanOpen: scr.PlanOpen}}})
	}
	s.settleRestoredPrompt(styled)
	return true
}

// settleRestoredPrompt handles Claude's early cancellation, which writes
// no end record (claude-code-notes §10). A plain idle composer or a sent
// Esc proves nothing: the CLI must restore this turn's original text,
// without any output, in a non-busy composer across successive polls.
// Replayed user/turn events provide the same evidence after a restart.
func (s *session) settleRestoredPrompt(styled string) {
	if s.ag.Kind() != pb.Agent_AGENT_CLAUDE {
		return
	}
	s.mu.Lock()
	prompt, turnID := s.turnPrompt, s.turnID
	if !s.running || s.turnOutput || s.driving || s.meta.Exited || !claude.RestoredPrompt(styled, prompt) {
		s.restoredPromptSince = time.Time{}
		s.mu.Unlock()
		return
	}
	if s.restoredPromptSince.IsZero() {
		s.restoredPromptSince = time.Now()
	}
	ready := time.Since(s.restoredPromptSince) >= time.Second
	s.mu.Unlock()
	if !ready {
		return
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if !s.readRolloutLocked() {
		return
	}
	// Reading a backlog may have taken time or ended the turn. Recheck
	// both the screen and the turn before appending an inferred end.
	styled, err := s.term.Capture()
	if err != nil || !claude.RestoredPrompt(styled, prompt) {
		return
	}
	s.mu.Lock()
	ended := s.running && s.turnID == turnID && !s.turnOutput && !s.driving && !s.meta.Exited && s.abortTurnLocked("input restored by CLI")
	s.mu.Unlock()
	if ended {
		s.retractApprovals()
		s.afterTurn()
	}
}

// abortTurnLocked persists a missing end before changing derived state,
// and keeps the live parser in step with replay. Hold readMu and mu.
func (s *session) abortTurnLocked(reason string) bool {
	e := withOrigin(&pb.Event{TurnId: s.turnID, Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{
		Outcome: pb.TurnOutcome_TURN_OUTCOME_ABORTED, Reason: reason,
	}}}, s.interruptedBy)
	if !s.logOutcomeLocked(e) {
		return false
	}
	s.parser.Replay(e)
	s.interruptedBy = nil
	clear(s.answeredBy)
	return true
}

// shellExitDetail explains an exit that happened before a login-shell
// launch got as far as starting the CLI: the shell's own message, or a
// plain statement that the CLI never ran. It is empty once the CLI
// started (or for any other session), where an exit is just the CLI
// ending.
func (s *session) shellExitDetail() string {
	s.mu.Lock()
	shell := s.meta.Shell
	s.mu.Unlock()
	if !shell {
		return ""
	}
	_, msg, ok := shellReport(s.dir)
	if !ok {
		return "the login shell did not start the CLI"
	}
	return msg
}

func (s *session) markExited(detail string) error {
	s.mu.Lock()
	if s.meta.Exited {
		s.mu.Unlock()
		return nil
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
	if err := m.save(s.dir); err != nil {
		slog.Error("save session", "session", m.ID, "err", errClass(err))
	}
	s.mu.Unlock()
	// Read the final records before settling the old turn and input. A
	// failed read or append is retried when restoring or resuming.
	settled := s.settleExited()
	commitErr := s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{
		Status: pb.SessionStatus_SESSION_STATUS_EXITED, Detail: detail,
	}}})
	// Images sent for the session are kept while it runs: Codex reads
	// them when a message is submitted, and the model may look at a path
	// again later.
	s.d.uploads.dropSession(m.ID)
	if s.stop != nil {
		s.stop()
	}
	if commitErr != nil {
		return fmt.Errorf("CLI already exited but could not record session state: %w", commitErr)
	}
	if !settled {
		return errors.New("CLI already exited but final events could not be recorded; restart the daemon or resume to retry")
	}
	return nil
}

// settleExited settles the messages an exited session still has queued:
// they are not sent after a resume, and each is settled, never dropped
// without a word. Nothing else touches the queue of an exited session. It
// reports whether none is left queued.
func (s *session) settleExited() bool {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if !s.readRolloutLocked() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.meta.Exited {
		return true
	}
	// A dead CLI cannot finish its old turn after a resume. Log the end
	// only after its final records, and teach the live parser as well as
	// replay so the next launch starts from the same state.
	if s.running {
		if !s.abortTurnLocked("session exited") {
			return false
		}
	}
	s.aborted = false // exiting cancels old input; new input belongs to the next CLI
	if len(s.queue.Queue) == 0 && len(s.queue.Held) == 0 {
		return true
	}
	var outcomes []*pb.Event
	for _, in := range s.queue.Queue {
		st := &pb.InputState{RequestId: in.RequestID}
		switch {
		case in.Inflight == nil:
			st.Status, st.Reason = pb.InputStatus_INPUT_STATUS_CANCELED, "exited"
		case s.loggedUserMessage(in):
			st.Status = pb.InputStatus_INPUT_STATUS_SUBMITTED
		default:
			// Written, maybe submitted: as when confirming times out.
			st.Status = pb.InputStatus_INPUT_STATUS_UNCONFIRMED
		}
		outcomes = append(outcomes, &pb.Event{Kind: &pb.Event_InputState{InputState: st}})
	}
	for _, in := range s.queue.Held {
		st := &pb.InputState{RequestId: in.RequestID}
		if in.received || s.loggedUserMessage(in) {
			st.Status = pb.InputStatus_INPUT_STATUS_SUBMITTED
		} else {
			// The CLI acknowledged holding it, but left no record proving
			// delivery to the model. Do not claim it was never delivered,
			// and never replay it automatically in the resumed CLI.
			st.Status, st.Reason = pb.InputStatus_INPUT_STATUS_UNCONFIRMED, "exited"
		}
		outcomes = append(outcomes, &pb.Event{Kind: &pb.Event_InputState{InputState: st}})
	}
	if !s.logOutcomeLocked(outcomes...) {
		return false // they stay for the next start, or resume, to settle
	}
	s.queue.Queue = nil
	s.queue.Held = nil
	s.saveSettledLocked()
	return true
}

// Info summarizes the session.
func (s *session) info() *pb.Session { s.mu.Lock(); defer s.mu.Unlock(); return s.infoLocked() }
func (s *session) infoLocked() *pb.Session {
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
	if s.meta.Title != "" {
		out.Title = s.meta.Title
	}
	out.ForkedFrom, out.ForkedSeq = s.meta.ForkedFrom, s.meta.ForkedSeq
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
			return strings.Clone(line)
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

// rename gives the session the title the user chose, on one line; an
// empty one goes back to the title from the first message. It works on
// an exited session too.
func (s *session) rename(title string) error {
	return s.renameUsing(title, (*meta).save)
}

func (s *session) renameUsing(title string, save func(*meta, string) error) error {
	title = strings.Join(strings.Fields(title), " ")
	if n := utf8.RuneCountInString(title); n > renameMax {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the title has %d characters; at most %d fit", n, renameMax))
	}
	s.mu.Lock()
	m := s.meta
	m.Title = title
	if err := save(&m, s.dir); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("save session: %w", err)
	}
	s.meta.Title = title
	s.mu.Unlock()
	// The relay uploads the session's metadata when the log changes.
	s.log.Poke()
	return nil
}

// renameMax is how many characters a title the user gives may have.
const renameMax = 60

// cancelInput removes a queued message unless it is already being
// written.
func (s *session) cancelInput(requestID string) error {
	return s.cancelInputBy(requestID, originLocal)
}

// cancelInputBy is cancelInput on behalf of origin.
func (s *session) cancelInputBy(requestID string, origin *pb.Origin) error {
	s.mu.Lock()
	if s.meta.Exited {
		s.mu.Unlock()
		return errExited
	}
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
	if !s.logOutcomeLocked(withOrigin(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{
		RequestId: requestID, Status: pb.InputStatus_INPUT_STATUS_CANCELED,
	}}}, origin)) {
		s.mu.Unlock()
		return errors.New("could not record the cancellation; the message is still queued")
	}
	prev := s.queue.Queue
	q := append([]input{}, prev[:idx]...)
	s.queue.Queue = append(q, prev[idx+1:]...)
	s.saveSettledLocked()
	s.mu.Unlock()
	s.d.uploads.releaseRefs(prev[idx].Images, s.meta.ID, requestID)
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
	if err := s.checkRemoteAnswer(remoteQuestion, questionID, false, origin); err != nil {
		s.mu.Unlock()
		return err
	}
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
	err = s.checkRemoteAnswer(remoteQuestion, questionID, false, origin)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("question %s was already answered: %w", questionID, errNotFound)
	}
	styled, err := s.term.Capture()
	if err != nil {
		return err
	}
	items := q.Questions
	var others []*pb.Question
	if index != nil {
		if *index < 0 || int(*index) >= len(items) {
			return fmt.Errorf("question index %d out of range", *index)
		}
		others = slices.Delete(slices.Clone(items), int(*index), int(*index)+1)
		items = items[*index : *index+1]
	}
	if !s.ag.Analyze(styled).QuestionOpen || !showsQuestion(styled, items, others) {
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
	return s.killContext(context.Background())
}

func (s *session) killContext(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.ctlMu.Lock()
	defer s.ctlMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	exited := s.meta.Exited
	s.mu.Unlock()
	if exited {
		return nil
	}
	if err := s.term.Kill(); err != nil && s.term.Alive() {
		return err
	}
	// The session is marked exited once the CLI is gone, so the last read
	// of its transcript sees everything. A host that outlives the wait (one
	// from an older version never exits) does not keep it running.
	if err := s.waitForPTYHostGone(ctx); err != nil {
		if ctx.Err() != nil {
			return err
		}
		slog.Warn("pty-host still running after stop", "session", s.meta.ID, "err", errClass(err))
	}
	return s.markExited("stopped")
}

func (s *session) waitForPTYHostGone(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, ptyHostExitTimeout)
	defer cancel()
	ticker := time.NewTicker(ptyHostPollInterval)
	defer ticker.Stop()
	for {
		alive, err := s.term.HostAlive(ctx)
		if err != nil {
			if parent.Err() != nil {
				return parent.Err()
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("pty-host is still alive after %s", ptyHostExitTimeout)
			}
			return fmt.Errorf("check whether pty-host exited: %w", err)
		}
		if !alive {
			return nil
		}
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return parent.Err()
			}
			return fmt.Errorf("pty-host is still alive after %s", ptyHostExitTimeout)
		case <-ticker.C:
		}
	}
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
	s.mu.Unlock()
	p := s.ag.NewParser()
	// It needs what is known so far, such as a model switched to before
	// the transcript was found.
	if err := s.log.Each(p.Replay); err != nil {
		slog.Error("replay session events", "session", s.meta.ID, "err", errClass(err))
		s.readMu.Unlock()
		return
	}
	s.mu.Lock()
	s.meta.RolloutPath = path
	if cliSessionID != "" {
		s.meta.CodexSessionID = cliSessionID
	}
	s.reader = tail.NewReader(path, 0)
	s.parserReplayPending = false
	s.parserReplayPath, s.parserReplayOffset = "", 0
	s.parser = p
	m := s.meta
	if err := m.save(s.dir); err != nil {
		slog.Error("save session", "session", m.ID, "err", errClass(err))
	}
	s.mu.Unlock()
	s.readMu.Unlock()
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
			fresh = append(fresh, e)
		}
		if len(fresh) > 0 {
			if err := s.emitLocked(false, fresh...); err != nil {
				s.readMu.Unlock()
				return nil, err
			}
			for _, e := range fresh {
				s.parser.Replay(e)
			}
		}
		s.readMu.Unlock()
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
	a := &approval{turnID: p.TurnID, id: newID(4), decision: make(chan decision, 1), deadline: time.Now().Add(timeout)}
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
	e := &pb.Event{TurnId: p.TurnID, Kind: &pb.Event_ApprovalRequested{ApprovalRequested: req}}
	e.Time = timestamppb.Now()
	if err := s.appendLocked([]*pb.Event{e}, "", 0); err != nil {
		s.mu.Unlock()
		// A transport error would make the hook discard this payload and
		// fall back to native approval. The denial must reach the CLI;
		// appendLocked already reports the storage fault locally.
		return agent.PermissionDecision(false, "remote-agent: could not record approval request; denied"), nil
	}
	s.approvals[a.id] = a
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.approvals, a.id)
		s.mu.Unlock()
	}()

	resolved := func(d pb.ApprovalDecision, msg string, o *pb.Origin) error {
		return s.emit(withOrigin(&pb.Event{TurnId: p.TurnID, Kind: &pb.Event_ApprovalResolved{ApprovalResolved: &pb.ApprovalResolved{
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
		_ = resolved(pb.ApprovalDecision_APPROVAL_DECISION_TIMEOUT, msg, originSystem)
		return agent.PermissionDecision(false, msg), nil
	case <-ctx.Done():
		if !s.claim(a) {
			d = <-a.decision
			break
		}
		err := resolved(pb.ApprovalDecision_APPROVAL_DECISION_CANCELED, "", originSystem)
		return nil, errors.Join(ctx.Err(), err)
	}
	if d.terminal {
		if err := resolved(pb.ApprovalDecision_APPROVAL_DECISION_TERMINAL, "", originLocal); err != nil {
			return nil, fmt.Errorf("approval already answered on the computer but could not be recorded: %w", err)
		}
		return nil, nil
	}
	if !d.recorded {
		kind := pb.ApprovalDecision_APPROVAL_DECISION_DENY
		if d.allow {
			kind = pb.ApprovalDecision_APPROVAL_DECISION_ALLOW
		}
		if err := resolved(kind, d.message, d.origin); err != nil {
			msg := "remote-agent: could not record approval result; denied"
			if !d.allow {
				msg = denialFor(d)
			}
			return agent.PermissionDecision(false, msg), nil
		}
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
	s.mu.Lock()
	if err := s.checkRemoteAnswer(remoteApproval, id, allow, origin); err != nil {
		s.mu.Unlock()
		return err
	}
	// Native permission prompts expose no stable tool request ID. Screen
	// text (especially command prefixes or basenames) cannot prove which
	// request would receive a permanent grant. Leave it pending for the
	// user to decide in the native terminal; never silently grant once.
	if allow && always {
		s.mu.Unlock()
		return fmt.Errorf("permanent approvals must be answered in the native terminal: %w", agent.ErrUnsupported)
	}
	a, ok := s.approvals[id]
	if !ok || a.claimed {
		s.mu.Unlock()
		return fmt.Errorf("approval %s: %w", id, errNotFound)
	}
	if !time.Now().Before(a.deadline) {
		s.mu.Unlock()
		return fmt.Errorf("approval %s: %w", id, errExpired)
	}
	kind := pb.ApprovalDecision_APPROVAL_DECISION_DENY
	if allow {
		kind = pb.ApprovalDecision_APPROVAL_DECISION_ALLOW
	}
	e := withOrigin(&pb.Event{TurnId: a.turnID, Time: timestamppb.Now(), Kind: &pb.Event_ApprovalResolved{ApprovalResolved: &pb.ApprovalResolved{
		ApprovalId: id, Decision: kind, Message: message,
	}}}, origin)
	commitErr := s.appendLocked([]*pb.Event{e}, "", 0)
	if commitErr != nil && allow {
		s.mu.Unlock()
		return fmt.Errorf("approval was not delivered; could not record result: %w", commitErr)
	}
	a.claimed = true
	delete(s.approvals, id)
	s.mu.Unlock()
	a.decision <- decision{allow: allow, message: message, origin: origin, recorded: commitErr == nil}
	if commitErr != nil {
		return fmt.Errorf("approval denied but could not record result: %w", commitErr)
	}
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

// showsQuestion reports whether the screen shows one of the questions and
// none of others, the rest of the same call. The start of the text is
// enough and survives the screen cutting it short; it is taken as long as
// it must be to differ from every one of others, and a question whose
// whole text also begins another cannot be told from it. White space is
// left out, so that line wrapping does not matter wherever it falls.
func showsQuestion(styled string, questions, others []*pb.Question) bool {
	squeeze := func(s string) []rune { return []rune(strings.Join(strings.Fields(s), "")) }
	screen := string(squeeze(agent.StripANSI(styled)))
	for _, item := range questions {
		text := squeeze(item.Text)
		n := min(len(text), 24)
		for _, o := range others {
			other := squeeze(o.Text)
			same := 0
			for same < len(text) && same < len(other) && text[same] == other[same] {
				same++
			}
			if same == len(text) {
				n = 0
				break
			}
			n = max(n, same+1)
		}
		if n > 0 && strings.Contains(screen, string(text[:n])) {
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
