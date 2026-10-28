package daemon

import (
	"errors"
	"slices"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/eventlog"
)

var errEventScanStop = errors.New("stop event scan")

// scanEventsSince streams from after and lets a callback stop early by
// returning false. The callback runs without the event log's mutex.
func scanEventsSince(log *eventlog.Log, after uint64, fn func(*pb.Event) bool) error {
	_, err := log.EachSince(after, func(e *pb.Event) error {
		if !fn(e) {
			return errEventScanStop
		}
		return nil
	})
	if err == errEventScanStop {
		return nil
	}
	return err
}

// Bringing the queue back in step with the log after a restart. The event
// log and queue.json are written one after the other, so a daemon that
// stops between the two leaves them telling different stories. A session
// is loaded by replaying its log; what the log says about input is noted
// on the way, and the queue is put right before anything is written.

// logged is what the replayed log says about input.
type logged struct {
	// lastEnd is the last turn end.
	lastEnd *pb.Event
	// wanted contains only IDs still in queue.json. Their last state is
	// enough to settle or restore them after a restart.
	wanted map[string]bool
	input  map[string]loggedInput
	// sent keeps queued inputs that may still be written and the bounded
	// recent window whose CLI records may arrive late.
	sent    map[string]*sentInput
	written []string
	// received contains only request IDs held in queue.json.
	received      map[string]bool
	approvals     map[string]bool
	approvalOrder []string
}

type loggedInput struct {
	status pb.InputStatus
	paused bool
	seq    uint64
}

// sentInput is a message as its first InputState describes it.
type sentInput struct {
	text    string
	origin  *pb.Origin
	images  bool
	files   []*pb.SentFile
	written bool
	last    loggedInput
}

func newLogged(q *queueState, recent []string) *logged {
	l := &logged{wanted: map[string]bool{}, input: map[string]loggedInput{}, sent: map[string]*sentInput{}, received: map[string]bool{}, approvals: map[string]bool{}}
	for _, in := range q.Queue {
		if in.RequestID != "" {
			l.wanted[in.RequestID] = true
		}
	}
	for _, in := range q.Held {
		if in.RequestID != "" {
			l.wanted[in.RequestID] = true
		}
	}
	for _, id := range recent {
		if id != "" {
			l.wanted[id] = true
		}
	}
	return l
}

// recentWrittenIDs finds the late-record window without retaining each
// historical request. Only one message can be in the terminal-writing
// stage at a time; the last recentInputs completed IDs are enough because
// restoreRecent truncates to the same window.
func recentWrittenIDs(log *eventlog.Log) ([]string, error) {
	var writing string
	var recent []string
	err := log.Each(func(e *pb.Event) {
		if msg := e.GetUserMessage(); msg != nil {
			if msg.RequestId == writing {
				writing = ""
			}
			for i := 0; i < len(recent); {
				if recent[i] == msg.RequestId {
					copy(recent[i:], recent[i+1:])
					recent = recent[:len(recent)-1]
					continue
				}
				i++
			}
		}
		st := e.GetInputState()
		if st == nil {
			return
		}
		if st.Status == pb.InputStatus_INPUT_STATUS_QUEUED && st.Stage == pb.InputStage_INPUT_STAGE_SUBMITTING {
			writing = st.RequestId
			return
		}
		if st.RequestId != writing {
			return
		}
		switch st.Status {
		case pb.InputStatus_INPUT_STATUS_SUBMITTED, pb.InputStatus_INPUT_STATUS_UNCONFIRMED:
			recent = append(recent, st.RequestId)
			if len(recent) > recentInputs {
				recent = append(recent[:0], recent[1:]...)
			}
			writing = ""
		case pb.InputStatus_INPUT_STATUS_HELD_BY_CLI, pb.InputStatus_INPUT_STATUS_CANCELED,
			pb.InputStatus_INPUT_STATUS_FAILED, pb.InputStatus_INPUT_STATUS_NEEDS_TERMINAL:
			writing = ""
		}
	})
	return recent, err
}

