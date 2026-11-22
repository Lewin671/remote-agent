package security

import (
	"bytes"
	"errors"
	"slices"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

const maxSyncPages = 512

type recordFlagPatch struct {
	needsRotation          *bool
	recoveryObjectsPending *bool
}

func clearRecoveryObjectsPatch() *recordFlagPatch {
	f := false
	return &recordFlagPatch{recoveryObjectsPending: &f}
}

func clearNeedsRotationPatch() *recordFlagPatch {
	f := false
	return &recordFlagPatch{needsRotation: &f}
}

type installRecordTask struct {
	taskBase
	states      []devicesec.State
	rawStates   [][]byte
	rawProofs   [][]byte
	leaseBytes  []byte
	touchNowMs  int64
	extraFlags  *recordFlagPatch
	attempt     int
	next        *PersistedRecord
	nextRev     string
	newHead     devicesec.State
	wroteNotify bool
}

//go:noinline
func (e *Engine) installRecordCAS(
	states []devicesec.State, rawStates, rawProofs [][]byte, leaseBytes []byte,
	touchNowMs int64, extraFlags *recordFlagPatch, target taskTarget,
) {
	(&installRecordTask{
		taskBase: taskBase{target: target}, states: states, rawStates: rawStates,
		rawProofs: rawProofs, leaseBytes: leaseBytes, touchNowMs: touchNowMs, extraFlags: extraFlags,
	}).beginAttempt(e)
}

func (t *installRecordTask) beginAttempt(e *Engine) {
	if t.attempt >= 4 {
		e.failTarget(t.target, devicesec.ErrStale)
		return
	}
	t.stage = 0
	e.waitStorage(&corev1.SecurityStorageEffect{
		Op: corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ, Bucket: bktRec, Key: "record",
	}, t, 0)
}

func (t *installRecordTask) rawErrMask() uint32 { return 1 << 1 }

func (t *installRecordTask) step(e *Engine, ev *secEvent) error {
	wf := e.ensureWF()
	switch t.stage {
	case 0: // read
		if !ev.storage.found || len(ev.storage.data) == 0 {
			return ErrNotInitialized
		}
		cur, err := DecodePersistedRecord(ev.storage.data)
		if err != nil {
			return err
		}
		if cur.Pin != e.pin || cur.MemberID != e.selfDeviceID {
			return devicesec.ErrStale
		}
		curStates, _, _, err := parseAndVerifyChain(cur.Pin, cur.States, cur.RecoveryProofs)
		if err != nil {
			return err
		}
		curHead, next := curStates[len(curStates)-1], cur.Clone()
		e.syncUnlockedSecretsToRecord(next)
		if t.touchNowMs > next.FloorMs {
			next.FloorMs = t.touchNowMs
		}
		mergedStates := curStates
		if len(t.states) > 0 {
			var newRaw, newProofs [][]byte
			for i, s := range t.states {
				var rp []byte
				if i < len(t.rawProofs) {
					rp = t.rawProofs[i]
				}
				if s.Core.Epoch <= curHead.Core.Epoch {
					idx := int(s.Core.Epoch - 1)
					if idx < 0 || idx >= len(curStates) || !sameBytes(curStates[idx].Signature, s.Signature) {
						return devicesec.ErrInvalid
					}
					continue
				}
				var rs *devicesec.RecoveryStatement
				if len(rp) > 0 {
					parsed, err := devicesec.ParseRecoveryStatement(rp)
					if err != nil {
						return err
					}
					rs, next.RecoveryObjectsPending = &parsed, true
				}
				if err := devicesec.VerifyTransition(mergedStates[len(mergedStates)-1], s, rs); err != nil {
					return err
				}
				mergedStates = append(mergedStates, s)
				newRaw, newProofs = append(newRaw, slices.Clone(t.rawStates[i])), append(newProofs, slices.Clone(rp))
			}
			next.States, next.RecoveryProofs = append(next.States, newRaw...), append(next.RecoveryProofs, newProofs...)
		}
		newHead := mergedStates[len(mergedStates)-1]
		newHeadHash, _ := newHead.Hash()
		_, next.Member = newHead.Core.Member(next.MemberID)
		if len(t.leaseBytes) > 0 {
			l, err := devicesec.ParseLease(t.leaseBytes)
			if err != nil || l.Verify(newHead) != nil {
				return devicesec.ErrInvalid
			}
			if l.IssuedAt > uint64(max(1, next.FloorMs))+devicesec.MaxClockSkew {
				return devicesec.ErrClock
			}
			if len(cur.Lease) > 0 {
				if oldL, err := devicesec.ParseLease(cur.Lease); err == nil && oldL.Head == newHeadHash {
					if oldL.IssuedAt > l.IssuedAt || oldL.NotAfter > l.NotAfter || (oldL.IssuedAt == l.IssuedAt && oldL.NotAfter != l.NotAfter) {
						return devicesec.ErrStale
					}
				}
			}
			next.Lease = slices.Clone(t.leaseBytes)
		} else if len(next.Lease) > 0 {
			if oldL, err := devicesec.ParseLease(next.Lease); err != nil || oldL.Head != newHeadHash {
				next.Lease = nil
			}
		}
		if t.extraFlags != nil {
			if t.extraFlags.needsRotation != nil {
				next.NeedsRecoveryRotation = *t.extraFlags.needsRotation
			}
			if t.extraFlags.recoveryObjectsPending != nil {
				next.RecoveryObjectsPending = *t.extraFlags.recoveryObjectsPending
			}
		}
		encoded, err := next.Encode()
		if err != nil {
			return err
		}
		nextRev := e.nextWorkflowToken()
		writes := []*corev1.SecurityStorageWrite{{Bucket: bktRec, Key: "record", ExpectedRevision: ev.storage.revision, NextRevision: nextRev, Data: encoded}}
		wroteNotify := false
		if next.Member {
			if ntf := e.buildNotifyRecordBytes(next, newHead); len(ntf) > 0 {
				wroteNotify = true
				writes = append(writes, &corev1.SecurityStorageWrite{Bucket: bktNotify, Key: "notify", NextRevision: nextRev, Data: ntf})
			}
		} else {
			writes = append(writes, &corev1.SecurityStorageWrite{Bucket: bktNotify, Key: "notify", Remove: true})
		}
		t.next, t.nextRev, t.newHead, t.wroteNotify, t.stage = next, nextRev, newHead, wroteNotify, 1
		e.batchCommitStorage("", "", nil, false, writes, t)
	case 1: // write
		if ev.storage != nil && ev.storage.conflict {
			t.attempt++
			t.beginAttempt(e)
			return nil
		}
		if ev.err != nil {
			return ev.err
		}
		wf.record, wf.recordRev, t.stage = t.next, t.nextRev, 2
		e.applyPersistedRecordToEngine(t.next, parentTarget(t, 0))
	case 2: // apply
		if t.next.Member && !t.wroteNotify {
			e.fireNotifyRecord(t.next, t.newHead, "")
		}
		e.completeTarget(t.target, &secEvent{rec: t.next, err: ev.err})
	}
	return nil
}

// SyncStates runs Core-driven state synchronization and drains emitted effects.
func (e *Engine) SyncStates(nowMs int64, touch bool, authToken string, onDone func(err error)) []*corev1.Effect {
	e.touchNow(nowMs)
	e.syncStates(touch, authToken, taskTarget{kind: targetCbErr, cbErr: onDone})
	return e.DrainEffects()
}

// SyncStatesTag runs Core-driven state synchronization routed to AsyncSink.OnSyncStatesDone.
func (e *Engine) SyncStatesTag(nowMs int64, touch bool, authToken string, tag uint64) []*corev1.Effect {
	e.touchNow(nowMs)
	e.syncStates(touch, authToken, taskTarget{kind: targetSinkSync, actionID: tag})
	return e.DrainEffects()
}

type syncStatesTask struct {
	server string
	pages  int
	touch  bool
	cur    *PersistedRecord
	head   devicesec.State
	stage  uint8
}

func (t *syncStatesTask) onError(e *Engine, err error) { e.finishSyncWaiters(err) }

//go:noinline
func (e *Engine) syncStates(touch bool, authToken string, target taskTarget) {
	wf := e.ensureWF()
	if target.kind != targetNone {
		wf.syncWaiters = append(wf.syncWaiters, target)
	}
	if wf.syncInFlight {
		return
	}
	wf.syncInFlight = true
	server := e.effectiveServer()
	if server == "" || wf.record == nil || !e.hasHead {
		if !wf.bootstrapped {
			e.readBootstrapStorage(&bootstrapTask{})
		}
		e.finishSyncWaiters(ErrNotInitialized)
		return
	}
	if authToken != "" {
		wf.cachedToken = authToken
	}
	(&syncStatesTask{server: server, touch: touch}).nextPage(e)
}

func (e *Engine) finishSyncWaiters(err error) {
	wf := e.ensureWF()
	wf.syncInFlight = false
	waiters := wf.syncWaiters
	wf.syncWaiters = nil
	ev := &secEvent{err: err}
	for _, w := range waiters {
		e.completeTarget(w, ev)
	}
}

func (t *syncStatesTask) nextPage(e *Engine) {
	wf := e.ensureWF()
	if t.pages >= maxSyncPages {
		wf.recoveryStatusStale = true
		e.finishSyncWaiters(devicesec.ErrCapacity)
		return
	}
	cur := wf.record
	if cur == nil {
		e.finishSyncWaiters(ErrNotInitialized)
		return
	}
	t.cur = cur
	if t.touch && e.lastNowMs > cur.FloorMs {
		t.stage = 0
		e.installRecordCAS(nil, nil, nil, nil, e.lastNowMs, nil, parentTarget(t, 0))
		return
	}
	t.fetchAfterTouch(e, cur)
}

func (t *syncStatesTask) fetchAfterTouch(e *Engine, rec *PersistedRecord) {
	if rec == nil || len(rec.States) == 0 {
		e.finishSyncWaiters(ErrNotInitialized)
		return
	}
	head, err := devicesec.ParseState(rec.States[len(rec.States)-1])
	if err != nil {
		e.finishSyncWaiters(err)
		return
	}
	t.cur, t.head, t.stage = rec, head, 1
	e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES, t.server, 0, &v1.GetSecurityStatesRequest{
		AfterEpoch: head.Core.Epoch, Limit: 8,
	}, parentTarget(t, 0))
}

