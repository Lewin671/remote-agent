package codex

import (
	"regexp"
	"strings"
)

// Screen is what remote-agent can tell from a capture of the Codex TUI.
type Screen struct {
	// ComposerReady: the input box is showing and no menu or popup covers it.
	ComposerReady bool
	// DraftEmpty: the input box holds no unsent text (only the dim
	// placeholder, or nothing).
	DraftEmpty bool
	// Popup names the menu or dialog in front of the input box, if any.
	Popup string
	// QuestionOpen: a request_user_input question is waiting for an answer.
	QuestionOpen bool
}

var sgrRE = regexp.MustCompile("\x1b\\[[0-9;:]*[A-Za-z]")

// StripANSI removes SGR and other CSI sequences.
func StripANSI(s string) string { return sgrRE.ReplaceAllString(s, "") }

const (
	// The composer prompt is a bold "›" followed by a reset.
	composerPrompt = "\x1b[1m›\x1b[0m"
	// The highlighted row of a selection list is bold and reversed.
	selectionCursor = "\x1b[1;7m›"
	// The placeholder in an empty composer is dim.
	dim = "\x1b[2m"
)

// bottomLines is how much of the screen bottom is inspected. Only the
// bottom holds the live composer and dialogs; older conversation above it
// may contain anything.
const bottomLines = 16

// popupTitles maps text shown by known dialogs to a short name.
var popupTitles = []struct{ text, name string }{
	{"Trust this folder?", "trust folder"},
	{"Hooks need review", "hooks need review"},
	{"Update available", "update available"},
	{"Would you like to run the following command?", "approval"},
	{"Would you like to make the following edits?", "approval"},
	{"Select Model and Effort", "model menu"},
	{"Select Reasoning Level", "model menu"},
	{"Question 1/", "question"},
	{"unanswered)", "question"},
}

// AnalyzeScreen inspects a capture made with escape sequences kept
// (capture-pane -e).
func AnalyzeScreen(styled string) Screen {
	lines := strings.Split(strings.TrimRight(styled, "\n"), "\n")
	// Drop trailing blank lines, then keep the bottom.
	for len(lines) > 0 && strings.TrimSpace(StripANSI(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > bottomLines {
		lines = lines[len(lines)-bottomLines:]
	}

	var s Screen
	composer := -1
	selection := false
	for i, l := range lines {
		plain := StripANSI(l)
		for _, p := range popupTitles {
			if strings.Contains(plain, p.text) {
				s.Popup = p.name
			}
		}
		if strings.HasPrefix(l, selectionCursor) || strings.Contains(l, " "+selectionCursor) {
			selection = true
		}
		if strings.HasPrefix(l, composerPrompt) {
			composer = i
		}
	}
	if s.Popup == "question" {
		s.QuestionOpen = true
	}
	if selection && s.Popup == "" {
		s.Popup = "menu"
	}
	if composer >= 0 && s.Popup == "" {
		s.ComposerReady = true
		rest := strings.TrimPrefix(lines[composer], composerPrompt)
		rest = strings.TrimPrefix(rest, " ")
		// Continuation lines of a multi-line draft follow the prompt line
		// indented; the footer lines are indented too, so only the first
		// line decides.
		s.DraftEmpty = strings.TrimSpace(StripANSI(rest)) == "" || strings.HasPrefix(rest, dim)
	}
	return s
}
