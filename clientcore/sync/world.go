package sync

import (
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func (e *Engine) worldUnlinked() bool {
	return e.conn == lists.ConnNoNetwork ||
		e.conn == lists.ConnOffline ||
		(e.conn == lists.ConnReconnecting && !e.liveLoaded)
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
	return finalizeResult(ctx)
}

// SyncToState materializes the derived sync state into screens.State.
func (e *Engine) SyncToState(st *screens.State) {
	if st == nil {
		return
	}
	displayGrace := e.displayGraceActive()
	reachabilityUnknown := e.coldStartGraceActive()
	unlinked := e.worldUnlinked()

	machines := make([]*lists.Machine, 0, len(e.machines))
	for _, m := range e.machines {
		if m == nil {
			continue
		}
		reachable := e.machineReachable(m)
		unknown := !reachable && e.machineReachabilityUnknown(m)
		mUnlinked := unlinked && !e.directReady[m.ID]
		var pv uint32
		if m.Meta != nil {
			pv = m.Meta.ProtocolVersion
		}
		machines = append(machines, &lists.Machine{
			ID:                  m.ID,
			Online:              m.Online,
			Reachable:           &reachable,
			ReachabilityUnknown: unknown,
			Unlinked:            &mUnlinked,
			LastSeenMs:          m.LastSeenMs,
			ProtocolVersion:     pv,
			DaemonOutOfDate:     pv > 0 && pv < ProtocolVersion,
			ClientOutOfDate:     pv > 0 && pv > ProtocolVersion,
			Meta:                m.Meta,
		})
	}
	slices.SortFunc(machines, func(a, b *lists.Machine) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})

	sessions := make([]*lists.Session, 0, len(e.sessions))
	for _, s := range e.sessions {
		if s == nil {
			continue
		}
		var preview string
		if s.Meta != nil {
			preview = s.Meta.Preview
		}
		sessions = append(sessions, &lists.Session{
			ID:             s.ID,
			MachineID:      s.MachineID,
			Info:           s.Meta,
			UpdatedMs:      s.UpdatedMs,
			ListedEndedMs:  s.ListedEndedMs,
			Preview:        preview,
			Pending:        s.LivePending,
			ScheduleName:   s.ScheduleName,
			ScheduleUnread: s.ScheduleUnread,
		})
	}
	slices.SortFunc(sessions, func(a, b *lists.Session) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})

	st.Cal = e.cal
	st.DisplayGrace = displayGrace
	st.ReachabilityUnknown = reachabilityUnknown
	st.ConnectionReason = ""
	st.ConnectionReasonText = e.connReasonText
	st.SessionListErrorText = e.sessionListErr
	st.ManagerNameText = e.managerNameText()
	st.DisconnectedAtMs = e.offlineSinceMs
	st.LivePushConnected = e.streamOpen

	st.ProjectChangedElsewhere = e.prefsChangedElsewhere
	st.PrefsGlobalError = ""
	st.PrefsGlobalErrorText = e.prefsGlobalErr

	clear(st.PrefsPendingPath)
	for k, v := range e.prefsPendingByPath {
		st.PrefsPendingPath[k] = v
	}
	clear(st.PrefsSaveErrorByPath)
	clear(st.PrefsSaveErrorTextByPath)
	for k, v := range e.prefsErrorByPath {
		st.PrefsSaveErrorTextByPath[k] = v
	}
	clear(st.PrefsSavingSettings)
	for k, v := range e.prefsSettingsSaving {
		st.PrefsSavingSettings[k] = v
	}
	clear(st.PrefsSettingsError)
	clear(st.PrefsSettingsErrorText)
	for k, v := range e.prefsSettingsErr {
		st.PrefsSettingsErrorText[k] = v
	}
	for key, d := range st.NewSessions {
		if d == nil {
			continue
		}
		d.ConfigSaving = st.PrefsSavingSettings[key]
		if st.PrefsSettingsErrorText[key] != nil {
			d.ConfigSaveError = "failed"
		} else {
			d.ConfigSaveError = ""
		}
	}

	if e.homeScopeToken != "" && st.RememberedScopeToken == "" {
		st.RememberedScopeToken = e.homeScopeToken
	}

	st.World = lists.World{
		Machines:            machines,
		MachinesListed:      e.machinesListed,
		Sessions:            sessions,
		SessionsListed:      e.sessionsListed,
		SessionsNext:        e.sessionsNext,
		HasMoreSessions:     e.hasMoreSessions,
		LoadingMoreSessions: e.loadingMore,
		SessionListFailed:   e.sessionListErr != nil,
		SessionListError:    e.sessionListRaw,
		LiveLoaded:          e.liveLoaded,
		Prefs:               e.prefs.Prefs,
		PrefsLoaded:         e.prefs.Loaded,
		PrefsLoadFailed:     e.prefs.LoadFailed,
		PrefsSyncing:        len(e.prefsQueue) > 0,
		Connection:          e.conn,
		Unlinked:            &unlinked,
		ClientVersion:       ProtocolVersion,
		IsManager:           e.isManager(),
		ManagerName:         e.managerDevicePlainName(),
		AccountControlLost:  e.accountControlLost(),
		AccountClockSkewed:  e.accountClockSkewed(),
	}
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
	return e.conn
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
