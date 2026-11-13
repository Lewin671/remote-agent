package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.runtime.withFrameNanos
import androidx.compose.runtime.collectAsState
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableDoubleStateOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.TransformOrigin
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.layout.boundsInRoot
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.platform.testTag
import androidx.compose.foundation.layout.requiredSize
import io.github.lewin671.remoteagent.core.SWAP_MS
import io.github.lewin671.remoteagent.core.swapNewAlpha
import io.github.lewin671.remoteagent.core.swapOldAlpha
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.currentStateAsState
import androidx.lifecycle.repeatOnLifecycle
import io.github.lewin671.remoteagent.core.ChartPoint
import io.github.lewin671.remoteagent.core.MachinePollState
import io.github.lewin671.remoteagent.core.NO_VALUE
import io.github.lewin671.remoteagent.core.MachineStatusController
import io.github.lewin671.remoteagent.core.MachineTone
import io.github.lewin671.remoteagent.core.RingSpec
import io.github.lewin671.remoteagent.core.StatusMetric
import io.github.lewin671.remoteagent.core.StatusText
import io.github.lewin671.remoteagent.core.StatusView
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.UsageOrder
import io.github.lewin671.remoteagent.core.UsageRow
import io.github.lewin671.remoteagent.core.RingKind
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.chartCeiling
import io.github.lewin671.remoteagent.core.chartPoints
import io.github.lewin671.remoteagent.core.chartSummary
import io.github.lewin671.remoteagent.core.cpuFigures
import io.github.lewin671.remoteagent.core.diskCard
import io.github.lewin671.remoteagent.core.footerParts
import io.github.lewin671.remoteagent.core.listTitle
import io.github.lewin671.remoteagent.core.machineRings
import io.github.lewin671.remoteagent.core.memoryCard
import io.github.lewin671.remoteagent.core.nextChartEnd
import io.github.lewin671.remoteagent.core.showDiskActions
import io.github.lewin671.remoteagent.core.statusView
import io.github.lewin671.remoteagent.core.usageRows
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.MachineStatus
import kotlinx.coroutines.launch

/*
 * 电脑状态 (ui-spec A, 界面稿 v2 第 ② 部分): the three rings at the top of a computer's page and the page they
 * open. The polling is core's [MachineStatusController]; what is drawn comes from core's models.
 */

/** How a screen reads a computer's status: the account's own way; a test supplies fixtures. */
internal val LocalStatusFetch = androidx.compose.runtime.staticCompositionLocalOf<suspend (io.github.lewin671.remoteagent.core.Remote, String) -> MachineStatus> { { remote, id -> remote.getMachineStatus(id) } }

/** What the controller holds, as one value a composition can read. */
@Stable
class MachineFeed(
    val state: MachinePollState,
    val last: MachineStatus?,
    val curveSinceMs: Long,
    /** When [last] arrived on this phone, for "how old is it". */
    val receivedAtMs: Long,
    /** Asks again at once (the retry of 读不到这台电脑的状态). */
    val retry: () -> Unit = {},
) {
    val view: StatusView get() = statusView(state, last)

    companion object { val Empty = MachineFeed(MachinePollState.IDLE, null, 0, 0) }
}

/**
 * The polling of one computer for as long as the page is on screen: every [intervalMs], not while the page is hidden
 * or the app is in the background (ui-spec A1, A4). [supported] is the computer's `machineStatus` capability.
 */
@Composable
fun rememberMachineFeed(machineId: String, intervalMs: Long, supported: Boolean, online: Boolean, fetch: suspend () -> MachineStatus): MachineFeed {
    val scope = rememberCoroutineScope()
    var feed by remember(machineId) { mutableStateOf(MachineFeed.Empty) }
    val latest by rememberUpdatedState(fetch)
    val controller = remember(machineId, intervalMs) {
        val self = arrayOfNulls<MachineStatusController>(1)
        var seen: MachineStatus? = null
        var at = 0L
        MachineStatusController(scope, { latest() }, intervalMs, changed = {
            self[0]?.let { c ->
                if (c.last !== seen) { seen = c.last; at = System.currentTimeMillis() }
                feed = MachineFeed(c.state, c.last, c.curveSinceMs, at) { c.retry() }
            }
        }).also { self[0] = it }
    }
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    val visible = lifecycle.currentStateAsState().value.isAtLeast(Lifecycle.State.STARTED)
    LaunchedEffect(controller, visible, online, supported) { controller.setActive(visible, online, supported) }
    DisposableEffect(controller) { onDispose { controller.dispose() } }
    return feed
}

// ---- the entrance: three rings ------------------------------------------------------------------------------

private fun RingKind.icon(): ImageVector = when (this) { RingKind.CPU -> Lucide.Cpu; RingKind.MEMORY -> Lucide.Memory; RingKind.DISK -> Lucide.HardDrive }

