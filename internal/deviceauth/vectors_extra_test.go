package deviceauth

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Lewin671/remote-agent/shared/devicesec"
)

type recoveryVector struct {
	Genesis         vectorState `json:"genesis"`
	Parent          vectorState `json:"parent"`
	Recovery        vectorState `json:"recovery"`
	Statement       string      `json:"recovery_statement"`
	OldRecoverySeed string      `json:"old_recovery_seed"`
	NewRecoverySeed string      `json:"new_recovery_seed"`
	NewPhoneSeed    string      `json:"new_phone_seed"`
	NewPhoneX       string      `json:"new_phone_x"`
	MachineX        string      `json:"machine_x"`
}

type chainVector struct {
	States    []vectorState `json:"states"`
	Fork      vectorState   `json:"fork"`
	BadReplay vectorState   `json:"bad_replay"`
}

// recoveryChainVector is a recovery whose parent already carries a non-empty
// removed set, so clients must accept the accumulated tombstones.
type recoveryChainVector struct {
	States          []vectorState `json:"states"`
	Recovery        vectorState   `json:"recovery"`
	Statement       string        `json:"recovery_statement"`
	OldRecoverySeed string        `json:"old_recovery_seed"`
	NewRecoverySeed string        `json:"new_recovery_seed"`
	NewPhoneSeed    string        `json:"new_phone_seed"`
	NewPhoneX       string        `json:"new_phone_x"`
	MachineX        string        `json:"machine_x"`
	RemovedID       string        `json:"removed_id"`
}

func vectorOf(t *testing.T, s devicesec.State) vectorState {
	t.Helper()
	raw, err := s.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	h, _ := s.Hash()
	return vectorState{State: base64.StdEncoding.EncodeToString(raw), Hash: hex.EncodeToString(h[:])}
}

func buildRecoveryVector(t *testing.T) recoveryVector {
	t.Helper()
	g, _, phoneEd, _, recovery := testGenesis(t)
	secret1 := bytes.Repeat([]byte{0x33}, 32)
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	machine, _, machineX := testMember(t, 0x44, devicesec.Machine)
	secret2 := bytes.Repeat([]byte{0x55}, 32)
	parent, err := BuildState(g, []devicesec.Member{phone, machine}, nil, secret2, mustPrev(t, secret2, secret1), phoneEd, 1_700_000_100_000)
	if err != nil {
		t.Fatal(err)
	}
	newPhone, newPhoneEd, newPhoneX := testMember(t, 0x77, devicesec.Client)
	var newRecoveryPub [32]byte
	newRecoveryPub[0] = 0x88
	recoveryState, stmt, err := BuildRecovery(parent, newPhone, []devicesec.Member{machine, newPhone}, bytes.Repeat([]byte{0x99}, 32), newPhoneEd, recovery, newRecoveryPub, 1_700_000_200_000)
	if err != nil {
		t.Fatal(err)
	}
	stmtRaw, err := stmt.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return recoveryVector{
		Genesis: vectorOf(t, g), Parent: vectorOf(t, parent), Recovery: vectorOf(t, recoveryState),
		Statement:       base64.StdEncoding.EncodeToString(stmtRaw),
		OldRecoverySeed: hex.EncodeToString(recovery.Seed()),
		NewRecoverySeed: hex.EncodeToString(bytes.Repeat([]byte{0x88}, 32)),
		NewPhoneSeed:    hex.EncodeToString(newPhoneEd.Seed()),
		NewPhoneX:       hex.EncodeToString(newPhoneX),
		MachineX:        hex.EncodeToString(machineX),
	}
}

