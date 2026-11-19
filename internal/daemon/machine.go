package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/contextio"
	"github.com/Lewin671/remote-agent/internal/i18n"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// --- models ---

// listModels lists the models a CLI's sessions can switch to.
func (d *Daemon) listModels(k pb.Agent) (*pb.ListModelsResponse, error) {
	ag, err := d.knownAgent(k)
	if err != nil {
		return nil, err
	}
	return ag.Models()
}

// --- directories ---

const maxDirectoryEntries = 20000
const directoryBatchSize = 256

type directoryReader interface {
	ReadDir(int) ([]os.DirEntry, error)
	Close() error
}

// listDirectories lists the folders in a directory, hidden ones left out.
func listDirectories(ctx context.Context, path string, openDir func(string) (directoryReader, error)) (*pb.ListDirectoriesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, contextio.Timeout)
	defer cancel()
	home, _ := os.UserHomeDir()
	switch {
	case path == "" || path == "~":
		path = home
	case strings.HasPrefix(path, "~/"):
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("path must be absolute")
	}
	path = filepath.Clean(path)
	if openDir == nil {
		openDir = func(path string) (directoryReader, error) { return os.Open(path) }
	}
	return contextio.Read(ctx, func() (*pb.ListDirectoriesResponse, error) {
		dir, err := openDir(path)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", path, err)
		}
		defer dir.Close()
		out := &pb.ListDirectoriesResponse{Path: path, Home: home}
		if parent := filepath.Dir(path); parent != path {
			out.Parent = parent
		}
		scanned := 0
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			entries, readErr := dir.ReadDir(directoryBatchSize)
			if readErr != nil && readErr != io.EOF {
				return nil, fmt.Errorf("list %s: %w", path, readErr)
			}
			for _, e := range entries {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				scanned++
				if scanned > maxDirectoryEntries {
					return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("directory exceeds the 20000-entry scan limit"))
				}
				if strings.HasPrefix(e.Name(), ".") {
					continue
				}
				isDir := e.IsDir()
				if e.Type()&os.ModeSymlink != 0 {
					st, err := os.Stat(filepath.Join(path, e.Name()))
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					isDir = err == nil && st.IsDir()
				}
				if isDir {
					out.Dirs = append(out.Dirs, e.Name())
				}
			}
			if readErr == io.EOF {
				break
			}
		}
		sort.Slice(out.Dirs, func(i, j int) bool { return strings.ToLower(out.Dirs[i]) < strings.ToLower(out.Dirs[j]) })
		if len(out.Dirs) > 1000 {
			out.Dirs = out.Dirs[:1000]
		}
		return out, nil
	})
}

// --- settings ---

func (d *Daemon) settingsPath() string { return filepath.Join(d.dir, "settings.json") }

// settings returns the daemon settings, defaults filled in.
func (d *Daemon) settings() *pb.DaemonSettings {
	st, _ := d.settingsContext(context.Background())
	if st == nil {
		st = &pb.DaemonSettings{ApprovalTimeoutSeconds: int32(d.cfg.ApprovalTimeout / time.Second), ImageUploadQuotaBytes: maxUploadStorage, IdleCloseMinutes: defaultIdleCloseMinutes}
	}
	return st
}

func (d *Daemon) settingsContext(ctx context.Context) (*pb.DaemonSettings, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.settingsMu.Lock()
	st := proto.CloneOf(d.cached)
	d.settingsMu.Unlock()
	if st == nil {
		b, err := contextio.ReadFile(ctx, d.settingsPath())
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, err
		}
		loaded := &pb.DaemonSettings{}
		if err == nil {
			_ = protojson.Unmarshal(b, loaded)
		}
		d.settingsMu.Lock()
		if d.cached == nil {
			d.cached = loaded
		}
		st = proto.CloneOf(d.cached)
		d.settingsMu.Unlock()
	}
	if st.ApprovalTimeoutSeconds == 0 {
		st.ApprovalTimeoutSeconds = int32(d.cfg.ApprovalTimeout / time.Second)
	}
	if st.ImageUploadQuotaBytes == 0 {
		st.ImageUploadQuotaBytes = maxUploadStorage
	}
	if st.IdleCloseMinutes == 0 {
		st.IdleCloseMinutes = defaultIdleCloseMinutes
	}
	st.DisabledAgents = normalizeDisabledAgents(st.DisabledAgents)
	return st, nil
}

var errCannotDisableLastAgent = errors.New("cannot_disable_last_agent: at least one installed CLI must remain enabled")
var errAgentDisabled = errors.New("agent_disabled: this CLI is disabled on this machine")

