//go:build !js

package corebind

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func decodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode hex %q: %v", s, err)
	}
	return b
}

func TestCryptoVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "e2e", "testdata", "vectors.json"))
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var vec struct {
		AccountPublicKey  string `json:"account_public_key"`
		RegisterSignature struct {
			Message   string `json:"message"`
			Signature string `json:"signature"`
		} `json:"register_signature"`
		Pairing struct {
			InitiatorPrivate string `json:"initiator_private"`
			JoinerPrivate    string `json:"joiner_private"`
			InitiatorPublic  string `json:"initiator_public"`
			JoinerPublic     string `json:"joiner_public"`
		} `json:"pairing"`
	}
	if err := json.Unmarshal(raw, &vec); err != nil {
		t.Fatalf("unmarshal vectors: %v", err)
	}

	pub := decodeHex(t, vec.AccountPublicKey)
	msg := append([]byte("remote-agent/v1/register\x00"), decodeHex(t, vec.RegisterSignature.Message)...)
	sig := decodeHex(t, vec.RegisterSignature.Signature)
	if !Ed25519Verify(pub, msg, sig) {
		t.Fatal("Ed25519Verify rejected frozen vector signature")
	}
	badSig := bytes.Clone(sig)
	badSig[0] ^= 0xff
	if Ed25519Verify(pub, msg, badSig) {
		t.Fatal("Ed25519Verify accepted tampered signature")
	}
	if Ed25519Verify(pub[:31], msg, sig) || Ed25519Verify(pub, msg, sig[:63]) {
		t.Fatal("Ed25519Verify accepted short key or signature")
	}

	seed := bytes.Repeat([]byte{0x44}, 32)
	derivedPub, err := Ed25519PublicKey(seed)
	if err != nil {
		t.Fatalf("Ed25519PublicKey: %v", err)
	}
	testMsg := []byte("roundtrip")
	gotSig, err := Ed25519Sign(seed, testMsg)
	if err != nil {
		t.Fatalf("Ed25519Sign: %v", err)
	}
	if !Ed25519Verify(derivedPub, testMsg, gotSig) {
		t.Fatal("Ed25519Verify rejected roundtrip signature")
	}
	if _, err := Ed25519PublicKey(seed[:31]); err == nil {
		t.Fatal("Ed25519PublicKey accepted short seed")
	}
	if _, err := Ed25519Sign(seed[:31], testMsg); err == nil {
		t.Fatal("Ed25519Sign accepted short seed")
	}

	initPriv := decodeHex(t, vec.Pairing.InitiatorPrivate)
	joinPriv := decodeHex(t, vec.Pairing.JoinerPrivate)
	initPub, err := X25519PublicKey(initPriv)
	if err != nil || !bytes.Equal(initPub, decodeHex(t, vec.Pairing.InitiatorPublic)) {
		t.Fatalf("X25519PublicKey initiator mismatch: err=%v got=%x", err, initPub)
	}
	joinPub, err := X25519PublicKey(joinPriv)
	if err != nil || !bytes.Equal(joinPub, decodeHex(t, vec.Pairing.JoinerPublic)) {
		t.Fatalf("X25519PublicKey joiner mismatch: err=%v got=%x", err, joinPub)
	}
	s1, err := X25519Shared(initPriv, joinPub)
	if err != nil {
		t.Fatalf("X25519Shared initiator: %v", err)
	}
	s2, err := X25519Shared(joinPriv, initPub)
	if err != nil || !bytes.Equal(s1, s2) {
		t.Fatalf("X25519Shared mismatch: err=%v", err)
	}
	if _, err := X25519Shared(initPriv, make([]byte, 32)); err == nil {
		t.Fatal("X25519Shared accepted all-zero peer key")
	}

	k1, err := HkdfSha256([]byte("secret"), []byte("salt"), []byte("info"), 32)
	if err != nil || len(k1) != 32 {
		t.Fatalf("HkdfSha256 failed: len=%d err=%v", len(k1), err)
	}
	k2, err := HkdfSha256([]byte("secret"), []byte("salt"), []byte("info"), 32)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatalf("HkdfSha256 not deterministic: %v", err)
	}
	if _, err := HkdfSha256([]byte("secret"), nil, nil, 0); err == nil {
		t.Fatal("HkdfSha256 accepted size=0")
	}
}
