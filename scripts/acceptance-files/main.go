// Run against a throwaway ios/scripts/dev-env.sh start --no-account relay:
// RA_DEV_DIR=<isolated directory> go run ./scripts/acceptance-files
// Real CLI quota is used. Requests go through encrypted Forward; startup
// prompts alone may be answered on the isolated tmux socket. Never uses a
// daily remote-agent account or daemon. Stop the dev-env relay afterwards.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/updater"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
	home, err := os.MkdirTemp(dir, "files-home-")
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
	keys, err := e2e.Derive(auth.Root)
	must(err)
	client := remoteclient.NewClient(server, auth.Token, auth.Member.ID, phone, keys)
	socket := read(filepath.Join(dir, "tmux-socket")) + "-files"
	must(os.Setenv("REMOTE_AGENT_TMUX_SOCKET", socket))
	binary := os.Getenv("RA_DEV_AGENT_BIN")
	if binary == "" {
		binary = filepath.Join(dir, "bin", "remote-agent")
	}
	log, err := os.OpenFile(filepath.Join(dir, "files-daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	must(err)
	defer log.Close()
	newDaemon := func() *exec.Cmd {
		cmd := exec.Command(binary, "daemon")
		// The scenario must use command-name lookup, never a daemon CLI override.
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "REMOTE_AGENT_CLAUDE=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Stdout, cmd.Stderr = log, log
		return cmd
	}
	process := newDaemon()
	must(process.Start())
	defer func() {
		process.Process.Signal(os.Interrupt)
		process.Wait()
		exec.Command("tmux", "-L", socket, "kill-server").Run()
	}()
	wait(ctx, "isolated machine online", func() bool {
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
	})
	runFiles(ctx, client, machine.ID, home, socket)
}

func upload(ctx context.Context, c *remoteclient.Client, machine, name string, data []byte, format pb.ImageFormat) string {
	id := remoteclient.NewRequestID()
	sum := sha256.Sum256(data)
	res, err := c.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_BeginImageUpload{BeginImageUpload: &pb.BeginImageUploadRequest{TransferId: id, FileName: name, TotalBytes: int64(len(data)), Format: format, Sha256: sum[:]}}})
	must(err)
	chunk := int(res.GetBeginImageUpload().GetStatus().GetMaxChunkBytes())
	for off := 0; off < len(data); off += chunk {
		_, err = c.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_PutImageChunk{PutImageChunk: &pb.PutImageChunkRequest{TransferId: id, Offset: int64(off), Data: data[off:min(len(data), off+chunk)]}}})
		must(err)
	}
	res, err = c.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_CompleteImageUpload{CompleteImageUpload: &pb.CompleteImageUploadRequest{TransferId: id}}})
	must(err)
	st := res.GetCompleteImageUpload().GetStatus()
	if name != "" {
		if st.FileId == "" || st.ImageId != "" {
			panic("bad file completion")
		}
		return st.FileId
	}
	if st.ImageId == "" {
		panic("missing image completion")
	}
	return st.ImageId
}

// Approvals here are only for the synthetic fixture reads requested below.
// Resolve them through the same signed client path as a real client.
var approvals = map[string]bool{}

