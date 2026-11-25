package sync

import (
	"slices"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/linkcore"
)

func (e *Engine) worldUnlinked() bool {
	return e.w.World.Connection == lists.ConnNoNetwork ||
		e.w.World.Connection == lists.ConnOffline ||
		(e.w.World.Connection == lists.ConnReconnecting && !e.w.World.LiveLoaded)
}

func (e *Engine) machineReachable(m *MachineEntry) bool {
	if m == nil {
		return false
	}
	if e.directReady[m.ID] {
		return true
	}
	return m.Online && !e.worldUnlinked()
}

func (e *Engine) machineReachabilityUnknown(m *MachineEntry) bool {
	if !e.coldStartGraceActive() {
		return false
	}
	if m != nil && e.directReady[m.ID] {
		return false
	}
	return true
}

func (e *Engine) accountControlLost() bool {
	if e.removed {
		return true
	}
	if !e.security.Loaded {
		return false
	}
	if !e.security.IsMember {
		return true
	}
	return e.security.LeaseEndMs > 0 && e.nowMs > 0 && e.nowMs >= e.security.LeaseEndMs
}

// LeaseExpired reports whether the client's remote-control lease has expired.
func (e *Engine) LeaseExpired() bool {
	return e.security.Loaded && e.security.LeaseEndMs > 0 && e.nowMs > 0 && e.nowMs >= e.security.LeaseEndMs
}

// ApplyMachineSettingsSnapshot updates a machine's in-memory DisabledAgents and SettingsRevision
// immediately from an authoritative GetSettings/UpdateSettings response so all screens converge
// without waiting for relay stream push.
func (e *Engine) ApplyMachineSettingsSnapshot(machineID string, st *v1.DaemonSettings) bool {
	if e == nil || machineID == "" || st == nil {
		return false
	}
	m := e.machines[machineID]
	if m == nil || m.Meta == nil {
		return false
	}
	if st.Revision < m.Meta.SettingsRevision {
		return false
	}
	next := make([]v1.Agent, 0, len(st.DisabledAgents))
	for _, a := range st.DisabledAgents {
		if a != v1.Agent_AGENT_UNSPECIFIED {
			next = append(next, a)
		}
	}
	changed := m.Meta.SettingsRevision != st.Revision || !slices.Equal(m.Meta.DisabledAgents, next)
	m.Meta.DisabledAgents = next
	m.Meta.SettingsRevision = st.Revision
	return changed
}

func (e *Engine) accountClockSkewed() bool {
	return e.serverAhead || e.security.ClockSkewed
}

func (e *Engine) isManager() bool {
	if !e.security.Loaded {
		return true
	}
	if !e.security.IsMember {
		return false
	}
	if e.security.AuthorityDeviceID == "" || e.security.SelfDeviceID == "" {
		return false
	}
	return e.security.AuthorityDeviceID == e.security.SelfDeviceID
}

func (e *Engine) managerDevicePlainName() string {
	id := e.security.AuthorityDeviceID
	if id == "" {
		return ""
	}
	d := e.devices[id]
	if d != nil && d.Meta != nil {
		if name := strings.TrimSpace(d.Meta.Name); name != "" {
			return name
		}
		if desc := strings.TrimSpace(d.Meta.Description); desc != "" {
			return desc
		}
	}
	return ""
}

// BeginStep enters a StepContext without closure allocation; pair with EndStep.
func (e *Engine) BeginStep() (prev, ctx *StepContext) {
	ctx = newStepContext()
	prev = e.activeStepCtx
	e.activeStepCtx = ctx
	e.entropyNeedRequested = false
	return prev, ctx
}

// EndStep exits ctx, refreshes derived world state, and returns the collected StepResult.
func (e *Engine) EndStep(prev, ctx *StepContext) StepResult {
	e.restoreStepCtx(prev)
	e.refreshWorld()
	e.detectConnectionChange(ctx)
	return finalizeResult(ctx)
}

