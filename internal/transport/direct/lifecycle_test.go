package direct

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
	"github.com/pion/webrtc/v4"
)

func testSession(t *testing.T, ctx context.Context, now func() time.Time, send func(Outbound) bool, initiate bool) (*Session, []linkcore.Effect) {
	t.Helper()
	self, peer := identity(t, 1), identity(t, 2)
	h := sha256.Sum256([]byte("fixture"))
	codec, _ := peerwire.NewCodec("00000000000000000000000000000001", h, h, 1, protocol.Version)
	s, initial, err := newSession(ctx, SessionConfig{Self: self, Peer: peer.Public().Member(), Codec: codec, Connection: make([]byte, 16), Deadline: uint64(now().UnixMilli()) + 10000, Pool: NewPool(1), LoopbackOnly: true, Allow: func() bool { return true }, Send: send, Now: now}, initiate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.cancel)
	return s, initial
}

// SDK callbacks of a replaced PC never reach the queue, and cannot close the
// replacement when the queue is full.
func TestSDKCallbacksKeepTheirOriginalPCOwner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now()
	s, _ := testSession(t, ctx, func() time.Time { return now }, func(Outbound) bool { return true }, false)
	s.events = make(chan linkcore.Event, 1)
	s.current[0].Store(1)
	s.generation[0].Store(1)
	if !s.createPC(linkcore.CreatePC{ID: 1, Use: 1, Generation: 1}) {
		t.Fatal("initial PC missing")
	}
	old := s.paths[1]
	// A restart moves use 1 to generation 2 and closes the old PC.
	s.current[0].Store(2)
	s.generation[0].Store(2)
	old.closeWhy("generation_reset")
	old.Wait()
	if !s.createPC(linkcore.CreatePC{ID: 2, Use: 1, Generation: 2}) {
		t.Fatal("replacement PC missing")
	}
	current := s.paths[2]
	defer func() { current.Close(); current.Wait() }()
	s.events <- linkcore.PeerDiscovered{}
	old.cfg.Candidate(nil)
	old.cfg.State(webrtc.PeerConnectionStateFailed)
	if ctx.Err() != nil || !pathAlive(current) {
		t.Fatal("stale SDK callback closed the replacement on a full queue")
	}
	if len(s.events) != 1 {
		t.Fatal("old callback entered replacement queue")
	}
	<-s.events
	old.cfg.Candidate(nil)
	if len(s.events) != 0 {
		t.Fatal("old SDK callback entered replacement queue")
	}
	current.cfg.Candidate(nil)
	if e, ok := (<-s.events).(linkcore.PCLocalCandidate); !ok || e.ID != 2 || !e.End {
		t.Fatal("current candidate callback was lost")
	}
}

// A delivery result is checked against the main connection when it is
// applied, not when it was queued.
func TestQueuedDeliveryRechecksMainLegAtDispatch(t *testing.T) {
	for _, status := range []uint32{2, 3} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			clock := time.Now().UnixMilli()
			var sent [][]byte
			s, initial := testSession(t, context.Background(), func() time.Time { return time.UnixMilli(clock) }, func(o Outbound) bool { sent = append(sent, o.Raw); return true }, true)
			s.apply(initial)
			if len(sent) != 1 {
				t.Fatal("the Probe was not sent")
			}
			fields, _ := peerwire.Parse(sent[0], 5)
			generation := 1
			s.Delivery(fields[4].B, status, 5000, func() bool { return generation == 1 })
			generation = 2
			s.deliver(<-s.delivery)
			clock += 600
			s.tick()
			if len(sent) != 2 {
				t.Fatal("old main-leg receipt changed current sender")
			}
			s.Delivery(fields[4].B, status, 5000, func() bool { return generation == 2 })
			s.deliver(<-s.delivery)
			clock += 1000
			s.tick()
			if len(sent) != 2 {
				t.Fatal("current main-leg receipt was ignored")
			}
		})
	}
}
