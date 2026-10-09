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
import androidx.compose.ui.graphics.vector.ImageVector
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
import io.github.lewin671.remoteagent.proto.ImageStorageUsage
import io.github.lewin671.remoteagent.proto.DaemonSettings
import io.github.lewin671.remoteagent.proto.GetSettingsRequest
import io.github.lewin671.remoteagent.proto.RateLimit
import io.github.lewin671.remoteagent.proto.UpdateSettingsRequest
import io.github.lewin671.remoteagent.push.Push
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.coroutines.launch
import java.time.Instant
import java.time.ZoneId
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.selection.selectable
import androidx.compose.ui.semantics.Role
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.TextStyle

/** A switch as the mockup draws it: 42 × 25, green when on. */
@Composable
fun Sw(on: Boolean) {
    val p = pal
    Box(Modifier.size(width = 42.dp, height = 25.dp).clip(RoundedCornerShape(13.dp)).background(if (on) p.green else p.track).padding(2.dp), contentAlignment = if (on) Alignment.CenterEnd else Alignment.CenterStart) {
        Box(Modifier.size(21.dp).clip(CircleShape).background(Color.White))
    }
}

/** A row with a switch; [sub] explains it under the title. */
@Composable
private fun ToggleRow(title: String, sub: String, value: Boolean, onChange: (Boolean) -> Unit) {
    val p = pal
    Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { onChange(!value) }.padding(horizontal = 14.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
        Column(Modifier.weight(1f)) {
            Text(title, style = T.body, color = p.fg)
            if (sub.isNotEmpty()) Text(sub, style = T.meta, color = p.muted)
        }
        Sw(value)
    }
}

/** A small colored square with an icon, in front of a machine's row. */
@Composable
private fun Ico(icon: ImageVector, bg: Color) {
    Box(Modifier.size(26.dp).clip(RoundedCornerShape(7.dp)).background(bg), contentAlignment = Alignment.Center) { Ic(icon, 16.dp, Color.White) }
}

