package screens

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/security"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func TestReasonTextAndConnectStepText(t *testing.T) {
	if got := ReasonText(world.Reason{}); got != nil {
		t.Fatalf("zero reason should yield nil, got %+v", got)
	}
	if got := ReasonText(world.PlainReason("custom error")); got.GetLiteral() != "custom error" {
		t.Fatalf("plain reason = %+v", got)
	}
	cases := []struct {
		r       world.Reason
		wantKey string
		wantArg string
	}{
		{world.Reason{Kind: world.ReasonNoNetwork}, text.KeyConnectionNoNetwork, ""},
		{world.Reason{Kind: world.ReasonNoServer}, text.KeyConnectionNoServer, ""},
		{world.Reason{Kind: world.ReasonNotConnected}, text.KeyErrorsNotConnected, ""},
		{world.Reason{Kind: world.ReasonDisconnectedFromServer}, text.KeyErrorsDisconnectedFromServer, ""},
		{world.Reason{Kind: world.ReasonConnectionTimedOut}, text.KeyErrorsConnectionTimedOut, ""},
		{world.Reason{Kind: world.ReasonConnectionTimedOutReconnecting}, text.KeyErrorsConnectionTimedOutReconnecting, ""},
		{world.Reason{Kind: world.ReasonDeviceUnrecognized}, text.KeyErrorsTheServerDoesNotRecognizeThisDeviceItMay, ""},
		{world.Reason{Kind: world.ReasonServerReturned, Param: "503"}, text.KeyErrorsServerReturned, "503"},
		{world.Reason{Kind: world.ReasonClockBehind}, text.KeyErrorsThisDeviceSClockIsBehindTheAccountStateRemote, ""},
		{world.Reason{Kind: world.ReasonRemoteControlExpired}, text.KeyErrorsRemoteControlExpiredSavingChangesSessionControlAndNew, ""},
		{world.Reason{Kind: world.ReasonReplyUnverified}, text.KeyErrorsTheComputerSReplyCouldNotBeVerifiedCommand, ""},
		{world.Reason{Kind: world.ReasonRelayNoVerifiableResult}, text.KeyErrorsTheRelayDidNotProvideAVerifiableExecutionResult, ""},
		{world.Reason{Kind: world.ReasonInvalidCiphertext}, text.KeyErrorsInvalidCiphertextSignatureKeyOrContentVerificationFailed, ""},
		{world.Reason{Kind: world.ReasonPreviousOutcomeUnknown}, text.KeyErrorsThePreviousOperationSOutcomeIsStillUnknownReload, ""},
		{world.Reason{Kind: world.ReasonComputerOffline}, text.KeyErrorsTheComputerIsOfflineTheCommandWasNotSent, ""},
		{world.Reason{Kind: world.ReasonExecutedResultUnavailable}, text.KeyErrorsExecutedResultUnavailable, ""},
		{world.Reason{Kind: world.ReasonRequestIdentityConflict}, text.KeyErrorsRequestIdentityConflict, ""},
		{world.Reason{Kind: world.ReasonRequestNotVerified}, text.KeyErrorsThisRequestHasNotBeenVerifiedByTheComputer, ""},
		{world.Reason{Kind: world.ReasonRequestChanged}, text.KeyErrorsTheRequestOnTheComputerChangedReviewItAgain, ""},
		{world.Reason{Kind: world.ReasonPlanDisplayChanged}, text.KeyErrorsThePlanDisplayChangedReviewItAgain, ""},
		{world.Reason{Kind: world.ReasonAttachmentUpdateRequired}, text.KeyAttachmentUpdateRequired, ""},
		{world.Reason{Kind: world.ReasonDaemonOutOfDate, Param: "MacBook"}, text.KeyErrorsRemoteAgentOnIsOutOfDateUpdateIt, "MacBook"},
		{world.Reason{Kind: world.ReasonClientOutOfDate}, text.KeyErrorsClientOutOfDate, ""},
		{world.Reason{Kind: world.ReasonHistoryKeysUnavailable}, text.KeyErrorsHistoryKeysUnavailableRecordsFromBeforeRecoveryCannotBe, ""},
		{world.Reason{Kind: world.ReasonHistoryLoadFailed}, text.KeyErrorsHistoryCouldNotContinueLoadingRetry, ""},
		{world.Reason{Kind: world.ReasonHistoryGap}, text.KeyErrorsHistoryHasAGapRetry, ""},
		{world.Reason{Kind: world.ReasonPrefsConflict}, text.KeyErrorsOtherClientsKeptChangingTheProjectListChangesCould, ""},
	}
	for _, tc := range cases {
		got := ReasonText(tc.r)
		if got == nil || got.GetKey() != tc.wantKey {
			t.Fatalf("ReasonText(%+v) = %+v, want key %q", tc.r, got, tc.wantKey)
		}
		if tc.wantArg != "" {
			if len(got.Args) != 1 || got.Args[0].GetStrValue() != tc.wantArg {
				t.Fatalf("ReasonText(%+v) args = %+v, want %q", tc.r, got.Args, tc.wantArg)
			}
		}
	}

	if ConnectStepText(world.ConnectStepNone) != nil {
		t.Fatalf("ConnectStepNone should be nil")
	}
	if ConnectStepText(world.ConnectStepReadingLocalState).GetKey() != text.KeyErrorsReadingLocalState {
		t.Fatalf("unexpected reading local state key")
	}
	if ConnectStepText(world.ConnectStepSigningIn).GetKey() != text.KeyErrorsSigningIn {
		t.Fatalf("unexpected signing in key")
	}
	if ConnectStepText(world.ConnectStepEstablishingPush).GetKey() != text.KeyErrorsEstablishingPushConnection {
		t.Fatalf("unexpected establishing push key")
	}

	w := world.New()
	if ManagerNameText(w) != nil {
		t.Fatalf("no authority should yield nil manager name")
	}
	w.AuthorityDeviceID = "dev-1"
	if ManagerNameText(w).GetKey() != text.KeySecurityClient {
		t.Fatalf("unnamed authority should use KeySecurityClient")
	}
	w.World.ManagerName = "Work Laptop"
	if ManagerNameText(w).GetLiteral() != "Work Laptop" {
		t.Fatalf("named authority should use literal name")
	}
}

