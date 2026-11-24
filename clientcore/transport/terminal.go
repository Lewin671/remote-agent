package transport

import (
	"bytes"

	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
)

// TerminalDirectState codes reported in CoordTerminalState.State.
const (
	TermStateUnspecified    uint32 = 0
	TermStateLive           uint32 = 1
	TermStateReconnecting   uint32 = 2
	TermStateFailStop       uint32 = 3
	TermStateInstanceChange uint32 = 4
	TermStateClosed         uint32 = 5
)

const (
	termDisconnectTimeoutMs = 15_000
	earlyOutputMaxFrames    = 16
	earlyOutputMaxBytes     = 256 << 10
	earlyOutputTTLMs        = 5_000
	inputStatusPageLimit    = 128
	inputStatusMaxDetails   = 256
)

// CoordTerminalSend instructs the shell to transmit the identical sealed
// terminal input envelope over relay and/or direct PC (hot standby dual-send).
type CoordTerminalSend struct {
	StreamID       string
	SendRelay      bool
	DirectPCID     uint64
	SealedEnvelope []byte
}

// CoordTerminalDeliver delivers ordered, deduplicated terminal output payloads
// to the shell renderer after Core's reorder + duplicate suppression.
type CoordTerminalDeliver struct {
	StreamID string
	Payloads [][]byte
}

// CoordTerminalAttachRelay asks the shell to open a replacement /v1/term relay
// WebSocket using LegID as TermHello.StreamId without calling OpenTerminal.
type CoordTerminalAttachRelay struct {
	StreamID  string
	MachineID string
	LegID     []byte
}

// CoordTerminalState notifies the shell of logical terminal continuity state
// (reconnecting, non-bypassable Input fail-stop, daemon restart confirmation gate, or closed).
// Unified shell policy (M4/M5): NeedsConfirmation renders the confirmation UI without
// auto-closing; State == TermStateClosed closes (or rebuilds when Reason == "rebuild_confirmed").
type CoordTerminalState struct {
	StreamID          string
	State             uint32
	Reason            string
	NeedsConfirmation bool
}

func (CoordTerminalSend) coordEffect()        {}
func (CoordTerminalDeliver) coordEffect()     {}
func (CoordTerminalAttachRelay) coordEffect() {}
func (CoordTerminalState) coordEffect()       {}

type earlyTermOutput struct {
	seq        uint64
	frameHash  []byte
	payload    []byte
	fromDirect bool
	pcID       uint64
	receivedAt int64
}

type clientTermStream struct {
	streamID        string
	machineID       string
	sessionID       string
	stream          *linkcore.TerminalStream
	daemonReady     bool
	relayOpen             bool
	relayLegID            string
	relayGen              uint64
	relayAttachGen        uint64
	relayRecoveryInFlight bool
	relayRecoveryLeg      string
	relayRetryAt          int64
	relayRetryAttempts    int
	directAttached  bool
	directPCID      uint64
	directGen       uint64
	directAttachGen uint64
	phase3Used      bool
	disconnectedAll bool
	disconnectedAt  int64
	queryInFlight   bool
	queryID         []byte
	pendingDetails  []linkcore.TermFrameStatus
	needsConfirm    bool
	replaySentSeq   uint64
}

func (c *Coordinator) ensureTermStreams() {
	if c.termStreams == nil {
		c.termStreams = map[string]*clientTermStream{}
	}
}

func (c *Coordinator) activeTerminalRound(now int64, machineID string) (*coordRound, uint64, uint64, bool) {
	if !c.engine.TerminalReady(now, machineID) {
		return nil, 0, 0, false
	}
	p := c.engine.peers[machineID]
	if p == nil {
		return nil, 0, 0, false
	}
	r := c.rounds[p.id]
	if r == nil || r.ended {
		return nil, 0, 0, false
	}
	pcID := r.globalPC[UseRead]
	if pcID == 0 || !r.confirmed[UseRead] || !r.dcOpen[UseRead] {
		return nil, 0, 0, false
	}
	gen := r.session.View().Generation[UseRead-1]
	if gen == 0 {
		gen = 1
	}
	return r, pcID, gen, true
}

