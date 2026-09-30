package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// testStream is a stream connection of a logged-in device.
type testStream struct {
	ws *websocket.Conn
}

func (ts *testServer) stream(t *testing.T, token string) *testStream {
	t.Helper()
	ctx := context.Background()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.url, "http")+"/v1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(8 << 20)
	t.Cleanup(func() { ws.CloseNow() })
	hello, _ := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_Hello{Hello: &pb.StreamHello{Token: token}}})
	if err := ws.Write(ctx, websocket.MessageBinary, hello); err != nil {
		t.Fatal(err)
	}
	s := &testStream{ws: ws}
	if f := s.read(t); f.GetReady() == nil {
		t.Fatalf("first frame %v", f)
	}
	return s
}

func (s *testStream) read(t *testing.T) *pb.ServerFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := s.ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f := &pb.ServerFrame{}
	if err := proto.Unmarshal(b, f); err != nil {
		t.Fatal(err)
	}
	return f
}

// next returns the next frame ok accepts, skipping others.
func (s *testStream) next(t *testing.T, ok func(*pb.ServerFrame) bool) *pb.ServerFrame {
	t.Helper()
	for {
		if f := s.read(t); ok(f) {
			return f
		}
	}
}

func (s *testStream) write(t *testing.T, f *pb.PeerFrame) {
	t.Helper()
	b, _ := proto.Marshal(f)
	if err := s.ws.Write(context.Background(), websocket.MessageBinary, b); err != nil {
		t.Fatal(err)
	}
}

func newV2Env(t *testing.T) (*testServer, string, string, remoteagentv1connect.MachineServiceClient, remoteagentv1connect.ClientServiceClient, *testStream) {
	t.Helper()
	ts := newTestServer(t)
	mpriv, mid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	cpriv, cid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	if err != nil {
		t.Fatal(err)
	}
	mtok, ctok := ts.login(t, mpriv, mid), ts.login(t, cpriv, cid)
	machine := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(mtok))
	client := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ctok))
	return ts, mid, cid, machine, client, ts.stream(t, mtok)
}

func TestForwardChecksSenderAndPassesRegistration(t *testing.T) {
	ts, mid, cid, _, client, ms := newV2Env(t)
	ctx := context.Background()
	seal := func(sender string) *pb.Envelope {
		env, err := e2e.Seal(ts.keys.Command, &pb.Header{
			Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND, MachineId: mid, RequestId: "0123456789abcdef0123456789abcdef",
			DeadlineMs: time.Now().Add(time.Minute).UnixMilli(), Sender: sender,
		}, []byte("cmd"))
		if err != nil {
			t.Fatal(err)
		}
		return env
	}
	// Claiming to be another device is refused before reaching the machine.
	_, err := client.Forward(ctx, connect.NewRequest(&pb.ForwardRequest{MachineId: mid, Command: seal(mid)}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("forged sender: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := client.Forward(ctx, connect.NewRequest(&pb.ForwardRequest{MachineId: mid, Command: seal(cid)}))
		done <- err
	}()
	f := ms.next(t, func(f *pb.ServerFrame) bool { return f.GetForwardCall() != nil })
	call := f.GetForwardCall()
	if call.SenderDeviceId != cid {
		t.Fatalf("sender %q, want %q", call.SenderDeviceId, cid)
	}
	reg := &pb.DeviceRegistration{}
	if err := proto.Unmarshal(call.SenderRegistration, reg); err != nil || e2e.DeviceID(reg.DevicePublicKey) != cid ||
		!e2e.VerifyRegistration(ts.keys.AccountPublicKey(), call.SenderRegistration, call.SenderRegistrationSignature) {
		t.Fatalf("registration does not check out: %v", err)
	}
	res, _ := e2e.Seal(ts.keys.Command, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND_RESULT, MachineId: mid}, []byte("ok"))
	ms.write(t, &pb.PeerFrame{Kind: &pb.PeerFrame_ForwardReply{ForwardReply: &pb.ForwardReply{CallId: call.CallId, Result: res}}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
