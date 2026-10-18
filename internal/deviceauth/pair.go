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
