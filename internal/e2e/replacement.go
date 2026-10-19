package e2e

import (
	"bytes"
	"slices"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

// MachineReplacementOrigins authenticates a contiguous archive lineage against
// an already verified chain. Expiry is checked at the candidate's first signed
// membership, not at historical-read time. This never authorizes live control.
func MachineReplacementOrigins(account, target string, states []deviceauth.State, proofs []*pb.MachineReplacementProof) (map[string]bool, error) {
	origins := map[string]bool{}
	if len(proofs) > deviceauth.MaxPairStates {
		return nil, ErrPairApproval
	}
	cursor := target
	for i := len(proofs) - 1; i >= 0; i-- {
		p := proofs[i]
		a, err := VerifyPairApproval(p.Approval, p.Signature)
		if err != nil {
			return nil, err
		}
		if a.AccountId != account || a.ReplacesDeviceId == "" || a.CandidateKind != pb.DeviceKind_DEVICE_KIND_MACHINE || DeviceID(a.CandidateDevicePublicKey) != cursor {
			return nil, ErrPairApproval
		}
		var first *deviceauth.State
		oldMachine := false
		for j := range states {
			s := &states[j]
			if m, ok := s.Core.Member(cursor); ok {
				if m.Kind != deviceauth.Machine || !bytes.Equal(m.EdPublic[:], a.CandidateDevicePublicKey) {
					return nil, ErrPairApproval
				}
				first = s
				break
			}
			if m, ok := s.Core.Member(a.ReplacesDeviceId); ok && m.Kind == deviceauth.Machine {
				oldMachine = true
			}
		}
		if first == nil || !oldMachine || first.Core.AuthorityID != a.ApproverDeviceId || !slices.Contains(first.Core.Removed, a.ReplacesDeviceId) || uint64(a.ExpiresMs)+deviceauth.MaxClockSkew < first.Core.IssuedAt {
			return nil, ErrPairApproval
		}
		signer, ok := first.Core.Member(a.ApproverDeviceId)
		if !ok || !bytes.Equal(signer.EdPublic[:], a.ApproverPublicKey) {
			return nil, ErrPairApproval
		}
		if origins[a.ReplacesDeviceId] {
			return nil, ErrPairApproval
		}
		origins[a.ReplacesDeviceId] = true
		cursor = a.ReplacesDeviceId
	}
	return origins, nil
}
