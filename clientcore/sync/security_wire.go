package sync

import (
	"bytes"
	"errors"
	"maps"

	"github.com/Lewin671/remote-agent/clientcore/domain/entropy"
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/security"
	"github.com/Lewin671/remote-agent/clientcore/text"
	"github.com/Lewin671/remote-agent/clientcore/transport"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

type decodedMachine struct {
	MachineID  string
	Online     bool
	LastSeenMs int64
	Meta       *v1.MachineMeta
	MetaRev    uint64
	MetaBroken bool
	Wire       *v1.Machine
	CacheRef   string
}

type decodedSession struct {
	SessionID         string
	MachineID         string
	LastSeq           uint64
	UpdatedMs         int64
	ListedEndedMs     int64
	Meta              *v1.Session
	MetaRev           uint64
	Checkpoint        *v1.LiveSnapshot
	CheckpointInvalid bool
	RetiredHistory    bool
	OpenFailed        bool
	Wire              *v1.RemoteSession
	CacheRef          string
}

type decodedDevice struct {
	DeviceID   string
	Kind       v1.DeviceKind
	CreatedMs  int64
	LastSeenMs int64
	Meta       *v1.DeviceMeta
	MetaRev    uint64
	MetaBroken bool
}

type decodedPrefs struct {
	Revision uint64
	Prefs    *v1.ProjectPrefs
	Broken   bool
	Wire     *v1.AccountPrefsObject
	CacheRef string
}

type decodedEventItem struct {
	Seq         uint64
	Event       *v1.Event
	Broken      bool
	Retired     bool
	Unavailable bool
	Verified    bool
	ApproxBytes int64
	RawStored   *v1.StoredEvent
	CacheRef    string
}

func (e *Engine) maybeRequestEntropy(ctx *StepContext) {
	if ctx == nil || e.entropyNeedRequested {
		return
	}
	if e.secEngine != nil && e.secEngine.Locked() {
		if !e.entropy.NeedsRefill() {
			return
		}
		e.entropyNeedRequested = true
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_RequestEntropy{
				RequestEntropy: &corev1.RequestEntropyEffect{
					MinIds:         EntropyRefillCount,
					MinFloats:      EntropyRefillCount,
					MinRandomBytes: entropy.RefillBytes,
				},
			},
		})
		return
	}
	if e.entropy.Avail() > 0 && e.entropy.NeedsRefill() {
		e.entropyNeedRequested = true
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_RequestEntropy{
				RequestEntropy: &corev1.RequestEntropyEffect{
					MinIds:         EntropyRefillCount,
					MinFloats:      EntropyRefillCount,
					MinRandomBytes: entropy.RefillBytes,
				},
			},
		})
		return
	}
	ids, floats := e.entropy.LegacyCounts()
	var needIDs, needFloats int32
	if ids < EntropyLowWater {
		needIDs = EntropyRefillCount
	}
	if floats < EntropyLowWater {
		needFloats = EntropyRefillCount
	}
	if needIDs > 0 || needFloats > 0 {
		e.entropyNeedRequested = true
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_RequestEntropy{
				RequestEntropy: &corev1.RequestEntropyEffect{
					MinIds:    needIDs,
					MinFloats: needFloats,
				},
			},
		})
	}
}

func (e *Engine) nextID(ctx *StepContext) string {
	id := e.entropy.TakeIDOrFallback()
	e.maybeRequestEntropy(ctx)
	return id
}

func (e *Engine) nextFloat(ctx *StepContext) float64 {
	f := e.entropy.TakeUnitFloatOrFallback()
	e.maybeRequestEntropy(ctx)
	return f
}

func (e *Engine) emitRawEffects(ctx *StepContext, effs []*corev1.Effect) {
	if ctx == nil || len(effs) == 0 {
		return
	}
	for _, eff := range effs {
		if eff != nil {
			ctx.emit(eff)
		}
	}
}

