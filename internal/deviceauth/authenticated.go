package deviceauth

import (
	"bytes"
	"context"
	"errors"
)

// Authenticated is an endpoint-bound capability, not a caller supplied header.
// It retains the original decoded protobuf bytes (never a re-serialization).
// Ciphertext and compression buffers can be released before scheduling.
type Authenticated struct {
	owner   *Endpoint
	header  Header
	body    []byte
	key     string
	payload Hash
}

func (e *Endpoint) Authenticate(w Wire, seed []byte, now uint64) (*Authenticated, error) {
	h, plain, err := e.openAuthenticated(w, seed, false, now)
	if err != nil {
		return nil, err
	}
	plain, err = Unpack(h, plain)
	if err != nil {
		return nil, err
	}
	// Compression is representation, not authorization or request identity.
	h.Compression = 0
	payload, err := payloadHash(h, plain)
	if err != nil {
		return nil, err
	}
	return &Authenticated{owner: e, header: h, body: plain, key: acceptedKey(h), payload: payload}, nil
}

func (a *Authenticated) Header() Header { return a.header }
func (a *Authenticated) Body() []byte   { return bytes.Clone(a.body) }
func (a *Authenticated) Size() int      { return len(a.body) }
func (a *Authenticated) Key() string    { return a.key }
func (a *Authenticated) Payload() Hash  { return a.payload }

// Conflict binds an identity-conflict outcome to the authenticated key and payload.
func (a *Authenticated) Conflict() error {
	var key Hash
	copy(key[:], a.key)
	return &EntryDecision{Outcome: "conflict", Reason: "identity_conflict", ConflictKind: "identity", Key: key, Payload: a.payload, cause: ErrIdentityConflict}
}

// Decision binds a transient scheduling failure to the authenticated request.
// No terminal refusal is invented: the same authorization may still execute.
func (a *Authenticated) Decision(reason string, cause error) error {
	var key Hash
	copy(key[:], a.key)
	return &EntryDecision{Outcome: "unknown", Reason: reason, Key: key, Payload: a.payload, cause: cause}
}

// Lookup checks cached/running identity without an execution slot. A waiter is
// registered before returning so it has a fixed, bounded lifetime and cannot
// race admission or exceed the per-key/global budgets. The caller must Wait.
func (e *Endpoint) Lookup(ctx context.Context, a *Authenticated, now uint64) (body []byte, err error, wait *ResultWaiter, found bool) {
	if a == nil || a.owner != e {
		return nil, ErrInvalid, nil, true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, a.Decision("outcome_unknown", ErrStorage), nil, true
	}
	record, exists := e.data.Accepted[a.key]
	run := e.running[a.key]
	if !exists && run == nil {
		return nil, nil, nil, false
	}
	if exists && record.PayloadHash != a.payload || run != nil && run.payload != a.payload {
		var key Hash
		copy(key[:], a.key)
		return nil, &EntryDecision{Outcome: "conflict", Reason: "identity_conflict", ConflictKind: "identity", Key: key, Payload: a.payload, cause: ErrIdentityConflict}, nil, true
	}
	if run != nil && run.readOnly && run.completed {
		if run.resultErr != nil {
			return nil, a.resultError(run.resultErr), nil, true
		}
		return bytes.Clone(run.result), nil, nil, true
	}
	if run != nil && (!exists || record.State == running) {
		w, err := e.registerWaitLocked(ctx, a.key, run, now)
		if err != nil {
			return nil, a.resultError(err), nil, true
		}
		var key Hash
		copy(key[:], a.key)
		payload := a.payload
		return nil, nil, &ResultWaiter{cancel: w.cancel, wait: func() ([]byte, error) {
			b, err := w.Wait()
			if err != nil {
				return b, bindResultError(key, payload, err)
			}
			return b, nil
		}}, true
	}
	body, err = acceptedResult(record)
	if err != nil {
		err = a.resultError(err)
	}
	return body, err, nil, true
}

func (a *Authenticated) resultError(err error) error {
	var key Hash
	copy(key[:], a.key)
	return bindResultError(key, a.payload, err)
}
func bindResultError(key, payload Hash, err error) error {
	var d *EntryDecision
	if errors.As(err, &d) {
		copy := *d
		copy.Key, copy.Payload = key, payload
		return &copy
	}
	return &EntryDecision{Outcome: "unknown", Reason: "outcome_unknown", Key: key, Payload: payload, cause: err}
}

// Failure attaches authenticated identity without changing the outcome category.
func (a *Authenticated) Failure(err error) error { return a.resultError(err) }
