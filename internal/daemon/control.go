package daemon

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/codex"
	"google.golang.org/protobuf/proto"
)

// Operations that drive Codex's menus. Each takes the control lock, holds
// local keys, checks the screen before every step, and waits for the
// screen it expects instead of sleeping. An unexpected screen is left with
// Esc and reported; keys are never pressed blind.

var (
	errBusy     = errors.New("Codex is busy; try again when the turn has ended")
	errNotReady = errors.New("the input box is not ready: a popup is open or the terminal holds an unsent draft")
	errNoPlan   = errors.New("Codex is not asking about a plan")
)

const (
	keyUp    = "\x1b[A"
	keyDown  = "\x1b[B"
	keyEsc   = "\x1b"
	keyEnter = "\r"
	keyBTab  = "\x1b[Z"
)

// menuWait bounds how long a step waits for its screen.
const menuWait = 4 * time.Second

// drive runs op with the terminal held for it. The status check is paused
// meanwhile so that the menus it opens are not reported as popups.
func (s *session) drive(op func(c control) error) error {
	s.mu.Lock()
	exited := s.meta.Exited
	s.mu.Unlock()
	if exited {
		return errExited
	}
	s.ctlMu.Lock()
	defer s.ctlMu.Unlock()
	c, err := s.term.Control()
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Hold(holdFor); err != nil {
		return err
	}
	defer c.Release()
	// A hold is bounded so that a stuck daemon cannot swallow local keys;
	// renew it while the operation runs.
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(holdFor / 2)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = c.Hold(holdFor)
			}
		}
	}()
	s.mu.Lock()
	s.driving = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.driving = false
		s.mu.Unlock()
	}()
	time.Sleep(settle)
	return op(c)
}

// screen captures and analyzes the terminal.
func (s *session) screenNow() (codex.Screen, string, error) {
	styled, err := s.term.Capture()
	if err != nil {
		return codex.Screen{}, "", err
	}
	return codex.AnalyzeScreen(styled), codex.StripANSI(styled), nil
}

