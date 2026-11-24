package linkcore

import (
	"bytes"
	"crypto/sha256"
	"errors"
)

// Terminal continuity budgets and limits (docs/transport.md §3, docs/transport-wire-draft.md).
const (
	TermGapMaxFrames               = 128
	TermGapMaxBytes                = 1 << 20 // 1 MiB per direction
	TermGapTimeoutMs        uint64 = 10_000  // 10 s per gap
	TermAckBatchFrames             = 16
	TermAckIntervalMs       uint64 = 20 // 20 ms
	TermOutputReplayMaxFrames      = 512
	TermOutputReplayMaxBytes       = 2 << 20 // 2 MiB
	TermOutputReplayMaxMs   uint64 = 30_000  // 30 s
	TermOutputMaxIdentities        = 512
	TermInputCacheMaxFrames        = 128
	TermInputCacheMaxBytes         = 64 << 10 // 64 KiB
	TermInputCacheMaxMs     uint64 = 30_000   // 30 s (3x direct 10s recovery target)
	TermInputDetailMax             = 128
	TermHotStandbyMaxBytes         = 4 << 10  // 4 KiB per frame
	TermHotStandbyRateBPS          = 32 << 10 // 32 KiB/s
	TermHotStandbyBurst            = 64 << 10 // 64 KiB burst
	TermMaxActiveStreams           = 16
	TermMaxRetiredStreams          = 16
	TermRetiredRetainMs     uint64 = 30_000 // 30 s retired query window
	TermMaxClientStreams           = 8
	TermBarrierTimeoutMs    uint64 = 5_000 // 5 s barrier wait
)

var (
	ErrTermStopped          = errors.New("terminal_stream_stopped")
	ErrTermConflict         = errors.New("terminal_payload_conflict")
	ErrTermExpired          = errors.New("terminal_frame_expired")
	ErrTermBudget           = errors.New("terminal_budget_exceeded")
	ErrTermReplayWindow     = errors.New("terminal_output_replay_window")
	ErrTermAttachBinding    = errors.New("terminal_attach_binding")
	ErrTermIdentityUnknown  = errors.New("terminal_output_identity_unavailable")
	ErrTermRegistryFull     = errors.New("terminal_registry_full")
	ErrTermNotStopped       = errors.New("terminal_retire_without_barrier")
	ErrTermInvalidControl   = errors.New("terminal_invalid_control")
)

// TermOutcome matches FrameOutcome in proto/remoteagent/v1/transport.proto.
type TermOutcome uint32

const (
	TermOutcomeUnspecified         TermOutcome = 0
	TermOutcomeApplied             TermOutcome = 1
	TermOutcomeNonInputFailed      TermOutcome = 2
	TermOutcomeInputPartialUnknown TermOutcome = 3
	TermOutcomeInputBlocked        TermOutcome = 4
	TermOutcomeNotApplied          TermOutcome = 5
	TermOutcomeExpiredNotApplied   TermOutcome = 6
)

// TermDirection matches Direction in proto/remoteagent/v1/transport.proto.
type TermDirection uint32

const (
	TermDirUnspecified TermDirection = 0
	TermDirToDaemon    TermDirection = 1
	TermDirToClient    TermDirection = 2
)

// TermFrameStatus matches FrameStatus in proto/remoteagent/v1/transport.proto.
type TermFrameStatus struct {
	Seq                uint64
	PayloadHash        [32]byte
	Outcome            TermOutcome
	DefinitelyInjected uint32
	PossiblyInjected   uint32
	InputBytes         uint32
	FailureReason      uint32
}

// TermWatermark matches Watermark in proto/remoteagent/v1/transport.proto.
type TermWatermark struct {
	Processed      uint64
	InputSuccess   uint64
	FailedInputSeq uint64
	Details        []TermFrameStatus
}

// TermFlowState matches FlowState in proto/remoteagent/v1/transport.proto.
type TermFlowState struct {
	ToDaemon TermWatermark
	ToClient TermWatermark
}

// TermFrame represents an authenticated logical terminal frame bound to one stream.
type TermFrame struct {
	Seq         uint64
	PayloadHash [32]byte // RequestIdentity original authorized payload SHA-256
	DeadlineMs  uint64
	RawBytes    int // complete retained encoded envelope bytes
	IsInput     bool
	InputBytes  uint32
	PlainBytes  []byte // original business plaintext (preserved for re-encryption/re-auth)
	RawEnvelope []byte // frozen sealed envelope bytes for dual-send/replay
}

// TermStatusSnapshot is an immutable bidirectional FlowState frozen at a serialized query or barrier point.
type TermStatusSnapshot struct {
	Available bool
	Final     bool
	Token     [32]byte
	State     TermFlowState
	FrozenAt  uint64
}

// TermInputAction is the daemon-side decision after ReceiveInput.
type TermInputAction uint8

const (
	TermInputReject TermInputAction = iota
	TermInputRun
	TermInputBuffered
	TermInputDuplicateACK
)

// TermOutputAction is the client-side decision after ReceiveOutput.
type TermOutputAction uint8

const (
	TermOutputReject TermOutputAction = iota
	TermOutputReady
	TermOutputBuffered
	TermOutputDuplicateACK
)

type termLeg struct {
	legID string
	gen   uint64
}

type termQueued struct {
	frame     TermFrame
	firstSeen uint64
}

