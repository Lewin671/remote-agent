package e2e

import (
	"crypto/ecdh"
	"crypto/rand"
	"errors"

	"github.com/Lewin671/remote-agent/shared/devicesec"
)

// PairKey is one side's ephemeral X25519 key pair for pairing; the
// transcripts, SAS and key derivation are in shared/devicesec.
type PairKey struct{ priv *ecdh.PrivateKey }

// NewPairKey makes a fresh X25519 key pair.
func NewPairKey() (*PairKey, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &PairKey{priv: k}, nil
}

// pairKeyFrom makes a key pair from fixed bytes, for test vectors.
func pairKeyFrom(b []byte) (*PairKey, error) {
	k, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return nil, err
	}
	return &PairKey{priv: k}, nil
}

// Public is the public key to send.
func (k *PairKey) Public() []byte { return k.priv.PublicKey().Bytes() }

// ErrPairKey means the other side's public key is unusable.
var ErrPairKey = errors.New("e2e: bad pairing key")

// Derive computes the shared secrets with the other side's public key.
func (k *PairKey) Derive(otherPublic, transcript []byte) (*devicesec.PairSecrets, error) {
	pub, err := ecdh.X25519().NewPublicKey(otherPublic)
	if err != nil {
		return nil, ErrPairKey
	}
	shared, err := k.priv.ECDH(pub) // fails on an all-zero result
	if err != nil {
		return nil, ErrPairKey
	}
	return devicesec.DerivePairSecrets(shared, transcript)
}
