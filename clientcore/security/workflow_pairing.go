package security

import (
	"slices"
	"strings"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

func (e *Engine) scheduleSecurityPollTimer() {
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_ScheduleTimer{ScheduleTimer: &corev1.ScheduleTimerEffect{Kind: corev1.TimerKind_TIMER_KIND_SECURITY_POLL, Key: "security-poll", DelayMs: pairPollIntervalMs}},
	})
}

func (e *Engine) cancelSecurityPollTimer() {
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_CancelTimer{CancelTimer: &corev1.CancelTimerEffect{Kind: corev1.TimerKind_TIMER_KIND_SECURITY_POLL, Key: "security-poll"}},
	})
}

func (e *Engine) clearActivePollWithError(err error) {
	wf := e.ensureWF()
	p := wf.activePoll
	wf.activePoll = nil
	e.cancelSecurityPollTimer()
	if p != nil && err != nil {
		e.emitActionResult(p.actionID, nil, err)
		e.emitStatusChanged()
	}
}

func (e *Engine) resolveActivePoll(pairingID string, res *corev1.SecurityWorkflowResultEffect, err error) {
	wf := e.ensureWF()
	cur := wf.activePoll
	if cur == nil || cur.pairingID != pairingID {
		return
	}
	wf.activePoll = nil
	e.cancelSecurityPollTimer()
	e.emitActionResult(cur.actionID, res, err)
	e.emitStatusChanged()
}

func (e *Engine) discardInitSlot(sess *initiatorPairingSession) {
	if sess != nil && sess.ephemSlot != "" {
		e.reqKeyDiscard(keyEphemX, sess.ephemSlot)
		sess.ephemSlot = ""
	}
}

func (e *Engine) resetWorkflowPairingSessions() {
	wf := e.ensureWF()
	e.clearActivePollWithError(fatalWfError("canceled", text.KeySecurityPairingCanceled, ""))
	for id, s := range wf.initPairings {
		e.discardInitSlot(s)
		delete(wf.initPairings, id)
	}
	for id, s := range wf.joinPairings {
		if s != nil {
			e.discardJoinSessionKeys(s)
		}
		delete(wf.joinPairings, id)
	}
}

func (e *Engine) discardJoinSessionKeys(s *joinerPairingSession) {
	if s.ephemSlot != "" {
		e.reqKeyDiscard(keyEphemX, s.ephemSlot)
	}
	ed := ""
	if !s.reusesDeviceEd {
		ed = s.edSlot
	}
	e.discardCandidatePair(ed, s.xSlot)
}

func initiatorSessionToProto(s *initiatorPairingSession) *corev1.SecurityPairingInfo {
	if s == nil {
		return nil
	}
	return &corev1.SecurityPairingInfo{
		PairingId: s.pairingID, Code: s.code, Url: s.url, ExpiresMs: s.expiresMs,
		SasEmoji: slices.Clone(s.sasEmoji), JoinerKind: s.candidateKind,
		Joined: s.joined, Approved: s.approved, Delivered: s.delivered,
	}
}

func joinerSessionToProto(s *joinerPairingSession) *corev1.SecurityPairingInfo {
	if s == nil {
		return nil
	}
	return &corev1.SecurityPairingInfo{
		PairingId: s.pairingID, Code: s.code, SasEmoji: slices.Clone(s.sasEmoji),
		JoinerKind: v1.DeviceKind_DEVICE_KIND_CLIENT, Joined: true, Registering: s.registering,
	}
}

type startPairingTask struct {
	taskBase
	webOrigin, replacesDeviceID, server, ephemSlot string
	trust, ephemPub, commit                        []byte
	hasEphem                                       bool
}

func (t *startPairingTask) onError(e *Engine, err error) {
	if t.hasEphem {
		e.reqKeyDiscard(keyEphemX, t.ephemSlot)
	}
	e.failTarget(t.target, err)
}

func (t *startPairingTask) beginCreate(e *Engine) {
	t.stage = 2
	e.waitEntropy(64, t, 0)
}

func (t *startPairingTask) fgCheckMask() uint32 { return fgOnStart | 1<<0 | 1<<2 }

