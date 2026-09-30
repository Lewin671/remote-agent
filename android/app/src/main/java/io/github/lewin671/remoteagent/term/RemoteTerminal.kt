package io.github.lewin671.remoteagent.term

import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.KeyEvent
import android.view.MotionEvent
import com.google.protobuf.ByteString
import com.termux.terminal.TerminalSession
import com.termux.terminal.TerminalSessionClient
import com.termux.view.TerminalView
import com.termux.view.TerminalViewClient
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.TermConn
import io.github.lewin671.remoteagent.core.TermListener
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.proto.TerminalFrame
import io.github.lewin671.remoteagent.proto.TerminalResize
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import java.io.ByteArrayOutputStream

/**
 * A session's terminal: a Termux emulator fed by the remote byte stream
 * (architecture §11, §15). The emulator keeps the tmux window's size; the
 * keyboard, rotation and the view's own size never change it. Only "fit"
 * resizes the tmux window. When output arrives faster than it is shown the
 * stream is dropped and reopened for a fresh snapshot rather than skipping
 * bytes.
 */
class RemoteTerminal(private val remote: Remote, private val sessionId: String, private val scope: CoroutineScope) {
    sealed interface State {
        data object Connecting : State
        data object Live : State
        data class Closed(val reason: String) : State
    }

    private val main = Handler(Looper.getMainLooper())
    private val _state = MutableStateFlow<State>(State.Connecting)
    val state: StateFlow<State> = _state

    /** The tmux window's size, from the latest snapshot. */
    private val _size = MutableStateFlow(0 to 0)
    val size: StateFlow<Pair<Int, Int>> = _size
    private val fit = FitTracker()
    private val _fitted = MutableStateFlow(false)
    val fitted: StateFlow<Boolean> = _fitted

    /** The window changed size without this screen asking: another screen fitted it or the computer's terminal was resized. */
    private val _resizedElsewhere = MutableStateFlow(false)
    val resizedElsewhere: StateFlow<Boolean> = _resizedElsewhere

    fun dismissResized() {
        _resizedElsewhere.value = false
    }

    /** Columns and rows the view has room for at a readable size, for "fit". */
    var fitSize: Pair<Int, Int> = 0 to 0

    var view: TerminalView? = null
    private var conn: TermConn? = null
    private var job: Job? = null
    private var generation = 0
    private var frames = TerminalFrames(MAX_BACKLOG)

    val session = TerminalSession(2000, SessionClient(), object : TerminalSession.Sink {
        override fun onInput(data: ByteArray) = send(data)
        override fun onViewSize(columns: Int, rows: Int) {}
    })

    fun connect() {
        if (remote.state.value.conn != io.github.lewin671.remoteagent.core.ConnState.ONLINE) { close(); return }
        job?.cancel()
        conn?.close()
        conn = null
        val gen = ++generation
        frames.close()
        val queue = TerminalFrames(MAX_BACKLOG)
        frames = queue
        fun flush() {
            if (gen != generation) return
            val (batch, more) = queue.drain()
            applyBatch(batch)
            if (more) main.postDelayed({ flush() }, FRAME_INTERVAL_MS)
        }
        fun enqueue(frame: TerminalFrame) {
            when (queue.offer(frame)) {
                TerminalFrames.Offer.SCHEDULE -> main.postDelayed({ flush() }, FRAME_INTERVAL_MS)
                TerminalFrames.Offer.OVERFLOW -> main.post { if (gen == generation) connect() }
                else -> {}
            }
        }
        _state.value = State.Connecting
        fit.reset()
        _fitted.value = false
        job = scope.launch {
            try {
                val c = remote.openTerminal(sessionId, object : TermListener {
                    override fun onFrame(frame: TerminalFrame) {
                        enqueue(frame)
                    }

                    override fun onClose(reason: String) {
                        enqueue(TerminalFrame.newBuilder().setClosed(reason).build())
                    }
                })
                if (gen == generation) conn = c else c.close()
            } catch (e: Exception) {
                if (gen == generation) _state.value = State.Closed(errorText(e))
            }
        }
    }

