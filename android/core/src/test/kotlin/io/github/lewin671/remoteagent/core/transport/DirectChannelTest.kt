package io.github.lewin671.remoteagent.core.transport

import io.github.lewin671.remoteagent.core.proto.DirectCandFamily
import io.github.lewin671.remoteagent.core.proto.DirectCandType
import io.github.lewin671.remoteagent.core.proto.DirectStatsPair
import io.github.lewin671.remoteagent.core.proto.DirectStatsReport
import io.github.lewin671.remoteagent.core.proto.DirectStateKind
import io.github.lewin671.remoteagent.proto.Fragment
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.cancel
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.TestDispatcher
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
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

@OptIn(ExperimentalCoroutinesApi::class)
class DirectChannelTest {
    private val validSdp =
        "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=fingerprint:sha-256 00\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n"
            .toByteArray(StandardCharsets.UTF_8)

    private fun TestScope.testRuntime(): Pair<TestDispatcher, CoroutineScope> {
        val dispatcher = StandardTestDispatcher(testScheduler)
        return dispatcher to CoroutineScope(SupervisorJob() + dispatcher)
    }

    @Test
    fun descriptionAndRemoteValidateMinimalSdpAndForwardCandidates() = runTest {
        val (dispatcher, closeScope) = testRuntime()
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
                monotonic = { testScheduler.currentTime },
                dispatcher = dispatcher,
                receiveDispatcher = dispatcher,
                closeScope = closeScope,
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
            runCurrent()
            closeScope.cancel()
        }
        assertTrue(fakePc!!.closed)
    }

    @Test
    fun fragmentRoundTripRejectsWrongGenerationAndUnknownFieldsBeforeConfirm() = runTest {
        val (dispatcher, closeScope) = testRuntime()
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
                monotonic = { testScheduler.currentTime },
                dispatcher = dispatcher,
                receiveDispatcher = dispatcher,
                closeScope = closeScope,
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
            runCurrent()
            assertTrue(received.isEmpty())

            // Replay real fragments -> exact payload reassembled.
            for (frag in data.sent) data.listener.onMessage(frag)
            runCurrent()
            assertEquals(1, received.size)
            assertArrayEquals(payload, received[0])
        } finally {
            channel.close()
            runCurrent()
            closeScope.cancel()
        }
    }

    @Test
    fun readStatsForwardsStructuredReportAndFallsBackDcState() = runTest {
        val (dispatcher, closeScope) = testRuntime()
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
                monotonic = { testScheduler.currentTime },
                dispatcher = dispatcher,
                receiveDispatcher = dispatcher,
                closeScope = closeScope,
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
            runCurrent()
            closeScope.cancel()
        }
    }

    @Test
    fun silentDataChannelCloseDetectedOnSendProbe() = runTest {
        val (dispatcher, closeScope) = testRuntime()
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
                monotonic = { testScheduler.currentTime },
                dispatcher = dispatcher,
                receiveDispatcher = dispatcher,
                closeScope = closeScope,
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
            runCurrent()
            closeScope.cancel()
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
    fun incomingQueueOverflowClosesChannelInsteadOfUnboundedBuffering() = runTest {
        val (dispatcher, closeScope) = testRuntime()
        var fakePc: FakePeerConnection? = null
        val closed = java.util.concurrent.atomic.AtomicBoolean(false)
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
                monotonic = { testScheduler.currentTime },
                dispatcher = dispatcher,
                receiveDispatcher = dispatcher,
                closeScope = closeScope,
            ),
        )
        try {
            // Start the receiver coroutine so it is suspended on incoming, then burst frames
            // before yielding to StandardTestDispatcher so the bounded incoming buffer overflows.
            runCurrent()
            val data = fakePc!!.channels.getValue(0)
            val chunk = ByteArray(1024)
            for (i in 0 until 200) {
                data.listener.onMessage(chunk)
            }
            assertTrue("expected incoming queue overflow to close channel", closed.get())
        } finally {
            channel.close()
            runCurrent()
            closeScope.cancel()
        }
    }

    @Test
    fun rejectedFirstFragmentDoesNotPoisonPartialMap() = runTest {
        val (dispatcher, closeScope) = testRuntime()
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
                monotonic = { testScheduler.currentTime },
                dispatcher = dispatcher,
                receiveDispatcher = dispatcher,
                closeScope = closeScope,
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
            runCurrent()
            assertEquals(1, received.size)
            assertArrayEquals(payload, received[0])
        } finally {
            channel.close()
            runCurrent()
            closeScope.cancel()
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
    fun syncStateChangeDuringCreateDataChannelDoesNotThrowAndDeliversOpen() = runTest {
        val (dispatcher, closeScope) = testRuntime()
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
                monotonic = { testScheduler.currentTime },
                dispatcher = dispatcher,
                receiveDispatcher = dispatcher,
                closeScope = closeScope,
            ),
        )
        try {
            assertEquals(listOf(true), dcStates.toList())
        } finally {
            channel.close()
            runCurrent()
            closeScope.cancel()
        }
    }

    @Test
    fun idleChannelDoesNotPollDataChannelStateAndExpiresPartialsOnDemand() = runTest {
        val (dispatcher, closeScope) = testRuntime()
        var fakePc: FakePeerConnection? = null
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
                monotonic = { testScheduler.currentTime },
                dispatcher = dispatcher,
                receiveDispatcher = dispatcher,
                closeScope = closeScope,
                partialTimeoutMs = 40L,
            ),
        )
        try {
            val data = fakePc!!.channels.getValue(0)
            val probe = fakePc!!.channels.getValue(1)
            data.open()
            probe.open()
            channel.confirm()
            runCurrent()
            data.stateCalls.set(0)
            probe.stateCalls.set(0)
            advanceTimeBy(250)
            runCurrent()
            assertEquals("idle channel must not poll data.state()", 0, data.stateCalls.get())
            assertEquals("idle channel must not poll probe.state()", 0, probe.stateCalls.get())

            // Send an incomplete 2-part message and verify on-demand expiry frees its receive budget
            // without ever mutating DirectBudget concurrently with DirectChannel.receive().
            fun incompleteFragment(idByte: Byte): ByteArray = Fragment.newBuilder()
                .setMessageId(com.google.protobuf.ByteString.copyFrom(ByteArray(16) { idByte }))
                .setIndex(0)
                .setCount(2)
                .setCiphertextLength(2048)
                .setCiphertextSha256(com.google.protobuf.ByteString.copyFrom(ByteArray(32)))
                .setPathGeneration(1L)
                .setFragment(com.google.protobuf.ByteString.copyFrom(ByteArray(1024) { 7 }))
                .build()
                .toByteArray()

            data.listener.onMessage(incompleteFragment(0x11))
            runCurrent()
            assertFalse("incomplete fragment must reserve receive budget", budget.reserve("m-1", "50", 1, 1, (2 shl 20), 1))
            assertTrue(budget.reserve("m-1", "50", 1, 1, (2 shl 20) - 1024, 127))
            budget.free("m-1", 1, (2 shl 20) - 1024, 127)

            // Just before partialTimeoutMs, the partial must still be retained.
            advanceTimeBy(39)
            runCurrent()
            assertFalse("partial must not expire before partialTimeoutMs", budget.reserve("m-1", "50", 1, 1, (2 shl 20), 1))

            // Crossing partialTimeoutMs expires the partial and frees the full receive budget.
            advanceTimeBy(1)
            runCurrent()
            assertTrue("on-demand expiry must free full receive budget", budget.reserve("m-1", "50", 1, 1, (2 shl 20), 128))
            budget.free("m-1", 1, (2 shl 20), 128)

            // A subsequent incomplete fragment re-arms on-demand expiry cleanly after the previous expiry job exits.
            data.listener.onMessage(incompleteFragment(0x22))
            runCurrent()
            assertFalse(budget.reserve("m-1", "50", 1, 1, (2 shl 20), 1))
            advanceTimeBy(40)
            runCurrent()
            assertTrue(budget.reserve("m-1", "50", 1, 1, (2 shl 20), 128))
            budget.free("m-1", 1, (2 shl 20), 128)

            advanceTimeBy(1_000)
            runCurrent()
            assertEquals("channel must remain idle after expiry", 0, data.stateCalls.get())
            assertEquals("channel must remain idle after expiry", 0, probe.stateCalls.get())
        } finally {
            channel.close()
            runCurrent()
            closeScope.cancel()
        }
    }

    @Test
    fun reassemblyTransfersFragmentBudgetWithoutDoubleCharging() = runTest {
        val (dispatcher, closeScope) = testRuntime()
        var fakePc: FakePeerConnection? = null
        val budget = DirectBudget()
        val received = CopyOnWriteArrayList<ByteArray>()
        val channel = DirectChannel(
            DirectChannelOptions(
                peer = "m-1",
                roundId = 60L,
                use = 2,
                generation = 1L,
                budget = budget,
                valid = { true },
                stun = emptyList(),
                factory = RtcFactory { _, l -> FakePeerConnection(l).also { fakePc = it } },
                candidate = {},
                state = { _, _ -> },
                frame = { received.add(it) },
                closed = {},
                monotonic = { testScheduler.currentTime },
                dispatcher = dispatcher,
                receiveDispatcher = dispatcher,
                closeScope = closeScope,
            ),
        )
        val preReserved = 768 shl 10
        assertTrue(budget.reserve("m-1", "60", 2, 1, preReserved, 1))
        try {
            val data = fakePc!!.channels.getValue(0)
            data.open()
            channel.confirm()

            // 768 KiB split into 49 fragments of (16 KiB - 128); total 1.5 MiB < 2 MiB,
            // whereas a 2x reassembly double-charge peaks at 2.25 MiB > 2 MiB and drops the message.
            val payload = ByteArray(768 shl 10) { (it * 31).toByte() }
            val digest = java.security.MessageDigest.getInstance("SHA-256").digest(payload)
            val chunkSize = (16 shl 10) - 128
            val count = (payload.size + chunkSize - 1) / chunkSize
            assertEquals(49, count)
            val msgId = com.google.protobuf.ByteString.copyFrom(ByteArray(16) { 0x60 })
            for (index in 0 until count) {
                val start = index * chunkSize
                val end = minOf(payload.size, start + chunkSize)
                val frag = Fragment.newBuilder()
                    .setMessageId(msgId)
                    .setIndex(index)
                    .setCount(count)
                    .setCiphertextLength(payload.size)
                    .setCiphertextSha256(com.google.protobuf.ByteString.copyFrom(digest))
                    .setPathGeneration(1L)
                    .setFragment(com.google.protobuf.ByteString.copyFrom(payload, start, end - start))
                    .build()
                    .toByteArray()
                data.listener.onMessage(frag)
            }
            runCurrent()
            assertEquals(1, received.size)
            assertArrayEquals(payload, received[0])

            // Also verify bad-hash reassembly frees all transferred bytes and frame slots cleanly.
            val badHash = digest.copyOf().also { it[0] = (it[0].toInt() xor 0xff).toByte() }
            val badMsgId = com.google.protobuf.ByteString.copyFrom(ByteArray(16) { 0x61 })
            for (index in 0 until count) {
                val start = index * chunkSize
                val end = minOf(payload.size, start + chunkSize)
                val frag = Fragment.newBuilder()
                    .setMessageId(badMsgId)
                    .setIndex(index)
                    .setCount(count)
                    .setCiphertextLength(payload.size)
                    .setCiphertextSha256(com.google.protobuf.ByteString.copyFrom(badHash))
                    .setPathGeneration(1L)
                    .setFragment(com.google.protobuf.ByteString.copyFrom(payload, start, end - start))
                    .build()
                    .toByteArray()
                data.listener.onMessage(frag)
            }
            runCurrent()
            budget.free("m-1", 1, preReserved, 1)
            // After bad-hash cleanup + releasing preReserved, the full 2 MiB / 128-frame receive budget must be free.
            assertTrue(budget.reserve("m-1", "60", 2, 1, (2 shl 20), 128))
            budget.free("m-1", 1, (2 shl 20), 128)
            assertEquals(1, received.size)
        } finally {
            channel.close()
            runCurrent()
            closeScope.cancel()
        }
    }

    @Test
    fun reassemblySupportsMaximum128FragmentMessage() = runTest {
        val (dispatcher, closeScope) = testRuntime()
        var fakePc: FakePeerConnection? = null
        val budget = DirectBudget()
        val received = CopyOnWriteArrayList<ByteArray>()
        val channel = DirectChannel(
            DirectChannelOptions(
                peer = "m-1",
                roundId = 61L,
                use = 2,
                generation = 1L,
                budget = budget,
                valid = { true },
                stun = emptyList(),
                factory = RtcFactory { _, l -> FakePeerConnection(l).also { fakePc = it } },
                candidate = {},
                state = { _, _ -> },
                frame = { received.add(it) },
                closed = {},
                monotonic = { testScheduler.currentTime },
                dispatcher = dispatcher,
                receiveDispatcher = dispatcher,
                closeScope = closeScope,
            ),
        )
        try {
            val data = fakePc!!.channels.getValue(0)
            data.open()
            channel.confirm()

            val count = 128
            val chunkSize = 64
            val payload = ByteArray(count * chunkSize) { (it % 251).toByte() }
            val digest = java.security.MessageDigest.getInstance("SHA-256").digest(payload)
            val msgId = com.google.protobuf.ByteString.copyFrom(ByteArray(16) { 0x62 })
            for (index in 0 until count) {
                val start = index * chunkSize
                val frag = Fragment.newBuilder()
                    .setMessageId(msgId)
                    .setIndex(index)
                    .setCount(count)
                    .setCiphertextLength(payload.size)
                    .setCiphertextSha256(com.google.protobuf.ByteString.copyFrom(digest))
                    .setPathGeneration(1L)
                    .setFragment(com.google.protobuf.ByteString.copyFrom(payload, start, chunkSize))
                    .build()
                    .toByteArray()
                data.listener.onMessage(frag)
            }
            runCurrent()
            assertEquals(1, received.size)
            assertArrayEquals(payload, received[0])
            assertTrue(budget.reserve("m-1", "61", 2, 1, (2 shl 20), 128))
            budget.free("m-1", 1, (2 shl 20), 128)
        } finally {
            channel.close()
            runCurrent()
            closeScope.cancel()
        }
    }

    @Test
    fun closeReportsFixedReasonWordForNativeFailures() = runTest {
        val (dispatcher, closeScope) = testRuntime()
        val reasons = CopyOnWriteArrayList<String>()
        var listenerRef: RtcPeerListener? = null
        val channel = DirectChannel(
            DirectChannelOptions(
                peer = "m-1",
                roundId = 71L,
                use = 1,
                generation = 1L,
                budget = DirectBudget(),
                valid = { true },
                stun = emptyList(),
                factory = RtcFactory { _, l ->
                    listenerRef = l
                    FakePeerConnection(l)
                },
                candidate = {},
                state = { _, _ -> },
                frame = {},
                closed = { reason -> reasons.add(reason) },
                monotonic = { testScheduler.currentTime },
                dispatcher = dispatcher,
                receiveDispatcher = dispatcher,
                closeScope = closeScope,
            ),
        )
        try {
            listenerRef!!.onConnectionStateChange(RtcPeerState.FAILED)
            runCurrent()
            assertEquals(listOf("pc_failed"), reasons.toList())
        } finally {
            channel.close()
            runCurrent()
            closeScope.cancel()
        }
    }
}
