package claude

import (
	"regexp"
	"strings"

	"github.com/Lewin671/remote-agent/internal/agent"
)

// Claude Code draws its input box between two horizontal rules: "❯", a
// no-break space, then the draft. The cursor is a reversed cell it draws
// itself; placeholders are dim. Dialogs replace the input box
// (claude-code-notes §4).
const promptMark = "❯"

// bottomLines is how much of the screen bottom is inspected; dialogs such
// as the plan prompt can be tall.
const bottomLines = 60

// selectionRE matches the highlighted row of a list: numbered, or
// indented as in the trust dialog. Earlier user messages also start with
// the prompt, but at the line start and without a number.
var selectionRE = regexp.MustCompile(`^\s*❯\s*\d+\.|^\s+❯\s`)

// popups names dialogs by text they show, most specific first.
var popups = []struct{ text, name string }{
	{"Yes, I trust this folder", "trust folder"},
	{"Accessing workspace", "trust folder"},
	{"Bypass Permissions mode", "bypass warning"},
	{"Claude has written up a plan", "plan"},
	{"Ready to submit your answers?", "question"},
	{"Enter to select", "question"},
	{"Select model", "model menu"},
	{"Switch model?", "model menu"},
	{"Do you want to", "approval"},
}

// modes maps the mode line to Claude Code's names for the modes.
var modes = []struct{ text, mode string }{
	{"manual mode on", "default"},
	{"accept edits on", "acceptEdits"},
	{"plan mode on", "plan"},
	{"bypass permissions on", "bypassPermissions"},
	{"auto mode on", "auto"},
}

type line struct {
	styled string
	plain  string
}

// AnalyzeScreen reads a capture made with escape sequences kept.
func AnalyzeScreen(styled string) agent.Screen {
	var lines []line
	for _, l := range strings.Split(strings.TrimRight(styled, "\n"), "\n") {
		lines = append(lines, line{styled: l, plain: strings.ReplaceAll(agent.StripANSI(l), " ", " ")})
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1].plain) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > bottomLines {
		lines = lines[len(lines)-bottomLines:]
	}

	var s agent.Screen
	top, bottom := composer(lines)
	if top < 0 {
		// The dialog's own words are the lowest match; what is above it
		// may be conversation.
	find:
		for i := len(lines) - 1; i >= 0; i-- {
			// A narrow window may wrap the words onto the next line.
			joined := lines[i].plain
			if i+1 < len(lines) {
				joined = strings.Join(strings.Fields(joined+" "+lines[i+1].plain), " ")
			}
			for _, p := range popups {
				if strings.Contains(lines[i].plain, p.text) || strings.Contains(joined, p.text) {
					s.Popup = p.name
					break find
				}
			}
		}
		// A dialog always shows a numbered list with a highlighted row;
		// without one the words are text, as while starting up.
		selection := false
		for _, l := range lines {
			if selectionRE.MatchString(l.plain) {
				selection = true
			}
		}
		switch {
		case !selection:
			s.Popup = ""
		case s.Popup == "":
			s.Popup = "menu"
		}
		s.QuestionOpen = s.Popup == "question"
		s.PlanOpen = s.Popup == "plan"
		s.PermissionOpen = s.Popup == "approval"
		return s
	}

	s.ComposerReady = true
	s.DraftEmpty = bottom == top+1 && emptyPrompt(lines[top].styled)
	for _, l := range lines[bottom+1:] {
		if s.Footer == "" && strings.TrimSpace(l.plain) != "" {
			s.Footer = strings.TrimSpace(l.plain)
		}
		for _, m := range modes {
			if strings.Contains(l.plain, m.text) {
				s.Mode = m.mode
			}
		}
	}
	return s
}

// composer finds the input box: the lowest line starting with the prompt
// right under a rule, and the rule closing it. It returns -1 if there is
// none, as when a dialog is showing.
func composer(lines []line) (int, int) {
	for i := len(lines) - 1; i >= 1; i-- {
		if !strings.HasPrefix(lines[i].plain, promptMark) || !isRule(lines[i-1].plain) {
			continue
		}
		// Earlier user messages also start with the prompt, on a
		// background color.
		if runs := agent.Runs(lines[i].styled); len(runs) > 0 {
			first := runs[0]
			for _, r := range runs {
				if strings.TrimSpace(r.Text) != "" {
					first = r
					break
				}
			}
			if first.BG >= 0 {
				continue
			}
		}
		for j := i + 1; j < len(lines) && j <= i+20; j++ {
			if isRule(lines[j].plain) {
				return i, j
			}
		}
	}
	return -1, -1
}

// isRule reports whether a line is one of the input box's rules, which may
// carry a title such as the session name.
func isRule(plain string) bool {
	t := strings.TrimSpace(plain)
	return strings.HasPrefix(t, "─") && strings.Count(t, "─") >= 10
}

// emptyPrompt reports whether the input box line holds no draft: after
// the prompt there is only the cursor (a reversed cell) and dim
// placeholder text. A reversed character followed by nothing dim is a
// draft with the cursor on it.
func emptyPrompt(styled string) bool {
	runs := agent.Runs(styled)
	seen := false
	var normal, reversed, dim strings.Builder
	for _, r := range runs {
		text := r.Text
		if !seen {
			i := strings.Index(text, promptMark)
			if i < 0 {
				continue
			}
			seen = true
			text = text[i+len(promptMark):]
		}
		text = strings.TrimSpace(strings.ReplaceAll(text, " ", " "))
		switch {
		case text == "":
		case r.Dim:
			dim.WriteString(text)
		case r.Reverse:
			reversed.WriteString(text)
		default:
			normal.WriteString(text)
		}
	}
	if normal.Len() > 0 {
		return false
	}
	return reversed.Len() == 0 || dim.Len() > 0
}
