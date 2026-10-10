package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/client"
	"github.com/Lewin671/remote-agent/internal/daemon"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/ptyhost"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/tmux"
	"google.golang.org/protobuf/encoding/protojson"
)

func tmuxClient() *tmux.Client { return &tmux.Client{Socket: paths.TmuxSocketName()} }

func dial() remoteagentv1connect.LocalServiceClient { return client.New(paths.Socket()) }

// explain turns a connection failure into advice.
func explain(err error) error {
	if connect.CodeOf(err) == connect.CodeUnavailable {
		return fmt.Errorf("cannot reach the daemon at %s; start it with `remote-agent daemon`", paths.Socket())
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return errors.New(ce.Message())
	}
	return err
}

func runDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	approvalTimeout := fs.Duration("approval-timeout", 10*time.Minute, "deny a permission request nobody answers within this time")
	codexBin := fs.String("codex", os.Getenv("REMOTE_AGENT_CODEX"), "codex executable (default: codex in PATH)")
	claudeBin := fs.String("claude", os.Getenv("REMOTE_AGENT_CLAUDE"), "claude executable (default: claude in PATH, if any)")
	fs.Parse(args)

	if max := time.Duration(agent.HookTimeoutSeconds-60) * time.Second; *approvalTimeout > max {
		return fmt.Errorf("--approval-timeout must be at most %s", max)
	}
	ensureUTF8()
	cfg, err := daemonConfig(*codexBin, *claudeBin, tmuxClient())
	if err != nil {
		return err
	}
	cfg.ApprovalTimeout = *approvalTimeout
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var d *daemon.Daemon
	cfg.Started = func(ctx context.Context) { relay.New(backend{d}).Run(ctx) }
	d = daemon.New(cfg)
	slog.Info("starting daemon", "codex", cfg.CodexBin, "claude", cfg.ClaudeBin, "approval_timeout", *approvalTimeout)
	return d.Run(ctx)
}

// daemonConfig finds the CLIs, this binary and the CLIs' files. Codex is
// required; Claude Code is optional.
func daemonConfig(codexBin, claudeBin string, tc *tmux.Client) (daemon.Config, error) {
	if _, err := tc.CheckVersion(); err != nil {
		return daemon.Config{}, err
	}
	if codexBin == "" {
		p, err := exec.LookPath("codex")
		if err != nil {
			return daemon.Config{}, errors.New("codex not found in PATH; pass --codex")
		}
		codexBin = p
	}
	exe, err := os.Executable()
	if err != nil {
		return daemon.Config{}, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return daemon.Config{}, err
	}
	env, err := agentEnv()
	if err != nil {
		return daemon.Config{}, err
	}
	home, _ := os.UserHomeDir()
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	if claudeBin == "" {
		// The path in PATH, not the versioned file it links to, which
		// Claude Code's updates replace.
		claudeBin, _ = exec.LookPath("claude")
	}
	claudeHome := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeHome == "" {
		claudeHome = filepath.Join(home, ".claude")
	}
	return daemon.Config{
		Exe:         exe,
		CodexBin:    codexBin,
		CodexHome:   codexHome,
		ClaudeBin:   claudeBin,
		ClaudeHome:  claudeHome,
		Tmux:        tc,
		HistoryPath: filepath.Join(codexHome, "history.jsonl"),
		Env:         env,
	}, nil
}

// readEnvFile reads KEY=VALUE lines; a missing file is not an error.
// agentEnv reads the variables every CLI session gets, such as proxies:
// ~/.remote-agent/agent.env, or codex.env from before Claude Code support.
func agentEnv() (map[string]string, error) {
	path := filepath.Join(paths.Dir(), "agent.env")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		path = filepath.Join(paths.Dir(), "codex.env")
	}
	return readEnvFile(path)
}

func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	env := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, n)
		}
		env[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return env, sc.Err()
}

func environ() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

// runNew starts a session without attaching: Codex, or the CLI named by
// a leading --agent.
func runNew(args []string) error {
	kind := pb.Agent_AGENT_CODEX
	if len(args) > 0 {
		name, value, inline := strings.Cut(args[0], "=")
		if name == "--agent" {
			rest := args[1:]
			if !inline {
				if len(rest) == 0 {
					return errors.New("usage: remote-agent new [--agent codex|claude] [args]")
				}
				value, rest = rest[0], rest[1:]
			}
			switch value {
			case "codex":
			case "claude":
				kind = pb.Agent_AGENT_CLAUDE
			default:
				return fmt.Errorf("unknown agent %q; use codex or claude", value)
			}
			args = rest
		}
	}
	return runAgent(kind, args, false)
}