/** 设置 (client-v7): computers, clients, notifications, this phone's storage, appearance and the account, on a grey page. */
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
    var storage by remember { mutableStateOf(false) }
    var licenses by remember { mutableStateOf(false) }
    val t = now()
    if (licenses) {
        androidx.activity.compose.BackHandler { licenses = false }
        LicensesPage { licenses = false }
        return
    }
    if (storage) {
        androidx.activity.compose.BackHandler { storage = false }
        PhoneImageStoragePage { storage = false }
        return
    }
    androidx.compose.runtime.CompositionLocalProvider(LocalOnSoft provides true) {
    Column(Modifier.fillMaxSize().background(p.soft).verticalScroll(rememberScrollState()).readableColumn()) {
        LargeTitle("设置")
        ConnBanner(state)
        GLabel("电脑", "全部设备", onAction = { nav.devices() })
        Group(rows = state.machines.values.sortedBy { machineLabel(state, it.id) }.map { m -> @Composable {
            Row(Modifier.fillMaxWidth().clickable { nav.device(m.id) }.padding(horizontal = 14.dp, vertical = 10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                Ico(Lucide.Laptop, p.blueBg)
                Column(Modifier.weight(1f)) {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text(machineLabel(state, m.id), style = T.body, color = p.fg)
                        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(5.dp)) {
                            Dot(if (m.online) p.green else p.gray)
                            Text(if (m.online) "在线" else whenText(m.lastSeenMs, t).ifEmpty { "不在线" }, style = T.sm, color = p.muted)
                        }
                    }
                    val q = io.github.lewin671.remoteagent.core.quotaGroups(m, t).firstOrNull()?.let { g -> g.rows.lastOrNull { it.percent != null }?.let { "${agentName(g.agent)} · ${it.label}${it.used}" } }
                    if (q != null) Text(q, style = T.sm, color = p.muted, modifier = Modifier.padding(top = 2.dp))
                }
                Arrow(p.muted, T.body)
            }
        } } + listOf(@Composable { GRow("添加设备", onClick = { nav.pair() }, action = true, icon = Lucide.Plus) }))
        GLabel("客户端")
        val me = remote.account.deviceId
        val others = state.devices.values.filter { it.kind == io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_CLIENT && it.id != me }.sortedBy { it.createdMs }.take(3)
        Group(rows = listOf<@Composable () -> Unit>(
            { GRow(io.github.lewin671.remoteagent.core.clientName(state.devices[me], me), "当前", onClick = { nav.client(me) }) },
        ) + others.map { c -> @Composable { GRow(io.github.lewin671.remoteagent.core.clientName(c, c.id), "已配对", onClick = { nav.client(c.id) }, chevron = true) } } + listOf(
            @Composable { GRow("全部客户端", onClick = { nav.devices() }, action = true, chevron = true) },
        ))
        GLabel("本客户端的通知")
        Group(rows = listOf(
            { ToggleRow("等我处理时", "", settings.notifyAttention) { v -> scope.launch { app.prefs.update { it.copy(notifyAttention = v) }; Push.register(app, remote) } } },
            { ToggleRow("一轮结束时", "", settings.notifyTurnEnded) { v -> scope.launch { app.prefs.update { it.copy(notifyTurnEnded = v) }; Push.register(app, remote) } } },
            {
                ToggleRow(
                    "在通知里显示内容并直接审批",
                    "打开后审批的命令会进入系统通知，可能留在通知历史里；锁屏上仍只显示通用文案",
                    settings.showContent,
                ) { v -> scope.launch { app.prefs.update { it.copy(showContent = v) } } }
            },
        ))
        PushNotice(push) { Push.fix(app, remote) }
        GLabel("本机存储")
        Group(rows = listOf { GRow("图片存储", onClick = { storage = true }, chevron = true) })
        GLabel("外观")
        Group(rows = listOf {
            val opts = listOf(ThemeMode.SYSTEM to "自动", ThemeMode.LIGHT to "亮色", ThemeMode.DARK to "暗色")
            Row(Modifier.fillMaxWidth().heightIn(min = 50.dp).padding(horizontal = 14.dp), verticalAlignment = Alignment.CenterVertically) {
                Text("主题", style = T.body, color = pal.fg, modifier = Modifier.weight(1f))
                SegPick(opts.map { it.second }, opts.indexOfFirst { it.first == settings.theme }) { i -> scope.launch { app.prefs.update { it.copy(theme = opts[i].first) } } }
            }
        })
        GLabel("账户")
        Group(rows = listOf(
            { GRow("服务器", remote.account.server.removePrefix("https://")) },
            { GRow("版本", app.versionName.let { v -> val c = app.buildCommit.take(7); if (c.isEmpty() || c in v) v else "$v · $c" }) },
            { GRow("开源许可", onClick = { licenses = true }, chevron = true) },
            { GRow("退出并删除本机密钥", onClick = { logout = true }, danger = true) },
        ))
        Spacer(Modifier.height(24.dp))
    }
    }
    if (logout) Confirm(
        "退出并删除本机密钥？", "这台设备会退出账户，重新使用时需要配对加入。电脑上的会话继续运行，其他设备不受影响。", "退出", danger = true,
        onDismiss = { logout = false },
    ) { scope.launch { app.logout() } }
}

/** Optional background delivery belongs with notification settings, not the app's connection banner. */
@Composable
internal fun PushNotice(push: io.github.lewin671.remoteagent.push.PushStatus, onAction: () -> Unit) {
    if (push.problem.isEmpty()) return
    Column(Modifier.padding(horizontal = 20.dp).padding(top = 8.dp)) {
        Text(push.problem, style = T.meta, color = pal.muted)
        Text("仅影响 App 在后台时的系统通知，不影响聊天和审批", style = T.meta, color = pal.muted)
        if (push.action.isNotEmpty()) TextButton(onClick = onAction) {
            Text(push.action, style = T.meta, color = pal.blue)
        }
    }
}

/** One setting's few values at the right of its row (主题): a grey capsule with the chosen one raised. */
@Composable
fun SegPick(items: List<String>, selected: Int, onSelect: (Int) -> Unit) {
    val p = pal
    Row(Modifier.clip(Radii.SM).background(p.chip).padding(2.dp), horizontalArrangement = Arrangement.spacedBy(2.dp)) {
        items.forEachIndexed { i, s ->
            val on = i == selected
            Box(
                Modifier.widthIn(min = 56.dp).heightIn(min = 32.dp).clip(RoundedCornerShape(8.dp))
                    .background(if (on) (if (p.card == Color.White) p.card else p.track) else Color.Transparent)
                    .selectable(selected = on, role = Role.RadioButton) { onSelect(i) }.padding(horizontal = 10.dp),
                contentAlignment = Alignment.Center,
            ) { Text(s, style = if (on) T.smB else T.sm, color = if (on) p.fg else p.muted) }
        }
    }
}

