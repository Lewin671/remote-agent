package io.github.lewin671.remoteagent.core.transport

import io.github.lewin671.remoteagent.core.proto.DirectCandFamily
import io.github.lewin671.remoteagent.core.proto.DirectCandType
import io.github.lewin671.remoteagent.core.proto.DirectStatsPair
import io.github.lewin671.remoteagent.core.proto.DirectStatsReport
import io.github.lewin671.remoteagent.core.proto.DirectStateKind
import io.github.lewin671.remoteagent.proto.Fragment
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.nio.charset.StandardCharsets
import java.util.concurrent.CopyOnWriteArrayList

private class FakeDataChannel(
    val label: String,
    var listener: RtcDataChannelListener,
) : RtcDataChannel {
    @Volatile var chState = RtcChannelState.CONNECTING
    val sent = CopyOnWriteArrayList<ByteArray>()
    var closed = false

    override fun state(): RtcChannelState = chState
    override fun bufferedAmount(): Long = 0L
    override fun send(data: ByteArray, binary: Boolean): Boolean {
        if (closed || chState != RtcChannelState.OPEN) return false
        sent.add(data.copyOf())
        return true
    }

    override fun close() {
        closed = true
        chState = RtcChannelState.CLOSED
    }

    fun open() {
        chState = RtcChannelState.OPEN
        listener.onStateChange()
    }
}

private class FakePeerConnection(
    private val listener: RtcPeerListener,
    private val onStats: () -> DirectStatsReport = { DirectStatsReport.getDefaultInstance() },
) : RtcPeerConnection {
    val channels = LinkedHashMap<Int, FakeDataChannel>()
    val addedCandidates = ArrayList<String>()
    var remoteSdp: String? = null
    var closed = false
    var offerSdp = "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=fingerprint:sha-256 00\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n"

    override fun createDataChannel(
        label: String,
        id: Int,
        ordered: Boolean,
        maxRetransmits: Int?,
        listener: RtcDataChannelListener,
    ): RtcDataChannel = FakeDataChannel(label, listener).also { channels[id] = it }

    override suspend fun createOffer(iceRestart: Boolean): String = offerSdp
    override suspend fun createAnswer(): String = offerSdp
    override suspend fun setLocalDescription(offer: Boolean, sdp: String) {}
    override suspend fun setRemoteDescription(offer: Boolean, sdp: String) {
        remoteSdp = sdp
    }

    override suspend fun addIceCandidate(candidate: String, sdpMid: String, sdpMLineIndex: Int, usernameFragment: String) {
        addedCandidates.add(candidate)
    }

    override suspend fun readStats(): DirectStatsReport = onStats()

    override fun close() {
        closed = true
        channels.values.forEach { it.close() }
    }

    fun emitCandidate(cand: RtcIceCandidate) = listener.onIceCandidate(cand)
    fun emitGatheringComplete() = listener.onIceGatheringComplete()
}

class DirectChannelTest {
    private val validSdp =
        "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=fingerprint:sha-256 00\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n"
            .toByteArray(StandardCharsets.UTF_8)

    @Test
    fun descriptionAndRemoteValidateMinimalSdpAndForwardCandidates() = runBlocking {
        var fakePc: FakePeerConnection? = null
        val candidates = ArrayList<RtcIceCandidate?>()
        val states = ArrayList<Pair<DirectStateKind, String>>()
        val channel = DirectChannel(
            DirectChannelOptions(
                peer = "m-1",
                roundId = 7L,
                use = 1,
                generation = 3L,
                budget = DirectBudget(),
                valid = { true },
                stun = listOf("stun:example.com:3478"),
                factory = RtcFactory { _, l -> FakePeerConnection(l).also { fakePc = it } },
                candidate = { candidates.add(it) },
                state = { k, v -> states.add(k to v) },
                frame = {},
                closed = {},
            ),
        )
        try {
            val sdp = channel.description(offer = true)
            assertTrue(String(sdp, StandardCharsets.UTF_8).contains("a=fingerprint:"))
            channel.remote(validSdp, offer = false)
            assertEquals(String(validSdp, StandardCharsets.UTF_8), fakePc!!.remoteSdp)

            assertTrue(
                runCatching { channel.remote("v=0\r\nno-sdp".toByteArray(), offer = false) }.isFailure,
            )

            channel.addCandidate("candidate:1 1 udp 1 192.0.2.1 9999 typ host", "0", 0, "u")
            assertEquals(listOf("candidate:1 1 udp 1 192.0.2.1 9999 typ host"), fakePc!!.addedCandidates)

            fakePc!!.emitCandidate(RtcIceCandidate("candidate:2 1 udp 1 198.51.100.2 5000 typ srflx", "0", 0, true))
            fakePc!!.emitGatheringComplete()
            assertEquals(2, candidates.size)
            assertEquals(true, candidates[0]?.isSrflx)
            assertEquals(null, candidates[1])
        } finally {
            channel.close()
        }
        kotlinx.coroutines.withTimeout(2_000L) {
            while (!fakePc!!.closed) kotlinx.coroutines.delay(5)
        }
        assertTrue(fakePc!!.closed)
    }

