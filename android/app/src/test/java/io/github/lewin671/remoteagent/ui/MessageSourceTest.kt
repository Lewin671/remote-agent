package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test

class MessageSourceTest {
    @Test fun messagesOnlyNameOtherSources() {
        assertNull(messageSource("本客户端", true, "MacBook"))
        assertEquals("MacBook 终端", messageSource(null, true, "MacBook"))
        assertEquals("Android · Pixel 9", messageSource("Android · Pixel 9", false, "MacBook"))
        assertNull(messageSource(null, false, "MacBook"))
    }

    @Test fun modeChangesSeparateTheNameAndText() {
        val e = Event.newBuilder().setSeq(1).setSettingsChanged(SettingsChanged.newBuilder().setMode(ApprovalMode.APPROVAL_MODE_PLAN)).build()
        val row = buildChat(listOf(e), derive(listOf(e)), "MacBook", who = { "Android · Pixel 9" }).filterIsInstance<ChatItem.Note>().single()
        assertEquals("Android · Pixel 9 将模式切到 计划", row.text)
        assertEquals("计划", row.emphasis)
        assertEquals(row.text.length - 2, noteText(row).spanStyles.single().start)
    }

    @Test fun toolDecisionsNameTheirSource() {
        val events = listOf(
            Event.newBuilder().setSeq(1).setApprovalRequested(ApprovalRequested.newBuilder().setApprovalId("a").setToolName("Bash").setInputDigest("d")).build(),
            Event.newBuilder().setSeq(2).setApprovalResolved(ApprovalResolved.newBuilder().setApprovalId("a").setDecision(ApprovalDecision.APPROVAL_DECISION_ALLOW)).build(),
            Event.newBuilder().setSeq(3).setToolCall(ToolCall.newBuilder().setItemType("Bash").setInputDigest("d").setStatus("completed")).build(),
        )
        fun label(name: String?) = buildChat(events, derive(events), "MacBook", who = { name }).filterIsInstance<ChatItem.Tool>().single().decision
        assertEquals("已允许 · Android · Pixel 9", label("Android · Pixel 9"))
        assertEquals("已允许", label("本客户端"))
        assertEquals("已允许 · 另一端", label(null))
    }
}
