package claude

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

func parseFile(t *testing.T, p *Parser, path string) []*pb.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var all []*pb.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		evs, err := p.Parse(sc.Bytes())
		if err != nil {
			t.Fatalf("parse %.200q: %v", sc.Text(), err)
		}
		all = append(all, evs...)
	}
	return all
}

func kind(e *pb.Event) string {
	return string(e.ProtoReflect().WhichOneof(e.ProtoReflect().Descriptor().Oneofs().ByName("kind")).Name())
}

// The fixture is a real Claude Code 2.1.284 session (sanitized): plain
// replies, parallel tools, an interrupt, approvals, questions (single,
// several, multi-select, answered by a hook), plans, a message queued
// mid-turn, /model, /compact, /exit and a resume.
func TestTranscriptFixture(t *testing.T) {
	evs := parseFile(t, NewParser(), "testdata/transcript-2.1.284.jsonl")

	var users []string
	started, ended, aborted := 0, 0, 0
	open := false
	toolStarts := map[string]string{}
	var calls []*pb.ToolCall
	var questions []*pb.QuestionAsked
	answers := map[string][]*pb.QuestionAnswer{}
	var plans []string
	compacted := 0
	var lastUsage *pb.TokenUsage
	var models []string
	for _, e := range evs {
		switch k := e.Kind.(type) {
		case *pb.Event_TurnStarted:
			if open {
				t.Fatalf("turn %s started inside another", e.TurnId)
			}
			open = true
			started++
		case *pb.Event_TurnEnded:
			if !open {
				t.Fatal("turn ended twice")
			}
			open = false
			ended++
			if k.TurnEnded.Outcome == pb.TurnOutcome_TURN_OUTCOME_ABORTED {
				aborted++
			}
		case *pb.Event_UserMessage:
			users = append(users, k.UserMessage.Text)
		case *pb.Event_ToolStarted:
			if k.ToolStarted.CallId == "" {
				t.Fatal("tool start without call ID")
			}
			toolStarts[k.ToolStarted.CallId] = k.ToolStarted.Title
		case *pb.Event_ToolCall:
			calls = append(calls, k.ToolCall)
		case *pb.Event_QuestionAsked:
			questions = append(questions, k.QuestionAsked)
		case *pb.Event_QuestionAnswered:
			answers[k.QuestionAnswered.QuestionId] = k.QuestionAnswered.Answers
		case *pb.Event_PlanProposed:
			plans = append(plans, k.PlanProposed.Text)
		case *pb.Event_SettingsChanged:
			if k.SettingsChanged.Compacted {
				compacted++
			}
		case *pb.Event_TokenUsage:
			lastUsage = k.TokenUsage
		case *pb.Event_TurnContext:
			models = append(models, k.TurnContext.Model+"/"+k.TurnContext.Effort+"/"+k.TurnContext.PermissionMode)
		}
		if e.Time == nil {
			t.Errorf("%s without time", kind(e))
		}
	}

	if started != 20 || ended != 20 || aborted != 5 {
		t.Errorf("turns: %d started, %d ended, %d aborted", started, ended, aborted)
	}
	// What a person typed, including messages taken into a running turn;
	// never slash commands, their output, or Claude Code's own notes.
	if len(users) != 22 || users[0] != "Reply with exactly the word PONG and nothing else." {
		t.Errorf("user messages: %d %q", len(users), users)
	}
	for _, u := range users {
		if strings.HasPrefix(u, "/") || strings.Contains(u, "<command-name>") || strings.Contains(u, "<local-command") ||
			strings.HasPrefix(u, "[") || strings.Contains(u, "being continued") {
			t.Errorf("not a user message: %q", u)
		}
	}
	if !slices.Contains(users, "Also reply QUEUED-OK") {
		t.Error("the queued message is missing")
	}
	if !strings.Contains(users[len(users)-1], "line twelve") {
		t.Errorf("multi-line message: %q", users[len(users)-1])
	}

	// Parallel tools pair by call ID.
	var a, b *pb.ToolCall
	for _, c := range calls {
		if toolStarts[c.CallId] == "" {
			t.Errorf("result %s without a start", c.CallId)
		}
		switch c.Title {
		case "echo A":
			a = c
		case "sleep 3; echo B":
			b = c
		}
	}
	if a == nil || b == nil || a.Output != "A" || b.Output != "B" || a.Kind != pb.ToolKind_TOOL_KIND_COMMAND || a.Status != "completed" {
		t.Fatalf("parallel calls: %v %v", a, b)
	}
	// A denied or interrupted call failed.
	failed := 0
	for _, c := range calls {
		if c.Status == "failed" {
			failed++
		}
	}
	if failed < 3 {
		t.Errorf("failed calls: %d", failed)
	}

	// Questions and their answers, keyed by the question text.
	if len(questions) != 4 {
		t.Fatalf("questions: %d", len(questions))
	}
	q := questions[0]
	if q.Questions[0].Text != "which language should a hello-world script use?" || len(q.Questions[0].Options) != 2 || q.Questions[0].Options[1].Label != "Python" {
		t.Errorf("question %v", q)
	}
	if got := answers[q.QuestionId]; len(got) != 1 || got[0].QuestionId != q.Questions[0].Id || got[0].Answers[0] != "Python" {
		t.Errorf("answer %v", got)
	}
	if got := answers[questions[2].QuestionId]; len(got) != 2 || got[0].Answers[0] != "Blue" || got[1].Answers[0] != "Apple" {
		t.Errorf("two questions: %v", got)
	}
	multi := questions[3]
	if !multi.Questions[0].MultiSelect {
		t.Error("multi-select not recognized")
	}
	if got := answers[multi.QuestionId]; len(got) != 1 || strings.Join(got[0].Answers, "|") != "Apple|Plum" {
		t.Errorf("multi-select answer %v", got)
	}

	if len(plans) != 4 || !strings.Contains(plans[0], "hello.txt") {
		t.Errorf("plans: %d", len(plans))
	}
	if compacted != 1 {
		t.Errorf("compactions: %d", compacted)
	}
	if lastUsage == nil || lastUsage.ContextWindow != 200_000 || lastUsage.LastTotalTokens == 0 || lastUsage.TotalTokens < lastUsage.LastTotalTokens {
		t.Errorf("usage %v", lastUsage)
	}
	// Plan mode answers with Sonnet for the time being; the session's
	// model stays haiku, which resuming keeps.
	if !slices.Contains(models, "claude-haiku-4-5-20251001//plan") || !slices.Contains(models, "claude-haiku-4-5-20251001//default") {
		t.Errorf("contexts %q", models)
	}
	for _, m := range models {
		if strings.Contains(m, "sonnet") {
			t.Errorf("the planning model became the session's: %q", models)
		}
	}
}

