package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import java.util.concurrent.atomic.AtomicInteger
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.ResponseBody.Companion.toResponseBody
import org.junit.Assert.*
import org.junit.Test

class LoginReuseTest {
    @Test fun coldStartSkipsLoginAndRpcRejectionDiscardsAndReconnects() = runBlocking {
        TestServer().use { server ->
            val http = Api.defaultHttp()
            FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken).use { daemon ->
                daemon.putMachine("test-machine")
                daemon.putSession("session", Session.newBuilder().setTitle("Test").build())
                daemon.upload("session", Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText("Hello")))
                val account = approvedClient(daemon.phone, http)
                val store = MemoryTokens()
                val logins = AtomicInteger()
                val unauthorized = AtomicInteger()
                var reject = false
                val counted = http.newBuilder().addInterceptor { chain ->
                    val path = chain.request().url.encodedPath
                    if (path.endsWith("GetChallenge") || path.endsWith("/Login")) logins.incrementAndGet()
                    if (path.endsWith("GetEvents")) {
                        if (chain.request().header("Authorization").isNullOrEmpty()) unauthorized.incrementAndGet()
                        if (reject) {
                            reject = false
                            return@addInterceptor okhttp3.Response.Builder().request(chain.request())
                                .protocol(okhttp3.Protocol.HTTP_1_1).code(401).message("Unauthorized")
                                .body("{\"code\":\"unauthenticated\"}".toResponseBody("application/json".toMediaType())).build()
                        }
                    }
                    chain.proceed(chain.request())
                }.build()
                suspend fun online(r: Remote) = withTimeout(15000) { r.state.first { it.loaded && it.conn == ConnState.ONLINE } }
                val first = fixtureRemote(account, counted, tokenStorage = store)
                first.start()
                online(first)
                assertEquals(2, logins.get())
                val original = store.saved!!.value
                first.close()
                val cold = fixtureRemote(account, counted, tokenStorage = store)
                try {
                    cold.start()
                    online(cold)
                    assertEquals(2, logins.get())
                    reject = true
                    withTimeout(15000) { cold.events("session").first { it.loaded && it.events.size == 1 } }
                    assertEquals(4, logins.get())
                    assertNotEquals(original, store.saved!!.value)
                    assertEquals(0, unauthorized.get())
                } finally { cold.close() }
                // A syntactically valid but unknown bearer is explicitly rejected with WS 1008.
                store.saved = LoginToken("unknown-credential", System.currentTimeMillis() + 2 * 86_400_000L)
                val refused = fixtureRemote(account, counted, tokenStorage = store)
                try {
                    refused.start()
                    online(refused)
                    assertEquals(6, logins.get())
                    assertNotEquals("unknown-credential", store.saved!!.value)
                } finally { refused.close() }
            }
        }
    }
    @Test fun openingCachedSessionDuringLoginWaitsInsteadOfSendingAnAnonymousRpc() = runBlocking {
        TestServer().use { server ->
            val http = Api.defaultHttp()
            FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken).use { daemon ->
                daemon.putMachine("test-machine")
                daemon.putSession("session", Session.newBuilder().setTitle("Test").build())
                daemon.upload("session", Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText("Hello")))
                val account = approvedClient(daemon.phone, http)
                val cache = MemoryCache()
                val warm = fixtureRemote(account, http, cache)
                warm.start()
                withTimeout(15000) { warm.state.first { it.loaded } }
                warm.close()
                val release = java.util.concurrent.CountDownLatch(1)
                val historyCalls = AtomicInteger()
                val anonymous = AtomicInteger()
                val delayed = http.newBuilder().addInterceptor { chain ->
                    if (chain.request().url.encodedPath.endsWith("GetChallenge"))
                        check(release.await(15, java.util.concurrent.TimeUnit.SECONDS))
                    if (chain.request().url.encodedPath.endsWith("GetEvents")) {
                        historyCalls.incrementAndGet()
                        if (chain.request().header("Authorization").isNullOrEmpty()) anonymous.incrementAndGet()
                    }
                    chain.proceed(chain.request())
                }.build()
                val r = fixtureRemote(account, delayed, cache)
                try {
                    r.start()
                    withTimeout(10000) { r.state.first { it.sessions.containsKey("session") } }
                    val events = r.events("session")
                    kotlinx.coroutines.delay(100)
                    assertEquals(0, historyCalls.get())
                    release.countDown()
                    withTimeout(15000) { events.first { it.loaded && it.events.size == 1 } }
                    assertEquals(0, anonymous.get())
                    assertTrue(historyCalls.get() >= 1)
                } finally {
                    release.countDown()
                    r.close()
                }
            }
        }
    }

}
