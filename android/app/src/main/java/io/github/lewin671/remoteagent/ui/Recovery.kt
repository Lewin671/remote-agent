package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.KeyboardOptions
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
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.Secret
import io.github.lewin671.remoteagent.core.SecurityClient
import io.github.lewin671.remoteagent.core.StandaloneSecurityHost
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.SecurityBootstrapKind
import io.github.lewin671.remoteagent.core.proto.SecurityRecoveryInfo
import io.github.lewin671.remoteagent.proto.GetRecoveryContextResponse
import kotlinx.coroutines.launch

/**
 * 管理端 · 恢复: a pending recovery can be vetoed here. The new code is shown and saved first; the
 * rotated state is published only after the user confirms.
 */
@Composable
fun RecoveryScreen(onBack: () -> Unit = {}) {
    val copy = uiText()
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val secStatus by remote.securityStatus.collectAsState()
    val pendingOp = secStatus?.pendingOp
    val scope = rememberAuthorityScope()
    var page by remember { mutableStateOf("overview") }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    suspend fun read() {
        try {
            remote.getSecurityStatus()
            error = ""
        } catch (e: Exception) {
            error = displayError(e, state)
        }
    }
    LaunchedEffect(state.leaseEndMs, state.authorityDeviceId) { read() }
    if (page != "overview") androidx.activity.compose.BackHandler { page = "overview" }
    when (page) {
        "recovery" -> RotateRecoveryScreen { page = "overview" }
        "handoff" -> if (!state.isAuthority) HandoffPreparation { page = "overview" } else PageScaffold(copy(UiMessages.securityTransferManagement()), onBack = { page = "overview" }) {
            FormColumn(Modifier.fillMaxWidth().verticalScrollFaded().padding(vertical = 16.dp)) { HandoffControls() }
        }
        "pending" -> PageScaffold(copy(UiMessages.securityUnfinishedSecurityOperation()), onBack = { page = "overview" }) {
            FormColumn(Modifier.fillMaxWidth().padding(vertical = 16.dp)) {
                Hint(copy(UiMessages.securityTheSecurityOperationSOutcomeIsUnverifiedContinueThe()))
                PillButton(copy(UiMessages.securityAndroidContinueOriginalOperation()), {
                    busy = true
                    scope.launch {
                        try {
                            remote.resumeSecurityOperation()
                            val curPending = remote.securityStatus.value?.pendingOp
                            if (curPending != null && curPending.hasDelivery && curPending.deliveryPairingId.isNotEmpty()) {
                                val res = remote.finishPairingDelivery(curPending.deliveryPairingId)
                                if (!res.delivered) error = copy(UiMessages.securityTheOriginalOperationIsUnfinishedContinueVerifying())
                            }
                            val latest = remote.getSecurityStatus()
                            if (!latest.hasPendingOp()) page = "overview"
                        } catch (e: Exception) {
                            error = displayError(e, state)
                        } finally {
                            busy = false
                        }
                    }
                }, enabled = !busy)
                if (error.isNotEmpty()) Notice(error, tone = NoticeTone.ERROR)
            }
        }
        else -> AccountSecurityContent(
            state = state,
            pending = pendingOp != null,
            onPending = { page = "pending" },
            onRecovery = { page = "recovery" },
            onHandoff = { page = "handoff" },
            onRenew = { scope.launch { remote.renewLeaseNow(); read() } },
            error = error,
        )
    }
}

