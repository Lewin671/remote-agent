package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.core.inputDigest
import io.github.lewin671.remoteagent.proto.ApprovalDecision
import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.ApprovalResolved
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

    private fun req(id: String, tool: String, input: String, command: String = "") =
        Event.newBuilder().setApprovalRequested(ApprovalRequested.newBuilder().setApprovalId(id).setToolName(tool).setToolInputJson(input).setCommand(command))

    private fun resolved(id: String, d: ApprovalDecision, message: String = "") =
        Event.newBuilder().setApprovalResolved(ApprovalResolved.newBuilder().setApprovalId(id).setDecision(d).setMessage(message))

    private fun call(tool: String, input: String, kind: ToolKind, title: String, digest: Boolean = true) =
        Event.newBuilder().setToolCall(
            ToolCall.newBuilder().setItemType(tool).setKind(kind).setTitle(title)
                .setInputDigest(if (digest) inputDigest(tool, input) else ""),
        )

    @Test
    fun keepsTheApprovalLineWithoutADigest() {
        // Codex: no digests, so the approval stays a line where it was asked.
        val events = listOf(
            ev(1, req("a", "shell", "", command = "curl /Users/me/x")),
            ev(2, resolved("a", ApprovalDecision.APPROVAL_DECISION_ALLOW)),
            ev(3, call("shell", "", ToolKind.TOOL_KIND_COMMAND, "curl /Users/me/x", digest = false)),
        )
        val items = buildChat(events, derive(events), "MacBook", "/tmp/w", "/Users/me")
        assertEquals("curl ~/x  已允许", (items[0] as ChatItem.Note).text)
        assertEquals("", (items[1] as ChatItem.Tool).decision)
    }

    @Test
    fun putsDecisionsOnTheirOwnCallsWhenRunInParallel() {
        val a = """{"file_path":"/tmp/w/a.txt","content":"a"}"""
        val b = """{"file_path":"/tmp/w/b.txt","content":"b"}"""
        // Both asked and decided before either result; b's result comes first.
        val events = listOf(
            ev(1, req("A", "Write", a)),
            ev(2, req("B", "Write", b)),
            ev(3, resolved("A", ApprovalDecision.APPROVAL_DECISION_ALLOW)),
            ev(4, resolved("B", ApprovalDecision.APPROVAL_DECISION_DENY, "不行")),
            ev(5, call("Write", b, ToolKind.TOOL_KIND_FILE_CHANGE, "/tmp/w/b.txt")),
            ev(6, call("Write", a, ToolKind.TOOL_KIND_FILE_CHANGE, "/tmp/w/a.txt")),
        )
        val items = buildChat(events, derive(events), "MacBook", "/tmp/w")
        assertEquals(2, items.size)
        val first = items[0] as ChatItem.Tool
        val second = items[1] as ChatItem.Tool
        assertEquals("编辑 b.txt", toolTitle(first.call, "/tmp/w"))
        assertEquals("已拒绝：不行" to false, first.decision to first.decisionOk)
        assertEquals("编辑 a.txt", toolTitle(second.call, "/tmp/w"))
        assertEquals("已允许" to true, second.decision to second.decisionOk)
    }

    @Test
    fun keepsLinesWhenCallsCannotBeToldApart() {
        val x = """{"command":"ls"}"""
        // The same input asked twice: no certain match, so both stay lines.
        val events = listOf(
            ev(1, req("A", "Bash", x, command = "ls")),
            ev(2, resolved("A", ApprovalDecision.APPROVAL_DECISION_ALLOW)),
            ev(3, req("B", "Bash", x, command = "ls")),
            ev(4, resolved("B", ApprovalDecision.APPROVAL_DECISION_ALLOW)),
            ev(5, call("Bash", x, ToolKind.TOOL_KIND_COMMAND, "ls")),
            ev(6, call("Bash", x, ToolKind.TOOL_KIND_COMMAND, "ls")),
        )
        val items = buildChat(events, derive(events), "MacBook")
        assertEquals(listOf("ls  已允许", "ls  已允许"), items.filterIsInstance<ChatItem.Note>().map { it.text })
        assertEquals(2, (items.last() as ChatItem.Tools).calls.size)
        // A call before the request is not its call either.
        val early = listOf(ev(1, call("Bash", x, ToolKind.TOOL_KIND_COMMAND, "ls")), ev(2, req("A", "Bash", x, command = "ls")), ev(3, resolved("A", ApprovalDecision.APPROVAL_DECISION_ALLOW)))
        assertEquals(1, buildChat(early, derive(early), "M").filterIsInstance<ChatItem.Note>().size)
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
