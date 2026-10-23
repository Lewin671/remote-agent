package deviceauth

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFailedFirstInitializationCanRetry(t *testing.T) {
	g, pin, ed, x, _ := testGenesis(t)
	path := filepath.Join(t.TempDir(), "security.db")
	// An invalid lease fails after the schema and placeholder have been committed.
	if ep, err := InitEndpoint(path, pin, []State{g}, []*RecoveryStatement{nil}, g.Core.AuthorityID, ed.Seed(), x, LeaseCertificate{}); err == nil {
		ep.Close()
		t.Fatal("invalid lease accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed initialization published database: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed initialization left private files: %v", entries)
	}
	ep, err := initTestEndpoint(path, pin, []State{g}, []*RecoveryStatement{nil}, g.Core.AuthorityID, ed.Seed(), x)
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	defer ep.Close()
	if _, head, id := ep.Snapshot(); head.Core.Epoch != g.Core.Epoch || id != g.Core.AuthorityID {
		t.Fatal("retry lost identity or chain")
	}
	if ep.LeaseExpiration() == 0 {
		t.Fatal("retry lost lease")
	}
}

func TestInterruptedPrivateInitializationDoesNotBlockRetry(t *testing.T) {
	c := newCurrentChain(t)
	dir := t.TempDir()
	// Model a process stopped after createStore: only its private database exists.
	private, err := os.MkdirTemp(dir, ".security-init-")
	if err != nil {
		t.Fatal(err)
	}
	db, err := openDB(filepath.Join(private, "security.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := createStore(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ep := c.machineEndpoint(t, filepath.Join(dir, "security.db"))
	defer ep.Close()
	secret, err := ep.ContentSecret(c.withClient.Core.ContentEpoch)
	if err != nil || !bytes.Equal(secret, bytes.Repeat([]byte{0x77}, 32)) {
		t.Fatalf("retry lost content secret: %v", err)
	}
}

func TestPublishEndpointNeverReplacesWinner(t *testing.T) {
	c := newCurrentChain(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "security.db")
	winner := c.machineEndpoint(t, path)
	if err := winner.Touch(12345); err != nil {
		t.Fatal(err)
	}
	if err := winner.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	candidate := c.machineEndpoint(t, filepath.Join(dir, "candidate.db"))
	defer candidate.Close()
	if err := publishEndpoint(candidate, path); err == nil {
		t.Fatal("publication replaced existing endpoint")
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("publication replaced the original database")
	}
	reopened, err := OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Floor() != 12345 {
		t.Fatalf("winner lost durable state: floor=%d", reopened.Floor())
	}
}

func TestFirstInitializationPublishesStandalonePrivateDatabase(t *testing.T) {
	c := newCurrentChain(t)
	path := filepath.Join(t.TempDir(), "security.db")
	ep := c.machineEndpoint(t, path)
	if err := ep.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("database permissions = %o", st.Mode().Perm())
	}
	// A copy of just the published DB must contain the complete chain and keys.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(copyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	copied, err := OpenEndpoint(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	_, head, id := copied.Snapshot()
	if head.Core.Epoch != c.withClient.Core.Epoch || id != c.machine.ID || copied.LeaseExpiration() == 0 {
		t.Fatal("published database depends on private WAL")
	}
	for _, epoch := range []uint64{1, 2, 3} {
		if _, err := copied.ContentSecret(epoch); err != nil {
			t.Fatalf("key %d: %v", epoch, err)
		}
	}
}
