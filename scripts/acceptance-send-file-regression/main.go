// Real Claude Code/Codex regression through a throwaway encrypted relay.
// Build this harness plus server, candidate daemon and baseline daemon under
// RA_DEV_DIR/bin. Native profiles live under RA_DEV_DIR/<phase>/native-<cli>.
// Every subprocess of a test binary checks and prints its isolated environment.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/claude"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/updater"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func write(path string, b []byte) { must(os.WriteFile(path, b, 0600)) }
func localPath(path string) bool {
	clean := filepath.Clean(path)
	return filepath.IsAbs(clean) && (strings.HasPrefix(clean, "/tmp/") || strings.HasPrefix(clean, "/private/tmp/"))
}
func baseEnv() map[string]string {
	env := map[string]string{}
	for _, entry := range os.Environ() {
		k, v, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(k, "REMOTE_AGENT_") || strings.HasPrefix(k, "RA_") || k == "CODEX_HOME" || k == "CLAUDE_CONFIG_DIR" {
			continue
		}
		env[k] = v
	}
	return env
}
func environment(root, phase, socket string) map[string]string {
	e := baseEnv()
	e["RA_DEV_DIR"] = root
	e["REMOTE_AGENT_HOME"] = filepath.Join(root, phase, "machine")
	e["REMOTE_AGENT_TMUX_SOCKET"] = socket
	e["CODEX_HOME"] = filepath.Join(root, phase, "native-codex")
	e["CLAUDE_CONFIG_DIR"] = filepath.Join(root, phase, "native-claude")
	return e
}
func guarded(command *exec.Cmd, env map[string]string) {
	for _, k := range []string{"REMOTE_AGENT_HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "RA_DEV_DIR"} {
		if !localPath(env[k]) {
			panic("refusing non-isolated " + k)
		}
	}
	if env["REMOTE_AGENT_TMUX_SOCKET"] == "" {
		panic("missing isolated tmux socket")
	}
	fmt.Printf("ISOLATION REMOTE_AGENT_HOME=%s REMOTE_AGENT_TMUX_SOCKET=%s CODEX_HOME=%s CLAUDE_CONFIG_DIR=%s exec=%s\n", env["REMOTE_AGENT_HOME"], env["REMOTE_AGENT_TMUX_SOCKET"], env["CODEX_HOME"], env["CLAUDE_CONFIG_DIR"], filepath.Base(command.Path))
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	command.Env = nil
	for _, k := range keys {
		command.Env = append(command.Env, k+"="+env[k])
	}
}
func start(command *exec.Cmd, env map[string]string, log string) func() {
	f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	must(err)
	command.Stdout = f
	command.Stderr = f
	guarded(command, env)
	must(command.Start())
	return func() { _ = command.Process.Signal(os.Interrupt); _ = command.Wait(); _ = f.Close() }
}
func poll(parent context.Context, what string, fn func() bool) {
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	defer cancel()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		if fn() {
			fmt.Println("PASS", what)
			return
		}
		select {
		case <-ctx.Done():
			panic("timeout: " + what)
		case <-ticker.C:
		}
	}
}
func find(ctx context.Context, c *remoteclient.Client, id string) (remoteclient.Session, bool) {
	rows, err := c.Sessions(ctx)
	if err != nil {
		return remoteclient.Session{}, false
	}
	for _, s := range rows {
		if s.ID == id {
			return s, true
		}
	}
	return remoteclient.Session{}, false
}

type run struct {
	root, phase, binary, socket, machine, project string
	env                                           map[string]string
	client                                        *remoteclient.Client
	ctx                                           context.Context
}
type native struct {
	r               *run
	kind            pb.Agent
	name, model, id string
	view            remoteclient.Session
	adapter         agent.Adapter
	startup         []string
	born            time.Time
	timed           bool
}

