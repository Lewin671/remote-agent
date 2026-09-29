package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/paths"
)

// verifiedPath lists the versions of a CLI that `remote-agent doctor`
// passed on this machine, in the state directory dir.
func verifiedPath(dir string, k pb.Agent) string {
	name := "codex"
	if k == pb.Agent_AGENT_CLAUDE {
		name = "claude"
	}
	return filepath.Join(dir, "verified-"+name+".json")
}

func verifiedVersions(dir string, k pb.Agent) []string {
	var vs []string
	if b, err := os.ReadFile(verifiedPath(dir, k)); err == nil {
		_ = json.Unmarshal(b, &vs)
	}
	return vs
}

// MarkVerified records in the state directory dir that doctor passed with
// version.
func MarkVerified(dir string, k pb.Agent, version string) error {
	vs := verifiedVersions(dir, k)
	if slices.Contains(vs, version) {
		return nil
	}
	b, _ := json.Marshal(append(vs, version))
	return writeFileAtomic(verifiedPath(dir, k), b)
}

// verified reports whether the installed version of a CLI is known to
// work: the code was tested against it, or doctor passed with it here.
func (d *Daemon) verified(a agent.Adapter) bool {
	v := a.Version()
	return v != "" && (a.Tested(v) || slices.Contains(verifiedVersions(paths.Dir(), a.Kind()), v))
}

// Version is the installed version of a CLI.
func (d *Daemon) Version(k pb.Agent) string { return d.adapter(k).Version() }