@Composable
internal fun AccountSecurityContent(
    state: io.github.lewin671.remoteagent.core.RemoteState,
    pending: Boolean = false,
    onPending: () -> Unit = {},
    onRecovery: () -> Unit = {},
    onHandoff: () -> Unit = {},
    onRenew: () -> Unit = {},
    error: String = "",
) {
    val copy = uiText()
    val t = now()
    val self = LocalRemote.current.account.deviceId
    PageScaffold(copy(UiMessages.securityAccountSecurity()), readable = true) {
        Column(Modifier.fillMaxWidth().verticalScrollFaded().padding(vertical = 16.dp)) {
            if (state.recoveryPending) Notice(
                copy(UiMessages.securitySomeoneIsRequestingManagementTakeoverWithARecoveryCode((io.github.lewin671.remoteagent.core.securityTime(state.recoveryReadyMs, t, copy = copy)).toString())),
                Modifier.padding(horizontal = 16.dp, vertical = 6.dp), NoticeTone.WARN,
                action = if (state.isAuthority) copy(UiMessages.securityReject()) else null, onAction = onRecovery,
            )
            if (error.isNotEmpty()) Notice(error, Modifier.padding(16.dp), NoticeTone.ERROR)
            if (pending) Group(rows = listOf({ GRow(copy(UiMessages.securityUnfinishedSecurityOperation()), chevron = true, onClick = onPending) }))
            Group(rows = listOf<@Composable () -> Unit>(
                { GRow(copy(UiMessages.securityCurrentManagingClient()), if (state.isAuthority) copy(UiMessages.securityThisDevice()) else uiText(state.authorityName)) },
                { GRow(copy(UiMessages.securityRemoteControlExpiry()), io.github.lewin671.remoteagent.core.securityTime(state.leaseEndMs, t, copy = copy)) },
            ) + if (state.isAuthority) listOf<@Composable () -> Unit>({ GRow(copy(UiMessages.securityRenewNow()), action = true, onClick = onRenew) }) else emptyList())
            Hint(copy(UiMessages.securityAndroidRenewsAutomaticallyWhenYouReturnToTheManagingClientAfter()), Modifier.padding(bottom = 8.dp))
            if (!state.isAuthority) Hint(copy(UiMessages.securityOpenToRenew(state.authorityName.resolve(copy))), Modifier.padding(bottom = 8.dp))
            if (state.isAuthority) Group(rows = listOf(
                { GRow(copy(UiMessages.securityRecoveryCode()), chevron = true, onClick = onRecovery) },
                { GRow(copy(UiMessages.securityTransferManagement()), chevron = true, onClick = onHandoff) },
            )) else if (state.devices.values.any { it.id == self && it.kind == io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_CLIENT })
                Group(rows = listOf({ GRow(copy(UiMessages.securityTakeOverManagement()), chevron = true, onClick = onHandoff) }))
        }
    }
}

@Composable
private fun rememberStandaloneOrExternalClient(
    app: RaApp,
    externalClient: SecurityClient?,
): SecurityClient {
    val ownHost = remember(app, externalClient) {
        if (externalClient == null) {
            StandaloneSecurityHost(
                store = app.vault,
                http = app.http,
                messageResolver = ::sessionText,
                allowPlainHttp = true,
                monotonic = android.os.SystemClock::elapsedRealtime,
            )
        } else {
            null
        }
    }
    DisposableEffect(ownHost) {
        onDispose { ownHost?.close() }
    }
    LaunchedEffect(ownHost) {
        if (ownHost != null) {
            ownHost.client.setForeground(true)
            runCatching {
                val st = ownHost.client.bootstrap()
                app.updateBootstrapStatus(st)
            }
        }
    }
    return externalClient ?: ownHost!!.client
}

