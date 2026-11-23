package io.github.lewin671.remoteagent.ui

import android.graphics.Paint
import androidx.compose.animation.core.Animatable
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.DatePickerDialog
import androidx.compose.material3.DateRangePicker
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.SelectableDates
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.rememberDateRangePickerState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.drawIntoCanvas
import androidx.compose.ui.graphics.nativeCanvas
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.layout
import androidx.compose.ui.layout.onPlaced
import androidx.compose.ui.layout.positionInParent
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.MessageResolver
import io.github.lewin671.remoteagent.core.USAGE_RETENTION_DAYS
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.clampUsageCustomRange
import io.github.lewin671.remoteagent.core.proto.UsageBannerKind
import io.github.lewin671.remoteagent.core.proto.UsageBannerView
import io.github.lewin671.remoteagent.core.proto.UsageBarBucketView
import io.github.lewin671.remoteagent.core.proto.UsageEmptyKind
import io.github.lewin671.remoteagent.core.proto.UsageHeroView
import io.github.lewin671.remoteagent.core.proto.UsageMetricStripView
import io.github.lewin671.remoteagent.core.proto.UsageSortColumn
import io.github.lewin671.remoteagent.core.proto.UsageViewModel
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.UsageBucketGranularity
import io.github.lewin671.remoteagent.proto.UsageRangePreset
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId
import java.time.ZoneOffset
import kotlin.math.max
import kotlin.math.round
import kotlin.math.roundToInt

private const val DAY_MS = 86_400_000L
private const val EN_DASH = "–"

enum class UsageTickAlign { START, CENTER, END }

data class UsageAxisTick(
    val pct: Float,
    val label: String,
    val align: UsageTickAlign,
)

private fun effectiveGranularity(
    granularity: UsageBucketGranularity,
    preset: UsageRangePreset,
): UsageBucketGranularity =
    if (granularity != UsageBucketGranularity.USAGE_BUCKET_GRANULARITY_UNSPECIFIED) granularity
    else when (preset) {
        UsageRangePreset.USAGE_RANGE_PRESET_TODAY -> UsageBucketGranularity.USAGE_BUCKET_GRANULARITY_HOUR
        UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS -> UsageBucketGranularity.USAGE_BUCKET_GRANULARITY_SIX_HOURS
        else -> UsageBucketGranularity.USAGE_BUCKET_GRANULARITY_DAY
    }

private fun mdLabel(m: Int, d: Int): String = listOf(m, d).joinToString("/")

fun buildUsageAxisTicks(
    granularity: UsageBucketGranularity,
    buckets: List<UsageBarBucketView>,
    startMs: Long,
    endMs: Long,
    preset: UsageRangePreset,
    axis24h: String,
    zone: ZoneId = ZoneId.systemDefault(),
    nowMs: Long = System.currentTimeMillis(),
): List<UsageAxisTick> {
    val gran = effectiveGranularity(granularity, preset)
    if (gran == UsageBucketGranularity.USAGE_BUCKET_GRANULARITY_HOUR) {
        return listOf(
            UsageAxisTick(0f, "0", UsageTickAlign.START),
            UsageAxisTick(25f, "6", UsageTickAlign.CENTER),
            UsageAxisTick(50f, "12", UsageTickAlign.CENTER),
            UsageAxisTick(75f, "18", UsageTickAlign.CENTER),
            UsageAxisTick(100f, axis24h, UsageTickAlign.END),
        )
    }

    val todayStart = Instant.ofEpochMilli(nowMs).atZone(zone).toLocalDate()
    val fallbackDays = if (preset == UsageRangePreset.USAGE_RANGE_PRESET_THIRTY_DAYS) 30 else 7
    val effStartMs = when {
        startMs > 0L -> startMs
        buckets.isNotEmpty() && buckets[0].startMs > 0L -> buckets[0].startMs
        else -> todayStart.minusDays((fallbackDays - 1).toLong()).atStartOfDay(zone).toInstant().toEpochMilli()
    }

    if (gran == UsageBucketGranularity.USAGE_BUCKET_GRANULARITY_SIX_HOURS) {
        val n = if (buckets.isNotEmpty()) buckets.size else 28
        val days = max(1, round(n / 4.0).toInt())
        val out = ArrayList<UsageAxisTick>(days)
        var prevMonth = -1
        val startDate = Instant.ofEpochMilli(effStartMs).atZone(zone).toLocalDate()
        for (d in 0 until days) {
            val idx = d * 4
            val dt = if (idx < buckets.size && buckets[idx].startMs > 0L) {
                Instant.ofEpochMilli(buckets[idx].startMs).atZone(zone).toLocalDate()
            } else {
                startDate.plusDays(d.toLong())
            }
            val m = dt.monthValue
            val day = dt.dayOfMonth
            val lbl = if (d == 0 || m != prevMonth) mdLabel(m, day) else day.toString()
            prevMonth = m
            out.add(
                UsageAxisTick(
                    pct = ((d * 4 + 2).toFloat() / n.toFloat()) * 100f,
                    label = lbl,
                    align = UsageTickAlign.CENTER,
                ),
            )
        }
        return out
    }

    val derivedDays =
        if (endMs > startMs && startMs > 0L) max(1, round((endMs - startMs).toDouble() / DAY_MS.toDouble()).toInt())
        else fallbackDays
    val n = if (buckets.isNotEmpty()) buckets.size else derivedDays
    val startDate = Instant.ofEpochMilli(effStartMs).atZone(zone).toLocalDate()
    if (n <= 1) {
        return listOf(UsageAxisTick(50f, mdLabel(startDate.monthValue, startDate.dayOfMonth), UsageTickAlign.CENTER))
    }

    val lastDate = when {
        buckets.isNotEmpty() && buckets.last().startMs > 0L ->
            Instant.ofEpochMilli(buckets.last().startMs).atZone(zone).toLocalDate()
        endMs > 0L -> Instant.ofEpochMilli(endMs - 1L).atZone(zone).toLocalDate()
        else -> startDate.plusDays((n - 1).toLong())
    }
    val tickCount = when {
        n <= 4 -> n
        n <= 18 -> 4
        else -> 5
    }
    val out = ArrayList<UsageAxisTick>(tickCount)
    for (k in 0 until tickCount) {
        val last = k == tickCount - 1
        val posIdx = if (last) n else round(k.toDouble() * n / (tickCount - 1)).toInt()
        val dt = when {
            last -> lastDate
            posIdx < buckets.size && buckets[posIdx].startMs > 0L ->
                Instant.ofEpochMilli(buckets[posIdx].startMs).atZone(zone).toLocalDate()
            else -> startDate.plusDays(posIdx.toLong())
        }
        val align = when (k) {
            0 -> UsageTickAlign.START
            tickCount - 1 -> UsageTickAlign.END
            else -> UsageTickAlign.CENTER
        }
        out.add(
            UsageAxisTick(
                pct = (posIdx.toFloat() / n.toFloat()) * 100f,
                label = mdLabel(dt.monthValue, dt.dayOfMonth),
                align = align,
            ),
        )
    }
    return out
}

