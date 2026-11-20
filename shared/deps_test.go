// Package shared holds the sans-IO Go code that both the daemon (main module)
// and clientcore compile. This test is its only file at the root.
package shared

import (
	"go/parser"
	"go/token"
	"io/fs"
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
var allowed = map[string][]string{
	"bytes":          nil,
	"errors":         nil,
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
	"time", "os", "net", "io", "fmt", "reflect", "regexp", "context", "sync", "sync/atomic", "unsafe",
	"encoding/hex", "encoding/binary", "encoding/json", "crypto/rand", "crypto/ecdh", "crypto/hpke",
	"math/rand", "math/rand/v2", "log", "log/slog", "runtime",
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
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range f.Imports {
			imp, _ := strconv.Unquote(spec.Path.Value)
			if !importAllowed(dir, imp) {
				t.Errorf("%s imports %q", filepath.ToSlash(filepath.Join("shared", path)), imp)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"shared/peerwire", "shared/linkcore"} {
		if !packages[want] {
			t.Errorf("guard did not see %s", want)
		}
	}
}

func importAllowed(dir, imp string) bool {
	if strings.HasPrefix(imp, module) {
		// Only other shared packages: never internal/, gen/ or clientcore.
		return strings.HasPrefix(imp, module+"shared/")
	}
	only, ok := allowed[imp]
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
