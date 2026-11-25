package sync

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

const internalSelfDescribeActionBase uint64 = 1 << 62

// emitWatchedSessionsIfChanged emits WatchedSessionsEffect whenever the sorted set of
// watched (session_id, machine_id) routes changes.
func (e *Engine) emitWatchedSessionsIfChanged(ctx *StepContext) {
	if ctx == nil {
		return
	}
	sids := make([]string, 0, len(e.watchedSessions))
	for sid := range e.watchedSessions {
		if sid != "" {
			sids = append(sids, sid)
		}
	}
	sortutil.Strings(sids)

	next := make([]watchedRoute, len(sids))
	for i, sid := range sids {
		var mid string
		if s := e.sessions[sid]; s != nil {
			mid = s.MachineID
		}
		next[i] = watchedRoute{sessionID: sid, machineID: mid}
	}
	if len(next) == len(e.lastEmittedWatched) {
		same := true
		for i := range next {
			if next[i] != e.lastEmittedWatched[i] {
				same = false
				break
			}
		}
		if same {
			return
		}
	}
	e.lastEmittedWatched = next
	routes := make([]*corev1.WatchedSessionRoute, len(next))
	for i, r := range next {
		routes[i] = &corev1.WatchedSessionRoute{
			SessionId: r.sessionID,
			MachineId: r.machineID,
		}
	}
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_WatchedSessions{
			WatchedSessions: &corev1.WatchedSessionsEffect{Sessions: routes},
		},
	})
}

// pruneUntrustedCaches emits ScheduleDeleteCacheEffect and UsageCacheRequestEffect(delete_cache=true)
// for any machine that is no longer trusted under the loaded security state (or for all known
// machines when this client has been removed / is not a member).
func (e *Engine) pruneUntrustedCaches(ctx *StepContext) {
	if ctx == nil || !e.security.Loaded {
		return
	}
	candidateSet := make(map[string]bool)
	for mid := range e.machines {
		if mid != "" {
			candidateSet[mid] = true
		}
	}
	for id, d := range e.devices {
		if id != "" && d != nil && d.Kind == v1.DeviceKind_DEVICE_KIND_MACHINE {
			candidateSet[id] = true
		}
	}
	for mid, ok := range e.security.ArchiveMachines {
		if mid != "" && ok {
			candidateSet[mid] = true
		}
	}
	if len(candidateSet) == 0 {
		return
	}

	pruneAll := e.removed || !e.security.IsMember || len(e.security.TrustedMachines) == 0
	trusted := e.security.TrustedMachines

	candidates := make([]string, 0, len(candidateSet))
	for mid := range candidateSet {
		candidates = append(candidates, mid)
	}
	sortutil.Strings(candidates)

	for _, mid := range candidates {
		if !pruneAll && trusted[mid] {
			delete(e.prunedUntrustedMachines, mid)
			continue
		}
		if e.prunedUntrustedMachines[mid] {
			continue
		}
		e.prunedUntrustedMachines[mid] = true
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_ScheduleDeleteCache{
				ScheduleDeleteCache: &corev1.ScheduleDeleteCacheEffect{MachineId: mid},
			},
		})
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_UsageCacheRequest{
				UsageCacheRequest: &corev1.UsageCacheRequestEffect{
					MachineId:   mid,
					DeleteCache: true,
				},
			},
		})
	}
}

// maybeDescribeSelf automatically updates this client's DeviceMeta (platform and
// device description) once connected and authenticated whenever the directory
// record for self is missing platform or description.
func (e *Engine) maybeDescribeSelf(ctx *StepContext) {
	if ctx == nil || e.selfPlatform == "" || !e.running || e.removed {
		return
	}
	if e.w.World.Connection != lists.ConnConnected {
		return
	}
	if e.secEngine == nil || !e.secEngine.HasWorkflowBootstrapped() {
		return
	}
	if !e.security.Loaded || !e.security.IsMember || e.security.SelfDeviceID == "" {
		return
	}
	if e.selfDescribeActionID != 0 {
		return
	}
	me := e.devices[e.security.SelfDeviceID]
	if me == nil {
		return
	}
	needsUpdate := me.Meta == nil ||
		me.Meta.Platform != e.selfPlatform ||
		(me.Meta.Description == "" && e.selfDescription != "")
	if !needsUpdate {
		e.selfDescribeAttempted = false
		return
	}
	if e.selfDescribeAttempted &&
		e.selfDescribeAttemptedRev == me.MetaRev &&
		e.selfDescribeAttemptedLease == e.security.LeaseEndMs {
		return
	}
	e.selfDescribeAttempted = true
	e.selfDescribeAttemptedRev = me.MetaRev
	e.selfDescribeAttemptedLease = e.security.LeaseEndMs

	aid := internalSelfDescribeActionBase | e.allocOpID()
	e.selfDescribeActionID = aid
	effs := e.secEngine.HandleWorkflowAction(
		e.nowMs,
		&corev1.SecurityWorkflowActionEvent{
			ActionId: aid,
			Action: &corev1.SecurityWorkflowActionEvent_UpdateDeviceMeta{
				UpdateDeviceMeta: &corev1.SecurityUpdateDeviceMetaAction{
					DeviceId:         e.security.SelfDeviceID,
					Platform:         e.selfPlatform,
					Description:      e.selfDescription,
					DescribeSelfOnly: true,
				},
			},
		},
		e.securityDeviceOffers(),
		nil,
	)
	e.emitSecurityEffects(ctx, effs)
	e.maybeRequestEntropy(ctx)
}
