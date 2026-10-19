package relay_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	localclient "github.com/Lewin671/remote-agent/internal/client"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/server"
	"google.golang.org/protobuf/proto"
)

// Explicit opt-in: this test builds and runs the real daemon, isolated tmux
// and a fake native CLI. No normal account, CLI home or tmux socket is used.
// RA_LIVE_E2E=1 go test ./internal/relay -run TestLiveDaemonPhoneAuthority -v -timeout 8m
func TestLiveDaemonPhoneAuthority(t *testing.T) {
	if os.Getenv("RA_LIVE_E2E") != "1" {
		t.Skip("set RA_LIVE_E2E=1 for the isolated real-daemon acceptance pass")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp("/tmp", "ra-live-")
	mustLive(t, err)
	defer os.RemoveAll(dir)
	machineDir := filepath.Join(dir, "machine")
	mustLive(t, os.MkdirAll(machineDir, 0700))
	bin := filepath.Join(dir, "remote-agent")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/remote-agent")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	cli := filepath.Join(dir, "fake-codex")
	mustLive(t, os.WriteFile(cli, []byte(liveFakeCLI), 0700))
	socket := fmt.Sprintf("ra-live-%d", time.Now().UnixNano())
	env := append(os.Environ(), "REMOTE_AGENT_HOME="+machineDir, "REMOTE_AGENT_TMUX_SOCKET="+socket, "CODEX_HOME="+filepath.Join(dir, "codex"), "CLAUDE_CONFIG_DIR="+filepath.Join(dir, "claude"), "REMOTE_AGENT_CLAUDE="+cli)
	defer exec.Command("tmux", "-L", socket, "kill-server").Run()
	dbPath := filepath.Join(dir, "relay.db")
	srv, err := server.New(server.Config{DBPath: dbPath, SetupToken: "throwaway-setup"})
	mustLive(t, err)
	defer srv.Close()
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	db, err := sql.Open("sqlite", dbPath)
	mustLive(t, err)
	defer db.Close()
	phone, err := remoteclient.NewAuthority(ctx, hs.URL, "throwaway-setup")
	mustLive(t, err)
	_, clientEd, err := ed25519.GenerateKey(nil)
	mustLive(t, err)
	clientX, _, err := deviceauth.NewXKey()
	mustLive(t, err)
	clientMember, err := phone.Enroll(ctx, clientEd, clientX, deviceauth.Client)
	mustLive(t, err)
	client := clientMember.Client(phone)
	defer client.Close()
	_, nextEd, err := ed25519.GenerateKey(nil)
	mustLive(t, err)
	nextX, _, err := deviceauth.NewXKey()
	mustLive(t, err)
	nextPhone, err := phone.Enroll(ctx, nextEd, nextX, deviceauth.Client)
	mustLive(t, err)
	defer nextPhone.Endpoint.Close()
	// Pairing publishes the new head with a genuine short, separately signed
	// certificate. Expiry below uses wall time, without changing a product clock.
	machine := livePairCLI(t, ctx, phone, bin, env, 20*time.Second, "")
	mustLive(t, os.WriteFile(filepath.Join(machineDir, "agent.env"), []byte("CODEX_HOME="+filepath.Join(dir, "codex")+"\nZDOTDIR="+filepath.Join(dir, "shell")+"\n"), 0600))
	phoneAPI := liveAPI(hs.URL, phone.Token)
	mustLive(t, client.Sync(ctx))
	daemonLog, err := os.Create(filepath.Join(dir, "daemon.log"))
	mustLive(t, err)
	defer daemonLog.Close()
	defer func() {
		if t.Failed() {
			for _, name := range []string{"daemon.log", "machine/fake-cli.log"} {
				raw, _ := os.ReadFile(filepath.Join(dir, name))
				if len(raw) > 4000 {
					raw = raw[len(raw)-4000:]
				}
				t.Logf("%s: %s", name, raw)
			}
		}
	}()
	var process *exec.Cmd
	stop := func() {
		if process != nil {
			_ = process.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- process.Wait() }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = process.Process.Kill()
				<-done
			}
			process = nil
		}
	}
	defer stop()
	start := func() {
		process = exec.CommandContext(ctx, bin, "daemon", "--codex", cli, "--claude", cli)
		process.Env = env
		process.Stdout = daemonLog
		process.Stderr = daemonLog
		mustLive(t, process.Start())
		liveEventually(t, ctx, "daemon online", func() bool {
			ms, e := client.Machines(ctx)
			if e != nil {
				return false
			}
			for _, m := range ms {
				if m.ID == machine && m.Online {
					return true
				}
			}
			return false
		})
	}
	start()
	request := remoteclient.NewRequestID()
	commandDeadline := time.Now().Add(9 * time.Minute)
	create := &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{Cwd: dir, Agent: pb.Agent_AGENT_CODEX, Env: map[string]string{"PATH": os.Getenv("PATH"), "CODEX_HOME": filepath.Join(dir, "codex")}}}}
	sealed, err := client.Seal(machine, "", request, commandDeadline, create)
	mustLive(t, err)
	created, err := client.Forward(ctx, machine, sealed)
	mustLive(t, err)
	sid := created.GetCreateSession().GetSession().GetId()
	if sid == "" {
		t.Fatal("no created session")
	}
	session := liveSession(t, ctx, client, sid)
	term, err := client.OpenTerminal(ctx, session)
	mustLive(t, err)
	defer term.Close()
	liveTerminalContains(t, ctx, term, "FAKE-CLI-READY")
	mustLive(t, term.Send(ctx, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("hello-live\r")}}))
	liveTerminalContains(t, ctx, term, "GOT:hello-live")
	liveEventually(t, ctx, "CLI events uploaded", func() bool { s := liveFindSession(ctx, client, sid); return s != nil && s.LastSeq >= 3 })
	t.Log("PASS join: real account join + four-picture confirmation; ordinary client enrollment; CreateSession and terminal hello-live/GOT round trip through relay, daemon, tmux and fake CLI")

	// After expiry, the client can still open stored history, but a pre-sealed
	// new command must not execute and local native activity must not upload.
	paused, err := client.Seal(machine, sid, remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_RenameSession{RenameSession: &pb.RenameSessionRequest{SessionId: sid, Title: "must-not-execute"}}})
	mustLive(t, err)
	expiry := phone.Lease.NotAfter
	liveEventually(t, ctx, "wall-clock lease expiry", func() bool { return uint64(time.Now().UnixMilli()) > expiry })
	rawBefore, err := client.API().ListRemoteSessions(ctx, connect.NewRequest(&pb.ListRemoteSessionsRequest{}))
	mustLive(t, err)
	var before *pb.RemoteSession
	for _, s := range rawBefore.Msg.Sessions {
		if s.SessionId == sid {
			before = s
		}
	}
	if before == nil {
		t.Fatal("missing stored session")
	}
	shortCtx, shortCancel := context.WithTimeout(ctx, 2*time.Second)
	_, forwardErr := client.API().Forward(shortCtx, connect.NewRequest(&pb.ForwardRequest{MachineId: machine, Command: paused}))
	shortCancel()
	if forwardErr == nil {
		t.Fatal("expired control returned success")
	}
	local := liveLocal(machineDir)
	localInfo, err := local.GetSession(ctx, connect.NewRequest(&pb.GetSessionRequest{SessionId: sid}))
	mustLive(t, err)
	if localInfo.Msg.Session.Title == "must-not-execute" {
		t.Fatal("control executed after expiry")
	}
	_, err = local.RenameSession(ctx, connect.NewRequest(&pb.RenameSessionRequest{SessionId: sid, Title: "buffered-native-title"}))
	mustLive(t, err)
	// Native tmux input keeps working without the remote control lease.
	input := exec.CommandContext(ctx, "tmux", "-L", socket, "send-keys", "-t", localInfo.Msg.Session.TmuxSession, "buffered-native-event", "Enter")
	if out, e := input.CombinedOutput(); e != nil {
		t.Fatalf("native input: %v %s", e, out)
	}
	liveEventually(t, ctx, "native event recorded locally", func() bool {
		r, e := local.GetSession(ctx, connect.NewRequest(&pb.GetSessionRequest{SessionId: sid}))
		return e == nil && r.Msg.Session.LastSeq > before.LastSeq
	})
	// Wait through an ordinary session sync interval, checking every poll.
	until := time.Now().Add(7 * time.Second)
	for time.Now().Before(until) {
		r, e := client.API().ListRemoteSessions(ctx, connect.NewRequest(&pb.ListRemoteSessionsRequest{}))
		mustLive(t, e)
		for _, s := range r.Msg.Sessions {
			if s.SessionId == sid && (!proto.Equal(s.Meta, before.Meta) || s.LastSeq != before.LastSeq) {
				t.Fatal("new content uploaded after expiry")
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	evs, err := client.API().GetEvents(ctx, connect.NewRequest(&pb.GetEventsRequest{SessionId: sid}))
	mustLive(t, err)
	if len(evs.Msg.Events) == 0 {
		t.Fatal("no retained history")
	}
	for _, event := range evs.Msg.Events {
		w, e := deviceauth.FromEnvelope(event.Envelope)
		mustLive(t, e)
		_, _, e = clientMember.Endpoint.Open(w, session.Key, false, uint64(time.Now().UnixMilli()))
		mustLive(t, e)
	}
	head := phone.States[len(phone.States)-1]
	headRaw, _ := head.MarshalBinary()
	renewed, err := deviceauth.BuildLease(head, phone.Ed, uint64(time.Now().UnixMilli()))
	mustLive(t, err)
	lr, _ := renewed.MarshalBinary()
	_, err = phoneAPI.PutSecurityState(ctx, connect.NewRequest(&pb.PutSecurityStateRequest{Lease: lr}))
	mustLive(t, err)
	mustLive(t, client.Sync(ctx))
	liveEventually(t, ctx, "renewal resumes buffered content", func() bool {
		s := liveFindSession(ctx, client, sid)
		return s != nil && s.Info.Title == "buffered-native-title" && s.LastSeq > before.LastSeq
	})
	chain, err := phoneAPI.GetSecurityStates(ctx, connect.NewRequest(&pb.GetSecurityStatesRequest{AfterEpoch: head.Core.Epoch}))
	mustLive(t, err)
	afterRaw, _ := phone.States[len(phone.States)-1].MarshalBinary()
	if len(chain.Msg.States) != 0 || !bytes.Equal(headRaw, afterRaw) || chain.Msg.HeadEpoch != head.Core.Epoch {
		t.Fatal("renewal changed membership chain")
	}
	_, err = client.Do(ctx, session, &pb.Command{Kind: &pb.Command_RenameSession{RenameSession: &pb.RenameSessionRequest{SessionId: sid, Title: "renewed-control"}}})
	mustLive(t, err)
	t.Log("PASS lease: real signed short certificate expired by wall time; forwarded control refused; local CLI events and title buffered without upload; retained events decrypted; certificate-only renewal left chain unchanged and resumed buffered upload plus control")

	// Planned handoff: the old phone signs, the incoming phone supplies its own
	// independently backed-up recovery key, and old history remains readable.
	_, handoffRecovery, err := ed25519.GenerateKey(nil)
	mustLive(t, err)
	var recoveryPub [32]byte
	copy(recoveryPub[:], handoffRecovery.Public().(ed25519.PublicKey))
	handoffSecret := e2e.NewKey()
	handoff, err := deviceauth.BuildHandoff(head, nextPhone.ID, recoveryPub, phone.Secret, handoffSecret, phone.Ed, uint64(time.Now().UnixMilli()))
	mustLive(t, err)
	handoffLease, err := deviceauth.BuildLease(handoff, nextEd, uint64(time.Now().UnixMilli()))
	mustLive(t, err)
	nextClient := remoteclient.NewClient(hs.URL, nextPhone.Token, nextPhone.ID, nextPhone.Endpoint, nil)
	mustLive(t, nextClient.Sync(ctx))
	livePutState(t, ctx, phoneAPI, handoff, nil, handoffLease)
	mustLive(t, nextPhone.Endpoint.Install([]deviceauth.State{handoff}, []*deviceauth.RecoveryStatement{nil}))
	mustLive(t, nextPhone.Endpoint.InstallLease(handoffLease, uint64(time.Now().UnixMilli())))
	nextAPI := nextClient.API()
	if _, e := phoneAPI.GetSecurityStates(ctx, connect.NewRequest(&pb.GetSecurityStatesRequest{})); e == nil {
		t.Fatal("retiring phone token survived")
	}
	mustLive(t, client.Sync(ctx))
	_, err = nextClient.Do(ctx, session, &pb.Command{Kind: &pb.Command_RenameSession{RenameSession: &pb.RenameSessionRequest{SessionId: sid, Title: "new-phone-control"}}})
	mustLive(t, err)
	handoffSession := liveSession(t, ctx, nextClient, sid)
	history, err := nextClient.Events(ctx, handoffSession, 0)
	mustLive(t, err)
	if len(history) == 0 {
		t.Fatal("handoff lost history")
	}
	t.Log("PASS handoff: retiring-phone signature accepted, incoming phone controls the same native session and decrypts pre-handoff history, retiring phone bearer refused")

	// Recovery uses a new content secret with no historical-secret bridge.
	_, recoveredEd, err := ed25519.GenerateKey(nil)
	mustLive(t, err)
	recoveredX, recoveredXPub, err := deviceauth.NewXKey()
	mustLive(t, err)
	recoveredMember, err := deviceauth.NewMember(recoveredEd.Public().(ed25519.PublicKey), recoveredXPub, deviceauth.Client)
	mustLive(t, err)
	_, newRecovery, err := ed25519.GenerateKey(nil)
	mustLive(t, err)
	var newRecoveryPub [32]byte
	copy(newRecoveryPub[:], newRecovery.Public().(ed25519.PublicKey))
	members := []deviceauth.Member{}
	for _, m := range handoff.Core.Members {
		if m.ID != handoff.Core.AuthorityID {
			members = append(members, m)
		}
	}
	members = append(members, recoveredMember)
	recoverySecret := e2e.NewKey()
	recoveredState, statement, err := deviceauth.BuildRecovery(handoff, recoveredMember, members, recoverySecret, recoveredEd, handoffRecovery, newRecoveryPub, uint64(time.Now().UnixMilli()))
	mustLive(t, err)
	recoveryAPI := remoteagentv1connect.NewRecoveryServiceClient(http.DefaultClient, hs.URL)
	sr, _ := recoveredState.MarshalBinary()
	pr, _ := statement.MarshalBinary()
	submitted, err := recoveryAPI.SubmitRecovery(ctx, connect.NewRequest(&pb.SubmitRecoveryRequest{State: sr, Statement: pr}))
	mustLive(t, err)
	if submitted.Msg.ReadyMs <= uint64(time.Now().Add(47*time.Hour).UnixMilli()) {
		t.Fatal("recovery wait missing")
	}
	// Only this throwaway relay DB's delay is accelerated; product clocks and
	// signatures are unchanged. Completion must still validate the new phone.
	_, err = db.Exec(`UPDATE security_recovery SET ready_ms=0 WHERE recovery_id=?`, submitted.Msg.RecoveryId)
	mustLive(t, err)
	_, err = recoveryAPI.CompleteRecovery(ctx, connect.NewRequest(&pb.CompleteRecoveryRequest{RecoveryId: submitted.Msg.RecoveryId, Signature: ed25519.Sign(recoveredEd, append([]byte("ra3/recovery-complete\x00"), []byte(submitted.Msg.RecoveryId)...))}))
	mustLive(t, err)
	recoveredToken := liveLogin(t, ctx, hs.URL, recoveredMember.ID, recoveredEd)
	recoveredAPI := liveAPI(hs.URL, recoveredToken)
	recoveryLease, err := deviceauth.BuildLease(recoveredState, recoveredEd, uint64(time.Now().UnixMilli()))
	mustLive(t, err)
	lr, _ = recoveryLease.MarshalBinary()
	_, err = recoveredAPI.PutSecurityState(ctx, connect.NewRequest(&pb.PutSecurityStateRequest{Lease: lr}))
	mustLive(t, err)
	states := append(append(append([]deviceauth.State{}, phone.States...), handoff), recoveredState)
	proofs := make([]*deviceauth.RecoveryStatement, len(states))
	proofs[len(proofs)-1] = &statement
	recoveredEndpoint, err := deviceauth.InitEndpoint("", phone.Pin, states, proofs, recoveredMember.ID, recoveredEd.Seed(), recoveredX, recoveryLease)
	mustLive(t, err)
	recoveredClient := remoteclient.NewClient(hs.URL, recoveredToken, recoveredMember.ID, recoveredEndpoint, nil)
	defer recoveredClient.Close()
	if _, e := nextAPI.GetSecurityStates(ctx, connect.NewRequest(&pb.GetSecurityStatesRequest{})); e == nil {
		t.Fatal("old phone survived recovery")
	}
	_, err = recoveredClient.Do(ctx, session, &pb.Command{Kind: &pb.Command_RenameSession{RenameSession: &pb.RenameSessionRequest{SessionId: sid, Title: "recovered-live-session"}}})
	mustLive(t, err)
	var bootstrap *pb.LiveSnapshot
	liveEventually(t, ctx, "recovered checkpoint bootstrap", func() bool {
		s := liveFindSession(ctx, recoveredClient, sid)
		if s == nil || len(s.Info.CurrentCheckpoint) == 0 {
			return false
		}
		cp := &pb.LiveSnapshot{}
		if proto.Unmarshal(s.Info.CurrentCheckpoint, cp) != nil || cp.Session == nil || cp.Session.Title != "recovered-live-session" {
			return false
		}
		bootstrap = cp
		return true
	})
	if bootstrap.Session.LastSeq == 0 {
		t.Fatal("empty bootstrap")
	}
	if _, e := recoveredEndpoint.ContentSecret(head.Core.ContentEpoch); e == nil {
		t.Fatal("recovered phone received historical secret")
	}
	t.Logf("PASS recovery: signed submit -> enforced 48-hour wait -> test-DB delay acceleration -> signed complete; old phone revoked; retained daemon re-sealed current checkpoint with last_seq=%d; new phone bootstrapped without old content key or event prefix", bootstrap.Session.LastSeq)

	// Retry the original executed CreateSession with a fresh nonce/new epoch
	// after a real daemon restart. The client remains a member across recovery.
	stop()
	start()
	mustLive(t, client.Sync(ctx))
	retried, err := client.Seal(machine, "", request, commandDeadline, create)
	mustLive(t, err)
	if bytes.Equal(retried.Ciphertext, sealed.Ciphertext) {
		t.Fatal("retry did not re-seal")
	}
	result, err := client.Forward(ctx, machine, retried)
	mustLive(t, err)
	if result.GetCreateSession().GetSession().GetId() != sid {
		t.Fatal("restart retried command executed again")
	}
	listed, err := client.API().ListRemoteSessions(ctx, connect.NewRequest(&pb.ListRemoteSessionsRequest{}))
	mustLive(t, err)
	if len(listed.Msg.Sessions) != 1 {
		t.Fatalf("retry created %d sessions", len(listed.Msg.Sessions))
	}
	t.Log("PASS restart: stopped/restarted actual daemon; same CreateSession request re-sealed in recovery epoch returned original session ID; exactly one session remained")

	// Removal revokes the machine's credentials but keeps its encrypted archive.
	retained := []deviceauth.Member{}
	for _, m := range recoveredState.Core.Members {
		if m.ID != machine {
			retained = append(retained, m)
		}
	}
	removed := append(append([]string{}, recoveredState.Core.Removed...), machine)
	slices.Sort(removed)
	removedSecret := e2e.NewKey()
	removedState, err := deviceauth.BuildMembershipState(recoveredState, retained, removed, recoverySecret, removedSecret, recoveredEd, uint64(time.Now().UnixMilli()))
	mustLive(t, err)
	removedLease, err := deviceauth.BuildLease(removedState, recoveredEd, uint64(time.Now().UnixMilli()))
	mustLive(t, err)
	livePutState(t, ctx, recoveredAPI, removedState, nil, removedLease)
	mustLive(t, recoveredClient.Sync(ctx))
	archived, err := recoveredAPI.ListRemoteSessions(ctx, connect.NewRequest(&pb.ListRemoteSessionsRequest{}))
	mustLive(t, err)
	if len(archived.Msg.Sessions) != 1 || archived.Msg.Sessions[0].SessionId != sid {
		t.Fatal("removal lost archive")
	}
	archiveWire, err := deviceauth.FromEnvelope(archived.Msg.Sessions[0].Meta)
	mustLive(t, err)
	_, archivePlain, err := recoveredEndpoint.Open(archiveWire, nil, false, uint64(time.Now().UnixMilli()))
	mustLive(t, err)
	archiveInfo := &pb.Session{}
	mustLive(t, proto.Unmarshal(archivePlain, archiveInfo))
	if archiveInfo.Id != sid || len(archiveInfo.CurrentCheckpoint) == 0 {
		t.Fatal("archive metadata unreadable")
	}

	acctData, err := os.ReadFile(filepath.Join(machineDir, "account.json"))
	mustLive(t, err)
	var acct relay.Account
	mustLive(t, json.Unmarshal(acctData, &acct))
	auth := remoteagentv1connect.NewAuthServiceClient(http.DefaultClient, hs.URL)
	ch, e := auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: machine}))
	if e == nil {
		_, e = auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{ProtocolVersion: protocol.Version, DeviceId: machine, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(acct.DeviceKey(), ch.Msg.Challenge)}))
	}
	if e == nil {
		t.Fatal("removed machine logged in")
	}
	oldMachine := machine
	replacementPhone := &remoteclient.Authority{Server: hs.URL, AccountPublicKey: phone.AccountPublicKey, Ed: recoveredEd, Member: recoveredMember, Pin: phone.Pin, States: append(append([]deviceauth.State{}, states...), removedState), Proofs: append(append([]*deviceauth.RecoveryStatement{}, proofs...), nil), Lease: removedLease, Secret: removedSecret, Token: recoveredToken}
	machine = livePairCLI(t, ctx, replacementPhone, bin, env, time.Hour, oldMachine)
	if machine == oldMachine {
		t.Fatal("rejoin restored revoked identity")
	}
	mustLive(t, client.Sync(ctx))
	mustLive(t, recoveredClient.Sync(ctx))
	var continued remoteclient.Session
	liveEventually(t, ctx, "same retained session republished by new identity", func() bool {
		found := liveFindSession(ctx, client, sid)
		if found == nil || found.MachineID != machine || found.Info == nil || len(found.Info.MachineReplacements) != 1 {
			return false
		}
		continued = *found
		return true
	})
	// Read pre-rejoin ciphertext with its original signed machine provenance.
	_, err = client.Events(ctx, continued, 0)
	mustLive(t, err)
	rt, err := client.OpenTerminal(ctx, continued)
	mustLive(t, err)
	defer rt.Close()
	mustLive(t, rt.Send(ctx, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("after-rejoin\r")}}))
	liveTerminalContains(t, ctx, rt, "GOT:after-rejoin")
	liveEventually(t, ctx, "original CLI session continues syncing after rejoin", func() bool {
		current := liveFindSession(ctx, client, sid)
		return current != nil && current.MachineID == machine && current.LastSeq > continued.LastSeq
	})
	check, err := local.GetSession(ctx, connect.NewRequest(&pb.GetSessionRequest{SessionId: sid}))
	mustLive(t, err)
	if check.Msg.Session.TmuxSession != localInfo.Msg.Session.TmuxSession {
		t.Fatal("rejoin replaced the native CLI session")
	}
	ch, e = auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: oldMachine}))
	if e == nil {
		_, e = auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{ProtocolVersion: protocol.Version, DeviceId: oldMachine, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(acct.DeviceKey(), ch.Msg.Challenge)}))
	}
	if e == nil {
		t.Fatal("old identity restored after transfer")
	}
	t.Log("PASS remove/rejoin: authority-signed replacement moved retained rows to a fresh identity; original session ID, tmux CLI and historical ciphertext retained; terminal and new events round trip after rejoin; revoked key still refused")

	// Removal and explicit deletion remain separate after the replacement too.
	latest := replacementPhone.States[len(replacementPhone.States)-1]
	keep := []deviceauth.Member{}
	for _, m := range latest.Core.Members {
		if m.ID != machine {
			keep = append(keep, m)
		}
	}
	gone := append(append([]string{}, latest.Core.Removed...), machine)
	slices.Sort(gone)
	finalState, err := deviceauth.BuildMembershipState(latest, keep, gone, replacementPhone.Secret, e2e.NewKey(), recoveredEd, uint64(time.Now().UnixMilli()))
	mustLive(t, err)
	finalLease, err := deviceauth.BuildLease(finalState, recoveredEd, uint64(time.Now().UnixMilli()))
	mustLive(t, err)
	livePutState(t, ctx, recoveredAPI, finalState, nil, finalLease)
	_, err = recoveredAPI.DeleteMachineRecords(ctx, connect.NewRequest(&pb.DeleteMachineRecordsRequest{MachineId: machine}))
	mustLive(t, err)
	listed, err = recoveredAPI.ListRemoteSessions(ctx, connect.NewRequest(&pb.ListRemoteSessionsRequest{}))
	mustLive(t, err)
	if len(listed.Msg.Sessions) != 0 {
		t.Fatal("explicit deletion retained records")
	}
	ms, err := recoveredAPI.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{}))
	mustLive(t, err)
	for _, m := range ms.Msg.Machines {
		if m.Id == machine {
			t.Fatal("explicit deletion retained machine record")
		}
	}
	t.Log("PASS removal/delete: signed removal refused fresh machine login while retained session archive remained decryptable; explicit authority DeleteMachineRecords removed session and machine records")
}

