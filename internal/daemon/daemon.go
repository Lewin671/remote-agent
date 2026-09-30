// Package daemon manages AI coding CLI sessions running in tmux and serves
// the local API. What is particular to each CLI is behind agent.Adapter.
package daemon

import (
	"connectrpc.com/connect"
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

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/claude"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/tmux"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Config configures a Daemon.
type Config struct {
	// Exe is the remote-agent binary, used as pty-host and hook command.
	Exe string
	// CodexBin is the real codex executable.
	CodexBin string
	Tmux     *tmux.Client
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
	Width, Height   int           // size of new sessions until attached

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
	if c.Width == 0 {
		c.Width, c.Height = 160, 48
	}
	if c.CodexHome == "" && c.HistoryPath != "" {
		c.CodexHome = filepath.Dir(c.HistoryPath)
	}
}

// Daemon owns all sessions.
type Daemon struct {
	cfg    Config
	ctx    context.Context
	agents map[pb.Agent]agent.Adapter

	// uploads are images clients sent for messages.
	uploads *uploads

	mu       sync.Mutex
	sessions map[string]*session
	limits   *pb.TokenUsage // latest usage limits seen in any session

	settingsMu sync.Mutex
	cached     *pb.DaemonSettings

	// resumeMu makes resumes one at a time: two for the same session
	// would both pass the checks and share its startup files.
	resumeMu sync.Mutex
}

// New returns a daemon; call Run to start it.
func New(cfg Config) *Daemon {
	cfg.defaults()
	d := &Daemon{cfg: cfg, sessions: map[string]*session{}, agents: map[pb.Agent]agent.Adapter{}, uploads: newUploads()}
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

	d.ctx = ctx
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
	d.uploads.reconcile()
	go d.uploads.gcLoop(ctx.Done())
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

	mux := http.NewServeMux()
	mux.Handle(remoteagentv1connect.NewLocalServiceHandler(&handler{d: d}))
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
		s := newSession(d, dir, *m, d.terminal(m), log)
		d.sessions[m.ID] = s
		s.cancelStaleApprovals()
		alive := live[m.Tmux+"\x00"+m.ID]
		if m.Exited && alive {
			// Marked exited while still running, as when an earlier daemon
			// misread tmux; stopping or ending kills the tmux session.
			s.meta.Exited = false
			if err := s.meta.save(dir); err != nil {
				slog.Error("save session", "session", m.ID, "err", errClass(err))
			}
			s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{
				Status: pb.SessionStatus_SESSION_STATUS_STARTING, Detail: "reattached",
			}}})
		} else if m.Exited {
			continue
		}
		if !alive {
			s.markExited("tmux session gone")
			continue
		}
		slog.Info("reattached session", "session", m.ID, "tmux", m.Tmux)
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
	pending := map[string]bool{}
	var order []string
	s.log.Each(func(e *pb.Event) {
		switch k := e.Kind.(type) {
		case *pb.Event_ApprovalRequested:
			pending[k.ApprovalRequested.ApprovalId] = true
			order = append(order, k.ApprovalRequested.ApprovalId)
		case *pb.Event_ApprovalResolved:
			delete(pending, k.ApprovalResolved.ApprovalId)
		}
	})
	for _, id := range order {
		if pending[id] {
			s.emit(&pb.Event{Kind: &pb.Event_ApprovalResolved{ApprovalResolved: &pb.ApprovalResolved{
				ApprovalId: id, Decision: pb.ApprovalDecision_APPROVAL_DECISION_CANCELED, Message: "daemon restarted",
			}}})
		}
	}
}

// create starts a new CLI session in tmux.
func (d *Daemon) create(req *pb.CreateSessionRequest) (*session, error) {
	return d.createBy(req, originLocal)
}

