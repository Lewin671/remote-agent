package deviceauth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"slices"
)

// RecoveryStatement is signed by the old phone's recovery key. It authorizes
// exactly one state that moves AuthorityID to a new phone member.
type RecoveryStatement struct {
	Account        string
	Domain         Hash
	Parent         Hash
	NewAuthorityID string
	NewRecoveryEd  [32]byte
	NewMember      Member
	IssuedAt       uint64
	NotAfter       uint64
	StateHash      Hash
	Nonce          Hash
	Signature      []byte
}

func (r RecoveryStatement) signBytes() ([]byte, error) {
	if !deviceID(r.Account) || r.Domain == (Hash{}) || r.Parent == (Hash{}) || !deviceID(r.NewAuthorityID) || r.NewAuthorityID != r.NewMember.ID || r.NewMember.Kind != Client || r.NewMember.validate() != nil || r.NewRecoveryEd == [32]byte{} || r.IssuedAt == 0 || r.NotAfter <= r.IssuedAt || r.StateHash == (Hash{}) || r.Nonce == (Hash{}) {
		return nil, ErrInvalid
	}
	e := encoder{b: []byte("ra3/recovery\x00")}
	e.str(r.Account)
	e.raw(r.Domain[:])
	e.raw(r.Parent[:])
	e.str(r.NewAuthorityID)
	e.raw(r.NewRecoveryEd[:])
	e.str(r.NewMember.ID)
	e.raw(r.NewMember.EdPublic[:])
	e.raw(r.NewMember.XPublic[:])
	e.u32(uint32(r.NewMember.Kind))
	e.u64(r.IssuedAt)
	e.u64(r.NotAfter)
	e.raw(r.StateHash[:])
	e.raw(r.Nonce[:])
	return e.b, nil
}
func (r *RecoveryStatement) Sign(oldRecovery ed25519.PrivateKey) error {
	b, err := r.signBytes()
	if err != nil || len(oldRecovery) != 64 {
		return ErrInvalid
	}
	r.Signature = ed25519.Sign(oldRecovery, b)
	return nil
}
func (r RecoveryStatement) Verify(parent, next State) error {
	b, err := r.signBytes()
	if err != nil || len(r.Signature) != 64 {
		return ErrInvalid
	}
	ph, _ := parent.Hash()
	nh, _ := next.Hash()
	if r.Account != parent.Core.Account || r.Domain != parent.Core.Domain || r.Parent != ph || r.NewAuthorityID != next.Core.AuthorityID || r.NewRecoveryEd != next.Core.RecoveryEd || r.NewMember != memberOf(next, next.Core.AuthorityID) || r.IssuedAt != next.Core.IssuedAt || r.NotAfter != next.Core.NotAfter || r.StateHash != nh {
		return ErrUntrusted
	}
	if !ed25519.Verify(parent.Core.RecoveryEd[:], b, r.Signature) {
		return ErrUntrusted
	}
	return nil
}
func memberOf(s State, id string) Member {
	m, _ := s.Core.Member(id)
	return m
}

// BuildRecovery builds the recovery state and its statement. The new state
// removes the old authority phone and never re-adds it.
func BuildRecovery(parent State, newMember Member, members []Member, secret []byte, newSigner ed25519.PrivateKey, oldRecovery ed25519.PrivateKey, newRecoveryEd [32]byte, now uint64) (State, RecoveryStatement, error) {
	var st RecoveryStatement
	if newMember.validate() != nil || newMember.Kind != Client || len(newSigner) != 64 || !bytes.Equal(newSigner.Public().(ed25519.PublicKey), newMember.EdPublic[:]) || newRecoveryEd == [32]byte{} || len(secret) != 32 {
		return State{}, st, ErrInvalid
	}
	if slices.ContainsFunc(members, func(m Member) bool { return m.ID == parent.Core.AuthorityID }) {
		return State{}, st, ErrInvalid
	}
	removed := append(slices.Clone(parent.Core.Removed), parent.Core.AuthorityID)
	slices.Sort(removed)
	ph, _ := parent.Hash()
	c := parent.Core
	c.Parent = ph
	c.Epoch++
	c.ContentEpoch++
	c.AuthorityID = newMember.ID
	c.RecoveryEd = newRecoveryEd
	c.IssuedAt = maxU64(now, c.IssuedAt)
	c.NotAfter = addLease(c.IssuedAt)
	c.Members = members
	c.Removed = removed
	s, err := build(c, secret, nil, newSigner)
	if err != nil {
		return State{}, st, err
	}
	h, _ := s.Hash()
	var nonce Hash
	if _, err := rand.Read(nonce[:]); err != nil {
		return State{}, st, err
	}
	st = RecoveryStatement{Account: c.Account, Domain: c.Domain, Parent: ph, NewAuthorityID: newMember.ID, NewRecoveryEd: newRecoveryEd, NewMember: newMember, IssuedAt: c.IssuedAt, NotAfter: c.NotAfter, StateHash: h, Nonce: nonce}
	if err := st.Sign(oldRecovery); err != nil {
		return State{}, st, err
	}
	return s, st, nil
}
