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