// TerminalDirectOpen registers a logical terminal stream in Core and attaches
// direct use=1 hot standby when available. Any early outputs buffered before
// open (M6) are drained immediately after registration.
func (c *Coordinator) TerminalDirectOpen(now int64, streamID, machineID, sessionID string, relayLegID []byte) []CoordEffect {
	if streamID == "" || machineID == "" || sessionID == "" {
		return nil
	}
	c.ensureTermStreams()
	if _, exists := c.termStreams[streamID]; !exists && len(c.termStreams) >= linkcore.TermMaxClientStreams {
		c.dropEarlyOutputs(streamID)
		c.emit(CoordTerminalState{StreamID: streamID, State: TermStateClosed, Reason: "capacity"})
		return c.take()
	}
	legStr := string(relayLegID)
	if legStr == "" {
		legStr = streamID
	}
	ts := linkcore.NewTerminalStream(streamID, sessionID, machineID, string(c.instance), uint64(now))
	_ = ts.AttachLeg("relay", legStr, string(c.instance), 1)
	ct := &clientTermStream{
		streamID:   streamID,
		machineID:  machineID,
		sessionID:  sessionID,
		stream:     ts,
		relayOpen:      true,
		relayLegID:     legStr,
		relayGen:       1,
		relayAttachGen: 1,
		phase3Used:     c.engine.TerminalReady(now, machineID),
	}
	c.termStreams[streamID] = ct
	// H-B: do NOT send direct AttachPath until daemon confirms openTerminal succeeded
	// (via TerminalDirectLeg(direct=false, open=true) or first inbound TerminalDirectOutput).
	c.emit(CoordTerminalState{StreamID: streamID, State: TermStateLive})
	// Drain any pre-open buffered outputs (M6).
	if early := c.takeEarlyOutputs(now, streamID); len(early) > 0 {
		for _, item := range early {
			if c.termStreams[streamID] != ct {
				break
			}
			c.processTerminalOutput(now, ct, item.seq, item.frameHash, item.payload, item.fromDirect, item.pcID)
		}
	}
	return c.take()
}

// TerminalDirectClose detaches and removes a logical terminal stream.
func (c *Coordinator) TerminalDirectClose(now int64, streamID string) []CoordEffect {
	c.dropEarlyOutputs(streamID)
	ct := c.termStreams[streamID]
	if ct == nil {
		return nil
	}
	if r, _, _, ok := c.activeTerminalRound(now, ct.machineID); ok && ct.directAttached {
		legBytes := ct.stream.LegID("direct")
		if len(legBytes) == 16 {
			body := linkcore.EncodeClosePath(linkcore.ClosePathMsg{
				Stream:         ct.streamID,
				LegID:          legBytes,
				PathGeneration: ct.directGen,
				Reason:         1,
			})
			_ = c.stepSession(now, r, linkcore.QueueControl{Type: linkcore.ControlClosePath, Body: body})
		}
	}
	delete(c.termStreams, streamID)
	return c.take()
}

// TerminalDirectLeg updates relay/direct leg state for a logical terminal stream.
func (c *Coordinator) TerminalDirectLeg(now int64, streamID string, legID []byte, direct, open bool, pcID uint64) []CoordEffect {
	ct := c.termStreams[streamID]
	if ct == nil {
		return nil
	}
	if !direct {
		if open {
			wasDaemonReady := ct.daemonReady
			ct.daemonReady = true
			if ct.relayOpen && !wasDaemonReady && (len(legID) == 0 || string(legID) == ct.relayLegID) {
				// H-B: initial openTerminal RPC confirmed stream exists on daemon -> attach direct now.
				c.maybeAttachDirectTerminal(now, ct)
				return c.take()
			}
			ct.relayOpen = true
			ct.relayRecoveryInFlight = false
			ct.relayRecoveryLeg = ""
			ct.relayRetryAt = 0
			ct.relayRetryAttempts = 0
			if len(legID) > 0 {
				ct.relayLegID = string(legID)
			}
			ct.relayGen++
			_ = ct.stream.AttachLeg("relay", ct.relayLegID, string(c.instance), ct.relayGen)
			ct.disconnectedAll = false
			ct.stream.NetworkRecovery = false
			c.maybeAttachDirectTerminal(now, ct)
			c.emit(CoordTerminalState{StreamID: streamID, State: TermStateLive})
			return c.take()
		}
		// N5: replacement relay dial failed -> schedule bounded retry with a new generation.
		if ct.relayRecoveryInFlight && len(legID) > 0 && string(legID) == ct.relayRecoveryLeg {
			ct.relayRecoveryInFlight = false
			ct.relayRecoveryLeg = ""
			ct.relayRetryAttempts++
			if !ct.relayOpen && ct.directAttached && ct.relayRetryAttempts <= 5 {
				shift := ct.relayRetryAttempts - 1
				if shift > 3 {
					shift = 3
				}
				ct.relayRetryAt = now + (int64(1000) << shift)
			}
			return c.take()
		}
		// Relay leg dropped: ignore stale leg closes.
		if len(legID) > 0 && ct.relayLegID != "" && string(legID) != ct.relayLegID {
			return nil
		}
		ct.relayOpen = false
		ct.stream.CloseLeg("relay", ct.relayGen)
		if r, _, _, ok := c.activeTerminalRound(now, ct.machineID); ok && ct.directAttached {
			c.triggerRelayRecovery(now, r, ct)
			return c.take()
		}
		if ct.phase3Used {
			// Full disconnect on a Phase 3 stream: enter bounded recovery window (M4).
			ct.disconnectedAll = true
			ct.disconnectedAt = now
			ct.stream.NetworkRecovery = true
			c.emit(CoordTerminalState{StreamID: streamID, State: TermStateReconnecting, Reason: "relay_closed"})
			return c.take()
		}
		// Pure-relay / legacy daemon: relay close ends the stream cleanly (M5).
		c.emit(CoordTerminalState{StreamID: streamID, State: TermStateClosed, Reason: "relay_closed"})
		delete(c.termStreams, streamID)
		return c.take()
	}
	// Direct leg state change.
	if open && pcID != 0 {
		ct.directAttached = true
		ct.directPCID = pcID
		ct.phase3Used = true
	} else if !open && (pcID == 0 || ct.directPCID == pcID) {
		ct.directAttached = false
		ct.directPCID = 0
		ct.queryInFlight = false
		ct.queryID = nil
		ct.stream.CloseLeg("direct", ct.directGen)
		if !ct.relayOpen {
			ct.disconnectedAll = true
			ct.disconnectedAt = now
			ct.stream.NetworkRecovery = true
			c.emit(CoordTerminalState{StreamID: streamID, State: TermStateReconnecting, Reason: "direct_closed"})
		}
	}
	return c.take()
}