func (t *startPairingTask) step(e *Engine, ev *secEvent) error {
	wf := e.ensureWF()
	switch t.stage {
	case 0: // sync
		if ev.kind == evStart {
			if wf.pendingOp != nil {
				return newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst)
			}
			e.syncStates(false, "", parentTarget(t, 0))
			return nil
		}
		if !e.isAuthority() || wf.record == nil {
			return devicesec.ErrUntrusted
		}
		server := e.effectiveServer()
		var acctPub []byte
		if len(wf.record.AccountPublicKey) == 32 {
			acctPub = wf.record.AccountPublicKey
		} else if prof := wf.publicProfiles[PublicProfileStorageKey(server, e.pin)]; prof != nil {
			acctPub = prof.AccountPublicKey[:]
		}
		if len(acctPub) != 32 {
			return newWfError("invalid", text.KeySecurityAccountPublicKeyMissingPairAgainOrUseA)
		}
		trust, err := devicesec.PairTrustDescriptor(e.pin, e.selfDeviceID)
		if err != nil {
			return err
		}
		t.server, t.trust = server, trust
		if t.replacesDeviceID != "" {
			if _, live := e.head.Core.Member(t.replacesDeviceID); live || !slices.Contains(e.head.Core.Removed, t.replacesDeviceID) {
				return devicesec.ErrUntrusted
			}
			var wasMachine bool
			for _, s := range e.states {
				if m, ok := s.Core.Member(t.replacesDeviceID); ok && m.Kind == devicesec.Machine {
					wasMachine = true
					break
				}
			}
			if !wasMachine {
				return devicesec.ErrUntrusted
			}
			t.stage = 1
			e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_MACHINES, server, t.fgGen, &v1.ListMachinesRequest{}, parentTarget(t, 0))
			return nil
		}
		t.beginCreate(e)
	case 1: // list machines
		var lmr v1.ListMachinesResponse
		if lmr.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		var hasArchive bool
		for _, m := range lmr.Machines {
			if m != nil && m.Id == t.replacesDeviceID {
				hasArchive = true
				break
			}
		}
		if !hasArchive {
			return newWfError("not_found", text.KeySecurityTheComputerSArchiveWasDeletedJoinAsA)
		}
		t.beginCreate(e)
	case 2: // entropy
		t.ephemSlot, t.stage = "pair-init-"+e.nextWorkflowToken(), 3
		e.waitKeyGen(keyEphemX, t.ephemSlot, false, t, 0)
	case 3: // keygen
		t.hasEphem = true
		if len(ev.bytes) != 32 {
			return devicesec.ErrInvalid
		}
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		commit := devicesec.PairDomainCommit(ev.bytes, t.trust)
		t.ephemPub, t.commit, t.stage = ev.bytes, commit, 4
		e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_CREATE_PAIRING, t.server, t.fgGen, &v1.CreatePairingRequest{Commit: commit}, parentTarget(t, 0))
	case 4: // create rpc
		var resp v1.CreatePairingResponse
		if resp.Unmarshal(ev.bytes) != nil || resp.PairingId == "" || resp.Code == "" {
			return devicesec.ErrInvalid
		}
		if len(resp.ApproverPublicKey) > 0 && wf.deviceEdPresent && !sameBytes(resp.ApproverPublicKey, wf.deviceEdPub) {
			return devicesec.ErrUntrusted
		}
		var commitArr [32]byte
		copy(commitArr[:], t.commit)
		sess := &initiatorPairingSession{
			pairingID: resp.PairingId, code: resp.Code, url: FormatPairLink(t.webOrigin, t.server, resp.Code, commitArr),
			server: t.server, expiresMs: resp.ExpiresMs, ephemSlot: t.ephemSlot, ephemPub: slices.Clone(t.ephemPub),
			trust: t.trust, replacesDeviceID: t.replacesDeviceID,
		}
		wf.initPairings[resp.PairingId] = sess
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{Pairing: initiatorSessionToProto(sess)})
	}
	return nil
}

type pollPairingInitiatorTask struct {
	taskBase
	pairingID, candID string
	sess              *initiatorPairingSession
	st                v1.GetPairingResponse
	ps                *devicesec.PairSecrets
}

func (t *pollPairingInitiatorTask) rawErrMask() uint32  { return 1 << 0 }
func (t *pollPairingInitiatorTask) fgCheckMask() uint32 { return fgOnStart | 1<<1 }

func (e *Engine) startPollPairingInitiator(fgGen uint64, pairingID string, target taskTarget) {
	e.stepTask(&pollPairingInitiatorTask{taskBase: taskBase{fgGen: fgGen, target: target}, pairingID: pairingID}, &secEvent{kind: evStart})
}

func (t *pollPairingInitiatorTask) step(e *Engine, ev *secEvent) error {
	wf := e.ensureWF()
	switch t.stage {
	case 0: // get
		if ev.kind == evStart {
			sess := wf.initPairings[t.pairingID]
			if sess == nil {
				return newWfError("not_found", text.KeySecurityPairingExpiredOrWasCanceled)
			}
			if !e.isAuthority() {
				return devicesec.ErrUntrusted
			}
			t.sess = sess
			e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_PAIRING, sess.server, t.fgGen, &v1.GetPairingRequest{PairingId: t.pairingID}, parentTarget(t, 0))
			return nil
		}
		sess := t.sess
		if ev.rpcErr != nil {
			if ev.rpcErr.code == connectCodeNotFound {
				e.discardInitSlot(sess)
				delete(wf.initPairings, t.pairingID)
				return fatalWfError("canceled", text.KeySecurityPairingExpiredOrWasCanceled, "")
			}
			return ev.err
		}
		var st v1.GetPairingResponse
		if st.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		switch st.State {
		case v1.PairingState_PAIRING_STATE_CANCELED:
			e.discardInitSlot(sess)
			delete(wf.initPairings, t.pairingID)
			return fatalWfError("canceled", text.KeySecurityPairingExpiredOrWasCanceled, "")
		case v1.PairingState_PAIRING_STATE_DELIVERED:
			e.discardInitSlot(sess)
			sess.delivered, sess.approved, sess.joined = true, true, true
			delete(wf.initPairings, t.pairingID)
		case v1.PairingState_PAIRING_STATE_JOINED:
			if sess.joined {
				break
			}
			if len(st.JoinerPublic) != 32 || len(st.DevicePublicKey) != 32 || len(st.DeviceHpkePublicKey) != 32 ||
				(st.JoinerKind != v1.DeviceKind_DEVICE_KIND_MACHINE && st.JoinerKind != v1.DeviceKind_DEVICE_KIND_CLIENT) ||
				(sess.replacesDeviceID != "" && st.JoinerKind != v1.DeviceKind_DEVICE_KIND_MACHINE) {
				return devicesec.ErrUntrusted
			}
			candID := devicesec.ID(st.DevicePublicKey)
			if !devicesec.IsDeviceID(candID) || candID == e.selfDeviceID || slices.Contains(e.head.Core.Removed, candID) {
				return devicesec.ErrUntrusted
			}
			if existing, ok := e.head.Core.Member(candID); ok {
				if existing.Kind != devicesec.Kind(st.JoinerKind) || !sameBytes(existing.EdPublic[:], st.DevicePublicKey) || sess.replacesDeviceID != "" {
					return devicesec.ErrUntrusted
				}
			}
			var peerX32 [32]byte
			copy(peerX32[:], st.JoinerPublic)
			t.st, t.candID, t.stage = st, candID, 1
			e.waitAgree(keyEphemX, sess.ephemSlot, peerX32, false, t, 0)
			return nil
		case v1.PairingState_PAIRING_STATE_APPROVED:
			sess.approved, sess.joined = true, true
		}
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{Pairing: initiatorSessionToProto(sess)})
	case 1: // agree
		sess := t.sess
		transcript := devicesec.PairDomainTranscript(sess.pairingID, sess.ephemPub, t.st.JoinerPublic, t.st.DevicePublicKey, t.st.DeviceHpkePublicKey, int32(t.st.JoinerKind), sess.trust)
		ps, err := devicesec.DerivePairSecrets(ev.bytes, transcript)
		if err != nil {
			return err
		}
		t.ps, t.stage = ps, 2
		e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_REVEAL_PAIRING, sess.server, t.fgGen, &v1.RevealPairingRequest{
			PairingId: sess.pairingID, InitiatorPublic: slices.Clone(sess.ephemPub), Trust: slices.Clone(sess.trust),
		}, parentTarget(t, 0))
	case 2: // reveal
		sess := t.sess
		e.discardInitSlot(sess)
		sess.joined = true
		sess.candidateID, sess.candidateEd, sess.candidateX = t.candID, slices.Clone(t.st.DevicePublicKey), slices.Clone(t.st.DeviceHpkePublicKey)
		sess.candidateKind, sess.pairKey, sess.sasEmoji = t.st.JoinerKind, slices.Clone(t.ps.Key), t.ps.Emoji()
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{Pairing: initiatorSessionToProto(sess)})
	}
	return nil
}

