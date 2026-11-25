package sync

import (
	"bytes"
	"crypto/sha256"
	"errors"

	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/security"
	"github.com/Lewin671/remote-agent/clientcore/transport"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

// DeliveredTermHistoryPage carries a decrypted TerminalHistoryPage from Core's
// terminal stream directly into app's termhistory.Machine without shell parsing.
type DeliveredTermHistoryPage struct {
	TerminalID string
	Page       *v1.TerminalHistoryPage
}

const (
	maxTerminalOpenQueueFrames = 512
	maxTerminalOpenQueueBytes  = 2 * 1024 * 1024
	maxTerminalSendQueueFrames = 64
)

type activeTerminal struct {
	opID             uint64
	streamID         string
	sessionID        string
	machineID        string
	terminalID       string
	initialLegID     []byte
	initialLegOpened bool
	openReqID        string
	openConfirmed    bool
	closed           bool
	lastSocketErr    string
	legTokens        map[string]string

	nextOutSeq        uint64
	sendInFlight      bool
	sendPendingFrames int
	waitingEntropy    bool
	entropyRetried    bool
	sendQueue         []*pendingTermSend

	openInFlight          bool
	openPendingFrames     int
	openPendingBytes      int
	pendingConsumedFrames int
	pendingConsumedBytes  int
	openQueue             []*pendingTermOpen
}

type pendingTermSend struct {
	purpose    devicesec.Purpose
	isInput    bool
	frameBytes []byte
}

type pendingTermOpen struct {
	env        *v1.Envelope
	rawBytes   int
	fromDirect bool
	pcID       uint64
}

func (e *Engine) handleTerminalOpen(ctx *StepContext, act *corev1.TerminalOpenAction) {
	if act == nil || act.OpId == 0 {
		return
	}
	machineID := ""
	if sess := e.sessions[act.SessionId]; sess != nil && sess.MachineID != "" {
		machineID = sess.MachineID
	} else {
		machineID = act.MachineId
	}
	if act.SessionId == "" || machineID == "" || e.secEngine == nil {
		e.emitTerminalStateChanged(ctx, &corev1.TerminalStateChangedEffect{
			OpId:        act.OpId,
			State:       corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CLOSED,
			ReasonCode:  "not_found",
			OpenSettled: true,
			OpenOk:      false,
		})
		return
	}
	if _, ok := e.secEngine.SessionSeed(act.SessionId); !ok {
		e.emitTerminalStateChanged(ctx, &corev1.TerminalStateChangedEffect{
			OpId:        act.OpId,
			State:       corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CLOSED,
			ReasonCode:  "not_found",
			OpenSettled: true,
			OpenOk:      false,
		})
		return
	}
	streamID := e.nextID(ctx)
	legID := []byte(streamID)
	ts := &activeTerminal{
		opID:         act.OpId,
		streamID:     streamID,
		sessionID:    act.SessionId,
		machineID:    machineID,
		terminalID:   act.TerminalId,
		initialLegID: legID,
		legTokens:    make(map[string]string),
	}
	if e.activeTerminals == nil {
		e.activeTerminals = make(map[string]*activeTerminal)
	}
	e.activeTerminals[streamID] = ts

	if e.cachedToken == "" && e.running && !e.removed {
		tag := e.allocSecOp(&pendingSecOp{kind: secOpTerminalAuth, streamID: streamID})
		effs := e.secEngine.EnsureAuthTokenTag(e.nowMs, false, relayRpcDefaultTimeoutMs, tag)
		e.emitSecurityEffects(ctx, effs)
		e.maybeRequestEntropy(ctx)
		return
	}
	e.emitTerminalInitialSocket(ctx, ts, e.cachedToken)
}

func (e *Engine) finishTerminalAuth(ctx *StepContext, streamID string, res security.AuthTokenResult) {
	ts := e.activeTerminals[streamID]
	if ts == nil || ts.closed {
		return
	}
	if res.Error != "" || res.Token == "" {
		var errText *corev1.Text
		if res.Error != "" {
			errText = &corev1.Text{Literal: res.Error}
		}
		code := res.ErrorCode
		if code == "" {
			code = errCodeUnauthenticated
		}
		e.failTerminalOpen(ctx, ts, code, errText)
		return
	}
	e.cachedToken = res.Token
	e.tokenExpiresMs = res.ExpiresMs
	e.emitTerminalInitialSocket(ctx, ts, res.Token)
}

func (e *Engine) emitTerminalInitialSocket(ctx *StepContext, ts *activeTerminal, token string) {
	if ts.legTokens == nil {
		ts.legTokens = make(map[string]string)
	}
	ts.legTokens[string(ts.initialLegID)] = token
	helloBytes := (&v1.TermHello{
		Token:     token,
		StreamId:  ts.streamID,
		MachineId: ts.machineID,
	}).AppendMarshal(nil)
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_TerminalOpenSocket{
			TerminalOpenSocket: &corev1.TerminalOpenSocketEffect{
				OpId:       ts.opID,
				StreamId:   ts.streamID,
				LegId:      bytes.Clone(ts.initialLegID),
				HelloBytes: helloBytes,
				IsInitial:  true,
			},
		},
	})
}

