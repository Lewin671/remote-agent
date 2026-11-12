package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.assertMessageEquals as assertEquals

import io.github.lewin671.remoteagent.proto.AccountPrefsObject
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentSettings
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.ProjectPrefs
import io.github.lewin671.remoteagent.proto.ProjectSettings
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class PrefsTest {
    private fun set(agent: Agent, model: String = "", mode: ApprovalMode = ApprovalMode.APPROVAL_MODE_UNSPECIFIED) =
        AgentSettings.newBuilder().setAgent(agent).setModel(model).setMode(mode).build()

    @Test
    fun aSettingsChangeRedoneOnANewerObjectKeepsWhatTheOtherClientChanged() {
        val base = applySettings(null, PrefsOp.SetSettings("/a", "mac", agent = Agent.AGENT_CODEX, sets = listOf(set(Agent.AGENT_CODEX, "astra"))), 1)
        val other = applySettings(base, PrefsOp.SetSettings("/a", "mac", agent = Agent.AGENT_CLAUDE, sets = listOf(set(Agent.AGENT_CLAUDE, "sonnet"))), 2)
        val s = applySettings(other, PrefsOp.SetSettings("/a", "other", sets = listOf(set(Agent.AGENT_CODEX, "sol", ApprovalMode.APPROVAL_MODE_ASK))), 3)
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
        // Another machine: all CLIs get an empty entry, the remembered CLI stays.
        val moved = applySettings(next, PrefsOp.SetSettings("/a", "vps", moved = true), 3)
        assertEquals("vps", moved.machineId)
        assertEquals(Agent.AGENT_CLAUDE, moved.agent)
        assertEquals(listOf(set(Agent.AGENT_CODEX), set(Agent.AGENT_CLAUDE), set(Agent.AGENT_ANTIGRAVITY)), moved.agentsList)
        // A set edited together with the machine is kept.
        val both = applySettings(next, PrefsOp.SetSettings("/a", "vps", moved = true, sets = listOf(set(Agent.AGENT_CLAUDE, "opus"))), 3)
        assertEquals(listOf(set(Agent.AGENT_CODEX), set(Agent.AGENT_CLAUDE, "opus"), set(Agent.AGENT_ANTIGRAVITY)), both.agentsList)
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
    fun recentOpsAreBounded() {
        var p = ProjectPrefs.getDefaultInstance()
        repeat(100) { p = applyPrefsOp(p, PrefsOp.Pin("/x$it"), "op$it", 1) }
        assertEquals(RECENT_OPS, p.recentOpsCount)
        assertEquals("op99", p.recentOpsList.last())
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
        val g = projectGroups(state, copy = ::messageTextForTest)
        assertEquals(listOf("/c", "/a"), g.pinned.map { it.key })
        assertEquals(listOf("/e", "/b"), g.others.map { it.key })
        assertEquals(listOf("/d"), g.hidden.map { it.key })
        // A hidden project's session still waits for the user.
        assertTrue(state.sessions.values.any { projectKey(it) == "/d" && it.info!!.pendingApprovals > 0 })
        assertTrue(isHidden(state, "/d"))
        assertTrue(isPinned(state, "/c"))
    }
}
