package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.core.PreparedCommand
import io.github.lewin671.remoteagent.proto.Envelope
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class AnswersTest {
    private var n = 0
    private fun prepare(options: List<Int>) = PreparedCommand("m1", "s1", "req-${++n}-$options", 0, Envelope.getDefaultInstance())

    @Test
    fun anAnswerWithAnUnknownOutcomeIsSentAgainUnchanged() = runBlocking {
        val a = Answers.of("q1", 0)
        val sent = mutableListOf<Pair<String, Boolean>>()
        val lost = runCatching {
            Answers.send(a, listOf(1), ::prepare) { p, retry ->
                sent += p.requestId to retry
                throw CommandException(CommandException.UNKNOWN, "no answer")
            }
        }
        assertTrue(lost.isFailure)
        assertTrue(a.attempt.frozen)
        // Tapping another option resends the first answer, which the daemon finds in its log.
        val answered = Answers.send(a, listOf(2), ::prepare) { p, retry -> sent += p.requestId to retry }
        assertEquals(listOf("req-1-[1]" to false, "req-1-[1]" to true), sent)
        assertEquals(listOf(1), answered)
        assertFalse(a.attempt.frozen)
        // Each question index has its own answer.
        assertEquals(listOf(0), Answers.send(Answers.of("q1", 1), listOf(0), ::prepare) { _, _ -> })
    }
}
