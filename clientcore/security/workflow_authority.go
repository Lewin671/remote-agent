package security

import (
	"crypto/sha256"
	"slices"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

type renewLeaseTask struct {
	taskBase
	force              bool
	pending            *PersistedOperation
	leaseRaw, reqBytes []byte
}

const (
	renewLeasePre uint8 = iota
	renewLeaseSync
	renewLeasePendingSign
	renewLeasePendingSave
	renewLeasePendingPut
	renewLeasePendingInstall
	renewLeasePendingClearRetry
	renewLeaseClaim
	renewLeaseResuming
)

func (t *renewLeaseTask) uploadPendingLease(e *Engine, curPending *PersistedOperation, leaseBytes, reqBytes []byte) {
	t.pending, t.leaseRaw, t.reqBytes, t.stage = curPending, leaseBytes, reqBytes, renewLeasePendingPut
	e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE, curPending.Server, "", true, t.fgGen, reqBytes, parentTarget(t, 0))
}

func (t *renewLeaseTask) runAfterPre(e *Engine) {
	if p := e.ensureWF().pendingOp; p != nil && p.Kind != "offer" && p.Kind != "delete-records" {
		e.okTarget(t.target, nil)
		return
	}
	t.stage = renewLeaseSync
	e.syncStates(false, "", parentTarget(t, 0))
}

func (t *renewLeaseTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case renewLeasePre:
		if ev.kind == evStart {
			if err := e.checkFG(t.fgGen); err != nil {
				return err
			}
			if wf.pendingOp != nil && wf.pendingOp.Kind != "delete-records" {
				e.startResumePendingOp(t.fgGen, parentTarget(t, 0))
				return nil
			}
		}
		t.runAfterPre(e)
	case renewLeaseSync:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if !e.isAuthority() {
			e.okTarget(t.target, nil)
			return nil
		}
		pending := wf.pendingOp
		var currentLease *devicesec.LeaseCertificate
		if e.hasLease && e.lease.Head == e.headHash {
			l := e.lease
			currentLease = &l
		}
		if !t.force && (pending == nil || len(pending.Retry) == 0) && !needsLeaseRenewal(currentLease, e.effectiveNow(e.lastNowMs)) {
			e.okTarget(t.target, nil)
			return nil
		}
		if pending != nil {
			if pending.Server != e.effectiveServer() || pending.MemberID != e.selfDeviceID {
				return devicesec.ErrStale
			}
			var existingCert *devicesec.LeaseCertificate
			if len(pending.Retry) > 0 {
				var retryReq v1.PutSecurityStateRequest
				if retryReq.Unmarshal(pending.Retry) == nil && len(retryReq.Lease) > 0 {
					if lc, err := devicesec.ParseLease(retryReq.Lease); err == nil && lc.Head == e.headHash && lc.Verify(e.head) == nil {
						existingCert = &lc
					}
				}
			}
			effNow := e.effectiveNow(e.lastNowMs)
			if existingCert == nil || effNow >= existingCert.NotAfter || (currentLease != nil && currentLease.IssuedAt > existingCert.IssuedAt) {
				t.pending, t.stage = pending, renewLeasePendingSign
				e.buildAndSignLease(e.head, keyDevEd, "", e.currentSigningTime(e.lastNowMs), parentTarget(t, 0))
				return nil
			}
			lb, _ := existingCert.MarshalBinary()
			t.uploadPendingLease(e, pending, lb, pending.Retry)
			return nil
		}
		hh := e.headHash
		t.stage = renewLeaseClaim
		e.claimOperationCAS(e.newPreparedOp(e.effectiveServer(), e.selfDeviceID, "lease", "", hh), &hh, e.selfDeviceID, parentTarget(t, 0))
	case renewLeasePendingSign:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		lb, _ := ev.lease.MarshalBinary()
		reqBytes := (&v1.PutSecurityStateRequest{Lease: lb}).AppendMarshal(nil)
		nextP := *t.pending
		nextP.Retry, t.leaseRaw, t.reqBytes, t.stage = reqBytes, lb, reqBytes, renewLeasePendingSave
		e.saveOperationCAS(t.pending, &nextP, parentTarget(t, 0))
	case renewLeasePendingSave:
		t.uploadPendingLease(e, ev.op, t.leaseRaw, t.reqBytes)
	case renewLeasePendingPut:
		t.stage = renewLeasePendingInstall
		e.installRecordCAS(nil, nil, nil, t.leaseRaw, e.lastNowMs, nil, parentTarget(t, 0))
	case renewLeasePendingInstall:
		clearedRetry := *t.pending
		clearedRetry.Retry, t.stage = nil, renewLeasePendingClearRetry
		e.saveOperationCAS(t.pending, &clearedRetry, parentTarget(t, 0))
	case renewLeasePendingClearRetry:
		e.completeTarget(t.target, &secEvent{err: ev.err})
	case renewLeaseClaim:
		t.stage = renewLeaseResuming
		e.startResumeLeaseOp(ev.op, t.fgGen, t.target)
	}
	return nil
}

type prepareCreationTask struct {
	taskBase
	rawServer, server, edSlot, xSlot, memberID, recoveryCode string
	storagePersisted, cleanEd, cleanX                        bool
	edPub, acctPub, regReqBytes                              []byte
	recSeed                                                  [32]byte
}

func (t *prepareCreationTask) cleanAbort(e *Engine, hasEd, hasX bool, err error, bufs ...[]byte) {
	for _, b := range bufs {
		zeroBytes(b)
	}
	t.cleanEd, t.cleanX = hasEd, hasX
	t.onError(e, err)
}

func (t *prepareCreationTask) onError(e *Engine, err error) {
	zeroBytes(t.recSeed[:])
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
	prepCreateStart uint8 = iota
	prepCreateEntropy
	prepCreateKeyEd
	prepCreateKeyX
	prepCreateKEM
	prepCreateClaim
)