func buildRecoveryWithRemovedParentVector(t *testing.T) recoveryChainVector {
	t.Helper()
	g, _, phoneEd, _, recovery := testGenesis(t)
	secret1 := bytes.Repeat([]byte{0x33}, 32)
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	machine, _, machineX := testMember(t, 0x44, devicesec.Machine)
	client, _, _ := testMember(t, 0x55, devicesec.Client)
	secret2 := bytes.Repeat([]byte{0x55}, 32)
	added := mustBuildCurrent(t, g, []devicesec.Member{phone, machine}, nil, secret2, secret1, phoneEd, 1_700_000_100_000)
	secret3 := bytes.Repeat([]byte{0x66}, 32)
	withClient := mustBuildCurrent(t, added, []devicesec.Member{phone, machine, client}, nil, secret3, secret2, phoneEd, 1_700_000_150_000)
	secret4 := bytes.Repeat([]byte{0x77}, 32)
	parent := mustBuildCurrent(t, withClient, []devicesec.Member{phone, machine}, []string{client.ID}, secret4, secret3, phoneEd, 1_700_000_200_000)
	newPhone, newPhoneEd, newPhoneX := testMember(t, 0x77, devicesec.Client)
	var newRecoveryPub [32]byte
	newRecoveryPub[0] = 0x88
	recoveryState, stmt, err := BuildRecovery(parent, newPhone, []devicesec.Member{machine, newPhone}, bytes.Repeat([]byte{0x99}, 32), newPhoneEd, recovery, newRecoveryPub, 1_700_000_200_000)
	if err != nil {
		t.Fatal(err)
	}
	stmtRaw, err := stmt.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return recoveryChainVector{
		States:          []vectorState{vectorOf(t, g), vectorOf(t, added), vectorOf(t, withClient), vectorOf(t, parent)},
		Recovery:        vectorOf(t, recoveryState),
		Statement:       base64.StdEncoding.EncodeToString(stmtRaw),
		OldRecoverySeed: hex.EncodeToString(recovery.Seed()),
		NewRecoverySeed: hex.EncodeToString(bytes.Repeat([]byte{0x88}, 32)),
		NewPhoneSeed:    hex.EncodeToString(newPhoneEd.Seed()),
		NewPhoneX:       hex.EncodeToString(newPhoneX),
		MachineX:        hex.EncodeToString(machineX),
		RemovedID:       client.ID,
	}
}

func buildChainVector(t *testing.T) chainVector {
	t.Helper()
	g, _, phoneEd, _, _ := testGenesis(t)
	secret1 := bytes.Repeat([]byte{0x33}, 32)
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	machine, _, _ := testMember(t, 0x44, devicesec.Machine)
	secret2 := bytes.Repeat([]byte{0x44}, 32)
	added := mustBuildCurrent(t, g, []devicesec.Member{phone, machine}, nil, secret2, secret1, phoneEd, 1_700_000_100_000)
	renew1 := mustRotate(t, added, secret2, phoneEd, 1_700_000_150_000)
	client, _, _ := testMember(t, 0x55, devicesec.Client)
	secret3 := bytes.Repeat([]byte{0x55}, 32)
	withClient := mustBuildCurrent(t, renew1, []devicesec.Member{phone, machine, client}, nil, secret3, secret2, phoneEd, 1_700_000_200_000)
	renew2 := mustRotate(t, withClient, secret3, phoneEd, 1_700_000_250_000)
	secret4 := bytes.Repeat([]byte{0x66}, 32)
	removed := mustBuildCurrent(t, renew2, []devicesec.Member{phone, client}, []string{machine.ID}, secret4, secret3, phoneEd, 1_700_000_300_000)
	renew3 := mustRotate(t, removed, secret4, phoneEd, 1_700_000_350_000)
	var newRecovery [32]byte
	newRecovery[0] = 0x91
	rotated, err := BuildRecoveryKeyRotation(renew3, secret4, newRecovery, phoneEd, 1_700_000_400_000)
	if err != nil {
		t.Fatal(err)
	}
	forkMachine, _, _ := testMember(t, 0x99, devicesec.Machine)
	fork, err := BuildPairState(g, forkMachine, forkMachine.XPublic[:], secret1, bytes.Repeat([]byte{0x77}, 32), phoneEd, 1_700_000_100_000)
	if err != nil {
		t.Fatal(err)
	}
	bad := cloneState(added)
	bad.Signature[0] ^= 1
	states := []devicesec.State{g, added, renew1, withClient, renew2, removed, renew3, rotated}
	out := chainVector{States: make([]vectorState, len(states)), Fork: vectorOf(t, fork), BadReplay: vectorOf(t, bad)}
	for i, s := range states {
		out.States[i] = vectorOf(t, s)
	}
	return out
}

