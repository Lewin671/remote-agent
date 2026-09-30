package daemon

import (
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// Operations that drive a CLI's menus. The daemon takes the control lock,
// holds local keys and pauses the status check; the adapter does the
// steps, checking the screen before each and leaving unexpected screens
// with Esc.

var (
	errBusy     = agent.ErrBusy
	errNotReady = agent.ErrNotReady
	errNoPlan   = agent.ErrNoPlan
)

// heldTerm is the terminal while an operation holds it.
type heldTerm struct {
	term terminal
	c    control
}

func (h heldTerm) Capture() (string, error) { return h.term.Capture() }
func (h heldTerm) Write(b []byte) error     { return h.c.Write(b) }

// drive runs op with the terminal held for it. The status check is paused
// meanwhile so that the menus it opens are not reported as popups.
//
// op is told whether the CLI is busy as found once the terminal is held,
// not before: an injection that held it until then may have started a
// turn. Busy means a turn in the transcript, a message written and not yet
// confirmed, or the screen showing the CLI at work.
func (s *session) drive(op func(t agent.Term, busy bool) error) error {
	s.mu.Lock()
	exited := s.meta.Exited
	s.mu.Unlock()
	if exited {
		return errExited
	}
	s.ctlMu.Lock()
	defer s.ctlMu.Unlock()
	c, err := s.term.Control()
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Hold(holdFor); err != nil {
		return err
	}
	defer c.Release()
	// A hold is bounded so that a stuck daemon cannot swallow local keys;
	// renew it while the operation runs.
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(holdFor / 2)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = c.Hold(holdFor)
			}
		}
	}()
	s.mu.Lock()
	s.driving = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.driving = false
		s.mu.Unlock()
	}()
	time.Sleep(settle)
	return op(heldTerm{s.term, c}, s.busyNow())
}

// busyNow tells whether a turn runs or is starting; the caller holds the
// terminal, so no injection happens meanwhile.
func (s *session) busyNow() bool {
	s.readRollout()
	s.mu.Lock()
	busy := s.running || len(s.queue.Queue) > 0 && s.queue.Queue[0].Inflight != nil
	s.mu.Unlock()
	if busy {
		return true
	}
	scr, _, err := s.screenNow()
	return err != nil || scr.Busy
}

// screenNow captures and analyzes the terminal.
func (s *session) screenNow() (agent.Screen, string, error) {
	styled, err := s.term.Capture()
	if err != nil {
		return agent.Screen{}, "", err
	}
	return s.ag.Analyze(styled), agent.StripANSI(styled), nil
}

func (s *session) isRunning() bool {
	s.readRollout()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *session) isRunningNoRead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// setModel switches the model and effort for this session only, while no
// turn is running.
func (s *session) setModel(model, effort string) error {
	return s.setModelBy(model, effort, originLocal)
}

// setModelBy is setModel on behalf of origin.
func (s *session) setModelBy(model, effort string, origin *pb.Origin) error {
	if err := s.ag.CheckEffort(effort); err != nil {
		return err
	}
	if s.isRunning() {
		return errBusy
	}
	if err := s.drive(func(t agent.Term, busy bool) error {
		if busy {
			return errBusy
		}
		return s.ag.SetModel(t, model, effort)
	}); err != nil {
		return err
	}
	s.emit(withOrigin(&pb.Event{Kind: &pb.Event_SettingsChanged{SettingsChanged: &pb.SettingsChanged{Model: model, Effort: effort}}}, origin))
	return nil
}

// setApprovalMode switches the approval mode.
func (s *session) setApprovalMode(mode pb.ApprovalMode) error {
	return s.setApprovalModeBy(mode, originLocal)
}

// setApprovalModeBy is setApprovalMode on behalf of origin.
func (s *session) setApprovalModeBy(mode pb.ApprovalMode, origin *pb.Origin) error {
	// The facts too are read with the terminal held: a model switch that
	// held it until then changes them.
	if err := s.drive(func(t agent.Term, busy bool) error { return s.ag.SetMode(t, mode, busy, s.modeFacts()) }); err != nil {
		return err
	}
	s.emit(withOrigin(&pb.Event{Kind: &pb.Event_SettingsChanged{SettingsChanged: &pb.SettingsChanged{Mode: mode}}}, origin))
	return nil
}

// compact compacts the context while no turn is running. The transcript
// records it, whoever started it.
func (s *session) compact() error {
	if s.isRunning() {
		return errBusy
	}
	return s.drive(func(t agent.Term, busy bool) error {
		if busy {
			return errBusy
		}
		return s.ag.Compact(t, s.isRunningNoRead)
	})
}

// answerPlan answers the CLI's prompt about a proposed plan.
func (s *session) answerPlan(choice pb.PlanChoice) error { return s.answerPlanBy(choice, originLocal) }

// answerPlanBy is answerPlan on behalf of origin, which is recorded.
func (s *session) answerPlanBy(choice pb.PlanChoice, origin *pb.Origin) error {
	// The adapter checks on the held screen that the plan prompt shows.
	if err := s.drive(func(t agent.Term, _ bool) error { return s.ag.AnswerPlan(t, choice) }); err != nil {
		return err
	}
	s.emit(withOrigin(&pb.Event{Kind: &pb.Event_PlanAnswered{PlanAnswered: &pb.PlanAnswered{Choice: int32(choice)}}}, origin))
	return nil
}