/** 紧急恢复: the recovering client submits the old recovery code and shows the new one once. */
@Composable
fun RecoverScreen(app: RaApp, client: SecurityClient? = null, onBack: () -> Unit = {}) {
    val copy = uiText()
    val p = pal
    val scope = rememberAuthorityScope()
    val secClient = rememberStandaloneOrExternalClient(app, client)
    val secStatus by secClient.status.collectAsState()
    val pending = secStatus?.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_PENDING_RECOVERY

    var oldCode by remember { mutableStateOf("") }
    var server by remember {
        mutableStateOf(
            secStatus?.server?.takeIf { it.isNotEmpty() }
                ?: app.lastServer.ifEmpty { "https://" },
        )
    }
    var newRecoveryCode by remember { mutableStateOf("") }
    var codeSaved by remember { mutableStateOf(false) }
    var recoveryInfo by remember { mutableStateOf<SecurityRecoveryInfo?>(null) }
    var signatureMissing by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    val t = now()

    fun checkedServer(s: String) = Secret.checkServer(s, allowPlainHttp = app.debuggable)

    LaunchedEffect(secStatus?.bootstrap, secStatus?.pendingOp?.recoveryId) {
        val op = secStatus?.pendingOp
        if (pending && op != null) {
            if (op.codeSaved) codeSaved = true
            if (op.server.isNotEmpty()) {
                server = op.server
                app.lastServer = op.server
            }
            if (!op.signed && newRecoveryCode.isEmpty()) {
                signatureMissing = true
            }
            if (recoveryInfo == null && op.recoveryId.isNotEmpty()) {
                recoveryInfo = SecurityRecoveryInfo.newBuilder()
                    .setRecoveryId(op.recoveryId)
                    .setReadyMs(op.recoveryReadyMs)
                    .build()
            }
        }
    }

    PageScaffold(copy(UiMessages.securityRecoverManagementWithARecoveryCode()), onBack = onBack, wrapTitle = true) {
        LegacyRepairBanner(secStatus, {
            scope.launch {
                runCatching {
                    val next = secClient.dismissUpgradeNotice()
                    app.updateBootstrapStatus(next)
                }
            }
        })
        if (error.isNotEmpty() && !busy) DiscardSetup(app, onBack)
        FormColumn(
            Modifier.fillMaxWidth()
                .verticalScrollFaded()
                .imePadding(),
        ) {
            if (!pending) {
                Text(
                    copy(UiMessages.securityAndroidEnterTheRecoveryCodeSavedDuringSetupAfterSubmittingWait()),
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.padding(vertical = 10.dp),
                )
                if (error.isNotEmpty()) Notice(error, Modifier.padding(bottom = 10.dp), NoticeTone.ERROR)
                RecoveryField(copy(UiMessages.securityRecoveryCode()), oldCode, { oldCode = it }, "XXXX-XXXX-…", lines = 3)
                RecoveryField(copy(UiMessages.securityServer()), server, { server = it.trim() }, "https://")
                PillButton(
                    if (busy) copy(UiMessages.securityAndroidSubmitting()) else copy(UiMessages.securityAndroidSubmitRecovery()),
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                val url = checkedServer(server)
                                app.lastServer = url
                                val prep = secClient.prepareEmergencyRecovery(url, oldCode)
                                newRecoveryCode = prep.recoveryCode
                                recoveryInfo = prep.recovery
                                codeSaved = false
                                signatureMissing = false
                                oldCode = ""
                            } catch (e: Exception) {
                                error = displayError(e)
                            } finally {
                                busy = false
                            }
                        }
                    },
                    Modifier.fillMaxWidth().padding(top = 8.dp),
                    ButtonKind.INK,
                    enabled = !busy && oldCode.isNotBlank() && server.length > 8,
                )
                TextAction(
                    copy(UiMessages.commonCancel()),
                    {
                        app.lastServer = server
                        onBack()
                    },
                    alignment = Alignment.CenterStart,
                )
            } else {
                val info = recoveryInfo
                val rejected = recoveryNextAction(info?.phase) == RecoveryNextAction.RESTART
                if (rejected) {
                    Hint(copy(UiMessages.securityAndroidThisRecoveryWasVetoedTheNewRecoveryCodeGeneratedFor()), Modifier)
                } else if (newRecoveryCode.isNotEmpty()) {
                    GLabel(copy(UiMessages.securityNewRecoveryCodeShownOnlyOnce()))
                    RecoveryCodeFigure(newRecoveryCode)
                    Hint(copy(UiMessages.securityAndroidTakesEffectAfter48HoursAVetoFromTheOriginal()), Modifier)
                } else {
                    Hint(copy(UiMessages.securityAndroidThisRecoveryWasSubmittedIfYouHaveNotSavedThe()), Modifier)
                }

                if (signatureMissing && secStatus?.pendingOp?.signed == false) {
                    RecoveryField(copy(UiMessages.securityOriginalRecoveryCode()), oldCode, { oldCode = it }, "XXXX-XXXX-…", lines = 3)
                    PillButton(
                        copy(UiMessages.securityContinueOriginalDraft()),
                        {
                            busy = true
                            error = ""
                            scope.launch {
                                try {
                                    val next = secClient.continueRecoverySigning(oldCode)
                                    if (next != null) recoveryInfo = next
                                    signatureMissing = false
                                    oldCode = ""
                                } catch (e: Exception) {
                                    error = displayError(e)
                                } finally {
                                    busy = false
                                }
                            }
                        },
                        Modifier.fillMaxWidth().padding(top = 8.dp),
                        ButtonKind.SECONDARY,
                        enabled = !busy && oldCode.isNotBlank(),
                    )
                    TextAction(
                        copy(UiMessages.securityAbandonUnsignedDraft()),
                        {
                            busy = true
                            error = ""
                            scope.launch {
                                try {
                                    secClient.discardUnsignedRecovery()
                                    newRecoveryCode = ""
                                    codeSaved = false
                                    recoveryInfo = null
                                    signatureMissing = false
                                    oldCode = ""
                                } catch (e: Exception) {
                                    error = displayError(e)
                                } finally {
                                    busy = false
                                }
                            }
                        },
                        enabled = !busy,
                    )
                }

                if (!rejected && newRecoveryCode.isNotEmpty() && !codeSaved) {
                    PillButton(
                        copy(UiMessages.securityAndroidISavedTheNewRecoveryCodeOffline()),
                        {
                            busy = true
                            error = ""
                            scope.launch {
                                try {
                                    secClient.confirmRecoveryCodeSaved()
                                    codeSaved = true
                                } catch (e: Exception) {
                                    error = displayError(e)
                                } finally {
                                    busy = false
                                }
                            }
                        },
                        Modifier.fillMaxWidth().padding(top = 8.dp),
                        ButtonKind.SECONDARY,
                        enabled = !busy,
                    )
                }
                GLabel(copy(UiMessages.securityAndroidRecoveryCodeBackupStatus()))
                Hint(
                    if (codeSaved) copy(UiMessages.securityAndroidNewRecoveryCodeConfirmedSavedOffline())
                    else if (newRecoveryCode.isNotEmpty()) copy(UiMessages.securityAndroidTheNewCodeIsStillOnThisPageSaveIt())
                    else copy(UiMessages.securityAndroidBackupNotConfirmedThisPhoneHasNoRecoveryPrivateKey()),
                    Modifier,
                )
                GLabel(copy(UiMessages.securityAndroidWait()))
                val readyMs = info?.readyMs ?: secStatus?.pendingOp?.recoveryReadyMs ?: 0L
                Hint(
                    if (readyMs > 0L) copy(UiMessages.securityAndroidRecoveryEligibleAt(io.github.lewin671.remoteagent.core.securityTime(readyMs, t, copy = copy)))
                    else copy(UiMessages.securityAndroidCompletionRequires48Hours()),
                    Modifier,
                )
                RecoveryStatus(
                    phase = info?.phase,
                    busy = busy,
                    error = error,
                    onRefresh = {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                val next = secClient.resumeRecovery()
                                if (next != null) recoveryInfo = next else signatureMissing = true
                            } catch (e: Exception) {
                                error = displayError(e)
                            } finally {
                                busy = false
                            }
                        }
                    },
                    onComplete = {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                completeRecoveryAndAdopt(
                                    complete = {
                                        if (codeSaved) secClient.confirmRecoveryCodeSaved()
                                        secClient.completeRecovery().status
                                    },
                                    adopt = app::adoptBootstrappedAccount,
                                )
                            } catch (e: Exception) {
                                error = displayError(e)
                            } finally {
                                busy = false
                            }
                        }
                    },
                    onRestart = {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                restartRejectedRecovery(
                                    recoveryInfo?.phase,
                                    clearPending = { secClient.discardRejectedRecovery() },
                                    resetScreen = {
                                        oldCode = ""
                                        newRecoveryCode = ""
                                        codeSaved = false
                                        recoveryInfo = null
                                        signatureMissing = false
                                        error = ""
                                    },
                                )
                            } catch (e: Exception) {
                                error = displayError(e)
                            } finally {
                                busy = false
                            }
                        }
                    },
                )
            }
        }
    }
}

