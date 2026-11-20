// Package deviceauth is the daemon's protocol-10 device security: the SQLite
// endpoint with its verified chain, content keys and at-most-once acceptance
// log, body validation, and the synchronous signing, HPKE and randomness
// adapter around the pure shared/devicesec.
package deviceauth

import "errors"

var (
	ErrUnknown        = errors.New("security: unknown state")
	ErrOutcomeUnknown = errors.New("security: accepted operation has no trustworthy result")
	ErrUnavailable    = errors.New("security: content is unavailable before the recovery boundary")
	ErrExpired        = errors.New("security: expired control was not newly accepted")
	ErrBackend        = errors.New("security: previously accepted operation failed")
	ErrStorage        = errors.New("security: local security state unavailable")
)
