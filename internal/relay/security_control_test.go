package relay

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

type securityProbeLocal struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	calls atomic.Int32
}

func (b *securityProbeLocal) Send(context.Context, *connect.Request[pb.SendRequest]) (*connect.Response[pb.SendResponse], error) {
	b.calls.Add(1)
	return connect.NewResponse(&pb.SendResponse{QueuePosition: 1}), nil
}

type securityProbeBackend struct {
	Backend
	local *securityProbeLocal
}

func (b *securityProbeBackend) SessionIDs() []string                            { return []string{"synthetic-session"} }
func (b *securityProbeBackend) Local() remoteagentv1connect.LocalServiceHandler { return b.local }

// Uses the actual conn.handleCall and fake Local.Send acceptance boundary. The
// client and attacker use newly generated synthetic keys, never user state.
func TestAuthorityControlAcceptsMemberAndRejectsRevokedRootColludingWithRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	replies := make(chan *pb.ForwardReply, 8)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			_, raw, err := ws.Read(ctx)
			if err != nil {
				return
			}
			frame := &pb.PeerFrame{}
			if proto.Unmarshal(raw, frame) == nil {
				replies <- frame.GetForwardReply()
			}
		}
	}))
	defer ts.Close()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	root := e2e.NewKey()
	keys, err := e2e.Derive(root)
	if err != nil {
		t.Fatal(err)
	}
	pub, ed, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	x, xpub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	machine, err := deviceauth.NewMember(pub, xpub, deviceauth.Machine)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	a, err := deviceauth.InitAuthority(filepath.Join(dir, "authority.db"), e2e.AccountID(keys.AccountPublicKey()), machine)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	pin, state := a.Snapshot()
	endpoint, err := deviceauth.InitEndpoint(filepath.Join(dir, "machine.db"), pin, state, machine.ID, ed.Seed(), x)
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	exchange := func(_ context.Context, c deviceauth.Capsule) (deviceauth.Capsule, error) { return a.Handle(c) }
	machineRemote := &deviceauth.Remote{Endpoint: endpoint, Exchange: exchange}
	cp, ce, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	cx, cxp, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	member, err := deviceauth.NewMember(cp, cxp, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := a.BeginPair("synthetic-pair-context", machine.ID, member)
	if err != nil {
		t.Fatal(err)
	}
	approve, err := deviceauth.SignPair(pair, ed, false)
	if err != nil {
		t.Fatal(err)
	}
	accept, err := deviceauth.SignPair(pair, ce, true)
	if err != nil {
		t.Fatal(err)
	}
	state, err = a.ApprovePair(pair.ID, approve, accept)
	if err != nil {
		t.Fatal(err)
	}
	clientEndpoint, err := deviceauth.InitEndpoint(filepath.Join(dir, "client.db"), pin, state, member.ID, ce.Seed(), cx)
	if err != nil {
		t.Fatal(err)
	}
	defer clientEndpoint.Close()
	client := deviceauth.Remote{Endpoint: clientEndpoint, Exchange: exchange}
	if err := machineRemote.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	backend := &securityProbeLocal{}
	c := &conn{acct: &Account{DeviceID: machine.ID}, keys: keys, ws: ws, r: &Relay{backend: &securityProbeBackend{local: backend}}, security: &securitySession{remote: machineRemote, authority: a}}
	cmd := &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "synthetic-session", Text: "synthetic probe"}}}
	plain, err := proto.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	h := deviceauth.Header{Kind: 5, Purpose: deviceauth.Command, Machine: machine.ID, Session: "synthetic-session", Request: "synthetic-valid-request", Deadline: uint64(time.Now().Add(time.Minute).UnixMilli())}
	wire, err := client.Seal(ctx, h, nil, plain)
	if err != nil {
		t.Fatal(err)
	}
	env, err := wire.Envelope()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		c.handleCall(ctx, &pb.ForwardCall{CallId: 1, Command: env})
		reply := <-replies
		if reply.Result == nil {
			t.Fatal(reply.Error)
		}
		rw, err := deviceauth.FromEnvelope(reply.Result)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := client.OpenCurrent(ctx, rw, nil); err != nil {
			t.Fatal(err)
		}
	}
	if backend.calls.Load() != 1 {
		t.Fatal("valid member operation repeated")
	}
	attackerPub, attackerKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	attackerID := e2e.DeviceID(attackerPub)
	reg, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: keys.AccountPublicKey(), DevicePublicKey: attackerPub, Kind: pb.DeviceKind_DEVICE_KIND_CLIENT})
	oldEnv, err := e2e.SealMessage(keys.Command, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND, MachineId: machine.ID, SessionId: "synthetic-session", RequestId: "synthetic-attacker-request", Sender: attackerID, DeadlineMs: time.Now().Add(time.Minute).UnixMilli()}, cmd)
	if err != nil {
		t.Fatal(err)
	}
	e2e.SignEnvelope(attackerKey, oldEnv)
	if _, err := e2e.VerifySender(keys.AccountPublicKey(), oldEnv, attackerID, reg, keys.SignRegistration(reg)); err != nil {
		t.Fatal("fixture must pass old root-only verifier", err)
	}
	c.handleCall(ctx, &pb.ForwardCall{CallId: 2, Command: oldEnv, SenderDeviceId: attackerID, SenderRegistration: reg, SenderRegistrationSignature: keys.SignRegistration(reg)})
	if (<-replies).Result != nil || backend.calls.Load() != 1 {
		t.Fatal("root-invented identity reached backend")
	}
	sh, _ := state.Hash()
	req := deviceauth.DeleteRequest{Account: pin.Account, Domain: pin.Domain, Requester: member.ID, Operation: "synthetic-removal-op", Expected: sh, Nonce: deviceauth.Hash{9}, ExactIDs: true, Targets: []deviceauth.Target{{ID: member.ID}}}
	if err := req.Sign(ce); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Delete(req); err != nil {
		t.Fatal(err)
	}
	if err := machineRemote.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Completion(req.Operation); err != nil {
		t.Fatal(err)
	}
	c.handleCall(ctx, &pb.ForwardCall{CallId: 3, Command: env})
	if (<-replies).Result != nil || backend.calls.Load() != 1 {
		t.Fatal("delayed revoked control reached backend after completion")
	}
}
