package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.core.PreparedCommand
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.PlanChoice
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

class PlansTest {
    private val lost = CommandException(CommandException.UNKNOWN, "lost response")

    @Test fun aLostReplyKeepsItsChoiceEnvelopeAndDeadlineAcrossPages() = runBlocking {
        Plans.clear()
        val reply = Plans.of("s")
        val first = PreparedCommand("m", "s", "r", 1234, Envelope.getDefaultInstance())
        var prepared = 0
        val make: (PlanChoice) -> PreparedCommand = { prepared++; first }
        assertTrue(runCatching {
            Plans.send(reply, PlanChoice.PLAN_CHOICE_KEEP_PLANNING, make) { _, retry ->
                assertFalse(retry)
                throw lost
            }
        }.isFailure)
        // Another page of the same session finds the decision that is still unknown, and sends it again.
        val again = Plans.of("s")
        assertTrue(again.frozen)
        assertEquals(PlanChoice.PLAN_CHOICE_KEEP_PLANNING, again.choice)
        Plans.retry(again) { p, retry ->
            assertSame(first, p)
            assertTrue(retry)
        }
        assertEquals(1, prepared)
        assertFalse(reply.frozen)
    }

    @Test fun anotherChoiceIsNeverSentInPlaceOfAnUnknownOne() = runBlocking {
        val reply = Plans.Reply()
        val make: (PlanChoice) -> PreparedCommand = { PreparedCommand("m", "s", "r", 1234, Envelope.getDefaultInstance()) }
        runCatching { Plans.send(reply, PlanChoice.PLAN_CHOICE_IMPLEMENT, make) { _, _ -> throw lost } }
        var sent = 0
        val refused = runCatching { Plans.send(reply, PlanChoice.PLAN_CHOICE_KEEP_PLANNING, make) { _, _ -> sent++ } }
        assertTrue(refused.isFailure)
        assertEquals("the approval must not go out under the other button", 0, sent)
        // Still the same unknown decision, to retry or to give up.
        assertTrue(reply.frozen)
        assertEquals(PlanChoice.PLAN_CHOICE_IMPLEMENT, reply.choice)
    }

    @Test fun anUnknownReplyOffersOnlyItsOwnRetryAndGivingUp() = runBlocking {
        val reply = Plans.Reply()
        val make: (PlanChoice) -> PreparedCommand = { PreparedCommand("m", "s", "r", 1234, Envelope.getDefaultInstance()) }
        assertEquals(PlanOffer(), planOffer(reply))
        runCatching { Plans.send(reply, PlanChoice.PLAN_CHOICE_IMPLEMENT, make) { _, _ -> throw lost } }
        val offer = planOffer(reply)
        assertFalse(offer.choices)
        assertEquals("重试「批准执行」", offer.retry)
        assertTrue(offer.abandon)
        assertTrue(offer.note.contains("批准执行"))

        // Given up: every choice is offered again, and the next one is a new request.
        Plans.abandon(reply)
        assertEquals(PlanOffer(), planOffer(reply))
        var prepared: PlanChoice? = null
        Plans.send(reply, PlanChoice.PLAN_CHOICE_KEEP_PLANNING, { prepared = it; make(it) }) { _, retry -> assertFalse(retry) }
        assertEquals(PlanChoice.PLAN_CHOICE_KEEP_PLANNING, prepared)
    }

    @Test fun aDefiniteFailureOffersEveryChoiceAgain() = runBlocking {
        val reply = Plans.Reply()
        val make: (PlanChoice) -> PreparedCommand = { PreparedCommand("m", "s", "r", 1234, Envelope.getDefaultInstance()) }
        runCatching { Plans.send(reply, PlanChoice.PLAN_CHOICE_IMPLEMENT, make) { _, _ -> throw CommandException("failed_precondition", "no", fromDaemon = true) } }
        assertEquals(PlanOffer(), planOffer(reply))
    }

    @Test fun anExpiredUnknownReplyCannotApproveTheNextPlan() = runBlocking {
        val reply = Plans.Reply()
        val make: (PlanChoice) -> PreparedCommand = { PreparedCommand("m", "s", "r", 1, Envelope.getDefaultInstance()) }
        runCatching { Plans.send(reply, PlanChoice.PLAN_CHOICE_IMPLEMENT, make) { _, _ -> throw lost } }
        runCatching { Plans.retry(reply) { _, _ -> throw CommandException("deadline_exceeded", "expired", fromDaemon = true) } }
        assertTrue(reply.expired)
        assertTrue(runCatching {
            Plans.send(reply, PlanChoice.PLAN_CHOICE_KEEP_PLANNING, { error("new approval") }) { _, _ -> error("sent") }
        }.exceptionOrNull() is CommandException)
        assertTrue(runCatching { Plans.retry(reply) { _, _ -> error("sent") } }.exceptionOrNull() is CommandException)
        // Expired: nothing to send again, only to give up.
        val offer = planOffer(reply)
        assertFalse(offer.choices)
        assertNull(offer.retry)
        assertTrue(offer.abandon)
    }
}
