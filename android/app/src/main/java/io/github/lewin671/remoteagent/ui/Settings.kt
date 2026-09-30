package io.github.lewin671.remoteagent.ui

import android.graphics.Bitmap
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowRight
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.FilterQuality
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.google.zxing.BarcodeFormat
import com.google.zxing.EncodeHintType
import com.google.zxing.qrcode.QRCodeWriter
import com.google.zxing.qrcode.decoder.ErrorCorrectionLevel
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.Initiation
import io.github.lewin671.remoteagent.core.Mode
import io.github.lewin671.remoteagent.core.PairException
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.machineAgents
import io.github.lewin671.remoteagent.core.modeOf
import io.github.lewin671.remoteagent.core.projectName
import io.github.lewin671.remoteagent.core.shortPath
import io.github.lewin671.remoteagent.core.whenText
import io.github.lewin671.remoteagent.data.Settings
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentDefaults
import io.github.lewin671.remoteagent.proto.DaemonSettings
import io.github.lewin671.remoteagent.proto.GetSettingsRequest
import io.github.lewin671.remoteagent.proto.RateLimit
import io.github.lewin671.remoteagent.proto.UpdateSettingsRequest
import io.github.lewin671.remoteagent.push.Push
import kotlinx.coroutines.launch
import java.time.Instant
import java.time.ZoneId

