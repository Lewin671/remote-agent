package app

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The wasm build must not pull in packages that bloat it or break the core's
// rules (architecture.md, 体积与性能预算): fmt and reflect cost tens of KB each,
// protobuf-go brings reflection, and the core never does network I/O or uses
// the main module's internals.
func TestCoreWasmDependencies(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./cmd/corewasm")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		switch {
		case dep == "fmt", dep == "reflect", dep == "encoding/json", dep == "net", strings.HasPrefix(dep, "net/"),
			strings.HasPrefix(dep, "google.golang.org/protobuf"),
			strings.HasPrefix(dep, "github.com/Lewin671/remote-agent/internal"):
			t.Errorf("core wasm depends on %s", dep)
		}
	}
}

// Core layers depend only downward: app → screens → sync → domain → wire
// (architecture.md, 分层). Deps covers non-test imports only, so tests may
// still reach across layers.
func TestCoreLayerDependencies(t *testing.T) {
	const core = "github.com/Lewin671/remote-agent/clientcore/"
	forbidden := map[string][]string{
		"sync":    {"screens", "app"},
		"domain":  {"sync", "screens", "app"},
		"screens": {"app"},
	}
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}} {{join .Deps \" \"}}", "./...")
	cmd.Dir = ".."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	inLayer := func(pkg, layer string) bool {
		return pkg == core+layer || strings.HasPrefix(pkg, core+layer+"/")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pkg := fields[0]
		for layer, banned := range forbidden {
			if !inLayer(pkg, layer) {
				continue
			}
			for _, dep := range fields[1:] {
				for _, upper := range banned {
					if inLayer(dep, upper) {
						t.Errorf("%s depends on %s", strings.TrimPrefix(pkg, core), strings.TrimPrefix(dep, core))
					}
				}
			}
		}
	}
}
