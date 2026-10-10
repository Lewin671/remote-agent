package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.NotifyApproval
import io.github.lewin671.remoteagent.push.Notifier
import org.junit.Assert.*
import org.junit.Test

class ApprovalDisplayTest {
    @Test fun revealsControlsAndPreservesOrdinaryLanguage() {
        val normal = "العربية 中文 👩‍💻\n\tcode"
        assertEquals(normal, approvalDisplay(normal))
        assertEquals("\\u061c\\u200e\\u200f\\u202e\\u2066\\u2069\\u000d\\u001b\\u009b",
            approvalDisplay("\u061c\u200e\u200f\u202e\u2066\u2069\r\u001b\u009b"))
    }

    @Test fun anApprovalIsHeadedByWhatItAsksForWhereverAllowIsOffered() {
        val command = ApprovalRequested.newBuilder().setCommand("go test ./...").setToolName("Bash").build()
        assertEquals("请求执行", approvalHead(command))
        assertEquals("go test ./...", approvalBody(command))
        // A tool that is not a shell command has no command: its name heads it and its input is what is shown.
        val write = ApprovalRequested.newBuilder().setToolName("Write").setToolInputJson("{\"file_path\":\"/p/foo/a.go\"}").build()
        assertEquals("请求权限 · Write", approvalHead(write))
        assertEquals("{\"file_path\":\"/p/foo/a.go\"}", approvalBody(write))
        assertEquals("请求权限", approvalHead(ApprovalRequested.getDefaultInstance()))
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
}
