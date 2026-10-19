package deviceauth

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCloseDoesNotWaitForAcceptedIOAndRetainsUnknownOutcome(t *testing.T) {
	c := newCurrentChain(t)
	path := filepath.Join(t.TempDir(), "endpoint.db")
	m := c.machineEndpoint(t, path)
	defer m.Close()
	cl := c.clientEndpoint(t)
	defer cl.Close()
	now := uint64(time.Now().UnixMilli())
	w, err := cl.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "close-gate-request", Deadline: now + 60000}, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := m.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { close(entered); <-release; return []byte("done"), nil })
		finished <- err
	}()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			close(release)
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("Close waited for backend I/O")
	}
	if err := m.Install(c.states(), c.proofs()); !errors.Is(err, ErrStorage) {
		close(release)
		t.Fatalf("Install after Close = %v", err)
	}
	close(release)
	if err := <-finished; !errors.Is(err, ErrUnknown) {
		t.Fatalf("late result = %v", err)
	}
	reopened, err := OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { t.Fatal("executed twice"); return nil, nil }); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unfinished accepted request = %v", err)
	}
}

func TestRePairPreservesAcceptanceAndTimeFloor(t *testing.T) {
	c := newCurrentChain(t)
	path := filepath.Join(t.TempDir(), "endpoint.db")
	m := c.machineEndpoint(t, path)
	cl := c.clientEndpoint(t)
	defer cl.Close()
	now := uint64(time.Now().UnixMilli())
	h := Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "repair-request-123", Deadline: now + 60000}
	w, err := cl.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { return []byte("original"), nil }); err != nil {
		t.Fatal(err)
	}
	floor := m.Floor()
	cutoff := m.data.AcceptedFloor
	m.Close()
	x, xpub, err := NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	machine := c.machine
	copy(machine.XPublic[:], xpub)
	phone, _ := c.genesis.Core.Member(c.genesis.Core.AuthorityID)
	next := mustBuildCurrent(t, c.withClient, []Member{phone, machine, c.client}, nil, bytes.Repeat([]byte{0x89}, 32), bytes.Repeat([]byte{0x77}, 32), c.phoneEd, now)
	states := append(c.states(), next)
	proofs := append(c.proofs(), nil)
	lease, err := BuildLease(next, c.phoneEd, now)
	if err != nil {
		t.Fatal(err)
	}
	m, err = InitEndpoint(path, c.pin, states, proofs, machine.ID, c.machineEd.Seed(), x, lease)
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	m, err = OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.Floor() < floor || m.data.AcceptedFloor < cutoff {
		t.Fatalf("time floors lost: %d/%d want %d/%d", m.Floor(), m.data.AcceptedFloor, floor, cutoff)
	}
	if err = cl.Install([]State{next}, []*RecoveryStatement{nil}); err != nil {
		t.Fatal(err)
	}
	if err = cl.InstallLease(lease, now); err != nil {
		t.Fatal(err)
	}
	w, err = cl.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { t.Fatal("re-pair ran the command twice"); return nil, nil })
	if err != nil || string(out) != "original" {
		t.Fatalf("retry %q %v", out, err)
	}
}

