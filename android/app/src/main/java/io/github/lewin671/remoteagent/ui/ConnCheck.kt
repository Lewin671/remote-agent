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
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.Ping
import io.github.lewin671.remoteagent.core.SLOW_SERVER_MS
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.median
import io.github.lewin671.remoteagent.core.proto.ConnCheckDirectRowView
import io.github.lewin671.remoteagent.core.proto.ConnProbeState
import io.github.lewin671.remoteagent.core.proto.ConnState as CoreConnState
import io.github.lewin671.remoteagent.core.proto.MachineLatencyState
import io.github.lewin671.remoteagent.core.proto.MachineLatencyView
import io.github.lewin671.remoteagent.core.proto.MachineListCardView
import io.github.lewin671.remoteagent.core.proto.SettingsViewModel
import java.text.NumberFormat
import java.util.Locale
import kotlinx.coroutines.CancellationException
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
        pingServer = { remote.pingServer() },
        reconnectNow = { remote.reconnectNow() },
        onProbeStatus = { s, ms, err -> remote.coreHost.reportConnCheckStatus(s, ms, err) },
        onStartCheck = { remote.coreHost.startConnCheck() },
        onStopCheck = { remote.coreHost.stopConnCheck() },
    )
}

@Composable
internal fun ConnCheckSheet(
    settingsView: SettingsViewModel,
    onClose: () -> Unit,
    serverAddress: String,
    pingServer: suspend () -> Long,
    reconnectNow: () -> Unit,
    clock: () -> Long = System::currentTimeMillis,
    onProbeStatus: (ConnProbeState, Int, String) -> Unit = { _, _, _ -> },
    onStartCheck: () -> Unit = {},
    onStopCheck: () -> Unit = {},
) {
    val copy = uiText()
    val p = pal
    var server by remember { mutableStateOf<Ping>(Ping.Wait) }
    var run by remember { mutableIntStateOf(0) }
    var serverBusy by remember { mutableStateOf(true) }
    val currentOnStop by rememberUpdatedState(onStopCheck)
    val cc = settingsView.connCheck
    val isConnected = cc.connState == CoreConnState.CONN_STATE_CONNECTED
    val startedAt = cc.connStartedAtMs.takeIf { it > 0L }
    val hasConnStep = cc.connStep.isCoreTextNotEmpty()
    val connecting = cc.isConnecting && hasConnStep && startedAt != null
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
    DisposableEffect(Unit) {
        onDispose { currentOnStop() }
    }
    LaunchedEffect(run) {
        serverBusy = true
        server = Ping.Wait
        onProbeStatus(ConnProbeState.CONN_PROBE_STATE_CHECKING, 0, "")
        onStartCheck()
        try {
            val ms = median(List(3) { pingServer() })
            server = Ping.Ok(ms)
            onProbeStatus(
                if (ms >= SLOW_SERVER_MS) ConnProbeState.CONN_PROBE_STATE_SLOW
                else ConnProbeState.CONN_PROBE_STATE_WORKING,
                ms.toInt(),
                "",
            )
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            val err = displayError(e).ifEmpty { copy(UiMessages.connectionNoResponse()) }
            server = Ping.Fail(err)
            onProbeStatus(ConnProbeState.CONN_PROBE_STATE_UNAVAILABLE, 0, err)
        } finally {
            serverBusy = false
        }
    }
    val busy = serverBusy || cc.machinesBusy
    val largeFont = LocalDensity.current.fontScale >= 1.8f

    @Composable
    fun dotted(color: Color, text: String, ink: Color = p.ink2) =
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            StatusMark(MarkKind.ONLINE, size = 9.dp, color = color)
            Text(text, style = T.s17.digits(), color = ink, maxLines = 1, softWrap = false)
        }

    @Composable
    fun spinning(text: String) =
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            Spinner(p.ink, size = 12.dp, stroke = 1.5.dp)
            Text(text, style = T.s17.digits(), color = p.ink2, maxLines = 1, softWrap = false)
        }

    @Composable
    fun standardLine(
        name: String,
        sub: String? = null,
        tag: String = "",
        tagA11y: String = "",
        value: @Composable () -> Unit,
    ) {
        if (largeFont) {
            Column(Modifier.fillMaxWidth().heightIn(min = 50.dp).padding(start = 18.dp, end = 16.dp, top = 11.dp, bottom = 11.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text(name, style = T.s17, color = p.ink, modifier = Modifier.weight(1f, fill = false))
                    if (tag.isNotEmpty()) DisambiguatorTag(tag, a11y = tagA11y)
                }
                if (sub != null) Text(sub, style = T.s13, color = p.ink2, modifier = Modifier.padding(top = 1.dp))
                Row(Modifier.fillMaxWidth().padding(top = 4.dp), horizontalArrangement = Arrangement.End) { value() }
            }
            return
        }
        Row(
            Modifier.fillMaxWidth().heightIn(min = 50.dp).padding(start = 18.dp, end = 16.dp, top = 11.dp, bottom = 11.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text(name, style = T.s17, color = p.ink, modifier = Modifier.weight(1f, fill = false))
                    if (tag.isNotEmpty()) DisambiguatorTag(tag, a11y = tagA11y)
                }
                if (sub != null) Text(sub, style = T.s13, color = p.ink2, modifier = Modifier.padding(top = 1.dp))
            }
            value()
        }
    }

    @Composable
    fun serverResult(ping: Ping) = when (ping) {
        Ping.Wait -> spinning(copy(UiMessages.connectionChecking()))
        is Ping.Fail -> Text(
            ping.error,
            style = T.s17,
            color = p.red,
            textAlign = TextAlign.End,
            modifier = Modifier.widthIn(max = 168.dp),
        )
        is Ping.Ok -> {
            val slow = ping.ms >= SLOW_SERVER_MS
            val formatted = formatLatencyMs(ping.ms)
            val label = copy(
                if (slow) UiMessages.connectionLatencyMsSlow(formatted)
                else UiMessages.connectionLatencyMs(formatted),
            )
            dotted(if (slow) p.faint else p.green, label)
        }
    }

    @Composable
    fun machineLatencyValue(latency: MachineLatencyView) {
        val valText = uiText(latency.valueText)
        when (latency.state) {
            MachineLatencyState.MACHINE_LATENCY_STATE_MEASURING ->
                spinning(valText.ifEmpty { copy(UiMessages.connectionChecking()) })
            MachineLatencyState.MACHINE_LATENCY_STATE_DONE ->
                dotted(if (latency.slow) p.faint else p.green, valText)
            MachineLatencyState.MACHINE_LATENCY_STATE_FAILED ->
                Text(
                    valText,
                    style = T.s17,
                    color = p.red,
                    textAlign = TextAlign.End,
                    modifier = Modifier.widthIn(max = 168.dp),
                )
            else ->
                Text(
                    valText.ifEmpty { copy(UiMessages.connectionNotChecked()) },
                    style = T.s17,
                    color = p.ink2,
                    maxLines = 1,
                    softWrap = false,
                )
        }
    }

    @Composable
    fun machineLatencyRow(m: MachineListCardView, directRow: ConnCheckDirectRowView?, latency: MachineLatencyView) {
        val name = m.name.ifEmpty { m.machineId }
        val tagA11y = if (m.hasDisambiguatorA11Y()) uiText(m.disambiguatorA11Y) else ""
        val summary = when {
            latency.hasSummaryOverrideText() && latency.summaryOverrideText.isCoreTextNotEmpty() ->
                uiText(latency.summaryOverrideText)
            directRow != null && directRow.visible -> {
                val statusWord = uiText(directRow.statusText)
                val detailWord = uiText(directRow.detailText).takeIf { it.isNotEmpty() }
                listOfNotNull(statusWord.takeIf { it.isNotEmpty() }, detailWord).joinToString(" · ")
            }
            else -> ""
        }
        val a11y = if (latency.hasA11YText() && latency.a11YText.isCoreTextNotEmpty()) uiText(latency.a11YText) else ""
        val rowMod = Modifier
            .fillMaxWidth()
            .then(
                if (a11y.isNotEmpty()) Modifier.semantics(mergeDescendants = true) { contentDescription = a11y }
                else Modifier,
            )
            .padding(start = 18.dp, end = 16.dp, top = 13.dp, bottom = 13.dp)

        Column(rowMod) {
            if (largeFont) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text(name, style = T.s17, color = p.ink, modifier = Modifier.weight(1f, fill = false))
                    if (m.disambiguator.isNotEmpty()) DisambiguatorTag(m.disambiguator, a11y = tagA11y)
                }
                if (summary.isNotEmpty()) Text(summary, style = T.s13, color = p.ink2, modifier = Modifier.padding(top = 1.dp))
                Row(Modifier.fillMaxWidth().padding(top = 4.dp), horizontalArrangement = Arrangement.End) {
                    machineLatencyValue(latency)
                }
            } else {
                Row(
                    Modifier.fillMaxWidth(),
                    verticalAlignment = Alignment.Top,
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    Column(Modifier.weight(1f)) {
                        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                            Text(name, style = T.s17, color = p.ink, modifier = Modifier.weight(1f, fill = false))
                            if (m.disambiguator.isNotEmpty()) DisambiguatorTag(m.disambiguator, a11y = tagA11y)
                        }
                        if (summary.isNotEmpty()) {
                            Text(summary, style = T.s13, color = p.ink2, modifier = Modifier.padding(top = 1.dp))
                        }
                    }
                    machineLatencyValue(latency)
                }
            }
            if (latency.segmentsCount > 0) {
                Column(Modifier.fillMaxWidth().padding(top = 8.dp)) {
                    for (seg in latency.segmentsList) {
                        val emphasized = seg.slow || seg.stuck
                        val segStyle = (if (emphasized) T.s13b else T.s13).copy(lineHeight = 20.sp)
                        val segColor = if (emphasized) p.ink else p.ink2
                        val label = uiText(seg.labelText)
                        val slowTag = if (seg.slow && seg.hasSlowLabelText()) uiText(seg.slowLabelText) else ""
                        val leftText = if (slowTag.isNotEmpty()) label + " · " + slowTag else label
                        val segVal = uiText(seg.valueText)
                        Row(
                            Modifier.fillMaxWidth(),
                            verticalAlignment = Alignment.Top,
                            horizontalArrangement = Arrangement.spacedBy(12.dp),
                        ) {
                            Text(leftText, style = segStyle, color = segColor, modifier = Modifier.weight(1f))
                            Text(
                                segVal,
                                style = segStyle.digits(),
                                color = segColor,
                                textAlign = TextAlign.End,
                                maxLines = 1,
                                softWrap = false,
                            )
                        }
                        val detail = if (seg.hasDetailText()) uiText(seg.detailText) else ""
                        if (detail.isNotEmpty()) {
                            Text(
                                detail,
                                style = T.s12.copy(lineHeight = 17.sp),
                                color = p.ink2,
                                modifier = Modifier.fillMaxWidth().padding(bottom = 3.dp),
                            )
                        }
                    }
                }
            }
            val note = if (latency.hasNoteText()) uiText(latency.noteText) else ""
            if (note.isNotEmpty()) {
                Text(
                    note,
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.fillMaxWidth().padding(top = if (latency.segmentsCount > 0) 4.dp else 8.dp),
                )
            }
        }
    }

    Sheet(onClose) {
        SheetHead(copy(UiMessages.homeConnectionCheck()), copy(UiMessages.commonDone()), onClose)
        Column(Modifier.fillMaxWidth().clip(Radii.R18).background(if (LocalOnSoft.current) p.raised else p.gbg)) {
            standardLine(copy(UiMessages.connectionLivePush())) {
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
            HorizontalDivider(Modifier.padding(start = 18.dp), thickness = 0.5.dp, color = p.sep)
            val network = cc.networkAvailable
            standardLine(copy(UiMessages.connectionLocalNetwork())) {
                dotted(if (network) p.green else p.faint, if (network) copy(UiMessages.connectionWorking()) else copy(UiMessages.connectionUnavailable()))
            }
            HorizontalDivider(Modifier.padding(start = 18.dp), thickness = 0.5.dp, color = p.sep)
            standardLine(copy(UiMessages.securityServer()), serverAddress.substringAfter("://")) { serverResult(server) }
            for (m in settingsView.machinesList) {
                HorizontalDivider(Modifier.padding(start = 18.dp), thickness = 0.5.dp, color = p.sep)
                val directRow: ConnCheckDirectRowView? = if (m.online && m.member && m.hasDirectRow()) m.directRow else null
                if (m.online && m.hasLatency()) {
                    machineLatencyRow(m, directRow, m.latency)
                } else {
                    val tagA11y = if (m.hasDisambiguatorA11Y()) uiText(m.disambiguatorA11Y) else ""
                    standardLine(m.name.ifEmpty { m.machineId }, tag = m.disambiguator, tagA11y = tagA11y) {
                        if (!m.online) {
                            dotted(p.faint, copy(UiMessages.settingsOffline()), p.ink2)
                        } else {
                            val fallback = if (m.hasStatusText()) uiText(m.statusText) else ""
                            Text(
                                fallback.ifEmpty { copy(UiMessages.connectionNotChecked()) },
                                style = T.s17,
                                color = p.ink2,
                                maxLines = 1,
                                softWrap = false,
                            )
                        }
                    }
                }
            }
        }
        if (!isConnected) {
            val rawFailure = if (cc.hasLastFailureText()) uiText(cc.lastFailureText) else ""
            Hint(
                when {
                    connecting -> {
                        val seconds = ((tick - startedAt!!) / 1000).coerceAtLeast(0)
                        val failureClause = if (rawFailure.isEmpty()) "" else copy(UiMessages.connectionLastFailure(rawFailure))
                        copy(
                            UiMessages.connectionSecElapsed(
                                seconds.coerceAtMost(Int.MAX_VALUE.toLong()).toInt(),
                                uiText(cc.connStep),
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
        val foot = (if (cc.hasFooterText()) uiText(cc.footerText) else "")
            .ifEmpty { copy(UiMessages.connectionLatencyIsOneRoundTripTheServerRowMeasures()) }
        Hint(foot)
        Btns(Modifier.padding(top = 12.dp)) {
            if (!isConnected) PillButton(copy(UiMessages.connectionReconnectNow()), reconnectNow, Modifier.weight(1f), ButtonKind.INK)
            PillButton(
                copy(UiMessages.connectionTestAgain()),
                { run++ },
                Modifier.weight(1f),
                kind = if (isConnected) ButtonKind.INK else ButtonKind.SECONDARY,
                enabled = !busy,
            )
        }
    }
}

private fun formatLatencyMs(ms: Long): String =
    NumberFormat.getIntegerInstance(Locale.US).format(ms)
