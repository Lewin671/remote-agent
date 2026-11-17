package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.CubicBezierEasing
import androidx.compose.animation.core.Spring
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.spring
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.proto.DiskPressureState
import io.github.lewin671.remoteagent.core.proto.MachineChartPoint
import io.github.lewin671.remoteagent.core.proto.MachineChartView
import io.github.lewin671.remoteagent.core.proto.MachinePressureTone
import io.github.lewin671.remoteagent.core.proto.MachineRingTone
import kotlin.math.abs
import kotlin.math.roundToLong

/*
 * Drawings and motion for 电脑状态 (machine-status.md + ui-spec): ring gauges, rolling digits,
 * pressure scale, disk stack, top-usage meter, and the 60fps Canvas history curve with touch scrubbing.
 */

internal const val SWAP_MS = 350

/** Stops continuous chart animation in tests where Espresso waits for main-thread frames to go idle. */
val LocalChartFrozen = androidx.compose.runtime.staticCompositionLocalOf { false }

fun Palette.ringToneInk(tone: MachineRingTone, normal: Color, text: Boolean = true): Color = when (tone) {
    MachineRingTone.MACHINE_RING_TONE_WARN -> if (text) orangeInk else orange
    MachineRingTone.MACHINE_RING_TONE_CRITICAL -> red
    else -> normal
}

fun Palette.pressureToneInk(tone: MachinePressureTone, normal: Color, text: Boolean = true): Color = when (tone) {
    MachinePressureTone.MACHINE_PRESSURE_TONE_HIGH -> if (text) orangeInk else orange
    MachinePressureTone.MACHINE_PRESSURE_TONE_CRITICAL -> red
    else -> normal
}

fun Palette.diskToneInk(state: DiskPressureState, normal: Color, text: Boolean = true): Color = when (state) {
    DiskPressureState.DISK_PRESSURE_STATE_LOW -> if (text) orangeInk else orange
    DiskPressureState.DISK_PRESSURE_STATE_CRITICAL -> red
    else -> normal
}

/** The raised card surface with its 1px light edge. */
fun Modifier.statusCard(p: Palette): Modifier {
    val edge = if (p.dark) p.faint else Color(0x0D000000)
    return this
        .clip(Radii.R22)
        .background(p.gbg)
        .border(0.5.dp, edge, Radii.R22)
}

/** Tabular numerals in TextStyle without pulling any font asset. */
fun TextStyle.digits(): TextStyle = copy(fontFeatureSettings = "tnum")

/**
 * Each character glides up/down on its own rail when [value] changes (machine-status.md
 * 「数字滚轮」). Falls back to plain text when [Motion.reduced] or [LocalChartFrozen] is active.
 */
@Composable
fun RollingText(
    value: String,
    number: Double,
    style: TextStyle,
    color: Color,
    modifier: Modifier = Modifier,
    fixedWidth: Boolean = true,
) {
    val static = Motion.reduced || LocalChartFrozen.current
    val styled = style.digits()
    if (static) {
        Text(value, style = styled, color = color, maxLines = 1, softWrap = false, modifier = modifier)
        return
    }
    var prev by remember { mutableStateOf(value to number) }
    val dir = if (number >= prev.second) 1 else -1
    if (value != prev.first) prev = value to number

    Row(modifier) {
        value.forEachIndexed { idx, ch ->
            val fromEnd = value.length - 1 - idx
            key(if (fixedWidth) idx else fromEnd) {
                if (ch in '0'..'9') RollingDigit(ch, dir, styled, color)
                else Text(ch.toString(), style = styled, color = color, maxLines = 1, softWrap = false)
            }
        }
    }
}

@Composable
private fun RollingDigit(target: Char, direction: Int, style: TextStyle, color: Color) {
    var shown by remember { mutableStateOf(target) }
    var prior by remember { mutableStateOf<Char?>(null) }
    val progress = remember { Animatable(1f) }
    LaunchedEffect(target) {
        if (target == shown) return@LaunchedEffect
        prior = shown
        shown = target
        progress.snapTo(0f)
        progress.animateTo(1f, tween(340, easing = CubicBezierEasing(0.22f, 1f, 0.36f, 1f)))
        prior = null
    }
    val t = progress.value
    Box(Modifier.clip(androidx.compose.ui.graphics.RectangleShape)) {
        // Reserve single-digit width from a stable zero.
        Text("0", style = style, color = Color.Transparent, maxLines = 1, softWrap = false)
        prior?.let { old ->
            val yFrac = if (direction >= 0) -t else t
            Text(
                old.toString(),
                style = style,
                color = color,
                maxLines = 1,
                softWrap = false,
                modifier = Modifier
                    .align(Alignment.Center)
                    .alpha(1f - t)
                    .let { m ->
                        m.offsetYFraction(yFrac * 0.8f)
                    },
            )
        }
        val enterFrac = if (prior == null) 0f else (if (direction >= 0) 1f - t else -(1f - t))
        Text(
            shown.toString(),
            style = style,
            color = color,
            maxLines = 1,
            softWrap = false,
            modifier = Modifier
                .align(Alignment.Center)
                .alpha(if (prior == null) 1f else t)
                .offsetYFraction(enterFrac * 0.8f),
        )
    }
}

