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
// the main module's internals. Forbidden packages are banned as direct imports
// anywhere under clientcore/ or shared/, and may appear transitively only behind
// explicit gateways (crypto/ed25519, compress/flate in shared/devicesec) whose
// sensitive paths are dead-code-banned by shared/deps_test.go.
func TestCoreWasmDependencies(t *testing.T) {
	const coreModule = "github.com/Lewin671/remote-agent/"
	forbidden := map[string]bool{
		"fmt": true, "reflect": true, "encoding/json": true, "encoding/hex": true,
		"encoding/binary": true, "crypto/rand": true, "math/rand": true, "math/rand/v2": true,
		"math/big": true, "net": true, "regexp": true, "context": true,
		"crypto/ecdh": true, "crypto/hpke": true, "log": true, "log/slog": true,
		"os": true,
	}
	gateways := map[string][]string{
		"crypto/ed25519": nil,
		"compress/flate": {"shared/devicesec"},
	}
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}\t{{join .Imports \" \"}}", "./cmd/corewasm")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	graph := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, imports, _ := strings.Cut(line, "\t")
		graph[pkg] = strings.Fields(imports)
	}
	for pkg, imports := range graph {
		if !strings.HasPrefix(pkg, coreModule+"clientcore/") && !strings.HasPrefix(pkg, coreModule+"shared/") {
			continue
		}
		for _, imp := range imports {
			if forbidden[imp] || strings.HasPrefix(imp, "net/") ||
				strings.HasPrefix(imp, "google.golang.org/protobuf") ||
				strings.HasPrefix(imp, coreModule+"internal") {
				t.Errorf("%s directly imports forbidden package %s", pkg, imp)
			}
		}
	}
	allowedViaGateway := func(importer, imp string) bool {
		only, ok := gateways[imp]
		if !ok {
			return false
		}
		if len(only) == 0 {
			return true
		}
		rel := strings.TrimPrefix(importer, coreModule)
		for _, prefix := range only {
			if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
				return true
			}
		}
		return false
	}
	type step struct{ pkg, via string }
	var queue []step
	seen := map[string]bool{}
	for pkg := range graph {
		if strings.HasPrefix(pkg, coreModule+"clientcore/") || strings.HasPrefix(pkg, coreModule+"shared/") {
			queue = append(queue, step{pkg, pkg})
			seen[pkg] = true
		}
	}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for _, imp := range graph[s.pkg] {
			if seen[imp] {
				continue
			}
			if (strings.HasPrefix(s.pkg, coreModule+"clientcore/") || strings.HasPrefix(s.pkg, coreModule+"shared/")) &&
				allowedViaGateway(s.pkg, imp) {
				continue
			}
			if strings.HasPrefix(imp, "crypto/internal/") {
				continue
			}
			seen[imp] = true
			queue = append(queue, step{imp, s.via})
			if forbidden[imp] || strings.HasPrefix(imp, "net/") ||
				strings.HasPrefix(imp, "google.golang.org/protobuf") ||
				strings.HasPrefix(imp, coreModule+"internal") {
				t.Errorf("core wasm %s reaches %s via %s outside permitted gateways", s.via, imp, s.pkg)
			}
		}
	}
}

// The direct-path engine is sans-IO (architecture.md, 分层): it imports only
// shared code, domain rules and standard packages that neither read clocks,
// draw randomness, start goroutines, log nor bloat the wasm build.
func TestTransportImports(t *testing.T) {
	const module = "github.com/Lewin671/remote-agent/"
	std := map[string]bool{"math": true, "slices": true, "strconv": true, "strings": true, "errors": true, "bytes": true, "sort": true, "unicode/utf8": true}
	cmd := exec.Command("go", "list", "-f", "{{join .Imports \" \"}}", "./transport/...")
	cmd.Dir = ".."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if !std[dep] && !strings.HasPrefix(dep, module+"shared/") && !strings.HasPrefix(dep, module+"clientcore/domain/") {
			t.Errorf("clientcore/transport imports %s", dep)
		}
	}
}

// Core layers depend only downward: app → screens → sync / transport →
// domain → wire (architecture.md, 分层). transport sits beside sync: sync and
// app may use it, it uses neither. Deps covers non-test imports only, so tests
// may still reach across layers.
func TestCoreLayerDependencies(t *testing.T) {
	const core = "github.com/Lewin671/remote-agent/clientcore/"
	forbidden := map[string][]string{
		"sync":      {"screens", "app"},
		"transport": {"sync", "screens", "app"},
		"domain":    {"sync", "transport", "screens", "app"},
		"screens":   {"app"},
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
