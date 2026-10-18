package deviceauth

import (
	"crypto/ed25519"
	"slices"
)

// PairContext is created and kept by the authority BEFORE SAS confirmation.
// The SAS transcript MUST include Binding(); a relay registration is not Join.
type PairContext struct {
	ID          string
	Account     string
	Domain      Hash
	Genesis     Hash
	AuthorityID string
	AuthorityEd [32]byte
	AuthorityX  [32]byte
	Approver    string
	Candidate   Member
	Expected    Hash
	Nonce       Hash
	Expires     uint64
	Canceled    bool
	Consumed    bool
	Joined      State
	Signature   []byte
}

func (p PairContext) Binding() ([]byte, error) {
	if !validID(p.ID) || len(p.ID) < 16 || !deviceID(p.Account) || p.Domain == (Hash{}) || p.Genesis == (Hash{}) || !deviceID(p.AuthorityID) || !deviceID(p.Approver) || p.Candidate.validate() != nil || p.Expected == (Hash{}) || p.Nonce == (Hash{}) || p.Expires == 0 {
		return nil, ErrInvalid
	}
	e := encoder{b: []byte("ra2/pair-context\x00")}
	e.str(p.ID)
	e.str(p.Account)
	e.raw(p.Domain[:])
	e.raw(p.Genesis[:])
	e.str(p.AuthorityID)
	e.raw(p.AuthorityEd[:])
	e.raw(p.AuthorityX[:])
	e.str(p.Approver)
	e.str(p.Candidate.ID)
	e.raw(p.Candidate.EdPublic[:])
	e.raw(p.Candidate.XPublic[:])
	e.u32(uint32(p.Candidate.Kind))
	e.raw(p.Expected[:])
	e.raw(p.Nonce[:])
	e.u64(p.Expires)
	return e.b, nil
}
func (p PairContext) Verify(pin Pin) error {
	b, err := p.Binding()
	if err != nil {
		return err
	}
	if p.Account != pin.Account || p.Domain != pin.Domain || p.Genesis != pin.Genesis || p.AuthorityID != pin.AuthorityID || p.AuthorityEd != pin.AuthorityEd || p.AuthorityX != pin.AuthorityX || !ed25519.Verify(pin.AuthorityEd[:], b, p.Signature) {
		return ErrUntrusted
	}
	return nil
}
func (a *Authority) BeginPair(id, approver string, candidate Member) (PairContext, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.disk.check(); err != nil {
		return PairContext{}, err
	}
	d := clone(a.data)
	if d.Pending != nil {
		return PairContext{}, ErrPending
	}
	if _, ok := d.State.Core.Member(approver); !ok {
		return PairContext{}, ErrUntrusted
	}
	if candidate.validate() != nil {
		return PairContext{}, ErrInvalid
	}
	if _, ok := d.State.Core.Member(candidate.ID); ok {
		return PairContext{}, ErrConflict
	}
	if len(d.State.Core.Members) >= MaxMembers {
		return PairContext{}, ErrCapacity
	}
	now := uint64(a.now().UnixMilli())
	for key, p := range d.Contexts {
		if p.Expires <= now {
			delete(d.Contexts, key)
		}
	}
	if _, ok := d.Contexts[id]; ok {
		return PairContext{}, ErrConflict
	}
	if len(d.Contexts) >= MaxMembers {
		return PairContext{}, ErrCapacity
	}
	sh, _ := d.State.Hash()
	nonce, err := randomHash()
	if err != nil {
		return PairContext{}, err
	}
	p := PairContext{ID: id, Account: d.Pin.Account, Domain: d.Pin.Domain, Genesis: d.Pin.Genesis, AuthorityID: d.Pin.AuthorityID, AuthorityEd: d.Pin.AuthorityEd, AuthorityX: d.Pin.AuthorityX, Approver: approver, Candidate: candidate, Expected: sh, Nonce: nonce, Expires: now + 5*60000}
	b, err := p.Binding()
	if err != nil {
		return PairContext{}, err
	}
	p.Signature = ed25519.Sign(ed25519.NewKeyFromSeed(d.EdSeed), b)
	d.Contexts[id] = p
	if err = a.save(d, nil); err != nil {
		return PairContext{}, err
	}
	return p, nil
}

