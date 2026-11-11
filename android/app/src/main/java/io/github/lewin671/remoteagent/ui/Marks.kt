package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.Status
import io.github.lewin671.remoteagent.core.Tone

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
