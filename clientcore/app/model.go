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
)

type cmdMeta struct {
	kind       cmdKind
	machineID  string
	agent      v1.Agent
	projectKey string
}

// Model holds the sync engine, screen state, and pending UI command metadata.
type Model struct {
	sync           *sync.Engine
	state          *screens.State
	pendingCmdMeta map[string]cmdMeta
}

func newModel() *Model {
	return &Model{
		sync:           sync.NewEngine(),
		state:          screens.NewState(),
		pendingCmdMeta: make(map[string]cmdMeta),
	}
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
	return m.state.StandaloneProjectKey
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
	if d != nil && d.ConfigInitialized {
		pickMachine = d.Config.MachineID
		pickAgent = d.AgentOverride
	}
	res := lists.ResolveProjectStart(&m.state.World, key, pickMachine, pickAgent, nil, false)
	if d != nil && !d.ConfigInitialized && res.Found {
		d.Config = res.Config
		d.ConfigInitialized = true
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

func dirEntriesFromPaths(dirs []string, parent string) []*corev1.DirEntryView {
	out := make([]*corev1.DirEntryView, 0, len(dirs))
	for _, full := range dirs {
		if full == "" {
			continue
		}
		name := path.Base(full)
		if name == "." || name == "/" {
			name = full
		}
		out = append(out, &corev1.DirEntryView{
			Name: name,
			Path: full,
		})
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
