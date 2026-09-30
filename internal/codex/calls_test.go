package codex

import (
	"testing"
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