/** A choice from a few options in a sheet: a mode, a timeout, a limit. */
@Composable
private fun ChoiceSheet(title: String, options: List<Pair<String, String?>>, selected: Int, onPick: (Int) -> Unit, onClose: () -> Unit) {
    Sheet(onClose) {
        SheetHead(title)
        options.forEachIndexed { i, (label, desc) -> ListItem(label, desc, checked = i == selected, last = i == options.size - 1, onClick = { onPick(i) }) }
    }
}

/** 设备详情 (client-v7): quota, CLI versions, the defaults for new sessions, image storage, projects, remove. */
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
    var imageStorage by remember { mutableStateOf<ImageStorageUsage?>(null) }
    var loadError by remember { mutableStateOf("") }
    val save = rememberAction()
    var attempt by remember { mutableStateOf(0) }
    LaunchedEffect(id, m?.online, attempt) {
        if (m?.online != true) return@LaunchedEffect
        try {
            val reply = remote.machineCommand(id, remote.cmd { getSettings = GetSettingsRequest.getDefaultInstance() }).getSettings
            settings = reply.settings
            if (reply.hasImageStorage()) imageStorage = reply.imageStorage
            loadError = ""
        } catch (e: Exception) {
            loadError = errorText(e)
        }
    }
    var pickModel by remember { mutableStateOf<Agent?>(null) }
    var pickMode by remember { mutableStateOf<Agent?>(null) }
    var pickTimeout by remember { mutableStateOf(false) }
    fun update(s: DaemonSettings, cleanImages: Boolean = false) = save.run({
        remote.machineCommand(id, remote.cmd { updateSettings = UpdateSettingsRequest.newBuilder().setSettings(s).setCleanImages(cleanImages).build() })
    }) { r -> r?.updateSettings?.let { settings = it.settings; if (it.hasImageStorage()) imageStorage = it.imageStorage } }
    PageScaffold(machineLabel(state, id), gray = true) {
        if (m == null) {
            PageNote("这台设备已经移除了")
            return@PageScaffold
        }
        Column(Modifier.verticalScroll(rememberScrollState())) {
            if (!m.online) Banner("不在线 · 最后在线 ${whenText(m.lastSeenMs)}", Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp))
            val meta = m.meta
            val groups = io.github.lewin671.remoteagent.core.quotaGroups(m, System.currentTimeMillis())
            // A line for each CLI's quota, leading to all of it.
            GLabel("账户额度")
            Group(rows = groups.map { g -> @Composable { GRow(g.title.substringBefore(" · "), io.github.lewin671.remoteagent.core.quotaLine(g), onClick = { nav.quota(id) }, chevron = true) } })
            if (meta != null) {
                val agents = meta.agentsList.ifEmpty {
                    listOf(io.github.lewin671.remoteagent.proto.AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX).setVersion(meta.codexVersion).setVerified(meta.codexVerified).build())
                }
                Group(rows = agents.map { a -> @Composable {
                    GRow("${agentName(a.agent)} 版本", trailing = {
                        Text(
                            androidx.compose.ui.text.buildAnnotatedString {
                                if (!a.verified) { pushStyle(androidx.compose.ui.text.SpanStyle(color = p.orangeInk)); append("未验证 · "); pop() }
                                append(a.version.ifEmpty { "未知" })
                            },
                            style = T.sm, color = p.muted,
                        )
                    })
                } })
                if (agents.any { !it.verified }) Hint("未验证的版本请在电脑上运行 remote-agent doctor", Modifier.padding(horizontal = 30.dp).padding(bottom = 12.dp))
            }
            val s = settings
            when {
                !m.online -> Hint("${machineLabel(state, id)} 不在线，连上后才能读取和修改新会话默认设置", Modifier.padding(horizontal = 30.dp).padding(bottom = 12.dp))
                loadError.isNotEmpty() -> Banner("没能读取设置：$loadError", Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp), Tone2.ERROR, action = "重试", onAction = { attempt++ })
                s == null -> Hint("读取中…", Modifier.padding(horizontal = 30.dp).padding(bottom = 12.dp))
                else -> for (agent in machineAgents(m)) {
                    val d = if (agent == Agent.AGENT_CLAUDE) s.claude else AgentDefaults.newBuilder().setModel(s.defaultModel).setEffort(s.defaultEffort).setMode(s.defaultMode).build()
                    // What a session started without a mode really gets, as the machine reads it.
                    val eff = defaultModeOf(m, agent)
                    val cur = modeOf(d.mode)
                    val timeoutRow: @Composable () -> Unit = { GRow("审批超时", "${s.approvalTimeoutSeconds / 60} 分钟", onClick = if (save.running) null else ({ pickTimeout = true }), chevron = true) }
                    GLabel("${agentName(agent)} · 新项目默认设置")
                    Group(rows = listOfNotNull<@Composable () -> Unit>(
                        @Composable { GRow("模型", d.model.ifEmpty { "默认" }, onClick = if (save.running) null else ({ pickModel = agent }), chevron = true) },
                        @Composable { GRow("推理强度", d.effort.ifEmpty { "默认" }, onClick = if (save.running) null else ({ pickModel = agent }), chevron = true) },
                        @Composable {
                            // One line: that project settings may differ is said in the picker.
                            GRow(
                                "审批模式", cur?.text ?: modeLabel(null, eff.copy(varies = false), agent), onClick = if (save.running) null else ({ pickMode = agent }),
                                valueColor = if (cur == null && (eff.mode == Mode.FULL || !eff.known)) p.red else null, chevron = true,
                            )
                        },
                        timeoutRow.takeIf { agent == Agent.AGENT_CODEX || machineAgents(m).size == 1 },
                    ))
                }
            }
            if (save.error.isNotEmpty()) Banner("没能保存：${save.error}", Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp), Tone2.ERROR)
            settings?.let { s ->
                MachineImageStorage(s, imageStorage, m.online && !save.running, onChange = { next, clean -> update(next, clean) })
            }
            val projects = io.github.lewin671.remoteagent.core.machineProjects(state, id)
            if (projects.isNotEmpty()) {
                GLabel("项目")
                Group(rows = projects.map { cwd -> @Composable { GRow(projectName(cwd), shortPath(cwd), onClick = { nav.project(cwd) }) } })
            }
            Group(rows = listOf { GRow("移除这台机器…", onClick = { confirm = true }, danger = true) })
            Text("移除会删除这台机器在服务器上的会话记录，并断开连接；它需要重新加入账户才能连接。", style = T.sm, color = p.muted, modifier = Modifier.padding(horizontal = 16.dp))
            if (remove.error.isNotEmpty()) Banner(remove.error, Modifier.padding(16.dp), Tone2.ERROR)
            Spacer(Modifier.height(24.dp))
        }
    }
    val cur = settings
    pickModel?.let { agent ->
        if (cur != null) DefaultModelSheet(id, agent, cur, onPick = { model, effort -> pickModel = null; update(withModel(cur, agent, model, effort)) }, onClose = { pickModel = null })
    }
    pickMode?.let { agent ->
        if (cur != null) {
            val eff = defaultModeOf(m, agent)
            val d = if (agent == Agent.AGENT_CLAUDE) cur.claude.mode else cur.defaultMode
            val opts = listOf(null, Mode.ASK, Mode.AUTO)
            ChoiceSheet(
                "${agentName(agent)} · 审批模式",
                opts.map { mode -> (mode?.text ?: "默认") to (if (mode == null) "不指定时实际用：" + modeLabel(null, eff, agent).removePrefix("默认：") else modeHelp.getValue(mode).replace("CLI", agentName(agent))) },
                opts.indexOf(modeOf(d)).coerceAtLeast(0),
                onPick = { i -> pickMode = null; update(withMode(cur, agent, opts[i])) },
                onClose = { pickMode = null },
            )
        }
    }
    if (pickTimeout && cur != null) {
        val mins = listOf(2, 5, 10, 30)
        ChoiceSheet("审批超时", mins.map { "$it 分钟" to null }, mins.indexOf(cur.approvalTimeoutSeconds / 60), onPick = { i ->
            pickTimeout = false
            update(cur.toBuilder().setApprovalTimeoutSeconds(mins[i] * 60).build())
        }, onClose = { pickTimeout = false })
    }
    if (confirm) Confirm(
        "移除 ${machineLabel(state, id)}？", "它的会话记录会从服务器上删除，它要重新加入账户才能再连上。", "移除", danger = true,
        onDismiss = { confirm = false },
    ) { remove.run({ remote.removeDevice(id); null }) { nav.back() } }
}

