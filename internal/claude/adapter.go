// Package claude adapts the native Claude Code CLI: its launch arguments,
// hooks, transcript and screen (docs/claude-code-notes.md,
// docs/architecture.md §16).
package claude

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/codex"
	"google.golang.org/protobuf/proto"
)

// TestedVersions are the Claude Code versions the screen analysis, menus
// and transcript parsing were checked against (docs/claude-code-notes.md).
var TestedVersions = []string{"2.1.284"}

// hookCommand runs remote-agent's hook; the session environment names the
// executable in RA_EXE, as for Codex.
const hookCommand = `"$RA_EXE" hook claude`

// SettingsFile is where a session's hook settings are written in its
// state directory; Claude Code reads it through --settings.
const SettingsFile = "claude-settings.json"

// Adapter is Claude Code's agent.Adapter.
type Adapter struct {
	bin  string
	home string // ~/.claude

	versionOnce sync.Once
	version     string
}

var (
	_ agent.Adapter   = (*Adapter)(nil)
	_ agent.Checkable = (*Adapter)(nil)
)

// New returns the adapter for the claude executable bin (empty if Claude
// Code is not installed). home is its settings directory, ~/.claude.
func New(bin, home string) *Adapter { return &Adapter{bin: bin, home: home} }

func (a *Adapter) Kind() pb.Agent { return pb.Agent_AGENT_CLAUDE }
func (a *Adapter) Name() string   { return "Claude Code" }
func (a *Adapter) Bin() string    { return a.bin }

// Version is the first word of `claude --version` ("2.1.284 (Claude
// Code)"), read once.
func (a *Adapter) Version() string {
	a.versionOnce.Do(func() {
		if a.bin == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, a.bin, "--version").Output()
		if err == nil {
			if f := strings.Fields(string(out)); len(f) > 0 {
				a.version = f[0]
			}
		}
	})
	return a.version
}

func (a *Adapter) Tested(v string) bool { return slices.Contains(TestedVersions, v) }

// efforts are the levels /model and --effort take, in the order →
// cycles through them from medium.
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

// models are the aliases /model lists. Which model an alias stands for
// changes with Claude Code releases; the menu shows it.
var models = []*pb.ModelInfo{
	{Slug: "opus", DisplayName: "Opus", Description: "Best for everyday, complex tasks", Efforts: efforts, DefaultEffort: "medium"},
	{Slug: "fable", DisplayName: "Fable", Description: "Most capable for your hardest and longest-running tasks", Efforts: efforts, DefaultEffort: "medium"},
	{Slug: "sonnet", DisplayName: "Sonnet", Description: "Efficient for routine tasks", Efforts: efforts, DefaultEffort: "medium"},
	{Slug: "haiku", DisplayName: "Haiku", Description: "Fastest for quick answers"},
}

// Models lists the aliases of /model. The default is the model set in
// ~/.claude/settings.json, if any.
func (a *Adapter) Models() (*pb.ListModelsResponse, error) {
	out := &pb.ListModelsResponse{}
	for _, m := range models {
		out.Models = append(out.Models, proto.CloneOf(m))
	}
	var st struct {
		Model string `json:"model"`
	}
	if b, err := os.ReadFile(filepath.Join(a.home, "settings.json")); err == nil && json.Unmarshal(b, &st) == nil {
		out.DefaultModel = st.Model
	}
	return out, nil
}

// CheckEffort accepts Claude Code's levels, and none for models without
// levels such as haiku.
func (a *Adapter) CheckEffort(effort string) error {
	if effort == "" || slices.Contains(efforts, effort) {
		return nil
	}
	return fmt.Errorf("unknown effort %q", effort)
}

// permissionModes are Claude Code's names for the client's modes.
var permissionModes = map[pb.ApprovalMode]string{
	pb.ApprovalMode_APPROVAL_MODE_ASK:         "default",
	pb.ApprovalMode_APPROVAL_MODE_AUTO:        "auto",
	pb.ApprovalMode_APPROVAL_MODE_PLAN:        "plan",
	pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS: "bypassPermissions",
}

// Mode is the client's mode for a turn context's permission mode.
func (a *Adapter) Mode(c *pb.TurnContext) pb.ApprovalMode {
	if c == nil {
		return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
	}
	return modeOf(c.PermissionMode)
}

func modeOf(permissionMode string) pb.ApprovalMode {
	for m, name := range permissionModes {
		if name == permissionMode {
			return m
		}
	}
	return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
}

