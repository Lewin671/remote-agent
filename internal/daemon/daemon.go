// Package daemon manages AI coding CLI sessions running in tmux and serves
// the local API. What is particular to each CLI is behind agent.Adapter.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/claude"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/contextio"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"github.com/Lewin671/remote-agent/internal/tmux"
	"github.com/Lewin671/remote-agent/internal/updater"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Config configures a Daemon.
type Config struct {
	// Exe is the remote-agent binary, used as pty-host and hook command.
	Exe string
	// CodexBin is the real codex executable.
	CodexBin string
	// CodexCmd and ClaudeCmd are what a client-created session runs in
	// the user's login shell: the executable the user configured, or the
	// command name for the shell to resolve in its own PATH when they
	// configured none.
	CodexCmd  string
	ClaudeCmd string
	Tmux      *tmux.Client
	// CodexHome is Codex's state directory, ~/.codex by default.
	CodexHome string
	// HistoryPath is Codex's history.jsonl.
	HistoryPath string
	// ClaudeBin is the claude executable, empty if Claude Code is not
	// installed; ClaudeHome is its settings directory, ~/.claude.
	ClaudeBin  string
	ClaudeHome string
	// Env is added to every session, e.g. proxy variables.
	Env map[string]string

	ApprovalTimeout time.Duration
	PollInterval    time.Duration // rollout reads
	ScreenInterval  time.Duration // screen and liveness checks
	RetryInterval   time.Duration // retry of blocked input
	SubmitTimeout   time.Duration // wait for history.jsonl after injecting
	// ShellStartTimeout bounds how long a client-created session's login
	// shell may take to start the CLI; a shell whose configuration waits
	// or exits otherwise leaves the session starting forever.
	ShellStartTimeout time.Duration
	Width, Height     int // size of new sessions until attached

	// Started, if set, runs in its own goroutine once the daemon holds its
	// lock and has restored its sessions.
	Started func(ctx context.Context)
}

func (c *Config) defaults() {
	if c.ApprovalTimeout == 0 {
		c.ApprovalTimeout = 10 * time.Minute
	}
	if c.PollInterval == 0 {
		c.PollInterval = 300 * time.Millisecond
	}
	if c.ScreenInterval == 0 {
		c.ScreenInterval = time.Second
	}
	if c.RetryInterval == 0 {
		c.RetryInterval = 500 * time.Millisecond
	}
	if c.SubmitTimeout == 0 {
		c.SubmitTimeout = 10 * time.Second
	}
	if c.ShellStartTimeout == 0 {
		c.ShellStartTimeout = time.Minute
	}
	if c.Width == 0 {
		c.Width, c.Height = 160, 48
	}
	if c.CodexCmd == "" {
		c.CodexCmd = c.CodexBin
	}
	if c.ClaudeCmd == "" {
		c.ClaudeCmd = c.ClaudeBin
	}
	if c.CodexHome == "" && c.HistoryPath != "" {
		c.CodexHome = filepath.Dir(c.HistoryPath)
	}
}

// Daemon owns all sessions.
type Daemon struct {
	updateMu        sync.Mutex
	updateIdleSince time.Time
	cfg             Config
	ctx             context.Context
	agents          map[pb.Agent]agent.Adapter

	// uploads are images clients sent for messages.
	uploads *uploads

	mu       sync.Mutex
	sessions map[string]*session
	// starting are the sessions whose CLI has been started and that are
	// not in sessions yet; each channel is closed once its session is, or
	// its creation failed.
	starting map[string]chan struct{}
	limits   *pb.TokenUsage // latest usage limits seen in any session
	limitsAt time.Time      // when Codex reported them
	quotas   claudeQuotas   // Claude Code's account limits
	// reported is each session's last Claude Code status line.
	reported map[string]quotaReport

	scheduleMu sync.Mutex
	tasks      map[string]*schedule.Task
	// lateRuns names, by task ID, the execution that ended uncertain
	// because its message was reported UNCONFIRMED and that a late record
	// may still confirm (schedule.go, reconfirmScheduledRun). Like the
	// sessions' memory of such messages, it does not outlive the daemon.
	lateRuns map[string]string

	settingsCommitMu sync.Mutex // serializes settings file + cache commits, never the security gate
	settingsMu       sync.Mutex
	cached           *pb.DaemonSettings

	// resumeMu makes resumes one at a time: two for the same session
	// would both pass the checks and share its startup files.
	cacheMu  sync.Mutex
	resumeMu sync.Mutex
}

