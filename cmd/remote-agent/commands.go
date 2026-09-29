package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/client"
	"github.com/Lewin671/remote-agent/internal/codex"
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
	fs.Parse(args)

	if max := time.Duration(codex.HookTimeoutSeconds-60) * time.Second; *approvalTimeout > max {
		return fmt.Errorf("--approval-timeout must be at most %s", max)
	}
	tc := tmuxClient()
	if _, err := tc.CheckVersion(); err != nil {
		return err
	}
	if *codexBin == "" {
		p, err := exec.LookPath("codex")
		if err != nil {
			return errors.New("codex not found in PATH; pass --codex")
		}
		*codexBin = p
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	env, err := readEnvFile(filepath.Join(paths.Dir(), "codex.env"))
	if err != nil {
		return err
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		home, _ := os.UserHomeDir()
		codexHome = filepath.Join(home, ".codex")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var d *daemon.Daemon
	d = daemon.New(daemon.Config{
		Exe:             exe,
		CodexBin:        *codexBin,
		Tmux:            tc,
		HistoryPath:     filepath.Join(codexHome, "history.jsonl"),
		Env:             env,
		ApprovalTimeout: *approvalTimeout,
		Started:         func(ctx context.Context) { relay.New(d).Run(ctx) },
	})
	slog.Info("starting daemon", "codex", *codexBin, "approval_timeout", *approvalTimeout)
	return d.Run(ctx)
}

// readEnvFile reads KEY=VALUE lines; a missing file is not an error.
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

func runCodex(args []string, attachNow bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	resp, err := dial().CreateSession(context.Background(), connect.NewRequest(&pb.CreateSessionRequest{
		Cwd: cwd, Args: args, Env: environ(),
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
	fmt.Fprintln(w, "ID\tSTATUS\tMODEL\tPENDING\tCREATED\tDIRECTORY")
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
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Id[:12], status, model, p,
			s.CreatedAt.AsTime().Local().Format("01-02 15:04"), s.Cwd)
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
	if err := needArgs(args, 2, "send <id> <text>"); err != nil {
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
	resp, err := dial().Send(context.Background(), connect.NewRequest(&pb.SendRequest{
		SessionId: args[0], Text: text, RequestId: randomID(),
	}))
	if err != nil {
		return explain(err)
	}
	if resp.Msg.QueuePosition > 1 {
		fmt.Printf("queued at position %d\n", resp.Msg.QueuePosition)
	}
	return nil
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
		SessionId: pos[0], ApprovalId: pos[1], Allow: allow, Message: *msg,
	}))
	return explain(err)
}

func runAnswer(args []string) error {
	if err := needArgs(args, 3, "answer <id> <question-id> <option>"); err != nil {
		return err
	}
	opt, err := strconv.Atoi(args[2])
	if err != nil {
		return fmt.Errorf("option must be a number: %w", err)
	}
	_, err = dial().AnswerQuestion(context.Background(), connect.NewRequest(&pb.AnswerQuestionRequest{
		SessionId: args[0], QuestionId: args[1], Option: int32(opt),
	}))
	return explain(err)
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

// runHook is the Codex hook entry point. It forwards the payload to the
// daemon and prints the daemon's answer. It never fails the hook: when the
// daemon cannot be reached it prints nothing, which leaves the decision to
// Codex's own prompt in the terminal.
func runHook(args []string) int {
	if len(args) < 1 || args[0] != "codex" {
		fmt.Fprintln(os.Stderr, "usage: remote-agent hook codex")
		return 2
	}
	sessionID := os.Getenv("RA_SESSION")
	payload, err := io.ReadAll(io.LimitReader(os.Stdin, 16<<20))
	if err != nil || sessionID == "" {
		return 0
	}
	var head codex.HookPayload
	_ = jsonUnmarshal(payload, &head)
	wait := 25 * time.Second
	retryFor := time.Duration(0)
	if head.HookEventName == "PermissionRequest" {
		wait = time.Duration(codex.HookTimeoutSeconds-30) * time.Second
		// Ride out a daemon restart.
		retryFor = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	c := dial()
	start := time.Now()
	for {
		resp, err := c.Hook(ctx, connect.NewRequest(&pb.HookRequest{SessionId: sessionID, Payload: payload}))
		if err == nil {
			os.Stdout.Write(resp.Msg.Output)
			return 0
		}
		if connect.CodeOf(err) != connect.CodeUnavailable || time.Since(start) > retryFor || ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "remote-agent hook:", explain(err))
			return 0
		}
		time.Sleep(time.Second)
	}
}

func runPTYHost(args []string) int {
	fs := flag.NewFlagSet("pty-host", flag.ContinueOnError)
	sock := fs.String("socket", "", "control socket path")
	if err := fs.Parse(args); err != nil || *sock == "" || fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: remote-agent pty-host --socket PATH -- command [args]")
		return 2
	}
	code, err := ptyhost.Run(*sock, fs.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, "remote-agent pty-host:", err)
		// Keep the message visible if the pane is about to close.
		time.Sleep(3 * time.Second)
	}
	return code
}
