package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectHorizontalDragGestures
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.DatePickerDialog
import androidx.compose.material3.DateRangePicker
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.SelectableDates
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.rememberDateRangePickerState
import androidx.compose.runtime.Composable
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
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.clipRect
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.MessageResolver
import io.github.lewin671.remoteagent.core.USAGE_RETENTION_DAYS
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.clampUsageCustomRange
import io.github.lewin671.remoteagent.core.proto.UsageMetricTab
import io.github.lewin671.remoteagent.core.proto.UsageSeriesView
import io.github.lewin671.remoteagent.core.proto.UsageViewModel
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.UsageBucketGranularity
import io.github.lewin671.remoteagent.proto.UsageBucketPoint
import io.github.lewin671.remoteagent.proto.UsageRangePreset
import io.github.lewin671.remoteagent.proto.UsageSourceState
import kotlinx.coroutines.delay
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter
import java.util.Locale
import kotlin.math.abs
import kotlin.math.ceil
import kotlin.math.max
import kotlin.math.min
import kotlin.math.round
import kotlin.math.roundToInt
import kotlin.math.roundToLong

const val MAX_USAGE_OVERLAY_SERIES = 4
const val DEFAULT_USAGE_OVERLAY_SERIES = 3
private const val USAGE_FEW_SAMPLES_MIN = 3L
private const val DAY_MS = 86_400_000L

private const val EM_DASH = "—"
private const val MINUS_SIGN = "−"
private const val UNIT_K = "k"
private const val UNIT_M = "M"
private const val UNIT_B = "B"
private const val UNIT_PCT = "%"
private const val UNIT_PT = " pt"
private const val UNIT_MS = " ms"
private const val UNIT_S = " s"
private const val ENGLISH_WAN_MARKER = "10k"

fun formatUsageCommaUint(n: Long): String {
    val clamped = max(0L, n)
    return String.format(Locale.US, "%,d", clamped)
}

private fun formatUsageScaled(value: Double, unit: String): String {
    if (value < 100.0) {
        val rounded = round(value * 10.0) / 10.0
        val whole = round(rounded)
        val numStr =
            if (abs(rounded - whole) < 1e-6) whole.toLong().toString()
            else String.format(Locale.US, "%.1f", rounded)
        return numStr + unit
    }
    return formatUsageCommaUint(round(value).toLong()) + unit
}

fun formatUsageTokens(
    n: Long,
    wanUnit: String,
    yiUnit: String,
    chinese: Boolean,
): String {
    if (n <= 0L) return "0"
    val v = n.toDouble()
    if (!chinese) {
        if (v < 1000.0) return formatUsageCommaUint(n)
        if (v < 999_500.0) return formatUsageScaled(v / 1000.0, UNIT_K)
        if (v < 999_500_000.0) return formatUsageScaled(v / 1_000_000.0, UNIT_M)
        return formatUsageScaled(v / 1_000_000_000.0, UNIT_B)
    }
    if (v < 10_000.0) return formatUsageCommaUint(n)
    if (v < 99_950_000.0) return formatUsageScaled(v / 10_000.0, wanUnit)
    if (v < 100_000_000.0) return "1$yiUnit"
    return formatUsageScaled(v / 100_000_000.0, yiUnit)
}

fun formatUsageTokens(n: Long, copy: MessageResolver): String {
    val wan = copy(UiMessages.usageUnitWan())
    val yi = copy(UiMessages.usageUnitYi())
    return formatUsageTokens(n, wan, yi, chinese = wan != ENGLISH_WAN_MARKER)
}

fun formatUsageCacheHit(rate: Double): String {
    if (!rate.isFinite() || rate <= 0.0) return "0$UNIT_PCT"
    if (rate >= 1.0) return "100$UNIT_PCT"
    val pct = rate * 100.0
    if (pct < 1.0 || pct > 99.0) {
        val rounded = round(pct * 10.0) / 10.0
        val whole = round(rounded)
        val numStr =
            if (abs(rounded - whole) < 1e-6) whole.toLong().toString()
            else String.format(Locale.US, "%.1f", rounded)
        return numStr + UNIT_PCT
    }
    return "${round(pct).toInt()}$UNIT_PCT"
}

fun formatUsageSpeed(tokPerSec: Double): String {
    if (!tokPerSec.isFinite() || tokPerSec <= 0.0) return "0"
    if (tokPerSec < 10.0) {
        val rounded = round(tokPerSec * 10.0) / 10.0
        val whole = round(rounded)
        return if (abs(rounded - whole) < 1e-6) whole.toLong().toString()
        else String.format(Locale.US, "%.1f", rounded)
    }
    return formatUsageCommaUint(round(tokPerSec).toLong())
}

fun formatUsageTtft(ms: Double): String {
    if (!ms.isFinite() || ms <= 0.0) return "0$UNIT_MS"
    if (ms < 1000.0) return "${round(ms).toLong()}$UNIT_MS"
    if (ms < 10_000.0) {
        val sec = round(ms / 100.0) / 10.0
        return String.format(Locale.US, "%.1f", sec) + UNIT_S
    }
    return "${round(ms / 1000.0).toLong()}$UNIT_S"
}

fun formatUsageSignedPercent(pct: Double, flatText: String): String {
    if (!pct.isFinite() || abs(pct) < 0.5) return flatText
    val r = round(pct).toLong()
    return if (r > 0L) "+$r$UNIT_PCT" else "$MINUS_SIGN${abs(r)}$UNIT_PCT"
}

fun formatUsageSignedPercent(pct: Double, copy: MessageResolver): String =
    formatUsageSignedPercent(pct, copy(UiMessages.usageFlat()))

fun formatUsageSignedPoints(pts: Double, flatText: String): String {
    if (!pts.isFinite() || abs(pts) < 0.5) return flatText
    val r = round(pts).toLong()
    return if (r > 0L) "+$r$UNIT_PT" else "$MINUS_SIGN${abs(r)}$UNIT_PT"
}

fun formatUsageSignedPoints(pts: Double, copy: MessageResolver): String =
    formatUsageSignedPoints(pts, copy(UiMessages.usageFlat()))

fun formatUsageTtftDelta(curMs: Double, prevMs: Double, flatText: String): String {
    if (!curMs.isFinite() || !prevMs.isFinite() || prevMs <= 0.0) return EM_DASH
    val diff = curMs - prevMs
    if (abs(diff) < 50.0) return flatText
    val sign = if (diff > 0.0) "+" else MINUS_SIGN
    val absDiff = abs(diff)
    if (absDiff < 1000.0) return "$sign${round(absDiff).toLong()}$UNIT_MS"
    if (absDiff < 10_000.0) {
        val sec = round(absDiff / 100.0) / 10.0
        return sign + String.format(Locale.US, "%.1f", sec) + UNIT_S
    }
    return "$sign${round(absDiff / 1000.0).toLong()}$UNIT_S"
}

fun formatUsageTtftDelta(curMs: Double, prevMs: Double, copy: MessageResolver): String =
    formatUsageTtftDelta(curMs, prevMs, copy(UiMessages.usageFlat()))

fun toggleUsageOverlaySelection(
    selected: List<String>,
    key: String,
    maxOverlay: Int = MAX_USAGE_OVERLAY_SERIES,
): List<String> {
    if (key.isEmpty()) return selected
    val idx = selected.indexOf(key)
    if (idx >= 0) {
        return if (selected.size <= 1) selected else selected.filter { it != key }
    }
    if (selected.size < maxOverlay) return selected + key
    return selected.take(maxOverlay - 1) + key
}

fun initialUsageSelection(series: List<UsageSeriesView>): List<String> {
    if (series.isEmpty()) return emptyList()
    val focused = series.firstOrNull { it.focused }?.seriesKey
    val top = series.take(DEFAULT_USAGE_OVERLAY_SERIES).map { it.seriesKey }.toMutableList()
    if (!focused.isNullOrEmpty() && focused !in top) {
        if (top.size < MAX_USAGE_OVERLAY_SERIES) top.add(0, focused)
        else {
            top[top.lastIndex] = focused
        }
    }
    return top
}

