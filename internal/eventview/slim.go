package eventview

import (
	"strings"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

// slimEvent returns e, or a copy without what no client shows. Clients
// draw a tool call as one line and never its output (architecture §13),
// and history is downloaded whole over a slow link, so these bytes are
// the bulk of what opening a long session waits for. The local log keeps
// the full event.
func slimEvent(e *pb.Event) *pb.Event {
	switch k := e.Kind.(type) {
	case *pb.Event_ToolCall:
		t := k.ToolCall
		if t.Output == "" && !sameAsTitle(t.Input, t.Title) {
			return e
		}
		e = proto.CloneOf(e)
		t = e.GetToolCall()
		t.Output, t.OutputTruncated = "", false
		if sameAsTitle(t.Input, t.Title) {
			t.Input = ""
		}
	case *pb.Event_ToolStarted:
		if !sameAsTitle(k.ToolStarted.Input, k.ToolStarted.Title) {
			return e
		}
		e = proto.CloneOf(e)
		e.GetToolStarted().Input = ""
	}
	return e
}

// sameAsTitle reports whether a tool's input only repeats its title, as a
// shell command's does. Clients fall back to the title when there is no
// input; input that is JSON is kept, since they read a command out of it.
func sameAsTitle(input, title string) bool {
	return input != "" && input == title && !strings.HasPrefix(input, "{")
}
