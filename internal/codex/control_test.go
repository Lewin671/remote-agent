package codex

import (
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// Codex 0.163.0 closes a current permission selection without emitting
// another "Permission selection requested" conversation message.
type currentPermissionTerm struct {
	plan, menu bool
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
	return "\x1b[1m›\x1b[0m \x1b[2mAsk Codex to do anything\x1b[0m\n" + footer + "\n", nil
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
