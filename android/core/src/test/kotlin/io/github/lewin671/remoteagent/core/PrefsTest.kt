package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.AccountPrefsObject
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentSettings
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.ProjectPrefs
import io.github.lewin671.remoteagent.proto.ProjectSettings
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

    private fun set(agent: Agent, model: String = "", mode: ApprovalMode = ApprovalMode.APPROVAL_MODE_UNSPECIFIED) =
        AgentSettings.newBuilder().setAgent(agent).setModel(model).setMode(mode).build()

    @Test
    fun settingsConflictRedoesOneProjectAndPreservesOtherSettingsAndUnknownFields() {
        val server = FakeServer()
        val first = ProjectSettings.newBuilder().setPath("/a").setMachineId("mac").setUpdatedMs(1000).build()
        val second = ProjectSettings.newBuilder().setPath("/b").setMachineId("vps").setUpdatedMs(1000).build()
        write(server, PrefsObject.EMPTY, PrefsOp.SetSettings("/a", "mac"), "s1")
        val stale = PrefsObject.EMPTY
        val after = write(server, stale, PrefsOp.SetSettings("/b", "vps"), "s2")
        assertEquals(listOf(first, second), after.prefs.settingsList)
        val updated = first.toBuilder().addAgents(set(Agent.AGENT_CODEX, "new-model")).build()
        val again = write(server, after, PrefsOp.SetSettings("/a", "mac", sets = listOf(set(Agent.AGENT_CODEX, "new-model"))), "s3")
        assertEquals(listOf(second, updated), again.prefs.settingsList)
        val unknown = byteArrayOf(0x9a.toByte(), 0x06, 0x01, 0x51)
        val newer = ProjectPrefs.parseFrom(again.prefs.toByteArray() + unknown)
        val pinned = applyPrefsOp(newer, PrefsOp.Pin("/c"), "s4", 1000)
        assertTrue(pinned.toByteArray().asList().windowed(unknown.size).any { it == unknown.asList() })
        assertEquals(listOf(second, updated), pinned.settingsList)
    }

    @Test
    fun aSettingsChangeRedoneOnANewerObjectKeepsWhatTheOtherClientChanged() {
        val server = FakeServer()
        val base = write(server, PrefsObject.EMPTY, PrefsOp.SetSettings("/a", "mac", agent = Agent.AGENT_CODEX, sets = listOf(set(Agent.AGENT_CODEX, "astra"))), "s1")
        // Another client edits Claude Code's set and a session there remembers Claude Code.
        write(server, base, PrefsOp.SetSettings("/a", "mac", agent = Agent.AGENT_CLAUDE, sets = listOf(set(Agent.AGENT_CLAUDE, "sonnet"))), "s2")
        // This client, still on the older object, edits Codex's set only.
        val mine = write(server, base, PrefsOp.SetSettings("/a", "other", sets = listOf(set(Agent.AGENT_CODEX, "sol", ApprovalMode.APPROVAL_MODE_ASK))), "s3")
        val s = mine.prefs.settingsList.single()
        assertEquals("mac", s.machineId)
        assertEquals(Agent.AGENT_CLAUDE, s.agent)
        assertEquals(listOf(set(Agent.AGENT_CODEX, "sol", ApprovalMode.APPROVAL_MODE_ASK), set(Agent.AGENT_CLAUDE, "sonnet")), s.agentsList)
    }

    @Test
    fun aStartFillsInOnlyWhatIsMissingAndAnotherMachineStartsEverySetOver() {
        val stored = applySettings(null, PrefsOp.SetSettings("/a", "mac", agent = Agent.AGENT_CODEX, sets = listOf(set(Agent.AGENT_CODEX, "astra")), keepExisting = true), 1)
        assertEquals("mac", stored.machineId)
        assertEquals(listOf(set(Agent.AGENT_CODEX, "astra")), stored.agentsList)
        // A later start with the other CLI: its set is added, the machine and Codex's set stay.
        val next = applySettings(stored, PrefsOp.SetSettings("/a", "vps", agent = Agent.AGENT_CLAUDE,
            sets = listOf(set(Agent.AGENT_CLAUDE, "sonnet"), set(Agent.AGENT_CODEX, "other")), keepExisting = true), 2)
        assertEquals("mac", next.machineId)
        assertEquals(Agent.AGENT_CLAUDE, next.agent)
        assertEquals(listOf(set(Agent.AGENT_CODEX, "astra"), set(Agent.AGENT_CLAUDE, "sonnet")), next.agentsList)
        assertEquals(2L, next.updatedMs)
        // Another machine: both CLIs get an empty entry, the remembered CLI stays.
        val moved = applySettings(next, PrefsOp.SetSettings("/a", "vps", moved = true), 3)
        assertEquals("vps", moved.machineId)
        assertEquals(Agent.AGENT_CLAUDE, moved.agent)
        assertEquals(listOf(set(Agent.AGENT_CODEX), set(Agent.AGENT_CLAUDE)), moved.agentsList)
        // A set edited together with the machine is kept.
        val both = applySettings(next, PrefsOp.SetSettings("/a", "vps", moved = true, sets = listOf(set(Agent.AGENT_CLAUDE, "opus"))), 3)
        assertEquals(listOf(set(Agent.AGENT_CODEX), set(Agent.AGENT_CLAUDE, "opus")), both.agentsList)
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