/** One usage window: its name, how much is used, a bar, and when it resets. */
@Composable
private fun QuotaRowView(r: io.github.lewin671.remoteagent.core.QuotaRow, received: String = "", compact: Boolean = false) {
    val p = pal
    Column(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 10.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(r.label, style = T.body, color = p.fg, modifier = Modifier.weight(1f))
            Text(
                if (compact && r.reset.isNotEmpty()) "${r.used} · ${r.reset}" else r.used, style = T.sm,
                color = when (r.tone) {
                    io.github.lewin671.remoteagent.core.QuotaTone.FULL, io.github.lewin671.remoteagent.core.QuotaTone.WARN -> p.orangeInk
                    else -> p.muted
                },
            )
        }
        r.percent?.let { pc ->
            Box(Modifier.padding(top = 6.dp).fillMaxWidth().height(6.dp).clip(RoundedCornerShape(3.dp)).background(p.track)) {
                Box(
                    Modifier.fillMaxWidth((pc / 100).toFloat().coerceIn(0f, 1f)).height(6.dp).clip(RoundedCornerShape(3.dp)).background(
                        when (r.tone) {
                            io.github.lewin671.remoteagent.core.QuotaTone.STALE -> p.gray
                            io.github.lewin671.remoteagent.core.QuotaTone.NORMAL -> p.blueBg
                            else -> p.orange
                        },
                    ),
                )
            }
        }
        val sub = if (compact) "" else listOf(r.reset, r.note, received).filter { it.isNotEmpty() }.joinToString(" · ")
        if (sub.isNotEmpty()) Text(sub, style = T.meta, color = p.muted, modifier = Modifier.padding(top = 8.dp))
    }
}

