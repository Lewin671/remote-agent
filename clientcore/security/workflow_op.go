package security

import (
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

func storageConflict(ev *secEvent) bool {
	return ev.storage != nil && ev.storage.conflict
}

func findDeviceByID(devices []*v1.Device, id string) *v1.Device {
	for _, d := range devices {
		if d != nil && d.Id == id {
			return d
		}
	}
	return nil
}

func openDeviceMetaItem(id string, env *v1.Envelope) OpenItem {
	return OpenItem{
		Env:             env,
		ExpectedPurpose: devicesec.DeviceMeta,
		RequestID:       id,
		History:         true,
	}
}

func (p *kemBuildParams) zeroSecrets() {
	zeroBytes(p.oldSecret[:])
	zeroBytes(p.newSecret[:])
}

const (
	opCASClaimRead uint8 = iota
	opCASClaimWrite
	opCASSaveWrite
	opCASClearWrite
)

type opCASTask struct {
	taskBase
	op               *PersistedOperation
	next             *PersistedOperation
	nextRev          string
	expectedHead     *devicesec.Hash
	expectedMemberID string
}

//go:noinline
func (e *Engine) claimOperationCAS(op *PersistedOperation, expectedHead *devicesec.Hash, expectedMemberID string, target taskTarget) {
	if e.ensureWF().regIntent != nil {
		e.failTarget(target, devicesec.ErrStale)
		return
	}
	e.readBootstrapStorage(&opCASTask{
		taskBase: taskBase{target: target, stage: opCASClaimRead}, op: op, expectedHead: expectedHead, expectedMemberID: expectedMemberID,
	})
}

//go:noinline
func (e *Engine) saveOperationCAS(prev, next *PersistedOperation, target taskTarget) {
	wf := e.ensureWF()
	cp := *next
	cp.Revision = prev.Revision + 1
	if err := prev.ValidateProgress(&cp); err != nil {
		e.failTarget(target, devicesec.ErrStale)
		return
	}
	data, err := cp.Encode()
	if err != nil {
		e.failTarget(target, err)
		return
	}
	expectedRev := u64Str(prev.Revision)
	if wf.pendingOpRev != "" {
		expectedRev = wf.pendingOpRev
	}
	nextRev := u64Str(cp.Revision)
	e.writeStorageCAS(bktOp, "pending", expectedRev, nextRev, false, data, &opCASTask{
		taskBase: taskBase{target: target, stage: opCASSaveWrite}, next: &cp, nextRev: nextRev,
	})
}

//go:noinline
func (e *Engine) clearOperationCAS(op *PersistedOperation, target taskTarget) {
	wf := e.ensureWF()
	expectedRev := u64Str(op.Revision)
	if wf.pendingOpRev != "" {
		expectedRev = wf.pendingOpRev
	}
	e.deleteStorageCAS(bktOp, "pending", expectedRev, &opCASTask{
		taskBase: taskBase{target: target, stage: opCASClearWrite},
	})
}

func (t *opCASTask) step(e *Engine, ev *secEvent) error {
	wf := e.ensureWF()
	if t.stage == opCASClaimRead {
		if ev.err != nil {
			return ev.err
		}
		var recRaw []byte
		if ev.storage != nil {
			for _, ent := range ev.storage.entries {
				if ent == nil || len(ent.Data) == 0 {
					continue
				}
				switch ent.Bucket {
				case bktOp:
					return newWfError("pending", text.KeySecurityASecurityOperationIsUnfinishedContinueItFirst)
				case bktIntent:
					return devicesec.ErrStale
				case bktRec:
					recRaw = ent.Data
				}
			}
		}
		if t.expectedHead != nil {
			if len(recRaw) == 0 {
				return devicesec.ErrStale
			}
			rec, err := DecodePersistedRecord(recRaw)
			if err != nil || rec.MemberID != t.expectedMemberID {
				return devicesec.ErrStale
			}
			states, _, _, err := parseAndVerifyChain(rec.Pin, rec.States, rec.RecoveryProofs)
			if err != nil {
				return devicesec.ErrStale
			}
			if hh, _ := states[len(states)-1].Hash(); hh != *t.expectedHead {
				return devicesec.ErrStale
			}
		}
		data, err := t.op.Encode()
		if err != nil {
			return err
		}
		rev := u64Str(t.op.Revision)
		t.next, t.nextRev, t.stage = t.op, rev, opCASClaimWrite
		e.writeStorageCAS(bktOp, "pending", "", rev, true, data, t)
		return nil
	}
	if storageConflict(ev) {
		if t.stage == opCASClaimWrite {
			return newWfError("pending", text.KeySecurityASecurityOperationIsUnfinishedContinueItFirst)
		}
		return devicesec.ErrStale
	}
	if ev.err != nil {
		return ev.err
	}
	switch t.stage {
	case opCASClaimWrite:
		wf.pendingOp, wf.pendingOpRev, e.securityLocked = t.op, t.nextRev, true
		e.completeTarget(t.target, &secEvent{op: t.op})
	case opCASSaveWrite:
		wf.pendingOp, wf.pendingOpRev = t.next, t.nextRev
		e.completeTarget(t.target, &secEvent{op: t.next})
	case opCASClearWrite:
		wf.pendingOp, wf.pendingOpRev = nil, ""
		e.okTarget(t.target, nil)
	}
	return nil
}

type confirmOpSavedTask struct {
	taskBase
	kind string
}

func (t *confirmOpSavedTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	if ev.kind == evChildDone {
		if t.kind == "create" && ev.op.Phase == "PUBLISHED" {
			e.startAdoptCreationOp(ev.op, t.target)
			return nil
		}
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{CodeSaved: true})
		return nil
	}
	if err := e.checkFG(t.fgGen); err != nil {
		return err
	}
	op := e.ensureWF().pendingOp
	if op == nil || op.Kind != t.kind {
		return devicesec.ErrStale
	}
	next := *op
	next.CodeSaved = true
	e.saveOperationCAS(op, &next, parentTarget(t, 0))
	return nil
}