func (e *Engine) syncSecurityStateFromEngine(ctx *StepContext, pushToTransport bool) {
	if e.secEngine == nil {
		return
	}
	snap := e.secEngine.Snapshot()
	e.security.Loaded = snap.Loaded
	e.security.IsMember = snap.IsMember
	if snap.SelfDeviceID != "" {
		e.security.SelfDeviceID = snap.SelfDeviceID
	}
	e.security.AuthorityDeviceID = snap.AuthorityDeviceID
	e.security.TrustedMachines = snap.TrustedMachines
	e.security.TrustedClients = snap.TrustedClients
	e.security.ArchiveMachines = snap.ArchiveMachines
	e.security.HeadEpoch = snap.HeadEpoch
	e.security.LeaseEndMs = snap.LeaseEndMs
	e.security.ClockSkewed = snap.ClockSkewed
	e.security.RecoveryPending = snap.RecoveryPending
	e.security.RecoveryReadyMs = snap.RecoveryReadyMs
	e.security.RecoveryObjectsPending = snap.RecoveryObjectsPending
	e.security.NetworkGen = snap.NetworkGen
	e.security.VerifiedNetGen = snap.VerifiedNetGen

	if pushToTransport {
		c := e.ensureTransportCoord()
		if ci, ok := e.secEngine.CoordInfo(); ok {
			members := make([]transport.CoordMember, len(ci.Members))
			for i, m := range ci.Members {
				members[i] = transport.CoordMember{
					ID:    m.ID,
					EdPub: m.EdPub,
					XPub:  m.XPub,
					Self:  m.Self,
					Kind:  m.Kind,
				}
			}
			ts := transport.CoordSecuritySnapshot{
				Member:        ci.Member,
				Account:       ci.Account,
				Domain:        ci.Domain,
				Head:          ci.Head,
				HeadHash:      ci.HeadHash,
				Epoch:         ci.Epoch,
				StartedAt:     ci.StartedAt,
				LeaseNotAfter: ci.LeaseNotAfter,
				SelfID:        ci.SelfID,
				SelfEdPub:     ci.SelfEdPub,
				SelfXPub:      ci.SelfXPub,
				Members:       members,
			}
			e.emitCoordEffects(ctx, c.OnSecuritySnapshot(e.nowMs, ts))
		} else if snap.Removed || snap.Cleared {
			e.emitCoordEffects(ctx, c.OnSecurityRejected(e.nowMs))
		}
	}
	e.updateLivePollers(ctx)
	ctx.Dirty.MarkAll()
}

func (e *Engine) handleSecurityConfig(ctx *StepContext, ev *corev1.SecurityConfigEvent) {
	if ev == nil {
		return
	}
	e.secEngine.Configure(ev)
	if ev.DeviceId != "" {
		e.security.SelfDeviceID = ev.DeviceId
	}
	e.maybeRequestEntropy(ctx)
}

func (e *Engine) handleSecurityRecord(ctx *StepContext, ev *corev1.SecurityRecordEvent) {
	if ev == nil {
		return
	}
	if ev.OpId != 0 {
		if e.activeSecOpID != 0 && ev.OpId != e.activeSecOpID {
			return
		}
		if ev.OpId == e.activeSecOpID {
			e.activeSecOpID = 0
		}
	}
	effs := e.secEngine.ApplyRecord(e.nowMs, ev)
	e.emitRawEffects(ctx, effs)
	e.syncSecurityStateFromEngine(ctx, true)
	if _, ok := e.secEngine.CoordInfo(); ev.Error != "" || (!ok && !ev.Removed && !ev.Cleared) {
		e.emitCoordEffects(ctx, e.ensureTransportCoord().OnAuthorizeDone(e.nowMs, false))
	}
	e.maybeRequestEntropy(ctx)
}

func (e *Engine) handleSecurityKeySigned(ctx *StepContext, ev *corev1.SecurityKeySignedEvent) {
	effs := e.secEngine.HandleKeySigned(e.nowMs, ev)
	e.emitRawEffects(ctx, effs)
}

func (e *Engine) handleSecurityKeyAgreed(ctx *StepContext, ev *corev1.SecurityKeyAgreedEvent) {
	effs := e.secEngine.HandleKeyAgreed(e.nowMs, ev)
	e.emitRawEffects(ctx, effs)
	e.syncSecurityStateFromEngine(ctx, true)
}

func (e *Engine) handleSecurityCryptoBatchDone(ctx *StepContext, ev *corev1.SecurityCryptoBatchDoneEvent) {
	effs := e.secEngine.HandleCryptoBatchDone(e.nowMs, ev)
	e.emitRawEffects(ctx, effs)
}