func TestSecurityReasonTextAndBuildNotifyDisplay(t *testing.T) {
	secCases := []struct {
		r       security.WorkflowReason
		wantKey string
	}{
		{security.WorkflowReasonPagePaused, text.KeySecurityThePageIsPausedReturnToTheForegroundTo},
		{security.WorkflowReasonFinishCurrentOpFirst, text.KeySecurityFinishTheCurrentSecurityOperationFirst},
		{security.WorkflowReasonFinishOriginalOpFirst, text.KeySecurityFinishTheOriginalSecurityOperationFirst},
		{security.WorkflowReasonUnfinishedOpContinueFirst, text.KeySecurityASecurityOperationIsUnfinishedContinueItFirst},
		{security.WorkflowReasonReturnToCreationOrRecovery, text.KeySecurityReturnToAccountCreationOrRecoveryToContinueThe},
		{security.WorkflowReasonDeviceInfoChangedTransferAborted, text.KeySecurityDeviceInformationChangedTheOriginalTransferRequestWasNot},
		{security.WorkflowReasonUnpublishedPairingDraftExpired, text.KeySecurityTheUnpublishedPairingDraftExpiredYouCanExplicitlyAbandon},
		{security.WorkflowReasonUnpublishedPairingDraftCanceled, text.KeySecurityTheUnpublishedPairingDraftWasCanceledOrItsCandidate},
		{security.WorkflowReasonPairingCanceledRemoveCandidate, text.KeySecurityPairingWasCanceledExplicitlyRemoveTheUnfinishedCandidateBefore},
		{security.WorkflowReasonPairingDeliveryExpiredRemoveCandidate, text.KeySecurityPairingDeliveryExpiredExplicitlyRemoveTheUnfinishedCandidateBefore},
		{security.WorkflowReasonAccountPubKeyMissing, text.KeySecurityAccountPublicKeyMissingPairAgainOrUseA},
		{security.WorkflowReasonComputerArchiveDeleted, text.KeySecurityTheComputerSArchiveWasDeletedJoinAsA},
		{security.WorkflowReasonPairingExpiredOrCanceled, text.KeySecurityPairingExpiredOrWasCanceled},
		{security.WorkflowReasonPairingCanceled, text.KeySecurityPairingCanceled},
		{security.WorkflowReasonConfirmPatternsBeforeApproving, text.KeySecurityConfirmThePatternsOnTheNewDeviceBeforeApproving},
		{security.WorkflowReasonInvalidPairingKey, text.KeySecurityInvalidPairingKey},
		{security.WorkflowReasonConfirmRecoveryCodeSavedFirst, text.KeySecurityConfirmRecoveryCodeSavedFirst},
		{security.WorkflowReasonRegPowInvalidOrExpired, text.KeySecurityRegistrationProofOfWorkIsInvalidOrExpired},
		{security.WorkflowReasonRegPowExpiredRetryDraft, text.KeySecurityRegistrationProofOfWorkExpiredRetryTheOriginalDraft},
		{security.WorkflowReasonManagerCannotBeRemovedDirectly, text.KeySecurityTheManagingClientCannotBeRemovedDirectlyTransferManagement},
		{security.WorkflowReasonGenerateRecoveryCodeOnSuccessor, text.KeySecurityGenerateAndSaveARecoveryCodeOnTheSuccessor},
		{security.WorkflowReasonNamesOnlyChangedOnThatDevice, text.KeyErrorsNamesCanOnlyBeChangedOnThatDevice},
		{security.WorkflowReasonServerMustUseHTTPS, text.KeyErrorsTheServerMustUseHttps},
		{security.WorkflowReasonInvalidRecoveryCode, text.KeySecurityInvalidRecoveryCode},
	}
	for _, tc := range secCases {
		got := SecurityWorkflowReasonText(tc.r, "")
		if got == nil || got.GetKey() != tc.wantKey {
			t.Fatalf("SecurityWorkflowReasonText(%v) = %+v, want %q", tc.r, got, tc.wantKey)
		}
	}
	if got := SecurityWorkflowReasonText(security.WorkflowReasonNone, "rpc error"); got.GetLiteral() != "rpc error" {
		t.Fatalf("raw fallback = %+v", got)
	}

	// BuildNotifyDisplay: critical disk, scheduled, turn ended (with fallback & files matrix), approval, invalid fail-closed.
	disk := BuildNotifyDisplay(security.NotifyOutcome{
		Valid: true, Kind: v1.NotifyKind_NOTIFY_KIND_MACHINE_DISK, Tag: "m1/NOTIFY_KIND_MACHINE_DISK",
		MachineID: "m1", MachineName: "MacBook", DiskAvailableBytes: 1_500_000_000, DiskCritical: true,
	})
	if !disk.Valid || !disk.Critical || !disk.ShowDetailedByDefault || disk.Title.GetKey() != text.KeyMachineStatusNotifyCritical {
		t.Fatalf("unexpected disk display: %+v", disk)
	}

	sched := BuildNotifyDisplay(security.NotifyOutcome{
		Valid: true, Kind: v1.NotifyKind_NOTIFY_KIND_SCHEDULED, Tag: "s1/scheduled:7/NOTIFY_KIND_SCHEDULED",
		MachineID: "m1", SessionID: "s1", SourceEventSeq: 7, Title: "Daily check", Text: "All green",
	})
	if !sched.Valid || !sched.ShowDetailedByDefault || sched.Title.GetLiteral() != "Daily check" || sched.Body.GetLiteral() != "All green" {
		t.Fatalf("unexpected scheduled display: %+v", sched)
	}

	turnCases := []struct {
		name      string
		hasFB     bool
		fb        string
		files     int32
		wantNil   bool
		wantLit   string
		wantKey   string
		wantJoin0 string
		wantJoin1 string
	}{
		{"web fallback with files", true, "A turn ended", 2, false, "", "", "lit:A turn ended", "key:" + text.KeySharedFileNotificationFiles},
		{"empty fallback with files", true, "", 3, false, "", text.KeySharedFileNotificationFiles, "", ""},
		{"web fallback without files", true, "A turn ended", 0, false, "A turn ended", "", "", ""},
		{"empty fallback without files", true, "", 0, true, "", "", "", ""},
		{"default generic without files", false, "", 0, false, "", text.KeyNotificationsAndroidAConversationTurnFinished, "", ""},
		{"default generic with files", false, "", 2, false, "", "", "key:" + text.KeyNotificationsAndroidAConversationTurnFinished, "key:" + text.KeySharedFileNotificationFiles},
	}
	for _, tc := range turnCases {
		t.Run(tc.name, func(t *testing.T) {
			d := BuildNotifyDisplay(security.NotifyOutcome{
				Valid: true, Kind: v1.NotifyKind_NOTIFY_KIND_TURN_ENDED, Tag: "s1/NOTIFY_KIND_TURN_ENDED",
				MachineID: "m1", SessionID: "s1", SourceEventSeq: 42, Text: "Done",
				HasFallbackBody: tc.hasFB, FallbackBody: tc.fb, SharedFileCount: tc.files,
			})
			got := d.FullGenericBody
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil FullGenericBody, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected non-nil FullGenericBody")
			}
			if tc.wantLit != "" && got.GetLiteral() != tc.wantLit {
				t.Fatalf("FullGenericBody literal = %q, want %q", got.GetLiteral(), tc.wantLit)
			}
			if tc.wantKey != "" && got.GetKey() != tc.wantKey {
				t.Fatalf("FullGenericBody key = %q, want %q", got.GetKey(), tc.wantKey)
			}
			if tc.wantJoin0 != "" {
				if len(got.Join) != 2 || got.SeparatorLiteral != " " {
					t.Fatalf("expected 2-part join, got %+v", got)
				}
				part := func(p *corev1.Text) string {
					if p.GetKey() != "" {
						return "key:" + p.GetKey()
					}
					return "lit:" + p.GetLiteral()
				}
				if part(got.Join[0]) != tc.wantJoin0 || part(got.Join[1]) != tc.wantJoin1 {
					t.Fatalf("join = [%s, %s], want [%s, %s]", part(got.Join[0]), part(got.Join[1]), tc.wantJoin0, tc.wantJoin1)
				}
			}
		})
	}

	app := BuildNotifyDisplay(security.NotifyOutcome{
		Valid: true, Kind: v1.NotifyKind_NOTIFY_KIND_ATTENTION, Tag: "s1/NOTIFY_KIND_ATTENTION",
		MachineID: "m1", SessionID: "s1", SourceEventSeq: 9, Title: "Fix build",
		HasApproval: true, ApprovalID: "ap-1", ApprovalToolName: "Bash", ApprovalCommand: "go test ./...",
		ApprovalDescription: "Run Go tests", ApprovalDeadlineMs: 1_800_000_000_000, ApprovalTruncated: true,
	})
	if !app.Valid || app.Title.GetKey() != text.KeyListStateRequestCommandPlain || app.BigText.GetLiteral() != "Run Go tests\ngo test ./..." {
		t.Fatalf("unexpected approval display: %+v", app)
	}

	inv := BuildNotifyDisplay(security.NotifyOutcome{Valid: false, Kind: v1.NotifyKind_NOTIFY_KIND_SCHEDULED})
	if inv.Valid || inv.FullGenericBody.GetKey() != text.KeyNotificationsScheduledTasksHaveNewResults || inv.Title != nil || inv.Body != nil {
		t.Fatalf("unexpected invalid display: %+v", inv)
	}

	// DecorateSecurityEffects fills ErrorText, ExitAdvice.MessageText, Status.ErrorText, and NotifyOpened.Display.
	nowMs := int64(1_700_000_000_000)
	wfRes := &corev1.SecurityWorkflowResultEffect{
		ActionId: 10,
		Ok:       false,
		Status: &corev1.SecurityWorkflowStatus{
			Bootstrap:              corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_INVALID,
			PendingRemoveDeviceIds: []string{"old-1"},
		},
		Pairing: &corev1.SecurityPairingInfo{
			Delivered: true,
			ReplacementCandidates: []*corev1.SecurityPairingReplacementCandidate{
				{DeviceId: "old-1", LastSeenMs: nowMs - 5*60_000},
			},
		},
		ExitAdvice: &corev1.SecurityExitAdvice{Authority: true},
	}
	scEff := &corev1.SecurityStatusChangedEffect{
		Status: &corev1.SecurityWorkflowStatus{
			Bootstrap:              corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_INVALID,
			PendingRemoveDeviceIds: []string{"old-1"},
		},
	}
	noEff := &corev1.SecurityNotifyOpenedEffect{OpId: 20}
	DecorateSecurityEffects(
		[]*corev1.Effect{
			{Effect: &corev1.Effect_SecurityWorkflowResult{SecurityWorkflowResult: wfRes}},
			{Effect: &corev1.Effect_SecurityStatusChanged{SecurityStatusChanged: scEff}},
			{Effect: &corev1.Effect_SecurityNotifyOpened{SecurityNotifyOpened: noEff}},
		},
		map[uint64]security.WorkflowMeta{
			10: {ActionID: 10, ErrReason: security.WorkflowReasonInvalidPairingKey, ErrRaw: "security: invalid", ExitReason: security.ExitAdviceOnlyManagingClient},
		},
		map[uint64]security.NotifyOutcome{
			20: {OpID: 20, Valid: true, Kind: v1.NotifyKind_NOTIFY_KIND_SCHEDULED, Title: "T", Text: "B"},
		},
		security.BootstrapErrorMeta{Reason: security.WorkflowReasonAccountPubKeyMissing},
		nowMs,
	)
	if wfRes.ErrorText.GetKey() != text.KeySecurityInvalidPairingKey {
		t.Fatalf("unexpected decorated workflow error: text=%+v", wfRes.ErrorText)
	}
	if wfRes.ExitAdvice.MessageText.GetKey() != text.KeySecurityThisDeviceIsTheOnlyManagingClientTransferManagement {
		t.Fatalf("unexpected decorated exit advice: %+v", wfRes.ExitAdvice)
	}
	if wfRes.Status.ErrorText.GetKey() != text.KeySecurityAccountPublicKeyMissingPairAgainOrUseA ||
		scEff.Status.ErrorText.GetKey() != text.KeySecurityAccountPublicKeyMissingPairAgainOrUseA {
		t.Fatalf("unexpected decorated status error text: wf=%+v sc=%+v", wfRes.Status.ErrorText, scEff.Status.ErrorText)
	}
	if wfRes.Status.GetPendingRemoveNotice().GetKey() != text.KeySecurityOldDevicesRemovalIncomplete ||
		scEff.Status.GetPendingRemoveNotice().GetKey() != text.KeySecurityOldDevicesRemovalIncomplete ||
		wfRes.Pairing.GetRemoveOldNotice().GetKey() != text.KeySecurityOldDevicesRemovalIncomplete {
		t.Fatalf("unexpected pending/remove old notice: st=%+v sc=%+v pair=%+v", wfRes.Status.PendingRemoveNotice, scEff.Status.PendingRemoveNotice, wfRes.Pairing.RemoveOldNotice)
	}
	if wfRes.Pairing.GetReplaceOldLabel().GetKey() != text.KeySecurityAlsoRemoveOldDevices ||
		wfRes.Pairing.GetReplaceOldHint().GetKey() != text.KeySecurityAlsoRemoveOldDevicesHint ||
		wfRes.Pairing.ReplacementCandidates[0].GetLastActiveText().GetKey() != text.KeyTimeMinutesAgo {
		t.Fatalf("unexpected pairing replacement texts: %+v", wfRes.Pairing)
	}
	if noEff.Display == nil || !noEff.Display.Valid || noEff.Display.Title.GetLiteral() != "T" {
		t.Fatalf("unexpected decorated notify display: %+v", noEff.Display)
	}

	// Non-wfError failures (RPC, devicesec, plain error) carry ErrRaw with WorkflowReasonNone
	// and must produce Plain(raw) ErrorText so shells show the message instead of enum names.
	rawRes := &corev1.SecurityWorkflowResultEffect{
		ActionId:            11,
		Ok:                  false,
		StructuredErrorCode: corev1.SecurityErrorCode_SECURITY_ERROR_CODE_STALE,
	}
	DecorateSecurityEffects(
		[]*corev1.Effect{
			{Effect: &corev1.Effect_SecurityWorkflowResult{SecurityWorkflowResult: rawRes}},
		},
		map[uint64]security.WorkflowMeta{
			11: {ActionID: 11, ErrReason: security.WorkflowReasonNone, ErrRaw: "boom raw rpc message"},
		},
		nil,
		security.BootstrapErrorMeta{},
		nowMs,
	)
	if rawRes.ErrorText.GetLiteral() != "boom raw rpc message" || rawRes.ErrorText.GetKey() != "" {
		t.Fatalf("expected Plain raw fallback ErrorText, got %+v", rawRes.ErrorText)
	}

	const dayMs = int64(24 * 3600_000)
	timeCases := []struct {
		name       string
		lastSeenMs int64
		wantKey    string
		wantStyle  corev1.TimestampStyle
	}{
		{"no_record", 0, text.KeySettingsNoRecord, corev1.TimestampStyle_TIMESTAMP_STYLE_UNSPECIFIED},
		{"just_now", nowMs - 30_000, text.KeyTimeJustNow, corev1.TimestampStyle_TIMESTAMP_STYLE_UNSPECIFIED},
		{"minutes_ago", nowMs - 5*60_000, text.KeyTimeMinutesAgo, corev1.TimestampStyle_TIMESTAMP_STYLE_UNSPECIFIED},
		{"hours_ago", nowMs - 3*3600_000, text.KeyTimeHoursAgo, corev1.TimestampStyle_TIMESTAMP_STYLE_UNSPECIFIED},
		{"yesterday", nowMs - 30*3600_000, text.KeyTimeYesterday, corev1.TimestampStyle_TIMESTAMP_STYLE_UNSPECIFIED},
		{"older_date", nowMs - 5*dayMs, "", corev1.TimestampStyle_TIMESTAMP_STYLE_DATE},
	}
	for _, tc := range timeCases {
		t.Run("last_active/"+tc.name, func(t *testing.T) {
			got := FormatCandidateLastActive(tc.lastSeenMs, nowMs)
			if tc.wantKey != "" && got.GetKey() != tc.wantKey {
				t.Fatalf("got key %q, want %q", got.GetKey(), tc.wantKey)
			}
			if tc.wantStyle != corev1.TimestampStyle_TIMESTAMP_STYLE_UNSPECIFIED && got.GetTimestamp().GetStyle() != tc.wantStyle {
				t.Fatalf("got timestamp style %v, want %v", got.GetTimestamp().GetStyle(), tc.wantStyle)
			}
		})
	}
}

