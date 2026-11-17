package io.github.lewin671.remoteagent.core.transport

import kotlin.math.min

enum class RouteName { RELAY, DIRECT }

enum class UploadKind { IMAGE, FILE }

sealed interface RouteHint {
    data object Read : RouteHint
    data class Bulk(val file: String, val upload: UploadKind? = null, val bytes: Int = 0) : RouteHint
}

/** Direct-first routing rule (docs/transport.md 「升级与热备」). */
internal object DirectRule {
    /** Fallback for reads: direct median exceeds 1.5x relay median + 20 ms. */
    const val FALLBACK_WINDOW_MS = 30_000L
    const val FALLBACK_DIRECT_SAMPLES = 8
    const val FALLBACK_RELAY_SAMPLES = 4
    const val FALLBACK_MEDIAN_RATIO = 1.5
    const val FALLBACK_SLACK_MS = 20.0

    /** Fallback for bulk: direct segment throughput falls below 70% of relay. */
    const val BULK_RATIO = 0.70
    const val BULK_DIRECT_MIN_BYTES = 1L shl 20
    const val BULK_RELAY_MIN_BYTES = 512L shl 10

    /** Exponential wait after fallback before direct becomes preferred again. */
    const val BACKOFF_INITIAL_MS = 60_000L
    const val BACKOFF_MAX_MS = 600_000L

    const val KEEP = 512
    const val KEEP_MS = 60_000L
    const val DISPLAY_MIN_SAMPLES = 3
}

/** Uses verified application completions. Ready DirectPaths are preferred
 * immediately; hot paths retain one relay control sample in eight. */
