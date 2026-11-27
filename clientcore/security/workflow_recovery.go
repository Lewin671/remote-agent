package security

import (
	"bytes"
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

func mapWireRecoveryPhase(p v1.GetRecoveryContextResponse_Phase) corev1.SecurityRecoveryPhase {
	switch p {
	case v1.GetRecoveryContextResponse_PHASE_PENDING:
		return corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_SUBMITTED_WAITING
	case v1.GetRecoveryContextResponse_PHASE_READY, v1.GetRecoveryContextResponse_PHASE_PUBLISHED:
		return corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_READY_TO_COMPLETE
	case v1.GetRecoveryContextResponse_PHASE_REJECTED:
		return corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_REJECTED
	default:
		return corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_UNSPECIFIED
	}
}

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
	e.waitProtoRPC(corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_RECOVERY_CHALLENGE, server, "", &v1.GetRecoveryChallengeRequest{}, t)
}

func (e *Engine) startFetchRecoveryCtxWithCandSlot(server, recoveryID, candEdSlot string, fgGen uint64, target taskTarget) {
	e.waitProtoRPC(corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_RECOVERY_CHALLENGE, server, "", &v1.GetRecoveryChallengeRequest{}, &fetchRecoveryCtxTask{
		taskBase: taskBase{fgGen: fgGen, target: target}, server: server, recoveryID: recoveryID, candEdSlot: candEdSlot,
	})
}

func (t *fetchRecoveryCtxTask) onError(e *Engine, err error) {
	zeroBytes(t.seed[:])
	e.failTarget(t.target, err)
}

const (
	recCtxChallenge uint8 = iota
	recCtxSign
	recCtxFetch
)

func (t *fetchRecoveryCtxTask) sendContextRPC(e *Engine, sig []byte) {
	req := &v1.GetRecoveryContextRequest{RecoveryId: t.recoveryID, Challenge: t.challenge, Signature: sig}
	if len(t.recPub) > 0 {
		req.RecoveryEd = slices.Clone(t.recPub)
	}
	t.stage = recCtxFetch
	e.waitProtoRPC(corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_RECOVERY_CONTEXT, t.server, "", req, t)
}

