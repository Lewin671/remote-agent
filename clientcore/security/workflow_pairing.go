package security

import (
	"slices"
	"strings"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
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
	e.clearActivePollWithError(fatalWfError("canceled", WorkflowReasonPairingCanceled, ""))
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

func shortDeviceSuffix(id string) string {
	r := []rune(id)
	if len(r) <= 4 {
		return id
	}
	return "…" + string(r[len(r)-4:])
}

func clampRunes(s string, max int) string {
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}

func isGenericModelName(model string) bool {
	switch model {
	case "", "iphone", "ipad", "ipod", "ios", "android", "phone", "tablet", "mobile", "unknown":
		return true
	default:
		return false
	}
}

// deviceModelFingerprint normalizes a device's platform and model from DeviceMeta
// so that re-paired instances of the same physical device or browser match cleanly.
// specificModel is true only when platform is android/ios and an explicit hardware
// model appears after the "·" separator (excluding generic device-family fallbacks).
func deviceModelFingerprint(meta *v1.DeviceMeta) (platform, model string, specificModel bool) {
	if meta == nil {
		return "", "", false
	}
	platform = strings.ToLower(strings.TrimSpace(meta.Platform))
	desc := strings.TrimSpace(meta.Description)
	if platform == "" || desc == "" {
		return "", "", false
	}
	switch platform {
	case "android", "ios":
		// Format: "Android 15 · Pixel 3" or "iOS 18.4 · iPhone16,1".
		// Strip the OS version prefix so OS updates still match the same model.
		if idx := strings.Index(desc, "·"); idx >= 0 {
			after := strings.ToLower(strings.TrimSpace(desc[idx+len("·"):]))
			if after != "" {
				return platform, after, !isGenericModelName(after)
			}
		}
		return platform, strings.ToLower(desc), false
	default:
		// Web ("Chrome on macOS") and other coarse descriptors match candidates
		// for manual review, but never qualify for automatic default selection.
		return platform, strings.ToLower(desc), false
	}
}

func shouldDefaultSelectReplacement(specificModel bool, lastSeenMs, nowMs int64) bool {
	if !specificModel || nowMs <= 0 {
		return false
	}
	if lastSeenMs > 0 && nowMs-lastSeenMs < dayMs {
		return false
	}
	return true
}

func (e *Engine) matchReplacementCandidates(sess *initiatorPairingSession) []*corev1.SecurityPairingReplacementCandidate {
	if sess == nil || sess.candidateKind != v1.DeviceKind_DEVICE_KIND_CLIENT || sess.replacesDeviceID != "" || sess.joinerMeta == nil || e.head.Core.Epoch == 0 {
		return nil
	}
	joinPlat, joinModel, joinSpecific := deviceModelFingerprint(sess.joinerMeta)
	if joinPlat == "" || joinModel == "" {
		return nil
	}
	nowMs := e.lastNowMs
	if eff := int64(e.effectiveNow(e.lastNowMs)); eff > nowMs {
		nowMs = eff
	}
	wf := e.ensureWF()
	var out []*corev1.SecurityPairingReplacementCandidate
	for _, d := range wf.devicesSnap {
		if d.ID == "" || d.ID == e.selfDeviceID || d.ID == e.head.Core.AuthorityID || d.ID == sess.candidateID {
			continue
		}
		if d.Kind != v1.DeviceKind_DEVICE_KIND_CLIENT {
			continue
		}
		if _, live := e.head.Core.Member(d.ID); !live {
			continue
		}
		dp, dm, devSpecific := deviceModelFingerprint(d.Meta)
		if dp != joinPlat || dm != joinModel {
			continue
		}
		desc := ""
		displayName := ""
		if d.Meta != nil {
			desc = clampRunes(strings.TrimSpace(d.Meta.Description), 120)
			displayName = clampRunes(strings.TrimSpace(d.Meta.Name), 120)
		}
		if displayName == "" {
			displayName = desc
		}
		if displayName == "" {
			displayName = shortDeviceSuffix(d.ID)
		}
		out = append(out, &corev1.SecurityPairingReplacementCandidate{
			DeviceId:        d.ID,
			DisplayName:     displayName,
			Description:     desc,
			ShortId:         shortDeviceSuffix(d.ID),
			LastSeenMs:      d.LastSeenMs,
			DefaultSelected: shouldDefaultSelectReplacement(joinSpecific && devSpecific, d.LastSeenMs, nowMs),
		})
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].DeviceId < out[j-1].DeviceId; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (e *Engine) initiatorSessionToProto(s *initiatorPairingSession) *corev1.SecurityPairingInfo {
	if s == nil {
		return nil
	}
	info := &corev1.SecurityPairingInfo{
		PairingId:             s.pairingID,
		Code:                  s.code,
		Url:                   s.url,
		ExpiresMs:             s.expiresMs,
		SasEmoji:              slices.Clone(s.sasEmoji),
		JoinerKind:            s.candidateKind,
		Joined:                s.joined,
		Approved:              s.approved,
		Delivered:             s.delivered,
		JoinerMetaReceived:    s.joinerMetaReceived,
		ReplacementCandidates: s.replacementCandidates,
	}
	if s.joinerMeta != nil {
		info.JoinerDescription = clampRunes(strings.TrimSpace(s.joinerMeta.Description), 120)
	}
	return info
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

const (
	startPairSync uint8 = iota
	startPairListMachines
	startPairEntropy
	startPairKeyGen
	startPairCreateRPC
)

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
	t.stage = startPairEntropy
	e.waitEntropy(64, t, 0)
}

func (t *startPairingTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case startPairSync:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if ev.kind == evStart {
			if wf.pendingOp != nil {
				return newWfError("pending", WorkflowReasonFinishCurrentOpFirst)
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
			return newWfError("invalid", WorkflowReasonAccountPubKeyMissing)
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
			t.stage = startPairListMachines
			e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_MACHINES, server, t.fgGen, &v1.ListMachinesRequest{}, parentTarget(t, 0))
			return nil
		}
		t.beginCreate(e)
	case startPairListMachines:
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
			return newWfError("not_found", WorkflowReasonComputerArchiveDeleted)
		}
		t.beginCreate(e)
	case startPairEntropy:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		t.ephemSlot, t.stage = "pair-init-"+e.nextWorkflowToken(), startPairKeyGen
		e.waitKeyGen(keyEphemX, t.ephemSlot, false, t, 0)
	case startPairKeyGen:
		t.hasEphem = true
		if len(ev.bytes) != 32 {
			return devicesec.ErrInvalid
		}
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		commit := devicesec.PairDomainCommit(ev.bytes, t.trust)
		t.ephemPub, t.commit, t.stage = ev.bytes, commit, startPairCreateRPC
		e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_CREATE_PAIRING, t.server, t.fgGen, &v1.CreatePairingRequest{Commit: commit}, parentTarget(t, 0))
	case startPairCreateRPC:
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
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{Pairing: e.initiatorSessionToProto(sess)})
	}
	return nil
}

