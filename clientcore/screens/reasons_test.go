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
	if ManagerNameText(w).GetKey() != text.KeySettingsUnnamed {
		t.Fatalf("unnamed authority should use KeySettingsUnnamed")
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

	// DecorateSecurityEffects fills ErrorText, ExitAdvice.MessageText, and NotifyOpened.Display.
	wfRes := &corev1.SecurityWorkflowResultEffect{
		ActionId:   10,
		Ok:         false,
		Error:      "security: invalid",
		ExitAdvice: &corev1.SecurityExitAdvice{Authority: true},
	}
	noEff := &corev1.SecurityNotifyOpenedEffect{OpId: 20}
	DecorateSecurityEffects(
		[]*corev1.Effect{
			{Effect: &corev1.Effect_SecurityWorkflowResult{SecurityWorkflowResult: wfRes}},
			{Effect: &corev1.Effect_SecurityNotifyOpened{SecurityNotifyOpened: noEff}},
		},
		map[uint64]security.WorkflowMeta{
			10: {ActionID: 10, ErrReason: security.WorkflowReasonInvalidPairingKey, ExitReason: security.ExitAdviceOnlyManagingClient},
		},
		map[uint64]security.NotifyOutcome{
			20: {OpID: 20, Valid: true, Kind: v1.NotifyKind_NOTIFY_KIND_SCHEDULED, Title: "T", Text: "B"},
		},
	)
	if wfRes.ErrorText.GetKey() != text.KeySecurityInvalidPairingKey || wfRes.Error != text.KeySecurityInvalidPairingKey {
		t.Fatalf("unexpected decorated workflow error: text=%+v err=%q", wfRes.ErrorText, wfRes.Error)
	}
	if wfRes.ExitAdvice.MessageText.GetKey() != text.KeySecurityThisBrowserIsTheOnlyManagingClientTransferManagement ||
		wfRes.ExitAdvice.Message != text.KeySecurityThisBrowserIsTheOnlyManagingClientTransferManagement {
		t.Fatalf("unexpected decorated exit advice: %+v", wfRes.ExitAdvice)
	}
	if noEff.Display == nil || !noEff.Display.Valid || noEff.Display.Title.GetLiteral() != "T" {
		t.Fatalf("unexpected decorated notify display: %+v", noEff.Display)
	}
}
