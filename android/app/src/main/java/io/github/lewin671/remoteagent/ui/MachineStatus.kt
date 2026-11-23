package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.layout.boundsInRoot
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsPropertyKey
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.UiMessage
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.DiskPressureState
import io.github.lewin671.remoteagent.core.proto.MachineChartKind
import io.github.lewin671.remoteagent.core.proto.MachineModelUsagePreview
import io.github.lewin671.remoteagent.core.proto.MachinePollMode
import io.github.lewin671.remoteagent.core.proto.MachinePollState
import io.github.lewin671.remoteagent.core.proto.MachineRingKind
import io.github.lewin671.remoteagent.core.proto.MachineRingView
import io.github.lewin671.remoteagent.core.proto.MachineStatusDot
import io.github.lewin671.remoteagent.core.proto.MachineStatusViewModel
import io.github.lewin671.remoteagent.core.proto.MachineUsageRow
import io.github.lewin671.remoteagent.core.proto.MachineUsageTab
import io.github.lewin671.remoteagent.proto.Agent

/*
 * 电脑状态 (machine-status.md): the computer's settings entrance card, and the full page with CPU/内存 hero chart swap,
 * 磁盘 card, cleanup / storage actions, and Top 占用. All business derivations come directly from Go Core ViewModels.
 */

enum class StatusMetric { CPU, MEMORY }

/** Semantics key inspected by swap transition tests. */
val SwapAlpha = SemanticsPropertyKey<Float>("SwapAlpha")
private var androidx.compose.ui.semantics.SemanticsPropertyReceiver.swapAlpha by SwapAlpha

private fun MachineRingKind.icon(): ImageVector = when (this) {
    MachineRingKind.MACHINE_RING_KIND_CPU -> Lucide.Cpu
    MachineRingKind.MACHINE_RING_KIND_MEMORY -> Lucide.Memory
    else -> Lucide.HardDrive
}

/** Entrance card shown on the computer's settings page: header + three small ring gauges that open the status page. */
@Composable
fun MachineRingsCard(
    rings: List<MachineRingView>,
    online: Boolean,
    onOpen: () -> Unit,
    modifier: Modifier = Modifier,
    dot: MachineStatusDot = if (online) MachineStatusDot.MACHINE_STATUS_DOT_GREEN_LIVE else MachineStatusDot.MACHINE_STATUS_DOT_GRAY_STATIC,
    badge: String = "",
    warn: Boolean = false,
) {
    val p = pal
    val copy = uiText()
    val open = copy(UiMessages.machineStatusTitle())
    val badgeLabel = badge.ifEmpty { if (online) copy(UiMessages.machineStatusLive()) else copy(UiMessages.settingsOffline()) }
    Column(
        modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp)
            .padding(bottom = 14.dp)
            .clip(Radii.R22)
            .background(p.gbg)
            .let { if (online) it.clickable(onClickLabel = open, role = Role.Button, onClick = onOpen) else it }
            .padding(horizontal = 16.dp, vertical = 14.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Row(
            Modifier.fillMaxWidth(),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(6.dp),
        ) {
            Text(copy(UiMessages.settingsStatusCardTitle()), style = T.s15b, color = p.ink, modifier = Modifier.weight(1f))
            if (dot != MachineStatusDot.MACHINE_STATUS_DOT_UNSPECIFIED) {
                StatusDotView(dot, size = 7.dp)
            }
            if (badgeLabel.isNotEmpty()) {
                Text(
                    badgeLabel,
                    style = T.s13,
                    color = if (warn) p.orangeInk else p.ink2,
                    maxLines = 1,
                )
            }
            if (online) Arrow(p.ink3, T.s15)
        }
        StatusAdaptiveRow { itemMod ->
            for (r in rings) {
                val accent = p.ringToneInk(r.tone, p.ink)
                val labelStr = uiText(r.label)
                val valueStr = uiText(r.valueText).ifEmpty { "—" }
                val spoken = uiText(r.a11YLabel).ifEmpty { listOf(labelStr, valueStr).filter { it.isNotEmpty() }.joinToString(" ") }
                Row(
                    itemMod.semantics(mergeDescendants = true) { contentDescription = spoken },
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(10.dp),
                ) {
                    StatusRing(if (r.hasValue) r.fraction.toFloat() else null, r.tone, r.kind.icon())
                    Column(Modifier.weight(1f)) {
                        Text(
                            valueStr,
                            style = ts(16.sp, FontWeight.SemiBold).digits(),
                            color = if (r.hasValue && online) accent else p.ink3,
                            maxLines = 1,
                        )
                        Text(labelStr, style = T.s12, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    }
                }
            }
        }
    }
}

@Composable
private fun StatusAdaptiveRow(
    modifier: Modifier = Modifier,
    minSideBySideDp: Float = 150f,
    content: @Composable (Modifier) -> Unit,
) {
    val fontScale = LocalDensity.current.fontScale
    BoxWithConstraints(modifier.fillMaxWidth()) {
        val stack = maxWidth < 300.dp || fontScale >= 1.3f || (maxWidth.value - 12f) / 2f < minSideBySideDp
        if (stack) {
            Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                content(Modifier.fillMaxWidth())
            }
        } else {
            Row(
                Modifier.fillMaxWidth(),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                content(Modifier.weight(1f))
            }
        }
    }
}