/** 设置 › 电脑 › 某台电脑: the rings. [online] false draws tracks and dashes, at 55%, and cannot be opened. */
@Composable
fun MachineRingsCard(rings: List<RingSpec>, online: Boolean, onOpen: () -> Unit, modifier: Modifier = Modifier) {
    val p = pal
    val open = uiText(UiMessages.machineStatusTitle())
    Row(
        modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, bottom = 12.dp).alpha(if (online) 1f else .55f).clip(Radii.R22).background(p.gbg)
            .then(if (online) Modifier.clickable(role = Role.Button, onClickLabel = open, onClick = onOpen) else Modifier)
            .padding(start = 6.dp, end = 6.dp, top = 16.dp, bottom = 14.dp),
    ) {
        for (r in rings) Column(
            Modifier.weight(1f).clearAndSetSemantics { contentDescription = r.spoken },
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(2.dp),
        ) {
            StatusRing(r.fraction, r.tone, r.kind.icon())
            Spacer(Modifier.height(6.dp))
            Text(r.value, style = ts(17.sp, FontWeight.SemiBold).digits(), color = p.toneInk(r.tone, p.ink), maxLines = 1, textAlign = TextAlign.Center)
            Text(r.name, style = T.s12, color = p.ink2, textAlign = TextAlign.Center)
        }
    }
}

// ---- the page ------------------------------------------------------------------------------------------------

/** The little dot under the title: green and breathing while live, orange and still when the data is old. */
@Composable
private fun LiveDot(stale: Boolean, idle: Boolean = false) {
    val p = pal
    val breathe = rememberInfiniteTransition()
    val phase by breathe.animateFloat(0f, 1f, infiniteRepeatable(tween(1200, easing = androidx.compose.animation.core.FastOutSlowInEasing), RepeatMode.Reverse))
    val t = if (Motion.reduced || LocalChartFrozen.current) 0f else phase
    val color = if (idle) p.ink3 else if (stale) p.orange else p.green
    Canvas(Modifier.size(13.dp)) {
        val c = center
        if (!stale && !idle) drawCircle(color.copy(alpha = .22f * (1f - t)), (3.5.dp + (3 + 2 * t).dp).toPx(), c)
        drawCircle(color, 3.5.dp.toPx(), c)
    }
}

@Composable
private fun StatusHeader(name: String, liveText: String?, stale: Boolean, p: Palette, idle: Boolean = false) {
    Column(Modifier.padding(start = 4.dp, end = 4.dp, top = 6.dp)) {
        Text(name, style = T.title.copy(letterSpacing = (-0.6).sp), color = p.ink, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.semantics { heading() })
        if (liveText != null) Row(Modifier.padding(top = 2.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(0.dp)) {
            LiveDot(stale, idle)
            Spacer(Modifier.width(3.dp))
            Text(liveText, style = T.s15, color = p.ink2)
        }
    }
}

/**
 * The swap of the main chart and a small card (ui-spec A3, 350 ms): the card's colour and corners stay in view the whole time and
 * move to the other's place and size; the old figures fade out in the first 100 ms, nothing shows until the last 120, in which
 * the new ones fade in. [progress] is linear time, 0 to 1.
 */
@Stable
class SwapAnimation {
    val bounds = mutableStateMapOf<StatusMetric, Rect>()
    var from by mutableStateOf<Map<StatusMetric, Rect>?>(null)
    val progress = Animatable(1f)
}

/** Where a card's box is on its way: an offset and a scale from the box it had to the box it has now. */
private class SwapTransform(val dx: Float, val dy: Float, val sx: Float, val sy: Float, val pending: Boolean)

private fun swapTransform(host: SwapAnimation, id: StatusMetric): SwapTransform? {
    val f = host.from?.get(id) ?: return null
    val to = host.bounds[id] ?: return null
    val linear = host.progress.value
    if (linear >= 1f || to.width <= 0f || to.height <= 0f) return null
    // The new layout is not measured yet: the box has not got its new place to move from.
    if (f == to) return SwapTransform(0f, 0f, 1f, 1f, pending = true)
    val e = Motion.Decelerate.transform(linear)
    return SwapTransform(
        (f.left - to.left) * (1 - e), (f.top - to.top) * (1 - e),
        f.width / to.width + (1 - f.width / to.width) * e, f.height / to.height + (1 - f.height / to.height) * e, pending = false,
    )
}

/** For tests: how visible the old and the new figures of a swapping card are. */
internal val SwapAlpha = androidx.compose.ui.semantics.SemanticsPropertyKey<Float>("SwapAlpha")
private var androidx.compose.ui.semantics.SemanticsPropertyReceiver.swapAlpha by SwapAlpha

/**
 * A card that can take the other's place. Its container (colour, corners, shadow) moves and resizes over the 350 ms; what is in it
 * is drawn at its own size, not stretched: the old content (the card as it was) fades out first, the new one fades in last.
 */
@Composable
private fun SwapCard(id: StatusMetric, hero: Boolean, swap: SwapAnimation, modifier: Modifier, content: @Composable (Boolean) -> Unit) {
    val p = pal
    val density = androidx.compose.ui.platform.LocalDensity.current
    val from = swap.from?.get(id)
    val running = from != null && swap.progress.value < 1f
    val ms = swap.progress.value.toDouble() * SWAP_MS
    val newAlpha = if (running) swapNewAlpha(ms) else 1f
    val oldAlpha = if (running) swapOldAlpha(ms) else 0f
    val natural = Modifier.graphicsLayer {
        val t = swapTransform(swap, id)
        if (t != null) { transformOrigin = TransformOrigin(0f, 0f); scaleX = 1f / t.sx; scaleY = 1f / t.sy }
    }
    Box(
        modifier.onGloballyPositioned { swap.bounds[id] = it.boundsInRoot() }.graphicsLayer {
            val t = swapTransform(swap, id)
            if (t != null) {
                transformOrigin = TransformOrigin(0f, 0f)
                translationX = t.dx; translationY = t.dy; scaleX = t.sx; scaleY = t.sy
                if (t.pending) alpha = 0f
            }
        }.statusCard(p),
        contentAlignment = Alignment.TopStart,
    ) {
        Box(natural.alpha(newAlpha).testTag("swap-new-$id").semantics { swapAlpha = newAlpha }) { content(hero) }
        if (running && oldAlpha > 0f && from != null) {
            Box(
                natural.requiredSize(with(density) { androidx.compose.ui.unit.DpSize(from.width.toDp(), from.height.toDp()) }).alpha(oldAlpha)
                    .testTag("swap-old-$id").clearAndSetSemantics { swapAlpha = oldAlpha },
            ) { content(!hero) }
        }
    }
}

/** The big figure of the main chart and its unit. */
@Composable
private fun BigNumber(metric: StatusMetric, value: String, number: Double, unit: String?, color: Color, unitColor: Color) {
    Row(verticalAlignment = Alignment.Top) {
        RollingText(value, number, ts(52.sp, FontWeight.SemiBold, 1.05f).copy(letterSpacing = (-1.2).sp), color)
        if (metric == StatusMetric.CPU) Text("%", style = ts(24.sp, FontWeight.SemiBold), color = color, modifier = Modifier.padding(top = 7.dp, start = 1.dp))
        else if (unit != null) Text(unit, style = ts(24.sp, FontWeight.SemiBold), color = unitColor, modifier = Modifier.padding(start = 4.dp, top = 22.dp))
    }
}

@Composable
private fun Kick(icon: ImageVector, label: String, color: Color) {
    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        Ic(icon, 14.dp, color)
        Text(label, style = T.s13b, color = color)
    }
}

