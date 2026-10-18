package deviceauth

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
)

type Exchange func(context.Context, Capsule) (Capsule, error)

// Remote coordinates bounded automatic sync and exact-cipher authorization.
// The transport may lose, replay, reorder or forge anything; only the pinned
// authority and durable endpoint state decide what is accepted.
type Remote struct {
	Endpoint *Endpoint
	Exchange Exchange
	syncMu   sync.Mutex
}

func (e *Endpoint) Request(method Method, body any) (Capsule, Waiter, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.disk.check(); err != nil {
		return Capsule{}, Waiter{}, err
	}
	return Request(e.data.Pin, ed25519.NewKeyFromSeed(e.data.EdSeed), method, body)
}
func (e *Endpoint) Reply(w Waiter, c Capsule, out any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.disk.check(); err != nil {
		return err
	}
	if w.Pin != e.data.Pin || w.Sender != e.data.MemberID {
		return ErrUntrusted
	}
	return w.Reply(c, e.data.XPrivate, out)
}
func (r *Remote) RPC(ctx context.Context, method Method, body, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q, w, err := r.Endpoint.Request(method, body)
	if err != nil {
		return err
	}
	c, err := r.Exchange(ctx, q)
	if err != nil {
		return err
	}
	return r.Endpoint.Reply(w, c, out)
}
func (r *Remote) Sync(ctx context.Context) error {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	for pages := 0; pages < maxEpochs; pages++ {
		_, s, _ := r.Endpoint.Snapshot()
		sh, _ := s.Hash()
		var reply StateReply
		if err := r.RPC(ctx, States, StateQuery{After: s.Core.Epoch}, &reply); err != nil {
			return err
		}
		if reply.Current == sh {
			if len(reply.States) != 0 {
				return ErrInvalid
			}
			return r.fence(ctx)
		}
		if len(reply.States) == 0 || len(reply.States) > 2 {
			return ErrStale
		}
		if err := r.Endpoint.Sync(reply.States); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return ErrCapacity
}
func (r *Remote) fence(ctx context.Context) error {
	var fences []FenceRequest
	if err := r.RPC(ctx, GetFences, struct{}{}, &fences); err != nil {
		return err
	}
	if len(fences) > 1 {
		return ErrInvalid
	}
	for _, q := range fences {
		receipt, err := r.Endpoint.Fence(q, nil)
		if err != nil {
			return err
		}
		var done *Completion
		if err := r.RPC(ctx, AckFence, receipt, &done); err != nil {
			return err
		}
	}
	return nil
}

// Seal keeps the formed ciphertext local until the exact authorization has
// arrived and been checked. Stale epoch responses cause fresh encryption after
// verified synchronization, never publication under an old content secret.
func (r *Remote) Seal(ctx context.Context, h Header, seed, plain []byte) (Wire, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return Wire{}, err
		}
		c, err := r.Endpoint.Prepare(h, seed, plain)
		if err != nil {
			return Wire{}, err
		}
		q, err := AuthorizationRequest(c)
		if err != nil {
			return Wire{}, err
		}
		var reply AuthorizeReply
		if err = r.RPC(ctx, Authorize, q, &reply); errors.Is(err, ErrStale) {
			if err = r.Sync(ctx); err != nil {
				return Wire{}, err
			}
			continue
		} else if err != nil {
			return Wire{}, err
		}
		p, s, _ := r.Endpoint.Snapshot()
		w, err := reply.Attach(c, p, s)
		if errors.Is(err, ErrStale) {
			continue
		}
		return w, err
	}
	return Wire{}, ErrStale
}
func (r *Remote) SealMessage(ctx context.Context, h Header, seed []byte, plain []byte) (*Wire, error) {
	w, err := r.Seal(ctx, h, seed, plain)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// OpenCurrent always obtains a nonce-bound current head first. Its callers must
// retain backpressure; stale display caches are never proof of current state.
func (r *Remote) OpenCurrent(ctx context.Context, w Wire, seed []byte) (Header, []byte, error) {
	if err := r.Sync(ctx); err != nil {
		return Header{}, nil, err
	}
	return r.Endpoint.Open(w, seed)
}

// OpenHistory authenticates old data without changing current membership or
// enabling a control path. The caller must render historical metadata/events as
// read-only, and must fetch a fresh owner snapshot for actionable approvals.
func (r *Remote) OpenHistory(ctx context.Context, w Wire, seed []byte) (Header, []byte, error) {
	h, err := ParseHeader(w.Header)
	if err != nil || h.Purpose.Control() {
		return Header{}, nil, ErrInvalid
	}
	if h, plain, err := r.Endpoint.openHistory(w, seed); err == nil {
		return h, plain, nil
	} else if !errors.Is(err, ErrNoHistory) && !errors.Is(err, ErrStale) {
		return h, nil, err
	}
	if err := r.Sync(ctx); err != nil {
		return h, nil, err
	}
	p, current, _ := r.Endpoint.Snapshot()
	if h.Epoch > current.Core.Epoch {
		return h, nil, ErrStale
	}
	if h.Epoch == current.Core.Epoch {
		return r.Endpoint.Open(w, seed)
	}
	var sr StateReply
	if err := r.RPC(ctx, States, StateQuery{After: h.Epoch - 1}, &sr); err != nil {
		return h, nil, err
	}
	if len(sr.States) == 0 || sr.States[0].Core.Epoch != h.Epoch || p.Verify(sr.States[0]) != nil {
		return h, nil, ErrUntrusted
	}
	var keys KeyReply
	if err := r.RPC(ctx, HistoryKeys, KeyQuery{After: h.ContentEpoch - 1}, &keys); err != nil {
		return h, nil, err
	}
	if len(keys.Keys) == 0 || keys.Keys[0].Epoch != h.ContentEpoch {
		return h, nil, ErrUntrusted
	}
	if err := r.Endpoint.cacheHistory(sr.States[0], keys.Keys[0].Secret); err != nil {
		return h, nil, err
	}
	return w.Open(p, sr.States[0], keys.Keys[0].Secret, seed)
}

// SealMutable reads the authority's nonce-bound catalog and retries a bounded
// CAS. Intended for an owner's complete machine/session snapshot. Client edit
// operations (rename/preferences) instead reapply their edit to the returned
// latest object; they must not overwrite unrelated fields with an old snapshot.
func (r *Remote) SealMutable(ctx context.Context, h Header, plain []byte) (Wire, error) {
	if !h.Purpose.Mutable() {
		return Wire{}, ErrInvalid
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := r.Sync(ctx); err != nil {
			return Wire{}, err
		}
		p, state, _ := r.Endpoint.Snapshot()
		sh, _ := state.Hash()
		after := []byte{}
		revision := uint64(0)
		restart := false
		for page := 0; page < MaxObjects; page++ {
			var catalog CatalogReply
			err := r.RPC(ctx, Catalog, CatalogQuery{After: after, AtState: sh}, &catalog)
			if errors.Is(err, ErrStale) {
				restart = true
				break
			}
			if err != nil {
				return Wire{}, err
			}
			ch, _ := catalog.State.Hash()
			if p.Verify(catalog.State) != nil || ch != sh {
				return Wire{}, ErrUntrusted
			}
			for _, entry := range catalog.Entries {
				eh, err := ParseHeader(entry.Wire.Header)
				if err != nil {
					return Wire{}, err
				}
				if string(entry.Object) != eh.objectKey() {
					return Wire{}, ErrUntrusted
				}
				if eh.objectKey() == h.objectKey() {
					revision = eh.Seq
				}
			}
			if len(catalog.Next) == 0 {
				break
			}
			if string(catalog.Next) <= string(after) {
				return Wire{}, ErrInvalid
			}
			after = catalog.Next
		}
		if restart {
			continue
		}
		if revision == ^uint64(0) {
			return Wire{}, ErrCapacity
		}
		h.Seq = revision + 1
		w, err := r.Seal(ctx, h, nil, plain)
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrStale) {
			continue
		}
		return w, err
	}
	return Wire{}, ErrConflict
}
