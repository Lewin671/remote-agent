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
	if !Uncertain(&CommandError{Code: "unknown_outcome"}) {
		t.Fatal("unsigned outcome is not final")
	}
}

func TestSignedUnknownAndReadOnlyInterruptionsAreFinal(t *testing.T) {
	for _, fromDaemon := range []bool{false, true} {
		for _, code := range []string{"unknown", "generation_conflict"} {
			if Uncertain(&CommandError{Code: code, FromDaemon: fromDaemon}) {
				t.Fatalf("%s daemon=%v", code, fromDaemon)
			}
		}
	}
	for _, code := range []string{"canceled", "deadline_exceeded", "aborted"} {
		if Uncertain(&CommandError{Code: code, FromDaemon: true, ReadOnly: true}) {
			t.Fatal(code)
		}
	}
}
