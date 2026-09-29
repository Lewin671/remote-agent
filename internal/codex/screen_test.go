package codex

import "testing"

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
		{"empty", "", Screen{}},
	}
	for _, c := range cases {
		if got := AnalyzeScreen(c.screen); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
