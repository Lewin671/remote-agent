package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import okhttp3.ResponseBody.Companion.toResponseBody
import okio.Buffer
import org.junit.Assert.*
import org.junit.Test

class NewestHistoryTest {
    private fun message(n: Int) = Event.newBuilder()
        .setAgentMessage(AgentMessage.newBuilder().setText("Message $n"))

    private class Requests {
        val after = CopyOnWriteArrayList<Long>()
        val blocked = CountDownLatch(1)
        val release = CountDownLatch(1)
        @Volatile var holdPrefix = false
        @Volatile var missing = 0L
        val http = Api.defaultHttp().newBuilder().addInterceptor { chain ->
            if (!chain.request().url.encodedPath.endsWith("GetEvents")) return@addInterceptor chain.proceed(chain.request())
            val body = Buffer().also { chain.request().body!!.writeTo(it) }.readByteArray()
            val request = GetEventsRequest.parseFrom(body)
            after += request.afterSeq
            if (holdPrefix && request.afterSeq == 0L) {
                blocked.countDown()
                check(release.await(15, TimeUnit.SECONDS))
            }
            val res = chain.proceed(chain.request())
            if (missing == 0L || res.code != 200) return@addInterceptor res
            val type = res.body!!.contentType()
            val raw = res.body!!.bytes()
            val plain = if (res.header("Content-Encoding") == "gzip")
                java.util.zip.GZIPInputStream(raw.inputStream()).use { it.readBytes() } else raw
            val page = GetEventsResponse.parseFrom(plain)
            res.newBuilder().removeHeader("Content-Encoding").removeHeader("Content-Length").body(page.toBuilder().clearEvents()
                .addAllEvents(page.eventsList.filter { it.seq != missing }).build()
                .toByteArray().toResponseBody(type)).build()
        }.build()
    }