@Composable
fun SettingsScreen() {
    val app = RaApp.instance
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val settings by app.prefs.settings.collectAsState(Settings())
    val push by Push.status.collectAsState()
    val p = pal
    val scope = rememberCoroutineScope()
    var logout by remember { mutableStateOf(false) }
    val t = now()
    Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState())) {
        Text("设置", style = MaterialTheme.typography.displaySmall, modifier = Modifier.padding(start = 16.dp, top = 12.dp, bottom = 4.dp))
        ConnBanner(state)
        if (push.problem.isNotEmpty()) {
            Card(Modifier.padding(12.dp)) {
                Row(Modifier.padding(14.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(push.problem, Modifier.weight(1f), color = p.orangeInk)
                    if (push.action.isNotEmpty()) TextButton(onClick = { Push.fix(app, remote) }) { Text(push.action) }
                }
            }
        }
        Row(verticalAlignment = Alignment.CenterVertically) {
            SectionHeader("电脑", modifier = Modifier.weight(1f))
            TextButton(onClick = { nav.devices() }, Modifier.padding(end = 8.dp)) { Text("全部设备 ›") }
        }
        Card(Modifier.padding(horizontal = 12.dp)) {
            Column {
                for ((i, m) in state.machines.values.sortedBy { machineLabel(state, it.id) }.withIndex()) {
                    if (i > 0) HorizontalDivider(Modifier.padding(start = 16.dp), color = p.line)
                    Row44(onClick = { nav.device(m.id) }) {
                        Text("⌘", color = p.muted)
                        Column(Modifier.weight(1f)) {
                            Text(machineLabel(state, m.id), style = MaterialTheme.typography.titleSmall)
                            val used = m.meta?.takeIf { it.hasSecondaryLimit() }?.secondaryLimit?.usedPercent
                            Text(
                                (if (m.online) "在线" else whenText(m.lastSeenMs, t).ifEmpty { "不在线" }) + (used?.let { " · 已用 ${it.toInt()}%" } ?: ""),
                                style = MaterialTheme.typography.bodySmall, color = if (m.online) p.greenInk else p.muted,
                            )
                        }
                        Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = p.faint)
                    }
                }
                if (state.machines.isNotEmpty()) HorizontalDivider(Modifier.padding(start = 16.dp), color = p.line)
                Row44(onClick = { nav.pair() }) { Text("＋ 添加设备", color = p.blue, style = MaterialTheme.typography.titleSmall) }
            }
        }
        SectionHeader("客户端")
        Card(Modifier.padding(horizontal = 12.dp)) {
            Column {
                val me = remote.account.deviceId
                Row44(onClick = if (state.devicesSupported) ({ nav.client(me) }) else null) {
                    Text("▢", color = p.muted)
                    Column(Modifier.weight(1f)) {
                        Text(io.github.lewin671.remoteagent.core.clientName(state.devices[me], me), style = MaterialTheme.typography.titleSmall)
                        Text("当前客户端", style = MaterialTheme.typography.bodySmall, color = p.muted)
                    }
                }
                val others = state.devices.values.filter { it.kind == io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_CLIENT && it.id != me }
                for (c in others.sortedBy { it.createdMs }.take(3)) {
                    HorizontalDivider(Modifier.padding(start = 16.dp), color = p.line)
                    Row44(onClick = { nav.client(c.id) }) {
                        Text("▢", color = p.muted)
                        Column(Modifier.weight(1f)) {
                            Text(io.github.lewin671.remoteagent.core.clientName(c, c.id), style = MaterialTheme.typography.titleSmall)
                            Text("已配对", style = MaterialTheme.typography.bodySmall, color = p.muted)
                        }
                        Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = p.faint)
                    }
                }
                HorizontalDivider(Modifier.padding(start = 16.dp), color = p.line)
                if (state.devicesSupported) Row44(onClick = { nav.devices() }) { Text("全部客户端 ›", color = p.blue) }
                else Text("服务器还不支持设备目录，更新服务器后才能看到其他客户端", Modifier.padding(16.dp), style = MaterialTheme.typography.bodySmall, color = p.muted)
            }
        }
        SectionHeader("本客户端的通知")
        Card(Modifier.padding(horizontal = 12.dp)) {
            Column {
                Toggle("等我处理时", "审批、提问、计划、终端弹窗", settings.notifyAttention) { v -> scope.launch { app.prefs.update { it.copy(notifyAttention = v) }; Push.register(app, remote) } }
                HorizontalDivider(Modifier.padding(start = 16.dp), color = p.line)
                Toggle("一轮结束时", "", settings.notifyTurnEnded) { v -> scope.launch { app.prefs.update { it.copy(notifyTurnEnded = v) }; Push.register(app, remote) } }
                HorizontalDivider(Modifier.padding(start = 16.dp), color = p.line)
                Toggle(
                    "在通知里显示内容并直接审批",
                    "关着时通知只写「有会话在等你处理」，点开再审批。打开后审批的命令会进入系统通知，可能留在通知历史里；锁屏上仍只显示通用文案",
                    settings.showContent,
                ) { v -> scope.launch { app.prefs.update { it.copy(showContent = v) } } }
            }
        }
        SectionHeader("外观")
        SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth().padding(horizontal = 12.dp)) {
            val opts = listOf(ThemeMode.SYSTEM to "自动", ThemeMode.LIGHT to "亮色", ThemeMode.DARK to "暗色")
            opts.forEachIndexed { i, (mode, label) ->
                SegmentedButton(settings.theme == mode, { scope.launch { app.prefs.update { it.copy(theme = mode) } } }, SegmentedButtonDefaults.itemShape(i, opts.size), icon = {}) { Text(label) }
            }
        }
        SectionHeader("版本")
        Card(Modifier.padding(horizontal = 12.dp)) {
            Row44 {
                Text("remote-agent", Modifier.weight(1f))
                Text("${io.github.lewin671.remoteagent.BuildConfig.VERSION_NAME} · ${io.github.lewin671.remoteagent.BuildConfig.BUILD_COMMIT.take(7)}", color = p.muted)
            }
        }
        SectionHeader("账户")
        Card(Modifier.padding(horizontal = 12.dp)) {
            Column {
                Row44 {
                    Text("服务器", Modifier.weight(1f))
                    Text(remote.account.server.removePrefix("https://"), color = p.muted, style = MaterialTheme.typography.bodyMedium)
                }
                HorizontalDivider(Modifier.padding(start = 16.dp), color = p.line)
                Row44(onClick = { logout = true }) { Text("退出并删除本机密钥", color = p.red) }
            }
        }
        Spacer(Modifier.height(32.dp))
    }
    if (logout) Confirm(
        "退出并删除本机密钥？", "这台手机会忘掉账户密钥、设备密钥和缓存，要重新配对才能再用。电脑上的会话不受影响。", "退出", danger = true,
        onDismiss = { logout = false },
    ) { scope.launch { app.logout() } }
}

@Composable
private fun Toggle(title: String, sub: String, value: Boolean, onChange: (Boolean) -> Unit) {
    val p = pal
    Row44(onClick = { onChange(!value) }) {
        Column(Modifier.weight(1f)) {
            Text(title, style = MaterialTheme.typography.titleSmall)
            if (sub.isNotEmpty()) Text(sub, style = MaterialTheme.typography.bodySmall, color = p.muted)
        }
        Switch(value, onChange)
    }
}

