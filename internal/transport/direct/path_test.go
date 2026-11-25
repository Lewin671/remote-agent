package direct

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"runtime"
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
	if pool.reserved(1) != 6 {
		t.Fatal("duplicate double charged")
	}
	f.Index = 1
	send(f)
	if pool.reserved(1) != 6 {
		t.Fatal("retained beyond declared total")
	}
	p.expire(time.Now().Add(5 * time.Second))
	if pool.reserved(1) != 0 || len(p.pending) != 0 {
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

func TestRealSDKPeerBudgetIncludesClosingReplacements(t *testing.T) {
	pool := NewPool(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var paths []*Path
	defer func() {
		for _, p := range paths {
			p.Close()
		}
		for _, p := range paths {
			p.Wait()
		}
	}()
	for peer := 0; peer < 8; peer++ {
		for use := uint64(1); use <= 2; use++ {
			p, err := New(ctx, Config{Peer: fmt.Sprintf("fixture-%d", peer), Owner: "round", Use: use, Generation: 1, Pool: pool, Allow: func() bool { return true }, LoopbackOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			paths = append(paths, p)
		}
	}
	if _, err := New(ctx, Config{Peer: "fixture-extra", Owner: "round", Use: 1, Generation: 1, Pool: pool, Allow: func() bool { return true }, LoopbackOnly: true}); err == nil {
		t.Fatal("ninth peer allocated an SDK PC")
	}
	if _, err := New(ctx, paths[0].cfg); err == nil {
		t.Fatal("same-use replacement escaped the PC budget")
	}
	paths[0].Close()
	paths[0].Wait()
	next, err := New(ctx, paths[0].cfg)
	if err != nil {
		t.Fatal("closed SDK reservation not released", err)
	}
	paths = append(paths, next)
}

// One reader races Close for the final buffered message. Using one inert SDK
// connection keeps the stress on queue ownership rather than SDK allocation.
func TestReadCloseConcurrentDrain(t *testing.T) {
	previous := runtime.GOMAXPROCS(2)
	defer runtime.GOMAXPROCS(previous)
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	confirmed := make(chan struct{})
	close(confirmed)
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for i := 0; i < 20000; i++ {
		pool := NewPool(1)
		if !pool.acquire("peer", "round", 1) || !pool.reserve("peer", 1, 1, 1) {
			t.Fatal("fixture reservation failed")
		}
		ctx, cancel := context.WithCancel(context.Background())
		p := &Path{pc: pc, ctx: ctx, cancel: cancel, cfg: Config{Peer: "peer", Use: 1, Pool: pool, Allow: func() bool { return true }}, confirmed: true, confirmation: confirmed, pending: map[string]*partial{}, received: make(chan message, 1), closed: make(chan struct{})}
		p.received <- message{[]byte{1}}
		start, done := make(chan struct{}), make(chan struct{}, 2)
		go func() { <-start; _, _ = p.Read(context.Background()); done <- struct{}{} }()
		go func() { <-start; p.Close(); p.Wait(); done <- struct{}{} }()
		close(start)
		for range 2 {
			select {
			case <-done:
			case <-deadline.C:
				t.Fatalf("Read/Close drain deadlocked at iteration %d", i)
			}
		}
		pool.mu.Lock()
		empty := pool.core.Idle()
		pool.mu.Unlock()
		if !empty {
			t.Fatal("close leaked receive or SDK reservations")
		}
	}
}

func TestTryWriteLowSingleFragmentAndRejectsOversizeOrBusy(t *testing.T) {
	a, b, ctx := localPair(t)
	// Wait for DataChannel open via one round-trip Write/Read.
	if err := a.Write(ctx, []byte("warm")); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Read(ctx); err != nil || string(got) != "warm" {
		t.Fatalf("warmup read got %q err=%v", got, err)
	}

	if a.TryWriteLow(make([]byte, MaxSingleFragmentBytes+1), 16<<10) {
		t.Fatal("expected TryWriteLow to reject oversize payload")
	}
	if a.TryWriteLow(nil, 16<<10) {
		t.Fatal("expected TryWriteLow to reject empty payload")
	}

	// Simulate busy sendMu: TryWriteLow must return false immediately without blocking.
	a.sendMu <- struct{}{}
	if a.TryWriteLow([]byte("blocked"), 16<<10) {
		<-a.sendMu
		t.Fatal("expected TryWriteLow to drop when sendMu is held")
	}
	<-a.sendMu

	payload := bytes.Repeat([]byte{0x5a}, 512)
	if !a.TryWriteLow(payload, 16<<10) {
		t.Fatal("expected TryWriteLow to send single-fragment payload")
	}
	got, err := b.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestReceiveDoesNotDoubleChargeBudgetOnReassembly(t *testing.T) {
	newTestPath := func(t *testing.T) (*Path, *Pool) {
		t.Helper()
		pool := NewPool(8)
		if !pool.acquire("peer", "test", 1) {
			t.Fatal("acquire")
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(func() {
			cancel()
			pool.release("peer", 1)
		})
		confirmed := make(chan struct{})
		close(confirmed)
		p := &Path{
			ctx:          ctx,
			cancel:       cancel,
			cfg:          Config{Use: 1, Owner: "test", Peer: "peer", Pool: pool, Generation: 1, Allow: func() bool { return true }},
			confirmed:    true,
			confirmation: confirmed,
			pending:      map[string]*partial{},
			received:     make(chan message, 128),
		}
		return p, pool
	}
	sendChunks := func(p *Path, idByte byte, payload []byte, count uint32) {
		hash := sha256.Sum256(payload)
		msgID := bytes.Repeat([]byte{idByte}, 16)
		chunkSize := (len(payload) + int(count) - 1) / int(count)
		for i := uint32(0); i < count; i++ {
			start := int(i) * chunkSize
			end := min(len(payload), start+chunkSize)
			f := &pb.Fragment{
				MessageId:        msgID,
				Index:            i,
				Count:            count,
				CiphertextLength: uint32(len(payload)),
				CiphertextSha256: hash[:],
				PathGeneration:   1,
				Fragment:         payload[start:end],
			}
			b, err := proto.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			p.receive(webrtc.DataChannelMessage{Data: b})
		}
	}

	t.Run("128-fragment message delivers instead of hitting 129-frame double charge", func(t *testing.T) {
		p, pool := newTestPath(t)
		payload := bytes.Repeat([]byte{0x42}, 128*100)
		sendChunks(p, 1, payload, 128)
		if len(p.received) != 1 {
			t.Fatalf("received len = %d, want 1", len(p.received))
		}
		got, err := p.Read(context.Background())
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("read failed: err=%v len=%d", err, len(got))
		}
		if pool.reserved(1) != 0 {
			t.Fatalf("reserved = %d, want 0", pool.reserved(1))
		}
	})

	t.Run("two pipelined 768KiB messages fit within 2MiB cap", func(t *testing.T) {
		p, pool := newTestPath(t)
		msg1 := bytes.Repeat([]byte{0x11}, 768<<10)
		msg2 := bytes.Repeat([]byte{0x22}, 768<<10)
		sendChunks(p, 2, msg1, 49)
		sendChunks(p, 3, msg2, 49)
		if len(p.received) != 2 {
			t.Fatalf("received len = %d, want 2", len(p.received))
		}
		for idx, want := range [][]byte{msg1, msg2} {
			got, err := p.Read(context.Background())
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("msg %d read failed: err=%v len=%d", idx, err, len(got))
			}
		}
		if pool.reserved(1) != 0 {
			t.Fatalf("reserved = %d, want 0", pool.reserved(1))
		}
	})

	t.Run("1B unread plus 1MiB message fits within 2MiB cap", func(t *testing.T) {
		p, pool := newTestPath(t)
		small := []byte{0xaa}
		big := bytes.Repeat([]byte{0xbb}, 1<<20)
		sendChunks(p, 4, small, 1)
		sendChunks(p, 5, big, 65)
		if len(p.received) != 2 {
			t.Fatalf("received len = %d, want 2", len(p.received))
		}
		for idx, want := range [][]byte{small, big} {
			got, err := p.Read(context.Background())
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("msg %d read failed: err=%v len=%d", idx, err, len(got))
			}
		}
		if pool.reserved(1) != 0 {
			t.Fatalf("reserved = %d, want 0", pool.reserved(1))
		}
	})
}

func TestTransientPeerConnectionDisconnectedDoesNotCloseImmediately(t *testing.T) {
	prevGrace := disconnectGrace
	disconnectGrace = 60 * time.Millisecond
	t.Cleanup(func() { disconnectGrace = prevGrace })

	t.Run("recovers to Connected within grace window", func(t *testing.T) {
		pool := NewPool(8)
		var states []webrtc.PeerConnectionState
		p, err := New(context.Background(), Config{
			Use:          1,
			Owner:        "test",
			Peer:         "peer",
			Generation:   1,
			Pool:         pool,
			Allow:        func() bool { return true },
			LoopbackOnly: true,
			State:        func(s webrtc.PeerConnectionState) { states = append(states, s) },
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { p.Close(); p.Wait() }()

		p.onConnectionState(webrtc.PeerConnectionStateDisconnected)
		select {
		case <-p.Done():
			t.Fatal("transient Disconnected closed path immediately")
		default:
		}
		p.onConnectionState(webrtc.PeerConnectionStateConnected)
		time.Sleep(100 * time.Millisecond)
		select {
		case <-p.Done():
			t.Fatal("path closed after recovering to Connected")
		default:
		}
		for _, s := range states {
			if s == webrtc.PeerConnectionStateDisconnected {
				t.Fatal("transient Disconnected was forwarded before grace elapsed")
			}
		}
	})

	t.Run("closes after grace expires without recovery", func(t *testing.T) {
		pool := NewPool(8)
		var sawDisconnected atomic.Bool
		p, err := New(context.Background(), Config{
			Use:          1,
			Owner:        "test",
			Peer:         "peer",
			Generation:   1,
			Pool:         pool,
			Allow:        func() bool { return true },
			LoopbackOnly: true,
			State: func(s webrtc.PeerConnectionState) {
				if s == webrtc.PeerConnectionStateDisconnected {
					sawDisconnected.Store(true)
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { p.Close(); p.Wait() }()

		p.onConnectionState(webrtc.PeerConnectionStateDisconnected)
		select {
		case <-p.Done():
		case <-time.After(time.Second):
			t.Fatal("persistent Disconnected did not close path after grace")
		}
		if !sawDisconnected.Load() {
			t.Fatal("Disconnected state was not forwarded after grace expiry")
		}
	})
}
