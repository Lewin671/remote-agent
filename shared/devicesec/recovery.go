package devicesec

import (
	"bytes"
	"crypto/ed25519"
	"io"
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

	StateHash Hash
	Nonce     Hash
	Signature []byte
}

// SigningBytes is what the parent's recovery key signs into Signature.
func (r RecoveryStatement) SigningBytes() ([]byte, error) {
	if !deviceID(r.Account) || r.Domain == (Hash{}) || r.Parent == (Hash{}) || !deviceID(r.NewAuthorityID) || r.NewAuthorityID != r.NewMember.ID || r.NewMember.Kind != Client || r.NewMember.validate() != nil || r.NewRecoveryEd == [32]byte{} || r.IssuedAt == 0 || r.StateHash == (Hash{}) || r.Nonce == (Hash{}) {
		return nil, ErrInvalid
	}
	e := Encoder{B: []byte("ra3/recovery\x00")}
	e.Str(r.Account)
	e.Raw(r.Domain[:])
	e.Raw(r.Parent[:])
	e.Str(r.NewAuthorityID)
	e.Raw(r.NewRecoveryEd[:])
	e.Str(r.NewMember.ID)
	e.Raw(r.NewMember.EdPublic[:])
	e.Raw(r.NewMember.XPublic[:])
	e.U32(uint32(r.NewMember.Kind))
	e.U64(r.IssuedAt)

	e.Raw(r.StateHash[:])
	e.Raw(r.Nonce[:])
	return e.B, nil
}
func (r RecoveryStatement) MarshalBinary() ([]byte, error) {
	b, err := r.SigningBytes()
	if err != nil || len(r.Signature) != ed25519.SignatureSize {
		return nil, ErrInvalid
	}
	e := Encoder{}
	e.LP(b)
	e.LP(r.Signature)
	if len(e.B) > MaxStateBytes {
		return nil, ErrCapacity
	}
	return e.B, nil
}

func ParseRecoveryStatement(b []byte) (r RecoveryStatement, err error) {
	if len(b) > MaxStateBytes {
		return r, ErrInvalid
	}
	d := decoder{b: b}
	body := d.lp(MaxStateBytes)
	sig := d.lp(ed25519.SignatureSize)
	if d.done() != nil {
		return r, ErrInvalid
	}
	if len(sig) != ed25519.SignatureSize {
		return r, ErrInvalid
	}
	label := []byte("ra3/recovery\x00")
	if len(body) < len(label) || !bytes.Equal(body[:len(label)], label) {
		return r, ErrInvalid
	}
	s := decoder{b: body[len(label):]}
	r.Account = s.str()
	r.Domain = s.hash()
	r.Parent = s.hash()
	r.NewAuthorityID = s.str()
	copy(r.NewRecoveryEd[:], s.raw(32))
	r.NewMember.ID = s.str()
	copy(r.NewMember.EdPublic[:], s.raw(32))
	copy(r.NewMember.XPublic[:], s.raw(32))
	r.NewMember.Kind = Kind(s.u32())
	r.IssuedAt = s.u64()

	r.StateHash = s.hash()
	r.Nonce = s.hash()
	if s.done() != nil {
		return r, ErrInvalid
	}
	r.Signature = sig
	// Re-encoding must reproduce the transmitted bytes exactly; anything
	// else would let a signature cover different content than the fields.
	again, err := r.SigningBytes()
	if err != nil || !bytes.Equal(again, body) {
		return RecoveryStatement{}, ErrInvalid
	}
	return r, nil
}

func (r RecoveryStatement) Verify(parent, next State) error {
	b, err := r.SigningBytes()
	if err != nil || len(r.Signature) != 64 {
		return ErrInvalid
	}
	ph, _ := parent.Hash()
	nh, _ := next.Hash()
	if r.Account != parent.Core.Account || r.Domain != parent.Core.Domain || r.Parent != ph || r.NewAuthorityID != next.Core.AuthorityID || r.NewRecoveryEd != next.Core.RecoveryEd || r.NewMember != memberOf(next, next.Core.AuthorityID) || r.IssuedAt != next.Core.IssuedAt || r.StateHash != nh {
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

// NewRecovery builds the unsigned recovery state; the new authority signs its
// SigningBytes. The new state removes the old authority client and never
// re-adds it.
func NewRecovery(parent State, newMember Member, members []Member, secret []byte, newRecoveryEd [32]byte, now uint64, hpke HPKESender) (State, error) {
	if newMember.validate() != nil || newMember.Kind != Client || newRecoveryEd == [32]byte{} || len(secret) != 32 {
		return State{}, ErrInvalid
	}
	if slices.ContainsFunc(members, func(m Member) bool { return m.ID == parent.Core.AuthorityID }) {
		return State{}, ErrInvalid
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
	c.IssuedAt = max(now, c.IssuedAt)

	c.Members = slices.Clone(members)
	slices.SortFunc(c.Members, func(a, b Member) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	c.Removed = removed
	return NewStateForCore(c, secret, nil, hpke)
}

// NewRecoveryStatement builds the unsigned statement authorizing exactly next
// (signed) after parent, with a 32-byte nonce read from random. The parent's
// recovery key signs its SigningBytes.
func NewRecoveryStatement(parent, next State, random io.Reader) (RecoveryStatement, error) {
	ph, err := parent.Hash()
	if err != nil {
		return RecoveryStatement{}, err
	}
	h, err := next.Hash()
	if err != nil {
		return RecoveryStatement{}, err
	}
	var nonce Hash
	if _, err := io.ReadFull(random, nonce[:]); err != nil {
		return RecoveryStatement{}, err
	}
	c := next.Core
	r := RecoveryStatement{Account: c.Account, Domain: c.Domain, Parent: ph, NewAuthorityID: c.AuthorityID, NewRecoveryEd: c.RecoveryEd, NewMember: memberOf(next, c.AuthorityID), IssuedAt: c.IssuedAt, StateHash: h, Nonce: nonce}
	if _, err := r.SigningBytes(); err != nil {
		return RecoveryStatement{}, err
	}
	return r, nil
}
