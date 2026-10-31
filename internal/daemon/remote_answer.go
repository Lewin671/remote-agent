package daemon

import (
	"errors"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/eventview"
	"google.golang.org/protobuf/proto"
)

var errRemoteAnswerTruncated = errors.New("内容被截断，请在终端处理")

type remoteAnswerKind int

const (
	remoteApproval remoteAnswerKind = iota
	remoteQuestion
	remotePlan
)

// pendingAnswerSafety derives prompt bodies and sequences from committed
// original events. Replay rebuilds it before accepting commands. planPending
// also covers successful live keys whose PlanAnswered has not been recorded.
// It retains only bounded fitted bodies for current actionable requests.
// Callers hold s.mu or are the only goroutine restoring the session.
type pendingAnswerSafety struct {
	events    map[uint64]*pb.Event
	planSeq   uint64
	approvals map[string]eventview.AnswerSafety
	questions map[string]eventview.AnswerSafety
	plan      eventview.AnswerSafety
	// Keys succeeded, but the CLI may still show this prompt. Also restored
	// from PlanAnswered; a new proposal or a closed plan settles the claim.
	planPending bool
}

func (p *pendingAnswerSafety) observe(e *pb.Event) {
	if p.events == nil {
		p.events = map[uint64]*pb.Event{}
	}

	switch k := e.Kind.(type) {
	case *pb.Event_ApprovalRequested:
		if p.approvals == nil {
			p.approvals = make(map[string]eventview.AnswerSafety)
		}
		fitted, safety := eventview.Action(e)
		delete(p.events, p.approvals[k.ApprovalRequested.ApprovalId].EventSeq)
		if len(p.events) < 128 {
			p.events[e.Seq] = proto.CloneOf(fitted)
		}
		p.approvals[k.ApprovalRequested.ApprovalId] = safety
	case *pb.Event_ApprovalResolved:
		delete(p.events, p.approvals[k.ApprovalResolved.ApprovalId].EventSeq)
		delete(p.approvals, k.ApprovalResolved.ApprovalId)
	case *pb.Event_QuestionAsked:
		if !k.QuestionAsked.Async {
			if p.questions == nil {
				p.questions = make(map[string]eventview.AnswerSafety)
			}
			fitted, safety := eventview.Action(e)
			delete(p.events, p.questions[k.QuestionAsked.QuestionId].EventSeq)
			if len(p.events) < 128 {
				p.events[e.Seq] = proto.CloneOf(fitted)
			}
			p.questions[k.QuestionAsked.QuestionId] = safety
		}
	case *pb.Event_QuestionAnswered:
		delete(p.events, p.questions[k.QuestionAnswered.QuestionId].EventSeq)
		delete(p.questions, k.QuestionAnswered.QuestionId)
	case *pb.Event_TurnEnded:
		// Codex can still await an approval or show a plan after its turn ends.
		for _, q := range p.questions {
			delete(p.events, q.EventSeq)
		}
		clear(p.questions)
	case *pb.Event_PlanProposed:
		p.planPending = false
		_, p.plan = eventview.Action(e)
		p.planSeq = e.Seq
	case *pb.Event_SessionState:
		if !k.SessionState.PlanOpen {
			p.planSeq = 0
			p.planPending = false
		} else if p.planSeq == 0 && !p.planPending {
			p.planSeq = e.Seq
		}
	case *pb.Event_PlanAnswered:
		p.planPending = true
		p.plan = eventview.AnswerSafety{}
		p.planSeq = 0
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
	var view eventview.AnswerSafety
	switch kind {
	case remoteApproval:
		view = s.answerSafety.approvals[id]
	case remoteQuestion:
		view = s.answerSafety.questions[id]
	case remotePlan:
		if s.answerSafety.planPending {
			return errRemoteAnswerStale
		}
		view = s.answerSafety.plan
	}
	// Rewritten IDs always carry the skeleton suffix. They are display text,
	// never executable aliases, even after settlement or on an ID collision.
	if kind != remotePlan && eventview.TerminalOnlyAnswerID(id) || view.TerminalOnly || allow && view.ApprovalTruncated {
		return errRemoteAnswerTruncated
	}
	if kind == remotePlan {
		if s.answerSafety.planSeq == 0 || origin.ObservedEventSeq != s.answerSafety.planSeq || len(origin.ObservedScreenHash) != 32 {
			return errRemoteAnswerStale
		}
	} else if view.EventSeq == 0 || origin.ObservedEventSeq != view.EventSeq {
		return errRemoteAnswerStale
	}
	return nil
}

// checkRemoteQuestion is checkRemoteAnswer for the index-th question of a
// call (nil: the one showing), refusing an index this daemon has already
// entered while the transcript has not recorded the answer.
func (s *session) checkRemoteQuestion(id string, index *int32, origin *pb.Origin) error {
	if origin.GetKind() == pb.OriginKind_ORIGIN_KIND_CLIENT && s.answeredBy[id].claimed(index) {
		return errRemoteAnswerStale
	}
	return s.checkRemoteAnswer(remoteQuestion, id, false, origin)
}

var errRemoteAnswerStale = errors.New("current prompt was not verified or changed; refresh the owner snapshot")