func (t *fetchRecoveryCtxTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	switch t.stage {
	case recCtxChallenge:
		if t.fgGen != 0 {
			if err := e.checkFG(t.fgGen); err != nil {
				return err
			}
		}
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
		t.stage = recCtxSign
		e.waitSign(keyCandEd, t.candEdSlot, signMsg, t, 0)
	case recCtxSign:
		t.sendContextRPC(e, ev.bytes)
	case recCtxFetch:
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
	oldSeed                                                   [32]byte
	resp                                                      *v1.GetRecoveryContextResponse
	pin                                                       devicesec.Pin
	acctPub                                                   [32]byte
	rawStates, rawProofs                                      [][]byte
	head                                                      devicesec.State
	candEdPub                                                 []byte
	cleanEd, cleanX                                           bool
}

func (t *prepareEmergencyRecoveryTask) onError(e *Engine, err error) {
	zeroBytes(t.oldSeed[:])
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

const (
	prepRecContext uint8 = iota
	prepRecEntropy
	prepRecKeyEd
	prepRecKeyX
	prepRecClaim
	prepRecSigned
)

func (t *prepareEmergencyRecoveryTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case prepRecContext:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if ev.kind == evStart {
			server, err := CheckServerURL(t.rawServer)
			if err != nil {
				return err
			}
			if wf.pendingOp != nil || wf.regIntent != nil || wf.bootstrapInvalid {
				return newWfError("pending", WorkflowReasonFinishCurrentOpFirst)
			}
			oldSeed, genPrefix, ok := DecodeRecoveryCode(t.code)
			if !ok {
				return errInvalidRecoveryCode
			}
			oldRecPub, err := devicesec.Ed25519PublicFromSeed(oldSeed[:])
			if err != nil {
				zeroBytes(oldSeed[:])
				return err
			}
			t.server, t.oldSeed = server, oldSeed
			zeroBytes(oldSeed[:])
			e.startFetchRecoveryCtxWithSeed(server, oldRecPub, genPrefix, wf.record, t.fgGen, t.oldSeed, parentTarget(t, 0))
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
		t.stage = prepRecEntropy
		e.waitEntropy(128, t, 0)
	case prepRecEntropy:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		tok := e.nextWorkflowToken()
		t.edSlot, t.xSlot, t.stage = "rec-ed-"+tok, "rec-x-"+tok, prepRecKeyEd
		e.waitKeyGen(keyCandEd, t.edSlot, true, t, 0)
	case prepRecKeyEd:
		t.candEdPub, t.cleanEd, t.stage = ev.bytes, true, prepRecKeyX
		e.waitKeyGen(keyCandX, t.xSlot, true, t, 0)
	case prepRecKeyX:
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
		op := e.newPreparedOp(t.server, candID, PendingOpKindRecovery, "", headHash)
		op.Recovery = &PersistedRecoveryDraft{
			AccountID: t.pin.Account, AccountPublicKey: slices.Clone(t.acctPub[:]), Domain: t.pin.Domain, Genesis: t.pin.Genesis,
			EdSlot: t.edSlot, XSlot: t.xSlot, CandidateEdPub: slices.Clone(t.candEdPub), CandidateXPub: slices.Clone(ev.bytes),
			RecoveryEd: slices.Clone(newRecPub), Secret: slices.Clone(newSecret[:]), NowMs: int64(now), RetiringMemberID: t.retiring,
			States: t.rawStates, RecoveryProofs: t.rawProofs,
		}
		zeroBytes(newSecret[:])
		t.stage = prepRecClaim
		e.claimOperationCAS(op, nil, candID, parentTarget(t, 0))
	case prepRecClaim:
		wf.server = t.server
		t.cleanEd, t.cleanX = false, false
		seed := t.oldSeed
		zeroBytes(t.oldSeed[:])
		t.stage = prepRecSigned
		e.startSignRecoveryDraft(ev.op, seed, t.fgGen, parentTarget(t, 0))
		zeroBytes(seed[:])
	case prepRecSigned:
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{
			RecoveryCode: t.newCode,
		})
	}
	return nil
}

const (
	signRecDraftEntropy uint8 = iota
	signRecDraftKEM
	signRecDraftSave
)

type signRecoveryDraftTask struct {
	taskBase
	op        *PersistedOperation
	oldSeed   [32]byte
	parent    devicesec.State
	newMember devicesec.Member
	members   []devicesec.Member
	newRecEd  [32]byte
	now       uint64
}

func (t *signRecoveryDraftTask) onError(e *Engine, err error) {
	zeroBytes(t.oldSeed[:])
	e.failTarget(t.target, err)
}

func (e *Engine) startSignRecoveryDraft(op *PersistedOperation, oldSeed [32]byte, fgGen uint64, target taskTarget) {
	defer zeroBytes(oldSeed[:])
	if err := e.checkFG(fgGen); err != nil {
		e.failTarget(target, err)
		return
	}
	draft := op.Recovery
	if draft == nil || len(draft.States) == 0 || len(draft.RecoveryEd) != 32 || len(draft.Secret) != 32 {
		e.failTarget(target, devicesec.ErrInvalid)
		return
	}
	oldRecPub, err := devicesec.Ed25519PublicFromSeed(oldSeed[:])
	if err != nil {
		e.failTarget(target, err)
		return
	}
	pin := devicesec.Pin{Account: draft.AccountID, Domain: draft.Domain, Genesis: draft.Genesis}
	states, _, _, err := parseAndVerifyChain(pin, draft.States, draft.RecoveryProofs)
	if err != nil || len(states) == 0 {
		if err == nil {
			err = devicesec.ErrInvalid
		}
		e.failTarget(target, err)
		return
	}
	parent := states[len(states)-1]
	if !bytes.Equal(parent.Core.RecoveryEd[:], oldRecPub) {
		e.failTarget(target, errInvalidRecoveryCode)
		return
	}
	newMember, err := devicesec.NewMember(draft.CandidateEdPub, draft.CandidateXPub, devicesec.Client)
	if err != nil {
		e.failTarget(target, err)
		return
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
	now := max(max(uint64(max(1, draft.NowMs)), uint64(max(1, e.lastNowMs))), parent.Core.IssuedAt)
	t := &signRecoveryDraftTask{
		taskBase: taskBase{fgGen: fgGen, target: target}, op: op, oldSeed: oldSeed,
		parent: parent, newMember: newMember, members: members, now: now,
	}
	copy(t.newRecEd[:], draft.RecoveryEd)
	e.waitEntropy(64, t, 0)
}

func (t *signRecoveryDraftTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	switch t.stage {
	case signRecDraftEntropy:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		draft := t.op.Recovery
		var secretCopy [32]byte
		copy(secretCopy[:], draft.Secret)
		params := kemBuildParams{kind: kemBuildRecovery, parent: t.parent, member: t.newMember, members: t.members, newSecret: secretCopy, recoveryEd: t.newRecEd, now: t.now}
		zeroBytes(secretCopy[:])
		t.stage = signRecDraftKEM
		e.buildAndSignStateWithKEM(keyCandEd, draft.EdSlot, true, t.fgGen, params, t.members, parentTarget(t, 0))
	case signRecDraftKEM:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
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
		nextOp.Signed, nextOp.Recovery, t.stage = reqBytes, &nextDraft, signRecDraftSave
		e.saveOperationCAS(t.op, &nextOp, parentTarget(t, 0))
	case signRecDraftSave:
		e.completeTarget(t.target, &secEvent{op: ev.op})
	}
	return nil
}

const (
	contRecStart uint8 = iota
	contRecSigned
	contRecSubmitRPC
	contRecSave
)

type continueRecoverySigningTask struct {
	taskBase
	op         *PersistedOperation
	oldCode    string
	recoveryID string
	readyMs    int64
}

func (t *continueRecoverySigningTask) submitSigned(e *Engine, curOp *PersistedOperation) {
	t.op, t.stage = curOp, contRecSubmitRPC
	e.waitRPC(corev1.RelayRpcMethod_RELAY_RPC_METHOD_SUBMIT_RECOVERY, curOp.Server, "", curOp.Signed, t, 0)
}

func (t *continueRecoverySigningTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case contRecStart:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		op := wf.pendingOp
		if op == nil || op.Kind != PendingOpKindRecovery || op.Recovery == nil {
			return devicesec.ErrStale
		}
		if len(op.Signed) > 0 {
			t.submitSigned(e, op)
			return nil
		}
		oldSeed, genPrefix, ok := DecodeRecoveryCode(t.oldCode)
		if !ok || !bytes.Equal(op.Recovery.Genesis[:10], genPrefix[:]) {
			zeroBytes(oldSeed[:])
			return errInvalidRecoveryCode
		}
		t.stage = contRecSigned
		e.startSignRecoveryDraft(op, oldSeed, t.fgGen, parentTarget(t, 0))
		zeroBytes(oldSeed[:])
	case contRecSigned:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		t.submitSigned(e, ev.op)
	case contRecSubmitRPC:
		var sr v1.SubmitRecoveryResponse
		if sr.Unmarshal(ev.bytes) != nil || sr.RecoveryId == "" {
			return devicesec.ErrInvalid
		}
		nextDraft := *t.op.Recovery
		nextDraft.RecoveryID, nextDraft.ReadyMs = sr.RecoveryId, int64(sr.ReadyMs)
		nextOp := *t.op
		nextOp.Phase, nextOp.Recovery, t.recoveryID, t.readyMs, t.stage = PendingOpPhasePublished, &nextDraft, sr.RecoveryId, int64(sr.ReadyMs), contRecSave
		e.saveOperationCAS(t.op, &nextOp, parentTarget(t, 0))
	case contRecSave:
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{
			Recovery: &corev1.SecurityRecoveryInfo{
				RecoveryId:    t.recoveryID,
				RecoveryPhase: corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_SUBMITTED_WAITING,
				ReadyMs:       t.readyMs,
			},
		})
	}
	return nil
}

