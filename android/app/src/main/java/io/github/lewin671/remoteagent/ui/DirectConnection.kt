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
import androidx.compose.runtime.key
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

enum class DirectKind { DIRECT, MEASURING, RELAY, TRYING, OFF, UNSUPPORTED }

/** The direct path in words (design-system 「连接」组), one place so the computer's
 * settings page and the connection check never disagree: the settings page
 * draws all of it, the connection check one line of it. */
internal data class DirectView(
    val kind: DirectKind,
    val mark: MarkKind,
    /** The state word; ink2, never colored. */
    val word: String,
    /** Second line of the status row: which single purpose goes direct, or why the relay is in use. */
    val sub: String? = null,
    /** The one line the connection check puts under a computer's name. */
    val summary: String? = null,
    /** Measured speeds only, one per purpose. */
    val speeds: List<Pair<String, String>> = emptyList(),
)

// One decimal below 100, whole numbers from there; 99.96 is already 100.
internal fun formatMibps(n: Double): String =
    if (Math.round(n * 10) / 10.0 >= 100) Math.round(n).toString() else String.format(Locale.US, "%.1f", n)

private fun directReasonText(reason: DirectFailReason?, copy: MessageResolver): String? = when (reason) {
    DirectFailReason.PEER_UNSUPPORTED -> copy(UiMessages.transportReasonPeerUnsupported())
    DirectFailReason.NO_RESPONSE -> copy(UiMessages.transportReasonNoResponse())
    DirectFailReason.NO_TRAVERSAL -> copy(UiMessages.transportReasonNoTraversal())
    DirectFailReason.FAILED -> copy(UiMessages.transportReasonFailed())
    null -> null
}

private fun directSpeeds(s: DirectStatus, copy: MessageResolver): List<Pair<String, String>> {
    val out = ArrayList<Pair<String, String>>()
    val fd = s.bulkDirectMibPerSec
    val fr = s.bulkRelayMibPerSec
    val name = copy(UiMessages.transportSpeedFile())
    when {
        s.stage != DirectStage.OFF && s.stage != DirectStage.UNCONNECTED && s.stage != DirectStage.TRYING && fd == null && fr == null ->
            out.add(name to copy(UiMessages.transportFileSpeedPending()))
        fd != null && fr != null -> out.add(name to copy(UiMessages.transportFileSpeedBoth(direct = formatMibps(fd), relay = formatMibps(fr))))
        fd != null -> out.add(name to copy(UiMessages.transportFileSpeedDirect(direct = formatMibps(fd))))
        fr != null -> out.add(name to copy(UiMessages.transportFileSpeedRelay(relay = formatMibps(fr))))
    }
    val rd = s.readDirectMs
    val rr = s.readRelayMs
    val readName = copy(UiMessages.transportSpeedRead())
    when {
        rd != null && rr != null -> out.add(readName to copy(UiMessages.transportReadLatencyBoth(direct = Math.round(rd).toString(), relay = Math.round(rr).toString())))
        rd != null -> out.add(readName to copy(UiMessages.transportReadLatencyDirect(direct = Math.round(rd).toString())))
        rr != null -> out.add(readName to copy(UiMessages.transportReadLatencyRelay(relay = Math.round(rr).toString())))
    }
    return out
}

private fun line(word: String, sub: String?, copy: MessageResolver) =
    if (sub != null) "$word · $sub" else word

