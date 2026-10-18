package deviceauth

import (
	"crypto/ed25519"
	"slices"
)

type Target struct {
	ID             string
	MetadataCommit Hash
}
type DeleteRequest struct {
	Account   string
	Domain    Hash
	Requester string
	Operation string
	Expected  Hash
	Nonce     Hash
	ExactIDs  bool
	Targets   []Target
	Signature []byte
}

func (r DeleteRequest) signBytes() ([]byte, error) {
	if !deviceID(r.Account) || r.Domain == (Hash{}) || !deviceID(r.Requester) || !validID(r.Operation) || len(r.Operation) < 16 || r.Expected == (Hash{}) || r.Nonce == (Hash{}) || len(r.Targets) == 0 || len(r.Targets) > MaxMembers {
		return nil, ErrInvalid
	}
	e := encoder{b: []byte("ra2/delete\x00")}
	e.str(r.Account)
	e.raw(r.Domain[:])
	e.str(r.Requester)
	e.str(r.Operation)
	e.raw(r.Expected[:])
	e.raw(r.Nonce[:])
	if r.ExactIDs {
		e.u32(1)
	} else {
		e.u32(0)
	}
	e.u32(uint32(len(r.Targets)))
	for i, t := range r.Targets {
		if !deviceID(t.ID) || i > 0 && r.Targets[i-1].ID >= t.ID || !r.ExactIDs && t.MetadataCommit == (Hash{}) {
			return nil, ErrInvalid
		}
		e.str(t.ID)
		e.raw(t.MetadataCommit[:])
	}
	return e.b, nil
}
func (r *DeleteRequest) Sign(ed ed25519.PrivateKey) error {
	b, err := r.signBytes()
	if err != nil || len(ed) != 64 || ID(ed.Public().(ed25519.PublicKey)) != r.Requester {
		return ErrInvalid
	}
	r.Signature = ed25519.Sign(ed, b)
	return nil
}
func (r DeleteRequest) verify(s State) (Hash, error) {
	b, err := r.signBytes()
	if err != nil {
		return Hash{}, err
	}
	m, ok := s.Core.Member(r.Requester)
	if !ok || r.Account != s.Core.Account || r.Domain != s.Core.Domain || !ed25519.Verify(m.EdPublic[:], b, r.Signature) {
		return Hash{}, ErrUntrusted
	}
	return digest("ra2/delete-digest\x00", b, r.Signature), nil
}

type GroupOperation struct {
	Request  DeleteRequest
	Digest   Hash
	NewState Hash
}
type RevocationGroup struct {
	ID         string
	Base       State
	Operations []GroupOperation
	Head       State
	Required   []string
	Receipts   []FenceReceipt
	Complete   bool
}

func (g RevocationGroup) revision() uint64 { return uint64(len(g.Operations)) }
func required(base, head State) []string {
	out := []string{}
	for _, m := range base.Core.Members {
		if m.Kind == Machine {
			if _, ok := head.Core.Member(m.ID); ok {
				out = append(out, m.ID)
			}
		}
	}
	return out
}

type FenceRequest struct {
	Account     string
	Domain      Hash
	AuthorityID string
	Group       string
	Revision    uint64
	Epoch       uint64
	State       Hash
	Executor    string
	Signature   []byte
}

func (r FenceRequest) signBytes() ([]byte, error) {
	if !deviceID(r.Account) || r.Domain == (Hash{}) || !deviceID(r.AuthorityID) || !validID(r.Group) || r.Revision == 0 || r.Revision > MaxMembers || r.Epoch < 2 || r.State == (Hash{}) || !deviceID(r.Executor) {
		return nil, ErrInvalid
	}
	e := encoder{b: []byte("ra2/fence-request\x00")}
	e.str(r.Account)
	e.raw(r.Domain[:])
	e.str(r.AuthorityID)
	e.str(r.Group)
	e.u64(r.Revision)
	e.u64(r.Epoch)
	e.raw(r.State[:])
	e.str(r.Executor)
	return e.b, nil
}
func (r FenceRequest) Verify(p Pin) error {
	b, err := r.signBytes()
	if err != nil {
		return err
	}
	if r.Account != p.Account || r.Domain != p.Domain || r.AuthorityID != p.AuthorityID || !ed25519.Verify(p.AuthorityEd[:], b, r.Signature) {
		return ErrUntrusted
	}
	return nil
}

type FenceReceipt struct {
	Account     string
	Domain      Hash
	AuthorityID string
	Executor    string
	Group       string
	Revision    uint64
	Epoch       uint64
	State       Hash
	Signature   []byte
}

