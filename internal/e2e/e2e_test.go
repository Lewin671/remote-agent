package e2e

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

var update = flag.Bool("update", false, "rewrite testdata/vectors.json")

func TestSealOpen(t *testing.T) {
	key := NewKey()
	h := &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, MachineId: "m", SessionId: "s", Seq: 7, Sender: "m"}
	env, err := Seal(key, h, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	got, plain, err := Open(key, env)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "hello" || got.Seq != 7 || got.Version != Version {
		t.Fatalf("got %v %q", got, plain)
	}
	if err := Expect(got, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, SessionId: "s", Seq: 7}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []*pb.Header{
		{Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND},
		{Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, SessionId: "other"},
		{Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, Seq: 8},
		{Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, MachineId: "x"},
	} {
		if Expect(got, want) == nil {
			t.Errorf("Expect(%v) passed", want)
		}
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	key := NewKey()
	h := &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, SessionId: "s", Seq: 1}
	env, err := Seal(key, h, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	moved, _ := proto.Marshal(&pb.Header{Version: Version, Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, SessionId: "s", Seq: 2})
	cases := map[string]*pb.Envelope{
		"header":     {Header: moved, Nonce: env.Nonce, Ciphertext: env.Ciphertext},
		"ciphertext": {Header: env.Header, Nonce: env.Nonce, Ciphertext: flip(env.Ciphertext)},
		"nonce":      {Header: env.Header, Nonce: flip(env.Nonce), Ciphertext: env.Ciphertext},
		"short":      {Header: env.Header, Nonce: env.Nonce[:8], Ciphertext: env.Ciphertext},
	}
	for name, e := range cases {
		if _, _, err := Open(key, e); !errors.Is(err, ErrOpen) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, _, err := Open(NewKey(), env); !errors.Is(err, ErrOpen) {
		t.Errorf("wrong key: err = %v", err)
	}
}

func TestOpenRejectsOtherVersion(t *testing.T) {
	key := NewKey()
	env, err := Seal(key, &pb.Header{Version: 2, Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(key, env); !errors.Is(err, ErrVersion) {
		t.Fatalf("err = %v", err)
	}
}

func TestDeriveSeparatesPurposes(t *testing.T) {
	k, err := Derive(NewKey())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(k.Wrap, k.Meta) || bytes.Equal(k.Wrap, k.Command) || bytes.Equal(k.Meta, k.Command) {
		t.Fatal("derived keys repeat")
	}
	if _, err := Derive([]byte("short")); err == nil {
		t.Fatal("short root key accepted")
	}
}

func TestSignatures(t *testing.T) {
	k, _ := Derive(NewKey())
	reg := []byte("registration")
	sig := k.SignRegistration(reg)
	if !VerifyRegistration(k.AccountPublicKey(), reg, sig) {
		t.Fatal("registration signature does not verify")
	}
	if VerifyRegistration(k.AccountPublicKey(), []byte("other"), sig) {
		t.Fatal("signature verifies for other data")
	}
	// A registration signature is not a login signature.
	if VerifyLogin(k.AccountPublicKey(), reg, sig) {
		t.Fatal("signature crosses contexts")
	}
	_, dev, _ := ed25519.GenerateKey(nil)
	ch := []byte("challenge")
	if !VerifyLogin(dev.Public().(ed25519.PublicKey), ch, SignLogin(dev, ch)) {
		t.Fatal("login signature does not verify")
	}
}

func TestSecret(t *testing.T) {
	s := Secret{Root: NewKey(), Server: "https://ra.example.com"}
	got, err := ParseSecret(" " + s.String() + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Root, s.Root) || got.Server != s.Server {
		t.Fatalf("got %+v", got)
	}
	for _, bad := range []string{"", "ra1.", "ra2.AAAA.AAAA", "ra1.AAAA.aHR0cHM", "ra1." + s.String()[4:40]} {
		if _, err := ParseSecret(bad); err == nil {
			t.Errorf("ParseSecret(%q) succeeded", bad)
		}
	}
}

func flip(b []byte) []byte {
	c := bytes.Clone(b)
	c[0] ^= 1
	return c
}

// vectors pins the scheme so that the web client, which has its own
// implementation, can be checked against this one.
type vectors struct {
	Root             string `json:"root"`
	AccountPublicKey string `json:"account_public_key"`
	Wrap             string `json:"wrap"`
	Meta             string `json:"meta"`
	Command          string `json:"command"`
	DeviceID         string `json:"device_id"`
	Secret           string `json:"secret"`
	Envelope         struct {
		Key        string `json:"key"`
		Header     string `json:"header"`
		Nonce      string `json:"nonce"`
		Ciphertext string `json:"ciphertext"`
		Plaintext  string `json:"plaintext"`
	} `json:"envelope"`
	RegisterSignature struct {
		Message   string `json:"message"`
		Signature string `json:"signature"`
	} `json:"register_signature"`
	Pairing struct {
		InitiatorPrivate string   `json:"initiator_private"`
		JoinerPrivate    string   `json:"joiner_private"`
		InitiatorPublic  string   `json:"initiator_public"`
		JoinerPublic     string   `json:"joiner_public"`
		PairingID        string   `json:"pairing_id"`
		JoinerKind       int32    `json:"joiner_kind"`
		Commit           string   `json:"commit"`
		Transcript       string   `json:"transcript"`
		SAS              string   `json:"sas"`
		Key              string   `json:"key"`
		Emoji            []string `json:"emoji"`
	} `json:"pairing"`
}

func TestVectors(t *testing.T) {
	root := bytes.Repeat([]byte{0x42}, KeySize)
	k, err := Derive(root)
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	v.Root = hex.EncodeToString(root)
	v.AccountPublicKey = hex.EncodeToString(k.AccountPublicKey())
	v.Wrap, v.Meta, v.Command = hex.EncodeToString(k.Wrap), hex.EncodeToString(k.Meta), hex.EncodeToString(k.Command)
	v.DeviceID = DeviceID(k.AccountPublicKey())
	v.Secret = Secret{Root: root, Server: "https://ra.example.com"}.String()
	key := bytes.Repeat([]byte{0x07}, KeySize)
	env, err := sealWithNonce(key, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, MachineId: "machine", SessionId: "session", Seq: 3, Sender: "machine",
	}, []byte("plaintext ✓"), bytes.Repeat([]byte{0x09}, 12))
	if err != nil {
		t.Fatal(err)
	}
	v.Envelope.Key = hex.EncodeToString(key)
	v.Envelope.Header = hex.EncodeToString(env.Header)
	v.Envelope.Nonce = hex.EncodeToString(env.Nonce)
	v.Envelope.Ciphertext = hex.EncodeToString(env.Ciphertext)
	v.Envelope.Plaintext = "plaintext ✓"
	reg := []byte("registration")
	v.RegisterSignature.Message = hex.EncodeToString(reg)
	v.RegisterSignature.Signature = hex.EncodeToString(k.SignRegistration(reg))

	ip, jp := bytes.Repeat([]byte{0x11}, 32), bytes.Repeat([]byte{0x22}, 32)
	ik, _ := pairKeyFrom(ip)
	jk, _ := pairKeyFrom(jp)
	pv := &v.Pairing
	pv.InitiatorPrivate, pv.JoinerPrivate = hex.EncodeToString(ip), hex.EncodeToString(jp)
	pv.InitiatorPublic, pv.JoinerPublic = hex.EncodeToString(ik.Public()), hex.EncodeToString(jk.Public())
	pv.PairingID, pv.JoinerKind = "0123456789abcdef0123456789abcdef", 2
	pv.Commit = hex.EncodeToString(PairCommit(ik.Public()))
	tr := PairTranscript(pv.PairingID, ik.Public(), jk.Public(), pv.JoinerKind)
	pv.Transcript = hex.EncodeToString(tr)
	ps, err := ik.Derive(jk.Public(), tr)
	if err != nil {
		t.Fatal(err)
	}
	pv.SAS, pv.Key = hex.EncodeToString(ps.SAS[:]), hex.EncodeToString(ps.Key)
	pv.Emoji = SASEmoji[:]

	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const path = "testdata/vectors.json"
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date; run go test ./internal/e2e -update", path)
	}
}

func TestPairing(t *testing.T) {
	init, _ := NewPairKey()
	join, _ := NewPairKey()
	tr := PairTranscript("pid", init.Public(), join.Public(), 2)
	a, err := init.Derive(join.Public(), tr)
	if err != nil {
		t.Fatal(err)
	}
	b, err := join.Derive(init.Public(), tr)
	if err != nil {
		t.Fatal(err)
	}
	if a.SAS != b.SAS || !bytes.Equal(a.Key, b.Key) {
		t.Fatal("sides disagree")
	}
	// A swapped key gives other emoji (with overwhelming probability).
	mitm, _ := NewPairKey()
	c, _ := join.Derive(mitm.Public(), PairTranscript("pid", mitm.Public(), join.Public(), 2))
	if c.SAS == a.SAS {
		t.Fatal("swapped key gave the same SAS")
	}
	if _, err := init.Derive(make([]byte, 32), tr); err == nil {
		t.Fatal("accepted an all-zero public key")
	}
	seen := map[string]bool{}
	for _, e := range SASEmoji {
		if e == "" || seen[e] {
			t.Fatalf("emoji table has %q twice or empty", e)
		}
		seen[e] = true
	}
}
