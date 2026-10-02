package io.github.lewin671.remoteagent.push

import java.io.IOException
import java.util.concurrent.ExecutionException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.currentTime
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class RegistrationTest {
    @Test fun wrappedGoogleFailureHasAnActionableMessageAndRetries() {
        val failure = tokenFailure(IOException(ExecutionException(IOException("SERVICE_NOT_AVAILABLE"))))
        assertTrue(failure.retry)
        assertTrue(failure.status.problem.contains("Google"))
        assertFalse(failure.status.problem.contains("java."))
        assertEquals("重试", failure.status.action)
        assertFalse(tokenFailure(ExecutionException(IOException("AUTHENTICATION_FAILED"))).retry)
        assertFalse(tokenFailure(IOException("MISSING_INSTANCEID_SERVICE")).retry)
    }

    @Test fun retriesBackOffAndClearTheBannerAfterSuccess() = runTest {
        val times = mutableListOf<Long>()
        val statuses = mutableListOf<PushStatus>()
        val registration = PushRegistration(backgroundScope)
        registration.request({
            times += currentTime
            if (times.size < 7) tokenFailure(IOException("SERVICE_NOT_AVAILABLE")) else PushAttempt(PushStatus())
        }, statuses::add)
        runCurrent()
        for (delay in listOf(30_000L, 60_000L, 120_000L, 240_000L, 300_000L, 300_000L)) {
            advanceTimeBy(delay)
            runCurrent()
        }
        assertEquals(listOf(0L, 30_000L, 90_000L, 210_000L, 450_000L, 750_000L, 1_050_000L), times)
        assertEquals(PushStatus(), statuses.last())
        advanceTimeBy(600_000)
        runCurrent()
        assertEquals(7, times.size)
    }

    @Test fun newerSettingsReplacePendingRetriesAndStopCancelsInFlightWork() = runTest {
        val registration = PushRegistration(backgroundScope)
        var oldAttempts = 0
        val statuses = mutableListOf<PushStatus>()
        registration.request({ oldAttempts++; tokenFailure(IOException("SERVICE_NOT_AVAILABLE")) }, statuses::add)
        runCurrent()
        registration.request({ PushAttempt(PushStatus("服务器没有配置 FCM")) }, statuses::add)
        runCurrent()
        advanceTimeBy(600_000)
        runCurrent()
        assertEquals(1, oldAttempts)
        assertEquals(2, statuses.size)

        val cancelled = CompletableDeferred<Unit>()
        registration.request({
            try { awaitCancellation() } finally { cancelled.complete(Unit) }
        }, statuses::add)
        runCurrent()
        registration.stop()
        runCurrent()
        assertTrue(cancelled.isCompleted)
        assertEquals(2, statuses.size)
    }

    @Test fun backgroundTokenRegistrationDoesNotKeepRetrying() = runTest {
        var attempts = 0
        val registration = PushRegistration(backgroundScope)
        registration.request({ attempts++; tokenFailure(IOException("SERVICE_NOT_AVAILABLE")) }, {}, autoRetry = false)
        runCurrent()
        advanceTimeBy(600_000)
        runCurrent()
        assertEquals(1, attempts)
    }
}
