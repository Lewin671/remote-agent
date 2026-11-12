package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.approvalDisplay
import io.github.lewin671.remoteagent.core.approvalHead
import io.github.lewin671.remoteagent.core.approvalBody
import io.github.lewin671.remoteagent.core.approvalTruncated
import io.github.lewin671.remoteagent.core.approvalTruncatedMessage

import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.NotifyApproval
import io.github.lewin671.remoteagent.push.Notifier
import org.junit.Assert.*
import org.junit.Test

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class ApprovalDisplayTest {
    @get:org.junit.Rule(order = 0) val sessionMessages = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    @Test fun revealsControlsAndPreservesOrdinaryLanguage() {
        val normal = "العربية 中文 👩‍💻\n\tcode"
        assertEquals(normal, approvalDisplay(normal))
        assertEquals("\\u061c\\u200e\\u200f\\u202e\\u2066\\u2069\\u000d\\u001b\\u009b",
            approvalDisplay("\u061c\u200e\u200f\u202e\u2066\u2069\r\u001b\u009b"))
    }

    @Test fun anApprovalIsHeadedByWhatItAsksForWhereverAllowIsOffered() {
        val command = ApprovalRequested.newBuilder().setCommand("go test ./...").setToolName("Bash").build()
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.listStateAndroidRequestCommand()), uiTextForTest(approvalHead(command)))
        assertEquals("go test ./...", approvalBody(command))
        // A tool that is not a shell command has no command: its name heads it and its input is what is shown.
        val write = ApprovalRequested.newBuilder().setToolName("Write").setToolInputJson("{\"file_path\":\"/p/foo/a.go\"}").build()
        assertEquals("请求权限 · Write", uiTextForTest(approvalHead(write)))
        assertEquals("{\"file_path\":\"/p/foo/a.go\"}", approvalBody(write))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.listStateAndroidRequestPermission()), uiTextForTest(approvalHead(ApprovalRequested.getDefaultInstance())))
    }

    @Test fun notificationAndDockAgreeWithoutRewritingApproval() {
        val raw = "printf '\u202e'; dangerous"
        val a = ApprovalRequested.newBuilder().setApprovalId("approval").setCommand(raw).build()
        val before = a.toByteArray()
        val shown = approvalDisplay(raw)
        assertEquals("请求执行" to shown, dockSummary(DockItem.Approval(a), "Codex"))
        assertEquals("请求执行" to shown, Notifier.approvalText(NotifyApproval.newBuilder().setCommand(raw).build()))
        assertEquals(raw, a.command)
        assertArrayEquals(before, a.toByteArray())
    }
    @Test fun detectsTruncationInEveryApprovalTextWithoutChangingTheEvent() {
        val marker = "\n…（remote-agent：内容过长，已截断）"
        val base = ApprovalRequested.newBuilder().setApprovalId("approval").setCommand("complete")
        assertFalse(approvalTruncated(base.build()))
        for (a in listOf(base.clone().setCommand("cut" + marker).build(),
                base.clone().setDescription("cut" + marker).build(),
                base.clone().setToolInputJson("cut" + marker).build())) {
            val before = a.toByteArray()
            assertTrue(approvalTruncated(a))
            assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.approvalTruncated()), messageTextForTest(approvalTruncatedMessage))
            assertArrayEquals(before, a.toByteArray())
        }
    }

}

private fun uiTextForTest(text: io.github.lewin671.remoteagent.core.MessageText) = text.testText()
