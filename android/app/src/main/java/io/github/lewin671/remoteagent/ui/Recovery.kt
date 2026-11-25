package io.github.lewin671.remoteagent.ui

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.CoreHost
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.AccountSecurityActionKind
import io.github.lewin671.remoteagent.core.proto.AccountSecuritySubpage
import io.github.lewin671.remoteagent.core.proto.AccountSecurityViewModel
import io.github.lewin671.remoteagent.core.proto.CreateAccountStep
import io.github.lewin671.remoteagent.core.proto.EmergencyRecoveryView
import io.github.lewin671.remoteagent.core.proto.HandoffActionKind
import io.github.lewin671.remoteagent.core.proto.OnboardingRecoveryActionKind
import io.github.lewin671.remoteagent.core.proto.PendingSecurityOpKind
import io.github.lewin671.remoteagent.core.proto.SecurityRecoveryPhase

@Composable
internal fun PendingSecurityOperationPage(
    vm: AccountSecurityViewModel,
    onBack: () -> Unit = {},
    onContinue: () -> Unit = {},
    onDiscard: (abandonHandoff: Boolean) -> Unit = {},
) {
    val copy = uiText()
    var confirmAbandonOffer by remember { mutableStateOf(false) }
    val err = if (vm.errorBanner.visible) uiText(vm.errorBanner.text) else ""
    PageScaffold(copy(UiMessages.securityUnfinishedSecurityOperation()), onBack = onBack) {
        FormColumn(Modifier.fillMaxWidth().padding(vertical = 16.dp)) {
            Hint(copy(UiMessages.securityTheSecurityOperationSOutcomeIsUnverifiedContinueThe()))
            PillButton(
                copy(UiMessages.securityAndroidContinueOriginalOperation()),
                onContinue,
                Modifier.fillMaxWidth(),
                ButtonKind.INK,
                enabled = vm.canContinuePending && !vm.busy,
            )
            if (vm.canDiscardPending) {
                val label = when (vm.pendingKind) {
                    PendingSecurityOpKind.PENDING_SECURITY_OP_KIND_HANDOFF_OFFER ->
                        copy(UiMessages.securityAbandonThisSuccessionPreparationSecurity())
                    PendingSecurityOpKind.PENDING_SECURITY_OP_KIND_RECOVERY_ROTATION ->
                        copy(UiMessages.securityAbandonUnpublishedRecoveryCode())
                    else ->
                        copy(UiMessages.securityAbandonUnsignedOperation())
                }
                PillButton(
                    label,
                    {
                        if (vm.discardRequiresConfirm) confirmAbandonOffer = true
                        else onDiscard(false)
                    },
                    Modifier.fillMaxWidth().padding(top = 10.dp),
                    ButtonKind.SECONDARY,
                    enabled = !vm.busy,
                )
            }
            if (err.isNotEmpty()) Notice(err, Modifier.padding(top = 12.dp), tone = bannerTone(vm.errorBanner.tone))
        }
    }
    if (confirmAbandonOffer) {
        Confirm(
            copy(UiMessages.securityAbandonThisSuccessionPreparation()),
            copy(UiMessages.securityThePublicKeyMayAlreadyBePublishedAbandoningOnly()),
            copy(UiMessages.newSessionAndroidAbandon()),
            danger = true,
            onDismiss = { confirmAbandonOffer = false },
        ) {
            confirmAbandonOffer = false
            onDiscard(true)
        }
    }
}

/**
 * 管理端 · 恢复: Core drives AccountSecurityViewModel (overview, pending op, recovery rotation)
 * and HandoffViewModel (transfer / takeover).
 */
