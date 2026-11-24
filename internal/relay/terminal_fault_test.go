package relay_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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

	// Attach direct leg ("d:...") and wait deterministically until daemon attaches it.
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          "d:00000000000001",
		PathGeneration: 1,
	})) {
		t.Fatal("AttachPath direct failed")
	}
	h.waitCond("direct leg attached", func() bool {
		return relay.TerminalHasDirectLegForTest(h.running, streamID)
	})
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
	h.waitCond("n6 direct leg attached", func() bool {
		return relay.TerminalHasDirectLegForTest(h.running, streamID)
	})

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
	h.waitCond("m8 direct leg attached", func() bool {
		return relay.TerminalHasDirectLegForTest(h.running, streamID)
	})

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

	// M7 + N7: Send AttachPath gen=3 (16 raw bytes -> 32-char hex /v1/term StreamId) then stale AttachPath gen=2;
	// stale gen=2 must be rejected and gen=3 must remain active.
	leg3Raw := bytes.Repeat([]byte{0x33}, 16)
	leg2Raw := bytes.Repeat([]byte{0x22}, 16)
	ws3 := h.dialTermWS(fmt.Sprintf("%x", leg3Raw))
	defer ws3.CloseNow()
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          string(leg3Raw),
		PathGeneration: 3,
	})) {
		t.Fatal("AttachPath gen=3 failed")
	}
	// Send stale gen=2 immediately after gen=3.
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          string(leg2Raw),
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

// TestReview2Repro_HB_EarlyDirectAttachBufferedAndUnknownNonDirectRejected reproduces review2 H-B:
//  1. Direct "d:" AttachPath arriving BEFORE openTerminal is buffered and consumed by openTerminal,
//     so 16 KiB output flows over the direct hot-standby leg.
//  2. Non-direct AttachPath for an unknown stream receives explicit Control 8 (Unsupported).
func TestReview2Repro_HB_EarlyDirectAttachBufferedAndUnknownNonDirectRejected(t *testing.T) {
	h := startPhase3PionHarness(t)
	h.advertisePhase3ClientCapabilities()
	streamID := "hb112233445566778899aabbccddeeff"

	// Send direct AttachPath BEFORE openTerminal creates the stream (exact review2 H-B repro).
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          "d:earlyattach001",
		PathGeneration: 1,
	})) {
		t.Fatal("failed to queue early direct AttachPath")
	}
	h.waitCond("pendingDirect stored", func() bool {
		return relay.TerminalHasPendingDirectForTest(h.running, streamID)
	})

	termWS := h.openTerminal(streamID)
	defer termWS.CloseNow()

	// Emit 16 KiB payload and assert it arrives on direct leg (h.readPath).
	want := bytes.Repeat([]byte("HB16K"), (16<<10)/5+1)[:16<<10]
	h.backend.term.outCh <- want
	var directBuf []byte
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for !bytes.Contains(directBuf, want) {
			raw, err := h.readPath.Read(h.ctx)
			if err != nil {
				return
			}
			_, tf := h.openTermOutput(raw, streamID)
			if out := tf.GetOutput(); len(out) > 0 {
				directBuf = append(directBuf, out...)
			}
		}
	}()
	select {
	case <-readDone:
		if !bytes.Contains(directBuf, want) {
			t.Fatalf("H-B: 16 KiB output did not arrive on direct leg after early AttachPath")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("H-B: timed out waiting for 16 KiB output on direct leg (got %d bytes)", len(directBuf))
	}

	// Unknown non-direct AttachPath is explicitly rejected via ControlUnsupported (type 10),
	// which linkcore.Session settles cleanly without creating/leaking a stream; verify by
	// querying InputStatus on the unknown stream -> Available=false.
	unknownStream := "deadbeefdeadbeefdeadbeefdeadbeef"
	unknownLeg := string(bytes.Repeat([]byte{0xab}, 16))
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         unknownStream,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          unknownLeg,
		PathGeneration: 2,
	})) {
		t.Fatal("failed to queue unknown non-direct AttachPath")
	}
	qid := bytes.Repeat([]byte{0x71}, 16)
	if !h.link.SendControl(linkcore.ControlInputStatus, linkcore.EncodeInputStatus(linkcore.InputStatusMsg{
		Stream:  unknownStream,
		QueryID: qid,
		FromSeq: 1,
		Limit:   8,
	})) {
		t.Fatal("failed to queue InputStatus for unknown stream")
	}
	repCtrl := h.waitControl(linkcore.ControlInputStatusReply)
	rep, err := linkcore.DecodeInputStatusReply(repCtrl.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Available {
		t.Fatalf("H-B: unknown stream must remain unavailable after rejected AttachPath, got %+v", rep)
	}
}

