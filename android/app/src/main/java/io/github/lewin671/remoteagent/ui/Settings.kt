package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.selection.selectableGroup

import androidx.compose.ui.draw.alpha

import android.graphics.Bitmap
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
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
import kotlin.math.roundToInt
import androidx.compose.foundation.border
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.FilterQuality
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.google.zxing.BarcodeFormat
import com.google.zxing.EncodeHintType
import com.google.zxing.qrcode.QRCodeWriter
import com.google.zxing.qrcode.decoder.ErrorCorrectionLevel
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.DEFAULT_AGENT
import io.github.lewin671.remoteagent.core.Mode
import io.github.lewin671.remoteagent.core.PairingUiState
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.agentInfo
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.defaultsFor
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.machineAgents
import io.github.lewin671.remoteagent.core.machineVersions
import io.github.lewin671.remoteagent.core.modeOf
import io.github.lewin671.remoteagent.core.projectName
import io.github.lewin671.remoteagent.core.shortPath
import io.github.lewin671.remoteagent.core.whenText
import io.github.lewin671.remoteagent.core.withDefaults
import io.github.lewin671.remoteagent.data.Settings
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentDefaults
import io.github.lewin671.remoteagent.proto.DaemonSettings
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.GetSettingsRequest
import io.github.lewin671.remoteagent.proto.ImageStorageUsage
import io.github.lewin671.remoteagent.proto.UpdateSettingsRequest
import io.github.lewin671.remoteagent.push.Push
import io.github.lewin671.remoteagent.update.Updater
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** A switch as the mockup draws it: 42 × 25, green when on. */
@Composable
fun Sw(on: Boolean) {
    val p = pal
    Box(
        Modifier.size(width = 42.dp, height = 25.dp)
            .clip(RoundedCornerShape(13.dp))
            .background(if (on) p.green else p.fill2)
            .padding(2.dp),
        contentAlignment = if (on) Alignment.CenterEnd else Alignment.CenterStart,
    ) {
        Box(Modifier.size(21.dp).clip(CircleShape).background(Color.White))
    }
}

/** A row with a switch; [sub] explains it under the title. */
@Composable
fun ToggleRow(title: String, sub: String, value: Boolean, enabled: Boolean = true, onChange: (Boolean) -> Unit) {
    val p = pal
    Row(
        Modifier.fillMaxWidth()
            .heightIn(min = 50.dp)
            .clickable(enabled = enabled) { onChange(!value) }
            .padding(start = 18.dp, end = 16.dp, top = 8.dp, bottom = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        // The same row as GRow (design-system §6 列表行): 17 over 13, 18 in from the card's edge.
        Column(Modifier.weight(1f)) {
            Text(title, style = T.s17, color = p.ink)
            if (sub.isNotEmpty()) Text(sub, style = T.s13, color = p.ink2)
        }
        Sw(value)
    }
}

/** A row's leading icon (design-system §5): an ink2 line icon, no coloured tile. */
@Composable
private fun Ico(icon: ImageVector) {
    Ic(icon, 22.dp, pal.ink2)
}

internal fun clientIcon(meta: io.github.lewin671.remoteagent.proto.DeviceMeta?): ImageVector = when {
    meta?.platform == "web" -> Lucide.Globe
    meta?.description?.contains("iPad") == true -> Lucide.Tablet
    else -> Lucide.Smartphone
}

/**
 * 设置 (client-v7): computers, clients, notifications, this phone's storage, appearance and the
 * account, on a grey page.
 */
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
    var checking by remember { mutableStateOf(false) }
    var updating by remember { mutableStateOf(false) }
    val update by Updater.state.collectAsState()
    var storage by remember { mutableStateOf(false) }
    var licenses by remember { mutableStateOf(false) }
    var rotating by remember { mutableStateOf(false) }
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
    if (rotating) {
        androidx.activity.compose.BackHandler { rotating = false }
        RotateRecoveryScreen { rotating = false }
        return
    }
    androidx.compose.runtime.CompositionLocalProvider(LocalOnSoft provides false) {
        Column(
            Modifier.fillMaxSize()
                .background(p.paper)
                .verticalScroll(rememberScrollState())
                .readableColumn(),
        ) {
            BackRow(nav.back)
            LargeTitle("设置")
            ConnBanner(state)
            if (state.clockWarning)
                Notice(
                    "手机时钟比服务器快，远程控制暂停。请在系统设置里校准时间。",
                    Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
                    NoticeTone.WARN,
                )
            MachinesGroup(state, t)
            GLabel("客户端")
            val me = remote.account.deviceId
            val others =
                state.devices.values
                    .filter {
                        it.kind ==
                            io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_CLIENT &&
                            it.id != me
                    }
                    .sortedBy { it.createdMs }
                    .take(3)
            Group(
                rows =
                    listOf<@Composable () -> Unit>(
                        {
                            GRow(
                                io.github.lewin671.remoteagent.core.clientName(
                                    state.devices[me],
                                    me,
                                    state.devices.values,
                                ),
                                "当前",
                                lead = { Ico(clientIcon(state.devices[me]?.meta)) },
                                onClick = { nav.client(me) },
                            )
                        },
                    ) +
                        others.map { c ->
                            @Composable {
                                GRow(
                                    io.github.lewin671.remoteagent.core.clientName(c, c.id, state.devices.values),
                                    "已配对",
                                    lead = { Ico(clientIcon(c.meta)) },
                                    onClick = { nav.client(c.id) },
                                    chevron = true,
                                )
                            }
                        } +
                        listOf(
                            @Composable {
                                GRow(
                                    "全部客户端",
                                    onClick = { nav.devices() },
                                    action = true,
                                    chevron = true,
                                )
                            },
                        ),
            )
            GLabel("账户安全")
            Group(rows = listOf({ GRow("账户安全", if (state.isAuthority) "本机是管理端" else state.authorityName, onClick = { nav.recovery() }, chevron = true) }))
            if (state.isAuthority && state.recoveryPending)
                Notice("恢复申请待处理 · 可生效时间：${io.github.lewin671.remoteagent.core.securityTime(state.recoveryReadyMs, t)}", Modifier.padding(horizontal = 16.dp, vertical = 6.dp), NoticeTone.WARN, action = "处理", onAction = { nav.recovery() })
            GLabel("本客户端的通知")
            Group(
                rows =
                    listOf(
                        {
                            ToggleRow("等我处理时", "", settings.notifyAttention) { v ->
                                scope.launch {
                                    app.prefs.update { it.copy(notifyAttention = v) }
                                    Push.register(app, remote)
                                }
                            }
                        },
                        {
                            ToggleRow("一轮结束时", "", settings.notifyTurnEnded) { v ->
                                scope.launch {
                                    app.prefs.update { it.copy(notifyTurnEnded = v) }
                                    Push.register(app, remote)
                                }
                            }
                        },
                        {
                            ToggleRow("定时任务结果", "", settings.notifyScheduled) { v ->
                                scope.launch {
                                    app.prefs.update { it.copy(notifyScheduled = v) }
                                    Push.register(app, remote)
                                }
                            }
                        },
                        {
                            ToggleRow(
                                "在通知里显示内容并直接审批",
                                "打开后审批的命令会进入系统通知，可能留在通知历史里；锁屏上仍只显示通用文案",
                                settings.showContent,
                            ) { v ->
                                scope.launch { app.prefs.update { it.copy(showContent = v) } }
                            }
                        },
                    ),
            )
            PushNotice(push) { Push.fix(app, remote) }
            GLabel("本机存储")
            Group(rows = listOf { GRow("附件存储", onClick = { storage = true }, chevron = true) })
            GLabel("外观")
            Group(
                rows =
                    listOf {
                        val opts =
                            listOf(
                                ThemeMode.SYSTEM to "自动",
                                ThemeMode.LIGHT to "亮色",
                                ThemeMode.DARK to "暗色",
                            )
                        Row(
                            Modifier.fillMaxWidth()
                                .heightIn(min = 50.dp)
                                .padding(horizontal = 14.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Text(
                                "主题",
                                style = T.s15,
                                color = pal.ink,
                                modifier = Modifier.weight(1f),
                            )
                            SegPick(
                                opts.map { it.second },
                                opts.indexOfFirst { it.first == settings.theme },
                            ) { i ->
                                scope.launch { app.prefs.update { it.copy(theme = opts[i].first) } }
                            }
                        }
                    },
            )
            GLabel("版本")
            Group(
                rows =
                    listOf(
                        {
                            val version =
                                app.versionName.let { v ->
                                    val c = app.buildCommit.take(7)
                                    if (c.isEmpty() || c in v) v else "$v · $c"
                                }
                            // A development copy does not update itself: its row only says what it
                            // is.
                            if (!Updater.enabled(app)) GRow("remote-agent", version)
                            else
                                GRow(
                                    "remote-agent",
                                    updateWord(update) ?: version,
                                    onClick = { updating = true },
                                    chevron = true,
                                    valueColor = if (updateWord(update) != null) p.blue else null,
                                )
                        },
                        { GRow("开源许可", onClick = { licenses = true }, chevron = true) },
                    ),
            )
            GLabel("账户")
            Group(
                rows =
                    listOf(
                        { GRow("服务器", remote.account.server.removePrefix("https://")) },
                        {
                            GRow(
                                "连接检测",
                                connWord(state),
                                onClick = { checking = true },
                                chevron = true,
                            )
                        },
                        {
                            AccountExitAction(
                                state,
                                readPending = { withContext(Dispatchers.IO) { app.vault.readSecurityOperation() != null } },
                                exit = { app.logout() },
                            )
                        },
                    ),
            )
            Spacer(Modifier.height(24.dp))
        }
    }
    if (checking) ConnCheckSheet(state) { checking = false }
    if (updating) UpdateSheet { updating = false }
}

/**
 * Settings' 电脑 (design-system §6 设置): each computer a card with its name,
 * whether it is online, and a line per CLI of how much quota is left with a
 * thin bar, orange at 20% or less. 添加设备 is the row after them; only the
 * authority has it, other clients name it.
 */
@Composable
internal fun MachinesGroup(state: RemoteState, t: Long) {
    val nav = LocalNav.current
    val p = pal
    GLabel("电脑", "全部设备", onAction = { nav.devices() })
    val machines = state.machines.values.sortedBy { machineLabel(state, it.id) }
    for (m in machines) MachineCard(state, m, t) { nav.device(m.id) }
    if (state.isAuthority)
        Row(
            Modifier.fillMaxWidth().padding(horizontal = 16.dp).heightIn(min = 52.dp).clip(Radii.Pill).background(p.gbg)
                .clickable(role = androidx.compose.ui.semantics.Role.Button) { nav.pair() },
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.CenterHorizontally),
        ) {
            Ic(Lucide.Plus, 20.dp, p.blue)
            Text("添加设备", style = T.s17b, color = p.blue)
        }
    else Hint(
        if (machines.isEmpty()) noMachinesHint(state) else addDeviceHint(state),
        Modifier.padding(horizontal = 28.dp),
    )
    Spacer(Modifier.height(12.dp))
}