@Composable
fun RecoveryScreen(onBack: () -> Unit = {}) {
    val copy = uiText()
    val remote = LocalRemote.current
    val host = remote.coreHost
    val vm by host.accountSecurityView.collectAsState()
    var showHandoff by remember { mutableStateOf(false) }

    LaunchedEffect(host) {
        host.accountSecurityOpen(AccountSecuritySubpage.ACCOUNT_SECURITY_SUBPAGE_OVERVIEW)
    }
    DisposableEffect(host) {
        onDispose { host.accountSecurityClose() }
    }

    val onSubpageBack: () -> Unit = {
        if (showHandoff) {
            showHandoff = false
        } else {
            host.accountSecurityAction(
                AccountSecurityActionKind.ACCOUNT_SECURITY_ACTION_KIND_SET_SUBPAGE,
                subpage = AccountSecuritySubpage.ACCOUNT_SECURITY_SUBPAGE_OVERVIEW,
            )
        }
    }

    val inSubpage = showHandoff || vm.subpage != AccountSecuritySubpage.ACCOUNT_SECURITY_SUBPAGE_OVERVIEW
    if (inSubpage) BackHandler { onSubpageBack() }

    when {
        showHandoff -> {
            if (!vm.isManager) {
                HandoffPreparation { showHandoff = false }
            } else {
                PageScaffold(copy(UiMessages.securityTransferManagement()), onBack = { showHandoff = false }) {
                    FormColumn(Modifier.fillMaxWidth().verticalScrollFaded().padding(vertical = 16.dp)) {
                        HandoffControls()
                    }
                }
            }
        }
        vm.subpage == AccountSecuritySubpage.ACCOUNT_SECURITY_SUBPAGE_ROTATE_RECOVERY -> {
            RotateRecoveryScreen { onSubpageBack() }
        }
        vm.subpage == AccountSecuritySubpage.ACCOUNT_SECURITY_SUBPAGE_PENDING_OP -> {
            PendingSecurityOperationPage(
                vm = vm,
                onBack = onSubpageBack,
                onContinue = {
                    host.accountSecurityAction(AccountSecurityActionKind.ACCOUNT_SECURITY_ACTION_KIND_CONTINUE_PENDING)
                },
                onDiscard = { abandonHandoff ->
                    host.accountSecurityAction(
                        AccountSecurityActionKind.ACCOUNT_SECURITY_ACTION_KIND_DISCARD_PENDING,
                        flag = abandonHandoff,
                    )
                },
            )
        }
        else -> {
            AccountSecurityContent(
                vm = vm,
                onRetryPendingRemove = {
                    host.accountSecurityAction(AccountSecurityActionKind.ACCOUNT_SECURITY_ACTION_KIND_RETRY_PENDING_REMOVE)
                },
                onPending = {
                    host.accountSecurityAction(
                        AccountSecurityActionKind.ACCOUNT_SECURITY_ACTION_KIND_SET_SUBPAGE,
                        subpage = AccountSecuritySubpage.ACCOUNT_SECURITY_SUBPAGE_PENDING_OP,
                    )
                },
                onRecovery = {
                    host.accountSecurityAction(
                        AccountSecurityActionKind.ACCOUNT_SECURITY_ACTION_KIND_SET_SUBPAGE,
                        subpage = AccountSecuritySubpage.ACCOUNT_SECURITY_SUBPAGE_ROTATE_RECOVERY,
                    )
                },
                onHandoff = {
                    host.handoffAction(HandoffActionKind.HANDOFF_ACTION_KIND_OPEN)
                    showHandoff = true
                },
                onRenew = {
                    host.accountSecurityAction(AccountSecurityActionKind.ACCOUNT_SECURITY_ACTION_KIND_RENEW_LEASE)
                },
            )
        }
    }
}

