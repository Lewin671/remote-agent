package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

import android.content.Intent
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.proto.ClientDetailViewModel
import io.github.lewin671.remoteagent.core.proto.ClientRowView
import io.github.lewin671.remoteagent.core.proto.DevicesViewModel
import kotlinx.coroutines.launch

/**
 * 客户端: paired clients and unfinished pairings on this account. Computers are managed from Settings.
 */
@Composable
fun DevicesScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val initialVm = remember(remote) { remote.coreHost.snapshotDevicesView() }
    val rawVm by remote.devicesView.collectAsState()
    val vm = if (rawVm != DevicesViewModel.getDefaultInstance()) rawVm else initialVm
    val t = now()
    LaunchedEffect(Unit) {
        remote.coreHost.openDevices()
        if (vm.online) remote.refreshDevices()
    }
    DevicesScreenContent(
        vm = vm,
        nowMs = t,
        onRecovery = { nav.recovery() },
        onClientClick = { nav.client(it) },
        onPair = { nav.pair() },
    )
}

@Composable
internal fun DevicesScreenContent(
    vm: DevicesViewModel,
    clockWarning: Boolean = vm.clockSkewed,
    recoveryPending: Boolean = vm.isManager && vm.recoveryPending,
    leaseEndMs: Long = vm.leaseEndMs,
    nowMs: Long = System.currentTimeMillis(),
    onRecovery: () -> Unit = {},
    onClientClick: (String) -> Unit = {},
    onPair: () -> Unit = {},
) {
    val copy = uiText()
    val p = pal
    PageScaffold(copy(UiMessages.settingsClientsTitle())) {
        Column(
            Modifier.weight(1f)
                .settingsReadableColumn()
                .verticalScrollFaded(),
        ) {
            if (clockWarning) {
                Notice(
                    copy(UiMessages.settingsAndroidThePhoneClockIsAheadOfTheServerRemoteControl()),
                    Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                    NoticeTone.WARN,
                )
            }
            if (recoveryPending) {
                Notice(
                    copy(UiMessages.securityAndroidSomeoneIsRequestingManagementTakeoverWithARecoveryCodeVeto()),
                    Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                    NoticeTone.ERROR,
                    action = copy(UiMessages.securityAndroidView()),
                    onAction = onRecovery,
                )
            }
            val err = uiText(vm.errorText)
            if (err.isNotEmpty()) {
                Notice(
                    err,
                    Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                    NoticeTone.ERROR,
                )
            }

            SectionLabel(
                copy(UiMessages.settingsClientsGroupPaired()),
                trailing = uiText(vm.clientCountText),
            )
            val pairedRows: List<@Composable () -> Unit> = buildList {
                for (c in vm.clientsList) {
                    add { DeviceClientRow(c, unfinished = false, onClick = { onClientClick(c.deviceId) }) }
                }
                if (vm.canAddDevice) {
                    add {
                        Row(
                            Modifier.fillMaxWidth().heightIn(min = 50.dp)
                                .clickable(role = androidx.compose.ui.semantics.Role.Button, onClick = onPair)
                                .padding(horizontal = 16.dp, vertical = 10.dp),
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(10.dp),
                        ) {
                            Box(Modifier.size(20.dp), contentAlignment = Alignment.Center) {
                                Ic(Lucide.Plus, 18.dp, p.blue)
                            }
                            Text(copy(UiMessages.settingsClientsAddClient()), style = T.s17, color = p.blue)
                        }
                    }
                }
            }
            if (pairedRows.isNotEmpty()) {
                ClientsAlignedCard(rows = pairedRows)
            }
            if (!vm.canAddDevice) {
                val addHint = uiText(vm.addDeviceHint)
                if (addHint.isNotEmpty()) {
                    Hint(addHint, Modifier.padding(bottom = 8.dp), inset = 32.dp)
                }
            }

            if (vm.unfinishedPairingsList.isNotEmpty()) {
                ClientsAlignedCard(
                    rows = vm.unfinishedPairingsList.map { u ->
                        @Composable { DeviceClientRow(u, unfinished = true, onClick = { onClientClick(u.deviceId) }) }
                    },
                )
            }

            if (vm.isManager) {
                Hint(
                    if (leaseEndMs > 0) {
                        copy(UiMessages.securityAndroidControlValidityAdvice(io.github.lewin671.remoteagent.core.securityTime(leaseEndMs, nowMs, copy = copy)))
                    } else {
                        copy(UiMessages.securityAndroidReadingMembershipState())
                    },
                    inset = 32.dp,
                )
            }
            Hint(copy(UiMessages.settingsClientsFooterRedesign()), Modifier.padding(bottom = 8.dp), inset = 32.dp)
            Spacer(Modifier.height(24.dp))
        }
    }
}

