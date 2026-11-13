package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

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
import io.github.lewin671.remoteagent.core.StatusText
import io.github.lewin671.remoteagent.core.machineRings
import androidx.compose.foundation.relocation.bringIntoViewRequester
import io.github.lewin671.remoteagent.core.Mode
import io.github.lewin671.remoteagent.core.PairingUiState
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.agentInfo
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.defaultsFor
import io.github.lewin671.remoteagent.ui.displayError
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
    val copy = uiText()
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
    val t = now()
    androidx.compose.runtime.CompositionLocalProvider(LocalOnSoft provides false) {
        Column(
            Modifier.fillMaxSize()
                .background(p.paper)
                .verticalScrollFaded()
                .readableColumn(),
        ) {
            BackRow(nav.back)
            LargeTitle(copy(UiMessages.commonSettings()))
            ConnBanner(state)
            if (state.clockWarning)
                Notice(
                    copy(UiMessages.settingsAndroidThePhoneClockIsAheadOfTheServerRemoteControl()),
                    Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
                    NoticeTone.WARN,
                )
            MachinesGroup(state, t)
            GLabel(copy(UiMessages.securityClient()))
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
                                 copy = copy),
                                copy(UiMessages.settingsCurrent()),
                                lead = { Ico(clientIcon(state.devices[me]?.meta)) },
                                onClick = { nav.client(me) },
                            )
                        },
                    ) +
                        others.map { c ->
                            @Composable {
                                GRow(
                                    io.github.lewin671.remoteagent.core.clientName(c, c.id, state.devices.values, copy = copy),
                                    copy(UiMessages.securityPaired()),
                                    lead = { Ico(clientIcon(c.meta)) },
                                    onClick = { nav.client(c.id) },
                                    chevron = true,
                                )
                            }
                        } +
                        listOf(
                            @Composable {
                                GRow(
                                    copy(UiMessages.settingsAllClients()),
                                    onClick = { nav.devices() },
                                    action = true,
                                    chevron = true,
                                )
                            },
                        ),
            )
            GLabel(copy(UiMessages.securityAccountSecurity()))
            Group(rows = listOf({ GRow(copy(UiMessages.securityAccountSecurity()), if (state.isAuthority) copy(UiMessages.securityThisDeviceManagesTheAccount()) else uiText(state.authorityName), onClick = { nav.recovery() }, chevron = true) }))
            if (state.isAuthority && state.recoveryPending)
                Notice(copy(UiMessages.settingsAndroidRecoveryPendingEligibleAtValue1((io.github.lewin671.remoteagent.core.securityTime(state.recoveryReadyMs, t, copy = copy)).toString())), Modifier.padding(horizontal = 16.dp, vertical = 6.dp), NoticeTone.WARN, action = copy(UiMessages.dockHandle()), onAction = { nav.recovery() })
            GLabel(copy(UiMessages.settingsNotificationsOnThisClient()))
            Group(
                rows =
                    listOf(
                        {
                            ToggleRow(copy(UiMessages.settingsWhenINeedToAct()), "", settings.notifyAttention) { v ->
                                scope.launch {
                                    app.prefs.update { it.copy(notifyAttention = v) }
                                    Push.register(app, remote)
                                }
                            }
                        },
                        {
                            ToggleRow(copy(UiMessages.settingsWhenATurnEnds()), "", settings.notifyTurnEnded) { v ->
                                scope.launch {
                                    app.prefs.update { it.copy(notifyTurnEnded = v) }
                                    Push.register(app, remote)
                                }
                            }
                        },
                        {
                            ToggleRow(copy(UiMessages.settingsScheduledTaskResults()), "", settings.notifyScheduled) { v ->
                                scope.launch {
                                    app.prefs.update { it.copy(notifyScheduled = v) }
                                    Push.register(app, remote)
                                }
                            }
                        },
                        {
                            ToggleRow(
                                copy(UiMessages.settingsAndroidShowContentAndApproveInNotifications()),
                                copy(UiMessages.settingsAndroidApprovalCommandsWillAppearInSystemNotificationsAndMayRemain()),
                                settings.showContent,
                            ) { v ->
                                scope.launch { app.prefs.update { it.copy(showContent = v) } }
                            }
                        },
                    ),
            )
            PushNotice(push) { Push.fix(app, remote) }
            GLabel(copy(UiMessages.settingsAndroidLocalStorage()))
            Group(rows = listOf { GRow(copy(UiMessages.settingsAttachmentStorage()), onClick = nav.phoneStorage, chevron = true) })
            GLabel(copy(UiMessages.settingsAppearance()))
            Group(
                rows =
                    listOf {
                        val opts =
                            listOf(
                                ThemeMode.SYSTEM to copy(UiMessages.modesAuto()),
                                ThemeMode.LIGHT to copy(UiMessages.settingsLight()),
                                ThemeMode.DARK to copy(UiMessages.settingsDark()),
                            )
                        Row(
                            Modifier.fillMaxWidth()
                                .heightIn(min = 50.dp)
                                .padding(horizontal = 14.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Text(
                                copy(UiMessages.settingsTheme()),
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
            GLabel(copy(UiMessages.settingsAndroidVersion()))
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
                                    updateWord(update, copy = copy) ?: version,
                                    onClick = { updating = true },
                                    chevron = true,
                                    valueColor = if (updateWord(update, copy = copy) != null) p.blue else null,
                                )
                        },
                        { GRow(copy(UiMessages.settingsAndroidOpenSourceLicenses()), onClick = nav.licenses, chevron = true) },
                    ),
            )
            GLabel(copy(UiMessages.settingsAccount()))
            Group(
                rows =
                    listOf(
                        { GRow(copy(UiMessages.securityServer()), remote.account.server.removePrefix("https://")) },
                        {
                            GRow(
                                copy(UiMessages.homeConnectionCheck()),
                                connWord(state, copy = copy),
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
    val copy = uiText()
    val nav = LocalNav.current
    val p = pal
    GLabel(copy(UiMessages.settingsComputer()), copy(UiMessages.settingsAllDevices()), onAction = { nav.devices() })
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
            Text(copy(UiMessages.homeAddDevice()), style = T.s17b, color = p.blue)
        }
    else Hint(
        if (machines.isEmpty()) noMachinesHint(state, copy = copy) else addDeviceHint(state, copy = copy),
    )
    Spacer(Modifier.height(12.dp))
}

/** One computer's card: gbg, radius 28, outlined only when it is away. */
@Composable
private fun MachineCard(state: RemoteState, m: io.github.lewin671.remoteagent.core.MachineView, t: Long, onClick: () -> Unit) {
    val copy = uiText()
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
                if (m.online) copy(UiMessages.settingsAndroidOnline()) else io.github.lewin671.remoteagent.core.ago(m.lastSeenMs, t, copy = copy).let { if (it.isEmpty()) copy(UiMessages.settingsOffline()) else copy(UiMessages.settingsOnline((it).toString())) },
                style = T.s13, color = p.ink2, maxLines = 1,
            )
        }
        if (m.online) for (g in io.github.lewin671.remoteagent.core.quotaGroups(m, t, copy = copy)) {
            val row = g.rows.filter { it.percent != null }.maxByOrNull { it.percent!! } ?: continue
            val left = (100 - row.percent!!).coerceIn(0.0, 100.0)
            val low = left <= 20.0
            Column(Modifier.padding(top = 14.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically) {
                        AgentMarkLead(g.agent, T.s15b, p.ink)
                        Text(agentName(g.agent, copy = copy), style = T.s15b, color = p.ink, maxLines = 1)
                    }
                    Text(copy(UiMessages.quotaLeft((left.roundToInt()).toString())), style = if (low) T.s13b else T.s13, color = if (low) p.orangeInk else p.ink2)
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
    val copy = uiText()
    if (push.problem.isEmpty()) return
    Notice(
        push.problem + copy(UiMessages.settingsAndroidOnlyAffectsSystemNotificationsWhileTheAppIsInThe()),
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
fun DeviceScreen(
    id: String,
    readSettings: suspend (io.github.lewin671.remoteagent.core.Remote, String) -> io.github.lewin671.remoteagent.proto.GetSettingsResponse = { remote, machine ->
        remote.machineCommand(machine, remote.cmd { getSettings = GetSettingsRequest.getDefaultInstance() }).getSettings
    },
    scrollToStorage: Boolean = false,
) {
    val copy = uiText()
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
    val storageAt = remember { androidx.compose.foundation.relocation.BringIntoViewRequester() }
    var attempt by remember { mutableStateOf(0) }
    LaunchedEffect(id, member, m?.online, attempt) {
        if (!member || m?.online != true) return@LaunchedEffect
        try {
            val reply = readSettings(remote, id)
            settings = reply.settings
            if (reply.hasImageStorage()) imageStorage = reply.imageStorage
            loadError = ""
        } catch (e: Exception) {
            loadError = displayError(e, remote.state.value)
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
            PageNote(copy(UiMessages.settingsAndroidThisComputerIsOutsideTheCurrentTrustedDirectoryCheckThe()))
            return@PageScaffold
        }
        Column(Modifier.verticalScrollFaded()) {
            // 电脑状态 (ui-spec A2): the card at the top of the page, only for a computer that has it.
            if (member && m.meta?.capabilities?.machineStatus == true) {
                val fetch = LocalStatusFetch.current
                val feed = rememberMachineFeed(id, 10_000, true, m.online) { fetch(remote, id) }
                val text = StatusText(copy, LocalMessageLocale.current)
                MachineRingsCard(machineRings(feed.last, m.online, text), m.online, onOpen = { nav.machineStatus(id) })
            }
            if (!member)
                Notice(
                    copy(UiMessages.settingsAndroidRemovedRetainedSessionArchive()),
                    Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp),
                )
            else if (!m.online)
                Notice(
                    copy(UiMessages.settingsAndroidOfflineLastOnlineValue1((whenText(m.lastSeenMs, copy = copy)).toString())),
                    Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp),
                )
            val meta = m.meta
            val groups =
                io.github.lewin671.remoteagent.core.quotaGroups(m, System.currentTimeMillis(), copy = copy)
            // A line for each CLI's quota, leading to all of it.
            GLabel(copy(UiMessages.settingsAccountQuota()))
            Group(
                rows =
                    groups.map { g ->
                        @Composable {
                            val qline = io.github.lewin671.remoteagent.core.quotaLine(g, copy = copy)
                            val sepIdx = qline.indexOf(" · ")
                            if (sepIdx != -1) {
                                GRow(
                                    g.title.substringBefore(" · "),
                                    icon = AgentMarks.of(g.agent),
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
                                    icon = AgentMarks.of(g.agent),
                                    value = qline,
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
                                    copy(UiMessages.settingsCliVersion((agentName(a.agent, copy = copy)).toString())),
                                    trailing = {
                                        Text(
                                            androidx.compose.ui.text.buildAnnotatedString {
                                                if (!a.verified) {
                                                    pushStyle(
                                                        androidx.compose.ui.text.SpanStyle(
                                                            color = p.orangeInk,
                                                        ),
                                                    )
                                                    append(copy(UiMessages.settingsAndroidUnverified()))
                                                    pop()
                                                    append(" · ")
                                                }
                                                append(a.version.ifEmpty { copy(UiMessages.settingsAndroidUnknown()) })
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
                        copy(UiMessages.settingsAndroidForAnUnverifiedVersionRunRemoteAgentDoctorOnThe()),
                        Modifier.padding(bottom = 12.dp),
                    )
            }
            val s = settings
            when {
                !m.online ->
                    Hint(
                        copy(UiMessages.settingsAndroidValue1IsOfflineConnectToReadAndEditNewSession((machineLabel(state, id)).toString())),
                        Modifier.padding(bottom = 12.dp),
                    )
                loadError.isNotEmpty() ->
                    Notice(
                        copy(UiMessages.settingsReadError((loadError).toString())),
                        Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp),
                        NoticeTone.ERROR,
                        action = uiText(UiMessages.commonRetry()),
                        onAction = { attempt++ },
                    )
                s == null ->
                    Hint(copy(UiMessages.projectsReadingDirectory()), Modifier.padding(bottom = 12.dp))
                else -> {
                    val mAgents = machineAgents(m)
                    val currentAgent = if (activeAgent in mAgents) activeAgent!! else mAgents.firstOrNull() ?: DEFAULT_AGENT
                    GLabel(
                        if (mAgents.size > 1) copy(UiMessages.settingsNewProjectDefaults())
                        else copy(UiMessages.settingsNewProjectDefaultsSettings((agentName(currentAgent, copy = copy)).toString()))
                    )
                    if (mAgents.size > 1) {
                        Segmented(
                            mAgents.map { agentName(it, copy = copy) },
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
                                    copy(UiMessages.modelsTitle()),
                                    d.model.ifEmpty { copy(UiMessages.modelsDefault()) },
                                    onClick = if (save.running) null else ({ pickModel = currentAgent }),
                                    chevron = true,
                                )
                            },
                            @Composable {
                                GRow(
                                    copy(UiMessages.modelsEffort()),
                                    d.effort.ifEmpty { copy(UiMessages.modelsDefault()) },
                                    onClick = if (save.running) null else ({ pickModel = currentAgent }),
                                    chevron = true,
                                )
                            },
                            @Composable {
                                GRow(
                                    copy(UiMessages.modesTitle()),
                                    cur?.text?.let(copy) ?: modeLabel(null, eff.copy(varies = false), currentAgent, copy = copy),
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
                                    copy(UiMessages.settingsApprovalTimeout()),
                                    copy(UiMessages.quotaMin((s.approvalTimeoutSeconds / 60).toInt())),
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
                                    copy(UiMessages.settingsAutomaticallyEndIdleSessions()),
                                    idleCloseLabel(s.idleCloseMinutes, copy = copy),
                                    onClick = if (save.running) null else ({ pickIdleClose = true }),
                                    chevron = true,
                                )
                            },
                        ),
                    )
                    Text(
                        copy(UiMessages.settingsAndroidSessionsEndAutomaticallyAfterProlongedInactivityNotRunningNoPending()),
                        style = T.s13,
                        color = p.ink2,
                        modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 12.dp),
                    )
                }
            }
            if (save.error.isNotEmpty())
                Notice(
                    copy(UiMessages.settingsSaveError((save.error).toString())),
                    Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp),
                    NoticeTone.ERROR,
                )
            settings?.let { s ->
                Box(Modifier.bringIntoViewRequester(storageAt)) {
                    Column {
                        MachineImageStorage(
                            s,
                            imageStorage,
                            m.online && !save.running,
                            onChange = { next, clean -> update(next, clean) },
                        )
                    }
                }
                // 电脑状态 › 磁盘 and 「remote-agent 的占用」 lead here (ui-spec A3).
                LaunchedEffect(scrollToStorage) {
                    if (scrollToStorage) { androidx.compose.runtime.withFrameNanos { }; storageAt.bringIntoView() }
                }
            }
            val projects = io.github.lewin671.remoteagent.core.machineProjects(state, id)
            if (projects.isNotEmpty()) {
                GLabel(copy(UiMessages.scheduleProject()))
                Group(
                    rows =
                        projects.map { cwd ->
                            @Composable {
                                GRow(
                                    projectName(cwd, copy = copy),
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
                                copy(UiMessages.settingsAndroidRejoinThisComputerAndResumeSessions()),
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
                Hint(copy(UiMessages.settingsAndroidReconnectArchivesOrDeleteRecordsOnTheManagingClientValue1(state.authorityName.resolve(copy))))
            if (member) {
                if (id == state.authorityDeviceId) Hint(copy(UiMessages.settingsAndroidThisIsTheManagingDeviceOrdinaryRemovalIsUnavailable()))
                else if (state.isAuthority)
                    Group(
                        rows =
                            listOf {
                                GRow(
                                    // It asks first, as every row that ends in an ellipsis does (design-system §8).
                                    copy(UiMessages.settingsAndroidRemoveThisMachine()),
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
                Hint(
                    if (state.isAuthority) copy(UiMessages.settingsAndroidRemovalImmediatelyDisconnectsItAndKeepsSessionsAndImagesOn())
                    else copy(UiMessages.settingsAndroidRemoveDevicesOnTheManagingClientValue1(state.authorityName.resolve(copy))),
                    Modifier.padding(bottom = 8.dp),
                )
            }
            if (remove.error.isNotEmpty())
                Notice(remove.error, Modifier.padding(16.dp), NoticeTone.ERROR)
            Spacer(Modifier.height(24.dp))
        }
    }
    if (confirm && member) {
        val (title, text) = removalDialog(machineLabel(state, id), machine = true, copy = copy)
        Alert(
            title,
            text,
            uiText(UiMessages.commonRemove()),
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
                copy(UiMessages.settingsAndroidValue1ApprovalMode((agentName(agent, copy = copy)).toString())),
                opts.map { mode ->
                    val desc = when {
                        mode == null -> copy(UiMessages.settingsWhenUnspecifiedUses()) + modeLabel(null, eff, agent, includeDefault = false, copy = copy)
                        mode == Mode.AUTO && noAuto -> if (allNoAuto) copy(UiMessages.modesAccountUnsupported()) else copy(UiMessages.modesModelUnsupported())
                        else -> modeHelp.getValue(mode).replace("CLI", agentName(agent, copy = copy))
                    }
                    (mode?.text?.let(copy) ?: copy(UiMessages.modelsDefault())) to desc
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
            copy(UiMessages.settingsApprovalTimeout()),
            mins.map { copy(UiMessages.quotaMin((it).toInt())) to null },
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
            copy(UiMessages.settingsAutomaticallyEndIdleSessions()),
            IDLE_CLOSE_CHOICES.map { (if (it == IDLE_CLOSE_DEFAULT) copy(UiMessages.settingsAndroidValue1Default((idleCloseLabel(it, copy = copy)).toString())) else idleCloseLabel(it, copy = copy)) to null },
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
internal fun idleCloseLabel(minutes: Int, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): String {
    val m = if (minutes == 0) IDLE_CLOSE_DEFAULT else minutes
    return when {
        m < 0 -> copy(UiMessages.commonClose())
        m % 60 == 0 -> copy(UiMessages.quotaHr((m / 60).toInt()))
        else -> copy(UiMessages.quotaMin((m).toInt()))
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
                        io.github.lewin671.remoteagent.core.QuotaTone.STALE -> p.faint
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
    val copy = uiText()
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
            error = displayError(e, remote.state.value)
        }
    }
    val d = defaultsFor(s, agent)
    var model by remember { mutableStateOf(d.model) }
    var all by remember { mutableStateOf(false) }
    Sheet(onClose) {
        SheetHead(copy(UiMessages.settingsAndroidValue1DefaultModelForNewSessions((agentName(agent, copy = copy)).toString())))
        val ms = models
        when {
            error.isNotEmpty() -> Notice(copy(UiMessages.sessionAndroidModelsFailed((error).toString())), tone = NoticeTone.ERROR)
            ms == null ->
                Text(
                    copy(UiMessages.projectsReadingDirectory()),
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
                    copy(UiMessages.settingsAndroidDefaultValue1SOwnConfiguration((agentName(agent, copy = copy)).toString())),
                    checked = model.isEmpty(),
                    last = shown.isEmpty(),
                    onClick = { onPick("", "") },
                )
                shown.forEachIndexed { i, m ->
                    ListItem(
                        m.displayName.ifEmpty { m.slug },
                        small =
                            if (m.effortsList.size == 1) copy(UiMessages.modelsFixedEffort((m.effortsList[0]).toString()))
                            else
                                listOfNotNull(
                                    io.github.lewin671.remoteagent.core.contextText(
                                        m.contextWindow,
                                     copy = copy),
                                    if (m.effortsList.isEmpty()) copy(UiMessages.sessionNoEffort()) else null,
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
                        FieldLabel(copy(UiMessages.modelsEffort()))
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
    val copy = uiText()
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val m = state.machines[id]
    val p = pal
    val t = now()
    val groups = m?.let { io.github.lewin671.remoteagent.core.quotaGroups(it, t, copy = copy) }.orEmpty()
    PageScaffold(
        copy(UiMessages.settingsAccountQuota()),
        subtitle = m?.let { machineLabel(state, id) + " · " + if (it.online) copy(UiMessages.settingsAndroidOnlineB90866()) else copy(UiMessages.settingsOffline()) },
        readable = true,
    ) {
        if (m == null) {
            PageNote(copy(UiMessages.settingsAndroidThisMachineHasBeenRemoved()))
            return@PageScaffold
        }
        Column(Modifier.verticalScrollFaded()) {
            if (!m.online)
                Text(
                    copy(UiMessages.settingsComputerOfflineShowingTheLastReading()),
                    style = T.s13,
                    color = p.orangeInk,
                    modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 6.dp),
                )
            for (g in groups) {
                if (g.status == io.github.lewin671.remoteagent.core.QuotaStatus.FAILED)
                    Text(
                        uiText(if (g.detail.isNotEmpty()) UiMessages.quotaFailure(g.detail, agentName(g.agent, copy = copy)) else UiMessages.quotaFailureNoReason(agentName(g.agent, copy = copy))),
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
                    GLabel(g.title + if (g.lastRecord) copy(UiMessages.quotaLastReading()) else "")
                    val rows = mutableListOf<@Composable () -> Unit>()
                    when (g.status) {
                        io.github.lewin671.remoteagent.core.QuotaStatus.UNSUPPORTED ->
                            rows +=
                                @Composable {
                                    Column(Modifier.padding(horizontal = 14.dp, vertical = 10.dp)) {
                                        Text(copy(UiMessages.quotaQuotaLookupIsNotSupportedYet()), style = T.s15b, color = p.ink)
                                        Text(
                                            g.detail.ifEmpty {
                                                copy(UiMessages.settingsAndroidRemoteAgentOnThisComputerCannotReadValue1Quotas((agentName(g.agent, copy = copy)).toString()))
                                            } + agentInfo(g.agent).usageHint?.resolve(copy).orEmpty(),
                                            style = T.s12,
                                            color = p.ink2,
                                        )
                                    }
                                }
                        io.github.lewin671.remoteagent.core.QuotaStatus.WAITING ->
                            rows +=
                                @Composable {
                                    Column(Modifier.padding(horizontal = 14.dp, vertical = 10.dp)) {
                                        Text(copy(UiMessages.quotaQuotaNotAvailableYet()), style = T.s15b, color = p.ink)
                                        Text(
                                            g.detail.ifEmpty { uiText(UiMessages.quotaWaiting(agentName(g.agent, copy = copy))) },
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
                // The warning stays next to its card, before observation metadata.
                io.github.lewin671.remoteagent.core.quotaBanner(g, copy = copy)?.let {
                    Text(uiText(it), style = T.s13, color = p.orangeInk,
                        modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 10.dp))
                }
                if (g.rows.isNotEmpty()) {
                    g.freshness?.let {
                        Text(uiText(it), style = T.s12, color = p.ink2,
                            modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 10.dp))
                    }
                    g.received?.let {
                        Text(uiText(it), style = T.s12, color = p.ink2,
                            modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 10.dp))
                    }
                }
            }
            Text(
                copy(UiMessages.settingsTimesAreShownInBeijingTimeQuotasBelongTo()),
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
    val copy = uiText()
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
            copy(UiMessages.settingsAndroidRecoveryCodeQrCode()),
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
    Hint(copy(UiMessages.securitySaveTheRecoveryCodeOfflineAnyoneWithItCan()))
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
    val copy = uiText()
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val settings by RaApp.instance.prefs.settings.collectAsState(Settings())
    val p = pal
    val model = remember(remote, replacesDeviceId) { RaApp.instance.pairing(remote, replacesDeviceId) }
    val state by model.state.collectAsState()
    AuthorityStartEffect(model) { model.enter() }
    DisposableEffect(model) { onDispose { model.leave() } }
    PageScaffold(copy(UiMessages.homeAddDevice())) {
        FormColumn(
            Modifier.fillMaxWidth()
            .verticalScrollFaded(),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Hint(copy(UiMessages.securityComputerRunRemoteAgentAccountJoinAndEnterThe()))
            if (replacesDeviceId.isNotEmpty()) {
                val current by remote.state.collectAsState()
                Hint(
                    copy(UiMessages.settingsAndroidAllowTheOriginalComputerToResumeValue1SRetainedSessions((machineLabel(current, replacesDeviceId)).toString())),
                )
            } else {
                Hint(copy(UiMessages.settingsAndroidToResumeARemovedComputerSSessionsOpenItsRetained()))
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
                            copy(UiMessages.settingsAndroidPairingQrCode()),
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
                        copy(UiMessages.securityPairValidUntil(io.github.lewin671.remoteagent.core.securityTime(step.expiresMs, timeOnly = true, copy = copy))),
                        style = T.s13,
                        color = p.ink2,
                        textAlign = TextAlign.Center,
                        modifier = Modifier.padding(top = 10.dp),
                    )
                    IconText(
                        Lucide.LoaderCircle,
                        copy(UiMessages.settingsAndroidWaitingForANewDevice()),
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
                                if (kind == DeviceKind.DEVICE_KIND_MACHINE) copy(UiMessages.settingsAndroidNewComputer()) else copy(UiMessages.settingsAndroidNewDevice()),
                                style = T.s13b,
                                color = p.ink,
                            )
                            Text(copy(UiMessages.settingsAndroidRequestsToJoin()), style = T.s13, color = p.ink2)
                        }
                        Text(
                            emoji.joinToString("  "),
                            fontSize = 30.sp,
                            modifier = Modifier.padding(top = 10.dp, bottom = 12.dp),
                        )
                        Text(
                            if (sending) copy(UiMessages.settingsAndroidCompareThese4PatternsOnTheNewDeviceAndConfirm())
                            else copy(UiMessages.securityCompareThe4PatternsOnTheNewDeviceConfirm()),
                            style = T.s13,
                            color = p.ink2,
                            textAlign = TextAlign.Center,
                            modifier = Modifier.padding(bottom = 10.dp),
                        )
                        Btns {
                            PillButton(
                                copy(UiMessages.approvalAndroidDenyDecision()),
                                {
                                    model.leave()
                                    nav.back()
                                },
                                Modifier.weight(1f),
                                ButtonKind.SECONDARY,
                                enabled = !sending,
                            )
                            PillButton(
                                if (sending) copy(UiMessages.settingsAndroidWaitingForTheNewDeviceToConfirm()) else copy(UiMessages.securityAllowToJoin()),
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
                        copy(UiMessages.settingsAndroidNewDeviceJoined()),
                        Modifier.padding(top = 14.dp),
                        action = uiText(UiMessages.commonDone()),
                        onAction = nav.back,
                        icon = Lucide.Check,
                    )
                is PairingUiState.Failed ->
                    Notice(
                        uiText(step.message),
                        Modifier.padding(top = 14.dp),
                        if (step.uncertain) NoticeTone.PLAIN else NoticeTone.ERROR,
                        action = if (step.canRetry) uiText(UiMessages.commonRetry()) else copy(UiMessages.securityStartOver()),
                        onAction = model::retry,
                    )
            }
        }
    }
}

private fun imageBytes(n: Long, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText) =
    if (n in 1 until 104858) copy(UiMessages.settingsLessThan01Mib())
    else if (n >= 1L shl 30) "%.1f GiB".format(n / 1073741824.0)
    else "%.1f MiB".format(n / 1048576.0)

private fun mib(n: Long) =
    if (n >= 1L shl 30 && n % (1L shl 30) == 0L) "${n shr 30} GiB" else "${n shr 20} MiB"

@Composable
internal fun MachineImageStorage(
    settings: DaemonSettings,
    usage: ImageStorageUsage?,
    enabled: Boolean,
    onChange: (DaemonSettings, Boolean) -> Unit,
) {
    val copy = uiText()
    var clean by remember { mutableStateOf(false) }
    var limit by remember { mutableStateOf(false) }
    val free = usage?.let { it.reclaimableBytes + it.cacheBytes } ?: 0L
    // What is worth saying: sizes that are not zero.
    val parts =
        usage
            ?.let { u ->
                listOfNotNull(
                        if (u.reclaimableBytes > 0) copy(UiMessages.settingsCanClear((imageBytes(u.reclaimableBytes, copy = copy)).toString()))
                        else null,
                        if (u.reservedBytes > 0) copy(UiMessages.settingsUploading((imageBytes(u.reservedBytes, copy = copy)).toString())) else null,
                    )
                    .joinToString(" · ")
            }
            .orEmpty()
    GLabel(copy(UiMessages.settingsAttachmentStorage()))
    Group(
        rows =
            listOf(
                {
                    GRow(
                        copy(UiMessages.settingsUploadedImagesAndFiles()),
                        usage?.let {
                            imageBytes(it.uploadBytes, copy = copy) + if (parts.isNotEmpty()) " · $parts" else ""
                        } ?: copy(UiMessages.projectsReadingDirectory()),
                    )
                },
                {
                    GRow(
                        copy(UiMessages.settingsStorageLimit()),
                        mib(settings.imageUploadQuotaBytes),
                        onClick = if (enabled) ({ limit = true }) else null,
                        chevron = true,
                    )
                },
                { GRow(copy(UiMessages.settingsPreviewCache()), usage?.let { imageBytes(it.cacheBytes, copy = copy) } ?: "—") },
                *(if (usage != null && usage.emergencyReserveBytes > 0) arrayOf<@Composable () -> Unit>({
                    // 应急预留 (ui-spec B2): not clearable, so no action; the line under it says why it is there.
                    Column(Modifier.fillMaxWidth().padding(start = 18.dp, end = 16.dp, top = 11.dp, bottom = 11.dp)) {
                        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                            Text(copy(UiMessages.machineStatusEmergencyReserve()), style = T.s17, color = pal.ink, modifier = Modifier.weight(1f))
                            Text(mib(usage.emergencyReserveBytes), style = T.s17, color = pal.ink2)
                        }
                        Text(copy(UiMessages.machineStatusEmergencyReserveNote()), style = T.s13, color = pal.ink2, modifier = Modifier.padding(top = 2.dp))
                    }
                }) else emptyArray()),
                {
                    GRow(
                        copy(UiMessages.settingsClear()),
                        usage
                            ?.let { if (free > 0) copy(UiMessages.settingsCanFree((imageBytes(free, copy = copy)).toString())) else copy(UiMessages.settingsNothingToClear()) }
                            .orEmpty(),
                        onClick = if (enabled && free > 0) ({ clean = true }) else null,
                        action = true,
                        enabled = enabled && free > 0,
                    )
                },
            ),
    )
    Text(
        copy(UiMessages.settingsAttachmentsAreStoredOnThisComputerSessionFilesRemain()),
        style = T.s13,
        color = pal.ink2,
        modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 12.dp),
    )
    if (limit) {
        val opts = listOf(256L shl 20, 1L shl 30, 5L shl 30)
        ChoiceSheet(
            copy(UiMessages.settingsStorageLimit()),
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
            copy(UiMessages.settingsAndroidCleanUpReclaimableImages()),
            copy(UiMessages.settingsAndroidRemoveReclaimableOriginalsAndCacheLargeImagesNotYetSynced()),
            copy(UiMessages.settingsClearSettings()),
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
fun addDeviceHint(state: RemoteState, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText) = copy(UiMessages.securityAddOnAuthority(state.authorityName.resolve(copy)))

/** An account with no computer, seen from a client that cannot add one. */
fun noMachinesHint(state: RemoteState, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText) = copy(UiMessages.homeAddViaManager(state.authorityName.resolve(copy)))

private fun bundledFonts(copy: io.github.lewin671.remoteagent.core.MessageResolver) =
    listOf(
        Triple("Inter", copy(UiMessages.settingsAndroidLatinTextAndNumbersInTheInterface()), io.github.lewin671.remoteagent.R.raw.ofl_inter),
        Triple(
            "JetBrains Mono",
            copy(UiMessages.settingsAndroidCodePathsCommandsAndTerminal()),
            io.github.lewin671.remoteagent.R.raw.ofl_jetbrains_mono,
        ),
        Triple(
            copy(UiMessages.settingsAndroidSarasaFixedScSymbolSubset()),
            copy(UiMessages.settingsAndroidTerminalSymbols()),
            io.github.lewin671.remoteagent.R.raw.ofl_sarasa_gothic,
        ),
    )

/** The icons, drawn from Lucide's paths, with its ISC text from res/raw. */
private fun bundledIcons(copy: io.github.lewin671.remoteagent.core.MessageResolver) =
    listOf(
        Triple("Lucide", copy(UiMessages.settingsAndroidInterfaceIcons()), io.github.lewin671.remoteagent.R.raw.isc_lucide),
    )

@Composable
fun LicensesPage(onBack: () -> Unit) {
    val copy = uiText()
    val p = pal
    val res = androidx.compose.ui.platform.LocalContext.current.resources
    var open by remember { mutableStateOf<Int?>(null) }
    androidx.compose.runtime.CompositionLocalProvider(LocalOnSoft provides false) {
        Column(Modifier.fillMaxSize().background(p.paper)) {
            PageBar(copy(UiMessages.settingsAndroidOpenSourceLicenses()), onBack)
            Column(Modifier.verticalScrollFaded()) {
                for ((label, items) in
                    listOf(
                        copy(UiMessages.settingsAndroidFontsSilOpenFontLicense11()) to bundledFonts(copy),
                        copy(UiMessages.settingsAndroidIconsIscLicense()) to bundledIcons(copy),
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
    val copy = uiText()
    val app = RaApp.instance
    val p = pal
    val action = rememberAction()
    var stamp by remember { mutableStateOf(0) }
    var used by remember { mutableStateOf(0L) }
    var cache by remember { mutableStateOf(0L) }
    var received by remember { mutableStateOf(0L) }
    LaunchedEffect(stamp) {
        withContext(Dispatchers.IO) {
            app.pendingStore.expire()
            used = app.pendingStore.used
            cache = app.imageEnvelopes.used
            received = app.sharedFiles.usedBytes()
        }
    }
    androidx.compose.runtime.CompositionLocalProvider(LocalOnSoft provides false) {
        Column(Modifier.fillMaxSize().background(p.paper)) {
            PageBar(copy(UiMessages.commonSettings()), onBack)
            GLabel(copy(UiMessages.settingsAndroidAttachmentStorageThisPhone()))
            Group(
                rows =
                    listOf(
                        { GRow(copy(UiMessages.settingsAndroidPendingAndUnconfirmedAttachments()), "${imageBytes(used)} / 256 MiB") },
                        {
                            Text(
                                copy(UiMessages.settingsAndroidImageDraftsAndFileSourceInformationAreClearedAfter7()),
                                style = T.s12,
                                color = p.ink2,
                                modifier = Modifier.padding(horizontal = 14.dp, vertical = 10.dp),
                            )
                        },
                        { GRow(copy(UiMessages.settingsAndroidViewingCache()), "${imageBytes(cache)} / 150 MiB") },
                        { GRow(copy(UiMessages.sharedFileStorageReceivedFiles()), imageBytes(received)) },
                        {
                            GRow(
                                if (action.running) copy(UiMessages.settingsAndroidCleaning()) else copy(UiMessages.settingsAndroidClearViewingCache()),
                                onClick = {
                                    action.run(
                                        {
                                            withContext(Dispatchers.IO) {
                                                app.imageEnvelopes.clear()
                                                app.sharedFiles.clearAll()
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