// A parser created after a restart pairs results with calls and ends the
// turn it did not see start.
func TestParserReplay(t *testing.T) {
	first := NewParser()
	lines := []string{
		`{"type":"user","promptId":"p1","origin":{"kind":"human"},"permissionMode":"default","message":{"role":"user","content":"hi"},"timestamp":"2026-09-29T10:00:00Z"}`,
		`{"type":"assistant","message":{"id":"m1","model":"claude-haiku-4-5-20251001","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"AskUserQuestion","input":{"questions":[{"question":"Q?","header":"H","options":[{"label":"A"},{"label":"B"}],"multiSelect":false}]}}]},"timestamp":"2026-09-29T10:00:01Z"}`,
	}
	var logged []*pb.Event
	for _, l := range lines {
		evs, err := first.Parse([]byte(l))
		if err != nil {
			t.Fatal(err)
		}
		logged = append(logged, evs...)
	}
	second := NewParser()
	for _, e := range logged {
		second.Replay(e)
	}
	evs, _ := second.Parse([]byte(`{"type":"user","promptId":"p1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"answered"}]},"toolUseResult":{"answers":{"Q?":"B"}},"timestamp":"2026-09-29T10:00:02Z"}`))
	if len(evs) != 1 || evs[0].GetQuestionAnswered().GetAnswers()[0].GetAnswers()[0] != "B" || evs[0].TurnId != "p1" {
		t.Fatalf("answer after replay: %v", evs)
	}
	evs, _ = second.Parse([]byte(`{"type":"system","subtype":"turn_duration","durationMs":5}`))
	if len(evs) != 1 || evs[0].GetTurnEnded() == nil {
		t.Fatalf("end after replay: %v", evs)
	}
	// Sidechain (subagent) records are not the session's conversation.
	evs, _ = second.Parse([]byte(`{"type":"user","isSidechain":true,"origin":{"kind":"human"},"message":{"role":"user","content":"sub"}}`))
	if len(evs) != 0 {
		t.Fatalf("sidechain: %v", evs)
	}
}

