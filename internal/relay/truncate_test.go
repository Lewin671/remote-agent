package relay

import (
	"strings"
	"testing"
	"unicode/utf8"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func texts(e *pb.Event) []string {
	switch k := e.Kind.(type) {
	case *pb.Event_UserMessage:
		return []string{k.UserMessage.Text}
	case *pb.Event_AgentMessage:
		return []string{k.AgentMessage.Text}
	case *pb.Event_ToolCall:
		return []string{k.ToolCall.Input, k.ToolCall.Output}
	case *pb.Event_ApprovalRequested:
		return []string{k.ApprovalRequested.ToolInputJson, k.ApprovalRequested.Command}
	}
	return nil
}

func TestFitEvent(t *testing.T) {
	small := &pb.Event{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "hi"}}}
	if fitEvent(small) != small {
		t.Fatal("small event changed")
	}
	huge := strings.Repeat("长", 1<<20) // 3 MiB
	for _, e := range []*pb.Event{
		{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: huge}}},
		{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: huge}}},
		{Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{Input: huge, Output: huge}}},
		{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ToolInputJson: huge, Command: huge}}},
	} {
		got := fitEvent(e)
		if n := proto.Size(got); n > maxEventBytes {
			t.Fatalf("%T: %d bytes", e.Kind, n)
		}
		if proto.Size(e) < 3<<20 {
			t.Fatal("the original was modified")
		}
		for _, text := range texts(got) {
			if !utf8.ValidString(text) {
				t.Fatalf("%T: invalid UTF-8 after cutting", e.Kind)
			}
			if len(text) > 1000 && !strings.HasSuffix(text, truncatedNote) {
				t.Fatalf("%T: long text without the truncation note", e.Kind)
			}
		}
	}
}

func TestFitEventFallsBack(t *testing.T) {
	var opts []*pb.QuestionOption
	for range 4000 {
		opts = append(opts, &pb.QuestionOption{Label: strings.Repeat("x", 300)})
	}
	e := &pb.Event{Seq: 7, Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{
		QuestionId: "q", Questions: []*pb.Question{{Options: opts}},
	}}}
	got := fitEvent(e)
	if proto.Size(got) > maxEventBytes || got.Seq != 7 || got.GetAgentMessage() == nil {
		t.Fatalf("fallback: %d bytes, %T", proto.Size(got), got.Kind)
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
	big := make([]byte, maxEventBytes/3)
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
	if kept != 3 || len(got.Images[1].Preview) != 100 || proto.Size(fitEvent(e)) > maxEventBytes {
		t.Fatalf("kept %d previews", kept)
	}
	if len(e.GetUserMessage().Images[0].Preview) == 0 {
		t.Fatal("the logged event was changed")
	}
}

// G14: a queued message's long text is cut, not the whole state dropped.
func TestFitEventKeepsInputState(t *testing.T) {
	e := &pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{
		RequestId: "r1", Status: pb.InputStatus_INPUT_STATUS_QUEUED, Text: strings.Repeat("x", 800<<10), ImageIds: []string{"a", "b"},
	}}}
	st := fitEvent(e).GetInputState()
	if st == nil || st.RequestId != "r1" || len(st.ImageIds) != 2 || len(st.Text) >= 800<<10 || proto.Size(fitEvent(e)) > maxEventBytes {
		t.Fatalf("got %v", fitEvent(e).Kind)
	}
	g := &pb.Event{Kind: &pb.Event_ImageGenerated{ImageGenerated: &pb.ImageGenerated{CallId: "c", RevisedPrompt: strings.Repeat("p", 900<<10), Images: []*pb.ImageRef{{ImageId: "i"}}}}}
	if got := fitEvent(g).GetImageGenerated(); got == nil || len(got.Images) != 1 || got.CallId != "c" {
		t.Fatalf("generated: %v", fitEvent(g).Kind)
	}
}
