package sync

import (
	"bytes"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

const (
	errCodeUnknown         = "unknown"
	errCodeExpired         = "expired"
	errCodeFailedPrecond   = "failed_precondition"
	errCodeUnavailable     = "unavailable"
	errCodeUnauthenticated = "unauthenticated"
	errCodeNetwork         = "network"
	errCodePathChanged     = "path_changed"
	errCodeUnresolvedHost  = "unresolved_host"
)

// DispatchState mirrors shared outcome evidence for one PreparedCommand across its attempts.
type DispatchState struct {
	PriorUnknown bool
	Possible     int
	Active       int
}

// Unresolved reports whether any prior or active delivery may have reached the daemon.
func (d *DispatchState) Unresolved() bool {
	return d.PriorUnknown || d.Possible > 0 || d.Active > 0
}

// HasPrior reports whether an earlier attempt may have reached the daemon.
func (d *DispatchState) HasPrior() bool {
	return d.PriorUnknown || d.Possible > 0
}

// CommandLeg tracks one in-flight transport attempt leg (direct vs relay) of a PreparedCommand.
type CommandLeg struct {
	AttemptID string
	Direct    bool
	TicketID  uint64
	HandedOff bool
	Done      bool
	Outcome   *corev1.CommandOutcomeEvent
}

// PreparedCommand preserves a fixed RequestID, DeadlineMs, and serialized command payload across retries.
type PreparedCommand struct {
	RequestID          string
	SlotKey            string
	MachineID          string
	SessionID          string
	DeadlineMs         int64
	RecoveryDeadlineMs int64
	Command            *v1.Command
	CommandBytes       []byte
	RouteHint                 corev1.CommandRouteHint
	ForceRelay                bool
	SkipConnectivityPreflight bool
	Dispatch           DispatchState
	AttemptCount       int
	RecoveryUntilMs    int64
	PendingAttempts    map[string]bool
	DualMode           bool
	ActiveLegs         map[string]*CommandLeg
	DirectLegID        string
	RelayLegID         string
	BulkFileKey        string
	BulkBytes          int64
	BulkUpload         bool
	Sealing         bool
	SealingRetry    bool
	SealedEnvelope  *v1.Envelope
	SealedHeadHash  [32]byte
	RequestKeyHash  []byte
	PayloadHash     []byte

	VerifiedSealedResult *v1.Envelope
}

// AttemptTracker preserves one unknown command per UI action slot until settled or explicitly abandoned.
type AttemptTracker struct {
	Pending *PreparedCommand
	Frozen  bool
	Expired bool
}

// SubmitCommandParams configures a command submission through the sync engine.
type SubmitCommandParams struct {
	SlotKey     string
	MachineID   string
	SessionID   string
	Command     *v1.Command
	BulkFileKey               string
	BulkBytes                 int64
	BulkUpload                bool
	SkipConnectivityPreflight bool
}

// SubmitCommand creates (or replays if frozen on SlotKey) a PreparedCommand and dispatches an attempt.
func (e *Engine) SubmitCommand(ctx *StepContext, in SubmitCommandParams) (requestID string, dispatched bool) {
	if in.Command == nil || in.MachineID == "" {
		return "", false
	}
	var tracker *AttemptTracker
	retryOfUnknown := false
	var prep *PreparedCommand

	if in.SlotKey != "" {
		tracker = e.attemptTrackers[in.SlotKey]
		if tracker == nil {
			tracker = &AttemptTracker{}
			e.attemptTrackers[in.SlotKey] = tracker
		}
		if tracker.Expired {
			ctx.Settled = append(ctx.Settled, SettledCommand{
				SlotKey:   in.SlotKey,
				MachineID: in.MachineID,
				SessionID: in.SessionID,
				Category:  outcome.Unknown,
				Expired:     true,
				Frozen:      true,
				ErrorCode:   errCodeUnknown,
				ErrorReason: world.Reason{Kind: world.ReasonPreviousOutcomeUnknown},
			})
			return "", false
		}
		if tracker.Frozen && tracker.Pending != nil {
			prep = tracker.Pending
			retryOfUnknown = true
		}
	}

	if prep == nil {
		cmd := in.Command.Clone()
		cmd.ProtocolVersion = PeerVersion
		raw, err := cmd.Marshal()
		if err != nil {
			return "", false
		}
		deadline := e.nowMs + CommandTTLMs
		prep = &PreparedCommand{
			RequestID:          e.nextID(ctx),
			SlotKey:            in.SlotKey,
			MachineID:          in.MachineID,
			SessionID:          in.SessionID,
			DeadlineMs:         deadline,
			RecoveryDeadlineMs: deadline + ResultRetentionMs,
			Command:            cmd,
			CommandBytes:       raw,
			RouteHint:          routeHintFor(cmd),
			PendingAttempts:    make(map[string]bool),
			BulkFileKey:               in.BulkFileKey,
			BulkBytes:                 in.BulkBytes,
			BulkUpload:                in.BulkUpload,
			SkipConnectivityPreflight: in.SkipConnectivityPreflight,
		}
		if tracker != nil {
			tracker.Pending = prep
		}
	}

	prep.Dispatch.PriorUnknown = prep.Dispatch.PriorUnknown || retryOfUnknown
	e.trackCommandTransferLifecycle(prep.Command, prep.MachineID)
	if !e.preflightAndEmit(ctx, prep, retryOfUnknown) {
		return prep.RequestID, false
	}
	return prep.RequestID, true
}

// HasFrozenAttempt reports whether slotKey holds a frozen or expired unknown-outcome attempt.
func (e *Engine) HasFrozenAttempt(slotKey string) bool {
	if slotKey == "" {
		return false
	}
	tr := e.attemptTrackers[slotKey]
	return tr != nil && (tr.Frozen || tr.Expired)
}

// AbandonAttempt clears any frozen/expired attempt associated with slotKey.
func (e *Engine) AbandonAttempt(ctx *StepContext, slotKey string) {
	if slotKey == "" {
		return
	}
	if tr, ok := e.attemptTrackers[slotKey]; ok {
		if tr.Pending != nil {
			tr.Pending.SlotKey = ""
		}
		delete(e.attemptTrackers, slotKey)
	}
	ctx.Dirty.Add(true, true, true)
}

// CancelCommand cancels an in-flight untracked (read-only, SlotKey=="") command and its retry timer.
func (e *Engine) CancelCommand(ctx *StepContext, requestID string) {
	if requestID == "" {
		return
	}
	prep := e.inFlightCmds[requestID]
	if prep == nil || prep.SlotKey != "" {
		return
	}
	delete(e.inFlightCmds, requestID)
	delete(e.retryReqByTimer, requestID)
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_COMMAND_RETRY, requestID)
	for aid := range prep.PendingAttempts {
		delete(e.reqIDByAttempt, aid)
		if e.relayPipe {
			e.cancelRelayForwardAttempt(ctx, aid)
		}
	}
	e.releaseOrDeferLegs(prep, "")
	clear(prep.PendingAttempts)
	clear(prep.ActiveLegs)
	prep.DualMode = false
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_CancelDispatch{
			CancelDispatch: &corev1.CancelDispatchEffect{
				RequestId: requestID,
			},
		},
	})
}

