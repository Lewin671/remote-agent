// Package shared holds the sans-IO Go code that both the daemon (main module)
// and clientcore compile. This test is its only file at the root.
package shared

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/Lewin671/remote-agent/"

// allowed lists every standard library package a shared production file may
// import directly. A non-empty value restricts it to those shared packages.
// Anything that reads clocks, draws randomness, runs goroutines, locks, does
// I/O, or pulls fmt/reflect into the wasm core stays out; see forbidden.
// io is allowed for its interfaces only (a caller-injected entropy Reader).
var allowed = map[string][]string{
	"bytes":          nil,
	"errors":         nil,
	"io":             nil,
	"slices":         nil,
	"sort":           nil,
	"strings":        nil,
	"strconv":        nil,
	"unicode/utf8":   nil,
	"math":           nil,
	"math/bits":      nil,
	"crypto/sha256":  nil,
	"crypto/hmac":    nil,
	"crypto/hkdf":    nil,
	"crypto/aes":     nil,
	"crypto/cipher":  nil,
	"crypto/ed25519": nil,
	"compress/flate": {"shared/devicesec"},
}

// forbidden documents why the obvious candidates are excluded; the test also
// fails if one of them is ever added to allowed.
var forbidden = []string{
	"time", "os", "net", "fmt", "reflect", "regexp", "context", "sync", "sync/atomic", "unsafe",
	"encoding/hex", "encoding/binary", "encoding/json", "crypto/rand", "crypto/ecdh", "crypto/hpke",
	"math/rand", "math/rand/v2", "math/big", "log", "log/slog", "runtime",
}

// Packages that may appear in the transitive closure only behind a gateway.
// crypto/ed25519 imports crypto/rand (and so math/big, fmt, reflect) for key
// generation, and compress/flate imports fmt; shared code calls neither path
// (see disallowedCalls), so linking cost and behavior stay out of the core.
var sensitive = []string{
	"crypto/rand", "math/rand", "math/rand/v2", "math/big", "time", "os", "fmt", "reflect",
	"net", "regexp", "context", "encoding/hex", "encoding/binary", "crypto/ecdh", "crypto/hpke", "log", "log/slog",
}

// gateways may bring sensitive packages in; the value restricts the importer.
var gateways = map[string][]string{
	"crypto/ed25519": nil,
	"compress/flate": {"shared/devicesec"},
}

// disallowedCalls read the process-global random source even though their
// package is allowed.
var disallowedCalls = map[string][]string{
	"crypto/ed25519": {"GenerateKey"},
	"crypto/cipher":  {"NewGCMWithRandomNonce"},
	"compress/flate": {"NewWriter", "NewWriterDict"},
}

func TestSharedImportsOnlyAllowedPackages(t *testing.T) {
	for _, p := range forbidden {
		if _, ok := allowed[p]; ok {
			t.Fatalf("%s is both allowed and forbidden", p)
		}
	}
	packages := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		dir := filepath.ToSlash(filepath.Join("shared", filepath.Dir(path)))
		packages[dir] = true
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		names := map[string]string{} // local name -> import path
		for _, spec := range f.Imports {
			imp, _ := strconv.Unquote(spec.Path.Value)
			if !importAllowed(allowed, dir, imp) {
				t.Errorf("%s imports %q", filepath.ToSlash(filepath.Join("shared", path)), imp)
			}
			name := imp[strings.LastIndex(imp, "/")+1:]
			if spec.Name != nil {
				name = spec.Name.Name
			}
			names[name] = imp
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok {
				for _, bad := range disallowedCalls[names[x.Name]] {
					if sel.Sel.Name == bad {
						t.Errorf("%s uses %s.%s, which reads the global random source", filepath.ToSlash(filepath.Join("shared", path)), names[x.Name], bad)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"shared/peerwire", "shared/linkcore", "shared/devicesec"} {
		if !packages[want] {
			t.Errorf("guard did not see %s", want)
		}
	}
}

// Transitive dependencies, for the host and for the wasm core: a sensitive
// package may be reachable only through a gateway.
func TestSharedTransitiveDependenciesPassOnlyThroughGateways(t *testing.T) {
	for _, target := range [][]string{nil, {"GOOS=js", "GOARCH=wasm"}} {
		cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}\t{{join .Imports \" \"}}", "./...")
		cmd.Env = append(os.Environ(), target...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list %v: %v", target, err)
		}
		graph := map[string][]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			pkg, imports, _ := strings.Cut(line, "\t")
			graph[pkg] = strings.Fields(imports)
		}
		// Walk from every shared package without entering permitted gateways.
		type step struct{ pkg, via string }
		var queue []step
		seen := map[string]bool{}
		for pkg := range graph {
			if strings.HasPrefix(pkg, module+"shared/") {
				queue = append(queue, step{pkg, pkg})
				seen[pkg] = true
			}
		}
		for len(queue) > 0 {
			s := queue[0]
			queue = queue[1:]
			importer := strings.TrimPrefix(s.pkg, module)
			for _, imp := range graph[s.pkg] {
				if seen[imp] {
					continue
				}
				if _, gate := gateways[imp]; gate && strings.HasPrefix(s.pkg, module+"shared/") && importAllowed(gateways, importer, imp) {
					continue
				}
				// Go's FIPS crypto core (DRBG, entropy) sits under every allowed
				// crypto package and reaches os/time through crypto/internal/sysrand.
				// Shared code cannot import it; disallowedCalls blocks the APIs
				// that would draw from it.
				if strings.HasPrefix(imp, "crypto/internal/") {
					continue
				}
				seen[imp] = true
				queue = append(queue, step{imp, s.via})
				for _, bad := range sensitive {
					if imp == bad {
						t.Errorf("%v: %s reaches %s via %s, not through a gateway", target, s.via, imp, s.pkg)
					}
				}
				if strings.HasPrefix(imp, "google.golang.org/protobuf") || strings.HasPrefix(imp, module) && !strings.HasPrefix(imp, module+"shared/") {
					t.Errorf("%v: %s reaches %s", target, s.via, imp)
				}
			}
		}
	}
}

func importAllowed(list map[string][]string, dir, imp string) bool {
	if strings.HasPrefix(imp, module) {
		// Only other shared packages: never internal/, gen/ or clientcore.
		return strings.HasPrefix(imp, module+"shared/")
	}
	only, ok := list[imp]
	if !ok {
		return false
	}
	if len(only) == 0 {
		return true
	}
	for _, d := range only {
		if dir == d || strings.HasPrefix(dir, d+"/") {
			return true
		}
	}
	return false
}
