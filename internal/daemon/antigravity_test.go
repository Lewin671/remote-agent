package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/antigravity"
)

func agyHarness(t *testing.T) *harness {
	h := newHarness(t, Config{ApprovalTimeout: 200 * time.Millisecond})
	a := antigravity.New("/bin/true", t.TempDir())
	h.d.agents[a.Kind()] = a
	h.s.ag = a
	h.s.parser = a.NewParser()
	h.s.meta.Agent = a.Kind()
	h.s.meta.CodexSessionID = "conv"
	h.s.meta.NoFullAccess = true
	h.d.sessions[h.s.meta.ID] = h.s
	t.Cleanup(func() { h.s.retractApprovals() })
	return h
}
func agyScreen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "antigravity", "testdata", "screen", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func nativeCard(t *testing.T, h *harness) *pb.Event {
	t.Helper()
	_ = h.s.emit(&pb.Event{Kind: &pb.Event_ToolStarted{ToolStarted: &pb.ToolStarted{CallId: "conv/step_2", ItemType: "run_command", Input: `{"CommandLine":"echo hello_perm_test"}`}}})
	step := int64(2)
	_, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "PreToolUse", SessionID: "conv", StepIndex: &step, ToolName: "run_command", ToolUseID: "conv/step_2", ToolInput: []byte(`{"CommandLine":"echo hello_perm_test"}`)})
	if err != nil {
		t.Fatal(err)
	}
	h.term.setScreen(agyScreen(t, "permission-prompt.txt"))
	h.s.checkScreen()
	h.s.checkScreen()
	for _, e := range h.events() {
		if e.GetApprovalRequested() != nil {
			return e
		}
	}
	t.Fatal("native card missing")
	return nil
}
func TestAgyNativeApprovalIsMatchedAndDeliveredSynchronously(t *testing.T) {
	h := agyHarness(t)
	card := nativeCard(t, h)
	h.term.mu.Lock()
	h.term.onWrite = func(f *fakeTerm, b string) {
		if b == "1" {
			f.screen = agyScreen(t, "idle-empty-box.txt")
		}
	}
	h.term.mu.Unlock()
	if err := h.s.resolve(card.GetApprovalRequested().ApprovalId, true, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(h.term.written(), ""), "1") {
		t.Fatal("RPC returned without delivery")
	}
	h.s.checkScreen()
	h.s.checkScreen()
	count := 0
	for _, e := range h.events() {
		if e.GetApprovalRequested() != nil {
			count++
		}
	}
	if count != 1 {
		t.Fatal("duplicate card after native answer")
	}
}
func TestAgyNativeApprovalDoesNotAnswerChangedScreen(t *testing.T) {
	h := agyHarness(t)
	card := nativeCard(t, h)
	h.term.setScreen(strings.ReplaceAll(agyScreen(t, "permission-prompt.txt"), "echo hello_perm_test", "echo changed"))
	if err := h.s.resolve(card.GetApprovalRequested().ApprovalId, true, ""); !errors.Is(err, agent.ErrNotAnswering) {
		t.Fatalf("changed prompt: %v", err)
	}
	if len(h.term.written()) != 0 {
		t.Fatal("sent keys to changed native prompt")
	}
}
func TestAgyNativeApprovalTimeoutDenies(t *testing.T) {
	h := agyHarness(t)
	h.term.onWrite = func(f *fakeTerm, b string) {
		if b == "4" {
			f.screen = agyScreen(t, "idle-empty-box.txt")
		}
	}
	_ = nativeCard(t, h)
	eventually(t, "native timeout denial", func() bool {
		for _, e := range h.events() {
			if e.GetApprovalResolved().GetDecision() == pb.ApprovalDecision_APPROVAL_DECISION_TIMEOUT {
				return true
			}
		}
		return false
	})
	if !strings.Contains(strings.Join(h.term.written(), ""), "4") {
		t.Fatal("timeout did not deny in native terminal")
	}
}
func TestAgyNativeToolResultRetractsCard(t *testing.T) {
	h := agyHarness(t)
	card := nativeCard(t, h)
	req := card.GetApprovalRequested()
	h.s.retractFor(&pb.ToolCall{CallId: "conv/step_2", ItemType: req.ToolName, InputDigest: req.InputDigest})
	eventually(t, "terminal resolution", func() bool {
		for _, e := range h.events() {
			if e.GetApprovalResolved().GetDecision() == pb.ApprovalDecision_APPROVAL_DECISION_TERMINAL {
				return true
			}
		}
		return false
	})
	if len(h.term.written()) > 0 {
		t.Fatal("terminal-completed call received keys")
	}
}
func TestAgyLockedModeAndUnsupportedActions(t *testing.T) {
	h := agyHarness(t)
	if err := h.s.setModel("gemini-3.8-flash", "low"); !errors.Is(err, agent.ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := h.d.fork(h.s.meta.ID, originLocal); !errors.Is(err, agent.ErrUnsupported) {
		t.Fatal(err)
	}
	h.s.context = &pb.TurnContext{ApprovalPolicy: "always-proceed", PermissionMode: "default"}
	h.s.meta.NoFullAccess = false
	h.s.statusLineAt = time.Now()
	info := h.s.info()
	if !info.ModeLocked || info.FullAccessUnavailable || info.Mode != pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS {
		t.Fatal(info)
	}
	h.s.meta.NoFullAccess = true
	h.s.context = &pb.TurnContext{ApprovalPolicy: "request-review", PermissionMode: "default"}
	info = h.s.info()
	if info.ModeLocked || !info.FullAccessUnavailable {
		t.Fatal(info)
	}
}
func TestAgyStopHookClosesRunningTurn(t *testing.T) {
	h := agyHarness(t)
	if err := h.s.emit(&pb.Event{TurnId: "step_0", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "Stop", SessionID: "conv", TerminationReason: "ERROR"}); err != nil {
		t.Fatal(err)
	}
	if h.s.isRunning() {
		t.Fatal("Stop hook left turn running")
	}
	ends := 0
	for _, e := range h.events() {
		if e.GetTurnEnded() != nil {
			ends++
			if e.TurnId != "step_0" {
				t.Fatal("Stop has no active turn ID")
			}
		}
	}
	_, _ = h.s.hook(context.Background(), &agent.Hook{HookEventName: "Stop", SessionID: "conv", TerminationReason: "ERROR"})
	count := 0
	for _, e := range h.events() {
		if e.GetTurnEnded() != nil {
			count++
		}
	}
	if ends != 1 || count != 1 {
		t.Fatal("Stop duplicate or missing")
	}
}
