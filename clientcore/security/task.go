package security

import (
	"errors"
	"slices"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

type secEventKind uint8

const (
	evStart secEventKind = iota
	evSign
	evAgree
	evBatch
	evKeyGen
	evEncap
	evStorage
	evRPC
	evEntropy
	evUnlocked
	evChildDone
)

type recoveryContextResult struct {
	resp      *v1.GetRecoveryContextResponse
	pin       devicesec.Pin
	acctPub   [32]byte
	rawStates [][]byte
	rawProofs [][]byte
}

const (
	keyDevEd  = corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED
	keyDevX   = corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_X
	keyCandEd = corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED
	keyCandX  = corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X
	keyEphemX = corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X

	bktRec    = corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD
	bktOp     = corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_OPERATION
	bktIntent = corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_REGISTRATION_INTENT
	bktCand   = corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_PAIRING_CANDIDATE
	bktProf   = corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_PUBLIC_PROFILE
	bktNotify = corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_NOTIFY_RECORD
)

type secEvent struct {
	kind    secEventKind
	tag     uint8
	err     error
	bytes   []byte
	rpcErr  *rpcError
	storage *storageReply
	encap   []*corev1.SecurityEncapsulatedPair
	batch   []*corev1.SecurityCryptoBatchResult
	nowMs   int64
	op      *PersistedOperation
	rec     *PersistedRecord
	state   *devicesec.State
	lease   *devicesec.LeaseCertificate
	seal    *SealResult
	opened  []OpenResult
	str     string
	wfRes   *corev1.SecurityWorkflowResultEffect
	recCtx  *recoveryContextResult
}

type secTask interface {
	step(e *Engine, ev *secEvent) error
}

type opTask interface {
	secTask
	setOpContext(fgGen uint64, target taskTarget)
}

type taskTargeter interface {
	taskTarget() taskTarget
}

type taskFailer interface {
	onError(e *Engine, err error)
}

type stageTask interface {
	secTask
	currentStage() uint8
}

type rawErrTask interface {
	rawErrMask() uint32
}

type ignoreErrTask interface {
	ignoreErrMask() uint32
}

const fgOnStart uint32 = 1 << 31

type fgCheckTask interface {
	stageTask
	fgCheckMask() uint32
	fgGenVal() uint64
}

type taskBase struct {
	_      [0]func()
	fgGen  uint64
	target taskTarget
	stage  uint8
}

func (b *taskBase) setOpContext(fgGen uint64, target taskTarget) { b.fgGen, b.target = fgGen, target }
func (b *taskBase) taskTarget() taskTarget                       { return b.target }
func (b *taskBase) currentStage() uint8                          { return b.stage }
func (b *taskBase) fgGenVal() uint64                             { return b.fgGen }

//go:noinline
func (e *Engine) stepTask(t secTask, ev *secEvent) {
	var err error
	if ev != nil && ev.err != nil {
		if st, ok := t.(stageTask); ok {
			bit := uint32(1) << st.currentStage()
			if ig, ok := t.(ignoreErrTask); ok && ig.ignoreErrMask()&bit != 0 {
				ev = &secEvent{kind: ev.kind, tag: ev.tag}
			} else if rt, ok := t.(rawErrTask); !ok || rt.rawErrMask()&bit == 0 {
				err = ev.err
			}
		}
	}
	if err == nil {
		if ft, ok := t.(fgCheckTask); ok {
			bit := uint32(1) << ft.currentStage()
			if ev != nil && ev.kind == evStart {
				bit = fgOnStart
			}
			if ft.fgCheckMask()&bit != 0 {
				err = e.checkFG(ft.fgGenVal())
			}
		}
	}
	if err == nil {
		err = t.step(e, ev)
	}
	if err != nil {
		if f, ok := t.(taskFailer); ok {
			f.onError(e, err)
			return
		}
		if tg, ok := t.(taskTargeter); ok {
			e.failTarget(tg.taskTarget(), err)
		}
	}
}

func (e *Engine) finishTaskEv(nowMs int64, opID uint64, errStr string, se secEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if w, ok := e.takeTask(opID); ok && w.task != nil {
		se.tag = w.tag
		if errStr != "" {
			se.err = errors.New(errStr)
		}
		e.stepTask(w.task, &se)
	}
	return e.DrainEffects()
}

type queuedOp struct {
	actionID  uint64
	requireFG bool
	task      opTask
}

type taskWait struct {
	task       secTask
	target     taskTarget
	tag        uint8
	cachePeerX [32]byte
	cachePeer  bool
}

type entropyTaskWait struct {
	need int
	task secTask
	tag  uint8
}

// AsyncSink receives tagged async completions from Engine without per-call closures.
type AsyncSink interface {
	OnOpenBatchDone(tag uint64, results []OpenResult)
	OnSealEnvelopeDone(tag uint64, res SealResult, err error)
	OnSyncStatesDone(tag uint64, err error)
	OnKeySignDone(tag uint64, sig []byte, err error)
	OnKeyAgreeDone(tag uint64, shared []byte, err error)
	OnSecurityStateChanged()
}

type taskTargetKind uint8

const (
	targetNone taskTargetKind = iota
	targetTask
	targetAction
	targetActionStatus
	targetActionStatusOnOK
	targetSerializedOp
	targetCbErr
	targetSinkSync
)

type taskTarget struct {
	kind     taskTargetKind
	task     secTask
	tag      uint8
	actionID uint64
	cbErr    func(error)
}

func parentTarget(task secTask, tag uint8) taskTarget {
	return taskTarget{kind: targetTask, task: task, tag: tag}
}

func serializedTarget(actionID uint64) taskTarget {
	return taskTarget{kind: targetSerializedOp, actionID: actionID}
}

func actionTarget(actionID uint64, kind taskTargetKind) taskTarget {
	return taskTarget{kind: kind, actionID: actionID}
}

//go:noinline
func (e *Engine) completeTarget(t taskTarget, ev *secEvent) {
	if ev == nil {
		ev = &secEvent{}
	}
	switch t.kind {
	case targetTask:
		if t.task != nil {
			ev.kind, ev.tag = evChildDone, t.tag
			e.stepTask(t.task, ev)
		}
	case targetSerializedOp:
		e.ensureWF().opBusy = false
		e.emitActionResult(t.actionID, ev.wfRes, ev.err)
		e.emitStatusChanged()
		e.pumpNextOp()
	case targetAction:
		e.emitActionResult(t.actionID, ev.wfRes, ev.err)
	case targetActionStatus:
		e.emitActionResult(t.actionID, ev.wfRes, ev.err)
		e.emitStatusChanged()
	case targetActionStatusOnOK:
		e.emitActionResult(t.actionID, ev.wfRes, ev.err)
		if ev.err == nil {
			e.emitStatusChanged()
		}
	case targetCbErr:
		if t.cbErr != nil {
			t.cbErr(ev.err)
		}
	case targetSinkSync:
		if wf := e.wf; wf != nil && wf.asyncSink != nil {
			wf.asyncSink.OnSyncStatesDone(t.actionID, ev.err)
		}
	}
}

//go:noinline
func (e *Engine) failTarget(t taskTarget, err error) {
	e.completeTarget(t, &secEvent{err: err, rpcErr: asRpcError(err)})
}

//go:noinline
func (e *Engine) okTarget(t taskTarget, res *corev1.SecurityWorkflowResultEffect) {
	e.completeTarget(t, &secEvent{wfRes: res})
}

//go:noinline
func (e *Engine) discardCandidatePair(edSlot, xSlot string) {
	if edSlot != "" {
		e.reqKeyDiscard(keyCandEd, edSlot)
	}
	if xSlot != "" {
		e.reqKeyDiscard(keyCandX, xSlot)
	}
}

//go:noinline
func (e *Engine) emitTokenUpdated(token string, expiresMs int64, cleared bool) {
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityTokenUpdated{
			SecurityTokenUpdated: &corev1.SecurityTokenUpdatedEffect{Token: token, ExpiresMs: expiresMs, Cleared: cleared},
		},
	})
}

