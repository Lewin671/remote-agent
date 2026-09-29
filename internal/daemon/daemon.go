// Package daemon manages Codex sessions running in tmux and serves the
// local API.
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

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
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
	// Env is added to every Codex session, e.g. proxy variables.
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
	cfg Config
	ctx context.Context

	mu       sync.Mutex
	sessions map[string]*session
	limits   *pb.TokenUsage // latest usage limits seen in any session

	settingsMu sync.Mutex
	cached     *pb.DaemonSettings

	versionOnce sync.Once
	version     string
}

// New returns a daemon; call Run to start it.
func New(cfg Config) *Daemon {
	cfg.defaults()
	return &Daemon{cfg: cfg, sessions: map[string]*session{}}
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
	if v := d.codexVersion(); v != "" && !d.codexVerified() {
		slog.Warn("this Codex version has not been checked with remote-agent; run `remote-agent doctor`", "codex", v)
	}
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
			slog.Warn("skip session", "dir", dir, "err", err)
			continue
		}
		log, err := eventlog.Open(filepath.Join(dir, "events.jsonl"))
		if err != nil {
			slog.Warn("skip session", "dir", dir, "err", err)
			continue
		}
		s := newSession(d, dir, *m, d.terminal(m), log)
		d.sessions[m.ID] = s
		s.cancelStaleApprovals()
		if m.Exited {
			continue
		}
		if !live[m.Tmux+"\x00"+m.ID] {
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

// create starts a new Codex session in tmux.
func (d *Daemon) create(req *pb.CreateSessionRequest) (*session, error) {
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
	args, err := d.sessionArgs(req)
	if err != nil {
		return nil, err
	}
	id := newID(8)
	m := meta{ID: id, Cwd: cwd, Tmux: "ra-" + id[:8], Args: args, CreatedAt: time.Now().UTC()}
	dir := paths.SessionDir(id)
	if err := paths.EnsureDir(dir); err != nil {
		return nil, err
	}
	if err := m.save(dir); err != nil {
		return nil, err
	}
	log, err := eventlog.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, err
	}

	env := sessionEnv(req.Env, d.cfg.Env)
	env["RA_SESSION"] = id
	if h := os.Getenv("REMOTE_AGENT_HOME"); h != "" {
		env["REMOTE_AGENT_HOME"] = h
	}
	// The environment goes through a private file, not tmux's command
	// line, where every local user could read secrets such as API keys.
	envFile := filepath.Join(dir, "env.json")
	if err := writeEnvFile(envFile, env); err != nil {
		log.Close()
		return nil, err
	}
	hookCmd := shellQuote(d.cfg.Exe) + " hook codex"
	argv := []string{d.cfg.Exe, "pty-host", "--socket", paths.PTYSocket(id), "--env-file", envFile, "--", d.cfg.CodexBin}
	argv = append(argv, codex.Args(hookCmd, args)...)
	err = d.cfg.Tmux.NewSession(tmux.NewSessionOptions{
		Name: m.Tmux, Dir: cwd, Width: d.cfg.Width, Height: d.cfg.Height, Argv: argv,
	})
	if err != nil {
		os.Remove(envFile)
		log.Close()
		return nil, err
	}
	if err := d.cfg.Tmux.SetSessionOption(m.Tmux, "@ra_session_id", id); err != nil {
		_ = d.cfg.Tmux.KillSession(m.Tmux)
		log.Close()
		return nil, err
	}
	// The tmux status line is not part of the Codex experience.
	_ = d.cfg.Tmux.SetSessionOption(m.Tmux, "status", "off")

	s := newSession(d, dir, m, d.terminal(&m), log)
	s.emit(&pb.Event{Time: timestamppb.Now(), Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{
		Status: pb.SessionStatus_SESSION_STATUS_STARTING,
	}}})
	d.mu.Lock()
	d.sessions[id] = s
	d.mu.Unlock()
	s.start(d.ctx)
	slog.Info("created session", "session", id)
	if strings.TrimSpace(req.Prompt) != "" {
		// Queued like any message: it is written once Codex shows its
		// input box, after startup prompts are answered.
		if _, err := s.send(req.Prompt, ""); err != nil {
			slog.Warn("queue first message", "session", id, "err", err)
		}
	}
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
	m := &pb.MachineMeta{Hostname: host, Os: runtime.GOOS, Home: home, CodexVersion: d.codexVersion(), CodexVerified: d.codexVerified()}
	d.mu.Lock()
	if u := d.limits; u != nil {
		m.PrimaryLimit, m.SecondaryLimit, m.PlanType = u.PrimaryLimit, u.SecondaryLimit, u.PlanType
	}
	d.mu.Unlock()
	return m
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