func (e *Engine) handleTerminalSocket(ctx *StepContext, ev *corev1.TerminalSocketEvent) {
	if ev == nil || ev.StreamId == "" {
		return
	}
	ts := e.activeTerminals[ev.StreamId]
	if ev.PolicyViolation {
		if ts != nil {
			if tok := ts.legTokens[string(ev.LegId)]; tok != "" {
				e.invalidateCachedToken(ctx, tok)
			}
		}
	}
	if ts == nil || ts.closed {
		return
	}
	isInitialLeg := bytes.Equal(ev.LegId, ts.initialLegID)
	if ev.Opened {
		if isInitialLeg && !ts.initialLegOpened {
			ts.initialLegOpened = true
			// M6: register in Coordinator before submitting OpenTerminal so early frames are buffered.
			e.emitCoordEffects(ctx, e.ensureTransportCoord().TerminalDirectOpen(
				e.nowMs, ts.streamID, ts.machineID, ts.sessionID, ts.initialLegID,
			))
			reqID, dispatched := e.SubmitCommand(ctx, SubmitCommandParams{
				MachineID: ts.machineID,
				SessionID: ts.sessionID,
				Command: &v1.Command{
					Kind: &v1.Command_OpenTerminal{
						OpenTerminal: &v1.OpenTerminalRequest{
							SessionId: ts.sessionID,
							StreamId:  ts.streamID,
						},
					},
				},
				SkipConnectivityPreflight: true,
			})
			if !dispatched || reqID == "" {
				e.failTerminalOpen(ctx, ts, errCodeUnavailable, nil)
				return
			}
			ts.openReqID = reqID
			if e.terminalOpenByReqID == nil {
				e.terminalOpenByReqID = make(map[string]string)
			}
			e.terminalOpenByReqID[reqID] = ts.streamID
			return
		}
		if ts.openConfirmed {
			e.emitCoordEffects(ctx, e.ensureTransportCoord().TerminalDirectLeg(
				e.nowMs, ts.streamID, ev.LegId, false, true, 0,
			))
		}
		return
	}
	if ev.Closed {
		if !ts.openConfirmed {
			if isInitialLeg {
				var errText *corev1.Text
				code := "closed"
				if ev.Error != "" {
					code = "socket_error"
					errText = &corev1.Text{Literal: ev.Error}
				}
				e.failTerminalOpen(ctx, ts, code, errText)
			}
			return
		}
		if ev.Error != "" {
			ts.lastSocketErr = ev.Error
		}
		e.emitCoordEffects(ctx, e.ensureTransportCoord().TerminalDirectLeg(
			e.nowMs, ts.streamID, ev.LegId, false, false, 0,
		))
	}
}