func (l *logged) note(e *pb.Event) {
	switch k := e.Kind.(type) {
	case *pb.Event_ApprovalRequested:
		id := k.ApprovalRequested.ApprovalId
		if !l.approvals[id] {
			l.approvals[id] = true
			l.approvalOrder = append(l.approvalOrder, id)
		}
	case *pb.Event_ApprovalResolved:
		id := k.ApprovalResolved.ApprovalId
		delete(l.approvals, id)
		for i, current := range l.approvalOrder {
			if current == id {
				copy(l.approvalOrder[i:], l.approvalOrder[i+1:])
				l.approvalOrder = l.approvalOrder[:len(l.approvalOrder)-1]
				break
			}
		}
	case *pb.Event_TurnEnded:
		l.lastEnd = e
	case *pb.Event_UserMessage:
		id := k.UserMessage.RequestId
		delete(l.sent, id)
		for i := 0; i < len(l.written); {
			if l.written[i] == id {
				copy(l.written[i:], l.written[i+1:])
				l.written = l.written[:len(l.written)-1]
				continue
			}
			i++
		}
		if l.wanted[id] {
			l.received[id] = true
		}
	case *pb.Event_InputState:
		st := k.InputState
		id := st.RequestId
		state := loggedInput{status: st.Status, paused: st.Paused, seq: e.Seq}
		wanted := l.wanted[id]
		if wanted {
			l.input[id] = state
		}
		in := l.sent[id]
		if in == nil {
			// Outside the saved queue, only a message that reached the
			// terminal's submitting stage can become a late record.
			if !wanted && (st.Status != pb.InputStatus_INPUT_STATUS_QUEUED || st.Stage != pb.InputStage_INPUT_STAGE_SUBMITTING) {
				return
			}
			in = &sentInput{text: st.Text, origin: e.Origin, images: len(st.ImageIds) > 0, files: st.Files}
			l.sent[id] = in
		}
		in.last = state
		switch st.Status {
		case pb.InputStatus_INPUT_STATUS_QUEUED:
			in.written = in.written || st.Stage == pb.InputStage_INPUT_STAGE_SUBMITTING
			if !wanted && !in.written {
				delete(l.sent, id)
			}
		case pb.InputStatus_INPUT_STATUS_HELD_BY_CLI:
			// Kept in the held list.
			if !wanted {
				delete(l.sent, id)
			}
		case pb.InputStatus_INPUT_STATUS_SUBMITTED, pb.InputStatus_INPUT_STATUS_UNCONFIRMED:
			if in.written {
				l.written = append(l.written, id)
				if n := len(l.written) - recentInputs; n > 0 {
					dropped := l.written[0]
					l.written = append(l.written[:0], l.written[n:]...)
					if !l.wanted[dropped] && !slices.Contains(l.written, dropped) {
						delete(l.sent, dropped)
					}
				}
				break
			}
			fallthrough
		default:
			delete(l.sent, id)
		}
	}
}

// restoreRecent rebuilds the memory of written messages the CLI's record
// may still come for: those that left the queue with no user message tied
// to them (Codex may record one long after confirming the submit), and
// those the CLI holds. One that was reported UNCONFIRMED is remembered as
// that too, for its record to confirm it (confirmLate). A message with
// images cannot be rebuilt from the log, which has neither its files nor
// its placeholders.
func (s *session) restoreRecent(l *logged) {
	for _, id := range l.written {
		if in := l.sent[id]; in != nil && !in.images {
			var files []queuedFile
			for _, f := range in.files {
				files = append(files, queuedFile{ID: f.FileId, Name: f.Name, Size: f.SizeBytes, Path: f.Path})
			}
			composed := ""
			if len(files) > 0 {
				composed = composeFiles(in.text, files, s.meta.Agent)
			}
			s.recent = append(s.recent, input{Text: in.text, Files: files, Composed: composed, RequestID: id, Origin: storeOrigin(in.origin)})
		}
	}
	for _, in := range s.queue.Held {
		if !in.received { // its record came already
			s.recent = append(s.recent, in)
		}
	}
	if n := len(s.recent) - recentInputs; n > 0 {
		s.recent = s.recent[n:]
	}
	for _, in := range s.recent {
		if recent := l.sent[in.RequestID]; recent != nil && recent.last.status == pb.InputStatus_INPUT_STATUS_UNCONFIRMED {
			s.unproven[in.RequestID] = true
		}
	}
}

// restoreReceived settles the held messages the model got before a
// previous daemon stopped, or could log their outcome: the log has the
// user message tied to each (held.go, received). They are not taken for
// withdrawn in the terminal later.
func (s *session) restoreReceived(l *logged) {
	found := false
	for i, in := range s.queue.Held {
		if l.received[in.RequestID] {
			s.queue.Held[i].received = true
			found = true
		}
	}
	if found {
		s.settleReceived()
	}
}

// dropSettled removes the messages whose outcome is logged: a previous
// daemon stopped after logging it and before saving the queue without
// them (logOutcomeLocked).
func (s *session) dropSettled(l *logged) {
	settled := func(in input) bool {
		last, ok := l.input[in.RequestID]
		if !ok {
			return false
		}
		switch last.status {
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
	_, _ = s.saveQueueLocked()
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
		last, hasLast := l.input[in.RequestID]
		switch {
		case in.Paused:
			if !hasLast || !last.paused {
				report = append(report, in)
			}
		case interrupted && (!hasLast || last.seq < l.lastEnd.Seq):
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
		var events []*pb.Event
		for _, in := range report {
			events = append(events, pausedState(in))
		}
		// A failed save keeps the memory pause, but must not leave a
		// notification that a later restart mistakes for a persisted pause.
		_, _ = s.saveQueueLocked(events...)
		return
	}
	for _, in := range report {
		_ = s.emit(pausedState(in))
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
