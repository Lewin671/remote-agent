package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"github.com/Lewin671/remote-agent/internal/schedulemcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Exercise the actual MCP adapter and HTTP handler: the tool saves a task,
// dispatch happens asynchronously, and get/list must not invite a duplicate
// run merely because its accepted message's notification failed.
func TestMCPTracksAcceptedScheduledInput(t *testing.T) {
	h := scopedHarness(t, "ask")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mux := http.NewServeMux()
	mux.Handle(remoteagentv1connect.NewLocalServiceHandler(&handler{d: h.d}))
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	server := schedulemcp.New(remoteagentv1connect.NewLocalServiceClient(httpServer.Client(), httpServer.URL), h.s.meta.ID, "test")
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
	call := func(name string, args map[string]any) *schedule.Task {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		return decodeScopedTask(t, b)
	}
	task := call("schedule_create", map[string]any{"request_id": "mcp-stable", "task": taskSpec(schedule.Trigger{Type: "interval", IntervalSeconds: 3600})})
	h.s.log.Close()
	h.d.tickSchedules(time.Now().Add(time.Second))
	task = call("schedule_get", map[string]any{"task_id": task.ID})
	if task.Active == nil || task.Status != "queued" || task.Active.Status != "queued" || !strings.Contains(task.Reason, "queued, but event recording failed") {
		t.Fatalf("MCP exposes accepted run as failure: %+v", task)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "schedule_run", Arguments: map[string]any{"task_id": task.ID}})
	if err != nil || !res.IsError {
		t.Fatalf("MCP allowed a duplicate active execution: %+v %v", res, err)
	}
	if len(h.s.queue.Queue) != 1 || task.Dispatches != 1 {
		t.Fatal("MCP retry duplicated accepted input")
	}
}
