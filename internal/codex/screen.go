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

// Codex draws a few marks that tell the screen's state. They are told
// apart by their style, not by exact escape sequences: versions write the
// same style differently ("\x1b[1m", "\x1b[0;1m", "\x1b[1;39m", ...).
//
//   - the input box prompt: "›" in bold, not dim, not reversed
//   - earlier user messages: "›" in bold and dim
//   - the highlighted row of a selection list: "›" reversed
//   - the empty input box's placeholder: dim text after the prompt
const promptMark = "›"

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
		if st, rest := leadingStyle(l, true); strings.HasPrefix(rest, promptMark) && st.reverse {
			selection = true
		}
		if st, rest := leadingStyle(l, false); strings.HasPrefix(rest, promptMark) && st.bold && !st.dim && !st.reverse {
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
		_, rest := leadingStyle(lines[composer], false)
		s.DraftEmpty = startsDim(strings.TrimPrefix(rest, promptMark))
	}
	return s
}

// style is the SGR state that matters here.
type style struct{ bold, dim, reverse bool }

// apply updates st with one SGR sequence's parameters.
func (st *style) apply(params string) {
	ps := strings.FieldsFunc(params, func(r rune) bool { return r == ';' || r == ':' })
	if len(ps) == 0 {
		*st = style{} // ESC[m resets
		return
	}
	for i := 0; i < len(ps); i++ {
		switch ps[i] {
		case "0":
			*st = style{}
		case "1":
			st.bold = true
		case "2":
			st.dim = true
		case "7":
			st.reverse = true
		case "22":
			st.bold, st.dim = false, false
		case "27":
			st.reverse = false
		case "38", "48", "58":
			// Extended colors carry their own arguments: 5;n or 2;r;g;b.
			if i+1 < len(ps) && ps[i+1] == "5" {
				i += 2
			} else if i+1 < len(ps) && ps[i+1] == "2" {
				i += 4
			}
		}
	}
}

// leadingStyle skips the escape sequences, and with skipSpaces the spaces,
// at the start of s. It returns the style in effect and the rest.
func leadingStyle(s string, skipSpaces bool) (style, string) {
	var st style
	for s != "" {
		if skipSpaces && s[0] == ' ' {
			s = s[1:]
			continue
		}
		loc := sgrRE.FindStringIndex(s)
		if loc == nil || loc[0] != 0 {
			break
		}
		if seq := s[:loc[1]]; strings.HasSuffix(seq, "m") {
			st.apply(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m"))
		}
		s = s[loc[1]:]
	}
	return st, s
}

// startsDim reports whether the first visible text of s is drawn dim.
// Codex may color the input box's background first, when the attached
// terminal reports its own background color, so leading spaces and other
// style changes are skipped.
func startsDim(s string) bool {
	st, rest := leadingStyle(s, true)
	return rest != "" && st.dim
}

// ComposerText is the text in the input box, its lines joined with "\n":
// the prompt line and every line under it down to the status line. footer
// is the status line as seen while the input box was empty, which is the
// only safe way to tell where a draft of several lines ends: its lines are
// indented like the status line, and may be blank. ok is false when no
// input box shows, a popup covers it, or the status line is not found (allowing resize elision and spacing).
func ComposerText(styled, footer string) (string, bool) {
	lines := strings.Split(strings.TrimRight(styled, "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(StripANSI(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > bottomLines {
		lines = lines[len(lines)-bottomLines:]
	}
	scr := AnalyzeScreen(styled)
	if !scr.ComposerReady {
		return "", false
	}
	if scr.DraftEmpty {
		return "", true
	}
	footer = strings.TrimSpace(footer)
	if footer == "" {
		return "", false
	}
	composer := -1
	for i, l := range lines {
		if st, rest := leadingStyle(l, false); strings.HasPrefix(rest, promptMark) && st.bold && !st.dim && !st.reverse {
			composer = i
		}
	}
	if composer < 0 {
		return "", false
	}
	// Search from the bottom: a local multiline draft can itself contain
	// the old status line. Never truncate that draft at its first match.
	end := -1
	for i := len(lines) - 1; i > composer; i-- {
		t := strings.TrimSpace(StripANSI(lines[i]))
		if sameFooter(t, footer) {
			end = i
			break
		}
		// Below the status row Codex may draw its shortcut/warning row.
		// Any other visible text means we cannot prove the box's boundary.
		if t != "" && !footerHintRE.MatchString(t) {
			return "", false
		}
	}
	if end < 0 {
		return "", false
	}
	_, rest := leadingStyle(lines[composer], false)
	parts := []string{strings.TrimSpace(StripANSI(strings.TrimPrefix(rest, promptMark)))}
	for _, l := range lines[composer+1 : end] {
		parts = append(parts, strings.TrimSpace(StripANSI(l)))
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), true
}

// These are UI chrome, not a second part of the composer. Keep this
// narrow: unknown trailing text must never be silently dropped.
var footerHintRE = regexp.MustCompile(`^(?:\? for shortcuts(?:\s+⚠ \d+ warnings? · f2 to view)?|⚠ \d+ warnings? · f2 to view)$`)

// sameFooter recognizes only spacing and terminal-width elision changes.
// In particular, an ellipsis cannot consume another status field or the
// warning badge. A changed model/effort or warning count is not accepted.
func sameFooter(a, b string) bool {
	a = strings.Join(strings.Fields(a), " ")
	b = strings.Join(strings.Fields(b), " ")
	if a == b {
		return true
	}
	matches := func(short, full string) bool {
		if !strings.Contains(short, "…") {
			return false
		}
		pattern := strings.ReplaceAll(regexp.QuoteMeta(short), "…", `[^·⚠]+`)
		return regexp.MustCompile("^" + pattern + "$").MatchString(full)
	}
	return matches(a, b) || matches(b, a)
}
