package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/client"
)

// TestRPCOverUnixSocket exercises the handler through a real client on a
// unix socket: prefix lookup, streaming with follow, and a blocking hook.
func TestRPCOverUnixSocket(t *testing.T) {
	h := newHarness(t, Config{ApprovalTimeout: time.Minute})
	h.d.sessions[h.s.meta.ID] = h.s

	dir, err := os.MkdirTemp("/tmp", "ra-rpc")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(remoteagentv1connect.NewLocalServiceHandler(&handler{d: h.d}))
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	c := client.New(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got, err := c.GetSession(ctx, connect.NewRequest(&pb.GetSessionRequest{SessionId: "0123"}))
	if err != nil || got.Msg.Session.Id != h.s.meta.ID {
		t.Fatalf("get by prefix: %v %v", got, err)
	}
	if _, err := c.GetSession(ctx, connect.NewRequest(&pb.GetSessionRequest{SessionId: "ffff"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}

	h.s.emit(&pb.Event{Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
	stream, err := c.WatchEvents(ctx, connect.NewRequest(&pb.WatchEventsRequest{SessionId: "0123", Follow: true}))
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() || stream.Msg().Event.Seq != 1 {
		t.Fatalf("first event: %v", stream.Err())
	}

	// A PermissionRequest hook blocks until it is resolved; the request and
	// the resolution arrive on the stream in between.
	h.writeRollout(turnContextLine("user"))
	payload, _ := json.Marshal(permissionPayload(h))
	hookDone := make(chan *pb.HookResponse, 1)
	go func() {
		resp, err := c.Hook(ctx, connect.NewRequest(&pb.HookRequest{SessionId: h.s.meta.ID, Payload: payload}))
		if err != nil {
			t.Error(err)
			hookDone <- nil
			return
		}
		hookDone <- resp.Msg
	}()
	var approvalID string
	for approvalID == "" && stream.Receive() {
		if a := stream.Msg().Event.GetApprovalRequested(); a != nil {
			approvalID = a.ApprovalId
		}
	}
	if approvalID == "" {
		t.Fatalf("no approval on the stream: %v", stream.Err())
	}
	if _, err := c.ResolveApproval(ctx, connect.NewRequest(&pb.ResolveApprovalRequest{
		SessionId: "0123", ApprovalId: approvalID, Allow: true,
	})); err != nil {
		t.Fatal(err)
	}
	resp := <-hookDone
	if resp == nil || string(resp.Output) != `{"hookSpecificOutput":{"decision":{"behavior":"allow"},"hookEventName":"PermissionRequest"}}` {
		t.Fatalf("hook output %v", resp)
	}
	for stream.Receive() {
		if r := stream.Msg().Event.GetApprovalResolved(); r != nil {
			if r.Decision != pb.ApprovalDecision_APPROVAL_DECISION_ALLOW {
				t.Fatalf("decision %v", r.Decision)
			}
			return
		}
	}
	t.Fatalf("stream ended: %v", stream.Err())
}
