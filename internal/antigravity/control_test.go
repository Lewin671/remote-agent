package antigravity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	facts := agent.ModeFacts{Observation: func() agent.ModeObservation {
		return agent.ModeObservation{ToolPermission: "request-review", Mode: mode, At: time.Now(), IdleKnown: true}
	}}
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
	term := &fakeTerm{screen: "────────────────────\n/workspace/example.txt  +1\n 1 + hello\nAllow creation of this file?\n> 1. Yes, allow creation\n  2. No, deny creation\n"}
	if err := a.AnswerApproval(term, h, false, func() bool { return true }); err != nil || string(term.written) != "2" {
		t.Fatalf("deny file %q %v", term.written, err)
	}
}

func TestNativeModePlaceholderStyle(t *testing.T) {
	a := New("agy", t.TempDir())
	s := a.Analyze(readScreenFixture(t, "mode-1.5.0-plan.txt"))
	if !s.ComposerReady || !s.DraftEmpty {
		t.Fatalf("styled plan placeholder is a draft: %+v", s)
	}
	s = a.Analyze(readScreenFixture(t, "mode-1.5.0-accept-edits.txt"))
	if s.DraftEmpty {
		t.Fatal("unstyled text was erased without placeholder evidence")
	}
}

func TestRealMultilineDraft(t *testing.T) {
	a := New("agy", t.TempDir())
	styled := readScreenFixture(t, "multiline-1.5.0-draft.txt")
	text, ok := a.ComposerText(styled, "")
	if !ok || text != "line one\nline two" || a.Analyze(styled).DraftEmpty {
		t.Fatalf("draft %q %v", text, ok)
	}
}

func TestRealNativeFileCreationMenu(t *testing.T) {
	a := New("agy", t.TempDir())
	screen := readScreenFixture(t, "file-create-1.5.0.txt")
	h := &agent.Hook{ToolName: "write_file", ToolInput: []byte(`{"Path":"/workspace/project/create-allow.txt","Content":"hello"}`)}
	if !a.Analyze(screen).PermissionOpen {
		t.Fatal("native file menu was not recognised")
	}
	if _, ok := a.MatchApproval(screen, h); !ok {
		t.Fatal("native file path was not matched")
	}
	term := &fakeTerm{screen: screen}
	if err := a.AnswerApproval(term, h, false, func() bool { return true }); err != nil || string(term.written) != "2" {
		t.Fatalf("file deny %q %v", term.written, err)
	}
}

func TestNativeFilePromptCanonicalAlias(t *testing.T) {
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skip(err)
	}
	if !sameNativeFile(filepath.Join(real, "new.txt"), filepath.Join(alias, "new.txt")) {
		t.Fatal("native canonical path alias was rejected")
	}
	if sameNativeFile(filepath.Join(real, "one.txt"), filepath.Join(alias, "two.txt")) {
		t.Fatal("different paths were matched")
	}
}

func TestRealNativePlanReviewPanel(t *testing.T) {
	a := New("agy", t.TempDir())
	root := t.TempDir()
	path := filepath.Join(root, "acceptance_approve_plan.md")
	text := "# Plan\nhello\n"
	mustWrite(t, path, []byte(text))
	sum := sha256.Sum256([]byte(text))
	facts := &agent.PlanFacts{Path: path, Root: root, Revision: hex.EncodeToString(sum[:]), Text: text}
	screen := readScreenFixture(t, "plan-review-1.5.0-pending.txt")
	if !a.Analyze(screen).PlanOpen || !a.PlanScreen(screen, facts) {
		t.Fatal("real action-required panel was not bound")
	}
	term := &fakeTerm{screen: screen}
	term.onWrite = func(b []byte) {
		if string(b) == "y" {
			term.screen = readScreenFixture(t, "idle-empty-box.txt")
		}
	}
	if err := a.AnswerPlan(term, pb.PlanChoice_PLAN_CHOICE_IMPLEMENT, facts, nil); err != nil || string(term.written) != "y" {
		t.Fatalf("native plan approval %q %v", term.written, err)
	}
}

