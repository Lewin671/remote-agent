package screens

import (
	"strconv"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/machine"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/security"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// ReasonText translates a structured sync world.Reason into a localized corev1.Text.
func ReasonText(r world.Reason) *corev1.Text {
	switch r.Kind {
	case world.ReasonNone:
		return nil
	case world.ReasonPlain:
		if r.Param == "" {
			return nil
		}
		return text.Plain(r.Param)
	case world.ReasonNoNetwork:
		return text.Msg(text.KeyConnectionNoNetwork)
	case world.ReasonNoServer:
		return text.Msg(text.KeyConnectionNoServer)
	case world.ReasonNotConnected:
		return text.Msg(text.KeyErrorsNotConnected)
	case world.ReasonDisconnectedFromServer:
		return text.Msg(text.KeyErrorsDisconnectedFromServer)
	case world.ReasonConnectionTimedOut:
		return text.Msg(text.KeyErrorsConnectionTimedOut)
	case world.ReasonConnectionTimedOutReconnecting:
		return text.Msg(text.KeyErrorsConnectionTimedOutReconnecting)
	case world.ReasonDeviceUnrecognized:
		return text.Msg(text.KeyErrorsTheServerDoesNotRecognizeThisDeviceItMay)
	case world.ReasonServerReturned:
		return text.Msg(text.KeyErrorsServerReturned, text.Str("value", r.Param))
	case world.ReasonClockBehind:
		return text.Msg(text.KeyErrorsThisDeviceSClockIsBehindTheAccountStateRemote)
	case world.ReasonRemoteControlExpired:
		return text.Msg(text.KeyErrorsRemoteControlExpiredSavingChangesSessionControlAndNew)
	case world.ReasonReplyUnverified:
		return text.Msg(text.KeyErrorsTheComputerSReplyCouldNotBeVerifiedCommand)
	case world.ReasonRelayNoVerifiableResult:
		return text.Msg(text.KeyErrorsTheRelayDidNotProvideAVerifiableExecutionResult)
	case world.ReasonInvalidCiphertext:
		return text.Msg(text.KeyErrorsInvalidCiphertextSignatureKeyOrContentVerificationFailed)
	case world.ReasonPreviousOutcomeUnknown:
		return text.Msg(text.KeyErrorsThePreviousOperationSOutcomeIsStillUnknownReload)
	case world.ReasonComputerOffline:
		return text.Msg(text.KeyErrorsTheComputerIsOfflineTheCommandWasNotSent)
	case world.ReasonExecutedResultUnavailable:
		return text.Msg(text.KeyErrorsExecutedResultUnavailable)
	case world.ReasonRequestIdentityConflict:
		return text.Msg(text.KeyErrorsRequestIdentityConflict)
	case world.ReasonRequestNotVerified:
		return text.Msg(text.KeyErrorsThisRequestHasNotBeenVerifiedByTheComputer)
	case world.ReasonRequestChanged:
		return text.Msg(text.KeyErrorsTheRequestOnTheComputerChangedReviewItAgain)
	case world.ReasonPlanDisplayChanged:
		return text.Msg(text.KeyErrorsThePlanDisplayChangedReviewItAgain)
	case world.ReasonAttachmentUpdateRequired:
		return text.Msg(text.KeyAttachmentUpdateRequired)
	case world.ReasonDaemonOutOfDate:
		return text.Msg(text.KeyErrorsRemoteAgentOnIsOutOfDateUpdateIt, text.Str("value", r.Param))
	case world.ReasonClientOutOfDate:
		return text.Msg(text.KeyErrorsClientOutOfDate)
	case world.ReasonHistoryKeysUnavailable:
		return text.Msg(text.KeyErrorsHistoryKeysUnavailableRecordsFromBeforeRecoveryCannotBe)
	case world.ReasonHistoryLoadFailed:
		return text.Msg(text.KeyErrorsHistoryCouldNotContinueLoadingRetry)
	case world.ReasonHistoryGap:
		return text.Msg(text.KeyErrorsHistoryHasAGapRetry)
	case world.ReasonPrefsConflict:
		return text.Msg(text.KeyErrorsOtherClientsKeptChangingTheProjectListChangesCould)
	default:
		if r.Param != "" {
			return text.Plain(r.Param)
		}
		return nil
	}
}

// ConnectStepText translates a structured sync ConnectStep into a localized corev1.Text.
func ConnectStepText(step world.ConnectStep) *corev1.Text {
	switch step {
	case world.ConnectStepReadingLocalState:
		return text.Msg(text.KeyErrorsReadingLocalState)
	case world.ConnectStepSigningIn:
		return text.Msg(text.KeyErrorsSigningIn)
	case world.ConnectStepEstablishingPush:
		return text.Msg(text.KeyErrorsEstablishingPushConnection)
	default:
		return nil
	}
}

// ManagerNameText returns the display Text for the account's managing device.
func ManagerNameText(w *world.State) *corev1.Text {
	if w == nil {
		return nil
	}
	if w.World.ManagerName != "" {
		return text.Plain(w.World.ManagerName)
	}
	if w.AuthorityDeviceID != "" {
		return text.Msg(text.KeySecurityClient)
	}
	return nil
}

func syncManagerNameText(v *corev1.SyncStateView, w *world.State) *corev1.Text {
	if w != nil && w.World.ManagerName != "" {
		return text.Plain(w.World.ManagerName)
	}
	if v != nil && v.AuthorityDeviceId != "" {
		for _, d := range v.Devices {
			if d != nil && d.DeviceId == v.AuthorityDeviceId {
				return machine.ReadableClientNameText(d, v.Devices)
			}
		}
		return text.Msg(text.KeySecurityClient)
	}
	return ManagerNameText(w)
}

func buildSyncConnBanner(v *corev1.SyncStateView, w *world.State) *corev1.ConnBannerView {
	if v == nil {
		return nil
	}
	var conn lists.ConnectionKind
	if w != nil {
		conn = w.World.Connection
	} else {
		switch v.Connection {
		case corev1.ConnState_CONN_STATE_CONNECTED:
			conn = lists.ConnConnected
		case corev1.ConnState_CONN_STATE_RECONNECTING:
			conn = lists.ConnReconnecting
		case corev1.ConnState_CONN_STATE_CONNECTING:
			conn = lists.ConnConnecting
		case corev1.ConnState_CONN_STATE_OFFLINE:
			conn = lists.ConnOffline
		case corev1.ConnState_CONN_STATE_NO_NETWORK:
			conn = lists.ConnNoNetwork
		}
	}
	var kind corev1.ConnBannerKind
	switch {
	case !v.NetworkAvailable:
		kind = corev1.ConnBannerKind_CONN_BANNER_KIND_NO_NETWORK
	case v.Removed || conn == lists.ConnOffline || (v.Unlinked && !v.ReachabilityUnknown):
		kind = corev1.ConnBannerKind_CONN_BANNER_KIND_NO_SERVER
	case conn == lists.ConnConnected:
		kind = corev1.ConnBannerKind_CONN_BANNER_KIND_NONE
	default:
		kind = corev1.ConnBannerKind_CONN_BANNER_KIND_WAITING
	}
	if kind == corev1.ConnBannerKind_CONN_BANNER_KIND_NONE || v.DisplayGrace {
		return nil
	}
	kept := (v.MachinesListed && v.SessionsListed) || len(v.Sessions) > 0
	out := &corev1.ConnBannerView{
		Kind:     kind,
		Visible:  true,
		KeptData: kept,
	}
	switch kind {
	case corev1.ConnBannerKind_CONN_BANNER_KIND_NO_NETWORK:
		if kept {
			out.Text = text.Msg(text.KeyConnectionNoNetworkPrevious)
		} else {
			out.Text = text.Msg(text.KeyConnectionNoNetworkEmpty)
		}
	case corev1.ConnBannerKind_CONN_BANNER_KIND_NO_SERVER:
		out.ErrorTone = !kept
		out.ShowDetails = true
		out.Text = text.Msg(text.KeyConnectionServerRetrying)
		reason := v.ConnectionReason
		if text.IsEmpty(reason) {
			reason = text.Msg(text.KeyConnectionNoResponse)
		}
		out.DetailText = text.Msg(text.KeyConnectionServerDetails, text.ArgText("reason", reason))
		if !kept && !text.IsEmpty(v.ConnectionReason) {
			out.FailedText = text.Msg(text.KeyConnectionServerFailed, text.ArgText("reason", v.ConnectionReason))
		}
	case corev1.ConnBannerKind_CONN_BANNER_KIND_WAITING:
		if v.LiveLoaded {
			out.Text = text.Msg(text.KeyConnectionReconnecting)
		} else {
			out.Text = text.Msg(text.KeyConnectionConnecting)
		}
	}
	return out
}

func leaseRenewalAdvice(loaded, isManager bool, managerName *corev1.Text) *corev1.Text {
	if !loaded {
		return nil
	}
	if isManager {
		return text.Msg(text.KeySecurityRenewNow)
	}
	if !text.IsEmpty(managerName) {
		return text.Msg(text.KeySecurityOpenToRenew, text.ArgText("value", managerName))
	}
	return nil
}

func buildSyncLeaseRenewalAdvice(v *corev1.SyncStateView) *corev1.Text {
	if v == nil {
		return nil
	}
	return leaseRenewalAdvice(v.SecurityLoaded, v.IsManager, v.ManagerName)
}

// ApplySyncStateTexts populates all localized Text fields on SyncStateView from
// structured sync reasons so sync stays free of clientcore/text.
func ApplySyncStateTexts(v *corev1.SyncStateView, w *world.State, connStep world.ConnectStep, devicesErr world.Reason) *corev1.SyncStateView {
	if v == nil {
		return nil
	}
	if w != nil {
		v.ConnectionReason = ReasonText(w.ConnectionReason)
		v.SessionListError = ReasonText(w.SessionListError)
	}
	v.ManagerName = syncManagerNameText(v, w)
	v.ConnectionStep = ConnectStepText(connStep)
	v.DevicesErrorText = ReasonText(devicesErr)
	v.ConnBanner = buildSyncConnBanner(v, w)
	if v.AccountClockSkewed {
		v.ClockSkewBannerText = text.Msg(text.KeyErrorsThisDeviceSClockIsBehindTheAccountState)
	}
	v.LeaseRenewalAdvice = buildSyncLeaseRenewalAdvice(v)
	return v
}

// ApplySessionDataTexts populates localized Text fields on SessionDataView from
// structured sync event buffer reasons.
func ApplySessionDataTexts(v *corev1.SessionDataView, errReason, fetchErrReason world.Reason) *corev1.SessionDataView {
	if v == nil {
		return nil
	}
	v.Error = ReasonText(errReason)
	v.FetchError = ReasonText(fetchErrReason)
	return v
}

// SecurityWorkflowReasonText translates a structured security.WorkflowReason (or raw fallback)
// into a localized corev1.Text.
func SecurityWorkflowReasonText(r security.WorkflowReason, rawFallback string) *corev1.Text {
	switch r {
	case security.WorkflowReasonPagePaused:
		return text.Msg(text.KeySecurityThePageIsPausedReturnToTheForegroundTo)
	case security.WorkflowReasonFinishCurrentOpFirst:
		return text.Msg(text.KeySecurityFinishTheCurrentSecurityOperationFirst)
	case security.WorkflowReasonFinishOriginalOpFirst:
		return text.Msg(text.KeySecurityFinishTheOriginalSecurityOperationFirst)
	case security.WorkflowReasonUnfinishedOpContinueFirst:
		return text.Msg(text.KeySecurityASecurityOperationIsUnfinishedContinueItFirst)
	case security.WorkflowReasonReturnToCreationOrRecovery:
		return text.Msg(text.KeySecurityReturnToAccountCreationOrRecoveryToContinueThe)
	case security.WorkflowReasonDeviceInfoChangedTransferAborted:
		return text.Msg(text.KeySecurityDeviceInformationChangedTheOriginalTransferRequestWasNot)
	case security.WorkflowReasonUnpublishedPairingDraftExpired:
		return text.Msg(text.KeySecurityTheUnpublishedPairingDraftExpiredYouCanExplicitlyAbandon)
	case security.WorkflowReasonUnpublishedPairingDraftCanceled:
		return text.Msg(text.KeySecurityTheUnpublishedPairingDraftWasCanceledOrItsCandidate)
	case security.WorkflowReasonPairingCanceledRemoveCandidate:
		return text.Msg(text.KeySecurityPairingWasCanceledExplicitlyRemoveTheUnfinishedCandidateBefore)
	case security.WorkflowReasonPairingDeliveryExpiredRemoveCandidate:
		return text.Msg(text.KeySecurityPairingDeliveryExpiredExplicitlyRemoveTheUnfinishedCandidateBefore)
	case security.WorkflowReasonAccountPubKeyMissing:
		return text.Msg(text.KeySecurityAccountPublicKeyMissingPairAgainOrUseA)
	case security.WorkflowReasonComputerArchiveDeleted:
		return text.Msg(text.KeySecurityTheComputerSArchiveWasDeletedJoinAsA)
	case security.WorkflowReasonPairingExpiredOrCanceled:
		return text.Msg(text.KeySecurityPairingExpiredOrWasCanceled)
	case security.WorkflowReasonPairingCanceled:
		return text.Msg(text.KeySecurityPairingCanceled)
	case security.WorkflowReasonConfirmPatternsBeforeApproving:
		return text.Msg(text.KeySecurityConfirmThePatternsOnTheNewDeviceBeforeApproving)
	case security.WorkflowReasonInvalidPairingKey:
		return text.Msg(text.KeySecurityInvalidPairingKey)
	case security.WorkflowReasonConfirmRecoveryCodeSavedFirst:
		return text.Msg(text.KeySecurityConfirmRecoveryCodeSavedFirst)
	case security.WorkflowReasonRegPowInvalidOrExpired:
		return text.Msg(text.KeySecurityRegistrationProofOfWorkIsInvalidOrExpired)
	case security.WorkflowReasonRegPowExpiredRetryDraft:
		return text.Msg(text.KeySecurityRegistrationProofOfWorkExpiredRetryTheOriginalDraft)
	case security.WorkflowReasonManagerCannotBeRemovedDirectly:
		return text.Msg(text.KeySecurityTheManagingClientCannotBeRemovedDirectlyTransferManagement)
	case security.WorkflowReasonGenerateRecoveryCodeOnSuccessor:
		return text.Msg(text.KeySecurityGenerateAndSaveARecoveryCodeOnTheSuccessor)
	case security.WorkflowReasonNamesOnlyChangedOnThatDevice:
		return text.Msg(text.KeyErrorsNamesCanOnlyBeChangedOnThatDevice)
	case security.WorkflowReasonServerMustUseHTTPS:
		return text.Msg(text.KeyErrorsTheServerMustUseHttps)
	case security.WorkflowReasonInvalidRecoveryCode:
		return text.Msg(text.KeySecurityInvalidRecoveryCode)
	case security.WorkflowReasonProtocolMismatch:
		return text.Msg(text.KeySecurityThisPageAndTheServerOrPairingDeviceUse)
	case security.WorkflowReasonPairingCodeInvalidOrUsed:
		return text.Msg(text.KeySecurityThePairingCodeIsWrongOrHasAlreadyBeen)
	case security.WorkflowReasonRateLimited:
		return text.Msg(text.KeySecurityTooManyAttemptsWaitAMinuteAndRetry)
	case security.WorkflowReasonClockBehind:
		return text.Msg(text.KeySecurityThisDeviceSClockIsBehindTheManagingClient)
	case security.WorkflowReasonSecurityChainFull:
		return text.Msg(text.KeySecurityTheAccountSStateChainIsTooLongTo)
	case security.WorkflowReasonSuccessUncertain:
		return text.Msg(text.KeySecuritySuccessIsUncertainConnectionLost)
	case security.WorkflowReasonSetupTokenRequired:
		return text.Msg(text.KeySecuritySetupTokenOptionalWithOpenRegistration)
	case security.WorkflowReasonRecoveryNotReady:
		return text.Msg(text.KeySecurityRecoveryRequestPending)
	case security.WorkflowReasonRecoveryRejected:
		return text.Msg(text.KeySecurityRecoveryWasRejectedYouCanApplyAgain)
	case security.WorkflowReasonPermissionDenied:
		return text.Msg(text.KeyErrorsTheServerDoesNotRecognizeThisDeviceItMay)
	case security.WorkflowReasonNotAuthority:
		return text.Msg(text.KeyErrorsThisDeviceSSecurityStateCouldNotBeVerified)
	case security.WorkflowReasonInvalidArgument:
		return text.Msg(text.KeyErrorsInvalidCommand)
	case security.WorkflowReasonNotFound,
		security.WorkflowReasonFailedPrecondition,
		security.WorkflowReasonOperationCanceled,
		security.WorkflowReasonCommandFailed:
		return text.Msg(text.KeyErrorsCommandFailed)
	default:
		if rawFallback != "" {
			return text.Plain(rawFallback)
		}
		return nil
	}
}

// SecurityExitAdviceText translates a structured security.ExitAdviceReason into corev1.Text.
func SecurityExitAdviceText(r security.ExitAdviceReason) *corev1.Text {
	switch r {
	case security.ExitAdviceUnresolvedOp:
		return text.Msg(text.KeySecurityTheSecurityOperationSOutcomeIsUnverifiedContinueThe)
	case security.ExitAdviceOnlyManagingClient:
		return text.Msg(text.KeySecurityThisDeviceIsTheOnlyManagingClientTransferManagement)
	default:
		return text.Msg(text.KeySecurityExitingDeletesLocalKeysYouWillNeedToPair)
	}
}

func genericBodyForNotifyKind(kind v1.NotifyKind) *corev1.Text {
	switch kind {
	case v1.NotifyKind_NOTIFY_KIND_MACHINE_DISK:
		return text.Msg(text.KeyMachineStatusHomeWarning)
	case v1.NotifyKind_NOTIFY_KIND_SCHEDULED:
		return text.Msg(text.KeyNotificationsScheduledTasksHaveNewResults)
	case v1.NotifyKind_NOTIFY_KIND_TURN_ENDED:
		return text.Msg(text.KeyNotificationsAndroidAConversationTurnFinished)
	default:
		return text.Msg(text.KeyNotificationsAndroidASessionNeedsYourAttention)
	}
}

// BuildNotifyDisplay translates a structured security.NotifyOutcome into SecurityNotifyDisplay
// containing localized corev1.Text fields.
func BuildNotifyDisplay(o security.NotifyOutcome) *corev1.SecurityNotifyDisplay {
	genericBody := genericBodyForNotifyKind(o.Kind)
	if !o.Valid {
		return &corev1.SecurityNotifyDisplay{
			Valid:           false,
			Kind:            o.Kind,
			FullGenericBody: genericBody,
		}
	}

	switch o.Kind {
	case v1.NotifyKind_NOTIFY_KIND_MACHINE_DISK:
		gbStr := strconv.FormatFloat(float64(o.DiskAvailableBytes)/1e9, 'f', 1, 64)
		sizeTxt := text.Msg(text.KeyMachineStatusSizeGb, text.Str("size", gbStr))
		out := &corev1.SecurityNotifyDisplay{
			Valid:                 true,
			Kind:                  o.Kind,
			Tag:                   o.Tag,
			MachineId:             o.MachineID,
			Critical:              o.DiskCritical,
			ShowDetailedByDefault: true,
			FullGenericBody:       genericBody,
		}
		if o.DiskCritical {
			out.Title = text.Msg(text.KeyMachineStatusNotifyCritical, text.Str("computer", o.MachineName), text.ArgText("size", sizeTxt))
			out.Body = text.Msg(text.KeyMachineStatusNotifyCriticalBody)
		} else {
			out.Title = text.Msg(text.KeyMachineStatusNotifyWarning, text.Str("computer", o.MachineName), text.ArgText("size", sizeTxt))
			out.Body = text.Msg(text.KeyMachineStatusNotifyWarningBody)
		}
		return out

	case v1.NotifyKind_NOTIFY_KIND_SCHEDULED:
		return &corev1.SecurityNotifyDisplay{
			Valid:                 true,
			Kind:                  o.Kind,
			Tag:                   o.Tag,
			MachineId:             o.MachineID,
			SessionId:             o.SessionID,
			SourceEventSeq:        o.SourceEventSeq,
			ShowDetailedByDefault: true,
			Title:                 text.Plain(o.Title),
			Body:                  text.Plain(o.Text),
			FullGenericBody:       genericBody,
		}

	case v1.NotifyKind_NOTIFY_KIND_TURN_ENDED:
		out := &corev1.SecurityNotifyDisplay{
			Valid:           true,
			Kind:            o.Kind,
			Tag:             o.Tag,
			MachineId:       o.MachineID,
			SessionId:       o.SessionID,
			SourceEventSeq:  o.SourceEventSeq,
			SharedFileCount: o.SharedFileCount,
		}
		if o.Title != "" {
			out.Title = text.Plain(o.Title)
		} else {
			out.Title = text.Plain("remote-agent")
		}
		baseGeneric := genericBody
		if o.HasFallbackBody {
			baseGeneric = text.Plain(o.FallbackBody)
		}
		if o.SharedFileCount > 0 {
			suffix := text.Msg(text.KeySharedFileNotificationFiles, text.Int("count", int64(o.SharedFileCount)))
			out.FullGenericBody = text.JoinLiteral(" ", baseGeneric, suffix)
			if o.Text != "" {
				out.Body = text.JoinLiteral(" ", text.Plain(o.Text), suffix)
			} else {
				out.Body = suffix
			}
		} else {
			out.FullGenericBody = baseGeneric
			if o.Text != "" {
				out.Body = text.Plain(o.Text)
			} else {
				out.Body = genericBody
			}
		}
		return out

	case v1.NotifyKind_NOTIFY_KIND_ATTENTION:
		out := &corev1.SecurityNotifyDisplay{
			Valid:           true,
			Kind:            o.Kind,
			Tag:             o.Tag,
			MachineId:       o.MachineID,
			SessionId:       o.SessionID,
			SourceEventSeq:  o.SourceEventSeq,
			FullGenericBody: genericBody,
		}
		if o.HasApproval {
			out.ApprovalId = o.ApprovalID
			out.DeadlineMs = o.ApprovalDeadlineMs
			out.Truncated = o.ApprovalTruncated
			var bodyStr string
			if o.ApprovalCommand != "" {
				out.Title = text.Msg(text.KeyListStateRequestCommandPlain)
				bodyStr = strings.TrimSpace(o.ApprovalCommand)
			} else {
				tool := strings.TrimSpace(o.ApprovalToolName)
				if tool != "" {
					out.Title = text.JoinLiteral(" · ", text.Msg(text.KeyListStateRequestPermissionPlain), text.Plain(tool))
				} else {
					out.Title = text.Msg(text.KeyListStateRequestPermissionPlain)
				}
				switch {
				case o.ApprovalDescription != "":
					bodyStr = strings.TrimSpace(o.ApprovalDescription)
				case o.ApprovalToolInput != "":
					bodyStr = strings.TrimSpace(o.ApprovalToolInput)
				default:
					bodyStr = tool
				}
			}
			out.Body = text.Plain(bodyStr)
			if o.Title != "" {
				out.SubText = text.Plain(o.Title)
			}
			descTrim := strings.TrimSpace(o.ApprovalDescription)
			if descTrim != "" && descTrim != bodyStr && bodyStr != "" {
				out.BigText = text.Plain(descTrim + "\n" + bodyStr)
			} else if descTrim != "" {
				out.BigText = text.Plain(descTrim)
			} else if bodyStr != "" {
				out.BigText = text.Plain(bodyStr)
			}
		} else {
			if o.Title != "" {
				out.Title = text.Plain(o.Title)
			} else {
				out.Title = text.Plain("remote-agent")
			}
			if o.Text != "" {
				out.Body = text.Plain(o.Text)
			} else {
				out.Body = genericBody
			}
		}
		return out

	default:
		return &corev1.SecurityNotifyDisplay{
			Valid:           false,
			Kind:            o.Kind,
			FullGenericBody: genericBody,
		}
	}
}

// FormatCandidateLastActive formats a pairing replacement candidate's last-seen
// timestamp into a localized corev1.Text.
func FormatCandidateLastActive(lastSeenMs, nowMs int64) *corev1.Text {
	if lastSeenMs <= 0 {
		return text.Msg(text.KeySettingsNoRecord)
	}
	if nowMs <= 0 {
		return text.Time(lastSeenMs, corev1.TimestampStyle_TIMESTAMP_STYLE_DATE)
	}
	diff := nowMs - lastSeenMs
	if diff < 60_000 {
		return text.Msg(text.KeyTimeJustNow)
	}
	mins := diff / 60_000
	if mins < 60 {
		return text.Msg(text.KeyTimeMinutesAgo, text.Int("count", mins))
	}
	hours := mins / 60
	if hours < 24 {
		return text.Msg(text.KeyTimeHoursAgo, text.Int("count", hours))
	}
	if hours < 48 {
		return text.Msg(text.KeyTimeYesterday)
	}
	return text.Time(lastSeenMs, corev1.TimestampStyle_TIMESTAMP_STYLE_DATE)
}

func decorateSecurityStatus(st *corev1.SecurityWorkflowStatus, bootErrText *corev1.Text) {
	if st == nil {
		return
	}
	if bootErrText != nil {
		st.ErrorText = bootErrText
	}
	if n := len(st.PendingRemoveDeviceIds); n > 0 {
		st.PendingRemoveNotice = text.Msg(text.KeySecurityOldDevicesRemovalIncomplete, text.Int("count", int64(n)))
	}
}

// DecorateSecurityEffects fills corev1.Text fields on SecurityWorkflowResultEffect,
// SecurityStatusChangedEffect, and SecurityNotifyOpenedEffect using structured
// reasons and outcomes produced by security.
func DecorateSecurityEffects(
	effects []*corev1.Effect,
	wfMeta map[uint64]security.WorkflowMeta,
	notifyMeta map[uint64]security.NotifyOutcome,
	bootErr security.BootstrapErrorMeta,
	nowMs int64,
) {
	bootErrText := SecurityWorkflowReasonText(bootErr.Reason, bootErr.Raw)
	for _, eff := range effects {
		if eff == nil {
			continue
		}
		switch k := eff.Effect.(type) {
		case *corev1.Effect_SecurityWorkflowResult:
			res := k.SecurityWorkflowResult
			if res == nil {
				continue
			}
			meta := wfMeta[res.ActionId]
			if !res.Ok {
				if txt := SecurityWorkflowReasonText(meta.ErrReason, meta.ErrRaw); txt != nil {
					res.ErrorText = txt
				}
			}
			decorateSecurityStatus(res.Status, bootErrText)
			if res.Pairing != nil {
				if len(res.Pairing.ReplacementCandidates) > 0 {
					for _, cand := range res.Pairing.ReplacementCandidates {
						if cand != nil {
							cand.LastActiveText = FormatCandidateLastActive(cand.LastSeenMs, nowMs)
						}
					}
					res.Pairing.ReplaceOldLabel = text.Msg(text.KeySecurityAlsoRemoveOldDevices)
					res.Pairing.ReplaceOldHint = text.Msg(text.KeySecurityAlsoRemoveOldDevicesHint)
				}
				if res.Pairing.Delivered && res.Status != nil && len(res.Status.PendingRemoveDeviceIds) > 0 {
					res.Pairing.RemoveOldNotice = text.Msg(
						text.KeySecurityOldDevicesRemovalIncomplete,
						text.Int("count", int64(len(res.Status.PendingRemoveDeviceIds))),
					)
				}
			}
			if res.ExitAdvice != nil {
				exitReason := meta.ExitReason
				if exitReason == security.ExitAdviceNone {
					switch {
					case res.ExitAdvice.Unresolved:
						exitReason = security.ExitAdviceUnresolvedOp
					case res.ExitAdvice.Authority:
						exitReason = security.ExitAdviceOnlyManagingClient
					default:
						exitReason = security.ExitAdviceDefault
					}
				}
				res.ExitAdvice.MessageText = SecurityExitAdviceText(exitReason)
			}
		case *corev1.Effect_SecurityStatusChanged:
			sc := k.SecurityStatusChanged
			if sc != nil {
				decorateSecurityStatus(sc.Status, bootErrText)
			}
		case *corev1.Effect_SecurityNotifyOpened:
			no := k.SecurityNotifyOpened
			if no == nil || no.Error != "" || no.Display != nil {
				continue
			}
			if o, ok := notifyMeta[no.OpId]; ok {
				no.Display = BuildNotifyDisplay(o)
			}
		}
	}
}