type termOutputIdentity struct {
	hash     [32]byte
	deadline uint64
}

type termReplayItem struct {
	frame      TermFrame
	recordedAt uint64
}

type termInputCacheItem struct {
	frame    TermFrame
	cachedAt uint64
	charge   int
}

type termWaitingQuery struct {
	queryID  string
	deadline uint64
}

// TerminalStream is the shared sans-IO logical terminal continuity state machine
// used by both daemon and client (docs/transport.md §3, docs/transport-wire-draft.md).
//
// It owns:
//   - physical leg generation binding (old leg callbacks cannot close newer legs)
//   - independent input/output out-of-order reassembly buffers (128 frames / 1 MiB, 10s gap deadline)
//   - duplicate same-hash ACK vs conflict rejection (never closes on identical duplicate)
//   - daemon-side Input fail-stop (cannot be bypassed by any leg; Resize/History failure never fail-stops)
//   - ACK batching (20ms / 16 frames, immediate on fail-stop)
//   - daemon-side output replay buffer (2 MiB / 512 frames / 30s) and hot-standby mirror bucket (4 KiB, 32 KiB/s, 64 KiB burst)
//   - client-side bounded input cache (64 KiB / 30s / 128 frames), InputStatus reconciliation, Barrier gating, and user-confirmation gating
type TerminalStream struct {
	StreamID  string
	SessionID string
	PeerID    string
	Instance  string // daemon process instance ID

	legs map[string]termLeg // keyed by path name ("relay", "direct")

	// Input direction (client -> daemon):
	processed       uint64
	inputSuccess    uint64
	failedInputSeq  uint64
	details         map[uint64]TermFrameStatus
	inflight        *TermFrame
	inputPending    map[uint64]termQueued
	inputPendBytes  int
	inputGapDue     uint64

	// Output direction (daemon -> client):
	rendered         uint64
	outputPending    map[uint64]termQueued
	outputPendBytes  int
	outputGapDue     uint64
	renderedIDs      map[uint64]termOutputIdentity
	renderReady      *TermFrame
	clientAckOutput  uint64 // daemon's view of client's rendered output watermark

	// Daemon output replay & hot-standby rate limiter:
	replay        []termReplayItem
	replayBytes   int
	mirrorBucket  float64
	mirrorLastMs  int64
	mirrorInited  bool

	// Client input cache & recovery state:
	inputCache      map[uint64]termInputCacheItem
	inputCacheBytes int

	// ACK batching:
	dirtyACK int
	lastACK  uint64

	// Serialized queries and barrier:
	waitingQueries []termWaitingQuery
	barrierPending bool
	barrierID      string

	// Lifecycle & recovery flags:
	Stopped          bool
	RetiredAt        uint64
	NetworkRecovery  bool
	Paused           bool
	NeedsUserConfirm bool
	FailStopDetail   *TermFrameStatus
}

// NewTerminalStream initializes a logical terminal stream bound to daemon instance.
func NewTerminalStream(streamID, sessionID, peerID, instance string, now uint64) *TerminalStream {
	return &TerminalStream{
		StreamID:     streamID,
		SessionID:    sessionID,
		PeerID:       peerID,
		Instance:     instance,
		legs:         map[string]termLeg{},
		details:      map[uint64]TermFrameStatus{},
		inputPending: map[uint64]termQueued{},
		outputPending: map[uint64]termQueued{},
		renderedIDs:  map[uint64]termOutputIdentity{},
		inputCache:   map[uint64]termInputCacheItem{},
		lastACK:      now,
	}
}

// AttachLeg binds or replaces a physical leg (e.g. "relay" or "direct") with strictly increasing generation.
func (s *TerminalStream) AttachLeg(path, legID, instance string, gen uint64) error {
	if s.Stopped || instance != s.Instance || path == "" || gen == 0 {
		return ErrTermAttachBinding
	}
	if cur, ok := s.legs[path]; ok && gen <= cur.gen {
		return ErrTermAttachBinding
	}
	s.legs[path] = termLeg{legID: legID, gen: gen}
	return nil
}

// CloseLeg detaches path only if its generation matches; stale callbacks from replaced legs are ignored.
// Returns true if no legs remain and the stream enters network recovery.
func (s *TerminalStream) CloseLeg(path string, gen uint64) bool {
	cur, ok := s.legs[path]
	if !ok || cur.gen != gen {
		return false
	}
	delete(s.legs, path)
	if len(s.legs) == 0 && !s.Stopped {
		s.NetworkRecovery = true
		return true
	}
	return false
}

// HasLeg reports whether path currently has an attached leg.
func (s *TerminalStream) HasLeg(path string) bool {
	_, ok := s.legs[path]
	return ok
}

// LegGen returns the active generation for path, or 0 if detached.
func (s *TerminalStream) LegGen(path string) uint64 {
	return s.legs[path].gen
}

// LegID returns the active leg ID for path, or "" if detached.
func (s *TerminalStream) LegID(path string) string {
	return s.legs[path].legID
}

// Processed returns the highest contiguous processed input sequence number.
func (s *TerminalStream) Processed() uint64 { return s.processed }

// InputSuccess returns the highest contiguous successfully injected Input sequence number.
func (s *TerminalStream) InputSuccess() uint64 { return s.inputSuccess }

// FailedInputSeq returns the fail-stop input sequence number, or 0 if no Input fail-stop occurred.
func (s *TerminalStream) FailedInputSeq() uint64 { return s.failedInputSeq }

