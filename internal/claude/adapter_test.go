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
	screen     string
	writes     []string
	mode       int  // index in cycle
	noBypass   bool // bypass not on offer
	noAuto     bool
	typed      string
	menu       []string // model menu labels
	cursor     int
	effort     int // index in efforts
	cached     bool
	confirm    bool
	note       string
	question   *pb.Question
	picked     []int
	ticked     map[int]bool
	review     bool
	plan       bool
	stuck      bool   // s does nothing in the model menu
	wrap       int    // wrap question and plan text after this many characters
	planText   string // the plan prompt as drawn, if set
	background string // appended to the mode line
}

func (f *fakeClaude) wrapped(s string) string {
	if f.wrap == 0 || len(s) <= f.wrap {
		return s
	}
	cut := strings.LastIndex(s[:f.wrap], " ") // terminals wrap Claude Code's text at words
	return s[:cut] + "\n      " + s[cut+1:]
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
		b.WriteString(rule + "\n ☐ H\n" + f.wrapped(f.question.Text) + "\n")
		for i, o := range f.question.Options {
			box := ""
			if f.question.MultiSelect {
				box = "[ ] "
				if f.ticked[i+1] {
					box = "[✔] "
				}
			}
			b.WriteString("  " + string(rune('1'+i)) + ". " + box + o.Label + "\n")
		}
		b.WriteString("Enter to select · ↑/↓ to navigate · Esc to cancel\n")
	case f.plan && f.planText != "":
		b.WriteString(f.planText)
	case f.plan:
		b.WriteString(rule + "\n Claude has written up a plan and is ready to execute. Would you like to proceed?\n" +
			" ❯ 1. Yes, auto-accept edits\n   2. Yes, manually approve edits\n   3. Tell Claude what to change\n")
	default:
		b.WriteString(rule + "\n\x1b[39m❯ " + f.typed + "\x1b[7m \x1b[0m\n" + rule + "\n   Haiku 4.5\n  " + f.modeLine() + f.background + "\n")
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
			if f.stuck {
				return nil
			}
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
			return nil
		}
		n := int(k[0] - '0')
		if f.ticked == nil {
			f.ticked = map[int]bool{}
		}
		f.ticked[n] = !f.ticked[n]
		f.picked = append(f.picked, n)
	case f.plan:
		f.plan = false
	case k != "" && strings.Count(k, agent.KeyBTab)*len(agent.KeyBTab) == len(k):
		for range strings.Count(k, agent.KeyBTab) {
			for {
				f.mode = (f.mode + 1) % len(cycle)
				if (cycle[f.mode] == "bypassPermissions" && f.noBypass) || (cycle[f.mode] == "auto" && f.noAuto) {
					continue
				}
				break
			}
		}
	case k == "/model" || k == "/plan":
		f.typed = k
	case k == agent.KeyEnter && f.typed == "/plan":
		f.typed = ""
		f.mode = index("plan")
	case k == agent.KeyEnter && f.typed == "/model":
		f.typed = ""
		f.menu = []string{"Default (recommended)", "Opus", "Fable", "Sonnet", "Haiku ✔"}
		f.cursor, f.effort = 4, 1
	}
	return nil
}

// Facts the daemon passes: what is known of the modes on offer.
var (
	factsHaiku   = agent.ModeFacts{Model: "claude-haiku-4-5-20251001"}                   // no bypass, no auto
	factsSonnet  = agent.ModeFacts{FullAccess: true, Model: "claude-sonnet-5-5"}         // everything
	factsUnknown = agent.ModeFacts{FullAccess: true}                                     // auto not known
	factsBypass  = agent.ModeFacts{FullAccess: true, Model: "claude-haiku-4-5-20251001"} // bypass, no auto
)