@Composable
internal fun AccountSecurityContent(
    vm: AccountSecurityViewModel,
    onRetryPendingRemove: () -> Unit = {},
    onPending: () -> Unit = {},
    onRecovery: () -> Unit = {},
    onHandoff: () -> Unit = {},
    onRenew: () -> Unit = {},
) {
    val copy = uiText()
    val managerName = uiText(vm.managerDisplay).ifEmpty {
        if (vm.isManager) copy(UiMessages.securityThisDevice()) else copy(UiMessages.securityClient())
    }
    val leaseExpiry = uiText(vm.leaseExpiryText)
    val pendingRemove = uiText(vm.pendingRemoveBanner)
    val err = if (vm.errorBanner.visible) uiText(vm.errorBanner.text) else uiText(vm.authorityErrorBanner)
    val success = uiText(vm.successNotice)
    PageScaffold(copy(UiMessages.securityAccountSecurity()), readable = true) {
        Column(Modifier.fillMaxWidth().verticalScrollFaded().padding(vertical = 16.dp)) {
            if (vm.recoveryPending) {
                val banner = uiText(vm.recoveryTakeoverBanner)
                if (banner.isNotEmpty()) {
                    Notice(
                        banner,
                        Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
                        NoticeTone.WARN,
                        action = if (vm.canVetoRecovery) copy(UiMessages.securityReject()) else null,
                        onAction = if (vm.canVetoRecovery) onRecovery else null,
                    )
                }
            }
            if (pendingRemove.isNotEmpty()) {
                Notice(
                    pendingRemove,
                    Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
                    NoticeTone.WARN,
                    action = copy(UiMessages.commonRetry()),
                    onAction = onRetryPendingRemove,
                )
            }
            if (success.isNotEmpty()) Notice(success, Modifier.padding(16.dp), NoticeTone.PLAIN)
            if (err.isNotEmpty()) Notice(err, Modifier.padding(16.dp), NoticeTone.ERROR)
            if (vm.hasPendingOp) {
                Group(rows = listOf({ GRow(copy(UiMessages.securityUnfinishedSecurityOperation()), chevron = true, onClick = onPending) }))
            }
            Group(
                rows = listOf<@Composable () -> Unit>(
                    { GRow(copy(UiMessages.securityCurrentManagingClient()), managerName) },
                    { GRow(copy(UiMessages.securityRemoteControlExpiry()), leaseExpiry) },
                ) + if (vm.isManager) listOf<@Composable () -> Unit>({ GRow(copy(UiMessages.securityRenewNow()), action = true, enabled = vm.canRenewNow, onClick = onRenew) }) else emptyList(),
            )
            Hint(copy(UiMessages.securityAndroidRenewsAutomaticallyWhenYouReturnToTheManagingClientAfter()), Modifier.padding(bottom = 8.dp))
            if (!vm.isManager) Hint(copy(UiMessages.securityOpenToRenew(managerName)), Modifier.padding(bottom = 8.dp))
            if (vm.canOpenRecovery && vm.canOpenHandoff) {
                Group(rows = listOf(
                    { GRow(copy(UiMessages.securityRecoveryCode()), chevron = true, onClick = onRecovery) },
                    { GRow(copy(UiMessages.securityTransferManagement()), chevron = true, onClick = onHandoff) },
                ))
            } else if (vm.canOpenHandoff) {
                Group(rows = listOf({ GRow(copy(UiMessages.securityTakeOverManagement()), chevron = true, onClick = onHandoff) }))
            }
        }
    }
}

