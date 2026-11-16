package direct

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/Lewin671/remote-agent/internal/transport/peerwire"
	"github.com/pion/webrtc/v4"
	"testing"
	"time"
)

func TestSDKCallbacksKeepTheirOriginalPCOwner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	self, peer := identity(t, 1), identity(t, 2)
	h := sha256.Sum256([]byte("fixture"))
	codec, _ := peerwire.NewCodec("00000000000000000000000000000001", h, h, 1)
	now := uint64(time.Now().UnixMilli())
	connection := make([]byte, 16)
	s := &Session{ctx: ctx, cancel: cancel, events: make(chan sessionEvent, 1), cfg: SessionConfig{Self: self, Peer: peer.Public(), Codec: codec, Connection: connection, Deadline: now + 10000, Pool: NewPool(1), LoopbackOnly: true, Allow: func() bool { return true }}}
	p := &s.pcs[0]
	p.signal = peerwire.NewSignal(codec, self, peer.Public(), connection, 1, now)
	p.generation = 1
	p.currentGeneration.Store(1)
	if !s.makePC(0, now) {
		t.Fatal("initial PC missing")
	}
	old := p.path
	if err := p.signal.NextGeneration(2); err != nil {
		t.Fatal(err)
	}
	s.resetPC(0)
	old.Wait()
	if !s.makePC(0, now) {
		t.Fatal("replacement PC missing")
	}
	current := p.path
	defer func() { current.Close(); current.Wait() }()
	s.events <- sessionEvent{kind: 3}
	old.cfg.Candidate(nil)
	old.cfg.State(webrtc.PeerConnectionStateFailed)
	if ctx.Err() != nil {
		t.Fatal("stale SDK callback canceled replacement on a full queue")
	}
	if len(s.events) != 1 {
		t.Fatal("old callback entered replacement queue")
	}
	<-s.events
	old.cfg.Candidate(nil)
	if len(s.events) != 0 {
		t.Fatal("old SDK callback entered replacement queue")
	}
	for _, generation := range []uint64{1, 2} {
		s.event(sessionEvent{use: 1, generation: generation, path: old, kind: 1}, now)
		s.event(sessionEvent{use: 1, generation: generation, path: old, kind: 2, state: webrtc.PeerConnectionStateConnected}, now)
	}
	if p.ended || p.signal.DTLS {
		t.Fatal("old PC callback changed replacement state")
	}
	current.cfg.Candidate(nil)
	s.event(<-s.events, now)
	if !p.ended {
		t.Fatal("current candidate callback was lost")
	}
}

func TestQueuedDeliveryRechecksMainLegAtDispatch(t *testing.T) {
	for _, status := range []uint32{2, 3} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			now := uint64(time.Now().UnixMilli())
			id := make([]byte, 16)
			raw := peerwire.PB(peerwire.B(4, id))
			boot := peerwire.NewSender(now + 10000)
			boot.Enqueue(raw, now, now+10000, 1)
			s := &Session{boot: boot, delivery: make(chan deliveryResult, 1)}
			for i := range s.pcs {
				s.pcs[i].sender = peerwire.NewSender(now + 10000)
			}
			generation := 1
			s.Delivery(id, status, 1000, func() bool { return generation == 1 })
			generation = 2
			boot.Discovered()
			s.result(<-s.delivery, now)
			for _, pending := range boot.Pending {
				if boot.Paused || pending.NotBefore != 0 {
					t.Fatal("old main-leg receipt changed current sender")
				}
			}
			s.Delivery(id, status, 1000, func() bool { return generation == 2 })
			s.result(<-s.delivery, now)
			for _, pending := range boot.Pending {
				if status == 2 && !boot.Paused || status == 3 && pending.NotBefore == 0 {
					t.Fatal("current main-leg receipt was ignored")
				}
			}
		})
	}
}
