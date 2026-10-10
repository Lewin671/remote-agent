package daemon

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// Messages the CLI holds (architecture §6). During a turn Claude Code
// takes a message into its own queue and gives it to the model when the
// current tool call ends (claude-code-notes §10). Until then it can be
// withdrawn: Up pulls every queued message back into the input box (the
// transcript records "popAll"), and the box can be emptied. The others are
// then written again, which queues them anew.

// heldSweep is how long after a turn ends a held message may stay without
// being received before it is taken to have been removed in the terminal.
const heldSweep = 15 * time.Second

var errHeldChanged = errors.New("the CLI's queue is not what remote-agent expects; sort it out in the terminal")

// heldByCLI reports whether a submitted message is still in the CLI's own
// queue: the CLI recorded it, but no user message for it yet.
func (s *session) heldByCLI(in input) bool {
	if s.ag.Kind() != pb.Agent_AGENT_CLAUDE {
		return false
	}
	s.readRollout()
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	return running && !s.loggedUserMessage(in)
}

// hold moves a written message from the queue to the held list.
func (s *session) hold(in input) {
	s.mu.Lock()
	ok := len(s.queue.Queue) > 0 && s.queue.Queue[0].RequestID == in.RequestID
	if ok {
		prevQueue, prevHeld := s.queue.Queue, s.queue.Held
		s.rememberLocked(s.queue.Queue[0])
		s.queue.Queue = s.queue.Queue[1:]
		s.queue.Held = append(s.queue.Held[:len(s.queue.Held):len(s.queue.Held)], in)
		if err := s.queue.save(s.dir); err != nil {
			s.queue.Queue, s.queue.Held = prevQueue, prevHeld
			ok = false
			slog.Error("save input queue", "session", s.meta.ID, "err", errClass(err))
		}
	}
	s.mu.Unlock()
	if !ok {
		s.finishInput(in, pb.InputStatus_INPUT_STATUS_SUBMITTED, "")
		return
	}
	s.d.uploads.releaseRefs(in.Images, s.meta.ID, in.RequestID)
	s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: in.RequestID, Status: pb.InputStatus_INPUT_STATUS_HELD_BY_CLI}}})
}

// received settles a held message once the model got it.
func (s *session) received(requestID string) {
	if requestID == "" || !s.dropHeld(requestID) {
		return
	}
	s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: requestID, Status: pb.InputStatus_INPUT_STATUS_SUBMITTED}}})
}

// dropHeld removes a message from the held list.
func (s *session) dropHeld(requestID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, in := range s.queue.Held {
		if in.RequestID == requestID {
			prev := s.queue.Held
			s.queue.Held = append(s.queue.Held[:i:i], s.queue.Held[i+1:]...)
			if err := s.queue.save(s.dir); err != nil {
				s.queue.Held = prev
				slog.Error("save input queue", "session", s.meta.ID, "err", errClass(err))
				return false
			}
			return true
		}
	}
	return false
}

// sweepHeld runs a while after a turn ends: a message still held while no
// turn runs was removed in the terminal.
func (s *session) sweepHeld() {
	s.mu.Lock()
	if s.running || len(s.queue.Held) == 0 {
		s.mu.Unlock()
		return
	}
	var ids []string
	for _, in := range s.queue.Held {
		ids = append(ids, in.RequestID)
	}
	s.mu.Unlock()
	for _, id := range ids {
		if s.dropHeld(id) {
			s.emit(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: id, Status: pb.InputStatus_INPUT_STATUS_CANCELED, Reason: "terminal"}}})
		}
	}
}

// cancelHeld withdraws a held message: Up pulls the CLI's queue into the
// input box; when it holds exactly the held messages, it is emptied and
// the others are written again.
func (s *session) cancelHeld(requestID string, origin *pb.Origin) error {
	s.ctlMu.Lock()
	defer s.ctlMu.Unlock()
	s.mu.Lock()
	held := append([]input(nil), s.queue.Held...)
	s.mu.Unlock()
	idx := -1
	for i, in := range held {
		if in.RequestID == requestID {
			idx = i
		}
	}
	if idx < 0 {
		return fmt.Errorf("queued message %s: %w", requestID, errNotFound)
	}
	for i, in := range held {
		if i != idx && len(in.Images) > 0 {
			return errHeldChanged // images cannot be written again as they were
		}
	}
	c, err := s.term.Control()
	if err != nil {
		return err
	}
	defer c.Close()
	l, err := holdLease(c, injectBase)
	if err != nil {
		return err
	}
	defer l.end()
	time.Sleep(settle)
	styled, err := s.term.Capture()
	if err != nil {
		return err
	}
	scr := s.ag.Analyze(styled)
	if !scr.ComposerReady || !scr.DraftEmpty {
		return errHeldChanged
	}
	footer := scr.Footer
	var want []string
	for _, in := range held {
		want = append(want, in.Text)
	}
	if err := c.Write([]byte("\x1b[A")); err != nil {
		return err
	}
	text, ok := s.waitComposer(footer)
	if !ok {
		return errHeldChanged
	}
	if normalizeText(strings.ReplaceAll(text, "\n", " ")) != normalizeText(strings.Join(want, " ")) {
		// Something else was in the CLI's queue (typed at the computer):
		// it is left in the input box for the user.
		slog.Warn("held messages do not match the CLI's queue", "session", s.meta.ID)
		return errHeldChanged
	}
	if !s.emptyBox(c, l, footer, len(held)*4+4) {
		return errHeldChanged
	}
	if !s.dropHeld(requestID) {
		return fmt.Errorf("queued message %s: %w", requestID, errNotFound)
	}
	for i, in := range held {
		if i == idx {
			continue
		}
		if !l.holding() {
			break
		}
		if err := c.Write(s.ag.Paste(in.Text)); err != nil {
			slog.Warn("write held message again", "session", s.meta.ID, "err", errClass(err))
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.emit(withOrigin(&pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{
		RequestId: requestID, Status: pb.InputStatus_INPUT_STATUS_CANCELED,
	}}}, origin))
	return nil
}

// waitComposer waits for the input box to hold text.
func (s *session) waitComposer(footer string) (string, bool) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		styled, err := s.term.Capture()
		if err != nil {
			return "", false
		}
		if text, ok := s.ag.ComposerText(styled, footer); ok && strings.TrimSpace(text) != "" {
			return text, true
		}
	}
	return "", false
}

// emptyBox clears the input box line by line: the clear key empties a
// line, Backspace joins it to the one before.
func (s *session) emptyBox(c control, l *lease, footer string, tries int) bool {
	for range tries {
		if !l.holding() {
			return false
		}
		styled, err := s.term.Capture()
		if err != nil {
			return false
		}
		if s.ag.Analyze(styled).DraftEmpty {
			return true
		}
		if err := c.Write(append(s.ag.ClearInput(), 0x7f)); err != nil {
			return false
		}
		time.Sleep(80 * time.Millisecond)
	}
	styled, err := s.term.Capture()
	return err == nil && s.ag.Analyze(styled).DraftEmpty
}
