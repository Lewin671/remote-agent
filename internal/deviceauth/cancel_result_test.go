package deviceauth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestInterruptedCallbackLeavesStartedRecord(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			c := newCurrentChain(t)
			path := filepath.Join(t.TempDir(), "endpoint.db")
			m := c.machineEndpoint(t, path)
			cl := c.clientEndpoint(t)
			defer cl.Close()
			now := uint64(time.Now().UnixMilli())
			w, err := cl.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "interrupted-request", Deadline: now + 60000}, nil, commandBody(t))
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			_, _ = m.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { calls++; return nil, cause })
			for _, a := range m.data.Accepted {
				if a.Done {
					t.Error("interrupted callback persisted as final")
				}
			}
			m.Close()
			m, err = OpenEndpoint(path)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			_, err = m.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { calls++; return nil, nil })
			if !errors.Is(err, ErrOutcomeUnknown) {
				t.Errorf("retry must stay unknown, got %v", err)
			}
			if calls != 1 {
				t.Fatalf("executed %d times", calls)
			}
		})
	}
}

func TestInFlightDuplicateIsNotUnknownState(t *testing.T) {
	c := newCurrentChain(t)
	m := c.machineEndpoint(t, "")
	defer m.Close()
	cl := c.clientEndpoint(t)
	defer cl.Close()
	now := uint64(time.Now().UnixMilli())
	w, err := cl.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "concurrent-request", Deadline: now + 60000}, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := m.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { close(entered); <-release; return []byte("done"), nil })
		done <- err
	}()
	<-entered
	_, err = m.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { t.Error("duplicate executed"); return nil, nil })
	close(release)
	if firstErr := <-done; firstErr != nil {
		t.Fatal(firstErr)
	}
	if err == nil || errors.Is(err, ErrUnknown) {
		t.Fatalf("in-flight duplicate confused with unknown epoch: %v", err)
	}
	out, err := m.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { t.Error("completed duplicate executed"); return nil, nil })
	if err != nil || string(out) != "done" {
		t.Fatalf("completed retry: %q %v", out, err)
	}
}