func (r FenceReceipt) signBytes() ([]byte, error) {
	q := FenceRequest{Account: r.Account, Domain: r.Domain, AuthorityID: r.AuthorityID, Group: r.Group, Revision: r.Revision, Epoch: r.Epoch, State: r.State, Executor: r.Executor}
	b, err := q.signBytes()
	if err != nil {
		return nil, err
	}
	return append([]byte("ra2/fence-receipt\x00"), b[len("ra2/fence-request\x00"):]...), nil
}
func (r FenceReceipt) verify(p Pin, g RevocationGroup) error {
	b, err := r.signBytes()
	if err != nil {
		return err
	}
	sh, _ := g.Head.Hash()
	m, ok := g.Head.Core.Member(r.Executor)
	if !ok || m.Kind != Machine || r.Account != p.Account || r.Domain != p.Domain || r.AuthorityID != p.AuthorityID || r.Group != g.ID || r.Revision != g.revision() || r.Epoch != g.Head.Core.Epoch || r.State != sh || !slices.Contains(g.Required, r.Executor) || !ed25519.Verify(m.EdPublic[:], b, r.Signature) {
		return ErrUntrusted
	}
	return nil
}
func (e *Endpoint) Fence(req FenceRequest, chain []State) (FenceReceipt, error) {
	p, s, id := e.Snapshot()
	if req.Verify(p) != nil || req.Executor != id {
		return FenceReceipt{}, ErrUntrusted
	}
	if len(chain) > 0 {
		s = chain[len(chain)-1]
	}
	sh, err := s.Hash()
	if err != nil || req.State != sh || req.Epoch != s.Core.Epoch {
		return FenceReceipt{}, ErrStale
	}
	return e.install(chain, req.Group, req.Revision)
}
func (e *Endpoint) Sync(chain []State) error { _, err := e.install(chain, "", 0); return err }

type Completion struct {
	Group     RevocationGroup
	Signature []byte
}

func (c Completion) signBytes() ([]byte, error) {
	g := c.Group
	if !g.Complete || !validID(g.ID) || len(g.Operations) == 0 || len(g.Operations) > MaxMembers || !slices.Equal(g.Required, required(g.Base, g.Head)) || len(g.Receipts) != len(g.Required) {
		return nil, ErrInvalid
	}
	bh, err := g.Base.Hash()
	if err != nil {
		return nil, err
	}
	hh, err := g.Head.Hash()
	if err != nil {
		return nil, err
	}
	e := encoder{b: []byte("ra2/completion\x00")}
	e.str(g.Head.Core.Account)
	e.raw(g.Head.Core.Domain[:])
	e.str(g.Head.Core.AuthorityID)
	e.str(g.ID)
	e.raw(bh[:])
	e.raw(hh[:])
	e.u32(uint32(len(g.Operations)))
	for _, op := range g.Operations {
		e.str(op.Request.Operation)
		e.raw(op.Digest[:])
		e.raw(op.NewState[:])
	}
	e.u32(uint32(len(g.Required)))
	for i, id := range g.Required {
		if i > 0 && g.Required[i-1] >= id || g.Receipts[i].Executor != id {
			return nil, ErrInvalid
		}
		e.str(id)
		b, err := g.Receipts[i].signBytes()
		if err != nil || len(g.Receipts[i].Signature) != 64 {
			return nil, ErrInvalid
		}
		e.lp(b)
		e.lp(g.Receipts[i].Signature)
	}
	return e.b, nil
}

// Verify never changes the caller's state head. chain is fetched in bounded
// pages and must connect Base through EVERY operation to the exact final head.
func (c Completion) Verify(p Pin, original DeleteRequest, chain []State) error {
	b, err := c.signBytes()
	if err != nil || !ed25519.Verify(p.AuthorityEd[:], b, c.Signature) {
		return ErrUntrusted
	}
	g := c.Group
	if len(chain) != len(g.Operations) || p.Verify(g.Base) != nil {
		return ErrInvalid
	}
	s := g.Base
	found := false
	union := map[string]bool{}
	for i, op := range g.Operations {
		if err := p.Advance(s, chain[i]); err != nil {
			return err
		}
		sh, _ := s.Hash()
		nh, _ := chain[i].Hash()
		dh, err := op.Request.verify(s)
		if err != nil || op.Digest != dh || op.NewState != nh || op.Request.Expected != sh {
			return ErrUntrusted
		}
		ids := []string{}
		for _, t := range op.Request.Targets {
			ids = append(ids, t.ID)
			union[t.ID] = true
		}
		if !slices.Equal(ids, chain[i].Core.Removed) {
			return ErrUntrusted
		}
		if op.Request.Operation == original.Operation {
			ob, err := original.signBytes()
			if err != nil || dh != digest("ra2/delete-digest\x00", ob, original.Signature) {
				return ErrUntrusted
			}
			found = true
		}
		s = chain[i]
	}
	hh, _ := g.Head.Hash()
	sh, _ := s.Hash()
	if !found || hh != sh {
		return ErrUntrusted
	}
	for id := range union {
		if _, ok := g.Head.Core.Member(id); ok {
			return ErrUntrusted
		}
	}
	for _, r := range g.Receipts {
		if r.verify(p, g) != nil {
			return ErrUntrusted
		}
	}
	return nil
}

// UnnamedMemberCommit binds a GUI's fixed-ID placeholder when no authenticated
// name exists yet. A concurrent first metadata write makes this binding stale.
func UnnamedMemberCommit(s State, m Member) Hash {
	sh, err := s.Hash()
	if err != nil || m.validate() != nil {
		return Hash{}
	}
	e := encoder{}
	e.raw(sh[:])
	e.str(m.ID)
	e.raw(m.EdPublic[:])
	e.raw(m.XPublic[:])
	e.u32(uint32(m.Kind))
	return digest("ra2/unnamed-member\x00", e.b)
}
