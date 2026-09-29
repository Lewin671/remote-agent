package codex

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// HookTimeoutSeconds is the timeout Codex gives the PermissionRequest hook.
// It must stay longer than any approval wait, because a hook that times
// out makes Codex fall back to its own prompt in the terminal. Changing it
// changes the hook definition, which makes Codex ask to trust it again.
const HookTimeoutSeconds = 3600

// Args returns the codex arguments that install remote-agent's hooks for
// this session only and turn off startup popups that can be turned off,
// followed by the user's own arguments.
// TestedVersions are the codex-cli versions the screen analysis, menus and
// rollout parsing were checked against (docs/codex-notes.md). Others may
// work; `remote-agent doctor` checks the installed one.
var TestedVersions = []string{"0.157.1", "0.159.0"}

func Args(hookCommand string, userArgs []string) []string {
	cmd := tomlString(hookCommand)
	args := []string{
		"-c", "hooks.PermissionRequest=[{matcher=\"*\",hooks=[{type=\"command\",command=" + cmd +
			",timeout=" + itoa(HookTimeoutSeconds) + "}]}]",
		"-c", "hooks.SessionStart=[{hooks=[{type=\"command\",command=" + cmd + ",timeout=30}]}]",
		"-c", "check_for_update_on_startup=false",
		"-c", "notice.hide_rate_limit_model_nudge=true",
	}
	return append(args, userArgs...)
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				b.WriteString(`\u00`)
				b.WriteByte("0123456789abcdef"[r>>4])
				b.WriteByte("0123456789abcdef"[r&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// HookPayload is the JSON Codex writes to a hook's stdin.
type HookPayload struct {
	SessionID      string          `json:"session_id"`
	TurnID         string          `json:"turn_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	Model          string          `json:"model"`
	PermissionMode string          `json:"permission_mode"`
	Source         string          `json:"source"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolUseID      string          `json:"tool_use_id"`
}

// ShellInput is the tool_input of a shell PermissionRequest.
type ShellInput struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// PermissionDecision is the hook output that allows or denies a request.
func PermissionDecision(allow bool, message string) []byte {
	type decision struct {
		Behavior string `json:"behavior"`
		Message  string `json:"message,omitempty"`
	}
	behavior := "deny"
	if allow {
		behavior = "allow"
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PermissionRequest",
			"decision":      decision{Behavior: behavior, Message: message},
		},
	}
	b, _ := json.Marshal(out)
	return b
}

// PasteBytes wraps text in a bracketed paste followed by Enter, the way a
// terminal delivers pasted text and a key press. Control characters other
// than newline and tab are removed so the text cannot end the paste early
// or send keys of its own.
func PasteBytes(text string) []byte {
	var b strings.Builder
	b.WriteString("\x1b[200~")
	for _, r := range strings.ReplaceAll(text, "\r\n", "\n") {
		if r == utf8.RuneError {
			continue
		}
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7f && !(r >= 0x80 && r < 0xa0)) {
			b.WriteRune(r)
		}
	}
	b.WriteString("\x1b[201~\r")
	return []byte(b.String())
}
