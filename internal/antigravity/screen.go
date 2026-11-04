package antigravity

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/Lewin671/remote-agent/internal/agent"
)

var dividerRE = regexp.MustCompile(`^─{10,}$`)
var spinnerRE = regexp.MustCompile(`^[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⣾⣽⣻⢿⣷⣟⣯⣸]\s+`)
var questionHeaderRE = regexp.MustCompile(`(?m)^Question ([0-9]+)/([0-9]+):\s*`)

func StripANSI(s string) string { return agent.StripANSI(s) }
func normalize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// composerRegion uses the last pair of full-width rules, and only accepts an
// input box whose first row begins with the live prompt. Menus and transcript
// separators also use rules, so a pair alone proves nothing.
func composerRegion(styled string) (lines []string, top, bottom int, ok bool) {
	lines = strings.Split(styled, "\n")
	var rules []int
	for i, l := range lines {
		if dividerRE.MatchString(strings.TrimSpace(StripANSI(l))) {
			rules = append(rules, i)
		}
	}
	if len(rules) < 2 {
		return lines, 0, 0, false
	}
	top, bottom = rules[len(rules)-2], rules[len(rules)-1]
	if bottom-top < 2 || bottom-top > 40 {
		return lines, top, bottom, false
	}
	first := strings.TrimSpace(StripANSI(lines[top+1]))
	if !strings.HasPrefix(first, ">") {
		return lines, top, bottom, false
	}
	for _, l := range lines[top+1 : bottom] {
		if spinnerRE.MatchString(strings.TrimSpace(StripANSI(l))) {
			return lines, top, bottom, false
		}
	}
	return lines, top, bottom, true
}
func (a *Adapter) Analyze(styled string) agent.Screen {
	plain := StripANSI(styled)

	var s agent.Screen
	lines, top, bottom, composer := composerRegion(styled)
	for i, raw := range lines {
		line := StripANSI(raw)
		if strings.HasPrefix(strings.TrimSpace(line), "warning: conversation ") {
			warning := line
			if i+1 < len(lines) {
				warning += " " + lines[i+1]
			}
			if strings.Contains(strings.Join(strings.Fields(warning), " "), "not found") {
				s.IdentityError = "requested conversation not found"
			}
		}
	}
	tail := plain
	if composer {
		tail = strings.Join(strings.Split(plain, "\n")[bottom+1:], "\n")
	}
	// Only active menu sections count, never similarly worded chat history.
	if strings.Contains(tail, "Is this a project you created or one you trust?") || strings.Contains(tail, "Do you trust the contents of this project?") {
		s.Popup = "trust folder"
		return s
	}
	if strings.Contains(tail, "Switch Model") && strings.Contains(tail, "Search:") {
		s.Popup = "model picker"
		return s
	}
	if _, _, ok := permissionBody(plain); ok && (!composer || permissionPosition(plain) > len(strings.Join(strings.Split(plain, "\n")[:bottom+1], "\n"))) {
		s.PermissionOpen = true
		s.Popup = "permission"
		return s
	}
	if questionHeaderRE.MatchString(tail) && strings.Contains(tail, "Navigate") && (strings.Contains(tail, "Select") || strings.Contains(tail, "Toggle")) {
		s.QuestionOpen = true
		s.Popup = "question"
		return s
	}
	if strings.Contains(tail, "Action required (") && strings.Contains(tail, "y/n Approve/reject") && strings.Contains(tail, "esc Done") {
		s.Popup = "plan"
		s.PlanOpen = true
		return s
	}
	if strings.Contains(tail, "Artifacts") && (strings.Contains(tail, "esc Dismiss") || strings.Contains(tail, "esc Done")) {
		s.Popup = "artifacts"
		// An empty list is not a plan. A review must actually offer y/n.
		s.PlanOpen = strings.Contains(strings.ToLower(tail), "approve") && strings.Contains(strings.ToLower(tail), "reject")
		if s.PlanOpen {
			s.Popup = "plan"
		}
		return s
	}
	if composer {
		s.ComposerReady = true
		text, ok := a.ComposerText(styled, "")
		s.DraftEmpty = ok && text == ""
		// Historical spinners and user status bars must not make an idle CLI busy.
		start := top - 5
		if start < 0 {
			start = 0
		}
		for _, l := range lines[start:top] {
			if spinnerRE.MatchString(strings.TrimSpace(StripANSI(l))) {
				s.Busy = true
			}
		}
	}
	return s
}
func (a *Adapter) ComposerText(styled, _ string) (string, bool) {
	lines, top, bottom, ok := composerRegion(styled)
	if !ok {
		return "", false
	}
	var out []string
	for i := top + 1; i < bottom; i++ {
		line := StripANSI(lines[i])
		if i == top+1 {
			idx := strings.Index(line, ">")
			line = strings.TrimPrefix(line[idx+1:], " ")
			if strings.HasPrefix(line, "Auto mode:") || strings.HasPrefix(line, "Plan mode:") || strings.HasPrefix(line, "Accept-edits mode:") {
				// Placeholders are dimmed; a user typing the same words is still a draft.
				dim := false
				for _, r := range agent.Runs(lines[i]) {
					if r.Dim && strings.Contains(r.Text, "mode:") {
						dim = true
					}
				}
				// agy 1.5.0 uses bright-black SGR (90), not SGR dim.
				// With NO_COLOR there is no proof it is a placeholder, so a
				// matching unstyled draft is deliberately left untouched.
				if dim || strings.Contains(lines[i], "\x1b[90m"+line) {
					line = ""
				}
			}
		} else {
			line = strings.TrimPrefix(line, "  ")
		}
		out = append(out, line)
	}
	return strings.TrimRight(strings.Join(out, "\n"), " \n"), true
}

// Interrupted requires the latest activity, directly above an idle composer,
// to be the cancellation banner. An older banner in scrollback is insufficient.
func (a *Adapter) Interrupted(styled string) bool {
	s := a.Analyze(styled)
	if !s.ComposerReady || s.Busy || s.Popup != "" {
		return false
	}
	lines, top, _, ok := composerRegion(styled)
	if !ok {
		return false
	}
	for i := top - 1; i >= 0; i-- {
		l := strings.TrimSpace(StripANSI(lines[i]))
		if l == "" {
			continue
		}
		return strings.Contains(l, "Interrupted · What should Antigravity CLI do instead?")
	}
	return false
}
