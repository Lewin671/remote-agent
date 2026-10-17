package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test

class AsyncQuestionTest {
    @Test fun asyncQuestionsStayInChatWithAllContentAndNoAnswerCard() {
        val questions = listOf(
            Question.newBuilder().setHeader("测试方式").setText("请选择 **测试方式**。")
                .addOptions(QuestionOption.newBuilder().setLabel("A [推荐]").setDescription("完整测试 *保留原文*"))
                .addOptions(QuestionOption.newBuilder().setLabel("B")).build(),
            Question.newBuilder().setText("还有什么要求？").build(),
        )
        val events = listOf(
            Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText("开始检查")),
            Event.newBuilder().setQuestionAsked(QuestionAsked.newBuilder().setQuestionId("async").setAsync(true).addAllQuestions(questions)),
            // Ordinary messages after the question must neither hide it nor turn it into a pending card.
            Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("选择 A")),
            Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText("收到")),
            // Synchronous questions still belong in the existing answer dock, not ordinary chat.
            Event.newBuilder().setQuestionAsked(QuestionAsked.newBuilder().setQuestionId("sync").addAllQuestions(questions)),
        ).mapIndexed { i, b -> b.setSeq(i + 1L).build() }
        val p = ChatProjection()
        for (n in 1..events.size) {
            val prefix = events.take(n)
            val d = derive(prefix)
            assertFalse(d.questions.containsKey("async"))
            val rows = buildChat(prefix, d, "M")
            val expected = if (n < 2) emptyList() else listOf(ChatItem.AsyncQuestion("e2", questions))
            assertEquals(expected, rows.filterIsInstance<ChatItem.AsyncQuestion>())
            assertEquals(rows, p.update(prefix, d, "M"))
            assertEquals(prefix.filter { it.hasAgentMessage() || it.hasUserMessage() || it.hasQuestionAsked() && it.questionAsked.async }.map { "e${it.seq}" }, rows.map { it.key })
        }
        assertEquals(setOf("sync"), derive(events).questions.keys)
    }
}