private fun withModel(s: DaemonSettings, agent: Agent, model: String, effort: String): DaemonSettings =
    if (agent == Agent.AGENT_CLAUDE) s.toBuilder().setClaude(s.claude.toBuilder().setModel(model).setEffort(effort)).build()
    else s.toBuilder().setDefaultModel(model).setDefaultEffort(effort).build()

/** A CLI's default model and effort for new sessions on this machine, from the models it offers. */
@Composable
private fun DefaultModelSheet(machineId: String, agent: Agent, s: DaemonSettings, onPick: (String, String) -> Unit, onClose: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    var models by remember { mutableStateOf<io.github.lewin671.remoteagent.proto.ListModelsResponse?>(null) }
    var error by remember { mutableStateOf("") }
    LaunchedEffect(machineId, agent) {
        try {
            models = remote.listModels(machineId, agent, fresh = true)
        } catch (e: Exception) {
            error = errorText(e)
        }
    }
    val d = if (agent == Agent.AGENT_CLAUDE) s.claude else AgentDefaults.newBuilder().setModel(s.defaultModel).setEffort(s.defaultEffort).build()
    var model by remember { mutableStateOf(d.model) }
    Sheet(onClose) {
        SheetHead("${agentName(agent)} · 新会话默认模型")
        val ms = models
        when {
            error.isNotEmpty() -> Banner("没能读取模型列表：$error", tone = Tone2.ERROR)
            ms == null -> Text("读取中…", style = T.sm, color = p.muted, modifier = Modifier.padding(vertical = 12.dp))
            else -> {
                ListItem("默认（${agentName(agent)} 自己的配置）", checked = model.isEmpty(), onClick = { onPick("", "") })
                ms.modelsList.forEachIndexed { i, m ->
                    ListItem(m.displayName.ifEmpty { m.slug }, small = listOfNotNull(io.github.lewin671.remoteagent.core.contextText(m.contextWindow), if (m.effortsList.isEmpty()) "无推理强度" else null).joinToString(" · ").ifEmpty { null }, checked = m.slug == model, last = i == ms.modelsCount - 1,
                        onClick = { if (m.effortsList.isEmpty()) onPick(m.slug, "") else model = m.slug })
                }
                ms.modelsList.firstOrNull { it.slug == model }?.takeIf { it.effortsList.isNotEmpty() }?.let { m ->
                    FieldLabel("推理强度")
                    ChoiceGrid(m.effortsList, if (d.model == m.slug) d.effort else null, true) { onPick(m.slug, it) }
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
    val groups = m?.let { io.github.lewin671.remoteagent.core.quotaGroups(it, t) }.orEmpty()
    PageScaffold("账户额度", subtitle = m?.let { machineLabel(state, id) + " · " + if (it.online) "在线" else "离线" }, gray = true) {
        if (m == null) {
            PageNote("这台机器已经移除了")
            return@PageScaffold
        }
        Column(Modifier.verticalScroll(rememberScrollState())) {
            if (!m.online) Text("机器离线，显示上次记录", style = T.sm, color = p.orangeInk, modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 6.dp))
            for (g in groups) {
                if (g.status == io.github.lewin671.remoteagent.core.QuotaStatus.FAILED) Text("没能读取 ${agentName(g.agent)} 额度" + if (g.detail.isNotEmpty()) "：${g.detail}" else "", style = T.sm, color = p.red, modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 6.dp))
                GLabel(g.title + if (g.lastRecord) " · 上次记录" else "")
                val rows = mutableListOf<@Composable () -> Unit>()
                when (g.status) {
                    io.github.lewin671.remoteagent.core.QuotaStatus.UNSUPPORTED -> rows += @Composable {
                        Column(Modifier.padding(horizontal = 14.dp, vertical = 10.dp)) {
                            Text("暂不支持读取额度", style = T.bodyB, color = p.fg)
                            Text(g.detail.ifEmpty { "这台电脑上的 remote-agent 读不到 ${agentName(g.agent)} 的额度。" } + if (g.agent == Agent.AGENT_CLAUDE) " 可在本机 Claude Code 中查看 /usage。" else "", style = T.meta, color = p.muted)
                        }
                    }
                    io.github.lewin671.remoteagent.core.QuotaStatus.WAITING -> rows += @Composable {
                        Column(Modifier.padding(horizontal = 14.dp, vertical = 10.dp)) {
                            Text("暂未获取额度", style = T.bodyB, color = p.fg)
                            Text("等待 ${agentName(g.agent)} 返回额度数据", style = T.meta, color = p.muted)
                        }
                    }
                    else -> {}
                }
                for (r in g.rows) rows += @Composable { QuotaRowView(r) }
                if (rows.isNotEmpty()) Group(rows = rows)
                // When it was heard, once for the group rather than on every row.
                if (g.rows.isNotEmpty() && g.received.isNotEmpty()) Text(g.received, style = T.meta, color = p.muted, modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 10.dp))
                // Used up or close to it; nothing switches CLI or model by itself.
                io.github.lewin671.remoteagent.core.quotaBanner(g).takeIf { it.isNotEmpty() }?.let { Text("$it，不会自动切换 CLI 或模型", style = T.sm, color = p.orangeInk, modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 10.dp)) }
            }
            Text("时间按北京时间显示。额度属于 CLI 登录账户，该账户的其他使用也可能计入。", style = T.sm, color = p.muted, modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp))
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
        Column(Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp), horizontalAlignment = Alignment.CenterHorizontally) {
            val i = init
            when (phase) {
                "start" -> Skeleton()
                "wait" -> if (i != null) {
                    val bmp = remember(i) { qrBitmap(i.link.url(webOrigin(remote.account.server, settings.webOrigin))) }
                    Box(Modifier.padding(top = 14.dp, bottom = 10.dp).size(170.dp).clip(RoundedCornerShape(20.dp)).background(Color.White).padding(12.dp)) {
                        Image(bmp.asImageBitmap(), "配对二维码", Modifier.fillMaxSize(), filterQuality = FilterQuality.None)
                    }
                    Text(i.code.chunked(4).joinToString(" "), style = TextStyle(fontFamily = MonoFamily, fontWeight = FontWeight.SemiBold, fontSize = 26.sp, letterSpacing = 4.sp), color = p.fg)
                    Text("在新设备上扫码，或输入这 8 位配对码。5 分钟内有效。", style = T.sm, color = p.muted, textAlign = TextAlign.Center, modifier = Modifier.padding(top = 10.dp))
                    IconText(Lucide.LoaderCircle, "等待新设备…", p.muted, T.sm, Modifier.padding(top = 10.dp))
                }
                "emoji", "sending" -> Column(Modifier.padding(top = 14.dp).fillMaxWidth().clip(Radii.MD).background(p.soft).padding(14.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                    Row(Modifier.fillMaxWidth()) {
                        Text("新电脑", style = T.smB, color = p.fg)
                        Text(" 请求加入", style = T.sm, color = p.muted)
                    }
                    Text(emoji.orEmpty().joinToString("  "), fontSize = 30.sp, modifier = Modifier.padding(top = 10.dp, bottom = 12.dp))
                    Text("和新设备上的 4 个图案一模一样才允许", style = T.sm, color = p.muted, textAlign = TextAlign.Center, modifier = Modifier.padding(bottom = 10.dp))
                    Btns {
                        Btn("拒绝", { scope.launch { i?.cancelOnServer() }; nav.back() }, Modifier.weight(1f), BtnKind.NEGATIVE, enabled = phase == "emoji", bg = p.card)
                        Btn(if (phase == "sending") "正在交给新设备…" else "图案一致，允许", {
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
                        }, Modifier.weight(1f), BtnKind.PRIMARY, enabled = phase == "emoji")
                    }
                }
                "done" -> {
                    Banner("新设备已加入", Modifier.padding(top = 14.dp), action = "完成", onAction = nav.back, lead = { Mark(Lucide.Check, p.fg2, iconSize(T.sm)) })
                }
                else -> Banner(error, Modifier.padding(top = 14.dp), Tone2.ERROR, action = "换一个", onAction = { attempt++ })
            }
        }
    }
}

