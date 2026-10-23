package eventview

import (
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestSlimEventDropsWhatClientsDoNotShow(t *testing.T) {
	call := &pb.Event{Seq: 7, Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{
		CallId: "c", Title: "go test ./...", Input: "go test ./...", Output: "ok", OutputTruncated: true, Status: "completed",
	}}}
	got := slimEvent(call).GetToolCall()
	if got.Output != "" || got.OutputTruncated || got.Input != "" || got.Title != "go test ./..." || got.CallId != "c" || got.Status != "completed" {
		t.Fatalf("got %v", got)
	}
	if call.GetToolCall().Output != "ok" || call.GetToolCall().Input == "" {
		t.Fatal("the local event was changed")
	}

	// Input that says more than the title, or that clients parse, stays.
	for _, in := range []string{`{"command":"ls"}`, "other"} {
		e := &pb.Event{Kind: &pb.Event_ToolStarted{ToolStarted: &pb.ToolStarted{Title: in, Input: in}}}
		if in == "other" {
			e.GetToolStarted().Title = "title"
		}
		if slimEvent(e) != e {
			t.Fatalf("input %q was dropped", in)
		}
	}
	started := &pb.Event{Kind: &pb.Event_ToolStarted{ToolStarted: &pb.ToolStarted{Title: "ls -la", Input: "ls -la", InputDigest: "d"}}}
	if s := slimEvent(started).GetToolStarted(); s.Input != "" || s.Title != "ls -la" || s.InputDigest != "d" {
		t.Fatalf("got %v", s)
	}

	msg := &pb.Event{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "hi"}}}
	plain := &pb.Event{Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{Title: "t", Input: "{}"}}}
	if slimEvent(msg) != msg || slimEvent(plain) != plain {
		t.Fatal("an event with nothing to drop was copied")
	}
}