type resumePendingTask struct {
	taskBase
	op *PersistedOperation
}

//go:noinline
func (e *Engine) startResumePendingOp(fgGen uint64, target taskTarget) {
	e.stepTask(&resumePendingTask{}, &secEvent{kind: evStart, fgGen: fgGen, target: target})
}

func (t *resumePendingTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	if err := e.checkFG(t.fgGen); err != nil {
		return err
	}
	if ev.kind == evChildDone {
		e.startResumeLeaseOp(t.op, t.fgGen, t.target)
		return nil
	}
	wf := e.ensureWF()
	op := wf.pendingOp
	if op == nil {
		if wf.regIntent != nil {
			e.startResumeRegistration(t.fgGen, t.target)
			return nil
		}
		e.okTarget(t.target, nil)
		return nil
	}
	if wf.record != nil && op.Kind != "create" && op.Kind != "recovery" && (op.Server != e.effectiveServer() || op.MemberID != e.selfDeviceID) {
		return devicesec.ErrStale
	}
	if op.Kind == "create" || op.Kind == "recovery" {
		return newWfError("conflict", text.KeySecurityReturnToAccountCreationOrRecoveryToContinueThe)
	}
	switch op.Kind {
	case "offer":
		e.startResumeOfferOp(op, t.fgGen, t.target)
	case "delete-records":
		e.startResumeDeleteRecordsOp(op, t.fgGen, t.target)
	case "lease":
		t.op = op
		e.syncStates(false, "", parentTarget(t, 0))
	case "state":
		e.startResumeStateOp(op, t.fgGen, t.target)
	default:
		return devicesec.ErrInvalid
	}
	return nil
}

const (
	offerSync uint8 = iota
	offerClearOp
	offerListDevices
	offerOpenMeta
	offerEntropy
	offerSeal
	offerSaveSigned
	offerPutMeta
	offerVerifyList
	offerVerifyOpen
	offerSaveRejected
	offerSaveDelivered
	offerDone
)

type resumeOfferTask struct {
	taskBase
	op       *PersistedOperation
	req      *v1.PutDeviceMetaRequest
	reqBytes []byte
	metaRev  uint64
	baseMeta *v1.DeviceMeta
}

func (e *Engine) startResumeOfferOp(op *PersistedOperation, fgGen uint64, target taskTarget) {
	if !op.CodeSaved {
		e.okTarget(target, nil)
		return
	}
	t := &resumeOfferTask{taskBase: taskBase{fgGen: fgGen, target: target, stage: offerSync}, op: op}
	e.syncStates(false, "", parentTarget(t, 0))
}

func (t *resumeOfferTask) finishOffer(e *Engine, ok bool) error {
	if err := e.checkFG(t.fgGen); err != nil {
		return err
	}
	next := *t.op
	if !ok {
		next.OfferRejected, t.stage = true, offerSaveRejected
	} else {
		next.Phase, t.stage = "DELIVERED", offerSaveDelivered
	}
	e.saveOperationCAS(t.op, &next, parentTarget(t, 0))
	return nil
}

func (t *resumeOfferTask) sendPutReq(e *Engine) error {
	if t.req.DeviceId != e.selfDeviceID {
		return devicesec.ErrUntrusted
	}
	t.stage = offerPutMeta
	e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_DEVICE_META, t.op.Server, "", true, t.fgGen, t.reqBytes, parentTarget(t, 0))
	return nil
}

func (t *resumeOfferTask) startSealForMeta(e *Engine, base *v1.DeviceMeta) {
	if base == nil {
		base = &v1.DeviceMeta{}
	}
	base.HandoffRecoveryEd = slices.Clone(t.op.RecoveryEd)
	t.baseMeta, t.stage = base, offerEntropy
	e.waitEntropy(64, t, 0)
}