func mustLive(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func liveEventually(t *testing.T, ctx context.Context, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(85 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: %v", what, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("timed out: %s", what)
}
func liveFindSession(ctx context.Context, c *remoteclient.Client, id string) *remoteclient.Session {
	ss, e := c.Sessions(ctx)
	if e != nil {
		return nil
	}
	for _, s := range ss {
		if s.ID == id {
			return &s
		}
	}
	return nil
}
func liveSession(t *testing.T, ctx context.Context, c *remoteclient.Client, id string) remoteclient.Session {
	t.Helper()
	var got *remoteclient.Session
	liveEventually(t, ctx, "session readable", func() bool { got = liveFindSession(ctx, c, id); return got != nil })
	return *got
}
func liveTerminalContains(t *testing.T, ctx context.Context, term *remoteclient.Term, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var data []byte
	for {
		f, e := term.Read(ctx)
		if e != nil {
			t.Fatalf("terminal waiting for %q: %v; received %q", want, e, data)
		}
		if f.GetClosed() != "" {
			t.Fatalf("terminal closed waiting for %q: %s; received %q", want, f.GetClosed(), data)
		}
		data = append(data, f.GetOutput()...)
		data = append(data, f.GetSnapshot().GetData()...)
		if bytes.Contains(data, []byte(want)) {
			return
		}
	}
}
func liveAPI(url, token string) remoteagentv1connect.ClientServiceClient {
	return remoteagentv1connect.NewClientServiceClient(http.DefaultClient, url, connect.WithInterceptors(bearerFor(token)))
}
func liveLogin(t *testing.T, ctx context.Context, url, id string, ed ed25519.PrivateKey) string {
	t.Helper()
	auth := remoteagentv1connect.NewAuthServiceClient(http.DefaultClient, url)
	ch, err := auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: id}))
	mustLive(t, err)
	res, err := auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{ProtocolVersion: protocol.Version, DeviceId: id, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(ed, ch.Msg.Challenge)}))
	mustLive(t, err)
	return res.Msg.Token
}
func livePutState(t *testing.T, ctx context.Context, api remoteagentv1connect.ClientServiceClient, s deviceauth.State, p *deviceauth.RecoveryStatement, l deviceauth.LeaseCertificate) {
	t.Helper()
	raw, _ := s.MarshalBinary()
	var proof []byte
	if p != nil {
		proof, _ = p.MarshalBinary()
	}
	lease, _ := l.MarshalBinary()
	_, err := api.PutSecurityState(ctx, connect.NewRequest(&pb.PutSecurityStateRequest{States: []*pb.SecurityState{{State: raw, Recovery: proof}}, Lease: lease}))
	mustLive(t, err)
}
func liveLocal(dir string) remoteagentv1connect.LocalServiceClient {
	return localclient.New(filepath.Join(dir, "daemon.sock"))
}