func (e *Engine) handleSecurityOpRequest(ctx *StepContext, ev *corev1.SecurityOpRequestEvent) {
	effs := e.secEngine.HandleSecurityOp(e.nowMs, ev)
	e.emitRawEffects(ctx, effs)
}

func (e *Engine) handleSeedDemoWorld(ctx *StepContext, ev *corev1.SeedDemoWorldEvent) {
	if ev == nil || e.secEngine.Locked() || e.security.Loaded {
		return
	}
	e.coldStartGraceExpired = true
	e.clearColdStartGrace(ctx)
	e.w.World.MachinesListed = true
	e.w.World.SessionsListed = true
	e.w.World.LiveLoaded = true
	e.w.SessionListErrorText = nil
	e.w.World.SessionListError = ""

	for _, m := range ev.Machines {
		if m == nil || m.MachineId == "" {
			continue
		}
		e.putMachineDecoded(ctx, decodedMachine{
			MachineID:  m.MachineId,
			Online:     m.Online,
			LastSeenMs: m.LastSeenMs,
			Meta:       m.Meta,
		}, false, false)
	}
	for _, s := range ev.Sessions {
		if s == nil || s.SessionId == "" {
			continue
		}
		e.listed[s.SessionId] = true
		e.putSessionDecoded(ctx, decodedSession{
			SessionID:     s.SessionId,
			MachineID:     s.MachineId,
			LastSeq:       s.LastSeq,
			UpdatedMs:     s.UpdatedMs,
			ListedEndedMs: s.ListedEndedMs,
			Meta:          s.Meta,
			Checkpoint:    s.Checkpoint,
		}, false)
	}
	for _, d := range ev.Devices {
		if d == nil || d.DeviceId == "" {
			continue
		}
		e.devices[d.DeviceId] = decodedToDeviceEntry(decodedDevice{
			DeviceID:   d.DeviceId,
			Kind:       d.Kind,
			CreatedMs:  d.CreatedMs,
			LastSeenMs: d.LastSeenMs,
			Meta:       d.Meta,
		})
	}
	if ev.Prefs != nil {
		e.applyAccountPrefsDecoded(&decodedPrefs{
			Revision: 1,
			Prefs:    ev.Prefs,
		}, true)
	}
	for _, se := range ev.SessionEvents {
		if se == nil || se.SessionId == "" {
			continue
		}
		b := e.buffers[se.SessionId]
		if b == nil {
			var agent v1.Agent
			if sess := e.sessions[se.SessionId]; sess != nil && sess.Info != nil {
				agent = sess.Info.Agent
			}
			b = newEventBuffer(se.SessionId, agent, e.monoMs)
			e.buffers[se.SessionId] = b
		}
		items := make([]decodedEventItem, 0, len(se.Events))
		for _, item := range se.Events {
			if item == nil || item.Seq == 0 {
				continue
			}
			items = append(items, decodedEventItem{
				Seq:         item.Seq,
				Event:       item,
				Verified:    true,
				ApproxBytes: 128,
			})
		}
		b.loaded = true
		b.awaitingCache = false
		e.ingestDecodedEvents(ctx, se.SessionId, b, items, false, false, true, false)
	}
	e.updateLivePollers(ctx)
	ctx.Dirty.MarkAll()
	ctx.Dirty.MarkSessionData()
}

func validateSessionCheckpoint(sid string, meta *v1.Session) (*v1.LiveSnapshot, bool) {
	if meta == nil || len(meta.CurrentCheckpoint) == 0 {
		return nil, false
	}
	var cp v1.LiveSnapshot
	if err := cp.Unmarshal(meta.CurrentCheckpoint); err != nil || cp.HasUnknown() {
		return nil, true
	}
	if cp.Session == nil || cp.Session.Id != sid || cp.Session.LastSeq > meta.LastSeq {
		return nil, true
	}
	for _, a := range cp.Actions {
		if a == nil || a.SessionId != sid || a.Seq > meta.LastSeq {
			return nil, true
		}
	}
	return &cp, false
}

