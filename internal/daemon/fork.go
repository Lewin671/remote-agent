package daemon

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/tmux"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	errForkBusy  = errors.New("the session can be forked once its turn has ended and nothing waits for an answer")
	errForkEmpty = errors.New("the session has no conversation to fork yet: no message was sent")
)

// fork starts a new session that carries on from session id's
// conversation as it is now, with the CLI's own fork, on behalf of origin.
// The source is left as it is. The new session runs in the same folder
// with the source's environment and options and the settings it last ran
// with, as a resumed session would (architecture §13).
//
// Its history is a copy of the source's events, not read from the CLI's
// records: Codex's fork only refers to its source's rollout, and Claude
// Code writes a fork's transcript with its first message.
func (d *Daemon) fork(id string, origin *pb.Origin) (*session, error) {
	d.resumeMu.Lock()
	defer d.resumeMu.Unlock()
	src, err := d.exact(id)
	if err != nil {
		return nil, err
	}
	src.readRollout()
	// Events and the offset they were read up to, as one: what the CLI
	// has recorded past it is the fork's to read.
	src.readMu.Lock()
	src.mu.Lock()
	m, ctx := src.meta, src.context
	busy := !m.Exited && (src.running || src.status != pb.SessionStatus_SESSION_STATUS_IDLE ||
		len(src.approvals) > 0 || len(src.questions) > 0 || src.planOpen)
	title := src.title
	if m.Title != "" {
		title = m.Title
	}
	var offset int64
	if src.reader != nil {
		offset = src.reader.Offset()
	}
	through := src.log.LastSeq()
	src.mu.Unlock()
	src.readMu.Unlock()
	if busy {
		return nil, errForkBusy
	}
	if m.CodexSessionID == "" || m.RolloutPath == "" || offset == 0 {
		return nil, errForkEmpty
	}
	if _, err := os.Stat(m.RolloutPath); err != nil {
		return nil, errForkEmpty
	}
	if st, err := os.Stat(m.Cwd); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("directory %s is gone", m.Cwd)
	}
	if src.ag.Bin() == "" {
		return nil, fmt.Errorf("%s is not installed on this machine", src.ag.Name())
	}

	nid := newID(8)
	nm := meta{ID: nid, Agent: m.Agent, Cwd: m.Cwd, Tmux: "ra-" + nid[:8], Args: m.Args, CreatedAt: time.Now().UTC(),
		Title: m.Title, NoFullAccess: m.NoFullAccess, StandardContext: m.StandardContext, Starting: true,
		ForkedFrom: m.ID}
	dir := paths.SessionDir(nid)
	if err := paths.EnsureDir(dir); err != nil {
		return nil, err
	}
	fail := func(err error) (*session, error) {
		_ = os.RemoveAll(dir) // a session that never started leaves nothing
		return nil, err
	}
	launch := &agent.Launch{
		Dir: dir, Cwd: m.Cwd, Args: m.Args, ForkOf: m.CodexSessionID, Transcript: m.RolloutPath, ForkOffset: offset,
		Context: ctx, FullAccess: !m.NoFullAccess, Exe: d.cfg.Exe, RemoteSession: nid,
	}
	cliArgs, err := src.ag.Argv(launch)
	if err != nil {
		return fail(err)
	}
	nm.CodexSessionID = launch.SessionID
	if err := nm.save(dir); err != nil {
		return fail(err)
	}
	// The same environment as the source: its caller's shell may have
	// provided credentials or proxies.
	env, err := readEnvFile(filepath.Join(src.dir, "env.json"))
	if err != nil {
		return fail(fmt.Errorf("read the session's environment: %w", err))
	}
	d.ownEnv(env, nid)
	if m.Agent == pb.Agent_AGENT_CLAUDE {
		env["CLAUDE_CODE_DISABLE_CRON"] = "1"
	}
	envFile, err := startEnv(dir, env)
	if err != nil {
		return fail(err)
	}
	log, err := eventlog.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return fail(err)
	}
	fail = func(err error) (*session, error) {
		log.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	forked := withOrigin(&pb.Event{Time: timestamppb.Now(), Kind: &pb.Event_SessionForked{SessionForked: &pb.SessionForked{
		SourceSessionId: m.ID, SourceTitle: title,
	}}}, origin)
	if err := copyForkHistory(src.log, through, log, nid); err != nil {
		return fail(err)
	}
	nm.ForkedSeq = log.LastSeq() + 1
	if err := nm.save(dir); err != nil {
		return fail(err)
	}
	if err := log.Append(nid, []*pb.Event{forked}, "", 0); err != nil {
		return fail(err)
	}
	argv := []string{d.cfg.Exe, "pty-host", "--socket", paths.PTYSocket(nid), "--env-file", envFile, "--", src.ag.Bin()}
	argv = append(argv, cliArgs...)
	defer d.beginStart(nid)()
	if err := d.cfg.Tmux.NewSession(tmux.NewSessionOptions{
		Name: nm.Tmux, Dir: nm.Cwd, Width: d.cfg.Width, Height: d.cfg.Height, Argv: argv,
	}); err != nil {
		return fail(err)
	}
	if err := d.cfg.Tmux.SetSessionOption(nm.Tmux, "@ra_session_id", nid); err != nil {
		_ = d.cfg.Tmux.KillSession(nm.Tmux)
		return fail(err)
	}
	_ = d.cfg.Tmux.SetSessionOption(nm.Tmux, "status", "off")

	s, err := newSession(d, dir, nm, d.terminal(&nm), log)
	if err != nil {
		_ = d.cfg.Tmux.KillSession(nm.Tmux)
		return fail(err)
	}
	s.mu.Lock()
	s.meta.Starting = false
	started := s.meta
	s.mu.Unlock()
	if err := s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{
		Status: pb.SessionStatus_SESSION_STATUS_STARTING,
	}}}); err != nil {
		_ = d.cfg.Tmux.KillSession(nm.Tmux)
		return fail(err)
	}
	if err := started.save(dir); err != nil {
		slog.Error("save session", "session", nid, "err", errClass(err))
	}
	d.mu.Lock()
	d.sessions[nid] = s
	d.mu.Unlock()
	s.start(d.ctx)
	slog.Info("forked session", "session", nid, "from", m.ID)
	return s, nil
}

