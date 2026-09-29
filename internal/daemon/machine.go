package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/paths"
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
// the CLI would ask in the terminal instead (architecture §7).
var maxApprovalTimeout = time.Duration(agent.HookTimeoutSeconds-60) * time.Second

func (d *Daemon) updateSettings(st *pb.DaemonSettings) (*pb.DaemonSettings, error) {
	t := time.Duration(st.ApprovalTimeoutSeconds) * time.Second
	if st.ApprovalTimeoutSeconds != 0 && (t < 30*time.Second || t > maxApprovalTimeout) {
		return nil, fmt.Errorf("approval timeout must be between 30s and %s", maxApprovalTimeout)
	}
	if st.DefaultEffort != "" {
		if err := d.agents[pb.Agent_AGENT_CODEX].CheckEffort(st.DefaultEffort); err != nil {
			return nil, err
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

// sessionArgs turns a create request's choices, or the daemon's defaults
// for the CLI, into its arguments.
func (d *Daemon) sessionArgs(ag agent.Adapter, req *pb.CreateSessionRequest) ([]string, error) {
	st := d.settings()
	model, effort, mode := req.Model, req.Effort, req.Mode
	def := &pb.AgentDefaults{Model: st.DefaultModel, Effort: st.DefaultEffort, Mode: st.DefaultMode}
	if model == "" {
		model = def.Model
	}
	if effort == "" {
		effort = def.Effort
	}
	if mode == pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		mode = def.Mode
	}
	return ag.Options(req.Args, model, effort, mode)
}