func (e *Engine) openWireDirectoryBatch(
	ctx *StepContext,
	machines []*v1.Machine,
	sessions []*v1.RemoteSession,
	devices []*v1.Device,
	prefs []*v1.AccountPrefsObject,
	onDone func(
		outM []decodedMachine,
		outS []decodedSession,
		outD []decodedDevice,
		outP []*decodedPrefs,
	),
) {
	var items []security.OpenItem
	type mIndices struct {
		metaIdx int
	}
	mIdx := make([]mIndices, len(machines))
	for i, m := range machines {
		mIdx[i].metaIdx = -1
		if m == nil || m.Id == "" || m.Meta == nil {
			continue
		}
		mIdx[i].metaIdx = len(items)
		items = append(items, security.OpenItem{
			Env:             m.Meta,
			ExpectedPurpose: devicesec.MachineMeta,
			MachineID:       m.Id,
			History:         true,
		})
	}

	type sIndices struct {
		keyIdx  int
		metaIdx int
	}
	sIdx := make([]sIndices, len(sessions))
	for i, rs := range sessions {
		sIdx[i] = sIndices{keyIdx: -1, metaIdx: -1}
		if rs == nil || rs.SessionId == "" {
			continue
		}
		if rs.Key != nil {
			sIdx[i].keyIdx = len(items)
			items = append(items, security.OpenItem{
				Env:             rs.Key,
				ExpectedPurpose: devicesec.SessionKey,
				MachineID:       rs.MachineId,
				SessionID:       rs.SessionId,
				History:         true,
			})
		}
		if rs.Meta != nil {
			sIdx[i].metaIdx = len(items)
			items = append(items, security.OpenItem{
				Env:             rs.Meta,
				ExpectedPurpose: devicesec.SessionMeta,
				MachineID:       rs.MachineId,
				SessionID:       rs.SessionId,
				History:         true,
			})
		}
	}

	dIdx := make([]int, len(devices))
	for i, d := range devices {
		dIdx[i] = -1
		if d == nil || d.Id == "" || d.Meta == nil || len(d.Meta.SecurityHeader) == 0 {
			continue
		}
		dIdx[i] = len(items)
		items = append(items, security.OpenItem{
			Env:             d.Meta,
			ExpectedPurpose: devicesec.DeviceMeta,
			RequestID:       d.Id,
			History:         true,
		})
	}

	pIdx := make([]int, len(prefs))
	for i, p := range prefs {
		pIdx[i] = -1
		if p == nil || p.Envelope == nil || p.Revision == 0 {
			continue
		}
		pIdx[i] = len(items)
		reqID := p.ObjectId
		if reqID == "" {
			reqID = ProjectPrefsObjectID
		}
		items = append(items, security.OpenItem{
			Env:             p.Envelope,
			ExpectedPurpose: devicesec.Prefs,
			RequestID:       reqID,
			Seq:             p.Revision,
			History:         true,
		})
	}

	finish := func(results []security.OpenResult) {
		outM := make([]decodedMachine, 0, len(machines))
		for i, m := range machines {
			if m == nil || m.Id == "" {
				continue
			}
			dm := decodedMachine{
				MachineID:  m.Id,
				Online:     m.Online,
				LastSeenMs: m.LastSeenMs,
				Wire:       m,
			}
			if idx := mIdx[i].metaIdx; idx >= 0 {
				r := results[idx]
				if r.Err != nil {
					dm.MetaBroken = true
				} else if mm, ok := r.Body.(*v1.MachineMeta); ok {
					dm.Meta = mm
					dm.MetaRev = r.Header.Seq
				}
			}
			outM = append(outM, dm)
		}

		outS := make([]decodedSession, 0, len(sessions))
		for i, rs := range sessions {
			if rs == nil || rs.SessionId == "" {
				continue
			}
			ds := decodedSession{
				SessionID:     rs.SessionId,
				MachineID:     rs.MachineId,
				LastSeq:       rs.LastSeq,
				UpdatedMs:     rs.UpdatedMs,
				ListedEndedMs: rs.EndedMs,
				Wire:          rs,
			}
			var keyErr error
			if idx := sIdx[i].keyIdx; idx >= 0 {
				keyErr = results[idx].Err
			}
			if keyErr != nil {
				if errors.Is(keyErr, security.ErrUnsupportedHistory) {
					ds.RetiredHistory = true
				} else {
					ds.OpenFailed = true
				}
			} else if idx := sIdx[i].metaIdx; idx >= 0 {
				r := results[idx]
				if r.Err != nil {
					if errors.Is(r.Err, security.ErrUnsupportedHistory) {
						ds.RetiredHistory = true
					} else {
						ds.OpenFailed = true
					}
				} else if meta, ok := r.Body.(*v1.Session); ok {
					ds.Meta = meta
					ds.MetaRev = r.Header.Seq
					if len(meta.MachineReplacements) > 0 {
						if e.sessionReplacements == nil {
							e.sessionReplacements = make(map[string][]*v1.MachineReplacementProof)
						}
						e.sessionReplacements[rs.SessionId] = append([]*v1.MachineReplacementProof(nil), meta.MachineReplacements...)
					}
					ds.Checkpoint, ds.CheckpointInvalid = validateSessionCheckpoint(rs.SessionId, meta)
				}
			}
			outS = append(outS, ds)
		}

		outD := make([]decodedDevice, 0, len(devices))
		for i, d := range devices {
			if d == nil || d.Id == "" {
				continue
			}
			dd := decodedDevice{
				DeviceID:   d.Id,
				Kind:       d.Kind,
				CreatedMs:  d.CreatedMs,
				LastSeenMs: d.LastSeenMs,
			}
			if idx := dIdx[i]; idx >= 0 {
				r := results[idx]
				if r.Err != nil {
					if !errors.Is(r.Err, security.ErrRecoveryHistoryUnavailable) {
						dd.MetaBroken = true
					}
				} else if dm, ok := r.Body.(*v1.DeviceMeta); ok {
					dd.Meta = dm
					dd.MetaRev = r.Header.Seq
				}
			}
			outD = append(outD, dd)
		}

		outP := make([]*decodedPrefs, len(prefs))
		for i, p := range prefs {
			if p == nil || p.Envelope == nil || p.Revision == 0 {
				outP[i] = &decodedPrefs{
					Revision: 0,
					Prefs:    &v1.ProjectPrefs{Schema: 1},
					Wire:     p,
				}
				continue
			}
			dp := &decodedPrefs{
				Revision: p.Revision,
				Wire:     p,
			}
			if idx := pIdx[i]; idx >= 0 {
				r := results[idx]
				if r.Err != nil {
					dp.Broken = true
				} else if pp, ok := r.Body.(*v1.ProjectPrefs); ok && r.Header.Seq == p.Revision {
					dp.Prefs = pp
				} else {
					dp.Broken = true
				}
			}
			outP[i] = dp
		}

		onDone(outM, outS, outD, outP)
	}

	if len(items) == 0 {
		finish(nil)
		return
	}
	effs := e.secEngine.OpenBatch(e.nowMs, items, finish)
	e.emitRawEffects(ctx, effs)
}