func normalizeDisabledAgents(in []pb.Agent) []pb.Agent {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[pb.Agent]bool, len(in))
	var out []pb.Agent
	for _, a := range in {
		if a == pb.Agent_AGENT_UNSPECIFIED {
			continue
		}
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (d *Daemon) installedKinds() []pb.Agent {
	var out []pb.Agent
	for _, a := range d.adapterList() {
		if a != nil && a.Bin() != "" {
			out = append(out, a.Kind())
		}
	}
	return out
}

func (d *Daemon) isAgentDisabled(k pb.Agent) bool {
	k = resolveKind(k)
	st := d.settings()
	return slices.Contains(st.DisabledAgents, k)
}

func (d *Daemon) enabledCountLocked(disabled []pb.Agent, installed []pb.Agent) int {
	n := 0
	for _, k := range installed {
		if !slices.Contains(disabled, k) {
			n++
		}
	}
	return n
}

// reconcileEnabledAgents re-enables one installed CLI if the user uninstalled the
// last remaining enabled CLI locally while other disabled CLIs are still installed.
func (d *Daemon) reconcileEnabledAgents() {
	installed := d.installedKinds()
	if len(installed) == 0 {
		return
	}
	st := d.settings()
	if d.enabledCountLocked(st.DisabledAgents, installed) > 0 {
		return
	}
	var nextDisabled []pb.Agent
	for _, a := range st.DisabledAgents {
		if a != installed[0] {
			nextDisabled = append(nextDisabled, a)
		}
	}
	st.DisabledAgents = normalizeDisabledAgents(nextDisabled)
	st.Revision++
	_, _ = d.saveSettings(st, writeFileAtomic)
}

// maxApprovalTimeout keeps the timeout below the hook's own, after which
// the CLI would ask in the terminal instead (architecture §7).
var maxApprovalTimeout = time.Duration(agent.HookTimeoutSeconds-60) * time.Second

func (d *Daemon) updateSettings(st *pb.DaemonSettings) (settings *pb.DaemonSettings, resultErr error) {
	return d.updateSettingsReq(&pb.UpdateSettingsRequest{Settings: st})
}

func (d *Daemon) updateSettingsReq(req *pb.UpdateSettingsRequest) (settings *pb.DaemonSettings, resultErr error) {
	saving := false
	defer func() {
		if !saving && resultErr != nil {
			resultErr = outcome.FinalError(resultErr)
		}
	}()
	if req == nil || (req.Settings == nil && req.ToggleAgent == nil) {
		return nil, errors.New("settings are required")
	}
	if req.ToggleAgent != nil && req.ToggleEnabled == nil {
		return nil, errors.New("toggle_enabled is required with toggle_agent")
	}

	d.settingsCommitMu.Lock()
	defer d.settingsCommitMu.Unlock()

	cur, err := d.settingsContext(context.Background())
	if err != nil {
		return nil, err
	}
	var st *pb.DaemonSettings
	if req.ToggleAgent != nil {
		st = proto.CloneOf(cur)
		target := resolveKind(req.GetToggleAgent())
		enabled := req.GetToggleEnabled()
		var nextDisabled []pb.Agent
		for _, a := range st.DisabledAgents {
			if a != target {
				nextDisabled = append(nextDisabled, a)
			}
		}
		if !enabled {
			nextDisabled = append(nextDisabled, target)
		}
		st.DisabledAgents = normalizeDisabledAgents(nextDisabled)
	} else {
		st = proto.CloneOf(req.Settings)
		st.DisabledAgents = append([]pb.Agent(nil), cur.DisabledAgents...)
	}

	for _, n := range []int64{st.ImageUploadQuotaBytes} {
		if n != 0 && (n < 32<<20 || n > 1024<<30) {
			return nil, errors.New("image storage limits must be between 32 MiB and 1 TiB")
		}
	}
	if n := st.IdleCloseMinutes; n < 0 {
		st.IdleCloseMinutes = -1
	} else if n != 0 && (n < minIdleCloseMinutes || n > maxIdleCloseMinutes) {
		return nil, fmt.Errorf("idle close must be off or between %d and %d minutes", minIdleCloseMinutes, maxIdleCloseMinutes)
	}
	t := time.Duration(st.ApprovalTimeoutSeconds) * time.Second
	if st.ApprovalTimeoutSeconds != 0 && (t < 30*time.Second || t > maxApprovalTimeout) {
		return nil, fmt.Errorf("approval timeout must be between 30s and %s", maxApprovalTimeout)
	}
	for _, a := range d.adapterList() {
		def := defaultsFor(st, a.Kind())
		if def.Mode == pb.ApprovalMode_APPROVAL_MODE_PLAN {
			return nil, errors.New("new sessions cannot start in Plan mode")
		}
		if def.Effort != "" {
			if err := a.CheckEffort(def.Effort); err != nil {
				return nil, err
			}
		}
		if def.Model != "" && !agent.SafeValue.MatchString(def.Model) {
			return nil, fmt.Errorf("invalid model %q", def.Model)
		}
	}
	installed := d.installedKinds()
	if len(installed) > 0 && d.enabledCountLocked(st.DisabledAgents, installed) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errCannotDisableLastAgent)
	}
	st.Revision = cur.Revision + 1
	saving = true
	return d.saveSettingsLocked(st, writeFileAtomic)
}