func TestContextWindow(t *testing.T) {
	for model, want := range map[string]int64{
		"claude-haiku-4-5-20251001": 200_000, "claude-sonnet-5-5": 1_000_000, "claude-sonnet-4-5-20250929": 200_000,
		"claude-opus-4-8[1m]": 1_000_000, "gpt-6": 0,
	} {
		if got := ContextWindow(model); got != want {
			t.Errorf("%s: %d, want %d", model, got, want)
		}
	}
}

// Questions and plans reported by the PreToolUse hook are not repeated
// when the transcript catches up, and their answers still count.
func TestHookEventsNotRepeated(t *testing.T) {
	a := New("", "")
	p := NewParser()
	q := a.HookEvents(&agent.Hook{HookEventName: "PreToolUse", ToolName: "AskUserQuestion", ToolUseID: "t1",
		ToolInput: []byte(`{"questions":[{"question":"Q?","header":"H","options":[{"label":"A"},{"label":"B"}]}]}`)})
	plan := a.HookEvents(&agent.Hook{HookEventName: "PreToolUse", ToolName: "ExitPlanMode", ToolUseID: "t2", ToolInput: []byte(`{"plan":"# Plan"}`)})
	if p.Reported(q[0]) || p.Reported(plan[0]) {
		t.Fatal("reported before anything was parsed")
	}
	if len(q) != 1 || q[0].GetQuestionAsked().GetQuestions()[0].GetText() != "Q?" || len(plan) != 1 || plan[0].GetPlanProposed().GetText() != "# Plan" {
		t.Fatalf("hook events %v %v", q, plan)
	}
	p.Replay(q[0])
	p.Replay(plan[0])
	lines := []string{
		`{"type":"assistant","message":{"id":"m1","model":"claude-haiku-4-5-20251001","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"AskUserQuestion","input":{"questions":[{"question":"Q?","header":"H","options":[{"label":"A"},{"label":"B"}]}]}}]}}`,
		`{"type":"assistant","message":{"id":"m2","model":"claude-haiku-4-5-20251001","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t2","name":"ExitPlanMode","input":{"plan":"# Plan"}}]}}`,
	}
	for _, l := range lines {
		evs, _ := p.Parse([]byte(l))
		for _, e := range evs {
			if e.GetQuestionAsked() != nil || e.GetPlanProposed() != nil {
				t.Fatalf("repeated %v", e)
			}
		}
	}
	evs, _ := p.Parse([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},"toolUseResult":{"answers":{"Q?":"A"}}}`))
	if len(evs) != 1 || evs[0].GetQuestionAnswered() == nil {
		t.Fatalf("answer %v", evs)
	}
	evs, _ = p.Parse([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"User has approved your plan."}]}}`))
	if len(evs) != 0 {
		t.Fatalf("plan result %v", evs)
	}
}

