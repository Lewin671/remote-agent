// Real native Antigravity acceptance; no terminal approval keys or follow-up
// tool hints. Use a fresh local dev-env.sh start --no-account relay:
// RA_DEV_DIR=<private directory> go run ./scripts/acceptance-agy-send-file
// RA_AGY_MODELS optionally selects comma-separated native model slugs.
// Each scenario starts a fresh ask-mode conversation. The signed Go client
// approves/denies requests and downloads through the encrypted relay.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
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

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
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
	b, err := os.ReadFile(path)
	must(err)
	return strings.TrimSpace(string(b))
}

func wait(ctx context.Context, what string, fn func() bool) {
	ticker := time.NewTicker(time.Second)
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

func main() {
	dir := os.Getenv("RA_DEV_DIR")
	if dir == "" {
		panic("RA_DEV_DIR is required")
	}
	server := read(filepath.Join(dir, "url"))
	u, err := url.Parse(server)
	must(err)
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		panic("refusing a non-local dev relay")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	auth, err := remoteclient.NewAuthority(ctx, server, read(filepath.Join(dir, "setup-token")))
	must(err)
	_, ed, err := ed25519.GenerateKey(nil)
	must(err)
	x, _, err := deviceauth.NewXKey()
	must(err)
	machine, err := auth.Enroll(ctx, ed, x, deviceauth.Machine)
	must(err)
	home, err := os.MkdirTemp(dir, "send-home-")
	must(err)
	must(updater.EnsureState(home))
	must(os.Setenv("REMOTE_AGENT_HOME", home))
	must(machine.Endpoint.Save(filepath.Join(home, "security-v3.db")))
	acct := &relay.Account{Server: server, Root: auth.Root, DeviceSeed: ed.Seed(), DeviceID: machine.ID, Security: &relay.SecurityConfig{Pin: auth.Pin, XPrivate: x}}
	must(acct.Save())
	machine.Endpoint.Close()
	phone, err := deviceauth.InitEndpoint("", auth.Pin, auth.States, auth.Proofs, auth.Member.ID, auth.Ed.Seed(), auth.X, auth.Lease)
	must(err)
	defer phone.Close()
	client := remoteclient.NewClient(server, auth.Token, auth.Member.ID, phone)
	socket := read(filepath.Join(dir, "tmux-socket")) + "-send"
	must(os.Setenv("REMOTE_AGENT_TMUX_SOCKET", socket))
	log, err := os.OpenFile(filepath.Join(dir, "send-daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	must(err)
	defer log.Close()
	process := exec.Command(filepath.Join(dir, "bin", "remote-agent"), "daemon", "--approval-timeout", "120s")
	process.Stdout, process.Stderr = log, log
	must(process.Start())
	defer func() {
		process.Process.Signal(os.Interrupt)
		process.Wait()
		exec.Command("tmux", "-L", socket, "kill-server").Run()
	}()
	var caps *pb.MachineCapabilities
	wait(ctx, "machine online and declares shared_files", func() bool {
		ms, err := client.Machines(ctx)
		if err != nil {
			return false
		}
		for _, m := range ms {
			if m.ID == machine.ID && m.Online {
				caps = m.Meta.GetCapabilities()
				return caps.GetSharedFiles() && caps.MaxSharedFileBytes == 100<<20 && caps.SharedFileChunkBytes == 768<<10
			}
		}
		return false
	})
	// The project lives outside the daemon's state directory, as a real one does.
	project, err := os.MkdirTemp("", "ra-agy-send-project-")
	must(err)
	defer os.RemoveAll(project)
	project, err = filepath.EvalSymlinks(project)
	must(err)
	runAgent(ctx, client, machine.ID, project, socket, int64(caps.SharedFileChunkBytes), pb.Agent_AGENT_ANTIGRAVITY)
}

func find(ctx context.Context, c *remoteclient.Client, sid string) (remoteclient.Session, bool) {
	rows, err := c.Sessions(ctx)
	if err != nil {
		return remoteclient.Session{}, false
	}
	for _, s := range rows {
		if s.ID == sid {
			return s, true
		}
	}
	return remoteclient.Session{}, false
}

var errQuota = errors.New("native CLI quota exhausted")

func runAgent(ctx context.Context, c *remoteclient.Client, machine, project, socket string, block int64, kind pb.Agent) {
	models := []string{"gemini-3.8-flash", "claude-sonnet-5-5", "gpt-oss-120b", "gemini-4-argon", "gemini-4-hydrogen", "claude-opus-5-5"}
	if value := os.Getenv("RA_AGY_MODELS"); value != "" {
		models = strings.Split(value, ",")
	}
	cases := []struct {
		file, prompt   string
		deny, negative bool
	}{
		{"report-cn.bin", "请把当前目录的 report-cn.bin 发给我。", false, false},
		{"video-cn.mp4", "把当前目录的 video-cn.mp4 发给我，我想在手机上查看。", false, false},
		{"report-en.bin", "Please send me report-en.bin from the current directory.", false, false},
		{"summary.txt", "请用一句话概括当前目录 summary.txt 的内容。", false, true},
		{"denied.bin", "Please send me denied.bin from the current directory.", true, false},
	}
	for _, model := range models {
		quota := false
		for i, tc := range cases {
			want := make([]byte, block+block/2)
			_, err := rand.Read(want)
			must(err)
			if tc.negative {
				want = []byte("This is a completed test report about a local file transfer.\n")
			}
			must(os.WriteFile(filepath.Join(project, tc.file), want, 0600))
			if err := scenario(ctx, c, machine, project, socket, block, kind, model, tc.file, tc.prompt, want, tc.deny, tc.negative); err != nil {
				if errors.Is(err, errQuota) {
					fmt.Printf("QUOTA %s scenario=%d\n", model, i+1)
					quota = true
					break
				}
				panic(err)
			}
		}
		if !quota {
			fmt.Println("PASS all five fresh Antigravity scenarios", model)
			return
		}
	}
	panic("all selected real Antigravity models are out of quota; acceptance is incomplete")
}

func scenario(parent context.Context, c *remoteclient.Client, machine, project, socket string, block int64, kind pb.Agent, model, file, prompt string, want []byte, deny, negative bool) error {
	ctx, cancel := context.WithTimeout(parent, 100*time.Second)
	defer cancel()
	res, err := c.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{Cwd: project, Agent: kind, Model: model, Effort: "medium", Prompt: prompt, Mode: pb.ApprovalMode_APPROVAL_MODE_ASK}}})
	if err != nil {
		return err
	}
	sid := res.GetCreateSession().GetSession().GetId()
	if sid == "" {
		return errors.New("missing session")
	}
	fmt.Printf("START model=%s file=%s deny=%t negative=%t prompt=%s\n", model, file, deny, negative, prompt)
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		if view, ok := find(cleanup, c, sid); ok {
			_, e := c.Do(cleanup, view, &pb.Command{Kind: &pb.Command_StopSession{StopSession: &pb.StopSessionRequest{SessionId: sid}}})
			must(e)
		}
	}()
	seen := map[uint64]bool{}
	answered := map[string]bool{}
	var view remoteclient.Session
	var offered *pb.SharedFile
	var turn string
	var approved, resolved, calls int
	var replied, ended, failed bool
	ticker := time.NewTicker(350 * time.Millisecond)
	defer ticker.Stop()
	for !ended {
		if s, ok := find(ctx, c, sid); ok {
			view = s
			events, e := c.Events(ctx, view, 0)
			if e != nil {
				return e
			}
			for _, event := range events {
				if seen[event.Seq] {
					continue
				}
				seen[event.Seq] = true
				if u := event.GetUserMessage(); u != nil {
					if u.Text != prompt || turn != "" {
						return errors.New("unexpected extra user turn")
					}
					turn = event.TurnId
				}
				if tc := event.GetTurnContext(); tc != nil && tc.Model != "" {
					fmt.Println("MODEL", tc.Model)
				}
				if t := event.GetToolStarted(); t != nil {
					fmt.Printf("TOOL %s %s\n", t.ItemType, t.Input)
					if t.ItemType == "call_mcp_tool" {
						var in struct {
							ServerName, ToolName string
							Arguments            map[string]string
						}
						if json.Unmarshal([]byte(t.Input), &in) != nil || in.ServerName != "remote_agent" || in.ToolName != "send_file" || filepath.Base(in.Arguments["path"]) != file {
							return errors.New("unexpected MCP call")
						}
						calls++
						if negative || calls > 1 {
							return errors.New("unexpected/repeated send_file")
						}
					} else if t.ItemType == "run_command" {
						var in struct{ CommandLine string }
						_ = json.Unmarshal([]byte(t.Input), &in)
						cmd := in.CommandLine
						if cmd != "ls -la" && cmd != "pwd" && cmd != "ls -la "+file && cmd != "ls -l "+file && cmd != "file "+file && !(negative && cmd == "cat "+file) {
							return errors.New("unexpected shell workaround: " + cmd)
						}
					} else if t.ItemType == "read_file" {
						var in struct{ Path string }
						if json.Unmarshal([]byte(t.Input), &in) != nil || filepath.Clean(in.Path) != filepath.Join(project, file) {
							return errors.New("unexpected file read")
						}
					} else {
						return errors.New("unexpected tool: " + t.ItemType)
					}
				}
				if a := event.GetApprovalRequested(); a != nil && !answered[a.ApprovalId] {
					if a.ToolName != "call_mcp_tool" && !(negative && a.ToolName == "read_file") {
						return errors.New("unexpected approval tool: " + a.ToolName)
					}
					_, e := c.Do(ctx, view, &pb.Command{Kind: &pb.Command_ResolveApproval{ResolveApproval: &pb.ResolveApprovalRequest{SessionId: sid, ApprovalId: a.ApprovalId, Allow: !deny, ObservedEventSeq: event.Seq}}})
					if e != nil {
						return fmt.Errorf("remote resolve: %w", e)
					}
					answered[a.ApprovalId] = true
					approved++
					fmt.Printf("REMOTE approval allow=%t tool=%s\n", !deny, a.ToolName)
				}
				if a := event.GetApprovalResolved(); a != nil && answered[a.ApprovalId] {
					expected := pb.ApprovalDecision_APPROVAL_DECISION_ALLOW
					if deny {
						expected = pb.ApprovalDecision_APPROVAL_DECISION_DENY
					}
					if a.Decision != expected {
						return fmt.Errorf("unexpected native approval decision %s", a.Decision)
					}
					resolved++
				}
				if t := event.GetToolCall(); t != nil && len(t.SharedFiles) > 0 {
					if negative || deny || offered != nil || turn == "" || event.TurnId != turn {
						return errors.New("unexpected file/turn")
					}
					offered = t.SharedFiles[0]
				}
				if a := event.GetAgentMessage(); a != nil && event.TurnId == turn && a.Text != "" {
					replied = true
					fmt.Println("REPLY", a.Text)
				}
				if event.GetTurnEnded() != nil && turn != "" && event.TurnId == turn {
					ended = true
					failed = event.GetTurnEnded().Outcome == pb.TurnOutcome_TURN_OUTCOME_ABORTED
				}
			}
		}
		// Diagnostic read only: it never presses native keys or supplies a prompt.
		raw, _ := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-t", "ra-"+sid[:8]+":").Output()
		if strings.Contains(string(raw), "Individual quota reached") {
			return errQuota
		}
		if failed {
			return errors.New("native turn ended with error")
		}
		if ended {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("scenario %s: %w", file, ctx.Err())
		case <-ticker.C:
		}
	}
	if !replied {
		return errors.New("missing reply for actual request turn")
	}
	if negative {
		if calls != 0 || offered != nil {
			return errors.New("ordinary task offered a file")
		}
		fmt.Printf("PASS negative first-turn reply; no send_file file=%s\n", file)
		return nil
	}
	if calls != 1 || approved != 1 || resolved != 1 {
		return fmt.Errorf("expected one bound call and remote decision, got calls=%d requests=%d resolved=%d", calls, approved, resolved)
	}
	if deny {
		if offered != nil {
			return errors.New("denied file was offered")
		}
		fmt.Println("PASS remote denial; no shared_files")
		return nil
	}
	if offered == nil {
		return errors.New("missing shared_files")
	}
	sum := sha256.Sum256(want)
	if offered.Name != file || offered.SizeBytes != int64(len(want)) || !bytes.Equal(offered.Sha256, sum[:]) {
		return errors.New("offer differs from source")
	}
	var got []byte
	for off := int64(0); off < offered.SizeBytes; off += block {
		result, e := c.Do(ctx, view, &pb.Command{Kind: &pb.Command_GetSharedFileChunk{GetSharedFileChunk: &pb.GetSharedFileChunkRequest{SessionId: sid, FileId: offered.FileId, Offset: off}}})
		if e != nil {
			return e
		}
		chunk := result.GetGetSharedFileChunk()
		if chunk == nil || chunk.TotalBytes == nil || chunk.GetTotalBytes() != uint64(len(want)) || int64(len(chunk.Data)) != block {
			return errors.New("invalid encrypted chunk")
		}
		got = append(got, chunk.Data...)
	}
	got = got[:len(want)]
	if sha256.Sum256(got) != sum || !bytes.Equal(got, want) {
		return errors.New("download differs from source")
	}
	fmt.Printf("PASS first-turn send_file + remote allow + shared_files + %d bytes in %d blocks SHA256=%x file=%s\n", len(got), (len(got)+int(block)-1)/int(block), sum, file)
	return nil
}
