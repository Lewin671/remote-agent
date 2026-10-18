package deviceauth

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type vectors struct {
	Account           string      `json:"account"`
	Domain            string      `json:"domain"`
	PhoneSeed         string      `json:"phone_seed"`
	PhoneX            string      `json:"phone_x"`
	MachineSeed       string      `json:"machine_seed"`
	MachineX          string      `json:"machine_x"`
	RecoverySeed      string      `json:"recovery_seed"`
	ContentSecret1    string      `json:"content_secret_1"`
	ContentSecret2    string      `json:"content_secret_2"`
	ContentSecret3    string      `json:"content_secret_3"`
	Genesis           vectorState `json:"genesis"`
	Added             vectorState `json:"added"`
	Renewal           vectorState `json:"renewal"`
	Recovery          vectorState `json:"recovery"`
	RecoveryStatement string      `json:"recovery_statement"`
}
type vectorState struct {
	State string `json:"state"`
	Hash  string `json:"hash"`
}

func buildVectors(t *testing.T) vectors {
	t.Helper()
	g, pin, phoneEd, phoneX, recovery := testGenesis(t)
	secret1 := bytes.Repeat([]byte{0x33}, 32)
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	machine, machineEd, machineX := testMember(t, 0x44, Machine)
	secret2 := bytes.Repeat([]byte{0x55}, 32)
	added, err := BuildState(g, []Member{phone, machine}, nil, secret2, mustPrev(t, secret2, secret1), phoneEd, 1_700_000_100_000)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := BuildRenewal(added, secret2, phoneEd, 1_700_000_200_000)
	if err != nil {
		t.Fatal(err)
	}
	newPhone, newPhoneEd, newPhoneX := testMember(t, 0x77, Client)
	var newRecoveryPub [32]byte
	newRecoveryPub[0] = 0x88
	rec, stmt, err := BuildRecovery(renewed, newPhone, []Member{machine, newPhone}, bytes.Repeat([]byte{0x99}, 32), newPhoneEd, recovery, newRecoveryPub, 1_700_000_300_000)
	if err != nil {
		t.Fatal(err)
	}
	_ = pin
	_ = machineEd
	_ = newPhoneX
	b := func(s State) vectorState {
		raw, err := s.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		h, _ := s.Hash()
		return vectorState{State: base64.StdEncoding.EncodeToString(raw), Hash: hex.EncodeToString(h[:])}
	}
	stmtRaw, err := stmtBytes(stmt)
	if err != nil {
		t.Fatal(err)
	}
	return vectors{
		Account: g.Core.Account, Domain: hex.EncodeToString(g.Core.Domain[:]),
		PhoneSeed: hex.EncodeToString(phoneEd.Seed()), PhoneX: hex.EncodeToString(phoneX),
		MachineSeed: hex.EncodeToString(machineEd.Seed()), MachineX: hex.EncodeToString(machineX),
		RecoverySeed:   hex.EncodeToString(recovery.Seed()),
		ContentSecret1: hex.EncodeToString(secret1), ContentSecret2: hex.EncodeToString(secret2), ContentSecret3: hex.EncodeToString(bytes.Repeat([]byte{0x99}, 32)),
		Genesis: b(g), Added: b(added), Renewal: b(renewed), Recovery: b(rec),
		RecoveryStatement: base64.StdEncoding.EncodeToString(stmtRaw),
	}
}

func stmtBytes(r RecoveryStatement) ([]byte, error) {
	b, err := r.signBytes()
	if err != nil {
		return nil, err
	}
	e := encoder{}
	e.lp(b)
	e.lp(r.Signature)
	return e.b, nil
}
func parseStmt(raw []byte) (RecoveryStatement, error) {
	var r RecoveryStatement
	d := decoder{b: raw}
	b := d.lp(MaxStateBytes)
	r.Signature = d.lp(64)
	if d.done() != nil || len(r.Signature) != 64 {
		return r, ErrInvalid
	}
	const label = "ra3/recovery\x00"
	if len(b) < len(label) || string(b[:len(label)]) != label {
		return r, ErrInvalid
	}
	dd := decoder{b: b[len(label):]}
	r.Account = dd.str()
	r.Domain = dd.hash()
	r.Parent = dd.hash()
	r.NewAuthorityID = dd.str()
	copy(r.NewRecoveryEd[:], dd.raw(32))
	r.NewMember.ID = dd.str()
	copy(r.NewMember.EdPublic[:], dd.raw(32))
	copy(r.NewMember.XPublic[:], dd.raw(32))
	r.NewMember.Kind = Kind(dd.u32())
	r.IssuedAt = dd.u64()
	r.NotAfter = dd.u64()
	r.StateHash = dd.hash()
	r.Nonce = dd.hash()
	if dd.done() != nil {
		return r, ErrInvalid
	}
	return r, nil
}