func livePairCLI(t *testing.T, ctx context.Context, phone *remoteclient.Authority, bin string, env []string, duration time.Duration, replaces string) string {
	t.Helper()
	api := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, phone.Server, connect.WithInterceptors(bearerFor(phone.Token)))
	key, err := e2e.NewPairKey()
	mustLive(t, err)
	trust, err := deviceauth.PairTrustDescriptor(phone.Pin, phone.Member.ID)
	mustLive(t, err)
	commit := e2e.PairDomainCommit(key.Public(), trust)
	created, err := api.CreatePairing(ctx, connect.NewRequest(&pb.CreatePairingRequest{Commit: commit}))
	mustLive(t, err)
	cmd := exec.CommandContext(ctx, bin, "account", "join", (relay.PairLink{Code: created.Msg.Code, Commit: commit, Server: phone.Server}).URL("https://example.com"))
	cmd.Env = env
	cmd.Stdin = strings.NewReader("y\n")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	mustLive(t, cmd.Start())
	defer func() {
		if t.Failed() {
			t.Logf("account join output: %s", output.String())
		}
	}()
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	var status *pb.GetPairingResponse
	liveEventually(t, ctx, "machine pairing join", func() bool {
		r, e := api.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: created.Msg.PairingId}))
		if e != nil {
			return false
		}
		status = r.Msg
		return status.State >= pb.PairingState_PAIRING_STATE_JOINED
	})
	candidate, err := deviceauth.NewMember(status.DevicePublicKey, status.DeviceHpkePublicKey, deviceauth.Kind(status.JoinerKind))
	mustLive(t, err)
	approval, sig, err := e2e.NewPairApprovalReplacing(phone.Pin.Account, created.Msg.PairingId, phone.Ed, candidate.EdPublic[:], pb.DeviceKind_DEVICE_KIND_MACHINE, created.Msg.ExpiresMs, replaces)
	mustLive(t, err)
	registration, err := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: phone.AccountPublicKey, DevicePublicKey: candidate.EdPublic[:], Kind: pb.DeviceKind_DEVICE_KIND_MACHINE, CreatedMs: time.Now().UnixMilli()})
	mustLive(t, err)
	auth := remoteagentv1connect.NewAuthServiceClient(http.DefaultClient, phone.Server)
	_, err = auth.RegisterDevice(ctx, connect.NewRequest(&pb.RegisterDeviceRequest{ProtocolVersion: protocol.Version, Registration: registration, Approval: approval, ApprovalSignature: sig}))
	mustLive(t, err)
	secret := e2e.NewKey()
	parent := phone.States[len(phone.States)-1]
	state, err := deviceauth.BuildPairState(parent, candidate, status.DeviceHpkePublicKey, phone.Secret, secret, phone.Ed, uint64(time.Now().UnixMilli()))
	mustLive(t, err)
	lease, err := deviceauth.BuildLease(state, phone.Ed, uint64(time.Now().UnixMilli()))
	mustLive(t, err)
	lease.NotAfter = lease.IssuedAt + uint64(duration.Milliseconds())
	// Marshal gives the canonical signing body; shorten and re-sign exactly
	// the same public certificate format used by every endpoint.
	lr, err := lease.MarshalBinary()
	mustLive(t, err)
	n := int(binary.BigEndian.Uint32(lr[:4]))
	lease.Signature = ed25519.Sign(phone.Ed, lr[4:4+n])
	livePutState(t, ctx, liveAPI(phone.Server, phone.Token), state, nil, lease)
	phone.States = append(phone.States, state)
	phone.Proofs = append(phone.Proofs, nil)
	phone.Secret = secret
	phone.Lease = lease
	_, err = api.RevealPairing(ctx, connect.NewRequest(&pb.RevealPairingRequest{PairingId: created.Msg.PairingId, InitiatorPublic: key.Public(), Trust: trust}))
	mustLive(t, err)
	transcript := e2e.PairDomainTranscript(created.Msg.PairingId, key.Public(), status.JoinerPublic, status.DevicePublicKey, status.DeviceHpkePublicKey, int32(status.JoinerKind), trust)
	secrets, err := key.Derive(status.JoinerPublic, transcript)
	mustLive(t, err)
	chain := []*pb.SecurityState{}
	for i, s := range phone.States {
		raw, _ := s.MarshalBinary()
		entry := &pb.SecurityState{State: raw}
		if i < len(phone.Proofs) && phone.Proofs[i] != nil {
			entry.Recovery, _ = phone.Proofs[i].MarshalBinary()
		}
		chain = append(chain, entry)
	}
	lr, _ = lease.MarshalBinary()
	payload, err := e2e.SealMessage(secrets.Key, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: created.Msg.PairingId, Sender: phone.Member.ID}, &pb.PairingPayload{Server: phone.Server, Approval: approval, ApprovalSignature: sig, SecurityStates: chain, AccountPublicKey: phone.AccountPublicKey, Lease: lr})
	mustLive(t, err)
	_, err = api.ApprovePairing(ctx, connect.NewRequest(&pb.ApprovePairingRequest{PairingId: created.Msg.PairingId, Payload: payload, Approval: approval, ApprovalSignature: sig}))
	mustLive(t, err)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("account join failed: %v %s", err, output.String())
	}
	if !strings.Contains(output.String(), "four pictures") {
		t.Fatal("join did not confirm pictures")
	}
	return candidate.ID
}

