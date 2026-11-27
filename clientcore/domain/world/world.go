// Package world holds the client core's synchronized world. The sync engine
// owns the single instance and is the only writer; screen builders read it in
// place, so nothing copies it between Dispatch and View.
package world

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/projection"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

// State is the synchronized world: the machine and session directory, project
// preferences, connection status, and the local calendar.
type State struct {
	World lists.World
	Cal   lists.Calendar

	DisplayGrace        bool
	ReachabilityUnknown bool
	Removed             bool
	ConnectionReason    Reason
	SessionListError    Reason
	AuthorityDeviceID   string
	DisconnectedAtMs    int64
	LivePushConnected   bool
	PrefsBroken         bool

	ProjectChangedElsewhere bool
	PrefsGlobalError        Reason
	PrefsPendingPath        map[string]bool
	PrefsSaveErrorByPath    map[string]Reason
	PrefsSavingSettings     map[string]bool
	PrefsSettingsError      map[string]Reason
}

// New returns an empty, connecting world with initialized lookup maps.
func New() *State {
	w := &State{}
	w.Init()
	return w
}

// Init resets w to an empty, connecting world with initialized lookup maps.
func (w *State) Init() {
	*w = State{
		World:                lists.World{Connection: lists.ConnConnecting},
		PrefsPendingPath:     make(map[string]bool),
		PrefsSaveErrorByPath: make(map[string]Reason),
		PrefsSavingSettings:  make(map[string]bool),
		PrefsSettingsError:   make(map[string]Reason),
	}
}

// SessionSnapshot is one session's sync state and shown events, assembled by
// the sync engine for the session screen.
type SessionSnapshot struct {
	Found                bool
	SessionsListed       bool
	LiveLoaded           bool
	Removed              bool
	AccountControlLost   bool
	AccountClockSkewed   bool
	Conn                 lists.ConnectionKind
	Unlinked             bool
	ReachabilityUnknown  bool
	TerminalReady        bool
	TerminalWaiting      bool
	DisconnectedAtMs     int64
	LookingUp            bool
	LookupFailed         bool
	LookupError          Reason
	ProtocolVersion      uint32
	Session              *lists.Session
	Machine              *lists.Machine
	Checkpoint           *v1.LiveSnapshot
	DeviceNames          map[string]string
	ThisDeviceID         string
	Loading              bool
	Loaded               bool
	AwaitingCache        bool
	Backfilling          bool
	Fetching             bool
	HasGap               bool
	TailRequested        bool
	Retired              bool
	RetryingCorruptCache bool
	Error                Reason
	FetchError           Reason
	PrefixLen            uint64
	PendingCount         int32
	UnavailableCount     uint32
	LastSeq              uint64
	EventRev             uint64
	Projection           *projection.State
	Events               []*v1.Event
}

// SyncDevice is a read-only device directory snapshot shared by sync, domain/machine, and screens.
type SyncDevice struct {
	DeviceID   string
	Kind       v1.DeviceKind
	CreatedMs  int64
	LastSeenMs int64
	Meta       *v1.DeviceMeta
	MetaRev    uint64
	MetaBroken bool
	Trusted    bool
	Registered bool
}

// SyncState is a lightweight Go-only snapshot of synchronized connection, security,
// and optional device/direct-readiness metadata built on demand for screen builders.
type SyncState struct {
	Connection            corev1.ConnState
	ConnectionReason      *corev1.Text
	ConnectionStep        *corev1.Text
	DisconnectedAtMs      int64
	LivePushConnected     bool
	Unlinked              bool
	IsManager             bool
	SelfDeviceID          string
	AuthorityDeviceID     string
	ManagerName           *corev1.Text
	AccountClockSkewed    bool
	Removed               bool
	SecurityLoaded        bool
	LeaseEndMs            int64
	RecoveryPending       bool
	RecoveryReadyMs       int64
	RecoveryStatusStale   bool
	HasHandoffOffer       bool
	MachinesListed        bool
	SessionsListed        bool
	LiveLoaded            bool
	HasSessions           bool
	DisplayGrace          bool
	ReachabilityUnknown   bool
	ConnectionStartedAtMs int64
	NetworkAvailable      bool
	EverOnline            bool
	ConnectFailures       int32
	DevicesLoadFailed     bool
	DevicesErrorText      *corev1.Text
	LeaseRenewalAdvice    *corev1.Text
	Devices               []*SyncDevice
	DirectReadyMachines   map[string]bool
}
