package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

func TestHookDenialSurvivesRecordingFailure(t *testing.T) {
	for _, scenario := range []string{"request", "timeout", "exit", "deny"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t, Config{ApprovalTimeout: time.Second})
			h.d.sessions[h.s.meta.ID] = h.s
			payload, _ := json.Marshal(&agent.Hook{HookEventName: "PermissionRequest", ToolName: "shell", ToolInput: json.RawMessage(`{"command":"true"}`)})
			handler := &handler{d: h.d}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if scenario == "request" {
				h.s.log.Close()
			}
			type reply struct {
				resp *connect.Response[pb.HookResponse]
				err  error
			}
			done := make(chan reply, 1)
			go func() {
				resp, err := handler.Hook(ctx, connect.NewRequest(&pb.HookRequest{SessionId: h.s.meta.ID, Payload: payload}))
				done <- reply{resp, err}
			}()
			if scenario != "request" {
				eventually(t, "approval request committed", func() bool { return h.s.info().PendingApprovals == 1 })
				h.s.log.Close()
				if scenario == "exit" {
					h.s.markExited("stopped")
				}
			}
			if scenario == "deny" {
				h.s.mu.Lock()
				var id string
				for key := range h.s.approvals {
					id = key
				}
				h.s.mu.Unlock()
				if err := h.s.resolve(id, false, "USER_TEST_REASON"); err == nil || !strings.Contains(err.Error(), "approval denied") {
					t.Fatalf("recording failure not returned to remote caller: %v", err)
				}
			}

			r := <-done
			if r.err != nil || r.resp == nil {
				t.Fatalf("hook discarded denial: response=%v error=%v", r.resp, r.err)
			}
			if !strings.Contains(string(r.resp.Msg.Output), `"behavior":"deny"`) {
				t.Fatalf("no deny payload: %s", r.resp.Msg.Output)
			}
			if scenario == "deny" && !strings.Contains(string(r.resp.Msg.Output), "USER_TEST_REASON") {
				t.Fatal("denial reason lost")
			}
			if scenario == "timeout" && !strings.Contains(string(r.resp.Msg.Output), "no answer within") {
				t.Fatalf("timeout denial changed: %s", r.resp.Msg.Output)
			}
			h.s.mu.Lock()
			fault := h.s.storageFault
			h.s.mu.Unlock()
			if fault == "" {
				t.Fatal("recording failure was not reported separately")
			}
		})
	}
}
