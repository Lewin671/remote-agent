package deviceauth

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"testing"
)

func rotateForTest(parent State, secret []byte, signer ed25519.PrivateKey, now uint64) (State, error) {
	h, _ := parent.Hash()
	seed := digest("test/recovery-rotation", h[:])
	pub := ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey)
	var next [32]byte
	copy(next[:], pub)
	return BuildRecoveryKeyRotation(parent, secret, next, signer, now)
}

func initTestEndpoint(path string, pin Pin, states []State, proofs []*RecoveryStatement, id string, edSeed, x []byte) (*Endpoint, error) {
	head := states[len(states)-1]
	for n := 0; n < 256; n++ {
		signer := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(n)}, 32))
		if ID(signer.Public().(ed25519.PublicKey)) == head.Core.AuthorityID {
			l, err := BuildLease(head, signer, head.Core.IssuedAt)
			if err != nil {
				return nil, err
			}
			return InitEndpoint(path, pin, states, proofs, id, edSeed, x, l)
		}
	}
	return nil, ErrInvalid
}

func TestLeaseIndependentDurableAndBounded(t *testing.T) {
	g, pin, ed, x, _ := testGenesis(t)
	original, _ := g.MarshalBinary()
	ep, err := InitEndpoint(filepath.Join(t.TempDir(), "security.db"), pin, []State{g}, []*RecoveryStatement{nil}, g.Core.AuthorityID, ed.Seed(), x)
	if err != nil {
		t.Fatal(err)
	}
	now := g.Core.IssuedAt
	if err := ep.CheckLease(now); err == nil {
		t.Fatal("missing lease accepted")
	}
	for i := uint64(0); i < 300; i++ {
		l, err := BuildLease(g, ed, now+i*Lease/2)
		if err != nil {
			t.Fatal(err)
		}
		if err := ep.InstallLease(l, l.IssuedAt); err != nil {
			t.Fatal(err)
		}
	}
	if ep.HeadEpoch() != 1 {
		t.Fatal("renewal grew membership chain")
	}
	raw, _ := g.MarshalBinary()
	if !bytes.Equal(raw, original) {
		t.Fatal("renewal rewrote state/grants")
	}
	last := ep.Lease()
	path := ep.path
	ep.Close()
	ep, err = OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	if !bytes.Equal(ep.Lease().Signature, last.Signature) {
		t.Fatal("lease lost after restart")
	}
	if err := ep.CheckLease(last.NotAfter); !errors.Is(err, ErrStale) {
		t.Fatalf("expired = %v", err)
	}
	if err := ep.Touch(last.NotAfter + MaxClockSkew + 1); err != nil {
		t.Fatal(err)
	}
	if err := ep.CheckLease(last.NotAfter - 1); err == nil {
		t.Fatal("clock rollback revived lease")
	}
	old, _ := BuildLease(g, ed, last.IssuedAt-1)
	if err := ep.InstallLease(old, last.NotAfter); err == nil {
		t.Fatal("lease rollback accepted")
	}
	bad := *last
	bad.NotAfter = bad.IssuedAt + Lease + 1
	b, _ := bad.signBytes()
	bad.Signature = ed25519.Sign(ed, b)
	if err := ep.InstallLease(bad, last.NotAfter); err == nil {
		t.Fatal("overlong lease accepted")
	}
	other := g
	other.Core.Domain[0] ^= 1
	if last.Verify(other) == nil {
		t.Fatal("wrong domain lease accepted")
	}
}

func TestHandoffKeepsHistoryAndRemovesRetiringAuthority(t *testing.T) {
	c := newCurrentChain(t)
	newSecret := bytes.Repeat([]byte{0x9a}, 32)
	newRec := [32]byte{0x9b}
	s, err := BuildHandoff(c.withClient, c.client.ID, newRec, bytes.Repeat([]byte{0x77}, 32), newSecret, c.phoneEd, c.withClient.Core.IssuedAt+1)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(c.withClient, s, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Core.Member(c.withClient.Core.AuthorityID); ok {
		t.Fatal("retiring phone retained")
	}
	key, err := c.pin.OpenGrant(s, c.client.ID, c.clientX)
	if err != nil {
		t.Fatal(err)
	}
	old, err := openPrevSecret(key, s.PrevSecret)
	if err != nil || !bytes.Equal(old, bytes.Repeat([]byte{0x77}, 32)) {
		t.Fatal("handoff lost history")
	}
	l, err := BuildLease(s, c.clientEd, s.Core.IssuedAt)
	if err != nil || l.Verify(s) != nil {
		t.Fatal("new authority cannot lease")
	}
	if _, err := BuildLease(s, c.phoneEd, s.Core.IssuedAt); err == nil {
		t.Fatal("old authority can lease")
	}
}
