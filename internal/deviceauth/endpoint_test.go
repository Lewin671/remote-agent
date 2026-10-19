package deviceauth

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestEndpointInstallAndRecoveryBoundary(t *testing.T) {
	g, pin, phoneEd, phoneX, recovery := testGenesis(t)
	secret1 := bytes.Repeat([]byte{0x33}, 32)
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	machine, machineEd, machineX := testMember(t, 0x44, Machine)
	secret2 := bytes.Repeat([]byte{0x55}, 32)
	added, err := BuildState(g, []Member{phone, machine}, nil, secret2, mustPrev(t, secret2, secret1), phoneEd, 1_700_000_100_000)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	ep, err := initTestEndpoint(filepath.Join(dir, "security.json"), pin, []State{g}, []*RecoveryStatement{nil}, phone.ID, phoneEd.Seed(), phoneX)
	if err != nil {
		t.Fatal(err)
	}
	if err := ep.Install([]State{added}, []*RecoveryStatement{nil}); err != nil {
		t.Fatal(err)
	}
	if got, err := ep.ContentSecret(1); err != nil || !bytes.Equal(got, secret1) {
		t.Fatalf("cached content key 1: %v", err)
	}
	if got, err := ep.ContentSecret(2); err != nil || !bytes.Equal(got, secret2) {
		t.Fatalf("content key 2: %v", err)
	}
	// Repeated sealing uses independent random nonces without touching storage.
	h := Header{Purpose: Command, Kind: 5, Machine: machine.ID, Request: "nonce-test-12345678", Deadline: 1_700_000_200_000}
	first, err := ep.Prepare(h, nil, []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ep.Prepare(h, nil, []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Nonce, second.Nonce) {
		t.Fatal("reused nonce")
	}

	// The machine is retained; the phone is replaced by a new authority phone.
	newPhone, newPhoneEd, newPhoneX := testMember(t, 0x77, Client)
	var newRecoveryPub [32]byte
	newRecoveryPub[0] = 0x88
	rec, stmt, err := BuildRecovery(added, newPhone, []Member{machine, newPhone}, bytes.Repeat([]byte{0x99}, 32), newPhoneEd, recovery, newRecoveryPub, 1_700_000_200_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := ep.Install([]State{rec}, []*RecoveryStatement{&stmt}); err != nil {
		t.Fatalf("retained machine install recovery: %v", err)
	}
	if got, err := ep.ContentSecret(1); err != nil || !bytes.Equal(got, secret1) {
		t.Fatalf("retained device lost cached pre-recovery key: %v", err)
	}

	// A new phone joins at the recovery state; it has no pre-recovery cache.
	np, err := initTestEndpoint(filepath.Join(dir, "new.json"), pin, []State{g, added, rec}, []*RecoveryStatement{nil, nil, &stmt}, newPhone.ID, newPhoneEd.Seed(), newPhoneX)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := np.ContentSecret(1); err != ErrUnavailable {
		t.Fatalf("new phone pre-recovery key = %v, want ErrUnavailable", err)
	}
	if _, err := np.ContentSecret(3); err != nil {
		t.Fatalf("new phone current key: %v", err)
	}
	_ = machineEd
	_ = machineX
}

func TestHistoryVsLiveMembership(t *testing.T) {
	g, _, phoneEd, _, _ := testGenesis(t)
	secret1 := bytes.Repeat([]byte{0x33}, 32)
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	machine, machineEd, _ := testMember(t, 0x44, Machine)
	secret2 := bytes.Repeat([]byte{0x55}, 32)
	added, err := BuildState(g, []Member{phone, machine}, nil, secret2, mustPrev(t, secret2, secret1), phoneEd, 1_700_000_100_000)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := BuildState(added, []Member{phone}, []string{machine.ID}, bytes.Repeat([]byte{0x66}, 32), mustPrev(t, bytes.Repeat([]byte{0x66}, 32), secret2), phoneEd, 1_700_000_200_000)
	if err != nil {
		t.Fatal(err)
	}
	h := Header{Account: g.Core.Account, Domain: g.Core.Domain, Epoch: added.Core.Epoch, State: mustHash(t, added), ContentEpoch: added.Core.ContentEpoch, Sender: machine.ID, Kind: 3, Purpose: SessionKey, Machine: machine.ID, Session: "0123456789abcdef", Seq: 1}
	w, err := Seal(secret2, nil, h, bytes.Repeat([]byte{0x01}, 32), machineEd)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Open(secret2, nil, added, added, false, 0); err != nil {
		t.Fatalf("stored history: %v", err)
	}
	if _, _, err := w.Open(secret2, nil, added, removed, true, (added.Core.IssuedAt + Lease)); err == nil {
		t.Fatal("removed machine's live envelope was accepted")
	}
}

func mustHash(t *testing.T, s State) Hash {
	t.Helper()
	h, err := s.Hash()
	if err != nil {
		t.Fatal(err)
	}
	return h
}
