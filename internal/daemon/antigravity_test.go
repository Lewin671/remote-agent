package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestAgyImagesRecognizedInPhoneWidthTerminal(t *testing.T) {
	t.Parallel()
	for _, rows := range [][]string{
		{"▸ 📎 1 media attached (file, 103 B, image/png)", "▸ 📎 2 media attached  (ctrl+o to expand)"},
		{"▸ 📎 1 media attached (file, 1", "▸ 📎 2 media attached  (ctrl+o"},
	} {
		t.Run(rows[0], func(t *testing.T) {
			h := agyHarness(t)
			h.s.status = pb.SessionStatus_SESSION_STATUS_IDLE
			h.term.setScreen(agyScreen(t, "image-cleared-1.5.0.txt"))
			imgs := imagesFor(t, h.dir, "one.png", "two.png")
			n := 0
			h.term.onWrite = func(f *fakeTerm, b string) {
				if strings.HasSuffix(b, "\x1b[201~") {
					f.screen = strings.Replace(agyScreen(t, "image-terminal.txt"), "▸ 📎 1 media attached (file, 663 B, image/jpeg)  (ctrl+o to expand)", rows[n], 1)
					n++
				}
			}
			if _, err := h.s.enqueue("describe both", "phone-images", imgs, originLocal); err != nil {
				t.Fatal(err)
			}
			result, err := h.s.inject(h.s.queue.Queue[0])
			if err != nil || result != written {
				t.Fatalf("inject: %v %v", result, err)
			}
			writes := h.term.written()
			if n != 2 || len(writes) != 3 || !strings.HasSuffix(writes[2], "describe both\x1b[201~\r") {
				t.Fatalf("images/text order: %q", writes)
			}
		})
	}
}

func TestAgyImageCleanupVerifiesMediaDisappears(t *testing.T) {
	for _, disappears := range []bool{true, false} {
		t.Run(fmt.Sprint(disappears), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
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
		})
	}
}

