package io.github.lewin671.remoteagent.core.transport

/**
 * Delay before the next direct round with a computer after one failed or ended
 * (docs/transport.md §4): full jitter, uniform below 1 s, 2 s, 4 s ... up to
 * 30 s. A round counts once however many of its paths close. A path that stayed
 * up for [STABLE_MS] starts the sequence again. Same as the web's
 * `DIRECT_RETRY`.
 */
internal class DirectRetry(private val jitter: () -> Double) {
    private val failures = HashMap<String, Int>()

    /** The delay for the end of a round with [id]; [readyAt] is when that round's first path came up, if it did. */
    @Synchronized
    fun delay(id: String, readyAt: Long?, now: Long): Long {
        if (readyAt != null && now - readyAt >= STABLE_MS) failures.remove(id)
        val count = failures[id] ?: 0
        failures[id] = count + 1
        val ceiling = minOf(MAX_MS, INITIAL_MS shl minOf(count, 5))
        return (jitter() * ceiling).toLong()
    }

    /** A new start: the main connection is back, a new computer process, the switch turned on again. */
    @Synchronized fun reset(id: String) { failures.remove(id) }

    @Synchronized fun clear() { failures.clear() }

    companion object {
        const val INITIAL_MS = 1_000L
        const val MAX_MS = 30_000L
        const val STABLE_MS = 30_000L
    }
}