/** 设备详情: usage windows, CLI versions, defaults for new sessions, timeout, projects, remove. */
@Composable
fun DeviceScreen(id: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val m = state.machines[id]
    val p = pal
    var confirm by remember { mutableStateOf(false) }
    val remove = rememberAction()
    var settings by remember { mutableStateOf<DaemonSettings?>(null) }
    var loadError by remember { mutableStateOf("") }
    val save = rememberAction()
    LaunchedEffect(id, m?.online) {
        if (m?.online != true) return@LaunchedEffect
        try {
            settings = remote.machineCommand(id, remote.cmd { getSettings = GetSettingsRequest.getDefaultInstance() }).getSettings.settings
            loadError = ""
        } catch (e: Exception) {
            loadError = errorText(e)
        }
    }
    var pickModel by remember { mutableStateOf<Agent?>(null) }
    fun update(s: DaemonSettings) = save.run({
        remote.machineCommand(id, remote.cmd { updateSettings = UpdateSettingsRequest.newBuilder().setSettings(s).build() })
    }) { r -> r?.updateSettings?.settings?.let { settings = it } }
    PageScaffold(machineLabel(state, id)) {
        if (m == null) {
            Text("这台设备已经移除了", Modifier.padding(16.dp), color = p.muted)
            return@PageScaffold
        }
        Column(Modifier.verticalScroll(rememberScrollState())) {
            Text(if (m.online) "在线" else "不在线 · 最后在线 ${whenText(m.lastSeenMs)}", Modifier.padding(horizontal = 16.dp), color = if (m.online) p.greenInk else p.muted)
            val meta = m.meta
            SectionHeader("账户额度")
            Card(Modifier.padding(horizontal = 12.dp), onClick = { nav.quota(id) }) {
                Column(Modifier.padding(vertical = 4.dp)) {
                    for (g in io.github.lewin671.remoteagent.core.quotaGroups(m, System.currentTimeMillis())) Row44 {
                        Text(g.title, Modifier.weight(1f))
                        Text(io.github.lewin671.remoteagent.core.quotaLine(g), color = p.muted, style = MaterialTheme.typography.bodyMedium)
                    }
                    Row44 { Text("查看全部额度 ›", color = p.blue) }
                }
            }
            if (meta != null) {
                SectionHeader("CLI 版本")
                Card(Modifier.padding(horizontal = 12.dp)) {
                    Column {
                        val agents = meta.agentsList.ifEmpty {
                            listOf(io.github.lewin671.remoteagent.proto.AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX).setVersion(meta.codexVersion).setVerified(meta.codexVerified).build())
                        }
                        for (a in agents) Row44 {
                            Text(agentName(a.agent), Modifier.weight(1f))
                            Text((if (a.verified) "已验证 · " else "未验证 · ") + a.version.ifEmpty { "未知" }, color = if (a.verified) p.muted else p.orangeInk, style = MaterialTheme.typography.bodyMedium)
                        }
                        if (agents.any { !it.verified }) Text("未验证的版本请在电脑上运行 remote-agent doctor", Modifier.padding(horizontal = 16.dp, vertical = 8.dp), style = MaterialTheme.typography.bodySmall, color = p.muted)
                    }
                }
            }
            SectionHeader("新会话默认设置")
            val s = settings
            when {
                !m.online -> Text("${machineLabel(state, id)} 不在线，连上后才能读取和修改", Modifier.padding(horizontal = 16.dp), color = p.muted)
                loadError.isNotEmpty() -> Text("没能读取设置：$loadError", Modifier.padding(horizontal = 16.dp), color = p.red)
                s == null -> Text("读取中…", Modifier.padding(horizontal = 16.dp), color = p.muted)
                else -> Card(Modifier.padding(horizontal = 12.dp)) {
                    Column(Modifier.padding(vertical = 4.dp)) {
                        for (agent in machineAgents(m)) {
                            val d = if (agent == Agent.AGENT_CLAUDE) s.claude else AgentDefaults.newBuilder().setModel(s.defaultModel).setEffort(s.defaultEffort).setMode(s.defaultMode).build()
                            Text(agentName(agent), Modifier.padding(start = 16.dp, top = 10.dp), style = MaterialTheme.typography.titleSmall)
                            Row44(onClick = if (save.running) null else ({ pickModel = agent })) {
                                Text("模型 · 推理强度", Modifier.weight(1f))
                                Text("${d.model.ifEmpty { "默认" }} · ${d.effort.ifEmpty { "默认" }} ›", color = p.muted, style = MaterialTheme.typography.bodyMedium)
                            }
                            // What a session started without a mode really gets, as the machine reads it.
                            val eff = defaultModeOf(m, agent)
                            val src = m.meta?.agentsList?.firstOrNull { io.github.lewin671.remoteagent.core.effectiveAgent(it.agent) == agent }?.defaultModeSource
                            Text(
                                "不指定时实际用：" + modeLabel(null, eff, agent).removePrefix("默认：") +
                                    when (src) {
                                        io.github.lewin671.remoteagent.proto.DefaultModeSource.DEFAULT_MODE_SOURCE_DAEMON_SETTINGS -> "（来自这里的设置）"
                                        io.github.lewin671.remoteagent.proto.DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG -> "（来自 ${agentName(agent)} 自己的配置）"
                                        else -> ""
                                    },
                                Modifier.padding(horizontal = 16.dp), style = MaterialTheme.typography.bodySmall,
                                color = if (eff.mode == Mode.FULL || !eff.known) p.red else p.muted,
                            )
                            Row(Modifier.padding(horizontal = 12.dp), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                                val cur = modeOf(d.mode)
                                FilterChip(cur == null, { update(withMode(s, agent, null)) }, { Text("默认") }, enabled = !save.running)
                                for (mode in listOf(Mode.ASK, Mode.AUTO)) FilterChip(cur == mode, { update(withMode(s, agent, mode)) }, { Text(mode.text) }, enabled = !save.running)
                            }
                        }
                        Text("审批超时", Modifier.padding(start = 16.dp, top = 10.dp), style = MaterialTheme.typography.titleSmall)
                        Row(Modifier.padding(horizontal = 12.dp), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                            for (min in listOf(2, 5, 10, 30)) FilterChip(
                                s.approvalTimeoutSeconds == min * 60, { update(s.toBuilder().setApprovalTimeoutSeconds(min * 60).build()) },
                                { Text("$min 分钟") }, enabled = !save.running,
                            )
                        }
                        if (save.error.isNotEmpty()) Text("没能保存：${save.error}", Modifier.padding(16.dp), color = p.red)
                    }
                }
            }
            val projects = state.sessions.values.filter { it.machineId == id }.mapNotNull { it.info?.cwd }.distinct()
            if (projects.isNotEmpty()) {
                SectionHeader("项目")
                Card(Modifier.padding(horizontal = 12.dp)) {
                    Column { projects.forEach { cwd -> Row44(onClick = { nav.project(cwd) }) { Column { Text(projectName(cwd)); Text(shortPath(cwd), style = MaterialTheme.typography.bodySmall, color = p.muted) } } } }
                }
            }
            Spacer(Modifier.height(16.dp))
            TextButton(onClick = { confirm = true }, Modifier.padding(horizontal = 8.dp)) { Text("移除这台机器…", color = p.red) }
            if (remove.error.isNotEmpty()) Text(remove.error, Modifier.padding(16.dp), color = p.red)
            Spacer(Modifier.height(24.dp))
        }
    }
    val cur = settings
    pickModel?.let { agent ->
        if (cur != null) DefaultModelSheet(id, agent, cur, onPick = { model, effort -> pickModel = null; update(withModel(cur, agent, model, effort)) }, onClose = { pickModel = null })
    }
    if (confirm) Confirm(
        "移除 ${machineLabel(state, id)}？", "它的会话记录会从服务器上删除，它要重新加入账户才能再连上。", "移除", danger = true,
        onDismiss = { confirm = false },
    ) { remove.run({ remote.removeDevice(id); null }) { nav.back() } }
}

