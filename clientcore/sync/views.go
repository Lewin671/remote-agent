package sync

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func protoConnState(c lists.ConnectionKind) corev1.ConnState {
	switch c {
	case lists.ConnConnected:
		return corev1.ConnState_CONN_STATE_CONNECTED
	case lists.ConnReconnecting:
		return corev1.ConnState_CONN_STATE_RECONNECTING
	case lists.ConnConnecting:
		return corev1.ConnState_CONN_STATE_CONNECTING
	case lists.ConnOffline:
		return corev1.ConnState_CONN_STATE_OFFLINE
	case lists.ConnNoNetwork:
		return corev1.ConnState_CONN_STATE_NO_NETWORK
	default:
		return corev1.ConnState_CONN_STATE_UNSPECIFIED
	}
}

func (e *Engine) machineTerminalReady(machineID string) bool {
	return e.w.World.Connection == lists.ConnConnected || (machineID != "" && e.directReady[machineID])
}

func (e *Engine) buildSyncDevice(d *DeviceEntry) *world.SyncDevice {
	if d == nil {
		return nil
	}
	var trusted bool
	if e.security.Loaded {
		switch d.Kind {
		case v1.DeviceKind_DEVICE_KIND_MACHINE:
			trusted = e.security.TrustedMachines[d.ID]
		case v1.DeviceKind_DEVICE_KIND_CLIENT:
			trusted = e.security.TrustedClients[d.ID]
		}
	}
	return &world.SyncDevice{
		DeviceID:   d.ID,
		Kind:       d.Kind,
		CreatedMs:  d.CreatedMs,
		LastSeenMs: d.LastSeenMs,
		Meta:       d.Meta,
		MetaRev:    d.MetaRev,
		MetaBroken: d.MetaBroken,
		Trusted:    trusted,
		Registered: d.Registered,
	}
}

type syncDevicesByID []*world.SyncDevice

func (s syncDevicesByID) Len() int           { return len(s) }
func (s syncDevicesByID) Less(i, j int) bool { return s[i].DeviceID < s[j].DeviceID }
func (s syncDevicesByID) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func (e *Engine) sortedSyncDevices() []*world.SyncDevice {
	if len(e.devices) == 0 {
		return nil
	}
	devices := make([]*world.SyncDevice, 0, len(e.devices))
	for _, d := range e.devices {
		if dv := e.buildSyncDevice(d); dv != nil {
			devices = append(devices, dv)
		}
	}
	sortutil.SortInterface(syncDevicesByID(devices))
	return devices
}

// ConnectionStep returns the current structured connection establishment step.
func (e *Engine) ConnectionStep() world.ConnectStep { return e.connStep }

// DevicesError returns the structured devices list error reason, if any.
func (e *Engine) DevicesError() world.Reason { return e.devicesError }

// SessionErrorReasons returns the structured error and fetch-error reasons for sessionID.
func (e *Engine) SessionErrorReasons(sessionID string) (errReason, fetchErrReason world.Reason) {
	if b := e.buffers[sessionID]; b != nil {
		return b.errorReason, b.fetchErrorReason
	}
	return world.Reason{}, world.Reason{}
}

// SecurityScreenSnapshot is a lightweight O(devices) read-only projection for security ViewModels.
type SecurityScreenSnapshot struct {
	Loaded              bool
	IsManager           bool
	MachinesListed      bool
	LeaseEndMs          int64
	RecoveryPending     bool
	RecoveryReadyMs     int64
	RecoveryStatusStale bool
	SelfDeviceID        string
	AuthorityDeviceID   string
	Devices             []*world.SyncDevice
}

// SecuritySnapshot returns security & client-device metadata without copying sessions or machines.
func (e *Engine) SecuritySnapshot() SecurityScreenSnapshot {
	return SecurityScreenSnapshot{
		Loaded:              e.security.Loaded,
		IsManager:           e.w.World.IsManager,
		MachinesListed:      e.w.World.MachinesListed,
		LeaseEndMs:          e.security.LeaseEndMs,
		RecoveryPending:     e.security.RecoveryPending,
		RecoveryReadyMs:     e.security.RecoveryReadyMs,
		RecoveryStatusStale: e.security.RecoveryStatusStale,
		SelfDeviceID:        e.security.SelfDeviceID,
		AuthorityDeviceID:   e.security.AuthorityDeviceID,
		Devices:             e.sortedSyncDevices(),
	}
}

// SyncStateOpts controls optional collections populated by Engine.BuildSyncState.
type SyncStateOpts struct {
	IncludeDevices     bool
	IncludeDirectReady bool
}

// BuildSyncState constructs a lightweight internal Go snapshot of synchronized
// connection, security, and optional device/direct-readiness state on demand.
// Text fields (ConnectionReason, ConnectionStep, ManagerName, DevicesErrorText,
// LeaseRenewalAdvice) are populated by screens.ApplySyncStateTexts.
func (e *Engine) BuildSyncState(opts SyncStateOpts) *world.SyncState {
	w := &e.w
	var devices []*world.SyncDevice
	if opts.IncludeDevices {
		devices = e.sortedSyncDevices()
	}
	var directReady map[string]bool
	if opts.IncludeDirectReady && len(e.directReady) > 0 {
		for id, ready := range e.directReady {
			if ready {
				if directReady == nil {
					directReady = make(map[string]bool, len(e.directReady))
				}
				directReady[id] = true
			}
		}
	}
	return &world.SyncState{
		Connection:            protoConnState(w.World.Connection),
		DisconnectedAtMs:      w.DisconnectedAtMs,
		LivePushConnected:     w.LivePushConnected,
		Unlinked:              e.unlinkedFlag,
		IsManager:             w.World.IsManager,
		SelfDeviceID:          e.security.SelfDeviceID,
		AuthorityDeviceID:     e.security.AuthorityDeviceID,
		AccountClockSkewed:    w.World.AccountClockSkewed,
		Removed:               e.removed,
		SecurityLoaded:        e.security.Loaded,
		LeaseEndMs:            e.security.LeaseEndMs,
		RecoveryPending:       e.security.RecoveryPending,
		RecoveryReadyMs:       e.security.RecoveryReadyMs,
		RecoveryStatusStale:   e.security.RecoveryStatusStale,
		HasHandoffOffer:       e.security.HasHandoffOffer,
		MachinesListed:        w.World.MachinesListed,
		SessionsListed:        w.World.SessionsListed,
		LiveLoaded:            w.World.LiveLoaded,
		HasSessions:           len(w.World.Sessions) > 0,
		DisplayGrace:          w.DisplayGrace,
		ReachabilityUnknown:   w.ReachabilityUnknown,
		ConnectionStartedAtMs: e.connStartedAtMs,
		NetworkAvailable:      e.networkAvailable,
		EverOnline:            e.everReady,
		ConnectFailures:       int32(e.recovery.Failures()),
		DevicesLoadFailed:     e.devicesLoadFailed,
		Devices:               devices,
		DirectReadyMachines:   directReady,
	}
}
