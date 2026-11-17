package antigravity

import (
	"context"
	"encoding/json"
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

func TestDirectSendFile159Approval(t *testing.T) {
	a := New("", "")
	screen := readScreenFixture(t, "mcp-direct-permission-1.5.9.txt")
	h := &agent.Hook{ToolUseID: "conv/step_15", ToolName: nativeSendFileTool, ToolInput: []byte(`{"path":"report.txt","toolSummary":"Send report.txt to user"}`)}
	if req, ok := a.MatchApproval(screen, h); !ok || req.InputDigest != agent.InputDigest(h.ToolName, h.ToolInput) {
		t.Fatalf("missing direct tool approval: %+v", req)
	}
	other := *h
	other.ToolUseID = "conv/step_16"
	other.ToolInput = []byte(`{"path":"another.txt"}`)
	if m := a.MatchApprovals(screen, []*agent.Hook{h, &other}); !m.Ambiguous || m.Request != nil {
		t.Fatal("ambiguous direct calls were trusted")
	}
	for _, name := range []string{"mcp_remote_agent_send_file", "mcp_other_send_file", "mcp_remote-agent_remote_agent_send_file_extra"} {
		bad := *h
		bad.ToolName = name
		if _, ok := a.MatchApproval(screen, &bad); ok {
			t.Fatalf("accepted %s", name)
		}
	}
	for _, broken := range []string{strings.ReplaceAll(screen, nativeSendFileTitle, "other/send_file"), strings.ReplaceAll(screen, "6. No, and always deny tool", "7. No, and always deny tool"), screen + "\n────────────────────\n>\n────────────────────\n"} {
		term := &fakeTerm{screen: broken}
		if err := a.AnswerApproval(term, h, true, nil); !errors.Is(err, agent.ErrNotAnswering) || len(term.written) > 0 {
			t.Fatal("answered unproven menu", err)
		}
	}
	for _, allow := range []bool{true, false} {
		term := &fakeTerm{screen: screen}
		term.onWrite = func([]byte) { term.screen = readScreenFixture(t, "idle-footer-1.5.9.txt") }
		if err := a.AnswerApproval(term, h, allow, nil); err != nil {
			t.Fatal(err)
		}
		want := "4"
		if allow {
			want = "1"
		}
		if string(term.written) != want {
			t.Fatal("non-single-shot decision", string(term.written))
		}
	}
}

func TestDirectScheduleAndNotifyMCPApproval(t *testing.T) {
	a := New("", "")
	base := readScreenFixture(t, "mcp-direct-permission-1.5.9.txt")
	for _, tc := range []struct {
		tool, input string
	}{
		{"notify_user", `{"text":"done"}`},
		{"schedule_list", `{}`},
		{"schedule_create", `{"task":{"title":"check","message":"ping","trigger":{"type":"after_turn","delay_seconds":60},"target":{"type":"resume_session","session":"current"}}}`},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			screen := strings.ReplaceAll(base, nativeSendFileTitle, managedMCPTitlePrefix+tc.tool)
			h := &agent.Hook{ToolUseID: "conv/step_20", ToolName: managedMCPToolPrefix + tc.tool, ToolInput: []byte(tc.input)}
			req, ok := a.MatchApproval(screen, h)
			if !ok || req.ToolName != h.ToolName || req.InputDigest != agent.InputDigest(h.ToolName, h.ToolInput) {
				t.Fatalf("missing direct %s approval: ok=%v req=%+v", tc.tool, ok, req)
			}
			// Cross-tool mismatch or unapproved direct MCP tool must still fall back to terminal.
			wrongScreen := strings.ReplaceAll(base, nativeSendFileTitle, managedMCPTitlePrefix+"schedule_pause")
			if _, ok := a.MatchApproval(wrongScreen, h); ok {
				t.Fatalf("%s matched another tool's menu", tc.tool)
			}
			nullInput := *h
			nullInput.ToolInput = []byte("null")
			if _, ok := a.MatchApproval(screen, &nullInput); ok {
				t.Fatalf("%s matched null input", tc.tool)
			}
		})
	}
	unknown := &agent.Hook{ToolUseID: "conv/step_21", ToolName: managedMCPToolPrefix + "unknown_tool", ToolInput: []byte(`{}`)}
	unknownScreen := strings.ReplaceAll(base, nativeSendFileTitle, managedMCPTitlePrefix+"unknown_tool")
	if _, ok := a.MatchApproval(unknownScreen, unknown); ok {
		t.Fatal("matched unmanaged direct MCP tool")
	}
}

func TestOutsideWorkspaceRead159Approval(t *testing.T) {
	a := New("", "")
	screen := readScreenFixture(t, "file-access-1.5.9.txt")
	const path = "/tmp/ra-agy-example/dev/probe-home/uploads/f583d1f098440654/files/231da22932728f88cdddf7f138987589/attachment.txt"
	input, _ := json.Marshal(map[string]string{"Path": path})
	h := &agent.Hook{ToolName: "read_file", ToolUseID: "conv/step_24", ToolInput: input}
	for _, allow := range []bool{true, false} {
		term := &fakeTerm{screen: screen}
		term.onWrite = func([]byte) { term.screen = readScreenFixture(t, "idle-footer-1.5.9.txt") }
		if err := a.AnswerApproval(term, h, allow, nil); err != nil {
			t.Fatal(err)
		}
		want := "3"
		if allow {
			want = "1"
		}
		if string(term.written) != want {
			t.Fatalf("wrong one-shot decision: %q", term.written)
		}
	}
	for _, broken := range []string{
		strings.ReplaceAll(screen, "Reason: outside workspace", "Reason: unknown"),
		strings.ReplaceAll(screen, "No, deny access", "Yes, always allow"),
		strings.ReplaceAll(screen, "Read:", "Write:"),
		strings.ReplaceAll(screen, "Read:", "Requesting permission for:"),
		screen + "\n────────────────────\n>\n────────────────────\n",
	} {
		if _, ok := a.MatchApproval(broken, h); ok {
			t.Fatal("accepted unknown file menu")
		}
	}
	for _, name := range []string{"write_file", "run_command"} {
		other := *h
		other.ToolName = name
		if _, ok := a.MatchApproval(screen, &other); ok {
			t.Fatal("matched wrong tool")
		}
	}
	other := *h
	other.ToolUseID = "conv/step_25"
	if m := a.MatchApprovals(screen, []*agent.Hook{h, &other}); !m.Ambiguous {
		t.Fatal("matched duplicate file calls")
	}
}
