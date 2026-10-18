package server

import (
	"context"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRevocationClosesClientAndTerminalStreams(t *testing.T) {
	ts := newTestServer(t)
	_, mid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	cp, cid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	token := ts.login(t, cp, cid)
	stream := ts.stream(t, token)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	term, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.url, "http")+"/v1/term", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer term.CloseNow()
	b, _ := proto.Marshal(&pb.TermHello{Token: token, MachineId: mid, StreamId: "security-stream-1"})
	if err := term.Write(ctx, websocket.MessageBinary, b); err != nil {
		t.Fatal(err)
	}
	// Wait until the terminal hello is authenticated; no fixed delay is needed.
	deadline := time.Now().Add(10 * time.Second)
	for {
		ts.srv.streamMu.Lock()
		n := len(ts.srv.streams[cid])
		ts.srv.streamMu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal was not registered")
		}
		time.Sleep(time.Millisecond)
	}
	// Use a second client as administrator.
	ap, aid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	admin := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, ap, aid)))
	_ = admin
	if err := ts.removeViaState(t, cid); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*websocket.Conn{stream.ws, term} {
		for {
			_, _, err := conn.Read(ctx)
			if err != nil {
				if ctx.Err() != nil {
					t.Fatal("revoked stream stayed open until test deadline")
				}
				break
			}
		}
	}
	if _, _, release, err := ts.srv.beginStream(ctx, token); err == nil {
		release()
		t.Fatal("revoked token reopened a stream")
	}
}

func TestStreamEndsAtTokenExpiry(t *testing.T) {
	ts := newTestServer(t)
	cp, cid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	token := ts.login(t, cp, cid)
	// Opening the stream must not race the expiry itself: leave enough time
	// for the WebSocket handshake and hello on a loaded machine, then wait
	// for the server to close the stream at the deadline it derived.
	if _, err := ts.srv.store.db.Exec(`UPDATE tokens SET expires_ms = ? WHERE hash = ?`, time.Now().Add(2*time.Second).UnixMilli(), hashToken(token)); err != nil {
		t.Fatal(err)
	}
	stream := ts.stream(t, token)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := stream.ws.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("expired stream did not close promptly: %v", err)
	}
}
