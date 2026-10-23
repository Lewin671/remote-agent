package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.BeforeClass
import kotlin.time.Duration.Companion.seconds
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class NewestHistoryTest {
    companion object {
        private lateinit var fixture: ColdOpenFixture
        @JvmStatic @BeforeClass fun signOnce() { fixture = ColdOpenFixture() }
    }

    @Test fun tailRendersBeforeBackfillAndLiveDuplicatesMergeWithCachedPages() = runTest(timeout = 5.seconds) {
        val peer = ColdOpenPeer(fixture, this)
        val reached = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        peer.before = { name, request ->
            if (name == "GetEvents" && GetEventsRequest.parseFrom(request.message.clone().readByteArray()).afterSeq == 0L) {
                reached.complete(Unit)
                release.await()
            }
        }
        val cache = MemoryCache()
        val r = peer.remote(cache)
        try {
            r.start()
            withTimeout(15000) { r.state.first { it.loaded } }
            val flow = r.events("long")
            withTimeout(15000) { reached.await() }
            val tail = flow.value
            assertEquals((2L..501L).toList(), tail.events.map { it.seq })
            assertTrue(tail.historyPartial)
            assertFalse(tail.loaded)
            assertTrue(tail.stateEvents.isEmpty())
            assertEquals(listOf(1L), peer.after)
            peer.count = 502
            val live = fixture.events.getValue("long").last()
            peer.push(live)
            peer.push(live)
            runCurrent()
            assertEquals((2L..502L).toList(), flow.value.events.map { it.seq })
            release.complete(Unit)
            val full = withTimeout(15000) { flow.first { it.loaded && it.events.size == 502 } }
            assertEquals((1L..502L).toList(), full.events.map { it.seq })
            assertEquals(full.events, full.stateEvents)
            assertEquals(full.events, full.verifiedEvents)
            assertFalse(full.historyPartial)
            assertEquals(502, cache.events("long").size)
            r.reconnectNow()
            runCurrent()
            assertEquals(ConnState.ONLINE, r.state.value.conn)
            assertEquals((1L..502L).toList(), flow.value.events.map { it.seq })
        } finally { r.close(); runCurrent() }
    }

    @Test fun partialDiskCacheDisplaysWithoutWaitingForTheNetwork() = runTest(timeout = 5.seconds) {
        val peer = ColdOpenPeer(fixture, this).apply { offline = true }
        val cache = MemoryCache()
        cache.putMachine(Machine.newBuilder().setId(fixture.machine.id).build())
        cache.putSession(fixture.sessions.getValue("long"))
        cache.putEvents("long", fixture.events.getValue("long").filter { it.seq == 1L || it.seq in 3L..501L })
        val r = peer.remote(cache)
        try {
            r.start()
            runCurrent()
            val flow = r.events("long")
            runCurrent()
            val restored = flow.value
            assertEquals(listOf(1L) + (3L..501L).toList(), restored.events.map { it.seq })
            assertTrue(restored.historyPartial)
            assertEquals(listOf(1L), restored.stateEvents.map { it.seq })
        } finally { r.close(); runCurrent() }
    }

    @Test fun evictedHistoryRefetchesDespiteThePersistedSessionsLastSeq() = runTest(timeout = 5.seconds) {
        val peer = ColdOpenPeer(fixture, this)
        val cache = MemoryCache()
        cache.putMachine(peer.machineRows.single())
        cache.putSession(fixture.sessions.getValue("long"))
        cache.putEvents("long", fixture.events.getValue("long").take(501))
        cache.dropEvents("long") // Whole-session eviction keeps the session metadata.
        val r = peer.remote(cache)
        try {
            r.start()
            withTimeout(15000) { r.state.first { it.loaded } }
            val full = withTimeout(15000) { r.events("long").first { it.loaded } }
            assertEquals((1L..501L).toList(), full.events.map { it.seq })
            assertEquals(listOf(1L, 0L), peer.after)
            assertEquals(full.events, full.stateEvents)
            assertEquals(501, cache.events("long").size)
        } finally { r.close(); runCurrent() }
    }

    @Test fun subscriptionsProtectMemoryOnlyReopens() = runTest(timeout = 5.seconds) {
        val peer = ColdOpenPeer(fixture, this)
        val changes = mutableListOf<Pair<String, Boolean>>()
        val cache = object : CipherCache by MemoryCache() {
            override suspend fun setSessionOpen(sessionId: String, open: Boolean) {
                changes += sessionId to open
            }
        }
        val r = peer.remote(cache)
        try {
            r.start()
            withTimeout(15000) { r.state.first { it.loaded } }
            val flow = r.events("short")
            val first = backgroundScope.launch { flow.collect {} }
            withTimeout(15000) { flow.first { it.loaded } }
            runCurrent()
            assertEquals("short" to true, changes.lastOrNull())
            first.cancelAndJoin()
            runCurrent()
            assertEquals("short" to false, changes.lastOrNull())
            val calls = peer.after.size
            val second = backgroundScope.launch { flow.collect {} }
            runCurrent()
            assertEquals("short" to true, changes.lastOrNull())
            assertEquals(calls, peer.after.size) // The reopen used the in-memory buffer.
            second.cancelAndJoin()
        } finally { r.close(); runCurrent() }
    }

    @Test fun closingRemoteStopsCacheMaintenance() = runTest(timeout = 5.seconds) {
        var closed = false
        val cache = object : CipherCache by MemoryCache() {
            override fun close() { closed = true }
        }
        ColdOpenPeer(fixture, this).remote(cache).close()
        runCurrent()
        assertTrue(closed)
    }

    private suspend fun TestScope.checkGap(missing: Long) {
        val peer = ColdOpenPeer(fixture, this).also { it.missing = missing }
        val r = peer.remote()
        try {
            r.start()
            withTimeout(15000) { r.state.first { it.loaded } }
            val flow = r.events("long")
            val gap = withTimeout(15000) { flow.first { it.fetchError != null } }
            assertFalse(gap.loaded)
            assertTrue(gap.historyPartial)
            assertEquals((missing - 1).toInt(), gap.stateEvents.size)
            assertEquals(500, gap.events.size)
            assertEquals(if (missing == 501L) listOf(1L, 0L) else listOf(1L, 0L, missing - 1), peer.after)
            peer.missing = 0
            r.retryEvents("long")
            val full = withTimeout(15000) { flow.first { it.loaded } }
            assertEquals((1L..501L).toList(), full.events.map { it.seq })
            assertNull(full.fetchError)
        } finally { r.close(); runCurrent() }
    }

    @Test fun interiorGapIsRetriedWithoutDuplicates() = runTest(timeout = 5.seconds) { checkGap(250) }
    @Test fun missingAdvertisedLastEventCannotCompleteHistory() = runTest(timeout = 5.seconds) { checkGap(501) }

    @Test fun partialTranscriptDoesNotSettleInputsAcrossAGap() {
        val event = Event.newBuilder().setSeq(601).setSessionId("session")
            .setInputState(InputState.newBuilder().setRequestId("request")
                .setStatus(InputStatus.INPUT_STATUS_SUBMITTED)).build()
        val partial = SessionEvents(events = listOf(event), stateEvents = emptyList(), historyPartial = true)
        assertTrue(currentState(partial).inputs.isEmpty())
        assertEquals(InputStatus.INPUT_STATUS_SUBMITTED,
            currentState(partial.copy(stateEvents = listOf(event))).inputs["request"])
    }

    @Test fun shortAndEmptySessionsNeedOnlyOneHistoryRequestEach() = runTest(timeout = 5.seconds) {
        val peer = ColdOpenPeer(fixture, this)
        val r = peer.remote()
        try {
            r.start()
            withTimeout(15000) { r.state.first { it.loaded } }
            assertEquals(2, withTimeout(15000) { r.events("short").first { it.loaded } }.events.size)
            val empty = withTimeout(15000) { r.events("empty").first { it.loaded } }
            assertTrue(empty.events.isEmpty())
            assertFalse(empty.historyPartial)
            assertEquals(listOf(0L, 0L), peer.after)
        } finally { r.close(); runCurrent() }
    }
}
