package remoteclient

import "testing"

func TestInterruptedResultsRemainUnknown(t *testing.T) {
	for _, code := range []string{"canceled", "deadline_exceeded", "aborted", "revision_conflict", "failed_precondition"} {
		for _, fromDaemon := range []bool{false, true} {
			want := fromDaemon && (code == "canceled" || code == "deadline_exceeded" || code == "aborted")
			if got := Uncertain(&CommandError{Code: code, FromDaemon: fromDaemon}); got != want {
				t.Errorf("%s daemon=%v: %v want %v", code, fromDaemon, got, want)
			}
		}
	}
	if !Uncertain(&CommandError{Code: "unknown"}) {
		t.Fatal("unsigned outcome is not final")
	}
}
