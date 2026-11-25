package linkcore

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"testing"

	"github.com/Lewin671/remote-agent/shared/peerwire"
)

// The session rules run here against a fake executor and a fake clock: two
// sessions exchange frames through a lossy in-memory relay, and fake PCs
// "connect" once each side holds the other's description. No test waits on
// wall time; one step is 25 ms of fake time.

type testKey struct {
	pub  peerwire.Public
	sign ed25519.PrivateKey
	box  *ecdh.PrivateKey
}

func newTestKey(t *testing.T, role uint64) *testKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	box, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(pub)
	instance, boot := make([]byte, 16), make([]byte, 32)
	_, _ = rand.Read(instance)
	_, _ = rand.Read(boot)
	return &testKey{pub: peerwire.Public{ID: []byte(hex.EncodeToString(h[:16])), Role: role, Instance: instance, Boot: boot, Sign: pub, Box: box.PublicKey().Bytes()}, sign: priv, box: box}
}

// orderedKeys returns the offerer (smaller ID) first.
func orderedKeys(t *testing.T) (*testKey, *testKey) {
	a, b := newTestKey(t, 1), newTestKey(t, 2)
	if string(a.pub.ID) > string(b.pub.ID) {
		a, b = b, a
	}
	return a, b
}

func (k *testKey) shared(peer peerwire.Public) []byte {
	pk, err := ecdh.X25519().NewPublicKey(peer.Box)
	if err != nil {
		panic(err)
	}
	s, err := k.box.ECDH(pk)
	if err != nil {
		panic(err)
	}
	return s
}

