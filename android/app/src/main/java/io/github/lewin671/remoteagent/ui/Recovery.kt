package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.CreatedAccount
import io.github.lewin671.remoteagent.core.Recovery
import io.github.lewin671.remoteagent.core.RecoveryChain
import io.github.lewin671.remoteagent.core.RecoveryCode
import io.github.lewin671.remoteagent.core.RecoverySubmission
import io.github.lewin671.remoteagent.core.Secret
import io.github.lewin671.remoteagent.core.SigningKey
import io.github.lewin671.remoteagent.core.createAccount
import io.github.lewin671.remoteagent.core.dayText
import io.github.lewin671.remoteagent.core.deviceId
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.randomBytes
import io.github.lewin671.remoteagent.proto.GetRecoveryContextResponse
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** 管理端 · 恢复: a pending recovery can be vetoed here, which shows a new recovery code once. */
@Composable
fun RecoveryScreen(onBack: () -> Unit = {}) {
    val remote = LocalRemote.current
    val p = pal
    val scope = rememberCoroutineScope()
    var chain by remember { mutableStateOf<RecoveryChain?>(null) }
    var loading by remember { mutableStateOf(true) }
    var newCode by remember { mutableStateOf("") }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    val t = now()

    LaunchedEffect(Unit) {
        loading = true
        chain = remote.pendingRecovery()
        loading = false
    }
    PageScaffold("恢复管理权") {
        Column(
            Modifier.fillMaxWidth()
                .verticalScroll(rememberScrollState())
                .imePadding()
                .padding(horizontal = 16.dp),
        ) {
            if (newCode.isNotEmpty()) {
                GLabel("新的恢复码 · 只显示这一次")
                Text(
                    newCode,
                    style = T.mono.copy(fontSize = 16.sp),
                    color = p.fg,
                    modifier = Modifier.fillMaxWidth().padding(vertical = 10.dp),
                )
                Hint("请抄下来或拍照保存。它只签恢复声明，不能解密内容。原恢复码已作废。", Modifier)
                Btn(
                    "我已保存",
                    { newCode = ""; onBack() },
                    Modifier.fillMaxWidth().padding(top = 12.dp),
                    BtnKind.PRIMARY,
                )
            } else if (loading) {
                Hint("正在读取…", Modifier.padding(vertical = 16.dp))
            } else {
                val c = chain
                if (c == null) {
                    Hint(
                        "现在没有等待处理的恢复。换手机时，在新手机上输入恢复码提交；提交后 48 小时内可以在这里否决。",
                        Modifier.padding(vertical = 12.dp),
                    )
                } else {
                    when (c.phase) {
                        GetRecoveryContextResponse.Phase.PHASE_PENDING,
                        GetRecoveryContextResponse.Phase.PHASE_READY -> {
                            Banner(
                                "有人正在用恢复码申请接管管理权",
                                Modifier.padding(bottom = 10.dp),
                                Tone2.ERROR,
                            )
                            GLabel("何时可以生效")
                            Hint(
                                if (c.readyMs > 0)
                                    dayText(c.readyMs, t) + "。不是你本人就现在否决；否决后这次恢复和它的新恢复码都作废。"
                                else "48 小时后。",
                                Modifier,
                            )
                            if (error.isNotEmpty())
                                Banner(error, Modifier.padding(vertical = 8.dp), Tone2.ERROR)
                            Btns {
                                Btn(
                                    if (busy) "正在否决…" else "否决这次恢复",
                                    {
                                        busy = true
                                        error = ""
                                        scope.launch {
                                            try {
                                                val code = remote.vetoRecovery()
                                                newCode = code
                                                chain = null
                                            } catch (e: Exception) {
                                                error = errorText(e)
                                            } finally {
                                                busy = false
                                            }
                                        }
                                    },
                                    Modifier.weight(1f),
                                    BtnKind.NEGATIVE,
                                    enabled = !busy,
                                )
                                Btn(
                                    "返回",
                                    onBack,
                                    Modifier.weight(1f),
                                    BtnKind.SECONDARY,
                                    enabled = !busy,
                                )
                            }
                        }
                        GetRecoveryContextResponse.Phase.PHASE_REJECTED ->
                            Hint("这次恢复已被否决。", Modifier.padding(vertical = 12.dp))
                        GetRecoveryContextResponse.Phase.PHASE_PUBLISHED ->
                            Hint("这次恢复已经完成，管理权已经转走。", Modifier.padding(vertical = 12.dp))
                        else -> Hint("恢复状态无法识别，请稍后再看。", Modifier.padding(vertical = 12.dp))
                    }
                }
                if (error.isNotEmpty())
                    Banner(error, Modifier.padding(vertical = 8.dp), Tone2.ERROR)
            }
        }
    }
}