func (e *Engine) onTerminalOpenCmdSettled(
	ctx *StepContext,
	reqID string,
	cat outcome.Category,
	errCode string,
	errReason world.Reason,
) {
	streamID := e.terminalOpenByReqID[reqID]
	delete(e.terminalOpenByReqID, reqID)
	ts := e.activeTerminals[streamID]
	if ts == nil || ts.closed {
		return
	}
	ts.openReqID = ""
	if cat == outcome.Executed && errCode == "" {
		ts.openConfirmed = true
		// H-B: signal Coordinator that daemon confirmed OpenTerminal so direct AttachPath fires now.
		e.emitCoordEffects(ctx, e.ensureTransportCoord().TerminalDirectLeg(
			e.nowMs, ts.streamID, ts.initialLegID, false, true, 0,
		))
		e.emitTerminalStateChanged(ctx, &corev1.TerminalStateChangedEffect{
			OpId:        ts.opID,
			StreamId:    ts.streamID,
			OpenSettled: true,
			OpenOk:      true,
		})
		return
	}
	var errText *corev1.Text
	if errReason.Kind == world.ReasonPlain && errReason.Param != "" {
		errText = &corev1.Text{Literal: errReason.Param}
	}
	code := errCode
	if code == "" {
		code = "open_failed"
	}
	e.failTerminalOpen(ctx, ts, code, errText)
}

func (e *Engine) failTerminalOpen(ctx *StepContext, ts *activeTerminal, reasonCode string, errText *corev1.Text) {
	if ts == nil || ts.closed {
		return
	}
	ts.closed = true
	if ts.openReqID != "" {
		delete(e.terminalOpenByReqID, ts.openReqID)
		e.CancelCommand(ctx, ts.openReqID)
		ts.openReqID = ""
	}
	delete(e.activeTerminals, ts.streamID)
	if ts.initialLegOpened {
		e.emitCoordEffects(ctx, e.ensureTransportCoord().TerminalDirectClose(e.nowMs, ts.streamID))
	}
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_TerminalCloseSocket{
			TerminalCloseSocket: &corev1.TerminalCloseSocketEffect{
				StreamId: ts.streamID,
			},
		},
	})
	e.emitTerminalStateChanged(ctx, &corev1.TerminalStateChangedEffect{
		OpId:        ts.opID,
		StreamId:    ts.streamID,
		State:       corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CLOSED,
		ReasonCode:  reasonCode,
		ReasonText:  errText,
		OpenSettled: !ts.openConfirmed,
		OpenOk:      false,
		OpenError:   errText,
	})
}

func (e *Engine) handleTerminalClose(ctx *StepContext, act *corev1.TerminalCloseAction) {
	if act == nil {
		return
	}
	var ts *activeTerminal
	if act.StreamId != "" {
		ts = e.activeTerminals[act.StreamId]
	} else if act.OpId != 0 {
		for _, cand := range e.activeTerminals {
			if cand.opID == act.OpId {
				ts = cand
				break
			}
		}
	}
	if ts == nil || ts.closed {
		return
	}
	wasConfirmed := ts.openConfirmed
	ts.closed = true
	if ts.openReqID != "" {
		delete(e.terminalOpenByReqID, ts.openReqID)
		e.CancelCommand(ctx, ts.openReqID)
		ts.openReqID = ""
	}
	delete(e.activeTerminals, ts.streamID)
	if ts.initialLegOpened {
		e.emitCoordEffects(ctx, e.ensureTransportCoord().TerminalDirectClose(e.nowMs, ts.streamID))
	}
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_TerminalCloseSocket{
			TerminalCloseSocket: &corev1.TerminalCloseSocketEffect{
				StreamId: ts.streamID,
			},
		},
	})
	if !wasConfirmed {
		e.emitTerminalStateChanged(ctx, &corev1.TerminalStateChangedEffect{
			OpId:        ts.opID,
			StreamId:    ts.streamID,
			State:       corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CLOSED,
			ReasonCode:  "canceled",
			OpenSettled: true,
			OpenOk:      false,
		})
	}
}

