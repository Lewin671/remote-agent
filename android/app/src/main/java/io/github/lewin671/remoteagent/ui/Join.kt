package io.github.lewin671.remoteagent.ui

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
import io.github.lewin671.remoteagent.core.errorText
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
    val p = pal
    val scope = rememberCoroutineScope()
    var leaving by remember { mutableStateOf(false) }
    Column(
        Modifier.fillMaxSize().background(p.card).padding(horizontal = 24.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text("服务器拒绝了这台设备的登录", style = T.h17, color = p.fg, textAlign = TextAlign.Center)
        Text(
            "可能是设备被移除、服务重置或连接异常。这条消息不能证明安全撤销已经完成，请先重试或在管理端核实。",
            style = T.sm,
            color = p.muted,
            textAlign = TextAlign.Center,
            modifier = Modifier.padding(top = 8.dp, bottom = 20.dp),
        )
        Btn(
            "重新加入账户",
            {
                leaving = true
                scope.launch { app.logout() }
            },
            Modifier.fillMaxWidth(),
            BtnKind.PRIMARY,
            enabled = !leaving,
        )
        Text(
            "会先删除这台设备上的账户密钥和缓存，再用配对码加入。",
            style = T.sm,
            color = p.muted,
            textAlign = TextAlign.Center,
            modifier = Modifier.padding(top = 10.dp, bottom = 4.dp),
        )
        TextAction(
            "再试一次",
            { remote.retryLogin() },
            enabled = !leaving,
            alignment = Alignment.Center,
        )
    }
}

