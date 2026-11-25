package io.github.lewin671.remoteagent.ui

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.wrapContentWidth
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.CoreHost
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.ExitIntentKind
import io.github.lewin671.remoteagent.core.proto.JoinPairingStep
import io.github.lewin671.remoteagent.core.proto.OnboardingMode
import io.github.lewin671.remoteagent.core.proto.OnboardingViewModel
import io.github.lewin671.remoteagent.core.proto.RemovedDeviceView
import io.github.lewin671.remoteagent.core.proto.RetainedDeniedView
import io.github.lewin671.remoteagent.core.proto.SecurityBannerAction
import io.github.lewin671.remoteagent.core.proto.SecurityBannerTone

private fun bannerActionLabel(action: SecurityBannerAction, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String): String =
    when (action) {
        SecurityBannerAction.SECURITY_BANNER_ACTION_RETRY -> copy(UiMessages.commonRetry())
        SecurityBannerAction.SECURITY_BANNER_ACTION_REFILL -> copy(UiMessages.securityReEnter())
        SecurityBannerAction.SECURITY_BANNER_ACTION_RESTART -> copy(UiMessages.securityStartOver())
        else -> ""
    }

/** The server turned this device away: nothing of the account is shown until it joins again. */
@Composable
fun RemovedScreen(
    view: RemovedDeviceView,
    coreHost: CoreHost,
    remote: Remote? = null,
) {
    val copy = uiText()
    val p = pal
    val exitView by coreHost.exitConfirmView.collectAsState()
    val title = if (view.removalVerified) {
        copy(UiMessages.settingsThisDeviceWasRemovedFromTheAccount())
    } else {
        copy(UiMessages.securityAndroidTheServerRejectedThisDeviceSLogin())
    }
    val body = if (view.removalVerified) {
        copy(UiMessages.settingsSignedAccountStateConfirmsThisDeviceWasRemovedSessions())
    } else {
        copy(UiMessages.settingsTheDeviceMayHaveBeenRemovedTheServiceReset())
    }
    Column(
        Modifier.fillMaxSize().background(p.paper).padding(horizontal = 24.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text(title, style = T.s17b, color = p.ink, textAlign = TextAlign.Center)
        Text(
            body,
            style = T.s13,
            color = p.ink2,
            textAlign = TextAlign.Center,
            modifier = Modifier.padding(top = 8.dp, bottom = 20.dp),
        )
        PillButton(
            copy(UiMessages.settingsRejoinAccount()),
            { coreHost.exitRequest(ExitIntentKind.EXIT_INTENT_KIND_REJOIN_PAIRING, view.server) },
            Modifier.fillMaxWidth(),
            ButtonKind.INK,
            enabled = !exitView.busy,
        )
        ExitConfirmDialog(view = exitView, onConfirm = coreHost::exitConfirm)
        if (view.hasPendingOp) {
            TextAction(
                copy(UiMessages.securityAndroidVerifyOriginalSecurityOperation()),
                { coreHost.appRootVerifyPendingOp() },
                enabled = view.canVerifyPending && !view.busy,
            )
        }
        val verifyResult = uiText(view.verifyResultText)
        if (verifyResult.isNotEmpty()) Hint(verifyResult)
        Text(
            copy(UiMessages.securityAndroidFirstDeleteTheAccountKeysAndCacheOnThisDevice()),
            style = T.s13,
            color = p.ink2,
            textAlign = TextAlign.Center,
            modifier = Modifier.padding(top = 10.dp, bottom = 4.dp),
        )
        if (view.canRetryLogin && remote != null) {
            TextAction(
                copy(UiMessages.sessionTryAgain()),
                { remote.retryLogin() },
                alignment = Alignment.Center,
            )
        }
    }
}

@Composable
fun RetainedDeniedScreen(
    view: RetainedDeniedView,
    coreHost: CoreHost,
) {
    val copy = uiText()
    val p = pal
    val exitView by coreHost.exitConfirmView.collectAsState()
    Column(
        Modifier.fillMaxSize().background(p.paper).padding(24.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        LegacyRepairBanner(view.showLegacyRepairBanner, coreHost::appRootDismissUpgradeNotice)
        Text(
            copy(UiMessages.securityTheServerDoesNotRecognizeThisDevice()),
            style = T.s17b,
            color = p.ink,
        )
        Hint(
            copy(UiMessages.securityItMayHaveBeenRemovedOrTheServiceMay()),
            inset = 0.dp,
        )
        PillButton(
            copy(UiMessages.securityRejoinWithAPairingCode()),
            { coreHost.exitRequest(ExitIntentKind.EXIT_INTENT_KIND_REJOIN_PAIRING, view.server) },
            Modifier.fillMaxWidth(),
            ButtonKind.INK,
            enabled = !exitView.busy,
        )
        ExitConfirmDialog(view = exitView, onConfirm = coreHost::exitConfirm)
        Hint(copy(UiMessages.securityAndroidFirstDeleteTheAccountKeysAndCacheOnThisPhone()), inset = 0.dp)
    }
}

@Composable
internal fun LegacyRepairBanner(
    visible: Boolean,
    onDismiss: () -> Unit,
    modifier: Modifier = Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
) {
    if (!visible) return
    val copy = uiText()
    Notice(
        copy(UiMessages.securityUpgradeRepairTitle()) + "\n" + copy(UiMessages.securityUpgradeRepairNotice()),
        modifier,
        NoticeTone.WARN,
        action = copy(UiMessages.commonGotIt()),
        onAction = onDismiss,
    )
}

/** First run: join the account with a pairing code or scanned QR code, or create/recover. */
@Composable
fun JoinScreen(
    app: RaApp,
    coreHost: CoreHost = remember(app) { app.ensureForegroundCoreHost() },
) {
    val vm by coreHost.onboardingView.collectAsState()
    OnboardingContent(
        vm = vm,
        coreHost = coreHost,
        allowPlainHttp = app.debuggable,
    )
}

@Composable
internal fun OnboardingContent(
    vm: OnboardingViewModel,
    coreHost: CoreHost,
    allowPlainHttp: Boolean = true,
) {
    val copy = uiText()
    val p = pal
    val mode = vm.mode
    if (mode == OnboardingMode.ONBOARDING_MODE_ENTRY || mode == OnboardingMode.ONBOARDING_MODE_UNSPECIFIED) {
        BoxWithConstraints(Modifier.fillMaxSize().background(p.paper)) {
            val minH = maxHeight
            Column(
                Modifier
                    .fillMaxSize()
                    .verticalScrollFaded(p.paper)
                    .heightIn(min = minH)
                    .padding(vertical = 24.dp),
                verticalArrangement = Arrangement.SpaceBetween,
            ) {
                Column(Modifier.fillMaxWidth().padding(start = 24.dp, end = 24.dp, top = 28.dp)) {
                    AppMarkBadge(size = 56.dp, corner = 14.dp)
                    Spacer(Modifier.height(18.dp))
                    Text("remote-agent", style = T.title, color = p.ink)
                    Spacer(Modifier.height(8.dp))
                    Text(copy(UiMessages.securityControlAiCodingClisOnYourComputerFromAny()), style = T.s17, color = p.ink2)
                }
                LegacyRepairBanner(vm.showLegacyRepairBanner, coreHost::appRootDismissUpgradeNotice)
                Column(Modifier.fillMaxWidth().padding(top = 28.dp)) {
                    Group(rows = listOf(
                        {
                            EntryRow(
                                copy(UiMessages.securityCreateAccount()),
                                copy(UiMessages.securityThisDeviceBecomesTheManagingClient()),
                            ) { coreHost.onboardingSetMode(OnboardingMode.ONBOARDING_MODE_CREATE) }
                        },
                        {
                            EntryRow(
                                copy(UiMessages.securityJoinAnExistingAccount()),
                                copy(UiMessages.securityJoinWithACodeFromTheManagingClient()),
                            ) { coreHost.onboardingSetMode(OnboardingMode.ONBOARDING_MODE_JOIN) }
                        },
                        {
                            EntryRow(
                                copy(UiMessages.securityRecoverManagementWithARecoveryCode()),
                                copy(UiMessages.securityUseWhenTheManagingClientIsLost()),
                            ) { coreHost.onboardingSetMode(OnboardingMode.ONBOARDING_MODE_RECOVERY) }
                        },
                    ))
                    Text(
                        copy(UiMessages.securityEntryE2EeNote()),
                        style = T.s13,
                        color = p.ink2,
                        textAlign = TextAlign.Center,
                        modifier = Modifier.fillMaxWidth().padding(horizontal = 24.dp, vertical = 4.dp),
                    )
                }
            }
        }
        return
    }

    if (mode == OnboardingMode.ONBOARDING_MODE_CREATE) {
        CreateAccountScreen(
            coreHost = coreHost,
            allowPlainHttp = allowPlainHttp,
            onBack = { coreHost.onboardingSetMode(OnboardingMode.ONBOARDING_MODE_ENTRY) },
        )
        return
    }
    if (mode == OnboardingMode.ONBOARDING_MODE_RECOVERY) {
        RecoverScreen(
            coreHost = coreHost,
            allowPlainHttp = allowPlainHttp,
            onBack = {
                val nextMode = if (vm.join.isExistingRepair) OnboardingMode.ONBOARDING_MODE_JOIN else OnboardingMode.ONBOARDING_MODE_ENTRY
                coreHost.onboardingSetMode(nextMode)
            },
        )
        return
    }

    // ONBOARDING_MODE_JOIN
    val j = vm.join
    var scanning by remember { mutableStateOf(false) }
    val step = j.step

    BackHandler(enabled = scanning) { scanning = false }
    BackHandler(enabled = !scanning && step == JoinPairingStep.JOIN_PAIRING_STEP_FORM && vm.canBack) {
        coreHost.onboardingSetMode(OnboardingMode.ONBOARDING_MODE_ENTRY)
    }
    BackHandler(enabled = !scanning && step != JoinPairingStep.JOIN_PAIRING_STEP_FORM && j.canCancel) {
        coreHost.onboardingJoinCancel()
    }

    if (scanning) {
        QrScanner(
            onResult = { text ->
                if (!scanning) return@QrScanner
                scanning = false
                coreHost.onboardingJoinSubmit(rawLink = text)
            },
            onCancel = { scanning = false },
        )
        return
    }

    Column(
        Modifier.fillMaxSize()
            .background(p.paper)
            .imePadding()
            .verticalScrollFaded(),
    ) {
        when (step) {
            JoinPairingStep.JOIN_PAIRING_STEP_COMPARE_SAS,
            JoinPairingStep.JOIN_PAIRING_STEP_WAITING_APPROVAL -> {
                val awaitingConfirm = step == JoinPairingStep.JOIN_PAIRING_STEP_COMPARE_SAS
                PageBar(
                    copy(UiMessages.securityComparePatterns()),
                    null,
                    right = {
                        if (j.canCancel) {
                            Text(
                                copy(UiMessages.commonCancel()),
                                style = T.s15,
                                color = p.blue,
                                modifier = Modifier.clickable { coreHost.onboardingJoinCancel() },
                            )
                        }
                    },
                    more = { if (j.canCancel) coreHost.onboardingJoinCancel() },
                )
                Text(
                    j.sasEmojiList.joinToString("  "),
                    fontSize = 30.sp,
                    modifier = Modifier.fillMaxWidth().padding(top = 40.dp, bottom = 12.dp),
                    textAlign = TextAlign.Center,
                )
                Text(
                    copy(UiMessages.securityAndroidCompareThe4PatternsOnTheManagingClientIfThey()),
                    style = T.s13,
                    color = p.ink2,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                )
                if (awaitingConfirm && j.canConfirmSas) {
                    PillButton(
                        copy(UiMessages.securityPatternsMatchTrustAndContinue()),
                        { coreHost.onboardingJoinConfirmSas() },
                        Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                        ButtonKind.INK,
                    )
                }
                IconText(
                    Lucide.LoaderCircle,
                    if (awaitingConfirm) copy(UiMessages.securityConfirmThePatternsOnThisDeviceFirst())
                    else copy(UiMessages.securityAndroidWaitingForManagingClientApprovalAndCompletingJoin()),
                    p.ink2,
                    T.s13,
                    Modifier.fillMaxWidth().padding(vertical = 20.dp).wrapContentWidth(),
                )
                PillButton(
                    copy(UiMessages.securityPatternsDifferReturnToCodeEntry()),
                    { coreHost.onboardingJoinCancel() },
                    Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                    enabled = j.canCancel,
                )
                Hint(copy(UiMessages.securityAndroidGoingBackStopsThisWaitRejectTheOldRequestOn()))
            }

            JoinPairingStep.JOIN_PAIRING_STEP_WORKING -> {
                JoinTitle(
                    if (j.isExistingRepair) copy(UiMessages.securityOneTimeSecuritySetup())
                    else copy(UiMessages.securityJoinAnExistingAccount()),
                )
                IconText(
                    Lucide.LoaderCircle,
                    copy(UiMessages.securityAndroidConnecting()),
                    p.ink2,
                    T.s13,
                    Modifier.padding(horizontal = 16.dp),
                )
                JoinBannerNotice(j, coreHost, Modifier.padding(horizontal = 16.dp, vertical = 10.dp))
                if (j.showSetupReset) DiscardSetup(coreHost)
            }

            else -> { // FORM
                JoinTitle(
                    if (j.isExistingRepair) copy(UiMessages.securityOneTimeSecuritySetup())
                    else copy(UiMessages.securityJoinAnExistingAccount()),
                )
                LegacyRepairBanner(vm.showLegacyRepairBanner, coreHost::appRootDismissUpgradeNotice)
                Text(
                    if (j.isExistingRepair)
                        copy(UiMessages.securityAndroidTheOriginalDeviceIdentityKeysAndHistoryAreKeptOpen())
                    else copy(UiMessages.securityAndroidOpenSettingsAddDeviceOnTheManagingClientScanIts()),
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.padding(horizontal = 16.dp).padding(bottom = 14.dp),
                )
                if (JoinBannerNotice(j, coreHost, Modifier.padding(horizontal = 16.dp).padding(bottom = 10.dp)) && j.showSetupReset) {
                    DiscardSetup(coreHost)
                }
                val codeDisplay = j.formattedCode
                val settledEditSeq by coreHost.settledOnboardingEditSeq.collectAsState()
                var codeFieldState by remember {
                    mutableStateOf(TextFieldValue(codeDisplay, TextRange(codeDisplay.length)))
                }
                var lastCoreCode by remember { mutableStateOf(codeDisplay) }
                var codeWaitSeq by remember { mutableStateOf(0L) }
                var codeEditedByUser by remember { mutableStateOf(false) }
                val codePending = settledEditSeq < codeWaitSeq
                if (!codePending) {
                    if (codeEditedByUser || codeDisplay != lastCoreCode) {
                        val mappedSelection = if (codeEditedByUser) {
                            mapPairingCodeSelection(codeFieldState.text, codeFieldState.selection, codeDisplay)
                        } else {
                            TextRange(codeDisplay.length)
                        }
                        if (codeFieldState.text != codeDisplay || codeFieldState.selection != mappedSelection) {
                            codeFieldState = TextFieldValue(text = codeDisplay, selection = mappedSelection)
                        }
                        lastCoreCode = codeDisplay
                        codeEditedByUser = false
                    }
                }
                androidx.compose.foundation.text.BasicTextField(
                    codeFieldState,
                    { next ->
                        val prevText = codeFieldState.text
                        codeFieldState = next
                        if (next.text != prevText) {
                            codeEditedByUser = true
                            codeWaitSeq = coreHost.onboardingJoinEdit(codeInput = next.text)
                        }
                    },
                    Modifier.fillMaxWidth()
                        .padding(horizontal = 16.dp)
                        .padding(bottom = 10.dp)
                        .testTag("join-pairing-code"),
                    textStyle =
                        androidx.compose.ui.text.TextStyle(
                            fontFamily = MonoFamily,
                            fontWeight = androidx.compose.ui.text.font.FontWeight.SemiBold,
                            fontSize = 22.sp,
                            letterSpacing = 4.sp,
                            textAlign = TextAlign.Center,
                            color = p.ink,
                        ),
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                    singleLine = true,
                    cursorBrush = androidx.compose.ui.graphics.SolidColor(p.blue),
                    decorationBox = { inner ->
                        FieldBox {
                            if (codeFieldState.text.isEmpty()) {
                                Text(
                                    copy(UiMessages.securityAndroidEnterThe8DigitPairingCode()),
                                    style = T.s15,
                                    color = p.ink2,
                                    textAlign = TextAlign.Center,
                                    modifier = Modifier.fillMaxWidth(),
                                )
                            }
                            inner()
                        }
                    },
                )
                var serverWaitSeq by remember { mutableStateOf(0L) }
                SecurityField(
                    copy(UiMessages.securityServer()),
                    j.serverInput,
                    {
                        if (j.serverEditable) {
                            serverWaitSeq = coreHost.onboardingJoinEdit(serverInput = it)
                        }
                    },
                    "https://",
                    horizontalPadding = 16.dp,
                    keyboard = KeyboardType.Uri,
                    readOnly = !j.serverEditable,
                    editPending = settledEditSeq < serverWaitSeq,
                    fieldTestTag = "join-server-input",
                )
                PillButton(
                    copy(if (j.pendingRegistration) UiMessages.commonRetry() else UiMessages.securityJoin()),
                    {
                        if (j.pendingRegistration) {
                            coreHost.onboardingJoinBannerAction()
                        } else {
                            coreHost.onboardingJoinSubmit(
                                serverInput = j.serverInput.trim(),
                                codeInput = codeDisplay.filter { it.isDigit() },
                            )
                        }
                    },
                    Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                    ButtonKind.INK,
                    enabled = j.pendingRegistration || j.canSubmit,
                )
                JoinLink(copy(UiMessages.securityAndroidScanQrCode())) {
                    scanning = true
                }
                if (j.isExistingRepair) {
                    JoinLink(copy(UiMessages.securityRecoverManagementWithARecoveryCode())) {
                        coreHost.onboardingSetMode(OnboardingMode.ONBOARDING_MODE_RECOVERY)
                    }
                } else if (vm.canBack) {
                    JoinLink(copy(UiMessages.commonBack())) {
                        coreHost.onboardingSetMode(OnboardingMode.ONBOARDING_MODE_ENTRY)
                    }
                }
            }
        }
    }
}

internal fun bannerTone(tone: SecurityBannerTone): NoticeTone = when (tone) {
    SecurityBannerTone.SECURITY_BANNER_TONE_ERROR -> NoticeTone.ERROR
    SecurityBannerTone.SECURITY_BANNER_TONE_WARN -> NoticeTone.WARN
    else -> NoticeTone.PLAIN
}

@Composable
private fun JoinTitle(text: String) {
    Text(
        text,
        style = T.title,
        color = pal.ink,
        modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 24.dp, bottom = 8.dp),
    )
}

@Composable
private fun JoinLink(text: String, onClick: () -> Unit) {
    Box(
        Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable(onClick = onClick),
        contentAlignment = Alignment.Center,
    ) {
        Text(text, style = T.s13, color = pal.blue)
    }
}

@Composable
private fun JoinBannerNotice(
    j: io.github.lewin671.remoteagent.core.proto.JoinPairingView,
    coreHost: CoreHost,
    modifier: Modifier,
): Boolean {
    if (!j.banner.visible) return false
    val copy = uiText()
    val text = uiText(j.banner.text)
    if (text.isEmpty()) return false
    val actionLabel = bannerActionLabel(j.banner.action, copy)
    Notice(
        text, modifier, tone = bannerTone(j.banner.tone),
        action = actionLabel.ifEmpty { null },
        onAction = if (actionLabel.isNotEmpty()) ({ coreHost.onboardingJoinBannerAction() }) else null,
    )
    return true
}

/**
 * Maps cursor/selection from an edited raw pairing-code string onto Core's formatted pairing code
 * by preserving the number of digits before each selection endpoint.
 */
internal fun mapPairingCodeSelection(
    editedText: String,
    editedSelection: TextRange,
    formattedText: String,
): TextRange {
    fun mapOffset(offset: Int): Int {
        val clamped = offset.coerceIn(0, editedText.length)
        var digitsBefore = 0
        for (i in 0 until clamped) {
            if (editedText[i].isDigit()) digitsBefore++
        }
        if (digitsBefore == 0) return 0
        var seen = 0
        for (i in formattedText.indices) {
            if (formattedText[i].isDigit()) {
                seen++
                if (seen == digitsBefore) return i + 1
            }
        }
        return formattedText.length
    }
    return TextRange(mapOffset(editedSelection.start), mapOffset(editedSelection.end))
}

/** Shared labeled input field for join, recovery, and account creation forms. */
@Composable
internal fun SecurityField(
    label: String,
    value: String,
    onValue: (String) -> Unit,
    placeholder: String,
    lines: Int = 1,
    horizontalPadding: androidx.compose.ui.unit.Dp = 0.dp,
    keyboard: KeyboardType = if (lines == 1) KeyboardType.Uri else KeyboardType.Text,
    readOnly: Boolean = false,
    editPending: Boolean = false,
    fieldTestTag: String? = null,
) {
    val p = pal
    var fieldState by remember { mutableStateOf(TextFieldValue(value, TextRange(value.length))) }
    var lastSyncedValue by remember { mutableStateOf(value) }
    var editedByUser by remember { mutableStateOf(false) }
    if (!editPending && (editedByUser || value != lastSyncedValue)) {
        if (fieldState.text != value) {
            val nextSelection = if (editedByUser) {
                TextRange(
                    fieldState.selection.start.coerceIn(0, value.length),
                    fieldState.selection.end.coerceIn(0, value.length),
                )
            } else {
                TextRange(value.length)
            }
            fieldState = TextFieldValue(text = value, selection = nextSelection)
        }
        lastSyncedValue = value
        editedByUser = false
    }
    androidx.compose.foundation.text.BasicTextField(
        fieldState,
        { next ->
            val prevText = fieldState.text
            fieldState = next
            if (next.text != prevText) {
                editedByUser = true
                onValue(next.text)
            }
        },
        Modifier.fillMaxWidth()
            .padding(horizontal = horizontalPadding)
            .padding(bottom = 10.dp)
            .then(if (fieldTestTag != null) Modifier.testTag(fieldTestTag) else Modifier),
        readOnly = readOnly,
        textStyle = (if (lines > 1) T.mono12 else T.s15).copy(color = p.ink),
        singleLine = lines == 1,
        minLines = lines,
        keyboardOptions = KeyboardOptions(keyboardType = keyboard),
        cursorBrush = androidx.compose.ui.graphics.SolidColor(p.blue),
        decorationBox = { inner ->
            FieldBox {
                Column {
                    Text(label, style = T.s12, color = p.ink2, modifier = Modifier.padding(bottom = 2.dp))
                    Box {
                        if (fieldState.text.isEmpty()) Text(placeholder, style = T.s15, color = p.ink2)
                        inner()
                    }
                }
            }
        },
    )
}

@Composable
private fun EntryRow(title: String, subtitle: String, onClick: () -> Unit) {
    val p = pal
    Row(
        Modifier.fillMaxWidth().heightIn(min = 50.dp).clickable(onClick = onClick)
            .padding(start = 18.dp, end = 16.dp, top = 12.dp, bottom = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Column(Modifier.weight(1f)) {
            Text(title, style = T.s17, color = p.ink)
            Text(subtitle, style = T.s13, color = p.ink2, modifier = Modifier.padding(top = 2.dp))
        }
        Arrow(p.ink3, T.s15)
    }
}