private fun Modifier.offsetYFraction(fraction: Float): Modifier =
    if (fraction == 0f) this else graphicsLayer { translationY = size.height * fraction }

/** One circular ring gauge with an icon at the center. */
@Composable
fun StatusRing(
    fraction: Float?,
    tone: MachineRingTone,
    icon: androidx.compose.ui.graphics.vector.ImageVector,
    modifier: Modifier = Modifier,
) {
    val p = pal
    val accent = p.ringToneInk(tone, p.ink, text = false)
    val track = if (p.dark) Color(0x18FFFFFF) else Color(0x14000000)
    val animated by animateFloatAsState(
        targetValue = (fraction ?: 0f).coerceIn(0f, 1f),
        animationSpec = spring(stiffness = Spring.StiffnessMediumLow),
        label = "ring",
    )
    Box(modifier.size(34.dp), contentAlignment = Alignment.Center) {
        Canvas(Modifier.fillMaxSize()) {
            val stroke = 3.dp.toPx()
            val inset = stroke / 2f
            val arcSize = Size(size.width - stroke, size.height - stroke)
            drawArc(track, 0f, 360f, false, Offset(inset, inset), arcSize, style = Stroke(stroke, cap = StrokeCap.Round))
            if (fraction != null && animated > 0.005f) {
                drawArc(accent, -90f, 360f * animated, false, Offset(inset, inset), arcSize, style = Stroke(stroke, cap = StrokeCap.Round))
            }
        }
        Ic(icon, 14.dp, if (fraction == null) p.ink3 else accent)
    }
}

/** Smooth 3-zone gradient scale (blue -> orange -> red) with a floating thumb at [position]. */
@Composable
fun PressureScale(position: Float, modifier: Modifier = Modifier) {
    val p = pal
    val clamped = position.coerceIn(0f, 1f)
    val animated by animateFloatAsState(
        targetValue = clamped,
        animationSpec = spring(stiffness = Spring.StiffnessMediumLow),
        label = "pressure",
    )
    val blue = p.blue
    val orange = p.orange
    val red = p.red
    val paper = p.paper
    Canvas(modifier.fillMaxWidth().height(10.dp)) {
        val barH = 4.dp.toPx()
        val top = (size.height - barH) / 2f
        val r = barH / 2f
        drawRoundRect(
            brush = Brush.horizontalGradient(listOf(blue, orange, red)),
            topLeft = Offset(0f, top),
            size = Size(size.width, barH),
            cornerRadius = CornerRadius(r, r),
        )
        val cx = (size.width * animated).coerceIn(5.dp.toPx(), size.width - 5.dp.toPx())
        val cy = size.height / 2f
        drawCircle(paper, radius = 5.dp.toPx(), center = Offset(cx, cy))
        drawCircle(
            when {
                animated >= 0.72f -> red
                animated >= 0.45f -> orange
                else -> blue
            },
            radius = 3.5.dp.toPx(),
            center = Offset(cx, cy),
        )
    }
}

/** Disk fill bar fed directly from Core's [usedFraction] and [state]. */
@Composable
fun DiskBar(usedFraction: Float, state: DiskPressureState, modifier: Modifier = Modifier) {
    val p = pal
    val fill = usedFraction.coerceIn(0f, 1f)
    val color = p.diskToneInk(state, p.blue, text = false)
    val track = if (p.dark) Color(0x18FFFFFF) else Color(0x14000000)
    Row(
        modifier.fillMaxWidth().height(6.dp).clip(Radii.Pill).background(track),
    ) {
        if (fill > 0.005f) {
            Box(Modifier.fillMaxHeight().fillMaxWidth(fill).background(color))
        }
    }
}

