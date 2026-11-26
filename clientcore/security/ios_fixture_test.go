package security

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/Lewin671/remote-agent/shared/devicesec"
)

var updateIOSHomeFixture = flag.Bool("update-ios-home-fixture", false, "regenerate ios/testdata/home-project-v4.json")

type iosHomeProjectFixture struct {
	Server        string   `json:"server"`
	NowMs         int64    `json:"nowMs"`
	AccountID     string   `json:"accountId"`
	ClientID      string   `json:"clientId"`
	MachineID     string   `json:"machineId"`
	ClientEdSeed  []byte   `json:"clientEdSeed"`
	ClientEdPub   []byte   `json:"clientEdPub"`
	ClientXPriv   []byte   `json:"clientXPriv"`
	ClientXPub    []byte   `json:"clientXPub"`
	MachineEdSeed []byte   `json:"machineEdSeed"`
	MachineEdPub  []byte   `json:"machineEdPub"`
	MachineXPriv  []byte   `json:"machineXPriv"`
	MachineXPub   []byte   `json:"machineXPub"`
	States        [][]byte `json:"states"`
	Lease         []byte   `json:"lease"`
	ClientRecord  []byte   `json:"clientRecord"`
	MachineRecord []byte   `json:"machineRecord"`
}

func generateIOSHomeProjectFixture(t *testing.T) iosHomeProjectFixture {
	t.Helper()
	const (
		nowMs  = uint64(1_791_000_000_000)
		server = "https://relay.example.com"
	)
	client := newTestIdentity(t, 0x41)
	machine := newTestIdentity(t, 0x42)
	recovery := newTestIdentity(t, 0x43)
	dom := devicesec.Digest("ios-home-project-fixture-domain", []byte{1})
	sec1 := bytes.Repeat([]byte{0x21}, 32)
	sec2 := bytes.Repeat([]byte{0x22}, 32)

	u0, err := devicesec.NewGenesis(client.id, dom, client.member(devicesec.Client), [32]byte(recovery.edP), sec1, nowMs, stdHPKESender{})
	if err != nil {
		t.Fatalf("NewGenesis: %v", err)
	}
	sb0, err := u0.SigningBytes()
	if err != nil {
		t.Fatalf("genesis SigningBytes: %v", err)
	}
	u0.Signature = ed25519.Sign(client.ed, sb0)
	s0Raw, err := u0.MarshalBinary()
	if err != nil {
		t.Fatalf("genesis MarshalBinary: %v", err)
	}
	s0, err := devicesec.ParseState(s0Raw)
	if err != nil {
		t.Fatalf("ParseState genesis: %v", err)
	}
	s0Hash, err := s0.Hash()
	if err != nil {
		t.Fatalf("genesis Hash: %v", err)
	}
	pin := devicesec.Pin{Account: client.id, Domain: dom, Genesis: s0Hash}

	u1, err := devicesec.NewPairState(
		s0,
		machine.member(devicesec.Machine),
		machine.xPub[:],
		sec1,
		sec2,
		nowMs+10,
		stdHPKESender{},
		rand.Reader,
	)
	if err != nil {
		t.Fatalf("NewPairState machine enrollment: %v", err)
	}
	sb1, err := u1.SigningBytes()
	if err != nil {
		t.Fatalf("state1 SigningBytes: %v", err)
	}
	u1.Signature = ed25519.Sign(client.ed, sb1)
	s1Raw, err := u1.MarshalBinary()
	if err != nil {
		t.Fatalf("state1 MarshalBinary: %v", err)
	}
	s1, err := devicesec.ParseState(s1Raw)
	if err != nil {
		t.Fatalf("ParseState state1: %v", err)
	}

	leaseU, err := devicesec.NewLease(s1, nowMs+10)
	if err != nil {
		t.Fatalf("NewLease: %v", err)
	}
	lsb, err := leaseU.SigningBytes()
	if err != nil {
		t.Fatalf("lease SigningBytes: %v", err)
	}
	leaseU.Signature = ed25519.Sign(client.ed, lsb)
	leaseRaw, err := leaseU.MarshalBinary()
	if err != nil {
		t.Fatalf("lease MarshalBinary: %v", err)
	}

	var k1, k2 [32]byte
	copy(k1[:], sec1)
	copy(k2[:], sec2)
	contentKeys := []ContentKeyEntry{
		{Epoch: s0.Core.ContentEpoch, Secret: k1},
		{Epoch: s1.Core.ContentEpoch, Secret: k2},
	}
	buildRecord := func(memberID string) []byte {
		rec := &PersistedRecord{
			Pin:              pin,
			MemberID:         memberID,
			Member:           true,
			Server:           server,
			AccountPublicKey: client.edP,
			States:           [][]byte{s0Raw, s1Raw},
			RecoveryProofs:   [][]byte{nil, nil},
			Lease:            leaseRaw,
			FloorMs:          int64(nowMs + 10),
			ContentKeys:      contentKeys,
		}
		raw, err := rec.Encode()
		if err != nil {
			t.Fatalf("Encode(%s): %v", memberID, err)
		}
		return raw
	}

	return iosHomeProjectFixture{
		Server:        server,
		NowMs:         int64(nowMs + 100),
		AccountID:     client.id,
		ClientID:      client.id,
		MachineID:     machine.id,
		ClientEdSeed:  client.ed.Seed(),
		ClientEdPub:   client.edP,
		ClientXPriv:   client.x.Bytes(),
		ClientXPub:    client.xPub[:],
		MachineEdSeed: machine.ed.Seed(),
		MachineEdPub:  machine.edP,
		MachineXPriv:  machine.x.Bytes(),
		MachineXPub:   machine.xPub[:],
		States:        [][]byte{s0Raw, s1Raw},
		Lease:         leaseRaw,
		ClientRecord:  buildRecord(client.id),
		MachineRecord: buildRecord(machine.id),
	}
}

