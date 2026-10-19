package e2e

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

const PairApprovalContext = "remote-agent/v1/pair-approval\x00"

var ErrPairApproval = errors.New("e2e: invalid device pairing approval")

// NewPairApproval is called only after the user confirms the SAS. The
// independent device key authorizes the candidate, never the shared root.
func NewPairApproval(accountID, pairingID string, signer ed25519.PrivateKey, candidate []byte, kind pb.DeviceKind, expires int64) ([]byte, []byte, error) {
	return NewPairApprovalReplacing(accountID, pairingID, signer, candidate, kind, expires, "")
}

// NewPairApprovalReplacing binds a fresh machine identity to an explicitly
// selected revoked machine's archive. Only the authority signs this decision.
func NewPairApprovalReplacing(accountID, pairingID string, signer ed25519.PrivateKey, candidate []byte, kind pb.DeviceKind, expires int64, replaces string) ([]byte, []byte, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	pub := signer.Public().(ed25519.PublicKey)
	a := &pb.PairApproval{AccountId: accountID, PairingId: pairingID, ApproverDeviceId: DeviceID(pub), ApproverPublicKey: pub, CandidateDevicePublicKey: candidate, CandidateKind: kind, ExpiresMs: expires, Nonce: nonce, ReplacesDeviceId: replaces}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(a)
	if err != nil {
		return nil, nil, err
	}
	sig := ed25519.Sign(signer, append([]byte(PairApprovalContext), raw...))
	if _, err := VerifyPairApproval(raw, sig); err != nil {
		return nil, nil, err
	}
	return raw, sig, nil
}

// VerifyPairApproval checks the signed bytes and identity bindings. The
// server must additionally check current membership and one-use admission
// inside its registration transaction. The joiner checks its own candidate,
// account, pairing and expiry before saving the received account.
func VerifyPairApproval(raw, sig []byte) (*pb.PairApproval, error) {
	a := &pb.PairApproval{}
	if len(raw) > 1024 || proto.Unmarshal(raw, a) != nil {
		return nil, ErrPairApproval
	}
	id := func(s string) bool {
		b, err := hex.DecodeString(s)
		return err == nil && len(b) == 16 && hex.EncodeToString(b) == s
	}
	if !id(a.AccountId) || !id(a.PairingId) || !id(a.ApproverDeviceId) || len(a.ApproverPublicKey) != ed25519.PublicKeySize || len(a.CandidateDevicePublicKey) != ed25519.PublicKeySize || len(a.Nonce) != 16 || a.ExpiresMs <= 0 {
		return nil, ErrPairApproval
	}
	if a.ReplacesDeviceId != "" && (!id(a.ReplacesDeviceId) || a.CandidateKind != pb.DeviceKind_DEVICE_KIND_MACHINE || a.ReplacesDeviceId == DeviceID(a.CandidateDevicePublicKey)) {
		return nil, ErrPairApproval
	}
	if a.CandidateKind != pb.DeviceKind_DEVICE_KIND_CLIENT && a.CandidateKind != pb.DeviceKind_DEVICE_KIND_MACHINE {
		return nil, ErrPairApproval
	}
	if a.ApproverDeviceId != DeviceID(a.ApproverPublicKey) || a.ApproverDeviceId == DeviceID(a.CandidateDevicePublicKey) || !ed25519.Verify(a.ApproverPublicKey, append([]byte(PairApprovalContext), raw...), sig) {
		return nil, ErrPairApproval
	}
	return a, nil
}