/** Status and actions share the same verified recovery phase. */
@Composable
internal fun RecoveryStatus(
    phase: GetRecoveryContextResponse.Phase?,
    busy: Boolean,
    error: String,
    onRefresh: () -> Unit,
    onComplete: () -> Unit,
    onRestart: () -> Unit,
) {
    val copy = uiText()
    TextAction(copy(UiMessages.securityAndroidViewStatus()), onRefresh, enabled = !busy, alignment = Alignment.CenterStart)
    when (phase) {
        GetRecoveryContextResponse.Phase.PHASE_READY -> Hint(copy(UiMessages.securityAndroidRecoveryCanNowBeCompleted()), Modifier)
        GetRecoveryContextResponse.Phase.PHASE_PENDING -> Hint(copy(UiMessages.securityAndroidStillWithinThe48HourWaitingPeriod()), Modifier)
        GetRecoveryContextResponse.Phase.PHASE_REJECTED -> Hint(copy(UiMessages.securityAndroidTheOriginalManagingClientVetoedThisRecovery()), Modifier)
        GetRecoveryContextResponse.Phase.PHASE_PUBLISHED -> Hint(copy(UiMessages.securityAndroidThisRecoveryIsComplete()), Modifier)
        else -> {}
    }
    if (error.isNotEmpty()) Notice(error, Modifier.padding(vertical = 8.dp), NoticeTone.ERROR)
    if (recoveryNextAction(phase) == RecoveryNextAction.COMPLETE) {
        PillButton(
            if (busy) copy(UiMessages.securityAndroidCompleting()) else copy(UiMessages.securityCompleteRecovery()),
            onComplete,
            Modifier.fillMaxWidth().padding(top = 10.dp),
            ButtonKind.INK,
            enabled = !busy,
        )
        Hint(copy(UiMessages.securityAndroidIf48HoursHaveNotPassedOrTheOriginalManaging()), Modifier.padding(top = 8.dp))
    } else {
        Hint(copy(UiMessages.securityAndroidTheNewRecoveryCodeGeneratedForThisRequestIsInvalid()), Modifier)
        PillButton(
            if (busy) copy(UiMessages.securityAndroidClearing()) else copy(UiMessages.securityAndroidAbandonThisRecoveryAndEnterANewCode()),
            onRestart,
            Modifier.fillMaxWidth().padding(top = 10.dp),
            ButtonKind.SECONDARY,
            enabled = !busy,
        )
    }
}