// AttemptState reports whether slotKey currently has a frozen or expired unknown attempt.
func (e *Engine) AttemptState(slotKey string) (frozen, expired bool, pendingReqID string) {
	tr := e.attemptTrackers[slotKey]
	if tr == nil {
		return false, false, ""
	}
	if tr.Pending != nil {
		pendingReqID = tr.Pending.RequestID
	}
	return tr.Frozen, tr.Expired, pendingReqID
}

func (e *Engine) preflightAndEmit(ctx *StepContext, prep *PreparedCommand, retryOfUnknown bool) bool {
	prior := retryOfUnknown || prep.Dispatch.Unresolved()
	if e.nowMs > 0 && prep.RecoveryDeadlineMs > 0 && e.nowMs >= prep.RecoveryDeadlineMs {
		if prior {
			e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, world.Reason{Kind: world.ReasonRelayNoVerifiableResult}, true)
		} else {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeExpired, world.Reason{Kind: world.ReasonConnectionTimedOut}, true)
		}
		return false
	}
	if !prior && e.nowMs > 0 && prep.DeadlineMs > 0 && e.nowMs >= prep.DeadlineMs {
		e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeExpired, world.Reason{Kind: world.ReasonConnectionTimedOut}, false)
		return false
	}
	if e.serverAhead || e.security.ClockSkewed {
		if prior {
			e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, world.Reason{Kind: world.ReasonClockBehind}, false)
		} else {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeFailedPrecond, world.Reason{Kind: world.ReasonClockBehind}, false)
		}
		return false
	}
	if e.security.Loaded && (!e.security.IsMember || (e.security.LeaseEndMs > 0 && e.nowMs >= e.security.LeaseEndMs)) {
		if prior {
			e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, world.Reason{Kind: world.ReasonReplyUnverified}, false)
		} else {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeFailedPrecond, world.Reason{Kind: world.ReasonRemoteControlExpired}, false)
		}
		return false
	}

	m := e.machines[prep.MachineID]
	if errReason := checkFileCapability(prep.Command, m, prior); !errReason.IsZero() {
		if prior {
			e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, errReason, false)
		} else {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeFailedPrecond, errReason, false)
		}
		return false
	}
	if errReason := checkProtocolMismatch(m, prior); !errReason.IsZero() {
		if prior {
			e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, errReason, false)
		} else {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeFailedPrecond, errReason, false)
		}
		return false
	}
	if !prior {
		if !e.networkAvailable {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeUnavailable, world.Reason{Kind: world.ReasonNoNetwork}, false)
			return false
		}
		if !prep.SkipConnectivityPreflight {
			if e.w.World.Connection != lists.ConnConnected {
				e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeUnavailable, world.Reason{Kind: world.ReasonNotConnected}, false)
				return false
			}
			if m == nil || !e.machineReachable(m) {
				e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeUnavailable, world.Reason{Kind: world.ReasonComputerOffline}, false)
				return false
			}
		}
	}
	if !retryOfUnknown && isObservedAction(prep.Command) {
		if errReason := e.checkObservedAction(prep); !errReason.IsZero() {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeFailedPrecond, errReason, false)
			return false
		}
	}

	e.emitCommandAttempt(ctx, prep, retryOfUnknown)
	return true
}