type approvePairingTask struct {
	taskBase
	pairingID    string
	sasConfirmed bool
	sess         *initiatorPairingSession
}

func (t *approvePairingTask) rawErrMask() uint32  { return 1 << 2 }
func (t *approvePairingTask) fgCheckMask() uint32 { return fgOnStart | 1<<0 }

func (t *approvePairingTask) step(e *Engine, ev *secEvent) error {
	wf := e.ensureWF()
	switch t.stage {
	case 0: // sync
		if ev.kind == evStart {
			if !t.sasConfirmed {
				return newWfError("invalid", text.KeySecurityConfirmThePatternsOnTheNewDeviceBeforeApproving)
			}
			sess := wf.initPairings[t.pairingID]
			if sess == nil || !sess.joined || len(sess.pairKey) != 32 || len(sess.candidateEd) != 32 || len(sess.candidateX) != 32 {
				return devicesec.ErrStale
			}
			if wf.pendingOp != nil {
				return newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst)
			}
			t.sess = sess
			e.syncStates(false, "", parentTarget(t, 0))
			return nil
		}
		if !e.isAuthority() {
			return devicesec.ErrUntrusted
		}
		sess := t.sess
		exp := sess.expiresMs
		if e.lastNowMs > 0 && (exp == 0 || exp > e.lastNowMs+maxPairWindowMs) {
			exp = e.lastNowMs + maxPairWindowMs
		}
		hh := e.headHash
		t.stage = 1
		op := e.newPreparedOp(sess.server, e.selfDeviceID, "state", "pair", hh)
		op.TargetDeviceID = sess.candidateID
		op.Pair = &PersistedOpPair{
			PairingID: sess.pairingID, CandidateID: sess.candidateID, CandidateEd: slices.Clone(sess.candidateEd),
			CandidateX: slices.Clone(sess.candidateX), CandidateKind: uint32(sess.candidateKind),
			PairKey: slices.Clone(sess.pairKey), ExpiresMs: exp, ReplacesDeviceID: sess.replacesDeviceID,
		}
		e.claimOperationCAS(op, &hh, e.selfDeviceID, parentTarget(t, 0))
	case 1: // claim
		t.stage = 2
		e.startResumePendingOp(t.fgGen, parentTarget(t, 0))
	case 2: // resume
		if ev.err != nil {
			e.completeTarget(t.target, &secEvent{wfRes: ev.wfRes, err: ev.err})
			return nil
		}
		t.sess.approved = true
		if wf.pendingOp == nil {
			t.sess.delivered = true
			delete(wf.initPairings, t.pairingID)
		}
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{Pairing: initiatorSessionToProto(t.sess)})
	}
	return nil
}

type finishPairingDeliveryTask struct {
	taskBase
	pairingID, pid string
}

func (t *finishPairingDeliveryTask) rawErrMask() uint32 { return 1 << 0 }

func (t *finishPairingDeliveryTask) step(e *Engine, ev *secEvent) error {
	wf := e.ensureWF()
	if ev.kind == evStart {
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if op := wf.pendingOp; op != nil && op.Kind == "state" && op.Pair != nil && (t.pairingID == "" || op.Pair.PairingID == t.pairingID) {
			t.pid = op.Pair.PairingID
			e.startResumePendingOp(t.fgGen, parentTarget(t, 0))
			return nil
		}
		e.startPollPairingInitiator(t.fgGen, t.pairingID, t.target)
		return nil
	}
	if ev.err != nil {
		e.completeTarget(t.target, &secEvent{wfRes: ev.wfRes, err: ev.err})
		return nil
	}
	sess := wf.initPairings[t.pid]
	if wf.pendingOp == nil {
		if sess != nil {
			sess.delivered, sess.approved = true, true
			delete(wf.initPairings, t.pid)
		} else {
			sess = &initiatorPairingSession{pairingID: t.pid, approved: true, delivered: true}
		}
	}
	e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{Pairing: initiatorSessionToProto(sess)})
	return nil
}