func (t *prepareCreationTask) buildDraft(e *Engine, xBytes []byte) error {
	if err := e.checkFG(t.fgGen); err != nil {
		return err
	}
	var rootRaw, domain, recoverySeed, secret, recEd [32]byte
	defer zeroBytes(rootRaw[:])
	defer zeroBytes(recoverySeed[:])
	defer zeroBytes(secret[:])
	if !e.readRandom(rootRaw[:]) || !e.readRandom(domain[:]) || !e.readRandom(recoverySeed[:]) || !e.readRandom(secret[:]) {
		return ErrNeedEntropy
	}
	acctSeed, err := deriveAccountSignSeed(rootRaw[:])
	zeroBytes(rootRaw[:])
	if err != nil {
		return err
	}
	defer zeroBytes(acctSeed)
	acctPub, err := devicesec.Ed25519PublicFromSeed(acctSeed)
	if err != nil {
		return err
	}
	recPubBytes, err := devicesec.Ed25519PublicFromSeed(recoverySeed[:])
	if err != nil {
		return err
	}
	copy(recEd[:], recPubBytes)
	member, err := devicesec.NewMember(t.edPub, xBytes, devicesec.Client)
	if err != nil {
		return err
	}
	now := max(1, uint64(max(1, e.lastNowMs)))
	regBody, err := (&v1.DeviceRegistration{
		AccountPublicKey: slices.Clone(acctPub), DevicePublicKey: slices.Clone(member.EdPublic[:]),
		Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, CreatedMs: int64(now),
	}).Marshal()
	if err != nil {
		return err
	}
	regSig, err := devicesec.Ed25519SignSeed(acctSeed, append(append([]byte{}, registerContext...), regBody...))
	if err != nil {
		return err
	}
	regReqBytes, err := (&v1.RegisterDeviceRequest{Registration: regBody, Signature: regSig, ProtocolVersion: protocolVersion}).Marshal()
	if err != nil {
		return err
	}
	t.acctPub, t.memberID, t.regReqBytes, t.recSeed, t.stage = acctPub, member.ID, regReqBytes, recoverySeed, prepCreateKEM
	params := kemBuildParams{kind: kemBuildGenesis, accountID: devicesec.ID(acctPub), domain: domain, member: member, newSecret: secret, recoveryEd: recEd, now: now}
	e.buildAndSignStateWithKEM(keyCandEd, t.edSlot, true, t.fgGen, params, []devicesec.Member{member}, parentTarget(t, 0))
	return nil
}

func (t *prepareCreationTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case prepCreateStart:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		server, err := CheckServerURL(t.rawServer)
		if err != nil {
			return err
		}
		if op := wf.pendingOp; op != nil {
			if op.Kind != "create" || op.Server != server {
				return newWfError("conflict", text.KeySecurityFinishTheOriginalSecurityOperationFirst)
			}
			persisted := t.storagePersisted
			if op.Create != nil {
				persisted = op.Create.StoragePersisted
			}
			e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{ResumedDraft: true, CodeSaved: op.CodeSaved, Flag: persisted})
			return nil
		}
		if wf.record != nil || wf.regIntent != nil || wf.bootstrapInvalid {
			return devicesec.ErrStale
		}
		t.server, t.stage = server, prepCreateEntropy
		e.waitEntropy(160, t, 0)
	case prepCreateEntropy:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		slotTok := e.nextWorkflowToken()
		t.edSlot, t.xSlot, t.stage = "create-ed-"+slotTok, "create-x-"+slotTok, prepCreateKeyEd
		e.waitKeyGen(keyCandEd, t.edSlot, true, t, 0)
	case prepCreateKeyEd:
		if err := e.checkFG(t.fgGen); err != nil {
			t.cleanEd = true
			return err
		}
		t.edPub, t.cleanEd, t.stage = ev.bytes, true, prepCreateKeyX
		e.waitKeyGen(keyCandX, t.xSlot, true, t, 0)
	case prepCreateKeyX:
		t.cleanX = true
		return t.buildDraft(e, ev.bytes)
	case prepCreateKEM:
		genHash, err := ev.state.Hash()
		if err != nil {
			return err
		}
		t.recoveryCode = EncodeRecoveryCode(t.recSeed, genHash)
		zeroBytes(t.recSeed[:])
		genBytes, err := ev.state.MarshalBinary()
		if err != nil {
			return err
		}
		t.stage = prepCreateClaim
		op := e.newPreparedOp(t.server, t.memberID, "create", "", devicesec.Hash{})
		op.Signed, op.StoragePersisted = t.regReqBytes, t.storagePersisted
		op.Create = &PersistedCreateDraft{
			AccountPublicKey: t.acctPub, EdSlot: t.edSlot, XSlot: t.xSlot, StoragePersisted: t.storagePersisted, States: [][]byte{genBytes},
		}
		e.claimOperationCAS(op, nil, t.memberID, parentTarget(t, 0))
	case prepCreateClaim:
		wf.server = t.server
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{RecoveryCode: t.recoveryCode, Flag: t.storagePersisted})
	}
	return nil
}

type continueCreationTask struct {
	taskBase
	setupToken, edSlot, tok string
	op                      *PersistedOperation
	baseReq                 v1.RegisterDeviceRequest
	isPowRetry              bool
	regReq, leaseBytes      []byte
	genesis                 devicesec.State
}

const (
	contCreateInitLogin uint8 = iota
	contCreateSaveRetry
	contCreateRegRPC
	contCreatePowChal
	contCreatePostRegLogin
	contCreatePin
	contCreateLease
	contCreateSaveLease
	contCreatePutLease
	contCreateSavePub
)

func (t *continueCreationTask) startRegisterAttempt(e *Engine, curOp *PersistedOperation, proof *v1.SignupProof, isPow bool) {
	req := t.baseReq
	req.SetupToken, req.SignupProof = t.setupToken, proof
	reqBytes, _ := (&req).Marshal()
	next := *curOp
	next.Retry, t.isPowRetry, t.regReq, t.stage = reqBytes, isPow, reqBytes, contCreateSaveRetry
	e.saveOperationCAS(curOp, &next, parentTarget(t, 0))
}

