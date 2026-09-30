package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.proto.AgentMessage
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.SettingsChanged
import io.github.lewin671.remoteagent.proto.TokenUsage
import io.github.lewin671.remoteagent.proto.ToolCall
import io.github.lewin671.remoteagent.proto.ToolKind
import io.github.lewin671.remoteagent.proto.TurnEnded
import io.github.lewin671.remoteagent.proto.UserMessage
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class ChatTest {
    @Test
    fun parsesMarkdownBlocks() {
        val b = parseMarkdown(
            """
            # Title
            Some `code` and **bold**.

            - one
              continued
            1. first
            ```go
            func main() {}
            ```
            | a | b |
            |---|---|
            | 1 | 2 |
            > quote
            """.trimIndent(),
        )
        assertEquals(Block.Heading(1, "Title"), b[0])
        assertEquals(Block.Para("Some `code` and **bold**."), b[1])
        assertEquals(Block.Item(false, "•", 0, "one continued"), b[2])
        assertEquals(Block.Item(true, "1.", 0, "first"), b[3])
        assertEquals(Block.Code("go", "func main() {}"), b[4])
        assertEquals(Block.Table(listOf("a", "b"), listOf(listOf("1", "2"))), b[5])
        assertEquals(Block.Quote("quote"), b[6])
    }

    private fun ev(seq: Long, b: Event.Builder) = b.setSeq(seq).build()

    @Test
    fun foldsToolRunsAndNotesCompaction() {
        val events = listOf(
            ev(1, Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("hi"))),
            ev(2, Event.newBuilder().setToolCall(ToolCall.newBuilder().setKind(ToolKind.TOOL_KIND_COMMAND).setTitle("ls"))),
            ev(3, Event.newBuilder().setToolCall(ToolCall.newBuilder().setKind(ToolKind.TOOL_KIND_COMMAND).setTitle("pwd"))),
            ev(4, Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText("done"))),
            ev(5, Event.newBuilder().setTokenUsage(TokenUsage.newBuilder().setTotalTokens(1000).setLastTotalTokens(860).setContextWindow(1000))),
            ev(6, Event.newBuilder().setSettingsChanged(SettingsChanged.newBuilder().setCompacted(true))),
            ev(7, Event.newBuilder().setTokenUsage(TokenUsage.newBuilder().setTotalTokens(1100).setLastTotalTokens(140).setContextWindow(1000))),
            ev(8, Event.newBuilder().setTurnEnded(TurnEnded.newBuilder().setDurationMs(252_000))),
        )
        val items = buildChat(events, derive(events), "MacBook")
        assertTrue(items[0] is ChatItem.User)
        assertEquals(2, (items[1] as ChatItem.Tools).calls.size)
        assertEquals("done", (items[2] as ChatItem.Agent).text)
        assertEquals("上下文已压缩 · 86% → 14%", (items[3] as ChatItem.Note).text)
        assertEquals("4 分 12 秒 · 100 tokens", (items[4] as ChatItem.TurnEnd).text)
    }

    @Test
    fun showsAnApprovedCallOnceWithItsDecision() {
        val events = listOf(
            ev(1, Event.newBuilder().setApprovalRequested(io.github.lewin671.remoteagent.proto.ApprovalRequested.newBuilder().setApprovalId("a").setToolName("Write"))),
            ev(2, Event.newBuilder().setApprovalResolved(io.github.lewin671.remoteagent.proto.ApprovalResolved.newBuilder().setApprovalId("a").setDecision(io.github.lewin671.remoteagent.proto.ApprovalDecision.APPROVAL_DECISION_ALLOW))),
            ev(3, Event.newBuilder().setToolCall(ToolCall.newBuilder().setKind(ToolKind.TOOL_KIND_FILE_CHANGE).setTitle("/private/tmp/w/CONTRIBUTING.md"))),
            ev(4, Event.newBuilder().setApprovalRequested(io.github.lewin671.remoteagent.proto.ApprovalRequested.newBuilder().setApprovalId("b").setCommand("curl x"))),
            ev(5, Event.newBuilder().setApprovalResolved(io.github.lewin671.remoteagent.proto.ApprovalResolved.newBuilder().setApprovalId("b").setDecision(io.github.lewin671.remoteagent.proto.ApprovalDecision.APPROVAL_DECISION_DENY))),
            ev(6, Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText("ok"))),
        )
        val items = buildChat(events, derive(events), "MacBook", "/tmp/w")
        val tool = items[0] as ChatItem.Tool
        assertEquals("已允许", tool.decision)
        assertEquals("编辑 CONTRIBUTING.md", toolTitle(tool.call, "/tmp/w"))
        // A denial with no call after it stays a line of its own.
        assertEquals("curl x  已拒绝", (items[1] as ChatItem.Note).text)
        assertEquals(3, items.size)
    }

    @Test
    fun foldsOnlyCompletedCalls() {
        val events = listOf(
            ev(1, Event.newBuilder().setToolCall(ToolCall.newBuilder().setKind(ToolKind.TOOL_KIND_COMMAND).setTitle("a"))),
            ev(2, Event.newBuilder().setToolCall(ToolCall.newBuilder().setKind(ToolKind.TOOL_KIND_COMMAND).setTitle("b").setExitCode(1))),
            ev(3, Event.newBuilder().setToolCall(ToolCall.newBuilder().setKind(ToolKind.TOOL_KIND_COMMAND).setTitle("c"))),
            ev(4, Event.newBuilder().setToolCall(ToolCall.newBuilder().setKind(ToolKind.TOOL_KIND_FILE_CHANGE).setTitle("d"))),
        )
        val items = buildChat(events, derive(events), "MacBook")
        assertEquals(listOf("a"), listOf((items[0] as ChatItem.Tool).call.title))
        assertEquals("b", (items[1] as ChatItem.Tool).call.title)
        assertEquals("完成了 2 个工具调用", toolsSummary((items[2] as ChatItem.Tools).calls))
    }
}
