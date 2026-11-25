package transport

import (
	"bytes"
	"crypto/sha256"
	"testing"

	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
)

func TestCoordinatorPhase3TerminalContinuityAndFailover(t *testing.T) {
	coord, _, daemon, now, _, rid, readPC := driveEstablishedCoordRound(t, "127.0.0.1", "")
	r := coord.rounds[rid]
	if r == nil {
		t.Fatal("expected active round")
	}

	// Peer advertises Phase 3 terminal continuity controls in Capabilities (Control 1).
	capBody := peerwire.PB(
		peerwire.B(1, bytes.Repeat([]byte{0x11}, 16)),
		peerwire.U(2, 1), peerwire.U(2, 2), peerwire.U(2, 3), peerwire.U(2, 4),
		peerwire.U(2, 5), peerwire.U(2, 6), peerwire.U(2, 7), peerwire.U(2, 8),
		peerwire.U(2, 11), peerwire.U(2, 12), peerwire.U(2, 15),
		peerwire.U(3, 1), peerwire.U(3, 2),
		peerwire.U(4, uint64(now+60_000)),
		peerwire.U(5, 1),
		peerwire.U(6, 1<<20),
	)
	_, _ = coord.decodeControl(now, r, linkcore.HandleControl{
		Frame: peerwire.Opened{
			Context: peerwire.Context{Class: 2, Seq: 10, Issued: uint64(now), Deadline: uint64(now + 60_000)},
			Type:    1,
			Payload: capBody,
			Hash:    bytes.Repeat([]byte{0xaa}, 32),
		},
	})
	st := coord.Status(now, daemon.id)
	if st.Terminal != Direct {
		t.Fatalf("expected Status.Terminal == Direct after Phase 3 Capabilities, got %v", st.Terminal)
	}

	streamID := "0123456789abcdef0123456789abcdef"
	sessionID := "sess-term-phase3-01"
	openEffs := coord.TerminalDirectOpen(now, streamID, daemon.id, sessionID, []byte("relayleg00000001"))
	_ = coord.TerminalDirectLeg(now+1, streamID, []byte("relayleg00000001"), false, true, 0)
	var sawLive bool
	for _, e := range openEffs {
		if ts, ok := e.(CoordTerminalState); ok && ts.State == TermStateLive {
			sawLive = true
		}
	}
	if !sawLive {
		t.Fatalf("expected CoordTerminalState live on TerminalDirectOpen, got %+v", openEffs)
	}

	// Outbound input emits hot-standby dual-send (SendRelay=true AND DirectPCID=readPC).
	h1 := sha256.Sum256([]byte("ls\n"))
	inEffs := coord.TerminalDirectInput(now+5, streamID, 1, true, []byte("sealed-input-1"), h1[:])
	var sendEff *CoordTerminalSend
	for _, e := range inEffs {
		if se, ok := e.(CoordTerminalSend); ok {
			cp := se
			sendEff = &cp
		}
	}
	if sendEff == nil || !sendEff.SendRelay || sendEff.DirectPCID != readPC {
		t.Fatalf("expected dual-send on relay + direct pc=%d, got %+v", readPC, sendEff)
	}

	// Out-of-order terminal outputs (seq=2 before seq=1) reorder cleanly, and duplicate seq=1 does NOT close!
	o1 := []byte("out-1")
	o2 := []byte("out-2")
	oh1 := sha256.Sum256(o1)
	oh2 := sha256.Sum256(o2)
	if effs := coord.TerminalDirectOutput(now+10, streamID, 2, oh2[:], o2, true, readPC); len(effs) != 0 {
		t.Fatalf("expected out-of-order seq=2 to buffer without immediate delivery, got %+v", effs)
	}
	delivEffs := coord.TerminalDirectOutput(now+12, streamID, 1, oh1[:], o1, false, 0)
	var delivered [][]byte
	for _, e := range delivEffs {
		if de, ok := e.(CoordTerminalDeliver); ok {
			delivered = append(delivered, de.Payloads...)
		}
	}
	if len(delivered) != 2 || !bytes.Equal(delivered[0], o1) || !bytes.Equal(delivered[1], o2) {
		t.Fatalf("expected ordered delivery [out-1, out-2], got %q", delivered)
	}
	// Duplicate seq=1 with matching hash must NOT deliver again and must NOT close stream.
	dupEffs := coord.TerminalDirectOutput(now+13, streamID, 1, oh1[:], o1, true, readPC)
	for _, e := range dupEffs {
		if _, ok := e.(CoordTerminalDeliver); ok {
			t.Fatal("duplicate seq=1 must not re-deliver")
		}
		if st, ok := e.(CoordTerminalState); ok && st.State == TermStateClosed {
			t.Fatal("duplicate seq=1 with matching hash must not close stream")
		}
	}

	// Relay WS drop while direct leg is live -> emits CoordTerminalAttachRelay (does NOT close stream!).
	legEffs := coord.TerminalDirectLeg(now+20, streamID, []byte("relayleg00000001"), false, false, 0)
	var attachEff *CoordTerminalAttachRelay
	for _, e := range legEffs {
		if ae, ok := e.(CoordTerminalAttachRelay); ok {
			cp := ae
			attachEff = &cp
		}
		if st, ok := e.(CoordTerminalState); ok && st.State == TermStateClosed {
			t.Fatal("relay drop while direct leg is live must not close stream")
		}
	}
	if attachEff == nil || len(attachEff.LegID) != 32 {
		t.Fatalf("expected 32-char hex CoordTerminalAttachRelay leg (N7), got %+v", attachEff)
	}

	// StreamFailed (Control 7) triggers non-bypassable fail-stop with NeedsConfirmation=true.
	sfBody := linkcore.EncodeStreamFailed(linkcore.StreamFailedMsg{
		Stream: streamID,
		InputFailure: linkcore.TermFrameStatus{
			Seq:                1,
			PayloadHash:        h1,
			Outcome:            linkcore.TermOutcomeInputPartialUnknown,
			DefinitelyInjected: 1,
			PossiblyInjected:   3,
			InputBytes:         3,
		},
		State: linkcore.TermFlowState{
			ToDaemon: linkcore.TermWatermark{Processed: 1, InputSuccess: 0, FailedInputSeq: 1},
		},
	})
	_, _ = coord.decodeControl(now+30, r, linkcore.HandleControl{
		Frame: peerwire.Opened{
			Context: peerwire.Context{Class: 2, Seq: 2},
			Type:    linkcore.ControlStreamFailed,
			Payload: sfBody,
			Hash:    bytes.Repeat([]byte{0xbb}, 32),
		},
	})
	// Subsequent input is blocked until ConfirmRebuild!
	h2 := sha256.Sum256([]byte("rm -rf\n"))
	blockedEffs := coord.TerminalDirectInput(now+35, streamID, 2, true, []byte("sealed-input-2"), h2[:])
	var sawConfirmGate bool
	for _, e := range blockedEffs {
		if _, ok := e.(CoordTerminalSend); ok {
			t.Fatal("fail-stop must block subsequent Input send")
		}
		if st, ok := e.(CoordTerminalState); ok && st.NeedsConfirmation {
			sawConfirmGate = true
		}
	}
	if !sawConfirmGate {
		t.Fatalf("expected NeedsConfirmation state on blocked input, got %+v", blockedEffs)
	}

	// ConfirmRebuild clears confirmation gate and signals shell rebuild.
	confirmEffs := coord.TerminalDirectConfirmRebuild(now+40, streamID)
	sawRebuild := false
	for _, e := range confirmEffs {
		if st, ok := e.(CoordTerminalState); ok && st.State == TermStateClosed && st.Reason == "rebuild_confirmed" && !st.NeedsConfirmation {
			sawRebuild = true
		}
	}
	if !sawRebuild {
		t.Fatalf("expected ConfirmRebuild to emit TermStateClosed(rebuild_confirmed), got %+v", confirmEffs)
	}
}

