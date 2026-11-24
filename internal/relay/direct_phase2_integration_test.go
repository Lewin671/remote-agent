package relay_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/transport/direct"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"github.com/Lewin671/remote-agent/shared/peerwire"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

type phase2LoopbackBackend struct {
	*fakeBackend
	createCalls   atomic.Int32
	sendCalls     atomic.Int32
	approvalCalls atomic.Int32
}

type phase2LoopbackLocal struct {
	remoteagentv1connect.LocalServiceHandler
	b *phase2LoopbackBackend
}

func (b *phase2LoopbackBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return phase2LoopbackLocal{b.fakeBackend.Local(), b}
}

func (l phase2LoopbackLocal) CreateSession(_ context.Context, req *connect.Request[pb.CreateSessionRequest]) (*connect.Response[pb.CreateSessionResponse], error) {
	n := l.b.createCalls.Add(1)
	return connect.NewResponse(&pb.CreateSessionResponse{Session: &pb.Session{Id: "pion-sess-" + req.Msg.Cwd + "-" + string(rune('0'+n))}}), nil
}

func (l phase2LoopbackLocal) Send(_ context.Context, _ *connect.Request[pb.SendRequest]) (*connect.Response[pb.SendResponse], error) {
	l.b.sendCalls.Add(1)
	return connect.NewResponse(&pb.SendResponse{}), nil
}

func (l phase2LoopbackLocal) ResolveApproval(_ context.Context, _ *connect.Request[pb.ResolveApprovalRequest]) (*connect.Response[pb.ResolveApprovalResponse], error) {
	l.b.approvalCalls.Add(1)
	return connect.NewResponse(&pb.ResolveApprovalResponse{}), nil
}