func (k *testKey) seal(t *testing.T, codec *peerwire.Codec, to peerwire.Public, c peerwire.Context, typ uint64, body []byte) []byte {
	t.Helper()
	entropy := make([]byte, peerwire.SealEntropy)
	_, _ = rand.Read(entropy)
	u, err := codec.Prepare(k.pub, to, k.shared(to), c, typ, body, entropy)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := u.Finish(ed25519.Sign(k.sign, u.Message))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testCodec(t *testing.T) *peerwire.Codec {
	domain, head := sha256.Sum256([]byte("domain")), sha256.Sum256([]byte("head"))
	codec, err := peerwire.NewCodec("00000000000000000000000000000001", domain, head, 1, 17)
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func fakeSDP(end int, id uint64) []byte {
	fp := make([]byte, 0, 95)
	for i := 0; i < 32; i++ {
		if i > 0 {
			fp = append(fp, ':')
		}
		fp = append(fp, fmt.Sprintf("%02X", byte(end*64+int(id)+i))...)
	}
	return []byte("v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\nc=IN IP4 0.0.0.0\r\n" +
		fmt.Sprintf("a=ice-ufrag:uf%d%03d\r\na=ice-pwd:pwdpwdpwdpwdpwdpwdpwdpwd\r\na=fingerprint:sha-256 %s\r\na=setup:actpass\r\na=mid:0\r\na=sctp-port:5000\r\n", end, id, fp))
}

type fakePC struct {
	id, use, gen      uint64
	local, remote     []byte
	candidates        int
	connected, closed bool
	confirmed         bool
	reason            string
}

// fakeEnd is the fake executor of one session: it performs effects in
// order, at once, and feeds results back like the daemon's executor does.
type fakeEnd struct {
	t         *testing.T
	which     int
	key       *testKey
	codec     *peerwire.Codec
	peer      peerwire.Public
	s         *Session
	now       *uint64
	pcs       map[uint64]*fakePC
	ready     map[uint64]*fakePC
	sent      []TransmitFrame
	events    []Event // SDK callbacks delivered on the next step
	hold      bool    // keep signatures back until release
	held      []SignFrame
	signs     int
	ended     string
	roundDone bool
	control   func(peerwire.Opened, uint64) (uint64, []byte)
	controls  int
	observe   func(Effect) // sees every effect, in the order performed
}

func newFakeEnd(t *testing.T, which int, key *testKey, peer peerwire.Public, codec *peerwire.Codec, connection []byte, now *uint64, deadline uint64, initiate bool, policy func() bool) *fakeEnd {
	t.Helper()
	e := &fakeEnd{t: t, which: which, key: key, codec: codec, peer: peer, now: now, pcs: map[uint64]*fakePC{}, ready: map[uint64]*fakePC{}}
	s, fx, err := NewSession(SessionConfig{Self: key.pub, Peer: peer.Member(), Shared: key.shared(peer), Codec: codec, Connection: connection, Deadline: deadline, Entropy: rand.Reader, Allow: func() bool { return true }, Policy: policy}, initiate, *now)
	if err != nil {
		t.Fatal(err)
	}
	e.s = s
	e.apply(fx)
	return e
}

func (e *fakeEnd) handle(ev Event) { e.apply(e.s.Handle(ev, *e.now)) }

func (e *fakeEnd) release() {
	held := e.held
	e.held = nil
	for i := len(held) - 1; i >= 0; i-- { // newest first
		e.handle(FrameSigned{held[i].Op, ed25519.Sign(e.key.sign, held[i].Message)})
	}
}

func (e *fakeEnd) apply(fx []Effect) {
	for _, f := range fx {
		if e.observe != nil {
			e.observe(f)
		}
		switch f := f.(type) {
		case SignFrame:
			e.signs++
			if e.hold {
				e.held = append(e.held, f)
				continue
			}
			e.handle(FrameSigned{f.Op, ed25519.Sign(e.key.sign, f.Message)})
		case TransmitFrame:
			e.sent = append(e.sent, f)
		case HandleControl:
			e.controls++
			typ, body := uint64(0), []byte(nil)
			if e.control != nil {
				typ, body = e.control(f.Frame, f.Now)
			}
			e.handle(ControlHandled{f.Op, typ, body})
		case CreatePC:
			e.pcs[f.ID] = &fakePC{id: f.ID, use: f.Use, gen: f.Generation}
			e.handle(PCCreated{f.ID, true})
		case DescribePC:
			pc := e.pcs[f.ID]
			pc.local = fakeSDP(e.which, f.ID)
			e.handle(PCDescribed{f.ID, pc.local, true})
			e.events = append(e.events,
				PCLocalCandidate{ID: f.ID, Candidate: []byte("candidate:1 1 udp 2130706431 127.0.0.1 50000 typ host"), Valid: true},
				PCLocalCandidate{ID: f.ID, End: true, Valid: true})
		case SetRemotePC:
			e.pcs[f.ID].remote = f.SDP
			e.handle(PCRemoteSet{f.ID, true})
		case AddCandidatesPC:
			e.pcs[f.ID].candidates += len(f.Candidates)
			e.handle(PCCandidatesAdded{f.ID, true})
		case ConfirmPC:
			pc := e.pcs[f.ID]
			pc.confirmed = true
			e.ready[f.Use] = pc
		case ClosePC:
			if pc := e.pcs[f.ID]; !pc.closed {
				pc.closed, pc.reason = true, f.Reason
				e.events = append(e.events, PCClosed{f.ID})
			}
		case RoundDone:
			e.roundDone = true
		case SessionEnd:
			e.ended = f.Reason
		}
	}
}

// opened authenticates a frame this end sent.
func (e *fakeEnd) opened(f TransmitFrame) peerwire.Opened {
	o, err := e.codec.OpenSent(f.Raw, e.key.pub, e.peer, e.key.shared(e.peer), *e.now)
	if err != nil {
		e.t.Fatal(err)
	}
	return o
}

type fakePair struct {
	now  uint64
	ends [2]*fakeEnd
	drop func(from int, f TransmitFrame) bool
}

// newFakePair starts end 0 as the initiator.
func newFakePair(t *testing.T, a, b *testKey) *fakePair {
	n := &fakePair{now: 1_700_000_000_000}
	codec := testCodec(t)
	connection := make([]byte, 16)
	_, _ = rand.Read(connection)
	deadline := n.now + 10000
	n.ends[1] = newFakeEnd(t, 1, b, a.pub, codec, connection, &n.now, deadline, false, nil)
	n.ends[0] = newFakeEnd(t, 0, a, b.pub, codec, connection, &n.now, deadline, true, nil)
	return n
}

func (n *fakePair) step() {
	for _, e := range n.ends {
		e.release()
	}
	for i, e := range n.ends {
		sent := e.sent
		e.sent = nil
		for _, f := range sent {
			if n.drop == nil || !n.drop(i, f) {
				n.ends[1-i].handle(FrameReceived{f.Raw})
			}
		}
	}
	for _, e := range n.ends {
		events := e.events
		e.events = nil
		for _, ev := range events {
			e.handle(ev)
		}
	}
	for _, pa := range n.ends[0].pcs {
		for _, pb := range n.ends[1].pcs {
			if !pa.closed && !pb.closed && !pa.connected && pa.local != nil && bytes.Equal(pa.local, pb.remote) && bytes.Equal(pa.remote, pb.local) {
				pa.connected, pb.connected = true, true
				n.ends[0].events = append(n.ends[0].events, PCStateChanged{pa.id, PCConnected, true})
				n.ends[1].events = append(n.ends[1].events, PCStateChanged{pb.id, PCConnected, true})
			}
		}
	}
	n.now += 25
	for _, e := range n.ends {
		e.handle(Tick{})
	}
}

// run steps until done or limit steps, and reports done.
func (n *fakePair) run(limit int, done func() bool) bool {
	for i := 0; i < limit && !done(); i++ {
		n.step()
	}
	return done()
}

func (n *fakePair) allReady() bool {
	for _, e := range n.ends {
		if e.ready[1] == nil || e.ready[2] == nil {
			return false
		}
	}
	return true
}

func TestSessionEstablishesBothUsesDespiteLoss(t *testing.T) {
	cases := []struct {
		name string
		drop func(n *fakePair, from int, f TransmitFrame) bool
		gen2 bool
	}{
		{name: "no loss"},
		{name: "first two initiator frames lost", drop: func() func(*fakePair, int, TransmitFrame) bool {
			lost := 0
			return func(_ *fakePair, from int, _ TransmitFrame) bool {
				if from == 0 && lost < 2 {
					lost++
					return true
				}
				return false
			}
		}()},
		{name: "two critical answerer frames lost", drop: func() func(*fakePair, int, TransmitFrame) bool {
			lost := 0
			return func(_ *fakePair, from int, f TransmitFrame) bool {
				if from == 1 && f.Critical && lost < 2 {
					lost++
					return true
				}
				return false
			}
		}()},
		// The offerer's sequence 2 of generation 1 never arrives: the
		// answerer's gap expires and both uses restart in generation 2.
		{name: "permanent gap restarts the generation", gen2: true, drop: func(n *fakePair, from int, f TransmitFrame) bool {
			if from != 0 || f.Class != 1 || f.Generation != 1 || f.Use == 0 {
				return false
			}
			return n.ends[0].opened(f).Context.Seq == 2
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, b := orderedKeys(t)
			n := newFakePair(t, a, b)
			if c.drop != nil {
				n.drop = func(from int, f TransmitFrame) bool { return c.drop(n, from, f) }
			}
			if !n.run(400, n.allReady) {
				t.Fatalf("not ready by the deadline: steps %v / %v", n.ends[0].s.Steps(), n.ends[1].s.Steps())
			}
			for _, e := range n.ends {
				if e.ended != "" {
					t.Fatalf("end %d ended: %s", e.which, e.ended)
				}
				if !e.roundDone {
					t.Fatal("RoundDone missing after both uses were ready")
				}
				for use := uint64(1); use <= 2; use++ {
					if gen := e.ready[use].gen; (gen >= 2) != c.gen2 {
						t.Fatalf("end %d use %d ready in generation %d", e.which, use, gen)
					}
				}
			}
		})
	}
}

// Signatures may complete late and in any order: frames wait, unsigned, in
// the signing queue and only then enter senders, caches and the wire.
func TestSessionEstablishesWithSignaturesCompletingOutOfOrder(t *testing.T) {
	a, b := orderedKeys(t)
	n := newFakePair(t, a, b)
	for _, e := range n.ends {
		e.hold = true
	}
	if !n.run(400, n.allReady) {
		t.Fatalf("not ready by the deadline: steps %v / %v", n.ends[0].s.Steps(), n.ends[1].s.Steps())
	}
}

func TestPartialRoundEndsWhenItsLastPCCloses(t *testing.T) {
	a, b := orderedKeys(t)
	n := newFakePair(t, a, b)
	n.drop = func(_ int, f TransmitFrame) bool { return f.Use == 2 }
	if !n.run(400, func() bool { return n.ends[0].ready[1] != nil && n.ends[1].ready[1] != nil }) {
		t.Fatal("use 1 not ready")
	}
	e := n.ends[0]
	if e.roundDone {
		t.Fatal("RoundDone before the deadline with use 2 pending")
	}
	n.run(400, func() bool { return e.roundDone })
	if !e.roundDone || e.ended != "" || !e.s.View().Established {
		t.Fatalf("after the deadline: roundDone=%v ended=%q", e.roundDone, e.ended)
	}
	for _, pc := range e.pcs {
		if pc.use == 2 && !pc.closed {
			t.Fatal("the unconfirmed use 2 PC outlived the round")
		}
	}
	for i := 0; i < 40; i++ {
		n.step()
	}
	if e.ended != "" {
		t.Fatalf("session ended with use 1 alive: %s", e.ended)
	}
	e.handle(PCClosed{e.ready[1].id})
	n.step()
	if e.ended != "paths_closed" {
		t.Fatalf("ended = %q, want paths_closed", e.ended)
	}
}

func TestRoundWithoutAnyReadyPCEndsAtTheDeadline(t *testing.T) {
	a, b := orderedKeys(t)
	n := newFakePair(t, a, b)
	n.drop = func(int, TransmitFrame) bool { return true }
	n.run(401, func() bool { return n.ends[0].ended != "" })
	if n.ends[0].ended != "round_deadline" || !n.ends[0].roundDone {
		t.Fatalf("ended = %q", n.ends[0].ended)
	}
}

func TestRetireEndsTheRoundAndKeepsTheReplayWindow(t *testing.T) {
	a, b := orderedKeys(t)
	n := newFakePair(t, a, b)
	e := n.ends[1]
	e.handle(RetireRound{})
	if !e.roundDone || e.ended != "" || !e.s.View().Retired {
		t.Fatalf("after retire: roundDone=%v ended=%q", e.roundDone, e.ended)
	}
	n.now = e.s.View().RetainUntil
	e.handle(Tick{})
	if e.ended != "retired_replay_window_over" {
		t.Fatalf("ended = %q", e.ended)
	}
}

// An SDK callback from a PC replaced by a restart must not touch the new one.
func TestCallbacksOfAReplacedPCAreIgnored(t *testing.T) {
	a, b := orderedKeys(t)
	var now uint64 = 1_700_000_000_000
	e := newFakeEnd(t, 0, a, b.pub, testCodec(t), make([]byte, 16), &now, now+10000, false, nil)
	p := &e.s.pcs[0]
	p.signal.BootReady = true
	e.handle(Tick{})
	old := p.pc
	if old == 0 {
		t.Fatal("offerer created no PC")
	}
	e.s.restartPC(0, now)
	e.apply(e.s.take())
	if p.pc == 0 || p.pc == old || !e.pcs[old].closed || e.pcs[old].reason != "generation_reset" {
		t.Fatal("restart did not replace the PC")
	}
	e.handle(PCLocalCandidate{ID: old, End: true, Valid: true})
	e.handle(PCStateChanged{old, PCConnected, true})
	e.handle(PCStateChanged{old, PCFailed, true})
	if p.ended || p.batch.Ended || p.signal.DTLS || p.signal.Generation != 2 {
		t.Fatal("old PC callback changed the replacement")
	}
	e.handle(PCLocalCandidate{ID: p.pc, End: true, Valid: true})
	if !p.ended && !p.batch.Ended {
		t.Fatal("current candidate callback was lost")
	}
}

func TestDeliveryResultsPauseOrDelayOnlyTheirFrame(t *testing.T) {
	for _, status := range []uint32{2, 3} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			a, b := orderedKeys(t)
			n := newFakePair(t, a, b)
			e := n.ends[0]
			if len(e.sent) != 1 || e.sent[0].Type != 1 {
				t.Fatal("initiator did not send its Probe")
			}
			fields, _ := peerwire.Parse(e.sent[0].Raw, 5)
			e.sent = nil
			e.handle(DeliveryResult{ID: []byte("other-frame-id!!"), Status: status, Retry: 5000})
			n.now += 600
			e.handle(Tick{})
			if len(e.sent) != 1 {
				t.Fatal("a result for another frame changed the Probe's retransmission")
			}
			e.sent = nil
			e.handle(DeliveryResult{ID: fields[4].B, Status: status, Retry: 5000})
			n.now += 1000
			e.handle(Tick{})
			if len(e.sent) != 0 {
				t.Fatal("the Probe was retransmitted although offline or limited")
			}
			e.handle(PeerDiscovered{})
			n.now += 4500
			e.handle(Tick{})
			if len(e.sent) != 1 {
				t.Fatal("the Probe did not resume")
			}
		})
	}
}

