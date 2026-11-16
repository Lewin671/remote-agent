package io.github.lewin671.remoteagent.core.transport

/**
 * Why a direct attempt or round ended, for the last few attempts (mirrors the
 * web collector's end reasons; docs/transport.md §6/§7). Fixed words and times
 * in milliseconds after the attempt began; never an address, SDP, ICE
 * credential, error text or payload. Bounded to [MAX_LINES] lines; the lines
 * are written when an attempt ends, so the volume follows the retry back-off.
 */
object DirectDiag {
    const val MAX_LINES = 24
    private val ring = ArrayDeque<String>()

    /** Hook for the host (the app logs the lines, tests read them). */
    @Volatile var sink: ((String) -> Unit)? = null

    fun note(line: String) {
        synchronized(ring) {
            if (ring.size >= MAX_LINES) ring.removeFirst()
            ring.addLast(line)
        }
        sink?.invoke(line)
    }

    fun lines(): List<String> = synchronized(ring) { ring.toList() }

    fun clear() = synchronized(ring) { ring.clear() }

    /** Milliseconds, or "-" when the step was not reached. */
    internal fun ms(since: Long, mark: Long): String = if (mark <= 0L) "-" else (mark - since).toString()
}
