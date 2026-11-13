package outcome

import "errors"

// The outermost execution decision wins. In particular, uncertainty after a
// native write must hide any earlier/preflight FinalError further in the chain.
type completionError struct {
	error
	final, verified bool
}

func (e *completionError) Unwrap() error { return e.error }
func decision(err error) *completionError {
	var e *completionError
	if errors.As(err, &e) {
		return e
	}
	return nil
}
func FinalError(err error) error {
	if err == nil {
		return nil
	}
	if decision(err) != nil {
		return err
	}
	return &completionError{error: err, final: true}
}
func UndeterminedError(err error) error {
	if err == nil {
		return nil
	}
	return &completionError{error: err}
}

// VerifiedError is only for a fresh terminal read proving the final state
// matches the reported business failure. It can settle a prior uncertain write.
func VerifiedError(err error) error {
	if err == nil {
		return nil
	}
	return &completionError{error: err, final: true, verified: true}
}
func IsFinalError(err error) bool    { e := decision(err); return e != nil && e.final }
func IsVerifiedError(err error) bool { e := decision(err); return e != nil && e.final && e.verified }