func (t *resumeOfferTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	switch t.stage {
	case offerSync:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if e.isAuthority() && len(t.op.RecoveryEd) == 32 && sameBytes(e.head.Core.RecoveryEd[:], t.op.RecoveryEd) {
			t.stage = offerClearOp
			e.clearOperationCAS(t.op, parentTarget(t, 0))
			return nil
		}
		if !e.isMember {
			return devicesec.ErrUntrusted
		}
		if e.isAuthority() {
			e.okTarget(t.target, nil)
			return nil
		}
		if len(t.op.Signed) > 0 {
			var req v1.PutDeviceMetaRequest
			if req.Unmarshal(t.op.Signed) != nil {
				return devicesec.ErrInvalid
			}
			t.req, t.reqBytes = &req, t.op.Signed
			return t.sendPutReq(e)
		}
		t.stage = offerListDevices
		e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES, t.op.Server, t.fgGen, &v1.ListDevicesRequest{}, parentTarget(t, 0))
	case offerClearOp, offerDone:
		e.okTarget(t.target, nil)
	case offerListDevices:
		var lr v1.ListDevicesResponse
		if lr.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		if ownDev := findDeviceByID(lr.Devices, e.selfDeviceID); ownDev != nil && ownDev.Meta != nil && ownDev.MetaRevision > 0 {
			t.metaRev, t.stage = ownDev.MetaRevision, offerOpenMeta
			e.startOpenBatchInternal(e.lastNowMs, []OpenItem{openDeviceMetaItem(e.selfDeviceID, ownDev.Meta)}, parentTarget(t, 0))
			return nil
		}
		t.startSealForMeta(e, nil)
	case offerOpenMeta:
		if ev.opened[0].Err != nil {
			return ev.opened[0].Err
		}
		dm, _ := ev.opened[0].Body.(*v1.DeviceMeta)
		t.startSealForMeta(e, dm.Clone())
	case offerEntropy:
		plain, err := t.baseMeta.Marshal()
		if err != nil {
			return err
		}
		t.stage = offerSeal
		e.startSealEnvelopeInternal(e.lastNowMs, devicesec.DeviceMeta, "", "", e.selfDeviceID, t.metaRev+1, 0, plain, false, nil, parentTarget(t, 0))
	case offerSeal:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		req := &v1.PutDeviceMetaRequest{DeviceId: e.selfDeviceID, ExpectedRevision: t.metaRev, Meta: ev.seal.Envelope}
		reqBytes, err := req.Marshal()
		if err != nil {
			return err
		}
		t.req, t.reqBytes = req, reqBytes
		next := *t.op
		next.Signed, t.stage = reqBytes, offerSaveSigned
		e.saveOperationCAS(t.op, &next, parentTarget(t, 0))
	case offerSaveSigned:
		t.op = ev.op
		return t.sendPutReq(e)
	case offerPutMeta:
		var resp v1.PutDeviceMetaResponse
		if resp.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		if resp.Applied {
			return t.finishOffer(e, true)
		}
		t.stage = offerVerifyList
		e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES, t.op.Server, t.fgGen, &v1.ListDevicesRequest{}, parentTarget(t, 0))
	case offerVerifyList:
		var lr v1.ListDevicesResponse
		if lr.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		ownDev := findDeviceByID(lr.Devices, e.selfDeviceID)
		if ownDev == nil || ownDev.Meta == nil || ownDev.MetaRevision <= t.req.ExpectedRevision {
			return t.finishOffer(e, false)
		}
		t.stage = offerVerifyOpen
		e.startOpenBatchInternal(e.lastNowMs, []OpenItem{openDeviceMetaItem(e.selfDeviceID, ownDev.Meta)}, parentTarget(t, 0))
	case offerVerifyOpen:
		if ev.opened[0].Err != nil {
			return t.finishOffer(e, false)
		}
		dm, ok := ev.opened[0].Body.(*v1.DeviceMeta)
		return t.finishOffer(e, ok && dm != nil && sameBytes(dm.HandoffRecoveryEd, t.op.RecoveryEd))
	case offerSaveRejected:
		return newWfError("conflict", text.KeySecurityDeviceInformationChangedTheOriginalTransferRequestWasNot)
	case offerSaveDelivered:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		t.stage = offerDone
		e.clearOperationCAS(ev.op, parentTarget(t, 0))
	}
	return nil
}

const (
	delRecSync uint8 = iota
	delRecSaveSigned
	delRecRPC
	delRecClear
)

type resumeDeleteRecordsTask struct {
	taskBase
	op *PersistedOperation
}

func (e *Engine) startResumeDeleteRecordsOp(op *PersistedOperation, fgGen uint64, target taskTarget) {
	t := &resumeDeleteRecordsTask{taskBase: taskBase{fgGen: fgGen, target: target}, op: op}
	e.syncStates(false, "", parentTarget(t, 0))
}

func (t *resumeDeleteRecordsTask) sendRPC(e *Engine, curOp *PersistedOperation) {
	t.op, t.stage = curOp, delRecRPC
	e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_DELETE_MACHINE_RECORDS, curOp.Server, "", true, t.fgGen, curOp.Signed, parentTarget(t, 0))
}

func (t *resumeDeleteRecordsTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	switch t.stage {
	case delRecSync:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if !e.isAuthority() {
			return devicesec.ErrUntrusted
		}
		if e.headHash != t.op.Parent {
			return devicesec.ErrStale
		}
		if len(t.op.Signed) > 0 {
			t.sendRPC(e, t.op)
			return nil
		}
		reqBytes, err := (&v1.DeleteMachineRecordsRequest{MachineId: t.op.TargetDeviceID}).Marshal()
		if err != nil {
			return err
		}
		next := *t.op
		next.Signed, t.stage = reqBytes, delRecSaveSigned
		e.saveOperationCAS(t.op, &next, parentTarget(t, 0))
	case delRecSaveSigned:
		t.sendRPC(e, ev.op)
	case delRecRPC:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		t.stage = delRecClear
		e.clearOperationCAS(t.op, parentTarget(t, 0))
	case delRecClear:
		e.okTarget(t.target, nil)
	}
	return nil
}

const (
	leaseSignSigned uint8 = iota
	leaseSaveSigned
	leaseSignRetry
	leaseSaveRetry
	leaseUpload
	leaseInstall
	leaseClear
)

type resumeLeaseTask struct {
	taskBase
	op       *PersistedOperation
	reqBytes []byte
	leaseRaw []byte
}

func (e *Engine) startResumeLeaseOp(op *PersistedOperation, fgGen uint64, target taskTarget) {
	if e.headHash != op.Parent {
		if _, ok := e.byHash[op.Parent]; !ok {
			e.failTarget(target, devicesec.ErrStale)
			return
		}
		e.clearOperationCAS(op, target)
		return
	}
	if !e.isAuthority() {
		e.failTarget(target, devicesec.ErrUntrusted)
		return
	}
	t := &resumeLeaseTask{taskBase: taskBase{fgGen: fgGen, target: target}, op: op}
	if len(op.Signed) == 0 {
		t.stage = leaseSignSigned
		e.buildAndSignLease(e.head, keyDevEd, "", e.currentSigningTime(e.lastNowMs), parentTarget(t, 0))
		return
	}
	raw := op.Retry
	if len(raw) == 0 {
		raw = op.Signed
	}
	var req v1.PutSecurityStateRequest
	if req.Unmarshal(raw) != nil {
		e.failTarget(target, devicesec.ErrInvalid)
		return
	}
	t.publishReq(e, op, &req, raw)
}