type cancelPairingInitiatorTask struct {
	taskBase
	pairingID string
}

func (t *cancelPairingInitiatorTask) ignoreErrMask() uint32 { return 1<<1 | 1<<2 }

func (t *cancelPairingInitiatorTask) cleanupDraft(e *Engine) {
	if op := e.ensureWF().pendingOp; op != nil && op.Pair != nil && op.Pair.PairingID == t.pairingID && op.Phase == "PREPARED" && len(op.Signed) == 0 {
		t.stage = 2
		e.clearOperationCAS(op, parentTarget(t, 0))
		return
	}
	e.okTarget(t.target, nil)
}

func (t *cancelPairingInitiatorTask) step(e *Engine, _ *secEvent) error {
	wf := e.ensureWF()
	switch t.stage {
	case 0: // start
		sess := wf.initPairings[t.pairingID]
		e.discardInitSlot(sess)
		delete(wf.initPairings, t.pairingID)
		srv := e.effectiveServer()
		if sess != nil && sess.server != "" {
			srv = sess.server
		}
		if srv == "" {
			t.cleanupDraft(e)
			return nil
		}
		t.stage = 1
		e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_CANCEL_PAIRING, srv, 0, &v1.CancelPairingRequest{PairingId: t.pairingID}, parentTarget(t, 0))
	case 1: // rpc
		t.cleanupDraft(e)
	case 2: // clear
		e.okTarget(t.target, nil)
	}
	return nil
}

type startJoinPairingTask struct {
	taskBase
	server, code, edSlot, xSlot, ephemSlot string
	commitHint, edPub, xPub, ephemPub      []byte
	existingRepair, reusesEd               bool
	peekResp                               v1.PeekPairingResponse
	cleanLevel                             uint8 // 0:none 1:ed 2:ed+x 3:all 4:deleteCand+all
}

func (t *startJoinPairingTask) discardSlots(e *Engine, lvl uint8) {
	if lvl >= 3 && t.ephemSlot != "" {
		e.reqKeyDiscard(keyEphemX, t.ephemSlot)
	}
	if lvl >= 4 && t.peekResp.PairingId != "" {
		e.fireDeleteStorage(bktCand, PairingCandidateStorageKey(t.server, t.peekResp.PairingId))
	}
	ed, x := "", ""
	if lvl >= 1 && !t.reusesEd {
		ed = t.edSlot
	}
	if lvl >= 2 {
		x = t.xSlot
	}
	e.discardCandidatePair(ed, x)
}

func (t *startJoinPairingTask) onError(e *Engine, err error) {
	t.discardSlots(e, t.cleanLevel)
	e.failTarget(t.target, err)
}

func (t *startJoinPairingTask) genXAndEphem(e *Engine, edPub []byte) {
	t.edPub, t.cleanLevel, t.stage = edPub, 1, 2
	e.waitKeyGen(keyCandX, t.xSlot, true, t, 0)
}

func (e *Engine) startJoinPairingAction(actionID uint64, act *corev1.SecurityStartJoinPairingAction) {
	wf := e.ensureWF()
	if act == nil {
		e.emitActionResult(actionID, nil, devicesec.ErrInvalid)
		return
	}
	rawServer, code, commitHint := act.Server, strings.ReplaceAll(strings.TrimSpace(act.Code), " ", ""), slices.Clone(act.CommitHint)
	if strings.TrimSpace(act.RawLink) != "" {
		lc, ls, lh, ok := ParsePairLink(act.RawLink)
		if !ok {
			e.emitActionResult(actionID, nil, newWfError("invalid", text.KeySecurityInvalidPairingKey))
			return
		}
		code, rawServer = lc, ls
		if len(lh) == 32 {
			commitHint = lh
		}
	}
	server, err := CheckServerURL(rawServer)
	if err != nil {
		e.emitActionResult(actionID, nil, err)
		return
	}
	if code == "" {
		e.emitActionResult(actionID, nil, devicesec.ErrInvalid)
		return
	}
	if wf.bootstrapInvalid || wf.pendingOp != nil || wf.regIntent != nil {
		e.emitActionResult(actionID, nil, newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst))
		return
	}
	if len(commitHint) != 0 && len(commitHint) != 32 {
		e.emitActionResult(actionID, nil, devicesec.ErrUntrusted)
		return
	}
	if act.ExistingRepair {
		if (act.ExistingServer != "" && act.ExistingServer != server) || wf.record == nil || wf.record.Server != server || !wf.deviceEdPresent || len(wf.deviceEdPub) != 32 {
			e.emitActionResult(actionID, nil, devicesec.ErrUntrusted)
			return
		}
	} else if wf.record != nil && wf.record.Member {
		e.emitActionResult(actionID, nil, devicesec.ErrStale)
		return
	}
	e.clearActivePollWithError(fatalWfError("canceled", text.KeySecurityPairingCanceled, ""))
	for id, old := range wf.joinPairings {
		if old != nil {
			e.discardJoinSessionKeys(old)
		}
		delete(wf.joinPairings, id)
	}
	e.waitEntropy(96, &startJoinPairingTask{
		taskBase: taskBase{target: actionTarget(actionID, targetAction)}, server: server, code: code,
		commitHint: commitHint, existingRepair: act.ExistingRepair, reusesEd: act.ExistingRepair,
	}, 0)
}