private fun loadLine(full: String, number: String?, size: Float, bold: Color, rest: Color): AnnotatedString {
    val i = number?.let { full.indexOf(it) } ?: -1
    return buildAnnotatedString {
        if (i < 0) { withStyle(SpanStyle(color = rest)) { append(full) }; return@buildAnnotatedString }
        withStyle(SpanStyle(color = rest)) { append(full.substring(0, i)) }
        withStyle(SpanStyle(color = bold, fontWeight = FontWeight.SemiBold, fontSize = size.sp)) { append(number) }
        withStyle(SpanStyle(color = rest)) { append(full.substring(i + number!!.length)) }
    }
}

/** The page's figures for one moment, with the user's current look-back or none. */
@Composable
private fun HeroCard(
    metric: StatusMetric,
    s: MachineStatus,
    text: StatusText,
    stale: Boolean,
    points: List<ChartPoint>,
    endMs: Double,
    scrub: ChartPoint?,
    onScrub: (ChartPoint?) -> Unit,
    onStep: (Int) -> Unit,
    modifier: Modifier,
) {
    val p = pal
    val ink = if (stale) p.ink3 else p.ink
    val ink2 = if (stale) p.ink3 else p.ink2
    val copy = text.copy
    val label = if (metric == StatusMetric.CPU) copy(UiMessages.machineStatusCpu()) else copy(UiMessages.machineStatusMemory())
    val cpu = cpuFigures(s, text)
    val mem = memoryCard(s, text)
    val value: String; val number: Double
    if (metric == StatusMetric.CPU) {
        val v = scrub?.value?.coerceIn(0.0, 100.0)?.let { Math.round(it).toInt() } ?: cpu.percent
        value = v.toString(); number = v.toDouble()
    } else {
        val bytes = scrub?.value?.toLong() ?: s.memoryUsedBytes
        value = text.gbNumber(bytes, oneDecimal = true, binary = true); number = bytes / io.github.lewin671.remoteagent.core.GIB
    }
    Column(modifier.fillMaxWidth().padding(top = 16.dp)) {
        Row(Modifier.padding(horizontal = 18.dp).fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.Bottom) {
            Column(Modifier.weight(1f, fill = false)) {
                Kick(if (metric == StatusMetric.CPU) Lucide.Cpu else Lucide.Memory, label, ink2)
                Spacer(Modifier.height(4.dp))
                BigNumber(metric, value, number, copy(UiMessages.machineStatusGbUsed()), ink, ink2)
            }
            Spacer(Modifier.width(12.dp))
            Column(Modifier.padding(bottom = 8.dp), horizontalAlignment = Alignment.End) {
                if (metric == StatusMetric.CPU) {
                    cpu.load?.let { Text(loadLine(copy(UiMessages.machineStatusLoad(it)), it, 15f, ink, ink2), style = T.s13.digits()) }
                    cpu.cores?.let { Text(it, style = T.s13, color = ink2) }
                } else {
                    Text(copy(UiMessages.machineStatusTotal(mem.total)), style = T.s13, color = ink2)
                    mem.swap?.let { Text(copy(UiMessages.machineStatusSwap(it)), style = T.s13, color = ink2) }
                }
            }
        }
        if (stale || points.isEmpty()) Spacer(Modifier.height(16.dp))
        else {
            val summary = chartSummary(points, metric, text)
            Box(
                Modifier.padding(top = 6.dp).semantics {
                    contentDescription = summary
                    customActions = listOf(
                        CustomAccessibilityAction(copy(UiMessages.machineStatusA11YPrevSample())) { onStep(-1); true },
                        CustomAccessibilityAction(copy(UiMessages.machineStatusA11YNextSample())) { onStep(1); true },
                    )
                },
            ) { StatusChart(points, chartCeiling(s, metric), endMs, onScrub, color = ink) }
            val pressure = mem.pressureWord
            if (metric == StatusMetric.MEMORY && pressure != null)
                Text(copy(pressure), style = T.s13b, color = p.toneInk(mem.tone, p.ink2), modifier = Modifier.fillMaxWidth().padding(end = 18.dp, top = 2.dp), textAlign = TextAlign.End)
            Row(Modifier.fillMaxWidth().padding(start = 18.dp, end = 18.dp, top = 6.dp, bottom = 13.dp), horizontalArrangement = Arrangement.SpaceBetween) {
                for (key in listOf(UiMessages.machineStatusAxis2Min(), UiMessages.machineStatusAxis1Min(), UiMessages.machineStatusAxisNow()))
                    Text(copy(key), style = ts(11.sp), color = p.ink3)
            }
        }
    }
}

