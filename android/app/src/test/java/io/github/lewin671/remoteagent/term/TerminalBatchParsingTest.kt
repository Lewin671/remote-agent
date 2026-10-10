package io.github.lewin671.remoteagent.term

import com.termux.terminal.TerminalSession
import com.termux.terminal.TerminalSessionClient
import java.lang.reflect.Proxy
import org.junit.Assert.*
import org.junit.Test

class TerminalBatchParsingTest {
    private fun session(): TerminalSession {
        val client = Proxy.newProxyInstance(TerminalSessionClient::class.java.classLoader, arrayOf(TerminalSessionClient::class.java)) { _, _, _ -> null } as TerminalSessionClient
        return TerminalSession(100, client, object : TerminalSession.Sink {
            override fun onInput(data: ByteArray) {}
            override fun onViewSize(columns: Int, rows: Int) {}
        }).apply { snapshot(80, 24, byteArrayOf()) }
    }
    @Test fun aSnapshotDiscardsUnfinishedParserSequences() {
        for (sequence in listOf("\u001b]0;unfinished", "\u001bPunfinished", "\u001b[", "\u00e4")) {
            val terminal = session()
            val old = "OLD$sequence".toByteArray()
            terminal.append(old, old.size)
            terminal.snapshot(80, 24, "NEW SNAPSHOT".toByteArray())
            assertEquals("NEW SNAPSHOT", terminal.emulator.screen.getSelectedText(0, 0, 79, 23).trim())
        }
        val terminal = session()
        terminal.append(byteArrayOf(0xe4.toByte()), 1)
        terminal.snapshot(80, 24, "NEW SNAPSHOT".toByteArray())
        assertEquals("NEW SNAPSHOT", terminal.emulator.screen.getSelectedText(0, 0, 79, 23).trim())
    }

    @Test fun batchingPreservesSplitUtf8AndControlSequences() {
        val bytes = "hello 中😀\r\nworld\u001b[1;1HOK\u001b[2;5Hdone".toByteArray()
        val split = session(); val merged = session()
        bytes.forEach { split.append(byteArrayOf(it), 1) }
        merged.append(bytes, bytes.size)
        val a = split.emulator; val b = merged.emulator
        assertEquals(a.cursorCol, b.cursorCol); assertEquals(a.cursorRow, b.cursorRow)
        assertEquals(a.screen.getSelectedText(0, 0, 79, 23), b.screen.getSelectedText(0, 0, 79, 23))
        assertTrue(b.screen.getSelectedText(0, 0, 79, 23).contains("中😀"))
    }
}
