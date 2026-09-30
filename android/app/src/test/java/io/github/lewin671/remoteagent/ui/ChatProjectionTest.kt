package io.github.lewin671.remoteagent.ui
import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test
class ChatProjectionTest {
    @Test fun appendingMessagesAndStateChangesMatchFullBuild() {
        val builders = listOf(
            Event.newBuilder().setToolCall(ToolCall.newBuilder().setTitle("a").setStatus("completed")),
            Event.newBuilder().setToolCall(ToolCall.newBuilder().setTitle("b").setStatus("completed")),
            Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText("   ")),
            Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("hello")),
            Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText("reply")),
            Event.newBuilder().setReasoning(Reasoning.newBuilder().setText("thought")),
            Event.newBuilder().setSettingsChanged(SettingsChanged.newBuilder().setCompacted(true)),
            Event.newBuilder().setTokenUsage(TokenUsage.newBuilder().setContextWindow(100).setLastTotalTokens(30)),
            Event.newBuilder().setTurnEnded(TurnEnded.newBuilder().setDurationMs(1000)),
        )
        val events = builders.mapIndexed { i, b -> b.setSeq(i + 1L).build() }
        val p = ChatProjection()
        for (n in 0..events.size) {
            val prefix = events.take(n); val d = derive(prefix)
            assertEquals(buildChat(prefix, d, "M"), p.update(prefix, d, "M"))
        }
        val shorter = events.take(2)
        assertEquals(buildChat(shorter, derive(shorter), "M"), p.update(shorter, derive(shorter), "M"))
    }
    @Test fun ordinaryAppendRetainsExistingRowObjects() {
        val first = Event.newBuilder().setSeq(1).setUserMessage(UserMessage.newBuilder().setText("hello")).build()
        val next = Event.newBuilder().setSeq(2).setAgentMessage(AgentMessage.newBuilder().setText("reply")).build()
        val p = ChatProjection(); val old = p.update(listOf(first), derive(listOf(first)), "M")
        val result = p.update(listOf(first, next), derive(listOf(first, next)), "M")
        assertSame(old[0], result[0]); assertEquals(1, old.size); assertEquals(2, result.size)
    }
}
