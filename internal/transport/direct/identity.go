package direct

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"sync"

	"github.com/Lewin671/remote-agent/shared/peerwire"
)

// Identity is this daemon process's peer identity and its private keys.
// shared/peerwire is key-free, so the daemon runs its key operations here,
// synchronously: X25519 with each member (cached by the member's X public
// key), frame entropy from crypto/rand, and the Ed25519 signature that
// completes a prepared frame.
type Identity struct {
	ID, Instance, Boot []byte
	Role               uint64
	Sign               ed25519.PrivateKey
	Box                *ecdh.PrivateKey

	mu     sync.Mutex
	agreed map[string][]byte
}

func (id *Identity) Public() peerwire.Public {
	return peerwire.Public{ID: id.ID, Instance: id.Instance, Boot: id.Boot, Role: id.Role, Sign: id.Sign.Public().(ed25519.PublicKey), Box: id.Box.PublicKey().Bytes()}
}

// Shared returns the X25519 secret with peer's member key. Membership bounds
// the cache; a removed member's entry is harmless and dropped on overflow.
func (id *Identity) Shared(peer peerwire.Public) ([]byte, error) {
	if id == nil || id.Box == nil || len(id.Sign) != ed25519.PrivateKeySize {
		return nil, peerwire.ErrPacket
	}
	id.mu.Lock()
	defer id.mu.Unlock()
	if s, ok := id.agreed[string(peer.Box)]; ok {
		return s, nil
	}
	key, err := ecdh.X25519().NewPublicKey(peer.Box)
	if err != nil {
		return nil, peerwire.ErrPacket
	}
	s, err := id.Box.ECDH(key)
	if err != nil {
		return nil, peerwire.ErrPacket
	}
	if id.agreed == nil || len(id.agreed) >= 256 {
		id.agreed = map[string][]byte{}
	}
	id.agreed[string(peer.Box)] = s
	return s, nil
}

// Seal encrypts, signs and frames one packet to peer.
func (id *Identity) Seal(codec *peerwire.Codec, to peerwire.Public, c peerwire.Context, typ uint64, payload []byte) ([]byte, error) {
	shared, err := id.Shared(to)
	if err != nil {
		return nil, err
	}
	entropy := make([]byte, peerwire.SealEntropy)
	if _, err = rand.Read(entropy); err != nil {
		return nil, err
	}
	u, err := codec.Prepare(id.Public(), to, shared, c, typ, payload, entropy)
	if err != nil {
		return nil, err
	}
	return u.Finish(ed25519.Sign(id.Sign, u.Message))
}

// Open authenticates a packet from peer addressed to this process.
func (id *Identity) Open(codec *peerwire.Codec, raw []byte, from peerwire.Public, now uint64) (peerwire.Opened, error) {
	shared, err := id.Shared(from)
	if err != nil {
		return peerwire.Opened{}, err
	}
	return codec.Open(raw, from, id.Public(), shared, now)
}

// OpenSent re-authenticates a packet this process sealed for peer.
func (id *Identity) OpenSent(codec *peerwire.Codec, raw []byte, to peerwire.Public, now uint64) (peerwire.Opened, error) {
	shared, err := id.Shared(to)
	if err != nil {
		return peerwire.Opened{}, err
	}
	return codec.OpenSent(raw, id.Public(), to, shared, now)
}
