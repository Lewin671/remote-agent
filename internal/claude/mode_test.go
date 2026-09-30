package claude

import (
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// Current evidence that auto is not on offer wins over having seen it
// with another model (review verify3 P2).
func TestAutoSeenWithAnotherModel(t *testing.T) {
	if o := offered(agent.ModeFacts{Seen: []string{"auto"}, Model: "claude-haiku-4-5-20251001"}); o["auto"] {
		t.Fatal("haiku now, auto seen before")
	}
	a := New("claude", "")
	f := &fakeClaude{noAuto: true}
	f.mode = index("plan")
	err := a.SetMode(f, pb.ApprovalMode_APPROVAL_MODE_ASK, false, agent.ModeFacts{FullAccess: true, Model: "claude-haiku-4-5-20251001", Seen: []string{"auto"}})
	if err != nil || cycle[f.mode] != "default" || strings.Join(f.writes, "|") != strings.Repeat(agent.KeyBTab, 2) {
		t.Fatalf("plan → ask: %v %q %s", err, f.writes, cycle[f.mode])
	}
}
