package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import java.util.concurrent.CopyOnWriteArrayList
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.*
import org.junit.Test

/** Local relay and synthetic daemon; each HTTP exchange/WS upgrade has 50 ms injected latency. */
class OpenLatencyBenchmarkTest {
    @Test fun newestVisibleAfterColdEngineStart() = runBlocking {
        TestServer().use { server ->
            val http = Api.defaultHttp()
            FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken).use { daemon ->
                daemon.putMachine("test-machine")
                daemon.putSession("session", Session.newBuilder().setTitle("Latency test").build())
                for (batch in 0..10) daemon.upload("session", *(1..100).map {
                    Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText("Message ${batch * 100 + it}"))
                }.toTypedArray())
                val account = approvedClient(daemon.phone, http)
                val login = loginResponse(Api(server.url, http), account)
                repeat(3) { sample ->
                    val calls = CopyOnWriteArrayList<String>()
                    val delayed = http.newBuilder().addInterceptor { chain ->
                        calls += chain.request().url.encodedPath.substringAfterLast('/')
                        Thread.sleep(50)
                        chain.proceed(chain.request())
                    }.build()
                    val store = MemoryTokens().apply { saved = LoginToken(login.token, login.expiresMs) }
                    val r = fixtureRemote(account, delayed, tokenStorage = store)
                    try {
                        val start = System.nanoTime()
                        val events = r.events("session")
                        r.start()
                        val visible = withTimeout(20000) { events.first { it.events.lastOrNull()?.seq == 1100L } }
                        val ms = (System.nanoTime() - start) / 1_000_000
                        val loginCalls = calls.count { it == "Login" || it == "GetChallenge" }
                        val pages = calls.count { it == "GetEvents" }
                        println("OpenLatency sample=$sample newestVisibleMs=$ms loginCalls=$loginCalls historyCalls=$pages visible=${visible.events.size} calls=$calls")
                        assertEquals(0, loginCalls)
                        assertTrue(pages >= 1)
                        assertEquals(500, visible.events.size)
                    } finally { r.close() }
                }
            }
        }
    }
}