const (
	pollInitGet uint8 = iota
	pollInitAgree
	pollInitReveal
	pollInitRemoveOld
)

type pollPairingInitiatorTask struct {
	taskBase
	pairingID, candID string
	sess              *initiatorPairingSession
	st                v1.GetPairingResponse
	ps                *devicesec.PairSecrets
}

func (e *Engine) startPollPairingInitiator(fgGen uint64, pairingID string, target taskTarget) {
	e.stepTask(&pollPairingInitiatorTask{pairingID: pairingID}, &secEvent{kind: evStart, fgGen: fgGen, target: target})
}

func (t *pollPairingInitiatorTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil && t.stage != pollInitGet && t.stage != pollInitRemoveOld {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case pollInitGet:
		if ev.kind == evStart {
			if err := e.checkFG(t.fgGen); err != nil {
				return err
			}
			sess := wf.initPairings[t.pairingID]
			if sess == nil {
				return newWfError("not_found", WorkflowReasonPairingExpiredOrCanceled)
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
			if ev.rpcErr.isPairingGone() {
				e.discardInitSlot(sess)
				delete(wf.initPairings, t.pairingID)
				return fatalWfError("canceled", WorkflowReasonPairingExpiredOrCanceled, "")
			}
			return ev.err
		} else if ev.err != nil {
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
			return fatalWfError("canceled", WorkflowReasonPairingExpiredOrCanceled, "")
		case v1.PairingState_PAIRING_STATE_DELIVERED:
			e.discardInitSlot(sess)
			sess.delivered, sess.approved, sess.joined = true, true, true
			ids := e.pendingRemoveDeviceIDs()
			if len(ids) == 0 && len(sess.removeDeviceIDs) > 0 {
				ids = sess.removeDeviceIDs
			}
			sess.removeDeviceIDs = nil
			if len(ids) > 0 {
				delete(wf.initPairings, t.pairingID)
				t.stage = pollInitRemoveOld
				e.startRemoveDeviceList(t.fgGen, ids, parentTarget(t, 0))
				return nil
			}
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
			t.st, t.candID, t.stage = st, candID, pollInitAgree
			e.waitAgree(keyEphemX, sess.ephemSlot, peerX32, false, t, 0)
			return nil
		case v1.PairingState_PAIRING_STATE_REVEALED:
			if !sess.joinerMetaReceived && st.JoinerMeta != nil && len(sess.pairKey) == 32 {
				if dm := openJoinerMetaEnvelope(sess.pairKey, sess.pairingID, st.JoinerMeta); dm != nil {
					sess.joinerMeta = dm
					sess.joinerMetaReceived = true
					sess.replacementCandidates = e.matchReplacementCandidates(sess)
				}
			}
		case v1.PairingState_PAIRING_STATE_APPROVED:
			sess.approved, sess.joined = true, true
		}
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{Pairing: e.initiatorSessionToProto(sess)})
	case pollInitAgree:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		sess := t.sess
		transcript := devicesec.PairDomainTranscript(sess.pairingID, sess.ephemPub, t.st.JoinerPublic, t.st.DevicePublicKey, t.st.DeviceHpkePublicKey, int32(t.st.JoinerKind), sess.trust)
		ps, err := devicesec.DerivePairSecrets(ev.bytes, transcript)
		if err != nil {
			return err
		}
		t.ps, t.stage = ps, pollInitReveal
		e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_REVEAL_PAIRING, sess.server, t.fgGen, &v1.RevealPairingRequest{
			PairingId: sess.pairingID, InitiatorPublic: slices.Clone(sess.ephemPub), Trust: slices.Clone(sess.trust),
		}, parentTarget(t, 0))
	case pollInitReveal:
		sess := t.sess
		e.discardInitSlot(sess)
		sess.joined = true
		sess.candidateID, sess.candidateEd, sess.candidateX = t.candID, slices.Clone(t.st.DevicePublicKey), slices.Clone(t.st.DeviceHpkePublicKey)
		sess.candidateKind, sess.pairKey, sess.sasEmoji = t.st.JoinerKind, slices.Clone(t.ps.Key), t.ps.Emoji()
		if t.st.JoinerKind != v1.DeviceKind_DEVICE_KIND_CLIENT {
			sess.joinerMetaReceived = true
		}
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{Pairing: e.initiatorSessionToProto(sess)})
	case pollInitRemoveOld:
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{Pairing: e.initiatorSessionToProto(t.sess)})
	}
	return nil
}