func (t *continueCreationTask) finishWithToken(e *Engine, tok string) error {
	genesisBytes := t.op.Create.States[0]
	genesis, err := devicesec.ParseState(genesisBytes)
	if err != nil {
		return err
	}
	t.tok, t.genesis, t.stage = tok, genesis, contCreatePin
	e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PIN_SECURITY_DOMAIN, t.op.Server, tok, &v1.PinSecurityDomainRequest{GenesisState: genesisBytes}, t)
	return nil
}

func (t *continueCreationTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	wf := e.ensureWF()
	if ev.kind == evStart {
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		op := wf.pendingOp
		if op == nil || op.Kind != "create" || op.Create == nil || len(op.Create.States) == 0 || len(op.Signed) == 0 {
			return devicesec.ErrStale
		}
		if op.Phase == "PUBLISHED" {
			if op.CodeSaved {
				e.startAdoptCreationOp(op, t.target)
				return nil
			}
			e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{NeedsRecoveryRotation: true})
			return nil
		}
		if t.baseReq.Unmarshal(op.Signed) != nil {
			return devicesec.ErrInvalid
		}
		if t.setupToken == "" && len(op.Retry) > 0 {
			var prevRetry v1.RegisterDeviceRequest
			if prevRetry.Unmarshal(op.Retry) == nil {
				t.setupToken = prevRetry.SetupToken
			}
		}
		t.op, t.edSlot, t.stage = op, op.Create.EdSlot, contCreateInitLogin
		e.forceLoginToken(op.Server, keyCandEd, t.edSlot, op.MemberID, true, t.fgGen, false, parentTarget(t, 0))
		return nil
	}
	if ev != nil && ev.err != nil && t.stage != contCreateInitLogin && t.stage != contCreateRegRPC {
		return ev.err
	}
	switch t.stage {
	case contCreateInitLogin:
		if ev.err == nil {
			return t.finishWithToken(e, ev.str)
		}
		re := asRpcError(ev.err)
		if re == nil || (re.code != connectCodePermissionDenied && re.code != connectCodeNotFound && re.code != connectCodeUnauthenticated) {
			return ev.err
		}
		t.startRegisterAttempt(e, t.op, nil, false)
	case contCreateSaveRetry:
		t.op, t.stage = ev.op, contCreateRegRPC
		e.waitRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_REGISTER_DEVICE, ev.op.Server, "", t.regReq, t, 0)
	case contCreateRegRPC:
		if ev.rpcErr == nil {
			if ev.err != nil {
				return ev.err
			}
			var resp v1.RegisterDeviceResponse
			if resp.Unmarshal(ev.bytes) != nil || resp.DeviceId != t.op.MemberID {
				return devicesec.ErrUntrusted
			}
			t.stage = contCreatePostRegLogin
			e.forceLoginToken(t.op.Server, keyCandEd, t.edSlot, t.op.MemberID, true, t.fgGen, false, parentTarget(t, 0))
			return nil
		}
		if t.isPowRetry || ev.rpcErr.code != connectCodeFailedPrecondition {
			return ev.rpcErr
		}
		t.stage = contCreatePowChal
		e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SIGNUP_CHALLENGE, t.op.Server, "", &v1.GetSignupChallengeRequest{}, t)
	case contCreatePowChal:
		var sc v1.GetSignupChallengeResponse
		if sc.Unmarshal(ev.bytes) != nil || len(sc.Challenge) == 0 || sc.DifficultyBits > 24 || (e.lastNowMs > 0 && sc.ExpiresMs <= e.lastNowMs) {
			return newWfError("failed_precondition", text.KeySecurityRegistrationProofOfWorkIsInvalidOrExpired)
		}
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		nonce, ok := solveSignupPoW(sc.Challenge, sc.DifficultyBits)
		if !ok {
			return newWfError("failed_precondition", text.KeySecurityRegistrationProofOfWorkExpiredRetryTheOriginalDraft)
		}
		t.startRegisterAttempt(e, wf.pendingOp, &v1.SignupProof{Challenge: sc.Challenge, Nonce: nonce}, true)
	case contCreatePostRegLogin:
		return t.finishWithToken(e, ev.str)
	case contCreatePin:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		t.stage = contCreateLease
		e.buildAndSignLease(t.genesis, keyCandEd, t.edSlot, max(uint64(max(1, e.lastNowMs)), t.genesis.Core.IssuedAt), parentTarget(t, 0))
	case contCreateLease:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		lb, _ := ev.lease.MarshalBinary()
		curPending := wf.pendingOp
		nextDraft := *curPending.Create
		nextDraft.Lease = lb
		nextOp := *curPending
		nextOp.Create, t.leaseBytes, t.stage = &nextDraft, lb, contCreateSaveLease
		e.saveOperationCAS(curPending, &nextOp, parentTarget(t, 0))
	case contCreateSaveLease:
		t.op, t.stage = ev.op, contCreatePutLease
		e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE, ev.op.Server, t.tok, &v1.PutSecurityStateRequest{Lease: t.leaseBytes}, t)
	case contCreatePutLease:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		pubOp := *t.op
		pubOp.Phase, t.stage = "PUBLISHED", contCreateSavePub
		e.saveOperationCAS(t.op, &pubOp, parentTarget(t, 0))
	case contCreateSavePub:
		pubSaved := ev.op
		wf.cachedToken = t.tok
		e.emitTokenUpdated(t.tok, 0, false)
		if pubSaved.CodeSaved {
			e.startAdoptCreationOp(pubSaved, t.target)
			return nil
		}
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{NeedsRecoveryRotation: true})
	}
	return nil
}

func solveSignupPoW(challenge []byte, difficultyBits uint32) ([]byte, bool) {
	if len(challenge) == 0 || difficultyBits > 24 {
		return nil, false
	}
	var nonce [8]byte
	buf := make([]byte, len(challenge)+8)
	copy(buf, challenge)
	for n := uint64(0); n < 1<<28; n++ {
		for i := range 8 {
			nonce[i] = byte(n >> (56 - 8*i))
		}
		copy(buf[len(challenge):], nonce[:])
		sum := sha256.Sum256(buf)
		ok := true
		for bit := range difficultyBits {
			if (sum[bit>>3] & (128 >> (bit & 7))) != 0 {
				ok = false
				break
			}
		}
		if ok {
			return nonce[:], true
		}
	}
	return nil, false
}