// TestReview2Repro_N3_DirectReattachSameGenAfterClose reproduces review2 N3:
// Closing direct leg with gen=1 resets daemon directGen, so re-attaching a new direct leg
// with gen=1 (e.g. after direct session rebuild) succeeds and delivers 16 KiB output on direct.
func TestReview2Repro_N3_DirectReattachSameGenAfterClose(t *testing.T) {
	h := startPhase3PionHarness(t)
	h.advertisePhase3ClientCapabilities()
	streamID := "n3112233445566778899aabbccddeeff"
	termWS := h.openTerminal(streamID)
	defer termWS.CloseNow()
	if _, _, err := termWS.Read(h.ctx); err != nil {
		t.Fatal(err)
	}

	// Attach direct leg #1 with gen=1.
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          "d:reattachleg001",
		PathGeneration: 1,
	})) {
		t.Fatal("AttachPath #1 failed")
	}
	h.waitCond("direct leg #1 attached", func() bool {
		return relay.TerminalDirectLegIDForTest(h.running, streamID) == "d:reattachleg001"
	})

	// Close direct leg #1 (Reason=0, path loss).
	if !h.link.SendControl(linkcore.ControlClosePath, linkcore.EncodeClosePath(linkcore.ClosePathMsg{
		Stream:         streamID,
		LegID:          "d:reattachleg001",
		PathGeneration: 1,
		Reason:         0,
	})) {
		t.Fatal("ClosePath #1 failed")
	}
	h.waitCond("direct leg #1 closed", func() bool {
		return !relay.TerminalHasDirectLegForTest(h.running, streamID)
	})

	// Re-attach direct leg #2 STILL with PathGeneration=1 (exact review2 N3 repro).
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          "d:reattachleg002",
		PathGeneration: 1,
	})) {
		t.Fatal("AttachPath #2 (gen=1) failed")
	}
	h.waitCond("direct leg #2 attached", func() bool {
		return relay.TerminalDirectLegIDForTest(h.running, streamID) == "d:reattachleg002"
	})

	want := bytes.Repeat([]byte("N316K"), (16<<10)/5+1)[:16<<10]
	h.backend.term.outCh <- want
	var directBuf []byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !bytes.Contains(directBuf, want) {
			raw, err := h.readPath.Read(h.ctx)
			if err != nil {
				return
			}
			_, tf := h.openTermOutput(raw, streamID)
			if out := tf.GetOutput(); len(out) > 0 {
				directBuf = append(directBuf, out...)
			}
		}
	}()
	select {
	case <-done:
		if !bytes.Contains(directBuf, want) {
			t.Fatal("N3: 16 KiB output did not arrive over reattached gen=1 direct leg")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("N3: timed out waiting for output on reattached gen=1 direct leg (got %d bytes)", len(directBuf))
	}
}

