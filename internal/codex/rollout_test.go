package codex

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/encoding/prototext"
)

func parseFile(t *testing.T, path string) []*pb.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := NewParser()
	var all []*pb.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		evs, err := p.Parse(sc.Bytes())
		if err != nil {
			t.Fatalf("parse %q: %v", sc.Text(), err)
		}
		all = append(all, evs...)
	}
	return all
}

// kinds renders each event as "<kind> <turn>" for a compact comparison.
func kinds(evs []*pb.Event) []string {
	var out []string
	for _, e := range evs {
		name := string(e.ProtoReflect().WhichOneof(e.ProtoReflect().Descriptor().Oneofs().ByName("kind")).Name())
		out = append(out, name+" "+e.TurnId)
	}
	return out
}

func TestParseBasicRollout(t *testing.T) {
	evs := parseFile(t, filepath.Join("testdata", "rollout-basic.jsonl"))
	want := []string{
		"turn_started turn-1",
		"turn_context turn-1",
		"user_message turn-1",
		"reasoning turn-1",
		"agent_message turn-1",
		"tool_call turn-1",
		"tool_call turn-1",
		"tool_call turn-1",
		"tool_call turn-1",
		"settings_changed turn-1",
		"token_usage ",
		"agent_message turn-1",
		"turn_ended turn-1",
		"turn_started turn-2",
		"question_asked ",
		"question_answered ",
		"question_asked ",
		"turn_ended turn-2",
	}
	got := kinds(evs)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("event kinds:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	check := func(i int, want string) {
		t.Helper()
		e := evs[i]
		e.Time = nil
		got := prototext.MarshalOptions{}.Format(e)
		if norm(got) != norm(want) {
			t.Errorf("event %d:\n%s\nwant:\n%s", i, got, want)
		}
	}
	check(1, `turn_id:"turn-1" turn_context:{model:"gpt-6-sol" effort:"high" approval_policy:"on-request" approvals_reviewer:"auto_review" sandbox:"workspace-write" collaboration_mode:"default"}`)
	check(2, `turn_id:"turn-1" user_message:{text:"List the files"}`)
	check(3, `turn_id:"turn-1" reasoning:{text:"**Listing files**\n\n**Checking sizes**"}`)
	check(5, `turn_id:"turn-1" tool_call:{call_id:"exec-1" kind:TOOL_KIND_COMMAND title:"ls -la" input:"ls -la" output:"a.txt\n" status:"completed" exit_code:0 duration_ms:1250 item_type:"CommandExecution"}`)
	check(6, `turn_id:"turn-1" tool_call:{call_id:"exec-2" kind:TOOL_KIND_FILE_CHANGE title:"/work/demo/b.txt" output:"add /work/demo/b.txt\n@@ -0,0 +1 @@\n+hello\n" status:"completed" item_type:"FileChange"}`)
	check(7, `turn_id:"turn-1" tool_call:{call_id:"call-mcp" kind:TOOL_KIND_MCP title:"docs.search" input:"{\"q\":\"tmux\"}" output:"{\"message\":\"tool call error\"}" status:"failed" duration_ms:5 item_type:"McpToolCall"}`)
	check(8, `turn_id:"turn-1" tool_call:{call_id:"ws-1" kind:TOOL_KIND_WEB_SEARCH title:"tmux control mode" item_type:"Extension:web.search"}`)
	check(10, `token_usage:{input_tokens:2000 cached_input_tokens:1000 output_tokens:50 reasoning_output_tokens:20 total_tokens:2050 last_total_tokens:1530 context_window:760000 primary_limit:{used_percent:35}}`)
	check(12, `turn_id:"turn-1" turn_ended:{outcome:TURN_OUTCOME_COMPLETED duration_ms:3000}`)
	check(14, `question_asked:{question_id:"call-q1" questions:{id:"lang" header:"Language" text:"Which language?" options:{label:"Go (Recommended)" description:"Single binary"} options:{label:"Python" description:"Quick to write"}}}`)
	check(15, `question_answered:{question_id:"call-q1" answers:{question_id:"lang" answers:"Python"}}`)
	check(16, `question_asked:{question_id:"call-q2" questions:{text:"Why change the plan?" options:{label:"Too cold"} options:{label:"Too far"}} async:true}`)
	check(17, `turn_id:"turn-2" turn_ended:{outcome:TURN_OUTCOME_ABORTED reason:"interrupted" duration_ms:7000}`)
	if evs[0].Time == nil {
		t.Error("events should carry the rollout timestamp")
	}
}

