package relay_test

import (
	"bytes"
	"sync"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

type blockingPhase3Terminal struct {
	*phase3FakeTerminal
	blockInput chan struct{}
	inputHit   chan struct{}
	hitOnce    sync.Once
}

func (b *blockingPhase3Terminal) Input(data []byte) error {
	b.hitOnce.Do(func() {
		if b.inputHit != nil {
			close(b.inputHit)
		}
	})
	if b.blockInput != nil {
		<-b.blockInput
	}
	return b.phase3FakeTerminal.Input(data)
}

type multiTermBackend struct {
	*fakeBackend
	mu     sync.Mutex
	terms  []*phase3FakeTerminal
	nextFn func() relay.Terminal
}

func (b *multiTermBackend) OpenTerminal(string) (relay.Terminal, error) {
	if b.nextFn != nil {
		return b.nextFn(), nil
	}
	t := newPhase3FakeTerminal()
	b.mu.Lock()
	b.terms = append(b.terms, t)
	b.mu.Unlock()
	return t, nil
}

// TestPhase3TerminalOpenClose18StreamsReclaimsRegistry verifies H3:
// opening and normally closing 18 sequential terminals on the same daemon
// reclaims registry slots every time instead of exhausting the 16-stream limit.
func TestPhase3TerminalOpenClose18StreamsReclaimsRegistry(t *testing.T) {
	e := setup(t)
	const session = "pion-phase3-h3-reclaim"
	e.backend.add(t, session)
	backend := &multiTermBackend{fakeBackend: e.backend}
	running := e.startRelayBackend(backend)
	e.online(t, true)
	s := e.session(t, session, 0)

	for i := range 18 {
		term, err := e.client.OpenTerminal(t.Context(), s)
		if err != nil {
			t.Fatalf("stream %d OpenTerminal failed: %v", i, err)
		}
		if _, err := term.Read(t.Context()); err != nil {
			t.Fatalf("stream %d initial snapshot read: %v", i, err)
		}
		// Close terminal cleanly -> daemon must reclaim registry slot.
		term.Close()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if relay.TerminalRegistryActiveForTest(running) == 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if got := relay.TerminalRegistryActiveForTest(running); got != 0 {
			t.Fatalf("stream %d did not reclaim active registry slot: active=%d", i, got)
		}
	}
}

// TestPhase3TerminalZeroLegIdleGC verifies M2:
// when a terminal stream loses all legs and no AttachPath arrives within the
// zero-leg TTL, the daemon closes tmux and reclaims the registry slot.
func TestPhase3TerminalZeroLegIdleGC(t *testing.T) {
	e := setup(t)
	const session = "pion-phase3-m2-gc"
	e.backend.add(t, session)
	termHandle := newPhase3FakeTerminal()
	backend := &phase3TermBackend{fakeBackend: e.backend, term: termHandle}

	var clockMu sync.Mutex
	fakeNow := time.Now()
	nowFn := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return fakeNow
	}
	running := e.startRelayBackend(backend, func(r *relay.Relay) {
		relay.SetDirectLoopbackForTest(r)
		relay.SetDirectClockForTest(r, nowFn)
		relay.SetTerminalClockForTest(r, 150*time.Millisecond, nowFn)
	})
	e.online(t, true)
	s := e.session(t, session, 0)

	term, err := e.client.OpenTerminal(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := term.Read(t.Context()); err != nil {
		t.Fatal(err)
	}
	term.Close()

	// Advance deterministic clock beyond zeroLegTTL.
	clockMu.Lock()
	fakeNow = fakeNow.Add(500 * time.Millisecond)
	clockMu.Unlock()

	select {
	case <-termHandle.doneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("zero-leg terminal was not closed after TTL")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if relay.TerminalRegistryActiveForTest(running) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := relay.TerminalRegistryActiveForTest(running); got != 0 {
		t.Fatalf("zero-leg GC did not reclaim registry slot: active=%d", got)
	}
}

// TestPhase3TerminalRelayQueueBackpressureNoDrop verifies M1:
// when tmux Input() stalls and >128 relay input frames arrive, the relay reader
// applies backpressure instead of silently dropping frames; once Input() unblocks,
// every single frame is injected in exact order with 0 dropped seqs.
func TestPhase3TerminalRelayQueueBackpressureNoDrop(t *testing.T) {
	blk := &blockingPhase3Terminal{
		phase3FakeTerminal: newPhase3FakeTerminal(),
		blockInput:         make(chan struct{}),
		inputHit:           make(chan struct{}),
	}

	e := setup(t)
	const session = "pion-phase3-m1-bp"
	e.backend.add(t, session)
	backend := &multiTermBackend{
		fakeBackend: e.backend,
		nextFn:      func() relay.Terminal { return blk },
	}
	_ = e.startRelayBackend(backend)
	e.online(t, true)
	s := e.session(t, session, 0)

	term, err := e.client.OpenTerminal(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if _, err := term.Read(t.Context()); err != nil {
		t.Fatal(err)
	}

	const totalFrames = 140 // > inboundQ capacity (128)
	writerErr := make(chan error, 1)
	go func() {
		for seq := 0; seq < totalFrames; seq++ {
			if err := term.Send(t.Context(), &pb.TerminalFrame{
				Kind: &pb.TerminalFrame_Input{Input: []byte{byte('a' + seq%26)}},
			}); err != nil {
				writerErr <- err
				return
			}
		}
		writerErr <- nil
	}()

	// Wait until frame 1 enters blocked Input(), then let inboundQ fill before unblocking.
	select {
	case <-blk.inputHit:
	case <-time.After(3 * time.Second):
		t.Fatal("first input frame never reached tmux Input()")
	}
	time.Sleep(150 * time.Millisecond)
	close(blk.blockInput)

	if err := <-writerErr; err != nil {
		t.Fatalf("writer failed: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(blk.allInputs()) >= totalFrames {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := blk.allInputs()
	if len(got) != totalFrames {
		t.Fatalf("relay backpressure dropped frames: got %d bytes, want %d", len(got), totalFrames)
	}
	for i := range totalFrames {
		want := byte('a' + i%26)
		if got[i] != want {
			t.Fatalf("frame %d out of order/corrupted: got %q want %q", i+1, got[i], want)
		}
	}
}

// TestPhase3TerminalDirectOnlyOutputReplayedOnDirectDrop verifies M4:
// when a Phase 3 client attaches a direct leg, a >4 KiB output is sent direct-only
// (not mirrored on relay); if direct drops before the client ACKs that output, the
// daemon replays the buffered output over the live relay WS so the screen never freezes.
func TestPhase3TerminalDirectOnlyOutputReplayedOnDirectDrop(t *testing.T) {
	h := startPhase3PionHarness(t)
	h.advertisePhase3ClientCapabilities()
	streamID := "d4000000000000000000000000000001"
	termWS := h.openTerminal(streamID)
	defer termWS.CloseNow()

	// Drain initial snapshot (seq=1) over relay.
	if _, raw, err := termWS.Read(h.ctx); err != nil {
		t.Fatal(err)
	} else if seq, _ := h.openTermOutput(raw, streamID); seq != 1 {
		t.Fatalf("expected seq=1 snapshot, got %d", seq)
	}

	// Attach direct leg ("d:...") and wait for daemon ack.
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          "d:00000000000001",
		PathGeneration: 1,
	})) {
		t.Fatal("AttachPath direct failed")
	}
	// Send one input over direct to ensure daemon processed AttachPath before emitting >4KiB output.
	if err := h.readPath.Write(h.ctx, h.sealTermFrame(streamID, 1, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("sync")}})); err != nil {
		t.Fatal(err)
	}
	h.waitInputs([]byte("sync"))

	// Emit 16 KiB output (> 4 KiB threshold -> sent on direct only, NOT mirrored on relay initially).
	large := bytes.Repeat([]byte("M"), 16*1024)
	h.backend.term.outCh <- large

	// Read it on direct (confirming daemon produced seq=2), then close the direct link WITHOUT ACKing seq=2!
	if raw, err := h.readPath.Read(h.ctx); err != nil {
		t.Fatal(err)
	} else if seq, tf := h.openTermOutput(raw, streamID); seq != 2 || !bytes.Equal(tf.GetOutput(), large) {
		t.Fatalf("expected direct seq=2 16KiB output, got seq=%d len=%d", seq, len(tf.GetOutput()))
	}

	// Simulate abrupt direct disconnect -> daemon's onDirectPeerPathClosed must replay unACKed seq=2 over live relay WS!
	h.link.Close()

	_, relayRaw, err := termWS.Read(h.ctx)
	if err != nil {
		t.Fatalf("relay WS did not receive replayed direct-only output after direct drop: %v", err)
	}
	seq, tf := h.openTermOutput(relayRaw, streamID)
	if seq != 2 || !bytes.Equal(tf.GetOutput(), large) {
		t.Fatalf("expected replayed seq=2 16KiB output over relay WS, got seq=%d len=%d", seq, len(tf.GetOutput()))
	}
}

