package antigravity

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

// Paste wraps text in bracketed paste followed by Enter.
func (a *Adapter) Paste(text string) []byte {
	return agent.PasteBytes(text)
}

// ClearInput writes Ctrl-E followed by Ctrl-U to empty the current input box line.
func (a *Adapter) ClearInput() []byte {
	return []byte("\x05\x15")
}

// Interrupt sends Escape to cancel the active tool or generation.
func (a *Adapter) Interrupt() []byte {
	return []byte(agent.KeyEsc)
}

// Answer validates the visible question and its entire numbered option list
// before sending keys. Local selections are respected when toggling multi-select.
func (a *Adapter) Answer(t agent.Term, q *pb.QuestionAsked, index *int32, options []int32) (operationErr error) {
	t, nativeWrites := agent.ObserveNative(t)
	defer func() { operationErr = nativeWrites.Failure(operationErr) }()

	styled, err := agent.WaitFor(t, "native question", func(s string) bool { return a.Analyze(s).QuestionOpen })
	if err != nil {
		return agent.ErrNotAnswering
	}
	if !a.Analyze(styled).QuestionOpen || q == nil {
		return agent.ErrNotAnswering
	}
	plain := StripANSI(styled)
	loc := questionHeaderRE.FindStringSubmatchIndex(plain)
	if loc == nil {
		return agent.ErrNotAnswering
	}
	area := plain[loc[1]:]
	i := -1
	for j, item := range q.Questions {
		if strings.HasPrefix(normalize(area), normalize(item.Text)) {
			if i >= 0 {
				return agent.ErrNotAnswering
			}
			i = j
		}
	}
	if i < 0 || index != nil && int(*index) != i {
		return agent.ErrNotAnswering
	}
	item := q.Questions[i]
	if !item.MultiSelect && len(options) != 1 {
		return errors.New("question requires one option")
	}
	rows := questionRows(area)
	for j, opt := range item.Options {
		r, ok := rows[j+1]
		clean := func(s string) string { return normalize(strings.TrimPrefix(s, "(Recommended) ")) }
		if !ok || clean(r.label) != clean(opt.Label) {
			return agent.ErrNotAnswering
		}
	}
	seen := map[int32]bool{}
	for _, o := range options {
		if o < 1 || int(o) > len(item.Options) || o > 9 || seen[o] {
			return errors.New("invalid or unsupported option number")
		}
		seen[o] = true
		if strings.Contains(strings.ToLower(rows[int(o)].label), "write-in") {
			return agent.ErrUnsupported
		}
	}
	if !item.MultiSelect {
		return t.Write([]byte(strconv.Itoa(int(options[0]))))
	}
	for j := range item.Options {
		n := int32(j + 1)
		r := rows[int(n)]
		if !r.hasTick {
			return agent.ErrNotAnswering
		}
		if r.checked != seen[n] {
			if err := t.Write([]byte(strconv.Itoa(int(n)))); err != nil {
				return err
			}
			if err := agent.Wait(t, 100*time.Millisecond); err != nil {
				return err
			}
			captured, err := t.Capture()
			if err != nil {
				return err
			}
			current := questionRows(StripANSI(captured))
			if !a.Analyze(captured).QuestionOpen || current[int(n)].checked != seen[n] {
				return agent.ErrNotAnswering
			}
		}
	}
	return t.Write([]byte(agent.KeyEnter))
}

type questionRow struct {
	label            string
	hasTick, checked bool
}

var numberedRowRE = regexp.MustCompile(`^\s*(?:>\s*)?([0-9]+)\.\s+(.*)$`)

func questionRows(plain string) map[int]questionRow {
	out := map[int]questionRow{}
	last := 0
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "Navigate") {
			break
		}
		m := numberedRowRE.FindStringSubmatch(line)
		if m == nil {
			if last > 0 && strings.TrimSpace(line) != "" {
				r := out[last]
				r.label += " " + strings.TrimSpace(line)
				out[last] = r
			}
			continue
		}
		n, _ := strconv.Atoi(m[1])
		label := m[2]
		r := questionRow{}
		if strings.HasPrefix(label, "[ ] ") || strings.HasPrefix(label, "[x] ") || strings.HasPrefix(label, "[X] ") {
			r.hasTick = true
			r.checked = label[1] != ' '
			label = label[4:]
		}
		r.label = label
		out[n] = r
		last = n
	}
	return out
}

// SetModel never invokes /model: that command changes the global user settings.
func (a *Adapter) SetModel(t agent.Term, _, _ string) (operationErr error) {
	t, nativeWrites := agent.ObserveNative(t)
	defer func() { operationErr = nativeWrites.Failure(operationErr) }()
	return agent.ErrUnsupported
}

