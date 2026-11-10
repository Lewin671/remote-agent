// Run against a throwaway ios/scripts/dev-env.sh start --no-account relay:
// RA_DEV_DIR=<isolated directory> go run ./scripts/acceptance-send-file
// RA_SEND_FILE_AGENT=claude|codex|agy picks one CLI (default: Claude, then Codex).
// Real CLI quota is used. A real CLI in an ask-mode session is asked to call
// the remote_agent MCP's send_file; approval requests that reach the client
// are recorded and allowed through the signed client path. The script then
// downloads the offered file block by block through encrypted Forward and
// checks it. Never uses a daily remote-agent account or daemon. Stop the
// dev-env relay afterwards.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
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
	process := exec.Command(filepath.Join(dir, "bin", "remote-agent"), "daemon")
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
	project, err := os.MkdirTemp(dir, "send-project-")
	must(err)
	agents := []pb.Agent{pb.Agent_AGENT_CLAUDE, pb.Agent_AGENT_CODEX}
	switch os.Getenv("RA_SEND_FILE_AGENT") {
	case "claude":
		agents = agents[:1]
	case "codex":
		agents = agents[1:]
	case "agy":
		agents = []pb.Agent{pb.Agent_AGENT_ANTIGRAVITY}
	}
	for _, kind := range agents {
		runAgent(ctx, client, machine.ID, project, socket, int64(caps.SharedFileChunkBytes), kind)
	}
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

func runAgent(ctx context.Context, c *remoteclient.Client, machine, project, socket string, block int64, kind pb.Agent) {
	name := strings.ToLower(strings.TrimPrefix(kind.String(), "AGENT_"))
	// 1.5 blocks of random bytes: two blocks, the last one padded.
	want := make([]byte, block+block/2)
	_, err := rand.Read(want)
	must(err)
	file := "report-" + name + ".bin"
	must(os.WriteFile(filepath.Join(project, file), want, 0o600))
	model := ""
	if kind == pb.Agent_AGENT_CLAUDE {
		model = "claude-haiku-5-5"
	}
	prompt := "Use the send_file tool of the remote_agent MCP server to send the file " + file + " in the current directory to me. Call it exactly once with that relative path, do nothing else, then reply with the word DONE."
	res, err := c.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{Cwd: project, Agent: kind, Model: model, Prompt: prompt, Mode: pb.ApprovalMode_APPROVAL_MODE_ASK}}})
	must(err)
	sid := res.GetCreateSession().GetSession().GetId()
	if sid == "" {
		panic("session missing")
	}
	var view remoteclient.Session
	var offered *pb.SharedFile
	approved := map[string]bool{}
	trusted := false
	wait(ctx, name+": send_file tool call carries shared_files", func() bool {
		s, ok := find(ctx, c, sid)
		if !ok {
			return false
		}
		view = s
		if !trusted && (s.Info.Status == pb.SessionStatus_SESSION_STATUS_STARTING || s.Info.Status == pb.SessionStatus_SESSION_STATUS_BLOCKED) {
			// Startup prompts are not part of the scenario; answer only known trust UI.
			screen, _ := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-t", "ra-"+sid[:8]+":").Output()
			raw := string(screen)
			if strings.Contains(raw, "Trust this folder") || strings.Contains(raw, "trust this folder") || strings.Contains(raw, "Quick safety check") {
				// Claude Code 2.1.322 puts the cursor on "No, exit"; move to the trusting option.
				if strings.Contains(raw, "❯ No, exit") {
					_ = exec.Command("tmux", "-L", socket, "send-keys", "-t", "ra-"+sid[:8]+":", "Down").Run()
				}
				_ = exec.Command("tmux", "-L", socket, "send-keys", "-t", "ra-"+sid[:8]+":", "Enter").Run()
				trusted = true
				fmt.Println("DEBUG startup trust accepted on isolated terminal")
			}
		}
		evs, err := c.Events(ctx, view, 0)
		if err != nil {
			return false
		}
		resolved := map[string]bool{}
		for _, e := range evs {
			if a := e.GetApprovalResolved(); a != nil {
				resolved[a.ApprovalId] = true
			}
		}
		for _, e := range evs {
			if a := e.GetApprovalRequested(); a != nil && !resolved[a.ApprovalId] && !approved[a.ApprovalId] {
				fmt.Printf("OBSERVED %s: approval requested for tool %q\n", name, a.ToolName)
				_, err := c.Do(ctx, view, &pb.Command{Kind: &pb.Command_ResolveApproval{ResolveApproval: &pb.ResolveApprovalRequest{SessionId: sid, ApprovalId: a.ApprovalId, Allow: true, ObservedEventSeq: e.Seq}}})
				if err == nil {
					approved[a.ApprovalId] = true
				}
			}
			if t := e.GetToolCall(); t != nil && len(t.SharedFiles) > 0 {
				fmt.Printf("OBSERVED %s: tool call %q status %q with %d shared file(s)\n", name, t.Title, t.Status, len(t.SharedFiles))
				offered = t.SharedFiles[0]
			}
		}
		return offered != nil
	})
	sum := sha256.Sum256(want)
	if offered.Name != file || offered.SizeBytes != int64(len(want)) || !bytes.Equal(offered.Sha256, sum[:]) || !strings.HasSuffix(offered.Path, "/"+file) {
		panic(fmt.Sprintf("offer does not describe the file: %+v", offered))
	}
	var got []byte
	for off := int64(0); off < offered.SizeBytes; off += block {
		res, err := c.Do(ctx, view, &pb.Command{Kind: &pb.Command_GetSharedFileChunk{GetSharedFileChunk: &pb.GetSharedFileChunkRequest{SessionId: sid, FileId: offered.FileId, Offset: off}}})
		must(err)
		data := res.GetGetSharedFileChunk().GetData()
		if int64(len(data)) != block {
			panic(fmt.Sprintf("block at %d has %d bytes", off, len(data)))
		}
		got = append(got, data...)
	}
	got = got[:offered.SizeBytes]
	if gotSum := sha256.Sum256(got); !bytes.Equal(gotSum[:], offered.Sha256) || !bytes.Equal(got, want) {
		panic("downloaded bytes differ")
	}
	fmt.Printf("PASS %s: %d bytes downloaded in %d encrypted blocks, SHA-256 matches\n", name, len(got), (len(got)+int(block)-1)/int(block))
	_, err = c.Do(ctx, view, &pb.Command{Kind: &pb.Command_StopSession{StopSession: &pb.StopSessionRequest{SessionId: sid}}})
	must(err)
	wait(ctx, name+": copy removed with the session", func() bool {
		_, err := c.Do(ctx, view, &pb.Command{Kind: &pb.Command_GetSharedFileChunk{GetSharedFileChunk: &pb.GetSharedFileChunkRequest{SessionId: sid, FileId: offered.FileId}}})
		return err != nil
	})
}