@Composable
internal fun ClientsAlignedCard(rows: List<@Composable () -> Unit>) {
    val p = pal
    Column(
        Modifier.fillMaxWidth()
            .padding(start = 16.dp, end = 16.dp, bottom = 12.dp)
            .clip(Radii.R18)
            .background(if (LocalOnSoft.current) p.raised else p.gbg),
    ) {
        rows.forEachIndexed { i, r ->
            Box(if (i > 0) Modifier.topLine(p.sep, 46.dp) else Modifier) { r() }
        }
    }
}

@Composable
private fun DeviceClientRow(c: ClientRowView, unfinished: Boolean, onClick: () -> Unit) {
    val p = pal
    val copy = uiText()
    val role = uiText(c.roleLabel)
    val used = uiText(c.lastUsedText)
    val subtitle = if (unfinished) {
        listOfNotNull(
            copy(UiMessages.settingsClientsPairingIncompleteDesc()),
            c.shortId.ifEmpty { null },
        ).joinToString(" · ")
    } else {
        listOfNotNull(
            c.description.ifEmpty { null },
            c.shortId.ifEmpty { null },
            role.ifEmpty { null },
        ).joinToString(" · ")
    }
    Row(
        Modifier.fillMaxWidth()
            .heightIn(min = 50.dp)
            .clickable(onClick = onClick)
            .padding(horizontal = 16.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Box(Modifier.size(20.dp), contentAlignment = Alignment.Center) {
            Ic(clientIconForDescription(c.description), 20.dp, p.ink2)
        }
        Column(Modifier.weight(1f)) {
            Text(c.displayName, style = T.s17m, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (subtitle.isNotEmpty()) {
                Text(
                    subtitle,
                    style = T.s13,
                    color = p.ink2,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(top = 2.dp),
                )
            }
        }
        if (unfinished) {
            Text(
                copy(UiMessages.settingsClientsPairingIncompleteBadge()),
                style = T.s13b,
                color = p.orangeInk,
                maxLines = 1,
            )
            Arrow(p.ink3, T.s15)
        } else {
            val trail = used.ifEmpty { role }
            if (trail.isNotEmpty()) {
                Text(trail, style = T.s13, color = p.ink2, maxLines = 1)
            }
            Arrow(p.ink3, T.s15)
        }
    }
}

/** A client's details: its name for the whole account, its ID to compare, and removing it. */
@Composable
fun ClientScreen(id: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val syncView by remote.coreHost.syncStateView.collectAsState()
    val initialVm = remember(remote, id) { remote.coreHost.snapshotClientDetailView(id) }
    val rawVm by remote.clientDetailView.collectAsState()
    val vm = if (rawVm.deviceId == id) rawVm else initialVm
    val rename by SettingsExecutor.renameState.collectAsState()
    val remove = rememberAction(authority = true)
    LaunchedEffect(id) {
        SettingsExecutor.clearRenameError()
        remote.coreHost.openClientDetail(id)
    }
    ClientScreenContent(
        vm = vm,
        rename = rename,
        canRemoveNow = vm.canRemove && syncView.connection == io.github.lewin671.remoteagent.core.proto.ConnState.CONN_STATE_CONNECTED && !remove.running,
        removeError = remove.error,
        onSettings = { nav.settings() },
        onRename = { name ->
            SettingsExecutor.clearRenameError()
            remote.coreHost.renameClient(id, name)
        },
        onRemove = {
            remove.run({ remote.removeDevice(id) })
        },
    )
}

@Composable
internal fun ClientScreenContent(
    vm: ClientDetailViewModel,
    rename: RenameClientUiState = RenameClientUiState(),
    canRemoveNow: Boolean = vm.canRemove,
    removeError: String = "",
    onSettings: () -> Unit = {},
    onRename: (String) -> Unit = {},
    onRemove: () -> Unit = {},
) {
    val copy = uiText()
    val p = pal
    var editing by remember(vm.deviceId) { mutableStateOf(false) }
    var confirmRemove by remember(vm.deviceId) { mutableStateOf(false) }
    val renameBusy = vm.renameSaving || (rename.savingDeviceId != null && rename.savingDeviceId == vm.deviceId)
    val renameError = rename.error.ifEmpty { uiText(vm.renameError) }
    PageScaffold(
        vm.displayName,
        subtitle =
            if (!vm.found) copy(UiMessages.securityAndroidRemoved())
            else if (vm.isSelf) copy(UiMessages.securityAndroidCurrentClient())
            else copy(UiMessages.settingsPairedClient()),
    ) {
        if (!vm.found) {
            PageNote(copy(UiMessages.securityAndroidThisClientIsNoLongerInTheCurrentMemberList()))
            return@PageScaffold
        }
        val groupLabel =
            uiText(vm.roleLabel).ifEmpty {
                if (vm.isAuthority) copy(UiMessages.settingsManagingClient()) else copy(UiMessages.securityClient())
            }
        val lastUsedWord = uiText(vm.lastUsedText).ifEmpty { copy(UiMessages.settingsNoRecord()) }
        val addedWord = uiText(vm.addedText)
        Column(Modifier.weight(1f).verticalScrollFaded()) {
            GLabel(groupLabel)
            Group(
                rows =
                    listOfNotNull<@Composable () -> Unit>(
                        {
                            GRow(
                                copy(UiMessages.settingsName()),
                                vm.customName.ifEmpty { copy(UiMessages.settingsUnnamed()) },
                                onClick = if (vm.canRename && !renameBusy) ({ editing = true }) else null,
                                chevron = vm.canRename,
                            )
                        },
                        if (vm.canRestoreDefault) {
                            {
                                GRow(
                                    copy(UiMessages.settingsRestoreDefaultName()),
                                    vm.defaultName,
                                    onClick = { onRename("") },
                                    action = true,
                                    enabled = vm.canRename && !renameBusy,
                                )
                            }
                        } else null,
                        vm.description
                            .takeIf { it.isNotEmpty() }
                            ?.let { desc -> { GRow(copy(UiMessages.settingsRunsOn()), desc) } },
                        { GRow(copy(UiMessages.settingsLastUsed()), lastUsedWord) },
                        addedWord
                            .takeIf { it.isNotEmpty() }
                            ?.let { added -> { GRow(copy(UiMessages.settingsAdded()), added) } },
                        {
                            GRow(
                                copy(UiMessages.settingsDeviceId()),
                                trailing = {
                                    Text(vm.shortId, style = T.mono13, color = p.ink2)
                                },
                            )
                        },
                    ),
            )
            if (renameError.isNotEmpty())
                Notice(
                    copy(UiMessages.settingsNameSaveError((renameError).toString())),
                    Modifier.padding(horizontal = 16.dp).padding(bottom = 8.dp),
                    NoticeTone.ERROR,
                )
            Text(
                if (vm.isSelf) copy(UiMessages.securityAndroidNameChangesSyncToOtherClientsSetNotificationsOnThis())
                else copy(UiMessages.securityAndroidOnlyThisClientCanChangeItsNameNotificationSettingsMust()),
                style = T.s13,
                color = p.ink2,
                modifier = Modifier.padding(horizontal = 16.dp),
            )
            val mgmtHint = uiText(vm.managementHint)
            if (vm.showExitAction) {
                Box(
                    Modifier.padding(horizontal = 16.dp, vertical = 12.dp)
                        .heightIn(min = 44.dp)
                        .clickable { onSettings() },
                    contentAlignment = Alignment.CenterStart,
                ) {
                    ArrowText(copy(UiMessages.securityAndroidExitAndDeleteLocalKeysInSettings()), p.ink2, T.s15)
                }
            } else if (vm.canRemove) {
                Box(
                    Modifier.padding(horizontal = 16.dp, vertical = 12.dp)
                        .heightIn(min = 44.dp)
                        .clickable(enabled = canRemoveNow) { confirmRemove = true },
                    contentAlignment = Alignment.CenterStart,
                ) {
                    Text(copy(UiMessages.securityAndroidRemoveThisClient()), style = T.s15, color = p.red)
                }
                Text(
                    copy(UiMessages.securityAndroidRemovalDisconnectsItsLoginAndClearsPushSubscriptionsComputerSessions()),
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.padding(horizontal = 16.dp),
                )
                if (removeError.isNotEmpty())
                    Notice(removeError, Modifier.padding(16.dp), NoticeTone.ERROR)
            } else if (mgmtHint.isNotEmpty()) {
                Hint(
                    mgmtHint,
                    Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                )
            }
        }
    }
    if (confirmRemove && vm.found) {
        val (title, text) = removalDialog(vm.displayName, machine = false, copy = copy)
        Alert(
            title,
            text,
            copy(UiMessages.commonRemove()),
            danger = true,
            onDismiss = { confirmRemove = false },
        ) {
            onRemove()
        }
    }
    if (editing && vm.canRename)
        InputAlert(
            copy(UiMessages.settingsClientName()),
            vm.customName,
            vm.defaultName,
            copy(UiMessages.commonSave()),
            onDismiss = { editing = false },
        ) { name ->
            onRename(name.trim())
        }
}

/** The one confirmation before removal: names the device and says what happens to it. */
internal fun removalDialog(name: String, machine: Boolean, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): Pair<String, String> =
    copy(UiMessages.projectsRemoveTitle((name).toString())) to
        if (machine) copy(UiMessages.securityAndroidThisImmediatelyDisconnectsItAndRevokesAccessKeepingSessionsAnd())
        else copy(UiMessages.securityAndroidThisDisconnectsItsLoginAndClearsPushSubscriptionsComputerSessions())