private fun imageBytes(n: Long) = if (n in 1 until 104858) "不到 0.1 MiB" else if (n >= 1L shl 30) "%.1f GiB".format(n / 1073741824.0) else "%.1f MiB".format(n / 1048576.0)
private fun mib(n: Long) = if (n >= 1L shl 30 && n % (1L shl 30) == 0L) "${n shr 30} GiB" else "${n shr 20} MiB"

@Composable
private fun MachineImageStorage(settings: DaemonSettings, usage: ImageStorageUsage?, enabled: Boolean, onChange: (DaemonSettings, Boolean) -> Unit) {
    var clean by remember { mutableStateOf(false) }
    var limit by remember { mutableStateOf(false) }
    val free = usage?.let { it.reclaimableBytes + it.cacheBytes } ?: 0L
    // What is worth saying: sizes that are not zero.
    val parts = usage?.let { u -> listOfNotNull(
        if (u.reclaimableBytes > 0) "可清理 ${imageBytes(u.reclaimableBytes)}" else null,
        if (u.reservedBytes > 0) "正在上传 ${imageBytes(u.reservedBytes)}" else null,
    ).joinToString(" · ") }.orEmpty()
    GLabel("图片存储")
    Group(rows = listOf(
        { GRow("上传的图片", usage?.let { imageBytes(it.uploadBytes) + if (parts.isNotEmpty()) " · $parts" else "" } ?: "读取中…") },
        { GRow("空间上限", mib(settings.imageUploadQuotaBytes), onClick = if (enabled) ({ limit = true }) else null, chevron = true) },
        { GRow("预览缓存", usage?.let { imageBytes(it.cacheBytes) } ?: "—") },
        { GRow("清理…", usage?.let { if (free > 0) "可释放 ${imageBytes(free)}" else "没有可清理的" }.orEmpty(), onClick = if (enabled && free > 0) ({ clean = true }) else null, action = true, enabled = enabled && free > 0) },
    ))
    Text("图片存在这台电脑上。正在发送和执行中用到的图片不会被清理；预览缓存最多 150 MiB，自动回收。", style = T.sm, color = pal.muted, modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 12.dp))
    if (limit) {
        val opts = listOf(256L shl 20, 1L shl 30, 5L shl 30)
        ChoiceSheet("空间上限", opts.map { mib(it) to null }, opts.indexOf(settings.imageUploadQuotaBytes), onPick = { i ->
            limit = false
            onChange(settings.toBuilder().setImageUploadQuotaBytes(opts[i]).build(), false)
        }, onClose = { limit = false })
    }
    if (clean) Confirm("清理可回收图片？", "清除可回收原图和缓存，尚未同步的大图会只显示预览。排队和当前执行中的原图、会话历史保留。", "清理", onDismiss = { clean = false }) { clean = false; onChange(settings, true) }
}