// TestCoordinatorPureRelayAndLegacyDaemonInputNeverHitsCacheBudget verifies H1:
// pure-relay streams and legacy Phase 1 daemons (no Phase 3 capabilities) must never
// block or drop typing after >128 frames or >30s idle without direct ACKs.
func TestCoordinatorPureRelayAndLegacyDaemonInputNeverHitsCacheBudget(t *testing.T) {
	t.Run("pure_relay", func(t *testing.T) {
		coord := NewCoordinator(CoordConfig{Supported: false, ProtocolVersion: 17})
		now := int64(1_000_000)
		streamID := "pure0123456789abcdef0123456789ab"
		_ = coord.TerminalDirectOpen(now, streamID, "machine-pure", "sess-pure", []byte("relay00000000001"))

		for i := uint64(1); i <= 200; i++ {
			h := sha256.Sum256([]byte{byte(i)})
			effs := coord.TerminalDirectInput(now+int64(i)*10, streamID, i, true, []byte("env"), h[:])
			var sent bool
			for _, e := range effs {
				if se, ok := e.(CoordTerminalSend); ok && se.SendRelay && se.DirectPCID == 0 {
					sent = true
				}
				if st, ok := e.(CoordTerminalState); ok && st.State == TermStateClosed {
					t.Fatalf("unexpected close at seq=%d: %+v", i, st)
				}
			}
			if !sent {
				t.Fatalf("expected relay send at seq=%d, got %+v", i, effs)
			}
		}
		// Advance 31s (>30s direct cache pause threshold) and keep typing.
		h201 := sha256.Sum256([]byte("after-31s"))
		effs := coord.TerminalDirectInput(now+35_000, streamID, 201, true, []byte("env-201"), h201[:])
		var sent bool
		for _, e := range effs {
			if se, ok := e.(CoordTerminalSend); ok && se.SendRelay && se.DirectPCID == 0 {
				sent = true
			}
		}
		if !sent {
			t.Fatalf("expected relay send after 31s idle, got %+v", effs)
		}
		// Pure-relay close must emit TermStateClosed immediately (M5).
		closeEffs := coord.TerminalDirectLeg(now+36_000, streamID, []byte("relay00000000001"), false, false, 0)
		var sawClosed bool
		for _, e := range closeEffs {
			if st, ok := e.(CoordTerminalState); ok && st.State == TermStateClosed && st.Reason == "relay_closed" {
				sawClosed = true
			}
		}
		if !sawClosed {
			t.Fatalf("expected pure-relay drop to emit TermStateClosed, got %+v", closeEffs)
		}
	})

	t.Run("legacy_phase1_daemon", func(t *testing.T) {
		coord, _, daemon, now, _, _, _ := driveEstablishedCoordRound(t, "127.0.0.1", "")
		// Deliberately do NOT deliver Phase 3 Capabilities -> daemon is Phase 1 legacy.
		streamID := "lega0123456789abcdef0123456789ab"
		_ = coord.TerminalDirectOpen(now, streamID, daemon.id, "sess-legacy", []byte("relay00000000002"))

		for i := uint64(1); i <= 200; i++ {
			h := sha256.Sum256([]byte{byte(i)})
			effs := coord.TerminalDirectInput(now+int64(i)*10, streamID, i, true, []byte("env"), h[:])
			var sent bool
			for _, e := range effs {
				if se, ok := e.(CoordTerminalSend); ok && se.SendRelay && se.DirectPCID == 0 {
					sent = true
				}
			}
			if !sent {
				t.Fatalf("legacy daemon seq=%d must send over relay without direct cache stall, got %+v", i, effs)
			}
		}
		h201 := sha256.Sum256([]byte("legacy-31s"))
		effs := coord.TerminalDirectInput(now+35_000, streamID, 201, true, []byte("env-201"), h201[:])
		var sent bool
		for _, e := range effs {
			if se, ok := e.(CoordTerminalSend); ok && se.SendRelay && se.DirectPCID == 0 {
				sent = true
			}
		}
		if !sent {
			t.Fatalf("legacy daemon seq=201 after 31s must still send over relay, got %+v", effs)
		}
	})
}

// TestCoordinatorPhase3ResizeIdleDoesNotBlockInput verifies H2:
// Resize/History frames do not enter inputCache, so 31s idle after Resize never blocks Input.
func TestCoordinatorPhase3ResizeIdleDoesNotBlockInput(t *testing.T) {
	coord, _, daemon, now, _, rid, readPC := driveEstablishedCoordRound(t, "127.0.0.1", "")
	r := coord.rounds[rid]
	capBody := peerwire.PB(
		peerwire.B(1, bytes.Repeat([]byte{0x22}, 16)),
		peerwire.U(2, 1), peerwire.U(2, 2), peerwire.U(2, 3), peerwire.U(2, 4),
		peerwire.U(2, 5), peerwire.U(2, 6), peerwire.U(2, 7), peerwire.U(2, 8),
		peerwire.U(2, 11), peerwire.U(2, 12), peerwire.U(2, 15),
		peerwire.U(3, 1), peerwire.U(3, 2),
		peerwire.U(4, uint64(now+55_000)),
		peerwire.U(5, 1),
		peerwire.U(6, 1<<20),
	)
	_, _ = coord.decodeControl(now, r, linkcore.HandleControl{
		Frame: peerwire.Opened{
			Context: peerwire.Context{Class: 2, Seq: 10, Issued: uint64(now), Deadline: uint64(now + 55_000)},
			Type:    1,
			Payload: capBody,
			Hash:    bytes.Repeat([]byte{0xaa}, 32),
		},
	})
	streamID := "resz0123456789abcdef0123456789ab"
	_ = coord.TerminalDirectOpen(now, streamID, daemon.id, "sess-resz", []byte("relay00000000003"))
	_ = coord.TerminalDirectLeg(now+1, streamID, []byte("relay00000000003"), false, true, 0)

	rh := sha256.Sum256([]byte("resize-80x24"))
	_ = coord.TerminalDirectInput(now+5, streamID, 1, false, []byte("sealed-resize"), rh[:])

	// Refresh capabilities at +20s (normal direct keepalive), with ZERO ControlAck for Resize.
	capBody2 := peerwire.PB(
		peerwire.B(1, bytes.Repeat([]byte{0x22}, 16)),
		peerwire.U(2, 1), peerwire.U(2, 2), peerwire.U(2, 3), peerwire.U(2, 4),
		peerwire.U(2, 5), peerwire.U(2, 6), peerwire.U(2, 7), peerwire.U(2, 8),
		peerwire.U(2, 11), peerwire.U(2, 12), peerwire.U(2, 15),
		peerwire.U(3, 1), peerwire.U(3, 2),
		peerwire.U(4, uint64(now+75_000)),
		peerwire.U(5, 1),
		peerwire.U(6, 1<<20),
	)
	_, _ = coord.decodeControl(now+20_000, r, linkcore.HandleControl{
		Frame: peerwire.Opened{
			Context: peerwire.Context{Class: 2, Seq: 11, Issued: uint64(now + 20_000), Deadline: uint64(now + 75_000)},
			Type:    1,
			Payload: capBody2,
			Hash:    bytes.Repeat([]byte{0xab}, 32),
		},
	})

	// Extend security freshness so the direct round stays valid across +31.5s idle while NO ControlAck is sent.
	coord.engine.freshUntil = now + 120_000

	// Advance 31.5s without any ACK, then send real user Input -> MUST dual-send cleanly!
	ih := sha256.Sum256([]byte("echo hi\n"))
	effs := coord.TerminalDirectInput(now+31_500, streamID, 2, true, []byte("sealed-input"), ih[:])
	var sentDual bool
	for _, e := range effs {
		if se, ok := e.(CoordTerminalSend); ok && se.SendRelay && se.DirectPCID == readPC {
			sentDual = true
		}
	}
	if !sentDual {
		t.Fatalf("expected Input after 31s Resize idle to dual-send cleanly, got %+v", effs)
	}
}