func (t *resumeLeaseTask) publishReq(e *Engine, curOp *PersistedOperation, req *v1.PutSecurityStateRequest, reqBytes []byte) {
	t.op = curOp
	cert, err := devicesec.ParseLease(req.Lease)
	if err != nil || cert.Verify(e.head) != nil {
		e.failTarget(t.target, devicesec.ErrInvalid)
		return
	}
	effNow := e.effectiveNow(e.lastNowMs)
	if effNow >= cert.NotAfter || (e.hasLease && e.lease.Head == e.headHash && e.lease.IssuedAt > cert.IssuedAt) {
		t.stage = leaseSignRetry
		e.buildAndSignLease(e.head, keyDevEd, "", e.currentSigningTime(e.lastNowMs), parentTarget(t, 0))
		return
	}
	t.upload(e, curOp, reqBytes, req.Lease)
}

func (t *resumeLeaseTask) upload(e *Engine, finalOp *PersistedOperation, finalReqBytes, finalLeaseBytes []byte) {
	t.op, t.reqBytes, t.leaseRaw, t.stage = finalOp, finalReqBytes, finalLeaseBytes, leaseUpload
	e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE, finalOp.Server, "", true, t.fgGen, finalReqBytes, parentTarget(t, 0))
}

func (t *resumeLeaseTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	switch t.stage {
	case leaseSignSigned, leaseSignRetry:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		lb, _ := ev.lease.MarshalBinary()
		reqBytes, _ := (&v1.PutSecurityStateRequest{Lease: lb}).Marshal()
		next := *t.op
		t.leaseRaw, t.reqBytes = lb, reqBytes
		if t.stage == leaseSignSigned {
			next.Signed, t.stage = reqBytes, leaseSaveSigned
		} else {
			next.Retry, t.stage = reqBytes, leaseSaveRetry
		}
		e.saveOperationCAS(t.op, &next, parentTarget(t, 0))
	case leaseSaveSigned:
		t.publishReq(e, ev.op, &v1.PutSecurityStateRequest{Lease: t.leaseRaw}, t.reqBytes)
	case leaseSaveRetry:
		t.upload(e, ev.op, t.reqBytes, t.leaseRaw)
	case leaseUpload:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		t.stage = leaseInstall
		e.installRecordCAS(nil, nil, nil, t.leaseRaw, e.lastNowMs, nil, parentTarget(t, 0))
	case leaseInstall:
		t.stage = leaseClear
		e.clearOperationCAS(t.op, parentTarget(t, 0))
	case leaseClear:
		e.okTarget(t.target, nil)
	}
	return nil
}

type resumeStateTask struct {
	taskBase
	op *PersistedOperation
}

func (e *Engine) startResumeStateOp(op *PersistedOperation, fgGen uint64, target taskTarget) {
	if len(op.Signed) == 0 {
		if op.Action == "recovery" && !op.CodeSaved {
			e.okTarget(target, nil)
			return
		}
		t := &resumeStateTask{taskBase: taskBase{fgGen: fgGen, target: target}, op: op}
		e.startSignStateDraft(op, fgGen, parentTarget(t, 0))
		return
	}
	e.startContinueStateOpAfterSign(op, fgGen, target)
}

func (t *resumeStateTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	e.startContinueStateOpAfterSign(ev.op, t.fgGen, t.target)
	return nil
}

const (
	draftSync uint8 = iota
	draftGetPairing
	draftEntropy
	draftKEM
	draftLease
	draftDelivery
	draftSave
)

type signStateDraftTask struct {
	taskBase
	op             *PersistedOperation
	now            uint64
	nextState      devicesec.State
	nextStateBytes []byte
	leaseRaw       []byte
	signedBytes    []byte
}

func (e *Engine) startSignStateDraft(op *PersistedOperation, fgGen uint64, target taskTarget) {
	t := &signStateDraftTask{taskBase: taskBase{fgGen: fgGen, target: target}, op: op}
	e.syncStates(false, "", parentTarget(t, 0))
}

func (t *signStateDraftTask) beginDraftEntropy(e *Engine) error {
	if _, ok := e.epochSecrets[e.head.Core.ContentEpoch]; !ok {
		return ErrNotInitialized
	}
	t.stage = draftEntropy
	e.waitEntropy(128, t, 0)
	return nil
}

func (t *signStateDraftTask) finishWithLease(e *Engine, leaseCert *devicesec.LeaseCertificate) error {
	nextStateBytes, err := t.nextState.MarshalBinary()
	if err != nil {
		return err
	}
	var leaseRaw []byte
	if leaseCert != nil {
		leaseRaw, _ = leaseCert.MarshalBinary()
	}
	signedBytes, err := (&v1.PutSecurityStateRequest{States: []*v1.SecurityState{{State: nextStateBytes}}, Lease: leaseRaw}).Marshal()
	if err != nil {
		return err
	}
	t.nextStateBytes, t.leaseRaw, t.signedBytes = nextStateBytes, leaseRaw, signedBytes
	if t.op.Pair == nil {
		t.saveWithDelivery(e, nil)
		return nil
	}
	t.stage = draftDelivery
	e.startBuildPairDelivery(t.op, nextStateBytes, leaseRaw, t.fgGen, parentTarget(t, 0))
	return nil
}

func (t *signStateDraftTask) saveWithDelivery(e *Engine, deliveryBytes []byte) {
	nextOp := *t.op
	nextOp.Signed, nextOp.Delivery, t.stage = t.signedBytes, deliveryBytes, draftSave
	e.saveOperationCAS(t.op, &nextOp, parentTarget(t, 0))
}