/** Horizontal usage meter for Top Usage rows. */
@Composable
fun UsageMeter(fraction: Float, top: Boolean, dim: Boolean, secondary: String, modifier: Modifier = Modifier) {
    val p = pal
    val barColor = when {
        dim -> p.ink3
        top -> p.blue
        else -> p.ink2.copy(alpha = .55f)
    }
    val track = if (p.dark) Color(0x16FFFFFF) else Color(0x10000000)
    Row(modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.weight(1f).height(4.dp).clip(Radii.Pill).background(track)) {
            val w = fraction.coerceIn(0f, 1f)
            if (w > 0.005f) Box(Modifier.fillMaxHeight().fillMaxWidth(w).background(barColor))
        }
        if (secondary.isNotEmpty()) {
            Spacer(Modifier.width(8.dp))
            Text(secondary, style = T.s12.digits(), color = p.ink3, maxLines = 1)
        }
    }
}

/** Smooth Catmull-Rom / midpoint quadratic Bezier path across [pts]. */
fun smoothPath(pts: List<Offset>, into: Path = Path()): Path {
    into.reset()
    if (pts.isEmpty()) return into
    into.moveTo(pts.first().x, pts.first().y)
    if (pts.size == 1) return into
    if (pts.size == 2) {
        into.lineTo(pts[1].x, pts[1].y)
        return into
    }
    for (i in 0 until pts.size - 1) {
        val mid = Offset((pts[i].x + pts[i + 1].x) / 2f, (pts[i].y + pts[i + 1].y) / 2f)
        if (i == 0) into.lineTo(mid.x, mid.y)
        else into.quadraticTo(pts[i].x, pts[i].y, mid.x, mid.y)
    }
    into.lineTo(pts.last().x, pts.last().y)
    return into
}

internal fun swapOldAlpha(progress: Float): Float = (1f - (progress * SWAP_MS) / 100f).coerceIn(0f, 1f)
internal fun swapNewAlpha(progress: Float): Float = (((progress * SWAP_MS) - 230f) / 120f).coerceIn(0f, 1f)

internal data class ChartRun(val points: List<Offset>)

internal fun chartRuns(
    points: List<MachineChartPoint>,
    windowStartMs: Long,
    windowEndMs: Long,
    gapThresholdMs: Long,
    ceiling: Float,
    w: Float,
    h: Float,
): List<ChartRun> {
    val span = (windowEndMs - windowStartMs).coerceAtLeast(1L).toFloat()
    val cap = ceiling.coerceAtLeast(1f)
    val runs = ArrayList<ArrayList<Offset>>()
    var current = ArrayList<Offset>()
    var lastTime = Long.MIN_VALUE
    for (pt in points) {
        if (lastTime != Long.MIN_VALUE && gapThresholdMs > 0 && pt.timeMs - lastTime > gapThresholdMs) {
            if (current.isNotEmpty()) runs.add(current)
            current = ArrayList()
        }
        val x = ((pt.timeMs - windowStartMs).toFloat() / span).coerceIn(0f, 1f) * w
        val y = (1f - (pt.value.toFloat() / cap).coerceIn(0f, 1f)) * h
        current.add(Offset(x, y))
        lastTime = pt.timeMs
    }
    if (current.isNotEmpty()) runs.add(current)
    return runs.map { ChartRun(it) }
}

internal fun nearestSampleIndex(
    points: List<MachineChartPoint>,
    startMs: Long,
    endMs: Long,
    fracX: Float,
): Int {
    if (points.isEmpty()) return -1
    val targetMs = startMs + ((endMs - startMs).coerceAtLeast(1L) * fracX.coerceIn(0f, 1f)).roundToLong()
    var bestIdx = 0
    var bestDist = Long.MAX_VALUE
    for (i in points.indices) {
        val d = abs(points[i].timeMs - targetMs)
        if (d < bestDist) {
            bestDist = d
            bestIdx = i
        }
    }
    return bestIdx
}

