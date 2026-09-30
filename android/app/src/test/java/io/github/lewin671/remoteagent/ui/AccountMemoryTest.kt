package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.proto.Agent
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class AccountMemoryTest {
    private fun write() {
        Drafts.newSession("/work/app", NewConfig("m1", "/work/app", Agent.AGENT_CODEX)).text.value = "上一个账户的草稿"
        Drafts.of("deny:a1").value = "别删"
        Outbox.draft("s1").value = "没发完的话"
        Outbox.of("s1") += Outgoing("r1", "发过的话")
        Outbox.rememberPrompt("s1", "第一句")
        Starts.of("/work/app").prompt = "启动时的话"
        Answers.of("q1", 0).options = listOf(1)
    }

    private fun assertEmpty() {
        assertEquals("", Drafts.newSession("/work/app", null).text.value)
        assertNull(Drafts.newSession("/work/app", null).config.value)
        assertEquals("", Drafts.of("deny:a1").value)
        assertEquals("", Outbox.draft("s1").value)
        assertTrue(Outbox.of("s1").isEmpty())
        assertNull(Outbox.prompt("s1"))
        assertEquals("", Starts.of("/work/app").prompt)
        assertEquals(emptyList<Int>(), Answers.of("q1", 0).options)
    }

    @Test
    fun anotherAccountOrALogoutStartsFromNothing() {
        AccountMemory.use("https://a device-1")
        write()
        // The same account, e.g. the app opened again: kept.
        AccountMemory.use("https://a device-1")
        assertEquals("上一个账户的草稿", Drafts.newSession("/work/app", null).text.value)
        assertFalse(Outbox.of("s1").isEmpty())
        // Another account sees none of it, even for the same path or IDs.
        AccountMemory.use("https://b device-2")
        assertEmpty()
        write()
        AccountMemory.clear()
        assertEmpty()
    }
}