func (t *signStateDraftTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	switch t.stage {
	case draftSync:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if !e.isAuthority() || e.headHash != t.op.Parent {
			return devicesec.ErrStale
		}
		if t.op.Pair != nil {
			if t.op.Pair.CandidateID == e.selfDeviceID {
				return devicesec.ErrUntrusted
			}
			if uint64(t.op.Pair.ExpiresMs) <= e.effectiveNow(e.lastNowMs) {
				return newWfError("expired", text.KeySecurityTheUnpublishedPairingDraftExpiredYouCanExplicitlyAbandon)
			}
			t.stage = draftGetPairing
			e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_PAIRING, t.op.Server, t.fgGen, &v1.GetPairingRequest{PairingId: t.op.Pair.PairingID}, parentTarget(t, 0))
			return nil
		}
		return t.beginDraftEntropy(e)
	case draftGetPairing:
		var st v1.GetPairingResponse
		if st.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		if st.State != v1.PairingState_PAIRING_STATE_REVEALED || uint32(st.JoinerKind) != t.op.Pair.CandidateKind ||
			(t.op.Pair.ReplacesDeviceID != "" && st.JoinerKind != v1.DeviceKind_DEVICE_KIND_MACHINE) ||
			!sameBytes(st.DevicePublicKey, t.op.Pair.CandidateEd) || !sameBytes(st.DeviceHpkePublicKey, t.op.Pair.CandidateX) {
			return newWfError("canceled", text.KeySecurityTheUnpublishedPairingDraftWasCanceledOrItsCandidate)
		}
		return t.beginDraftEntropy(e)
	case draftEntropy:
		oldSecret, ok := e.epochSecrets[e.head.Core.ContentEpoch]
		if !ok {
			return ErrNotInitialized
		}
		if err := e.checkFG(t.fgGen); err != nil {
			zeroBytes(oldSecret[:])
			return err
		}
		var newSecret [32]byte
		if !e.readRandom(newSecret[:]) {
			zeroBytes(oldSecret[:])
			return ErrNeedEntropy
		}
		now, parent := e.currentSigningTime(e.lastNowMs), e.head
		t.now = now
		params := kemBuildParams{parent: parent, oldSecret: oldSecret, newSecret: newSecret, now: now}
		zeroBytes(oldSecret[:])
		zeroBytes(newSecret[:])
		var kemMembers []devicesec.Member
		switch t.op.Action {
		case "remove":
			var members []devicesec.Member
			for _, m := range parent.Core.Members {
				if m.ID != t.op.TargetDeviceID {
					members = append(members, m)
				}
			}
			removed := append(slices.Clone(parent.Core.Removed), t.op.TargetDeviceID)
			sortutil.Strings(removed)
			kemMembers, params.kind, params.members, params.removed = members, kemBuildMembership, members, removed
		case "recovery":
			if len(t.op.RecoveryEd) != 32 {
				params.zeroSecrets()
				return devicesec.ErrInvalid
			}
			copy(params.recoveryEd[:], t.op.RecoveryEd)
			kemMembers, params.kind = parent.Core.Members, kemBuildRecoveryRot
		case "handoff":
			if len(t.op.RecoveryEd) != 32 {
				params.zeroSecrets()
				return devicesec.ErrInvalid
			}
			copy(params.recoveryEd[:], t.op.RecoveryEd)
			for _, m := range parent.Core.Members {
				if m.ID != parent.Core.AuthorityID {
					kemMembers = append(kemMembers, m)
				}
			}
			params.kind, params.targetID = kemBuildHandoff, t.op.TargetDeviceID
		case "pair":
			p := t.op.Pair
			if p == nil {
				params.zeroSecrets()
				return devicesec.ErrInvalid
			}
			cand, err := devicesec.NewMember(p.CandidateEd, p.CandidateX, devicesec.Kind(p.CandidateKind))
			if err != nil {
				params.zeroSecrets()
				return err
			}
			if existing, found := parent.Core.Member(cand.ID); found {
				if existing.Kind != cand.Kind || existing.EdPublic != cand.EdPublic {
					params.zeroSecrets()
					return devicesec.ErrStale
				}
				members := make([]devicesec.Member, len(parent.Core.Members))
				for i, m := range parent.Core.Members {
					if m.ID == cand.ID {
						members[i] = cand
					} else {
						members[i] = m
					}
				}
				kemMembers, params.kind, params.members, params.removed = members, kemBuildMembership, members, parent.Core.Removed
			} else {
				kemMembers = append(slices.Clone(parent.Core.Members), cand)
				params.kind, params.member, params.candX = kemBuildPair, cand, p.CandidateX
			}
		default:
			params.zeroSecrets()
			return devicesec.ErrInvalid
		}
		t.stage = draftKEM
		e.buildAndSignStateWithKEM(keyDevEd, "", true, t.fgGen, params, kemMembers, parentTarget(t, 0))
	case draftKEM:
		t.nextState = *ev.state
		if ev.state.Core.AuthorityID == e.selfDeviceID {
			t.stage = draftLease
			e.buildAndSignLease(*ev.state, keyDevEd, "", t.now, parentTarget(t, 0))
			return nil
		}
		return t.finishWithLease(e, nil)
	case draftLease:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		return t.finishWithLease(e, ev.lease)
	case draftDelivery:
		t.saveWithDelivery(e, ev.bytes)
	case draftSave:
		e.completeTarget(t.target, &secEvent{op: ev.op})
	}
	return nil
}

const (
	deliveryEntropy uint8 = iota
	deliverySign
)

type buildPairDeliveryTask struct {
	taskBase
	op          *PersistedOperation
	acctPub     []byte
	leaseRaw    []byte
	chainStates []*v1.SecurityState
	authPub     [32]byte
	envNonce    [12]byte
	apBytes     []byte
}

