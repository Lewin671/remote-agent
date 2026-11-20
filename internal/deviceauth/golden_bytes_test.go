package deviceauth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"testing/cryptotest"
)

// TestSignedBytesGolden pins the exact bytes the daemon's signing and sealing
// paths produce from fixed keys and a fixed randomness source (HPKE
// encapsulation, PrevSecret, envelope and recovery nonces all draw from it).
// The digests were recorded from the implementation before the pure half moved
// to shared/devicesec; any change to labels, AAD, nonce draws or signed bytes
// fails here.
func TestSignedBytesGolden(t *testing.T) {
	cryptotest.SetGlobalRandom(t, 20261120)
	key := func(b byte) ed25519.PrivateKey { return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{b}, 32)) }
	member := func(ed, x byte, kind Kind) Member {
		pub, err := XPublic(bytes.Repeat([]byte{x}, 32))
		if err != nil {
			t.Fatal(err)
		}
		m, err := NewMember(key(ed).Public().(ed25519.PublicKey), pub, kind)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	recovery := func(b byte) [32]byte { return [32]byte(key(b).Public().(ed25519.PublicKey)) }
	secret := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	const t0 = 1_800_000_000_000
	phone, machine, tablet, laptop := member(0x11, 0x21, Client), member(0x12, 0x22, Machine), member(0x13, 0x23, Client), member(0x14, 0x24, Client)
	got := map[string][]byte{}
	state := func(name string) func(State, error) State {
		return func(s State, err error) State {
			t.Helper()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			b, err := s.MarshalBinary()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			got[name] = b
			return s
		}
	}

	genesis := state("genesis")(BuildGenesis("0123456789abcdef0123456789abcdef", Hash(secret(0x41)), phone, recovery(0x31), secret(0x51), key(0x11), t0))
	paired := state("paired")(BuildPairState(genesis, machine, machine.XPublic[:], secret(0x51), secret(0x52), key(0x11), t0+1_000))
	added := state("added")(BuildMembershipState(paired, []Member{phone, machine, tablet}, nil, secret(0x52), secret(0x53), key(0x11), t0+2_000))
	rotated := state("rotated")(BuildRecoveryKeyRotation(added, secret(0x53), recovery(0x32), key(0x11), t0+3_000))
	lease, err := BuildLease(rotated, key(0x11), t0+4_000)
	if err != nil {
		t.Fatal(err)
	}
	if got["lease"], err = lease.MarshalBinary(); err != nil {
		t.Fatal(err)
	}
	head, _ := rotated.Hash()
	command := Header{Account: rotated.Core.Account, Domain: rotated.Core.Domain, Epoch: rotated.Core.Epoch, State: head, ContentEpoch: rotated.Core.ContentEpoch, Sender: phone.ID, Kind: 5, Purpose: Command, Machine: machine.ID, Request: "0123456789abcdef", Deadline: t0 + 60_000}
	event := Header{Account: rotated.Core.Account, Domain: rotated.Core.Domain, Epoch: rotated.Core.Epoch, State: head, ContentEpoch: rotated.Core.ContentEpoch, Sender: machine.ID, Kind: 1, Purpose: Event, Machine: machine.ID, Session: "session-1", Seq: 7}
	for _, c := range []struct {
		name   string
		h      Header
		seed   []byte
		signer ed25519.PrivateKey
	}{{"command", command, nil, key(0x11)}, {"event", event, secret(0x61), key(0x12)}} {
		w, err := Seal(secret(0x53), c.seed, c.h, []byte("golden "+c.name), c.signer)
		if err != nil {
			t.Fatal(err)
		}
		if got[c.name], err = w.MarshalBinary(); err != nil {
			t.Fatal(err)
		}
	}
	handoff := state("handoff")(BuildHandoff(rotated, tablet.ID, recovery(0x33), secret(0x53), secret(0x54), key(0x11), t0+5_000))
	recovered, statement, err := BuildRecovery(handoff, laptop, []Member{machine, laptop}, secret(0x55), key(0x14), key(0x33), recovery(0x34), t0+6_000)
	state("recovered")(recovered, err)
	if got["statement"], err = statement.MarshalBinary(); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		parent, next State
		proof        *RecoveryStatement
	}{{genesis, paired, nil}, {paired, added, nil}, {added, rotated, nil}, {rotated, handoff, nil}, {handoff, recovered, &statement}} {
		if err := VerifyTransition(step.parent, step.next, step.proof); err != nil {
			t.Fatalf("epoch %d: %v", step.next.Core.Epoch, err)
		}
	}

	want := map[string]string{
		"genesis":   "e3f6582a36ca71079b50c6ae904bf406d39405b4f58f0bcc3d4a2cbbbb42cc60",
		"paired":    "3614e613b98c2e01ee5a653b3b44667334d71afc08eac5a4fc4afa715b3f7b8f",
		"added":     "6dea56f03024d9166ccf0edb3af64d61e0ea58c2273cfd8b8a8603ceb9284284",
		"rotated":   "0c751c2d9c59634d256f0ef8656b2f896c21569a26ef69db755b7c4e00aa0586",
		"lease":     "d9b0e5655c157e55601349476068727d193a170b4d805aade56e40e7353fca42",
		"command":   "3b5d864e9fa929a8a8f733dc44b54cf6314d881af6239616607952df2827e5ea",
		"event":     "c6f15820b49703a7aecadf510169b164f4703f9afb7df145cdd2b14fbf9d0a3c",
		"handoff":   "9ed5bfef573b06980e06e630d259c6a015ff64587c5f74b94b77eb66cf4b914b",
		"recovered": "408e203ecb9f33b38b3edbf5851a0c9fd1715a5985773c0a5a4c94395fa5b3f3",
		"statement": "c6aa64a5ea1940aca0933b02b7a33d2705d760b247fc2755c114291378f09912",
	}
	if len(got) != len(want) {
		t.Fatalf("golden set has %d entries, produced %d", len(want), len(got))
	}
	for name, b := range got {
		sum := sha256.Sum256(b)
		if h := hex.EncodeToString(sum[:]); h != want[name] {
			t.Errorf("%s: sha256 %s (%d bytes), want %s", name, h, len(b), want[name])
		}
	}
}
