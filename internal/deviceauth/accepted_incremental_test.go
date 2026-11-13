package deviceauth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func checkAcceptedIndex(t *testing.T, e *Endpoint) {
	t.Helper()
	size, volatile, bodies := 0, 0, 0
	for k, a := range e.data.Accepted {
		if a.Persisted {
			size += acceptedEntrySize(k, a)
			if a.Available {
				bodies++
			}
		} else {
			volatile += acceptedEntrySize(k, a)
		}
	}
	if size != e.acceptedBytes || bodies != e.acceptedBodies || volatile != e.volatileBytes || len(e.data.Accepted) != len(e.volatileExpiry)+len(e.durableExpiry) {
		t.Fatal("invalid acceptance indexes")
	}

}
func testKey(s string) string { h := digest("test/key", []byte(s)); return string(h[:]) }

func TestAcceptedRetentionAndIndependentBudgets(t *testing.T) {
	e := newCurrentChain(t).machineEndpoint(t, "")
	defer e.Close()
	k := testKey("durable")
	if err := e.startAccepted(k, Hash{1}, 20, 10, true); err != nil {
		t.Fatal(err)
	}
	if err := e.finishAccepted(k, Final([]byte("saved")), 21); err != nil {
		t.Fatal(err)
	}
	if err := e.startAccepted(testKey("transient"), Hash{}, 40, 20, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.data.Accepted[k]; !ok {
		t.Fatal("transient pruned durable")
	}
	// Exact grace boundary: only a fresh durable admission may prune history.
	if err := e.startAccepted(testKey("before"), Hash{}, 20+acceptedGrace+10, 20+acceptedGrace-1, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.data.Accepted[k]; !ok {
		t.Fatal("history removed early")
	}
	if err := e.startAccepted(testKey("at"), Hash{}, 20+acceptedGrace+10, 20+acceptedGrace, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.data.Accepted[k]; ok {
		t.Fatal("history not pruned at boundary")
	}
	if err := e.startAccepted(k, Hash{}, 20, 10, true); err != ErrExpired {
		t.Fatalf("rollback resurrected request: %v", err)
	}
	checkAcceptedIndex(t, e)
}
func TestAcceptedSummaryCapacityAndBodyLimit(t *testing.T) {
	e := newCurrentChain(t).machineEndpoint(t, "")
	defer e.Close()
	for i := 0; i < maxAccepted; i++ {
		e.data.Accepted[testKey(fmt.Sprint(i))] = accepted{Deadline: 20, RetainUntil: 20 + acceptedGrace, Persisted: true}
	}
	e.indexAccepted()
	if err := e.startAccepted(testKey("extra"), Hash{}, 30, 10, true); err != ErrCapacity {
		t.Fatalf("summary cap: %v", err)
	}
	if err := e.startAccepted(testKey("extra"), Hash{}, acceptedGrace+40, acceptedGrace+20, true); err != nil {
		t.Fatal(err)
	}
	// Body budget is entirely separate from summary admission.
	e.acceptedBytes = maxAcceptedBytes
	if err := e.finishAccepted(testKey("extra"), Final([]byte("large")), acceptedGrace+20); err != nil {
		t.Fatal(err)
	}
	a := e.data.Accepted[testKey("extra")]
	_, err := acceptedResult(a)
	var missing *ResultUnavailable
	if !errors.As(err, &missing) || missing.Category != Executed || a.State != final {
		t.Fatalf("lost category: %+v %v", a, err)
	}
	if err := e.startAccepted(testKey("next"), Hash{}, acceptedGrace+50, acceptedGrace+20, true); err != nil {
		t.Fatalf("body cap blocked summary: %v", err)
	}
}
func TestAcceptedWriteFailures(t *testing.T) {
	for _, failure := range []string{"admission", "body", "summary", "both"} {
		t.Run(failure, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "security.db")
			e := newCurrentChain(t).machineEndpoint(t, path)
			defer func() { e.Close() }()
			k := testKey("request")
			if failure == "admission" {
				before := e.data.AcceptedFloor
				_, err := e.db.Exec(`CREATE TRIGGER fail BEFORE INSERT ON accepted BEGIN SELECT RAISE(ABORT,'test');END`)
				if err != nil {
					t.Fatal(err)
				}
				if e.startAccepted(k, Hash{}, 100, 10, true) == nil {
					t.Fatal("admission succeeded")
				}
				if len(e.data.Accepted) != 0 || e.data.AcceptedFloor != before {
					t.Fatal("failed admission mutated indexes")
				}
				return
			}
			if err := e.startAccepted(k, Hash{1}, 100, 10, true); err != nil {
				t.Fatal(err)
			}
			if failure == "body" || failure == "both" {
				if _, err := e.db.Exec(`CREATE TRIGGER fail_body BEFORE INSERT ON accepted_bodies BEGIN SELECT RAISE(ABORT,'test');END`); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "summary" || failure == "both" {
				if _, err := e.db.Exec(`CREATE TRIGGER fail_summary BEFORE UPDATE ON accepted BEGIN SELECT RAISE(ABORT,'test');END`); err != nil {
					t.Fatal(err)
				}
			}
			err := e.finishAccepted(k, FinalBusinessConflict([]byte("conflict")), 20)
			if failure == "body" {
				if err != nil {
					t.Fatal(err)
				}
				_, err = acceptedResult(e.data.Accepted[k])
				var m *ResultUnavailable
				if !errors.As(err, &m) || m.Category != BusinessConflict {
					t.Fatalf("category: %v", err)
				}
			} else {
				if !errors.Is(err, ErrOutcomeUnknown) || e.data.Accepted[k].State != endedUnknown {
					t.Fatalf("summary failure: %v", err)
				}
			}
			checkAcceptedIndex(t, e)
			e.Close()
			var openErr error
			e, openErr = OpenEndpoint(path)
			if openErr != nil {
				t.Fatal(openErr)
			}
			_, err = acceptedResult(e.data.Accepted[k])
			if failure == "body" {
				var m *ResultUnavailable
				if !errors.As(err, &m) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatal(err)
			}
		})
	}
}
func TestAcceptedChecksumAndBodyDamage(t *testing.T) {
	for _, table := range []string{"accepted", "accepted_bodies"} {
		t.Run(table, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "security.db")
			e := newCurrentChain(t).machineEndpoint(t, path)
			k := testKey("request")
			if err := e.startAccepted(k, Hash{1}, 100, 10, true); err != nil {
				t.Fatal(err)
			}
			if err := e.finishAccepted(k, Final([]byte("saved")), 20); err != nil {
				t.Fatal(err)
			}
			query := `UPDATE accepted SET payload_hash=zeroblob(32)`
			if table == "accepted_bodies" {
				query = `UPDATE accepted_bodies SET result=X'00'`
			}
			if _, err := e.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			e.Close()
			reopened, err := OpenEndpoint(path)
			if table == "accepted" {
				if err == nil {
					reopened.Close()
					t.Fatal("tampered summary loaded")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			_, err = acceptedResult(reopened.data.Accepted[k])
			var m *ResultUnavailable
			if !errors.As(err, &m) || m.Category != Executed {
				t.Fatal("body damage lost category")
			}
		})
	}
}
func TestAcceptedHotPathAllocationsIndependentOfTableSize(t *testing.T) {
	allocations := func(n int) float64 {
		e := &Endpoint{data: record{Accepted: map[string]accepted{}}}
		for i := 0; i < n; i++ {
			e.data.Accepted[testKey(fmt.Sprint(i))] = accepted{Deadline: 100, RetainUntil: 100}
		}
		e.indexAccepted()
		k := testKey("frame")
		return testing.AllocsPerRun(20, func() {
			if err := e.startAccepted(k, Hash{}, 100, 1, false); err != nil {
				t.Fatal(err)
			}
			if err := e.finishAccepted(k, Final(nil), 2); err != nil {
				t.Fatal(err)
			}
			e.accountAcceptedBody(k, e.data.Accepted[k], -1)
			delete(e.data.Accepted, k)
			e.volatileExpiry = e.volatileExpiry[:n]
		})
	}
	small, large := allocations(1), allocations(8191)
	if large > small+2 {
		t.Fatalf("scaled allocations: %g %g", small, large)
	}
}
func TestIdentityConflictUsesOriginalUnpackedPayload(t *testing.T) {
	c := newCurrentChain(t)
	m := c.machineEndpoint(t, "")
	defer m.Close()
	cl := c.clientEndpoint(t)
	defer cl.Close()
	now := uint64(time.Now().UnixMilli())
	h := Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "payload-identity", Deadline: now + 60000}
	plain := commandBody(t)
	w, err := cl.Prepare(h, nil, plain)
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	fn := func(_ Header, p []byte) (Completion, error) {
		called++
		p[0] ^= 1
		return Final([]byte("original")), nil
	}
	if _, err = m.Accept(w, nil, now, fn); err != nil {
		t.Fatal(err)
	}
	w, err = cl.PrepareUncompressed(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := m.Accept(w, nil, now, fn); err != nil || string(out) != "original" {
		t.Fatalf("resealed: %q %v", out, err)
	}
	h.Deadline++
	w, err = cl.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := m.Accept(w, nil, now, fn); !errors.Is(err, ErrIdentityConflict) || out != nil {
		t.Fatalf("deadline conflict leaked result: %q %v", out, err)
	}
	if called != 1 {
		t.Fatal("conflict executed")
	}
}
func TestConcurrentDuplicateWaitsWithoutAcquiring(t *testing.T) {
	c := newCurrentChain(t)
	m := c.machineEndpoint(t, "")
	defer m.Close()
	cl := c.clientEndpoint(t)
	defer cl.Close()
	now := uint64(time.Now().UnixMilli())
	w, err := cl.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "wait-request-0001", Deadline: now + 60000}, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var callbacks, slots atomic.Int32
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	waiting := make(chan struct{}, 16)
	options := AcceptOptions{Acquire: func(context.Context) (func(), error) { slots.Add(1); return func() {}, nil }}
	fn := func(Header, []byte) (Completion, error) {
		callbacks.Add(1)
		close(entered)
		<-release
		return Final([]byte("done")), nil
	}
	first := make(chan error, 1)
	go func() { _, err := m.AcceptWith(context.Background(), w, nil, now, options, fn); first <- err }()
	<-entered
	var wg sync.WaitGroup
	wg.Add(16)
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func() {
			defer wg.Done()
			out, err := m.AcceptWith(&observedWaitContext{Context: context.Background(), entered: waiting}, w, nil, now, options, fn)
			if err == nil && string(out) != "done" {
				err = fmt.Errorf("wrong result")
			}
			results <- err
		}()
	}
	for range 16 {
		select {
		case <-waiting:
		case <-time.After(5 * time.Second):
			t.Fatal("duplicates did not enter wait table")
		}
	}
	m.mu.Lock()
	n := m.waiters
	m.mu.Unlock()
	if n != 16 {
		t.Fatalf("waiting=%d", n)
	}
	select {
	case <-results:
		t.Fatal("duplicate returned before original finished")
	default:
	}
	unblock.Do(func() { close(release) })
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 1 || slots.Load() != 1 {
		t.Fatalf("callbacks=%d slots=%d", callbacks.Load(), slots.Load())
	}
}
func TestWaitFullTimeoutAndEndedUnknown(t *testing.T) {
	c := newCurrentChain(t)
	m := c.machineEndpoint(t, "")
	defer m.Close()
	cl := c.clientEndpoint(t)
	defer cl.Close()
	now := uint64(time.Now().UnixMilli())
	w, err := cl.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "bounded-wait-request", Deadline: now + 60000}, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := m.Accept(w, nil, now, func(Header, []byte) (Completion, error) { close(entered); <-release; return Undetermined(), nil })
		done <- err
	}()
	<-entered
	m.mu.Lock()
	m.waiters = MaxWaiters
	m.mu.Unlock()
	start := time.Now()
	_, err = m.Accept(w, nil, now, nil)
	if !errors.Is(err, ErrOutcomeUnknown) || time.Since(start) > time.Second {
		t.Fatal("full wait table blocked")
	}
	m.mu.Lock()
	m.waiters = 0
	m.waitTimeout = time.Millisecond
	m.mu.Unlock()
	_, err = m.Accept(w, nil, now, nil)
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	close(release)
	<-done
	m.mu.Lock()
	h, _ := ParseHeader(w.Header)
	a := m.data.Accepted[acceptedKey(h)]
	m.mu.Unlock()
	if a.State != endedUnknown {
		t.Fatalf("state=%d", a.State)
	}
	_, err = m.Accept(w, nil, now, nil)
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal(err)
	}
}
func TestCallbackPastRetentionNeverRebuilds(t *testing.T) {
	c := newCurrentChain(t)
	m := c.machineEndpoint(t, "")
	defer m.Close()
	cl := c.clientEndpoint(t)
	defer cl.Close()
	now := uint64(time.Now().UnixMilli())
	h := Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "late-request-0001", Deadline: now + 1000}
	w, err := cl.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := m.Accept(w, nil, now, func(Header, []byte) (Completion, error) { close(entered); <-release; return Final([]byte("late")), nil })
		done <- err
	}()
	<-entered
	if err := m.Touch(h.Deadline + acceptedGrace); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.pruneAccepted(&m.durableExpiry, h.Deadline+acceptedGrace)
	m.waitTimeout = time.Millisecond
	m.mu.Unlock()
	if _, err := m.Accept(w, nil, h.Deadline+acceptedGrace, func(Header, []byte) (Completion, error) { t.Error("executed twice"); return Final(nil), nil }); err != ErrOutcomeUnknown {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != ErrOutcomeUnknown {
		t.Fatal(err)
	}
	if _, err := m.Accept(w, nil, h.Deadline+acceptedGrace, nil); err != ErrExpired {
		t.Fatal(err)
	}
	if len(m.data.Accepted) != 0 || len(m.running) != 0 {
		t.Fatal("late callback resurrected entry")
	}
}
func TestKilledRunningRecovery(t *testing.T) { testKilledAcceptedRecovery(t, false) }
func TestKilledFinalRecovery(t *testing.T)   { testKilledAcceptedRecovery(t, true) }
func testKilledAcceptedRecovery(t *testing.T, completed bool) {
	if path := os.Getenv("RA_KILL_STORAGE_PATH"); path != "" {
		e, err := OpenEndpoint(path)
		if err != nil {
			panic(err)
		}
		if err = e.startAccepted(testKey("killed"), Hash{1}, 100, 10, true); err != nil {
			panic(err)
		}
		if os.Getenv("RA_KILL_COMPLETED") == "1" {
			if err = e.finishAccepted(testKey("killed"), Final([]byte("committed result")), 20); err != nil {
				panic(err)
			}
		}
		fmt.Fprintln(os.Stdout, "ready")
		select {} // parent deliberately kills this process after durable admission.
	}
	path := filepath.Join(t.TempDir(), "security.db")
	e := newCurrentChain(t).machineEndpoint(t, path)
	e.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKilledRunningRecovery$")
	cmd.Env = append(os.Environ(), "RA_KILL_STORAGE_PATH="+path)
	if completed {
		cmd.Env = append(cmd.Env, "RA_KILL_COMPLETED=1")
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan bool, 1)
	go func() { scanner := bufio.NewScanner(pipe); ready <- scanner.Scan() && scanner.Text() == "ready" }()
	select {
	case ok := <-ready:
		if !ok {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatal("child not ready")
		}
	case <-ctx.Done():
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("child readiness timed out")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	e, err = OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	a := e.data.Accepted[testKey("killed")]
	wantState := running
	if completed {
		wantState = final
	}
	if a.State != wantState || len(e.running) != 0 {
		t.Fatalf("restart state=%v", a.State)
	}
	out, err := acceptedResult(a)
	if completed {
		if err != nil || string(out) != "committed result" || a.Category != Executed {
			t.Fatalf("committed final lost: %q %v", out, err)
		}
	} else if err != ErrOutcomeUnknown {
		t.Fatal(err)
	}

}
func TestAcceptedAdmissionFailureLeavesOriginalHistory(t *testing.T) {
	e := newCurrentChain(t).machineEndpoint(t, "")
	defer e.Close()
	old := testKey("old")
	if err := e.startAccepted(old, Hash{}, 20, 10, true); err != nil {
		t.Fatal(err)
	}
	before := e.data.Accepted[old]
	beforeHeap := append(acceptedHeap{}, e.durableExpiry...)
	if _, err := e.db.Exec(`CREATE TRIGGER fail BEFORE INSERT ON accepted BEGIN SELECT RAISE(ABORT,'test');END`); err != nil {
		t.Fatal(err)
	}
	if err := e.startAccepted(testKey("next"), Hash{}, acceptedGrace+40, acceptedGrace+20, true); err == nil {
		t.Fatal("admission succeeded")
	}
	if !reflect.DeepEqual(e.data.Accepted[old], before) || !reflect.DeepEqual(e.durableExpiry, beforeHeap) {
		t.Fatal("failed admission pruned history")
	}
	var count int
	if e.db.QueryRow(`SELECT count(*) FROM accepted`).Scan(&count) != nil || count != 1 {
		t.Fatal("failed transaction pruned disk history")
	}
}
