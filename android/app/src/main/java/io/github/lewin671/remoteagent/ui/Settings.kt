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
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Icon
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
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.toggleableState
import androidx.compose.ui.state.ToggleableState
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.google.zxing.BarcodeFormat
import com.google.zxing.EncodeHintType
import com.google.zxing.qrcode.QRCodeWriter
import com.google.zxing.qrcode.decoder.ErrorCorrectionLevel
import io.github.lewin671.remoteagent.RaApp
import androidx.compose.foundation.relocation.bringIntoViewRequester
import io.github.lewin671.remoteagent.core.PairingUiState
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.proto.AttachmentStorageView
import io.github.lewin671.remoteagent.core.proto.CliVersionView
import io.github.lewin671.remoteagent.core.proto.ConnProbeState
import io.github.lewin671.remoteagent.core.proto.MachineAgentDefaultsView
import io.github.lewin671.remoteagent.core.proto.MachineListCardView
import io.github.lewin671.remoteagent.core.proto.MachinePollMode
import io.github.lewin671.remoteagent.core.proto.DirectDiagnosticsViewModel
import io.github.lewin671.remoteagent.core.proto.MachineSettingsViewModel
import io.github.lewin671.remoteagent.core.proto.MachineStatusDot
import io.github.lewin671.remoteagent.core.proto.NotificationsView
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
            .toggleable(value = value, enabled = enabled, role = Role.Switch, onValueChange = onChange)
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

internal fun clientIconForDescription(desc: String): ImageVector = when {
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
    val copy = uiText()
    val syncView by remote.coreHost.syncStateView.collectAsState()
    val initialVm = remember(remote) { remote.coreHost.snapshotSettingsView() }
    val rawVm by remote.settingsView.collectAsState()
    val vm = if (rawVm != SettingsViewModel.getDefaultInstance()) rawVm else initialVm
    val push by Push.status.collectAsState()
    var checking by remember { mutableStateOf(false) }
    var updating by remember { mutableStateOf(false) }
    var localCacheBytes by remember { mutableStateOf<Long?>(null) }
    val scope = rememberCoroutineScope()
    val owner = androidx.lifecycle.compose.LocalLifecycleOwner.current
    LaunchedEffect(Unit) {
        remote.coreHost.openSettings()
        localCacheBytes = withContext(Dispatchers.IO) {
            app.attachments.usedBytes() + app.imageEnvelopes.used + app.sharedFiles.usedBytes()
        }
    }
    DisposableEffect(owner) {
        val obs = androidx.lifecycle.LifecycleEventObserver { _, ev ->
            if (ev == androidx.lifecycle.Lifecycle.Event.ON_RESUME) {
                scope.launch {
                    localCacheBytes = withContext(Dispatchers.IO) {
                        app.attachments.usedBytes() + app.imageEnvelopes.used + app.sharedFiles.usedBytes()
                    }
                }
            }
        }
        owner.lifecycle.addObserver(obs)
        onDispose { owner.lifecycle.removeObserver(obs) }
    }
    val version =
        app.versionName.let { v ->
            val c = app.buildCommit.take(7)
            if (c.isEmpty() || c in v) v else "$v · $c"
        }
    SettingsScreenContent(
        vm = vm,
        syncView = syncView,
        push = push,
        serverDisplay = remote.account.server.removePrefix("https://").removePrefix("http://"),
        localCacheText = localCacheBytes?.let { imageBytes(it, copy) }.orEmpty(),
        versionFallback = version,
        updaterEnabled = Updater.enabled(app),
        onBack = nav.back,
        onRecovery = { nav.recovery() },
        onDevices = { nav.devices() },
        onDevice = { nav.device(it) },
        onNotifications = { nav.notifications() },
        onPair = { nav.pair() },
        onPhoneStorage = nav.phoneStorage,
        onTheme = { remote.coreHost.setAppearancePref(it) },
        onOpenUpdate = { updating = true },
        onLicenses = nav.licenses,
        onOpenConnCheck = { checking = true },
        readExitAdvice = remote::checkExitAdvice,
        onExitAccount = { app.logout() },
    )
    if (checking) ConnCheckSheet { checking = false }
    if (updating) UpdateSheet { updating = false }
}

private fun connDotFor(vm: SettingsViewModel): MachineStatusDot = when {
    !vm.online || vm.connCheck.serverState == ConnProbeState.CONN_PROBE_STATE_UNAVAILABLE ->
        MachineStatusDot.MACHINE_STATUS_DOT_RED_STATIC
    vm.connCheck.serverState == ConnProbeState.CONN_PROBE_STATE_SLOW ||
        vm.connCheck.serverState == ConnProbeState.CONN_PROBE_STATE_CHECKING ->
        MachineStatusDot.MACHINE_STATUS_DOT_ORANGE_STATIC
    else -> MachineStatusDot.MACHINE_STATUS_DOT_GREEN_LIVE
}