// createBy is create on behalf of origin, who sent the first message's
// images.
func (d *Daemon) createBy(req *pb.CreateSessionRequest, origin *pb.Origin) (*session, error) {
	cwd := req.Cwd
	if cwd == "" {
		return nil, errors.New("cwd is required")
	}
	if !filepath.IsAbs(cwd) {
		return nil, errors.New("cwd must be an absolute path")
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
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
	// Full access needs a client user's say-so every time, also when it
	// is only the default the session would get here. At the computer the
	// user's own configuration stands.
	remote := origin != nil && origin.Kind != pb.OriginKind_ORIGIN_KIND_LOCAL
	if remote && req.Mode == pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED && !req.ConfirmFullAccess {
		if m, src, _ := d.defaultMode(ag, cwd); m == pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS && src != pb.DefaultModeSource_DEFAULT_MODE_SOURCE_UNSPECIFIED {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s starts in full access here by default; confirm it or choose another mode", ag.Name()))
		}
	}
	args, err := d.sessionArgs(ag, req)
	if err != nil {
		return nil, err
	}
	id := newID(8)
	m := meta{ID: id, Agent: req.Agent, Cwd: cwd, Tmux: "ra-" + id[:8], Args: args, CreatedAt: time.Now().UTC(), NoFullAccess: !ag.FullAccess(args)}
	dir := paths.SessionDir(id)
	if err := paths.EnsureDir(dir); err != nil {
		return nil, err
	}
	launch := &agent.Launch{Dir: dir, Args: args}
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

	env := sessionEnv(req.Env, d.cfg.Env)
	d.ownEnv(env, id)
	envFile, err := startEnv(dir, env)
	if err != nil {
		log.Close()
		return nil, err
	}
	// The first message's images move into the session before the CLI
	// starts: if they cannot, nothing starts.
	images, err := d.uploads.take(req.ImageIds, owner, id)
	if err != nil {
		os.Remove(envFile)
		log.Close()
		return nil, err
	}
	fail := func(err error) (*session, error) {
		d.uploads.untake(images, id)
		log.Close()
		_ = os.RemoveAll(dir) // a session that never started leaves nothing
		return nil, err
	}
	argv := []string{d.cfg.Exe, "pty-host", "--socket", paths.PTYSocket(id), "--env-file", envFile, "--", ag.Bin()}
	argv = append(argv, cliArgs...)
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

	s := newSession(d, dir, m, d.terminal(&m), log)
	s.emit(&pb.Event{Time: timestamppb.Now(), Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{
		Status: pb.SessionStatus_SESSION_STATUS_STARTING,
	}}})
	if strings.TrimSpace(req.Prompt) != "" || len(images) > 0 {
		// Queued like any message: it is written once the CLI shows its
		// input box, after startup prompts are answered. A session whose
		// first message cannot be accepted is not started.
		if _, err := s.enqueue(req.Prompt, "", images, origin); err != nil {
			_ = d.cfg.Tmux.KillSession(m.Tmux)
			return fail(err)
		}
	}
	d.mu.Lock()
	d.sessions[id] = s
	d.mu.Unlock()
	s.start(d.ctx)
	slog.Info("created session", "session", id)
	return s, nil
}

// noteLimits keeps the newest usage limits Codex reported.
func (d *Daemon) noteLimits(u *pb.TokenUsage) {
	if u.PrimaryLimit == nil && u.SecondaryLimit == nil {
		return
	}
	d.mu.Lock()
	d.limits = u
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
	d.mu.Lock()
	if u := d.limits; u != nil {
		m.PrimaryLimit, m.SecondaryLimit, m.PlanType = u.PrimaryLimit, u.SecondaryLimit, u.PlanType
	}
	d.mu.Unlock()
	return m
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
	if h := os.Getenv("REMOTE_AGENT_HOME"); h != "" {
		env["REMOTE_AGENT_HOME"] = h
	}
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
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

var errRunning = errors.New("the session is still running")

// resume starts the CLI again for an exited session, in its directory and
// tmux session name, continuing the same conversation. The session keeps
// its ID and history; the CLI appends to the same transcript, which is
// read on from where it stopped.
func (d *Daemon) resume(id string) (*session, error) {
	d.resumeMu.Lock()
	defer d.resumeMu.Unlock()
	s, err := d.exact(id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	m := s.meta
	ctx := s.context
	s.mu.Unlock()
	if !m.Exited {
		return nil, errRunning
	}
	if d.cfg.Tmux.HasSession(m.Tmux) {
		return nil, errRunning
	}
	if st, err := os.Stat(m.Cwd); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("directory %s is gone", m.Cwd)
	}
	if s.ag.Bin() == "" {
		return nil, fmt.Errorf("%s is not installed on this machine", s.ag.Name())
	}
	cliArgs, err := s.ag.Argv(&agent.Launch{
		Dir: s.dir, Args: m.Args, SessionID: m.CodexSessionID, Transcript: m.RolloutPath, Resume: true, Context: ctx, FullAccess: !m.NoFullAccess,
	})
	if err != nil {
		return nil, err
	}
	// The same environment it started with: the caller's shell may have
	// provided credentials or proxies. Older sessions did not keep it.
	env, err := readEnvFile(filepath.Join(s.dir, "env.json"))
	if err != nil {
		env = sessionEnv(nil, d.cfg.Env)
	}
	d.ownEnv(env, id)
	envFile, err := startEnv(s.dir, env)
	if err != nil {
		return nil, err
	}
	argv := []string{d.cfg.Exe, "pty-host", "--socket", paths.PTYSocket(id), "--env-file", envFile, "--", s.ag.Bin()}
	argv = append(argv, cliArgs...)
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
	s.mu.Unlock()
	if err := m.save(s.dir); err != nil {
		slog.Error("save session", "session", id, "err", errClass(err))
	}
	s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{
		Status: pb.SessionStatus_SESSION_STATUS_STARTING, Detail: "resumed",
	}}})
	s.start(d.ctx)
	slog.Info("resumed session", "session", id)
	return s, nil
}
