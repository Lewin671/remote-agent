package codex

import (
	"os"
	"testing"
)

// Captures from codex-cli 0.157.1 (capture-pane -p -e), trimmed.
const (
	screenIdle = "\x1b[2m╰──────╯\x1b[0m\n" +
		"\x1b[1;2m› \x1b[0mReply with PONG\n" +
		"• PONG\n" +
		"\x1b[1m›\x1b[0m \x1b[2mAsk Codex to do anything\x1b[0m\n" +
		"  \x1b[38;2;246;226;183mGPT-6-Sol medium\x1b[39m · /tmp/w\n" +
		"  \x1b[1m?\x1b[0m for shortcuts\n\n\n"
	screenDraft = "\x1b[1m›\x1b[0m hello draft\n" +
		"  GPT-6-Sol medium · /tmp/w\n"
	screenTrust = "\x1b[1m  Folder access\x1b[0m\n" +
		"  Trust this folder? Codex can read, edit, and run files here.\n" +
		"\x1b[1;7m› 1. Trust and continue\n" +
		"\x1b[0m  2. Quit\n" +
		"  \x1b[1menter\x1b[0;2m continue · \x1b[0;1mesc\x1b[0;2m quit\x1b[0m\n"
	screenApproval = "\x1b[1m\x1b[38;2;128;128;128m•\x1b[0m \x1b[1mRunning\x1b[0m touch ~/x\n" +
		"  \x1b[1mWould you like to run the following command?\x1b[0m\n" +
		"\x1b[1;7m› 1. Yes, proceed (y)\n" +
		"\x1b[0m  2. Yes, and don't ask again (\x1b[1mp\x1b[0m)\n" +
		"  3. No, and tell Codex what to do differently (\x1b[1mesc\x1b[0m)\n"
	screenQuestion = "\x1b[1;2m› \x1b[0mAsk me a question\n" +
		"  \x1b[2mQuestion 1/1 (1 unanswered)\x1b[0m\n" +
		"  Which language?\n" +
		"  \x1b[1;7m› 1. Go (Recommended)   \x1b[0;7mUse Go.\x1b[0m\n" +
		"    2. Python             \x1b[2mUse Python.\x1b[0m\n" +
		"    3. None of the above  \x1b[2mOptionally, add details in notes (tab)\x1b[0m\n"
	screenBlankFirstLine = "\x1b[1m›\x1b[0m \n" +
		"  second line of a local draft\n" +
		"  GPT-6-Sol medium · /tmp/w\n"
	screenTitleInChat = "• You asked about the Trust this folder? dialog and Question 1/2 counters.\n" +
		"\x1b[1m›\x1b[0m \x1b[2mAsk Codex to do anything\x1b[0m\n" +
		"  GPT-6-Sol medium · /tmp/w\n"
	// Attached from a terminal that reports its background color, Codex
	// paints the input box's background before the placeholder.
	screenIdleBackground = "\x1b[1m›\x1b[0m\x1b[48;2;57;57;57m \x1b[2mAsk Codex to do anything\x1b[0m\x1b[48;2;57;57;57m\n" +
		"  GPT-6-Sol medium · ~/w\n"
	screenDraftBackground = "\x1b[1m›\x1b[0m\x1b[48;2;57;57;57m hello draft\x1b[0m\x1b[48;2;57;57;57m\n" +
		"  GPT-6-Sol medium · ~/w\n"
	// A draft colored like the placeholder's RGB arguments must not pass.
	screenDraftRGB2      = "\x1b[1m›\x1b[0m\x1b[38;2;2;2;2m draft\x1b[0m\n"
	screenBareBackground = "\x1b[1m›\x1b[0m\x1b[48;2;57;57;57m \x1b[0m\n" +
		"  second line of a local draft\n"
	screenPlanPrompt = "• Proposed Plan\n  1. Create hello.txt\n" +
		"  Implement this plan?\n" +
		"\x1b[1;7m› 1. Yes, implement this plan          Switch to Default and start coding\n" +
		"\x1b[0m  2. Yes, clear context and implement  Start a fresh thread\n" +
		"  3. No, stay in Plan mode             Continue planning with the model\n" +
		"  enter select · esc back                                            Plan mode\n"
	screenUnknownMenu = "\x1b[1m›\x1b[0m \x1b[2mAsk Codex\x1b[0m\n" +
		"  Some new dialog\n" +
		"\x1b[1;7m› 1. First\n" +
		"\x1b[0m  2. Second\n"
)

