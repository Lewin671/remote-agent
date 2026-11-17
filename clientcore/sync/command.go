package sync

import (
	"bytes"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
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
	RouteHint          corev1.CommandRouteHint
	ForceRelay         bool
	Dispatch           DispatchState
	AttemptCount       int
	RecoveryUntilMs    int64
	PendingAttempts    map[string]bool
	BulkFileKey        string
	BulkBytes          int64
	BulkUpload         bool
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
	BulkFileKey string
	BulkBytes   int64
	BulkUpload  bool
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
				Expired:   true,
				Frozen:    true,
				ErrorCode: errCodeUnknown,
				ErrorText: text.Msg(text.KeyErrorsThePreviousOperationSOutcomeIsStillUnknownReload),
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
		cmd.ProtocolVersion = ProtocolVersion
		raw, err := cmd.Marshal()
		if err != nil {
			return "", false
		}
		deadline := e.nowMs + CommandTTLMs
		prep = &PreparedCommand{
			RequestID:          e.entropy.nextID(ctx),
			SlotKey:            in.SlotKey,
			MachineID:          in.MachineID,
			SessionID:          in.SessionID,
			DeadlineMs:         deadline,
			RecoveryDeadlineMs: deadline + ResultRetentionMs,
			Command:            cmd,
			CommandBytes:       raw,
			RouteHint:          routeHintFor(cmd),
			PendingAttempts:    make(map[string]bool),
			BulkFileKey:        in.BulkFileKey,
			BulkBytes:          in.BulkBytes,
			BulkUpload:         in.BulkUpload,
		}
		if tracker != nil {
			tracker.Pending = prep
		}
	}

	prep.Dispatch.PriorUnknown = prep.Dispatch.PriorUnknown || retryOfUnknown
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
			e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, text.Msg(text.KeyErrorsTheRelayDidNotProvideAVerifiableExecutionResult), true)
		} else {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeExpired, text.Msg(text.KeyErrorsConnectionTimedOut), true)
		}
		return false
	}
	if !prior && e.nowMs > 0 && prep.DeadlineMs > 0 && e.nowMs >= prep.DeadlineMs {
		e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeExpired, text.Msg(text.KeyErrorsConnectionTimedOut), false)
		return false
	}
	if e.serverAhead || e.security.ClockSkewed {
		if prior {
			e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, text.Msg(text.KeyErrorsThisDeviceSClockIsBehindTheAccountStateRemote), false)
		} else {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeFailedPrecond, text.Msg(text.KeyErrorsThisDeviceSClockIsBehindTheAccountStateRemote), false)
		}
		return false
	}
	if e.security.Loaded && (!e.security.IsMember || (e.security.LeaseEndMs > 0 && e.nowMs >= e.security.LeaseEndMs)) {
		if prior {
			e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, text.Msg(text.KeyErrorsTheComputerSReplyCouldNotBeVerifiedCommand), false)
		} else {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeFailedPrecond, text.Msg(text.KeyErrorsRemoteControlExpiredSavingChangesSessionControlAndNew), false)
		}
		return false
	}

	m := e.machines[prep.MachineID]
	if errText := checkFileCapability(prep.Command, m, prior); errText != nil {
		if prior {
			e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, errText, false)
		} else {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeFailedPrecond, errText, false)
		}
		return false
	}
	if errText := checkProtocolMismatch(m, prior); errText != nil {
		if prior {
			e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, errText, false)
		} else {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeFailedPrecond, errText, false)
		}
		return false
	}
	if !prior {
		if !e.networkAvailable {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeUnavailable, text.Msg(text.KeyConnectionNoNetwork), false)
			return false
		}
		if e.conn != lists.ConnConnected {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeUnavailable, text.Msg(text.KeyErrorsNotConnected), false)
			return false
		}
		if m == nil || !e.machineReachable(m) {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeUnavailable, text.Msg(text.KeyErrorsTheComputerIsOfflineTheCommandWasNotSent), false)
			return false
		}
	}
	if !retryOfUnknown && isObservedAction(prep.Command) {
		if errText := e.checkObservedAction(prep); errText != nil {
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeFailedPrecond, errText, false)
			return false
		}
	}

	e.emitCommandAttempt(ctx, prep, retryOfUnknown)
	return true
}

