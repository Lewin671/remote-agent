package direct

import (
	"bytes"
	"context"
	"crypto/sha256"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"
)

func localPair(t *testing.T) (*Path, *Path, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	ac, bc := make(chan *webrtc.ICECandidateInit, 65), make(chan *webrtc.ICECandidateInit, 65)
	a, err := New(ctx, Config{Use: 1, Owner: "test", Peer: "peer-b", Generation: 1, Pool: NewPool(8), Allow: func() bool { return true }, LoopbackOnly: true, Candidate: func(c *webrtc.ICECandidateInit) { ac <- c }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); a.Wait() })
	b, err := New(ctx, Config{Use: 1, Owner: "test", Peer: "peer-a", Generation: 1, Pool: NewPool(8), Allow: func() bool { return true }, LoopbackOnly: true, Candidate: func(c *webrtc.ICECandidateInit) { bc <- c }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close(); b.Wait() })
	offer, err := a.Description(true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Remote(offer, true); err != nil {
		t.Fatal(err)
	}
	answer, err := b.Description(false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Remote(answer, false); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		source chan *webrtc.ICECandidateInit
		target *Path
	}{{ac, b}, {bc, a}} {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case c := <-pair.source:
					if c == nil {
						return
					}
					_ = pair.target.AddCandidate(*c)
				}
			}
		}()
	}
	a.Confirm()
	b.Confirm()
	return a, b, ctx
}
func TestRealLocalDataChannelFragmentRoundTrip(t *testing.T) {
	a, b, ctx := localPair(t)
	for _, n := range []int{1, 768 << 10, 1 << 20} {
		raw := bytes.Repeat([]byte{byte(n + 17)}, n)
		done := make(chan error, 1)
		go func() { done <- a.Write(ctx, raw) }()
		got, err := b.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = <-done; err != nil {
			t.Fatal(err)
		}
		if sha256.Sum256(got) != sha256.Sum256(raw) {
			t.Fatal("ciphertext changed")
		}
	}
}
func TestPoolCountsReplacementsAndAllUses(t *testing.T) {
	p := NewPool(8)
	for _, peer := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		if !p.acquire(peer, "test", 1) || !p.acquire(peer, "test", 2) || p.acquire(peer, "test", 1) {
			t.Fatal("per-peer PC cap")
		}
	}
	if p.acquire("i", "test", 1) {
		t.Fatal("peer cap")
	}
	if !p.reserve("a", 0, 2<<20, 128) || p.reserve("a", 0, 1, 1) {
		t.Fatal("peer send cap")
	}
	p.free("a", 0, 2<<20, 128)
	if !p.reserve("a", 1, 2<<20, 128) || p.reserve("a", 1, 1, 0) {
		t.Fatal("peer receive cap")
	}
	p.free("a", 1, 2<<20, 128)
}
func TestUntrustedFragmentsChargeActualBytesAndExpire(t *testing.T) {
	pool := NewPool(8)
	if !pool.acquire("peer", "test", 1) {
		t.Fatal("acquire")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Path{ctx: ctx, cancel: cancel, cfg: Config{Use: 1, Owner: "test", Peer: "peer", Pool: pool, Generation: 1, Allow: func() bool { return true }}, confirmed: true, pending: map[string]*partial{}, received: make(chan message, 128)}
	send := func(f *pb.Fragment) { b, _ := proto.Marshal(f); p.receive(webrtc.DataChannelMessage{Data: b}) }
	f := &pb.Fragment{MessageId: make([]byte, 16), Count: 2, CiphertextLength: 8, CiphertextSha256: make([]byte, 32), PathGeneration: 1, Fragment: []byte("123456")}
	send(f)
	send(f)
	if pool.total[1] != 6 {
		t.Fatal("duplicate double charged")
	}
	f.Index = 1
	send(f)
	if pool.total[1] != 6 {
		t.Fatal("retained beyond declared total")
	}
	p.expire(time.Now().Add(5 * time.Second))
	if pool.total[1] != 0 || len(p.pending) != 0 {
		t.Fatal("expiry did not free actual retained bytes")
	}
	pool.release("peer", 1)
}
func TestAuthorizationLossClosesIdlePC(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)
	pool := NewPool(8)
	p, err := New(context.Background(), Config{Use: 1, Owner: "test", Peer: "peer", Generation: 1, Pool: pool, Allow: allowed.Load, LoopbackOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { p.Close(); p.Wait() }()
	allowed.Store(false)
	select {
	case <-p.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("idle peer survived authorization loss")
	}
	if err := p.Write(context.Background(), []byte{1}); err == nil {
		t.Fatal("unauthorized send")
	}
}
