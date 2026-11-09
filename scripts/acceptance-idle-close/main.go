// Run only against ios/scripts/dev-env.sh's throwaway relay:
// RA_DEV_DIR=<isolated directory> go run ./scripts/acceptance-idle-close
// A synthetic client over encrypted Forward and a real Claude Haiku CLI:
// the idle close setting round-trips, an idle session is closed with
// idle_closed, and a message sent to it afterwards resumes it.
package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	auth, err := remoteclient.NewAuthority(ctx, server, read(filepath.Join(dir, "setup-token")))
	must(err)
	_, ed, err := ed25519.GenerateKey(nil)
	must(err)
	x, _, err := deviceauth.NewXKey()
	must(err)
	machine, err := auth.Enroll(ctx, ed, x, deviceauth.Machine)
	must(err)
	home := filepath.Join(dir, "idle-home")
	must(os.MkdirAll(home, 0700))
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
	socket := read(filepath.Join(dir, "tmux-socket")) + "-idle"
	must(os.Setenv("REMOTE_AGENT_TMUX_SOCKET", socket))
	log, err := os.OpenFile(filepath.Join(dir, "idle-daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	must(err)
	defer log.Close()
	var process *exec.Cmd
	start := func() {
		process = exec.Command(filepath.Join(dir, "bin", "remote-agent"), "daemon")
		process.Stdout, process.Stderr = log, log
		must(process.Start())
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
	}
	stop := func() {
		must(process.Process.Signal(os.Interrupt))
		process.Wait()
	}
	start()
	defer func() {
		process.Process.Signal(os.Interrupt)
		process.Wait()
		exec.Command("tmux", "-L", socket, "kill-server").Run()
	}()

	// The setting through the client's encrypted machine commands.
	settings := func() *pb.DaemonSettings {
		r, e := client.DoMachine(ctx, machine.ID, &pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})
		must(e)
		return r.GetGetSettings().GetSettings()
	}
	update := func(st *pb.DaemonSettings) (*pb.DaemonSettings, error) {
		r, e := client.DoMachine(ctx, machine.ID, &pb.Command{Kind: &pb.Command_UpdateSettings{UpdateSettings: &pb.UpdateSettingsRequest{Settings: st}}})
		if e != nil {
			return nil, e
		}
		if r.GetErrorMessage() != "" {
			return nil, fmt.Errorf("%s", r.GetErrorMessage())
		}
		return r.GetUpdateSettings().GetSettings(), nil
	}
	st := settings()
	if st.IdleCloseMinutes != 360 {
		panic(fmt.Sprintf("default idle close = %d", st.IdleCloseMinutes))
	}
	fmt.Println("PASS client reads the default of 6 hours")
	st.IdleCloseMinutes = 30
	if _, e := update(st); e == nil {
		panic("30 minutes accepted")
	}
	st.IdleCloseMinutes = -1
	got, e := update(st)
	must(e)
	if got.IdleCloseMinutes != -1 || settings().IdleCloseMinutes != -1 {
		panic("off did not stick")
	}
	st.IdleCloseMinutes = 120
	got, e = update(st)
	must(e)
	if got.IdleCloseMinutes != 120 {
		panic("2 hours did not stick")
	}
	fmt.Println("PASS client sets off and 2 hours; rejects 30 minutes")

	// Below the client's minimum only through the file, to keep this short.
	stop()
	must(os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"idleCloseMinutes":1}`), 0600))
	start()

	cwd, err := os.Getwd()
	must(err)
	r, err := client.DoMachine(ctx, machine.ID, &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{
		Cwd: cwd, Agent: pb.Agent_AGENT_CLAUDE, Model: "haiku", Mode: pb.ApprovalMode_APPROVAL_MODE_ASK,
		Prompt: "This is an isolated acceptance check. Reply with exactly: first reply. Use no tools.",
	}}})
	must(err)
	if r.GetErrorMessage() != "" {
		panic(r.GetErrorMessage())
	}
	sid := r.GetCreateSession().GetSession().GetId()
	fmt.Println("PASS session created through encrypted Forward:", sid[:8])
	view := func() remoteclient.Session {
		rows, e := client.API().ListRemoteSessions(ctx, connect.NewRequest(&pb.ListRemoteSessionsRequest{SessionIds: []string{sid}}))
		must(e)
		if len(rows.Msg.Sessions) != 1 {
			return remoteclient.Session{}
		}
		v, e := client.OpenSession(rows.Msg.Sessions[0])
		must(e)
		return v
	}
	trust := func() {
		out, _ := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-t", "ra-"+sid[:8]).Output()
		if strings.Contains(string(out), "trust this folder") || strings.Contains(string(out), "Trust this folder") {
			exec.Command("tmux", "-L", socket, "send-keys", "-t", "ra-"+sid[:8], "Enter").Run()
		}
	}
	replied := func(v remoteclient.Session, text string) bool {
		events, e := client.Events(ctx, v, 0)
		must(e)
		for _, ev := range events {
			if m := ev.GetAgentMessage(); m != nil && strings.Contains(strings.ToLower(m.Text), text) {
				return true
			}
		}
		return false
	}
	var idleAt time.Time
	wait(ctx, "real Claude Code answered the first message", func() bool {
		trust()
		v := view()
		if v.Info.GetStatus() == pb.SessionStatus_SESSION_STATUS_IDLE && replied(v, "first reply") {
			idleAt = time.Now()
			return true
		}
		return false
	})
	wait(ctx, "daemon closed the idle session", func() bool {
		return view().Info.GetStatus() == pb.SessionStatus_SESSION_STATUS_EXITED
	})
	if waited := time.Since(idleAt); waited < 50*time.Second {
		panic(fmt.Sprintf("closed after only %s", waited))
	}
	v := view()
	events, err := client.Events(ctx, v, 0)
	must(err)
	var exit *pb.SessionState
	for _, ev := range events {
		if s := ev.GetSessionState(); s.GetStatus() == pb.SessionStatus_SESSION_STATUS_EXITED {
			exit = s
		}
	}
	if exit == nil || exit.Detail != "stopped" || !exit.IdleClosed || exit.ScheduledClosed {
		panic(fmt.Sprintf("exit state = %v", exit))
	}
	if out, _ := exec.Command("tmux", "-L", socket, "has-session", "-t", "=ra-"+sid[:8]).CombinedOutput(); !strings.Contains(string(out), "can't find") && !strings.Contains(string(out), "no server running") {
		panic("tmux session still there: " + string(out))
	}
	fmt.Printf("PASS client sees EXITED with idle_closed after %s idle; tmux session gone\n", time.Since(idleAt).Round(time.Second))

	// A message to the closed session resumes it and is answered.
	r, err = client.Do(ctx, v, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: sid, Text: "Reply with exactly: second reply. Use no tools.", RequestId: "idle-accept-2"}}})
	must(err)
	if r.GetErrorMessage() != "" {
		panic("send to idle-closed session: " + r.GetErrorMessage())
	}
	wait(ctx, "message to the idle-closed session resumed it and was answered", func() bool {
		trust()
		v := view()
		return v.Info.GetStatus() != pb.SessionStatus_SESSION_STATUS_EXITED && replied(v, "second reply")
	})
	wait(ctx, "resumed session closed again once idle", func() bool {
		return view().Info.GetStatus() == pb.SessionStatus_SESSION_STATUS_EXITED
	})
}