// TestPhase2PionDualPathCommandLoopback establishes real Pion WebRTC direct paths
// through the local relay server and dual-sends byte-identical sealed CreateSession,
// Send, and ResolveApproval envelopes over BOTH Pion DirectPath (use=1) AND Relay
// Forward simultaneously, asserting 0 duplicate side effects and relay failover
// when the direct path closes.
func TestPhase2PionDualPathCommandLoopback(t *testing.T) {
	e := setup(t)
	const session = "pion-phase2-session"
	e.backend.add(t, session)
	backend := &phase2LoopbackBackend{fakeBackend: e.backend}
	baseNow := time.Now()
	directNow := func() time.Time { return baseNow.Add(time.Since(baseNow) / 6) }
	running := e.startRelayBackend(backend, func(r *relay.Relay) {
		relay.SetDirectLoopbackForTest(r)
		relay.SetDirectClockForTest(r, directNow)
	})
	e.online(t, true)
	e.session(t, session, 0)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for !relay.DirectFreshForTest(running) {
		select {
		case <-ctx.Done():
			t.Fatal("relay did not become direct-fresh")
		case <-time.After(10 * time.Millisecond):
		}
	}

	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.http.URL, "http")+"/v1/stream", &websocket.DialOptions{HTTPClient: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(1 << 20)
	hello, _ := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_Hello{Hello: &pb.StreamHello{Token: e.authority.Token}}})
	if err := ws.Write(ctx, websocket.MessageBinary, hello); err != nil {
		t.Fatal(err)
	}
	if _, raw, err := ws.Read(ctx); err != nil {
		t.Fatal(err)
	} else {
		f := &pb.ServerFrame{}
		if proto.Unmarshal(raw, f) != nil || f.GetReady() == nil {
			t.Fatal("expected Ready")
		}
	}

	authority := e.authority
	head := authority.States[len(authority.States)-1]
	hash, _ := head.Hash()
	codec, err := peerwire.NewCodec(authority.Pin.Account, authority.Pin.Domain, hash, head.Core.Epoch, protocol.PeerVersion)
	if err != nil {
		t.Fatal(err)
	}
	box, err := ecdh.X25519().NewPrivateKey(authority.X)
	if err != nil {
		t.Fatal(err)
	}
	member, _ := head.Core.Member(e.acct.DeviceID)
	peerBox, err := ecdh.X25519().NewPublicKey(member.XPublic[:])
	if err != nil {
		t.Fatal(err)
	}
	self := &direct.Identity{ID: []byte(authority.Member.ID), Role: 1, Sign: authority.Ed, Box: box, Instance: make([]byte, 16), Boot: make([]byte, 32)}
	_, _ = rand.Read(self.Instance)
	_, _ = rand.Read(self.Boot)
	peer := peerwire.Public{ID: []byte(member.ID), Role: 2, Sign: member.EdPublic[:], Box: peerBox.Bytes()}
	connection := make([]byte, 16)
	_, _ = rand.Read(connection)

	out := make(chan direct.Outbound, 128)
	var workers sync.WaitGroup
	ready := make(chan struct {
		use  uint64
		path *direct.Path
	}, 8)
	link, err := direct.NewSession(ctx, direct.SessionConfig{
		Self: self, Peer: peer, Codec: codec, Connection: connection,
		Deadline: uint64(directNow().Add(9 * time.Second).UnixMilli()),
		Pool: direct.NewPool(4), Allow: func() bool { return true },
		LoopbackOnly: true, Now: directNow,
		Send: func(frame direct.Outbound) bool {
			select {
			case out <- frame:
				return true
			default:
				return false
			}
		},
		Ready: func(use uint64, path *direct.Path) {
			ready <- struct {
				use  uint64
				path *direct.Path
			}{use, path}
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); ws.CloseNow(); link.Close(); link.Wait(); workers.Wait() }()

	workers.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-out:
				if !frame.Valid() {
					continue
				}
				fields, err := peerwire.Parse(frame.Raw, 5)
				if err != nil {
					return
				}
				packet, _ := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_DirectedPeerFrame{DirectedPeerFrame: &pb.DirectedPeerFrame{ToDevice: fields[2].B, FrameClass: pb.FrameClass(fields[3].U), FrameId: fields[4].B, Sealed: fields[5].B}}})
				if ws.Write(ctx, websocket.MessageBinary, packet) != nil {
					return
				}
			}
		}
	})
	workers.Go(func() {
		for {
			_, raw, err := ws.Read(ctx)
			if err != nil {
				return
			}
			f := &pb.ServerFrame{}
			if proto.Unmarshal(raw, f) != nil {
				return
			}
			if f.GetPeerFrameDelivery() != nil {
				if fields, err := peerwire.Parse(raw, 1); err == nil {
					link.Receive(fields[11].B)
				}
			}
			if r := f.GetPeerFrameResult(); r != nil {
				link.Delivery(r.FrameId, uint32(r.Status), uint64(r.RetryAfterMs), func() bool { return ctx.Err() == nil })
			}
		}
	})

	var readPath *direct.Path
	for readPath == nil {
		select {
		case p := <-ready:
			if p.use == 1 {
				readPath = p.path
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for Pion direct use=1 path")
		}
	}

	ep, err := deviceauth.InitEndpoint("", authority.Pin, authority.States, authority.Proofs, authority.Member.ID, authority.Ed.Seed(), authority.X, authority.Lease)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()

	relayEpoch, err := e.client.RelayEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clientAPI := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, e.http.URL)

	dualSend := func(sess string, cmd *pb.Command) (*pb.CommandResult, *pb.CommandResult) {
		t.Helper()
		cmd.ProtocolVersion = protocol.PeerVersion
		plain, _ := proto.Marshal(cmd)
		var nonce [16]byte
		_, _ = rand.Read(nonce[:])
		reqID := hex.EncodeToString(nonce[:])
		now := uint64(time.Now().UnixMilli())
		h := devicesec.Header{Kind: 5, Purpose: devicesec.Command, Machine: e.acct.DeviceID, Session: sess, Request: reqID, Deadline: now + 60_000}
		sealed, err := ep.Prepare(h, nil, plain)
		if err != nil {
			t.Fatal(err)
		}
		env, err := deviceauth.Envelope(sealed)
		if err != nil {
			t.Fatal(err)
		}
		packet, _ := proto.Marshal(env)

		openRes := func(resEnv *pb.Envelope) *pb.CommandResult {
			w, err := deviceauth.FromEnvelope(resEnv)
			if err != nil {
				t.Fatal(err)
			}
			rh, rp, err := ep.Open(w, nil, true, uint64(time.Now().UnixMilli()))
			if err != nil {
				t.Fatal(err)
			}
			rp, err = deviceauth.Unpack(rh, rp)
			if err != nil {
				t.Fatal(err)
			}
			out := &pb.CommandResult{}
			if err := proto.Unmarshal(rp, out); err != nil {
				t.Fatal(err)
			}
			return out
		}

		directCh := make(chan *pb.CommandResult, 1)
		relayCh := make(chan *pb.CommandResult, 1)
		go func() {
			if err := readPath.Write(ctx, packet); err != nil {
				directCh <- nil
				return
			}
			raw, err := readPath.Read(ctx)
			if err != nil {
				directCh <- nil
				return
			}
			resEnv := &pb.Envelope{}
			if err := proto.Unmarshal(raw, resEnv); err != nil {
				directCh <- nil
				return
			}
			directCh <- openRes(resEnv)
		}()
		go func() {
			var attNonce [16]byte
			_, _ = rand.Read(attNonce[:])
			attempt := hex.EncodeToString(attNonce[:])
			req := connect.NewRequest(&pb.ForwardRequest{
				MachineId:  e.acct.DeviceID,
				AttemptId:  attempt,
				RelayEpoch: relayEpoch,
				Command:    env,
			})
			req.Header().Set("Authorization", "Bearer "+authority.Token)
			req.Header().Set("Remote-Agent-Attempt", attempt)
			req.Header().Set("Remote-Agent-Relay-Epoch", relayEpoch)
			resp, err := clientAPI.Forward(ctx, req)
			if err != nil {
				t.Logf("relay Forward error: %v", err)
				relayCh <- nil
				return
			}
			if resp.Msg.Result == nil {
				t.Logf("relay Forward nil result: %+v", resp.Msg)
				relayCh <- nil
				return
			}
			relayCh <- openRes(resp.Msg.Result)
		}()
		return <-directCh, <-relayCh
	}

	// 1) CreateSession dual-sent over Pion direct + Relay -> executes ONCE, identical session ID returned on both legs.
	dCreate, rCreate := dualSend("", &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{Cwd: "proj", Agent: pb.Agent_AGENT_CODEX}}})
	if dCreate == nil || rCreate == nil || dCreate.Outcome != "executed" || rCreate.Outcome != "executed" {
		t.Fatalf("expected both legs to return executed CreateSession, got direct=%v relay=%v", dCreate, rCreate)
	}
	if dCreate.GetCreateSession().GetSession().GetId() != rCreate.GetCreateSession().GetSession().GetId() {
		t.Fatalf("CreateSession legs returned different session IDs: direct=%q relay=%q", dCreate.GetCreateSession().GetSession().GetId(), rCreate.GetCreateSession().GetSession().GetId())
	}
	if backend.createCalls.Load() != 1 {
		t.Fatalf("expected CreateSession to execute once across Pion+Relay, got %d", backend.createCalls.Load())
	}

	// 2) Send dual-sent -> executes ONCE.
	dSend, rSend := dualSend(session, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: session, Text: "hello-pion"}}})
	if dSend == nil || rSend == nil || dSend.Outcome != "executed" || rSend.Outcome != "executed" || backend.sendCalls.Load() != 1 {
		t.Fatalf("Send dual-path failed: direct=%v relay=%v calls=%d", dSend, rSend, backend.sendCalls.Load())
	}

	// 3) ResolveApproval dual-sent -> executes ONCE.
	dApp, rApp := dualSend(session, &pb.Command{Kind: &pb.Command_ResolveApproval{ResolveApproval: &pb.ResolveApprovalRequest{SessionId: session, ApprovalId: "ap-1", Allow: true}}})
	if dApp == nil || rApp == nil || dApp.Outcome != "executed" || rApp.Outcome != "executed" || backend.approvalCalls.Load() != 1 {
		t.Fatalf("ResolveApproval dual-path failed: direct=%v relay=%v calls=%d", dApp, rApp, backend.approvalCalls.Load())
	}

	// 4) Close Pion direct path mid-stream -> Relay leg still completes cleanly!
	readPath.Close()
	_, rAfterDrop := dualSend(session, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: session, Text: "after-drop"}}})
	if rAfterDrop == nil || rAfterDrop.Outcome != "executed" || backend.sendCalls.Load() != 2 {
		t.Fatalf("Relay failover after direct drop failed: relay=%v calls=%d", rAfterDrop, backend.sendCalls.Load())
	}
}
