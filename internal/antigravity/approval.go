package antigravity

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// permissionBody isolates the active request, excluding history and the menu's
// permanent-grant command prefixes. Only complete, recognized menus are used.
var fileDiffHeaderRE = regexp.MustCompile(`^(/.+?)\s+[+-][0-9]+(?:\s+[+-][0-9]+)?$`)

func permissionPosition(plain string) int {
	pos := strings.LastIndex(plain, "Requesting permission for:")
	for _, title := range []string{"Allow creation of this file?", "Allow access to this file?"} {
		if at := strings.LastIndex(plain, title); at > pos {
			pos = at
		}
	}
	return pos
}
func permissionBody(plain string) (body, deny string, ok bool) {
	pos := permissionPosition(plain)
	if pos < 0 {
		return "", "", false
	}
	area := plain[pos:]
	if strings.HasPrefix(area, "Requesting permission for:") {
		area = strings.TrimPrefix(area, "Requesting permission for:")
		if j := strings.Index(area, "Run this command?"); j >= 0 {
			rows := questionRows(area[j:])
			if len(rows) == 4 && rows[1].label == "Yes, run command" && rows[4].label == "No, cancel" && strings.HasPrefix(rows[2].label, "Yes, and always allow in this conversation for commands that start with ") && strings.HasPrefix(rows[3].label, "Yes, and always allow for commands that start with ") && strings.Contains(rows[3].label, "Persist to settings.json") {
				return strings.TrimSpace(area[:j]), "4", true
			}
		}
		for _, title := range []string{"Allow creation of this file?", "Allow access to this file?"} {
			if j := strings.Index(area, title); j >= 0 && validFileMenu(area[j:]) {
				return strings.TrimSpace(area[:j]), "2", true
			}
		}
		return "", "", false
	}
	if !validFileMenu(area) {
		return "", "", false
	}
	// Native file prompts put the full path above the diff, without the
	// command prompt's Requesting-permission heading. Bind only that active
	// header between the last rule and this question, never history text.
	before := strings.Split(plain[:pos], "\n")
	start := 0
	for i, line := range before {
		if dividerRE.MatchString(strings.TrimSpace(line)) {
			start = i + 1
		}
	}
	for _, line := range before[start:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if match := fileDiffHeaderRE.FindStringSubmatch(line); match != nil {
			return match[1], "2", true
		}
		// A read-access prompt may show the path alone, with no diff count.
		if strings.HasPrefix(area, "Allow access") && strings.HasPrefix(line, "/") {
			return line, "2", true
		}
		break
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
	if json.Unmarshal(h.ToolInput, &args) != nil {
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
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return nil, false
	}
	shown := unwrapRequest(body)
	if h.ToolName == "run_command" {
		if shown != value {
			return nil, false
		}
	} else if !sameNativeFile(shown, value) {
		return nil, false
	}
	switch h.ToolName {
	case "run_command", "write_file", "edit_file", "read_file":
	default:
		return nil, false
	}
	req := &pb.ApprovalRequested{ToolName: h.ToolName, ToolInputJson: string(h.ToolInput), InputDigest: agent.InputDigest(h.ToolName, h.ToolInput), Description: summary}
	if h.ToolName == "run_command" {
		req.Command = value
	}
	return req, true
}
func (a *Adapter) MatchApprovals(styled string, pending []*agent.Hook) agent.NativeMatch {
	var out agent.NativeMatch
	matches := 0
	for _, h := range pending {
		if req, ok := a.MatchApproval(styled, h); ok {
			matches++
			out.Hook = h
			out.Request = req
			if h.Uncertain {
				out.Ambiguous = true
			}
		}
	}
	if matches != 1 {
		out.Hook = nil
		out.Request = nil
		out.Ambiguous = matches > 1
	}
	return out
}
func (a *Adapter) AnswerApproval(t agent.Term, h *agent.Hook, allow bool, consumed func() bool) error {
	styled, err := t.Capture()
	if err != nil {
		return err
	}
	if h == nil || h.Uncertain || !a.Analyze(styled).PermissionOpen {
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
	if err := t.Write([]byte(key)); err != nil {
		return fmt.Errorf("%w: key write failed", agent.ErrDeliveryUnconfirmed)
	}
	_, err = agent.WaitFor(t, "native permission prompt consumption", func(screen string) bool {
		if consumed != nil && consumed() {
			return true
		}
		_, same := a.MatchApproval(screen, h)
		return !same || !a.Analyze(screen).PermissionOpen
	})
	if err != nil {
		return fmt.Errorf("%w: native prompt still visible", agent.ErrDeliveryUnconfirmed)
	}
	return nil
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

func validFileMenu(area string) bool {
	rows := questionRows(area)
	if len(rows) != 2 {
		return false
	}
	if strings.HasPrefix(area, "Allow creation of this file?") {
		return rows[1].label == "Yes, allow creation" && rows[2].label == "No, deny creation"
	}
	if strings.HasPrefix(area, "Allow access to this file?") {
		return rows[1].label == "Yes, allow access" && rows[2].label == "No, deny access"
	}
	return false
}

func sameNativeFile(shown, requested string) bool {
	if shown == requested {
		return true
	}
	canonical := func(path string) (string, error) {
		if !filepath.IsAbs(path) {
			return "", fmt.Errorf("relative native file path")
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved, nil
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return "", err
		}
		return filepath.Join(parent, filepath.Base(path)), nil
	}
	a, err := canonical(shown)
	if err != nil {
		return false
	}
	b, err := canonical(requested)
	return err == nil && a == b
}
