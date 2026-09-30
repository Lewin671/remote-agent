package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
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
import io.github.lewin671.remoteagent.BuildConfig
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.PairLink
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
@Composable
fun JoinScreen(app: RaApp) {
    val p = pal
    val scope = rememberCoroutineScope()
    var step by remember { mutableStateOf<JoinStep>(JoinStep.Form) }
    var code by remember { mutableStateOf("") }
    var server by rememberSaveable { mutableStateOf("https://") }
    var commit by remember { mutableStateOf<ByteArray?>(null) }
    var secret by remember { mutableStateOf("") }
    var error by remember { mutableStateOf("") }
    var job by remember { mutableStateOf<Job?>(null) }
    var stopped by remember { mutableStateOf(false) }
    // Registering has begun: canceling now would misreport the outcome.
    var registering by remember { mutableStateOf(false) }

    fun checkedServer(s: String) = Secret.checkServer(s, allowPlainHttp = BuildConfig.DEBUG)

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
        else -> Column(
            Modifier.fillMaxSize().safeDrawingPadding().imePadding().verticalScroll(rememberScrollState()).padding(24.dp),
            verticalArrangement = Arrangement.spacedBy(14.dp),
        ) {
            Spacer(Modifier.height(24.dp))
            when (s) {
                is JoinStep.Emoji -> {
                    Text("核对图案", style = MaterialTheme.typography.headlineSmall)
                    Text(s.emoji.joinToString("  "), fontSize = 40.sp, modifier = Modifier.fillMaxWidth(), textAlign = TextAlign.Center)
                    Text("确认另一台设备上显示的 4 个图案和这里一模一样，然后在那台设备上点「图案一致，允许」。", color = p.muted)
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        CircularProgressIndicator(Modifier.padding(end = 10.dp).size(18.dp), strokeWidth = 2.dp)
                        Text("等待允许…", color = p.muted)
                    }
                    TextButton(onClick = { cancel() }) { Text("取消，图案不一致也点这里") }
                }
                JoinStep.Working -> {
                    Text("加入账户", style = MaterialTheme.typography.headlineSmall)
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        CircularProgressIndicator(Modifier.padding(end = 10.dp).size(18.dp), strokeWidth = 2.dp)
                        Text("正在连接…", color = p.muted)
                    }
                }
                JoinStep.Secret -> {
                    Text("用账户密钥加入", style = MaterialTheme.typography.headlineSmall)
                    Text("在电脑上运行 remote-agent account secret，把输出的 ra1. 开头的整串粘贴到这里。", color = p.muted)
                    OutlinedTextField(
                        secret, { secret = it }, Modifier.fillMaxWidth(), label = { Text("账户密钥") },
                        textStyle = MaterialTheme.typography.bodyMedium.copy(fontFamily = FontFamily.Monospace), minLines = 3,
                    )
                    if (error.isNotEmpty()) Text(error, color = p.red)
                    Button(onClick = {
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
                    }, enabled = secret.isNotBlank(), modifier = Modifier.fillMaxWidth().height(48.dp)) { Text("加入") }
                    TextButton(onClick = { error = ""; step = JoinStep.Form }) { Text("改用配对码") }
                }
                JoinStep.Form, JoinStep.Scan -> {
                    Text("加入账户", style = MaterialTheme.typography.headlineSmall)
                    Text("在已加入的设备上打开「设置 › 添加设备」，扫二维码，或把 8 位配对码填在这里。", color = p.muted)
                    OutlinedButton(onClick = { error = ""; step = JoinStep.Scan }, modifier = Modifier.fillMaxWidth().height(48.dp)) { Text("扫描二维码") }
                    OutlinedTextField(
                        code, { v -> code = v.filter { it.isDigit() || it == ' ' }.take(9); commit = null }, Modifier.fillMaxWidth(),
                        label = { Text("配对码") }, singleLine = true,
                        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                        textStyle = MaterialTheme.typography.titleMedium.copy(fontFamily = FontFamily.Monospace),
                    )
                    OutlinedTextField(
                        server, { server = it.trim() }, Modifier.fillMaxWidth(), label = { Text("服务器") }, singleLine = true,
                        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
                    )
                    if (error.isNotEmpty()) Text(error, color = p.red)
                    Button(
                        onClick = {
                            try {
                                join(PairLink(code.filter { it.isDigit() }, checkedServer(server), commit))
                            } catch (e: Exception) {
                                error = errorText(e)
                            }
                        },
                        enabled = code.count { it.isDigit() } == 8 && server.length > 8,
                        modifier = Modifier.fillMaxWidth().height(48.dp),
                    ) { Text("加入") }
                    TextButton(onClick = { error = ""; step = JoinStep.Secret }) { Text("改用账户密钥") }
                }
            }
        }
    }
}
