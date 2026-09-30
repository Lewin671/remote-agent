package codex

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"google.golang.org/protobuf/proto"
)

// Operations that drive Codex's menus. The daemon holds the terminal for
// them; each checks the screen before every step and waits for the screen
// it expects instead of sleeping. An unexpected screen is left with Esc
// and reported; keys are never pressed blind.

// screenNow captures and analyzes the terminal.
func screenNow(t agent.Term) (Screen, string, error) {
	styled, err := t.Capture()
	if err != nil {
		return Screen{}, "", err
	}
	return AnalyzeScreen(styled), StripANSI(styled), nil
}

// waitFor polls the screen until ok accepts it.
func waitFor(t agent.Term, what string, ok func(Screen, string) bool) (string, error) {
	styled, err := agent.WaitFor(t, what, func(styled string) bool { return ok(AnalyzeScreen(styled), StripANSI(styled)) })
	return StripANSI(styled), err
}

// bail leaves whatever menu is open.
func bail(t agent.Term) {
	for i := 0; i < 3; i++ {
		if scr, _, err := screenNow(t); err == nil && scr.Popup == "" {
			return
		}
		_ = t.Write([]byte(agent.KeyEsc))
		time.Sleep(200 * time.Millisecond)
	}
}

// readyComposer checks that the input box is showing and empty, so that
// typed commands neither mix with a draft nor land in a popup.
func readyComposer(t agent.Term) error {
	scr, _, err := screenNow(t)
	if err != nil {
		return err
	}
	if !scr.ComposerReady || !scr.DraftEmpty {
		return agent.ErrNotReady
	}
	return nil
}

// menuLine matches a numbered menu entry and captures its number and label.
var menuLine = regexp.MustCompile(`^\s*(?:›\s*)?(\d+)\.\s+(.+?)(?:\s{2,}.*)?$`)

type menuEntry struct {
	num    int
	label  string
	cursor bool
	row    int
}

// menuEntries reads the numbered entries after the line containing title.
func menuEntries(plain, title string) []menuEntry {
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
	var out []menuEntry
	for i, l := range lines[start+1:] {
		m := menuLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		label := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(m[2]), "(current)"), "(default)"))
		out = append(out, menuEntry{num: n, label: strings.TrimSpace(label), cursor: strings.Contains(l, "›"), row: i})
	}
	return out
}

