package linkcore

import (
	"github.com/Lewin671/remote-agent/shared/peerwire"
	"google.golang.org/protobuf/encoding/protowire"
)

// Frozen TransportControl type IDs (docs/transport-wire-draft.md).
const (
	ControlCapabilities       uint64 = 1
	ControlAttachPath         uint64 = 2
	ControlInputStatus        uint64 = 3
	ControlAck                uint64 = 4
	ControlReplay             uint64 = 5
	ControlBarrier            uint64 = 6
	ControlStreamFailed       uint64 = 7
	ControlClosePath          uint64 = 8
	ControlDirectPolicy       uint64 = 9
	ControlUnsupported        uint64 = 10
	ControlInputStatusReply   uint64 = 11
	ControlBarrierReply       uint64 = 12
	ControlPolicyReply        uint64 = 13
	ControlCapabilitySnapshot uint64 = 14
	ControlEventSubscription  uint64 = 15
)

// AttachPathMsg is TransportControl type 2.
type AttachPathMsg struct {
	Stream         string
	Session        string
	Sender         string
	LegID          string
	PathGeneration uint64
	State          TermFlowState
}

// InputStatusMsg is TransportControl type 3.
type InputStatusMsg struct {
	Stream        string
	QueryID       []byte
	FromSeq       uint64
	Limit         uint32
	SnapshotToken []byte
}

// InputStatusReplyMsg is TransportControl type 11.
type InputStatusReplyMsg struct {
	Stream        string
	QueryID       []byte
	Available     bool
	SnapshotToken []byte
	State         TermFlowState
	NextSeq       uint64
	Complete      bool
}

// TermAckMsg is TransportControl type 4 (stream ACK variant).
type TermAckMsg struct {
	Stream    string
	Direction TermDirection
	State     TermWatermark
}

// ReplayMsg is TransportControl type 5.
type ReplayMsg struct {
	Stream     string
	Direction  TermDirection
	FromSeq    uint64
	ThroughSeq uint64
	MaxBytes   uint32
	MaxFrames  uint32
}

// BarrierMsg is TransportControl type 6.
type BarrierMsg struct {
	Stream         string
	BarrierID      []byte
	Requested      TermFlowState
	StopAdmission  bool
	NewLegID       string
	PathGeneration uint64
}

// BarrierReplyMsg is TransportControl type 12.
type BarrierReplyMsg struct {
	Stream    string
	BarrierID []byte
	State     TermFlowState
	Final     bool
	Accepted  bool
}

// StreamFailedMsg is TransportControl type 7.
type StreamFailedMsg struct {
	Stream       string
	InputFailure TermFrameStatus
	State        TermFlowState
}

// ClosePathMsg is TransportControl type 8.
type ClosePathMsg struct {
	Stream         string
	LegID          string
	PathGeneration uint64
	Reason         uint32
}

// EncodeWatermark encodes TermWatermark matching proto/remoteagent/v1/transport.proto Watermark.
func EncodeWatermark(w TermWatermark) []byte {
	out := peerwire.PB(
		peerwire.U(1, w.Processed),
		peerwire.U(2, w.InputSuccess),
		peerwire.U(3, w.FailedInputSeq),
	)
	for _, d := range w.Details {
		out = append(out, peerwire.PB(peerwire.B(4, EncodeFrameStatus(d)))...)
	}
	return out
}

// EncodeFrameStatus encodes TermFrameStatus matching proto FrameStatus.
func EncodeFrameStatus(d TermFrameStatus) []byte {
	return peerwire.PB(
		peerwire.U(1, d.Seq),
		peerwire.B(2, d.PayloadHash[:]),
		peerwire.U(3, uint64(d.Outcome)),
		peerwire.U(4, uint64(d.DefinitelyInjected)),
		peerwire.U(5, uint64(d.PossiblyInjected)),
		peerwire.U(6, uint64(d.InputBytes)),
		peerwire.U(7, uint64(d.FailureReason)),
	)
}

