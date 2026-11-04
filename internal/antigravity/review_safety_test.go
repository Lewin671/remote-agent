package antigravity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

func TestPendingNativeIdentitiesMustBeUnambiguous(t *testing.T) {
	a := New("agy", t.TempDir())
	screen := readScreenFixture(t, "permission-prompt.txt")
	one := &agent.Hook{SessionID: "conv", ToolUseID: "conv/step_1", ToolName: "run_command", ToolInput: []byte(`{"CommandLine":"echo hello_perm_test","toolSummary":"first"}`)}
	two := *one
	two.ToolUseID = "conv/step_2"
	two.ToolInput = []byte(`{"CommandLine":"echo hello_perm_test","toolSummary":"second"}`)
	if m := a.MatchApprovals(screen, []*agent.Hook{one, &two}); !m.Ambiguous || m.Hook != nil {
		t.Fatal("repeated command was guessed", m)
	}
	two.Uncertain = true
	if m := a.MatchApprovals(screen, []*agent.Hook{&two}); !m.Ambiguous {
		t.Fatal("rewritten or unproven args were trusted")
	}
	if m := a.MatchApprovals(screen, []*agent.Hook{one}); m.Ambiguous || m.Hook != one {
		t.Fatal("unique complete request rejected")
	}
	changed := strings.Replace(screen, "4. No, cancel", "4. Always allow", 1)
	if m := a.MatchApprovals(changed, []*agent.Hook{one}); m.Hook != nil {
		t.Fatal("unknown menu accepted")
	}
}

type contextTerm struct {
	*fakeTerm
	ctx context.Context
}