@Composable
internal fun SettingsScreenContent(
    vm: SettingsViewModel,
    syncView: io.github.lewin671.remoteagent.core.proto.SyncStateView = io.github.lewin671.remoteagent.core.proto.SyncStateView.getDefaultInstance(),
    push: PushStatus = PushStatus(),
    serverDisplay: String = "",
    localCacheText: String = "",
    versionFallback: String = "",
    updaterEnabled: Boolean = false,
    onBack: () -> Unit = {},
    onRecovery: () -> Unit = {},
    onDevices: () -> Unit = {},
    onDevice: (String) -> Unit = {},
    onNotifications: () -> Unit = {},
    onPair: () -> Unit = {},
    onPhoneStorage: () -> Unit = {},
    onTheme: (ThemePreference) -> Unit = {},
    onOpenUpdate: () -> Unit = {},
    onLicenses: () -> Unit = {},
    onOpenConnCheck: () -> Unit = {},
    readExitAdvice: suspend () -> io.github.lewin671.remoteagent.core.proto.SecurityExitAdvice = {
        io.github.lewin671.remoteagent.core.proto.SecurityExitAdvice.getDefaultInstance()
    },
    onExitAccount: suspend () -> Unit = {},
) {
    val copy = uiText()
    val p = pal
    androidx.compose.runtime.CompositionLocalProvider(LocalOnSoft provides false) {
        Column(
            Modifier.fillMaxSize()
                .background(p.paper)
                .verticalScrollFaded()
                .settingsReadableColumn(),
        ) {
            BackRow(onBack)
            LargeTitle(copy(UiMessages.commonSettings()))
            ConnBanner(syncView)
            if (vm.recoveryPending) {
                val recMsg = uiText(vm.recoveryBannerText).ifEmpty {
                    if (vm.isManager) copy(UiMessages.securityAndroidSomeoneIsRequestingManagementTakeoverWithARecoveryCodeVeto())
                    else copy(UiMessages.settingsRecoveryWaitingBanner())
                }
                if (recMsg.isNotEmpty()) {
                    Notice(
                        recMsg,
                        Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
                        NoticeTone.WARN,
                        action = if (vm.isManager) copy(UiMessages.dockHandle()) else null,
                        onAction = if (vm.isManager) onRecovery else null,
                    )
                }
            }
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
            if (vm.unlinked) {
                val unlinkedMsg = uiText(vm.unlinkedBannerText).ifEmpty {
                    copy(UiMessages.settingsThisDeviceWasRemovedFromTheAccount())
                }
                Notice(
                    unlinkedMsg,
                    Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
                    NoticeTone.WARN,
                )
            }

            // Group 1: 电脑
            MachinesGroup(vm, onDevice = onDevice, onPair = onPair)

            // Group 2: 本机
            SectionLabel(copy(UiMessages.settingsSectionThisDevice()))
            val n = vm.notifications
            val pushUnreachable = push.problem.isNotEmpty()
            val notifSummary = if (pushUnreachable) {
                copy(UiMessages.settingsNotificationsSummaryUnreachable())
            } else {
                uiText(n.summaryText)
            }
            val notifWarn = pushUnreachable || n.summaryWarn
            val appearanceOpts = listOf(
                ThemePreference.THEME_PREFERENCE_AUTO to copy(UiMessages.modesAuto()),
                ThemePreference.THEME_PREFERENCE_LIGHT to copy(UiMessages.settingsLight()),
                ThemePreference.THEME_PREFERENCE_DARK to copy(UiMessages.settingsDark()),
            )
            Group(
                rows = listOf(
                    {
                        GRow(
                            copy(UiMessages.settingsNotificationsTitle()),
                            notifSummary,
                            valueColor = if (notifWarn) p.orangeInk else null,
                            singleLineValue = true,
                            onClick = onNotifications,
                            chevron = true,
                        )
                    },
                    {
                        Row(
                            Modifier.fillMaxWidth()
                                .heightIn(min = 50.dp)
                                .padding(start = 18.dp, end = 16.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Text(
                                copy(UiMessages.settingsAppearance()),
                                style = T.s17,
                                color = p.ink,
                                modifier = Modifier.weight(1f),
                            )
                            SegPick(
                                appearanceOpts.map { it.second },
                                appearanceOpts.indexOfFirst { it.first == vm.appearance.theme }.coerceAtLeast(0),
                            ) { i ->
                                onTheme(appearanceOpts[i].first)
                            }
                        }
                    },
                    {
                        GRow(
                            copy(UiMessages.settingsLocalCache()),
                            localCacheText,
                            singleLineValue = true,
                            onClick = onPhoneStorage,
                            chevron = true,
                        )
                    },
                ),
            )

            // Group 3: 账户与设备
            SectionLabel(copy(UiMessages.settingsSectionAccountAndDevices()))
            val clientsSummary = uiText(vm.clientsSummaryText)
            Group(
                rows = listOf(
                    {
                        GRow(
                            copy(UiMessages.settingsClientsTitle()),
                            clientsSummary,
                            valueColor = if (vm.clientsSummaryWarn) p.orangeInk else null,
                            singleLineValue = true,
                            onClick = onDevices,
                            chevron = true,
                        )
                    },
                    {
                        GRow(
                            copy(UiMessages.settingsAccountSecurityTitle()),
                            uiText(vm.security.summaryText),
                            valueColor = if (vm.security.recoveryPending) p.orangeInk else null,
                            singleLineValue = true,
                            onClick = onRecovery,
                            chevron = true,
                        )
                    },
                    {
                        GRow(
                            copy(UiMessages.settingsServerAndConnection()),
                            onClick = onOpenConnCheck,
                            trailing = {
                                Row(
                                    verticalAlignment = Alignment.CenterVertically,
                                    horizontalArrangement = Arrangement.spacedBy(6.dp),
                                ) {
                                    StatusDotView(connDotFor(vm), size = 7.dp)
                                    if (serverDisplay.isNotEmpty()) {
                                        Text(
                                            serverDisplay,
                                            style = T.s15,
                                            color = p.ink2,
                                            maxLines = 1,
                                            overflow = TextOverflow.Ellipsis,
                                            modifier = Modifier.widthIn(max = 180.dp),
                                        )
                                    }
                                    Arrow(p.ink3, T.s15)
                                }
                            },
                        )
                    },
                ),
            )

            // Group 4: 关于
            SectionLabel(copy(UiMessages.settingsAbout()))
            val u = vm.update
            val version = u.currentVersion.ifEmpty { versionFallback }
            val updateStatus = if (u.checking || u.hasUpdate) uiText(u.statusText) else ""
            Group(
                rows = listOf(
                    {
                        if (!updaterEnabled) {
                            GRow(copy(UiMessages.settingsVersionLabel()), version, singleLineValue = true)
                        } else {
                            GRow(
                                copy(UiMessages.settingsVersionLabel()),
                                updateStatus.ifEmpty { version },
                                onClick = onOpenUpdate,
                                chevron = true,
                                singleLineValue = true,
                                valueColor = if (updateStatus.isNotEmpty()) p.blue else null,
                            )
                        }
                    },
                    { GRow(copy(UiMessages.settingsOpenSourceLicenses()), onClick = onLicenses, chevron = true) },
                ),
            )

            // Separated danger card
            if (vm.canExitAccount) {
                Group(
                    rows = listOf {
                        AccountExitAction(
                            syncView,
                            readAdvice = readExitAdvice,
                            exit = onExitAccount,
                        )
                    },
                )
            }
            Spacer(Modifier.height(24.dp))
        }
    }
}

/** Notifications secondary screen (`只影响这台设备`). */
@Composable
fun NotificationsScreen() {
    val app = RaApp.instance
    val remote = LocalRemote.current
    val initialVm = remember(remote) { remote.coreHost.snapshotSettingsView() }
    val rawVm by remote.settingsView.collectAsState()
    val vm = if (rawVm != SettingsViewModel.getDefaultInstance()) rawVm else initialVm
    val push by Push.status.collectAsState()
    LaunchedEffect(Unit) { remote.coreHost.openSettings() }
    NotificationsScreenContent(
        notifications = vm.notifications,
        push = push,
        onNotifyAttention = { remote.coreHost.reportNotificationPrefs(notifyAttention = it) },
        onNotifyTurnEnded = { remote.coreHost.reportNotificationPrefs(notifyTurnEnded = it) },
        onNotifyScheduled = { remote.coreHost.reportNotificationPrefs(notifyScheduled = it) },
        onShowContent = { remote.coreHost.reportNotificationPrefs(showContent = it) },
        onFixPush = { Push.fix(app, remote) },
    )
}

@Composable
internal fun NotificationsScreenContent(
    notifications: NotificationsView,
    push: PushStatus = PushStatus(),
    onNotifyAttention: (Boolean) -> Unit = {},
    onNotifyTurnEnded: (Boolean) -> Unit = {},
    onNotifyScheduled: (Boolean) -> Unit = {},
    onShowContent: (Boolean) -> Unit = {},
    onFixPush: () -> Unit = {},
) {
    val copy = uiText()
    PageScaffold(
        copy(UiMessages.settingsNotificationsTitle()),
        subtitle = copy(UiMessages.settingsNotificationsSubtitle()),
    ) {
        Column(Modifier.weight(1f).settingsReadableColumn().verticalScrollFaded()) {
            PushNotice(push, onAction = onFixPush)
            SectionLabel(copy(UiMessages.settingsNotificationsGroupAlerts()))
            Group(
                rows = listOf(
                    {
                        ToggleRow(
                            copy(UiMessages.settingsWhenINeedToAct()),
                            copy(UiMessages.settingsNotificationsWhenINeedToActDesc()),
                            notifications.notifyAttention,
                            onChange = onNotifyAttention,
                        )
                    },
                    {
                        ToggleRow(
                            copy(UiMessages.settingsWhenATurnEnds()),
                            copy(UiMessages.settingsNotificationsWhenATurnEndsDesc()),
                            notifications.notifyTurnEnded,
                            onChange = onNotifyTurnEnded,
                        )
                    },
                    {
                        ToggleRow(
                            copy(UiMessages.settingsScheduledTaskResults()),
                            copy(UiMessages.settingsNotificationsScheduledResultsDesc()),
                            notifications.notifyScheduled,
                            onChange = onNotifyScheduled,
                        )
                    },
                ),
            )
            SectionLabel(copy(UiMessages.settingsNotificationsGroupPrivacy()))
            Group(
                rows = listOf {
                    ToggleRow(
                        copy(UiMessages.settingsNotificationsShowContentTitle()),
                        copy(UiMessages.settingsNotificationsShowContentDesc()),
                        notifications.showContent,
                        onChange = onShowContent,
                    )
                },
            )
            Hint(copy(UiMessages.settingsNotificationsSecurityFooter()), Modifier.padding(bottom = 24.dp), inset = 32.dp)
        }
    }
}

/**
 * Settings Group 1 `电脑`: unified rounded card with 20 dp laptop icons (`46 dp` text/divider inset)
 * and optional inline `＋ 添加设备` row when [SettingsViewModel.canAddDevice] is true.
 */
@Composable
internal fun MachinesGroup(
    vm: SettingsViewModel,
    onDevice: (String) -> Unit = {},
    onPair: () -> Unit = {},
) {
    val copy = uiText()
    val p = pal
    SectionLabel(copy(UiMessages.settingsSectionComputers()), trailing = uiText(vm.machineCountText))
    if (vm.machinesList.isEmpty()) {
        val emptyTitle = uiText(vm.emptyMachinesText).ifEmpty { copy(UiMessages.settingsEmptyComputersTitle()) }
        val emptyBody = uiText(vm.emptyMachinesBody)
        Column(
            Modifier.fillMaxWidth()
                .padding(start = 16.dp, end = 16.dp, bottom = 12.dp)
                .clip(Radii.R18)
                .background(p.gbg)
                .padding(18.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                Box(Modifier.size(20.dp), contentAlignment = Alignment.Center) {
                    Ic(Lucide.Laptop, 20.dp, p.ink2)
                }
                Text(emptyTitle, style = T.s17m, color = p.ink)
            }
            if (emptyBody.isNotEmpty()) {
                Text(emptyBody, style = T.s13, color = p.ink2)
            } else if (!vm.canAddDevice) {
                val addHint = uiText(vm.addDeviceHint)
                if (addHint.isNotEmpty()) {
                    Text(addHint, style = T.s13, color = p.ink2)
                }
            }
            if (!vm.machinesLoading && vm.canAddDevice) {
                PillButton(
                    copy(UiMessages.settingsAddFirstComputer()),
                    onPair,
                    Modifier.fillMaxWidth().padding(top = 4.dp),
                    ButtonKind.INK,
                )
            }
        }
        return
    }

    val rows: List<@Composable () -> Unit> = buildList {
        for (m in vm.machinesList) {
            add { MachineCard(m) { onDevice(m.machineId) } }
        }
        if (vm.canAddDevice) {
            add {
                Row(
                    Modifier.fillMaxWidth()
                        .heightIn(min = 50.dp)
                        .clickable(role = Role.Button, onClick = onPair)
                        .padding(horizontal = 16.dp, vertical = 10.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(10.dp),
                ) {
                    Box(Modifier.size(20.dp), contentAlignment = Alignment.Center) {
                        Ic(Lucide.Plus, 18.dp, p.blue)
                    }
                    Text(copy(UiMessages.settingsAddDeviceItem()), style = T.s17, color = p.blue)
                }
            }
        }
    }
    ClientsAlignedCard(rows = rows)
    if (!vm.canAddDevice) {
        val addHint = uiText(vm.addDeviceHint)
        if (addHint.isNotEmpty()) Hint(addHint, Modifier.padding(bottom = 8.dp), inset = 32.dp)
    }
}

@Composable
private fun MachineCard(m: MachineListCardView, onClick: () -> Unit) {
    val p = pal
    val statusStr = uiText(m.statusText)
    Row(
        Modifier.fillMaxWidth()
            .heightIn(min = 50.dp)
            .clickable(onClick = onClick)
            .padding(horizontal = 16.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Box(Modifier.size(20.dp), contentAlignment = Alignment.Center) {
            Ic(Lucide.Laptop, 20.dp, p.ink2)
        }
        Column(Modifier.weight(1f)) {
            Text(m.name, style = T.s17m, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (m.subtitlePartsList.isNotEmpty()) {
                val sub = buildAnnotatedString {
                    m.subtitlePartsList.forEachIndexed { idx, seg ->
                        if (idx > 0) {
                            withStyle(SpanStyle(color = p.ink2)) { append(" · ") }
                        }
                        if (seg.warn) {
                            withStyle(SpanStyle(color = p.orangeInk, fontWeight = FontWeight.SemiBold)) {
                                append(uiText(seg.text))
                            }
                        } else {
                            withStyle(SpanStyle(color = p.ink2)) { append(uiText(seg.text)) }
                        }
                    }
                }
                Text(
                    sub,
                    style = T.s13,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(top = 2.dp),
                )
            }
        }
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            StatusDotView(m.statusDot, size = 7.dp)
            if (statusStr.isNotEmpty()) {
                Text(
                    statusStr,
                    style = T.s15,
                    color = if (m.statusWarn) p.orangeInk else p.ink2,
                    maxLines = 1,
                )
            }
            Arrow(p.ink3, T.s15)
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
 * One setting's few values at the right of its row (外观): a grey capsule with the chosen one raised.
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

/** A choice from a few options in an adaptive sheet/dialog: a mode, a timeout, a limit. */
@Composable
private fun ChoiceSheet(
    title: String,
    options: List<Pair<String, String?>>,
    selected: Int,
    enabled: (Int) -> Boolean = { true },
    danger: (Int) -> Boolean = { false },
    footer: String? = null,
    onPick: (Int) -> Unit,
    onClose: () -> Unit,
) {
    AdaptiveSheet(onClose) {
        SheetHead(title, if (wideWindow()) uiText(UiMessages.commonCancel()) else null, onClose)
        SheetList { options.forEachIndexed { i, (label, desc) ->
            val ok = enabled(i)
            ListItem(
                label,
                desc,
                checked = i == selected,
                enabled = ok,
                danger = danger(i),
                last = i == options.size - 1,
                onClick = if (ok) ({ onPick(i) }) else null,
            )
        } }
        if (!footer.isNullOrEmpty()) {
            Hint(footer)
        }
    }
}

/**
 * 电脑详情: 4 core modules (电脑状态, 额度与用量, 编程 CLI, 运行与存储) + 管理.
 * Renders Core's [MachineSettingsViewModel] directly.
 */
@Composable
fun DeviceScreen(
    id: String,
    openDirect: Boolean = false,
    initialAgent: Agent? = null,
) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val scope = rememberCoroutineScope()
    val initialVm = remember(remote, id) { remote.coreHost.snapshotMachineSettingsView(id) }
    val rawVm by remote.machineSettingsView.collectAsState()
    val vm = if (rawVm.machineId == id) rawVm else initialVm
    val diagVm by remote.coreHost.directDiagnosticsView.collectAsState()
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
        directDiagnostics = diagVm,
        openDirect = openDirect,
        initialAgent = initialAgent,
        isAuthority = vm.isManager,
        appOnline = vm.appOnline,
        removeRunning = remove.running,
        removeError = remove.error,
        onOpenStatus = { nav.machineStatus(id) },
        onOpenQuota = { nav.quota(id) },
        onOpenUsage = { nav.openUsage(id, Agent.AGENT_UNSPECIFIED, "") },
        onToggleDirect = { enabled -> remote.coreHost.updateDirectPreference(id, enabled) },
        onRetryDirect = { scope.launch { remote.retryDirect(id) } },
        onRefreshDirectDiag = { remote.coreHost.refreshDirectDiagnostics(id) },
        onOpenDirectDiag = { remote.coreHost.openDirectDiagnostics(id) },
        onCloseDirectDiag = { remote.coreHost.closeDirectDiagnostics(id) },
        onOpenStorage = { nav.deviceStorage(id) },
        onOpenProjects = { nav.manageMachineProjects(id) },
        onRetrySettings = { remote.coreHost.openMachineSettings(id) },
        onSelectAgent = { agent -> remote.coreHost.selectMachineSettingsAgent(id, agent) },
        onToggleAgent = { agent, enabled -> remote.coreHost.toggleMachineAgent(id, agent, enabled) },
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
        onRejoin = { nav.rejoin(id) },
        onRemove = {
            remove.run({ remote.removeDevice(id) })
        },
    )
}

@Composable
internal fun DeviceScreenContent(
    vm: MachineSettingsViewModel,
    directDiagnostics: DirectDiagnosticsViewModel = DirectDiagnosticsViewModel.getDefaultInstance(),
    openDirect: Boolean = false,
    initialAgent: Agent? = null,
    isAuthority: Boolean = false,
    appOnline: Boolean = true,
    removeRunning: Boolean = false,
    removeError: String = "",
    onOpenStatus: () -> Unit = {},
    onOpenQuota: () -> Unit = {},
    onOpenUsage: () -> Unit = {},
    onToggleDirect: (Boolean) -> Unit = {},
    onRetryDirect: () -> Unit = {},
    onRefreshDirectDiag: () -> Unit = {},
    onOpenDirectDiag: () -> Unit = {},
    onCloseDirectDiag: () -> Unit = {},
    onOpenStorage: () -> Unit = {},
    onOpenProjects: (String) -> Unit = {},
    onRetrySettings: () -> Unit = {},
    onSelectAgent: (Agent) -> Unit = {},
    onUpdateAgentDefaults: (agent: Agent, model: String?, effort: String?, mode: ApprovalMode?) -> Unit = { _, _, _, _ -> },
    onUpdateDaemonSettings: (approvalTimeoutSeconds: Int?, idleCloseMinutes: Int?, imageUploadQuotaBytes: Long?, cleanImages: Boolean) -> Unit = { _, _, _, _ -> },
    onRejoin: () -> Unit = {},
    onRemove: () -> Unit = {},
    onDeleteRecords: (() -> Unit)? = null,
    onToggleAgent: ((Agent, Boolean) -> Unit)? = null,
) {
    val copy = uiText()
    val p = pal
    var confirmRemove by remember(vm.machineId) { mutableStateOf(false) }
    var showDirectSheet by remember(vm.machineId, openDirect) { mutableStateOf(openDirect) }
    var openCliAgent by remember(vm.machineId, initialAgent) {
        mutableStateOf(
            initialAgent?.takeIf {
                it != Agent.AGENT_UNSPECIFIED && it != Agent.UNRECOGNIZED
            },
        )
    }
    var pickModelAgent by remember(vm.machineId) { mutableStateOf<Agent?>(null) }
    var pickEffortAgent by remember(vm.machineId) { mutableStateOf<Agent?>(null) }
    var pickModeAgent by remember(vm.machineId) { mutableStateOf<Agent?>(null) }
    var pickTimeout by remember(vm.machineId) { mutableStateOf(false) }
    var pickIdleClose by remember(vm.machineId) { mutableStateOf(false) }

    val title = vm.name.ifEmpty { vm.machineId }
    val headerSub = uiText(vm.headerSubtitle).ifEmpty { null }
    val headerDot = vm.headerDot.takeIf { it != MachineStatusDot.MACHINE_STATUS_DOT_UNSPECIFIED }
    PageScaffold(title, subtitle = headerSub, subtitleDot = headerDot) {
        if (!vm.found) {
            val missingMsg = uiText(vm.offlineOrMismatchBanner).ifEmpty {
                if (vm.loading) copy(UiMessages.commonLoading())
                else copy(UiMessages.settingsAndroidThisComputerIsOutsideTheCurrentTrustedDirectoryCheckThe())
            }
            PageNote(missingMsg)
            return@PageScaffold
        }
        Column(Modifier.weight(1f).settingsReadableColumn().verticalScrollFaded()) {
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

            // Module 0: 电脑状态
            if (vm.showStatusEntry) {
                MachineRingsCard(
                    vm.statusRingsList,
                    vm.member && vm.online && vm.protocolMismatch == 0,
                    onOpen = onOpenStatus,
                    dot = vm.statusCardDot,
                    badge = uiText(vm.statusCardBadge),
                    warn = vm.statusCardWarn,
                )
            }

            // Module 1: 额度与用量
            if (vm.quotaRowsList.isNotEmpty() || vm.showUsageEntry) {
                SectionLabel(copy(UiMessages.settingsSectionQuotaAndUsage()))
                val rows: List<@Composable () -> Unit> = buildList {
                    for (sec in vm.quotaRowsList) {
                        add {
                            val primary = uiText(sec.summaryPrimaryText).ifEmpty {
                                sec.takeIf { it.hasSummaryRow() }?.let { uiText(it.summaryRow.remainingText) }.orEmpty()
                            }
                            val window = uiText(sec.summaryWindowText).ifEmpty {
                                sec.takeIf { it.hasSummaryRow() }?.let { uiText(it.summaryRow.label) }.orEmpty()
                            }
                            val fallback = uiText(sec.emptyOrErrorText)
                            val trailingText = when {
                                primary.isNotEmpty() -> buildAnnotatedString {
                                    withStyle(
                                        SpanStyle(
                                            color = if (sec.summaryPrimaryWarn) p.orangeInk else p.ink2,
                                            fontWeight = if (sec.summaryPrimaryWarn) FontWeight.SemiBold else FontWeight.Normal,
                                        ),
                                    ) {
                                        append(primary)
                                    }
                                    if (window.isNotEmpty()) {
                                        withStyle(SpanStyle(color = p.ink2)) {
                                            append(" · ")
                                            append(window)
                                        }
                                    }
                                }
                                fallback.isNotEmpty() -> buildAnnotatedString {
                                    withStyle(SpanStyle(color = p.ink2)) { append(fallback) }
                                }
                                else -> AnnotatedString("")
                            }
                            Row(
                                Modifier.fillMaxWidth()
                                    .heightIn(min = 50.dp)
                                    .clickable(onClick = onOpenQuota)
                                    .padding(horizontal = 16.dp, vertical = 10.dp),
                                verticalAlignment = Alignment.CenterVertically,
                                horizontalArrangement = Arrangement.spacedBy(10.dp),
                            ) {
                                Box(Modifier.size(20.dp), contentAlignment = Alignment.Center) {
                                    AgentMarks.of(sec.agent)?.let { Icon(it, null, Modifier.size(20.dp), p.ink2) }
                                }
                                Text(
                                    sec.agentName,
                                    style = T.s17,
                                    color = p.ink,
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis,
                                    modifier = Modifier.weight(1f),
                                )
                                Row(
                                    verticalAlignment = Alignment.CenterVertically,
                                    horizontalArrangement = Arrangement.spacedBy(4.dp),
                                ) {
                                    if (trailingText.isNotEmpty()) {
                                        Text(
                                            trailingText,
                                            style = T.s15.digits(),
                                            maxLines = 1,
                                            overflow = TextOverflow.Ellipsis,
                                            modifier = Modifier.widthIn(max = 200.dp),
                                        )
                                    }
                                    Arrow(p.ink3, T.s15)
                                }
                            }
                        }
                    }
                    if (vm.showUsageEntry) {
                        add {
                            val usageEmpty = uiText(vm.usageEmptyText)
                            val usageTok = uiText(vm.usageTokensText)
                            val usageSummary = when {
                                usageEmpty.isNotEmpty() -> usageEmpty
                                usageTok.isNotEmpty() -> when {
                                    vm.usageCachedOffline -> copy(UiMessages.settingsUsageSummaryCached(usageTok))
                                    vm.usageFallbackThirtyDays -> copy(UiMessages.settingsUsageSummary30D(usageTok))
                                    else -> copy(UiMessages.settingsUsageSummary7D(usageTok))
                                }
                                else -> copy(UiMessages.settingsUsageSummaryEmpty())
                            }
                            Row(
                                Modifier.fillMaxWidth()
                                    .heightIn(min = 50.dp)
                                    .clickable(onClick = onOpenUsage)
                                    .padding(horizontal = 16.dp, vertical = 10.dp),
                                verticalAlignment = Alignment.CenterVertically,
                                horizontalArrangement = Arrangement.spacedBy(10.dp),
                            ) {
                                Box(Modifier.size(20.dp), contentAlignment = Alignment.Center) {
                                    Ic(Lucide.Gauge, 20.dp, p.ink2)
                                }
                                Text(
                                    copy(UiMessages.settingsUsageAndSpeed()),
                                    style = T.s17,
                                    color = p.ink,
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis,
                                    modifier = Modifier.weight(1f),
                                )
                                Row(
                                    verticalAlignment = Alignment.CenterVertically,
                                    horizontalArrangement = Arrangement.spacedBy(4.dp),
                                ) {
                                    if (usageSummary.isNotEmpty()) {
                                        Text(
                                            usageSummary,
                                            style = T.s15.digits(),
                                            color = p.ink2,
                                            maxLines = 1,
                                            overflow = TextOverflow.Ellipsis,
                                            modifier = Modifier.widthIn(max = 200.dp),
                                        )
                                    }
                                    Arrow(p.ink3, T.s15)
                                }
                            }
                        }
                    }
                }
                ClientsAlignedCard(rows = rows)
            }

            // Module 2: 编程 CLI
            if (vm.cliVersionsList.isNotEmpty()) {
                SectionLabel(copy(UiMessages.settingsSectionCodingClis()))
                val conflictBanner = if (vm.hasCliSwitchesConflictBanner()) uiText(vm.cliSwitchesConflictBanner) else ""
                if (conflictBanner.isNotEmpty()) {
                    Notice(
                        conflictBanner,
                        Modifier.padding(horizontal = 16.dp).padding(bottom = 8.dp),
                        tone = NoticeTone.WARN,
                    )
                }
                ClientsAlignedCard(
                    rows = vm.cliVersionsList.map { cli ->
                        @Composable {
                            val unverifiedWord = uiText(cli.unverifiedLabel)
                                .ifEmpty { copy(UiMessages.settingsCliUnverifiedShort()) }
                                .trimEnd()
                                .removeSuffix("·")
                                .trimEnd()
                            val verText = cli.version.ifEmpty { copy(UiMessages.settingsAndroidUnknown()) }
                            val effDefaults = effectiveDefaultsFor(cli, vm)
                            val modelLbl = uiText(cli.defaultModelLabel).ifEmpty {
                                effDefaults?.let { uiText(it.selectedModelLabel).ifEmpty { it.selectedModel } }.orEmpty()
                            }
                            val modeLbl = uiText(cli.defaultModeLabel).ifEmpty {
                                effDefaults?.modeChoicesList?.firstOrNull { it.selected }?.let { uiText(it.label) }.orEmpty()
                            }
                            val modeDanger = cli.defaultModeDanger ||
                                effDefaults?.selectedMode == ApprovalMode.APPROVAL_MODE_FULL_ACCESS
                            Row(
                                Modifier.fillMaxWidth()
                                    .heightIn(min = 50.dp)
                                    .clickable {
                                        onSelectAgent(cli.agent)
                                        openCliAgent = cli.agent
                                    }
                                    .padding(start = 16.dp, end = 16.dp, top = 10.dp, bottom = 10.dp),
                                verticalAlignment = Alignment.CenterVertically,
                                horizontalArrangement = Arrangement.spacedBy(10.dp),
                            ) {
                                Box(Modifier.size(20.dp), contentAlignment = Alignment.Center) {
                                    AgentMarks.of(cli.agent)?.let {
                                        Icon(it, null, Modifier.size(20.dp), if (cli.enabled) p.ink2 else p.ink3)
                                    }
                                }
                                Column(Modifier.weight(1f)) {
                                    Text(
                                        cli.name,
                                        style = T.s17,
                                        color = if (cli.enabled) p.ink else p.ink2,
                                        maxLines = 1,
                                        overflow = TextOverflow.Ellipsis,
                                    )
                                    if (!cli.verified) {
                                        Text(
                                            buildAnnotatedString {
                                                withStyle(SpanStyle(color = p.orangeInk, fontWeight = FontWeight.SemiBold)) {
                                                    append(unverifiedWord)
                                                }
                                                withStyle(SpanStyle(color = p.ink2)) {
                                                    append(" · ")
                                                    append(verText)
                                                }
                                            },
                                            style = T.s13,
                                            maxLines = 1,
                                            overflow = TextOverflow.Ellipsis,
                                            modifier = Modifier.padding(top = 2.dp),
                                        )
                                    }
                                }
                                Row(
                                    verticalAlignment = Alignment.CenterVertically,
                                    horizontalArrangement = Arrangement.spacedBy(4.dp),
                                ) {
                                    if (!cli.enabled) {
                                        Text(
                                            uiText(cli.disabledLabel).ifEmpty { copy(UiMessages.settingsCliDisabled()) },
                                            style = T.s13,
                                            color = p.ink2,
                                            softWrap = false,
                                            maxLines = 1,
                                        )
                                    } else {
                                        if (modelLbl.isNotEmpty()) {
                                            Text(
                                                modelLbl,
                                                style = T.s13,
                                                color = p.ink2,
                                                maxLines = 1,
                                                overflow = TextOverflow.Ellipsis,
                                                modifier = Modifier.widthIn(max = 110.dp),
                                            )
                                        }
                                        if (modelLbl.isNotEmpty() && modeLbl.isNotEmpty()) {
                                            Text("·", style = T.s13, color = p.ink2, softWrap = false, maxLines = 1)
                                        }
                                        if (modeLbl.isNotEmpty()) {
                                            Text(
                                                modeLbl,
                                                style = T.s13,
                                                color = if (modeDanger) p.red else p.ink2,
                                                softWrap = false,
                                                maxLines = 1,
                                            )
                                        }
                                    }
                                    Arrow(p.ink3, T.s15)
                                }
                            }
                        }
                    },
                )
            }

            // Module 3: 运行与存储
            val readError = uiText(vm.settingsError)
            val saveErr = uiText(vm.saveError)
            val editableControls = vm.online && vm.editable && !vm.savePending
            if (vm.online && readError.isNotEmpty()) {
                Notice(
                    readError,
                    Modifier.padding(horizontal = 16.dp).padding(bottom = 8.dp),
                    NoticeTone.ERROR,
                    action = copy(UiMessages.commonRetry()),
                    onAction = onRetrySettings,
                )
            }
            if (saveErr.isNotEmpty()) {
                Notice(
                    saveErr,
                    Modifier.padding(horizontal = 16.dp).padding(bottom = 8.dp),
                    NoticeTone.ERROR,
                )
            }
            SectionLabel(copy(UiMessages.settingsSectionRuntimeAndStorage()))
            val timeoutLabel = vm.approvalTimeoutChoicesList.firstOrNull { it.selected }?.let { uiText(it.label) }
                ?: when {
                    vm.settingsLoading && vm.approvalTimeoutChoicesList.isEmpty() -> copy(UiMessages.projectsReadingDirectory())
                    vm.selectedApprovalTimeoutSeconds > 0 -> copy(UiMessages.quotaMin(vm.selectedApprovalTimeoutSeconds / 60))
                    else -> "—"
                }
            val idleLabel = vm.idleCloseChoicesList.firstOrNull { it.selected }?.let { uiText(it.label) }
                ?: if (vm.settingsLoading && vm.idleCloseChoicesList.isEmpty()) copy(UiMessages.projectsReadingDirectory()) else "—"
            val storageSummary = if (vm.hasAttachmentStorage()) {
                uiText(vm.attachmentStorage.summaryText).ifEmpty { "—" }
            } else {
                "—"
            }
            val runtimeRows: List<@Composable () -> Unit> = buildList {
                if (vm.member && vm.hasConnection()) {
                    add {
                        val conn = vm.connection
                        val rowVal = uiText(conn.rowValueText).ifEmpty { uiText(conn.statusText) }
                        val rowSub = uiText(conn.rowSubtitleText)
                        Row(
                            Modifier.fillMaxWidth()
                                .heightIn(min = 50.dp)
                                .clickable { showDirectSheet = true }
                                .padding(start = 18.dp, end = 16.dp, top = 10.dp, bottom = 10.dp),
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(10.dp),
                        ) {
                            Column(Modifier.weight(1f)) {
                                Text(copy(UiMessages.transportDirect()), style = T.s17, color = p.ink, maxLines = 1)
                                if (rowSub.isNotEmpty()) {
                                    Text(rowSub, style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                }
                            }
                            Row(
                                verticalAlignment = Alignment.CenterVertically,
                                horizontalArrangement = Arrangement.spacedBy(6.dp),
                            ) {
                                when {
                                    conn.rowSpinning -> Spinner(p.ink2, size = 12.dp)
                                    conn.rowDot != MachineStatusDot.MACHINE_STATUS_DOT_UNSPECIFIED ->
                                        StatusDotView(conn.rowDot, size = 7.dp)
                                }
                                if (rowVal.isNotEmpty()) {
                                    Text(rowVal, style = T.s15.digits(), color = p.ink2, maxLines = 1)
                                }
                                Arrow(p.ink3, T.s15)
                            }
                        }
                    }
                }
                add {
                    GRow(
                        copy(UiMessages.settingsApprovalTimeout()),
                        timeoutLabel,
                        singleLineValue = true,
                        onClick = if (editableControls && vm.approvalTimeoutChoicesList.isNotEmpty()) ({ pickTimeout = true }) else null,
                        chevron = true,
                    )
                }
                add {
                    GRow(
                        copy(UiMessages.settingsAutomaticallyEndIdleSessions()),
                        idleLabel,
                        singleLineValue = true,
                        onClick = if (editableControls && vm.idleCloseChoicesList.isNotEmpty()) ({ pickIdleClose = true }) else null,
                        chevron = true,
                    )
                }
                add {
                    GRow(
                        copy(UiMessages.settingsAttachmentStorage()),
                        storageSummary,
                        valueColor = if (vm.attachmentStorage.summaryWarn) p.orangeInk else null,
                        singleLineValue = true,
                        onClick = if (vm.member && vm.protocolMismatch == 0) onOpenStorage else null,
                        chevron = true,
                    )
                }
            }
            Group(rows = runtimeRows)

            // Module 4: 管理 + Bottom Danger
            SectionLabel(copy(UiMessages.settingsSectionManagement()))
            val rawProjectCount = uiText(vm.projectCountText)
            val showProjects = vm.projectCount > 0 || rawProjectCount.isNotEmpty()
            val projectCountLabel = rawProjectCount.ifEmpty {
                copy(UiMessages.settingsProjectCountValue(vm.projectCount))
            }
            val mgmtRows: List<@Composable () -> Unit> = buildList {
                if (showProjects) {
                    add {
                        GRow(
                            copy(UiMessages.settingsProjectsRow()),
                            projectCountLabel,
                            singleLineValue = true,
                            onClick = { onOpenProjects(vm.machineId) },
                            chevron = true,
                        )
                    }
                }
                add {
                    GRow(
                        copy(UiMessages.settingsDeviceId()),
                        trailing = {
                            Text(
                                vm.shortId,
                                style = T.s13.copy(fontFamily = FontFamily.Monospace),
                                color = p.ink2,
                                maxLines = 1,
                            )
                        },
                    )
                }
            }
            Group(rows = mgmtRows)

            val mgmt = vm.management
            if (mgmt.canRejoin) {
                Group(
                    rows = listOf {
                        GRow(
                            copy(UiMessages.settingsAndroidRejoinThisComputerAndResumeSessions()),
                            onClick = onRejoin,
                            enabled = appOnline,
                        )
                    },
                )
            }
            if (onDeleteRecords != null && mgmt.canDeleteRecords) {
                Group(
                    rows = listOf {
                        GRow(
                            copy(UiMessages.settingsDeleteComputerRecordsEllipsis()),
                            onClick = onDeleteRecords,
                            danger = true,
                        )
                    },
                )
            } else {
                DeleteMachineRecordsAction(vm.machineId, isAuthority)
            }
            if (mgmt.canRemove) {
                Group(
                    rows = listOf {
                        GRow(
                            copy(UiMessages.settingsRemoveComputerEllipsis()),
                            onClick = { confirmRemove = true },
                            danger = true,
                            enabled = !removeRunning && appOnline,
                        )
                    },
                )
            }
            val expHint = uiText(mgmt.explanationText)
            if (expHint.isNotEmpty()) {
                Hint(expHint, Modifier.padding(bottom = 8.dp), inset = 32.dp)
            }
            if (removeError.isNotEmpty()) {
                Notice(removeError, Modifier.padding(16.dp), NoticeTone.ERROR)
            }
            Spacer(Modifier.height(24.dp))
        }
    }

    if (confirmRemove && vm.member) {
        val (titleText, text) = removalDialog(title, machine = true, copy = copy)
        Alert(
            titleText,
            text,
            uiText(UiMessages.commonRemove()),
            danger = true,
            onDismiss = { confirmRemove = false },
        ) {
            onRemove()
        }
    }

    val openCli = openCliAgent?.let { ag -> vm.cliVersionsList.firstOrNull { it.agent == ag } }
    if (openCli != null) {
        val editableControls = vm.online && vm.editable && !vm.savePending
        CliSettingsSheet(
            vm = vm,
            cli = openCli,
            editableDefaults = editableControls,
            readError = uiText(vm.settingsError),
            onRetrySettings = onRetrySettings,
            onToggleAgent = onToggleAgent,
            onPickModel = { pickModelAgent = openCli.agent },
            onPickEffort = { pickEffortAgent = openCli.agent },
            onPickMode = { pickModeAgent = openCli.agent },
            onClose = { openCliAgent = null },
        )
    }

    if (showDirectSheet && vm.hasConnection()) {
        DirectConnectionSheet(
            machineId = vm.machineId,
            computerName = vm.name,
            view = vm.connection,
            diagVm = directDiagnostics,
            onToggle = onToggleDirect,
            onRetry = onRetryDirect,
            onRefreshDiag = onRefreshDirectDiag,
            onOpenDiag = onOpenDirectDiag,
            onCloseDiag = onCloseDirectDiag,
            onClose = { showDirectSheet = false },
        )
    }

    val modelTarget = pickModelAgent?.let { ag -> vm.cliVersionsList.firstOrNull { it.agent == ag }?.let { effectiveDefaultsFor(it, vm) }?.let { ag to it } }
    if (modelTarget != null && modelTarget.second.modelChoicesList.isNotEmpty()) {
        val (targetAgent, d) = modelTarget
        val opts = d.modelChoicesList.map { uiText(it.label) to it.desc.ifEmpty { null } }
        ChoiceSheet(
            copy(UiMessages.settingsAndroidValue1DefaultModelForNewSessions(d.agentName)),
            opts,
            d.modelChoicesList.indexOfFirst { it.selected }.coerceAtLeast(0),
            onPick = { i ->
                pickModelAgent = null
                onUpdateAgentDefaults(targetAgent, d.modelChoicesList[i].id, "", null)
            },
            onClose = { pickModelAgent = null },
        )
    }

    val effortTarget = pickEffortAgent?.let { ag -> vm.cliVersionsList.firstOrNull { it.agent == ag }?.let { effectiveDefaultsFor(it, vm) }?.let { ag to it } }
    if (effortTarget != null && effortTarget.second.effortChoicesList.isNotEmpty()) {
        val (targetAgent, d) = effortTarget
        val opts = d.effortChoicesList.map { uiText(it.label) to null }
        ChoiceSheet(
            copy(UiMessages.modelsEffort()),
            opts,
            d.effortChoicesList.indexOfFirst { it.selected }.coerceAtLeast(0),
            onPick = { i ->
                pickEffortAgent = null
                onUpdateAgentDefaults(targetAgent, d.selectedModel, d.effortChoicesList[i].id, null)
            },
            onClose = { pickEffortAgent = null },
        )
    }

    val modeTarget = pickModeAgent?.let { ag -> vm.cliVersionsList.firstOrNull { it.agent == ag }?.let { effectiveDefaultsFor(it, vm) }?.let { ag to it } }
    if (modeTarget != null && modeTarget.second.modeChoicesList.isNotEmpty()) {
        val (targetAgent, d) = modeTarget
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
            danger = { i -> d.modeChoicesList[i].danger || d.modeChoicesList[i].mode == ApprovalMode.APPROVAL_MODE_FULL_ACCESS },
            onPick = { i ->
                pickModeAgent = null
                onUpdateAgentDefaults(targetAgent, null, null, d.modeChoicesList[i].mode)
            },
            onClose = { pickModeAgent = null },
        )
    }

    if (pickTimeout && vm.approvalTimeoutChoicesList.isNotEmpty()) {
        ChoiceSheet(
            copy(UiMessages.settingsApprovalTimeout()),
            vm.approvalTimeoutChoicesList.map { uiText(it.label) to null },
            vm.approvalTimeoutChoicesList.indexOfFirst { it.selected }.coerceAtLeast(0),
            footer = copy(UiMessages.settingsTimeoutFooter()),
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
            footer = copy(UiMessages.settingsAndroidSessionsEndAutomaticallyAfterProlongedInactivityNotRunningNoPending()),
            onPick = { i ->
                pickIdleClose = false
                onUpdateDaemonSettings(null, vm.idleCloseChoicesList[i].minutes, null, false)
            },
            onClose = { pickIdleClose = false },
        )
    }
}

private fun effectiveDefaultsFor(cli: CliVersionView, vm: MachineSettingsViewModel): MachineAgentDefaultsView? = when {
    cli.hasDefaults() -> cli.defaults
    vm.hasAgentDefaults() && (vm.agentDefaults.agent == cli.agent || vm.cliVersionsCount == 1) -> vm.agentDefaults
    else -> null
}

@Composable
private fun CliSettingsSheet(
    vm: MachineSettingsViewModel,
    cli: CliVersionView,
    editableDefaults: Boolean,
    readError: String,
    onRetrySettings: () -> Unit,
    onToggleAgent: ((Agent, Boolean) -> Unit)?,
    onPickModel: () -> Unit,
    onPickEffort: () -> Unit,
    onPickMode: () -> Unit,
    onClose: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    val showToggle = vm.cliVersionsCount >= 2 && vm.showCliSwitches
    val locked = cli.lockedLastEnabled
    val toggleInteractive = vm.cliSwitchesEditable && !locked && !cli.saving && onToggleAgent != null
    val verText = cli.version.ifEmpty { copy(UiMessages.settingsAndroidUnknown()) }
    val unverifiedWord = uiText(cli.unverifiedLabel)
        .ifEmpty { copy(UiMessages.settingsCliUnverifiedShort()) }
        .trimEnd()
        .removeSuffix("·")
        .trimEnd()

    AdaptiveSheet(onClose = onClose) {
        SheetHead(cli.name, copy(UiMessages.commonDone()), onClose)
        if (vm.hasCliSwitchesConflictBanner() && uiText(vm.cliSwitchesConflictBanner).isNotEmpty()) {
            Notice(
                uiText(vm.cliSwitchesConflictBanner),
                Modifier.padding(bottom = 10.dp),
                NoticeTone.WARN,
            )
        }
        if (vm.hasCliSwitchesDisabledReason() && uiText(vm.cliSwitchesDisabledReason).isNotEmpty()) {
            Notice(
                uiText(vm.cliSwitchesDisabledReason),
                Modifier.padding(bottom = 10.dp),
                NoticeTone.WARN,
            )
        }
        SheetList {
            if (showToggle) {
                val sub = when {
                    locked -> copy(UiMessages.settingsCliSwitchesKeepOne())
                    else -> ""
                }
                val a11yText = if (locked) {
                    copy(UiMessages.settingsCliSwitchesA11YLocked(cli.name, verText))
                } else {
                    copy(
                        UiMessages.settingsCliSwitchesA11YRow(
                            name = cli.name,
                            state = if (cli.enabled) copy(UiMessages.settingsCliSwitchesStateOn()) else copy(UiMessages.settingsCliSwitchesStateOff()),
                            version = verText,
                        ),
                    )
                }
                Row(
                    Modifier.fillMaxWidth()
                        .heightIn(min = 50.dp)
                        .toggleable(
                            value = cli.enabled,
                            enabled = toggleInteractive,
                            role = Role.Switch,
                            onValueChange = { next -> onToggleAgent?.invoke(cli.agent, next) },
                        )
                        .semantics(mergeDescendants = true) {
                            contentDescription = a11yText
                            toggleableState = if (cli.enabled) ToggleableState.On else ToggleableState.Off
                        }
                        .padding(start = 18.dp, end = 16.dp, top = 8.dp, bottom = 8.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(10.dp),
                ) {
                    Column(Modifier.weight(1f)) {
                        Text(copy(UiMessages.settingsCliEnableOnComputer()), style = T.s17, color = p.ink)
                        if (sub.isNotEmpty()) {
                            Text(sub, style = T.s13, color = p.ink2)
                        }
                    }
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        if (cli.saving) Spinner(p.ink2, size = 16.dp)
                        Box(Modifier.alpha(if (locked) 0.4f else if (vm.cliSwitchesEditable) 1f else 0.5f)) {
                            Sw(cli.enabled)
                        }
                    }
                }
            }
            Box(if (showToggle) Modifier.topLine(p.sep, 18.dp) else Modifier) {
                GRow(
                    copy(UiMessages.settingsCliInstalledVersion()),
                    trailing = {
                        Text(
                            buildAnnotatedString {
                                if (!cli.verified) {
                                    withStyle(SpanStyle(color = p.orangeInk, fontWeight = FontWeight.SemiBold)) {
                                        append(unverifiedWord)
                                    }
                                    withStyle(SpanStyle(color = p.ink2)) { append(" · ") }
                                }
                                withStyle(SpanStyle(color = p.ink2)) { append(verText) }
                            },
                            style = T.s15.copy(fontFamily = FontFamily.Monospace),
                            maxLines = 1,
                        )
                    },
                )
            }
        }

        if (vm.cliSwitchesLeaseExpired) {
            Hint(copy(UiMessages.settingsCliSwitchesLeaseExpiredFooter()))
        }
        if (!cli.verified) {
            Hint(copy(UiMessages.settingsVersionUnverifiedRunRemoteAgentDoctorOnThisComputer()))
        }

        val d = effectiveDefaultsFor(cli, vm)
        if (!cli.enabled) {
            Hint(copy(UiMessages.settingsCliDisabledExplanation()))
        } else if (d != null) {
            FieldLabel(copy(UiMessages.settingsNewProjectDefaults()))
            val modelLabel = uiText(d.selectedModelLabel).ifEmpty {
                d.selectedModel.ifEmpty { copy(UiMessages.modelsDefault()) }
            }
            val selectedEffort = d.effortChoicesList.firstOrNull { it.selected }
            val effortLabel = selectedEffort?.let { uiText(it.label) }?.ifEmpty { null }
                ?: d.selectedEffort.ifEmpty { copy(UiMessages.modelsDefault()) }
            val selectedMode = d.modeChoicesList.firstOrNull { it.selected }
            val modeLabel = selectedMode?.let { uiText(it.label) }.orEmpty()
            val modeDanger = d.selectedMode == ApprovalMode.APPROVAL_MODE_FULL_ACCESS || selectedMode?.danger == true

            SheetList {
                GRow(
                    copy(UiMessages.settingsCliSheetModel()),
                    modelLabel,
                    singleLineValue = true,
                    onClick = if (editableDefaults && d.modelChoicesList.isNotEmpty()) onPickModel else null,
                    chevron = true,
                )
                if (d.effortChoicesList.size > 1) {
                    Box(Modifier.topLine(p.sep, 18.dp)) {
                        GRow(
                            copy(UiMessages.settingsCliSheetEffort()),
                            effortLabel,
                            singleLineValue = true,
                            onClick = if (editableDefaults) onPickEffort else null,
                            chevron = true,
                        )
                    }
                }
                Box(Modifier.topLine(p.sep, 18.dp)) {
                    GRow(
                        copy(UiMessages.settingsCliSheetMode()),
                        modeLabel,
                        singleLineValue = true,
                        valueColor = if (modeDanger) p.red else null,
                        onClick = if (editableDefaults && d.modeChoicesList.isNotEmpty()) onPickMode else null,
                        chevron = true,
                    )
                }
            }
            Hint(copy(UiMessages.settingsCliNewProjectDefaultsFooter()))
        } else if (vm.online && readError.isNotEmpty()) {
            Spacer(Modifier.height(10.dp))
            Notice(
                readError,
                tone = NoticeTone.ERROR,
                action = copy(UiMessages.commonRetry()),
                onAction = onRetrySettings,
            )
        } else if (!vm.online) {
            Hint(copy(UiMessages.settingsAndroidValue1IsOfflineConnectToReadAndEditNewSession(vm.name.ifEmpty { vm.machineId })))
        }
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
    val initialView = remember(remote, id) { remote.coreHost.snapshotQuotaView(id) }
    val rawView by remote.quotaView.collectAsState()
    val view = if (rawView.machineId == id) rawView else initialView
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
    val model = remember(remote, replacesDeviceId, settings.webOrigin) {
        RaApp.instance.pairing(
            remote,
            replacesDeviceId,
            webOrigin(remote.account.server, settings.webOrigin),
        )
    }
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
                val settingsVm by remote.settingsView.collectAsState()
                val syncVm by remote.coreHost.syncStateView.collectAsState()
                val label = settingsVm.machinesList.firstOrNull { it.machineId == replacesDeviceId }?.name
                    ?.takeIf { it.isNotEmpty() }
                    ?: syncVm.machinesList.firstOrNull { it.machineId == replacesDeviceId }?.displayName
                        ?.takeIf { it.isNotEmpty() }
                    ?: copy(UiMessages.commonUnknownComputer())
                Hint(
                    copy(UiMessages.settingsAndroidAllowTheOriginalComputerToResumeValue1SRetainedSessions(label)),
                )
            } else {
                Hint(copy(UiMessages.settingsAndroidToResumeARemovedComputerSSessionsOpenItsRetained()))
            }
            when (val step = state) {
                PairingUiState.Starting -> Skeleton()
                is PairingUiState.Waiting -> {
                    val bmp = remember(step.url) { qrBitmap(step.url) }
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
fun MachineAttachmentStorageScreen(id: String) {
    val remote = LocalRemote.current
    val initialVm = remember(remote, id) { remote.coreHost.snapshotMachineSettingsView(id) }
    val rawVm by remote.machineSettingsView.collectAsState()
    val vm = if (rawVm.machineId == id) rawVm else initialVm
    LaunchedEffect(id) { remote.coreHost.openMachineSettings(id) }
    MachineAttachmentStorageScreenContent(
        vm = vm,
        onRetry = { remote.coreHost.openMachineSettings(id) },
        onQuota = { bytes -> remote.coreHost.updateMachineDaemonSettings(id, imageUploadQuotaBytes = bytes) },
        onClean = { remote.coreHost.updateMachineDaemonSettings(id, cleanImages = true) },
    )
}

@Composable
internal fun MachineAttachmentStorageScreenContent(
    vm: MachineSettingsViewModel,
    onRetry: () -> Unit = {},
    onQuota: (Long) -> Unit = {},
    onClean: () -> Unit = {},
) {
    val copy = uiText()
    val subtitle = vm.name.ifEmpty { null }
    PageScaffold(copy(UiMessages.settingsAttachmentStorage()), subtitle = subtitle) {
        if (!vm.found && vm.loading) {
            PageNote(copy(UiMessages.commonLoading()))
            return@PageScaffold
        }
        Column(Modifier.weight(1f).settingsReadableColumn().verticalScrollFaded()) {
            val removedText = uiText(vm.removedBanner)
            val offlineOrMismatch = uiText(vm.offlineOrMismatchBanner)
            if (removedText.isNotEmpty()) {
                Notice(removedText, Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp))
            } else if (offlineOrMismatch.isNotEmpty()) {
                Notice(offlineOrMismatch, Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp))
            }
            val saveErr = uiText(vm.saveError)
            if (saveErr.isNotEmpty()) {
                Notice(saveErr, Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp), NoticeTone.ERROR)
            }
            val readError = uiText(vm.settingsError)
            val editableControls = vm.online && vm.editable && !vm.savePending
            if (!vm.attachmentStorage.loaded) {
                if (readError.isNotEmpty()) {
                    Notice(
                        readError,
                        Modifier.padding(horizontal = 16.dp).padding(bottom = 12.dp),
                        NoticeTone.ERROR,
                        action = copy(UiMessages.commonRetry()),
                        onAction = onRetry,
                    )
                } else if (!vm.online) {
                    PageNote(copy(UiMessages.settingsAndroidValue1IsOfflineConnectToReadAndEditNewSession(vm.name.ifEmpty { vm.machineId })))
                } else {
                    PageNote(copy(UiMessages.commonLoading()))
                }
            } else {
                MachineAttachmentStorage(
                    storage = vm.attachmentStorage,
                    enabled = editableControls && !vm.attachmentStorage.cleaning,
                    onQuota = onQuota,
                    onClean = onClean,
                )
            }
            Spacer(Modifier.height(24.dp))
        }
    }
}

@Composable
internal fun MachineAttachmentStorage(
    storage: AttachmentStorageView,
    enabled: Boolean,
    onQuota: (Long) -> Unit,
    onClean: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    var clean by remember { mutableStateOf(false) }
    var limit by remember { mutableStateOf(false) }
    val uploaded = uiText(storage.uploadedText).ifEmpty {
        if (storage.loaded) "0 B" else copy(UiMessages.projectsReadingDirectory())
    }
    val detail = uiText(storage.uploadedDetail)
    val uploadedValue = listOf(uploaded, detail).filter { it.isNotEmpty() }.joinToString(" · ")
    val quotaLabel =
        storage.quotaChoicesList.firstOrNull { it.selected }?.let { uiText(it.label) }
            ?: uiText(storage.limitLabel)
    val previewCache = uiText(storage.previewCacheText).ifEmpty { "—" }
    val emergencyText = uiText(storage.emergencyReserveText)
    val clearLabel = uiText(storage.clearButtonLabel)
    val usedTotal = uiText(storage.usedTotalText).ifEmpty { "—" }
    val limitHeadline = uiText(storage.limitLabel).ifEmpty { quotaLabel }.ifEmpty { "—" }

    val upPct = storage.uploadedPercent.coerceIn(0, 100)
    val prePct = storage.previewPercent.coerceIn(0, 100 - upPct)
    val resPct = storage.reservePercent.coerceIn(0, 100 - upPct - prePct)
    val remPct = (100 - upPct - prePct - resPct).coerceAtLeast(0)

    // Composition stacked bar card
    Column(
        Modifier.fillMaxWidth()
            .padding(start = 16.dp, end = 16.dp, bottom = 12.dp)
            .clip(Radii.R18)
            .background(p.gbg)
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.Bottom) {
            Text(
                copy(UiMessages.settingsStorageUsedHeadline(usedTotal)),
                style = T.s17b.digits(),
                color = p.ink,
                modifier = Modifier.weight(1f),
            )
            Text(
                copy(UiMessages.settingsStorageLimitHeadline(limitHeadline)),
                style = T.s13.digits(),
                color = p.ink2,
            )
        }
        Box(
            Modifier.fillMaxWidth()
                .height(10.dp)
                .clip(RoundedCornerShape(5.dp))
                .background(p.fill2),
        ) {
            Row(Modifier.fillMaxSize()) {
                if (upPct > 0) Box(Modifier.weight(upPct.toFloat()).fillMaxHeight().background(p.ink))
                if (prePct > 0) Box(Modifier.weight(prePct.toFloat()).fillMaxHeight().background(p.ink3))
                if (resPct > 0) Box(Modifier.weight(resPct.toFloat()).fillMaxHeight().background(p.orange))
                if (remPct > 0) Spacer(Modifier.weight(remPct.toFloat()))
            }
        }
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(14.dp)) {
            StorageLegendDot(p.ink, copy(UiMessages.settingsStorageLegendUploaded()))
            StorageLegendDot(p.ink3, copy(UiMessages.settingsStorageLegendPreview()))
            if (storage.showEmergencyReserve || resPct > 0) {
                StorageLegendDot(p.orange, copy(UiMessages.settingsStorageLegendReserve()))
            }
        }
    }

    SectionLabel(copy(UiMessages.settingsStorageSectionDetails()))
    Group(
        rows = buildList {
            add { GRow(copy(UiMessages.settingsUploadedImagesAndFiles()), uploadedValue, singleLineValue = true) }
            add { GRow(copy(UiMessages.settingsPreviewCache()), previewCache, singleLineValue = true) }
            if (storage.showEmergencyReserve && emergencyText.isNotEmpty()) {
                add {
                    Column(Modifier.fillMaxWidth().padding(start = 18.dp, end = 16.dp, top = 11.dp, bottom = 11.dp)) {
                        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                            Text(copy(UiMessages.machineStatusEmergencyReserve()), style = T.s17, color = p.ink, modifier = Modifier.weight(1f))
                            Text(emergencyText, style = T.s17.digits(), color = p.ink2)
                        }
                        Text(copy(UiMessages.machineStatusEmergencyReserveNote()), style = T.s13, color = p.ink2, modifier = Modifier.padding(top = 2.dp))
                    }
                }
            }
            add {
                GRow(
                    copy(UiMessages.settingsStorageLimitRow()),
                    quotaLabel,
                    singleLineValue = true,
                    onClick = if (enabled && storage.quotaChoicesList.isNotEmpty()) ({ limit = true }) else null,
                    chevron = true,
                )
            }
        },
    )

    Group(
        rows = listOf {
            GRow(
                copy(UiMessages.settingsStorageClearRow()),
                clearLabel,
                singleLineValue = true,
                onClick = if (enabled && storage.canClear && !storage.cleaning) ({ clean = true }) else null,
                action = true,
                enabled = enabled && storage.canClear && !storage.cleaning,
            )
        },
    )
    Hint(copy(UiMessages.settingsStoragePageFooter()), Modifier.padding(bottom = 12.dp), inset = 32.dp)

    if (limit && storage.quotaChoicesList.isNotEmpty()) {
        ChoiceSheet(
            copy(UiMessages.settingsStorageLimitRow()),
            storage.quotaChoicesList.map { uiText(it.label) to null },
            storage.quotaChoicesList.indexOfFirst { it.selected }.coerceAtLeast(0),
            onPick = { i ->
                limit = false
                onQuota(storage.quotaChoicesList[i].bytes)
            },
            onClose = { limit = false },
        )
    }
    if (clean) {
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
}

@Composable
private fun StorageLegendDot(color: Color, label: String) {
    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(5.dp)) {
        Box(Modifier.size(7.dp).clip(CircleShape).background(color))
        Text(label, style = T.s12, color = pal.ink2, maxLines = 1)
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
            used = app.attachments.usedBytes()
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