    @Test
    fun fragmentRoundTripRejectsWrongGenerationAndUnknownFieldsBeforeConfirm() = runBlocking {
        var fakePc: FakePeerConnection? = null
        val received = CopyOnWriteArrayList<ByteArray>()
        val channel = DirectChannel(
            DirectChannelOptions(
                peer = "m-1",
                roundId = 9L,
                use = 1,
                generation = 42L,
                budget = DirectBudget(),
                valid = { true },
                stun = emptyList(),
                factory = RtcFactory { _, l -> FakePeerConnection(l).also { fakePc = it } },
                candidate = {},
                state = { _, _ -> },
                frame = { received.add(it) },
                closed = {},
            ),
        )
        try {
            val data = fakePc!!.channels.getValue(0)
            data.open()

            // Before confirm, writes fail with PathChanged.
            assertTrue(
                runCatching { channel.write(byteArrayOf(1, 2, 3)) {} }.exceptionOrNull() is PathChanged,
            )

            channel.confirm()
            assertTrue(channel.ready)

            val payload = ByteArray(40 * 1024) { (it % 251).toByte() }
            var handedOff = false
            channel.write(payload) { handedOff = true }
            assertTrue(handedOff)
            assertTrue(data.sent.size > 1)

            // Feed fragments with wrong path_generation -> ignored.
            val badGen = Fragment.parseFrom(data.sent.first()).toBuilder().setPathGeneration(99L).build().toByteArray()
            data.listener.onMessage(badGen)

            // Feed unknown field -> ignored.
            val withUnknown = data.sent.first() + byteArrayOf(0x78.toByte(), 0x01)
            data.listener.onMessage(withUnknown)
            kotlinx.coroutines.delay(50)
            assertTrue(received.isEmpty())

            // Replay real fragments -> exact payload reassembled.
            for (frag in data.sent) data.listener.onMessage(frag)
            kotlinx.coroutines.withTimeout(2_000L) {
                while (received.isEmpty()) kotlinx.coroutines.delay(5)
            }
            assertEquals(1, received.size)
            assertArrayEquals(payload, received[0])
        } finally {
            channel.close()
        }
    }

    @Test
    fun readStatsForwardsStructuredReportAndFallsBackDcState() = runBlocking {
        var fakePc: FakePeerConnection? = null
        val statsReport = DirectStatsReport.newBuilder()
            .setIceState("connected")
            .setDtlsState("connected")
            .addPairs(
                DirectStatsPair.newBuilder()
                    .setLocalType(DirectCandType.DIRECT_CAND_TYPE_HOST)
                    .setLocalFamily(DirectCandFamily.DIRECT_CAND_FAMILY_V4)
                    .setRemoteType(DirectCandType.DIRECT_CAND_TYPE_SRFLX)
                    .setRemoteFamily(DirectCandFamily.DIRECT_CAND_FAMILY_V4)
                    .setNominated(true),
            )
            .build()
        val channel = DirectChannel(
            DirectChannelOptions(
                peer = "m-1",
                roundId = 11L,
                use = 1,
                generation = 1L,
                budget = DirectBudget(),
                valid = { true },
                stun = emptyList(),
                factory = RtcFactory { _, l -> FakePeerConnection(l) { statsReport } .also { fakePc = it } },
                candidate = {},
                state = { _, _ -> },
                frame = {},
                closed = {},
            ),
        )
        try {
            fakePc!!.channels.getValue(0).open()
            val stats = channel.readStats()
            assertEquals("connected", stats.iceState)
            assertEquals("open", stats.dcState)
            assertEquals(1, stats.pairsCount)
            assertEquals(DirectCandType.DIRECT_CAND_TYPE_HOST, stats.pairsList[0].localType)
            assertEquals(DirectCandFamily.DIRECT_CAND_FAMILY_V4, stats.pairsList[0].localFamily)
        } finally {
            channel.close()
        }
    }