type adoptCreationWithoutCodeTask struct{ taskBase }

func (t *adoptCreationWithoutCodeTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	if err := e.checkFG(t.fgGen); err != nil {
		return err
	}
	op := e.ensureWF().pendingOp
	if op == nil || op.Kind != "create" || op.Phase != "PUBLISHED" {
		return devicesec.ErrStale
	}
	e.startAdoptCreationOp(op, t.target)
	return nil
}

const (
	adoptCommit uint8 = iota
	adoptApply
)

type adoptCreationTask struct {
	taskBase
	op      *PersistedOperation
	genesis devicesec.State
	pin     devicesec.Pin
	prof    *PersistedPublicProfile
	rec     *PersistedRecord
	rev     string
}

func (e *Engine) startAdoptCreationOp(op *PersistedOperation, target taskTarget) {
	wf := e.ensureWF()
	draft := op.Create
	if draft == nil || len(draft.States) == 0 || len(draft.AccountPublicKey) != 32 {
		e.failTarget(target, devicesec.ErrInvalid)
		return
	}
	genesis, err := devicesec.ParseState(draft.States[0])
	if err != nil {
		e.failTarget(target, err)
		return
	}
	pin, err := genesis.Pin()
	if err != nil {
		e.failTarget(target, err)
		return
	}
	var acctPub [32]byte
	copy(acctPub[:], draft.AccountPublicKey)
	prof := &PersistedPublicProfile{Server: op.Server, Pin: pin, AccountPublicKey: acctPub}
	profBytes, err := prof.Encode()
	if err != nil {
		e.failTarget(target, err)
		return
	}
	rec := &PersistedRecord{
		Pin: pin, MemberID: op.MemberID, Member: true, Server: op.Server, AccountPublicKey: slices.Clone(acctPub[:]),
		States: draft.States, RecoveryProofs: make([][]byte, len(draft.States)), Lease: draft.Lease, FloorMs: e.lastNowMs, NeedsRecoveryRotation: !op.CodeSaved,
	}
	recBytes, err := rec.Encode()
	if err != nil {
		e.failTarget(target, err)
		return
	}
	rev, opRev := e.nextWorkflowToken(), u64Str(op.Revision)
	if wf.pendingOpRev != "" {
		opRev = wf.pendingOpRev
	}
	writes := buildAccountCommitWrites(op.Server, pin, profBytes, recBytes, true, "", rev, opRev, true, "", "")
	e.batchCommitStorage(draft.EdSlot, draft.XSlot, []string{draft.EdSlot, draft.XSlot}, false, writes, &adoptCreationTask{
		taskBase: taskBase{target: target}, op: op, genesis: genesis, pin: pin, prof: prof, rec: rec, rev: rev,
	})
}

func (t *adoptCreationTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	switch t.stage {
	case adoptCommit:
		var fallbackEd, fallbackX []byte
		if m, ok := t.genesis.Core.Member(t.op.MemberID); ok {
			fallbackEd, fallbackX = m.EdPublic[:], m.XPublic[:]
		}
		e.installCommittedAccount(t.op.Server, t.rev, t.rec, t.prof, ev.storage, fallbackEd, fallbackX)
		t.stage = adoptApply
		e.applyPersistedRecordToEngine(t.rec, parentTarget(t, 0))
	case adoptApply:
		e.fireNotifyRecord(t.rec, t.genesis, t.rev)
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{
			CodeSaved: t.op.CodeSaved, NeedsRecoveryRotation: !t.op.CodeSaved,
			AccountAdopted: &corev1.SecurityAccountAdopted{Server: t.op.Server, DeviceId: t.op.MemberID, AccountId: t.pin.Account, NeedsRecoveryRotation: !t.op.CodeSaved},
		})
	}
	return nil
}

const (
	removeDevPre uint8 = iota
	removeDevSyncOp
	removeDevClearOp
	removeDevSync
	removeDevClaim
)

type removeDeviceTask struct {
	taskBase
	id                  string
	abandonPairDelivery bool
}

func (t *removeDeviceTask) proceedAfterPre(e *Engine) error {
	if e.ensureWF().pendingOp != nil {
		return newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst)
	}
	t.stage = removeDevSync
	e.syncStates(false, "", parentTarget(t, 0))
	return nil
}

func (t *removeDeviceTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case removeDevPre:
		if ev.kind == evStart {
			if err := e.checkFG(t.fgGen); err != nil {
				return err
			}
			if pending := wf.pendingOp; pending != nil && t.abandonPairDelivery && pending.Pair != nil && pending.Pair.CandidateID == t.id {
				t.stage = removeDevSyncOp
				e.syncStates(false, "", parentTarget(t, 0))
				return nil
			}
			e.startResumePendingOp(t.fgGen, parentTarget(t, 0))
			return nil
		}
		return t.proceedAfterPre(e)
	case removeDevSyncOp:
		pending := wf.pendingOp
		if pending == nil {
			return t.proceedAfterPre(e)
		}
		var orig v1.PutSecurityStateRequest
		if orig.Unmarshal(pending.Signed) != nil || len(orig.States) == 0 {
			return devicesec.ErrStale
		}
		ts, err := devicesec.ParseState(orig.States[len(orig.States)-1].State)
		if err != nil {
			return err
		}
		th, _ := ts.Hash()
		if _, ok := e.byHash[th]; !ok {
			return devicesec.ErrStale
		}
		t.stage = removeDevClearOp
		e.clearOperationCAS(pending, parentTarget(t, 0))
	case removeDevClearOp:
		return t.proceedAfterPre(e)
	case removeDevSync:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if !e.isAuthority() {
			return devicesec.ErrUntrusted
		}
		if t.id == e.selfDeviceID {
			return newWfError("invalid", text.KeySecurityTheManagingClientCannotBeRemovedDirectlyTransferManagement)
		}
		if _, ok := e.head.Core.Member(t.id); !ok {
			e.okTarget(t.target, nil)
			return nil
		}
		hh := e.headHash
		t.stage = removeDevClaim
		op := e.newPreparedOp(e.effectiveServer(), e.selfDeviceID, "state", "remove", hh)
		op.TargetDeviceID = t.id
		e.claimOperationCAS(op, &hh, e.selfDeviceID, parentTarget(t, 0))
	case removeDevClaim:
		e.startResumePendingOp(t.fgGen, t.target)
	}
	return nil
}

