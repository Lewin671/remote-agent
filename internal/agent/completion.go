package agent

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/Lewin671/remote-agent/internal/outcome"
)

// NativeWrites observes attempted writes, including failed/partial writes and
// cleanup keys. It cannot turn a diagnostic sentinel into completion evidence.
type NativeWrites struct{ attempted atomic.Bool }
type observedTerm struct {
	Term
	writes *NativeWrites
}

func (t observedTerm) Write(b []byte) error { t.writes.attempted.Store(true); return t.Term.Write(b) }

type observedViewTerm struct {
	observedTerm
	view ViewRefresher
}

func (t observedViewTerm) RefreshView() error { return t.view.RefreshView() }
func ObserveNative(t Term) (Term, *NativeWrites) {
	w := &NativeWrites{}
	wrapped := observedTerm{t, w}
	if view, ok := t.(ViewRefresher); ok {
		return observedViewTerm{wrapped, view}, w
	}
	return wrapped, w
}
func (w *NativeWrites) Failure(err error) error {
	if err == nil || outcome.IsVerifiedError(err) {
		return err
	}
	if w.attempted.Load() || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrDeliveryUnconfirmed) {
		return outcome.UndeterminedError(err)
	}
	// At this adapter boundary the only effects are terminal writes. A failure
	// before the first attempted key is an established preflight failure.
	return outcome.FinalError(err)
}
