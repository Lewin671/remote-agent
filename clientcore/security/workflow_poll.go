package security

import (
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

// StartInitiatorJoinedPoll begins core-managed polling for a joiner to enter pairingID.
func (e *Engine) StartInitiatorJoinedPoll(nowMs int64, pairingID string) []*corev1.Effect {
	e.touchNow(nowMs)
	wf := e.ensureWF()
	if pairingID == "" || wf.initPairings[pairingID] == nil {
		return nil
	}
	wf.activePoll = &activePoll{kind: PollInitiatorJoined, pairingID: pairingID}
	if wf.foreground {
		e.scheduleSecurityPollTimerDelay(pairInitJoinedPollIntervalMs)
	}
	return e.DrainEffects()
}

// StartInitiatorDeliveryPoll begins core-managed polling to finish pairing delivery until complete.
func (e *Engine) StartInitiatorDeliveryPoll(nowMs int64, pairingID string) []*corev1.Effect {
	e.touchNow(nowMs)
	e.startInitiatorDeliveryPoll(pairingID)
	return e.DrainEffects()
}

func (e *Engine) startInitiatorDeliveryPoll(pairingID string) {
	wf := e.ensureWF()
	if wf.pendingOp == nil && (pairingID == "" || wf.initPairings[pairingID] == nil) {
		return
	}
	if pairingID == "" && wf.pendingOp != nil && wf.pendingOp.Pair != nil {
		pairingID = wf.pendingOp.Pair.PairingID
	}
	wf.activePoll = &activePoll{kind: PollInitiatorDelivery, pairingID: pairingID}
	if wf.foreground {
		e.scheduleSecurityPollTimerDelay(pairInitDeliveryPollIntervalMs)
	}
}

// StartRecoveryWindowPoll begins adaptive polling for an active emergency recovery wait window.
func (e *Engine) StartRecoveryWindowPoll(nowMs int64, readyMs int64) []*corev1.Effect {
	e.touchNow(nowMs)
	wf := e.ensureWF()
	if wf.pendingOp == nil || wf.pendingOp.Kind != PendingOpKindRecovery || wf.pendingOp.Recovery == nil || wf.pendingOp.Recovery.RecoveryID == "" {
		return nil
	}
	if readyMs == 0 {
		readyMs = wf.pendingOp.Recovery.ReadyMs
	}
	wf.activePoll = &activePoll{kind: PollRecoveryWindow, readyMs: readyMs}
	if wf.foreground {
		e.scheduleSecurityPollTimerDelay(recoveryPollDelayMs(nowMs, readyMs))
	}
	return e.DrainEffects()
}

// StopManagedPoll cancels the active security poll if it matches kind (or any managed poll when kind == PollNone).
func (e *Engine) StopManagedPoll(kind ActivePollKind) []*corev1.Effect {
	wf := e.ensureWF()
	if wf.activePoll == nil {
		return nil
	}
	if kind != PollNone && wf.activePoll.kind != kind {
		return nil
	}
	wf.activePoll = nil
	e.cancelSecurityPollTimer()
	return e.DrainEffects()
}

func recoveryPollDelayMs(nowMs, readyMs int64) int64 {
	if readyMs > 0 && nowMs > 0 {
		remaining := readyMs - nowMs
		if remaining <= recoveryPollFastThresholdMs {
			return recoveryPollFastIntervalMs
		}
	}
	return recoveryPollSlowIntervalMs
}

func (e *Engine) runActivePollTick() {
	wf := e.ensureWF()
	p := wf.activePoll
	if p == nil || !wf.foreground || p.inFlight {
		return
	}
	switch p.kind {
	case PollJoinRevealed:
		e.pollJoinState(p.pairingID, v1.PairingState_PAIRING_STATE_REVEALED)
	case PollJoinApproved:
		e.pollJoinState(p.pairingID, v1.PairingState_PAIRING_STATE_APPROVED)
	case PollInitiatorJoined:
		sess := wf.initPairings[p.pairingID]
		if sess == nil {
			wf.activePoll = nil
			e.cancelSecurityPollTimer()
			return
		}
		if sess.expiresMs > 0 && e.lastNowMs > 0 && e.lastNowMs >= sess.expiresMs {
			e.discardInitSlot(sess)
			delete(wf.initPairings, p.pairingID)
			wf.activePoll = nil
			e.cancelSecurityPollTimer()
			const codeCanceled = "canceled"
			e.notifyPollUpdate(PollInitiatorJoined, p.pairingID, nil, nil, false, false, fatalWfError(codeCanceled, WorkflowReasonPairingExpiredOrCanceled, ""))
			return
		}
		p.inFlight = true
		e.startPollPairingInitiator(wf.fgGen, p.pairingID, parentTarget(&managedPollTask{kind: PollInitiatorJoined, pairingID: p.pairingID}, 0))
	case PollInitiatorJoinerMeta:
		sess := wf.initPairings[p.pairingID]
		if sess == nil || sess.joinerMetaReceived || sess.approved {
			wf.activePoll = nil
			e.cancelSecurityPollTimer()
			e.notifyPollUpdate(PollInitiatorJoinerMeta, p.pairingID, e.initiatorSessionToProto(sess), nil, false, true, nil)
			return
		}
		if p.metaPollCount >= pairInitMetaMaxPolls {
			wf.activePoll = nil
			e.cancelSecurityPollTimer()
			e.notifyPollUpdate(PollInitiatorJoinerMeta, p.pairingID, e.initiatorSessionToProto(sess), nil, false, true, nil)
			return
		}
		p.metaPollCount++
		p.inFlight = true
		e.startPollPairingInitiator(wf.fgGen, p.pairingID, parentTarget(&managedPollTask{kind: PollInitiatorJoinerMeta, pairingID: p.pairingID}, 0))
	case PollInitiatorDelivery:
		if wf.pendingOp == nil || len(wf.pendingOp.Delivery) == 0 {
			wf.activePoll = nil
			e.cancelSecurityPollTimer()
			e.notifyPollUpdate(PollInitiatorDelivery, p.pairingID, nil, nil, true, false, nil)
			return
		}
		p.inFlight = true
		e.stepTask(&finishPairingDeliveryTask{pairingID: p.pairingID}, &secEvent{
			kind:   evStart,
			fgGen:  wf.fgGen,
			target: parentTarget(&managedPollTask{kind: PollInitiatorDelivery, pairingID: p.pairingID}, 0),
		})
	case PollRecoveryWindow:
		if wf.pendingOp == nil || wf.pendingOp.Kind != PendingOpKindRecovery || wf.pendingOp.Recovery == nil || wf.pendingOp.Recovery.RecoveryID == "" {
			wf.activePoll = nil
			e.cancelSecurityPollTimer()
			return
		}
		p.inFlight = true
		e.stepTask(&resumeRecoveryTask{}, &secEvent{
			kind:   evStart,
			fgGen:  wf.fgGen,
			target: parentTarget(&managedPollTask{kind: PollRecoveryWindow}, 0),
		})
	}
}

type managedPollTask struct {
	kind      ActivePollKind
	pairingID string
}

func (t *managedPollTask) step(e *Engine, ev *secEvent) error {
	wf := e.ensureWF()
	p := wf.activePoll
	if p == nil || p.kind != t.kind || p.pairingID != t.pairingID {
		return nil
	}
	p.inFlight = false

	switch t.kind {
	case PollInitiatorJoined:
		if ev.err != nil {
			meta, code, retryable, _, _ := classifyWorkflowError(ev.err, WorkflowMeta{})
			if !retryable || code == corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CANCELED || code == corev1.SecurityErrorCode_SECURITY_ERROR_CODE_NOT_FOUND {
				wf.activePoll = nil
				e.cancelSecurityPollTimer()
				e.notifyPollUpdate(t.kind, t.pairingID, nil, nil, false, false, ev.err)
				_ = meta
				return nil
			}
			// Transient error: surface error and keep polling while foreground.
			e.notifyPollUpdate(t.kind, t.pairingID, nil, nil, false, false, ev.err)
			if wf.foreground {
				e.scheduleSecurityPollTimerDelay(pairInitJoinedPollIntervalMs)
			}
			return nil
		}
		var info *corev1.SecurityPairingInfo
		if ev.wfRes != nil {
			info = ev.wfRes.Pairing
		}
		if info != nil && info.Joined && len(info.SasEmoji) > 0 {
			if !info.JoinerMetaReceived {
				p.kind = PollInitiatorJoinerMeta
				p.metaPollCount = 0
				e.notifyPollUpdate(PollInitiatorJoined, t.pairingID, info, nil, false, false, nil)
				if wf.foreground {
					e.scheduleSecurityPollTimerDelay(pairInitMetaPollIntervalMs)
				}
			} else {
				wf.activePoll = nil
				e.cancelSecurityPollTimer()
				e.notifyPollUpdate(PollInitiatorJoined, t.pairingID, info, nil, false, true, nil)
			}
			return nil
		}
		if wf.foreground {
			e.scheduleSecurityPollTimerDelay(pairInitJoinedPollIntervalMs)
		}

	case PollInitiatorJoinerMeta:
		if ev.err != nil {
			// Metadata polling is best-effort: stop quietly on error and keep SAS approvable.
			wf.activePoll = nil
			e.cancelSecurityPollTimer()
			e.notifyPollUpdate(t.kind, t.pairingID, e.initiatorSessionToProto(wf.initPairings[t.pairingID]), nil, false, true, nil)
			return nil
		}
		var info *corev1.SecurityPairingInfo
		if ev.wfRes != nil {
			info = ev.wfRes.Pairing
		}
		if info != nil && info.JoinerMetaReceived {
			wf.activePoll = nil
			e.cancelSecurityPollTimer()
			e.notifyPollUpdate(t.kind, t.pairingID, info, nil, false, true, nil)
			return nil
		}
		if p.metaPollCount >= pairInitMetaMaxPolls {
			wf.activePoll = nil
			e.cancelSecurityPollTimer()
			e.notifyPollUpdate(t.kind, t.pairingID, info, nil, false, true, nil)
			return nil
		}
		if wf.foreground {
			e.scheduleSecurityPollTimerDelay(pairInitMetaPollIntervalMs)
		}

	case PollInitiatorDelivery:
		if ev.err != nil {
			_, code, retryable, _, _ := classifyWorkflowError(ev.err, WorkflowMeta{})
			fatal := isFatalDeliveryPollError(code, retryable)
			if fatal {
				wf.activePoll = nil
				e.cancelSecurityPollTimer()
			}
			e.notifyPollUpdate(t.kind, t.pairingID, nil, nil, false, false, ev.err)
			if !fatal && wf.foreground {
				e.scheduleSecurityPollTimerDelay(pairInitDeliveryPollIntervalMs)
			}
			return nil
		}
		var info *corev1.SecurityPairingInfo
		if ev.wfRes != nil {
			info = ev.wfRes.Pairing
		}
		delivered := (info != nil && info.Delivered) || wf.pendingOp == nil || len(wf.pendingOp.Delivery) == 0
		if delivered {
			wf.activePoll = nil
			e.cancelSecurityPollTimer()
			e.emitStatusChanged()
			if wf.pollObserver != nil {
				wf.pollObserver.OnSecurityDevicesMayHaveChanged()
			}
			e.notifyPollUpdate(t.kind, t.pairingID, info, nil, true, false, nil)
			return nil
		}
		if wf.foreground {
			e.scheduleSecurityPollTimerDelay(pairInitDeliveryPollIntervalMs)
		}

	case PollRecoveryWindow:
		if ev.err != nil {
			// Keep polling on transient error while foreground.
			e.notifyPollUpdate(t.kind, "", nil, nil, false, false, ev.err)
			if wf.foreground {
				e.scheduleSecurityPollTimerDelay(recoveryPollDelayMs(e.lastNowMs, p.readyMs))
			}
			return nil
		}
		var rec *corev1.SecurityRecoveryInfo
		if ev.wfRes != nil {
			rec = ev.wfRes.Recovery
		}
		if rec != nil {
			if rec.ReadyMs > 0 {
				p.readyMs = rec.ReadyMs
			}
			if rec.Phase == v1.GetRecoveryContextResponse_PHASE_READY ||
				rec.Phase == v1.GetRecoveryContextResponse_PHASE_PUBLISHED ||
				rec.Phase == v1.GetRecoveryContextResponse_PHASE_REJECTED {
				wf.activePoll = nil
				e.cancelSecurityPollTimer()
				e.notifyPollUpdate(t.kind, "", nil, rec, false, false, nil)
				return nil
			}
			e.notifyPollUpdate(t.kind, "", nil, rec, false, false, nil)
		}
		if wf.foreground {
			e.scheduleSecurityPollTimerDelay(recoveryPollDelayMs(e.lastNowMs, p.readyMs))
		}
	}
	return nil
}

func isFatalDeliveryPollError(code corev1.SecurityErrorCode, retryable bool) bool {
	switch code {
	case corev1.SecurityErrorCode_SECURITY_ERROR_CODE_INVALID,
		corev1.SecurityErrorCode_SECURITY_ERROR_CODE_UNTRUSTED,
		corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CONFLICT,
		corev1.SecurityErrorCode_SECURITY_ERROR_CODE_STORAGE,
		corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CAPACITY,
		corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CANCELED,
		corev1.SecurityErrorCode_SECURITY_ERROR_CODE_NOT_FOUND,
		corev1.SecurityErrorCode_SECURITY_ERROR_CODE_PERMISSION_DENIED,
		corev1.SecurityErrorCode_SECURITY_ERROR_CODE_FAILED_PRECONDITION:
		return true
	default:
		return !retryable && code != corev1.SecurityErrorCode_SECURITY_ERROR_CODE_FOREGROUND &&
			code != corev1.SecurityErrorCode_SECURITY_ERROR_CODE_UNAVAILABLE
	}
}

func (e *Engine) notifyPollUpdate(
	kind ActivePollKind,
	pairingID string,
	pairing *corev1.SecurityPairingInfo,
	recovery *corev1.SecurityRecoveryInfo,
	delivered bool,
	metaDone bool,
	err error,
) {
	obs := e.ensureWF().pollObserver
	if obs == nil {
		return
	}
	u := PollUpdate{
		Kind:      kind,
		PairingID: pairingID,
		Pairing:   pairing,
		Recovery:  recovery,
		Delivered: delivered,
		MetaDone:  metaDone,
		Err:       err,
	}
	if err != nil {
		u.Meta, u.ErrCode, u.Retryable, u.Uncertain, _ = classifyWorkflowError(err, WorkflowMeta{})
	}
	obs.OnSecurityPollUpdate(u)
}

// EffectiveServer returns the server URL associated with the current security record or workflow.
func (e *Engine) EffectiveServer() string {
	if e == nil {
		return ""
	}
	return e.effectiveServer()
}

// CachedAuthToken returns the current cached bearer token (if any) for push unregistration on exit.
func (e *Engine) CachedAuthToken() string {
	if e == nil || e.wf == nil {
		return ""
	}
	return e.wf.cachedToken
}

// IsForeground reports whether the security engine considers the app in the foreground.
func (e *Engine) IsForeground() bool {
	if e == nil || e.wf == nil {
		return true
	}
	return e.wf.foreground
}
