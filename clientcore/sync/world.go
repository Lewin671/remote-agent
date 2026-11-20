package sync

import (
	"slices"

	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
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
	if d != nil && d.Meta != nil && d.Meta.Name != "" {
		return d.Meta.Name
	}
	return ""
}

func (e *Engine) managerNameText() *corev1.Text {
	if name := e.managerDevicePlainName(); name != "" {
		return text.Plain(name)
	}
	if e.security.AuthorityDeviceID != "" {
		return text.Msg(text.KeySettingsUnnamed)
	}
	return nil
}

// Run executes fn inside a StepContext and returns the collected StepResult.
func (e *Engine) Run(fn func(ctx *StepContext)) StepResult {
	ctx := newStepContext()
	e.entropy.needRequested = false
	if fn != nil {
		fn(ctx)
	}
	e.refreshWorld()
	return finalizeResult(ctx)
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
		w.World.Machines = sortedByID(e.machines, func(m *MachineEntry) *lists.Machine { return &m.Machine })
		e.machineOrderStale = false
	}
	if e.sessionOrderStale {
		w.World.Sessions = sortedByID(e.sessions, func(s *SessionEntry) *lists.Session { return &s.Session })
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
		m.ReachabilityUnknown = !reachable && e.machineReachabilityUnknown(m)
		var pv uint32
		if m.Meta != nil {
			pv = m.Meta.ProtocolVersion
		}
		m.ProtocolVersion = pv
		m.DaemonOutOfDate = pv > 0 && pv < ProtocolVersion
		m.ClientOutOfDate = pv > 0 && pv > ProtocolVersion
	}

	e.unlinkedFlag = unlinked
	w.DisplayGrace = e.displayGraceActive()
	w.ReachabilityUnknown = e.coldStartGraceActive()
	w.ManagerNameText = e.managerNameText()
	w.World.SessionListFailed = w.SessionListErrorText != nil
	w.World.Prefs = e.prefs.Prefs
	w.World.PrefsLoaded = e.prefs.Loaded
	w.World.PrefsLoadFailed = e.prefs.LoadFailed
	w.World.PrefsSyncing = len(e.prefsQueue) > 0
	w.World.IsManager = e.isManager()
	w.World.ManagerName = e.managerDevicePlainName()
	w.World.AccountControlLost = e.accountControlLost()
	w.World.AccountClockSkewed = e.accountClockSkewed()
}

// storeMachine records m as the entry for its ID, replacing any previous one.
func (e *Engine) storeMachine(m *MachineEntry) {
	prev := e.machines[m.ID]
	e.machines[m.ID] = m
	e.machineOrderStale = e.machineOrderStale || !replaceByID(e.w.World.Machines, prev != nil, &m.Machine, func(x *lists.Machine) string { return x.ID })
}

// storeSession records s as the entry for its ID, replacing any previous one.
func (e *Engine) storeSession(s *SessionEntry) {
	prev := e.sessions[s.ID]
	e.sessions[s.ID] = s
	e.sessionOrderStale = e.sessionOrderStale || !replaceByID(e.w.World.Sessions, prev != nil, &s.Session, func(x *lists.Session) string { return x.ID })
}

// replaceByID swaps v into the ID-sorted list in place when an entry with the
// same ID is already there; it reports false when the list must be re-sorted.
func replaceByID[T any](list []*T, existed bool, v *T, id func(*T) string) bool {
	if !existed {
		return false
	}
	key := id(v)
	i, ok := slices.BinarySearchFunc(list, key, func(x *T, k string) int { return strings.Compare(id(x), k) })
	if !ok {
		return false
	}
	list[i] = v
	return true
}

func sortedByID[E any, T any](entries map[string]*E, view func(*E) *T) []*T {
	ids := make([]string, 0, len(entries))
	for id, x := range entries {
		if x != nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	out := make([]*T, len(ids))
	for i, id := range ids {
		out[i] = view(entries[id])
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
func (e *Engine) RecoveryState() *Recovery {
	return e.recovery
}

// ScopeHMACKey returns the cached scope HMAC key (from SyncLifecycleAction or cache bootstrap).
func (e *Engine) ScopeHMACKey() []byte {
	return e.scopeHMACKey
}

// HomeScopeToken returns the remembered home scope token from lifecycle/cache bootstrap.
func (e *Engine) HomeScopeToken() string {
	return e.homeScopeToken
}
