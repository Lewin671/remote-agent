package io.github.lewin671.remoteagent.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class PairLinkSecurityTest {
    @Test fun rejectsMalformedQrWithoutThrowing() {
        for (query in listOf("code=12345678&s=%", "code=12345678&s=%GG",
            "code=12345678&s=https%3A%2F%2Frelay.example&c=%A",
            "code=bad&s=https%3A%2F%2Frelay.example",
            "code=12345678&s=https%3A%2F%2Frelay.example&c=AA",
            "code=12345678&s=http%3A%2F%2Fremote.example")) {
            assertNull(query, PairLink.parse("https://app.example/#/pair?$query"))
        }
    }

    @Test fun validQrRoundTrips() {
        val link = PairLink("12345678", "https://relay.example", ByteArray(32) { it.toByte() })
        val parsed = PairLink.parse(link.url("https://app.example"))!!
        assertEquals(link.code, parsed.code)
        assertEquals(link.server, parsed.server)
        org.junit.Assert.assertArrayEquals(link.commit, parsed.commit)
    }
}
