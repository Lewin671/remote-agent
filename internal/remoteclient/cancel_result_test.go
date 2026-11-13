package remoteclient

import (
	"github.com/Lewin671/remote-agent/internal/outcome"
	"testing"
)

func TestOnlyExplicitEvidenceSettlesInterruptedResults(t *testing.T) {
	for _, code := range []string{"canceled", "deadline_exceeded", "aborted", "unknown", "revision_conflict", "failed_precondition"} {
		for _, category := range []outcome.Category{outcome.NotSent, outcome.Rejected, outcome.Executed, outcome.Unknown, outcome.Conflict} {
			got := Uncertain(&CommandError{Code: code, FromDaemon: true, Outcome: category, ResultAvailable: true})
			if got != (category == outcome.Unknown) {
				t.Fatalf("%s/%s: %v", code, category, got)
			}
		}
	}
	for _, e := range []*CommandError{{Outcome: outcome.Conflict, ConflictKind: "identity"}, {Outcome: outcome.Executed, ResultAvailable: false}} {
		if !Uncertain(e) {
			t.Fatal("must preserve original request while refreshing")
		}
	}
}
