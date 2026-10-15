package updater

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in: validates the generated LaunchAgent with a disposable label
// and a harmless recording executable, never the user's real daemon or timer.
func TestLaunchAgentLoads(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("RA_TEST_LAUNCHD") != "1" {
		t.Skip("set RA_TEST_LAUNCHD=1 in a macOS GUI session")
	}
	dir := t.TempDir()
	f := setup(t)
	f.u.exe = filepath.Join(dir, "record")
	f.u.state = filepath.Join(dir, "state")
	if err := os.MkdirAll(f.u.state, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "triggered")
	if err := os.WriteFile(f.u.exe+".updater", []byte("#!/bin/sh\nprintf done > '"+marker+"'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	files, err := f.u.autoFiles("darwin", dir, "04:00")
	if err != nil {
		t.Fatal(err)
	}
	label := fmt.Sprintf("%s.integration.%d", updateLabel, os.Getpid())
	for _, body := range files {
		path := filepath.Join(dir, "test.plist")
		body = strings.ReplaceAll(body, updateLabel, label)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := command(context.Background(), "/usr/bin/plutil", "-lint", path); err != nil {
			t.Fatal(err)
		}
		if _, err := command(context.Background(), "/bin/launchctl", "bootstrap", domain(), path); err != nil {
			t.Fatal(err)
		}
		defer command(context.Background(), "/bin/launchctl", "bootout", domain()+"/"+label)
		// RunAtLoad may be deferred as a speculative spawn by macOS. Demand an invocation for this bounded test.
		if _, err := command(context.Background(), "/bin/launchctl", "kickstart", domain()+"/"+label); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(marker); err == nil && string(b) == "done" {
			return
		}
		if time.Now().After(deadline) {
			state, _ := command(context.Background(), "/bin/launchctl", "print", domain()+"/"+label)
			log, _ := os.ReadFile(filepath.Join(f.u.state, "update.log"))
			t.Fatalf("LaunchAgent did not invoke updater executable: %s log=%s", state, log)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