/** 新账户: create the root key, device identity and recovery code on this phone. */
@Composable
fun CreateAccountScreen(app: RaApp, client: SecurityClient? = null, onBack: () -> Unit = {}) {
    val copy = uiText()
    val p = pal
    val scope = rememberAuthorityScope()
    val secClient = rememberStandaloneOrExternalClient(app, client)
    val secStatus by secClient.status.collectAsState()
    val pending = secStatus?.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_PENDING_CREATE

    var server by remember {
        mutableStateOf(
            secStatus?.server?.takeIf { it.isNotEmpty() }
                ?: app.lastServer.ifEmpty { "https://" },
        )
    }
    var setupToken by remember { mutableStateOf("") }
    var recoveryCode by remember { mutableStateOf("") }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }

    fun checkedServer(s: String) = Secret.checkServer(s, allowPlainHttp = app.debuggable)

    PageScaffold(copy(UiMessages.securityCreateAccount()), onBack = onBack) {
        LegacyRepairBanner(secStatus, {
            scope.launch {
                runCatching {
                    val next = secClient.dismissUpgradeNotice()
                    app.updateBootstrapStatus(next)
                }
            }
        })
        if (error.isNotEmpty() && !busy) DiscardSetup(app, onBack)
        FormColumn(
            Modifier.fillMaxWidth()
                .verticalScrollFaded()
                .imePadding(),
        ) {
            if (!pending) {
                Text(
                    copy(UiMessages.securityAndroidThisPhoneWillBecomeTheManagingClientTheAccountRoot()),
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.padding(vertical = 10.dp),
                )
                if (error.isNotEmpty()) Notice(error, Modifier.padding(bottom = 10.dp), NoticeTone.ERROR)
                RecoveryField(copy(UiMessages.securityServer()), server, { server = it.trim() }, "https://")
                RecoveryField(copy(UiMessages.securityAndroidSetupTokenIfRequiredByTheServer()), setupToken, { setupToken = it }, copy(UiMessages.securityAndroidOptional()))
                PillButton(
                    if (busy) copy(UiMessages.scheduleCreating()) else copy(UiMessages.securityAndroidCreateAccount()),
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                val url = checkedServer(server)
                                app.lastServer = url
                                val prep = secClient.prepareCreation(url, storagePersisted = true)
                                recoveryCode = prep.recoveryCode
                            } catch (e: Exception) {
                                error = displayError(e)
                            } finally {
                                busy = false
                            }
                        }
                    },
                    Modifier.fillMaxWidth().padding(top = 8.dp),
                    ButtonKind.INK,
                    enabled = !busy && server.length > 8,
                )
            } else {
                if (recoveryCode.isNotEmpty()) {
                    GLabel(copy(UiMessages.securityAndroidRecoveryCodeShownOnlyOnce()))
                    RecoveryCodeFigure(recoveryCode)
                    Hint(copy(UiMessages.securityAndroidUseThisToRecoverManagementIfTheManagingClientIs()), Modifier)
                } else {
                    Hint(copy(UiMessages.securityTheOriginalCreationDraftIsPreservedContinueWithThe()), Modifier)
                    RecoveryField(copy(UiMessages.securityAndroidSetupTokenIfRequiredByTheServer()), setupToken, { setupToken = it }, copy(UiMessages.securityAndroidOptional()))
                }
                if (error.isNotEmpty()) {
                    Notice(error, Modifier.padding(vertical = 8.dp), NoticeTone.ERROR)
                }
                PillButton(
                    if (busy) copy(UiMessages.securityAndroidSaving()) else copy(UiMessages.securityAndroidISavedItStartUsingTheApp()),
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                if (recoveryCode.isNotEmpty()) {
                                    secClient.confirmCreationCodeSaved()
                                }
                                var res = secClient.continueCreation(setupToken.trim())
                                var st = res.status
                                if (recoveryCode.isEmpty() &&
                                    st.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_PENDING_CREATE &&
                                    !st.pendingOp.codeSaved
                                ) {
                                    res = secClient.adoptCreationWithoutCode()
                                    st = res.status
                                }
                                app.adoptBootstrappedAccount(st)
                            } catch (e: Exception) {
                                error = displayError(e)
                                busy = false
                            }
                        }
                    },
                    Modifier.fillMaxWidth().padding(top = 12.dp),
                    ButtonKind.INK,
                    enabled = !busy,
                )
            }
        }
    }
}

