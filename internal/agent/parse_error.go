package agent

import "errors"

// ErrUnknownRecord distinguishes an unrecognized CLI record from metadata
// the adapter deliberately ignores. Never include the raw record in it.
var ErrUnknownRecord = errors.New("unknown CLI record type")