// standalone returns a session with both PC signals bootstrapped with peer.
func standalone(t *testing.T, a, b *testKey, codec *peerwire.Codec, now *uint64) *fakeEnd {
	e := newFakeEnd(t, 0, a, b.pub, codec, make([]byte, 16), now, *now+10000, false, func() bool { return false })
	for i := range e.s.pcs {
		e.s.pcs[i].signal.Peer = b.pub
		e.s.pcs[i].signal.BootReady = true
	}
	return e
}

func TestPeerScopeRestartValidatesBothTargetsAndKeepsOriginalReceipt(t *testing.T) {
	a, b := orderedKeys(t)
	codec := testCodec(t)
	var now uint64 = 1_700_000_000_000
	e := standalone(t, a, b, codec, &now)
	e.hold = true
	frame := func(generation2 uint64) []byte {
		body := peerwire.PB(peerwire.B(1, peerwire.PB(peerwire.U(1, 1), peerwire.U(2, 1))), peerwire.B(1, peerwire.PB(peerwire.U(1, 2), peerwire.U(2, generation2))))
		return b.seal(t, codec, a.pub, peerwire.NewContext(b.pub, a.pub, make([]byte, 16), 0, 1, 1, now, 1, false), 8, body)
	}
	e.handle(FrameReceived{frame(2)})
	if e.s.pcs[0].signal.Generation != 1 || e.s.pcs[1].signal.Generation != 1 || e.signs != 0 {
		t.Fatal("invalid second target partially restarted the first PC")
	}
	raw := frame(1)
	e.handle(FrameReceived{raw})
	if e.s.pcs[0].signal.Generation != 2 || e.s.pcs[1].signal.Generation != 2 || e.signs != 1 || len(e.sent) != 0 {
		t.Fatal("peer-scope restart did not advance both PCs and prepare one ACK")
	}
	e.handle(FrameReceived{raw}) // while the ACK waits for its signature
	if e.signs != 1 || len(e.sent) != 0 {
		t.Fatal("a duplicate prepared a second ACK")
	}
	e.release()
	now++
	e.handle(FrameReceived{raw})
	if e.signs != 1 || len(e.sent) != 2 || !bytes.Equal(e.sent[0].Raw, e.sent[1].Raw) {
		t.Fatal("retired generation duplicate lost its original ACK")
	}
	if e.s.cfg.Deadline != 1_700_000_000_000+10000 {
		t.Fatal("restart refreshed the original deadline")
	}
}

