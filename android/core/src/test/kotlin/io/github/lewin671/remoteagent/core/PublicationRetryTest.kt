package io.github.lewin671.remoteagent.core

import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

class PublicationRetryTest {
    @Test
    fun exhaustedCASRemainsPendingAndCanFinishOnTheNextPoll() =
        runBlocking<Unit> {
            var calls = 0
            val failure = runCatching { retryRecoveryPublication { ++calls > 5 } }.exceptionOrNull()
            assertTrue(
                "exhausted publication was acknowledged",
                failure is CommandException && failure.code == "aborted",
            )
            assertEquals(5, calls)
            retryRecoveryPublication { ++calls > 5 }
            assertEquals(6, calls)
        }
}
