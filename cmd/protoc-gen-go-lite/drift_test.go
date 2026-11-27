package main

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestLiteGenNoDrift verifies that clientcore/gen/remoteagent matches a clean
// `buf generate --template buf.gen.lite.yaml` run. Skips only when `buf` is
// absent from PATH (CI installs buf in release.yml go-checks).
func TestLiteGenNoDrift(t *testing.T) {
	bufBin, err := exec.LookPath("buf")
	if err != nil {
		t.Skip("buf not found on PATH; skipping lite-gen drift check")
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	cfgPath := filepath.Join(root, "buf.gen.lite.yaml")
	rawCfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read %s: %v", cfgPath, err)
	}
	if !strings.Contains(string(rawCfg), "out: clientcore/gen") {
		t.Fatalf("unexpected buf.gen.lite.yaml (missing 'out: clientcore/gen')")
	}

	tmpDir := t.TempDir()
	tmpOut := filepath.Join(tmpDir, "gen")
	if err := os.MkdirAll(tmpOut, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", tmpOut, err)
	}
	tmpCfg := filepath.Join(tmpDir, "buf.gen.lite.yaml")
	rewritten := strings.Replace(string(rawCfg), "out: clientcore/gen", "out: "+tmpOut, 1)
	if err := os.WriteFile(tmpCfg, []byte(rewritten), 0o644); err != nil {
		t.Fatalf("write temp buf config: %v", err)
	}

	cmd := exec.Command(bufBin, "generate", "--template", tmpCfg)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("buf generate failed: %v\n%s", err, string(out))
	}

	want := collectFiles(t, filepath.Join(tmpOut, "remoteagent"))
	have := collectFiles(t, filepath.Join(root, "clientcore", "gen", "remoteagent"))

	var extra, missing, changed []string
	for k := range have {
		if _, ok := want[k]; !ok {
			extra = append(extra, k)
		}
	}
	for k, wantBytes := range want {
		haveBytes, ok := have[k]
		if !ok {
			missing = append(missing, k)
		} else if !bytes.Equal(haveBytes, wantBytes) {
			changed = append(changed, k)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	sort.Strings(changed)
	if len(extra) > 0 || len(missing) > 0 || len(changed) > 0 {
		t.Fatalf("clientcore/gen/remoteagent drifted from buf.gen.lite.yaml: extra=%v missing=%v changed=%v (re-run: rm -rf clientcore/gen/remoteagent && buf generate --template buf.gen.lite.yaml)", extra, missing, changed)
	}
}

func collectFiles(t *testing.T, base string) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte)
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = raw
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk %s: %v", base, err)
	}
	return out
}
