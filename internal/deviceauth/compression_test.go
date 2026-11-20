package deviceauth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

// openCipher decrypts without checking the origin, to inspect packed or
// forged bodies.
func openCipher(secret, sessionSeed []byte, w devicesec.Ciphertext) (devicesec.Header, []byte, error) {
	h, err := devicesec.ParseHeader(w.Header)
	if err != nil {
		return h, nil, err
	}
	if _, err := w.Commitment(); err != nil {
		return h, nil, err
	}
	key, err := devicesec.DeriveKey(secret, sessionSeed, h)
	if err != nil {
		return h, nil, err
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return h, nil, err
	}
	aead, err := cipher.NewGCM(b)
	if err != nil {
		return h, nil, err
	}
	plain, err := aead.Open(nil, w.Nonce, w.Cipher, w.Header)
	if err != nil {
		return h, nil, devicesec.ErrUntrusted
	}
	return h, plain, nil
}

func TestPrepareCompression(t *testing.T) {
	c := newCurrentChain(t)
	machine, err := initTestEndpoint("", c.pin, c.states(), c.proofs(), c.machine.ID, c.machineEd.Seed(), c.machineX)
	if err != nil {
		t.Fatal(err)
	}
	defer machine.Close()
	client := c.clientEndpoint(t)
	defer client.Close()
	random := make([]byte, 1024)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		p          devicesec.Purpose
		body       []byte
		compressed bool
	}{
		{"threshold", devicesec.Event, bytes.Repeat([]byte("x"), 256), false},
		{"over-threshold", devicesec.Event, bytes.Repeat([]byte("x"), 257), true},
		{"incompressible", devicesec.Event, random, false},
		{"session-key", devicesec.SessionKey, bytes.Repeat([]byte("x"), 1024), false},
		{"image", devicesec.Image, bytes.Repeat([]byte("x"), 1024), false},
	} {
		t.Run(tc.name, func(t *testing.T) { checkPreparedCompression(t, machine, c.machineEd, tc.p, tc.body, tc.compressed) })
	}
	for _, p := range []devicesec.Purpose{devicesec.Event, devicesec.SessionMeta, devicesec.MachineMeta, devicesec.Result, devicesec.TerminalOutput, devicesec.Notify} {
		checkPreparedCompression(t, machine, c.machineEd, p, bytes.Repeat([]byte("payload"), 100), true)
	}
	for _, p := range []devicesec.Purpose{devicesec.Command, devicesec.TerminalInput, devicesec.TerminalResize, devicesec.TerminalHistory, devicesec.Prefs, devicesec.DeviceMeta} {
		checkPreparedCompression(t, client, c.clientEd, p, bytes.Repeat([]byte("payload"), 100), true)
	}
}

func checkPreparedCompression(t *testing.T, ep *Endpoint, ed ed25519.PrivateKey, p devicesec.Purpose, body []byte, compressed bool) {
	t.Helper()
	_, head, _ := ep.Snapshot()
	var machine string
	for _, m := range head.Core.Members {
		if m.Kind == devicesec.Machine {
			machine = m.ID
		}
	}
	h := devicesec.Header{Kind: p.Kind(), Purpose: p, Machine: machine, Session: "session", Seq: 1}
	switch p {
	case devicesec.MachineMeta:
		h.Session = ""
	case devicesec.Command, devicesec.Result:
		h.Request = "request-1234567890"
	case devicesec.TerminalInput, devicesec.TerminalOutput, devicesec.TerminalResize, devicesec.TerminalHistory, devicesec.Image, devicesec.Notify:
		h.Request = "request-1234567890"
	case devicesec.Prefs:
		h.Machine = ""
		h.Session = ""
		h.Request = "project-prefs"
	case devicesec.DeviceMeta:
		h.Machine = ""
		h.Session = ""
		h.Request = devicesec.ID(ed.Public().(ed25519.PublicKey))
	}
	if p.Control() {
		h.Deadline = head.Core.IssuedAt + 60000
	}
	seed := bytes.Repeat([]byte{0x91}, 32)
	w, err := ep.Prepare(h, seed, body)
	if err != nil {
		t.Fatal(err)
	}
	got, packed, err := openCipher(ep.data.Secret, seed, w)
	if err != nil {
		t.Fatal(err)
	}
	if (got.Compression == 1) != compressed {
		t.Fatalf("purpose %d compression=%d, want compressed=%v", p, got.Compression, compressed)
	}
	plain, err := Unpack(got, packed)
	if err != nil || !bytes.Equal(plain, body) {
		t.Fatalf("round trip purpose %d: %v", p, err)
	}
	if compressed && len(packed) >= len(body) {
		t.Fatal("compression expanded payload")
	}
	// Compression is bound by both the origin signature and AES-GCM AAD.
	got.Compression ^= 1
	w.Header, _ = got.MarshalBinary()
	if _, err := w.VerifyOrigin(head, head, false, head.Core.IssuedAt); err == nil {
		t.Fatal("compression tampering passed origin verification")
	}
	if _, _, err := openCipher(ep.data.Secret, seed, w); err == nil {
		t.Fatal("compression tampering passed AEAD")
	}
}