// SetMode confirms every Shift+Tab against statusLine cycle_mode.
func (a *Adapter) SetMode(t agent.Term, mode pb.ApprovalMode, running bool, facts agent.ModeFacts) (operationErr error) {
	t, nativeWrites := agent.ObserveNative(t)
	defer func() { operationErr = nativeWrites.Failure(operationErr) }()

	if mode == pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS || facts.FullAccess {
		return agent.ErrUnsupported
	}
	target := ""
	switch mode {
	case pb.ApprovalMode_APPROVAL_MODE_ASK:
		target = "default"
	case pb.ApprovalMode_APPROVAL_MODE_AUTO:
		target = "auto"
	case pb.ApprovalMode_APPROVAL_MODE_PLAN:
		target = "plan"
	default:
		return agent.ErrUnsupported
	}

	styled, err := agent.WaitFor(t, "the native composer after redraw", func(s string) bool { screen := a.Analyze(s); return screen.ComposerReady && screen.Popup == "" })
	if err != nil {
		return agent.ErrNotReady
	}
	scr := a.Analyze(styled)
	if running || scr.Busy {
		return agent.ErrBusy
	}
	if facts.Observation == nil {
		return errors.New("no status line mode observation")
	}
	observation := func() string {
		o := facts.Observation()
		if o.At.IsZero() || time.Since(o.At) > 10*time.Second || !o.IdleKnown || o.Background {
			return ""
		}
		return o.Mode
	}

	if facts.Observation().ToolPermission != "request-review" {
		return agent.ErrUnsupported
	}
	start := observation()
	if start == "" {
		return errors.New("no status line mode observation")
	}
	if start == target {
		return nil
	}
	for i := 0; i < 4; i++ {
		before := observation()
		if err := t.Write([]byte(agent.KeyBTab)); err != nil {
			return err
		}
		if _, err := agent.WaitFor(t, "status line cycle_mode to change", func(string) bool { return observation() != "" && observation() != before }); err != nil {
			return err
		}
		now := observation()
		if now == target {
			return nil
		}
		if now == start {
			break
		}
	}
	return fmt.Errorf("%w: requested mode is unavailable", agent.ErrUnsupported)
}

// Compact triggers context compaction via /compact command.
func (a *Adapter) Compact(t agent.Term, _ func() bool) (operationErr error) {
	t, nativeWrites := agent.ObserveNative(t)
	defer func() { operationErr = nativeWrites.Failure(operationErr) }()

	styled, err := t.Capture()
	if err != nil {
		return err
	}
	scr := a.Analyze(styled)
	if !scr.ComposerReady || !scr.DraftEmpty || scr.Popup != "" {
		return agent.ErrNotReady
	}
	if scr.Busy {
		return agent.ErrBusy
	}
	return agent.Slash(t, "/compact")
}

// Mode returns the high-level ApprovalMode corresponding to TurnContext.
func (a *Adapter) Mode(ctx *pb.TurnContext) pb.ApprovalMode {
	if ctx == nil {
		return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
	}
	if ctx.ApprovalPolicy == "always-proceed" && ctx.PermissionMode == "default" {
		return pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS
	}
	if ctx.ApprovalPolicy != "request-review" {
		return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
	}
	switch ctx.PermissionMode {
	case "default":
		return pb.ApprovalMode_APPROVAL_MODE_ASK
	case "auto":
		return pb.ApprovalMode_APPROVAL_MODE_AUTO
	case "plan":
		return pb.ApprovalMode_APPROVAL_MODE_PLAN
	}
	return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
}

// ApplySettings updates TurnContext when settings are changed through remote-agent.
func (a *Adapter) ApplySettings(ctx *pb.TurnContext, ch *pb.SettingsChanged) *pb.TurnContext {
	return applySettings(ctx, ch)
}

// ForwardPermission always returns false for Antigravity because PreToolUse is report-only.
func (a *Adapter) ForwardPermission(_ *agent.Hook, _ *pb.TurnContext) bool {
	return false
}

func (a *Adapter) RestoredPrompt(styled, prompt string) bool {
	text, ok := a.ComposerText(styled, "")
	if !ok {
		return false
	}
	return prompt != "" && !a.Analyze(styled).Busy && normalize(text) == normalize(prompt)
}

// A viewport redraw changes terminal_width in agy's status-line input and
// refreshes its command even while the idle payload would otherwise be cached.
func (a *Adapter) RefreshStatus(t agent.Term) error {
	view, ok := t.(agent.ViewRefresher)
	if !ok {
		return agent.ErrUnsupported
	}
	return view.RefreshView()
}
