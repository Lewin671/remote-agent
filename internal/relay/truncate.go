package relay

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

// maxEventBytes keeps a sealed event well below the server's 1 MiB
// envelope limit. An event the server refuses would stop the session's
// upload for good, since events are uploaded in order.
const maxEventBytes = 768 << 10

const truncatedNote = "\n…（remote-agent：内容过长，已截断）"

// fitEvent returns e, or a copy with its longest texts cut so that it fits
// in maxEventBytes. The local log keeps the full event.
func fitEvent(e *pb.Event) *pb.Event {
	if proto.Size(e) <= maxEventBytes {
		return e
	}
	e = proto.CloneOf(e)
	var fields []*string
	switch k := e.Kind.(type) {
	case *pb.Event_UserMessage:
		fields = []*string{&k.UserMessage.Text}
	case *pb.Event_AgentMessage:
		fields = []*string{&k.AgentMessage.Text}
	case *pb.Event_Reasoning:
		fields = []*string{&k.Reasoning.Text}
	case *pb.Event_ToolCall:
		fields = []*string{&k.ToolCall.Output, &k.ToolCall.Input, &k.ToolCall.Title}
		output := k.ToolCall.Output
		defer func() {
			if k.ToolCall.Output != output {
				k.ToolCall.OutputTruncated = true
			}
		}()
	case *pb.Event_ApprovalRequested:
		fields = []*string{&k.ApprovalRequested.ToolInputJson, &k.ApprovalRequested.Command, &k.ApprovalRequested.Description}
	}
	// Cut each field to an equal share of the budget, longest first.
	budget := maxEventBytes / 2
	for _, f := range fields {
		if len(*f) > budget/len(fields) {
			*f = cutUTF8(*f, budget/len(fields)) + truncatedNote
		}
	}
	if proto.Size(e) > maxEventBytes {
		// Nothing left to cut: say so rather than block the session.
		e.Kind = &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{
			Text: "（remote-agent：这条事件过大，没有上传，请在终端里查看）", Phase: "commentary",
		}}
	}
	return e
}

// cutUTF8 shortens s to at most n bytes without splitting a character.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
