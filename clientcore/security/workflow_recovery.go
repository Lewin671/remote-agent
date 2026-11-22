package security

import (
	"bytes"
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

func buildRecoveryContextSignBytes(challenge, recoveryEd []byte, recoveryID string) []byte {
	var ed [32]byte
	copy(ed[:], recoveryEd)
	msg := make([]byte, 0, len(recoveryCtxPrefix)+len(challenge)+32+len(recoveryID))
	msg = append(msg, recoveryCtxPrefix...)
	msg = append(msg, challenge...)
	msg = append(msg, ed[:]...)
	msg = append(msg, recoveryID...)
	return msg
}

type fetchRecoveryCtxTask struct {
	taskBase
	server, recoveryID, candEdSlot string
	recPub, challenge              []byte
	genPrefix                      [10]byte
	existingRec                    *PersistedRecord
	useSeed, verifyChain           bool
	seed                           [32]byte
}

func (e *Engine) startFetchRecoveryCtxWithSeed(
	server string, recPub []byte, genPrefix [10]byte, existingRec *PersistedRecord, fgGen uint64, seed [32]byte, target taskTarget,
) {
	t := &fetchRecoveryCtxTask{
		taskBase: taskBase{fgGen: fgGen, target: target}, server: server, recPub: recPub,
		genPrefix: genPrefix, existingRec: existingRec, useSeed: true, seed: seed, verifyChain: true,
	}
	zeroBytes(seed[:])
	e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_RECOVERY_CHALLENGE, server, "", &v1.GetRecoveryChallengeRequest{}, t)
}

func (e *Engine) startFetchRecoveryCtxWithCandSlot(server, recoveryID, candEdSlot string, fgGen uint64, target taskTarget) {
	e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_RECOVERY_CHALLENGE, server, "", &v1.GetRecoveryChallengeRequest{}, &fetchRecoveryCtxTask{
		taskBase: taskBase{fgGen: fgGen, target: target}, server: server, recoveryID: recoveryID, candEdSlot: candEdSlot,
	})
}

func (t *fetchRecoveryCtxTask) onError(e *Engine, err error) {
	zeroBytes(t.seed[:])
	e.failTarget(t.target, err)
}

func (t *fetchRecoveryCtxTask) sendContextRPC(e *Engine, sig []byte) {
	req := &v1.GetRecoveryContextRequest{RecoveryId: t.recoveryID, Challenge: t.challenge, Signature: sig}
	if len(t.recPub) > 0 {
		req.RecoveryEd = slices.Clone(t.recPub)
	}
	t.stage = 2
	e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_RECOVERY_CONTEXT, t.server, "", req, t)
}

func (t *fetchRecoveryCtxTask) fgCheckMask() uint32 { return 1 << 0 }

