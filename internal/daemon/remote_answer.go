package daemon

import (
	"errors"
	"fmt"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/relay"
)

var errRemoteAnswerTruncated = errors.New("内容被截断，请在终端处理")

type remoteAnswerKind int

const (
	remoteApproval remoteAnswerKind = iota
	remoteQuestion
	remotePlan
)

// checkRemoteAnswer runs with s.mu held, before claiming an approval or
// sending terminal keys. Original events are the authority, not a sync cursor
// or a client-supplied warning flag: this also works before upload and after
// restart. Local socket calls and native terminal input are unaffected.
func (s *session) checkRemoteAnswer(kind remoteAnswerKind, id string, allow bool, origin *pb.Origin) error {
	if origin.GetKind() != pb.OriginKind_ORIGIN_KIND_CLIENT {
		return nil
	}
	blocked, rewritten := false, false
	err := s.log.Each(func(e *pb.Event) {
		var originalID string
		switch kind {
		case remoteApproval:
			if a := e.GetApprovalRequested(); a != nil {
				originalID = a.ApprovalId
			} else {
				return
			}
		case remoteQuestion:
			if q := e.GetQuestionAsked(); q != nil {
				originalID = q.QuestionId
			} else {
				return
			}
		case remotePlan:
			if e.GetPlanAnswered() != nil {
				blocked = false
			}
			if e.GetPlanProposed() == nil {
				return
			}
		}
		view := relay.RemoteAnswerSafety(e)
		if kind == remotePlan || originalID == id {
			blocked = view.TerminalOnly || allow && view.ApprovalTruncated
		}
		// A reduced ID is never an alias for an executable request. Reject
		// it explicitly, even if it coincides with another original ID.
		if kind != remotePlan && view.ID != originalID && view.ID == id {
			rewritten = true
		}
	})
	if err != nil {
		return fmt.Errorf("无法核对完整请求，请在终端处理: %w", err)
	}
	if blocked || rewritten {
		return errRemoteAnswerTruncated
	}
	return nil
}