/** 紧急恢复: Core drives EmergencyRecoveryView and SecurityRecoveryPhase. */
@Composable
fun RecoverScreen(
    coreHost: CoreHost,
    allowPlainHttp: Boolean = true,
    onBack: () -> Unit = {},
) {
    val copy = uiText()
    val p = pal
    val ob by coreHost.onboardingView.collectAsState()
    val rec = ob.recovery
    val phase = rec.phase
    val inForm = phase == SecurityRecoveryPhase.SECURITY_RECOVERY_PHASE_FORM ||
        phase == SecurityRecoveryPhase.SECURITY_RECOVERY_PHASE_UNSPECIFIED

    var oldCode by remember { mutableStateOf("") }
    val settledEditSeq by coreHost.settledOnboardingEditSeq.collectAsState()
    var serverWaitSeq by remember { mutableStateOf(0L) }
    val bannerErr = if (rec.banner.visible) uiText(rec.banner.text) else ""
    val t = now()

    PageScaffold(copy(UiMessages.securityRecoverManagementWithARecoveryCode()), onBack = onBack, wrapTitle = true) {
        LegacyRepairBanner(ob.showLegacyRepairBanner, coreHost::appRootDismissUpgradeNotice)
        if ((bannerErr.isNotEmpty() || rec.showSetupReset) && !rec.busy) DiscardSetup(coreHost)
        FormColumn(
            Modifier.fillMaxWidth()
                .verticalScrollFaded()
                .imePadding(),
        ) {
            if (inForm) {
                Text(
                    copy(UiMessages.securityAndroidEnterTheRecoveryCodeSavedDuringSetupAfterSubmittingWait()),
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.padding(vertical = 10.dp),
                )
                if (bannerErr.isNotEmpty()) Notice(bannerErr, Modifier.padding(bottom = 10.dp), NoticeTone.ERROR)
                SecurityField(copy(UiMessages.securityRecoveryCode()), oldCode, { oldCode = it }, "XXXX-XXXX-…", lines = 3)
                SecurityField(
                    copy(UiMessages.securityServer()),
                    rec.serverInput,
                    { serverWaitSeq = coreHost.onboardingRecoveryEdit(serverInput = it) },
                    "https://",
                    editPending = settledEditSeq < serverWaitSeq,
                )
                PillButton(
                    if (rec.busy) copy(UiMessages.securityAndroidSubmitting()) else copy(UiMessages.securityAndroidSubmitRecovery()),
                    {
                        val codeToSubmit = oldCode
                        oldCode = ""
                        coreHost.onboardingRecoveryAction(
                            kind = OnboardingRecoveryActionKind.ONBOARDING_RECOVERY_ACTION_KIND_PREPARE,
                            serverInput = rec.serverInput.trim(),
                            oldCode = codeToSubmit,
                        )
                    },
                    Modifier.fillMaxWidth().padding(top = 8.dp),
                    ButtonKind.INK,
                    enabled = rec.canPrepare && oldCode.isNotBlank(),
                )
                TextAction(copy(UiMessages.commonCancel()), onBack, alignment = Alignment.CenterStart)
            } else {
                val rejected = phase == SecurityRecoveryPhase.SECURITY_RECOVERY_PHASE_REJECTED
                if (rejected) {
                    Hint(copy(UiMessages.securityAndroidThisRecoveryWasVetoedTheNewRecoveryCodeGeneratedFor()), Modifier)
                } else if (rec.newRecoveryCode.isNotEmpty()) {
                    GLabel(copy(UiMessages.securityNewRecoveryCodeShownOnlyOnce()))
                    RecoveryCodeFigure(rec.newRecoveryCode)
                    Hint(copy(UiMessages.securityAndroidTakesEffectAfter48HoursAVetoFromTheOriginal()), Modifier)
                } else {
                    Hint(copy(UiMessages.securityAndroidThisRecoveryWasSubmittedIfYouHaveNotSavedThe()), Modifier)
                }

                if (phase == SecurityRecoveryPhase.SECURITY_RECOVERY_PHASE_UNSIGNED_DRAFT) {
                    SecurityField(copy(UiMessages.securityOriginalRecoveryCode()), oldCode, { oldCode = it }, "XXXX-XXXX-…", lines = 3)
                    PillButton(
                        copy(UiMessages.securityContinueOriginalDraft()),
                        {
                            val c = oldCode
                            oldCode = ""
                            coreHost.onboardingRecoveryAction(
                                kind = OnboardingRecoveryActionKind.ONBOARDING_RECOVERY_ACTION_KIND_CONTINUE_SIGNING,
                                oldCode = c,
                            )
                        },
                        Modifier.fillMaxWidth().padding(top = 8.dp),
                        ButtonKind.SECONDARY,
                        enabled = rec.canContinueSigning && !rec.busy && oldCode.isNotBlank(),
                    )
                    TextAction(
                        copy(UiMessages.securityAbandonUnsignedDraft()),
                        {
                            oldCode = ""
                            coreHost.onboardingRecoveryAction(OnboardingRecoveryActionKind.ONBOARDING_RECOVERY_ACTION_KIND_DISCARD_UNSIGNED)
                        },
                        enabled = rec.canDiscard && !rec.busy,
                    )
                }

                if (!rejected && rec.canConfirmCodeSaved) {
                    PillButton(
                        copy(UiMessages.securityAndroidISavedTheNewRecoveryCodeOffline()),
                        {
                            coreHost.onboardingRecoveryAction(OnboardingRecoveryActionKind.ONBOARDING_RECOVERY_ACTION_KIND_CONFIRM_CODE_SAVED)
                        },
                        Modifier.fillMaxWidth().padding(top = 8.dp),
                        ButtonKind.SECONDARY,
                        enabled = !rec.busy,
                    )
                }
                GLabel(copy(UiMessages.securityAndroidRecoveryCodeBackupStatus()))
                Hint(
                    when {
                        rec.codeSavedChecked -> copy(UiMessages.securityAndroidNewRecoveryCodeConfirmedSavedOffline())
                        rec.newRecoveryCode.isNotEmpty() -> copy(UiMessages.securityAndroidTheNewCodeIsStillOnThisPageSaveIt())
                        else -> copy(UiMessages.securityAndroidBackupNotConfirmedThisPhoneHasNoRecoveryPrivateKey())
                    },
                    Modifier,
                )
                GLabel(copy(UiMessages.securityAndroidWait()))
                Hint(
                    if (rec.readyMs > 0L) copy(UiMessages.securityAndroidRecoveryEligibleAt(io.github.lewin671.remoteagent.core.securityTime(rec.readyMs, t, copy = copy)))
                    else copy(UiMessages.securityAndroidCompletionRequires48Hours()),
                    Modifier,
                )
                RecoveryStatus(
                    rec = rec,
                    error = bannerErr,
                    onRefresh = { coreHost.onboardingRecoveryAction(OnboardingRecoveryActionKind.ONBOARDING_RECOVERY_ACTION_KIND_REFRESH_OR_SUBMIT) },
                    onComplete = { coreHost.onboardingRecoveryAction(OnboardingRecoveryActionKind.ONBOARDING_RECOVERY_ACTION_KIND_COMPLETE) },
                    onRestart = {
                        oldCode = ""
                        coreHost.onboardingRecoveryAction(OnboardingRecoveryActionKind.ONBOARDING_RECOVERY_ACTION_KIND_DISCARD_REJECTED)
                    },
                )
            }
        }
    }
}