// saveSettings commits the file and its in-memory projection together.
func (d *Daemon) saveSettings(st *pb.DaemonSettings, write func(string, []byte) error) (*pb.DaemonSettings, error) {
	d.settingsCommitMu.Lock()
	defer d.settingsCommitMu.Unlock()
	return d.saveSettingsLocked(st, write)
}

func (d *Daemon) saveSettingsLocked(st *pb.DaemonSettings, write func(string, []byte) error) (*pb.DaemonSettings, error) {
	b, err := protojson.Marshal(st)
	if err != nil {
		return nil, err
	}
	if err := write(d.settingsPath(), b); err != nil {
		return nil, err
	}
	d.settingsMu.Lock()
	d.cached = proto.CloneOf(st)
	d.settingsMu.Unlock()
	return d.settings(), nil
}

// approvalTimeout is the configured timeout, else the daemon's flag.
func (d *Daemon) approvalTimeout() time.Duration {
	d.settings() // loads the file once
	d.settingsMu.Lock()
	defer d.settingsMu.Unlock()
	if sec := d.cached.GetApprovalTimeoutSeconds(); sec > 0 {
		return time.Duration(sec) * time.Second
	}
	return d.cfg.ApprovalTimeout
}

func writeFileAtomic(path string, b []byte) error {
	return writeFileSync(path, b)
}

// --- new sessions ---

// sessionArgs turns a create request's choices, or the daemon's defaults
// for the CLI, into its arguments.
// defaultMode is the approval mode a session of ag started in cwd without
// one gets: the daemon's default for the CLI, else the CLI's own.
func (d *Daemon) defaultMode(ag agent.Adapter, cwd string) (pb.ApprovalMode, pb.DefaultModeSource, bool) {
	st := d.settings()
	def := defaultsFor(st, ag.Kind()).Mode
	if def != pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		return def, pb.DefaultModeSource_DEFAULT_MODE_SOURCE_DAEMON_SETTINGS, false
	}
	type observation struct {
		mode          pb.ApprovalMode
		known, varies bool
	}
	got, err := contextio.Read(context.Background(), func() (observation, error) { m, k, v := ag.DefaultMode(cwd); return observation{m, k, v}, nil })
	if err != nil {
		return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED, pb.DefaultModeSource_DEFAULT_MODE_SOURCE_UNSPECIFIED, true
	}
	m, known, varies := got.mode, got.known, got.varies
	if !known {
		return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED, pb.DefaultModeSource_DEFAULT_MODE_SOURCE_UNSPECIFIED, varies
	}
	return m, pb.DefaultModeSource_DEFAULT_MODE_SOURCE_CLI_CONFIG, varies
}

const daemonCliLocale = "en"

var agyInlineEfforts = []string{"low", "medium", "high", "xhigh", "max"}

func splitAntigravityInlineEffort(raw string) (base, inline string) {
	if strings.HasPrefix(raw, "gemini-pro-orchestrator") {
		return raw, ""
	}
	for _, eff := range agyInlineEfforts {
		if base, ok := strings.CutSuffix(raw, "-"+eff); ok && base != "" {
			return base, eff
		}
	}
	return raw, ""
}

func findCatalogModel(models []*pb.ModelInfo, raw string) *pb.ModelInfo {
	for _, m := range models {
		if m.Slug == raw {
			return m
		}
	}
	trimmed := strings.TrimSpace(raw)
	var byName *pb.ModelInfo
	for _, m := range models {
		if m.DisplayName != "" && strings.EqualFold(trimmed, m.DisplayName) {
			if byName != nil && byName.Slug != m.Slug {
				return nil
			}
			byName = m
		}
	}
	return byName
}

