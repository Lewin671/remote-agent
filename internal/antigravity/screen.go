package antigravity

import (
	"fmt"
	"regexp"
	"strconv"
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

// composerRegion finds the last actual input box, allowing the native
// background-task bar to add another rule below it.
func composerRegion(styled string) (lines []string, top, bottom int, ok bool) {
	lines = strings.Split(styled, "\n")
	previous := -1
	for i, l := range lines {
		if !dividerRE.MatchString(strings.TrimSpace(StripANSI(l))) {
			continue
		}
		if previous >= 0 && i-previous >= 2 && i-previous <= 40 && strings.HasPrefix(strings.TrimSpace(StripANSI(lines[previous+1])), ">") {
			valid := true
			for _, row := range lines[previous+1 : i] {
				if spinnerRE.MatchString(strings.TrimSpace(StripANSI(row))) {
					valid = false
				}
			}
			if valid {
				top, bottom, ok = previous, i, true
			}
		}
		previous = i
	}
	return
}

var backgroundRowRE = regexp.MustCompile(`^● \[\d{2}:\d{2}:\d{2}\] .+ running$`)

// A panel may leave the composer visible. Its immediate trailing section is
// interactive until proved otherwise; the native background bar is passive.
func trailingPanel(lines []string, bottom int) string {
	tail := lines[bottom+1:]
	for len(tail) > 0 && backgroundRowRE.MatchString(strings.TrimSpace(StripANSI(tail[0]))) {
		tail = tail[1:]
		if len(tail) == 0 || !dividerRE.MatchString(strings.TrimSpace(StripANSI(tail[0]))) {
			return "unknown panel"
		}
		tail = tail[1:]
	}
	if len(tail) > 0 && strings.TrimSpace(StripANSI(tail[0])) != "" {
		return strings.TrimSpace(StripANSI(tail[0]))
	}
	// Menus can be separated from the composer by a blank row, too. Footer
	// output may be user-defined; native navigation/selection affordances prove
	// an interaction rather than assuming arbitrary status text is a panel.
	for i, line := range tail {
		text := strings.TrimSpace(StripANSI(line))
		if strings.Contains(text, "Keyboard:") || strings.Contains(text, "Navigate") || strings.HasPrefix(text, "> ") || strings.HasPrefix(text, "› ") {
			for _, title := range tail[:i+1] {
				if title = strings.TrimSpace(StripANSI(title)); title != "" {
					return title
				}
			}
		}
	}
	return ""
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
		if title := trailingPanel(lines, bottom); title != "" {
			s.Popup = title
			return s
		}
		s.ComposerReady = true
		text, ok := a.ComposerText(styled, "")
		s.DraftEmpty = ok && text == "" && attachedMedia(lines, top) == 0
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
	if !ok || trailingPanel(lines, bottom) != "" {
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

var mediaAttachedRE = regexp.MustCompile(`^(?:▸ 📎 ([1-9][0-9]*) media attached(?: \([^()]*\))? +\(ctrl\+o to expand\)|▾ 📎 ([1-9][0-9]*) Media\(s\) attached)$`)

// Attached media is outside the text box, immediately above its top rule.
// Expanded media has a header, one row per attachment and a collapse hint.
func attachedMedia(lines []string, top int) int {
	if top == 0 {
		return 0
	}
	end := top - 1
	row := strings.TrimSpace(StripANSI(lines[end]))
	if row == "(ctrl+o to collapse)" {
		for end--; end >= 0; end-- {
			row = strings.TrimSpace(StripANSI(lines[end]))
			if strings.HasPrefix(row, "▾ 📎") {
				break
			}
			// Do not borrow a historical media header across a transcript
			// separator. Wrapped native media rows are left untrusted below.
			if row == "" || dividerRE.MatchString(row) || end < top-102 {
				return 0
			}
		}
		if end < 0 {
			return 0
		}
	}
	match := mediaAttachedRE.FindStringSubmatch(row)
	if match == nil {
		if strings.HasPrefix(row, "▸ 📎 ") && strings.Contains(row, "(ctrl+o to expand)") || strings.HasPrefix(row, "▾ 📎 ") && strings.TrimSpace(StripANSI(lines[top-1])) == "(ctrl+o to collapse)" {
			return -1
		}
		return 0
	}
	digits := match[1]
	if digits == "" {
		digits = match[2]
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n > 100 {
		return -1
	}
	if match[2] != "" && top-end != n+2 {
		return -1
	}
	return n
}
func (a *Adapter) ImageInput(styled, footer string) (string, []int32, bool) {
	text, ok := a.ComposerText(styled, footer)
	lines, top, _, box := composerRegion(styled)
	if !ok || !box {
		return "", nil, false
	}
	count := attachedMedia(lines, top)
	if count < 0 {
		return "", nil, false
	}
	var indexes []int32
	var tokens strings.Builder
	for i := 1; i <= count; i++ {
		indexes = append(indexes, int32(i))
		fmt.Fprintf(&tokens, "[Image #%d]", i)
	}
	return tokens.String() + text, indexes, true
}

// Escape clears attached media in agy 1.5.0; Ctrl-U and Backspace leave it.
// Escape also interrupts a foreground turn, so it is only safe while idle.
func (a *Adapter) ClearImageInput(count int, running bool) []byte {
	if count == 0 {
		return a.ClearInput()
	}
	if running {
		return nil
	}
	return append(a.ClearInput(), []byte(agent.KeyEsc)...)
}
