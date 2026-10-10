package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.wrapContentWidth
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.activity.compose.BackHandler
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.PairLink
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.Secret
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.joinByPairing
import io.github.lewin671.remoteagent.core.joinWithRoot
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

private sealed interface JoinStep {
    data object Form : JoinStep
    data object Scan : JoinStep
    data object Secret : JoinStep
    data class Emoji(val emoji: List<String>) : JoinStep
    data object Working : JoinStep
}

/** First run: join the account with a pairing code, a scanned QR code, or the account secret. */
/** The server turned this device away: nothing of the account is shown until it joins again. */
@Composable
fun RemovedScreen(app: RaApp, remote: Remote) {
    val p = pal
    val scope = rememberCoroutineScope()
    var leaving by remember { mutableStateOf(false) }
    Column(
        Modifier.fillMaxSize().background(p.card).safeDrawingPadding().padding(horizontal = 24.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text("这台设备已被移出账户", style = T.h17, color = p.fg, textAlign = TextAlign.Center)
        Text(
            "服务器不再接受它的登录：它在另一台设备上被移除了，或者服务器的数据被重置了。电脑上的会话不受影响。",
            style = T.sm, color = p.muted, textAlign = TextAlign.Center, modifier = Modifier.padding(top = 8.dp, bottom = 20.dp),
        )
        Btn("重新加入账户", { leaving = true; scope.launch { app.logout() } }, Modifier.fillMaxWidth(), BtnKind.PRIMARY, enabled = !leaving)
        Text("会先删除这台设备上的账户密钥和缓存，再用配对码加入。", style = T.sm, color = p.muted, textAlign = TextAlign.Center, modifier = Modifier.padding(top = 10.dp, bottom = 4.dp))
        TextAction("再试一次", { remote.retryLogin() }, enabled = !leaving, alignment = Alignment.Center)
    }
}

@Composable
fun JoinScreen(app: RaApp) {
    val p = pal
    val scope = rememberCoroutineScope()
    var step by remember { mutableStateOf<JoinStep>(JoinStep.Form) }
    var code by remember { mutableStateOf("") }
    var server by rememberSaveable { mutableStateOf(app.lastServer.ifEmpty { "https://" }) }
    var commit by remember { mutableStateOf<ByteArray?>(null) }
    var secret by remember { mutableStateOf("") }
    var error by remember { mutableStateOf("") }
    var job by remember { mutableStateOf<Job?>(null) }
    var stopped by remember { mutableStateOf(false) }
    // Registering has begun: canceling now would misreport the outcome.
    var registering by remember { mutableStateOf(false) }

    fun checkedServer(s: String) = Secret.checkServer(s, allowPlainHttp = app.debuggable)

    fun join(link: PairLink) {
        error = ""
        stopped = false
        step = JoinStep.Working
        job = scope.launch {
            try {
                val l = link.copy(server = checkedServer(link.server))
                val acct = withContext(Dispatchers.IO) {
                    joinByPairing(l, app.http, { emoji -> scope.launch { step = JoinStep.Emoji(emoji) } }, { stopped }, { registering = true })
                }
                app.joined(acct)
            } catch (e: Exception) {
                if (registering) error = "不确定这台手机有没有加入：${errorText(e)}。可以在已有设备的设置里看看客户端列表"
                else if (!stopped) error = errorText(e)
                registering = false
                step = JoinStep.Form
            }
        }
    }

    /**
     * Stops waiting: no more polling or joining by itself, the one-off pairing
     * key goes with the job. The server is kept and the old code cleared; the
     * server may still hold the old request. Once registering has begun the
     * join goes on and its outcome is shown instead.
     */
    fun cancel() {
        if (registering) {
            error = "已经在登记这台手机，稍等结果"
            return
        }
        stopped = true
        job?.cancel()
        code = ""
        commit = null
        error = "已取消本次加入。旧请求可能还占着配对码：请在已有设备上拒绝它，再生成新码"
        step = JoinStep.Form
    }

    BackHandler(enabled = step != JoinStep.Form) { cancel() }

    when (val s = step) {
        JoinStep.Scan -> QrScanner(
            onResult = { text ->
                // The camera may still hand over a code after 取消: the scanner is not what is shown then, and nothing is joined.
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
        else -> Column(Modifier.fillMaxSize().background(p.card).safeDrawingPadding().imePadding().verticalScroll(rememberScrollState())) {
            when (s) {
                is JoinStep.Emoji -> {
                    NavBar("核对图案", null, right = { Text("取消", style = T.body, color = p.blue, modifier = Modifier.clickable { cancel() }) }, more = { cancel() })
                    Text(s.emoji.joinToString("  "), fontSize = 30.sp, modifier = Modifier.fillMaxWidth().padding(top = 40.dp, bottom = 12.dp), textAlign = TextAlign.Center)
                    Text("确认另一台设备上显示的 4 个图案和这里一模一样，然后在那台设备上点「图案一致，允许」。", style = T.sm, color = p.muted, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp))
                    IconText(Lucide.LoaderCircle, "等待允许…", p.muted, T.sm, Modifier.fillMaxWidth().padding(vertical = 20.dp).wrapContentWidth())
                    Btn("图案不一致，返回填码", { cancel() }, Modifier.fillMaxWidth().padding(horizontal = 16.dp))
                    Hint("返回后停止这次等待；请让另一台设备拒绝旧请求，再生成新的配对码。", Modifier.padding(horizontal = 16.dp))
                }
                JoinStep.Working -> {
                    JoinTitle("加入账户")
                    IconText(Lucide.LoaderCircle, "正在连接…", p.muted, T.sm, Modifier.padding(horizontal = 16.dp))
                }
                JoinStep.Secret -> {
                    JoinTitle("用账户密钥加入")
                    Text("在电脑上运行 remote-agent account secret，把输出的 ra1. 开头的整串粘贴到这里。", style = T.sm, color = p.muted, modifier = Modifier.padding(horizontal = 16.dp).padding(bottom = 14.dp))
                    JoinField("账户密钥", secret, { secret = it }, "ra1.…", mono = true, lines = 3)
                    if (error.isNotEmpty()) Banner(error, Modifier.padding(horizontal = 16.dp).padding(bottom = 10.dp), Tone2.ERROR)
                    Btn("加入", {
                        error = ""
                        step = JoinStep.Working
                        scope.launch {
                            try {
                                val sec = Secret.parse(secret)
                                val acct = withContext(Dispatchers.IO) { joinWithRoot(sec.root, sec.server, app.http) }
                                secret = ""
                                app.joined(acct)
                            } catch (e: Exception) {
                                error = errorText(e)
                                step = JoinStep.Secret
                            }
                        }
                    }, Modifier.fillMaxWidth().padding(horizontal = 16.dp), BtnKind.PRIMARY, enabled = secret.isNotBlank())
                    JoinLink("改用配对码") { error = ""; step = JoinStep.Form }
                }
                JoinStep.Form, JoinStep.Scan -> {
                    JoinTitle("加入账户")
                    Text("在已加入的设备上打开「设置 › 添加设备」，扫二维码，或把 8 位配对码填在这里。", style = T.sm, color = p.muted, modifier = Modifier.padding(horizontal = 16.dp).padding(bottom = 14.dp))
                    if (error.isNotEmpty()) Banner(error, Modifier.padding(horizontal = 16.dp).padding(bottom = 10.dp), Tone2.PLAIN)
                    androidx.compose.foundation.text.BasicTextField(
                        code, { v -> code = v.filter { it.isDigit() || it == ' ' }.take(9); commit = null },
                        Modifier.fillMaxWidth().padding(horizontal = 16.dp).padding(bottom = 10.dp),
                        textStyle = androidx.compose.ui.text.TextStyle(fontFamily = MonoFamily, fontWeight = androidx.compose.ui.text.font.FontWeight.SemiBold, fontSize = 22.sp, letterSpacing = 4.sp, textAlign = TextAlign.Center, color = p.fg),
                        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number), singleLine = true,
                        cursorBrush = androidx.compose.ui.graphics.SolidColor(p.blue),
                        decorationBox = { inner ->
                            FieldBox {
                                if (code.isEmpty()) Text("输入 8 位配对码", style = T.body, color = p.muted, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth())
                                inner()
                            }
                        },
                    )
                    JoinField("服务器", server, { server = it.trim() }, "https://", keyboard = KeyboardType.Uri)
                    Btn("加入", {
                        try {
                            join(PairLink(code.filter { it.isDigit() }, checkedServer(server), commit))
                        } catch (e: Exception) {
                            error = errorText(e)
                        }
                    }, Modifier.fillMaxWidth().padding(horizontal = 16.dp), BtnKind.PRIMARY, enabled = code.count { it.isDigit() } == 8 && server.length > 8)
                    JoinLink("扫描二维码") { error = ""; step = JoinStep.Scan }
                    JoinLink("改用账户密钥") { error = ""; step = JoinStep.Secret }
                }
            }
        }
    }
}

@Composable
private fun JoinTitle(text: String) {
    Text(text, style = T.title30, color = pal.fg, modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 24.dp, bottom = 8.dp))
}

