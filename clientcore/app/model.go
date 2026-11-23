package app

import (
	"path"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/sync"
)

type cmdKind uint8

const (
	cmdKindListModels cmdKind = iota + 1
	cmdKindGetSettings
	cmdKindListDirs
	cmdKindCreateSession
	cmdKindScheduleList
	cmdKindScheduleCreate
	cmdKindScheduleOperate
	cmdKindScheduleUpdate
	cmdKindToggleMachineAgent
	cmdKindGetUsageStats
)

type cmdMeta struct {
	_             [0]func()
	kind          cmdKind
	machineID     string
	agent         v1.Agent
	projectKey    string
	taskID        string
	formKey       string
	opName        string
	revision      uint64
	usageKey      screens.UsageCacheKey
	toggleEnabled bool
	pickerKey     string
}

type scheduleDirty struct {
	list   bool
	detail bool
	form   bool
	strip  bool
}

type frozenSchedOp struct {
	machineID string
	cmd       *v1.Command
	meta      cmdMeta
}

// Model holds the sync engine, screen state, and pending UI command metadata.
// state reads the engine's world in place; only the engine writes it.
type Model struct {
	sync                  *sync.Engine
	state                 *screens.State
	pendingCmdMeta        map[string]cmdMeta
	settings              *screens.SettingsExtra
	settingsDirty         settingsDirtyFlags
	sessions              *sessionStore
	schedActive           bool
	schedDirty            scheduleDirty
	nextScheduleCacheOpID uint64
	frozenSchedOps        map[string]frozenSchedOp
	usageDirty            bool
	usageActive           bool
	lastUsageFetchedMs    map[screens.UsageCacheKey]int64
	activeUsageReq        *corev1.UsageScreenRequest
	lastUsageParams       map[string]*corev1.UsageScreenOpenEvent
}

func newModel() *Model {
	eng := sync.NewEngine()
	m := &Model{
		sync:               eng,
		state:              screens.NewState(eng.World()),
		pendingCmdMeta:     make(map[string]cmdMeta),
		frozenSchedOps:     make(map[string]frozenSchedOp),
		lastUsageFetchedMs: make(map[screens.UsageCacheKey]int64),
		lastUsageParams:    make(map[string]*corev1.UsageScreenOpenEvent),
	}
	m.state.Schedules.SessionEvents = func(sid string) ([]*v1.Event, bool) {
		if sid == "" {
			return nil, false
		}
		var events []*v1.Event
		if buf := m.sync.Buffer(sid); buf != nil {
			events = buf.ShownEvents()
		}
		return events, m.sync.SessionEventsSettled(sid)
	}
	m.state.DirectReportFn = func(machineID string) (bool, *corev1.DirectRouteReport, bool) {
		prefEnabled, daemonDisabled, status, rounds, _, _ := m.sync.DirectMachineReport(machineID)
		return prefEnabled, screens.BuildDirectRouteReport(status, prefEnabled, daemonDisabled, len(rounds)), true
	}
	return m
}

func (m *Model) ensureDraft(key string) *screens.NewSessionDraft {
	if key == "" {
		return nil
	}
	d := m.state.NewSessions[key]
	if d == nil {
		d = &screens.NewSessionDraft{ProjectKey: key}
		m.state.NewSessions[key] = d
	}
	return d
}

func (m *Model) activeNewSessionKey(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return screens.StandaloneNewSessionKey(m.state)
}

func (m *Model) findMachine(id string) *lists.Machine {
	for _, mac := range m.state.World.Machines {
		if mac != nil && mac.ID == id {
			return mac
		}
	}
	return nil
}

func (m *Model) resolveDraftConfig(key string, d *screens.NewSessionDraft) lists.ProjectStartResult {
	var pickMachine string
	var pickAgent v1.Agent
	if d != nil && (d.ConfigChosen || d.MachineChosen) {
		pickMachine = d.Config.MachineID
	}
	if d != nil && d.ConfigChosen {
		pickAgent = d.AgentOverride
		if pickAgent == v1.Agent_AGENT_UNSPECIFIED {
			pickAgent = d.Config.Agent
		}
	}
	res := lists.ResolveProjectStart(&m.state.World, key, pickMachine, pickAgent, nil, false)
	if d != nil && res.Found {
		res.Config.Agent = lists.EffectiveAgent(res.Config.Agent)
		if !d.ConfigInitialized || (!d.ConfigChosen && d.Prompt == "" && d.AttachmentCount == 0 && !d.Busy && !d.Frozen) {
			d.Config = res.Config
			d.ConfigInitialized = true
		}
	}
	if d != nil && d.ConfigInitialized {
		d.Config.Agent = lists.EffectiveAgent(d.Config.Agent)
		if !d.Busy && !d.Frozen && d.Config.MachineID != "" {
			if mac := m.findMachine(d.Config.MachineID); mac != nil && lists.IsAgentDisabled(mac.Meta, d.Config.Agent) {
				d.DisabledFromAgent = d.Config.Agent
				enabled := lists.MachineAgents(mac.Meta)
				nextAgent := d.Config.Agent
				if len(enabled) > 0 {
					nextAgent = enabled[0]
				}
				if nextAgent != d.Config.Agent {
					fb := lists.ResolveProjectStart(&m.state.World, key, d.Config.MachineID, nextAgent, nil, false)
					d.Config = lists.SwitchAgent(fb.Config, &d.Config, nextAgent)
					d.Config.Agent = lists.EffectiveAgent(d.Config.Agent)
					d.AgentOverride = d.Config.Agent
				}
			}
		}
	}
	return res
}

