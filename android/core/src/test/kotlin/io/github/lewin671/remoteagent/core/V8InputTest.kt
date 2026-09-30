package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test

class V8InputTest {
    private fun input(seq: Long, id: String, status: InputStatus, mode: SendMode = SendMode.SEND_MODE_UNSPECIFIED, paused: Boolean = false, text: String = "") =
        Event.newBuilder().setSeq(seq).setInputState(InputState.newBuilder().setRequestId(id).setStatus(status).setMode(mode).setPaused(paused).setText(text)).build()

    @Test fun pendingProjectionFollowsQueueHeldPauseResumeAndDelivery() {
        val events = listOf(
            input(1, "queue", InputStatus.INPUT_STATUS_QUEUED, SendMode.SEND_MODE_QUEUE, text = "later"),
            input(2, "steer", InputStatus.INPUT_STATUS_QUEUED, SendMode.SEND_MODE_STEER, text = "now"),
            input(3, "steer", InputStatus.INPUT_STATUS_HELD_BY_CLI),
            input(4, "queue", InputStatus.INPUT_STATUS_QUEUED, paused = true),
        )
        val pending = pendingInputs(derive(events))
        assertEquals(listOf("queue", "steer"), pending.map { it.id })
        assertEquals("later", pending[0].text)
        assertEquals(SendMode.SEND_MODE_QUEUE, pending[0].mode)
        assertTrue(pending[0].paused)
        assertEquals(SendMode.SEND_MODE_STEER, pending[1].mode)
        assertTrue(pending[1].held)
        assertEquals(listOf("steer"), pendingInputs(derive(events + input(5, "queue", InputStatus.INPUT_STATUS_CANCELED))).map { it.id })
        assertTrue(pendingInputs(derive(events + input(5, "queue", InputStatus.INPUT_STATUS_CANCELED) + input(6, "steer", InputStatus.INPUT_STATUS_SUBMITTED))).isEmpty())
        val resumed = pendingInputs(derive(events + input(5, "queue", InputStatus.INPUT_STATUS_QUEUED)))
        assertFalse(resumed[0].paused)
    }

    @Test fun sendAndApprovalCommandsCarryV8Choices() {
        assertEquals(SendMode.SEND_MODE_UNSPECIFIED, wireSendMode(SendMode.SEND_MODE_STEER, false))
        assertEquals(SendMode.SEND_MODE_QUEUE, wireSendMode(SendMode.SEND_MODE_QUEUE, true))
        assertThrows(IllegalStateException::class.java) { wireSendMode(SendMode.SEND_MODE_QUEUE, false) }
        val send = SendRequest.newBuilder().setSessionId("s").setRequestId("r").setMode(SendMode.SEND_MODE_QUEUE).build()
        val approval = ResolveApprovalRequest.newBuilder().setSessionId("s").setApprovalId("a").setAllow(true).setAlways(true).build()
        assertEquals(SendMode.SEND_MODE_QUEUE, send.mode)
        assertTrue(approval.always)
        assertEquals("`go test` 开头的命令以后不再问", approvalAlwaysText("Yes, and don't ask again for commands that start with `go test`"))
        assertEquals("本项目访问 /w 以后不再问", approvalAlwaysText("Yes, and always allow access to /w from this project"))
        assertEquals("本会话自动接受编辑", approvalAlwaysText("Yes, and switch to accept edits"))
        assertEquals("New CLI wording", approvalAlwaysText("New CLI wording"))
    }
}
