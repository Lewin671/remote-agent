@file:Suppress("DEPRECATION")

package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.Crossfade
import androidx.compose.animation.core.tween
import androidx.compose.animation.expandVertically
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.shrinkVertically
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.transport.DirectDiag
import io.github.lewin671.remoteagent.core.transport.DirectFailReason
import io.github.lewin671.remoteagent.core.transport.DirectStage
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupStage
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupTone
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupView
import kotlinx.coroutines.delay

/**
 * Renders Core's 「连接」 group ([ConnectionGroupView]) directly inside [DeviceScreen].
 */
@Composable
internal fun DirectConnectionGroup(
    view: ConnectionGroupView,
    onDiagnostics: (() -> Unit)? = null,
    onToggle: (Boolean) -> Unit,
) {
    val copy = uiText()
    val p = pal
    val supported = view.toggleVisible
    val statusText = uiText(view.statusText)
    val detailText = uiText(view.detailText)
    val metricText = uiText(view.metricText)
    val fileSpeed = uiText(view.fileSpeedText)
    val readLatency = uiText(view.readLatencyText)
    val footerText = uiText(view.footerText).ifEmpty { copy(UiMessages.transportFooter()) }
    val showStatus = statusText.isNotEmpty() || detailText.isNotEmpty() || metricText.isNotEmpty()

    val enter = if (Motion.reduced) fadeIn(tween(150)) else expandVertically(tween(200)) + fadeIn(tween(200))
    val exit = if (Motion.reduced) fadeOut(tween(150)) else shrinkVertically(tween(200)) + fadeOut(tween(200))

    GLabel(copy(UiMessages.transportGroup()))
    Column(
        Modifier.fillMaxWidth()
            .padding(start = 16.dp, end = 16.dp, bottom = 6.dp)
            .clip(Radii.R20)
            .background(if (LocalOnSoft.current) p.raised else p.gbg),
    ) {
        Box(Modifier.alpha(if (supported) 1f else 0.4f)) {
            ToggleRow(
                copy(UiMessages.transportDirect()),
                "",
                value = supported && view.toggleChecked,
                enabled = view.toggleEnabled,
                onChange = onToggle,
            )
        }
        AnimatedVisibility(visible = showStatus, enter = enter, exit = exit) {
            Box(Modifier.topLine(p.sep, 18.dp)) {
                DirectStatusRow(view.stage, view.tone, statusText, detailText, metricText)
            }
        }
        val showSpeeds = view.stage != ConnectionGroupStage.CONNECTION_GROUP_STAGE_DISABLED &&
            view.stage != ConnectionGroupStage.CONNECTION_GROUP_STAGE_UNSUPPORTED &&
            view.stage != ConnectionGroupStage.CONNECTION_GROUP_STAGE_OFFLINE
        if (showSpeeds && fileSpeed.isNotEmpty()) {
            Box(Modifier.topLine(p.sep, 18.dp)) {
                SpeedMetricRow(copy(UiMessages.transportSpeedFile()), fileSpeed)
            }
        }
        if (showSpeeds && readLatency.isNotEmpty()) {
            Box(Modifier.topLine(p.sep, 18.dp)) {
                SpeedMetricRow(copy(UiMessages.transportSpeedRead()), readLatency)
            }
        }
        if (view.showDiagnosticsEntry && onDiagnostics != null) {
            Box(Modifier.topLine(p.sep, 18.dp)) {
                GRow(
                    copy(UiMessages.transportDiag()),
                    onClick = onDiagnostics,
                    chevron = true,
                )
            }
        }
    }
    Hint(footerText, Modifier.padding(bottom = 12.dp))
}

