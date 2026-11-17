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
import androidx.compose.runtime.produceState
import kotlinx.coroutines.delay
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
import androidx.compose.foundation.relocation.bringIntoViewRequester
import io.github.lewin671.remoteagent.core.PairingUiState
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.proto.AttachmentStorageView
import io.github.lewin671.remoteagent.core.proto.MachineListCardView
import io.github.lewin671.remoteagent.core.proto.MachinePollMode
import io.github.lewin671.remoteagent.core.proto.MachineSettingsViewModel
import io.github.lewin671.remoteagent.core.proto.QuotaTone
import io.github.lewin671.remoteagent.core.proto.SettingsViewModel
import io.github.lewin671.remoteagent.core.proto.ThemePreference
import io.github.lewin671.remoteagent.data.Settings
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.push.Push
import io.github.lewin671.remoteagent.push.PushStatus
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

private fun clientIconForDescription(desc: String): ImageVector = when {
    desc.contains("iPad", ignoreCase = true) -> Lucide.Tablet
    desc.contains("Web", ignoreCase = true) ||
        desc.contains("Chrome", ignoreCase = true) ||
        desc.contains("Firefox", ignoreCase = true) ||
        desc.contains("Safari", ignoreCase = true) ||
        desc.contains("Edge", ignoreCase = true) -> Lucide.Globe
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
    val vm by remote.settingsView.collectAsState()
    val push by Push.status.collectAsState()
    var checking by remember { mutableStateOf(false) }
    var updating by remember { mutableStateOf(false) }
    LaunchedEffect(Unit) { remote.coreHost.openSettings() }
    val version =
        app.versionName.let { v ->
            val c = app.buildCommit.take(7)
            if (c.isEmpty() || c in v) v else "$v · $c"
        }
    SettingsScreenContent(
        vm = vm,
        state = state,
        push = push,
        serverDisplay = remote.account.server.removePrefix("https://"),
        versionFallback = version,
        updaterEnabled = Updater.enabled(app),
        onBack = nav.back,
        onRecovery = { nav.recovery() },
        onDevices = { nav.devices() },
        onDevice = { nav.device(it) },
        onClient = { nav.client(it) },
        onPair = { nav.pair() },
        onNotifyAttention = { remote.coreHost.reportNotificationPrefs(notifyAttention = it) },
        onNotifyTurnEnded = { remote.coreHost.reportNotificationPrefs(notifyTurnEnded = it) },
        onNotifyScheduled = { remote.coreHost.reportNotificationPrefs(notifyScheduled = it) },
        onShowContent = { remote.coreHost.reportNotificationPrefs(showContent = it) },
        onFixPush = { Push.fix(app, remote) },
        onPhoneStorage = nav.phoneStorage,
        onTheme = { remote.coreHost.setAppearancePref(it) },
        onOpenUpdate = { updating = true },
        onLicenses = nav.licenses,
        onOpenConnCheck = { checking = true },
        readExitPending = { withContext(Dispatchers.IO) { app.vault.readSecurityOperation() != null } },
        onExitAccount = { app.logout() },
    )
    if (checking) ConnCheckSheet(state) { checking = false }
    if (updating) UpdateSheet { updating = false }
}

