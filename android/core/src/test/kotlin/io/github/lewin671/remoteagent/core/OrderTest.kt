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

    @Test fun projectPositionsIgnoreOutputExitAndReloadButFollowNewSessions() {
        val a = s("a", 1000, 99999) { cwd = "/a" }
        val b = s("b", 2000, 1) { cwd = "/b" }
        fun keys(vararg list: SessionView) = projects(RemoteState(sessions = list.associateBy { it.id })).map { it.key }
        assertEquals(listOf("/b", "/a"), keys(a, b))
        val changed = a.copy(updatedMs = 999999, info = a.info!!.toBuilder().setEndedAt(at(5000)).setPendingApprovals(1).build())
        assertEquals(listOf("/b", "/a"), keys(b, changed))
        assertEquals(999999L, projects(RemoteState(sessions = mapOf("a" to changed))).single().updatedMs)
        assertEquals(listOf("/a", "/b"), keys(b, changed, s("new", 3000, 0) { cwd = "/a" }))
    }
    @Test fun projectTiesAndLegacyMetadataUsePathsInsteadOfUploadTimes() {
        val a = s("z", 1000, 99999) { cwd = "/a" }
        val b = s("a", 1000, 1) { cwd = "/b" }
        fun keys(vararg list: SessionView) = projects(RemoteState(sessions = list.associateBy { it.id })).map { it.key }
        assertEquals(listOf("/a", "/b"), keys(b, a))
        assertEquals(listOf("/a", "/b"), keys(b.copy(info = b.info!!.toBuilder().clearCreatedAt().build()), a.copy(info = a.info!!.toBuilder().clearCreatedAt().build())))
    }

    @Test fun liveProjectsFirstUntilTheirLastLiveSessionExits() {
        val ended = s("ended", 9000, 999999) { cwd = "/ended"; status = SessionStatus.SESSION_STATUS_EXITED }
        var a = s("a", 1000, 0) { cwd = "/a"; status = SessionStatus.SESSION_STATUS_RUNNING }
        var older = s("older", 500, 0) { cwd = "/a"; status = SessionStatus.SESSION_STATUS_IDLE }
        var b = s("b", 2000, 0) { cwd = "/b"; status = SessionStatus.SESSION_STATUS_IDLE }
        fun keys() = projects(RemoteState(sessions = listOf(ended, a, older, b).associateBy { it.id })).map { it.key }
        assertEquals(listOf("/b", "/a", "/ended"), keys())
        for (status in listOf(SessionStatus.SESSION_STATUS_STARTING, SessionStatus.SESSION_STATUS_IDLE, SessionStatus.SESSION_STATUS_BLOCKED)) {
            b = b.copy(info = b.info!!.toBuilder().setStatus(status).setPendingApprovals(1).build())
            assertEquals(listOf("/b", "/a", "/ended"), keys())
        }
        a = a.copy(info = a.info!!.toBuilder().setStatus(SessionStatus.SESSION_STATUS_EXITED).build())
        assertEquals(listOf("/b", "/a", "/ended"), keys())
        older = older.copy(info = older.info!!.toBuilder().setStatus(SessionStatus.SESSION_STATUS_EXITED).build())
        assertEquals(listOf("/b", "/ended", "/a"), keys())
        b = b.copy(info = b.info!!.toBuilder().setStatus(SessionStatus.SESSION_STATUS_EXITED).build())
        assertEquals(listOf("/ended", "/b", "/a"), keys())
    }
}
