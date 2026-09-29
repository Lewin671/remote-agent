package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Envelope
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotSame
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test

class AttemptTest {
    private var n = 0
    private fun make() = PreparedCommand("m", "", "req-${++n}", 1000, Envelope.getDefaultInstance())

    @Test
    fun anUnknownOutcomeIsRetriedAsItWas() {
        val a = Attempt()
        val first = a.next(::make)
        a.settle(CommandException(CommandException.UNKNOWN, "timeout"))
        assertTrue(a.frozen)
        assertSame(first, a.next(::make))
        a.settle()
        assertFalse(a.frozen)
        assertNotSame(first, a.next(::make))
    }

    @Test
    fun aDefiniteFailureFreesTheNextAttempt() {
        val a = Attempt()
        val first = a.next(::make)
        a.settle(CommandException(CommandException.UNAVAILABLE, "offline"))
        assertNotSame(first, a.next(::make))
    }

    @Test
    fun anExpiredUnknownStaysUnknown() {
        val a = Attempt()
        a.next(::make)
        a.settle(CommandException(CommandException.UNKNOWN, "timeout"))
        a.next(::make)
        val e = a.settle(CommandException(CommandException.EXPIRED, "这条指令已过期，没有发出")) as CommandException
        assertEquals(CommandException.UNKNOWN, e.code)
        assertFalse(a.frozen)
    }
}