@Composable
internal fun SettingsScreenContent(
    vm: SettingsViewModel,
    state: RemoteState = RemoteState(),
    push: PushStatus = PushStatus(),
    serverDisplay: String = "",
    versionFallback: String = "",
    updaterEnabled: Boolean = false,
    onBack: () -> Unit = {},
    onRecovery: () -> Unit = {},
    onDevices: () -> Unit = {},
    onDevice: (String) -> Unit = {},
    onClient: (String) -> Unit = {},
    onPair: () -> Unit = {},
    onNotifyAttention: (Boolean) -> Unit = {},
    onNotifyTurnEnded: (Boolean) -> Unit = {},
    onNotifyScheduled: (Boolean) -> Unit = {},
    onShowContent: (Boolean) -> Unit = {},
    onFixPush: () -> Unit = {},
    onPhoneStorage: () -> Unit = {},
    onTheme: (ThemePreference) -> Unit = {},
    onOpenUpdate: () -> Unit = {},
    onLicenses: () -> Unit = {},
    onOpenConnCheck: () -> Unit = {},
    readExitPending: suspend () -> Boolean = { false },
    onExitAccount: suspend () -> Unit = {},
) {
    val copy = uiText()
    val p = pal
    androidx.compose.runtime.CompositionLocalProvider(LocalOnSoft provides false) {
        Column(
            Modifier.fillMaxSize()
                .background(p.paper)
                .verticalScrollFaded()
                .readableColumn(),
        ) {
            BackRow(onBack)
            LargeTitle(copy(UiMessages.commonSettings()))
            ConnBanner(state)
            if (vm.clockSkewed) {
                val skewMsg = uiText(vm.clockSkewBannerText).ifEmpty {
                    copy(UiMessages.settingsAndroidThePhoneClockIsAheadOfTheServerRemoteControl())
                }
                Notice(
                    skewMsg,
                    Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
                    NoticeTone.WARN,
                )
            }
            if (vm.isManager && vm.recoveryPending) {
                val recMsg = uiText(vm.recoveryBannerText)
                if (recMsg.isNotEmpty())
                    Notice(
                        recMsg,
                        Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
                        NoticeTone.ERROR,
                        action = copy(UiMessages.dockHandle()),
                        onAction = onRecovery,
                    )
            }
            MachinesGroup(vm, onDevices = onDevices, onDevice = onDevice, onPair = onPair)
            GLabel(copy(UiMessages.securityClient()))
            Group(
                rows =
                    vm.recentClientsList.map { c ->
                        @Composable {
                            val sub = uiText(c.roleLabel).ifEmpty { uiText(c.lastUsedText) }
                            GRow(
                                c.displayName,
                                sub,
                                lead = { Ico(clientIconForDescription(c.description)) },
                                onClick = { onClient(c.deviceId) },
                                chevron = !c.isSelf,
                            )
                        }
                    } +
                        listOf(
                            @Composable {
                                GRow(
                                    copy(UiMessages.settingsAllClients()),
                                    onClick = onDevices,
                                    action = true,
                                    chevron = true,
                                )
                            },
                        ),
            )
            GLabel(copy(UiMessages.securityAccountSecurity()))
            Group(
                rows = listOf {
                    GRow(
                        copy(UiMessages.securityAccountSecurity()),
                        uiText(vm.security.summaryText),
                        onClick = onRecovery,
                        chevron = true,
                    )
                },
            )
            GLabel(copy(UiMessages.settingsNotificationsOnThisClient()))
            val n = vm.notifications
            Group(
                rows =
                    listOf(
                        {
                            ToggleRow(copy(UiMessages.settingsWhenINeedToAct()), "", n.notifyAttention, onChange = onNotifyAttention)
                        },
                        {
                            ToggleRow(copy(UiMessages.settingsWhenATurnEnds()), "", n.notifyTurnEnded, onChange = onNotifyTurnEnded)
                        },
                        {
                            ToggleRow(copy(UiMessages.settingsScheduledTaskResults()), "", n.notifyScheduled, onChange = onNotifyScheduled)
                        },
                        {
                            ToggleRow(
                                copy(UiMessages.settingsAndroidShowContentAndApproveInNotifications()),
                                copy(UiMessages.settingsAndroidApprovalCommandsWillAppearInSystemNotificationsAndMayRemain()),
                                n.showContent,
                                onChange = onShowContent,
                            )
                        },
                    ),
            )
            PushNotice(push, onAction = onFixPush)
            GLabel(copy(UiMessages.settingsAndroidLocalStorage()))
            Group(rows = listOf { GRow(copy(UiMessages.settingsAttachmentStorage()), onClick = onPhoneStorage, chevron = true) })
            GLabel(copy(UiMessages.settingsAppearance()))
            Group(
                rows =
                    listOf {
                        val opts =
                            listOf(
                                ThemePreference.THEME_PREFERENCE_AUTO to copy(UiMessages.modesAuto()),
                                ThemePreference.THEME_PREFERENCE_LIGHT to copy(UiMessages.settingsLight()),
                                ThemePreference.THEME_PREFERENCE_DARK to copy(UiMessages.settingsDark()),
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
                                opts.indexOfFirst { it.first == vm.appearance.theme }.coerceAtLeast(0),
                            ) { i ->
                                onTheme(opts[i].first)
                            }
                        }
                    },
            )
            GLabel(copy(UiMessages.settingsAndroidVersion()))
            val u = vm.update
            val version = u.currentVersion.ifEmpty { versionFallback }
            val updateStatus = if (u.checking || u.hasUpdate) uiText(u.statusText) else ""
            Group(
                rows =
                    listOf(
                        {
                            // A development copy does not update itself: its row only says what it is.
                            if (!updaterEnabled) GRow("remote-agent", version)
                            else
                                GRow(
                                    "remote-agent",
                                    updateStatus.ifEmpty { version },
                                    onClick = onOpenUpdate,
                                    chevron = true,
                                    valueColor = if (updateStatus.isNotEmpty()) p.blue else null,
                                )
                        },
                        { GRow(copy(UiMessages.settingsAndroidOpenSourceLicenses()), onClick = onLicenses, chevron = true) },
                    ),
            )
            GLabel(copy(UiMessages.settingsAccount()))
            val cc = vm.connCheck
            val connValue = listOf(uiText(cc.statusWord), uiText(cc.summaryText))
                .filter { it.isNotEmpty() }
                .joinToString(" · ")
            Group(
                rows =
                    listOf(
                        { GRow(copy(UiMessages.securityServer()), serverDisplay) },
                        {
                            GRow(
                                copy(UiMessages.homeConnectionCheck()),
                                connValue,
                                onClick = onOpenConnCheck,
                                chevron = true,
                            )
                        },
                        {
                            AccountExitAction(
                                state,
                                readPending = readExitPending,
                                exit = onExitAccount,
                            )
                        },
                    ),
            )
            Spacer(Modifier.height(24.dp))
        }
    }
}

/**
 * Settings' 电脑 (design-system §6 设置): each computer a card with its name,
 * whether it is online, and a line per CLI of how much quota is left with a
 * thin bar, orange at 20% or less. 添加设备 is the row after them; only the
 * authority has it, other clients name it.
 */
@Composable
internal fun MachinesGroup(
    vm: SettingsViewModel,
    onDevices: () -> Unit = {},
    onDevice: (String) -> Unit = {},
    onPair: () -> Unit = {},
) {
    val copy = uiText()
    val p = pal
    GLabel(copy(UiMessages.settingsComputer()), copy(UiMessages.settingsAllDevices()), onAction = onDevices)
    for (m in vm.machinesList) MachineCard(m) { onDevice(m.machineId) }
    val emptyHint = if (vm.machinesList.isEmpty()) uiText(vm.emptyMachinesText) else ""
    if (emptyHint.isNotEmpty()) Hint(emptyHint)
    if (vm.canAddDevice)
        Row(
            Modifier.fillMaxWidth().padding(horizontal = 16.dp).heightIn(min = 52.dp).clip(Radii.Pill).background(p.gbg)
                .clickable(role = androidx.compose.ui.semantics.Role.Button) { onPair() },
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.CenterHorizontally),
        ) {
            Ic(Lucide.Plus, 20.dp, p.blue)
            Text(copy(UiMessages.homeAddDevice()), style = T.s17b, color = p.blue)
        }
    else {
        val addHint = uiText(vm.addDeviceHint)
        if (addHint.isNotEmpty() && addHint !in emptyHint) Hint(addHint)
    }
    Spacer(Modifier.height(12.dp))
}