func TestRePairRejectsUnverifiedOrNonextendingStore(t *testing.T) {
	for _, mode := range []string{"corrupt", "same-head", "fork"} {
		t.Run(mode, func(t *testing.T) {
			c := newCurrentChain(t)
			path := filepath.Join(t.TempDir(), "endpoint.db")
			m := c.machineEndpoint(t, path)
			m.Close()
			if mode == "corrupt" {
				if err := os.WriteFile(path, []byte("not a verified endpoint"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			states, proofs := c.states(), c.proofs()
			if mode == "fork" {
				phone, _ := c.genesis.Core.Member(c.genesis.Core.AuthorityID)
				fork := mustBuildCurrent(t, c.added, []Member{phone, c.machine, c.client}, nil, bytes.Repeat([]byte{0x88}, 32), bytes.Repeat([]byte{0x55}, 32), c.phoneEd, c.withClient.Core.IssuedAt)
				states = []State{c.genesis, c.added, fork}
			}
			if ep, err := initTestEndpoint(path, c.pin, states, proofs, c.machine.ID, c.machineEd.Seed(), c.machineX); err == nil {
				ep.Close()
				t.Fatal("replaced an unverified or nonextending endpoint")
			}
			if mode == "corrupt" {
				raw, _ := os.ReadFile(path)
				if string(raw) != "not a verified endpoint" {
					t.Fatal("overwrote corrupt trust anchor")
				}
				return
			}
			ep, err := OpenEndpoint(path)
			if err != nil {
				t.Fatal(err)
			}
			defer ep.Close()
			_, head, _ := ep.Snapshot()
			want, _ := c.withClient.Hash()
			got, _ := head.Hash()
			if want != got {
				t.Fatal("replaced original chain")
			}
		})
	}
}

func TestRePairRevokedMachineUsesNewIdentityAndKeepsLocalFloor(t *testing.T) {
	c := newCurrentChain(t)
	path := filepath.Join(t.TempDir(), "endpoint.db")
	m := c.machineEndpoint(t, path)
	now := uint64(time.Now().UnixMilli())
	if err := m.Touch(now); err != nil {
		t.Fatal(err)
	}
	m.Close()
	phone, _ := c.genesis.Core.Member(c.genesis.Core.AuthorityID)
	removed := mustBuildCurrent(t, c.withClient, []Member{phone, c.client}, []string{c.machine.ID}, bytes.Repeat([]byte{0x88}, 32), bytes.Repeat([]byte{0x77}, 32), c.phoneEd, now)
	replacement, ed, x := testMember(t, 0x99, Machine)
	paired, err := BuildPairState(removed, replacement, replacement.XPublic[:], bytes.Repeat([]byte{0x88}, 32), bytes.Repeat([]byte{0xaa}, 32), c.phoneEd, now)
	if err != nil {
		t.Fatal(err)
	}
	ep, err := initTestEndpoint(path, c.pin, append(c.states(), removed, paired), append(c.proofs(), nil, nil), replacement.ID, ed.Seed(), x)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	if ep.Floor() < now {
		t.Fatal("replacement lost verified local time floor")
	}
}

func TestRePairSnapshotCannotOverwriteFinishedAcceptanceOrNewerFloor(t *testing.T) {
	c := newCurrentChain(t)
	path := filepath.Join(t.TempDir(), "endpoint.db")
	m := c.machineEndpoint(t, path)
	cl := c.clientEndpoint(t)
	defer cl.Close()
	now := uint64(time.Now().UnixMilli())
	w, err := cl.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "repair-inflight-123", Deadline: now + 60000}, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := m.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { close(entered); <-release; return []byte("completed"), nil })
		done <- err
	}()
	<-entered
	// Re-pair reads the verified store while the old daemon is still running.
	stale, err := OpenEndpoint(path)
	if err != nil {
		close(release)
		<-done
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := m.Touch(now + 2000); err != nil {
		t.Fatal(err)
	}
	m.Close()
	// Its header/chain transaction must not rewrite a callback's completed row
	// or lower time/pruning floors that advanced after that verified snapshot.
	if err := stale.persistRecord(false); err != nil {
		t.Fatal(err)
	}
	stale.Close()
	reopened, err := OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Floor() < now+2000 {
		t.Error("re-pair snapshot rolled back receive time")
	}
	out, err := reopened.Accept(w, nil, now+2000, func(Header, []byte) ([]byte, error) { t.Fatal("executed twice"); return nil, nil })
	if err != nil || string(out) != "completed" {
		t.Fatalf("re-pair overwrote completed acceptance: %q %v", out, err)
	}
}