func TestSetMode(t *testing.T) {
	a := New("claude", "")
	f := &fakeClaude{noBypass: true, noAuto: true}
	if err := a.SetMode(f, pb.ApprovalMode_APPROVAL_MODE_PLAN, false, factsHaiku); err != nil || cycle[f.mode] != "plan" {
		t.Fatalf("plan: %v %s", err, cycle[f.mode])
	}
	if err := a.SetMode(f, pb.ApprovalMode_APPROVAL_MODE_ASK, false, factsHaiku); err != nil || cycle[f.mode] != "default" {
		t.Fatalf("ask: %v %s", err, cycle[f.mode])
	}
	// Known not to be on offer: refused without a key.
	n := len(f.writes)
	for _, m := range []pb.ApprovalMode{pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS, pb.ApprovalMode_APPROVAL_MODE_AUTO} {
		if err := a.SetMode(f, m, false, factsHaiku); !errors.Is(err, agent.ErrUnsupported) || len(f.writes) != n || cycle[f.mode] != "default" {
			t.Fatalf("%v: %v %q", m, err, f.writes[n:])
		}
	}
	// Idle, Plan is entered with /plan, passing no other mode.
	f = &fakeClaude{}
	if err := a.SetMode(f, pb.ApprovalMode_APPROVAL_MODE_PLAN, false, factsSonnet); err != nil || cycle[f.mode] != "plan" || strings.Join(f.writes, "|") != "/plan|\r" {
		t.Fatalf("/plan: %v %q", err, f.writes)
	}
	// While a turn runs, never pass through a looser mode (review C1).
	for _, c := range []struct {
		from  string
		to    pb.ApprovalMode
		facts agent.ModeFacts
		ok    bool
	}{
		{"plan", pb.ApprovalMode_APPROVAL_MODE_ASK, factsSonnet, false},        // via bypass and auto
		{"plan", pb.ApprovalMode_APPROVAL_MODE_ASK, factsHaiku, true},          // neither on offer: next
		{"plan", pb.ApprovalMode_APPROVAL_MODE_ASK, factsUnknown, false},       // via bypass
		{"default", pb.ApprovalMode_APPROVAL_MODE_PLAN, factsSonnet, false},    // via accept edits
		{"acceptEdits", pb.ApprovalMode_APPROVAL_MODE_PLAN, factsSonnet, true}, // next
		{"auto", pb.ApprovalMode_APPROVAL_MODE_ASK, factsSonnet, true},         // next
		{"bypassPermissions", pb.ApprovalMode_APPROVAL_MODE_AUTO, factsSonnet, true},
		{"default", pb.ApprovalMode_APPROVAL_MODE_AUTO, factsSonnet, false}, // via bypass
		{"plan", pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS, factsSonnet, true},
	} {
		f = &fakeClaude{noBypass: !c.facts.FullAccess, noAuto: c.facts.Model != "claude-sonnet-5-5"}
		f.mode = index(c.from)
		err := a.SetMode(f, c.to, true, c.facts)
		if c.ok != (err == nil) || !c.ok && (!errors.Is(err, agent.ErrBusy) || len(f.writes) != 0) {
			t.Errorf("running %s → %v with %v: %v %q", c.from, c.to, c.facts, err, f.writes)
		}
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

// Only the real confirmation gets its 1: the same words in the
// conversation, with the menu still up, are not it (review C3).
func TestSetModelConfirmIsTheDialog(t *testing.T) {
	a := New("claude", "")
	f := &fakeClaude{stuck: true, note: "⏺ You asked: Switch model?\n  ❯ 1. Yes, switch to Opus"}
	if err := a.SetModel(f, "sonnet", ""); err == nil {
		t.Fatal("no error for a menu that stayed up")
	}
	for _, w := range f.writes {
		if w == "1" {
			t.Fatalf("pressed %q into the menu: %q", w, f.writes)
		}
	}
}

// Ticks the local user made are kept or cleared to match the answer
// (review C4).
func TestAnswerMultiSelectReconciles(t *testing.T) {
	a := New("claude", "")
	q := &pb.QuestionAsked{QuestionId: "t", Questions: []*pb.Question{{Id: "Which?", Text: "Which?", MultiSelect: true,
		Options: []*pb.QuestionOption{{Label: "A"}, {Label: "B"}, {Label: "C"}}}}}
	f := &fakeClaude{question: q.Questions[0], ticked: map[int]bool{1: true, 2: true}}
	if err := a.Answer(f, q, nil, []int32{1, 3}); err != nil {
		t.Fatal(err)
	}
	if !f.ticked[1] || f.ticked[2] || !f.ticked[3] {
		t.Fatalf("ticked %v after %q", f.ticked, f.writes)
	}
}

// A wrapped question is found without an index; none showing is an
// error, not question zero (review C5).
func TestAnswerFindsWrappedQuestion(t *testing.T) {
	a := New("claude", "")
	long := "Which of these fruits would you like to have for breakfast tomorrow morning?"
	q := &pb.QuestionAsked{QuestionId: "t", Questions: []*pb.Question{
		{Id: "Color?", Text: "Color?", Options: []*pb.QuestionOption{{Label: "Red"}}},
		{Id: long, Text: long, MultiSelect: true, Options: []*pb.QuestionOption{{Label: "A"}, {Label: "B"}}},
	}}
	f := &fakeClaude{question: q.Questions[1], wrap: 30}
	if err := a.Answer(f, q, nil, []int32{1, 2}); err != nil {
		t.Fatal(err)
	}
	if !f.ticked[1] || !f.ticked[2] {
		t.Fatalf("keys %q", f.writes)
	}
	f = &fakeClaude{question: &pb.Question{Text: "Something else?"}}
	if err := a.Answer(f, q, nil, []int32{1}); err == nil || len(f.writes) != 0 {
		t.Fatalf("answered a question not showing: %v %q", err, f.writes)
	}
}

// The plan prompt is recognized and answered when a narrow window wraps
// its title and rows (review C6).
func TestAnswerPlanWrapped(t *testing.T) {
	a := New("claude", "")
	text := rule + "\n Claude has written up a plan and is ready to execute. Would you\n like to proceed?\n" +
		" ❯ 1. Yes, and switch to BYPASS PERMISSIONS (no\n      further prompts) for this session\n   2. Yes, manually\n      approve edits\n   3. Tell Claude what to\n      change\n"
	if !AnalyzeScreen(text).PlanOpen {
		t.Fatal("wrapped plan prompt not recognized")
	}
	narrow := rule + "\n Claude has written up a plan and is ready to execute. Would you like to\n proceed?\n ❯ 1. Yes\n   2. Yes, manually approve\n      edits\n"
	if !AnalyzeScreen(narrow).PlanOpen {
		t.Fatal("title wrapped mid-phrase")
	}
	f := &fakeClaude{plan: true, planText: text}
	if err := a.AnswerPlan(f, pb.PlanChoice_PLAN_CHOICE_IMPLEMENT); err != nil || strings.Join(f.writes, "|") != "2" {
		t.Fatalf("implement: %v %q", err, f.writes)
	}
}

func TestConfirmedEffortUnchanged(t *testing.T) {
	if !confirmed("  ⎿  Set model to Sonnet 5.5 for this session only\n", "Sonnet", "low") {
		t.Fatal("an unchanged effort is not named")
	}
	if confirmed("  ⎿  Set model to Sonnet 5.5 for this session only with high effort\n", "Sonnet", "low") {
		t.Fatal("wrong effort accepted")
	}
}

// A session started without bypass does not resume in bypass because the
// default mode became bypass meanwhile, even with no mode known (C9).
func TestResumeDoesNotTakeUpALaterBypassDefault(t *testing.T) {
	home := t.TempDir()
	a := New("claude", home)
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"permissions":{"defaultMode":"bypassPermissions"}}`), 0o600)
	argv, _ := a.Argv(&agent.Launch{Dir: t.TempDir(), SessionID: "x", Resume: true, FullAccess: false})
	if j := strings.Join(argv, " "); !strings.Contains(j, "--permission-mode default") || strings.Contains(j, "dangerously") {
		t.Fatalf("argv %q", j)
	}
	// With bypass on offer at launch, nothing is forced.
	argv, _ = a.Argv(&agent.Launch{Dir: t.TempDir(), SessionID: "x", Resume: true, FullAccess: true})
	if j := strings.Join(argv, " "); strings.Contains(j, "--permission-mode") {
		t.Fatalf("argv %q", j)
	}
	// Nor when the default is not bypass.
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{}`), 0o600)
	argv, _ = a.Argv(&agent.Launch{Dir: t.TempDir(), SessionID: "x", Resume: true})
	if j := strings.Join(argv, " "); strings.Contains(j, "--permission-mode") {
		t.Fatalf("argv %q", j)
	}
}

