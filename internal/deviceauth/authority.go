package deviceauth

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"slices"
	"sync"
	"time"
)

const maxEpochs = 32768
const maxCompleted = 1024

type authorityData struct {
	Pin       Pin
	State     State
	EdSeed    []byte
	XPrivate  []byte
	Secrets   map[uint64][]byte
	Pending   *RevocationGroup
	Completed map[string]Completion
	Catalog   map[string]Wire
	Contexts  map[string]PairContext
}
type Authority struct {
	mu   sync.Mutex
	disk *localStore
	data authorityData
	now  func() time.Time
}

// InitAuthority is local and explicit. Existing root/account identity is kept,
// but neither authority key is derived from (or distributed with) that root.
func InitAuthority(path, account string, member Member) (*Authority, error) {
	if member.validate() != nil || member.Kind != Machine || !deviceID(account) {
		return nil, ErrInvalid
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	xpriv, xpub, err := NewXKey()
	if err != nil {
		return nil, err
	}
	domain, err := randomHash()
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return nil, err
	}
	core := Core{Account: account, Domain: domain, AuthorityID: member.ID, Epoch: 1, ContentEpoch: 1, Members: []Member{member}}
	copy(core.AuthorityEd[:], pub)
	copy(core.AuthorityX[:], xpub)
	state, err := BuildState(core, secret, priv)
	if err != nil {
		return nil, err
	}
	pin, err := state.Pin()
	if err != nil {
		return nil, err
	}
	d := authorityData{Pin: pin, State: state, EdSeed: bytes.Clone(priv.Seed()), XPrivate: xpriv, Secrets: map[uint64][]byte{1: secret}, Catalog: map[string]Wire{}, Contexts: map[string]PairContext{}, Completed: map[string]Completion{}}
	disk, err := openStore(path, d)
	if err != nil {
		return nil, err
	}
	if err = disk.save(d, &state); err != nil {
		disk.close()
		return nil, err
	}
	return &Authority{disk: disk, data: d, now: time.Now}, nil
}
func OpenAuthority(path string) (*Authority, error) {
	disk, err := openStore(path, nil)
	if err != nil {
		return nil, err
	}
	a := &Authority{disk: disk, now: time.Now}
	if disk.load(&a.data) != nil || a.validate() != nil {
		disk.close()
		return nil, ErrStorage
	}
	return a, nil
}
func (a *Authority) validate() error {
	d := a.data
	if d.Pin.Verify(d.State) != nil || len(d.EdSeed) != 32 || len(d.XPrivate) != 32 || len(d.Secrets) == 0 || len(d.Secrets) > maxEpochs || d.Catalog == nil || d.Contexts == nil || d.Completed == nil || len(d.Catalog) > MaxObjects || len(d.Contexts) > MaxMembers || len(d.Completed) > maxCompleted {
		return ErrStorage
	}
	x, err := ecdh.X25519().NewPrivateKey(d.XPrivate)
	if err != nil || !bytes.Equal(x.PublicKey().Bytes(), d.Pin.AuthorityX[:]) {
		return ErrStorage
	}
	ed := ed25519.NewKeyFromSeed(d.EdSeed)
	if !bytes.Equal(ed.Public().(ed25519.PublicKey), d.Pin.AuthorityEd[:]) {
		return ErrStorage
	}
	if d.Pending != nil {
		sh, _ := d.State.Hash()
		ph, _ := d.Pending.Head.Hash()
		if sh != ph || d.Pending.Complete || len(d.Pending.Operations) == 0 || len(d.Pending.Operations) > MaxMembers || !slices.Equal(d.Pending.Required, required(d.Pending.Base, d.State)) {
			return ErrStorage
		}
		for _, r := range d.Pending.Receipts {
			if r.verify(d.Pin, *d.Pending) != nil {
				return ErrStorage
			}
		}
	}
	for epoch, secret := range d.Secrets {
		if epoch == 0 || epoch > d.State.Core.Epoch || len(secret) != 32 {
			return ErrStorage
		}
	}
	if len(d.Secrets[d.State.Core.ContentEpoch]) != 32 {
		return ErrStorage
	}
	return nil
}
func (a *Authority) Close() error { a.mu.Lock(); defer a.mu.Unlock(); return a.disk.close() }
func (a *Authority) Snapshot() (Pin, State) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.data.Pin, clone(a.data.State)
}
func (a *Authority) StatePage(after uint64) ([]State, error) { return a.disk.page(after) }
func (a *Authority) save(d authorityData, s *State) error {
	if err := a.disk.save(d, s); err != nil {
		return err
	}
	a.data = d
	return nil
}
func (a *Authority) nextState(d authorityData, members []Member, removed []string) (State, []byte, error) {
	if d.State.Core.Epoch >= maxEpochs {
		return State{}, nil, ErrCapacity
	}
	c := clone(d.State.Core)
	c.Parent, _ = d.State.Hash()
	c.Epoch++
	c.ContentEpoch++
	c.Members = members
	c.Removed = removed
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return State{}, nil, err
	}
	s, err := BuildState(c, secret, ed25519.NewKeyFromSeed(d.EdSeed))
	return s, secret, err
}

