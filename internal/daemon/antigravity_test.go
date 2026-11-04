package daemon

import (
	"context"
	"errors"
	"fmt"
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

func TestAgyImagesRecognizedBeforeSubmittingText(t *testing.T) {
	h := agyHarness(t)
	h.s.status = pb.SessionStatus_SESSION_STATUS_IDLE
	h.term.setScreen(agyScreen(t, "image-cleared-1.5.0.txt"))
	imgs := imagesFor(t, h.dir, "one.png", "two.png")
	n := 0
	h.term.onWrite = func(f *fakeTerm, b string) {
		if strings.HasSuffix(b, "\x1b[201~") {
			n++
			f.screen = strings.Replace(agyScreen(t, "image-terminal.txt"), "1 media attached", fmt.Sprintf("%d media attached", n), 1)
		}
	}
	if _, err := h.s.enqueue("describe both", "images", imgs, originLocal); err != nil {
		t.Fatal(err)
	}
	result, err := h.s.inject(h.s.queue.Queue[0])
	if err != nil || result != written {
		t.Fatalf("inject: %v %v", result, err)
	}
	writes := h.term.written()
	if len(writes) != 3 || !strings.HasSuffix(writes[2], "describe both\x1b[201~\r") {
		t.Fatalf("images/text order: %q", writes)
	}
}

func TestAgyImageCleanupVerifiesMediaDisappears(t *testing.T) {
	for _, disappears := range []bool{true, false} {
		t.Run(fmt.Sprint(disappears), func(t *testing.T) {
			h := agyHarness(t)
			h.term.setScreen(agyScreen(t, "image-terminal.txt"))
			h.term.onWrite = func(f *fakeTerm, b string) {
				if b == "\x05\x15\x1b" && disappears {
					f.screen = agyScreen(t, "image-cleared-1.5.0.txt")
				}
			}
			c, err := h.term.Control()
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			l, err := holdLease(c, injectBase)
			if err != nil {
				t.Fatal(err)
			}
			defer l.end()
			in := input{Images: imagesFor(t, h.dir, "one.png")}
			if got := h.s.clearPasted(c, l, in, []int32{1}, "", false, false); got != disappears {
				t.Fatalf("cleanup=%v want %v", got, disappears)
			}
		})
	}
}

func TestAgyExistingMediaAndMenusBlockInjection(t *testing.T) {
	for _, name := range []string{"image-terminal.txt", "mcp-terminal.txt"} {
		t.Run(name, func(t *testing.T) {
			h := agyHarness(t)
			h.s.status = pb.SessionStatus_SESSION_STATUS_IDLE
			h.term.setScreen(agyScreen(t, name))
			if _, err := h.s.enqueue("do not send", "blocked", nil, originLocal); err != nil {
				t.Fatal(err)
			}
			result, err := h.s.inject(h.s.queue.Queue[0])
			if err != nil || result != notWritten || len(h.term.written()) != 0 {
				t.Fatalf("foreign input changed: %v %v", result, err)
			}
		})
	}
}
