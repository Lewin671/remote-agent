package deviceauth

import (
	"bytes"
	"crypto/ed25519"
	"path/filepath"
	"testing"
	"time"
)

// cloneState copies every slice so a test can tamper with one field without
// disturbing the original.
func cloneState(s State) State {
	out := s
	out.Core.Members = append([]Member(nil), s.Core.Members...)
	out.Core.Removed = append([]string(nil), s.Core.Removed...)
	out.Grants = make([]Grant, len(s.Grants))
	for i, g := range s.Grants {
		out.Grants[i] = Grant{Recipient: g.Recipient, Enc: g.Enc, Cipher: append([]byte(nil), g.Cipher...)}
	}
	out.PrevSecret = append([]byte(nil), s.PrevSecret...)
	out.Signature = append([]byte(nil), s.Signature...)
	return out
}

func TestRecoveryCannotKeepOrReAddOldAuthority(t *testing.T) {
	g, _, phoneEd, _, recovery := testGenesis(t)
	secret := bytes.Repeat([]byte{0x33}, 32)
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	machine, _, _ := testMember(t, 0x44, Machine)
	added, err := BuildState(g, []Member{phone, machine}, nil, bytes.Repeat([]byte{0x55}, 32), mustPrev(t, bytes.Repeat([]byte{0x55}, 32), secret), phoneEd, 1_700_000_100_000)
	if err != nil {
		t.Fatal(err)
	}
	newPhone, newPhoneEd, _ := testMember(t, 0x77, Client)
	var newRecovery [32]byte
	newRecovery[0] = 0x88

	// BuildRecovery itself refuses to keep the old authority in the list.
	if _, _, err := BuildRecovery(added, newPhone, []Member{phone, machine, newPhone}, bytes.Repeat([]byte{0x99}, 32), newPhoneEd, recovery, newRecovery, 1_700_000_200_000); err == nil {
		t.Fatal("recovery kept the old authority")
	}

	// A hand-built recovery state that re-adds the old authority must fail
	// transition verification even with a valid recovery statement.
	rec, stmt, err := BuildRecovery(added, newPhone, []Member{machine, newPhone}, bytes.Repeat([]byte{0x99}, 32), newPhoneEd, recovery, newRecovery, 1_700_000_200_000)
	if err != nil {
		t.Fatal(err)
	}
	core := rec.Core
	core.Members = append([]Member(nil), core.Members...)
	core.Members = append(core.Members, phone)
	core.Removed = nil
	for _, id := range rec.Core.Removed {
		if id != phone.ID {
			core.Removed = append(core.Removed, id)
		}
	}
	// Re-sort the members as a state requires.
	for i := 1; i < len(core.Members); i++ {
		for j := i; j > 0 && core.Members[j].ID < core.Members[j-1].ID; j-- {
			core.Members[j], core.Members[j-1] = core.Members[j-1], core.Members[j]
		}
	}
	readded, err := build(core, bytes.Repeat([]byte{0x99}, 32), nil, newPhoneEd)
	if err != nil {
		t.Fatal(err)
	}
	stmt.StateHash, _ = readded.Hash()
	if err := stmt.Sign(recovery); err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(added, readded, &stmt); err == nil {
		t.Fatal("a recovery state that re-added the old authority verified")
	}
}

func TestRecoverySignedByWrongKeyRejected(t *testing.T) {
	g, _, _, _, recovery := testGenesis(t)
	secret := bytes.Repeat([]byte{0x33}, 32)
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	newPhone, newPhoneEd, _ := testMember(t, 0x77, Client)
	var newRecovery [32]byte
	newRecovery[0] = 0x88
	rec, stmt, err := BuildRecovery(g, newPhone, []Member{newPhone}, bytes.Repeat([]byte{0x99}, 32), newPhoneEd, recovery, newRecovery, 1_700_000_200_000)
	if err != nil {
		t.Fatal(err)
	}
	_ = secret
	_ = phone
	wrong := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0xAB}, 32))
	if err := stmt.Sign(wrong); err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(g, rec, &stmt); err == nil {
		t.Fatal("recovery statement signed by the wrong key verified")
	}
}

