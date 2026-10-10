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
}

func (l *logged) note(e *pb.Event) {
	switch k := e.Kind.(type) {
	case *pb.Event_TurnEnded:
		l.lastEnd = e
	case *pb.Event_InputState:
		if l.input == nil {
			l.input = map[string]*pb.Event{}
		}
		l.input[k.InputState.RequestId] = e
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