func TestControlSeenAndReceiptsExpirePast2047Controls(t *testing.T) {
	a, b := orderedKeys(t)
	codec := testCodec(t)
	var now uint64 = 1_700_000_000_000
	e := standalone(t, a, b, codec, &now)
	e.s.established = true
	e.control = func(o peerwire.Opened, _ uint64) (uint64, []byte) {
		if o.Type == 15 {
			return 4, peerwire.PB(peerwire.U(4, o.Context.Class), peerwire.U(5, o.Context.Seq), peerwire.B(6, o.Hash), peerwire.U(7, o.Context.Generation), peerwire.U(8, o.Context.Use))
		}
		return 0, nil
	}
	body := []byte{0x0a, 0x02, 's', '1'} // DirectEventSubscription{session_ids: ["s1"]}
	seq := uint64(0)
	control := func() []byte {
		seq++
		return b.seal(t, codec, a.pub, peerwire.NewContext(b.pub, a.pub, make([]byte, 16), 0, 1, seq, now, 2, false), 15, body)
	}
	// 5 x 500 control frames (> 2047), moving the clock past deadline + 300 s
	// between batches so expired controlSeen and receipts are pruned.
	for batch := range 5 {
		for range 500 {
			e.handle(FrameReceived{control()})
		}
		if len(e.s.controlSeen) > 500 || len(e.s.receipts) > 500 {
			t.Fatalf("batch %d: controlSeen=%d receipts=%d", batch, len(e.s.controlSeen), len(e.s.receipts))
		}
		now += 310_001
		e.s.flush(now)
		if len(e.s.controlSeen) != 0 || len(e.s.receipts) != 0 {
			t.Fatalf("batch %d: flush kept controlSeen=%d receipts=%d", batch, len(e.s.controlSeen), len(e.s.receipts))
		}
	}
	e.handle(FrameReceived{control()})
	if e.controls != 2501 || len(e.sent) != 2501 {
		t.Fatalf("handled=%d replies=%d, want 2501", e.controls, len(e.sent))
	}
}

// A control reply is cached once signed; a duplicate that arrives while the
// owner's reply is still unsigned gets nothing, later ones the same bytes.
func TestControlReplyIsCachedOnlyOnceSigned(t *testing.T) {
	a, b := orderedKeys(t)
	codec := testCodec(t)
	var now uint64 = 1_700_000_000_000
	e := standalone(t, a, b, codec, &now)
	e.hold = true
	raw := b.seal(t, codec, a.pub, peerwire.NewContext(b.pub, a.pub, make([]byte, 16), 0, 1, 1, now, 2, false), 15, []byte{0x0a, 0x02, 's', '1'})
	e.handle(FrameReceived{raw})
	e.handle(FrameReceived{raw})
	if e.controls != 1 || e.signs != 1 || len(e.sent) != 0 {
		t.Fatalf("controls=%d signs=%d sent=%d", e.controls, e.signs, len(e.sent))
	}
	e.release()
	e.handle(FrameReceived{raw})
	if e.signs != 1 || len(e.sent) != 2 || !bytes.Equal(e.sent[0].Raw, e.sent[1].Raw) || e.sent[0].Type != 10 {
		t.Fatal("the cached reply was not resent unchanged")
	}
}

// The answerer's Candidates(seq 1), Confirm(seq 2) and Restart(seq 3) reach
// the offerer with seq 1 last. The PC waits on the SDK for the candidates
// while Confirm and Restart are still queued; the Restart must replace the PC
// before readiness is judged, so the old PC is never confirmed.
func TestQueuedRestartWinsOverReadinessWhileTheSDKIsBusy(t *testing.T) {
	a, b := orderedKeys(t)
	codec := testCodec(t)
	var now uint64 = 1_700_000_000_000
	conn := make([]byte, 16)
	_, _ = rand.Read(conn)
	e := newFakeEnd(t, 0, a, b.pub, codec, conn, &now, now+10000, false, nil)
	var log []string
	e.observe = func(f Effect) {
		switch f := f.(type) {
		case CreatePC:
			log = append(log, fmt.Sprintf("create%d", f.ID))
		case AddCandidatesPC:
			log = append(log, fmt.Sprintf("addcand%d", f.ID))
		case ConfirmPC:
			log = append(log, fmt.Sprintf("confirm%d", f.ID))
		case ClosePC:
			log = append(log, fmt.Sprintf("close%d:%s", f.ID, f.Reason))
		}
	}
	s := e.s
	for i := range s.pcs {
		s.pcs[i].signal.Peer = b.pub
	}
	p := &s.pcs[0]
	g := p.signal
	h := func(v byte) []byte { return bytes.Repeat([]byte{v}, 32) }
	g.BootReady, g.LocalSDP, g.RemoteSDP, g.DTLS = true, true, true, true
	g.LocalHash, g.RemoteHash, g.LocalFingerprint, g.RemoteFingerprint = h(1), h(2), h(3), h(4)
	g.RemoteUfrag, g.RemoteMid = []byte("ufBB"), []byte("0")
	e.pcs[7] = &fakePC{id: 7, use: 1, gen: 1}
	p.pc, p.confirmed = 7, true
	s.nextPC = 7
	frame := func(seq, typ uint64, body []byte) []byte {
		c := peerwire.NewContext(b.pub, a.pub, conn, 1, 1, seq, now, 1, false)
		c.Deadline = now + 9000
		return b.seal(t, codec, a.pub, c, typ, body)
	}
	candidates := frame(1, 5, peerwire.CandidateBody([]peerwire.Candidate{{Original: []byte("candidate:1 1 udp 2130706431 127.0.0.1 50000 typ host"), Ufrag: g.RemoteUfrag, Mid: g.RemoteMid, Generation: 1}}, 0))
	confirm := frame(2, 7, g.ConfirmBody())
	restart := frame(3, 8, peerwire.PB(peerwire.B(1, peerwire.PB(peerwire.U(1, 1), peerwire.U(2, 1)))))
	e.handle(FrameReceived{confirm})
	e.handle(FrameReceived{restart})
	log = nil
	e.handle(FrameReceived{candidates})
	if want := "[addcand7 close7:generation_reset create8]"; fmt.Sprint(log) != want {
		t.Fatalf("effects %v, want %s", log, want)
	}
	if p.announced || p.business || s.established || p.generation != 2 {
		t.Fatalf("a replaced PC was confirmed: announced=%v established=%v generation=%d", p.announced, s.established, p.generation)
	}
}

