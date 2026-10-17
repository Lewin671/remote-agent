package claude

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
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
//
// A narrow window wraps text: the title may continue on the next line,
// and a label's continuation lines are joined to it.
func rowsAfter(plain, title string) []row {
	lines := strings.Split(plain, "\n")
	start := titleLine(lines, title)
	if start < 0 {
		return nil
	}
	var out []row
	for _, l := range lines[start+1:] {
		m := menuRow.FindStringSubmatch(l)
		if m == nil {
			if len(out) > 0 && strings.TrimSpace(l) != "" && strings.HasPrefix(l, "  ") && out[len(out)-1].detail == "" {
				last := &out[len(out)-1]
				last.label += " " + strings.TrimSpace(l)
			} else if strings.TrimSpace(l) == "" && len(out) > 0 {
				out[len(out)-1].detail = " " // no more continuation
			}
			continue
		}
		n, _ := strconv.Atoi(m[2])
		out = append(out, row{num: n, label: strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[3]), "✔")), detail: m[4], cursor: m[1] != ""})
	}
	return out
}

// titleLine is the last line holding title, or ending it when the title
// wraps onto a second line; -1 if none.
func titleLine(lines []string, title string) int {
	start := -1
	for i, l := range lines {
		switch {
		case strings.Contains(l, title):
			start = i
		case i > 0 && !strings.Contains(lines[i-1], title) && strings.Contains(normalize(lines[i-1]+" "+l), title):
			start = i
		}
	}
	return start
}

// normalize collapses white space, as wrapping changes it.
func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }

// menuHint is in the last line of /model, which a narrow window wraps.
const menuHint = "to use this session only"

