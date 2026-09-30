package daemon

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Ordering is derived from logged events, never from relay arrival time.
// It survives daemon restarts by replay and remains inside encrypted metadata.
type sessionOrdering struct {
	approvals                      map[string]*timestamppb.Timestamp
	questions                      map[string]*timestamppb.Timestamp
	planSince, popupSince, endedAt *timestamppb.Timestamp
	popup                          string
}

func (o *sessionOrdering) observe(e *pb.Event) {
	switch k := e.Kind.(type) {
	case *pb.Event_ApprovalRequested:
		if o.approvals == nil {
			o.approvals = make(map[string]*timestamppb.Timestamp)
		}
		if _, exists := o.approvals[k.ApprovalRequested.ApprovalId]; !exists {
			o.approvals[k.ApprovalRequested.ApprovalId] = e.Time
		}
	case *pb.Event_ApprovalResolved:
		delete(o.approvals, k.ApprovalResolved.ApprovalId)
	case *pb.Event_QuestionAsked:
		if !k.QuestionAsked.Async {
			if o.questions == nil {
				o.questions = make(map[string]*timestamppb.Timestamp)
			}
			if _, exists := o.questions[k.QuestionAsked.QuestionId]; !exists {
				o.questions[k.QuestionAsked.QuestionId] = e.Time
			}
		}
	case *pb.Event_QuestionAnswered:
		delete(o.questions, k.QuestionAnswered.QuestionId)
	case *pb.Event_TurnEnded:
		clear(o.approvals)
		clear(o.questions)
	case *pb.Event_SessionState:
		st := k.SessionState
		if st.PlanOpen {
			if o.planSince == nil {
				o.planSince = e.Time
			}
		} else {
			o.planSince = nil
		}
		if st.Status == pb.SessionStatus_SESSION_STATUS_BLOCKED {
			if o.popupSince == nil || o.popup != st.Detail {
				o.popupSince = e.Time
			}
			o.popup = st.Detail
		} else {
			o.popupSince = nil
			o.popup = ""
		}
		if st.Status == pb.SessionStatus_SESSION_STATUS_EXITED {
			if o.endedAt == nil {
				o.endedAt = e.Time
			}
		} else {
			o.endedAt = nil
		}
	}
}

func oldestWait(waits map[string]*timestamppb.Timestamp) *timestamppb.Timestamp {
	var oldest *timestamppb.Timestamp
	for _, at := range waits {
		if at != nil && (oldest == nil || at.AsTime().Before(oldest.AsTime())) {
			oldest = at
		}
	}
	return oldest
}

func (o *sessionOrdering) attentionSince(s *pb.Session) *timestamppb.Timestamp {
	switch {
	case s.Status == pb.SessionStatus_SESSION_STATUS_EXITED:
		return nil
	case s.PendingApprovals > 0:
		return oldestWait(o.approvals)
	case s.PendingQuestions > 0:
		return oldestWait(o.questions)
	case s.PlanOpen:
		return o.planSince
	case s.Status == pb.SessionStatus_SESSION_STATUS_BLOCKED:
		return o.popupSince
	default:
		return nil
	}
}