/** One computer's card: gbg, radius 28, outlined only when it is away. */
@Composable
private fun MachineCard(state: RemoteState, m: io.github.lewin671.remoteagent.core.MachineView, t: Long, onClick: () -> Unit) {
    val p = pal
    Column(
        Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, bottom = 10.dp).clip(Radii.R28)
            .then(if (m.online) Modifier.background(p.gbg) else Modifier.border(0.5.dp, p.sep, Radii.R28))
            .clickable(onClick = onClick).padding(18.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(machineLabel(state, m.id), style = T.h20, color = p.ink, maxLines = 1, overflow = androidx.compose.ui.text.style.TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            Arrow(p.ink3, T.s17)
        }
        Row(Modifier.padding(top = 3.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            StatusMark(if (m.online) MarkKind.ONLINE else MarkKind.OFF, size = 8.dp)
            Text(
                if (m.online) "在线" else io.github.lewin671.remoteagent.core.ago(m.lastSeenMs, t).let { if (it.isEmpty()) "离线" else "${it}在线" },
                style = T.s13, color = p.ink2, maxLines = 1,
            )
        }
        if (m.online) for (g in io.github.lewin671.remoteagent.core.quotaGroups(m, t)) {
            val row = g.rows.filter { it.percent != null }.maxByOrNull { it.percent!! } ?: continue
            val left = (100 - row.percent!!).coerceIn(0.0, 100.0)
            val low = left <= 20.0
            Column(Modifier.padding(top = 14.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(agentName(g.agent), style = T.s15b, color = p.ink, modifier = Modifier.weight(1f), maxLines = 1)
                    Text("剩 ${left.roundToInt()}%", style = if (low) T.s13b else T.s13, color = if (low) p.orangeInk else p.ink2)
                }
                Box(Modifier.padding(top = 6.dp).fillMaxWidth().height(5.dp).clip(Radii.Pill).background(p.fill2)) {
                    Box(Modifier.fillMaxWidth((left / 100).toFloat()).height(5.dp).clip(Radii.Pill).background(if (low) p.orange else p.ink))
                }
                // The reset time is long ("2 天 15 小时后重置 · 11月14日 周六 1:00"); under the bar it leaves the CLI's name whole.
                if (row.reset.isNotEmpty()) Text(row.reset, style = T.s13, color = p.ink2, modifier = Modifier.padding(top = 4.dp))
            }
        }
    }
}

/**
 * Optional background delivery belongs with notification settings, not the app's connection banner: a
 * notice under the group (design-system §6 提示条) with the one action that fixes it.
 */
@Composable
internal fun PushNotice(
    push: io.github.lewin671.remoteagent.push.PushStatus,
    onAction: () -> Unit,
) {
    if (push.problem.isEmpty()) return
    Notice(
        push.problem + "\n仅影响 App 在后台时的系统通知，不影响聊天和审批",
        Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp),
        action = push.action.ifEmpty { null },
        onAction = onAction,
    )
}

