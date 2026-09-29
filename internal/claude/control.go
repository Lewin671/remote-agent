package claude

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// Operations that drive Claude Code's menus (claude-code-notes §5, §7-§9).
// The daemon holds the terminal for them; each step checks the screen
// first and waits for the screen it expects. An unexpected screen is left
// with Esc and reported.

func screenNow(t agent.Term) (agent.Screen, string, error) {
	styled, err := t.Capture()
	if err != nil {
		return agent.Screen{}, "", err
	}
	return AnalyzeScreen(styled), plainText(styled), nil
}

func plainText(styled string) string {
	return strings.ReplaceAll(agent.StripANSI(styled), " ", " ")
}

func waitFor(t agent.Term, what string, ok func(agent.Screen, string) bool) (string, error) {
	styled, err := agent.WaitFor(t, what, func(styled string) bool { return ok(AnalyzeScreen(styled), plainText(styled)) })
	return plainText(styled), err
}

// bail leaves whatever dialog is open.
func bail(t agent.Term) {
	for i := 0; i < 3; i++ {
		if scr, _, err := screenNow(t); err == nil && scr.ComposerReady {
			return
		}
		_ = t.Write([]byte(agent.KeyEsc))
		time.Sleep(200 * time.Millisecond)
	}
}

func readyComposer(t agent.Term) (agent.Screen, error) {
	scr, _, err := screenNow(t)
	if err != nil {
		return scr, err
	}
	if !scr.ComposerReady || !scr.DraftEmpty {
		return scr, agent.ErrNotReady
	}
	return scr, nil
}

// menuRow matches a numbered row: its number and label, up to two spaces.
var menuRow = regexp.MustCompile(`^\s*(❯)?\s*(\d+)\.\s+(.+?)(?:\s{2,}(.*))?$`)

type row struct {
	num    int
	label  string
	detail string
	cursor bool
}

// rowsAfter reads the numbered rows below the lowest line containing title.
func rowsAfter(plain, title string) []row {
	lines := strings.Split(plain, "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, title) {
			start = i
		}
	}
	if start < 0 {
		return nil
	}
	var out []row
	for _, l := range lines[start+1:] {
		m := menuRow.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		out = append(out, row{num: n, label: strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[3]), "✔")), detail: m[4], cursor: m[1] != ""})
	}
	return out
}

// effortRE reads the effort line of /model, e.g. "◐ Medium effort
// (default) ←/→ to adjust" or "○ Effort not supported for Haiku".
var effortRE = regexp.MustCompile(`(?i)\b(low|medium|high|xhigh|max) effort\b`)

func shownEffort(plain string) string {
	lines := strings.Split(plain, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "Effort not supported") {
			return ""
		}
		if m := effortRE.FindStringSubmatch(lines[i]); m != nil && strings.Contains(lines[i], "to adjust") {
			return strings.ToLower(m[1])
		}
	}
	return ""
}

