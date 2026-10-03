package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// Adapter is Codex's agent.Adapter.
type Adapter struct {
	bin     string
	home    string
	history string

	versionOnce sync.Once
	version     string
}

var _ agent.Adapter = (*Adapter)(nil)

// New returns the adapter for the codex executable bin. home is Codex's
// state directory (~/.codex); history its history.jsonl, which defaults to
// the one in home.
func New(bin, home, history string) *Adapter {
	if history == "" && home != "" {
		history = filepath.Join(home, "history.jsonl")
	}
	return &Adapter{bin: bin, home: home, history: history}
}

func (a *Adapter) Kind() pb.Agent { return pb.Agent_AGENT_CODEX }
func (a *Adapter) Name() string   { return "Codex" }
func (a *Adapter) Bin() string    { return a.bin }

// Version is `codex --version` without the program name, read once.
func (a *Adapter) Version() string {
	a.versionOnce.Do(func() {
		if a.bin == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, a.bin, "--version").Output()
		if err == nil {
			f := strings.Fields(string(out))
			if len(f) > 0 {
				a.version = f[len(f)-1]
			}
		}
	})
	return a.version
}

func (a *Adapter) Tested(v string) bool { return slices.Contains(TestedVersions, v) }

// Models reads the models Codex offers in /model from its cache.
func (a *Adapter) Models() (*pb.ListModelsResponse, error) {
	b, err := os.ReadFile(filepath.Join(a.home, "models_cache.json"))
	if err != nil {
		return nil, fmt.Errorf("read Codex's model list: %w", err)
	}
	var cache struct {
		Models []struct {
			Slug         string `json:"slug"`
			DisplayName  string `json:"display_name"`
			Description  string `json:"description"`
			Visibility   string `json:"visibility"`
			Priority     int    `json:"priority"`
			DefaultLevel string `json:"default_reasoning_level"`
			Levels       []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &cache); err != nil {
		return nil, fmt.Errorf("read Codex's model list: %w", err)
	}
	sort.SliceStable(cache.Models, func(i, j int) bool { return cache.Models[i].Priority < cache.Models[j].Priority })
	out := &pb.ListModelsResponse{DefaultModel: defaultModel(a.home)}
	for _, m := range cache.Models {
		if m.Visibility != "list" && m.Slug != out.DefaultModel {
			continue
		}
		info := &pb.ModelInfo{Slug: m.Slug, DisplayName: m.DisplayName, Description: m.Description, DefaultEffort: m.DefaultLevel}
		for _, l := range m.Levels {
			info.Efforts = append(info.Efforts, l.Effort)
		}
		out.Models = append(out.Models, info)
	}
	// A configured model can be newer than the CLI's on-disk catalog.
	// Keep it selectable without inventing unsupported reasoning levels.
	if out.DefaultModel != "" && !slices.ContainsFunc(out.Models, func(m *pb.ModelInfo) bool { return m.Slug == out.DefaultModel }) {
		out.Models = append([]*pb.ModelInfo{{Slug: out.DefaultModel, DisplayName: modelName(out.DefaultModel)}}, out.Models...)
	}
	return out, nil
}

func modelName(slug string) string {
	parts := strings.Split(slug, "-")
	for i, part := range parts {
		if strings.EqualFold(part, "gpt") {
			parts[i] = "GPT"
		} else if part != "" {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, "-")
}

var configModel = regexp.MustCompile(`(?m)^\s*model\s*=\s*"([^"]+)"`)

// defaultModel is the model set at the top of Codex's config.toml.
func defaultModel(codexHome string) string {
	b, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		return ""
	}
	// Only the part before the first table holds the top-level keys.
	top := string(b)
	if i := strings.Index(top, "\n["); i >= 0 {
		top = top[:i]
	}
	if m := configModel.FindStringSubmatch(top); m != nil {
		return m[1]
	}
	return ""
}

// displayName is the name /model shows for a model slug.
func (a *Adapter) displayName(slug string) (string, error) {
	models, err := a.Models()
	if err != nil {
		return "", err
	}
	for _, m := range models.Models {
		if m.Slug == slug {
			return m.DisplayName, nil
		}
	}
	return "", fmt.Errorf("model %s: %w", slug, agent.ErrNotFound)
}

func (a *Adapter) CheckEffort(effort string) error {
	if _, ok := effortLabels[effort]; !ok {
		return fmt.Errorf("unknown effort %q", effort)
	}
	return nil
}

// SafeValue matches model names that may go on a command line.
var SafeValue = regexp.MustCompile(`^[A-Za-z0-9._\[\]-]+$`)

// modeArgs are the codex flags that start a session in mode.
func modeArgs(mode pb.ApprovalMode) []string {
	switch mode {
	case pb.ApprovalMode_APPROVAL_MODE_ASK:
		return []string{"-c", `approvals_reviewer="user"`, "-c", `approval_policy="on-request"`, "-c", `sandbox_mode="workspace-write"`}
	case pb.ApprovalMode_APPROVAL_MODE_AUTO:
		return []string{"-c", `approvals_reviewer="auto_review"`, "-c", `approval_policy="on-request"`, "-c", `sandbox_mode="workspace-write"`}
	case pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS:
		return []string{"-c", `approvals_reviewer="user"`, "-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`}
	}
	return nil
}

// Options turns a new session's model, effort and approval mode into
// codex flags after the extra arguments.
func (a *Adapter) Options(extra []string, model, effort string, mode pb.ApprovalMode) ([]string, error) {
	if mode == pb.ApprovalMode_APPROVAL_MODE_PLAN {
		return nil, errors.New("sessions cannot start in Plan mode; switch to it once the session runs")
	}
	args := append([]string{}, extra...)
	if model != "" {
		if !SafeValue.MatchString(model) {
			return nil, fmt.Errorf("invalid model %q", model)
		}
		args = append(args, "-m", model)
	}
	if effort != "" {
		if err := a.CheckEffort(effort); err != nil {
			return nil, err
		}
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
	}
	return append(args, modeArgs(mode)...), nil
}

// FullAccess: Codex's Full Access is always on offer.
func (a *Adapter) FullAccess([]string) bool { return true }

// valueFlags are codex options that take a value.
var valueFlags = map[string]bool{
	"-c": true, "--config": true, "-m": true, "--model": true, "-p": true, "--profile": true,
	"-s": true, "--sandbox": true, "-a": true, "--ask-for-approval": true, "-C": true, "--cd": true,
	"--add-dir": true, "-i": true, "--image": true, "--enable": true, "--disable": true, "--local-provider": true,
}

// approvalFlags are codex options that set the approval mode.
var approvalFlags = []string{"-s", "--sandbox", "-a", "--ask-for-approval", "--full-auto", "--dangerously-bypass-approvals-and-sandbox", "--yolo"}

// FlagArgs keeps the options in args, except those in drop, and drops
// positional ones, which for resume would be read as a session ID or
// prompt. valueFlags names the options that take a value.
func FlagArgs(args []string, drop, valueFlags map[string]bool) []string {
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
		if !inline && valueFlags[name] && i+1 < len(args) {
			n = 2
		}
		if !drop[name] {
			out = append(out, args[i:i+n]...)
		}
		i += n - 1
	}
	return out
}

// Argv starts Codex with its hooks installed. On resume it continues the
// same conversation with its original options, minus positional arguments
// such as a first prompt, then the model, effort and approval mode it last
// ran with, which may have been changed since. Codex refuses a repeated
// -m, and its approval flags win over -c, so the options overridden are
// dropped from the original ones first. A fork takes its options the same
// way and starts with `codex fork` (codex-notes §11).
func (a *Adapter) Argv(l *agent.Launch) ([]string, error) {
	argv, err := a.argv(l)
	if err != nil || l.Exe == "" || l.RemoteSession == "" {
		return argv, err
	}
	config := "mcp_servers.remote_agent={command=" + tomlString(l.Exe) + ",args=[\"mcp\",\"--session\"," + tomlString(l.RemoteSession) + "],env_vars=[\"REMOTE_AGENT_HOME\"],enabled=true}"
	return append(argv, "-c", config), nil
}

func (a *Adapter) argv(l *agent.Launch) ([]string, error) {
	if !l.Resume && l.ForkOf == "" {
		return Args(l.Args), nil
	}
	var over []string
	drop := map[string]bool{}
	if ctx := l.Context; ctx != nil {
		if ctx.Model != "" && SafeValue.MatchString(ctx.Model) {
			over = append(over, "-m", ctx.Model)
			drop["-m"], drop["--model"] = true, true
		}
		if _, ok := effortLabels[ctx.Effort]; ok {
			over = append(over, "-c", fmt.Sprintf("model_reasoning_effort=%q", ctx.Effort))
		}
		if ma := modeArgs(exactMode(ctx)); ma != nil {
			over = append(over, ma...)
			for _, f := range approvalFlags {
				drop[f] = true
			}
		}
	}
	args := append(FlagArgs(l.Args, drop, valueFlags), over...)
	if l.ForkOf != "" {
		argv := append([]string{"fork"}, Args(args)...)
		return append(argv, l.ForkOf), nil
	}
	if l.SessionID == "" {
		// Nothing was ever sent: there is no conversation to resume.
		return Args(args), nil
	}
	argv := append([]string{"resume"}, Args(args)...)
	return append(argv, l.SessionID), nil
}

// exactMode is the approval mode whose settings the turn context has
// exactly, or UNSPECIFIED. Unlike contextMode it does not round, so that
// resuming never loosens a sandbox, e.g. a read-only one.
func exactMode(c *pb.TurnContext) pb.ApprovalMode {
	m := contextMode(c)
	want := fmt.Sprintf("-c|approvals_reviewer=%q|-c|approval_policy=%q|-c|sandbox_mode=%q", c.ApprovalsReviewer, c.ApprovalPolicy, c.Sandbox)
	if strings.Join(modeArgs(m), "|") != want {
		return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
	}
	return m
}

// contextMode is the permission level a turn context shows, ignoring Plan
// mode, which is not kept across a restart.
func contextMode(c *pb.TurnContext) pb.ApprovalMode {
	switch {
	case c.Sandbox == "danger-full-access" && c.ApprovalPolicy == "never":
		return pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS
	case c.ApprovalsReviewer == "auto_review":
		return pb.ApprovalMode_APPROVAL_MODE_AUTO
	case c.ApprovalsReviewer == "user":
		return pb.ApprovalMode_APPROVAL_MODE_ASK
	}
	return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
}

// Mode is the client-facing approval mode: Plan while Plan mode is on,
// else the permission level.
func (a *Adapter) Mode(c *pb.TurnContext) pb.ApprovalMode {
	if c == nil {
		return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
	}
	if c.CollaborationMode == "plan" {
		return pb.ApprovalMode_APPROVAL_MODE_PLAN
	}
	return contextMode(c)
}

// NewParser returns a rollout parser.
func (a *Adapter) NewParser() agent.Parser { return NewParser() }

// SubmitLog is ~/.codex/history.jsonl, where Codex appends one line per
// submitted message.
func (a *Adapter) SubmitLog(string) string { return a.history }

type historyEntry struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

// Submitted reports whether a history.jsonl line records text that match
// accepts. When the Codex session ID is known, only that session's lines
// count, so identical text sent to another session is not mistaken for
// this one's.
func (a *Adapter) Submitted(line []byte, match func(recorded string) bool, sessionID string) bool {
	var e historyEntry
	if json.Unmarshal(line, &e) != nil {
		return false
	}
	if sessionID != "" && e.SessionID != sessionID {
		return false
	}
	return match(e.Text)
}

// NormalizeText collapses white space for comparing submitted text.
func NormalizeText(s string) string { return strings.Join(strings.Fields(s), " ") }

// HookEvents: Codex writes what remote-agent needs to the rollout in time.
func (a *Adapter) HookEvents(*agent.Hook) []*pb.Event { return nil }

// Copied: a fork's rollout refers to its source's and repeats none of it
// (codex-notes §11).
func (a *Adapter) Copied(string) func([]byte) bool { return nil }

// ForwardPermission: with auto_review on, the hook stays silent and
// Codex's reviewer decides (architecture §7).
func (a *Adapter) ForwardPermission(_ *agent.Hook, ctx *pb.TurnContext) bool {
	return ctx == nil || ctx.ApprovalsReviewer != "auto_review"
}

func (a *Adapter) Analyze(styled string) agent.Screen {
	s := AnalyzeScreen(styled)
	return agent.Screen{
		ComposerReady: s.ComposerReady, DraftEmpty: s.DraftEmpty, Popup: s.Popup,
		QuestionOpen: s.QuestionOpen, PlanOpen: s.PlanOpen, PermissionOpen: s.Popup == "approval", Footer: s.Footer,
	}
}

func (a *Adapter) Paste(text string) []byte { return agent.PasteBytes(text) }

func (a *Adapter) ComposerText(styled, footer string) (string, bool) {
	return ComposerText(styled, footer)
}
func (a *Adapter) ClearInput() []byte { return []byte(agent.KeyClearLine) }

// Interrupt is Esc; never Ctrl-C, which clears the input box and, pressed
// again, quits Codex.
func (a *Adapter) Interrupt() []byte { return []byte(agent.KeyEsc) }

// DoctorModels: GPT-6-Luna at low effort, and medium to switch to.
func (a *Adapter) DoctorModels() (string, string, string, string) {
	return "gpt-6-luna", "low", "gpt-6-luna", "medium"
}

func (a *Adapter) DoctorModes() []pb.ApprovalMode {
	return []pb.ApprovalMode{pb.ApprovalMode_APPROVAL_MODE_AUTO, pb.ApprovalMode_APPROVAL_MODE_PLAN, pb.ApprovalMode_APPROVAL_MODE_ASK}
}

// StartupKeys answer Codex's startup popups the way a local user would:
// trust the folder, trust the hooks, skip the update.
func (a *Adapter) StartupKeys(popup string) []string {
	switch popup {
	case "trust folder":
		return []string{"Enter"}
	case "hooks need review":
		return []string{"Down", "Enter"}
	case "update available":
		return []string{"Escape"}
	}
	return nil
}
