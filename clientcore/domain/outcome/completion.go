package outcome

// The outermost execution decision wins. In particular, uncertainty after a
// native write must hide any earlier/preflight FinalError further in the chain.
type completionError struct {
	error
	final, verified bool
}

func (e *completionError) Unwrap() error { return e.error }

func decision(err error) *completionError {
	for err != nil {
		if e, ok := err.(*completionError); ok {
			return e
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return nil
		}
		err = u.Unwrap()
	}
	return nil
}

// FinalError wraps err as a deterministic failure when not already marked.
func FinalError(err error) error {
	if err == nil {
		return nil
	}
	if decision(err) != nil {
		return err
	}
	return &completionError{error: err, final: true}
}

// UndeterminedError marks err as an uncertain write outcome.
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

// IsFinalError reports whether the outermost completion decision is final.
func IsFinalError(err error) bool { e := decision(err); return e != nil && e.final }

// IsVerifiedError reports whether the outermost completion decision is both final and verified.
func IsVerifiedError(err error) bool { e := decision(err); return e != nil && e.final && e.verified }