// SetModel picks the model in /model with the arrow keys, sets the effort
// with ←/→ and presses s, which applies to this session only; the number
// keys and Enter would change the default.
func (a *Adapter) SetModel(t agent.Term, model, effort string) error {
	var info *pb.ModelInfo
	for _, m := range models {
		if m.Slug == model {
			info = m
		}
	}
	if info == nil {
		return fmt.Errorf("model %s: %w", model, agent.ErrNotFound)
	}
	if effort != "" && len(info.Efforts) == 0 {
		return fmt.Errorf("%s has no effort levels: %w", info.DisplayName, agent.ErrUnsupported)
	}
	if _, err := readyComposer(t); err != nil {
		return err
	}
	if err := agent.Slash(t, "/model"); err != nil {
		return err
	}
	plain, err := waitFor(t, "the model menu", func(_ agent.Screen, p string) bool {
		return strings.Contains(p, "Select model") && strings.Contains(p, "to use this session only")
	})
	if err != nil {
		bail(t)
		return err
	}
	rows := rowsAfter(plain, "Select model")
	target, cur := -1, -1
	for i, r := range rows {
		if strings.EqualFold(strings.Fields(r.label)[0], info.DisplayName) {
			target = i
		}
		if r.cursor {
			cur = i
		}
	}
	if target < 0 || cur < 0 {
		bail(t)
		return fmt.Errorf("model %s is not in Claude Code's /model menu", info.DisplayName)
	}
	key := agent.KeyDown
	steps := target - cur
	if steps < 0 {
		key, steps = agent.KeyUp, -steps
	}
	for i := 0; i < steps; i++ {
		if err := t.Write([]byte(key)); err != nil {
			return err
		}
		time.Sleep(60 * time.Millisecond)
	}
	plain, err = waitFor(t, "the cursor on "+info.DisplayName, func(_ agent.Screen, p string) bool {
		for _, r := range rowsAfter(p, "Select model") {
			if r.cursor {
				return strings.EqualFold(strings.Fields(r.label)[0], info.DisplayName)
			}
		}
		return false
	})
	if err != nil {
		bail(t)
		return err
	}
	if effort != "" {
		// → cycles low … max and wraps; read the line after every press.
		for i := 0; shownEffort(plain) != effort; i++ {
			if i >= len(efforts) {
				bail(t)
				return fmt.Errorf("effort %s is not offered for %s", effort, info.DisplayName)
			}
			before := shownEffort(plain)
			if err := t.Write([]byte(agent.KeyRight)); err != nil {
				return err
			}
			plain, err = waitFor(t, "the effort to change", func(_ agent.Screen, p string) bool { return shownEffort(p) != before })
			if err != nil {
				bail(t)
				return err
			}
		}
	}
	if err := t.Write([]byte("s")); err != nil {
		return err
	}
	// With a cached conversation Claude Code asks to confirm the switch.
	_, err = waitFor(t, "the model to switch", func(scr agent.Screen, p string) bool {
		return scr.ComposerReady || strings.Contains(p, "Switch model?")
	})
	if err != nil {
		bail(t)
		return err
	}
	if scr, p, err := screenNow(t); err == nil && !scr.ComposerReady && strings.Contains(p, "Switch model?") {
		if err := t.Write([]byte("1")); err != nil {
			return err
		}
	}
	if _, err := waitFor(t, "Claude Code to confirm the model", func(scr agent.Screen, p string) bool {
		return scr.ComposerReady && confirmed(p, info.DisplayName, effort)
	}); err != nil {
		bail(t)
		return err
	}
	return nil
}

// confirmed reports whether the latest "Set model to …" note names the
// model and effort, e.g. "Set model to Sonnet 5.5 for this session only
// with low effort".
func confirmed(plain, name, effort string) bool {
	lines := strings.Split(plain, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "Set model to") {
			l := lines[i]
			return strings.Contains(l, name) && strings.Contains(l, "for this session only") &&
				(effort == "" || strings.Contains(l, "with "+effort+" effort"))
		}
	}
	return false
}

