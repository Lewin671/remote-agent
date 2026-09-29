package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/paths"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// --- models ---

// listModels reads the models Codex offers in /model from its cache.
func (d *Daemon) listModels() (*pb.ListModelsResponse, error) {
	b, err := os.ReadFile(filepath.Join(d.cfg.CodexHome, "models_cache.json"))
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
	out := &pb.ListModelsResponse{DefaultModel: codexDefaultModel(d.cfg.CodexHome)}
	for _, m := range cache.Models {
		if m.Visibility != "list" {
			continue
		}
		info := &pb.ModelInfo{Slug: m.Slug, DisplayName: m.DisplayName, Description: m.Description, DefaultEffort: m.DefaultLevel}
		for _, l := range m.Levels {
			info.Efforts = append(info.Efforts, l.Effort)
		}
		out.Models = append(out.Models, info)
	}
	return out, nil
}

var configModel = regexp.MustCompile(`(?m)^\s*model\s*=\s*"([^"]+)"`)

// codexDefaultModel is the model set at the top of Codex's config.toml.
func codexDefaultModel(codexHome string) string {
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
func (d *Daemon) displayName(slug string) (string, error) {
	models, err := d.listModels()
	if err != nil {
		return "", err
	}
	for _, m := range models.Models {
		if m.Slug == slug {
			return m.DisplayName, nil
		}
	}
	return "", fmt.Errorf("model %s: %w", slug, errNotFound)
}

// --- directories ---

// listDirectories lists the folders in a directory, hidden ones left out.
func listDirectories(path string) (*pb.ListDirectoriesResponse, error) {
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
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", path, err)
	}
	out := &pb.ListDirectoriesResponse{Path: path, Home: home}
	if parent := filepath.Dir(path); parent != path {
		out.Parent = parent
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			st, err := os.Stat(filepath.Join(path, e.Name()))
			isDir = err == nil && st.IsDir()
		}
		if isDir {
			out.Dirs = append(out.Dirs, e.Name())
		}
		if len(out.Dirs) >= 1000 {
			break
		}
	}
	sort.Slice(out.Dirs, func(i, j int) bool { return strings.ToLower(out.Dirs[i]) < strings.ToLower(out.Dirs[j]) })
	return out, nil
}

// --- settings ---

func settingsPath() string { return filepath.Join(paths.Dir(), "settings.json") }

// settings returns the daemon settings, defaults filled in.
func (d *Daemon) settings() *pb.DaemonSettings {
	d.settingsMu.Lock()
	defer d.settingsMu.Unlock()
	if d.cached == nil {
		st := &pb.DaemonSettings{}
		if b, err := os.ReadFile(settingsPath()); err == nil {
			_ = protojson.Unmarshal(b, st)
		}
		d.cached = st
	}
	st := proto.CloneOf(d.cached)
	if st.ApprovalTimeoutSeconds == 0 {
		st.ApprovalTimeoutSeconds = int32(d.cfg.ApprovalTimeout / time.Second)
	}
	return st
}

// maxApprovalTimeout keeps the timeout below the hook's own, after which
// Codex would ask in the terminal instead (architecture §7).
var maxApprovalTimeout = time.Duration(codex.HookTimeoutSeconds-60) * time.Second

func (d *Daemon) updateSettings(st *pb.DaemonSettings) (*pb.DaemonSettings, error) {
	t := time.Duration(st.ApprovalTimeoutSeconds) * time.Second
	if st.ApprovalTimeoutSeconds != 0 && (t < 30*time.Second || t > maxApprovalTimeout) {
		return nil, fmt.Errorf("approval timeout must be between 30s and %s", maxApprovalTimeout)
	}
	if st.DefaultEffort != "" {
		if _, ok := effortLabels[st.DefaultEffort]; !ok {
			return nil, fmt.Errorf("unknown effort %q", st.DefaultEffort)
		}
	}
	if st.DefaultMode == pb.ApprovalMode_APPROVAL_MODE_PLAN {
		return nil, errors.New("new sessions cannot start in Plan mode")
	}
	b, err := protojson.Marshal(st)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(settingsPath(), b); err != nil {
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- new sessions ---

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

// sessionArgs turns a create request's choices, or the daemon's defaults,
// into codex flags.
func (d *Daemon) sessionArgs(req *pb.CreateSessionRequest) ([]string, error) {
	st := d.settings()
	model, effort, mode := req.Model, req.Effort, req.Mode
	if model == "" && effort == "" {
		model, effort = st.DefaultModel, st.DefaultEffort
	}
	if mode == pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		mode = st.DefaultMode
	}
	if mode == pb.ApprovalMode_APPROVAL_MODE_PLAN {
		return nil, errors.New("sessions cannot start in Plan mode; switch to it once the session runs")
	}
	args := append([]string{}, req.Args...)
	if model != "" {
		if !safeValue.MatchString(model) {
			return nil, fmt.Errorf("invalid model %q", model)
		}
		args = append(args, "-m", model)
	}
	if effort != "" {
		if _, ok := effortLabels[effort]; !ok {
			return nil, fmt.Errorf("unknown effort %q", effort)
		}
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
	}
	return append(args, modeArgs(mode)...), nil
}

var safeValue = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