/** 图片存储 · 这台手机 (Android): drafts and the viewing cache, limited apart; clearing the cache keeps drafts. */
/** Fonts bundled in the app, each with its SIL OFL 1.1 text from res/raw. */
private val bundledFonts = listOf(
    Triple("Inter", "界面中的西文和数字", io.github.lewin671.remoteagent.R.raw.ofl_inter),
    Triple("JetBrains Mono", "代码、路径、命令和终端", io.github.lewin671.remoteagent.R.raw.ofl_jetbrains_mono),
    Triple("Sarasa Fixed SC（符号子集）", "终端里的符号", io.github.lewin671.remoteagent.R.raw.ofl_sarasa_gothic),
)

/** The icons, drawn from Lucide's paths, with its ISC text from res/raw. */
private val bundledIcons = listOf(
    Triple("Lucide", "界面中的图标", io.github.lewin671.remoteagent.R.raw.isc_lucide),
)

@Composable
fun LicensesPage(onBack: () -> Unit) {
    val p = pal
    val res = androidx.compose.ui.platform.LocalContext.current.resources
    var open by remember { mutableStateOf<Int?>(null) }
    androidx.compose.runtime.CompositionLocalProvider(LocalOnSoft provides true) {
    Column(Modifier.fillMaxSize().background(p.soft)) {
        NavBar("开源许可", onBack)
        Column(Modifier.verticalScroll(rememberScrollState())) {
            for ((label, items) in listOf("字体 · SIL Open Font License 1.1" to bundledFonts, "图标 · ISC License" to bundledIcons)) {
                GLabel(label)
                Group(rows = items.map { (name, use, id) -> @Composable { GRow(name, use, onClick = { open = if (open == id) null else id }) } })
                // The text opens under the group its row is in.
                open?.takeIf { id -> items.any { it.third == id } }?.let { id ->
                    val text = remember(id) { res.openRawResource(id).bufferedReader().use { it.readText() } }
                    Text(text, style = T.meta, color = p.fg2, modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp))
                }
            }
            Spacer(Modifier.height(24.dp))
        }
    }
    }
}