func (t *syncStatesTask) step(e *Engine, ev *secEvent) error {
	wf := e.ensureWF()
	switch t.stage {
	case 0: // touch
		if ev.err != nil {
			return ev.err
		}
		t.fetchAfterTouch(e, ev.rec)
	case 1: // fetch
		if ev.err != nil {
			wf.recoveryStatusStale = true
			return ev.err
		}
		var resp v1.GetSecurityStatesResponse
		if resp.Unmarshal(ev.bytes) != nil {
			wf.recoveryStatusStale = true
			return devicesec.ErrInvalid
		}
		e.recoveryPending, e.recoveryReadyMs, wf.recoveryStatusStale = resp.RecoveryPending, int64(resp.RecoveryReadyMs), false
		if resp.HeadEpoch < t.head.Core.Epoch {
			return devicesec.ErrStale
		}
		if len(resp.States) == 0 {
			if resp.HeadEpoch > t.head.Core.Epoch {
				return devicesec.ErrStale
			}
			if len(resp.Lease) > 0 && !sameLeaseBytes(resp.Lease, t.cur.Lease) {
				t.stage = 2
				e.installRecordCAS(nil, nil, nil, resp.Lease, e.lastNowMs, nil, parentTarget(t, 1))
				return nil
			}
			e.afterSyncSuccess(t.cur)
			return nil
		}
		parsed, rawStates, rawProofs := make([]devicesec.State, 0, len(resp.States)), make([][]byte, 0, len(resp.States)), make([][]byte, 0, len(resp.States))
		for _, ss := range resp.States {
			if ss == nil || len(ss.State) == 0 {
				return devicesec.ErrInvalid
			}
			st, err := devicesec.ParseState(ss.State)
			if err != nil {
				return err
			}
			parsed, rawStates, rawProofs = append(parsed, st), append(rawStates, ss.State), append(rawProofs, ss.Recovery)
		}
		var pageLease []byte
		if len(resp.Lease) > 0 && len(parsed) > 0 {
			if l, lErr := devicesec.ParseLease(resp.Lease); lErr == nil && l.Verify(parsed[len(parsed)-1]) == nil {
				pageLease = resp.Lease
			}
		}
		t.stage = 2
		e.installRecordCAS(parsed, rawStates, rawProofs, pageLease, e.lastNowMs, nil, parentTarget(t, 2))
	case 2: // install
		if ev.err != nil {
			return ev.err
		}
		if ev.tag == 1 {
			e.afterSyncSuccess(ev.rec)
			return nil
		}
		if ev.rec != nil && len(ev.rec.States) <= len(t.cur.States) {
			return devicesec.ErrStale
		}
		t.pages++
		t.nextPage(e)
	}
	return nil
}