func (t *fetchRecoveryCtxTask) step(e *Engine, ev *secEvent) error {
	switch t.stage {
	case 0: // challenge
		var cr v1.GetRecoveryChallengeResponse
		if cr.Unmarshal(ev.bytes) != nil || len(cr.Challenge) == 0 {
			return devicesec.ErrInvalid
		}
		t.challenge = cr.Challenge
		signMsg := buildRecoveryContextSignBytes(cr.Challenge, t.recPub, t.recoveryID)
		if t.useSeed {
			sig, err := devicesec.Ed25519SignSeed(t.seed[:], signMsg)
			zeroBytes(t.seed[:])
			if err != nil {
				return err
			}
			t.sendContextRPC(e, sig)
			return nil
		}
		t.stage = 1
		e.waitSign(keyCandEd, t.candEdSlot, signMsg, t, 0)
	case 1: // sign
		t.sendContextRPC(e, ev.bytes)
	case 2: // context
		var resp v1.GetRecoveryContextResponse
		if resp.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		if !t.verifyChain {
			zeroBytes(t.seed[:])
			e.completeTarget(t.target, &secEvent{recCtx: &recoveryContextResult{resp: &resp}})
			return nil
		}
		if len(resp.AccountPublicKey) != 32 || devicesec.ID(resp.AccountPublicKey) != resp.AccountId {
			return devicesec.ErrUntrusted
		}
		rawStates, rawProofs, err := unpackProtoStates(resp.States)
		if err != nil {
			return err
		}
		if len(rawStates) == 0 {
			return devicesec.ErrInvalid
		}
		genesis, err := devicesec.ParseState(rawStates[0])
		if err != nil {
			return err
		}
		genHash, _ := genesis.Hash()
		if t.genPrefix != ([10]byte{}) && !bytes.Equal(genHash[:10], t.genPrefix[:]) {
			return devicesec.ErrUntrusted
		}
		pin, err := genesis.Pin()
		if err != nil || pin.Account != resp.AccountId {
			return devicesec.ErrUntrusted
		}
		states, _, _, err := parseAndVerifyChain(pin, rawStates, rawProofs)
		if err != nil {
			return err
		}
		if t.existingRec != nil {
			if t.existingRec.Pin != pin || len(rawStates) < len(t.existingRec.States) {
				return devicesec.ErrUntrusted
			}
			for i, s := range t.existingRec.States {
				if !bytes.Equal(rawStates[i], s) {
					return devicesec.ErrUntrusted
				}
			}
		}
		head := states[len(states)-1]
		if len(t.recPub) == 32 && resp.RecoveryId == "" && !bytes.Equal(head.Core.RecoveryEd[:], t.recPub) {
			return devicesec.ErrUntrusted
		}
		var acctPub32 [32]byte
		copy(acctPub32[:], resp.AccountPublicKey)
		zeroBytes(t.seed[:])
		e.completeTarget(t.target, &secEvent{recCtx: &recoveryContextResult{
			resp: &resp, pin: pin, acctPub: acctPub32, rawStates: rawStates, rawProofs: rawProofs,
		}})
	}
	return nil
}

type prepareEmergencyRecoveryTask struct {
	taskBase
	rawServer, code, server, retiring, edSlot, xSlot, newCode string
	resp                                                      *v1.GetRecoveryContextResponse
	pin                                                       devicesec.Pin
	acctPub                                                   [32]byte
	rawStates, rawProofs                                      [][]byte
	head                                                      devicesec.State
	candEdPub                                                 []byte
	cleanEd, cleanX                                           bool
}

func (t *prepareEmergencyRecoveryTask) onError(e *Engine, err error) {
	ed, x := "", ""
	if t.cleanEd {
		ed = t.edSlot
	}
	if t.cleanX {
		x = t.xSlot
	}
	e.discardCandidatePair(ed, x)
	e.failTarget(t.target, err)
}

func (t *prepareEmergencyRecoveryTask) fgCheckMask() uint32 { return fgOnStart | 1<<0 | 1<<1 }