func (e *Engine) startBuildPairDelivery(op *PersistedOperation, nextStateBytes, leaseRaw []byte, fgGen uint64, target taskTarget) {
	wf := e.ensureWF()
	rec := wf.record
	if rec == nil || len(rec.States) == 0 {
		e.failTarget(target, devicesec.ErrInvalid)
		return
	}
	var acctPub []byte
	if len(rec.AccountPublicKey) == 32 {
		acctPub = rec.AccountPublicKey
	} else if prof := wf.publicProfiles[PublicProfileStorageKey(op.Server, e.pin)]; prof != nil {
		acctPub = prof.AccountPublicKey[:]
	}
	if len(acctPub) != 32 {
		e.failTarget(target, newWfError("invalid", text.KeySecurityAccountPublicKeyMissingPairAgainOrUseA))
		return
	}
	authMem, ok := e.head.Core.Member(e.selfDeviceID)
	if !ok {
		e.failTarget(target, devicesec.ErrUntrusted)
		return
	}
	var totalBytes int
	chainStates := make([]*v1.SecurityState, 0, len(rec.States)+1)
	for i, s := range rec.States {
		var rp []byte
		if i < len(rec.RecoveryProofs) {
			rp = rec.RecoveryProofs[i]
		}
		totalBytes += len(s) + len(rp)
		chainStates = append(chainStates, &v1.SecurityState{State: s, Recovery: rp})
	}
	totalBytes += len(nextStateBytes)
	chainStates = append(chainStates, &v1.SecurityState{State: nextStateBytes})
	if len(chainStates) > devicesec.MaxPairStates || totalBytes > devicesec.MaxPairChainBytes {
		e.failTarget(target, devicesec.ErrChainTooLarge)
		return
	}
	e.waitEntropy(64, &buildPairDeliveryTask{
		taskBase: taskBase{fgGen: fgGen, target: target}, op: op, acctPub: acctPub, leaseRaw: leaseRaw, chainStates: chainStates, authPub: authMem.EdPublic,
	}, 0)
}

func (t *buildPairDeliveryTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	if err := e.checkFG(t.fgGen); err != nil {
		return err
	}
	p := t.op.Pair
	switch t.stage {
	case deliveryEntropy:
		var nonce [16]byte
		if !e.readRandom(nonce[:]) || !e.readRandom(t.envNonce[:]) {
			return ErrNeedEntropy
		}
		apBytes, err := (&v1.PairApproval{
			AccountId: e.pin.Account, PairingId: p.PairingID, ApproverDeviceId: e.selfDeviceID,
			ApproverPublicKey: slices.Clone(t.authPub[:]), CandidateDevicePublicKey: slices.Clone(p.CandidateEd),
			CandidateKind: v1.DeviceKind(p.CandidateKind), ExpiresMs: p.ExpiresMs, Nonce: nonce[:], ReplacesDeviceId: p.ReplacesDeviceID,
		}).Marshal()
		if err != nil {
			return err
		}
		t.apBytes, t.stage = apBytes, deliverySign
		e.waitSign(keyDevEd, "", append(append([]byte{}, PairApprovalContext...), apBytes...), t, 0)
	case deliverySign:
		plain, err := (&v1.PairingPayload{
			Server: t.op.Server, Approval: t.apBytes, ApprovalSignature: ev.bytes,
			SecurityStates: t.chainStates, AccountPublicKey: slices.Clone(t.acctPub), Lease: slices.Clone(t.leaseRaw),
		}).Marshal()
		if err != nil {
			return err
		}
		hdrBytes, err := (&v1.Header{
			Version: 1, Kind: v1.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: p.PairingID,
			Sender: e.selfDeviceID, Compression: v1.Compression_COMPRESSION_NONE,
		}).Marshal()
		if err != nil {
			return err
		}
		ct, err := sealPairingEnvelopeAESGCM(p.PairKey, t.envNonce[:], hdrBytes, plain)
		if err != nil {
			return err
		}
		reqBytes, err := (&v1.ApprovePairingRequest{
			PairingId: p.PairingID, Payload: &v1.Envelope{Header: hdrBytes, Nonce: t.envNonce[:], Ciphertext: ct},
			Approval: t.apBytes, ApprovalSignature: ev.bytes,
		}).Marshal()
		if err != nil {
			return err
		}
		e.completeTarget(t.target, &secEvent{bytes: reqBytes})
	}
	return nil
}

const (
	contStateSync uint8 = iota
	contStateEarlyDelivered
	contStateClearRotPre
	contStateSignLease
	contStateSaveLease
	contStatePutState
	contStateInstallState
	contStatePutLease
	contStateInstallLease
	contStateSavePub
	contStateApprovePair
	contStateGetPair
	contStateListDevices
	contStateSaveDelivered
	contStateClearRotPost
	contStateDone
	contStateRemoveOld
)

type continueStateOpTask struct {
	taskBase
	op          *PersistedOperation
	origStates  []*v1.SecurityState
	targetBytes []byte
	targetState devicesec.State
	targetHash  devicesec.Hash
	installed   bool
	curReqBytes []byte
	curReqLease []byte
	delivery    *v1.ApprovePairingRequest
}

