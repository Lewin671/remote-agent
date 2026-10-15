package io.github.lewin671.remoteagent.term

import androidx.test.platform.app.InstrumentationRegistry
import com.termux.terminal.TerminalSession
import com.termux.terminal.TerminalSessionClient
import com.termux.view.TerminalView
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class TerminalHistoryTest {
    @Test fun incomingOutputKeepsHistoryInView() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        instrumentation.runOnMainSync {
            val view = TerminalView(instrumentation.targetContext, null)
            val client = object : TerminalSessionClient {
                override fun onTextChanged(session: TerminalSession) { view.onScreenUpdated() }
                override fun onTitleChanged(session: TerminalSession) {}
                override fun onSessionFinished(session: TerminalSession) {}
                override fun onCopyTextToClipboard(session: TerminalSession, text: String?) {}
                override fun onPasteTextFromClipboard(session: TerminalSession) {}
                override fun onBell(session: TerminalSession) {}
                override fun onColorsChanged(session: TerminalSession) {}
                override fun onTerminalCursorStateChange(state: Boolean) {}
                override fun getTerminalCursorStyle(): Int? = null
                override fun logError(tag: String?, message: String?) {}
                override fun logWarn(tag: String?, message: String?) {}
                override fun logInfo(tag: String?, message: String?) {}
                override fun logDebug(tag: String?, message: String?) {}
                override fun logVerbose(tag: String?, message: String?) {}
                override fun logStackTraceWithMessage(tag: String?, message: String?, e: Exception?) {}
                override fun logStackTrace(tag: String?, e: Exception?) {}
            }
            val session = TerminalSession(2000, client, object : TerminalSession.Sink {
                override fun onInput(data: ByteArray) {}
                override fun onViewSize(columns: Int, rows: Int) {}
            })
            view.setTerminalViewClient(RemoteTerminal.ViewClient {})
            view.setTextSize(16)
            view.layout(0, 0, 800, 400)
            view.attachSession(session)
            val history = (1..100).joinToString("\r\n") { "history-$it" }.toByteArray()
            session.snapshot(80, 10, history)
            assertTrue(session.emulator!!.screen.activeTranscriptRows > 0)
            view.topRow = -20
            fun visible() = session.emulator!!.screen.getSelectedText(0, view.topRow, 79, view.topRow + 9)
            val before = visible()
            val output = "\r\nnew output".toByteArray()
            session.append(output, output.size)
            assertEquals(-21, view.topRow)
            assertEquals(before, visible())
            view.topRow = 0
            session.append(output, output.size)
            assertEquals(0, view.topRow)
        }
    }
}