// DecodeFrameStatus strictly decodes FrameStatus via peerwire schema validation.
func DecodeFrameStatus(raw []byte) (TermFrameStatus, error) {
	f, err := peerwire.ParseMessage(raw, "FrameStatus")
	if err != nil {
		return TermFrameStatus{}, ErrTermInvalidControl
	}
	if len(f[2].B) != 32 || f[3].U < 1 || f[3].U > 6 {
		return TermFrameStatus{}, ErrTermInvalidControl
	}
	var h [32]byte
	copy(h[:], f[2].B)
	return TermFrameStatus{
		Seq:                f[1].U,
		PayloadHash:        h,
		Outcome:            TermOutcome(f[3].U),
		DefinitelyInjected: uint32(f[4].U),
		PossiblyInjected:   uint32(f[5].U),
		InputBytes:         uint32(f[6].U),
		FailureReason:      uint32(f[7].U),
	}, nil
}

// DecodeWatermark strictly decodes Watermark (including repeated FrameStatus field 4).
func DecodeWatermark(raw []byte) (TermWatermark, error) {
	norm, err := peerwire.NormalizeMessage(raw, "Watermark")
	if err != nil {
		return TermWatermark{}, ErrTermInvalidControl
	}
	var wm TermWatermark
	for len(norm) > 0 {
		num, typ, n := protowire.ConsumeTag(norm)
		if n < 0 {
			return TermWatermark{}, ErrTermInvalidControl
		}
		norm = norm[n:]
		switch num {
		case 1, 2, 3:
			if typ != protowire.VarintType {
				return TermWatermark{}, ErrTermInvalidControl
			}
			v, m := protowire.ConsumeVarint(norm)
			if m < 0 {
				return TermWatermark{}, ErrTermInvalidControl
			}
			norm = norm[m:]
			switch num {
			case 1:
				wm.Processed = v
			case 2:
				wm.InputSuccess = v
			case 3:
				wm.FailedInputSeq = v
			}
		case 4:
			if typ != protowire.BytesType || len(wm.Details) >= TermInputDetailMax {
				return TermWatermark{}, ErrTermInvalidControl
			}
			b, m := protowire.ConsumeBytes(norm)
			if m < 0 {
				return TermWatermark{}, ErrTermInvalidControl
			}
			norm = norm[m:]
			d, err := DecodeFrameStatus(b)
			if err != nil {
				return TermWatermark{}, err
			}
			wm.Details = append(wm.Details, d)
		default:
			return TermWatermark{}, ErrTermInvalidControl
		}
	}
	return wm, nil
}

// EncodeFlowState encodes FlowState.
func EncodeFlowState(fs TermFlowState) []byte {
	return peerwire.PB(
		peerwire.B(1, EncodeWatermark(fs.ToDaemon)),
		peerwire.B(2, EncodeWatermark(fs.ToClient)),
	)
}

// DecodeFlowState strictly decodes FlowState.
func DecodeFlowState(raw []byte) (TermFlowState, error) {
	if len(raw) == 0 {
		return TermFlowState{}, nil
	}
	f, err := peerwire.ParseMessage(raw, "FlowState")
	if err != nil {
		return TermFlowState{}, ErrTermInvalidControl
	}
	var fs TermFlowState
	if len(f[1].B) > 0 {
		wm, err := DecodeWatermark(f[1].B)
		if err != nil {
			return TermFlowState{}, err
		}
		fs.ToDaemon = wm
	}
	if len(f[2].B) > 0 {
		wm, err := DecodeWatermark(f[2].B)
		if err != nil {
			return TermFlowState{}, err
		}
		fs.ToClient = wm
	}
	return fs, nil
}

