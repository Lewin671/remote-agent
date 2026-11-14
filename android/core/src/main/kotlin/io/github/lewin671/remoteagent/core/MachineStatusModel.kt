package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import java.text.NumberFormat
import java.util.Locale
import kotlin.math.abs
import kotlin.math.max
import kotlin.math.min
import kotlin.math.roundToInt

/*
 * What the computer-status screens draw, as plain values: the three rings, the two small cards, the
 * ranking rows and the chart's geometry. Nothing here knows Compose; words come only from the
 * `machine_status` messages through the [MessageResolver], numbers and units in the language's
 * own format. State is read from the proto and its enums, never from translated text.
 */

/** How a figure reads: not at all, orange for 注意, red for 紧张 (ui-spec A5). */
enum class MachineTone { NORMAL, WARN, CRITICAL }

fun tone(pressure: ResourcePressure): MachineTone = when (pressure) {
    ResourcePressure.RESOURCE_PRESSURE_WARNING -> MachineTone.WARN
    ResourcePressure.RESOURCE_PRESSURE_CRITICAL -> MachineTone.CRITICAL
    else -> MachineTone.NORMAL
}

enum class StatusMetric { CPU, MEMORY }

const val GIB = 1073741824.0

/** Sizes for display: disk in decimal bytes, memory in binary ones (both written GB / MB). */
class StatusText(val copy: MessageResolver, val locale: String) {
    private fun nf(decimals: Int) = NumberFormat.getNumberInstance(Locale.forLanguageTag(locale)).apply {
        minimumFractionDigits = decimals; maximumFractionDigits = decimals; isGroupingUsed = false
    }

    fun number(v: Double, decimals: Int): String = nf(decimals).format(v)
    fun percent(v: Double): String = "${v.coerceIn(0.0, 100_000.0).roundToInt()}%"

    /**
     * The number alone of a size in GB: one decimal under 10, none from 10 (the rings' and cards' style).
     * [binary]: memory is counted in 1024s and still called GB, as the system's own monitor does; disk is decimal, as the Finder.
     */
    fun gbNumber(bytes: Long, oneDecimal: Boolean = false, binary: Boolean = false): String {
        val gb = bytes / (if (binary) GIB else 1e9)
        return number(gb, if (oneDecimal || gb < 10) 1 else 0)
    }

    /** "42 GB", "1.2 GB", "380 MB". */
    fun size(bytes: Long, binary: Boolean = false): String =
        if (bytes >= (if (binary) GIB.toLong() else 1_000_000_000L)) copy(UiMessages.machineStatusSizeGb(gbNumber(bytes, binary = binary)))
        else copy(UiMessages.machineStatusSizeMb(number(Math.round(max(1.0, bytes / (if (binary) 1048576.0 else 1e6))).toDouble(), 0)))

    fun load(v: Double): String = number(v, 1)

    fun duration(seconds: Long): String {
        val minutes = seconds / 60
        val hours = minutes / 60
        val days = hours / 24
        return when {
            days > 0 -> copy(UiMessages.machineStatusDurationDaysHours(days.toInt(), (hours % 24).toInt()))
            hours > 0 -> copy(UiMessages.timeDurationHoursMinutes(hours.toInt(), (minutes % 60).toInt()))
            else -> copy(UiMessages.machineStatusDurationMinutes(max(1L, minutes).toInt()))
        }
    }

    /** 「{N 秒/分钟}前的数据」: the age of what is kept. */
    fun stale(ageMs: Long): String {
        val s = max(0L, ageMs / 1000)
        return if (s < 60) copy(UiMessages.machineStatusStaleSeconds(max(1L, s).toInt()))
        else copy(UiMessages.machineStatusStaleMinutes((s / 60).toInt()))
    }

    /** 「{N} 秒前」 while looking back: N rounded to 2 seconds. */
    fun lookback(ageMs: Long): String = copy(UiMessages.machineStatusSecondsAgo((max(0L, ageMs) / 2000.0).roundToInt() * 2))

    fun cores(n: Int) = copy(UiMessages.machineStatusCores(n))
}

// ---- the three rings (A2) ----------------------------------------------------------------------

enum class RingKind { CPU, MEMORY, DISK }

/** One ring: its progress (null draws only the track), the big figure, the name, how it reads. */
data class RingSpec(val kind: RingKind, val fraction: Float?, val value: String, val name: String, val tone: MachineTone, val spoken: String)

const val NO_VALUE = "—"

/**
 * The rings of a computer. [status] null, or the computer [online] false, draws tracks and a dash.
 * The disk ring draws the share used and says the amount free.
 */
