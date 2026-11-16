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
import io.github.lewin671.remoteagent.core.MessageResolver
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.transport.DirectFailReason
import io.github.lewin671.remoteagent.core.transport.DirectStage
import io.github.lewin671.remoteagent.core.transport.DirectStatus
import java.util.Locale
import kotlin.math.roundToLong

internal fun directUseText(status: DirectStatus, copy: MessageResolver): String = when {
    status.fileDirect && status.readDirect -> copy(UiMessages.transportUseFilesAndReads())
    status.fileDirect -> copy(UiMessages.transportUseFiles())
    else -> copy(UiMessages.transportUseReads())
}

internal fun directReasonText(reason: DirectFailReason?, copy: MessageResolver): String? = when (reason) {
    DirectFailReason.PEER_UNSUPPORTED -> copy(UiMessages.transportReasonPeerUnsupported())
    DirectFailReason.NO_RESPONSE -> copy(UiMessages.transportReasonNoResponse())
    DirectFailReason.NO_TRAVERSAL -> copy(UiMessages.transportReasonNoTraversal())
    DirectFailReason.FAILED -> copy(UiMessages.transportReasonFailed())
    null -> null
}

/** One-line plain summary for computer rows in Connection Check (no status mark). */
internal fun directSummary(status: DirectStatus, copy: MessageResolver): String = when {
    !status.supported -> copy(UiMessages.transportUnsupportedDevice())
    !status.enabled -> copy(UiMessages.transportSummaryOff())
    else -> when (status.stage) {
        DirectStage.OFF -> copy(UiMessages.transportSummaryOff())
        DirectStage.DIRECT_ACTIVE -> copy(
            UiMessages.transportSummaryWithDetail(
                status = copy(UiMessages.transportStatusDirect()),
                detail = directUseText(status, copy),
            ),
        )
        DirectStage.CONNECTED_RELAY -> copy(UiMessages.transportStatusConnectedRelay())
        DirectStage.TRYING -> copy(UiMessages.transportStatusTrying())
        DirectStage.UNCONNECTED -> {
            val base = copy(UiMessages.transportStatusUnconnected())
            val reason = directReasonText(status.reason, copy)
            if (reason != null) copy(UiMessages.transportSummaryWithDetail(status = base, detail = reason)) else base
        }
    }
}

internal fun formatBulkSpeed(directMib: Double?, relayMib: Double?, copy: MessageResolver): String? = when {
    directMib != null && relayMib != null -> copy(
        UiMessages.transportSpeedPairMib(
            direct = String.format(Locale.US, "%.1f", directMib),
            relay = String.format(Locale.US, "%.1f", relayMib),
        ),
    )
    directMib != null -> copy(UiMessages.transportSpeedDirectMib(String.format(Locale.US, "%.1f", directMib)))
    relayMib != null -> copy(UiMessages.transportSpeedRelayMib(String.format(Locale.US, "%.1f", relayMib)))
    else -> null
}

internal fun formatReadLatency(directMs: Double?, relayMs: Double?, copy: MessageResolver): String? = when {
    directMs != null && relayMs != null -> copy(
        UiMessages.transportSpeedPairMs(
            direct = directMs.roundToLong().toString(),
            relay = relayMs.roundToLong().toString(),
        ),
    )
    directMs != null -> copy(UiMessages.transportSpeedDirectMs(directMs.roundToLong().toString()))
    relayMs != null -> copy(UiMessages.transportSpeedRelayMs(relayMs.roundToLong().toString()))
    else -> null
}

