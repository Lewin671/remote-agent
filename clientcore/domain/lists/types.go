// Package lists implements pure, cross-platform domain rules for sessions,
// projects, new-session configuration, models, modes, and pagination.
package lists

import v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"

// SessionTone classifies visual urgency on session and card badges.
type SessionTone uint8

const (
	ToneOff SessionTone = iota + 1
	ToneIdle
	ToneRun
	ToneWait
)

// SessionDisplayStatus is the unified status shown on session rows across all clients.
type SessionDisplayStatus uint8

const (
	StatusUnknown SessionDisplayStatus = iota
	StatusLoading
	StatusStarting
	StatusRunning
	StatusIdle
	StatusApproval
	StatusQuestion
	StatusPlan
	StatusPopup
	StatusOffline
	StatusEnded
	StatusUnlinked
)

// ApprovalAlwaysKind identifies structured CLI "always allow" permission scopes.
type ApprovalAlwaysKind uint8

const (
	AlwaysNone ApprovalAlwaysKind = iota
	AlwaysPrefix
	AlwaysDirectory
	AlwaysAutoEdits
)

// EndedGroupKind categorizes ended sessions by local calendar bucket.
type EndedGroupKind uint8

const (
	GroupToday EndedGroupKind = iota + 1
	GroupYesterday
	GroupWeek
	GroupMonth
)

// AutoIssueKind describes why automatic approval mode cannot currently start.
type AutoIssueKind uint8

const (
	AutoIssueNone AutoIssueKind = iota
	AutoIssueModel
	AutoIssueAccount
)

// ModeUnavailableReason explains why a particular approval mode cannot be chosen.
type ModeUnavailableReason uint8

const (
	ModeAvailable ModeUnavailableReason = iota
	ModeLockedFullAccess
	ModeFullAccessRequiresStart
	ModeModelNoAuto
	ModeAccountNoAuto
)

// ConnectionKind describes the client's transport reachability.
type ConnectionKind uint8

const (
	ConnConnected ConnectionKind = iota + 1
	ConnReconnecting
	ConnConnecting
	ConnOffline
	ConnNoNetwork
)

// PrefsOpKind selects an idempotent ProjectPrefs mutation.
type PrefsOpKind uint8

const (
	PrefsOpPin PrefsOpKind = iota + 1
	PrefsOpUnpin
	PrefsOpHide
	PrefsOpUnhide
	PrefsOpAdd
	PrefsOpSetSettings
)

// Machine represents one daemon computer known to the client.
type Machine struct {
	ID                  string
	Online              bool
	Reachable           *bool
	ReachabilityUnknown bool
	Unlinked            *bool
	Archived            bool
	LastSeenMs          int64
	ProtocolVersion     uint32
	DaemonOutOfDate     bool
	ClientOutOfDate     bool
	Meta                *v1.MachineMeta
}

// PendingQuestionOption captures one option from a live unanswered question.
type PendingQuestionOption struct {
	Label       string
	Description string
}

// PendingQuestionData captures details of the active unanswered question on a session.
type PendingQuestionData struct {
	QuestionID       string
	QuestionKey      string
	Header           string
	Text             string
	MultiSelect      bool
	QuestionCount    int32
	Options          []PendingQuestionOption
	CanFreeText      bool
	Async            bool
	ObservedEventSeq uint64
	AskedAtMs        int64
}

// PendingAction captures unresolved interactive state on a live session.
type PendingAction struct {
	_                   [0]func()
	HasApproval         bool
	ApprovalID          string
	ApprovalToolName    string
	ApprovalCommand     string
	ApprovalToolInput   string
	ApprovalDescription string
	ApprovalTruncated   bool
	ApprovalSkeleton    bool
	ApprovalDeadlineMs  int64
	HasQuestion         bool
	HasPlan             bool
	HasPopup            bool
	PopupPreview        string
	Question            *PendingQuestionData
}

// Session represents one session known to the client core.
type Session struct {
	_              [0]func()
	ID             string
	MachineID      string
	Info           *v1.Session
	UpdatedMs      int64
	ListedEndedMs  int64
	Preview        string
	Summary        string
	Pending        PendingAction
	ScheduleName   string
	ScheduleUnread bool
}

// MonthStart marks the Unix-millisecond boundary where a local calendar month begins.
type MonthStart struct {
	Year    int32
	Month   int32
	StartMs int64
}

// Calendar holds host-computed local calendar boundaries and current wall clock.
type Calendar struct {
	NowMs            int64
	TodayStartMs     int64
	YesterdayStartMs int64
	WeekStartMs      int64
	NowYear          int32
	UTCOffsetMinutes int32
	Months           []MonthStart
}

// World holds the domain inputs used to derive home, project, and new-session state.
type World struct {
	Machines            []*Machine
	MachinesListed      bool
	Sessions            []*Session
	SessionsListed      bool
	SessionsCursor      string
	SessionsNext        *SessionPlace
	HasMoreSessions     bool
	LoadingMoreSessions bool
	SessionListFailed   bool
	SessionListError    string
	LiveLoaded          bool
	Prefs               *v1.ProjectPrefs
	PrefsLoaded         bool
	PrefsLoadFailed     bool
	PrefsSyncing        bool
	Connection          ConnectionKind
	Unlinked            *bool
	ClientVersion       uint32
	IsManager           bool
	ManagerName         string
	AccountControlLost  bool
	AccountClockSkewed  bool
	MachineReplacements map[string]string
}

// ProjectAgg is the aggregated cross-machine summary of a project directory.
type ProjectAgg struct {
	Key        string
	Name       string
	ShortPath  string
	Machines   []string
	LiveCount  int32
	EndedTotal int32
	LatestMs   int64
	AnyOnline  bool
	Live       []*Session
	Ended      []*Session
	Prefs      []string
}

// Scope represents either "all projects" or one resolved project scope.
type Scope struct {
	AllProjects bool
	Key         string
	Name        string
	ShortPath   string
}

// EndedGroup describes a local calendar section heading for ended sessions.
type EndedGroup struct {
	Kind         EndedGroupKind
	Key          string
	Year         int32
	Month        int32
	WeekdayIndex int32
	RepMs        int64
}

// DefaultMode describes a machine/CLI's resolved default approval mode.
type DefaultMode struct {
	Mode   v1.ApprovalMode
	Known  bool
	Varies bool
}

// StartConfig holds concrete new-session configuration selections.
type StartConfig struct {
	_               [0]func()
	MachineID       string
	Cwd             string
	Agent           v1.Agent
	Model           string
	Effort          string
	Mode            v1.ApprovalMode
	StandardContext bool
}

// OfflineMachineChoice represents a fallback online computer when the preferred machine is offline.
type OfflineMachineChoice struct {
	MachineID string
	Name      string
	Config    StartConfig
	LastUsed  int64
}

// StartResolution is the resolved launch state for starting a new session in a project.
type StartResolution struct {
	Config          StartConfig
	Missing         v1.Agent
	ConfigFallback  bool
	DefaultMode     DefaultMode
	Issue           AutoIssueKind
	PendingSettled  bool
	OfflineDraft    bool
	Choices         []OfflineMachineChoice
	AllTriedOffline bool
}

// PrefsOp describes a single project-preference mutation.
type PrefsOp struct {
	Kind     PrefsOpKind
	Path     string
	Settings *v1.ProjectSettings
}

// SessionPlace identifies a pagination cursor boundary.
type SessionPlace struct {
	EndedMs   int64
	SessionID string
}
