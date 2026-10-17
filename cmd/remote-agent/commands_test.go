package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A clean environment still carries the pane's terminal type: a login
// shell expects one, and its terminal is this pane.
func TestEnvListKeepsThePaneTerm(t *testing.T) {
	t.Setenv("TERM", "screen-256color")
	env := envList(map[string]string{"PATH": "/usr/bin"})
	if !slices.Contains(env, "TERM=screen-256color") || !slices.Contains(env, "PATH=/usr/bin") {
		t.Fatalf("env %q", env)
	}
	// A variable the file names itself wins.
	if env := envList(map[string]string{"TERM": "xterm", "PATH": "/bin"}); !slices.Contains(env, "TERM=xterm") {
		t.Fatalf("env %q", env)
	}
}

// loadEnvFile reads the variables and removes the file: what it held
// (possibly credentials) must not linger on disk.
func TestLoadEnvFileReadsAndDeletes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env.json")
	if err := os.WriteFile(path, []byte(`{"A":"1","B":"two"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := loadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if env["A"] != "1" || env["B"] != "two" {
		t.Fatalf("env %v", env)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the environment file is still there: %v", err)
	}
}