func (t *startJoinPairingTask) step(e *Engine, ev *secEvent) error {
	wf := e.ensureWF()
	switch t.stage {
	case 0: // entropy
		tok := e.nextWorkflowToken()
		if !t.reusesEd {
			t.edSlot = "join-ed-" + tok
		}
		t.xSlot, t.ephemSlot = "join-x-"+tok, "join-ephem-"+tok
		if t.reusesEd {
			t.genXAndEphem(e, slices.Clone(wf.deviceEdPub))
			return nil
		}
		t.stage = 1
		e.waitKeyGen(keyCandEd, t.edSlot, true, t, 0)
	case 1: // ed
		t.genXAndEphem(e, ev.bytes)
	case 2: // x
		t.xPub, t.cleanLevel, t.stage = ev.bytes, 2, 3
		e.waitKeyGen(keyEphemX, t.ephemSlot, false, t, 0)
	case 3: // ephem
		t.ephemPub, t.cleanLevel, t.stage = ev.bytes, 3, 4
		e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PEEK_PAIRING, t.server, "", &v1.PeekPairingRequest{Code: t.code, ProtocolVersion: protocolVersion}, t)
	case 4: // peek
		var pr v1.PeekPairingResponse
		if pr.Unmarshal(ev.bytes) != nil || pr.PairingId == "" || len(pr.Commit) != 32 {
			return devicesec.ErrInvalid
		}
		if len(t.commitHint) == 32 && !sameBytes(pr.Commit, t.commitHint) {
			return devicesec.ErrUntrusted
		}
		candBytes, err := (&PersistedPairingCandidate{
			Server: t.server, PairingID: pr.PairingId, EdSlot: t.edSlot, XSlot: t.xSlot,
			EdPublic: slices.Clone(t.edPub), XPublic: slices.Clone(t.xPub), ReusesDeviceEd: t.reusesEd,
		}).Encode()
		if err != nil {
			return err
		}
		t.peekResp, t.stage = pr, 5
		e.writeStorageCAS(bktCand, PairingCandidateStorageKey(t.server, pr.PairingId), "", e.nextWorkflowToken(), false, candBytes, t)
	case 5: // save cand
		t.cleanLevel, t.stage = 4, 6
		e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_JOIN_PAIRING, t.server, "", &v1.JoinPairingRequest{
			Code: t.code, JoinerPublic: slices.Clone(t.ephemPub), JoinerKind: v1.DeviceKind_DEVICE_KIND_CLIENT,
			DevicePublicKey: slices.Clone(t.edPub), DeviceHpkePublicKey: slices.Clone(t.xPub), ProtocolVersion: protocolVersion,
		}, t)
	case 6: // join
		var jr v1.JoinPairingResponse
		if jr.Unmarshal(ev.bytes) != nil || jr.PairingId == "" || jr.CandidateToken == "" || len(jr.Commit) != 32 {
			return devicesec.ErrInvalid
		}
		if jr.PairingId != t.peekResp.PairingId || !sameBytes(jr.Commit, t.peekResp.Commit) || (len(t.commitHint) == 32 && !sameBytes(jr.Commit, t.commitHint)) {
			return devicesec.ErrUntrusted
		}
		wf.joinPairings[jr.PairingId] = &joinerPairingSession{
			pairingID: jr.PairingId, server: t.server, code: t.code, commitHint: t.commitHint,
			commit: slices.Clone(jr.Commit), existingRepair: t.existingRepair, replace: t.existingRepair,
			ephemSlot: t.ephemSlot, ephemPub: slices.Clone(t.ephemPub), edSlot: t.edSlot, xSlot: t.xSlot,
			edPublic: slices.Clone(t.edPub), xPublic: slices.Clone(t.xPub), reusesDeviceEd: t.reusesEd, candidateToken: jr.CandidateToken,
		}
		wf.activePoll = &activePoll{kind: pollJoinRevealed, pairingID: jr.PairingId, actionID: t.target.actionID}
		e.pollJoinState(jr.PairingId, v1.PairingState_PAIRING_STATE_REVEALED)
	}
	return nil
}

func (e *Engine) confirmJoinSasAction(actionID uint64, pairingID string) {
	wf := e.ensureWF()
	sess := wf.joinPairings[pairingID]
	if sess == nil || len(sess.pairKey) != 32 || sess.registering {
		e.emitActionResult(actionID, nil, devicesec.ErrStale)
		return
	}
	sess.sasConfirmed = true
	e.clearActivePollWithError(fatalWfError("canceled", text.KeySecurityPairingCanceled, ""))
	wf.activePoll = &activePoll{kind: pollJoinApproved, pairingID: pairingID, actionID: actionID}
	e.pollJoinState(pairingID, v1.PairingState_PAIRING_STATE_APPROVED)
}

type cancelJoinPairingTask struct{ actionID uint64 }

func (e *Engine) cancelJoinPairingAction(actionID uint64, pairingID string) {
	wf := e.ensureWF()
	if pairingID == "" && wf.activePoll != nil {
		pairingID = wf.activePoll.pairingID
	}
	if wf.activePoll != nil && (pairingID == "" || wf.activePoll.pairingID == pairingID) {
		e.clearActivePollWithError(fatalWfError("canceled", text.KeySecurityPairingCanceled, ""))
	}
	sess := wf.joinPairings[pairingID]
	delete(wf.joinPairings, pairingID)
	if sess == nil {
		e.emitActionResult(actionID, nil, nil)
		return
	}
	e.discardJoinSessionKeys(sess)
	e.fireDeleteStorage(bktCand, PairingCandidateStorageKey(sess.server, sess.pairingID))
	e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_CANCEL_PAIRING, sess.server, "", &v1.CancelPairingRequest{
		PairingId: sess.pairingID, CandidateToken: sess.candidateToken,
	}, &cancelJoinPairingTask{actionID: actionID})
}

