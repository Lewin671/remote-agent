package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.assertMessageEquals as assertEquals

import org.junit.Assert.*
import org.junit.Test
import java.time.ZoneId

class ListMessagesTest {
    @Test fun chineseTimeFormatsThresholdsAcrossCalendars() {
        val zone = ZoneId.of("Asia/Shanghai")
        val now = java.time.LocalDateTime.parse("2026-09-29T14:00:00").atZone(zone).toInstant().toEpochMilli()
        assertEquals(messageTextForTest(UiMessages.timeJustNow()), listWhen(now - 10_000L, now, zone).testText())
        assertEquals("3 分钟前", listWhen(now - 180_000L, now, zone).testText())
        assertEquals("9:05", listWhen(now - (4 * 3600 + 55 * 60) * 1000L, now, zone).testText())
        assertEquals(messageTextForTest(UiMessages.timeYesterday()), listWhen(now - 24 * 3600_000L, now, zone).testText())
        assertEquals("9月27日", listWhen(now - 48 * 3600_000L, now, zone).testText())
        assertEquals("", listWhen(0, now, zone).testText())
    }

    @Test fun theSameDeferredValueRendersInBothLanguagesWithoutChangingRawContent() {
        assertEquals("运行中", UiMessages.listStateRunning().messageText().testText())
        assertEquals("Running", UiMessages.listStateRunning().messageText().testText("en"))
        assertEquals("Unknown project", listProjectName("").testText("en"))
        assertEquals("Unknown computer", listMachineName(null).testText("en"))
    }
}
