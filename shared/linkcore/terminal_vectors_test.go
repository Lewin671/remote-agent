package linkcore

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/Lewin671/remote-agent/shared/peerwire"
)

var updateTerminalVectors = flag.Bool("update-terminal-vectors", false, "rewrite testdata/transport/terminal/control-vectors.json")

type terminalControlVector struct {
	Name        string `json:"name"`
	ControlType uint64 `json:"control_type"`
	Message     string `json:"message"`
	WireHex     string `json:"wire_hex"`
	Stream      string `json:"stream,omitempty"`
	Valid       bool   `json:"valid"`
}

type probeFrameVector struct {
	Name    string `json:"name"`
	WireHex string `json:"wire_hex"`
	Kind    uint8  `json:"kind"`
	Valid   bool   `json:"valid"`
}

type terminalControlVectorFile struct {
	Description string                  `json:"description"`
	Controls    []terminalControlVector `json:"controls"`
	Probes      []probeFrameVector      `json:"probes"`
}

func buildCanonicalTerminalControlVectors() terminalControlVectorFile {
	const (
		streamID  = "0123456789abcdef0123456789abcdef"
		sessionID = "session-0000000000000001"
		senderID  = "abcdef0123456789abcdef0123456789"
		legID     = "leg-000000000001"
		queryID   = "query-0000000001"
		barrierID = "barrier-00000001"
	)
	var h1, h2 [32]byte
	for i := range h1 {
		h1[i] = byte(0x10 + i)
		h2[i] = byte(0x80 + i)
	}
	wmIn := TermWatermark{
		Processed:      5,
		InputSuccess:   4,
		FailedInputSeq: 5,
		Details: []TermFrameStatus{
			{
				Seq:                5,
				PayloadHash:        h1,
				Outcome:            TermOutcomeInputPartialUnknown,
				DefinitelyInjected: 256,
				PossiblyInjected:   512,
				InputBytes:         512,
				FailureReason:      2,
			},
		},
	}
	wmOut := TermWatermark{Processed: 12}
	fs := TermFlowState{ToDaemon: wmIn, ToClient: wmOut}
	tok := FlowStateToken(fs)

	attachRaw := EncodeAttachPath(AttachPathMsg{
		Stream:         streamID,
		Session:        sessionID,
		Sender:         senderID,
		LegID:          legID,
		PathGeneration: 2,
		State:          fs,
	})
	statusReqRaw := EncodeInputStatus(InputStatusMsg{
		Stream:        streamID,
		QueryID:       []byte(queryID),
		FromSeq:       1,
		Limit:         16,
		SnapshotToken: tok[:],
	})
	statusRepRaw := EncodeInputStatusReply(InputStatusReplyMsg{
		Stream:        streamID,
		QueryID:       []byte(queryID),
		Available:     true,
		SnapshotToken: tok[:],
		State:         fs,
		NextSeq:       6,
		Complete:      true,
	})
	ackRaw := EncodeTermAck(TermAckMsg{
		Stream:    streamID,
		Direction: TermDirToDaemon,
		State:     wmIn,
	})
	replayRaw := EncodeReplay(ReplayMsg{
		Stream:     streamID,
		Direction:  TermDirToClient,
		FromSeq:    3,
		ThroughSeq: 12,
		MaxBytes:   1 << 20,
		MaxFrames:  128,
	})
	barrierRaw := EncodeBarrier(BarrierMsg{
		Stream:         streamID,
		BarrierID:      []byte(barrierID),
		Requested:      fs,
		StopAdmission:  true,
		NewLegID:       legID,
		PathGeneration: 3,
	})
	barrierRepRaw := EncodeBarrierReply(BarrierReplyMsg{
		Stream:    streamID,
		BarrierID: []byte(barrierID),
		State:     fs,
		Final:     true,
		Accepted:  true,
	})
	failedRaw := EncodeStreamFailed(StreamFailedMsg{
		Stream:       streamID,
		InputFailure: wmIn.Details[0],
		State:        fs,
	})
	closeRaw := EncodeClosePath(ClosePathMsg{
		Stream:         streamID,
		LegID:          legID,
		PathGeneration: 2,
		Reason:         1,
	})

	// Negative vector: unknown field 99 appended to ClosePath.
	badUnknown := append(append([]byte(nil), closeRaw...), peerwire.PB(peerwire.U(99, 1))...)
	// Negative vector: AttachPath with short leg_id (must be 16 bytes).
	badAttach := EncodeAttachPath(AttachPathMsg{
		Stream:  streamID,
		Session: sessionID,
		Sender:  senderID,
		LegID:   "short",
	})

	probeReq := make([]byte, ProbeFrameBytes)
	probeReq[0] = 0
	for i := 1; i < ProbeFrameBytes; i++ {
		probeReq[i] = byte(i)
	}
	probeRep := append([]byte(nil), probeReq...)
	probeRep[0] = 1
	probeBad := []byte{0, 1, 2, 3}

	return terminalControlVectorFile{
		Description: "Frozen Phase 3 terminal continuity control wire vectors (Controls 2,3,4,5,6,7,8,11,12) and 17-byte liveness probe vectors shared across Go, Web, Android, and iOS.",
		Controls: []terminalControlVector{
			{Name: "attach_path_valid", ControlType: ControlAttachPath, Message: "AttachPath", WireHex: hex.EncodeToString(attachRaw), Stream: streamID, Valid: true},
			{Name: "input_status_valid", ControlType: ControlInputStatus, Message: "InputStatus", WireHex: hex.EncodeToString(statusReqRaw), Stream: streamID, Valid: true},
			{Name: "input_status_reply_valid", ControlType: ControlInputStatusReply, Message: "InputStatusReply", WireHex: hex.EncodeToString(statusRepRaw), Stream: streamID, Valid: true},
			{Name: "term_ack_valid", ControlType: ControlAck, Message: "Ack", WireHex: hex.EncodeToString(ackRaw), Stream: streamID, Valid: true},
			{Name: "replay_valid", ControlType: ControlReplay, Message: "Replay", WireHex: hex.EncodeToString(replayRaw), Stream: streamID, Valid: true},
			{Name: "barrier_valid", ControlType: ControlBarrier, Message: "Barrier", WireHex: hex.EncodeToString(barrierRaw), Stream: streamID, Valid: true},
			{Name: "barrier_reply_valid", ControlType: ControlBarrierReply, Message: "BarrierReply", WireHex: hex.EncodeToString(barrierRepRaw), Stream: streamID, Valid: true},
			{Name: "stream_failed_valid", ControlType: ControlStreamFailed, Message: "StreamFailed", WireHex: hex.EncodeToString(failedRaw), Stream: streamID, Valid: true},
			{Name: "close_path_valid", ControlType: ControlClosePath, Message: "ClosePath", WireHex: hex.EncodeToString(closeRaw), Stream: streamID, Valid: true},
			{Name: "close_path_unknown_field_rejected", ControlType: ControlClosePath, Message: "ClosePath", WireHex: hex.EncodeToString(badUnknown), Valid: false},
			{Name: "attach_path_short_leg_rejected", ControlType: ControlAttachPath, Message: "AttachPath", WireHex: hex.EncodeToString(badAttach), Valid: false},
		},
		Probes: []probeFrameVector{
			{Name: "probe_request_valid", WireHex: hex.EncodeToString(probeReq), Kind: 0, Valid: true},
			{Name: "probe_reply_valid", WireHex: hex.EncodeToString(probeRep), Kind: 1, Valid: true},
			{Name: "probe_short_rejected", WireHex: hex.EncodeToString(probeBad), Kind: 0, Valid: false},
		},
	}
}