@Composable
private fun TileCard(modifier: Modifier, onClick: (() -> Unit)?, description: String, onClickLabel: String? = null, card: Boolean = true, content: @Composable androidx.compose.foundation.layout.ColumnScope.() -> Unit) {
    val p = pal
    Column(
        modifier.then(if (card) Modifier.statusCard(p) else Modifier.fillMaxSize())
            .then(if (onClick != null) Modifier.clickable(role = Role.Button, onClickLabel = onClickLabel, onClick = onClick) else Modifier)
            .semantics(mergeDescendants = true) { contentDescription = description }
            .heightIn(min = 168.dp).padding(start = 16.dp, end = 16.dp, top = 14.dp, bottom = 15.dp),
        content = content,
    )
}

@Composable
private fun LargeFigure(value: String, number: Double, unit: String, color: Color, unitColor: Color) {
    // The unit drops under the figure, whole, when the two do not fit side by side.
    @OptIn(androidx.compose.foundation.layout.ExperimentalLayoutApi::class)
    androidx.compose.foundation.layout.FlowRow(Modifier.padding(top = 6.dp), horizontalArrangement = Arrangement.spacedBy(3.dp), itemVerticalAlignment = Alignment.Bottom) {
        RollingText(value, number, ts(34.sp, FontWeight.SemiBold, 1.1f).copy(letterSpacing = (-0.6).sp), color, fixedWidth = false)
        Text(unit, style = ts(17.sp, FontWeight.SemiBold), color = unitColor, modifier = Modifier.padding(bottom = 2.dp))
    }
}

@Composable
private fun SayLines(first: String, firstColor: Color, second: String?, secondColor: Color, third: String? = null, breakSecondAtComma: Boolean = false, oneLineEach: Boolean = false) {
    Column(Modifier.padding(top = 10.dp)) {
        Text(first, style = T.s13b, color = firstColor)
        if (oneLineEach && second != null) OneLine(second, secondColor)
        else if (second != null) {
            val i = if (breakSecondAtComma) second.indexOfFirst { it == '，' || it == ',' } else -1
            if (i < 0) Text(second, style = T.s13, color = secondColor)
            else {
                // Too narrow for one line: it breaks at the comma, not inside a figure.
                @OptIn(androidx.compose.foundation.layout.ExperimentalLayoutApi::class)
                androidx.compose.foundation.layout.FlowRow {
                    Text(second.substring(0, i + 1) + if (second[i] == ',') " " else "", style = T.s13, color = secondColor)
                    Text(second.substring(i + 1).trimStart(), style = T.s13, color = secondColor)
                }
            }
        }
        if (third != null) { if (oneLineEach) OneLine(third, secondColor) else Text(third, style = T.s13, color = secondColor) }
    }
}