// Rendered returns the highest contiguous rendered output sequence number (client side).
func (s *TerminalStream) Rendered() uint64 { return s.rendered }

// InflightSeq returns the currently executing input sequence number, or 0 if none.
func (s *TerminalStream) InflightSeq() uint64 {
	if s.inflight == nil {
		return 0
	}
	return s.inflight.Seq
}

// InputCacheLen returns the number of unacknowledged cached client inputs.
func (s *TerminalStream) InputCacheLen() int { return len(s.inputCache) }

// InputCacheBytes returns the retained client input cache bytes.
func (s *TerminalStream) InputCacheBytes() int { return s.inputCacheBytes }

// ReplayLen returns the number of frames in the daemon output replay buffer.
func (s *TerminalStream) ReplayLen() int { return len(s.replay) }

// ReplayBytes returns the bytes held in the daemon output replay buffer.
func (s *TerminalStream) ReplayBytes() int { return s.replayBytes }

// OutputGapDue returns the current output gap deadline (0 if no missing output gap).
func (s *TerminalStream) OutputGapDue() uint64 { return s.outputGapDue }

// InputGapDue returns the current input gap deadline (0 if no missing input gap).
func (s *TerminalStream) InputGapDue() uint64 { return s.inputGapDue }

// NoteClientOutputAck updates the daemon's record of the client's rendered output watermark.
func (s *TerminalStream) NoteClientOutputAck(rendered uint64) {
	if rendered > s.clientAckOutput {
		s.clientAckOutput = rendered
	}
}

// PruneConfirmedDetails reclaims completed, non-failed details up to confirmedSeq once
// the client has confirmed receipt (e.g. via ACK or InputStatus), keeping unconfirmed
// and failed details intact so conservative backpressure still holds when unacknowledged.
func (s *TerminalStream) PruneConfirmedDetails(confirmedSeq uint64) {
	if s.failedInputSeq != 0 || confirmedSeq == 0 {
		return
	}
	for seq, d := range s.details {
		if seq <= confirmedSeq && (d.Outcome == TermOutcomeApplied || d.Outcome == TermOutcomeNonInputFailed) {
			delete(s.details, seq)
		}
	}
}

// ReceiveInput validates, deduplicates, and reorders an authenticated client->daemon frame BEFORE Accept/tmux.
func (s *TerminalStream) ReceiveInput(now uint64, f TermFrame) (TermInputAction, *TermFrame, error) {
	if s.Stopped {
		return TermInputReject, nil, ErrTermStopped
	}
	if f.Seq == 0 || (f.DeadlineMs > 0 && now >= f.DeadlineMs) {
		return TermInputReject, nil, ErrTermExpired
	}
	if old, ok := s.details[f.Seq]; ok {
		if old.PayloadHash != f.PayloadHash {
			return TermInputReject, nil, ErrTermConflict
		}
		return TermInputDuplicateACK, nil, nil
	}
	if s.inflight != nil && s.inflight.Seq == f.Seq {
		if s.inflight.PayloadHash != f.PayloadHash {
			return TermInputReject, nil, ErrTermConflict
		}
		return TermInputBuffered, nil, nil
	}
	if p, ok := s.inputPending[f.Seq]; ok {
		if p.frame.PayloadHash != f.PayloadHash {
			return TermInputReject, nil, ErrTermConflict
		}
		delta := f.RawBytes - p.frame.RawBytes
		if s.inputPendBytes+delta > TermGapMaxBytes {
			return TermInputReject, nil, ErrTermBudget
		}
		s.inputPendBytes += delta
		p.frame = f
		s.inputPending[f.Seq] = p
		s.refreshInputGap()
		return TermInputBuffered, nil, nil
	}
	if f.Seq != s.processed+1 || s.inflight != nil {
		if len(s.inputPending) >= TermGapMaxFrames || s.inputPendBytes+f.RawBytes > TermGapMaxBytes {
			return TermInputReject, nil, ErrTermBudget
		}
		s.inputPending[f.Seq] = termQueued{frame: f, firstSeen: now}
		s.inputPendBytes += f.RawBytes
		s.refreshInputGap()
		return TermInputBuffered, nil, nil
	}
	if len(s.details) >= TermInputDetailMax {
		return TermInputReject, nil, ErrTermBudget
	}
	cp := f
	s.inflight = &cp
	return TermInputRun, &cp, nil
}

// IsInputBlocked reports whether a newly admitted frame must be immediately rejected as INPUT_BLOCKED
// because a prior TerminalInput failed (fail-stop cannot be bypassed by any leg).
func (s *TerminalStream) IsInputBlocked(f TermFrame) bool {
	return f.IsInput && s.failedInputSeq != 0
}