func (e *Engine) emitCommandAttempt(ctx *StepContext, prep *PreparedCommand, retryOfUnknown bool) {
	attemptID := e.entropy.nextID(ctx)
	prep.Dispatch.Active++
	prep.Dispatch.Possible++
	prep.PendingAttempts[attemptID] = true
	e.inFlightCmds[prep.RequestID] = prep
	e.reqIDByAttempt[attemptID] = prep.RequestID

	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_DispatchCommand{
			DispatchCommand: &corev1.DispatchCommandEffect{
				RequestId:      prep.RequestID,
				AttemptId:      attemptID,
				MachineId:      prep.MachineID,
				SessionId:      prep.SessionID,
				DeadlineMs:     prep.DeadlineMs,
				CommandBytes:   prep.CommandBytes,
				RouteHint:      prep.RouteHint,
				RetryOfUnknown: retryOfUnknown,
				ForceRelay:     prep.ForceRelay,
				BulkFileKey:    prep.BulkFileKey,
				BulkBytes:      prep.BulkBytes,
				BulkUpload:     prep.BulkUpload,
			},
		},
	})
}

func (e *Engine) handleCommandOutcome(ctx *StepContext, ev *corev1.CommandOutcomeEvent) {
	if ev == nil || ev.RequestId == "" {
		return
	}
	if ev.AttemptId != "" {
		delete(e.reqIDByAttempt, ev.AttemptId)
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

	priorBefore := prep.Dispatch.PriorUnknown || (prep.Dispatch.Possible > 1) || (ev.Source != corev1.OutcomeSource_OUTCOME_SOURCE_LOCAL && !ev.NotForwarded)

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
		e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, text.Msg(text.KeyErrorsTheComputerSReplyCouldNotBeVerifiedCommand), false)

	case corev1.OutcomeSource_OUTCOME_SOURCE_SERVER:
		if ev.NotForwarded && ev.AttemptBound {
			if prep.Dispatch.Possible > 0 {
				prep.Dispatch.Possible--
			}
			if ev.TransportCode == errCodeUnauthenticated && prep.AttemptCount < CommandRetryMaxAttempts && e.running {
				prep.AttemptCount++
				if !e.security.Loaded || e.security.VerifiedNetGen != e.networkGen {
					e.activeSecOpID = e.allocOpID()
					ctx.emit(&corev1.Effect{
						Effect: &corev1.Effect_SyncSecurity{
							SyncSecurity: &corev1.SyncSecurityEffect{
								OpId:              e.activeSecOpID,
								NetworkGeneration: e.networkGen,
								Touch:             true,
							},
						},
					})
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
			e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, text.Msg(text.KeyErrorsTheComputerSReplyCouldNotBeVerifiedCommand), false)
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
			e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, text.Msg(text.KeyErrorsTheRelayDidNotProvideAVerifiableExecutionResult), true)
			return
		}
		if (ev.TransportCode == errCodeNetwork || ev.TransportCode == errCodeUnresolvedHost) && !priorBefore && ev.NotForwarded {
			if prep.Dispatch.Possible > 0 {
				prep.Dispatch.Possible--
			}
			e.settleCommand(ctx, prep, outcome.NotSent, nil, errCodeNetwork, text.Msg(text.KeyConnectionNoNetwork), false)
			return
		}
		prep.Dispatch.PriorUnknown = true
		e.handleRetryableTransportOutcome(ctx, prep, ev)

	case corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON:
		cat := classifyDaemonOutcome(ev, priorBefore)
		if cat == outcome.Unknown {
			prep.Dispatch.PriorUnknown = true
			e.settleCommand(ctx, prep, outcome.Unknown, ev.Result, errCodeUnknown, text.Msg(text.KeyErrorsTheComputerSReplyCouldNotBeVerifiedCommand), false)
			return
		}
		res := ev.Result
		if (cat == outcome.Executed && res != nil && !res.ResultAvailable) || (cat == outcome.Conflict && res != nil && res.ConflictKind == "identity") {
			if e.running && e.streamOpen {
				e.startRefresh(ctx)
				if prep.SessionID != "" {
					e.dispatchLivePoll(ctx, prep.SessionID)
				}
			}
			var errText *corev1.Text
			if cat == outcome.Executed {
				errText = text.Msg(text.KeyErrorsExecutedResultUnavailable)
			} else {
				errText = text.Msg(text.KeyErrorsRequestIdentityConflict)
			}
			e.settleCommand(ctx, prep, cat, res, res.GetErrorCode(), errText, false)
			return
		}
		if res != nil && (res.ErrorCode != "" || cat != outcome.Executed) {
			errText := text.Plain(res.ErrorMessage)
			if res.ErrorMessage == "" {
				errText = text.Plain(res.ErrorCode)
			}
			e.settleCommand(ctx, prep, cat, res, res.ErrorCode, errText, false)
			return
		}
		e.settleCommand(ctx, prep, outcome.Executed, res, "", nil, false)
	}
}

