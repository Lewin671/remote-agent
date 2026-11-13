package eventview

import (
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

// Large display fields, UTF-8, identities and immutability are exercised for
// every event kind by TestFitEventEveryKind.
func TestFitEventKeepsSmallEvent(t *testing.T) {
	small := &pb.Event{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "hi"}}}
	if fitEvent(small) != small {
		t.Fatal("small event changed")
	}
}

func TestFitEventMarksOnlyCutOutput(t *testing.T) {
	e := &pb.Event{Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{Input: strings.Repeat("x", 2<<20), Output: "short"}}}
	got := fitEvent(e).GetToolCall()
	if got.OutputTruncated || got.Output != "short" {
		t.Fatalf("output %q truncated=%v", got.Output, got.OutputTruncated)
	}
	e = &pb.Event{Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{Output: strings.Repeat("x", 2<<20)}}}
	if !fitEvent(e).GetToolCall().OutputTruncated {
		t.Fatal("cut output not marked")
	}
}

func TestFitEventDropsPreviewsFirst(t *testing.T) {
	big := make([]byte, MaxBytes/3)
	e := &pb.Event{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "keep me", Images: []*pb.ImageRef{
		{ImageId: "a", Preview: big}, {ImageId: "b", Preview: big[:100]}, {ImageId: "c", Preview: big}, {ImageId: "d", Preview: big},
	}}}}
	got := fitEvent(e).GetUserMessage()
	if got == nil || got.Text != "keep me" || len(got.Images) != 4 {
		t.Fatalf("message lost: %v", got)
	}
	kept := 0
	for _, r := range got.Images {
		if len(r.Preview) > 0 {
			kept++
		}
	}
	// One large preview goes, the largest first; the small one stays.
	if kept != 3 || len(got.Images[1].Preview) != 100 || proto.Size(fitEvent(e)) > MaxBytes {
		t.Fatalf("kept %d previews", kept)
	}
	if len(e.GetUserMessage().Images[0].Preview) == 0 {
		t.Fatal("the logged event was changed")
	}
}