func (n *native) screen() string {
	b, _ := exec.Command("tmux", "-L", n.r.socket, "capture-pane", "-p", "-e", "-t", "ra-"+n.id[:8]+":").Output()
	return string(b)
}
func (n *native) startupAction(raw string) bool {
	plain := agent.StripANSI(raw)
	if strings.Contains(plain, "Make auto mode your default permission mode?") {
		key := "Down"
		if strings.Contains(plain, "❯ No, keep manual mode") {
			key = "Enter"
		}
		must(exec.Command("tmux", "-L", n.r.socket, "send-keys", "-t", "ra-"+n.id[:8]+":", key).Run())
		fmt.Println("DEBUG isolated native onboarding", n.name, key)
		return true
	}
	if len(n.startup) > 0 {
		key := n.startup[0]
		n.startup = n.startup[1:]
		must(exec.Command("tmux", "-L", n.r.socket, "send-keys", "-t", "ra-"+n.id[:8]+":", key).Run())
		fmt.Println("DEBUG isolated startup key", n.name, key)
		return true
	}
	if keys := n.adapter.(agent.Checkable).StartupKeys(n.adapter.Analyze(raw).Popup); len(keys) > 0 {
		n.startup = keys
		return true
	}
	return false
}

func (n *native) ready() {
	poll(n.r.ctx, n.r.phase+" "+n.name+" native composer ready", func() bool {
		if s, ok := find(n.r.ctx, n.r.client, n.id); ok {
			n.view = s
		}
		raw := n.screen()
		if n.startupAction(raw) {
			return false
		}
		scr := n.adapter.Analyze(raw)
		return scr.ComposerReady && !scr.Busy && scr.Popup == "" && n.view.ID != ""
	})
	if !n.timed {
		fmt.Printf("TIMING %s %s startup_ms=%d\n", n.r.phase, n.name, time.Since(n.born).Milliseconds())
		n.timed = true
	}
}
func (r *run) create(kind pb.Agent) *native {
	n := &native{r: r, kind: kind, born: time.Now()}
	if kind == pb.Agent_AGENT_CLAUDE {
		n.name = "claude"
		n.model = "claude-haiku-5-5"
		n.adapter = claude.New("", r.env["CLAUDE_CONFIG_DIR"])
	} else {
		n.name = "codex"
		n.model = "gpt-6.1-sol"
		n.adapter = codex.New("", r.env["CODEX_HOME"], "")
	}
	res, err := r.client.DoMachine(r.ctx, r.machine, &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{Cwd: r.project, Agent: kind, Model: n.model, Mode: pb.ApprovalMode_APPROVAL_MODE_ASK}}})
	must(err)
	n.id = res.GetCreateSession().GetSession().GetId()
	if n.id == "" {
		panic("missing new session")
	}
	n.ready()
	return n
}
func (n *native) stop() {
	if s, ok := find(n.r.ctx, n.r.client, n.id); ok {
		n.view = s
	}
	_, err := n.r.client.Do(n.r.ctx, n.view, &pb.Command{Kind: &pb.Command_StopSession{StopSession: &pb.StopSessionRequest{SessionId: n.id}}})
	must(err)
	poll(n.r.ctx, n.name+" stopped", func() bool {
		s, ok := find(n.r.ctx, n.r.client, n.id)
		if ok {
			n.view = s
		}
		return ok && s.Info.Status == pb.SessionStatus_SESSION_STATUS_EXITED
	})
}
func (n *native) probeTools() []byte {
	startTime := time.Now()
	cmd := exec.CommandContext(n.r.ctx, n.r.binary, "mcp", "--session", n.id)
	guarded(cmd, n.r.env)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "isolated-regression", Version: "test"}, nil).Connect(n.r.ctx, &mcp.CommandTransport{Command: cmd}, nil)
	must(err)
	defer session.Close()
	initialized := time.Since(startTime)
	list, err := session.ListTools(n.r.ctx, nil)
	must(err)
	sort.Slice(list.Tools, func(i, j int) bool { return list.Tools[i].Name < list.Tools[j].Name })
	found := false
	for _, tool := range list.Tools {
		if tool.Name == "send_file" {
			found = true
			if n.r.phase == "candidate" && tool.Meta["antigravity/alwaysLoad"] != true {
				panic("missing alwaysLoad metadata")
			}
			if tool.Annotations != nil && tool.Annotations.ReadOnlyHint {
				panic("send_file marked read-only")
			}
		}
	}
	if !found {
		panic("missing send_file")
	}
	raw, err := json.MarshalIndent(list, "", "  ")
	must(err)
	write(filepath.Join(n.r.root, n.r.phase+"-"+n.name+"-tools-"+fmt.Sprint(time.Now().UnixNano())+".json"), raw)
	for _, tool := range list.Tools {
		tool.Meta = nil
	}
	canonical, err := json.Marshal(list)
	must(err)
	fmt.Printf("MCP %s %s initialize_ms=%d tools_list_ms=%d count=%d\n", n.r.phase, n.name, initialized.Milliseconds(), time.Since(startTime).Milliseconds(), len(list.Tools))
	return canonical
}
func (n *native) assertEnv() {
	var env map[string]string
	b, err := os.ReadFile(filepath.Join(n.r.env["REMOTE_AGENT_HOME"], "sessions", n.id, "env.json"))
	must(err)
	must(json.Unmarshal(b, &env))
	for _, key := range []string{"REMOTE_AGENT_HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		if env[key] != n.r.env[key] {
			panic("native environment escaped isolation: " + key)
		}
	}
	fmt.Println("PASS native persisted environment isolated", n.r.phase, n.name)
}

