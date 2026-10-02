package daemon

import (
	"connectrpc.com/connect"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// Codex 0.159.0's approval prompt (codex-notes §4).
const codexApproval = "\x1b[1m\x1b[38;2;128;128;128m•\x1b[0m \x1b[1mRunning\x1b[0m touch ~/x\n" +
	"  \x1b[1mWould you like to run the following command?\x1b[0m\n" +
	"\x1b[1;7m› 1. Yes, proceed (y)\n" +
	"\x1b[0m  2. Yes, and don't ask again for commands that start with `touch` (\x1b[1mp\x1b[0m)\n" +
	"  3. No, and tell Codex what to do differently (\x1b[1mesc\x1b[0m)\n"

func resolvedOf(h *harness, id string) *pb.ApprovalResolved {
	for _, e := range h.events() {
		if r := e.GetApprovalResolved(); r != nil && r.ApprovalId == id {
			return r
		}
	}
	return nil
}

func TestCodexApprovalOutlivesTurn(t *testing.T) {
	h := newHarness(t, Config{ApprovalTimeout: time.Minute})
	h.writeRollout(turnContextLine("user"), taskStarted)
	h.s.bindRollout("codex-1", h.dir+"/rollout.jsonl")
	go h.s.hook(context.Background(), permissionPayload(h))
	var id string
	eventually(t, "approval request", func() bool {
		h.s.mu.Lock()
		defer h.s.mu.Unlock()
		for k := range h.s.approvals {
			id = k
		}
		return id != ""
	})
	h.writeRollout(taskComplete)
	h.s.readRollout()
	if r := resolvedOf(h, id); r != nil {
		t.Fatalf("approval settled at the end of the turn: %v", r)
	}
	if err := h.s.resolve(id, true, ""); err != nil {
		t.Fatalf("resolve after the turn: %v", err)
	}
}

// A command prefix, basename or old conversation text cannot establish
// which native prompt receives a permanent grant. Reject old clients too,
// keep the original approval pending and never type a key or grant once.
func TestPermanentApprovalFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, input, screen string }{
		{"same command prefix", `{"command":"touch /tmp/shared-prefix-safe-file"}`, strings.ReplaceAll(codexApproval, "touch ~/x", "touch /tmp/shared-prefix-dangerous-file")},
		{"same basename", `{"file_path":"/safe/config.json"}`, strings.ReplaceAll(ccPrompt, "touch /w/x", "Edit /dangerous/config.json")},
		{"historical text", `{"command":"touch ~/x"}`, "Earlier: touch ~/x\n" + strings.ReplaceAll(codexApproval, "touch ~/x", "rm -rf ~/important")},
		{"unidentified tool", `{}`, codexApproval},
		{"matching prompt", `{"command":"touch ~/x"}`, codexApproval},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Config{ApprovalTimeout: time.Minute})
			h.writeRollout(turnContextLine("user"))
			p := permissionPayload(h)
			p.ToolInput = json.RawMessage(tc.input)
			done := make(chan []byte, 1)
			go func() { out, _ := h.s.hook(context.Background(), p); done <- out }()
			var id string
			eventually(t, "approval", func() bool {
				h.s.mu.Lock()
				defer h.s.mu.Unlock()
				for k := range h.s.approvals {
					id = k
				}
				return id != ""
			})
			h.term.setScreen(tc.screen)
			err := rpcErr(h.s.resolveBy(id, true, true, "", originLocal))
			if connect.CodeOf(err) != connect.CodeFailedPrecondition || !errors.Is(err, agent.ErrUnsupported) {
				t.Fatalf("permanent approval error = %v", err)
			}
			if h.d.capabilities().ApprovalAlways {
				t.Fatal("unsafe permanent approval advertised")
			}
			if len(h.term.written()) != 0 || resolvedOf(h, id) != nil {
				t.Fatal("permanent request changed the terminal or resolved approval")
			}
			select {
			case <-done:
				t.Fatal("rejected permanent request granted or dismissed original hook")
			default:
			}
			// Single-use approval remains available for the same pending request.
			if err := h.s.resolve(id, true, "once"); err != nil {
				t.Fatal(err)
			}
			if out := <-done; !strings.Contains(string(out), `"allow"`) {
				t.Fatalf("single-use result = %s", out)
			}
		})
	}
}
