// Package world holds the client core's synchronized world. The sync engine
// owns the single instance and is the only writer; screen builders read it in
// place, so nothing copies it between Dispatch and View.
package world

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
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
	Projection           *corev1.ProjectionState
	Events               []*v1.Event
}
