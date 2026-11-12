package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

import android.content.Intent
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
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.core.content.FileProvider
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.DeviceSecurity
import io.github.lewin671.remoteagent.core.clientDetail
import io.github.lewin671.remoteagent.core.clientName
import io.github.lewin671.remoteagent.core.clientUsed
import io.github.lewin671.remoteagent.core.dayText
import io.github.lewin671.remoteagent.core.defaultClientName
import io.github.lewin671.remoteagent.core.lastUsed
import io.github.lewin671.remoteagent.core.shortId
import io.github.lewin671.remoteagent.core.sortClients
import io.github.lewin671.remoteagent.core.whenText
import io.github.lewin671.remoteagent.proto.DeviceKind
import java.io.File
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * 设备: computers, where the CLIs run, apart from clients, which operate them. A computer's online
 * state says nothing about the clients, and a paired client is not shown as watching or online.
 */
@Composable
fun DevicesScreen() {
    val copy = uiText()
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val p = pal
    val t = now()
    val me = remote.account.deviceId
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    // The times on this page are as of opening it.
    LaunchedEffect(Unit) {
        if (remote.state.value.conn == ConnState.ONLINE) remote.refreshDevices()
    }
    PageScaffold(copy(UiMessages.settingsDevices())) {
        Column(Modifier.weight(1f).verticalScrollFaded()) {
            if (state.clockWarning)
                Notice(
                    copy(UiMessages.settingsAndroidThePhoneClockIsAheadOfTheServerRemoteControl()),
                    Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                    NoticeTone.WARN,
                )
            if (state.isAuthority && state.recoveryPending)
                Notice(
                    copy(UiMessages.securityAndroidSomeoneIsRequestingManagementTakeoverWithARecoveryCodeVeto()),
                    Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                    NoticeTone.ERROR,
                    action = copy(UiMessages.securityAndroidView()),
                    onAction = { nav.recovery() },
                )
            GLabel(copy(UiMessages.settingsComputersRunClis()))
            val ms = state.machines.values.sortedBy { machineLabel(state, it.id) }
            if (ms.isEmpty())
                Hint(copy(UiMessages.securityAndroidNoComputersYet()), Modifier.padding(bottom = 12.dp))
            else
                Group(
                    rows =
                        ms.map { m ->
                            @Composable {
                                GRow(
                                    machineLabel(state, m.id),
                                    onClick = { nav.device(m.id) },
                                    trailing = {
                                        Row(
                                            verticalAlignment = Alignment.CenterVertically,
                                            horizontalArrangement = Arrangement.spacedBy(5.dp),
                                        ) {
                                            if (m.online) StatusMark(MarkKind.ONLINE)
                                            ArrowText(
                                                (if (m.online) copy(UiMessages.securityAndroidOnline())
                                                else
                                                    whenText(m.lastSeenMs, t, copy = copy).ifEmpty {
                                                        copy(UiMessages.securityAndroidOffline())
                                                    }),
                                                p.ink2,
                                                T.s13,
                                            )
                                        }
                                    },
                                )
                            }
                        },
                )
            GLabel(copy(UiMessages.settingsClientsControlSessions()))
            val clients =
                sortClients(
                    state.devices.values.filter { it.kind == DeviceKind.DEVICE_KIND_CLIENT },
                    me,
                )
            Group(
                rows =
                    clients.map { c ->
                        @Composable {
                            Row(
                                Modifier.fillMaxWidth()
                                    .heightIn(min = 44.dp)
                                    .clickable { nav.client(c.id) }
                                    .padding(horizontal = 14.dp, vertical = 8.dp),
                                verticalAlignment = Alignment.CenterVertically,
                                horizontalArrangement = Arrangement.spacedBy(10.dp),
                            ) {
                                Column(Modifier.weight(1f)) {
                                    Text(clientName(c, c.id, state.devices.values, copy = copy), style = T.s15, color = p.ink)
                                    Text(
                                        clientDetail(c, copy = copy),
                                        style = T.s13,
                                        color = p.ink2,
                                        modifier = Modifier.padding(top = 2.dp),
                                    )
                                }
                                if (c.id == state.authorityDeviceId)
                                    Text(copy(UiMessages.settingsManagingClient()), style = T.s13, color = p.ink2)
                                else if (c.id == me) Text(copy(UiMessages.securityThisDevice()), style = T.s13, color = p.ink2)
                                else ArrowText(clientUsed(c, t, copy = copy), p.ink2, T.s13)
                            }
                        }
                    },
            )
            if (state.isAuthority) {
                GLabel(copy(UiMessages.securityAndroidRemoteControl()))
                Hint(
                    if (state.leaseEndMs > 0)
                        copy(UiMessages.securityAndroidControlValidityAdvice(io.github.lewin671.remoteagent.core.securityTime(state.leaseEndMs, t, copy = copy)))
                    else copy(UiMessages.securityAndroidReadingMembershipState()),
                )
            }
            Text(
                copy(UiMessages.securityAndroidLastUsedTimesAreOnTheRightEachDeviceChanges()),
                style = T.s13,
                color = p.ink2,
                modifier = Modifier.padding(horizontal = 16.dp),
            )
            if (state.isAuthority)
                Box(
                    Modifier.padding(horizontal = 16.dp, vertical = 8.dp)
                        .heightIn(min = 44.dp)
                        .clickable { nav.pair() },
                    contentAlignment = Alignment.CenterStart,
                ) {
                    IconText(Lucide.Plus, copy(UiMessages.homeAddDevice()), p.blue, T.s15)
                }
            else
                Hint(
                    addDeviceHint(state, copy = copy),
                    Modifier.padding(bottom = 8.dp),
                )
            if (state.isAuthority) {
                Box(Modifier.padding(horizontal = 16.dp)) {
                    TextAction(
                        copy(UiMessages.securityAndroidExportGenesisStateForOfflineRelayPinning()),
                        {
                            scope.launch {
                                val genesis =
                                    withContext(Dispatchers.IO) {
                                        DeviceSecurity.stateBytes(remote.chain().first())
                                    }
                                withContext(Dispatchers.Main) {
                                    shareGenesis(context, genesis, copy = copy)
                                }
                            }
                        },
                        arrow = true,
                    )
                }
                Hint(
                    copy(UiMessages.securityAndroidThisPublicGenesisStateContainsNoKeysUseItOnly()),
                    Modifier.padding(bottom = 4.dp),
                )
            }
            Spacer(Modifier.height(24.dp))
        }
    }
}