/**
 * One setting's few values at the right of its row (主题): a grey capsule with the chosen one raised.
 */
@Composable
fun SegPick(items: List<String>, selected: Int, enabled: Boolean = true, onSelect: (Int) -> Unit) {
    val p = pal
    Row(
        Modifier.clip(Radii.R14).background(p.fill2).padding(2.dp).selectableGroup().alpha(if (enabled) 1f else .45f),
        horizontalArrangement = Arrangement.spacedBy(2.dp),
    ) {
        items.forEachIndexed { i, s ->
            val on = i == selected
            Box(
                Modifier.widthIn(min = 56.dp)
                    .heightIn(min = 32.dp)
                    .clip(RoundedCornerShape(8.dp))
                    .background(
                        if (on) (if (p.paper == Color.White) p.paper else p.fill2)
                        else Color.Transparent,
                    )
                    .selectable(selected = on, enabled = enabled, role = Role.RadioButton) { onSelect(i) }
                    .padding(horizontal = 10.dp),
                contentAlignment = Alignment.Center,
            ) {
                Text(s, style = if (on) T.s13b else T.s13, color = if (on) p.ink else p.ink2)
            }
        }
    }
}

/** A choice from a few options in a sheet: a mode, a timeout, a limit. */
@Composable
private fun ChoiceSheet(
    title: String,
    options: List<Pair<String, String?>>,
    selected: Int,
    enabled: (Int) -> Boolean = { true },
    onPick: (Int) -> Unit,
    onClose: () -> Unit,
) {
    Sheet(onClose) {
        SheetHead(title)
        SheetList { options.forEachIndexed { i, (label, desc) ->
            val ok = enabled(i)
            ListItem(
                label,
                desc,
                checked = i == selected,
                enabled = ok,
                last = i == options.size - 1,
                onClick = if (ok) ({ onPick(i) }) else null,
            )
        } }
    }
}

/**
 * 设备详情 (client-v7): quota, CLI versions, the defaults for new sessions, image storage, projects,
 * remove.
 */