func (t *cancelJoinPairingTask) step(e *Engine, _ *secEvent) error {
	e.emitActionResult(t.actionID, nil, nil)
	return nil
}

func (e *Engine) cleanupJoinerFailure(sess *joinerPairingSession) {
	wf := e.ensureWF()
	if sess == nil {
		return
	}
	delete(wf.joinPairings, sess.pairingID)
	e.discardJoinSessionKeys(sess)
	e.fireDeleteStorage(bktCand, PairingCandidateStorageKey(sess.server, sess.pairingID))
}

type pollJoinTask struct {
	taskBase
	pairingID, approver, selfID, intentRev string
	want                                   v1.PairingState
	sess                                   *joinerPairingSession
	st                                     v1.GetPairingResponse
	pin                                    devicesec.Pin
	payload                                v1.PairingPayload
	regBody                                []byte
	acctPub32                              [32]byte
	newRec                                 *PersistedRecord
	intent                                 *PersistedRegistrationIntent
	signingPhase                           bool
}

func (t *pollJoinTask) rawErrMask() uint32 { return 1<<0 | 1<<4 }

func (t *pollJoinTask) onError(e *Engine, err error) {
	if t.signingPhase {
		t.sess.registering = false
		e.resolveActivePoll(t.pairingID, nil, err)
		return
	}
	e.cleanupJoinerFailure(t.sess)
	e.resolveActivePoll(t.pairingID, nil, err)
}

func (e *Engine) pollJoinState(pairingID string, want v1.PairingState) {
	wf := e.ensureWF()
	sess, poll := wf.joinPairings[pairingID], wf.activePoll
	if sess == nil || poll == nil || poll.pairingID != pairingID {
		e.cancelSecurityPollTimer()
		return
	}
	e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_PAIRING, sess.server, "", &v1.GetPairingRequest{
		PairingId: sess.pairingID, CandidateToken: sess.candidateToken,
	}, &pollJoinTask{pairingID: pairingID, want: want, sess: sess})
}

func (t *pollJoinTask) handleRevealed(e *Engine, st *v1.GetPairingResponse) error {
	wf, sess := e.ensureWF(), t.sess
	if len(st.InitiatorPublic) != 32 || len(st.Trust) == 0 ||
		!sameBytes(devicesec.PairDomainCommit(st.InitiatorPublic, st.Trust), sess.commit) ||
		(len(sess.commitHint) == 32 && !sameBytes(sess.commit, sess.commitHint)) {
		return devicesec.ErrUntrusted
	}
	pin, approver, err := devicesec.ParsePairTrustDescriptor(st.Trust)
	if err != nil || !devicesec.IsDeviceID(approver) || (sess.existingRepair && wf.record != nil && pin != wf.record.Pin) {
		return devicesec.ErrUntrusted
	}
	var initX32 [32]byte
	copy(initX32[:], st.InitiatorPublic)
	t.st, t.pin, t.approver, t.stage = *st, pin, approver, 1
	e.waitAgree(keyEphemX, sess.ephemSlot, initX32, false, t, 0)
	return nil
}