// TestCoordinatorEarlyOutputBufferedBeforeOpen verifies M6:
// output frames arriving before TerminalDirectOpen are buffered and delivered in order on open.
func TestCoordinatorEarlyOutputBufferedBeforeOpen(t *testing.T) {
	coord := NewCoordinator(CoordConfig{Supported: false, ProtocolVersion: 17})
	now := int64(2_000_000)
	streamID := "earl0123456789abcdef0123456789ab"
	o1 := []byte("early-1")
	o2 := []byte("early-2")
	h1 := sha256.Sum256(o1)
	h2 := sha256.Sum256(o2)

	// Arrive out-of-order BEFORE TerminalDirectOpen.
	if effs := coord.TerminalDirectOutput(now, streamID, 2, h2[:], o2, true, 99); len(effs) != 0 {
		t.Fatalf("expected early seq=2 before open to buffer, got %+v", effs)
	}
	if effs := coord.TerminalDirectOutput(now+10, streamID, 1, h1[:], o1, false, 0); len(effs) != 0 {
		t.Fatalf("expected early seq=1 before open to buffer, got %+v", effs)
	}

	openEffs := coord.TerminalDirectOpen(now+20, streamID, "m1", "s1", []byte("relay00000000004"))
	var delivered [][]byte
	for _, e := range openEffs {
		if de, ok := e.(CoordTerminalDeliver); ok {
			delivered = append(delivered, de.Payloads...)
		}
	}
	if len(delivered) != 2 || !bytes.Equal(delivered[0], o1) || !bytes.Equal(delivered[1], o2) {
		t.Fatalf("expected buffered early outputs [early-1, early-2] delivered on open, got %q", delivered)
	}
}

// TestCoordinatorFullDisconnectRecoveryViaInputStatus verifies M4:
// when both legs disconnect on a Phase 3 stream, Core enters TermStateReconnecting,
// automatically queries Control 3 InputStatus when direct returns, and reconciles/resends
// unconfirmed inputs (or enters TermStateInstanceChange if daemon reports Available=false).
func TestCoordinatorFullDisconnectRecoveryViaInputStatus(t *testing.T) {
	coord, _, daemon, now, _, rid, readPC := driveEstablishedCoordRound(t, "127.0.0.1", "")
	r := coord.rounds[rid]
	capBody := peerwire.PB(
		peerwire.B(1, bytes.Repeat([]byte{0x33}, 16)),
		peerwire.U(2, 1), peerwire.U(2, 2), peerwire.U(2, 3), peerwire.U(2, 4),
		peerwire.U(2, 5), peerwire.U(2, 6), peerwire.U(2, 7), peerwire.U(2, 8),
		peerwire.U(2, 11), peerwire.U(2, 12), peerwire.U(2, 15),
		peerwire.U(3, 1), peerwire.U(3, 2),
		peerwire.U(4, uint64(now+55_000)),
		peerwire.U(5, 1),
		peerwire.U(6, 1<<20),
	)
	_, _ = coord.decodeControl(now, r, linkcore.HandleControl{
		Frame: peerwire.Opened{
			Context: peerwire.Context{Class: 2, Seq: 10, Issued: uint64(now), Deadline: uint64(now + 55_000)},
			Type:    1,
			Payload: capBody,
			Hash:    bytes.Repeat([]byte{0xaa}, 32),
		},
	})
	streamID := "m4rc0123456789abcdef0123456789ab"
	_ = coord.TerminalDirectOpen(now, streamID, daemon.id, "sess-m4", []byte("relay00000000005"))
	_ = coord.TerminalDirectLeg(now+1, streamID, []byte("relay00000000005"), false, true, 0)

	// Send unconfirmed Input seq=1.
	h1 := sha256.Sum256([]byte("pwd\n"))
	_ = coord.TerminalDirectInput(now+10, streamID, 1, true, []byte("sealed-pwd"), h1[:])

	// Disconnect direct leg first (stream stays live on relay), then disconnect relay -> both legs down!
	_ = coord.TerminalDirectLeg(now+20, streamID, nil, true, false, readPC)
	dropEffs := coord.TerminalDirectLeg(now+25, streamID, []byte("relay00000000005"), false, false, 0)
	var sawReconnecting bool
	for _, e := range dropEffs {
		if st, ok := e.(CoordTerminalState); ok {
			if st.State == TermStateClosed {
				t.Fatalf("Phase 3 full disconnect must enter Reconnecting, not Closed: %+v", st)
			}
			if st.State == TermStateReconnecting {
				sawReconnecting = true
			}
		}
	}
	if !sawReconnecting {
		t.Fatalf("expected TermStateReconnecting after both legs drop, got %+v", dropEffs)
	}

	// Direct round returns -> attachDirectTerminalsForMachine automatically queues Control 3 InputStatus!
	coord.attachDirectTerminalsForMachine(now+50, daemon.id)
	ct := coord.termStreams[streamID]
	if ct == nil || !ct.queryInFlight || len(ct.queryID) != 16 {
		t.Fatalf("expected Core to automatically send Control 3 InputStatus on reconnect, ct=%+v", ct)
	}

	// Daemon replies Available=true, Processed=0 -> Core reconciles, resends seq=1, triggers relay recovery, emits Live!
	repBody := linkcore.EncodeInputStatusReply(linkcore.InputStatusReplyMsg{
		Stream:        streamID,
		QueryID:       ct.queryID,
		Available:     true,
		Complete:      true,
		SnapshotToken: bytes.Repeat([]byte{0x77}, 32),
		State: linkcore.TermFlowState{
			ToDaemon: linkcore.TermWatermark{Processed: 0, InputSuccess: 0},
		},
	})
	_, _ = coord.decodeControl(now+60, r, linkcore.HandleControl{
		Frame: peerwire.Opened{
			Context: peerwire.Context{Class: 2, Seq: 11},
			Type:    linkcore.ControlInputStatusReply,
			Payload: repBody,
			Hash:    bytes.Repeat([]byte{0xbc}, 32),
		},
	})
	effs := coord.take()
	var resent, attachedRelay, sawLive bool
	for _, e := range effs {
		switch v := e.(type) {
		case CoordTerminalSend:
			if bytes.Equal(v.SealedEnvelope, []byte("sealed-pwd")) && v.DirectPCID == readPC {
				resent = true
			}
		case CoordTerminalAttachRelay:
			if v.StreamID == streamID && len(v.LegID) == 32 {
				attachedRelay = true
			}
		case CoordTerminalState:
			if v.State == TermStateLive && v.Reason == "reconciled" {
				sawLive = true
			}
		}
	}
	if !resent || !attachedRelay || !sawLive {
		t.Fatalf("expected resend=%v attachRelay=%v live=%v after InputStatusReply, got %+v", resent, attachedRelay, sawLive, effs)
	}

	// Sub-check: daemon restart (Available=false) triggers TermStateInstanceChange + NeedsConfirmation.
	_ = coord.TerminalDirectLeg(now+80, streamID, nil, true, false, readPC)
	coord.attachDirectTerminalsForMachine(now+90, daemon.id)
	qid2 := ct.queryID
	unavailBody := linkcore.EncodeInputStatusReply(linkcore.InputStatusReplyMsg{
		Stream:    streamID,
		QueryID:   qid2,
		Available: false,
		Complete:  true,
	})
	_, _ = coord.decodeControl(now+95, r, linkcore.HandleControl{
		Frame: peerwire.Opened{
			Context: peerwire.Context{Class: 2, Seq: 12},
			Type:    linkcore.ControlInputStatusReply,
			Payload: unavailBody,
			Hash:    bytes.Repeat([]byte{0xbd}, 32),
		},
	})
	effs2 := coord.take()
	var sawInstanceChange bool
	for _, e := range effs2 {
		if st, ok := e.(CoordTerminalState); ok && st.State == TermStateInstanceChange && st.NeedsConfirmation {
			sawInstanceChange = true
		}
	}
	if !sawInstanceChange {
		t.Fatalf("expected TermStateInstanceChange + NeedsConfirmation when Available=false, got %+v", effs2)
	}
}

