// Package screens builds screen-ready ViewModels from core domain state using localizable Text messages.
package screens

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
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
	ProjectKey           string
	Config               lists.StartConfig
	ConfigInitialized    bool
	ConfigChosen         bool
	MachineChosen        bool
	ResetOnNextOpen      bool
	AgentOverride        v1.Agent
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
	ConfigSaveError      string
	ConfigSaving         bool
	AutoModeSupported    bool
}

// State is the full semantic client state snapshot owned by Core and rendered by screens.
type State struct {
	World                lists.World
	Cal                  lists.Calendar
	DisplayGrace         bool
	ConnectionReason     string
	ConnectionReasonText *corev1.Text
	SessionListErrorText *corev1.Text
	ManagerNameText      *corev1.Text
	DisconnectedAtMs     int64
	LivePushConnected    bool

	RememberedScope       *lists.Scope
	RememberedScopeToken  string
	Scope                 *lists.Scope
	HomeFilterQuery       string
	HomeSelectedSessionID string

	ProjectSearch            string
	ProjectChangedElsewhere  bool
	PrefsPendingPath         map[string]bool
	PrefsSaveErrorByPath     map[string]string
	PrefsSaveErrorTextByPath map[string]*corev1.Text
	PrefsGlobalError         string
	PrefsGlobalErrorText     *corev1.Text
	PrefsSavingSettings      map[string]bool
	PrefsSettingsError       map[string]string
	PrefsSettingsErrorText   map[string]*corev1.Text

	DirPicker DirPickerState

	MachineModels   map[MachineAgentKey]*ModelsCacheEntry
	MachineSettings map[string]*SettingsCacheEntry

	StandaloneProjectKey string
	NewSessions          map[string]*NewSessionDraft

	Schedules *ScheduleState
}

// NewState constructs an initialized State with empty lookup maps.
func NewState() *State {
	return &State{
		World: lists.World{
			Connection: lists.ConnConnecting,
		},
		PrefsPendingPath:         make(map[string]bool),
		PrefsSaveErrorByPath:     make(map[string]string),
		PrefsSaveErrorTextByPath: make(map[string]*corev1.Text),
		PrefsSavingSettings:      make(map[string]bool),
		PrefsSettingsError:       make(map[string]string),
		PrefsSettingsErrorText:   make(map[string]*corev1.Text),
		MachineModels:            make(map[MachineAgentKey]*ModelsCacheEntry),
		MachineSettings:          make(map[string]*SettingsCacheEntry),
		NewSessions:              make(map[string]*NewSessionDraft),
		Schedules:                NewScheduleState(),
	}
}