// ownFlags are claude options remote-agent sets itself or that would take
// the session out of its hands.
// --bare and --safe-mode turn off the hooks from --settings, without
// which remote-agent cannot find the transcript or forward approvals.
var ownFlags = []string{"-p", "--print", "--session-id", "-r", "--resume", "-c", "--continue", "--settings", "--bare", "--safe-mode",
	"--fork-session", "--bg", "--background", "--teleport", "--cloud", "--from-pr", "--tmux", "-w", "--worktree"}

// Options turns a new session's model, effort and approval mode into
// claude flags after the extra arguments.
func (a *Adapter) Options(extra []string, model, effort string, mode pb.ApprovalMode) ([]string, error) {
	for _, arg := range extra {
		name, _, _ := strings.Cut(arg, "=")
		if slices.Contains(ownFlags, name) {
			return nil, fmt.Errorf("remote-agent sets %s itself or cannot run a session with it", name)
		}
	}
	if mode == pb.ApprovalMode_APPROVAL_MODE_PLAN {
		return nil, errors.New("sessions cannot start in Plan mode; switch to it once the session runs")
	}
	args := append([]string{}, extra...)
	if model != "" {
		if !codex.SafeValue.MatchString(model) {
			return nil, fmt.Errorf("invalid model %q", model)
		}
		args = append(args, "--model", model)
	}
	if effort != "" {
		if err := a.CheckEffort(effort); err != nil {
			return nil, err
		}
		args = append(args, "--effort", effort)
	}
	if name, ok := permissionModes[mode]; ok {
		args = append(args, "--permission-mode", name)
	}
	return args, nil
}

// valueFlags are claude options that take a value. Options that take
// several (--add-dir a b) are read as taking one.
var valueFlags = map[string]bool{
	"--add-dir": true, "--agent": true, "--agents": true, "--allowedTools": true, "--allowed-tools": true,
	"--append-system-prompt": true, "--betas": true, "--debug-file": true, "--disallowedTools": true,
	"--disallowed-tools": true, "--effort": true, "--fallback-model": true, "--file": true, "--mcp-config": true,
	"--model": true, "-n": true, "--name": true, "--permission-mode": true, "--plugin-dir": true, "--plugin-url": true,
	"--setting-sources": true, "--system-prompt": true, "--tools": true, "--json-schema": true, "--autocompact": true,
	"--client-data-url": true, "--system-prompt-snapshot": true, "--remote-control-session-name-prefix": true,
	"--settings": true, "--session-id": true, "-r": true, "--resume": true,
}

// hasFlag reports whether args give the option, as "--x v" or "--x=v".
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

// variadic are claude options that take every value up to the next
// option (--disallowedTools Write Edit), as its parser does.
var variadic = map[string]bool{
	"--add-dir": true, "--allowedTools": true, "--allowed-tools": true, "--betas": true,
	"--disallowedTools": true, "--disallowed-tools": true, "--file": true, "--mcp-config": true, "--tools": true,
}

// flagArgs keeps the options in claude arguments, except those in drop,
// with all their values, and drops positional ones such as a first prompt.
// A value after a variadic option is one of its values, not a prompt.
func flagArgs(args []string, drop map[string]bool) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, _, inline := strings.Cut(a, "=")
		n := 1
		switch {
		case inline:
		case variadic[name]:
			for i+n < len(args) && !strings.HasPrefix(args[i+n], "-") {
				n++
			}
		case valueFlags[name] && i+1 < len(args):
			n = 2
		}
		if !drop[name] {
			out = append(out, args[i:i+n]...)
		}
		i += n - 1
	}
	return out
}

// FullAccess reports whether a session started with args can switch to
// bypass: Claude Code only offers it to sessions started in it or with it
// allowed, which ~/.claude/settings.json can do by making it the default
// mode (claude-code-notes §5).
func (a *Adapter) FullAccess(args []string) bool {
	mode := ""
	for i, arg := range args {
		name, value, inline := strings.Cut(arg, "=")
		switch name {
		case "--dangerously-skip-permissions", "--allow-dangerously-skip-permissions":
			return true
		case "--permission-mode":
			if !inline && i+1 < len(args) {
				value = args[i+1]
			}
			mode = value
		}
	}
	if mode == "" {
		mode = a.defaultMode()
	}
	return mode == "bypassPermissions"
}

// managedSettings is where an administrator's settings for Claude Code
// live; they override all others.
var managedSettings = func() string {
	if runtime.GOOS == "darwin" {
		return "/Library/Application Support/ClaudeCode/managed-settings.json"
	}
	return "/etc/claude-code/managed-settings.json"
}()

