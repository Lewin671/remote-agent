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
	addPendingRemove       []string
	setPendingRemove       bool
	pendingRemove          []string
}

func clearRecoveryObjectsPatch() *recordFlagPatch {
	f := false
	return &recordFlagPatch{recoveryObjectsPending: &f}
}

func clearNeedsRotationPatch() *recordFlagPatch {
	f := false
	return &recordFlagPatch{needsRotation: &f}
}

func addPendingRemovePatch(ids []string) *recordFlagPatch {
	return &recordFlagPatch{addPendingRemove: slices.Clone(ids)}
}

func setPendingRemovePatch(ids []string) *recordFlagPatch {
	return &recordFlagPatch{setPendingRemove: true, pendingRemove: slices.Clone(ids)}
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

const (
	installRecordRead uint8 = iota
	installRecordWrite
	installRecordApply
)

func (t *installRecordTask) beginAttempt(e *Engine) {
	if t.attempt >= 4 {
		e.failTarget(t.target, devicesec.ErrStale)
		return
	}
	t.stage = installRecordRead
	e.waitStorage(&corev1.SecurityStorageEffect{
		Op: corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ, Bucket: bktRec, Key: "record",
	}, t, 0)
}

func (t *installRecordTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil && t.stage != installRecordWrite {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case installRecordRead:
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
			if t.extraFlags.setPendingRemove {
				next.PendingRemoveDeviceIDs = slices.Clone(t.extraFlags.pendingRemove)
			}
			if len(t.extraFlags.addPendingRemove) > 0 {
				next.PendingRemoveDeviceIDs = append(next.PendingRemoveDeviceIDs, t.extraFlags.addPendingRemove...)
			}
		}
		if len(next.PendingRemoveDeviceIDs) > 0 {
			if !next.Member || newHead.Core.AuthorityID != next.MemberID {
				next.PendingRemoveDeviceIDs = nil
			} else {
				seen := make(map[string]bool, len(next.PendingRemoveDeviceIDs))
				pruned := next.PendingRemoveDeviceIDs[:0]
				for _, id := range next.PendingRemoveDeviceIDs {
					if id == "" || seen[id] || id == next.MemberID || id == newHead.Core.AuthorityID {
						continue
					}
					if m, live := newHead.Core.Member(id); !live || m.Kind != devicesec.Client {
						continue
					}
					seen[id] = true
					pruned = append(pruned, id)
				}
				if len(pruned) == 0 {
					next.PendingRemoveDeviceIDs = nil
				} else {
					next.PendingRemoveDeviceIDs = pruned
				}
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
		t.next, t.nextRev, t.newHead, t.wroteNotify, t.stage = next, nextRev, newHead, wroteNotify, installRecordWrite
		e.batchCommitStorage("", "", nil, false, writes, t)
	case installRecordWrite:
		if ev.storage != nil && ev.storage.conflict {
			t.attempt++
			t.beginAttempt(e)
			return nil
		}
		if ev.err != nil {
			return ev.err
		}
		wf.record, wf.recordRev, t.stage = t.next, t.nextRev, installRecordApply
		e.applyPersistedRecordToEngine(t.next, parentTarget(t, 0))
	case installRecordApply:
		if t.next.Member && !t.wroteNotify {
			e.fireNotifyRecord(t.next, t.newHead, "")
		}
		e.completeTarget(t.target, &secEvent{rec: t.next, err: ev.err})
	}
	return nil
}

// SyncStatesTag runs Core-driven state synchronization routed to AsyncSink.OnSyncStatesDone.
func (e *Engine) SyncStatesTag(nowMs int64, touch bool, authToken string, tag uint64) []*corev1.Effect {
	e.touchNow(nowMs)
	e.syncWait(touch, authToken, syncWaiter{hasSink: true, sinkTag: tag})
	return e.DrainEffects()
}

type syncStatesTask struct {
	server     string
	authToken  string
	pages      int
	touch      bool
	accountGen uint64
	cur        *PersistedRecord
	head       devicesec.State
	stage      uint8
}

const (
	syncStageTouch uint8 = iota
	syncStageFetch
	syncStageInstall
)

func (t *syncStatesTask) onError(e *Engine, err error) { e.finishSyncWaiters(err) }

//go:noinline
func (e *Engine) syncStates(touch bool, authToken string, target taskTarget) {
	e.syncWait(touch, authToken, syncWaiter{target: target})
}

func (e *Engine) syncWait(touch bool, authToken string, waiter syncWaiter) {
	wf := e.ensureWF()
	if waiter.hasSink || waiter.target.kind != targetNone {
		wf.syncWaiters = append(wf.syncWaiters, waiter)
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
	(&syncStatesTask{server: server, authToken: authToken, touch: touch, accountGen: wf.accountGen}).nextPage(e)
}

func (e *Engine) finishSyncWaiters(err error) {
	wf := e.ensureWF()
	wf.syncInFlight = false
	waiters := wf.syncWaiters
	wf.syncWaiters = nil
	ev := &secEvent{err: err}
	for _, w := range waiters {
		if w.hasSink {
			if wf.asyncSink != nil {
				wf.asyncSink.OnSyncStatesDone(w.sinkTag, err)
			}
			continue
		}
		e.completeTarget(w.target, ev)
	}
}

func (t *syncStatesTask) nextPage(e *Engine) {
	wf := e.ensureWF()
	if wf.accountGen != t.accountGen {
		e.finishSyncWaiters(devicesec.ErrStale)
		return
	}
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
		t.stage = syncStageTouch
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
	t.cur, t.head, t.stage = rec, head, syncStageFetch
	reqBytes := (&v1.GetSecurityStatesRequest{
		AfterEpoch: head.Core.Epoch, Limit: 8,
	}).AppendMarshal(nil)
	e.callAuthedRpc(corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_SECURITY_STATES, t.server, t.authToken, false, 0, reqBytes, parentTarget(t, 0))
}

func (t *syncStatesTask) step(e *Engine, ev *secEvent) error {
	wf := e.ensureWF()
	if wf.accountGen != t.accountGen {
		return devicesec.ErrStale
	}
	switch t.stage {
	case syncStageTouch:
		if ev.err != nil {
			return ev.err
		}
		t.fetchAfterTouch(e, ev.rec)
	case syncStageFetch:
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
				t.stage = syncStageInstall
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
		t.stage = syncStageInstall
		e.installRecordCAS(parsed, rawStates, rawProofs, pageLease, e.lastNowMs, nil, parentTarget(t, 2))
	case syncStageInstall:
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
		if wf.pendingOp == nil || wf.pendingOp.Kind == PendingOpKindOffer || wf.pendingOp.Kind == PendingOpKindDeleteRecords {
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

const (
	repubStageGet uint8 = iota
	repubStageOpen
	repubStageEntropy
	repubStageSeal
	repubStagePut
	repubStageClear
)

func (t *republishObjsTask) clearFlag(e *Engine) {
	t.stage = repubStageClear
	e.installRecordCAS(nil, nil, nil, nil, 0, clearRecoveryObjectsPatch(), parentTarget(t, 0))
}

func (t *republishObjsTask) tryAttempt(e *Engine) {
	if t.attempt >= 5 {
		t.onError(e, devicesec.ErrStale)
		return
	}
	t.stage = repubStageGet
	e.callAuthedProtoRPC(corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_ACCOUNT_PREFS, t.server, 0, &v1.GetAccountPrefsRequest{ObjectId: "project-prefs"}, parentTarget(t, 0))
}

func (t *republishObjsTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	const prefsObj = "project-prefs"
	switch t.stage {
	case repubStageGet:
		var resp v1.GetAccountPrefsResponse
		if resp.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		obj := resp.Object
		if obj == nil || obj.Envelope == nil || obj.Revision == 0 {
			t.clearFlag(e)
			return nil
		}
		t.objRev, t.stage = obj.Revision, repubStageOpen
		e.startOpenBatchInternal(e.lastNowMs, []OpenItem{{Env: obj.Envelope, ExpectedPurpose: devicesec.Prefs, RequestID: prefsObj, Seq: obj.Revision, History: true}}, parentTarget(t, 0))
	case repubStageOpen:
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
		t.rawPlain, t.stage = raw, repubStageEntropy
		e.waitEntropy(64, t, 0)
	case repubStageEntropy:
		t.stage = repubStageSeal
		e.startSealEnvelopeInternal(e.lastNowMs, devicesec.Prefs, "", "", prefsObj, t.objRev+1, 0, t.rawPlain, false, nil, parentTarget(t, 0))
	case repubStageSeal:
		t.stage = repubStagePut
		e.callAuthedProtoRPC(corev1.RelayRpcMethod_RELAY_RPC_METHOD_PUT_ACCOUNT_PREFS, t.server, 0, &v1.PutAccountPrefsRequest{
			ObjectId: prefsObj, ExpectedRevision: t.objRev, Envelope: ev.seal.Envelope,
		}, parentTarget(t, 0))
	case repubStagePut:
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
	case repubStageClear:
		t.onError(e, nil)
	}
	return nil
}

func sameLeaseBytes(a, b []byte) bool { return bytes.Equal(a, b) }