func (t *pollJoinTask) handleApproved(e *Engine, st *v1.GetPairingResponse) error {
	wf, sess := e.ensureWF(), t.sess
	if st.Payload == nil {
		e.scheduleSecurityPollTimer()
		return nil
	}
	var hdr v1.Header
	if hdr.Unmarshal(st.Payload.Header) != nil || hdr.Kind != v1.PayloadKind_PAYLOAD_KIND_PAIRING || hdr.RequestId != sess.pairingID {
		return devicesec.ErrUntrusted
	}
	plain, err := openPairingEnvelopeAESGCM(sess.pairKey, st.Payload.Nonce, st.Payload.Header, st.Payload.Ciphertext)
	if err != nil {
		return devicesec.ErrUntrusted
	}
	if hdr.Compression == v1.Compression_COMPRESSION_DEFLATE {
		if plain, err = devicesec.InflateBounded(plain, maxStoreBlobBytes); err != nil {
			return err
		}
	} else if hdr.Compression != v1.Compression_COMPRESSION_NONE {
		return devicesec.ErrInvalid
	}
	var payload v1.PairingPayload
	if payload.Unmarshal(plain) != nil {
		return devicesec.ErrInvalid
	}
	if pSrv, err := CheckServerURL(payload.Server); err != nil || pSrv != sess.server || len(payload.AccountPublicKey) != 32 ||
		devicesec.ID(payload.AccountPublicKey) != sess.trust.Account {
		return devicesec.ErrUntrusted
	}
	rawStates, rawProofs := make([][]byte, 0, len(payload.SecurityStates)), make([][]byte, 0, len(payload.SecurityStates))
	for _, ss := range payload.SecurityStates {
		if ss == nil || len(ss.State) == 0 {
			return devicesec.ErrInvalid
		}
		rawStates, rawProofs = append(rawStates, ss.State), append(rawProofs, ss.Recovery)
	}
	states, _, _, err := parseAndVerifyChain(sess.trust, rawStates, rawProofs)
	if err != nil || len(states) == 0 {
		return devicesec.ErrUntrusted
	}
	head := states[len(states)-1]
	selfID := devicesec.ID(sess.edPublic)
	if selfMem, ok := head.Core.Member(selfID); !ok || selfMem.Kind != devicesec.Client || !sameBytes(selfMem.XPublic[:], sess.xPublic) {
		return devicesec.ErrUntrusted
	}
	if ap, err := VerifyPairApproval(payload.Approval, payload.ApprovalSignature); err != nil ||
		ap.AccountId != sess.trust.Account || ap.PairingId != sess.pairingID || ap.ApproverDeviceId != sess.approver ||
		head.Core.AuthorityID != ap.ApproverDeviceId || ap.CandidateKind != v1.DeviceKind_DEVICE_KIND_CLIENT ||
		!sameBytes(ap.CandidateDevicePublicKey, sess.edPublic) || ap.ReplacesDeviceId != "" {
		return devicesec.ErrUntrusted
	}
	if len(payload.Lease) > 0 {
		if lease, err := devicesec.ParseLease(payload.Lease); err != nil || lease.Verify(head) != nil {
			return devicesec.ErrUntrusted
		}
	}
	var floorMs int64
	var priorKeys []ContentKeyEntry
	if sess.existingRepair {
		oldRec := wf.record
		if oldRec == nil || oldRec.Pin != sess.trust || len(rawStates) < len(oldRec.States) {
			return devicesec.ErrStale
		}
		for i, prevRaw := range oldRec.States {
			if !sameBytes(rawStates[i], prevRaw) {
				return devicesec.ErrStale
			}
		}
		floorMs, priorKeys = oldRec.FloorMs, slices.Clone(oldRec.ContentKeys)
	}
	floorMs = max(floorMs, e.lastNowMs)
	var acctPub32 [32]byte
	copy(acctPub32[:], payload.AccountPublicKey)
	newRec := &PersistedRecord{
		Pin: sess.trust, MemberID: selfID, Member: true, Server: sess.server, AccountPublicKey: slices.Clone(acctPub32[:]),
		States: rawStates, RecoveryProofs: rawProofs, Lease: slices.Clone(payload.Lease), FloorMs: floorMs, ContentKeys: priorKeys,
	}
	regBody, err := (&v1.DeviceRegistration{
		AccountPublicKey: slices.Clone(payload.AccountPublicKey), DevicePublicKey: slices.Clone(sess.edPublic),
		Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, CreatedMs: max(1, e.lastNowMs),
	}).Marshal()
	if err != nil {
		return err
	}
	signKind, signSlot := keyCandEd, sess.edSlot
	if sess.reusesDeviceEd {
		signKind, signSlot = keyDevEd, ""
	}
	t.payload, t.regBody, t.selfID, t.acctPub32, t.newRec = payload, regBody, selfID, acctPub32, newRec
	sess.registering, t.signingPhase, t.stage = true, true, 2
	e.waitSign(signKind, signSlot, append(append([]byte{}, registerContext...), regBody...), t, 0)
	return nil
}

func (t *pollJoinTask) step(e *Engine, ev *secEvent) error {
	wf, sess := e.ensureWF(), t.sess
	switch t.stage {
	case 0: // get
		if wf.activePoll == nil || wf.activePoll.pairingID != t.pairingID {
			return nil
		}
		if ev.rpcErr != nil {
			if ev.rpcErr.code == connectCodeNotFound || ev.rpcErr.code == connectCodePermissionDenied {
				return fatalWfError("canceled", text.KeySecurityPairingExpiredOrWasCanceled, "")
			}
			e.scheduleSecurityPollTimer()
			return nil
		}
		var st v1.GetPairingResponse
		if st.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		if st.State == v1.PairingState_PAIRING_STATE_CANCELED {
			return fatalWfError("canceled", text.KeySecurityPairingExpiredOrWasCanceled, "")
		}
		if st.State < t.want {
			e.scheduleSecurityPollTimer()
			return nil
		}
		if t.want == v1.PairingState_PAIRING_STATE_REVEALED {
			return t.handleRevealed(e, &st)
		}
		return t.handleApproved(e, &st)
	case 1: // reveal agree
		transcript := devicesec.PairDomainTranscript(sess.pairingID, t.st.InitiatorPublic, sess.ephemPub, sess.edPublic, sess.xPublic, int32(v1.DeviceKind_DEVICE_KIND_CLIENT), t.st.Trust)
		ps, err := devicesec.DerivePairSecrets(ev.bytes, transcript)
		if err != nil {
			return err
		}
		if sess.ephemSlot != "" {
			e.reqKeyDiscard(keyEphemX, sess.ephemSlot)
			sess.ephemSlot = ""
		}
		sess.trust, sess.approver, sess.pairKey, sess.sasEmoji = t.pin, t.approver, slices.Clone(ps.Key), ps.Emoji()
		e.resolveActivePoll(t.pairingID, &corev1.SecurityWorkflowResultEffect{Pairing: joinerSessionToProto(sess)}, nil)
	case 2: // sign reg
		regReqBytes, err := (&v1.RegisterDeviceRequest{
			Registration: t.regBody, Signature: ev.bytes, Approval: slices.Clone(t.payload.Approval),
			ApprovalSignature: slices.Clone(t.payload.ApprovalSignature), ProtocolVersion: protocolVersion,
		}).Marshal()
		if err != nil {
			return err
		}
		intent := &PersistedRegistrationIntent{
			Server: sess.server, DeviceID: t.selfID, RegisterRequest: regReqBytes, Replace: sess.existingRepair,
			AccountPublicKey: slices.Clone(t.acctPub32[:]), PairingID: sess.pairingID, CandidateToken: sess.candidateToken,
			EdSlot: sess.edSlot, XSlot: sess.xSlot, ReusesDeviceEd: sess.reusesDeviceEd, Record: t.newRec,
		}
		intentBytes, err := intent.Encode()
		if err != nil {
			return err
		}
		intentRev := e.nextWorkflowToken()
		t.intent, t.intentRev, t.stage = intent, intentRev, 3
		e.writeStorageCAS(bktIntent, "intent", "", intentRev, true, intentBytes, t)
	case 3: // write intent
		wf.regIntent, wf.regIntentRev, e.securityLocked, t.stage = t.intent, t.intentRev, true, 4
		e.startResumeRegistration(0, parentTarget(t, 0))
	case 4: // resume reg
		sess.registering = false
		e.resolveActivePoll(t.pairingID, ev.wfRes, ev.err)
	}
	return nil
}