func (e *Engine) closeAllActiveTerminals(ctx *StepContext) {
	if len(e.activeTerminals) == 0 {
		return
	}
	list := make([]*activeTerminal, 0, len(e.activeTerminals))
	for _, ts := range e.activeTerminals {
		list = append(list, ts)
	}
	for _, ts := range list {
		e.failTerminalOpen(ctx, ts, "closed", nil)
	}
}

func (e *Engine) handleTerminalInput(ctx *StepContext, act *corev1.TerminalInputAction) {
	if act == nil || act.StreamId == "" {
		return
	}
	ts := e.activeTerminals[act.StreamId]
	if ts == nil || ts.closed || e.secEngine == nil {
		return
	}
	var frame v1.TerminalFrame
	var purpose devicesec.Purpose
	var isInput bool
	switch k := act.Kind.(type) {
	case *corev1.TerminalInputAction_Input:
		frame.Kind = &v1.TerminalFrame_Input{Input: k.Input}
		purpose = devicesec.TerminalInput
		isInput = true
	case *corev1.TerminalInputAction_Resize:
		if k.Resize == nil {
			return
		}
		frame.Kind = &v1.TerminalFrame_Resize{
			Resize: &v1.TerminalResize{
				Cols: k.Resize.Cols,
				Rows: k.Resize.Rows,
				Fit:  k.Resize.Fit,
			},
		}
		purpose = devicesec.TerminalResize
	case *corev1.TerminalInputAction_History:
		if k.History == nil {
			return
		}
		frame.Kind = &v1.TerminalFrame_HistoryRequest{
			HistoryRequest: &v1.TerminalHistoryRequest{
				RequestId: k.History.RequestId,
				Cursor:    k.History.Cursor,
			},
		}
		purpose = devicesec.TerminalHistory
	default:
		return
	}
	if ts.sendPendingFrames >= maxTerminalSendQueueFrames {
		e.failTerminalOpen(ctx, ts, "input_backlog", nil)
		return
	}
	frameBytes := frame.AppendMarshal(nil)
	ts.sendPendingFrames++
	ts.sendQueue = append(ts.sendQueue, &pendingTermSend{
		purpose:    purpose,
		isInput:    isInput,
		frameBytes: frameBytes,
	})
	e.pumpTerminalSendQueue(ctx, ts)
}

func (e *Engine) resumeEntropyWaitingTerminals(ctx *StepContext) {
	for _, ts := range e.activeTerminals {
		if ts != nil && !ts.closed && ts.waitingEntropy {
			ts.waitingEntropy = false
			e.pumpTerminalSendQueue(ctx, ts)
		}
	}
}

func (e *Engine) pumpTerminalSendQueue(ctx *StepContext, ts *activeTerminal) {
	for !ts.closed && !ts.sendInFlight && !ts.waitingEntropy && len(ts.sendQueue) > 0 {
		item := ts.sendQueue[0]
		ts.sendQueue = ts.sendQueue[1:]
		ts.nextOutSeq++
		seq := ts.nextOutSeq
		ts.sendInFlight = true
		tag := e.allocSecOp(&pendingSecOp{
			kind:           secOpTerminalSeal,
			streamID:       ts.streamID,
			termSeq:        seq,
			termIsInput:    item.isInput,
			termFrameBytes: item.frameBytes,
			termItem:       item,
		})
		deadline := uint64(max(int64(0), e.nowMs+CommandTTLMs))
		effs := e.secEngine.SealEnvelopeTag(
			e.nowMs,
			item.purpose,
			ts.machineID,
			ts.sessionID,
			ts.streamID,
			seq,
			deadline,
			item.frameBytes,
			true,
			nil,
			tag,
		)
		e.emitRawEffects(ctx, effs)
	}
}