//go:noinline
func (e *Engine) fireNotifyRecord(rec *PersistedRecord, head devicesec.State, rev string) {
	if ntf := e.buildNotifyRecordBytes(rec, head); len(ntf) > 0 {
		e.fireStorage(&corev1.SecurityStorageEffect{
			Op: corev1.SecurityStorageOp_SECURITY_STORAGE_OP_WRITE_CAS, Bucket: bktNotify, Key: "notify", NextRevision: rev, Data: ntf,
		})
	}
}

//go:noinline
func (e *Engine) installCommittedAccount(server, rev string, rec *PersistedRecord, prof *PersistedPublicProfile, st *storageReply, fallbackEd, fallbackX []byte) {
	wf := e.ensureWF()
	wf.server, wf.record, wf.recordRev = server, rec, rev
	wf.pendingOp, wf.pendingOpRev, wf.regIntent, wf.regIntentRev, wf.cachedToken, wf.tokenExpiresMs = nil, "", nil, "", "", 0
	wf.publicProfiles[PublicProfileStorageKey(server, rec.Pin)] = prof
	wf.legacyRepairNotice, wf.deviceEdPresent, wf.deviceXPresent = false, true, true
	if st != nil && len(st.deviceEdPub) == 32 {
		wf.deviceEdPub = slices.Clone(st.deviceEdPub)
	} else if len(fallbackEd) == 32 {
		wf.deviceEdPub = slices.Clone(fallbackEd)
	}
	if st != nil && len(st.deviceXPub) == 32 {
		wf.deviceXPub = slices.Clone(st.deviceXPub)
	} else if len(fallbackX) == 32 {
		wf.deviceXPub = slices.Clone(fallbackX)
	}
}