@Composable
private fun StatusHeader(name: String, badgeText: String, dot: MachineStatusDot, p: Palette) {
    Column(Modifier.fillMaxWidth()) {
        Text(
            name,
            style = ts(30.sp, FontWeight.Bold, 1.15f),
            color = p.ink,
            maxLines = 2,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.semantics { heading() },
        )
        if (badgeText.isNotEmpty()) {
            Row(
                Modifier.padding(top = 5.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                StatusDotView(dot)
                Text(badgeText, style = T.s13.digits(), color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}

class SwapAnimation {
    var bounds: MutableMap<StatusMetric, Rect> = HashMap()
    var from: Map<StatusMetric, Rect>? by mutableStateOf(null)
    val progress = Animatable(1f)
}

private class SwapTransform(val dx: Float, val dy: Float, val sx: Float, val sy: Float)

private fun swapTransform(host: SwapAnimation, id: StatusMetric): SwapTransform? {
    val oldRect = host.from?.get(id) ?: return null
    val curRect = host.bounds[id] ?: return null
    if (curRect.width <= 1f || curRect.height <= 1f) return null
    val p = host.progress.value
    val dx = (oldRect.left - curRect.left) * (1f - p)
    val dy = (oldRect.top - curRect.top) * (1f - p)
    val sx = 1f + (oldRect.width / curRect.width - 1f) * (1f - p)
    val sy = 1f + (oldRect.height / curRect.height - 1f) * (1f - p)
    return SwapTransform(dx, dy, sx, sy)
}

@Composable
private fun SwapCard(
    id: StatusMetric,
    hero: Boolean,
    swap: SwapAnimation,
    modifier: Modifier,
    content: @Composable (Boolean) -> Unit,
) {
    val from = swap.from?.get(id)
    val running = from != null && swap.progress.value < 1f
    val newAlpha = if (running) swapNewAlpha(swap.progress.value) else 1f
    val oldAlpha = if (running) swapOldAlpha(swap.progress.value) else 0f
    val p = pal
    Box(
        modifier
            .onGloballyPositioned { swap.bounds[id] = it.boundsInRoot() }
            .graphicsLayer {
                val t = swapTransform(swap, id) ?: return@graphicsLayer
                transformOrigin = androidx.compose.ui.graphics.TransformOrigin(0f, 0f)
                translationX = t.dx
                translationY = t.dy
                scaleX = t.sx
                scaleY = t.sy
            }
            .statusCard(p),
    ) {
        Box(
            Modifier
                .fillMaxWidth()
                .alpha(newAlpha)
                .testTag("swap-new-$id")
                .semantics { swapAlpha = newAlpha },
        ) { content(hero) }
        if (running && oldAlpha > 0.01f) {
            Box(
                Modifier
                    .matchParentSize()
                    .alpha(oldAlpha)
                    .testTag("swap-old-$id")
                    .clearAndSetSemantics { swapAlpha = oldAlpha },
            ) { content(!hero) }
        }
    }
}

@Composable
private fun BigNumber(value: String, number: Double, unit: String?, color: Color, unitColor: Color) {
    Row(verticalAlignment = Alignment.Bottom, horizontalArrangement = Arrangement.spacedBy(3.dp)) {
        RollingText(value, number, ts(52.sp, FontWeight.SemiBold, 1.05f).copy(letterSpacing = (-1.2).sp), color)
        if (!unit.isNullOrEmpty()) {
            Text(unit, style = ts(16.sp, FontWeight.Medium), color = unitColor, modifier = Modifier.padding(bottom = 8.dp))
        }
    }
}

@Composable
private fun Kick(icon: ImageVector, label: String, color: Color) {
    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        Ic(icon, 14.dp, color)
        Text(label, style = T.s13, color = color, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

private fun highlightNumbers(full: String, bold: Color, rest: Color): AnnotatedString = buildAnnotatedString {
    var i = 0
    while (i < full.length) {
        val ch = full[i]
        if (ch in '0'..'9') {
            var j = i + 1
            while (j < full.length && (full[j] in '0'..'9' || full[j] == '.')) j++
            withStyle(SpanStyle(color = bold, fontWeight = FontWeight.Medium)) { append(full.substring(i, j)) }
            i = j
        } else {
            withStyle(SpanStyle(color = rest)) { append(ch) }
            i++
        }
    }
}

@Composable
private fun HeroCard(
    view: MachineStatusViewModel,
    metric: StatusMetric,
    dim: Boolean,
    onScrub: (MachineChartKind, Int, Boolean) -> Unit,
) {
    val p = pal
    val copy = uiText()
    val chartKind = if (metric == StatusMetric.CPU) MachineChartKind.MACHINE_CHART_KIND_CPU else MachineChartKind.MACHINE_CHART_KIND_MEMORY
    val chart = if (metric == StatusMetric.CPU) view.cpu.chart else view.memory.chart
    val toneColor = if (metric == StatusMetric.MEMORY) p.pressureToneInk(view.memory.pressure, p.ink) else p.ink
    val accent = if (dim) p.ink3 else if (metric == StatusMetric.MEMORY && toneColor != p.ink) toneColor else p.blue
    val ink = if (dim) p.ink3 else toneColor
    val ink2 = if (dim) p.ink3 else p.ink2
    val label = copy(if (metric == StatusMetric.CPU) UiMessages.machineStatusCpu() else UiMessages.machineStatusMemory())

    val a11yCard = uiText(if (metric == StatusMetric.CPU) view.cpu.a11YLabel else view.memory.a11YLabel)
    val a11yChart = uiText(chart.a11YSummary)
    val prevSampleLabel = copy(UiMessages.machineStatusA11YPrevSample())
    val nextSampleLabel = copy(UiMessages.machineStatusA11YNextSample())

    Column(
        Modifier.fillMaxWidth().padding(16.dp).semantics(mergeDescendants = true) {
            if (a11yCard.isNotEmpty()) contentDescription = a11yCard
        },
    ) {
        Kick(if (metric == StatusMetric.CPU) Lucide.Cpu else Lucide.Memory, label, ink2)
        Spacer(Modifier.height(6.dp))
        Row(
            Modifier.fillMaxWidth(),
            verticalAlignment = Alignment.Bottom,
            horizontalArrangement = Arrangement.SpaceBetween,
        ) {
            val memCaption = uiText(view.memory.caption)
            if (chart.hasScrub) {
                val text = chart.scrubValueText.ifEmpty { "—" }
                val unit = chart.scrubUnit.takeIf { it.isNotEmpty() }
                BigNumber(text, chart.scrubNumericValue, unit, ink, ink2)
            } else if (metric == StatusMetric.CPU) {
                val cpu = view.cpu
                BigNumber(cpu.valueText.ifEmpty { "—" }, cpu.value, cpu.unit.takeIf { it.isNotEmpty() }, ink, ink2)
            } else {
                val mem = view.memory
                val unit = memCaption.ifEmpty { mem.unit }.takeIf { it.isNotEmpty() }
                BigNumber(mem.valueText.ifEmpty { "—" }, mem.value, unit, ink, ink2)
            }

            Column(horizontalAlignment = Alignment.End) {
                if (metric == StatusMetric.CPU) {
                    val d1 = uiText(view.cpu.detail)
                    val d2 = uiText(view.cpu.detail2)
                    if (d1.isNotEmpty()) Text(highlightNumbers(d1, ink, ink2), style = T.s13.digits(), maxLines = 1)
                    if (d2.isNotEmpty()) Text(d2, style = T.s12, color = ink2, maxLines = 1)
                } else {
                    val pText = uiText(view.memory.pressureText)
                    val tText = uiText(view.memory.totalText)
                    val sText = uiText(view.memory.swapText)
                    if (pText.isNotEmpty()) Text(pText, style = T.s13b, color = ink, maxLines = 1)
                    if (tText.isNotEmpty()) Text(tText, style = T.s12.digits(), color = ink2, maxLines = 1)
                    if (sText.isNotEmpty()) Text(sText, style = T.s12.digits(), color = ink2, maxLines = 1)
                }
            }
        }

        if (chart.visible) {
            Spacer(Modifier.height(12.dp))
            Box(
                Modifier.fillMaxWidth().semantics {
                    if (a11yChart.isNotEmpty()) stateDescription = a11yChart
                    val actions = ArrayList<CustomAccessibilityAction>()
                    if (chart.canStepPrev) {
                        actions.add(CustomAccessibilityAction(prevSampleLabel) {
                            onScrub(chartKind, (chart.scrubIndex - 1).coerceAtLeast(0), false)
                            true
                        })
                    }
                    if (chart.canStepNext) {
                        actions.add(CustomAccessibilityAction(nextSampleLabel) {
                            onScrub(chartKind, chart.scrubIndex + 1, false)
                            true
                        })
                    }
                    if (actions.isNotEmpty()) customActions = actions
                },
            ) {
                StatusChart(
                    chart = chart,
                    accent = accent,
                    onScrub = { idx, clear -> onScrub(chartKind, idx, clear) },
                )
            }
            Row(Modifier.fillMaxWidth().padding(top = 6.dp), horizontalArrangement = Arrangement.SpaceBetween) {
                for (key in listOf(UiMessages.machineStatusAxis2Min(), UiMessages.machineStatusAxis1Min(), UiMessages.machineStatusAxisNow())) {
                    Text(copy(key), style = ts(11.sp).digits(), color = p.ink3)
                }
            }
        }
    }
}

@Composable
private fun TileCard(
    modifier: Modifier,
    onClick: (() -> Unit)?,
    description: String,
    onClickLabel: String? = null,
    card: Boolean = true,
    content: @Composable androidx.compose.foundation.layout.ColumnScope.() -> Unit,
) {
    val p = pal
    Column(
        modifier
            .then(if (card) Modifier.statusCard(p) else Modifier.fillMaxSize())
            .let { if (onClick != null) it.clickable(onClickLabel = onClickLabel, role = Role.Button, onClick = onClick) else it }
            .padding(14.dp)
            .semantics(mergeDescendants = true) { if (description.isNotEmpty()) contentDescription = description },
        content = content,
    )
}

@Composable
private fun LargeFigure(value: String, number: Double, unit: String?, color: Color, unitColor: Color) {
    Row(verticalAlignment = Alignment.Bottom, horizontalArrangement = Arrangement.spacedBy(2.dp)) {
        RollingText(value, number, ts(34.sp, FontWeight.SemiBold, 1.1f).copy(letterSpacing = (-0.6).sp), color, fixedWidth = false)
        if (!unit.isNullOrEmpty()) {
            Text(unit, style = T.s13, color = unitColor, modifier = Modifier.padding(bottom = 5.dp))
        }
    }
}

@Composable
private fun SayLines(first: String, firstColor: Color, second: String?, secondColor: Color, third: String? = null) {
    Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
        if (first.isNotEmpty()) Text(first, style = T.s13.digits(), color = firstColor, maxLines = 2, overflow = TextOverflow.Ellipsis)
        if (!second.isNullOrEmpty()) Text(second, style = T.s12.digits(), color = secondColor, maxLines = 2, overflow = TextOverflow.Ellipsis)
        if (!third.isNullOrEmpty()) Text(third, style = T.s12.digits(), color = secondColor, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

@Composable
private fun MemoryTile(
    view: MachineStatusViewModel,
    dim: Boolean,
    modifier: Modifier,
    card: Boolean = true,
    onClick: () -> Unit,
) {
    val p = pal
    val copy = uiText()
    val mem = view.memory
    val unit = uiText(mem.caption).ifEmpty { mem.unit }.takeIf { it.isNotEmpty() }
    val ink = if (dim) p.ink3 else p.pressureToneInk(mem.pressure, p.ink)
    val ink2 = if (dim) p.ink3 else p.ink2
    TileCard(modifier, onClick, uiText(mem.a11YLabel), card = card) {
        Kick(Lucide.Memory, copy(UiMessages.machineStatusMemory()), ink2)
        Spacer(Modifier.height(6.dp))
        LargeFigure(mem.valueText.ifEmpty { "—" }, mem.value, unit, ink, ink2)
        if (mem.hasPressurePosition) {
            Spacer(Modifier.height(10.dp))
            PressureScale(mem.pressurePosition.toFloat(), Modifier.alpha(if (dim) .5f else 1f))
        }
        Spacer(Modifier.height(10.dp))
        SayLines(
            uiText(mem.pressureText),
            ink,
            uiText(mem.totalText),
            ink2,
            uiText(mem.swapText).takeIf { it.isNotEmpty() },
        )
    }
}

@Composable
private fun CpuTile(
    view: MachineStatusViewModel,
    dim: Boolean,
    modifier: Modifier,
    card: Boolean = true,
    onClick: () -> Unit,
) {
    val p = pal
    val copy = uiText()
    val cpu = view.cpu
    val ink = if (dim) p.ink3 else p.ink
    val ink2 = if (dim) p.ink3 else p.ink2
    TileCard(modifier, onClick, uiText(cpu.a11YLabel), card = card) {
        Kick(Lucide.Cpu, copy(UiMessages.machineStatusCpu()), ink2)
        Spacer(Modifier.height(6.dp))
        LargeFigure(cpu.valueText.ifEmpty { "—" }, cpu.value, cpu.unit.takeIf { it.isNotEmpty() }, ink, ink2)
        Spacer(Modifier.height(10.dp))
        SayLines(uiText(cpu.detail), ink, uiText(cpu.detail2).takeIf { it.isNotEmpty() }, ink2)
    }
}

@Composable
private fun DiskTile(
    view: MachineStatusViewModel,
    dim: Boolean,
    modifier: Modifier,
    onClick: () -> Unit,
) {
    val p = pal
    val copy = uiText()
    val disk = view.disk
    val diskUnit = uiText(disk.caption).ifEmpty { disk.unit }.takeIf { it.isNotEmpty() }
    val ink = if (dim) p.ink3 else p.diskToneInk(disk.state, p.ink)
    val ink2 = if (dim) p.ink3 else p.ink2
    val bad = disk.state == DiskPressureState.DISK_PRESSURE_STATE_CRITICAL
    TileCard(modifier, onClick, uiText(disk.a11YLabel), onClickLabel = copy(UiMessages.machineStatusAgentUsage())) {
        Kick(Lucide.HardDrive, copy(UiMessages.machineStatusDisk()), ink2)
        Spacer(Modifier.height(6.dp))
        LargeFigure(disk.valueText.ifEmpty { "—" }, disk.value, diskUnit, ink, if (dim) p.ink3 else if (bad) p.red else ink2)
        Spacer(Modifier.height(10.dp))
        if (disk.hasUsedFraction) {
            Box(Modifier.alpha(if (dim) .5f else 1f)) { DiskBar(disk.usedFraction.toFloat(), disk.state) }
            Spacer(Modifier.height(10.dp))
        }
        SayLines(
            uiText(disk.stateBadge),
            ink,
            uiText(disk.detail).takeIf { it.isNotEmpty() },
            ink2,
            uiText(disk.detail2).takeIf { it.isNotEmpty() },
        )
    }
}

@Composable
private fun ListRow(text: String, color: Color, value: String?, chevron: Boolean, first: Boolean, onClick: () -> Unit) {
    val p = pal
    Column(Modifier.fillMaxWidth().clickable(role = Role.Button, onClick = onClick)) {
        if (!first) Box(Modifier.padding(start = 16.dp).fillMaxWidth().height(0.5.dp).background(p.sep))
        Row(
            Modifier.fillMaxWidth().heightIn(min = 48.dp).padding(horizontal = 16.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(text, style = T.s15, color = color, modifier = Modifier.weight(1f))
            if (!value.isNullOrEmpty()) Text(value, style = T.s15.digits(), color = p.ink2, modifier = Modifier.padding(end = 6.dp))
            if (chevron) Arrow(p.ink3, T.s15)
        }
    }
}

@Composable
private fun UsageIcon(row: MachineUsageRow, dim: Boolean) {
    val p = pal
    val mark = if (row.agent != Agent.AGENT_UNSPECIFIED) AgentMarks.of(row.agent) else null
    Box(
        Modifier.size(28.dp).clip(CircleShape).background(if (p.dark) Color(0x18FFFFFF) else Color(0x0D000000)),
        contentAlignment = Alignment.Center,
    ) {
        if (row.sessionId.isNotEmpty() && mark != null) {
            androidx.compose.material3.Icon(mark, null, Modifier.size(16.dp), if (dim) p.ink3 else p.ink2)
        } else {
            Ic(Lucide.Terminal, 16.dp, p.ink2)
        }
    }
}

@Composable
private fun UsageList(
    rows: List<MachineUsageRow>,
    dim: Boolean,
    empty: String,
    onSession: (String) -> Unit,
) {
    val p = pal
    Column(Modifier.fillMaxWidth().statusCard(p).padding(vertical = 2.dp)) {
        if (rows.isEmpty()) {
            Text(empty, style = T.s15, color = p.ink3, modifier = Modifier.padding(16.dp))
        } else {
            for (i in rows.indices) {
                val r = rows[i]
                val clickable = r.sessionId.isNotEmpty()
                val title = uiText(r.title).ifEmpty { r.id }
                val sec = uiText(r.secondary)
                val frac = r.cpuBarFraction.toFloat().coerceIn(0f, 1f)
                Row(
                    Modifier
                        .fillMaxWidth()
                        .let { if (clickable) it.clickable(role = Role.Button) { onSession(r.sessionId) } else it }
                        .padding(start = 16.dp, end = 16.dp, top = 12.dp),
                    verticalAlignment = Alignment.Top,
                    horizontalArrangement = Arrangement.spacedBy(12.dp),
                ) {
                    UsageIcon(r, dim)
                    Column(
                        Modifier
                            .weight(1f)
                            .then(
                                if (i < rows.lastIndex) Modifier.drawBehind {
                                    drawLine(p.sep, Offset(0f, size.height), Offset(size.width, size.height), 0.5.dp.toPx())
                                } else Modifier,
                            )
                            .padding(bottom = 12.dp),
                    ) {
                        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                            Text(title, style = T.s15, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                            if (clickable) Arrow(p.ink3, T.s15)
                        }
                        UsageMeter(frac, top = i == 0, dim = dim, secondary = sec, modifier = Modifier.padding(top = 6.dp))
                    }
                }
            }
        }
    }
}

@Composable
private fun HeroSkeleton(metric: StatusMetric, copy: (UiMessage) -> String, modifier: Modifier) {
    val p = pal
    Column(modifier.fillMaxWidth().statusCard(p).padding(16.dp)) {
        Kick(if (metric == StatusMetric.CPU) Lucide.Cpu else Lucide.Memory, copy(if (metric == StatusMetric.CPU) UiMessages.machineStatusCpu() else UiMessages.machineStatusMemory()), p.ink2)
        Spacer(Modifier.height(10.dp))
        Text("—", style = ts(52.sp, FontWeight.SemiBold, 1.05f), color = p.ink3)
        Spacer(Modifier.height(12.dp))
        Box(Modifier.fillMaxWidth().height(92.dp))
        Row(Modifier.fillMaxWidth().padding(top = 6.dp), horizontalArrangement = Arrangement.SpaceBetween) {
            for (key in listOf(UiMessages.machineStatusAxis2Min(), UiMessages.machineStatusAxis1Min(), UiMessages.machineStatusAxisNow())) {
                Text(copy(key), style = ts(11.sp).digits(), color = p.ink3)
            }
        }
    }
}

@Composable
private fun TileSkeleton(icon: ImageVector, label: String, modifier: Modifier) {
    val p = pal
    Column(modifier.fillMaxWidth().statusCard(p).padding(14.dp)) {
        Kick(icon, label, p.ink2)
        Spacer(Modifier.height(6.dp))
        Text("—", style = ts(34.sp, FontWeight.SemiBold), color = p.ink3)
    }
}

/**
 * Pure presentation layer for 电脑状态 driven by Go Core's [MachineStatusViewModel].
 */
@Composable
fun MachineStatusContent(
    view: MachineStatusViewModel,
    main: StatusMetric,
    onMain: (StatusMetric) -> Unit,
    onScrub: (MachineChartKind, Int, Boolean) -> Unit,
    onSelectTab: (MachineUsageTab) -> Unit,
    onBack: () -> Unit,
    onRetry: () -> Unit,
    onOpenStorage: () -> Unit,
    onCleanup: () -> Unit,
    onSession: (String) -> Unit,
    swap: SwapAnimation = remember { SwapAnimation() },
    onOpenUsage: (Agent, String) -> Unit = { _, _ -> },
) {
    val p = pal
    val copy = uiText()
    val noMotion = Motion.reduced
    var prevMetric by remember { mutableStateOf(main) }

    LaunchedEffect(main) {
        if (main == prevMetric) return@LaunchedEffect
        prevMetric = main
        if (noMotion) {
            swap.from = null
            swap.progress.snapTo(1f)
        } else {
            swap.from = swap.bounds.toMap()
            swap.progress.snapTo(0f)
            swap.progress.animateTo(1f, tween(SWAP_MS, easing = androidx.compose.animation.core.LinearEasing))
            swap.from = null
        }
    }

    val activeChart = if (main == StatusMetric.CPU) view.cpu.chart else view.memory.chart
    val headerBadge = if (activeChart.hasScrub) uiText(activeChart.scrubAge) else uiText(view.statusBadge)
    val dim = view.dim
    val other = if (main == StatusMetric.CPU) StatusMetric.MEMORY else StatusMetric.CPU
    val loading = view.pollState == MachinePollState.MACHINE_POLL_STATE_LOADING && !view.cpu.hasValue && !view.memory.hasValue
    val unavailable = (!view.found || !view.supported || view.pollState == MachinePollState.MACHINE_POLL_STATE_UNAVAILABLE) && !view.cpu.hasValue && !view.memory.hasValue

    Column(
        Modifier
            .fillMaxSize()
            .background(p.paper)
            .statusBarsPadding()
            .navigationBarsPadding(),
    ) {
        BackRow(onBack)
        when {
            loading -> {
                Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = 16.dp, vertical = 4.dp)) {
                    StatusHeader(view.machineName, copy(UiMessages.machineStatusLoading()), MachineStatusDot.MACHINE_STATUS_DOT_GRAY_STATIC, p)
                    HeroSkeleton(main, copy, Modifier.padding(top = 18.dp))
                    StatusAdaptiveRow(Modifier.padding(top = 12.dp)) { tile ->
                        TileSkeleton(if (other == StatusMetric.MEMORY) Lucide.Memory else Lucide.Cpu, copy(if (other == StatusMetric.MEMORY) UiMessages.machineStatusMemory() else UiMessages.machineStatusCpu()), tile)
                        TileSkeleton(Lucide.HardDrive, copy(UiMessages.machineStatusDisk()), tile)
                    }
                }
            }
            unavailable -> {
                Column(Modifier.weight(1f).padding(horizontal = 16.dp, vertical = 4.dp)) {
                    StatusHeader(view.machineName, headerBadge, view.statusDot, p)
                    Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
                        Column(horizontalAlignment = Alignment.CenterHorizontally) {
                            Text(
                                uiText(view.unavailableTitle).ifEmpty { copy(UiMessages.machineStatusUnavailable()) },
                                style = T.s17,
                                color = p.ink2,
                                textAlign = TextAlign.Center,
                            )
                            if (view.showRetry) {
                                TextLink(copy(UiMessages.commonRetry()), onRetry, Modifier.padding(horizontal = 12.dp))
                            }
                        }
                    }
                }
            }
            else -> {
                val hero: @Composable (StatusMetric) -> Unit = { which ->
                    HeroCard(view, which, dim, onScrub)
                }
                val tileOf: @Composable (StatusMetric) -> Unit = { which ->
                    if (which == StatusMetric.MEMORY) MemoryTile(view, dim, Modifier.fillMaxWidth(), card = false) { onMain(StatusMetric.MEMORY) }
                    else CpuTile(view, dim, Modifier.fillMaxWidth(), card = false) { onMain(StatusMetric.CPU) }
                }
                val showingSessions = view.topUsage.selectedTab != MachineUsageTab.MACHINE_USAGE_TAB_PROCESSES
                val toggleTab = if (showingSessions) MachineUsageTab.MACHINE_USAGE_TAB_PROCESSES else MachineUsageTab.MACHINE_USAGE_TAB_SESSIONS

                Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = 16.dp, vertical = 4.dp)) {
                    StatusHeader(view.machineName, headerBadge, view.statusDot, p)
                    SwapCard(main, true, swap, Modifier.padding(top = 18.dp)) { asHero -> if (asHero) hero(main) else tileOf(main) }
                    StatusAdaptiveRow(Modifier.padding(top = 12.dp)) { tile ->
                        SwapCard(other, false, swap, tile) { asHero -> if (asHero) hero(other) else tileOf(other) }
                        DiskTile(view, dim, tile, onOpenStorage)
                    }
                    if (view.disk.showActions) {
                        Column(Modifier.padding(top = 12.dp).fillMaxWidth().statusCard(p)) {
                            ListRow(uiText(view.disk.cleanupActionLabel).ifEmpty { copy(UiMessages.machineStatusCleanupAction()) }, p.blue, null, false, true, onCleanup)
                            ListRow(uiText(view.disk.agentUsageLabel).ifEmpty { copy(UiMessages.machineStatusAgentUsage()) }, p.ink, uiText(view.disk.agentUsageValue).takeIf { it.isNotEmpty() }, true, false, onOpenStorage)
                        }
                    }
                    Row(Modifier.fillMaxWidth().padding(top = 24.dp, bottom = 10.dp), verticalAlignment = Alignment.CenterVertically) {
                        Text(copy(UiMessages.machineStatusTopUsage()), style = T.h20, color = p.ink, modifier = Modifier.weight(1f).semantics { heading() })
                        Box(Modifier.heightIn(min = 44.dp).clip(Radii.R14).clickable(role = Role.Button) { onSelectTab(toggleTab) }.padding(horizontal = 6.dp), contentAlignment = Alignment.Center) {
                            Text(copy(if (showingSessions) UiMessages.machineStatusShowProcesses() else UiMessages.machineStatusShowSessions()), style = T.s15, color = p.blue)
                        }
                    }
                    UsageList(
                        view.topUsage.rowsList,
                        dim,
                        uiText(view.topUsage.emptyText).ifEmpty { copy(UiMessages.machineStatusNoRunningSessions()) },
                        onSession,
                    )
                    if (view.hasModelUsage() && view.modelUsage.supported) {
                        ModelUsagePreviewSection(
                            mu = view.modelUsage,
                            dim = dim,
                            onOpenUsage = onOpenUsage,
                        )
                    }
                    val footer = listOf(uiText(view.uptimeText), uiText(view.batteryText)).filter { it.isNotEmpty() }.joinToString(" · ")
                    if (footer.isNotEmpty()) {
                        Text(footer, style = T.s13.digits(), color = p.ink3, modifier = Modifier.padding(top = 14.dp, bottom = 18.dp))
                    } else {
                        Spacer(Modifier.height(18.dp))
                    }
                }
            }
        }
    }
}

/** 电脑状态 destination: watches Core's 2s machine status stream while started and renders [MachineStatusViewModel]. */
@Composable
fun MachineStatusScreen(id: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val owner = androidx.lifecycle.compose.LocalLifecycleOwner.current
    val initialView = remember(remote, id) { remote.coreHost.snapshotMachineStatusView(id) }
    val rawView by remote.machineStatusView.collectAsState()
    val view = if (rawView.machineId == id) rawView else initialView
    var main by remember { mutableStateOf(StatusMetric.CPU) }

    DisposableEffect(id, owner) {
        val obs = androidx.lifecycle.LifecycleEventObserver { _, ev ->
            when (ev) {
                androidx.lifecycle.Lifecycle.Event.ON_START ->
                    remote.coreHost.watchMachineStatus(id, true, MachinePollMode.MACHINE_POLL_MODE_LIVE)
                androidx.lifecycle.Lifecycle.Event.ON_STOP ->
                    remote.coreHost.watchMachineStatus(id, false, MachinePollMode.MACHINE_POLL_MODE_LIVE)
                else -> {}
            }
        }
        owner.lifecycle.addObserver(obs)
        if (owner.lifecycle.currentState.isAtLeast(androidx.lifecycle.Lifecycle.State.STARTED)) {
            remote.coreHost.watchMachineStatus(id, true, MachinePollMode.MACHINE_POLL_MODE_LIVE)
        }
        onDispose {
            owner.lifecycle.removeObserver(obs)
            remote.coreHost.watchMachineStatus(id, false, MachinePollMode.MACHINE_POLL_MODE_LIVE)
        }
    }

    MachineStatusContent(
        view = view,
        main = main,
        onMain = { main = it },
        onScrub = { kind, index, clear -> remote.coreHost.scrubMachineStatus(id, kind, index, clear) },
        onSelectTab = { tab -> remote.coreHost.selectMachineStatusTab(id, tab) },
        onBack = nav.back,
        onRetry = { remote.coreHost.retryMachineStatus(id) },
        onOpenStorage = { nav.deviceStorage(id) },
        onCleanup = { nav.cleanup(id) },
        onSession = nav.session,
        onOpenUsage = { agent, model -> nav.openUsage(id, agent, model) },
    )
}

@Composable
private fun ModelUsagePreviewSection(
    mu: MachineModelUsagePreview,
    dim: Boolean,
    onOpenUsage: (Agent, String) -> Unit,
) {
    val copy = uiText()
    val p = pal
    Row(Modifier.fillMaxWidth().padding(top = 24.dp, bottom = 10.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(
            copy(UiMessages.machineStatusModelUsageTitle()),
            style = T.h20,
            color = p.ink,
            modifier = Modifier.weight(1f).semantics { heading() },
        )
        Box(
            Modifier.heightIn(min = 44.dp)
                .clip(Radii.R14)
                .clickable(role = Role.Button) { onOpenUsage(Agent.AGENT_UNSPECIFIED, "") }
                .padding(horizontal = 6.dp),
            contentAlignment = Alignment.Center,
        ) {
            Text(copy(UiMessages.machineStatusModelUsageViewTrends()), style = T.s15, color = p.blue)
        }
    }
    Column(Modifier.fillMaxWidth().statusCard(p).padding(vertical = 2.dp)) {
        if (mu.hasData && mu.rowsList.isNotEmpty()) {
            mu.rowsList.forEachIndexed { idx, row ->
                if (idx > 0) {
                    Box(Modifier.padding(start = 54.dp).fillMaxWidth().height(0.5.dp).background(p.sep))
                }
                val meta = uiText(row.metaText)
                val tokenStr = uiText(row.totalTokensText)
                val pairLabel = uiText(row.titleText)
                Row(
                    Modifier.fillMaxWidth()
                        .clickable(role = Role.Button) { onOpenUsage(row.agent, row.model) }
                        .padding(horizontal = 16.dp, vertical = 12.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(10.dp),
                ) {
                    Box(
                        Modifier.size(28.dp).clip(CircleShape).background(if (p.dark) Color(0x18FFFFFF) else Color(0x0D000000)),
                        contentAlignment = Alignment.Center,
                    ) {
                        AgentMark(row.agent, T.s15, if (dim) p.ink3 else p.ink)
                    }
                    Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                            Text(
                                pairLabel,
                                style = T.s15b,
                                color = p.ink,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                                modifier = Modifier.weight(1f),
                            )
                            Text(
                                tokenStr,
                                style = T.s15.digits(),
                                color = p.ink,
                                modifier = Modifier.padding(start = 8.dp),
                            )
                        }
                        Text(meta, style = T.s13.digits(), color = p.ink2)
                    }
                }
            }
        } else {
            val emptyStr = uiText(mu.emptyText).ifEmpty { copy(UiMessages.machineStatusModelUsageEmpty()) }
            Text(emptyStr, style = T.s15, color = p.ink3, modifier = Modifier.padding(16.dp))
        }
    }
    val unavail = if (mu.hasUnsupportedFootnote()) uiText(mu.unsupportedFootnote) else ""
    if (unavail.isNotEmpty()) {
        Text(unavail, style = T.s13, color = p.ink2, modifier = Modifier.padding(top = 8.dp))
    }
}