fun formatUsageBucketLabel(
    startMs: Long,
    endMs: Long,
    granularity: UsageBucketGranularity,
    copy: MessageResolver,
    zone: ZoneId = ZoneId.systemDefault(),
): String {
    val s = Instant.ofEpochMilli(startMs).atZone(zone)
    val e = Instant.ofEpochMilli(if (endMs > startMs) endMs else startMs + 3_600_000L).atZone(zone)
    val startH = s.hour
    var endH = e.hour
    if (endH <= startH && endMs > startMs) endH = 24
    return when (granularity) {
        UsageBucketGranularity.USAGE_BUCKET_GRANULARITY_HOUR ->
            copy(UiMessages.usageScrubHourRange(end = endH, start = startH))
        UsageBucketGranularity.USAGE_BUCKET_GRANULARITY_SIX_HOURS ->
            copy(UiMessages.usageScrubDayHourRange(day = s.dayOfMonth, end = endH, month = s.monthValue, start = startH))
        else ->
            copy(UiMessages.usageScrubDay(day = s.dayOfMonth, month = s.monthValue))
    }
}

fun formatUsageCustomRange(
    startMs: Long,
    inclusiveEndMs: Long,
    copy: MessageResolver,
    zone: ZoneId = ZoneId.systemDefault(),
): String {
    val s = Instant.ofEpochMilli(startMs).atZone(zone).toLocalDate()
    val e = Instant.ofEpochMilli(inclusiveEndMs).atZone(zone).toLocalDate()
    return when {
        s == e -> copy(UiMessages.usageCustomRangeSingleDay(day = s.dayOfMonth, month = s.monthValue, year = s.year))
        s.year == e.year -> copy(
            UiMessages.usageCustomRangeSameYear(
                endDay = e.dayOfMonth,
                endMonth = e.monthValue,
                startDay = s.dayOfMonth,
                startMonth = s.monthValue,
                year = s.year,
            ),
        )
        else -> copy(
            UiMessages.usageCustomRangeCrossYear(
                endDay = e.dayOfMonth,
                endMonth = e.monthValue,
                endYear = e.year,
                startDay = s.dayOfMonth,
                startMonth = s.monthValue,
                startYear = s.year,
            ),
        )
    }
}

@Composable
fun UsageScreen(
    machineId: String,
    focusAgent: Agent = Agent.AGENT_UNSPECIFIED,
    focusModel: String = "",
) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val initialView = remember(remote, machineId, focusAgent, focusModel) {
        remote.coreHost.snapshotUsageView(
            machineId = machineId,
            focusAgent = focusAgent,
            focusModel = focusModel,
        )
    }
    val rawView by remote.usageView.collectAsState()
    val view = if (rawView.machineId == machineId) rawView else initialView

    LaunchedEffect(remote, machineId, focusAgent, focusModel) {
        remote.coreHost.openUsageScreen(
            machineId = machineId,
            focusAgent = focusAgent,
            focusModel = focusModel,
            preset = UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS,
            applyFocus = true,
        )
    }

    UsageScreenContent(
        vm = view,
        onBack = nav.back,
        onSelectPreset = { preset ->
            remote.coreHost.openUsageScreen(
                machineId = machineId,
                focusAgent = focusAgent,
                focusModel = focusModel,
                preset = preset,
                applyFocus = false,
            )
        },
        onSelectCustomRange = { startMs, endMs ->
            remote.coreHost.openUsageScreen(
                machineId = machineId,
                focusAgent = focusAgent,
                focusModel = focusModel,
                preset = UsageRangePreset.USAGE_RANGE_PRESET_CUSTOM,
                customStartMs = startMs,
                customEndMs = endMs,
                applyFocus = false,
            )
        },
        onSelectSort = { remote.coreHost.selectUsageSort(machineId, it) },
        onSelectSeries = { key, toggle -> remote.coreHost.selectUsageSeries(machineId, key, toggle) },
        onScrub = { idx, clear -> remote.coreHost.scrubUsage(machineId, idx, clear) },
        onRetry = { remote.coreHost.retryUsageScreen(machineId) },
    )
}