const (
	resumeRecStart uint8 = iota
	resumeRecSubmitRPC
	resumeRecSavePub
	resumeRecFetchCtx
	resumeRecSaveReady
)

type resumeRecoveryTask struct {
	taskBase
	op    *PersistedOperation
	phase corev1.SecurityRecoveryPhase
}

func (t *resumeRecoveryTask) fetchContext(e *Engine, op *PersistedOperation) {
	t.op, t.stage = op, resumeRecFetchCtx
	e.startFetchRecoveryCtxWithCandSlot(op.Server, op.Recovery.RecoveryID, op.Recovery.EdSlot, t.fgGen, parentTarget(t, 0))
}

func (t *resumeRecoveryTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case resumeRecStart:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		op := wf.pendingOp
		if op == nil || op.Kind != PendingOpKindRecovery || op.Recovery == nil {
			e.okTarget(t.target, nil)
			return nil
		}
		if op.Recovery.RecoveryID == "" {
			if len(op.Signed) > 0 {
				t.op, t.stage = op, resumeRecSubmitRPC
				e.waitRPC(corev1.RelayRpcMethod_RELAY_RPC_METHOD_SUBMIT_RECOVERY, op.Server, "", op.Signed, t, 0)
				return nil
			}
			e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{ResumedDraft: true, CodeSaved: op.CodeSaved})
			return nil
		}
		t.fetchContext(e, op)
	case resumeRecSubmitRPC:
		var sr v1.SubmitRecoveryResponse
		if sr.Unmarshal(ev.bytes) != nil || sr.RecoveryId == "" {
			return devicesec.ErrInvalid
		}
		nextDraft := *t.op.Recovery
		nextDraft.RecoveryID, nextDraft.ReadyMs = sr.RecoveryId, int64(sr.ReadyMs)
		nextOp := *t.op
		nextOp.Phase, nextOp.Recovery, t.stage = PendingOpPhasePublished, &nextDraft, resumeRecSavePub
		e.saveOperationCAS(t.op, &nextOp, parentTarget(t, 0))
	case resumeRecSavePub:
		t.fetchContext(e, ev.op)
	case resumeRecFetchCtx:
		resp, draft := ev.recCtx.resp, t.op.Recovery
		t.phase = mapWireRecoveryPhase(resp.Phase)
		if int64(resp.ReadyMs) > 0 && int64(resp.ReadyMs) != draft.ReadyMs {
			nextDraft := *draft
			nextDraft.ReadyMs = int64(resp.ReadyMs)
			nextOp := *t.op
			nextOp.Recovery, t.stage = &nextDraft, resumeRecSaveReady
			e.saveOperationCAS(t.op, &nextOp, parentTarget(t, 0))
			return nil
		}
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{
			ResumedDraft: true, CodeSaved: t.op.CodeSaved,
			Recovery: &corev1.SecurityRecoveryInfo{RecoveryId: draft.RecoveryID, RecoveryPhase: t.phase, ReadyMs: draft.ReadyMs},
		})
	case resumeRecSaveReady:
		saved := ev.op
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{
			ResumedDraft: true, CodeSaved: saved.CodeSaved,
			Recovery: &corev1.SecurityRecoveryInfo{RecoveryId: saved.Recovery.RecoveryID, RecoveryPhase: t.phase, ReadyMs: saved.Recovery.ReadyMs},
		})
	}
	return nil
}

