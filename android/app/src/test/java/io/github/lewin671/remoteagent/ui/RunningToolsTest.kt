package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentMessage
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.ToolCall
import io.github.lewin671.remoteagent.proto.ToolKind
import io.github.lewin671.remoteagent.proto.ToolStarted
import io.github.lewin671.remoteagent.proto.TurnEnded
import io.github.lewin671.remoteagent.proto.TurnStarted
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class RunningToolsTest {
    private fun ev(seq: Long, b: Event.Builder) = b.setSeq(seq).build()
    private fun start(seq: Long, title: String, id: String, input: String = "") =
        ev(seq, Event.newBuilder().setToolStarted(ToolStarted.newBuilder().setTitle(title).setCallId(id).setInput(input).setKind(ToolKind.TOOL_KIND_COMMAND).setItemType("Bash")))
    private fun call(seq: Long, title: String, id: String, status: String = "completed", input: String = "") =
        ev(seq, Event.newBuilder().setToolCall(ToolCall.newBuilder().setTitle(title).setCallId(id).setStatus(status).setInput(input).setKind(ToolKind.TOOL_KIND_COMMAND)))
    private fun say(seq: Long, text: String) = ev(seq, Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText(text)))
    private val turn = ev(1, Event.newBuilder().setTurnStarted(TurnStarted.getDefaultInstance()))

    private fun chat(events: List<Event>) = buildChat(events, derive(events, Agent.AGENT_CLAUDE), "MacBook")

    @Test
    fun aStartedToolShowsAtOnceWhereItStarted() {
        val items = chat(listOf(turn, start(2, "go test ./...", "a"), say(3, "等测试跑完")))
        assertTrue(items[0] is ChatItem.Running)
        assertEquals("e2", items[0].key)
        assertTrue(items[1] is ChatItem.Agent)
    }

    @Test
    fun itsResultUpdatesTheSameRow() {
        val items = chat(listOf(turn, start(2, "go test ./...", "a"), say(3, "等测试跑完"), call(4, "go test ./...", "a")))
        assertEquals(listOf("e2", "e3"), items.map { it.key })
        assertTrue(items[0] is ChatItem.Tool)
    }

    @Test
    fun parallelCallsKeepTheirOwnStateAndFailuresShow() {
        val items = chat(
            listOf(
                turn, start(2, "go test ./...", "a"), start(3, "go build ./...", "b"), start(4, "pnpm lint", "c"),
                call(5, "go build ./...", "b"), call(6, "pnpm lint", "c", status = "failed"),
            ),
        )
        assertEquals(listOf("e2", "e3", "e4"), items.map { it.key })
        assertTrue(items[0] is ChatItem.Running)
        assertTrue(items[1] is ChatItem.Tool)
        assertTrue(items[2] is ChatItem.Tool && !toolOk((items[2] as ChatItem.Tool).call))
    }

    @Test
    fun aRunningToolIsNeverFoldedIntoTheCompletedOnes() {
        val items = chat(
            listOf(
                turn, call(2, "ls", "x"), call(3, "pwd", "y"), start(4, "go test", "a"), call(5, "cat a", "z"),
            ),
        )
        assertTrue(items[0] is ChatItem.Tools)
        assertTrue(items[1] is ChatItem.Running)
        assertTrue(items[2] is ChatItem.Tool)
    }

    @Test
    fun anUnfinishedToolStaysUnconfirmedAfterTheTurn() {
        val items = chat(listOf(turn, start(2, "go test ./...", "a"), ev(3, Event.newBuilder().setTurnEnded(TurnEnded.getDefaultInstance()))))
        val r = items[0] as ChatItem.Running
        assertTrue(r.unfinished)
    }

    @Test
    fun aBackgroundCallIsNotShownAsASuccess() {
        val input = """{"command":"npm run dev","run_in_background":true}"""
        val items = chat(listOf(turn, start(2, "npm run dev", "a", input), call(3, "npm run dev", "a", input = input)))
        assertTrue((items[0] as ChatItem.Tool).background)
    }

    @Test
    fun theSummaryNamesTheToolOrCountsThem() {
        val one = derive(listOf(turn, start(2, "go test", "a")), Agent.AGENT_CLAUDE)
        assertEquals("Bash · 处理中", toolSummary(one, Agent.AGENT_CLAUDE, 10_000, 0))
        val two = derive(listOf(turn, start(2, "go test", "a"), start(3, "go vet", "b")), Agent.AGENT_CLAUDE)
        assertEquals("2 项处理中", toolSummary(two, Agent.AGENT_CLAUDE, 10_000, 0))
        val bg = derive(listOf(turn, start(2, "go test", "a"), ev(3, Event.newBuilder().setTurnEnded(TurnEnded.getDefaultInstance()))), Agent.AGENT_CLAUDE)
        assertEquals("后台工作 · 去终端确认", toolSummary(bg, Agent.AGENT_CLAUDE, 10_000, 0))
    }
}