// CompleteInput settles the currently executing input frame, updates watermarks and fail-stop state,
// resolves any serialized InputStatus queries or Barrier, and pops the next contiguous ready frame if any.
func (s *TerminalStream) CompleteInput(
	now uint64,
	seq uint64,
	outcome TermOutcome,
	definitelyInjected, possiblyInjected, failureReason uint32,
) (
	nextReady *TermFrame,
	emitACK bool,
	failStop *TermFrameStatus,
	resolvedQueries []struct {
		QueryID  string
		Snapshot TermStatusSnapshot
	},
	resolvedBarrier *struct {
		BarrierID string
		Snapshot  TermStatusSnapshot
	},
) {
	if s.inflight == nil || s.inflight.Seq != seq {
		return nil, false, nil, nil, nil
	}
	cur := *s.inflight
	s.inflight = nil

	if cur.IsInput && s.failedInputSeq != 0 {
		outcome = TermOutcomeInputBlocked
		definitelyInjected = 0
		possiblyInjected = 0
	}
	detail := TermFrameStatus{
		Seq:                cur.Seq,
		PayloadHash:        cur.PayloadHash,
		Outcome:            outcome,
		DefinitelyInjected: definitelyInjected,
		PossiblyInjected:   possiblyInjected,
		InputBytes:         cur.InputBytes,
		FailureReason:      failureReason,
	}
	if cur.IsInput && outcome == TermOutcomeApplied {
		if detail.DefinitelyInjected == 0 && cur.InputBytes > 0 {
			detail.DefinitelyInjected = cur.InputBytes
		}
		if detail.PossiblyInjected == 0 && cur.InputBytes > 0 {
			detail.PossiblyInjected = cur.InputBytes
		}
		s.inputSuccess = cur.Seq
	}
	if cur.IsInput && outcome == TermOutcomeInputPartialUnknown {
		if s.failedInputSeq == 0 {
			s.failedInputSeq = cur.Seq
		}
		dCopy := detail
		s.FailStopDetail = &dCopy
		failStop = &dCopy
		emitACK = true
	}

	s.processed = cur.Seq
	s.details[cur.Seq] = detail
	s.dirtyACK++
	if s.dirtyACK >= TermAckBatchFrames || emitACK {
		emitACK = true
		s.dirtyACK = 0
		s.lastACK = now
	}

	if len(s.waitingQueries) > 0 {
		for _, q := range s.waitingQueries {
			if q.deadline > 0 && now >= q.deadline {
				continue
			}
			snap := s.FreezeSnapshot(now, false)
			resolvedQueries = append(resolvedQueries, struct {
				QueryID  string
				Snapshot TermStatusSnapshot
			}{QueryID: q.queryID, Snapshot: snap})
		}
		s.waitingQueries = nil
	}

	if s.barrierPending {
		s.barrierPending = false
		bid := s.barrierID
		s.barrierID = ""
		s.Stopped = true
		s.RetiredAt = now
		snap := s.FreezeSnapshot(now, true)
		resolvedBarrier = &struct {
			BarrierID string
			Snapshot  TermStatusSnapshot
		}{BarrierID: bid, Snapshot: snap}
		return nil, emitACK, failStop, resolvedQueries, resolvedBarrier
	}

	if next, ok := s.inputPending[s.processed+1]; ok {
		delete(s.inputPending, s.processed+1)
		s.inputPendBytes -= next.frame.RawBytes
		s.refreshInputGap()
		if next.frame.DeadlineMs > 0 && now >= next.frame.DeadlineMs {
			s.NetworkRecovery = true
		} else if len(s.details) < TermInputDetailMax {
			cp := next.frame
			s.inflight = &cp
			nextReady = &cp
		}
	} else {
		s.refreshInputGap()
	}
	return nextReady, emitACK, failStop, resolvedQueries, resolvedBarrier
}

// QueryStatus serializes an InputStatus request behind any in-flight input.
// Returns (snap, true) immediately if idle, or (zero, false) if queued until CompleteInput.
func (s *TerminalStream) QueryStatus(now uint64, queryID string, deadline uint64) (TermStatusSnapshot, bool) {
	if s.inflight != nil {
		s.waitingQueries = append(s.waitingQueries, termWaitingQuery{queryID: queryID, deadline: deadline})
		return TermStatusSnapshot{}, false
	}
	return s.FreezeSnapshot(now, s.Stopped), true
}

// RequestBarrier serializes a stop-admission barrier behind any in-flight input.
// Returns (snap, true) immediately if idle (stopping admission now), or (zero, false) if queued.
func (s *TerminalStream) RequestBarrier(now uint64, barrierID string, stopAdmission bool) (TermStatusSnapshot, bool) {
	if !stopAdmission {
		if s.inflight != nil {
			return TermStatusSnapshot{}, false
		}
		return s.FreezeSnapshot(now, s.Stopped), true
	}
	if s.inflight != nil {
		s.barrierPending = true
		s.barrierID = barrierID
		return TermStatusSnapshot{}, false
	}
	s.Stopped = true
	s.RetiredAt = now
	return s.FreezeSnapshot(now, true), true
}

// FreezeSnapshot captures an immutable bidirectional FlowState and binds its SHA-256 snapshot token.
func (s *TerminalStream) FreezeSnapshot(now uint64, final bool) TermStatusSnapshot {
	details := make([]TermFrameStatus, 0, len(s.details))
	for _, d := range s.details {
		details = append(details, d)
	}
	sortFrameStatuses(details)
	outProcessed := s.rendered
	if s.clientAckOutput > outProcessed {
		outProcessed = s.clientAckOutput
	}
	state := TermFlowState{
		ToDaemon: TermWatermark{
			Processed:      s.processed,
			InputSuccess:   s.inputSuccess,
			FailedInputSeq: s.failedInputSeq,
			Details:        details,
		},
		ToClient: TermWatermark{
			Processed: outProcessed,
		},
	}
	token := FlowStateToken(state)
	return TermStatusSnapshot{
		Available: true,
		Final:     final,
		Token:     token,
		State:     state,
		FrozenAt:  now,
	}
}

