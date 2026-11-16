package lists

import (
	"strings"
	"unicode"

	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/projection"
)

// StandardContextWindow is the 200k token limit used by Claude Code standard context mode.
const StandardContextWindow int64 = 200_000

// NewSessionModes lists the approval modes offered for machine defaults and scheduled tasks.
var NewSessionModes = []v1.ApprovalMode{
	v1.ApprovalMode_APPROVAL_MODE_ASK,
	v1.ApprovalMode_APPROVAL_MODE_AUTO,
	v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS,
}

func splitModelWords(id string) []string {
	return strings.FieldsFunc(strings.ToLower(id), func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || unicode.IsSpace(r)
	})
}

// ModelIs reports whether a CLI-reported model ID matches the chosen slug or alias.
func ModelIs(id, slug string) bool {
	return projection.ModelIs(id, slug)
}

// FindModel finds the ModelInfo entry matching id by exact slug or alias word, stripping any "[1m]" suffix.
func FindModel(models []*v1.ModelInfo, id string) *v1.ModelInfo {
	if id == "" || len(models) == 0 {
		return nil
	}
	id = strings.TrimSuffix(id, "[1m]")
	for _, m := range models {
		if m != nil && m.Slug == id {
			return m
		}
	}
	words := splitModelWords(id)
	for _, m := range models {
		if m == nil {
			continue
		}
		target := strings.ToLower(m.Slug)
		for _, w := range words {
			if w == target {
				return m
			}
		}
	}
	return nil
}

// AllAutoUnavailable reports whether a non-empty model list marks every model as lacking auto mode.
func AllAutoUnavailable(models []*v1.ModelInfo) bool {
	if len(models) == 0 {
		return false
	}
	for _, m := range models {
		if m == nil || !m.AutoUnavailable {
			return false
		}
	}
	return true
}

// AutoUnavailable reports whether auto approval is unavailable for model in models.
func AutoUnavailable(models []*v1.ModelInfo, hasModels bool, model string) bool {
	if !hasModels {
		return false
	}
	if AllAutoUnavailable(models) {
		return true
	}
	if model == "" {
		return false
	}
	m := FindModel(models, model)
	return m != nil && m.AutoUnavailable
}

// ModelLabel resolves the display name of model id given optional models and agent.
func ModelLabel(models []*v1.ModelInfo, hasModels bool, id string, agent v1.Agent) string {
	if id == "" {
		return ""
	}
	if hasModels {
		if m := FindModel(models, id); m != nil && m.DisplayName != "" {
			return m.DisplayName
		}
		return AgentModelLabel(agent, id)
	}
	return id
}

// ShownModels returns either the full model list or its first 3 entries plus picked (when list > 5).
func ShownModels(models []*v1.ModelInfo, picked *v1.ModelInfo, all bool) []*v1.ModelInfo {
	if all || len(models) <= 5 {
		return models
	}
	top := make([]*v1.ModelInfo, 0, 4)
	top = append(top, models[:3]...)
	if picked == nil {
		return top
	}
	for _, m := range top {
		if m != nil && m.Slug == picked.Slug {
			return top
		}
	}
	return append(top, picked)
}

// DefaultModeOf reads the CLI's effective default approval mode on machine m.
func DefaultModeOf(m *Machine, agent v1.Agent) DefaultMode {
	want := EffectiveAgent(agent)
	var info *v1.AgentInfo
	if m != nil && m.Meta != nil {
		for _, a := range m.Meta.Agents {
			if a != nil && EffectiveAgent(a.Agent) == want {
				info = a
				break
			}
		}
	}
	known := info != nil &&
		(info.DefaultModeSource == v1.DefaultModeSource_DEFAULT_MODE_SOURCE_DAEMON_SETTINGS ||
			info.DefaultModeSource == v1.DefaultModeSource_DEFAULT_MODE_SOURCE_CLI_CONFIG)
	mode := v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
	var varies bool
	if info != nil {
		varies = info.DefaultModeVaries
		if known {
			mode = projection.ModeOf(info.DefaultMode)
		}
	}
	return DefaultMode{
		Mode:   mode,
		Known:  known,
		Varies: varies,
	}
}

// DefaultModeFromSettings returns the daemon setting's default mode for agent, or UNSPECIFIED.
func DefaultModeFromSettings(settings *v1.DaemonSettings, agent v1.Agent) v1.ApprovalMode {
	return projection.ModeOf(DefaultsFor(settings, agent).Mode)
}

// AutoIssue determines whether starting in AUTO mode is blocked by model or account support.
func AutoIssue(agent v1.Agent, model string, mode v1.ApprovalMode, models []*v1.ModelInfo, hasModels bool) AutoIssueKind {
	if !AgentInfo(agent).AutoMayBeUnavailable ||
		mode != v1.ApprovalMode_APPROVAL_MODE_AUTO ||
		!AutoUnavailable(models, hasModels, model) {
		return AutoIssueNone
	}
	if AllAutoUnavailable(models) {
		return AutoIssueAccount
	}
	return AutoIssueModel
}

// ProjectModel resolves the ModelInfo a new session config would run.
func ProjectModel(c StartConfig, models *v1.ListModelsResponse, settings *v1.DaemonSettings) *v1.ModelInfo {
	if models == nil {
		return nil
	}
	id := c.Model
	if id == "" {
		id = DefaultsFor(settings, c.Agent).Model
	}
	if id == "" {
		id = models.DefaultModel
	}
	return FindModel(models.Models, id)
}

// PickModel updates config c when selecting ModelInfo m, resetting effort and dropping AUTO if unsupported.
func PickModel(c StartConfig, m *v1.ModelInfo) StartConfig {
	out := c
	if m == nil {
		return out
	}
	out.Model = m.Slug
	out.Effort = m.DefaultEffort
	if m.AutoUnavailable && out.Mode == v1.ApprovalMode_APPROVAL_MODE_AUTO {
		out.Mode = v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
	}
	return out
}

// EffortChoices returns available effort levels and the effective selected effort for config c.
func EffortChoices(c StartConfig, models *v1.ListModelsResponse, settings *v1.DaemonSettings) ([]string, string) {
	m := ProjectModel(c, models, settings)
	if m == nil || len(m.Efforts) == 0 {
		return nil, ""
	}
	own := m.DefaultEffort
	if c.Model == "" {
		if defEffort := DefaultsFor(settings, c.Agent).Effort; defEffort != "" {
			own = defEffort
		}
	}
	selected := c.Effort
	if selected == "" {
		selected = own
	}
	return m.Efforts, selected
}

// SessionModeUnavailableReason checks whether a mode is disabled for an active session.
func SessionModeUnavailableReason(mode v1.ApprovalMode, modeLocked, fullAccessUnavailable bool, sessionModel string, models []*v1.ModelInfo, hasModels bool) ModeUnavailableReason {
	if modeLocked {
		return ModeLockedFullAccess
	}
	if mode == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS && fullAccessUnavailable {
		return ModeFullAccessRequiresStart
	}
	if mode == v1.ApprovalMode_APPROVAL_MODE_AUTO && hasModels {
		if AllAutoUnavailable(models) {
			return ModeAccountNoAuto
		}
		if AutoUnavailable(models, true, sessionModel) {
			return ModeModelNoAuto
		}
	}
	return ModeAvailable
}