// The transcript may have the call before the hook reports it; the hook's
// event is then recognized as the same call, also after a restart.
func TestTranscriptBeforeHook(t *testing.T) {
	a := New("", "")
	p := NewParser()
	lines := []string{
		`{"type":"assistant","message":{"id":"m1","model":"claude-haiku-4-5-20251001","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"AskUserQuestion","input":{"questions":[{"question":"Q?","options":[{"label":"A"}]}]}}]}}`,
		`{"type":"assistant","message":{"id":"m2","model":"claude-haiku-4-5-20251001","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t2","name":"ExitPlanMode","input":{"plan":"# Plan"}}]}}`,
	}
	var logged []*pb.Event
	for _, l := range lines {
		evs, _ := p.Parse([]byte(l))
		logged = append(logged, evs...)
	}
	hooks := append(a.HookEvents(&agent.Hook{HookEventName: "PreToolUse", ToolName: "AskUserQuestion", ToolUseID: "t1", ToolInput: []byte(`{"questions":[{"question":"Q?","options":[{"label":"A"}]}]}`)}),
		a.HookEvents(&agent.Hook{HookEventName: "PreToolUse", ToolName: "ExitPlanMode", ToolUseID: "t2", ToolInput: []byte(`{"plan":"# Plan"}`)})...)
	for _, e := range hooks {
		if !p.Reported(e) {
			t.Fatalf("not recognized: %v", e)
		}
	}
	// A parser restored from the log knows them too.
	q := NewParser()
	for _, e := range logged {
		q.Replay(e)
	}
	for _, e := range hooks {
		if !q.Reported(e) {
			t.Fatalf("not recognized after a restart: %v", e)
		}
	}
	// And a plan reported by a hook before a restart is not repeated by the
	// transcript after it.
	r := NewParser()
	r.Replay(hooks[1])
	if evs, _ := r.Parse([]byte(lines[1])); len(evs) != 1 || evs[0].GetPlanProposed() != nil {
		t.Fatalf("plan repeated after restart: %v", evs)
	}
}

