package deviceauth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Done is consulted by waitLocked only after registering the waiter. This
// handshake makes the concurrent-wait tests independent of scheduler timing.
type observedWaitContext struct {
	context.Context
	once    sync.Once
	entered chan<- struct{}
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { c.entered <- struct{}{} })
	return c.Context.Done()
}

func TestCanceledDuplicateReleasesWaiterWithoutCancelingExecution(t *testing.T) {
	c := newCurrentChain(t)
	e := c.machineEndpoint(t, "")
	defer e.Close()
	cl := c.clientEndpoint(t)
	defer cl.Close()
	now := uint64(time.Now().UnixMilli())
	w, err := cl.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "cancel-waiting-request", Deadline: now + 60000}, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	done := make(chan error, 1)
	go func() {
		_, err := e.Accept(w, nil, now, func(Header, []byte) (Completion, error) { close(entered); <-release; return Final([]byte("done")), nil })
		done <- err
	}()
	<-entered
	waiting := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	duplicate := make(chan error, 1)
	go func() {
		_, err := e.AcceptWith(&observedWaitContext{Context: ctx, entered: waiting}, w, nil, now, AcceptOptions{}, nil)
		duplicate <- err
	}()
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("not waiting")
	}
	cancel()
	select {
	case err := <-duplicate:
		if err != ErrOutcomeUnknown {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait goroutine leaked")
	}
	e.mu.Lock()
	waiters, runners := e.waiters, len(e.running)
	e.mu.Unlock()
	if waiters != 0 || runners != 1 {
		t.Fatalf("waiters=%d runners=%d", waiters, runners)
	}
	select {
	case <-done:
		t.Fatal("waiter canceled execution")
	default:
	}
	once.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSlotAcquisitionRechecksIdentityPauseAndExpiry(t *testing.T) {
	for _, change := range []string{"duplicate", "conflict", "pause", "expiry", "canceled"} {
		t.Run(change, func(t *testing.T) {
			c := newCurrentChain(t)
			e := c.machineEndpoint(t, "")
			defer e.Close()
			cl := c.clientEndpoint(t)
			defer cl.Close()
			now := uint64(time.Now().UnixMilli())
			h := Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "slot-recheck-request", Deadline: now + 60000}
			w, err := cl.Prepare(h, nil, commandBody(t))
			if err != nil {
				t.Fatal(err)
			}
			entered, grant := make(chan struct{}), make(chan struct{})
			var unblock sync.Once
			defer unblock.Do(func() { close(grant) })
			var held, calls atomic.Int32
			var paused atomic.Bool
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			options := AcceptOptions{AllowNew: func() bool { return !paused.Load() }, Acquire: func(ctx context.Context) (func(), error) {
				close(entered)
				select {
				case <-grant:
					held.Add(1)
					return func() { held.Add(-1) }, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}}
			done := make(chan error, 1)
			go func() {
				out, err := e.AcceptWith(ctx, w, nil, now, options, func(Header, []byte) (Completion, error) { calls.Add(1); return Final(nil), nil })
				if change == "duplicate" && err == nil && string(out) != "saved" {
					err = errors.New("lost cached result")
				}
				done <- err
			}()
			<-entered
			// These operations require e.mu. They must complete while Acquire blocks.
			switch change {
			case "duplicate", "conflict":
				original := w
				if change == "conflict" {
					h.Deadline++
					original, err = cl.Prepare(h, nil, commandBody(t))
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err = e.Accept(original, nil, now, func(Header, []byte) (Completion, error) { return Final([]byte("saved")), nil }); err != nil {
					t.Fatal(err)
				}
			case "pause":
				paused.Store(true)
			case "expiry":
				if err = e.Touch(h.Deadline); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			}
			unblock.Do(func() { close(grant) })
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("slot acquisition held a lock")
			}
			want := map[string]error{"duplicate": nil, "conflict": ErrIdentityConflict, "pause": ErrUntrusted, "expiry": ErrExpired}[change]
			if change == "canceled" {
				if !errors.Is(err, context.Canceled) && err != ErrOutcomeUnknown {
					t.Fatal(err)
				}
			} else if err != want {
				t.Fatalf("got %v want %v", err, want)
			}
			if calls.Load() != 0 || held.Load() != 0 {
				t.Fatalf("calls=%d held=%d", calls.Load(), held.Load())
			}
		})
	}
}

func TestSlotWaitHonorsSignedDeadline(t *testing.T) {
	c := newCurrentChain(t)
	e := c.machineEndpoint(t, "")
	defer e.Close()
	cl := c.clientEndpoint(t)
	defer cl.Close()
	now := uint64(time.Now().UnixMilli())
	w, err := cl.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "slot-deadline-request", Deadline: now + 100}, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.AcceptWith(context.Background(), w, nil, now, AcceptOptions{Acquire: func(ctx context.Context) (func(), error) { <-ctx.Done(); return nil, ctx.Err() }}, func(Header, []byte) (Completion, error) {
		t.Fatal("expired slot wait executed")
		return Final(nil), nil
	})
	if !errors.Is(err, context.DeadlineExceeded) && err != ErrExpired {
		t.Fatal(err)
	}
	if len(e.data.Accepted) != 0 {
		t.Fatal("slot wait was durably admitted")
	}
}

func TestSlotRaceReleasesAcquiredSlotBeforeDuplicateWait(t *testing.T) {
	c := newCurrentChain(t)
	e := c.machineEndpoint(t, "")
	defer e.Close()
	cl := c.clientEndpoint(t)
	defer cl.Close()
	now := uint64(time.Now().UnixMilli())
	w, err := cl.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "slot-race-request", Deadline: now + 60000}, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	queued, grant, released := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	running, finish := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(finish) })
	var held atomic.Int32
	result := make(chan error, 1)
	go func() {
		_, err := e.AcceptWith(context.Background(), w, nil, now, AcceptOptions{Acquire: func(ctx context.Context) (func(), error) {
			close(queued)
			select {
			case <-grant:
				held.Add(1)
				return func() { held.Add(-1); released <- struct{}{} }, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}}, func(Header, []byte) (Completion, error) { t.Error("second callback started"); return Final(nil), nil })
		result <- err
	}()
	<-queued
	first := make(chan error, 1)
	go func() {
		_, err := e.Accept(w, nil, now, func(Header, []byte) (Completion, error) { close(running); <-finish; return Final(nil), nil })
		first <- err
	}()
	<-running
	close(grant)
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("duplicate retained acquired slot")
	}
	e.mu.Lock()
	n := e.waiters
	e.mu.Unlock()
	if n != 1 || held.Load() != 0 {
		t.Fatalf("waiters=%d held=%d", n, held.Load())
	}
	once.Do(func() { close(finish) })
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