type resumeRegistrationTask struct {
	taskBase
	intent *PersistedRegistrationIntent
	regErr *rpcError
	prof   *PersistedPublicProfile
	rev    string
}

func (t *resumeRegistrationTask) rawErrMask() uint32 { return 1<<0 | 1<<1 }

func (e *Engine) startResumeRegistration(fgGen uint64, target taskTarget) {
	if fgGen != 0 {
		if err := e.checkFG(fgGen); err != nil {
			e.failTarget(target, err)
			return
		}
	}
	intent := e.ensureWF().regIntent
	if intent == nil || intent.Record == nil {
		e.okTarget(target, nil)
		return
	}
	e.waitRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_REGISTER_DEVICE, intent.Server, "", intent.RegisterRequest, &resumeRegistrationTask{
		taskBase: taskBase{fgGen: fgGen, target: target}, intent: intent,
	}, 0)
}

func (t *resumeRegistrationTask) commitIntent(e *Engine) error {
	wf, intent := e.ensureWF(), t.intent
	if t.fgGen != 0 {
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
	}
	if intent.PairingID != "" && intent.CandidateToken != "" {
		e.fireProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_PAIRING, intent.Server, "", &v1.GetPairingRequest{
			PairingId: intent.PairingID, CandidateToken: intent.CandidateToken, Received: true,
		})
	}
	var acctPub32 [32]byte
	copy(acctPub32[:], intent.AccountPublicKey)
	prof := &PersistedPublicProfile{Server: intent.Server, Pin: intent.Record.Pin, AccountPublicKey: acctPub32}
	profBytes, err := prof.Encode()
	if err != nil {
		return err
	}
	recBytes, err := intent.Record.Encode()
	if err != nil {
		return err
	}
	rev, expectedRecRev, clearPairKey := e.nextWorkflowToken(), "", ""
	if intent.Replace {
		expectedRecRev = wf.recordRev
	}
	if intent.PairingID != "" {
		clearPairKey = PairingCandidateStorageKey(intent.Server, intent.PairingID)
	}
	batch := buildAccountCommitWrites(intent.Server, intent.Record.Pin, profBytes, recBytes, !intent.Replace || wf.recordRev == "", expectedRecRev, rev, "", false, wf.regIntentRev, clearPairKey)
	var discard []string
	var promoteEd string
	if !intent.ReusesDeviceEd && intent.EdSlot != "" {
		discard, promoteEd = append(discard, intent.EdSlot), intent.EdSlot
	}
	if intent.XSlot != "" {
		discard = append(discard, intent.XSlot)
	}
	t.prof, t.rev, t.stage = prof, rev, 2
	e.batchCommitStorage(promoteEd, intent.XSlot, discard, true, batch, t)
	return nil
}

func (t *resumeRegistrationTask) step(e *Engine, ev *secEvent) error {
	wf, intent := e.ensureWF(), t.intent
	switch t.stage {
	case 0: // rpc
		if ev.rpcErr == nil {
			var resp v1.RegisterDeviceResponse
			if resp.Unmarshal(ev.bytes) != nil || resp.DeviceId != intent.DeviceID {
				return devicesec.ErrUntrusted
			}
			return t.commitIntent(e)
		}
		if ev.rpcErr.code == connectCodeAlreadyExists || ev.rpcErr.code == connectCodePermissionDenied {
			kind, slot := keyCandEd, intent.EdSlot
			if intent.ReusesDeviceEd {
				kind, slot = keyDevEd, ""
			}
			t.regErr, t.stage = ev.rpcErr, 1
			e.forceLoginToken(intent.Server, kind, slot, intent.DeviceID, false, 0, false, parentTarget(t, 0))
			return nil
		}
		return ev.rpcErr
	case 1: // fallback login
		if ev.err == nil {
			return t.commitIntent(e)
		}
		return t.regErr
	case 2: // commit
		rec := intent.Record.Clone()
		if intent.PairingID != "" {
			delete(wf.joinPairings, intent.PairingID)
		}
		var fallbackEd, fallbackX []byte
		if len(rec.States) > 0 {
			if hd, err := devicesec.ParseState(rec.States[len(rec.States)-1]); err == nil {
				if m, ok := hd.Core.Member(rec.MemberID); ok {
					fallbackEd, fallbackX = m.EdPublic[:], m.XPublic[:]
				}
			}
		}
		e.installCommittedAccount(intent.Server, t.rev, rec, t.prof, ev.storage, fallbackEd, fallbackX)
		t.stage = 3
		e.applyPersistedRecordToEngine(rec, parentTarget(t, 0))
	case 3: // apply
		rec := wf.record
		if rec != nil && len(rec.States) > 0 {
			if head, pErr := devicesec.ParseState(rec.States[len(rec.States)-1]); pErr == nil {
				e.fireNotifyRecord(rec, head, t.rev)
			}
		}
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{
			AccountAdopted: &corev1.SecurityAccountAdopted{
				Server: intent.Server, DeviceId: rec.MemberID, AccountId: rec.Pin.Account, NeedsRecoveryRotation: rec.NeedsRecoveryRotation,
			},
		})
	}
	return nil
}
