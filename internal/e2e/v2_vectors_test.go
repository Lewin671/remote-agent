package e2e

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

// v2Vectors pins what client v2 added: the new key purposes, the envelope
// signature, and sample envelopes of the new kinds. It is a file of its
// own so that parsers of vectors.json need no change. Regenerate with
// go test ./internal/e2e -update.
type v2Vectors struct {
	// Root is the same root as in vectors.json.
	Root       string `json:"root"`
	Notify     string `json:"notify"`
	Prefs      string `json:"account_prefs"`
	DeviceMeta string `json:"device_meta"`

	// A COMMAND envelope sealed with the command key and signed by a
	// device, with the registration the server passes to the daemon.
	CommandSignature struct {
		DeviceSeed            string `json:"device_seed"`
		DevicePublicKey       string `json:"device_public_key"`
		DeviceID              string `json:"device_id"`
		Registration          string `json:"registration"`
		RegistrationSignature string `json:"registration_signature"`
		Header                string `json:"header"`
		Nonce                 string `json:"nonce"`
		Ciphertext            string `json:"ciphertext"`
		// Command is the plaintext: a serialized Command (a Send).
		Command       string `json:"command"`
		SignedMessage string `json:"signed_message"`
		Signature     string `json:"signature"`
	} `json:"command_signature"`

	// Sealed samples of the new payload kinds, with their keys.
	Image       sealedVector `json:"image"`
	Prefs2      sealedVector `json:"account_prefs_object"`
	DeviceMeta2 sealedVector `json:"device_meta_object"`
}

