package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.wrapContentWidth
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
import io.github.lewin671.remoteagent.ui.displayError
import io.github.lewin671.remoteagent.core.joinByPairing
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

private sealed interface JoinStep {
    data object Form : JoinStep

    data object Scan : JoinStep

    data class Emoji(val emoji: List<String>) : JoinStep

    data object Working : JoinStep
}

/** First run: join the account with a pairing code, a scanned QR code, with explicit approval. */
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
        RejoinButton(app, copy(UiMessages.settingsRejoinAccount()))
        androidx.compose.runtime.key(remote) {
            PendingSecurityOperation(
                readPending = { app.vault.readSecurityOperation() != null },
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

/** No action until a background vault read confirms there is work to resume. */
@Composable
internal fun PendingSecurityOperation(readPending: () -> Boolean, resume: suspend () -> Unit) {
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
                        // The operation may have completed since the page opened.
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
fun JoinScreen(app: RaApp) {
    val generation by app.joinGeneration.collectAsState()
    androidx.compose.runtime.key(generation) { JoinContent(app) }
}

@Composable
private fun JoinContent(app: RaApp) {
    val copy = uiText()
    val p = pal
    val scope = rememberCoroutineScope()
    val existing by app.securitySetup.collectAsState()
    val pendingCreation by app.pendingCreation.collectAsState()
    var checkingIdentity by remember(existing) { mutableStateOf(existing != null) }
    var existingRemoved by remember(existing) { mutableStateOf(false) }
    LaunchedEffect(existing, pendingCreation) {
        existingRemoved = false
        existing?.takeUnless { pendingCreation }?.let { account ->
            existingRemoved =
                runCatching {
                        withContext(Dispatchers.IO) {
                            val api = io.github.lewin671.remoteagent.core.Api(account.server, app.http)
                            io.github.lewin671.remoteagent.core.probeRetainedIdentity {
                                io.github.lewin671.remoteagent.core.login(api, account)
                            }
                        }
                    }
                    .getOrDefault(false)
        }
        checkingIdentity = false
    }
    var step by remember { mutableStateOf<JoinStep>(JoinStep.Form) }
    var code by remember { mutableStateOf("") }
    var server by rememberSaveable { mutableStateOf(app.lastServer.ifEmpty { "https://" }) }
    var commit by remember { mutableStateOf<ByteArray?>(null) }
    var error by remember { mutableStateOf("") }
    var job by remember { mutableStateOf<Job?>(null) }
    var stopped by remember { mutableStateOf(false) }
    // Registering has begun: canceling now would misreport the outcome.
    var registering by remember { mutableStateOf(false) }
    var comparison by remember {
        mutableStateOf<kotlinx.coroutines.CompletableDeferred<Boolean>?>(null)
    }
    var mode by
        remember(pendingCreation, existing) {
            mutableStateOf(
                if (pendingCreation) if (existing == null) "create" else "rebuild" else if (existing != null) "join" else if (app.joinDirectly) "join" else "entry",
            )
        }

    fun checkedServer(s: String) = Secret.checkServer(s, allowPlainHttp = app.debuggable)

    fun join(link: PairLink) {
        error = ""
        stopped = false
        step = JoinStep.Working
        job = scope.launch {
            try {
                val l = link.copy(server = checkedServer(link.server))
                val acct =
                    withContext(Dispatchers.IO) {
                        val pending = app.vault.loadRegistrationIntent()
                        if (pending != null) {
                            if (pending.account.server != l.server)
                                throw io.github.lewin671.remoteagent.core.PairException(
                                    copy(UiMessages.securityRegistrationOnAnotherServerIsUnfinishedRetryItOr()),
                                )
                            registering = true
                            val resumed =
                                pending.resume(app.http) { account, record ->
                                    app.persistPairing(account, record)
                                }
                            app.vault.clearRegistrationIntent(pending)
                            resumed
                        } else
                            joinByPairing(
                                l,
                                app.http,
                                { emoji -> scope.launch { step = JoinStep.Emoji(emoji) } },
                                { stopped },
                                { registering = true },
                                persist = { acct, record -> app.persistPairing(acct, record) },
                                loadIdentity = app.vault::loadPairingIdentity,
                                persistIdentity = app.vault::savePairingIdentity,
                                persistRegistration = app.vault::saveRegistrationIntent,
                                clearRegistration = { app.vault.clearRegistrationIntent(it) },
                                existing = existing,
                                existingRemoved = existingRemoved,
                                confirm = { emoji ->
                                    val answer = kotlinx.coroutines.CompletableDeferred<Boolean>()
                                    withContext(Dispatchers.Main) {
                                        step = JoinStep.Emoji(emoji)
                                        comparison = answer
                                    }
                                    answer.await()
                                },
                            )
                    }
                app.joined(acct)
            } catch (e: Exception) {
                if (registering) error = copy(UiMessages.securityAndroidCannotConfirmWhetherThisPhoneJoinedValue1CheckTheClient((displayError(e)).toString()))
                else if (!stopped) error = displayError(e)
                registering = false
                step = JoinStep.Form
            }
        }
    }

    /**
     * Stops waiting: no more polling or joining by itself, the one-off pairing key goes with the
     * job. The server is kept and the old code cleared; the server may still hold the old request.
     * Once registering has begun the join goes on and its outcome is shown instead.
     */
    fun cancel() {
        if (registering) {
            error = copy(UiMessages.securityAndroidAlreadyRegisteringThisPhoneWaitForTheResult())
            return
        }
        stopped = true
        comparison?.complete(false)
        comparison = null
        job?.cancel()
        code = ""
        commit = null
        error = copy(UiMessages.securityAndroidThisJoinAttemptWasCancelledTheOldRequestMayStill())
        step = JoinStep.Form
    }

    if (checkingIdentity) {
        Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) { Text(copy(UiMessages.securityAndroidConnecting()), color = p.ink2) }
        return
    }
    if (existingRemoved) {
        Column(Modifier.fillMaxSize().background(p.paper).padding(24.dp),
            verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
            Text(copy(UiMessages.securityTheServerDoesNotRecognizeThisDevice()), style = T.s17b, color = p.ink)
            Hint(copy(UiMessages.securityItMayHaveBeenRemovedOrTheServiceMay()), inset = 0.dp)
            RejoinButton(app, copy(UiMessages.securityRejoinWithAPairingCode()))
            Hint(copy(UiMessages.securityAndroidFirstDeleteTheAccountKeysAndCacheOnThisPhone()), inset = 0.dp)
        }
        return
    }

    if (mode == "entry") {
        Column(Modifier.fillMaxSize().background(p.gbg).verticalScrollFaded(p.gbg)) {
            LargeTitle(copy(UiMessages.securityJoinAccount()))
            Group(rows = listOf(
                { GRow(copy(UiMessages.securityCreateAccount()), onClick = { mode = "create" }, chevron = true) },
                { GRow(copy(UiMessages.securityJoinAnExistingAccount()), onClick = { mode = "join" }, chevron = true) },
                { GRow(copy(UiMessages.securityRecoverManagementWithARecoveryCode()), onClick = { mode = "recover" }, chevron = true) },
            ))
        }
        return
    }
    BackHandler(enabled = existing == null && mode == "join" && step == JoinStep.Form) { mode = "entry" }

    if (mode == "create") {
        CreateAccountScreen(app) { mode = if (existing == null) "entry" else "join" }
        return
    }
    if (mode == "recover") {
        RecoverScreen(app) { mode = if (existing == null) "entry" else "join" }
        return
    }
    if (mode == "rebuild") {
        RebuildSecurityScreen(app, existing) { mode = "join" }
        return
    }

    BackHandler(enabled = step != JoinStep.Form) { cancel() }

    when (val s = step) {
        JoinStep.Scan ->
            QrScanner(
                onResult = { text ->
                    // The camera may still hand over a code after 取消: the scanner is not what is
                    // shown then, and nothing is joined.
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
                        if (comparison != null)
                            PillButton(
                                copy(UiMessages.securityPatternsMatchTrustAndContinue()),
                                {
                                    registering = true
                                    comparison?.complete(true)
                                    comparison = null
                                },
                                Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                                ButtonKind.INK,
                            )
                        IconText(
                            Lucide.LoaderCircle,
                            if (comparison != null) copy(UiMessages.securityConfirmThePatternsOnThisDeviceFirst()) else copy(UiMessages.securityAndroidWaitingForManagingClientApprovalAndCompletingJoin()),
                            p.ink2,
                            T.s13,
                            Modifier.fillMaxWidth().padding(vertical = 20.dp).wrapContentWidth(),
                        )
                        PillButton(
                            copy(UiMessages.securityPatternsDifferReturnToCodeEntry()),
                            { cancel() },
                            Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                        )
                        Hint(
                            copy(UiMessages.securityAndroidGoingBackStopsThisWaitRejectTheOldRequestOn()),
                        )
                    }
                    JoinStep.Working -> {
                        JoinTitle(if (existing != null) copy(UiMessages.securityOneTimeSecuritySetup()) else copy(UiMessages.securityJoinAnExistingAccount()))
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
                        JoinTitle(if (existing != null) copy(UiMessages.securityOneTimeSecuritySetup()) else copy(UiMessages.securityJoinAnExistingAccount()))
                        Text(
                            if (existing != null)
                                copy(UiMessages.securityAndroidTheOriginalDeviceIdentityKeysAndHistoryAreKeptOpen())
                            else copy(UiMessages.securityAndroidOpenSettingsAddDeviceOnTheManagingClientScanIts()),
                            style = T.s13,
                            color = p.ink2,
                            modifier = Modifier.padding(horizontal = 16.dp).padding(bottom = 14.dp),
                        )
                        if (error.isNotEmpty())
                            Notice(
                                error,
                                Modifier.padding(horizontal = 16.dp).padding(bottom = 10.dp),
                                NoticeTone.PLAIN,
                            )
                        if (error.isNotEmpty())
                            DiscardSetup(app) {
                                mode = "join"
                                error = ""
                                step = JoinStep.Form
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
                            copy(UiMessages.securityJoin()),
                            {
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
                            },
                            Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                            ButtonKind.INK,
                            enabled = code.count { it.isDigit() } == 8 && server.length > 8,
                        )
                        JoinLink(copy(UiMessages.securityAndroidScanQrCode())) {
                            error = ""
                            step = JoinStep.Scan
                        }
                        if (existing != null && !existingRemoved)
                            JoinLink(copy(UiMessages.securityAndroidUpgradingAnExistingAccountRebuildTheSecurityDomainAndBecome())) { mode = "rebuild" }
                        if (existing != null) JoinLink(copy(UiMessages.securityRecoverManagementWithARecoveryCode())) { mode = "recover" }
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
private fun RejoinButton(app: RaApp, title: String) {
    val copy = uiText()
    val scope = rememberCoroutineScope()
    var advice by remember { mutableStateOf<ExitRisk?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    fun leave(confirmed: Boolean) {
        if (busy) return
        busy = true
        scope.launch {
            try {
                val warning = withContext(Dispatchers.IO) {
                    val pending = app.vault.readSecurityOperation() != null || app.vault.loadRegistrationIntent() != null
                    val admin = if (app.vault.hasSecurityState()) app.vault.readSecurity().let { it.memberID == it.head.core.authorityID } else false
                    if (pending || admin) authorityExitRisk(admin, pending) else null
                }
                if (warning != null && (!confirmed || warning != advice)) advice = warning
                else app.logout(pairing = true)
            } catch (e: Exception) { error = displayError(e) }
            finally { busy = false }
        }
    }
    PillButton(title, { leave(false) }, Modifier.fillMaxWidth(), ButtonKind.INK, enabled = !busy)
    if (error.isNotEmpty()) Hint(error)
    advice?.let { risk ->
        Confirm(copy(UiMessages.securityDeleteLocalKeysAndRejoin()), authorityExitAdvice(risk, copy) + copy(UiMessages.securityAndroidThisDeletesTheAccountKeysAndCacheOnThisPhone()), copy(UiMessages.securityRejoin()), danger = true,
            onDismiss = { advice = null }) { leave(true) }
    }
}