// TerminalDirectInput gates input caching on active negotiated Phase 3 direct
// legs (H1/H2) and emits hot-standby dual-send when direct use=1 is ready.
func (c *Coordinator) TerminalDirectInput(now int64, streamID string, seq uint64, isInput bool, sealedEnvelope, frameHash []byte) []CoordEffect {
	ct := c.termStreams[streamID]
	if ct == nil || seq == 0 || len(sealedEnvelope) == 0 {
		return nil
	}
	if ct.needsConfirm || (isInput && ct.stream.FailedInputSeq() != 0) {
		c.emit(CoordTerminalState{
			StreamID:          streamID,
			State:             TermStateFailStop,
			Reason:            "input_fail_stop",
			NeedsConfirmation: true,
		})
		return c.take()
	}

	var directPC uint64
	if _, pcID, _, ok := c.activeTerminalRound(now, ct.machineID); ok {
		if !ct.directAttached || ct.directPCID != pcID {
			c.maybeAttachDirectTerminal(now, ct)
		}
		if ct.directAttached && ct.directPCID == pcID {
			directPC = pcID
		}
	}

	if !ct.relayOpen && directPC == 0 {
		if !ct.phase3Used {
			c.emit(CoordTerminalState{StreamID: streamID, State: TermStateClosed, Reason: "no_leg"})
			delete(c.termStreams, streamID)
			return c.take()
		}
		// H-A: cache ALL frames (including Resize/History) during full-disconnect so
		// InputStatus reconciliation resends them in order without leaving seq gaps on daemon.
		var hash32 [32]byte
		copy(hash32[:], frameHash)
		if err := ct.stream.CacheInput(uint64(now), linkcore.TermFrame{
			Seq:         seq,
			PayloadHash: hash32,
			RawBytes:    len(sealedEnvelope),
			IsInput:     isInput,
			RawEnvelope: append([]byte(nil), sealedEnvelope...),
		}); err != nil {
			c.emit(CoordTerminalState{StreamID: streamID, State: TermStateClosed, Reason: "input_cache_full"})
			delete(c.termStreams, streamID)
			return c.take()
		}
		c.emit(CoordTerminalState{StreamID: streamID, State: TermStateReconnecting, Reason: "no_leg"})
		return c.take()
	}

	// H1/H2/H-A: Cache all frames (Input + Resize/History) whenever the stream is Phase 3-capable
	// (active direct leg OR previously negotiated Phase 3 so in-flight relay frames survive WS drop).
	// Pure-relay / legacy-daemon streams never enter the direct-ACK cache (H1).
	if directPC != 0 || ct.phase3Used {
		var hash32 [32]byte
		copy(hash32[:], frameHash)
		if err := ct.stream.CacheInput(uint64(now), linkcore.TermFrame{
			Seq:         seq,
			PayloadHash: hash32,
			RawBytes:    len(sealedEnvelope),
			IsInput:     isInput,
			RawEnvelope: append([]byte(nil), sealedEnvelope...),
		}); err != nil {
			// If direct has been absent long enough to fill cache while relay is healthy,
			// degrade cleanly to pure-relay instead of killing a healthy relay stream.
			if directPC == 0 && ct.relayOpen && !ct.stream.NeedsUserConfirm && ct.stream.FailedInputSeq() == 0 {
				ct.stream.ClearInputCache()
				ct.phase3Used = false
			} else {
				// Never silently drop a frame while keeping the stream open (H1):
				// fail/close explicitly so no unrecoverable seq hole is created on the daemon.
				c.emit(CoordTerminalState{StreamID: streamID, State: TermStateClosed, Reason: "input_cache_full"})
				delete(c.termStreams, streamID)
				return c.take()
			}
		}
	}

	c.emit(CoordTerminalSend{
		StreamID:       streamID,
		SendRelay:      ct.relayOpen,
		DirectPCID:     directPC,
		SealedEnvelope: sealedEnvelope,
	})
	return c.take()
}

