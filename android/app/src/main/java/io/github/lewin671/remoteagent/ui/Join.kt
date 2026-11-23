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
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.PairLink
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.Secret
import io.github.lewin671.remoteagent.core.SecurityClient
import io.github.lewin671.remoteagent.core.StandaloneSecurityHost
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.SecurityBootstrapKind
import io.github.lewin671.remoteagent.core.proto.SecurityExitAdvice
import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowStatus
import io.github.lewin671.remoteagent.core.translateSecurityKey
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

private sealed interface JoinStep {
    data object Form : JoinStep

    data object Scan : JoinStep

    data class Emoji(val emoji: List<String>, val pairingId: String) : JoinStep

    data object Working : JoinStep
}

/** First run: join the account with a pairing code or scanned QR code, or create/recover. */
/** The server turned this device away: nothing of the account is shown until it joins again. */
@Composable
fun RemovedScreen(app: RaApp, remote: Remote) {
    val copy = uiText()
    val p = pal
    Column(
        Modifier.fillMaxSize().background(p.paper).padding(horizontal = 24.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text(copy(UiMessages.securityAndroidTheServerRejectedThisDeviceSLogin()), style = T.s17b, color = p.ink, textAlign = TextAlign.Center)
        Text(
            copy(UiMessages.settingsTheDeviceMayHaveBeenRemovedTheServiceReset()),
            style = T.s13,
            color = p.ink2,
            textAlign = TextAlign.Center,
            modifier = Modifier.padding(top = 8.dp, bottom = 20.dp),
        )
        RejoinButton(app, copy(UiMessages.settingsRejoinAccount()), readAdvice = { remote.checkExitAdvice() })
        androidx.compose.runtime.key(remote) {
            PendingSecurityOperation(
                readPending = { remote.getSecurityStatus().hasPendingOp() },
                resume = { remote.resumeSecurityOperation() },
            )
        }
        Text(
            copy(UiMessages.securityAndroidFirstDeleteTheAccountKeysAndCacheOnThisDevice()),
            style = T.s13,
            color = p.ink2,
            textAlign = TextAlign.Center,
            modifier = Modifier.padding(top = 10.dp, bottom = 4.dp),
        )
        TextAction(
            copy(UiMessages.sessionTryAgain()),
            { remote.retryLogin() },
            alignment = Alignment.Center,
        )
    }
}

/** No action until a background check confirms there is work to resume. */
@Composable
internal fun PendingSecurityOperation(readPending: suspend () -> Boolean, resume: suspend () -> Unit) {
    val copy = uiText()
    val scope = rememberCoroutineScope()
    var pending by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var result by remember { mutableStateOf("") }
    LaunchedEffect(Unit) {
        try { pending = withContext(Dispatchers.IO) { readPending() } }
        catch (e: kotlinx.coroutines.CancellationException) { throw e }
        catch (e: Exception) { result = displayError(e) }
    }
    if (pending) {
        TextAction(copy(UiMessages.securityAndroidVerifyOriginalSecurityOperation()), {
            if (!busy) {
                busy = true
                scope.launch {
                    try {
                        result = ""
                        pending = withContext(Dispatchers.IO) { readPending() }
                        if (pending) {
                            resume()
                            pending = withContext(Dispatchers.IO) { readPending() }
                            result = if (pending) copy(UiMessages.settingsTheOriginalOperationIsUnfinishedContinueVerifying()) else copy(UiMessages.securityAndroidTheOriginalSecurityOperationIsVerifiedComplete())
                        }
                    } catch (e: kotlinx.coroutines.CancellationException) { throw e }
                    catch (e: Exception) { result = displayError(e) }
                    finally { busy = false }
                }
            }
        }, enabled = !busy)
    }
    if (result.isNotEmpty()) Hint(result)
}

@Composable
internal fun LegacyRepairBanner(
    status: SecurityWorkflowStatus?,
    onDismiss: () -> Unit,
    modifier: Modifier = Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
) {
    if (status?.legacyRepairNotice != true) return
    val copy = uiText()
    Notice(
        copy(UiMessages.securityUpgradeRepairTitle()) + "\n" + copy(UiMessages.securityUpgradeRepairNotice()),
        modifier,
        NoticeTone.WARN,
        action = copy(UiMessages.commonGotIt()),
        onAction = onDismiss,
    )
}

@Composable
fun JoinScreen(app: RaApp) {
    val generation by app.joinGeneration.collectAsState()
    androidx.compose.runtime.key(generation) { JoinContent(app) }
}

@Composable
internal fun JoinContent(
    app: RaApp,
    bootstrapOverride: SecurityWorkflowStatus? = null,
    resumeOverride: (suspend () -> Unit)? = null,
) {
    val copy = uiText()
    val p = pal
    val scope = rememberCoroutineScope()
    val appBootstrapStatus by app.bootstrapStatus.collectAsState()
    val retainedDenied by app.retainedDenied.collectAsState()

    val secHost = remember(app) {
        StandaloneSecurityHost(
            store = app.vault,
            http = app.http,
            messageResolver = ::sessionText,
            allowPlainHttp = true,
            monotonic = android.os.SystemClock::elapsedRealtime,
        )
    }
    DisposableEffect(secHost) {
        onDispose { secHost.close() }
    }
    LaunchedEffect(secHost, bootstrapOverride) {
        if (bootstrapOverride != null) return@LaunchedEffect
        secHost.client.setForeground(true)
        runCatching {
            val st = secHost.client.bootstrap()
            app.updateBootstrapStatus(st)
        }
    }

    val liveStatus by secHost.client.status.collectAsState()
    val status = bootstrapOverride ?: liveStatus ?: appBootstrapStatus
    val existingRepair = status != null &&
        status.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_EMPTY &&
        status.deviceId.isNotEmpty() &&
        status.server.isNotEmpty()
    val pendingCreate = status?.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_PENDING_CREATE
    val pendingRecovery = status?.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_PENDING_RECOVERY
    val pendingRegistration = status?.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_PENDING_REGISTRATION

    var step by remember { mutableStateOf<JoinStep>(JoinStep.Form) }
    var code by remember { mutableStateOf("") }
    var server by rememberSaveable {
        mutableStateOf(
            status?.server?.takeIf { it.isNotEmpty() }
                ?: app.lastServer.ifEmpty { "https://" },
        )
    }
    LaunchedEffect(status?.server) {
        val srv = status?.server.orEmpty()
        if (srv.isNotEmpty() && (server == "https://" || existingRepair)) {
            server = srv
        }
    }
    var commit by remember { mutableStateOf<ByteArray?>(null) }
    var error by remember { mutableStateOf("") }
    var job by remember { mutableStateOf<Job?>(null) }
    var stopped by remember { mutableStateOf(false) }
    var comparisonPending by remember { mutableStateOf(false) }
    var registering by remember { mutableStateOf(false) }
    var activePairingId by remember { mutableStateOf<String?>(null) }
    var mode by remember {
        mutableStateOf(
            when {
                pendingCreate -> "create"
                pendingRecovery -> "recover"
                pendingRegistration || existingRepair || app.joinDirectly -> "join"
                else -> "entry"
            },
        )
    }
    // Keep the active onboarding screen mounted once a pending create/recovery/registration
    // transitions to READY so account adoption finishes without dropping back to "entry".
    LaunchedEffect(pendingCreate, pendingRecovery, pendingRegistration, existingRepair) {
        when {
            pendingCreate -> mode = "create"
            pendingRecovery -> mode = "recover"
            pendingRegistration || existingRepair -> mode = "join"
        }
    }

    fun checkedServer(s: String) = Secret.checkServer(s, allowPlainHttp = app.debuggable)

    fun dismissUpgradeNotice() {
        scope.launch {
            runCatching {
                val next = secHost.client.dismissUpgradeNotice()
                app.updateBootstrapStatus(next)
            }
        }
    }

    fun join(link: PairLink) {
        error = ""
        stopped = false
        comparisonPending = false
        registering = false
        step = JoinStep.Working
        job = scope.launch {
            try {
                val url = checkedServer(link.server)
                val info = secHost.client.startJoinPairing(
                    server = url,
                    code = link.code.replace(" ", ""),
                    commitHint = link.commit,
                    existingRepair = existingRepair,
                    existingServer = status?.server.orEmpty(),
                )
                if (stopped) {
                    if (info.pairingId.isNotEmpty()) {
                        runCatching { secHost.client.cancelJoinPairing(info.pairingId) }
                    }
                    return@launch
                }
                activePairingId = info.pairingId
                comparisonPending = true
                step = JoinStep.Emoji(info.sasEmojiList.toList(), info.pairingId)
            } catch (e: kotlinx.coroutines.CancellationException) {
                throw e
            } catch (e: Exception) {
                if (!stopped) error = displayError(e)
                registering = false
                comparisonPending = false
                activePairingId = null
                step = JoinStep.Form
            }
        }
    }

    fun finishRegistrationError(e: Exception) {
        error = if (registering) {
            copy(UiMessages.securityAndroidCannotConfirmWhetherThisPhoneJoinedValue1CheckTheClient(displayError(e)))
        } else if (!stopped) {
            displayError(e)
        } else {
            error
        }
        registering = false
        activePairingId = null
        step = JoinStep.Form
    }

    fun resumeRegistration() {
        error = ""
        stopped = false
        comparisonPending = false
        registering = true
        step = JoinStep.Working
        job = scope.launch {
            try {
                if (resumeOverride != null) {
                    resumeOverride()
                } else {
                    val res = secHost.client.resumePendingOp()
                    activePairingId = null
                    withContext(NonCancellable) { app.adoptBootstrappedAccount(res.status) }
                }
            } catch (e: kotlinx.coroutines.CancellationException) {
                throw e
            } catch (e: Exception) {
                finishRegistrationError(e)
            }
        }
    }

    fun confirmSas(pairingId: String) {
        comparisonPending = false
        registering = true
        job = scope.launch {
            try {
                val res = secHost.client.confirmJoinSas(pairingId)
                activePairingId = null
                withContext(NonCancellable) { app.adoptBootstrappedAccount(res.status) }
            } catch (e: kotlinx.coroutines.CancellationException) {
                throw e
            } catch (e: Exception) {
                finishRegistrationError(e)
            }
        }
    }

    fun cancel() {
        if (registering) {
            error = copy(UiMessages.securityAndroidAlreadyRegisteringThisPhoneWaitForTheResult())
            return
        }
        stopped = true
        comparisonPending = false
        val pid = activePairingId
        activePairingId = null
        job?.cancel()
        if (!pid.isNullOrEmpty()) {
            scope.launch { runCatching { secHost.client.cancelJoinPairing(pid) } }
        }
        code = ""
        commit = null
        error = copy(UiMessages.securityAndroidThisJoinAttemptWasCancelledTheOldRequestMayStill())
        step = JoinStep.Form
    }

    if (existingRepair && retainedDenied) {
        Column(
            Modifier.fillMaxSize().background(p.paper).padding(24.dp),
            verticalArrangement = Arrangement.Center,
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            LegacyRepairBanner(status, ::dismissUpgradeNotice)
            Text(copy(UiMessages.securityTheServerDoesNotRecognizeThisDevice()), style = T.s17b, color = p.ink)
            Hint(copy(UiMessages.securityItMayHaveBeenRemovedOrTheServiceMay()), inset = 0.dp)
            RejoinButton(
                app,
                copy(UiMessages.securityRejoinWithAPairingCode()),
                readAdvice = { secHost.client.checkExitAdvice() },
            )
            Hint(copy(UiMessages.securityAndroidFirstDeleteTheAccountKeysAndCacheOnThisPhone()), inset = 0.dp)
        }
        return
    }

    if (mode == "entry") {
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
                LegacyRepairBanner(status, ::dismissUpgradeNotice)
                Column(Modifier.fillMaxWidth().padding(top = 28.dp)) {
                    Group(rows = listOf(
                        { EntryRow(copy(UiMessages.securityCreateAccount()), copy(UiMessages.securityThisDeviceBecomesTheManagingClient())) { mode = "create" } },
                        { EntryRow(copy(UiMessages.securityJoinAnExistingAccount()), copy(UiMessages.securityJoinWithACodeFromTheManagingClient())) { mode = "join" } },
                        { EntryRow(copy(UiMessages.securityRecoverManagementWithARecoveryCode()), copy(UiMessages.securityUseWhenTheManagingClientIsLost())) { mode = "recover" } },
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
    BackHandler(enabled = !existingRepair && mode == "join" && step == JoinStep.Form) { mode = "entry" }

    if (mode == "create") {
        CreateAccountScreen(app, secHost.client) { mode = if (!existingRepair) "entry" else "join" }
        return
    }
    if (mode == "recover") {
        RecoverScreen(app, secHost.client) { mode = if (!existingRepair) "entry" else "join" }
        return
    }

    BackHandler(enabled = step != JoinStep.Form) { cancel() }

    when (val s = step) {
        JoinStep.Scan ->
            QrScanner(
                onResult = { text ->
                    if (step != JoinStep.Scan) return@QrScanner
                    val link = PairLink.parse(text)
                    if (link == null) {
                        error = copy(UiMessages.securityAndroidThisIsNotARemoteAgentPairingQrCode())
                        step = JoinStep.Form
                    } else {
                        code = link.code
                        server = link.server
                        commit = link.commit
                        join(link)
                    }
                },
                onCancel = { step = JoinStep.Form },
            )
        else ->
            Column(
                Modifier.fillMaxSize()
                    .background(p.paper)
                    .imePadding()
                    .verticalScrollFaded(),
            ) {
                when (s) {
                    is JoinStep.Emoji -> {
                        PageBar(
                            copy(UiMessages.securityComparePatterns()),
                            null,
                            right = {
                                Text(
                                    copy(UiMessages.commonCancel()),
                                    style = T.s15,
                                    color = p.blue,
                                    modifier = Modifier.clickable { cancel() },
                                )
                            },
                            more = { cancel() },
                        )
                        Text(
                            s.emoji.joinToString("  "),
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
                        if (comparisonPending) {
                            PillButton(
                                copy(UiMessages.securityPatternsMatchTrustAndContinue()),
                                { confirmSas(s.pairingId) },
                                Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                                ButtonKind.INK,
                            )
                        }
                        IconText(
                            Lucide.LoaderCircle,
                            if (comparisonPending) copy(UiMessages.securityConfirmThePatternsOnThisDeviceFirst()) else copy(UiMessages.securityAndroidWaitingForManagingClientApprovalAndCompletingJoin()),
                            p.ink2,
                            T.s13,
                            Modifier.fillMaxWidth().padding(vertical = 20.dp).wrapContentWidth(),
                        )
                        PillButton(
                            copy(UiMessages.securityPatternsDifferReturnToCodeEntry()),
                            { cancel() },
                            Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                            enabled = !registering,
                        )
                        Hint(
                            copy(UiMessages.securityAndroidGoingBackStopsThisWaitRejectTheOldRequestOn()),
                        )
                    }
                    JoinStep.Working -> {
                        JoinTitle(if (existingRepair) copy(UiMessages.securityOneTimeSecuritySetup()) else copy(UiMessages.securityJoinAnExistingAccount()))
                        IconText(
                            Lucide.LoaderCircle,
                            copy(UiMessages.securityAndroidConnecting()),
                            p.ink2,
                            T.s13,
                            Modifier.padding(horizontal = 16.dp),
                        )
                        if (error.isNotEmpty()) Hint(error)
                    }
                    JoinStep.Form,
                    JoinStep.Scan -> {
                        JoinTitle(if (existingRepair) copy(UiMessages.securityOneTimeSecuritySetup()) else copy(UiMessages.securityJoinAnExistingAccount()))
                        LegacyRepairBanner(status, ::dismissUpgradeNotice)
                        Text(
                            if (existingRepair)
                                copy(UiMessages.securityAndroidTheOriginalDeviceIdentityKeysAndHistoryAreKeptOpen())
                            else copy(UiMessages.securityAndroidOpenSettingsAddDeviceOnTheManagingClientScanIts()),
                            style = T.s13,
                            color = p.ink2,
                            modifier = Modifier.padding(horizontal = 16.dp).padding(bottom = 14.dp),
                        )
                        if (error.isNotEmpty()) {
                            Notice(
                                error,
                                Modifier.padding(horizontal = 16.dp).padding(bottom = 10.dp),
                                NoticeTone.PLAIN,
                            )
                            DiscardSetup(app) {
                                mode = "join"
                                error = ""
                                step = JoinStep.Form
                            }
                        }
                        androidx.compose.foundation.text.BasicTextField(
                            code,
                            { v ->
                                code = v.filter { it.isDigit() || it == ' ' }.take(9)
                                commit = null
                            },
                            Modifier.fillMaxWidth()
                                .padding(horizontal = 16.dp)
                                .padding(bottom = 10.dp),
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
                                    if (code.isEmpty())
                                        Text(
                                            copy(UiMessages.securityAndroidEnterThe8DigitPairingCode()),
                                            style = T.s15,
                                            color = p.ink2,
                                            textAlign = TextAlign.Center,
                                            modifier = Modifier.fillMaxWidth(),
                                        )
                                    inner()
                                }
                            },
                        )
                        JoinField(
                            copy(UiMessages.securityServer()),
                            server,
                            { server = it.trim() },
                            "https://",
                            keyboard = KeyboardType.Uri,
                        )
                        PillButton(
                            copy(if (pendingRegistration) UiMessages.commonRetry() else UiMessages.securityJoin()),
                            {
                                if (pendingRegistration) {
                                    resumeRegistration()
                                } else {
                                    try {
                                        join(
                                            PairLink(
                                                code.filter { it.isDigit() },
                                                checkedServer(server),
                                                commit,
                                            ),
                                        )
                                    } catch (e: Exception) {
                                        error = displayError(e)
                                    }
                                }
                            },
                            Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                            ButtonKind.INK,
                            enabled = pendingRegistration || (code.count { it.isDigit() } == 8 && server.length > 8),
                        )
                        JoinLink(copy(UiMessages.securityAndroidScanQrCode())) {
                            error = ""
                            step = JoinStep.Scan
                        }
                        if (existingRepair) JoinLink(copy(UiMessages.securityRecoverManagementWithARecoveryCode())) { mode = "recover" }
                        else JoinLink(copy(UiMessages.commonBack())) { mode = "entry" }
                    }
                }
            }
    }
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

/** A field on the join page: a small label over what is typed. */
@Composable
private fun JoinField(
    label: String,
    value: String,
    onValue: (String) -> Unit,
    placeholder: String,
    mono: Boolean = false,
    lines: Int = 1,
    keyboard: KeyboardType = KeyboardType.Text,
) {
    val p = pal
    androidx.compose.foundation.text.BasicTextField(
        value,
        onValue,
        Modifier.fillMaxWidth().padding(horizontal = 16.dp).padding(bottom = 10.dp),
        textStyle = (if (mono) T.mono13 else T.s15).copy(color = p.ink),
        singleLine = lines == 1,
        minLines = lines,
        keyboardOptions = KeyboardOptions(keyboardType = keyboard),
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
                    Box {
                        if (value.isEmpty()) Text(placeholder, style = T.s15, color = p.ink2)
                        inner()
                    }
                }
            }
        },
    )
}

/** Read again at confirmation, so newly pending work cannot be silently erased. */
@Composable
private fun RejoinButton(
    app: RaApp,
    title: String,
    readAdvice: suspend () -> SecurityExitAdvice,
) {
    val copy = uiText()
    val scope = rememberCoroutineScope()
    var advice by remember { mutableStateOf<SecurityExitAdvice?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    fun leave(confirmed: Boolean) {
        if (busy) return
        busy = true
        error = ""
        scope.launch {
            try {
                val current = readAdvice()
                val risky = current.authority || current.unresolved
                if (risky && (!confirmed || current != advice)) advice = current
                else app.logout(pairing = true)
            } catch (e: Exception) {
                error = displayError(e)
            } finally {
                busy = false
            }
        }
    }
    PillButton(title, { leave(false) }, Modifier.fillMaxWidth(), ButtonKind.INK, enabled = !busy)
    if (error.isNotEmpty()) Hint(error)
    advice?.let { risk ->
        val body = translateSecurityKey(risk.message, copy).ifEmpty {
            copy(UiMessages.securityAndroidThisDeletesTheAccountKeysAndCacheOnThisPhone())
        }
        Confirm(
            copy(UiMessages.securityDeleteLocalKeysAndRejoin()),
            body,
            copy(UiMessages.securityRejoin()),
            danger = true,
            onDismiss = { advice = null },
        ) { leave(true) }
    }
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
