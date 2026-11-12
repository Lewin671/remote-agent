package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
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
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.CreatedAccount
import io.github.lewin671.remoteagent.core.DeviceSecurity
import io.github.lewin671.remoteagent.core.PendingRecoveryInfo
import io.github.lewin671.remoteagent.core.PreparedRecovery
import io.github.lewin671.remoteagent.core.Recovery
import io.github.lewin671.remoteagent.core.RecoveryChain
import io.github.lewin671.remoteagent.core.RecoveryCode
import io.github.lewin671.remoteagent.core.RecoverySubmission
import io.github.lewin671.remoteagent.core.Secret
import io.github.lewin671.remoteagent.core.SigningKey
import io.github.lewin671.remoteagent.core.createAccount
import io.github.lewin671.remoteagent.core.dayText
import io.github.lewin671.remoteagent.core.deviceId
import io.github.lewin671.remoteagent.ui.displayError
import io.github.lewin671.remoteagent.core.randomBytes
import io.github.lewin671.remoteagent.core.rebuildSecurityDomain
import io.github.lewin671.remoteagent.proto.GetRecoveryContextResponse
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * 管理端 · 恢复: a pending recovery can be vetoed here. The new code is shown and saved first; the
 * rotated state is published only after the user confirms.
 */
@Composable
fun RecoveryScreen(onBack: () -> Unit = {}) {
    val copy = uiText()
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val scope = rememberAuthorityScope()
    var page by remember { mutableStateOf("overview") }
    var pending by remember { mutableStateOf<io.github.lewin671.remoteagent.core.SecurityOperation?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    suspend fun read() {
        try { pending = withContext(Dispatchers.IO) { RaApp.instance.vault.readSecurityOperation() }; error = "" }
        catch (e: Exception) { error = displayError(e, state) }
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
                            pending?.delivery?.takeIf { it.isNotEmpty() }?.let {
                                remote.finishPairingOperation(io.github.lewin671.remoteagent.proto.ApprovePairingRequest.parseFrom(it).pairingId)
                            }
                            read()
                            if (pending == null) page = "overview"
                        } catch (e: Exception) { error = displayError(e, state) }
                        finally { busy = false }
                    }
                }, enabled = !busy)
                if (error.isNotEmpty()) Notice(error, tone = NoticeTone.ERROR)
            }
        }
        else -> AccountSecurityContent(state, pending != null, { page = "pending" }, { page = "recovery" }, { page = "handoff" }, {
            scope.launch { remote.renewLeaseNow(); read() }
        }, error = error)
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