const (
	compRecStart uint8 = iota
	compRecSign
	compRecSave
	compRecRPC
	compRecLogin
	compRecLease
	compRecPutLease
	compRecRetEntropy
	compRecRetKEM
	compRecRetLease
	compRecRetSave
	compRecRetPut
	compRecCommit
	compRecApply
)

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
	t.op, t.stage = curOp, compRecRPC
	e.waitRPC(corev1.RelayRpcMethod_RELAY_RPC_METHOD_COMPLETE_RECOVERY, curOp.Server, "", curOp.Recovery.CompleteRequest, t, 0)
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
	t.op, t.finalHead, t.rec, t.prof, t.rev, t.stage = curOp, head, rec, prof, rev, compRecCommit
	e.batchCommitStorage(curOp.Recovery.EdSlot, curOp.Recovery.XSlot, []string{curOp.Recovery.EdSlot, curOp.Recovery.XSlot}, true,
		buildAccountCommitWrites(curOp.Server, t.pin, profBytes, recBytes, createRecord, recordExpected, rev, opRev, true, "", ""), t)
	return nil
}

func (t *completeRecoveryTask) publishRetirement(e *Engine, retState devicesec.State, retStateRaw, retLeaseRaw, putRetBytes []byte) {
	t.retState, t.retStateRaw, t.retLeaseRaw, t.putRetBytes, t.stage = retState, retStateRaw, retLeaseRaw, putRetBytes, compRecRetPut
	e.waitRPC(corev1.RelayRpcMethod_RELAY_RPC_METHOD_PUT_SECURITY_STATE, t.op.Server, t.tok, putRetBytes, t, 0)
}