const joinerMetaHeaderSeq uint64 = 1

func sealJoinerMetaEnvelope(pairKey, nonce []byte, pairingID string, meta *v1.DeviceMeta) *v1.Envelope {
	if len(pairKey) != 32 || len(nonce) != 12 || pairingID == "" || meta == nil {
		return nil
	}
	plain, err := meta.Marshal()
	if err != nil || len(plain) == 0 {
		return nil
	}
	hdrBytes, err := (&v1.Header{
		Version:     1,
		Kind:        v1.PayloadKind_PAYLOAD_KIND_PAIRING,
		RequestId:   pairingID,
		Seq:         joinerMetaHeaderSeq,
		Compression: v1.Compression_COMPRESSION_NONE,
	}).Marshal()
	if err != nil {
		return nil
	}
	ct, err := sealPairingEnvelopeAESGCM(pairKey, nonce, hdrBytes, plain)
	if err != nil {
		return nil
	}
	return &v1.Envelope{Header: hdrBytes, Nonce: slices.Clone(nonce), Ciphertext: ct}
}

func openJoinerMetaEnvelope(pairKey []byte, pairingID string, env *v1.Envelope) *v1.DeviceMeta {
	if len(pairKey) != 32 || pairingID == "" || env == nil {
		return nil
	}
	var hdr v1.Header
	if hdr.Unmarshal(env.Header) != nil || hdr.Kind != v1.PayloadKind_PAYLOAD_KIND_PAIRING || hdr.RequestId != pairingID || hdr.Seq != joinerMetaHeaderSeq || hdr.Compression != v1.Compression_COMPRESSION_NONE {
		return nil
	}
	plain, err := openPairingEnvelopeAESGCM(pairKey, env.Nonce, env.Header, env.Ciphertext)
	if err != nil {
		return nil
	}
	var dm v1.DeviceMeta
	if dm.Unmarshal(plain) != nil {
		return nil
	}
	return &dm
}