// TestReview2Repro_N4_DirectCloseBeforeRelayKeepsZeroLegWindow reproduces review2 N4:
// Closing direct leg FIRST (Reason=0) and then closing relay WS SECOND on a Phase 3 stream
// must keep tmux alive in the zero-leg TTL window (InputStatus returns Available=true).
func TestReview2Repro_N4_DirectCloseBeforeRelayKeepsZeroLegWindow(t *testing.T) {
	h := startPhase3PionHarness(t)
	h.advertisePhase3ClientCapabilities()
	streamID := "n4112233445566778899aabbccddeeff"
	termWS := h.openTerminal(streamID)
	if _, _, err := termWS.Read(h.ctx); err != nil {
		t.Fatal(err)
	}

	// Attach direct leg first so stream is Phase 3.
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          "d:n4directleg001",
		PathGeneration: 1,
	})) {
		t.Fatal("AttachPath failed")
	}
	h.waitCond("n4 direct leg attached", func() bool {
		return relay.TerminalHasDirectLegForTest(h.running, streamID)
	})

	// Step 1: Close direct leg FIRST (Reason=0, network drop).
	if !h.link.SendControl(linkcore.ControlClosePath, linkcore.EncodeClosePath(linkcore.ClosePathMsg{
		Stream:         streamID,
		LegID:          "d:n4directleg001",
		PathGeneration: 1,
		Reason:         0,
	})) {
		t.Fatal("ClosePath failed")
	}
	h.waitCond("n4 direct leg closed", func() bool {
		return !relay.TerminalHasDirectLegForTest(h.running, streamID)
	})

	// Step 2: Close relay WS SECOND -> zero legs active, MUST stay alive in TTL window!
	termWS.CloseNow()
	h.waitCond("relay leg cleared while stream stays alive", func() bool {
		return !relay.TerminalHasRelayLegForTest(h.running, streamID)
	})
	select {
	case <-h.backend.term.Done():
		t.Fatal("N4 regression: daemon canceled tmux immediately when direct closed before relay")
	default:
	}

	// Query Control 3 InputStatus -> daemon must report Available=true!
	qid := bytes.Repeat([]byte{0x44}, 16)
	if !h.link.SendControl(linkcore.ControlInputStatus, linkcore.EncodeInputStatus(linkcore.InputStatusMsg{
		Stream:  streamID,
		QueryID: qid,
		FromSeq: 1,
		Limit:   32,
	})) {
		t.Fatal("InputStatus send failed")
	}
	repCtrl := h.waitControl(linkcore.ControlInputStatusReply)
	rep, err := linkcore.DecodeInputStatusReply(repCtrl.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Available {
		t.Fatalf("N4: expected InputStatusReply Available=true during zero-leg window, got %+v", rep)
	}
}

// TestReview2_N8_ReplayOverflowExplicitlyFailsWithHint verifies N8:
// When daemon's 128-frame output replay buffer overflows without client output ACK,
// triggering replay on direct failover explicitly fails/closes the stream with a user hint
// rather than silently truncating output.
func TestReview2_N8_ReplayOverflowExplicitlyFailsWithHint(t *testing.T) {
	h := startPhase3PionHarness(t)
	h.advertisePhase3ClientCapabilities()
	streamID := "n8112233445566778899aabbccddeeff"
	termWS := h.openTerminal(streamID)
	defer termWS.CloseNow()
	if _, _, err := termWS.Read(h.ctx); err != nil {
		t.Fatal(err)
	}

	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          "d:n8directleg001",
		PathGeneration: 1,
	})) {
		t.Fatal("AttachPath failed")
	}
	h.waitCond("n8 direct leg attached", func() bool {
		return relay.TerminalHasDirectLegForTest(h.running, streamID)
	})

	// Emit >2 MiB of incompressible random output (exceeding TermOutputReplayMaxBytes = 2 MiB) without ACKing output.
	const chunkSize = 32 << 10
	const totalChunks = 76 // ~2.43 MiB incompressible ciphertext > 2 MiB replay buffer cap
	chunks := make([][]byte, totalChunks)
	for i := range chunks {
		b := make([]byte, chunkSize)
		_, _ = rand.Read(b)
		chunks[i] = b
	}
	reached := make(chan struct{})
	go func() {
		var totalBytes int
		for totalBytes < (2<<20)+(128<<10) {
			raw, err := h.readPath.Read(h.ctx)
			if err != nil {
				return
			}
			_, tf := h.openTermOutput(raw, streamID)
			totalBytes += len(tf.GetOutput())
		}
		close(reached)
	}()
	go func() {
		for _, chunk := range chunks {
			select {
			case h.backend.term.outCh <- chunk:
			case <-h.ctx.Done():
				return
			}
		}
	}()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out producing >1 MiB output frames")
	}

	// Request replay from seq=2 (already evicted because >1 MiB output was produced) -> replay overflow!
	if !h.link.SendControl(linkcore.ControlReplay, linkcore.EncodeReplay(linkcore.ReplayMsg{
		Stream:     streamID,
		Direction:  linkcore.TermDirToClient,
		FromSeq:    2,
		ThroughSeq: 0,
		MaxBytes:   1 << 20,
		MaxFrames:  128,
	})) {
		t.Fatal("ControlReplay send failed")
	}

	// Daemon must explicitly signal ControlClosePath(Reason=2) and close the stream promptly.
	cpCtrl := h.waitControl(linkcore.ControlClosePath)
	cp, err := linkcore.DecodeClosePath(cpCtrl.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Stream != streamID || cp.Reason != 2 {
		t.Fatalf("N8: expected ControlClosePath(Reason=2), got %+v", cp)
	}
	select {
	case <-h.backend.term.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("N8: expected daemon to explicitly close stream on output replay overflow")
	}
}