func (e *Engine) emitCommandLeg(
	ctx *StepContext,
	prep *PreparedCommand,
	retryOfUnknown bool,
	attemptID string,
	leg corev1.CommandRouteLeg,
	dec directRouteArg,
) {
	prep.Dispatch.Active++
	prep.Dispatch.Possible++
	prep.PendingAttempts[attemptID] = true
	e.reqIDByAttempt[attemptID] = prep.RequestID
	isDirect := leg == corev1.CommandRouteLeg_COMMAND_ROUTE_LEG_DIRECT
	prep.ActiveLegs[attemptID] = &CommandLeg{
		AttemptID: attemptID,
		Direct:    isDirect,
		TicketID:  dec.ticketID,
	}
	if e.relayPipe && !isDirect {
		e.emitCommandRelayLegViaPipe(ctx, prep, attemptID)
		return
	}
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_DispatchCommand{
			DispatchCommand: &corev1.DispatchCommandEffect{
				RequestId:       prep.RequestID,
				AttemptId:       attemptID,
				MachineId:       prep.MachineID,
				SessionId:       prep.SessionID,
				DeadlineMs:      prep.DeadlineMs,
				CommandBytes:    prep.CommandBytes,
				RouteHint:       prep.RouteHint,
				RetryOfUnknown:  retryOfUnknown,
				ForceRelay:      prep.ForceRelay,
				BulkFileKey:     prep.BulkFileKey,
				BulkBytes:       prep.BulkBytes,
				BulkUpload:      prep.BulkUpload,
				RouteLeg:        leg,
				DirectPcId:      dec.pcID,
				DirectTimeoutMs: dec.timeoutMs,
				DirectTicketId:  dec.ticketID,
				SealedEnvelope:  prep.SealedEnvelope,
			},
		},
	})
}

func (e *Engine) emitCommandAttempt(ctx *StepContext, prep *PreparedCommand, retryOfUnknown bool) {
	if e.secEngine != nil && e.secEngine.Locked() {
		if prep.SealedEnvelope != nil {
			if curHead, ok := e.secEngine.CurrentHeadHash(); ok && prep.SealedHeadHash != curHead {
				prep.SealedEnvelope = nil
				prep.SealedHeadHash = [32]byte{}
				prep.RequestKeyHash = nil
				prep.PayloadHash = nil
			}
		}
		if prep.SealedEnvelope == nil {
			e.inFlightCmds[prep.RequestID] = prep
			e.sealAndEmitCommandAttempt(ctx, prep, retryOfUnknown)
			return
		}
	}
	if prep.ActiveLegs == nil {
		prep.ActiveLegs = make(map[string]*CommandLeg)
	} else {
		if e.transportCoord != nil {
			for _, l := range prep.ActiveLegs {
				if l != nil && l.TicketID != 0 {
					e.transportCoord.ReleaseTicket(l.TicketID)
					l.TicketID = 0
				}
			}
		}
		clear(prep.ActiveLegs)
	}
	prep.DualMode = false
	prep.DirectLegID = ""
	prep.RelayLegID = ""
	e.inFlightCmds[prep.RequestID] = prep

	phase2Ready := e.transportCoord != nil && e.transportCoord.Phase2CommandsReady(e.nowMs, prep.MachineID)
	if !prep.ForceRelay && (isLegacyInteractiveCommand(prep) || (phase2Ready && isDualSendCommand(prep))) {
		if dec, ok := e.transportCoord.PrepareInteractive(e.nowMs, prep.MachineID, prep.DeadlineMs); ok {
			attemptD := e.nextID(ctx)
			attemptR := e.nextID(ctx)
			prep.DualMode = true
			prep.DirectLegID = attemptD
			prep.RelayLegID = attemptR
			e.emitCommandLeg(ctx, prep, retryOfUnknown, attemptD, corev1.CommandRouteLeg_COMMAND_ROUTE_LEG_DIRECT, directRouteArg{
				pcID:      dec.PCID,
				timeoutMs: dec.TimeoutMs,
				ticketID:  dec.TicketID,
			})
			e.emitCommandLeg(ctx, prep, retryOfUnknown, attemptR, corev1.CommandRouteLeg_COMMAND_ROUTE_LEG_RELAY, directRouteArg{})
			e.syncDirectTimers(ctx)
			return
		}
	}

	if !prep.ForceRelay {
		if h, ok := e.commandTransportHint(prep); ok {
			dec := e.transportCoord.PrepareAttempt(e.nowMs, prep.MachineID, h, prep.DeadlineMs, prep.ForceRelay)
			e.syncDirectTimers(ctx)
			attemptID := e.nextID(ctx)
			leg := corev1.CommandRouteLeg_COMMAND_ROUTE_LEG_RELAY
			var arg directRouteArg
			if dec.UseDirect {
				leg = corev1.CommandRouteLeg_COMMAND_ROUTE_LEG_DIRECT
			}
			arg = directRouteArg{pcID: dec.PCID, timeoutMs: dec.TimeoutMs, ticketID: dec.TicketID}
			e.emitCommandLeg(ctx, prep, retryOfUnknown, attemptID, leg, arg)
			return
		}
	}

	attemptID := e.nextID(ctx)
	e.emitCommandLeg(ctx, prep, retryOfUnknown, attemptID, corev1.CommandRouteLeg_COMMAND_ROUTE_LEG_RELAY, directRouteArg{})
}