const (
	remListRun uint8 = iota
	remListPersist
)

type removeDeviceListTask struct {
	stage  uint8
	fgGen  uint64
	ids    []string
	curID  string
	failed []string
	target taskTarget
}

func (e *Engine) startRemoveDeviceList(fgGen uint64, ids []string, target taskTarget) {
	e.ensureWF().removingOldDevices = true
	t := &removeDeviceListTask{stage: remListRun, fgGen: fgGen, ids: slices.Clone(ids), target: target}
	t.next(e)
}

func (t *removeDeviceListTask) next(e *Engine) {
	for len(t.ids) > 0 {
		id := t.ids[0]
		t.ids = t.ids[1:]
		if id == "" || id == e.selfDeviceID || id == e.head.Core.AuthorityID {
			continue
		}
		if m, live := e.head.Core.Member(id); !live || m.Kind != devicesec.Client {
			continue
		}
		t.curID = id
		e.stepTask(&removeDeviceTask{id: id}, &secEvent{kind: evStart, fgGen: t.fgGen, target: parentTarget(t, 0)})
		return
	}
	t.curID = ""
	if e.ensureWF().record != nil {
		t.stage = remListPersist
		e.installRecordCAS(nil, nil, nil, nil, 0, setPendingRemovePatch(t.failed), parentTarget(t, 0))
		return
	}
	e.ensureWF().removingOldDevices = false
	e.emitStatusChanged()
	e.okTarget(t.target, nil)
}

func (t *removeDeviceListTask) step(e *Engine, ev *secEvent) error {
	if t.stage == remListPersist {
		e.ensureWF().removingOldDevices = false
		e.emitStatusChanged()
		e.okTarget(t.target, nil)
		return nil
	}
	// Record any device that failed to remove so it stays persisted in the
	// security record for retry/notice, while continuing with remaining IDs.
	if ev != nil && ev.err != nil && t.curID != "" {
		t.failed = append(t.failed, t.curID)
	}
	t.curID = ""
	t.next(e)
	return nil
}

const (
	approvePairSync uint8 = iota
	approvePairClaim
	approvePairResume
	approvePairRemoveOld
)

type approvePairingTask struct {
	taskBase
	pairingID       string
	sasConfirmed    bool
	removeDeviceIDs []string
	sess            *initiatorPairingSession
}