// TerminalDirectOutput reorders and deduplicates verified inbound terminal output
// frames from either relay WS or direct DC, replacing seq <= last close-on-duplicate.
// Frames arriving slightly before TerminalDirectOpen are boundedly buffered (M6).
func (c *Coordinator) TerminalDirectOutput(now int64, streamID string, seq uint64, frameHash, payload []byte, fromDirect bool, pcID uint64) []CoordEffect {
	if streamID == "" || seq == 0 {
		return nil
	}
	ct := c.termStreams[streamID]
	if ct == nil {
		c.bufferEarlyOutput(now, streamID, seq, frameHash, payload, fromDirect, pcID)
		return nil
	}
	c.processTerminalOutput(now, ct, seq, frameHash, payload, fromDirect, pcID)
	return c.take()
}

func (c *Coordinator) processTerminalOutput(now int64, ct *clientTermStream, seq uint64, frameHash, payload []byte, fromDirect bool, pcID uint64) {
	if !ct.daemonReady {
		ct.daemonReady = true
		c.maybeAttachDirectTerminal(now, ct)
	}
	var hash32 [32]byte
	copy(hash32[:], frameHash)
	action, ready, err := ct.stream.ReceiveOutput(uint64(now), linkcore.TermFrame{
		Seq:         seq,
		PayloadHash: hash32,
		RawBytes:    len(payload),
		PlainBytes:  append([]byte(nil), payload...),
	})
	switch action {
	case linkcore.TermOutputDuplicateACK:
		// Duplicate with matching hash: ACK without closing or double-rendering.
		c.sendClientOutputAck(now, ct)
	case linkcore.TermOutputReject:
		if err == linkcore.ErrTermConflict {
			c.emit(CoordTerminalState{StreamID: ct.streamID, State: TermStateClosed, Reason: "conflict"})
			delete(c.termStreams, ct.streamID)
		}
	case linkcore.TermOutputReady:
		var payloads [][]byte
		needAck := false
		for cur := ready; cur != nil; {
			payloads = append(payloads, cur.PlainBytes)
			next, emitAck, cerr := ct.stream.ConfirmRendered(uint64(now), cur.Seq)
			if cerr != nil {
				break
			}
			if emitAck {
				needAck = true
			}
			cur = next
		}
		if len(payloads) > 0 {
			c.emit(CoordTerminalDeliver{StreamID: ct.streamID, Payloads: payloads})
		}
		if needAck {
			c.sendClientOutputAck(now, ct)
		}
	case linkcore.TermOutputBuffered:
		// Out-of-order frame buffered; 10s gap timer / Replay will fill missing seqs.
	}
	_ = fromDirect
	_ = pcID
}

func (c *Coordinator) bufferEarlyOutput(now int64, streamID string, seq uint64, frameHash, payload []byte, fromDirect bool, pcID uint64) {
	c.pruneEarlyOutputs(now)
	if len(payload) > earlyOutputMaxBytes {
		return
	}
	if c.earlyOutputs == nil {
		c.earlyOutputs = map[string][]earlyTermOutput{}
	}
	list := c.earlyOutputs[streamID]
	if len(list) >= earlyOutputMaxFrames {
		return
	}
	if c.earlyOutputBytes+len(payload) > earlyOutputMaxBytes {
		return
	}
	c.earlyOutputs[streamID] = append(list, earlyTermOutput{
		seq:        seq,
		frameHash:  append([]byte(nil), frameHash...),
		payload:    append([]byte(nil), payload...),
		fromDirect: fromDirect,
		pcID:       pcID,
		receivedAt: now,
	})
	c.earlyOutputBytes += len(payload)
}