func (e *Engine) buildAuthoritativeDevices(opened []decodedDevice) []decodedDevice {
	if !e.security.Loaded || (len(e.security.TrustedMachines) == 0 && len(e.security.TrustedClients) == 0) {
		return opened
	}
	byID := make(map[string]decodedDevice, len(opened))
	for _, d := range opened {
		byID[d.DeviceID] = d
	}
	var out []decodedDevice
	for id := range e.security.TrustedMachines {
		if d, ok := byID[id]; ok {
			out = append(out, d)
		} else {
			out = append(out, decodedDevice{DeviceID: id, Kind: v1.DeviceKind_DEVICE_KIND_MACHINE})
		}
	}
	for id := range e.security.TrustedClients {
		if d, ok := byID[id]; ok {
			out = append(out, d)
		} else {
			out = append(out, decodedDevice{DeviceID: id, Kind: v1.DeviceKind_DEVICE_KIND_CLIENT})
		}
	}
	return out
}

func (e *Engine) applyListsDecoded(
	ctx *StepContext,
	machines []decodedMachine,
	sessions []decodedSession,
	devices []decodedDevice,
	evNext *v1.SessionCursor,
	mp, sp, rp, dp map[string]uint64,
) {
	e.listBackoffMs = ListRetryInitialMs
	e.listingGen++
	e.pageOpID = 0
	e.w.World.LoadingMoreSessions = false
	e.w.World.MachinesListed = true
	e.w.World.SessionsListed = true
	e.w.World.LiveLoaded = true
	e.coldStartGraceExpired = true
	e.clearColdStartGrace(ctx)
	e.w.SessionListErrorText = nil
	e.w.World.SessionListError = ""

	mids := make(map[string]bool, len(machines))
	for _, m := range machines {
		if m.MachineID != "" {
			mids[m.MachineID] = true
		}
	}
	sids := make([]string, 0, len(sessions))
	sidSet := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		if s.SessionID != "" {
			sids = append(sids, s.SessionID)
			sidSet[s.SessionID] = true
		}
	}

	var pageNext *lists.SessionPlace
	if evNext != nil && (evNext.EndedMs != 0 || evNext.SessionId != "") {
		pageNext = &lists.SessionPlace{
			EndedMs:   evNext.EndedMs,
			SessionID: evNext.SessionId,
		}
	}

	for id := range e.machines {
		if !mids[id] && !pushedSince(e.machinePushes, mp, id) {
			e.dropMachine(ctx, id)
		}
	}

	knownMachines := make([]string, 0, len(mids)+len(e.machines))
	seenMachines := make(map[string]bool, len(mids)+len(e.machines))
	for id := range mids {
		seenMachines[id] = true
		knownMachines = append(knownMachines, id)
	}
	for id := range e.machines {
		if !seenMachines[id] {
			seenMachines[id] = true
			knownMachines = append(knownMachines, id)
		}
	}

	var reconcile []lists.ReconcileSessionEntry
	var pushedIDs []string
	for id, cur := range e.sessions {
		if cur == nil || sidSet[id] {
			continue
		}
		if pushedSince(e.sessionPushes, sp, id) {
			pushedIDs = append(pushedIDs, id)
			continue
		}
		reconcile = append(reconcile, lists.ReconcileSessionEntry{
			ID:            id,
			MachineID:     cur.MachineID,
			ListedEndedMs: cur.ListedEndedMs,
		})
	}
	removeIDs, unlistIDs := lists.ReconcileEndedSessions(nil, reconcile, sids, pushedIDs, knownMachines, pageNext)

	for _, id := range removeIDs {
		e.removeSession(ctx, id, true)
	}
	for _, id := range unlistIDs {
		delete(e.listed, id)
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_CacheDelete{
				CacheDelete: &corev1.CacheDeleteEffect{
					Kind: &corev1.CacheDeleteEffect_UnlistSessionId{UnlistSessionId: id},
				},
			},
		})
	}
	for id := range e.listed {
		if !sidSet[id] && !pushedSince(e.sessionPushes, sp, id) {
			delete(e.listed, id)
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_CacheDelete{
					CacheDelete: &corev1.CacheDeleteEffect{
						Kind: &corev1.CacheDeleteEffect_UnlistSessionId{UnlistSessionId: id},
					},
				},
			})
		}
	}

	for _, m := range machines {
		if m.MachineID == "" || pushedSince(e.removedPushes, rp, m.MachineID) {
			continue
		}
		e.putMachineDecoded(ctx, m, pushedSince(e.machinePushes, mp, m.MachineID), true)
	}

	for _, s := range sessions {
		if s.SessionID == "" {
			continue
		}
		e.listed[s.SessionID] = true
		e.putSessionDecoded(ctx, s, true)
	}

	e.reconcileDecodedDevices(devices, rp, dp, false)
	e.w.World.SessionsNext = pageNext
	e.w.World.HasMoreSessions = pageNext != nil

	e.updateLivePollers(ctx)
	e.syncDirectWatched(ctx)
	if e.wantAllSessions && e.w.World.SessionsNext != nil {
		e.RequestMoreSessions(ctx)
	}
	ctx.Dirty.MarkAll()
}