// runAgent starts a session of the CLI in this directory, passing args to
// it unchanged, and attaches to it unless attachNow is false.
func runAgent(kind pb.Agent, args []string, attachNow bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	resp, err := dial().CreateSession(context.Background(), connect.NewRequest(&pb.CreateSessionRequest{
		Cwd: cwd, Args: args, Env: environ(), Agent: kind,
	}))
	if err != nil {
		return explain(err)
	}
	s := resp.Msg.Session
	if !attachNow {
		fmt.Println(s.Id)
		return nil
	}
	fmt.Fprintf(os.Stderr, "remote-agent: session %s (detach with the tmux prefix + d; the session keeps running)\n", s.Id)
	return attach(s.TmuxSession)
}

// attach replaces this process with tmux attach.
func attach(tmuxSession string) error {
	argv := tmuxClient().AttachArgv(tmuxSession)
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	var env []string
	for _, kv := range os.Environ() {
		// Attaching from inside another tmux is fine: it is a different
		// server, but tmux refuses unless TMUX is unset.
		if !strings.HasPrefix(kv, "TMUX=") {
			env = append(env, kv)
		}
	}
	return syscall.Exec(bin, argv, env)
}

func runList(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	all := fs.Bool("a", false, "include exited sessions")
	fs.Parse(args)
	resp, err := dial().ListSessions(context.Background(), connect.NewRequest(&pb.ListSessionsRequest{IncludeExited: *all}))
	if err != nil {
		return explain(err)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tAGENT\tSTATUS\tMODEL\tPENDING\tCREATED\tDIRECTORY")
	for _, s := range resp.Msg.Sessions {
		status := statusName(s.Status)
		if s.StatusDetail != "" {
			status += " (" + s.StatusDetail + ")"
		}
		model := strings.TrimSpace(s.Model + " " + s.Effort)
		if model == "" {
			model = "-"
		}
		var pending []string
		if s.PendingApprovals > 0 {
			pending = append(pending, fmt.Sprintf("%d approval", s.PendingApprovals))
		}
		if s.PendingQuestions > 0 {
			pending = append(pending, fmt.Sprintf("%d question", s.PendingQuestions))
		}
		if s.QueuedInputs > 0 {
			pending = append(pending, fmt.Sprintf("%d queued", s.QueuedInputs))
		}
		p := strings.Join(pending, ", ")
		if p == "" {
			p = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", shortID(s.Id), agentName(s.Agent), terminalText(status, false), terminalText(model, false), p,
			s.CreatedAt.AsTime().Local().Format("01-02 15:04"), terminalText(s.Cwd, false))
	}
	return w.Flush()
}

func statusName(s pb.SessionStatus) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "SESSION_STATUS_"))
}

func needArgs(args []string, n int, use string) error {
	if len(args) < n {
		return fmt.Errorf("usage: remote-agent %s", use)
	}
	return nil
}

func getSession(id string) (*pb.Session, error) {
	resp, err := dial().GetSession(context.Background(), connect.NewRequest(&pb.GetSessionRequest{SessionId: id}))
	if err != nil {
		return nil, explain(err)
	}
	return resp.Msg.Session, nil
}

func runAttach(args []string) error {
	if err := needArgs(args, 1, "attach <id>"); err != nil {
		return err
	}
	s, err := getSession(args[0])
	if err != nil {
		return err
	}
	if s.Status == pb.SessionStatus_SESSION_STATUS_EXITED {
		return fmt.Errorf("session %s has exited", s.Id)
	}
	return attach(s.TmuxSession)
}

func runSend(args []string) error {
	mode := pb.SendMode_SEND_MODE_STEER
	var rest []string
	for _, a := range args {
		if a == "--queue" && len(rest) < 2 {
			mode = pb.SendMode_SEND_MODE_QUEUE
			continue
		}
		rest = append(rest, a)
	}
	args = rest
	if err := needArgs(args, 2, "send [--queue] <id> <text>"); err != nil {
		return err
	}
	text := strings.Join(args[1:], " ")
	if text == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		text = strings.TrimRight(string(b), "\n")
	}
	id := randomID()
	resp, err := dial().Send(context.Background(), connect.NewRequest(&pb.SendRequest{
		SessionId: args[0], Text: text, RequestId: id, Mode: mode,
	}))
	if err != nil {
		return explain(err)
	}
	if resp.Msg.QueuePosition > 1 || mode == pb.SendMode_SEND_MODE_QUEUE {
		fmt.Printf("queued at position %d (request %s)\n", resp.Msg.QueuePosition, id)
	}
	return nil
}