func (e *Engine) startContinueStateOpAfterSign(op *PersistedOperation, fgGen uint64, target taskTarget) {
	var orig v1.PutSecurityStateRequest
	if orig.Unmarshal(op.Signed) != nil || len(orig.States) == 0 {
		e.failTarget(target, devicesec.ErrInvalid)
		return
	}
	targetBytes := orig.States[len(orig.States)-1].State
	targetState, err := devicesec.ParseState(targetBytes)
	if err != nil {
		e.failTarget(target, err)
		return
	}
	targetHash, _ := targetState.Hash()
	t := &continueStateOpTask{
		taskBase: taskBase{fgGen: fgGen, target: target}, op: op, origStates: orig.States,
		targetBytes: targetBytes, targetState: targetState, targetHash: targetHash,
	}
	if !e.isMember && e.byHash[targetHash].Core.Epoch == targetState.Core.Epoch && targetState.Core.AuthorityID != e.selfDeviceID {
		delivered := *op
		delivered.Phase, t.stage = "DELIVERED", contStateEarlyDelivered
		e.saveOperationCAS(op, &delivered, parentTarget(t, 0))
		return
	}
	e.syncStates(false, "", parentTarget(t, 0))
}

func (t *continueStateOpTask) deliverIfNeeded(e *Engine, curOp *PersistedOperation) {
	pubOp := *curOp
	pubOp.Phase, t.op, t.stage = "PUBLISHED", curOp, contStateSavePub
	e.saveOperationCAS(curOp, &pubOp, parentTarget(t, 0))
}

func (t *continueStateOpTask) finishDeliveryAndClear(e *Engine, curOp *PersistedOperation) {
	delivered := *curOp
	delivered.Phase, t.op, t.stage = "DELIVERED", curOp, contStateSaveDelivered
	e.saveOperationCAS(curOp, &delivered, parentTarget(t, 0))
}

func (t *continueStateOpTask) proceedPublish(e *Engine, curOp *PersistedOperation, reqLease, reqBytes []byte) {
	t.op, t.curReqLease, t.curReqBytes = curOp, reqLease, reqBytes
	if !t.installed {
		t.stage = contStatePutState
		e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE, curOp.Server, "", true, t.fgGen, reqBytes, parentTarget(t, 0))
		return
	}
	if e.headHash == t.targetHash && len(reqLease) > 0 {
		if reqLC, err := devicesec.ParseLease(reqLease); err == nil && (!e.hasLease || e.lease.IssuedAt < reqLC.IssuedAt) {
			t.stage = contStatePutLease
			e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE, curOp.Server, "", true, t.fgGen, reqBytes, parentTarget(t, 0))
			return
		}
	}
	t.deliverIfNeeded(e, curOp)
}

func (t *continueStateOpTask) checkDeliveryFallback(e *Engine, origErr *rpcError) error {
	if origErr.code != connectCodeNotFound && origErr.code != connectCodeFailedPrecondition && origErr.code != connectCodeCanceled {
		return origErr
	}
	t.stage = contStateListDevices
	e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES, t.op.Server, t.fgGen, &v1.ListDevicesRequest{}, parentTarget(t, 0))
	return nil
}