// TestPhase3TerminalExpiredInputEntersConfirmationGateN6 verifies N6 + L1:
// an expired Input failing pre-callback is classified as ExpiredNotApplied with
// definitely_injected=0, possibly_injected=0, emits StreamFailed to enter user
// confirmation, and blocks subsequent Input from silently injecting into tmux.
// Meanwhile, an expired Resize (non-input) is classified as NonInputFailed and
// does NOT block subsequent Inputs.
func TestPhase3TerminalExpiredInputEntersConfirmationGateN6(t *testing.T) {
	h := startPhase3PionHarness(t)
	h.advertisePhase3ClientCapabilities()
	streamID := "e6000000000000000000000000000001"
	termWS := h.openTerminal(streamID)
	defer termWS.CloseNow()

	if _, _, err := termWS.Read(h.ctx); err != nil {
		t.Fatal(err)
	}
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          "d:00000000000006",
		PathGeneration: 1,
	})) {
		t.Fatal("direct AttachPath failed")
	}

	// 1) Expired Resize at seq=1 (non-input) -> NonInputFailed, does NOT block seq=2 Input.
	expResizeBody, _ := proto.Marshal(&pb.TerminalFrame{Kind: &pb.TerminalFrame_Resize{Resize: &pb.TerminalResize{Cols: 80, Rows: 24}}})
	expResizeWire, err := h.ep.Prepare(devicesec.Header{
		Purpose:  devicesec.TerminalResize,
		Kind:     8,
		Machine:  h.e.acct.DeviceID,
		Session:  h.session,
		Request:  streamID,
		Seq:      1,
		Deadline: uint64(time.Now().Add(-5 * time.Minute).UnixMilli()),
	}, h.sessKey, expResizeBody)
	if err != nil {
		t.Fatal(err)
	}
	expResizeEnv, _ := deviceauth.Envelope(expResizeWire)
	expResizeRaw, _ := proto.Marshal(expResizeEnv)
	if err := h.readPath.Write(h.ctx, expResizeRaw); err != nil {
		t.Fatal(err)
	}
	// Follow with valid Input at seq=2 -> MUST inject cleanly.
	if err := h.readPath.Write(h.ctx, h.sealTermFrame(streamID, 2, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("ok-after-resize")}})); err != nil {
		t.Fatal(err)
	}
	h.waitInputs([]byte("ok-after-resize"))

	// 2) Expired Input at seq=3 -> MUST emit StreamFailed (ExpiredNotApplied, 0/0 injected) and block seq=4!
	expInBody, _ := proto.Marshal(&pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("expired-in")}})
	expInWire, err := h.ep.Prepare(devicesec.Header{
		Purpose:  devicesec.TerminalInput,
		Kind:     8,
		Machine:  h.e.acct.DeviceID,
		Session:  h.session,
		Request:  streamID,
		Seq:      3,
		Deadline: uint64(time.Now().Add(-5 * time.Minute).UnixMilli()),
	}, h.sessKey, expInBody)
	if err != nil {
		t.Fatal(err)
	}
	expInEnv, _ := deviceauth.Envelope(expInWire)
	expInRaw, _ := proto.Marshal(expInEnv)
	if err := h.readPath.Write(h.ctx, expInRaw); err != nil {
		t.Fatal(err)
	}
	sfFrame := h.waitControl(linkcore.ControlStreamFailed)
	sf, err := linkcore.DecodeStreamFailed(sfFrame.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if sf.Stream != streamID || sf.InputFailure.Seq != 3 || sf.InputFailure.Outcome != linkcore.TermOutcomeExpiredNotApplied || sf.InputFailure.DefinitelyInjected != 0 || sf.InputFailure.PossiblyInjected != 0 {
		t.Fatalf("N6: unexpected StreamFailed for expired input: %+v", sf)
	}

	// 3) Subsequent Input at seq=4 MUST be blocked (NOT silently injected into tmux!).
	if err := h.readPath.Write(h.ctx, h.sealTermFrame(streamID, 4, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("MUST-NOT-RUN")}})); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if bytes.Contains(h.backend.term.allInputs(), []byte("MUST-NOT-RUN")) {
		t.Fatal("N6 violation: subsequent Input injected after expired Input without user confirmation")
	}
}