// skipEnv lists variables that belong to the caller's terminal rather than
// to the Codex process.
var skipEnv = map[string]bool{
	"TMUX": true, "TMUX_PANE": true, "TERM": true, "TERM_PROGRAM": true, "TERM_PROGRAM_VERSION": true,
	"TERM_SESSION_ID": true, "COLUMNS": true, "LINES": true, "SHLVL": true, "PWD": true, "OLDPWD": true,
	"_": true, "RA_SESSION": true, "COLORTERM": true, "ITERM_SESSION_ID": true,
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

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:@", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeEnvFile stores env for the PTY host, readable by the owner only.
// The PTY host deletes it once read.
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

// resume starts Codex again for an exited session, in its directory and
// tmux session name, continuing the same Codex conversation. The session
// keeps its ID and history; Codex appends to the same rollout, which is
// read on from where it stopped.
func (d *Daemon) resume(id string) (*session, error) {
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
	// Start it the way it last ran: model, effort and approval mode from
	// the latest turn context.
	var args []string
	if ctx != nil {
		if ctx.Model != "" && safeValue.MatchString(ctx.Model) {
			args = append(args, "-m", ctx.Model)
		}
		if _, ok := effortLabels[ctx.Effort]; ok {
			args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", ctx.Effort))
		}
		args = append(args, modeArgs(contextMode(ctx))...)
	}
	env := sessionEnv(nil, d.cfg.Env)
	env["RA_SESSION"] = id
	if h := os.Getenv("REMOTE_AGENT_HOME"); h != "" {
		env["REMOTE_AGENT_HOME"] = h
	}
	envFile := filepath.Join(s.dir, "env.json")
	if err := writeEnvFile(envFile, env); err != nil {
		return nil, err
	}
	hookCmd := shellQuote(d.cfg.Exe) + " hook codex"
	argv := []string{d.cfg.Exe, "pty-host", "--socket", paths.PTYSocket(id), "--env-file", envFile, "--", d.cfg.CodexBin}
	if m.CodexSessionID != "" {
		argv = append(argv, "resume")
		argv = append(argv, codex.Args(hookCmd, args)...)
		argv = append(argv, m.CodexSessionID)
	} else {
		// Nothing was ever sent: there is no conversation to resume.
		argv = append(argv, codex.Args(hookCmd, args)...)
	}
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
		slog.Error("save session", "session", id, "err", err)
	}
	s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{
		Status: pb.SessionStatus_SESSION_STATUS_STARTING, Detail: "resumed",
	}}})
	s.start(d.ctx)
	slog.Info("resumed session", "session", id)
	return s, nil
}

// contextMode is the approval mode a turn context shows; Plan mode is not
// kept across a restart.
func contextMode(c *pb.TurnContext) pb.ApprovalMode {
	switch {
	case c.Sandbox == "danger-full-access" && c.ApprovalPolicy == "never":
		return pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS
	case c.ApprovalsReviewer == "auto_review":
		return pb.ApprovalMode_APPROVAL_MODE_AUTO
	case c.ApprovalsReviewer == "user":
		return pb.ApprovalMode_APPROVAL_MODE_ASK
	}
	return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
}
