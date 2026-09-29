package claude

import (
	"strings"
	"testing"

	"github.com/Lewin671/remote-agent/internal/agent"
)

// Lines as Claude Code 2.1.284 draws them (capture-pane -p -e).
const (
	rule       = "\x1b[38;5;244m" + "────────────────────────────────────────────────────────────"
	titledRule = "\x1b[38;5;244m" + "──────────────────────────────────────────── create-hello-txt ─"
	pastUser   = "\x1b[38;5;239m\x1b[48;5;237m❯ \x1b[38;5;231mRun the shell command: sleep 30 ; then reply DONE\x1b[39m"
	footer     = "\x1b[39m   \x1b[36mHaiku 4.5\x1b[38;5;246m  \x1b[2m│\x1b[0m\x1b[38;5;246m  \x1b[35mra-cc-work\x1b[39m\n" +
		"   \x1b[38;5;239mctx unavailable\x1b[39m\n"
	manual = "  \x1b[38;5;246m⏸ manual mode on\x1b[39m \x1b[38;5;246m·\x1b[39m \x1b[38;5;246m←\x1b[39m \x1b[38;5;246mfor agents\x1b[39m"
	bypass = "  \x1b[38;5;211m⏵⏵\x1b[39m \x1b[38;5;211mbypass\x1b[39m \x1b[38;5;211mpermissions\x1b[39m \x1b[38;5;211mon\x1b[38;5;246m (shift+tab\x1b[39m \x1b[38;5;246mto\x1b[39m \x1b[38;5;246mcycle)\x1b[39m"
	focus  = "                                  \x1b[38;5;246mtmux\x1b[39m \x1b[38;5;246mfocus-events\x1b[39m \x1b[38;5;246moff\x1b[39m"
)

func screen(composer ...string) string {
	return "\x1b[38;5;174m⏺\x1b[39m PONG\n" + pastUser + "\n" + titledRule + "\n" + strings.Join(composer, "\n") + "\n" + rule + "\n" + footer + manual + "\n" + focus + "\n\n"
}

func TestAnalyzeScreen(t *testing.T) {
	for _, c := range []struct {
		name   string
		screen string
		want   agent.Screen
	}{
		{"idle", screen("\x1b[39m❯ \x1b[7m \x1b[0m"),
			agent.Screen{ComposerReady: true, DraftEmpty: true, Mode: "default"}},
		{"draft", screen("\x1b[39m❯ hello draft\x1b[7m \x1b[0m"),
			agent.Screen{ComposerReady: true, Mode: "default"}},
		{"cursor on a one-letter draft", screen("\x1b[39m❯ \x1b[7mx\x1b[0m"),
			agent.Screen{ComposerReady: true, Mode: "default"}},
		{"multi-line draft", screen("\x1b[39m❯ ", "  line2\x1b[7m \x1b[0m"),
			agent.Screen{ComposerReady: true, Mode: "default"}},
		{"running", screen("\x1b[38;5;246m❯ \x1b[7m\x1b[39m \x1b[0m"),
			agent.Screen{ComposerReady: true, DraftEmpty: true, Mode: "default"}},
		{"queued placeholder", screen("\x1b[38;5;246m❯ \x1b[7m\x1b[39mP\x1b[0;2mress up to edit queued messages\x1b[0m"),
			agent.Screen{ComposerReady: true, DraftEmpty: true, Mode: "default"}},
	} {
		got := AnalyzeScreen(c.screen)
		got.Footer = ""
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	s := AnalyzeScreen(strings.Replace(screen("\x1b[39m❯ \x1b[7m \x1b[0m"), manual, bypass, 1))
	if s.Mode != "bypassPermissions" || s.Footer != "Haiku 4.5  │  ra-cc-work" {
		t.Errorf("mode %q footer %q", s.Mode, s.Footer)
	}
}

func TestAnalyzeDialogs(t *testing.T) {
	permission := "⏺ Bash(touch /w/perm1.txt)\n  ⎿  Waiting…\n" + rule + "\n" +
		" \x1b[1mBash command\x1b[0m\n   touch /w/perm1.txt\n   Create file perm1.txt\n Do you want to proceed?\n" +
		" \x1b[38;5;153m❯\x1b[39m \x1b[38;5;246m1. \x1b[38;5;153mYes\x1b[39m\n" +
		"   \x1b[38;5;246m2.\x1b[39m Yes, and always allow access to /w from this project\n" +
		"   \x1b[38;5;246m3.\x1b[39m No\n Esc to cancel · Tab to amend\n"
	trust := rule + "\n \x1b[1m\x1b[38;5;220mAccessing\x1b[0m \x1b[1m\x1b[38;5;220mworkspace:\x1b[0m\n \x1b[1m/private/tmp/w\x1b[0m\n" +
		" Quick safety check: Is this a project you created or one you trust?\n" +
		" \x1b[38;5;153m❯\x1b[39m \x1b[38;5;153mNo,\x1b[39m \x1b[38;5;153mexit\x1b[39m\n   Yes, I trust this folder\n" +
		" \x1b[38;5;246mEnter\x1b[39m \x1b[38;5;246mto\x1b[39m \x1b[38;5;246mconfirm\x1b[39m\n"
	question := pastUser + "\n" + rule + "\n ☐ Language\nwhich language should a hello-world script use?\n" +
		"❯ 1. Go\n     Compiled language\n  2. Python\n     Interpreted\n  3. Type something.\n" + rule + "\n  4. Chat about this\n" +
		"Enter to select · ↑/↓ to navigate · Esc to cancel\n"
	review := rule + "\n←  ☒ Color  ☒ Fruit  ✔ Submit  →\nReview your answers\n ● which color?\n   → Blue\nReady to submit your answers?\n❯ 1. Submit answers\n  2. Cancel\n"
	plan := rule + "\n Ready to code?\n Here is Claude's plan:\n Do you want to proceed with this? (plan text)\n" + rule + "\n" +
		" Claude has written up a plan and is ready to execute. Would you like to proceed?\n" +
		" \x1b[38;5;153m❯\x1b[39m 1. Yes, auto-accept edits\n   2. Yes, manually approve edits\n   3. Tell Claude what to change\n"
	models := rule + "\n  Select model\n    1. Default (recommended)  Opus 5.5 · Best\n  ❯ 5. Haiku ✔                Haiku 4.5 · Fastest\n" +
		"  ○ Effort not supported for Haiku\n  Enter to set as default · s to use this session only · Esc to cancel\n"
	// While starting, text without a list is not a dialog.
	starting := " ▐▛███▛█   Claude Code v2.1.284\n Do you want to see what's new?\n"
	for _, c := range []struct {
		name, screen, popup string
	}{
		{"permission", permission, "approval"},
		{"trust", trust, "trust folder"},
		{"question", question, "question"},
		{"review", review, "question"},
		{"plan", plan, "plan"},
		{"models", models, "model menu"},
		{"starting", starting, ""},
	} {
		s := AnalyzeScreen(c.screen)
		if s.ComposerReady || s.Popup != c.popup {
			t.Errorf("%s: %+v", c.name, s)
		}
		if s.QuestionOpen != (c.popup == "question") || s.PlanOpen != (c.popup == "plan") || s.PermissionOpen != (c.popup == "approval") {
			t.Errorf("%s: flags %+v", c.name, s)
		}
	}
}
