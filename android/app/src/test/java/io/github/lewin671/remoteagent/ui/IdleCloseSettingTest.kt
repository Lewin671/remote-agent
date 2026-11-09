package io.github.lewin671.remoteagent.ui

import org.junit.Assert.assertEquals
import org.junit.Test

class IdleCloseSettingTest {
    @Test
    fun theSettingReadsInWordsWhateverTheDaemonHolds() {
        assertEquals(listOf("关闭", "1 小时", "2 小时", "6 小时", "12 小时", "24 小时"), IDLE_CLOSE_CHOICES.map(::idleCloseLabel))
        // Values set elsewhere still read right.
        assertEquals("3 小时", idleCloseLabel(180))
        assertEquals("90 分钟", idleCloseLabel(90))
        assertEquals("168 小时", idleCloseLabel(10080))
        assertEquals("关闭", idleCloseLabel(-5))
        // 0 is the daemon's default.
        assertEquals("6 小时", idleCloseLabel(0))
    }

    @Test
    fun theChosenOptionIsCheckedAndOthersLeaveNoneChecked() {
        assertEquals(0, idleCloseChoice(-1))
        assertEquals(0, idleCloseChoice(-30))
        assertEquals(3, idleCloseChoice(360))
        assertEquals(3, idleCloseChoice(0))
        assertEquals(5, idleCloseChoice(1440))
        assertEquals(-1, idleCloseChoice(90))
    }
}
