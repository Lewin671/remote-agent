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
// passed on this machine.
func verifiedPath(k pb.Agent) string {
	name := "codex"
	if k == pb.Agent_AGENT_CLAUDE {
		name = "claude"
	}
	return filepath.Join(paths.Dir(), "verified-"+name+".json")
}

// VerifiedVersions are the versions of a CLI that `remote-agent doctor`
// passed on this machine.
func VerifiedVersions(k pb.Agent) []string {
	var vs []string
	if b, err := os.ReadFile(verifiedPath(k)); err == nil {
		_ = json.Unmarshal(b, &vs)
	}
	return vs
}

// MarkVerified records that doctor passed with version.
func MarkVerified(k pb.Agent, version string) error {
	vs := VerifiedVersions(k)
	if slices.Contains(vs, version) {
		return nil
	}
	b, _ := json.Marshal(append(vs, version))
	return writeFileAtomic(verifiedPath(k), b)
}

// verified reports whether the installed version of a CLI is known to
// work: the code was tested against it, or doctor passed with it here.
func (d *Daemon) verified(a agent.Adapter) bool {
	v := a.Version()
	return v != "" && (a.Tested(v) || slices.Contains(VerifiedVersions(a.Kind()), v))
}

// Version is the installed version of a CLI.
func (d *Daemon) Version(k pb.Agent) string { return d.adapter(k).Version() }
