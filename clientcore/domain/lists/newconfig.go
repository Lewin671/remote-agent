package lists

import (
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

// OfflineMachineOption describes an online fallback machine that has previously used a project.
type OfflineMachineOption struct {
	ID              string
	Name            string
	InstalledAgents []v1.Agent
	Config          StartConfig
	FallbackAgent   v1.Agent
}

// ProjectStartResult holds the resolved launch configuration and diagnostics for a project.
type ProjectStartResult struct {
	Found              bool
	NeedsMachineChoice bool
	Config             StartConfig
	Settled            bool
	WantedAgent        v1.Agent
	DisabledAgent      v1.Agent
	MissingAgent       v1.Agent
	DefaultMode        DefaultMode
	ProtocolMismatch   int32
	OfflineChoices     []OfflineMachineOption
	ProjectMachines    []string
}

type inheritance struct {
	all       []*Session
	sure      []*Session
	unfetched bool
	unsettled bool
}

func newInheritance(w *World, allProjects []*projectAgg, key string) *inheritance {
	var all []*Session
	for _, s := range w.Sessions {
		if s == nil {
			continue
		}
		if (s.Info == nil || !s.Info.TaskOwned) && ProjectKey(s) == key {
			all = append(all, s)
		}
	}
	sortutil.Sort(len(all), func(i, j int) bool { return CompareSession(all[i], all[j]) < 0 }, func(i, j int) { all[i], all[j] = all[j], all[i] })

	var sure []*Session
	var exitedCount int32
	for _, s := range all {
		if !PastFetched(w, s) {
			sure = append(sure, s)
		}
		if Exited(s) {
			exitedCount++
		}
	}

	var projEnded int32
	for _, p := range allProjects {
		if p.key == key {
			projEnded = p.ended
			break
		}
	}
	unfetched := MoreSessions(w) && projEnded > exitedCount
	return &inheritance{
		all:       all,
		sure:      sure,
		unfetched: unfetched,
	}
}

func (in *inheritance) first(match func(*Session) bool) *Session {
	for _, s := range in.sure {
		if match == nil || match(s) {
			return s
		}
	}
	if in.unfetched {
		in.unsettled = true
	}
	for _, s := range in.all {
		if match == nil || match(s) {
			return s
		}
	}
	return nil
}

func wantedAgent(from *inheritance, pick v1.Agent, saved *v1.ProjectSettings) v1.Agent {
	if pick != v1.Agent_AGENT_UNSPECIFIED {
		return pick
	}
	if saved != nil && IsKnownAgent(saved.Agent) {
		return saved.Agent
	}
	if s := from.first(nil); s != nil {
		return SessionAgent(s)
	}
	return v1.Agent_AGENT_UNSPECIFIED
}

func agentSet(from *inheritance, agent v1.Agent, saved *v1.ProjectSettings) *v1.AgentSettings {
	want := EffectiveAgent(agent)
	if saved != nil {
		var fallback *v1.AgentSettings
		for _, s := range saved.Agents {
			if s == nil {
				continue
			}
			if s.Agent == want {
				out := s.Clone()
				out.Mode = v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
				return out
			}
			if fallback == nil && EffectiveAgent(s.Agent) == want {
				fallback = s
			}
		}
		if fallback != nil {
			out := fallback.Clone()
			out.Agent = want
			out.Mode = v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
			return out
		}
	}
	out := &v1.AgentSettings{Agent: want}
	if last := from.first(func(s *Session) bool {
		return SessionAgent(s) == want
	}); last != nil && last.Info != nil {
		out.Model = last.Info.Model
		out.Effort = last.Info.Effort
	}
	return out
}

func configFromAgentSettings(machineID, cwd string, s *v1.AgentSettings) StartConfig {
	if s == nil {
		return StartConfig{MachineID: machineID, Cwd: cwd, Agent: DefaultAgent}
	}
	eff := EffectiveAgent(s.Agent)
	return StartConfig{
		MachineID:       machineID,
		Cwd:             cwd,
		Agent:           eff,
		Model:           s.Model,
		Effort:          s.Effort,
		StandardContext: AgentInfo(eff).StandardContextOption && s.StandardContext,
	}
}

func settingsFromConfig(c StartConfig) *v1.AgentSettings {
	eff := EffectiveAgent(c.Agent)
	return &v1.AgentSettings{
		Agent:           eff,
		Model:           c.Model,
		Effort:          c.Effort,
		Mode:            v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED,
		StandardContext: AgentInfo(eff).StandardContextOption && c.StandardContext,
	}
}

// ResolveMachineReplacement follows verified machine replacement proofs
// (with cycle protection) to return the latest replacement machine ID.
func ResolveMachineReplacement(w *World, id string) string {
	if id == "" || w == nil || len(w.MachineReplacements) == 0 {
		return id
	}
	seen := make(map[string]bool, 4)
	cur := id
	for {
		seen[cur] = true
		next := w.MachineReplacements[cur]
		if next == "" || seen[next] {
			break
		}
		cur = next
	}
	return cur
}

// ResolveProjectStart resolves the initial StartConfig, settled flag, offline machine choices,
// default approval mode, and missing-agent warning for a project.
func ResolveProjectStart(w *World, key, pickMachineID string, pickAgent v1.Agent, overrideSaved *v1.ProjectSettings, useOverrideSaved bool) ProjectStartResult {
	var res ProjectStartResult
	if w == nil {
		return res
	}
	allProjects, machinesByID := aggregateProjects(w)

	var pr *projectAgg
	for _, p := range allProjects {
		if p.key == key {
			pr = p
			break
		}
	}

	var projectMachines []string
	if pr != nil && len(pr.machineIDs) > 0 {
		projectMachines = slices.Clone(pr.machineIDs)
	} else {
		for mid := range machinesByID {
			projectMachines = append(projectMachines, mid)
		}
		sortutil.Sort(len(projectMachines), func(i, j int) bool { return compareUTF16(projectMachines[i], projectMachines[j]) < 0 }, func(i, j int) { projectMachines[i], projectMachines[j] = projectMachines[j], projectMachines[i] })
	}
	res.ProjectMachines = projectMachines

	saved := SavedSettings(w.Prefs, key)
	if useOverrideSaved {
		saved = overrideSaved
	}

	from := newInheritance(w, allProjects, key)

	hasMachines := len(machinesByID) > 0
	activeMachineCount := 0
	for _, m := range machinesByID {
		if m != nil && !m.Archived {
			activeMachineCount++
		}
	}
	hasActiveMachine := activeMachineCount > 0
	isUsableMachine := func(id string) bool {
		if id == "" {
			return false
		}
		if !hasMachines {
			return true
		}
		m, ok := machinesByID[id]
		if !ok {
			return false
		}
		if hasActiveMachine && m.Archived {
			return false
		}
		return true
	}
	resolveUsable := func(id string) string {
		if id == "" {
			return ""
		}
		remapped := ResolveMachineReplacement(w, id)
		if isUsableMachine(remapped) {
			return remapped
		}
		return ""
	}
	pickFallbackMachine := func(ids []string) string {
		for _, id := range ids {
			if m := machinesByID[id]; isUsableMachine(id) && MachineReachable(m) {
				return id
			}
		}
		for _, id := range ids {
			if isUsableMachine(id) {
				return id
			}
		}
		return ""
	}

	machineID := resolveUsable(pickMachineID)
	if machineID == "" && saved != nil && saved.MachineId != "" {
		machineID = resolveUsable(saved.MachineId)
		if machineID == "" && pickMachineID == "" && activeMachineCount > 1 {
			// In a multi-computer account, do not silently fall back to another
			// computer when the saved machine is missing/archived with no verified
			// replacement mapping; prompt the user to choose explicitly.
			res.NeedsMachineChoice = true
			res.Settled = !from.unsettled
			return res
		}
	}
	if machineID == "" {
		if latest := from.first(func(s *Session) bool {
			return resolveUsable(s.MachineID) != ""
		}); latest != nil {
			machineID = resolveUsable(latest.MachineID)
		}
	}
	if machineID == "" && pr != nil && len(pr.machineIDs) > 0 {
		machineID = pickFallbackMachine(pr.machineIDs)
	}
	if machineID == "" && len(projectMachines) > 0 {
		machineID = pickFallbackMachine(projectMachines)
	}
	if machineID == "" && hasMachines {
		allIDs := make([]string, 0, len(machinesByID))
		for mid := range machinesByID {
			allIDs = append(allIDs, mid)
		}
		sortutil.Sort(len(allIDs), func(i, j int) bool { return compareUTF16(allIDs[i], allIDs[j]) < 0 }, func(i, j int) { allIDs[i], allIDs[j] = allIDs[j], allIDs[i] })
		machineID = pickFallbackMachine(allIDs)
	}
	if machineID == "" {
		return res
	}

	m := machinesByID[machineID]
	var mMeta *v1.MachineMeta
	if m != nil {
		mMeta = m.Meta
	}
	agents := MachineAgents(mMeta)
	wanted := wantedAgent(from, pickAgent, saved)
	agent := agents[0]
	if wanted != v1.Agent_AGENT_UNSPECIFIED && slices.Contains(agents, wanted) {
		agent = wanted
	}

	set := agentSet(from, agent, saved)
	cfg := configFromAgentSettings(machineID, key, set)

	res.Found = true
	res.Config = cfg
	res.Settled = !from.unsettled
	res.WantedAgent = wanted
	if wanted != v1.Agent_AGENT_UNSPECIFIED && wanted != agent && !slices.Contains(agents, wanted) {
		if slices.Contains(MachineInstalledAgents(mMeta), wanted) && IsAgentDisabled(mMeta, wanted) {
			res.DisabledAgent = wanted
		} else {
			res.MissingAgent = wanted
		}
	}
	res.DefaultMode = DefaultModeOf(m, agent)
	res.ProtocolMismatch = ProtocolMismatchDirection(m, w.ClientVersion)

	for _, id := range projectMachines {
		if id == machineID {
			continue
		}
		cand := machinesByID[id]
		if !MachineReachable(cand) {
			continue
		}
		var candMeta *v1.MachineMeta
		if cand != nil {
			candMeta = cand.Meta
		}
		candAgents := MachineAgents(candMeta)
		switched := SwitchMachine(cfg, id, candAgents)
		res.OfflineChoices = append(res.OfflineChoices, OfflineMachineOption{
			ID:              id,
			Name:            MachineName(cand, id),
			InstalledAgents: candAgents,
			Config:          switched,
			FallbackAgent:   switched.Agent,
		})
	}

	return res
}

// PlanStartSettings computes the FullPrefsOp to save when starting a session, or nil if unchanged.
func PlanStartSettings(used StartConfig, proj *StartConfig, saved *v1.ProjectSettings) *FullPrefsOp {
	used.Agent = EffectiveAgent(used.Agent)
	if proj != nil {
		p := *proj
		p.Agent = EffectiveAgent(p.Agent)
		proj = &p
	}
	if saved != nil && saved.Agent == used.Agent {
		for _, a := range saved.Agents {
			if a != nil && a.Agent == used.Agent {
				return nil
			}
		}
	}

	var setCfg *StartConfig
	if proj != nil && used.MachineID == proj.MachineID {
		u := used
		setCfg = &u
	} else if proj != nil && proj.Agent == used.Agent {
		setCfg = proj
	} else if proj == nil {
		u := used
		setCfg = &u
	}

	var sets []*v1.AgentSettings
	if setCfg != nil {
		sets = []*v1.AgentSettings{settingsFromConfig(*setCfg)}
	}
	machineID := used.MachineID
	if proj != nil && proj.MachineID != "" {
		machineID = proj.MachineID
	}
	agent := used.Agent
	return &FullPrefsOp{
		Kind: PrefsOpSetSettings,
		Path: used.Cwd,
		SetSettings: &PrefsSetSettingsOp{
			Path:         used.Cwd,
			MachineID:    machineID,
			Agent:        &agent,
			Sets:         sets,
			KeepExisting: true,
		},
	}
}

// SwitchMachine moves a StartConfig to another machine, clearing model/effort/mode and falling back if CLI is absent.
func SwitchMachine(c StartConfig, machineID string, agents []v1.Agent) StartConfig {
	out := c
	if out.Agent == v1.Agent_AGENT_UNSPECIFIED {
		out.Agent = DefaultAgent
	}
	out.MachineID = machineID
	out.Model = ""
	out.Effort = ""
	out.Mode = v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
	if !slices.Contains(agents, out.Agent) {
		if len(agents) > 0 {
			out.Agent = agents[0]
		} else {
			out.Agent = DefaultAgent
		}
	}
	return out
}

// SwitchAgent switches a draft config to another CLI using the project's config for that CLI when on the same machine.
func SwitchAgent(c StartConfig, project *StartConfig, agent v1.Agent) StartConfig {
	if project != nil && project.Agent == agent {
		out := *project
		if out.MachineID != c.MachineID {
			out.MachineID = c.MachineID
			out.Model = ""
			out.Effort = ""
		}
		return out
	}
	return StartConfig{
		MachineID: c.MachineID,
		Cwd:       c.Cwd,
		Agent:     agent,
	}
}