@Composable
fun JoinScreen(app: RaApp) {
    val p = pal
    val scope = rememberCoroutineScope()
    val existing by app.securitySetup.collectAsState()
    var existingRemoved by remember(existing) { mutableStateOf(false) }
    LaunchedEffect(existing) {
        existing?.let { account ->
            existingRemoved =
                runCatching {
                        withContext(Dispatchers.IO) {
                            io.github.lewin671.remoteagent.core.existingIdentityRemoved(
                                io.github.lewin671.remoteagent.core.Api(account.server, app.http),
                                account,
                            )
                        }
                    }
                    .getOrDefault(false)
        }
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
    val pendingCreation by app.pendingCreation.collectAsState()
    var mode by
        remember(pendingCreation, existing) {
            mutableStateOf(
                if (pendingCreation) if (existing == null) "create" else "rebuild" else "join",
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
                                    "另一个服务器的注册尚未完成，请先重试或退出并重新开始",
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
                if (registering) error = "不确定这台手机有没有加入：${errorText(e)}。可以在管理端的设置里看看客户端列表"
                else if (!stopped) error = errorText(e)
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
            error = "已经在登记这台手机，稍等结果"
            return
        }
        stopped = true
        comparison?.complete(false)
        comparison = null
        job?.cancel()
        code = ""
        commit = null
        error = "已取消本次加入。旧请求可能还占着配对码：请在管理端上拒绝它，再生成新的配对码"
        step = JoinStep.Form
    }

    if (mode == "create") {
        CreateAccountScreen(app) { mode = "join" }
        return
    }
    if (mode == "recover") {
        RecoverScreen(app) { mode = "join" }
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
                        error = "这不是 remote-agent 的配对二维码"
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
                    .background(p.card)
                    .imePadding()
                    .verticalScroll(rememberScrollState()),
            ) {
                when (s) {
                    is JoinStep.Emoji -> {
                        NavBar(
                            "核对图案",
                            null,
                            right = {
                                Text(
                                    "取消",
                                    style = T.body,
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
                            "核对管理端上的 4 个图案；一致后先在本机确认，再在管理端上允许。",
                            style = T.sm,
                            color = p.muted,
                            textAlign = TextAlign.Center,
                            modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                        )
                        if (comparison != null)
                            Btn(
                                "图案一致，信任并继续",
                                {
                                    registering = true
                                    comparison?.complete(true)
                                    comparison = null
                                },
                                Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                                BtnKind.PRIMARY,
                            )
                        IconText(
                            Lucide.LoaderCircle,
                            if (comparison != null) "请先在本机确认图案" else "等待管理端批准并完成加入…",
                            p.muted,
                            T.sm,
                            Modifier.fillMaxWidth().padding(vertical = 20.dp).wrapContentWidth(),
                        )
                        Btn(
                            "图案不一致，返回填码",
                            { cancel() },
                            Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                        )
                        Hint(
                            "返回后停止这次等待；请在管理端上拒绝旧请求，再生成新的配对码。",
                            Modifier.padding(horizontal = 16.dp),
                        )
                    }
                    JoinStep.Working -> {
                        JoinTitle(if (existing != null) "一次性安全设置" else "加入已有账户")
                        IconText(
                            Lucide.LoaderCircle,
                            "正在连接…",
                            p.muted,
                            T.sm,
                            Modifier.padding(horizontal = 16.dp),
                        )
                        if (error.isNotEmpty()) Hint(error, Modifier.padding(horizontal = 16.dp))
                    }
                    JoinStep.Form,
                    JoinStep.Scan -> {
                        JoinTitle(if (existing != null) "一次性安全设置" else "加入已有账户")
                        Text(
                            if (existingRemoved) "这台设备的旧身份已被移除，重新加入会生成新的设备身份，旧历史仍可读。请用管理端重新配对。"
                            else if (existing != null)
                                "原设备身份、密钥和历史保留。在管理端上打开「设置 › 添加设备」，扫码或填配对码后，两边核对 4 个图案完成加入。"
                            else "在管理端打开「设置 › 添加设备」，扫它显示的二维码，或把 8 位配对码填在这里。",
                            style = T.sm,
                            color = p.muted,
                            modifier = Modifier.padding(horizontal = 16.dp).padding(bottom = 14.dp),
                        )
                        if (error.isNotEmpty())
                            Banner(
                                error,
                                Modifier.padding(horizontal = 16.dp).padding(bottom = 10.dp),
                                Tone2.PLAIN,
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
                                    color = p.fg,
                                ),
                            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                            singleLine = true,
                            cursorBrush = androidx.compose.ui.graphics.SolidColor(p.blue),
                            decorationBox = { inner ->
                                FieldBox {
                                    if (code.isEmpty())
                                        Text(
                                            "输入 8 位配对码",
                                            style = T.body,
                                            color = p.muted,
                                            textAlign = TextAlign.Center,
                                            modifier = Modifier.fillMaxWidth(),
                                        )
                                    inner()
                                }
                            },
                        )
                        JoinField(
                            "服务器",
                            server,
                            { server = it.trim() },
                            "https://",
                            keyboard = KeyboardType.Uri,
                        )
                        Btn(
                            "加入",
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
                                    error = errorText(e)
                                }
                            },
                            Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                            BtnKind.PRIMARY,
                            enabled = code.count { it.isDigit() } == 8 && server.length > 8,
                        )
                        JoinLink("扫描二维码") {
                            error = ""
                            step = JoinStep.Scan
                        }
                        if (existing != null && !existingRemoved)
                            JoinLink("已有账户升级？重建安全域并成为管理端") { mode = "rebuild" }
                        JoinLink("用恢复码找回管理权") { mode = "recover" }
                        if (existing == null) JoinLink("新建账户") { mode = "create" }
                    }
                }
            }
    }
}

@Composable
private fun JoinTitle(text: String) {
    Text(
        text,
        style = T.title30,
        color = pal.fg,
        modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 24.dp, bottom = 8.dp),
    )
}

@Composable
private fun JoinLink(text: String, onClick: () -> Unit) {
    Box(
        Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable(onClick = onClick),
        contentAlignment = Alignment.Center,
    ) {
        Text(text, style = T.sm, color = pal.blue)
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
        textStyle = (if (mono) T.mono13 else T.body).copy(color = p.fg),
        singleLine = lines == 1,
        minLines = lines,
        keyboardOptions = KeyboardOptions(keyboardType = keyboard),
        cursorBrush = androidx.compose.ui.graphics.SolidColor(p.blue),
        decorationBox = { inner ->
            FieldBox {
                Column {
                    Text(
                        label,
                        style = T.meta,
                        color = p.muted,
                        modifier = Modifier.padding(bottom = 2.dp),
                    )
                    Box {
                        if (value.isEmpty()) Text(placeholder, style = T.body, color = p.muted)
                        inner()
                    }
                }
            }
        },
    )
}