func (t contextTerm) Context() context.Context { return t.ctx }
func TestApprovalRequiresConsumptionProof(t *testing.T) {
	a := New("agy", t.TempDir())
	h := &agent.Hook{ToolName: "run_command", ToolInput: []byte(`{"CommandLine":"echo hello_perm_test"}`)}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	term := contextTerm{&fakeTerm{screen: readScreenFixture(t, "permission-prompt.txt")}, ctx}
	err := a.AnswerApproval(term, h, true, nil)
	if !errors.Is(err, agent.ErrDeliveryUnconfirmed) || string(term.written) != "1" {
		t.Fatalf("delivery without consumption %q %v", term.written, err)
	}
}
func TestModeUsesBothPolicyAndCycle(t *testing.T) {
	a := New("agy", t.TempDir())
	for _, tc := range []struct {
		policy, cycle string
		want          pb.ApprovalMode
	}{
		{"request-review", "default", pb.ApprovalMode_APPROVAL_MODE_ASK}, {"request-review", "auto", pb.ApprovalMode_APPROVAL_MODE_AUTO}, {"request-review", "plan", pb.ApprovalMode_APPROVAL_MODE_PLAN},
		{"always-proceed", "default", pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS}, {"always-proceed", "plan", 0}, {"always-proceed", "auto", 0}, {"strict", "default", 0}, {"", "default", 0}, {"request-review", "accept-edits", 0},
	} {
		c := &pb.TurnContext{ApprovalPolicy: tc.policy, PermissionMode: tc.cycle}
		if got := a.Mode(c); got != tc.want {
			t.Fatalf("%s + %s = %v", tc.policy, tc.cycle, got)
		}
		if a.ModeLocked(c) != (tc.policy == "always-proceed") {
			t.Fatal("full-access policy lock changed")
		}
	}
}
func TestModeCyclingRejectsUnknownStaleAndBackground(t *testing.T) {
	a := New("agy", t.TempDir())
	for _, o := range []agent.ModeObservation{{ToolPermission: "request-review", Mode: "default"}, {ToolPermission: "request-review", Mode: "default", At: time.Now().Add(-time.Minute), IdleKnown: true}, {ToolPermission: "request-review", Mode: "default", At: time.Now(), IdleKnown: true, Background: true}, {ToolPermission: "request-review", Mode: "default", At: time.Now()}} {
		term := &fakeTerm{screen: readScreenFixture(t, "idle-empty-box.txt")}
		if err := a.SetMode(term, pb.ApprovalMode_APPROVAL_MODE_PLAN, false, agent.ModeFacts{Observation: func() agent.ModeObservation { return o }}); err == nil || len(term.written) > 0 {
			t.Fatal("unsafe cycle started", o)
		}
	}
}
func TestFirstPromptRequiresConversationBoundTranscript(t *testing.T) {
	a := New("agy", t.TempDir())
	line := []byte(`{"display":"identical prompt","workspace":"/workspace/project"}`)
	for _, id := range []string{"", "conversation-one", "conversation-two"} {
		if a.Submitted(line, func(s string) bool { return s == "identical prompt" }, id) {
			t.Fatal("unbound history confirmed", id)
		}
	}
	later := []byte(`{"display":"identical prompt","conversationId":"conversation-one"}`)
	if !a.Submitted(later, func(string) bool { return true }, "conversation-one") || a.Submitted(later, func(string) bool { return true }, "conversation-two") {
		t.Fatal("bound later history mismatch")
	}
}
func TestPlanIdentityRevisionAndLocalCompletion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.md")
	text := "# Exact plan\nDo one thing.\n"
	mustWrite(t, path, []byte(text))
	args, _ := json.Marshal(map[string]any{"Path": path, "Content": text, "ArtifactMetadata": map[string]bool{"RequestFeedback": true}})
	planner, _ := json.Marshal(map[string]any{"step_index": 1, "type": "PLANNER_RESPONSE", "tool_calls": []any{map[string]any{"name": "write_file", "args": json.RawMessage(args)}}})
	p := NewParser()
	p.SetConversation("conv")
	if evs, err := p.Parse(planner); err != nil || len(evs) != 2 || evs[0].GetPlanProposed() != nil {
		t.Fatalf("plan presented before successful write %v %v", evs, err)
	}
	evs, err := p.Parse([]byte(`{"step_index":2,"type":"GENERIC","status":"DONE","content":"Created file"}`))
	if err != nil || len(evs) != 2 || evs[1].GetPlanProposed() == nil {
		t.Fatal(evs, err)
	}
	facts := p.Plan()
	sum := sha256.Sum256([]byte(text))
	if facts == nil || facts.Path != path || facts.Revision != hex.EncodeToString(sum[:]) || !planRevisionMatches(facts) {
		t.Fatal("unbound plan", facts)
	}
	mustWrite(t, path, []byte("edited after presentation"))
	if planRevisionMatches(facts) {
		t.Fatal("old revision remained actionable")
	}
	mustWrite(t, path, []byte(text))
	evs, err = p.Parse([]byte(`{"step_index":3,"type":"USER_INPUT","content":"<USER_REQUEST>\n[Approved] plan.md\n</USER_REQUEST>"}`))
	if err != nil {
		t.Fatal(err)
	}
	answered, system := false, false
	for _, e := range evs {
		if e.GetPlanAnswered() != nil {
			answered = true
		}
		if e.GetUserMessage() != nil {
			system = e.Origin.GetKind() == pb.OriginKind_ORIGIN_KIND_SYSTEM
		}
	}
	if !answered || !system || p.Plan() != nil {
		t.Fatal("local plan completion or provenance missing")
	}
}
func TestAmbiguousPlansAndEditsNeverSendKeys(t *testing.T) {
	p := NewParser()
	p.plans["one"] = &agent.PlanFacts{Path: "one"}
	p.plans["two"] = &agent.PlanFacts{Path: "two"}
	if p.Plan() != nil {
		t.Fatal("selected one of multiple pending plans")
	}
	a := New("agy", t.TempDir())
	term := &fakeTerm{screen: readScreenFixture(t, "artifact-review-screen.txt")}
	if err := a.AnswerPlan(term, pb.PlanChoice_PLAN_CHOICE_IMPLEMENT, nil); err == nil || len(term.written) > 0 {
		t.Fatal("empty list answered as a plan")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.md")
	_ = os.WriteFile(path, []byte("new revision"), 0600)
	if err := a.AnswerPlan(term, pb.PlanChoice_PLAN_CHOICE_IMPLEMENT, &agent.PlanFacts{Path: path, Revision: "old"}); err == nil || len(term.written) > 0 {
		t.Fatal("changed artifact received a key")
	}
}

func TestHookLiteralStringsAreNotDecodedAsJSONValues(t *testing.T) {
	a := New("agy", t.TempDir())
	h, err := a.DecodeHook([]byte(`{"hook_event_name":"PreToolUse","conversationId":"conv","stepIdx":1,"toolCall":{"name":"run_command","args":{"CommandLine":"true","WaitMsBeforeAsync":5000}}}`))
	if err != nil {
		t.Fatal(err)
	}
	normalized := normalizeTranscriptArgs([]byte(`{"CommandLine":"\"true\"","WaitMsBeforeAsync":"5000"}`))
	if agent.InputDigest("run_command", h.ToolInput) != agent.InputDigest("run_command", normalized) {
		t.Fatalf("native literal string was changed: %s / %s", h.ToolInput, normalized)
	}
}
