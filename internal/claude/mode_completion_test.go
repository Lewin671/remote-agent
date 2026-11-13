package claude

import (
	"errors"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"testing"
	"testing/synctest"
)

func TestModeRejectionUsesObservedFinalState(t *testing.T) {
	for _, state := range []string{"default", "plan", "auto"} {
		t.Run(state, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := &fakeClaude{mode: index(state)}
				term, w := agent.ObserveNative(f)
				// Model a completed traversal; the actual settled footer determines result.
				_ = term.Write(nil)
				err := w.Failure(modeRejection(term, "default", "plan", agent.ErrUnsupported))
				switch state {
				case "plan":
					if err != nil {
						t.Fatal(err)
					}
				case "default":
					if !outcome.IsFinalError(err) || !errors.Is(err, agent.ErrUnsupported) {
						t.Fatal(err)
					}
				default:
					if outcome.IsFinalError(err) {
						t.Fatal("other mode was called a final failure")
					}
				}
			})
		})
	}
}
