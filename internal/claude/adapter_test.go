package claude

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

func TestOptionsAndArgv(t *testing.T) {
	a := New("claude", t.TempDir())
	args, err := a.Options([]string{"--add-dir", "/x"}, "sonnet", "low", pb.ApprovalMode_APPROVAL_MODE_ASK)
	if err != nil || strings.Join(args, " ") != "--add-dir /x --model sonnet --effort low --permission-mode default" {
		t.Fatalf("options %q %v", args, err)
	}
	for _, bad := range [][]string{{"-p"}, {"--settings", "x"}, {"--resume=abc"}, {"--session-id", "x"}, {"--bare"}, {"--safe-mode"}} {
		if _, err := a.Options(bad, "", "", 0); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := a.Options(nil, "", "", pb.ApprovalMode_APPROVAL_MODE_PLAN); err == nil {
		t.Error("accepted starting in Plan mode")
	}
	if _, err := a.Options(nil, "x; rm", "", 0); err == nil {
		t.Error("accepted a bad model")
	}

	dir := t.TempDir()
	l := &agent.Launch{Dir: dir, Args: []string{"--model", "haiku", "fix the bug"}}
	argv, err := a.Argv(l)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.SessionID) != 36 || argv[0] != "--settings" || argv[2] != "--session-id" || argv[3] != l.SessionID || argv[len(argv)-1] != "fix the bug" {
		t.Fatalf("argv %q", argv)
	}
	b, err := os.ReadFile(filepath.Join(dir, SettingsFile))
	if err != nil || !strings.Contains(string(b), `"$RA_EXE\" hook claude`) || !strings.Contains(string(b), "PermissionRequest") {
		t.Fatalf("settings %s %v", b, err)
	}
	var parsed map[string]any
	if json.Unmarshal(b, &parsed) != nil {
		t.Fatal("settings are not JSON")
	}

	// Resuming before anything was sent starts the same session ID again.
	l = &agent.Launch{Dir: dir, Args: []string{"--model", "haiku", "fix the bug"}, SessionID: "id-1", Resume: true, Transcript: filepath.Join(dir, "none.jsonl")}
	argv, _ = a.Argv(l)
	if argv[2] != "--session-id" || strings.Contains(strings.Join(argv, " "), "fix the bug") {
		t.Fatalf("resume without transcript %q", argv)
	}
	// With a transcript it resumes, with the settings it last ran with.
	tr := filepath.Join(dir, "t.jsonl")
	os.WriteFile(tr, []byte("{}\n"), 0o600)
	l = &agent.Launch{Dir: dir, Args: []string{"--model", "haiku", "--permission-mode", "default", "--verbose", "fix"}, SessionID: "id-1", Resume: true, Transcript: tr,
		Context: &pb.TurnContext{Model: "claude-sonnet-5-5", Effort: "low", PermissionMode: "plan"}}
	argv, _ = a.Argv(l)
	j := strings.Join(argv, " ")
	if argv[2] != "--resume" || argv[3] != "id-1" || !strings.Contains(j, "--verbose --model claude-sonnet-5-5 --effort low --permission-mode plan") ||
		strings.Contains(j, "haiku") || strings.Contains(j, "fix") || strings.Count(j, "--permission-mode") != 1 {
		t.Fatalf("resume %q", argv)
	}
}