@Composable
private fun JoinLink(text: String, onClick: () -> Unit) {
    Box(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable(onClick = onClick), contentAlignment = Alignment.Center) {
        Text(text, style = T.sm, color = pal.blue)
    }
}

/** A field on the join page: a small label over what is typed. */
@Composable
private fun JoinField(label: String, value: String, onValue: (String) -> Unit, placeholder: String, mono: Boolean = false, lines: Int = 1, keyboard: KeyboardType = KeyboardType.Text) {
    val p = pal
    androidx.compose.foundation.text.BasicTextField(
        value, onValue, Modifier.fillMaxWidth().padding(horizontal = 16.dp).padding(bottom = 10.dp),
        textStyle = (if (mono) T.mono13 else T.body).copy(color = p.fg), singleLine = lines == 1, minLines = lines,
        keyboardOptions = KeyboardOptions(keyboardType = keyboard), cursorBrush = androidx.compose.ui.graphics.SolidColor(p.blue),
        decorationBox = { inner ->
            FieldBox {
                Column {
                    Text(label, style = T.meta, color = p.muted, modifier = Modifier.padding(bottom = 2.dp))
                    Box {
                        if (value.isEmpty()) Text(placeholder, style = T.body, color = p.muted)
                        inner()
                    }
                }
            }
        },
    )
}
