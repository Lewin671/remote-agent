package daemon

import (
	"errors"

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

// pendingAnswerSafety is derived only from committed original events, just
// like the pending questions and approval ordering. Replay rebuilds it before
// the session accepts commands. It retains no history or fitted event bodies.
// Callers hold s.mu or are the only goroutine restoring the session.
type pendingAnswerSafety struct {
	approvals map[string]relay.AnswerSafety
	questions map[string]relay.AnswerSafety
	plan      relay.AnswerSafety
}

func (p *pendingAnswerSafety) observe(e *pb.Event) {
	switch k := e.Kind.(type) {
	case *pb.Event_ApprovalRequested:
		if p.approvals == nil {
			p.approvals = make(map[string]relay.AnswerSafety)
		}
		p.approvals[k.ApprovalRequested.ApprovalId] = relay.RemoteAnswerSafety(e)
	case *pb.Event_ApprovalResolved:
		delete(p.approvals, k.ApprovalResolved.ApprovalId)
	case *pb.Event_QuestionAsked:
		if !k.QuestionAsked.Async {
			if p.questions == nil {
				p.questions = make(map[string]relay.AnswerSafety)
			}
			p.questions[k.QuestionAsked.QuestionId] = relay.RemoteAnswerSafety(e)
		}
	case *pb.Event_QuestionAnswered:
		delete(p.questions, k.QuestionAnswered.QuestionId)
	case *pb.Event_TurnEnded:
		// Codex can still await an approval or show a plan after its turn ends.
		clear(p.questions)
	case *pb.Event_PlanProposed:
		p.plan = relay.RemoteAnswerSafety(e)
	case *pb.Event_PlanAnswered:
		p.plan = relay.AnswerSafety{}
	}
}

// checkRemoteAnswer runs with s.mu held, before claiming an approval or
// sending terminal keys. The request path only looks up pending state; it
// neither reads the event log nor repeats the upload reduction. Local socket
// calls and native terminal input are unaffected.
func (s *session) checkRemoteAnswer(kind remoteAnswerKind, id string, allow bool, origin *pb.Origin) error {
	if origin.GetKind() != pb.OriginKind_ORIGIN_KIND_CLIENT {
		return nil
	}
	var view relay.AnswerSafety
	switch kind {
	case remoteApproval:
		view = s.answerSafety.approvals[id]
	case remoteQuestion:
		view = s.answerSafety.questions[id]
	case remotePlan:
		view = s.answerSafety.plan
	}
	// Rewritten IDs always carry the skeleton suffix. They are display text,
	// never executable aliases, even after settlement or on an ID collision.
	if kind != remotePlan && relay.TerminalOnlyAnswerID(id) || view.TerminalOnly || allow && view.ApprovalTruncated {
		return errRemoteAnswerTruncated
	}
	return nil
}
