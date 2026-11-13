package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.AnimationVector2D
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.animation.core.VectorConverter
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.slideOutVertically
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.requiredSize
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.composed
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.onPlaced
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.layout.positionInParent
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.round
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.CHART_WINDOW_MS
import io.github.lewin671.remoteagent.core.ChartPoint
import io.github.lewin671.remoteagent.core.MachineTone
import io.github.lewin671.remoteagent.core.chartRuns
import io.github.lewin671.remoteagent.core.nearestSample
import io.github.lewin671.remoteagent.core.valueAt
import kotlinx.coroutines.launch
import kotlin.math.max
import kotlin.math.min
import kotlin.math.sqrt

/*
 * The drawing of 电脑状态 (ui-spec A; 界面稿 v2 第 ② 部分): the surfaces of its cards, the rolling digits,
 * a ring, the memory pressure scale, the disk bar, the chart and the row that slides to a new place.
 */

/** The page behind the cards: grey in the light theme, black in the dark one (界面稿: `--gbg`). */
val Palette.statusPage: Color get() = if (dark) paper else gbg

/** The card's own surface, `lift`: white in the light theme, the dark card grey in the dark one. */
val Palette.lift: Color get() = if (dark) gbg else paper

/** Colour of a figure that needs attention: red, or orange (as text, its readable ink). */
fun Palette.toneInk(tone: MachineTone, normal: Color, text: Boolean = true): Color = when (tone) {
    MachineTone.NORMAL -> normal
    MachineTone.WARN -> if (text) orangeInk else orange
    MachineTone.CRITICAL -> red
}

/** A card of the page: radius 22, `lift`, and in the light theme the draft's soft shadow. */
fun Modifier.statusCard(p: Palette): Modifier {
    val shape = Radii.R22
    return (if (p.dark) this else this.shadow(6.dp, shape, clip = false, ambientColor = Color(0x0A000000), spotColor = Color(0x1A000000)))
        .clip(shape).background(p.lift)
}

private val tabular = "tnum"

/** A figure's style with fixed-width digits so that rolling digits do not jitter. */
fun TextStyle.digits(): TextStyle = copy(fontFeatureSettings = tabular)

/**
 * A number whose digits roll (ui-spec A4): only the places that changed move, up when the number grew
 * and down when it fell, 200 ms. With the system's animations reduced it is just text.
 */
@Composable
fun RollingText(value: String, number: Double, style: TextStyle, color: Color, modifier: Modifier = Modifier, fixedWidth: Boolean = true) {
    val shape = if (fixedWidth) style.digits() else style
    if (Motion.reduced) {
        Text(value, modifier, style = shape, color = color, maxLines = 1)
        return
    }
    val last = remember { doubleArrayOf(number) }
    val up = number >= last[0]
    SideEffect { last[0] = number }
    Row(modifier.clipToBounds()) {
        value.forEachIndexed { i, ch ->
            androidx.compose.runtime.key(value.length - i) {
                AnimatedContent(
                    targetState = ch,
                    transitionSpec = {
                        val d = if (up) 1 else -1
                        (slideInVertically(tween(Motion.BASE, easing = Motion.Decelerate)) { d * (it * 0.6f).toInt() } + fadeIn(tween(Motion.BASE)))
                            .togetherWith(slideOutVertically(tween(Motion.BASE, easing = Motion.Decelerate)) { -d * (it * 0.6f).toInt() } + fadeOut(tween(Motion.FAST)))
                    },
                ) { c -> Text(c.toString(), style = shape, color = color, maxLines = 1, softWrap = false) }
            }
        }
    }
}

/** A row that glides to its new place when the list around it reorders (300 ms); snaps with reduced motion. */
fun Modifier.animatePlacement(ms: Int = 300): Modifier = composed {
    val scope = rememberCoroutineScope()
    var target by remember { mutableStateOf(IntOffset.Zero) }
    var anim by remember { mutableStateOf<Animatable<IntOffset, AnimationVector2D>?>(null) }
    this.onPlaced { target = it.positionInParent().round() }.offset {
        val a = anim ?: Animatable(target, IntOffset.VectorConverter).also { anim = it }
        if (a.targetValue != target) scope.launch { if (Motion.reduced) a.snapTo(target) else a.animateTo(target, tween(ms, easing = Motion.Decelerate)) }
        a.value - target
    }
}