func (c *Coordinator) takeEarlyOutputs(now int64, streamID string) []earlyTermOutput {
	if len(c.earlyOutputs) == 0 {
		return nil
	}
	list := c.earlyOutputs[streamID]
	if len(list) == 0 {
		return nil
	}
	delete(c.earlyOutputs, streamID)
	valid := list[:0]
	for _, item := range list {
		c.earlyOutputBytes -= len(item.payload)
		if now-item.receivedAt <= earlyOutputTTLMs {
			valid = append(valid, item)
		}
	}
	if c.earlyOutputBytes < 0 {
		c.earlyOutputBytes = 0
	}
	return valid
}

func (c *Coordinator) dropEarlyOutputs(streamID string) {
	if len(c.earlyOutputs) == 0 {
		return
	}
	if list, ok := c.earlyOutputs[streamID]; ok {
		for _, item := range list {
			c.earlyOutputBytes -= len(item.payload)
		}
		if c.earlyOutputBytes < 0 {
			c.earlyOutputBytes = 0
		}
		delete(c.earlyOutputs, streamID)
	}
}

func (c *Coordinator) pruneEarlyOutputs(now int64) {
	for id, list := range c.earlyOutputs {
		keep := list[:0]
		for _, item := range list {
			if now-item.receivedAt <= earlyOutputTTLMs {
				keep = append(keep, item)
			} else {
				c.earlyOutputBytes -= len(item.payload)
			}
		}
		if len(keep) == 0 {
			delete(c.earlyOutputs, id)
		} else {
			c.earlyOutputs[id] = keep
		}
	}
	if c.earlyOutputBytes < 0 {
		c.earlyOutputBytes = 0
	}
}

// TerminalDirectConfirmRebuild clears the user confirmation gate after fail-stop
// or daemon restart, releases the old stream, and signals the shell to rebuild a fresh stream.
func (c *Coordinator) TerminalDirectConfirmRebuild(now int64, streamID string) []CoordEffect {
	ct := c.termStreams[streamID]
	if ct == nil {
		return nil
	}
	_ = ct.stream.ConfirmRebuild(nil)
	ct.needsConfirm = false
	ct.disconnectedAll = false
	if r, _, _, ok := c.activeTerminalRound(now, ct.machineID); ok && ct.directAttached {
		legBytes := ct.stream.LegID("direct")
		if len(legBytes) == 16 {
			body := linkcore.EncodeClosePath(linkcore.ClosePathMsg{
				Stream:         ct.streamID,
				LegID:          legBytes,
				PathGeneration: ct.directGen,
				Reason:         1,
			})
			_ = c.stepSession(now, r, linkcore.QueueControl{Type: linkcore.ControlClosePath, Body: body})
		}
	}
	delete(c.termStreams, streamID)
	c.emit(CoordTerminalState{StreamID: streamID, State: TermStateClosed, Reason: "rebuild_confirmed"})
	return c.take()
}

// makeRelayAttachLegID generates 16 raw random bytes for AttachPath.leg_id
// (exact 16-byte wire schema) and its 32-char lowercase hex encoding for /v1/term
// TermHello.StreamId, matching ordinary streams' 32-char hex length identically (N7).
func makeRelayAttachLegID(rand16 [16]byte) (wireLeg16 string, streamID32 []byte) {
	if rand16[0] == 'd' && rand16[1] == ':' {
		rand16[0] ^= 0xff
	}
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 32)
	for i, b := range rand16 {
		out[i*2] = hexDigits[b>>4]
		out[i*2+1] = hexDigits[b&0x0f]
	}
	return string(rand16[:]), out
}

func (c *Coordinator) triggerRelayRecovery(now int64, r *coordRound, ct *clientTermStream) {
	var rawRand [16]byte
	if _, err := c.randPool.Read(rawRand[:]); err != nil {
		c.maybeNeedEntropy(CoordEntropyRefillBytes)
		return
	}
	c.maybeLowWater()
	wireLeg16, streamID32 := makeRelayAttachLegID(rawRand)
	// N5: strictly advance relayAttachGen on every recovery attempt so retries are never rejected as stale.
	if ct.relayAttachGen < ct.relayGen {
		ct.relayAttachGen = ct.relayGen
	}
	ct.relayAttachGen++
	nextGen := ct.relayAttachGen
	ct.relayRecoveryInFlight = true
	ct.relayRecoveryLeg = string(streamID32)
	ct.relayRetryAt = 0
	body := linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         ct.streamID,
		Session:        ct.sessionID,
		Sender:         c.snap.SelfID,
		LegID:          wireLeg16,
		PathGeneration: nextGen,
		State: linkcore.TermFlowState{
			ToDaemon: ct.stream.CurrentInputWatermark(),
			ToClient: ct.stream.CurrentOutputWatermark(),
		},
	})
	_ = c.stepSession(now, r, linkcore.QueueControl{Type: linkcore.ControlAttachPath, Body: body})
	c.emit(CoordTerminalAttachRelay{
		StreamID:  ct.streamID,
		MachineID: ct.machineID,
		LegID:     streamID32,
	})
}

