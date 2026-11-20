package devicesec

import (
	"io"
	"slices"
)

// handoffDelta retains every other member exactly, and retires the old phone.
func handoffDelta(parent, next State) error {
	if _, ok := parent.Core.Member(next.Core.AuthorityID); !ok {
		return ErrInvalid
	}
	return recoveryDelta(parent, next)
}

// NewHandoff builds the unsigned state the retiring authority signs; the
// incoming phone is an already paired member and has confirmed its new
// emergency code offline. The previous content key is sealed with a nonce
// from random.
func NewHandoff(parent State, incoming string, recoveryEd [32]byte, oldSecret, newSecret []byte, now uint64, hpke HPKESender, random io.Reader) (State, error) {
	if incoming == parent.Core.AuthorityID || recoveryEd == parent.Core.RecoveryEd || recoveryEd == [32]byte{} {
		return State{}, ErrInvalid
	}
	m, ok := parent.Core.Member(incoming)
	if !ok || m.Kind != Client {
		return State{}, ErrInvalid
	}
	members := []Member{}
	for _, m := range parent.Core.Members {
		if m.ID != parent.Core.AuthorityID {
			members = append(members, m)
		}
	}
	removed := append([]string{}, parent.Core.Removed...)
	removed = append(removed, parent.Core.AuthorityID)
	prev, err := SealPrevSecret(newSecret, oldSecret, random)
	if err != nil {
		return State{}, err
	}
	c := parent.Core
	c.Parent, _ = parent.Hash()
	c.Epoch++
	c.ContentEpoch++
	c.IssuedAt = max(now, c.IssuedAt)
	c.Members = members
	c.Removed = removed
	c.AuthorityID = incoming
	c.RecoveryEd = recoveryEd
	slices.Sort(c.Removed)
	return NewStateForCore(c, newSecret, prev, hpke)
}
