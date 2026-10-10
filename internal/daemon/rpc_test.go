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
	"github.com/Lewin671/remote-agent/internal/protocol"
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
	mux.Handle(remoteagentv1connect.NewLocalServiceHandler(&handler{d: h.d}, connect.WithInterceptors(protocol.Local())))
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

// A CLI can send its first hook before the daemon has registered the
// session it just started. The hook waits for the registration instead of
// being told the session does not exist, which would leave a session with
// no later hook without its transcript.
func TestHookWaitsForAStartingSession(t *testing.T) {
	h := newHarness(t, Config{})
	id := h.s.meta.ID
	rollout := filepath.Join(h.dir, "rollout.jsonl")
	payload, _ := json.Marshal(map[string]string{"hook_event_name": "SessionStart", "session_id": "cli-1", "transcript_path": rollout})
	hook := func() error {
		_, err := (&handler{d: h.d}).Hook(context.Background(), connect.NewRequest(&pb.HookRequest{SessionId: id, Payload: payload}))
		return err
	}
	if err := hook(); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("a session nobody is starting: %v", err)
	}
	started := h.d.beginStart(id)
	errc := make(chan error, 1)
	go func() { errc <- hook() }()
	select {
	case err := <-errc:
		t.Fatalf("answered before the session was registered: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	h.d.mu.Lock()
	h.d.sessions[id] = h.s
	h.d.mu.Unlock()
	started()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	h.s.mu.Lock()
	bound := h.s.meta.RolloutPath
	h.s.mu.Unlock()
	if bound != rollout {
		t.Fatalf("transcript %q", bound)
	}
	// A creation that failed answers as before.
	h.d.mu.Lock()
	delete(h.d.sessions, id)
	h.d.mu.Unlock()
	failed := h.d.beginStart(id)
	go func() { errc <- hook() }()
	failed()
	if err := <-errc; connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("after a failed creation: %v", err)
	}
}