func TestVectors(t *testing.T) {
	path := filepath.Join("testdata", "domain-v3.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	decodeState := func(s vectorState) State {
		b, err := base64.StdEncoding.DecodeString(s.State)
		if err != nil {
			t.Fatal(err)
		}
		st, err := ParseState(b)
		if err != nil {
			t.Fatal(err)
		}
		h, _ := st.Hash()
		if hex.EncodeToString(h[:]) != s.Hash {
			t.Fatalf("state hash mismatch at epoch %d", st.Core.Epoch)
		}
		return st
	}
	g := decodeState(v.Genesis)
	added := decodeState(v.Added)
	renewed := decodeState(v.Renewal)
	rec := decodeState(v.Recovery)
	if err := VerifyTransition(g, added, nil); err != nil {
		t.Fatalf("added: %v", err)
	}
	if err := VerifyTransition(added, renewed, nil); err != nil {
		t.Fatalf("renewal: %v", err)
	}
	stmtRaw, _ := base64.StdEncoding.DecodeString(v.RecoveryStatement)
	stmt, err := parseStmt(stmtRaw)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransition(renewed, rec, &stmt); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if len(rec.PrevSecret) != 0 {
		t.Fatal("recovery vector carries PrevSecret")
	}
	pin, _ := g.Pin()
	phoneX, _ := hex.DecodeString(v.PhoneX)
	if got, err := pin.OpenGrant(added, g.Core.AuthorityID, phoneX); err != nil || !bytes.Equal(got, mustHex(t, v.ContentSecret2)) {
		t.Fatalf("phone grant: %v", err)
	}
	if _, err := pin.OpenGrant(rec, g.Core.AuthorityID, phoneX); err == nil {
		t.Fatal("removed authority still has a grant in the recovery state")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGenerateVectors(t *testing.T) {
	if os.Getenv("GEN_VECTORS") == "" {
		t.Skip("set GEN_VECTORS=1 to regenerate")
	}
	v := buildVectors(t)
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", "domain-v3.json"), append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

type envelopeVector struct {
	Account string `json:"account"`
	Domain  string `json:"domain"`
	State   string `json:"state"`
	Secret  string `json:"content_secret"`
	Header  string `json:"header"`
	Nonce   string `json:"nonce"`
	Cipher  string `json:"cipher"`
	Origin  string `json:"origin"`
	Plain   string `json:"plain"`
}

func TestGenerateEnvelopeVector(t *testing.T) {
	if os.Getenv("GEN_VECTORS") == "" {
		t.Skip("set GEN_VECTORS=1 to regenerate")
	}
	g, _, phoneEd, _, _ := testGenesis(t)
	secret1 := bytes.Repeat([]byte{0x33}, 32)
	phone, _ := g.Core.Member(g.Core.AuthorityID)
	machine, _, _ := testMember(t, 0x44, Machine)
	secret2 := bytes.Repeat([]byte{0x55}, 32)
	added, err := BuildState(g, []Member{phone, machine}, nil, secret2, mustPrev(t, secret2, secret1), phoneEd, 1_700_000_100_000)
	if err != nil {
		t.Fatal(err)
	}
	h := Header{Account: added.Core.Account, Domain: added.Core.Domain, Epoch: added.Core.Epoch, State: mustHash(t, added), ContentEpoch: added.Core.ContentEpoch, Sender: phone.ID, Kind: 5, Purpose: Command, Machine: machine.ID, Request: "0123456789abcdef", Deadline: 1_700_000_200_000, Counter: 1}
	plain := []byte("protocol-10 vector")
	w, err := Seal(secret2, nil, h, plain, phoneEd)
	if err != nil {
		t.Fatal(err)
	}
	v := envelopeVector{Account: added.Core.Account, Domain: hex.EncodeToString(added.Core.Domain[:]), State: base64.StdEncoding.EncodeToString(mustRawState(t, added)), Secret: hex.EncodeToString(secret2), Header: base64.StdEncoding.EncodeToString(w.Header), Nonce: base64.StdEncoding.EncodeToString(w.Nonce), Cipher: base64.StdEncoding.EncodeToString(w.Cipher), Origin: base64.StdEncoding.EncodeToString(w.Origin), Plain: base64.StdEncoding.EncodeToString(plain)}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("..", "e2e", "testdata", "v3-vectors.json"), append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
func mustRawState(t *testing.T, s State) []byte {
	t.Helper()
	b, err := s.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
