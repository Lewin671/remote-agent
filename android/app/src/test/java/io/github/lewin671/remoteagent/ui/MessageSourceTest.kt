package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class MessageSourceTest {
    @get:org.junit.Rule(order = 0) val sessionMessages = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    @Test fun messagesOnlyNameOtherSources() {
        assertNull(messageSource(ChatSource("本客户端", true), true, "MacBook"))
        assertEquals("MacBook 终端", messageSource(null, true, "MacBook"))
        assertEquals("Android · Pixel 9", messageSource(ChatSource("Android · Pixel 9"), false, "MacBook"))
        assertNull(messageSource(null, false, "MacBook"))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.sessionOriginSelf()), messageSource(ChatSource("本客户端"), false, "MacBook"))
    }

    @Test fun modeChangesSeparateTheNameAndText() {
        val e = Event.newBuilder().setSeq(1).setSettingsChanged(SettingsChanged.newBuilder().setMode(ApprovalMode.APPROVAL_MODE_PLAN)).build()
        val row = buildChat(listOf(e), derive(listOf(e)), "MacBook", who = { ChatSource("Android · Pixel 9") }).filterIsInstance<ChatItem.Note>().single()
        assertEquals("Android · Pixel 9 将模式切到 计划", row.text)
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.modesPlan()), row.emphasis)
        assertEquals(row.text.length - 2, noteText(row).spanStyles.single().start)
    }

    @Test fun interruptNotesAlsoHideThisClientsSource() {
        val e = Event.newBuilder().setSeq(1).setTurnEnded(TurnEnded.newBuilder().setOutcome(TurnOutcome.TURN_OUTCOME_ABORTED)).build()
        fun note(source: String) = buildChat(listOf(e), derive(listOf(e)), "MacBook", who = { ChatSource(source, source == "本客户端") }).filterIsInstance<ChatItem.Note>().single().text
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.sessionInterrupted()), note("本客户端"))
        assertEquals("已中断（Android · Pixel 9）", note("Android · Pixel 9"))
    }

    @Test fun toolDecisionsNameTheirSource() {
        val events = listOf(
            Event.newBuilder().setSeq(1).setApprovalRequested(ApprovalRequested.newBuilder().setApprovalId("a").setToolName("Bash").setInputDigest("d")).build(),
            Event.newBuilder().setSeq(2).setApprovalResolved(ApprovalResolved.newBuilder().setApprovalId("a").setDecision(ApprovalDecision.APPROVAL_DECISION_ALLOW)).build(),
            Event.newBuilder().setSeq(3).setToolCall(ToolCall.newBuilder().setItemType("Bash").setInputDigest("d").setStatus("completed")).build(),
        )
        fun label(name: String?) = buildChat(events, derive(events), "MacBook", who = { ChatSource(name, name == "本客户端") }).filterIsInstance<ChatItem.Tool>().single().decision
        assertEquals("已允许 · Android · Pixel 9", label("Android · Pixel 9"))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.approvalAllowed()), label("本客户端"))
        assertEquals("已允许 · 另一端", label(null))
    }
}