func mustRotate(t *testing.T, parent devicesec.State, secret []byte, signer ed25519.PrivateKey, now uint64) devicesec.State {
	t.Helper()
	s, err := rotateForTest(parent, secret, signer, now)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRecoveryVector(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "recovery-v3.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v recoveryVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	parse := func(s vectorState) devicesec.State {
		b, err := base64.StdEncoding.DecodeString(s.State)
		if err != nil {
			t.Fatal(err)
		}
		st, err := devicesec.ParseState(b)
		if err != nil {
			t.Fatal(err)
		}
		h, _ := st.Hash()
		if hex.EncodeToString(h[:]) != s.Hash {
			t.Fatalf("hash mismatch at epoch %d", st.Core.Epoch)
		}
		return st
	}
	genesis := parse(v.Genesis)
	parent := parse(v.Parent)
	rec := parse(v.Recovery)
	straw, _ := base64.StdEncoding.DecodeString(v.Statement)
	stmt, err := devicesec.ParseRecoveryStatement(straw)
	if err != nil {
		t.Fatal(err)
	}
	if err := devicesec.VerifyTransition(parent, rec, &stmt); err != nil {
		t.Fatal(err)
	}
	if len(rec.PrevSecret) != 0 {
		t.Fatal("recovery carries PrevSecret")
	}
	pin, err := genesis.Pin()
	if err != nil {
		t.Fatal(err)
	}
	machineX := mustHex(t, v.MachineX)
	var machine devicesec.Member
	for _, m := range rec.Core.Members {
		if m.Kind == devicesec.Machine {
			machine = m
		}
	}
	if _, err := OpenGrant(pin, rec, machine.ID, machineX); err != nil {
		t.Fatalf("retained machine grant: %v", err)
	}
	oldPhone := parent.Core.AuthorityID
	if _, err := OpenGrant(pin, rec, oldPhone, bytes.Repeat([]byte{1}, 32)); err == nil {
		t.Fatal("old authority still has a grant")
	}
	newSeed := mustHex(t, v.NewPhoneSeed)
	newX := mustHex(t, v.NewPhoneX)
	newID := devicesec.ID(ed25519.NewKeyFromSeed(newSeed).Public().(ed25519.PublicKey))
	ep, err := initTestEndpoint("", pin, []devicesec.State{genesis, parent, rec}, []*devicesec.RecoveryStatement{nil, nil, &stmt}, newID, newSeed, newX)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	if _, err := ep.ContentSecret(parent.Core.ContentEpoch); err != ErrUnavailable {
		t.Fatalf("new phone read pre-recovery content: %v", err)
	}
}