// TestReview2_N9_LegacyPeerDoesNotReceivePhase3Control verifies N9:
// When a peer has NOT negotiated Phase 3 terminal direct capabilities, the daemon does not
// emit Phase 3 Control 4 stream ACKs over the direct control channel.
func TestReview2_N9_LegacyPeerDoesNotReceivePhase3Control(t *testing.T) {
	h := startPhase3PionHarness(t)
	// Intentionally DO NOT call h.advertisePhase3ClientCapabilities().
	streamID := "n9112233445566778899aabbccddeeff"
	termWS := h.openTerminal(streamID)
	defer termWS.CloseNow()
	if _, _, err := termWS.Read(h.ctx); err != nil {
		t.Fatal(err)
	}

	raw := h.sealTermFrame(streamID, 1, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("ls\n")}})
	if err := termWS.Write(h.ctx, websocket.MessageBinary, raw); err != nil {
		t.Fatal(err)
	}
	h.waitInputs([]byte("ls\n"))

	// Ensure no Phase 3 ControlStreamAck (4) is emitted on the direct control channel.
	deadline := time.After(250 * time.Millisecond)
	for {
		select {
		case o := <-h.controls:
			if o.Type == linkcore.ControlAck {
				if _, isStreamAck, err := linkcore.DecodeTermAck(o.Payload); err == nil && isStreamAck {
					t.Fatalf("N9: legacy peer must not receive Phase 3 stream ControlAck, got %+v", o)
				}
			}
		case <-deadline:
			return
		}
	}
}

