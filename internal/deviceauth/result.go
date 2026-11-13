package deviceauth

import (
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// EntryDecision is produced only after authenticating and hashing the original
// payload. It carries no cached body on identity conflict.
type EntryDecision struct {
	Outcome, Reason, ConflictKind string
	Key, Payload                  Hash
	cause                         error
}

func (d *EntryDecision) Error() string { return d.cause.Error() }
func (d *EntryDecision) Unwrap() error { return d.cause }
func RejectedEntry(reason string, err error) error {
	return &EntryDecision{Outcome: "rejected", Reason: reason, cause: err}
}
func notAdmitted(reason string, err error) error {
	return &EntryDecision{Outcome: "not_sent", Reason: reason, cause: err}
}

// RequestIdentity uses the original unpacked bytes, never a reserialized Command.
func RequestIdentity(h Header, plain []byte) (Hash, Hash, error) {
	var key Hash
	copy(key[:], acceptedKey(h))
	payload, err := payloadHash(h, plain)
	return key, payload, err
}

// CommandResult converts acceptance evidence to the encrypted result shell.
// Unauthenticated failures deliberately have no request-bound proof.
func CommandResult(body []byte, err error) (*pb.CommandResult, bool) {
	var d *EntryDecision
	if !errors.As(err, &d) {
		return nil, false
	}
	r := &pb.CommandResult{Outcome: d.Outcome, Reason: d.Reason, ConflictKind: d.ConflictKind, RequestKeyHash: d.Key[:], PayloadHash: d.Payload[:]}
	var missing *ResultUnavailable
	if errors.As(err, &missing) {
		r.Outcome = "executed"
		r.Reason = "result_unavailable"
		if missing.Category == BusinessConflict {
			r.Outcome = "conflict"
			r.ConflictKind = "business"
		}
	}
	return r, true
}
