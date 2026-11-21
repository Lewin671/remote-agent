package security

import (
	"bytes"
	"errors"
	"slices"

	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

// PairApprovalContext is the fixed domain-separation prefix for pair approvals.
const PairApprovalContext = "remote-agent/v1/pair-approval\x00"

var (
	errPairApproval = errors.New("security: invalid pair approval")
)

// VerifyPairApproval parses and verifies a signed v1.PairApproval payload using
// strict unknown-field checks and Ed25519 signature verification.
func VerifyPairApproval(raw, signature []byte) (*v1.PairApproval, error) {
	if len(raw) == 0 || len(raw) > 1024 || len(signature) != devicesec.Ed25519SignatureSize {
		return nil, errPairApproval
	}
	var a v1.PairApproval
	if err := a.Unmarshal(raw); err != nil || a.HasUnknown() {
		return nil, errPairApproval
	}
	if !devicesec.IsDeviceID(a.AccountId) || !devicesec.IsDeviceID(a.PairingId) || !devicesec.IsDeviceID(a.ApproverDeviceId) {
		return nil, errPairApproval
	}
	if len(a.ApproverPublicKey) != devicesec.Ed25519PublicKeySize || len(a.CandidateDevicePublicKey) != devicesec.Ed25519PublicKeySize || len(a.Nonce) != 16 || a.ExpiresMs <= 0 {
		return nil, errPairApproval
	}
	if a.CandidateKind != v1.DeviceKind_DEVICE_KIND_CLIENT && a.CandidateKind != v1.DeviceKind_DEVICE_KIND_MACHINE {
		return nil, errPairApproval
	}
	candID := devicesec.ID(a.CandidateDevicePublicKey)
	if a.ReplacesDeviceId != "" && (!devicesec.IsDeviceID(a.ReplacesDeviceId) || a.CandidateKind != v1.DeviceKind_DEVICE_KIND_MACHINE || a.ReplacesDeviceId == candID) {
		return nil, errPairApproval
	}
	if devicesec.ID(a.ApproverPublicKey) != a.ApproverDeviceId || a.ApproverDeviceId == candID {
		return nil, errPairApproval
	}
	msg := make([]byte, 0, len(PairApprovalContext)+len(raw))
	msg = append(msg, PairApprovalContext...)
	msg = append(msg, raw...)
	if !devicesec.VerifyEd25519(a.ApproverPublicKey, msg, signature) {
		return nil, errPairApproval
	}
	return &a, nil
}

// MachineReplacementOrigins verifies authority-signed replacement lineage proofs
// (oldest first) for target on account against the verified membership chain states.
func MachineReplacementOrigins(account, target string, states []devicesec.State, proofs []*v1.MachineReplacementProof) (map[string]bool, error) {
	origins := map[string]bool{}
	if len(proofs) == 0 {
		return origins, nil
	}
	if len(proofs) > devicesec.MaxPairStates {
		return nil, errPairApproval
	}
	if !devicesec.IsDeviceID(account) || !devicesec.IsDeviceID(target) {
		return nil, errPairApproval
	}
	cur := target
	for i := len(proofs) - 1; i >= 0; i-- {
		p := proofs[i]
		if p == nil || p.HasUnknown() {
			return nil, errPairApproval
		}
		a, err := VerifyPairApproval(p.Approval, p.Signature)
		if err != nil {
			return nil, err
		}
		if a.AccountId != account || a.ReplacesDeviceId == "" || a.CandidateKind != v1.DeviceKind_DEVICE_KIND_MACHINE || devicesec.ID(a.CandidateDevicePublicKey) != cur {
			return nil, errPairApproval
		}
		var first *devicesec.State
		oldMachine := false
		for j := range states {
			s := &states[j]
			if m, ok := s.Core.Member(cur); ok {
				if m.Kind != devicesec.Machine || !bytes.Equal(m.EdPublic[:], a.CandidateDevicePublicKey) {
					return nil, errPairApproval
				}
				first = s
				break
			}
			if m, ok := s.Core.Member(a.ReplacesDeviceId); ok && m.Kind == devicesec.Machine {
				oldMachine = true
			}
		}
		if first == nil || !oldMachine || first.Core.AuthorityID != a.ApproverDeviceId ||
			!slices.Contains(first.Core.Removed, a.ReplacesDeviceId) ||
			uint64(a.ExpiresMs)+devicesec.MaxClockSkew < first.Core.IssuedAt {
			return nil, errPairApproval
		}
		signer, ok := first.Core.Member(a.ApproverDeviceId)
		if !ok || !bytes.Equal(signer.EdPublic[:], a.ApproverPublicKey) {
			return nil, errPairApproval
		}
		if origins[a.ReplacesDeviceId] {
			return nil, errPairApproval
		}
		origins[a.ReplacesDeviceId] = true
		cur = a.ReplacesDeviceId
	}
	return origins, nil
}
