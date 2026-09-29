// Package daemon manages Codex sessions running in tmux and serves the
// local API.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
}

// Daemon owns all sessions.
type Daemon struct {
	cfg Config
	ctx context.Context

	mu       sync.Mutex
	sessions map[string]*session
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
	id := newID(8)
	m := meta{ID: id, Cwd: cwd, Tmux: "ra-" + id[:8], Args: req.Args, CreatedAt: time.Now().UTC()}
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
	hookCmd := shellQuote(d.cfg.Exe) + " hook codex"
	argv := []string{d.cfg.Exe, "pty-host", "--socket", paths.PTYSocket(id), "--", d.cfg.CodexBin}
	argv = append(argv, codex.Args(hookCmd, req.Args)...)
	err = d.cfg.Tmux.NewSession(tmux.NewSessionOptions{
		Name: m.Tmux, Dir: cwd, Width: d.cfg.Width, Height: d.cfg.Height, Env: env, Argv: argv,
	})
	if err != nil {
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
	slog.Info("created session", "session", id, "cwd", cwd)
	return s, nil
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
