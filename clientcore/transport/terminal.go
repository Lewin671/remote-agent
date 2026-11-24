package transport

import (
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

type clientTermStream struct {
	streamID       string
	machineID      string
	sessionID      string
	stream         *linkcore.TerminalStream
	relayOpen      bool
	relayLegID     string
	relayGen       uint64
	directAttached bool
	directPCID     uint64
	directGen      uint64
	needsConfirm   bool
	replaySentSeq  uint64
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
// direct use=1 hot standby when available.
func (c *Coordinator) TerminalDirectOpen(now int64, streamID, machineID, sessionID string, relayLegID []byte) []CoordEffect {
	if streamID == "" || machineID == "" || sessionID == "" {
		return nil
	}
	c.ensureTermStreams()
	if _, exists := c.termStreams[streamID]; !exists && len(c.termStreams) >= linkcore.TermMaxClientStreams {
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
		relayOpen:  true,
		relayLegID: legStr,
		relayGen:   1,
	}
	c.termStreams[streamID] = ct
	c.maybeAttachDirectTerminal(now, ct)
	c.emit(CoordTerminalState{StreamID: streamID, State: TermStateLive})
	return c.take()
}

// TerminalDirectClose detaches and removes a logical terminal stream.
func (c *Coordinator) TerminalDirectClose(now int64, streamID string) []CoordEffect {
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
			ct.relayOpen = true
			if len(legID) > 0 {
				ct.relayLegID = string(legID)
			}
			ct.relayGen++
			_ = ct.stream.AttachLeg("relay", ct.relayLegID, string(c.instance), ct.relayGen)
			c.emit(CoordTerminalState{StreamID: streamID, State: TermStateLive})
			return c.take()
		}
		// Relay leg dropped: if direct leg is live, stay alive and trigger AttachPath relay recovery.
		if len(legID) > 0 && ct.relayLegID != "" && string(legID) != ct.relayLegID {
			// Stale old relay leg closed: ignore.
			return nil
		}
		ct.relayOpen = false
		ct.stream.CloseLeg("relay", ct.relayGen)
		if r, _, _, ok := c.activeTerminalRound(now, ct.machineID); ok && ct.directAttached {
			var rawRand [7]byte
			if _, err := c.randPool.Read(rawRand[:]); err == nil {
				c.maybeLowWater()
				newLeg := makeRelayAttachLegID(rawRand)
				nextGen := ct.relayGen + 1
				if nextGen == 0 {
					nextGen = 1
				}
				body := linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
					Stream:         ct.streamID,
					Session:        ct.sessionID,
					Sender:         c.snap.SelfID,
					LegID:          string(newLeg),
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
					LegID:     newLeg,
				})
				return c.take()
			}
			c.maybeNeedEntropy(CoordEntropyRefillBytes)
			return c.take()
		}
		c.emit(CoordTerminalState{StreamID: streamID, State: TermStateReconnecting, Reason: "relay_closed"})
		return c.take()
	}
	// Direct leg state change.
	if open && pcID != 0 {
		ct.directAttached = true
		ct.directPCID = pcID
	} else if !open && (pcID == 0 || ct.directPCID == pcID) {
		ct.directAttached = false
		ct.directPCID = 0
		ct.stream.CloseLeg("direct", ct.directGen)
		if !ct.relayOpen {
			c.emit(CoordTerminalState{StreamID: streamID, State: TermStateReconnecting, Reason: "direct_closed"})
		}
	}
	return c.take()
}