fun machineRings(status: MachineStatus?, online: Boolean, text: StatusText): List<RingSpec> {
    val have = if (online) status else null
    fun ring(kind: RingKind, fraction: Float?, value: String?, name: String, tone: MachineTone): RingSpec {
        val v = value ?: NO_VALUE
        return RingSpec(kind, fraction?.coerceIn(0f, 1f), v, name, tone, text.copy(UiMessages.machineStatusA11YRing(name, v)))
    }
    val cpuName = text.copy(UiMessages.machineStatusCpu())
    val memName = text.copy(UiMessages.machineStatusMemory())
    val diskName = text.copy(UiMessages.machineStatusDiskAvailable())
    if (have == null) return listOf(
        ring(RingKind.CPU, null, null, cpuName, MachineTone.NORMAL),
        ring(RingKind.MEMORY, null, null, memName, MachineTone.NORMAL),
        ring(RingKind.DISK, null, null, diskName, MachineTone.NORMAL),
    )
    val memFraction = if (have.memoryTotalBytes > 0) have.memoryUsedBytes.toFloat() / have.memoryTotalBytes else 0f
    val d = have.disk
    val diskFraction = if (d.totalBytes > 0) 1f - d.availableBytes.toFloat() / d.totalBytes else 0f
    return listOf(
        ring(RingKind.CPU, (have.cpuPercent / 100).toFloat(), text.percent(have.cpuPercent), cpuName, MachineTone.NORMAL),
        ring(RingKind.MEMORY, memFraction, text.percent(memFraction * 100.0), memName, tone(have.memoryPressure)),
        ring(RingKind.DISK, diskFraction, text.size(d.availableBytes), diskName, tone(d.pressure)),
    )
}

// ---- the two small cards and the main figure (A3) ----------------------------------------------

/** The memory card: its figure, the pressure scale (null when the computer does not say) and its two lines. */
data class MemoryCard(
    val used: String, val usedNumber: Double, val pressureWord: UiMessage?, val position: Float?, val tone: MachineTone,
    val total: String, val swap: String?, val detail: String,
)

fun memoryCard(s: MachineStatus, text: StatusText): MemoryCard {
    val word = when (s.memoryPressure) {
        ResourcePressure.RESOURCE_PRESSURE_NORMAL -> UiMessages.machineStatusPressureNormal()
        ResourcePressure.RESOURCE_PRESSURE_WARNING -> UiMessages.machineStatusPressureHigh()
        ResourcePressure.RESOURCE_PRESSURE_CRITICAL -> UiMessages.machineStatusPressureCritical()
        else -> null
    }
    val total = text.size(s.memoryTotalBytes, binary = true)
    val swap = if (s.hasSwapUsedBytes() && s.swapUsedBytes > 0) text.size(s.swapUsedBytes, binary = true) else null
    return MemoryCard(
        used = text.gbNumber(s.memoryUsedBytes, oneDecimal = true, binary = true), usedNumber = s.memoryUsedBytes / GIB, pressureWord = word,
        position = pressurePosition(s), tone = tone(s.memoryPressure), total = total, swap = swap,
        detail = if (swap == null) text.copy(UiMessages.machineStatusTotal(total)) else text.copy(UiMessages.machineStatusTotalSwap(total, swap)),
    )
}

/** Where the dot sits on the scale: the computer's own number only; with a level and no position the scale is not drawn. */
fun pressurePosition(s: MachineStatus): Float? =
    if (s.memoryPressure != ResourcePressure.RESOURCE_PRESSURE_UNSPECIFIED && s.hasMemoryPressurePosition()) s.memoryPressurePosition.toFloat().coerceIn(0f, 1f) else null

/** The disk card: the free amount, the bar's segments (shares of the volume) and its two lines. */
data class DiskCard(
    val free: String, val tone: MachineTone, val word: UiMessage, val agentShare: Float, val otherShare: Float,
    /** The first line under the state word, and the second (remote-agent's share); each its own line. */
    val detail: String, val detail2: String?, val agent: String?,
)

