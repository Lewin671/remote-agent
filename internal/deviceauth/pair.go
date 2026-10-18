package deviceauth

import (
	"bytes"
	"crypto/ed25519"
	"slices"
)

// PairTrustDescriptor is committed before the joiner's ephemeral key is known.
// It binds the immutable genesis anchor and the approver, not a mutable
// authority key.
func PairTrustDescriptor(p Pin, approver string) ([]byte, error) {
	if !deviceID(p.Account) || p.Domain == (Hash{}) || p.Genesis == (Hash{}) || !deviceID(approver) {
		return nil, ErrInvalid
	}
	e := encoder{b: []byte("ra3/pair-trust\x00")}
	e.str(p.Account)
	e.raw(p.Domain[:])
	e.raw(p.Genesis[:])
	e.str(approver)
	return e.b, nil
}

// ParsePairTrustDescriptor reads the public descriptor bound by the pairing
// commitment and the SAS transcript. It carries no secret.
func ParsePairTrustDescriptor(b []byte) (Pin, string, error) {
	var p Pin
	e := encoder{b: []byte("ra3/pair-trust\x00")}
	if len(e.b) > len(b) || !bytes.Equal(b[:len(e.b)], e.b) {
		return p, "", ErrInvalid
	}
	d := decoder{b: b[len(e.b):]}
	p.Account = d.str()
	p.Domain = d.hash()
	p.Genesis = d.hash()
	approver := d.str()
	if d.done() != nil || !deviceID(p.Account) || p.Domain == (Hash{}) || p.Genesis == (Hash{}) || !deviceID(approver) {
		return Pin{}, "", ErrInvalid
	}
	return p, approver, nil
}

// BuildMembershipState builds a normal state that changes the member list
// (add, remove, or X rotation) and seals the previous content key.
func BuildMembershipState(parent State, members []Member, removed []string, oldSecret, newSecret []byte, signer ed25519.PrivateKey, now uint64) (State, error) {
	if len(oldSecret) != 32 || len(newSecret) != 32 {
		return State{}, ErrInvalid
	}
	prev, err := sealPrevSecret(newSecret, oldSecret)
	if err != nil {
		return State{}, err
	}
	return BuildState(parent, members, removed, newSecret, prev, signer, now)
}

// BuildPairState adds one candidate. The caller (Android) has its own builder;
// this is the Go test/helper path.
func BuildPairState(parent State, candidate Member, candidateX []byte, oldSecret, secret []byte, signer ed25519.PrivateKey, now uint64) (State, error) {
	if candidate.validate() != nil || len(candidateX) != 32 || !bytes.Equal(candidate.XPublic[:], candidateX) || len(oldSecret) != 32 || len(secret) != 32 || len(signer) != 64 {
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
	prev, err := sealPrevSecret(secret, oldSecret)
	if err != nil {
		return State{}, err
	}
	return BuildState(parent, members, parent.Core.Removed, secret, prev, signer, now)
}