// runCancel withdraws a message the model has not received.
func runCancel(args []string) error {
	if err := needArgs(args, 2, "cancel <id> <request-id>"); err != nil {
		return err
	}
	_, err := dial().CancelInput(context.Background(), connect.NewRequest(&pb.CancelInputRequest{SessionId: args[0], RequestId: args[1]}))
	return explain(err)
}

// runResumeInput sends a message paused by an interrupted turn.
func runResumeInput(args []string) error {
	if err := needArgs(args, 2, "resume-input <id> <request-id>"); err != nil {
		return err
	}
	_, err := dial().ResumeInput(context.Background(), connect.NewRequest(&pb.ResumeInputRequest{SessionId: args[0], RequestId: args[1]}))
	return explain(err)
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func runEvents(args []string) error {
	fs := flag.NewFlagSet("events", flag.ExitOnError)
	follow := fs.Bool("f", false, "keep printing new events")
	fs.BoolVar(follow, "follow", false, "keep printing new events")
	asJSON := fs.Bool("json", false, "print events as JSON lines")
	after := fs.Uint64("after", 0, "only events after this sequence number")
	id, rest := splitID(args)
	fs.Parse(rest)
	if id == "" {
		if fs.NArg() < 1 {
			return errors.New("usage: remote-agent events <id> [-f] [--json] [--after N]")
		}
		id = fs.Arg(0)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stream, err := dial().WatchEvents(ctx, connect.NewRequest(&pb.WatchEventsRequest{
		SessionId: id, AfterSeq: *after, Follow: *follow,
	}))
	if err != nil {
		return explain(err)
	}
	defer stream.Close()
	out := bufio.NewWriter(os.Stdout)
	for stream.Receive() {
		e := stream.Msg().Event
		if *asJSON {
			b, _ := protojson.Marshal(e)
			out.Write(b)
			out.WriteByte('\n')
		} else {
			out.WriteString(formatEvent(e))
		}
		out.Flush()
	}
	if err := stream.Err(); err != nil && ctx.Err() == nil {
		return explain(err)
	}
	return nil
}

// splitID lets the session ID come before or after flags.
func splitID(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

func runResolve(args []string, allow bool) error {
	fs := flag.NewFlagSet("deny", flag.ExitOnError)
	msg := fs.String("m", "", "reason shown to the model")
	always := fs.Bool("always", false, "with approve: also stop asking about requests like it")
	var pos []string
	for len(args) > 0 {
		if strings.HasPrefix(args[0], "-") {
			fs.Parse(args)
			args = fs.Args()
			continue
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	verb := "approve"
	if !allow {
		verb = "deny"
	}
	if err := needArgs(pos, 2, verb+" <id> <approval-id>"); err != nil {
		return err
	}
	_, err := dial().ResolveApproval(context.Background(), connect.NewRequest(&pb.ResolveApprovalRequest{
		SessionId: pos[0], ApprovalId: pos[1], Allow: allow, Message: *msg, Always: allow && *always,
	}))
	return explain(err)
}

func runAnswer(args []string) error {
	if err := needArgs(args, 3, "answer <id> <question-id> <option>[,<option>...]"); err != nil {
		return err
	}
	req := &pb.AnswerQuestionRequest{SessionId: args[0], QuestionId: args[1]}
	for _, f := range strings.Split(args[2], ",") {
		opt, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			return fmt.Errorf("option must be a number: %w", err)
		}
		req.Options = append(req.Options, int32(opt))
	}
	if len(req.Options) == 1 {
		req.Option, req.Options = req.Options[0], nil
	}
	_, err := dial().AnswerQuestion(context.Background(), connect.NewRequest(req))
	return explain(err)
}

func agentName(a pb.Agent) string {
	if a == pb.Agent_AGENT_CLAUDE {
		return "claude"
	}
	return "codex"
}

func runInterrupt(args []string) error {
	if err := needArgs(args, 1, "interrupt <id>"); err != nil {
		return err
	}
	resp, err := dial().Interrupt(context.Background(), connect.NewRequest(&pb.InterruptRequest{SessionId: args[0]}))
	if err != nil {
		return explain(err)
	}
	if !resp.Msg.Interrupted {
		fmt.Println("no turn is running")
	}
	return nil
}

func runStop(args []string) error {
	if err := needArgs(args, 1, "stop <id>"); err != nil {
		return err
	}
	_, err := dial().StopSession(context.Background(), connect.NewRequest(&pb.StopSessionRequest{SessionId: args[0]}))
	return explain(err)
}

// runHook is the hook entry point of Codex and Claude Code, which send the
// same payload fields. It forwards the payload to the daemon and prints
// the daemon's answer. It never fails the hook: when the daemon cannot be
// reached it prints nothing, which leaves the decision to the CLI's own
// prompt in the terminal.
func runHook(args []string) int {
	if len(args) < 1 || (args[0] != "codex" && args[0] != "claude") {
		fmt.Fprintln(os.Stderr, "usage: remote-agent hook codex|claude")
		return 2
	}
	sessionID := os.Getenv("RA_SESSION")
	payload, err := io.ReadAll(io.LimitReader(os.Stdin, 16<<20))
	if err != nil || sessionID == "" {
		return 0
	}
	var head agent.Hook
	_ = jsonUnmarshal(payload, &head)
	wait := 25 * time.Second
	retryFor := time.Duration(0)
	if head.HookEventName == "PermissionRequest" {
		wait = time.Duration(agent.HookTimeoutSeconds-30) * time.Second
		// Ride out a daemon restart.
		retryFor = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	c := dial()
	out, err := forwardHook(ctx, retryFor, time.Second, func(ctx context.Context) ([]byte, error) {
		resp, err := c.Hook(ctx, connect.NewRequest(&pb.HookRequest{SessionId: sessionID, Payload: payload}))
		if err != nil {
			return nil, err
		}
		return resp.Msg.Output, nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "remote-agent hook:", explain(err))
		return 0
	}
	os.Stdout.Write(out)
	return 0
}

// forwardHook makes the hook call, again every pause for up to retryFor
// while the daemon cannot be reached. The time runs from when the daemon
// was lost, not from the first call: an approval may have waited for
// minutes before the daemon restarted under it. A call that outlasted the
// pause was being served, so its failure starts the time anew.
func forwardHook(ctx context.Context, retryFor, pause time.Duration, call func(context.Context) ([]byte, error)) ([]byte, error) {
	var lost time.Time
	for {
		called := time.Now()
		out, err := call(ctx)
		if err == nil {
			return out, nil
		}
		if lost.IsZero() || time.Since(called) > pause {
			lost = time.Now()
		}
		if connect.CodeOf(err) != connect.CodeUnavailable || time.Since(lost) >= retryFor || ctx.Err() != nil {
			return nil, err
		}
		time.Sleep(pause)
	}
}

func runPTYHost(args []string) int {
	fs := flag.NewFlagSet("pty-host", flag.ContinueOnError)
	sock := fs.String("socket", "", "control socket path")
	envFile := fs.String("env-file", "", "JSON file with environment variables for the command; deleted once read")
	if err := fs.Parse(args); err != nil || *sock == "" || fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: remote-agent pty-host --socket PATH [--env-file PATH] -- command [args]")
		return 2
	}
	if *envFile != "" {
		if err := loadEnvFile(*envFile); err != nil {
			fmt.Fprintln(os.Stderr, "remote-agent pty-host:", err)
			time.Sleep(3 * time.Second)
			return 1
		}
	}
	code, err := ptyhost.Run(*sock, fs.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, "remote-agent pty-host:", err)
		// Keep the message visible if the pane is about to close.
		time.Sleep(3 * time.Second)
	}
	return code
}

// loadEnvFile sets the variables in a JSON file and deletes the file.
func loadEnvFile(path string) error {
	b, err := os.ReadFile(path)
	os.Remove(path)
	if err != nil {
		return err
	}
	env := map[string]string{}
	if err := json.Unmarshal(b, &env); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for k, v := range env {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}

// backend adapts the daemon to what the relay needs.
type backend struct{ *daemon.Daemon }

func (b backend) Image(sessionID, imageID string) relay.ImageUpload {
	u := b.Daemon.Image(sessionID, imageID)
	return relay.ImageUpload{Payload: u.Payload, Done: u.Done, Failed: u.Failed}
}

func (b backend) OpenTerminal(id string) (relay.Terminal, error) {
	t, err := b.Daemon.OpenTerminal(id)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ensureUTF8 gives the daemon a UTF-8 locale when it has none, as under
// launchd or systemd. tmux and Codex inherit it: without one, tmux
// replaces tabs and non-ASCII characters in its output with "_", which
// breaks reading sessions and screens.
func ensureUTF8() {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		v := os.Getenv(k)
		if v == "" {
			continue
		}
		if isUTF8Locale(v) {
			return
		}
		if k == "LC_ALL" {
			// It overrides LC_CTYPE.
			os.Setenv("LC_ALL", utf8Locale())
			return
		}
		break
	}
	os.Setenv("LC_CTYPE", utf8Locale())
}

func isUTF8Locale(v string) bool {
	v = strings.ToLower(v)
	return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
}

func utf8Locale() string {
	if runtime.GOOS == "darwin" {
		return "en_US.UTF-8"
	}
	return "C.UTF-8"
}