func (e *Engine) openWireStoredEvents(
	ctx *StepContext,
	sid, machineID string,
	stored []*v1.StoredEvent,
	history bool,
	onDone func([]decodedEventItem),
) {
	if len(stored) == 0 {
		onDone(nil)
		return
	}
	var repls []*v1.MachineReplacementProof
	if history {
		repls = e.sessionReplacements[sid]
		if len(repls) == 0 {
			if sess := e.sessions[sid]; sess != nil && sess.Info != nil && len(sess.Info.MachineReplacements) > 0 {
				repls = sess.Info.MachineReplacements
			}
		}
	}
	items := make([]security.OpenItem, len(stored))
	for i, se := range stored {
		var env *v1.Envelope
		var seq uint64
		if se != nil {
			env = se.Envelope
			seq = se.Seq
		}
		items[i] = security.OpenItem{
			Env:             env,
			ExpectedPurpose: devicesec.Event,
			MachineID:       machineID,
			SessionID:       sid,
			Seq:             seq,
			History:         history,
			Replacements:    repls,
		}
	}
	effs := e.secEngine.OpenBatch(e.nowMs, items, func(results []security.OpenResult) {
		out := make([]decodedEventItem, 0, len(stored))
		for i, se := range stored {
			if se == nil || se.Seq == 0 {
				continue
			}
			r := results[i]
			if r.Err != nil {
				if errors.Is(r.Err, security.ErrRecoveryHistoryUnavailable) {
					out = append(out, decodedEventItem{
						Seq:         se.Seq,
						Unavailable: true,
						ApproxBytes: 64,
						RawStored:   se,
					})
					continue
				}
				out = append(out, decodedEventItem{
					Seq:     se.Seq,
					Broken:  true,
					Retired: errors.Is(r.Err, security.ErrUnsupportedHistory),
				})
				break
			}
			evMsg, ok := r.Body.(*v1.Event)
			if !ok || evMsg == nil || evMsg.Seq != se.Seq || evMsg.SessionId != sid {
				out = append(out, decodedEventItem{
					Seq:    se.Seq,
					Broken: true,
				})
				break
			}
			out = append(out, decodedEventItem{
				Seq:         se.Seq,
				Event:       evMsg,
				Verified:    true,
				ApproxBytes: max(int64(len(r.Plain)), 64),
				RawStored:   se,
			})
		}
		onDone(out)
	})
	e.emitRawEffects(ctx, effs)
}

