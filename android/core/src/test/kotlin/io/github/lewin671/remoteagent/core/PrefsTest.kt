package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.AccountPrefsObject
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.ProjectPrefs
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class PrefsTest {
    private val key = ByteArray(32) { 7 }
    private val codec = PrefsCodec(key, "dev")

    /** The server's compare-and-set, as internal/server does it. */
    private inner class FakeServer {
        var obj: AccountPrefsObject = AccountPrefsObject.newBuilder().setObjectId(PROJECT_PREFS).build()
        var loseNextReply = false

        fun put(expected: Long, env: Envelope): PutResult {
            if (expected != obj.revision) return PutResult(false, obj)
            obj = AccountPrefsObject.newBuilder().setObjectId(PROJECT_PREFS).setRevision(expected + 1).setEnvelope(env).build()
            if (loseNextReply) {
                loseNextReply = false
                throw java.net.SocketTimeoutException()
            }
            return PutResult(true, obj)
        }
    }

    private fun write(server: FakeServer, base: PrefsObject, op: PrefsOp, id: String) = runBlocking {
        writePrefsOp(codec, base, op, id, 1000, { e, env -> server.put(e, env) }, { server.obj })
    }

    @Test
    fun aConflictRedoesTheOperationAndKeepsTheOtherClientsChange() {
        val server = FakeServer()
        val a = write(server, PrefsObject.EMPTY, PrefsOp.Pin("/p/foo"), "op1")
        assertEquals(listOf("/p/foo"), a.pinned)
        // Another client, still at revision 0, hides a project.
        val b = write(server, PrefsObject.EMPTY, PrefsOp.Hide("/p/billing"), "op2")
        assertEquals(2L, b.revision)
        assertEquals(listOf("/p/foo"), b.pinned)
        assertEquals(setOf("/p/billing"), b.hidden)
        assertEquals(b, codec.open(server.obj))
    }

    @Test
    fun pinsKeepTheirOrderAndHidingUnpins() {
        var p = ProjectPrefs.getDefaultInstance()
        p = applyPrefsOp(p, PrefsOp.Pin("/b"), "1", 1)
        p = applyPrefsOp(p, PrefsOp.Pin("/a"), "2", 2)
        p = applyPrefsOp(p, PrefsOp.Pin("/b"), "3", 3)
        assertEquals(listOf("/b", "/a"), p.pinnedList.map { it.path })
        p = applyPrefsOp(p, PrefsOp.Hide("/b"), "4", 4)
        assertEquals(listOf("/a"), p.pinnedList.map { it.path })
        p = applyPrefsOp(p, PrefsOp.Unhide("/b"), "5", 5)
        // Restored among the others, not pinned again.
        assertEquals(listOf("/a"), p.pinnedList.map { it.path })
        assertTrue(p.hiddenList.isEmpty())
    }

    @Test
    fun aLostReplyIsSettledByTheOpId() {
        val server = FakeServer()
        server.loseNextReply = true
        val r = write(server, PrefsObject.EMPTY, PrefsOp.Pin("/p/foo"), "op1")
        assertEquals(1L, r.revision)
        // Redoing it finds it in and writes nothing more.
        val again = write(server, PrefsObject.EMPTY, PrefsOp.Pin("/p/foo"), "op1")
        assertEquals(1L, again.revision)
        assertEquals(1L, server.obj.revision)
    }

    @Test
    fun recentOpsAreBounded() {
        var p = ProjectPrefs.getDefaultInstance()
        repeat(100) { p = applyPrefsOp(p, PrefsOp.Pin("/x$it"), "op$it", 1) }
        assertEquals(RECENT_OPS, p.recentOpsCount)
        assertEquals("op99", p.recentOpsList.last())
    }

    @Test
    fun aRevisionTheEnvelopeDoesNotSayIsRefused() {
        val env = codec.seal(3, ProjectPrefs.getDefaultInstance())
        val lie = AccountPrefsObject.newBuilder().setObjectId(PROJECT_PREFS).setRevision(4).setEnvelope(env).build()
        assertThrows(OpenException::class.java) { codec.open(lie) }
    }

    @Test
    fun aPushNeverTakesTheListBack() {
        val newer = PrefsObject(5, ProjectPrefs.getDefaultInstance())
        assertEquals(newer, newerPrefs(newer, PrefsObject(4, ProjectPrefs.getDefaultInstance())))
        assertFalse(newerPrefs(null, newer) !== newer)
    }
}

class ProjectGroupsTest {
    private fun session(id: String, cwd: String, updated: Long, pending: Int = 0) =
        SessionView(id, "m", io.github.lewin671.remoteagent.proto.Session.newBuilder().setCwd(cwd).setCreatedAt(com.google.protobuf.Timestamp.newBuilder().setNanos((updated * 1_000_000).toInt())).setPendingApprovals(pending).build(), updatedMs = updated)

    @Test
    fun pinnedFirstInPinOrderOthersByCreationHiddenApart() {
        val prefs = ProjectPrefs.newBuilder()
            .addPinned(io.github.lewin671.remoteagent.proto.ProjectPref.newBuilder().setPath("/c"))
            .addPinned(io.github.lewin671.remoteagent.proto.ProjectPref.newBuilder().setPath("/a"))
            .addHidden(io.github.lewin671.remoteagent.proto.ProjectPref.newBuilder().setPath("/d"))
            .build()
        val state = RemoteState(
            sessions = listOf(session("1", "/a", 10), session("2", "/b", 30), session("3", "/c", 5), session("4", "/d", 50, pending = 1), session("5", "/e", 40))
                .associateBy { it.id },
            prefs = PrefsObject(3, prefs),
        )
        val g = projectGroups(state)
        assertEquals(listOf("/c", "/a"), g.pinned.map { it.key })
        assertEquals(listOf("/e", "/b"), g.others.map { it.key })
        assertEquals(listOf("/d"), g.hidden.map { it.key })
        // A hidden project's session still waits for the user.
        assertTrue(state.sessions.values.any { projectKey(it) == "/d" && it.info!!.pendingApprovals > 0 })
        assertTrue(isHidden(state, "/d"))
        assertTrue(isPinned(state, "/c"))
    }
}