func (e *Engine) regTask(w taskWait) uint64 {
	if e.tasks == nil {
		e.tasks = make(map[uint64]taskWait)
	}
	opID := e.allocOp()
	e.tasks[opID] = w
	return opID
}

func (e *Engine) takeTask(opID uint64) (taskWait, bool) {
	if e.tasks == nil {
		return taskWait{}, false
	}
	w, ok := e.tasks[opID]
	if ok {
		delete(e.tasks, opID)
	}
	return w, ok
}

//go:noinline
func (e *Engine) waitSign(kind corev1.SecurityKeyKind, slot string, msg []byte, task secTask, tag uint8) {
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityKeySign{
			SecurityKeySign: &corev1.SecurityKeySignEffect{OpId: e.regTask(taskWait{task: task, tag: tag}), KeyKind: kind, Slot: slot, Message: slices.Clone(msg)},
		},
	})
}

//go:noinline
func (e *Engine) waitAgree(kind corev1.SecurityKeyKind, slot string, peerX [32]byte, cachePeer bool, task secTask, tag uint8) {
	if cachePeer && kind == keyDevX && slot == "" {
		if sec, ok := e.peerShared[peerX]; ok {
			e.stepTask(task, &secEvent{kind: evAgree, tag: tag, bytes: slices.Clone(sec[:])})
			return
		}
	}
	opID := e.regTask(taskWait{task: task, tag: tag, cachePeerX: peerX, cachePeer: cachePeer && kind == keyDevX && slot == ""})
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityKeyAgree{
			SecurityKeyAgree: &corev1.SecurityKeyAgreeEffect{OpId: opID, KeyKind: kind, Slot: slot, PeerPublic: slices.Clone(peerX[:])},
		},
	})
}

//go:noinline
func (e *Engine) waitBatch(items []*corev1.SecurityCryptoBatchItem, task secTask, tag uint8) {
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityCryptoBatch{SecurityCryptoBatch: &corev1.SecurityCryptoBatchEffect{OpId: e.regTask(taskWait{task: task, tag: tag}), Items: items}},
	})
}

//go:noinline
func (e *Engine) waitKeyGen(kind corev1.SecurityKeyKind, slot string, persist bool, task secTask, tag uint8) {
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityKeyGenerate{
			SecurityKeyGenerate: &corev1.SecurityKeyGenerateEffect{OpId: e.regTask(taskWait{task: task, tag: tag}), KeyKind: kind, Slot: slot, Persist: persist},
		},
	})
}