func TestFullAccess(t *testing.T) {
	home := t.TempDir()
	a := New("claude", home)
	if a.FullAccess(nil) || a.FullAccess([]string{"--permission-mode", "default"}) {
		t.Fatal("full access without it allowed")
	}
	for _, args := range [][]string{{"--allow-dangerously-skip-permissions"}, {"--dangerously-skip-permissions"}, {"--permission-mode", "bypassPermissions"}, {"--permission-mode=bypassPermissions"}} {
		if !a.FullAccess(args) {
			t.Errorf("%q", args)
		}
	}
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"permissions":{"defaultMode":"bypassPermissions"}}`), 0o600)
	if !a.FullAccess(nil) || a.FullAccess([]string{"--permission-mode", "auto"}) {
		t.Fatal("the default mode in settings.json")
	}
	// Resuming keeps bypass on offer as it was at launch, whatever the
	// default mode says now (review C9).
	argv, _ := a.Argv(&agent.Launch{Dir: t.TempDir(), SessionID: "x", Resume: true, FullAccess: true, Context: &pb.TurnContext{PermissionMode: "default"}})
	if !strings.Contains(strings.Join(argv, " "), "--allow-dangerously-skip-permissions") {
		t.Fatalf("argv %q", argv)
	}
	argv, _ = a.Argv(&agent.Launch{Dir: t.TempDir(), SessionID: "x", Resume: true, FullAccess: false, Context: &pb.TurnContext{PermissionMode: "default"}})
	if strings.Contains(strings.Join(argv, " "), "dangerously") {
		t.Fatalf("bypass added to a session started without it: %q", argv)
	}
}

func TestSubmitted(t *testing.T) {
	a := New("", "")
	for line, want := range map[string]bool{
		`{"type":"user","origin":{"kind":"human"},"message":{"role":"user","content":"hello  world"}}`:           true,
		`{"type":"user","message":{"role":"user","content":"hello world"}}`:                                      false,
		`{"type":"queue-operation","operation":"enqueue","content":"hello world"}`:                               true,
		`{"type":"queue-operation","operation":"remove","content":"hello world"}`:                                false,
		`{"type":"attachment","attachment":{"type":"queued_command","prompt":"hello world"}}`:                    true,
		`{"type":"user","origin":{"kind":"human"},"message":{"content":[{"type":"text","text":"hello world"}]}}`: true,
		`{"type":"user","origin":{"kind":"human"},"message":{"role":"user","content":"other"}}`:                  false,
	} {
		if got := a.Submitted([]byte(line), "hello world", ""); got != want {
			t.Errorf("%s: %v", line, got)
		}
	}
}

// fakeClaude is a terminal that reacts to keys like Claude Code 2.1.284.
type fakeClaude struct {
	screen   string
	writes   []string
	mode     int  // index in cycle
	noBypass bool // bypass not on offer
	noAuto   bool
	typed    string
	menu     []string // model menu labels
	cursor   int
	effort   int // index in efforts
	cached   bool
	confirm  bool
	note     string
	question *pb.Question
	picked   []int
	review   bool
	plan     bool
}

func (f *fakeClaude) modeLine() string {
	return map[string]string{
		"default": "⏸ manual mode on", "acceptEdits": "⏵⏵ accept edits on (shift+tab to cycle)", "plan": "⏸ plan mode on (shift+tab to cycle)",
		"bypassPermissions": "⏵⏵ bypass permissions on (shift+tab to cycle)", "auto": "⏵⏵ auto mode on (shift+tab to cycle)",
	}[cycle[f.mode]]
}

func (f *fakeClaude) draw() {
	var b strings.Builder
	b.WriteString(f.note + "\n")
	switch {
	case f.confirm:
		b.WriteString(rule + "\n  Switch model?\n  ❯ 1. Yes, switch to Haiku 4.5\n    2. No, go back\n")
	case f.menu != nil:
		b.WriteString(rule + "\n  Select model\n")
		for i, l := range f.menu {
			c := "  "
			if i == f.cursor {
				c = "❯ "
			}
			b.WriteString("  " + c + string(rune('1'+i)) + ". " + l + "                Something · desc\n")
		}
		if strings.HasPrefix(f.menu[f.cursor], "Haiku") {
			b.WriteString("  ○ Effort not supported for Haiku\n")
		} else {
			b.WriteString("  ◐ " + []string{"Low", "Medium", "High", "xHigh", "Max"}[f.effort] + " effort ←/→ to adjust\n")
		}
		b.WriteString("  Enter to set as default · s to use this session only · Esc to cancel\n")
	case f.review:
		b.WriteString(rule + "\nReview your answers\nReady to submit your answers?\n❯ 1. Submit answers\n  2. Cancel\n")
	case f.question != nil:
		b.WriteString(rule + "\n ☐ H\n" + f.question.Text + "\n")
		for i, o := range f.question.Options {
			b.WriteString("  " + string(rune('1'+i)) + ". " + o.Label + "\n")
		}
		b.WriteString("Enter to select · ↑/↓ to navigate · Esc to cancel\n")
	case f.plan:
		b.WriteString(rule + "\n Claude has written up a plan and is ready to execute. Would you like to proceed?\n" +
			" ❯ 1. Yes, auto-accept edits\n   2. Yes, manually approve edits\n   3. Tell Claude what to change\n")
	default:
		b.WriteString(rule + "\n\x1b[39m❯ " + f.typed + "\x1b[7m \x1b[0m\n" + rule + "\n   Haiku 4.5\n  " + f.modeLine() + "\n")
	}
	f.screen = b.String()
}

func (f *fakeClaude) Capture() (string, error) { f.draw(); return f.screen, nil }

func (f *fakeClaude) Write(b []byte) error {
	k := string(b)
	f.writes = append(f.writes, k)
	switch {
	case f.confirm && k == "1":
		f.confirm, f.menu = false, nil
		f.note = "⎿  Set model to Haiku 4.5 for this session only"
	case f.menu != nil:
		switch k {
		case agent.KeyUp:
			f.cursor--
		case agent.KeyDown:
			f.cursor++
		case agent.KeyRight:
			f.effort = (f.effort + 1) % len(efforts)
		case "s":
			label := strings.Fields(f.menu[f.cursor])[0]
			if f.cached {
				f.confirm = true
				return nil
			}
			f.menu = nil
			f.note = "⎿  Set model to " + label + " 5.5 for this session only with " + efforts[f.effort] + " effort"
		case agent.KeyEsc:
			f.menu = nil
		}
	case f.review && k == "1":
		f.review, f.question = false, nil
	case f.question != nil:
		if k == agent.KeyRight || !f.question.MultiSelect {
			f.review = true
		}
		f.picked = append(f.picked, int(k[0]-'0'))
	case f.plan:
		f.plan = false
	case k == agent.KeyBTab:
		for {
			f.mode = (f.mode + 1) % len(cycle)
			if (cycle[f.mode] == "bypassPermissions" && f.noBypass) || (cycle[f.mode] == "auto" && f.noAuto) {
				continue
			}
			break
		}
	case k == "/model":
		f.typed = k
	case k == agent.KeyEnter && f.typed == "/model":
		f.typed = ""
		f.menu = []string{"Default (recommended)", "Opus", "Fable", "Sonnet", "Haiku ✔"}
		f.cursor, f.effort = 4, 1
	}
	return nil
}

func TestSetMode(t *testing.T) {
	a := New("claude", "")
	f := &fakeClaude{noBypass: true, noAuto: true}
	if err := a.SetMode(f, pb.ApprovalMode_APPROVAL_MODE_PLAN, false); err != nil || cycle[f.mode] != "plan" {
		t.Fatalf("plan: %v %s", err, cycle[f.mode])
	}
	if err := a.SetMode(f, pb.ApprovalMode_APPROVAL_MODE_ASK, false); err != nil || cycle[f.mode] != "default" {
		t.Fatalf("ask: %v %s", err, cycle[f.mode])
	}
	// Bypass is not on offer: a full turn, then back where it was.
	err := a.SetMode(f, pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS, false)
	if !errors.Is(err, agent.ErrUnsupported) || cycle[f.mode] != "default" {
		t.Fatalf("bypass: %v %s", err, cycle[f.mode])
	}
	// While a turn runs, never pass through a looser mode.
	f = &fakeClaude{}
	f.mode = index("plan")
	n := len(f.writes)
	if err := a.SetMode(f, pb.ApprovalMode_APPROVAL_MODE_ASK, true); !errors.Is(err, agent.ErrBusy) || len(f.writes) != n {
		t.Fatalf("running: %v %q", err, f.writes)
	}
	if err := a.SetMode(f, pb.ApprovalMode_APPROVAL_MODE_ASK, false); err != nil || cycle[f.mode] != "default" {
		t.Fatalf("idle: %v %s", err, cycle[f.mode])
	}
}

func TestSetModel(t *testing.T) {
	a := New("claude", "")
	f := &fakeClaude{}
	if err := a.SetModel(f, "sonnet", "max"); err != nil {
		t.Fatal(err)
	}
	w := strings.Join(f.writes, "|")
	if w != "/model|\r|"+agent.KeyUp+"|"+agent.KeyRight+"|"+agent.KeyRight+"|"+agent.KeyRight+"|s" {
		t.Fatalf("keys %q", f.writes)
	}
	// A cached conversation asks to confirm; haiku has no effort.
	f = &fakeClaude{cached: true}
	if err := a.SetModel(f, "haiku", ""); err != nil {
		t.Fatal(err)
	}
	if w := strings.Join(f.writes, "|"); w != "/model|\r|s|1" {
		t.Fatalf("keys %q", f.writes)
	}
	if err := a.SetModel(f, "haiku", "low"); !errors.Is(err, agent.ErrUnsupported) {
		t.Fatalf("haiku effort: %v", err)
	}
	if err := a.SetModel(f, "gpt-9", ""); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("unknown model: %v", err)
	}
	// Never over a draft.
	f = &fakeClaude{typed: "my draft"}
	if err := a.SetModel(f, "sonnet", "low"); !errors.Is(err, agent.ErrNotReady) || len(f.writes) != 0 {
		t.Fatalf("draft: %v %q", err, f.writes)
	}
}

func TestAnswerAndPlan(t *testing.T) {
	a := New("claude", "")
	q := &pb.QuestionAsked{QuestionId: "t1", Questions: []*pb.Question{{Id: "Which?", Text: "Which?", Options: []*pb.QuestionOption{{Label: "A"}, {Label: "B"}}}}}
	f := &fakeClaude{question: q.Questions[0]}
	if err := a.Answer(f, q, nil, []int32{2}); err != nil {
		t.Fatal(err)
	}
	if w := strings.Join(f.writes, "|"); w != "2|1" || f.question != nil {
		t.Fatalf("single: %q", f.writes)
	}
	multi := &pb.QuestionAsked{QuestionId: "t2", Questions: []*pb.Question{{Id: "Which?", Text: "Which?", MultiSelect: true, Options: []*pb.QuestionOption{{Label: "A"}, {Label: "B"}, {Label: "C"}}}}}
	f = &fakeClaude{question: multi.Questions[0]}
	if err := a.Answer(f, multi, nil, []int32{1, 3}); err != nil {
		t.Fatal(err)
	}
	if w := strings.Join(f.writes, "|"); w != "1|3|"+agent.KeyRight+"|1" {
		t.Fatalf("multi: %q", f.writes)
	}

	f = &fakeClaude{}
	if err := a.AnswerPlan(f, pb.PlanChoice_PLAN_CHOICE_IMPLEMENT); !errors.Is(err, agent.ErrNoPlan) {
		t.Fatalf("no plan: %v", err)
	}
	f.plan = true
	if err := a.AnswerPlan(f, pb.PlanChoice_PLAN_CHOICE_CLEAR_AND_IMPLEMENT); !errors.Is(err, agent.ErrUnsupported) {
		t.Fatalf("clear: %v", err)
	}
	if err := a.AnswerPlan(f, pb.PlanChoice_PLAN_CHOICE_IMPLEMENT); err != nil || strings.Join(f.writes, "|") != "2" {
		t.Fatalf("implement: %v %q", err, f.writes)
	}
	f = &fakeClaude{plan: true}
	if err := a.AnswerPlan(f, pb.PlanChoice_PLAN_CHOICE_KEEP_PLANNING); err != nil || strings.Join(f.writes, "|") != agent.KeyEsc {
		t.Fatalf("keep: %v %q", err, f.writes)
	}
}

// Resuming keeps every value of a variadic option; the prompt goes
// (review C2).
func TestResumeKeepsVariadicValues(t *testing.T) {
	a := New("claude", t.TempDir())
	args := []string{"--disallowedTools", "Write", "Edit", "--add-dir", "/a", "/b", "--mcp-config=/m.json", "--model", "haiku", "--verbose", "--", "fix it"}
	argv, _ := a.Argv(&agent.Launch{Dir: t.TempDir(), SessionID: "x", Resume: true, Args: args, Context: &pb.TurnContext{Model: "claude-haiku-4-5-20251001"}})
	j := strings.Join(argv[4:], " ")
	if j != "--disallowedTools Write Edit --add-dir /a /b --mcp-config=/m.json --verbose --model claude-haiku-4-5-20251001" {
		t.Fatalf("argv %q", j)
	}
	// A prompt given before any option is dropped.
	argv, _ = a.Argv(&agent.Launch{Dir: t.TempDir(), SessionID: "x", Resume: true, Args: []string{"fix it", "--tools", "Bash", "Read"}})
	if j := strings.Join(argv[4:], " "); j != "--tools Bash Read" {
		t.Fatalf("argv %q", j)
	}
}
