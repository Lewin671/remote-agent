// Package lists implements shared pure client logic for agents, project/session lists,
// home timeline, new-session config inheritance, models/modes, pagination, and display helpers.
package lists

import (
	"strings"
	"unicode"
	"unicode/utf8"

	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

// DefaultAgent is the fallback CLI when a session or machine does not specify one.
const DefaultAgent = v1.Agent_AGENT_CODEX

// AgentCapabilities describes a CLI's static capabilities and naming rules.
type AgentCapabilities struct {
	Agent                  v1.Agent
	ID                     string
	Name                   string
	QuotaWindows           []string
	ToolLabelFromItemType  bool
	PlanClearContext       bool
	StandardContextOption  bool
	AutoMayBeUnavailable   bool
	ReconcilesUnclaimedIn  bool
	SessionNeedsModelList  bool
	SwitchesModelInSession bool
	Forks                  bool
	SteersDuringTurn       bool
	ShowMaxEffort          bool
	NormalizesPlanName     bool
	FormatsGptSlug         bool
}

var (
	codexCapabilities = AgentCapabilities{
		Agent:                  v1.Agent_AGENT_CODEX,
		ID:                     "codex",
		Name:                   "Codex",
		QuotaWindows:           []string{"primary", "secondary"},
		PlanClearContext:       true,
		SwitchesModelInSession: true,
		Forks:                  true,
		SteersDuringTurn:       true,
		ShowMaxEffort:          true,
		NormalizesPlanName:     true,
		FormatsGptSlug:         true,
	}
	claudeCapabilities = AgentCapabilities{
		Agent:                  v1.Agent_AGENT_CLAUDE,
		ID:                     "claude",
		Name:                   "Claude Code",
		QuotaWindows:           []string{"five_hour", "seven_day"},
		ToolLabelFromItemType:  true,
		StandardContextOption:  true,
		AutoMayBeUnavailable:   true,
		ReconcilesUnclaimedIn:  true,
		SessionNeedsModelList:  true,
		SwitchesModelInSession: true,
		Forks:                  true,
		SteersDuringTurn:       true,
		NormalizesPlanName:     true,
	}
	antigravityCapabilities = AgentCapabilities{
		Agent:                 v1.Agent_AGENT_ANTIGRAVITY,
		ID:                    "agy",
		Name:                  "Antigravity",
		AutoMayBeUnavailable:  true,
		SessionNeedsModelList: true,
	}
	unknownCapabilities = AgentCapabilities{
		Agent: v1.Agent_AGENT_UNSPECIFIED,
	}

	// KnownAgents lists the supported CLIs in display order.
	KnownAgents = []AgentCapabilities{codexCapabilities, claudeCapabilities, antigravityCapabilities}
	// SettingsAgents lists the Agent enum values stored in ProjectSettings.
	SettingsAgents = []v1.Agent{v1.Agent_AGENT_CODEX, v1.Agent_AGENT_CLAUDE, v1.Agent_AGENT_ANTIGRAVITY}
)

// IsKnownAgent reports whether a is one of the supported CLI agents.
func IsKnownAgent(a v1.Agent) bool {
	return a == v1.Agent_AGENT_CODEX || a == v1.Agent_AGENT_CLAUDE || a == v1.Agent_AGENT_ANTIGRAVITY
}

// AgentInfo resolves capabilities for a, mapping AGENT_UNSPECIFIED to Codex.
func AgentInfo(a v1.Agent) AgentCapabilities {
	switch a {
	case v1.Agent_AGENT_UNSPECIFIED, v1.Agent_AGENT_CODEX:
		return codexCapabilities
	case v1.Agent_AGENT_CLAUDE:
		return claudeCapabilities
	case v1.Agent_AGENT_ANTIGRAVITY:
		return antigravityCapabilities
	default:
		u := unknownCapabilities
		u.Agent = a
		return u
	}
}

// AgentByID looks up a CLI entry by its short string ID ("codex", "claude", "agy").
func AgentByID(id string) AgentCapabilities {
	for _, entry := range KnownAgents {
		if entry.ID == id {
			return entry
		}
	}
	return unknownCapabilities
}

// EffectiveAgent normalizes AGENT_UNSPECIFIED to DefaultAgent, preserving unknown enum values.
func EffectiveAgent(a v1.Agent) v1.Agent {
	return AgentInfo(a).Agent
}

func capitalizeFirst(s string) string {
	if s == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size == 0 {
		return s
	}
	return string(unicode.ToTitle(r)) + s[size:]
}

// NormalizePlanName maps CLI plan identifiers (e.g. "claude_max", "prolite") to display labels.
func NormalizePlanName(plan string) string {
	switch strings.ToLower(plan) {
	case "prolite":
		return "Pro Lite"
	case "claude_max":
		return "Max"
	case "claude_pro":
		return "Pro"
	case "claude_team":
		return "Team"
	case "claude_enterprise":
		return "Enterprise"
	}
	trimmed := plan
	if len(trimmed) >= len("claude_") && strings.EqualFold(trimmed[:len("claude_")], "claude_") {
		trimmed = trimmed[len("claude_"):]
	}
	words := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == '_' || unicode.IsSpace(r)
	})
	for i, w := range words {
		words[i] = capitalizeFirst(w)
	}
	return strings.Join(words, " ")
}

