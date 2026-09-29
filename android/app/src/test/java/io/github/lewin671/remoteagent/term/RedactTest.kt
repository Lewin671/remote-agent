package io.github.lewin671.remoteagent.term

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test

class RedactTest {
    @Test
    fun logsOnlyTheSize() {
        val m = "OSC Manipulate selection, invalid string 'sk-secret-token"
        val out = RemoteTerminal.redact(m)
        assertFalse(out.contains("secret"))
        assertEquals("terminal: emulator message of ${m.length} chars", out)
    }
}
