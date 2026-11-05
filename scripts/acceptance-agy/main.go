// Real Antigravity acceptance through an isolated, encrypted local relay.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/antigravity"
	localclient "github.com/Lewin671/remote-agent/internal/client"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/updater"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func read(path string) string {
	b, e := os.ReadFile(path)
	must(e)
	return strings.TrimSpace(string(b))
}
func poll(ctx context.Context, what string, fn func() bool) error {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		if fn() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", what, ctx.Err())
		case <-ticker.C:
		}
	}
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL", err)
		os.Exit(1)
	}
}
func run() (runErr error) {
	dir := os.Getenv("RA_DEV_DIR")
	if dir == "" {
		return errors.New("RA_DEV_DIR is required")
	}
	server := read(filepath.Join(dir, "url"))
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		return errors.New("refusing a non-local dev relay")
	}
	preflight := len(os.Args) == 2 && os.Args[1] == "--preflight"
	imagesOnly := len(os.Args) == 2 && os.Args[1] == "--images-only"
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	auth, err := remoteclient.NewAuthority(ctx, server, read(filepath.Join(dir, "setup-token")))
	if err != nil {
		return err
	}
	_, ed, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	x, _, err := deviceauth.NewXKey()
	if err != nil {
		return err
	}
	machine, err := auth.Enroll(ctx, ed, x, deviceauth.Machine)
	if err != nil {
		return err
	}
	home := filepath.Join(dir, "agy-home")
	must(os.MkdirAll(home, 0700))
	must(updater.EnsureState(home))
	must(os.Setenv("REMOTE_AGENT_HOME", home))
	must(machine.Endpoint.Save(filepath.Join(home, "security-v3.db")))
	acct := &relay.Account{Server: server, Root: auth.Root, DeviceSeed: ed.Seed(), DeviceID: machine.ID, Security: &relay.SecurityConfig{Pin: auth.Pin, XPrivate: x}}
	must(acct.Save())
	machine.Endpoint.Close()
	phone, err := deviceauth.InitEndpoint("", auth.Pin, auth.States, auth.Proofs, auth.Member.ID, auth.Ed.Seed(), auth.X, auth.Lease)
	if err != nil {
		return err
	}
	defer phone.Close()
	client := remoteclient.NewClient(server, auth.Token, auth.Member.ID, phone)
	socket := read(filepath.Join(dir, "tmux-socket")) + "-agy"
	must(os.Setenv("REMOTE_AGENT_TMUX_SOCKET", socket))
	binary := filepath.Join(dir, "bin", "remote-agent")
	log, err := os.OpenFile(filepath.Join(dir, "agy-daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	var process *exec.Cmd
	start := func() {
		process = exec.Command(binary, "daemon", "--approval-timeout", "120s")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "NO_COLOR=") {
				process.Env = append(process.Env, entry)
			}
		}
		process.Stdout, process.Stderr = log, log
		must(process.Start())
	}
	stop := func() {
		if process != nil {
			_ = process.Process.Signal(os.Interrupt)
			_ = process.Wait()
			process = nil
		}
	}
	defer func() { stop(); _ = exec.Command("tmux", "-L", socket, "kill-server").Run() }()
	// Fingerprints only, never contents, verify that trust/model operations did
	// not persist to either user's settings. The normal CLI transcript is native.
	userHome, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	configs := []string{filepath.Join(userHome, ".gemini", "antigravity-cli", "settings.json"), filepath.Join(userHome, ".claude", "settings.json")}
	hashes := map[string][32]byte{}
	for _, path := range configs {
		b, _ := os.ReadFile(path)
		hashes[path] = sha256.Sum256(b)
	}
	defer func() {
		for _, path := range configs {
			b, _ := os.ReadFile(path)
			if sha256.Sum256(b) != hashes[path] {
				fmt.Println("FAIL user configuration changed")
				runErr = errors.New("user configuration changed")
			}
		}
	}()
	start()
	wait := func(what string, fn func() bool) error {
		c, done := context.WithTimeout(ctx, 150*time.Second)
		defer done()
		return poll(c, what, fn)
	}
	if err := wait("isolated machine online", func() bool {
		ms, e := client.Machines(ctx)
		if e != nil {
			return false
		}
		for _, m := range ms {
			if m.ID == machine.ID && m.Online {
				return true
			}
		}
		return false
	}); err != nil {
		return err
	}
	model := os.Getenv("RA_AGY_MODEL")
	if model == "" {
		model = "gemini-3.8-flash"
	}
	fmt.Printf("Environment: local relay, encrypted synthetic client, real agy; model=%s effort=low preflight=%v\n", model, preflight)
	_, err = client.DoMachine(ctx, machine.ID, &pb.Command{Kind: &pb.Command_UpdateSettings{UpdateSettings: &pb.UpdateSettingsRequest{Settings: &pb.DaemonSettings{Antigravity: &pb.AgentDefaults{Model: model, Effort: "low", Mode: pb.ApprovalMode_APPROVAL_MODE_ASK}}}}})
	if err != nil {
		return err
	}
	work := filepath.Join(dir, "work")
	must(os.MkdirAll(work, 0700))
	cmd := exec.Command(binary, "new", "--agent", "agy")
	cmd.Dir = work
	// The host harness may export NO_COLOR. Exercise the normal styled TUI;
	// unstyled mode placeholders cannot be distinguished from identical drafts.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "NO_COLOR=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("CLI start: %w", err)
	}
	sid := strings.TrimSpace(string(output))
	var view remoteclient.Session
	refresh := func() bool {
		ss, e := client.Sessions(ctx)
		if e != nil {
			return false
		}
		for _, s := range ss {
			if s.ID == sid {
				view = s
				return true
			}
		}
		return false
	}
	if err := wait("CLI-created session idle", func() bool { return refresh() && view.Info.Status == pb.SessionStatus_SESSION_STATUS_IDLE }); err != nil {
		return err
	}
	fmt.Println("PASS CLI-created session reaches native input box")
	events := func(after uint64) []*pb.Event {
		evs, e := client.Events(ctx, view, after)
		if e != nil {
			return nil
		}
		return evs
	}
	sequence := func() uint64 {
		var n uint64
		for _, e := range events(0) {
			n = e.Seq
		}
		return n
	}
	event := func(after uint64, what string, match func(*pb.Event) bool) (*pb.Event, error) {
		var found *pb.Event
		e := wait(what, func() bool {
			for _, v := range events(after) {
				if match(v) {
					found = v
					return true
				}
			}
			return false
		})
		return found, e
	}
	do := func(cmd *pb.Command) error { _, e := client.Do(ctx, view, cmd); return e }
	send := func(text string) error {
		return do(&pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: sid, RequestId: remoteclient.NewRequestID(), Text: text}}})
	}
	mode := func(m pb.ApprovalMode) error {
		return waitModeObservation(wait, func() error {
			return do(&pb.Command{Kind: &pb.Command_SetApprovalMode{SetApprovalMode: &pb.SetApprovalModeRequest{SessionId: sid, Mode: m}}})
		})
	}
	if err := mode(pb.ApprovalMode_APPROVAL_MODE_PLAN); err != nil {
		return fmt.Errorf("enter plan: %w", err)
	}
	if err := mode(pb.ApprovalMode_APPROVAL_MODE_ASK); err != nil {
		return fmt.Errorf("return to default: %w", err)
	}
	var order []string
	for _, e := range events(0) {
		if c := e.GetTurnContext(); c != nil && c.ApprovalPolicy == "request-review" {
			if len(order) == 0 || order[len(order)-1] != c.PermissionMode {
				order = append(order, c.PermissionMode)
			}
		}
	}
	if len(order) < 3 || order[0] != "default" || order[len(order)-1] != "default" || !strings.Contains(strings.Join(order, ","), "plan") {
		return fmt.Errorf("mode targets were not confirmed: %v", order)
	}
	fmt.Println("PASS request-review cycle observed from statusLine:", strings.Join(order, " -> "))
	// Direct native keystrokes are explicit debug observations, not product UI.
	pane := "ra-" + sid[:8] + ":"
	adapter := antigravity.New("", "")
	capture := func() string {
		b, e := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-e", "-t", pane).Output()
		must(e)
		return string(b)
	}
	must(exec.Command("tmux", "-L", socket, "send-keys", "-t", pane, "-l", "acceptance draft").Run())
	if err := wait("native local draft", func() bool { return !adapter.Analyze(capture()).DraftEmpty }); err != nil {
		return err
	}
	must(exec.Command("tmux", "-L", socket, "send-keys", "-t", pane, "C-e", "C-u").Run())
	if err := wait("single-line clear", func() bool { return adapter.Analyze(capture()).DraftEmpty }); err != nil {
		return err
	}
	must(exec.Command("tmux", "-L", socket, "set-buffer", "line one\nline two").Run())
	must(exec.Command("tmux", "-L", socket, "paste-buffer", "-p", "-t", pane).Run())
	if err := wait("native multiline draft", func() bool { v, ok := adapter.ComposerText(capture(), ""); return ok && v == "line one\nline two" }); err != nil {
		return err
	}
	// Ctrl-E Ctrl-U clears one line; Backspace joins the previous line.
	for i := 0; i < 3 && !adapter.Analyze(capture()).DraftEmpty; i++ {
		must(exec.Command("tmux", "-L", socket, "send-keys", "-t", pane, "C-e", "C-u", "BSpace").Run())
		time.Sleep(100 * time.Millisecond)
	}
	if !adapter.Analyze(capture()).DraftEmpty {
		return errors.New("multiline draft was not cleared")
	}
	fmt.Println("PASS native single-line and multiline draft capture; verified line-by-line clear")
	c := localclient.New(filepath.Join(home, "daemon.sock"))
	if evidence := os.Getenv("RA_AGY_EVIDENCE_DIR"); evidence != "" {
		defer func() {
			_ = os.MkdirAll(evidence, 0700)
			if b, e := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-e", "-t", pane).Output(); e == nil {
				_ = os.WriteFile(filepath.Join(evidence, "native-screen-last.txt"), b, 0600)
			}
			if b, e := os.ReadFile(filepath.Join(home, "sessions", sid, "events.jsonl")); e == nil {
				_ = os.WriteFile(filepath.Join(evidence, "events.jsonl"), b, 0600)
			}
			if b, e := os.ReadFile(filepath.Join(home, "sessions", sid, "session.json")); e == nil {
				_ = os.WriteFile(filepath.Join(evidence, "session.json"), b, 0600)
			}
			if b, e := os.ReadFile(filepath.Join(home, "sessions", sid, "session.json")); e == nil {
				var m struct {
					Rollout string `json:"rollout_path"`
				}
				if json.Unmarshal(b, &m) == nil && m.Rollout != "" {
					for _, name := range []string{"transcript.jsonl", "transcript_full.jsonl"} {
						if data, e := os.ReadFile(filepath.Join(filepath.Dir(m.Rollout), name)); e == nil {
							_ = os.WriteFile(filepath.Join(evidence, name), data, 0600)
						}
					}
				}
			}
			if b, e := os.ReadFile(filepath.Join(dir, "agy-daemon.log")); e == nil {
				_ = os.WriteFile(filepath.Join(evidence, "daemon.log"), b, 0600)
			}

		}()
	}

	if preflight {
		fmt.Println("SKIP model reply, approvals, question, plan, interrupt, resume, compact, quota (preflight sends no model requests)")
		fmt.Println("all preflight checks passed")
		return nil
	}
	// Never spend model quota until the current status-line sample proves the
	// Gemini five-hour bucket has at least 20% remaining. Missing is not API.
	var quotaReady, quotaLow bool
	if err := wait("status-line Gemini quota before model requests", func() bool {
		machines, err := client.Machines(ctx)
		if err != nil {
			return false
		}
		for _, m := range machines {
			if m.ID == machine.ID {
				for _, q := range m.Meta.GetQuotas() {
					if q.Agent == pb.Agent_AGENT_ANTIGRAVITY {
						for _, w := range q.Windows {
							if w.Name == "gemini-5h" && w.UsedPercent != nil {
								quotaReady, quotaLow = true, *w.UsedPercent > 80
								return true
							}
						}
					}
				}
			}
		}
		return false
	}); err != nil {
		return err
	}
	if !quotaReady || quotaLow {
		fmt.Println("SKIP real model acceptance: Gemini five-hour remaining quota below 20%")
		return nil
	}
	fmt.Println("PASS status-line Gemini five-hour quota at least 20% before requests")
	seq := sequence()
	if err := send("Reply with exactly PONG and nothing else. Do not call tools."); err != nil {
		return err
	}
	if _, err := event(seq, "PONG reply", func(e *pb.Event) bool { return e.GetAgentMessage().GetText() == "PONG" }); err != nil {
		return err
	}
	if _, err := event(seq, "first turn completion", func(e *pb.Event) bool { return e.GetTurnEnded() != nil }); err != nil {
		return err
	}
	fmt.Println("PASS send and read model reply as encrypted events")
	// Two distinct uploads prove agy's count grows after each path, then the
	// native CLI receives both media and the text in one submitted message.
	// A phone-width terminal clips the media summary and expand hint. This
	// resize is a debug trigger; upload/send still cross the encrypted relay.
	must(exec.Command("tmux", "-L", socket, "resize-window", "-t", pane, "-x", "32", "-y", "48").Run())
	imageIDs := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		id, err := uploadRedImage(ctx, client, machine.ID)
		if err != nil {
			return err
		}
		imageIDs = append(imageIDs, id)
	}
	seq = sequence()
	if err := do(&pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: sid, RequestId: remoteclient.NewRequestID(), Text: "What is the dominant color in both attached images? Reply with RED only, without tools.", ImageIds: imageIDs}}}); err != nil {
		return err
	}
	if _, err := event(seq, "native image recognition reply", func(e *pb.Event) bool { return strings.Contains(strings.ToUpper(e.GetAgentMessage().GetText()), "RED") }); err != nil {
		return err
	}
	if _, err := event(seq, "image turn completed", func(e *pb.Event) bool { return e.GetTurnEnded() != nil }); err != nil {
		return err
	}
	if _, err := event(seq, "both uploaded images bound to user message", func(e *pb.Event) bool {
		m := e.GetUserMessage()
		if m == nil || len(m.Images) != 2 {
			return false
		}
		return m.Images[0].UploadId == imageIDs[0] && m.Images[1].UploadId == imageIDs[1]
	}); err != nil {
		return err
	}
	fmt.Println("PASS two-image upload in a 32-column terminal, native attachment recognition, text submission and model reply over encrypted relay (debug resize)")
	must(exec.Command("tmux", "-L", socket, "resize-window", "-t", pane, "-x", "160", "-y", "48").Run())
	if imagesOnly {
		return nil
	}
	resolve := func(e *pb.Event, allow bool) error {
		return do(&pb.Command{Kind: &pb.Command_ResolveApproval{ResolveApproval: &pb.ResolveApprovalRequest{SessionId: sid, ApprovalId: e.GetApprovalRequested().ApprovalId, Allow: allow, ObservedEventSeq: e.Seq}}})
	}
	for _, allow := range []bool{true, false} {
		name := "deny"
		if allow {
			name = "allow"
		}
		target := filepath.Join(work, name+"-marker")
		seq = sequence()
		if err := send("Run exactly this shell command once, and do not retry if refused: touch " + target); err != nil {
			return err
		}
		e, err := event(seq, "native command approval ("+name+")", func(e *pb.Event) bool { return e.GetApprovalRequested() != nil })
		if err != nil {
			return err
		}
		if err := resolve(e, allow); err != nil {
			return err
		}
		if _, err := event(seq, "approval resolution", func(e *pb.Event) bool { return e.GetApprovalResolved() != nil }); err != nil {
			return err
		}
		if _, err := event(seq, "command turn completion", func(e *pb.Event) bool { return e.GetTurnEnded() != nil }); err != nil {
			return err
		}
		_, statErr := os.Stat(target)
		if allow != (statErr == nil) {
			return fmt.Errorf("%s marker result is wrong", name)
		}
		fmt.Println("PASS remote native approval", name)
	}
	for _, allow := range []bool{true, false} {
		label := "deny"
		if allow {
			label = "allow"
		}
		target := filepath.Join(work, "create-"+label+".txt")
		seq = sequence()
		if err := send("Use write_file once to create " + target + " containing hello. Do not use a shell and do not retry if refused."); err != nil {
			return err
		}
		e, err := event(seq, "native file creation approval", func(e *pb.Event) bool { return e.GetApprovalRequested() != nil })
		if err != nil {
			return err
		}
		if err := resolve(e, allow); err != nil {
			return err
		}
		if _, err := event(seq, "file creation turn completion", func(e *pb.Event) bool { return e.GetTurnEnded() != nil }); err != nil {
			return err
		}
		_, statErr := os.Stat(target)
		if allow != (statErr == nil) {
			return fmt.Errorf("native file %s result wrong", label)
		}
		fmt.Println("PASS native file creation", label, "(menu single-use keys)")
	}
	seq = sequence()
	if err := send("Use ask_question to ask exactly one single-select question 'Which language?' with options Go and Python. Do not answer it yourself."); err != nil {
		return err
	}
	q, err := event(seq, "single-select question", func(e *pb.Event) bool { return e.GetQuestionAsked() != nil })
	if err != nil {
		return err
	}
	if err := do(&pb.Command{Kind: &pb.Command_AnswerQuestion{AnswerQuestion: &pb.AnswerQuestionRequest{SessionId: sid, QuestionId: q.GetQuestionAsked().QuestionId, Option: 2, ObservedEventSeq: q.Seq}}}); err != nil {
		return err
	}
	if _, err := event(seq, "question result", func(e *pb.Event) bool { return e.GetQuestionAnswered() != nil }); err != nil {
		return err
	}
	if _, err := event(seq, "question turn completion", func(e *pb.Event) bool { return e.GetTurnEnded() != nil }); err != nil {
		return err
	}
	fmt.Println("PASS remotely answer native question")
	planRun := &planAcceptance{ctx: ctx, client: client, view: &view, sid: sid, socket: socket, events: events, wait: wait, send: send, mode: mode, sequence: sequence, resolve: resolve, refresh: refresh}
	if err := planRun.run(); err != nil {
		return err
	}
	if err := mode(pb.ApprovalMode_APPROVAL_MODE_ASK); err != nil {
		return err
	}
	seq = sequence()
	started := filepath.Join(work, "interrupt-started")
	finish := filepath.Join(work, "interrupt-finish")
	defer os.WriteFile(finish, nil, 0600)
	prompt := "Run exactly this shell command and nothing else, using WaitMsBeforeAsync 100000: touch " + started + "; while [ ! -f " + finish + " ]; do sleep 1; done"
	if err := send(prompt); err != nil {
		return err
	}
	if _, err := event(seq, "interruptible tool", func(e *pb.Event) bool { return e.GetToolStarted() != nil }); err != nil {
		return err
	}
	resolvedInterrupt := map[string]bool{}
	if err := wait("real interrupt command started", func() bool {
		for _, e := range events(seq) {
			if a := e.GetApprovalRequested(); a != nil && !resolvedInterrupt[a.ApprovalId] {
				if resolve(e, true) == nil {
					resolvedInterrupt[a.ApprovalId] = true
				}
			}
		}
		_, e := os.Stat(started)
		return e == nil
	}); err != nil {
		return err
	}
	if err := do(&pb.Command{Kind: &pb.Command_Interrupt{Interrupt: &pb.InterruptRequest{SessionId: sid}}}); err != nil {
		return err
	}
	if _, err := event(seq, "aborted turn", func(e *pb.Event) bool { return e.GetTurnEnded().GetOutcome() == pb.TurnOutcome_TURN_OUTCOME_ABORTED }); err != nil {
		return err
	}
	_ = os.WriteFile(finish, nil, 0600)
	fmt.Println("PASS interrupt a verified running native command; stable cancellation screen records aborted turn")
	stop()
	start()
	if err := wait("new daemon authenticated relay round trip", func() bool {
		check, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		result, e := client.DoMachine(check, machine.ID, &pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})
		return e == nil && result.GetGetSettings() != nil
	}); err != nil {
		return err
	}
	if err := wait("daemon reattachment", func() bool { return refresh() && view.Info.Status == pb.SessionStatus_SESSION_STATUS_IDLE }); err != nil {
		return err
	}
	seq = sequence()
	if err := send("Reply exactly AFTER-RESTART, without tools."); err != nil {
		return err
	}
	if _, err := event(seq, "post-restart reply", func(e *pb.Event) bool { return strings.Contains(e.GetAgentMessage().GetText(), "AFTER-RESTART") }); err != nil {
		return err
	}
	if _, err := event(seq, "post-restart completion", func(e *pb.Event) bool { return e.GetTurnEnded() != nil }); err != nil {
		return err
	}
	fmt.Println("PASS daemon restart reattaches native session; events continue")
	// Stop and resume also exercises --conversation, rather than only reattachment.
	_, err = c.StopSession(ctx, connect.NewRequest(&pb.StopSessionRequest{SessionId: sid}))
	if err != nil {
		return err
	}
	if err := do(&pb.Command{Kind: &pb.Command_ResumeSession{ResumeSession: &pb.ResumeSessionRequest{SessionId: sid}}}); err != nil {
		return err
	}
	seq = sequence()
	if err := send("Reply exactly RESUMED, without tools."); err != nil {
		return err
	}
	if _, err := event(seq, "conversation resume reply", func(e *pb.Event) bool { return e.GetAgentMessage().GetText() == "RESUMED" }); err != nil {
		return err
	}
	if _, err := event(seq, "resume completion", func(e *pb.Event) bool { return e.GetTurnEnded() != nil }); err != nil {
		return err
	}
	fmt.Println("PASS --conversation resumes the same transcript after process exit")
	if err := wait("resumed conversation metadata", func() bool { return refresh() && view.Info.CodexSessionId != "" }); err != nil {
		return err
	}
	originalConversation := view.Info.CodexSessionId
	localCommand := func(text string) error {
		if e := exec.Command("tmux", "-L", socket, "send-keys", "-t", pane, "-l", text).Run(); e != nil {
			return e
		}
		return exec.Command("tmux", "-L", socket, "send-keys", "-t", pane, "Enter").Run()
	}
	seq = sequence()
	if err := localCommand("/fork"); err != nil {
		return err
	}
	if err := wait("native local fork identity", func() bool {
		return refresh() && view.Info.CodexSessionId != "" && view.Info.CodexSessionId != originalConversation && view.Info.Status == pb.SessionStatus_SESSION_STATUS_IDLE
	}); err != nil {
		return err
	}
	if err := send("Reply exactly FORKED-LOCAL-OK, without tools."); err != nil {
		return err
	}
	if _, err := event(seq, "local fork reply", func(e *pb.Event) bool { return e.GetAgentMessage().GetText() == "FORKED-LOCAL-OK" }); err != nil {
		return err
	}
	if _, err := event(seq, "local fork completion", func(e *pb.Event) bool { return e.GetTurnEnded() != nil }); err != nil {
		return err
	}
	seq = sequence()
	if err := localCommand("/resume " + originalConversation); err != nil {
		return err
	}
	if err := wait("native local resume identity", func() bool {
		return refresh() && view.Info.CodexSessionId == originalConversation && view.Info.Status == pb.SessionStatus_SESSION_STATUS_IDLE
	}); err != nil {
		return err
	}
	if err := send("Reply exactly BACK-LOCAL-OK, without tools."); err != nil {
		return err
	}
	if _, err := event(seq, "local resume reply", func(e *pb.Event) bool { return e.GetAgentMessage().GetText() == "BACK-LOCAL-OK" }); err != nil {
		return err
	}
	if _, err := event(seq, "local resume completion", func(e *pb.Event) bool { return e.GetTurnEnded() != nil }); err != nil {
		return err
	}
	fmt.Println("PASS native local /fork and /resume rebind identity and transcript; encrypted input follows verified conversation")

	seq = sequence()
	if err := do(&pb.Command{Kind: &pb.Command_Compact{Compact: &pb.CompactRequest{SessionId: sid}}}); err != nil {
		return err
	}
	if _, err := event(seq, "CHECKPOINT compaction", func(e *pb.Event) bool { return e.GetSettingsChanged().GetCompacted() }); err != nil {
		return err
	}
	fmt.Println("PASS compact completes with transcript CHECKPOINT")
	if err := wait("Antigravity quota metadata", func() bool {
		ms, e := client.Machines(ctx)
		if e != nil {
			return false
		}
		for _, m := range ms {
			if m.ID == machine.ID {
				for _, q := range m.Meta.Quotas {
					if q.Agent == pb.Agent_AGENT_ANTIGRAVITY && q.State == pb.QuotaState_QUOTA_STATE_OK && len(q.Windows) > 0 {
						return true
					}
				}
			}
		}
		return false
	}); err != nil {
		return err
	}
	fmt.Println("PASS quota appears in encrypted machine metadata (bucket values omitted)")
	for _, path := range configs {
		b, _ := os.ReadFile(path)
		if sha256.Sum256(b) != hashes[path] {
			return errors.New("user configuration changed")
		}
	}
	fmt.Println("PASS user agy and Claude settings unchanged")
	fmt.Println("all checks passed")
	return nil
}
