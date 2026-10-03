package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.derive
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
    fun retainsAcknowledgedStartUntilHistoryArrivesAndShowsFailuresHonestly() {
        val d = io.github.lewin671.remoteagent.core.Derived()
        assertEquals("first", pendingStartPrompt("first", emptyList(), d, emptySet())?.text)
        d.inputs["later-local"] = io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_FAILED
        d.inputs["start"] = io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_QUEUED
        // Waiting: the pending bubble shows it, not a second copy here.
        assertEquals(null, pendingStartPrompt("first", emptyList(), d, setOf("later-local")))
        d.inputs["start"] = io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_SUBMITTED
        assertEquals("已送达电脑", pendingStartPrompt("first", emptyList(), d, setOf("later-local"))?.caption)
        val recorded = listOf(Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("first")).build())
        assertEquals(null, pendingStartPrompt("first", recorded, d, setOf("later-local")))
        d.inputs["start"] = io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_CANCELED
        assertEquals(null, pendingStartPrompt("first", emptyList(), d, setOf("later-local")))
        d.inputs["start"] = io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_FAILED
        d.failures["start"] = "CLI 没认出图片"
        val failed = pendingStartPrompt("first", emptyList(), d, setOf("later-local"))!!
        assertTrue(failed.warning)
        assertEquals("没能发送：CLI 没认出图片", failed.caption)
        d.inputs["start"] = io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_UNCONFIRMED
        assertTrue(pendingStartPrompt("first", emptyList(), d, setOf("later-local"))!!.warning)
        assertEquals(null, pendingStartPrompt(null, emptyList(), d, emptySet()))
    }

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
    fun marksWhereAForkLeavesItsSource() {
        val at = java.time.ZonedDateTime.now().withHour(14).withMinute(6).toEpochSecond()
        val events = listOf(
            ev(1, Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("登录接口偶尔超时"))),
            ev(2, Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText("原因是限流器"))),
            ev(3, Event.newBuilder().setTime(com.google.protobuf.Timestamp.newBuilder().setSeconds(at))
                .setSessionForked(io.github.lewin671.remoteagent.proto.SessionForked.newBuilder().setSourceSessionId("src").setSourceTitle("登录接口偶尔超时"))),
            ev(4, Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("换一种做法"))),
        )
        val items = buildChat(events, derive(events), "MacBook")
        assertEquals(4, items.size)
        val fork = items[2] as ChatItem.Fork
        assertEquals(ChatItem.Fork("e3", "14:06", "src", "登录接口偶尔超时"), fork)
        // A message after the fork appends without losing the line.
        val projection = ChatProjection()
        projection.update(events.take(3), derive(events.take(3)), "MacBook")
        assertEquals(fork, projection.update(events, derive(events), "MacBook")[2])

        // The source as this client has it now; else the title the event recorded, not to be opened.
        val renamed = io.github.lewin671.remoteagent.core.SessionView("src", "m", io.github.lewin671.remoteagent.proto.Session.newBuilder().setTitle("登录超时：限流").build())
        assertEquals("登录超时：限流" to true, forkSource(fork, mapOf("src" to renamed)))
        assertEquals("登录接口偶尔超时" to true, forkSource(fork, mapOf("src" to io.github.lewin671.remoteagent.core.SessionView("src", "m"))))
        assertEquals("登录接口偶尔超时" to false, forkSource(fork, emptyMap()))
        assertEquals("另一个会话" to false, forkSource(fork.copy(sourceTitle = ""), emptyMap()))
    }

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

    // Ported from "approval records" in web/src/model.test.ts.
    private fun approval(id: String, command: String, digest: String, json: String = "{\"command\":\"$command\"}") =
        Event.newBuilder().setApprovalRequested(
            ApprovalRequested.newBuilder().setApprovalId(id).setToolName("Bash").setCommand(command).setToolInputJson(json).setInputDigest(digest),
        )

    private fun resolved(id: String, d: ApprovalDecision) =
        Event.newBuilder().setApprovalResolved(ApprovalResolved.newBuilder().setApprovalId(id).setDecision(d))

    private fun call(title: String, callId: String, digest: String, status: String = "completed", tool: String = "Bash", kind: ToolKind = ToolKind.TOOL_KIND_COMMAND) =
        Event.newBuilder().setToolCall(ToolCall.newBuilder().setTitle(title).setCallId(callId).setItemType(tool).setInputDigest(digest).setKind(kind).setStatus(status))

    private fun rows(vararg list: Event.Builder): List<String> {
        val events = list.mapIndexed { i, b -> ev(i + 1L, b) }
        return buildChat(events, derive(events, io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE), "M").map {
            when (it) {
                is ChatItem.Note -> "record " + listOf(it.text, it.meta).filter { m -> m.isNotEmpty() }.joinToString(" ")
                is ChatItem.Tool -> "tool " + it.call.title + if (it.decision.isNotEmpty()) " " + it.decision else ""
                is ChatItem.Tools -> "tools"
                else -> it.javaClass.simpleName
            }
        }
    }

    @Test
    fun putsDecisionsOnlyOnCallsThatSurelyWereTheirs() {
        // Claude Code runs tools in parallel: approvals wait, an unrelated call
        // finishes first, then the approved ones, out of order.
        val got = rows(
            Event.newBuilder().setTurnStarted(io.github.lewin671.remoteagent.proto.TurnStarted.getDefaultInstance()),
            approval("A", "mkdir x", "dA"),
            approval("B", "rm y", "dB"),
            approval("C", "npm test", "dC"),
            approval("D", "npm test", "dC"),
            resolved("B", ApprovalDecision.APPROVAL_DECISION_DENY),
            resolved("A", ApprovalDecision.APPROVAL_DECISION_ALLOW),
            resolved("C", ApprovalDecision.APPROVAL_DECISION_ALLOW),
            resolved("D", ApprovalDecision.APPROVAL_DECISION_ALLOW),
            call("Read a.go", "r", "dR", tool = "Read", kind = ToolKind.TOOL_KIND_OTHER),
            call("rm y", "d", "", status = "failed"), // no digest
            call("mkdir x", "m", "dA"),
            call("npm test", "t1", "dC"),
            call("npm test", "t2", "dC"),
        )
        assertEquals(
            listOf(
                // B has no call it can surely be tied to, and C and D two each.
                "record rm y 已拒绝",
                "record npm test 已允许",
                "record npm test 已允许",
                "tool Read a.go",
                "tool rm y",
                // A's call carries A's result, and A's own line goes.
                "tool mkdir x 已允许",
                "tools",
            ),
            got,
        )
    }

    @Test
    fun matchesByTheDaemonsDigestsOnly() {
        val got = rows(
            // Input that decodes differently here than in Go: only the daemon's
            // digest says which call it is.
            approval("A", "a", "db", json = "{\"command\":\"a\",\"COMMAND\":\"b\"}"),
            resolved("A", ApprovalDecision.APPROVAL_DECISION_ALLOW),
            // A request without a digest (Codex), next to a call
            // whose digest its input would give.
            approval("B", "ls", ""),
            resolved("B", ApprovalDecision.APPROVAL_DECISION_ALLOW),
            call("b", "1", "db"),
            call("ls", "2", "c7b68ac37f364473e922936708e7f43c293dd07b295171566c07ff5fe024fab9"),
        )
        assertEquals(listOf("record ls 已允许", "tool b 已允许", "tool ls"), got)
    }

    @Test
    fun keepsCodexApprovalsAsTheirOwnLines() {
        val got = rows(
            Event.newBuilder().setApprovalRequested(ApprovalRequested.newBuilder().setApprovalId("A").setToolName("shell").setCommand("mkdir x")),
            resolved("A", ApprovalDecision.APPROVAL_DECISION_ALLOW),
            Event.newBuilder().setToolCall(ToolCall.newBuilder().setTitle("mkdir x").setKind(ToolKind.TOOL_KIND_COMMAND).setStatus("completed")),
        )
        assertEquals(listOf("record mkdir x 已允许", "tool mkdir x"), got)
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

    @Test
    fun detailsKeepTheOriginalCommand() {
        val c = ToolCall.newBuilder().setKind(ToolKind.TOOL_KIND_COMMAND).setTitle("ls -la /Users/me/Code/foo/").setInput("ls -la /Users/me/Code/foo/").build()
        assertEquals("ls -la .", toolTitle(c, "/Users/me/Code/foo", "/Users/me"))
        // The row is shortened; the details show the command as it ran.
        assertEquals("ls -la /Users/me/Code/foo/", toolRaw(c, "/Users/me/Code/foo", "/Users/me"))
        // Nothing was shortened and the input is the title: nothing more to show.
        val same = c.toBuilder().setTitle("go test ./...").setInput("go test ./...").build()
        assertEquals("", toolRaw(same, "/Users/me/Code/foo", "/Users/me"))
        val other = same.toBuilder().setInput("{\"command\":\"go test ./...\",\"timeout\":60}").build()
        assertEquals(other.input, toolRaw(other, "", ""))
    }

    @Test
    fun rawCodexItemTypesAreNotToolTitles() {
        val generic = ToolCall.newBuilder().setKind(ToolKind.TOOL_KIND_OTHER).setItemType("ToolCall").setTitle("ToolCall").build()
        assertEquals("工具", toolTitle(generic, ""))
        assertEquals("", toolRaw(generic, "", ""))
        val command = generic.toBuilder().setKind(ToolKind.TOOL_KIND_COMMAND).setItemType("CommandExecution").setTitle("CommandExecution").build()
        assertEquals("命令", toolTitle(command, ""))
    }

    @Test
    fun namesARunningCallAsWhenItIsDone() {
        val events = listOf(ev(1, Event.newBuilder().setToolStarted(io.github.lewin671.remoteagent.proto.ToolStarted.newBuilder()
            .setTitle("/private/tmp/w/hello.txt").setCallId("c").setKind(ToolKind.TOOL_KIND_FILE_CHANGE).setItemType("Write"))))
        val running = derive(events).tools.single()
        assertEquals("编辑 hello.txt", toolTitle(running, "/tmp/w"))
        val done = ToolCall.newBuilder().setTitle("/private/tmp/w/hello.txt").setKind(ToolKind.TOOL_KIND_FILE_CHANGE).setItemType("Write").build()
        assertEquals(toolTitle(done, "/tmp/w"), toolTitle(running, "/tmp/w"))
    }

    private fun input(id: String, st: io.github.lewin671.remoteagent.proto.InputStatus, reason: String = "") =
        Event.newBuilder().setInputState(io.github.lewin671.remoteagent.proto.InputState.newBuilder().setRequestId(id).setStatus(st).setReason(reason))

    private fun exit() = Event.newBuilder().setSessionState(io.github.lewin671.remoteagent.proto.SessionState.newBuilder().setStatus(io.github.lewin671.remoteagent.proto.SessionStatus.SESSION_STATUS_EXITED))

    @Test
    fun showsMessagesTheSessionEndedBeforeSending() {
        val q = io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_QUEUED
        val c = io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_CANCELED
        // The daemon says why; the user's own cancel is not one of these.
        val events = listOf(ev(1, input("mine", q)), ev(2, input("other", q)), ev(3, input("gone", q)), ev(4, input("gone", c)), ev(5, input("mine", c, "exited")), ev(6, input("other", c, "exited")), ev(7, exit()))
        val d = derive(events)
        assertEquals(listOf("mine", "other"), d.unsent.keys.toList())
        val items = buildChat(events, d, "M", textOf = { if (it == "mine") "补一个测试" else null })
        assertEquals(listOf(ChatItem.Unsent("umine", "mine", "补一个测试"), ChatItem.Unsent("uother", "other", null)), items.filterIsInstance<ChatItem.Unsent>())
    }
}
