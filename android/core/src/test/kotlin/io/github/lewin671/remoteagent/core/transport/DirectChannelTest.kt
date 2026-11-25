package io.github.lewin671.remoteagent.core.transport

import io.github.lewin671.remoteagent.core.proto.DirectCandFamily
import io.github.lewin671.remoteagent.core.proto.DirectCandType
import io.github.lewin671.remoteagent.core.proto.DirectStatsPair
import io.github.lewin671.remoteagent.core.proto.DirectStatsReport
import io.github.lewin671.remoteagent.core.proto.DirectStateKind
import io.github.lewin671.remoteagent.proto.Fragment
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.async
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
    @Volatile var buffered: Long = 0L
    val stateCalls = java.util.concurrent.atomic.AtomicInteger(0)
    val sent = CopyOnWriteArrayList<ByteArray>()
    var closed = false

    override fun state(): RtcChannelState {
        stateCalls.incrementAndGet()
        return chState
    }
    override fun bufferedAmount(): Long = buffered
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
    private val syncOpenOnCreateDataChannel: Boolean = false,
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
    ): RtcDataChannel = FakeDataChannel(label, listener).also { ch ->
        channels[id] = ch
        if (syncOpenOnCreateDataChannel && id == 0) {
            ch.chState = RtcChannelState.OPEN
            listener.onStateChange()
        }
    }

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
    fun silentDataChannelCloseDetectedOnSendProbe() = runBlocking {
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
            val probe = fakePc!!.channels.getValue(1)
            data.open()
            probe.open()
            channel.confirm()
            assertTrue(channel.ready)

            // Transition state silently without invoking listener.onStateChange(); next probe closes channel.
            data.chState = RtcChannelState.CLOSED
            assertFalse(channel.sendProbe(ByteArray(17)))
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
    fun incomingQueueOverflowClosesChannelInsteadOfUnboundedBuffering() = runBlocking {
        var fakePc: FakePeerConnection? = null
        val closed = java.util.concurrent.atomic.AtomicBoolean(false)
        val blockReceive = java.util.concurrent.CountDownLatch(1)
        val receiveExec = java.util.concurrent.Executors.newSingleThreadExecutor()
        val receiveDispatcher = receiveExec.asCoroutineDispatcher()
        // Occupy the single receive worker thread so incoming frames pile up in DirectChannel.incoming.
        receiveExec.submit { blockReceive.await(5, java.util.concurrent.TimeUnit.SECONDS) }
        val channel = DirectChannel(
            DirectChannelOptions(
                peer = "m-1",
                roundId = 20L,
                use = 1,
                generation = 1L,
                budget = DirectBudget(),
                valid = { true },
                stun = emptyList(),
                factory = RtcFactory { _, l -> FakePeerConnection(l).also { fakePc = it } },
                candidate = {},
                state = { _, _ -> },
                frame = {},
                closed = { closed.set(true) },
                receiveDispatcher = receiveDispatcher,
            ),
        )
        try {
            val data = fakePc!!.channels.getValue(0)
            val chunk = ByteArray(1024)
            for (i in 0 until 200) {
                data.listener.onMessage(chunk)
            }
            assertTrue("expected incoming queue overflow to close channel", closed.get())
        } finally {
            blockReceive.countDown()
            channel.close()
            receiveExec.shutdownNow()
        }
    }

    @Test
    fun rejectedFirstFragmentDoesNotPoisonPartialMap() = runBlocking {
        var fakePc: FakePeerConnection? = null
        val received = CopyOnWriteArrayList<ByteArray>()
        val channel = DirectChannel(
            DirectChannelOptions(
                peer = "m-1",
                roundId = 21L,
                use = 1,
                generation = 7L,
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
            channel.confirm()
            val msgId = com.google.protobuf.ByteString.copyFrom(ByteArray(16) { 0x42 })
            val payload = "hello-valid-payload".toByteArray(StandardCharsets.UTF_8)
            val hash = com.google.protobuf.ByteString.copyFrom(
                java.security.MessageDigest.getInstance("SHA-256").digest(payload),
            )
            // First fragment declares ciphertextLength=5 but carries 10 bytes -> rejected before inserting partial.
            val badFirst = Fragment.newBuilder()
                .setMessageId(msgId)
                .setIndex(0)
                .setCount(2)
                .setCiphertextLength(5)
                .setCiphertextSha256(hash)
                .setPathGeneration(7L)
                .setFragment(com.google.protobuf.ByteString.copyFrom(ByteArray(10)))
                .build()
                .toByteArray()
            data.listener.onMessage(badFirst)
            // Now send a valid single-fragment message with the same messageId; must succeed cleanly.
            val good = Fragment.newBuilder()
                .setMessageId(msgId)
                .setIndex(0)
                .setCount(1)
                .setCiphertextLength(payload.size)
                .setCiphertextSha256(hash)
                .setPathGeneration(7L)
                .setFragment(com.google.protobuf.ByteString.copyFrom(payload))
                .build()
                .toByteArray()
            data.listener.onMessage(good)
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
    fun newRoundCanAcquireBudgetWhileOldRoundHasUnwindingBytes() {
        val budget = DirectBudget()
        assertTrue(budget.acquire("m-1", "round-1", 2))
        // Simulate in-flight receive SHA-256 holding bytes[1] while old round closes.
        assertTrue(budget.reserve("m-1", "round-1", 2, 1, 1024, 1))
        budget.release("m-1", 2)

        assertTrue("new round must be allowed to acquire after old uses released", budget.acquire("m-1", "round-2", 2))
        // Unwinding bytes from round-1 still count toward the 2 MiB receive cap.
        assertFalse(budget.reserve("m-1", "round-2", 2, 1, (2 shl 20), 1))
        budget.free("m-1", 1, 1024, 1)
        assertTrue(budget.reserve("m-1", "round-2", 2, 1, (2 shl 20), 1))
        budget.free("m-1", 1, (2 shl 20), 1)
        budget.release("m-1", 2)
    }

    @Test
    fun newRoundCanAcquireImmediatelyWhenClosingChannelWithInFlightWriter() = runBlocking {
        var fakePc: FakePeerConnection? = null
        val budget = DirectBudget()
        val channel = DirectChannel(
            DirectChannelOptions(
                peer = "m-1",
                roundId = 30L,
                use = 1,
                generation = 1L,
                budget = budget,
                valid = { true },
                stun = emptyList(),
                factory = RtcFactory { _, l -> FakePeerConnection(l).also { fakePc = it } },
                candidate = {},
                state = { _, _ -> },
                frame = {},
                closed = {},
            ),
        )
        val data = fakePc!!.channels.getValue(0)
        data.open()
        channel.confirm()

        val inHandoff = java.util.concurrent.CountDownLatch(1)
        val releaseHandoff = java.util.concurrent.CountDownLatch(1)
        val writeJob = async(kotlinx.coroutines.Dispatchers.IO) {
            runCatching {
                channel.write(ByteArray(1024)) {
                    inHandoff.countDown()
                    releaseHandoff.await(2, java.util.concurrent.TimeUnit.SECONDS)
                }
            }
        }
        try {
            assertTrue(inHandoff.await(2, java.util.concurrent.TimeUnit.SECONDS))
            // Close while write() is still actively holding send budget and inside write().
            channel.close()
            assertTrue("new round should acquire immediately after close()", budget.acquire("m-1", "31", 1))
            // Send bytes from round 30 remain counted until write() exits finally.
            assertFalse(budget.reserve("m-1", "31", 1, 0, (2 shl 20), 1))
            releaseHandoff.countDown()
            writeJob.await()
            assertTrue(budget.reserve("m-1", "31", 1, 0, (2 shl 20), 1))
            budget.free("m-1", 0, (2 shl 20), 1)
            budget.release("m-1", 1, "31")
        } finally {
            releaseHandoff.countDown()
        }
    }

    @Test
    fun syncStateChangeDuringCreateDataChannelDoesNotThrowAndDeliversOpen() = runBlocking {
        val dcStates = CopyOnWriteArrayList<Boolean>()
        val channel = DirectChannel(
            DirectChannelOptions(
                peer = "m-1",
                roundId = 40L,
                use = 1,
                generation = 1L,
                budget = DirectBudget(),
                valid = { true },
                stun = emptyList(),
                factory = RtcFactory { _, l -> FakePeerConnection(l, syncOpenOnCreateDataChannel = true) },
                candidate = {},
                state = { _, _ -> },
                dcState = { dcStates.add(it) },
                frame = {},
                closed = {},
            ),
        )
        try {
            assertEquals(listOf(true), dcStates.toList())
        } finally {
            channel.close()
        }
    }

    @Test
    fun idleChannelDoesNotPollDataChannelStateAndExpiresPartialsOnDemand() = runBlocking {
        var fakePc: FakePeerConnection? = null
        val mono = java.util.concurrent.atomic.AtomicLong(1_000L)
        val budget = DirectBudget()
        val channel = DirectChannel(
            DirectChannelOptions(
                peer = "m-1",
                roundId = 50L,
                use = 1,
                generation = 1L,
                budget = budget,
                valid = { true },
                stun = emptyList(),
                factory = RtcFactory { _, l -> FakePeerConnection(l).also { fakePc = it } },
                candidate = {},
                state = { _, _ -> },
                frame = {},
                closed = {},
                monotonic = { mono.get() },
                partialTimeoutMs = 40L,
            ),
        )
        try {
            val data = fakePc!!.channels.getValue(0)
            val probe = fakePc!!.channels.getValue(1)
            data.open()
            probe.open()
            channel.confirm()
            data.stateCalls.set(0)
            probe.stateCalls.set(0)
            kotlinx.coroutines.delay(250)
            assertEquals("idle channel must not poll data.state()", 0, data.stateCalls.get())
            assertEquals("idle channel must not poll probe.state()", 0, probe.stateCalls.get())

            // Send an incomplete 2-part message (1 MiB) and verify on-demand expiry frees its receive budget.
            val half = ByteArray(1024) { 7 }
            val frag = Fragment.newBuilder()
                .setMessageId(com.google.protobuf.ByteString.copyFrom(ByteArray(16) { 0x11 }))
                .setIndex(0)
                .setCount(2)
                .setCiphertextLength(2048)
                .setCiphertextSha256(com.google.protobuf.ByteString.copyFrom(ByteArray(32)))
                .setPathGeneration(1L)
                .setFragment(com.google.protobuf.ByteString.copyFrom(half))
                .build()
                .toByteArray()
            data.listener.onMessage(frag)
            kotlinx.coroutines.withTimeout(2_000L) {
                while (budget.reserve("m-1", "50", 1, 1, (2 shl 20), 1)) {
                    budget.free("m-1", 1, (2 shl 20), 1)
                    kotlinx.coroutines.delay(5)
                }
            }
            // Advance monotonic clock past partialTimeoutMs and wait for on-demand expiry to free budget.
            mono.addAndGet(50L)
            kotlinx.coroutines.withTimeout(2_000L) {
                while (!budget.reserve("m-1", "50", 1, 1, (2 shl 20), 1)) {
                    kotlinx.coroutines.delay(10)
                }
                budget.free("m-1", 1, (2 shl 20), 1)
            }
        } finally {
            channel.close()
        }
    }
}
