package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.proto.Agent
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class AccountMemoryTest {
    private fun write() {
        ImageDrafts.forProject("/work/app").text.value = "上一个账户的草稿"
        Drafts.of("deny:a1").value = "别删"
        ImageDrafts.forSession("s1").text.value = "没发完的话"
        Outbox.of("s1") += Outgoing("r1")
        Starts.of("/work/app").prompt = "启动时的话"
    }

    private fun assertEmpty() {
        assertEquals("", ImageDrafts.forProject("/work/app").text.value)
        assertEquals("", Drafts.of("deny:a1").value)
        assertEquals("", ImageDrafts.forSession("s1").text.value)
        assertTrue(Outbox.of("s1").isEmpty())
        assertEquals("", Starts.of("/work/app").prompt)
    }

    @Test
    fun anotherAccountOrALogoutStartsFromNothing() {
        AccountMemory.use("https://a device-1")
        write()
        // The same account, e.g. the app opened again: kept.
        AccountMemory.use("https://a device-1")
        assertEquals("上一个账户的草稿", ImageDrafts.forProject("/work/app").text.value)
        assertFalse(Outbox.of("s1").isEmpty())
        // Another account sees none of it, even for the same path or IDs.
        AccountMemory.use("https://b device-2")
        assertEmpty()
        write()
        AccountMemory.clear()
        assertEmpty()
    }

    @Test
    fun settleNewSessionStartClearsDraftOutsideComposerLifetime() {
        AccountMemory.use("https://a device-1")
        val key = "/work/app"
        val images = ImageDrafts.forProject(key)
        images.text.value = "新建一条会话草稿"
        val st = Starts.of(key)
        st.prompt = "新建一条会话草稿"
        st.frozen = true

        settleNewSessionStart(
            io.github.lewin671.remoteagent.core.proto.AttachmentsSettledEffect.newBuilder()
                .setProjectKey(key)
                .setRequestId("req-1")
                .setConsumed(true)
                .build(),
        )

        assertEquals("", images.text.value)
        assertFalse(st.frozen)
        assertTrue(st.taken.isEmpty())
    }
}