func (e *Engine) sealAndEmitCommandAttempt(ctx *StepContext, prep *PreparedCommand, retryOfUnknown bool) {
	if prep.Sealing {
		return
	}
	prep.Sealing = true
	prep.SealingRetry = retryOfUnknown
	effs := e.secEngine.SealEnvelope(
		e.nowMs,
		devicesec.Command,
		prep.MachineID,
		prep.SessionID,
		prep.RequestID,
		0,
		uint64(max(0, prep.DeadlineMs)),
		prep.CommandBytes,
		false,
		func(sr security.SealResult) {
			stepCtx := e.activeStepCtx
			prep.Sealing = false
			if e.inFlightCmds[prep.RequestID] != prep || !e.running {
				return
			}
			if sr.Err != nil {
				prior := prep.Dispatch.Unresolved() || prep.SealingRetry
				cat := outcome.NotSent
				if prior {
					cat = outcome.Unknown
				}
				if errors.Is(sr.Err, security.ErrNeedEntropy) {
					e.maybeRequestEntropy(stepCtx)
				}
				errCode := errCodeFailedPrecond
				if prior {
					errCode = errCodeUnknown
				}
				var errText *corev1.Text
				switch {
				case errors.Is(sr.Err, security.ErrClockSkewed), errors.Is(sr.Err, devicesec.ErrClock):
					errText = text.Msg(text.KeyErrorsThisDeviceSClockIsBehindTheAccountStateRemote)
				case errors.Is(sr.Err, security.ErrLeaseExpired), errors.Is(sr.Err, devicesec.ErrStale), errors.Is(sr.Err, security.ErrNotMember):
					if prior {
						errText = text.Msg(text.KeyErrorsTheComputerSReplyCouldNotBeVerifiedCommand)
					} else {
						errText = text.Msg(text.KeyErrorsRemoteControlExpiredSavingChangesSessionControlAndNew)
					}
				default:
					errText = text.Plain(sr.Err.Error())
				}
				e.settleCommand(stepCtx, prep, cat, nil, errCode, errText, false)
				return
			}
			prep.SealedEnvelope = sr.Envelope
			prep.RequestKeyHash = sr.RequestKeyHash
			prep.PayloadHash = sr.PayloadHash
			e.emitCommandAttempt(stepCtx, prep, prep.SealingRetry)
		},
	)
	e.emitRawEffects(ctx, effs)
}

