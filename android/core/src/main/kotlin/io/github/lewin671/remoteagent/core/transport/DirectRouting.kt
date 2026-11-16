package io.github.lewin671.remoteagent.core.transport

import kotlin.math.ceil

enum class RouteName { RELAY, DIRECT }

enum class UploadKind { IMAGE, FILE }

sealed interface RouteHint {
    data object Read : RouteHint
    data class Bulk(val file: String, val upload: UploadKind? = null, val bytes: Int = 0) : RouteHint
}

/** Read-only routing rule (docs/transport.md 「只读选路」); every number here is
 * that document's, the same as the web's READ_RULE. */
internal object ReadRule {
    /** Warm-up after a connection, a network change or a fallback. */
    const val WARMUP_WINDOW_MS = 5000L
    const val WARMUP_PER_PATH = 10
    const val WARMUP_MIN_GAP_MS = 250L
    /** Fast upgrade: a clear win needs few samples. */
    const val FAST_WINDOW_MS = 60_000L
    const val FAST_MIN_SAMPLES = 8
    const val FAST_MIN_SPAN_MS = 2000L
    const val FAST_MEDIAN_RATIO = 0.7
    /** Fallback: 0.7 against 1.2 is the gap that stops flapping. */
    const val BACK_WINDOW_MS = 30_000L
    const val BACK_DIRECT_SAMPLES = 8
    const val BACK_RELAY_SAMPLES = 4
    const val BACK_MEDIAN_RATIO = 1.2
    const val BACK_SLACK_MS = 10.0
    /** Stability wait after any fallback before direct may be chosen again. */
    const val STABLE_MS = 5000L
    const val KEEP = 512
    const val KEEP_MS = 60_000L
}

/** Uses verified application completions. Trial transfers carry disjoint file
 * chunks; a hot path retains one relay sample in eight, never duplicate bodies. */
