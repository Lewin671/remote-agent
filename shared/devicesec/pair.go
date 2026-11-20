package devicesec

import (
	"bytes"
	"io"
	"slices"
)

// PairTrustDescriptor is committed before the joiner's ephemeral key is known.
// It binds the immutable genesis anchor and the approver, not a mutable
// authority key.
func PairTrustDescriptor(p Pin, approver string) ([]byte, error) {
	if !deviceID(p.Account) || p.Domain == (Hash{}) || p.Genesis == (Hash{}) || !deviceID(approver) {
		return nil, ErrInvalid
	}
	e := Encoder{B: []byte("ra3/pair-trust\x00")}
	e.Str(p.Account)
	e.Raw(p.Domain[:])
	e.Raw(p.Genesis[:])
	e.Str(approver)
	return e.B, nil
}

// ParsePairTrustDescriptor reads the public descriptor bound by the pairing
// commitment and the SAS transcript. It carries no secret.
func ParsePairTrustDescriptor(b []byte) (Pin, string, error) {
	var p Pin
	e := Encoder{B: []byte("ra3/pair-trust\x00")}
	if len(e.B) > len(b) || !bytes.Equal(b[:len(e.B)], e.B) {
		return p, "", ErrInvalid
	}
	d := decoder{b: b[len(e.B):]}
	p.Account = d.str()
	p.Domain = d.hash()
	p.Genesis = d.hash()
	approver := d.str()
	if d.done() != nil || !deviceID(p.Account) || p.Domain == (Hash{}) || p.Genesis == (Hash{}) || !deviceID(approver) {
		return Pin{}, "", ErrInvalid
	}
	return p, approver, nil
}

// NewMembershipState builds the unsigned normal state that changes the member
// list (add, remove, or X rotation) and seals the previous content key with a
// nonce from random.
func NewMembershipState(parent State, members []Member, removed []string, oldSecret, newSecret []byte, now uint64, hpke HPKESender, random io.Reader) (State, error) {
	if len(oldSecret) != 32 || len(newSecret) != 32 {
		return State{}, ErrInvalid
	}
	prev, err := SealPrevSecret(newSecret, oldSecret, random)
	if err != nil {
		return State{}, err
	}
	return NewState(parent, members, removed, newSecret, prev, now, hpke)
}

// NewPairState builds the unsigned state that adds one candidate.
func NewPairState(parent State, candidate Member, candidateX []byte, oldSecret, secret []byte, now uint64, hpke HPKESender, random io.Reader) (State, error) {
	if candidate.validate() != nil || len(candidateX) != 32 || !bytes.Equal(candidate.XPublic[:], candidateX) || len(oldSecret) != 32 || len(secret) != 32 {
		return State{}, ErrInvalid
	}
	if _, ok := parent.Core.Member(candidate.ID); ok {
		return State{}, ErrInvalid
	}
	members := append(slices.Clone(parent.Core.Members), candidate)
	slices.SortFunc(members, func(a, b Member) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	prev, err := SealPrevSecret(secret, oldSecret, random)
	if err != nil {
		return State{}, err
	}
	return NewState(parent, members, parent.Core.Removed, secret, prev, now, hpke)
}
