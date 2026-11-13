package relay_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
)

// Real local server + encrypted WS + production admission and execution pools.
func TestFullExecutionSlotsQueueNinthCommandAndCoalesceDuplicate(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "sess1")
	entered := make(chan struct{}, 9)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	e.backend.sendResult = func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e.startRelay()
	s := e.session(t, "sess1", 0)
	e.online(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	envs := make([]*pb.Envelope, 9)
	for i := range envs {
		var err error
		envs[i], err = e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: s.ID, Text: "test", RequestId: fmt.Sprintf("slot-message-%d", i)}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, 10)
	forward := func(i int) { _, err := e.client.Forward(ctx, s.MachineID, envs[i]); results <- err }
	for i := range 8 {
		go forward(i)
	}
	for range 8 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("execution slots did not fill")
		}
	}
	go forward(8)
	go forward(0)
	select {
	case err := <-results:
		t.Fatalf("full slots returned before release: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if n := e.backend.sendCount(); n != 8 {
		t.Fatalf("executed while full: %d", n)
	}
	once.Do(func() { close(release) })
	for range 10 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("queued request never completed")
		}
	}
	if n := e.backend.sendCount(); n != 9 {
		t.Fatalf("wanted nine unique callbacks, got %d", n)
	}
}