@Composable
fun DeviceScreen(id: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val m = state.machines[id]
    val member = id in state.devices
    val p = pal
    var confirm by remember { mutableStateOf(false) }
    val remove = rememberAction(authority = true)
    var settings by remember { mutableStateOf<DaemonSettings?>(null) }
    var imageStorage by remember { mutableStateOf<ImageStorageUsage?>(null) }
    var loadError by remember { mutableStateOf("") }
    val save = rememberAction()
    var attempt by remember { mutableStateOf(0) }
    LaunchedEffect(id, member, m?.online, attempt) {
        if (!member || m?.online != true) return@LaunchedEffect
        try {
            val reply =
                remote
                    .machineCommand(
                        id,
                        remote.cmd { getSettings = GetSettingsRequest.getDefaultInstance() },
                    )
                    .getSettings
            settings = reply.settings
            if (reply.hasImageStorage()) imageStorage = reply.imageStorage
            loadError = ""
        } catch (e: Exception) {
            loadError = errorText(e, remote.state.value)
        }
    }
    var pickModel by remember { mutableStateOf<Agent?>(null) }
    var pickMode by remember { mutableStateOf<Agent?>(null) }
    var pickTimeout by remember { mutableStateOf(false) }
    var pickIdleClose by remember { mutableStateOf(false) }
    var activeAgent by remember(id) { mutableStateOf<Agent?>(null) }
    fun update(s: DaemonSettings, cleanImages: Boolean = false) =
        save.run(
            {
                remote.machineCommand(
                    id,
                    remote.cmd {
                        updateSettings =
                            UpdateSettingsRequest.newBuilder()
                                .setSettings(s)
                                .setCleanImages(cleanImages)
                                .build()
                    },
                )
            },
        ) { r ->
            r?.updateSettings?.let {
                settings = it.settings
                if (it.hasImageStorage()) imageStorage = it.imageStorage
            }
        }
    PageScaffold(machineLabel(state, id), readable = true) {
        if (m == null) {
            PageNote("这台电脑不在当前可信目录中，撤销状态以回执为准")
            return@PageScaffold
        }
        Column(Modifier.verticalScroll(rememberScrollState())) {
            if (!member)
                Notice(
                    "已移除 · 保留的会话档案",
                    Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp),
                )
            else if (!m.online)
                Notice(
                    "不在线 · 最后在线 ${whenText(m.lastSeenMs)}",
                    Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp),
                )
            val meta = m.meta
            val groups =
                io.github.lewin671.remoteagent.core.quotaGroups(m, System.currentTimeMillis())
            // A line for each CLI's quota, leading to all of it.
            GLabel("账户额度")
            Group(
                rows =
                    groups.map { g ->
                        @Composable {
                            val qline = io.github.lewin671.remoteagent.core.quotaLine(g)
                            val sepIdx = qline.indexOf(" · ")
                            if (sepIdx != -1) {
                                GRow(
                                    g.title.substringBefore(" · "),
                                    trailing = {
                                        Text(
                                            androidx.compose.ui.text.buildAnnotatedString {
                                                append(qline.substring(0, sepIdx))
                                                pushStyle(androidx.compose.ui.text.SpanStyle(color = p.ink2))
                                                append(" · ")
                                                append(qline.substring(sepIdx + 3))
                                                pop()
                                            },
                                            style = T.s13,
                                            color = p.ink,
                                        )
                                    },
                                    onClick = { nav.quota(id) },
                                    chevron = true,
                                )
                            } else {
                                GRow(
                                    g.title.substringBefore(" · "),
                                    qline,
                                    onClick = { nav.quota(id) },
                                    chevron = true,
                                )
                            }
                        }
                    },
            )
            if (meta != null) {
                val agents = machineVersions(meta)
                Group(
                    rows =
                        agents.map { a ->
                            @Composable {
                                GRow(
                                    "${agentName(a.agent)} 版本",
                                    trailing = {
                                        Text(
                                            androidx.compose.ui.text.buildAnnotatedString {
                                                if (!a.verified) {
                                                    pushStyle(
                                                        androidx.compose.ui.text.SpanStyle(
                                                            color = p.orangeInk,
                                                        ),
                                                    )
                                                    append("未验证")
                                                    pop()
                                                    append(" · ")
                                                }
                                                append(a.version.ifEmpty { "未知" })
                                            },
                                            style = T.s13,
                                            color = p.ink2,
                                        )
                                    },
                                )
                            }
                        },
                )
                if (agents.any { !it.verified })
                    Hint(
                        "未验证的版本请在电脑上运行 remote-agent doctor",
                        Modifier.padding(horizontal = 30.dp).padding(bottom = 12.dp),
                    )
            }
            val s = settings
            when {
                !m.online ->
                    Hint(
                        "${machineLabel(state, id)} 不在线，连上后才能读取和修改新会话默认设置",
                        Modifier.padding(horizontal = 30.dp).padding(bottom = 12.dp),
                    )
                loadError.isNotEmpty() ->
                    Notice(
                        "没能读取设置：$loadError",
                        Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp),
                        NoticeTone.ERROR,
                        action = "重试",
                        onAction = { attempt++ },
                    )
                s == null ->
                    Hint("读取中…", Modifier.padding(horizontal = 30.dp).padding(bottom = 12.dp))
                else -> {
                    val mAgents = machineAgents(m)
                    val currentAgent = if (activeAgent in mAgents) activeAgent!! else mAgents.firstOrNull() ?: DEFAULT_AGENT
                    GLabel(
                        if (mAgents.size > 1) "新项目默认设置"
                        else "新项目默认设置 · ${agentName(currentAgent)}"
                    )
                    if (mAgents.size > 1) {
                        Segmented(
                            mAgents.map { agentName(it) },
                            mAgents.indexOf(currentAgent),
                            { activeAgent = mAgents[it] },
                            Modifier.padding(start = 16.dp, end = 16.dp, bottom = 10.dp).fillMaxWidth(),
                        )
                    }
                    val d = defaultsFor(s, currentAgent)
                    val eff = defaultModeOf(m, currentAgent)
                    val cur = modeOf(d.mode)
                    Group(
                        rows = listOf(
                            @Composable {
                                GRow(
                                    "模型",
                                    d.model.ifEmpty { "默认" },
                                    onClick = if (save.running) null else ({ pickModel = currentAgent }),
                                    chevron = true,
                                )
                            },
                            @Composable {
                                GRow(
                                    "推理强度",
                                    d.effort.ifEmpty { "默认" },
                                    onClick = if (save.running) null else ({ pickModel = currentAgent }),
                                    chevron = true,
                                )
                            },
                            @Composable {
                                GRow(
                                    "审批模式",
                                    cur?.text ?: modeLabel(null, eff.copy(varies = false), currentAgent),
                                    onClick = if (save.running) null else ({ pickMode = currentAgent }),
                                    valueColor = if (cur == null && (eff.mode == Mode.FULL || !eff.known)) p.red else null,
                                    chevron = true,
                                )
                            },
                        ),
                    )
                    Group(
                        rows = listOf(
                            @Composable {
                                GRow(
                                    "审批超时",
                                    "${s.approvalTimeoutSeconds / 60} 分钟",
                                    onClick = if (save.running) null else ({ pickTimeout = true }),
                                    chevron = true,
                                )
                            },
                        ),
                    )
                    Group(
                        rows = listOf(
                            @Composable {
                                GRow(
                                    "空闲自动结束会话",
                                    idleCloseLabel(s.idleCloseMinutes),
                                    onClick = if (save.running) null else ({ pickIdleClose = true }),
                                    chevron = true,
                                )
                            },
                        ),
                    )
                    Text(
                        "会话长时间没有任何活动（没在运行、没有待处理、终端里没人输入）就自动结束，随时可以恢复，聊天记录都在。",
                        style = T.s13,
                        color = p.ink2,
                        modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 12.dp),
                    )
                }
            }
            if (save.error.isNotEmpty())
                Notice(
                    "没能保存：${save.error}",
                    Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp),
                    NoticeTone.ERROR,
                )
            settings?.let { s ->
                MachineImageStorage(
                    s,
                    imageStorage,
                    m.online && !save.running,
                    onChange = { next, clean -> update(next, clean) },
                )
            }
            val projects = io.github.lewin671.remoteagent.core.machineProjects(state, id)
            if (projects.isNotEmpty()) {
                GLabel("项目")
                Group(
                    rows =
                        projects.map { cwd ->
                            @Composable {
                                GRow(
                                    projectName(cwd),
                                    shortPath(cwd),
                                    onClick = { nav.project(cwd) },
                                )
                            }
                        },
                )
            }
            if (state.isAuthority && id !in state.devices) {
                Group(
                    rows =
                        listOf {
                            GRow(
                                "让这台电脑重新加入并接续会话",
                                onClick = { nav.rejoin(id) },
                                enabled =
                                    state.conn ==
                                        io.github.lewin671.remoteagent.core.ConnState.ONLINE,
                            )
                        },
                )
            }
            DeleteMachineRecordsAction(id, state.isAuthority)
            if (!member && !state.isAuthority)
                Hint("档案重接和删除记录，请在管理端（${state.authorityName}）上操作。", Modifier.padding(horizontal = 30.dp))
            if (member) {
                if (id == state.authorityDeviceId) Hint("这是管理端设备，普通移除不可用", Modifier.padding(horizontal = 30.dp))
                else if (state.isAuthority)
                    Group(
                        rows =
                            listOf {
                                GRow(
                                    "移除这台机器",
                                    onClick = { confirm = true },
                                    danger = true,
                                    enabled =
                                        state.isAuthority &&
                                            !remove.running &&
                                            state.conn ==
                                                io.github.lewin671.remoteagent.core.ConnState
                                                    .ONLINE,
                                )
                            },
                    )
                Text(
                    if (state.isAuthority) "移除会立即断开连接，保留中继上的会话和图片；它需要重新加入账户才能连接。"
                    else "请在管理端（${state.authorityName}）上移除设备。",
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.padding(horizontal = 30.dp, vertical = 8.dp),
                )
            }
            if (remove.error.isNotEmpty())
                Notice(remove.error, Modifier.padding(16.dp), NoticeTone.ERROR)
            Spacer(Modifier.height(24.dp))
        }
    }
    if (confirm && member) {
        val (title, text) = removalDialog(machineLabel(state, id), machine = true)
        Alert(
            title,
            text,
            "移除",
            danger = true,
            onDismiss = { confirm = false },
        ) {
            remove.run(
                {
                    remote.removeDevice(id)
                    null
                },
            )
        }
    }
    val cur = settings
    pickModel?.let { agent ->
        if (cur != null)
            DefaultModelSheet(
                id,
                agent,
                cur,
                onPick = { model, effort ->
                    pickModel = null
                    update(withModel(cur, agent, model, effort))
                },
                onClose = { pickModel = null },
            )
    }
    pickMode?.let { agent ->
        if (cur != null) {
            val eff = defaultModeOf(m, agent)
            val d = defaultsFor(cur, agent)
            var models by remember(id, agent) {
                mutableStateOf<io.github.lewin671.remoteagent.proto.ListModelsResponse?>(null)
            }
            LaunchedEffect(id, agent) {
                if (agentInfo(agent).autoMayBeUnavailable) {
                    runCatching { models = remote.listModels(id, agent) }
                }
            }
            val allNoAuto = agentInfo(agent).autoMayBeUnavailable && io.github.lewin671.remoteagent.core.allAutoUnavailable(models?.modelsList)
            val noAuto = allNoAuto || (agentInfo(agent).autoMayBeUnavailable && io.github.lewin671.remoteagent.core.autoUnavailable(models?.modelsList, d.model))
            val opts = listOf(null, Mode.ASK, Mode.AUTO)
            ChoiceSheet(
                "${agentName(agent)} · 审批模式",
                opts.map { mode ->
                    val desc = when {
                        mode == null -> "不指定时实际用：" + modeLabel(null, eff, agent).removePrefix("默认：")
                        mode == Mode.AUTO && noAuto -> if (allNoAuto) "当前账户不支持自动模式" else "当前模型不支持"
                        else -> modeHelp.getValue(mode).replace("CLI", agentName(agent))
                    }
                    (mode?.text ?: "默认") to desc
                },
                opts.indexOf(modeOf(d.mode)).coerceAtLeast(0),
                enabled = { i -> !(opts[i] == Mode.AUTO && noAuto) },
                onPick = { i ->
                    pickMode = null
                    update(withMode(cur, agent, opts[i]))
                },
                onClose = { pickMode = null },
            )
        }
    }
    if (pickTimeout && cur != null) {
        val mins = listOf(2, 5, 10, 30)
        ChoiceSheet(
            "审批超时",
            mins.map { "$it 分钟" to null },
            mins.indexOf(cur.approvalTimeoutSeconds / 60),
            onPick = { i ->
                pickTimeout = false
                update(cur.toBuilder().setApprovalTimeoutSeconds(mins[i] * 60).build())
            },
            onClose = { pickTimeout = false },
        )
    }
    if (pickIdleClose && cur != null) {
        ChoiceSheet(
            "空闲自动结束会话",
            IDLE_CLOSE_CHOICES.map { (if (it == IDLE_CLOSE_DEFAULT) "${idleCloseLabel(it)}（默认）" else idleCloseLabel(it)) to null },
            idleCloseChoice(cur.idleCloseMinutes),
            onPick = { i ->
                pickIdleClose = false
                update(cur.toBuilder().setIdleCloseMinutes(IDLE_CLOSE_CHOICES[i]).build())
            },
            onClose = { pickIdleClose = false },
        )
    }
}