func (t *prepareEmergencyRecoveryTask) step(e *Engine, ev *secEvent) error {
	wf := e.ensureWF()
	switch t.stage {
	case 0: // context
		if ev.kind == evStart {
			server, err := CheckServerURL(t.rawServer)
			if err != nil {
				return err
			}
			if wf.pendingOp != nil || wf.regIntent != nil || wf.bootstrapInvalid {
				return newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst)
			}
			oldSeed, genPrefix, ok := DecodeRecoveryCode(t.code)
			if !ok {
				return newWfError("invalid", errInvalidRecoveryCode.Error())
			}
			oldRecPub, err := devicesec.Ed25519PublicFromSeed(oldSeed[:])
			if err != nil {
				zeroBytes(oldSeed[:])
				return err
			}
			t.server = server
			e.startFetchRecoveryCtxWithSeed(server, oldRecPub, genPrefix, wf.record, t.fgGen, oldSeed, parentTarget(t, 0))
			zeroBytes(oldSeed[:])
			return nil
		}
		rc := ev.recCtx
		head, err := devicesec.ParseState(rc.rawStates[len(rc.rawStates)-1])
		if err != nil {
			return err
		}
		var retiring string
		if wf.record != nil {
			if wf.record.Pin != rc.pin || (wf.record.Server != "" && wf.record.Server != t.server) {
				return devicesec.ErrUntrusted
			}
			if _, ok := head.Core.Member(wf.record.MemberID); ok && head.Core.AuthorityID != wf.record.MemberID {
				retiring = wf.record.MemberID
			}
		}
		t.resp, t.pin, t.acctPub, t.rawStates, t.rawProofs, t.head, t.retiring = rc.resp, rc.pin, rc.acctPub, rc.rawStates, rc.rawProofs, head, retiring
		t.stage = 1
		e.waitEntropy(128, t, 0)
	case 1: // entropy
		tok := e.nextWorkflowToken()
		t.edSlot, t.xSlot, t.stage = "rec-ed-"+tok, "rec-x-"+tok, 2
		e.waitKeyGen(keyCandEd, t.edSlot, true, t, 0)
	case 2: // keygen ed
		t.candEdPub, t.cleanEd, t.stage = ev.bytes, true, 3
		e.waitKeyGen(keyCandX, t.xSlot, true, t, 0)
	case 3: // keygen x
		t.cleanX = true
		var newRecSeed, newSecret [32]byte
		if !e.readRandom(newRecSeed[:]) || !e.readRandom(newSecret[:]) {
			zeroBytes(newRecSeed[:])
			zeroBytes(newSecret[:])
			return ErrNeedEntropy
		}
		newRecPub, err := devicesec.Ed25519PublicFromSeed(newRecSeed[:])
		if err != nil {
			zeroBytes(newRecSeed[:])
			zeroBytes(newSecret[:])
			return err
		}
		t.newCode = EncodeRecoveryCode(newRecSeed, t.pin.Genesis)
		zeroBytes(newRecSeed[:])
		headHash, _ := t.head.Hash()
		candID := devicesec.ID(t.candEdPub)
		now := max(uint64(max(1, e.lastNowMs)), t.head.Core.IssuedAt)
		op := e.newPreparedOp(t.server, candID, "recovery", "", headHash)
		op.Recovery = &PersistedRecoveryDraft{
			AccountID: t.pin.Account, AccountPublicKey: slices.Clone(t.acctPub[:]), Domain: t.pin.Domain, Genesis: t.pin.Genesis,
			EdSlot: t.edSlot, XSlot: t.xSlot, CandidateEdPub: slices.Clone(t.candEdPub), CandidateXPub: slices.Clone(ev.bytes),
			RecoveryEd: slices.Clone(newRecPub), Secret: slices.Clone(newSecret[:]), NowMs: int64(now), RetiringMemberID: t.retiring,
			RecoveryID: t.resp.RecoveryId, ReadyMs: int64(t.resp.ReadyMs), States: t.rawStates, RecoveryProofs: t.rawProofs,
		}
		zeroBytes(newSecret[:])
		t.stage = 4
		e.claimOperationCAS(op, nil, candID, parentTarget(t, 0))
	case 4: // claim
		wf.server = t.server
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{
			RecoveryCode: t.newCode,
			Recovery:     &corev1.SecurityRecoveryInfo{RecoveryId: t.resp.RecoveryId, Phase: t.resp.Phase, ReadyMs: int64(t.resp.ReadyMs)},
		})
	}
	return nil
}

type continueRecoverySigningTask struct {
	taskBase
	op         *PersistedOperation
	oldCode    string
	oldSeed    [32]byte
	parent     devicesec.State
	newMember  devicesec.Member
	members    []devicesec.Member
	newRecEd   [32]byte
	now        uint64
	recoveryID string
	readyMs    int64
}

func (t *continueRecoverySigningTask) onError(e *Engine, err error) {
	zeroBytes(t.oldSeed[:])
	e.failTarget(t.target, err)
}

func (t *continueRecoverySigningTask) submitSigned(e *Engine, curOp *PersistedOperation) {
	t.op, t.stage = curOp, 4
	e.waitRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_SUBMIT_RECOVERY, curOp.Server, "", curOp.Signed, t, 0)
}

func (t *continueRecoverySigningTask) fgCheckMask() uint32 { return fgOnStart | 1<<0 | 1<<1 }