private fun withModel(s: DaemonSettings, agent: Agent, model: String, effort: String): DaemonSettings =
    if (agent == Agent.AGENT_CLAUDE) s.toBuilder().setClaude(s.claude.toBuilder().setModel(model).setEffort(effort)).build()
    else s.toBuilder().setDefaultModel(model).setDefaultEffort(effort).build()

/** A CLI's default model and effort for new sessions on this machine, from the models it offers. */
@OptIn(androidx.compose.material3.ExperimentalMaterial3Api::class)
@Composable
private fun DefaultModelSheet(machineId: String, agent: Agent, s: DaemonSettings, onPick: (String, String) -> Unit, onClose: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    var models by remember { mutableStateOf<io.github.lewin671.remoteagent.proto.ListModelsResponse?>(null) }
    var error by remember { mutableStateOf("") }
    LaunchedEffect(machineId, agent) {
        try {
            models = remote.listModels(machineId, agent)
        } catch (e: Exception) {
            error = errorText(e)
        }
    }
    val d = if (agent == Agent.AGENT_CLAUDE) s.claude else AgentDefaults.newBuilder().setModel(s.defaultModel).setEffort(s.defaultEffort).build()
    var model by remember { mutableStateOf(d.model) }
    androidx.compose.material3.ModalBottomSheet(onDismissRequest = onClose, sheetState = androidx.compose.material3.rememberModalBottomSheetState(skipPartiallyExpanded = true)) {
        Column(Modifier.padding(horizontal = 16.dp).padding(bottom = 24.dp).verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("${agentName(agent)} · 新会话默认模型", style = MaterialTheme.typography.titleMedium)
            val ms = models
            when {
                error.isNotEmpty() -> Text("没能读取模型列表：$error", color = p.red)
                ms == null -> Text("读取中…", color = p.muted)
                else -> {
                    Row44(onClick = { onPick("", "") }) {
                        Text("默认（${agentName(agent)} 自己的配置）", Modifier.weight(1f))
                        if (model.isEmpty()) Text("✓", color = p.blue)
                    }
                    for (m in ms.modelsList) Row44(onClick = { if (m.effortsList.isEmpty()) onPick(m.slug, "") else model = m.slug }) {
                        Text(m.displayName.ifEmpty { m.slug }, Modifier.weight(1f))
                        if (m.effortsList.isEmpty()) Text("无推理强度", style = MaterialTheme.typography.bodySmall, color = p.faint)
                        if (m.slug == model) Text("  ✓", color = p.blue)
                    }
                    ms.modelsList.firstOrNull { it.slug == model }?.takeIf { it.effortsList.isNotEmpty() }?.let { m ->
                        Text("推理强度", style = MaterialTheme.typography.titleSmall)
                        Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                            for (e in m.effortsList) FilterChip(d.model == m.slug && d.effort == e, { onPick(m.slug, e) }, { Text(e) })
                        }
                    }
                }
            }
        }
    }
}