// lastLines is the bottom n lines of plain.
func lastLines(plain string, n int) string {
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// cycle is the order Shift+Tab goes through the modes; modes not on offer
// are skipped (claude-code-notes §5).
var cycle = []string{"default", "acceptEdits", "plan", "bypassPermissions", "auto"}

// looser are the modes that let tools run without asking.
var looser = map[string]bool{"bypassPermissions": true, "auto": true}

// SetMode presses Shift+Tab, reading the mode line after each press, until
// it shows the mode. A mode not on offer is never reached: after a full
// turn the original mode is restored and an error returned. While a turn
// runs it refuses to pass through a looser mode on the way, which could
// let a tool call through in that moment.
func (a *Adapter) SetMode(t agent.Term, mode pb.ApprovalMode, running bool) error {
	want, ok := permissionModes[mode]
	if !ok {
		return fmt.Errorf("unknown approval mode %v", mode)
	}
	scr, err := readyComposer(t)
	if err != nil {
		return err
	}
	start := scr.Mode
	if start == "" {
		return errors.New("cannot read the permission mode on the screen")
	}
	if start == want {
		return nil
	}
	if running {
		for i := index(start) + 1; ; i++ {
			m := cycle[i%len(cycle)]
			if m == want {
				break
			}
			if looser[m] {
				return agent.ErrBusy
			}
		}
	}
	press := func() (string, error) {
		before, _, err := screenNow(t)
		if err != nil {
			return "", err
		}
		if err := t.Write([]byte(agent.KeyBTab)); err != nil {
			return "", err
		}
		var now agent.Screen
		_, err = waitFor(t, "the permission mode to change", func(s agent.Screen, _ string) bool {
			now = s
			return s.ComposerReady && s.Mode != "" && s.Mode != before.Mode
		})
		return now.Mode, err
	}
	for i := 0; i < len(cycle); i++ {
		cur, err := press()
		if err != nil {
			return err
		}
		if cur == want {
			return nil
		}
		if cur == start {
			break // a full turn
		}
	}
	// Back to where it was.
	for i := 0; i < len(cycle); i++ {
		if scr, _, err := screenNow(t); err != nil || scr.Mode == start {
			break
		}
		if _, err := press(); err != nil {
			break
		}
	}
	if mode == pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS {
		return fmt.Errorf("%w: this session was not started with bypass permissions allowed", agent.ErrUnsupported)
	}
	return fmt.Errorf("%w: %s is not on offer in this session", agent.ErrUnsupported, want)
}

func index(mode string) int {
	for i, m := range cycle {
		if m == mode {
			return i
		}
	}
	return 0
}

// Compact runs /compact; the transcript's compact_boundary records it.
func (a *Adapter) Compact(t agent.Term, started func() bool) error {
	if _, err := readyComposer(t); err != nil {
		return err
	}
	if err := agent.Slash(t, "/compact"); err != nil {
		return err
	}
	_, err := waitFor(t, "compaction to start", func(_ agent.Screen, p string) bool {
		b := lastLines(p, 30)
		return strings.Contains(b, "Compacting conversation") || strings.Contains(b, "Compacted") || started()
	})
	return err
}

// AnswerPlan answers "Would you like to proceed?": implementing picks
// "Yes, manually approve edits", which returns to manual mode; keeping on
// planning is Esc, which stays in Plan mode and ends the turn. There is no
// choice to clear the context first.
func (a *Adapter) AnswerPlan(t agent.Term, choice pb.PlanChoice) error {
	scr, plain, err := screenNow(t)
	if err != nil {
		return err
	}
	if !scr.PlanOpen {
		return agent.ErrNoPlan
	}
	var label string
	switch choice {
	case pb.PlanChoice_PLAN_CHOICE_IMPLEMENT:
		label = "manually approve edits"
	case pb.PlanChoice_PLAN_CHOICE_CLEAR_AND_IMPLEMENT:
		label = "clear context"
	case pb.PlanChoice_PLAN_CHOICE_KEEP_PLANNING:
		return t.Write([]byte(agent.KeyEsc))
	default:
		return fmt.Errorf("unknown plan choice %v", choice)
	}
	for _, r := range rowsAfter(plain, "Would you like to proceed?") {
		if strings.Contains(strings.ToLower(r.label), label) {
			return t.Write([]byte(strconv.Itoa(r.num)))
		}
	}
	return fmt.Errorf("%w: the plan prompt offers no %q", agent.ErrUnsupported, label)
}

// Answer presses the option numbers of the question showing: one picks
// and moves on; for a multi-select question they tick options and → goes
// on. After the last question Claude Code shows a review page, where 1
// submits (claude-code-notes §7).
func (a *Adapter) Answer(t agent.Term, q *pb.QuestionAsked, index *int32, options []int32) error {
	i := 0
	if index != nil {
		i = int(*index)
	} else if len(q.Questions) > 1 {
		// Without an index, answer the question that is showing.
		plain := ""
		if styled, err := t.Capture(); err == nil {
			plain = plainText(styled)
		}
		for j, item := range q.Questions {
			if strings.Contains(plain, item.Text) {
				i = j
			}
		}
	}
	if i < 0 || i >= len(q.Questions) {
		return fmt.Errorf("question index %d out of range", i)
	}
	item := q.Questions[i]
	if !item.MultiSelect && len(options) != 1 {
		return fmt.Errorf("the question takes one option, got %d", len(options))
	}
	for _, o := range options {
		if err := t.Write([]byte(strconv.Itoa(int(o)))); err != nil {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	if item.MultiSelect {
		if err := t.Write([]byte(agent.KeyRight)); err != nil {
			return err
		}
	}
	// Submit on the review page if it comes up.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		scr, plain, err := screenNow(t)
		if err != nil {
			return err
		}
		if strings.Contains(plain, "Ready to submit your answers?") {
			return t.Write([]byte("1"))
		}
		if !scr.QuestionOpen {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}
