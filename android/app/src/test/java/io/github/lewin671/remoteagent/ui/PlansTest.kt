package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.core.PreparedCommand
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.PlanChoice
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

class PlansTest {
    @Test fun aLostReplyKeepsItsChoiceEnvelopeAndDeadlineAcrossPages() = runBlocking {
        Plans.clear()
        val reply = Plans.of("s")
        val first = PreparedCommand("m", "s", "r", 1234, Envelope.getDefaultInstance())
        var prepared = 0
        val make: (PlanChoice) -> PreparedCommand = { prepared++; first }
        assertTrue(runCatching {
            Plans.send(reply, PlanChoice.PLAN_CHOICE_KEEP_PLANNING, make) { _, retry ->
                assertFalse(retry)
                throw CommandException(CommandException.UNKNOWN, "lost response")
            }
        }.isFailure)
        // The full page can ask for another choice, but only the pending decision is resent.
        Plans.send(Plans.of("s"), PlanChoice.PLAN_CHOICE_IMPLEMENT, make) { p, retry ->
            assertSame(first, p)
            assertTrue(retry)
            assertEquals(PlanChoice.PLAN_CHOICE_KEEP_PLANNING, reply.choice)
        }
        assertEquals(1, prepared)
        assertFalse(reply.attempt.frozen)
    }

    @Test fun anExpiredUnknownReplyCannotApproveTheNextPlan() = runBlocking {
        val reply = Plans.Reply()
        val make: (PlanChoice) -> PreparedCommand = { PreparedCommand("m", "s", "r", 1, Envelope.getDefaultInstance()) }
        runCatching { Plans.send(reply, PlanChoice.PLAN_CHOICE_IMPLEMENT, make) { _, _ -> throw CommandException(CommandException.UNKNOWN, "lost") } }
        runCatching { Plans.send(reply, PlanChoice.PLAN_CHOICE_IMPLEMENT, make) { _, _ -> throw CommandException("deadline_exceeded", "expired", fromDaemon = true) } }
        assertTrue(reply.attempt.expired)
        assertTrue(runCatching {
            Plans.send(reply, PlanChoice.PLAN_CHOICE_KEEP_PLANNING, { error("new approval") }) { _, _ -> error("sent") }
        }.exceptionOrNull() is CommandException)
    }
}