//go:noinline
func (e *Engine) waitEncap(recipients []*corev1.SecurityEncapsulateRecipient, task secTask, tag uint8) {
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityKeyEncapsulate{
			SecurityKeyEncapsulate: &corev1.SecurityKeyEncapsulateEffect{OpId: e.regTask(taskWait{task: task, tag: tag}), Recipients: recipients},
		},
	})
}

//go:noinline
func (e *Engine) waitStorage(eff *corev1.SecurityStorageEffect, task secTask, tag uint8) {
	if task != nil {
		eff.OpId = e.regTask(taskWait{task: task, tag: tag})
	} else {
		eff.OpId = e.allocOp()
	}
	e.out = append(e.out, &corev1.Effect{Effect: &corev1.Effect_SecurityStorage{SecurityStorage: eff}})
}

func (e *Engine) fireStorage(eff *corev1.SecurityStorageEffect) { e.waitStorage(eff, nil, 0) }

//go:noinline
func (e *Engine) waitRPC(method corev1.SecurityRpcMethod, server, token string, reqBytes []byte, task secTask, tag uint8) {
	var opID uint64
	if task != nil {
		opID = e.regTask(taskWait{task: task, tag: tag})
	} else {
		opID = e.allocOp()
	}
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityRpc{
			SecurityRpc: &corev1.SecurityRpcEffect{OpId: opID, Method: method, Server: server, AuthToken: token, RequestBytes: reqBytes},
		},
	})
}

func (e *Engine) fireRPC(method corev1.SecurityRpcMethod, server, token string, reqBytes []byte) {
	e.waitRPC(method, server, token, reqBytes, nil, 0)
}

type wireMarshaler interface{ AppendMarshal([]byte) []byte }

//go:noinline
func (e *Engine) waitProtoRPC(method corev1.SecurityRpcMethod, server, token string, msg wireMarshaler, task secTask) {
	e.waitRPC(method, server, token, msg.AppendMarshal(nil), task, 0)
}

//go:noinline
func (e *Engine) fireProtoRPC(method corev1.SecurityRpcMethod, server, token string, msg wireMarshaler) {
	e.fireRPC(method, server, token, msg.AppendMarshal(nil))
}

//go:noinline
func (e *Engine) callAuthedProtoRPC(method corev1.SecurityRpcMethod, server string, fgGen uint64, msg wireMarshaler, target taskTarget) {
	e.callAuthedRpc(method, server, "", fgGen != 0, fgGen, msg.AppendMarshal(nil), target)
}

//go:noinline
func (e *Engine) readBootstrapStorage(task secTask) {
	e.waitStorage(&corev1.SecurityStorageEffect{Op: corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP}, task, 0)
}

//go:noinline
func (e *Engine) writeStorageCAS(bucket corev1.SecurityStoreBucket, key, expectedRev, nextRev string, createOnly bool, data []byte, task secTask) {
	e.waitStorage(&corev1.SecurityStorageEffect{
		Op: corev1.SecurityStorageOp_SECURITY_STORAGE_OP_WRITE_CAS, Bucket: bucket, Key: key,
		ExpectedRevision: expectedRev, NextRevision: nextRev, CreateOnly: createOnly, Data: data,
	}, task, 0)
}

//go:noinline
func (e *Engine) deleteStorageCAS(bucket corev1.SecurityStoreBucket, key, expectedRev string, task secTask) {
	e.waitStorage(&corev1.SecurityStorageEffect{
		Op: corev1.SecurityStorageOp_SECURITY_STORAGE_OP_DELETE_CAS, Bucket: bucket, Key: key, ExpectedRevision: expectedRev,
	}, task, 0)
}

//go:noinline
func (e *Engine) fireDeleteStorage(bucket corev1.SecurityStoreBucket, key string) {
	e.fireStorage(&corev1.SecurityStorageEffect{Op: corev1.SecurityStorageOp_SECURITY_STORAGE_OP_DELETE_CAS, Bucket: bucket, Key: key})
}

//go:noinline
func (e *Engine) batchCommitStorage(promoteEd, promoteX string, discard []string, clearLogin bool, batch []*corev1.SecurityStorageWrite, task secTask) {
	e.waitStorage(&corev1.SecurityStorageEffect{
		Op: corev1.SecurityStorageOp_SECURITY_STORAGE_OP_BATCH_COMMIT, PromoteEdSlot: promoteEd, PromoteXSlot: promoteX,
		DiscardSlots: discard, ClearLoginToken: clearLogin, Batch: batch,
	}, task, 0)
}