func (t *continueStateOpTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil && t.stage != contStateApprovePair && t.stage != contStateGetPair && t.stage != contStateRemoveOld {
		return ev.err
	}
	switch t.stage {
	case contStateEarlyDelivered:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		t.stage = contStateDone
		e.clearOperationCAS(ev.op, parentTarget(t, 0))
	case contStateDone:
		if t.op != nil && t.op.Pair != nil {
			if sess := e.ensureWF().initPairings[t.op.Pair.PairingID]; sess != nil && len(sess.removeDeviceIDs) > 0 {
				ids := sess.removeDeviceIDs
				sess.removeDeviceIDs = nil
				t.stage = contStateRemoveOld
				e.startRemoveDeviceList(t.fgGen, ids, parentTarget(t, 0))
				return nil
			}
		}
		e.okTarget(t.target, nil)
	case contStateRemoveOld:
		e.okTarget(t.target, nil)
	case contStateClearRotPre:
		t.stage = contStateDone
		e.clearOperationCAS(t.op, parentTarget(t, 0))
	case contStateSync:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if int(t.targetState.Core.Epoch) <= len(e.states) && t.targetState.Core.Epoch >= 1 {
			if ch, _ := e.states[t.targetState.Core.Epoch-1].Hash(); ch != t.targetHash {
				return devicesec.ErrStale
			}
			t.installed = true
		} else if e.headHash != t.op.Parent {
			return devicesec.ErrStale
		}
		if t.op.Phase == "DELIVERED" {
			if !t.installed {
				return devicesec.ErrStale
			}
			if t.op.Action == "recovery" && t.op.CodeSaved {
				t.stage = contStateClearRotPre
				e.installRecordCAS(nil, nil, nil, nil, 0, clearNeedsRotationPatch(), parentTarget(t, 0))
				return nil
			}
			t.stage = contStateDone
			e.clearOperationCAS(t.op, parentTarget(t, 0))
			return nil
		}
		reqRaw := t.op.Retry
		if len(reqRaw) == 0 {
			reqRaw = t.op.Signed
		}
		var req v1.PutSecurityStateRequest
		if req.Unmarshal(reqRaw) != nil {
			return devicesec.ErrInvalid
		}
		var leaseCert *devicesec.LeaseCertificate
		if len(req.Lease) > 0 {
			lc, err := devicesec.ParseLease(req.Lease)
			if err != nil || lc.Verify(t.targetState) != nil {
				return devicesec.ErrInvalid
			}
			leaseCert = &lc
		}
		effNow := e.effectiveNow(e.lastNowMs)
		if (!t.installed || e.headHash == t.targetHash) && t.targetState.Core.AuthorityID == e.selfDeviceID &&
			(leaseCert == nil || effNow >= leaseCert.NotAfter || (e.hasLease && e.lease.Head == t.targetHash && e.lease.IssuedAt > leaseCert.IssuedAt)) {
			t.stage = contStateSignLease
			e.buildAndSignLease(t.targetState, keyDevEd, "", max(e.currentSigningTime(e.lastNowMs), t.targetState.Core.IssuedAt), parentTarget(t, 0))
			return nil
		}
		t.proceedPublish(e, t.op, req.Lease, reqRaw)
	case contStateSignLease:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		lb, _ := ev.lease.MarshalBinary()
		newReqBytes, _ := (&v1.PutSecurityStateRequest{States: t.origStates, Lease: lb}).Marshal()
		nextOp := *t.op
		nextOp.Retry, t.curReqLease, t.curReqBytes, t.stage = newReqBytes, lb, newReqBytes, contStateSaveLease
		e.saveOperationCAS(t.op, &nextOp, parentTarget(t, 0))
	case contStateSaveLease:
		t.proceedPublish(e, ev.op, t.curReqLease, t.curReqBytes)
	case contStatePutState:
		var resp v1.PutSecurityStateResponse
		if resp.Unmarshal(ev.bytes) != nil || resp.HeadEpoch != t.targetState.Core.Epoch {
			return devicesec.ErrStale
		}
		respHead, err := devicesec.ParseState(resp.Head)
		if err != nil {
			return devicesec.ErrStale
		}
		if rhh, _ := respHead.Hash(); rhh != t.targetHash {
			return devicesec.ErrStale
		}
		t.stage = contStateInstallState
		e.installRecordCAS([]devicesec.State{t.targetState}, [][]byte{t.targetBytes}, [][]byte{nil}, t.curReqLease, e.lastNowMs, nil, parentTarget(t, 0))
	case contStatePutLease:
		t.stage = contStateInstallLease
		e.installRecordCAS(nil, nil, nil, t.curReqLease, e.lastNowMs, nil, parentTarget(t, 0))
	case contStateInstallState, contStateInstallLease:
		t.deliverIfNeeded(e, t.op)
	case contStateSavePub:
		saved := ev.op
		t.op = saved
		if len(saved.Delivery) == 0 {
			t.finishDeliveryAndClear(e, saved)
			return nil
		}
		var delivery v1.ApprovePairingRequest
		if delivery.Unmarshal(saved.Delivery) != nil {
			return devicesec.ErrInvalid
		}
		t.delivery, t.stage = &delivery, contStateApprovePair
		e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_APPROVE_PAIRING, saved.Server, "", true, t.fgGen, saved.Delivery, parentTarget(t, 0))
	case contStateApprovePair:
		if ev.rpcErr != nil {
			return t.checkDeliveryFallback(e, ev.rpcErr)
		}
		if ev.err != nil {
			return ev.err
		}
		t.stage = contStateGetPair
		e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_PAIRING, t.op.Server, t.fgGen, &v1.GetPairingRequest{PairingId: t.delivery.PairingId}, parentTarget(t, 0))
	case contStateGetPair:
		if ev.rpcErr != nil {
			return t.checkDeliveryFallback(e, ev.rpcErr)
		}
		if ev.err != nil {
			return ev.err
		}
		var gr v1.GetPairingResponse
		if gr.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		switch gr.State {
		case v1.PairingState_PAIRING_STATE_DELIVERED:
			t.finishDeliveryAndClear(e, t.op)
		case v1.PairingState_PAIRING_STATE_CANCELED:
			return fatalWfError("pairing_delivery", text.KeySecurityPairingWasCanceledExplicitlyRemoveTheUnfinishedCandidateBefore, "pairing_delivery")
		default:
			e.okTarget(t.target, nil)
		}
	case contStateListDevices:
		var lr v1.ListDevicesResponse
		if lr.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		ap, err := VerifyPairApproval(t.delivery.Approval, t.delivery.ApprovalSignature)
		if err != nil {
			return err
		}
		if findDeviceByID(lr.Devices, devicesec.ID(ap.CandidateDevicePublicKey)) != nil {
			t.finishDeliveryAndClear(e, t.op)
			return nil
		}
		return fatalWfError("pairing_delivery", text.KeySecurityPairingDeliveryExpiredExplicitlyRemoveTheUnfinishedCandidateBefore, "pairing_delivery")
	case contStateSaveDelivered:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		t.op = ev.op
		if ev.op.Action == "recovery" && ev.op.CodeSaved {
			t.stage = contStateClearRotPost
			e.installRecordCAS(nil, nil, nil, nil, 0, clearNeedsRotationPatch(), parentTarget(t, 0))
			return nil
		}
		t.stage = contStateDone
		e.clearOperationCAS(ev.op, parentTarget(t, 0))
	case contStateClearRotPost:
		t.stage = contStateDone
		e.clearOperationCAS(t.op, parentTarget(t, 0))
	}
	return nil
}

type discardPendingTask struct {
	taskBase
	acknowledgePublished bool
}

func (t *discardPendingTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	wf := e.ensureWF()
	if err := e.checkFG(t.fgGen); err != nil {
		return err
	}
	op := wf.pendingOp
	if op == nil {
		wf.codeDraft = nil
		e.okTarget(t.target, nil)
		return nil
	}
	switch {
	case op.Kind == "offer" && op.Phase == "PREPARED":
		if !t.acknowledgePublished && op.CodeSaved && len(op.Signed) > 0 {
			return devicesec.ErrStale
		}
	case op.Kind == "create" && op.Phase == "PREPARED" && !op.CodeSaved:
		if op.Create != nil {
			e.discardCandidatePair(op.Create.EdSlot, op.Create.XSlot)
		}
	case op.Kind == "state" && op.Phase == "PREPARED" && len(op.Signed) == 0 && !(op.Action == "recovery" && op.CodeSaved):
	case op.Kind == "recovery" && op.Phase == "PREPARED" && len(op.Signed) == 0:
		if op.Recovery != nil {
			e.discardCandidatePair(op.Recovery.EdSlot, op.Recovery.XSlot)
		}
	default:
		return devicesec.ErrStale
	}
	e.clearOperationCAS(op, t.target)
	return nil
}