// waitFor polls the screen until ok accepts it.
func (s *session) waitFor(what string, ok func(codex.Screen, string) bool) (string, error) {
	deadline := time.Now().Add(menuWait)
	for {
		scr, plain, err := s.screenNow()
		if err == nil && ok(scr, plain) {
			return plain, nil
		}
		if time.Now().After(deadline) {
			return plain, fmt.Errorf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// bail leaves whatever menu is open.
func (s *session) bail(c control) {
	for i := 0; i < 3; i++ {
		if scr, _, err := s.screenNow(); err == nil && scr.Popup == "" {
			return
		}
		_ = c.Write([]byte(keyEsc))
		time.Sleep(200 * time.Millisecond)
	}
}

// readyComposer checks that the input box is showing and empty, so that
// typed commands neither mix with a draft nor land in a popup.
func (s *session) readyComposer() error {
	scr, _, err := s.screenNow()
	if err != nil {
		return err
	}
	if !scr.ComposerReady || !scr.DraftEmpty {
		return errNotReady
	}
	return nil
}

func (s *session) isRunning() bool {
	s.readRollout()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// slash types a slash command and presses Enter.
func slash(c control, cmd string) error {
	if err := c.Write([]byte(cmd)); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond) // let the command popup catch up
	return c.Write([]byte(keyEnter))
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
func moveTo(c control, entries []menuEntry, target int) error {
	cur := -1
	for i, e := range entries {
		if e.cursor {
			cur = i
		}
	}
	if cur < 0 {
		return errors.New("cannot find the menu cursor")
	}
	key := keyDown
	steps := target - cur
	if steps < 0 {
		key, steps = keyUp, -steps
	}
	for i := 0; i < steps; i++ {
		if err := c.Write([]byte(key)); err != nil {
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

// setModel picks model and effort in /model for this session only.
func (s *session) setModel(model, effort string, displayName string) error {
	label, ok := effortLabels[effort]
	if !ok {
		return fmt.Errorf("unknown effort %q", effort)
	}
	if s.isRunning() {
		return errBusy
	}
	err := s.drive(func(c control) error {
		if err := s.readyComposer(); err != nil {
			return err
		}
		if err := slash(c, "/model"); err != nil {
			return err
		}
		plain, err := s.waitFor("the model menu", func(_ codex.Screen, p string) bool { return strings.Contains(p, "Select Model and Effort") })
		if err != nil {
			s.bail(c)
			return err
		}
		num := 0
		for _, e := range menuEntries(plain, "Select Model and Effort") {
			if strings.EqualFold(e.label, displayName) {
				num = e.num
			}
		}
		if num < 1 || num > 9 {
			s.bail(c)
			return fmt.Errorf("model %s is not in Codex's /model menu", displayName)
		}
		if err := c.Write([]byte(strconv.Itoa(num))); err != nil {
			return err
		}
		title := "Select Reasoning Level"
		plain, err = s.waitFor("the effort menu", func(_ codex.Screen, p string) bool { return strings.Contains(p, title) })
		if err != nil {
			s.bail(c)
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
				s.bail(c)
				return fmt.Errorf("effort %s is not offered for %s", effort, displayName)
			}
			if err := moveTo(c, entries, more); err != nil {
				s.bail(c)
				return err
			}
			if err := c.Write([]byte(keyEnter)); err != nil {
				return err
			}
			plain, err = s.waitFor("the reasoning submenu", func(_ codex.Screen, p string) bool {
				return indexOf(menuEntries(p, "Max"), label) >= 0 || strings.Contains(p, "\n  1. Max") || strings.Contains(p, "› 1. Max")
			})
			if err != nil {
				s.bail(c)
				return err
			}
			entries = lastMenu(plain)
			target = indexOf(entries, label)
		}
		if target < 0 {
			s.bail(c)
			return fmt.Errorf("effort %s is not offered for %s", effort, displayName)
		}
		if err := moveTo(c, entries, target); err != nil {
			s.bail(c)
			return err
		}
		// s applies to this session only; Enter would change the default.
		if err := c.Write([]byte("s")); err != nil {
			return err
		}
		word := strings.ToLower(label)
		if effort == "xhigh" {
			word = "xhigh"
		}
		if _, err := s.waitFor("the new model in the footer", func(scr codex.Screen, _ string) bool {
			f := strings.ToLower(scr.Footer)
			return scr.ComposerReady && strings.Contains(f, strings.ToLower(displayName)) &&
				(strings.Contains(f, word) || strings.Contains(f, strings.ToLower(label)) || strings.Contains(f, effort))
		}); err != nil {
			s.bail(c)
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.emit(&pb.Event{Kind: &pb.Event_SettingsChanged{SettingsChanged: &pb.SettingsChanged{Model: model, Effort: effort}}})
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

// setApprovalMode switches the permission level or Plan mode. Choosing
// Plan turns Plan mode on and leaves permissions alone; any other mode
// turns Plan mode off first.
func (s *session) setApprovalMode(mode pb.ApprovalMode) error {
	level, isLevel := permissionLevels[mode]
	if !isLevel && mode != pb.ApprovalMode_APPROVAL_MODE_PLAN {
		return fmt.Errorf("unknown approval mode %v", mode)
	}
	err := s.drive(func(c control) error {
		if err := s.readyComposer(); err != nil {
			return err
		}
		scr, _, err := s.screenNow()
		if err != nil {
			return err
		}
		wantPlan := mode == pb.ApprovalMode_APPROVAL_MODE_PLAN
		if scr.PlanMode != wantPlan {
			if err := c.Write([]byte(keyBTab)); err != nil {
				return err
			}
			if _, err := s.waitFor("Plan mode to switch", func(scr codex.Screen, _ string) bool { return scr.PlanMode == wantPlan }); err != nil {
				return err
			}
		}
		if wantPlan {
			return nil
		}
		if err := slash(c, "/permissions"); err != nil {
			return err
		}
		if _, err := s.waitFor("the permissions menu", func(_ codex.Screen, p string) bool { return strings.Contains(p, "Update Model Permissions") }); err != nil {
			s.bail(c)
			return err
		}
		if err := c.Write([]byte(strconv.Itoa(level))); err != nil {
			return err
		}
		if mode == pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS {
			if _, err := s.waitFor("the full access confirmation", func(_ codex.Screen, p string) bool { return strings.Contains(p, "Enable full access?") }); err != nil {
				s.bail(c)
				return err
			}
			if err := c.Write([]byte("1")); err != nil {
				return err
			}
		}
		if _, err := s.waitFor("Codex to confirm the permissions", func(scr codex.Screen, p string) bool {
			return scr.Popup == "" && strings.Contains(p, "Permission selection requested")
		}); err != nil {
			s.bail(c)
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.emit(&pb.Event{Kind: &pb.Event_SettingsChanged{SettingsChanged: &pb.SettingsChanged{Mode: mode}}})
	return nil
}

// compact runs /compact while no turn is running.
func (s *session) compact() error {
	if s.isRunning() {
		return errBusy
	}
	err := s.drive(func(c control) error {
		if err := s.readyComposer(); err != nil {
			return err
		}
		if err := slash(c, "/compact"); err != nil {
			return err
		}
		// Compacting runs as a turn; seeing the input box leave its idle
		// placeholder or a turn start is enough to know it was accepted.
		_, err := s.waitFor("compaction to start", func(scr codex.Screen, _ string) bool { return !scr.DraftEmpty || s.isRunningNoRead() })
		return err
	})
	// The rollout's ContextCompaction item records it.
	return err
}

func (s *session) isRunningNoRead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// answerPlan answers "Implement this plan?".
func (s *session) answerPlan(choice pb.PlanChoice) error {
	if choice < pb.PlanChoice_PLAN_CHOICE_IMPLEMENT || choice > pb.PlanChoice_PLAN_CHOICE_KEEP_PLANNING {
		return fmt.Errorf("unknown plan choice %v", choice)
	}
	return s.drive(func(c control) error {
		scr, _, err := s.screenNow()
		if err != nil {
			return err
		}
		if !scr.PlanOpen {
			return errNoPlan
		}
		return c.Write([]byte(strconv.Itoa(int(choice))))
	})
}

// applySettings updates the turn context the session reports after a
// change made through remote-agent; Codex records it with the next turn.
func applySettings(ctx *pb.TurnContext, ch *pb.SettingsChanged) *pb.TurnContext {
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
