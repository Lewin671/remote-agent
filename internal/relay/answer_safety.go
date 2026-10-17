package relay

import (
	"strings"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// AnswerSafety describes the actionable content actually sent to a client.
// ID may differ from the original only for a terminal-only skeleton.
type AnswerSafety struct {
	ID                string
	TerminalOnly      bool
	ApprovalTruncated bool
}

// RemoteAnswerSafety uses the same deterministic reduction as event upload.
// The daemon projects it when an original event is committed or replayed,
// without trusting a client's version or its rendering of warnings.
func RemoteAnswerSafety(e *pb.Event) AnswerSafety {
	fitted := fitEvent(slimEvent(e))
	s := AnswerSafety{TerminalOnly: eventSkeleton(fitted)}
	switch k := fitted.Kind.(type) {
	case *pb.Event_ApprovalRequested:
		s.ID = k.ApprovalRequested.ApprovalId
		s.ApprovalTruncated = approvalTextTruncated(k.ApprovalRequested)
	case *pb.Event_QuestionAsked:
		s.ID = k.QuestionAsked.QuestionId
	}
	return s
}

func approvalTextTruncated(a *pb.ApprovalRequested) bool {
	for _, text := range []string{a.Description, a.Command, a.ToolInputJson} {
		if strings.HasSuffix(text, truncatedNote) || strings.HasSuffix(text, skeletonNote) {
			return true
		}
	}
	return false
}

// TerminalOnlyAnswerID identifies skeleton IDs that are display text, never
// executable request aliases. The same marker in an original ID is treated
// conservatively, just as it is when detecting a skeleton event.
func TerminalOnlyAnswerID(id string) bool {
	return strings.HasSuffix(id, skeletonNote)
}
