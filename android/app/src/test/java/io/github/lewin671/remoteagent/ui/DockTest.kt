package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.Question
import io.github.lewin671.remoteagent.proto.QuestionAsked
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class DockTest {
    @After fun reset() {
        DockFolds.clear()
        QuestionPicks.clear()
    }

    private val approval =
        DockItem.Approval(
            ApprovalRequested.newBuilder().setApprovalId("A1").setCommand("rm -rf build\nls").build(),
        )

    @Test fun anItemOpensByItselfAndStaysFoldedOnceFolded() {
        assertTrue(DockFolds.isOpen("s1", approval.id))
        DockFolds.set("s1", approval.id, false)
        assertFalse(DockFolds.isOpen("s1", approval.id))
        // The next item, or the same ID in another session, opens again.
        assertTrue(DockFolds.isOpen("s1", "a:A2"))
        assertTrue(DockFolds.isOpen("s2", approval.id))
        DockFolds.set("s1", approval.id, true)
        assertTrue(DockFolds.isOpen("s1", approval.id))
    }

    @Test fun aNewPlanIsANewItem() {
        assertNotEquals(DockItem.Plan("1. a").id, DockItem.Plan("1. b").id)
        assertEquals(DockItem.Plan("1. a").id, DockItem.Plan("1. a").id)
    }

    @Test fun theOpenHeadCarriesTheMetaAndNotTheSummary() {
        // Folded: the type, the summary and how many more.
        assertEquals("请求执行" to "rm -rf build · 还有 1 项", dockHeadLine(approval, "Codex", false, 1, left = "剩 4 分钟"))
        // Open: one head line, the type, how many more and the time left; the command is below.
        assertEquals(
            "请求执行" to "还有 1 项 · 剩 4 分钟",
            dockHeadLine(approval, "Codex", true, 1, left = "剩 4 分钟", openLabel = "请求执行"),
        )
        assertEquals("请求执行" to "", dockHeadLine(approval, "Codex", true, 0))
        val popup = DockItem.Popup("Trust this folder?")
        assertEquals("终端里有弹窗" to "Codex 在等你", dockHeadLine(popup, "Codex", true, 0, meta = "Codex 在等你"))
    }

    @Test fun questionProgressFollowsThePicks() {
        val q =
            QuestionAsked.newBuilder()
                .setQuestionId("Q1")
                .addQuestions(Question.newBuilder().setText("一"))
                .addQuestions(Question.newBuilder().setText("二").setMultiSelect(true))
                .build()
        assertEquals("第 1 题，共 2 题", questionProgress(q))
        QuestionPicks.index["Q1"] = 1
        assertEquals("第 2 题，共 2 题 · 可多选", questionProgress(q))
        val one = QuestionAsked.newBuilder().setQuestionId("Q2").addQuestions(Question.newBuilder().setText("一")).build()
        assertEquals("", questionProgress(one))
    }
}
