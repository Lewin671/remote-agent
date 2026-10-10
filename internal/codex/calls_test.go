package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// Every started call ends with a ToolCall of the same ID: from the item
// that reports what it did, or from its output when no item does; a wait
// on a yielded exec cell continues that exec (sequences from 0.159.0
// rollouts).
func TestCallsPairByID(t *testing.T) {
	p := NewParser()
	var starts, ends []string
	feed := func(line string) {
		t.Helper()
		evs, err := p.Parse([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if s := e.GetToolStarted(); s != nil {
				starts = append(starts, s.CallId)
			}
			if c := e.GetToolCall(); c != nil {
				ends = append(ends, c.CallId+":"+c.Status)
			}
		}
	}
	// An exec with a command item.
	feed(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c1","input":"tools.exec_command({cmd:\"sleep 20\"})"}}`)
	feed(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"CommandExecution","id":"exec-1","command":["/bin/zsh","-lc","sleep 20"],"status":"completed"}}}`)
	feed(`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":[{"type":"input_text","text":"Script completed"}]}}`)
	// An exec without an item.
	feed(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c2","input":"tools.exec_command({cmd:\"touch x\"})"}}`)
	feed(`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c2","output":[{"type":"input_text","text":"Script completed\nOutput:\n"}]}}`)
	// An exec that yields, two waits, then fails.
	feed(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c3","input":"tools.exec_command({cmd:\"touch y\"})"}}`)
	feed(`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c3","output":"Script running with cell ID 6\nWall time 31.0 seconds"}}`)
	feed(`{"type":"response_item","payload":{"type":"function_call","name":"wait","call_id":"w1","arguments":"{\"cell_id\":\"6\",\"yield_time_ms\":20000}"}}`)
	feed(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"w1","output":"Script running with cell ID 6\nWall time 21.0 seconds"}}`)
	feed(`{"type":"response_item","payload":{"type":"function_call","name":"wait","call_id":"w2","arguments":"{\"cell_id\":\"6\",\"yield_time_ms\":20000}"}}`)
	feed(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"w2","output":[{"type":"input_text","text":"Script failed\nWall time 11.1 seconds"}]}}`)
	want := []string{"c1", "c2", "c3"}
	if len(starts) != 3 || starts[0] != want[0] || starts[1] != want[1] || starts[2] != want[2] {
		t.Fatalf("starts %v", starts)
	}
	if len(ends) != 3 || ends[0] != "c1:completed" || ends[1] != "c2:completed" || ends[2] != "c3:failed" {
		t.Fatalf("ends %v", ends)
	}
}

// An exec cell still running when the turn ends is reported as gone to
// the background.
func TestYieldedExecAtTurnEnd(t *testing.T) {
	p := NewParser()
	p.Parse([]byte(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c1","input":"tools.exec_command({cmd:\"npm run dev\"})"}}`))
	p.Parse([]byte(`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":"Script running with cell ID 3"}}`))
	evs, _ := p.Parse([]byte(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t"}}`))
	if len(evs) != 2 || evs[0].GetToolCall().GetBackground() != pb.ToolBackground_TOOL_BACKGROUND_STILL_RUNNING || evs[0].GetToolCall().CallId != "c1" {
		t.Fatalf("events %v", evs)
	}
}

// Notifications precede a yielded cell and its eventual result. They must
// neither finish the call nor prevent a wait from continuing the same call.
func TestExecNotificationsDoNotFinishCall(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprintf("replay=%v", replay), func(t *testing.T) {
			p := NewParser()
			feed := func(line string) []*pb.Event {
				t.Helper()
				evs, err := p.Parse([]byte(line))
				if err != nil {
					t.Fatal(err)
				}
				return evs
			}
			starts := feed(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c1","input":"notify(123); await slowTool()"}}`)
			if replay {
				p = NewParser()
				p.Replay(starts[0])
			}
			for _, text := range []string{"123", ""} {
				evs := feed(fmt.Sprintf(`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":%q}}`, text))
				if len(evs) != 0 || p.calls["c1"] == nil {
					t.Fatalf("notification ended call: %v", evs)
				}
			}
			feed(`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":"Script running with cell ID 7"}}`)
			evs := feed(`{"type":"response_item","payload":{"type":"function_call","name":"wait","call_id":"w1","arguments":"{\"cell_id\":\"7\"}"}}`)
			if len(evs) != 0 {
				t.Fatalf("wait became a new call: %v", evs)
			}
			evs = feed(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"w1","output":"progress"}}`)
			if len(evs) != 0 || p.calls["c1"] == nil {
				t.Fatal("wait notification ended call")
			}
			// A later output on the same wait still belongs to the original exec.
			evs = feed(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"w1","output":"Script completed\nOutput:\nDone"}}`)
			if len(evs) != 1 || evs[0].GetToolCall().GetCallId() != "c1" || evs[0].GetToolCall().GetStatus() != "completed" {
				t.Fatalf("completion %v", evs)
			}
		})
	}
}

func TestPlainFunctionResultFinishesCall(t *testing.T) {
	p := NewParser()
	p.Parse([]byte(`{"type":"response_item","payload":{"type":"function_call","name":"sleep","call_id":"c1","arguments":"{}"}}`))
	evs, err := p.Parse([]byte(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"Slept"}}`))
	if err != nil || len(evs) != 1 || evs[0].GetToolCall().GetCallId() != "c1" {
		t.Fatalf("events %v, err %v", evs, err)
	}
}

// A restart after an exec yielded: the yield itself has no event, and the
// token count after it moved the saved reading position past its record,
// so only the rollout still says which call cell 7 is. The wait that
// follows continues that call, as without a restart (records as 0.159.0
// writes them).
func TestYieldedExecAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	var file []byte
	var logged []*pb.Event
	p := NewParser()
	feed := func(line string) []*pb.Event {
		t.Helper()
		file = append(file, line+"\n"...)
		if err := os.WriteFile(path, file, 0o600); err != nil {
			t.Fatal(err)
		}
		evs, err := p.Parse([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		logged = append(logged, evs...)
		return evs
	}
	restart := func() {
		t.Helper()
		p = NewParser()
		for _, e := range logged {
			p.Replay(e)
		}
		if err := p.Restore(path, int64(len(file))); err != nil {
			t.Fatal(err)
		}
	}
	const usage = `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":10},"last_token_usage":{"total_tokens":10},"model_context_window":1000}}}`
	feed(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t0"}}`)
	// An earlier turn's cell with the same number is over with its turn.
	feed(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c0","input":"tools.exec_command({cmd:\"npm run dev\"})"}}`)
	feed(`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c0","output":"Script running with cell ID 7"}}`)
	feed(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t0"}}`)
	feed(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`)
	feed(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c1","input":"tools.exec_command({cmd:\"go test ./...\"})"}}`)
	feed(`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":"Script running with cell ID 7\nWall time 31.0 seconds"}}`)
	if evs := feed(usage); len(evs) != 1 {
		t.Fatalf("token count: %v", evs)
	}
	restart()
	if evs := feed(`{"type":"response_item","payload":{"type":"function_call","name":"wait","call_id":"w1","arguments":"{\"cell_id\":\"7\",\"yield_time_ms\":20000}"}}`); len(evs) != 0 {
		t.Fatalf("wait became a call of its own: %v", evs)
	}
	feed(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"w1","output":"Script running with cell ID 7\nWall time 21.0 seconds"}}`)
	feed(usage)
	// And again, with the wait itself before the restart.
	feed(`{"type":"response_item","payload":{"type":"function_call","name":"wait","call_id":"w2","arguments":"{\"cell_id\":\"7\",\"yield_time_ms\":20000}"}}`)
	feed(usage)
	restart()
	evs := feed(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"w2","output":[{"type":"input_text","text":"Script completed\nWall time 11.1 seconds\nOutput:\nok"}]}}`)
	if len(evs) != 1 || evs[0].GetToolCall().GetCallId() != "c1" || evs[0].GetToolCall().GetStatus() != "completed" {
		t.Fatalf("completion %v", evs)
	}
	if len(p.calls) != 0 || len(p.cells) != 0 || len(p.waits) != 0 {
		t.Fatalf("left over: calls %v, cells %v, waits %v", p.calls, p.cells, p.waits)
	}
	// With no call open there is nothing to read the rollout for.
	restart()
	if err := NewParser().Restore(filepath.Join(t.TempDir(), "none.jsonl"), 100); err != nil {
		t.Fatal(err)
	}
}