@Composable
fun UsageScreenContent(
    vm: UsageViewModel,
    onBack: () -> Unit = {},
    onSelectPreset: (UsageRangePreset) -> Unit = {},
    onSelectCustomRange: (Long, Long) -> Unit = { _, _ -> },
    onSelectSort: (UsageSortColumn) -> Unit = {},
    onSelectSeries: (String, Boolean) -> Unit = { _, _ -> },
    onScrub: (Int, Boolean) -> Unit = { _, _ -> },
    onRetry: () -> Unit = {},
) {
    val copy = uiText()
    val p = pal
    val preset =
        if (vm.preset == UsageRangePreset.USAGE_RANGE_PRESET_UNSPECIFIED) UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS
        else vm.preset

    var showSkeleton by remember(vm.machineId) { mutableStateOf(false) }
    LaunchedEffect(vm.loading, vm.hasAnyUsage) {
        if (!vm.loading || vm.hasAnyUsage) {
            showSkeleton = false
        } else {
            delay(200)
            showSkeleton = true
        }
    }

    val clampedDefault = remember(vm.startMs, vm.endMs) {
        val rawEnd = if (vm.endMs > vm.startMs && vm.endMs > 0L) vm.endMs - 1L else vm.endMs
        clampUsageCustomRange(vm.startMs, rawEnd)
    }
    var customPickerOpen by remember { mutableStateOf(false) }

    val subtitle = uiText(vm.subtitle)

    val ranges = listOf(
        UsageRangePreset.USAGE_RANGE_PRESET_TODAY to copy(UiMessages.usageRangeToday()),
        UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS to copy(UiMessages.usageRange7D()),
        UsageRangePreset.USAGE_RANGE_PRESET_THIRTY_DAYS to copy(UiMessages.usageRange30D()),
        UsageRangePreset.USAGE_RANGE_PRESET_CUSTOM to copy(UiMessages.usageRangeCustom()),
    )
    val selectedRangeIdx = ranges.indexOfFirst { it.first == preset }.coerceAtLeast(0)

    val handleSelectRangeIdx: (Int) -> Unit = { idx ->
        val chosen = ranges[idx].first
        if (chosen == UsageRangePreset.USAGE_RANGE_PRESET_CUSTOM) {
            if (preset == UsageRangePreset.USAGE_RANGE_PRESET_CUSTOM) {
                customPickerOpen = true
            } else {
                onSelectPreset(chosen)
                customPickerOpen = true
            }
        } else {
            onSelectPreset(chosen)
        }
    }

    BoxWithConstraints(Modifier.fillMaxSize().background(p.paper).testTag("usage-page")) {
        val isWide = maxWidth >= 900.dp

        Column(Modifier.fillMaxSize()) {
            if (isWide) {
                WideUsageToolbar(
                    title = copy(UiMessages.usageTitle()),
                    subtitle = subtitle,
                    onBack = onBack,
                    customDateText = if (preset == UsageRangePreset.USAGE_RANGE_PRESET_CUSTOM) {
                        formatUsageCustomRange(clampedDefault.first, clampedDefault.second, copy)
                    } else null,
                    onOpenCustomDate = { customPickerOpen = true },
                    rangeLabels = ranges.map { it.second },
                    selectedRangeIdx = selectedRangeIdx,
                    onSelectRangeIdx = handleSelectRangeIdx,
                )
            } else {
                PageBar(
                    title = copy(UiMessages.usageTitle()),
                    onBack = onBack,
                    subtitle = subtitle.ifEmpty { null },
                )
            }

            if (isWide && vm.found && vm.supported && vm.hasAnyUsage) {
                val leftScroll = rememberScrollState()
                val rightScroll = rememberScrollState()
                Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.TopCenter) {
                    Column(Modifier.widthIn(max = 1120.dp).fillMaxSize().padding(horizontal = 28.dp)) {
                        UsageBanners(vm.bannersList, onRetry, copy, Modifier.padding(top = 8.dp))
                        Row(
                            Modifier.weight(1f).fillMaxWidth().padding(top = 6.dp),
                            horizontalArrangement = Arrangement.spacedBy(48.dp),
                            verticalAlignment = Alignment.Top,
                        ) {
                            Column(
                                Modifier.weight(0.54f).widthIn(max = 600.dp)
                                    .verticalScroll(leftScroll)
                                    .padding(bottom = 22.dp),
                            ) {
                                UsageHeroBlock(
                                    vm = vm,
                                    isWide = true,
                                    copy = copy,
                                    onClearSeries = { onSelectSeries("", false) },
                                )
                                Spacer(Modifier.height(16.dp))
                                UsageChartsStack(
                                    vm = vm,
                                    isWide = true,
                                    copy = copy,
                                    onScrub = onScrub,
                                )
                            }
                            Column(
                                Modifier.weight(0.46f)
                                    .verticalScroll(rightScroll)
                                    .padding(top = 20.dp, bottom = 22.dp),
                            ) {
                                UsageModelTable(
                                    vm = vm,
                                    isWide = true,
                                    copy = copy,
                                    onSelectSort = onSelectSort,
                                    onSelectSeries = { key, _ -> onSelectSeries(key, true) },
                                )
                            }
                        }
                    }
                }
            } else {
                val scrollState = rememberScrollState()
                val scope = rememberCoroutineScope()
                Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.TopCenter) {
                    Column(
                        Modifier
                            .widthIn(max = 560.dp)
                            .fillMaxSize()
                            .verticalScroll(scrollState),
                    ) {
                        Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp)) {
                            if (!isWide) {
                                Spacer(Modifier.height(6.dp))
                                UsageRangeSegmented(
                                    items = ranges.map { it.second },
                                    selected = selectedRangeIdx,
                                    onSelect = handleSelectRangeIdx,
                                    height = 32.dp,
                                    modifier = Modifier.fillMaxWidth(),
                                )
                                if (preset == UsageRangePreset.USAGE_RANGE_PRESET_CUSTOM) {
                                    Spacer(Modifier.height(8.dp))
                                    Box(Modifier.fillMaxWidth(), contentAlignment = Alignment.Center) {
                                        UsageCustomRangeTrigger(
                                            text = formatUsageCustomRange(clampedDefault.first, clampedDefault.second, copy),
                                            onClick = { customPickerOpen = true },
                                        )
                                    }
                                }
                            }

                            UsageBanners(vm.bannersList, onRetry, copy, Modifier.padding(top = 8.dp))

                            when (vm.emptyKind) {
                                UsageEmptyKind.USAGE_EMPTY_KIND_LOADING -> {
                                    if (showSkeleton) UsageLoadingSkeleton()
                                }
                                UsageEmptyKind.USAGE_EMPTY_KIND_NOT_FOUND -> {
                                    PageNote(uiText(vm.emptyTitle).ifEmpty { copy(UiMessages.settingsAndroidThisComputerIsOutsideTheCurrentTrustedDirectoryCheckThe()) })
                                }
                                UsageEmptyKind.USAGE_EMPTY_KIND_UNSUPPORTED,
                                UsageEmptyKind.USAGE_EMPTY_KIND_OFFLINE_EMPTY,
                                UsageEmptyKind.USAGE_EMPTY_KIND_LOAD_ERROR,
                                UsageEmptyKind.USAGE_EMPTY_KIND_ALL_SOURCES_UNAVAILABLE -> {
                                    Spacer(Modifier.height(14.dp))
                                    val msg = listOf(uiText(vm.emptyTitle), uiText(vm.emptyBody))
                                        .filter { it.isNotEmpty() }
                                        .joinToString("\n")
                                    val tone = when (vm.emptyKind) {
                                        UsageEmptyKind.USAGE_EMPTY_KIND_LOAD_ERROR -> NoticeTone.ERROR
                                        UsageEmptyKind.USAGE_EMPTY_KIND_UNSUPPORTED,
                                        UsageEmptyKind.USAGE_EMPTY_KIND_OFFLINE_EMPTY -> NoticeTone.WARN
                                        else -> NoticeTone.PLAIN
                                    }
                                    Notice(
                                        msg,
                                        tone = tone,
                                        action = if (vm.emptyCanRetry) copy(UiMessages.commonRetry()) else null,
                                        onAction = if (vm.emptyCanRetry) onRetry else null,
                                    )
                                }
                                UsageEmptyKind.USAGE_EMPTY_KIND_NO_RECORDS -> {
                                    UsageEmptyRecordsView(vm = vm, preset = preset, isWide = false, copy = copy)
                                }
                                else -> {
                                    if (vm.hasAnyUsage) {
                                        UsageHeroBlock(
                                            vm = vm,
                                            isWide = false,
                                            copy = copy,
                                            onClearSeries = { onSelectSeries("", false) },
                                        )
                                        Spacer(Modifier.height(16.dp))
                                        UsageChartsStack(
                                            vm = vm,
                                            isWide = false,
                                            copy = copy,
                                            onScrub = onScrub,
                                        )
                                    }
                                }
                            }
                        }

                        if (vm.found && vm.supported && vm.hasAnyUsage) {
                            Spacer(Modifier.height(22.dp))
                            UsageModelTable(
                                vm = vm,
                                isWide = false,
                                copy = copy,
                                onSelectSort = onSelectSort,
                                onSelectSeries = { key, selecting ->
                                    onSelectSeries(key, true)
                                    if (selecting) {
                                        scope.launch { scrollState.animateScrollTo(0) }
                                    }
                                },
                            )
                        }
                        Spacer(Modifier.height(24.dp))
                    }
                }
            }
        }
    }

    if (customPickerOpen) {
        UsageCustomRangeDialog(
            initialStartMs = clampedDefault.first,
            initialEndMs = clampedDefault.second,
            copy = copy,
            onApply = { s, e ->
                customPickerOpen = false
                onSelectCustomRange(s, e)
            },
            onDismiss = { customPickerOpen = false },
        )
    }
}