func (t *continueRecoverySigningTask) step(e *Engine, ev *secEvent) error {
	wf := e.ensureWF()
	switch t.stage {
	case 0:
		op := wf.pendingOp
		if op == nil || op.Kind != "recovery" || op.Recovery == nil {
			return devicesec.ErrStale
		}
		if len(op.Signed) > 0 {
			t.submitSigned(e, op)
			return nil
		}
		draft := op.Recovery
		oldSeed, genPrefix, ok := DecodeRecoveryCode(t.oldCode)
		if !ok || !bytes.Equal(draft.Genesis[:10], genPrefix[:]) {
			zeroBytes(oldSeed[:])
			return newWfError("invalid", errInvalidRecoveryCode.Error())
		}
		t.oldSeed = oldSeed
		zeroBytes(oldSeed[:])
		oldRecPub, err := devicesec.Ed25519PublicFromSeed(t.oldSeed[:])
		if err != nil {
			return err
		}
		if len(draft.States) == 0 || len(draft.RecoveryEd) != 32 || len(draft.Secret) != 32 {
			return devicesec.ErrInvalid
		}
		pin := devicesec.Pin{Account: draft.AccountID, Domain: draft.Domain, Genesis: draft.Genesis}
		states, _, _, err := parseAndVerifyChain(pin, draft.States, draft.RecoveryProofs)
		if err != nil || len(states) == 0 {
			return err
		}
		parent := states[len(states)-1]
		if !bytes.Equal(parent.Core.RecoveryEd[:], oldRecPub) {
			return newWfError("invalid", errInvalidRecoveryCode.Error())
		}
		newMember, err := devicesec.NewMember(draft.CandidateEdPub, draft.CandidateXPub, devicesec.Client)
		if err != nil {
			return err
		}
		members := make([]devicesec.Member, 0, len(parent.Core.Members)+1)
		var replaced bool
		for _, m := range parent.Core.Members {
			if m.ID == parent.Core.AuthorityID {
				continue
			}
			if m.ID == newMember.ID {
				members, replaced = append(members, newMember), true
			} else {
				members = append(members, m)
			}
		}
		if !replaced {
			members = append(members, newMember)
		}
		copy(t.newRecEd[:], draft.RecoveryEd)
		t.op, t.parent, t.newMember, t.members, t.now, t.stage = op, parent, newMember, members, max(max(uint64(max(1, draft.NowMs)), uint64(max(1, e.lastNowMs))), parent.Core.IssuedAt), 1
		e.waitEntropy(64, t, 0)
	case 1:
		draft := t.op.Recovery
		var secretCopy [32]byte
		copy(secretCopy[:], draft.Secret)
		params := kemBuildParams{kind: kemBuildRecovery, parent: t.parent, member: t.newMember, members: t.members, newSecret: secretCopy, recoveryEd: t.newRecEd, now: t.now}
		zeroBytes(secretCopy[:])
		t.stage = 2
		e.buildAndSignStateWithKEM(keyCandEd, draft.EdSlot, true, t.fgGen, params, t.members, parentTarget(t, 0))
	case 2:
		target := *ev.state
		stmt, err := devicesec.NewRecoveryStatement(t.parent, target, e.entropy)
		if err != nil {
			return err
		}
		stmtSB, err := stmt.SigningBytes()
		if err != nil {
			return err
		}
		stmtSig, err := devicesec.Ed25519SignSeed(t.oldSeed[:], stmtSB)
		zeroBytes(t.oldSeed[:])
		if err != nil {
			return err
		}
		stmt.Signature = stmtSig
		if err := stmt.Verify(t.parent, target); err != nil {
			return err
		}
		targetBytes, err := target.MarshalBinary()
		if err != nil {
			return err
		}
		proofBytes, err := stmt.MarshalBinary()
		if err != nil {
			return err
		}
		reqBytes, err := (&v1.SubmitRecoveryRequest{Statement: proofBytes, State: targetBytes}).Marshal()
		if err != nil {
			return err
		}
		nextDraft := *t.op.Recovery
		nextDraft.TargetState, nextDraft.Proof = targetBytes, proofBytes
		nextOp := *t.op
		nextOp.Signed, nextOp.Recovery, t.stage = reqBytes, &nextDraft, 3
		e.saveOperationCAS(t.op, &nextOp, parentTarget(t, 0))
	case 3:
		t.submitSigned(e, ev.op)
	case 4:
		var sr v1.SubmitRecoveryResponse
		if sr.Unmarshal(ev.bytes) != nil || sr.RecoveryId == "" {
			return devicesec.ErrInvalid
		}
		nextDraft := *t.op.Recovery
		nextDraft.RecoveryID, nextDraft.ReadyMs = sr.RecoveryId, int64(sr.ReadyMs)
		nextOp := *t.op
		nextOp.Phase, nextOp.Recovery, t.recoveryID, t.readyMs, t.stage = "PUBLISHED", &nextDraft, sr.RecoveryId, int64(sr.ReadyMs), 5
		e.saveOperationCAS(t.op, &nextOp, parentTarget(t, 0))
	case 5:
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{
			Recovery: &corev1.SecurityRecoveryInfo{RecoveryId: t.recoveryID, Phase: v1.GetRecoveryContextResponse_PHASE_PENDING, ReadyMs: t.readyMs},
		})
	}
	return nil
}