/**
 * A line that stays one line: the type steps down from 13 to 10 to fit the card's width. At large text sizes it wraps as any text
 * does, so that nothing is cut.
 */
@OptIn(androidx.compose.foundation.ExperimentalFoundationApi::class)
@Composable
private fun OneLine(text: String, color: Color) {
    if (androidx.compose.ui.platform.LocalDensity.current.fontScale > 1.15f) { Text(text, style = T.s13, color = color); return }
    androidx.compose.foundation.text.BasicText(
        text, style = T.s13.copy(color = color), maxLines = 1,
        autoSize = androidx.compose.foundation.text.TextAutoSize.StepBased(minFontSize = 10.sp, maxFontSize = 13.sp),
    )
}

@Composable
private fun MemoryTile(s: MachineStatus, text: StatusText, stale: Boolean, modifier: Modifier, card: Boolean = true, onClick: () -> Unit) {
    val p = pal
    val c = memoryCard(s, text)
    val copy = text.copy
    val word = c.pressureWord?.let { copy(it) }
    val used = copy(UiMessages.machineStatusSizeGb(c.used))
    val spoken = if (word != null) copy(UiMessages.machineStatusA11YMemoryCard(pressure = word, used = used)) else copy(UiMessages.machineStatusA11YMemoryCardPlain(used))
    TileCard(modifier, onClick, spoken, card = card) {
        Kick(Lucide.Memory, copy(UiMessages.machineStatusMemory()), if (stale) p.ink3 else p.ink2)
        LargeFigure(c.used, c.usedNumber, copy(UiMessages.machineStatusGbUsed()), if (stale) p.ink3 else p.ink, if (stale) p.ink3 else p.ink2)
        val position = c.position
        if (position != null) { Spacer(Modifier.height(14.dp)); PressureScale(position, Modifier.alpha(if (stale) .5f else 1f)) }
        Spacer(Modifier.weight(1f, fill = true).heightIn(min = 0.dp))
        SayLines(word.orEmpty().takeIf { it.isNotEmpty() } ?: c.detail, if (stale) p.ink3 else p.toneInk(c.tone, p.ink), if (word != null) c.detail else null, if (stale) p.ink3 else p.ink2, breakSecondAtComma = true)
    }
}

@Composable
private fun CpuTile(s: MachineStatus, text: StatusText, stale: Boolean, modifier: Modifier, card: Boolean = true, onClick: () -> Unit) {
    val p = pal
    val copy = text.copy
    val f = cpuFigures(s, text)
    TileCard(modifier, onClick, copy(UiMessages.machineStatusA11YCpuCard("${f.percent}%")), card = card) {
        Kick(Lucide.Cpu, copy(UiMessages.machineStatusCpu()), if (stale) p.ink3 else p.ink2)
        LargeFigure(f.percent.toString(), f.percent.toDouble(), "%", if (stale) p.ink3 else p.ink, if (stale) p.ink3 else p.ink2)
        Spacer(Modifier.weight(1f, fill = true))
        SayLines(f.load?.let { copy(UiMessages.machineStatusLoadLine(it)) } ?: f.cores.orEmpty(), if (stale) p.ink3 else p.ink, if (f.load != null) f.cores else null, if (stale) p.ink3 else p.ink2)
    }
}

@Composable
private fun DiskTile(s: MachineStatus, text: StatusText, stale: Boolean, modifier: Modifier, onClick: () -> Unit) {
    val p = pal
    val c = diskCard(s, text)
    val copy = text.copy
    val word = copy(c.word)
    val bad = c.tone == MachineTone.CRITICAL
    val ink = if (stale) p.ink3 else p.toneInk(c.tone, p.ink)
    TileCard(modifier, onClick, copy(UiMessages.machineStatusA11YDiskCard(text.size(s.disk.availableBytes), word)), onClickLabel = copy(UiMessages.machineStatusAgentUsage())) {
        Kick(Lucide.HardDrive, copy(UiMessages.machineStatusDisk()), if (stale) p.ink3 else p.ink2)
        LargeFigure(c.free, s.disk.availableBytes / 1e9, copy(UiMessages.machineStatusGbAvailable()), ink, if (stale) p.ink3 else if (bad) p.red else p.ink2)
        Spacer(Modifier.height(12.dp))
        Box(Modifier.alpha(if (stale) .5f else 1f)) { DiskBar(c.agentShare, c.otherShare, c.tone) }
        Spacer(Modifier.weight(1f, fill = true))
        SayLines(word, ink, c.detail, if (stale) p.ink3 else if (bad) p.red else p.ink2, third = c.detail2, oneLineEach = true)
    }
}