// New returns a daemon; call Run to start it.
func New(cfg Config) *Daemon {
	cfg.defaults()
	d := &Daemon{cfg: cfg, sessions: map[string]*session{}, starting: map[string]chan struct{}{}, agents: map[pb.Agent]agent.Adapter{}, uploads: newUploads()}
	d.uploads.limits = func() int64 { return d.settings().ImageUploadQuotaBytes }
	d.uploads.pins = d.protectedImageSessions
	d.agents[pb.Agent_AGENT_CODEX] = codex.New(cfg.CodexBin, cfg.CodexHome, cfg.HistoryPath)
	d.agents[pb.Agent_AGENT_CLAUDE] = claude.New(cfg.ClaudeBin, cfg.ClaudeHome)
	return d
}

// adapter is the adapter for a session's CLI. Sessions from before Claude
// Code support name none and are Codex sessions.
func (d *Daemon) adapter(k pb.Agent) agent.Adapter {
	if a, ok := d.agents[k]; ok {
		return a
	}
	return d.agents[pb.Agent_AGENT_CODEX]
}

// knownAgent returns the adapter for a request's CLI, refusing ones this
// daemon cannot run.
func (d *Daemon) knownAgent(k pb.Agent) (agent.Adapter, error) {
	if k == pb.Agent_AGENT_UNSPECIFIED {
		k = pb.Agent_AGENT_CODEX
	}
	a, ok := d.agents[k]
	if !ok {
		return nil, fmt.Errorf("%v: %w", k, agent.ErrUnsupported)
	}
	return a, nil
}

// Run takes the daemon lock, restores sessions, and serves the local API on
// the unix socket until ctx is done.
func (d *Daemon) Run(ctx context.Context) error {
	if err := paths.EnsureDir(paths.Dir()); err != nil {
		return err
	}
	lock, err := os.OpenFile(paths.Lock(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another remote-agent daemon is already running")
	}

	if err := updater.EnsureState(paths.Dir()); err != nil {
		return err
	}
	d.ctx = ctx
	d.loadQuotas()
	d.uploads.reconcile()
	if err := d.restore(); err != nil {
		return err
	}
	for _, a := range d.agents {
		if a.Bin() == "" {
			continue
		}
		if v := a.Version(); v != "" && !d.verified(a) {
			slog.Warn("this CLI version has not been checked with remote-agent; run `remote-agent doctor`", "agent", a.Name(), "version", v)
		}
	}
	if err := d.loadSchedules(); err != nil {
		return err
	}
	go d.scheduleLoop(ctx)
	go d.imageGCLoop(ctx.Done())
	go d.releaseLogsLoop(ctx.Done())
	if d.cfg.Started != nil {
		go d.cfg.Started(ctx)
	}

	sock := paths.Socket()
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return err
	}
	defer os.Remove(sock)

	go d.watchUpdateIdle(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc("/update/health", d.updateHealth)
	mux.Handle(remoteagentv1connect.NewLocalServiceHandler(&handler{d: d}, connect.WithInterceptors(protocol.Local())))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	slog.Info("daemon listening", "socket", sock)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// restore loads every session from disk and reattaches to those whose tmux
// session is still running.
func (d *Daemon) restore() error {
	if err := paths.EnsureDir(paths.SessionsDir()); err != nil {
		return err
	}
	entries, err := os.ReadDir(paths.SessionsDir())
	if err != nil {
		return err
	}
	live := map[string]bool{}
	infos, err := d.cfg.Tmux.ListSessions()
	if err != nil {
		return err
	}
	for _, info := range infos {
		if info.SessionID != "" {
			live[info.Name+"\x00"+info.SessionID] = true
		}
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(paths.SessionsDir(), e.Name())
		m, err := loadMeta(dir)
		if err != nil {
			slog.Warn("skip session", "session", filepath.Base(dir), "err", errClass(err))
			continue
		}
		log, err := eventlog.Open(filepath.Join(dir, "events.jsonl"))
		if err != nil {
			slog.Warn("skip session", "session", filepath.Base(dir), "err", errClass(err))
			continue
		}
		alive := live[m.Tmux+"\x00"+m.ID]
		if d.unstarted(m, dir, alive) {
			log.Close()
			continue
		}
		s, err := newSession(d, dir, *m, d.terminal(m), log)
		if err != nil {
			slog.Warn("skip session with unreadable history", "session", m.ID, "err", errClass(err))
			_ = log.Close()
			continue
		}
		// Sessions restored before this one already run their loops, which
		// read the list.
		d.mu.Lock()
		d.sessions[m.ID] = s
		d.mu.Unlock()
		d.uploads.restoreRefs(s)
		s.cancelStaleApprovals()
		if m.Exited && alive {
			// Marked exited while still running, as when tmux was misread;
			// stopping or ending kills the tmux session.
			s.mu.Lock()
			s.meta.Exited = false
			if err := s.meta.save(dir); err != nil {
				slog.Error("save session", "session", m.ID, "err", errClass(err))
			}
			s.mu.Unlock()
			_ = s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{
				Status: pb.SessionStatus_SESSION_STATUS_STARTING, Detail: "reattached",
			}}})
		} else if m.Exited {
			// Exited records process liveness, not a transcript checkpoint.
			// Retry the final read and settlement even when that flag was
			// saved before the previous daemon stopped.
			s.settleExited()
			// Read once to restore it; nothing reads an ended session's
			// events again unless a client or the server asks. Letting go
			// now keeps a start from holding every session at once.
			log.Release(0)
			continue
		}
		if !alive {
			if err := s.waitForPTYHostGone(d.ctx); err != nil {
				if d.ctx.Err() != nil {
					return d.ctx.Err()
				}
				slog.Warn("pty-host still running after its tmux session ended", "session", m.ID, "err", errClass(err))
			}
			s.markExited("tmux session gone")
			continue
		}
		slog.Info("reattached session", "session", m.ID, "tmux", m.Tmux)
		// A launch through the login shell that has not started its CLI
		// gets a fresh watchdog window after the restart.
		s.noteShellLaunch()
		s.start(d.ctx)
	}
	return nil
}