func (e *Engine) openSealedCommandOutcome(ctx *StepContext, prep *PreparedCommand, ev *corev1.CommandOutcomeEvent) {
	evCopy := *ev
	item := security.OpenItem{
		Env:             ev.SealedResult,
		ExpectedPurpose: devicesec.Result,
		MachineID:       prep.MachineID,
		SessionID:       prep.SessionID,
		RequestID:       prep.RequestID,
		History:         false,
	}
	effs := e.secEngine.OpenBatch(e.nowMs, []security.OpenItem{item}, func(results []security.OpenResult) {
		stepCtx := e.activeStepCtx
		if e.inFlightCmds[prep.RequestID] != prep {
			return
		}
		if evCopy.AttemptId != "" && !prep.PendingAttempts[evCopy.AttemptId] {
			return
		}
		evCopy.SealedResult = nil
		r := results[0]
		if r.Err != nil {
			evCopy.Signed = false
			evCopy.HashesMatch = false
			evCopy.Result = nil
			e.finishCommandOutcome(stepCtx, &evCopy)
			return
		}
		res, ok := r.Body.(*v1.CommandResult)
		if !ok || res == nil {
			evCopy.Signed = false
			evCopy.HashesMatch = false
			evCopy.Result = nil
			e.finishCommandOutcome(stepCtx, &evCopy)
			return
		}
		hashesMatch := len(prep.RequestKeyHash) == 32 && len(prep.PayloadHash) == 32 &&
			bytes.Equal(res.RequestKeyHash, prep.RequestKeyHash) &&
			bytes.Equal(res.PayloadHash, prep.PayloadHash) &&
			(res.Outcome == "conflict" || res.ConflictKind == "")
		outerMatchesInner := evCopy.RouteWasDirect || (evCopy.OuterOutcome == res.Outcome && evCopy.OuterReason == res.Reason)
		evCopy.Signed = true
		evCopy.HashesMatch = hashesMatch
		evCopy.OuterMatchesInner = outerMatchesInner
		evCopy.Result = res
		if evCopy.ObservedBytes == 0 {
			if pic := prep.Command.GetPutImageChunk(); pic != nil {
				evCopy.ObservedBytes = int64(len(pic.Data))
			} else if fc := res.GetGetSharedFileChunk(); fc != nil {
				evCopy.ObservedBytes = int64(len(fc.Data))
			}
		}
		if evCopy.RouteWasDirect {
			evCopy.OuterOutcome = res.Outcome
			evCopy.OuterReason = res.Reason
		}
		e.finishCommandOutcome(stepCtx, &evCopy)
	})
	e.emitRawEffects(ctx, effs)
}

func (e *Engine) sealAndEmitPrefsPut(ctx *StepContext, head *prefsQueueItem, baseRev uint64, nextPrefs *v1.ProjectPrefs) {
	if nextPrefs == nil {
		nextPrefs = &v1.ProjectPrefs{Schema: 1}
	} else {
		nextPrefs.Schema = 1
	}
	raw, err := nextPrefs.Marshal()
	if err != nil {
		e.finishPrefsOp(ctx, text.Plain(err.Error()))
		return
	}
	effs := e.secEngine.SealEnvelope(
		e.nowMs,
		devicesec.Prefs,
		"",
		"",
		ProjectPrefsObjectID,
		baseRev+1,
		0,
		raw,
		false,
		func(sr security.SealResult) {
			stepCtx := e.activeStepCtx
			if len(e.prefsQueue) == 0 || e.prefsQueue[0].OpID != head.OpID {
				return
			}
			if sr.Err != nil {
				if errors.Is(sr.Err, security.ErrNeedEntropy) {
					e.maybeRequestEntropy(stepCtx)
				}
				e.finishPrefsOp(stepCtx, text.Plain(sr.Err.Error()))
				return
			}
			curHead := &e.prefsQueue[0]
			curHead.Phase = prefsPhasePut
			curHead.ActiveOpID = e.allocOpID()
			stepCtx.emit(&corev1.Effect{
				Effect: &corev1.Effect_Rpc{
					Rpc: &corev1.RpcEffect{
						OpId: curHead.ActiveOpID,
						Kind: &corev1.RpcEffect_PutAccountPrefs{
							PutAccountPrefs: &corev1.PutAccountPrefsRpc{
								ObjectId:         ProjectPrefsObjectID,
								ExpectedRevision: baseRev,
								NextPrefs:        nextPrefs,
								SealedEnvelope:   sr.Envelope,
							},
						},
					},
				},
			})
		},
	)
	e.emitRawEffects(ctx, effs)
}

func (e *Engine) cloneRefreshSnapshots() (mp, sp, rp, dp map[string]uint64) {
	return maps.Clone(e.refreshSnapshotM), maps.Clone(e.refreshSnapshotS), maps.Clone(e.refreshSnapshotR), maps.Clone(e.refreshSnapshotD)
}
