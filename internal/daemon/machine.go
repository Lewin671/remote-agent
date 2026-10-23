package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/contextio"
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

func settingsPath() string { return filepath.Join(paths.Dir(), "settings.json") }

// settings returns the daemon settings, defaults filled in.
func (d *Daemon) settings() *pb.DaemonSettings {
	st, _ := d.settingsContext(context.Background())
	if st == nil {
		st = &pb.DaemonSettings{ApprovalTimeoutSeconds: int32(d.cfg.ApprovalTimeout / time.Second), ImageUploadQuotaBytes: maxUploadStorage}
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
		b, err := contextio.ReadFile(ctx, settingsPath())
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
	return st, nil
}

// maxApprovalTimeout keeps the timeout below the hook's own, after which
// the CLI would ask in the terminal instead (architecture §7).
var maxApprovalTimeout = time.Duration(agent.HookTimeoutSeconds-60) * time.Second

func (d *Daemon) updateSettings(st *pb.DaemonSettings) (*pb.DaemonSettings, error) {
	for _, n := range []int64{st.ImageUploadQuotaBytes} {
		if n != 0 && (n < 32<<20 || n > 1024<<30) {
			return nil, errors.New("image storage limits must be between 32 MiB and 1 TiB")
		}
	}
	t := time.Duration(st.ApprovalTimeoutSeconds) * time.Second
	if st.ApprovalTimeoutSeconds != 0 && (t < 30*time.Second || t > maxApprovalTimeout) {
		return nil, fmt.Errorf("approval timeout must be between 30s and %s", maxApprovalTimeout)
	}
	if st.DefaultEffort != "" {
		if err := d.agents[pb.Agent_AGENT_CODEX].CheckEffort(st.DefaultEffort); err != nil {
			return nil, err
		}
	}
	if st.DefaultMode == pb.ApprovalMode_APPROVAL_MODE_PLAN || st.GetClaude().GetMode() == pb.ApprovalMode_APPROVAL_MODE_PLAN {
		return nil, errors.New("new sessions cannot start in Plan mode")
	}
	if c := st.GetClaude(); c != nil {
		if err := d.agents[pb.Agent_AGENT_CLAUDE].CheckEffort(c.Effort); err != nil {
			return nil, err
		}
		if c.Model != "" && !codex.SafeValue.MatchString(c.Model) {
			return nil, fmt.Errorf("invalid model %q", c.Model)
		}
	}
	return d.saveSettings(st, writeFileAtomic)
}

// saveSettings commits the file and its in-memory projection together.
func (d *Daemon) saveSettings(st *pb.DaemonSettings, write func(string, []byte) error) (*pb.DaemonSettings, error) {
	d.settingsCommitMu.Lock()
	defer d.settingsCommitMu.Unlock()
	b, err := protojson.Marshal(st)
	if err != nil {
		return nil, err
	}
	if err := write(settingsPath(), b); err != nil {
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
	def := st.DefaultMode
	if ag.Kind() == pb.Agent_AGENT_CLAUDE {
		def = st.GetClaude().GetMode()
	}
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

func (d *Daemon) sessionArgs(ag agent.Adapter, req *pb.CreateSessionRequest) ([]string, error) {
	return d.sessionArgsContext(context.Background(), ag, req)
}

func (d *Daemon) sessionArgsContext(ctx context.Context, ag agent.Adapter, req *pb.CreateSessionRequest) ([]string, error) {
	st, err := d.settingsContext(ctx)
	if err != nil {
		return nil, err
	}
	model, effort, mode := req.Model, req.Effort, req.Mode
	def := &pb.AgentDefaults{Model: st.DefaultModel, Effort: st.DefaultEffort, Mode: st.DefaultMode}
	if ag.Kind() == pb.Agent_AGENT_CLAUDE {
		def = st.GetClaude()
		if def == nil {
			def = &pb.AgentDefaults{}
		}
	}
	if model == "" {
		model = def.Model
	}
	if effort == "" {
		effort = def.Effort
	}
	if mode == pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		mode = def.Mode
	}
	return contextio.Read(ctx, func() ([]string, error) { return ag.Options(req.Args, model, effort, mode) })
}
