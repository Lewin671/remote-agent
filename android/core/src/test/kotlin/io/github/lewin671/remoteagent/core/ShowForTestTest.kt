package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.AgentMessage
import io.github.lewin671.remoteagent.proto.Event
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** The fixed state that layout tests and screenshots render, without a server. */
class ShowForTestTest {
    @Test
    fun showsStateAndHistoryWithoutFetching() {
        val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "2222222222222222"), Api.defaultHttp())
        try {
            val m = MachineView("m", online = true, lastSeenMs = 1)
            val events = listOf(Event.newBuilder().setSeq(1).setAgentMessage(AgentMessage.newBuilder().setText("你好")).build())
            remote.showForTest(RemoteState(machines = mapOf("m" to m), loaded = true), mapOf("s" to events))
            assertEquals(m, remote.state.value.machines["m"])
            val flow = remote.events("s")
            Thread.sleep(200)
            // Nothing was fetched over the fixed history: the session is not in the lists.
            assertEquals(events, flow.value.events)
            assertTrue(flow.value.loaded)
        } finally {
            remote.close()
        }
    }
}
