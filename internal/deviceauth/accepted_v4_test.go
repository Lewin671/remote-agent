package deviceauth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestV4BodyCountBudgetIncludesEmptyResults(t *testing.T) {
	e := newCurrentChain(t).machineEndpoint(t, "")
	defer e.Close()
	for i := 0; i < maxAcceptedBodies; i++ {
		k := testKey(string(rune(i)))
		e.data.Accepted[k] = accepted{State: final, Category: Executed, Available: true, RetainUntil: 1000, Persisted: true}
	}
	e.indexAccepted()
	if e.acceptedBytes != maxAcceptedBodies*bodyOverhead {
		t.Fatal("entry overhead missing")
	}
	k := testKey("overflow")
	if err := e.startAccepted(k, Hash{}, 100, 1, true); err != nil {
		t.Fatal(err)
	}
	if err := e.finishAccepted(k, Final(nil), 2); err != nil {
		t.Fatal(err)
	}
	var missing *ResultUnavailable
	if _, err := acceptedResult(e.data.Accepted[k]); !errors.As(err, &missing) || missing.Category != Executed {
		t.Fatal("body count lost evidence", err)
	}
	checkAcceptedIndex(t, e)
}

func TestV4MissingEmptyBodyIsUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "security.db")
	e := newCurrentChain(t).machineEndpoint(t, path)
	k := testKey("empty")
	if err := e.startAccepted(k, Hash{}, 100, 1, true); err != nil {
		t.Fatal(err)
	}
	if err := e.finishAccepted(k, Final(nil), 2); err != nil {
		t.Fatal(err)
	}
	var stored, empty int
	if err := e.db.QueryRow(`SELECT count(*),count(CASE WHEN typeof(result)='blob' AND length(result)=0 THEN 1 END) FROM accepted_bodies`).Scan(&stored, &empty); err != nil || stored != 1 || empty != 1 {
		t.Fatalf("empty body not actually stored: rows=%d empty=%d err=%v", stored, empty, err)
	}
	e.Close()
	var err error
	e, err = OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := acceptedResult(e.data.Accepted[k]); err != nil || len(out) != 0 || !e.data.Accepted[k].Available {
		t.Fatal("empty body lost on restart", err)
	}
	if _, err := e.db.Exec(`DELETE FROM accepted_bodies`); err != nil {
		t.Fatal(err)
	}
	e.Close()
	e, err = OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	var missing *ResultUnavailable
	if _, err = acceptedResult(e.data.Accepted[k]); !errors.As(err, &missing) {
		t.Fatal("invented empty body", err)
	}
}

func TestV4IdentityConflictAndExpiredMissing(t *testing.T) {
	c := newCurrentChain(t)
	e := c.machineEndpoint(t, "")
	defer e.Close()
	client := c.clientEndpoint(t)
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	h := Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "immutable-request", Deadline: now + 1000}
	w, err := client.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	fn := func(Header, []byte) (Completion, error) { count++; return Final([]byte("secret result")), nil }
	if _, err = e.Accept(w, nil, now, fn); err != nil {
		t.Fatal(err)
	}
	changed, err := client.Prepare(h, nil, []byte("different payload"))
	if err != nil {
		t.Fatal(err)
	}
	if b, err := e.Accept(changed, nil, now, fn); b != nil || err != ErrIdentityConflict {
		t.Fatal("conflicting payload exposed original", err)
	}
	if b, err := e.Accept(w, nil, h.Deadline+acceptedGrace-1, fn); string(b) != "secret result" || err != nil {
		t.Fatal("grace result missing", err)
	}
	h.Request = "expired-missing-id"
	expired, err := client.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Accept(expired, nil, h.Deadline, nil); err != ErrExpired {
		t.Fatal("expired missing confused with invalid", err)
	}
	if _, err = e.Accept(Wire{}, nil, now, nil); err != ErrInvalid {
		t.Fatal("invalid envelope", err)
	}
	if count != 1 {
		t.Fatal("duplicate callback")
	}
}