func (t *approvePairingTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil && t.stage != approvePairResume && t.stage != approvePairRemoveOld {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case approvePairSync:
		if err := e.checkFG(t.fgGen); err != nil {
			return err
		}
		if ev.kind == evStart {
			if !t.sasConfirmed {
				return newWfError("invalid", WorkflowReasonConfirmPatternsBeforeApproving)
			}
			sess := wf.initPairings[t.pairingID]
			if sess == nil || !sess.joined || len(sess.pairKey) != 32 || len(sess.candidateEd) != 32 || len(sess.candidateX) != 32 {
				return devicesec.ErrStale
			}
			if wf.pendingOp != nil {
				return newWfError("pending", WorkflowReasonFinishCurrentOpFirst)
			}
			t.sess = sess
			e.syncStates(false, "", parentTarget(t, 0))
			return nil
		}
		if !e.isAuthority() {
			return devicesec.ErrUntrusted
		}
		sess := t.sess
		allowed := make(map[string]bool, len(sess.replacementCandidates))
		for _, cand := range sess.replacementCandidates {
			if cand != nil && cand.DeviceId != "" {
				allowed[cand.DeviceId] = true
			}
		}
		var toRemove []string
		seenRemove := make(map[string]bool, len(t.removeDeviceIDs))
		for _, id := range t.removeDeviceIDs {
			if id == "" || id == e.selfDeviceID || id == e.head.Core.AuthorityID || id == sess.candidateID || !allowed[id] {
				return devicesec.ErrInvalid
			}
			member, live := e.head.Core.Member(id)
			if !live || member.Kind != devicesec.Client {
				return devicesec.ErrInvalid
			}
			if seenRemove[id] {
				continue
			}
			seenRemove[id] = true
			toRemove = append(toRemove, id)
		}
		sess.removeDeviceIDs = toRemove
		exp := sess.expiresMs
		if e.lastNowMs > 0 && (exp == 0 || exp > e.lastNowMs+maxPairWindowMs) {
			exp = e.lastNowMs + maxPairWindowMs
		}
		hh := e.headHash
		t.stage = approvePairClaim
		op := e.newPreparedOp(sess.server, e.selfDeviceID, "state", "pair", hh)
		op.TargetDeviceID = sess.candidateID
		op.Pair = &PersistedOpPair{
			PairingID: sess.pairingID, CandidateID: sess.candidateID, CandidateEd: slices.Clone(sess.candidateEd),
			CandidateX: slices.Clone(sess.candidateX), CandidateKind: uint32(sess.candidateKind),
			PairKey: slices.Clone(sess.pairKey), ExpiresMs: exp, ReplacesDeviceID: sess.replacesDeviceID,
			RemoveDeviceIDs: slices.Clone(toRemove),
		}
		e.claimOperationCAS(op, &hh, e.selfDeviceID, parentTarget(t, 0))
	case approvePairClaim:
		t.stage = approvePairResume
		e.startResumePendingOp(t.fgGen, parentTarget(t, 0))
	case approvePairResume:
		if ev.err != nil {
			e.completeTarget(t.target, &secEvent{wfRes: ev.wfRes, err: ev.err})
			return nil
		}
		t.sess.approved = true
		pairPending := wf.pendingOp != nil && wf.pendingOp.Kind == "state" && wf.pendingOp.Pair != nil && wf.pendingOp.Pair.PairingID == t.pairingID
		if !pairPending {
			if len(t.sess.removeDeviceIDs) > 0 {
				ids := t.sess.removeDeviceIDs
				t.sess.removeDeviceIDs = nil
				t.stage = approvePairRemoveOld
				e.startRemoveDeviceList(t.fgGen, ids, parentTarget(t, 0))
				return nil
			}
			t.sess.delivered = true
			delete(wf.initPairings, t.pairingID)
		}
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{Pairing: e.initiatorSessionToProto(t.sess)})
	case approvePairRemoveOld:
		t.sess.delivered = true
		delete(wf.initPairings, t.pairingID)
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{Pairing: e.initiatorSessionToProto(t.sess)})
	}
	return nil
}

const (
	finishPairResume uint8 = iota
	finishPairRemoveOld
)

type finishPairingDeliveryTask struct {
	taskBase
	pairingID, pid string
	sess           *initiatorPairingSession
}

func (t *finishPairingDeliveryTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	wf := e.ensureWF()
	if t.stage == finishPairRemoveOld {
		if t.sess != nil {
			t.sess.delivered, t.sess.approved = true, true
			delete(wf.initPairings, t.pid)
		}
		e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{Pairing: e.initiatorSessionToProto(t.sess)})
		return nil
	}
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
	pairPending := wf.pendingOp != nil && wf.pendingOp.Kind == "state" && wf.pendingOp.Pair != nil && wf.pendingOp.Pair.PairingID == t.pid
	if !pairPending {
		if sess != nil && len(sess.removeDeviceIDs) > 0 {
			ids := sess.removeDeviceIDs
			sess.removeDeviceIDs = nil
			t.sess = sess
			t.stage = finishPairRemoveOld
			e.startRemoveDeviceList(t.fgGen, ids, parentTarget(t, 0))
			return nil
		}
		if sess != nil {
			sess.delivered, sess.approved = true, true
			delete(wf.initPairings, t.pid)
		} else {
			sess = &initiatorPairingSession{pairingID: t.pid, approved: true, delivered: true}
		}
	}
	e.okTarget(t.target, &corev1.SecurityWorkflowResultEffect{Pairing: e.initiatorSessionToProto(sess)})
	return nil
}

