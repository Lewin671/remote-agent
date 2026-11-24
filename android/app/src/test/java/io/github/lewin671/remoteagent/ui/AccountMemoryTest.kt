package io.github.lewin671.remoteagent.ui

import org.junit.Assert.assertEquals
import org.junit.Test

class AccountMemoryTest {
    private fun write() {
        Drafts.of("deny:a1").value = "别删"
    }

    private fun assertEmpty() {
        assertEquals("", Drafts.of("deny:a1").value)
    }

    @Test
    fun anotherAccountOrALogoutStartsFromNothing() {
        AccountMemory.use("https://a device-1")
        write()
        // The same account, e.g. the app opened again: kept.
        AccountMemory.use("https://a device-1")
        assertEquals("别删", Drafts.of("deny:a1").value)
        // Another account sees none of it, even for the same IDs.
        AccountMemory.use("https://b device-2")
        assertEmpty()
        write()
        AccountMemory.clear()
        assertEmpty()
    }
}
