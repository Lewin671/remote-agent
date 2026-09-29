package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/paths"
)

// codexVersion is `codex --version` without the program name, read once.
func (d *Daemon) codexVersion() string {
	d.versionOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, d.cfg.CodexBin, "--version").Output()
		if err == nil {
			f := strings.Fields(string(out))
			if len(f) > 0 {
				d.version = f[len(f)-1]
			}
		}
	})
	return d.version
}

func verifiedPath() string { return filepath.Join(paths.Dir(), "verified-codex.json") }

// VerifiedVersions are the Codex versions `remote-agent doctor` passed on
// this machine.
func VerifiedVersions() []string {
	var vs []string
	if b, err := os.ReadFile(verifiedPath()); err == nil {
		_ = json.Unmarshal(b, &vs)
	}
	return vs
}

// MarkVerified records that doctor passed with version.
func MarkVerified(version string) error {
	vs := VerifiedVersions()
	if slices.Contains(vs, version) {
		return nil
	}
	b, _ := json.Marshal(append(vs, version))
	return writeFileAtomic(verifiedPath(), b)
}

// codexVerified reports whether this Codex version is known to work: the
// code was tested against it, or doctor passed with it here.
func (d *Daemon) codexVerified() bool {
	v := d.codexVersion()
	return v != "" && (slices.Contains(codex.TestedVersions, v) || slices.Contains(VerifiedVersions(), v))
}
