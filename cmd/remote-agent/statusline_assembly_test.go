package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/paths"
)

func captureStatusOutput(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = original }()
	code := fn()
	_ = w.Close()
	b, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return code, string(b)
}
func TestAgyStatusLinePreservesUserCommandAndExitCode(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	dir := paths.SessionDir("test-session")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"type": "command", "command": "cat; exit 7"})
	if err := os.WriteFile(filepath.Join(dir, agent.StatusLineFile), b, 0600); err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"model":{"display_name":"Native Model"},"quota":{"example":{}}}`)
	code, out := captureStatusOutput(t, func() int { return runUserStatusLineFor("test-session", input, "agy") })
	if code != 7 || out != string(input) {
		t.Fatalf("status line %d %q", code, out)
	}
}
func TestAgyStatusLineWithoutCustomCommandKeepsNativeFooter(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	dir := paths.SessionDir("test-session")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, agent.StatusLineFile), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	code, out := captureStatusOutput(t, func() int {
		return runUserStatusLineFor("test-session", []byte(`{"model":{"display_name":"Native Model"},"terminal_width":40}`), "agy")
	})
	if code != 0 || !strings.HasPrefix(out, "? for shortcuts") || !strings.HasSuffix(out, "Native Model\n") {
		t.Fatalf("native footer %d %q", code, out)
	}
}