/** Status and actions driven directly by Core's [EmergencyRecoveryView] flags and phase. */
@Composable
internal fun RecoveryStatus(
    rec: EmergencyRecoveryView,
    error: String = if (rec.banner.visible) uiText(rec.banner.text) else "",
    onRefresh: () -> Unit,
    onComplete: () -> Unit,
    onRestart: () -> Unit,
) {
    val copy = uiText()
    TextAction(copy(UiMessages.securityAndroidViewStatus()), onRefresh, enabled = rec.canSubmitOrRefresh, alignment = Alignment.CenterStart)
    when (rec.phase) {
        SecurityRecoveryPhase.SECURITY_RECOVERY_PHASE_READY_TO_COMPLETE ->
            Hint(copy(UiMessages.securityAndroidRecoveryCanNowBeCompleted()), Modifier)
        SecurityRecoveryPhase.SECURITY_RECOVERY_PHASE_SUBMITTED_WAITING ->
            Hint(copy(UiMessages.securityAndroidStillWithinThe48HourWaitingPeriod()), Modifier)
        SecurityRecoveryPhase.SECURITY_RECOVERY_PHASE_REJECTED ->
            Hint(copy(UiMessages.securityAndroidTheOriginalManagingClientVetoedThisRecovery()), Modifier)
        else -> {}
    }
    if (error.isNotEmpty()) Notice(error, Modifier.padding(vertical = 8.dp), NoticeTone.ERROR)
    if (rec.phase == SecurityRecoveryPhase.SECURITY_RECOVERY_PHASE_REJECTED) {
        Hint(copy(UiMessages.securityAndroidTheNewRecoveryCodeGeneratedForThisRequestIsInvalid()), Modifier)
        PillButton(
            if (rec.busy) copy(UiMessages.securityAndroidClearing()) else copy(UiMessages.securityAndroidAbandonThisRecoveryAndEnterANewCode()),
            onRestart,
            Modifier.fillMaxWidth().padding(top = 10.dp),
            ButtonKind.SECONDARY,
            enabled = rec.canDiscard,
        )
    } else {
        PillButton(
            if (rec.busy) copy(UiMessages.securityAndroidCompleting()) else copy(UiMessages.securityCompleteRecovery()),
            onComplete,
            Modifier.fillMaxWidth().padding(top = 10.dp),
            ButtonKind.INK,
            enabled = rec.canComplete,
        )
        Hint(copy(UiMessages.securityAndroidIf48HoursHaveNotPassedOrTheOriginalManaging()), Modifier.padding(top = 8.dp))
    }
}