type compressedVector struct {
	Wire  string `json:"wire"`
	Plain string `json:"plain"`
	Seed  string `json:"seed"`
}

// These envelopes are read by the existing Go, web, Android and iOS vector suites.
func TestCompressedProtocol10Vectors(t *testing.T) {
	path := "testdata/domain-v3.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	stateRaw, _ := base64.StdEncoding.DecodeString(v.Added.State)
	head, err := devicesec.ParseState(stateRaw)
	if err != nil {
		t.Fatal(err)
	}
	genesisRaw, _ := base64.StdEncoding.DecodeString(v.Genesis.State)
	genesis, err := devicesec.ParseState(genesisRaw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := genesis.Pin(); err != nil {
		t.Fatal(err)
	}
	if len(v.CompressedEnvelopes) != 3 {
		t.Fatalf("compressed vectors=%d", len(v.CompressedEnvelopes))
	}
	secret := mustHex(t, v.ContentSecret2)
	for _, v := range v.CompressedEnvelopes {
		raw, _ := base64.StdEncoding.DecodeString(v.Wire)
		w, err := devicesec.ParseWire(raw)
		if err != nil {
			t.Fatal(err)
		}
		seed, _ := base64.StdEncoding.DecodeString(v.Seed)
		h, p, err := OpenEnvelope(w, secret, seed, head, head, false, head.Core.IssuedAt)
		if err != nil {
			t.Fatal(err)
		}
		if h.Compression != 1 {
			t.Fatal("vector is not compressed")
		}
		plain, err := Unpack(h, p)
		want, _ := base64.StdEncoding.DecodeString(v.Plain)
		if err != nil || !bytes.Equal(plain, want) {
			t.Fatalf("vector payload: %v", err)
		}
	}
}

func buildCompressedVectors(t *testing.T, pin devicesec.Pin, genesis, head devicesec.State, ed ed25519.PrivateKey, machineX []byte) []compressedVector {
	t.Helper()
	id := devicesec.ID(ed.Public().(ed25519.PublicKey))
	ep, err := initTestEndpoint("", pin, []devicesec.State{genesis, head}, []*devicesec.RecoveryStatement{nil, nil}, id, ed.Seed(), machineX)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	text := string(bytes.Repeat([]byte("compressible protocol-10 payload "), 32))
	seed := bytes.Repeat([]byte{0x91}, 32)
	var fixtures []compressedVector
	for _, tc := range []struct {
		p    devicesec.Purpose
		body proto.Message
	}{
		{devicesec.Event, &pb.Event{SessionId: "session", Seq: 1, Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: text}}}},
		{devicesec.SessionMeta, &pb.Session{Id: "session", Title: text}},
		{devicesec.TerminalOutput, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Output{Output: []byte(text)}}},
	} {
		h := devicesec.Header{Kind: tc.p.Kind(), Purpose: tc.p, Machine: id, Session: "session", Seq: 1}
		if tc.p == devicesec.TerminalOutput {
			h.Request = "terminal-1234567890"
		}
		body, err := proto.Marshal(tc.body)
		if err != nil {
			t.Fatal(err)
		}
		w, err := ep.Prepare(h, seed, body)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := w.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		fixtures = append(fixtures, compressedVector{base64.StdEncoding.EncodeToString(wire), base64.StdEncoding.EncodeToString(body), base64.StdEncoding.EncodeToString(seed)})
	}
	return fixtures
}
