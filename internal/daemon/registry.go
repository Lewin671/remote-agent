package daemon

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/antigravity"
	"github.com/Lewin671/remote-agent/internal/claude"
	"github.com/Lewin671/remote-agent/internal/codex"
)

// AgentConfig holds per-CLI executable and path configuration.
type AgentConfig struct {
	Bin         string
	Cmd         string
	Home        string
	HistoryPath string
}

// AgentSpec defines a registered CLI adapter.
type AgentSpec struct {
	ID       string
	Kind     pb.Agent
	Name     string
	Required bool
	HomeEnv  string
	HomePath string
	New      func(ac AgentConfig) agent.Adapter
}

// RegisteredAgents is the ordered list of supported coding CLIs, with Codex first.
var RegisteredAgents = []AgentSpec{
	{
		ID:       "codex",
		Kind:     pb.Agent_AGENT_CODEX,
		Name:     "Codex",
		Required: true,
		HomeEnv:  "CODEX_HOME",
		HomePath: ".codex",
		New: func(ac AgentConfig) agent.Adapter {
			return codex.New(ac.Bin, ac.Home, ac.HistoryPath)
		},
	},
	{
		ID:       "claude",
		Kind:     pb.Agent_AGENT_CLAUDE,
		Name:     "Claude Code",
		Required: false,
		HomeEnv:  "CLAUDE_CONFIG_DIR",
		HomePath: ".claude",
		New: func(ac AgentConfig) agent.Adapter {
			return claude.New(ac.Bin, ac.Home)
		},
	},
	{
		ID:       "agy",
		Kind:     pb.Agent_AGENT_ANTIGRAVITY,
		Name:     "Antigravity",
		Required: false,
		HomePath: ".gemini/antigravity-cli",
		New: func(ac AgentConfig) agent.Adapter {
			return antigravity.New(ac.Bin, ac.Home)
		},
	},
}

// FindAgent looks up a registered agent by its short ID.
func FindAgent(id string) (AgentSpec, bool) {
	for _, a := range RegisteredAgents {
		if a.ID == id {
			return a, true
		}
	}
	return AgentSpec{}, false
}

// FindAgentByKind looks up a registered agent by its protobuf enum value.
func FindAgentByKind(k pb.Agent) (AgentSpec, bool) {
	k = resolveKind(k)
	for _, a := range RegisteredAgents {
		if a.Kind == k {
			return a, true
		}
	}
	return AgentSpec{}, false
}

// AgentIDs returns the ordered list of registered CLI short IDs.
func AgentIDs() []string {
	ids := make([]string, len(RegisteredAgents))
	for i, a := range RegisteredAgents {
		ids[i] = a.ID
	}
	return ids
}

// Adapters constructs the ordered list of adapters for the given configuration.
func Adapters(cfg Config) []agent.Adapter {
	adapters := make([]agent.Adapter, 0, len(RegisteredAgents))
	for _, spec := range RegisteredAgents {
		var ac AgentConfig
		if cfg.Agents != nil {
			ac = cfg.Agents[spec.Kind]
		}
		adapter := spec.New(ac)
		if configurable, ok := adapter.(interface{ SetCommandEnv(map[string]string) }); ok {
			configurable.SetCommandEnv(cfg.Env)
		}
		adapters = append(adapters, adapter)
	}
	return adapters
}

var defaultAdapters = Adapters(Config{})

func adapterForKind(k pb.Agent) agent.Adapter {
	k = resolveKind(k)
	for _, a := range defaultAdapters {
		if a.Kind() == k {
			return a
		}
	}
	return nil
}

// resolveKind applies the current request default: an omitted Agent selects
// Codex. Protocol-version negotiation is independent of this missing value.
func resolveKind(k pb.Agent) pb.Agent {
	if k == pb.Agent_AGENT_UNSPECIFIED {
		return pb.Agent_AGENT_CODEX
	}
	return k
}

// defaultsFor returns the AgentDefaults for kind k in st.
// The switch here is the only place allowed to know DaemonSettings field layout.
func defaultsFor(st *pb.DaemonSettings, k pb.Agent) *pb.AgentDefaults {
	if st == nil {
		return &pb.AgentDefaults{}
	}
	switch resolveKind(k) {
	case pb.Agent_AGENT_CODEX:
		return &pb.AgentDefaults{
			Model:  st.DefaultModel,
			Effort: st.DefaultEffort,
			Mode:   st.DefaultMode,
		}
	case pb.Agent_AGENT_CLAUDE:
		if c := st.GetClaude(); c != nil {
			return c
		}
		return &pb.AgentDefaults{}
	case pb.Agent_AGENT_ANTIGRAVITY:
		if a := st.GetAntigravity(); a != nil {
			return a
		}
		return &pb.AgentDefaults{}
	default:
		return &pb.AgentDefaults{}
	}
}

// setDefaultsFor updates the AgentDefaults for kind k in st.
// The switch here is the only place allowed to know DaemonSettings field layout.
func setDefaultsFor(st *pb.DaemonSettings, k pb.Agent, d *pb.AgentDefaults) {
	if st == nil {
		return
	}
	if d == nil {
		d = &pb.AgentDefaults{}
	}
	switch resolveKind(k) {
	case pb.Agent_AGENT_CODEX:
		st.DefaultModel = d.Model
		st.DefaultEffort = d.Effort
		st.DefaultMode = d.Mode
	case pb.Agent_AGENT_CLAUDE:
		st.Claude = d
	case pb.Agent_AGENT_ANTIGRAVITY:
		st.Antigravity = d
	}
}