/** A 48-high row of a list card: its words, a value and an arrow. */
@Composable
private fun ListRow(text: String, color: Color, value: String?, chevron: Boolean, first: Boolean, onClick: () -> Unit) {
    val p = pal
    Row(
        Modifier.fillMaxWidth().heightIn(min = 48.dp).clickable(role = Role.Button, onClick = onClick).padding(horizontal = 16.dp)
            .then(if (!first) Modifier.topLine(p.sep, 0.dp) else Modifier),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(text, style = T.s17, color = color, modifier = Modifier.weight(1f).padding(vertical = 11.dp))
        if (value != null) Text(value, style = T.s17.digits(), color = p.ink2, modifier = Modifier.padding(start = 8.dp))
        if (chevron) { Spacer(Modifier.width(8.dp)); Arrow(p.ink3, T.s15) }
    }
}

private val agentGradients = mapOf(
    Agent.AGENT_CLAUDE to listOf(Color(0xFFE69A72), Color(0xFFD2714A)),
    Agent.AGENT_CODEX to listOf(Color(0xFF3A3A3C), Color(0xFF1C1C1E)),
    Agent.AGENT_ANTIGRAVITY to listOf(Color(0xFF6AA7FF), Color(0xFF3478F6)),
)

@Composable
private fun UsageIcon(row: UsageRow, dim: Boolean) {
    val p = pal
    Box(Modifier.padding(top = 1.dp).size(30.dp).clip(RoundedCornerShape(8.dp)), contentAlignment = Alignment.Center) {
        val agent = row.agent
        val mark = agent?.let { AgentMarks.of(it) }
        if (row.isSession && mark != null) {
            Box(Modifier.fillMaxSize().background(Brush.linearGradient(agentGradients[agent] ?: listOf(Color(0xFFB0B0B8), Color(0xFF8E8E93)))).alpha(if (dim) .5f else 1f))
            Icon16(mark, Color.White)
        } else {
            Box(Modifier.fillMaxSize().background(p.fill2))
            Ic(Lucide.Terminal, 16.dp, p.ink2)
        }
    }
}

@Composable
private fun Icon16(v: ImageVector, tint: Color) = androidx.compose.material3.Icon(v, null, Modifier.size(16.dp), tint)

@Composable
private fun UsageList(rows: List<UsageRow>, showSessions: Boolean, dim: Boolean, empty: String, onSession: (String) -> Unit) {
    val p = pal
    Column(Modifier.fillMaxWidth().statusCard(p).padding(vertical = 2.dp)) {
        if (rows.isEmpty()) Text(empty, style = T.s13, color = p.ink2, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth().padding(16.dp))
        rows.forEachIndexed { i, r ->
            androidx.compose.runtime.key(r.id) {
                Row(
                    Modifier.fillMaxWidth().animatePlacement()
                        .then(if (showSessions) Modifier.clickable(role = Role.Button) { onSession(r.sessionId) } else Modifier)
                        .padding(start = 16.dp, end = 16.dp, top = 11.dp),
                    horizontalArrangement = Arrangement.spacedBy(12.dp),
                ) {
                    UsageIcon(r, dim)
                    Column(Modifier.weight(1f).then(if (i < rows.lastIndex) Modifier.bottomLine(p.sep) else Modifier).padding(bottom = 12.dp)) {
                        Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                            Text(r.title, style = ts(16.sp), color = if (dim) p.ink3 else p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                            Text(r.primary, style = ts(16.sp, FontWeight.Medium).digits(), color = if (dim) p.ink3 else p.ink, maxLines = 1)
                        }
                        UsageMeter(r.bar, r.top, dim, r.secondary, Modifier.padding(top = 6.dp))
                    }
                }
            }
        }
    }
}

private fun Modifier.bottomLine(color: Color): Modifier = this.drawBehind {
    drawLine(color, Offset(0f, size.height - 0.25.dp.toPx()), Offset(size.width, size.height - 0.25.dp.toPx()), strokeWidth = 0.5.dp.toPx())
}

/** The main card before any data: its title, a dash, the empty room of the curve and the axis. */
@Composable
private fun HeroSkeleton(metric: StatusMetric, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String, modifier: Modifier) {
    val p = pal
    Column(modifier.fillMaxWidth().statusCard(p).padding(top = 16.dp)) {
        Column(Modifier.padding(horizontal = 18.dp)) {
            Kick(if (metric == StatusMetric.CPU) Lucide.Cpu else Lucide.Memory, copy(if (metric == StatusMetric.CPU) UiMessages.machineStatusCpu() else UiMessages.machineStatusMemory()), p.ink2)
            Spacer(Modifier.height(4.dp))
            Text(NO_VALUE, style = ts(52.sp, FontWeight.SemiBold, 1.05f).copy(letterSpacing = (-1.2).sp), color = p.ink3)
        }
        Spacer(Modifier.height(8.dp + 104.dp))
        Row(Modifier.fillMaxWidth().padding(start = 18.dp, end = 18.dp, top = 6.dp, bottom = 13.dp), horizontalArrangement = Arrangement.SpaceBetween) {
            for (key in listOf(UiMessages.machineStatusAxis2Min(), UiMessages.machineStatusAxis1Min(), UiMessages.machineStatusAxisNow()))
                Text(copy(key), style = ts(11.sp), color = p.ink3)
        }
    }
}