func (e *Engine) finishTerminalSeal(stepCtx *StepContext, op *pendingSecOp, sr security.SealResult) {
	ts := e.activeTerminals[op.streamID]
	if ts == nil {
		return
	}
	ts.sendInFlight = false
	if ts.closed {
		return
	}
	if sr.Err != nil {
		if errors.Is(sr.Err, security.ErrNeedEntropy) {
			e.maybeRequestEntropy(stepCtx)
			if !ts.entropyRetried && op.termItem != nil {
				// Re-queue this frame at the head, unwind its unused seq, and resume once OS entropy arrives.
				if ts.nextOutSeq > 0 {
					ts.nextOutSeq--
				}
				ts.sendQueue = append([]*pendingTermSend{op.termItem}, ts.sendQueue...)
				ts.waitingEntropy = true
				ts.entropyRetried = true
				return
			}
		}
		if ts.sendPendingFrames > 0 {
			ts.sendPendingFrames--
		}
		e.failTerminalOpen(stepCtx, ts, sessionImageOpenErrorCode(sr.Err), nil)
		return
	}
	ts.entropyRetried = false
	if ts.sendPendingFrames > 0 {
		ts.sendPendingFrames--
	}
	sealedBytes := sr.Envelope.AppendMarshal(nil)
	sum := sha256.Sum256(op.termFrameBytes)
	e.emitCoordEffects(stepCtx, e.ensureTransportCoord().TerminalDirectInput(
		e.nowMs, ts.streamID, op.termSeq, op.termIsInput, sealedBytes, sum[:],
	))
	e.pumpTerminalSendQueue(stepCtx, ts)
}

func (e *Engine) handleTerminalInboundBytes(ctx *StepContext, ev *corev1.TerminalInboundBytesEvent) {
	if ev == nil || ev.StreamId == "" || e.secEngine == nil {
		return
	}
	ts := e.activeTerminals[ev.StreamId]
	if ts == nil || ts.closed {
		return
	}
	rawLen := len(ev.RawEnvelope)
	if ts.openPendingFrames >= maxTerminalOpenQueueFrames || ts.openPendingBytes+rawLen > maxTerminalOpenQueueBytes {
		e.failTerminalOpen(ctx, ts, "backlog", nil)
		return
	}
	var env v1.Envelope
	if err := env.Unmarshal(ev.RawEnvelope); err != nil || env.HasUnknown() {
		if !ev.FromDirect {
			e.failTerminalOpen(ctx, ts, "invalid", nil)
			return
		}
		e.emitTerminalConsumedAck(ctx, ts.streamID, 1, rawLen)
		return
	}
	ts.openPendingFrames++
	ts.openPendingBytes += rawLen
	ts.openQueue = append(ts.openQueue, &pendingTermOpen{
		env:        &env,
		rawBytes:   rawLen,
		fromDirect: ev.FromDirect,
		pcID:       ev.PcId,
	})
	e.pumpTerminalOpenQueue(ctx, ts)
}

func (e *Engine) emitTerminalConsumedAck(ctx *StepContext, streamID string, frames, rawBytes int) {
	if frames <= 0 && rawBytes <= 0 {
		return
	}
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_TerminalDeliverOutput{
			TerminalDeliverOutput: &corev1.TerminalDeliverOutputEffect{
				StreamId:       streamID,
				ConsumedFrames: uint32(max(frames, 0)),
				ConsumedBytes:  uint32(max(rawBytes, 0)),
			},
		},
	})
}

func (e *Engine) pumpTerminalOpenQueue(ctx *StepContext, ts *activeTerminal) {
	for !ts.closed && !ts.openInFlight && len(ts.openQueue) > 0 {
		item := ts.openQueue[0]
		ts.openQueue = ts.openQueue[1:]
		ts.openInFlight = true
		tag := e.allocSecOp(&pendingSecOp{
			kind:         secOpTerminalOpen,
			streamID:     ts.streamID,
			fromDirect:   item.fromDirect,
			pcID:         item.pcID,
			termRawBytes: item.rawBytes,
		})
		effs := e.secEngine.OpenBatchTag(e.nowMs, []security.OpenItem{{
			Env:             item.env,
			ExpectedPurpose: devicesec.TerminalOutput,
			MachineID:       ts.machineID,
			SessionID:       ts.sessionID,
			RequestID:       ts.streamID,
			History:         false,
		}}, tag)
		e.emitRawEffects(ctx, effs)
	}
}