@Composable
internal fun DirectConnectionGroup(
    status: DirectStatus,
    online: Boolean,
    onToggle: (Boolean) -> Unit,
) {
    val copy = uiText()
    val p = pal
    val bulkText = if (status.supported && status.enabled && online) {
        formatBulkSpeed(status.bulkDirectMibPerSec, status.bulkRelayMibPerSec, copy)
    } else null
    val readText = if (status.supported && status.enabled && online) {
        formatReadLatency(status.readDirectMs, status.readRelayMs, copy)
    } else null
    val hasSpeeds = bulkText != null || readText != null

    val enter = if (Motion.reduced) fadeIn(tween(150)) else expandVertically(tween(200)) + fadeIn(tween(200))
    val exit = if (Motion.reduced) fadeOut(tween(150)) else shrinkVertically(tween(200)) + fadeOut(tween(200))

    GLabel(copy(UiMessages.transportConnectionGroup()))
    Column(
        Modifier.fillMaxWidth()
            .padding(start = 16.dp, end = 16.dp, bottom = 6.dp)
            .clip(Radii.R20)
            .background(if (LocalOnSoft.current) p.raised else p.gbg),
    ) {
        Box(Modifier.alpha(if (status.supported) 1f else 0.4f)) {
            ToggleRow(
                copy(UiMessages.transportDirect()),
                "",
                value = if (status.supported) status.enabled else false,
                enabled = status.supported,
                onChange = onToggle,
            )
        }
        val showStatus = !status.supported || (status.enabled && online)
        AnimatedVisibility(visible = showStatus, enter = enter, exit = exit) {
            Box(Modifier.topLine(p.sep, 18.dp)) {
                DirectStatusRow(status, copy)
            }
        }
        AnimatedVisibility(visible = bulkText != null, enter = enter, exit = exit) {
            Box(Modifier.topLine(p.sep, 18.dp)) {
                if (bulkText != null) SpeedMetricRow(copy(UiMessages.transportSpeedFileTransfer()), bulkText)
            }
        }
        AnimatedVisibility(visible = readText != null, enter = enter, exit = exit) {
            Box(Modifier.topLine(p.sep, 18.dp)) {
                if (readText != null) SpeedMetricRow(copy(UiMessages.transportSpeedReadLatency()), readText)
            }
        }
    }
    Hint(
        copy(if (hasSpeeds) UiMessages.transportGroupNoteSpeeds() else UiMessages.transportGroupNote()),
        Modifier.padding(bottom = 12.dp),
    )
}

@Composable
private fun DirectStatusRow(status: DirectStatus, copy: MessageResolver) {
    val p = pal
    if (!status.supported) {
        Row(
            Modifier.fillMaxWidth().heightIn(min = 50.dp).padding(start = 18.dp, end = 16.dp, top = 10.dp, bottom = 10.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            StatusMark(MarkKind.OFF)
            Text(copy(UiMessages.transportUnsupportedDevice()), style = T.s17, color = p.ink2)
        }
        return
    }
    val mark = when (status.stage) {
        DirectStage.DIRECT_ACTIVE -> MarkKind.ONLINE
        DirectStage.CONNECTED_RELAY -> MarkKind.IDLE
        DirectStage.TRYING -> MarkKind.RUNNING
        DirectStage.UNCONNECTED, DirectStage.OFF -> MarkKind.OFF
    }
    val word = when (status.stage) {
        DirectStage.DIRECT_ACTIVE -> copy(UiMessages.transportStatusDirect())
        DirectStage.CONNECTED_RELAY -> copy(UiMessages.transportStatusConnectedRelay())
        DirectStage.TRYING -> copy(UiMessages.transportStatusTrying())
        DirectStage.UNCONNECTED, DirectStage.OFF -> copy(UiMessages.transportStatusUnconnected())
    }
    val rightValue = if (status.stage == DirectStage.DIRECT_ACTIVE) directUseText(status, copy) else null
    val subLine = when (status.stage) {
        DirectStage.CONNECTED_RELAY -> copy(
            if (status.benchmarkDecidedRelay) UiMessages.transportSpeedRelayFaster()
            else UiMessages.transportSpeedComparing(),
        )
        DirectStage.UNCONNECTED -> directReasonText(status.reason, copy)
        else -> null
    }
    Column(
        Modifier.fillMaxWidth().heightIn(min = 50.dp).padding(start = 18.dp, end = 16.dp, top = 11.dp, bottom = 11.dp),
        verticalArrangement = Arrangement.Center,
    ) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            StatusMark(mark)
            Crossfade(targetState = word, animationSpec = tween(200), modifier = Modifier.weight(1f)) { w ->
                Text(w, style = T.s17, color = p.ink2)
            }
            if (rightValue != null) {
                Text(rightValue, style = T.s17, color = p.ink2, textAlign = TextAlign.End)
            }
        }
        if (subLine != null) {
            Text(subLine, style = T.s13, color = p.ink2, modifier = Modifier.padding(start = 19.dp, top = 3.dp))
        }
    }
}

@Composable
private fun SpeedMetricRow(label: String, value: String) {
    val p = pal
    val stacked = LocalDensity.current.fontScale >= 1.5f
    val valueStyle = T.s17.copy(fontFeatureSettings = "tnum")
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