@Composable
internal fun DirectDiagSheet(
    machineId: String,
    state: RemoteState,
    onRetry: () -> Boolean,
    onClose: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    val clipboard = LocalClipboardManager.current
    var copied by remember { mutableStateOf(false) }
    var diagLines by remember(machineId, state) { mutableStateOf(DirectDiag.lines()) }
    LaunchedEffect(machineId) {
        while (true) {
            diagLines = DirectDiag.lines()
            delay(1000)
        }
    }
    LaunchedEffect(copied) {
        if (copied) {
            delay(1500)
            copied = false
        }
    }
    val status = state.machines[machineId]?.pathState?.directStatus
    val verdict = when (status?.reason) {
        DirectFailReason.PEER_UNSUPPORTED -> copy(UiMessages.transportReasonPeerUnsupported())
        DirectFailReason.NO_RESPONSE -> copy(UiMessages.transportReasonNoResponse())
        DirectFailReason.NO_TRAVERSAL -> copy(UiMessages.transportReasonNoTraversal())
        DirectFailReason.FAILED -> copy(UiMessages.transportReasonFailed())
        DirectFailReason.AT_CAP -> copy(UiMessages.transportReasonAtCap())
        DirectFailReason.SECURITY_PENDING -> copy(UiMessages.transportReasonSecurityPending())
        DirectFailReason.STARTING -> copy(UiMessages.transportReasonStarting())
        null -> when {
            status?.readSlow == true || status?.fileSlow == true -> copy(UiMessages.transportDirectSlow())
            status?.stage == DirectStage.DIRECT_ACTIVE -> copy(UiMessages.transportStatusDirect())
            status?.stage == DirectStage.TRYING -> copy(UiMessages.transportStatusConnecting())
            status?.stage == DirectStage.CONNECTED_RELAY -> copy(UiMessages.transportReasonChannelPending())
            else -> copy(UiMessages.transportDiagVerdictNone())
        }
    }
    Sheet(onClose) {
        SheetHead(copy(UiMessages.transportDiag()), copy(UiMessages.commonDone()), onClose)
        Text(verdict, style = T.s15, color = p.ink, modifier = Modifier.padding(bottom = 12.dp))
        Column(
            Modifier.fillMaxWidth()
                .clip(Radii.R14)
                .background(if (LocalOnSoft.current) p.raised else p.gbg)
                .padding(horizontal = 14.dp, vertical = 10.dp),
            verticalArrangement = Arrangement.spacedBy(6.dp),
        ) {
            if (diagLines.isEmpty()) {
                Text(copy(UiMessages.transportDiagVerdictNone()), style = T.s13, color = p.ink2)
            } else {
                for (line in diagLines.asReversed()) {
                    Text(line, style = T.s12.digits(), color = p.ink2)
                }
            }
        }
        Spacer(Modifier.height(12.dp))
        Btns {
            PillButton(
                copy(if (copied) UiMessages.commonCopied() else UiMessages.transportDiagCopy()),
                onClick = {
                    val payload = buildString {
                        appendLine(verdict)
                        for (line in diagLines.asReversed()) appendLine(line)
                    }.trim()
                    clipboard.setText(AnnotatedString(payload))
                    copied = true
                },
                Modifier.weight(1f),
            )
            PillButton(
                copy(UiMessages.transportDiagRetry()),
                onClick = {
                    onRetry()
                    diagLines = DirectDiag.lines()
                },
                Modifier.weight(1f),
                enabled = status?.stage != DirectStage.DIRECT_ACTIVE && status?.stage != DirectStage.TRYING,
            )
        }
        Hint(copy(UiMessages.transportDiagFooter()))
    }
}

@Composable
private fun DirectStatusRow(
    stage: ConnectionGroupStage,
    tone: ConnectionGroupTone,
    word: String,
    detail: String,
    metric: String,
) {
    val p = pal
    val mark = when (stage) {
        ConnectionGroupStage.CONNECTION_GROUP_STAGE_DIRECT -> MarkKind.ONLINE
        ConnectionGroupStage.CONNECTION_GROUP_STAGE_CONNECTING,
        ConnectionGroupStage.CONNECTION_GROUP_STAGE_MEASURING -> MarkKind.RUNNING
        else -> MarkKind.OFF
    }
    val wordColor = when (tone) {
        ConnectionGroupTone.CONNECTION_GROUP_TONE_WARN -> p.orangeInk
        ConnectionGroupTone.CONNECTION_GROUP_TONE_LIVE -> p.ink
        else -> p.ink2
    }
    Column(
        Modifier.fillMaxWidth().heightIn(min = 50.dp).padding(start = 18.dp, end = 16.dp, top = 11.dp, bottom = 11.dp),
        verticalArrangement = Arrangement.Center,
    ) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            StatusMark(mark)
            Crossfade(targetState = stage to word, animationSpec = tween(200), modifier = Modifier.weight(1f)) { (_, w) ->
                Text(w, style = T.s17, color = wordColor)
            }
            if (metric.isNotEmpty()) {
                Text(metric, style = T.s13.digits(), color = p.ink2)
            }
        }
        if (detail.isNotEmpty()) {
            Text(detail, style = T.s13, color = p.ink2, modifier = Modifier.padding(start = 19.dp, top = 3.dp))
        }
    }
}

@Composable
private fun SpeedMetricRow(label: String, value: String) {
    val p = pal
    val stacked = LocalDensity.current.fontScale >= 1.5f
    val valueStyle = T.s17.digits()
    if (stacked) {
        Column(
            Modifier.fillMaxWidth().heightIn(min = 50.dp).padding(start = 18.dp, end = 16.dp, top = 10.dp, bottom = 10.dp),
            verticalArrangement = Arrangement.spacedBy(3.dp),
        ) {
            Text(label, style = T.s17, color = p.ink)
            Text(value, style = valueStyle, color = p.ink2)
        }
    } else {
        Row(
            Modifier.fillMaxWidth().heightIn(min = 50.dp).padding(start = 18.dp, end = 16.dp, top = 11.dp, bottom = 11.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            Text(label, style = T.s17, color = p.ink, modifier = Modifier.weight(1f))
            Text(value, style = valueStyle, color = p.ink2, textAlign = TextAlign.End, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
    }
}