/** 新账户: Core drives CreateAccountView; setup token stays transient in Compose state only. */
@Composable
fun CreateAccountScreen(
    coreHost: CoreHost,
    allowPlainHttp: Boolean = true,
    onBack: () -> Unit = {},
) {
    val copy = uiText()
    val p = pal
    val ob by coreHost.onboardingView.collectAsState()
    val cv = ob.create
    var setupToken by remember { mutableStateOf("") }
    val settledEditSeq by coreHost.settledOnboardingEditSeq.collectAsState()
    var serverWaitSeq by remember { mutableStateOf(0L) }
    val err = if (cv.banner.visible) uiText(cv.banner.text) else ""
    val inForm = cv.step == CreateAccountStep.CREATE_ACCOUNT_STEP_FORM ||
        cv.step == CreateAccountStep.CREATE_ACCOUNT_STEP_UNSPECIFIED

    PageScaffold(copy(UiMessages.securityCreateAccount()), onBack = onBack) {
        LegacyRepairBanner(ob.showLegacyRepairBanner, coreHost::appRootDismissUpgradeNotice)
        if ((err.isNotEmpty() || cv.showSetupReset) && !cv.busy) DiscardSetup(coreHost)
        FormColumn(
            Modifier.fillMaxWidth()
                .verticalScrollFaded()
                .imePadding(),
        ) {
            if (inForm) {
                Text(
                    copy(UiMessages.securityAndroidThisPhoneWillBecomeTheManagingClientTheAccountRoot()),
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.padding(vertical = 10.dp),
                )
                if (err.isNotEmpty()) Notice(err, Modifier.padding(bottom = 10.dp), NoticeTone.ERROR)
                SecurityField(
                    copy(UiMessages.securityServer()),
                    cv.serverInput,
                    { serverWaitSeq = coreHost.onboardingCreateEdit(serverInput = it) },
                    "https://",
                    editPending = settledEditSeq < serverWaitSeq,
                )
                SecurityField(copy(UiMessages.securityAndroidSetupTokenIfRequiredByTheServer()), setupToken, { setupToken = it }, copy(UiMessages.securityAndroidOptional()))
                PillButton(
                    if (cv.busy) copy(UiMessages.scheduleCreating()) else copy(UiMessages.securityAndroidCreateAccount()),
                    { coreHost.onboardingCreateSubmit(serverInput = cv.serverInput.trim(), setupToken = setupToken.trim(), confirmCodeSaved = false) },
                    Modifier.fillMaxWidth().padding(top = 8.dp),
                    ButtonKind.INK,
                    enabled = cv.canSubmit,
                )
            } else {
                if (cv.recoveryCode.isNotEmpty()) {
                    GLabel(copy(UiMessages.securityAndroidRecoveryCodeShownOnlyOnce()))
                    RecoveryCodeFigure(cv.recoveryCode)
                    Hint(copy(UiMessages.securityAndroidUseThisToRecoverManagementIfTheManagingClientIs()), Modifier)
                } else {
                    Hint(copy(UiMessages.securityTheOriginalCreationDraftIsPreservedContinueWithThe()), Modifier)
                    SecurityField(copy(UiMessages.securityAndroidSetupTokenIfRequiredByTheServer()), setupToken, { setupToken = it }, copy(UiMessages.securityAndroidOptional()))
                }
                if (err.isNotEmpty()) Notice(err, Modifier.padding(vertical = 8.dp), NoticeTone.ERROR)
                PillButton(
                    if (cv.busy) copy(UiMessages.securityAndroidSaving()) else copy(UiMessages.securityAndroidISavedItStartUsingTheApp()),
                    { coreHost.onboardingCreateSubmit(serverInput = cv.serverInput.trim(), setupToken = setupToken.trim(), confirmCodeSaved = true) },
                    Modifier.fillMaxWidth().padding(top = 12.dp),
                    ButtonKind.INK,
                    enabled = !cv.busy,
                )
            }
        }
    }
}