type sealedVector struct {
	Key        string `json:"key"`
	Header     string `json:"header"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
	// Plaintext is the serialized inner message.
	Plaintext string `json:"plaintext"`
}

func sealVector(t *testing.T, key []byte, h *pb.Header, m proto.Message, nonceByte byte) sealedVector {
	t.Helper()
	plain, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	env, err := sealWithNonce(key, h, plain, bytes.Repeat([]byte{nonceByte}, 12))
	if err != nil {
		t.Fatal(err)
	}
	return sealedVector{
		Key: hex.EncodeToString(key), Header: hex.EncodeToString(env.Header), Nonce: hex.EncodeToString(env.Nonce),
		Ciphertext: hex.EncodeToString(env.Ciphertext), Plaintext: hex.EncodeToString(plain),
	}
}

func TestV2Vectors(t *testing.T) {
	root := bytes.Repeat([]byte{0x42}, KeySize)
	k, err := Derive(root)
	if err != nil {
		t.Fatal(err)
	}
	var v v2Vectors
	v.Root = hex.EncodeToString(root)
	v.Notify, v.Prefs, v.DeviceMeta = hex.EncodeToString(k.Notify), hex.EncodeToString(k.Prefs), hex.EncodeToString(k.DeviceMeta)

	seed := bytes.Repeat([]byte{0x33}, ed25519.SeedSize)
	dev := ed25519.NewKeyFromSeed(seed)
	pub := dev.Public().(ed25519.PublicKey)
	id := DeviceID(pub)
	reg, err := proto.MarshalOptions{Deterministic: true}.Marshal(&pb.DeviceRegistration{
		AccountPublicKey: k.AccountPublicKey(), DevicePublicKey: pub, Kind: pb.DeviceKind_DEVICE_KIND_CLIENT, CreatedMs: 1790000000000,
	})
	if err != nil {
		t.Fatal(err)
	}
	regSig := k.SignRegistration(reg)
	cmd, err := proto.MarshalOptions{Deterministic: true}.Marshal(&pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{
		SessionId: "session", Text: "hello", RequestId: "0123456789abcdef0123456789abcdef", ImageIds: []string{"fedcba9876543210fedcba9876543210"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	env, err := sealWithNonce(k.Command, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND, MachineId: "machine", SessionId: "session",
		RequestId: "0123456789abcdef0123456789abcdef", DeadlineMs: 1790000060000, Sender: id,
	}, cmd, bytes.Repeat([]byte{0x0a}, 12))
	if err != nil {
		t.Fatal(err)
	}
	SignEnvelope(dev, env)
	if got, err := VerifySender(k.AccountPublicKey(), env, id, reg, regSig); err != nil || got != id {
		t.Fatalf("VerifySender = %q, %v", got, err)
	}
	cs := &v.CommandSignature
	cs.DeviceSeed, cs.DevicePublicKey, cs.DeviceID = hex.EncodeToString(seed), hex.EncodeToString(pub), id
	cs.Registration, cs.RegistrationSignature = hex.EncodeToString(reg), hex.EncodeToString(regSig)
	cs.Header, cs.Nonce, cs.Ciphertext = hex.EncodeToString(env.Header), hex.EncodeToString(env.Nonce), hex.EncodeToString(env.Ciphertext)
	cs.Command = hex.EncodeToString(cmd)
	cs.SignedMessage = hex.EncodeToString(SignedEnvelopeBytes(env))
	cs.Signature = hex.EncodeToString(env.Signature)

	sessionKey := bytes.Repeat([]byte{0x07}, KeySize)
	v.Image = sealVector(t, sessionKey, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_IMAGE, MachineId: "machine", SessionId: "session",
		RequestId: "00112233445566778899aabbccddeeff", Sender: "machine",
	}, &pb.ImagePayload{Format: pb.ImageFormat_IMAGE_FORMAT_PNG, Width: 1, Height: 1, Data: []byte("\x89PNG\r\n\x1a\n")}, 0x0b)
	v.Prefs2 = sealVector(t, k.Prefs, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_ACCOUNT_PREFS, RequestId: "project-prefs", Seq: 4, Sender: id,
	}, &pb.ProjectPrefs{Schema: 1, Pinned: []*pb.ProjectPref{{Path: "/Users/me/Code/foo", SinceMs: 1790000000000}},
		Hidden: []*pb.ProjectPref{{Path: "/tmp/old", SinceMs: 1790000001000}}, RecentOps: []string{"op-1"}}, 0x0c)
	v.DeviceMeta2 = sealVector(t, k.DeviceMeta, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_DEVICE_META, RequestId: id, Seq: 1, Sender: id,
	}, &pb.DeviceMeta{Name: "Pixel", Platform: "android", Description: "Android 16"}, 0x0d)

	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const path = "testdata/v2-vectors.json"
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

func TestEnvelopeSignature(t *testing.T) {
	root := bytes.Repeat([]byte{0x42}, KeySize)
	k, _ := Derive(root)
	_, dev, _ := ed25519.GenerateKey(nil)
	pub := dev.Public().(ed25519.PublicKey)
	id := DeviceID(pub)
	reg, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: k.AccountPublicKey(), DevicePublicKey: pub, Kind: pb.DeviceKind_DEVICE_KIND_CLIENT})
	regSig := k.SignRegistration(reg)
	env, err := Seal(k.Command, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND, Sender: id}, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySender(k.AccountPublicKey(), env, id, reg, regSig); err != ErrUnsigned {
		t.Fatalf("unsigned: %v", err)
	}
	SignEnvelope(dev, env)
	if _, err := VerifySender(k.AccountPublicKey(), env, id, reg, regSig); err != nil {
		t.Fatal(err)
	}
	// Another sender, a tampered part, or a registration the account did
	// not sign must all fail.
	if _, err := VerifySender(k.AccountPublicKey(), env, "other", reg, regSig); err == nil {
		t.Fatal("wrong sender accepted")
	}
	for i, part := range [][]byte{env.Header, env.Nonce, env.Ciphertext} {
		part[0] ^= 1
		if _, err := VerifySender(k.AccountPublicKey(), env, id, reg, regSig); err == nil {
			t.Fatalf("tampered part %d accepted", i)
		}
		part[0] ^= 1
	}
	other, _ := Derive(bytes.Repeat([]byte{0x43}, KeySize))
	if _, err := VerifySender(k.AccountPublicKey(), env, id, reg, other.SignRegistration(reg)); err == nil {
		t.Fatal("registration signed by another account accepted")
	}
}