func TestRenewalCannotChangeMembersOrPrevSecret(t *testing.T) {
	g, _, phoneEd, _, _ := testGenesis(t)
	secret := bytes.Repeat([]byte{0x33}, 32)
	base, err := BuildRenewal(g, secret, phoneEd, 1_700_000_100_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(g, base, nil); err != nil {
		t.Fatalf("base renewal: %v", err)
	}

	// Same content epoch, one more member: must be rejected.
	machine, _, _ := testMember(t, 0x44, Machine)
	cc := base.Core
	cc.Members = append(append([]Member(nil), cc.Members...), machine)
	for i := 1; i < len(cc.Members); i++ {
		for j := i; j > 0 && cc.Members[j].ID < cc.Members[j-1].ID; j-- {
			cc.Members[j], cc.Members[j-1] = cc.Members[j-1], cc.Members[j]
		}
	}
	changed, err := build(cc, secret, nil, phoneEd)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(g, changed, nil); err == nil {
		t.Fatal("a lease renewal changed the member list")
	}

	// Same content epoch with a PrevSecret: must be rejected.
	withPrev, err := build(base.Core, secret, mustPrev(t, secret, bytes.Repeat([]byte{0xEE}, 32)), phoneEd)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(g, withPrev, nil); err == nil {
		t.Fatal("a lease renewal carried a PrevSecret")
	}

	// A renewal may not move IssuedAt backwards.
	bc := base.Core
	bc.IssuedAt = g.Core.IssuedAt - 1
	bc.NotAfter = bc.IssuedAt + Lease
	back, err := build(bc, secret, nil, phoneEd)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(g, back, nil); err == nil {
		t.Fatal("a renewal moved IssuedAt backwards")
	}
}

func TestExpiredLeaseRejectedAfterClockRollback(t *testing.T) {
	g, pin, phoneEd, phoneX, _ := testGenesis(t)
	secret1 := bytes.Repeat([]byte{0x33}, 32)
	secret2 := bytes.Repeat([]byte{0x55}, 32)
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	machine, _, _ := testMember(t, 0x44, Machine)
	added, err := BuildState(g, []Member{phone, machine}, nil, secret2, mustPrev(t, secret2, secret1), phoneEd, 1_700_000_100_000)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "security.db")
	ep, err := InitEndpoint(path, pin, []State{g}, []*RecoveryStatement{nil}, phone.ID, phoneEd.Seed(), phoneX)
	if err != nil {
		t.Fatal(err)
	}
	if err := ep.Install([]State{added}, []*RecoveryStatement{nil}); err != nil {
		t.Fatal(err)
	}
	expired := added.Core.NotAfter + 1000
	if err := ep.Touch(expired); err != nil {
		t.Fatal(err)
	}
	if err := ep.Close(); err != nil {
		t.Fatal(err)
	}
	ep, err = OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()

	// A fresh control envelope, sealed with the current key. Its deadline is
	// valid against the persisted floor; only the lease is expired.
	h := Header{Account: g.Core.Account, Domain: g.Core.Domain, Epoch: added.Core.Epoch, State: mustHash(t, added), ContentEpoch: added.Core.ContentEpoch, Purpose: Command, Kind: 5, Sender: phone.ID, Machine: machine.ID, Request: "0123456789abcdef", Deadline: expired + 5*60*1000}
	w, err := Seal(secret2, nil, h, commandBody(t), phoneEd)
	if err != nil {
		t.Fatal(err)
	}
	// The wall clock is below the persisted floor, which must not save it.
	if _, err := ep.Accept(w, nil, added.Core.NotAfter-1000, func(Header, []byte) ([]byte, error) {
		t.Fatal("an expired control envelope was executed")
		return nil, nil
	}); err != ErrStale {
		t.Fatalf("expired control envelope = %v, want ErrStale", err)
	}
	if ep.data.Floor < expired {
		t.Fatalf("floor rolled back to %d", ep.data.Floor)
	}
}

