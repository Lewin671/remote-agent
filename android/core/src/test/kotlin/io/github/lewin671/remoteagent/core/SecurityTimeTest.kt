package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.assertMessageEquals as assertEquals

import java.time.Instant
import java.time.ZoneId
import java.util.Locale
import org.junit.Test

class SecurityTimeTest {
    @Test fun calendarLabelsAcrossMidnightYearAndDst() {
        val original = Locale.getDefault()
        try {
            Locale.setDefault(Locale.US)
            val zone = ZoneId.of("America/Los_Angeles")
            fun ms(value: String) = Instant.parse(value).toEpochMilli()
            val now = ms("2026-12-31T20:00:00Z")
            assertEquals("今天 05:02", securityTime(ms("2026-12-31T13:02:59Z"), now, zone = zone, copy = ::messageTextForTest))
            assertEquals("明天 05:02", securityTime(ms("2027-01-01T13:02:59Z"), now, zone = zone, copy = ::messageTextForTest))
            assertEquals("1月2日 05:02", securityTime(ms("2027-01-02T13:02:59Z"), now, zone = zone, copy = ::messageTextForTest))
            assertEquals("05:02", securityTime(ms("2027-01-02T13:02:59Z"), now, timeOnly = true, zone = zone, copy = ::messageTextForTest))
            assertEquals("明天 03:02", securityTime(ms("2026-03-08T10:02:00Z"), ms("2026-03-07T20:00:00Z"), zone = zone, copy = ::messageTextForTest))
            assertEquals(messageTextForTest(UiMessages.securityAndroidNotReadYet()), securityTime(0, now, zone = zone, copy = ::messageTextForTest))
        } finally { Locale.setDefault(original) }
    }
}