func (d *Daemon) terminal(m *meta) terminal {
	return &tmuxTerminal{tmux: d.cfg.Tmux, name: m.Tmux, socket: paths.PTYSocket(m.ID)}
}

// cancelStaleApprovals resolves approvals that were pending when a previous
// daemon stopped; their hooks have given up on that daemon.
func (s *session) cancelStaleApprovals() {
	order := s.staleApprovals
	for _, id := range order {
		if err := s.emit(&pb.Event{Kind: &pb.Event_ApprovalResolved{ApprovalResolved: &pb.ApprovalResolved{
			ApprovalId: id, Decision: pb.ApprovalDecision_APPROVAL_DECISION_CANCELED, Message: "daemon restarted",
		}}}); err != nil {
			return
		}
		s.staleApprovals = s.staleApprovals[1:]
	}
}

// unstarted settles a session a previous daemon was creating when it
// stopped. If its CLI never started, the creation did not happen: the
// first message's images go back to pending, where they can be sent
// again, and the session is removed; it reports true then. A creation
// whose CLI did start is kept as it is.
func (d *Daemon) unstarted(m *meta, dir string, alive bool) bool {
	if !m.Starting {
		return false
	}
	if alive {
		m.Starting = false
		if err := m.save(dir); err != nil {
			slog.Error("save session", "session", m.ID, "err", errClass(err))
		}
		return false
	}
	d.uploads.release(m.ID)
	if err := os.RemoveAll(dir); err != nil {
		slog.Error("remove unstarted session", "session", m.ID, "err", errClass(err))
	}
	slog.Info("undid a session creation that did not finish", "session", m.ID)
	return true
}

// beginStart is called before a new session's CLI is started. The CLI's
// first hook (SessionStart, which says where its transcript is) can arrive
// before the session is registered; hookSession makes it wait until the
// returned function is called, when the session is registered or its
// creation has failed.
func (d *Daemon) beginStart(id string) (done func()) {
	ch := make(chan struct{})
	d.mu.Lock()
	d.starting[id] = ch
	d.mu.Unlock()
	return func() {
		d.mu.Lock()
		delete(d.starting, id)
		d.mu.Unlock()
		close(ch)
	}
}