/** 更换恢复码: Core drives preparedCode / publish / error state. */
@Composable
fun RotateRecoveryScreen(onBack: () -> Unit = {}) {
    val remote = LocalRemote.current
    val host = remote.coreHost
    val vm by host.accountSecurityView.collectAsState()
    RotateRecoveryContent(
        vm = vm,
        onPrepare = { host.accountSecurityAction(AccountSecurityActionKind.ACCOUNT_SECURITY_ACTION_KIND_PREPARE_ROTATION) },
        onPublish = {
            host.accountSecurityAction(AccountSecurityActionKind.ACCOUNT_SECURITY_ACTION_KIND_SET_ROTATION_SAVED, flag = true)
            host.accountSecurityAction(AccountSecurityActionKind.ACCOUNT_SECURITY_ACTION_KIND_PUBLISH_ROTATION)
        },
        onCancelPrepared = {
            host.accountSecurityAction(AccountSecurityActionKind.ACCOUNT_SECURITY_ACTION_KIND_DISCARD_PENDING, flag = false)
        },
        onBack = onBack,
    )
}

@Composable
internal fun RotateRecoveryContent(
    vm: AccountSecurityViewModel,
    onPrepare: () -> Unit,
    onPublish: () -> Unit,
    onCancelPrepared: () -> Unit = {},
    onBack: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    val err = if (vm.errorBanner.visible) uiText(vm.errorBanner.text) else ""
    val success = uiText(vm.successNotice)
    PageScaffold(copy(UiMessages.securityRecoveryCode()), onBack = onBack) {
        FormColumn(
            Modifier.fillMaxWidth()
                .verticalScrollFaded()
                .imePadding(),
        ) {
            if (success.isNotEmpty()) {
                Notice(success, Modifier.padding(vertical = 8.dp), NoticeTone.PLAIN)
            }
            if (vm.preparedCode.isEmpty()) {
                Text(
                    copy(UiMessages.securityReplacingTheRecoveryCodeImmediatelyInvalidatesTheOldCode()),
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.padding(vertical = 10.dp),
                )
                if (err.isNotEmpty()) Notice(err, Modifier.padding(vertical = 8.dp), NoticeTone.ERROR)
                PillButton(
                    if (vm.busy) copy(UiMessages.securityAndroidGenerating()) else copy(UiMessages.securityGenerateNewRecoveryCode()),
                    onPrepare,
                    Modifier.fillMaxWidth().padding(top = 8.dp),
                    ButtonKind.INK,
                    enabled = vm.canPrepareRotation && !vm.busy,
                )
            } else {
                GLabel(copy(UiMessages.securityNewRecoveryCodeShownOnlyOnce()))
                RecoveryCodeFigure(vm.preparedCode)
                Hint(copy(UiMessages.securityAndroidWriteItDownOrPhotographItBeforePublishingPublishingImmediately()), Modifier)
                if (err.isNotEmpty()) Notice(err, Modifier.padding(vertical = 8.dp), NoticeTone.ERROR)
                PillButton(
                    if (vm.busy) copy(UiMessages.securityAndroidPublishing()) else copy(UiMessages.securitySavedPublishReplacement()),
                    onPublish,
                    Modifier.fillMaxWidth().padding(top = 12.dp),
                    ButtonKind.INK,
                    enabled = !vm.busy,
                )
                PillButton(
                    copy(UiMessages.securityAndroidDoNotPublishYet()),
                    onCancelPrepared,
                    Modifier.fillMaxWidth().padding(top = 6.dp),
                    ButtonKind.SECONDARY,
                    enabled = !vm.busy,
                )
            }
        }
    }
}