func sortFrameStatuses(items []TermFrameStatus) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].Seq < items[j-1].Seq; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// CurrentInputWatermark returns the current to_daemon watermark (without full details, for Ack frames).
func (s *TerminalStream) CurrentInputWatermark() TermWatermark {
	return TermWatermark{
		Processed:      s.processed,
		InputSuccess:   s.inputSuccess,
		FailedInputSeq: s.failedInputSeq,
	}
}

// CurrentOutputWatermark returns the current to_client rendered watermark (for client->daemon Ack frames).
func (s *TerminalStream) CurrentOutputWatermark() TermWatermark {
	return TermWatermark{
		Processed: s.rendered,
	}
}

// ReceiveOutput validates, deduplicates, and reorders a daemon->client output frame.
func (s *TerminalStream) ReceiveOutput(now uint64, f TermFrame) (TermOutputAction, *TermFrame, error) {
	s.pruneOutputIdentities(now)
	if f.Seq == 0 || (f.DeadlineMs > 0 && now >= f.DeadlineMs) {
		return TermOutputReject, nil, ErrTermExpired
	}
	if f.Seq <= s.rendered {
		old, ok := s.renderedIDs[f.Seq]
		if !ok {
			return TermOutputReject, nil, ErrTermIdentityUnknown
		}
		if old.hash != f.PayloadHash {
			return TermOutputReject, nil, ErrTermConflict
		}
		s.dirtyACK++
		return TermOutputDuplicateACK, nil, nil
	}
	if old, ok := s.outputPending[f.Seq]; ok {
		if old.frame.PayloadHash != f.PayloadHash {
			return TermOutputReject, nil, ErrTermConflict
		}
		return TermOutputBuffered, nil, nil
	}
	if len(s.outputPending) >= TermGapMaxFrames ||
		len(s.outputPending)+len(s.renderedIDs) >= TermOutputMaxIdentities ||
		f.RawBytes > TermGapMaxBytes-s.outputPendBytes {
		return TermOutputReject, nil, ErrTermBudget
	}
	s.outputPending[f.Seq] = termQueued{frame: f, firstSeen: now}
	s.outputPendBytes += f.RawBytes
	s.refreshOutputGap()
	if s.renderReady == nil {
		if next, ok := s.outputPending[s.rendered+1]; ok {
			cp := next.frame
			s.renderReady = &cp
			return TermOutputReady, &cp, nil
		}
	}
	return TermOutputBuffered, nil, nil
}

// ConfirmRendered marks the head contiguous output frame rendered, advancing output ACK watermark.
func (s *TerminalStream) ConfirmRendered(now uint64, seq uint64) (nextReady *TermFrame, emitACK bool, err error) {
	if s.renderReady == nil || s.renderReady.Seq != seq {
		return nil, false, nil
	}
	cur := *s.renderReady
	if cur.DeadlineMs > 0 && now >= cur.DeadlineMs {
		s.NetworkRecovery = true
		return nil, false, ErrTermExpired
	}
	s.rendered = cur.Seq
	s.renderedIDs[cur.Seq] = termOutputIdentity{hash: cur.PayloadHash, deadline: cur.DeadlineMs}
	s.outputPendBytes -= cur.RawBytes
	delete(s.outputPending, cur.Seq)
	s.renderReady = nil
	s.refreshOutputGap()

	s.dirtyACK++
	if s.dirtyACK >= TermAckBatchFrames {
		emitACK = true
		s.dirtyACK = 0
		s.lastACK = now
	}
	if next, ok := s.outputPending[s.rendered+1]; ok {
		cp := next.frame
		s.renderReady = &cp
		nextReady = &cp
	}
	return nextReady, emitACK, nil
}

// ShouldMirrorOutput implements the docs/transport.md §3 output hot-standby rule:
// ciphertext <= 4 KiB AND per-stream rate <= 32 KiB/s (burst 64 KiB).
func (s *TerminalStream) ShouldMirrorOutput(nowMs int64, ciphertextBytes int) bool {
	if ciphertextBytes <= 0 || ciphertextBytes > TermHotStandbyMaxBytes {
		return false
	}
	if !s.mirrorInited {
		s.mirrorBucket = float64(TermHotStandbyBurst)
		s.mirrorLastMs = nowMs
		s.mirrorInited = true
	}
	if nowMs > s.mirrorLastMs {
		elapsedSec := float64(nowMs-s.mirrorLastMs) / 1000.0
		s.mirrorBucket += elapsedSec * float64(TermHotStandbyRateBPS)
		if s.mirrorBucket > float64(TermHotStandbyBurst) {
			s.mirrorBucket = float64(TermHotStandbyBurst)
		}
		s.mirrorLastMs = nowMs
	}
	need := float64(ciphertextBytes)
	if s.mirrorBucket < need {
		return false
	}
	s.mirrorBucket -= need
	return true
}

// RecordOutputReplay stores a sealed output frame in the bounded 2 MiB / 512 frames / 30s replay buffer.
func (s *TerminalStream) RecordOutputReplay(now uint64, f TermFrame) {
	s.replay = append(s.replay, termReplayItem{frame: f, recordedAt: now})
	s.replayBytes += len(f.RawEnvelope)
	s.pruneReplay(now)
}

