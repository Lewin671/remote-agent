package antigravity

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

func TestTranscriptParserFull(t *testing.T) {
	path := filepath.Join("testdata", "transcript", "transcript.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open transcript fixture: %v", err)
	}
	defer f.Close()

	p := NewParser()
	p.SetTranscriptPath(path)

	var allEvents []*pb.Event
	var questions []*pb.QuestionAsked
	var answers []*pb.QuestionAnswered
	var plans []*pb.PlanProposed
	var toolCalls []*pb.ToolCall
	var compactEvents []*pb.Event

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		evs, err := p.Parse(line)
		if err != nil {
			t.Fatalf("parse error: %v on line: %s", err, string(line))
		}
		allEvents = append(allEvents, evs...)
		for _, e := range evs {
			switch k := e.Kind.(type) {
			case *pb.Event_QuestionAsked:
				t.Logf("Got QuestionAsked: %s %+v", k.QuestionAsked.QuestionId, k.QuestionAsked.Questions)
				questions = append(questions, k.QuestionAsked)
			case *pb.Event_QuestionAnswered:
				t.Logf("Got QuestionAnswered: %s %+v", k.QuestionAnswered.QuestionId, k.QuestionAnswered.Answers)
				answers = append(answers, k.QuestionAnswered)
			case *pb.Event_PlanProposed:
				plans = append(plans, k.PlanProposed)
			case *pb.Event_ToolCall:
				toolCalls = append(toolCalls, k.ToolCall)
			case *pb.Event_SettingsChanged:
				if k.SettingsChanged.Compacted {
					compactEvents = append(compactEvents, e)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}

	if len(allEvents) == 0 {
		t.Fatal("expected events, got none")
	}

	// 1. Verify questions & answers
	if len(questions) < 1 {
		t.Fatalf("expected at least 1 QuestionAsked, got %d", len(questions))
	}
	firstQ := questions[0]
	if len(firstQ.Questions) == 0 || firstQ.Questions[0].Text != "What is your favorite fruit?" {
		t.Errorf("unexpected first question: %+v", firstQ)
	}
	if len(answers) < 1 {
		t.Fatalf("expected at least 1 QuestionAnswered, got %d", len(answers))
	}
	firstAns := answers[0]
	if len(firstAns.Answers) == 0 || len(firstAns.Answers[0].Answers) == 0 || firstAns.Answers[0].Answers[0] != "Banana" {
		t.Errorf("unexpected first answer: %+v", firstAns)
	}

	// 2. Verify plans
	if len(plans) < 1 {
		t.Fatalf("expected at least 1 PlanProposed, got %d", len(plans))
	}
	if plans[0].Text == "" {
		t.Errorf("expected non-empty plan text, got empty")
	}

	// 3. Verify compaction
	if len(compactEvents) < 1 {
		t.Fatalf("expected at least 1 compaction event, got %d", len(compactEvents))
	}

	// 4. Verify tool calls
	if len(toolCalls) < 1 {
		t.Fatalf("expected tool calls, got %d", len(toolCalls))
	}

	// 5. Verify replay
	p2 := NewParser()
	for _, e := range allEvents {
		p2.Replay(e)
	}
	if p2.usage == nil || p2.usage.TotalTokens == 0 {
		t.Errorf("expected usage to be restored after replay, got %+v", p2.usage)
	}
}

func TestSubmitLogAndSubmitted(t *testing.T) {
	a := New("/usr/local/bin/agy", "/tmp/antigravity-home")
	if a.SubmitLog("") != "/tmp/antigravity-home/history.jsonl" {
		t.Errorf("unexpected submit log: %s", a.SubmitLog(""))
	}

	// Prompt without conversationId (first message)
	line1 := []byte(`{"display":"hello world","timestamp":1730650000000,"workspace":"/workspace"}`)
	if a.Submitted(line1, func(s string) bool { return s == "hello world" }, "conv-123") {
		t.Errorf("CID-less global history must not confirm the first prompt")
	}

	// Prompt with matching conversationId
	line2 := []byte(`{"display":"second prompt","conversationId":"conv-123","timestamp":1730650010000}`)
	if !a.Submitted(line2, func(s string) bool { return s == "second prompt" }, "conv-123") {
		t.Errorf("expected match for matching conversationId")
	}

	// Prompt with different conversationId
	line3 := []byte(`{"display":"other prompt","conversationId":"conv-999","timestamp":1730650020000}`)
	if a.Submitted(line3, func(s string) bool { return s == "other prompt" }, "conv-123") {
		t.Errorf("expected rejection for different conversationId")
	}
}

func TestMatchApproval(t *testing.T) {
	a := New("/usr/local/bin/agy", "")
	screenPath := filepath.Join("testdata", "screen", "permission-prompt.txt")
	styledBytes, err := os.ReadFile(screenPath)
	if err != nil {
		t.Fatalf("read permission-prompt screen: %v", err)
	}
	styled := string(styledBytes)

	// Matching hook
	matchingHook := &agent.Hook{
		ToolName:  "run_command",
		ToolInput: []byte(`{"CommandLine":"echo hello_perm_test","toolSummary":"Run echo hello_perm_test"}`),
	}
	req, ok := a.MatchApproval(styled, matchingHook)
	if !ok || req == nil {
		t.Fatalf("expected MatchApproval to succeed, got ok=%v req=%v", ok, req)
	}
	if req.Command != "echo hello_perm_test" {
		t.Errorf("expected command 'echo hello_perm_test', got %q", req.Command)
	}
	if req.Description != "Run echo hello_perm_test" {
		t.Errorf("expected description 'Run echo hello_perm_test', got %q", req.Description)
	}

	// Non-matching hook
	otherHook := &agent.Hook{
		ToolName:  "run_command",
		ToolInput: []byte(`{"CommandLine":"git push origin main"}`),
	}
	if _, ok := a.MatchApproval(styled, otherHook); ok {
		t.Errorf("expected MatchApproval to reject non-matching command")
	}
}

type fakeTerm struct {
	written []byte
	screen  string
	onWrite func([]byte)
}

func (f *fakeTerm) Write(b []byte) error {
	f.written = append(f.written, b...)
	if f.onWrite != nil {
		f.onWrite(b)
	}
	return nil
}
func (f *fakeTerm) Capture() (string, error) { return f.screen, nil }
func (f *fakeTerm) Context() context.Context { return context.Background() }

func TestAnswerApproval(t *testing.T) {
	a := New("/usr/local/bin/agy", "")
	term := &fakeTerm{screen: readScreenFixture(t, "permission-prompt.txt")}
	h := &agent.Hook{ToolName: "run_command", ToolInput: []byte(`{"CommandLine":"echo hello_perm_test"}`)}

	if err := a.AnswerApproval(term, h, true, func() bool { return true }); err != nil {
		t.Fatalf("AnswerApproval allow error: %v", err)
	}
	if string(term.written) != "1" {
		t.Errorf("expected '1' for allow, got %q", string(term.written))
	}

	term.written = nil
	if err := a.AnswerApproval(term, h, false, func() bool { return true }); err != nil {
		t.Fatalf("AnswerApproval deny error: %v", err)
	}
	if string(term.written) != "4" {
		t.Errorf("expected '4' for deny, got %q", string(term.written))
	}
}

func TestHookEvents(t *testing.T) {
	a := New("/usr/local/bin/agy", "")
	hook := &agent.Hook{
		HookEventName: "PreToolUse",
		ToolName:      "ask_question",
		ToolUseID:     "call_q1",
		ToolInput:     []byte(`{"questions":[{"question":"Choose flavor","options":["Vanilla","Chocolate"],"is_multi_select":false}]}`),
	}
	evs := a.HookEvents(hook)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	q := evs[0].GetQuestionAsked()
	if q == nil || len(q.Questions) != 1 || q.Questions[0].Text != "Choose flavor" {
		t.Errorf("unexpected QuestionAsked event: %+v", evs[0])
	}
}
