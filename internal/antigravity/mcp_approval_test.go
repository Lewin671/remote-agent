package antigravity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/outcome"
)

func mcpHook(path string) *agent.Hook {
	return &agent.Hook{SessionID: "conversation", ToolUseID: "conversation/step_2", ToolName: "call_mcp_tool", ToolInput: []byte(`{"ServerName":"remote_agent","ToolName":"send_file","Arguments":{"path":"` + path + `"}}`)}
}

func TestMCPApprovalIdentity(t *testing.T) {
	a := New("agy", t.TempDir())
	screen := readScreenFixture(t, "mcp-permission-1.5.8.txt")
	h := mcpHook("report.bin")
	if !a.Analyze(screen).PermissionOpen {
		t.Fatal("MCP panel is not recognized as permission")
	}
	req, ok := a.MatchApproval(screen, h)
	if !ok || req.ToolName != h.ToolName || req.ToolInputJson != string(h.ToolInput) || req.InputDigest != agent.InputDigest(h.ToolName, h.ToolInput) {
		t.Fatalf("lost native request identity: %+v", req)
	}
	two := mcpHook("another.bin")
	two.ToolUseID = "conversation/step_3"
	if m := a.MatchApprovals(screen, []*agent.Hook{h, two}); !m.Ambiguous || m.Hook != nil || m.Request != nil {
		t.Fatal("menu hides arguments; repeated qualified tool must be ambiguous")
	}
	h.Uncertain = true
	if m := a.MatchApprovals(screen, []*agent.Hook{h}); !m.Ambiguous {
		t.Fatal("uncertain native arguments were trusted")
	}
}

func TestMCPApprovalRejectsMismatchedAndIncompletePanels(t *testing.T) {
	a := New("agy", t.TempDir())
	screen := readScreenFixture(t, "mcp-permission-1.5.8.txt")
	for name, changed := range map[string]string{
		"duplicate row":     strings.Replace(screen, "  2. Yes,", "  1. Yes, allow tool call\n  2. Yes,", 1),
		"missing footer":    strings.Replace(screen, "Navigate · tab Amend", "", 1),
		"server":            strings.ReplaceAll(screen, "remote_agent/", "other/"),
		"tool":              strings.ReplaceAll(screen, "/send_file", "/schedule_list"),
		"wrong panel":       strings.Replace(screen, "MCP\n", "Command\n", 1),
		"missing row":       strings.Replace(screen, "  6. No, and always deny tool 'remote_agent/send_file' (Persist to settings.json)\n", "", 1),
		"unknown choice":    strings.Replace(screen, "4. No, deny tool call", "4. Always allow", 1),
		"inconsistent rule": strings.Replace(screen, "tool 'remote_agent/send_file' in this conversation", "tool 'other/send_file' in this conversation", 1),
		"history":           screen + "\n────────────────────\n>\n────────────────────\n",
	} {
		t.Run(name, func(t *testing.T) {
			term := &fakeTerm{screen: changed}
			if _, ok := a.MatchApproval(changed, mcpHook("report.bin")); ok {
				t.Fatal("matched another or unproven native prompt")
			}
			if err := a.AnswerApproval(term, mcpHook("report.bin"), true, nil); !errors.Is(err, agent.ErrNotAnswering) || !outcome.IsFinalError(err) || len(term.written) > 0 {
				t.Fatalf("wrote %q to unproven prompt: %v", term.written, err)
			}
		})
	}
	for _, raw := range []string{
		`{}`, `{"ServerName":"remote_agent","ToolName":"send_file","Arguments":null}`,
		`{"ServerName":"remote_agent","ToolName":"send_file","Arguments":"report.bin"}`,
		`{"CommandLine":"remote_agent/send_file"}`,
	} {
		h := mcpHook("report.bin")
		h.ToolInput = []byte(raw)
		if _, ok := a.MatchApproval(screen, h); ok {
			t.Fatalf("matched malformed MCP args: %s", raw)
		}
	}
	h := &agent.Hook{ToolName: "run_command", ToolInput: []byte(`{"CommandLine":"remote_agent/send_file"}`)}
	if _, ok := a.MatchApproval(screen, h); ok {
		t.Fatal("MCP title mistaken for a shell command")
	}
	if _, ok := a.MatchApproval(readScreenFixture(t, "permission-prompt.txt"), mcpHook("report.bin")); ok {
		t.Fatal("command menu mistaken for MCP")
	}
}

func TestMCPApprovalOneShotKeysAndConsumption(t *testing.T) {
	a := New("agy", t.TempDir())
	screen := readScreenFixture(t, "mcp-permission-1.5.8.txt")
	for _, allow := range []bool{true, false} {
		term := &fakeTerm{screen: screen}
		term.onWrite = func([]byte) { term.screen = readScreenFixture(t, "idle-empty-box.txt") }
		if err := a.AnswerApproval(term, mcpHook("report.bin"), allow, nil); err != nil {
			t.Fatal(err)
		}
		want := "4"
		if allow {
			want = "1"
		}
		if string(term.written) != want {
			t.Fatalf("wrote %q, want one-shot %s", term.written, want)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	term := contextTerm{&fakeTerm{screen: screen}, ctx}
	if err := a.AnswerApproval(term, mcpHook("report.bin"), true, nil); !errors.Is(err, agent.ErrDeliveryUnconfirmed) || outcome.IsFinalError(err) {
		t.Fatalf("consumption was not proved: %v", err)
	}
}
