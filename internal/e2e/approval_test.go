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

func TestApprovalRejectsSignatureAndIdentitySubstitution(t *testing.T) {
	signer := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32))
	candidate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{2}, 32)).Public().(ed25519.PublicKey)
	raw, sig, err := NewPairApproval("11111111111111111111111111111111", "22222222222222222222222222222222", signer, candidate, pb.DeviceKind_DEVICE_KIND_CLIENT, 4102444800000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyPairApproval(raw, sig); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*pb.PairApproval){
		func(a *pb.PairApproval) { a.CandidateDevicePublicKey[0] ^= 1 },
		func(a *pb.PairApproval) { a.CandidateKind = pb.DeviceKind_DEVICE_KIND_MACHINE },
		func(a *pb.PairApproval) { a.PairingId = "33333333333333333333333333333333" },
		func(a *pb.PairApproval) { a.AccountId = "33333333333333333333333333333333" },
		func(a *pb.PairApproval) { a.ExpiresMs++ },
	} {
		a, _ := VerifyPairApproval(raw, sig)
		change(a)
		changed, _ := proto.Marshal(a)
		if _, err := VerifyPairApproval(changed, sig); err == nil {
			t.Fatal("substituted approval verified")
		}
	}
	x, _ := NewPairKey()
	y, _ := NewPairKey()
	first := PairIdentityTranscript("pair", x.Public(), y.Public(), candidate, 2)
	candidate[0] ^= 1
	second := PairIdentityTranscript("pair", x.Public(), y.Public(), candidate, 2)
	if bytes.Equal(first, second) {
		t.Fatal("SAS is not bound to the permanent signing identity")
	}
}

// Fixed synthetic identities, never deployment keys. Other client languages
// independently verify these signed bytes and the identity-bound SAS.
func TestPairApprovalVectors(t *testing.T) {
	signerSeed, candidateSeed, root := bytes.Repeat([]byte{0x11}, 32), bytes.Repeat([]byte{0x22}, 32), bytes.Repeat([]byte{0x33}, 32)
	signer := ed25519.NewKeyFromSeed(signerSeed)
	candidate := ed25519.NewKeyFromSeed(candidateSeed)
	keys, _ := Derive(root)
	a := &pb.PairApproval{AccountId: AccountID(keys.AccountPublicKey()), PairingId: "0123456789abcdef0123456789abcdef", ApproverDeviceId: DeviceID(signer.Public().(ed25519.PublicKey)), ApproverPublicKey: signer.Public().(ed25519.PublicKey), CandidateDevicePublicKey: candidate.Public().(ed25519.PublicKey), CandidateKind: pb.DeviceKind_DEVICE_KIND_CLIENT, ExpiresMs: 4102444800000, Nonce: bytes.Repeat([]byte{0x44}, 16)}
	raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(a)
	sig := ed25519.Sign(signer, append([]byte(PairApprovalContext), raw...))
	x, _ := pairKeyFrom(bytes.Repeat([]byte{0x55}, 32))
	y, _ := pairKeyFrom(bytes.Repeat([]byte{0x66}, 32))
	tr := PairIdentityTranscript(a.PairingId, x.Public(), y.Public(), a.CandidateDevicePublicKey, 2)
	shared, _ := x.Derive(y.Public(), tr)
	values := map[string]string{"root": hex.EncodeToString(root), "approver_seed": hex.EncodeToString(signerSeed), "candidate_seed": hex.EncodeToString(candidateSeed), "candidate_public": hex.EncodeToString(a.CandidateDevicePublicKey), "approval": hex.EncodeToString(raw), "signature": hex.EncodeToString(sig), "initiator_public": hex.EncodeToString(x.Public()), "joiner_public": hex.EncodeToString(y.Public()), "transcript": hex.EncodeToString(tr), "sas": hex.EncodeToString(shared.SAS[:]), "pairing_id": a.PairingId}
	b, _ := json.MarshalIndent(values, "", "  ")
	b = append(b, '\n')
	const path = "testdata/pair-approval-vectors.json"
	if *update {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, b) {
		t.Fatalf("approval vectors need regeneration: %v", err)
	}
}
