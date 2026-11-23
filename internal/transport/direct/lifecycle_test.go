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
	codec, _ := peerwire.NewCodec("00000000000000000000000000000001", h, h, 1, protocol.PeerVersion)
	s, initial, err := newSession(ctx, SessionConfig{Self: self, Peer: peer.Public().Member(), Codec: codec, Connection: make([]byte, 16), Deadline: uint64(now().UnixMilli()) + 10000, Pool: NewPool(1), LoopbackOnly: true, Allow: func() bool { return true }, Send: send, Now: now}, initiate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.cancel)
	return s, initial
}

// A real restart: the offerer's healthy use-1 PC reports failure while a
// write still holds its pool slot, so the replacement is refused and created
// on a later tick once the old PC has released the slot. Callbacks of the
// old PC never reach the queue and, with the queue full, cannot close the
// replacement; the pump checks the mirrored View after every step.
func TestRestartReplacesThePCAndOldCallbacksStayOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := newPump(t, ctx)
	initiator, responder, a, b := pumpPair(t, p)
	offerer, answerer := initiator, responder
	if string(b.ID) < string(a.ID) {
		offerer, answerer = responder, initiator
	}
	p.run(func() bool { return offerer.bothReady() && answerer.bothReady() })
	old := offerer.ready[1]
	_, hold, err := old.reserveWrite([]byte("in flight"))
	if err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			hold()
		}
	}
	t.Cleanup(release) // before the sessions wait for their PCs
	offerer.s.events <- linkcore.PCStateChanged{ID: offerer.s.current[0].Load(), State: linkcore.PCFailed}
	p.run(func() bool {
		// The restart asked for a replacement and the SDK side refused it:
		// use 1 has no PC while the old one still holds its slot.
		if offerer.s.current[0].Load() == 0 && !isClosed(old) {
			release()
		}
		return offerer.ready[1].cfg.Generation == 2 && answerer.ready[1].cfg.Generation == 2
	})
	if !released || p.elapsed() >= 10000 {
		t.Fatalf("replacement not refused while the slot was held (released=%v) or late (%d ms)", released, p.elapsed())
	}
	current := offerer.ready[1]
	s := offerer.s
	for len(s.events) < cap(s.events) {
		s.events <- linkcore.PeerDiscovered{}
	}
	old.cfg.Candidate(nil)
	old.cfg.State(webrtc.PeerConnectionStateFailed)
	if !pathAlive(current) {
		t.Fatal("stale SDK callback closed the replacement on a full queue")
	}
	for len(s.events) > 0 {
		<-s.events
	}
	old.cfg.Candidate(nil)
	if len(s.events) != 0 {
		t.Fatal("old SDK callback entered the queue")
	}
	current.cfg.Candidate(nil)
	if e, ok := (<-s.events).(linkcore.PCLocalCandidate); !ok || e.ID != s.current[0].Load() || !e.End {
		t.Fatal("current candidate callback was lost")
	}
	exchange(t, ctx, offerer, answerer)
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
