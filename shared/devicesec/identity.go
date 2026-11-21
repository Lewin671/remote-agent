package devicesec

import "crypto/ed25519"

const (
	Ed25519PublicKeySize = ed25519.PublicKeySize
	Ed25519SeedSize      = ed25519.SeedSize
	Ed25519SignatureSize = ed25519.SignatureSize
)

// VerifyEd25519 verifies an Ed25519 signature over message.
func VerifyEd25519(pub, msg, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, msg, sig)
}

// Ed25519PublicFromSeed derives the 32-byte Ed25519 public key from a 32-byte seed.
func Ed25519PublicFromSeed(seed []byte) ([]byte, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, ErrInvalid
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	out := make([]byte, ed25519.PublicKeySize)
	copy(out, pub)
	return out, nil
}

// Ed25519SignSeed signs message using a 32-byte Ed25519 seed.
func Ed25519SignSeed(seed, msg []byte) ([]byte, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, ErrInvalid
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return ed25519.Sign(priv, msg), nil
}

// RequestIdentity returns the canonical 32-byte digest key for h.
func RequestIdentity(h Header) Hash {
	e := Encoder{}
	e.Str(h.Account)
	e.Raw(h.Domain[:])
	e.Str(h.Sender)
	e.U32(uint32(h.Purpose))
	e.Str(h.Machine)
	e.Str(h.Session)
	e.Str(h.Request)
	e.U64(h.Seq)
	return Digest("ra4/request-key\x00", e.B)
}

// AuthorizedPayloadHash returns the canonical payload hash committing to request
// identity, payload kind, deadline, and unpacked plaintext.
func AuthorizedPayloadHash(h Header, plain []byte) (Hash, error) {
	unpacked, err := Unpack(h, plain)
	if err != nil {
		return Hash{}, err
	}
	id := RequestIdentity(h)
	e := Encoder{}
	e.LP(id[:])
	e.U32(h.Kind)
	e.U64(h.Deadline)
	e.LP(unpacked)
	return Digest("ra4/authorized-payload\x00", e.B), nil
}