/** One computer's card: gbg, radius 28, outlined only when it is away. */
@Composable
private fun MachineCard(m: MachineListCardView, onClick: () -> Unit) {
    val p = pal
    Column(
        Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, bottom = 10.dp).clip(Radii.R28)
            .then(if (m.online) Modifier.background(p.gbg) else Modifier.border(0.5.dp, p.sep, Radii.R28))
            .clickable(onClick = onClick).padding(18.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(m.name, style = T.h20, color = p.ink, maxLines = 1, overflow = androidx.compose.ui.text.style.TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            Arrow(p.ink3, T.s17)
        }
        Row(Modifier.padding(top = 3.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            StatusMark(if (m.online) MarkKind.ONLINE else MarkKind.OFF, size = 8.dp)
            Text(
                uiText(m.statusText),
                style = T.s13, color = p.ink2, maxLines = 1,
            )
        }
        if (m.online) for (sec in m.quotaSummariesList) {
            val row = sec.takeIf { it.hasSummaryRow() }?.summaryRow?.takeIf { it.hasPercent } ?: continue
            val left = row.remainingPercent.coerceIn(0, 100)
            val low = row.tone == QuotaTone.QUOTA_TONE_WARN || row.tone == QuotaTone.QUOTA_TONE_FULL
            Column(Modifier.padding(top = 14.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically) {
                        AgentMarkLead(sec.agent, T.s15b, p.ink)
                        Text(sec.agentName, style = T.s15b, color = p.ink, maxLines = 1)
                    }
                    Text(uiText(row.remainingText), style = if (low) T.s13b else T.s13, color = if (low) p.orangeInk else p.ink2)
                }
                Box(Modifier.padding(top = 6.dp).fillMaxWidth().height(5.dp).clip(Radii.Pill).background(p.fill2)) {
                    Box(Modifier.fillMaxWidth((left / 100f).coerceIn(0f, 1f)).height(5.dp).clip(Radii.Pill).background(if (low) p.orange else p.ink))
                }
                val reset = uiText(row.resetText)
                if (reset.isNotEmpty()) Text(reset, style = T.s13, color = p.ink2, modifier = Modifier.padding(top = 4.dp))
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
 * remove. Renders Core's [MachineSettingsViewModel] directly.
 */
@Composable
fun DeviceScreen(
    id: String,
    scrollToStorage: Boolean = false,
) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val vm by remote.machineSettingsView.collectAsState()
    val remove = rememberAction(authority = true)
    LaunchedEffect(id) { remote.coreHost.openMachineSettings(id) }
    if (vm.showStatusEntry) {
        val owner = androidx.lifecycle.compose.LocalLifecycleOwner.current
        DisposableEffect(id, owner) {
            val obs = androidx.lifecycle.LifecycleEventObserver { _, ev ->
                when (ev) {
                    androidx.lifecycle.Lifecycle.Event.ON_START ->
                        remote.coreHost.watchMachineStatus(id, true, MachinePollMode.MACHINE_POLL_MODE_OVERVIEW)
                    androidx.lifecycle.Lifecycle.Event.ON_STOP ->
                        remote.coreHost.watchMachineStatus(id, false, MachinePollMode.MACHINE_POLL_MODE_OVERVIEW)
                    else -> {}
                }
            }
            owner.lifecycle.addObserver(obs)
            if (owner.lifecycle.currentState.isAtLeast(androidx.lifecycle.Lifecycle.State.STARTED)) {
                remote.coreHost.watchMachineStatus(id, true, MachinePollMode.MACHINE_POLL_MODE_OVERVIEW)
            }
            onDispose {
                owner.lifecycle.removeObserver(obs)
                remote.coreHost.watchMachineStatus(id, false, MachinePollMode.MACHINE_POLL_MODE_OVERVIEW)
            }
        }
    }
    DeviceScreenContent(
        vm = vm,
        scrollToStorage = scrollToStorage,
        isAuthority = state.isAuthority,
        appOnline = state.conn == io.github.lewin671.remoteagent.core.ConnState.ONLINE,
        removeRunning = remove.running,
        removeError = remove.error,
        onOpenStatus = { nav.machineStatus(id) },
        onOpenQuota = { nav.quota(id) },
        onToggleDirect = { enabled -> remote.coreHost.updateDirectPreference(id, enabled) },
        onRetrySettings = { remote.coreHost.openMachineSettings(id) },
        onSelectAgent = { agent -> remote.coreHost.selectMachineSettingsAgent(id, agent) },
        onUpdateAgentDefaults = { agent, model, effort, mode ->
            remote.coreHost.updateMachineAgentDefaults(id, agent, model = model, effort = effort, mode = mode)
        },
        onUpdateDaemonSettings = { timeoutSec, idleMin, quotaBytes, clean ->
            remote.coreHost.updateMachineDaemonSettings(
                id,
                approvalTimeoutSeconds = timeoutSec,
                idleCloseMinutes = idleMin,
                imageUploadQuotaBytes = quotaBytes,
                cleanImages = clean,
            )
        },
        onProject = { path -> nav.project(path) },
        onRejoin = { nav.rejoin(id) },
        onRemove = {
            remove.run(
                {
                    remote.removeDevice(id)
                    null
                },
            )
        },
    )
}

@Composable
internal fun DeviceScreenContent(
    vm: MachineSettingsViewModel,
    scrollToStorage: Boolean = false,
    isAuthority: Boolean = false,
    appOnline: Boolean = true,
    removeRunning: Boolean = false,
    removeError: String = "",
    onOpenStatus: () -> Unit = {},
    onOpenQuota: () -> Unit = {},
    onToggleDirect: (Boolean) -> Unit = {},
    onRetrySettings: () -> Unit = {},
    onSelectAgent: (Agent) -> Unit = {},
    onUpdateAgentDefaults: (agent: Agent, model: String?, effort: String?, mode: ApprovalMode?) -> Unit = { _, _, _, _ -> },
    onUpdateDaemonSettings: (approvalTimeoutSeconds: Int?, idleCloseMinutes: Int?, imageUploadQuotaBytes: Long?, cleanImages: Boolean) -> Unit = { _, _, _, _ -> },
    onProject: (String) -> Unit = {},
    onRejoin: () -> Unit = {},
    onRemove: () -> Unit = {},
) {
    val copy = uiText()
    val p = pal
    var confirm by remember(vm.machineId) { mutableStateOf(false) }
    var pickModel by remember(vm.machineId) { mutableStateOf(false) }
    var pickEffort by remember(vm.machineId) { mutableStateOf(false) }
    var pickMode by remember(vm.machineId) { mutableStateOf(false) }
    var pickTimeout by remember(vm.machineId) { mutableStateOf(false) }
    var pickIdleClose by remember(vm.machineId) { mutableStateOf(false) }
    val storageAt = remember { androidx.compose.foundation.relocation.BringIntoViewRequester() }
    val title = vm.name.ifEmpty { vm.machineId }
    PageScaffold(title, readable = true) {
        if (!vm.found) {
            PageNote(copy(UiMessages.settingsAndroidThisComputerIsOutsideTheCurrentTrustedDirectoryCheckThe()))
            return@PageScaffold
        }
        Column(Modifier.verticalScrollFaded()) {
            if (vm.showStatusEntry) {
                MachineRingsCard(vm.statusRingsList, vm.online, onOpen = onOpenStatus)
            }
            val removedText = uiText(vm.removedBanner)
            val offlineOrMismatch = uiText(vm.offlineOrMismatchBanner)
            if (removedText.isNotEmpty()) {
                Notice(
                    removedText,
                    Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp),
                )
            } else if (offlineOrMismatch.isNotEmpty()) {
                Notice(
                    offlineOrMismatch,
                    Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp),
                )
            }
            if (vm.quotaRowsList.isNotEmpty()) {
                GLabel(copy(UiMessages.settingsAccountQuota()))
                Group(
                    rows =
                        vm.quotaRowsList.map { sec ->
                            @Composable {
                                val summary = sec.takeIf { it.hasSummaryRow() }?.summaryRow
                                val rem = summary?.let { uiText(it.remainingText) }.orEmpty()
                                val win = summary?.let { uiText(it.label) }.orEmpty()
                                val fallback = uiText(sec.emptyOrErrorText)
                                GRow(
                                    sec.agentName,
                                    icon = AgentMarks.of(sec.agent),
                                    trailing = {
                                        if (rem.isNotEmpty() && win.isNotEmpty()) {
                                            Text(
                                                androidx.compose.ui.text.buildAnnotatedString {
                                                    append(rem)
                                                    pushStyle(androidx.compose.ui.text.SpanStyle(color = p.ink2))
                                                    append(" · ")
                                                    append(win)
                                                    pop()
                                                },
                                                style = T.s13,
                                                color = p.ink,
                                            )
                                        } else {
                                            Text(
                                                rem.ifEmpty { fallback },
                                                style = T.s13,
                                                color = p.ink2,
                                            )
                                        }
                                    },
                                    onClick = onOpenQuota,
                                    chevron = true,
                                )
                            }
                        },
                )
            }
            if (vm.member && vm.hasConnection()) {
                DirectConnectionGroup(vm.connection, onToggle = onToggleDirect)
            }
            if (vm.cliVersionsList.isNotEmpty()) {
                Group(
                    rows =
                        vm.cliVersionsList.map { a ->
                            @Composable {
                                val unverifiedWord = uiText(a.unverifiedLabel).ifEmpty {
                                    copy(UiMessages.settingsAndroidUnverified())
                                }
                                GRow(
                                    copy(UiMessages.settingsCliVersion(a.name)),
                                    trailing = {
                                        Text(
                                            androidx.compose.ui.text.buildAnnotatedString {
                                                if (!a.verified) {
                                                    pushStyle(
                                                        androidx.compose.ui.text.SpanStyle(
                                                            color = p.orangeInk,
                                                        ),
                                                    )
                                                    append(unverifiedWord)
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
                if (vm.hasUnverifiedCli)
                    Hint(
                        copy(UiMessages.settingsAndroidForAnUnverifiedVersionRunRemoteAgentDoctorOnThe()),
                        Modifier.padding(bottom = 12.dp),
                    )
            }
            val readError = uiText(vm.settingsError)
            val editableControls = vm.online && vm.editable && !vm.savePending
            when {
                !vm.online ->
                    Hint(
                        copy(UiMessages.settingsAndroidValue1IsOfflineConnectToReadAndEditNewSession(title)),
                        Modifier.padding(bottom = 12.dp),
                    )
                readError.isNotEmpty() ->
                    Notice(
                        readError,
                        Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp),
                        NoticeTone.ERROR,
                        action = uiText(UiMessages.commonRetry()),
                        onAction = onRetrySettings,
                    )
                vm.settingsLoading ->
                    Hint(copy(UiMessages.projectsReadingDirectory()), Modifier.padding(bottom = 12.dp))
                vm.hasAgentDefaults() -> {
                    val agents = vm.availableAgentsList
                    val d = vm.agentDefaults
                    val currentAgent = if (vm.selectedAgent in agents) vm.selectedAgent else agents.firstOrNull() ?: d.agent
                    GLabel(
                        if (agents.size > 1) copy(UiMessages.settingsNewProjectDefaults())
                        else copy(UiMessages.settingsNewProjectDefaultsSettings(d.agentName))
                    )
                    if (agents.size > 1) {
                        Segmented(
                            agents.map { agentName(it, copy = copy) },
                            agents.indexOf(currentAgent).coerceAtLeast(0),
                            { onSelectAgent(agents[it]) },
                            Modifier.padding(start = 16.dp, end = 16.dp, bottom = 10.dp).fillMaxWidth(),
                        )
                    }
                    val modelLabel = uiText(d.selectedModelLabel).ifEmpty {
                        d.selectedModel.ifEmpty { copy(UiMessages.modelsDefault()) }
                    }
                    val selectedEffort = d.effortChoicesList.firstOrNull { it.selected }
                    val effortLabel = selectedEffort?.let { uiText(it.label) }?.ifEmpty { null }
                        ?: d.selectedEffort.ifEmpty { copy(UiMessages.modelsDefault()) }
                    val selectedMode = d.modeChoicesList.firstOrNull { it.selected }
                    val modeLabel = selectedMode?.let { uiText(it.label) }.orEmpty()
                    Group(
                        rows = listOf(
                            @Composable {
                                GRow(
                                    copy(UiMessages.modelsTitle()),
                                    modelLabel,
                                    onClick = if (editableControls) ({ pickModel = true }) else null,
                                    chevron = true,
                                )
                            },
                            @Composable {
                                GRow(
                                    copy(UiMessages.modelsEffort()),
                                    effortLabel,
                                    onClick = if (editableControls) {
                                        {
                                            if (d.effortChoicesList.size > 1) pickEffort = true
                                            else pickModel = true
                                        }
                                    } else null,
                                    chevron = true,
                                )
                            },
                            @Composable {
                                GRow(
                                    copy(UiMessages.modesTitle()),
                                    modeLabel,
                                    onClick = if (editableControls) ({ pickMode = true }) else null,
                                    valueColor = if (d.selectedMode == ApprovalMode.APPROVAL_MODE_FULL_ACCESS) p.red else null,
                                    chevron = true,
                                )
                            },
                        ),
                    )
                    val selectedTimeout = vm.approvalTimeoutChoicesList.firstOrNull { it.selected }
                    val timeoutLabel = selectedTimeout?.let { uiText(it.label) }
                        ?: copy(UiMessages.quotaMin(vm.selectedApprovalTimeoutSeconds / 60))
                    Group(
                        rows = listOf(
                            @Composable {
                                GRow(
                                    copy(UiMessages.settingsApprovalTimeout()),
                                    timeoutLabel,
                                    onClick = if (editableControls) ({ pickTimeout = true }) else null,
                                    chevron = true,
                                )
                            },
                        ),
                    )
                    val selectedIdle = vm.idleCloseChoicesList.firstOrNull { it.selected }
                    val idleLabel = selectedIdle?.let { uiText(it.label) }.orEmpty()
                    Group(
                        rows = listOf(
                            @Composable {
                                GRow(
                                    copy(UiMessages.settingsAutomaticallyEndIdleSessions()),
                                    idleLabel,
                                    onClick = if (editableControls) ({ pickIdleClose = true }) else null,
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
            val saveErr = uiText(vm.saveError)
            if (saveErr.isNotEmpty())
                Notice(
                    saveErr,
                    Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp),
                    NoticeTone.ERROR,
                )
            if (vm.hasAttachmentStorage() && vm.attachmentStorage.loaded) {
                Box(Modifier.bringIntoViewRequester(storageAt)) {
                    Column {
                        MachineAttachmentStorage(
                            storage = vm.attachmentStorage,
                            enabled = editableControls && !vm.attachmentStorage.cleaning,
                            onQuota = { bytes -> onUpdateDaemonSettings(null, null, bytes, false) },
                            onClean = { onUpdateDaemonSettings(null, null, null, true) },
                        )
                    }
                }
                // 电脑状态 › 磁盘 and 「remote-agent 的占用」 lead here (ui-spec A3).
                LaunchedEffect(scrollToStorage) {
                    if (scrollToStorage) { androidx.compose.runtime.withFrameNanos { }; storageAt.bringIntoView() }
                }
            }
            if (vm.projectsList.isNotEmpty()) {
                GLabel(copy(UiMessages.scheduleProject()))
                Group(
                    rows =
                        vm.projectsList.map { proj ->
                            @Composable {
                                val sub = uiText(proj.lastActiveText).ifEmpty { proj.path }
                                GRow(
                                    proj.name,
                                    sub,
                                    onClick = { onProject(proj.path) },
                                )
                            }
                        },
                )
            }
            val mgmt = vm.management
            if (mgmt.canRejoin) {
                Group(
                    rows =
                        listOf {
                            GRow(
                                copy(UiMessages.settingsAndroidRejoinThisComputerAndResumeSessions()),
                                onClick = onRejoin,
                                enabled = appOnline,
                            )
                        },
                )
            }
            DeleteMachineRecordsAction(vm.machineId, isAuthority)
            val expHint = uiText(mgmt.explanationText)
            if (mgmt.canRemove) {
                Group(
                    rows =
                        listOf {
                            GRow(
                                // It asks first, as every row that ends in an ellipsis does (design-system §8).
                                copy(UiMessages.settingsAndroidRemoveThisMachine()),
                                onClick = { confirm = true },
                                danger = true,
                                enabled = !removeRunning && appOnline,
                            )
                        },
                )
            }
            if (expHint.isNotEmpty()) {
                Hint(expHint, Modifier.padding(bottom = 8.dp))
            }
            if (removeError.isNotEmpty())
                Notice(removeError, Modifier.padding(16.dp), NoticeTone.ERROR)
            Spacer(Modifier.height(24.dp))
        }
    }
    if (confirm && vm.member) {
        val (titleText, text) = removalDialog(title, machine = true, copy = copy)
        Alert(
            titleText,
            text,
            uiText(UiMessages.commonRemove()),
            danger = true,
            onDismiss = { confirm = false },
        ) {
            onRemove()
        }
    }
    val d = vm.agentDefaults
    val currentAgent = if (vm.selectedAgent != Agent.AGENT_UNSPECIFIED) vm.selectedAgent else d.agent
    if (pickModel && vm.hasAgentDefaults() && d.modelChoicesList.isNotEmpty()) {
        val opts = d.modelChoicesList.map { uiText(it.label) to it.desc.ifEmpty { null } }
        ChoiceSheet(
            copy(UiMessages.settingsAndroidValue1DefaultModelForNewSessions(d.agentName)),
            opts,
            d.modelChoicesList.indexOfFirst { it.selected }.coerceAtLeast(0),
            onPick = { i ->
                pickModel = false
                onUpdateAgentDefaults(currentAgent, d.modelChoicesList[i].id, "", null)
            },
            onClose = { pickModel = false },
        )
    }
    if (pickEffort && vm.hasAgentDefaults() && d.effortChoicesList.isNotEmpty()) {
        val opts = d.effortChoicesList.map { uiText(it.label) to null }
        ChoiceSheet(
            copy(UiMessages.modelsEffort()),
            opts,
            d.effortChoicesList.indexOfFirst { it.selected }.coerceAtLeast(0),
            onPick = { i ->
                pickEffort = false
                onUpdateAgentDefaults(currentAgent, d.selectedModel, d.effortChoicesList[i].id, null)
            },
            onClose = { pickEffort = false },
        )
    }
    if (pickMode && vm.hasAgentDefaults() && d.modeChoicesList.isNotEmpty()) {
        val autoNotice = uiText(d.autoUnavailableNotice)
        val opts = d.modeChoicesList.map { c ->
            val reason = uiText(c.disabledReason).ifEmpty {
                if (c.disabled) autoNotice else ""
            }
            uiText(c.label) to reason.ifEmpty { null }
        }
        ChoiceSheet(
            copy(UiMessages.settingsAndroidValue1ApprovalMode(d.agentName)),
            opts,
            d.modeChoicesList.indexOfFirst { it.selected }.coerceAtLeast(0),
            enabled = { i -> !d.modeChoicesList[i].disabled },
            onPick = { i ->
                pickMode = false
                onUpdateAgentDefaults(currentAgent, null, null, d.modeChoicesList[i].mode)
            },
            onClose = { pickMode = false },
        )
    }
    if (pickTimeout && vm.approvalTimeoutChoicesList.isNotEmpty()) {
        ChoiceSheet(
            copy(UiMessages.settingsApprovalTimeout()),
            vm.approvalTimeoutChoicesList.map { uiText(it.label) to null },
            vm.approvalTimeoutChoicesList.indexOfFirst { it.selected }.coerceAtLeast(0),
            onPick = { i ->
                pickTimeout = false
                onUpdateDaemonSettings(vm.approvalTimeoutChoicesList[i].seconds, null, null, false)
            },
            onClose = { pickTimeout = false },
        )
    }
    if (pickIdleClose && vm.idleCloseChoicesList.isNotEmpty()) {
        ChoiceSheet(
            copy(UiMessages.settingsAutomaticallyEndIdleSessions()),
            vm.idleCloseChoicesList.map { uiText(it.label) to null },
            vm.idleCloseChoicesList.indexOfFirst { it.selected }.coerceAtLeast(0),
            onPick = { i ->
                pickIdleClose = false
                onUpdateDaemonSettings(null, vm.idleCloseChoicesList[i].minutes, null, false)
            },
            onClose = { pickIdleClose = false },
        )
    }
}

/** One usage window rendered from Core's [QuotaRowView]. */
@Composable
private fun QuotaRowItem(r: io.github.lewin671.remoteagent.core.proto.QuotaRowView) {
    val p = pal
    val label = uiText(r.label)
    val left = uiText(r.remainingText)
    val reset = uiText(r.resetText)
    val clock = uiText(r.resetClockText)
    val footnote = uiText(r.footnote)
    val pool = uiText(r.poolName)
    Column(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 10.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Text(label, style = T.s15, color = p.ink)
                if (pool.isNotEmpty() && pool != label) {
                    Text(pool, style = T.s12, color = p.ink2)
                }
            }
            Text(
                left,
                style = T.s13,
                color = when (r.tone) {
                    io.github.lewin671.remoteagent.core.proto.QuotaTone.QUOTA_TONE_FULL -> p.red
                    io.github.lewin671.remoteagent.core.proto.QuotaTone.QUOTA_TONE_WARN -> p.orangeInk
                    io.github.lewin671.remoteagent.core.proto.QuotaTone.QUOTA_TONE_STALE -> p.faint
                    else -> p.ink2
                },
            )
        }
        if (r.hasPercent) {
            val fill = (r.remainingPercent / 100f).coerceIn(0f, 1f)
            Box(
                Modifier.padding(top = 6.dp)
                    .fillMaxWidth()
                    .height(6.dp)
                    .clip(RoundedCornerShape(3.dp))
                    .background(p.fill2),
            ) {
                Box(
                    Modifier.fillMaxWidth(fill)
                        .height(6.dp)
                        .clip(RoundedCornerShape(3.dp))
                        .background(
                            when (r.tone) {
                                io.github.lewin671.remoteagent.core.proto.QuotaTone.QUOTA_TONE_STALE -> p.faint
                                io.github.lewin671.remoteagent.core.proto.QuotaTone.QUOTA_TONE_NORMAL -> p.ink
                                io.github.lewin671.remoteagent.core.proto.QuotaTone.QUOTA_TONE_FULL -> p.red
                                else -> p.orange
                            },
                        ),
                )
            }
        }
        val sub = listOf(reset, clock, footnote).filter { it.isNotEmpty() }.joinToString(" · ")
        if (sub.isNotEmpty()) {
            Text(sub, style = T.s12, color = p.ink2, modifier = Modifier.padding(top = 8.dp))
        }
    }
}

/**
 * 账户额度: renders Core's [QuotaViewModel] directly for the requested computer.
 */
@Composable
fun QuotaScreen(id: String) {
    val remote = LocalRemote.current
    val view by remote.quotaView.collectAsState()
    LaunchedEffect(id) { remote.coreHost.openQuota(id) }
    QuotaScreenContent(view)
}

@Composable
internal fun QuotaScreenContent(view: io.github.lewin671.remoteagent.core.proto.QuotaViewModel) {
    val copy = uiText()
    val p = pal
    val subtitle = if (view.found) {
        val conn = if (view.reachable) copy(UiMessages.settingsAndroidOnlineB90866()) else copy(UiMessages.settingsOffline())
        if (view.machineName.isNotEmpty()) "${view.machineName} · $conn" else conn
    } else null
    PageScaffold(
        copy(UiMessages.settingsAccountQuota()),
        subtitle = subtitle,
        readable = true,
    ) {
        if (!view.found) {
            PageNote(copy(UiMessages.settingsAndroidThisMachineHasBeenRemoved()))
            return@PageScaffold
        }
        Column(Modifier.verticalScrollFaded()) {
            val offlineNotice = uiText(view.offlineNotice)
            if (offlineNotice.isNotEmpty()) {
                Text(
                    offlineNotice,
                    style = T.s13,
                    color = p.orangeInk,
                    modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 6.dp),
                )
            }
            if (view.hasBanner()) {
                val bannerText = uiText(view.banner.message)
                if (bannerText.isNotEmpty()) {
                    val color = if (view.banner.severity == io.github.lewin671.remoteagent.core.proto.QuotaBannerSeverity.QUOTA_BANNER_SEVERITY_FULL) p.red else p.orangeInk
                    Text(
                        bannerText,
                        style = T.s13,
                        color = color,
                        modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 10.dp),
                    )
                }
            }
            val emptyTop = uiText(view.emptyText)
            if (view.sectionsList.isEmpty() && emptyTop.isNotEmpty()) {
                PageNote(emptyTop)
            }
            for (sec in view.sectionsList) {
                val emptyOrError = uiText(sec.emptyOrErrorText)
                val hint = uiText(sec.usageHint)
                if (sec.accountsList.isEmpty()) {
                    GLabel(sec.agentName)
                    if (sec.state == io.github.lewin671.remoteagent.core.proto.QuotaSectionState.QUOTA_SECTION_STATE_FAILED && emptyOrError.isNotEmpty()) {
                        Text(
                            emptyOrError,
                            style = T.s13,
                            color = p.red,
                            modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 6.dp),
                        )
                    } else if (emptyOrError.isNotEmpty() || hint.isNotEmpty()) {
                        Group(
                            rows = listOf {
                                Column(Modifier.padding(horizontal = 14.dp, vertical = 10.dp)) {
                                    if (emptyOrError.isNotEmpty()) {
                                        Text(emptyOrError, style = T.s15b, color = p.ink)
                                    }
                                    if (hint.isNotEmpty()) {
                                        Text(hint, style = T.s12, color = p.ink2)
                                    }
                                }
                            },
                        )
                    }
                    continue
                }
                for (acct in sec.accountsList) {
                    val acctErr = uiText(acct.errorText)
                    if (acctErr.isNotEmpty()) {
                        Text(
                            acctErr,
                            style = T.s13,
                            color = p.red,
                            modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 6.dp),
                        )
                    }
                    val headerBase = listOf(sec.agentName, acct.account, acct.plan).filter { it.isNotEmpty() }.joinToString(" · ")
                    if (acct.groupsList.isEmpty()) {
                        GLabel(headerBase + if (acct.lastRecord) copy(UiMessages.quotaLastReading()) else "")
                    } else {
                        for (wg in acct.groupsList) {
                            val wgTitle = uiText(wg.groupLabel).ifEmpty { headerBase + if (acct.lastRecord) copy(UiMessages.quotaLastReading()) else "" }
                            GLabel(wgTitle)
                            if (wg.rowsList.isNotEmpty()) {
                                Group(rows = wg.rowsList.map { r -> @Composable { QuotaRowItem(r) } })
                            }
                        }
                    }
                    val statusLine = uiText(acct.statusText)
                    val freshnessLine = uiText(acct.freshnessText)
                    if (freshnessLine.isNotEmpty()) {
                        Text(freshnessLine, style = T.s12, color = p.ink2, modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 6.dp))
                    }
                    if (statusLine.isNotEmpty()) {
                        Text(statusLine, style = T.s12, color = p.ink2, modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 10.dp))
                    }
                }
            }
            val footer = uiText(view.footerNote).ifEmpty { copy(UiMessages.settingsTimesAreShownInBeijingTimeQuotasBelongTo()) }
            Text(
                footer,
                style = T.s13,
                color = p.ink2,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp),
            )
        }
    }
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

@Composable
internal fun MachineAttachmentStorage(
    storage: AttachmentStorageView,
    enabled: Boolean,
    onQuota: (Long) -> Unit,
    onClean: () -> Unit,
) {
    val copy = uiText()
    var clean by remember { mutableStateOf(false) }
    var limit by remember { mutableStateOf(false) }
    val uploaded = uiText(storage.uploadedText).ifEmpty { copy(UiMessages.projectsReadingDirectory()) }
    val detail = uiText(storage.uploadedDetail)
    val uploadedValue = listOf(uploaded, detail).filter { it.isNotEmpty() }.joinToString(" · ")
    val quotaLabel =
        storage.quotaChoicesList.firstOrNull { it.selected }?.let { uiText(it.label) }.orEmpty()
    val previewCache = uiText(storage.previewCacheText).ifEmpty { "—" }
    val emergencyText = uiText(storage.emergencyReserveText)
    val clearLabel = uiText(storage.clearButtonLabel)
    val footerText = uiText(storage.footerText).ifEmpty {
        copy(UiMessages.settingsAttachmentsAreStoredOnThisComputerSessionFilesRemain())
    }
    GLabel(copy(UiMessages.settingsAttachmentStorage()))
    Group(
        rows =
            listOf(
                { GRow(copy(UiMessages.settingsUploadedImagesAndFiles()), uploadedValue) },
                {
                    GRow(
                        copy(UiMessages.settingsStorageLimit()),
                        quotaLabel,
                        onClick = if (enabled && storage.quotaChoicesList.isNotEmpty()) ({ limit = true }) else null,
                        chevron = true,
                    )
                },
                { GRow(copy(UiMessages.settingsPreviewCache()), previewCache) },
                *(if (storage.showEmergencyReserve && emergencyText.isNotEmpty()) arrayOf<@Composable () -> Unit>({
                    // 应急预留 (ui-spec B2): not clearable, so no action; the line under it says why it is there.
                    Column(Modifier.fillMaxWidth().padding(start = 18.dp, end = 16.dp, top = 11.dp, bottom = 11.dp)) {
                        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                            Text(copy(UiMessages.machineStatusEmergencyReserve()), style = T.s17, color = pal.ink, modifier = Modifier.weight(1f))
                            Text(emergencyText, style = T.s17, color = pal.ink2)
                        }
                        Text(copy(UiMessages.machineStatusEmergencyReserveNote()), style = T.s13, color = pal.ink2, modifier = Modifier.padding(top = 2.dp))
                    }
                }) else emptyArray()),
                {
                    GRow(
                        copy(UiMessages.settingsClear()),
                        clearLabel,
                        onClick = if (enabled && storage.canClear && !storage.cleaning) ({ clean = true }) else null,
                        action = true,
                        enabled = enabled && storage.canClear && !storage.cleaning,
                    )
                },
            ),
    )
    Text(
        footerText,
        style = T.s13,
        color = pal.ink2,
        modifier = Modifier.padding(start = 30.dp, end = 30.dp, bottom = 12.dp),
    )
    if (limit && storage.quotaChoicesList.isNotEmpty()) {
        ChoiceSheet(
            copy(UiMessages.settingsStorageLimit()),
            storage.quotaChoicesList.map { uiText(it.label) to null },
            storage.quotaChoicesList.indexOfFirst { it.selected }.coerceAtLeast(0),
            onPick = { i ->
                limit = false
                onQuota(storage.quotaChoicesList[i].bytes)
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
            onClean()
        }
}

/**
 * 附件存储 · 这台手机 (Android): drafts and the viewing cache, limited apart; clearing the cache keeps
 * drafts.
 */
/** Fonts bundled in the app, each with its SIL OFL 1.1 text from res/raw. */
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

/** Core native libraries bundled in libgojni.so, each with its BSD-3-Clause text from res/raw. */
private fun bundledCoreLibraries(copy: io.github.lewin671.remoteagent.core.MessageResolver) =
    listOf(
        Triple(
            "The Go Programming Language",
            copy(UiMessages.settingsAndroidGoRuntimeAndStandardLibrary()),
            io.github.lewin671.remoteagent.R.raw.bsd3_go,
        ),
        Triple(
            "Protocol Buffers for Go (google.golang.org/protobuf)",
            copy(UiMessages.settingsAndroidCoreProtobufEncoding()),
            io.github.lewin671.remoteagent.R.raw.bsd3_protobuf_go,
        ),
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
                        copy(UiMessages.settingsAndroidCoreLibrariesBsd3Clause()) to bundledCoreLibraries(copy),
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
