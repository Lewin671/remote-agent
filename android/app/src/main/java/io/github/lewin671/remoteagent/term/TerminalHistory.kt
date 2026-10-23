package io.github.lewin671.remoteagent.term

import io.github.lewin671.remoteagent.proto.TerminalHistoryPage
import io.github.lewin671.remoteagent.core.PagedList
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.currentCoroutineContext
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
class TerminalHistory(
    private val scope: CoroutineScope,
    private val worker: CoroutineDispatcher = Dispatchers.Default,
    private val parseLine: (String) -> List<SgrRun> = TerminalSgr::parse,
) {
    data class Page(val id: String, val lines: List<List<SgrRun>>) {
        val maxCells: Int = lines.maxOfOrNull { lineCells(it) } ?: 0
        val items: List<Pair<String, List<SgrRun>>> =
            lines.indices.reversed().map { i -> "$id:$i" to lines[i] }
    }

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
        val items: PagedList<Pair<String, List<SgrRun>>> = PagedList.empty(),
        val maxCells: Int = 0,
    )

    private val mutable = MutableStateFlow(State())
    val state: StateFlow<State> = mutable
    private var timeout: Job? = null
    private var processing: Job? = null

    /** Starts reading: the next page is a fresh capture of the terminal. */
    fun open() = replace(State(visible = true))

    /** Leaves reading; what was read is gone. */
    fun close() = replace(State())

    private fun replace(next: State) {
        timeout?.cancel()
        processing?.cancel()
        processing = null
        mutable.value = next
    }

    /**
     * The connection is gone. With the reader open the lines read so far stay readable and no
     * more can be read; before it opened there was nothing to keep, so reading just ends.
     */
    fun disconnected() {
        timeout?.cancel()
        processing?.cancel()
        processing = null
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
        if (processing?.isActive == true) return
        timeout?.cancel()
        if (page.error.isNotEmpty()) {
            // An expired capture cannot be read from again, but that wording belongs to the
            // reader: before it opens, any failure is a failure, with a retry at hand.
            val expired = before.entered && page.error.contains("expired")
            mutable.value = before.copy(pending = "", failed = !expired, expired = expired)
            return
        }
        processing = scope.launch {
            val prepared = withContext(worker) {
                val lines = page.linesList.map {
                    currentCoroutineContext().ensureActive()
                    parseLine(it)
                }
                Page(page.requestId, lines)
            }
            val current = mutable.value
            if (!current.visible || current.pending != page.requestId) return@launch
            if (prepared.lines.isEmpty()) {
                // A session with nothing to scroll back to never opens a blank reader.
                mutable.value = if (current.pages.isEmpty()) State() else current.copy(pending = "")
                return@launch
            }
            mutable.value = current.copy(
                pending = "", failed = false, expired = false, cursor = page.nextCursor,
                pages = current.pages + prepared, entered = true,
                items = current.items.append(prepared.items),
                maxCells = maxOf(current.maxCells, prepared.maxCells),
            )
        }
    }

    companion object {
        /** How long one page may take before it is given up on, with a retry in its place. */
        const val TIMEOUT_MS = 10_000L
    }
}
