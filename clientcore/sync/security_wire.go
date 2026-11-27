package sync

import (
	"bytes"
	"errors"
	"maps"
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/entropy"
	"github.com/Lewin671/remote-agent/clientcore/domain/imageparse"
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/security"
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
}

type decodedDevice struct {
	DeviceID   string
	Kind       v1.DeviceKind
	CreatedMs  int64
	LastSeenMs int64
	Meta       *v1.DeviceMeta
	MetaRev    uint64
	MetaBroken bool
	Registered bool
}

type decodedPrefs struct {
	Revision uint64
	Prefs    *v1.ProjectPrefs
	Broken   bool
	Wire     *v1.AccountPrefsObject
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
	e.emitSecurityEffects(ctx, effs)
}

func (e *Engine) emitSecurityEffects(ctx *StepContext, effs []*corev1.Effect) {
	if ctx == nil {
		return
	}
	if e.secEngine != nil {
		if wm := e.secEngine.DrainWorkflowMeta(); len(wm) > 0 {
			if ctx.SecurityWorkflowMeta == nil {
				ctx.SecurityWorkflowMeta = make(map[uint64]security.WorkflowMeta, len(wm))
			}
			maps.Copy(ctx.SecurityWorkflowMeta, wm)
		}
		if nm := e.secEngine.DrainNotifyOutcomes(); len(nm) > 0 {
			if ctx.SecurityNotifyOutcomes == nil {
				ctx.SecurityNotifyOutcomes = make(map[uint64]security.NotifyOutcome, len(nm))
			}
			maps.Copy(ctx.SecurityNotifyOutcomes, nm)
		}
		ctx.SecurityBootstrapError = e.secEngine.BootstrapError()
	}
	if len(effs) == 0 {
		return
	}
	for _, eff := range effs {
		if eff == nil {
			continue
		}
		if st := eff.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_SECURITY_POLL {
			e.scheduleTimer(ctx, st.Kind, st.Key, st.DelayMs)
			continue
		}
		if ct := eff.GetCancelTimer(); ct != nil && ct.Kind == corev1.TimerKind_TIMER_KIND_SECURITY_POLL {
			e.cancelTimer(ctx, ct.Kind, ct.Key)
			continue
		}
		if res := eff.GetSecurityWorkflowResult(); res != nil && e.selfDescribeActionID != 0 && res.ActionId == e.selfDescribeActionID {
			e.selfDescribeActionID = 0
			if res.Ok && e.running && !e.removed {
				e.startRefreshDevices(ctx, false)
			}
			continue
		}
		if tu := eff.GetSecurityTokenUpdated(); tu != nil {
			if tu.Cleared {
				e.cachedToken = ""
				e.tokenExpiresMs = 0
			} else if tu.Token != "" {
				e.cachedToken = tu.Token
				e.tokenExpiresMs = tu.ExpiresMs
			}
		}
		ctx.emit(eff)
	}
}

func (e *Engine) securityDeviceOffers() []security.DeviceOfferInfo {
	if len(e.devices) == 0 {
		return nil
	}
	out := make([]security.DeviceOfferInfo, 0, len(e.devices))
	for _, d := range e.devices {
		if d == nil {
			continue
		}
		var recEd []byte
		if d.Meta != nil {
			recEd = d.Meta.HandoffRecoveryEd
		}
		out = append(out, security.DeviceOfferInfo{
			ID:         d.ID,
			Kind:       d.Kind,
			RecoveryEd: recEd,
			Meta:       d.Meta,
			MetaRev:    d.MetaRev,
			LastSeenMs: d.LastSeenMs,
		})
	}
	return out
}

func (e *Engine) clearDemoState() {
	e.demoMode = false
	e.demoListError = ""
	e.demoPrefsError = ""
	e.demoSessionRetired = nil
	e.demoSessionLoadError = nil
	e.demoDirectReports = nil
}