// TestReview2_HA_ResizeAndInFlightInputNoSeqHole reproduces H-A:
// Resize (non-input) and Input frames sent while disconnected on a Phase 3 stream
// must BOTH be cached and resent in sequence order on InputStatus reconciliation so
// the daemon never sees a seq hole (seq=1 -> seq=3 skipping seq=2).
func TestReview2_HA_ResizeAndInFlightInputNoSeqHole(t *testing.T) {
	coord, _, daemon, now, _, rid, readPC := driveEstablishedCoordRound(t, "127.0.0.1", "")
	r := coord.rounds[rid]
	capBody := peerwire.PB(
		peerwire.B(1, bytes.Repeat([]byte{0x41}, 16)),
		peerwire.U(2, 1), peerwire.U(2, 2), peerwire.U(2, 3), peerwire.U(2, 4),
		peerwire.U(2, 5), peerwire.U(2, 6), peerwire.U(2, 7), peerwire.U(2, 8),
		peerwire.U(2, 11), peerwire.U(2, 12), peerwire.U(2, 15),
		peerwire.U(3, 1), peerwire.U(3, 2),
		peerwire.U(4, uint64(now+55_000)),
		peerwire.U(5, 1),
		peerwire.U(6, 1<<20),
	)
	_, _ = coord.decodeControl(now, r, linkcore.HandleControl{
		Frame: peerwire.Opened{
			Context: peerwire.Context{Class: 2, Seq: 10, Issued: uint64(now), Deadline: uint64(now + 55_000)},
			Type:    1,
			Payload: capBody,
			Hash:    bytes.Repeat([]byte{0xaa}, 32),
		},
	})
	streamID := "ha0123456789abcdef0123456789abcd"
	_ = coord.TerminalDirectOpen(now, streamID, daemon.id, "sess-ha", []byte("relay00000000010"))

	// In-flight input seq=1 sent BEFORE direct attaches (daemonReady=false) on a Phase 3 stream must still be cached.
	h1 := sha256.Sum256([]byte("echo 1\n"))
	_ = coord.TerminalDirectInput(now+1, streamID, 1, true, []byte("env-seq-1"), h1[:])

	// Confirm daemon ready -> attaches direct leg.
	_ = coord.TerminalDirectLeg(now+2, streamID, []byte("relay00000000010"), false, true, 0)

	// Drop direct + relay -> full disconnect recovery window.
	_ = coord.TerminalDirectLeg(now+10, streamID, nil, true, false, readPC)
	_ = coord.TerminalDirectLeg(now+15, streamID, []byte("relay00000000010"), false, false, 0)

	// Send Resize (seq=2, isInput=false) followed by Input (seq=3, isInput=true) while disconnected.
	h2 := sha256.Sum256([]byte("resize-100x40"))
	_ = coord.TerminalDirectInput(now+20, streamID, 2, false, []byte("env-seq-2-resize"), h2[:])
	h3 := sha256.Sum256([]byte("echo 3\n"))
	_ = coord.TerminalDirectInput(now+25, streamID, 3, true, []byte("env-seq-3-input"), h3[:])

	// Reconnect direct -> InputStatus query fired; daemon reports Processed=1 (seq=1 done, seq=2&3 missing).
	coord.attachDirectTerminalsForMachine(now+30, daemon.id)
	ct := coord.termStreams[streamID]
	if ct == nil || !ct.queryInFlight {
		t.Fatalf("expected InputStatus query in flight")
	}
	repBody := linkcore.EncodeInputStatusReply(linkcore.InputStatusReplyMsg{
		Stream:        streamID,
		QueryID:       ct.queryID,
		Available:     true,
		Complete:      true,
		SnapshotToken: bytes.Repeat([]byte{0x42}, 32),
		State: linkcore.TermFlowState{
			ToDaemon: linkcore.TermWatermark{
				Processed:    1,
				InputSuccess: 1,
				Details: []linkcore.TermFrameStatus{
					{Seq: 1, PayloadHash: h1, Outcome: linkcore.TermOutcomeApplied, DefinitelyInjected: 7, PossiblyInjected: 7, InputBytes: 7},
				},
			},
		},
	})
	_, _ = coord.decodeControl(now+35, r, linkcore.HandleControl{
		Frame: peerwire.Opened{
			Context: peerwire.Context{Class: 2, Seq: 11},
			Type:    linkcore.ControlInputStatusReply,
			Payload: repBody,
			Hash:    bytes.Repeat([]byte{0xbb}, 32),
		},
	})
	effs := coord.take()
	var resent [][]byte
	for _, e := range effs {
		if se, ok := e.(CoordTerminalSend); ok && se.DirectPCID == readPC {
			resent = append(resent, se.SealedEnvelope)
		}
	}
	if len(resent) != 2 || !bytes.Equal(resent[0], []byte("env-seq-2-resize")) || !bytes.Equal(resent[1], []byte("env-seq-3-input")) {
		t.Fatalf("expected BOTH seq=2 (Resize) and seq=3 (Input) resent in order, got %q", resent)
	}

	// Verify daemon-side TerminalStream executes BOTH seq=2 and seq=3 sequentially with zero seq hole.
	daemonTS := linkcore.NewTerminalStream(streamID, "sess-ha", daemon.id, "daemon-inst", uint64(now))
	if act, _, _ := daemonTS.ReceiveInput(uint64(now+1), linkcore.TermFrame{Seq: 1, PayloadHash: h1, IsInput: true}); act != linkcore.TermInputRun {
		t.Fatalf("daemon seq=1 expected Run, got %v", act)
	}
	_, _, _, _, _ = daemonTS.CompleteInput(uint64(now+2), 1, linkcore.TermOutcomeApplied, 1, 1, 0)
	if act, _, _ := daemonTS.ReceiveInput(uint64(now+40), linkcore.TermFrame{Seq: 2, PayloadHash: h2, IsInput: false}); act != linkcore.TermInputRun {
		t.Fatalf("daemon seq=2 (Resize) expected Run, got %v", act)
	}
	next, _, _, _, _ := daemonTS.CompleteInput(uint64(now+41), 2, linkcore.TermOutcomeApplied, 0, 0, 0)
	if next != nil {
		t.Fatalf("unexpected buffered next before seq=3")
	}
	if act, _, _ := daemonTS.ReceiveInput(uint64(now+42), linkcore.TermFrame{Seq: 3, PayloadHash: h3, IsInput: true}); act != linkcore.TermInputRun {
		t.Fatalf("daemon seq=3 (Input) expected Run immediately after Resize (no seq hole), got %v", act)
	}
	_, _, _, _, _ = daemonTS.CompleteInput(uint64(now+43), 3, linkcore.TermOutcomeApplied, 1, 1, 0)
	if got := daemonTS.CurrentInputWatermark().Processed; got != 3 {
		t.Fatalf("expected daemon Processed=3 after seq 1,2,3, got %d", got)
	}
}

