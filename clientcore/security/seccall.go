package security

import (
	"bytes"
	"maps"
	"slices"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

type kemEntry struct {
	enc [32]byte
	dh  [32]byte
}

type dhHPKESender struct {
	byX map[[32]byte]kemEntry
}

func (d *dhHPKESender) Seal(recipient, info, aad, plaintext []byte) ([]byte, []byte, error) {
	if len(recipient) != 32 {
		return nil, nil, devicesec.ErrInvalid
	}
	var x [32]byte
	copy(x[:], recipient)
	k, ok := d.byX[x]
	if !ok {
		return nil, nil, devicesec.ErrInvalid
	}
	ct, err := devicesec.HPKESealWithDH(recipient, k.enc[:], k.dh[:], info, aad, plaintext)
	if err != nil {
		return nil, nil, err
	}
	return slices.Clone(k.enc[:]), ct, nil
}

func makeKemSender(kems []*corev1.SecKemPair, members ...devicesec.Member) (*dhHPKESender, error) {
	byID := make(map[string]devicesec.Member, len(members))
	for _, m := range members {
		byID[m.ID] = m
	}
	byX := make(map[[32]byte]kemEntry, len(kems))
	for i, kp := range kems {
		if kp == nil || len(kp.Enc) != 32 || len(kp.Dh) != 32 {
			return nil, devicesec.ErrInvalid
		}
		var entry kemEntry
		copy(entry.enc[:], kp.Enc)
		copy(entry.dh[:], kp.Dh)
		var m devicesec.Member
		var ok bool
		if kp.MemberId != "" {
			m, ok = byID[kp.MemberId]
		} else if i < len(members) {
			m = members[i]
			ok = true
		}
		if !ok {
			return nil, devicesec.ErrInvalid
		}
		byX[m.XPublic] = entry
	}
	return &dhHPKESender{byX: byX}, nil
}

func errSecResp(err error) *corev1.SecCallResponse {
	if err == nil {
		err = devicesec.ErrInvalid
	}
	return &corev1.SecCallResponse{
		Error:     err.Error(),
		ErrorCode: ErrorCode(err),
	}
}

func parseHash32(b []byte) (devicesec.Hash, error) {
	var h devicesec.Hash
	if len(b) != 32 {
		return h, devicesec.ErrInvalid
	}
	copy(h[:], b)
	return h, nil
}

func parseEdPublic32(b []byte) ([32]byte, error) {
	var k [32]byte
	if len(b) != devicesec.Ed25519PublicKeySize {
		return k, devicesec.ErrInvalid
	}
	copy(k[:], b)
	return k, nil
}

func parsePin(p *corev1.SecPin) (devicesec.Pin, error) {
	if p == nil || len(p.Domain) != 32 || len(p.Genesis) != 32 {
		return devicesec.Pin{}, devicesec.ErrInvalid
	}
	dom, err := parseHash32(p.Domain)
	if err != nil {
		return devicesec.Pin{}, err
	}
	gen, err := parseHash32(p.Genesis)
	if err != nil {
		return devicesec.Pin{}, err
	}
	return devicesec.Pin{
		Account: p.Account,
		Domain:  dom,
		Genesis: gen,
	}, nil
}

func pinToProto(p devicesec.Pin) *corev1.SecPin {
	return &corev1.SecPin{
		Account: p.Account,
		Domain:  slices.Clone(p.Domain[:]),
		Genesis: slices.Clone(p.Genesis[:]),
	}
}

func memberFromProto(m *corev1.SecMember) (devicesec.Member, error) {
	if m == nil {
		return devicesec.Member{}, devicesec.ErrInvalid
	}
	mem, err := devicesec.NewMember(m.EdPublic, m.XPublic, devicesec.Kind(m.Kind))
	if err != nil {
		return devicesec.Member{}, err
	}
	if m.Id != "" && m.Id != mem.ID {
		return devicesec.Member{}, devicesec.ErrInvalid
	}
	return mem, nil
}

func membersFromProto(list []*corev1.SecMember) ([]devicesec.Member, error) {
	out := make([]devicesec.Member, len(list))
	for i, m := range list {
		mem, err := memberFromProto(m)
		if err != nil {
			return nil, err
		}
		out[i] = mem
	}
	return out, nil
}

func memberToProto(m devicesec.Member) *corev1.SecMember {
	return &corev1.SecMember{
		Id:       m.ID,
		EdPublic: slices.Clone(m.EdPublic[:]),
		XPublic:  slices.Clone(m.XPublic[:]),
		Kind:     uint32(m.Kind),
	}
}

func grantsToProto(grants []devicesec.Grant) []*corev1.SecGrant {
	out := make([]*corev1.SecGrant, len(grants))
	for i, g := range grants {
		out[i] = &corev1.SecGrant{
			Recipient: g.Recipient,
			Enc:       slices.Clone(g.Enc[:]),
			Cipher:    slices.Clone(g.Cipher),
		}
	}
	return out
}

func grantsFromProto(grants []*corev1.SecGrant) ([]devicesec.Grant, error) {
	out := make([]devicesec.Grant, len(grants))
	for i, g := range grants {
		if g == nil || len(g.Enc) != 32 {
			return nil, devicesec.ErrInvalid
		}
		var enc [32]byte
		copy(enc[:], g.Enc)
		out[i] = devicesec.Grant{
			Recipient: g.Recipient,
			Enc:       enc,
			Cipher:    slices.Clone(g.Cipher),
		}
	}
	return out, nil
}

func stateToProto(s devicesec.State) *corev1.SecParsedState {
	sh, _ := s.Hash()
	ch, _ := s.Core.Hash()
	sb, _ := s.SigningBytes()
	members := make([]*corev1.SecMember, len(s.Core.Members))
	for i, m := range s.Core.Members {
		members[i] = memberToProto(m)
	}
	return &corev1.SecParsedState{
		StateHash:    slices.Clone(sh[:]),
		CoreHash:     slices.Clone(ch[:]),
		Account:      s.Core.Account,
		Domain:       slices.Clone(s.Core.Domain[:]),
		AuthorityId:  s.Core.AuthorityID,
		RecoveryEd:   slices.Clone(s.Core.RecoveryEd[:]),
		Parent:       slices.Clone(s.Core.Parent[:]),
		Epoch:        s.Core.Epoch,
		ContentEpoch: s.Core.ContentEpoch,
		IssuedAt:     s.Core.IssuedAt,
		Members:      members,
		Removed:      slices.Clone(s.Core.Removed),
		Grants:       grantsToProto(s.Grants),
		PrevSecret:   slices.Clone(s.PrevSecret),
		Signature:    slices.Clone(s.Signature),
		SigningBytes: sb,
	}
}

func unsignedStateResp(s devicesec.State) *corev1.SecCallResponse {
	uc, err := s.Core.MarshalBinary()
	if err != nil {
		return errSecResp(err)
	}
	sb, err := s.SigningBytes()
	if err != nil {
		return errSecResp(err)
	}
	sh, err := s.Hash()
	if err != nil {
		return errSecResp(err)
	}
	return &corev1.SecCallResponse{
		Data: sb,
		UnsignedState: &corev1.SecUnsignedState{
			Parsed:       stateToProto(s),
			UnsignedCore: uc,
			Grants:       grantsToProto(s.Grants),
			PrevSecret:   slices.Clone(s.PrevSecret),
			SigningBytes: sb,
			StateHash:    slices.Clone(sh[:]),
		},
	}
}

func leaseToProto(l devicesec.LeaseCertificate) *corev1.SecParsedLease {
	sb, _ := l.SigningBytes()
	return &corev1.SecParsedLease{
		Domain:       slices.Clone(l.Domain[:]),
		Head:         slices.Clone(l.Head[:]),
		IssuedAt:     l.IssuedAt,
		NotAfter:     l.NotAfter,
		Signature:    slices.Clone(l.Signature),
		SigningBytes: sb,
	}
}

func recoveryToProto(r devicesec.RecoveryStatement) *corev1.SecParsedRecoveryStatement {
	sb, _ := r.SigningBytes()
	return &corev1.SecParsedRecoveryStatement{
		Account:        r.Account,
		Domain:         slices.Clone(r.Domain[:]),
		Parent:         slices.Clone(r.Parent[:]),
		NewAuthorityId: r.NewAuthorityID,
		NewRecoveryEd:  slices.Clone(r.NewRecoveryEd[:]),
		NewMember:      memberToProto(r.NewMember),
		IssuedAt:       r.IssuedAt,
		StateHash:      slices.Clone(r.StateHash[:]),
		Nonce:          slices.Clone(r.Nonce[:]),
		Signature:      slices.Clone(r.Signature),
		SigningBytes:   sb,
	}
}

func deriveEpochSecretsFromHead(states []devicesec.State, headSecret []byte) (map[uint64][32]byte, error) {
	if len(states) == 0 || len(headSecret) != 32 {
		return nil, devicesec.ErrInvalid
	}
	out := make(map[uint64][32]byte)
	var headArr [32]byte
	copy(headArr[:], headSecret)
	out[states[len(states)-1].Core.ContentEpoch] = headArr
	for i := len(states) - 1; i >= 1; i-- {
		s := states[i]
		prev := states[i-1]
		curEp := s.Core.ContentEpoch
		prevEp := prev.Core.ContentEpoch
		if prevEp == curEp || len(s.PrevSecret) == 0 {
			continue
		}
		curSec, ok := out[curEp]
		if !ok {
			continue
		}
		oldSec, err := devicesec.OpenPrevSecret(curSec[:], s.PrevSecret)
		if err != nil || len(oldSec) != 32 {
			break
		}
		var arr [32]byte
		copy(arr[:], oldSec)
		out[prevEp] = arr
	}
	return out, nil
}

func isAllZero(b []byte) bool {
	var acc byte
	for _, v := range b {
		acc |= v
	}
	return acc == 0
}

// SecCall executes stateless device security operations shared by S6 shell
// workflows, push/service-worker notification decryption, and tests.
func SecCall(req *corev1.SecCallRequest) *corev1.SecCallResponse {
	if req == nil || req.Call == nil {
		return errSecResp(devicesec.ErrInvalid)
	}
	switch c := req.Call.(type) {
	case *corev1.SecCallRequest_ParseState:
		s, err := devicesec.ParseState(c.ParseState)
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{
			Data:        slices.Clone(c.ParseState),
			ParsedState: stateToProto(s),
		}

	case *corev1.SecCallRequest_VerifyChain:
		if c.VerifyChain == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		pin, err := parsePin(c.VerifyChain.Pin)
		if err != nil {
			return errSecResp(err)
		}
		if len(c.VerifyChain.States) == 0 {
			return errSecResp(devicesec.ErrInvalid)
		}
		states, _, _, err := parseAndVerifyChain(pin, c.VerifyChain.States, c.VerifyChain.RecoveryProofs)
		if err != nil {
			return errSecResp(err)
		}
		head := states[len(states)-1]
		if c.VerifyChain.MemberId != "" {
			if _, ok := head.Core.Member(c.VerifyChain.MemberId); !ok {
				return errSecResp(ErrNotMember)
			}
		}
		if c.VerifyChain.NowMs > 0 && head.Core.IssuedAt > uint64(c.VerifyChain.NowMs)+devicesec.MaxClockSkew {
			return errSecResp(ErrClockSkewed)
		}
		var parsedLease *corev1.SecParsedLease
		if len(c.VerifyChain.Lease) > 0 {
			lease, err := devicesec.ParseLease(c.VerifyChain.Lease)
			if err != nil {
				return errSecResp(err)
			}
			if err := lease.Verify(head); err != nil {
				return errSecResp(err)
			}
			if c.VerifyChain.CheckLeaseWindow {
				if err := lease.Valid(head, uint64(max(0, c.VerifyChain.NowMs))); err != nil {
					return errSecResp(err)
				}
			}
			parsedLease = leaseToProto(lease)
		} else if c.VerifyChain.CheckLeaseWindow {
			return errSecResp(ErrLeaseExpired)
		}
		return &corev1.SecCallResponse{
			Pin:         pinToProto(pin),
			ParsedState: stateToProto(head),
			ParsedLease: parsedLease,
		}

	case *corev1.SecCallRequest_GrantChallenge:
		if c.GrantChallenge == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		pin, err := parsePin(c.GrantChallenge.Pin)
		if err != nil {
			return errSecResp(err)
		}
		s, err := devicesec.ParseState(c.GrantChallenge.State)
		if err != nil {
			return errSecResp(err)
		}
		enc, _, err := pin.GrantChallenge(s, c.GrantChallenge.MemberId)
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{Data: slices.Clone(enc[:])}

	case *corev1.SecCallRequest_OpenGrantWithDh:
		if c.OpenGrantWithDh == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		pin, err := parsePin(c.OpenGrantWithDh.Pin)
		if err != nil {
			return errSecResp(err)
		}
		s, err := devicesec.ParseState(c.OpenGrantWithDh.State)
		if err != nil {
			return errSecResp(err)
		}
		sec, err := pin.OpenGrantWithDH(s, c.OpenGrantWithDh.MemberId, c.OpenGrantWithDh.Dh)
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{Data: sec}

	case *corev1.SecCallRequest_ParseLease:
		l, err := devicesec.ParseLease(c.ParseLease)
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{
			Data:        slices.Clone(c.ParseLease),
			ParsedLease: leaseToProto(l),
		}

	case *corev1.SecCallRequest_VerifyLease:
		if c.VerifyLease == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		head, err := devicesec.ParseState(c.VerifyLease.HeadState)
		if err != nil {
			return errSecResp(err)
		}
		l, err := devicesec.ParseLease(c.VerifyLease.Lease)
		if err != nil {
			return errSecResp(err)
		}
		if err := l.Verify(head); err != nil {
			return errSecResp(err)
		}
		if c.VerifyLease.CheckWindow {
			if err := l.Valid(head, uint64(max(0, c.VerifyLease.NowMs))); err != nil {
				return errSecResp(err)
			}
		}
		return &corev1.SecCallResponse{ParsedLease: leaseToProto(l)}

	case *corev1.SecCallRequest_NewLeaseUnsigned:
		if c.NewLeaseUnsigned == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		head, err := devicesec.ParseState(c.NewLeaseUnsigned.HeadState)
		if err != nil {
			return errSecResp(err)
		}
		l, err := devicesec.NewLease(head, c.NewLeaseUnsigned.NowMs)
		if err != nil {
			return errSecResp(err)
		}
		sb, err := l.SigningBytes()
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{
			Data:        sb,
			ParsedLease: leaseToProto(l),
		}

	case *corev1.SecCallRequest_MarshalLease:
		if c.MarshalLease == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		dom, err := parseHash32(c.MarshalLease.Domain)
		if err != nil {
			return errSecResp(err)
		}
		head, err := parseHash32(c.MarshalLease.Head)
		if err != nil {
			return errSecResp(err)
		}
		l := devicesec.LeaseCertificate{
			Domain:    dom,
			Head:      head,
			IssuedAt:  c.MarshalLease.IssuedAt,
			NotAfter:  c.MarshalLease.NotAfter,
			Signature: slices.Clone(c.MarshalLease.Signature),
		}
		raw, err := l.MarshalBinary()
		if err != nil {
			return errSecResp(err)
		}
		parsed, err := devicesec.ParseLease(raw)
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{
			Data:        raw,
			ParsedLease: leaseToProto(parsed),
		}

	case *corev1.SecCallRequest_ParseRecoveryStatement:
		rs, err := devicesec.ParseRecoveryStatement(c.ParseRecoveryStatement)
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{
			Data:           slices.Clone(c.ParseRecoveryStatement),
			ParsedRecovery: recoveryToProto(rs),
		}

	case *corev1.SecCallRequest_VerifyRecoveryStatement:
		if c.VerifyRecoveryStatement == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		rs, err := devicesec.ParseRecoveryStatement(c.VerifyRecoveryStatement.Statement)
		if err != nil {
			return errSecResp(err)
		}
		parent, err := devicesec.ParseState(c.VerifyRecoveryStatement.ParentState)
		if err != nil {
			return errSecResp(err)
		}
		next, err := devicesec.ParseState(c.VerifyRecoveryStatement.NextState)
		if err != nil {
			return errSecResp(err)
		}
		if err := rs.Verify(parent, next); err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{ParsedRecovery: recoveryToProto(rs)}

	case *corev1.SecCallRequest_NewRecoveryStatementUnsigned:
		if c.NewRecoveryStatementUnsigned == nil || len(c.NewRecoveryStatementUnsigned.Nonce) != 32 {
			return errSecResp(devicesec.ErrInvalid)
		}
		parent, err := devicesec.ParseState(c.NewRecoveryStatementUnsigned.ParentState)
		if err != nil {
			return errSecResp(err)
		}
		next, err := devicesec.ParseState(c.NewRecoveryStatementUnsigned.NextState)
		if err != nil {
			return errSecResp(err)
		}
		rs, err := devicesec.NewRecoveryStatement(parent, next, bytes.NewReader(c.NewRecoveryStatementUnsigned.Nonce))
		if err != nil {
			return errSecResp(err)
		}
		sb, err := rs.SigningBytes()
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{
			Data:           sb,
			ParsedRecovery: recoveryToProto(rs),
		}

	case *corev1.SecCallRequest_MarshalRecoveryStatement:
		if c.MarshalRecoveryStatement == nil || c.MarshalRecoveryStatement.NewMember == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		dom, err := parseHash32(c.MarshalRecoveryStatement.Domain)
		if err != nil {
			return errSecResp(err)
		}
		parent, err := parseHash32(c.MarshalRecoveryStatement.Parent)
		if err != nil {
			return errSecResp(err)
		}
		newRecEd, err := parseEdPublic32(c.MarshalRecoveryStatement.NewRecoveryEd)
		if err != nil {
			return errSecResp(err)
		}
		newMem, err := memberFromProto(c.MarshalRecoveryStatement.NewMember)
		if err != nil {
			return errSecResp(err)
		}
		sh, err := parseHash32(c.MarshalRecoveryStatement.StateHash)
		if err != nil {
			return errSecResp(err)
		}
		nonce, err := parseHash32(c.MarshalRecoveryStatement.Nonce)
		if err != nil {
			return errSecResp(err)
		}
		rs := devicesec.RecoveryStatement{
			Account:        c.MarshalRecoveryStatement.Account,
			Domain:         dom,
			Parent:         parent,
			NewAuthorityID: c.MarshalRecoveryStatement.NewAuthorityId,
			NewRecoveryEd:  newRecEd,
			NewMember:      newMem,
			IssuedAt:       c.MarshalRecoveryStatement.IssuedAt,
			StateHash:      sh,
			Nonce:          nonce,
			Signature:      slices.Clone(c.MarshalRecoveryStatement.Signature),
		}
		raw, err := rs.MarshalBinary()
		if err != nil {
			return errSecResp(err)
		}
		parsed, err := devicesec.ParseRecoveryStatement(raw)
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{
			Data:           raw,
			ParsedRecovery: recoveryToProto(parsed),
		}

	case *corev1.SecCallRequest_BuildState:
		return secBuildState(c.BuildState)

	case *corev1.SecCallRequest_MarshalState:
		if c.MarshalState == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		core, err := devicesec.ParseCore(c.MarshalState.UnsignedCore)
		if err != nil {
			return errSecResp(err)
		}
		grants, err := grantsFromProto(c.MarshalState.Grants)
		if err != nil {
			return errSecResp(err)
		}
		s := devicesec.State{
			Core:       core,
			Grants:     grants,
			PrevSecret: slices.Clone(c.MarshalState.PrevSecret),
			Signature:  slices.Clone(c.MarshalState.Signature),
		}
		raw, err := s.MarshalBinary()
		if err != nil {
			return errSecResp(err)
		}
		parsed, err := devicesec.ParseState(raw)
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{
			Data:        raw,
			ParsedState: stateToProto(parsed),
		}

	case *corev1.SecCallRequest_OpenPrevSecret:
		if c.OpenPrevSecret == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		out, err := devicesec.OpenPrevSecret(c.OpenPrevSecret.NewSecret, c.OpenPrevSecret.Blob)
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{Data: out}

	case *corev1.SecCallRequest_VerifyPairApproval:
		if c.VerifyPairApproval == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		a, err := VerifyPairApproval(c.VerifyPairApproval.Approval, c.VerifyPairApproval.Signature)
		if err != nil {
			return errSecResp(err)
		}
		candID := devicesec.ID(a.CandidateDevicePublicKey)
		if c.VerifyPairApproval.ExpectedAccount != "" && a.AccountId != c.VerifyPairApproval.ExpectedAccount {
			return errSecResp(devicesec.ErrUntrusted)
		}
		if c.VerifyPairApproval.ExpectedCandidateId != "" && candID != c.VerifyPairApproval.ExpectedCandidateId {
			return errSecResp(devicesec.ErrUntrusted)
		}
		return &corev1.SecCallResponse{
			ParsedApproval: &corev1.SecParsedApproval{
				AccountId:                a.AccountId,
				PairingId:                a.PairingId,
				ApproverDeviceId:         a.ApproverDeviceId,
				ApproverPublicKey:        slices.Clone(a.ApproverPublicKey),
				CandidateDeviceId:        candID,
				CandidateDevicePublicKey: slices.Clone(a.CandidateDevicePublicKey),
				CandidateKind:            a.CandidateKind,
				ExpiresMs:                a.ExpiresMs,
				Nonce:                    slices.Clone(a.Nonce),
				ReplacesDeviceId:         a.ReplacesDeviceId,
			},
		}

	case *corev1.SecCallRequest_MachineReplacementOrigins:
		if c.MachineReplacementOrigins == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		states := make([]devicesec.State, len(c.MachineReplacementOrigins.States))
		for i, raw := range c.MachineReplacementOrigins.States {
			s, err := devicesec.ParseState(raw)
			if err != nil {
				return errSecResp(err)
			}
			states[i] = s
		}
		origins, err := MachineReplacementOrigins(
			c.MachineReplacementOrigins.Account,
			c.MachineReplacementOrigins.TargetMachineId,
			states,
			c.MachineReplacementOrigins.Proofs,
		)
		if err != nil {
			return errSecResp(err)
		}
		keys := slices.Sorted(maps.Keys(origins))
		return &corev1.SecCallResponse{Strings: keys}

	case *corev1.SecCallRequest_PairTrustDescriptor:
		if c.PairTrustDescriptor == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		pin, err := parsePin(c.PairTrustDescriptor.Pin)
		if err != nil {
			return errSecResp(err)
		}
		raw, err := devicesec.PairTrustDescriptor(pin, c.PairTrustDescriptor.Approver)
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{Data: raw}

	case *corev1.SecCallRequest_ParsePairTrustDescriptor:
		pin, approver, err := devicesec.ParsePairTrustDescriptor(c.ParsePairTrustDescriptor)
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{
			Pin:  pinToProto(pin),
			Text: approver,
		}

	case *corev1.SecCallRequest_PairDomainCommit:
		if c.PairDomainCommit == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		out := devicesec.PairDomainCommit(c.PairDomainCommit.Initiator, c.PairDomainCommit.TrustDescriptor)
		if out == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		return &corev1.SecCallResponse{Data: out}

	case *corev1.SecCallRequest_PairTranscript:
		if c.PairTranscript == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		out := devicesec.PairTranscript(
			c.PairTranscript.PairingId,
			c.PairTranscript.InitiatorPublic,
			c.PairTranscript.JoinerPublic,
			c.PairTranscript.JoinerKind,
		)
		return &corev1.SecCallResponse{Data: out}

	case *corev1.SecCallRequest_PairIdentityTranscript:
		if c.PairIdentityTranscript == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		out := devicesec.PairIdentityTranscript(
			c.PairIdentityTranscript.PairingId,
			c.PairIdentityTranscript.InitiatorPublic,
			c.PairIdentityTranscript.JoinerPublic,
			c.PairIdentityTranscript.DeviceEdPublic,
			c.PairIdentityTranscript.JoinerKind,
		)
		return &corev1.SecCallResponse{Data: out}

	case *corev1.SecCallRequest_PairDomainTranscript:
		if c.PairDomainTranscript == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		out := devicesec.PairDomainTranscript(
			c.PairDomainTranscript.PairingId,
			c.PairDomainTranscript.InitiatorPublic,
			c.PairDomainTranscript.JoinerPublic,
			c.PairDomainTranscript.DeviceEdPublic,
			c.PairDomainTranscript.DeviceXPublic,
			c.PairDomainTranscript.JoinerKind,
			c.PairDomainTranscript.TrustDescriptor,
		)
		return &corev1.SecCallResponse{Data: out}

	case *corev1.SecCallRequest_DerivePairSecrets:
		if c.DerivePairSecrets == nil || len(c.DerivePairSecrets.Shared) != devicesec.PairKeySize || isAllZero(c.DerivePairSecrets.Shared) {
			return errSecResp(devicesec.ErrInvalid)
		}
		ps, err := devicesec.DerivePairSecrets(c.DerivePairSecrets.Shared, c.DerivePairSecrets.Transcript)
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{
			Data:          ps.Key,
			SecondaryData: slices.Clone(ps.SAS[:]),
			Strings:       ps.Emoji(),
		}

	case *corev1.SecCallRequest_Ed25519SeedPublic:
		pub, err := devicesec.Ed25519PublicFromSeed(c.Ed25519SeedPublic)
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{Data: pub}

	case *corev1.SecCallRequest_Ed25519SeedSign:
		if c.Ed25519SeedSign == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		sig, err := devicesec.Ed25519SignSeed(c.Ed25519SeedSign.Seed, c.Ed25519SeedSign.Message)
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{Data: sig}

	case *corev1.SecCallRequest_InflateBounded:
		if c.InflateBounded == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		out, err := devicesec.InflateBounded(c.InflateBounded.Compressed, int(c.InflateBounded.Limit))
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{Data: out}

	case *corev1.SecCallRequest_OpenNotifyEnvelope:
		return secOpenNotifyEnvelope(c.OpenNotifyEnvelope)

	case *corev1.SecCallRequest_DerivePurposeKey:
		if c.DerivePurposeKey == nil {
			return errSecResp(devicesec.ErrInvalid)
		}
		dom, err := parseHash32(c.DerivePurposeKey.Domain)
		if err != nil {
			return errSecResp(err)
		}
		out, err := devicesec.DerivePurposeKeyRaw(
			c.DerivePurposeKey.Secret,
			c.DerivePurposeKey.AccountId,
			dom,
			c.DerivePurposeKey.ContentEpoch,
			devicesec.Purpose(c.DerivePurposeKey.Purpose),
		)
		if err != nil {
			return errSecResp(err)
		}
		return &corev1.SecCallResponse{Data: out}

	default:
		return errSecResp(devicesec.ErrInvalid)
	}
}

func secBuildState(c *corev1.SecBuildStateCall) *corev1.SecCallResponse {
	if c == nil {
		return errSecResp(devicesec.ErrInvalid)
	}
	var (
		s   devicesec.State
		err error
	)
	switch c.Kind {
	case 1: // Genesis
		var dom devicesec.Hash
		var mem devicesec.Member
		var recEd [32]byte
		dom, err = parseHash32(c.Domain)
		if err == nil {
			mem, err = memberFromProto(c.Member)
		}
		if err == nil {
			recEd, err = parseEdPublic32(c.RecoveryEd)
		}
		var sender *dhHPKESender
		if err == nil {
			sender, err = makeKemSender(c.Kems, mem)
		}
		if err == nil {
			s, err = devicesec.NewGenesis(c.Account, dom, mem, recEd, c.Secret, c.NowMs, sender)
		}
	case 2, 7: // Membership / CustomState
		var parent devicesec.State
		var members []devicesec.Member
		var sender *dhHPKESender
		parent, err = devicesec.ParseState(c.ParentState)
		if err == nil {
			members, err = membersFromProto(c.Members)
		}
		if err == nil {
			sender, err = makeKemSender(c.Kems, members...)
		}
		if err == nil {
			if c.Kind == 2 {
				if len(c.PrevSecretNonce) != 12 {
					return errSecResp(devicesec.ErrInvalid)
				}
				s, err = devicesec.NewMembershipState(parent, members, c.Removed, c.OldSecret, c.Secret, c.NowMs, sender, bytes.NewReader(c.PrevSecretNonce))
			} else {
				s, err = devicesec.NewState(parent, members, c.Removed, c.Secret, c.PrevSecret, c.NowMs, sender)
			}
		}
	case 3: // Pair
		if len(c.PrevSecretNonce) != 12 {
			return errSecResp(devicesec.ErrInvalid)
		}
		var parent devicesec.State
		var cand devicesec.Member
		var sender *dhHPKESender
		parent, err = devicesec.ParseState(c.ParentState)
		if err == nil {
			cand, err = memberFromProto(c.Member)
		}
		if err == nil {
			allMembers := append(slices.Clone(parent.Core.Members), cand)
			sender, err = makeKemSender(c.Kems, allMembers...)
		}
		if err == nil {
			s, err = devicesec.NewPairState(parent, cand, cand.XPublic[:], c.OldSecret, c.Secret, c.NowMs, sender, bytes.NewReader(c.PrevSecretNonce))
		}
	case 4: // Handoff
		if len(c.PrevSecretNonce) != 12 {
			return errSecResp(devicesec.ErrInvalid)
		}
		var parent devicesec.State
		var recEd [32]byte
		var sender *dhHPKESender
		parent, err = devicesec.ParseState(c.ParentState)
		if err == nil {
			recEd, err = parseEdPublic32(c.RecoveryEd)
		}
		if err == nil {
			sender, err = makeKemSender(c.Kems, parent.Core.Members...)
		}
		if err == nil {
			s, err = devicesec.NewHandoff(parent, c.IncomingId, recEd, c.OldSecret, c.Secret, c.NowMs, sender, bytes.NewReader(c.PrevSecretNonce))
		}
	case 5: // Recovery
		var parent devicesec.State
		var newMem devicesec.Member
		var members []devicesec.Member
		var recEd [32]byte
		var sender *dhHPKESender
		parent, err = devicesec.ParseState(c.ParentState)
		if err == nil {
			newMem, err = memberFromProto(c.Member)
		}
		if err == nil {
			members, err = membersFromProto(c.Members)
		}
		if err == nil {
			recEd, err = parseEdPublic32(c.RecoveryEd)
		}
		if err == nil {
			allMembers := append(slices.Clone(members), newMem)
			sender, err = makeKemSender(c.Kems, allMembers...)
		}
		if err == nil {
			s, err = devicesec.NewRecovery(parent, newMem, members, c.Secret, recEd, c.NowMs, sender)
		}
	case 6: // RecoveryKeyRotation
		var parent devicesec.State
		var recEd [32]byte
		var sender *dhHPKESender
		parent, err = devicesec.ParseState(c.ParentState)
		if err == nil {
			recEd, err = parseEdPublic32(c.RecoveryEd)
		}
		if err == nil {
			sender, err = makeKemSender(c.Kems, parent.Core.Members...)
		}
		if err == nil {
			s, err = devicesec.NewRecoveryKeyRotation(parent, c.Secret, recEd, c.NowMs, sender)
		}
	default:
		return errSecResp(devicesec.ErrInvalid)
	}
	if err != nil {
		return errSecResp(err)
	}
	return unsignedStateResp(s)
}

func secOpenNotifyEnvelope(c *corev1.SecOpenNotifyEnvelopeCall) *corev1.SecCallResponse {
	if c == nil || c.Envelope == nil {
		return errSecResp(devicesec.ErrInvalid)
	}
	pin, err := parsePin(c.Pin)
	if err != nil {
		return errSecResp(err)
	}
	if len(c.States) == 0 {
		return errSecResp(devicesec.ErrInvalid)
	}
	if len(c.PurposeKey) != 0 {
		if len(c.States) != 1 || len(c.PurposeKey) != 32 || len(c.ContentSecret) != 0 || len(c.Dh) != 0 {
			return errSecResp(devicesec.ErrInvalid)
		}
		head, err := devicesec.ParseState(c.States[0])
		if err != nil {
			return errSecResp(err)
		}
		if err := pin.Verify(head); err != nil {
			return errSecResp(err)
		}
		ct, err := FromProtoEnvelope(c.Envelope)
		if err != nil {
			return errSecResp(err)
		}
		h, err := devicesec.ParseHeader(ct.Header)
		if err != nil {
			return errSecResp(err)
		}
		if h.Purpose != devicesec.Notify {
			return errSecResp(devicesec.ErrInvalid)
		}
		headHash, _ := head.Hash()
		if h.State != headHash {
			return errSecResp(devicesec.ErrStale)
		}
		vh, err := ct.VerifyOrigin(head, head, false, 0)
		if err != nil {
			return errSecResp(err)
		}
		_, plain, err := devicesec.OpenCipherWithPurposeKey(c.PurposeKey, nil, ct)
		if err != nil {
			return errSecResp(err)
		}
		body, unpacked, err := ValidateAndDecodeBody(vh, plain)
		if err != nil {
			return errSecResp(err)
		}
		if _, ok := body.(*v1.NotifyContent); !ok {
			return errSecResp(devicesec.ErrInvalid)
		}
		return &corev1.SecCallResponse{
			Data: unpacked,
		}
	}
	states, byHash, _, err := parseAndVerifyChain(pin, c.States, c.RecoveryProofs)
	if err != nil {
		return errSecResp(err)
	}
	head := states[len(states)-1]
	ct, err := FromProtoEnvelope(c.Envelope)
	if err != nil {
		return errSecResp(err)
	}
	h, err := devicesec.ParseHeader(ct.Header)
	if err != nil {
		return errSecResp(err)
	}
	if h.Purpose != devicesec.Notify {
		return errSecResp(devicesec.ErrInvalid)
	}
	s, ok := byHash[h.State]
	if !ok {
		return errSecResp(devicesec.ErrStale)
	}
	vh, err := ct.VerifyOrigin(s, head, false, 0)
	if err != nil {
		return errSecResp(err)
	}
	headSec := slices.Clone(c.ContentSecret)
	if len(headSec) == 0 {
		if c.MemberId == "" || len(c.Dh) == 0 {
			return errSecResp(devicesec.ErrInvalid)
		}
		headSec, err = pin.OpenGrantWithDH(head, c.MemberId, c.Dh)
		if err != nil {
			return errSecResp(err)
		}
	}
	epochs, err := deriveEpochSecretsFromHead(states, headSec)
	if err != nil {
		return errSecResp(err)
	}
	secArr, hasSec := epochs[vh.ContentEpoch]
	var sec []byte
	if hasSec {
		sec = secArr[:]
	} else if len(c.ContentSecret) == 32 && len(c.Dh) == 0 {
		// Caller explicitly supplied content_secret for the target epoch.
		sec = c.ContentSecret
	} else {
		return errSecResp(devicesec.ErrStale)
	}
	_, plain, err := devicesec.OpenCipher(sec, nil, ct)
	if err != nil {
		return errSecResp(err)
	}
	body, unpacked, err := ValidateAndDecodeBody(vh, plain)
	if err != nil {
		return errSecResp(err)
	}
	if _, ok := body.(*v1.NotifyContent); !ok {
		return errSecResp(devicesec.ErrInvalid)
	}
	return &corev1.SecCallResponse{
		Data:          unpacked,
		SecondaryData: headSec,
	}
}