type directRouteArg struct {
	pcID      uint64
	timeoutMs int64
	ticketID  uint64
}

func (e *Engine) handleCommandOutcome(ctx *StepContext, ev *corev1.CommandOutcomeEvent) {
	if ev == nil || ev.RequestId == "" {
		return
	}
	if ev.Source == corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON && ev.SealedResult != nil && e.secEngine != nil {
		if prep := e.inFlightCmds[ev.RequestId]; prep != nil && (ev.AttemptId == "" || prep.PendingAttempts[ev.AttemptId]) {
			e.openSealedCommandOutcome(ctx, prep, ev)
			return
		}
	}
	e.finishCommandOutcome(ctx, ev)
}

func (e *Engine) finishCommandOutcome(ctx *StepContext, ev *corev1.CommandOutcomeEvent) {
	if ev.AttemptId != "" {
		delete(e.reqIDByAttempt, ev.AttemptId)
		if lateTicketID, isLate := e.lateDirectTickets[ev.AttemptId]; isLate {
			delete(e.lateDirectTickets, ev.AttemptId)
			delete(e.lateDirectDeadline, ev.AttemptId)
			if c := e.transportCoord; c != nil && lateTicketID != 0 {
				daemonOK := ev.Source == corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON &&
					classifyDaemonOutcome(ev, true) != outcome.Unknown
				if daemonOK {
					e.emitCoordEffects(ctx, c.ObserveTicket(e.nowMs, lateTicketID, ev.RouteWasDirect, ev.ObservedBytes, ev.ElapsedMs))
				} else if ev.RouteWasDirect && ev.Source != corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON {
					e.emitCoordEffects(ctx, c.FailTicket(e.nowMs, lateTicketID))
				} else {
					c.ReleaseTicket(lateTicketID)
				}
			}
			return
		}
	}
	prep := e.inFlightCmds[ev.RequestId]
	if prep == nil {
		return
	}
	if ev.AttemptId != "" {
		if !prep.PendingAttempts[ev.AttemptId] {
			return
		}
		delete(prep.PendingAttempts, ev.AttemptId)
		if prep.Dispatch.Active > 0 {
			prep.Dispatch.Active--
		}
	}

	var leg *CommandLeg
	if len(prep.ActiveLegs) > 0 && ev.AttemptId != "" {
		leg = prep.ActiveLegs[ev.AttemptId]
	}

	priorBefore := prep.Dispatch.PriorUnknown || (prep.Dispatch.Possible > 1) || (ev.Source != corev1.OutcomeSource_OUTCOME_SOURCE_LOCAL && !ev.NotForwarded)

	if c := e.transportCoord; c != nil {
		ticketID := ev.DirectTicketId
		if ticketID == 0 && leg != nil {
			ticketID = leg.TicketID
		}
		daemonOK := false
		if ev.Source == corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON {
			cat := classifyDaemonOutcome(ev, priorBefore)
			daemonOK = cat != outcome.Unknown
			if ticketID != 0 {
				if daemonOK {
					e.emitCoordEffects(ctx, c.ObserveTicket(e.nowMs, ticketID, ev.RouteWasDirect, ev.ObservedBytes, ev.ElapsedMs))
				} else {
					c.ReleaseTicket(ticketID)
				}
				if leg != nil {
					leg.TicketID = 0
				}
			}
		} else if ticketID != 0 {
			if ev.RouteWasDirect {
				e.emitCoordEffects(ctx, c.FailTicket(e.nowMs, ticketID))
			} else {
				c.ReleaseTicket(ticketID)
			}
			if leg != nil {
				leg.TicketID = 0
			}
		}

		if prep.DualMode && leg != nil {
			if daemonOK {
				if e.relayPipe && prep.RelayLegID != "" && leg.AttemptID != prep.RelayLegID {
					e.cancelRelayForwardAttempt(ctx, prep.RelayLegID)
				}
				e.releaseOrDeferLegs(prep, leg.AttemptID)
				prep.DualMode = false
				clear(prep.ActiveLegs)
			} else {
				leg.Done = true
				evCopy := *ev
				leg.Outcome = &evCopy
				leg.HandedOff = ev.HandedOff || !ev.NotForwarded
				if ev.RouteWasDirect {
					prep.ForceRelay = true
				}
				if !leg.HandedOff && prep.Dispatch.Possible > 0 {
					prep.Dispatch.Possible--
				}
				var sibling *CommandLeg
				for id, l := range prep.ActiveLegs {
					if id != leg.AttemptID {
						sibling = l
						break
					}
				}
				if sibling != nil && !sibling.Done {
					return
				}
				// Both legs finished without a verified daemon result.
				directLeg := prep.ActiveLegs[prep.DirectLegID]
				relayLeg := prep.ActiveLegs[prep.RelayLegID]
				chosen := evCopy
				if relayLeg != nil && relayLeg.Outcome != nil {
					chosen = *relayLeg.Outcome
				}
				serverNotForwarded := chosen.Source == corev1.OutcomeSource_OUTCOME_SOURCE_SERVER && chosen.NotForwarded && chosen.AttemptBound
				relayPossiblyForwarded := relayLeg != nil && relayLeg.HandedOff && !serverNotForwarded
				if (directLeg != nil && directLeg.HandedOff) || relayPossiblyForwarded {
					chosen.NotForwarded = false
					if chosen.Source == corev1.OutcomeSource_OUTCOME_SOURCE_LOCAL || chosen.Source == corev1.OutcomeSource_OUTCOME_SOURCE_SERVER {
						chosen.Source = corev1.OutcomeSource_OUTCOME_SOURCE_TRANSPORT
					}
					prep.Dispatch.PriorUnknown = true
				}
				prep.DualMode = false
				clear(prep.ActiveLegs)
				prep.ForceRelay = true
				ev = &chosen
				priorBefore = prep.Dispatch.PriorUnknown || (prep.Dispatch.Possible > 1) || (ev.Source != corev1.OutcomeSource_OUTCOME_SOURCE_LOCAL && !ev.NotForwarded)
			}
		} else if !prep.DualMode && ev.RouteWasDirect && ev.Source != corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON {
			prep.ForceRelay = true
			if ev.NotForwarded && !ev.HandedOff {
				if prep.Dispatch.Possible > 0 {
					prep.Dispatch.Possible--
				}
			} else {
				prep.Dispatch.PriorUnknown = true
			}
			deadlineLimit := prep.DeadlineMs
			if ev.TransportCode == errCodePathChanged {
				deadlineLimit = prep.RecoveryDeadlineMs
			}
			if e.nowMs < deadlineLimit && e.running {
				e.emitCommandAttempt(ctx, prep, prep.Dispatch.HasPrior())
				return
			}
		}
	}

	switch ev.Source {
	case corev1.OutcomeSource_OUTCOME_SOURCE_LOCAL:
		if prep.Dispatch.Possible > 0 {
			prep.Dispatch.Possible--
		}
		if ev.NotForwarded && !prep.Dispatch.HasPrior() && prep.Dispatch.Active == 0 {
			code := ev.TransportCode
			if code == "" {
				code = "failed_precondition"
			}
			e.settleCommand(ctx, prep, outcome.NotSent, nil, code, classifyTransportError(ev), false)
			return
		}
		prep.Dispatch.PriorUnknown = true
		e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, world.Reason{Kind: world.ReasonReplyUnverified}, false)

	case corev1.OutcomeSource_OUTCOME_SOURCE_SERVER:
		if ev.NotForwarded && ev.AttemptBound {
			if prep.Dispatch.Possible > 0 {
				prep.Dispatch.Possible--
			}
			if ev.TransportCode == errCodeUnauthenticated && prep.AttemptCount < CommandRetryMaxAttempts && e.running {
				prep.AttemptCount++
				if !e.security.Loaded || e.security.VerifiedNetGen != e.networkGen {
					e.requestSyncSecurity(ctx, true)
				}
				e.emitCommandAttempt(ctx, prep, prep.Dispatch.HasPrior())
				return
			}
			if !prep.Dispatch.HasPrior() && prep.Dispatch.Active == 0 {
				code := ev.TransportCode
				if code == "" {
					code = ev.OuterReason
				}
				e.settleCommand(ctx, prep, outcome.NotSent, nil, code, classifyServerRefusal(ev), false)
				return
			}
			prep.Dispatch.PriorUnknown = true
			// Completing an upload has no side effect beyond accepting bytes
			// already on the computer, so keep retrying it through a
			// reconnect. Every other command keeps its outcome unknown here.
			if ev.TransportCode == errCodeUnavailable && prep.Command.GetCompleteImageUpload() != nil {
				e.handleRetryableTransportOutcome(ctx, prep, ev)
				return
			}
			e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, world.Reason{Kind: world.ReasonReplyUnverified}, false)
			return
		}
		prep.Dispatch.PriorUnknown = true
		e.handleRetryableTransportOutcome(ctx, prep, ev)

	case corev1.OutcomeSource_OUTCOME_SOURCE_TRANSPORT:
		if ev.TransportCode == errCodePathChanged {
			if prep.Dispatch.Possible > 0 {
				prep.Dispatch.Possible--
			}
			if ev.RouteWasDirect {
				prep.ForceRelay = true
			}
			if e.nowMs < prep.RecoveryDeadlineMs && e.running {
				e.emitCommandAttempt(ctx, prep, prep.Dispatch.HasPrior())
				return
			}
			prep.Dispatch.PriorUnknown = true
			e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, world.Reason{Kind: world.ReasonRelayNoVerifiableResult}, true)
			return
		}
		if (ev.TransportCode == errCodeNetwork || ev.TransportCode == errCodeUnresolvedHost) && !priorBefore && ev.NotForwarded {
			if prep.Dispatch.Possible > 0 {
				prep.Dispatch.Possible--
			}
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeNetwork, world.Reason{Kind: world.ReasonNoNetwork}, false)
			return
		}
		prep.Dispatch.PriorUnknown = true
		e.handleRetryableTransportOutcome(ctx, prep, ev)

	case corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON:
		cat := classifyDaemonOutcome(ev, priorBefore)
		if cat == outcome.Unknown {
			prep.Dispatch.PriorUnknown = true
			e.settleCommand(ctx, prep, outcome.Unknown, ev.Result, errCodeUnknown, world.Reason{Kind: world.ReasonReplyUnverified}, false)
			return
		}
		res := ev.Result
		if (cat == outcome.Executed && res != nil && !res.ResultAvailable) || (cat == outcome.Conflict && res != nil && res.ConflictKind == "identity") {
			if e.running && e.w.LivePushConnected {
				e.startRefresh(ctx)
				if prep.SessionID != "" {
					e.dispatchLivePoll(ctx, prep.SessionID)
				}
			}
			var errReason world.Reason
			if cat == outcome.Executed {
				errReason = world.Reason{Kind: world.ReasonExecutedResultUnavailable}
			} else {
				errReason = world.Reason{Kind: world.ReasonRequestIdentityConflict}
			}
			e.settleCommand(ctx, prep, cat, res, res.GetErrorCode(), errReason, false)
			return
		}
		if res != nil && (res.ErrorCode != "" || cat != outcome.Executed) {
			errReason := world.PlainReason(res.ErrorMessage)
			if res.ErrorMessage == "" {
				errReason = world.PlainReason(res.ErrorCode)
			}
			e.settleCommand(ctx, prep, cat, res, res.ErrorCode, errReason, false)
			return
		}
		e.settleCommand(ctx, prep, outcome.Executed, res, "", world.Reason{}, false)
	}
}