func norm(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestTruncate(t *testing.T) {
	s := strings.Repeat("é", MaxOutput) // 2 bytes each
	out, cut := truncate(s)
	if !cut || len(out) > MaxOutput || !strings.HasPrefix(s, out) || len(out)%2 != 0 {
		t.Fatalf("truncate: len %d cut %v", len(out), cut)
	}
}

// TestParseRealRollouts parses every rollout under $RA_REAL_ROLLOUTS, to
// catch format drift after a Codex upgrade. It is skipped by default.
func TestParseRealRollouts(t *testing.T) {
	dir := os.Getenv("RA_REAL_ROLLOUTS")
	if dir == "" {
		t.Skip("set RA_REAL_ROLLOUTS to a directory of rollout files")
	}
	var n int
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && strings.HasSuffix(path, ".jsonl") && strings.Contains(filepath.Base(path), "rollout-") {
			n += len(parseFile(t, path))
		}
		return nil
	})
	t.Logf("parsed %d events", n)
}

// In Plan mode the plan arrives as a Plan item and again as a message
// wrapped in <proposed_plan>; only the plan event is kept.
func TestParsePlan(t *testing.T) {
	p := NewParser()
	evs, err := p.Parse([]byte(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"Plan","id":"t-plan","text":"1. Do it."}}}`))
	if err != nil || len(evs) != 1 || evs[0].GetPlanProposed().GetText() != "1. Do it." {
		t.Fatalf("plan: %v %v", evs, err)
	}
	evs, err = p.Parse([]byte(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"AgentMessage","id":"m","content":[{"text":"<proposed_plan>\n1. Do it.\n</proposed_plan>"}]}}}`))
	if err != nil || len(evs) != 0 {
		t.Fatalf("duplicate plan message: %v %v", evs, err)
	}
}

func TestParseCompaction(t *testing.T) {
	evs, err := NewParser().Parse([]byte(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"ContextCompaction","id":"c"}}}`))
	if err != nil || len(evs) != 1 || !evs[0].GetSettingsChanged().GetCompacted() {
		t.Fatalf("%v %v", evs, err)
	}
}

func TestParseToolStarted(t *testing.T) {
	evs, err := NewParser().Parse([]byte(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c","input":"const r = await tools.exec_command({cmd:\"go test ./... \\\"x\\\"\",\"workdir\":\"/w\"});"}}`))
	if err != nil || len(evs) != 1 || evs[0].GetToolStarted().GetTitle() != `go test ./... "x"` {
		t.Fatalf("%v %v", evs, err)
	}
	evs, _ = NewParser().Parse([]byte(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","input":"*** Begin Patch"}}`))
	if len(evs) != 1 || evs[0].GetToolStarted().GetTitle() != "apply_patch" {
		t.Fatalf("%v", evs)
	}
}

// Only the "codex" limit is the account's usage: a rollout also reports
// base_model_inference, with a window of its own.
func TestTokenCountLimitID(t *testing.T) {
	const info = `"info":{"total_token_usage":{"total_tokens":1},"last_token_usage":{"total_tokens":1},"model_context_window":10}`
	for _, c := range []struct {
		limits string
		want   bool
	}{
		{`{"limit_id":"codex","primary":{"used_percent":21,"window_minutes":10080,"resets_at":1791949426},"plan_type":"prolite"}`, true},
		{`{"primary":{"used_percent":21,"window_minutes":10080,"resets_at":1791949426},"plan_type":"prolite"}`, true},
		{`{"limit_id":"base_model_inference","limit_name":"gpt-reserve","primary":{"used_percent":0,"window_minutes":10080,"resets_at":1792140878},"plan_type":"prolite"}`, false},
	} {
		evs, err := tokenCount([]byte(`{` + info + `,"rate_limits":` + c.limits + `}`))
		if err != nil || len(evs) != 1 {
			t.Fatalf("%s: %v %v", c.limits, evs, err)
		}
		u := evs[0].GetTokenUsage()
		if got := u.PrimaryLimit != nil; got != c.want {
			t.Errorf("%s: primary limit kept = %v, want %v", c.limits, got, c.want)
		}
		if u.TotalTokens != 1 {
			t.Errorf("%s: token usage lost", c.limits)
		}
	}
}