/**
 * 账户额度: each CLI's account usage on this machine, apart, as the daemon
 * reads it. What is not known is said, never shown as 0%; an offline
 * machine's or a failed read's numbers are the last record, and a reset
 * time that passed only waits for an update.
 */
@Composable
fun QuotaScreen(id: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val m = state.machines[id]
    val p = pal
    val t = now()
    PageScaffold("账户额度") {
        if (m == null) {
            Text("这台机器已经移除了", Modifier.padding(16.dp), color = p.muted)
            return@PageScaffold
        }
        val groups = io.github.lewin671.remoteagent.core.quotaGroups(m, t)
        Column(Modifier.verticalScroll(rememberScrollState())) {
            Text(machineLabel(state, id) + if (m.online) " · 在线" else " · 离线", Modifier.padding(horizontal = 16.dp), color = p.muted)
            if (!m.online) Text("机器离线，显示上次记录", Modifier.padding(16.dp), color = p.orangeInk)
            for (g in groups) {
                SectionHeader(g.title + if (g.lastRecord) " · 上次记录" else "")
                Card(Modifier.padding(horizontal = 12.dp)) {
                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        when (g.status) {
                            io.github.lewin671.remoteagent.core.QuotaStatus.UNSUPPORTED -> {
                                Text("暂不支持读取额度", style = MaterialTheme.typography.titleSmall)
                                Text(g.detail.ifEmpty { "这台电脑上的 remote-agent 读不到 ${agentName(g.agent)} 的额度。" } + if (g.agent == Agent.AGENT_CLAUDE) " 可在本机 Claude Code 中查看 /usage。" else "", style = MaterialTheme.typography.bodySmall, color = p.muted)
                            }
                            io.github.lewin671.remoteagent.core.QuotaStatus.WAITING -> {
                                Text("暂未获取额度", style = MaterialTheme.typography.titleSmall)
                                Text("等待 ${agentName(g.agent)} 返回额度数据", style = MaterialTheme.typography.bodySmall, color = p.muted)
                            }
                            io.github.lewin671.remoteagent.core.QuotaStatus.FAILED -> {
                                Text("没能读取 ${agentName(g.agent)} 额度" + if (g.detail.isNotEmpty()) "：${g.detail}" else "", style = MaterialTheme.typography.titleSmall, color = p.orangeInk)
                                if (g.rows.isEmpty()) Text("暂未获取额度", style = MaterialTheme.typography.bodySmall, color = p.muted)
                            }
                            else -> {}
                        }
                        // Used up or close to it; nothing switches CLI or model by itself.
                        io.github.lewin671.remoteagent.core.quotaBanner(g).takeIf { it.isNotEmpty() }?.let {
                            Text("$it，不会自动切换 CLI 或模型", style = MaterialTheme.typography.bodyMedium, color = p.orangeInk)
                        }
                        for (r in g.rows) {
                            Row {
                                Text(r.label, Modifier.weight(1f), style = MaterialTheme.typography.titleSmall)
                                Text(
                                    r.used,
                                    color = when (r.tone) {
                                        io.github.lewin671.remoteagent.core.QuotaTone.FULL, io.github.lewin671.remoteagent.core.QuotaTone.WARN -> p.orangeInk
                                        io.github.lewin671.remoteagent.core.QuotaTone.STALE -> p.muted
                                        else -> p.fg2
                                    },
                                )
                            }
                            r.percent?.let { pc ->
                                LinearProgressIndicator(
                                    progress = { (pc / 100).toFloat().coerceIn(0f, 1f) }, Modifier.fillMaxWidth(),
                                    color = when (r.tone) {
                                        io.github.lewin671.remoteagent.core.QuotaTone.STALE -> p.gray
                                        io.github.lewin671.remoteagent.core.QuotaTone.NORMAL -> p.blue
                                        else -> p.orange
                                    },
                                    trackColor = p.track,
                                )
                            }
                            val sub = listOf(r.reset, r.note).filter { it.isNotEmpty() }.joinToString(" · ")
                            if (sub.isNotEmpty()) Text(sub, style = MaterialTheme.typography.bodySmall, color = p.muted)
                        }
                        io.github.lewin671.remoteagent.core.quotaPartial(g).takeIf { it.isNotEmpty() }?.let { Text(it, style = MaterialTheme.typography.bodySmall, color = p.muted) }
                        if (g.received.isNotEmpty()) Text(g.received, style = MaterialTheme.typography.bodySmall, color = p.faint)
                        if (g.agent == Agent.AGENT_CLAUDE && g.status != io.github.lewin671.remoteagent.core.QuotaStatus.OK) {
                            val claudeSession = state.sessions.values.filter { it.machineId == id && it.info?.agent == Agent.AGENT_CLAUDE && !io.github.lewin671.remoteagent.core.exited(it) }.maxByOrNull { it.updatedMs }
                            if (claudeSession != null) TextButton(onClick = { nav.sessionTerminal(claudeSession.id) }, enabled = m.online) { Text("去终端查看 /usage ›") }
                            else Text("没有进行中的 Claude Code 会话：先打开一个会话再去终端查看。", style = MaterialTheme.typography.bodySmall, color = p.muted)
                        }
                    }
                }
            }
            Text("额度属于 CLI 登录账户，该账户的其他使用也可能计入。", Modifier.padding(16.dp), style = MaterialTheme.typography.bodySmall, color = p.muted)
        }
    }
}