type turn struct {
	events        []*pb.Event
	shared        *pb.SharedFile
	approvalCount int
	callCount     int
	id            string
}

func (n *native) request(prompt, action, commandFile string, allow bool) turn {
	n.ready()
	events, err := n.r.client.Events(n.r.ctx, n.view, 0)
	must(err)
	cursor := uint64(0)
	for _, e := range events {
		if e.Seq > cursor {
			cursor = e.Seq
		}
	}
	rid := remoteclient.NewRequestID()
	_, err = n.r.client.Do(n.r.ctx, n.view, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: n.id, RequestId: rid, Text: prompt}}})
	must(err)
	seen := map[uint64]bool{}
	answers := map[string]bool{}
	out := turn{}
	ended, replied := false, false
	decisions := 0
	poll(n.r.ctx, n.name+" "+action+" actual request completed", func() bool {
		if n.startupAction(n.screen()) {
			return false
		}
		if s, ok := find(n.r.ctx, n.r.client, n.id); ok {
			n.view = s
		}
		rows, e := n.r.client.Events(n.r.ctx, n.view, cursor)
		if e != nil {
			return false
		}
		out.events = rows
		for _, ev := range rows {
			if seen[ev.Seq] {
				continue
			}
			seen[ev.Seq] = true
			if u := ev.GetUserMessage(); u != nil && u.RequestId == rid {
				out.id = ev.TurnId
			}
			if t := ev.GetToolStarted(); t != nil {
				fmt.Printf("TOOL %s %s %s\n", n.name, action, t.Title)

			}
			if a := ev.GetApprovalRequested(); a != nil && !answers[a.ApprovalId] {
				if action == "negative" {
					panic("ordinary summary asked for tool approval")
				}
				if action == "allow-command" || action == "deny-command" {
					if a.Command == "" || !strings.Contains(a.Command, commandFile) {
						panic("unexpected command approval")
					}
				} else if !strings.Contains(a.ToolName, "send_file") {
					panic("unexpected non-file approval")
				}
				_, err := n.r.client.Do(n.r.ctx, n.view, &pb.Command{Kind: &pb.Command_ResolveApproval{ResolveApproval: &pb.ResolveApprovalRequest{SessionId: n.id, ApprovalId: a.ApprovalId, Allow: allow, ObservedEventSeq: ev.Seq}}})
				must(err)
				answers[a.ApprovalId] = true
				out.approvalCount++
				fmt.Printf("REMOTE %s %s allow=%t tool=%s\n", n.name, action, allow, a.ToolName)
			}
			if a := ev.GetApprovalResolved(); a != nil && answers[a.ApprovalId] {
				want := pb.ApprovalDecision_APPROVAL_DECISION_ALLOW
				if !allow {
					want = pb.ApprovalDecision_APPROVAL_DECISION_DENY
				}
				if a.Decision != want {
					panic("unexpected approval decision")
				}
				decisions++
			}
			if t := ev.GetToolCall(); t != nil && (strings.Contains(t.Title, "send_file") || strings.Contains(t.ItemType, "send_file")) {
				out.callCount++
			}
			if t := ev.GetToolCall(); t != nil && len(t.SharedFiles) > 0 {
				if action != "new-send" && action != "resumed-send" {
					panic("non-send request offered a file")
				}
				if out.id == "" || ev.TurnId != out.id || out.shared != nil {
					panic("file attached to wrong/repeated turn")
				}
				out.shared = t.SharedFiles[0]
			}
			if ev.TurnId == out.id && out.id != "" {
				if m := ev.GetAgentMessage(); m != nil && m.Text != "" {
					replied = true
				}
				if end := ev.GetTurnEnded(); end != nil {
					if end.Outcome != pb.TurnOutcome_TURN_OUTCOME_COMPLETED {
						panic("native request aborted")
					}
					ended = true
				}
			}
		}
		raw := n.screen()
		if strings.Contains(raw, "rate limit") || strings.Contains(raw, "out of extra usage") {
			write(filepath.Join(n.r.root, n.name+"-quota-screen.txt"), []byte(raw))
		}
		return ended && replied
	})
	b, err := json.MarshalIndent(out.events, "", "  ")
	must(err)
	write(filepath.Join(n.r.root, n.r.phase+"-"+n.name+"-"+action+"-events.json"), b)
	if out.approvalCount != decisions {
		panic("missing approval result")
	}
	if action == "negative" && (out.shared != nil || out.callCount != 0) {
		panic("ordinary summary called send_file")
	}
	if strings.HasSuffix(action, "-command") && out.approvalCount != 1 {
		panic("expected exactly one command approval")
	}
	return out
}
func (n *native) sendFile(label string) {
	file := n.name + "-" + label + ".bin"
	want := make([]byte, (768<<10)+(384<<10))
	_, err := rand.Read(want)
	must(err)
	write(filepath.Join(n.r.project, file), want)
	result := n.request("请把当前目录的 "+file+" 发到我的手机，我要在 Remote Agent 客户端里下载原文件。", label+"-send", "", true)
	if result.shared == nil || result.callCount != 1 {
		panic("first user turn did not call send_file once")
	}
	f := result.shared
	sum := sha256.Sum256(want)
	if f.Name != file || f.SizeBytes != int64(len(want)) || !bytes.Equal(f.Sha256, sum[:]) {
		panic("offer differs from source")
	}
	var got []byte
	for offset := int64(0); offset < f.SizeBytes; offset += 768 << 10 {
		res, err := n.r.client.Do(n.r.ctx, n.view, &pb.Command{Kind: &pb.Command_GetSharedFileChunk{GetSharedFileChunk: &pb.GetSharedFileChunkRequest{SessionId: n.id, FileId: f.FileId, Offset: offset}}})
		must(err)
		chunk := res.GetGetSharedFileChunk()
		if chunk == nil || chunk.TotalBytes == nil || chunk.GetTotalBytes() != uint64(len(want)) || len(chunk.Data) != 768<<10 {
			panic("invalid chunk")
		}
		got = append(got, chunk.Data...)
	}
	got = got[:len(want)]
	if sha256.Sum256(got) != sum || !bytes.Equal(got, want) {
		panic("download hash mismatch")
	}
	fmt.Printf("PASS %s %s first-turn send_file, encrypted shared_files, 2 chunks, %d bytes SHA256=%x approvals=%d\n", n.name, label, len(want), sum, result.approvalCount)
}
func (n *native) command(allow bool) {
	file := n.name + "-denied.txt"
	action := "deny-command"
	if allow {
		file = n.name + "-allowed.txt"
		action = "allow-command"
	}
	command := "python3 -c 'from pathlib import Path; Path(\"" + file + "\").write_text(\"COMMAND_OK\")'"
	prompt := "Run this exact shell command once using the command execution tool, not a file editing tool: " + command + " . If approval is denied, do not retry or create the file another way; just reply BLOCKED. Otherwise reply DONE."
	if n.kind == pb.Agent_AGENT_CODEX {
		prompt += " Execute it with elevated permissions outside the sandbox, requesting an explicit command approval through the normal tool approval mechanism, not an ordinary question. Do not execute it without that approval."
	}
	n.request(prompt, action, file, allow)
	got, err := os.ReadFile(filepath.Join(n.r.project, file))
	if allow {
		must(err)
		if string(got) != "COMMAND_OK" {
			panic("approved command did not execute")
		}
	} else if !os.IsNotExist(err) {
		panic("denied command produced a file")
	}
	fmt.Printf("PASS %s command approval allow=%t filesystem effect verified\n", n.name, allow)
}
func main() {
	root := os.Getenv("RA_DEV_DIR")
	if !localPath(root) || os.Getenv("REMOTE_AGENT_HOME") != filepath.Join(root, "bootstrap") {
		panic("start harness with explicit isolated bootstrap REMOTE_AGENT_HOME")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	addr := listener.Addr().String()
	must(listener.Close())
	server := "http://" + addr
	seed := make([]byte, 24)
	_, err = rand.Read(seed)
	must(err)
	env := environment(root, "server", "ra-cc-server")
	env["RA_SETUP_TOKEN"] = hex.EncodeToString(seed)
	must(os.MkdirAll(env["REMOTE_AGENT_HOME"], 0700))
	stopServer := start(exec.Command(filepath.Join(root, "bin/remote-agent-server"), "--listen", addr, "--db", filepath.Join(root, "relay.db"), "--max-accounts", "4"), env, filepath.Join(root, "server.log"))
	defer stopServer()
	poll(ctx, "loopback relay ready", func() bool {
		res, e := http.Get(server + "/healthz")
		if e != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200
	})
	auth, err := remoteclient.NewAuthority(ctx, server, env["RA_SETUP_TOKEN"])
	must(err)
	baseline := map[string][]byte{}
	for _, phase := range []string{"baseline", "candidate"} {
		func() {
			socket := "ra-cc-" + phase + "-" + strings.Split(addr, ":")[1]
			e := environment(root, phase, socket)
			must(os.MkdirAll(e["REMOTE_AGENT_HOME"], 0700))
			must(updater.EnsureState(e["REMOTE_AGENT_HOME"]))
			_, ed, err := ed25519.GenerateKey(nil)
			must(err)
			x, _, err := deviceauth.NewXKey()
			must(err)
			machine, err := auth.Enroll(ctx, ed, x, deviceauth.Machine)
			must(err)
			must(machine.Endpoint.Save(filepath.Join(e["REMOTE_AGENT_HOME"], "security-v3.db")))
			machine.Endpoint.Close()
			// Save uses only this explicitly isolated state, never the caller's account.
			must(os.Setenv("REMOTE_AGENT_HOME", e["REMOTE_AGENT_HOME"]))
			must((&relay.Account{Server: server, Root: auth.Root, DeviceSeed: ed.Seed(), DeviceID: machine.ID, Security: &relay.SecurityConfig{Pin: auth.Pin, XPrivate: x}}).Save())
			endpoint, err := deviceauth.InitEndpoint("", auth.Pin, auth.States, auth.Proofs, auth.Member.ID, auth.Ed.Seed(), auth.X, auth.Lease)
			must(err)
			defer endpoint.Close()
			client := remoteclient.NewClient(server, auth.Token, auth.Member.ID, endpoint)
			var agentEnv strings.Builder
			for _, k := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"} {
				if e[k] != "" {
					fmt.Fprintf(&agentEnv, "%s=%s\n", k, e[k])
				}
			}
			write(filepath.Join(e["REMOTE_AGENT_HOME"], "agent.env"), []byte(agentEnv.String()))
			binary := filepath.Join(root, "bin/remote-agent")
			if phase == "baseline" {
				binary = filepath.Join(root, "bin/remote-agent-baseline")
			}
			claudeBin := os.Getenv("RA_REG_CLAUDE_BIN")
			if claudeBin == "" {
				claudeBin, err = exec.LookPath("claude")
				must(err)
			}
			codexBin := os.Getenv("RA_REG_CODEX_BIN")
			if codexBin == "" {
				codexBin, err = exec.LookPath("codex")
				must(err)
			}
			stopDaemon := start(exec.Command(binary, "daemon", "--claude", claudeBin, "--codex", codexBin, "--agy", "/usr/bin/false", "--approval-timeout", "180s"), e, filepath.Join(root, phase+"-daemon.log"))
			defer stopDaemon()
			defer exec.Command("tmux", "-L", socket, "kill-server").Run()
			poll(ctx, phase+" isolated machine online", func() bool {
				rows, er := client.Machines(ctx)
				if er != nil {
					return false
				}
				for _, m := range rows {
					if m.ID == machine.ID {
						return m.Online && m.Meta.GetCapabilities().GetSharedFiles()
					}
				}
				return false
			})
			project := filepath.Join(root, phase, "project")
			must(os.MkdirAll(project, 0700))
			project, err = filepath.EvalSymlinks(project)
			must(err)
			r := &run{root: root, phase: phase, binary: binary, socket: socket, machine: machine.ID, project: project, env: e, client: client, ctx: ctx}
			kinds := []pb.Agent{pb.Agent_AGENT_CLAUDE, pb.Agent_AGENT_CODEX}
			if only := os.Getenv("RA_REG_AGENT"); only == "codex" {
				kinds = kinds[1:]
			} else if only == "claude" {
				kinds = kinds[:1]
			}
			for _, kind := range kinds {
				if phase == "baseline" && kind == pb.Agent_AGENT_CODEX && os.Getenv("RA_REG_BASELINE_ENGLISH") == "1" {
					control := r.create(kind)
					write(filepath.Join(project, "codex-control.bin"), []byte("isolated file control"))
					result := control.request("Please send me codex-control.bin from the current directory.", "new-send", "", true)
					fmt.Printf("CONTROL baseline codex English natural shared_files=%t calls=%d\n", result.shared != nil, result.callCount)
					control.stop()
				}
				n := r.create(kind)
				n.assertEnv()
				tools := n.probeTools()
				if phase == "baseline" {
					baseline[n.name] = tools
					if os.Getenv("RA_REG_BASELINE_SEND") == "1" {
						n.sendFile("new")
					}
					n.stop()
					continue
				}
				if !bytes.Equal(baseline[n.name], tools) {
					panic("tools/list changed beyond vendor metadata")
				}
				fmt.Println("PASS", n.name, "tools/list matches v0.72.1 except vendor metadata")
				if os.Getenv("RA_REG_STARTUP_ONLY") == "1" {
					n.stop()
					continue
				}
				n.sendFile("new")
				n.request("Reply with a one-sentence completion summary mentioning "+n.name+"-new.bin.", "negative", "", true)
				fmt.Println("PASS", n.name, "ordinary summary does not call send_file")
				n.command(true)
				n.command(false)
				if s, ok := find(ctx, client, n.id); ok {
					n.view = s
				}
				identity := n.view.Info.CodexSessionId
				if identity == "" {
					panic("missing original native identity")
				}
				n.stop()
				n.born = time.Now()
				n.timed = false
				_, err := client.Do(ctx, n.view, &pb.Command{Kind: &pb.Command_ResumeSession{ResumeSession: &pb.ResumeSessionRequest{SessionId: n.id}}})
				must(err)
				n.ready()
				n.assertEnv()
				if !bytes.Equal(baseline[n.name], n.probeTools()) {
					panic("resumed tools/list changed")
				}
				n.sendFile("resumed")
				if n.view.Info.CodexSessionId != identity {
					panic("resume created another native conversation")
				}
				fmt.Println("PASS", n.name, "resume retained native conversation identity")
				n.stop()
			}
		}()
	}
	fmt.Println("PASS ALL selected regression scenarios; startup_only=" + os.Getenv("RA_REG_STARTUP_ONLY"))
}
