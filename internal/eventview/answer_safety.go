package eventview

import (
	"strings"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// AnswerSafety describes the actionable content actually sent to a client.
// ID may differ from the original only for a terminal-only skeleton.
type AnswerSafety struct {
	EventSeq          uint64
	ID                string
	TerminalOnly      bool
	ApprovalTruncated bool
}

// Action fits the event once and derives safety from that exact representation.
func Action(e *pb.Event) (*pb.Event, AnswerSafety) {
	fitted := Fit(e)
	s := AnswerSafety{EventSeq: e.Seq, TerminalOnly: IsSkeleton(fitted)}
	switch k := fitted.Kind.(type) {
	case *pb.Event_ApprovalRequested:
		s.ID = k.ApprovalRequested.ApprovalId
		s.ApprovalTruncated = ApprovalTextTruncated(k.ApprovalRequested)
	case *pb.Event_QuestionAsked:
		s.ID = k.QuestionAsked.QuestionId
	}
	return fitted, s
}

func ApprovalTextTruncated(a *pb.ApprovalRequested) bool {
	for _, text := range []string{a.Description, a.Command, a.ToolInputJson} {
		if strings.HasSuffix(text, TruncatedNote) || strings.HasSuffix(text, SkeletonNote) {
			return true
		}
	}
	return false
}

// TerminalOnlyAnswerID identifies skeleton IDs that are display text, never
// executable request aliases. The same marker in an original ID is treated
// conservatively, just as it is when detecting a skeleton event.
func TerminalOnlyAnswerID(id string) bool {
	return strings.HasSuffix(id, SkeletonNote)
}

// Fit returns the bounded representation published to clients, without
// mutating e. It may return e when no reduction is needed.
func Fit(e *pb.Event) *pb.Event { return fitEvent(slimEvent(e)) }