@Composable
private fun RecoveryField(
    label: String,
    value: String,
    onValue: (String) -> Unit,
    placeholder: String,
    lines: Int = 1,
) {
    val p = pal
    androidx.compose.foundation.text.BasicTextField(
        value,
        onValue,
        Modifier.fillMaxWidth().padding(bottom = 10.dp),
        textStyle = (if (lines > 1) T.mono12 else T.s15).copy(color = p.ink),
        singleLine = lines == 1,
        minLines = lines,
        keyboardOptions =
            KeyboardOptions(keyboardType = if (lines == 1) KeyboardType.Uri else KeyboardType.Text),
        cursorBrush = androidx.compose.ui.graphics.SolidColor(p.blue),
        decorationBox = { inner ->
            FieldBox {
                Column {
                    Text(
                        label,
                        style = T.s12,
                        color = p.ink2,
                        modifier = Modifier.padding(bottom = 2.dp),
                    )
                    androidx.compose.foundation.layout.Box {
                        if (value.isEmpty()) Text(placeholder, style = T.s15, color = p.ink2)
                        inner()
                    }
                }
            }
        },
    )
}

/** 更换恢复码: rotate on suspected leak, with the same save-before-publish order. */
@Composable
fun RotateRecoveryScreen(onBack: () -> Unit = {}) {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    RotateRecoveryContent(state, remote::prepareRecoveryRotation, remote::confirmRecoveryRotation, onBack)
}