// TestReview2_HB_DirectAttachWaitsForDaemonReady verifies H-B on ClientCore:
// TerminalDirectOpen does not queue direct AttachPath until daemon confirms openTerminal
// via TerminalDirectLeg(direct=false, open=true).
func TestReview2_HB_DirectAttachWaitsForDaemonReady(t *testing.T) {
	coord, _, daemon, now, _, rid, readPC := driveEstablishedCoordRound(t, "127.0.0.1", "")
	r := coord.rounds[rid]
	capBody := peerwire.PB(
		peerwire.B(1, bytes.Repeat([]byte{0x43}, 16)),
		peerwire.U(2, 1), peerwire.U(2, 2), peerwire.U(2, 3), peerwire.U(2, 4),
		peerwire.U(2, 5), peerwire.U(2, 6), peerwire.U(2, 7), peerwire.U(2, 8),
		peerwire.U(2, 11), peerwire.U(2, 12), peerwire.U(2, 15),
		peerwire.U(3, 1), peerwire.U(3, 2),
		peerwire.U(4, uint64(now+55_000)),
		peerwire.U(5, 1),
		peerwire.U(6, 1<<20),
	)
	_, _ = coord.decodeControl(now, r, linkcore.HandleControl{
		Frame: peerwire.Opened{
			Context: peerwire.Context{Class: 2, Seq: 10, Issued: uint64(now), Deadline: uint64(now + 55_000)},
			Type:    1,
			Payload: capBody,
			Hash:    bytes.Repeat([]byte{0xaa}, 32),
		},
	})
	streamID := "hb0123456789abcdef0123456789abcd"
	_ = coord.TerminalDirectOpen(now, streamID, daemon.id, "sess-hb", []byte("relay00000000011"))
	ct := coord.termStreams[streamID]
	if ct == nil || ct.directAttached || ct.directAttachGen != 0 {
		t.Fatalf("H-B violation: direct AttachPath must NOT fire before openTerminal confirms daemonReady, ct=%+v", ct)
	}
	// Confirm openTerminal succeeded -> direct AttachPath fires immediately.
	_ = coord.TerminalDirectLeg(now+5, streamID, []byte("relay00000000011"), false, true, 0)
	if !ct.directAttached || ct.directPCID != readPC || ct.directAttachGen != 1 {
		t.Fatalf("expected direct AttachPath immediately after daemonReady, ct=%+v", ct)
	}
}