// ApprovePair requires signatures from BOTH SAS participants over the same full
// binding. Replaying an old signed certificate cannot reconstruct a context.
func (a *Authority) ApprovePair(id string, approverSig, candidateSig []byte) (State, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.disk.check(); err != nil {
		return State{}, err
	}
	d := clone(a.data)
	if d.Pending != nil {
		return State{}, ErrPending
	}
	p, ok := d.Contexts[id]
	if !ok || p.Canceled || p.Expires <= uint64(a.now().UnixMilli()) {
		return State{}, ErrUntrusted
	}
	b, err := p.Binding()
	if err != nil || p.Verify(d.Pin) != nil {
		return State{}, ErrUntrusted
	}
	m, ok := d.State.Core.Member(p.Approver)
	if !ok || !ed25519.Verify(m.EdPublic[:], append([]byte("ra2/pair-approve\x00"), b...), approverSig) || !ed25519.Verify(p.Candidate.EdPublic[:], append([]byte("ra2/pair-accept\x00"), b...), candidateSig) {
		return State{}, ErrUntrusted
	}
	if p.Consumed {
		if _, ok := d.State.Core.Member(p.Candidate.ID); !ok {
			return State{}, ErrUntrusted
		}
		return clone(p.Joined), nil
	}
	sh, _ := d.State.Hash()
	if p.Expected != sh {
		return State{}, ErrStale
	}
	if _, ok := d.State.Core.Member(p.Candidate.ID); ok {
		return State{}, ErrConflict
	}
	members := append(slices.Clone(d.State.Core.Members), p.Candidate)
	slices.SortFunc(members, func(x, y Member) int {
		if x.ID < y.ID {
			return -1
		}
		if x.ID > y.ID {
			return 1
		}
		return 0
	})
	s, secret, err := a.nextState(d, members, nil)
	if err != nil {
		return State{}, err
	}
	p.Consumed = true
	p.Joined = s
	d.Contexts[id] = p
	d.State = s
	d.Secrets[s.Core.ContentEpoch] = secret
	if err = a.save(d, &s); err != nil {
		return State{}, err
	}
	return clone(s), nil
}
func (a *Authority) CancelPair(id string, signature []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.disk.check(); err != nil {
		return err
	}
	d := clone(a.data)
	p, ok := d.Contexts[id]
	if !ok {
		return ErrUntrusted
	}
	m, ok := d.State.Core.Member(p.Approver)
	b, err := p.Binding()
	if !ok || err != nil || !ed25519.Verify(m.EdPublic[:], append([]byte("ra2/pair-cancel\x00"), b...), signature) {
		return ErrUntrusted
	}
	if p.Consumed {
		return ErrConflict
	}
	p.Canceled = true
	d.Contexts[id] = p
	return a.save(d, nil)
}
func SignPair(p PairContext, ed ed25519.PrivateKey, accept bool) ([]byte, error) {
	b, err := p.Binding()
	if err != nil || len(ed) != 64 {
		return nil, ErrInvalid
	}
	label := "ra2/pair-approve\x00"
	id := p.Approver
	if accept {
		label = "ra2/pair-accept\x00"
		id = p.Candidate.ID
	}
	if ID(ed.Public().(ed25519.PublicKey)) != id {
		return nil, ErrInvalid
	}
	return ed25519.Sign(ed, append([]byte(label), b...)), nil
}

func (p PairContext) Pin() Pin {
	return Pin{Account: p.Account, Domain: p.Domain, Genesis: p.Genesis, AuthorityID: p.AuthorityID, AuthorityEd: p.AuthorityEd, AuthorityX: p.AuthorityX}
}

// PairTrustDescriptor is committed BEFORE the joiner's ephemeral public key is
// known. It contains every SAS trust field, but deliberately no late nonce,
// expiry, state head or signature: a malicious fake authority must not gain a
// freely grindable post-DH field with which to search for a matching short SAS.
// Those operation fields are authenticated separately by PairContext.Binding.
func PairTrustDescriptor(p Pin, approver string) ([]byte, error) {
	if !deviceID(p.Account) || p.Domain == (Hash{}) || p.Genesis == (Hash{}) || !deviceID(p.AuthorityID) || p.AuthorityEd == [32]byte{} || p.AuthorityX == [32]byte{} || !deviceID(approver) {
		return nil, ErrInvalid
	}
	e := encoder{b: []byte("ra2/pair-trust\x00")}
	e.str(p.Account)
	e.raw(p.Domain[:])
	e.raw(p.Genesis[:])
	e.str(p.AuthorityID)
	e.raw(p.AuthorityEd[:])
	e.raw(p.AuthorityX[:])
	e.str(approver)
	return e.b, nil
}