func (e *Engine) handleRetryableTransportOutcome(ctx *StepContext, prep *PreparedCommand, ev *corev1.CommandOutcomeEvent) {
	if !e.running {
		e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, world.Reason{Kind: world.ReasonRelayNoVerifiableResult}, false)
		return
	}
	if ev.TransportCode == errCodeUnavailable && prep.Dispatch.Unresolved() {
		if prep.RecoveryUntilMs == 0 {
			prep.RecoveryUntilMs = e.monoMs + CommandUnavailableWindowMs
			if !ev.NotForwarded && !e.w.LivePushConnected && !e.connecting {
				e.beginConnect(ctx)
			}
		}
		if e.monoMs+CommandUnavailableRetryMs < prep.RecoveryUntilMs {
			e.retryReqByTimer[prep.RequestID] = prep.RequestID
			e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_COMMAND_RETRY, prep.RequestID, CommandUnavailableRetryMs)
			return
		}
	}
	if ev.TransportCode == errCodeUnauthenticated && prep.AttemptCount < CommandRetryMaxAttempts {
		prep.AttemptCount++
		e.retryReqByTimer[prep.RequestID] = prep.RequestID
		e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_COMMAND_RETRY, prep.RequestID, CommandUnavailableRetryMs)
		return
	}
	retryLimit := prep.DeadlineMs
	if prep.Command.GetCompleteImageUpload() != nil && prep.Dispatch.HasPrior() && prep.RecoveryDeadlineMs > retryLimit {
		retryLimit = prep.RecoveryDeadlineMs
	}
	if isTransientRetryCode(ev.TransportCode) && prep.AttemptCount < CommandRetryMaxAttempts && e.nowMs+2_000 < retryLimit {
		prep.AttemptCount++
		wait := int64(1_000) * int64(prep.AttemptCount)
		e.retryReqByTimer[prep.RequestID] = prep.RequestID
		e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_COMMAND_RETRY, prep.RequestID, wait)
		return
	}
	var errReason world.Reason
	if ev.TransportCode == errCodeUnavailable {
		errReason = world.Reason{Kind: world.ReasonReplyUnverified}
	} else {
		errReason = classifyTransportError(ev)
	}
	e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, errReason, false)
}