type resumeRecoveryTask struct {
	taskBase
	op    *PersistedOperation
	phase v1.GetRecoveryContextResponse_Phase
}

func (t *resumeRecoveryTask) fetchContext(e *Engine, op *PersistedOperation) {
	t.op, t.stage = op, 3
	e.startFetchRecoveryCtxWithCandSlot(op.Server, op.Recovery.RecoveryID, op.Recovery.EdSlot, t.fgGen, parentTarget(t, 0))
}

func (t *resumeRecoveryTask) fgCheckMask() uint32 { return fgOnStart | 1<<0 }

func (t *resumeRecoveryTask) step(e *Engine, ev *secEvent) error {
	wf := e.ensureWF()
	switch t.stage {
	case 0:
		op := wf.pendingOp
		if op == nil || op.Kind != "recovery" || op.Recovery == nil {
			e.okTarget(t.target, nil)
			return nil
		}
		if op.Recovery.RecoveryID == "" {
			if len(op.Signed) > 0 {
				t.op, t.stage = op, 1
				e.waitRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_SUBMIT_RECOVERY, op.Server, "", op.Signed, t, 0)
				return nil
			}
			e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{ResumedDraft: true, CodeSaved: op.CodeSaved})
			return nil
		}
		t.fetchContext(e, op)
	case 1:
		var sr v1.SubmitRecoveryResponse
		if sr.Unmarshal(ev.bytes) != nil || sr.RecoveryId == "" {
			return devicesec.ErrInvalid
		}
		nextDraft := *t.op.Recovery
		nextDraft.RecoveryID, nextDraft.ReadyMs = sr.RecoveryId, int64(sr.ReadyMs)
		nextOp := *t.op
		nextOp.Phase, nextOp.Recovery, t.stage = "PUBLISHED", &nextDraft, 2
		e.saveOperationCAS(t.op, &nextOp, parentTarget(t, 0))
	case 2:
		t.fetchContext(e, ev.op)
	case 3:
		resp, draft := ev.recCtx.resp, t.op.Recovery
		t.phase = resp.Phase
		if int64(resp.ReadyMs) > 0 && int64(resp.ReadyMs) != draft.ReadyMs {
			nextDraft := *draft
			nextDraft.ReadyMs = int64(resp.ReadyMs)
			nextOp := *t.op
			nextOp.Recovery, t.stage = &nextDraft, 4
			e.saveOperationCAS(t.op, &nextOp, parentTarget(t, 0))
			return nil
		}
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{
			ResumedDraft: true, CodeSaved: t.op.CodeSaved,
			Recovery: &corev1.SecurityRecoveryInfo{RecoveryId: draft.RecoveryID, Phase: resp.Phase, ReadyMs: draft.ReadyMs},
		})
	case 4:
		saved := ev.op
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{
			ResumedDraft: true, CodeSaved: saved.CodeSaved,
			Recovery: &corev1.SecurityRecoveryInfo{RecoveryId: saved.Recovery.RecoveryID, Phase: t.phase, ReadyMs: saved.Recovery.ReadyMs},
		})
	}
	return nil
}

type completeRecoveryTask struct {
	taskBase
	op          *PersistedOperation
	pin         devicesec.Pin
	targetState devicesec.State
	tok         string
	signAt      uint64
	leaseRaw    []byte
	contentKeys []ContentKeyEntry
	states      [][]byte
	proofs      [][]byte
	retiring    string
	retNow      uint64
	retSecret   [32]byte
	retState    devicesec.State
	retStateRaw []byte
	retLeaseRaw []byte
	putRetBytes []byte
	finalHead   devicesec.State
	rec         *PersistedRecord
	prof        *PersistedPublicProfile
	rev         string
}

func (t *completeRecoveryTask) onError(e *Engine, err error) {
	zeroBytes(t.retSecret[:])
	e.failTarget(t.target, err)
}

