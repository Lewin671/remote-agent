package daemon

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// Snapshot the first context check before the request attempts ownership.
// The barrier lets the owner cancel it before releasing ctlMu, without sleeps.
type ownershipContext struct {
	context.Context
	once             sync.Once
	checked, proceed chan struct{}
}

func (c *ownershipContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked); <-c.proceed })
	return err
}

type controlCountingTerminal struct {
	*fakeTerm
	opens atomic.Int32
}

func (t *controlCountingTerminal) Control() (control, error) {
	t.opens.Add(1)
	return t.fakeTerm.Control()
}

// Exercise the RPC boundary so dropping ctx in a handler is caught too.
func controlContextRequest(t *testing.T, name string) (*harness, func(context.Context) error) {
	t.Helper()
	h, _ := controlHarness(t)
	approvalID := ""
	switch name {
	case "interrupt":
		h.s.running = true
	case "question":
		if err := h.s.emit(&pb.Event{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "q", Questions: []*pb.Question{{Text: "Which language?", Options: []*pb.QuestionOption{{Label: "Go"}}}}}}}); err != nil {
			t.Fatal(err)
		}
		h.term.setScreen(answerRaceQuestionScreen)
	case "approval":
		h = agyHarness(t)
		h.d.cfg.ApprovalTimeout = time.Hour
		approvalID = nativeCard(t, h).GetApprovalRequested().ApprovalId
	}
	h.d.sessions[h.s.meta.ID] = h.s
	h.s.planOpen = true
	handler := &handler{d: h.d}
	return h, func(ctx context.Context) error {
		var err error
		switch name {
		case "model":
			_, err = handler.SetModel(ctx, connect.NewRequest(&pb.SetModelRequest{SessionId: h.s.meta.ID, Model: "gpt-6-luna", Effort: "high"}))
		case "mode":
			_, err = handler.SetApprovalMode(ctx, connect.NewRequest(&pb.SetApprovalModeRequest{SessionId: h.s.meta.ID, Mode: pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS}))
		case "compact":
			_, err = handler.Compact(ctx, connect.NewRequest(&pb.CompactRequest{SessionId: h.s.meta.ID}))
		case "plan":
			_, err = handler.AnswerPlan(ctx, connect.NewRequest(&pb.AnswerPlanRequest{SessionId: h.s.meta.ID, Choice: pb.PlanChoice_PLAN_CHOICE_IMPLEMENT}))
		case "snapshot":
			_, err = h.d.LiveSnapshot(ctx, h.s.meta.ID)
		case "interrupt":
			_, err = handler.Interrupt(ctx, connect.NewRequest(&pb.InterruptRequest{SessionId: h.s.meta.ID}))
		case "question":
			_, err = handler.AnswerQuestion(ctx, connect.NewRequest(&pb.AnswerQuestionRequest{SessionId: h.s.meta.ID, QuestionId: "q", Option: 1}))
		case "approval":
			_, err = handler.ResolveApproval(ctx, connect.NewRequest(&pb.ResolveApprovalRequest{SessionId: h.s.meta.ID, ApprovalId: approvalID, Allow: true}))
		default:
			t.Fatalf("unknown operation %s", name)
		}
		return err
	}
}

func TestControlLockWaitIsCancelable(t *testing.T) {
	for _, name := range []string{"model", "mode", "compact", "plan", "snapshot", "interrupt", "question", "approval"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, request := controlContextRequest(t, name)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				h.s.ctlMu.Lock()
				done := make(chan error, 1)
				go func() { done <- request(ctx) }()
				synctest.Wait()
				cancel()
				synctest.Wait()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Errorf("canceled wait: %v", err)
					}
				default:
					t.Error("canceled request still waits for terminal ownership")
					h.s.ctlMu.Unlock()
					<-done
					return
				}
				if len(h.term.written()) != 0 {
					t.Error("canceled waiter wrote keys")
				}
				h.s.ctlMu.Unlock()
			})
		})
	}
}

