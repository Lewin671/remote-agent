package schedulemcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type backend struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	requests chan *pb.ScheduleRequest
	scope    string
}

func (b *backend) Schedule(_ context.Context, q *connect.Request[pb.ScheduleRequest]) (*connect.Response[pb.ScheduleResponse], error) {
	b.scope = q.Header().Get(schedule.MCPScopeHeader)
	b.requests <- q.Msg
	return connect.NewResponse(&pb.ScheduleResponse{ResultJson: []byte(`{"id":"created","status":"waiting_turn"}`)}), nil
}
func TestToolDiscoveryAndCreate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &backend{requests: make(chan *pb.ScheduleRequest, 3)}
	mux := http.NewServeMux()
	mux.Handle(remoteagentv1connect.NewLocalServiceHandler(b))
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	server := New(remoteagentv1connect.NewLocalServiceClient(httpServer.Client(), httpServer.URL), "bound-session", "test")
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if cs.InitializeResult().Instructions != "" {
		t.Fatal("injected session instructions")
	}
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 9 {
		t.Fatalf("tool count %d", len(list.Tools))
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "schedule_create", Arguments: map[string]any{"task": map[string]any{"title": "check", "message": "continue", "trigger": map[string]any{"type": "after_turn", "delay_seconds": 30}, "target": map[string]any{"type": "resume_session", "session": "current"}}, "request_id": "stable"}})
	if err != nil || res.IsError {
		t.Fatalf("call: %+v %v", res, err)
	}
	q := <-b.requests
	if b.scope != "bound-session" {
		t.Fatalf("missing MCP scope header: %q", b.scope)
	}
	if q.SessionId != "bound-session" || q.Operation != "create" || q.RequestId != "stable" {
		t.Fatalf("not bound: %+v", q)
	}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "schedule_cancel", Arguments: map[string]any{"task_id": "created", "expected_revision": 1}})
	if err != nil || res.IsError {
		t.Fatalf("cancel %+v %v", res, err)
	}
	q = <-b.requests
	if q.Operation != "cancel" || q.TaskId != "created" || q.ExpectedRevision != 1 {
		t.Fatal("wrong cancel")
	}
}