// TestReview3Repro_A_ZeroLegOutputSurvivesAndRetiredInputStatusUnavailable reproduces review3 A:
//  1. In a Phase 3 stream's zero-leg window (direct closed, relay closed), terminal output MUST NOT
//     close the stream; output is buffered for replay on reconnect and InputStatus returns Available=true.
//  2. Once the stream is retired/stopped, InputStatus MUST return Available=false so the client
//     enters confirmation/rebuild instead of Live with silently dropped inputs.
func TestReview3Repro_A_ZeroLegOutputSurvivesAndRetiredInputStatusUnavailable(t *testing.T) {
	var nowNano atomic.Int64
	nowNano.Store(time.Now().UnixNano())
	clock := func() time.Time { return time.Unix(0, nowNano.Load()) }
	h := startPhase3PionHarnessWithOpt(t, func(r *relay.Relay) {
		relay.SetTerminalClockForTest(r, 30*time.Second, clock)
	})
	h.advertisePhase3ClientCapabilities()
	streamID := "a3112233445566778899aabbccddeeff"
	termWS := h.openTerminal(streamID)
	if _, _, err := termWS.Read(h.ctx); err != nil {
		t.Fatal(err)
	}

	// Attach direct leg -> stream becomes phase3Eligible.
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          "d:rev3a000000001",
		PathGeneration: 1,
	})) {
		t.Fatal("AttachPath failed")
	}
	h.waitCond("rev3a direct leg attached", func() bool {
		return relay.TerminalHasDirectLegForTest(h.running, streamID)
	})

	// Drop direct leg first, then close relay WS -> zero-leg window!
	if !h.link.SendControl(linkcore.ControlClosePath, linkcore.EncodeClosePath(linkcore.ClosePathMsg{
		Stream:         streamID,
		LegID:          "d:rev3a000000001",
		PathGeneration: 1,
		Reason:         0,
	})) {
		t.Fatal("ClosePath failed")
	}
	h.waitCond("rev3a direct leg closed", func() bool {
		return !relay.TerminalHasDirectLegForTest(h.running, streamID)
	})
	termWS.CloseNow()
	h.waitCond("relay leg cleared", func() bool {
		return !relay.TerminalHasRelayLegForTest(h.running, streamID)
	})

	// Emit terminal output WHILE BOTH LEGS ARE DOWN -> stream MUST stay alive!
	beforeSeq := relay.TerminalOutSeqForTest(h.running, streamID)
	h.backend.term.outCh <- []byte("output-during-zero-leg\n")
	h.waitCond("zero-leg output recorded", func() bool {
		return relay.TerminalOutSeqForTest(h.running, streamID) > beforeSeq
	})
	select {
	case <-h.backend.term.Done():
		t.Fatal("A regression: zero-leg terminal output closed the stream")
	default:
	}

	// InputStatus while live in zero-leg window returns Available=true.
	if !h.link.SendControl(linkcore.ControlInputStatus, linkcore.EncodeInputStatus(linkcore.InputStatusMsg{
		Stream:  streamID,
		QueryID: bytes.Repeat([]byte{0xa1}, 16),
		FromSeq: 1,
		Limit:   16,
	})) {
		t.Fatal("InputStatus failed")
	}
	repLive, err := linkcore.DecodeInputStatusReply(h.waitControl(linkcore.ControlInputStatusReply).Payload)
	if err != nil || !repLive.Available {
		t.Fatalf("expected Available=true while live in zero-leg window, got %+v err=%v", repLive, err)
	}

	// Reattach replacement relay WS via AttachPath -> zero-leg output is replayed and input reaches tmux!
	recRaw := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xfe, 0x01}
	recWS := h.dialTermWS(hex.EncodeToString(recRaw))
	defer recWS.CloseNow()
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          string(recRaw),
		PathGeneration: 2,
		State:          linkcore.TermFlowState{ToClient: linkcore.TermWatermark{Processed: beforeSeq}},
	})) {
		t.Fatal("relay AttachPath failed")
	}
	h.waitCond("replacement relay leg attached", func() bool {
		return relay.TerminalRelayAttachGenForTest(h.running, streamID) == 2
	})
	for {
		_, raw, err := recWS.Read(h.ctx)
		if err != nil {
			t.Fatalf("failed reading replayed zero-leg output: %v", err)
		}
		_, tf := h.openTermOutput(raw, streamID)
		if bytes.Contains(tf.GetOutput(), []byte("output-during-zero-leg")) {
			break
		}
	}
	in1 := h.sealTermFrame(streamID, 1, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("after-zero-leg\n")}})
	if err := recWS.Write(h.ctx, websocket.MessageBinary, in1); err != nil {
		t.Fatal(err)
	}
	h.waitInputs([]byte("after-zero-leg\n"))

	// Part 2: Close relay leg and expire zero-leg TTL -> stream retires; InputStatus MUST return Available=false!
	recWS.CloseNow()
	h.waitCond("replacement relay leg cleared", func() bool {
		return !relay.TerminalHasRelayLegForTest(h.running, streamID)
	})
	nowNano.Add(int64(35 * time.Second))
	select {
	case <-h.backend.term.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("expected stream to retire after zero-leg TTL elapsed")
	}
	h.waitCond("retired stream removed from active registry", func() bool {
		return relay.TerminalRegistryActiveForTest(h.running) == 0
	})
	if !h.link.SendControl(linkcore.ControlInputStatus, linkcore.EncodeInputStatus(linkcore.InputStatusMsg{
		Stream:  streamID,
		QueryID: bytes.Repeat([]byte{0xa2}, 16),
		FromSeq: 1,
		Limit:   16,
	})) {
		t.Fatal("retired InputStatus failed")
	}
	repRetired, err := linkcore.DecodeInputStatusReply(h.waitControl(linkcore.ControlInputStatusReply).Payload)
	if err != nil {
		t.Fatal(err)
	}
	if repRetired.Available {
		t.Fatalf("A regression: retired stream InputStatus MUST return Available=false, got %+v", repRetired)
	}
}

