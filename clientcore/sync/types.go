// Package sync implements the sans-IO client synchronization engine:
// connection lifecycle, backoff recovery, directory reconciliation,
// session event caching/gap-filling, live snapshot polling, prefs CAS,
// and command delivery state machines.
package sync

import (
	"strconv"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

const (
	// ProtocolVersion must match internal/protocol.Version (and web/android/ios).
	ProtocolVersion uint32 = 17

	MaxClockSkewMs             int64  = 300_000
	CommandTTLMs               int64  = 60_000
	ResultRetentionMs          int64  = 300_000
	LinkGraceMs                int64  = 10_000
	DisplayGraceMs             int64  = 1_500
	displayGraceTimerKey              = "display"
	coldStartGraceTimerKey            = "cold-start"
	StreamOpenTimeoutMs        int64  = 15_000
	TokenFastTimeoutMs         int64  = 3_000
	SecuritySyncBudgetMs       int64  = 5_000
	SessionPageLimit           uint32 = 50
	EventsPageLimit            uint32 = 500
	CacheEventPageSize         int32  = 128
	HistoryIdleMs              int64  = 60_000
	HistoryIdleCount                  = 8
	HistoryIdleBytes           int64  = 16 * 1024 * 1024
	HistorySweepIntervalMs     int64  = 15_000
	LivePollIntervalMs         int64  = 5_000
	LivenessTickMs             int64  = 1_000
	ListRetryInitialMs         int64  = 2_000
	ListRetryMaxMs             int64  = 30_000
	PrefsRetryInitialMs        int64  = 2_000
	PrefsRetryMaxMs            int64  = 30_000
	PrefsCASMaxAttempts               = 5
	CommandRetryMaxAttempts           = 3
	CommandUnavailableWindowMs int64  = 3_000
	CommandUnavailableRetryMs  int64  = 250
	ProjectPrefsObjectID              = "project-prefs"
	EntropyLowWater                   = 4
	EntropyRefillCount         int32  = 16
)

// DirtyFlags marks which screens may have changed as a result of a step.
type DirtyFlags struct {
	Home        bool
	Project     bool
	NewSession  bool
	SyncState   bool
	SessionData bool
}

// Add merges additional dirty screen markers (and marks SyncState when any view changes).
func (d *DirtyFlags) Add(home, project, newSession bool) {
	d.Home = d.Home || home
	d.Project = d.Project || project
	d.NewSession = d.NewSession || newSession
	if home || project || newSession {
		d.SyncState = true
	}
}

// MarkSyncState marks the transitional SyncState view dirty.
func (d *DirtyFlags) MarkSyncState() {
	d.SyncState = true
}

// MarkSessionData marks both SessionData and SyncState views dirty.
func (d *DirtyFlags) MarkSessionData() {
	d.SessionData = true
	d.SyncState = true
}

// MarkAll marks every managed screen and transitional view dirty.
func (d *DirtyFlags) MarkAll() {
	d.Home = true
	d.Project = true
	d.NewSession = true
	d.SyncState = true
	d.SessionData = true
}

// Any reports whether at least one screen or transitional view is dirty.
func (d DirtyFlags) Any() bool {
	return d.Home || d.Project || d.NewSession || d.SyncState || d.SessionData
}

// ScreenTargets returns ordered corev1.ScreenTarget entries for RenderEffect.
func (d DirtyFlags) ScreenTargets() []corev1.ScreenTarget {
	var out []corev1.ScreenTarget
	if d.Home {
		out = append(out, corev1.ScreenTarget_SCREEN_TARGET_HOME)
	}
	if d.Project {
		out = append(out, corev1.ScreenTarget_SCREEN_TARGET_PROJECT)
	}
	if d.NewSession {
		out = append(out, corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION)
	}
	if d.SyncState {
		out = append(out, corev1.ScreenTarget_SCREEN_TARGET_SYNC_STATE)
	}
	if d.SessionData {
		out = append(out, corev1.ScreenTarget_SCREEN_TARGET_SESSION_DATA)
	}
	return out
}

// MachineEntry holds plaintext machine state verified by the shell security gate.
// The embedded lists.Machine is the copy the world's machine list points at;
// its reachability fields are derived and refreshed by refreshWorld.
type MachineEntry struct {
	lists.Machine
	MetaRev    uint64
	MetaBroken bool
	CacheRef   string

	reachable bool
	unlinked  bool
}

// SessionEntry holds plaintext session state and derived checkpoint/reducer actions.
// The embedded lists.Session is the copy the world's session list points at.
type SessionEntry struct {
	lists.Session
	LastSeq           uint64
	ServerLastSeq     uint64
	MetaRev           uint64
	Checkpoint        *v1.LiveSnapshot
	CheckpointInvalid bool
	RetiredHistory    bool
	OpenFailed        bool
	CacheRef          string
}

// setInfo replaces the session metadata and the list preview derived from it.
func (s *SessionEntry) setInfo(info *v1.Session) {
	s.Info = info
	s.Preview = info.GetPreview()
}

// DeviceEntry holds plaintext device state.
type DeviceEntry struct {
	ID         string
	Kind       v1.DeviceKind
	CreatedMs  int64
	LastSeenMs int64
	Meta       *v1.DeviceMeta
	MetaRev    uint64
	MetaBroken bool
}

// PrefsState holds the decrypted project preferences and CAS revision.
type PrefsState struct {
	Loaded     bool
	Revision   uint64
	Prefs      *v1.ProjectPrefs
	Broken     bool
	LoadFailed bool
	CacheRef   string
}

// SecurityState mirrors the shell security gate snapshot relevant to sync and world state.
type SecurityState struct {
	Loaded                 bool
	IsMember               bool
	SelfDeviceID           string
	AuthorityDeviceID      string
	TrustedMachines        map[string]bool
	TrustedClients         map[string]bool
	ArchiveMachines        map[string]bool
	HeadEpoch              uint64
	LeaseEndMs             int64
	ClockSkewed            bool
	RecoveryPending        bool
	RecoveryReadyMs        int64
	RecoveryObjectsPending bool
	NetworkGen             uint64
	VerifiedNetGen         uint64
}

// SettledCommand notifies upper layers (e.g. app) when a dispatched command finishes.
type SettledCommand struct {
	RequestID string
	SlotKey   string
	MachineID string
	SessionID string
	Category  outcome.Category
	Result    *v1.CommandResult
	ErrorCode string
	ErrorText *corev1.Text
	Expired   bool
	Frozen    bool
}

// StepContext accumulates side effects, dirty flags, and settled command notifications for one Step call.
type StepContext struct {
	Raw     []*corev1.Effect
	Dirty   DirtyFlags
	Settled []SettledCommand
}

// StepResult summarizes the outputs of Engine.Step or direct Engine calls.
type StepResult struct {
	Effects []*corev1.Effect
	Dirty   DirtyFlags
	Settled []SettledCommand
}

// StepContext intentionally omits generic reflection; helper keeps Emit concise.
func newStepContext() *StepContext {
	return &StepContext{}
}

func (c *StepContext) emit(e *corev1.Effect) {
	if e != nil {
		c.Raw = append(c.Raw, e)
	}
}

type timerKey struct {
	kind corev1.TimerKind
	key  string
}

type timerEntry struct {
	id   uint64
	kind corev1.TimerKind
	key  string
}

type entropyPool struct {
	randomIDs     []string
	unitFloats    []float64
	fallbackSeq   uint64
	needRequested bool
}

func (p *entropyPool) push(ids []string, floats []float64) {
	for _, id := range ids {
		if id != "" {
			p.randomIDs = append(p.randomIDs, id)
		}
	}
	for _, f := range floats {
		if f < 0 {
			f = 0
		} else if f >= 1 {
			f = 0.999999999
		}
		p.unitFloats = append(p.unitFloats, f)
	}
}

func (p *entropyPool) nextID(ctx *StepContext) string {
	var out string
	if len(p.randomIDs) > 0 {
		out = p.randomIDs[0]
		p.randomIDs = p.randomIDs[1:]
	} else {
		p.fallbackSeq++
		out = "core-" + strconv.FormatUint(p.fallbackSeq, 10)
	}
	p.maybeRequest(ctx)
	return out
}

func (p *entropyPool) nextFloat(ctx *StepContext) float64 {
	var out float64
	if len(p.unitFloats) > 0 {
		out = p.unitFloats[0]
		p.unitFloats = p.unitFloats[1:]
	} else {
		// Golden ratio fractional progression avoids degenerate zero jitter without Go rand.
		p.fallbackSeq++
		f := float64((p.fallbackSeq*618033988749895)%1_000_000) / 1_000_000.0
		out = f
	}
	p.maybeRequest(ctx)
	return out
}

func (p *entropyPool) maybeRequest(ctx *StepContext) {
	if ctx == nil || p.needRequested {
		return
	}
	var needIDs, needFloats int32
	if len(p.randomIDs) < EntropyLowWater {
		needIDs = EntropyRefillCount
	}
	if len(p.unitFloats) < EntropyLowWater {
		needFloats = EntropyRefillCount
	}
	if needIDs > 0 || needFloats > 0 {
		p.needRequested = true
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_RequestEntropy{
				RequestEntropy: &corev1.RequestEntropyEffect{
					MinIds:    needIDs,
					MinFloats: needFloats,
				},
			},
		})
	}
}