    @Test
    fun silentDataChannelCloseDetectedOnTick() = runBlocking {
        var fakePc: FakePeerConnection? = null
        val closed = java.util.concurrent.atomic.AtomicBoolean(false)
        val channel = DirectChannel(
            DirectChannelOptions(
                peer = "m-1",
                roundId = 12L,
                use = 0,
                generation = 1L,
                budget = DirectBudget(),
                valid = { true },
                stun = emptyList(),
                factory = RtcFactory { _, l -> FakePeerConnection(l).also { fakePc = it } },
                candidate = {},
                state = { _, _ -> },
                frame = {},
                closed = { closed.set(true) },
            ),
        )
        try {
            val data = fakePc!!.channels.getValue(0)
            data.open()
            channel.confirm()
            assertTrue(channel.ready)

            // Transition state silently without invoking listener.onStateChange().
            data.chState = RtcChannelState.CLOSED
            kotlinx.coroutines.withTimeout(3_000L) {
                while (!closed.get()) kotlinx.coroutines.delay(20)
            }
            assertTrue(closed.get())
        } finally {
            channel.close()
        }
    }

    @Test
    fun blockingPcCloseDoesNotBlockChannelCloseAndReleasesBudget() = runBlocking {
        val enterLatch = java.util.concurrent.CountDownLatch(1)
        val releaseLatch = java.util.concurrent.CountDownLatch(1)
        val closed = java.util.concurrent.atomic.AtomicBoolean(false)
        val budget = DirectBudget()
        val blockingFactory = RtcFactory { _, listener ->
            object : RtcPeerConnection {
                override fun createDataChannel(label: String, id: Int, ordered: Boolean, maxRetransmits: Int?, listener: RtcDataChannelListener) =
                    FakeDataChannel(label, listener)
                override suspend fun createOffer(iceRestart: Boolean) = ""
                override suspend fun createAnswer() = ""
                override suspend fun setLocalDescription(offer: Boolean, sdp: String) {}
                override suspend fun setRemoteDescription(offer: Boolean, sdp: String) {}
                override suspend fun addIceCandidate(candidate: String, sdpMid: String, sdpMLineIndex: Int, usernameFragment: String) {}
                override fun close() {
                    enterLatch.countDown()
                    releaseLatch.await()
                }
            }
        }
        val channel = DirectChannel(
            DirectChannelOptions(
                peer = "m-1",
                roundId = 13L,
                use = 0,
                generation = 1L,
                budget = budget,
                valid = { true },
                stun = emptyList(),
                factory = blockingFactory,
                candidate = {},
                state = { _, _ -> },
                frame = {},
                closed = { closed.set(true) },
            ),
        )
        try {
            channel.close()
            // pc.close() is still blocked, yet channel.close() returned, closed fired, and budget was released.
            assertTrue(enterLatch.await(2, java.util.concurrent.TimeUnit.SECONDS))
            assertTrue(closed.get())
            assertTrue(budget.acquire("m-1", "99", 0))
            budget.release("m-1", 0)
        } finally {
            releaseLatch.countDown()
        }
    }

    @Test
    fun expireStaleProbesHandlesConcurrentRemovalWithoutNoSuchElementException() {
        // Reproduce the exact Kotlin Collection.toList() race on ConcurrentHashMap:
        // size() observes 1, then another thread removes the sole entry before iterator().next().
        val backing = java.util.concurrent.ConcurrentHashMap<String, Long>()
        val racy = object : java.util.concurrent.ConcurrentMap<String, Long> by backing {
            override val entries: MutableSet<MutableMap.MutableEntry<String, Long>>
                get() {
                    val delegate = backing.entries
                    return object : MutableSet<MutableMap.MutableEntry<String, Long>> by delegate {
                        override val size: Int
                            get() {
                                val s = delegate.size
                                if (s == 1) backing.clear()
                                return s
                            }
                    }
                }
        }
        racy["probe-1"] = 0L
        var missed = 0
        assertFalse(expireStaleProbes(racy, 2_000_000_000L) { ++missed })
        assertEquals(1, missed)
        assertTrue(racy.isEmpty())

        // Normal sweep removes expired probes (including concurrent removal during iteration),
        // leaves fresh ones, and triggers at 3 misses.
        missed = 0
        val probes = java.util.concurrent.ConcurrentHashMap<String, Long>()
        probes["stale-1"] = 0L
        probes["stale-2"] = 100L
        probes["stale-3"] = 200L
        probes["replied-concurrently"] = 1_500_000_000L
        probes["fresh"] = 1_500_000_000L
        assertTrue(
            expireStaleProbes(probes, 2_000_000_000L) {
                probes.remove("replied-concurrently")
                ++missed
            },
        )
        assertEquals(3, missed)
        assertEquals(setOf("fresh"), probes.keys)
    }
}