// settingsMode reads permissions.defaultMode from a settings file. A
// missing file is no answer and no error.
func settingsMode(path string) (string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var st struct {
		Permissions struct {
			DefaultMode string `json:"defaultMode"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return "", err
	}
	return st.Permissions.DefaultMode, nil
}

// DefaultMode follows Claude Code's settings precedence: managed settings,
// then the project's local and shared settings in cwd, then the user's.
// Without any it is manual (ASK).
func (a *Adapter) DefaultMode(cwd string) (pb.ApprovalMode, bool, bool) {
	files := []string{managedSettings}
	if cwd != "" {
		files = append(files, filepath.Join(cwd, ".claude", "settings.local.json"), filepath.Join(cwd, ".claude", "settings.json"))
	}
	files = append(files, filepath.Join(a.home, "settings.json"))
	for _, f := range files {
		m, err := settingsMode(f)
		if err != nil {
			return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED, false, true
		}
		if m != "" {
			return modeOf(m), true, true
		}
	}
	return pb.ApprovalMode_APPROVAL_MODE_ASK, true, true
}

// defaultMode is permissions.defaultMode in ~/.claude/settings.json.
func (a *Adapter) defaultMode() string {
	var st struct {
		Permissions struct {
			DefaultMode string `json:"defaultMode"`
		} `json:"permissions"`
	}
	if b, err := os.ReadFile(filepath.Join(a.home, "settings.json")); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st.Permissions.DefaultMode
}

// settings is the --settings file: remote-agent's hooks, which run
// alongside the user's own. SessionStart reports the transcript as soon as
// the folder is trusted; PermissionRequest forwards approvals; PreToolUse
// reports questions and plans, which Claude Code may write to the
// transcript only once they are answered.
//
// It also sets the status line to remote-agent's command, which hands
// the account's usage limits in the status line's input to the daemon and
// then runs the user's own status line command, if any (saved as
// StatusLineFile), with the same input, so the terminal shows what it
// did. A flag-level status line replaces the user's, so padding and
// refresh interval are carried over too.
func settings(user *statusLine) []byte {
	hook := func(timeout int) []map[string]any {
		return []map[string]any{{"type": "command", "command": hookCommand, "timeout": timeout}}
	}
	sl := map[string]any{"type": "command", "command": statusLineCommand}
	if user != nil {
		if user.Padding != nil {
			sl["padding"] = *user.Padding
		}
		if user.RefreshInterval != nil {
			sl["refreshInterval"] = *user.RefreshInterval
		}
	}
	s := map[string]any{
		"hooks": map[string]any{
			"SessionStart":      []map[string]any{{"hooks": hook(30)}},
			"PermissionRequest": []map[string]any{{"matcher": "*", "hooks": hook(agent.HookTimeoutSeconds)}},
			"PreToolUse":        []map[string]any{{"matcher": "AskUserQuestion|ExitPlanMode", "hooks": hook(30)}},
		},
		"statusLine": sl,
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return b
}

// statusLineCommand is the status line remote-agent sets; like the hooks
// it names no binary path.
const statusLineCommand = `"$RA_EXE" statusline claude`

// StatusLineFile holds, in the session's directory, the user's own status
// line that remote-agent's runs after it.
const StatusLineFile = "statusline.json"

// statusLine is a statusLine setting.
type statusLine struct {
	Type            string   `json:"type"`
	Command         string   `json:"command"`
	Padding         *float64 `json:"padding,omitempty"`
	RefreshInterval *float64 `json:"refreshInterval,omitempty"`
}

// userStatusLine is the status line the user's settings give a session in
// cwd, by Claude Code's precedence below the command line: managed
// settings, the project's local and shared settings, the user's. managed
// reports that managed settings set it, which then override remote-agent's
// too.
func (a *Adapter) userStatusLine(cwd string) (sl *statusLine, managed bool) {
	files := []string{managedSettings}
	if cwd != "" {
		files = append(files, filepath.Join(cwd, ".claude", "settings.local.json"), filepath.Join(cwd, ".claude", "settings.json"))
	}
	files = append(files, filepath.Join(a.home, "settings.json"))
	for i, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var st struct {
			StatusLine *statusLine `json:"statusLine"`
		}
		if json.Unmarshal(b, &st) == nil && st.StatusLine != nil {
			return st.StatusLine, i == 0
		}
	}
	return nil, false
}

// ManagedStatusLine reports whether managed settings set the status line,
// which keeps remote-agent from reading the account's limits.
func (a *Adapter) ManagedStatusLine(cwd string) bool {
	_, managed := a.userStatusLine(cwd)
	return managed
}

// Argv starts Claude Code with remote-agent's hooks and a session ID
// chosen here. Resuming continues the conversation when there is one,
// with the original options minus positional arguments such as a first
// prompt, then the model, effort and permission mode it last ran with.
func (a *Adapter) Argv(l *agent.Launch) ([]string, error) {
	user, _ := a.userStatusLine(l.Cwd)
	own := []byte("{}")
	if user != nil && user.Type == "command" && user.Command != "" {
		own, _ = json.Marshal(user)
	}
	if err := os.WriteFile(filepath.Join(l.Dir, StatusLineFile), own, 0o600); err != nil {
		return nil, err
	}
	path := filepath.Join(l.Dir, SettingsFile)
	if err := os.WriteFile(path, settings(user), 0o600); err != nil {
		return nil, err
	}
	if l.SessionID == "" {
		l.SessionID = newUUID()
	}
	if !l.Resume {
		return append([]string{"--settings", path, "--session-id", l.SessionID}, l.Args...), nil
	}
	drop := map[string]bool{}
	var over []string
	if c := l.Context; c != nil {
		if c.Model != "" && codex.SafeValue.MatchString(c.Model) {
			over = append(over, "--model", c.Model)
			drop["--model"] = true
		}
		if c.Effort != "" && slices.Contains(efforts, c.Effort) {
			over = append(over, "--effort", c.Effort)
			drop["--effort"] = true
		}
		if c.PermissionMode != "" && (modeOf(c.PermissionMode) != pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED || c.PermissionMode == "acceptEdits") {
			over = append(over, "--permission-mode", c.PermissionMode)
			drop["--permission-mode"], drop["--dangerously-skip-permissions"] = true, true
		}
	}
	if !l.FullAccess && !drop["--permission-mode"] && !hasFlag(l.Args, "--permission-mode") && a.defaultMode() == "bypassPermissions" {
		// Started without bypass, and the default mode has become bypass
		// since: resume as it was, not in bypass (review C9).
		over = append(over, "--permission-mode", "default")
	}
	if l.FullAccess && !slices.Contains(l.Args, "--allow-dangerously-skip-permissions") {
		// Keep bypass on offer, as it was at launch, even when resuming in
		// another mode. Whether it was is saved with the session rather
		// than read again from settings that may have changed since.
		over = append(over, "--allow-dangerously-skip-permissions")
	}
	args := append(flagArgs(l.Args, drop), over...)
	flag := "--session-id" // nothing was sent: there is no conversation yet
	if l.Transcript != "" {
		if _, err := os.Stat(l.Transcript); err == nil {
			flag = "--resume"
		}
	}
	return append([]string{"--settings", path, flag, l.SessionID}, args...), nil
}

func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// NewParser returns a transcript parser.
func (a *Adapter) NewParser() agent.Parser { return NewParser() }

// SubmitLog is the transcript itself: submitted messages show up there
// at once, or as queued while a turn runs.
func (a *Adapter) SubmitLog(transcript string) string { return transcript }

// Submitted reports whether a transcript line records, as typed, text
// that match accepts: a user message, a message queued while a turn runs,
// or one taken into it. The text keeps its "[Image #N]" placeholders.
func (a *Adapter) Submitted(line []byte, match func(recorded string) bool, _ string) bool {
	var r struct {
		Type      string  `json:"type"`
		Operation string  `json:"operation"`
		Content   string  `json:"content"`
		Origin    *origin `json:"origin"`
		Message   *struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Attachment *struct {
			Type   string          `json:"type"`
			Prompt json.RawMessage `json:"prompt"`
		} `json:"attachment"`
	}
	if json.Unmarshal(line, &r) != nil {
		return false
	}
	switch r.Type {
	case "queue-operation":
		return r.Operation == "enqueue" && match(r.Content)
	case "attachment":
		return r.Attachment != nil && r.Attachment.Type == "queued_command" && match(contentText(r.Attachment.Prompt))
	case "user":
		if r.Origin == nil || r.Origin.Kind != "human" || r.Message == nil {
			return false
		}
		return match(contentText(r.Message.Content))
	}
	return false
}

// contentText is the text of message content: a string, or the text of
// its blocks.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// HookEvents turns AskUserQuestion and ExitPlanMode calls, reported by
// PreToolUse before Claude Code shows them, into a question and a plan:
// the transcript may get them only after they are answered.
func (a *Adapter) HookEvents(h *agent.Hook) []*pb.Event {
	if h.HookEventName != "PreToolUse" || h.ToolUseID == "" {
		return nil
	}
	switch h.ToolName {
	case "AskUserQuestion":
		q, err := ParseQuestions(h.ToolUseID, h.ToolInput)
		if err != nil {
			return nil
		}
		return []*pb.Event{{Kind: &pb.Event_QuestionAsked{QuestionAsked: q}}}
	case "ExitPlanMode":
		return []*pb.Event{{Kind: &pb.Event_PlanProposed{PlanProposed: PlanFromInput(h.ToolUseID, h.ToolInput)}}}
	}
	return nil
}

// ForwardPermission forwards every permission prompt: Claude Code only
// asks when its mode does not decide by itself. AskUserQuestion and
// ExitPlanMode ask too, but they are answered as a question and a plan.
func (a *Adapter) ForwardPermission(h *agent.Hook, _ *pb.TurnContext) bool {
	return h.ToolName != "AskUserQuestion" && h.ToolName != "ExitPlanMode"
}

// ApplySettings records a change made through remote-agent until the
// transcript shows it.
func (a *Adapter) ApplySettings(ctx *pb.TurnContext, ch *pb.SettingsChanged) *pb.TurnContext {
	return applySettings(ctx, ch)
}

func applySettings(ctx *pb.TurnContext, ch *pb.SettingsChanged) *pb.TurnContext {
	if ctx == nil {
		ctx = &pb.TurnContext{}
	}
	ctx = proto.CloneOf(ctx)
	if ch.Model != "" {
		ctx.Model, ctx.Effort = ch.Model, ch.Effort
	}
	if name, ok := permissionModes[ch.Mode]; ok {
		ctx.PermissionMode = name
	}
	return ctx
}

func (a *Adapter) Analyze(styled string) agent.Screen           { return AnalyzeScreen(styled) }
func (a *Adapter) Paste(text string) []byte                     { return agent.PasteBytes(text) }
func (a *Adapter) ComposerText(styled, _ string) (string, bool) { return ComposerText(styled) }
func (a *Adapter) ClearInput() []byte                           { return []byte(agent.KeyClearLine) }

// Interrupt is Esc. Only sent while a turn runs: pressed twice in an idle
// input box it opens the rewind menu.
func (a *Adapter) Interrupt() []byte { return []byte(agent.KeyEsc) }

// DoctorModels: haiku, which has no effort levels, and sonnet at low
// effort to switch to.
func (a *Adapter) DoctorModels() (string, string, string, string) {
	return "haiku", "", "sonnet", "low"
}

// DoctorModes: from manual through accept edits to plan, and back; haiku
// offers no auto mode.
func (a *Adapter) DoctorModes() []pb.ApprovalMode {
	return []pb.ApprovalMode{pb.ApprovalMode_APPROVAL_MODE_PLAN, pb.ApprovalMode_APPROVAL_MODE_ASK}
}

// StartupKeys trust the folder the way a local user would: the cursor
// starts on "No, exit".
func (a *Adapter) StartupKeys(popup string) []string {
	if popup == "trust folder" {
		return []string{"Down", "Enter"}
	}
	return nil
}

// Account describes the login Claude Code keeps in .claude.json: a short
// fingerprint of the account and organization (never the IDs or the
// address themselves), the organization type as the plan, and whether it
// is a subscription (OAuth) login. Unknown parts are empty.
func (a *Adapter) Account() (fingerprint, plan string, subscription bool) {
	path := filepath.Join(a.home, ".claude.json")
	if filepath.Base(a.home) == ".claude" {
		path = filepath.Join(filepath.Dir(a.home), ".claude.json")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	var st struct {
		OAuth *struct {
			AccountUUID      string `json:"accountUuid"`
			OrganizationUUID string `json:"organizationUuid"`
			OrganizationType string `json:"organizationType"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(b, &st) != nil || st.OAuth == nil || st.OAuth.AccountUUID == "" {
		return "", "", false
	}
	sum := sha256.Sum256([]byte("remote-agent/quota-account\x00" + st.OAuth.AccountUUID + "\x00" + st.OAuth.OrganizationUUID))
	return hex.EncodeToString(sum[:6]), st.OAuth.OrganizationType, true
}