func (e *Engine) onCommandRetryTimer(ctx *StepContext, key string) {
	reqID := e.retryReqByTimer[key]
	delete(e.retryReqByTimer, key)
	if reqID == "" {
		reqID = key
	}
	prep := e.inFlightCmds[reqID]
	if prep == nil || !e.running {
		return
	}
	if e.nowMs >= prep.RecoveryDeadlineMs {
		e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, world.Reason{Kind: world.ReasonRelayNoVerifiableResult}, true)
		return
	}
	e.preflightAndEmit(ctx, prep, true)
}

func (e *Engine) settleCommand(
	ctx *StepContext,
	prep *PreparedCommand,
	cat outcome.Category,
	res *v1.CommandResult,
	errCode string,
	errReason world.Reason,
	expired bool,
) {
	delete(e.inFlightCmds, prep.RequestID)
	delete(e.retryReqByTimer, prep.RequestID)
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_COMMAND_RETRY, prep.RequestID)
	for aid := range prep.PendingAttempts {
		delete(e.reqIDByAttempt, aid)
		if e.relayPipe {
			e.cancelRelayForwardAttempt(ctx, aid)
		}
	}
	e.releaseOrDeferLegs(prep, "")
	clear(prep.PendingAttempts)
	clear(prep.ActiveLegs)
	prep.DualMode = false

	if _, isLive := e.livePollReqToSid[prep.RequestID]; isLive {
		e.onLivePollSettled(ctx, prep.RequestID, res)
		return
	}

	var frozen, trackerExpired bool
	if prep.SlotKey != "" {
		tr := e.attemptTrackers[prep.SlotKey]
		if tr == nil {
			tr = &AttemptTracker{Pending: prep}
			e.attemptTrackers[prep.SlotKey] = tr
		}
		if cat == outcome.Unknown {
			tr.Frozen = true
			if expired {
				tr.Expired = true
				errReason = world.Reason{Kind: world.ReasonPreviousOutcomeUnknown}
			}
			frozen = tr.Frozen
			trackerExpired = tr.Expired
		} else {
			delete(e.attemptTrackers, prep.SlotKey)
		}
	}

	var sealedResult *v1.Envelope
	if cat == outcome.Executed {
		sealedResult = prep.VerifiedSealedResult
	}
	ctx.Settled = append(ctx.Settled, SettledCommand{
		RequestID:   prep.RequestID,
		SlotKey:     prep.SlotKey,
		MachineID:   prep.MachineID,
		SessionID:   prep.SessionID,
		Category:    cat,
		Result:      res,
		ErrorCode:   errCode,
		ErrorReason: errReason,
		FromDaemon:  res != nil && cat != outcome.Unknown,
		Expired:     trackerExpired || expired,
		Frozen:      frozen,

		SealedResult: sealedResult,
	})
	ctx.Dirty.Add(true, true, true)
}