    /** Concatenate adjacent output only; snapshots and closes retain their position. */
    private fun applyBatch(batch: List<TerminalFrame>) {
        val output = ByteArrayOutputStream()
        fun flushOutput() {
            if (output.size() == 0) return
            val bytes = output.toByteArray()
            session.append(bytes, bytes.size)
            output.reset()
        }
        for (frame in batch) {
            if (frame.kindCase == TerminalFrame.KindCase.OUTPUT) frame.output.writeTo(output)
            else { flushOutput(); apply(frame) }
        }
        flushOutput()
    }

    private fun apply(f: TerminalFrame) {
        when (f.kindCase) {
            TerminalFrame.KindCase.SNAPSHOT -> {
                val s = f.snapshot
                session.snapshot(s.cols, s.rows, s.data.toByteArray())
                _size.value = s.cols to s.rows
                if (fit.onSnapshot(s.cols to s.rows)) _resizedElsewhere.value = true
                _fitted.value = fit.fitted
                _state.value = State.Live
            }
            TerminalFrame.KindCase.OUTPUT -> {
                val b = f.output.toByteArray()
                session.append(b, b.size)
            }
            TerminalFrame.KindCase.CLOSED -> _state.value = State.Closed(f.closed.ifEmpty { "终端已关闭" })
            else -> {}
        }
    }

    fun send(data: ByteArray) {
        if (_state.value != State.Live) return
        conn?.send(TerminalFrame.newBuilder().setInput(ByteString.copyFrom(data)).build())
    }

    fun sendText(s: String) = send(s.toByteArray())

    /** Resizes the tmux window to this screen, or lets it follow the attached terminals again. */
    fun toggleFit() {
        val c = conn ?: return
        if (_fitted.value) {
            c.send(TerminalFrame.newBuilder().setResize(TerminalResize.newBuilder().setFit(false)).build())
            fit.release()
        } else {
            fitToScreen()
        }
        _resizedElsewhere.value = false
        _fitted.value = fit.fitted
    }

    /** Explicit user font changes may refit an already fitted window. */
    fun fitToScreen() {
        if (_state.value != State.Live) return
        val c = conn ?: return
        val (cols, rows) = fitSize
        if (cols < 10 || rows < 5) return
        c.send(TerminalFrame.newBuilder().setResize(TerminalResize.newBuilder().setCols(cols).setRows(rows).setFit(true)).build())
        fit.request(cols to rows)
        _resizedElsewhere.value = false
        _fitted.value = fit.fitted
    }

    fun close() {
        generation++
        frames.close()
        _state.value = State.Closed("终端已暂停，连接恢复后会重新获取画面")
        job?.cancel()
        conn?.close()
        conn = null
    }

    private inner class SessionClient : TerminalSessionClient {
        override fun onTextChanged(changedSession: TerminalSession) {
            view?.onScreenUpdated()
            onScreen?.invoke()
        }
        override fun onTitleChanged(changedSession: TerminalSession) {}
        override fun onSessionFinished(finishedSession: TerminalSession) {}
        override fun onCopyTextToClipboard(session: TerminalSession, text: String) {
            val cm = RaApp.instance.getSystemService(android.content.ClipboardManager::class.java)
            cm.setPrimaryClip(android.content.ClipData.newPlainText("terminal", text))
        }
        override fun onPasteTextFromClipboard(session: TerminalSession?) {
            val cm = RaApp.instance.getSystemService(android.content.ClipboardManager::class.java)
            val t = cm.primaryClip?.getItemAt(0)?.coerceToText(RaApp.instance)?.toString() ?: return
            sendText(t)
        }
        override fun onBell(session: TerminalSession) {}
        override fun onColorsChanged(session: TerminalSession) {}
        override fun onTerminalCursorStateChange(state: Boolean) {}
        override fun getTerminalCursorStyle(): Int? = null
        override fun logError(tag: String?, message: String?) = log(message)
        override fun logWarn(tag: String?, message: String?) = log(message)
        override fun logInfo(tag: String?, message: String?) {}
        override fun logDebug(tag: String?, message: String?) {}
        override fun logVerbose(tag: String?, message: String?) {}
        override fun logStackTraceWithMessage(tag: String?, message: String?, e: Exception?) = log(message)
        override fun logStackTrace(tag: String?, e: Exception?) = log(e?.javaClass?.simpleName)
    }

