package daemon

import (
	"errors"
	"slices"
	"unicode/utf8"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"google.golang.org/protobuf/proto"
)

// LiveSnapshot reads current owner state rather than replaying a relay's event
// history. The enclosing result binds the caller's unpredictable request ID.
func (d *Daemon) LiveSnapshot(id string) (*pb.LiveSnapshot, error) {
	s, err := d.exact(id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	snapshot := &pb.LiveSnapshot{Session: s.infoLocked(), Running: s.running}
	plan := s.planOpen
	appendAction := func(seq uint64) error {
		e := s.answerSafety.events[seq]
		if seq == 0 || e == nil {
			return errors.New("live prompt source unavailable; use native terminal")
		}
		snapshot.Actions = append(snapshot.Actions, proto.CloneOf(e))
		return nil
	}
	for id, a := range s.approvals {
		if !a.claimed {
			if err := appendAction(s.answerSafety.approvals[id].EventSeq); err != nil {
				s.mu.Unlock()
				return nil, err
			}
		}
	}
	for id := range s.questions {
		if err := appendAction(s.answerSafety.questions[id].EventSeq); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	// Current queue contents are re-attested by this fresh owner response,
	// including inputs originally accepted before the security cutover.
	if s.queue != nil {
		if len(s.queue.Queue)+len(s.queue.Held) > 512 {
			s.mu.Unlock()
			return nil, errors.New("too many current inputs for a snapshot; use the native terminal")
		}
		addInput := func(in input, status pb.InputStatus) {
			text := in.Text
			if len(text) > 512 {
				end := 512
				for end > 0 && !utf8.RuneStart(text[end]) {
					end--
				}
				text = text[:end] + "… [内容已截断]"
			}
			state := &pb.InputState{RequestId: in.RequestID, Status: status, Stage: in.Stage, Text: text, Mode: in.Mode, Paused: in.Paused}
			for _, image := range in.Images {
				state.ImageIds = append(state.ImageIds, image.ID)
			}
			snapshot.Actions = append(snapshot.Actions, &pb.Event{SessionId: id, Origin: in.Origin.pb(), Kind: &pb.Event_InputState{InputState: state}})
		}
		for _, in := range s.queue.Queue {
			addInput(in, pb.InputStatus_INPUT_STATUS_QUEUED)
		}
		for _, in := range s.queue.Held {
			addInput(in, pb.InputStatus_INPUT_STATUS_HELD_BY_CLI)
		}
	}
	slices.SortFunc(snapshot.Actions, func(a, b *pb.Event) int {
		if a.Seq < b.Seq {
			return -1
		}
		if a.Seq > b.Seq {
			return 1
		}
		return 0
	})
	s.mu.Unlock()
	if plan {
		err = s.drive(func(t agent.Term, _ bool) error {
			styled, err := t.Capture()
			if err != nil {
				return err
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.planOpen || s.answerSafety.planSeq == 0 {
				return errRemoteAnswerStale
			}
			snapshot.PlanEventSeq = s.answerSafety.planSeq
			snapshot.PlanScreen = agent.StripANSI(styled)
			snapshot.PlanScreenHash = planScreenHash(styled)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return snapshot, nil
}