// TestReview2_N3_N5_N7_N8_N9_ClientCore verifies:
// - N3: monotonic directAttachGen across detach/reattach
// - N5 & N7: failed relay recovery dial schedules bounded retry with strictly higher relayAttachGen and 32-char lowercase hex LegID
// - N8: ControlClosePath(Reason=2) emits TermStateClosed(replay_overflow)
// - N9: InputStatusReply rejects empty or mismatched QueryID
func TestReview2_N3_N5_N7_N8_N9_ClientCore(t *testing.T) {
	coord, _, daemon, now, _, rid, readPC := driveEstablishedCoordRound(t, "127.0.0.1", "")
	r := coord.rounds[rid]
	capBody := peerwire.PB(
		peerwire.B(1, bytes.Repeat([]byte{0x44}, 16)),
		peerwire.U(2, 1), peerwire.U(2, 2), peerwire.U(2, 3), peerwire.U(2, 4),
		peerwire.U(2, 5), peerwire.U(2, 6), peerwire.U(2, 7), peerwire.U(2, 8),
		peerwire.U(2, 11), peerwire.U(2, 12), peerwire.U(2, 15),
		peerwire.U(3, 1), peerwire.U(3, 2),
		peerwire.U(4, uint64(now+55_000)),
		peerwire.U(5, 1),
		peerwire.U(6, 1<<20),
	)
	_, _ = coord.decodeControl(now, r, linkcore.HandleControl{
		Frame: peerwire.Opened{
			Context: peerwire.Context{Class: 2, Seq: 10, Issued: uint64(now), Deadline: uint64(now + 55_000)},
			Type:    1,
			Payload: capBody,
			Hash:    bytes.Repeat([]byte{0xaa}, 32),
		},
	})
	streamID := "n357890123456789abcdef0123456789"
	_ = coord.TerminalDirectOpen(now, streamID, daemon.id, "sess-n", []byte("relay00000000012"))
	_ = coord.TerminalDirectLeg(now+1, streamID, []byte("relay00000000012"), false, true, 0)
	ct := coord.termStreams[streamID]
	if ct.directAttachGen != 1 {
		t.Fatalf("expected initial directAttachGen=1, got %d", ct.directAttachGen)
	}

	// N3: detach direct and reattach -> directAttachGen advances to 2.
	_ = coord.TerminalDirectLeg(now+5, streamID, nil, true, false, readPC)
	coord.attachDirectTerminalsForMachine(now+10, daemon.id)
	if ct.directAttachGen != 2 {
		t.Fatalf("N3: expected monotonic directAttachGen=2 after reattach, got %d", ct.directAttachGen)
	}

	// N5 & N7: drop relay while direct is active -> triggers recovery with 32-char lowercase hex StreamId.
	isHex32 := func(b []byte) bool {
		if len(b) != 32 {
			return false
		}
		for _, ch := range b {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
				return false
			}
		}
		return true
	}
	dropEffs := coord.TerminalDirectLeg(now+20, streamID, []byte("relay00000000012"), false, false, 0)
	var leg1 []byte
	for _, e := range dropEffs {
		if ar, ok := e.(CoordTerminalAttachRelay); ok {
			leg1 = ar.LegID
		}
	}
	if !isHex32(leg1) || ct.relayAttachGen != 2 {
		t.Fatalf("N7/N5: expected 32-char hex leg and relayAttachGen=2, got leg=%q gen=%d", string(leg1), ct.relayAttachGen)
	}

	// Report dial failure for leg1 -> Core schedules retry at now+25+1000.
	_ = coord.TerminalDirectLeg(now+25, streamID, leg1, false, false, 0)
	_, _, sessAt, sessOK, _, _ := coord.NextTimers(now + 25)
	if !sessOK || sessAt > now+1025 {
		t.Fatalf("N5: expected retry timer <= %d, got ok=%v at=%d", now+1025, sessOK, sessAt)
	}
	retryEffs := coord.OnSessionTimer(now + 1025)
	var leg2 []byte
	for _, e := range retryEffs {
		if ar, ok := e.(CoordTerminalAttachRelay); ok {
			leg2 = ar.LegID
		}
	}
	if !isHex32(leg2) || bytes.Equal(leg1, leg2) || ct.relayAttachGen != 3 {
		t.Fatalf("N5/N7: expected retry with new 32-char hex leg and relayAttachGen=3, got leg2=%q gen=%d", string(leg2), ct.relayAttachGen)
	}

	// N9: trigger InputStatus query, then send empty and mismatched QueryID -> must be ignored!
	_ = coord.TerminalDirectLeg(now+1030, streamID, nil, true, false, readPC)
	coord.attachDirectTerminalsForMachine(now+1040, daemon.id)
	if !ct.queryInFlight || len(ct.queryID) != 16 {
		t.Fatalf("expected queryInFlight with 16B QueryID")
	}
	for _, badQID := range [][]byte{nil, bytes.Repeat([]byte{0xee}, 16)} {
		badRep := linkcore.EncodeInputStatusReply(linkcore.InputStatusReplyMsg{
			Stream:    streamID,
			QueryID:   badQID,
			Available: false,
			Complete:  true,
		})
		_, _ = coord.decodeControl(now+1045, r, linkcore.HandleControl{
			Frame: peerwire.Opened{
				Context: peerwire.Context{Class: 2, Seq: 20},
				Type:    linkcore.ControlInputStatusReply,
				Payload: badRep,
				Hash:    bytes.Repeat([]byte{0xcc}, 32),
			},
		})
		if !ct.queryInFlight || ct.needsConfirm {
			t.Fatalf("N9: InputStatusReply with bad QueryID %x must be ignored", badQID)
		}
	}

	// N8: daemon sends ControlClosePath(Reason=2) -> immediate TermStateClosed(replay_overflow).
	cpBody := linkcore.EncodeClosePath(linkcore.ClosePathMsg{
		Stream:         streamID,
		LegID:          "d:0123456789abcd",
		PathGeneration: 2,
		Reason:         2,
	})
	_, _ = coord.decodeControl(now+1050, r, linkcore.HandleControl{
		Frame: peerwire.Opened{
			Context: peerwire.Context{Class: 2, Seq: 21},
			Type:    linkcore.ControlClosePath,
			Payload: cpBody,
			Hash:    bytes.Repeat([]byte{0xdd}, 32),
		},
	})
	closeEffs := coord.take()
	var sawOverflow bool
	for _, e := range closeEffs {
		if st, ok := e.(CoordTerminalState); ok && st.State == TermStateClosed && st.Reason == "replay_overflow" {
			sawOverflow = true
		}
	}
	if !sawOverflow || coord.termStreams[streamID] != nil {
		t.Fatalf("N8: expected TermStateClosed(replay_overflow) and stream deletion, got %+v", closeEffs)
	}
}

// TestReview3_C_PureRelayFailStopClosedEntersConfirmationGate verifies C on ClientCore:
// A pure-relay stream receiving daemon's fail-stop TerminalFrame_Closed enters Core's
// confirmation gate (TermStateFailStop + NeedsConfirmation=true) and keeps that gate open
// when relay WS EOF follows immediately.
func TestReview3_C_PureRelayFailStopClosedEntersConfirmationGate(t *testing.T) {
	coord := NewCoordinator(CoordConfig{Supported: false, ProtocolVersion: 17})
	now := int64(2_000_000)
	streamID := "c3client0123456789abcdef01234567"
	_ = coord.TerminalDirectOpen(now, streamID, "machine-pure", "sess-pure", []byte("relay00000000031"))

	closedPayload := peerwire.PB(peerwire.B(3, []byte(linkcore.TermRelayFailStopTag+" input could not be confirmed")))
	h := sha256.Sum256(closedPayload)
	effs := coord.TerminalDirectOutput(now+10, streamID, 1, h[:], closedPayload, false, 0)
	var sawConfirm bool
	for _, e := range effs {
		if _, ok := e.(CoordTerminalDeliver); ok {
			t.Fatalf("fail-stop Closed frame should trigger confirmation state instead of plain close deliver: %+v", e)
		}
		if st, ok := e.(CoordTerminalState); ok && st.State == TermStateFailStop && st.NeedsConfirmation {
			sawConfirm = true
		}
	}
	if !sawConfirm {
		t.Fatalf("C: expected TermStateFailStop + NeedsConfirmation=true on pure-relay fail-stop Closed frame, got %+v", effs)
	}

	// Relay WS EOF immediately following daemon rp.Finish() must NOT overwrite confirmation gate with plain Closed!
	eofEffs := coord.TerminalDirectLeg(now+15, streamID, []byte("relay00000000031"), false, false, 0)
	for _, e := range eofEffs {
		if st, ok := e.(CoordTerminalState); ok && st.State == TermStateClosed {
			t.Fatalf("C: relay EOF must not clobber active confirmation gate with TermStateClosed: %+v", st)
		}
	}

	// User confirms rebuild -> emits TermStateClosed("rebuild_confirmed").
	rebuildEffs := coord.TerminalDirectConfirmRebuild(now+20, streamID)
	var sawRebuild bool
	for _, e := range rebuildEffs {
		if st, ok := e.(CoordTerminalState); ok && st.State == TermStateClosed && st.Reason == "rebuild_confirmed" {
			sawRebuild = true
		}
	}
	if !sawRebuild {
		t.Fatalf("expected ConfirmRebuild after pure-relay fail-stop to emit rebuild_confirmed, got %+v", rebuildEffs)
	}
}

