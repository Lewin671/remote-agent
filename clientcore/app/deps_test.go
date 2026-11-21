package app

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The wasm build must not pull in packages that bloat it or break the core's
// rules (architecture.md, 体积与性能预算): fmt, reflect, math/big, math/rand*, and
// crypto/rand stay out except behind the single explicit crypto/ed25519 gateway
// imported solely by shared/peerwire (needed for Ed25519 verify in direct
// signaling). No package may bypass this check via crypto/internal/*.
func TestCoreWasmDependencies(t *testing.T) {
	const coreModule = "github.com/Lewin671/remote-agent/"
	neverAllowed := map[string]bool{
		"encoding/json": true, "encoding/hex": true, "encoding/binary": true,
		"net": true, "regexp": true, "context": true,
		"crypto/ecdh": true, "crypto/hpke": true, "log": true, "log/slog": true,
	}
	forbidden := map[string]bool{
		"fmt": true, "reflect": true, "crypto/rand": true,
		"math/rand": true, "math/rand/v2": true, "math/big": true,
		"os": true,
	}
	for k := range neverAllowed {
		forbidden[k] = true
	}
	const ed25519GatewayPkg = coreModule + "shared/peerwire"

	isDisallowedPrefix := func(imp string) bool {
		return strings.HasPrefix(imp, "net/") ||
			strings.HasPrefix(imp, "google.golang.org/protobuf") ||
			strings.HasPrefix(imp, coreModule+"internal")
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
	edImporters := map[string]bool{}
	for pkg, imports := range graph {
		for _, imp := range imports {
			if imp == "crypto/ed25519" {
				edImporters[pkg] = true
				if pkg != ed25519GatewayPkg {
					t.Errorf("%s imports crypto/ed25519; only %s may import it", pkg, ed25519GatewayPkg)
				}
			}
			if neverAllowed[imp] || isDisallowedPrefix(imp) {
				t.Errorf("core wasm dependency %s imports forbidden package %s", pkg, imp)
			}
			if (strings.HasPrefix(pkg, coreModule+"clientcore/") || strings.HasPrefix(pkg, coreModule+"shared/")) && forbidden[imp] {
				t.Errorf("%s directly imports forbidden package %s", pkg, imp)
			}
		}
	}
	if !edImporters[ed25519GatewayPkg] {
		t.Fatalf("expected %s to import crypto/ed25519 in wasm build graph", ed25519GatewayPkg)
	}

	// BFS every clientcore/shared package in the wasm build without skipping crypto/internal/*.
	// Only two exact edges are exempted:
	//   1) shared/peerwire -> crypto/ed25519 (reaches fmt/reflect/math/big/math/rand*/crypto/rand),
	//   2) crypto/internal/sysrand -> os, time (reached under Go 1.26 FIPS AES/HMAC/SHA256).
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
			if s.pkg == ed25519GatewayPkg && imp == "crypto/ed25519" {
				continue
			}
			if s.pkg == "crypto/internal/sysrand" && (imp == "os" || imp == "time") {
				continue
			}
			if forbidden[imp] || isDisallowedPrefix(imp) {
				t.Errorf("core wasm %s reaches %s via %s outside permitted gateways", s.via, imp, s.pkg)
			}
			if seen[imp] {
				continue
			}
			seen[imp] = true
			queue = append(queue, step{imp, s.via})
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