/** 换手机: the new phone submits the old recovery code and shows the new one once. */
@Composable
fun RecoverScreen(app: RaApp, onBack: () -> Unit = {}) {
    val p = pal
    val scope = rememberCoroutineScope()
    val deviceSeed = remember { randomBytes(32) }
    var code by remember { mutableStateOf("") }
    var server by remember { mutableStateOf(app.lastServer.ifEmpty { "https://" }) }
    var submission by remember { mutableStateOf<RecoverySubmission?>(null) }
    var chain by remember { mutableStateOf<RecoveryChain?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    val t = now()

    fun checkedServer(s: String) = Secret.checkServer(s, allowPlainHttp = app.debuggable)

    PageScaffold("用恢复码找回管理权") {
        Column(
            Modifier.fillMaxWidth()
                .verticalScroll(rememberScrollState())
                .imePadding()
                .padding(horizontal = 16.dp),
        ) {
            val s = submission
            if (s == null) {
                Text(
                    "输入初始化时保存的恢复码。提交后要等 48 小时，原手机会收到通知；期间它一旦续期或否决，这次恢复就作废。",
                    style = T.sm,
                    color = p.muted,
                    modifier = Modifier.padding(vertical = 10.dp),
                )
                if (error.isNotEmpty())
                    Banner(error, Modifier.padding(bottom = 10.dp), Tone2.ERROR)
                RecoveryField("恢复码", code, { code = it }, "XXXX-XXXX-…", lines = 3)
                RecoveryField("服务器", server, { server = it.trim() }, "https://")
                Btn(
                    if (busy) "正在提交…" else "提交恢复",
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                val seed = RecoveryCode.decode(code)
                                    ?: throw IllegalArgumentException("恢复码不对")
                                val url = checkedServer(server)
                                val recoveryEd = SigningKey(seed).publicKey
                                val existing =
                                    withContext(Dispatchers.IO) {
                                        Recovery.context(url, app.http, recoveryEd)
                                    }
                                submission =
                                    if (
                                        existing.phase ==
                                            GetRecoveryContextResponse.Phase.PHASE_PENDING ||
                                            existing.phase ==
                                                GetRecoveryContextResponse.Phase.PHASE_READY
                                    ) {
                                        val saved = app.vault.loadPendingRecovery(existing.recoveryId)
                                        if (saved == null)
                                            throw IllegalStateException(
                                                "这次恢复已经提交过，新恢复码在提交时显示过；请在提交它的那台手机上完成",
                                            )
                                        RecoverySubmission(
                                            existing.recoveryId,
                                            existing.readyMs,
                                            "",
                                            saved.first,
                                            deviceId(SigningKey(saved.first).publicKey),
                                            saved.second,
                                            existing.accountId,
                                            existing.accountPublicKey,
                                        )
                                    } else {
                                        val r =
                                            withContext(Dispatchers.IO) {
                                                Recovery.submit(url, app.http, seed, deviceSeed)
                                            }
                                        app.vault.savePendingRecovery(
                                            r.recoveryId,
                                            r.deviceSeed,
                                            r.xPrivate,
                                        )
                                        r
                                    }
                                app.lastServer = url
                            } catch (e: Exception) {
                                error = errorText(e)
                            } finally {
                                busy = false
                            }
                        }
                    },
                    Modifier.fillMaxWidth().padding(top=8.dp),
                    BtnKind.PRIMARY,
                    enabled = !busy && code.isNotBlank() && server.length > 8,
                )
                TextAction("取消", { app.lastServer = server; onBack() }, alignment = Alignment.CenterStart)
            } else {
                if (s.newCode.isNotEmpty()) {
                    GLabel("新的恢复码 · 只显示这一次")
                    Text(
                        s.newCode,
                        style = T.mono.copy(fontSize = 16.sp),
                        color = p.fg,
                        modifier = Modifier.fillMaxWidth().padding(vertical = 10.dp),
                    )
                    Hint(
                        "48 小时后生效；如果原手机否决了这次恢复，它会作废。请现在抄下来或拍照保存。",
                        Modifier,
                    )
                } else {
                    Hint("这次恢复提交过了。新恢复码只在提交时显示一次。", Modifier)
                }
                GLabel("等待")
                Hint(
                    if (s.readyMs > 0) "可以生效的时间：" + dayText(s.readyMs, t) else "48 小时后才能完成。",
                    Modifier,
                )
                val c = chain
                if (c == null)
                    TextAction(
                        "查看状态",
                        {
                            scope.launch {
                                try {
                                    chain =
                                        withContext(Dispatchers.IO) {
                                            Recovery.context(
                                                checkedServer(server),
                                                app.http,
                                                ByteArray(0),
                                                s.recoveryId,
                                            )
                                        }
                                } catch (e: Exception) {
                                    error = errorText(e)
                                }
                            }
                        },
                        alignment = Alignment.CenterStart,
                    )
                else when (c.phase) {
                    GetRecoveryContextResponse.Phase.PHASE_READY ->
                        Hint("已经可以完成恢复。", Modifier)
                    GetRecoveryContextResponse.Phase.PHASE_PENDING ->
                        Hint("还在 48 小时等待期内。", Modifier)
                    GetRecoveryContextResponse.Phase.PHASE_REJECTED ->
                        Hint("这次恢复被原手机否决了。", Modifier)
                    GetRecoveryContextResponse.Phase.PHASE_PUBLISHED ->
                        Hint("这次恢复已经完成。", Modifier)
                    else -> {}
                }
                if (error.isNotEmpty())
                    Banner(error, Modifier.padding(vertical = 8.dp), Tone2.ERROR)
                Btn(
                    if (busy) "正在完成…" else "完成恢复",
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                val url = checkedServer(server)
                                val (acct, record) =
                                    withContext(Dispatchers.IO) {
                                        Recovery.complete(
                                            url,
                                            app.http,
                                            s.recoveryId,
                                            s.accountId,
                                            s.deviceSeed,
                                            s.xPrivate,
                                        )
                                    }
                                app.vault.clearPendingRecovery()
                                app.recovered(acct, record)
                            } catch (e: Exception) {
                                error = errorText(e)
                            } finally {
                                busy = false
                            }
                        }
                    },
                    Modifier.fillMaxWidth().padding(top = 10.dp),
                    BtnKind.PRIMARY,
                    enabled = !busy,
                )
                Hint(
                    "如果还没到 48 小时，或者原手机否决了，完成会失败并说明原因。",
                    Modifier.padding(top = 8.dp),
                )
            }
        }
    }
}

