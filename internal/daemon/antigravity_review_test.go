package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

func TestAgyRepeatedPendingCallsDoNotCreateCards(t *testing.T) {
	h := agyHarness(t)
	for _, id := range []string{"conv/step_2", "conv/step_3"} {
		_ = h.s.emit(&pb.Event{Kind: &pb.Event_ToolStarted{ToolStarted: &pb.ToolStarted{CallId: id, ItemType: "run_command", Input: `{"CommandLine":"echo hello_perm_test"}`}}})
	}
	step := int64(2)
	_, err := h.s.hook(context.Background(), &agent.Hook{SessionID: "conv", HookEventName: "PreToolUse", StepIndex: &step, ToolUseID: "conv/step_2", ToolName: "run_command", ToolInput: []byte(`{"CommandLine":"echo hello_perm_test"}`)})
	if err != nil {
		t.Fatal(err)
	}
	h.term.setScreen(agyScreen(t, "permission-prompt.txt"))
	h.s.checkScreen()
	h.s.checkScreen()
	if len(h.s.approvals) != 0 || len(h.term.written()) != 0 {
		t.Fatal("ambiguous native call became actionable")
	}
}
func TestAgyRewrittenArgsWithdrawNativeActions(t *testing.T) {
	h := agyHarness(t)
	_ = h.s.emit(&pb.Event{Kind: &pb.Event_ToolStarted{ToolStarted: &pb.ToolStarted{CallId: "conv/step_2", ItemType: "run_command", Input: `{"CommandLine":"echo hello_perm_test","toolSummary":"original"}`}}})
	step := int64(2)
	_, _ = h.s.hook(context.Background(), &agent.Hook{SessionID: "conv", HookEventName: "PreToolUse", StepIndex: &step, ToolUseID: "conv/step_2", ToolName: "run_command", ToolInput: []byte(`{"CommandLine":"echo hello_perm_test","toolSummary":"rewritten"}`)})
	h.term.setScreen(agyScreen(t, "permission-prompt.txt"))
	h.s.checkScreen()
	h.s.checkScreen()
	if len(h.s.approvals) != 0 {
		t.Fatal("full-argument mismatch ignored")
	}
}
func TestAgyLateIdenticalToolResultDoesNotRetractAnotherIdentity(t *testing.T) {
	h := agyHarness(t)
	card := nativeCard(t, h)
	a := card.GetApprovalRequested()
	h.s.retractFor(&pb.ToolCall{CallId: "conv/step_1", ItemType: a.ToolName, InputDigest: a.InputDigest})
	h.s.mu.Lock()
	_, ok := h.s.approvals[a.ApprovalId]
	h.s.mu.Unlock()
	if !ok {
		t.Fatal("another step's result retracted this card")
	}
}
func TestAgyResumeMismatchPausesQueuedInput(t *testing.T) {
	h := agyHarness(t)
	_, err := h.s.send("must not reach a different conversation", "identity-test")
	if err != nil {
		t.Fatal(err)
	}
	h.s.identityPending, h.s.identityResume = true, true
	if h.s.observeConversation(context.Background(), "wrong", "", time.Now()) {
		t.Fatal("resume silently accepted a fresh conversation")
	}
	if !h.s.identityFailed || h.s.meta.CodexSessionID != "conv" || !h.s.queue.Queue[0].Paused || len(h.term.written()) > 0 {
		t.Fatal("mismatched resume did not fence input")
	}
}
func TestAgyReconnectFollowsLocalConversationChange(t *testing.T) {
	h := agyHarness(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	h.s.identityPending, h.s.identityResume = true, false
	if !h.s.observeConversation(context.Background(), "new-conversation", path, time.Now()) {
		t.Fatal("reconnect treated local fork as failed resume")
	}
	if h.s.meta.CodexSessionID != "new-conversation" || h.s.identityPending {
		t.Fatal("conversation not rebound")
	}
	found := false
	for _, e := range h.events() {
		if e.GetAgentMessage() != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("conversation change not recorded in chat")
	}
	// A late Stop from the old native conversation must never rebind it.
	_, _ = h.s.hook(context.Background(), &agent.Hook{SessionID: "conv", HookEventName: "Stop", TerminationReason: "ERROR"})
	if h.s.meta.CodexSessionID != "new-conversation" {
		t.Fatal("late old hook rebound the session")
	}
}
func TestAgyResumeIdentityProofUnblocksWithoutResending(t *testing.T) {
	h := agyHarness(t)
	h.s.identityPending, h.s.identityResume = true, true
	if !h.s.observeConversation(context.Background(), "conv", "", time.Now()) || h.s.identityPending {
		t.Fatal("matching identity did not unblock")
	}
	if len(h.term.written()) > 0 {
		t.Fatal("observing identity dispatched input itself")
	}
}
func TestAgyPendingNativeEvidenceSurvivesRestart(t *testing.T) {
	h := agyHarness(t)
	_ = h.s.emit(&pb.Event{Kind: &pb.Event_ToolStarted{ToolStarted: &pb.ToolStarted{CallId: "conv/step_2", ItemType: "run_command", Input: `{"CommandLine":"echo hello_perm_test"}`}}})
	h.s.rememberNative(&agent.Hook{SessionID: "conv", ToolUseID: "conv/step_2", ToolName: "run_command", ToolInput: []byte(`{"CommandLine":"echo hello_perm_test"}`)})
	m := h.s.meta
	_ = h.s.log.Close()
	h.open(m)
	h.s.identityPending = false
	h.s.mu.Lock()
	candidates := h.s.nativeCandidatesLocked()
	h.s.mu.Unlock()
	if len(candidates) != 1 || candidates[0].Uncertain {
		t.Fatal("complete persisted native evidence lost")
	}
	_ = os.Remove(h.s.nativeRequestPath())
	_ = h.s.log.Close()
	h.open(m)
	h.s.identityPending = false
	h.s.mu.Lock()
	candidates = h.s.nativeCandidatesLocked()
	h.s.mu.Unlock()
	if len(candidates) != 1 || !candidates[0].Uncertain {
		t.Fatal("request reconstructed without report-only hook evidence")
	}
}
func TestQuotaProvidersRemainIndependentOnFingerprintCollision(t *testing.T) {
	h := agyHarness(t)
	now := time.Now()
	for _, kind := range []pb.Agent{pb.Agent_AGENT_CLAUDE, pb.Agent_AGENT_ANTIGRAVITY} {
		h.d.recordQuota("source", &pb.AgentQuota{Agent: kind, Account: "same-fingerprint", State: pb.QuotaState_QUOTA_STATE_OK, Windows: []*pb.QuotaWindow{{Name: "window"}}}, now)
	}
	if len(h.d.quotas) != 2 {
		t.Fatal("providers merged on account fingerprint")
	}
	h.d.recordQuota("source", &pb.AgentQuota{Agent: pb.Agent_AGENT_ANTIGRAVITY, Account: "changed-account", State: pb.QuotaState_QUOTA_STATE_OK}, now.Add(time.Minute))
	if len(h.d.quotas) != 2 {
		t.Fatal("agy account change deleted Claude's quota")
	}
}

func TestAgyOlderIdentityObservationCannotRebindAfterNewer(t *testing.T) {
	h := agyHarness(t)
	now := time.Now()
	if !h.s.observeConversation(context.Background(), "conv", "", now) {
		t.Fatal("initial identity")
	}
	if h.s.observeConversation(context.Background(), "old-snapshot", "", now.Add(-time.Second)) {
		t.Fatal("older observation accepted")
	}
	if h.s.meta.CodexSessionID != "conv" {
		t.Fatal("identity moved backwards")
	}
}
func TestAgyCLIGeneratedPlanInputKeepsScheduledProvenance(t *testing.T) {
	h := agyHarness(t)
	h.s.scheduledTask, h.s.scheduledRun = "task", "run"
	e := &pb.Event{Origin: originSystem, Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "[Approved] plan.md"}}}
	consume := h.s.annotate([]*pb.Event{e})
	if err := h.s.emit(e); err != nil {
		t.Fatal(err)
	}
	consume()
	if h.s.scheduledTask != "task" || h.s.scheduledRun != "run" {
		t.Fatal("generated native label took over scheduled provenance")
	}
}