// TestReview3_NK_AttachPathUnsupportedEntersConfirmationGate verifies N-K:
// When daemon replies ControlUnsupported (10) to AttachPath for a stream, ClientCore
// clears directAttached and transitions that stream to confirmation state.
func TestReview3_NK_AttachPathUnsupportedEntersConfirmationGate(t *testing.T) {
	coord, _, daemon, now, _, rid, _ := driveEstablishedCoordRound(t, "127.0.0.1", "")
	r := coord.rounds[rid]
	capBody := peerwire.PB(
		peerwire.B(1, bytes.Repeat([]byte{0x55}, 16)),
		peerwire.U(2, 1), peerwire.U(2, 2), peerwire.U(2, 3), peerwire.U(2, 4),
		peerwire.U(2, 5), peerwire.U(2, 6), peerwire.U(2, 7), peerwire.U(2, 8),
		peerwire.U(2, 11), peerwire.U(2, 12), peerwire.U(2, 15),
		peerwire.U(3, 1), peerwire.U(3, 2),
		peerwire.U(4, uint64(now+55_000)),
		peerwire.U(5, 1),
		peerwire.U(6, 1<<20),
	)
	_, _ = coord.decodeControl(now, r, linkcore.HandleControl{
		Frame: peerwire.Opened{
			Context: peerwire.Context{Class: 2, Seq: 10, Issued: uint64(now), Deadline: uint64(now + 55_000)},
			Type:    1,
			Payload: capBody,
			Hash:    bytes.Repeat([]byte{0xaa}, 32),
		},
	})
	streamID := "nk0123456789abcdef0123456789abcd"
	_ = coord.TerminalDirectOpen(now, streamID, daemon.id, "sess-nk", []byte("relay00000000041"))
	_ = coord.TerminalDirectLeg(now+1, streamID, []byte("relay00000000041"), false, true, 0)
	ct := coord.termStreams[streamID]
	if ct == nil || !ct.directAttached {
		t.Fatalf("expected directAttached=true before Unsupported")
	}

	coord.applySessionEffects(now+10, r, []linkcore.Effect{
		linkcore.PeerUnsupported{Class: 2, Type: linkcore.ControlAttachPath, Use: 0, Stream: streamID},
	})
	effs := coord.take()
	var sawConfirm bool
	for _, e := range effs {
		if st, ok := e.(CoordTerminalState); ok && st.State == TermStateInstanceChange && st.NeedsConfirmation {
			sawConfirm = true
		}
	}
	if !sawConfirm || ct.directAttached {
		t.Fatalf("N-K: expected directAttached=false and TermStateInstanceChange+NeedsConfirmation, attached=%v effs=%+v", ct.directAttached, effs)
	}
}

func TestTerminalRelayOpenRespectsNeedsConfirmAndInitialLegIdempotency(t *testing.T) {
	// Audit core B6:
	// 1) TerminalDirectLeg(relay open=true) while needsConfirm=true must not emit TermStateLive.
	// 2) Initial leg open arriving after first output set daemonReady=true must not bump relayGen.
	coord, _, daemon, now, _, rid, _ := driveEstablishedCoordRound(t, "127.0.0.1", "")
	r := coord.rounds[rid]
	coord.handleEngine(now, Capabilities{Round: rid, Seq: 1, Terminal: true})

	// Scenario 1: needsConfirm=true, then replacement relay opens.
	s1 := "b60123456789abcdef0123456789abc1"
	_ = coord.TerminalDirectOpen(now, s1, daemon.id, "sess-b6-1", []byte("relay000000000b1"))
	ct1 := coord.termStreams[s1]
	ct1.needsConfirm = true
	ct1.relayOpen = false
	effs := coord.TerminalDirectLeg(now+10, s1, []byte("relay000000000b2"), false, true, 0)
	for _, e := range effs {
		if st, ok := e.(CoordTerminalState); ok && st.State == TermStateLive {
			t.Fatalf("B6: relay open overwrote needsConfirm gate with TermStateLive: %+v", st)
		}
	}

	// Scenario 2: first output sets daemonReady=true before openTerminal RPC confirms initial leg.
	s2 := "b60123456789abcdef0123456789abc2"
	initLeg := []byte("relay000000000b3")
	_ = coord.TerminalDirectOpen(now+20, s2, daemon.id, "sess-b6-2", initLeg)
	h := sha256.Sum256([]byte("out1"))
	_ = coord.TerminalDirectOutput(now+21, s2, 1, h[:], []byte("out1"), false, 0)
	ct2 := coord.termStreams[s2]
	if !ct2.daemonReady || ct2.relayGen != 1 {
		t.Fatalf("precondition: daemonReady=%v relayGen=%d", ct2.daemonReady, ct2.relayGen)
	}
	effs2 := coord.TerminalDirectLeg(now+22, s2, initLeg, false, true, 0)
	if ct2.relayGen != 1 {
		t.Fatalf("B6: initial leg open after early output bumped relayGen to %d (want 1)", ct2.relayGen)
	}
	for _, e := range effs2 {
		if st, ok := e.(CoordTerminalState); ok && st.State == TermStateLive {
			t.Fatalf("B6: duplicate TermStateLive emitted on initial leg confirm")
		}
	}
	_ = r
}

func TestTerminalInputAndOutputPopulateDeadlineMs(t *testing.T) {
	// Audit core B7d: TerminalDirectInput must populate TermFrame.DeadlineMs
	// (from Envelope Header.deadline_ms or 60s fallback) so ReconcileStatus
	// requests a Barrier after expiry instead of blindly resending expired envelopes.
	coord, _, daemon, now, _, rid, _ := driveEstablishedCoordRound(t, "127.0.0.1", "")
	coord.handleEngine(now, Capabilities{Round: rid, Seq: 1, Terminal: true})
	streamID := "b7d123456789abcdef0123456789abcd"
	_ = coord.TerminalDirectOpen(now, streamID, daemon.id, "sess-b7d", []byte("relay00000000b7d"))
	_ = coord.TerminalDirectLeg(now+1, streamID, []byte("relay00000000b7d"), false, true, 0)

	// Craft a minimal Envelope whose Header (field 1) has deadline_ms (field 7) = now + 5000.
	hdr := peerwire.PB(peerwire.U(1, 1), peerwire.U(7, uint64(now+5000)))
	env := peerwire.PB(peerwire.B(1, hdr))
	h := sha256.Sum256(env)
	_ = coord.TerminalDirectInput(now+10, streamID, 1, true, env, h[:])

	ct := coord.termStreams[streamID]
	snap := linkcore.TermStatusSnapshot{Available: true}
	toResend, needBarrier, needConfirm := ct.stream.ReconcileStatus(uint64(now+6000), snap)
	if !needBarrier || len(toResend) != 0 || needConfirm {
		t.Fatalf("B7d: expected needBarrier=true for expired cached input, got resend=%d barrier=%v confirm=%v",
			len(toResend), needBarrier, needConfirm)
	}
}