// Engine holds the full sans-IO sync state machine for a single client account.
type Engine struct {
	// w is the core's single synchronized world. The engine is its only
	// writer; screens read it in place through World.
	w world.State
	// unlinkedFlag backs w.World.Unlinked; the order flags mark the world's
	// machine and session lists for re-sorting after inserts and removals.
	unlinkedFlag      bool
	machineOrderStale bool
	sessionOrderStale bool

	nowMs            int64
	monoMs           int64
	seenExplicitMono bool
	entropy          entropyPool

	running          bool
	foreground       bool
	networkAvailable bool
	networkSignature string
	networkGen       uint64
	removed          bool
	bootstrapped     bool

	lifecycleGen uint64
	connectGen   uint64
	nextOpSeq    uint64
	nextLegSeq   uint64
	nextTimerSeq uint64
	nextPushSeq  uint64
	nextEventRev uint64

	recovery              *Recovery
	liveness              *LegLiveness
	timers                map[timerKey]timerEntry
	everReady             bool
	displayGraceUntilMs   int64
	coldStartGraceUntilMs int64
	coldStartGraceExpired bool
	connecting            bool
	wantedConn            bool

	activeTokenOpID   uint64
	activeSecOpID     uint64
	lastSecPushSeq    uint64
	activeCacheBootID uint64
	cachedToken       string
	tokenExpiresMs    int64
	forceLogin        bool
	legID             uint64
	connStartedAtMs   int64
	connStepText      *corev1.Text
	serverAhead       bool
	listBackoffMs     int64
	prefsBackoffMs    int64

	machines        map[string]*MachineEntry
	sessions        map[string]*SessionEntry
	devices         map[string]*DeviceEntry
	listed          map[string]bool
	wantAllSessions bool

	listingGen         uint64
	refreshRun         uint64
	refreshOpID        uint64
	refreshSnapshotM   map[string]uint64
	refreshSnapshotS   map[string]uint64
	refreshSnapshotR   map[string]uint64
	pageOpID           uint64
	pageListingGen     uint64
	devicesOpID        uint64
	lastDevicesPushSeq uint64
	devicesSnapshotR   map[string]uint64
	sessionByIDOps     map[uint64]string
	sessionByIDFailed  map[string]bool
	machinePushes      map[string]uint64
	sessionPushes      map[string]uint64
	removedPushes      map[string]uint64
	devicePushes       map[string]uint64
	directReady        map[string]bool

	buffers          map[string]*EventBuffer
	watchedSessions  map[string]bool
	pinnedSessions   map[string]bool
	livePollInFlight map[string]string // session_id -> active request_id
	livePollReqToSid map[string]string // request_id -> session_id

	prefs         PrefsState
	prefsLoadOpID uint64
	prefsQueue    []prefsQueueItem

	security       SecurityState
	homeScopeToken string
	scopeHMACKey   []byte

	inFlightCmds    map[string]*PreparedCommand
	reqIDByAttempt  map[string]string
	attemptTrackers map[string]*AttemptTracker
	retryReqByTimer map[string]string // timer key -> request_id
}