/** A progress ring (ui-spec A2): 54 across, line 5.5, round caps, from twelve o'clock; the icon in its middle. */
@Composable
fun StatusRing(fraction: Float?, tone: MachineTone, icon: androidx.compose.ui.graphics.vector.ImageVector, modifier: Modifier = Modifier) {
    val p = pal
    val shown by animateFloatAsState(fraction ?: 0f, if (Motion.reduced) androidx.compose.animation.core.snap() else tween(400, easing = Motion.Decelerate))
    val color = p.toneInk(tone, p.ink, text = false)
    Box(modifier.size(54.dp), contentAlignment = Alignment.Center) {
        Canvas(Modifier.fillMaxSize()) {
            val w = 5.5.dp.toPx()
            val inset = w / 2
            val arc = Size(size.width - w, size.height - w)
            drawArc(p.fill2, 0f, 360f, false, Offset(inset, inset), arc, style = Stroke(w))
            if (fraction != null && shown > 0.002f) drawArc(color, -90f, 360f * shown, false, Offset(inset, inset), arc, style = Stroke(w, cap = StrokeCap.Round))
        }
        Ic(icon, 18.dp, p.toneInk(tone, p.ink2, text = false))
    }
}

/** The pressure scale (ui-spec A3 ②): 6 high, green to yellow to red, with the dot at the current pressure. */
@Composable
fun PressureScale(position: Float, modifier: Modifier = Modifier) {
    val p = pal
    val yellow = if (p.dark) Color(0xFFFFD60A) else Color(0xFFF5B400)
    androidx.compose.foundation.layout.BoxWithConstraints(modifier.fillMaxWidth().height(12.dp)) {
        Box(
            Modifier.align(Alignment.CenterStart).fillMaxWidth().height(6.dp).clip(RoundedCornerShapeDp(3.dp)).background(
                Brush.horizontalGradient(0f to p.green, .45f to p.green, .7f to yellow, 1f to p.red),
            ),
        )
        val x = (maxWidth - 12.dp) * position.coerceIn(0f, 1f)
        // 12 across in ink, ringed by 3 of the card's own colour.
        Box(Modifier.align(Alignment.CenterStart).offset(x = x - 3.dp).requiredSize(18.dp).background(p.lift, CircleShape).padding(3.dp).background(p.ink, CircleShape))
    }
}

private fun RoundedCornerShapeDp(r: Dp) = androidx.compose.foundation.shape.RoundedCornerShape(r)

/** The disk bar (ui-spec A3 ②): remote-agent's share in ink, the rest used in `faint`, the free room as the groove. */
@Composable
fun DiskBar(agentShare: Float, otherShare: Float, tone: MachineTone, modifier: Modifier = Modifier) {
    val p = pal
    val red = tone == MachineTone.CRITICAL
    val a = if (red) p.red else p.ink
    val b = if (red) p.red else p.faint
    val agent = agentShare.coerceIn(0f, 1f)
    val other = otherShare.coerceIn(0f, 1f - agent)
    val rest = (1f - agent - other).coerceAtLeast(0f)
    Row(modifier.fillMaxWidth().height(10.dp).clip(RoundedCornerShapeDp(5.dp)).background(p.fill2), horizontalArrangement = Arrangement.spacedBy(1.5.dp)) {
        if (agent > 0.0005f) Box(Modifier.weight(agent).fillMaxSize().background(a))
        if (other > 0.0005f) Box(Modifier.weight(other).fillMaxSize().background(b))
        if (rest > 0.0005f) Box(Modifier.weight(rest).fillMaxSize())
    }
}

/** The bar of a ranking row and the words that follow it: the bar's length is its share of the container, never crowding the words out. */
@Composable
fun UsageMeter(fraction: Float, top: Boolean, dim: Boolean, secondary: String, modifier: Modifier = Modifier) {
    val p = pal
    val shown by animateFloatAsState(fraction, if (Motion.reduced) androidx.compose.animation.core.snap() else tween(300, easing = Motion.Decelerate))
    val color = if (dim || !top) p.ink3 else p.ink
    Layout(
        modifier = modifier.fillMaxWidth(),
        content = {
            Box(Modifier.height(5.dp).clip(RoundedCornerShapeDp(2.5.dp)).background(color))
            Text(secondary, style = ts(12.5.sp).digits(), color = if (dim) p.ink3 else p.ink2, maxLines = 1)
        },
    ) { measurables, constraints ->
        val gap = 10.dp.roundToPx()
        val text = measurables[1].measure(constraints.copy(minWidth = 0, maxWidth = max(0, constraints.maxWidth - gap - 5.dp.roundToPx())))
        val full = constraints.maxWidth
        val wanted = (shown * full).toInt().coerceAtLeast(5.dp.roundToPx())
        val barW = min(wanted, full - text.width - gap).coerceAtLeast(5.dp.roundToPx())
        val bar = measurables[0].measure(androidx.compose.ui.unit.Constraints.fixed(barW, 5.dp.roundToPx()))
        val h = max(bar.height, text.height)
        layout(full, h) {
            bar.placeRelative(0, (h - bar.height) / 2)
            text.placeRelative(barW + gap, (h - text.height) / 2)
        }
    }
}

/** True while the frame clock of the chart must stand still (tests and screenshots draw a fixed frame). */
val LocalChartFrozen = androidx.compose.runtime.staticCompositionLocalOf { false }

