package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVersionProbeDoesNotOwnCacheLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic-cli")
	if err := os.WriteFile(path, []byte("synthetic"), 0700); err != nil {
		t.Fatal(err)
	}
	c := &BinVersion{}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() { c.Get(path, func() string { close(entered); <-release; return "test" }); close(done) }()
	<-entered
	if !c.mu.TryLock() {
		t.Error("slow filesystem/version probe owns cache mutex")
	} else {
		c.mu.Unlock()
	}
	close(release)
	<-done
}