// ReplayOutput returns sealed output frames in [fromSeq, throughSeq] within maxBytes/maxFrames.
// Window overflow returns ErrTermReplayWindow and sets NetworkRecovery (never Input fail-stop).
func (s *TerminalStream) ReplayOutput(now uint64, fromSeq, throughSeq uint64, maxBytes, maxFrames uint32) ([]TermFrame, error) {
	s.pruneReplay(now)
	if s.Stopped || len(s.replay) == 0 || fromSeq == 0 || fromSeq < s.replay[0].frame.Seq {
		s.NetworkRecovery = true
		return nil, ErrTermReplayWindow
	}
	var out []TermFrame
	var totalBytes uint32
	for _, item := range s.replay {
		f := item.frame
		if f.Seq < fromSeq {
			continue
		}
		if throughSeq > 0 && f.Seq > throughSeq {
			break
		}
		if f.DeadlineMs > 0 && now >= f.DeadlineMs {
			s.NetworkRecovery = true
			return nil, ErrTermExpired
		}
		if maxFrames > 0 && uint32(len(out)) >= maxFrames {
			break
		}
		if maxBytes > 0 && totalBytes+uint32(len(f.RawEnvelope)) > maxBytes {
			break
		}
		out = append(out, f)
		totalBytes += uint32(len(f.RawEnvelope))
	}
	return out, nil
}

// CacheInput records a client->daemon frame in the bounded 64 KiB / 30s / 128-frame input cache.
func (s *TerminalStream) CacheInput(now uint64, f TermFrame) error {
	if f.DeadlineMs > 0 && now >= f.DeadlineMs {
		return ErrTermExpired
	}
	charge := len(f.RawEnvelope) + len(f.PlainBytes) + 128
	if s.Paused || s.NeedsUserConfirm || s.inputCacheBytes+charge > TermInputCacheMaxBytes || len(s.inputCache) >= TermInputCacheMaxFrames {
		s.Paused = true
		return ErrTermBudget
	}
	s.inputCache[f.Seq] = termInputCacheItem{frame: f, cachedAt: now, charge: charge}
	s.inputCacheBytes += charge
	return nil
}

// ApplyInputAck evicts confirmed inputs up to inputSuccess (or details) from the client input cache.
// If failedInputSeq != 0, activates fail-stop confirmation gating immediately.
func (s *TerminalStream) ApplyInputAck(wm TermWatermark) (failStopTriggered bool) {
	if wm.FailedInputSeq != 0 {
		s.failedInputSeq = wm.FailedInputSeq
		s.Paused = true
		s.NeedsUserConfirm = true
		failStopTriggered = true
	}
	for seq, item := range s.inputCache {
		if seq <= wm.InputSuccess {
			s.inputCacheBytes -= item.charge
			delete(s.inputCache, seq)
		}
	}
	for _, d := range wm.Details {
		if item, ok := s.inputCache[d.Seq]; ok && item.frame.PayloadHash == d.PayloadHash {
			if d.Outcome == TermOutcomeApplied || d.Outcome == TermOutcomeNonInputFailed {
				s.inputCacheBytes -= item.charge
				delete(s.inputCache, d.Seq)
			} else if d.Outcome == TermOutcomeInputPartialUnknown {
				s.failedInputSeq = d.Seq
				dCopy := d
				s.FailStopDetail = &dCopy
				s.Paused = true
				s.NeedsUserConfirm = true
				failStopTriggered = true
			}
		}
	}
	return failStopTriggered
}

// ReconcileStatus compares client's cached inputs against an authenticated daemon InputStatusReply.
// Returns inputs to resend in ascending seq order on the old stream, whether a stop-admission Barrier
// is required before opening a new stream, and whether user confirmation is required (fail-stop / unavailable).
func (s *TerminalStream) ReconcileStatus(now uint64, snap TermStatusSnapshot) (toResend []TermFrame, needBarrier bool, needUserConfirm bool) {
	if !snap.Available {
		s.Paused = true
		s.NeedsUserConfirm = true
		return nil, false, true
	}
	if snap.State.ToDaemon.FailedInputSeq != 0 {
		s.failedInputSeq = snap.State.ToDaemon.FailedInputSeq
		s.Paused = true
		s.NeedsUserConfirm = true
		needUserConfirm = true
	}
	bySeq := make(map[uint64]TermFrameStatus, len(snap.State.ToDaemon.Details))
	for _, d := range snap.State.ToDaemon.Details {
		bySeq[d.Seq] = d
	}

	seqs := make([]uint64, 0, len(s.inputCache))
	for seq := range s.inputCache {
		seqs = append(seqs, seq)
	}
	for i := 1; i < len(seqs); i++ {
		for j := i; j > 0 && seqs[j] < seqs[j-1]; j-- {
			seqs[j], seqs[j-1] = seqs[j-1], seqs[j]
		}
	}

	for _, seq := range seqs {
		item := s.inputCache[seq]
		if d, ok := bySeq[seq]; ok {
			if d.PayloadHash != item.frame.PayloadHash {
				s.Paused = true
				s.NeedsUserConfirm = true
				return nil, false, true
			}
			switch d.Outcome {
			case TermOutcomeApplied, TermOutcomeNonInputFailed:
				s.inputCacheBytes -= item.charge
				delete(s.inputCache, seq)
			case TermOutcomeInputPartialUnknown:
				s.failedInputSeq = d.Seq
				dCopy := d
				s.FailStopDetail = &dCopy
				s.Paused = true
				s.NeedsUserConfirm = true
				needUserConfirm = true
			case TermOutcomeInputBlocked:
				s.Paused = true
				s.NeedsUserConfirm = true
				needUserConfirm = true
			case TermOutcomeExpiredNotApplied:
				needBarrier = true
			case TermOutcomeNotApplied:
				if item.frame.DeadlineMs > 0 && now >= item.frame.DeadlineMs {
					needBarrier = true
				} else if !needUserConfirm && !needBarrier {
					toResend = append(toResend, item.frame)
				}
			}
			continue
		}
		if seq <= snap.State.ToDaemon.Processed {
			// Processed without detail -> conservative: require user confirmation rather than guessing.
			s.Paused = true
			s.NeedsUserConfirm = true
			needUserConfirm = true
			continue
		}
		if item.frame.DeadlineMs > 0 && now >= item.frame.DeadlineMs {
			needBarrier = true
			continue
		}
		if !needUserConfirm && !needBarrier {
			toResend = append(toResend, item.frame)
		}
	}
	if needUserConfirm {
		return nil, false, true
	}
	return toResend, needBarrier, false
}

