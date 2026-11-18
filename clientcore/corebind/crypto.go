//go:build !js

package corebind

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"errors"
)

var errBadKeySize = errors.New("corebind: invalid key size")

// Ed25519PublicKey returns the 32-byte Ed25519 public key for a 32-byte seed.
func Ed25519PublicKey(seed []byte) ([]byte, error) {
	s := bytes.Clone(seed)
	if len(s) != ed25519.SeedSize {
		return nil, errBadKeySize
	}
	priv := ed25519.NewKeyFromSeed(s)
	return bytes.Clone(priv[ed25519.SeedSize:]), nil
}

// Ed25519Sign signs message with a 32-byte Ed25519 seed.
func Ed25519Sign(seed, message []byte) ([]byte, error) {
	s := bytes.Clone(seed)
	m := bytes.Clone(message)
	if len(s) != ed25519.SeedSize {
		return nil, errBadKeySize
	}
	priv := ed25519.NewKeyFromSeed(s)
	return ed25519.Sign(priv, m), nil
}

// Ed25519Verify reports whether signature is valid for message under publicKey.
func Ed25519Verify(publicKey, message, signature []byte) bool {
	pub := bytes.Clone(publicKey)
	m := bytes.Clone(message)
	sig := bytes.Clone(signature)
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, m, sig)
}

// X25519PublicKey returns the 32-byte X25519 public key for a 32-byte private key.
func X25519PublicKey(privateKey []byte) ([]byte, error) {
	privBytes := bytes.Clone(privateKey)
	priv, err := ecdh.X25519().NewPrivateKey(privBytes)
	if err != nil {
		return nil, err
	}
	return priv.PublicKey().Bytes(), nil
}

// X25519Shared computes the 32-byte X25519 shared secret and rejects low-order peer keys.
func X25519Shared(privateKey, peerPublic []byte) ([]byte, error) {
	privBytes := bytes.Clone(privateKey)
	pubBytes := bytes.Clone(peerPublic)
	priv, err := ecdh.X25519().NewPrivateKey(privBytes)
	if err != nil {
		return nil, err
	}
	pub, err := ecdh.X25519().NewPublicKey(pubBytes)
	if err != nil {
		return nil, err
	}
	return priv.ECDH(pub)
}