func (e *Engine) handleRetryableTransportOutcome(ctx *StepContext, prep *PreparedCommand, ev *corev1.CommandOutcomeEvent) {
	if !e.running {
		e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, text.Msg(text.KeyErrorsTheRelayDidNotProvideAVerifiableExecutionResult), false)
		return
	}
	if ev.TransportCode == errCodeUnavailable && prep.Dispatch.Unresolved() {
		if prep.RecoveryUntilMs == 0 {
			prep.RecoveryUntilMs = e.monoMs + CommandUnavailableWindowMs
			if !ev.NotForwarded && !e.streamOpen && !e.connecting {
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
	if isTransientRetryCode(ev.TransportCode) && prep.AttemptCount < CommandRetryMaxAttempts && e.nowMs+2_000 < prep.DeadlineMs {
		prep.AttemptCount++
		wait := int64(1_000) * int64(prep.AttemptCount)
		e.retryReqByTimer[prep.RequestID] = prep.RequestID
		e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_COMMAND_RETRY, prep.RequestID, wait)
		return
	}
	var errText *corev1.Text
	if ev.TransportCode == errCodeUnavailable {
		errText = text.Msg(text.KeyErrorsTheComputerSReplyCouldNotBeVerifiedCommand)
	} else {
		errText = classifyTransportError(ev)
	}
	e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, errText, false)
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
		e.settleCommand(ctx, prep, outcome.Unknown, nil, errCodeUnknown, text.Msg(text.KeyErrorsTheRelayDidNotProvideAVerifiableExecutionResult), true)
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
	errText *corev1.Text,
	expired bool,
) {
	delete(e.inFlightCmds, prep.RequestID)
	delete(e.retryReqByTimer, prep.RequestID)
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_COMMAND_RETRY, prep.RequestID)
	for aid := range prep.PendingAttempts {
		delete(e.reqIDByAttempt, aid)
	}
	clear(prep.PendingAttempts)

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
				errText = text.Msg(text.KeyErrorsThePreviousOperationSOutcomeIsStillUnknownReload)
			}
			frozen = tr.Frozen
			trackerExpired = tr.Expired
		} else {
			delete(e.attemptTrackers, prep.SlotKey)
		}
	}

	ctx.Settled = append(ctx.Settled, SettledCommand{
		RequestID: prep.RequestID,
		SlotKey:   prep.SlotKey,
		MachineID: prep.MachineID,
		SessionID: prep.SessionID,
		Category:  cat,
		Result:    res,
		ErrorCode: errCode,
		ErrorText: errText,
		Expired:   trackerExpired || expired,
		Frozen:    frozen,
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

func (e *Engine) checkObservedAction(prep *PreparedCommand) *corev1.Text {
	s := e.sessions[prep.SessionID]
	if s == nil || s.Checkpoint == nil {
		return text.Msg(text.KeyErrorsThisRequestHasNotBeenVerifiedByTheComputer)
	}
	actions := s.Checkpoint.Actions
	switch k := prep.Command.Kind.(type) {
	case *v1.Command_ResolveApproval:
		if k.ResolveApproval == nil {
			return text.Msg(text.KeyErrorsTheRequestOnTheComputerChangedReviewItAgain)
		}
		for _, a := range actions {
			if a != nil && a.GetApprovalRequested() != nil && a.GetApprovalRequested().ApprovalId == k.ResolveApproval.ApprovalId {
				if k.ResolveApproval.ObservedEventSeq != 0 && a.Seq != k.ResolveApproval.ObservedEventSeq {
					return text.Msg(text.KeyErrorsTheRequestOnTheComputerChangedReviewItAgain)
				}
				return nil
			}
		}
		return text.Msg(text.KeyErrorsTheRequestOnTheComputerChangedReviewItAgain)
	case *v1.Command_AnswerQuestion:
		if k.AnswerQuestion == nil {
			return text.Msg(text.KeyErrorsTheRequestOnTheComputerChangedReviewItAgain)
		}
		for _, a := range actions {
			if a != nil && a.GetQuestionAsked() != nil && a.GetQuestionAsked().QuestionId == k.AnswerQuestion.QuestionId {
				if k.AnswerQuestion.ObservedEventSeq != 0 && a.Seq != k.AnswerQuestion.ObservedEventSeq {
					return text.Msg(text.KeyErrorsTheRequestOnTheComputerChangedReviewItAgain)
				}
				return nil
			}
		}
		return text.Msg(text.KeyErrorsTheRequestOnTheComputerChangedReviewItAgain)
	case *v1.Command_AnswerPlan:
		if k.AnswerPlan == nil || s.Checkpoint.PlanEventSeq == 0 || len(s.Checkpoint.PlanScreenHash) != 32 {
			return text.Msg(text.KeyErrorsThePlanDisplayChangedReviewItAgain)
		}
		if s.Checkpoint.Session != nil && !s.Checkpoint.Session.PlanOpen {
			return text.Msg(text.KeyErrorsThePlanDisplayChangedReviewItAgain)
		}
		if k.AnswerPlan.ObservedEventSeq != 0 && s.Checkpoint.PlanEventSeq != k.AnswerPlan.ObservedEventSeq {
			return text.Msg(text.KeyErrorsThePlanDisplayChangedReviewItAgain)
		}
		if len(k.AnswerPlan.ObservedScreenHash) != 0 && !bytes.Equal(s.Checkpoint.PlanScreenHash, k.AnswerPlan.ObservedScreenHash) {
			return text.Msg(text.KeyErrorsThePlanDisplayChangedReviewItAgain)
		}
		return nil
	}
	return nil
}

func checkFileCapability(cmd *v1.Command, m *MachineEntry, prior bool) *corev1.Text {
	if cmd == nil {
		return nil
	}
	var hasFiles bool
	switch k := cmd.Kind.(type) {
	case *v1.Command_Send:
		hasFiles = k.Send != nil && len(k.Send.FileIds) > 0
	case *v1.Command_CreateSession:
		hasFiles = k.CreateSession != nil && len(k.CreateSession.FileIds) > 0
	}
	if !hasFiles {
		return nil
	}
	if m != nil && m.Meta != nil && m.Meta.Capabilities != nil && m.Meta.Capabilities.FileUpload {
		return nil
	}
	if prior {
		return text.Msg(text.KeyErrorsThePreviousOperationSOutcomeIsStillUnknownReload)
	}
	return text.Msg(text.KeyAttachmentUpdateRequired)
}

func checkProtocolMismatch(m *MachineEntry, prior bool) *corev1.Text {
	if m == nil {
		return nil
	}
	lm := &lists.Machine{ID: m.ID, Meta: m.Meta}
	dir := lists.ProtocolMismatchDirection(lm, ProtocolVersion)
	if dir == 0 {
		return nil
	}
	var base *corev1.Text
	if dir < 0 {
		base = text.Msg(text.KeyErrorsRemoteAgentOnIsOutOfDateUpdateIt, text.Str("value", lists.MachineName(lm, m.ID)))
	} else {
		base = text.Msg(text.KeyErrorsClientOutOfDate)
	}
	if prior {
		return text.Msg(text.KeyErrorsThePreviousOperationSOutcomeIsStillUnknownReload)
	}
	return base
}

func classifyServerRefusal(ev *corev1.CommandOutcomeEvent) *corev1.Text {
	if ev.OuterReason == "machine_offline" {
		return text.Msg(text.KeyErrorsTheComputerIsOfflineTheCommandWasNotSent)
	}
	if ev.TransportDetail != "" {
		return text.Plain(ev.TransportDetail)
	}
	if ev.TransportCode != "" {
		return text.Plain(ev.TransportCode)
	}
	return text.Msg(text.KeyErrorsDisconnectedFromServer)
}

func classifyTransportError(ev *corev1.CommandOutcomeEvent) *corev1.Text {
	switch ev.TransportCode {
	case errCodeUnresolvedHost:
		return text.Msg(text.KeyConnectionNoServer)
	case errCodeNetwork:
		return text.Msg(text.KeyConnectionNoNetwork)
	case errCodeUnauthenticated, "permission_denied":
		return text.Msg(text.KeyErrorsTheServerDoesNotRecognizeThisDeviceItMay)
	case errCodeUnavailable:
		if ev.TransportDetail != "" {
			return text.Msg(text.KeyErrorsServerReturned, text.Str("value", ev.TransportDetail))
		}
		return text.Msg(text.KeyErrorsDisconnectedFromServer)
	}
	if ev.TransportDetail != "" {
		return text.Plain(ev.TransportDetail)
	}
	return text.Msg(text.KeyErrorsTheRelayDidNotProvideAVerifiableExecutionResult)
}