// OnInstanceChanged pauses input and requires user confirmation when the daemon restarted.
// This is NOT attributed as Input fail-stop.
func (s *TerminalStream) OnInstanceChanged() {
	s.Paused = true
	s.NeedsUserConfirm = true
}

// ConfirmRebuild records explicit user confirmation after inspecting screen and ordered input statuses.
// Drops old queue; returns ONLY frames explicitly known to be unsent/not-applied as editable drafts.
func (s *TerminalStream) ConfirmRebuild(finalStatus *TermStatusSnapshot) (unsentDrafts []TermFrame) {
	bySeq := map[uint64]TermFrameStatus{}
	if finalStatus != nil && finalStatus.Available {
		for _, d := range finalStatus.State.ToDaemon.Details {
			bySeq[d.Seq] = d
		}
	}
	seqs := make([]uint64, 0, len(s.inputCache))
	for seq := range s.inputCache {
		seqs = append(seqs, seq)
	}
	for i := 1; i < len(seqs); i++ {
		for j := i; j > 0 && seqs[j] < seqs[j-1]; j-- {
			seqs[j], seqs[j-1] = seqs[j-1], seqs[j]
		}
	}
	if finalStatus != nil && finalStatus.Available && finalStatus.Final {
		for _, seq := range seqs {
			item := s.inputCache[seq]
			if d, ok := bySeq[seq]; ok {
				if d.Outcome == TermOutcomeNotApplied || d.Outcome == TermOutcomeExpiredNotApplied {
					unsentDrafts = append(unsentDrafts, item.frame)
				}
			} else if seq > finalStatus.State.ToDaemon.Processed && (s.failedInputSeq == 0 || seq > s.failedInputSeq) {
				if s.failedInputSeq == 0 {
					unsentDrafts = append(unsentDrafts, item.frame)
				}
			}
		}
	}
	clear(s.inputCache)
	s.inputCacheBytes = 0
	s.Paused = false
	s.NeedsUserConfirm = false
	s.Stopped = true
	return unsentDrafts
}

// Advance ticks gap deadlines, input cache age, ACK batch interval, output replay pruning, and rendered identity expiry.
// Returns emitACK=true if a pending ACK batch is now due.
func (s *TerminalStream) Advance(now uint64) (emitACK bool) {
	for _, q := range s.inputPending {
		if q.frame.DeadlineMs > 0 && now >= q.frame.DeadlineMs {
			s.NetworkRecovery = true
		}
	}
	for _, q := range s.outputPending {
		if q.frame.DeadlineMs > 0 && now >= q.frame.DeadlineMs {
			s.NetworkRecovery = true
		}
	}
	if !s.Stopped && s.inputGapDue != 0 && now >= s.inputGapDue {
		s.NetworkRecovery = true
	}
	if !s.Stopped && s.outputGapDue != 0 && now >= s.outputGapDue {
		s.NetworkRecovery = true
	}
	for _, item := range s.inputCache {
		if now-item.cachedAt >= TermInputCacheMaxMs || (item.frame.DeadlineMs > 0 && now >= item.frame.DeadlineMs) {
			s.Paused = true
		}
	}
	if s.dirtyACK > 0 && now-s.lastACK >= TermAckIntervalMs {
		s.dirtyACK = 0
		s.lastACK = now
		emitACK = true
	}
	s.pruneReplay(now)
	s.pruneOutputIdentities(now)
	return emitACK
}

// NextDue returns the earliest timestamp at which Advance has timed work.
func (s *TerminalStream) NextDue() (uint64, bool) {
	var due uint64
	ok := false
	add := func(t uint64) {
		if t == 0 {
			return
		}
		if !ok || t < due {
			due, ok = t, true
		}
	}
	if !s.Stopped {
		add(s.inputGapDue)
		add(s.outputGapDue)
	}
	if s.dirtyACK > 0 {
		add(s.lastACK + TermAckIntervalMs)
	}
	for _, item := range s.inputCache {
		add(item.cachedAt + TermInputCacheMaxMs)
		add(item.frame.DeadlineMs)
	}
	return due, ok
}