// PlanName formats a raw plan string according to the agent's rules.
func PlanName(agent v1.Agent, raw string) string {
	if AgentInfo(agent).NormalizesPlanName {
		return NormalizePlanName(raw)
	}
	return raw
}

// GptSlugLabel formats a Codex slug such as "gpt-6.1-sol" into "GPT-6.1-Sol".
func GptSlugLabel(id string) string {
	parts := strings.Split(id, "-")
	for i, w := range parts {
		if strings.EqualFold(w, "gpt") {
			parts[i] = "GPT"
		} else {
			parts[i] = capitalizeFirst(w)
		}
	}
	return strings.Join(parts, "-")
}

// AgentModelLabel formats a fallback model ID using the agent's rule.
func AgentModelLabel(agent v1.Agent, id string) string {
	if AgentInfo(agent).FormatsGptSlug {
		return GptSlugLabel(id)
	}
	return id
}

// DefaultsFor extracts the DaemonSettings defaults configured for agent.
func DefaultsFor(settings *v1.DaemonSettings, agent v1.Agent) *v1.AgentDefaults {
	if settings == nil {
		return &v1.AgentDefaults{}
	}
	switch EffectiveAgent(agent) {
	case v1.Agent_AGENT_CODEX:
		return &v1.AgentDefaults{
			Model:  settings.DefaultModel,
			Effort: settings.DefaultEffort,
			Mode:   settings.DefaultMode,
		}
	case v1.Agent_AGENT_CLAUDE:
		if settings.Claude != nil {
			return settings.Claude.Clone()
		}
		return &v1.AgentDefaults{}
	case v1.Agent_AGENT_ANTIGRAVITY:
		if settings.Antigravity != nil {
			return settings.Antigravity.Clone()
		}
		return &v1.AgentDefaults{}
	default:
		return &v1.AgentDefaults{}
	}
}

// WithDefaults returns a copy of settings with non-nil model/effort/mode applied for agent.
func WithDefaults(settings *v1.DaemonSettings, agent v1.Agent, model, effort *string, mode *v1.ApprovalMode) *v1.DaemonSettings {
	out := &v1.DaemonSettings{}
	if settings != nil {
		out = settings.Clone()
	}
	switch EffectiveAgent(agent) {
	case v1.Agent_AGENT_CODEX:
		if model != nil {
			out.DefaultModel = *model
		}
		if effort != nil {
			out.DefaultEffort = *effort
		}
		if mode != nil {
			out.DefaultMode = *mode
		}
	case v1.Agent_AGENT_CLAUDE:
		cur := DefaultsFor(out, v1.Agent_AGENT_CLAUDE)
		if model != nil {
			cur.Model = *model
		}
		if effort != nil {
			cur.Effort = *effort
		}
		if mode != nil {
			cur.Mode = *mode
		}
		out.Claude = cur
	case v1.Agent_AGENT_ANTIGRAVITY:
		cur := DefaultsFor(out, v1.Agent_AGENT_ANTIGRAVITY)
		if model != nil {
			cur.Model = *model
		}
		if effort != nil {
			cur.Effort = *effort
		}
		if mode != nil {
			cur.Mode = *mode
		}
		out.Antigravity = cur
	}
	return out
}

