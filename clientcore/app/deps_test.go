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