internal fun usageBucketTokens(b: UsageBucketPoint): Long =
    b.uncachedInputTokens + b.cacheReadTokens + b.cacheWriteTokens + b.outputTokens

internal fun usageSeriesTotalTokens(s: UsageSeriesView): Long {
    if (!s.hasCurrent()) return 0L
    val c = s.current
    return c.uncachedInputTokens + c.cacheReadTokens + c.cacheWriteTokens + c.outputTokens
}

private fun bucketCacheHit(b: UsageBucketPoint): Double? {
    val denom = b.uncachedInputTokens + b.cacheReadTokens + b.cacheWriteTokens
    if (denom <= 0L) return null
    return b.cacheReadTokens.toDouble() / denom.toDouble()
}

private fun bucketHasSpeed(b: UsageBucketPoint): Boolean = b.speedRequestCount > 0L && b.speedP50 > 0.0

private fun bucketHasTtft(b: UsageBucketPoint): Boolean = b.speedRequestCount > 0L && b.ttftP50Ms > 0.0

private fun seriesIsFewSamples(s: UsageSeriesView, metric: UsageMetricTab): Boolean = when (metric) {
    UsageMetricTab.USAGE_METRIC_TAB_TOKENS -> false
    UsageMetricTab.USAGE_METRIC_TAB_CACHE_HIT -> s.current.usageRequestCount < USAGE_FEW_SAMPLES_MIN
    else -> s.fewSpeedSamples || s.current.speedRequestCount < USAGE_FEW_SAMPLES_MIN
}

private fun formatBucketAxisTime(ms: Long, gran: UsageBucketGranularity, zone: ZoneId = ZoneId.systemDefault()): String {
    val zdt = Instant.ofEpochMilli(ms).atZone(zone)
    return if (gran == UsageBucketGranularity.USAGE_BUCKET_GRANULARITY_HOUR) {
        String.format(Locale.US, "%02d:00", zdt.hour)
    } else {
        "${zdt.monthValue}/${zdt.dayOfMonth}"
    }
}

private fun formatShortDay(ms: Long, zone: ZoneId = ZoneId.systemDefault()): String {
    val d = Instant.ofEpochMilli(ms).atZone(zone).toLocalDate()
    return d.format(DateTimeFormatter.ofPattern("yyyy-MM-dd", Locale.US))
}

private fun formatCachedAt(ms: Long, zone: ZoneId = ZoneId.systemDefault()): String {
    if (ms <= 0L) return ""
    val zdt = Instant.ofEpochMilli(ms).atZone(zone)
    return String.format(Locale.US, "%02d/%02d %02d:%02d", zdt.monthValue, zdt.dayOfMonth, zdt.hour, zdt.minute)
}

private data class ActiveSummary(
    val uncached: Long,
    val read: Long,
    val write: Long,
    val out: Long,
    val reas: Long,
    val total: Long,
    val usageReqs: Long,
    val speedReqs: Long,
    val hitRate: Double,
    val inDenom: Long,
    val tokDeltaPct: Double?,
    val prevLabel: String,
    val hasWeightedSpeed: Boolean,
    val hasOutlier: Boolean,
    val primary: UsageSeriesView?,
)

private data class UsageLinePoint(
    val bucketIndex: Int,
    val xFrac: Float,
    val value: Double,
    val lowValue: Double?,
    val highValue: Double?,
)

private data class UsageLineSeries(
    val series: UsageSeriesView,
    val slot: Int,
    val few: Boolean,
    val segments: List<List<UsageLinePoint>>,
)

private data class UsageChartModel(
    val isBars: Boolean,
    val bucketCount: Int,
    val yMax: Double,
    val yLabels: List<String>,
    val xLabels: List<String>,
    val barTotals: LongArray,
    val barSeriesValues: List<Pair<Int, LongArray>>,
    val lineSeries: List<UsageLineSeries>,
)

private fun buildAndroidUsageChart(
    selectedSeries: List<UsageSeriesView>,
    bucketCount: Int,
    metric: UsageMetricTab,
    granularity: UsageBucketGranularity,
    copy: MessageResolver,
): UsageChartModel? {
    if (bucketCount <= 0 || selectedSeries.isEmpty()) return null
    val firstBuckets = selectedSeries.first().bucketsList
    val xLabels =
        if (firstBuckets.isNotEmpty()) {
            listOf(
                formatBucketAxisTime(firstBuckets.first().startMs, granularity),
                formatBucketAxisTime(firstBuckets[(firstBuckets.size - 1) / 2].startMs, granularity),
                formatBucketAxisTime(firstBuckets.last().startMs, granularity),
            )
        } else {
            emptyList()
        }

    if (metric == UsageMetricTab.USAGE_METRIC_TAB_TOKENS) {
        val totals = LongArray(bucketCount)
        val seriesRows = ArrayList<Pair<Int, LongArray>>(selectedSeries.size)
        for ((slot, s) in selectedSeries.withIndex()) {
            val arr = LongArray(bucketCount)
            for (bi in 0 until bucketCount) {
                val b = s.bucketsList.getOrNull(bi)
                val v = if (b != null) usageBucketTokens(b) else 0L
                arr[bi] = v
                totals[bi] += v
            }
            seriesRows += slot to arr
        }
        val peak = totals.maxOrNull()?.coerceAtLeast(1L) ?: 1L
        val yMax = ceil(peak * 1.12).coerceAtLeast(1.0)
        val yLabels = listOf(
            formatUsageTokens(yMax.roundToLong(), copy),
            formatUsageTokens((yMax / 2.0).roundToLong(), copy),
            "0",
        )
        return UsageChartModel(
            isBars = true,
            bucketCount = bucketCount,
            yMax = yMax,
            yLabels = yLabels,
            xLabels = xLabels,
            barTotals = totals,
            barSeriesValues = seriesRows,
            lineSeries = emptyList(),
        )
    }

    val maxY: Double =
        if (metric == UsageMetricTab.USAGE_METRIC_TAB_CACHE_HIT) {
            100.0
        } else {
            var peak = 0.0
            for (s in selectedSeries) {
                val few = seriesIsFewSamples(s, metric)
                for (b in s.bucketsList) {
                    if (metric == UsageMetricTab.USAGE_METRIC_TAB_OUTPUT_SPEED && bucketHasSpeed(b)) {
                        peak = max(peak, if (!few && b.speedP90 > 0.0) b.speedP90 else b.speedP50)
                    } else if (metric == UsageMetricTab.USAGE_METRIC_TAB_TTFT && bucketHasTtft(b)) {
                        peak = max(peak, if (!few && b.ttftP90Ms > 0.0) b.ttftP90Ms else b.ttftP50Ms)
                    }
                }
            }
            max(if (metric == UsageMetricTab.USAGE_METRIC_TAB_TTFT) 200.0 else 10.0, ceil(peak * 1.15))
        }

    val yLabels =
        if (metric == UsageMetricTab.USAGE_METRIC_TAB_CACHE_HIT) {
            listOf("100$UNIT_PCT", "50$UNIT_PCT", "0$UNIT_PCT")
        } else {
            val mid = maxY / 2.0
            listOf(
                if (metric == UsageMetricTab.USAGE_METRIC_TAB_TTFT) formatUsageTtft(maxY) else formatUsageSpeed(maxY),
                if (metric == UsageMetricTab.USAGE_METRIC_TAB_TTFT) formatUsageTtft(mid) else formatUsageSpeed(mid),
                "0",
            )
        }

    val lineSeries = selectedSeries.mapIndexed { slot, s ->
        val few = seriesIsFewSamples(s, metric)
        val segs = ArrayList<List<UsageLinePoint>>()
        var curSeg = ArrayList<UsageLinePoint>()
        for (bi in 0 until bucketCount) {
            val b = s.bucketsList.getOrNull(bi)
            var v: Double? = null
            var lo: Double? = null
            var hi: Double? = null
            if (b != null) {
                when (metric) {
                    UsageMetricTab.USAGE_METRIC_TAB_CACHE_HIT -> {
                        val r = bucketCacheHit(b)
                        if (r != null) v = r * 100.0
                    }
                    UsageMetricTab.USAGE_METRIC_TAB_OUTPUT_SPEED -> if (bucketHasSpeed(b)) {
                        v = b.speedP50
                        if (!few && b.speedP10 > 0.0 && b.speedP90 >= b.speedP10) {
                            lo = b.speedP10
                            hi = b.speedP90
                        }
                    }
                    UsageMetricTab.USAGE_METRIC_TAB_TTFT -> if (bucketHasTtft(b)) {
                        v = b.ttftP50Ms
                        if (!few && b.ttftP10Ms > 0.0 && b.ttftP90Ms >= b.ttftP10Ms) {
                            lo = b.ttftP10Ms
                            hi = b.ttftP90Ms
                        }
                    }
                    else -> {}
                }
            }
            if (v == null) {
                if (curSeg.isNotEmpty()) {
                    segs.add(curSeg)
                    curSeg = ArrayList()
                }
            } else {
                val xFrac = if (bucketCount <= 1) 0.5f else bi.toFloat() / (bucketCount - 1).toFloat()
                curSeg.add(UsageLinePoint(bi, xFrac, v, lo, hi))
            }
        }
        if (curSeg.isNotEmpty()) segs.add(curSeg)
        UsageLineSeries(series = s, slot = slot, few = few, segments = segs)
    }

    return UsageChartModel(
        isBars = false,
        bucketCount = bucketCount,
        yMax = maxY,
        yLabels = yLabels,
        xLabels = xLabels,
        barTotals = LongArray(0),
        barSeriesValues = emptyList(),
        lineSeries = lineSeries,
    )
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
            metric = UsageMetricTab.USAGE_METRIC_TAB_TOKENS,
        )
    }

    UsageScreenContent(
        vm = view,
        onBack = nav.back,
        onSelectMetric = { remote.coreHost.setUsageMetric(it) },
        onSelectPreset = { preset ->
            remote.coreHost.openUsageScreen(
                machineId = machineId,
                focusAgent = focusAgent,
                focusModel = focusModel,
                preset = preset,
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
            )
        },
        onRetry = { remote.coreHost.retryUsageScreen(machineId) },
    )
}

