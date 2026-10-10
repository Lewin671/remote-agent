package relay

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type dirBackend struct {
	Backend
	dir string
}

func (b dirBackend) SessionDir(id string) string { return filepath.Join(b.dir, id) }

// The sync of a connection that just dropped may still be creating a
// session's key when the next connection asks for it. Whichever key one of
// them uploads, the file must hold the same one.
func TestSessionKeyIsCreatedOnceAcrossConnections(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	b := dirBackend{dir: t.TempDir()}
	r := New(b)
	for range 50 {
		id := "s1"
		dir := b.SessionDir(id)
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		const conns = 8
		keys := make([][]byte, conns)
		var wg sync.WaitGroup
		for i := range conns {
			wg.Add(1)
			go func() {
				defer wg.Done()
				key, err := (&conn{r: r}).sessionKey(id, false)
				if err != nil {
					t.Error(err)
				}
				keys[i] = key
			}()
		}
		wg.Wait()
		stored, err := os.ReadFile(filepath.Join(dir, "relay.key"))
		if err != nil {
			t.Fatal(err)
		}
		for i, key := range keys {
			if !bytes.Equal(key, stored) {
				t.Fatalf("connection %d would upload a key other than the one on disk", i)
			}
		}
	}
}
