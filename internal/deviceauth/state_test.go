package deviceauth

import (
	"bytes"
	"crypto/ed25519"
	"testing"
)

func testMember(t *testing.T, seed byte, kind Kind) (Member, ed25519.PrivateKey, []byte) {
	t.Helper()
	ed := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32))
	x, xpub, err := NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMember(ed.Public().(ed25519.PublicKey), xpub, kind)
	if err != nil {
		t.Fatal(err)
	}
	return m, ed, x
}

func testGenesis(t *testing.T) (State, Pin, ed25519.PrivateKey, []byte, ed25519.PrivateKey) {
	t.Helper()
	phone, phoneEd, phoneX := testMember(t, 0x11, Client)
	recovery := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x22}, 32))
	var recoveryPub [32]byte
	copy(recoveryPub[:], recovery.Public().(ed25519.PublicKey))
	secret := bytes.Repeat([]byte{0x33}, 32)
	domain := digest("test/domain", []byte("one"))
	g, err := BuildGenesis("0123456789abcdef0123456789abcdef", domain, phone, recoveryPub, secret, phoneEd, 1_700_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := g.Pin()
	if err != nil || pin.VerifyGenesis(g) != nil {
		t.Fatalf("genesis: %v", err)
	}
	return g, pin, phoneEd, phoneX, recovery
}

func TestGenesisRenewalAddAndRemove(t *testing.T) {
	g, pin, phoneEd, phoneX, recovery := testGenesis(t)
	secret := bytes.Repeat([]byte{0x33}, 32)
	phone, _ := g.Core.Member(g.Core.AuthorityID)

	renewed, err := BuildRenewal(g, secret, phoneEd, 1_700_000_100_000)
	if err != nil || VerifyTransition(g, renewed, nil) != nil {
		t.Fatalf("renewal: %v", err)
	}

	machine, machineEd, machineX := testMember(t, 0x44, Machine)
	added, err := BuildState(renewed, []Member{phone, machine}, nil, bytes.Repeat([]byte{0x55}, 32), mustPrev(t, bytes.Repeat([]byte{0x55}, 32), secret), phoneEd, 1_700_000_200_000)
	if err != nil || VerifyTransition(renewed, added, nil) != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := pin.OpenGrant(added, machine.ID, machineX); err != nil {
		t.Fatalf("machine grant: %v", err)
	}
	if _, err := pin.OpenGrant(added, phone.ID, phoneX); err != nil {
		t.Fatalf("phone grant: %v", err)
	}
	_ = machineEd

	removed, err := BuildState(added, []Member{phone}, []string{machine.ID}, bytes.Repeat([]byte{0x66}, 32), mustPrev(t, bytes.Repeat([]byte{0x66}, 32), bytes.Repeat([]byte{0x55}, 32)), phoneEd, 1_700_000_300_000)
	if err != nil || VerifyTransition(added, removed, nil) != nil {
		t.Fatalf("remove: %v", err)
	}

	// Recovery must remove the old authority.
	newPhone, newPhoneEd, _ := testMember(t, 0x77, Client)
	var newRecoveryPub [32]byte
	newRecoveryPub[0] = 0x88

	rec, st, err := BuildRecovery(removed, newPhone, []Member{newPhone}, bytes.Repeat([]byte{0x99}, 32), newPhoneEd, recovery, newRecoveryPub, 1_700_000_400_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(removed, rec, &st); err != nil {
		t.Fatalf("recovery transition: %v", err)
	}
	if len(rec.PrevSecret) != 0 {
		t.Fatal("recovery must not carry PrevSecret")
	}
}

func mustPrev(t *testing.T, newSecret, oldSecret []byte) []byte {
	t.Helper()
	p, err := sealPrevSecret(newSecret, oldSecret)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPrevSecretIsSigned(t *testing.T) {
	g, _, phoneEd, _, _ := testGenesis(t)
	secret := bytes.Repeat([]byte{0x33}, 32)
	nextSecret := bytes.Repeat([]byte{0xaa}, 32)
	machine, _, _ := testMember(t, 0x44, Machine)
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	prev := mustPrev(t, nextSecret, secret)
	s, err := BuildState(g, []Member{machine, phone}, nil, nextSecret, prev, phoneEd, 1_700_000_100_000)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Hash()
	s.PrevSecret[20] ^= 1
	after, _ := s.Hash()
	if before == after {
		t.Fatal("PrevSecret is not covered by the state hash")
	}
	if VerifyTransition(g, s, nil) == nil {
		t.Fatal("a state with a modified PrevSecret verified")
	}
}

func TestBuildPairState(t *testing.T) {
	g, pin, phoneEd, phoneX, _ := testGenesis(t)
	secret := bytes.Repeat([]byte{0x33}, 32)
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	if _, err := pin.OpenGrant(g, phone.ID, phoneX); err != nil {
		t.Fatal(err)
	}
	candidate, _, _ := testMember(t, 0x44, Machine)
	next, err := BuildPairState(g, candidate, candidate.XPublic[:], secret, bytes.Repeat([]byte{0x55}, 32), phoneEd, 1_700_000_100_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(g, next, nil); err != nil {
		t.Fatal(err)
	}
	_ = phone
}
