package daemon

import (
	"log/slog"
	"slices"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// Bringing the queue back in step with the log after a restart. The event
// log and queue.json are written one after the other, so a daemon that
// stops between the two leaves them telling different stories. A session
// is loaded by replaying its log; what the log says about input is noted
// on the way, and the queue is put right before anything is written.

// logged is what the replayed log says about input.
type logged struct {
	// lastEnd is the last turn end.
	lastEnd *pb.Event
	// input is each message's last InputState.
	input map[string]*pb.Event
	// sent are the messages no user message was tied to yet, by request
	// ID, and written the IDs of those that were written and left the
	// queue, in that order.
	sent    map[string]*sentInput
	written []string
}

// sentInput is a message as its first InputState describes it.
type sentInput struct {
	text    string
	origin  *pb.Origin
	images  bool
	written bool
}

func (l *logged) note(e *pb.Event) {
	switch k := e.Kind.(type) {
	case *pb.Event_TurnEnded:
		l.lastEnd = e
	case *pb.Event_UserMessage:
		delete(l.sent, k.UserMessage.RequestId)
	case *pb.Event_InputState:
		st := k.InputState
		if l.input == nil {
			l.input, l.sent = map[string]*pb.Event{}, map[string]*sentInput{}
		}
		if l.input[st.RequestId] == nil {
			l.sent[st.RequestId] = &sentInput{text: st.Text, origin: e.Origin, images: len(st.ImageIds) > 0}
		}
		l.input[st.RequestId] = e
		in := l.sent[st.RequestId]
		if in == nil {
			return
		}
		switch st.Status {
		case pb.InputStatus_INPUT_STATUS_QUEUED:
			in.written = in.written || st.Stage == pb.InputStage_INPUT_STAGE_SUBMITTING
		case pb.InputStatus_INPUT_STATUS_HELD_BY_CLI:
			// Kept in the held list.
		case pb.InputStatus_INPUT_STATUS_SUBMITTED, pb.InputStatus_INPUT_STATUS_UNCONFIRMED:
			if in.written {
				l.written = append(l.written, st.RequestId)
				break
			}
			fallthrough
		default:
			delete(l.sent, st.RequestId)
		}
	}
}

// restoreRecent rebuilds the memory of written messages the CLI's record
// may still come for: those that left the queue with no user message tied
// to them (Codex may record one long after confirming the submit), and
// those the CLI holds. A message with images cannot be rebuilt from the
// log, which has neither its files nor its placeholders.
func (s *session) restoreRecent(l *logged) {
	for _, id := range l.written {
		if in := l.sent[id]; in != nil && !in.images {
			s.recent = append(s.recent, input{Text: in.text, RequestID: id, Origin: storeOrigin(in.origin)})
		}
	}
	s.recent = append(s.recent, s.queue.Held...)
	if n := len(s.recent) - recentInputs; n > 0 {
		s.recent = s.recent[n:]
	}
}

// dropSettled removes the messages whose outcome is logged: a previous
// daemon stopped after logging it and before saving the queue without
// them (logOutcomeLocked).
func (s *session) dropSettled(l *logged) {
	settled := func(in input) bool {
		last := l.input[in.RequestID]
		if last == nil {
			return false
		}
		switch last.GetInputState().Status {
		case pb.InputStatus_INPUT_STATUS_QUEUED, pb.InputStatus_INPUT_STATUS_HELD_BY_CLI:
			return false
		}
		return true
	}
	n := len(s.queue.Queue) + len(s.queue.Held)
	s.queue.Queue = slices.DeleteFunc(s.queue.Queue, settled)
	s.queue.Held = slices.DeleteFunc(s.queue.Held, settled)
	if len(s.queue.Queue)+len(s.queue.Held) == n {
		return
	}
	if err := s.queue.save(s.dir); err != nil {
		slog.Error("save input queue", "session", s.meta.ID, "err", errClass(err))
	}
}

// restorePause finishes what afterTurn was doing when a previous daemon
// stopped. If the last turn was interrupted, a message that waits unpaused
// with nothing logged for it since was neither accepted nor resumed after
// the interruption: the pause was not saved, and it is paused now. A pause
// that was saved but not reported is reported.
func (s *session) restorePause(l *logged) {
	// Replaying sets it for every interrupted turn there ever was; only
	// afterTurn, for the turn that just ended, may act on it.
	s.aborted = false
	if s.meta.Exited {
		return
	}
	interrupted := l.lastEnd != nil && l.lastEnd.GetTurnEnded().Outcome == pb.TurnOutcome_TURN_OUTCOME_ABORTED
	var report []input
	changed := false
	for i, in := range s.queue.Queue {
		if in.writing() {
			continue
		}
		last := l.input[in.RequestID]
		switch {
		case in.Paused:
			if last == nil || !last.GetInputState().Paused {
				report = append(report, in)
			}
		case interrupted && (last == nil || last.Seq < l.lastEnd.Seq):
			s.queue.Queue[i].Paused = true
			report = append(report, s.queue.Queue[i])
			changed = true
		}
	}
	if changed {
		// Nothing waits behind paused messages (queueState.insertAt).
		slices.SortStableFunc(s.queue.Queue, func(a, b input) int {
			return btoi(!a.writing() && a.Paused) - btoi(!b.writing() && b.Paused)
		})
		if err := s.queue.save(s.dir); err != nil {
			// Paused in memory all the same: nothing interrupted is written.
			slog.Error("save input queue", "session", s.meta.ID, "err", errClass(err))
		}
	}
	for _, in := range report {
		s.emit(pausedState(in))
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