func TestControlRequestsExpiredBehindLockWriteNothing(t *testing.T) {
	for _, name := range []string{"model", "mode", "compact", "plan", "snapshot", "interrupt", "question", "approval"} {
		t.Run(name, func(t *testing.T) {
			h, request := controlContextRequest(t, name)
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &ownershipContext{Context: base, checked: make(chan struct{}), proceed: make(chan struct{})}
			term := &controlCountingTerminal{fakeTerm: h.term}
			h.s.term = term
			h.s.ctlMu.Lock()
			done := make(chan error, 1)
			go func() { done <- request(ctx) }()
			// The request observed a live context while another operation held
			// ctlMu. Cancel before it can obtain terminal ownership.
			<-ctx.checked
			cancel()
			close(ctx.proceed)
			h.s.ctlMu.Unlock()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("expired request: %v", err)
			}
			if keys := h.term.written(); len(keys) != 0 {
				t.Fatalf("expired request wrote %q", keys)
			}
			if term.opens.Load() != 0 {
				t.Fatal("expired request acquired PTY control")
			}
		})
	}
}

func TestControlCancellationAfterFirstKeyStopsMenu(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _ := controlHarness(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		original := h.term.onWrite
		h.term.onWrite = func(f *fakeTerm, b string) { original(f, b); cancel() }
		err := h.s.setModelBy(ctx, "gpt-6-luna", "high", originLocal)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled menu: %v", err)
		}
		keys := h.term.written()
		if len(keys) != 1 || keys[0] != "/model" {
			t.Fatalf("keys after cancellation: %q", keys)
		}
		if lastSettings(h) != nil {
			t.Fatal("incomplete change recorded as applied")
		}
	})
}

func TestHeldTermChecksCancellationBeforeEveryWrite(t *testing.T) {
	h := newHarness(t, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	term := heldTerm{h.term, &fakeControl{h.term}, ctx}
	cancel()
	if err := term.Write([]byte(agent.KeyEnter)); !errors.Is(err, context.Canceled) {
		t.Fatalf("write: %v", err)
	}
	if _, err := term.Capture(); !errors.Is(err, context.Canceled) {
		t.Fatalf("capture: %v", err)
	}
	if len(h.term.written()) != 0 {
		t.Fatal("canceled terminal wrote keys")
	}
}

func TestPlanAnswerRecordsSuccessfulKeyEvenIfCallerCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _ := controlHarness(t)
		h.term.setScreen("  Implement this plan?\n\x1b[1;7m› 1. Yes, implement this plan  Switch\n\x1b[0m  2. Yes, clear context  Fresh\n  3. No, stay in Plan mode  Continue\n")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		h.term.onWrite = func(*fakeTerm, string) { cancel() }
		if err := h.s.answerPlanBy(ctx, pb.PlanChoice_PLAN_CHOICE_IMPLEMENT, originLocal); err != nil {
			t.Fatal(err)
		}
		if !h.s.answerSafety.planPending || len(h.term.written()) != 1 {
			t.Fatal("successful answer was made retryable on cancellation")
		}
		found := false
		for _, e := range h.events() {
			found = found || e.GetPlanAnswered() != nil
		}
		if !found {
			t.Fatal("successful answer not recorded")
		}
	})
}

type canceledCaptureTerminal struct {
	*fakeTerm
	entered chan struct{}
}

func (t *canceledCaptureTerminal) CaptureContext(ctx context.Context) (string, error) {
	close(t.entered)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestControlCancellationDuringCaptureReturnsCancellation(t *testing.T) {
	h, _ := controlHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	term := &canceledCaptureTerminal{fakeTerm: h.term, entered: make(chan struct{})}
	h.s.term = term
	done := make(chan error, 1)
	go func() { done <- h.s.setModelBy(ctx, "gpt-6-luna", "high", originLocal) }()
	<-term.entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled capture: %v", err)
	}
	if len(h.term.written()) != 0 {
		t.Fatal("wrote keys after canceled capture")
	}
}

type cancelOnControlTerminal struct {
	*fakeTerm
	cancel context.CancelFunc
}

func (t *cancelOnControlTerminal) Control() (control, error) {
	c, err := t.fakeTerm.Control()
	t.cancel()
	return c, err
}

func TestActionCancellationAfterOwnershipWritesNothing(t *testing.T) {
	for _, name := range []string{"interrupt", "question", "approval"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, request := controlContextRequest(t, name)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				h.s.term = &cancelOnControlTerminal{h.term, cancel}
				if err := request(ctx); !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled action: %v", err)
				}
				if len(h.term.written()) != 0 {
					t.Fatal("action wrote after cancellation")
				}
				if h.s.answeredBy["q"] != nil || h.s.interruptedBy != nil {
					t.Fatal("unwritten action left a claim")
				}
				if name == "approval" && len(h.s.approvals) != 1 {
					t.Fatal("unwritten approval was consumed")
				}
			})
		})
	}
}