/** The daemon closes a session idle for 6 hours unless told otherwise; 0 stands for that. */
internal const val IDLE_CLOSE_DEFAULT = 360

/** Idle close choices in minutes; -1 never closes. */
internal val IDLE_CLOSE_CHOICES = listOf(-1, 60, 120, IDLE_CLOSE_DEFAULT, 720, 1440)

/** An idle close setting in words: 关闭, whole hours, or minutes for anything else. */
internal fun idleCloseLabel(minutes: Int): String {
    val m = if (minutes == 0) IDLE_CLOSE_DEFAULT else minutes
    return when {
        m < 0 -> "关闭"
        m % 60 == 0 -> "${m / 60} 小时"
        else -> "$m 分钟"
    }
}

/** Which choice the setting is, or -1 for a value set elsewhere. */
internal fun idleCloseChoice(minutes: Int): Int =
    IDLE_CLOSE_CHOICES.indexOf(if (minutes < 0) -1 else if (minutes == 0) IDLE_CLOSE_DEFAULT else minutes)

/** One usage window: its name, what is left, a bar of it, and when it resets. */
@Composable
private fun QuotaRowView(
    r: io.github.lewin671.remoteagent.core.QuotaRow,
    received: String = "",
    compact: Boolean = false,
) {
    val p = pal
    Column(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 10.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(r.label, style = T.s15, color = p.ink, modifier = Modifier.weight(1f))
            Text(
                if (compact && r.reset.isNotEmpty()) "${r.left} · ${r.reset}" else r.left,
                style = T.s13,
                color =
                    when (r.tone) {
                        io.github.lewin671.remoteagent.core.QuotaTone.FULL,
                        io.github.lewin671.remoteagent.core.QuotaTone.WARN -> p.orangeInk
                        else -> p.ink2
                    },
            )
        }
        r.percent?.let { pc ->
            Box(
                Modifier.padding(top = 6.dp)
                    .fillMaxWidth()
                    .height(6.dp)
                    .clip(RoundedCornerShape(3.dp))
                    .background(p.fill2),
            ) {
                Box(
                    Modifier.fillMaxWidth(((100 - pc) / 100).toFloat().coerceIn(0f, 1f))
                        .height(6.dp)
                        .clip(RoundedCornerShape(3.dp))
                        .background(
                            when (r.tone) {
                                io.github.lewin671.remoteagent.core.QuotaTone.STALE -> p.faint
                                io.github.lewin671.remoteagent.core.QuotaTone.NORMAL -> p.ink
                                else -> p.orange
                            },
                        ),
                )
            }
        }
        val sub =
            if (compact) ""
            else listOf(r.reset, r.note, received).filter { it.isNotEmpty() }.joinToString(" · ")
        if (sub.isNotEmpty())
            Text(sub, style = T.s12, color = p.ink2, modifier = Modifier.padding(top = 8.dp))
    }
}

private fun withModel(
    s: DaemonSettings,
    agent: Agent,
    model: String,
    effort: String,
): DaemonSettings =
    withDefaults(s, agent, model = model, effort = effort)

