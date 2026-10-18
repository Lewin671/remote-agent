package relay

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

func TestIdleExecutorSynchronizesAndAcknowledgesRevocation(t *testing.T) {
	c := &conn{}
	initializeTestSecurity(t, c)
	a := c.security.authority
	pub, ed, _ := ed25519.GenerateKey(nil)
	_, xp, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	member, err := deviceauth.NewMember(pub, xp, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := a.BeginPair("idle-pair-context", c.acct.DeviceID, member)
	if err != nil {
		t.Fatal(err)
	}
	approve, err := deviceauth.SignPair(pair, c.acct.DeviceKey(), false)
	if err != nil {
		t.Fatal(err)
	}
	accept, err := deviceauth.SignPair(pair, ed, true)
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.ApprovePair(pair.ID, approve, accept)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := state.Hash()
	pin, _ := a.Snapshot()
	request := deviceauth.DeleteRequest{Account: pin.Account, Domain: pin.Domain, Requester: c.acct.DeviceID, Operation: "idle-delete-operation", Expected: hash, Nonce: deviceauth.Hash{1}, ExactIDs: true, Targets: []deviceauth.Target{{ID: member.ID}}}
	if err = request.Sign(c.acct.DeviceKey()); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Delete(request); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Completion(request.Operation); !errors.Is(err, deviceauth.ErrPending) {
		t.Fatalf("completed before idle sync: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.securitySyncLoop(ctx) }()
	defer func() { cancel(); <-done }()
	until := time.NewTimer(5 * time.Second)
	defer until.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-until.C:
			t.Fatal("idle executor never fenced")
		case <-poll.C:
			completion, err := a.Completion(request.Operation)
			if err == nil {
				_, installed, _ := c.security.remote.Endpoint.Snapshot()
				if installed.Core.Epoch != completion.Group.Head.Core.Epoch {
					t.Fatal("ACK before installing head")
				}
				return
			}
			if !errors.Is(err, deviceauth.ErrPending) {
				t.Fatal(err)
			}
		}
	}
}
