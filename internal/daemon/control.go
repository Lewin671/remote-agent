package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/outcome"
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
	ctx  context.Context
}

func (h heldTerm) Context() context.Context { return h.ctx }
func (h heldTerm) Capture() (string, error) {
	if err := h.ctx.Err(); err != nil {
		return "", err
	}
	return h.term.CaptureContext(h.ctx)
}
func (h heldTerm) RefreshView() error {
	view, ok := h.term.(interface{ RefreshView(context.Context) error })
	if !ok {
		return outcome.FinalError(agent.ErrUnsupported)
	}
	return view.RefreshView(h.ctx)
}

func (h heldTerm) Write(b []byte) error {
	if err := h.ctx.Err(); err != nil {
		return err
	}
	return h.c.Write(b)
}

// answerAttempt distinguishes cancellation before writing from an answer
// whose keys may already have reached the native CLI. Only the former can be
// retried safely; heldTerm checks ctx again immediately before each write.
type answerAttempt struct {
	agent.Term
	wrote bool
}

func (t *answerAttempt) Write(b []byte) error {
	if err := t.Context().Err(); err != nil {
		return err
	}
	t.wrote = true
	return t.Term.Write(b)
}

// drive runs op with the terminal held for it. The status check is paused
// meanwhile so that the menus it opens are not reported as popups.
//
// op is told whether the CLI is busy as found once the terminal is held,
// not before: an injection that held it until then may have started a
// turn. Busy means a turn in the transcript, a message written and not yet
// confirmed, or the screen showing the CLI at work.
func (s *session) drive(ctx context.Context, op func(t agent.Term, busy bool) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	exited := s.meta.Exited
	s.mu.Unlock()
	if exited {
		return outcome.FinalError(errExited)
	}
	if err := s.ctlMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.ctlMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	exited = s.meta.Exited
	s.mu.Unlock()
	if exited {
		return outcome.FinalError(errExited)
	}
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
	term := heldTerm{s.term, c, ctx}
	if err := agent.Wait(term, settle); err != nil {
		return err
	}
	busy := s.busyNow(term)
	if err := ctx.Err(); err != nil {
		return err
	}
	return op(term, busy)
}

// busyNow tells whether a turn runs or is starting; the caller holds the
// terminal, so no injection happens meanwhile.
func (s *session) busyNow(t agent.Term) bool {
	s.readRollout()
	s.mu.Lock()
	busy := s.running || len(s.queue.Queue) > 0 && s.queue.Queue[0].Inflight != nil
	s.mu.Unlock()
	if busy {
		return true
	}
	styled, err := t.Capture()
	return err != nil || s.ag.Analyze(styled).Busy
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
	return s.setModelBy(context.Background(), model, effort, originLocal)
}