// TestReview3Repro_B_LegacyClientUnaffectedOnDirectSessionEnd reproduces review3 B:
// Legacy clients (that never negotiated Phase 3 capabilities) must NEVER receive duplicate
// replayed frames or overflow closes when their peer's direct session ends, regardless of
// frame count (3 or 200) or stream age (45s > 30s replay window).
func TestReview3Repro_B_LegacyClientUnaffectedOnDirectSessionEnd(t *testing.T) {
	t.Run("three_frames_no_duplicates", func(t *testing.T) {
		h := startPhase3PionHarness(t)
		// Legacy client: DO NOT advertise Phase 3 capabilities.
		streamID := "b3012233445566778899aabbccddeeff"
		termWS := h.openTerminal(streamID)
		defer termWS.CloseNow()
		seq, _ := h.openTermOutput(mustReadWS(t, h.ctx, termWS), streamID)
		if seq != 1 {
			t.Fatalf("expected snapshot seq=1, got %d", seq)
		}
		lastSeq := seq
		for i := 1; i <= 3; i++ {
			msg := fmt.Sprintf("legacy-line-%d\n", i)
			h.backend.term.outCh <- []byte(msg)
			s, tf := h.openTermOutput(mustReadWS(t, h.ctx, termWS), streamID)
			if s <= lastSeq || !bytes.Contains(tf.GetOutput(), []byte(msg)) {
				t.Fatalf("unexpected frame seq=%d last=%d", s, lastSeq)
			}
			lastSeq = s
		}
		// End the direct session cleanly (triggers onDirectPeerPathClosed on daemon).
		h.link.Close()
		h.link.Wait()

		// Emit a new line -> next frame on relay WS MUST be lastSeq+1 with ZERO duplicate frames!
		h.backend.term.outCh <- []byte("after-direct-end\n")
		nextSeq, nextTF := h.openTermOutput(mustReadWS(t, h.ctx, termWS), streamID)
		if nextSeq <= lastSeq {
			t.Fatalf("B regression: legacy client received duplicate seq=%d (last=%d)", nextSeq, lastSeq)
		}
		if nextTF.GetClosed() != "" || !bytes.Contains(nextTF.GetOutput(), []byte("after-direct-end")) {
			t.Fatalf("B regression: expected live output after direct end, got %+v", nextTF)
		}
	})

	t.Run("200_frames_no_overflow_close", func(t *testing.T) {
		h := startPhase3PionHarness(t)
		streamID := "b3022233445566778899aabbccddeeff"
		termWS := h.openTerminal(streamID)
		defer termWS.CloseNow()
		lastSeq, _ := h.openTermOutput(mustReadWS(t, h.ctx, termWS), streamID)
		for i := 0; i < 200; i++ {
			h.backend.term.outCh <- []byte(fmt.Sprintf("l-%03d\n", i))
			s, tf := h.openTermOutput(mustReadWS(t, h.ctx, termWS), streamID)
			if s <= lastSeq || tf.GetClosed() != "" {
				t.Fatalf("unexpected seq=%d closed=%q", s, tf.GetClosed())
			}
			lastSeq = s
		}
		h.link.Close()
		h.link.Wait()

		h.backend.term.outCh <- []byte("alive-after-200\n")
		nextSeq, nextTF := h.openTermOutput(mustReadWS(t, h.ctx, termWS), streamID)
		if nextSeq <= lastSeq || nextTF.GetClosed() != "" || !bytes.Contains(nextTF.GetOutput(), []byte("alive-after-200")) {
			t.Fatalf("B regression: legacy 200-frame stream affected on direct end: seq=%d tf=%+v", nextSeq, nextTF)
		}
	})

	t.Run("45s_stream_age_no_close", func(t *testing.T) {
		var nowNano atomic.Int64
		nowNano.Store(time.Now().UnixNano())
		clock := func() time.Time { return time.Unix(0, nowNano.Load()) }
		h := startPhase3PionHarnessWithOpt(t, func(r *relay.Relay) {
			relay.SetTerminalClockForTest(r, 30*time.Second, clock)
		})
		streamID := "b3032233445566778899aabbccddeeff"
		termWS := h.openTerminal(streamID)
		defer termWS.CloseNow()
		lastSeq, _ := h.openTermOutput(mustReadWS(t, h.ctx, termWS), streamID)

		// Advance terminal clock 45s (beyond 30s replay window) and close direct session.
		nowNano.Add(int64(45 * time.Second))
		h.link.Close()
		h.link.Wait()

		h.backend.term.outCh <- []byte("alive-after-45s\n")
		nextSeq, nextTF := h.openTermOutput(mustReadWS(t, h.ctx, termWS), streamID)
		if nextSeq <= lastSeq || nextTF.GetClosed() != "" || !bytes.Contains(nextTF.GetOutput(), []byte("alive-after-45s")) {
			t.Fatalf("B regression: 45s-old legacy stream closed on direct end: seq=%d tf=%+v", nextSeq, nextTF)
		}
	})
}