func TestSharedTerminalControlVectors(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "transport", "terminal", "control-vectors.json")
	if *updateTerminalVectors {
		vf := buildCanonicalTerminalControlVectors()
		data, err := json.MarshalIndent(vf, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, '\n')
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rawFile, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var vf terminalControlVectorFile
	if err := json.Unmarshal(rawFile, &vf); err != nil {
		t.Fatal(err)
	}
	if len(vf.Controls) < 11 || len(vf.Probes) < 3 {
		t.Fatalf("unexpected vector counts: controls=%d probes=%d", len(vf.Controls), len(vf.Probes))
	}

	for _, tc := range vf.Controls {
		raw, err := hex.DecodeString(tc.WireHex)
		if err != nil {
			t.Fatalf("%s: bad hex: %v", tc.Name, err)
		}
		var decErr error
		var reencoded []byte
		switch tc.ControlType {
		case ControlAttachPath:
			m, err := DecodeAttachPath(raw)
			decErr = err
			if err == nil {
				reencoded = EncodeAttachPath(m)
			}
		case ControlInputStatus:
			m, err := DecodeInputStatus(raw)
			decErr = err
			if err == nil {
				reencoded = EncodeInputStatus(m)
			}
		case ControlInputStatusReply:
			m, err := DecodeInputStatusReply(raw)
			decErr = err
			if err == nil {
				reencoded = EncodeInputStatusReply(m)
			}
		case ControlAck:
			m, isStream, err := DecodeTermAck(raw)
			if err == nil && !isStream {
				err = ErrTermInvalidControl
			}
			decErr = err
			if err == nil {
				reencoded = EncodeTermAck(m)
			}
		case ControlReplay:
			m, err := DecodeReplay(raw)
			decErr = err
			if err == nil {
				reencoded = EncodeReplay(m)
			}
		case ControlBarrier:
			m, err := DecodeBarrier(raw)
			decErr = err
			if err == nil {
				reencoded = EncodeBarrier(m)
			}
		case ControlBarrierReply:
			m, err := DecodeBarrierReply(raw)
			decErr = err
			if err == nil {
				reencoded = EncodeBarrierReply(m)
			}
		case ControlStreamFailed:
			m, err := DecodeStreamFailed(raw)
			decErr = err
			if err == nil {
				reencoded = EncodeStreamFailed(m)
			}
		case ControlClosePath:
			m, err := DecodeClosePath(raw)
			decErr = err
			if err == nil {
				reencoded = EncodeClosePath(m)
			}
		default:
			t.Fatalf("unknown control type %d", tc.ControlType)
		}

		if tc.Valid {
			if decErr != nil {
				t.Fatalf("%s: expected valid decode, got %v", tc.Name, decErr)
			}
			if _, err := peerwire.KnownPayload(2, tc.ControlType, raw); err != nil {
				t.Fatalf("%s: peerwire.KnownPayload failed: %v", tc.Name, err)
			}
			if !bytes.Equal(raw, reencoded) {
				t.Fatalf("%s: canonical re-encode mismatch", tc.Name)
			}
		} else if decErr == nil {
			t.Fatalf("%s: expected decode rejection, succeeded", tc.Name)
		}
	}

	for _, p := range vf.Probes {
		raw, err := hex.DecodeString(p.WireHex)
		if err != nil {
			t.Fatal(err)
		}
		ok := len(raw) == ProbeFrameBytes && (raw[0] == 0 || raw[0] == 1)
		if ok != p.Valid {
			t.Fatalf("%s: expected valid=%v got %v", p.Name, p.Valid, ok)
		}
	}
}
