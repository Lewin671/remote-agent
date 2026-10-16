package daemon

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/eventlog"
)

func TestRestoreRecentKeepsUnmatchedLateInputPastMatchedHistory(t *testing.T) {
	log, err := eventlog.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	events := []*pb.Event{
		{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: "queued", Status: pb.InputStatus_INPUT_STATUS_QUEUED, Stage: pb.InputStage_INPUT_STAGE_WAITING, Text: "still queued"}}},
		{Origin: &pb.Origin{DeviceId: "phone"}, Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: "late", Status: pb.InputStatus_INPUT_STATUS_QUEUED, Stage: pb.InputStage_INPUT_STAGE_WAITING, Text: "late message"}}},
		{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: "late", Status: pb.InputStatus_INPUT_STATUS_QUEUED, Stage: pb.InputStage_INPUT_STAGE_SUBMITTING}}},
		{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: "late", Status: pb.InputStatus_INPUT_STATUS_UNCONFIRMED}}},
	}
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("matched-%04d", i)
		events = append(events,
			&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: id, Status: pb.InputStatus_INPUT_STATUS_QUEUED, Stage: pb.InputStage_INPUT_STAGE_WAITING, Text: "already recorded"}}},
			&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: id, Status: pb.InputStatus_INPUT_STATUS_QUEUED, Stage: pb.InputStage_INPUT_STAGE_SUBMITTING}}},
			&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: id, Status: pb.InputStatus_INPUT_STATUS_SUBMITTED}}},
			&pb.Event{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{RequestId: id, Text: "already recorded"}}},
		)
	}
	if err := log.Append("session", events, "", 0); err != nil {
		t.Fatal(err)
	}
	ids, err := recentWrittenIDs(log)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "late" {
		t.Fatalf("unmatched late window = %v, want [late]", ids)
	}

	q := &queueState{Queue: []input{{RequestID: "queued", Text: "still queued"}}}
	seen := newLogged(q, ids)
	if err := log.Each(func(e *pb.Event) { seen.note(e) }); err != nil {
		t.Fatal(err)
	}
	if len(seen.wanted) != 2 || len(seen.input) != 2 || len(seen.sent) != 2 {
		t.Fatalf("recovery retained historical IDs: wanted=%d input=%d sent=%d", len(seen.wanted), len(seen.input), len(seen.sent))
	}
	s := &session{queue: q, unproven: map[string]bool{}}
	s.restoreRecent(seen)
	if len(s.recent) != 1 || s.recent[0].RequestID != "late" || s.recent[0].Text != "late message" || s.recent[0].Origin.DeviceID != "phone" {
		t.Fatalf("late input was not restored: %+v", s.recent)
	}
	if !s.unproven["late"] {
		t.Fatal("the unconfirmed late input can no longer be confirmed")
	}
}

func TestNewSessionHistoryReadFailureLeavesQueueUntouched(t *testing.T) {
	h := newHarness(t, Config{})
	path := filepath.Join(h.dir, "events.jsonl")
	var events []*pb.Event
	for i := 0; i < 300; i++ {
		if i == 0 {
			events = append(events, &pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: "settled", Status: pb.InputStatus_INPUT_STATUS_SUBMITTED}}})
		} else {
			events = append(events, &pb.Event{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "history"}}})
		}
	}
	if err := h.s.log.Append(h.s.meta.ID, events, "", 0); err != nil {
		t.Fatal(err)
	}
	q := &queueState{Queue: []input{{RequestID: "settled", Text: "must remain until history is readable"}}}
	if err := q.save(h.dir); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(h.dir, "queue.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.log.Close(); err != nil {
		t.Fatal(err)
	}
	log, err := eventlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, err := newSession(h.d, h.dir, h.s.meta, h.term, log); err == nil || got != nil {
		log.Close()
		t.Fatalf("session restored from incomplete history: session=%v err=%v", got, err)
	}
	_ = log.Close()
	after, err := os.ReadFile(filepath.Join(h.dir, "queue.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("history read failure changed queue.json: before=%q after=%q", before, after)
	}
}

func TestBindRolloutReadFailureKeepsExistingParserAndReader(t *testing.T) {
	h := newHarness(t, Config{})
	var events []*pb.Event
	for i := 0; i < 300; i++ {
		events = append(events, &pb.Event{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "history"}}})
	}
	if err := h.s.log.Append(h.s.meta.ID, events, "", 0); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.dir, "events.jsonl")
	if err := h.s.log.Close(); err != nil {
		t.Fatal(err)
	}
	log, err := eventlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h.s.log = log
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	oldParser, oldReader, oldPath := h.s.parser, h.s.reader, h.s.meta.RolloutPath
	h.s.bindRollout("codex-session", filepath.Join(h.dir, "rollout.jsonl"))
	if h.s.parser != oldParser || h.s.reader != oldReader || h.s.meta.RolloutPath != oldPath {
		t.Fatal("bindRollout replaced state after an incomplete history replay")
	}
}
