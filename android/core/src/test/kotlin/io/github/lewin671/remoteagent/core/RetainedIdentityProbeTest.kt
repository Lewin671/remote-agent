package io.github.lewin671.remoteagent.core

import com.connectrpc.Code
import com.connectrpc.ConnectException
import java.io.IOException
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.delay
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test

class RetainedIdentityProbeTest {
    @Test fun retriesOneDenialWithAFreshLogin() = runTest {
        for (deniedAgain in listOf(false, true)) {
            var attempts = 0
            val denied = probeRetainedIdentity {
                attempts++
                if (attempts == 1 || deniedAgain) throw ConnectException(Code.PERMISSION_DENIED, "login failed")
            }
            assertEquals(deniedAgain, denied)
            assertEquals(2, attempts)
        }
    }

    @Test fun offlineOnTheRetryIsNotDenial() = runTest {
        var attempts = 0
        assertFalse(probeRetainedIdentity {
            attempts++
            if (attempts == 1) throw ConnectException(Code.PERMISSION_DENIED, "login failed")
            throw IOException("offline")
        })
        assertEquals(2, attempts)
    }

    @Test fun fiveSecondsIsTheTotalBudgetIncludingTheRetry() = runTest {
        var attempts = 0
        var cancelled = false
        val start = testScheduler.currentTime
        assertFalse(probeRetainedIdentity {
            attempts++
            if (attempts == 1) {
                delay(3_000)
                throw ConnectException(Code.PERMISSION_DENIED, "login failed")
            }
            try { awaitCancellation() } finally { cancelled = true }
        })
        assertEquals(5_000L, testScheduler.currentTime - start)
        assertEquals(2, attempts)
        assertTrue(cancelled)
    }

    @Test fun timeoutOnTheFirstLoginDoesNotRetryOrDeclareDenial() = runTest {
        var attempts = 0
        assertFalse(probeRetainedIdentity { attempts++; awaitCancellation() })
        assertEquals(5_000L, testScheduler.currentTime)
        assertEquals(1, attempts)
    }

    @Test fun theDeadlineCancelsAnActualConnectRequest() = runTest {
        val http = Api.defaultHttp()
        var requests = 0
        var cancelled = false
        val transport = object : com.connectrpc.http.HTTPClientInterface by com.connectrpc.okhttp.ConnectOkHttpClient(http) {
            override fun unary(
                request: com.connectrpc.http.UnaryHTTPRequest,
                onResult: (com.connectrpc.http.HTTPResponse) -> Unit,
            ): () -> Unit {
                requests++
                // Hold the challenge response indefinitely, like a silent relay.
                return { cancelled = true }
            }
        }
        val account = Account("https://relay.example.com", ByteArray(0), ByteArray(32), "synthetic-device")
        val api = Api(account.server, http, transport = transport)
        assertFalse(probeRetainedIdentity { login(api, account) })
        assertEquals(5_000L, testScheduler.currentTime)
        assertEquals(1, requests)
        assertTrue(cancelled)
    }

    @Test fun callerCancellationStillCancelsTheProbe() = runTest {
        val probe = async { probeRetainedIdentity { awaitCancellation() } }
        testScheduler.runCurrent()
        probe.cancel()
        probe.join()
        assertTrue(probe.isCancelled)
    }
}