private fun withMode(s: DaemonSettings, agent: Agent, mode: Mode?): DaemonSettings {
    val m = mode?.proto ?: io.github.lewin671.remoteagent.proto.ApprovalMode.APPROVAL_MODE_UNSPECIFIED
    return if (agent == Agent.AGENT_CLAUDE) s.toBuilder().setClaude(s.claude.toBuilder().setMode(m)).build()
    else s.toBuilder().setDefaultMode(m).build()
}

fun qrBitmap(text: String, size: Int = 512): Bitmap {
    val m = QRCodeWriter().encode(text, BarcodeFormat.QR_CODE, size, size, mapOf(EncodeHintType.ERROR_CORRECTION to ErrorCorrectionLevel.M, EncodeHintType.MARGIN to 1))
    val px = IntArray(size * size) { i -> if (m.get(i % size, i / size)) 0xFF000000.toInt() else 0xFFFFFFFF.toInt() }
    return Bitmap.createBitmap(px, size, size, Bitmap.Config.ARGB_8888)
}

/** The web client's address for the QR code: app.<domain> next to ra.<domain>. */
fun webOrigin(server: String, pref: String): String {
    if (pref.isNotEmpty()) return pref
    val u = java.net.URI(server)
    val host = u.host ?: return server
    return if (host.startsWith("ra.")) "https://app." + host.removePrefix("ra.") else server
}

