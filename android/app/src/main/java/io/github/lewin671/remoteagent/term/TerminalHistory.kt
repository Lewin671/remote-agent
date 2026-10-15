package io.github.lewin671.remoteagent.term

import io.github.lewin671.remoteagent.proto.TerminalHistoryPage
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import java.util.UUID

/**
 * The pages the reader shows: newest first, each page's lines in their own order, each line
 * already parsed into styled runs (architecture §11). One request is in flight at a time and
 * only the answer to the newest one is taken; a page that is late, or left over from an
 * earlier reading, is dropped.
 */
class TerminalHistory(private val scope: CoroutineScope) {
    data class Page(val id: String, val lines: List<List<SgrRun>>)

    /**
     * [visible] from the gesture that asks for history until reading is left; [entered] once
     * there is something to read, so a capture with no lines never opens the reader. [cursor]
     * is where earlier pages continue from, empty at the oldest record the terminal keeps.
     */
    data class State(
        val visible: Boolean = false,
        val entered: Boolean = false,
        val pages: List<Page> = emptyList(),
        val cursor: String = "",
        val pending: String = "",
        val failed: Boolean = false,
        val expired: Boolean = false,
        val disconnected: Boolean = false,
    )

    private val mutable = MutableStateFlow(State())
    val state: StateFlow<State> = mutable
    private var timeout: Job? = null

    /** Starts reading: the next page is a fresh capture of the terminal. */
    fun open() = replace(State(visible = true))

    /** Leaves reading; what was read is gone. */
    fun close() = replace(State())

    private fun replace(next: State) {
        timeout?.cancel()
        mutable.value = next
    }

    /**
     * The connection is gone. With the reader open the lines read so far stay readable and no
     * more can be read; before it opened there was nothing to keep, so reading just ends.
     */
    fun disconnected() {
        timeout?.cancel()
        val before = mutable.value
        if (!before.visible) return
        mutable.value = if (before.entered) before.copy(pending = "", failed = false, expired = false, disconnected = true) else State()
    }

    /** Asks for the first page ([fresh]) or the one before [cursor]; failures are retried by hand. */
    fun request(fresh: Boolean, send: (String, String) -> Unit) {
        if (fresh) open()
        val before = mutable.value
        if (!before.visible || before.pending.isNotEmpty()) return
        if (!fresh && before.pages.isNotEmpty() && before.cursor.isEmpty()) return
        val id = UUID.randomUUID().toString()
        mutable.value = before.copy(pending = id, failed = false)
        timeout = scope.launch {
            delay(TIMEOUT_MS)
            if (mutable.value.pending == id) mutable.value = mutable.value.copy(pending = "", failed = true)
        }
        send(id, if (fresh) "" else before.cursor)
    }

    fun receive(page: TerminalHistoryPage) {
        val before = mutable.value
        if (!before.visible || before.pending != page.requestId) return
        timeout?.cancel()
        if (page.error.isNotEmpty()) {
            // An expired capture cannot be read from again, but that wording belongs to the
            // reader: before it opens, any failure is a failure, with a retry at hand.
            val expired = before.entered && page.error.contains("expired")
            mutable.value = before.copy(pending = "", failed = !expired, expired = expired)
            return
        }
        val lines = page.linesList.map { TerminalSgr.parse(it) }
        if (lines.isEmpty()) {
            // A session with nothing to scroll back to: no reader at all, and nothing to say.
            mutable.value = if (before.pages.isEmpty()) State() else before.copy(pending = "")
            return
        }
        mutable.value = before.copy(
            pending = "", failed = false, expired = false, cursor = page.nextCursor,
            pages = before.pages + Page(page.requestId, lines), entered = true,
        )
    }

    companion object {
        /** How long one page may take before it is given up on, with a retry in its place. */
        const val TIMEOUT_MS = 10_000L
    }
}
