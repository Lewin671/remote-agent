package schedulemcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if len(list.Tools) != 10 {
		t.Fatalf("tool count %d", len(list.Tools))
	}
	for _, tool := range list.Tools {
		if tool.Name == "notify_user" {
			raw, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Properties map[string]any `json:"properties"`
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			if len(schema.Properties) != 1 || schema.Properties["text"] == nil {
				t.Fatalf("unexpected notify schema: %s", raw)
			}
		}
	}
	bad, callErr := cs.CallTool(ctx, &mcp.CallToolParams{Name: "notify_user", Arguments: map[string]any{"text": "result", "title": "Agent title"}})
	if callErr == nil && !bad.IsError {
		t.Fatal("MCP accepted removed title parameter")
	}
	if len(b.requests) != 0 {
		t.Fatal("invalid MCP input reached daemon")
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
	// The list is scoped to this session; its result must say so, or an empty
	// list reads as "no tasks exist". Other operations carry no such note.
	if note, _ := res.StructuredContent.(map[string]any)["scope_note"]; note != nil {
		t.Fatalf("scope note on a non-list result: %v", note)
	}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "schedule_list", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("list %+v %v", res, err)
	}
	if q = <-b.requests; q.Operation != "list" {
		t.Fatalf("wrong list: %+v", q)
	}
	if note, _ := res.StructuredContent.(map[string]any)["scope_note"].(string); note != listScopeNote {
		t.Fatalf("list result lacks the scope note: %+v", res.StructuredContent)
	}
}

type sendBackend struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	scope string
	req   *pb.SendFileRequest
	fail  bool
}

func (b *sendBackend) SendFile(_ context.Context, q *connect.Request[pb.SendFileRequest]) (*connect.Response[pb.SendFileResponse], error) {
	b.scope, b.req = q.Header().Get(schedule.MCPScopeHeader), q.Msg
	if b.fail {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("at most 6 files can be sent in one turn"))
	}
	return connect.NewResponse(&pb.SendFileResponse{File: &pb.SharedFile{FileId: strings.Repeat("ab", 16), Name: "report.pdf", SizeBytes: 12, Sha256: []byte{0xde, 0xad}}}), nil
}

// send_file is a thin client: it binds the session, says what success means
// and passes the daemon's refusal on as a tool error.
func TestSendFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &sendBackend{}
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
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range list.Tools {
		// A read-only hint could let a CLI skip asking in its ask mode.
		if tool.Name == "send_file" && tool.Annotations != nil && tool.Annotations.ReadOnlyHint {
			t.Fatal("send_file claims to be read-only")
		}
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "send_file", Arguments: map[string]any{"path": "out/report.pdf"}})
	if err != nil || res.IsError {
		t.Fatalf("send_file: %+v %v", res, err)
	}
	if b.scope != "bound-session" || b.req.SessionId != "bound-session" || b.req.Path != "out/report.pdf" {
		t.Fatalf("not bound: %q %+v", b.scope, b.req)
	}
	out := res.StructuredContent.(map[string]any)
	if out["file_id"] != strings.Repeat("ab", 16) || out["status"] != "offered" || out["sha256"] != "dead" || out["note"] != sendFileNote {
		t.Fatalf("result: %+v", out)
	}
	// The CLIs record the text content; the daemon finds the ID there.
	if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `"file_id":"`+strings.Repeat("ab", 16)+`"`) {
		t.Fatalf("text content: %s", text)
	}
	b.fail = true
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "send_file", Arguments: map[string]any{"path": "x"}})
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "at most 6 files") {
		t.Fatalf("refusal: %+v %v", res, err)
	}
}
