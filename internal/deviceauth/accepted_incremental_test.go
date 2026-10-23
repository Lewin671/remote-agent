package deviceauth

import (
	"bytes"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func checkAcceptedIndex(t *testing.T, e *Endpoint) {
	t.Helper()
	size := 0
	for k, a := range e.data.Accepted {
		size += acceptedEntrySize(k, a)
	}
	if size != e.acceptedBytes || len(e.data.Accepted) != len(e.volatileExpiry)+len(e.durableExpiry) {
		t.Fatalf("index: bytes=%d want=%d entries=%d queues=%d", e.acceptedBytes, size, len(e.data.Accepted), len(e.volatileExpiry)+len(e.durableExpiry))
	}
}

func TestAcceptedIncrementalExpiryAndDuplicates(t *testing.T) {
	e := newCurrentChain(t).machineEndpoint(t, "")
	defer e.Close()
	for _, tc := range []struct {
		key     string
		durable bool
	}{{"durable", true}, {"volatile", false}} {
		if _, _, err := e.startAccepted(tc.key, 20, 10, tc.durable); err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.startAccepted(tc.key, 20, 10, tc.durable); err != ErrUnknown {
			t.Fatalf("in progress: %v", err)
		}
		if err := e.finishAccepted(tc.key, []byte(tc.key), false, tc.durable); err != nil {
			t.Fatal(err)
		}
	}
	// Expired retries still find their results before any pruning, and return copies.
	out, fresh, err := e.startAccepted("durable", 20, 30, true)
	if err != nil || fresh || string(out) != "durable" {
		t.Fatalf("retry: %q %v %v", out, fresh, err)
	}
	out[0] = 'x'
	if string(e.data.Accepted["durable"].Result) != "durable" {
		t.Fatal("aliased result")
	}
	if _, _, err := e.startAccepted("new-volatile", 40, 20, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.data.Accepted["volatile"]; ok {
		t.Fatal("expired transient retained")
	}
	if _, ok := e.data.Accepted["durable"]; !ok {
		t.Fatal("transient pruned durable history")
	}
	if _, _, err := e.startAccepted("new-durable", 50, 25, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.data.Accepted["durable"]; ok {
		t.Fatal("expired durable retained")
	}
	if _, _, err := e.startAccepted("old", 25, 10, false); err != ErrExpired {
		t.Fatalf("floor rollback: %v", err)
	}
	checkAcceptedIndex(t, e)
}

func TestAcceptedCapacityEdges(t *testing.T) {
	e := newCurrentChain(t).machineEndpoint(t, "")
	defer e.Close()
	for i := 0; i < maxAccepted; i++ {
		e.data.Accepted[fmt.Sprint(i)] = accepted{Deadline: 20}
	}
	e.indexAccepted()
	if _, _, err := e.startAccepted("extra", 30, 10, false); err != ErrCapacity {
		t.Fatalf("entry cap: %v", err)
	}
	if _, _, err := e.startAccepted("extra", 30, 20, false); err != nil {
		t.Fatalf("expiry at cap: %v", err)
	}
	// Fill to exactly the byte cap. A completion that cannot be retained must
	// leave an unknown entry, never allow the operation to execute again.
	result := bytes.Repeat([]byte{'x'}, maxAcceptedBytes-e.acceptedBytes)
	if err := e.finishAccepted("extra", result, false, false); err != nil {
		t.Fatal(err)
	}
	if e.acceptedBytes != maxAcceptedBytes {
		t.Fatalf("bytes: %d", e.acceptedBytes)
	}
	if _, _, err := e.startAccepted("over", 30, 20, false); err != ErrCapacity {
		t.Fatalf("byte cap: %v", err)
	}
	if _, _, err := e.startAccepted("replacement", 40, 30, false); err != nil {
		t.Fatal(err)
	}
	if err := e.finishAccepted("replacement", result, false, false); err != ErrCapacity {
		t.Fatalf("result cap: %v", err)
	}
	if _, _, err := e.startAccepted("replacement", 40, 30, false); err != ErrUnknown {
		t.Fatalf("lost unknown outcome: %v", err)
	}
	checkAcceptedIndex(t, e)
}

func TestAcceptedPersistenceFailureRollsBackDeltas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "endpoint.db")
	e := newCurrentChain(t).machineEndpoint(t, path)
	defer func() { e.Close() }()
	if _, _, err := e.startAccepted("expired", 20, 10, true); err != nil {
		t.Fatal(err)
	}
	if err := e.finishAccepted("expired", []byte("saved"), false, true); err != nil {
		t.Fatal(err)
	}
	before := e.data.Accepted["expired"]
	beforeBytes := e.acceptedBytes
	beforeHeap := append(acceptedHeap(nil), e.durableExpiry...)
	if _, err := e.db.Exec(`CREATE TRIGGER fail_accept BEFORE INSERT ON accepted BEGIN SELECT RAISE(ABORT, 'test'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.startAccepted("next", 40, 20, true); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	if len(e.data.Accepted) != 1 || !reflect.DeepEqual(e.data.Accepted["expired"], before) || e.acceptedBytes != beforeBytes || e.data.AcceptedFloor != 10 || !reflect.DeepEqual(e.durableExpiry, beforeHeap) {
		t.Fatal("failed admission mutated live state")
	}
	var count int
	if err := e.db.QueryRow(`SELECT count(*) FROM accepted WHERE key=?`, []byte("expired")).Scan(&count); err != nil || count != 1 {
		t.Fatalf("prune was not rolled back: %d %v", count, err)
	}
	if _, err := e.db.Exec(`DROP TRIGGER fail_accept`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.startAccepted("next", 40, 20, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`CREATE TRIGGER fail_finish BEFORE UPDATE ON accepted BEGIN SELECT RAISE(ABORT, 'test'); END`); err != nil {
		t.Fatal(err)
	}
	if err := e.finishAccepted("next", []byte("result"), true, true); err == nil {
		t.Fatal("finish unexpectedly succeeded")
	}
	if e.data.Accepted["next"].Done {
		t.Fatal("failed finish committed in memory")
	}
	checkAcceptedIndex(t, e)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	e, err = OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	checkAcceptedIndex(t, e)
	if _, _, err := e.startAccepted("next", 40, 30, true); err != ErrUnknown {
		t.Fatalf("restart lost in-progress entry: %v", err)
	}
	if _, _, err := e.startAccepted("expired", 20, 10, false); err != ErrExpired {
		t.Fatalf("restart lost pruning floor: %v", err)
	}
	if _, err := e.db.Exec(`DROP TRIGGER fail_finish`); err != nil {
		t.Fatal(err)
	}
	if err := e.finishAccepted("next", []byte("result"), true, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.startAccepted("next", 40, 30, true); err != ErrBackend {
		t.Fatalf("failed outcome: %v", err)
	}
	checkAcceptedIndex(t, e)
}

func TestAcceptedHotPathAllocationsIndependentOfTableSize(t *testing.T) {
	allocations := func(n int) float64 {
		e := &Endpoint{data: record{Accepted: make(map[string]accepted, n+1)}}
		for i := 0; i < n; i++ {
			e.data.Accepted[fmt.Sprint(i)] = accepted{Deadline: 100}
		}
		e.indexAccepted()
		size := e.acceptedBytes
		return testing.AllocsPerRun(20, func() {
			if _, _, err := e.startAccepted("frame", 100, 1, false); err != nil {
				t.Fatal(err)
			}
			if err := e.finishAccepted("frame", nil, false, false); err != nil {
				t.Fatal(err)
			}
			// Equal deadlines keep this frame at the end of the heap.
			delete(e.data.Accepted, "frame")
			e.volatileExpiry = e.volatileExpiry[:n]
			e.acceptedBytes = size
		})
	}
	small, large := allocations(1), allocations(maxAccepted-1)
	t.Logf("allocations per admission+finish: small=%.0f full=%.0f", small, large)
	if large > small+2 {
		t.Fatalf("hot-path allocations scale with table size: small=%g full=%g", small, large)
	}
}