@Composable
fun UsageScreenContent(
    vm: UsageViewModel,
    onBack: () -> Unit = {},
    onSelectMetric: (UsageMetricTab) -> Unit = {},
    onSelectPreset: (UsageRangePreset) -> Unit = {},
    onSelectCustomRange: (Long, Long) -> Unit = { _, _ -> },
    onRetry: () -> Unit = {},
) {
    val copy = uiText()
    val p = pal
    val largeFont = LocalDensity.current.fontScale >= 1.8f
    val metric =
        if (vm.metric == UsageMetricTab.USAGE_METRIC_TAB_UNSPECIFIED) UsageMetricTab.USAGE_METRIC_TAB_TOKENS
        else vm.metric
    val preset =
        if (vm.preset == UsageRangePreset.USAGE_RANGE_PRESET_UNSPECIFIED) UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS
        else vm.preset

    var showSkeleton by remember(vm.machineId) { mutableStateOf(false) }
    val hasSeries = vm.seriesList.isNotEmpty()
    LaunchedEffect(vm.loading, hasSeries) {
        if (!vm.loading || hasSeries) {
            showSkeleton = false
        } else {
            delay(200)
            showSkeleton = true
        }
    }

    val clampedDefault = remember(vm.startMs, vm.endMs) {
        val rawEnd = if (vm.endMs > 0L) vm.endMs - DAY_MS else 0L
        clampUsageCustomRange(vm.startMs, rawEnd)
    }
    var customPickerOpen by remember { mutableStateOf(false) }

    var selectedKeys by remember(vm.machineId) { mutableStateOf(emptyList<String>()) }
    val seriesSig = remember(vm.seriesList) {
        vm.seriesList.joinToString("|") { "${it.seriesKey}:${if (it.focused) 1 else 0}" }
    }
    LaunchedEffect(seriesSig) {
        selectedKeys = initialUsageSelection(vm.seriesList)
    }
    val selectedSeries = remember(vm.seriesList, selectedKeys) {
        val byKey = vm.seriesList.associateBy { it.seriesKey }
        val picked = selectedKeys.mapNotNull { byKey[it] }
        picked.ifEmpty { vm.seriesList.take(1) }
    }

    var scrubIndex by remember(metric, preset, vm.startMs, vm.endMs, selectedKeys) { mutableStateOf<Int?>(null) }

    val name = vm.machineName.ifEmpty { vm.machineId.ifEmpty { copy(UiMessages.commonUnknownComputer()) } }
    val subtitle =
        if (vm.found) {
            if (vm.cachedOffline || !vm.online) copy(UiMessages.usageSubtitleOffline(name))
            else copy(UiMessages.usageSubtitleLive(name))
        } else {
            ""
        }
    val errStr = if (vm.hasErrorText()) uiText(vm.errorText) else ""

    val allSourcesUnavailable =
        vm.found &&
            vm.supported &&
            ((vm.sourcesList.isNotEmpty() && vm.sourcesList.all { it.state == UsageSourceState.USAGE_SOURCE_STATE_UNAVAILABLE }) ||
                (!hasSeries && vm.hasUnsupportedFootnote() && vm.unsupportedFootnote.key == "usage.only_unavailable_title"))
    val unavailAgentNames = vm.sourcesList
        .filter { it.state == UsageSourceState.USAGE_SOURCE_STATE_UNAVAILABLE }
        .joinToString(", ") { agentName(it.agent, copy = copy) }
        .ifEmpty { agentName(Agent.AGENT_ANTIGRAVITY, copy = copy) }

    val summary = remember(selectedSeries, preset, copy) {
        var uncached = 0L
        var read = 0L
        var write = 0L
        var out = 0L
        var reas = 0L
        var prevTok = 0L
        var usageReqs = 0L
        var speedReqs = 0L
        var hasWeightedSpeed = false
        var hasOutlier = false
        for (s in selectedSeries) {
            if (!s.hasCurrent()) continue
            val c = s.current
            uncached += c.uncachedInputTokens
            read += c.cacheReadTokens
            write += c.cacheWriteTokens
            out += c.outputTokens
            reas += c.reasoningOutputTokens
            usageReqs += c.usageRequestCount
            speedReqs += c.speedRequestCount
            if (s.hasPrevious()) {
                val pr = s.previous
                prevTok += pr.uncachedInputTokens + pr.cacheReadTokens + pr.cacheWriteTokens + pr.outputTokens
            }
            if (c.hasSpeed) hasWeightedSpeed = true
            if (c.speedP50 > 0.0 && c.speedP90 > c.speedP50 * 4.0) hasOutlier = true
            if (c.ttftP50Ms > 0.0 && c.ttftP90Ms > c.ttftP50Ms * 4.0) hasOutlier = true
        }
        val total = uncached + read + write + out
        val inDenom = uncached + read + write
        val hitRate = if (inDenom > 0L) read.toDouble() / inDenom.toDouble() else 0.0
        val prevLabel = when (preset) {
            UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS -> copy(UiMessages.usageSubVsPrev7D())
            UsageRangePreset.USAGE_RANGE_PRESET_THIRTY_DAYS -> copy(UiMessages.usageSubVsPrev30D())
            else -> copy(UiMessages.usageSubVsPrevGeneric())
        }
        val anyPrevBf = selectedSeries.any { it.previousBackfilling }
        val tokDeltaPct =
            if (!anyPrevBf && usageReqs >= 3L && prevTok > 0L) ((total - prevTok).toDouble() / prevTok.toDouble()) * 100.0
            else null
        ActiveSummary(
            uncached = uncached,
            read = read,
            write = write,
            out = out,
            reas = reas,
            total = total,
            usageReqs = usageReqs,
            speedReqs = speedReqs,
            hitRate = hitRate,
            inDenom = inDenom,
            tokDeltaPct = tokDeltaPct,
            prevLabel = prevLabel,
            hasWeightedSpeed = hasWeightedSpeed,
            hasOutlier = hasOutlier,
            primary = selectedSeries.firstOrNull(),
        )
    }

    val anySelectedFewSamples =
        metric != UsageMetricTab.USAGE_METRIC_TAB_TOKENS &&
            selectedSeries.isNotEmpty() &&
            selectedSeries.all { seriesIsFewSamples(it, metric) }

    val pairHeadline = when {
        selectedSeries.size == 1 -> {
            val s = selectedSeries.first()
            "${s.agentName} · ${s.model}"
        }
        selectedKeys.size >= MAX_USAGE_OVERLAY_SERIES && vm.seriesList.size > MAX_USAGE_OVERLAY_SERIES ->
            copy(UiMessages.usagePairsCappedSummary(vm.seriesList.size))
        selectedSeries.size < vm.seriesList.size ->
            copy(UiMessages.usagePairsSummary(count = vm.seriesList.size, name = name))
        else -> "$name · ${copy(UiMessages.usageFilterAll())}"
    }

    val bucketCount = selectedSeries.maxOfOrNull { it.bucketsCount } ?: 0
    val chartData = remember(selectedSeries, bucketCount, metric, vm.granularity, copy) {
        buildAndroidUsageChart(selectedSeries, bucketCount, metric, vm.granularity, copy)
    }

    val kpiHeadline = TextStyle(
        fontSize = 30.sp,
        lineHeight = 34.sp,
        fontWeight = FontWeight.SemiBold,
        letterSpacing = (-0.5).sp,
    ).digits()

    Column(Modifier.fillMaxSize().background(p.paper).testTag("usage-page")) {
        PageBar(
            title = copy(UiMessages.usageTitle()),
            onBack = onBack,
            subtitle = subtitle.ifEmpty { null },
        )
        Column(
            Modifier.weight(1f)
                .fillMaxWidth()
                .readableColumn()
                .verticalScrollFaded()
                .padding(horizontal = 16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            val metrics = listOf(
                UsageMetricTab.USAGE_METRIC_TAB_TOKENS to copy(UiMessages.usageTabTokens()),
                UsageMetricTab.USAGE_METRIC_TAB_CACHE_HIT to copy(UiMessages.usageTabCacheHit()),
                UsageMetricTab.USAGE_METRIC_TAB_OUTPUT_SPEED to copy(UiMessages.usageTabSpeed()),
                UsageMetricTab.USAGE_METRIC_TAB_TTFT to copy(UiMessages.usageTabTtft()),
            )
            Segmented(
                items = metrics.map { it.second },
                selected = metrics.indexOfFirst { it.first == metric }.coerceAtLeast(0),
                onSelect = { idx -> onSelectMetric(metrics[idx].first) },
                modifier = Modifier.fillMaxWidth(),
            )

            val ranges = listOf(
                UsageRangePreset.USAGE_RANGE_PRESET_TODAY to copy(UiMessages.usageRangeToday()),
                UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS to copy(UiMessages.usageRange7D()),
                UsageRangePreset.USAGE_RANGE_PRESET_THIRTY_DAYS to copy(UiMessages.usageRange30D()),
                UsageRangePreset.USAGE_RANGE_PRESET_CUSTOM to copy(UiMessages.usageRangeCustom()),
            )
            Segmented(
                items = ranges.map { it.second },
                selected = ranges.indexOfFirst { it.first == preset }.coerceAtLeast(0),
                onSelect = { idx ->
                    val chosen = ranges[idx].first
                    if (chosen == UsageRangePreset.USAGE_RANGE_PRESET_CUSTOM) {
                        customPickerOpen = true
                    } else {
                        onSelectPreset(chosen)
                    }
                },
                modifier = Modifier.fillMaxWidth(),
            )

            if (preset == UsageRangePreset.USAGE_RANGE_PRESET_CUSTOM) {
                val rangeStart = if (vm.startMs > 0L) vm.startMs else clampedDefault.first
                val rangeEndInc = if (vm.endMs > 0L) vm.endMs - DAY_MS else clampedDefault.second
                val days = if (vm.startMs > 0L && vm.endMs > vm.startMs) max(1, ((vm.endMs - vm.startMs) / DAY_MS).toInt()) else 7
                Row(
                    Modifier.fillMaxWidth()
                        .clip(Radii.R14)
                        .background(p.gbg)
                        .clickable(role = Role.Button) { customPickerOpen = true }
                        .padding(horizontal = 14.dp, vertical = 10.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    Text(
                        copy(
                            UiMessages.usageCustomRangeSummary(
                                days = days,
                                end = formatShortDay(rangeEndInc),
                                start = formatShortDay(rangeStart),
                            ),
                        ),
                        style = T.s13.digits(),
                        color = p.ink2,
                        modifier = Modifier.weight(1f),
                    )
                    Text(copy(UiMessages.usageCustomRangeChange()), style = T.s13b, color = p.blue)
                }
            }

            if (vm.cachedOffline && hasSeries) {
                val cachedStr = formatCachedAt(vm.cachedAtMs)
                Notice(
                    copy(UiMessages.usageOfflineBanner(name = name, time = cachedStr)),
                    tone = NoticeTone.WARN,
                    action = copy(UiMessages.commonRetry()),
                    onAction = onRetry,
                )
            }
            if (vm.backfilling) {
                Notice(
                    copy(UiMessages.usageBackfillingBanner(scanned = vm.backfillScannedDays, total = vm.backfillTotalDays)),
                    tone = NoticeTone.PLAIN,
                )
            }
            if (errStr.isNotEmpty() && hasSeries) {
                Notice(
                    errStr,
                    tone = NoticeTone.ERROR,
                    action = copy(UiMessages.commonRetry()),
                    onAction = onRetry,
                )
            }

            when {
                !vm.found -> PageNote(copy(UiMessages.settingsAndroidThisComputerIsOutsideTheCurrentTrustedDirectoryCheckThe()))
                !vm.supported -> Notice(
                    "${copy(UiMessages.usageUnsupportedTitle())}\n${copy(UiMessages.usageUnsupportedBody(name))}",
                    tone = NoticeTone.WARN,
                )
                vm.loading && !hasSeries -> {
                    if (showSkeleton) UsageLoadingSkeleton()
                }
                vm.cachedOffline && !hasSeries -> Notice(
                    "${copy(UiMessages.usageOfflineEmptyTitle(name))}\n${copy(UiMessages.usageOfflineEmptyBody(name))}",
                    tone = NoticeTone.WARN,
                    action = copy(UiMessages.commonRetry()),
                    onAction = onRetry,
                )
                errStr.isNotEmpty() && !hasSeries -> Notice(
                    "${copy(UiMessages.usageLoadErrorTitle())}\n$errStr",
                    tone = NoticeTone.ERROR,
                    action = copy(UiMessages.commonRetry()),
                    onAction = onRetry,
                )
                allSourcesUnavailable -> Notice(
                    "${copy(UiMessages.usageOnlyUnavailableTitle(unavailAgentNames))}\n${copy(UiMessages.usageOnlyUnavailableBody(unavailAgentNames))}",
                    tone = NoticeTone.PLAIN,
                )
                !hasSeries -> Notice(
                    "${copy(UiMessages.usageEmptyTitle())}\n${copy(UiMessages.usageEmptyBody())}",
                    tone = NoticeTone.PLAIN,
                )
                else -> {
                    Column(Modifier.fillMaxWidth().statusCard(p).padding(16.dp)) {
                        UsageCardHeader(
                            metric = metric,
                            pairHeadline = pairHeadline,
                            summary = summary,
                            anyFewSamples = anySelectedFewSamples,
                            kpiStyle = kpiHeadline,
                            copy = copy,
                        )
                        Spacer(Modifier.height(10.dp))

                        if (scrubIndex != null && chartData != null) {
                            UsageScrubCallout(
                                chart = chartData,
                                metric = metric,
                                bucketIndex = scrubIndex!!,
                                buckets = selectedSeries.first().bucketsList,
                                granularity = vm.granularity,
                                copy = copy,
                            )
                            Spacer(Modifier.height(6.dp))
                        }

                        if (chartData != null) {
                            val chartA11y =
                                if (metric == UsageMetricTab.USAGE_METRIC_TAB_TOKENS) {
                                    copy(
                                        UiMessages.usageChartA11YTokens(
                                            count = bucketCount,
                                            name = pairHeadline,
                                            total = formatUsageTokens(summary.total, copy),
                                        ),
                                    )
                                } else {
                                    copy(
                                        UiMessages.usageChartA11YLine(
                                            count = bucketCount,
                                            metric = metrics.firstOrNull { it.first == metric }?.second.orEmpty(),
                                            name = pairHeadline,
                                        ),
                                    )
                                }
                            UsageChartCanvas(
                                chart = chartData,
                                metric = metric,
                                scrubIndex = scrubIndex,
                                a11yDescription = chartA11y,
                                onScrub = { next -> scrubIndex = next },
                            )
                        }

                        if (metric == UsageMetricTab.USAGE_METRIC_TAB_TOKENS && summary.total > 0L) {
                            Spacer(Modifier.height(14.dp))
                            UsageCompositionBar(summary = summary, anyWriteSupported = selectedSeries.any { it.cacheWriteSupported }, copy = copy)
                        }

                        Spacer(Modifier.height(14.dp))
                        Box(Modifier.fillMaxWidth().height(0.5.dp).background(p.sep))
                        Spacer(Modifier.height(10.dp))

                        UsageSeriesList(
                            series = vm.seriesList,
                            selectedKeys = selectedKeys,
                            metric = metric,
                            largeFont = largeFont,
                            copy = copy,
                            onToggleSeries = { key ->
                                selectedKeys = toggleUsageOverlaySelection(selectedKeys, key)
                            },
                        )

                        if (anySelectedFewSamples && summary.speedReqs > 0L) {
                            Spacer(Modifier.height(10.dp))
                            Hint(copy(UiMessages.usageFewSamplesHint()), Modifier.fillMaxWidth())
                        }
                        if ((metric == UsageMetricTab.USAGE_METRIC_TAB_OUTPUT_SPEED || metric == UsageMetricTab.USAGE_METRIC_TAB_TTFT) && summary.hasOutlier) {
                            Spacer(Modifier.height(8.dp))
                            Hint(copy(UiMessages.usageOutlierNote()), Modifier.fillMaxWidth())
                        }
                    }

                    UsageFootnotes(
                        vm = vm,
                        metric = metric,
                        summary = summary,
                        copy = copy,
                    )
                }
            }
            Spacer(Modifier.height(20.dp))
        }
    }

    if (customPickerOpen) {
        val initStart = if (vm.startMs > 0L) vm.startMs else clampedDefault.first
        val initEnd = if (vm.endMs > 0L) vm.endMs - DAY_MS else clampedDefault.second
        UsageCustomRangeDialog(
            initialStartMs = initStart,
            initialEndMs = initEnd,
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
private fun UsageLoadingSkeleton() {
    val p = pal
    Column(Modifier.fillMaxWidth().statusCard(p).padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Box(Modifier.fillMaxWidth(0.45f).height(14.dp).clip(RoundedCornerShape(10.dp)).background(p.fill))
        Box(Modifier.fillMaxWidth(0.35f).height(30.dp).clip(RoundedCornerShape(10.dp)).background(p.fill))
        Box(Modifier.fillMaxWidth().height(140.dp).clip(Radii.R14).background(p.fill))
        Box(Modifier.fillMaxWidth().height(42.dp).clip(Radii.R14).background(p.fill))
    }
}

@Composable
private fun UsageCardHeader(
    metric: UsageMetricTab,
    pairHeadline: String,
    summary: ActiveSummary,
    anyFewSamples: Boolean,
    kpiStyle: TextStyle,
    copy: MessageResolver,
) {
    val p = pal
    val prim = summary.primary
    val rightCol: @Composable (List<String>) -> Unit = { lines ->
        Column(horizontalAlignment = Alignment.End) {
            lines.forEachIndexed { idx, line ->
                Text(
                    line,
                    style = T.s12.digits(),
                    color = if (idx == 0) p.ink else p.ink2,
                    textAlign = TextAlign.End,
                )
            }
        }
    }
    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.Top) {
        Column(Modifier.weight(1f).padding(end = 10.dp)) {
            Text(pairHeadline, style = T.s12, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Spacer(Modifier.height(2.dp))
            when (metric) {
                UsageMetricTab.USAGE_METRIC_TAB_TOKENS -> Row(verticalAlignment = Alignment.Bottom, horizontalArrangement = Arrangement.spacedBy(5.dp)) {
                    Text(formatUsageTokens(summary.total, copy), style = kpiStyle, color = p.ink)
                    Text(copy(UiMessages.usageKpiUnitTokens()), style = T.s13, color = p.ink2, modifier = Modifier.padding(bottom = 4.dp))
                }
                UsageMetricTab.USAGE_METRIC_TAB_CACHE_HIT -> Text(
                    if (summary.inDenom > 0L) formatUsageCacheHit(summary.hitRate) else EM_DASH,
                    style = kpiStyle,
                    color = p.ink,
                )
                UsageMetricTab.USAGE_METRIC_TAB_OUTPUT_SPEED -> {
                    val c = prim?.takeIf { it.hasCurrent() }?.current
                    val showNum = !anyFewSamples && c != null && c.hasSpeed && c.speedRequestCount >= USAGE_FEW_SAMPLES_MIN
                    if (showNum) {
                        Row(verticalAlignment = Alignment.Bottom, horizontalArrangement = Arrangement.spacedBy(5.dp)) {
                            Text(formatUsageSpeed(c!!.speedP50), style = kpiStyle, color = p.ink)
                            Text(copy(UiMessages.usageKpiUnitToks()), style = T.s13, color = p.ink2, modifier = Modifier.padding(bottom = 4.dp))
                        }
                    } else {
                        Text(
                            if (anyFewSamples) copy(UiMessages.usageFewSamplesCount(summary.speedReqs.toInt())) else EM_DASH,
                            style = T.h20,
                            color = p.ink2,
                        )
                    }
                }
                else -> {
                    val c = prim?.takeIf { it.hasCurrent() }?.current
                    val showNum = !anyFewSamples && c != null && c.hasTtft && c.speedRequestCount >= USAGE_FEW_SAMPLES_MIN
                    Text(
                        when {
                            showNum -> formatUsageTtft(c!!.ttftP50Ms)
                            anyFewSamples -> copy(UiMessages.usageFewSamplesCount(summary.speedReqs.toInt()))
                            else -> EM_DASH
                        },
                        style = if (showNum) kpiStyle else T.h20,
                        color = if (showNum) p.ink else p.ink2,
                    )
                }
            }
        }
        when (metric) {
            UsageMetricTab.USAGE_METRIC_TAB_TOKENS -> {
                val deltaStr = summary.tokDeltaPct?.let { formatUsageSignedPercent(it, copy) } ?: EM_DASH
                val hitStr = if (summary.inDenom > 0L) formatUsageCacheHit(summary.hitRate) else EM_DASH
                rightCol(
                    listOf(
                        "$deltaStr ${summary.prevLabel}",
                        "${copy(UiMessages.usageColHitRate())} $hitStr",
                    ),
                )
            }
            UsageMetricTab.USAGE_METRIC_TAB_CACHE_HIT -> {
                val sub1 =
                    if (prim != null && prim.hasCacheHitDelta && !prim.previousBackfilling) {
                        "${formatUsageSignedPoints(prim.cacheHitDeltaPoints, copy)} ${summary.prevLabel}"
                    } else {
                        "${formatUsageCommaUint(summary.usageReqs)} ${copy(UiMessages.usageColReqs())}"
                    }
                rightCol(listOf(sub1, copy(UiMessages.usageSubWeightedCache())))
            }
            UsageMetricTab.USAGE_METRIC_TAB_OUTPUT_SPEED -> {
                val c = prim?.takeIf { it.hasCurrent() }?.current
                val p10 = if (c != null && c.speedP10 > 0.0) formatUsageSpeed(c.speedP10) else EM_DASH
                val p90 = if (c != null && c.speedP90 > 0.0) formatUsageSpeed(c.speedP90) else EM_DASH
                val sub2 =
                    if (c != null && c.recent10SpeedCount > 0L && c.recent10SpeedP50 > 0.0) {
                        copy(UiMessages.usageSubRecent10(formatUsageSpeed(c.recent10SpeedP50)))
                    } else {
                        "${formatUsageCommaUint(summary.speedReqs)} ${copy(UiMessages.usageColValidReqs())}"
                    }
                rightCol(listOf(copy(UiMessages.usageSubMedianP10P90(high = p90, low = p10)), sub2))
            }
            else -> {
                val c = prim?.takeIf { it.hasCurrent() }?.current
                val p10 = if (c != null && c.ttftP10Ms > 0.0) formatUsageTtft(c.ttftP10Ms) else EM_DASH
                val p90 = if (c != null && c.ttftP90Ms > 0.0) formatUsageTtft(c.ttftP90Ms) else EM_DASH
                rightCol(
                    listOf(
                        copy(UiMessages.usageSubP10P90(high = p90, low = p10)),
                        "${formatUsageCommaUint(summary.speedReqs)} ${copy(UiMessages.usageColValidReqs())}",
                    ),
                )
            }
        }
    }
}

@Composable
private fun UsageScrubCallout(
    chart: UsageChartModel,
    metric: UsageMetricTab,
    bucketIndex: Int,
    buckets: List<UsageBucketPoint>,
    granularity: UsageBucketGranularity,
    copy: MessageResolver,
) {
    val p = pal
    val b = buckets.getOrNull(bucketIndex) ?: return
    val timeLabel = formatBucketAxisTime(b.startMs, granularity)
    val valueText = when (metric) {
        UsageMetricTab.USAGE_METRIC_TAB_TOKENS ->
            copy(UiMessages.usageScrubTotal(formatUsageTokens(chart.barTotals.getOrElse(bucketIndex) { 0L }, copy)))
        else -> chart.lineSeries.mapNotNull { ls ->
            val pt = ls.segments.flatten().firstOrNull { it.bucketIndex == bucketIndex } ?: return@mapNotNull null
            val formatted = when (metric) {
                UsageMetricTab.USAGE_METRIC_TAB_CACHE_HIT -> formatUsageCacheHit(pt.value / 100.0)
                UsageMetricTab.USAGE_METRIC_TAB_TTFT -> formatUsageTtft(pt.value)
                else -> "${formatUsageSpeed(pt.value)} ${copy(UiMessages.usageKpiUnitToks())}"
            }
            "${ls.series.model}: $formatted"
        }.joinToString(" · ").ifEmpty { EM_DASH }
    }
    Row(
        Modifier.fillMaxWidth()
            .clip(RoundedCornerShape(10.dp))
            .background(p.paper)
            .padding(horizontal = 10.dp, vertical = 6.dp),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(timeLabel, style = T.s12.digits(), color = p.ink2)
        Text(valueText, style = T.s12b.digits(), color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

@Composable
private fun UsageChartCanvas(
    chart: UsageChartModel,
    metric: UsageMetricTab,
    scrubIndex: Int?,
    a11yDescription: String,
    onScrub: (Int?) -> Unit,
) {
    val p = pal
    val bar2 = p.ink2
    val bandFill = p.ink.copy(alpha = 0.08f)
    val bandAlt = p.ink2.copy(alpha = 0.06f)

    Column(Modifier.fillMaxWidth()) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.Top) {
            Column(
                Modifier.height(148.dp).padding(end = 6.dp),
                verticalArrangement = Arrangement.SpaceBetween,
                horizontalAlignment = Alignment.End,
            ) {
                for (lbl in chart.yLabels) {
                    Text(lbl, style = T.s12.digits(), color = p.ink3)
                }
            }
            Canvas(
                Modifier.weight(1f)
                    .height(148.dp)
                    .semantics { contentDescription = a11yDescription }
                    .pointerInput(chart.bucketCount, metric) {
                        detectTapGestures { pos ->
                            if (chart.bucketCount <= 0 || size.width <= 0) return@detectTapGestures
                            val frac = (pos.x / size.width.toFloat()).coerceIn(0f, 0.9999f)
                            val idx = (frac * chart.bucketCount).toInt().coerceIn(0, chart.bucketCount - 1)
                            onScrub(if (scrubIndex == idx) null else idx)
                        }
                    }
                    .pointerInput(chart.bucketCount, metric) {
                        detectHorizontalDragGestures(
                            onDragEnd = {},
                            onDragCancel = { onScrub(null) },
                        ) { change, _ ->
                            if (chart.bucketCount <= 0 || size.width <= 0) return@detectHorizontalDragGestures
                            change.consume()
                            val frac = (change.position.x / size.width.toFloat()).coerceIn(0f, 0.9999f)
                            val idx = (frac * chart.bucketCount).toInt().coerceIn(0, chart.bucketCount - 1)
                            onScrub(idx)
                        }
                    },
            ) {
                val topY = 8.dp.toPx()
                val baseY = size.height - 8.dp.toPx()
                val plotH = max(1f, baseY - topY)
                val dash = PathEffect.dashPathEffect(floatArrayOf(3.dp.toPx(), 3.dp.toPx()))
                drawLine(p.sep, Offset(0f, topY), Offset(size.width, topY), strokeWidth = 0.7.dp.toPx(), pathEffect = dash)
                drawLine(p.sep, Offset(0f, topY + plotH / 2f), Offset(size.width, topY + plotH / 2f), strokeWidth = 0.7.dp.toPx(), pathEffect = dash)
                drawLine(p.sep, Offset(0f, baseY), Offset(size.width, baseY), strokeWidth = 1.dp.toPx())

                if (chart.isBars && chart.bucketCount > 0) {
                    val step = size.width / chart.bucketCount.toFloat()
                    val barW = max(3.dp.toPx(), min(18.dp.toPx(), step * 0.62f))
                    val hatchStep = 5.dp.toPx()
                    for (bi in 0 until chart.bucketCount) {
                        val cx = (bi + 0.5f) * step
                        val bx = cx - barW / 2f
                        val dimmed = scrubIndex != null && scrubIndex != bi
                        val alpha = if (dimmed) 0.28f else 1f
                        var curY = baseY
                        for ((slot, arr) in chart.barSeriesValues) {
                            val v = arr[bi]
                            if (v <= 0L) continue
                            val segH = max(2.dp.toPx(), ((v.toDouble() / chart.yMax) * plotH).toFloat())
                            curY -= segH
                            when (slot) {
                                0 -> drawRoundRect(
                                    color = p.ink.copy(alpha = alpha),
                                    topLeft = Offset(bx, curY),
                                    size = Size(barW, segH),
                                    cornerRadius = CornerRadius(1.5.dp.toPx()),
                                )
                                1 -> drawRoundRect(
                                    color = bar2.copy(alpha = alpha * 0.72f),
                                    topLeft = Offset(bx, curY),
                                    size = Size(barW, segH),
                                    cornerRadius = CornerRadius(1.5.dp.toPx()),
                                )
                                2 -> {
                                    drawRect(p.paper.copy(alpha = alpha), Offset(bx, curY), Size(barW, segH))
                                    clipRect(bx, curY, bx + barW, curY + segH) {
                                        var x = bx - segH
                                        while (x < bx + barW + segH) {
                                            drawLine(
                                                color = p.ink.copy(alpha = alpha * 0.85f),
                                                start = Offset(x, curY + segH),
                                                end = Offset(x + segH, curY),
                                                strokeWidth = 1.4.dp.toPx(),
                                            )
                                            x += hatchStep
                                        }
                                    }
                                    drawRect(
                                        color = p.ink3.copy(alpha = alpha),
                                        topLeft = Offset(bx, curY),
                                        size = Size(barW, segH),
                                        style = Stroke(width = 0.6.dp.toPx()),
                                    )
                                }
                                else -> {
                                    drawRect(p.fill.copy(alpha = alpha), Offset(bx, curY), Size(barW, segH))
                                    drawRect(
                                        color = p.ink2.copy(alpha = alpha),
                                        topLeft = Offset(bx, curY),
                                        size = Size(barW, segH),
                                        style = Stroke(width = 1.dp.toPx()),
                                    )
                                }
                            }
                        }
                    }
                    if (scrubIndex != null && scrubIndex in 0 until chart.bucketCount) {
                        val sx = (scrubIndex + 0.5f) * step
                        drawLine(p.ink3, Offset(sx, topY), Offset(sx, baseY), strokeWidth = 1.dp.toPx())
                    }
                } else {
                    fun yOf(valNum: Double): Float {
                        val norm = (valNum / chart.yMax).coerceIn(0.0, 1.0).toFloat()
                        return baseY - norm * plotH
                    }
                    for (ls in chart.lineSeries) {
                        if (!ls.few) {
                            for (seg in ls.segments) {
                                val bandPts = seg.filter { it.lowValue != null && it.highValue != null }
                                if (bandPts.size >= 2) {
                                    val band = Path().apply {
                                        moveTo(bandPts.first().xFrac * size.width, yOf(bandPts.first().highValue!!))
                                        for (pt in bandPts.drop(1)) {
                                            lineTo(pt.xFrac * size.width, yOf(pt.highValue!!))
                                        }
                                        for (pt in bandPts.asReversed()) {
                                            lineTo(pt.xFrac * size.width, yOf(pt.lowValue!!))
                                        }
                                        close()
                                    }
                                    drawPath(band, color = if (ls.slot == 0) bandFill else bandAlt)
                                }
                            }
                            for (seg in ls.segments) {
                                if (seg.size < 2) continue
                                val strokePath = Path().apply {
                                    moveTo(seg.first().xFrac * size.width, yOf(seg.first().value))
                                    for (pt in seg.drop(1)) {
                                        lineTo(pt.xFrac * size.width, yOf(pt.value))
                                    }
                                }
                                val strokeColor = if (ls.slot % 2 == 0) p.ink else bar2
                                val strokeWidth = when (ls.slot) {
                                    0 -> 2.2.dp.toPx()
                                    1 -> 1.8.dp.toPx()
                                    2 -> 1.7.dp.toPx()
                                    else -> 1.6.dp.toPx()
                                }
                                val effect = when (ls.slot) {
                                    2 -> PathEffect.dashPathEffect(floatArrayOf(6.dp.toPx(), 4.dp.toPx()))
                                    3 -> PathEffect.dashPathEffect(floatArrayOf(2.dp.toPx(), 4.dp.toPx()))
                                    else -> null
                                }
                                drawPath(
                                    strokePath,
                                    color = strokeColor,
                                    style = Stroke(width = strokeWidth, cap = StrokeCap.Round, join = StrokeJoin.Round, pathEffect = effect),
                                )
                            }
                        }
                        val markerColor = if (ls.slot % 2 == 0) p.ink else bar2
                        val r = 3.2.dp.toPx()
                        for (pt in ls.segments.flatten()) {
                            val cx = pt.xFrac * size.width
                            val cy = yOf(pt.value)
                            when (ls.slot) {
                                0 -> drawCircle(markerColor, radius = r, center = Offset(cx, cy))
                                1 -> {
                                    drawCircle(p.paper, radius = r, center = Offset(cx, cy))
                                    drawCircle(markerColor, radius = r, center = Offset(cx, cy), style = Stroke(width = 1.6.dp.toPx()))
                                }
                                2 -> {
                                    val dia = Path().apply {
                                        moveTo(cx, cy - r * 1.15f)
                                        lineTo(cx + r * 1.15f, cy)
                                        lineTo(cx, cy + r * 1.15f)
                                        lineTo(cx - r * 1.15f, cy)
                                        close()
                                    }
                                    drawPath(dia, markerColor)
                                }
                                else -> drawRect(
                                    markerColor,
                                    topLeft = Offset(cx - r * 0.85f, cy - r * 0.85f),
                                    size = Size(r * 1.7f, r * 1.7f),
                                )
                            }
                        }
                    }
                    if (scrubIndex != null && chart.bucketCount > 0) {
                        val sx =
                            if (chart.bucketCount <= 1) size.width / 2f
                            else (scrubIndex.toFloat() / (chart.bucketCount - 1).toFloat()) * size.width
                        drawLine(p.ink3, Offset(sx, topY), Offset(sx, baseY), strokeWidth = 1.dp.toPx())
                    }
                }
            }
        }
        if (chart.xLabels.isNotEmpty()) {
            Spacer(Modifier.height(4.dp))
            Row(Modifier.fillMaxWidth().padding(start = 34.dp), horizontalArrangement = Arrangement.SpaceBetween) {
                for (lbl in chart.xLabels) {
                    Text(lbl, style = T.s12.digits(), color = p.ink3)
                }
            }
        }
    }
}

@Composable
private fun UsageCompositionBar(
    summary: ActiveSummary,
    anyWriteSupported: Boolean,
    copy: MessageResolver,
) {
    val p = pal
    val total = summary.total.coerceAtLeast(1L).toFloat()
    val parts = listOf(
        Triple(copy(UiMessages.usageCompUncached()), summary.uncached, p.ink),
        Triple(copy(UiMessages.usageCompCacheRead()), summary.read, p.ink2),
        Triple(copy(UiMessages.usageCompCacheWrite()), summary.write, p.ink3),
        Triple(copy(UiMessages.usageCompOutput()), summary.out, p.faint),
    )
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Row(
            Modifier.fillMaxWidth().height(8.dp).clip(Radii.Pill).background(p.fill),
        ) {
            for ((_, count, color) in parts) {
                if (count > 0L) {
                    Box(Modifier.weight(count.toFloat() / total).height(8.dp).background(color))
                }
            }
        }
        val uncachedLabel = "${parts[0].first} ${formatUsageTokens(parts[0].second, copy)}"
        val readLabel = "${parts[1].first} ${formatUsageTokens(parts[1].second, copy)}"
        val writeVal = if (anyWriteSupported) formatUsageTokens(parts[2].second, copy) else EM_DASH
        val writeLabel = "${parts[2].first} $writeVal"
        val reasNote =
            if (summary.reas > 0L) " (${copy(UiMessages.usageCompReasoning(formatUsageTokens(summary.reas, copy)))})"
            else ""
        val outLabel = "${parts[3].first} ${formatUsageTokens(parts[3].second, copy)}$reasNote"
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                Text(uncachedLabel, style = T.s12.digits(), color = p.ink2)
                Text(readLabel, style = T.s12.digits(), color = p.ink2)
            }
            Column(Modifier.weight(1f), horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(2.dp)) {
                Text(writeLabel, style = T.s12.digits(), color = p.ink2)
                Text(outLabel, style = T.s12.digits(), color = p.ink2)
            }
        }
    }
}

@Composable
private fun UsageSeriesSwatch(slot: Int, isBars: Boolean, selected: Boolean) {
    val p = pal
    val alpha = if (selected) 1f else 0.3f
    if (isBars) {
        Canvas(Modifier.size(12.dp).alpha(alpha)) {
            when (slot) {
                0 -> drawRoundRect(p.ink, cornerRadius = CornerRadius(2.dp.toPx()))
                1 -> drawRoundRect(p.ink2, cornerRadius = CornerRadius(2.dp.toPx()))
                2 -> {
                    drawRoundRect(p.paper, cornerRadius = CornerRadius(2.dp.toPx()))
                    clipRect {
                        var x = -size.height
                        while (x < size.width + size.height) {
                            drawLine(p.ink, Offset(x, size.height), Offset(x + size.height, 0f), strokeWidth = 1.4.dp.toPx())
                            x += 4.dp.toPx()
                        }
                    }
                    drawRoundRect(p.ink3, style = Stroke(0.6.dp.toPx()), cornerRadius = CornerRadius(2.dp.toPx()))
                }
                else -> {
                    drawRoundRect(p.fill, cornerRadius = CornerRadius(2.dp.toPx()))
                    drawRoundRect(p.ink2, style = Stroke(1.dp.toPx()), cornerRadius = CornerRadius(2.dp.toPx()))
                }
            }
        }
    } else {
        Canvas(Modifier.size(width = 20.dp, height = 10.dp).alpha(alpha)) {
            val c = if (slot % 2 == 0) p.ink else p.ink2
            val effect = when (slot) {
                2 -> PathEffect.dashPathEffect(floatArrayOf(4.dp.toPx(), 2.dp.toPx()))
                3 -> PathEffect.dashPathEffect(floatArrayOf(1.5.dp.toPx(), 2.5.dp.toPx()))
                else -> null
            }
            drawLine(c, Offset(0f, size.height / 2f), Offset(size.width, size.height / 2f), strokeWidth = 1.6.dp.toPx(), pathEffect = effect)
            drawCircle(c, radius = 2.4.dp.toPx(), center = Offset(size.width / 2f, size.height / 2f))
        }
    }
}

@Composable
private fun UsageSeriesList(
    series: List<UsageSeriesView>,
    selectedKeys: List<String>,
    metric: UsageMetricTab,
    largeFont: Boolean,
    copy: MessageResolver,
    onToggleSeries: (String) -> Unit,
) {
    val p = pal
    val isBars = metric == UsageMetricTab.USAGE_METRIC_TAB_TOKENS
    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
        for (s in series) {
            val slot = selectedKeys.indexOf(s.seriesKey)
            val isSelected = slot >= 0
            val c = if (s.hasCurrent()) s.current else null
            val writeStr = if (s.cacheWriteSupported) formatUsageTokens(c?.cacheWriteTokens ?: 0L, copy) else EM_DASH
            val primaryVal = when (metric) {
                UsageMetricTab.USAGE_METRIC_TAB_TOKENS -> formatUsageTokens(usageSeriesTotalTokens(s), copy)
                UsageMetricTab.USAGE_METRIC_TAB_CACHE_HIT -> if (c != null && c.hasCacheHitRate) formatUsageCacheHit(c.cacheHitRate) else EM_DASH
                UsageMetricTab.USAGE_METRIC_TAB_OUTPUT_SPEED ->
                    if (c != null && c.hasSpeed) "${formatUsageSpeed(c.speedP50)} ${copy(UiMessages.usageKpiUnitToks())}"
                    else EM_DASH
                else -> if (c != null && c.hasTtft) formatUsageTtft(c.ttftP50Ms) else EM_DASH
            }
            val deltaVal = when {
                s.previousBackfilling -> copy(UiMessages.usageBackfillingCell())
                metric == UsageMetricTab.USAGE_METRIC_TAB_TOKENS ->
                    if (s.hasTokensDelta) formatUsageSignedPercent(s.tokensDeltaPercent, copy) else EM_DASH
                metric == UsageMetricTab.USAGE_METRIC_TAB_CACHE_HIT ->
                    if (s.hasCacheHitDelta) formatUsageSignedPoints(s.cacheHitDeltaPoints, copy) else EM_DASH
                metric == UsageMetricTab.USAGE_METRIC_TAB_OUTPUT_SPEED ->
                    if (s.hasSpeedDelta) formatUsageSignedPercent(s.speedDeltaPercent, copy) else EM_DASH
                else ->
                    if (s.hasTtftDelta && s.hasCurrent() && s.hasPrevious()) {
                        formatUsageTtftDelta(s.current.ttftP50Ms, s.previous.ttftP50Ms, copy)
                    } else EM_DASH
            }
            val subText = when (metric) {
                UsageMetricTab.USAGE_METRIC_TAB_TOKENS -> {
                    val inp = formatUsageTokens(c?.uncachedInputTokens ?: 0L, copy)
                    val out = formatUsageTokens(c?.outputTokens ?: 0L, copy)
                    val rd = formatUsageTokens(c?.cacheReadTokens ?: 0L, copy)
                    if ((c?.reasoningOutputTokens ?: 0L) > 0L) {
                        copy(
                            UiMessages.usageRowSubTokensReasoning(
                                inp = inp,
                                out = out,
                                read = rd,
                                reas = formatUsageTokens(c!!.reasoningOutputTokens, copy),
                                write = writeStr,
                            ),
                        )
                    } else {
                        copy(UiMessages.usageRowSubTokens(inp = inp, out = out, read = rd, write = writeStr))
                    }
                }
                UsageMetricTab.USAGE_METRIC_TAB_CACHE_HIT -> copy(
                    UiMessages.usageRowSubCache(
                        inp = formatUsageTokens(c?.uncachedInputTokens ?: 0L, copy),
                        read = formatUsageTokens(c?.cacheReadTokens ?: 0L, copy),
                        write = writeStr,
                    ),
                )
                else -> {
                    val hi = if (c != null && c.speedP90 > 0.0) formatUsageSpeed(c.speedP90) else EM_DASH
                    val lo = if (c != null && c.speedP10 > 0.0) formatUsageSpeed(c.speedP10) else EM_DASH
                    val ttft = if (c != null && c.hasTtft) formatUsageTtft(c.ttftP50Ms) else EM_DASH
                    copy(UiMessages.usageRowSubSpeed(high = hi, low = lo, ttft = ttft))
                }
            }
            val rowModifier = Modifier
                .fillMaxWidth()
                .clip(Radii.R14)
                .background(if (isSelected) p.paper else Color.Transparent)
                .border(
                    width = if (isSelected) 1.dp else 0.5.dp,
                    color = if (isSelected) p.ink3 else p.sep,
                    shape = Radii.R14,
                )
                .clickable(role = Role.Button) { onToggleSeries(s.seriesKey) }
                .padding(horizontal = 12.dp, vertical = 10.dp)

            if (largeFont) {
                Column(rowModifier, verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        UsageSeriesSwatch(slot = if (slot >= 0) slot else 3, isBars = isBars, selected = isSelected)
                        AgentMark(s.agent, T.s13, p.ink)
                        Text(s.model, style = T.s15b, color = p.ink, modifier = Modifier.weight(1f))
                    }
                    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                        Text(primaryVal, style = T.s15b.digits(), color = p.ink)
                        Text(deltaVal, style = T.s13.digits(), color = p.ink2)
                    }
                    Text(subText, style = T.s12.digits(), color = p.ink2)
                }
            } else {
                Column(rowModifier, verticalArrangement = Arrangement.spacedBy(3.dp)) {
                    val rowPairTitle = "${s.agentName} · ${s.model}"
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        UsageSeriesSwatch(slot = if (slot >= 0) slot else 3, isBars = isBars, selected = isSelected)
                        AgentMark(s.agent, T.s13, p.ink)
                        Text(
                            rowPairTitle,
                            style = T.s13b,
                            color = p.ink,
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis,
                            modifier = Modifier.weight(1f),
                        )
                        Text(primaryVal, style = T.s13b.digits(), color = p.ink)
                        Text(deltaVal, style = T.s12.digits(), color = p.ink2)
                    }
                    Text(subText, style = T.s12.digits(), color = p.ink2)
                }
            }
        }
    }
}