func (t *completeRecoveryTask) callComplete(e *Engine, curOp *PersistedOperation) {
	t.op, t.stage = curOp, 3
	e.waitRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_COMPLETE_RECOVERY, curOp.Server, "", curOp.Recovery.CompleteRequest, t, 0)
}

func (t *completeRecoveryTask) commitRecord(e *Engine, curOp *PersistedOperation, head devicesec.State, leaseRaw []byte) error {
	wf := e.ensureWF()
	var acctPub32 [32]byte
	copy(acctPub32[:], curOp.Recovery.AccountPublicKey)
	prof := &PersistedPublicProfile{Server: curOp.Server, Pin: t.pin, AccountPublicKey: acctPub32}
	profBytes, err := prof.Encode()
	if err != nil {
		return err
	}
	floorMs := max(e.lastNowMs, curOp.Recovery.NowMs)
	var priorKeys []ContentKeyEntry
	if wf.record != nil {
		if wf.record.FloorMs > floorMs {
			floorMs = wf.record.FloorMs
		}
		if wf.record.Pin == t.pin {
			priorKeys = slices.Clone(wf.record.ContentKeys)
		}
	}
	rec := &PersistedRecord{
		Pin: t.pin, MemberID: curOp.MemberID, Member: true, Server: curOp.Server,
		AccountPublicKey: slices.Clone(acctPub32[:]), States: t.states, RecoveryProofs: t.proofs,
		Lease: leaseRaw, FloorMs: floorMs, ContentKeys: append(priorKeys, t.contentKeys...), RecoveryObjectsPending: true,
	}
	recBytes, err := rec.Encode()
	if err != nil {
		return err
	}
	rev, opRev, recordExpected, createRecord := e.nextWorkflowToken(), u64Str(curOp.Revision), "", true
	if wf.pendingOpRev != "" {
		opRev = wf.pendingOpRev
	}
	if wf.recordRev != "" {
		recordExpected, createRecord = wf.recordRev, false
	}
	t.op, t.finalHead, t.rec, t.prof, t.rev, t.stage = curOp, head, rec, prof, rev, 12
	e.batchCommitStorage(curOp.Recovery.EdSlot, curOp.Recovery.XSlot, []string{curOp.Recovery.EdSlot, curOp.Recovery.XSlot}, true,
		buildAccountCommitWrites(curOp.Server, t.pin, profBytes, recBytes, createRecord, recordExpected, rev, opRev, true, "", ""), t)
	return nil
}

func (t *completeRecoveryTask) publishRetirement(e *Engine, retState devicesec.State, retStateRaw, retLeaseRaw, putRetBytes []byte) {
	t.retState, t.retStateRaw, t.retLeaseRaw, t.putRetBytes, t.stage = retState, retStateRaw, retLeaseRaw, putRetBytes, 11
	e.waitRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE, t.op.Server, t.tok, putRetBytes, t, 0)
}

func (t *completeRecoveryTask) fgCheckMask() uint32 { return fgOnStart | 1<<0 | 1<<1 }

