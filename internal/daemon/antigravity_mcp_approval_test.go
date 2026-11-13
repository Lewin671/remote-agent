package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

func TestAgyMCPApprovalUsesExistingCardAndDecision(t *testing.T) {
	t.Parallel()
	for _, allow := range []bool{true, false} {
		name := "deny"
		if allow {
			name = "allow"
		}
		t.Run(name, func(t *testing.T) {
			h := agyHarness(t)
			h.d.cfg.ApprovalTimeout = time.Hour
			const input = `{"ServerName":"remote_agent","ToolName":"send_file","Arguments":{"path":"report.bin"}}`
			const callID = "conv/step_2"
			if err := h.s.emit(&pb.Event{Kind: &pb.Event_ToolStarted{ToolStarted: &pb.ToolStarted{CallId: callID, ItemType: "call_mcp_tool", Input: input}}}); err != nil {
				t.Fatal(err)
			}
			step := int64(2)
			_, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "PreToolUse", SessionID: "conv", StepIndex: &step, ToolName: "call_mcp_tool", ToolUseID: callID, ToolInput: []byte(input)})
			if err != nil {
				t.Fatal(err)
			}
			h.term.setScreen(agyScreen(t, "mcp-permission-1.5.8.txt"))
			h.s.checkScreen()
			h.s.checkScreen()
			var card *pb.ApprovalRequested
			for _, e := range h.events() {
				if a := e.GetApprovalRequested(); a != nil {
					card = a
				}
			}
			if card == nil || card.ToolName != "call_mcp_tool" || card.ToolInputJson != input || card.InputDigest != agent.InputDigest("call_mcp_tool", []byte(input)) {
				t.Fatalf("missing bound MCP card: %+v", card)
			}
			key := "4"
			decision := pb.ApprovalDecision_APPROVAL_DECISION_DENY
			if allow {
				key = "1"
				decision = pb.ApprovalDecision_APPROVAL_DECISION_ALLOW
			}
			h.term.mu.Lock()
			h.term.onWrite = func(term *fakeTerm, b string) {
				if b == key {
					term.screen = agyScreen(t, "idle-empty-box.txt")
				}
			}
			h.term.mu.Unlock()
			if err := h.s.resolve(card.ApprovalId, allow, ""); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(h.term.written(), ""); got != key {
				t.Fatalf("wrote %q, want one-shot %s", got, key)
			}
			for _, e := range h.events() {
				if a := e.GetApprovalResolved(); a != nil && a.ApprovalId == card.ApprovalId && a.Decision == decision {
					return
				}
			}
			t.Fatal("existing approval result was not recorded")
		})
	}
}
