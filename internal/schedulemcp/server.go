// Package schedulemcp exposes daemon schedules to native coding CLIs over
// stdio. It supplies tool schemas only: no server instructions or prompts.
package schedulemcp

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type CreateInput struct {
	Task      schedule.Spec `json:"task"`
	RequestID string        `json:"request_id,omitempty" jsonschema:"Optional stable key for retrying this creation without duplicating the task; use the same key for a retry"`
}
type IDInput struct {
	TaskID           string `json:"task_id"`
	ExpectedRevision uint64 `json:"expected_revision,omitempty" jsonschema:"Optional revision from get/list to reject concurrent modifications"`
}
type UpdateInput struct {
	TaskID           string        `json:"task_id"`
	ExpectedRevision uint64        `json:"expected_revision,omitempty"`
	Task             schedule.Spec `json:"task" jsonschema:"Complete replacement definition; an active execution must finish first"`
}
type NotifyInput struct {
	Text  string `json:"text" jsonschema:"Required notification body, up to 200 characters"`
	Title string `json:"title,omitempty" jsonschema:"Optional title, up to 60 characters; defaults to the task title"`
}
type Empty struct{}

// New binds "current" to the Remote Agent session that launched this MCP.
// The MCP is merely a client; all persistence and timing belong to the daemon.
func New(client remoteagentv1connect.LocalServiceClient, sessionID, version string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "remote-agent", Version: version}, nil)
	call := func(ctx context.Context, op, id string, revision uint64, spec any, key string) (*mcp.CallToolResult, map[string]any, error) {
		var b []byte
		var err error
		if spec != nil {
			b, err = json.Marshal(spec)
			if err != nil {
				return nil, nil, err
			}
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		req := connect.NewRequest(&pb.ScheduleRequest{Operation: op, TaskId: id, SessionId: sessionID, SpecJson: b, ExpectedRevision: revision, RequestId: key})
		req.Header().Set(schedule.MCPScopeHeader, sessionID)
		res, err := client.Schedule(ctx, req)
		if err != nil {
			// Keep tool errors concise. Never log requests, messages or environment.
			var ce *connect.Error
			if errors.As(err, &ce) {
				return nil, nil, errors.New(ce.Message())
			}
			return nil, nil, errors.New("cannot reach the Remote Agent daemon")
		}
		var result map[string]any
		if err = json.Unmarshal(res.Msg.ResultJson, &result); err != nil {
			return nil, nil, errors.New("invalid daemon task response")
		}
		return nil, result, nil
	}
	mcp.AddTool(server, &mcp.Tool{Name: "schedule_create", Description: "Persist a future Agent message in Remote Agent, visible through its task API. Supports a timestamp, fixed interval (minimum 300 seconds), calendar time in an IANA timezone, or delay (repeating minimum 60 seconds) after the current turn ends. Resume only this conversation (target.type=resume_session, session=current) or start a fresh one in the same project and CLI with the exact same standard approval mode (new_session). Plan and unknown/custom permissions cannot delegate tasks. Tasks survive CLI exit and daemon restart; this computer must be running to execute. During a scheduled execution you cannot create, update, resume or run tasks. Recurrence may end at max_runs or until. Only a successful result means the task was saved. After an ambiguous connection error, list tasks before retrying or reuse a stable request_id."}, func(ctx context.Context, _ *mcp.CallToolRequest, in CreateInput) (*mcp.CallToolResult, map[string]any, error) {
		return call(ctx, "create", "", 0, &in.Task, in.RequestID)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "schedule_list", Description: "List this bound session's MCP-created Remote Agent scheduled tasks, including status, next execution time, revisions and execution history."}, func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, map[string]any, error) {
		return call(ctx, "list", "", 0, nil, "")
	})
	mcp.AddTool(server, &mcp.Tool{Name: "schedule_update", Description: "Replace a Remote Agent scheduled task definition; the active execution must finish first. Use expected_revision to avoid overwriting concurrent edits."}, func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateInput) (*mcp.CallToolResult, map[string]any, error) {
		return call(ctx, "update", in.TaskID, in.ExpectedRevision, &in.Task, "")
	})
	for _, item := range []struct{ op, description string }{
		{"get", "Read a Remote Agent scheduled task and its execution history."},
		{"pause", "Pause future scheduled executions. Does not interrupt or withdraw an already-dispatched message."},
		{"resume", "Resume a paused Remote Agent scheduled task."},
		{"cancel", "Cancel future scheduled executions, retaining history. Does not interrupt or withdraw an already-dispatched message."},
		{"run", "Request one extra execution using this task's normal target, for every trigger type. Waits behind an active execution; never overlaps it, changes its automatic plan, or counts toward max_runs. Pause/cancel clears pending manual requests. Refuses uncertain, completed or cancelled tasks; resume or update the definition first. Failed recurring tasks require resume/update; a failed one-shot may explicitly run one extra attempt."},
	} {
		mcp.AddTool(server, &mcp.Tool{Name: "schedule_" + item.op, Description: item.description}, func(ctx context.Context, _ *mcp.CallToolRequest, in IDInput) (*mcp.CallToolResult, map[string]any, error) {
			return call(ctx, item.op, in.TaskID, in.ExpectedRevision, nil, "")
		})
	}
	mcp.AddTool(server, &mcp.Tool{Name: "notify_user", Description: "Notify the user of a result worth their attention during this scheduled task turn. Ordinary turn-end notifications are suppressed for scheduled turns. Only available in a bound scheduled turn, at most 3 times per run. text is required (up to 200 characters); title is optional (up to 60, defaults to the dispatch-time task name for new_session or the conversation title for resume_session). Success means the notification was durably recorded for delivery, not that the user has received it."}, func(ctx context.Context, _ *mcp.CallToolRequest, in NotifyInput) (*mcp.CallToolResult, map[string]any, error) {
		return call(ctx, "notify_user", "", 0, in, "")
	})
	return server
}