func (t *completeRecoveryTask) step(e *Engine, ev *secEvent) error {
	switch t.stage {
	case 0:
		op := e.ensureWF().pendingOp
		if op == nil || op.Kind != "recovery" || op.Recovery == nil || op.Recovery.RecoveryID == "" ||
			len(op.Recovery.TargetState) == 0 || len(op.Recovery.Proof) == 0 || len(op.Recovery.Secret) != 32 {
			return devicesec.ErrStale
		}
		if !op.CodeSaved {
			return newWfError("invalid", text.KeySecurityConfirmRecoveryCodeSavedFirst)
		}
		draft := op.Recovery
		targetState, err := devicesec.ParseState(draft.TargetState)
		if err != nil {
			return err
		}
		pin := devicesec.Pin{Account: draft.AccountID, Domain: draft.Domain, Genesis: draft.Genesis}
		if pin.Verify(targetState) != nil {
			return devicesec.ErrUntrusted
		}
		t.op, t.pin, t.targetState = op, pin, targetState
		if len(draft.CompleteRequest) > 0 {
			t.callComplete(e, op)
			return nil
		}
		t.stage = 1
		e.waitSign(keyCandEd, draft.EdSlot, append(append([]byte{}, recoveryDonePrefix...), draft.RecoveryID...), t, 0)
	case 1:
		nextDraft := *t.op.Recovery
		nextDraft.CompleteRequest = (&v1.CompleteRecoveryRequest{RecoveryId: t.op.Recovery.RecoveryID, Signature: ev.bytes}).AppendMarshal(nil)
		nextOp := *t.op
		nextOp.Recovery, t.stage = &nextDraft, 2
		e.saveOperationCAS(t.op, &nextOp, parentTarget(t, 0))
	case 2:
		t.callComplete(e, ev.op)
	case 3:
		var resp v1.CompleteRecoveryResponse
		if resp.Unmarshal(ev.bytes) != nil || resp.DeviceId != t.op.MemberID {
			return devicesec.ErrUntrusted
		}
		t.stage = 4
		e.forceLoginToken(t.op.Server, keyCandEd, t.op.Recovery.EdSlot, t.op.MemberID, true, t.fgGen, false, parentTarget(t, 0))
	case 4:
		t.tok = ev.str
		curOp := t.op
		var secret32 [32]byte
		copy(secret32[:], curOp.Recovery.Secret)
		t.contentKeys = []ContentKeyEntry{{Epoch: t.targetState.Core.ContentEpoch, Secret: secret32}}
		zeroBytes(secret32[:])
		states, proofs := make([][]byte, 0, len(curOp.Recovery.States)+2), make([][]byte, 0, len(curOp.Recovery.RecoveryProofs)+2)
		for i, s := range curOp.Recovery.States {
			var rp []byte
			if i < len(curOp.Recovery.RecoveryProofs) {
				rp = curOp.Recovery.RecoveryProofs[i]
			}
			states, proofs = append(states, s), append(proofs, rp)
		}
		t.states, t.proofs = append(states, curOp.Recovery.TargetState), append(proofs, curOp.Recovery.Proof)
		t.signAt, t.stage = max(e.currentSigningTime(e.lastNowMs), t.targetState.Core.IssuedAt), 5
		e.buildAndSignLease(t.targetState, keyCandEd, curOp.Recovery.EdSlot, t.signAt, parentTarget(t, 0))
	case 5:
		leaseRaw, _ := ev.lease.MarshalBinary()
		t.leaseRaw, t.stage = leaseRaw, 6
		e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE, t.op.Server, t.tok, &v1.PutSecurityStateRequest{Lease: leaseRaw}, t)
	case 6:
		curOp := t.op
		if retiring := curOp.Recovery.RetiringMemberID; retiring != "" && retiring != curOp.MemberID {
			if _, stillMember := t.targetState.Core.Member(retiring); stillMember {
				if len(curOp.Recovery.RetirementRequest) > 0 {
					var rr v1.PutSecurityStateRequest
					if rr.Unmarshal(curOp.Recovery.RetirementRequest) == nil && len(rr.States) == 1 {
						if rs, rErr := devicesec.ParseState(rr.States[0].State); rErr == nil {
							t.publishRetirement(e, rs, rr.States[0].State, rr.Lease, curOp.Recovery.RetirementRequest)
							return nil
						}
					}
				}
				t.retiring, t.stage = retiring, 7
				e.waitEntropy(64, t, 0)
				return nil
			}
		}
		return t.commitRecord(e, curOp, t.targetState, t.leaseRaw)
	case 7:
		var retSecret, secret32 [32]byte
		if !e.readRandom(retSecret[:]) {
			return ErrNeedEntropy
		}
		var members []devicesec.Member
		for _, m := range t.targetState.Core.Members {
			if m.ID != t.retiring {
				members = append(members, m)
			}
		}
		removed := append(slices.Clone(t.targetState.Core.Removed), t.retiring)
		sortutil.Strings(removed)
		rNow := max(t.signAt, t.targetState.Core.IssuedAt)
		t.retNow, t.retSecret = rNow, retSecret
		copy(secret32[:], t.op.Recovery.Secret)
		params := kemBuildParams{kind: kemBuildMembership, parent: t.targetState, members: members, removed: removed, oldSecret: secret32, newSecret: retSecret, now: rNow}
		zeroBytes(secret32[:])
		zeroBytes(retSecret[:])
		t.stage = 8
		e.buildAndSignStateWithKEM(keyCandEd, t.op.Recovery.EdSlot, true, t.fgGen, params, members, parentTarget(t, 0))
	case 8:
		t.retState = *ev.state
		t.retStateRaw, _ = ev.state.MarshalBinary()
		t.stage = 9
		e.buildAndSignLease(*ev.state, keyCandEd, t.op.Recovery.EdSlot, t.retNow, parentTarget(t, 0))
	case 9:
		retLeaseRaw, _ := ev.lease.MarshalBinary()
		putRetBytes := (&v1.PutSecurityStateRequest{States: []*v1.SecurityState{{State: t.retStateRaw}}, Lease: retLeaseRaw}).AppendMarshal(nil)
		t.contentKeys = append(t.contentKeys, ContentKeyEntry{Epoch: t.retState.Core.ContentEpoch, Secret: t.retSecret})
		zeroBytes(t.retSecret[:])
		nextDraft := *t.op.Recovery
		nextDraft.RetirementRequest = putRetBytes
		nextOp := *t.op
		nextOp.Recovery, t.retLeaseRaw, t.putRetBytes, t.stage = &nextDraft, retLeaseRaw, putRetBytes, 10
		e.saveOperationCAS(t.op, &nextOp, parentTarget(t, 0))
	case 10:
		t.op = ev.op
		t.publishRetirement(e, t.retState, t.retStateRaw, t.retLeaseRaw, t.putRetBytes)
	case 11:
		t.states, t.proofs = append(t.states, t.retStateRaw), append(t.proofs, nil)
		return t.commitRecord(e, t.op, t.retState, t.retLeaseRaw)
	case 12:
		curOp := t.op
		e.installCommittedAccount(curOp.Server, t.rev, t.rec, t.prof, ev.storage, curOp.Recovery.CandidateEdPub, curOp.Recovery.CandidateXPub)
		t.stage = 13
		e.applyPersistedRecordToEngine(t.rec, parentTarget(t, 0))
	case 13:
		e.fireNotifyRecord(t.rec, t.finalHead, t.rev)
		e.republishRecoveryObjects(taskTarget{})
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{
			AccountAdopted: &corev1.SecurityAccountAdopted{Server: t.op.Server, DeviceId: t.op.MemberID, AccountId: t.pin.Account},
		})
	}
	return nil
}

