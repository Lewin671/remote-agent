package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.core.PreparedCommand
import io.github.lewin671.remoteagent.proto.CommandResult
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.ForkSessionResponse
import io.github.lewin671.remoteagent.proto.Session
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ForksTest {
    private var n = 0
    private fun prepare() = PreparedCommand("m1", "s1", "req-${++n}", 0, Envelope.getDefaultInstance())
    private val forked = CommandResult.newBuilder().setForkSession(ForkSessionResponse.newBuilder().setSession(Session.newBuilder().setId("s-fork"))).build()
    private val sent = mutableListOf<Pair<String, Boolean>>()
    private fun lost(code: String): suspend (PreparedCommand, Boolean) -> CommandResult? = { p, retry ->
        sent += p.requestId to retry
        throw CommandException(code, "原因")
    }
    private val answered: suspend (PreparedCommand, Boolean) -> CommandResult? = { p, retry ->
        sent += p.requestId to retry
        forked
    }

    @Test
    fun aForkWhoseAnswerWasLostIsSentAgainAsItWas() = runBlocking {
        val f = Forks.Fork()
        assertNull(Forks.send(f, ::prepare, lost(CommandException.UNKNOWN)))
        // What the menu reads when it is opened again, and what the page reads when it is come back to.
        assertTrue(f.frozen && !f.expired && !f.running)
        Forks.dismissError(f)
        assertTrue("closing the menu does not forget a fork that may have happened", f.frozen && f.error.isNotEmpty())
        // The same request: the computer answers with the session it already made instead of forking again.
        assertEquals("s-fork", Forks.send(f, ::prepare, answered))
        assertEquals(listOf("req-1" to false, "req-1" to true), sent)
        assertFalse(f.frozen)
        assertEquals("", f.error)
        // The next fork is another one.
        assertEquals("s-fork", Forks.send(f, ::prepare, answered))
        assertEquals("req-2" to false, sent.last())
    }

    @Test
    fun aForkThatSurelyDidNotHappenIsANewRequestNextTime() = runBlocking {
        val f = Forks.Fork()
        assertNull(Forks.send(f, ::prepare, lost(CommandException.UNAVAILABLE)))
        assertFalse(f.frozen)
        assertEquals("原因", f.error)
        Forks.dismissError(f)
        assertEquals("", f.error)
        assertEquals("s-fork", Forks.send(f, ::prepare, answered))
        assertEquals(listOf("req-1" to false, "req-2" to false), sent)
    }

    @Test
    fun anUnknownForkThatExpiredIsNotSentAgainUntilItIsGivenUp() = runBlocking {
        val f = Forks.Fork()
        Forks.send(f, ::prepare, lost(CommandException.UNKNOWN))
        Forks.send(f, ::prepare, lost(CommandException.EXPIRED))
        assertTrue(f.frozen && f.expired)
        assertNull(Forks.send(f, ::prepare, answered))
        assertEquals("nothing more is sent", 2, sent.size)
        Forks.abandon(f)
        assertFalse(f.frozen || f.expired)
        assertEquals("s-fork", Forks.send(f, ::prepare, answered))
        assertEquals("req-2" to false, sent.last())
    }

    @Test
    fun anAnswerThatNamesNoSessionIsAFailure() = runBlocking {
        val f = Forks.Fork()
        assertNull(Forks.send(f, ::prepare) { _, _ -> CommandResult.getDefaultInstance() })
        assertEquals("意外的回复", f.error)
        assertFalse(f.frozen)
    }

    @Test
    fun theRowSaysHowAForkStands() {
        assertEquals(ForkAction(noteError = true), forkAction(null, running = false, error = "", frozen = false, expired = false))
        assertEquals(ForkAction(text = "正在分叉…", busy = true), forkAction(null, running = true, error = "x", frozen = true, expired = false))
        assertEquals(ForkAction(note = "这一轮结束后可用", enabled = false), forkAction("这一轮结束后可用", running = false, error = "", frozen = true, expired = false))
        assertEquals(ForkAction(note = "没能分叉：电脑不在线", noteError = true), forkAction(null, running = false, error = "电脑不在线", frozen = false, expired = false))
        // It may have happened: the row never promises a new fork, only the same request again.
        val unsure = forkAction(null, running = false, error = "x", frozen = true, expired = false)
        assertEquals(ForkAction(note = "上次的分叉结果不确定。再点会重发同一个请求，电脑收到过就不会再分叉一次", noteError = true), unsure)
        // Expired: it cannot be asked again, so the tap gives it up and nothing is sent.
        val gone = forkAction(null, running = false, error = "x", frozen = true, expired = true)
        assertTrue(gone.abandons && gone.enabled)
        assertEquals("放弃上次的分叉", gone.text)
    }
}