func classifyDaemonOutcome(ev *corev1.CommandOutcomeEvent, prior bool) outcome.Category {
	res := ev.Result
	if !ev.Signed || !ev.HashesMatch || !ev.AttemptBound || res == nil {
		return outcome.Unknown
	}
	if !ev.RouteWasDirect && (!ev.OuterMatchesInner || res.Outcome != ev.OuterOutcome || res.Reason != ev.OuterReason) {
		return outcome.Unknown
	}
	if res.Reason == "" ||
		(res.Outcome != "conflict" && res.ConflictKind != "") ||
		(!res.ResultAvailable && res.Kind != nil) ||
		(res.ResultAvailable && res.Outcome != "executed" && !(res.Outcome == "conflict" && res.ConflictKind == "business")) {
		return outcome.Unknown
	}
	return outcome.Classify(outcome.Evidence{
		Source:       "daemon",
		Code:         res.ErrorCode,
		Signed:       true,
		Prior:        prior,
		Declared:     outcome.Category(res.Outcome),
		Reason:       res.Reason,
		ConflictKind: res.ConflictKind,
	})
}

func isTransientRetryCode(code string) bool {
	switch code {
	case "deadline_exceeded", errCodeUnknown, "internal", "internal_error", "canceled", "aborted":
		return true
	}
	return false
}

func (e *Engine) releaseOrDeferLegs(prep *PreparedCommand, skipAttemptID string) {
	c := e.transportCoord
	if c == nil || prep == nil || len(prep.ActiveLegs) == 0 {
		return
	}
	dl := prep.RecoveryDeadlineMs
	if dl <= 0 {
		dl = prep.DeadlineMs
	}
	if dl <= 0 {
		dl = e.nowMs + 120_000
	}
	for id, l := range prep.ActiveLegs {
		if id == skipAttemptID || l == nil || l.TicketID == 0 {
			continue
		}
		if !l.Done && l.Direct && l.AttemptID != "" {
			e.lateDirectTickets[l.AttemptID] = l.TicketID
			e.lateDirectDeadline[l.AttemptID] = dl
		} else {
			c.ReleaseTicket(l.TicketID)
		}
		l.TicketID = 0
	}
}

func routeHintFor(cmd *v1.Command) corev1.CommandRouteHint {
	if cmd == nil {
		return corev1.CommandRouteHint_COMMAND_ROUTE_HINT_UNSPECIFIED
	}
	switch k := cmd.Kind.(type) {
	case *v1.Command_ListDirectories,
		*v1.Command_ListModels,
		*v1.Command_GetSettings,
		*v1.Command_GetLiveSnapshot,
		*v1.Command_GetMachineStatus,
		*v1.Command_GetUsageStats,
		*v1.Command_GetImageUploadStatus:
		return corev1.CommandRouteHint_COMMAND_ROUTE_HINT_READ
	case *v1.Command_Schedule:
		if k.Schedule != nil && (k.Schedule.Operation == "list" || k.Schedule.Operation == "get") {
			return corev1.CommandRouteHint_COMMAND_ROUTE_HINT_READ
		}
		return corev1.CommandRouteHint_COMMAND_ROUTE_HINT_NORMAL
	case *v1.Command_GetSharedFileChunk, *v1.Command_PutImageChunk:
		return corev1.CommandRouteHint_COMMAND_ROUTE_HINT_BULK
	default:
		return corev1.CommandRouteHint_COMMAND_ROUTE_HINT_NORMAL
	}
}

func isObservedAction(cmd *v1.Command) bool {
	if cmd == nil {
		return false
	}
	switch cmd.Kind.(type) {
	case *v1.Command_ResolveApproval, *v1.Command_AnswerQuestion, *v1.Command_AnswerPlan:
		return true
	}
	return false
}

