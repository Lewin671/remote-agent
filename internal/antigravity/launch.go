package antigravity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// Hook command executed by Antigravity CLI lifecycle hooks.
const hookCommand = `"$RA_EXE" hook agy`

// StatusLine command executed by Antigravity CLI statusLine configuration.
const statusLineCommand = `"$RA_EXE" statusline agy`

// SettingsFile is where a session's settings are written in its state directory.
const SettingsFile = "agy-settings.json"

// AddonDir is the subdirectory passed to --add-dir holding .agents/hooks.json and .agents/mcp_config.json.
const AddonDir = "agy"

// ownFlags are agy options that remote-agent manages itself or that would interfere with session operation.
var ownFlags = []string{
	"-p", "--print", "--prompt", "-i", "--prompt-interactive",
	"--conversation", "-c", "--continue", "--settings",
	"--dangerously-skip-permissions", "--ra-tool-permission", "--remote-agent", "--remote-control",
}

// statusLineSetting represents a statusLine entry in settings.json.
type statusLineSetting struct {
	Type            string   `json:"type"`
	Command         string   `json:"command"`
	Padding         *float64 `json:"padding,omitempty"`
	RefreshInterval *float64 `json:"refreshInterval,omitempty"`
}

// userStatusLine reads the user's configured statusLine from ~/.gemini/antigravity-cli/settings.json.
func (a *Adapter) userStatusLine() *statusLineSetting {
	path := filepath.Join(a.home, "settings.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var st struct {
		StatusLine *statusLineSetting `json:"statusLine"`
	}
	if json.Unmarshal(b, &st) == nil && st.StatusLine != nil {
		return st.StatusLine
	}
	return nil
}

// ManagedStatusLine reports whether managed settings dictate the statusline.
func (a *Adapter) ManagedStatusLine(_ string) bool {
	return false
}

// Options converts new session options into agy command-line arguments.
func (a *Adapter) Options(extra []string, model, effort string, mode pb.ApprovalMode) ([]string, error) {
	for _, arg := range extra {
		name, _, _ := strings.Cut(arg, "=")
		if slices.Contains(ownFlags, name) {
			return nil, fmt.Errorf("remote-agent sets %s itself or cannot run a session with it", name)
		}
	}

	args := append([]string{}, extra...)
	if model != "" {
		if !agent.SafeValue.MatchString(model) {
			return nil, fmt.Errorf("invalid model %q", model)
		}
		args = append(args, "--model", model)
	}
	if effort != "" {
		if err := CheckEffort(effort); err != nil {
			return nil, err
		}
		args = append(args, "--effort", effort)
	}

	switch mode {
	case pb.ApprovalMode_APPROVAL_MODE_AUTO:
		args = append(args, "--mode", "auto", "--ra-tool-permission=request-review")
	case pb.ApprovalMode_APPROVAL_MODE_PLAN:
		args = append(args, "--mode", "plan", "--ra-tool-permission=request-review")
	case pb.ApprovalMode_APPROVAL_MODE_ASK:
		args = append(args, "--ra-tool-permission=request-review")
	case pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS:
		args = append(args, "--ra-tool-permission=always-proceed")
	case pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED:
		policy := a.launchPermission(nil)
		if policy == "" {
			policy = "request-review"
		}
		args = append(args, "--ra-tool-permission="+policy)
	default:
		return nil, fmt.Errorf("unknown approval mode %v", mode)
	}

	return args, nil
}

// FullAccess reports whether an Antigravity session can be switched to full access mid-session.
// Architecture §19: ToolPermission must be determined at launch; sessions cannot switch mid-session.
func (a *Adapter) FullAccess(args []string) bool {
	return a.launchPermission(args) == "always-proceed"
}

func (a *Adapter) launchPermission(args []string) string {
	for _, arg := range args {
		if v, ok := strings.CutPrefix(arg, "--ra-tool-permission="); ok {
			return v
		}
	}
	var st struct {
		ToolPermission string `json:"toolPermission"`
	}
	b, _ := os.ReadFile(filepath.Join(a.home, "settings.json"))
	_ = json.Unmarshal(b, &st)
	return st.ToolPermission
}

// DefaultMode reads ~/.gemini/antigravity-cli/settings.json for default toolPermission.
func (a *Adapter) DefaultMode(_ string) (mode pb.ApprovalMode, known, varies bool) {
	path := filepath.Join(a.home, "settings.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED, false, false
	}
	var st struct {
		ToolPermission string `json:"toolPermission"`
	}
	if json.Unmarshal(b, &st) != nil {
		return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED, false, false
	}

	switch st.ToolPermission {
	case "always-proceed":
		return pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS, true, false
	case "request-review", "strict":
		return pb.ApprovalMode_APPROVAL_MODE_ASK, true, false
	default:
		return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED, false, false
	}
}