/** A CLI's default model and effort for new sessions on this machine, from the models it offers. */
@Composable
private fun DefaultModelSheet(
    machineId: String,
    agent: Agent,
    s: DaemonSettings,
    onPick: (String, String) -> Unit,
    onClose: () -> Unit,
) {
    val p = pal
    val remote = LocalRemote.current
    var models by remember {
        mutableStateOf<io.github.lewin671.remoteagent.proto.ListModelsResponse?>(null)
    }
    var error by remember { mutableStateOf("") }
    LaunchedEffect(machineId, agent) {
        try {
            models = remote.listModels(machineId, agent, fresh = true)
        } catch (e: Exception) {
            error = errorText(e, remote.state.value)
        }
    }
    val d = defaultsFor(s, agent)
    var model by remember { mutableStateOf(d.model) }
    var all by remember { mutableStateOf(false) }
    Sheet(onClose) {
        SheetHead("${agentName(agent)} · 新会话默认模型")
        val ms = models
        when {
            error.isNotEmpty() -> Notice("没能读取模型列表：$error", tone = NoticeTone.ERROR)
            ms == null ->
                Text(
                    "读取中…",
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.padding(vertical = 12.dp),
                )
            else -> {
                val shown =
                    io.github.lewin671.remoteagent.core.shownModels(
                        ms.modelsList,
                        ms.modelsList.firstOrNull { it.slug == model },
                        all,
                    )
                val few = shown.size == ms.modelsCount
                SheetList {
                ListItem(
                    "默认（${agentName(agent)} 自己的配置）",
                    checked = model.isEmpty(),
                    last = shown.isEmpty(),
                    onClick = { onPick("", "") },
                )
                shown.forEachIndexed { i, m ->
                    ListItem(
                        m.displayName.ifEmpty { m.slug },
                        small =
                            if (m.effortsList.size == 1) "推理强度固定为 ${m.effortsList[0]}"
                            else
                                listOfNotNull(
                                    io.github.lewin671.remoteagent.core.contextText(
                                        m.contextWindow,
                                    ),
                                    if (m.effortsList.isEmpty()) "无推理强度" else null,
                                )
                                .joinToString(" · ")
                                .ifEmpty { null },
                        checked = m.slug == model,
                        last = i == shown.size - 1,
                        onClick = {
                            if (m.effortsList.isEmpty()) onPick(m.slug, "") else model = m.slug
                        },
                    )
                }
                }
                if (!few) AllModels { all = true }
                ms.modelsList
                    .firstOrNull { it.slug == model }
                    ?.takeIf { it.effortsList.isNotEmpty() }
                    ?.let { m ->
                        FieldLabel("推理强度")
                        ChoiceGrid(m.effortsList, if (d.model == m.slug) d.effort else null, true) {
                            onPick(m.slug, it)
                        }
                    }
            }
        }
    }
}

