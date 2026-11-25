package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.proto.SessionAgentMessageItem
import io.github.lewin671.remoteagent.core.proto.SessionTimelineItem
import io.github.lewin671.remoteagent.core.proto.SessionTimelineView
import io.github.lewin671.remoteagent.core.proto.SessionViewModel
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** The fixed state that layout tests and screenshots render, without a server. */
class ShowForTestTest {
    @get:org.junit.Rule val hangGuard = hangGuard()
    @Test
    fun showsStateAndHistoryWithoutFetching() = runBlocking {
        val remote =
            fixtureRemote(
                Account("http://127.0.0.1:1", "2222222222222222"),
                Api.defaultHttp(),
            )
        try {
            val m = MachineView("m", online = true, lastSeenMs = 1)
            val vm = SessionViewModel.newBuilder()
                .setTimeline(
                    SessionTimelineView.newBuilder()
                        .setLoaded(true)
                        .addItems(
                            SessionTimelineItem.newBuilder()
                                .setId("e1")
                                .setSeq(1)
                                .setAgentMessage(SessionAgentMessageItem.newBuilder().setMarkdown("你好")),
                        ),
                )
                .build()
            remote.showForTest(
                RemoteState(machines = mapOf("m" to m), loaded = true),
                sessions = mapOf("s" to vm),
            )
            val gotMachine = remote.coreHost.snapshotSettingsView().machinesList.firstOrNull { it.machineId == "m" }
            assertEquals("m", gotMachine?.machineId)
            assertEquals(true, gotMachine?.online)
            val got = remote.watchSession("s").first()
            // Nothing was fetched over the fixed history: the session is not in the lists.
            assertEquals("你好", got.timeline.itemsList.single().agentMessage.markdown)
            assertTrue(got.timeline.loaded)
        } finally {
            remote.close()
        }
    }

    @Test
    fun showForTestWorksWithoutSecurityStorage() {
        val remote = Remote(
            Account(server = "https://example.com", deviceId = "d".repeat(32), accountId = "a".repeat(32)),
            Api.defaultHttp(),
        )
        try {
            val expected = RemoteState(conn = ConnState.ONLINE, loaded = true)
            remote.showForTest(expected)
            assertEquals(io.github.lewin671.remoteagent.core.proto.ConnState.CONN_STATE_CONNECTED, remote.coreHost.connectionView.value.state)
        } finally {
            remote.close()
        }
    }
}