@Composable
private fun WideUsageToolbar(
    title: String,
    subtitle: String,
    onBack: () -> Unit,
    customDateText: String?,
    onOpenCustomDate: () -> Unit,
    rangeLabels: List<String>,
    selectedRangeIdx: Int,
    onSelectRangeIdx: (Int) -> Unit,
) {
    val p = pal
    val backLabel = uiText(UiMessages.commonBack())
    Column(Modifier.fillMaxWidth()) {
        Row(
            Modifier.fillMaxWidth().height(52.dp).padding(horizontal = 20.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            Box(
                Modifier.size(28.dp).clip(CircleShape).background(p.fill)
                    .clickable(role = Role.Button, onClickLabel = backLabel, onClick = onBack),
                contentAlignment = Alignment.Center,
            ) {
                Ic(Lucide.ChevronLeft, 18.dp, p.ink)
            }
            Text(title, style = T.s15b, color = p.ink, maxLines = 1)
            Box(Modifier.weight(1f), contentAlignment = Alignment.CenterStart) {
                if (subtitle.isNotEmpty()) {
                    Text(subtitle, style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            }
            if (customDateText != null) {
                UsageCustomRangeTrigger(text = customDateText, onClick = onOpenCustomDate)
            }
            UsageRangeSegmented(
                items = rangeLabels,
                selected = selectedRangeIdx,
                onSelect = onSelectRangeIdx,
                height = 30.dp,
                modifier = Modifier.width(300.dp),
            )
        }
        Box(Modifier.fillMaxWidth().height(0.5.dp).background(p.sep))
    }
}

@Composable
private fun UsageRangeSegmented(
    items: List<String>,
    selected: Int,
    onSelect: (Int) -> Unit,
    height: androidx.compose.ui.unit.Dp,
    modifier: Modifier = Modifier,
) {
    val p = pal
    val outerShape = RoundedCornerShape(9.dp)
    val innerShape = RoundedCornerShape(7.dp)
    val bounds = remember { mutableStateMapOf<Int, IntRange>() }
    val density = LocalDensity.current
    val target = bounds[selected]
    val animX = remember { Animatable(0f) }
    val animW = remember { Animatable(0f) }
    var placed by remember { mutableStateOf(false) }
    val frozen = Motion.reduced || LocalChartFrozen.current

    LaunchedEffect(target, frozen) {
        if (target == null) return@LaunchedEffect
        val left = target.first.toFloat()
        val width = (target.last - target.first + 1).toFloat()
        if (!placed || frozen) {
            animX.snapTo(left)
            animW.snapTo(width)
            placed = true
        } else {
            launch { animX.animateTo(left, Motion.enter(Motion.BASE)) }
            launch { animW.animateTo(width, Motion.enter(Motion.BASE)) }
        }
    }

    Box(
        modifier
            .heightIn(min = height)
            .clip(outerShape)
            .background(p.fill)
            .padding(2.dp),
    ) {
        if (placed && animW.value > 0f) {
            Box(
                Modifier
                    .offset { IntOffset(animX.value.roundToInt(), 0) }
                    .width(with(density) { animW.value.toDp() })
                    .height(height - 4.dp)
                    .shadow(1.dp, innerShape, clip = false, ambientColor = p.glassShadow, spotColor = p.glassShadow)
                    .clip(innerShape)
                    .background(p.raised)
                    .border(0.5.dp, p.sep, innerShape),
            )
        }
        Row(Modifier.selectableGroup(), verticalAlignment = Alignment.CenterVertically) {
            items.forEachIndexed { idx, label ->
                val on = idx == selected
                Box(
                    Modifier
                        .weight(1f)
                        .heightIn(min = height - 4.dp)
                        .onPlaced { coords ->
                            val x = coords.positionInParent().x.roundToInt()
                            bounds[idx] = x until (x + coords.size.width)
                        }
                        .clip(innerShape)
                        .selectable(selected = on, role = Role.Tab) { onSelect(idx) }
                        .padding(horizontal = 4.dp),
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        label,
                        style = if (on) T.s13b else T.s13.copy(fontWeight = platformWeight(FontWeight.Medium)),
                        color = if (on) p.ink else p.ink2,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
            }
        }
    }
}

@Composable
private fun UsageCustomRangeTrigger(text: String, onClick: () -> Unit) {
    val p = pal
    Row(
        Modifier
            .height(32.dp)
            .clip(Radii.R6)
            .clickable(role = Role.Button, onClick = onClick)
            .padding(horizontal = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        Text(text, style = T.s13.copy(fontWeight = platformWeight(FontWeight.Medium)).digits(), color = p.blue, maxLines = 1)
        Ic(Lucide.ChevronDown, 14.dp, p.blue)
    }
}

@Composable
private fun UsageBanners(
    banners: List<UsageBannerView>,
    onRetry: () -> Unit,
    copy: MessageResolver,
    modifier: Modifier = Modifier,
) {
    if (banners.isEmpty()) return
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        for (banner in banners) {
            val text = uiText(banner.text)
            if (text.isEmpty()) continue
            val tone = when {
                banner.isError -> NoticeTone.ERROR
                banner.kind == UsageBannerKind.USAGE_BANNER_KIND_OFFLINE_CACHED -> NoticeTone.WARN
                else -> NoticeTone.PLAIN
            }
            Notice(
                text,
                tone = tone,
                action = if (banner.canRetry) copy(UiMessages.commonRetry()) else null,
                onAction = if (banner.canRetry) onRetry else null,
            )
        }
    }
}

@Composable
private fun UsageHeroBlock(
    vm: UsageViewModel,
    isWide: Boolean,
    copy: MessageResolver,
    onClearSeries: () -> Unit,
) {
    val p = pal
    val hero = if (vm.hasHero()) vm.hero else UsageHeroView.getDefaultInstance()
    val comp = if (hero.hasComposition()) hero.composition else null
    val compRowHeight = if (isWide) 18 else 17
    val minCompHeight = (max(0, hero.periodCompositionRows) * compRowHeight + 5).dp
    val topMargin = if (isWide) 14.dp else 18.dp
    val compWidth = if (isWide) 168.dp else 132.dp
    val bigSize = if (isWide) 52.sp else 44.sp

    Spacer(Modifier.height(topMargin))

    // Scope line
    Row(
        Modifier.fillMaxWidth().heightIn(min = 18.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(5.dp),
    ) {
        when {
            hero.scrubbing -> {
                val bucketLabel = formatUsageBucketLabel(hero.scrubStartMs, hero.scrubEndMs, vm.granularity, copy)
                Text(
                    bucketLabel,
                    style = T.s12.copy(fontWeight = platformWeight(FontWeight.Medium)).digits(),
                    color = p.ink,
                    maxLines = 1,
                )
                if (hero.hasSelectedModel) {
                    val modelSuffix = "· " + hero.selectedModel
                    Text(
                        modelSuffix,
                        style = T.s12,
                        color = p.ink2,
                        maxLines = 1,
                        overflow = TextOverflow.MiddleEllipsis,
                        modifier = Modifier.weight(1f, fill = false),
                    )
                }
            }
            hero.hasSelectedModel -> {
                UsageAgentMarkIcon(hero.selectedAgent, size = 13.dp, tint = p.ink2)
                Text(
                    hero.selectedModel,
                    style = T.s12.copy(fontWeight = platformWeight(FontWeight.Medium)),
                    color = p.ink,
                    maxLines = 1,
                    overflow = TextOverflow.MiddleEllipsis,
                    modifier = Modifier.weight(1f, fill = false),
                )
                Box(
                    Modifier
                        .size(18.dp)
                        .clip(CircleShape)
                        .background(p.fill2)
                        .semantics { contentDescription = copy(UiMessages.usageScopeClearA11Y()) }
                        .clickable(
                            role = Role.Button,
                            onClick = onClearSeries,
                        ),
                    contentAlignment = Alignment.Center,
                ) {
                    Ic(Lucide.X, 10.dp, p.ink)
                }
            }
            else -> {
                Text(
                    uiText(hero.scopeText),
                    style = T.s12,
                    color = p.ink2,
                    maxLines = 1,
                )
            }
        }
    }

    Spacer(Modifier.height(2.dp))

    Row(
        Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(12.dp),
        verticalAlignment = Alignment.Top,
    ) {
        Column(Modifier.weight(1f)) {
            val bigText = uiText(hero.totalTokensText)
            val unitText = copy(UiMessages.usageKpiUnitTokens())
            Text(
                text = buildAnnotatedString {
                    withStyle(
                        SpanStyle(
                            fontSize = bigSize,
                            fontWeight = platformWeight(FontWeight.Bold),
                            letterSpacing = (-0.8).sp,
                            color = p.ink,
                        ),
                    ) {
                        append(bigText)
                    }
                    append(" ")
                    withStyle(
                        SpanStyle(
                            fontSize = 13.sp,
                            fontWeight = platformWeight(FontWeight.Medium),
                            letterSpacing = 0.sp,
                            color = p.ink2,
                        ),
                    ) {
                        append(unitText)
                    }
                },
                style = TextStyle(fontFamily = UiFamily, lineHeight = (bigSize.value * 1.08f).sp).digits(),
                maxLines = 1,
                softWrap = false,
            )
            Spacer(Modifier.height(3.dp))
            val subText = if (hero.scrubbing) "" else uiText(hero.sublineText)
            Box(Modifier.height(17.dp), contentAlignment = Alignment.CenterStart) {
                if (subText.isNotEmpty()) {
                    Text(
                        subText,
                        style = T.s12.digits(),
                        color = p.ink2,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
            }
        }

        Column(
            Modifier.width(compWidth).heightIn(min = minCompHeight).padding(top = 5.dp),
        ) {
            if (comp != null) {
                val compTextSize = if (isWide) 12.5.sp else 12.sp
                val compLineH = if (isWide) 18.sp else 17.sp
                val items = listOf(
                    Triple(copy(UiMessages.usageCompUncached()), uiText(comp.uncachedInputText), false),
                    Triple(copy(UiMessages.usageCompCacheRead()), uiText(comp.cacheReadText), false),
                    Triple(copy(UiMessages.usageCompCacheWrite()), uiText(comp.cacheWriteText), false),
                    Triple(copy(UiMessages.usageCompOutput()), uiText(comp.outputText), false),
                    Triple(copy(UiMessages.usageCompReasoningLabel()), uiText(comp.reasoningOutputText), true),
                ).filter { it.second.isNotEmpty() }
                items.forEach { (label, amountText, indent) ->
                    Row(
                        Modifier.fillMaxWidth().height(compLineH.value.dp),
                        horizontalArrangement = Arrangement.SpaceBetween,
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Text(
                            label,
                            style = TextStyle(fontFamily = UiFamily, fontSize = compTextSize).digits(),
                            color = p.ink2,
                            modifier = if (indent) Modifier.padding(start = 10.dp) else Modifier,
                            maxLines = 1,
                        )
                        Text(
                            amountText,
                            style = TextStyle(
                                fontFamily = UiFamily,
                                fontSize = compTextSize,
                                fontWeight = if (indent) FontWeight.Normal else platformWeight(FontWeight.Medium),
                            ).digits(),
                            color = if (indent) p.ink2 else p.ink,
                            maxLines = 1,
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun UsageEmptyRecordsView(
    vm: UsageViewModel,
    preset: UsageRangePreset,
    isWide: Boolean,
    copy: MessageResolver,
) {
    val p = pal
    val bigSize = if (isWide) 52.sp else 44.sp
    val barHeight = if (isWide) 150.dp else 108.dp
    val ticks = remember(vm.granularity, vm.startMs, vm.endMs, preset, copy) {
        buildUsageAxisTicks(
            granularity = vm.granularity,
            buckets = emptyList(),
            startMs = vm.startMs,
            endMs = vm.endMs,
            preset = preset,
            axis24h = copy(UiMessages.usageAxis24H()),
        )
    }
    Spacer(Modifier.height(if (isWide) 14.dp else 18.dp))
    Text(uiText(vm.hero.scopeText).ifEmpty { copy(UiMessages.usageScopeAllEmpty()) }, style = T.s12, color = p.ink2, maxLines = 1)
    Spacer(Modifier.height(2.dp))
    Text(
        text = buildAnnotatedString {
            withStyle(SpanStyle(fontSize = bigSize, fontWeight = platformWeight(FontWeight.Bold), color = p.ink3)) {
                append(uiText(vm.hero.totalTokensText).ifEmpty { "0" })
            }
            append(" ")
            withStyle(SpanStyle(fontSize = 13.sp, fontWeight = platformWeight(FontWeight.Medium), color = p.ink2)) {
                append(copy(UiMessages.usageKpiUnitTokens()))
            }
        },
        style = TextStyle(fontFamily = UiFamily, lineHeight = (bigSize.value * 1.08f).sp).digits(),
        maxLines = 1,
    )
    Spacer(Modifier.height(20.dp + 16.dp))
    Column(Modifier.fillMaxWidth()) {
        Box(Modifier.fillMaxWidth().height(barHeight), contentAlignment = Alignment.Center) {
            Text(uiText(vm.emptyTitle).ifEmpty { copy(UiMessages.usageEmptyNoRecords()) }, style = T.s13, color = p.ink2)
        }
        Box(Modifier.fillMaxWidth().height(1.dp).background(p.faint))
        Spacer(Modifier.height(4.dp))
        UsageAxisRow(ticks = ticks)
    }
}

@Composable
private fun UsageChartsStack(
    vm: UsageViewModel,
    isWide: Boolean,
    copy: MessageResolver,
    onScrub: (Int, Boolean) -> Unit,
) {
    val p = pal
    val haptic = LocalHapticFeedback.current
    val density = LocalDensity.current
    val barH = if (isWide) 150.dp else 108.dp
    val stripH = if (isWide) 34.dp else 26.dp
    val topPadDp = 12.dp

    val barChart = if (vm.hasBarChart()) vm.barChart else null
    val buckets = barChart?.bucketsList ?: emptyList()
    val n = buckets.size
    val hero = if (vm.hasHero()) vm.hero else null
    val scrubbing = (hero?.scrubbing == true) && n > 0
    val scrubIdx = hero?.scrubBucketIndex ?: -1
    val hasSelected = hero?.hasSelectedModel == true

    val ticks = remember(vm.granularity, buckets, vm.startMs, vm.endMs, vm.preset, copy) {
        buildUsageAxisTicks(
            granularity = vm.granularity,
            buckets = buckets,
            startMs = vm.startMs,
            endMs = vm.endMs,
            preset = vm.preset,
            axis24h = copy(UiMessages.usageAxis24H()),
        )
    }

    val chartsA11y = uiText(vm.chartsA11YLabel)
    val maxLabel = if (barChart != null) uiText(barChart.yMaxText) else ""
    val midLabel = if (barChart != null) uiText(barChart.yMidText) else ""

    val labelStrokePaint = remember(p.paper, density) {
        Paint(Paint.ANTI_ALIAS_FLAG).apply {
            style = Paint.Style.STROKE
            strokeWidth = with(density) { 4.dp.toPx() }
            strokeJoin = Paint.Join.ROUND
            color = p.paper.toArgb()
            textSize = with(density) { 10.5.sp.toPx() }
            textAlign = Paint.Align.RIGHT
        }
    }
    val labelFillPaint = remember(p.ink2, density) {
        Paint(Paint.ANTI_ALIAS_FLAG).apply {
            style = Paint.Style.FILL
            color = p.ink2.toArgb()
            textSize = with(density) { 10.5.sp.toPx() }
            textAlign = Paint.Align.RIGHT
        }
    }

    Column(Modifier.fillMaxWidth().semantics { contentDescription = chartsA11y }) {
        Box(
            Modifier
                .fillMaxWidth()
                .pointerInput(n) {
                    if (n <= 0) return@pointerInput
                    val slop = viewConfiguration.touchSlop
                    awaitEachGesture {
                        val down = awaitFirstDown(requireUnconsumed = false)
                        val startPos = down.position
                        var active = false
                        var lastBucket = -1
                        try {
                            val early = withTimeoutOrNull(150L) {
                                while (true) {
                                    val ev = awaitPointerEvent()
                                    val change = ev.changes.firstOrNull { it.id == down.id } ?: ev.changes.firstOrNull()
                                    if (change == null || !change.pressed) return@withTimeoutOrNull "up"
                                    if ((change.position - startPos).getDistance() > slop) return@withTimeoutOrNull "moved"
                                }
                            }
                            if (early != null) return@awaitEachGesture
                            active = true
                            haptic.performHapticFeedback(HapticFeedbackType.TextHandleMove)
                            val wPx = size.width.coerceAtLeast(1)
                            val b0 = ((startPos.x / wPx.toFloat()) * n).toInt().coerceIn(0, n - 1)
                            lastBucket = b0
                            onScrub(b0, false)
                            while (true) {
                                val ev = awaitPointerEvent()
                                val change = ev.changes.firstOrNull { it.id == down.id } ?: ev.changes.firstOrNull()
                                if (change == null || !change.pressed) break
                                change.consume()
                                val next = ((change.position.x / wPx.toFloat()) * n).toInt().coerceIn(0, n - 1)
                                if (next != lastBucket) {
                                    lastBucket = next
                                    haptic.performHapticFeedback(HapticFeedbackType.TextHandleMove)
                                    onScrub(next, false)
                                }
                            }
                        } finally {
                            if (active) onScrub(0, true)
                        }
                    }
                },
        ) {
            Column(Modifier.fillMaxWidth()) {
                // Bar chart
                Canvas(Modifier.fillMaxWidth().height(barH)) {
                    val w = size.width
                    val h = size.height
                    val topPx = topPadDp.toPx()
                    val innerH = (h - topPx).coerceAtLeast(1f)
                    val midY = topPx + innerH / 2f

                    drawLine(p.sep, Offset(0f, topPx), Offset(w, topPx), strokeWidth = 1.dp.toPx())
                    drawLine(p.sep, Offset(0f, midY), Offset(w, midY), strokeWidth = 1.dp.toPx())

                    val yMax = (barChart?.yMax ?: 0L).toDouble()
                    if (yMax > 0.0 && n > 0) {
                        val bw = w / n.toFloat()
                        val gap = (if (n > 26) 2.dp else 3.dp).toPx()
                        val barW = (bw - gap).coerceAtLeast(1f)
                        val minH = 1.5.dp.toPx()
                        val rx = CornerRadius(1.5.dp.toPx(), 1.5.dp.toPx())

                        buckets.forEachIndexed { i, b ->
                            val allTok = b.allTokens.toDouble()
                            if (allTok <= 0.0) return@forEachIndexed
                            val x = i * bw + gap / 2f
                            val allH = max(minH, ((allTok / yMax) * innerH).toFloat()).coerceAtMost(innerH)
                            val alpha = if (scrubbing && scrubIdx != i) 0.28f else 1f
                            if (hasSelected) {
                                drawRoundRect(
                                    color = p.fill2.copy(alpha = p.fill2.alpha * alpha),
                                    topLeft = Offset(x, h - allH),
                                    size = Size(barW, allH),
                                    cornerRadius = rx,
                                )
                                val selTok = b.selectedTokens.toDouble()
                                if (selTok > 0.0) {
                                    val selH = max(minH, ((selTok / yMax) * innerH).toFloat()).coerceAtMost(innerH)
                                    drawRoundRect(
                                        color = p.ink.copy(alpha = alpha),
                                        topLeft = Offset(x, h - selH),
                                        size = Size(barW, selH),
                                        cornerRadius = rx,
                                    )
                                }
                            } else {
                                drawRoundRect(
                                    color = p.ink.copy(alpha = alpha),
                                    topLeft = Offset(x, h - allH),
                                    size = Size(barW, allH),
                                    cornerRadius = rx,
                                )
                            }
                        }

                        val labelOffset = 4.dp.toPx()
                        drawIntoCanvas { canvas ->
                            val nc = canvas.nativeCanvas
                            if (maxLabel.isNotEmpty()) {
                                nc.drawText(maxLabel, w, topPx - labelOffset, labelStrokePaint)
                                nc.drawText(maxLabel, w, topPx - labelOffset, labelFillPaint)
                            }
                            if (midLabel.isNotEmpty()) {
                                nc.drawText(midLabel, w, midY - labelOffset, labelStrokePaint)
                                nc.drawText(midLabel, w, midY - labelOffset, labelFillPaint)
                            }
                        }
                    }

                    val baseY = h - 0.5.dp.toPx()
                    drawLine(p.faint, Offset(0f, baseY), Offset(w, baseY), strokeWidth = 1.dp.toPx())
                }

                // Strip 1: Cache Hit
                UsageMetricStrip(
                    label = copy(UiMessages.usageStripCacheHit()),
                    unit = null,
                    strip = if (vm.hasCacheHitStrip()) vm.cacheHitStrip else null,
                    scrubbing = scrubbing,
                    scrubIdx = scrubIdx,
                    stripHeight = stripH,
                    noSampleA11y = copy(UiMessages.usageNoSampleA11Y()),
                )

                // Strip 2: Output Speed
                UsageMetricStrip(
                    label = copy(UiMessages.usageStripSpeed()),
                    unit = copy(UiMessages.usageKpiUnitToks()),
                    strip = if (vm.hasOutputSpeedStrip()) vm.outputSpeedStrip else null,
                    scrubbing = scrubbing,
                    scrubIdx = scrubIdx,
                    stripHeight = stripH,
                    noSampleA11y = copy(UiMessages.usageNoSampleA11Y()),
                )

                // Strip 3: TTFT
                UsageMetricStrip(
                    label = copy(UiMessages.usageStripTtft()),
                    unit = null,
                    strip = if (vm.hasTtftStrip()) vm.ttftStrip else null,
                    scrubbing = scrubbing,
                    scrubIdx = scrubIdx,
                    stripHeight = stripH,
                    noSampleA11y = copy(UiMessages.usageNoSampleA11Y()),
                )
            }

            if (scrubbing && scrubIdx in 0 until n) {
                Canvas(Modifier.matchParentSize()) {
                    val cx = ((scrubIdx + 0.5f) / n.toFloat()) * size.width
                    drawLine(p.ink2, Offset(cx, 0f), Offset(cx, size.height), strokeWidth = 1.dp.toPx())
                }
            }
        }

        Spacer(Modifier.height(4.dp))
        UsageAxisRow(ticks = ticks)
    }
}

@Composable
private fun UsageMetricStrip(
    label: String,
    unit: String?,
    strip: UsageMetricStripView?,
    scrubbing: Boolean,
    scrubIdx: Int,
    stripHeight: androidx.compose.ui.unit.Dp,
    noSampleA11y: String,
) {
    val p = pal
    Spacer(Modifier.height(12.dp))
    if (strip == null || !strip.hasRange) {
        Row(Modifier.fillMaxWidth().height(17.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            Text(label, style = T.s12, color = p.ink2)
            Text(EN_DASH, style = T.s13b, color = p.ink3, modifier = Modifier.semantics { contentDescription = noSampleA11y })
        }
        Spacer(Modifier.fillMaxWidth().height(stripHeight))
        return
    }

    val curStr = if (strip.hasValue) uiText(strip.currentValueText).ifEmpty { EN_DASH } else EN_DASH
    val minStr = uiText(strip.minValueText)
    val maxStr = uiText(strip.maxValueText)
    val showRange = strip.showRange && minStr.isNotEmpty() && maxStr.isNotEmpty()

    Row(Modifier.fillMaxWidth().height(17.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(label, style = T.s12, color = p.ink2, maxLines = 1)
        Text(
            curStr,
            style = T.s13b.copy(fontSize = 13.5.sp).digits(),
            color = if (strip.hasValue) p.ink else p.ink3,
            modifier = if (strip.hasValue) Modifier else Modifier.semantics { contentDescription = noSampleA11y },
            maxLines = 1,
        )
        if (strip.hasValue && unit != null) {
            Text(unit, style = T.s12, color = p.ink2, maxLines = 1)
        }
        Spacer(Modifier.weight(1f))
        if (showRange) {
            val rangeText = listOf(minStr, maxStr).joinToString(" – ")
            Text(rangeText, style = T.s12.copy(fontSize = 11.sp).digits(), color = p.ink2, maxLines = 1)
        }
    }

    Canvas(Modifier.fillMaxWidth().height(stripHeight)) {
        val w = size.width
        val h = size.height
        val pad = 3.dp.toPx()
        val innerH = (h - pad * 2f).coerceAtLeast(1f)
        val span = max(strip.scaleMax - strip.scaleMin, 1e-9)

        fun pointCoord(pts: List<io.github.lewin671.remoteagent.core.proto.UsageStripPointView>, i: Int): Offset? {
            if (i !in pts.indices || !pts[i].hasValue) return null
            val n = pts.size
            val x = ((i + 0.5f) / n.toFloat()) * w
            val y = h - pad - (((pts[i].value - strip.scaleMin) / span) * innerH).toFloat()
            return Offset(x, y)
        }

        fun drawSeries(
            pts: List<io.github.lewin671.remoteagent.core.proto.UsageStripPointView>,
            strokeColor: Color,
            strokeWidthPx: Float,
            drawDots: Boolean,
        ) {
            val n = pts.size
            if (n == 0) return
            val path = Path()
            var pen = false
            for (i in 0 until n) {
                val c = pointCoord(pts, i)
                if (c == null) {
                    pen = false
                    continue
                }
                val prev = pointCoord(pts, i - 1)
                val next = pointCoord(pts, i + 1)
                if (prev == null && next == null) {
                    if (drawDots) {
                        drawCircle(color = strokeColor, radius = 1.8.dp.toPx(), center = c)
                    }
                    pen = false
                    continue
                }
                if (!pen) path.moveTo(c.x, c.y) else path.lineTo(c.x, c.y)
                pen = true
            }
            if (!path.isEmpty) {
                drawPath(
                    path = path,
                    color = strokeColor,
                    style = Stroke(width = strokeWidthPx, cap = StrokeCap.Round, join = StrokeJoin.Round),
                )
            }
        }

        val hasSel = strip.selectedPointsList.isNotEmpty()
        if (hasSel) {
            drawSeries(strip.allPointsList, p.faint, 1.2.dp.toPx(), drawDots = false)
        }
        val frontPts = if (hasSel) strip.selectedPointsList else strip.allPointsList
        drawSeries(frontPts, p.ink, 1.6.dp.toPx(), drawDots = true)

        val markOffset = if (scrubbing) {
            pointCoord(frontPts, scrubIdx)
        } else {
            pointCoord(frontPts, strip.lastValidIndex)
        }
        if (markOffset != null) {
            if (scrubbing) {
                drawCircle(color = p.paper, radius = 3.dp.toPx(), center = markOffset)
                drawCircle(
                    color = p.ink,
                    radius = 3.dp.toPx(),
                    center = markOffset,
                    style = Stroke(width = 1.6.dp.toPx()),
                )
            } else {
                drawCircle(color = p.ink, radius = 2.2.dp.toPx(), center = markOffset)
            }
        }
    }
}

@Composable
private fun UsageAxisRow(ticks: List<UsageAxisTick>) {
    val p = pal
    Box(Modifier.fillMaxWidth().height(20.dp)) {
        ticks.forEach { tick ->
            Text(
                tick.label,
                style = T.s12.copy(fontSize = 10.5.sp).digits(),
                color = p.ink2,
                maxLines = 1,
                softWrap = false,
                modifier = Modifier.layout { measurable, constraints ->
                    val placeable = measurable.measure(constraints.copy(minWidth = 0))
                    val w = constraints.maxWidth
                    val anchor = ((tick.pct / 100f) * w).roundToInt()
                    val rawX = when (tick.align) {
                        UsageTickAlign.START -> anchor
                        UsageTickAlign.CENTER -> anchor - placeable.width / 2
                        UsageTickAlign.END -> anchor - placeable.width
                    }
                    val x = rawX.coerceIn(0, max(0, w - placeable.width))
                    layout(placeable.width, placeable.height) {
                        placeable.place(x, 4.dp.roundToPx())
                    }
                },
            )
        }
    }
}

@Composable
private fun UsageModelTable(
    vm: UsageViewModel,
    isWide: Boolean,
    copy: MessageResolver,
    onSelectSort: (UsageSortColumn) -> Unit,
    onSelectSeries: (String, Boolean) -> Unit,
) {
    val p = pal
    val sortBy =
        if (vm.sortBy == UsageSortColumn.USAGE_SORT_COLUMN_UNSPECIFIED) UsageSortColumn.USAGE_SORT_COLUMN_TOKENS
        else vm.sortBy
    val hPad = if (isWide) 12.dp else 16.dp
    val noSampleA11y = copy(UiMessages.usageNoSampleA11Y())

    Column(Modifier.fillMaxWidth()) {
        // Table Header
        Row(
            Modifier
                .fillMaxWidth()
                .padding(start = hPad, end = hPad, bottom = 7.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Text(
                copy(UiMessages.usageColModel()),
                style = T.s12.copy(fontSize = 11.5.sp),
                color = p.ink2,
                modifier = Modifier.weight(1f),
                maxLines = 1,
            )
            SortHeaderCell(copy(UiMessages.usageColTokens()), UsageSortColumn.USAGE_SORT_COLUMN_TOKENS, sortBy, 62.dp, asc = false, onSelectSort)
            SortHeaderCell(copy(UiMessages.usageColCache()), UsageSortColumn.USAGE_SORT_COLUMN_CACHE_HIT, sortBy, 42.dp, asc = false, onSelectSort)
            SortHeaderCell(copy(UiMessages.usageColSpeed()), UsageSortColumn.USAGE_SORT_COLUMN_SPEED, sortBy, 46.dp, asc = false, onSelectSort)
            SortHeaderCell(copy(UiMessages.usageColTtft()), UsageSortColumn.USAGE_SORT_COLUMN_TTFT, sortBy, 44.dp, asc = true, onSelectSort)
        }
        Box(Modifier.fillMaxWidth().height(0.5.dp).background(p.sep))

        // Rows
        vm.rowsList.forEachIndexed { idx, row ->
            val prevSel = idx > 0 && vm.rowsList[idx - 1].selected
            if (idx > 0 && !row.selected && !prevSel) {
                Box(Modifier.fillMaxWidth().padding(horizontal = hPad).height(0.5.dp).background(p.sep))
            }
            val rowShape = if (isWide) RoundedCornerShape(10.dp) else RoundedCornerShape(0.dp)
            Column(
                Modifier
                    .fillMaxWidth()
                    .clip(rowShape)
                    .background(if (row.selected) p.fill else Color.Transparent)
                    .clickable(role = Role.Button) {
                        onSelectSeries(row.seriesKey, !row.selected)
                    }
                    .padding(start = hPad, end = hPad, top = 9.dp, bottom = 10.dp),
            ) {
                Row(
                    Modifier.fillMaxWidth().heightIn(min = 18.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(6.dp),
                ) {
                    UsageAgentMarkIcon(row.agent, size = 13.dp, tint = p.ink2)
                    Text(
                        row.model,
                        style = T.s13.copy(fontSize = 13.5.sp, fontWeight = platformWeight(FontWeight.Medium)),
                        color = p.ink,
                        maxLines = 1,
                        overflow = TextOverflow.MiddleEllipsis,
                        modifier = Modifier.weight(1f),
                    )
                    val deltaStr = uiText(row.deltaText)
                    if (deltaStr.isNotEmpty()) {
                        Text(deltaStr, style = T.s12.copy(fontSize = 11.5.sp).digits(), color = p.ink2, maxLines = 1)
                    }
                }

                Spacer(Modifier.height(4.dp))

                Row(
                    Modifier.fillMaxWidth().heightIn(min = 17.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    // Share bar always proportional to tokens
                    Box(
                        Modifier
                            .weight(1f)
                            .height(3.dp)
                            .clip(RoundedCornerShape(1.5.dp))
                            .background(p.fill),
                    ) {
                        val frac = row.shareRatio.toFloat().coerceIn(0f, 1f)
                        Box(
                            Modifier
                                .fillMaxHeight()
                                .fillMaxWidth(frac)
                                .widthIn(min = 2.dp)
                                .clip(RoundedCornerShape(1.5.dp))
                                .background(if (row.selected) p.ink else p.ink2),
                        )
                    }

                    Text(
                        uiText(row.totalTokensText),
                        style = T.s13b.digits(),
                        color = p.ink,
                        textAlign = TextAlign.End,
                        maxLines = 1,
                        modifier = Modifier.width(62.dp),
                    )
                    MetricCell(
                        text = if (row.hasCacheHit) uiText(row.cacheHitText).ifEmpty { EN_DASH } else EN_DASH,
                        hasValue = row.hasCacheHit,
                        width = 42.dp,
                        noSampleA11y = noSampleA11y,
                    )
                    MetricCell(
                        text = if (row.hasSpeed) uiText(row.speedText).ifEmpty { EN_DASH } else EN_DASH,
                        hasValue = row.hasSpeed,
                        width = 46.dp,
                        noSampleA11y = noSampleA11y,
                    )
                    MetricCell(
                        text = if (row.hasTtft) uiText(row.ttftText).ifEmpty { EN_DASH } else EN_DASH,
                        hasValue = row.hasTtft,
                        width = 44.dp,
                        noSampleA11y = noSampleA11y,
                    )
                }
            }
        }

        // Footer
        Box(Modifier.fillMaxWidth().height(0.5.dp).background(p.sep))
        Column(
            Modifier.fillMaxWidth().padding(start = hPad, end = hPad, top = 14.dp, bottom = 6.dp),
            verticalArrangement = Arrangement.spacedBy(6.dp),
        ) {
            Text(copy(UiMessages.usageFooterNote()), style = T.s12.copy(fontSize = 11.5.sp, lineHeight = 17.sp), color = p.ink2)
            if (vm.hasUnsupportedFootnote()) {
                val extra = uiText(vm.unsupportedFootnote)
                if (extra.isNotEmpty()) {
                    Text(extra, style = T.s12.copy(fontSize = 11.5.sp, lineHeight = 17.sp), color = p.ink2)
                }
            }
        }
    }
}

@Composable
private fun SortHeaderCell(
    label: String,
    column: UsageSortColumn,
    activeSort: UsageSortColumn,
    width: androidx.compose.ui.unit.Dp,
    asc: Boolean,
    onSelect: (UsageSortColumn) -> Unit,
) {
    val p = pal
    val active = activeSort == column
    val arrow = if (active) (if (asc) " ↑" else " ↓") else ""
    val headerText = label + arrow
    Box(
        Modifier
            .width(width)
            .heightIn(min = 24.dp)
            .clickable(role = Role.Button) { onSelect(column) },
        contentAlignment = Alignment.CenterEnd,
    ) {
        Text(
            headerText,
            style = T.s12.copy(
                fontSize = 11.5.sp,
                fontWeight = if (active) platformWeight(FontWeight.SemiBold) else FontWeight.Normal,
            ),
            color = if (active) p.ink else p.ink2,
            textAlign = TextAlign.End,
            maxLines = 1,
        )
    }
}

@Composable
private fun MetricCell(
    text: String,
    hasValue: Boolean,
    width: androidx.compose.ui.unit.Dp,
    noSampleA11y: String,
) {
    val p = pal
    Text(
        text,
        style = T.s13.digits(),
        color = if (hasValue) p.ink else p.ink3,
        textAlign = TextAlign.End,
        maxLines = 1,
        modifier = Modifier.width(width).let { base ->
            if (hasValue) base else base.semantics { contentDescription = noSampleA11y }
        },
    )
}

@Composable
private fun UsageAgentMarkIcon(
    agent: Agent,
    size: androidx.compose.ui.unit.Dp,
    tint: Color,
) {
    val copy = uiText()
    val vector = AgentMarks.of(agent) ?: return
    Icon(
        imageVector = vector,
        contentDescription = agentName(agent, copy = copy),
        modifier = Modifier.size(size),
        tint = tint,
    )
}

@Composable
private fun UsageLoadingSkeleton() {
    val p = pal
    Box(
        Modifier.fillMaxWidth().padding(top = 16.dp).height(260.dp)
            .clip(Radii.R18)
            .background(p.gbg),
    )
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun UsageCustomRangeDialog(
    initialStartMs: Long,
    initialEndMs: Long,
    copy: MessageResolver,
    onApply: (Long, Long) -> Unit,
    onDismiss: () -> Unit,
) {
    val zone = ZoneId.systemDefault()
    val today = LocalDate.now(zone)
    val minDay = today.minusDays((USAGE_RETENTION_DAYS - 1).toLong())
    val minUtcMs = minDay.atStartOfDay(ZoneOffset.UTC).toInstant().toEpochMilli()
    val todayUtcMs = today.atStartOfDay(ZoneOffset.UTC).toInstant().toEpochMilli()

    fun localMsToUtcPickerMs(ms: Long): Long? {
        if (ms <= 0L) return null
        val d = Instant.ofEpochMilli(ms).atZone(zone).toLocalDate()
        return d.atStartOfDay(ZoneOffset.UTC).toInstant().toEpochMilli()
    }

    fun utcPickerMsToLocalMs(utcMs: Long): Long {
        val d = Instant.ofEpochMilli(utcMs).atZone(ZoneOffset.UTC).toLocalDate()
        return d.atStartOfDay(zone).toInstant().toEpochMilli()
    }

    val pickerState = rememberDateRangePickerState(
        initialSelectedStartDateMillis = localMsToUtcPickerMs(initialStartMs),
        initialSelectedEndDateMillis = localMsToUtcPickerMs(initialEndMs),
        selectableDates = object : SelectableDates {
            override fun isSelectableDate(utcTimeMillis: Long): Boolean = utcTimeMillis in minUtcMs..todayUtcMs
            override fun isSelectableYear(year: Int): Boolean = year in minDay.year..today.year
        },
    )

    DatePickerDialog(
        onDismissRequest = onDismiss,
        confirmButton = {
            TextButton(
                onClick = {
                    val rawStart = pickerState.selectedStartDateMillis?.let(::utcPickerMsToLocalMs) ?: initialStartMs
                    val rawEnd = pickerState.selectedEndDateMillis?.let(::utcPickerMsToLocalMs) ?: rawStart
                    val (clampedStart, clampedEnd) = clampUsageCustomRange(rawStart, rawEnd)
                    onApply(clampedStart, clampedEnd)
                },
            ) {
                Text(copy(UiMessages.commonDone()))
            }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) {
                Text(copy(UiMessages.commonCancel()))
            }
        },
    ) {
        DateRangePicker(
            state = pickerState,
            title = {
                Text(
                    copy(UiMessages.usageCustomRangeLimitHint()),
                    style = T.s13,
                    color = pal.ink2,
                    modifier = Modifier.padding(horizontal = 24.dp, vertical = 12.dp),
                )
            },
        )
    }
}
