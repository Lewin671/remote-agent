package codex

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// Codex 0.163.0 closes a current permission selection without emitting
// another "Permission selection requested" conversation message.
type currentPermissionTerm struct {
	plan, menu bool
	draft      string
	writes     []string
}

func (t *currentPermissionTerm) Capture() (string, error) {
	if t.menu {
		return "  Update Model Permissions\n\x1b[1;7m› 1. Ask for approval (current)  \x1b[0;7mRead and edit workspace files\x1b[0m\n  2. Approve for me\n  3. Full Access\n  4. Read Only\n  enter select · esc back\n", nil
	}
	footer := "  GPT-6.1-Sol low · /tmp/work"
	if t.plan {
		footer += "   Plan mode (⇧tab to cycle)    ⚠ 1 warning"
	}
	prompt := "\x1b[2mAsk Codex to do anything\x1b[0m"
	if t.draft != "" {
		prompt = t.draft
	}
	return "\x1b[1m›\x1b[0m " + prompt + "\n" + footer + "\n", nil
}
func (t *currentPermissionTerm) Write(b []byte) error {
	key := string(b)
	t.writes = append(t.writes, key)
	switch key {
	case agent.KeyBTab:
		t.plan = !t.plan
	case agent.KeyEnter:
		t.menu = true
	case agent.KeyEsc, "1":
		t.menu = false
	}
	return nil
}
func TestSetModePreservesCurrentPermission(t *testing.T) {
	for _, plan := range []bool{false, true} {
		name := "already ask"
		if plan {
			name = "leave plan with underlying ask"
		}
		t.Run(name, func(t *testing.T) {
			term := &currentPermissionTerm{plan: plan}
			if err := new(Adapter).SetMode(term, pb.ApprovalMode_APPROVAL_MODE_ASK, false, agent.ModeFacts{}); err != nil {
				t.Fatal(err)
			}
			if term.plan || term.menu {
				t.Fatalf("not back at default composer: %+v", term)
			}
			if got := strings.Join(term.writes, "|"); strings.Contains(got, "|1") || term.writes[len(term.writes)-1] != agent.KeyEsc {
				t.Fatalf("reselected an already current permission: %q", got)
			}
		})
	}
}

// Codex 0.163.0 hides its busy indicator during MCP startup but still
// ignores Shift+Tab (chatwidget/interaction.rs and turn_runtime.rs).
// /plan is accepted in that state (slash_dispatch.rs).
type startingMCPTerm struct {
	currentPermissionTerm
	command string
	applied chan struct{}
	render  chan struct{}
	failure error
}

func (t *startingMCPTerm) Write(b []byte) error {
	if t.failure != nil {
		return t.failure
	}
	t.writes = append(t.writes, string(b))
	switch string(b) {
	case agent.KeyBTab:
		// Native TUI deliberately ignores this while MCP starts.
	case agent.KeyEnter:
		if t.command == "/plan" {
			close(t.applied)
			t.command = ""
		}
	default:
		t.command += string(b)
	}
	return nil
}

func (t *startingMCPTerm) Capture() (string, error) {
	select {
	case <-t.render:
		t.plan = true
	default:
	}
	styled, err := t.currentPermissionTerm.Capture()
	// The busy TUI suppresses the cycle shortcut, retaining the warning badge.
	return strings.ReplaceAll(styled, "Plan mode (⇧tab to cycle)", "Plan mode"), err
}

func TestSetPlanDuringMCPStartup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		term := &startingMCPTerm{applied: make(chan struct{}), render: make(chan struct{})}
		done := make(chan error, 1)
		go func() {
			done <- new(Adapter).SetMode(term, pb.ApprovalMode_APPROVAL_MODE_PLAN, false, agent.ModeFacts{})
		}()
		select {
		case <-term.applied:
		case err := <-done:
			t.Fatalf("Plan command was not accepted during MCP startup: %v", err)
		}
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("returned before the footer confirmed Plan mode: %v", err)
		default:
		}
		close(term.render)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if !term.plan {
			t.Fatal("Plan mode was not applied")
		}
	})
}

func TestSetPlanAlreadyEnabled(t *testing.T) {
	for _, running := range []bool{false, true} {
		term := &currentPermissionTerm{plan: true}
		if err := new(Adapter).SetMode(term, pb.ApprovalMode_APPROVAL_MODE_PLAN, running, agent.ModeFacts{}); err != nil {
			t.Fatal(err)
		}
		if len(term.writes) != 0 {
			t.Fatalf("already enabled Plan mode wrote keys: %q", term.writes)
		}
	}
}

func TestSetModeAllowsPermissionsDuringTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		term := &currentPermissionTerm{}
		if err := new(Adapter).SetMode(term, pb.ApprovalMode_APPROVAL_MODE_ASK, true, agent.ModeFacts{}); err != nil {
			t.Fatal(err)
		}
		if term.plan || term.menu {
			t.Fatalf("permission selection did not return to the composer: %+v", term)
		}
	})
}

func TestSetPlanPropagatesWriteError(t *testing.T) {
	want := errors.New("write failed")
	term := &startingMCPTerm{failure: want}
	if err := new(Adapter).SetMode(term, pb.ApprovalMode_APPROVAL_MODE_PLAN, false, agent.ModeFacts{}); !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestSetModeRefusesPlanTransitionDuringTurn(t *testing.T) {
	for _, plan := range []bool{false, true} {
		t.Run(fmt.Sprintf("from-plan-%t", plan), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				term := &currentPermissionTerm{plan: plan}
				want := pb.ApprovalMode_APPROVAL_MODE_PLAN
				if plan {
					want = pb.ApprovalMode_APPROVAL_MODE_ASK
				}
				if err := new(Adapter).SetMode(term, want, true, agent.ModeFacts{}); !errors.Is(err, agent.ErrBusy) {
					t.Fatalf("got %v, want ErrBusy", err)
				}
				if len(term.writes) != 0 || term.plan != plan {
					t.Fatalf("changed a running terminal: %+v", term)
				}
			})
		})
	}
}

func TestSetModePreservesDraftForPlanTransition(t *testing.T) {
	for _, plan := range []bool{false, true} {
		for _, running := range []bool{false, true} {
			t.Run(fmt.Sprintf("from-plan-%t-running-%t", plan, running), func(t *testing.T) {
				term := &currentPermissionTerm{plan: plan, draft: "local unsent draft"}
				want := pb.ApprovalMode_APPROVAL_MODE_PLAN
				if plan {
					want = pb.ApprovalMode_APPROVAL_MODE_ASK
				}
				if err := new(Adapter).SetMode(term, want, running, agent.ModeFacts{}); !errors.Is(err, agent.ErrNotReady) {
					t.Fatalf("got %v, want ErrNotReady", err)
				}
				if len(term.writes) != 0 || term.draft != "local unsent draft" || term.plan != plan {
					t.Fatalf("changed terminal with a draft: %+v", term)
				}
			})
		}
	}
}