/** The smooth curve through the points, without overshoot (a monotone cubic). */
fun smoothPath(pts: List<Offset>, into: Path = Path()): Path {
    if (pts.isEmpty()) return into
    into.moveTo(pts[0].x, pts[0].y)
    if (pts.size == 1) return into
    val n = pts.size
    val dx = FloatArray(n - 1) { pts[it + 1].x - pts[it].x }
    val d = FloatArray(n - 1) { if (dx[it] == 0f) 0f else (pts[it + 1].y - pts[it].y) / dx[it] }
    val m = FloatArray(n)
    m[0] = d[0]; m[n - 1] = d[n - 2]
    for (i in 1 until n - 1) m[i] = if (d[i - 1] * d[i] <= 0f) 0f else (d[i - 1] + d[i]) / 2
    for (i in 0 until n - 1) {
        if (d[i] == 0f) { m[i] = 0f; m[i + 1] = 0f; continue }
        val a = m[i] / d[i]; val b = m[i + 1] / d[i]
        val s = a * a + b * b
        if (s > 9f) { val t = 3f / sqrt(s); m[i] = t * a * d[i]; m[i + 1] = t * b * d[i] }
    }
    for (i in 0 until n - 1) {
        val h = dx[i] / 3
        into.cubicTo(pts[i].x + h, pts[i].y + m[i] * h, pts[i + 1].x - h, pts[i + 1].y - m[i + 1] * h, pts[i + 1].x, pts[i + 1].y)
    }
    return into
}

/**
 * The chart (ui-spec A3 ①): the last 2 minutes, [endMs] being "now" at the right edge; the vertical axis runs 0 to
 * [ceiling]. Pressing and dragging picks a sample ([onScrub] gets it, or null on release) and draws a line and a dot
 * on it. [dim] is for stale data.
 */
@Composable
fun StatusChart(
    points: List<ChartPoint>,
    ceiling: Double,
    endMs: Double,
    onScrub: (ChartPoint?) -> Unit,
    modifier: Modifier = Modifier,
    color: Color = pal.ink,
) {
    val p = pal
    val d = LocalDensity.current
    var width by remember { mutableStateOf(0f) }
    var fingerX by remember { mutableStateOf<Float?>(null) }
    val end = endMs.toLong()
    val selected = fingerX?.takeIf { width > 0f }?.let { x ->
        val shown = points.filter { it.timeMs <= end }
        shown.getOrNull(nearestSample(shown, endMs - (1.0 - x / width.toDouble()) * CHART_WINDOW_MS))
    }
    val report by rememberUpdatedState(onScrub)
    val haptic = androidx.compose.ui.platform.LocalView.current
    LaunchedEffect(selected?.timeMs) {
        report(selected)
        if (selected != null) haptic.performHapticFeedback(android.view.HapticFeedbackConstants.CLOCK_TICK)
    }
    Canvas(
        modifier.fillMaxWidth().height(104.dp).onSizeChanged { width = it.width.toFloat() }.pointerInput(Unit) {
            awaitEachGesture {
                val down = awaitFirstDown(requireUnconsumed = false)
                fingerX = down.position.x
                down.consume()
                while (true) {
                    val event = awaitPointerEvent()
                    val c = event.changes.firstOrNull { it.id == down.id }
                    if (c == null || !c.pressed) break
                    fingerX = c.position.x
                    c.consume()
                }
                fingerX = null
            }
        },
    ) {
        val h = size.height
        val pad = 4.dp.toPx()
        fun px(x: Double, y: Double) = Offset((x * size.width).toFloat(), (pad + (1 - y) * (h - 2 * pad)).toFloat())
        for (run in chartRuns(points, end, ceiling)) {
            if (run.size < 2) continue
            val pts = run.map { px(it.x, it.y) }
            val line = smoothPath(pts)
            val fill = Path().apply { addPath(line); lineTo(pts.last().x, h); lineTo(pts.first().x, h); close() }
            val top = pts.minOf { it.y }
            drawPath(fill, Brush.verticalGradient(listOf(color.copy(alpha = .16f), color.copy(alpha = 0f)), startY = top, endY = h))
            drawPath(line, color, style = Stroke(2.dp.toPx(), cap = StrokeCap.Round, join = androidx.compose.ui.graphics.StrokeJoin.Round))
        }
        valueAt(points, end)?.let { v ->
            val o = px(1.0, (v / ceiling).coerceIn(0.0, 1.0))
            drawCircle(color, 3.5.dp.toPx(), Offset(size.width - 4.dp.toPx(), o.y))
        }
        selected?.let { s ->
            val x = ((s.timeMs - (end - CHART_WINDOW_MS)).toDouble() / CHART_WINDOW_MS * size.width).toFloat()
            drawLine(p.ink3, Offset(x, 0f), Offset(x, h), strokeWidth = 1.dp.toPx())
            drawCircle(color, 3.5.dp.toPx(), px(x.toDouble() / size.width, (s.value / ceiling).coerceIn(0.0, 1.0)).copy(x = x))
        }
    }
}
