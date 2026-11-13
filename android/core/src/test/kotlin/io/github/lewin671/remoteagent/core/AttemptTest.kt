package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Envelope
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotSame
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test

class AttemptTest {
    @Test fun interruptedResultsRemainUnknown() {
        for (code in listOf("canceled", "deadline_exceeded", "aborted")) {
            val a = Attempt()
            val first = a.next(::make)
            val e = CommandException(code, "interrupted", fromDaemon = true)
            assertTrue(code, uncertain(e))
            a.settle(e)
            assertTrue(a.frozen)
            assertSame(first, a.next(::make))
            assertFalse(uncertain(CommandException(code, "local refusal")))
        }
    }
    private var n = 0
    private fun make() = PreparedCommand("m", "", "req-${++n}", 1000, Envelope.getDefaultInstance())

    @Test
    fun anUnknownOutcomeIsRetriedAsItWas() {
        val a = Attempt()
        val first = a.next(::make)
        a.settle(CommandException(CommandException.UNKNOWN, "timeout", outcome = Outcome.UNKNOWN))
        assertTrue(a.frozen)
        assertSame(first, a.next(::make))
        a.settle()
        assertFalse(a.frozen)
        assertNotSame(first, a.next(::make))
    }

    @Test
    fun aDaemonThatStoppedHalfwayLeavesTheOutcomeUnknown() {
        val a = Attempt()
        val first = a.next(::make)
        // What the daemon answers for a request it began and has no result for (errUnknownOutcome in internal/relay).
        val stopped = CommandException("aborted", "the daemon stopped while running this command", fromDaemon = true)
        assertTrue(uncertain(stopped))
        a.settle(stopped)
        assertTrue(a.frozen)
        assertSame(first, a.next(::make))
        // The same answer to the retry is still no answer.
        a.settle(CommandException(CommandException.UNKNOWN, "timeout", outcome = Outcome.UNKNOWN))
        a.settle(stopped)
        assertTrue(a.frozen)
        assertSame(first, a.next(::make))
        // The code alone is not the daemon's word: the app gives up on a device's name with it.
        assertFalse(uncertain(CommandException("aborted", "设备名称一直在被其他客户端修改，没能保存")))
    }

    @Test
    fun aDefiniteFailureFreesTheNextAttempt() {
        val a = Attempt()
        val first = a.next(::make)
        a.settle(CommandException(CommandException.UNAVAILABLE, "offline"))
        assertNotSame(first, a.next(::make))
    }

    @Test
    fun anExpiredUnknownStaysUnknownAndIsNotRenewed() {
        val a = Attempt()
        val first = a.next(::make)
        a.settle(CommandException(CommandException.UNKNOWN, "timeout", outcome = Outcome.UNKNOWN))
        assertSame(first, a.next(::make))
        // The retry comes back expired: still unknown, and never sealed again.
        val e = a.settle(CommandException(CommandException.EXPIRED, "the command expired before it was sent", outcome=Outcome.NOT_SENT)) as CommandException
        assertEquals(CommandException.UNKNOWN, e.code)
        assertTrue(a.frozen && a.expired)
        repeat(2) {
            try {
                a.next(::make)
                org.junit.Assert.fail("an expired unknown command was sent again")
            } catch (x: CommandException) {
                assertTrue(x.unknown)
            }
        }
        assertEquals(1, n)
        // Only giving up frees it.
        a.abandon()
        assertNotSame(first, a.next(::make))
    }

    @Test
    fun anExpiryWithoutAnUnknownOutcomeIsDefinite() {
        val a = Attempt()
        val first = a.next(::make)
        a.settle(CommandException(CommandException.EXPIRED, "这条指令已过期，没有发出"))
        assertFalse(a.frozen)
        assertNotSame(first, a.next(::make))
    }

    @Test
    fun anyLaterFailureKeepsAnUnknownOutcome() {
        val a = Attempt()
        val first = a.next(::make)
        a.settle(CommandException(CommandException.UNKNOWN, "timeout", outcome = Outcome.UNKNOWN))
        // A retry that fails to log in, or on the network, says nothing about the first try.
        for (e in listOf(RuntimeException("login failed"), CommandException("network", "网络不通"), CommandException(CommandException.UNAVAILABLE, "电脑不在线"))) {
            val shown = a.settle(e) as CommandException
            assertTrue(shown.unknown)
            assertTrue(a.frozen)
            assertSame(first, a.next(::make))
        }
        // The daemon's own answer for the request does settle it.
        a.settle(CommandException("unimplemented", "no", fromDaemon = true,outcome=Outcome.REJECTED))
        assertFalse(a.frozen)
        assertNotSame(first, a.next(::make))
    }
}