// switchPrompt finds the "Switch model?" confirmation, which Claude Code
// shows instead of the menu: the title below any menu, and its "Yes,
// switch to …" row. The words alone, say in the conversation, are not it.
func switchPrompt(plain string) (int, bool) {
	lines := strings.Split(plain, "\n")
	at := titleLine(lines, "Switch model?")
	if at < 0 {
		return 0, false
	}
	if below := normalize(strings.Join(lines[at+1:], " ")); strings.Contains(below, "Select model") || strings.Contains(below, menuHint) {
		return 0, false // the menu is still up
	}
	for _, r := range rowsAfter(plain, "Switch model?") {
		if strings.HasPrefix(r.label, "Yes, switch to") {
			return r.num, true
		}
	}
	return 0, false
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

// versionRE matches a version as /model prints it after a model's name.
var versionRE = regexp.MustCompile(`^\d+(\.\d+)*$`)

// modelRow finds the row of /model for the alias called name. The menu
// has one row per alias ("Sonnet"), or names its rows by version ("Sonnet
// 5.5") and lists older models after the current ones (claude-code-notes
// §9): the alias is the newest of them. Rows the version does not tell
// apart are not chosen between.
func modelRow(rows []row, name string) (int, error) {
	var found []int
	for i, r := range rows {
		if strings.EqualFold(strings.Fields(r.label)[0], name) {
			found = append(found, i)
		}
	}
	if len(found) == 0 {
		return -1, fmt.Errorf("model %s is not in Claude Code's /model menu", name)
	}
	if len(found) == 1 {
		return found[0], nil
	}
	ambiguous := fmt.Errorf("the /model menu lists %d models named %s that cannot be told apart", len(found), name)
	// The alias's own row.
	own := -1
	for _, i := range found {
		if len(strings.Fields(rows[i].label)) == 1 {
			if own >= 0 {
				return -1, ambiguous
			}
			own = i
		}
	}
	if own >= 0 {
		return own, nil
	}
	best, tie := -1, false
	var newest []int
	for _, i := range found {
		f := strings.Fields(rows[i].label)
		if len(f) != 2 || !versionRE.MatchString(f[1]) {
			return -1, ambiguous
		}
		var v []int
		for _, part := range strings.Split(f[1], ".") {
			n, _ := strconv.Atoi(part)
			v = append(v, n)
		}
		switch c := slices.Compare(v, newest); {
		case c > 0:
			best, newest, tie = i, v, false
		case c == 0:
			tie = true
		}
	}
	if tie {
		return -1, ambiguous
	}
	return best, nil
}

// SetModel picks the model in /model with the arrow keys, sets the effort
// with ←/→ and presses s, which applies to this session only; the number
// keys and Enter would change the default.
func (a *Adapter) SetModel(t agent.Term, model, effort string) error {
	// The window is fixed when the session starts; /model lists the aliases.
	model = strings.TrimSuffix(model, largeSuffix)
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
		return strings.Contains(p, "Select model") && strings.Contains(normalize(p), menuHint)
	})
	if err != nil {
		bail(t)
		return err
	}
	rows := rowsAfter(plain, "Select model")
	target, err := modelRow(rows, info.DisplayName)
	cur := slices.IndexFunc(rows, func(r row) bool { return r.cursor })
	if err == nil && cur < 0 {
		err = errors.New("cannot find the cursor in Claude Code's /model menu")
	}
	if err != nil {
		bail(t)
		return err
	}
	label := rows[target].label
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
	plain, err = waitFor(t, "the cursor on "+label, func(_ agent.Screen, p string) bool {
		for i, r := range rowsAfter(p, "Select model") {
			if r.cursor {
				return i == target && r.label == label
			}
		}
		return false
	})
	if err != nil {
		bail(t)
		return err
	}
	if effort != "" {
		// ←/→ step one level along low … max and stop at the ends rather
		// than wrapping (2.1.300); the level may be below the one showing.
		// The order is the same on both sides, so which key to press is
		// the direction to the wanted one.
		want := slices.Index(efforts, effort)
		for i := 0; shownEffort(plain) != effort; i++ {
			cur := slices.Index(efforts, shownEffort(plain))
			if i >= len(efforts) || want < 0 || cur < 0 {
				bail(t)
				return fmt.Errorf("effort %s is not offered for %s", effort, info.DisplayName)
			}
			key := agent.KeyRight
			if want < cur {
				key = agent.KeyLeft
			}
			before := shownEffort(plain)
			if err := t.Write([]byte(key)); err != nil {
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
	// Only its "Yes" row is pressed: a number in the menu itself would
	// change the default model.
	_, err = waitFor(t, "the model to switch", func(scr agent.Screen, p string) bool {
		_, confirm := switchPrompt(p)
		return scr.ComposerReady || (confirm && !scr.ComposerReady)
	})
	if err != nil {
		bail(t)
		return err
	}
	if scr, p, err := screenNow(t); err == nil && !scr.ComposerReady {
		num, ok := switchPrompt(p)
		if !ok {
			bail(t)
			return errors.New("unexpected screen after choosing the model")
		}
		if err := t.Write([]byte(strconv.Itoa(num))); err != nil {
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
// with low effort"; an effort that did not change is not named. A narrow
// window wraps the note onto lines indented like its text, which are
// joined to it.
func confirmed(plain, name, effort string) bool {
	lines := strings.Split(plain, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "Set model to") {
			l := lines[i]
			for _, next := range lines[i+1:] {
				if !strings.HasPrefix(next, "     ") || strings.TrimSpace(next) == "" {
					break
				}
				l += " " + next
			}
			l = normalize(l)
			// The effort is named only when it changed.
			return strings.Contains(l, name) && strings.Contains(l, "for this session only") &&
				(effort == "" || !strings.Contains(l, " effort") || strings.Contains(l, "with "+effort+" effort"))
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

// looseness orders the modes by what they let through without asking.
var looseness = map[string]int{"plan": 0, "default": 1, "acceptEdits": 2, "auto": 3, "bypassPermissions": 4}

// crosses reports whether cycling from one mode to another passes a mode
// looser than limit on the way. Modes known not to be on offer are
// skipped, as Shift+Tab skips them; others may be on offer.
func crosses(from, to string, limit int, offer map[string]bool) bool {
	for i := index(from) + 1; ; i++ {
		m := cycle[i%len(cycle)]
		if m == to {
			return false
		}
		if on, known := offer[m]; known && !on {
			continue
		}
		if looseness[m] > limit {
			return true
		}
	}
}

// noAutoRE matches the models known to have no auto mode: Haiku up to 4.5
// (claude-code-notes §5).
var noAutoRE = regexp.MustCompile(`^claude-(?:3(?:-5)?-haiku|haiku-[34])\b`)

// offered is which of the modes not always on offer, bypass and auto, the
// session offers, as far as known: bypass from the session's launch-time
// capability; auto from the model (Haiku 4.5 has none, Haiku 5.5 has, and
// which of them the alias is depends on Claude Code's version, so for
// "haiku" it is not known) or from having shown auto on the screen with
// this model. The status
// area's "auto mode unavailable for this model" is not used: Claude Code
// shows it for a while after Shift+Tab skipped auto, and it stays after a
// switch to a model that has auto. A mode missing from the map is not
// known.
func offered(facts agent.ModeFacts) map[string]bool {
	offer := map[string]bool{"default": true, "acceptEdits": true, "plan": true, "bypassPermissions": facts.FullAccess}
	model := strings.ToLower(facts.Model)
	switch {
	case noAutoRE.MatchString(model):
		offer["auto"] = false
	case slices.Contains(facts.Seen, "auto"):
		offer["auto"] = true
	case strings.HasPrefix(model, "claude-") || model == "sonnet" || model == "opus" || model == "fable":
		offer["auto"] = true
	}
	if slices.Contains(facts.Seen, "bypassPermissions") {
		offer["bypassPermissions"] = true
	}
	return offer
}

// SetMode switches the permission mode. Plan, while idle, is /plan, which
// goes there directly. Otherwise it presses Shift+Tab, reading the mode
// line after each press, until it shows the mode; a mode not on offer is
// never reached, and after a full turn the original mode is restored and
// an error returned.
//
// While a turn runs, no mode on the way, nor on the way back should the
// mode not be on offer, may be looser than the one asked for: a tool call
// could get through in that moment. Idle, Shift+Tab has no other way
// round (there is no reverse key, and /plan only enters Plan mode), so a
// way through looser modes is taken, but only with no background work on
// the screen, and with the presses sent at once (burst), shortening the
// moment (architecture §16).
func (a *Adapter) SetMode(t agent.Term, mode pb.ApprovalMode, running bool, facts agent.ModeFacts) error {
	want, ok := permissionModes[mode]
	if !ok {
		return fmt.Errorf("unknown approval mode %v", mode)
	}
	scr, err := readyComposer(t)
	if err != nil {
		return err
	}
	styled, err := t.Capture()
	if err != nil {
		return err
	}
	offer := offered(facts)
	if on, known := offer[want]; known && !on {
		if mode == pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS {
			return fmt.Errorf("%w: this session was not started with bypass permissions allowed", agent.ErrUnsupported)
		}
		return fmt.Errorf("%w: %s is not on offer in this session", agent.ErrUnsupported, want)
	}
	start := scr.Mode
	if start == "" {
		return errors.New("cannot read the permission mode on the screen")
	}
	if start == want {
		return nil
	}
	limit := looseness[want]
	_, wantKnown := offer[want]
	risky := crosses(start, want, limit, offer) || !wantKnown && crosses(want, start, limit, offer)
	if running && risky {
		return agent.ErrBusy
	}
	if want == "plan" && !running {
		return enterPlan(t)
	}
	if risky {
		if bg := BackgroundWork(styled); bg != "" {
			return fmt.Errorf("%w (%s), and switching to %s passes a looser mode on the way; try again when it has finished", agent.ErrBackground, bg, want)
		}
		return burst(t, start, want, offer)
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

func enterPlan(t agent.Term) error {
	if err := agent.Slash(t, "/plan"); err != nil {
		return err
	}
	_, err := waitFor(t, "Plan mode", func(s agent.Screen, _ string) bool { return s.ComposerReady && s.Mode == "plan" })
	return err
}

// burst goes from one mode to another with all the Shift+Tabs written at
// once, without reading the screen in between, then checks the mode.
// The presses are counted from what offer knows. When whether auto is on
// offer is not known, it is counted as not: pressing too few can land on
// it, which then shows it is on offer, and the missing presses follow at
// once (architecture §16); pressing too many cannot happen. A target that
// turns out not to be on offer is left for the mode it started in.
func burst(t agent.Term, start, want string, offer map[string]bool) error {
	count := func(from string) int {
		n := 0
		for i := index(from) + 1; ; i++ {
			m := cycle[i%len(cycle)]
			if offer[m] || m == want {
				n++
			}
			if m == want {
				return n
			}
		}
	}
	cur := start
	for try := 0; try < 3; try++ {
		n := count(cur)
		slog.Info("switching the permission mode in one write", "from", cur, "to", want, "presses", n)
		if err := t.Write([]byte(strings.Repeat(agent.KeyBTab, n))); err != nil {
			return err
		}
		m, err := settledMode(t)
		if err != nil {
			return err
		}
		if m == want {
			return nil
		}
		if _, known := offer[m]; !known || !offer[m] {
			// Counted as not on offer, but it is: go on at once.
			offer[m] = true
			cur = m
			continue
		}
		// The target was skipped: it is not on offer.
		if start == "plan" {
			_ = enterPlan(t)
		} else if m != start {
			offer[want] = false
			_ = burst(t, m, start, offer)
		}
		return fmt.Errorf("%w: %s is not on offer in this session", agent.ErrUnsupported, want)
	}
	return errors.New("the permission mode did not settle")
}

// settledMode waits for the mode line to show the same mode three times
// in a row, as a burst of key presses is taken in.
func settledMode(t agent.Term) (string, error) {
	last, same := "", 0
	deadline := time.Now().Add(agent.MenuWait)
	for {
		if scr, _, err := screenNow(t); err == nil && scr.ComposerReady && scr.Mode != "" {
			if scr.Mode == last {
				same++
			} else {
				last, same = scr.Mode, 1
			}
			if same >= 3 {
				return last, nil
			}
		}
		if time.Now().After(deadline) {
			return "", errors.New("timed out waiting for the permission mode")
		}
		time.Sleep(100 * time.Millisecond)
	}
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

// tickRE reads a multi-select row: its number and whether it is ticked.
var tickRE = regexp.MustCompile(`^\s*(?:❯\s*)?(\d+)\.\s+\[(.)\]`)

// toggles are the option numbers to press so that exactly want is ticked.
func toggles(plain string, n int, want []int32) []int32 {
	ticked := map[int32]bool{}
	for _, l := range strings.Split(plain, "\n") {
		if m := tickRE.FindStringSubmatch(l); m != nil {
			k, _ := strconv.Atoi(m[1])
			ticked[int32(k)] = m[2] != " "
		}
	}
	wanted := map[int32]bool{}
	for _, o := range want {
		wanted[o] = true
	}
	var out []int32
	for k := int32(1); k <= int32(n); k++ {
		if ticked[k] != wanted[k] {
			out = append(out, k)
		}
	}
	return out
}

// reviewing reports whether the page that submits the answers is showing:
// its question with the "Submit answers" row under it. The words alone,
// say in the conversation above a question, are not it.
func reviewing(plain string) bool {
	for _, r := range rowsAfter(plain, "Ready to submit your answers?") {
		if r.num == 1 {
			return strings.HasPrefix(r.label, "Submit answers")
		}
	}
	return false
}

// Answer presses the option numbers of the question showing: one picks
// and moves on; for a multi-select question they tick options and → goes
// on. After the last question Claude Code shows a review page, where 1
// submits (claude-code-notes §7).
//
// The review page left by answers given in the terminal names the
// questions too, but there the numbers submit and cancel: nothing is
// pressed on it.
func (a *Adapter) Answer(t agent.Term, q *pb.QuestionAsked, index *int32, options []int32) error {
	styled, err := t.Capture()
	if err != nil {
		return err
	}
	if reviewing(plainText(styled)) {
		return fmt.Errorf("%w: the answers given there are waiting to be submitted", agent.ErrNotAnswering)
	}
	i := 0
	if index != nil {
		i = int(*index)
	} else if len(q.Questions) > 1 {
		// Without an index, answer the question that is showing, found in
		// the screen with wrapping undone; never guess.
		screen := normalize(plainText(styled))
		i = -1
		for j, item := range q.Questions {
			if strings.Contains(screen, normalize(item.Text)) {
				if i >= 0 {
					return errors.New("cannot tell which question is showing; answer with its index")
				}
				i = j
			}
		}
		if i < 0 {
			return errors.New("cannot find the question on the screen")
		}
	}
	if i < 0 || i >= len(q.Questions) {
		return fmt.Errorf("question index %d out of range", i)
	}
	item := q.Questions[i]
	if !item.MultiSelect && len(options) != 1 {
		return fmt.Errorf("the question takes one option, got %d", len(options))
	}
	keys := options
	if item.MultiSelect {
		// A number toggles its option: press only those whose tick differs
		// from the answer, as the local user may have ticked some.
		keys = toggles(plainText(styled), len(item.Options), options)
	}
	for _, o := range keys {
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
		if reviewing(plain) {
			return t.Write([]byte("1"))
		}
		if !scr.QuestionOpen {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}