/** Smooth Canvas history curve fed from Core's [MachineChartView], forwarding scrub & a11y steps. */
@Composable
fun StatusChart(
    chart: MachineChartView,
    accent: Color,
    onScrub: (pointIndex: Int, clear: Boolean) -> Unit,
    modifier: Modifier = Modifier,
) {
    val p = pal
    val points = chart.pointsList
    val currentOnScrub by rememberUpdatedState(onScrub)
    val currentPoints by rememberUpdatedState(points)
    val currentChart by rememberUpdatedState(chart)

    val frozen = LocalChartFrozen.current || Motion.reduced
    var animTick by remember { mutableLongStateOf(0L) }
    LaunchedEffect(frozen, points.size, chart.hasScrub) {
        if (frozen || chart.hasScrub || points.isEmpty()) return@LaunchedEffect
        var startNanos = 0L
        while (true) {
            withFrameNanos { nanos ->
                if (startNanos == 0L) startNanos = nanos
                animTick = (nanos - startNanos) / 16_000_000L
            }
        }
    }

    val hairline = if (p.dark) Color(0x1FFFFFFF) else Color(0x17000000)
    val grid = if (p.dark) Color(0x14FFFFFF) else Color(0x10000000)
    val paper = p.paper

    Box(
        modifier
            .fillMaxWidth()
            .height(92.dp)
            .pointerInput(chart.windowStartMs, chart.windowEndMs) {
                awaitEachGesture {
                    val down = awaitFirstDown(requireUnconsumed = false)
                    val w = size.width.toFloat().coerceAtLeast(1f)
                    val c = currentChart
                    val idx0 = nearestSampleIndex(currentPoints, c.windowStartMs, c.windowEndMs, down.position.x / w)
                    if (idx0 >= 0) currentOnScrub(idx0, false)
                    var lastIdx = idx0
                    do {
                        val event = awaitPointerEvent()
                        val change = event.changes.firstOrNull { it.id == down.id } ?: break
                        if (!change.pressed) break
                        val idx = nearestSampleIndex(currentPoints, c.windowStartMs, c.windowEndMs, change.position.x / w)
                        if (idx >= 0 && idx != lastIdx) {
                            lastIdx = idx
                            currentOnScrub(idx, false)
                        }
                    } while (true)
                    currentOnScrub(lastIdx.coerceAtLeast(0), true)
                }
            },
    ) {
        Canvas(Modifier.fillMaxSize()) {
            // 2px padding top/bottom so strokes aren't clipped.
            val padY = 4.dp.toPx()
            val h = (size.height - padY * 2f).coerceAtLeast(1f)
            val w = size.width
            // Reference dashed horizontal line at 50%.
            drawLine(
                color = grid,
                start = Offset(0f, padY + h * 0.5f),
                end = Offset(w, padY + h * 0.5f),
                strokeWidth = 1.dp.toPx(),
                pathEffect = PathEffect.dashPathEffect(floatArrayOf(4.dp.toPx(), 4.dp.toPx())),
            )
            // Keep animTick read so Compose re-draws smoothly when running live.
            @Suppress("UNUSED_VARIABLE")
            val frame = animTick

            if (points.isEmpty()) return@Canvas
            val ceiling = chart.ceiling.toFloat().takeIf { it > 0f } ?: 100f
            val runs = chartRuns(
                points = points,
                windowStartMs = chart.windowStartMs,
                windowEndMs = chart.windowEndMs,
                gapThresholdMs = chart.gapThresholdMs,
                ceiling = ceiling,
                w = w,
                h = h,
            )
            val strokePx = 2.dp.toPx()
            for (run in runs) {
                val shifted = run.points.map { Offset(it.x, it.y + padY) }
                if (shifted.size == 1) {
                    drawCircle(accent, radius = 3.dp.toPx(), center = shifted[0])
                } else {
                    val line = smoothPath(shifted)
                    val fill = Path().apply {
                        addPath(line)
                        lineTo(shifted.last().x, padY + h)
                        lineTo(shifted.first().x, padY + h)
                        close()
                    }
                    drawPath(
                        path = fill,
                        brush = Brush.verticalGradient(
                            colors = listOf(accent.copy(alpha = 0.22f), accent.copy(alpha = 0.0f)),
                            startY = padY,
                            endY = padY + h,
                        ),
                    )
                    drawPath(line, accent, style = Stroke(strokePx, cap = StrokeCap.Round))
                }
            }

            if (chart.hasScrub && chart.scrubIndex in points.indices) {
                val pt = points[chart.scrubIndex]
                val span = (chart.windowEndMs - chart.windowStartMs).coerceAtLeast(1L).toFloat()
                val sx = ((pt.timeMs - chart.windowStartMs).toFloat() / span).coerceIn(0f, 1f) * w
                val sy = (1f - (pt.value.toFloat() / ceiling).coerceIn(0f, 1f)) * h + padY
                drawLine(hairline, Offset(sx, 0f), Offset(sx, size.height), strokeWidth = 1.dp.toPx())
                drawCircle(paper, radius = 5.dp.toPx(), center = Offset(sx, sy))
                drawCircle(accent, radius = 3.5.dp.toPx(), center = Offset(sx, sy))
            }
        }
    }
}