func mustReadWS(t *testing.T, ctx context.Context, ws *websocket.Conn) []byte {
	t.Helper()
	_, raw, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("ws.Read failed: %v", err)
	}
	return raw
}

// TestReview3Repro_C_PureRelayFailStopNotifiesClientAndCloses reproduces review3 C:
// On a pure-relay stream (no Phase 3 direct control channel), an expired Input triggers
// fail-stop and daemon MUST send TerminalFrame_Closed (prefixed with TermRelayFailStopTag)
// over relay WS and cancel the stream instead of silently freezing subsequent typing.
func TestReview3Repro_C_PureRelayFailStopNotifiesClientAndCloses(t *testing.T) {
	h := startPhase3PionHarness(t)
	// Legacy / pure-relay client: DO NOT advertise Phase 3 capabilities or attach direct leg.
	streamID := "c3012233445566778899aabbccddeeff"
	termWS := h.openTerminal(streamID)
	defer termWS.CloseNow()
	if _, _, err := termWS.Read(h.ctx); err != nil {
		t.Fatal(err)
	}

	// Send an expired Input at seq=1 over relay WS.
	inPlain, _ := proto.Marshal(&pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("expired-pure-relay\n")}})
	hdr := devicesec.Header{
		Kind:     uint32(pb.PayloadKind_PAYLOAD_KIND_TERMINAL_INPUT),
		Purpose:  devicesec.TerminalInput,
		Machine:  h.e.acct.DeviceID,
		Session:  h.session,
		Request:  streamID,
		Seq:      1,
		Deadline: uint64(time.Now().Add(-time.Minute).UnixMilli()),
	}
	sealed, err := h.ep.Prepare(hdr, h.sessKey, inPlain)
	if err != nil {
		t.Fatal(err)
	}
	env, err := deviceauth.Envelope(sealed)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := proto.Marshal(env)
	if err := termWS.Write(h.ctx, websocket.MessageBinary, raw); err != nil {
		t.Fatal(err)
	}

	// Client MUST receive TerminalFrame_Closed with TermRelayFailStopTag and stream must close!
	_, closedTF := h.openTermOutput(mustReadWS(t, h.ctx, termWS), streamID)
	if !strings.HasPrefix(closedTF.GetClosed(), linkcore.TermRelayFailStopTag) {
		t.Fatalf("C regression: expected TerminalFrame_Closed prefixed with %q, got %+v", linkcore.TermRelayFailStopTag, closedTF)
	}
	select {
	case <-h.backend.term.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("C regression: expected daemon to cancel stream after pure-relay fail-stop")
	}
	if len(h.backend.term.allInputs()) != 0 {
		t.Fatalf("expired input must not inject into tmux, got %q", h.backend.term.allInputs())
	}
}

