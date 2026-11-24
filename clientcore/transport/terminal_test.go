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
	if attachEff == nil || len(attachEff.LegID) != 16 {
		t.Fatalf("expected 16-byte CoordTerminalAttachRelay leg, got %+v", attachEff)
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

	// ConfirmRebuild clears confirmation gate.
	confirmEffs := coord.TerminalDirectConfirmRebuild(now+40, streamID)
	sawLive = false
	for _, e := range confirmEffs {
		if st, ok := e.(CoordTerminalState); ok && st.State == TermStateLive && !st.NeedsConfirmation {
			sawLive = true
		}
	}
	if !sawLive {
		t.Fatalf("expected ConfirmRebuild to clear gate, got %+v", confirmEffs)
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
			if v.StreamID == streamID && len(v.LegID) == 16 {
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
