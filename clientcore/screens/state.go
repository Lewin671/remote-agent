// Package screens builds screen-ready ViewModels from core domain state using localizable Text messages.
package screens

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

// MachineAgentKey indexes per-machine, per-agent cached model lists.
type MachineAgentKey struct {
	MachineID string
	Agent     v1.Agent
}

// ModelsCacheEntry holds the fetched model list and loading/error state for one machine and agent.
type ModelsCacheEntry struct {
	Response  *v1.ListModelsResponse
	Loaded    bool
	Loading   bool
	Error     string
	ErrorText *corev1.Text
}

// SettingsCacheEntry holds the fetched DaemonSettings and loading/error state for one machine.
type SettingsCacheEntry struct {
	Settings  *v1.DaemonSettings
	Loaded    bool
	Loading   bool
	Error     string
	ErrorText *corev1.Text
}

// UsageCacheKey indexes passive usage snapshots per machine and range/custom interval.
type UsageCacheKey struct {
	MachineID     string
	Preset        v1.UsageRangePreset
	CustomStartMs int64
	CustomEndMs   int64
}

// UsageUIState tracks per-machine Usage & Speed sort, single-model selection, and scrub state.
type UsageUIState struct {
	SortBy               corev1.UsageSortColumn
	SelectedSeriesKey    string
	HasSelectionOverride bool
	HasScrub             bool
	ScrubBucketIndex     int32
}

// UsageSnapshotEntry holds live and offline-cached usage telemetry for one machine+range.
type UsageSnapshotEntry struct {
	Live        *v1.UsageStatsResponse
	LiveAtMs    int64
	Cached      *v1.UsageStatsResponse
	CachedAtMs  int64
	Loading     bool
	ActiveReqID string
	Error       string
	ErrorText   *corev1.Text
}

// DirPickerState holds the directory chooser state used by Manage Projects and New Task.
type DirPickerState struct {
	Open            bool
	ForNewTask      bool
	MachineID       string
	CurrentPath     string
	ParentPath      string
	Dirs            []*corev1.DirEntryView
	Loading         bool
	Error           string
	ErrorText       *corev1.Text
	ActiveRequestID string
}

// NewSessionDraft tracks per-project composer selection and launch attempt state.
type NewSessionDraft struct {
	_                    [0]func()
	ProjectKey           string
	Config               lists.StartConfig
	ConfigInitialized    bool
	ConfigChosen         bool
	MachineChosen        bool
	ResetOnNextOpen      bool
	AgentOverride        v1.Agent
	DisabledFromAgent    v1.Agent
	Prompt               string
	AttachmentCount      int32
	AttachmentsMoved     bool
	Busy                 bool
	Frozen               bool
	Expired              bool
	UnknownStart         bool
	StartError           string
	StartErrorText       *corev1.Text
	DiskFull             bool
	ActiveStartRequestID string
	AutoModeSupported    bool
}

// State is what the screen builders render from: the core's synchronized
// world, which the sync engine owns and screens only read, plus the
// screen-owned UI state (scope, search, drafts, pickers and fetched caches).
type State struct {
	*world.State

	RememberedScope       *lists.Scope
	RememberedScopeToken  string
	Scope                 *lists.Scope
	HomeFilterQuery       string
	HomeSelectedSessionID string

	ProjectSearch string

	DirPicker DirPickerState

	MachineModels   map[MachineAgentKey]*ModelsCacheEntry
	MachineSettings map[string]*SettingsCacheEntry
	UsageSnapshots  map[UsageCacheKey]*UsageSnapshotEntry
	UsageUI         map[string]*UsageUIState

	StandaloneProjectKey string
	NewSessions          map[string]*NewSessionDraft

	Schedules *ScheduleState

	// DirectReportFn returns the live direct-path preference & report when transport is owned by Core.
	DirectReportFn func(machineID string) (enabled bool, report *corev1.DirectRouteReport, ok bool)
}

// NewState constructs a State over w with empty UI lookup maps. A nil w gets a
// fresh, empty world, which is what builders see before any sync.
func NewState(w *world.State) *State {
	if w == nil {
		w = world.New()
	}
	return &State{
		State:           w,
		MachineModels:   make(map[MachineAgentKey]*ModelsCacheEntry),
		MachineSettings: make(map[string]*SettingsCacheEntry),
		UsageSnapshots:  make(map[UsageCacheKey]*UsageSnapshotEntry),
		UsageUI:         make(map[string]*UsageUIState),
		NewSessions:     make(map[string]*NewSessionDraft),
		Schedules:       NewScheduleState(),
	}
}