func TestQuestionCancellationAfterFirstKeyKeepsClaim(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := claudeHarness(t, Config{})
		q := &pb.QuestionAsked{QuestionId: "q", Questions: []*pb.Question{{Text: "Which language?", MultiSelect: true, Options: []*pb.QuestionOption{{Label: "Go"}, {Label: "Rust"}}}}}
		if err := h.s.emit(&pb.Event{Kind: &pb.Event_QuestionAsked{QuestionAsked: q}}); err != nil {
			t.Fatal(err)
		}
		h.term.setScreen(ccRule + "\n ☐ Language\nWhich language?\n❯ 1. [ ] Go\n  2. [ ] Rust\nEnter to select · ↑/↓ to navigate · Esc to cancel\n")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		h.term.onWrite = func(*fakeTerm, string) { cancel() }
		origin := answerRaceOrigin(h.s, remoteQuestion, "q", "first-answer")
		if err := h.s.answerBy(ctx, "q", []int32{1, 2}, nil, origin); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled answer: %v", err)
		}
		if keys := h.term.written(); len(keys) != 1 || keys[0] != "1" {
			t.Fatalf("keys after cancellation: %q", keys)
		}
		if !h.s.answeredBy["q"].claimed(nil) {
			t.Fatal("partially written answer became retryable")
		}
		if err := h.s.answerBy(context.Background(), "q", []int32{1, 2}, nil, answerRaceOrigin(h.s, remoteQuestion, "q", "retry-answer")); !errors.Is(err, errRemoteAnswerStale) {
			t.Fatalf("retry: %v", err)
		}
	})
}

func TestNativeApprovalCancellationAfterKeyStaysWithdrawn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, request := controlContextRequest(t, "approval")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		h.term.onWrite = func(*fakeTerm, string) { cancel() }
		if err := request(ctx); !errors.Is(err, agent.ErrDeliveryUnconfirmed) {
			t.Fatalf("unconfirmed answer: %v", err)
		}
		if keys := h.term.written(); len(keys) != 1 || keys[0] != "1" {
			t.Fatalf("keys after cancellation: %q", keys)
		}
		if len(h.s.approvals) != 0 {
			t.Fatal("written approval became retryable")
		}
		h.s.checkScreen()
		h.s.checkScreen()
		if len(h.s.approvals) != 0 {
			t.Fatal("same native prompt was rearmed")
		}
	})
}

type cancelAfterCaptureTerminal struct {
	*fakeTerm
	cancel    context.CancelFunc
	remaining int
}

func (t *cancelAfterCaptureTerminal) CaptureContext(ctx context.Context) (string, error) {
	screen, err := t.fakeTerm.CaptureContext(ctx)
	t.remaining--
	if t.remaining == 0 {
		t.cancel()
	}
	return screen, err
}

func TestActionCancellationBeforeWriteLeavesPromptPending(t *testing.T) {
	for _, name := range []string{"question", "approval"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, request := controlContextRequest(t, name)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				captures := 1 // question screen revalidation, immediately before Answer
				if name == "approval" {
					captures = 3
				} // busyNow, identity check, adapter revalidation
				h.s.term = &cancelAfterCaptureTerminal{h.term, cancel, captures}
				if err := request(ctx); !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled action: %v", err)
				}
				if len(h.term.written()) != 0 {
					t.Fatal("action wrote after canceled capture")
				}
				if h.s.answeredBy["q"] != nil {
					t.Fatal("unwritten answer left a claim")
				}
				if name == "approval" && len(h.s.approvals) != 1 {
					t.Fatal("unwritten approval was consumed")
				}
			})
		})
	}
}

func TestQuestionSettleWaitIsCancelable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, request := controlContextRequest(t, "question")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- request(ctx) }()
		synctest.Wait() // request is in the settle timer; fake time has not advanced
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("canceled settle: %v", err)
			}
		default:
			t.Error("canceled request still waits for settle timer")
			<-done
		}
		if len(h.term.written()) != 0 {
			t.Fatal("canceled settle wrote keys")
		}
	})
}
