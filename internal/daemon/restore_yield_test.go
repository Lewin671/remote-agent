package daemon

import (
	"path/filepath"
	"testing"

	"github.com/Lewin671/remote-agent/internal/eventlog"
)

// An exec that yielded before a restart is still continued by the wait
// that follows: the yield has no event, so the parser reads it again from
// the rollout, up to where reading had come.
func TestRestartKeepsYieldedExec(t *testing.T) {
	h := newHarness(t, Config{})
	h.writeRollout(
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c1","input":"tools.exec_command({cmd:\"go test ./...\"})"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":"Script running with cell ID 7\nWall time 31.0 seconds"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":10},"last_token_usage":{"total_tokens":10},"model_context_window":1000}}}`,
	)
	h.s.bindRollout("codex-1", filepath.Join(h.dir, "rollout.jsonl"))
	before := len(h.events())
	m := h.s.meta
	h.s.log.Close()

	h.writeRollout(
		`{"type":"response_item","payload":{"type":"function_call","name":"wait","call_id":"w1","arguments":"{\"cell_id\":\"7\",\"yield_time_ms\":20000}"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"w1","output":[{"type":"input_text","text":"Script completed\nWall time 11.1 seconds\nOutput:\nok"}]}}`,
	)
	h.open(m)
	h.s.readRollout()
	evs := h.events()[before:]
	if len(evs) != 1 || evs[0].GetToolCall().GetCallId() != "c1" || evs[0].GetToolCall().GetStatus() != "completed" {
		t.Fatalf("events after restart: %v", evs)
	}
}

// The same when the parser is rebuilt without a restart, because events
// could not be logged: the yield is before the saved reading position and
// has no event, so it is read again from the rollout.
func TestFailedAppendKeepsYieldedExec(t *testing.T) {
	h := newHarness(t, Config{})
	h.writeRollout(
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c1","input":"tools.exec_command({cmd:\"go test ./...\"})"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":"Script running with cell ID 7\nWall time 31.0 seconds"}}`,
	)
	h.s.bindRollout("codex-1", filepath.Join(h.dir, "rollout.jsonl"))
	h.s.readRollout()
	before := len(h.events())

	h.s.log.Close() // the disk is full
	h.writeRollout(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":10},"last_token_usage":{"total_tokens":10},"model_context_window":1000}}}`)
	h.s.readRollout()
	log, err := eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	h.s.log = log
	if n := len(h.events()); n != before {
		t.Fatalf("%d events logged while the log could not be written, %d before", n, before)
	}

	h.writeRollout(
		`{"type":"response_item","payload":{"type":"function_call","name":"wait","call_id":"w1","arguments":"{\"cell_id\":\"7\",\"yield_time_ms\":20000}"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"w1","output":[{"type":"input_text","text":"Script completed\nWall time 11.1 seconds\nOutput:\nok"}]}}`,
	)
	h.s.readRollout()
	evs := h.events()[before:]
	if len(evs) != 2 || evs[0].GetTokenUsage() == nil || evs[1].GetToolCall().GetCallId() != "c1" || evs[1].GetToolCall().GetStatus() != "completed" {
		t.Fatalf("events after the log came back: %v", evs)
	}
}
