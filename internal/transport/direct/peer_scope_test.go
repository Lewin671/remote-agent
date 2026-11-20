package direct

import (
	"bytes"
	"context"
	"crypto/sha256"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/shared/peerwire"
	"google.golang.org/protobuf/proto"
)

func TestPeerScopeRestartValidatesBothTargetsAndKeepsOriginalReceipt(t *testing.T) {
	a, b := identity(t, 1), identity(t, 2)
	if string(a.ID) > string(b.ID) {
		a, b = b, a
	}
	domain, head := sha256.Sum256([]byte("domain")), sha256.Sum256([]byte("head"))
	codec, err := peerwire.NewCodec("00000000000000000000000000000001", domain, head, 1, protocol.Version)
	if err != nil {
		t.Fatal(err)
	}
	now := uint64(time.Now().UnixMilli())
	conn := make([]byte, 16)
	var replies [][]byte
	s := &Session{ctx: context.Background(), receipts: map[string]cachedBytes{}, controlSeen: map[string]cachedBytes{}, cfg: SessionConfig{Self: a, Peer: b.Public(), Codec: codec, Connection: conn, Deadline: now + 10000, Pool: NewPool(8), Allow: func() bool { return true }, Policy: func() bool { return false }, Send: func(out Outbound) bool { replies = append(replies, out.Raw); return true }}}
	for i := range s.pcs {
		g := mustSignal(a.NewSignal(codec, b.Public(), conn, uint64(i+1), now))
		g.Peer = b.Public()
		g.BootReady = true
		s.pcs[i].signal = g
		s.pcs[i].generation = 1
		s.pcs[i].currentGeneration.Store(1)
	}
	makeFrame := func(seq, generation2 uint64) []byte {
		body := peerwire.PB(peerwire.B(1, peerwire.PB(peerwire.U(1, 1), peerwire.U(2, 1))), peerwire.B(1, peerwire.PB(peerwire.U(1, 2), peerwire.U(2, generation2))))
		raw, err := b.Seal(codec, a.Public(), peerwire.NewContext(b.Public(), a.Public(), conn, 0, 1, seq, now, 1, false), 8, body)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	s.receive(makeFrame(1, 2), now)
	if s.pcs[0].signal.Generation != 1 || s.pcs[1].signal.Generation != 1 || len(replies) != 0 {
		t.Fatal("invalid second target partially restarted the first PC")
	}
	raw := makeFrame(1, 1)
	s.receive(raw, now)
	if s.pcs[0].signal.Generation != 2 || s.pcs[1].signal.Generation != 2 || len(replies) != 1 {
		t.Fatal("peer-scope restart did not advance both PCs")
	}
	s.receive(raw, now+1)
	if len(replies) != 2 || !bytes.Equal(replies[0], replies[1]) {
		t.Fatal("retired generation duplicate lost its original ACK")
	}
	if s.cfg.Deadline != now+10000 {
		t.Fatal("restart refreshed the original deadline")
	}
}

func TestControlSeenAndReceiptsExpirePast2047ControlsWithInjectedClock(t *testing.T) {
	a, b := identity(t, 1), identity(t, 2)
	if string(a.ID) > string(b.ID) {
		a, b = b, a
	}
	domain, head := sha256.Sum256([]byte("domain")), sha256.Sum256([]byte("head"))
	codec, err := peerwire.NewCodec("00000000000000000000000000000001", domain, head, 1, protocol.Version)
	if err != nil {
		t.Fatal(err)
	}
	var nowMs atomic.Uint64
	nowMs.Store(1_700_000_000_000)
	conn := make([]byte, 16)
	var control15Handled int
	var replies [][]byte
	s := &Session{
		ctx:         context.Background(),
		boot:        peerwire.NewSender(nowMs.Load() + 10_000),
		receipts:    map[string]cachedBytes{},
		controlSeen: map[string]cachedBytes{},
		cfg: SessionConfig{
			Self:       a,
			Peer:       b.Public(),
			Codec:      codec,
			Connection: conn,
			Deadline:   nowMs.Load() + 10_000,
			Pool:       NewPool(8),
			Now:        func() time.Time { return time.UnixMilli(int64(nowMs.Load())) },
			Allow:      func() bool { return true },
			Policy:     func() bool { return false },
			Send: func(out Outbound) bool {
				replies = append(replies, out.Raw)
				return true
			},
			Control: func(o peerwire.Opened, _ uint64) (uint64, []byte) {
				if o.Type == 15 {
					control15Handled++
					return 4, peerwire.PB(peerwire.U(4, o.Context.Class), peerwire.U(5, o.Context.Seq), peerwire.B(6, o.Hash), peerwire.U(7, o.Context.Generation), peerwire.U(8, o.Context.Use))
				}
				return 0, nil
			},
		},
	}
	s.established.Store(true)
	for i := range s.pcs {
		g := mustSignal(a.NewSignal(codec, b.Public(), conn, uint64(i+1), nowMs.Load()))
		g.Peer = b.Public()
		g.BootReady = true
		s.pcs[i].signal = g
		s.pcs[i].sender = peerwire.NewSender(nowMs.Load() + 10_000)
		s.pcs[i].generation = 1
		s.pcs[i].currentGeneration.Store(1)
	}
	subBody, err := proto.Marshal(&pb.DirectEventSubscription{SessionIds: []string{"s1"}})
	if err != nil {
		t.Fatal(err)
	}

	seq := uint64(0)
	// Send 5 batches x 500 = 2500 Control 15 frames (> 2047), advancing injected clock
	// past deadline + 300s between batches so expired controlSeen and receipts are pruned.
	for batch := range 5 {
		t0 := nowMs.Load()
		for range 500 {
			seq++
			raw, err := b.Seal(codec, a.Public(), peerwire.NewContext(b.Public(), a.Public(), conn, 0, 1, seq, t0, 2, false), 15, subBody)
			if err != nil {
				t.Fatal(err)
			}
			s.receive(raw, t0)
		}
		if len(s.controlSeen) > 500 || len(s.receipts) > 500 {
			t.Fatalf("batch %d: expected pruned maps <= 500, got controlSeen=%d receipts=%d", batch, len(s.controlSeen), len(s.receipts))
		}
		// Control context deadline is t0 + 10_000, so expiry is t0 + 310_000.
		nowMs.Add(310_001)
		s.flush(nowMs.Load())
		if len(s.controlSeen) != 0 || len(s.receipts) != 0 {
			t.Fatalf("batch %d: expected flush after deadline+300s to prune all entries, got controlSeen=%d receipts=%d", batch, len(s.controlSeen), len(s.receipts))
		}
	}

	// Send packet #2501 (> 2047 lifetime controls) and verify it is accepted and handled.
	tFinal := nowMs.Load()
	seq++
	raw, err := b.Seal(codec, a.Public(), peerwire.NewContext(b.Public(), a.Public(), conn, 0, 1, seq, tFinal, 2, false), 15, subBody)
	if err != nil {
		t.Fatal(err)
	}
	s.receive(raw, tFinal)
	if control15Handled != 2501 || len(replies) != 2501 {
		t.Fatalf("expected 2501 Control 15 packets handled/replied after pruning, got handled=%d replies=%d", control15Handled, len(replies))
	}
}