const (
	delMachPre uint8 = iota
	delMachSync
	delMachClaim
)

type deleteMachineRecordsTask struct {
	taskBase
	machineID string
}

func (t *deleteMachineRecordsTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case delMachPre:
		if ev.kind == evStart {
			e.startResumePendingOp(t.fgGen, parentTarget(t, 0))
			return nil
		}
		t.stage = delMachSync
		e.syncStates(false, "", parentTarget(t, 0))
	case delMachSync:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if !e.isAuthority() {
			return devicesec.ErrUntrusted
		}
		if _, live := e.head.Core.Member(t.machineID); live {
			return devicesec.ErrUntrusted
		}
		var wasMachine bool
		for _, s := range e.states {
			if m, ok := s.Core.Member(t.machineID); ok && m.Kind == devicesec.Machine {
				wasMachine = true
				break
			}
		}
		if !wasMachine {
			return devicesec.ErrUntrusted
		}
		if wf.pendingOp != nil {
			return newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst)
		}
		hh := e.headHash
		t.stage = delMachClaim
		op := e.newPreparedOp(e.effectiveServer(), e.selfDeviceID, "delete-records", "", hh)
		op.TargetDeviceID = t.machineID
		op.Signed = (&v1.DeleteMachineRecordsRequest{MachineId: t.machineID}).AppendMarshal(nil)
		e.claimOperationCAS(op, &hh, e.selfDeviceID, parentTarget(t, 0))
	case delMachClaim:
		e.startResumePendingOp(t.fgGen, t.target)
	}
	return nil
}

const (
	delAcctSync uint8 = iota
	delAcctChallenge
	delAcctSign
	delAcctClear
)

type deleteAccountTask struct {
	taskBase
	server    string
	head      devicesec.Hash
	challenge []byte
}

func (t *deleteAccountTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case delAcctSync:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if ev.kind == evStart {
			if wf.pendingOp != nil {
				return newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst)
			}
			e.syncStates(false, "", parentTarget(t, 0))
			return nil
		}
		if !e.isAuthority() {
			return devicesec.ErrUntrusted
		}
		t.head, t.server, t.stage = e.headHash, e.effectiveServer(), delAcctChallenge
		e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_CHALLENGE, t.server, t.fgGen, &v1.GetChallengeRequest{DeviceId: e.selfDeviceID}, parentTarget(t, 0))
	case delAcctChallenge:
		var chal v1.GetChallengeResponse
		if chal.Unmarshal(ev.bytes) != nil || len(chal.Challenge) != 32 {
			return devicesec.ErrInvalid
		}
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if !e.isAuthority() || e.headHash != t.head || wf.pendingOp != nil {
			return devicesec.ErrStale
		}
		t.challenge, t.stage = chal.Challenge, delAcctSign
		msg := make([]byte, 0, len(deleteAccountPrefix)+len(e.pin.Account)+1+32+len(chal.Challenge))
		msg = append(append(append(append(msg, deleteAccountPrefix...), e.pin.Account...), 0), t.head[:]...)
		msg = append(msg, chal.Challenge...)
		e.waitSign(keyDevEd, "", msg, t, 0)
	case delAcctSign:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		t.stage = delAcctClear
		e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_DELETE_ACCOUNT, t.server, t.fgGen, &v1.DeleteAccountRequest{
			StateHash: t.head[:], Challenge: t.challenge, Signature: ev.bytes,
		}, parentTarget(t, 0))
	case delAcctClear:
		e.startClearLocalSecurity(t.target, false)
	}
	return nil
}

const (
	prepCodePre uint8 = iota
	prepCodeSync
	prepCodeEntropy
)

type prepareCodeTask struct {
	taskBase
	kind             string // "state" or "offer"
	storagePersisted bool
}

func (t *prepareCodeTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case prepCodePre:
		if ev.kind == evStart {
			if t.kind == "offer" {
				if err := e.checkFG(t.fgGen); err != nil {
					return err
				}
				if wf.pendingOp != nil {
					return newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst)
				}
				t.stage = prepCodeSync
				e.syncStates(false, "", parentTarget(t, 0))
				return nil
			}
			e.startResumePendingOp(t.fgGen, parentTarget(t, 0))
			return nil
		}
		t.stage = prepCodeSync
		e.syncStates(false, "", parentTarget(t, 0))
	case prepCodeSync:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if t.kind == "state" {
			if !e.isAuthority() {
				return devicesec.ErrUntrusted
			}
			if wf.pendingOp != nil {
				return newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst)
			}
		} else if !e.isMember || e.isAuthority() {
			return devicesec.ErrUntrusted
		}
		t.stage = prepCodeEntropy
		e.waitEntropy(64, t, 0)
	case prepCodeEntropy:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		var seed [32]byte
		if !e.readRandom(seed[:]) {
			return ErrNeedEntropy
		}
		pub, err := devicesec.Ed25519PublicFromSeed(seed[:])
		if err != nil {
			zeroBytes(seed[:])
			return err
		}
		code := EncodeRecoveryCode(seed, e.pin.Genesis)
		zeroBytes(seed[:])
		wf.codeDraft = &codeDraft{kind: t.kind, parent: e.headHash, recoveryEd: pub, storagePersisted: t.storagePersisted}
		res := &corev1.SecurityWorkflowResultEffect{RecoveryCode: code}
		if t.kind == "offer" {
			res.Flag = t.storagePersisted
		}
		e.okTarget(t.target, res)
	}
	return nil
}

const (
	confirmCodeSaveOp uint8 = iota
	confirmCodeSync
	confirmCodeClaim
)

type confirmCodeTask struct {
	taskBase
	kind  string
	draft *codeDraft
}