@Composable
private fun TileSkeleton(icon: ImageVector, label: String, modifier: Modifier) {
    val p = pal
    TileCard(modifier, null, label) {
        Kick(icon, label, p.ink2)
        Text(NO_VALUE, style = ts(34.sp, FontWeight.SemiBold, 1.1f).copy(letterSpacing = (-0.6).sp), color = p.ink3, modifier = Modifier.padding(top = 6.dp))
    }
}

/**
 * The page (ui-spec A3): the pieces are all values, so a screenshot can draw any moment of it. [main] is the metric of the
 * big chart; [liveText] is the line under the title.
 */
@Composable
fun MachineStatusContent(
    name: String,
    feed: MachineFeed,
    nowMs: Long,
    main: StatusMetric,
    onMain: (StatusMetric) -> Unit,
    showSessions: Boolean,
    onShowSessions: (Boolean) -> Unit,
    titleOf: (String) -> String?,
    agentOf: (String) -> Agent?,
    onBack: () -> Unit,
    onSession: (String) -> Unit,
    onOpenStorage: () -> Unit,
    onCleanup: () -> Unit,
    onRetry: () -> Unit,
    swap: SwapAnimation = remember { SwapAnimation() },
) {
    val p = pal
    val locale = LocalMessageLocale.current
    val copy = uiText()
    val text = remember(copy, locale) { StatusText(copy, locale) }
    val view = feed.view
    val s = feed.last
    val stale = view == StatusView.STALE
    val frozen = LocalChartFrozen.current
    val points = remember(s, main, feed.curveSinceMs) { if (s == null) emptyList() else chartPoints(s, main, feed.curveSinceMs) }
    val first = points.firstOrNull()?.timeMs ?: 0L
    val latest = points.lastOrNull()?.timeMs ?: 0L
    var end by remember { mutableDoubleStateOf(Double.NaN) }
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(first, latest, Motion.reduced, frozen, stale) {
        if (latest == 0L) return@LaunchedEffect
        if (frozen || Motion.reduced || stale) { end = latest.toDouble(); return@LaunchedEffect }
        lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            var prev = withFrameNanos { it }
            while (true) {
                val t = withFrameNanos { it }
                end = nextChartEnd(end.takeIf { !it.isNaN() }, (t - prev) / 1e6, first, latest)
                prev = t
            }
        }
    }
    val endMs = if (end.isNaN()) latest.toDouble() else end
    var scrub by remember { mutableStateOf<ChartPoint?>(null) }
    // The adjust action of a screen reader walks the samples as dragging would.
    var stepIndex by remember { mutableStateOf(-1) }
    val coroutines = rememberCoroutineScope()
    fun step(d: Int) {
        if (points.isEmpty()) return
        val from = if (stepIndex in points.indices) stepIndex else points.lastIndex
        stepIndex = (from + d).coerceIn(0, points.lastIndex)
        scrub = points[stepIndex]
    }
    val liveText = when {
        view == StatusView.LIVE && scrub != null && s != null -> text.lookback(latest - scrub!!.timeMs)
        view == StatusView.LIVE -> copy(UiMessages.machineStatusLive())
        view == StatusView.STALE -> text.stale(nowMs - feed.receivedAtMs)
        else -> null
    }
    fun pick(next: StatusMetric) {
        if (next == main) return
        scrub = null; stepIndex = -1
        if (Motion.reduced) { onMain(next); return }
        coroutines.launch {
            swap.from = swap.bounds.toMap()
            swap.progress.snapTo(0f)
            onMain(next)
            swap.progress.animateTo(1f, tween(SWAP_MS, easing = androidx.compose.animation.core.LinearEasing))
            swap.from = null
        }
    }
    CompositionLocalProvider(LocalOnSoft provides !p.dark) {
        Column(Modifier.fillMaxSize().background(p.statusPage)) {
            BackRow(onBack)
            when (view) {
                StatusView.LOADING -> Column(Modifier.weight(1f).verticalScrollFaded(p.statusPage).padding(start = 16.dp, end = 16.dp, bottom = 30.dp)) {
                    // Nothing read yet: the page's frame, dashes for the figures, no curve and no ranking.
                    StatusHeader(name, copy(UiMessages.machineStatusLoading()), false, p, idle = true)
                    val other = if (main == StatusMetric.CPU) StatusMetric.MEMORY else StatusMetric.CPU
                    HeroSkeleton(main, copy, Modifier.padding(top = 18.dp))
                    Row(Modifier.padding(top = 12.dp).height(IntrinsicSize.Max), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        val tile = Modifier.weight(1f).fillMaxHeight()
                        TileSkeleton(if (other == StatusMetric.MEMORY) Lucide.Memory else Lucide.Cpu, copy(if (other == StatusMetric.MEMORY) UiMessages.machineStatusMemory() else UiMessages.machineStatusCpu()), tile)
                        TileSkeleton(Lucide.HardDrive, copy(UiMessages.machineStatusDisk()), tile)
                    }
                }
                StatusView.UNAVAILABLE -> Column(Modifier.weight(1f).padding(horizontal = 16.dp)) {
                    StatusHeader(name, null, false, p)
                    Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
                        Column(horizontalAlignment = Alignment.CenterHorizontally) {
                            Text(copy(UiMessages.machineStatusUnavailable()), style = T.s17, color = p.ink2, textAlign = TextAlign.Center)
                            TextLink(copy(UiMessages.commonRetry()), onRetry, Modifier.padding(horizontal = 12.dp))
                        }
                    }
                }
                else -> Column(Modifier.weight(1f).verticalScrollFaded(p.statusPage).padding(start = 16.dp, end = 16.dp, bottom = 30.dp)) {
                    s!!
                    StatusHeader(name, liveText, stale, p)
                    val other = if (main == StatusMetric.CPU) StatusMetric.MEMORY else StatusMetric.CPU
                    val hero = @Composable { metric: StatusMetric -> HeroCard(metric, s, text, stale, points, endMs, scrub, { scrub = it }, ::step, Modifier) }
                    val tileOf = @Composable { metric: StatusMetric ->
                        if (metric == StatusMetric.MEMORY) MemoryTile(s, text, stale, Modifier, card = false) { pick(StatusMetric.MEMORY) }
                        else CpuTile(s, text, stale, Modifier, card = false) { pick(StatusMetric.CPU) }
                    }
                    SwapCard(main, true, swap, Modifier.padding(top = 18.dp)) { asHero -> if (asHero) hero(main) else tileOf(main) }
                    Row(Modifier.padding(top = 12.dp).height(IntrinsicSize.Max), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        val tile = Modifier.weight(1f).fillMaxHeight()
                        SwapCard(other, false, swap, tile) { asHero -> if (asHero) hero(other) else tileOf(other) }
                        DiskTile(s, text, stale, tile, onOpenStorage)
                    }
                    if (showDiskActions(s)) Column(Modifier.padding(top = 12.dp).fillMaxWidth().statusCard(p)) {
                        ListRow(copy(UiMessages.machineStatusCleanupAction()), p.blue, null, false, true, onCleanup)
                        ListRow(copy(UiMessages.machineStatusAgentUsage()), p.ink, if (s.hasAgentBytes()) text.size(s.agentBytes) else null, true, false, onOpenStorage)
                    }
                    Row(Modifier.fillMaxWidth().padding(start = 4.dp, end = 4.dp, top = 26.dp, bottom = 10.dp), verticalAlignment = Alignment.Bottom) {
                        Text(copy(UiMessages.machineStatusTopUsage()), style = T.h20, color = p.ink, modifier = Modifier.weight(1f).semantics { heading() })
                        Box(Modifier.heightIn(min = 44.dp).clickable(role = Role.Button) { onShowSessions(!showSessions) }, contentAlignment = Alignment.CenterEnd) {
                            Text(copy(if (showSessions) UiMessages.machineStatusShowProcesses() else UiMessages.machineStatusShowSessions()), style = T.s15, color = p.blue)
                        }
                    }
                    val order = remember { UsageOrder() }
                    val rows = remember(s, showSessions, main) { usageRows(s, showSessions, main, order, text, titleOf, agentOf) }
                    UsageList(rows, showSessions, stale, copy(UiMessages.machineStatusNoRunningSessions()), onSession)
                    val foot = footerParts(s, text)
                    if (foot.isNotEmpty()) Text(foot.joinToString(" · "), style = T.s13, color = p.ink3, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth().padding(top = 22.dp))
                }
            }
        }
    }
}

/** The page of a computer, with its own polling every 2 seconds. */
@Composable
fun MachineStatusScreen(id: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val copy = uiText()
    val m = state.machines[id]
    val supported = m?.meta?.capabilities?.machineStatus == true
    val fetch = LocalStatusFetch.current
    val feed = rememberMachineFeed(id, 2_000, supported, m?.online == true) { fetch(remote, id) }
    var main by remember(id) { mutableStateOf(StatusMetric.CPU) }
    var sessions by remember(id) { mutableStateOf(true) }
    val now = now(5_000)
    MachineStatusContent(
        name = machineLabel(state, id), feed = feed, nowMs = now, main = main, onMain = { main = it },
        showSessions = sessions, onShowSessions = { sessions = it },
        titleOf = { sid -> state.sessions[sid]?.let { listTitle(it).resolve(copy) } },
        agentOf = { sid -> state.sessions[sid]?.agent() },
        onBack = nav.back, onSession = nav.session, onOpenStorage = { nav.deviceStorage(id) }, onCleanup = { nav.cleanup(id) },
        onRetry = feed.retry,
    )
}