// NextTimer names the retransmission an idle executor must wake for, and
// nothing while the sender is paused.
func TestNextTimerIsTheProbeRetransmission(t *testing.T) {
	a, b := orderedKeys(t)
	n := newFakePair(t, a, b)
	e, start := n.ends[0], n.now
	if due, ok := e.s.NextTimer(); !ok || due != start+500 {
		t.Fatalf("NextTimer = %d, %v; want the Probe's first retry", due-start, ok)
	}
	e.handle(PeerDiscovered{})
	fields, _ := peerwire.Parse(e.sent[0].Raw, 5)
	e.handle(DeliveryResult{ID: fields[4].B, Status: 2})
	if _, ok := e.s.NextTimer(); ok {
		t.Fatal("a paused sender still has a timer")
	}
	if _, ok := n.ends[1].s.NextTimer(); ok {
		t.Fatal("the responder has a timer before any frame")
	}
}

func TestStepsAreWords(t *testing.T) {
	if got := steps(true, true, true, false, false, false); got != "blr---" {
		t.Fatalf("steps = %q", got)
	}
	if got := steps(true, true, true, true, true, true); got != "blrdca" {
		t.Fatalf("steps = %q", got)
	}
}

func TestPeerUnsupportedAndSignalRejectedAndMarks(t *testing.T) {
	a, b := orderedKeys(t)
	n := newFakePair(t, a, b)
	e := n.ends[0]
	for i := 0; i < 6 && (!e.s.Booted() || len(e.s.pcs[0].sender.Pending) == 0); i++ {
		n.step()
	}
	if !e.s.Booted() {
		t.Fatal("expected offerer Booted() == true after Challenge")
	}
	marks := e.s.Marks()
	if !marks[0][0] {
		t.Fatalf("expected Boot mark on use 1, got %+v", marks)
	}

	// Find a pending Offer frame from a -> b (Class=1, Type=3, Use=1).
	var offer peerwire.Opened
	var offerRaw []byte
	for _, p := range e.s.pcs[0].sender.Pending {
		o, err := e.codec.OpenSent(p.Raw, a.pub, b.pub, a.shared(b.pub), n.now)
		if err == nil && o.Context.Class == 1 && o.Type == 3 && o.Context.Use == 1 {
			offer, offerRaw = o, p.Raw
			break
		}
	}
	if len(offerRaw) == 0 {
		t.Fatal("expected pending Offer on use 1")
	}

	// Tampered frame emits SignalRejected.
	bad := append([]byte(nil), offerRaw...)
	bad[len(bad)-1] ^= 1
	var rejected bool
	for _, eff := range e.s.Handle(FrameReceived{bad}, n.now) {
		if _, ok := eff.(SignalRejected); ok {
			rejected = true
		}
	}
	if !rejected {
		t.Fatal("expected SignalRejected on tampered frame")
	}

	// Unsupported response targeting pending Offer closes use 1 PC and emits PeerUnsupported.
	uBody := peerwire.PB(peerwire.U(1, offer.Context.Class), peerwire.U(2, offer.Type), peerwire.U(3, offer.Context.Seq), peerwire.B(4, offer.Hash))
	uc := peerwire.NewContext(b.pub, a.pub, e.s.cfg.Connection, 0, 1, 99, n.now, 2, false)
	uc.Deadline = n.now + 9000
	uRaw := b.seal(t, e.codec, a.pub, uc, 10, uBody)
	var gotUnsupported []PeerUnsupported
	var closedPC bool
	for _, eff := range e.s.Handle(FrameReceived{uRaw}, n.now) {
		switch eff := eff.(type) {
		case PeerUnsupported:
			gotUnsupported = append(gotUnsupported, eff)
		case ClosePC:
			if eff.Reason == "peer_unsupported" {
				closedPC = true
			}
		}
	}
	if len(gotUnsupported) != 1 || gotUnsupported[0] != (PeerUnsupported{Class: 1, Type: 3, Use: 1}) {
		t.Fatalf("got Unsupported %+v", gotUnsupported)
	}
	if !closedPC || !e.s.pcs[0].signal.Closed {
		t.Fatalf("expected use 1 PC closed as peer_unsupported (closedPC=%v signal.Closed=%v)", closedPC, e.s.pcs[0].signal.Closed)
	}
}

type limitedEntropyReader struct {
	buf []byte
}

func (r *limitedEntropyReader) Read(p []byte) (int, error) {
	if len(r.buf) < len(p) {
		return 0, ErrNeedEntropy
	}
	n := copy(p, r.buf)
	copy(r.buf, r.buf[n:])
	r.buf = r.buf[:len(r.buf)-n]
	return n, nil
}

func (r *limitedEntropyReader) topUp(n int) {
	extra := make([]byte, n)
	_, _ = rand.Read(extra)
	r.buf = append(r.buf, extra...)
}

func TestSessionQueuesAndResumesOnEntropyExhaustion(t *testing.T) {
	a, b := orderedKeys(t)
	codec := testCodec(t)
	var now uint64 = 1_700_000_000_000
	conn := make([]byte, 16)
	_, _ = io.ReadFull(rand.Reader, conn)
	deadline := now + 10000

	rA := &limitedEntropyReader{} // empty initially: Probe queues on NeedEntropy
	eA := &fakeEnd{t: t, which: 0, key: a, codec: codec, peer: b.pub, now: &now, pcs: map[uint64]*fakePC{}, ready: map[uint64]*fakePC{}}
	eB := newFakeEnd(t, 1, b, a.pub, codec, conn, &now, deadline, false, nil)

	sessA, fxA, err := NewSession(SessionConfig{
		Self: a.pub, Peer: b.pub.Member(), Shared: a.shared(b.pub), Codec: codec,
		Connection: conn, Deadline: deadline, Entropy: rA, Allow: func() bool { return true },
	}, true, now)
	if err != nil {
		t.Fatalf("NewSession with exhausted entropy: %v", err)
	}
	eA.s = sessA
	var needBytes int
	for _, f := range fxA {
		if ne, ok := f.(NeedEntropy); ok {
			needBytes = ne.Bytes
		}
	}
	if needBytes != peerwire.SealEntropy {
		t.Fatalf("expected NeedEntropy(%d) on Probe, got %d", peerwire.SealEntropy, needBytes)
	}
	eA.apply(fxA)
	if len(eA.sent) != 0 {
		t.Fatal("Probe must not transmit before entropy arrives")
	}

	// Refill just enough for Probe (1 frame), then exhaust mid-handshake on Offers/Candidates.
	rA.topUp(peerwire.SealEntropy)
	eA.handle(EntropyReady{})
	if len(eA.sent) != 1 || eA.sent[0].Type != 1 {
		t.Fatalf("expected Probe after EntropyReady, got %+v", eA.sent)
	}

	pair := &fakePair{now: now, ends: [2]*fakeEnd{eA, eB}}
	sawMidNeed := false
	eA.observe = func(f Effect) {
		if _, ok := f.(NeedEntropy); ok {
			sawMidNeed = true
			// Refill synchronously on the next step after queueing multiple frames.
		}
	}
	for i := 0; i < 400 && !pair.allReady(); i++ {
		if rA.buf == nil || len(rA.buf) < peerwire.SealEntropy {
			// Let one step run while empty if we haven't observed mid-handshake NeedEntropy yet.
			if i > 2 || sawMidNeed {
				rA.topUp(512)
				eA.handle(EntropyReady{})
			}
		}
		pair.step()
	}
	if !sawMidNeed {
		t.Fatal("expected NeedEntropy mid-handshake after initial refill")
	}
	if !pair.allReady() {
		t.Fatalf("session did not establish after entropy refill: steps %v / %v", eA.s.Steps(), eB.s.Steps())
	}
}

