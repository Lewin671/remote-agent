package relay

import (
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"testing"
	"time"
)

func TestAuthorizationPauseRetiresStreamsButNotEndpoint(t *testing.T) {
	c := &conn{}
	keys := initializeTestSecurityKeys(t, c)
	s := c.security
	ctx, closeStream, err := s.streamContext(context.Background(), keys.states[0].Core.Members[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStream()
	s.setAuthorized(false)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("authentication failure left stream alive")
	}
	if !s.blocked.Load() {
		t.Fatal("control was not paused")
	}
	if _, err := s.accept(context.Background(), deviceauth.Wire{}, nil, s.now(), func(deviceauth.Header, []byte) (deviceauth.Completion, error) {
		t.Fatal("paused callback ran")
		return deviceauth.Final(nil), nil
	}); !errors.Is(err, deviceauth.ErrUntrusted) {
		t.Fatal(err)
	}
	for _, seal := range []func(*pb.Header, deviceauth.Purpose, []byte, []byte) (*pb.Envelope, error){s.seal, s.sealRaw} {
		if _, err := seal(&pb.Header{}, deviceauth.Result, nil, []byte("fixture")); !errors.Is(err, deviceauth.ErrUntrusted) {
			t.Fatalf("paused output path: %v", err)
		}
	}
	if err := s.endpoint.CheckLease(s.now()); err != nil {
		t.Fatalf("pausing destroyed endpoint: %v", err)
	}
	s.setAuthorized(true)
	fresh, closeFresh, err := s.streamContext(context.Background(), keys.states[0].Core.Members[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFresh()
	if fresh.Err() != nil || ctx.Err() == nil {
		t.Fatal("fresh/retired stream lifetimes mixed")
	}
	s.Close()
	select {
	case <-fresh.Done():
	case <-time.After(time.Second):
		t.Fatal("endpoint shutdown left stream alive")
	}
}