@Composable
fun PhoneImageStoragePage(onBack: () -> Unit) {
    val app = RaApp.instance
    val p = pal
    val action = rememberAction()
    var stamp by remember { mutableStateOf(0) }
    var used by remember { mutableStateOf(0L) }
    var cache by remember { mutableStateOf(0L) }
    LaunchedEffect(stamp) {
        withContext(Dispatchers.IO) { app.pendingStore.expire(); used = app.pendingStore.used; cache = app.imageEnvelopes.used }
    }
    androidx.compose.runtime.CompositionLocalProvider(LocalOnSoft provides true) {
    Column(Modifier.fillMaxSize().background(p.soft)) {
        NavBar("设置", onBack)
        GLabel("图片存储 · 这台手机")
        Group(rows = listOf(
            { GRow("待发和待确认图片", "${imageBytes(used)} / 256 MiB") },
            { Text("草稿闲置 7 天后清理；待确认消息保留到确认或移除。容量不足时提醒，保留已有内容。", style = T.meta, color = p.muted, modifier = Modifier.padding(horizontal = 14.dp, vertical = 10.dp)) },
            { GRow("查看缓存", "${imageBytes(cache)} / 150 MiB") },
            { GRow(if (action.running) "正在清理…" else "清理查看缓存", onClick = { action.run({ withContext(Dispatchers.IO) { app.imageEnvelopes.clear() }; null }) { stamp++ } }, action = true, enabled = !action.running) },
        ))
        if (action.error.isNotEmpty()) Banner(action.error, Modifier.padding(horizontal = 16.dp), Tone2.ERROR)
    }
}
}
