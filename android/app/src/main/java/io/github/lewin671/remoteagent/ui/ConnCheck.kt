package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.Ping
import io.github.lewin671.remoteagent.core.SLOW_MACHINE_MS
import io.github.lewin671.remoteagent.core.SLOW_SERVER_MS
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.median
import io.github.lewin671.remoteagent.core.proto.ConnCheckDirectRowView
import io.github.lewin671.remoteagent.core.proto.ConnProbeState
import io.github.lewin671.remoteagent.core.proto.ConnState as CoreConnState
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupStage
import io.github.lewin671.remoteagent.core.proto.MachineListCardView
import io.github.lewin671.remoteagent.core.proto.SettingsViewModel
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay

@Composable
fun ConnCheckSheet(onClose: () -> Unit) {
    val remote = LocalRemote.current
    val initialVm = remember(remote) { remote.coreHost.snapshotSettingsView() }
    val rawVm by remote.settingsView.collectAsState()
    val settingsView = if (rawVm != SettingsViewModel.getDefaultInstance()) rawVm else initialVm
    ConnCheckSheet(
        settingsView = settingsView,
        onClose = onClose,
        serverAddress = remote.account.server,
        onlineMachines = {
            val vm = remote.settingsView.value.takeIf { it != SettingsViewModel.getDefaultInstance() } ?: initialVm
            vm.machinesList.filter { it.online }.map { it.machineId }
        },
        pingServer = { remote.pingServer() },
        pingMachine = { remote.pingMachine(it) },
        reconnectNow = { remote.reconnectNow() },
        onProbeStatus = { s, ms, err -> remote.coreHost.reportConnCheckStatus(s, ms, err) },
    )
}

