package io.github.lewin671.remoteagent.core

import org.junit.Assert.*
import org.junit.Test
import java.time.ZoneId

class ListMessagesTest {
    @Test fun chineseTimeMatchesExistingThresholdsAcrossCalendarsAndDst() {
        for (name in listOf("Asia/Shanghai", "America/Los_Angeles", "UTC")) {
            val zone = ZoneId.of(name)
            for (date in listOf("2026-01-01T00:30:00", "2026-03-08T03:30:00", "2026-11-01T02:30:00")) {
                val now = java.time.LocalDateTime.parse(date).atZone(zone).toInstant().toEpochMilli()
                for (elapsed in listOf(0L, 59_999L, 60_000L, 3_599_999L, 3_600_000L, 86_400_000L, 7 * 86_400_000L, 365 * 86_400_000L)) {
                    assertEquals(whenText(now - elapsed, now, zone), listWhen(now - elapsed, now, zone).testText())
                }
                assertEquals("", listWhen(0, now, zone).testText())
            }
        }
    }

    @Test fun theSameDeferredValueRendersInBothLanguagesWithoutChangingRawContent() {
        val value = listOf(UiMessages.listStateRunning().messageText(), "原始 CLI / raw CLI".messageText()).joinMessages(" · ")
        assertEquals("运行中 · 原始 CLI / raw CLI", value.testText())
        assertEquals("Running · 原始 CLI / raw CLI", value.testText("en"))
        assertEquals("Unknown project", listProjectName("").testText("en"))
        assertEquals("Unknown computer", listMachineName(null).testText("en"))
    }
}
