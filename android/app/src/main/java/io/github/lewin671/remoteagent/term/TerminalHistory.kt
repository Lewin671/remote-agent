package io.github.lewin671.remoteagent.term

import io.github.lewin671.remoteagent.proto.TerminalHistoryPage
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import java.util.UUID

/** Pages are newest first, for a reverse LazyColumn; each page's lines remain chronological. */
class TerminalHistory(private val scope: CoroutineScope) {
    data class Page(val id: String, val text: String)
    data class State(val visible: Boolean = false, val pages: List<Page> = emptyList(), val cursor: String = "", val pending: String = "", val error: String = "")
    private val mutable = MutableStateFlow(State())
    val state: StateFlow<State> = mutable
    private var timeout: Job? = null
    fun close() { timeout?.cancel(); mutable.value = State() }
    fun disconnected() {
        timeout?.cancel()
        mutable.value = mutable.value.copy(pending = "", error = "终端连接已断开，请返回终端并重新连接")
    }
    fun request(fresh: Boolean, send: (String, String) -> Unit) {
        if (fresh) { close(); mutable.value = State(visible = true) }
        val before = mutable.value
        if (!before.visible || before.pending.isNotEmpty() || (!fresh && before.pages.isNotEmpty() && before.cursor.isEmpty())) return
        val id = UUID.randomUUID().toString()
        mutable.value = before.copy(pending = id, error = "")
        timeout = scope.launch {
            delay(10_000)
            if (mutable.value.pending == id) mutable.value = mutable.value.copy(pending = "", error = "读取历史超时，请重试")
        }
        send(id, if (fresh) "" else before.cursor)
    }
    fun receive(page: TerminalHistoryPage) {
        val before = mutable.value
        if (!before.visible || before.pending != page.requestId) return
        timeout?.cancel()
        mutable.value = if (page.error.isNotEmpty()) before.copy(pending = "", error = "历史读取失败，请重试；若历史已过期，请返回终端后重新打开")
        else before.copy(pending = "", error = "", cursor = page.nextCursor, pages = if (page.linesCount == 0) before.pages else before.pages + Page(page.requestId, page.linesList.joinToString("\n")))
    }
}