func TestRealReviewFixtureVariants(t *testing.T) {
	a := New("agy", t.TempDir())
	root := t.TempDir()
	text := "# Exact revision\n"
	sum := sha256.Sum256([]byte(text))
	for _, tc := range []struct{ screen, name string }{
		{"plan-1.5.0-approve.txt", "acceptance_approve_plan.md"}, {"plan-1.5.0-reject.txt", "acceptance_reject_plan.md"},
	} {
		path := filepath.Join(root, tc.name)
		mustWrite(t, path, []byte(text))
		facts := &agent.PlanFacts{Root: root, Path: path, Revision: hex.EncodeToString(sum[:]), Decisions: map[string]string{tc.name: "new"}}
		if tc.name == "acceptance_reject_plan.md" {
			facts.Decisions["acceptance_approve_plan.md"] = "approved"
		}
		if !a.PlanScreen(readScreenFixture(t, tc.screen), facts) {
			t.Fatalf("real panel %s not bound", tc.screen)
		}
	}
	local := &agent.PlanFacts{Root: root, Path: filepath.Join(root, "acceptance_local_first_plan.md"), Revision: hex.EncodeToString(sum[:])}
	mustWrite(t, local.Path, []byte(text))
	if a.PlanScreen(readScreenFixture(t, "plan-1.5.0-local-first.txt"), local) {
		t.Fatal("locally answered plan remained actionable")
	}
	edited := &agent.PlanFacts{Root: root, Path: filepath.Join(root, "acceptance_edit_plan.md"), Revision: hex.EncodeToString(sum[:])}
	mustWrite(t, edited.Path, []byte(text+"changed"))
	if a.PlanScreen(readScreenFixture(t, "plan-1.5.0-edited.txt"), edited) {
		t.Fatal("edited revision remained actionable")
	}
	term := &fakeTerm{screen: readScreenFixture(t, "plan-1.5.0-multiple.txt")}
	if err := a.AnswerPlan(term, pb.PlanChoice_PLAN_CHOICE_IMPLEMENT, nil, nil); err == nil || len(term.written) > 0 {
		t.Fatal("ambiguous real artifact list received a key")
	}
}

func TestNativePlanSubmitCommitsOnlyTheRequestedChoice(t *testing.T) {
	a := New("agy", t.TempDir())
	root := t.TempDir()
	path := filepath.Join(root, "acceptance_reject_plan.md")
	text := "# Reject plan\n"
	mustWrite(t, path, []byte(text))
	sum := sha256.Sum256([]byte(text))
	facts := &agent.PlanFacts{Root: root, Path: path, Revision: hex.EncodeToString(sum[:]), Decisions: map[string]string{"acceptance_reject_plan.md": "new", "acceptance_approve_plan.md": "approved", "walkthrough.md": "open"}}
	initial := strings.Replace(readScreenFixture(t, "plan-1.5.0-reject.txt"), "Keyboard:", "  walkthrough.md\n\nKeyboard:", 1)
	staged := readScreenFixture(t, "plan-1.5.0-submit-staged.txt")
	for _, focus := range []bool{true, false} {
		submitted := false
		term := &fakeTerm{screen: initial}
		term.onWrite = func(b []byte) {
			switch string(b) {
			case "n":
				term.screen = staged
				if !focus {
					term.screen = strings.Replace(term.screen, "\x1b[7m\x1b[32m Submit ", "\x1b[32m Submit ", 1)
				}
			case agent.KeyEsc:
				term.screen = staged
			case agent.KeyEnter:
				submitted = true
				term.screen = readScreenFixture(t, "idle-empty-box.txt")
			}
		}
		if err := a.AnswerPlan(term, pb.PlanChoice_PLAN_CHOICE_KEEP_PLANNING, facts, func() bool { return submitted }); err != nil {
			t.Fatal(err)
		}
		want := "n\r"
		if !focus {
			want = "n\x1b\r"
		}
		if string(term.written) != want || !submitted {
			t.Fatalf("review was not submitted: %q", term.written)
		}
	}
	lines := strings.Split(initial, "\n")
	for i, line := range lines {
		if strings.Contains(StripANSI(line), "acceptance_approve_plan.md") {
			line = strings.Replace(line, "✓", "✗", 1)
			lines[i] = strings.Replace(line, "approved", "rejected", 1)
		}
	}
	dirty := strings.Join(lines, "\n")
	term := &fakeTerm{screen: dirty}
	if err := a.AnswerPlan(term, pb.PlanChoice_PLAN_CHOICE_KEEP_PLANNING, facts, nil); err == nil || len(term.written) > 0 {
		t.Fatal("another artifact's unsubmitted choice was committed")
	}
}