func (t *confirmCodeTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	wf := e.ensureWF()
	if ev.kind == evStart {
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if op := wf.pendingOp; op != nil {
			if op.Kind != t.kind || (t.kind == "state" && op.Action != "recovery") || op.Server != e.effectiveServer() || op.MemberID != e.selfDeviceID {
				return devicesec.ErrStale
			}
			if !op.CodeSaved {
				next := *op
				next.CodeSaved, t.stage = true, confirmCodeSaveOp
				e.saveOperationCAS(op, &next, parentTarget(t, 0))
				return nil
			}
			e.startResumePendingOp(t.fgGen, t.target)
			return nil
		}
		draft := wf.codeDraft
		if draft == nil || draft.kind != t.kind {
			return devicesec.ErrStale
		}
		t.draft, t.stage = draft, confirmCodeSync
		e.syncStates(false, "", parentTarget(t, 0))
		return nil
	}
	switch t.stage {
	case confirmCodeSaveOp:
		e.startResumePendingOp(t.fgGen, t.target)
	case confirmCodeSync:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if t.kind == "state" {
			if !e.isAuthority() || e.headHash != t.draft.parent {
				return devicesec.ErrStale
			}
		} else if !e.isMember || e.isAuthority() {
			return devicesec.ErrUntrusted
		}
		hh := e.headHash
		op := e.newPreparedOp(e.effectiveServer(), e.selfDeviceID, t.kind, "", hh)
		op.CodeSaved, op.RecoveryEd, op.StoragePersisted = true, slices.Clone(t.draft.recoveryEd), t.draft.storagePersisted
		if t.kind == "state" {
			op.Action = "recovery"
		}
		t.stage = confirmCodeClaim
		e.claimOperationCAS(op, &hh, e.selfDeviceID, parentTarget(t, 0))
	case confirmCodeClaim:
		wf.codeDraft = nil
		e.startResumePendingOp(t.fgGen, t.target)
	}
	return nil
}

func (e *Engine) verifiedHandoffCandidates(devices []DeviceOfferInfo) []*corev1.SecurityHandoffCandidate {
	if !e.hasHead {
		return nil
	}
	var out []*corev1.SecurityHandoffCandidate
	for _, d := range devices {
		ed := d.RecoveryEd
		if len(ed) != 32 && d.Meta != nil && len(d.Meta.HandoffRecoveryEd) == 32 {
			ed = d.Meta.HandoffRecoveryEd
		}
		if len(ed) != 32 || d.ID == e.selfDeviceID {
			continue
		}
		if m, ok := e.head.Core.Member(d.ID); ok && m.Kind == devicesec.Client {
			out = append(out, &corev1.SecurityHandoffCandidate{DeviceId: d.ID, RecoveryEd: slices.Clone(ed)})
		}
	}
	return out
}

const (
	listHandoffRPC uint8 = iota
	listHandoffOpen
)

type listHandoffCandidatesTask struct {
	actionID uint64
	devIDs   []string
	stage    uint8
}

func (t *listHandoffCandidatesTask) target() taskTarget {
	return actionTarget(t.actionID, targetAction)
}

func (t *listHandoffCandidatesTask) onError(e *Engine, err error) {
	e.failTarget(t.target(), err)
}

func (e *Engine) listHandoffCandidatesOp(actionID uint64) {
	if !e.isAuthority() {
		e.emitActionResult(actionID, nil, devicesec.ErrUntrusted)
		return
	}
	e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES, e.effectiveServer(), 0, &v1.ListDevicesRequest{}, parentTarget(&listHandoffCandidatesTask{actionID: actionID}, 0))
}

func (t *listHandoffCandidatesTask) step(e *Engine, ev *secEvent) error {
	wf := e.ensureWF()
	switch t.stage {
	case listHandoffRPC:
		if ev.err != nil {
			e.okTarget(t.target(), &corev1.SecurityWorkflowResultEffect{HandoffCandidates: e.verifiedHandoffCandidates(wf.devicesSnap)})
			return nil
		}
		var lr v1.ListDevicesResponse
		if lr.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		items, devIDs := make([]OpenItem, 0, len(lr.Devices)), make([]string, 0, len(lr.Devices))
		for _, d := range lr.Devices {
			if d != nil && d.Id != "" && d.Meta != nil && len(d.Meta.SecurityHeader) > 0 {
				items, devIDs = append(items, openDeviceMetaItem(d.Id, d.Meta)), append(devIDs, d.Id)
			}
		}
		if len(items) == 0 {
			e.okTarget(t.target(), &corev1.SecurityWorkflowResultEffect{})
			return nil
		}
		t.devIDs, t.stage = devIDs, listHandoffOpen
		e.startOpenBatchInternal(e.lastNowMs, items, parentTarget(t, 0))
	case listHandoffOpen:
		var infos []DeviceOfferInfo
		for i, r := range ev.opened {
			if r.Err != nil {
				continue
			}
			if dm, ok := r.Body.(*v1.DeviceMeta); ok && dm != nil {
				infos = append(infos, DeviceOfferInfo{ID: t.devIDs[i], RecoveryEd: slices.Clone(dm.HandoffRecoveryEd), Meta: dm})
			}
		}
		wf.devicesSnap = infos
		e.okTarget(t.target(), &corev1.SecurityWorkflowResultEffect{HandoffCandidates: e.verifiedHandoffCandidates(infos)})
	}
	return nil
}

const (
	handoffPre uint8 = iota
	handoffSync
	handoffList
	handoffOpenMeta
	handoffClaim
)

type handoffAuthorityTask struct {
	taskBase
	incomingID string
}

func (t *handoffAuthorityTask) proceed(e *Engine, list []*corev1.SecurityHandoffCandidate) error {
	var offer *corev1.SecurityHandoffCandidate
	for _, c := range list {
		if c.DeviceId == t.incomingID {
			offer = c
			break
		}
	}
	if offer == nil || len(offer.RecoveryEd) != 32 {
		return newWfError("invalid", text.KeySecurityGenerateAndSaveARecoveryCodeOnTheSuccessor)
	}
	if e.ensureWF().pendingOp != nil {
		return newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst)
	}
	hh := e.headHash
	t.stage = handoffClaim
	op := e.newPreparedOp(e.effectiveServer(), e.selfDeviceID, "state", "handoff", hh)
	op.TargetDeviceID, op.RecoveryEd = t.incomingID, slices.Clone(offer.RecoveryEd)
	e.claimOperationCAS(op, &hh, e.selfDeviceID, parentTarget(t, 0))
	return nil
}

