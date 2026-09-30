package io.github.lewin671.remoteagent.core
import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test
class EventProjectionTest {
    private fun normalized(v: Any?): Any? = when (v) {
        is RunningTool -> listOf(v.title, v.callId, v.since, v.kind, v.itemType, v.seq, v.input, v.inputDigest)
        is BackgroundTool -> listOf(normalized(v.tool), v.returned)
        is Map<*, *> -> v.mapValues { normalized(it.value) }
        is Set<*> -> v.toSet()
        is List<*> -> v.map { normalized(it) }
        else -> v
    }
    private fun fields(d: Derived) = Derived::class.java.declaredMethods.filter { it.name.startsWith("get") && it.parameterCount == 0 }.sortedBy { it.name }.map { normalized(it.invoke(d)) }
    private fun ev(seq: Int, b: Event.Builder) = b.setSeq(seq.toLong()).setSessionId("s").build()
    @Test fun incrementalResultsMatchReplayAcrossLateResultsAndCliChanges() {
        val history = listOf(
            ev(1, Event.newBuilder().setTurnStarted(TurnStarted.getDefaultInstance())),
            ev(2, Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("i").setStatus(InputStatus.INPUT_STATUS_QUEUED).setText("hello"))),
            ev(3, Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("hello"))),
            ev(4, Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("i").setStatus(InputStatus.INPUT_STATUS_SUBMITTED))),
            ev(5, Event.newBuilder().setToolStarted(ToolStarted.newBuilder().setCallId("c").setTitle("sleep"))),
            ev(6, Event.newBuilder().setTurnEnded(TurnEnded.newBuilder().setOutcome(TurnOutcome.TURN_OUTCOME_ABORTED))),
            ev(7, Event.newBuilder().setToolCall(ToolCall.newBuilder().setCallId("c").setStatus("completed"))),
            ev(8, Event.newBuilder().setApprovalRequested(ApprovalRequested.newBuilder().setApprovalId("a"))),
            ev(9, Event.newBuilder().setApprovalResolved(ApprovalResolved.newBuilder().setApprovalId("a").setDecision(ApprovalDecision.APPROVAL_DECISION_ALLOW))),
            ev(10, Event.newBuilder().setSessionState(SessionState.newBuilder().setStatus(SessionStatus.SESSION_STATUS_EXITED))),
        )
        val p = EventProjection()
        for (agent in listOf(Agent.AGENT_CODEX, Agent.AGENT_CLAUDE)) {
            for (n in 0..history.size) {
                val prefix = history.take(n)
                val result = p.update(prefix, agent)
                assertEquals(fields(derive(prefix, agent)), fields(result))
                assertSame(result, p.update(prefix, agent))
            }
        }
        assertEquals(fields(derive(history.take(3))), fields(p.update(history.take(3))))
        val replaced = history.toMutableList().apply { this[2] = this[2].toBuilder().setUserMessage(UserMessage.newBuilder().setText("changed")).build() }
        assertEquals(fields(derive(replaced)), fields(p.update(replaced)))
    }
    @Test fun publishedCollectionsStayUnchangedAfterMoreEvents() {
        val p = EventProjection()
        val first = ev(1, Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("local")))
        val old = p.update(listOf(first), Agent.AGENT_CLAUDE)
        val oldFields = fields(old)
        val next = p.update(listOf(first, ev(2, Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("i").setStatus(InputStatus.INPUT_STATUS_SUBMITTED)))), Agent.AGENT_CLAUDE)
        assertTrue(old.typedLocally.contains(1L)); assertFalse(next.typedLocally.contains(1L))
        assertEquals(oldFields, fields(old))
    }
}