// A call still running at a restart is reported in full when it ends.
func TestPendingCallAfterRestart(t *testing.T) {
	p := NewParser()
	evs, _ := p.Parse([]byte(`{"type":"assistant","message":{"id":"m1","model":"claude-haiku-4-5-20251001","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"sleep 30"}}]}}`))
	q := NewParser()
	for _, e := range evs {
		q.Replay(e)
	}
	out, _ := q.Parse([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"done"}]}}`))
	if len(out) != 1 {
		t.Fatalf("events %v", out)
	}
	c := out[0].GetToolCall()
	if c.Kind != pb.ToolKind_TOOL_KIND_COMMAND || c.Input != "sleep 30" || c.ItemType != "Bash" || c.Title != "sleep 30" {
		t.Fatalf("call %v", c)
	}
}

// A restart between two lines of one reply does not count its usage twice.
func TestUsageAfterRestartMidReply(t *testing.T) {
	line := func(block string) []byte {
		return []byte(`{"type":"assistant","message":{"id":"m1","model":"claude-haiku-4-5-20251001","stop_reason":"end_turn","usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":5},"content":[` + block + `]}}`)
	}
	p := NewParser()
	evs, _ := p.Parse(line(`{"type":"thinking","thinking":"hm"}`))
	q := NewParser()
	for _, e := range evs {
		q.Replay(e)
	}
	evs, _ = q.Parse(line(`{"type":"text","text":"hi"}`))
	for _, e := range evs {
		if u := e.GetTokenUsage(); u != nil {
			t.Fatalf("counted again: %v", u)
		}
	}
	// The next reply counts.
	evs, _ = q.Parse([]byte(`{"type":"assistant","message":{"id":"m2","model":"claude-haiku-4-5-20251001","stop_reason":"end_turn","usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":1120,"output_tokens":3},"content":[{"type":"text","text":"x"}]}}`))
	var u *pb.TokenUsage
	for _, e := range evs {
		if e.GetTokenUsage() != nil {
			u = e.GetTokenUsage()
		}
	}
	if u == nil || u.InputTokens != 1110+1121 || u.OutputTokens != 8 {
		t.Fatalf("usage %v", u)
	}
}

// A new reply after a restart counts even when its total equals the last
// one's (review V3).
func TestUsageAfterRestartSameTotal(t *testing.T) {
	reply := func(id string, in, out int) []byte {
		return []byte(fmt.Sprintf(`{"type":"assistant","message":{"id":%q,"model":"claude-haiku-4-5-20251001","stop_reason":"end_turn","usage":{"input_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":%d},"content":[{"type":"text","text":"x"}]}}`, id, in, out))
	}
	p := NewParser()
	evs, _ := p.Parse(reply("m1", 100, 10))
	q := NewParser()
	for _, e := range evs {
		q.Replay(e)
	}
	evs, _ = q.Parse(reply("m2", 105, 5)) // same total, another reply
	var u *pb.TokenUsage
	for _, e := range evs {
		if e.GetTokenUsage() != nil {
			u = e.GetTokenUsage()
		}
	}
	if u == nil || u.InputTokens != 205 || u.OutputTokens != 15 || u.MessageId != "m2" {
		t.Fatalf("usage %v", u)
	}
}

// A model chosen through remote-agent stays the session's model through
// Plan mode (review V1).
func TestSelectedModelThroughPlan(t *testing.T) {
	p := NewParser()
	p.Parse([]byte(`{"type":"user","promptId":"p1","origin":{"kind":"human"},"permissionMode":"default","message":{"role":"user","content":"hi"}}`))
	p.Parse([]byte(`{"type":"assistant","message":{"id":"m1","model":"claude-haiku-4-5-20251001","stop_reason":"end_turn","content":[{"type":"text","text":"x"}]}}`))
	p.Parse([]byte(`{"type":"system","subtype":"turn_duration","durationMs":1}`))
	p.Replay(&pb.Event{Kind: &pb.Event_SettingsChanged{SettingsChanged: &pb.SettingsChanged{Model: "sonnet", Effort: "low"}}})
	var ctx *pb.TurnContext
	for _, l := range []string{
		`{"type":"user","promptId":"p2","origin":{"kind":"human"},"permissionMode":"plan","message":{"role":"user","content":"plan"}}`,
		`{"type":"assistant","message":{"id":"m2","model":"claude-sonnet-5-5","stop_reason":"end_turn","content":[{"type":"text","text":"x"}]},"perTurnEffort":"medium"}`,
	} {
		evs, _ := p.Parse([]byte(l))
		for _, e := range evs {
			if c := e.GetTurnContext(); c != nil {
				ctx = c
			}
		}
	}
	if ctx == nil || ctx.Model != "sonnet" || ctx.Effort != "low" || ctx.PermissionMode != "plan" {
		t.Fatalf("context %v", ctx)
	}
}

// A background task's notice starts a turn Claude Code answers by itself.
func TestTaskNotificationTurn(t *testing.T) {
	p := NewParser()
	evs, _ := p.Parse([]byte(`{"type":"user","promptId":"p9","origin":{"kind":"task-notification","producer":"session-task"},"promptSource":"system","message":{"role":"user","content":"<task-notification>done</task-notification>"}}`))
	if len(evs) == 0 || evs[0].GetTurnStarted() == nil {
		t.Fatalf("events %v", evs)
	}
	for _, e := range evs {
		if e.GetUserMessage() != nil {
			t.Fatal("the notice is not a user message")
		}
	}
	if evs, _ := p.Parse([]byte(`{"type":"system","subtype":"turn_duration","durationMs":5}`)); len(evs) != 1 || evs[0].GetTurnEnded() == nil {
		t.Fatalf("end %v", evs)
	}
}

// A message with a pasted image, sent while a turn ran, is recorded with a
// prompt of blocks rather than a string (2.1.284).
func TestQueuedMessageWithImage(t *testing.T) {
	evs, err := NewParser().Parse([]byte(`{"type":"attachment","attachment":{"type":"queued_command","prompt":[{"type":"text","text":"[Image #5]what color?"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBO"}}],"imagePasteIds":[5],"commandMode":"prompt","origin":{"kind":"human"}}}`))
	if err != nil || len(evs) != 1 {
		t.Fatalf("events: %v %v", evs, err)
	}
	m := evs[0].GetUserMessage()
	if m.GetText() != "[Image #5]what color?" || m.GetImageCount() != 1 {
		t.Errorf("message: %v", m)
	}
}