func (t *handoffAuthorityTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil && t.stage != handoffList {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case handoffPre:
		if ev.kind == evStart {
			e.startResumePendingOp(t.fgGen, parentTarget(t, 0))
			return nil
		}
		t.stage = handoffSync
		e.syncStates(false, "", parentTarget(t, 0))
	case handoffSync:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if !e.isAuthority() {
			return devicesec.ErrUntrusted
		}
		t.stage = handoffList
		e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES, e.effectiveServer(), t.fgGen, &v1.ListDevicesRequest{}, parentTarget(t, 0))
	case handoffList:
		if ev.err != nil {
			return t.proceed(e, e.verifiedHandoffCandidates(wf.devicesSnap))
		}
		var lr v1.ListDevicesResponse
		if lr.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		d := findDeviceByID(lr.Devices, t.incomingID)
		if d == nil || d.Meta == nil || len(d.Meta.SecurityHeader) == 0 {
			return t.proceed(e, e.verifiedHandoffCandidates(wf.devicesSnap))
		}
		t.stage = handoffOpenMeta
		e.startOpenBatchInternal(e.lastNowMs, []OpenItem{openDeviceMetaItem(d.Id, d.Meta)}, parentTarget(t, 0))
	case handoffOpenMeta:
		if len(ev.opened) > 0 && ev.opened[0].Err == nil {
			if dm, ok := ev.opened[0].Body.(*v1.DeviceMeta); ok && dm != nil {
				return t.proceed(e, e.verifiedHandoffCandidates([]DeviceOfferInfo{{ID: t.incomingID, RecoveryEd: dm.HandoffRecoveryEd, Meta: dm}}))
			}
		}
		return t.proceed(e, nil)
	case handoffClaim:
		e.startResumePendingOp(t.fgGen, t.target)
	}
	return nil
}

const (
	metaUpdateList uint8 = iota
	metaUpdateOpen
	metaUpdateEntropy
	metaUpdateSeal
	metaUpdatePut
)

type updateDeviceMetaTask struct {
	actionID uint64
	act      *corev1.SecurityUpdateDeviceMetaAction
	server   string
	attempt  int
	rev      uint64
	plain    []byte
	stage    uint8
}

func (t *updateDeviceMetaTask) target() taskTarget { return actionTarget(t.actionID, targetAction) }

func (t *updateDeviceMetaTask) onError(e *Engine, err error) {
	e.failTarget(t.target(), err)
}

func (e *Engine) updateDeviceMetaOp(actionID uint64, act *corev1.SecurityUpdateDeviceMetaAction) {
	if act == nil {
		e.emitActionResult(actionID, nil, devicesec.ErrInvalid)
		return
	}
	targetID := act.DeviceId
	if targetID == "" {
		targetID = e.selfDeviceID
	}
	if targetID == "" || targetID != e.selfDeviceID {
		e.emitActionResult(actionID, nil, newWfError("invalid", text.KeyErrorsNamesCanOnlyBeChangedOnThatDevice))
		return
	}
	server := e.effectiveServer()
	if server == "" || !e.loaded || !e.isMember {
		e.emitActionResult(actionID, nil, ErrNotMember)
		return
	}
	if err := (&updateDeviceMetaTask{actionID: actionID, act: act, server: server}).tryCAS(e); err != nil {
		e.emitActionResult(actionID, nil, err)
	}
}

func (t *updateDeviceMetaTask) tryCAS(e *Engine) error {
	if t.attempt >= 5 {
		return devicesec.ErrStale
	}
	t.stage = metaUpdateList
	e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES, t.server, 0, &v1.ListDevicesRequest{}, parentTarget(t, 0))
	return nil
}

func (t *updateDeviceMetaTask) applyUpdate(e *Engine, cur *v1.DeviceMeta) error {
	nextMeta := &v1.DeviceMeta{}
	if cur != nil {
		nextMeta = cur.Clone()
	}
	if !t.act.DescribeSelfOnly {
		nextMeta.Name = t.act.Name
	}
	if t.act.Platform != "" && (t.act.DescribeSelfOnly || nextMeta.Platform == "") {
		nextMeta.Platform = t.act.Platform
	}
	if nextMeta.Description == "" && t.act.Description != "" {
		nextMeta.Description = t.act.Description
	}
	plain, err := nextMeta.Marshal()
	if err != nil {
		return err
	}
	t.plain, t.stage = plain, metaUpdateEntropy
	e.waitEntropy(64, t, 0)
	return nil
}

func (t *updateDeviceMetaTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	switch t.stage {
	case metaUpdateList:
		var lr v1.ListDevicesResponse
		if lr.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		own := findDeviceByID(lr.Devices, e.selfDeviceID)
		t.rev = 0
		if own != nil {
			t.rev = own.MetaRevision
			if own.Meta != nil && t.rev > 0 {
				t.stage = metaUpdateOpen
				e.startOpenBatchInternal(e.lastNowMs, []OpenItem{openDeviceMetaItem(e.selfDeviceID, own.Meta)}, parentTarget(t, 0))
				return nil
			}
		}
		return t.applyUpdate(e, nil)
	case metaUpdateOpen:
		if ev.opened[0].Err != nil {
			return ev.opened[0].Err
		}
		dm, _ := ev.opened[0].Body.(*v1.DeviceMeta)
		return t.applyUpdate(e, dm)
	case metaUpdateEntropy:
		t.stage = metaUpdateSeal
		e.startSealEnvelopeInternal(e.lastNowMs, devicesec.DeviceMeta, "", "", e.selfDeviceID, t.rev+1, 0, t.plain, false, nil, parentTarget(t, 0))
	case metaUpdateSeal:
		t.stage = metaUpdatePut
		e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_DEVICE_META, t.server, 0, &v1.PutDeviceMetaRequest{
			DeviceId: e.selfDeviceID, ExpectedRevision: t.rev, Meta: ev.seal.Envelope,
		}, parentTarget(t, 0))
	case metaUpdatePut:
		var pr v1.PutDeviceMetaResponse
		if pr.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		if pr.Applied {
			e.okTarget(t.target(), nil)
			return nil
		}
		t.attempt++
		return t.tryCAS(e)
	}
	return nil
}

