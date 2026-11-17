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
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupStage
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupTone
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupView

/**
 * Renders Core's 「连接」 group ([ConnectionGroupView]) directly inside [DeviceScreen].
 */
@Composable
internal fun DirectConnectionGroup(
    view: ConnectionGroupView,
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
    }
    Hint(footerText, Modifier.padding(bottom = 12.dp))
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