// Run executes fn inside a StepContext and returns the collected StepResult.
func (e *Engine) Run(fn func(ctx *StepContext)) StepResult {
	prev, ctx := e.BeginStep()
	defer e.restoreStepCtx(prev)
	if fn != nil {
		fn(ctx)
	}
	e.refreshWorld()
	e.detectConnectionChange(ctx)
	return finalizeResult(ctx)
}

func (e *Engine) currentConnSnap() connStateSnap {
	return connStateSnap{
		conn:                e.w.World.Connection,
		displayGrace:        e.w.DisplayGrace,
		reachabilityUnknown: e.w.ReachabilityUnknown,
		unlinked:            e.unlinkedFlag,
		removed:             e.removed,
		networkAvailable:    e.networkAvailable,
		liveLoaded:          e.w.World.LiveLoaded,
		securityLoaded:      e.security.Loaded,
		isMember:            e.security.IsMember,
		machinesListed:      e.w.World.MachinesListed,
		sessionsListed:      e.w.World.SessionsListed,
		hasSessions:         len(e.sessions) > 0,
		accountControlLost:  e.w.World.AccountControlLost,
		accountClockSkewed:  e.w.World.AccountClockSkewed,
		disconnectedAtMs:    e.w.DisconnectedAtMs,
		connectionReason:    e.w.ConnectionReason,
		managerName:         e.w.World.ManagerName,
		isManager:           e.w.World.IsManager,
	}
}

func (e *Engine) detectConnectionChange(ctx *StepContext) {
	if ctx == nil {
		return
	}
	cur := e.currentConnSnap()
	if !e.connSnapInit || cur != e.connSnap {
		e.connSnap = cur
		e.connSnapInit = true
		ctx.Dirty.MarkConnection()
	}
}

// HasWatchedSessions reports whether any session currently has an active watcher.
func (e *Engine) HasWatchedSessions() bool {
	return len(e.watchedSessions) > 0
}

// CanUseMachineCache reports whether local schedule/usage caches for machineID are
// authorized under the loaded security state. Before security loads (cold start),
// signed caches remain readable offline; once security loads, both self membership
// and machine trust (active member, excluding revoked/archived machines) are required.
func (e *Engine) CanUseMachineCache(machineID string) bool {
	if machineID == "" || e.removed {
		return false
	}
	if !e.security.Loaded {
		return true
	}
	if !e.security.IsMember {
		return false
	}
	return e.security.TrustedMachines[machineID]
}

// World returns the core's synchronized world. The engine is its only writer:
// derived fields are refreshed when HandleEvent and Run return and when
// SeedSession and EnqueuePrefsOp change the directory or prefs mid-step.
// Callers must treat it as read-only.
func (e *Engine) World() *world.State {
	return &e.w
}

// refreshWorld recomputes the world fields derived from other engine state
// (connection, clocks, security, prefs queue) and re-sorts the machine and
// session lists after membership changes. It costs O(machines), never
// O(sessions), unless sessions were added or removed.
func (e *Engine) refreshWorld() {
	w := &e.w
	if e.machineOrderStale {
		w.World.Machines = sortedMachines(e.machines)
		e.machineOrderStale = false
	}
	if e.sessionOrderStale {
		w.World.Sessions = sortedSessions(e.sessions)
		e.sessionOrderStale = false
	}

	unlinked := e.worldUnlinked()
	for _, m := range e.machines {
		if m == nil {
			continue
		}
		reachable := e.machineReachable(m)
		m.reachable = reachable
		m.unlinked = unlinked && !e.directReady[m.ID]
		m.Reachable = &m.reachable
		m.Unlinked = &m.unlinked
		m.Archived = e.security.Loaded && !e.security.TrustedMachines[m.ID] && e.security.ArchiveMachines[m.ID]
		m.ReachabilityUnknown = !reachable && e.machineReachabilityUnknown(m)
		var pv uint32
		if m.Meta != nil {
			pv = m.Meta.ProtocolVersion
		}
		m.ProtocolVersion = pv
		m.DaemonOutOfDate = pv > 0 && pv < PeerVersion
		m.ClientOutOfDate = pv > 0 && pv > PeerVersion
	}

	e.unlinkedFlag = unlinked
	w.DisplayGrace = e.displayGraceActive()
	w.ReachabilityUnknown = e.coldStartGraceActive()
	w.Removed = e.removed
	w.AuthorityDeviceID = e.security.AuthorityDeviceID
	w.World.SessionListFailed = !w.SessionListError.IsZero()
	w.World.Prefs = e.prefs.Prefs
	w.World.PrefsLoaded = e.prefs.Loaded
	w.World.PrefsLoadFailed = e.prefs.LoadFailed
	w.PrefsBroken = e.prefs.Broken
	w.World.PrefsSyncing = len(e.prefsQueue) > 0
	w.World.IsManager = e.isManager()
	w.World.ManagerName = e.managerDevicePlainName()
	w.World.AccountControlLost = e.accountControlLost()
	w.World.AccountClockSkewed = e.accountClockSkewed()
	w.World.MachineReplacements = e.machineReplacements
}

