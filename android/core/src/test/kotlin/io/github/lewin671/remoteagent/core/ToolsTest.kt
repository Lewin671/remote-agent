package io.github.lewin671.remoteagent.core

import org.junit.Test

class ToolsTest {
    @Test
    fun clockFormatsMinutesAndHours() {
        assertMessageEquals("2:11", clock(131_000))
        assertMessageEquals("1:02:03", clock(3_723_000))
    }
}