func TestTerminalRecoveryControlTimeoutsAndReplay(t *testing.T) {
	// Audit core B8:
	// 1) queryInFlight retries after 3s and PeerUnsupported for ControlInputStatus enters confirm.
	// 2) ControlBarrier ignores mismatched BarrierID and times out after 5s (TermBarrierTimeoutMs).
	// 3) Output gap schedules and sends ControlReplay well before 10s gap timeout (not at 10s kill).
	coord, _, daemon, now, _, rid, _ := driveEstablishedCoordRound(t, "127.0.0.1", "")
	r := coord.rounds[rid]
	coord.handleEngine(now, Capabilities{Round: rid, Seq: 1, Terminal: true})

	// 1. Output gap triggers ControlReplay before 10s deadline.
	sGap := "b80123456789abcdef0123456789abc1"
	_ = coord.TerminalDirectOpen(now, sGap, daemon.id, "sess-b8-gap", []byte("relay00000000b81"))
	_ = coord.TerminalDirectLeg(now+1, sGap, []byte("relay00000000b81"), false, true, 0)
	// Deliver seq=2 (missing seq=1 -> output gap opens).
	h2 := sha256.Sum256([]byte("out2"))
	_ = coord.TerminalDirectOutput(now+10, sGap, 2, h2[:], []byte("out2"), true, r.globalPC[UseRead])
	_, _, sessAt, sessOK, _, _ := coord.NextTimers(now + 10)
	if !sessOK || sessAt > now+10+termReplayDebounceMs {
		t.Fatalf("B8: output gap NextTimers scheduled at %d (expected Replay at <= %d)",
			sessAt, now+10+termReplayDebounceMs)
	}
	// Fire timer at sessAt (< 1s): must emit SignFrame for ControlReplay and keep stream alive.
	effs := coord.OnSessionTimer(sessAt)
	sawSign := false
	for _, e := range effs {
		if _, ok := e.(CoordKeySign); ok {
			sawSign = true
		}
	}
	if !sawSign || coord.termStreams[sGap] == nil {
		t.Fatalf("B8: expected ControlReplay queued before 10s timeout (sawSign=%v streamAlive=%v)", sawSign, coord.termStreams[sGap] != nil)
	}

	// 2. queryInFlight times out after 3s and retries.
	sQ := "b80123456789abcdef0123456789abc2"
	_ = coord.TerminalDirectOpen(now+200, sQ, daemon.id, "sess-b8-q", []byte("relay00000000b82"))
	ctQ := coord.termStreams[sQ]
	ctQ.daemonReady = true
	ctQ.stream.NetworkRecovery = true
	coord.maybeAttachDirectTerminal(now+200, ctQ)
	coord.take()
	if !ctQ.queryInFlight {
		t.Fatal("expected queryInFlight=true")
	}
	firstQID := append([]byte(nil), ctQ.queryID...)
	coord.OnSessionTimer(now + 200 + 3500)
	if bytes.Equal(firstQID, ctQ.queryID) {
		t.Fatal("B8: queryInFlight did not time out and retry with fresh queryID after 3s")
	}

	// 3. ControlBarrier ignores mismatched BarrierID and times out after 5s.
	sB := "b80123456789abcdef0123456789abc3"
	_ = coord.TerminalDirectOpen(now+300, sB, daemon.id, "sess-b8-b", []byte("relay00000000b83"))
	_ = coord.TerminalDirectLeg(now+301, sB, []byte("relay00000000b83"), false, true, 0)
	ctB := coord.termStreams[sB]
	// Cache an input with deadline now+4000, then reconcile at now+4500 -> triggers ControlBarrier.
	hdr := peerwire.PB(peerwire.U(1, 1), peerwire.U(7, uint64(now+4000)))
	env := peerwire.PB(peerwire.B(1, hdr))
	hb := sha256.Sum256(env)
	_ = coord.TerminalDirectInput(now+310, sB, 1, true, env, hb[:])
	ctB.stream.NetworkRecovery = true
	ctB.directAttached = false
	ctB.directPCID = 0
	coord.maybeAttachDirectTerminal(now+4500, ctB)
	coord.take()
	if !ctB.queryInFlight {
		t.Fatal("expected queryInFlight for barrier setup")
	}
	repBody := linkcore.EncodeInputStatusReply(linkcore.InputStatusReplyMsg{
		Stream:        sB,
		QueryID:       ctB.queryID,
		Available:     true,
		Complete:      true,
		SnapshotToken: bytes.Repeat([]byte{0x11}, 32),
	})
	coord.handleInboundTerminalControl(now+4510, r, peerwire.Opened{
		Type:    linkcore.ControlInputStatusReply,
		Payload: repBody,
	})
	coord.take()
	if !ctB.barrierInFlight {
		t.Fatal("expected barrierInFlight=true after InputStatusReply with expired input")
	}
	// Deliver a ControlBarrierReply with a mismatched BarrierID -> must be ignored.
	wrongReply := linkcore.EncodeBarrierReply(linkcore.BarrierReplyMsg{
		Stream:    sB,
		BarrierID: bytes.Repeat([]byte{0x99}, 16),
		Final:     true,
	})
	coord.handleInboundTerminalControl(now+4600, r, peerwire.Opened{
		Type:    linkcore.ControlBarrierReply,
		Payload: wrongReply,
	})
	if ctB.needsConfirm || !ctB.barrierInFlight {
		t.Fatal("B8: ControlBarrierReply accepted mismatched BarrierID")
	}
	// Advance past 5s TermBarrierTimeoutMs -> must time out and enter confirmation gate.
	effsB := coord.OnSessionTimer(now + 4510 + int64(linkcore.TermBarrierTimeoutMs))
	sawTimeout := false
	for _, e := range effsB {
		if st, ok := e.(CoordTerminalState); ok && st.StreamID == sB && st.NeedsConfirmation && st.Reason == "barrier_timeout" {
			sawTimeout = true
		}
	}
	if !sawTimeout || ctB.barrierInFlight {
		t.Fatalf("B8: ControlBarrier did not time out after 5s (sawTimeout=%v barrierInFlight=%v)", sawTimeout, ctB.barrierInFlight)
	}

	// 4. PeerUnsupported for ControlInputStatus enters confirmation gate.
	coord.applySessionEffects(now+4600, r, []linkcore.Effect{
		linkcore.PeerUnsupported{Class: 2, Type: linkcore.ControlInputStatus, Stream: sQ},
	})
	effsU := coord.take()
	sawUnsupp := false
	for _, e := range effsU {
		if st, ok := e.(CoordTerminalState); ok && st.StreamID == sQ && st.NeedsConfirmation {
			sawUnsupp = true
		}
	}
	if !sawUnsupp || ctQ.queryInFlight {
		t.Fatalf("B8: PeerUnsupported for ControlInputStatus did not enter confirm gate (sawUnsupp=%v queryInFlight=%v)", sawUnsupp, ctQ.queryInFlight)
	}
}