func TestSessionPostAnnounceGapDeadlineNoTimerSpin(t *testing.T) {
	// Audit core B2a: out-of-order signal frame arriving after PC is announced
	// must not leave GapDeadline stuck in the past causing 1ms NextTimer spin.
	a, b := orderedKeys(t)
	p := newFakePair(t, a, b)
	for i := 0; i < 400 && !p.allReady(); i++ {
		p.step()
	}
	if !p.allReady() {
		t.Fatalf("did not establish")
	}
	eA, eB := p.ends[0], p.ends[1]
	// Forge a future-seq (seq = Next+1) EndCandidates frame from B to A on use=1 so A buffers it and sets GapDeadline.
	nextSeq := eA.s.pcs[0].signal.Next
	ufrag := eA.s.pcs[0].signal.RemoteUfrag
	ctx := peerwire.NewContext(eB.key.pub, eA.key.pub, eA.s.cfg.Connection, 1, eA.s.pcs[0].generation, nextSeq+1, p.now, 1, false)
	ctx.Deadline = p.now + 5000
	body := peerwire.EndBody(ufrag, uint64(eA.s.pcs[0].signal.Candidates), uint64(eA.s.pcs[0].signal.Batches))
	raw := eB.key.seal(t, eB.codec, eA.key.pub, ctx, 6, body)
	eA.handle(FrameReceived{Raw: raw})
	if eA.s.pcs[0].signal.GapDeadline == 0 {
		t.Fatalf("expected GapDeadline to be set for out-of-order frame")
	}
	// Advance past GapDeadline + 20s and tick.
	p.now += 25000
	eA.handle(Tick{})
	if due, ok := eA.s.NextTimer(); ok && due <= p.now {
		t.Fatalf("B2a: NextTimer returned past due=%d <= now=%d", due, p.now)
	}
	if !eA.s.pcs[0].announced || eA.s.pcs[0].pc == 0 {
		t.Fatalf("announced PC must remain alive after post-announce gap cleanup")
	}
}

func TestSessionRetiredPeerScopeNoTimerSpinAndNoTransmit(t *testing.T) {
	// Audit core B2b & B10b: expired PeerScope gap retires session without leaving
	// NextTimer spinning in the past, and retired sessions must not transmit from flush.
	a, b := orderedKeys(t)
	p := newFakePair(t, a, b)
	eA, eB := p.ends[0], p.ends[1]
	// Complete bootstrap first.
	for i := 0; i < 20 && !eA.s.Booted(); i++ {
		p.step()
	}
	if !eA.s.Booted() {
		t.Fatal("did not boot")
	}
	// Send out-of-order peer-scope Close (seq=2, use=0) from B to A to open a PeerScope gap.
	ctx := peerwire.NewContext(eB.key.pub, eA.key.pub, eA.s.cfg.Connection, 0, 1, 2, p.now, 1, false)
	ctx.Deadline = p.now + 5000
	raw := eB.key.seal(t, eB.codec, eA.key.pub, ctx, 9, nil)
	eA.handle(FrameReceived{Raw: raw})
	// Advance past the 2.5s peerScope gap so flush retires the session.
	p.now += 3000
	eA.handle(Tick{})
	if !eA.s.View().Retired {
		t.Fatal("expected session to retire on expired peerScope gap")
	}
	// Advance further and check NextTimer doesn't spin in the past.
	p.now += 5000
	if due, ok := eA.s.NextTimer(); ok && due <= p.now {
		t.Fatalf("B2b: retired session NextTimer returned past due=%d <= now=%d", due, p.now)
	}

	// B10b: explicit RetireRound while Probe is pending must not transmit on subsequent Tick.
	a2, b2 := orderedKeys(t)
	codec := testCodec(t)
	now := uint64(1_700_000_000_000)
	conn := make([]byte, 16)
	_, _ = rand.Read(conn)
	e := newFakeEnd(t, 0, a2, b2.pub, codec, conn, &now, now+10000, true, nil)
	e.sent = nil
	e.handle(RetireRound{})
	now += 600
	e.handle(Tick{})
	if len(e.sent) != 0 {
		t.Fatalf("B10b: retired session transmitted %d frames on Tick", len(e.sent))
	}
}

func TestSessionControlWindowRenewalAndOfflineDiscovery(t *testing.T) {
	// Audit core B3a & B3b + daemon Opt 1:
	// 1) A new control frame queued after an un-ACKed control frame's 10s deadline must transmit.
	// 2) PeerDiscovered must resume controlWindow after Status=2 (offline) and flush immediately.
	a, b := orderedKeys(t)
	p := newFakePair(t, a, b)
	for i := 0; i < 400 && !p.allReady(); i++ {
		p.step()
	}
	if !p.allReady() {
		t.Fatal("did not establish")
	}
	eA := p.ends[0]
	eA.sent = nil

	// Queue control #1 (Control 15 subscription) and let it exhaust retries without ACK.
	body1 := peerwire.PB(peerwire.B(1, []byte("sess-1")))
	eA.handle(QueueControl{Type: 15, Body: body1})
	if len(eA.sent) != 1 {
		t.Fatalf("expected initial control #1 send, got %d", len(eA.sent))
	}
	p.now += 500
	eA.handle(Tick{})
	p.now += 1000
	eA.handle(Tick{})
	if len(eA.sent) != 3 {
		t.Fatalf("expected 3 total attempts for control #1, got %d", len(eA.sent))
	}

	// Advance past control #1's 10s window WITHOUT ticking first, then queue control #2.
	p.now += 9000 // total +10500ms since control #1
	eA.sent = nil
	body2 := peerwire.PB(peerwire.B(1, []byte("sess-2")))
	eA.handle(QueueControl{Type: 15, Body: body2})
	if len(eA.sent) != 1 {
		t.Fatalf("B3a: expected control #2 to transmit after old window expired, got %d", len(eA.sent))
	}

	// B3b + Opt 1: deliver Status=2 (offline) for control #2, then PeerDiscovered must unpause and flush.
	fields, err := peerwire.Parse(eA.sent[0].Raw, 5)
	if err != nil {
		t.Fatal(err)
	}
	eA.handle(DeliveryResult{ID: fields[4].B, Status: 2})
	eA.sent = nil
	p.now += 600
	eA.handle(PeerDiscovered{})
	if len(eA.sent) == 0 {
		t.Fatalf("B3b/Opt1: PeerDiscovered failed to unpause and immediately flush controlWindow")
	}
}