func (t *completeRecoveryTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	switch t.stage {
	case compRecStart:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		op := e.ensureWF().pendingOp
		if op == nil || op.Kind != PendingOpKindRecovery || op.Recovery == nil || op.Recovery.RecoveryID == "" ||
			len(op.Recovery.TargetState) == 0 || len(op.Recovery.Proof) == 0 || len(op.Recovery.Secret) != 32 {
			return devicesec.ErrStale
		}
		if !op.CodeSaved {
			return newWfError("invalid", WorkflowReasonConfirmRecoveryCodeSavedFirst)
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
		t.stage = compRecSign
		e.waitSign(keyCandEd, draft.EdSlot, append(append([]byte{}, recoveryDonePrefix...), draft.RecoveryID...), t, 0)
	case compRecSign:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		nextDraft := *t.op.Recovery
		nextDraft.CompleteRequest = (&v1.CompleteRecoveryRequest{RecoveryId: t.op.Recovery.RecoveryID, Signature: ev.bytes}).AppendMarshal(nil)
		nextOp := *t.op
		nextOp.Recovery, t.stage = &nextDraft, compRecSave
		e.saveOperationCAS(t.op, &nextOp, parentTarget(t, 0))
	case compRecSave:
		t.callComplete(e, ev.op)
	case compRecRPC:
		var resp v1.CompleteRecoveryResponse
		if resp.Unmarshal(ev.bytes) != nil || resp.DeviceId != t.op.MemberID {
			return devicesec.ErrUntrusted
		}
		t.stage = compRecLogin
		e.forceLoginToken(t.op.Server, keyCandEd, t.op.Recovery.EdSlot, t.op.MemberID, true, t.fgGen, false, parentTarget(t, 0))
	case compRecLogin:
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
		t.signAt, t.stage = max(e.currentSigningTime(e.lastNowMs), t.targetState.Core.IssuedAt), compRecLease
		e.buildAndSignLease(t.targetState, keyCandEd, curOp.Recovery.EdSlot, t.signAt, parentTarget(t, 0))
	case compRecLease:
		leaseRaw, _ := ev.lease.MarshalBinary()
		t.leaseRaw, t.stage = leaseRaw, compRecPutLease
		e.waitProtoRPC(corev1.RelayRpcMethod_RELAY_RPC_METHOD_PUT_SECURITY_STATE, t.op.Server, t.tok, &v1.PutSecurityStateRequest{Lease: leaseRaw}, t)
	case compRecPutLease:
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
				t.retiring, t.stage = retiring, compRecRetEntropy
				e.waitEntropy(64, t, 0)
				return nil
			}
		}
		return t.commitRecord(e, curOp, t.targetState, t.leaseRaw)
	case compRecRetEntropy:
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
		t.stage = compRecRetKEM
		e.buildAndSignStateWithKEM(keyCandEd, t.op.Recovery.EdSlot, true, t.fgGen, params, members, parentTarget(t, 0))
	case compRecRetKEM:
		t.retState = *ev.state
		t.retStateRaw, _ = ev.state.MarshalBinary()
		t.stage = compRecRetLease
		e.buildAndSignLease(*ev.state, keyCandEd, t.op.Recovery.EdSlot, t.retNow, parentTarget(t, 0))
	case compRecRetLease:
		retLeaseRaw, _ := ev.lease.MarshalBinary()
		putRetBytes := (&v1.PutSecurityStateRequest{States: []*v1.SecurityState{{State: t.retStateRaw}}, Lease: retLeaseRaw}).AppendMarshal(nil)
		t.contentKeys = append(t.contentKeys, ContentKeyEntry{Epoch: t.retState.Core.ContentEpoch, Secret: t.retSecret})
		zeroBytes(t.retSecret[:])
		nextDraft := *t.op.Recovery
		nextDraft.RetirementRequest = putRetBytes
		nextOp := *t.op
		nextOp.Recovery, t.retLeaseRaw, t.putRetBytes, t.stage = &nextDraft, retLeaseRaw, putRetBytes, compRecRetSave
		e.saveOperationCAS(t.op, &nextOp, parentTarget(t, 0))
	case compRecRetSave:
		t.op = ev.op
		t.publishRetirement(e, t.retState, t.retStateRaw, t.retLeaseRaw, t.putRetBytes)
	case compRecRetPut:
		t.states, t.proofs = append(t.states, t.retStateRaw), append(t.proofs, nil)
		return t.commitRecord(e, t.op, t.retState, t.retLeaseRaw)
	case compRecCommit:
		curOp := t.op
		e.installCommittedAccount(curOp.Server, t.rev, t.rec, t.prof, ev.storage, curOp.Recovery.CandidateEdPub, curOp.Recovery.CandidateXPub)
		t.stage = compRecApply
		e.applyPersistedRecordToEngine(t.rec, parentTarget(t, 0))
	case compRecApply:
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

func (t *discardUnsignedRecoveryTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	if ev.kind == evChildDone {
		e.discardCandidatePair(t.edSlot, t.xSlot)
		e.okTarget(t.target, nil)
		return nil
	}
	if err := e.checkFG(t.fgGen); err != nil {
		return err
	}
	op := e.ensureWF().pendingOp
	if op == nil || op.Kind != PendingOpKindRecovery || op.Phase != PendingOpPhasePrepared || len(op.Signed) > 0 || op.Recovery == nil {
		return devicesec.ErrStale
	}
	t.edSlot, t.xSlot = op.Recovery.EdSlot, op.Recovery.XSlot
	e.clearOperationCAS(op, parentTarget(t, 0))
	return nil
}

const (
	discardRejStart uint8 = iota
	discardRejCtx
	discardRejClear
)

type discardRejectedRecoveryTask struct {
	taskBase
	op            *PersistedOperation
	edSlot, xSlot string
}

func (t *discardRejectedRecoveryTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	switch t.stage {
	case discardRejStart:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		op := e.ensureWF().pendingOp
		if op == nil || op.Kind != PendingOpKindRecovery || op.Recovery == nil || op.Recovery.RecoveryID == "" {
			return devicesec.ErrStale
		}
		t.op, t.edSlot, t.xSlot, t.stage = op, op.Recovery.EdSlot, op.Recovery.XSlot, discardRejCtx
		e.startFetchRecoveryCtxWithCandSlot(op.Server, op.Recovery.RecoveryID, op.Recovery.EdSlot, 0, parentTarget(t, 0))
	case discardRejCtx:
		if ev.recCtx.resp.Phase != v1.GetRecoveryContextResponse_PHASE_REJECTED {
			return devicesec.ErrStale
		}
		t.stage = discardRejClear
		e.clearOperationCAS(t.op, parentTarget(t, 0))
	case discardRejClear:
		e.discardCandidatePair(t.edSlot, t.xSlot)
		e.okTarget(t.target, nil)
	}
	return nil
}