func (e *Engine) newPreparedOp(server, memberID, kind, action string, parent devicesec.Hash) *PersistedOperation {
	return &PersistedOperation{ID: e.nextWorkflowToken(), Revision: 1, Server: server, MemberID: memberID, Kind: kind, Parent: parent, Phase: "PREPARED", Action: action}
}

//go:noinline
func buildAccountCommitWrites(
	server string, pin devicesec.Pin, profBytes, recBytes []byte,
	createRec bool, expectedRecRev, nextRev, expectedOpRev string,
	removeOp bool, expectedIntentRev, clearPairingKey string,
) []*corev1.SecurityStorageWrite {
	batch := []*corev1.SecurityStorageWrite{
		{Bucket: bktProf, Key: PublicProfileStorageKey(server, pin), NextRevision: nextRev, Data: profBytes},
		{Bucket: bktRec, Key: "record", CreateOnly: createRec, ExpectedRevision: expectedRecRev, NextRevision: nextRev, Data: recBytes},
	}
	if removeOp {
		batch = append(batch, &corev1.SecurityStorageWrite{Bucket: bktOp, Key: "pending", ExpectedRevision: expectedOpRev, Remove: true})
	}
	if expectedIntentRev != "" {
		batch = append(batch, &corev1.SecurityStorageWrite{Bucket: bktIntent, Key: "intent", ExpectedRevision: expectedIntentRev, Remove: true})
	}
	if clearPairingKey != "" {
		batch = append(batch, &corev1.SecurityStorageWrite{Bucket: bktCand, Key: clearPairingKey, Remove: true})
	}
	return batch
}

//go:noinline
func (e *Engine) waitEntropy(minBytes int, task secTask, tag uint8) {
	if e.entropy.Avail() >= minBytes {
		e.stepTask(task, &secEvent{kind: evEntropy, tag: tag})
		return
	}
	wf := e.ensureWF()
	wf.entropyWait = append(wf.entropyWait, entropyTaskWait{need: minBytes, task: task, tag: tag})
	e.emitEntropyNeed(minBytes)
}

func (e *Engine) runOpTask(actionID uint64, requireFG bool, task opTask) {
	wf := e.ensureWF()
	item := queuedOp{actionID: actionID, requireFG: requireFG, task: task}
	if wf.opBusy {
		wf.opQueue = append(wf.opQueue, item)
		return
	}
	wf.opBusy = true
	e.startQueuedOp(item)
}

func (e *Engine) startQueuedOp(item queuedOp) {
	wf, target := e.ensureWF(), serializedTarget(item.actionID)
	if item.requireFG && !wf.foreground {
		e.failTarget(target, fatalWfError("foreground", text.KeySecurityThePageIsPausedReturnToTheForegroundTo, ""))
		return
	}
	item.task.setOpContext(wf.fgGen, target)
	e.stepTask(item.task, &secEvent{kind: evStart})
}

func (e *Engine) pumpNextOp() {
	wf := e.ensureWF()
	if wf.pumpingOps {
		return
	}
	wf.pumpingOps = true
	for !wf.opBusy && len(wf.opQueue) > 0 {
		next := wf.opQueue[0]
		wf.opQueue, wf.opBusy = wf.opQueue[1:], true
		e.startQueuedOp(next)
	}
	wf.pumpingOps = false
}

type cbKeyTask struct {
	fn    func([]byte, error)
	tag   uint64
	agree bool
}

func (t *cbKeyTask) step(e *Engine, ev *secEvent) error {
	if t.fn != nil {
		t.fn(ev.bytes, ev.err)
		return nil
	}
	if wf := e.wf; wf != nil && wf.asyncSink != nil {
		if t.agree {
			wf.asyncSink.OnKeyAgreeDone(t.tag, ev.bytes, ev.err)
		} else {
			wf.asyncSink.OnKeySignDone(t.tag, ev.bytes, ev.err)
		}
	}
	return nil
}
