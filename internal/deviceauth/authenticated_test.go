package deviceauth

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestAuthenticatedCapabilityKeepsOriginalBytesAndOwner(t *testing.T) {
	c := newCurrentChain(t)
	e := c.machineEndpoint(t, "")
	defer e.Close()
	other := c.machineEndpoint(t, "")
	defer other.Close()
	client := c.clientEndpoint(t)
	defer client.Close()
	// Repeated known protobuf fields are valid but would be collapsed by
	// decoding and reserializing. The wire authorization binds all these bytes.
	original := bytes.Repeat(commandBody(t), 512)
	now := uint64(time.Now().UnixMilli())
	wire, err := client.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "authenticated-byte-identity", Deadline: now + 60000}, nil, original)
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Authenticate(wire, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	copy := a.Body()
	copy[0] ^= 1
	if !bytes.Equal(a.Body(), original) {
		t.Fatal("caller mutated authenticated bytes")
	}
	calls := 0
	fn := func(h Header, plain []byte) (Completion, error) {
		calls++
		decoded, err := Unpack(h, plain)
		if err != nil || !bytes.Equal(decoded, original) {
			t.Error("original protobuf bytes changed")
		}
		return Final([]byte("done")), nil
	}
	if _, err := other.AcceptAuthenticated(context.Background(), a, now, AcceptOptions{}, fn); !errors.Is(err, ErrInvalid) {
		t.Fatal("capability crossed endpoint ownership")
	}
	if calls != 0 {
		t.Fatal("foreign endpoint executed capability")
	}
	if _, err := e.AcceptAuthenticated(context.Background(), a, now, AcceptOptions{}, fn); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("callbacks=%d", calls)
	}
}
func TestAbandonedCallbackCannotPublishLateFinalResult(t *testing.T) {
	c := newCurrentChain(t)
	e := c.machineEndpoint(t, "")
	defer e.Close()
	client := c.clientEndpoint(t)
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	wire, err := client.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "overdue-mutation-identity", Deadline: now + 60000}, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Authenticate(wire, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	abandon := make(chan func(), 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := e.AcceptAuthenticated(context.Background(), a, now, AcceptOptions{Running: func(fn func()) { abandon <- fn }}, func(Header, []byte) (Completion, error) { <-release; return Final([]byte("late success")), nil })
		done <- err
	}()
	mark := <-abandon
	mark()
	_, err, wait, found := e.Lookup(context.Background(), a, now)
	if !found || wait != nil || !errors.Is(err, ErrOutcomeUnknown) {
		close(release)
		<-done
		t.Fatal("abandoned execution remained waitable or final")
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("late completion=%v", err)
	}
	body, err, wait, found := e.Lookup(context.Background(), a, now)
	if !found || wait != nil || len(body) != 0 || !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal("late callback replaced unknown with a final body")
	}
}