// TerminalDirectInput caches an outbound sealed terminal frame and emits
// hot-standby dual-send (relay + direct use=1 when ready).
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
	var hash32 [32]byte
	copy(hash32[:], frameHash)
	if err := ct.stream.CacheInput(uint64(now), linkcore.TermFrame{
		Seq:         seq,
		PayloadHash: hash32,
		RawBytes:    len(sealedEnvelope),
		IsInput:     isInput,
		RawEnvelope: append([]byte(nil), sealedEnvelope...),
	}); err != nil {
		c.emit(CoordTerminalState{StreamID: streamID, State: TermStateReconnecting, Reason: "input_cache_full"})
		return c.take()
	}

	var directPC uint64
	if _, pcID, _, ok := c.activeTerminalRound(now, ct.machineID); ok {
		if !ct.directAttached || ct.directPCID != pcID {
			c.maybeAttachDirectTerminal(now, ct)
		}
		directPC = pcID
	}
	if !ct.relayOpen && directPC == 0 {
		c.emit(CoordTerminalState{StreamID: streamID, State: TermStateReconnecting, Reason: "no_leg"})
		return c.take()
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
func (c *Coordinator) TerminalDirectOutput(now int64, streamID string, seq uint64, frameHash, payload []byte, fromDirect bool, pcID uint64) []CoordEffect {
	ct := c.termStreams[streamID]
	if ct == nil || seq == 0 {
		return nil
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
			c.emit(CoordTerminalState{StreamID: streamID, State: TermStateClosed, Reason: "conflict"})
			delete(c.termStreams, streamID)
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
			c.emit(CoordTerminalDeliver{StreamID: streamID, Payloads: payloads})
		}
		if needAck {
			c.sendClientOutputAck(now, ct)
		}
	case linkcore.TermOutputBuffered:
		// Out-of-order frame buffered; 10s gap timer / Replay will fill missing seqs.
	}
	_ = fromDirect
	_ = pcID
	return c.take()
}

// TerminalDirectConfirmRebuild clears the user confirmation gate after fail-stop
// or daemon restart so the shell may rebuild a fresh logical stream.
func (c *Coordinator) TerminalDirectConfirmRebuild(now int64, streamID string) []CoordEffect {
	ct := c.termStreams[streamID]
	if ct == nil {
		return nil
	}
	_ = ct.stream.ConfirmRebuild(nil)
	ct.needsConfirm = false
	c.emit(CoordTerminalState{StreamID: streamID, State: TermStateLive, Reason: "confirmed"})
	return c.take()
}

func makeRelayAttachLegID(rand7 [7]byte) []byte {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 16)
	out[0] = 'r'
	out[1] = ':'
	for i, b := range rand7 {
		out[2+i*2] = hexDigits[b>>4]
		out[2+i*2+1] = hexDigits[b&0x0f]
	}
	return out
}

func (c *Coordinator) maybeAttachDirectTerminal(now int64, ct *clientTermStream) {
	r, pcID, gen, ok := c.activeTerminalRound(now, ct.machineID)
	if !ok {
		return
	}
	if ct.directAttached && ct.directPCID == pcID && ct.directGen == gen {
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
	ct.directAttached = true
	ct.directPCID = pcID
	ct.directGen = gen
	_ = ct.stream.AttachLeg("direct", string(leg), string(c.instance), gen)
	body := linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         ct.streamID,
		Session:        ct.sessionID,
		Sender:         c.snap.SelfID,
		LegID:          string(leg),
		PathGeneration: gen,
		State: linkcore.TermFlowState{
			ToDaemon: ct.stream.CurrentInputWatermark(),
			ToClient: ct.stream.CurrentOutputWatermark(),
		},
	})
	_ = c.stepSession(now, r, linkcore.QueueControl{Type: linkcore.ControlAttachPath, Body: body})
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
			ct.stream.CloseLeg("direct", ct.directGen)
			if !ct.relayOpen {
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
	for _, ct := range c.termStreams {
		if ct == nil {
			continue
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
				c.emit(CoordTerminalState{StreamID: ct.streamID, State: TermStateReconnecting, Reason: "output_gap_timeout"})
			}
		}
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
		if !rep.Available {
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
			_, pcID, _, hasDirect := c.activeTerminalRound(now, ct.machineID)
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
			c.emit(CoordTerminalState{
				StreamID:          ct.streamID,
				State:             TermStateInstanceChange,
				Reason:            "barrier_final",
				NeedsConfirmation: ct.needsConfirm,
			})
		}
		return 0, nil
	}
	return 0, nil
}
