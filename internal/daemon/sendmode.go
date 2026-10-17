package daemon

import (
	"fmt"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// Steer and queue (architecture §6). A steer message is written as soon as
// the input box is free, into the running turn; a queue message waits for
// the turn to end. The queue keeps steer messages ahead of queue messages
// (queueState.insertAt), so the injector only looks at the head. When a
// turn is interrupted, what has not been written is paused until the user
// sends or cancels it.

// gateFallback lets a queue message go when the message written before it
// started no turn (a slash command, say) and nothing runs.
const gateFallback = 15 * time.Second

// holdsLocked reports whether the head message must wait. Callers hold
// s.mu.
func (s *session) holdsLocked(in input) bool {
	if in.writing() {
		return false
	}
	if in.Paused {
		return true
	}
	if !in.queued() {
		return false
	}
	if s.running {
		return true
	}
	return s.turnEnds < s.gate && time.Since(s.gateAt) < gateFallback
}

// poke wakes the injector.
func (s *session) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// afterTurn runs once the end of a turn is logged: queue messages may go,
// and after an interruption, what was not written is paused.
func (s *session) afterTurn() {
	s.mu.Lock()
	aborted := s.aborted
	s.aborted = false
	if aborted {
		var events []*pb.Event
		for i, in := range s.queue.Queue {
			if !in.writing() && !in.Paused {
				s.queue.Queue[i].Paused = true
				events = append(events, pausedState(s.queue.Queue[i]))
			}
		}
		if len(events) > 0 {
			// Keep the safe pause in memory if saving fails. The committed
			// turn end lets a restart retry it; the helper never reports a
			// pause absent from queue.json and logs storage errors centrally.
			_, _ = s.saveQueueLocked(events...)
		}
	}
	held := len(s.queue.Held) > 0
	s.mu.Unlock()
	if held {
		time.AfterFunc(heldSweep, s.sweepHeld)
	}
	s.poke()
}

// pausedState reports a message paused by an interruption.
func pausedState(in input) *pb.Event {
	return &pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{
		RequestId: in.RequestID, Status: pb.InputStatus_INPUT_STATUS_QUEUED, Stage: pb.InputStage_INPUT_STAGE_WAITING,
		Mode: in.Mode, Paused: true, Reason: "interrupted",
	}}}
}

// resumeInputBy sends a paused message now, as a steer.
func (s *session) resumeInputBy(requestID string, origin *pb.Origin) error {
	s.mu.Lock()
	if s.meta.Exited {
		s.mu.Unlock()
		return errExited
	}
	idx := -1
	for i, in := range s.queue.Queue {
		if in.RequestID == requestID {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("queued message %s: %w", requestID, errNotFound)
	}
	in := s.queue.Queue[idx]
	if !in.Paused {
		s.mu.Unlock()
		s.poke()
		return nil
	}
	prev := s.queue.Queue
	rest := append(append([]input{}, prev[:idx]...), prev[idx+1:]...)
	in.Paused, in.Mode = false, pb.SendMode_SEND_MODE_STEER
	s.queue.Queue = rest
	s.queue.insert(in)
	e := withOrigin(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{
		RequestId: requestID, Status: pb.InputStatus_INPUT_STATUS_QUEUED, Stage: pb.InputStage_INPUT_STAGE_WAITING, Mode: pb.SendMode_SEND_MODE_STEER,
	}}}, origin)
	saved, err := s.saveQueueLocked(e)
	if !saved {
		s.queue.Queue = prev
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("message %s already resumed and queued for sending: %w", requestID, err)
	}
	return nil
}
