package codex

import (
	"encoding/json"
	"strings"

	"github.com/Lewin671/remote-agent/internal/agent"
)

// HookTimeoutSeconds is the timeout Codex gives the PermissionRequest hook;
// see agent.HookTimeoutSeconds.
const HookTimeoutSeconds = agent.HookTimeoutSeconds

// TestedVersions are the codex-cli versions the screen analysis, menus and
// rollout parsing were checked against (docs/codex-notes.md). Others may
// work; `remote-agent doctor` checks the installed one.
var TestedVersions = []string{"0.157.1", "0.159.0"}

// hookCommand runs remote-agent's hook. Codex asks the user to trust a
// hook once per definition and remembers one definition per hook slot, so
// the command must not depend on where the executable is: a second binary
// (another build, a test) would otherwise replace the trust of the first
// and every new session would ask again. The session environment names
// the executable in RA_EXE.
const hookCommand = `"$RA_EXE" hook codex`

// Args returns the codex arguments that install remote-agent's hooks for
// this session only and turn off startup popups that can be turned off,
// followed by the user's own arguments. The session needs RA_EXE and
// RA_SESSION in its environment.
func Args(userArgs []string) []string {
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

// PasteBytes is agent.PasteBytes; Codex submits a bracketed paste
// followed by Enter.
func PasteBytes(text string) []byte { return agent.PasteBytes(text) }