@Composable
private fun UsageFootnotes(
    vm: UsageViewModel,
    metric: UsageMetricTab,
    summary: ActiveSummary,
    copy: MessageResolver,
) {
    Column(Modifier.fillMaxWidth().padding(horizontal = 4.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
        if (vm.hasUnsupportedFootnote()) {
            val txt = uiText(vm.unsupportedFootnote)
            if (txt.isNotEmpty()) Text(txt, style = T.s12, color = pal.ink2)
        }
        val unavail = vm.sourcesList.filter { it.state == UsageSourceState.USAGE_SOURCE_STATE_UNAVAILABLE }
        if (unavail.isNotEmpty()) {
            val agentsStr = unavail.joinToString(", ") { agentName(it.agent, copy = copy) }
            Text(copy(UiMessages.usageMixedUnavailableFootnote(agentsStr)), style = T.s12, color = pal.ink2)
        }
        if (vm.showCacheWriteFootnote && metric == UsageMetricTab.USAGE_METRIC_TAB_TOKENS) {
            Text(copy(UiMessages.usageCacheWriteFootnote()), style = T.s12, color = pal.ink2)
        }
        if (metric == UsageMetricTab.USAGE_METRIC_TAB_CACHE_HIT) {
            Text(copy(UiMessages.usageCacheFormulaFootnote()), style = T.s12, color = pal.ink2)
        }
        if (metric == UsageMetricTab.USAGE_METRIC_TAB_OUTPUT_SPEED && summary.speedReqs > 0L) {
            Text(copy(UiMessages.usageSpeedThresholdFootnote()), style = T.s12, color = pal.ink2)
        }
        Text(copy(UiMessages.usagePassiveFootnote()), style = T.s12, color = pal.ink2)
    }
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