/** A client's details: its name for the whole account, its ID to compare, and removing it. */
@Composable
fun ClientScreen(id: String) {
    val copy = uiText()
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val p = pal
    val me = remote.account.deviceId
    val canRename = id in state.devices && clientCanRename(id, me, state.conn)
    val d = state.devices[id]
    val t = now()
    var editing by remember { mutableStateOf(false) }
    var confirmRemove by remember { mutableStateOf(false) }
    val save = rememberAction()
    val remove = rememberAction(authority = true)
    PageScaffold(
        clientName(d, id, state.devices.values, copy = copy),
        subtitle = if (d == null) copy(UiMessages.securityAndroidRemoved()) else if (id == me) copy(UiMessages.securityAndroidCurrentClient()) else copy(UiMessages.settingsPairedClient()),
    ) {
        if (d == null) {
            PageNote(copy(UiMessages.securityAndroidThisClientIsNoLongerInTheCurrentMemberList()))
            return@PageScaffold
        }
        Column(Modifier.weight(1f).verticalScrollFaded()) {
            GLabel(if (id == state.authorityDeviceId) copy(UiMessages.settingsManagingClient()) else copy(UiMessages.securityClient()))
            Group(
                rows =
                    listOfNotNull<@Composable () -> Unit>(
                        {
                            GRow(
                                copy(UiMessages.settingsName()),
                                (d?.meta?.name?.ifEmpty { null } ?: copy(UiMessages.settingsUnnamed())),
                                onClick = if (canRename) ({ editing = true }) else null,
                                chevron = canRename,
                            )
                        },
                        d?.meta
                            ?.name
                            ?.takeIf { it.isNotEmpty() }
                            ?.let {
                                {
                                    GRow(
                                        copy(UiMessages.settingsRestoreDefaultName()),
                                        defaultClientName(d, id, state.devices.values, copy = copy),
                                        onClick = {
                                            save.run(
                                                {
                                                    remote.renameDevice(id, "")
                                                    null
                                                },
                                            )
                                        },
                                        action = true,
                                        enabled = canRename,
                                    )
                                }
                            },
                        d?.meta
                            ?.description
                            ?.takeIf { it.isNotEmpty() }
                            ?.let { desc -> { GRow(copy(UiMessages.settingsRunsOn()), desc) } },
                        { GRow(copy(UiMessages.settingsLastUsed()), if (id == me) copy(UiMessages.settingsNow()) else lastUsed(d, t, copy = copy).ifEmpty { copy(UiMessages.settingsNoRecord()) }) },
                        d?.createdMs
                            ?.takeIf { it > 0 }
                            ?.let { created -> { GRow(copy(UiMessages.settingsAdded()), dayText(created, t, copy = copy)) } },
                        {
                            GRow(
                                copy(UiMessages.settingsDeviceId()),
                                trailing = {
                                    Text("…" + shortId(id), style = T.mono13, color = p.ink2)
                                },
                            )
                        },
                    ),
            )
            if (save.error.isNotEmpty())
                Notice(
                    copy(UiMessages.settingsNameSaveError((save.error).toString())),
                    Modifier.padding(horizontal = 16.dp).padding(bottom = 8.dp),
                    NoticeTone.ERROR,
                )
            Text(
                if (id == me) copy(UiMessages.securityAndroidNameChangesSyncToOtherClientsSetNotificationsOnThis())
                else copy(UiMessages.securityAndroidOnlyThisClientCanChangeItsNameNotificationSettingsMust()),
                style = T.s13,
                color = p.ink2,
                modifier = Modifier.padding(horizontal = 16.dp),
            )
            if (id == me) {
                Box(
                    Modifier.padding(horizontal = 16.dp, vertical = 12.dp)
                        .heightIn(min = 44.dp)
                        .clickable { nav.settings() },
                    contentAlignment = Alignment.CenterStart,
                ) {
                    ArrowText(copy(UiMessages.securityAndroidExitAndDeleteLocalKeysInSettings()), p.ink2, T.s15)
                }
            } else if (state.isAuthority) {
                if (id == state.authorityDeviceId) {
                    Text(
                        copy(UiMessages.securityTheManagingClientCannotBeRemovedDirectlyTransferManagementDeviceManagement()),
                        style = T.s13,
                        color = p.ink2,
                        modifier = Modifier.padding(horizontal = 16.dp),
                    )
                } else {
                    Box(
                        Modifier.padding(horizontal = 16.dp, vertical = 12.dp)
                            .heightIn(min = 44.dp)
                            .clickable(
                                enabled = state.conn == ConnState.ONLINE && !remove.running,
                            ) {
                                confirmRemove = true
                            },
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
                }
                if (remove.error.isNotEmpty())
                    Notice(remove.error, Modifier.padding(16.dp), NoticeTone.ERROR)
            } else {
                Hint(
                    copy(UiMessages.settingsAndroidRemoveDevicesOnTheManagingClientValue1(state.authorityName.resolve(copy))),
                    Modifier.padding(bottom = 8.dp),
                )
            }
        }
    }
    if (confirmRemove && d != null) {
        val (title, text) = removalDialog(clientName(d, id, state.devices.values, copy = copy), machine = false, copy = copy)
        Alert(
            title,
            text,
            copy(UiMessages.commonRemove()),
            danger = true,
            onDismiss = { confirmRemove = false },
        ) {
            remove.run(
                {
                    remote.removeDevice(id)
                    null
                },
            )
        }
    }
    if (editing && canRename)
        InputAlert(
            copy(UiMessages.settingsClientName()),
            d?.meta?.name.orEmpty(),
            defaultClientName(d, id, state.devices.values, copy = copy),
            copy(UiMessages.commonSave()),
            onDismiss = { editing = false },
        ) { name ->
            save.run(
                {
                    remote.renameDevice(id, name.trim())
                    null
                },
            )
        }
}

/** The one confirmation before removal: names the device and says what happens to it. */
internal fun removalDialog(name: String, machine: Boolean, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): Pair<String, String> =
    copy(UiMessages.projectsRemoveTitle((name).toString())) to
        if (machine) copy(UiMessages.securityAndroidThisImmediatelyDisconnectsItAndRevokesAccessKeepingSessionsAnd())
        else copy(UiMessages.securityAndroidThisDisconnectsItsLoginAndClearsPushSubscriptionsComputerSessions())

/** Shares the public genesis state for the relay's offline pin step. */
internal fun shareGenesis(context: android.content.Context, state: ByteArray, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText) {
    val dir = File(context.cacheDir, SHARED_DIR).apply { mkdirs() }
    val file = File(dir, "public-genesis.bin")
    file.writeBytes(state)
    val uri = FileProvider.getUriForFile(context, context.packageName + ".files", file)
    context.startActivity(
        Intent.createChooser(
            Intent(Intent.ACTION_SEND)
                .setType("application/octet-stream")
                .putExtra(Intent.EXTRA_STREAM, uri)
                .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION),
            copy(UiMessages.securityAndroidExportGenesisState()),
        ),
    )
}

/** Both name actions follow the same client ownership/connection policy. */
internal fun clientCanRename(id: String, me: String, connection: ConnState): Boolean =
    id == me && connection == ConnState.ONLINE