// setModelBy is setModel on behalf of origin.
func (s *session) setModelBy(ctx context.Context, model, effort string, origin *pb.Origin) error {
	if !s.ag.Capabilities().ModelSwitch {
		return outcome.FinalError(agent.ErrUnsupported)
	}
	if err := s.ag.CheckEffort(effort); err != nil {
		return outcome.FinalError(err)
	}
	if s.isRunning() {
		return outcome.FinalError(errBusy)
	}
	if err := s.drive(ctx, func(t agent.Term, busy bool) error {
		if busy {
			return outcome.FinalError(errBusy)
		}
		if err := s.ag.SetModel(t, model, effort); err != nil {
			return err
		}
		if err := s.emit(withOrigin(&pb.Event{Kind: &pb.Event_SettingsChanged{SettingsChanged: &pb.SettingsChanged{Model: model, Effort: effort}}}, origin)); err != nil {
			return outcome.UndeterminedError(fmt.Errorf("settings already applied on the computer but could not be recorded: %w", err))
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// setApprovalMode switches the approval mode.
func (s *session) setApprovalMode(mode pb.ApprovalMode) error {
	return s.setApprovalModeBy(context.Background(), mode, originLocal)
}

// setApprovalModeBy is setApprovalMode on behalf of origin.
func (s *session) setApprovalModeBy(ctx context.Context, mode pb.ApprovalMode, origin *pb.Origin) error {
	if !s.nativeIdentityReady() {
		return outcome.FinalError(agent.ErrNotReady)
	}
	if _, ok := s.ag.(agent.StatusRefresher); ok {
		s.mu.Lock()
		stale := s.statusLineAt.IsZero() || time.Since(s.statusLineAt) > 5*time.Second
		s.mu.Unlock()
		if stale {
			_ = s.refreshNativeStatus(ctx)
		}
	}
	// The facts too are read with the terminal held: a model switch that
	// held it until then changes them.
	return s.drive(ctx, func(t agent.Term, busy bool) error {
		if err := s.ag.SetMode(t, mode, busy, s.modeFacts()); err != nil {
			return err
		}
		if err := s.emit(withOrigin(&pb.Event{Kind: &pb.Event_SettingsChanged{SettingsChanged: &pb.SettingsChanged{Mode: mode}}}, origin)); err != nil {
			return outcome.UndeterminedError(fmt.Errorf("settings already applied on the computer but could not be recorded: %w", err))
		}
		return nil
	})
}

// compact compacts the context while no turn is running. The transcript
// records it, whoever started it.
func (s *session) compact() error { return s.compactContext(context.Background()) }

func (s *session) compactContext(ctx context.Context) error {
	if !s.nativeIdentityReady() {
		return outcome.FinalError(agent.ErrNotReady)
	}
	if s.isRunning() {
		return outcome.FinalError(errBusy)
	}
	return s.drive(ctx, func(t agent.Term, busy bool) error {
		if busy {
			return outcome.FinalError(errBusy)
		}
		return s.ag.Compact(t, s.isRunningNoRead)
	})
}

// answerPlan answers the CLI's prompt about a proposed plan.
func (s *session) answerPlan(choice pb.PlanChoice) error {
	return s.answerPlanBy(context.Background(), choice, originLocal)
}

// answerPlanBy is answerPlan on behalf of origin, which is recorded.
func (s *session) answerPlanBy(ctx context.Context, choice pb.PlanChoice, origin *pb.Origin) error {
	return s.answerPlanUsing(ctx, choice, origin, func(e *pb.Event) error { return s.emit(e) })
}

func (s *session) answerPlanUsing(ctx context.Context, choice pb.PlanChoice, origin *pb.Origin, record func(*pb.Event) error) error {
	if !s.nativeIdentityReady() {
		return outcome.FinalError(agent.ErrNotReady)
	}
	// The adapter checks on the held screen that the plan prompt shows.
	return s.drive(ctx, func(t agent.Term, _ bool) error {
		s.mu.Lock()
		err := s.checkRemoteAnswer(remotePlan, "", false, origin)
		s.mu.Unlock()
		if err != nil {
			return err
		}
		if origin.GetKind() == pb.OriginKind_ORIGIN_KIND_CLIENT {
			styled, err := t.Capture()
			if err != nil {
				return err
			}
			sum := planScreenHash(styled)
			if !bytes.Equal(sum, origin.ObservedScreenHash) {
				return errRemoteAnswerStale
			}
		}
		s.readMu.Lock()
		var facts *agent.PlanFacts
		if source, ok := s.parser.(agent.PlanSource); ok {
			facts = source.Plan()
		}
		if source, ok := s.parser.(agent.PlanAnswerSource); ok {
			source.BeginPlanAnswer(facts, choice)
		}
		s.readMu.Unlock()
		observed := func() bool {
			if !s.readRollout() {
				return false
			}
			s.readMu.Lock()
			defer s.readMu.Unlock()
			if source, ok := s.parser.(agent.PlanAnswerSource); ok {
				return source.PlanAnswerObserved()
			}
			return false
		}
		err = s.ag.AnswerPlan(t, choice, facts, observed)
		s.readMu.Lock()
		var localAnswers []*pb.Event
		if source, ok := s.parser.(agent.PlanAnswerSource); ok {
			localAnswers = source.EndPlanAnswer(err == nil)
		}
		if len(localAnswers) > 0 {
			_ = s.emitLocked(false, localAnswers...)
			for _, e := range localAnswers {
				s.parser.Replay(e)
			}
		}
		s.readMu.Unlock()
		if err != nil {
			return err
		}
		// Successful keys consume this prompt even if recording fails or a
		// status poll still sees it. Failed key delivery leaves it retryable.
		s.mu.Lock()
		s.answerSafety.planPending = true
		s.mu.Unlock()
		if err := record(withOrigin(&pb.Event{Kind: &pb.Event_PlanAnswered{PlanAnswered: &pb.PlanAnswered{Choice: int32(choice)}}}, origin)); err != nil {
			return outcome.UndeterminedError(fmt.Errorf("plan answer already applied on the computer but could not be recorded: %w", err))
		}
		s.readMu.Lock()
		s.parser.Replay(&pb.Event{Kind: &pb.Event_PlanAnswered{PlanAnswered: &pb.PlanAnswered{Choice: int32(choice)}}})
		if source, ok := s.parser.(agent.PlanSource); ok {
			s.mu.Lock()
			s.planFacts = source.Plan()
			s.mu.Unlock()
		}
		s.readMu.Unlock()

		return nil
	})
}

func planScreenHash(styled string) []byte {
	sum := sha256.Sum256(append([]byte("ra2/plan-screen\x00"), []byte(agent.StripANSI(styled))...))
	return sum[:]
}
