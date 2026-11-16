package io.github.lewin671.remoteagent.core.transport

import kotlin.math.ceil

enum class RouteName { RELAY, DIRECT }

enum class UploadKind { IMAGE, FILE }

sealed interface RouteHint {
    data object Read : RouteHint
    data class Bulk(val file: String, val upload: UploadKind? = null, val bytes: Int = 0) : RouteHint
}

/** Uses verified application completions. Trial transfers carry disjoint file
 * chunks; a hot path retains one relay sample in eight, never duplicate bodies. */
internal class DirectRouting(private val now: () -> Long = ::monotonicMillis) {
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

    private val lock = Any()
    private val readsRelay = ArrayDeque<Sample>()
    private val readsDirect = ArrayDeque<Sample>()
    private val recentReadsRelay = ArrayDeque<Sample>()
    private val recentReadsDirect = ArrayDeque<Sample>()
    private var readPreferred = RouteName.RELAY
    private var readRejected = false
    private var readStableAfter = 0L
    private var readBad = 0
    private var readWindow = now() / 5000L
    private var sampleCounter = 0L
    private val files = LinkedHashMap<String, BulkState>()
    private var bulkRelayMib: Double? = null
    private var bulkDirectMib: Double? = null
    private var bulkRejected = false
    @Volatile var filePath: RouteName = RouteName.RELAY
        private set

    data class Metrics(
        val readDirect: Boolean,
        val fileDirect: Boolean,
        val benchmarkDecidedRelay: Boolean,
        val bulkDirectMibPerSec: Double?,
        val bulkRelayMibPerSec: Double?,
        val readDirectMs: Double?,
        val readRelayMs: Double?,
    )

    fun metrics(): Metrics = synchronized(lock) {
        val relayReadMs = if (readsRelay.isEmpty()) null else p95(readsRelay)
        val directReadMs = if (readsDirect.isEmpty()) null else p95(readsDirect)
        Metrics(
            readDirect = readPreferred == RouteName.DIRECT,
            fileDirect = filePath == RouteName.DIRECT,
            benchmarkDecidedRelay = (readRejected || bulkRejected) &&
                readPreferred == RouteName.RELAY &&
                filePath == RouteName.RELAY,
            bulkDirectMibPerSec = bulkDirectMib,
            bulkRelayMibPerSec = bulkRelayMib,
            readDirectMs = directReadMs,
            readRelayMs = relayReadMs,
        )
    }

    fun needsReadSamples(): Boolean = synchronized(lock) {
        readPreferred == RouteName.RELAY && now() >= readStableAfter
    }

    fun choose(hint: RouteHint, ready: Boolean): RouteName = synchronized(lock) {
        if (!ready) return RouteName.RELAY
        val t = now()
        if (hint is RouteHint.Read) {
            advanceReads(t)
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
            val target = if (path == RouteName.DIRECT) readsDirect else readsRelay
            val recent = if (path == RouteName.DIRECT) recentReadsDirect else recentReadsRelay
            target.addLast(s)
            recent.addLast(s)
            for (q in listOf(readsRelay, readsDirect)) {
                while (q.size > 512 || (q.isNotEmpty() && t - q.first().at > 60_000L)) q.removeFirst()
            }
            val queueStable = readsDirect.isNotEmpty() && readsDirect.last().queued <= readsDirect.first().queued
            if (readsRelay.size >= 100 &&
                readsDirect.size >= 100 &&
                t - maxOf(readsRelay.first().at, readsDirect.first().at) >= 5000L &&
                t >= readStableAfter &&
                queueStable
            ) {
                val relayP95 = p95(readsRelay)
                val directP95 = p95(readsDirect)
                if (directP95 <= relayP95 + maxOf(5.0, relayP95 * 0.10)) {
                    readPreferred = RouteName.DIRECT
                    readRejected = false
                } else {
                    readRejected = true
                }
            }
            return@synchronized
        }
        hint as RouteHint.Bulk
        val file = file(hint.file, t)
        advanceBulk(hint, file, t)
        filePath = path
        if (bytes <= 0) return@synchronized
        val measured = if (path == RouteName.DIRECT) file.measuredDirect else file.measuredRelay
        val recent = if (path == RouteName.DIRECT) file.recentDirect else file.recentRelay
        measured.bytes += bytes
        measured.milliseconds += milliseconds
        recent.bytes += bytes
        recent.milliseconds += milliseconds
        if (path == RouteName.DIRECT) {
            bulkDirectMib = (recent.bytes.toDouble() / 1048576.0) / (recent.milliseconds / 1000.0)
        } else {
            bulkRelayMib = (recent.bytes.toDouble() / 1048576.0) / (recent.milliseconds / 1000.0)
        }
        if (file.preferred == RouteName.RELAY) {
            if (file.measuring == RouteName.RELAY && file.measuredRelay.bytes >= (4L shl 20)) {
                file.measuring = RouteName.DIRECT
            }
            if (file.measuredRelay.bytes >= (4L shl 20) && file.measuredDirect.bytes >= (4L shl 20)) {
                val pass = file.measuredDirect.rate() >= file.measuredRelay.rate() * 0.95
                file.comparisons = if (pass) file.comparisons + 1 else 0
                bulkRejected = !pass
                if (file.comparisons >= 2 && t >= file.stableAfter) {
                    file.preferred = RouteName.DIRECT
                    file.bad = 0
                    file.resetRecent()
                    bulkRejected = false
                }
                file.resetMeasured()
                file.measuring = RouteName.RELAY
            }
        }
    }

    private fun p95(samples: Collection<Sample>): Double {
        if (samples.isEmpty()) return Double.POSITIVE_INFINITY
        val sorted = samples.map { it.milliseconds }.sorted()
        val idx = (ceil(sorted.size * 0.95).toInt() - 1).coerceIn(0, sorted.lastIndex)
        return sorted[idx]
    }

    private fun advanceReads(t: Long) {
        val window = t / 5000L
        if (window <= readWindow) return
        val bad = recentReadsRelay.isNotEmpty() && recentReadsDirect.isNotEmpty() && run {
            val relayP95 = p95(recentReadsRelay)
            p95(recentReadsDirect) > relayP95 + maxOf(10.0, relayP95 * 0.20)
        }
        readBad = if (bad) (if (window == readWindow + 1L) readBad + 1 else 1) else 0
        recentReadsRelay.clear()
        recentReadsDirect.clear()
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

    fun fail(hint: RouteHint? = null) = synchronized(lock) {
        failLocked(hint, now())
    }

    private fun failLocked(hint: RouteHint?, t: Long) {
        val until = t + 5000L
        if (hint == null || hint is RouteHint.Read) {
            readPreferred = RouteName.RELAY
            readRejected = false
            readStableAfter = until
            readsRelay.clear()
            readsDirect.clear()
            recentReadsRelay.clear()
            recentReadsDirect.clear()
            readBad = 0
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
            bulkRejected = false
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
