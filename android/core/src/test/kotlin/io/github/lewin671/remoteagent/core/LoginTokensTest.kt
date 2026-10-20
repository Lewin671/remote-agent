package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.LoginResponse
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.delay
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import kotlin.time.Duration.Companion.seconds
import org.junit.Test

internal class MemoryTokens : LoginTokenStore {
    var saved: LoginToken? = null
    var reads = 0
    var clears = 0
    override fun loadLoginToken(account: Account): LoginToken? { reads++; return saved }
    override fun saveLoginToken(account: Account, token: LoginToken) { saved = token }
    override fun clearLoginToken(account: Account) { clears++; saved = null }
}

class LoginTokensTest {
    private val account = Account("https://example.com", ByteArray(32), ByteArray(32), "phone")

    @Test fun coldStartsAndConcurrentRpcsReuseOneCredentialUntilItsRefreshBoundary() = runTest(timeout = 5.seconds) {
        val store = MemoryTokens()
        var now = 100L
        var calls = 0
        fun tokens() = LoginTokens(account, store, { now }) {
            delay(10)
            calls++
            LoginResponse.newBuilder().setToken("credential-$calls").setExpiresMs(now + LOGIN_REFRESH_MARGIN_MS + 100).build()
        }
        val first = tokens()
        assertEquals(List(10) { "credential-1" }, List(10) { async { first.current() } }.awaitAll())
        assertEquals(1, calls)
        val cold = tokens()
        assertEquals("credential-1", cold.current())
        assertEquals(1, calls)
        now = 201
        assertEquals("credential-2", cold.current())
        assertEquals(2, calls)
        assertEquals(1, store.clears)
        assertEquals(2, store.reads)
    }

    @Test fun storedAndInMemoryTokensHonorTheExactOneDayMargin() = runTest(timeout = 5.seconds) {
        for (remaining in listOf(86_400_000L - 1, 86_400_000L, 86_400_000L + 1)) {
            val store = MemoryTokens().apply { saved = LoginToken("stored", 100 + remaining) }
            var now = 100L
            var calls = 0
            val tokens = LoginTokens(account, store, { now }) {
                calls++
                LoginResponse.newBuilder().setToken("fresh").setExpiresMs(now + 3 * 86_400_000L).build()
            }
            assertEquals(if (remaining < 86_400_000L) "fresh" else "stored", tokens.current())
            assertEquals(if (remaining < 86_400_000L) 1 else 0, calls)
            if (remaining >= 86_400_000L) {
                now += remaining - 86_400_000L + 1
                assertEquals("fresh", tokens.current())
                assertEquals(1, calls)
            }
            assertEquals(1, store.clears)
        }
    }

    @Test fun refusalDiscardsPersistenceAndLateRefusalCannotDiscardANewerLogin() = runTest(timeout = 5.seconds) {
        val store = MemoryTokens().apply { saved = LoginToken("rejected", 100 + LOGIN_REFRESH_MARGIN_MS) }
        val tokens = LoginTokens(account, store, { 100 }) {
            LoginResponse.newBuilder().setToken("fresh").setExpiresMs(300 + LOGIN_REFRESH_MARGIN_MS).build()
        }
        assertEquals("rejected", tokens.current())
        assertTrue(tokens.discard("rejected"))
        assertNull(store.saved)
        assertEquals("fresh", tokens.current())
        assertFalse(tokens.discard("rejected"))
        assertEquals("fresh", store.saved!!.value)
        tokens.discard()
        assertNull(store.saved)
    }

    @Test fun expiredPersistenceIsDeletedBeforeANetworkFailureAndNoTokenIsLoggedByToString() = runTest(timeout = 5.seconds) {
        val store = MemoryTokens().apply { saved = LoginToken("secret", 100 + LOGIN_REFRESH_MARGIN_MS - 1) }
        val tokens = LoginTokens(account, store, { 100 }) { error("offline") }
        assertTrue(runCatching { tokens.current() }.isFailure)
        assertNull(store.saved)
        assertFalse(LoginToken("secret", 200).toString().contains("secret"))
    }
}
