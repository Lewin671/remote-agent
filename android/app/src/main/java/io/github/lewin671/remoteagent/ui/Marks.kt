package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.Status
import io.github.lewin671.remoteagent.core.Tone
import kotlinx.coroutines.delay

/**
 * The status graphics (design-system §2 状态图形): one in the first column
 * of a row, always beside a status word that says the same. They are drawn,
 * not icons.
 */
enum class MarkKind {
    /** 运行中: an ink ring open on one side, turning. */
    RUNNING,
    /** 空闲: a hollow grey ring. */
    IDLE,
    /** 等你: an orange dot. */
    WAITING,
    /** 未连接, 电脑离线: a faint dot. */
    OFF,
    /** 未读结果: a blue dot. */
    UNREAD,
    /** 在线: a green dot. */
    ONLINE,
}

/** The graphic of a session's status; an ended one has none (its date group says it). */
fun markOf(status: Status): MarkKind? = when {
    status == Status.EXITED -> null
    else -> when (status.tone) {
        Tone.RUN -> MarkKind.RUNNING
        Tone.IDLE -> MarkKind.IDLE
        Tone.WAIT -> MarkKind.WAITING
        Tone.OFF -> MarkKind.OFF
    }
}

/**
 * A status graphic at its size in a list (dot 9, ring 11, spinner 14);
 * [size] scales all three for smaller places such as a status line, and
 * [color] replaces their colour, as on the dark terminal.
 * The spinner turns once a second, and stands still when the system's
 * animations are off.
 */
@Composable
fun StatusMark(kind: MarkKind, modifier: Modifier = Modifier, size: Dp = 9.dp, color: Color? = null) {
    val p = pal
    when (kind) {
        MarkKind.RUNNING -> Spinner(color ?: p.ink, modifier, size * 14 / 9)
        MarkKind.IDLE -> Box(modifier.size(size * 11 / 9).border(1.5.dp, color ?: p.ink3, CircleShape))
        else -> Box(
            modifier.size(size).clip(CircleShape).background(
                color ?: when (kind) {
                    MarkKind.WAITING -> p.orange
                    MarkKind.UNREAD -> p.blue
                    MarkKind.ONLINE -> p.green
                    else -> p.faint
                },
            ),
        )
    }
}

/** The turning half-open ring of 运行中 and of work in progress. */
@Composable
fun Spinner(color: Color, modifier: Modifier = Modifier, size: Dp = 14.dp, stroke: Dp = 2.dp) {
    val transition = rememberInfiniteTransition(label = "spinner")
    val angle by transition.animateFloat(0f, 360f, infiniteRepeatable(tween(1000, easing = LinearEasing)), label = "rotation")
    Canvas(modifier.size(size).rotate(if (Motion.reduced) 0f else angle)) {
        val w = stroke.toPx()
        drawArc(color, -90f, 270f, useCenter = false, topLeft = androidx.compose.ui.geometry.Offset(w / 2, w / 2),
            size = androidx.compose.ui.geometry.Size(this.size.width - w, this.size.height - w), style = Stroke(w))
    }
}

/** Monochrome `>_` badge drawn on gbg with a 0.5dp sep rim (client-v63 #appmark). */
@Composable
fun AppMarkBadge(size: Dp = 56.dp, corner: Dp = 14.dp, modifier: Modifier = Modifier) {
    val p = pal
    val iconSize = if (size <= 48.dp) 24.dp else 26.dp
    val shape = RoundedCornerShape(corner)
    Box(
        modifier
            .size(size)
            .clip(shape)
            .background(p.gbg)
            .border(0.5.dp, p.sep, shape)
            .clearAndSetSemantics {},
        contentAlignment = Alignment.Center,
    ) {
        Canvas(Modifier.size(iconSize)) {
            val s = this.size.minDimension / 24f
            val stroke = Stroke(width = 2.3f * s, cap = StrokeCap.Round, join = StrokeJoin.Round)
            val chevron = Path().apply {
                moveTo(6.2f * s, 7.2f * s)
                lineTo(11.6f * s, 12f * s)
                lineTo(6.2f * s, 16.8f * s)
            }
            drawPath(chevron, color = p.ink, style = stroke)
            val cursor = Path().apply {
                moveTo(13.2f * s, 16.8f * s)
                lineTo(18.0f * s, 16.8f * s)
            }
            drawPath(cursor, color = p.ink, style = stroke)
        }
    }
}

/** Centred AppMark startup placeholder; [status] fades in after 300ms so fast cold starts stay quiet. */
@Composable
fun StartupShell(status: String?, modifier: Modifier = Modifier) {
    val p = pal
    var showStatus by remember { mutableStateOf(false) }
    LaunchedEffect(status) {
        if (status.isNullOrEmpty()) {
            showStatus = false
        } else {
            delay(300)
            showStatus = true
        }
    }
    val statusAlpha by animateFloatAsState(
        targetValue = if (showStatus) 1f else 0f,
        animationSpec = tween(durationMillis = if (Motion.reduced) 0 else 120),
        label = "startupStatus",
    )
    Column(
        modifier.fillMaxSize().background(p.paper).padding(24.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.Center,
    ) {
        AppMarkBadge(size = 48.dp, corner = 12.dp)
        if (!status.isNullOrEmpty()) {
            Spacer(Modifier.height(14.dp))
            Text(status, style = T.s13, color = p.ink2, modifier = Modifier.alpha(statusAlpha))
        }
    }
}
