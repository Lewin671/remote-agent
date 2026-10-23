package daemon

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

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

func TestControlRequestsExpiredBehindLockWriteNothing(t *testing.T) {
	for _, name := range []string{"model", "mode", "compact", "plan", "snapshot"} {
		t.Run(name, func(t *testing.T) {
			h, _ := controlHarness(t)
			h.d.sessions[h.s.meta.ID] = h.s
			h.s.planOpen = true
			handler := &handler{d: h.d}
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &ownershipContext{Context: base, checked: make(chan struct{}), proceed: make(chan struct{})}
			term := &controlCountingTerminal{fakeTerm: h.term}
			h.s.term = term
			h.s.ctlMu.Lock()
			done := make(chan error, 1)
			go func() {
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
				}
				done <- err
			}()
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
