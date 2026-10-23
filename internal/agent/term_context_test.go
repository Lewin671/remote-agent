package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

type canceledMenuTerm struct {
	ctx      context.Context
	captures int
}

func (t *canceledMenuTerm) Context() context.Context { return t.ctx }
func (t *canceledMenuTerm) Capture() (string, error) { t.captures++; return "menu not ready", nil }
func (*canceledMenuTerm) Write([]byte) error         { return nil }

func TestWaitForStopsAtOperationDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		term := &canceledMenuTerm{ctx: ctx}
		start := time.Now()
		_, err := WaitFor(term, "menu", func(string) bool { return false })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("wait: %v", err)
		}
		if elapsed := time.Since(start); elapsed != 25*time.Millisecond {
			t.Fatalf("wait ignored deadline: %s", elapsed)
		}
		if term.captures != 1 {
			t.Fatalf("captures after cancellation: %d", term.captures)
		}
	})
}