// MachineVersions returns per-CLI version entries from meta, synthesizing legacy Codex fields when absent.
func MachineVersions(meta *v1.MachineMeta) []*v1.AgentInfo {
	if meta == nil {
		return nil
	}
	var primary *v1.AgentInfo
	var others []*v1.AgentInfo
	for _, a := range meta.Agents {
		if a == nil {
			continue
		}
		if a.Agent == v1.Agent_AGENT_CODEX {
			if primary == nil {
				primary = a.Clone()
			}
		} else {
			others = append(others, a.Clone())
		}
	}
	if primary == nil && (meta.CodexVersion != "" || len(meta.Agents) == 0) {
		primary = &v1.AgentInfo{
			Agent:    v1.Agent_AGENT_CODEX,
			Version:  meta.CodexVersion,
			Verified: meta.CodexVerified,
		}
	}
	out := make([]*v1.AgentInfo, 0, 1+len(others))
	if primary != nil {
		out = append(out, primary)
	}
	return append(out, others...)
}

// MachineInstalledAgents returns all distinct CLIs reported as installed on the machine,
// ignoring disabled_agents, defaulting to [DefaultAgent].
func MachineInstalledAgents(meta *v1.MachineMeta) []v1.Agent {
	if meta == nil || len(meta.Agents) == 0 {
		return []v1.Agent{DefaultAgent}
	}
	seen := make(map[v1.Agent]bool, len(meta.Agents))
	var out []v1.Agent
	for _, info := range meta.Agents {
		if info == nil {
			continue
		}
		a := EffectiveAgent(info.Agent)
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return []v1.Agent{DefaultAgent}
	}
	return out
}

// MachineDisabledSet returns the normalized set of disabled CLIs on the machine.
func MachineDisabledSet(meta *v1.MachineMeta) map[v1.Agent]bool {
	if meta == nil || len(meta.DisabledAgents) == 0 {
		return nil
	}
	out := make(map[v1.Agent]bool, len(meta.DisabledAgents))
	for _, a := range meta.DisabledAgents {
		if a == v1.Agent_AGENT_UNSPECIFIED {
			continue
		}
		out[EffectiveAgent(a)] = true
	}
	return out
}

// IsAgentDisabled reports whether agent is installed/advertised on the machine and explicitly disabled.
func IsAgentDisabled(meta *v1.MachineMeta, agent v1.Agent) bool {
	if meta == nil || len(meta.DisabledAgents) == 0 {
		return false
	}
	eff := EffectiveAgent(agent)
	for _, a := range meta.DisabledAgents {
		if a != v1.Agent_AGENT_UNSPECIFIED && EffectiveAgent(a) == eff {
			return true
		}
	}
	return false
}

// MachineAgents returns the distinct enabled CLIs a machine advertises (installed minus disabled),
// falling back to all installed if every installed CLI were marked disabled, or [DefaultAgent].
func MachineAgents(meta *v1.MachineMeta) []v1.Agent {
	installed := MachineInstalledAgents(meta)
	if meta == nil || len(meta.DisabledAgents) == 0 {
		return installed
	}
	disabled := MachineDisabledSet(meta)
	var out []v1.Agent
	for _, a := range installed {
		if !disabled[a] {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return installed
	}
	return out
}

// SendModeFor resolves the SendMode for an input given whether the session is running.
func SendModeFor(agent v1.Agent, requested v1.SendMode, running bool) v1.SendMode {
	if running && !AgentInfo(agent).SteersDuringTurn {
		return v1.SendMode_SEND_MODE_QUEUE
	}
	return requested
}

// UnknownCapabilities returns the descriptor used for unrecognized agents.
func UnknownCapabilities() AgentCapabilities { return unknownCapabilities }