// forkHistory is the part of a source's events a fork starts with: the
// conversation, without what described the source's own process and
// queue. Requests nobody answered are left out, as nobody can answer them
// in the fork; a turn the source never saw the end of (it exited in it)
// ends here. Images keep their previews only: the display copies are
// stored under the source session, sealed with its key.
func forkHistory(evs []*pb.Event) []*pb.Event {
	resolved := map[string]bool{}
	answered := map[string]bool{}
	for _, e := range evs {
		rememberForkResolution(resolved, answered, e)
	}
	var out []*pb.Event
	running := false
	for _, e := range evs {
		if e.GetTurnStarted() != nil {
			running = true
		}
		if e.GetTurnEnded() != nil {
			running = false
		}
		if c := forkEvent(e, resolved, answered); c != nil {
			out = append(out, c)
		}
	}
	if running {
		end := &pb.Event{Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_ABORTED}}}
		if n := len(out); n > 0 {
			end.Time, end.TurnId = out[n-1].Time, out[n-1].TurnId
		}
		out = append(out, end)
	}
	return out
}

func rememberForkResolution(resolved, answered map[string]bool, e *pb.Event) {
	switch k := e.Kind.(type) {
	case *pb.Event_ApprovalResolved:
		resolved[k.ApprovalResolved.ApprovalId] = true
	case *pb.Event_QuestionAnswered:
		answered[k.QuestionAnswered.QuestionId] = true
	}
}

func forkEvent(e *pb.Event, resolved, answered map[string]bool) *pb.Event {
	switch k := e.Kind.(type) {
	case *pb.Event_SessionState, *pb.Event_InputState:
		return nil
	case *pb.Event_ApprovalRequested:
		if !resolved[k.ApprovalRequested.ApprovalId] {
			return nil
		}
	case *pb.Event_QuestionAsked:
		if !k.QuestionAsked.Async && !answered[k.QuestionAsked.QuestionId] {
			return nil
		}
	}
	c := proto.CloneOf(e)
	c.Seq, c.SessionId = 0, ""
	for _, ref := range agent.EventImages(c) {
		ref.UploadId = ""
		if ref.Sync != pb.ImageSync_IMAGE_SYNC_SYNCED {
			continue
		}
		ref.DisplayWidth, ref.DisplayHeight = 0, 0
		ref.Sync = pb.ImageSync_IMAGE_SYNC_PREVIEW_ONLY
		if len(ref.Preview) == 0 {
			ref.Sync = pb.ImageSync_IMAGE_SYNC_NAME_ONLY
		}
	}
	return c
}

// copyForkHistory makes two passes over the source snapshot without retaining
// event payloads: one to resolve the IDs which are copied, then one to append
// the transformed events in durable batches. The resolution sets grow only
// with approval and question IDs.
func copyForkHistory(src *eventlog.Log, through uint64, dst *eventlog.Log, sessionID string) error {
	resolved, answered := map[string]bool{}, map[string]bool{}
	if err := src.EachRange(0, through, func(e *pb.Event) error {
		rememberForkResolution(resolved, answered, e)
		return nil
	}); err != nil {
		return err
	}
	const batchLimit = 200
	var batch []*pb.Event
	running := false
	var last *pb.Event
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := dst.Append(sessionID, batch, "", 0); err != nil {
			return err
		}
		batch = nil
		return nil
	}
	if err := src.EachRange(0, through, func(e *pb.Event) error {
		if e.GetTurnStarted() != nil {
			running = true
		}
		if e.GetTurnEnded() != nil {
			running = false
		}
		if copy := forkEvent(e, resolved, answered); copy != nil {
			batch = append(batch, copy)
			last = copy
			if len(batch) == batchLimit {
				return flush()
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if running {
		end := &pb.Event{Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_ABORTED}}}
		if last != nil {
			end.Time, end.TurnId = last.Time, last.TurnId
		}
		batch = append(batch, end)
	}
	return flush()
}