func boolU64(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// EncodeAttachPath encodes Control 2.
func EncodeAttachPath(m AttachPathMsg) []byte {
	return peerwire.PB(
		peerwire.B(1, []byte(m.Stream)),
		peerwire.B(2, []byte(m.Session)),
		peerwire.B(3, []byte(m.Sender)),
		peerwire.B(4, []byte(m.LegID)),
		peerwire.U(5, m.PathGeneration),
		peerwire.B(6, EncodeFlowState(m.State)),
	)
}

// DecodeAttachPath strictly decodes Control 2.
func DecodeAttachPath(raw []byte) (AttachPathMsg, error) {
	if _, err := peerwire.KnownPayload(2, ControlAttachPath, raw); err != nil {
		return AttachPathMsg{}, ErrTermInvalidControl
	}
	f, err := peerwire.ParseMessage(raw, "AttachPath")
	if err != nil {
		return AttachPathMsg{}, ErrTermInvalidControl
	}
	st, err := DecodeFlowState(f[6].B)
	if err != nil {
		return AttachPathMsg{}, err
	}
	return AttachPathMsg{
		Stream:         string(f[1].B),
		Session:        string(f[2].B),
		Sender:         string(f[3].B),
		LegID:          string(f[4].B),
		PathGeneration: f[5].U,
		State:          st,
	}, nil
}

// EncodeInputStatus encodes Control 3.
func EncodeInputStatus(m InputStatusMsg) []byte {
	return peerwire.PB(
		peerwire.B(1, []byte(m.Stream)),
		peerwire.B(2, m.QueryID),
		peerwire.U(3, m.FromSeq),
		peerwire.U(4, uint64(m.Limit)),
		peerwire.B(5, m.SnapshotToken),
	)
}

// DecodeInputStatus strictly decodes Control 3.
func DecodeInputStatus(raw []byte) (InputStatusMsg, error) {
	if _, err := peerwire.KnownPayload(2, ControlInputStatus, raw); err != nil {
		return InputStatusMsg{}, ErrTermInvalidControl
	}
	f, err := peerwire.ParseMessage(raw, "InputStatus")
	if err != nil {
		return InputStatusMsg{}, ErrTermInvalidControl
	}
	return InputStatusMsg{
		Stream:        string(f[1].B),
		QueryID:       append([]byte(nil), f[2].B...),
		FromSeq:       f[3].U,
		Limit:         uint32(f[4].U),
		SnapshotToken: append([]byte(nil), f[5].B...),
	}, nil
}

// EncodeInputStatusReply encodes Control 11.
func EncodeInputStatusReply(m InputStatusReplyMsg) []byte {
	return peerwire.PB(
		peerwire.B(1, []byte(m.Stream)),
		peerwire.B(2, m.QueryID),
		peerwire.U(3, boolU64(m.Available)),
		peerwire.B(4, m.SnapshotToken),
		peerwire.B(5, EncodeFlowState(m.State)),
		peerwire.U(6, m.NextSeq),
		peerwire.U(7, boolU64(m.Complete)),
	)
}

// DecodeInputStatusReply strictly decodes Control 11.
func DecodeInputStatusReply(raw []byte) (InputStatusReplyMsg, error) {
	if _, err := peerwire.KnownPayload(2, ControlInputStatusReply, raw); err != nil {
		return InputStatusReplyMsg{}, ErrTermInvalidControl
	}
	f, err := peerwire.ParseMessage(raw, "InputStatusReply")
	if err != nil {
		return InputStatusReplyMsg{}, ErrTermInvalidControl
	}
	st, err := DecodeFlowState(f[5].B)
	if err != nil {
		return InputStatusReplyMsg{}, err
	}
	return InputStatusReplyMsg{
		Stream:        string(f[1].B),
		QueryID:       append([]byte(nil), f[2].B...),
		Available:     f[3].U != 0,
		SnapshotToken: append([]byte(nil), f[4].B...),
		State:         st,
		NextSeq:       f[6].U,
		Complete:      f[7].U != 0,
	}, nil
}

// PaginateInputStatusReply splits an immutable frozen TermStatusSnapshot into bounded Control 11 pages.
// Every page carries the exact same SnapshotToken and frozen ToClient watermark (never re-reading live state).
func PaginateInputStatusReply(stream string, queryID []byte, snap TermStatusSnapshot, pageSize int) []InputStatusReplyMsg {
	if !snap.Available {
		return []InputStatusReplyMsg{{
			Stream:    stream,
			QueryID:   append([]byte(nil), queryID...),
			Available: false,
			Complete:  true,
		}}
	}
	if pageSize <= 0 || pageSize > 32 {
		pageSize = 32
	}
	details := snap.State.ToDaemon.Details
	if len(details) == 0 {
		return []InputStatusReplyMsg{{
			Stream:        stream,
			QueryID:       append([]byte(nil), queryID...),
			Available:     true,
			SnapshotToken: append([]byte(nil), snap.Token[:]...),
			State:         snap.State,
			NextSeq:       0,
			Complete:      true,
		}}
	}
	var pages []InputStatusReplyMsg
	for i := 0; i < len(details); i += pageSize {
		end := i + pageSize
		if end > len(details) {
			end = len(details)
		}
		last := end == len(details)
		var nextSeq uint64
		if !last {
			nextSeq = details[end].Seq
		}
		pageState := TermFlowState{
			ToDaemon: TermWatermark{
				Processed:      snap.State.ToDaemon.Processed,
				InputSuccess:   snap.State.ToDaemon.InputSuccess,
				FailedInputSeq: snap.State.ToDaemon.FailedInputSeq,
				Details:        append([]TermFrameStatus(nil), details[i:end]...),
			},
			ToClient: snap.State.ToClient,
		}
		pages = append(pages, InputStatusReplyMsg{
			Stream:        stream,
			QueryID:       append([]byte(nil), queryID...),
			Available:     true,
			SnapshotToken: append([]byte(nil), snap.Token[:]...),
			State:         pageState,
			NextSeq:       nextSeq,
			Complete:      last,
		})
	}
	return pages
}

// EncodeTermAck encodes a stream-level Control 4 Ack.
func EncodeTermAck(m TermAckMsg) []byte {
	return peerwire.PB(
		peerwire.B(1, []byte(m.Stream)),
		peerwire.U(2, uint64(m.Direction)),
		peerwire.B(3, EncodeWatermark(m.State)),
	)
}

// DecodeTermAck strictly decodes a stream-level Control 4 Ack (returns ok=false if it is a signal/control seq ACK).
func DecodeTermAck(raw []byte) (TermAckMsg, bool, error) {
	if _, err := peerwire.KnownPayload(2, ControlAck, raw); err != nil {
		return TermAckMsg{}, false, ErrTermInvalidControl
	}
	f, err := peerwire.ParseMessage(raw, "Ack")
	if err != nil {
		return TermAckMsg{}, false, ErrTermInvalidControl
	}
	if len(f[1].B) == 0 {
		return TermAckMsg{}, false, nil
	}
	wm, err := DecodeWatermark(f[3].B)
	if err != nil {
		return TermAckMsg{}, false, err
	}
	return TermAckMsg{
		Stream:    string(f[1].B),
		Direction: TermDirection(f[2].U),
		State:     wm,
	}, true, nil
}

// EncodeReplay encodes Control 5.
func EncodeReplay(m ReplayMsg) []byte {
	return peerwire.PB(
		peerwire.B(1, []byte(m.Stream)),
		peerwire.U(2, uint64(m.Direction)),
		peerwire.U(3, m.FromSeq),
		peerwire.U(4, m.ThroughSeq),
		peerwire.U(5, uint64(m.MaxBytes)),
		peerwire.U(6, uint64(m.MaxFrames)),
	)
}

// DecodeReplay strictly decodes Control 5.
func DecodeReplay(raw []byte) (ReplayMsg, error) {
	if _, err := peerwire.KnownPayload(2, ControlReplay, raw); err != nil {
		return ReplayMsg{}, ErrTermInvalidControl
	}
	f, err := peerwire.ParseMessage(raw, "Replay")
	if err != nil {
		return ReplayMsg{}, ErrTermInvalidControl
	}
	return ReplayMsg{
		Stream:     string(f[1].B),
		Direction:  TermDirection(f[2].U),
		FromSeq:    f[3].U,
		ThroughSeq: f[4].U,
		MaxBytes:   uint32(f[5].U),
		MaxFrames:  uint32(f[6].U),
	}, nil
}

// EncodeBarrier encodes Control 6.
func EncodeBarrier(m BarrierMsg) []byte {
	return peerwire.PB(
		peerwire.B(1, []byte(m.Stream)),
		peerwire.B(2, m.BarrierID),
		peerwire.B(3, EncodeFlowState(m.Requested)),
		peerwire.U(4, boolU64(m.StopAdmission)),
		peerwire.B(5, []byte(m.NewLegID)),
		peerwire.U(6, m.PathGeneration),
	)
}

// DecodeBarrier strictly decodes Control 6.
func DecodeBarrier(raw []byte) (BarrierMsg, error) {
	if _, err := peerwire.KnownPayload(2, ControlBarrier, raw); err != nil {
		return BarrierMsg{}, ErrTermInvalidControl
	}
	f, err := peerwire.ParseMessage(raw, "Barrier")
	if err != nil {
		return BarrierMsg{}, ErrTermInvalidControl
	}
	req, err := DecodeFlowState(f[3].B)
	if err != nil {
		return BarrierMsg{}, err
	}
	return BarrierMsg{
		Stream:         string(f[1].B),
		BarrierID:      append([]byte(nil), f[2].B...),
		Requested:      req,
		StopAdmission:  f[4].U != 0,
		NewLegID:       string(f[5].B),
		PathGeneration: f[6].U,
	}, nil
}

// EncodeBarrierReply encodes Control 12.
func EncodeBarrierReply(m BarrierReplyMsg) []byte {
	return peerwire.PB(
		peerwire.B(1, []byte(m.Stream)),
		peerwire.B(2, m.BarrierID),
		peerwire.B(3, EncodeFlowState(m.State)),
		peerwire.U(4, boolU64(m.Final)),
		peerwire.U(5, boolU64(m.Accepted)),
	)
}

// DecodeBarrierReply strictly decodes Control 12.
func DecodeBarrierReply(raw []byte) (BarrierReplyMsg, error) {
	if _, err := peerwire.KnownPayload(2, ControlBarrierReply, raw); err != nil {
		return BarrierReplyMsg{}, ErrTermInvalidControl
	}
	f, err := peerwire.ParseMessage(raw, "BarrierReply")
	if err != nil {
		return BarrierReplyMsg{}, ErrTermInvalidControl
	}
	st, err := DecodeFlowState(f[3].B)
	if err != nil {
		return BarrierReplyMsg{}, err
	}
	return BarrierReplyMsg{
		Stream:    string(f[1].B),
		BarrierID: append([]byte(nil), f[2].B...),
		State:     st,
		Final:     f[4].U != 0,
		Accepted:  f[5].U != 0,
	}, nil
}

// EncodeStreamFailed encodes Control 7.
func EncodeStreamFailed(m StreamFailedMsg) []byte {
	return peerwire.PB(
		peerwire.B(1, []byte(m.Stream)),
		peerwire.B(2, EncodeFrameStatus(m.InputFailure)),
		peerwire.B(3, EncodeFlowState(m.State)),
	)
}

// DecodeStreamFailed strictly decodes Control 7.
func DecodeStreamFailed(raw []byte) (StreamFailedMsg, error) {
	if _, err := peerwire.KnownPayload(2, ControlStreamFailed, raw); err != nil {
		return StreamFailedMsg{}, ErrTermInvalidControl
	}
	f, err := peerwire.ParseMessage(raw, "StreamFailed")
	if err != nil {
		return StreamFailedMsg{}, ErrTermInvalidControl
	}
	failSt, err := DecodeFrameStatus(f[2].B)
	if err != nil {
		return StreamFailedMsg{}, err
	}
	st, err := DecodeFlowState(f[3].B)
	if err != nil {
		return StreamFailedMsg{}, err
	}
	return StreamFailedMsg{
		Stream:       string(f[1].B),
		InputFailure: failSt,
		State:        st,
	}, nil
}

// EncodeClosePath encodes Control 8.
func EncodeClosePath(m ClosePathMsg) []byte {
	return peerwire.PB(
		peerwire.B(1, []byte(m.Stream)),
		peerwire.B(2, []byte(m.LegID)),
		peerwire.U(3, m.PathGeneration),
		peerwire.U(4, uint64(m.Reason)),
	)
}

// DecodeClosePath strictly decodes Control 8.
func DecodeClosePath(raw []byte) (ClosePathMsg, error) {
	if _, err := peerwire.KnownPayload(2, ControlClosePath, raw); err != nil {
		return ClosePathMsg{}, ErrTermInvalidControl
	}
	f, err := peerwire.ParseMessage(raw, "ClosePath")
	if err != nil {
		return ClosePathMsg{}, ErrTermInvalidControl
	}
	return ClosePathMsg{
		Stream:         string(f[1].B),
		LegID:          string(f[2].B),
		PathGeneration: f[3].U,
		Reason:         uint32(f[4].U),
	}, nil
}