internal class DirectRouting(
    private val now: () -> Long = ::monotonicMillis,
    /** Called, under this object's lock, when a warm-up opens: it must only
     * schedule work, never block or take another lock. */
    private val armed: () -> Unit = {},
) {
    private data class Sample(val at: Long, val milliseconds: Double, val queued: Long)
    private class Window(var bytes: Long = 0L, var milliseconds: Double = 0.0) {
        fun rate(): Double = if (milliseconds > 0.0) bytes / milliseconds else 0.0
    }

    private class BulkState(var window: Long) {
        var measuredRelay = Window()
        var measuredDirect = Window()
        var recentRelay = Window()
        var recentDirect = Window()
        var comparisons = 0
        var preferred = RouteName.RELAY
        var measuring = RouteName.RELAY
        var bad = 0
        var stableAfter = 0L

        fun resetMeasured() {
            measuredRelay = Window()
            measuredDirect = Window()
        }

        fun resetRecent() {
            recentRelay = Window()
            recentDirect = Window()
        }
    }

    private class Warmup {
        var start = Long.MAX_VALUE
        var end = Long.MIN_VALUE
        val issued = intArrayOf(0, 0)
        val inflight = booleanArrayOf(false, false)
        var last = RouteName.RELAY
    }

    private val lock = Any()
    private val reads = arrayOf(ArrayDeque<Sample>(), ArrayDeque<Sample>())
    private val recentReads = arrayOf(ArrayDeque<Sample>(), ArrayDeque<Sample>())
    private var readPreferred = RouteName.RELAY
    private var readStableAfter = 0L
    private var readBad = 0
    private var readWindow = now() / 5000L
    private var sampleCounter = 0L
    private var warm = Warmup()
    private var lastProbeAt = Long.MIN_VALUE / 2
    /** Display only: each path's last window median that had at least three
     * samples; never read by a decision. A fallback discards it with the samples. */
    private val lastReadMs = arrayOfNulls<Double>(2)
    private val files = LinkedHashMap<String, BulkState>()
    private val fileRate = arrayOfNulls<Double>(2)
    private val fileTally = arrayOf(Window(), Window())
    @Volatile var filePath: RouteName = RouteName.RELAY
        private set

    private fun RouteName.i() = if (this == RouteName.RELAY) 0 else 1

    data class Measured(
        val bulkDirectMibPerSec: Double?,
        val bulkRelayMibPerSec: Double?,
        val readDirectMs: Double?,
        val readRelayMs: Double?,
        val readDirect: Boolean,
        val fileDirect: Boolean,
        val warming: Boolean,
    ) {
        /** At least one purpose was measured on both paths, and on none of them is direct ahead. */
        val relayNotSlower: Boolean
            get() {
                val file = bulkDirectMibPerSec != null && bulkRelayMibPerSec != null
                val read = readDirectMs != null && readRelayMs != null
                return (file || read) &&
                    (!file || bulkRelayMibPerSec!! >= bulkDirectMibPerSec!!) &&
                    (!read || readRelayMs!! <= readDirectMs!!)
            }
    }

    private fun live(path: RouteName, windowMs: Long, t: Long): List<Sample> =
        reads[path.i()].filter { t - it.at <= windowMs }

    private fun sorted(samples: List<Sample>) = samples.map { it.milliseconds }.sorted()
    private fun quantile(samples: List<Sample>, q: Double): Double {
        val s = sorted(samples)
        return s.getOrNull(ceil(s.size * q).toInt() - 1) ?: Double.POSITIVE_INFINITY
    }
    private fun p95(samples: List<Sample>) = quantile(samples, 0.95)
    /** The one latency summary for reads: what the page shows is what the fast
     * upgrade and the fallback compare. */
    private fun median(samples: List<Sample>): Double = sorted(samples).getOrNull(samples.size / 2) ?: Double.POSITIVE_INFINITY
    private fun span(samples: List<Sample>) = if (samples.isEmpty()) 0L else samples.last().at - samples.first().at

    private fun snapshotLocked(path: RouteName, t: Long) {
        val samples = live(path, ReadRule.FAST_WINDOW_MS, t)
        if (samples.size >= 3) lastReadMs[path.i()] = median(samples)
    }

    /** What has actually been measured. A path with no samples is absent, never zero. */
    fun measured(): Measured = synchronized(lock) {
        val t = now()
        snapshotLocked(RouteName.RELAY, t)
        snapshotLocked(RouteName.DIRECT, t)
        Measured(
            bulkDirectMibPerSec = fileRate[1]?.let { it * 1000.0 / (1 shl 20) },
            bulkRelayMibPerSec = fileRate[0]?.let { it * 1000.0 / (1 shl 20) },
            readDirectMs = lastReadMs[1],
            readRelayMs = lastReadMs[0],
            readDirect = t >= readStableAfter && readPreferred == RouteName.DIRECT,
            fileDirect = filePath == RouteName.DIRECT,
            warming = warmingLocked(t),
        )
    }

    /** Path that read requests use now; the same gate as choose() for reads. */
    fun readPath(): RouteName = synchronized(lock) {
        if (now() >= readStableAfter) readPreferred else RouteName.RELAY
    }

    /** Whether the live window lacks the samples a decision needs on either path. */
    fun sparse(): Boolean = synchronized(lock) {
        val t = now()
        live(RouteName.RELAY, ReadRule.FAST_WINDOW_MS, t).size < ReadRule.FAST_MIN_SAMPLES ||
            live(RouteName.DIRECT, ReadRule.FAST_WINDOW_MS, t).size < ReadRule.FAST_MIN_SAMPLES
    }

    /** Starts a warm-up: background samples of both paths, right away when the
     * read path is ready, after the stability wait when a fallback just
     * happened. A trigger while one is running is absorbed, so triggers cannot
     * stack budgets. */
    fun arm() {
        synchronized(lock) {
            val t = now()
            val w = warm
            if (t >= w.start && t < w.end) return
            val start = maxOf(t, readStableAfter)
            val next = Warmup()
            next.start = start
            next.end = start + ReadRule.WARMUP_WINDOW_MS
            next.inflight[0] = w.inflight[0]
            next.inflight[1] = w.inflight[1]
            warm = next
        }
        armed()
    }

    /** Whether a warm-up window is open or still waiting to open. */
    fun pending(): Boolean = synchronized(lock) { now() < warm.end }

    private fun warmingLocked(t: Long): Boolean {
        val w = warm
        val cap = ReadRule.WARMUP_PER_PATH
        return t >= w.start && t < w.end &&
            !(w.issued[0] >= cap && w.issued[1] >= cap && !w.inflight[0] && !w.inflight[1])
    }

    /** Whether the warm-up is running now: what the page calls measuring. */
    fun warming(): Boolean = synchronized(lock) { warmingLocked(now()) }

    /** The path to take the next warm-up sample on, or nothing now. Paths
     * alternate (the less sampled first, direct before relay), one request in
     * flight per path, at most [ReadRule.WARMUP_PER_PATH] per path per warm-up,
     * and never closer than [ReadRule.WARMUP_MIN_GAP_MS] to the previous one. */
    fun nextProbe(): RouteName? = synchronized(lock) {
        val t = now()
        val w = warm
        if (t < w.start || t >= w.end || t - lastProbeAt < ReadRule.WARMUP_MIN_GAP_MS) return null
        val open = listOf(RouteName.DIRECT, RouteName.RELAY).filter { w.issued[it.i()] < ReadRule.WARMUP_PER_PATH && !w.inflight[it.i()] }
            .sortedWith(compareBy<RouteName>({ w.issued[it.i()] }, { if (it == w.last) 1 else 0 }))
        val path = open.firstOrNull() ?: return null
        w.issued[path.i()]++
        w.inflight[path.i()] = true
        w.last = path
        lastProbeAt = t
        path
    }

    fun probeDone(path: RouteName) = synchronized(lock) { warm.inflight[path.i()] = false }

    fun choose(hint: RouteHint, ready: Boolean): RouteName = synchronized(lock) {
        if (!ready) return RouteName.RELAY
        val t = now()
        if (hint is RouteHint.Read) {
            advanceReads(t)
            decideReads(t)
            if (t < readStableAfter) return RouteName.RELAY
            if (readPreferred == RouteName.DIRECT) {
                return if (++sampleCounter % 8L == 0L) RouteName.RELAY else RouteName.DIRECT
            }
            return RouteName.RELAY
        }
        hint as RouteHint.Bulk
        val file = file(hint.file, t)
        advanceBulk(hint, file, t)
        if (t < file.stableAfter) return RouteName.RELAY
        if (file.preferred == RouteName.DIRECT) {
            return if (++sampleCounter % 8L == 0L) RouteName.RELAY else RouteName.DIRECT
        }
        file.measuring
    }

    fun observe(hint: RouteHint, path: RouteName, bytes: Int, milliseconds: Double, queued: Long = 0L) = synchronized(lock) {
        val t = now()
        if (!milliseconds.isFinite() || milliseconds <= 0.0) return@synchronized
        if (hint is RouteHint.Read) {
            advanceReads(t)
            val s = Sample(t, milliseconds, queued)
            reads[path.i()].addLast(s)
            recentReads[path.i()].addLast(s)
            for (q in reads) {
                while (q.size > ReadRule.KEEP || (q.isNotEmpty() && t - q.first().at > ReadRule.KEEP_MS)) q.removeFirst()
            }
            snapshotLocked(path, t)
            decideReads(t)
            return@synchronized
        }
        hint as RouteHint.Bulk
        val file = file(hint.file, t)
        advanceBulk(hint, file, t)
        if (bytes <= 0) {
            filePath = file.preferred
            return@synchronized
        }
        val tally = fileTally[path.i()]
        tally.bytes += bytes
        tally.milliseconds += milliseconds
        if (tally.bytes >= (4L shl 20)) {
            fileRate[path.i()] = tally.rate()
            fileTally[path.i()] = Window()
        }
        val measured = if (path == RouteName.DIRECT) file.measuredDirect else file.measuredRelay
        val recent = if (path == RouteName.DIRECT) file.recentDirect else file.recentRelay
        measured.bytes += bytes
        measured.milliseconds += milliseconds
        recent.bytes += bytes
        recent.milliseconds += milliseconds
        if (file.preferred == RouteName.RELAY) {
            if (file.measuring == RouteName.RELAY && file.measuredRelay.bytes >= (4L shl 20)) {
                file.measuring = RouteName.DIRECT
            }
            if (file.measuredRelay.bytes >= (4L shl 20) && file.measuredDirect.bytes >= (4L shl 20)) {
                val pass = file.measuredDirect.rate() >= file.measuredRelay.rate() * 0.95
                file.comparisons = if (pass) file.comparisons + 1 else 0
                if (file.comparisons >= 2 && t >= file.stableAfter) {
                    file.preferred = RouteName.DIRECT
                    file.bad = 0
                    file.resetRecent()
                }
                file.resetMeasured()
                file.measuring = RouteName.RELAY
            }
        }
        filePath = file.preferred
    }

    /** Upgrades when direct is clearly or steadily better, falls back when it is
     * clearly worse. Fast and strict upgrades are alternatives; either suffices. */
    private fun decideReads(t: Long) {
        if (readPreferred == RouteName.RELAY) {
            if (t < readStableAfter) return
            val relay = live(RouteName.RELAY, ReadRule.FAST_WINDOW_MS, t)
            val direct = live(RouteName.DIRECT, ReadRule.FAST_WINDOW_MS, t)
            val queueStable = direct.isNotEmpty() && direct.last().queued <= direct.first().queued
            val clear = relay.size >= ReadRule.FAST_MIN_SAMPLES && direct.size >= ReadRule.FAST_MIN_SAMPLES &&
                span(relay) >= ReadRule.FAST_MIN_SPAN_MS && span(direct) >= ReadRule.FAST_MIN_SPAN_MS && queueStable &&
                median(direct) <= ReadRule.FAST_MEDIAN_RATIO * median(relay) && quantile(direct, 0.9) <= median(relay)
            val steady = relay.size >= 100 && direct.size >= 100 &&
                t - maxOf(relay.first().at, direct.first().at) >= 5000L && queueStable &&
                p95(direct) <= p95(relay) + maxOf(5.0, p95(relay) * 0.10)
            if (clear || steady) readPreferred = RouteName.DIRECT
            return
        }
        val relay = live(RouteName.RELAY, ReadRule.BACK_WINDOW_MS, t)
        val direct = live(RouteName.DIRECT, ReadRule.BACK_WINDOW_MS, t)
        if (direct.size >= ReadRule.BACK_DIRECT_SAMPLES && relay.size >= ReadRule.BACK_RELAY_SAMPLES &&
            median(direct) > ReadRule.BACK_MEDIAN_RATIO * median(relay) + ReadRule.BACK_SLACK_MS
        ) failLocked(RouteHint.Read, t)
    }

    private fun advanceReads(t: Long) {
        val window = t / 5000L
        if (window <= readWindow) return
        val relay = recentReads[0]
        val direct = recentReads[1]
        val bad = relay.isNotEmpty() && direct.isNotEmpty() && run {
            val relayP95 = p95(relay)
            p95(direct) > relayP95 + maxOf(10.0, relayP95 * 0.20)
        }
        readBad = if (bad) (if (window == readWindow + 1L) readBad + 1 else 1) else 0
        relay.clear()
        direct.clear()
        readWindow = window
        if (readBad >= 2) failLocked(RouteHint.Read, t)
    }

    private fun advanceBulk(hint: RouteHint.Bulk, file: BulkState, t: Long) {
        val window = t / 5000L
        if (window <= file.window) return
        val bad = file.recentRelay.bytes > 0L && file.recentDirect.bytes > 0L &&
            file.recentDirect.rate() < file.recentRelay.rate() * 0.90
        file.bad = if (bad) (if (window == file.window + 1L) file.bad + 1 else 1) else 0
        file.resetRecent()
        file.window = window
        if (file.bad >= 2) failLocked(hint, t)
    }

    fun fail(hint: RouteHint? = null) = synchronized(lock) { failLocked(hint, now()) }

    private fun failLocked(hint: RouteHint?, t: Long) {
        val until = t + ReadRule.STABLE_MS
        if (hint == null || hint is RouteHint.Read) {
            readPreferred = RouteName.RELAY
            readStableAfter = until
            for (q in reads) q.clear()
            for (q in recentReads) q.clear()
            readBad = 0
            lastReadMs[0] = null
            lastReadMs[1] = null
            // A fallback re-measures after the stability wait; a window still open is cancelled.
            val w = warm
            val next = Warmup()
            next.start = until
            next.end = until + ReadRule.WARMUP_WINDOW_MS
            next.inflight[0] = w.inflight[0]
            next.inflight[1] = w.inflight[1]
            warm = next
            armed()
        }
        val list = when (hint) {
            null -> files.values.toList()
            is RouteHint.Bulk -> listOf(file(hint.file, t))
            is RouteHint.Read -> emptyList()
        }
        for (file in list) {
            file.preferred = RouteName.RELAY
            file.stableAfter = until
            file.comparisons = 0
            file.resetMeasured()
            file.resetRecent()
            file.measuring = RouteName.RELAY
            file.bad = 0
        }
        if (hint == null || hint is RouteHint.Bulk) {
            filePath = RouteName.RELAY
            fileRate[0] = null
            fileRate[1] = null
            fileTally[0] = Window()
            fileTally[1] = Window()
        }
    }

    private fun file(key: String, t: Long): BulkState {
        files[key]?.let { return it }
        if (files.size >= 32) {
            val oldest = files.keys.firstOrNull()
            if (oldest != null) files.remove(oldest)
        }
        return BulkState(t / 5000L).also { files[key] = it }
    }
}