fun diskCard(s: MachineStatus, text: StatusText): DiskCard {
    val d = s.disk
    val t = tone(d.pressure)
    val word = when (t) {
        MachineTone.NORMAL -> UiMessages.machineStatusDiskOk()
        MachineTone.WARN -> UiMessages.machineStatusDiskLow()
        MachineTone.CRITICAL -> UiMessages.machineStatusDiskCritical()
    }
    val total = max(1L, d.totalBytes).toDouble()
    val used = max(0L, d.totalBytes - d.availableBytes)
    val agentBytes = if (s.hasAgentBytes()) min(s.agentBytes, used) else 0L
    val totalText = text.size(d.totalBytes)
    val agent = if (s.hasAgentBytes()) text.size(s.agentBytes) else null
    val detail = if (t == MachineTone.CRITICAL) text.copy(UiMessages.machineStatusDiskCriticalHint()) else text.copy(UiMessages.machineStatusDiskTotal(totalText))
    val detail2 = if (t != MachineTone.CRITICAL && agent != null) text.copy(UiMessages.machineStatusDiskAgentShare(agent)) else null
    return DiskCard(
        free = text.gbNumber(d.availableBytes), tone = t, word = word,
        agentShare = (agentBytes / total).toFloat(), otherShare = ((used - agentBytes) / total).toFloat(), detail = detail, detail2 = detail2, agent = agent,
    )
}

/** The disk lines appear only when the disk is 注意 or 紧张 (A3 ③). */
fun showDiskActions(s: MachineStatus): Boolean = tone(s.disk.pressure) != MachineTone.NORMAL

/** The CPU figures: the big number, the load and the cores (each absent when the computer does not say). */
data class CpuFigures(val percent: Int, val load: String?, val cores: String?)

fun cpuFigures(s: MachineStatus, text: StatusText) = CpuFigures(
    s.cpuPercent.coerceIn(0.0, 100.0).roundToInt(),
    if (s.hasLoadOne()) text.load(s.loadOne) else null,
    if (s.cores > 0) text.cores(s.cores.toInt()) else null,
)

/** The line at the page's foot: how long it has been up and, on a laptop, the battery. */
fun footerParts(s: MachineStatus, text: StatusText): List<String> = listOfNotNull(
    if (s.uptimeSeconds > 0) text.copy(UiMessages.machineStatusUptime(text.duration(s.uptimeSeconds))) else null,
    if (s.hasBattery()) {
        val pct = s.battery.percent.coerceIn(0.0, 100.0).roundToInt()
        text.copy(if (s.battery.charging) UiMessages.machineStatusBatteryCharging(pct) else UiMessages.machineStatusBattery(pct))
    } else null,
)

// ---- the chart (A3 ①, A4) ---------------------------------------------------------------------

/**
 * The window drawn: 116 s. The newest sample is 118 s from the oldest the daemon keeps, and the right edge trails the newest by up
 * to one sampling interval so that the curve slides evenly; a window of 116 s then always starts on data, never on a blank.
 */
const val CHART_WINDOW_MS = 116_000L
const val SAMPLE_INTERVAL_MS = 2_000L
/** Two neighbouring samples more than this apart are not joined (the page was away; no fake points). */
const val CHART_GAP_MS = 10_000L

data class ChartPoint(val timeMs: Long, val value: Double)

/** The samples of [metric] since [sinceMs] (the recovery point after a stale stretch), oldest first. */
fun chartPoints(s: MachineStatus, metric: StatusMetric, sinceMs: Long = 0): List<ChartPoint> =
    s.samplesList.asSequence().filter { it.timeMs >= sinceMs }.sortedBy { it.timeMs }
        .map { ChartPoint(it.timeMs, if (metric == StatusMetric.CPU) it.cpuPercent else it.memoryUsedBytes.toDouble()) }.toList()

/** The top of the vertical axis: 100% for the CPU, the total memory for memory. */
fun chartCeiling(s: MachineStatus, metric: StatusMetric): Double =
    if (metric == StatusMetric.CPU) 100.0 else max(1.0, s.memoryTotalBytes.toDouble())

/** A point of a drawn run: [x] across the window, [y] up the axis, both 0..1. */
data class ChartXY(val x: Double, val y: Double)

/**
 * The runs of the curve in the window ending at [endMs]: points are placed by their own time, a hole
 * of more than [gapMs] ends a run, and the run is cut exactly at the window's edges.
 */
fun chartRuns(points: List<ChartPoint>, endMs: Long, ceiling: Double, windowMs: Long = CHART_WINDOW_MS, gapMs: Long = CHART_GAP_MS): List<List<ChartXY>> {
    val start = endMs - windowMs
    fun xy(t: Double, v: Double) = ChartXY((t - start) / windowMs, (v / ceiling).coerceIn(0.0, 1.0))
    val runs = ArrayList<MutableList<ChartXY>>()
    var run: MutableList<ChartXY>? = null
    var prev: ChartPoint? = null
    for (p in points) {
        if (prev != null && p.timeMs - prev.timeMs > gapMs) run = null
        val before = prev
        prev = p
        if (p.timeMs < start) continue
        if (p.timeMs > endMs) {
            // The edge falls between the last point inside and this one.
            if (run != null && before != null && before.timeMs <= endMs) {
                val f = (endMs - before.timeMs).toDouble() / (p.timeMs - before.timeMs)
                run += xy(endMs.toDouble(), before.value + (p.value - before.value) * f)
            }
            run = null
            break
        }
        if (run == null) {
            run = ArrayList()
            runs += run
            // The first point after the left edge continues the line from just outside it.
            if (before != null && before.timeMs < start && p.timeMs - before.timeMs <= gapMs) {
                val f = (start - before.timeMs).toDouble() / (p.timeMs - before.timeMs)
                run += xy(start.toDouble(), before.value + (p.value - before.value) * f)
            }
        }
        run += xy(p.timeMs.toDouble(), p.value)
    }
    return runs
}