func TestSessionSDKCallbackBugs(t *testing.T) {
	// Audit core B5a, B5b, B5c, B5d.
	a, b := orderedKeys(t)
	codec := testCodec(t)
	now := uint64(1_700_000_000_000)
	conn := make([]byte, 16)
	_, _ = rand.Read(conn)
	deadline := now + 10000

	// Helper to create an offerer session already bootstrapped with PC 1 created.
	setupOfferer := func() (*Session, *fakeEnd) {
		eA := newFakeEnd(t, 0, a, b.pub, codec, conn, &now, deadline, true, nil)
		eB := newFakeEnd(t, 1, b, a.pub, codec, conn, &now, deadline, false, nil)
		// Deliver Probe -> Challenge -> A receives Challenge -> CreatePC emitted.
		for _, f := range eA.sent {
			eB.handle(FrameReceived{Raw: f.Raw})
		}
		eA.sent = nil
		var chal []byte
		for _, f := range eB.sent {
			if f.Type == 2 {
				chal = f.Raw
			}
		}
		if chal == nil {
			t.Fatal("missing challenge")
		}
		// Drive A manually without auto-describing.
		sA := eA.s
		fx := sA.Handle(FrameReceived{Raw: chal}, now)
		// Sign ACK if needed and collect CreatePC.
		var created uint64
		for _, f := range fx {
			if c, ok := f.(CreatePC); ok && c.Use == 1 {
				created = c.ID
			}
		}
		if created == 0 {
			t.Fatal("expected CreatePC for use 1")
		}
		return sA, eB
	}

	// B5b: PCDescribed{OK: false} must close PC and not loop DescribePC on every Tick.
	t.Run("B5b_PCDescribedFailureClosesPC", func(t *testing.T) {
		sA, _ := setupOfferer()
		fx := sA.Handle(PCCreated{ID: 1, OK: true}, now)
		var sawDesc bool
		for _, f := range fx {
			if _, ok := f.(DescribePC); ok {
				sawDesc = true
			}
		}
		if !sawDesc {
			t.Fatal("expected initial DescribePC")
		}
		fx = sA.Handle(PCDescribed{ID: 1, OK: false}, now)
		var sawClose bool
		for _, f := range fx {
			if c, ok := f.(ClosePC); ok && c.ID == 1 {
				sawClose = true
			}
		}
		if !sawClose {
			t.Fatal("B5b: expected ClosePC on PCDescribed failure")
		}
		fx = sA.Handle(Tick{}, now+25)
		for _, f := range fx {
			if _, ok := f.(DescribePC); ok {
				t.Fatal("B5b: looped DescribePC on Tick after describe failure")
			}
		}
	})

	// B5d: Duplicate PCCreated{ID: 1, OK: true} must be ignored (not kill active PC 1),
	// and late PCCreated after deadline must be closed without emitting PCChosen.
	t.Run("B5d_DuplicateAndLatePCCreated", func(t *testing.T) {
		sA, _ := setupOfferer()
		_ = sA.Handle(PCCreated{ID: 1, OK: true}, now)
		fx := sA.Handle(PCCreated{ID: 1, OK: true}, now)
		for _, f := range fx {
			if c, ok := f.(ClosePC); ok && c.ID == 1 {
				t.Fatal("B5d: duplicate PCCreated killed active PC 1")
			}
		}
		// Late PCCreated for use 2 (ID 2) after deadline must close without PCChosen.
		fx = sA.Handle(PCCreated{ID: 2, OK: true}, deadline+1)
		for _, f := range fx {
			if _, ok := f.(PCChosen); ok {
				t.Fatal("B5d: late PCCreated after deadline emitted PCChosen")
			}
		}
	})

	// B5a: PCCandidatesAdded{OK: false} after PCConnected must still drainActions/maybeReady and emit Confirm.
	t.Run("B5a_CandidatesAddedFailureStillConfirms", func(t *testing.T) {
		pa, pb := orderedKeys(t)
		p := newFakePair(t, pa, pb)
		eA, eB := p.ends[0], p.ends[1]
		// Hold AddCandidatesPC on A for use 1 so we can connect DTLS first, then fail AddCandidatesPC.
		var heldCand *AddCandidatesPC
		eA.observe = func(f Effect) {}
		// Establish bootstrap + SDP exchange manually by running until remote SDP is set on A.
		sA := eA.s
		origStep := false
		for i := 0; i < 60; i++ {
			if sA.pcs[0].signal.LocalSDP && sA.pcs[0].signal.RemoteSDP {
				origStep = true
				break
			}
			p.step()
		}
		if !origStep {
			t.Fatal("did not exchange SDPs")
		}
		// Inject a candidate batch from B to A directly via sA.Handle so A enters opCandidates.
		ufrag := sA.pcs[0].signal.RemoteUfrag
		mid := sA.pcs[0].signal.RemoteMid
		candLine := []byte("candidate:1 1 UDP 2122252543 192.0.2.1 54321 typ host")
		cands := []peerwire.Candidate{{Original: candLine, Ufrag: ufrag, Mid: mid, Generation: 1}}
		cCtx := peerwire.NewContext(eB.key.pub, eA.key.pub, sA.cfg.Connection, 1, 1, sA.pcs[0].signal.Next, p.now, 1, false)
		cCtx.Deadline = p.now + 5000
		rawCand := eB.key.seal(t, eB.codec, eA.key.pub, cCtx, 5, peerwire.CandidateBody(cands, uint64(sA.pcs[0].signal.Batches)))
		fx := sA.Handle(FrameReceived{Raw: rawCand}, p.now)
		for _, f := range fx {
			if ac, ok := f.(AddCandidatesPC); ok && ac.ID == sA.pcs[0].pc {
				cp := ac
				heldCand = &cp
			}
		}
		if heldCand == nil {
			t.Fatal("expected AddCandidatesPC effect")
		}
		// Now PC connects while opCandidates is in flight (so Confirm is deferred).
		_ = sA.Handle(PCStateChanged{ID: sA.pcs[0].pc, State: PCConnected, Valid: true}, p.now)
		if sA.pcs[0].confirmed || sA.pcs[0].confirming {
			t.Fatal("confirm should be deferred while opCandidates is busy")
		}
		// Now AddCandidatesPC returns OK: false -> must still trigger maybeReady -> SignFrame(Confirm).
		fx = sA.Handle(PCCandidatesAdded{ID: sA.pcs[0].pc, OK: false}, p.now)
		if !sA.pcs[0].confirming && !sA.pcs[0].confirmed {
			t.Fatalf("B5a: PCCandidatesAdded{OK:false} stalled Confirm (effects=%d)", len(fx))
		}
	})

	// B5c: PCRemoteSet{OK: false} must clear p.pc = 0 and set signal.Closed so no duplicate ClosePC.
	t.Run("B5c_ClosePCClearsPCID", func(t *testing.T) {
		sA, _ := setupOfferer()
		_ = sA.Handle(PCCreated{ID: 1, OK: true}, now)
		sA.pcs[0].busy = opRemoteAnswer
		fx := sA.Handle(PCRemoteSet{ID: 1, OK: false}, now)
		closes := 0
		for _, f := range fx {
			if c, ok := f.(ClosePC); ok && c.ID == 1 {
				closes++
			}
		}
		if closes != 1 || sA.pcs[0].pc != 0 || !sA.pcs[0].signal.Closed {
			t.Fatalf("B5c: expected 1 ClosePC, p.pc=0, signal.Closed=true; got closes=%d pc=%d closed=%v", closes, sA.pcs[0].pc, sA.pcs[0].signal.Closed)
		}
		fx2 := sA.Handle(Tick{}, now+25)
		for _, f := range fx2 {
			if c, ok := f.(ClosePC); ok && c.ID == 1 {
				t.Fatal("B5c: duplicate ClosePC emitted on subsequent Tick")
			}
		}
	})
}

