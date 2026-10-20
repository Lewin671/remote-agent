package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.AgentMessage
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.LiveSnapshot
import io.github.lewin671.remoteagent.proto.PlanProposed
import io.github.lewin671.remoteagent.proto.Session
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/** A pending plan reads as the plan event the live snapshot names, not as the terminal's box. */
class LivePlanTest {
    private val screen =
        "⎿ /plan to preview\n────────\nReady to code?\nHere is Claude's plan:\n────────\n计划：在 run-P 目录创建 5 个 beta 前缀文件\n"
    private val markdown = "# 计划：在 run-P 目录创建 5 个 beta 前缀文件\n\n## Context\n\n- 用户要求用前缀 beta"

    private fun history(text: String = markdown) =
        listOf(
            Event.newBuilder().setSessionId("s").setSeq(1)
                .setAgentMessage(AgentMessage.newBuilder().setText("先问一个问题")).build(),
            Event.newBuilder().setSessionId("s").setSeq(2)
                .setPlanProposed(PlanProposed.newBuilder().setText(text)).build(),
        )

    private fun live(seq: Long = 2, open: Boolean = true) =
        LiveSnapshot.newBuilder()
            .setSession(Session.newBuilder().setId("s").setPlanOpen(open))
            .setPlanEventSeq(seq)
            .setPlanScreenHash(ByteString.copyFrom(ByteArray(32) { 1 }))
            .setPlanScreen(screen)
            .build()

    @Test fun aPendingPlanReadsAsItsEvent() {
        assertEquals(markdown, livePlanText(live(), history()))
        assertEquals(markdown, currentActions(derive(history()), live(), history()).plan?.text)
        // A history that does not start at the first event still holds it.
        assertEquals(markdown, livePlanText(live(), history().drop(1)))
    }

    @Test fun theScreenStandsInOnlyWithoutThatEvent() {
        // Not here yet, another kind at that place, too large to carry, or empty.
        assertEquals(screen, livePlanText(live(3), history()))
        assertEquals(screen, livePlanText(live(1), history()))
        assertEquals(screen, livePlanText(live(), history("长计划$skeletonNote")))
        assertEquals(screen, livePlanText(live(), history("  ")))
        assertEquals(screen, currentActions(derive(history()), live()).plan?.text)
    }

    @Test fun backfillReplacesTheScreenWithoutChangingTheLiveSnapshotOrVisibleTail() {
        val live = live()
        val tail = listOf(Event.newBuilder().setSeq(9).setSessionId("s").build())
        for (legacy in listOf(false, true)) {
            val partial = SessionEvents(events = tail, stateEvents = emptyList(),
                verifiedEvents = emptyList(), unverifiedLegacy = legacy, live = live, historyPartial = true)
            assertEquals(screen, currentState(partial).plan?.text)
            val filled = partial.copy(stateEvents = history(), verifiedEvents = history())
            assertEquals(tail, filled.events)
            assertEquals(markdown, currentState(filled).plan?.text)
            // A legacy body at the right sequence is never used as verified text.
            if (legacy) assertEquals(screen, currentState(filled.copy(verifiedEvents = history().take(1))).plan?.text)
        }
    }

    @Test fun onlyTheSnapshotSaysAPlanIsPending() {
        assertNull(currentActions(derive(history()), live(open = false), history()).plan)
        assertNull(currentActions(derive(history()), null, history()).plan)
    }
}