// TestIOSHomeProjectFixture validates the committed Go-generated v4 security fixture consumed by
// ios/RemoteAgentTests/HomeProjectTests.swift (run with -update-ios-home-fixture to regenerate).
func TestIOSHomeProjectFixture(t *testing.T) {
	path := filepath.Join("..", "..", "ios", "testdata", "home-project-v4.json")
	if *updateIOSHomeFixture {
		fix := generateIOSHomeProjectFixture(t)
		raw, err := json.MarshalIndent(fix, "", "  ")
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var fix iosHomeProjectFixture
	if err := json.Unmarshal(raw, &fix); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	if len(fix.States) != 2 || len(fix.Lease) == 0 {
		t.Fatalf("expected 2 states and non-empty lease")
	}

	cRec, err := DecodePersistedRecord(fix.ClientRecord)
	if err != nil {
		t.Fatalf("DecodePersistedRecord(client): %v", err)
	}
	if reEncoded, err := cRec.Encode(); err != nil || !bytes.Equal(reEncoded, fix.ClientRecord) {
		t.Fatalf("ClientRecord round-trip mismatch: err=%v", err)
	}
	mRec, err := DecodePersistedRecord(fix.MachineRecord)
	if err != nil {
		t.Fatalf("DecodePersistedRecord(machine): %v", err)
	}
	if reEncoded, err := mRec.Encode(); err != nil || !bytes.Equal(reEncoded, fix.MachineRecord) {
		t.Fatalf("MachineRecord round-trip mismatch: err=%v", err)
	}
	if cRec.MemberID != fix.ClientID || mRec.MemberID != fix.MachineID {
		t.Fatalf("record member IDs mismatch: client=%q machine=%q", cRec.MemberID, mRec.MemberID)
	}

	// Verify Ed25519 seeds and X25519 private keys derive the exact public keys in the fixture.
	checkKeys := func(label string, edSeed, edPub, xPriv, xPub []byte) {
		t.Helper()
		if len(edSeed) != ed25519.SeedSize {
			t.Fatalf("%s Ed25519 seed len = %d, want %d", label, len(edSeed), ed25519.SeedSize)
		}
		derivedEd := ed25519.NewKeyFromSeed(edSeed).Public().(ed25519.PublicKey)
		if !bytes.Equal(derivedEd, edPub) {
			t.Fatalf("%s derived Ed25519 pubkey does not match fixture", label)
		}
		privX, err := ecdh.X25519().NewPrivateKey(xPriv)
		if err != nil {
			t.Fatalf("%s X25519 private key invalid: %v", label, err)
		}
		if !bytes.Equal(privX.PublicKey().Bytes(), xPub) {
			t.Fatalf("%s derived X25519 pubkey does not match fixture", label)
		}
	}
	checkKeys("client", fix.ClientEdSeed, fix.ClientEdPub, fix.ClientXPriv, fix.ClientXPub)
	checkKeys("machine", fix.MachineEdSeed, fix.MachineEdPub, fix.MachineXPriv, fix.MachineXPub)

	// Verify both persisted records carry the exact fix.States and fix.Lease bytes.
	checkRecordChain := func(label string, rec *PersistedRecord) {
		t.Helper()
		if len(rec.States) != len(fix.States) {
			t.Fatalf("%s record states len = %d, want %d", label, len(rec.States), len(fix.States))
		}
		for i := range fix.States {
			if !bytes.Equal(rec.States[i], fix.States[i]) {
				t.Fatalf("%s record state[%d] does not match fix.States[%d]", label, i, i)
			}
		}
		if !bytes.Equal(rec.Lease, fix.Lease) {
			t.Fatalf("%s record lease does not match fix.Lease", label)
		}
	}
	checkRecordChain("client", cRec)
	checkRecordChain("machine", mRec)

	s0, err := devicesec.ParseState(fix.States[0])
	if err != nil {
		t.Fatalf("ParseState(0): %v", err)
	}
	if s0Round, err := s0.MarshalBinary(); err != nil || !bytes.Equal(s0Round, fix.States[0]) {
		t.Fatalf("state[0] round-trip mismatch: err=%v", err)
	}
	s1, err := devicesec.ParseState(fix.States[1])
	if err != nil {
		t.Fatalf("ParseState(1): %v", err)
	}
	if s1Round, err := s1.MarshalBinary(); err != nil || !bytes.Equal(s1Round, fix.States[1]) {
		t.Fatalf("state[1] round-trip mismatch: err=%v", err)
	}
	if err := cRec.Pin.VerifyGenesis(s0); err != nil {
		t.Fatalf("VerifyGenesis: %v", err)
	}
	if err := devicesec.VerifyTransition(s0, s1, nil); err != nil {
		t.Fatalf("VerifyTransition: %v", err)
	}
	lease, err := devicesec.ParseLease(fix.Lease)
	if err != nil {
		t.Fatalf("ParseLease: %v", err)
	}
	if leaseRound, err := lease.MarshalBinary(); err != nil || !bytes.Equal(leaseRound, fix.Lease) {
		t.Fatalf("lease round-trip mismatch: err=%v", err)
	}
	if err := lease.Valid(s1, uint64(fix.NowMs)); err != nil {
		t.Fatalf("lease.Valid: %v", err)
	}
	cm, ok := s1.Core.Member(fix.ClientID)
	if !ok || cm.Kind != devicesec.Client || !bytes.Equal(cm.EdPublic[:], fix.ClientEdPub) || !bytes.Equal(cm.XPublic[:], fix.ClientXPub) {
		t.Fatalf("client member = (%+v, %v), want Client with matching keys", cm, ok)
	}
	mm, ok := s1.Core.Member(fix.MachineID)
	if !ok || mm.Kind != devicesec.Machine || !bytes.Equal(mm.EdPublic[:], fix.MachineEdPub) || !bytes.Equal(mm.XPublic[:], fix.MachineXPub) {
		t.Fatalf("machine member = (%+v, %v), want Machine with matching keys", mm, ok)
	}
}