// hookSession finds the session a hook is for, waiting for one that is
// just starting.
func (d *Daemon) hookSession(ctx context.Context, id string) (*session, error) {
	d.mu.Lock()
	starting := d.starting[id]
	d.mu.Unlock()
	if starting != nil {
		select {
		case <-starting:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return d.session(id)
}

// create starts a new CLI session in tmux with the daemon's own
// environment, as `remote-agent doctor` uses it.
func (d *Daemon) create(req *pb.CreateSessionRequest) (*session, error) {
	return d.createBy(req, originLocal, false, false, "the terminal's PATH")
}

// createBy is create on behalf of origin, who sent the first message's
// images. viaShell starts the CLI through the user's login shell instead
// of with the daemon's environment: what a session a client created does,
// so that its environment comes from the shell's configuration. terminal
// says the caller is a terminal on this machine, whose environment the
// session runs with and whose CLI it must find. pathFrom names the PATH
// that CLI is looked up in, for the error that says where it was missing.
func (d *Daemon) createBy(req *pb.CreateSessionRequest, origin *pb.Origin, viaShell, terminal bool, pathFrom string) (*session, error) {
	return d.createByContext(context.Background(), req, origin, viaShell, terminal, pathFrom)
}

func (d *Daemon) createByContext(ctx context.Context, req *pb.CreateSessionRequest, origin *pb.Origin, viaShell, terminal bool, pathFrom string) (*session, error) {
	cwd := req.Cwd
	if cwd == "" {
		return nil, errors.New("cwd is required")
	}
	if !filepath.IsAbs(cwd) {
		return nil, errors.New("cwd must be an absolute path")
	}
	if st, err := contextio.Stat(ctx, cwd); err != nil || !st.IsDir() {
		if err != nil {
			return nil, fmt.Errorf("inspect working directory: %w", err)
		}
		return nil, fmt.Errorf("cwd %s is not a directory", cwd)
	}
	ag, err := d.knownAgent(req.Agent)
	if err != nil {
		return nil, err
	}
	// The first message's images must be here before a session starts.
	owner := ""
	if len(req.ImageIds) > 0 {
		if owner, err = imageOwner(origin); err != nil {
			return nil, err
		}
		if err := d.uploads.check(req.ImageIds, owner); err != nil {
			return nil, err
		}
	}
	if ag.Bin() == "" {
		return nil, fmt.Errorf("%s is not installed on this machine", ag.Name())
	}
	args, err := d.sessionArgsContext(ctx, ag, req)
	if err != nil {
		return nil, err
	}
	id := newID(8)
	m := meta{ID: id, Agent: req.Agent, Cwd: cwd, Tmux: "ra-" + id[:8], Args: args, CreatedAt: time.Now().UTC(), NoFullAccess: !ag.FullAccess(args), Starting: true,
		StandardContext: req.Agent == pb.Agent_AGENT_CLAUDE && req.StandardContext, Shell: viaShell}
	// The environment the CLI starts with: a client-created session's
	// login shell gets what a new terminal gives it and builds the rest
	// from the user's configuration; any other session gets the caller's
	// own environment, as from that terminal, and runs the CLI that
	// terminal would. A session without a caller's environment (doctor's)
	// runs the daemon's. A terminal session that cannot find the CLI in
	// its PATH does not start: the daemon's own is a different CLI.
	var env map[string]string
	bin := ag.Bin()
	if m.Shell {
		env = shellSessionEnv(d.cfg.Env)
	} else {
		env = sessionEnv(req.Env, d.cfg.Env)
		if terminal {
			if bin, err = d.terminalCLI(ag, env["PATH"], pathFrom); err != nil {
				return nil, err
			}
		}
	}
	d.cliEnv(env, &m)
	dir := paths.SessionDir(id)
	if err := paths.EnsureDir(dir); err != nil {
		return nil, err
	}
	if m.Shell {
		clearShellReport(dir)
	}
	launch := &agent.Launch{Dir: dir, Args: args, Cwd: cwd, Exe: d.cfg.Exe, RemoteSession: id}
	cliArgs, err := ag.Argv(launch)
	if err != nil {
		return nil, err
	}
	m.CodexSessionID = launch.SessionID
	if err := m.save(dir); err != nil {
		return nil, err
	}
	log, err := eventlog.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, err
	}

	envFile, err := startEnv(dir, env)
	if err != nil {
		log.Close()
		return nil, err
	}
	// The first message's images move into the session before the CLI
	// starts: if they cannot, nothing starts.
	firstRequest := newID(8)
	images, err := d.uploads.take(req.ImageIds, owner, id, firstRequest)
	if err != nil {
		os.Remove(envFile)
		log.Close()
		return nil, err
	}
	fail := func(err error) (*session, error) {
		d.uploads.untake(images, id, firstRequest)
		log.Close()
		_ = os.RemoveAll(dir) // a session that never started leaves nothing
		return nil, err
	}
	argv := d.ptyHostArgv(dir, &m, bin, cliArgs, envFile)
	defer d.beginStart(id)()
	err = d.cfg.Tmux.NewSession(tmux.NewSessionOptions{
		Name: m.Tmux, Dir: cwd, Width: d.cfg.Width, Height: d.cfg.Height, Argv: argv,
	})
	if err != nil {
		os.Remove(envFile)
		return fail(err)
	}
	if err := d.cfg.Tmux.SetSessionOption(m.Tmux, "@ra_session_id", id); err != nil {
		_ = d.cfg.Tmux.KillSession(m.Tmux)
		return fail(err)
	}
	// The tmux status line is not part of the CLI's experience.
	_ = d.cfg.Tmux.SetSessionOption(m.Tmux, "status", "off")

	s, err := newSession(d, dir, m, d.terminal(&m), log)
	if err != nil {
		_ = d.cfg.Tmux.KillSession(m.Tmux)
		return fail(err)
	}
	s.noteShellLaunch()
	if err := s.emit(&pb.Event{Time: timestamppb.Now(), Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{
		Status: pb.SessionStatus_SESSION_STATUS_STARTING,
	}}}); err != nil {
		_ = d.cfg.Tmux.KillSession(m.Tmux)
		return fail(err)
	}
	var acceptedErr error
	if strings.TrimSpace(req.Prompt) != "" || len(images) > 0 {
		// Queued like any message: it is written once the CLI shows its
		// input box, after startup prompts are answered. A session whose
		// first message cannot be accepted is not started.
		if _, err := s.enqueue(req.Prompt, firstRequest, images, origin); err != nil {
			if !queueAccepted(err) {
				_ = d.cfg.Tmux.KillSession(m.Tmux)
				return fail(err)
			}
			// The prompt and its images belong to this live session now.
			// Register and supervise it even though its notification failed.
			acceptedErr = fmt.Errorf("session %s already created; first %w", id, err)
		}
	}
	s.mu.Lock()
	s.meta.Starting = false
	started := s.meta
	s.mu.Unlock()
	if err := started.save(dir); err != nil {
		slog.Error("save session", "session", id, "err", errClass(err))
	}
	d.mu.Lock()
	d.sessions[id] = s
	d.mu.Unlock()
	s.start(d.ctx)
	slog.Info("created session", "session", id)
	return s, acceptedErr
}