    @Test fun tailRendersBeforeBackfillAndLiveDuplicatesMergeWithCachedPages() = runBlocking {
        TestServer().use { server ->
            val http = Api.defaultHttp()
            FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken).use { daemon ->
                daemon.putMachine("test-machine")
                daemon.putSession("session", Session.newBuilder().setTitle("Long history").build())
                for (batch in 0..10) daemon.upload("session", *(1..100).map { message(batch * 100 + it) }.toTypedArray())
                daemon.connect()
                val account = approvedClient(daemon.phone, http)
                val requests = Requests().apply { holdPrefix = true }
                val cache = MemoryCache()
                val r = fixtureRemote(account, requests.http, cache)
                try {
                    r.start()
                    withTimeout(15000) { r.state.first { it.loaded } }
                    val flow = r.events("session")
                    val tail = withTimeout(15000) { flow.first { it.events.size == 500 } }
                    assertEquals((601L..1100L).toList(), tail.events.map { it.seq })
                    assertTrue(tail.historyPartial)
                    assertFalse(tail.loaded)
                    assertTrue(tail.stateEvents.isEmpty())
                    withTimeout(5000) { while (requests.blocked.count != 0L) delay(10) }
                    assertEquals(listOf(600L, 0L), requests.after.toList())
                    daemon.upload("session", message(1101))
                    val live = withTimeout(10000) { flow.first { it.events.lastOrNull()?.seq == 1101L } }
                    assertEquals((601L..1101L).toList(), live.events.map { it.seq })
                    assertFalse(live.loaded)
                    requests.holdPrefix = false
                    requests.release.countDown()
                    val full = withTimeout(15000) { flow.first { it.loaded && it.events.size == 1101 } }
                    assertEquals((1L..1101L).toList(), full.events.map { it.seq })
                    assertEquals(full.events, full.stateEvents)
                    assertEquals(full.events, full.verifiedEvents)
                    assertFalse(full.historyPartial)
                    assertEquals(1101, cache.events("session").size)
                    // Reconnect overlaps tail and live data without duplicating messages.
                    r.reconnectNow()
                    withTimeout(10000) { r.state.first { it.conn == ConnState.ONLINE } }
                    assertEquals(1101, flow.value.events.map { it.seq }.distinct().size)
                } finally {
                    requests.release.countDown()
                    r.close()
                }
                // A partial disk cache displays immediately even if the next network fetch fails.
                val partial = object : CipherCache by cache {
                    override suspend fun events(sessionId: String) = cache.events(sessionId).filter { it.seq <= 20 || it.seq >= 601 }
                }
                val offline = fixtureRemote(account, http.newBuilder().addInterceptor { throw java.io.IOException("offline") }.build(), partial)
                try {
                    offline.start()
                    withTimeout(10000) { offline.state.first { it.sessions.isNotEmpty() } }
                    val restored = withTimeout(10000) { offline.events("session").first { it.events.size == 521 } }
                    assertTrue(restored.historyPartial)
                    assertEquals((1L..20L).toList(), restored.stateEvents.map { it.seq })
                    assertEquals((601L..1101L).toList(), restored.events.drop(20).map { it.seq })
                } finally { offline.close() }
            }
        }
    }

    @Test fun gapsCannotCompleteHistoryAndRetryFillsThemWithoutDuplicates() = runBlocking {
        for (missing in listOf(650L, 1100L)) {
            TestServer().use { server ->
                val http = Api.defaultHttp()
                FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken).use { daemon ->
                    daemon.putMachine("test-machine")
                    daemon.putSession("session", Session.newBuilder().setTitle("Gap").build())
                    for (batch in 0..10) daemon.upload("session", *(1..100).map { message(batch * 100 + it) }.toTypedArray())
                    val requests = Requests()
                    requests.missing = missing
                    val r = fixtureRemote(approvedClient(daemon.phone, http), requests.http)
                    try {
                        r.start()
                        withTimeout(15000) { r.state.first { it.loaded } }
                        val flow = r.events("session")
                        val gap = try {
                        withTimeout(30000) { flow.first { it.fetchError != null } }
                    } catch (e: Exception) {
                        val snapshot = flow.value
                        throw AssertionError("missing=$missing configured=${requests.missing} " +
                            "loaded=${snapshot.loaded} events=${snapshot.events.size} " +
                            "prefix=${snapshot.stateEvents.size} error=${snapshot.error} " +
                            "after=${requests.after}", e)
                    }
                        assertFalse(gap.loaded)
                        assertTrue("fetch=${gap.fetchError} error=${gap.error} size=${gap.events.size}", gap.historyPartial)
                        assertEquals((missing - 1).toInt(), gap.stateEvents.size)
                        assertEquals(1099, gap.events.size)
                        requests.missing = 0
                        r.retryEvents("session")
                        val full = withTimeout(15000) { flow.first { it.loaded } }
                        assertEquals((1L..1100L).toList(), full.events.map { it.seq })
                        assertNull(full.fetchError)
                    } finally { r.close() }
                }
            }
        }
    }

    @Test fun partialTranscriptDoesNotSettleInputsAcrossAGap() {
        val event = Event.newBuilder().setSeq(601).setSessionId("session")
            .setInputState(InputState.newBuilder().setRequestId("request")
                .setStatus(InputStatus.INPUT_STATUS_SUBMITTED)).build()
        val partial = SessionEvents(events = listOf(event), stateEvents = emptyList(), historyPartial = true)
        assertTrue(currentState(partial).inputs.isEmpty())
        assertEquals(InputStatus.INPUT_STATUS_SUBMITTED,
            currentState(partial.copy(stateEvents = listOf(event))).inputs["request"])
    }

    @Test fun shortAndEmptySessionsNeedOnlyOneHistoryRequestEach() = runBlocking {
        TestServer().use { server ->
            val http = Api.defaultHttp()
            FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken).use { daemon ->
                daemon.putMachine("test-machine")
                for (sid in listOf("short", "empty")) daemon.putSession(sid, Session.newBuilder().setTitle(sid).build())
                daemon.upload("short", message(1), message(2))
                val requests = Requests()
                val r = fixtureRemote(approvedClient(daemon.phone, http), requests.http)
                try {
                    r.start()
                    withTimeout(15000) { r.state.first { it.loaded } }
                    assertEquals(2, withTimeout(10000) { r.events("short").first { it.loaded } }.events.size)
                    val empty = withTimeout(10000) { r.events("empty").first { it.loaded } }
                    assertTrue(empty.events.isEmpty())
                    assertFalse(empty.historyPartial)
                    assertEquals(listOf(0L, 0L), requests.after.toList())
                } finally { r.close() }
            }
        }
    }
}