func (e *Engine) checkObservedAction(prep *PreparedCommand) world.Reason {
	s := e.sessions[prep.SessionID]
	if s == nil || s.Checkpoint == nil {
		return world.Reason{Kind: world.ReasonRequestNotVerified}
	}
	actions := s.Checkpoint.Actions
	switch k := prep.Command.Kind.(type) {
	case *v1.Command_ResolveApproval:
		if k.ResolveApproval == nil {
			return world.Reason{Kind: world.ReasonRequestChanged}
		}
		for _, a := range actions {
			if a != nil && a.GetApprovalRequested() != nil && a.GetApprovalRequested().ApprovalId == k.ResolveApproval.ApprovalId {
				if k.ResolveApproval.ObservedEventSeq != 0 && a.Seq != k.ResolveApproval.ObservedEventSeq {
					return world.Reason{Kind: world.ReasonRequestChanged}
				}
				return world.Reason{}
			}
		}
		return world.Reason{Kind: world.ReasonRequestChanged}
	case *v1.Command_AnswerQuestion:
		if k.AnswerQuestion == nil {
			return world.Reason{Kind: world.ReasonRequestChanged}
		}
		for _, a := range actions {
			if a != nil && a.GetQuestionAsked() != nil && a.GetQuestionAsked().QuestionId == k.AnswerQuestion.QuestionId {
				if k.AnswerQuestion.ObservedEventSeq != 0 && a.Seq != k.AnswerQuestion.ObservedEventSeq {
					return world.Reason{Kind: world.ReasonRequestChanged}
				}
				return world.Reason{}
			}
		}
		return world.Reason{Kind: world.ReasonRequestChanged}
	case *v1.Command_AnswerPlan:
		if k.AnswerPlan == nil || s.Checkpoint.PlanEventSeq == 0 || len(s.Checkpoint.PlanScreenHash) != 32 {
			return world.Reason{Kind: world.ReasonPlanDisplayChanged}
		}
		if s.Checkpoint.Session != nil && !s.Checkpoint.Session.PlanOpen {
			return world.Reason{Kind: world.ReasonPlanDisplayChanged}
		}
		if k.AnswerPlan.ObservedEventSeq != 0 && s.Checkpoint.PlanEventSeq != k.AnswerPlan.ObservedEventSeq {
			return world.Reason{Kind: world.ReasonPlanDisplayChanged}
		}
		if len(k.AnswerPlan.ObservedScreenHash) != 0 && !bytes.Equal(s.Checkpoint.PlanScreenHash, k.AnswerPlan.ObservedScreenHash) {
			return world.Reason{Kind: world.ReasonPlanDisplayChanged}
		}
		return world.Reason{}
	}
	return world.Reason{}
}

func checkFileCapability(cmd *v1.Command, m *MachineEntry, prior bool) world.Reason {
	if cmd == nil {
		return world.Reason{}
	}
	var hasFiles bool
	switch k := cmd.Kind.(type) {
	case *v1.Command_Send:
		hasFiles = k.Send != nil && len(k.Send.FileIds) > 0
	case *v1.Command_CreateSession:
		hasFiles = k.CreateSession != nil && len(k.CreateSession.FileIds) > 0
	}
	if !hasFiles {
		return world.Reason{}
	}
	if m != nil && m.Meta != nil && m.Meta.Capabilities != nil && m.Meta.Capabilities.FileUpload {
		return world.Reason{}
	}
	if prior {
		return world.Reason{Kind: world.ReasonPreviousOutcomeUnknown}
	}
	return world.Reason{Kind: world.ReasonAttachmentUpdateRequired}
}

func checkProtocolMismatch(m *MachineEntry, prior bool) world.Reason {
	if m == nil {
		return world.Reason{}
	}
	lm := &lists.Machine{ID: m.ID, Meta: m.Meta}
	dir := lists.ProtocolMismatchDirection(lm, PeerVersion)
	if dir == 0 {
		return world.Reason{}
	}
	if prior {
		return world.Reason{Kind: world.ReasonPreviousOutcomeUnknown}
	}
	if dir < 0 {
		return world.Reason{Kind: world.ReasonDaemonOutOfDate, Param: lists.MachineName(lm, m.ID)}
	}
	return world.Reason{Kind: world.ReasonClientOutOfDate}
}

func classifyServerRefusal(ev *corev1.CommandOutcomeEvent) world.Reason {
	if ev.OuterReason == "machine_offline" {
		return world.Reason{Kind: world.ReasonComputerOffline}
	}
	if ev.TransportDetail != "" {
		return world.PlainReason(ev.TransportDetail)
	}
	if ev.TransportCode != "" {
		return world.PlainReason(ev.TransportCode)
	}
	return world.Reason{Kind: world.ReasonDisconnectedFromServer}
}

func classifyTransportError(ev *corev1.CommandOutcomeEvent) world.Reason {
	switch ev.TransportCode {
	case errCodeUnresolvedHost:
		return world.Reason{Kind: world.ReasonNoServer}
	case errCodeNetwork:
		return world.Reason{Kind: world.ReasonNoNetwork}
	case errCodeUnauthenticated, "permission_denied":
		return world.Reason{Kind: world.ReasonDeviceUnrecognized}
	case errCodeUnavailable:
		if ev.TransportDetail != "" {
			return world.Reason{Kind: world.ReasonServerReturned, Param: ev.TransportDetail}
		}
		return world.Reason{Kind: world.ReasonDisconnectedFromServer}
	}
	if ev.TransportDetail != "" {
		return world.PlainReason(ev.TransportDetail)
	}
	return world.Reason{Kind: world.ReasonRelayNoVerifiableResult}
}
