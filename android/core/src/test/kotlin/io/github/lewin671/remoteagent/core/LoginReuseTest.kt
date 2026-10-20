package io.github.lewin671.remoteagent.core

import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.BeforeClass
import kotlin.time.Duration.Companion.seconds
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class LoginReuseTest {
    companion object {
        private lateinit var fixture: ColdOpenFixture
        @JvmStatic @BeforeClass fun signOnce() { fixture = ColdOpenFixture(historySize = 0) }
    }

    @Test fun coldStartSkipsLoginAndRpcRejectionDiscardsAndReconnects() = runTest(timeout = 5.seconds) {
        val peer = ColdOpenPeer(fixture, this)
        val store = MemoryTokens()
        suspend fun online(r: Remote) = withTimeout(15000) { r.state.first { it.loaded && it.conn == ConnState.ONLINE } }
        val first = peer.remote(tokens = store)
        try { first.start(); online(first) } finally { first.close(); runCurrent() }
        assertEquals(2, peer.calls.count { it == "Login" || it == "GetChallenge" })
        val original = store.saved!!.value
        val cold = peer.remote(tokens = store)
        try {
            cold.start()
            online(cold)
            assertEquals(2, peer.calls.count { it == "Login" || it == "GetChallenge" })
            peer.rejectNextHistory = true
            withTimeout(15000) { cold.events("short").first { it.loaded && it.events.size == 2 } }
            assertEquals(4, peer.calls.count { it == "Login" || it == "GetChallenge" })
            assertNotEquals(original, store.saved!!.value)
            assertEquals(0, peer.anonymous)
        } finally { cold.close(); runCurrent() }
        store.saved = LoginToken("unknown-credential", fixture.now + 2 * LOGIN_REFRESH_MARGIN_MS)
        val refused = peer.remote(tokens = store)
        try {
            refused.start()
            online(refused) // The real Remote's reconnect backoff runs on virtual time.
            assertEquals(1, peer.refused)
            assertEquals(6, peer.calls.count { it == "Login" || it == "GetChallenge" })
            assertNotEquals("unknown-credential", store.saved!!.value)
        } finally { refused.close(); runCurrent() }
    }

    @Test fun openingCachedSessionDuringLoginWaitsInsteadOfSendingAnAnonymousRpc() = runTest(timeout = 5.seconds) {
        val peer = ColdOpenPeer(fixture, this)
        val cache = MemoryCache()
        val warm = peer.remote(cache)
        try {
            warm.start()
            withTimeout(15000) { warm.state.first { it.loaded } }
        } finally { warm.close(); runCurrent() }
        peer.calls.clear()
        val reached = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        peer.before = { name, _ ->
            if (name == "GetChallenge") { reached.complete(Unit); release.await() }
        }
        val r = peer.remote(cache, MemoryTokens())
        try {
            r.start()
            reached.await()
            assertTrue(r.state.value.sessions.containsKey("short"))
            val events = r.events("short")
            runCurrent() // Execute every runnable action while login is held, without sleeping.
            assertFalse(peer.calls.contains("GetEvents"))
            release.complete(Unit)
            withTimeout(15000) { events.first { it.loaded && it.events.size == 2 } }
            assertEquals(0, peer.anonymous)
            assertTrue(peer.calls.contains("GetEvents"))
        } finally { r.close(); runCurrent() }
    }
}
