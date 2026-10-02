package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestUnauthenticatedStreamHelloConnectionsAreBounded(t *testing.T) {
	ts := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	base := "ws" + strings.TrimPrefix(ts.url, "http")
	var conns []*websocket.Conn
	defer func() {
		for _, conn := range conns {
			conn.CloseNow()
		}
	}()
	for i := range maxPendingStreamHellos {
		path := "/v1/stream"
		if i%2 != 0 {
			path = "/v1/term"
		}
		conn, _, err := websocket.Dial(ctx, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
	}
	for _, path := range []string{"/v1/stream", "/v1/term"} {
		conn, res, err := websocket.Dial(ctx, base+path, nil)
		if conn != nil {
			conn.CloseNow()
		}
		if err == nil || res == nil || res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("extra unauthenticated connection admitted on %s: response=%v err=%v", path, res, err)
		}
	}
	res, err := http.Get(ts.url + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatal("stream flood blocked health endpoint")
	}
	for _, conn := range conns {
		conn.CloseNow()
	}
	deadline := time.Now().Add(time.Second)
	for len(ts.srv.pendingStreamHellos) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("hello slots leaked after disconnect")
		}
		time.Sleep(time.Millisecond)
	}
	conn, _, err := websocket.Dial(ctx, base+"/v1/stream", nil)
	if err != nil {
		t.Fatalf("slots were not reusable: %v", err)
	}
	conn.CloseNow()
}

func TestStreamHelloFrameSizeIsBoundedBeforeAuthentication(t *testing.T) {
	ts := newTestServer(t)
	_, mid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	cp, cid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	token := ts.login(t, cp, cid)
	frame := &pb.PeerFrame{Kind: &pb.PeerFrame_Hello{Hello: &pb.StreamHello{Token: token}}}
	// Unknown fields are legal protobuf. A valid hello with a large unknown
	// field must be refused before allocation at the authenticated frame limit.
	frame.ProtoReflect().SetUnknown(protowire.AppendBytes(protowire.AppendTag(nil, 10000, protowire.BytesType), make([]byte, 8192)))
	main, _ := proto.Marshal(frame)
	term, _ := proto.Marshal(&pb.TermHello{Token: token, MachineId: mid, StreamId: strings.Repeat("x", 8192)})
	for path, data := range map[string][]byte{"/v1/stream": main, "/v1/term": term} {
		t.Run(path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.url, "http")+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if err := conn.Write(ctx, websocket.MessageBinary, data); err != nil {
				t.Fatal(err)
			}
			_, _, err = conn.Read(ctx)
			if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
				t.Fatalf("oversized hello was accepted: %v", err)
			}
		})
	}
}

func TestAuthenticatedStreamCountsAreBoundedAndReusable(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	var releases []context.CancelFunc
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	var lastToken string
	for i := 0; i < maxTotalStreams/maxDeviceStreams; i++ {
		cp, cid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
		if err != nil {
			t.Fatal(err)
		}
		token := ts.login(t, cp, cid)
		lastToken = token
		for range maxDeviceStreams {
			_, _, release, err := ts.srv.beginStream(ctx, token)
			if err != nil {
				t.Fatal(err)
			}
			releases = append(releases, release)
		}
		if _, _, release, err := ts.srv.beginStream(ctx, token); connect.CodeOf(err) != connect.CodeResourceExhausted {
			if release != nil {
				release()
			}
			t.Fatalf("per-device stream limit not enforced: %v", err)
		}
	}
	cp, cid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	if _, _, release, err := ts.srv.beginStream(ctx, ts.login(t, cp, cid)); connect.CodeOf(err) != connect.CodeResourceExhausted {
		if release != nil {
			release()
		}
		t.Fatalf("global stream limit not enforced: %v", err)
	}
	releases[len(releases)-1]()
	_, _, release, err := ts.srv.beginStream(ctx, lastToken)
	if err != nil {
		t.Fatalf("released stream slot not reusable: %v", err)
	}
	release()
}