/** The value the curve has at [t], or null where there is a hole or no data. */
fun valueAt(points: List<ChartPoint>, t: Long, gapMs: Long = CHART_GAP_MS): Double? {
    for (i in points.indices) {
        val b = points[i]
        if (b.timeMs == t) return b.value
        if (b.timeMs > t) {
            val a = points.getOrNull(i - 1) ?: return null
            if (b.timeMs - a.timeMs > gapMs) return null
            return a.value + (b.value - a.value) * (t - a.timeMs).toDouble() / (b.timeMs - a.timeMs)
        }
    }
    return null
}

/** On-time samples trail by at most 2 s. After a poll gap, catch up in 500 ms
 * with cubic ease-out. Retargeting keeps the deadline; old snapshots never rewind. */
class ChartTimeline {
    private var end: Double? = null
    private var latest = 0L
    private var lastNow = 0.0
    private data class Catchup(val from: Double, val target: Double, val start: Double, val deadline: Double)
    private var catchup: Catchup? = null

    fun advance(nowMs: Double, firstMs: Long, latestMs: Long, moving: Boolean = true): Double {
        if (latestMs <= 0) return 0.0
        if (end == null || !moving) {
            end = if (moving) max(firstMs.toDouble(), (latestMs - SAMPLE_INTERVAL_MS).toDouble()) else latestMs.toDouble()
            latest = latestMs
            lastNow = nowMs
            catchup = null
            return end!!
        }
        val now = max(lastNow, nowMs)
        val c = catchup
        var value = end!!
        if (c != null) {
            val p = ((now - c.start) / (c.deadline - c.start)).coerceAtMost(1.0)
            val remaining = 1 - p
            value = c.from + (c.target - c.from) * (1 - remaining * remaining * remaining)
            if (p == 1.0) catchup = null
        } else {
            value = min(latest.toDouble(), value + now - lastNow)
        }
        lastNow = now
        if (latestMs > latest) {
            latest = latestMs
            if (catchup != null || latestMs - value > SAMPLE_INTERVAL_MS) {
                catchup = Catchup(value, latestMs.toDouble(), now, catchup?.deadline ?: (now + 500))
            }
        }
        end = value
        return value
    }
}

/** The sample nearest to [t], or -1 without data. */
fun nearestSample(points: List<ChartPoint>, t: Double): Int {
    var best = -1
    var d = Double.MAX_VALUE
    for (i in points.indices) { val x = abs(points[i].timeMs - t); if (x < d) { d = x; best = i } }
    return best
}

/** The text a screen reader says for the curve (A7). */
fun chartSummary(points: List<ChartPoint>, metric: StatusMetric, text: StatusText): String {
    if (points.isEmpty()) return ""
    fun fmt(v: Double) = if (metric == StatusMetric.CPU) text.percent(v) else text.size(v.toLong(), binary = true)
    return text.copy(UiMessages.machineStatusA11YChart(fmt(points.maxOf { it.value }), fmt(points.last().value)))
}

// ---- the ranking (A3 ④) -----------------------------------------------------------------------

/** What a ranking row shows; [bar] is its share of the head (0..1). */
data class UsageRow(
    val id: String, val title: String, val isSession: Boolean, val agent: Agent?, val primary: String, val secondary: String,
    val bar: Float, val top: Boolean, val sessionId: String,
)

/** The previous order of each list, kept per metric so rows do not trade places for nothing. */
class UsageOrder {
    private val kept = HashMap<Boolean, Pair<Boolean, List<String>>>()

    /** The rows in their stable order; a changed metric starts again from the metric's own order. */
    fun order(sessions: Boolean, memory: Boolean, rows: List<ResourceUsage>): List<ResourceUsage> {
        val previous = kept[sessions]?.takeIf { it.first == memory }?.second.orEmpty()
        val out = stableUsage(previous, rows, memory)
        kept[sessions] = memory to out.map { it.id }
        return out
    }
}