func (e *Engine) syncSecurityStateFromEngine(ctx *StepContext, pushToTransport bool) {
	if e.secEngine == nil {
		return
	}
	snap := e.secEngine.Snapshot()
	if snap.Cleared || snap.Loaded || e.secEngine.Locked() {
		e.clearDemoState()
	}
	e.security.Loaded = snap.Loaded
	e.security.IsMember = snap.IsMember
	if snap.SelfDeviceID != "" {
		e.security.SelfDeviceID = snap.SelfDeviceID
	} else if snap.Cleared {
		e.security.SelfDeviceID = ""
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
	e.security.RecoveryStatusStale = snap.RecoveryStatusStale
	e.security.RecoveryObjectsPending = snap.RecoveryObjectsPending
	e.security.HasHandoffOffer = snap.HasHandoffOffer
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
	e.recomputeMachineReplacements()
	e.pruneUntrustedCaches(ctx)
	e.maybeDescribeSelf(ctx)
	e.updateLivePollers(ctx)
	ctx.Dirty.MarkAll()
	ctx.Dirty.MarkSecurity()
}

func (e *Engine) recomputeMachineReplacements() {
	if e.secEngine == nil {
		return
	}
	sids := make([]string, 0, len(e.sessions))
	for sid := range e.sessions {
		sids = append(sids, sid)
	}
	sortutil.Sort(len(sids), func(i, j int) bool { return sids[i] < sids[j] }, func(i, j int) { sids[i], sids[j] = sids[j], sids[i] })
	next := make(map[string]string)
	for _, sid := range sids {
		sess := e.sessions[sid]
		if sess == nil || sess.MachineID == "" {
			continue
		}
		repls := e.sessionReplacements[sid]
		if len(repls) == 0 && sess.Info != nil {
			repls = sess.Info.MachineReplacements
		}
		if len(repls) == 0 {
			continue
		}
		origins := e.secEngine.VerifiedMachineReplacementOrigins(sess.MachineID, repls)
		originIDs := make([]string, 0, len(origins))
		for oldID := range origins {
			originIDs = append(originIDs, oldID)
		}
		sortutil.Sort(len(originIDs), func(i, j int) bool { return originIDs[i] < originIDs[j] }, func(i, j int) { originIDs[i], originIDs[j] = originIDs[j], originIDs[i] })
		for _, oldID := range originIDs {
			if oldID == "" || oldID == sess.MachineID {
				continue
			}
			cur, exists := next[oldID]
			if !exists {
				next[oldID] = sess.MachineID
				continue
			}
			_, curActive := e.machines[cur]
			_, candActive := e.machines[sess.MachineID]
			if (!curActive && candActive) || (curActive == candActive && sess.MachineID < cur) {
				next[oldID] = sess.MachineID
			}
		}
	}
	e.machineReplacements = next
}

func (e *Engine) handleSecurityConfig(ctx *StepContext, ev *corev1.SecurityConfigEvent) {
	if ev == nil {
		return
	}
	e.clearDemoState()
	e.secEngine.Configure(ev)
	if ev.DeviceId != "" {
		e.security.SelfDeviceID = ev.DeviceId
	}
	if ev.Platform != "" {
		e.selfPlatform = ev.Platform
	}
	if ev.DeviceDescription != "" {
		e.selfDescription = ev.DeviceDescription
	}
	e.maybeDescribeSelf(ctx)
	e.maybeRequestEntropy(ctx)
}

func (e *Engine) handleSecurityKeySigned(ctx *StepContext, ev *corev1.SecurityKeySignedEvent) {
	effs := e.secEngine.HandleKeySigned(e.nowMs, ev)
	e.emitSecurityEffects(ctx, effs)
	e.maybeRequestEntropy(ctx)
}

func (e *Engine) handleSecurityKeyAgreed(ctx *StepContext, ev *corev1.SecurityKeyAgreedEvent) {
	prevMember := e.security.IsMember
	prevLoaded := e.security.Loaded
	effs := e.secEngine.HandleKeyAgreed(e.nowMs, ev)
	e.emitSecurityEffects(ctx, effs)
	snap := e.secEngine.Snapshot()
	if snap.IsMember != prevMember || snap.Loaded != prevLoaded {
		e.syncSecurityStateFromEngine(ctx, true)
	}
	e.maybeRequestEntropy(ctx)
}

func (e *Engine) handleSecurityCryptoBatchDone(ctx *StepContext, ev *corev1.SecurityCryptoBatchDoneEvent) {
	effs := e.secEngine.HandleCryptoBatchDone(e.nowMs, ev)
	e.emitSecurityEffects(ctx, effs)
}

func (e *Engine) handleSecurityKeyGenerated(ctx *StepContext, ev *corev1.SecurityKeyGeneratedEvent) {
	effs := e.secEngine.HandleKeyGenerated(e.nowMs, ev)
	e.emitSecurityEffects(ctx, effs)
	e.maybeRequestEntropy(ctx)
}

func (e *Engine) handleSecurityKeyEncapsulated(ctx *StepContext, ev *corev1.SecurityKeyEncapsulatedEvent) {
	effs := e.secEngine.HandleKeyEncapsulated(e.nowMs, ev)
	e.emitSecurityEffects(ctx, effs)
	e.maybeRequestEntropy(ctx)
}

func (e *Engine) handleSecurityStorageResult(ctx *StepContext, ev *corev1.SecurityStorageResultEvent) {
	effs := e.secEngine.HandleStorageResult(e.nowMs, ev)
	e.emitSecurityEffects(ctx, effs)
	e.maybeRequestEntropy(ctx)
}

func (e *Engine) handleSecurityForeground(ctx *StepContext, ev *corev1.SecurityForegroundEvent) {
	if ev != nil {
		effs := e.secEngine.HandleForeground(e.nowMs, ev.Foreground)
		e.emitSecurityEffects(ctx, effs)
	}
}

func (e *Engine) handleSecurityWorkflowAction(ctx *StepContext, ev *corev1.SecurityWorkflowActionEvent) {
	effs := e.secEngine.HandleWorkflowAction(e.nowMs, ev, e.securityDeviceOffers(), nil)
	e.emitSecurityEffects(ctx, effs)
	e.maybeRequestEntropy(ctx)
}

func (e *Engine) handleSecurityOpenNotify(ctx *StepContext, ev *corev1.SecurityOpenNotifyEvent) {
	effs := e.secEngine.HandleOpenNotify(e.nowMs, ev)
	e.emitSecurityEffects(ctx, effs)
	e.maybeRequestEntropy(ctx)
}

func (e *Engine) handleSecurityPollTimer(ctx *StepContext) {
	effs := e.secEngine.HandleSecurityPollTick(e.nowMs)
	e.emitSecurityEffects(ctx, effs)
	e.maybeRequestEntropy(ctx)
}

func (e *Engine) handleSeedDemoWorld(ctx *StepContext, ev *corev1.SeedDemoWorldEvent) {
	if ev == nil || e.secEngine.Locked() {
		return
	}
	e.demoMode = true
	if ev.ClearPrefsError {
		e.demoPrefsError = ""
	}
	if ev.PrefsError != "" {
		e.demoPrefsError = ev.PrefsError
	}
	if ev.ClearListError {
		e.demoListError = ""
	}
	if ev.ListError != "" {
		e.demoListError = ev.ListError
	}
	if s := ev.Security; s != nil {
		e.security.Loaded = true
		e.security.IsMember = s.IsMember
		e.removed = s.Removed
		if s.SelfDeviceId != "" {
			e.security.SelfDeviceID = s.SelfDeviceId
		}
		e.security.AuthorityDeviceID = s.AuthorityDeviceId
		e.security.TrustedMachines = toStringSet(s.TrustedMachineIds)
		e.security.TrustedClients = toStringSet(s.TrustedClientIds)
		e.security.ArchiveMachines = toStringSet(s.ArchiveMachineIds)
		e.security.HeadEpoch = s.HeadEpoch
		e.security.LeaseEndMs = s.LeaseEndMs
		e.security.ClockSkewed = s.ClockSkewed
		e.security.RecoveryPending = s.RecoveryPending
		e.security.RecoveryReadyMs = s.RecoveryReadyMs
		e.security.RecoveryObjectsPending = s.RecoveryObjectsPending
		e.security.NetworkGen = s.NetworkGeneration
		if s.NetworkGeneration == e.networkGen || s.NetworkGeneration >= e.security.VerifiedNetGen {
			e.security.VerifiedNetGen = s.NetworkGeneration
		}
	}

	if ev.ClearDirectReports {
		e.demoDirectReports = nil
		clear(e.directReady)
		ctx.Dirty.MarkDirect()
	}
	if len(ev.DirectReports) > 0 {
		if e.demoDirectReports == nil {
			e.demoDirectReports = make(map[string]*corev1.DemoDirectReport, len(ev.DirectReports))
		}
		for _, dr := range ev.DirectReports {
			if dr == nil || dr.MachineId == "" {
				continue
			}
			if dr.Report == nil {
				delete(e.demoDirectReports, dr.MachineId)
				delete(e.directReady, dr.MachineId)
			} else {
				e.demoDirectReports[dr.MachineId] = dr
				e.directReady[dr.MachineId] = dr.Enabled && dr.Report.State == corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED
			}
		}
		ctx.Dirty.MarkDirect()
	}

	if ev.ReplaceMachines {
		clear(e.machines)
		e.machineOrderStale = true
	}
	for _, mid := range ev.RemovedMachineIds {
		delete(e.machines, mid)
		e.machineOrderStale = true
	}
	if ev.ReplaceSessions {
		clear(e.sessions)
		clear(e.listed)
		e.sessionOrderStale = true
	}
	for _, sid := range ev.RemovedSessionIds {
		delete(e.sessions, sid)
		delete(e.listed, sid)
		delete(e.buffers, sid)
		e.sessionOrderStale = true
	}
	if ev.ReplaceDevices {
		clear(e.devices)
	}

	seedWorld := (ev.Security == nil && len(ev.DirectReports) == 0 && !ev.ClearDirectReports && ev.ListError == "") ||
		len(ev.Machines) > 0 || len(ev.Sessions) > 0 ||
		len(ev.Devices) > 0 || len(ev.RawPrefs) > 0 || len(ev.SessionEvents) > 0 ||
		ev.ReplaceMachines || ev.ReplaceSessions || ev.ReplaceDevices ||
		len(ev.RemovedMachineIds) > 0 || len(ev.RemovedSessionIds) > 0 || ev.ClearListError
	if seedWorld {
		if !e.running || e.w.World.Connection != lists.ConnConnecting {
			e.coldStartGraceExpired = true
			e.clearColdStartGrace(ctx)
		}
		if e.demoListError != "" {
			e.w.World.MachinesListed = false
			e.w.World.SessionsListed = false
			e.w.SessionListError = world.PlainReason(e.demoListError)
			e.w.World.SessionListError = e.demoListError
		} else if !e.running || e.w.World.Connection != lists.ConnConnecting {
			e.w.World.MachinesListed = true
			e.w.World.SessionsListed = true
			e.w.World.LiveLoaded = true
			e.w.SessionListError = world.Reason{}
			e.w.World.SessionListError = ""
		}
		if ev.HasMoreSessions {
			e.w.World.SessionsNext = &lists.SessionPlace{EndedMs: 1, SessionID: "z"}
			e.w.World.HasMoreSessions = true
		} else if len(ev.Sessions) > 0 || ev.ReplaceSessions {
			e.w.World.SessionsNext = nil
			e.w.World.HasMoreSessions = false
		}

		for _, m := range ev.Machines {
			if m == nil || m.MachineId == "" {
				continue
			}
			var meta *v1.MachineMeta
			if len(m.RawMeta) > 0 {
				var parsed v1.MachineMeta
				if err := parsed.Unmarshal(m.RawMeta); err == nil {
					meta = &parsed
				}
			}
			e.putMachineDecoded(ctx, decodedMachine{
				MachineID:  m.MachineId,
				Online:     m.Online,
				LastSeenMs: m.LastSeenMs,
				Meta:       meta,
			}, false, false)
		}
		for _, s := range ev.Sessions {
			if s == nil || s.SessionId == "" {
				continue
			}
			var meta *v1.Session
			if len(s.RawMeta) > 0 {
				var parsed v1.Session
				if err := parsed.Unmarshal(s.RawMeta); err == nil {
					meta = &parsed
				}
			}
			var cp *v1.LiveSnapshot
			if len(s.RawCheckpoint) > 0 {
				var parsed v1.LiveSnapshot
				if err := parsed.Unmarshal(s.RawCheckpoint); err == nil {
					cp = &parsed
				}
			}
			e.listed[s.SessionId] = true
			e.putSessionDecoded(ctx, decodedSession{
				SessionID:     s.SessionId,
				MachineID:     s.MachineId,
				LastSeq:       s.LastSeq,
				UpdatedMs:     s.UpdatedMs,
				ListedEndedMs: s.ListedEndedMs,
				Meta:          meta,
				Checkpoint:    cp,
			}, false)
		}
		if len(ev.Devices) > 0 || ev.ReplaceDevices {
			if ev.ReplaceDevices {
				clear(e.devices)
			}
			seen := make(map[string]bool, len(ev.Devices))
			for _, d := range ev.Devices {
				if d == nil || d.DeviceId == "" {
					continue
				}
				seen[d.DeviceId] = true
				var meta *v1.DeviceMeta
				if len(d.RawMeta) > 0 {
					var parsed v1.DeviceMeta
					if err := parsed.Unmarshal(d.RawMeta); err == nil {
						meta = &parsed
					}
				}
				e.devices[d.DeviceId] = decodedToDeviceEntry(decodedDevice{
					DeviceID:   d.DeviceId,
					Kind:       d.Kind,
					CreatedMs:  d.CreatedMs,
					LastSeenMs: d.LastSeenMs,
					Meta:       meta,
					Registered: true,
				})
			}
			for id := range e.security.TrustedClients {
				if !seen[id] {
					if _, exists := e.devices[id]; !exists {
						e.devices[id] = decodedToDeviceEntry(decodedDevice{
							DeviceID:   id,
							Kind:       v1.DeviceKind_DEVICE_KIND_CLIENT,
							Registered: false,
						})
					}
				}
			}
		}
		if len(ev.RawPrefs) > 0 {
			var prefs v1.ProjectPrefs
			if err := prefs.Unmarshal(ev.RawPrefs); err == nil {
				rev := ev.PrefsRevision
				if rev == 0 {
					rev = max(e.prefs.Revision, 1)
				}
				e.applyAccountPrefsDecoded(&decodedPrefs{
					Revision: rev,
					Prefs:    &prefs,
				}, true)
			}
		}
		for _, se := range ev.SessionEvents {
			if se == nil || se.SessionId == "" {
				continue
			}
			if se.Retired {
				if e.demoSessionRetired == nil {
					e.demoSessionRetired = make(map[string]bool)
				}
				e.demoSessionRetired[se.SessionId] = true
				if sess := e.sessions[se.SessionId]; sess != nil {
					sess.RetiredHistory = true
				}
			}
			if se.LoadError != "" {
				if e.demoSessionLoadError == nil {
					e.demoSessionLoadError = make(map[string]string)
				}
				e.demoSessionLoadError[se.SessionId] = se.LoadError
			} else if se.Replace && e.demoSessionLoadError != nil {
				delete(e.demoSessionLoadError, se.SessionId)
			}
			b := e.buffers[se.SessionId]
			if b == nil || se.Replace {
				var agent v1.Agent
				if sess := e.sessions[se.SessionId]; sess != nil && sess.Info != nil {
					agent = sess.Info.Agent
				}
				b = newEventBuffer(se.SessionId, agent, e.monoMs)
				e.buffers[se.SessionId] = b
			}
			if se.Retired {
				b.retired = true
				b.loaded = true
				b.awaitingCache = false
				b.errorReason = world.Reason{Kind: world.ReasonHistoryKeysUnavailable}
				e.bumpRev(b)
				continue
			}
			if se.LoadError != "" {
				b.loaded = true
				b.awaitingCache = false
				b.errorReason = world.PlainReason(se.LoadError)
				b.fetchErrorReason = world.PlainReason(se.LoadError)
				e.bumpRev(b)
				continue
			}
			items := make([]decodedEventItem, 0, len(se.RawEvents))
			for _, raw := range se.RawEvents {
				if len(raw) == 0 {
					continue
				}
				var item v1.Event
				if err := item.Unmarshal(raw); err != nil || item.Seq == 0 {
					continue
				}
				if se.Unavailable {
					items = append(items, decodedEventItem{
						Seq:         item.Seq,
						Unavailable: true,
						ApproxBytes: 64,
					})
					continue
				}
				items = append(items, decodedEventItem{
					Seq:         item.Seq,
					Event:       &item,
					Verified:    true,
					ApproxBytes: 128,
				})
			}
			b.loaded = true
			b.awaitingCache = false
			e.ingestDecodedEvents(ctx, se.SessionId, b, items, false, false, true, false)
		}
	} else if e.demoListError != "" {
		e.w.World.MachinesListed = false
		e.w.World.SessionsListed = false
		e.w.SessionListError = world.PlainReason(e.demoListError)
		e.w.World.SessionListError = e.demoListError
	}
	if ev.Security != nil {
		e.pruneUntrustedCaches(ctx)
		ctx.Dirty.MarkSecurity()
	}
	e.updateLivePollers(ctx)
	ctx.Dirty.MarkAll()
	if seedWorld {
		ctx.Dirty.MarkSessionData()
	}
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

type secOpKind uint8

const (
	secOpSyncStates secOpKind = iota + 1
	secOpCoordSign
	secOpCoordAgree
	secOpDirBatch
	secOpStoredEvents
	secOpDirectEvents
	secOpSealCmd
	secOpOpenCmdOutcome
	secOpSealPrefs
	secOpAuthToken
	secOpAuthRetry
	secOpForwardAuth
	secOpPushAuth
	secOpScheduleCache
	secOpSessionImage
	secOpTerminalAuth
	secOpTerminalSeal
	secOpTerminalOpen
)

type dirBatchKind uint8

const (
	dirKindBootstrap dirBatchKind = iota + 1
	dirKindListsFetched
	dirKindSessionsPage
	dirKindSessionByID
	dirKindDevices
	dirKindStreamMachine
	dirKindStreamSession
	dirKindStreamPrefs
	dirKindStreamDevice
	dirKindPrefsLoad
	dirKindPrefsCASGet
	dirKindPrefsCASPut
)

type storedEventsKind uint8

const (
	storedEventsCache storedEventsKind = iota + 1
	storedEventsPage
	storedEventsStream
)

type dirSessIdx struct {
	keyIdx  int
	metaIdx int
}

type dirBatchMeta struct {
	kind           dirBatchKind
	refreshRun     uint64
	listingGen     uint64
	devGen         uint64
	legID          uint64
	sid            string
	headOpID       string
	evNext         *v1.SessionCursor
	mp, sp, rp, dp map[string]uint64
	wm             *v1.Machine
	ws             *v1.RemoteSession
	wd             *v1.Device
}

type pendingSecOp struct {
	kind           secOpKind
	opID           uint64
	dir            dirBatchMeta
	machines       []*v1.Machine
	sessions       []*v1.RemoteSession
	devices        []*v1.Device
	prefs          []*v1.AccountPrefsObject
	mIdx           []int
	sIdx           []dirSessIdx
	dIdx           []int
	pIdx           []int
	eventsKind     storedEventsKind
	stored         []*v1.StoredEvent
	lifeGen        uint64
	b              *EventBuffer
	agent          v1.Agent
	wasBackfilling bool
	rawCount       int
	firstSeq       uint64
	lastSeq        uint64
	more           bool
	sid            string
	machineID      string
	imageID        string
	rawEnvBytes    []byte
	fromCache      bool
	fromDirect     bool
	streamID       string
	termSeq        uint64
	termIsInput    bool
	termFrameBytes []byte
	termItem       *pendingTermSend
	termRawBytes   int
	pcID           uint64
	prep           *PreparedCommand
	cmdEv          *cmdOutcome
	headOpID       string
	baseRev        uint64
	nextPrefs      *v1.ProjectPrefs
	fwdLeg         *pendingForwardLeg
	push           *pendingPushRpc
}

func (e *Engine) allocSecOp(op *pendingSecOp) uint64 {
	e.nextSecOpTag++
	tag := e.nextSecOpTag
	e.secOps[tag] = op
	return tag
}

func (e *Engine) takeSecOp(tag uint64) *pendingSecOp {
	op := e.secOps[tag]
	if op != nil {
		delete(e.secOps, tag)
	}
	return op
}

// OnSecurityStateChanged implements security.AsyncSink.
func (e *Engine) OnSecurityStateChanged() {
	if stepCtx := e.activeStepCtx; stepCtx != nil {
		e.syncSecurityStateFromEngine(stepCtx, true)
	}
}

// OnSyncStatesDone implements security.AsyncSink.
func (e *Engine) OnSyncStatesDone(tag uint64, err error) {
	op := e.takeSecOp(tag)
	if op == nil {
		return
	}
	if e.activeSecOpID == op.opID {
		e.activeSecOpID = 0
	}
	stepCtx := e.activeStepCtx
	if stepCtx == nil {
		return
	}
	if err != nil {
		e.emitCoordEffects(stepCtx, e.ensureTransportCoord().OnAuthorizeDone(e.nowMs, false))
	} else {
		e.syncSecurityStateFromEngine(stepCtx, true)
	}
	e.maybeRequestEntropy(stepCtx)
}

// OnAuthTokenDone implements security.AsyncSink.
func (e *Engine) OnAuthTokenDone(tag uint64, res security.AuthTokenResult) {
	op := e.takeSecOp(tag)
	if op == nil {
		return
	}
	stepCtx := e.activeStepCtx
	if stepCtx == nil {
		return
	}
	switch op.kind {
	case secOpAuthToken:
		e.handleTokenResult(stepCtx, tokenResult{
			OpID:           op.opID,
			Token:          res.Token,
			ExpiresMs:      res.ExpiresMs,
			Removed:        res.Removed,
			UnresolvedHost: res.UnresolvedHost,
			ErrorCode:      res.ErrorCode,
			Error:          res.Error,
		})
	case secOpAuthRetry:
		e.finishAuthRetry(stepCtx, res)
	case secOpForwardAuth:
		e.finishForwardAuth(stepCtx, op.fwdLeg, res)
	case secOpPushAuth:
		e.finishPushAuth(stepCtx, op.push, res)
	case secOpTerminalAuth:
		e.finishTerminalAuth(stepCtx, op.streamID, res)
	}
	e.maybeRequestEntropy(stepCtx)
}

// OnKeySignDone implements security.AsyncSink.
func (e *Engine) OnKeySignDone(tag uint64, sig []byte, err error) {
	op := e.takeSecOp(tag)
	if op == nil || err != nil {
		return
	}
	e.emitCoordEffects(e.activeStepCtx, e.ensureTransportCoord().OnKeySigned(e.nowMs, op.opID, sig))
}

// OnKeyAgreeDone implements security.AsyncSink.
func (e *Engine) OnKeyAgreeDone(tag uint64, shared []byte, err error) {
	op := e.takeSecOp(tag)
	if op == nil {
		return
	}
	e.emitCoordEffects(e.activeStepCtx, e.ensureTransportCoord().OnKeyAgreed(e.nowMs, op.opID, shared, err == nil))
}

// OnOpenBatchDone implements security.AsyncSink.
func (e *Engine) OnOpenBatchDone(tag uint64, results []security.OpenResult) {
	op := e.takeSecOp(tag)
	if op == nil {
		return
	}
	switch op.kind {
	case secOpDirBatch:
		e.finishDirectoryBatch(e.activeStepCtx, op, results)
	case secOpStoredEvents:
		e.finishStoredEvents(e.activeStepCtx, op, results)
	case secOpDirectEvents:
		e.finishDirectEventsOpen(e.activeStepCtx, op, results)
	case secOpOpenCmdOutcome:
		e.finishOpenCmdOutcome(e.activeStepCtx, op, results)
	case secOpScheduleCache:
		e.finishScheduleCacheOpen(e.activeStepCtx, op, results)
	case secOpSessionImage:
		e.finishSessionImageOpen(e.activeStepCtx, op, results)
	case secOpTerminalOpen:
		e.finishTerminalOpen(e.activeStepCtx, op, results)
	}
}

// OpenScheduleCache verifies and decrypts a persisted signed schedule.list result envelope.
// Completion is delivered synchronously (inlineCrypto / <=16 KiB) or asynchronously (Web CryptoBatch)
// via StepContext.OpenedScheduleCaches.
func (e *Engine) OpenScheduleCache(ctx *StepContext, opID uint64, machineID, requestID string, env *v1.Envelope) {
	if ctx == nil {
		return
	}
	if e.secEngine == nil || machineID == "" || requestID == "" || env == nil {
		ctx.OpenedScheduleCaches = append(ctx.OpenedScheduleCaches, OpenedScheduleCache{
			OpID:      opID,
			MachineID: machineID,
			Err:       devicesec.ErrInvalid,
		})
		return
	}
	tag := e.allocSecOp(&pendingSecOp{
		kind:      secOpScheduleCache,
		opID:      opID,
		machineID: machineID,
	})
	effs := e.secEngine.OpenBatchTag(e.nowMs, []security.OpenItem{{
		Env:             env,
		ExpectedPurpose: devicesec.Result,
		MachineID:       machineID,
		RequestID:       requestID,
		History:         true,
	}}, tag)
	e.emitRawEffects(ctx, effs)
}

func (e *Engine) finishScheduleCacheOpen(stepCtx *StepContext, op *pendingSecOp, results []security.OpenResult) {
	if stepCtx == nil || op == nil {
		return
	}
	out := OpenedScheduleCache{
		OpID:      op.opID,
		MachineID: op.machineID,
	}
	if len(results) != 1 || results[0].Err != nil {
		if len(results) == 1 && results[0].Err != nil {
			out.Err = results[0].Err
		} else {
			out.Err = devicesec.ErrInvalid
		}
		stepCtx.OpenedScheduleCaches = append(stepCtx.OpenedScheduleCaches, out)
		return
	}
	res, ok := results[0].Body.(*v1.CommandResult)
	if !ok || res == nil || res.ErrorCode != "" || res.GetSchedule() == nil || len(res.GetSchedule().GetResultJson()) == 0 {
		out.Err = devicesec.ErrInvalid
		stepCtx.OpenedScheduleCaches = append(stepCtx.OpenedScheduleCaches, out)
		return
	}
	out.ResultJSON = slices.Clone(res.GetSchedule().GetResultJson())
	stepCtx.OpenedScheduleCaches = append(stepCtx.OpenedScheduleCaches, out)
}

func (e *Engine) openSessionImageEnvelope(
	ctx *StepContext,
	opID uint64,
	sid, imageID, machineID string,
	env *v1.Envelope,
	rawEnvBytes []byte,
	fromCache bool,
) {
	if ctx == nil || e.secEngine == nil || env == nil {
		return
	}
	repls := e.sessionReplacements[sid]
	if len(repls) == 0 {
		if sess := e.sessions[sid]; sess != nil && sess.Info != nil {
			repls = sess.Info.MachineReplacements
		}
	}
	tag := e.allocSecOp(&pendingSecOp{
		kind:        secOpSessionImage,
		opID:        opID,
		sid:         sid,
		machineID:   machineID,
		imageID:     imageID,
		rawEnvBytes: rawEnvBytes,
		fromCache:   fromCache,
	})
	effs := e.secEngine.OpenBatchTag(e.nowMs, []security.OpenItem{{
		Env:             env,
		ExpectedPurpose: devicesec.Image,
		MachineID:       machineID,
		SessionID:       sid,
		RequestID:       imageID,
		History:         true,
		Replacements:    repls,
	}}, tag)
	e.emitRawEffects(ctx, effs)
}

func (e *Engine) finishSessionImageOpen(stepCtx *StepContext, op *pendingSecOp, results []security.OpenResult) {
	if stepCtx == nil || op == nil {
		return
	}
	if len(results) != 1 || results[0].Err != nil {
		var err error
		if len(results) == 1 {
			err = results[0].Err
		}
		retired := errors.Is(err, security.ErrUnsupportedHistory)
		e.emitSessionImageLoaded(stepCtx, &corev1.SessionImageLoadedEffect{
			OpId:           op.opID,
			SessionId:      op.sid,
			ImageId:        op.imageID,
			Status:         corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_BAD,
			ErrorCode:      sessionImageOpenErrorCode(err),
			RetiredHistory: retired,
		})
		return
	}
	payload, ok := results[0].Body.(*v1.ImagePayload)
	if !ok || payload == nil || !matchesImageFormat(payload.Data, payload.Format) {
		e.emitSessionImageLoaded(stepCtx, &corev1.SessionImageLoadedEffect{
			OpId:      op.opID,
			SessionId: op.sid,
			ImageId:   op.imageID,
			Status:    corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_BAD,
			ErrorCode: "format_mismatch",
		})
		return
	}
	var cacheBytes []byte
	if !op.fromCache && len(op.rawEnvBytes) > 0 && e.CanUseMachineCache(op.machineID) {
		cacheBytes = op.rawEnvBytes
	}
	var w, h uint32
	if payload.Width > 0 {
		w = uint32(payload.Width)
	}
	if payload.Height > 0 {
		h = uint32(payload.Height)
	}
	e.emitSessionImageLoaded(stepCtx, &corev1.SessionImageLoadedEffect{
		OpId:               op.opID,
		SessionId:          op.sid,
		ImageId:            op.imageID,
		Status:             corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_OK,
		Data:               payload.Data,
		Width:              w,
		Height:             h,
		Format:             payload.Format,
		CacheEnvelopeBytes: cacheBytes,
	})
}

func matchesImageFormat(data []byte, f v1.ImageFormat) bool {
	detected := imageparse.SniffFormat(data)
	switch f {
	case v1.ImageFormat_IMAGE_FORMAT_PNG:
		return detected == imageparse.FormatPNG
	case v1.ImageFormat_IMAGE_FORMAT_JPEG:
		return detected == imageparse.FormatJPEG
	case v1.ImageFormat_IMAGE_FORMAT_WEBP:
		return detected == imageparse.FormatWebP
	case v1.ImageFormat_IMAGE_FORMAT_GIF:
		return detected == imageparse.FormatGIF
	default:
		return false
	}
}

func sessionImageOpenErrorCode(err error) string {
	switch security.StructuredErrorCode(err) {
	case corev1.SecurityErrorCode_SECURITY_ERROR_CODE_UNSUPPORTED_HISTORY:
		return "retired_history"
	case corev1.SecurityErrorCode_SECURITY_ERROR_CODE_LEASE_EXPIRED:
		return "lease"
	case corev1.SecurityErrorCode_SECURITY_ERROR_CODE_RECOVERY_UNAVAILABLE,
		corev1.SecurityErrorCode_SECURITY_ERROR_CODE_STALE:
		return "unavailable"
	case corev1.SecurityErrorCode_SECURITY_ERROR_CODE_UNTRUSTED:
		return "untrusted"
	default:
		return "invalid"
	}
}

// OnSealEnvelopeDone implements security.AsyncSink.
func (e *Engine) OnSealEnvelopeDone(tag uint64, sr security.SealResult, _ error) {
	op := e.takeSecOp(tag)
	if op == nil {
		return
	}
	switch op.kind {
	case secOpSealCmd:
		e.finishSealCmd(e.activeStepCtx, op, sr)
	case secOpSealPrefs:
		e.finishSealPrefs(e.activeStepCtx, op, sr)
	case secOpTerminalSeal:
		e.finishTerminalSeal(e.activeStepCtx, op, sr)
	}
}

func (e *Engine) openWireDirectoryBatchTag(
	ctx *StepContext,
	machines []*v1.Machine,
	sessions []*v1.RemoteSession,
	devices []*v1.Device,
	prefs []*v1.AccountPrefsObject,
	meta dirBatchMeta,
) {
	var items []security.OpenItem
	mIdx := make([]int, len(machines))
	for i, m := range machines {
		mIdx[i] = -1
		if m == nil || m.Id == "" || m.Meta == nil {
			continue
		}
		mIdx[i] = len(items)
		items = append(items, security.OpenItem{
			Env: m.Meta, ExpectedPurpose: devicesec.MachineMeta, MachineID: m.Id, History: true,
		})
	}
	sIdx := make([]dirSessIdx, len(sessions))
	for i, rs := range sessions {
		sIdx[i] = dirSessIdx{keyIdx: -1, metaIdx: -1}
		if rs == nil || rs.SessionId == "" {
			continue
		}
		if rs.Key != nil {
			sIdx[i].keyIdx = len(items)
			items = append(items, security.OpenItem{
				Env: rs.Key, ExpectedPurpose: devicesec.SessionKey, MachineID: rs.MachineId, SessionID: rs.SessionId, History: true,
			})
		}
		if rs.Meta != nil {
			sIdx[i].metaIdx = len(items)
			items = append(items, security.OpenItem{
				Env: rs.Meta, ExpectedPurpose: devicesec.SessionMeta, MachineID: rs.MachineId, SessionID: rs.SessionId, History: true,
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
			Env: d.Meta, ExpectedPurpose: devicesec.DeviceMeta, RequestID: d.Id, History: true,
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
			Env: p.Envelope, ExpectedPurpose: devicesec.Prefs, RequestID: reqID, Seq: p.Revision, History: true,
		})
	}
	op := &pendingSecOp{
		kind: secOpDirBatch, dir: meta, machines: machines, sessions: sessions, devices: devices, prefs: prefs,
		mIdx: mIdx, sIdx: sIdx, dIdx: dIdx, pIdx: pIdx,
	}
	if len(items) == 0 {
		e.finishDirectoryBatch(ctx, op, nil)
		return
	}
	tag := e.allocSecOp(op)
	effs := e.secEngine.OpenBatchTag(e.nowMs, items, tag)
	e.emitRawEffects(ctx, effs)
}

func (e *Engine) writePrefsCache(stepCtx *StepContext, wire *v1.AccountPrefsObject) {
	if wire != nil && wire.Revision > 0 {
		if raw, err := wire.Marshal(); err == nil && len(raw) > 0 {
			stepCtx.emit(&corev1.Effect{
				Effect: &corev1.Effect_CacheWrite{
					CacheWrite: &corev1.CacheWriteEffect{Kind: &corev1.CacheWriteEffect_RawWritePrefs{RawWritePrefs: raw}},
				},
			})
		}
	}
}

func (e *Engine) finishDirectoryBatch(stepCtx *StepContext, op *pendingSecOp, results []security.OpenResult) {
	if stepCtx == nil {
		stepCtx = e.activeStepCtx
	}
	outM := make([]decodedMachine, 0, len(op.machines))
	for i, m := range op.machines {
		if m == nil || m.Id == "" {
			continue
		}
		dm := decodedMachine{MachineID: m.Id, Online: m.Online, LastSeenMs: m.LastSeenMs, Wire: m}
		if idx := op.mIdx[i]; idx >= 0 {
			r := results[idx]
			if r.Err != nil {
				dm.MetaBroken = true
			} else if mm, ok := r.Body.(*v1.MachineMeta); ok {
				dm.Meta, dm.MetaRev = mm, r.Header.Seq
			}
		}
		outM = append(outM, dm)
	}
	outS := make([]decodedSession, 0, len(op.sessions))
	for i, rs := range op.sessions {
		if rs == nil || rs.SessionId == "" {
			continue
		}
		ds := decodedSession{
			SessionID: rs.SessionId, MachineID: rs.MachineId, LastSeq: rs.LastSeq,
			UpdatedMs: rs.UpdatedMs, ListedEndedMs: rs.EndedMs, Wire: rs,
		}
		var keyErr error
		if idx := op.sIdx[i].keyIdx; idx >= 0 {
			keyErr = results[idx].Err
		}
		if keyErr != nil {
			if errors.Is(keyErr, security.ErrUnsupportedHistory) {
				ds.RetiredHistory = true
			} else {
				ds.OpenFailed = true
			}
		} else if idx := op.sIdx[i].metaIdx; idx >= 0 {
			r := results[idx]
			if r.Err != nil {
				if errors.Is(r.Err, security.ErrUnsupportedHistory) {
					ds.RetiredHistory = true
				} else {
					ds.OpenFailed = true
				}
			} else if meta, ok := r.Body.(*v1.Session); ok {
				ds.Meta, ds.MetaRev = meta, r.Header.Seq
				if len(meta.MachineReplacements) > 0 {
					if e.sessionReplacements == nil {
						e.sessionReplacements = make(map[string][]*v1.MachineReplacementProof)
					}
					e.sessionReplacements[rs.SessionId] = append([]*v1.MachineReplacementProof(nil), meta.MachineReplacements...)
					if e.secEngine != nil && rs.MachineId != "" {
						for oldID := range e.secEngine.VerifiedMachineReplacementOrigins(rs.MachineId, meta.MachineReplacements) {
							if oldID != "" && oldID != rs.MachineId {
								if e.machineReplacements == nil {
									e.machineReplacements = make(map[string]string)
								}
								e.machineReplacements[oldID] = rs.MachineId
							}
						}
					}
				}
				ds.Checkpoint, ds.CheckpointInvalid = validateSessionCheckpoint(rs.SessionId, meta)
			}
		}
		outS = append(outS, ds)
	}
	outD := make([]decodedDevice, 0, len(op.devices))
	for i, d := range op.devices {
		if d == nil || d.Id == "" {
			continue
		}
		dd := decodedDevice{DeviceID: d.Id, Kind: d.Kind, CreatedMs: d.CreatedMs, LastSeenMs: d.LastSeenMs, Registered: true}
		if idx := op.dIdx[i]; idx >= 0 {
			r := results[idx]
			if r.Err != nil {
				if !errors.Is(r.Err, security.ErrRecoveryHistoryUnavailable) {
					dd.MetaBroken = true
				}
			} else if dm, ok := r.Body.(*v1.DeviceMeta); ok {
				dd.Meta, dd.MetaRev = dm, r.Header.Seq
			}
		}
		outD = append(outD, dd)
	}
	outP := make([]*decodedPrefs, len(op.prefs))
	for i, p := range op.prefs {
		if p == nil || p.Envelope == nil || p.Revision == 0 {
			outP[i] = &decodedPrefs{Revision: 0, Prefs: &v1.ProjectPrefs{Schema: 1}, Wire: p}
			continue
		}
		dp := &decodedPrefs{Revision: p.Revision, Wire: p}
		if idx := op.pIdx[i]; idx >= 0 {
			r := results[idx]
			if r.Err != nil {
				if errors.Is(r.Err, security.ErrRecoveryHistoryUnavailable) {
					dp.Prefs = &v1.ProjectPrefs{Schema: 1}
				} else {
					dp.Broken = true
				}
			} else if pp, ok := r.Body.(*v1.ProjectPrefs); ok && r.Header.Seq == p.Revision {
				dp.Prefs = pp
			} else {
				dp.Broken = true
			}
		}
		outP[i] = dp
	}
	m := op.dir
	switch m.kind {
	case dirKindBootstrap:
		if !e.w.World.MachinesListed {
			for _, dm := range outM {
				if dm.MachineID != "" {
					if _, exists := e.machines[dm.MachineID]; !exists {
						dm.Online = false
						e.storeMachine(decodedToMachineEntry(dm))
					}
				}
			}
		}
		if !e.w.World.SessionsListed {
			for _, ds := range outS {
				if ds.SessionID != "" {
					if _, exists := e.sessions[ds.SessionID]; !exists {
						e.storeSession(decodedToSessionEntry(ds, nil))
						e.listed[ds.SessionID] = true
					}
				}
			}
		}
		if !e.prefs.Loaded && len(outP) > 0 && outP[0] != nil {
			e.applyAccountPrefsDecoded(outP[0], false)
		}
		e.emitWatchedSessionsIfChanged(stepCtx)
		e.pruneUntrustedCaches(stepCtx)
		e.updateLivePollers(stepCtx)
		stepCtx.Dirty.MarkAll()
	case dirKindListsFetched:
		if !e.running || e.refreshRun != m.refreshRun {
			return
		}
		e.devicesApplyGen++
		e.applyListsDecoded(stepCtx, outM, outS, e.buildAuthoritativeDevices(outD), m.evNext, m.mp, m.sp, m.rp, m.dp)
	case dirKindSessionsPage:
		if !e.running || e.listingGen != m.listingGen || e.pageListingGen != e.listingGen {
			return
		}
		e.w.SessionListError = world.Reason{}
		e.w.World.SessionListError = ""
		for _, s := range outS {
			if s.SessionID != "" {
				e.putSessionDecoded(stepCtx, s, false)
			}
		}
		if m.evNext != nil && (m.evNext.EndedMs != 0 || m.evNext.SessionId != "") {
			e.w.World.SessionsNext = &lists.SessionPlace{EndedMs: m.evNext.EndedMs, SessionID: m.evNext.SessionId}
			e.w.World.HasMoreSessions = true
		} else {
			e.w.World.SessionsNext = nil
			e.w.World.HasMoreSessions = false
		}
		e.updateLivePollers(stepCtx)
		e.syncDirectWatched(stepCtx)
		if e.wantAllSessions && e.w.World.SessionsNext != nil {
			e.RequestMoreSessions(stepCtx)
		}
		stepCtx.Dirty.Add(true, true, false)
	case dirKindSessionByID:
		if !e.running {
			return
		}
		if len(outS) == 0 || outS[0].OpenFailed {
			if m.sid != "" {
				e.sessionByIDFailed[m.sid] = true
				e.sessionByIDErrors[m.sid] = world.Reason{Kind: world.ReasonInvalidCiphertext}
			}
			stepCtx.Dirty.Add(false, true, false)
			stepCtx.Dirty.MarkSessionData()
			return
		}
		delete(e.sessionByIDFailed, m.sid)
		delete(e.sessionByIDErrors, m.sid)
		e.putSessionDecoded(stepCtx, outS[0], false)
		e.emitWatchedSessionsIfChanged(stepCtx)
		e.updateLivePollers(stepCtx)
		stepCtx.Dirty.MarkAll()
	case dirKindDevices:
		if e.devicesApplyGen != m.devGen {
			return
		}
		e.devicesLoadFailed = false
		e.devicesError = world.Reason{}
		e.reconcileDecodedDevices(e.buildAuthoritativeDevices(outD), m.mp, m.sp, true)
		e.pruneUntrustedCaches(stepCtx)
		e.maybeDescribeSelf(stepCtx)
		stepCtx.Dirty.MarkAll()
		stepCtx.Dirty.MarkSecurity()
	case dirKindStreamMachine:
		if !e.running || e.legID != m.legID || len(outM) == 0 {
			return
		}
		e.putMachineDecoded(stepCtx, outM[0], false, true)
		if m.wm.Online && e.security.Loaded && !e.isTrustedMachine(m.wm.Id) {
			e.requestSyncSecurity(stepCtx, true)
		}
		e.updateLivePollers(stepCtx)
		stepCtx.Dirty.MarkAll()
	case dirKindStreamSession:
		if !e.running || e.legID != m.legID || len(outS) == 0 {
			return
		}
		prev := e.sessions[m.sid]
		var prevServerSeq uint64
		if prev != nil {
			prevServerSeq = prev.ServerLastSeq
		}
		e.putSessionDecoded(stepCtx, outS[0], true)
		e.emitWatchedSessionsIfChanged(stepCtx)
		updated := e.sessions[m.sid]
		b := e.buffers[m.sid]
		needGapFill := b != nil && len(b.pending) > 0 && updated != nil && updated.ServerLastSeq > uint64(len(b.prefix))
		if m.ws.LastSeq > prevServerSeq || needGapFill {
			if e.watchedSessions[m.sid] || b != nil {
				e.startFetchEvents(stepCtx, m.sid)
			}
		}
		e.updateLivePollers(stepCtx)
		stepCtx.Dirty.MarkAll()
	case dirKindStreamPrefs:
		if !e.running || e.legID != m.legID || len(outP) == 0 || outP[0] == nil {
			return
		}
		e.applyAccountPrefsDecoded(outP[0], true)
		e.writePrefsCache(stepCtx, outP[0].Wire)
		stepCtx.Dirty.MarkAll()
	case dirKindStreamDevice:
		if !e.running || e.legID != m.legID || len(outD) == 0 {
			return
		}
		e.devices[m.wd.Id] = decodedToDeviceEntry(outD[0])
		if e.secEngine != nil {
			e.secEngine.SetDeviceDirectory(e.securityDeviceOffers())
		}
		if e.deviceKindNeedsSecuritySync(m.wd.Id, m.wd.Kind) {
			e.requestSyncSecurity(stepCtx, true)
		}
		e.pruneUntrustedCaches(stepCtx)
		e.maybeDescribeSelf(stepCtx)
		stepCtx.Dirty.Add(true, false, false)
		stepCtx.Dirty.MarkSecurity()
	case dirKindPrefsLoad:
		var dp *decodedPrefs
		if len(outP) > 0 {
			dp = outP[0]
		}
		e.applyAccountPrefsDecoded(dp, true)
		if dp != nil {
			e.writePrefsCache(stepCtx, dp.Wire)
		}
		stepCtx.Dirty.MarkAll()
	case dirKindPrefsCASGet:
		if len(e.prefsQueue) == 0 || e.prefsQueue[0].OpID != m.headOpID {
			return
		}
		curHead := &e.prefsQueue[0]
		var dp *decodedPrefs
		if len(outP) > 0 {
			dp = outP[0]
		}
		if dp != nil && dp.Broken {
			e.applyAccountPrefsDecoded(dp, true)
			e.finishPrefsOp(stepCtx, world.Reason{Kind: world.ReasonInvalidCiphertext})
			return
		}
		var baseRev uint64
		var basePrefs *v1.ProjectPrefs
		if dp != nil {
			baseRev = dp.Revision
			basePrefs = dp.Prefs
		}
		if curHead.Attempt == 0 {
			curHead.InitialBase = baseRev
		} else if baseRev > curHead.InitialBase {
			e.w.ProjectChangedElsewhere = true
		}
		if basePrefs != nil && slices.Contains(basePrefs.RecentOps, curHead.OpID) {
			e.applyAccountPrefsDecoded(dp, true)
			if dp != nil {
				e.writePrefsCache(stepCtx, dp.Wire)
			}
			e.finishPrefsOp(stepCtx, world.Reason{})
			return
		}
		nextPrefs := lists.ApplyPrefsOp(basePrefs, curHead.Op, curHead.OpID, e.nowMs)
		e.sealAndEmitPrefsPut(stepCtx, curHead, baseRev, nextPrefs)
	case dirKindPrefsCASPut:
		if len(outP) > 0 && outP[0] != nil {
			e.applyAccountPrefsDecoded(outP[0], true)
			e.writePrefsCache(stepCtx, outP[0].Wire)
		}
		e.finishPrefsOp(stepCtx, world.Reason{})
	}
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
	e.w.SessionListError = world.Reason{}
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

	e.pruneUntrustedCaches(ctx)
	e.maybeDescribeSelf(ctx)
	e.updateLivePollers(ctx)
	e.syncDirectWatched(ctx)
	if e.wantAllSessions && e.w.World.SessionsNext != nil {
		e.RequestMoreSessions(ctx)
	}
	ctx.Dirty.MarkAll()
}

func (e *Engine) openWireStoredEventsTag(
	ctx *StepContext,
	sid, machineID string,
	stored []*v1.StoredEvent,
	history bool,
	op *pendingSecOp,
) {
	if len(stored) == 0 {
		e.finishStoredEvents(ctx, op, nil)
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
			env, seq = se.Envelope, se.Seq
		}
		items[i] = security.OpenItem{
			Env: env, ExpectedPurpose: devicesec.Event, MachineID: machineID, SessionID: sid,
			Seq: seq, History: history, Replacements: repls,
		}
	}
	tag := e.allocSecOp(op)
	effs := e.secEngine.OpenBatchTag(e.nowMs, items, tag)
	e.emitRawEffects(ctx, effs)
}

func (e *Engine) finishStoredEvents(stepCtx *StepContext, op *pendingSecOp, results []security.OpenResult) {
	if stepCtx == nil {
		stepCtx = e.activeStepCtx
	}
	var items []decodedEventItem
	if len(op.stored) > 0 {
		items = make([]decodedEventItem, 0, len(op.stored))
		for i, se := range op.stored {
			if se == nil || se.Seq == 0 {
				continue
			}
			r := results[i]
			if r.Err != nil {
				if errors.Is(r.Err, security.ErrRecoveryHistoryUnavailable) {
					items = append(items, decodedEventItem{Seq: se.Seq, Unavailable: true, ApproxBytes: 64, RawStored: se})
					continue
				}
				items = append(items, decodedEventItem{Seq: se.Seq, Broken: true, Retired: errors.Is(r.Err, security.ErrUnsupportedHistory)})
				break
			}
			evMsg, ok := r.Body.(*v1.Event)
			if !ok || evMsg == nil || evMsg.Seq != se.Seq || evMsg.SessionId != op.sid {
				items = append(items, decodedEventItem{Seq: se.Seq, Broken: true})
				break
			}
			items = append(items, decodedEventItem{Seq: se.Seq, Event: evMsg, Verified: true, ApproxBytes: max(int64(len(r.Plain)), 64), RawStored: se})
		}
	}
	switch op.eventsKind {
	case storedEventsCache:
		if e.lifecycleGen != op.lifeGen || e.buffers[op.sid] != op.b {
			return
		}
		e.finishCacheEventsLoaded(stepCtx, op.sid, op.b, e.sessions[op.sid], op.agent, op.wasBackfilling, op.rawCount, op.firstSeq, op.lastSeq, items)
	case storedEventsPage:
		if !e.running || op.b.fetchGen != op.lifeGen || e.buffers[op.sid] != op.b {
			return
		}
		curS := e.sessions[op.sid]
		if curS == nil {
			op.b.fetching = false
			return
		}
		e.finishEventsPageFetched(stepCtx, op.sid, op.b, curS, op.more, op.lastSeq, items)
	case storedEventsStream:
		if !e.running || e.lifecycleGen != op.lifeGen || e.buffers[op.sid] != op.b {
			return
		}
		e.ingestDecodedEvents(stepCtx, op.sid, op.b, items, true, true, true, true)
		stepCtx.Dirty.MarkSessionData()
	}
}

func (e *Engine) finishDirectEventsOpen(stepCtx *StepContext, op *pendingSecOp, results []security.OpenResult) {
	if !e.running || !e.foreground || e.buffers[op.sid] != op.b || op.b.tailRequested || e.transportCoord == nil {
		return
	}
	curRoundID, curMachineID, curUse, curOK := e.transportCoord.InboundDCContext(e.nowMs, op.pcID)
	if !curOK || curMachineID != op.machineID {
		return
	}
	expected := uint64(len(op.b.prefix)) + 1
	var clean []decodedEventItem
	for _, r := range results {
		if r.Err != nil {
			continue
		}
		if !e.transportCoord.PushedEventAllowed(e.nowMs, curRoundID, curUse, op.machineID, r.Header.Sender) {
			continue
		}
		evMsg, ok := r.Body.(*v1.Event)
		if !ok || evMsg == nil || evMsg.Seq == 0 || evMsg.Seq != expected || evMsg.SessionId != op.sid {
			continue
		}
		clean = append(clean, decodedEventItem{Seq: evMsg.Seq, Event: evMsg, Verified: true, ApproxBytes: max(int64(len(r.Plain)), 64)})
		expected++
	}
	if len(clean) == 0 {
		return
	}
	e.ingestDecodedEvents(stepCtx, op.sid, op.b, clean, false, false, false, true)
	stepCtx.Dirty.MarkSessionData()
}

func (e *Engine) sealAndEmitCommandAttempt(ctx *StepContext, prep *PreparedCommand, retryOfUnknown bool) {
	if prep.Sealing {
		return
	}
	prep.Sealing = true
	prep.SealingRetry = retryOfUnknown
	tag := e.allocSecOp(&pendingSecOp{kind: secOpSealCmd, prep: prep})
	effs := e.secEngine.SealEnvelopeTag(
		e.nowMs, devicesec.Command, prep.MachineID, prep.SessionID, prep.RequestID,
		0, uint64(max(0, prep.DeadlineMs)), prep.CommandBytes, false, nil, tag,
	)
	e.emitRawEffects(ctx, effs)
}

func (e *Engine) finishSealCmd(stepCtx *StepContext, op *pendingSecOp, sr security.SealResult) {
	prep := op.prep
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
		var errReason world.Reason
		switch {
		case errors.Is(sr.Err, security.ErrClockSkewed), errors.Is(sr.Err, devicesec.ErrClock):
			errReason = world.Reason{Kind: world.ReasonClockBehind}
		case errors.Is(sr.Err, security.ErrLeaseExpired), errors.Is(sr.Err, devicesec.ErrStale), errors.Is(sr.Err, security.ErrNotMember):
			if prior {
				errReason = world.Reason{Kind: world.ReasonReplyUnverified}
			} else {
				errReason = world.Reason{Kind: world.ReasonRemoteControlExpired}
			}
		default:
			errReason = world.PlainReason(sr.Err.Error())
		}
		e.settleCommand(stepCtx, prep, cat, nil, errCode, errReason, false)
		return
	}
	prep.SealedEnvelope = sr.Envelope
	prep.SealedHeadHash = sr.HeadHash
	prep.RequestKeyHash = sr.RequestKeyHash
	prep.PayloadHash = sr.PayloadHash
	e.emitCommandAttempt(stepCtx, prep, prep.SealingRetry)
}

func (e *Engine) openSealedCommandOutcome(ctx *StepContext, prep *PreparedCommand, ev *cmdOutcome) {
	evCopy := cloneCmdOutcome(ev)
	item := security.OpenItem{
		Env: ev.SealedResult, ExpectedPurpose: devicesec.Result,
		MachineID: prep.MachineID, SessionID: prep.SessionID, RequestID: prep.RequestID, History: false,
	}
	tag := e.allocSecOp(&pendingSecOp{kind: secOpOpenCmdOutcome, prep: prep, cmdEv: &evCopy})
	effs := e.secEngine.OpenBatchTag(e.nowMs, []security.OpenItem{item}, tag)
	e.emitRawEffects(ctx, effs)
}

func (e *Engine) finishOpenCmdOutcome(stepCtx *StepContext, op *pendingSecOp, results []security.OpenResult) {
	prep, evCopy := op.prep, op.cmdEv
	if e.inFlightCmds[prep.RequestID] != prep {
		return
	}
	if evCopy.AttemptId != "" && !prep.PendingAttempts[evCopy.AttemptId] {
		return
	}
	sealedEnv := evCopy.SealedResult
	evCopy.SealedResult = nil
	r := results[0]
	if r.Err != nil {
		evCopy.Signed, evCopy.HashesMatch, evCopy.Result = false, false, nil
		e.finishCommandOutcome(stepCtx, evCopy)
		return
	}
	res, ok := r.Body.(*v1.CommandResult)
	if !ok || res == nil {
		evCopy.Signed, evCopy.HashesMatch, evCopy.Result = false, false, nil
		e.finishCommandOutcome(stepCtx, evCopy)
		return
	}
	hashesMatch := len(prep.RequestKeyHash) == 32 && len(prep.PayloadHash) == 32 &&
		bytes.Equal(res.RequestKeyHash, prep.RequestKeyHash) &&
		bytes.Equal(res.PayloadHash, prep.PayloadHash) &&
		(res.Outcome == "conflict" || res.ConflictKind == "")
	outerMatchesInner := evCopy.RouteWasDirect || (evCopy.OuterOutcome == res.Outcome && evCopy.OuterReason == res.Reason)
	if hashesMatch && outerMatchesInner {
		prep.VerifiedSealedResult = sealedEnv
	}
	evCopy.Signed, evCopy.HashesMatch, evCopy.OuterMatchesInner, evCopy.Result = true, hashesMatch, outerMatchesInner, res
	if evCopy.ObservedBytes == 0 {
		if pic := prep.Command.GetPutImageChunk(); pic != nil {
			evCopy.ObservedBytes = int64(len(pic.Data))
		} else if fc := res.GetGetSharedFileChunk(); fc != nil {
			evCopy.ObservedBytes = int64(len(fc.Data))
		}
	}
	if evCopy.RouteWasDirect {
		evCopy.OuterOutcome, evCopy.OuterReason = res.Outcome, res.Reason
	}
	e.finishCommandOutcome(stepCtx, evCopy)
}

func (e *Engine) sealAndEmitPrefsPut(ctx *StepContext, head *prefsQueueItem, baseRev uint64, nextPrefs *v1.ProjectPrefs) {
	if nextPrefs == nil {
		nextPrefs = &v1.ProjectPrefs{Schema: 1}
	} else {
		nextPrefs.Schema = 1
	}
	raw, err := nextPrefs.Marshal()
	if err != nil {
		e.finishPrefsOp(ctx, world.PlainReason(err.Error()))
		return
	}
	tag := e.allocSecOp(&pendingSecOp{
		kind: secOpSealPrefs, headOpID: head.OpID, baseRev: baseRev, nextPrefs: nextPrefs,
	})
	effs := e.secEngine.SealEnvelopeTag(
		e.nowMs, devicesec.Prefs, "", "", ProjectPrefsObjectID, baseRev+1, 0, raw, false, nil, tag,
	)
	e.emitRawEffects(ctx, effs)
}

func (e *Engine) finishSealPrefs(stepCtx *StepContext, op *pendingSecOp, sr security.SealResult) {
	if len(e.prefsQueue) == 0 || e.prefsQueue[0].OpID != op.headOpID {
		return
	}
	if sr.Err != nil {
		if errors.Is(sr.Err, security.ErrNeedEntropy) {
			e.maybeRequestEntropy(stepCtx)
		}
		e.finishPrefsOp(stepCtx, world.PlainReason(sr.Err.Error()))
		return
	}
	curHead := &e.prefsQueue[0]
	curHead.Phase = prefsPhasePut
	curHead.ActiveOpID = e.allocOpID()
	e.emitRelayRpc(
		stepCtx,
		corev1.RelayRpcMethod_RELAY_RPC_METHOD_PUT_ACCOUNT_PREFS,
		e.cachedToken,
		relayRpcDefaultTimeoutMs,
		"",
		(&v1.PutAccountPrefsRequest{
			ObjectId:         ProjectPrefsObjectID,
			ExpectedRevision: op.baseRev,
			Envelope:         sr.Envelope,
		}).AppendMarshal(nil),
		&pendingRelayRpc{kind: relayRpcPrefsPut, opID: curHead.ActiveOpID},
	)
}

func (e *Engine) cloneRefreshSnapshots() (mp, sp, rp, dp map[string]uint64) {
	return maps.Clone(e.refreshSnapshotM), maps.Clone(e.refreshSnapshotS), maps.Clone(e.refreshSnapshotR), maps.Clone(e.refreshSnapshotD)
}

// SecurityEngine returns the engine's embedded security.Engine.
func (e *Engine) SecurityEngine() *security.Engine {
	return e.secEngine
}

// SetSecurityPollObserver registers an observer for managed TIMER_KIND_SECURITY_POLL updates.
func (e *Engine) SetSecurityPollObserver(obs security.PollObserver) {
	if e != nil && e.secEngine != nil {
		e.secEngine.SetPollObserver(obs)
	}
}

// EmitSecurityEffects routes security effects through sync's timer/token/meta pipeline.
func (e *Engine) EmitSecurityEffects(ctx *StepContext, effs []*corev1.Effect) {
	e.emitSecurityEffects(ctx, effs)
	e.maybeRequestEntropy(ctx)
}

// SecurityDeviceOffers returns verified client device metadata from the sync directory.
func (e *Engine) SecurityDeviceOffers() []security.DeviceOfferInfo {
	return e.securityDeviceOffers()
}

// RefreshDevices triggers a device directory refresh when sync is running.
func (e *Engine) RefreshDevices(ctx *StepContext, force bool) {
	if e != nil && e.running && !e.removed {
		e.startRefreshDevices(ctx, force)
	}
}

// SyncRemoved reports whether sync has marked the current device removed.
func (e *Engine) SyncRemoved() bool {
	return e != nil && e.removed
}

// SetPlatformIdentity records the shell's platform and device description metadata.
func (e *Engine) SetPlatformIdentity(platform, description string) {
	if e == nil {
		return
	}
	if platform != "" {
		e.selfPlatform = platform
	}
	if description != "" {
		e.selfDescription = description
	}
}

// SetInitialNetworkState seeds network availability and signature prior to starting sync.
func (e *Engine) SetInitialNetworkState(available bool, signature string) {
	if e == nil {
		return
	}
	e.networkAvailable = available
	if signature != "" {
		e.networkSignature = signature
	}
}

// InvalidateAccountAuthForExit marks the outgoing account as exiting, drops cached tokens and
// pending 401-retry queues, and advances security.Engine's accountGen so late login RPCs on the
// P2-5 relay pipe or legacy security RPC pipe are silently discarded without emitting token updates.
func (e *Engine) InvalidateAccountAuthForExit(ctx *StepContext) {
	if e == nil {
		return
	}
	e.exitingAccount = true
	e.cachedToken = ""
	e.tokenExpiresMs = 0
	if e.activeTokenOpID != 0 {
		e.activeTokenOpID = 0
		e.connecting = false
	}
	prevCtx := e.activeStepCtx
	e.activeStepCtx = ctx
	defer func() { e.activeStepCtx = prevCtx }()

	queue := e.authRetryQueue
	e.authRetryInFlight = false
	e.authRetryQueue = nil
	canceledEv := &corev1.RelayRpcResultEvent{
		ConnectCode:       security.ConnectCodeCanceled,
		DiagnosticMessage: "canceled",
	}
	for _, item := range queue {
		if item != nil && item.op != nil {
			e.dispatchRelayRpcResult(ctx, item.op, canceledEv)
		}
	}
	if e.secEngine != nil {
		e.emitSecurityEffects(ctx, e.secEngine.InvalidateAccountAuth())
	}
}

// ResumeAccountAuthAfterFailedExit clears the exitingAccount guard after ClearSecurity fails
// and the user stays on the current account, allowing subsequent token/auth results to proceed.
func (e *Engine) ResumeAccountAuthAfterFailedExit() {
	if e == nil {
		return
	}
	e.exitingAccount = false
}

// StopAllForLogout stops sync, cancels all active timers/streams/direct routes/relay RPCs/terminals,
// settles pending image/push waiters, and discards cached tokens.
func (e *Engine) StopAllForLogout(ctx *StepContext) {
	if e == nil {
		return
	}
	e.exitingAccount = true
	prevCtx := e.activeStepCtx
	e.activeStepCtx = ctx
	defer func() { e.activeStepCtx = prevCtx }()

	if e.cachedToken != "" {
		e.cachedToken = ""
		e.tokenExpiresMs = 0
	}
	e.handleLifecycle(ctx, &corev1.SyncLifecycleAction{
		Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP,
	})
	// Normal STOP preserves Forward/PrefsPut/Healthz; logout must cancel every remaining relay RPC
	// and settle every auth-retry waiter before the outgoing Engine is discarded.
	e.cancelAllSyncRelayRpcs(ctx, true)
	if e.secEngine != nil {
		e.emitSecurityEffects(ctx, e.secEngine.InvalidateAccountAuth())
	}
	if len(e.secOps) > 0 {
		tags := make([]uint64, 0, len(e.secOps))
		for tag := range e.secOps {
			tags = append(tags, tag)
		}
		sortutil.Uint64s(tags)
		for _, tag := range tags {
			op := e.secOps[tag]
			if op == nil {
				continue
			}
			switch op.kind {
			case secOpSessionImage:
				e.emitSessionImageLoaded(ctx, &corev1.SessionImageLoadedEffect{
					OpId:      op.opID,
					SessionId: op.sid,
					ImageId:   op.imageID,
					Status:    corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_ERROR,
					ErrorCode: "canceled",
				})
			case secOpPushAuth:
				if op.push != nil {
					e.emitPushRpcResult(ctx, &corev1.PushRpcResultEffect{
						OpId:      op.push.opID,
						Ok:        false,
						Retryable: true,
						Error:     "canceled",
					})
				}
			}
		}
		clear(e.secOps)
	}
}

// DeviceDisplayName returns the human-readable name for a synced device ID, if known.
func (e *Engine) DeviceDisplayName(deviceID string) string {
	if e == nil || deviceID == "" {
		return ""
	}
	d := e.devices[deviceID]
	if d == nil || d.Meta == nil {
		return ""
	}
	if d.Meta.Name != "" {
		return d.Meta.Name
	}
	return d.Meta.Description
}

// MachineDisplayName returns the human-readable name for a synced machine ID, if known.
func (e *Engine) MachineDisplayName(machineID string) string {
	if e == nil || machineID == "" {
		return ""
	}
	m := e.machines[machineID]
	if m == nil || m.Meta == nil {
		return ""
	}
	return m.Meta.Hostname
}

// HasSelfClientDevice reports whether the current device is listed as a client device in the directory.
func (e *Engine) HasSelfClientDevice(selfID string) bool {
	if e == nil || selfID == "" {
		return false
	}
	if d := e.devices[selfID]; d != nil {
		return d.Kind == v1.DeviceKind_DEVICE_KIND_CLIENT
	}
	return e.security.TrustedClients[selfID]
}