// NewEngine constructs a fresh sans-IO sync Engine.
func NewEngine() *Engine {
	e := &Engine{
		foreground:        true,
		networkAvailable:  true,
		recovery:          NewRecovery(NewRecoveryBudget(4.0, 2.0)),
		timers:            make(map[timerKey]timerEntry),
		listBackoffMs:     ListRetryInitialMs,
		prefsBackoffMs:    PrefsRetryInitialMs,
		machines:          make(map[string]*MachineEntry),
		sessions:          make(map[string]*SessionEntry),
		devices:           make(map[string]*DeviceEntry),
		listed:            make(map[string]bool),
		sessionByIDOps:    make(map[uint64]string),
		sessionByIDFailed: make(map[string]bool),
		machinePushes:     make(map[string]uint64),
		sessionPushes:     make(map[string]uint64),
		removedPushes:     make(map[string]uint64),
		devicePushes:      make(map[string]uint64),
		directReady:       make(map[string]bool),
		buffers:           make(map[string]*EventBuffer),
		watchedSessions:   make(map[string]bool),
		pinnedSessions:    make(map[string]bool),
		livePollInFlight:  make(map[string]string),
		livePollReqToSid:  make(map[string]string),
		inFlightCmds:      make(map[string]*PreparedCommand),
		reqIDByAttempt:    make(map[string]string),
		attemptTrackers:   make(map[string]*AttemptTracker),
		retryReqByTimer:   make(map[string]string),
	}
	e.w.Init()
	e.w.World.ClientVersion = ProtocolVersion
	e.w.World.Unlinked = &e.unlinkedFlag
	e.refreshWorld()
	return e
}

