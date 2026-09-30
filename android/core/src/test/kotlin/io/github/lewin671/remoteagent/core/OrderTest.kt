package io.github.lewin671.remoteagent.core

import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionStatus
import org.junit.Assert.assertEquals
import org.junit.Test

class OrderTest {
    private fun at(ms: Long) = Timestamp.newBuilder().setSeconds(ms / 1000).setNanos((ms % 1000 * 1_000_000).toInt()).build()
    private fun s(id: String, created: Long, updated: Long, extra: Session.Builder.() -> Unit = {}) =
        SessionView(id, "m", Session.newBuilder().setCreatedAt(at(created)).apply(extra).build(), updatedMs = updated)
    @Test fun outputAndStatusDoNotMoveLiveSessions() {
        val a = s("a", 1000, 99999) { status = SessionStatus.SESSION_STATUS_RUNNING }
        val b = s("b", 2000, 1) { status = SessionStatus.SESSION_STATUS_IDLE }
        assertEquals(listOf("b", "a"), listOf(a, b).sortedWith(liveOrder).map { it.id })
        assertEquals(listOf("b", "a"), listOf(a.copy(updatedMs = 999999), b).sortedWith(liveOrder).map { it.id })
    }
    @Test fun attentionUsesTypeThenOldestWait() {
        val a = s("a", 100, 99999) { pendingApprovals = 1; attentionSince = at(2000) }
        val b = s("b", 200, 1) { pendingApprovals = 1; attentionSince = at(1000) }
        val q = s("q", 1, 999999) { pendingQuestions = 1; attentionSince = at(1) }
        assertEquals(listOf("b", "a", "q"), listOf(q, a, b).sortedWith(attentionOrder(RemoteState())).map { it.id })
    }
    @Test fun exitAndLegacyTimesIgnoreRelayTime() {
        val a = s("a", 100, 99999) { endedAt = at(1000) }
        val b = s("b", 200, 1) { endedAt = at(2000) }
        assertEquals(listOf("b", "a"), listOf(a, b).sortedWith(endedOrder).map { it.id })
        val oldA = s("a", 100, 99999); val oldB = s("b", 100, 1)
        assertEquals(listOf("a", "b"), listOf(oldB, oldA).sortedWith(endedOrder).map { it.id })
        assertEquals(listOf("a", "b"), listOf(oldB, oldA).sortedWith(liveOrder).map { it.id })
    }
}