// shouldLoadCatalog avoids spawning an external CLI (`agy models`) when a fast
// synthetic slug is already valid and no effort/display-name lookup is needed.
func shouldLoadCatalog(ag agent.Adapter, model, effort string) bool {
	if model == "" {
		return false
	}
	if ag.Kind() != pb.Agent_AGENT_ANTIGRAVITY {
		return true
	}
	if !codex.SafeValue.MatchString(model) {
		return ag.Bin() != ""
	}
	if effort != "" {
		return ag.Bin() != "" && ag.Bin() != "agy" && ag.Bin() != "/usr/bin/true" && ag.Bin() != "/bin/true"
	}
	_, inline := splitAntigravityInlineEffort(model)
	if inline != "" {
		return ag.Bin() != "" && ag.Bin() != "agy" && ag.Bin() != "/usr/bin/true" && ag.Bin() != "/bin/true"
	}
	return false
}

// resolveModelAndEffort normalizes a model slug or client display name against
// the CLI's catalog when available and validates per-model effort support.
func resolveModelAndEffort(ag agent.Adapter, model, effort string, inheritedEffort bool) (string, string, *pb.ModelInfo, error) {
	model = strings.TrimSpace(model)
	effort = strings.TrimSpace(effort)
	if model == "" {
		return "", effort, nil, nil
	}
	var matched *pb.ModelInfo
	if shouldLoadCatalog(ag, model, effort) {
		if resp, err := ag.Models(); err == nil && resp != nil && len(resp.Models) > 0 {
			matched = findCatalogModel(resp.Models, model)
			if matched == nil && ag.Kind() == pb.Agent_AGENT_ANTIGRAVITY {
				base, inline := splitAntigravityInlineEffort(model)
				if inline != "" {
					if m := findCatalogModel(resp.Models, base); m != nil && slices.Contains(m.Efforts, inline) {
						if effort != "" && effort != inline {
							return "", "", nil, errors.New(i18n.NewSessionCliEffortUnsupportedForModel(daemonCliLocale, effort, model, inline))
						}
						matched = m
						effort = inline
					}
				}
			}
		}
	}
	if matched != nil {
		model = matched.Slug
		if effort != "" {
			// Claude and Antigravity list empty Efforts for effortless models
			// (e.g. Haiku, Orchestrator); Codex cache entries may omit levels.
			if len(matched.Efforts) == 0 && ag.Kind() != pb.Agent_AGENT_CODEX {
				if inheritedEffort {
					effort = ""
				} else {
					return "", "", matched, &agent.EffortNotSupportedError{Msg: i18n.NewSessionCliModelNoEffort(daemonCliLocale, model)}
				}
			} else if len(matched.Efforts) > 0 && !slices.Contains(matched.Efforts, effort) {
				if inheritedEffort {
					effort = ""
				} else {
					return "", "", matched, errors.New(i18n.NewSessionCliEffortUnsupportedForModel(daemonCliLocale, effort, model, strings.Join(matched.Efforts, ", ")))
				}
			}
		}
		return model, effort, matched, nil
	}
	if !codex.SafeValue.MatchString(model) {
		return "", "", nil, errors.New(i18n.NewSessionCliModelInvalid(daemonCliLocale, ag.ID(), model))
	}
	return model, effort, nil, nil
}

func (d *Daemon) sessionArgs(ag agent.Adapter, req *pb.CreateSessionRequest) ([]string, error) {
	return d.sessionArgsContext(context.Background(), ag, req)
}

func (d *Daemon) sessionArgsContext(ctx context.Context, ag agent.Adapter, req *pb.CreateSessionRequest) ([]string, error) {
	st, err := d.settingsContext(ctx)
	if err != nil {
		return nil, err
	}
	model, effort, mode := req.Model, req.Effort, req.Mode
	def := defaultsFor(st, ag.Kind())
	inheritedEffort := false
	if model == "" {
		model = def.Model
		if effort == "" && def.Effort != "" {
			effort, inheritedEffort = def.Effort, true
		}
	} else if effort == "" && model == def.Model && def.Effort != "" {
		effort, inheritedEffort = def.Effort, true
	}
	if mode == pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		mode = def.Mode
	}
	return contextio.Read(ctx, func() ([]string, error) {
		resolvedModel, resolvedEffort, _, err := resolveModelAndEffort(ag, model, effort, inheritedEffort)
		if err != nil {
			return nil, err
		}
		args, err := ag.Options(req.Args, resolvedModel, resolvedEffort, mode)
		var noEffort *agent.EffortNotSupportedError
		if inheritedEffort && errors.As(err, &noEffort) {
			return ag.Options(req.Args, resolvedModel, "", mode)
		}
		return args, err
	})
}