func TestTamperingAnySignedStateFieldRejected(t *testing.T) {
	g, _, phoneEd, _, _ := testGenesis(t)
	secret1 := bytes.Repeat([]byte{0x33}, 32)
	secret2 := bytes.Repeat([]byte{0x55}, 32)
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	machine, _, _ := testMember(t, 0x44, Machine)
	added, err := BuildState(g, []Member{phone, machine}, nil, secret2, mustPrev(t, secret2, secret1), phoneEd, 1_700_000_100_000)
	if err != nil {
		t.Fatal(err)
	}
	other := "fedcba9876543210fedcba9876543210"
	mutations := map[string]func(s *State){
		"account":       func(s *State) { s.Core.Account = other },
		"domain":        func(s *State) { s.Core.Domain[0] ^= 1 },
		"authority":     func(s *State) { s.Core.AuthorityID = other },
		"recovery key":  func(s *State) { s.Core.RecoveryEd[0] ^= 1 },
		"parent":        func(s *State) { s.Core.Parent[0] ^= 1 },
		"epoch":         func(s *State) { s.Core.Epoch++ },
		"content epoch": func(s *State) { s.Core.ContentEpoch++ },
		"issued at":     func(s *State) { s.Core.IssuedAt-- },
		"not after":     func(s *State) { s.Core.NotAfter++ },
		"member key":    func(s *State) { s.Core.Members[0].XPublic[0] ^= 1 },
		"member kind":   func(s *State) { s.Core.Members[0].Kind = Machine },
		"removed":       func(s *State) { s.Core.Removed = []string{machine.ID} },
		"grant recipient": func(s *State) {
			s.Grants[0].Recipient = other
		},
		"grant encaps": func(s *State) { s.Grants[0].Enc[0] ^= 1 },
		"grant cipher": func(s *State) { s.Grants[0].Cipher[0] ^= 1 },
		"prev secret":  func(s *State) { s.PrevSecret[0] ^= 1 },
	}
	base, err := added.Hash()
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s := cloneState(added)
			mutate(&s)
			h, err := s.Hash()
			if err != nil {
				return // already malformed: rejected
			}
			if h == base {
				t.Fatal("field is not covered by the state hash")
			}
			if err := VerifyTransition(g, s, nil); err == nil {
				t.Fatal("a state with a tampered signed field verified")
			}
		})
	}
}

func TestRecoveryStatementRoundTrip(t *testing.T) {
	g, _, phoneEd, _, recovery := testGenesis(t)
	secret := bytes.Repeat([]byte{0x33}, 32)
	newPhone, newPhoneEd, _ := testMember(t, 0x77, Client)
	var newRecovery [32]byte
	newRecovery[0] = 0x88
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	_ = phoneEd
	_ = phone
	_, stmt, err := BuildRecovery(g, newPhone, []Member{newPhone}, secret, newPhoneEd, recovery, newRecovery, 1_700_000_200_000)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := stmt.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseRecoveryStatement(raw); err != nil {
		t.Fatalf("parse: %v", err)
	}
}