// moveTo presses Up or Down until the cursor is on target.
func moveTo(t agent.Term, entries []menuEntry, target int) error {
	cur := -1
	for i, e := range entries {
		if e.cursor {
			cur = i
		}
	}
	if cur < 0 {
		return errors.New("cannot find the menu cursor")
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
	return nil
}

// effortLabels are the reasoning levels as the /model menu names them.
var effortLabels = map[string]string{
	"low": "Low", "medium": "Medium", "high": "High", "xhigh": "Extra high", "max": "Max", "ultra": "Ultra",
}

// SetModel picks model and effort in /model for this session only.
func (a *Adapter) SetModel(t agent.Term, model, effort string) error {
	label, ok := effortLabels[effort]
	if !ok {
		return fmt.Errorf("unknown effort %q", effort)
	}
	displayName, err := a.displayName(model)
	if err != nil {
		return err
	}
	if err := readyComposer(t); err != nil {
		return err
	}
	if err := agent.Slash(t, "/model"); err != nil {
		return err
	}
	plain, err := waitFor(t, "the model menu", func(_ Screen, p string) bool { return strings.Contains(p, "Select Model and Effort") })
	if err != nil {
		bail(t)
		return err
	}
	num := 0
	for _, e := range menuEntries(plain, "Select Model and Effort") {
		if strings.EqualFold(e.label, displayName) {
			num = e.num
		}
	}
	if num < 1 || num > 9 {
		bail(t)
		return fmt.Errorf("model %s is not in Codex's /model menu", displayName)
	}
	if err := t.Write([]byte(strconv.Itoa(num))); err != nil {
		return err
	}
	title := "Select Reasoning Level"
	plain, err = waitFor(t, "the effort menu", func(_ Screen, p string) bool { return strings.Contains(p, title) })
	if err != nil {
		bail(t)
		return err
	}
	entries := menuEntries(plain, title)
	target := indexOf(entries, label)
	if target < 0 && (effort == "max" || effort == "ultra") {
		// Max and Ultra sit in the "More reasoning…" submenu.
		more := -1
		for i, e := range entries {
			if strings.HasPrefix(e.label, "More reasoning") {
				more = i
			}
		}
		if more < 0 {
			bail(t)
			return fmt.Errorf("effort %s is not offered for %s", effort, displayName)
		}
		if err := moveTo(t, entries, more); err != nil {
			bail(t)
			return err
		}
		if err := t.Write([]byte(agent.KeyEnter)); err != nil {
			return err
		}
		plain, err = waitFor(t, "the reasoning submenu", func(_ Screen, p string) bool {
			return indexOf(menuEntries(p, "Max"), label) >= 0 || strings.Contains(p, "\n  1. Max") || strings.Contains(p, "› 1. Max")
		})
		if err != nil {
			bail(t)
			return err
		}
		entries = lastMenu(plain)
		target = indexOf(entries, label)
	}
	if target < 0 {
		bail(t)
		return fmt.Errorf("effort %s is not offered for %s", effort, displayName)
	}
	if err := moveTo(t, entries, target); err != nil {
		bail(t)
		return err
	}
	// s applies to this session only; Enter would change the default.
	if err := t.Write([]byte("s")); err != nil {
		return err
	}
	word := strings.ToLower(label)
	if effort == "xhigh" {
		word = "xhigh"
	}
	if _, err := waitFor(t, "the new model in the footer", func(scr Screen, _ string) bool {
		f := strings.ToLower(scr.Footer)
		return scr.ComposerReady && strings.Contains(f, strings.ToLower(displayName)) &&
			(strings.Contains(f, word) || strings.Contains(f, strings.ToLower(label)) || strings.Contains(f, effort))
	}); err != nil {
		bail(t)
		return err
	}
	return nil
}

// lastMenu reads the numbered entries of the menu at the bottom.
func lastMenu(plain string) []menuEntry {
	lines := strings.Split(plain, "\n")
	var out []menuEntry
	for i := len(lines) - 1; i >= 0; i-- {
		m := menuLine.FindStringSubmatch(lines[i])
		if m == nil {
			if len(out) > 0 && strings.TrimSpace(lines[i]) != "" && !strings.HasPrefix(strings.TrimSpace(lines[i]), "enter") {
				break
			}
			continue
		}
		n, _ := strconv.Atoi(m[1])
		out = append([]menuEntry{{num: n, label: strings.TrimSpace(m[2]), cursor: strings.Contains(lines[i], "›"), row: i}}, out...)
		if n == 1 {
			break
		}
	}
	return out
}

func indexOf(entries []menuEntry, label string) int {
	for i, e := range entries {
		if strings.EqualFold(e.label, label) || strings.HasPrefix(strings.ToLower(e.label), strings.ToLower(label)+" ") {
			return i
		}
	}
	return -1
}

// permissionLevels are the /permissions menu entries.
var permissionLevels = map[pb.ApprovalMode]int{
	pb.ApprovalMode_APPROVAL_MODE_ASK:         1,
	pb.ApprovalMode_APPROVAL_MODE_AUTO:        2,
	pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS: 3,
}

// SetMode switches the permission level or Plan mode. Choosing Plan turns
// Plan mode on and leaves permissions alone; any other mode turns Plan
// mode off first. Both work while a turn runs and apply from the next.
func (a *Adapter) SetMode(t agent.Term, mode pb.ApprovalMode, running bool, _ agent.ModeFacts) error {
	level, isLevel := permissionLevels[mode]
	if !isLevel && mode != pb.ApprovalMode_APPROVAL_MODE_PLAN {
		return fmt.Errorf("unknown approval mode %v", mode)
	}
	if err := readyComposer(t); err != nil {
		return err
	}
	scr, _, err := screenNow(t)
	if err != nil {
		return err
	}
	wantPlan := mode == pb.ApprovalMode_APPROVAL_MODE_PLAN
	if scr.PlanMode != wantPlan {
		if err := t.Write([]byte(agent.KeyBTab)); err != nil {
			return err
		}
		if _, err := waitFor(t, "Plan mode to switch", func(scr Screen, _ string) bool { return scr.PlanMode == wantPlan }); err != nil {
			return err
		}
	}
	if wantPlan {
		return nil
	}
	if err := agent.Slash(t, "/permissions"); err != nil {
		return err
	}
	if _, err := waitFor(t, "the permissions menu", func(_ Screen, p string) bool { return strings.Contains(p, "Update Model Permissions") }); err != nil {
		bail(t)
		return err
	}
	if err := t.Write([]byte(strconv.Itoa(level))); err != nil {
		return err
	}
	if mode == pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS {
		if _, err := waitFor(t, "the full access confirmation", func(_ Screen, p string) bool { return strings.Contains(p, "Enable full access?") }); err != nil {
			bail(t)
			return err
		}
		if err := t.Write([]byte("1")); err != nil {
			return err
		}
	}
	if _, err := waitFor(t, "Codex to confirm the permissions", func(scr Screen, p string) bool {
		return scr.Popup == "" && strings.Contains(p, "Permission selection requested")
	}); err != nil {
		bail(t)
		return err
	}
	return nil
}

// Compact runs /compact.
func (a *Adapter) Compact(t agent.Term, started func() bool) error {
	if err := readyComposer(t); err != nil {
		return err
	}
	if err := agent.Slash(t, "/compact"); err != nil {
		return err
	}
	// Compacting runs as a turn; seeing the input box leave its idle
	// placeholder or a turn start is enough to know it was accepted.
	_, err := waitFor(t, "compaction to start", func(scr Screen, _ string) bool { return !scr.DraftEmpty || started() })
	// The rollout's ContextCompaction item records it.
	return err
}

// AnswerPlan answers "Implement this plan?"; the choices are its entries.
func (a *Adapter) AnswerPlan(t agent.Term, choice pb.PlanChoice) error {
	if choice < pb.PlanChoice_PLAN_CHOICE_IMPLEMENT || choice > pb.PlanChoice_PLAN_CHOICE_KEEP_PLANNING {
		return fmt.Errorf("unknown plan choice %v", choice)
	}
	scr, _, err := screenNow(t)
	if err != nil {
		return err
	}
	if !scr.PlanOpen {
		return agent.ErrNoPlan
	}
	return t.Write([]byte(strconv.Itoa(int(choice))))
}

// Answer enters the option number of the question showing; the caller has
// checked that it is showing.
func (a *Adapter) Answer(t agent.Term, q *pb.QuestionAsked, index *int32, options []int32) error {
	if len(options) != 1 {
		return fmt.Errorf("%w: Codex takes one option per question", agent.ErrUnsupported)
	}
	return t.Write([]byte(strconv.Itoa(int(options[0]))))
}

// ApplySettings updates the turn context after a change made through
// remote-agent; Codex records it with the next turn.
func (a *Adapter) ApplySettings(ctx *pb.TurnContext, ch *pb.SettingsChanged) *pb.TurnContext {
	if ctx == nil {
		ctx = &pb.TurnContext{}
	}
	ctx = proto.CloneOf(ctx)
	if ch.Model != "" {
		ctx.Model, ctx.Effort = ch.Model, ch.Effort
	}
	switch ch.Mode {
	case pb.ApprovalMode_APPROVAL_MODE_ASK:
		ctx.ApprovalPolicy, ctx.ApprovalsReviewer, ctx.Sandbox, ctx.CollaborationMode = "on-request", "user", "workspace-write", "default"
	case pb.ApprovalMode_APPROVAL_MODE_AUTO:
		ctx.ApprovalPolicy, ctx.ApprovalsReviewer, ctx.Sandbox, ctx.CollaborationMode = "on-request", "auto_review", "workspace-write", "default"
	case pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS:
		ctx.ApprovalPolicy, ctx.ApprovalsReviewer, ctx.Sandbox, ctx.CollaborationMode = "never", "user", "danger-full-access", "default"
	case pb.ApprovalMode_APPROVAL_MODE_PLAN:
		ctx.CollaborationMode = "plan"
	}
	return ctx
}
