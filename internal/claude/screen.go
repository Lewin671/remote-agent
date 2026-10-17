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
	s.Busy = busy(lines, top)
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

// backgroundRE matches Claude Code's live notes about background work: the
// mode line's "· 1 shell", "Waiting for 1 background agent to finish", and
// the rows of running subagents under the mode line (claude-code-notes §10).
var backgroundRE = regexp.MustCompile(`\b\d+ shells?\b|\b\d+ background (?:agents?|tasks?)\b|^\s*◯ `)

var waitingRE = regexp.MustCompile(`Waiting for \d+ background (?:agents?|tasks?)`)

// BackgroundWork describes background work the screen shows as running,
// or is empty. Only the live parts are read: the line above the input box
// and what is under it; older notes in the conversation are not.
func BackgroundWork(styled string) string {
	var lines []line
	for _, l := range strings.Split(strings.TrimRight(styled, "\n"), "\n") {
		lines = append(lines, line{styled: l, plain: strings.ReplaceAll(agent.StripANSI(l), " ", " ")})
	}
	top, bottom := composer(lines)
	if top < 0 {
		return ""
	}
	// Above the input box only the live "Waiting for …" counts; a turn's
	// summary ("… · 1 shell still running") stays after the shell ends.
	if top >= 2 {
		if m := waitingRE.FindString(lines[top-2].plain); m != "" {
			return strings.TrimPrefix(m, "Waiting for ")
		}
	}
	for _, l := range lines[bottom+1:] {
		if m := backgroundRE.FindString(l.plain); m != "" {
			if strings.Contains(m, "◯") {
				return "a background agent"
			}
			return m
		}
	}
	return ""
}

// spinnerRE matches the line Claude Code shows above the input box while
// it works: "· Fermenting… (8s · ↓ 118 tokens)", "· Compacting
// conversation… (2s)".
var spinnerRE = regexp.MustCompile(`…\s*\(\d+s\b`)

// busy reports whether the screen shows Claude Code working: its spinner
// line just above the input box, or the input box's prompt dimmed (color
// 246), as while a turn runs (claude-code-notes §4).
func busy(lines []line, top int) bool {
	for i := top - 2; i >= 0 && i >= top-3; i-- {
		if spinnerRE.MatchString(lines[i].plain) {
			return true
		}
	}
	for _, r := range agent.Runs(lines[top].styled) {
		if strings.Contains(r.Text, promptMark) {
			return r.FG == 246
		}
	}
	return false
}

// ComposerText is the text between the input box's rules, without the
// prompt, the cursor cell and dim placeholder text. ok is false when no
// input box shows.
func ComposerText(styled string) (string, bool) {
	var lines []line
	for _, l := range strings.Split(strings.TrimRight(styled, "\n"), "\n") {
		lines = append(lines, line{styled: l, plain: strings.ReplaceAll(agent.StripANSI(l), "\u00a0", " ")})
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1].plain) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > bottomLines {
		lines = lines[len(lines)-bottomLines:]
	}
	top, bottom := composer(lines)
	if top < 0 {
		return "", false
	}
	if bottom == top+1 && emptyPrompt(lines[top].styled) {
		return "", true
	}
	var parts []string
	for i := top; i < bottom; i++ {
		var b strings.Builder
		for _, r := range agent.Runs(lines[i].styled) {
			if r.Dim {
				continue
			}
			b.WriteString(r.Text)
		}
		t := strings.ReplaceAll(b.String(), "\u00a0", " ")
		if i == top {
			if j := strings.Index(t, promptMark); j >= 0 {
				t = t[j+len(promptMark):]
			}
		}
		parts = append(parts, strings.TrimSpace(t))
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), true
}

// RestoredPrompt recognizes an early-canceled request restored for editing
// by Claude Code 2.1.300. The composer remains visible while working, so
// its presence alone is not an end signal. Screen captures cannot tell
// soft wraps from original whitespace. Ignore all whitespace on both
// sides so wraps inside Chinese text or a long token do not change it.
func RestoredPrompt(styled, prompt string) bool {
	scr := AnalyzeScreen(styled)
	if !scr.ComposerReady || scr.Busy || scr.DraftEmpty || strings.TrimSpace(prompt) == "" {
		return false
	}
	text, ok := ComposerText(styled)
	return ok && strings.Join(strings.Fields(text), "") == strings.Join(strings.Fields(prompt), "")
}