func TestAgySubmissionEvidenceKeepsItsOriginalConversation(t *testing.T) {
	h := agyHarness(t)
	e := &pb.Event{TurnId: "conv/step_1", Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "identical"}}}
	if err := h.s.emit(e); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   string
		want bool
	}{{"conv", true}, {"other", false}, {"", false}} {
		if got := h.s.loggedUserMessage(input{Text: "identical", CLIConversation: tc.id}); got != tc.want {
			t.Fatalf("conversation %q confirmed=%v", tc.id, got)
		}
	}
}
func TestAgyConsumptionDoesNotRaceTerminalResolution(t *testing.T) {
	h := agyHarness(t)
	card := nativeCard(t, h)
	req := card.GetApprovalRequested()
	h.term.mu.Lock()
	h.term.onWrite = func(f *fakeTerm, b string) {
		if b == "1" {
			call := &pb.ToolCall{CallId: "conv/step_2", ItemType: req.ToolName, InputDigest: req.InputDigest}
			_ = h.s.emit(&pb.Event{Kind: &pb.Event_ToolCall{ToolCall: call}})
			h.s.retractFor(call)
			f.screen = agyScreen(t, "idle-empty-box.txt")
		}
	}
	h.term.mu.Unlock()
	if err := h.s.resolve(req.ApprovalId, true, ""); err != nil {
		t.Fatal(err)
	}
	resolutions := 0
	for _, e := range h.events() {
		if a := e.GetApprovalResolved(); a != nil && a.ApprovalId == req.ApprovalId {
			resolutions++
			if a.Decision != pb.ApprovalDecision_APPROVAL_DECISION_ALLOW {
				t.Fatal("own key delivery reported as terminal response")
			}
		}
	}
	if resolutions != 1 {
		t.Fatalf("approval has %d outcomes", resolutions)
	}
}