func (m *Model) maybeFetchMachineConfig(ctx *sync.StepContext, machineID string, agent v1.Agent) {
	if machineID == "" || m.state.World.Connection != lists.ConnConnected {
		return
	}
	mac := m.findMachine(machineID)
	if !lists.MachineReachable(mac) {
		return
	}

	effAgent := lists.EffectiveAgent(agent)
	k := screens.MachineAgentKey{MachineID: machineID, Agent: effAgent}
	me := m.state.MachineModels[k]
	if me == nil {
		me = &screens.ModelsCacheEntry{}
		m.state.MachineModels[k] = me
	}
	if !me.Loaded && !me.Loading && me.Error == "" && me.ErrorText == nil {
		me.Loading = true
		reqID, ok := m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
			MachineID: machineID,
			Command: &v1.Command{
				Kind: &v1.Command_ListModels{
					ListModels: &v1.ListModelsRequest{Agent: effAgent},
				},
			},
		})
		if reqID != "" {
			m.pendingCmdMeta[reqID] = cmdMeta{
				kind:      cmdKindListModels,
				machineID: machineID,
				agent:     effAgent,
			}
		}
		if !ok {
			me.Loading = false
		}
	}

	se := m.state.MachineSettings[machineID]
	if se == nil {
		se = &screens.SettingsCacheEntry{}
		m.state.MachineSettings[machineID] = se
	}
	if !se.Loaded && !se.Loading && se.Error == "" && se.ErrorText == nil {
		se.Loading = true
		reqID, ok := m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
			MachineID: machineID,
			Command: &v1.Command{
				Kind: &v1.Command_GetSettings{
					GetSettings: &v1.GetSettingsRequest{},
				},
			},
		})
		if reqID != "" {
			m.pendingCmdMeta[reqID] = cmdMeta{
				kind:      cmdKindGetSettings,
				machineID: machineID,
			}
		}
		if !ok {
			se.Loading = false
		}
	}
}

func (m *Model) syncDraftAutoSupport(key string, res lists.ProjectStartResult) {
	d := m.state.NewSessions[key]
	if d == nil {
		return
	}
	modelsEntry := m.state.MachineModels[screens.MachineAgentKey{
		MachineID: d.Config.MachineID,
		Agent:     lists.EffectiveAgent(d.Config.Agent),
	}]
	settingsEntry := m.state.MachineSettings[d.Config.MachineID]
	var models *v1.ListModelsResponse
	var hasModels bool
	if modelsEntry != nil && modelsEntry.Loaded {
		models = modelsEntry.Response
		hasModels = models != nil
	}
	var settings *v1.DaemonSettings
	if settingsEntry != nil && settingsEntry.Loaded {
		settings = settingsEntry.Settings
	}
	if d.Config.MachineID != "" && d.Config.Model == "" && d.Config.Effort == "" && models != nil {
		if picked := lists.ProjectModel(d.Config, models, settings); picked != nil {
			d.Config = lists.PickModel(d.Config, picked)
		}
	}
	if len(models.GetModels()) > 0 || settings != nil {
		_, effort := lists.EffortChoices(d.Config, models, settings)
		if d.Config.Effort == "" && effort != "" {
			d.Config.Effort = effort
		}
	}
	_ = hasModels
}

func (m *Model) decodeScopeIfReady() {
	if m.state.RememberedScope != nil || m.state.RememberedScopeToken == "" {
		return
	}
	key := m.sync.ScopeHMACKey()
	if len(key) == 0 {
		return
	}
	projects := lists.BuildProjects(&m.state.World, "", nil, "", nil, nil)
	if decoded := lists.DecodeScope(m.state.RememberedScopeToken, key, projects.Listed); decoded != nil {
		m.state.RememberedScope = decoded
		if m.state.Scope == nil {
			m.state.Scope = decoded
		}
	}
}

// adoptHomeScopeToken seeds the remembered home scope from the token the shell
// restored at startup, until the user picks a scope of their own.
func (m *Model) adoptHomeScopeToken() {
	if m.state.RememberedScopeToken == "" {
		m.state.RememberedScopeToken = m.sync.HomeScopeToken()
	}
}

// dirEntriesFromNames turns the folder names of one listed directory into
// entries whose paths are absolute, as the daemon requires for the next listing.
func dirEntriesFromNames(names []string, dir string) []*corev1.DirEntryView {
	out := make([]*corev1.DirEntryView, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		out = append(out, &corev1.DirEntryView{Name: name, Path: path.Join(dir, name)})
	}
	return out
}

func renderEffect(dirty sync.DirtyFlags) *corev1.Effect {
	targets := dirty.ScreenTargets()
	if len(targets) == 0 {
		return nil
	}
	return &corev1.Effect{
		Effect: &corev1.Effect_Render{
			Render: &corev1.RenderEffect{Screens: targets},
		},
	}
}

func buildFullSettingsOp(cfg lists.StartConfig, moved bool) *lists.FullPrefsOp {
	effAgent := lists.EffectiveAgent(cfg.Agent)
	return &lists.FullPrefsOp{
		Kind: lists.PrefsOpSetSettings,
		Path: cfg.Cwd,
		SetSettings: &lists.PrefsSetSettingsOp{
			Path:      cfg.Cwd,
			MachineID: cfg.MachineID,
			Moved:     moved,
			Agent:     &effAgent,
			Sets: []*v1.AgentSettings{
				{
					Agent:           effAgent,
					Model:           cfg.Model,
					Effort:          cfg.Effort,
					Mode:            cfg.Mode,
					StandardContext: cfg.StandardContext,
				},
			},
		},
	}
}