func (e *Engine) allocOpID() uint64 {
	e.nextOpSeq++
	return e.nextOpSeq
}

func (e *Engine) allocLegID() uint64 {
	e.nextLegSeq++
	return e.nextLegSeq
}

func (e *Engine) advanceClocks(nowMs, monoMs int64, cal *corev1.LocalCalendar) {
	prevNow := e.nowMs
	if nowMs > 0 {
		e.nowMs = nowMs
		e.w.Cal.NowMs = nowMs
	}
	if monoMs > 0 {
		if !e.seenExplicitMono {
			e.seenExplicitMono = true
			e.monoMs = monoMs
		} else if monoMs >= e.monoMs {
			e.monoMs = monoMs
		}
	} else if !e.seenExplicitMono && nowMs > 0 && prevNow > 0 && nowMs > prevNow {
		e.monoMs += nowMs - prevNow
	}
	if cal != nil {
		if cal.NowMs > 0 {
			e.nowMs = cal.NowMs
		}
		months := make([]lists.MonthStart, 0, len(cal.Months))
		for _, m := range cal.Months {
			if m != nil {
				months = append(months, lists.MonthStart{
					Year:    m.Year,
					Month:   m.Month,
					StartMs: m.StartMs,
				})
			}
		}
		e.w.Cal = lists.Calendar{
			NowMs:            e.nowMs,
			TodayStartMs:     cal.TodayStartMs,
			YesterdayStartMs: cal.YesterdayStartMs,
			WeekStartMs:      cal.WeekStartMs,
			NowYear:          cal.NowYear,
			Months:           months,
		}
	}
}

// NowMs returns the current host wall-clock timestamp known to the engine.
func (e *Engine) NowMs() int64 { return e.nowMs }

// MonoMs returns the current host monotonic timestamp known to the engine.
func (e *Engine) MonoMs() int64 { return e.monoMs }

// Calendar returns the current local calendar boundaries.
func (e *Engine) Calendar() lists.Calendar { return e.w.Cal }

// HandleEvent processes sync-level fields of ev (clocks, entropy, lifecycle, network,
// timers, cache, auth, security, stream, RPC, commands, watch, pin, route/schedule hints).
// It returns whether the event was consumed by sync (ui-screen events return false).
func (e *Engine) HandleEvent(ev *corev1.Event) (consumed bool, res StepResult) {
	consumed, res = e.handleEvent(ev)
	e.refreshWorld()
	return consumed, res
}

