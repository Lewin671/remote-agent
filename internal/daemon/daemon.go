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

	// resumeMu makes resumes one at a time: two for the same session
	// would both pass the checks and share its startup files.
	resumeMu sync.Mutex

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
		alive := live[m.Tmux+"\x00"+m.ID]
		if m.Exited && alive {
			// Marked exited while still running, as when an earlier daemon
			// misread tmux; stopping or ending kills the tmux session.
			s.meta.Exited = false
			if err := s.meta.save(dir); err != nil {
				slog.Error("save session", "session", m.ID, "err", err)
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
	d.ownEnv(env, id)
	envFile, err := startEnv(dir, env)
	if err != nil {
		log.Close()
		return nil, err
	}
	argv := []string{d.cfg.Exe, "pty-host", "--socket", paths.PTYSocket(id), "--env-file", envFile, "--", d.cfg.CodexBin}
	argv = append(argv, codex.Args(args)...)
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

// ownEnv sets the variables remote-agent itself needs in a session:
// which session it is, and the executable the hooks run (see codex.Args).
func (d *Daemon) ownEnv(env map[string]string, id string) {
	env["RA_SESSION"] = id
	env["RA_EXE"] = d.cfg.Exe
	if h := os.Getenv("REMOTE_AGENT_HOME"); h != "" {
		env["REMOTE_AGENT_HOME"] = h
	}
}

// skipEnv lists variables that belong to the caller's terminal rather than
// to the Codex process.
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

// valueFlags are codex options that take a value.
var valueFlags = map[string]bool{
	"-c": true, "--config": true, "-m": true, "--model": true, "-p": true, "--profile": true,
	"-s": true, "--sandbox": true, "-a": true, "--ask-for-approval": true, "-C": true, "--cd": true,
	"--add-dir": true, "-i": true, "--image": true, "--enable": true, "--disable": true, "--local-provider": true,
}

// approvalFlags are codex options that set the approval mode.
var approvalFlags = []string{"-s", "--sandbox", "-a", "--ask-for-approval", "--full-auto", "--dangerously-bypass-approvals-and-sandbox", "--yolo"}

// flagArgs keeps the options in codex arguments, except those in drop,
// and drops positional ones, which for resume would be read as a session
// ID or prompt.
func flagArgs(args []string, drop map[string]bool) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, _, inline := strings.Cut(a, "=")
		n := 1
		if !inline && valueFlags[name] && i+1 < len(args) {
			n = 2
		}
		if !drop[name] {
			out = append(out, args[i:i+n]...)
		}
		i += n - 1
	}
	return out
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

// resume starts Codex again for an exited session, in its directory and
// tmux session name, continuing the same Codex conversation. The session
// keeps its ID and history; Codex appends to the same rollout, which is
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
	// Start it with its original options, minus positional arguments such
	// as a first prompt, then the model, effort and approval mode it last
	// ran with, which may have been changed since.
	// Codex refuses a repeated -m, and its approval flags win over -c, so
	// the options overridden are dropped from the original ones first.
	var over []string
	drop := map[string]bool{}
	if ctx != nil {
		if ctx.Model != "" && safeValue.MatchString(ctx.Model) {
			over = append(over, "-m", ctx.Model)
			drop["-m"], drop["--model"] = true, true
		}
		if _, ok := effortLabels[ctx.Effort]; ok {
			over = append(over, "-c", fmt.Sprintf("model_reasoning_effort=%q", ctx.Effort))
		}
		if ma := modeArgs(exactMode(ctx)); ma != nil {
			over = append(over, ma...)
			for _, f := range approvalFlags {
				drop[f] = true
			}
		}
	}
	args := append(flagArgs(m.Args, drop), over...)
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
	argv := []string{d.cfg.Exe, "pty-host", "--socket", paths.PTYSocket(id), "--env-file", envFile, "--", d.cfg.CodexBin}
	if m.CodexSessionID != "" {
		argv = append(argv, "resume")
		argv = append(argv, codex.Args(args)...)
		argv = append(argv, m.CodexSessionID)
	} else {
		// Nothing was ever sent: there is no conversation to resume.
		argv = append(argv, codex.Args(args)...)
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
// exactMode is the approval mode whose settings the turn context has
// exactly, or UNSPECIFIED. Unlike contextMode it does not round, so that
// resuming never loosens a sandbox, e.g. a read-only one.
func exactMode(c *pb.TurnContext) pb.ApprovalMode {
	m := contextMode(c)
	want := fmt.Sprintf("-c|approvals_reviewer=%q|-c|approval_policy=%q|-c|sandbox_mode=%q", c.ApprovalsReviewer, c.ApprovalPolicy, c.Sandbox)
	if strings.Join(modeArgs(m), "|") != want {
		return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
	}
	return m
}

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