/** What a computer's direct path looks like to the user right now. */
internal fun directView(s: DirectStatus, copy: MessageResolver): DirectView {
    if (!s.supported) return DirectView(DirectKind.UNSUPPORTED, MarkKind.OFF, copy(UiMessages.transportUnsupportedDevice()))
    if (!s.enabled || s.stage == DirectStage.OFF) {
        return DirectView(DirectKind.OFF, MarkKind.OFF, "", summary = copy(UiMessages.transportSummaryOff()))
    }
    val shown = directSpeeds(s, copy)
    return when (s.stage) {
        DirectStage.DIRECT_ACTIVE -> {
            val word = copy(UiMessages.transportStatusDirect())
            // Both purposes direct say nothing more; one says which.
            val sub = when {
                s.fileDirect && s.readDirect -> null
                s.fileDirect -> copy(UiMessages.transportUseFile())
                else -> copy(UiMessages.transportUseRead())
            }
            DirectView(DirectKind.DIRECT, MarkKind.ONLINE, word, sub, line(word, sub, copy), shown)
        }
        DirectStage.MEASURING -> {
            val word = copy(UiMessages.transportStatusMeasuring())
            DirectView(DirectKind.MEASURING, MarkKind.RUNNING, word, null, word, shown)
        }
        DirectStage.CONNECTED_RELAY -> {
            val word = copy(UiMessages.transportStatusRelay())
            val sub = copy(if (s.relayNotSlower) UiMessages.transportRelayFaster() else UiMessages.transportNotClearlyFaster())
            DirectView(DirectKind.RELAY, MarkKind.OFF, word, sub, line(word, sub, copy), shown)
        }
        DirectStage.TRYING -> directTrying(copy).copy(speeds = shown)
        else -> {
            val word = copy(UiMessages.transportStatusRelay())
            val why = directReasonText(s.reason, copy)
            val sub = why?.let { copy(UiMessages.transportUnavailable(reason = it)) }
            DirectView(DirectKind.RELAY, MarkKind.OFF, word, sub, line(word, sub, copy), shown)
        }
    }
}

/** A direct path just turned on that has reported nothing yet: trying, which is what is happening. */
internal fun directTrying(copy: MessageResolver): DirectView {
    val word = copy(UiMessages.transportStatusConnecting())
    return DirectView(DirectKind.TRYING, MarkKind.RUNNING, word, null, word)
}

/** The connection check's note under its list: the old explanation of a
 * latency, then (once, not on every computer's line) that this device cannot do it. */
internal fun connFooter(statuses: List<DirectStatus>, locale: String, copy: MessageResolver): String {
    val sentences = ArrayList<String>()
    sentences.add(copy(UiMessages.connectionLatencyIsOneRoundTripTheServerRowMeasures()))
    if (statuses.any { !it.supported }) sentences.add(copy(UiMessages.transportConnFooterUnsupportedDevice()))
    // Chinese sentences end in a full stop and need no space between them.
    return sentences.joinToString(if (locale.startsWith("zh")) "" else " ")
}

/** How long a direct path just turned on may say "trying" before it reports
 * anything: a round of attempts is bounded at about this long. */
internal const val DIRECT_TRYING_MS = 10_000L

@Composable
internal fun DirectConnectionGroup(
    status: DirectStatus,
    online: Boolean,
    /** When the switch was last turned on (ms, monotonic); shows "trying" until the engine reports. */
    turnedOnAt: Long?,
    nowMs: Long,
    onToggle: (Boolean) -> Unit,
) {
    val copy = uiText()
    val p = pal
    var view = directView(status, copy)
    if (turnedOnAt != null && nowMs - turnedOnAt < DIRECT_TRYING_MS && status.supported && status.enabled &&
        status.stage == DirectStage.UNCONNECTED && status.reason == null
    ) {
        view = directTrying(copy)
    }
    val supported = view.kind != DirectKind.UNSUPPORTED
    val on = supported && view.kind != DirectKind.OFF
    val showStatus = !supported || (on && online)
    val speeds = if (on && online) view.speeds else emptyList()

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
                value = on,
                enabled = supported,
                onChange = onToggle,
            )
        }
        AnimatedVisibility(visible = showStatus, enter = enter, exit = exit) {
            Box(Modifier.topLine(p.sep, 18.dp)) {
                DirectStatusRow(view)
            }
        }
        for ((index, speed) in speeds.withIndex()) {
            key(index) {
                Box(Modifier.topLine(p.sep, 18.dp)) { SpeedMetricRow(speed.first, speed.second) }
            }
        }
    }
    Hint(copy(UiMessages.transportFooter()), Modifier.padding(bottom = 12.dp))
}

@Composable
private fun DirectStatusRow(view: DirectView) {
    val p = pal
    Column(
        Modifier.fillMaxWidth().heightIn(min = 50.dp).padding(start = 18.dp, end = 16.dp, top = 11.dp, bottom = 11.dp),
        verticalArrangement = Arrangement.Center,
    ) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            StatusMark(view.mark)
            Crossfade(targetState = view.kind to view.word, animationSpec = tween(200), modifier = Modifier.weight(1f)) { (_, w) ->
                Text(w, style = T.s17, color = p.ink2)
            }
        }
        if (view.sub != null) {
            Text(view.sub, style = T.s13, color = p.ink2, modifier = Modifier.padding(start = 19.dp, top = 3.dp))
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