// Delete advances the signed head and persists Pending BEFORE exposing it.
// It never waits for a local or remote executor while holding authority.mu.
func (a *Authority) Delete(req DeleteRequest) (RevocationGroup, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.disk.check(); err != nil {
		return RevocationGroup{}, err
	}
	d := clone(a.data)
	for _, done := range d.Completed {
		for _, op := range done.Group.Operations {
			if op.Request.Operation == req.Operation {
				b, err := req.signBytes()
				if err != nil || digest("ra2/delete-digest\x00", b, req.Signature) != op.Digest {
					return RevocationGroup{}, ErrConflict
				}
				return clone(done.Group), nil
			}
		}
	}
	if d.Pending != nil {
		for _, op := range d.Pending.Operations {
			if op.Request.Operation == req.Operation {
				b, err := req.signBytes()
				if err != nil || digest("ra2/delete-digest\x00", b, req.Signature) != op.Digest {
					return RevocationGroup{}, ErrConflict
				}
				return clone(*d.Pending), nil
			}
		}
	}
	dh, err := req.verify(d.State)
	if err != nil {
		return RevocationGroup{}, err
	}
	sh, _ := d.State.Hash()
	if req.Expected != sh {
		return RevocationGroup{}, ErrStale
	}
	if len(d.Completed) >= maxCompleted || d.Pending != nil && len(d.Pending.Operations) >= MaxMembers {
		return RevocationGroup{}, ErrCapacity
	}
	removals := []string{}
	for _, t := range req.Targets {
		m, ok := d.State.Core.Member(t.ID)
		if !ok || m.ID == d.Pin.AuthorityID {
			return RevocationGroup{}, ErrInvalid
		}
		if !req.ExactIDs {
			purpose := DeviceMeta
			if m.Kind == Machine {
				purpose = MachineMeta
			}
			h := Header{Purpose: purpose, Request: m.ID}
			if m.Kind == Machine {
				h.Machine = m.ID
				h.Request = ""
			}
			latest, ok := d.Catalog[h.objectKey()]
			var commit Hash
			if !ok {
				commit = UnnamedMemberCommit(d.State, m)
			} else {
				commit, _ = latest.Commitment()
			}
			if commit != t.MetadataCommit {
				return RevocationGroup{}, ErrConflict
			}
		}
		removals = append(removals, t.ID)
	}
	members := []Member{}
	for _, m := range d.State.Core.Members {
		if !slices.Contains(removals, m.ID) {
			members = append(members, m)
		}
	}
	s, secret, err := a.nextState(d, members, removals)
	if err != nil {
		return RevocationGroup{}, err
	}
	if d.Pending == nil {
		d.Pending = &RevocationGroup{ID: req.Operation, Base: clone(d.State)}
	}
	g := d.Pending
	nh, _ := s.Hash()
	g.Operations = append(g.Operations, GroupOperation{Request: clone(req), Digest: dh, NewState: nh})
	g.Head = s
	g.Required = required(g.Base, s)
	g.Receipts = nil
	d.State = s
	d.Secrets[s.Core.ContentEpoch] = secret
	// Active old-head contexts are permanently canceled. They are not recreated
	// from certificates on retry or when the pending group eventually completes.
	for id, c := range d.Contexts {
		c.Canceled = true
		d.Contexts[id] = c
	}
	if err = a.save(d, &s); err != nil {
		return RevocationGroup{}, err
	}
	return clone(*g), nil
}
func (a *Authority) FenceRequests() ([]FenceRequest, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.disk.check(); err != nil {
		return nil, err
	}
	g := a.data.Pending
	if g == nil {
		return nil, nil
	}
	sh, _ := g.Head.Hash()
	out := []FenceRequest{}
	for _, id := range g.Required {
		if slices.ContainsFunc(g.Receipts, func(r FenceReceipt) bool { return r.Executor == id }) {
			continue
		}
		r := FenceRequest{Account: a.data.Pin.Account, Domain: a.data.Pin.Domain, AuthorityID: a.data.Pin.AuthorityID, Group: g.ID, Revision: g.revision(), Epoch: g.Head.Core.Epoch, State: sh, Executor: id}
		b, err := r.signBytes()
		if err != nil {
			return nil, err
		}
		r.Signature = ed25519.Sign(ed25519.NewKeyFromSeed(a.data.EdSeed), b)
		out = append(out, r)
	}
	return out, nil
}
func (a *Authority) Ack(receipt FenceReceipt) (*Completion, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.disk.check(); err != nil {
		return nil, err
	}
	d := clone(a.data)
	if d.Pending == nil {
		return nil, ErrStale
	}
	g := d.Pending
	if err := receipt.verify(d.Pin, *g); err != nil {
		return nil, err
	}
	for _, r := range g.Receipts {
		if r.Executor == receipt.Executor {
			if !bytes.Equal(r.Signature, receipt.Signature) {
				return nil, ErrConflict
			}
			return nil, nil
		}
	}
	g.Receipts = append(g.Receipts, clone(receipt))
	slices.SortFunc(g.Receipts, func(x, y FenceReceipt) int {
		if x.Executor < y.Executor {
			return -1
		}
		if x.Executor > y.Executor {
			return 1
		}
		return 0
	})
	if len(g.Receipts) < len(g.Required) {
		return nil, a.save(d, nil)
	}
	// Completion is durable before the authority releases its signed receipt.
	g.Complete = true
	c := Completion{Group: clone(*g)}
	b, err := c.signBytes()
	if err != nil {
		return nil, err
	}
	d.Completed[g.ID] = c
	d.Pending = nil
	if err = a.save(d, nil); err != nil {
		return nil, err
	}
	c.Signature = ed25519.Sign(ed25519.NewKeyFromSeed(d.EdSeed), b)
	return &c, nil
}
func (a *Authority) Completion(operation string) (*Completion, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.disk.check(); err != nil {
		return nil, err
	}
	for _, c := range a.data.Completed {
		for _, op := range c.Group.Operations {
			if op.Request.Operation == operation {
				b, err := c.signBytes()
				if err != nil {
					return nil, err
				}
				c.Signature = ed25519.Sign(ed25519.NewKeyFromSeed(a.data.EdSeed), b)
				return &c, nil
			}
		}
	}
	if a.data.Pending != nil {
		for _, op := range a.data.Pending.Operations {
			if op.Request.Operation == operation {
				return nil, ErrPending
			}
		}
	}
	return nil, ErrInvalid
}