const (
	cancelInitStart uint8 = iota
	cancelInitRPC
	cancelInitClear
)

type cancelPairingInitiatorTask struct {
	taskBase
	pairingID string
}

func (t *cancelPairingInitiatorTask) cleanupDraft(e *Engine) {
	if op := e.ensureWF().pendingOp; op != nil && op.Pair != nil && op.Pair.PairingID == t.pairingID && op.Phase == "PREPARED" && len(op.Signed) == 0 {
		t.stage = cancelInitClear
		e.clearOperationCAS(op, parentTarget(t, 0))
		return
	}
	e.okTarget(t.target, nil)
}

func (t *cancelPairingInitiatorTask) step(e *Engine, ev *secEvent) error {
	t.acceptStart(ev)
	if ev != nil && ev.err != nil && t.stage == cancelInitStart {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case cancelInitStart:
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
		t.stage = cancelInitRPC
		e.callAuthedProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_CANCEL_PAIRING, srv, 0, &v1.CancelPairingRequest{PairingId: t.pairingID}, parentTarget(t, 0))
	case cancelInitRPC:
		t.cleanupDraft(e)
	case cancelInitClear:
		e.okTarget(t.target, nil)
	}
	return nil
}

const (
	joinStartEntropy uint8 = iota
	joinStartKeyEd
	joinStartKeyX
	joinStartKeyEphem
	joinStartPeek
	joinStartSaveCand
	joinStartRPC
)