/** 添加设备: show a code and QR, compare emoji, then hand over the root key. */
@Composable
fun PairScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val settings by RaApp.instance.prefs.settings.collectAsState(Settings())
    val p = pal
    val scope = rememberCoroutineScope()
    var init by remember { mutableStateOf<Initiation?>(null) }
    var emoji by remember { mutableStateOf<List<String>?>(null) }
    var phase by remember { mutableStateOf("start") } // start, wait, emoji, sending, done, error
    var error by remember { mutableStateOf("") }
    var attempt by remember { mutableStateOf(0) }
    LaunchedEffect(attempt) {
        phase = "start"
        emoji = null
        error = ""
        try {
            val i = Initiation.start(remote)
            init = i
            phase = "wait"
            val (e, _) = i.joined()
            emoji = e
            phase = "emoji"
        } catch (e: Exception) {
            error = if (e is PairException) e.message.orEmpty() else errorText(e)
            phase = "error"
        }
    }
    DisposableEffect(Unit) { onDispose { init?.let { i -> if (phase != "done") scope.launch { i.cancelOnServer() } } } }
    PageScaffold("添加设备") {
        Column(Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(20.dp), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(14.dp)) {
            val i = init
            when (phase) {
                "start" -> CircularProgressIndicator()
                "wait" -> if (i != null) {
                    Text(i.code.chunked(4).joinToString(" "), fontSize = 30.sp, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.displaySmall)
                    val bmp = remember(i) { qrBitmap(i.link.url(webOrigin(remote.account.server, settings.webOrigin))) }
                    Image(bmp.asImageBitmap(), "配对二维码", Modifier.size(240.dp).background(androidx.compose.ui.graphics.Color.White, RoundedCornerShape(8.dp)).padding(8.dp), filterQuality = FilterQuality.None)
                    Text("在新设备上扫码，或输入这 8 位配对码。5 分钟内有效。", color = p.muted, textAlign = TextAlign.Center)
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        CircularProgressIndicator(Modifier.size(16.dp), strokeWidth = 2.dp)
                        Text("  等待新设备…", color = p.muted)
                    }
                }
                "emoji", "sending" -> {
                    Text("新设备请求加入", style = MaterialTheme.typography.titleMedium)
                    Text(emoji.orEmpty().joinToString("  "), fontSize = 40.sp)
                    Text("和新设备上的 4 个图案一模一样才允许", color = p.muted, textAlign = TextAlign.Center)
                    Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                        OutlinedButton(onClick = { scope.launch { i?.cancelOnServer() }; nav.back() }, enabled = phase == "emoji", modifier = Modifier.heightIn(min = 48.dp)) { Text("拒绝") }
                        Button(onClick = {
                            phase = "sending"
                            scope.launch {
                                try {
                                    i?.approve()
                                    phase = "done"
                                } catch (e: Exception) {
                                    error = if (e is PairException) e.message.orEmpty() else "不确定新设备收到没有，到新设备上看一眼是否已加入：" + errorText(e)
                                    phase = "error"
                                }
                            }
                        }, enabled = phase == "emoji", modifier = Modifier.heightIn(min = 48.dp)) { Text(if (phase == "sending") "正在把密钥交给新设备…" else "图案一致，允许") }
                    }
                }
                "done" -> {
                    Text("✓ 新设备已加入", style = MaterialTheme.typography.titleMedium, color = p.greenInk)
                    Button(onClick = nav.back) { Text("完成") }
                }
                else -> {
                    Text(error, color = p.red, textAlign = TextAlign.Center)
                    Button(onClick = { attempt++ }) { Text("换一个") }
                }
            }
        }
    }
}