/** 紧急恢复: the recovering client submits the old recovery code and shows the new one once. */
@Composable
fun RecoverScreen(app: RaApp, onBack: () -> Unit = {}) {
    val copy = uiText()
    val p = pal
    val scope = rememberAuthorityScope()
    var deviceSeed by remember { mutableStateOf(randomBytes(32)) }
    var code by remember { mutableStateOf("") }
    var server by remember { mutableStateOf(app.lastServer.ifEmpty { "https://" }) }
    var submission by remember { mutableStateOf<RecoverySubmission?>(null) }
    var codeSaved by remember { mutableStateOf(false) }
    var chain by remember { mutableStateOf<RecoveryChain?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    val t = now()

    fun checkedServer(s: String) = Secret.checkServer(s, allowPlainHttp = app.debuggable)
    AuthorityEffect(Unit) {
        val saved = withContext(Dispatchers.IO) { app.vault.loadRecoveryPublication() }
        codeSaved = app.vault.readSecurityOperation()?.codeSaved == true
        if (saved != null) {
            busy = true
            try {
                server = checkedServer(saved.server)
                app.lastServer = server
                submission =
                    withContext(Dispatchers.IO) {
                        Recovery.resume(
                            app.http,
                            saved,
                            app.vault::saveRecoveryPublication,
                        )
                    }
            } catch (e: Exception) {
                error = displayError(e)
            } finally {
                busy = false
            }
        }
    }

    PageScaffold(copy(UiMessages.securityRecoverManagementWithARecoveryCode()), onBack = onBack) {
        if (error.isNotEmpty() && !busy) DiscardSetup(app, onBack)
        FormColumn(
            Modifier.fillMaxWidth()
            .verticalScrollFaded()
            .imePadding(),
        ) {
            val s = submission
            if (s == null) {
                Text(
                    copy(UiMessages.securityAndroidEnterTheRecoveryCodeSavedDuringSetupAfterSubmittingWait()),
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.padding(vertical = 10.dp),
                )
                if (error.isNotEmpty()) Notice(error, Modifier.padding(bottom = 10.dp), NoticeTone.ERROR)
                RecoveryField(copy(UiMessages.securityRecoveryCode()), code, { code = it }, "XXXX-XXXX-…", lines = 3)
                RecoveryField(copy(UiMessages.securityServer()), server, { server = it.trim() }, "https://")
                PillButton(
                    if (busy) copy(UiMessages.securityAndroidSubmitting()) else copy(UiMessages.securityAndroidSubmitRecovery()),
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                val decoded =
                                    RecoveryCode.decode(code)
                                        ?: throw IllegalArgumentException(copy(UiMessages.securityAndroidIncorrectRecoveryCode()))
                                val seed = decoded.seed
                                val url = checkedServer(server)
                                app.lastServer = url
                                val recoveryEd = SigningKey(seed).publicKey
                                val draft =
                                    app.vault.loadRecoveryPublication()?.takeIf {
                                        it.submission.genesisPrefix.contentEquals(decoded.genesis)
                                    }
                                val existing =
                                    withContext(Dispatchers.IO) {
                                        val saved = draft?.submission
                                        if (saved != null && saved.recoveryId.isNotEmpty())
                                            Recovery.context(
                                                url,
                                                app.http,
                                                ByteArray(0),
                                                saved.deviceSeed,
                                                recoveryId = saved.recoveryId,
                                                expectedGenesis = decoded.genesis,
                                            )
                                        else
                                            Recovery.context(
                                                url,
                                                app.http,
                                                recoveryEd,
                                                seed,
                                                expectedGenesis = decoded.genesis,
                                            )
                                    }
                                submission =
                                    if (
                                        existing.phase ==
                                            GetRecoveryContextResponse.Phase.PHASE_PENDING ||
                                            existing.phase ==
                                                GetRecoveryContextResponse.Phase.PHASE_READY ||
                                            existing.phase ==
                                                GetRecoveryContextResponse.Phase.PHASE_PUBLISHED
                                    ) {
                                        val saved =
                                            app.vault
                                                .loadRecoveryPublication()
                                                ?.submission
                                                ?.takeIf {
                                                    it.accountId == existing.accountId &&
                                                        it.genesisPrefix.contentEquals(
                                                            decoded.genesis,
                                                        )
                                                }
                                        if (saved == null)
                                            throw IllegalStateException(copy(UiMessages.securityAndroidThisRecoveryWasSubmittedCompleteItOnThePhoneThat()))
                                        RecoverySubmission(
                                            existing.recoveryId,
                                            existing.readyMs,
                                            "",
                                            saved.deviceSeed,
                                            deviceId(SigningKey(saved.deviceSeed).publicKey),
                                            saved.xPrivate,
                                            existing.accountId,
                                            existing.accountPublicKey,
                                            saved.stateHash,
                                            existing.pin.genesis.copyOf(10),
                                        )
                                    } else {
                                        val r =
                                            withContext(Dispatchers.IO) {
                                                Recovery.submit(
                                                    url,
                                                    app.http,
                                                    seed,
                                                    deviceSeed,
                                                    decoded.genesis,
                                                    floor = app.vault.signingTime().toULong(),
                                                    draft =
                                                        draft?.takeIf {
                                                            DeviceSecurity.parseRecoveryStatement(
                                                                    it.statement,
                                                                )
                                                                .parent
                                                                .contentEquals(
                                                                    DeviceSecurity.stateHash(
                                                                        existing.head,
                                                                    ),
                                                                )
                                                        },
                                                    persist = app.vault::saveRecoveryPublication,
                                                )
                                            }
                                        r
                                    }
                                submission?.let { current ->
                                    draft
                                        ?.takeIf {
                                            it.submission.stateHash.contentEquals(current.stateHash)
                                        }
                                        ?.let {
                                            app.vault.saveRecoveryPublication(
                                                io.github.lewin671.remoteagent.core
                                                    .RecoveryPublication(
                                                        it.server,
                                                        current,
                                                        it.statement,
                                                        it.state,
                                                    ),
                                            )
                                        }
                                }
                                app.lastServer = url
                            } catch (e: Exception) {
                                error = displayError(e)
                            } finally {
                                busy = false
                            }
                        }
                    },
                    Modifier.fillMaxWidth().padding(top = 8.dp),
                    ButtonKind.INK,
                    enabled = !busy && code.isNotBlank() && server.length > 8,
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
                val rejected = recoveryNextAction(chain?.phase) == RecoveryNextAction.RESTART
                if (rejected) {
                    Hint(copy(UiMessages.securityAndroidThisRecoveryWasVetoedTheNewRecoveryCodeGeneratedFor()), Modifier)
                } else if (s.newCode.isNotEmpty()) {
                    GLabel(copy(UiMessages.securityNewRecoveryCodeShownOnlyOnce()))
                    RecoveryCodeFigure(s.newCode)
                    Hint(copy(UiMessages.securityAndroidTakesEffectAfter48HoursAVetoFromTheOriginal()), Modifier)
                } else {
                    Hint(copy(UiMessages.securityAndroidThisRecoveryWasSubmittedIfYouHaveNotSavedThe()), Modifier)
                }
                val computers = chain?.head?.core?.members?.filter { it.kind == 1u }.orEmpty()
                if (!rejected && s.newCode.isNotEmpty() && !codeSaved)
                    PillButton(
                        copy(UiMessages.securityAndroidISavedTheNewRecoveryCodeOffline()),
                        {
                            try {
                                app.vault.confirmRecoveryCodeSaved()
                                codeSaved = true
                            } catch (e: Exception) {
                                error = displayError(e)
                            }
                        },
                        Modifier.fillMaxWidth().padding(top = 8.dp),
                        ButtonKind.SECONDARY,
                    )
                GLabel(copy(UiMessages.securityAndroidRecoveryCodeBackupStatus()))
                Hint(
                    if (codeSaved) copy(UiMessages.securityAndroidNewRecoveryCodeConfirmedSavedOffline())
                    else if (s.newCode.isNotEmpty()) copy(UiMessages.securityAndroidTheNewCodeIsStillOnThisPageSaveIt())
                    else copy(UiMessages.securityAndroidBackupNotConfirmedThisPhoneHasNoRecoveryPrivateKey()),
                    Modifier,
                )
                GLabel(copy(UiMessages.securityAndroidWaitingForComputersToRestoreCurrentSessions()))
                Hint(
                    if (computers.isEmpty()) copy(UiMessages.securityAndroidAfterCompletionWaitForRetainedComputersToComeOnlineAnd())
                    else
                        computers.joinToString("、") { it.id.take(8) } +
                            copy(UiMessages.securityAndroidTheseComputersMustRepublishCheckpointsAfterRecovery()),
                    Modifier,
                )
                GLabel(copy(UiMessages.securityAndroidWait()))
                Hint(
                    if (s.readyMs > 0) copy(UiMessages.securityAndroidRecoveryEligibleAt(io.github.lewin671.remoteagent.core.securityTime(s.readyMs, t, copy = copy))) else copy(UiMessages.securityAndroidCompletionRequires48Hours()),
                    Modifier,
                )
                RecoveryStatus(
                    phase = chain?.phase,
                    busy = busy,
                    error = error,
                    onRefresh = {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                chain =
                                    withContext(Dispatchers.IO) {
                                        Recovery.context(
                                            checkedServer(server),
                                            app.http,
                                            ByteArray(0),
                                            s.deviceSeed,
                                            recoveryId = s.recoveryId,
                                            expectedGenesis = s.genesisPrefix,
                                        )
                                    }
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
                                val url = checkedServer(server)
                                app.lastServer = url
                                completeRecoveryAndAdopt(
                                    complete = {
                                        withContext(Dispatchers.IO) {
                                            Recovery.complete(
                                                url,
                                                app.http,
                                                s.recoveryId,
                                                s.accountId,
                                                s.deviceSeed,
                                                s.xPrivate,
                                                s.stateHash,
                                            )
                                        }
                                    },
                                    adopt = app::recovered,
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
                                    chain?.phase,
                                    clearPending = {
                                        withContext(Dispatchers.IO) {
                                            app.vault.clearPendingRecovery()
                                        }
                                    },
                                    resetScreen = {
                                        submission = null
                                        code = ""
                                        codeSaved = false
                                        chain = null
                                        error = ""
                                        deviceSeed = randomBytes(32)
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
fun CreateAccountScreen(app: RaApp, onBack: () -> Unit = {}) {
    val copy = uiText()
    val p = pal
    val scope = rememberAuthorityScope()
    var server by remember { mutableStateOf(app.lastServer.ifEmpty { "https://" }) }
    var setupToken by remember { mutableStateOf("") }
    var created by remember { mutableStateOf<CreatedAccount?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }

    fun checkedServer(s: String) = Secret.checkServer(s, allowPlainHttp = app.debuggable)

    PageScaffold(copy(UiMessages.securityCreateAccount()), onBack = onBack) {
        if (error.isNotEmpty() && !busy) DiscardSetup(app, onBack)
        FormColumn(
            Modifier.fillMaxWidth()
            .verticalScrollFaded()
            .imePadding(),
        ) {
            val c = created
            if (c == null) {
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
                                created =
                                    withContext(Dispatchers.IO) {
                                        createAccount(
                                            url,
                                            app.http,
                                            setupToken.trim(),
                                            now = app.vault.signingTime(),
                                            draft = app.vault.loadCreationOperation(),
                                            persistDraft = app.vault::saveCreationOperation,
                                        )
                                    }
                                app.lastServer = url
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
                GLabel(copy(UiMessages.securityAndroidRecoveryCodeShownOnlyOnce()))
                RecoveryCodeFigure(c.recoveryCode)
                Hint(copy(UiMessages.securityAndroidUseThisToRecoverManagementIfTheManagingClientIs()), Modifier)
                if (error.isNotEmpty())
                    Notice(error, Modifier.padding(vertical = 8.dp), NoticeTone.ERROR)
                PillButton(
                    copy(UiMessages.securityAndroidISavedItStartUsingTheApp()),
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                app.created(c.account, c.record)
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

/**
 * 已有账户升级到协议 10: rebuild the domain locally and show the recovery code before the account is
 * adopted. An upgraded relay pins offline instead.
 */
@Composable
fun RebuildSecurityScreen(app: RaApp, existing: Account?, onBack: () -> Unit = {}) {
    val copy = uiText()
    val p = pal
    val ctx = androidx.compose.ui.platform.LocalContext.current
    val scope = rememberAuthorityScope()
    var created by remember { mutableStateOf<CreatedAccount?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    LaunchedEffect(existing) {
        val saved = withContext(Dispatchers.IO) { app.vault.loadCreationOperation() }
        if (
            saved != null &&
                existing != null &&
                saved.account.deviceId == existing.deviceId &&
                saved.account.server == existing.server
        )
            created = saved.copy(needsOfflinePin = true)
    }
    PageScaffold(copy(UiMessages.securityAndroidRebuildSecurityDomain()), onBack = onBack) {
        if (error.isNotEmpty() && !busy) DiscardSetup(app, onBack)
        FormColumn(
            Modifier.fillMaxWidth()
            .verticalScrollFaded()
            .imePadding(),
        ) {
            val c = created
            if (c == null) {
                Text(
                    copy(UiMessages.securityAndroidKeepThisPhoneSAccountRootKeyAndDeviceIdentity()),
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.padding(vertical = 10.dp),
                )
                if (existing == null || !existing.hasRoot)
                    Hint(copy(UiMessages.securityAndroidThisPhoneHasNoAccountRootKeyAndCannotBecome()), Modifier)
                if (error.isNotEmpty())
                    Notice(error, Modifier.padding(vertical = 8.dp), NoticeTone.ERROR)
                PillButton(
                    if (busy) copy(UiMessages.securityAndroidRebuilding()) else copy(UiMessages.securityAndroidRebuildSecurityDomainAndBecomeManagingClient()),
                    {
                        if (existing == null) {
                            error = copy(UiMessages.securityAndroidNoAccountAvailableToUpgrade())
                            return@PillButton
                        }
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                created =
                                    withContext(Dispatchers.IO) {
                                        rebuildSecurityDomain(
                                            existing.server,
                                            app.http,
                                            existing,
                                            now = app.vault.signingTime(),
                                            draft = app.vault.loadCreationOperation(),
                                            persistDraft = app.vault::saveCreationOperation,
                                        )
                                    }
                            } catch (e: Exception) {
                                error = displayError(e)
                            } finally {
                                busy = false
                            }
                        }
                    },
                    Modifier.fillMaxWidth().padding(top = 10.dp),
                    ButtonKind.INK,
                    enabled = !busy && existing != null && existing.hasRoot,
                )
            } else {
                GLabel(copy(UiMessages.securityNewRecoveryCodeShownOnlyOnce()))
                if (c.recoveryCode.isNotEmpty()) RecoveryCodeFigure(c.recoveryCode)
                else Hint(copy(UiMessages.securityAndroidThisIsASavedRebuildDraftTheRecoveryCodeWill()), Modifier)
                Hint(copy(UiMessages.securityAndroidWriteItDownOrPhotographItItOnlySignsRecovery()), Modifier)
                if (c.needsOfflinePin)
                    Notice(
                        copy(UiMessages.securityAndroidTheRelayHasNotPinnedThisSecurityDomainAnUpgrade()),
                        Modifier.padding(top = 10.dp),
                        NoticeTone.WARN,
                    )
                if (c.needsOfflinePin)
                    TextAction(
                        copy(UiMessages.securityAndroidExportGenesisStateForOfflineRelayPinning()),
                        { shareGenesis(ctx, DeviceSecurity.stateBytes(c.record.chain.first()), copy = copy) },
                        alignment = Alignment.CenterStart,
                    )
                if (error.isNotEmpty()) Notice(error, Modifier.padding(top = 8.dp), NoticeTone.ERROR)
                PillButton(
                    if (busy) copy(UiMessages.securityAndroidSaving()) else copy(UiMessages.securityAndroidISavedItStartUsingTheApp()),
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                app.created(c.account, c.record)
                            } catch (e: Exception) {
                                error = displayError(e)
                            } finally {
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

/** 更换恢复码: rotate on suspected leak, with the same save-before-publish order. */
@Composable
fun RotateRecoveryScreen(onBack: () -> Unit = {}) {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    RotateRecoveryContent(state, remote::prepareRecovery, remote::publishRecovery, onBack)
}

@Composable
internal fun RotateRecoveryContent(
    state: io.github.lewin671.remoteagent.core.RemoteState,
    prepareCode: suspend () -> PreparedRecovery,
    publishCode: suspend (PreparedRecovery) -> Unit,
    onBack: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    val scope = rememberAuthorityScope()
    var prepared by remember { mutableStateOf<PreparedRecovery?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    PageScaffold(copy(UiMessages.securityRecoveryCode()), onBack = onBack) {
        FormColumn(
            Modifier.fillMaxWidth()
            .verticalScrollFaded()
            .imePadding(),
        ) {
            val pv = prepared
            if (pv == null) {
                Text(
                    copy(UiMessages.securityReplacingTheRecoveryCodeImmediatelyInvalidatesTheOldCode()),
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.padding(vertical = 10.dp),
                )
                if (error.isNotEmpty())
                    Notice(error, Modifier.padding(vertical = 8.dp), NoticeTone.ERROR)
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
                RecoveryCodeFigure(pv.code)
                Hint(copy(UiMessages.securityAndroidWriteItDownOrPhotographItBeforePublishingPublishingImmediately()), Modifier)
                if (error.isNotEmpty())
                    Notice(error, Modifier.padding(vertical = 8.dp), NoticeTone.ERROR)
                PillButton(
                    if (busy) copy(UiMessages.securityAndroidPublishing()) else copy(UiMessages.securitySavedPublishReplacement()),
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                publishCode(pv)
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
