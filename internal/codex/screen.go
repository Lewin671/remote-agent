package codex

import (
	"regexp"
	"strings"
)

// Screen is what remote-agent can tell from a capture of the Codex TUI.
type Screen struct {
	// ComposerReady: the input box is showing and no menu or popup covers it.
	ComposerReady bool
	// DraftEmpty: the input box shows its dim placeholder, so it holds no
	// unsent text.
	DraftEmpty bool
	// Popup names the menu or dialog in front of the input box, if any.
	Popup string
	// QuestionOpen: a request_user_input question is waiting for an answer.
	QuestionOpen bool
	// PlanOpen: Codex asks whether to implement the plan it proposed.
	PlanOpen bool
	// PlanMode: the footer shows Plan mode is on.
	PlanMode bool
	// Footer is the plain status line under the input box, e.g.
	// "GPT-6-Sol medium · ~/code/foo".
	Footer string
}

var sgrRE = regexp.MustCompile("\x1b\\[[0-9;:]*[A-Za-z]")

// StripANSI removes SGR and other CSI sequences.
func StripANSI(s string) string { return sgrRE.ReplaceAllString(s, "") }

const (
	// The composer prompt is a bold "›" followed by a reset.
	composerPrompt = "\x1b[1m›\x1b[0m"
	// The highlighted row of a selection list is bold and reversed.
	selectionCursor = "\x1b[1;7m›"
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
	{"Implement this plan?", "plan"},
	{"Update Model Permissions", "permissions menu"},
	{"Enable full access?", "permissions menu"},
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
	// A dialog always shows a selection list. Its title only names it:
	// the same words in the conversation above an input box mean nothing.
	if !selection {
		s.Popup = ""
	} else if s.Popup == "" {
		s.Popup = "menu"
	}
	s.QuestionOpen = s.Popup == "question"
	s.PlanOpen = s.Popup == "plan"
	for _, l := range lines {
		if strings.HasSuffix(strings.TrimSpace(StripANSI(l)), "Plan mode") {
			s.PlanMode = true
		}
	}
	if composer >= 0 {
		// The status line is the first non-blank line under the input box;
		// Codex may leave a blank line in between.
		for _, l := range lines[composer+1:] {
			if f := strings.TrimSpace(StripANSI(l)); f != "" {
				s.Footer = f
				break
			}
		}
	}
	if composer >= 0 && s.Popup == "" {
		s.ComposerReady = true
		// Only the placeholder proves the box is empty. A bare prompt may
		// head a multi-line draft whose text is on later lines, and those
		// cannot be told apart from the footer, so it counts as a draft.
		s.DraftEmpty = startsDim(strings.TrimPrefix(lines[composer], composerPrompt))
	}
	return s
}

// startsDim reports whether the first visible text of s is drawn dim.
// Codex may color the input box's background first, when the attached
// terminal reports its own background color, so leading spaces and other
// style changes are skipped.
func startsDim(s string) bool {
	dimOn := false
	for s != "" {
		if s[0] == ' ' {
			s = s[1:]
			continue
		}
		loc := sgrRE.FindStringIndex(s)
		if loc == nil || loc[0] != 0 {
			return dimOn // visible text
		}
		seq := s[:loc[1]]
		s = s[loc[1]:]
		if strings.HasSuffix(seq, "m") {
			dimOn = applyDim(dimOn, strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m"))
		}
	}
	return false // nothing visible: a bare prompt
}

// applyDim tracks the dim attribute through one SGR sequence's parameters.
func applyDim(on bool, params string) bool {
	ps := strings.FieldsFunc(params, func(r rune) bool { return r == ';' || r == ':' })
	if len(ps) == 0 {
		return false // ESC[m resets
	}
	for i := 0; i < len(ps); i++ {
		switch ps[i] {
		case "0", "22":
			on = false
		case "2":
			on = true
		case "38", "48", "58":
			// Extended colors carry their own arguments: 5;n or 2;r;g;b.
			if i+1 < len(ps) && ps[i+1] == "5" {
				i += 2
			} else if i+1 < len(ps) && ps[i+1] == "2" {
				i += 4
			}
		}
	}
	return on
}
