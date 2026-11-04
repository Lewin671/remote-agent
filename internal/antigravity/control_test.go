package antigravity

import (
	"errors"
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

func TestQuestionControllerValidatesQuestionAndOptionRows(t *testing.T) {
	a := New("agy", t.TempDir())
	screen := readScreenFixture(t, "ask-question-single.txt")
	q := &pb.QuestionAsked{QuestionId: "step_1", Questions: []*pb.Question{{Text: "What is your primary language?", Options: []*pb.QuestionOption{{Label: "(Recommended) Python"}, {Label: "Go"}, {Label: "Rust"}}}}}
	for _, bad := range []struct {
		name     string
		question *pb.QuestionAsked
		options  []int32
	}{
		{"out of range", q, []int32{4}}, {"multiple answers", q, []int32{1, 2}},
		{"wrong question", &pb.QuestionAsked{Questions: []*pb.Question{{Text: "Another question?"}}}, []int32{1}},
	} {
		t.Run(bad.name, func(t *testing.T) {
			term := &fakeTerm{screen: screen}
			if err := a.Answer(term, bad.question, nil, bad.options); err == nil || len(term.written) > 0 {
				t.Fatal("unsafe question answer")
			}
		})
	}
	term := &fakeTerm{screen: screen}
	if err := a.Answer(term, q, nil, []int32{2}); err != nil || string(term.written) != "2" {
		t.Fatalf("answer %q %v", term.written, err)
	}
}
func TestMultiQuestionTogglesOnlyChangedSelections(t *testing.T) {
	a := New("agy", t.TempDir())
	screen := readScreenFixture(t, "ask-question-multi.txt")
	screen = strings.Replace(screen, "1. [ ]", "1. [x]", 1)
	q := &pb.QuestionAsked{Questions: []*pb.Question{{Text: "Please select your toppings:", MultiSelect: true, Options: []*pb.QuestionOption{{Label: "(Recommended) Cheese"}, {Label: "Mushrooms"}, {Label: "Olives"}, {Label: "Pepperoni"}}}}}
	term := &fakeTerm{screen: screen}
	term.onWrite = func(b []byte) {
		if string(b) == "3" {
			term.screen = strings.Replace(term.screen, "3. [ ]", "3. [x]", 1)
		}
	}
	if err := a.Answer(term, q, nil, []int32{1, 3}); err != nil || string(term.written) != "3\r" {
		t.Fatalf("toggles %q %v", term.written, err)
	}
}
func TestModeConfirmationAndImmutableCapabilities(t *testing.T) {
	a := New("agy", t.TempDir())
	mode := "default"
	term := &fakeTerm{screen: readScreenFixture(t, "idle-empty-box.txt")}
	cycle := map[string]string{"default": "accept-edits", "accept-edits": "auto", "auto": "plan", "plan": "default"}
	term.onWrite = func(b []byte) {
		if string(b) == agent.KeyBTab {
			mode = cycle[mode]
		}
	}
	facts := agent.ModeFacts{CurrentMode: func() string { return mode }}
	if err := a.SetMode(term, pb.ApprovalMode_APPROVAL_MODE_PLAN, false, facts); err != nil || mode != "plan" {
		t.Fatal(err, mode)
	}
	if err := a.SetMode(term, pb.ApprovalMode_APPROVAL_MODE_ASK, false, facts); err != nil || mode != "default" {
		t.Fatal(err, mode)
	}
	before := len(term.written)
	if err := a.SetMode(term, pb.ApprovalMode_APPROVAL_MODE_PLAN, true, facts); !errors.Is(err, agent.ErrBusy) {
		t.Fatal("cycled while running", err)
	}
	facts.FullAccess = true
	if err := a.SetMode(term, pb.ApprovalMode_APPROVAL_MODE_ASK, false, facts); !errors.Is(err, agent.ErrUnsupported) {
		t.Fatal("unlocked full access", err)
	}
	if err := a.SetModel(term, "claude-sonnet-5-5", "low"); !errors.Is(err, agent.ErrUnsupported) {
		t.Fatal("live model", err)
	}
	if len(term.written) != before {
		t.Fatal("unsupported action sent keys")
	}
}
func TestAllRemainingScreenSamples(t *testing.T) {
	a := New("agy", t.TempDir())
	for _, tc := range []struct {
		name        string
		busy, ready bool
	}{
		{"queued-message.txt", true, true}, {"artifact-review-notice.txt", true, true}, {"interrupted.txt", false, true},
	} {
		s := a.Analyze(readScreenFixture(t, tc.name))
		if s.Busy != tc.busy || s.ComposerReady != tc.ready {
			t.Fatalf("%s: %+v", tc.name, s)
		}
	}
	if !a.Interrupted(readScreenFixture(t, "interrupted.txt")) {
		t.Fatal("missing cancel banner")
	}
	screen := readScreenFixture(t, "interrupted.txt")
	screen = strings.Replace(screen, "  ⎿  Interrupted · What should Antigravity CLI do instead?", "Old banner\nNew reply", 1)
	if a.Interrupted(screen) {
		t.Fatal("old banner inferred an interruption")
	}
	prefix := readScreenFixture(t, "permission-prompt.txt")
	idle := readScreenFixture(t, "idle-empty-box.txt")
	if a.Analyze(idle + prefix + idle).PermissionOpen {
		t.Fatal("historical permission became an active popup")
	}
	draft := "────────────────────\n> Auto mode: user text\n────────────────────\n"
	if a.Analyze(draft).DraftEmpty {
		t.Fatal("typed mode-like draft was erased")
	}
}
func TestFilePermissionDenyUsesObservedMenuNumber(t *testing.T) {
	a := New("agy", t.TempDir())
	h := &agent.Hook{ToolName: "write_file", ToolInput: []byte(`{"Path":"/workspace/example.txt"}`)}
	term := &fakeTerm{screen: "Requesting permission for:\n   /workspace/example.txt\n\nAllow creation of this file?\n> 1. Yes, allow creation\n  2. No, deny creation\n"}
	if err := a.AnswerApproval(term, h, false); err != nil || string(term.written) != "2" {
		t.Fatalf("deny file %q %v", term.written, err)
	}
}