func (e *Engine) finishTerminalOpen(stepCtx *StepContext, op *pendingSecOp, results []security.OpenResult) {
	ts := e.activeTerminals[op.streamID]
	if ts == nil {
		return
	}
	ts.openInFlight = false
	if ts.openPendingFrames > 0 {
		ts.openPendingFrames--
	}
	ts.openPendingBytes = max(0, ts.openPendingBytes-op.termRawBytes)
	if ts.closed {
		return
	}
	if len(results) != 1 || results[0].Err != nil {
		var err error
		if len(results) == 1 {
			err = results[0].Err
		}
		if !op.fromDirect {
			e.failTerminalOpen(stepCtx, ts, sessionImageOpenErrorCode(err), nil)
			return
		}
		e.emitTerminalConsumedAck(stepCtx, ts.streamID, 1, op.termRawBytes)
		e.pumpTerminalOpenQueue(stepCtx, ts)
		return
	}
	r := results[0]
	sum := sha256.Sum256(r.Plain)
	ts.pendingConsumedFrames++
	ts.pendingConsumedBytes += op.termRawBytes
	e.emitCoordEffects(stepCtx, e.ensureTransportCoord().TerminalDirectOutput(
		e.nowMs, ts.streamID, r.Header.Seq, sum[:], r.Plain, op.fromDirect, op.pcID,
	))
	if !ts.closed && (ts.pendingConsumedFrames > 0 || ts.pendingConsumedBytes > 0) {
		cf, cb := ts.pendingConsumedFrames, ts.pendingConsumedBytes
		ts.pendingConsumedFrames = 0
		ts.pendingConsumedBytes = 0
		e.emitTerminalConsumedAck(stepCtx, ts.streamID, cf, cb)
	}
	e.pumpTerminalOpenQueue(stepCtx, ts)
}

func (e *Engine) handleTerminalConfirmRebuild(ctx *StepContext, act *corev1.TerminalConfirmRebuildAction) {
	if act == nil || act.StreamId == "" {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().TerminalDirectConfirmRebuild(e.nowMs, act.StreamId))
}

func (e *Engine) handleTerminalCoordDeliver(ctx *StepContext, eff transport.CoordTerminalDeliver) bool {
	ts := e.activeTerminals[eff.StreamID]
	if ts == nil {
		return false
	}
	if ts.closed {
		return true
	}
	var frames []*corev1.TerminalDeliveredFrame
	for _, raw := range eff.Payloads {
		var tf v1.TerminalFrame
		if err := tf.Unmarshal(raw); err != nil {
			continue
		}
		switch k := tf.Kind.(type) {
		case *v1.TerminalFrame_Output:
			frames = append(frames, &corev1.TerminalDeliveredFrame{
				Kind: &corev1.TerminalDeliveredFrame_Output{Output: k.Output},
			})
		case *v1.TerminalFrame_Snapshot:
			if k.Snapshot != nil {
				frames = append(frames, &corev1.TerminalDeliveredFrame{
					Kind: &corev1.TerminalDeliveredFrame_Snapshot{
						Snapshot: &corev1.TerminalSnapshotFrame{
							Cols: k.Snapshot.Cols,
							Rows: k.Snapshot.Rows,
							Data: k.Snapshot.Data,
						},
					},
				})
			}
		case *v1.TerminalFrame_Closed:
			frames = append(frames, &corev1.TerminalDeliveredFrame{
				Kind: &corev1.TerminalDeliveredFrame_Closed{Closed: k.Closed},
			})
		case *v1.TerminalFrame_HistoryPage:
			if k.HistoryPage != nil && ts.terminalID != "" {
				ctx.DeliveredTermHistoryPages = append(ctx.DeliveredTermHistoryPages, DeliveredTermHistoryPage{
					TerminalID: ts.terminalID,
					Page:       k.HistoryPage,
				})
			}
		}
	}
	if len(frames) > 0 {
		cf, cb := ts.pendingConsumedFrames, ts.pendingConsumedBytes
		ts.pendingConsumedFrames = 0
		ts.pendingConsumedBytes = 0
		// Hot path: emit directly without setting any DirtyFlags (zero RenderEffect / zero VM recompute).
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_TerminalDeliverOutput{
				TerminalDeliverOutput: &corev1.TerminalDeliverOutputEffect{
					StreamId:       eff.StreamID,
					Frames:         frames,
					ConsumedFrames: uint32(max(cf, 0)),
					ConsumedBytes:  uint32(max(cb, 0)),
				},
			},
		})
	}
	return true
}

