package deviceauth

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestPeerBindingIsRecheckedAfterExecutionSlotWait(t *testing.T) {
	chain := newCurrentChain(t)
	endpoint := chain.machineEndpoint(t, "")
	defer endpoint.Close()
	client := chain.clientEndpoint(t)
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	hash, _ := chain.withClient.Hash()
	binding := PeerBinding{chain.machine, chain.client, hash}
	wire, err := client.Prepare(Header{Purpose: Command, Kind: 5, Machine: chain.machine.ID, Request: "direct-original-identity", Deadline: now + 60000}, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	authenticated, err := endpoint.Authenticate(wire, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	waited := false
	calls := 0
	opts := AcceptOptions{PeerBinding: &binding, Acquire: func(context.Context) (func(), error) {
		waited = true
		next, err := rotateForTest(chain.withClient, bytes.Repeat([]byte{0x77}, 32), chain.phoneEd, now)
		if err != nil {
			t.Fatal(err)
		}
		if err = endpoint.Install([]State{next}, []*RecoveryStatement{nil}); err != nil {
			t.Fatal(err)
		}
		lease, err := BuildLease(next, chain.phoneEd, now)
		if err != nil {
			t.Fatal(err)
		}
		if err = endpoint.InstallLease(lease, now); err != nil {
			t.Fatal(err)
		}
		return func() {}, nil
	}}
	_, err = endpoint.AcceptAuthenticated(context.Background(), authenticated, now, opts, func(Header, []byte) (Completion, error) { calls++; return Final([]byte("done")), nil })
	if err == nil || !waited || calls != 0 {
		t.Fatal("retired peer binding entered execution after slot wait", err)
	}
	if _, _, _, found := endpoint.Lookup(context.Background(), authenticated, now); found {
		t.Fatal("direct backpressure created an acceptance record")
	}
	// A direct path refusal is not a durable not-sent or rejected result. The
	// unchanged original authorization can still be admitted over healthy relay.
	if _, err = endpoint.AcceptAuthenticated(context.Background(), authenticated, now, AcceptOptions{}, func(Header, []byte) (Completion, error) { calls++; return Final([]byte("done")), nil }); err != nil || calls != 1 {
		t.Fatal("path refusal changed original request semantics", err, calls)
	}
}