internal class DirectRouting(
    private val now: () -> Long = ::monotonicMillis,
) {
    private data class Sample(val at: Long, val milliseconds: Double, val queued: Long)
    private class Window(var bytes: Long = 0L, var milliseconds: Double = 0.0) {
        fun rate(): Double = if (milliseconds > 0.0) bytes / milliseconds else 0.0
    }

    private class FileSegment {
        var relay = Window()
        var direct = Window()
    }

    private val lock = Any()
    private val reads = arrayOf(ArrayDeque<Sample>(), ArrayDeque<Sample>())
    private var readStableAfter = 0L
    private var readFallbacks = 0
    private var readSlow = false
    private var readSampleCounter = 0L
    /** Display only: each path's last 60s median with >=3 samples; cleared on fallback or hard fault. */
    private val lastReadMs = arrayOfNulls<Double>(2)

    private val files = LinkedHashMap<String, FileSegment>()
    private var bulkStableAfter = 0L
    private var bulkFallbacks = 0
    private var bulkSlow = false
    private var bulkSampleCounter = 0L
    private val fileRate = arrayOfNulls<Double>(2)
    private val fileTally = arrayOf(Window(), Window())

    val filePath: RouteName
        get() = synchronized(lock) { if (now() >= bulkStableAfter) RouteName.DIRECT else RouteName.RELAY }

    private fun RouteName.i() = if (this == RouteName.RELAY) 0 else 1

    data class Measured(
        val bulkDirectMibPerSec: Double?,
        val bulkRelayMibPerSec: Double?,
        val readDirectMs: Double?,
        val readRelayMs: Double?,
        val readDirect: Boolean,
        val fileDirect: Boolean,
        val readSlow: Boolean,
        val fileSlow: Boolean,
    )

    private fun live(path: RouteName, windowMs: Long, t: Long): List<Sample> =
        reads[path.i()].filter { t - it.at <= windowMs }

    private fun median(samples: List<Sample>): Double {
        val s = samples.map { it.milliseconds }.sorted()
        return s.getOrNull(s.size / 2) ?: Double.POSITIVE_INFINITY
    }

    private fun snapshotLocked(path: RouteName, t: Long) {
        val samples = live(path, DirectRule.KEEP_MS, t)
        if (samples.size >= DirectRule.DISPLAY_MIN_SAMPLES) lastReadMs[path.i()] = median(samples)
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
            readDirect = t >= readStableAfter,
            fileDirect = t >= bulkStableAfter,
            readSlow = readSlow && t < readStableAfter,
            fileSlow = bulkSlow && t < bulkStableAfter,
        )
    }

    /** Preferred path for read requests when a read DirectPath is ready. */
    fun readPath(): RouteName = synchronized(lock) {
        if (now() >= readStableAfter) RouteName.DIRECT else RouteName.RELAY
    }

    fun choose(hint: RouteHint, ready: Boolean): RouteName = synchronized(lock) {
        if (!ready) return RouteName.RELAY
        val t = now()
        if (hint is RouteHint.Read) {
            if (t < readStableAfter) return RouteName.RELAY
            return if (++readSampleCounter % 8L == 0L) RouteName.RELAY else RouteName.DIRECT
        }
        if (t < bulkStableAfter) return RouteName.RELAY
        return if (++bulkSampleCounter % 8L == 0L) RouteName.RELAY else RouteName.DIRECT
    }

    fun observe(hint: RouteHint, path: RouteName, bytes: Int, milliseconds: Double, queued: Long = 0L) = synchronized(lock) {
        val t = now()
        if (!milliseconds.isFinite() || milliseconds <= 0.0) return@synchronized
        if (hint is RouteHint.Read) {
            val s = Sample(t, milliseconds, queued)
            reads[path.i()].addLast(s)
            for (q in reads) {
                while (q.size > DirectRule.KEEP || (q.isNotEmpty() && t - q.first().at > DirectRule.KEEP_MS)) q.removeFirst()
            }
            snapshotLocked(path, t)
            decideReadsLocked(t)
            return@synchronized
        }
        hint as RouteHint.Bulk
        if (bytes <= 0) return@synchronized
        val tally = fileTally[path.i()]
        tally.bytes += bytes
        tally.milliseconds += milliseconds
        if (tally.bytes >= (4L shl 20)) {
            fileRate[path.i()] = tally.rate()
            fileTally[path.i()] = Window()
        }
        val seg = file(hint.file)
        val win = if (path == RouteName.DIRECT) seg.direct else seg.relay
        win.bytes += bytes
        win.milliseconds += milliseconds
        if (t >= bulkStableAfter &&
            seg.direct.bytes >= DirectRule.BULK_DIRECT_MIN_BYTES &&
            seg.relay.bytes >= DirectRule.BULK_RELAY_MIN_BYTES
        ) {
            if (seg.direct.rate() < seg.relay.rate() * DirectRule.BULK_RATIO) {
                failLocked(hint, t)
            } else {
                seg.direct = Window()
                seg.relay = Window()
            }
        }
    }

    private fun decideReadsLocked(t: Long) {
        if (t < readStableAfter) return
        val relay = live(RouteName.RELAY, DirectRule.FALLBACK_WINDOW_MS, t)
        val direct = live(RouteName.DIRECT, DirectRule.FALLBACK_WINDOW_MS, t)
        if (direct.size >= DirectRule.FALLBACK_DIRECT_SAMPLES &&
            relay.size >= DirectRule.FALLBACK_RELAY_SAMPLES &&
            median(direct) > DirectRule.FALLBACK_MEDIAN_RATIO * median(relay) + DirectRule.FALLBACK_SLACK_MS
        ) {
            failLocked(RouteHint.Read, t)
        }
    }

    fun fail(hint: RouteHint? = null) = synchronized(lock) { failLocked(hint, now()) }

    private fun failLocked(hint: RouteHint?, t: Long) {
        if (hint == null || hint is RouteHint.Read) {
            readFallbacks++
            val shift = min(readFallbacks - 1, 10)
            val wait = min(DirectRule.BACKOFF_MAX_MS, DirectRule.BACKOFF_INITIAL_MS shl shift)
            readStableAfter = t + wait
            readSlow = true
            for (q in reads) q.clear()
            lastReadMs[0] = null
            lastReadMs[1] = null
        }
        if (hint == null || hint is RouteHint.Bulk) {
            bulkFallbacks++
            val shift = min(bulkFallbacks - 1, 10)
            val wait = min(DirectRule.BACKOFF_MAX_MS, DirectRule.BACKOFF_INITIAL_MS shl shift)
            bulkStableAfter = t + wait
            bulkSlow = true
            files.clear()
            fileRate[0] = null
            fileRate[1] = null
            fileTally[0] = Window()
            fileTally[1] = Window()
        }
    }

    /** Clears observation samples on physical close without starting a slow-fallback banner. */
    fun hardFail(read: Boolean = true, bulk: Boolean = true) = synchronized(lock) {
        if (read) {
            readSlow = false
            for (q in reads) q.clear()
            lastReadMs[0] = null
            lastReadMs[1] = null
        }
        if (bulk) {
            bulkSlow = false
            files.clear()
            fileRate[0] = null
            fileRate[1] = null
            fileTally[0] = Window()
            fileTally[1] = Window()
        }
    }

    /** Resets fallback backoff and samples on network generation change or direct rebuild. */
    fun reset(read: Boolean = true, bulk: Boolean = true) = synchronized(lock) {
        if (read) {
            readFallbacks = 0
            readStableAfter = 0L
            readSlow = false
            readSampleCounter = 0L
            for (q in reads) q.clear()
            lastReadMs[0] = null
            lastReadMs[1] = null
        }
        if (bulk) {
            bulkFallbacks = 0
            bulkStableAfter = 0L
            bulkSlow = false
            bulkSampleCounter = 0L
            files.clear()
            fileRate[0] = null
            fileRate[1] = null
            fileTally[0] = Window()
            fileTally[1] = Window()
        }
    }

    private fun file(key: String): FileSegment {
        files[key]?.let { return it }
        if (files.size >= 32) {
            val oldest = files.keys.firstOrNull()
            if (oldest != null) files.remove(oldest)
        }
        return FileSegment().also { files[key] = it }
    }
}
