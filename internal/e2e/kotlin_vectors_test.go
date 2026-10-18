package e2e

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// kotlinVectors are made by the Android client's :core tests
// (android/core, KotlinVectorsTest): the reverse of vectors.json.
type kotlinVectors struct {
	Root             string `json:"root"`
	AccountPublicKey string `json:"account_public_key"`
	Wrap             string `json:"wrap"`
	Meta             string `json:"meta"`
	Command          string `json:"command"`
	Notify           string `json:"notify"`
	DeviceID         string `json:"device_id"`
	Envelope         struct {
		Key        string `json:"key"`
		Header     string `json:"header"`
		Nonce      string `json:"nonce"`
		Ciphertext string `json:"ciphertext"`
		Plaintext  string `json:"plaintext"`
		Seq        uint64 `json:"seq,string"`
	} `json:"envelope"`
	RegisterSignature struct {
		Message   string `json:"message"`
		Signature string `json:"signature"`
	} `json:"register_signature"`
	LoginSignature struct {
		DeviceSeed      string `json:"device_seed"`
		DevicePublicKey string `json:"device_public_key"`
		DeviceID        string `json:"device_id"`
		Challenge       string `json:"challenge"`
		Signature       string `json:"signature"`
	} `json:"login_signature"`
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
		Payload          struct {
			Header     string `json:"header"`
			Nonce      string `json:"nonce"`
			Ciphertext string `json:"ciphertext"`
		} `json:"payload"`
	} `json:"pairing"`
	// An envelope Kotlin sealed packed (Header.compression).
	Packed struct {
		Key        string `json:"key"`
		Header     string `json:"header"`
		Nonce      string `json:"nonce"`
		Ciphertext string `json:"ciphertext"`
		Plaintext  string `json:"plaintext"`
	} `json:"packed"`
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestKotlinVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/kotlin-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v kotlinVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	root := unhex(t, v.Root)
	k, err := Derive(root)
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2]string{
		"account": {hex.EncodeToString(k.AccountPublicKey()), v.AccountPublicKey},
		"wrap":    {hex.EncodeToString(k.Wrap), v.Wrap},
		"meta":    {hex.EncodeToString(k.Meta), v.Meta},
		"command": {hex.EncodeToString(k.Command), v.Command},
		"notify":  {hex.EncodeToString(k.Notify), v.Notify},
		"device":  {DeviceID(k.AccountPublicKey()), v.DeviceID},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s: Go %s, Kotlin %s", name, pair[0], pair[1])
		}
	}

	e := v.Envelope
	env := &pb.Envelope{Header: unhex(t, e.Header), Nonce: unhex(t, e.Nonce), Ciphertext: unhex(t, e.Ciphertext)}
	h, plain, err := Open(unhex(t, e.Key), env)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != e.Plaintext {
		t.Errorf("plaintext %q", plain)
	}
	pk := v.Packed
	ph, packed, err := Open(unhex(t, pk.Key), &pb.Envelope{Header: unhex(t, pk.Header), Nonce: unhex(t, pk.Nonce), Ciphertext: unhex(t, pk.Ciphertext)})
	if err != nil || string(packed) != pk.Plaintext || ph.Compression != pb.Compression_COMPRESSION_DEFLATE || len(pk.Ciphertext)/2 >= len(pk.Plaintext) {
		t.Errorf("packed envelope: %v, compression %v", err, ph.GetCompression())
	}
	if h.Seq != e.Seq || e.Seq != 18446744073709551600 {
		t.Errorf("seq %d, want %d", h.Seq, e.Seq)
	}
	if err := Expect(h, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, MachineId: "machine-k", SessionId: "session-k", Seq: e.Seq, RequestId: "req-k"}); err != nil {
		t.Error(err)
	}

	reg := unhex(t, v.RegisterSignature.Message)
	if !VerifyRegistration(k.AccountPublicKey(), reg, unhex(t, v.RegisterSignature.Signature)) {
		t.Error("registration signature does not verify")
	}
	if got := hex.EncodeToString(k.SignRegistration(reg)); got != v.RegisterSignature.Signature {
		t.Error("registration signature differs")
	}
	ls := v.LoginSignature
	dev := ed25519.NewKeyFromSeed(unhex(t, ls.DeviceSeed))
	pub := dev.Public().(ed25519.PublicKey)
	if hex.EncodeToString(pub) != ls.DevicePublicKey || DeviceID(pub) != ls.DeviceID {
		t.Error("device key differs")
	}
	if !VerifyLogin(pub, unhex(t, ls.Challenge), unhex(t, ls.Signature)) {
		t.Error("login signature does not verify")
	}

	p := v.Pairing
	ik, err := pairKeyFrom(unhex(t, p.InitiatorPrivate))
	if err != nil {
		t.Fatal(err)
	}
	jk, err := pairKeyFrom(unhex(t, p.JoinerPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(ik.Public()) != p.InitiatorPublic || hex.EncodeToString(jk.Public()) != p.JoinerPublic {
		t.Error("pairing public keys differ")
	}
	if hex.EncodeToString(PairCommit(ik.Public())) != p.Commit {
		t.Error("commit differs")
	}
	tr := PairTranscript(p.PairingID, ik.Public(), jk.Public(), p.JoinerKind)
	if hex.EncodeToString(tr) != p.Transcript {
		t.Error("transcript differs")
	}
	s, err := jk.Derive(ik.Public(), tr)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(s.SAS[:]) != p.SAS || hex.EncodeToString(s.Key) != p.Key || !slices.Equal(s.Emoji(), p.Emoji) {
		t.Error("pairing secrets differ")
	}
	var payload pb.PairingPayload
	penv := &pb.Envelope{Header: unhex(t, p.Payload.Header), Nonce: unhex(t, p.Payload.Nonce), Ciphertext: unhex(t, p.Payload.Ciphertext)}
	if _, err := OpenMessage(s.Key, penv, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: p.PairingID}, &payload); err != nil {
		t.Fatal(err)
	}
	rootKeys, err := Derive(unhex(t, v.Root))
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.AccountPublicKey) > 0 && !slices.Equal(payload.AccountPublicKey, rootKeys.AccountPublicKey()) || payload.Server != "https://ra.example.com" {
		t.Errorf("pairing payload account %x, server %q", payload.AccountPublicKey, payload.Server)
	}
}