func (c *Coordinator) sendInputStatusQuery(now int64, r *coordRound, ct *clientTermStream, fromSeq uint64, snapshotToken []byte) {
	var qid [16]byte
	if _, err := c.randPool.Read(qid[:]); err != nil {
		c.maybeNeedEntropy(CoordEntropyRefillBytes)
		return
	}
	c.maybeLowWater()
	ct.queryInFlight = true
	ct.queryID = append([]byte(nil), qid[:]...)
	if fromSeq <= 1 {
		ct.pendingDetails = nil
	}
	body := linkcore.EncodeInputStatus(linkcore.InputStatusMsg{
		Stream:        ct.streamID,
		QueryID:       ct.queryID,
		FromSeq:       fromSeq,
		Limit:         inputStatusPageLimit,
		SnapshotToken: snapshotToken,
	})
	_ = c.stepSession(now, r, linkcore.QueueControl{Type: linkcore.ControlInputStatus, Body: body})
}

func (c *Coordinator) sendAttachDirectPath(now int64, r *coordRound, pcID, _ uint64, ct *clientTermStream) {
	if ct.directAttached && ct.directPCID == pcID {
		return
	}
	var rawRand [7]byte
	if _, err := c.randPool.Read(rawRand[:]); err != nil {
		c.maybeNeedEntropy(CoordEntropyRefillBytes)
		return
	}
	c.maybeLowWater()
	leg := make([]byte, 16)
	leg[0] = 'd'
	leg[1] = ':'
	const hexDigits = "0123456789abcdef"
	for i, b := range rawRand {
		leg[2+i*2] = hexDigits[b>>4]
		leg[2+i*2+1] = hexDigits[b&0x0f]
	}
	ct.directAttachGen++
	attachGen := ct.directAttachGen
	ct.directAttached = true
	ct.directPCID = pcID
	ct.directGen = attachGen
	ct.phase3Used = true
	_ = ct.stream.AttachLeg("direct", string(leg), string(c.instance), attachGen)
	body := linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         ct.streamID,
		Session:        ct.sessionID,
		Sender:         c.snap.SelfID,
		LegID:          string(leg),
		PathGeneration: attachGen,
		State: linkcore.TermFlowState{
			ToDaemon: ct.stream.CurrentInputWatermark(),
			ToClient: ct.stream.CurrentOutputWatermark(),
		},
	})
	_ = c.stepSession(now, r, linkcore.QueueControl{Type: linkcore.ControlAttachPath, Body: body})
}

func (c *Coordinator) maybeAttachDirectTerminal(now int64, ct *clientTermStream) {
	if !ct.daemonReady {
		return
	}
	r, pcID, gen, ok := c.activeTerminalRound(now, ct.machineID)
	if !ok {
		return
	}
	if ct.directAttached && ct.directPCID == pcID {
		return
	}
	// M4: after full disconnect or with unconfirmed inputs on a new direct round,
	// query daemon InputStatus first instead of blindly attaching/resending.
	if ct.disconnectedAll || ct.stream.NetworkRecovery || ct.stream.HasUnconfirmedInputs() {
		if !ct.queryInFlight {
			c.sendInputStatusQuery(now, r, ct, 1, nil)
		}
		return
	}
	c.sendAttachDirectPath(now, r, pcID, gen, ct)
}

func (c *Coordinator) attachDirectTerminalsForMachine(now int64, machineID string) {
	for _, ct := range c.termStreams {
		if ct != nil && ct.machineID == machineID {
			c.maybeAttachDirectTerminal(now, ct)
		}
	}
}

func (c *Coordinator) detachDirectTerminalsForPC(now int64, pcID uint64) {
	for _, ct := range c.termStreams {
		if ct != nil && ct.directAttached && ct.directPCID == pcID {
			ct.directAttached = false
			ct.directPCID = 0
			ct.queryInFlight = false
			ct.queryID = nil
			ct.stream.CloseLeg("direct", ct.directGen)
			if !ct.relayOpen {
				ct.disconnectedAll = true
				ct.disconnectedAt = now
				ct.stream.NetworkRecovery = true
				c.emit(CoordTerminalState{StreamID: ct.streamID, State: TermStateReconnecting, Reason: "direct_closed"})
			}
		}
	}
}