func (e *Engine) afterSyncSuccess(rec *PersistedRecord) {
	wf := e.ensureWF()
	e.verifiedNetGen = e.netGen
	if rec != nil && !rec.Member {
		wf.cachedToken, wf.tokenExpiresMs = "", 0
		e.emitTokenUpdated("", 0, true)
	}
	if rec != nil && rec.Member && rec.RecoveryObjectsPending {
		e.republishRecoveryObjects(taskTarget{})
	}
	if rec != nil && e.isAuthority() && wf.foreground && !wf.opBusy && !e.isClockSkewed(e.lastNowMs) {
		if wf.pendingOp == nil || wf.pendingOp.Kind == "offer" || wf.pendingOp.Kind == "delete-records" {
			var curLease *devicesec.LeaseCertificate
			if e.hasLease && e.lease.Head == e.headHash {
				l := e.lease
				curLease = &l
			}
			if needsLeaseRenewal(curLease, e.effectiveNow(e.lastNowMs)) && (wf.lastLeaseRenewAttemptMs == 0 || e.lastNowMs-wf.lastLeaseRenewAttemptMs >= 60_000) {
				wf.lastLeaseRenewAttemptMs = e.lastNowMs
				e.runOpTask(0, true, &renewLeaseTask{force: false})
			}
		}
	}
	e.finishSyncWaiters(nil)
}