func TestV4RejectsOlderGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "security-v3.db")
	e := newCurrentChain(t).machineEndpoint(t, path)
	if _, err := e.db.Exec(`UPDATE security SET version=3`); err != nil {
		t.Fatal(err)
	}
	e.Close()
	if got, err := OpenEndpoint(path); err != ErrStorageVersion {
		if got != nil {
			got.Close()
		}
		t.Fatal(err)
	}
}

func TestV4RestartRunningDoesNotWaitOrAcquire(t *testing.T) {
	c := newCurrentChain(t)
	path := filepath.Join(t.TempDir(), "security.db")
	e := c.machineEndpoint(t, path)
	client := c.clientEndpoint(t)
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	w, err := client.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "restart-running-id", Deadline: now + 60000}, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	h, p, err := e.Open(w, nil, false, now)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := payloadHash(h, p)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.startAccepted(acceptedKey(h), hash, h.Deadline, now, true); err != nil {
		t.Fatal(err)
	}
	e.Close()
	e, err = OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	start := time.Now()
	_, err = e.AcceptWith(context.Background(), w, nil, now, AcceptOptions{Acquire: func(context.Context) (func(), error) { t.Error("restart acquired slot"); return func() {}, nil }}, nil)
	if err != ErrOutcomeUnknown || time.Since(start) > time.Second || e.waiters != 0 {
		t.Fatal("restart waited", err)
	}
}

func TestVolatileBodiesCannotConsumePersistentBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "security.db")
	e := newCurrentChain(t).machineEndpoint(t, path)
	defer func() { e.Close() }()
	for i := range maxVolatileAccepted {
		k := testKey(string(rune(i)))
		if err := e.startAccepted(k, Hash{}, 100, 1, false); err != nil {
			t.Fatal(err)
		}
		var body []byte
		if i == 0 {
			body = []byte("terminal result")
		}
		if err := e.finishAccepted(k, Final(body), 2); err != nil {
			t.Fatal(err)
		}
	}
	e.indexAccepted()
	if e.acceptedBytes != 0 || e.acceptedBodies != 0 || e.volatileBytes != maxVolatileAccepted*bodyOverhead+len("terminal result") {
		t.Fatal("volatile bodies charged to durable budget")
	}
	k := testKey("durable-after-terminal-flood")
	if err := e.startAccepted(k, Hash{}, 200, 2, true); err != nil {
		t.Fatal(err)
	}
	if err := e.finishAccepted(k, Final([]byte("saved")), 3); err != nil {
		t.Fatal(err)
	}
	if out, err := acceptedResult(e.data.Accepted[k]); err != nil || string(out) != "saved" {
		t.Fatal("terminal flood lost command result", err)
	}
	e.pruneAccepted(&e.volatileExpiry, 100)
	checkAcceptedIndex(t, e)
	if e.volatileBytes != 0 || e.acceptedBodies != 1 {
		t.Fatal("pruning crossed budgets")
	}
	e.Close()
	var err error
	e, err = OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	checkAcceptedIndex(t, e)
	if e.volatileBytes != 0 || e.acceptedBodies != 1 {
		t.Fatal("restart budget incorrect")
	}
	if out, err := acceptedResult(e.data.Accepted[k]); err != nil || string(out) != "saved" {
		t.Fatal("lost persistent body", err)
	}
	// Exhausting durable bodies cannot steal the separate volatile allowance.
	e.acceptedBytes = maxAcceptedBytes
	e.acceptedBodies = maxAcceptedBodies
	v := testKey("volatile-after-durable-full")
	if err := e.startAccepted(v, Hash{}, 300, 101, false); err != nil {
		t.Fatal(err)
	}
	if err := e.finishAccepted(v, Final(nil), 102); err != nil {
		t.Fatal(err)
	}
	if _, err := acceptedResult(e.data.Accepted[v]); err != nil {
		t.Fatal("durable budget blocked terminal", err)
	}
}