/** 新账户: create the root key, device identity and recovery code on this phone. */
@Composable
fun CreateAccountScreen(app: RaApp, onBack: () -> Unit = {}) {
    val p = pal
    val scope = rememberCoroutineScope()
    var server by remember { mutableStateOf(app.lastServer.ifEmpty { "https://" }) }
    var setupToken by remember { mutableStateOf("") }
    var created by remember { mutableStateOf<CreatedAccount?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }

    fun checkedServer(s: String) = Secret.checkServer(s, allowPlainHttp = app.debuggable)

    PageScaffold("创建新账户") {
        Column(
            Modifier.fillMaxWidth()
                .verticalScroll(rememberScrollState())
                .imePadding()
                .padding(horizontal = 16.dp),
        ) {
            val c = created
            if (c == null) {
                Text(
                    "这台手机会成为管理端。账户根密钥和设备身份只保存在手机上；恢复码只显示一次，请准备好纸笔或相机。",
                    style = T.sm,
                    color = p.muted,
                    modifier = Modifier.padding(vertical = 10.dp),
                )
                if (error.isNotEmpty())
                    Banner(error, Modifier.padding(bottom = 10.dp), Tone2.ERROR)
                RecoveryField("服务器", server, { server = it.trim() }, "https://")
                RecoveryField("Setup token（服务器要求时）", setupToken, { setupToken = it }, "可留空")
                Btn(
                    if (busy) "正在创建…" else "创建账户",
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                val url = checkedServer(server)
                                created =
                                    withContext(Dispatchers.IO) {
                                        createAccount(url, app.http, setupToken.trim())
                                    }
                                app.lastServer = url
                            } catch (e: Exception) {
                                error = errorText(e)
                            } finally {
                                busy = false
                            }
                        }
                    },
                    Modifier.fillMaxWidth().padding(top = 8.dp),
                    BtnKind.PRIMARY,
                    enabled = !busy && server.length > 8,
                )
                TextAction("返回", { onBack() }, alignment = Alignment.CenterStart)
            } else {
                GLabel("恢复码 · 只显示这一次")
                Text(
                    c.recoveryCode,
                    style = T.mono.copy(fontSize = 16.sp),
                    color = p.fg,
                    modifier = Modifier.fillMaxWidth().padding(vertical = 10.dp),
                )
                Hint(
                    "换手机时用它把管理权转过去；它只签恢复声明，不能解密内容。丢失后只能重建账户。",
                    Modifier,
                )
                if (error.isNotEmpty())
                    Banner(error, Modifier.padding(vertical = 8.dp), Tone2.ERROR)
                Btn(
                    "我已保存，开始使用",
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                app.created(c.account, c.record)
                            } catch (e: Exception) {
                                error = errorText(e)
                                busy = false
                            }
                        }
                    },
                    Modifier.fillMaxWidth().padding(top = 12.dp),
                    BtnKind.PRIMARY,
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
        textStyle = (if (lines > 1) T.mono else T.body).copy(color = p.fg),
        singleLine = lines == 1,
        minLines = lines,
        keyboardOptions = KeyboardOptions(keyboardType = if (lines == 1) KeyboardType.Uri else KeyboardType.Text),
        cursorBrush = androidx.compose.ui.graphics.SolidColor(p.blue),
        decorationBox = { inner ->
            FieldBox {
                Column {
                    Text(label, style = T.meta, color = p.muted, modifier = Modifier.padding(bottom = 2.dp))
                    androidx.compose.foundation.layout.Box {
                        if (value.isEmpty()) Text(placeholder, style = T.body, color = p.muted)
                        inner()
                    }
                }
            }
        },
    )
}
