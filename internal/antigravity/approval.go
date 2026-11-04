package antigravity

import (
	"encoding/json"
	"strings"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// permissionBody isolates the active request, excluding history and the menu's
// permanent-grant command prefixes. Only complete, recognized menus are used.
func permissionBody(plain string) (body string, deny string, ok bool) {
	i := strings.LastIndex(plain, "Requesting permission for:")
	if i < 0 {
		return "", "", false
	}
	area := plain[i+len("Requesting permission for:"):]
	for _, kind := range []struct{ title, allow, deny, key string }{
		{"Run this command?", "1. Yes, run command", "4. No, cancel", "4"},
		{"Allow creation of this file?", "1. Yes, allow creation", "2. No, deny creation", "2"},
		{"Allow access to this file?", "1. Yes, allow access", "2. No, deny access", "2"},
	} {
		j := strings.Index(area, kind.title)
		if j < 0 || !strings.Contains(area[j:], kind.allow) || !strings.Contains(area[j:], kind.deny) {
			continue
		}
		return strings.TrimSpace(area[:j]), kind.key, true
	}
	return "", "", false
}
func (a *Adapter) MatchApproval(styled string, h *agent.Hook) (*pb.ApprovalRequested, bool) {
	if h == nil {
		return nil, false
	}
	body, _, ok := permissionBody(StripANSI(styled))
	if !ok {
		return nil, false
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(normalizeArgs(h.ToolInput), &args) != nil {
		return nil, false
	}
	key := "Path"
	if h.ToolName == "run_command" {
		key = "CommandLine"
	}
	var value, summary string
	_ = json.Unmarshal(args[key], &value)
	_ = json.Unmarshal(args["toolSummary"], &summary)
	// Whitespace inside a command/path is meaningful. Reject multiline
	// requests and any wrapping that cannot be reconstructed exactly.
	if value == "" || strings.ContainsAny(value, "\r\n") || unwrapRequest(body) != value {
		return nil, false
	}
	switch h.ToolName {
	case "run_command", "write_file", "edit_file", "read_file":
	default:
		return nil, false
	}
	req := &pb.ApprovalRequested{ToolName: h.ToolName, ToolInputJson: string(normalizeArgs(h.ToolInput)), InputDigest: agent.InputDigest(h.ToolName, normalizeArgs(h.ToolInput)), Description: summary}
	if h.ToolName == "run_command" {
		req.Command = value
	}
	return req, true
}
func (a *Adapter) AnswerApproval(t agent.Term, h *agent.Hook, allow bool) error {
	styled, err := t.Capture()
	if err != nil {
		return err
	}
	if !a.Analyze(styled).PermissionOpen {
		return agent.ErrNotAnswering
	}
	if _, ok := a.MatchApproval(styled, h); !ok {
		return agent.ErrNotAnswering
	}
	_, deny, _ := permissionBody(StripANSI(styled))
	key := deny
	if allow {
		key = "1"
	}
	return t.Write([]byte(key))
}

func unwrapRequest(body string) string {
	lines := strings.Split(body, "\n")
	for i := range lines {
		if i > 0 {
			lines[i] = strings.TrimPrefix(lines[i], "   ")
		}
	}
	return strings.Join(lines, "")
}