func TestAnalyzeScreen(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   Screen
	}{
		{"idle", screenIdle, Screen{ComposerReady: true, DraftEmpty: true}},
		{"draft", screenDraft, Screen{ComposerReady: true, DraftEmpty: false}},
		{"trust", screenTrust, Screen{Popup: "trust folder"}},
		{"approval", screenApproval, Screen{Popup: "approval"}},
		{"question", screenQuestion, Screen{Popup: "question", QuestionOpen: true}},
		{"unknown menu", screenUnknownMenu, Screen{Popup: "menu"}},
		{"dialog title quoted in chat", screenTitleInChat, Screen{ComposerReady: true, DraftEmpty: true}},
		{"draft with blank first line", screenBlankFirstLine, Screen{ComposerReady: true, DraftEmpty: false}},
		{"idle with background", screenIdleBackground, Screen{ComposerReady: true, DraftEmpty: true}},
		{"draft with background", screenDraftBackground, Screen{ComposerReady: true, DraftEmpty: false}},
		{"draft colored 2;2;2", screenDraftRGB2, Screen{ComposerReady: true, DraftEmpty: false}},
		{"bare prompt with background", screenBareBackground, Screen{ComposerReady: true, DraftEmpty: false}},
		{"plan prompt", screenPlanPrompt, Screen{Popup: "plan", PlanOpen: true, PlanMode: true}},
		{"empty", "", Screen{}},
	}
	for _, c := range cases {
		got := AnalyzeScreen(c.screen)
		got.Footer = "" // checked in TestScreenFooter
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestScreenFooter(t *testing.T) {
	scr := AnalyzeScreen(screenIdle)
	if scr.Footer != "GPT-6-Sol medium · /tmp/w" || scr.PlanMode {
		t.Fatalf("%+v", scr)
	}
	// Seen with codex-cli 0.157.1: a blank line under the input box.
	gap := AnalyzeScreen("\x1b[1m›\x1b[0m \x1b[2mAsk Codex to do anything\x1b[0m\n\n  GPT-6-Luna medium · /tmp/w\n  ? for shortcuts\n")
	if gap.Footer != "GPT-6-Luna medium · /tmp/w" {
		t.Fatalf("footer after a blank line: %q", gap.Footer)
	}
	plan := AnalyzeScreen("\x1b[1m›\x1b[0m \x1b[2mAsk Codex to do anything\x1b[0m\n" +
		"  GPT-6-Sol medium · /tmp/w                                   Plan mode\n")
	if !plan.PlanMode || !plan.DraftEmpty {
		t.Fatalf("%+v", plan)
	}
}

// Codex 0.163.0 adds a cycle hint and may put warnings after the mode.
func TestPlanModeFooterWithCycleHint(t *testing.T) {
	prompt := "\x1b[1m›\x1b[0m \x1b[2mAsk Codex to do anything\x1b[0m\n"
	cases := []struct {
		name, screen string
		want         bool
	}{
		{"cycle hint and warning", prompt + "  GPT-6.1-Sol medium · /tmp/work   Plan mode (⇧tab to cycle)    ⚠ 1 warning · f2 to view\n", true},
		{"cycle hint only", prompt + "  GPT-6.1-Sol medium · /tmp/work   Plan mode (⇧tab to cycle)\n", true},
		{"ordinary footer", prompt + "  GPT-6.1-Sol medium · /tmp/work    ⚠ 1 warning · f2 to view\n", false},
		{"quoted old mode above composer", "• The previous footer said Plan mode (⇧tab to cycle)\n" + prompt + "  GPT-6.1-Sol medium · /tmp/work\n", false},
		{"model word is not mode", prompt + "  Plan model medium · /tmp/work\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AnalyzeScreen(c.screen); got.PlanMode != c.want || !got.ComposerReady || !got.DraftEmpty {
				t.Fatalf("got %+v, want PlanMode=%v and an idle composer", got, c.want)
			}
		})
	}
}

// The same marks written with other escape sequences, as other Codex
// versions do.
func TestAnalyzeScreenStyleVariants(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   Screen
	}{
		{"prompt reset then bold", "\x1b[0;1m›\x1b[0m \x1b[2mAsk Codex to do anything\x1b[0m\n  f\n", Screen{ComposerReady: true, DraftEmpty: true}},
		{"prompt bold with color", "\x1b[1;39m›\x1b[39;22m \x1b[38;5;245;2mAsk Codex\x1b[0m\n  f\n", Screen{ComposerReady: true, DraftEmpty: true}},
		{"prompt split sequences", "\x1b[0m\x1b[1m›\x1b[22m \x1b[2mAsk Codex\x1b[22m\n  f\n", Screen{ComposerReady: true, DraftEmpty: true}},
		{"draft after a bold prompt", "\x1b[0;1m›\x1b[0m my draft\n  f\n", Screen{ComposerReady: true}},
		{"earlier message is not the prompt", "\x1b[0;1;2m› \x1b[0mhello\n  f\n", Screen{}},
		{"selection reset then reversed", "  Trust this folder?\n\x1b[0;1;7m› 1. Trust and continue\n\x1b[0m  2. Quit\n", Screen{Popup: "trust folder"}},
		{"selection reversed only", "  Some menu\n  \x1b[7m› 1. First\x1b[27m\n", Screen{Popup: "menu"}},
	}
	for _, c := range cases {
		got := AnalyzeScreen(c.screen)
		got.Footer = ""
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

// A capture from codex-cli 0.159.0, which writes the prompt as ESC[0;1m.
func TestAnalyzeScreen0159(t *testing.T) {
	b, err := os.ReadFile("testdata/screen-0.159.0-idle.txt")
	if err != nil {
		t.Fatal(err)
	}
	if scr := AnalyzeScreen(string(b)); !scr.ComposerReady || !scr.DraftEmpty || scr.Popup != "" {
		t.Fatalf("%+v", scr)
	}
}