func TestAgyExistingMediaAndMenusBlockInjection(t *testing.T) {
	t.Parallel()
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

func TestAgyPlanIsActionableOnlyAfterForegroundTurnEnds(t *testing.T) {
	h := agyHarness(t)
	text := []byte("# Plan\nDo one thing.")
	path := filepath.Join(h.dir, "implementation_plan.md")
	if err := os.WriteFile(path, text, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(text)
	h.s.planFacts = &agent.PlanFacts{Path: path, Root: h.dir, Revision: hex.EncodeToString(sum[:]), Decisions: map[string]string{"implementation_plan.md": "new"}}
	h.term.setScreen(agyScreen(t, "plan-terminal-before.txt"))
	h.s.running = true
	h.s.checkScreen()
	h.s.checkScreen()
	if h.s.info().PlanOpen {
		t.Fatal("running turn exposed answer")
	}
	h.s.running = false
	h.s.checkScreen()
	h.s.checkScreen()
	if !h.s.info().PlanOpen {
		t.Fatal("idle bound plan missing actions")
	}
	if err := os.WriteFile(path, []byte("revised"), 0600); err != nil {
		t.Fatal(err)
	}
	h.s.checkScreen()
	h.s.checkScreen()
	if info := h.s.info(); info.PlanOpen || info.Status == pb.SessionStatus_SESSION_STATUS_BLOCKED {
		t.Fatalf("revision mismatch left an action or fabricated a popup: %v", info)
	}
	// A real review panel for the invalidated revision still requires the
	// terminal, without inventing a popup over an ordinary idle composer.
	h.term.setScreen(agyScreen(t, "plan-terminal-after.txt"))
	h.s.checkScreen()
	h.s.checkScreen()
	if info := h.s.info(); info.PlanOpen || info.Status != pb.SessionStatus_SESSION_STATUS_BLOCKED {
		t.Fatalf("native mismatch panel not terminal-only: %v", info)
	}
}

func TestAgyModeDisplayRetainsConfirmedModeWhenStale(t *testing.T) {
	for _, policy := range []string{"request-review", "always-proceed"} {
		h := agyHarness(t)
		h.s.context = &pb.TurnContext{ApprovalPolicy: policy, PermissionMode: "default"}
		h.s.statusLineAt = time.Now().Add(-time.Hour)
		expected := h.s.ag.Mode(h.s.context)
		if info := h.s.info(); info.Mode != expected || info.PermissionMode != "default" || info.ModeLocked != (policy == "always-proceed") {
			t.Fatal(info)
		}
		h.s.statusLineAt = time.Time{}
		if info := h.s.info(); info.Mode != 0 || strings.Contains(info.PermissionMode, "toolPermission") || strings.Contains(info.PermissionMode, "cycle_mode") {
			t.Fatal(info)
		}
		h.s.statusLineAt = time.Now()
		h.s.context.PermissionMode = "unknown"
		if info := h.s.info(); info.Mode != 0 || info.PermissionMode != "未知审批模式" {
			t.Fatal(info)
		}
	}
}

func TestAgyQuotaOrderSurvivesMergingAndUnknownBuckets(t *testing.T) {
	h := agyHarness(t)
	names := []string{"unknown-z", "3p-weekly", "gemini-argon-wtu-weekly", "gemini-weekly", "3p-5h", "gemini-5h", "unknown-a"}
	var windows []*pb.QuotaWindow
	for _, name := range names {
		windows = append(windows, &pb.QuotaWindow{Name: name})
	}
	q := &pb.AgentQuota{Agent: pb.Agent_AGENT_ANTIGRAVITY, Account: "example-account", Windows: windows, State: pb.QuotaState_QUOTA_STATE_OK}
	h.d.recordQuotaWithPolicy("example-session", q, time.Now(), false)
	h.d.recordQuotaWithPolicy("example-session", &pb.AgentQuota{Agent: q.Agent, Account: q.Account, Windows: []*pb.QuotaWindow{{Name: "gemini-5h"}}}, time.Now().Add(time.Second), false)
	h.d.mu.Lock()
	entries := h.d.quotaEntriesLocked()
	h.d.mu.Unlock()
	var got []string
	for _, entry := range entries {
		if entry.Agent == q.Agent {
			for _, window := range entry.Windows {
				got = append(got, window.Name)
			}
		}
	}
	want := "gemini-5h,gemini-weekly,gemini-argon-wtu-weekly,3p-5h,3p-weekly,unknown-a,unknown-z"
	if strings.Join(got, ",") != want {
		t.Fatalf("quota order: %v", got)
	}
}

func TestAgyBackgroundTaskDoesNotDelayAbortedForegroundTurn(t *testing.T) {
	h := agyHarness(t)
	if err := h.s.emit(&pb.Event{TurnId: "foreground", Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}); err != nil {
		t.Fatal(err)
	}
	h.term.setScreen(agyScreen(t, "interrupt-background-terminal.txt"))
	h.s.restoredPromptSince = time.Now().Add(-2 * time.Second)
	h.s.settleRestoredPrompt(agyScreen(t, "interrupt-background-terminal.txt"))
	if h.s.isRunning() {
		t.Fatal("background task kept interrupted foreground turn running")
	}
	found := false
	for _, e := range h.events() {
		if e.GetTurnEnded().GetOutcome() == pb.TurnOutcome_TURN_OUTCOME_ABORTED {
			found = true
		}
	}
	if !found {
		t.Fatal("no ABORTED turn end")
	}
}

func TestAgyImagesWaitForIdleSoCleanupCannotInterrupt(t *testing.T) {
	h := agyHarness(t)
	h.s.status = pb.SessionStatus_SESSION_STATUS_RUNNING
	h.s.running = true
	h.term.setScreen(agyScreen(t, "idle-empty-box.txt"))
	if _, err := h.s.enqueue("describe", "waiting-image", imagesFor(t, h.dir, "one.png"), originLocal); err != nil {
		t.Fatal(err)
	}
	result, err := h.s.inject(h.s.queue.Queue[0])
	if err != nil || result != notWritten || len(h.term.written()) != 0 {
		t.Fatalf("image pasted during foreground turn: %v %v", result, err)
	}
}

func TestAgyPendingPlanDoesNotInventPopupOverOrdinaryInput(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"ignored", "draft", "wrapped footer", "changed then ignored"} {
		t.Run(scenario, func(t *testing.T) {
			h := agyHarness(t)
			text := []byte("# Pending plan")
			path := filepath.Join(h.dir, "implementation_plan.md")
			if err := os.WriteFile(path, text, 0600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(text)
			h.s.planFacts = &agent.PlanFacts{Path: path, Root: h.dir, Revision: hex.EncodeToString(sum[:]), Decisions: map[string]string{"implementation_plan.md": "new"}}
			screen := agyScreen(t, "idle-empty-box.txt")
			switch scenario {
			case "draft":
				screen = agyScreen(t, "draft-oneline.txt")
			case "wrapped footer":
				screen = strings.Replace(agyScreen(t, "plan-terminal-before.txt"), "1 artifact · /artifact to review", "1 artifact ·\n/artifact to review", 1)
			case "changed then ignored":
				if err := os.WriteFile(path, []byte("revised"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			h.term.setScreen(screen)
			h.s.checkScreen()
			h.s.checkScreen()
			if info := h.s.info(); info.PlanOpen || info.Status == pb.SessionStatus_SESSION_STATUS_BLOCKED || info.AttentionSince != nil {
				t.Fatalf("passive plan fabricated an interactive popup: %v", info)
			}
			if scenario != "draft" {
				if _, err := h.s.enqueue("hello", "followup", nil, originLocal); err != nil {
					t.Fatal(err)
				}
				result, err := h.s.inject(h.s.queue.Queue[0])
				if err != nil || result != written {
					t.Fatalf("ordinary composer refused followup: %v %v", result, err)
				}
			}
		})
	}
}

func TestAgyImageCleanupRechecksVisibleBusyTurn(t *testing.T) {
	h := agyHarness(t)
	screen := strings.Replace(agyScreen(t, "image-terminal.txt"), "  ▸ 📎 1 media attached", "⣟ Generating...\n  ▸ 📎 1 media attached", 1)
	h.term.setScreen(screen)
	if !h.s.ag.Analyze(screen).Busy {
		t.Fatal("fixture lacks a visible busy turn")
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
	if h.s.clearPasted(c, l, in, []int32{1}, "", false, false) || len(h.term.written()) != 0 {
		t.Fatal("cleanup interrupted an unparsed visible turn")
	}
}