func (c *Coordinator) sendClientOutputAck(now int64, ct *clientTermStream) {
	r, _, _, ok := c.activeTerminalRound(now, ct.machineID)
	if !ok {
		return
	}
	body := linkcore.EncodeTermAck(linkcore.TermAckMsg{
		Stream:    ct.streamID,
		Direction: linkcore.TermDirToClient,
		State:     ct.stream.CurrentOutputWatermark(),
	})
	_ = c.stepSession(now, r, linkcore.QueueControl{Type: linkcore.ControlAck, Body: body})
}

func (c *Coordinator) stepClientTerminals(now int64) {
	c.pruneEarlyOutputs(now)
	var expiredIDs []string
	for id, ct := range c.termStreams {
		if ct == nil {
			continue
		}
		if ct.disconnectedAll && now-ct.disconnectedAt >= termDisconnectTimeoutMs {
			ct.disconnectedAll = false
			c.emit(CoordTerminalState{StreamID: ct.streamID, State: TermStateClosed, Reason: "reconnect_timeout"})
			expiredIDs = append(expiredIDs, id)
			continue
		}
		if ct.relayRetryAt > 0 && now >= ct.relayRetryAt {
			ct.relayRetryAt = 0
			if !ct.relayOpen && !ct.relayRecoveryInFlight && ct.directAttached {
				if r, _, _, ok := c.activeTerminalRound(now, ct.machineID); ok {
					c.triggerRelayRecovery(now, r, ct)
				}
			}
		}
		emitAck := ct.stream.Advance(uint64(now))
		if emitAck {
			c.sendClientOutputAck(now, ct)
		}
		// Trigger Replay (Control 5) if an output gap is open and we haven't requested this seq yet.
		if gapDue := ct.stream.OutputGapDue(); gapDue > 0 {
			wantFrom := ct.stream.Rendered() + 1
			if wantFrom > ct.replaySentSeq {
				if r, _, _, ok := c.activeTerminalRound(now, ct.machineID); ok {
					ct.replaySentSeq = wantFrom
					body := linkcore.EncodeReplay(linkcore.ReplayMsg{
						Stream:    ct.streamID,
						Direction: linkcore.TermDirToClient,
						FromSeq:   wantFrom,
						MaxBytes:  1 << 20,
						MaxFrames: 128,
					})
					_ = c.stepSession(now, r, linkcore.QueueControl{Type: linkcore.ControlReplay, Body: body})
				}
			}
			if uint64(now) >= gapDue {
				// M4/M5: an unrecovered 10s output gap must close/rebuild instead of freezing forever.
				c.emit(CoordTerminalState{StreamID: ct.streamID, State: TermStateClosed, Reason: "output_gap_timeout"})
				expiredIDs = append(expiredIDs, id)
			}
		}
	}
	for _, id := range expiredIDs {
		delete(c.termStreams, id)
	}
}

