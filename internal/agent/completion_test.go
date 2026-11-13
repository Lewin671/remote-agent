package agent

import (
	"context"
	"errors"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"testing"
)

type observedTestTerm struct {
	writes int
	fail   error
}

func (t *observedTestTerm) Context() context.Context { return context.Background() }
func (t *observedTestTerm) Capture() (string, error) { return "", nil }
func (t *observedTestTerm) Write([]byte) error       { t.writes++; return t.fail }
func TestNativeEvidenceRequiresReadbackAfterAnyWrite(t *testing.T) {
	for _, failedWrite := range []bool{false, true} {
		raw := &observedTestTerm{}
		if failedWrite {
			raw.fail = ErrUnsupported
		}
		term, w := ObserveNative(raw)
		if !outcome.IsFinalError(w.Failure(ErrUnsupported)) {
			t.Fatal("preflight lost certainty")
		}
		_ = term.Write([]byte("key"))
		for _, cause := range []error{ErrUnsupported, ErrNotFound, outcome.FinalError(ErrUnsupported)} {
			err := w.Failure(cause)
			if outcome.IsFinalError(err) || !errors.Is(err, cause) {
				t.Fatalf("unverified write finalized: %v", err)
			}
		}
		if !outcome.IsFinalError(w.Failure(outcome.VerifiedError(ErrUnsupported))) {
			t.Fatal("verified result lost evidence")
		}
	}
}