type startJoinPairingTask struct {
	taskBase
	server, code, edSlot, xSlot, ephemSlot string
	platform, description                  string
	commitHint, edPub, xPub, ephemPub      []byte
	existingRepair                         bool
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
	if lvl >= 1 && !t.existingRepair {
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
	t.edPub, t.cleanLevel, t.stage = edPub, 1, joinStartKeyX
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
			e.emitActionResult(actionID, nil, newWfError("invalid", WorkflowReasonInvalidPairingKey))
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
		e.emitActionResult(actionID, nil, newWfError("pending", WorkflowReasonFinishCurrentOpFirst))
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
	e.clearActivePollWithError(fatalWfError("canceled", WorkflowReasonPairingCanceled, ""))
	for id, old := range wf.joinPairings {
		if old != nil {
			e.discardJoinSessionKeys(old)
		}
		delete(wf.joinPairings, id)
	}
	e.waitEntropy(96, &startJoinPairingTask{
		taskBase: taskBase{target: actionTarget(actionID, targetAction)}, server: server, code: code,
		platform: strings.TrimSpace(act.Platform), description: strings.TrimSpace(act.Description),
		commitHint: commitHint, existingRepair: act.ExistingRepair,
	}, 0)
}

func (t *startJoinPairingTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil {
		return ev.err
	}
	wf := e.ensureWF()
	switch t.stage {
	case joinStartEntropy:
		tok := e.nextWorkflowToken()
		if !t.existingRepair {
			t.edSlot = "join-ed-" + tok
		}
		t.xSlot, t.ephemSlot = "join-x-"+tok, "join-ephem-"+tok
		if t.existingRepair {
			t.genXAndEphem(e, slices.Clone(wf.deviceEdPub))
			return nil
		}
		t.stage = joinStartKeyEd
		e.waitKeyGen(keyCandEd, t.edSlot, true, t, 0)
	case joinStartKeyEd:
		t.genXAndEphem(e, ev.bytes)
	case joinStartKeyX:
		t.xPub, t.cleanLevel, t.stage = ev.bytes, 2, joinStartKeyEphem
		e.waitKeyGen(keyEphemX, t.ephemSlot, false, t, 0)
	case joinStartKeyEphem:
		t.ephemPub, t.cleanLevel, t.stage = ev.bytes, 3, joinStartPeek
		e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PEEK_PAIRING, t.server, "", &v1.PeekPairingRequest{Code: t.code, ProtocolVersion: e.relayVersion}, t)
	case joinStartPeek:
		var pr v1.PeekPairingResponse
		if pr.Unmarshal(ev.bytes) != nil || pr.PairingId == "" || len(pr.Commit) != 32 {
			return devicesec.ErrInvalid
		}
		if len(t.commitHint) == 32 && !sameBytes(pr.Commit, t.commitHint) {
			return devicesec.ErrUntrusted
		}
		candBytes, err := (&PersistedPairingCandidate{
			Server: t.server, PairingID: pr.PairingId, EdSlot: t.edSlot, XSlot: t.xSlot,
			EdPublic: slices.Clone(t.edPub), XPublic: slices.Clone(t.xPub), ReusesDeviceEd: t.existingRepair,
		}).Encode()
		if err != nil {
			return err
		}
		t.peekResp, t.stage = pr, joinStartSaveCand
		e.writeStorageCAS(bktCand, PairingCandidateStorageKey(t.server, pr.PairingId), "", e.nextWorkflowToken(), false, candBytes, t)
	case joinStartSaveCand:
		t.cleanLevel, t.stage = 4, joinStartRPC
		e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_JOIN_PAIRING, t.server, "", &v1.JoinPairingRequest{
			Code: t.code, JoinerPublic: slices.Clone(t.ephemPub), JoinerKind: v1.DeviceKind_DEVICE_KIND_CLIENT,
			DevicePublicKey: slices.Clone(t.edPub), DeviceHpkePublicKey: slices.Clone(t.xPub), ProtocolVersion: e.relayVersion,
		}, t)
	case joinStartRPC:
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
			platform: t.platform, description: t.description,
			ephemSlot: t.ephemSlot, ephemPub: slices.Clone(t.ephemPub), edSlot: t.edSlot, xSlot: t.xSlot,
			edPublic: slices.Clone(t.edPub), xPublic: slices.Clone(t.xPub), reusesDeviceEd: t.existingRepair, candidateToken: jr.CandidateToken,
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
	e.clearActivePollWithError(fatalWfError("canceled", WorkflowReasonPairingCanceled, ""))
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
		e.clearActivePollWithError(fatalWfError("canceled", WorkflowReasonPairingCanceled, ""))
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

const (
	pollJoinGet uint8 = iota
	pollJoinRevealAgree
	pollJoinMetaEntropy
	pollJoinSendMeta
	pollJoinSignReg
	pollJoinWriteIntent
	pollJoinResumeReg
)

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
	t.st, t.pin, t.approver, t.stage = *st, pin, approver, pollJoinRevealAgree
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
	if hdr.Unmarshal(st.Payload.Header) != nil || hdr.Kind != v1.PayloadKind_PAYLOAD_KIND_PAIRING || hdr.RequestId != sess.pairingID || hdr.Seq != 0 {
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
	sess.registering, t.signingPhase, t.stage = true, true, pollJoinSignReg
	e.waitSign(signKind, signSlot, append(append([]byte{}, registerContext...), regBody...), t, 0)
	return nil
}

func (t *pollJoinTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil && t.stage != pollJoinGet && t.stage != pollJoinResumeReg && t.stage != pollJoinSendMeta {
		return ev.err
	}
	wf, sess := e.ensureWF(), t.sess
	switch t.stage {
	case pollJoinGet:
		if wf.activePoll == nil || wf.activePoll.pairingID != t.pairingID {
			return nil
		}
		if ev.rpcErr != nil {
			if ev.rpcErr.isJoinPairingGone() {
				return fatalWfError("canceled", WorkflowReasonPairingExpiredOrCanceled, "")
			}
			e.scheduleSecurityPollTimer()
			return nil
		} else if ev.err != nil {
			return ev.err
		}
		var st v1.GetPairingResponse
		if st.Unmarshal(ev.bytes) != nil {
			return devicesec.ErrInvalid
		}
		if st.State == v1.PairingState_PAIRING_STATE_CANCELED {
			return fatalWfError("canceled", WorkflowReasonPairingExpiredOrCanceled, "")
		}
		if st.State < t.want {
			e.scheduleSecurityPollTimer()
			return nil
		}
		if t.want == v1.PairingState_PAIRING_STATE_REVEALED {
			return t.handleRevealed(e, &st)
		}
		return t.handleApproved(e, &st)
	case pollJoinRevealAgree:
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
		if sess.platform != "" && sess.description != "" {
			t.stage = pollJoinMetaEntropy
			e.waitEntropy(16, t, 0)
			return nil
		}
		e.resolveActivePoll(t.pairingID, &corev1.SecurityWorkflowResultEffect{Pairing: joinerSessionToProto(sess)}, nil)
	case pollJoinMetaEntropy:
		var nonce [12]byte
		if e.readRandom(nonce[:]) {
			if env := sealJoinerMetaEnvelope(sess.pairKey, nonce[:], sess.pairingID, &v1.DeviceMeta{
				Platform: sess.platform, Description: sess.description,
			}); env != nil {
				t.stage = pollJoinSendMeta
				e.waitProtoRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_PAIRING, sess.server, "", &v1.GetPairingRequest{
					PairingId: sess.pairingID, CandidateToken: sess.candidateToken, JoinerMeta: env,
				}, t)
				return nil
			}
		}
		e.resolveActivePoll(t.pairingID, &corev1.SecurityWorkflowResultEffect{Pairing: joinerSessionToProto(sess)}, nil)
	case pollJoinSendMeta:
		e.resolveActivePoll(t.pairingID, &corev1.SecurityWorkflowResultEffect{Pairing: joinerSessionToProto(sess)}, nil)
	case pollJoinSignReg:
		regReqBytes, err := (&v1.RegisterDeviceRequest{
			Registration: t.regBody, Signature: ev.bytes, Approval: slices.Clone(t.payload.Approval),
			ApprovalSignature: slices.Clone(t.payload.ApprovalSignature), ProtocolVersion: e.relayVersion,
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
		t.intent, t.intentRev, t.stage = intent, intentRev, pollJoinWriteIntent
		e.writeStorageCAS(bktIntent, "intent", "", intentRev, true, intentBytes, t)
	case pollJoinWriteIntent:
		wf.regIntent, wf.regIntentRev, e.securityLocked, t.stage = t.intent, t.intentRev, true, pollJoinResumeReg
		e.startResumeRegistration(0, parentTarget(t, 0))
	case pollJoinResumeReg:
		sess.registering = false
		e.resolveActivePoll(t.pairingID, ev.wfRes, ev.err)
	}
	return nil
}

const (
	resumeRegRPC uint8 = iota
	resumeRegFallbackLogin
	resumeRegCommit
	resumeRegApply
)

type resumeRegistrationTask struct {
	taskBase
	intent *PersistedRegistrationIntent
	regErr *rpcError
	prof   *PersistedPublicProfile
	rev    string
}

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
	t.prof, t.rev, t.stage = prof, rev, resumeRegCommit
	e.batchCommitStorage(promoteEd, intent.XSlot, discard, true, batch, t)
	return nil
}

func (t *resumeRegistrationTask) step(e *Engine, ev *secEvent) error {
	if ev != nil && ev.err != nil && t.stage != resumeRegRPC && t.stage != resumeRegFallbackLogin {
		return ev.err
	}
	wf, intent := e.ensureWF(), t.intent
	switch t.stage {
	case resumeRegRPC:
		if ev.rpcErr == nil {
			if ev.err != nil {
				return ev.err
			}
			var resp v1.RegisterDeviceResponse
			if resp.Unmarshal(ev.bytes) != nil || resp.DeviceId != intent.DeviceID {
				return devicesec.ErrUntrusted
			}
			return t.commitIntent(e)
		}
		if ev.rpcErr.isResumeRegFallbackLogin() {
			kind, slot := keyCandEd, intent.EdSlot
			if intent.ReusesDeviceEd {
				kind, slot = keyDevEd, ""
			}
			t.regErr, t.stage = ev.rpcErr, resumeRegFallbackLogin
			e.forceLoginToken(intent.Server, kind, slot, intent.DeviceID, false, 0, false, parentTarget(t, 0))
			return nil
		}
		return ev.rpcErr
	case resumeRegFallbackLogin:
		if ev.err == nil {
			return t.commitIntent(e)
		}
		return t.regErr
	case resumeRegCommit:
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
		t.stage = resumeRegApply
		e.applyPersistedRecordToEngine(rec, parentTarget(t, 0))
	case resumeRegApply:
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