func TestRecoveryWithRemovedParentVector(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "recovery-removed-v3.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v recoveryChainVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	parse := func(s vectorState) devicesec.State {
		b, err := base64.StdEncoding.DecodeString(s.State)
		if err != nil {
			t.Fatal(err)
		}
		st, err := devicesec.ParseState(b)
		if err != nil {
			t.Fatal(err)
		}
		h, _ := st.Hash()
		if hex.EncodeToString(h[:]) != s.Hash {
			t.Fatalf("hash mismatch at epoch %d", st.Core.Epoch)
		}
		return st
	}
	states := make([]devicesec.State, len(v.States))
	for i := range v.States {
		states[i] = parse(v.States[i])
	}
	pin, err := states[0].Pin()
	if err != nil {
		t.Fatal(err)
	}
	proofs := make([]*devicesec.RecoveryStatement, len(states))
	for i := range states {
		if i > 0 {
			if err := devicesec.VerifyTransition(states[i-1], states[i], nil); err != nil {
				t.Fatalf("state %d: %v", i, err)
			}
		}
	}
	parent := states[len(states)-1]
	if len(parent.Core.Removed) == 0 || parent.Core.Removed[0] != v.RemovedID {
		t.Fatalf("parent removed = %v, want %s", parent.Core.Removed, v.RemovedID)
	}
	rec := parse(v.Recovery)
	straw, _ := base64.StdEncoding.DecodeString(v.Statement)
	stmt, err := devicesec.ParseRecoveryStatement(straw)
	if err != nil {
		t.Fatal(err)
	}
	if err := devicesec.VerifyTransition(parent, rec, &stmt); err != nil {
		t.Fatalf("recovery with a removed parent: %v", err)
	}
	if !slices.Contains(rec.Core.Removed, v.RemovedID) || !slices.Contains(rec.Core.Removed, parent.Core.AuthorityID) {
		t.Fatalf("recovery removed = %v, want the accumulated %s and the old authority", rec.Core.Removed, v.RemovedID)
	}
	newSeed := mustHex(t, v.NewPhoneSeed)
	newX := mustHex(t, v.NewPhoneX)
	newID := devicesec.ID(ed25519.NewKeyFromSeed(newSeed).Public().(ed25519.PublicKey))
	chain := append(append([]devicesec.State{}, states...), rec)
	chainProofs := append(append([]*devicesec.RecoveryStatement{}, proofs...), &stmt)
	ep, err := initTestEndpoint("", pin, chain, chainProofs, newID, newSeed, newX)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
}

func TestChainVector(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "chain-v3.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v chainVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	parse := func(s vectorState) devicesec.State {
		b, err := base64.StdEncoding.DecodeString(s.State)
		if err != nil {
			t.Fatal(err)
		}
		st, err := devicesec.ParseState(b)
		if err != nil {
			t.Fatal(err)
		}
		h, _ := st.Hash()
		if hex.EncodeToString(h[:]) != s.Hash {
			t.Fatalf("hash mismatch at epoch %d", st.Core.Epoch)
		}
		return st
	}
	states := make([]devicesec.State, len(v.States))
	for i := range v.States {
		states[i] = parse(v.States[i])
	}
	pin, err := states[0].Pin()
	if err != nil {
		t.Fatal(err)
	}
	proofs := make([]*devicesec.RecoveryStatement, len(states))
	if err := verifyChain(pin, states, proofs); err != nil {
		t.Fatal(err)
	}
	fork := parse(v.Fork)
	if err := devicesec.VerifyTransition(states[0], fork, nil); err != nil {
		t.Fatalf("fork transition: %v", err)
	}
	fh, _ := fork.Hash()
	sh, _ := states[1].Hash()
	if fh == sh {
		t.Fatal("fork has the same hash as the stored state")
	}
	bad := parse(v.BadReplay)
	if err := devicesec.VerifyTransition(states[0], bad, nil); err == nil {
		t.Fatal("tampered replay verified")
	}
	if got := states[len(states)-1].Core.Removed; len(got) != 1 {
		t.Fatalf("accumulated removed = %v", got)
	}
}

func TestGenerateExtraVectors(t *testing.T) {
	if os.Getenv("GEN_VECTORS") == "" {
		t.Skip("set GEN_VECTORS=1 to regenerate")
	}
	write := func(name string, v any) {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("testdata", name), append(b, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("recovery-v3.json", buildRecoveryVector(t))
	write("recovery-removed-v3.json", buildRecoveryWithRemovedParentVector(t))
	write("chain-v3.json", buildChainVector(t))
}