const (
	completeProfFetch uint8 = iota
	completeProfCommit
)

type completeAccountPublicProfileTask struct {
	taskBase
	rawServer, code, server string
	pin                     devicesec.Pin
	prof                    *PersistedPublicProfile
	nextRec                 *PersistedRecord
	nextRev                 string
}

func (t *completeAccountPublicProfileTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case completeProfFetch:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if ev.kind == evStart {
			server := e.effectiveServer()
			if t.rawServer != "" {
				srv, err := CheckServerURL(t.rawServer)
				if err != nil {
					return err
				}
				server = srv
			}
			if !e.loaded || !e.hasHead || wf.record == nil {
				return ErrNotInitialized
			}
			seed, genPrefix, ok := DecodeRecoveryCode(t.code)
			if !ok {
				return newWfError("invalid", errInvalidRecoveryCode.Error())
			}
			recPub, err := devicesec.Ed25519PublicFromSeed(seed[:])
			if err != nil {
				zeroBytes(seed[:])
				return err
			}
			t.server = server
			e.startFetchRecoveryCtxWithSeed(server, recPub, genPrefix, wf.record, t.fgGen, seed, parentTarget(t, 0))
			zeroBytes(seed[:])
			return nil
		}
		rc, cur := ev.recCtx, wf.record
		if cur == nil || cur.Pin != rc.pin {
			return devicesec.ErrStale
		}
		prof := &PersistedPublicProfile{Server: t.server, Pin: rc.pin, AccountPublicKey: rc.acctPub}
		profBytes, err := prof.Encode()
		if err != nil {
			return err
		}
		nextRec := cur.Clone()
		nextRec.AccountPublicKey = slices.Clone(rc.acctPub[:])
		recBytes, err := nextRec.Encode()
		if err != nil {
			return err
		}
		nextRev := e.nextWorkflowToken()
		t.pin, t.prof, t.nextRec, t.nextRev, t.stage = rc.pin, prof, nextRec, nextRev, completeProfCommit
		e.batchCommitStorage("", "", nil, false, buildAccountCommitWrites(t.server, rc.pin, profBytes, recBytes, false, wf.recordRev, nextRev, "", false, "", ""), t)
	case completeProfCommit:
		wf.record, wf.recordRev = t.nextRec, t.nextRev
		wf.publicProfiles[PublicProfileStorageKey(t.server, t.pin)] = t.prof
		e.okTarget(t.target, nil)
	}
	return nil
}

type checkRetainedDeniedTask struct {
	actionID uint64
	server   string
	retries  int
}

func (e *Engine) checkRetainedDeniedOp(actionID uint64) {
	server := e.effectiveServer()
	if server == "" || e.selfDeviceID == "" {
		e.emitActionResult(actionID, &corev1.SecurityWorkflowResultEffect{Flag: false}, nil)
		return
	}
	e.forceLoginToken(server, keyDevEd, "", e.selfDeviceID, false, 0, false, parentTarget(&checkRetainedDeniedTask{actionID: actionID, server: server}, 0))
}

func (t *checkRetainedDeniedTask) step(e *Engine, ev *secEvent) error {
	var denied bool
	if ev.err != nil {
		if re := asRpcError(ev.err); re != nil && re.code == connectCodePermissionDenied {
			if t.retries == 0 {
				t.retries = 1
				e.forceLoginToken(t.server, keyDevEd, "", e.selfDeviceID, false, 0, false, parentTarget(t, 0))
				return nil
			}
			denied = true
		}
	}
	e.okTarget(actionTarget(t.actionID, targetAction), &corev1.SecurityWorkflowResultEffect{Flag: denied})
	return nil
}

func (e *Engine) checkExitAdviceOp(actionID uint64) {
	wf := e.ensureWF()
	unresolved, auth := wf.pendingOp != nil || wf.regIntent != nil, e.isAuthority()
	msg := text.KeySecurityExitingDeletesLocalKeysYouWillNeedToPair
	if unresolved {
		msg = text.KeySecurityTheSecurityOperationSOutcomeIsUnverifiedContinueThe
	} else if auth {
		msg = text.KeySecurityThisBrowserIsTheOnlyManagingClientTransferManagement
	}
	e.emitActionResult(actionID, &corev1.SecurityWorkflowResultEffect{
		ExitAdvice: &corev1.SecurityExitAdvice{Authority: auth, Unresolved: unresolved, Message: msg},
	}, nil)
}

type clearLocalSecurityTask struct {
	taskBase
	statusOnOK bool
}

func (e *Engine) startClearLocalSecurity(target taskTarget, statusOnOK bool) {
	e.resetWorkflowPairingSessions()
	e.waitStorage(&corev1.SecurityStorageEffect{
		Op: corev1.SecurityStorageOp_SECURITY_STORAGE_OP_CLEAR_ALL, ClearLoginToken: true,
	}, &clearLocalSecurityTask{taskBase: taskBase{target: target}, statusOnOK: statusOnOK}, 0)
}

func (t *clearLocalSecurityTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	wf := e.ensureWF()
	e.resetChainState()
	e.loaded, e.isMember, e.removed, e.cleared, e.securityLocked = false, false, false, true, false
	wf.record, wf.recordRev, wf.pendingOp, wf.pendingOpRev, wf.regIntent, wf.regIntentRev, wf.codeDraft = nil, "", nil, "", nil, "", nil
	wf.cachedToken, wf.tokenExpiresMs = "", 0
	wf.deviceEdPresent, wf.deviceEdPub, wf.deviceXPresent, wf.deviceXPub = false, nil, false, nil
	clear(wf.publicProfiles)
	e.emitTokenUpdated("", 0, true)
	e.notifyStateChanged()
	e.okTarget(t.target, nil)
	if t.statusOnOK {
		e.emitStatusChanged()
	}
	return nil
}