// TestPhase3TerminalAttachPathOutOfOrderAndBudgetDedupM8 verifies M7 and M8
// on a single real Pion Phase 3 harness:
//   - M7: out-of-order AttachPath (gen=3 before stale gen=2) rejects stale gen=2
//   - M8: budget-rejected out-of-order Input is NOT poisoned by CipherDedup when retried within 10s
func TestPhase3TerminalAttachPathOutOfOrderAndBudgetDedupM8(t *testing.T) {
	h := startPhase3PionHarness(t)
	h.advertisePhase3ClientCapabilities()
	streamID := "e7000000000000000000000000000001"
	termWS := h.openTerminal(streamID)
	defer termWS.CloseNow()

	// Drain initial snapshot (seq=1).
	if _, _, err := termWS.Read(h.ctx); err != nil {
		t.Fatal(err)
	}
	// Attach direct leg.
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          "d:00000000000007",
		PathGeneration: 1,
	})) {
		t.Fatal("direct AttachPath failed")
	}

	// Send valid seq=1 Resize and seq=2 Input to advance baseline.
	if err := h.readPath.Write(h.ctx, h.sealTermFrame(streamID, 1, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Resize{Resize: &pb.TerminalResize{Cols: 80, Rows: 24}}})); err != nil {
		t.Fatal(err)
	}
	in2 := h.sealTermFrame(streamID, 2, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("valid-base")}})
	if err := h.readPath.Write(h.ctx, in2); err != nil {
		t.Fatal(err)
	}
	h.waitInputs([]byte("valid-base"))

	// M8: Send out-of-order seq=135 (gap > 128 -> rejected by ErrTermBudget) FIRST, then
	// advance seq=3..134, then resend the EXACT same sealed bytes for seq=135 within 10s!
	// If CipherDedup poisoned seq=135 on budget rejection, seq=135 would be silently dropped.
	in135 := h.sealTermFrame(streamID, 135, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("M8-OK")}})
	if err := h.readPath.Write(h.ctx, in135); err != nil {
		t.Fatal(err)
	}
	// Give daemon a moment to receive & budget-reject seq=135.
	time.Sleep(50 * time.Millisecond)

	for seq := uint64(3); seq <= 134; seq++ {
		f := h.sealTermFrame(streamID, seq, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Resize{Resize: &pb.TerminalResize{Cols: 80, Rows: 24}}})
		if err := termWS.Write(h.ctx, websocket.MessageBinary, f); err != nil {
			t.Fatalf("send seq=%d: %v", seq, err)
		}
	}
	// Wait until daemon processes resize up to 134 (verify via seq=135 retry).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.backend.term.mu.Lock()
		rc := h.backend.term.resizes
		h.backend.term.mu.Unlock()
		if rc >= 133 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Resend EXACT SAME sealed bytes for seq=135!
	if err := h.readPath.Write(h.ctx, in135); err != nil {
		t.Fatal(err)
	}
	h.waitInputs([]byte("valid-baseM8-OK"))

	// M7: Send AttachPath gen=3 (pure 16-hex leg, L2) then stale AttachPath gen=2;
	// stale gen=2 must be rejected and gen=3 must remain active.
	leg3 := "3333333333333333"
	leg2 := "2222222222222222"
	ws3 := h.dialTermWS(leg3)
	defer ws3.CloseNow()
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          leg3,
		PathGeneration: 3,
	})) {
		t.Fatal("AttachPath gen=3 failed")
	}
	// Send stale gen=2 immediately after gen=3.
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          leg2,
		PathGeneration: 2,
	})) {
		t.Fatal("AttachPath gen=2 send failed")
	}
	// Confirm ws3 receives live output (proving gen=3 stayed active and was not replaced by stale gen=2).
	h.backend.term.outCh <- []byte("gen3-alive")
	for {
		_, raw, err := ws3.Read(h.ctx)
		if err != nil {
			t.Fatalf("ws3 (gen=3) did not stay active: %v", err)
		}
		_, tf := h.openTermOutput(raw, streamID)
		if bytes.Contains(tf.GetOutput(), []byte("gen3-alive")) {
			break
		}
	}
}