func TestAuthorityRotatesRecoveryKeyWithoutRecovery(t *testing.T) {
	g, _, phoneEd, phoneX, oldRecovery := testGenesis(t)
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	_, rotatedRecovery, _ := ed25519.GenerateKey(nil)
	var rotatedPub [32]byte
	copy(rotatedPub[:], rotatedRecovery.Public().(ed25519.PublicKey))
	rotated, err := BuildRenewalRotating(g, bytes.Repeat([]byte{0x33}, 32), rotatedPub, phoneEd, 1_700_000_100_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(g, rotated, nil); err != nil {
		t.Fatalf("rotation: %v", err)
	}
	newPhone, newPhoneEd, _ := testMember(t, 0x66, Client)
	// The old code cannot sign the next recovery after rotation.
	rec, stmt, err := BuildRecovery(rotated, newPhone, []Member{newPhone}, bytes.Repeat([]byte{0x44}, 32), newPhoneEd, oldRecovery, [32]byte{1}, 1_700_000_200_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(rotated, rec, &stmt); err == nil {
		t.Fatal("old recovery key still authorized a recovery after rotation")
	}
	// The rotated key can, and installs a fresh recovery key of its own.
	var nextRecoveryPub [32]byte
	nextRecoveryPub[0] = 0x22
	rec, stmt, err = BuildRecovery(rotated, newPhone, []Member{newPhone}, bytes.Repeat([]byte{0x44}, 32), newPhoneEd, rotatedRecovery, nextRecoveryPub, 1_700_000_200_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(rotated, rec, &stmt); err != nil {
		t.Fatalf("rotated recovery key: %v", err)
	}
	_ = phone
	_ = phoneX
}

func TestRemovedSetAccumulatesAndCannotBeReadded(t *testing.T) {
	g, _, phoneEd, _, _ := testGenesis(t)
	secret := bytes.Repeat([]byte{0x33}, 32)
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	a, _, _ := testMember(t, 0x44, Machine)
	b, _, _ := testMember(t, 0x55, Client)
	secret2 := bytes.Repeat([]byte{0x44}, 32)
	added, err := BuildState(g, []Member{phone, a, b}, nil, secret2, mustPrev(t, secret2, secret), phoneEd, 1_700_000_100_000)
	if err != nil {
		t.Fatal(err)
	}
	secret3 := bytes.Repeat([]byte{0x55}, 32)
	removedA, err := BuildState(added, []Member{phone, b}, []string{a.ID}, secret3, mustPrev(t, secret3, secret2), phoneEd, 1_700_000_200_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(added, removedA, nil); err != nil {
		t.Fatal(err)
	}
	secret4 := bytes.Repeat([]byte{0x66}, 32)
	removedBoth, err := BuildState(removedA, []Member{phone}, []string{a.ID, b.ID}, secret4, mustPrev(t, secret4, secret3), phoneEd, 1_700_000_300_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(removedA, removedBoth, nil); err != nil {
		t.Fatalf("accumulated removals: %v", err)
	}
	// Dropping an earlier tombstone is rejected.
	secret5 := bytes.Repeat([]byte{0x77}, 32)
	dropped, err := BuildState(removedA, []Member{phone}, []string{b.ID}, secret5, mustPrev(t, secret5, secret3), phoneEd, 1_700_000_300_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(removedA, dropped, nil); err == nil {
		t.Fatal("dropped tombstone accepted")
	}
	// A removed ID cannot return.
	secret6 := bytes.Repeat([]byte{0x88}, 32)
	readd, err := BuildState(removedBoth, []Member{phone, a}, []string{a.ID, b.ID}, secret6, mustPrev(t, secret6, secret4), phoneEd, 1_700_000_400_000)
	if err == nil && VerifyTransition(removedBoth, readd, nil) == nil {
		t.Fatal("removed device re-added")
	}
}

func TestStatesFromTheFutureAreRejected(t *testing.T) {
	now := uint64(time.Now().UnixMilli())
	phone, phoneEd, phoneX := testMember(t, 0x11, Client)
	var rec [32]byte
	rec[0] = 1
	secret := bytes.Repeat([]byte{0x22}, 32)
	genesis, err := BuildGenesis("0123456789abcdef0123456789abcdef", digest("test/domain", []byte("future")), phone, rec, secret, phoneEd, now+10*60*1000)
	if err != nil {
		t.Fatal(err)
	}
	pin, _ := genesis.Pin()
	if _, err := InitEndpoint("", pin, []State{genesis}, []*RecoveryStatement{nil}, phone.ID, phoneEd.Seed(), phoneX); err == nil {
		t.Fatal("future genesis accepted")
	}
}
