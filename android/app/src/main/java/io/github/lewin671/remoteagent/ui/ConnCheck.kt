package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

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
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.Link
import io.github.lewin671.remoteagent.core.Ping
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SLOW_MACHINE_MS
import io.github.lewin671.remoteagent.core.SLOW_SERVER_MS
import io.github.lewin671.remoteagent.ui.displayError
import io.github.lewin671.remoteagent.core.median
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay

/** The link to the server in a word: what 设置 says before anything is measured. */
fun connWord(state: RemoteState, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): String = when {
    state.conn == ConnState.ONLINE -> copy(UiMessages.connectionConnected())
    state.synced -> copy(UiMessages.connectionReconnectingConnCheck())
    state.link == Link.NO_SERVER -> copy(UiMessages.connectionUnreachable())
    else -> copy(UiMessages.connectionConnectingConnCheck())
}

/**
 * 连接检测 (client-v34): how long a round trip takes now on each stretch of the way, the
 * nearest first: this device's network, the server, then each computer through the
 * server. Measured when opened and on 再测一次; nothing runs in the background.
 */
@Composable
fun ConnCheckSheet(state: RemoteState, onClose: () -> Unit) {
    val remote = LocalRemote.current
    ConnCheckSheet(
        state, onClose, remote.account.server,
        { remote.state.value.machines.values.filter { it.online }.map { it.id } },
        { remote.pingServer() }, { remote.pingMachine(it) },
        { remote.reconnectNow() },
    )
}

@Composable
internal fun ConnCheckSheet(
    state: RemoteState,
    onClose: () -> Unit,
    serverAddress: String,
    onlineMachines: () -> List<String>,
    pingServer: suspend () -> Long,
    pingMachine: suspend (String) -> Long,
    reconnectNow: () -> Unit,
    clock: () -> Long = System::currentTimeMillis,
) {
    val copy = uiText()
    val p = pal
    var server by remember { mutableStateOf<Ping>(Ping.Wait) }
    val machines = remember { mutableStateMapOf<String, Ping>() }
    var run by remember { mutableStateOf(0) }
    var busy by remember { mutableStateOf(true) }
    val startedAt = state.connStartedAtMs
    val connecting = state.conn != ConnState.ONLINE && state.connStep.isNotEmpty() && startedAt != null
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
    val list = state.machines.values.sortedWith(compareByDescending<io.github.lewin671.remoteagent.core.MachineView> { it.online }.thenBy { machineLabel(state, it.id) })
    LaunchedEffect(run) {
        busy = true
        val online = onlineMachines()
        server = Ping.Wait
        machines.clear()
        online.forEach { machines[it] = Ping.Wait }
        try {
            server = Ping.Ok(median(List(3) { pingServer() }))
            coroutineScope {
                online.map { id ->
                    async { machines[id] = runCatching { Ping.Ok(pingMachine(id)) }.getOrElse { if (it is kotlinx.coroutines.CancellationException) throw it else Ping.Fail(displayError(it).ifEmpty { copy(UiMessages.connectionNoResponse()) }) } }
                }.awaitAll()
            }
        } catch (e: kotlinx.coroutines.CancellationException) {
            throw e
        } catch (e: Exception) {
            server = Ping.Fail(displayError(e).ifEmpty { copy(UiMessages.connectionNoResponse()) })
            online.forEach { machines[it] = Ping.Skip }
        }
        busy = false
    }
    @Composable
    fun line(name: String, sub: String? = null, value: @Composable () -> Unit) {
        Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).padding(horizontal = 14.dp, vertical = 6.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Column(Modifier.weight(1f)) {
                Text(name, style = T.s15, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (sub != null) Text(sub, style = T.s12, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
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
                dotted(if (state.conn == ConnState.ONLINE) p.green else if (state.synced) p.orange else p.faint, connWord(state, copy = uiText()))
            }
            HorizontalDivider(color = p.sep, thickness = 0.5.dp)
            val network = state.link != Link.NO_NETWORK
            line(copy(UiMessages.connectionLocalNetwork())) { dotted(if (network) p.green else p.faint, if (network) copy(UiMessages.connectionWorking()) else copy(UiMessages.connectionUnavailable())) }
            HorizontalDivider(color = p.sep, thickness = 0.5.dp)
            line(copy(UiMessages.securityServer()), serverAddress.substringAfter("://")) { result(server, SLOW_SERVER_MS) }
            for (m in list) {
                HorizontalDivider(color = p.sep, thickness = 0.5.dp)
                line(machineLabel(state, m.id)) { if (m.online) result(machines[m.id], SLOW_MACHINE_MS) else dotted(p.faint, copy(UiMessages.commonAndroidOffline()), p.ink2) }
            }
        }
        if (state.conn != ConnState.ONLINE) {
            Hint(when {
                state.connStep.isNotEmpty() && startedAt != null -> {
                    val seconds = ((tick - startedAt) / 1000).coerceAtLeast(0)
                    copy(UiMessages.connectionSecElapsed(
                        seconds.coerceAtMost(Int.MAX_VALUE.toLong()).toInt(),
                        state.connStep.resolve(copy),
                        if (state.connError.isNotEmpty()) copy(UiMessages.connectionLastFailure(state.connError.resolve(copy))) else "",
                        copy(if (state.synced) UiMessages.connectionReconnectingConnCheckLabel() else UiMessages.connectionConnectingConnCheckLabel()),
                    ))
                }
                state.connError.isNotEmpty() -> copy(UiMessages.connectionLivePushIsNotConnectedSessionChangesMayBe(state.connError.resolve(copy)))
                state.link == Link.NO_NETWORK -> copy(UiMessages.connectionThisDeviceHasNoNetworkItWillReconnectWhen())
                else -> copy(UiMessages.connectionLivePushIsNotConnected())
            })
        }
        Hint(copy(UiMessages.connectionLatencyIsOneRoundTripTheServerRowMeasures()))
        Btns(Modifier.padding(top = 12.dp)) {
            if (state.conn != ConnState.ONLINE) PillButton(copy(UiMessages.connectionReconnectNow()), reconnectNow, Modifier.weight(1f))
            PillButton(copy(UiMessages.connectionTestAgain()), { run++ }, Modifier.weight(1f), enabled = !busy)
        }
    }
}