func (s *TerminalStream) refreshInputGap() {
	missing := s.processed + 1
	if s.inflight != nil && s.inflight.Seq == missing {
		missing++
	}
	for {
		if _, ok := s.inputPending[missing]; !ok {
			break
		}
		missing++
	}
	s.inputGapDue = 0
	for seq, q := range s.inputPending {
		if seq > missing {
			due := q.firstSeen + TermGapTimeoutMs
			if q.frame.DeadlineMs > 0 && q.frame.DeadlineMs < due {
				due = q.frame.DeadlineMs
			}
			if s.inputGapDue == 0 || due < s.inputGapDue {
				s.inputGapDue = due
			}
		}
	}
}

func (s *TerminalStream) refreshOutputGap() {
	missing := s.rendered + 1
	for {
		if _, ok := s.outputPending[missing]; !ok {
			break
		}
		missing++
	}
	s.outputGapDue = 0
	for seq, q := range s.outputPending {
		if seq > missing {
			due := q.firstSeen + TermGapTimeoutMs
			if q.frame.DeadlineMs > 0 && q.frame.DeadlineMs < due {
				due = q.frame.DeadlineMs
			}
			if s.outputGapDue == 0 || due < s.outputGapDue {
				s.outputGapDue = due
			}
		}
	}
}

func (s *TerminalStream) pruneReplay(now uint64) {
	for len(s.replay) > 0 && (len(s.replay) > TermOutputReplayMaxFrames || s.replayBytes > TermOutputReplayMaxBytes || now-s.replay[0].recordedAt >= TermOutputReplayMaxMs) {
		s.replayBytes -= len(s.replay[0].frame.RawEnvelope)
		s.replay = s.replay[1:]
	}
}

func (s *TerminalStream) pruneOutputIdentities(now uint64) {
	for seq, rec := range s.renderedIDs {
		if rec.deadline > 0 && now > rec.deadline {
			delete(s.renderedIDs, seq)
		}
	}
}

// TerminalRegistry enforces the daemon process-level budget: max 16 active + 16 retired streams (30s retention).
type TerminalRegistry struct {
	maxActive  int
	maxRetired int
	active     map[string]*TerminalStream
	retired    map[string]*TerminalStream
}

// NewTerminalRegistry creates a daemon-sized TerminalRegistry (16 active, 16 retired).
func NewTerminalRegistry(maxActive, maxRetired int) *TerminalRegistry {
	if maxActive <= 0 {
		maxActive = TermMaxActiveStreams
	}
	if maxRetired <= 0 {
		maxRetired = TermMaxRetiredStreams
	}
	return &TerminalRegistry{
		maxActive:  maxActive,
		maxRetired: maxRetired,
		active:     map[string]*TerminalStream{},
		retired:    map[string]*TerminalStream{},
	}
}

// Add registers a new active stream; returns ErrTermRegistryFull if at cap or stream ID already used.
func (r *TerminalRegistry) Add(s *TerminalStream) error {
	if s == nil || s.StreamID == "" {
		return ErrTermAttachBinding
	}
	if _, dupA := r.active[s.StreamID]; dupA {
		return ErrTermAttachBinding
	}
	if _, dupR := r.retired[s.StreamID]; dupR {
		return ErrTermAttachBinding
	}
	if len(r.active) >= r.maxActive {
		return ErrTermRegistryFull
	}
	r.active[s.StreamID] = s
	return nil
}

// GetActive returns an active stream by ID.
func (r *TerminalRegistry) GetActive(id string) *TerminalStream { return r.active[id] }

// GetQueryable returns an active or retired stream by ID (for InputStatus queries).
func (r *TerminalRegistry) GetQueryable(id string) *TerminalStream {
	if s := r.active[id]; s != nil {
		return s
	}
	return r.retired[id]
}

// Retire moves a barrier-stopped stream into the 30s retired query table.
func (r *TerminalRegistry) Retire(id string, now uint64) error {
	r.Prune(now)
	s := r.active[id]
	if s == nil {
		return nil
	}
	if !s.Stopped {
		return ErrTermNotStopped
	}
	if len(r.retired) >= r.maxRetired {
		return ErrTermRegistryFull
	}
	if s.RetiredAt == 0 {
		s.RetiredAt = now
	}
	delete(r.active, id)
	r.retired[id] = s
	return nil
}

// Prune removes retired streams whose 30s query retention has elapsed.
func (r *TerminalRegistry) Prune(now uint64) {
	for id, s := range r.retired {
		if now-s.RetiredAt >= TermRetiredRetainMs {
			delete(r.retired, id)
		}
	}
}

// ActiveCount returns the number of active streams.
func (r *TerminalRegistry) ActiveCount() int { return len(r.active) }

// RetiredCount returns the number of retired queryable streams.
func (r *TerminalRegistry) RetiredCount() int { return len(r.retired) }

// FlowStateToken computes the immutable SHA-256 snapshot token over the canonical FlowState encoding.
func FlowStateToken(fs TermFlowState) [32]byte {
	return sha256.Sum256(EncodeFlowState(fs))
}

// VerifySnapshotToken checks that token equals FlowStateToken(fs).
func VerifySnapshotToken(fs TermFlowState, token []byte) bool {
	if len(token) != 32 {
		return false
	}
	want := FlowStateToken(fs)
	return bytes.Equal(want[:], token)
}