func (c *Coordinator) handleInboundTerminalControl(now int64, r *coordRound, o peerwire.Opened) (uint64, []byte) {
	switch o.Type {
	case linkcore.ControlAck:
		ack, isStreamAck, err := linkcore.DecodeTermAck(o.Payload)
		if err != nil || !isStreamAck {
			return 0, nil
		}
		ct := c.termStreams[ack.Stream]
		if ct == nil || ct.machineID != r.machine {
			return 0, nil
		}
		if ack.Direction == linkcore.TermDirToDaemon {
			if failStop := ct.stream.ApplyInputAck(ack.State); failStop {
				ct.needsConfirm = true
				c.emit(CoordTerminalState{
					StreamID:          ct.streamID,
					State:             TermStateFailStop,
					Reason:            "input_fail_stop",
					NeedsConfirmation: true,
				})
			}
		}
		return 0, nil

	case linkcore.ControlStreamFailed:
		sf, err := linkcore.DecodeStreamFailed(o.Payload)
		if err != nil {
			return 0, nil
		}
		ct := c.termStreams[sf.Stream]
		if ct == nil || ct.machineID != r.machine {
			return 0, nil
		}
		_ = ct.stream.ApplyInputAck(sf.State.ToDaemon)
		ct.needsConfirm = true
		c.emit(CoordTerminalState{
			StreamID:          ct.streamID,
			State:             TermStateFailStop,
			Reason:            "stream_failed",
			NeedsConfirmation: true,
		})
		return 4, ackPayload(o)

	case linkcore.ControlInputStatusReply:
		rep, err := linkcore.DecodeInputStatusReply(o.Payload)
		if err != nil {
			return 0, nil
		}
		ct := c.termStreams[rep.Stream]
		if ct == nil || ct.machineID != r.machine {
			return 0, nil
		}
		if !ct.queryInFlight || len(ct.queryID) == 0 || !bytes.Equal(ct.queryID, rep.QueryID) {
			return 0, nil
		}
		if !rep.Available {
			ct.queryInFlight = false
			ct.queryID = nil
			ct.pendingDetails = nil
			ct.disconnectedAll = false
			ct.stream.OnInstanceChanged()
			ct.needsConfirm = true
			c.emit(CoordTerminalState{
				StreamID:          ct.streamID,
				State:             TermStateInstanceChange,
				Reason:            "daemon_unavailable",
				NeedsConfirmation: true,
			})
			return 0, nil
		}
		if !rep.Complete && rep.NextSeq > 0 {
			for _, d := range rep.State.ToDaemon.Details {
				if len(ct.pendingDetails) < inputStatusMaxDetails {
					ct.pendingDetails = append(ct.pendingDetails, d)
				}
			}
			c.sendInputStatusQuery(now, r, ct, rep.NextSeq, rep.SnapshotToken)
			return 0, nil
		}
		ct.queryInFlight = false
		ct.queryID = nil
		if len(ct.pendingDetails) > 0 {
			merged := append(ct.pendingDetails, rep.State.ToDaemon.Details...)
			rep.State.ToDaemon.Details = merged
			ct.pendingDetails = nil
		}
		var tok [32]byte
		copy(tok[:], rep.SnapshotToken)
		snap := linkcore.TermStatusSnapshot{
			Available: true,
			Token:     tok,
			State:     rep.State,
		}
		toResend, needBarrier, needConfirm := ct.stream.ReconcileStatus(uint64(now), snap)
		if needConfirm {
			ct.needsConfirm = true
			ct.disconnectedAll = false
			c.emit(CoordTerminalState{
				StreamID:          ct.streamID,
				State:             TermStateFailStop,
				Reason:            "reconcile_confirm",
				NeedsConfirmation: true,
			})
		} else if needBarrier {
			var bid [16]byte
			if _, err := c.randPool.Read(bid[:]); err == nil {
				c.maybeLowWater()
				body := linkcore.EncodeBarrier(linkcore.BarrierMsg{
					Stream:        ct.streamID,
					BarrierID:     bid[:],
					Requested:     rep.State,
					StopAdmission: true,
				})
				_ = c.stepSession(now, r, linkcore.QueueControl{Type: linkcore.ControlBarrier, Body: body})
			}
		} else {
			wasDisconnected := ct.disconnectedAll || ct.stream.NetworkRecovery
			ct.disconnectedAll = false
			ct.stream.NetworkRecovery = false
			r2, pcID, gen, hasDirect := c.activeTerminalRound(now, ct.machineID)
			if hasDirect {
				c.sendAttachDirectPath(now, r2, pcID, gen, ct)
				if !ct.relayOpen {
					c.triggerRelayRecovery(now, r2, ct)
				}
			}
			for _, f := range toResend {
				if len(f.RawEnvelope) == 0 {
					continue
				}
				var dpc uint64
				if hasDirect {
					dpc = pcID
				}
				c.emit(CoordTerminalSend{
					StreamID:       ct.streamID,
					SendRelay:      ct.relayOpen,
					DirectPCID:     dpc,
					SealedEnvelope: f.RawEnvelope,
				})
			}
			if wasDisconnected {
				c.emit(CoordTerminalState{StreamID: ct.streamID, State: TermStateLive, Reason: "reconciled"})
			}
		}
		return 0, nil

	case linkcore.ControlBarrierReply:
		rep, err := linkcore.DecodeBarrierReply(o.Payload)
		if err != nil {
			return 0, nil
		}
		ct := c.termStreams[rep.Stream]
		if ct == nil || ct.machineID != r.machine {
			return 0, nil
		}
		if rep.Final {
			ct.needsConfirm = true
			ct.disconnectedAll = false
			c.emit(CoordTerminalState{
				StreamID:          ct.streamID,
				State:             TermStateInstanceChange,
				Reason:            "barrier_final",
				NeedsConfirmation: true,
			})
		}
		return 0, nil

	case linkcore.ControlClosePath:
		msg, err := linkcore.DecodeClosePath(o.Payload)
		if err != nil {
			return 0, nil
		}
		ct := c.termStreams[msg.Stream]
		if ct == nil || ct.machineID != r.machine {
			return 0, nil
		}
		if msg.Reason == 2 {
			// N8: daemon signaled explicit output replay buffer overflow -> fail immediately with hint.
			c.emit(CoordTerminalState{
				StreamID: ct.streamID,
				State:    TermStateClosed,
				Reason:   "replay_overflow",
			})
			delete(c.termStreams, ct.streamID)
		}
		return 4, ackPayload(o)
	}
	return 0, nil
}