func TestApplySyncStateTextsManagerBannerClockSkewAndRenewalAdvice(t *testing.T) {
	// 1. Manager name fallbacks.
	v := &corev1.SyncStateView{AuthorityDeviceId: "auth-1"}
	w := world.New()
	ApplySyncStateTexts(v, w, world.ConnectStepNone, world.Reason{})
	if v.GetManagerName().GetKey() != text.KeySecurityClient {
		t.Fatalf("expected KeySecurityClient when authority device absent, got %+v", v.ManagerName)
	}

	w.World.ManagerName = "Android 16 · Pixel 9"
	ApplySyncStateTexts(v, w, world.ConnectStepNone, world.Reason{})
	if v.GetManagerName().GetLiteral() != "Android 16 · Pixel 9" {
		t.Fatalf("expected description literal, got %+v", v.ManagerName)
	}

	w.World.ManagerName = ""
	v.Devices = []*corev1.SyncDeviceView{{
		DeviceId: "auth-1",
		Kind:     v1.DeviceKind_DEVICE_KIND_CLIENT,
		Meta:     &v1.DeviceMeta{Platform: "web"},
	}}
	ApplySyncStateTexts(v, w, world.ConnectStepNone, world.Reason{})
	if v.GetManagerName().GetKey() != text.KeySecurityBrowser {
		t.Fatalf("expected KeySecurityBrowser for bare web device, got %+v", v.ManagerName)
	}

	// 2. Clock skew banner and lease renewal advice (non-manager vs manager).
	v.SecurityLoaded = true
	v.AccountClockSkewed = true
	v.IsManager = false
	w.World.ManagerName = "My Phone"
	ApplySyncStateTexts(v, w, world.ConnectStepNone, world.Reason{})
	if v.GetClockSkewBannerText().GetKey() != text.KeyErrorsThisDeviceSClockIsBehindTheAccountState {
		t.Fatalf("expected clock skew banner key, got %+v", v.ClockSkewBannerText)
	}
	if v.GetLeaseRenewalAdvice().GetKey() != text.KeySecurityOpenToRenew {
		t.Fatalf("expected non-manager leaseRenewalAdvice=security.open_to_renew, got %+v", v.LeaseRenewalAdvice)
	}
	v.IsManager = true
	ApplySyncStateTexts(v, w, world.ConnectStepNone, world.Reason{})
	if v.GetLeaseRenewalAdvice().GetKey() != text.KeySecurityRenewNow {
		t.Fatalf("expected manager leaseRenewalAdvice=security.renew_now, got %+v", v.LeaseRenewalAdvice)
	}

	// 3. ConnBanner: hidden when connected, NO_NETWORK when offline and not in display grace.
	vConn := &corev1.SyncStateView{
		Connection:       corev1.ConnState_CONN_STATE_CONNECTED,
		NetworkAvailable: true,
	}
	ApplySyncStateTexts(vConn, nil, world.ConnectStepNone, world.Reason{})
	if vConn.ConnBanner != nil {
		t.Fatalf("expected nil ConnBanner when connected, got %+v", vConn.ConnBanner)
	}
	vNoNet := &corev1.SyncStateView{
		Connection:       corev1.ConnState_CONN_STATE_NO_NETWORK,
		NetworkAvailable: false,
		DisplayGrace:     false,
	}
	ApplySyncStateTexts(vNoNet, nil, world.ConnectStepNone, world.Reason{})
	if vNoNet.ConnBanner == nil || !vNoNet.ConnBanner.Visible || vNoNet.ConnBanner.Kind != corev1.ConnBannerKind_CONN_BANNER_KIND_NO_NETWORK {
		t.Fatalf("expected NO_NETWORK visible ConnBanner, got %+v", vNoNet.ConnBanner)
	}
}