// TestReview3Repro_D_PaginatedReplayAbove128FramesSucceeds reproduces review3 D:
// When a Phase 3 stream has 200 output frames (within the 512-frame / 2 MiB replay window),
// paginated ControlReplay(FromSeq=2, MaxFrames=128) MUST return page 1 cleanly without
// falsely aborting as overflow, and page 2 (FromSeq=130) delivers the remaining frames.
func TestReview3Repro_D_PaginatedReplayAbove128FramesSucceeds(t *testing.T) {
	h := startPhase3PionHarness(t)
	h.advertisePhase3ClientCapabilities()
	streamID := "d3012233445566778899aabbccddeeff"
	termWS := h.openTerminal(streamID)
	defer termWS.CloseNow()
	if _, _, err := termWS.Read(h.ctx); err != nil {
		t.Fatal(err)
	}

	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          "d:rev3d000000001",
		PathGeneration: 1,
	})) {
		t.Fatal("AttachPath failed")
	}
	h.waitCond("rev3d direct leg attached", func() bool {
		return relay.TerminalHasDirectLegForTest(h.running, streamID)
	})

	// Produce 200 distinct frames on direct leg (wait per frame so daemon doesn't coalesce).
	for i := 0; i < 200; i++ {
		h.backend.term.outCh <- []byte(fmt.Sprintf("pg-%03d\n", i))
		raw, err := h.readPath.Read(h.ctx)
		if err != nil {
			t.Fatalf("direct read failed at %d: %v", i, err)
		}
		h.openTermOutput(raw, streamID)
	}
	targetSeq := relay.TerminalOutSeqForTest(h.running, streamID)
	if targetSeq < 201 {
		t.Fatalf("expected outSeq >= 201, got %d", targetSeq)
	}

	// Page 1: Replay(FromSeq=2, MaxFrames=128) -> MUST NOT trigger overflow close!
	if !h.link.SendControl(linkcore.ControlReplay, linkcore.EncodeReplay(linkcore.ReplayMsg{
		Stream:     streamID,
		Direction:  linkcore.TermDirToClient,
		FromSeq:    2,
		ThroughSeq: 0,
		MaxBytes:   1 << 20,
		MaxFrames:  128,
	})) {
		t.Fatal("ControlReplay page 1 failed")
	}
	var page1Max uint64
	for i := 0; i < 128; i++ {
		raw, err := h.readPath.Read(h.ctx)
		if err != nil {
			t.Fatalf("failed reading page 1 replay frame %d: %v", i, err)
		}
		s, tf := h.openTermOutput(raw, streamID)
		if tf.GetClosed() != "" {
			t.Fatalf("D regression: paginated Replay page 1 aborted stream: %q", tf.GetClosed())
		}
		page1Max = s
	}
	if page1Max != 129 {
		t.Fatalf("expected page 1 to reach seq=129, got %d", page1Max)
	}

	// Page 2: Replay(FromSeq=130, MaxFrames=128) -> delivers remaining [130..targetSeq]!
	if !h.link.SendControl(linkcore.ControlReplay, linkcore.EncodeReplay(linkcore.ReplayMsg{
		Stream:     streamID,
		Direction:  linkcore.TermDirToClient,
		FromSeq:    130,
		ThroughSeq: 0,
		MaxBytes:   1 << 20,
		MaxFrames:  128,
	})) {
		t.Fatal("ControlReplay page 2 failed")
	}
	remaining := int(targetSeq - 129)
	var page2Max uint64
	for i := 0; i < remaining; i++ {
		raw, err := h.readPath.Read(h.ctx)
		if err != nil {
			t.Fatalf("failed reading page 2 replay frame %d: %v", i, err)
		}
		s, tf := h.openTermOutput(raw, streamID)
		if tf.GetClosed() != "" {
			t.Fatalf("D regression: paginated Replay page 2 aborted stream: %q", tf.GetClosed())
		}
		page2Max = s
	}
	if page2Max != targetSeq {
		t.Fatalf("expected page 2 to reach seq=%d, got %d", targetSeq, page2Max)
	}
	select {
	case <-h.backend.term.Done():
		t.Fatal("D regression: stream closed after normal paginated replay")
	default:
	}
}