type republishObjsTask struct {
	taskBase
	server, boundaryErr string
	boundary            uint64
	attempt             int
	objRev              uint64
	rawPlain            []byte
}

//go:noinline
func (e *Engine) republishRecoveryObjects(target taskTarget) {
	wf := e.ensureWF()
	rec := wf.record
	if rec == nil || !rec.Member || !e.hasHead {
		e.emitStatusChanged()
		e.okTarget(target, nil)
		return
	}
	var boundary uint64
	for i := len(e.states) - 1; i >= 0; i-- {
		if i < len(e.recoveryAt) && e.recoveryAt[i] {
			boundary = e.states[i].Core.ContentEpoch
			break
		}
	}
	t := &republishObjsTask{taskBase: taskBase{target: target}, server: e.effectiveServer(), boundary: boundary}
	if boundary == 0 {
		t.clearFlag(e)
		return
	}
	t.tryAttempt(e)
}

func (t *republishObjsTask) onError(e *Engine, err error) {
	if err == nil {
		e.emitStatusChanged()
	}
	e.failTarget(t.target, err)
}

func (t *republishObjsTask) clearFlag(e *Engine) {
	t.stage = 5
	e.installRecordCAS(nil, nil, nil, nil, 0, clearRecoveryObjectsPatch(), parentTarget(t, 0))
}

func (t *republishObjsTask) tryAttempt(e *Engine) {
	if t.attempt >= 5 {
		t.onError(e, devicesec.ErrStale)
		return
	}
	t.stage = 0
	e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_ACCOUNT_PREFS, t.server, 0, &v1.GetAccountPrefsRequest{ObjectId: "project-prefs"}, parentTarget(t, 0))
}

func (t *republishObjsTask) step(e *Engine, ev *secEvent) error {
	const prefsObj = "project-prefs"
	switch t.stage {
	case 0: // get
		var resp v1.GetAccountPrefsResponse
		if resp.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		obj := resp.Object
		if obj == nil || obj.Envelope == nil || obj.Revision == 0 {
			t.clearFlag(e)
			return nil
		}
		t.objRev, t.stage = obj.Revision, 1
		e.startOpenBatchInternal(e.lastNowMs, []OpenItem{{Env: obj.Envelope, ExpectedPurpose: devicesec.Prefs, RequestID: prefsObj, Seq: obj.Revision, History: true}}, parentTarget(t, 0))
	case 1: // open
		r := ev.opened[0]
		if r.Err != nil {
			if errors.Is(r.Err, ErrRecoveryHistoryUnavailable) {
				t.clearFlag(e)
				return nil
			}
			return r.Err
		}
		if r.Header.ContentEpoch >= t.boundary {
			t.clearFlag(e)
			return nil
		}
		pp, ok := r.Body.(*v1.ProjectPrefs)
		if !ok || pp == nil {
			return devicesec.ErrInvalid
		}
		pp.Schema = 1
		raw, err := pp.Marshal()
		if err != nil {
			return err
		}
		t.rawPlain, t.stage = raw, 2
		e.waitEntropy(64, t, 0)
	case 2: // entropy
		t.stage = 3
		e.startSealEnvelopeInternal(e.lastNowMs, devicesec.Prefs, "", "", prefsObj, t.objRev+1, 0, t.rawPlain, false, nil, parentTarget(t, 0))
	case 3: // seal
		t.stage = 4
		e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_ACCOUNT_PREFS, t.server, 0, &v1.PutAccountPrefsRequest{
			ObjectId: prefsObj, ExpectedRevision: t.objRev, Envelope: ev.seal.Envelope,
		}, parentTarget(t, 0))
	case 4: // put
		var pr v1.PutAccountPrefsResponse
		if pr.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		if pr.Applied {
			t.clearFlag(e)
			return nil
		}
		t.attempt++
		t.tryAttempt(e)
	case 5: // clear
		t.onError(e, nil)
	}
	return nil
}

func sameLeaseBytes(a, b []byte) bool { return bytes.Equal(a, b) }