// Idle, a way through looser modes is taken in one write of all its
// presses, counted from what is known to be on offer (review C1). Only
// when auto is not known and turns out to be on offer do the missing
// presses follow in a second write. With background work showing it is
// refused.
func TestSetModeIdleBurst(t *testing.T) {
	a := New("claude", "")
	bt := func(n int) string { return strings.Repeat(agent.KeyBTab, n) }
	for _, c := range []struct {
		name   string
		f      *fakeClaude
		from   string
		to     pb.ApprovalMode
		facts  agent.ModeFacts
		writes []string
		end    string
		err    error
	}{
		{"haiku with bypass: plan → ask", &fakeClaude{noAuto: true}, "plan", pb.ApprovalMode_APPROVAL_MODE_ASK, factsBypass, []string{bt(2)}, "default", nil},
		{"sonnet: default → auto", &fakeClaude{}, "default", pb.ApprovalMode_APPROVAL_MODE_AUTO, factsSonnet, []string{bt(4)}, "auto", nil},
		{"sonnet: plan → ask", &fakeClaude{}, "plan", pb.ApprovalMode_APPROVAL_MODE_ASK, factsSonnet, []string{bt(3)}, "default", nil},
		{"auto unknown, on offer: plan → ask", &fakeClaude{}, "plan", pb.ApprovalMode_APPROVAL_MODE_ASK, factsUnknown, []string{bt(2), bt(1)}, "default", nil},
		{"auto unknown, not on offer: plan → ask", &fakeClaude{noAuto: true}, "plan", pb.ApprovalMode_APPROVAL_MODE_ASK, factsUnknown, []string{bt(2)}, "default", nil},
		{"auto unknown, not on offer: default → auto", &fakeClaude{noAuto: true}, "default", pb.ApprovalMode_APPROVAL_MODE_AUTO, factsUnknown, []string{bt(4)}, "default", agent.ErrUnsupported},
	} {
		c.f.mode = index(c.from)
		err := a.SetMode(c.f, c.to, false, c.facts)
		if !errors.Is(err, c.err) || (c.err == nil) != (err == nil) || strings.Join(c.f.writes, "|") != strings.Join(c.writes, "|") || cycle[c.f.mode] != c.end {
			t.Errorf("%s: %v, writes %q, ends in %s", c.name, err, c.f.writes, cycle[c.f.mode])
		}
	}
	// Background work: refused, nothing pressed.
	for _, bg := range []string{" · 1 shell", "\n  ⏺ main\n  ◯ general-purpose  sleep  6s"} {
		f := &fakeClaude{background: bg}
		f.mode = index("plan")
		if err := a.SetMode(f, pb.ApprovalMode_APPROVAL_MODE_ASK, false, factsSonnet); !errors.Is(err, agent.ErrBackground) || len(f.writes) != 0 {
			t.Fatalf("background %q: %v %q", bg, err, f.writes)
		}
	}
	// A safe way needs no burst and ignores background work.
	f := &fakeClaude{background: " · 1 shell"}
	f.mode = index("acceptEdits")
	if err := a.SetMode(f, pb.ApprovalMode_APPROVAL_MODE_PLAN, true, factsSonnet); err != nil || cycle[f.mode] != "plan" {
		t.Fatalf("safe way: %v %s", err, cycle[f.mode])
	}
	// The screen's note settles auto even with no model known.
	if o := offered(factsUnknown, "   Haiku 4.5  │  w   auto mode unavailable for this model"); o["auto"] {
		t.Fatal("the note was not read")
	}
	if o := offered(agent.ModeFacts{Seen: []string{"auto"}, Model: "haiku"}, ""); !o["auto"] {
		t.Fatal("a mode seen on the screen is on offer")
	}
}

func TestBackgroundWork(t *testing.T) {
	base := rule + "\n\x1b[39m❯ \x1b[7m \x1b[0m\n" + rule + "\n   Haiku\n"
	for scr, want := range map[string]string{
		base + "  ⏵⏵ accept edits on · 1 shell · ← for agents\n":                                                             "1 shell",
		"✻ Waiting for 1 background agent to finish\n" + base + "  ⏸ manual mode on\n":                                       "1 background agent",
		base + "  ⏸ manual mode on\n  ⏺ main\n  ◯ general-purpose  task  6s\n":                                               "a background agent",
		"✻ Worked for 5s · done 4:54 PM · 1 shell still running\n" + base + "  ⏸ manual mode on · /tasks to see subagents\n": "",
	} {
		if got := BackgroundWork(scr); got != want {
			t.Errorf("%q: got %q, want %q", scr, got, want)
		}
	}
}