/**
 * 账户额度: each CLI's account usage on this machine, apart, as the daemon reads it. What is not known
 * is said, never shown as 0%; an offline machine's or a failed read's numbers are the last record,
 * and a reset time that passed only waits for an update.
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
    PageScaffold(
        "账户额度",
        subtitle = m?.let { machineLabel(state, id) + " · " + if (it.online) "在线" else "离线" },
        readable = true,
    ) {
        if (m == null) {
            PageNote("这台机器已经移除了")
            return@PageScaffold
        }
        Column(Modifier.verticalScroll(rememberScrollState())) {
            if (!m.online)
                Text(
                    "机器离线，显示上次记录",
                    style = T.s13,
                    color = p.orangeInk,
                    modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 6.dp),
                )
            for (g in groups) {
                if (g.status == io.github.lewin671.remoteagent.core.QuotaStatus.FAILED)
                    Text(
                        "没能读取 ${agentName(g.agent)} 额度" +
                            if (g.detail.isNotEmpty()) "：${g.detail}" else "",
                        style = T.s13,
                        color = p.red,
                        modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 6.dp),
                    )
                if (g.sections.isNotEmpty()) {
                    for (sec in g.sections) {
                        GLabel(sec.title)
                        val srows = sec.rows.map { r ->
                            @Composable { QuotaRowView(r) }
                        }
                        Group(rows = srows)
                    }
                } else {
                    GLabel(g.title + if (g.lastRecord) " · 上次记录" else "")
                    val rows = mutableListOf<@Composable () -> Unit>()
                    when (g.status) {
                        io.github.lewin671.remoteagent.core.QuotaStatus.UNSUPPORTED ->
                            rows +=
                                @Composable {
                                    Column(Modifier.padding(horizontal = 14.dp, vertical = 10.dp)) {
                                        Text("暂不支持读取额度", style = T.s15b, color = p.ink)
                                        Text(
                                            g.detail.ifEmpty {
                                                "这台电脑上的 remote-agent 读不到 ${agentName(g.agent)} 的额度。"
                                            } + agentInfo(g.agent).usageHint.orEmpty(),
                                            style = T.s12,
                                            color = p.ink2,
                                        )
                                    }
                                }
                        io.github.lewin671.remoteagent.core.QuotaStatus.WAITING ->
                            rows +=
                                @Composable {
                                    Column(Modifier.padding(horizontal = 14.dp, vertical = 10.dp)) {
                                        Text("暂未获取额度", style = T.s15b, color = p.ink)
                                        Text(
                                            "等待 ${agentName(g.agent)} 返回额度数据",
                                            style = T.s12,
                                            color = p.ink2,
                                        )
                                    }
                                }
                        else -> {}
                    }
                    for (r in g.rows) rows += @Composable { QuotaRowView(r) }
                    if (rows.isNotEmpty()) Group(rows = rows)
                }
                // When it was heard, once for the group rather than on every row.
                if (g.rows.isNotEmpty() && g.received.isNotEmpty())
                    Text(
                        g.received,
                        style = T.s12,
                        color = p.ink2,
                        modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 10.dp),
                    )
                // Used up or close to it; nothing switches CLI or model by itself.
                io.github.lewin671.remoteagent.core
                    .quotaBanner(g)
                    .takeIf { it.isNotEmpty() }
                    ?.let {
                        Text(
                            "$it，不会自动切换 CLI 或模型",
                            style = T.s13,
                            color = p.orangeInk,
                            modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 10.dp),
                        )
                    }
            }
            Text(
                "时间按北京时间显示。额度属于 CLI 登录账户，该账户的其他使用也可能计入。",
                style = T.s13,
                color = p.ink2,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp),
            )
        }
    }
}

private fun withMode(s: DaemonSettings, agent: Agent, mode: Mode?): DaemonSettings {
    val m =
        mode?.proto ?: io.github.lewin671.remoteagent.proto.ApprovalMode.APPROVAL_MODE_UNSPECIFIED
    return withDefaults(s, agent, mode = m)
}

fun qrBitmap(text: String, size: Int = 512): Bitmap {
    val m =
        QRCodeWriter()
            .encode(
                text,
                BarcodeFormat.QR_CODE,
                size,
                size,
                mapOf(
                    EncodeHintType.ERROR_CORRECTION to ErrorCorrectionLevel.M,
                    EncodeHintType.MARGIN to 1,
                ),
            )
    val px =
        IntArray(size * size) { i ->
            if (m.get(i % size, i / size)) 0xFF000000.toInt() else 0xFFFFFFFF.toInt()
        }
    return Bitmap.createBitmap(px, size, size, Bitmap.Config.ARGB_8888)
}

/** A recovery code as the text the user writes down and a QR to photograph. */
@Composable
fun RecoveryCodeFigure(code: String) {
    val p = pal
    val bmp = remember(code) { qrBitmap(code) }
    Box(
        Modifier.padding(vertical = 10.dp)
            .size(170.dp)
            .clip(RoundedCornerShape(20.dp))
            .background(Color.White)
            .padding(10.dp),
    ) {
        Image(
            bmp.asImageBitmap(),
            "恢复码二维码",
            Modifier.fillMaxSize(),
            filterQuality = FilterQuality.None,
        )
    }
    Text(
        code,
        style = T.mono12.copy(fontSize = 16.sp),
        color = p.ink,
        modifier = Modifier.fillMaxWidth().padding(vertical = 6.dp),
    )
    Hint("请离线保存恢复码。任何拿到它的人都能申请接管管理权；不要发送给别人。")
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
fun PairScreen(replacesDeviceId: String = "") {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val settings by RaApp.instance.prefs.settings.collectAsState(Settings())
    val p = pal
    val model = remember(remote, replacesDeviceId) { RaApp.instance.pairing(remote, replacesDeviceId) }
    val state by model.state.collectAsState()
    AuthorityStartEffect(model) { model.enter() }
    DisposableEffect(model) { onDispose { model.leave() } }
    PageScaffold("添加设备") {
        Column(
            Modifier.fillMaxWidth()
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 16.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Hint("电脑：运行 remote-agent account join，输入下面的配对码。客户端：扫二维码或输入配对码。")
            if (replacesDeviceId.isNotEmpty()) {
                val current by remote.state.collectAsState()
                Hint(
                    "允许原电脑接续 ${machineLabel(current, replacesDeviceId)} 的保留会话和图片。旧身份仍然撤销；请在原电脑上输入配对码。",
                )
            } else {
                Hint("已移除的电脑要接续原会话，请从那台电脑的保留档案进入“重新加入并接续会话”。")
            }
            when (val step = state) {
                PairingUiState.Starting -> Skeleton()
                is PairingUiState.Waiting -> {
                    val bmp =
                        remember(step.link) {
                            qrBitmap(
                                step.link.url(webOrigin(remote.account.server, settings.webOrigin)),
                            )
                        }
                    Box(
                        Modifier.padding(top = 14.dp, bottom = 10.dp)
                            .size(170.dp)
                            .clip(RoundedCornerShape(20.dp))
                            .background(Color.White)
                            .padding(12.dp),
                    ) {
                        Image(
                            bmp.asImageBitmap(),
                            "配对二维码",
                            Modifier.fillMaxSize(),
                            filterQuality = FilterQuality.None,
                        )
                    }
                    Text(
                        step.code.chunked(4).joinToString(" "),
                        style =
                            TextStyle(
                                fontFamily = MonoFamily,
                                fontWeight = FontWeight.SemiBold,
                                fontSize = 40.sp,
                                lineHeight = 44.sp,
                                letterSpacing = 2.4.sp,
                            ),
                        color = p.ink,
                    )
                    Text(
                        "有效期至 " + io.github.lewin671.remoteagent.core.securityTime(step.expiresMs, timeOnly = true),
                        style = T.s13,
                        color = p.ink2,
                        textAlign = TextAlign.Center,
                        modifier = Modifier.padding(top = 10.dp),
                    )
                    IconText(
                        Lucide.LoaderCircle,
                        "等待新设备…",
                        p.ink2,
                        T.s13,
                        Modifier.padding(top = 10.dp),
                    )
                }
                is PairingUiState.Compare,
                is PairingUiState.Sending -> {
                    val sending = step is PairingUiState.Sending
                    val emoji =
                        when (step) {
                            is PairingUiState.Compare -> step.emoji
                            is PairingUiState.Sending -> step.emoji
                            else -> emptyList()
                        }
                    val kind =
                        when (step) {
                            is PairingUiState.Compare -> step.kind
                            is PairingUiState.Sending -> step.kind
                            else -> DeviceKind.DEVICE_KIND_UNSPECIFIED
                        }
                    Column(
                        Modifier.padding(top = 14.dp)
                            .fillMaxWidth()
                            .clip(Radii.R14)
                            .background(p.gbg)
                            .padding(14.dp),
                        horizontalAlignment = Alignment.CenterHorizontally,
                    ) {
                        Row(Modifier.fillMaxWidth()) {
                            Text(
                                if (kind == DeviceKind.DEVICE_KIND_MACHINE) "新电脑" else "新设备",
                                style = T.s13b,
                                color = p.ink,
                            )
                            Text(" 请求加入", style = T.s13, color = p.ink2)
                        }
                        Text(
                            emoji.joinToString("  "),
                            fontSize = 30.sp,
                            modifier = Modifier.padding(top = 10.dp, bottom = 12.dp),
                        )
                        Text(
                            if (sending) "请在新设备上核对这 4 个图案，并点确认。确认后才能加入；可以离开本页等待。"
                            else "核对新设备上的 4 个图案。先在新设备确认，再在这里允许。",
                            style = T.s13,
                            color = p.ink2,
                            textAlign = TextAlign.Center,
                            modifier = Modifier.padding(bottom = 10.dp),
                        )
                        Btns {
                            PillButton(
                                "拒绝",
                                {
                                    model.leave()
                                    nav.back()
                                },
                                Modifier.weight(1f),
                                ButtonKind.SECONDARY,
                                enabled = !sending,
                            )
                            PillButton(
                                if (sending) "等新设备确认图案…" else "允许加入",
                                model::approve,
                                Modifier.weight(1f),
                                ButtonKind.INK,
                                enabled = !sending,
                            )
                        }
                    }
                }
                PairingUiState.Done ->
                    Notice(
                        "新设备已加入",
                        Modifier.padding(top = 14.dp),
                        action = "完成",
                        onAction = nav.back,
                        icon = Lucide.Check,
                    )
                is PairingUiState.Failed ->
                    Notice(
                        step.message,
                        Modifier.padding(top = 14.dp),
                        if (step.uncertain) NoticeTone.PLAIN else NoticeTone.ERROR,
                        action = if (step.canRetry) "重试" else "重新开始",
                        onAction = model::retry,
                    )
            }
        }
    }
}

private fun imageBytes(n: Long) =
    if (n in 1 until 104858) "不到 0.1 MiB"
    else if (n >= 1L shl 30) "%.1f GiB".format(n / 1073741824.0)
    else "%.1f MiB".format(n / 1048576.0)

private fun mib(n: Long) =
    if (n >= 1L shl 30 && n % (1L shl 30) == 0L) "${n shr 30} GiB" else "${n shr 20} MiB"

@Composable
private fun MachineImageStorage(
    settings: DaemonSettings,
    usage: ImageStorageUsage?,
    enabled: Boolean,
    onChange: (DaemonSettings, Boolean) -> Unit,
) {
    var clean by remember { mutableStateOf(false) }
    var limit by remember { mutableStateOf(false) }
    val free = usage?.let { it.reclaimableBytes + it.cacheBytes } ?: 0L
    // What is worth saying: sizes that are not zero.
    val parts =
        usage
            ?.let { u ->
                listOfNotNull(
                        if (u.reclaimableBytes > 0) "可清理 ${imageBytes(u.reclaimableBytes)}"
                        else null,
                        if (u.reservedBytes > 0) "正在上传 ${imageBytes(u.reservedBytes)}" else null,
                    )
                    .joinToString(" · ")
            }
            .orEmpty()
    GLabel("附件存储")
    Group(
        rows =
            listOf(
                {
                    GRow(
                        "上传的图片和文件",
                        usage?.let {
                            imageBytes(it.uploadBytes) + if (parts.isNotEmpty()) " · $parts" else ""
                        } ?: "读取中…",
                    )
                },
                {
                    GRow(
                        "空间上限",
                        mib(settings.imageUploadQuotaBytes),
                        onClick = if (enabled) ({ limit = true }) else null,
                        chevron = true,
                    )
                },
                { GRow("预览缓存", usage?.let { imageBytes(it.cacheBytes) } ?: "—") },
                {
                    GRow(
                        "清理…",
                        usage
                            ?.let { if (free > 0) "可释放 ${imageBytes(free)}" else "没有可清理的" }
                            .orEmpty(),
                        onClick = if (enabled && free > 0) ({ clean = true }) else null,
                        action = true,
                        enabled = enabled && free > 0,
                    )
                },
            ),
    )
    Text(
        "附件存在这台电脑上。会话使用过的文件保留到会话结束；正在发送和执行中用到的图片不会被清理。预览缓存最多 150 MiB，自动回收。",
        style = T.s13,
        color = pal.ink2,
        modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 12.dp),
    )
    if (limit) {
        val opts = listOf(256L shl 20, 1L shl 30, 5L shl 30)
        ChoiceSheet(
            "空间上限",
            opts.map { mib(it) to null },
            opts.indexOf(settings.imageUploadQuotaBytes),
            onPick = { i ->
                limit = false
                onChange(settings.toBuilder().setImageUploadQuotaBytes(opts[i]).build(), false)
            },
            onClose = { limit = false },
        )
    }
    if (clean)
        Confirm(
            "清理可回收图片？",
            "清除可回收原图和缓存，尚未同步的大图会只显示预览。排队和当前执行中的原图、会话历史保留。",
            "清理",
            onDismiss = { clean = false },
        ) {
            clean = false
            onChange(settings, true)
        }
}