type discardUnsignedRecoveryTask struct {
	taskBase
	edSlot, xSlot string
}

func (t *discardUnsignedRecoveryTask) fgCheckMask() uint32 { return fgOnStart }

func (t *discardUnsignedRecoveryTask) step(e *Engine, ev *secEvent) error {
	if ev.kind == evChildDone {
		e.discardCandidatePair(t.edSlot, t.xSlot)
		e.okTarget(t.target, nil)
		return nil
	}
	op := e.ensureWF().pendingOp
	if op == nil || op.Kind != "recovery" || op.Phase != "PREPARED" || len(op.Signed) > 0 || op.Recovery == nil {
		return devicesec.ErrStale
	}
	t.edSlot, t.xSlot = op.Recovery.EdSlot, op.Recovery.XSlot
	e.clearOperationCAS(op, parentTarget(t, 0))
	return nil
}

type discardRejectedRecoveryTask struct {
	taskBase
	op            *PersistedOperation
	edSlot, xSlot string
}

func (t *discardRejectedRecoveryTask) fgCheckMask() uint32 { return fgOnStart | 1<<0 }

func (t *discardRejectedRecoveryTask) step(e *Engine, ev *secEvent) error {
	switch t.stage {
	case 0:
		op := e.ensureWF().pendingOp
		if op == nil || op.Kind != "recovery" || op.Recovery == nil || op.Recovery.RecoveryID == "" {
			return devicesec.ErrStale
		}
		t.op, t.edSlot, t.xSlot, t.stage = op, op.Recovery.EdSlot, op.Recovery.XSlot, 1
		e.startFetchRecoveryCtxWithCandSlot(op.Server, op.Recovery.RecoveryID, op.Recovery.EdSlot, 0, parentTarget(t, 0))
	case 1:
		if ev.recCtx.resp.Phase != v1.GetRecoveryContextResponse_PHASE_REJECTED {
			return devicesec.ErrStale
		}
		t.stage = 2
		e.clearOperationCAS(t.op, parentTarget(t, 0))
	case 2:
		e.discardCandidatePair(t.edSlot, t.xSlot)
		e.okTarget(t.target, nil)
	}
	return nil
}