    /** Called after the screen changed, e.g. to keep the cursor in view. */
    var onScreen: (() -> Unit)? = null

    /** Callbacks from the view. [onTap] shows the keyboard. */
    class ViewClient(private val onTap: () -> Unit) : TerminalViewClient {
        override fun onScale(scale: Float): Float = 1f
        override fun onSingleTapUp(e: MotionEvent?) = onTap()
        override fun shouldBackButtonBeMappedToEscape() = false
        override fun shouldEnforceCharBasedInput() = false
        override fun shouldUseCtrlSpaceWorkaround() = false
        override fun isTerminalViewSelected() = true
        override fun copyModeChanged(copyMode: Boolean) {}
        override fun onKeyDown(keyCode: Int, e: KeyEvent?, session: TerminalSession?) = false
        override fun onKeyUp(keyCode: Int, e: KeyEvent?) = false
        override fun onLongPress(event: MotionEvent?) = false
        override fun readControlKey() = false
        override fun readAltKey() = false
        override fun readShiftKey() = false
        override fun readFnKey() = false
        override fun onCodePoint(codePoint: Int, ctrlDown: Boolean, session: TerminalSession?) = false
        override fun onEmulatorSet() {}
        override fun logError(tag: String?, message: String?) = log(message)
        override fun logWarn(tag: String?, message: String?) = log(message)
        override fun logInfo(tag: String?, message: String?) {}
        override fun logDebug(tag: String?, message: String?) {}
        override fun logVerbose(tag: String?, message: String?) {}
        override fun logStackTraceWithMessage(tag: String?, message: String?, e: Exception?) = log(message)
        override fun logStackTrace(tag: String?, e: Exception?) = log(e?.javaClass?.simpleName)
    }

    companion object {
        /** Bytes received but not yet shown before the stream is restarted. */
        const val MAX_BACKLOG = 2L * 1024 * 1024
        private const val FRAME_INTERVAL_MS = 33L

        // The emulator's messages can quote terminal output (a bad OSC 52
        // payload, an unknown control string): only their size is logged.
        private fun log(m: String?) {
            Log.w("remote-agent", redact(m))
        }

        fun redact(m: String?): String = "terminal: emulator message of ${m?.length ?: 0} chars"
    }
}

/**
 * Whether the shared tmux window is fitted to this screen (architecture §9):
 * only while the latest snapshot has the size this screen asked for. Once
 * another screen or the computer changes it, it no longer is, whatever this
 * screen did before.
 */
class FitTracker {
    private var requested: Pair<Int, Int>? = null
    private var confirmed = false
    private var last: Pair<Int, Int>? = null
    private var releasing = false

    /** Shown as fitted: asked for, and not changed since by anyone else. */
    val fitted: Boolean get() = requested != null

    fun request(size: Pair<Int, Int>) {
        requested = size
        confirmed = false
        releasing = false
    }

    /** Let the window follow the attached terminals again; the next size change is ours. */
    fun release() {
        requested = null
        confirmed = false
        releasing = true
    }

    fun reset() {
        requested = null
        confirmed = false
        last = null
        releasing = false
    }

    /** A new snapshot's size; true when it changed without this screen asking. */
    fun onSnapshot(size: Pair<Int, Int>): Boolean {
        val before = last
        last = size
        val r = requested
        if (r != null) {
            if (size == r) {
                confirmed = true
                return false
            }
            // A snapshot in flight before the resize took effect.
            if (!confirmed) return false
            requested = null
            return true
        }
        if (releasing) {
            if (before != null && size != before) releasing = false
            return false
        }
        return before != null && size != before
    }
}