/**
 * 附件存储 · 这台手机 (Android): drafts and the viewing cache, limited apart; clearing the cache keeps
 * drafts.
 */
/** Fonts bundled in the app, each with its SIL OFL 1.1 text from res/raw. */
/** Where devices are added; shared wording with web and iOS. */
fun addDeviceHint(state: RemoteState) = "要添加电脑或客户端，在管理端（${state.authorityName}）上打开「设置 › 添加设备」。"

/** An account with no computer, seen from a client that cannot add one. */
fun noMachinesHint(state: RemoteState) = "还没有电脑。在管理端（${state.authorityName}）上打开「设置 › 添加设备」，把电脑加进来。"

private val bundledFonts =
    listOf(
        Triple("Inter", "界面中的西文和数字", io.github.lewin671.remoteagent.R.raw.ofl_inter),
        Triple(
            "JetBrains Mono",
            "代码、路径、命令和终端",
            io.github.lewin671.remoteagent.R.raw.ofl_jetbrains_mono,
        ),
        Triple(
            "Sarasa Fixed SC（符号子集）",
            "终端里的符号",
            io.github.lewin671.remoteagent.R.raw.ofl_sarasa_gothic,
        ),
    )

/** The icons, drawn from Lucide's paths, with its ISC text from res/raw. */
private val bundledIcons =
    listOf(
        Triple("Lucide", "界面中的图标", io.github.lewin671.remoteagent.R.raw.isc_lucide),
    )

@Composable
fun LicensesPage(onBack: () -> Unit) {
    val p = pal
    val res = androidx.compose.ui.platform.LocalContext.current.resources
    var open by remember { mutableStateOf<Int?>(null) }
    androidx.compose.runtime.CompositionLocalProvider(LocalOnSoft provides false) {
        Column(Modifier.fillMaxSize().background(p.paper)) {
            PageBar("开源许可", onBack)
            Column(Modifier.verticalScroll(rememberScrollState())) {
                for ((label, items) in
                    listOf(
                        "字体 · SIL Open Font License 1.1" to bundledFonts,
                        "图标 · ISC License" to bundledIcons,
                    )) {
                    GLabel(label)
                    Group(
                        rows =
                            items.map { (name, use, id) ->
                                @Composable {
                                    GRow(
                                        name,
                                        use,
                                        onClick = { open = if (open == id) null else id },
                                    )
                                }
                            },
                    )
                    // The text opens under the group its row is in.
                    open
                        ?.takeIf { id -> items.any { it.third == id } }
                        ?.let { id ->
                            val text =
                                remember(id) {
                                    res.openRawResource(id).bufferedReader().use { it.readText() }
                                }
                            Text(
                                text,
                                style = T.s12,
                                color = p.ink,
                                modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                            )
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
        withContext(Dispatchers.IO) {
            app.pendingStore.expire()
            used = app.pendingStore.used
            cache = app.imageEnvelopes.used
        }
    }
    androidx.compose.runtime.CompositionLocalProvider(LocalOnSoft provides false) {
        Column(Modifier.fillMaxSize().background(p.paper)) {
            PageBar("设置", onBack)
            GLabel("附件存储 · 这台手机")
            Group(
                rows =
                    listOf(
                        { GRow("待发和待确认附件", "${imageBytes(used)} / 256 MiB") },
                        {
                            Text(
                                "图片草稿和文件来源信息闲置 7 天后清理；待确认消息保留到确认或移除。文件正文不复制到手机，容量不足时保留已有内容。",
                                style = T.s12,
                                color = p.ink2,
                                modifier = Modifier.padding(horizontal = 14.dp, vertical = 10.dp),
                            )
                        },
                        { GRow("查看缓存", "${imageBytes(cache)} / 150 MiB") },
                        {
                            GRow(
                                if (action.running) "正在清理…" else "清理查看缓存",
                                onClick = {
                                    action.run(
                                        {
                                            withContext(Dispatchers.IO) {
                                                app.imageEnvelopes.clear()
                                            }
                                            null
                                        },
                                    ) {
                                        stamp++
                                    }
                                },
                                action = true,
                                enabled = !action.running,
                            )
                        },
                    ),
            )
            if (action.error.isNotEmpty())
                Notice(action.error, Modifier.padding(horizontal = 16.dp), NoticeTone.ERROR)
        }
    }
}