const liveFakeCLI = `#!/usr/bin/env python3
import datetime, json, os, pathlib, subprocess, sys, tty, uuid
if '--version' in sys.argv:
    print('codex-cli 0.159.0')
    sys.exit(0)
sys.stderr = open(os.path.join(os.environ['REMOTE_AGENT_HOME'], 'fake-cli.log'), 'a')
cli_dir = pathlib.Path(os.environ['CODEX_HOME'])
cli_dir.mkdir(parents=True, exist_ok=True)
identity = str(uuid.uuid4())
rollout = cli_dir / ('rollout-' + identity + '.jsonl')
def record(kind, payload):
    with rollout.open('a') as stream:
        stream.write(json.dumps({'timestamp': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'type': kind, 'payload': payload}) + '\n')
record('session_meta', {'id': identity, 'session_id': identity, 'cwd': os.getcwd(), 'cli_version': '0.159.0'})
subprocess.run([os.environ['RA_EXE'], 'hook', 'codex'], input=json.dumps({'hook_event_name': 'SessionStart', 'session_id': identity, 'transcript_path': str(rollout)}).encode(), stdout=subprocess.DEVNULL, check=True)
tty.setraw(sys.stdin.fileno())
print('FAKE-CLI-READY\r', flush=True)
buffer = ''
while True:
    char = sys.stdin.read(1)
    if not char:
        break
    if char in '\r\n':
        text = buffer
        buffer = ''
        turn = str(uuid.uuid4())
        record('event_msg', {'type': 'task_started', 'turn_id': turn})
        record('response_item', {'type': 'message', 'role': 'user', 'content': [{'type': 'input_text', 'text': text}]})
        record('event_msg', {'type': 'item_completed', 'item': {'type': 'AgentMessage', 'id': turn, 'content': [{'type': 'Text', 'text': 'GOT:' + text}], 'phase': 'final_answer'}})
        record('event_msg', {'type': 'task_complete', 'turn_id': turn, 'last_agent_message': 'GOT:' + text})
        print('GOT:' + text + '\r', flush=True)
    else:
        buffer += char
`