func (e *Engine) handleEvent(ev *corev1.Event) (consumed bool, res StepResult) {
	ctx := newStepContext()
	e.entropy.needRequested = false
	if ev == nil {
		return false, StepResult{}
	}
	var cal *corev1.LocalCalendar
	if c := ev.GetClockUpdated(); c != nil {
		cal = c
	}
	e.advanceClocks(ev.NowMs, ev.MonoMs, cal)
	e.expireDisplayGraceOnClock(ctx)
	e.entropy.push(ev.RandomIds, ev.UnitFloats)
	if ep := ev.GetEntropyProvided(); ep != nil {
		e.entropy.push(ep.RandomIds, ep.UnitFloats)
		return true, finalizeResult(ctx)
	}
	if cal != nil {
		e.checkLeaseClock(ctx)
		ctx.Dirty.Add(true, true, false)
		return true, finalizeResult(ctx)
	}

	consumed = true
	switch k := ev.Event.(type) {
	case *corev1.Event_SyncLifecycle:
		e.handleLifecycle(ctx, k.SyncLifecycle)
	case *corev1.Event_NetworkChanged:
		e.handleNetworkChanged(ctx, k.NetworkChanged)
	case *corev1.Event_TimerFired:
		if k.TimerFired != nil && isAppTimerKind(k.TimerFired.Kind) {
			consumed = false
		} else {
			e.handleTimerFired(ctx, k.TimerFired)
		}
	case *corev1.Event_CacheBootstrapLoaded:
		e.handleCacheBootstrapLoaded(ctx, k.CacheBootstrapLoaded)
	case *corev1.Event_CacheEventsLoaded:
		e.handleCacheEventsLoaded(ctx, k.CacheEventsLoaded)
	case *corev1.Event_TokenResult:
		e.handleTokenResult(ctx, k.TokenResult)
	case *corev1.Event_SecuritySnapshot:
		e.handleSecuritySnapshot(ctx, k.SecuritySnapshot)
	case *corev1.Event_StreamReady:
		e.handleStreamReady(ctx, k.StreamReady)
	case *corev1.Event_StreamClosed:
		e.handleStreamClosed(ctx, k.StreamClosed)
	case *corev1.Event_StreamFrame:
		e.handleStreamFrame(ctx, k.StreamFrame)
	case *corev1.Event_ListsFetched:
		e.handleListsFetched(ctx, k.ListsFetched)
	case *corev1.Event_SessionsPageFetched:
		e.handleSessionsPageFetched(ctx, k.SessionsPageFetched)
	case *corev1.Event_SessionByIdFetched:
		e.handleSessionByIDFetched(ctx, k.SessionByIdFetched)
	case *corev1.Event_EventsPageFetched:
		e.handleEventsPageFetched(ctx, k.EventsPageFetched)
	case *corev1.Event_AccountPrefsFetched:
		e.handleAccountPrefsFetched(ctx, k.AccountPrefsFetched)
	case *corev1.Event_AccountPrefsPutResult:
		e.handleAccountPrefsPutResult(ctx, k.AccountPrefsPutResult)
	case *corev1.Event_DevicesFetched:
		e.handleDevicesFetched(ctx, k.DevicesFetched)
	case *corev1.Event_CommandOutcome:
		e.handleCommandOutcome(ctx, k.CommandOutcome)
	case *corev1.Event_SessionWatch:
		e.handleSessionWatch(ctx, k.SessionWatch)
	case *corev1.Event_SessionPin:
		e.handleSessionPin(ctx, k.SessionPin)
	case *corev1.Event_RetrySessionEvents:
		if k.RetrySessionEvents != nil {
			e.retryEvents(ctx, k.RetrySessionEvents.SessionId)
		}
	case *corev1.Event_FetchSession:
		if k.FetchSession != nil {
			e.fetchSessionByID(ctx, k.FetchSession.SessionId)
		}
	case *corev1.Event_MachineRouteStatus:
		e.handleMachineRouteStatus(ctx, k.MachineRouteStatus)
	case *corev1.Event_SessionScheduleHints:
		e.handleScheduleHints(ctx, k.SessionScheduleHints)
	case *corev1.Event_DirectEventsReceived:
		e.handleDirectEvents(ctx, k.DirectEventsReceived)
	default:
		consumed = false
	}
	return consumed, finalizeResult(ctx)
}

func finalizeResult(ctx *StepContext) StepResult {
	return StepResult{
		Effects: ctx.Raw,
		Dirty:   ctx.Dirty,
		Settled: ctx.Settled,
	}
}

// NextID allocates the next deterministic ID from the engine entropy pool.
func (e *Engine) NextID(ctx *StepContext) string {
	return e.entropy.nextID(ctx)
}