// noteLimits keeps the newest usage limits Codex reported.
func (d *Daemon) noteLimits(u *pb.TokenUsage, at time.Time) {
	if u.PrimaryLimit == nil && u.SecondaryLimit == nil {
		return
	}
	d.mu.Lock()
	if at.After(d.limitsAt) || d.limits == nil {
		d.limits, d.limitsAt = u, at
	}
	d.mu.Unlock()
}

// MachineMeta describes this machine for clients.
func (d *Daemon) MachineMeta() *pb.MachineMeta {
	host, _ := os.Hostname()
	home, _ := os.UserHomeDir()
	cx := d.agents[pb.Agent_AGENT_CODEX]
	m := &pb.MachineMeta{Hostname: host, Os: runtime.GOOS, Home: home, CodexVersion: cx.Version(), CodexVerified: d.verified(cx)}
	m.Agents = d.installed()
	m.Capabilities = d.capabilities()
	m.ProtocolVersion = protocol.Version
	d.mu.Lock()
	m.Quotas = d.quotaEntriesLocked()
	sessions := make([]*session, 0, len(d.sessions))
	for _, s := range d.sessions {
		sessions = append(sessions, s)
	}
	d.mu.Unlock()
	m.Projects = projectSummaries(sessions)
	return m
}

// projectSummaries counts the sessions of each working directory: how
// many ended, when the newest was created and when one last ended. It
// reads only what changes when a session starts or ends, so the machine's
// metadata is not sent anew as sessions merely run.
func projectSummaries(sessions []*session) []*pb.ProjectSummary {
	byPath := map[string]*pb.ProjectSummary{}
	for _, s := range sessions {
		s.mu.Lock()
		cwd, created, exited, ended := s.meta.Cwd, s.meta.CreatedAt.UnixMilli(), s.status == pb.SessionStatus_SESSION_STATUS_EXITED, s.ordering.endedAt
		s.mu.Unlock()
		p := byPath[cwd]
		if p == nil {
			p = &pb.ProjectSummary{Path: cwd}
			byPath[cwd] = p
		}
		p.LatestCreatedMs = max(p.LatestCreatedMs, created)
		if exited {
			p.Ended++
			if ended != nil {
				p.LatestEndedMs = max(p.LatestEndedMs, ended.AsTime().UnixMilli())
			}
		}
	}
	out := make([]*pb.ProjectSummary, 0, len(byPath))
	for _, p := range byPath {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// installed lists the CLIs on this machine and their versions.
func (d *Daemon) installed() []*pb.AgentInfo {
	var out []*pb.AgentInfo
	for _, k := range []pb.Agent{pb.Agent_AGENT_CODEX, pb.Agent_AGENT_CLAUDE} {
		a := d.agents[k]
		if a == nil || a.Bin() == "" {
			continue
		}
		info := &pb.AgentInfo{Agent: k, Version: a.Version(), Verified: d.verified(a)}
		info.DefaultMode, info.DefaultModeSource, info.DefaultModeVaries = d.defaultMode(a, "")
		out = append(out, info)
	}
	return out
}

// session finds a session by full ID or unique prefix.
func (d *Daemon) session(id string) (*session, error) {
	if id == "" {
		return nil, fmt.Errorf("session ID is required")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s, ok := d.sessions[id]; ok {
		return s, nil
	}
	var found *session
	for sid, s := range d.sessions {
		if strings.HasPrefix(sid, id) {
			if found != nil {
				return nil, fmt.Errorf("session prefix %q is ambiguous", id)
			}
			found = s
		}
	}
	if found == nil {
		return nil, fmt.Errorf("session %s: %w", id, errNotFound)
	}
	return found, nil
}

func (d *Daemon) list(includeExited bool) []*pb.Session {
	d.mu.Lock()
	all := make([]*session, 0, len(d.sessions))
	for _, s := range d.sessions {
		all = append(all, s)
	}
	d.mu.Unlock()
	var out []*pb.Session
	for _, s := range all {
		info := s.info()
		if !includeExited && info.Status == pb.SessionStatus_SESSION_STATUS_EXITED {
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.AsTime().Before(out[j].CreatedAt.AsTime()) })
	return out
}

// ownEnv sets the variables remote-agent itself needs in a session:
// which session it is, and the executable the hooks run.
func (d *Daemon) ownEnv(env map[string]string, id string) {
	env["RA_SESSION"] = id
	env["RA_EXE"] = d.cfg.Exe
	env["REMOTE_AGENT_HOME"] = paths.Dir()
}

// cliEnv sets the variables a session's CLI needs: which session it is,
// the executable the hooks run, and the CLI's own switches.
func (d *Daemon) cliEnv(env map[string]string, m *meta) {
	d.ownEnv(env, m.ID)
	if m.Agent == pb.Agent_AGENT_CLAUDE {
		env["CLAUDE_CODE_DISABLE_CRON"] = "1"
		if m.StandardContext {
			env[claude.StandardContextEnv] = "1"
		}
	}
}

// shellSessionEnv is the environment a client-created session's login
// shell starts with: a new terminal's, then agent.env, then the shell
// computes the rest. The daemon's PATH deliberately stays out of it.
func shellSessionEnv(agentEnv map[string]string) map[string]string {
	env := shellStartEnv(loginShell())
	for k, v := range agentEnv {
		env[k] = v
	}
	return env
}

// shellCmd is what a client-created session's login shell runs: the
// executable the user configured for the CLI, or its name for the shell
// to resolve — the same command the user would type.
func (d *Daemon) shellCmd(kind pb.Agent) string {
	if kind == pb.Agent_AGENT_CLAUDE {
		return d.cfg.ClaudeCmd
	}
	return d.cfg.CodexCmd
}

// ptyHostArgv is the command that runs a session's CLI in its tmux pane:
// pty-host wrapping either the CLI itself (bin) or the user's login shell.
func (d *Daemon) ptyHostArgv(dir string, m *meta, bin string, args []string, envFile string) []string {
	argv := []string{d.cfg.Exe, "pty-host", "--socket", paths.PTYSocket(m.ID), "--env-file", envFile}
	if !m.Shell {
		argv = append(argv, "--", bin)
		return append(argv, args...)
	}
	// The login shell starts from exactly envFile (--clean-env): the
	// daemon's own environment, with its outdated PATH, must not leak in.
	shell := loginShell()
	slog.Info("starting a session through the login shell", "session", m.ID, "agent", d.adapter(m.Agent).Name(), "shell", shell)
	argv = append(argv, "--clean-env", "--")
	return append(argv, shellLaunchArgv(shell, dir, d.shellCmd(m.Agent), args)...)
}

// learnCLI records the CLI a login-shell launch or a terminal's PATH
// resolved, so that version checks, the installed list and doctor follow
// what sessions actually run rather than the daemon's own PATH at the time
// it started. The shell reports a plain name when it resolved an alias or
// function instead of a file; that is not something the daemon can use.
func (d *Daemon) learnCLI(kind pb.Agent, bin string) {
	ag, ok := d.agents[kind]
	if !ok || !filepath.IsAbs(bin) {
		return
	}
	if st, err := os.Stat(bin); err != nil || st.IsDir() {
		return
	}
	if ag.Bin() == bin {
		return
	}
	ag.SetBin(bin)
	slog.Info("a session resolved its CLI", "agent", ag.Name(), "bin", bin)
}

// terminalCLI is the CLI a session started from a terminal runs: what
// typing the CLI's name in that terminal would start, found in path, the
// PATH of the session's environment. A CLI the user configured by path
// runs from that path. When path is empty or has no such command, the
// session does not start: the daemon's own CLI is not what the terminal
// would run. What it finds is learned like a login shell's report, so that
// version checks follow what sessions run.
func (d *Daemon) terminalCLI(ag agent.Adapter, path, where string) (string, error) {
	cmd := d.shellCmd(ag.Kind())
	if cmd == "" || strings.ContainsRune(cmd, '/') {
		return ag.Bin(), nil
	}
	bin := findInPath(cmd, path)
	if bin == "" {
		variable := "REMOTE_AGENT_CODEX"
		if ag.Kind() == pb.Agent_AGENT_CLAUDE {
			variable = "REMOTE_AGENT_CLAUDE"
		}
		return "", fmt.Errorf("%s was not found in %s; install it there or set %s", cmd, where, variable)
	}
	d.learnCLI(ag.Kind(), bin)
	return bin, nil
}

// findInPath looks name up in path the way a shell does: the first
// directory, in order, holding an executable regular file of that name.
// Empty and relative entries are skipped. exec.LookPath cannot be used: it
// reads this process's PATH, not the session's.
func findInPath(name, path string) string {
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		file := filepath.Join(dir, name)
		if st, err := os.Stat(file); err == nil && st.Mode().IsRegular() && unix.Access(file, unix.X_OK) == nil {
			return file
		}
	}
	return ""
}

// skipEnv lists variables that belong to the caller's terminal rather than
// to the CLI process.
var skipEnv = map[string]bool{
	"TMUX": true, "TMUX_PANE": true, "TERM": true, "TERM_PROGRAM": true, "TERM_PROGRAM_VERSION": true,
	"TERM_SESSION_ID": true, "COLUMNS": true, "LINES": true, "SHLVL": true, "PWD": true, "OLDPWD": true,
	"_": true, "RA_SESSION": true, "RA_EXE": true, "COLORTERM": true, "ITERM_SESSION_ID": true,
}

func sessionEnv(fromClient, extra map[string]string) map[string]string {
	env := map[string]string{}
	for k, v := range fromClient {
		if !skipEnv[k] && k != "" && !strings.ContainsAny(k, "=\x00") && !strings.Contains(v, "\x00") {
			env[k] = v
		}
	}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

// startEnv keeps the session's environment in env.json, for resuming, and
// writes the copy the PTY host reads and deletes. The environment goes
// through private files, not tmux's command line, where every local user
// could read secrets such as API keys.
func startEnv(dir string, env map[string]string) (string, error) {
	if err := writeEnvFile(filepath.Join(dir, "env.json"), env); err != nil {
		return "", err
	}
	start := filepath.Join(dir, "env.start.json")
	return start, writeEnvFile(start, env)
}

func readEnvFile(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	return env, json.Unmarshal(b, &env)
}

// writeEnvFile stores env, readable by the owner only.
func writeEnvFile(path string, env map[string]string) error {
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return writeFileSync(path, b)
}

var errRunning = errors.New("the session is still running")

// errUnsettled: the messages queued when the session exited have no
// logged outcome yet (settleExited).
var errUnsettled = errors.New("what became of the session's queued messages could not be recorded; it is not resumed until it can be")

// resume starts the CLI again for an exited session, in its directory and
// tmux session name, continuing the same conversation. The session keeps
// its ID and history; the CLI appends to the same transcript, which is
// read on from where it stopped.
func (d *Daemon) resume(id string) (*session, error) {
	return d.resumeContext(context.Background(), id)
}

func (d *Daemon) resumeContext(ctx context.Context, id string) (*session, error) {
	s, err := d.exact(id)
	if err != nil {
		return nil, err
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.ctlMu.Lock()
	defer s.ctlMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	m := s.meta
	turnContext := s.context
	s.mu.Unlock()
	if !m.Exited {
		return nil, errRunning
	}
	if d.cfg.Tmux.HasSession(m.Tmux) {
		return nil, errRunning
	}
	if err := s.waitForPTYHostGone(ctx); err != nil {
		return nil, fmt.Errorf("cannot resume while the previous pty-host may still be running: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.resumeMu.Lock()
	defer d.resumeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	m = s.meta
	turnContext = s.context
	s.mu.Unlock()
	if !m.Exited {
		return nil, errRunning
	}
	if d.cfg.Tmux.HasSession(m.Tmux) {
		return nil, errRunning
	}
	// Nothing queued before it exited is sent now. While their outcomes
	// cannot be logged they stay queued, and a started session would be
	// sent them.
	if !s.settleExited() {
		return nil, errUnsettled
	}
	if st, err := contextio.Stat(ctx, m.Cwd); err != nil || !st.IsDir() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("directory %s is gone", m.Cwd)
	}
	if s.ag.Bin() == "" {
		return nil, fmt.Errorf("%s is not installed on this machine", s.ag.Name())
	}
	cliArgs, err := s.ag.Argv(&agent.Launch{
		Dir: s.dir, Cwd: m.Cwd, Args: m.Args, SessionID: m.CodexSessionID, Transcript: m.RolloutPath, Resume: true, Context: turnContext, FullAccess: !m.NoFullAccess, Exe: d.cfg.Exe, RemoteSession: id,
	})
	if err != nil {
		return nil, err
	}
	// The same environment it started with: the caller's shell may have
	// provided credentials or proxies. A session that started through the
	// login shell computes its environment the same way again, so that
	// configuration changes since apply — that is why it starts that way.
	var env map[string]string
	var bin string
	if m.Shell {
		env = shellSessionEnv(d.cfg.Env)
		clearShellReport(s.dir)
	} else {
		if env, err = readEnvFile(filepath.Join(s.dir, "env.json")); err != nil {
			return nil, fmt.Errorf("read the session's environment: %w", err)
		}
		if bin, err = d.terminalCLI(s.ag, env["PATH"], "the session's saved PATH"); err != nil {
			return nil, err
		}
	}
	d.cliEnv(env, &m)
	envFile, err := startEnv(s.dir, env)
	if err != nil {
		return nil, err
	}
	argv := d.ptyHostArgv(s.dir, &m, bin, cliArgs, envFile)
	if err := d.cfg.Tmux.NewSession(tmux.NewSessionOptions{
		Name: m.Tmux, Dir: m.Cwd, Width: d.cfg.Width, Height: d.cfg.Height, Argv: argv,
	}); err != nil {
		os.Remove(envFile)
		return nil, err
	}
	if err := d.cfg.Tmux.SetSessionOption(m.Tmux, "@ra_session_id", id); err != nil {
		_ = d.cfg.Tmux.KillSession(m.Tmux)
		return nil, err
	}
	_ = d.cfg.Tmux.SetSessionOption(m.Tmux, "status", "off")

	s.mu.Lock()
	s.meta.Exited = false
	s.sawComposer = false
	s.done = make(chan struct{})
	m = s.meta
	if err := m.save(s.dir); err != nil {
		slog.Error("save session", "session", id, "err", errClass(err))
	}
	s.mu.Unlock()
	s.noteShellLaunch()
	commitErr := s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{
		Status: pb.SessionStatus_SESSION_STATUS_STARTING, Detail: "resumed",
	}}})
	s.start(d.ctx)
	slog.Info("resumed session", "session", id)
	if commitErr != nil {
		return nil, fmt.Errorf("session already resumed on the computer but could not be recorded: %w", commitErr)
	}
	return s, nil
}

// logIdle is how long a session's events stay in memory after they were
// last read. Most sessions have ended and are read once, when the daemon
// starts; keeping them all would grow with every session ever run.
const logIdle = 2 * time.Minute

func (d *Daemon) releaseLogsLoop(stop <-chan struct{}) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		d.mu.Lock()
		all := make([]*session, 0, len(d.sessions))
		for _, s := range d.sessions {
			all = append(all, s)
		}
		d.mu.Unlock()
		for _, s := range all {
			s.log.Release(logIdle)
		}
	}
}