/**
 * The (up to) 8 rows of the list. [titleOf] and [agentOf] say what a session is called and which CLI
 * it runs; an unknown session shows nothing but its processes.
 */
fun usageRows(
    s: MachineStatus, showSessions: Boolean, metric: StatusMetric, order: UsageOrder, text: StatusText,
    titleOf: (String) -> String?, agentOf: (String) -> Agent?,
): List<UsageRow> {
    val memory = metric == StatusMetric.MEMORY
    val source = if (showSessions) s.sessionsList else s.processesList
    val ordered = order.order(showSessions, memory, source).take(8)
    fun main(r: ResourceUsage) = if (memory) r.memoryBytes.toDouble() else r.cpuPercent
    val head = ordered.maxOfOrNull { main(it) } ?: 0.0
    return ordered.mapIndexed { i, r ->
        val sid = if (showSessions) r.id else r.sessionId
        val sessionTitle = if (sid.isEmpty()) null else titleOf(sid)
        UsageRow(
            id = r.id,
            title = if (showSessions) sessionTitle ?: r.executable.ifEmpty { r.id } else r.executable,
            isSession = showSessions,
            agent = if (showSessions) agentOf(r.id) else null,
            primary = if (memory) text.size(r.memoryBytes, binary = true) else text.percent(r.cpuPercent),
            secondary = (if (memory) text.percent(r.cpuPercent) else text.size(r.memoryBytes, binary = true)) +
                if (!showSessions && sessionTitle != null) " · $sessionTitle" else "",
            bar = if (head <= 0) 0f else (main(r) / head).toFloat().coerceIn(0f, 1f),
            top = i == 0,
            sessionId = sid,
        )
    }
}

// ---- the home reminder (B1) --------------------------------------------------------------------

/** One reminder card for a computer whose disk is low. */
data class DiskReminder(val machineId: String, val critical: Boolean, val availableBytes: Long, val title: UiMessage, val detail: UiMessage)

/** The reminders of every online computer under 10 GB, the tightest first. */
fun diskReminders(state: RemoteState, text: StatusText, nameOf: (String) -> String): List<DiskReminder> =
    state.machines.values.mapNotNull { m ->
        val meta = m.meta ?: return@mapNotNull null
        val d = machineDiskReminder(m.online, meta) ?: return@mapNotNull null
        val critical = d.availableBytes < 2_000_000_000L
        val size = text.copy(UiMessages.machineStatusSizeGb(text.gbNumber(d.availableBytes)))
        val name = nameOf(m.id)
        DiskReminder(
            m.id, critical, d.availableBytes,
            if (critical) UiMessages.machineStatusHomeCritical() else UiMessages.machineStatusHomeWarning(),
            if (critical) UiMessages.machineStatusHomeCriticalDetail(name, size) else UiMessages.machineStatusHomeWarningDetail(name, size),
        )
    }.sortedWith(compareByDescending<DiskReminder> { it.critical }.thenBy { it.availableBytes }.thenBy { it.machineId })

// ---- what the page shows (A6) -------------------------------------------------------------------

enum class StatusView { LOADING, UNAVAILABLE, LIVE, STALE }

/** The page's face for the controller's state: nothing to show yet, nothing ever read, live, or kept. */
fun statusView(state: MachinePollState, last: MachineStatus?): StatusView = when {
    last == null -> if (state == MachinePollState.UNAVAILABLE) StatusView.UNAVAILABLE else StatusView.LOADING
    state == MachinePollState.STALE || state == MachinePollState.UNAVAILABLE -> StatusView.STALE
    else -> StatusView.LIVE
}

/** The prompt of 让 Agent 帮我清理, added after what is already written in the draft, and not again when the draft already holds it. */
fun cleanupDraft(existing: String, prompt: String): String =
    if (existing.isBlank()) prompt else if (existing.contains(prompt)) existing else existing.trimEnd() + "\n\n" + prompt

// ---- the swap of the main chart and a small card (A3) ---------------------------------------------------------

const val SWAP_MS = 350
const val SWAP_OUT_MS = 100
const val SWAP_IN_MS = 120

/**
 * What the swap shows [ms] into its 350: the card keeps its colour and corners throughout; the old figures fade out over the
 * first 100, nothing shows until the last 120, in which the new ones fade in. Never both at once.
 */
fun swapOldAlpha(ms: Double): Float = (1.0 - ms / SWAP_OUT_MS).coerceIn(0.0, 1.0).toFloat()
fun swapNewAlpha(ms: Double): Float = ((ms - (SWAP_MS - SWAP_IN_MS)) / SWAP_IN_MS).coerceIn(0.0, 1.0).toFloat()