func (e *Engine) handleTerminalCoordAttachRelay(ctx *StepContext, eff transport.CoordTerminalAttachRelay) bool {
	ts := e.activeTerminals[eff.StreamID]
	if ts == nil {
		return false
	}
	if ts.closed {
		return true
	}
	if ts.legTokens == nil {
		ts.legTokens = make(map[string]string)
	}
	ts.legTokens[string(eff.LegID)] = e.cachedToken
	helloBytes := (&v1.TermHello{
		Token:     e.cachedToken,
		StreamId:  string(eff.LegID),
		MachineId: eff.MachineID,
	}).AppendMarshal(nil)
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_TerminalOpenSocket{
			TerminalOpenSocket: &corev1.TerminalOpenSocketEffect{
				OpId:       ts.opID,
				StreamId:   eff.StreamID,
				LegId:      bytes.Clone(eff.LegID),
				HelloBytes: helloBytes,
				IsInitial:  false,
			},
		},
	})
	return true
}

func (e *Engine) handleTerminalCoordState(ctx *StepContext, eff transport.CoordTerminalState) bool {
	ts := e.activeTerminals[eff.StreamID]
	if ts == nil {
		return false
	}
	if ts.closed {
		return true
	}
	if eff.NeedsConfirmation {
		nextState := corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CONFIRM_INPUT
		if eff.State == transport.TermStateInstanceChange {
			nextState = corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CONFIRM_RESTART
		}
		e.emitTerminalStateChanged(ctx, &corev1.TerminalStateChangedEffect{
			OpId:       ts.opID,
			StreamId:   eff.StreamID,
			State:      nextState,
			ReasonCode: eff.Reason,
		})
		return true
	}
	if eff.State == transport.TermStateClosed && eff.Reason == "rebuild_confirmed" {
		ts.closed = true
		delete(e.activeTerminals, eff.StreamID)
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_TerminalCloseSocket{
				TerminalCloseSocket: &corev1.TerminalCloseSocketEffect{StreamId: eff.StreamID},
			},
		})
		e.emitTerminalStateChanged(ctx, &corev1.TerminalStateChangedEffect{
			OpId:       ts.opID,
			StreamId:   eff.StreamID,
			State:      corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_REBUILD,
			ReasonCode: "rebuild_confirmed",
		})
		return true
	}
	if eff.State == transport.TermStateClosed {
		ts.closed = true
		delete(e.activeTerminals, eff.StreamID)
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_TerminalCloseSocket{
				TerminalCloseSocket: &corev1.TerminalCloseSocketEffect{StreamId: eff.StreamID},
			},
		})
		reasonCode := eff.Reason
		var reasonText *corev1.Text
		if eff.Reason != "conflict" && eff.Reason != "replay_overflow" && ts.lastSocketErr != "" {
			reasonCode = "socket_error"
			reasonText = &corev1.Text{Literal: ts.lastSocketErr}
		}
		e.emitTerminalStateChanged(ctx, &corev1.TerminalStateChangedEffect{
			OpId:        ts.opID,
			StreamId:    eff.StreamID,
			State:       corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CLOSED,
			ReasonCode:  reasonCode,
			ReasonText:  reasonText,
			OpenSettled: !ts.openConfirmed,
			OpenOk:      false,
			OpenError:   reasonText,
		})
		return true
	}
	return true
}

func (e *Engine) emitTerminalStateChanged(ctx *StepContext, st *corev1.TerminalStateChangedEffect) {
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_TerminalStateChanged{
			TerminalStateChanged: st,
		},
	})
}