// Argv prepares the command line arguments and writes session-specific configuration files.
func (a *Adapter) Argv(l *agent.Launch) ([]string, error) {
	if l.ForkOf != "" {
		return nil, agent.ErrUnsupported
	}

	addonPath := filepath.Join(l.Dir, AddonDir)
	agentsDir := filepath.Join(addonPath, ".agents")
	if err := os.MkdirAll(agentsDir, 0o700); err != nil {
		return nil, err
	}

	// 1. Write hooks.json
	hooksJSON := buildHooksJSON()
	if err := os.WriteFile(filepath.Join(agentsDir, "hooks.json"), hooksJSON, 0o600); err != nil {
		return nil, err
	}

	// 2. Write mcp_config.json
	mcpJSON := buildMCPConfig(l)
	if err := os.WriteFile(filepath.Join(agentsDir, "mcp_config.json"), mcpJSON, 0o600); err != nil {
		return nil, err
	}

	// 3. User statusline preservation
	userSL := a.userStatusLine()
	ownSL := []byte("{}")
	if userSL != nil && (userSL.Type == "command" || userSL.Type == "") && userSL.Command != "" {
		ownSL, _ = json.Marshal(userSL)
	}
	if err := os.WriteFile(filepath.Join(l.Dir, agent.StatusLineFile), ownSL, 0o600); err != nil {
		return nil, err
	}

	// 4. Write agy-settings.json
	permission := a.launchPermission(l.Args)
	if permission == "" {
		permission = "request-review"
	}
	if l.Resume {
		var old struct {
			ToolPermission string `json:"toolPermission"`
		}
		b, err := os.ReadFile(filepath.Join(l.Dir, SettingsFile))
		if err != nil {
			return nil, fmt.Errorf("read original agy settings: %w", err)
		}
		if err := json.Unmarshal(b, &old); err != nil {
			return nil, err
		}
		permission = old.ToolPermission
		if permission == "" {
			return nil, fmt.Errorf("original toolPermission is unknown; handle this conversation in the terminal")
		}
		if l.FullAccess {
			permission = "always-proceed"
		}
	}
	settingsJSON := buildSettingsJSON(l.Cwd, addonPath, permission, userSL)
	settingsPath := filepath.Join(l.Dir, SettingsFile)
	if err := os.WriteFile(settingsPath, settingsJSON, 0o600); err != nil {
		return nil, err
	}

	// 5. Construct CLI argv
	argv := []string{
		"--add-dir", addonPath,
		"--settings", settingsPath,
	}

	if l.Resume {
		if l.SessionID != "" {
			argv = append(argv, "--conversation", l.SessionID)
		}
		// Apply latest context overrides if present
		args := resumeArgs(l.Args)
		if l.Context != nil {
			if l.Context.Model != "" && agent.SafeValue.MatchString(l.Context.Model) {
				args = setFlag(args, "--model", l.Context.Model)
			}
			if l.Context.Effort != "" && CheckEffort(l.Context.Effort) == nil {
				args = setFlag(args, "--effort", l.Context.Effort)
			}
			args = filterModeArgs(args)
			if l.Context.PermissionMode == "auto" {
				args = setFlag(args, "--mode", "auto")
			} else if l.Context.PermissionMode == "plan" || l.Context.PermissionMode == "accept-edits" {
				args = setFlag(args, "--mode", l.Context.PermissionMode)
			}
		}
		argv = append(argv, args...)
	} else {
		// New session
		args := filterArgs(l.Args, "--ra-tool-permission")
		argv = append(argv, args...)
	}

	return argv, nil
}

func buildHooksJSON() []byte {
	hooks := map[string]any{
		"remote-agent": map[string]any{
			"SessionStart": []map[string]any{
				{"type": "command", "command": hookCommand + " SessionStart", "timeout": 30},
			},
			"PreToolUse": []map[string]any{
				{
					"matcher": ".*",
					"hooks": []map[string]any{
						{"type": "command", "command": hookCommand + " PreToolUse", "timeout": 30},
					},
				},
			},
			"Stop": []map[string]any{
				{"type": "command", "command": hookCommand + " Stop", "timeout": 30},
			},
		},
	}
	b, _ := json.MarshalIndent(hooks, "", "  ")
	return b
}

func buildMCPConfig(l *agent.Launch) []byte {
	servers := map[string]any{}
	if l.Exe != "" && l.RemoteSession != "" {
		servers["remote_agent"] = map[string]any{
			"command": l.Exe,
			"args":    []string{"mcp", "--session", l.RemoteSession},
			"env": map[string]string{
				"RA_SESSION": l.RemoteSession,
			},
		}
	}
	cfg := map[string]any{
		"mcpServers": servers,
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return b
}

func buildSettingsJSON(cwd, addonDir, toolPerm string, userSL *statusLineSetting) []byte {

	sl := map[string]any{
		"type":    "command",
		"command": statusLineCommand,
	}
	if userSL != nil {
		if userSL.Padding != nil {
			sl["padding"] = *userSL.Padding
		}
		if userSL.RefreshInterval != nil {
			sl["refreshInterval"] = *userSL.RefreshInterval
		}
	}

	trusted := []string{cwd, addonDir}
	st := map[string]any{
		"trustedWorkspaces": trusted,
		"statusLine":        sl,
		"toolPermission":    toolPerm,
	}
	if toolPerm == "" {
		delete(st, "toolPermission")
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	return b
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

func filterArgs(args []string, exclude ...string) []string {
	var out []string
	for _, a := range args {
		skip := false
		for _, ex := range exclude {
			if a == ex || strings.HasPrefix(a, ex+"=") {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, a)
		}
	}
	return out
}

func setFlag(args []string, name, value string) []string {
	var out []string
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if a == name {
			skipNext = true
			continue
		}
		if strings.HasPrefix(a, name+"=") {
			continue
		}
		out = append(out, a)
	}
	return append(out, name, value)
}

func filterModeArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--mode" {
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--mode=") {
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// Resume keeps each flag's full value and every repeated add-dir, but drops
// positional initial prompts so they are not submitted again.
func resumeArgs(args []string) []string {
	values := map[string]bool{"--add-dir": true, "--agent": true, "--effort": true, "--json-schema": true, "--log-file": true, "--mode": true, "--model": true, "--project": true}
	var out []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, _, inline := strings.Cut(arg, "=")
		if name == "--ra-tool-permission" {
			continue
		}
		out = append(out, arg)
		if values[name] && !inline && i+1 < len(args) {
			i++
			out = append(out, args[i])
		}
	}
	return out
}