@Composable
internal fun RotateRecoveryContent(
    state: io.github.lewin671.remoteagent.core.RemoteState,
    prepareCode: suspend () -> String,
    publishCode: suspend () -> Unit,
    onBack: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    val scope = rememberAuthorityScope()
    var prepared by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    PageScaffold(copy(UiMessages.securityRecoveryCode()), onBack = onBack) {
        FormColumn(
            Modifier.fillMaxWidth()
                .verticalScrollFaded()
                .imePadding(),
        ) {
            val codeValue = prepared
            if (codeValue == null) {
                Text(
                    copy(UiMessages.securityReplacingTheRecoveryCodeImmediatelyInvalidatesTheOldCode()),
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.padding(vertical = 10.dp),
                )
                if (error.isNotEmpty()) {
                    Notice(error, Modifier.padding(vertical = 8.dp), NoticeTone.ERROR)
                }
                PillButton(
                    if (busy) copy(UiMessages.securityAndroidGenerating()) else copy(UiMessages.securityGenerateNewRecoveryCode()),
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                prepared = prepareCode()
                            } catch (e: Exception) {
                                error = displayError(e, state)
                            } finally {
                                busy = false
                            }
                        }
                    },
                    Modifier.fillMaxWidth().padding(top = 8.dp),
                    ButtonKind.INK,
                    enabled = !busy,
                )
            } else {
                GLabel(copy(UiMessages.securityNewRecoveryCodeShownOnlyOnce()))
                RecoveryCodeFigure(codeValue)
                Hint(copy(UiMessages.securityAndroidWriteItDownOrPhotographItBeforePublishingPublishingImmediately()), Modifier)
                if (error.isNotEmpty()) {
                    Notice(error, Modifier.padding(vertical = 8.dp), NoticeTone.ERROR)
                }
                PillButton(
                    if (busy) copy(UiMessages.securityAndroidPublishing()) else copy(UiMessages.securitySavedPublishReplacement()),
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                publishCode()
                                onBack()
                            } catch (e: Exception) {
                                error = displayError(e, state)
                            } finally {
                                busy = false
                            }
                        }
                    },
                    Modifier.fillMaxWidth().padding(top = 12.dp),
                    ButtonKind.INK,
                    enabled = !busy,
                )
                PillButton(
                    copy(UiMessages.securityAndroidDoNotPublishYet()),
                    { prepared = null },
                    Modifier.fillMaxWidth().padding(top = 6.dp),
                    ButtonKind.SECONDARY,
                    enabled = !busy,
                )
            }
        }
    }
}