@Composable
internal fun ConnCheckSheet(
    settingsView: SettingsViewModel,
    onClose: () -> Unit,
    serverAddress: String,
    onlineMachines: () -> List<String>,
    pingServer: suspend () -> Long,
    pingMachine: suspend (String) -> Long,
    reconnectNow: () -> Unit,
    clock: () -> Long = System::currentTimeMillis,
    onProbeStatus: (ConnProbeState, Int, String) -> Unit = { _, _, _ -> },
) {
    val copy = uiText()
    val p = pal
    var server by remember { mutableStateOf<Ping>(Ping.Wait) }
    val machines = remember { mutableStateMapOf<String, Ping>() }
    var run by remember { mutableStateOf(0) }
    var busy by remember { mutableStateOf(true) }
    val cc = settingsView.connCheck
    val isConnected = cc.connState == CoreConnState.CONN_STATE_CONNECTED
    val startedAt = cc.connStartedAtMs.takeIf { it > 0L }
    val stepText = if (cc.hasConnStep()) uiText(cc.connStep) else ""
    val connecting = !isConnected && stepText.isNotEmpty() && startedAt != null
    var tick by remember(startedAt) { mutableStateOf(clock()) }
    LaunchedEffect(connecting, startedAt) {
        if (connecting) {
            tick = clock()
            while (true) {
                delay(1000)
                tick = clock()
            }
        }
    }
    val list = settingsView.machinesList
    val directReadyById = remember(settingsView.machinesList) {
        settingsView.machinesList.associate { it.machineId to it.directReady }
    }
    LaunchedEffect(run) {
        busy = true
        onProbeStatus(ConnProbeState.CONN_PROBE_STATE_CHECKING, 0, "")
        val online = onlineMachines()
        server = Ping.Wait
        machines.clear()
        online.forEach { machines[it] = Ping.Wait }
        var reached = true
        try {
            val ms = median(List(3) { pingServer() })
            server = Ping.Ok(ms)
            onProbeStatus(
                if (ms >= SLOW_SERVER_MS) ConnProbeState.CONN_PROBE_STATE_SLOW
                else ConnProbeState.CONN_PROBE_STATE_WORKING,
                ms.toInt(),
                "",
            )
        } catch (e: kotlinx.coroutines.CancellationException) {
            throw e
        } catch (e: Exception) {
            reached = false
            val err = displayError(e).ifEmpty { copy(UiMessages.connectionNoResponse()) }
            server = Ping.Fail(err)
            onProbeStatus(ConnProbeState.CONN_PROBE_STATE_UNAVAILABLE, 0, err)
            online.filter { directReadyById[it] != true }.forEach { machines[it] = Ping.Skip }
        }
        coroutineScope {
            online.filter { reached || directReadyById[it] == true }.map { id ->
                async {
                    machines[id] = runCatching { Ping.Ok(pingMachine(id)) }.getOrElse {
                        if (it is kotlinx.coroutines.CancellationException) throw it
                        else Ping.Fail(displayError(it).ifEmpty { copy(UiMessages.connectionNoResponse()) })
                    }
                }
            }.awaitAll()
        }
        busy = false
    }
    @Composable
    fun line(name: String, sub: String? = null, value: @Composable () -> Unit) {
        Row(
            Modifier.fillMaxWidth().heightIn(min = 44.dp).padding(horizontal = 14.dp, vertical = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Column(Modifier.weight(1f)) {
                Text(name, style = T.s15, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (sub != null) Text(sub, style = T.s13, color = p.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
            value()
        }
    }
    @Composable
    fun dotted(color: androidx.compose.ui.graphics.Color, text: String, ink: androidx.compose.ui.graphics.Color = p.ink) =
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            StatusMark(MarkKind.ONLINE, color = color)
            Text(text, style = T.s13, color = ink)
        }
    @Composable
    fun result(ping: Ping?, slow: Long) = when (ping) {
        null, Ping.Wait -> IconText(Lucide.LoaderCircle, copy(UiMessages.connectionChecking()), p.ink2, T.s13)
        Ping.Skip -> Text(copy(UiMessages.connectionNotChecked()), style = T.s13, color = p.ink2)
        is Ping.Fail -> Text(ping.error, style = T.s13, color = p.red, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.widthIn(max = 200.dp))
        is Ping.Ok -> dotted(if (ping.ms >= slow) p.orange else p.green, "${ping.ms} ms" + if (ping.ms >= slow) copy(UiMessages.connectionSlow()) else "")
    }
    Sheet(onClose) {
        SheetHead(copy(UiMessages.homeConnectionCheck()), copy(UiMessages.commonDone()), onClose)
        Column(Modifier.fillMaxWidth().clip(Radii.R14).background(if (LocalOnSoft.current) p.raised else p.gbg)) {
            line(copy(UiMessages.connectionLivePush())) {
                val connLabel = when (cc.connState) {
                    CoreConnState.CONN_STATE_CONNECTED -> copy(UiMessages.connectionConnected())
                    CoreConnState.CONN_STATE_CONNECTING -> copy(UiMessages.connectionConnectingConnCheck())
                    CoreConnState.CONN_STATE_RECONNECTING -> copy(UiMessages.connectionReconnectingConnCheck())
                    else ->
                        if (cc.everOnline) copy(UiMessages.connectionReconnectingConnCheck())
                        else copy(UiMessages.connectionUnreachable())
                }
                val dotColor = when {
                    isConnected -> p.green
                    cc.everOnline || cc.connState == CoreConnState.CONN_STATE_RECONNECTING -> p.orange
                    else -> p.faint
                }
                dotted(dotColor, connLabel)
            }
            HorizontalDivider(color = p.sep, thickness = 0.5.dp)
            val network = cc.networkAvailable
            line(copy(UiMessages.connectionLocalNetwork())) {
                dotted(if (network) p.green else p.faint, if (network) copy(UiMessages.connectionWorking()) else copy(UiMessages.connectionUnavailable()))
            }
            HorizontalDivider(color = p.sep, thickness = 0.5.dp)
            line(copy(UiMessages.securityServer()), serverAddress.substringAfter("://")) { result(server, SLOW_SERVER_MS) }
            for (m in list) {
                HorizontalDivider(color = p.sep, thickness = 0.5.dp)
                val directRow: ConnCheckDirectRowView? = if (m.online && m.member && m.hasDirectRow()) m.directRow else null
                line(m.name.ifEmpty { m.machineId }) {
                    if (m.online) result(machines[m.machineId], SLOW_MACHINE_MS)
                    else dotted(p.faint, copy(UiMessages.settingsOffline()), p.ink2)
                }
                if (directRow != null && directRow.visible) {
                    val statusWord = uiText(directRow.statusText)
                    val detailWord = uiText(directRow.detailText).takeIf { it.isNotEmpty() }
                    val summary = listOfNotNull(statusWord.takeIf { it.isNotEmpty() }, detailWord).joinToString(" · ")
                    if (summary.isNotEmpty()) {
                        Text(
                            summary,
                            style = T.s13,
                            color = p.ink2,
                            modifier = Modifier.fillMaxWidth().padding(start = 28.dp, end = 14.dp, bottom = 8.dp),
                        )
                    }
                }
            }
        }
        if (!isConnected) {
            val rawFailure = if (cc.hasLastFailureText()) uiText(cc.lastFailureText) else ""
            val isAlreadyPrefixed = cc.hasLastFailureText() && cc.lastFailureText.key == UiMessages.connectionLastFailure("").key
            Hint(
                when {
                    stepText.isNotEmpty() && startedAt != null -> {
                        val seconds = ((tick - startedAt) / 1000).coerceAtLeast(0)
                        val failureClause = when {
                            rawFailure.isEmpty() -> ""
                            isAlreadyPrefixed -> rawFailure
                            else -> copy(UiMessages.connectionLastFailure(rawFailure))
                        }
                        copy(
                            UiMessages.connectionSecElapsed(
                                seconds.coerceAtMost(Int.MAX_VALUE.toLong()).toInt(),
                                stepText,
                                failureClause,
                                copy(if (cc.everOnline) UiMessages.connectionReconnectingConnCheckLabel() else UiMessages.connectionConnectingConnCheckLabel()),
                            ),
                        )
                    }
                    rawFailure.isNotEmpty() -> copy(UiMessages.connectionLivePushIsNotConnectedSessionChangesMayBe(rawFailure))
                    !cc.networkAvailable || cc.connState == CoreConnState.CONN_STATE_NO_NETWORK ->
                        copy(UiMessages.connectionThisDeviceHasNoNetworkItWillReconnectWhen())
                    else -> copy(UiMessages.connectionLivePushIsNotConnected())
                },
            )
        }
        val locale = LocalMessageLocale.current
        val foot = buildList {
            add(copy(UiMessages.connectionLatencyIsOneRoundTripTheServerRowMeasures()))
            if (list.any {
                it.online &&
                    it.member &&
                    it.hasDirectRow() &&
                    it.directRow.stage == ConnectionGroupStage.CONNECTION_GROUP_STAGE_UNSUPPORTED
            }) {
                add(copy(UiMessages.transportConnFooterUnsupportedApp()))
            }
        }.joinToString(if (locale.startsWith("zh")) "" else " ")
        Hint(foot)
        Btns(Modifier.padding(top = 12.dp)) {
            if (!isConnected) PillButton(copy(UiMessages.connectionReconnectNow()), reconnectNow, Modifier.weight(1f))
            PillButton(copy(UiMessages.connectionTestAgain()), { run++ }, Modifier.weight(1f), enabled = !busy)
        }
    }
}