// storeMachine records m as the entry for its ID, replacing any previous one.
func (e *Engine) storeMachine(m *MachineEntry) {
	prev := e.machines[m.ID]
	e.machines[m.ID] = m
	e.machineOrderStale = e.machineOrderStale || !replaceMachineByID(e.w.World.Machines, prev != nil, &m.Machine)
}

// storeSession records s as the entry for its ID, replacing any previous one.
func (e *Engine) storeSession(s *SessionEntry) {
	prev := e.sessions[s.ID]
	e.sessions[s.ID] = s
	e.sessionOrderStale = e.sessionOrderStale || !replaceSessionByID(e.w.World.Sessions, prev != nil, &s.Session)
}

func replaceMachineByID(list []*lists.Machine, existed bool, v *lists.Machine) bool {
	if !existed {
		return false
	}
	lo, hi := 0, len(list)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if list[mid].ID < v.ID {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo >= len(list) || list[lo].ID != v.ID {
		return false
	}
	list[lo] = v
	return true
}

func replaceSessionByID(list []*lists.Session, existed bool, v *lists.Session) bool {
	if !existed {
		return false
	}
	lo, hi := 0, len(list)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if list[mid].ID < v.ID {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo >= len(list) || list[lo].ID != v.ID {
		return false
	}
	list[lo] = v
	return true
}

func sortedMachines(entries map[string]*MachineEntry) []*lists.Machine {
	ids := make([]string, 0, len(entries))
	for id, x := range entries {
		if x != nil {
			ids = append(ids, id)
		}
	}
	sortutil.Strings(ids)
	out := make([]*lists.Machine, len(ids))
	for i, id := range ids {
		out[i] = &entries[id].Machine
	}
	return out
}

func sortedSessions(entries map[string]*SessionEntry) []*lists.Session {
	ids := make([]string, 0, len(entries))
	for id, x := range entries {
		if x != nil {
			ids = append(ids, id)
		}
	}
	sortutil.Strings(ids)
	out := make([]*lists.Session, len(ids))
	for i, id := range ids {
		out[i] = &entries[id].Session
	}
	return out
}

// MachineEntry returns the plain machine entry for machineID, or nil.
func (e *Engine) MachineEntry(machineID string) *MachineEntry {
	return e.machines[machineID]
}

// SessionEntry returns the plain session entry for sessionID, or nil.
func (e *Engine) SessionEntry(sessionID string) *SessionEntry {
	return e.sessions[sessionID]
}

// Prefs returns the current PrefsState.
func (e *Engine) Prefs() PrefsState {
	return e.prefs
}

// Connection returns the current connection state.
func (e *Engine) Connection() lists.ConnectionKind {
	return e.w.World.Connection
}

// RecoveryState exposes the internal Recovery instance for testing/inspection.
func (e *Engine) RecoveryState() *linkcore.Recovery {
	return &e.recovery
}

// ScopeHMACKey returns the cached scope HMAC key (from SyncLifecycleAction or cache bootstrap).
func (e *Engine) ScopeHMACKey() []byte {
	return e.scopeHMACKey
}

// HomeScopeToken returns the remembered home scope token from lifecycle/cache bootstrap.
func (e *Engine) HomeScopeToken() string {
	return e.homeScopeToken
}