func approve(ctx context.Context, c *remoteclient.Client, s remoteclient.Session, evs []*pb.Event) {
	resolved := map[string]bool{}
	for _, e := range evs {
		if a := e.GetApprovalResolved(); a != nil {
			resolved[a.ApprovalId] = true
		}
	}
	for _, e := range evs {
		if a := e.GetApprovalRequested(); a != nil && !resolved[a.ApprovalId] && !approvals[a.ApprovalId] {
			_, err := c.Do(ctx, s, &pb.Command{Kind: &pb.Command_ResolveApproval{ResolveApproval: &pb.ResolveApprovalRequest{SessionId: s.ID, ApprovalId: a.ApprovalId, Allow: true, ObservedEventSeq: e.Seq}}})
			if err == nil {
				approvals[a.ApprovalId] = true
				fmt.Println("PASS fixture read approval via encrypted client")
			}
		}
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
func runFiles(ctx context.Context, c *remoteclient.Client, machine, home, socket string) {
	cwd, err := os.Getwd()
	must(err)
	var caps *pb.MachineCapabilities
	wait(ctx, "file capability and chunk limits", func() bool {
		ms, err := c.Machines(ctx)
		if err != nil {
			return false
		}
		for _, m := range ms {
			if m.ID == machine {
				caps = m.Meta.GetCapabilities()
				return caps.FileUpload && caps.MaxFileUploadBytes == 100<<20 && caps.MaxFilesPerMessage == 6 && caps.MaxChunkBytes <= 1<<20
			}
		}
		return false
	})
	if os.Getenv("RA_FILES_LARGE") == "1" {
		data := make([]byte, 100<<20)
		_, err := rand.Read(data)
		must(err)
		id := upload(ctx, c, machine, "boundary.bin", data, 0)
		path := filepath.Join(home, "uploads", "pending", id, "boundary.bin")
		got, err := os.ReadFile(path)
		must(err)
		if sha256.Sum256(got) != sha256.Sum256(data) {
			panic("100 MiB file changed")
		}
		fmt.Println("PASS exact 100 MiB random binary via encrypted chunks; stored SHA-256 matches")
	}
	for _, kind := range []pb.Agent{pb.Agent_AGENT_CLAUDE, pb.Agent_AGENT_CODEX} {
		// A new account and session each run; the CLI uses the user's installed
		// authenticated executable/configuration, as the normal acceptance scripts do.
		model := ""
		if kind == pb.Agent_AGENT_CLAUDE {
			model = "claude-haiku-5-5"
		}
		txtID := upload(ctx, c, machine, "note.txt", []byte("FILE_TEXT_TOKEN_ORCHID_482"), 0)
		text := "Read the attached note.txt with your file tools and reply with its exact token only. Do not edit any file."
		res, err := c.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{Cwd: cwd, Agent: kind, Model: model, Prompt: text, FileIds: []string{txtID}, Mode: pb.ApprovalMode_APPROVAL_MODE_ASK}}})
		must(err)
		sid := res.GetCreateSession().GetSession().GetId()
		if sid == "" {
			panic("session missing")
		}
		prefix := ""
		if kind == pb.Agent_AGENT_CLAUDE {
			prefix = "@"
		}
		txtPath := filepath.Join(home, "uploads", sid, "files", txtID, "note.txt")
		var view remoteclient.Session
		started := false
		wait(ctx, kind.String()+" text+txt submitted, attested, read and answered", func() bool {
			s, ok := find(ctx, c, sid)
			if !ok {
				return false
			}
			view = s
			if !started && s.Info.Status == pb.SessionStatus_SESSION_STATUS_STARTING {
				// Startup prompts are not sent messages; answer only known trust/hook UI.
				screen, _ := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-t", "ra-"+sid[:8]+":").Output()
				raw := string(screen)
				if strings.Contains(raw, "Trust this folder") || strings.Contains(raw, "trust this folder") || strings.Contains(raw, "Quick safety check") {
					_ = exec.Command("tmux", "-L", socket, "send-keys", "-t", "ra-"+sid[:8]+":", "Enter").Run()
					fmt.Println("DEBUG startup trust accepted on isolated terminal")
				}
				if strings.Contains(raw, "Hooks need review") {
					_ = exec.Command("tmux", "-L", socket, "send-keys", "-t", "ra-"+sid[:8]+":", "Down", "Enter").Run()
					fmt.Println("DEBUG startup hook review accepted on isolated terminal")
				}
			}
			if s.Info.Status == pb.SessionStatus_SESSION_STATUS_IDLE || s.Info.Status == pb.SessionStatus_SESSION_STATUS_RUNNING {
				started = true
			}
			return messageResult(ctx, c, view, text+"\n\n"+prefix+txtPath, []string{txtID}, nil, "FILE_TEXT_TOKEN_ORCHID_482", 0)
		})
		// The file-only record must contain just these path lines. A subsequent
		// ordinary user instruction asks the agent to inspect both files later.
		csvID := upload(ctx, c, machine, "data.csv", []byte("label,token\nfile,FILE_CSV_TOKEN_MAPLE_739\n"), 0)
		var z bytes.Buffer
		zw := zip.NewWriter(&z)
		f, err := zw.Create("note.txt")
		must(err)
		_, err = f.Write([]byte("FILE_ZIP_TOKEN_CEDAR_615"))
		must(err)
		must(zw.Close())
		zipID := upload(ctx, c, machine, "archive.zip", z.Bytes(), 0)
		after := view.LastSeq
		reqID := remoteclient.NewRequestID()
		_, err = c.Do(ctx, view, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: sid, RequestId: reqID, FileIds: []string{csvID, zipID}}}})
		must(err)
		lines := prefix + filepath.Join(home, "uploads", sid, "files", csvID, "data.csv") + "\n" + prefix + filepath.Join(home, "uploads", sid, "files", zipID, "archive.zip")
		wait(ctx, kind.String()+" files-only csv+zip confirmed and events carry files", func() bool { return messageResult(ctx, c, view, lines, []string{csvID, zipID}, nil, "", after) })
		// Agent can read them after the initial submit, which image LRU cannot guarantee.
		follow := remoteclient.NewRequestID()
		_, err = c.Do(ctx, view, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: sid, RequestId: follow, Text: "Read the data.csv and archive.zip from my previous message using your tools (Python zipfile is fine), and report both FILE_CSV_TOKEN and FILE_ZIP_TOKEN values. Do not edit files."}}})
		must(err)
		wait(ctx, kind.String()+" agent read csv and zip after submission", func() bool {
			evs, err := c.Events(ctx, view, after)
			if err != nil {
				return false
			}
			approve(ctx, c, view, evs)
			var reply string
			for _, e := range evs {
				if m := e.GetAgentMessage(); m != nil {
					reply += m.Text
				}
			}
			return strings.Contains(reply, "FILE_CSV_TOKEN_MAPLE_739") && strings.Contains(reply, "FILE_ZIP_TOKEN_CEDAR_615")
		})
		img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				img.Set(x, y, color.NRGBA{R: 80, G: 120, B: 180, A: 255})
			}
		}
		var pngbuf bytes.Buffer
		must(png.Encode(&pngbuf, img))
		imageID := upload(ctx, c, machine, "", pngbuf.Bytes(), pb.ImageFormat_IMAGE_FORMAT_PNG)
		pdfID := upload(ctx, c, machine, "report.pdf", pdf("FILE_PDF_TOKEN_WILLOW_926"), 0)
		view, _ = find(ctx, c, sid)
		after = view.LastSeq
		mixed := "Ignore the image. Extract text from the attached report.pdf using your tools and reply with its FILE_PDF_TOKEN value only. Do not edit files."
		mixedID := remoteclient.NewRequestID()
		_, err = c.Do(ctx, view, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: sid, RequestId: mixedID, Text: mixed, ImageIds: []string{imageID}, FileIds: []string{pdfID}}}})
		must(err)
		pdfPath := filepath.Join(home, "uploads", sid, "files", pdfID, "report.pdf")
		wait(ctx, kind.String()+" image+pdf+text submitted, confirmed and read", func() bool {
			return messageResult(ctx, c, view, mixed+"\n\n"+prefix+pdfPath, []string{pdfID}, []string{imageID}, "FILE_PDF_TOKEN_WILLOW_926", after)
		})
		// Explicitly stop only this isolated session. Existing exited-session cleanup
		// owns the files; no daily CLI session or remote-agent state is touched.
		_, err = c.Do(ctx, view, &pb.Command{Kind: &pb.Command_StopSession{StopSession: &pb.StopSessionRequest{SessionId: sid}}})
		must(err)
		fmt.Println("PASS", kind.String(), "all three scenarios; session stopped")
	}
}
func messageResult(ctx context.Context, c *remoteclient.Client, s remoteclient.Session, want string, fileIDs, imageIDs []string, token string, after uint64) bool {
	evs, err := c.Events(ctx, s, after)
	if err != nil {
		return false
	}
	approve(ctx, c, s, evs)
	match, reply, confirmed := false, token == "", false
	request := ""
	for _, e := range evs {
		if m := e.GetUserMessage(); m != nil {
			text := m.Text
			for _, ref := range m.Images {
				text = strings.Replace(text, fmt.Sprintf("[Image #%d]", ref.CliIndex), "", 1)
			}
			if strings.TrimSpace(text) == want {
				if len(m.Files) != len(fileIDs) || len(m.Images) != len(imageIDs) {
					panic("wrong attachment counts")
				}
				for i, id := range fileIDs {
					if m.Files[i].FileId != id || m.Files[i].SizeBytes == 0 {
						panic("wrong file attestation")
					}
				}
				for i, id := range imageIDs {
					if m.Images[i].UploadId != id {
						panic("wrong image upload attestation")
					}
				}
				match = true
				request = m.RequestId
			}
		}
		if m := e.GetAgentMessage(); m != nil && strings.Contains(m.Text, token) {
			reply = true
		}
	}
	for _, e := range evs {
		if st := e.GetInputState(); st != nil && st.RequestId == request && request != "" {
			if st.Status == pb.InputStatus_INPUT_STATUS_FAILED || st.Status == pb.InputStatus_INPUT_STATUS_NEEDS_TERMINAL {
				panic("message failed: " + st.Reason)
			}
			if st.Status == pb.InputStatus_INPUT_STATUS_SUBMITTED {
				confirmed = true
			}
		}
	}
	return match && reply && confirmed
}
func pdf(text string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 400 200] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	stream := "BT /F1 12 Tf 20 100 Td (" + text + ") Tj ET\n"
	objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream))
	for i, obj := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, off := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return b.Bytes()
}
