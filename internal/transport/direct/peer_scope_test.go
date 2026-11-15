package direct

import (
	"bytes"
	"context"
	"crypto/sha256"
	"github.com/Lewin671/remote-agent/internal/transport/peerwire"
	"testing"
	"time"
)

func TestPeerScopeRestartValidatesBothTargetsAndKeepsOriginalReceipt(t *testing.T) {
	a, b := identity(t, 1), identity(t, 2)
	if string(a.ID) > string(b.ID) {
		a, b = b, a
	}
	domain, head := sha256.Sum256([]byte("domain")), sha256.Sum256([]byte("head"))
	codec, err := peerwire.NewCodec("00000000000000000000000000000001", domain, head, 1)
	if err != nil {
		t.Fatal(err)
	}
	now := uint64(time.Now().UnixMilli())
	conn := make([]byte, 16)
	var replies [][]byte
	s := &Session{ctx: context.Background(), receipts: map[string][]byte{}, controlSeen: map[string][]byte{}, cfg: SessionConfig{Self: a, Peer: b.Public(), Codec: codec, Connection: conn, Deadline: now + 10000, Pool: NewPool(8), Allow: func() bool { return true }, Policy: func() bool { return false }, Send: func(out Outbound) bool { replies = append(replies, out.Raw); return true }}}
	for i := range s.pcs {
		g := peerwire.NewSignal(codec, a, b.Public(), conn, uint64(i+1), now)
		g.Peer = b.Public()
		g.BootReady = true
		s.pcs[i].signal = g
		s.pcs[i].generation = 1
		s.pcs[i].currentGeneration.Store(1)
	}
	makeFrame := func(seq, generation2 uint64) []byte {
		body := peerwire.PB(peerwire.B(1, peerwire.PB(peerwire.U(1, 1), peerwire.U(2, 1))), peerwire.B(1, peerwire.PB(peerwire.U(1, 2), peerwire.U(2, generation2))))
		raw, err := codec.Seal(b, a.Public(), peerwire.NewContext(b, a.Public(), conn, 0, 1, seq, now, 1, false), 8, body, nil, nil, nil)
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
