package outcome

import "errors"

// A backend's diagnostic code is independent of its completion evidence.
// Keep this marker local; it is never serialized or sent to the server.
type completionError struct{ error }

func (e *completionError) Unwrap() error { return e.error }

// FinalError marks a settled business failure at the backend decision point.
// Unmarked errors are conservatively undetermined. Success responses themselves
// establish completion; a context canceled after a response does not undo it.
func FinalError(err error) error {
	if err == nil {
		return nil
	}
	return &completionError{err}
}
func IsFinalError(err error) bool {
	var e *completionError
	return errors.As(err, &e)
}