func TestSessionLocalCandidateEndFlushAndPreDescribeGuard(t *testing.T) {
	// Audit core B13 + web-ios #11 + daemon Opt 1:
	// 1) Local candidate & End arriving BEFORE PCDescribed must NOT flush with empty ufrag/mid;
	//    once PCDescribed completes, Offer is sent first and buffered candidates+End flush with valid ufrag/mid.
	// 2) PCLocalCandidate{End: true} with 0 candidates after LocalSDP flushes EndCandidates immediately.
	a, b := orderedKeys(t)
	codec := testCodec(t)
	now := uint64(1_700_000_000_000)
	conn := make([]byte, 16)
	_, _ = rand.Read(conn)
	deadline := now + 10000

	eA := newFakeEnd(t, 0, a, b.pub, codec, conn, &now, deadline, true, nil)
	eB := newFakeEnd(t, 1, b, a.pub, codec, conn, &now, deadline, false, nil)
	for _, f := range eA.sent {
		eB.handle(FrameReceived{Raw: f.Raw})
	}
	var chal []byte
	for _, f := range eB.sent {
		if f.Type == 2 {
			chal = f.Raw
		}
	}
	sA := eA.s
	_ = sA.Handle(FrameReceived{Raw: chal}, now)
	_ = sA.Handle(PCCreated{ID: 1, OK: true}, now)
	// Before PCDescribed completes, a local candidate AND End=true arrive!
	cand := []byte("candidate:1 1 UDP 2122252543 192.0.2.1 54321 typ host")
	fx1 := sA.Handle(PCLocalCandidate{ID: 1, Candidate: cand, Valid: true}, now+5)
	fx2 := sA.Handle(PCLocalCandidate{ID: 1, End: true, Valid: true}, now+10)
	fxTick := sA.Handle(Tick{}, now+100)
	for _, list := range [][]Effect{fx1, fx2, fxTick} {
		for _, f := range list {
			if _, ok := f.(SignFrame); ok {
				t.Fatalf("web-ios #11 / B13: emitted candidate/End SignFrame before PCDescribed")
			}
		}
	}
	// Now PCDescribed completes -> Offer (seq 1), Candidate batch (seq 2 with ufrag/mid), EndCandidates (seq 3).
	sdp := fakeSDP(0, 1)
	fxDesc := sA.Handle(PCDescribed{ID: 1, SDP: sdp, OK: true}, now+120)
	var signedTypes []uint64
	for _, f := range fxDesc {
		if sf, ok := f.(SignFrame); ok {
			sig := ed25519.Sign(a.sign, sf.Message)
			for _, tf := range sA.Handle(FrameSigned{Op: sf.Op, Signature: sig}, now+120) {
				if tr, ok := tf.(TransmitFrame); ok && tr.Use == 1 {
					signedTypes = append(signedTypes, tr.Type)
					if tr.Type == 5 {
						opened, err := codec.OpenSent(tr.Raw, a.pub, sA.Peer(), a.shared(b.pub), now+120)
						if err != nil {
							t.Fatal(err)
						}
						cs, _, err := peerwire.ReadCandidates(opened.Payload)
						if err != nil || len(cs) != 1 || len(cs[0].Ufrag) == 0 || len(cs[0].Mid) == 0 {
							t.Fatalf("web-ios #11: candidate missing ufrag/mid: err=%v cs=%+v", err, cs)
						}
					}
				}
			}
		}
	}
	if len(signedTypes) != 3 || signedTypes[0] != 3 || signedTypes[1] != 5 || signedTypes[2] != 6 {
		t.Fatalf("B13/web-ios #11: expected Offer(3), Candidates(5), End(6) after PCDescribed, got %v", signedTypes)
	}

	// Case 2: 0 candidates + PCLocalCandidate{End: true} after LocalSDP flushes EndCandidates immediately.
	_ = sA.Handle(PCCreated{ID: 2, OK: true}, now+130)
	_ = sA.Handle(PCDescribed{ID: 2, SDP: fakeSDP(0, 2), OK: true}, now+135)
	fxEnd := sA.Handle(PCLocalCandidate{ID: 2, End: true, Valid: true}, now+140)
	sawEndSign := false
	for _, f := range fxEnd {
		if _, ok := f.(SignFrame); ok {
			sawEndSign = true
		}
	}
	if !sawEndSign {
		t.Fatalf("B13: 0-candidate PCLocalCandidate{End:true} did not immediately flush EndCandidates")
	}
}
