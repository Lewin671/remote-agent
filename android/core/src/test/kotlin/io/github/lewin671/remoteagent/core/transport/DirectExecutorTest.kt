package io.github.lewin671.remoteagent.core.transport

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.SigningKey
import io.github.lewin671.remoteagent.core.proto.DirectCandFamily
import io.github.lewin671.remoteagent.core.proto.DirectCandType
import io.github.lewin671.remoteagent.core.proto.DirectDcReadStatsEffect
import io.github.lewin671.remoteagent.core.proto.DirectPcAddCandidatesEffect
import io.github.lewin671.remoteagent.core.proto.DirectPcCloseEffect
import io.github.lewin671.remoteagent.core.proto.DirectPcConfirmEffect
import io.github.lewin671.remoteagent.core.proto.DirectPcCreateEffect
import io.github.lewin671.remoteagent.core.proto.DirectPcDescribeEffect
import io.github.lewin671.remoteagent.core.proto.DirectPcSetRemoteEffect
import io.github.lewin671.remoteagent.core.proto.DirectIceCandidate
import io.github.lewin671.remoteagent.core.proto.DirectStatsPair
import io.github.lewin671.remoteagent.core.proto.DirectStatsReport
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.Fragment
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.PayloadKind
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.nio.charset.StandardCharsets
import java.util.concurrent.CopyOnWriteArrayList

class DirectExecutorTest {
    private class StubDataChannel(private val listener: RtcDataChannelListener) : RtcDataChannel {
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

        fun deliver(bytes: ByteArray) = listener.onMessage(bytes)
    }

    private class StubPeerConnection(
        private val listener: RtcPeerListener,
        private val report: DirectStatsReport,
        emitSyncOnCreate: Boolean = false,
    ) : RtcPeerConnection {
        val channels = LinkedHashMap<Int, StubDataChannel>()
        var closed = false

        init {
            if (emitSyncOnCreate) {
                listener.onConnectionStateChange(RtcPeerState.CONNECTING)
                listener.onIceCandidate(RtcIceCandidate("candidate:0 1 udp 1 192.0.2.1 9999 typ host", "0", 0, false))
            }
        }

        override fun createDataChannel(
            label: String,
            id: Int,
            ordered: Boolean,
            maxRetransmits: Int?,
            listener: RtcDataChannelListener,
        ): RtcDataChannel = StubDataChannel(listener).also { channels[id] = it }

        override suspend fun createOffer(iceRestart: Boolean): String =
            "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=fingerprint:sha-256 00\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n"

        override suspend fun createAnswer(): String = createOffer(false)
        override suspend fun setLocalDescription(offer: Boolean, sdp: String) {}
        override suspend fun setRemoteDescription(offer: Boolean, sdp: String) {}
        override suspend fun addIceCandidate(candidate: String, sdpMid: String, sdpMLineIndex: Int, usernameFragment: String) {}
        override suspend fun readStats(): DirectStatsReport = report
        override fun close() {
            closed = true
            channels.values.forEach { it.close() }
        }
    }

    @Test
    fun keyOpsAndPcLifecycleAndRpcAndCloseWorkEndToEnd() = runBlocking {
        val events = CopyOnWriteArrayList<CoreEvent>()
        val seed = ByteArray(32) { (it + 1).toByte() }
        val xPriv = ByteArray(32) { (it + 41).toByte() }
        val peerXPriv = ByteArray(32) { (it + 81).toByte() }
        val peerXPub = Corebind.x25519PublicKey(peerXPriv)
        val report = DirectStatsReport.newBuilder()
            .setIceState("connected")
            .setDcState("open")
            .addPairs(
                DirectStatsPair.newBuilder()
                    .setLocalFamily(DirectCandFamily.DIRECT_CAND_FAMILY_V6)
                    .setLocalType(DirectCandType.DIRECT_CAND_TYPE_HOST)
                    .setRemoteType(DirectCandType.DIRECT_CAND_TYPE_SRFLX)
                    .setNominated(true),
            )
            .build()
        var pc: StubPeerConnection? = null
        val pushed = CopyOnWriteArrayList<String>()

        val executor = DirectExecutor(
            emit = { events.add(it) },
            onPushedEvent = { _, _, _, hdr, _ -> pushed.add(hdr.requestId) },
            factory = RtcFactory { _, l -> StubPeerConnection(l, report, emitSyncOnCreate = true).also { pc = it } },
        )

        // 1. Synchronous callbacks during factory.create() must not lose pcId events before DirectPcCreatedEvent
        executor.pcCreate(DirectPcCreateEffect.newBuilder().setPcId(10L).setMachineId("m-1").setRoundId(100L).setUse(1).setGeneration(5L).build())
        assertTrue(events.any { it.hasDirectPcState() && it.directPcState.pcId == 10L })
        assertTrue(events.any { it.hasDirectPcLocalCandidate() && it.directPcLocalCandidate.pcId == 10L })
        assertTrue(events.any { it.hasDirectPcCreated() && it.directPcCreated.pcId == 10L && it.directPcCreated.ok })

        // 3. Describe / SetRemote / AddCandidates / Confirm / DcReadStats
        executor.pcDescribe(DirectPcDescribeEffect.newBuilder().setPcId(10L).setOffer(true).build())
        assertTrue(events.last().directPcDescription.ok)

        val validRemoteSdp = "v=0\r\na=fingerprint:sha-256 00\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n"
            .toByteArray(StandardCharsets.UTF_8)
        executor.pcSetRemote(DirectPcSetRemoteEffect.newBuilder().setPcId(10L).setOffer(false).setSdp(ByteString.copyFrom(validRemoteSdp)).build())
        assertTrue(events.last().directPcRemoteSet.ok)

        executor.pcAddCandidates(
            DirectPcAddCandidatesEffect.newBuilder()
                .setPcId(10L)
                .addCandidates(DirectIceCandidate.newBuilder().setLine(ByteString.copyFromUtf8("cand")).setMid(ByteString.copyFromUtf8("0")))
                .build(),
        )
        assertTrue(events.last().directPcCandidatesAdded.ok)

        pc!!.channels.getValue(0).open()
        executor.pcConfirm(DirectPcConfirmEffect.newBuilder().setPcId(10L).setMachineId("m-1").setUse(1).build())

        executor.dcReadStats(DirectDcReadStatsEffect.newBuilder().setPcId(10L).build())
        val statsEv = events.last().directDcStats
        assertEquals(10L, statsEv.pcId)
        assertEquals(DirectCandFamily.DIRECT_CAND_FAMILY_V6, statsEv.report.pairsList[0].localFamily)

        // 4. sendRpc + response matching + pushed event forwarding
        val reqHdr = Header.newBuilder().setMachineId("m-1").setSender("client").setRequestId("req-1").setKind(PayloadKind.PAYLOAD_KIND_COMMAND).build()
        val reqBytes = Envelope.newBuilder().setHeader(reqHdr.toByteString()).build().toByteArray()
        val rpcDeferred = async { executor.sendRpc(10L, "req-1", reqBytes, 5_000L) {} }
        val dataCh = pc!!.channels.getValue(0)
        while (dataCh.sent.isEmpty()) kotlinx.coroutines.delay(5)
        val respEnv = Envelope.newBuilder()
            .setHeader(Header.newBuilder().setMachineId("m-1").setSender("m-1").setRequestId("req-1").build().toByteString())
            .build()
        val respFrag = Fragment.newBuilder()
            .setMessageId(ByteString.copyFrom(ByteArray(16) { 1 }))
            .setIndex(0)
            .setCount(1)
            .setCiphertextLength(respEnv.toByteArray().size)
            .setCiphertextSha256(ByteString.copyFrom(sha256Bytes(respEnv.toByteArray())))
            .setPathGeneration(5L)
            .setFragment(respEnv.toByteString())
            .build()
        dataCh.deliver(respFrag.toByteArray())
        assertEquals(respEnv.toByteArray().toList(), rpcDeferred.await().toByteArray().toList())

        val evEnv = Envelope.newBuilder()
            .setHeader(Header.newBuilder().setMachineId("m-1").setSender("m-1").setRequestId("ev-1").setKind(PayloadKind.PAYLOAD_KIND_EVENT).build().toByteString())
            .build()
        val evFrag = Fragment.newBuilder()
            .setMessageId(ByteString.copyFrom(ByteArray(16) { 2 }))
            .setIndex(0)
            .setCount(1)
            .setCiphertextLength(evEnv.toByteArray().size)
            .setCiphertextSha256(ByteString.copyFrom(sha256Bytes(evEnv.toByteArray())))
            .setPathGeneration(5L)
            .setFragment(evEnv.toByteString())
            .build()
        dataCh.deliver(evFrag.toByteArray())
        kotlinx.coroutines.withTimeout(2_000L) {
            while (pushed.isEmpty()) kotlinx.coroutines.delay(5)
        }
        assertEquals(listOf("ev-1"), pushed.toList())

        // 5. pcClose rejects in-flight RPC without emitting unexpected DirectPcClosedEvent
        val pendingRpc = async { runCatching { executor.sendRpc(10L, "req-2", reqBytes, 5_000L) {} } }
        while (dataCh.sent.size < 2) kotlinx.coroutines.delay(5)
        val countBeforeClose = events.size
        executor.pcClose(DirectPcCloseEffect.newBuilder().setPcId(10L).setReason("done").build())
        kotlinx.coroutines.withTimeout(2_000L) {
            while (!pc!!.closed) kotlinx.coroutines.delay(5)
        }
        assertTrue(pc!!.closed)
        assertTrue(pendingRpc.await().exceptionOrNull() is PathChanged)
        assertFalse(events.drop(countBeforeClose).any { it.hasDirectPcClosed() })

        executor.close()
    }

    @Test
    fun replacedPcLateCallbacksAreIgnored() = runBlocking {
        val events = CopyOnWriteArrayList<CoreEvent>()
        val createdPcs = ArrayList<Pair<StubPeerConnection, RtcPeerListener>>()
        val executor = DirectExecutor(
            emit = { events.add(it) },
            onPushedEvent = { _, _, _, _, _ -> },
            factory = RtcFactory { _, l ->
                StubPeerConnection(l, DirectStatsReport.getDefaultInstance()).also { createdPcs.add(it to l) }
            },
        )
        try {
            executor.pcCreate(DirectPcCreateEffect.newBuilder().setPcId(10L).setMachineId("m-1").setRoundId(100L).setUse(0).setGeneration(1L).build())
            executor.pcCreate(DirectPcCreateEffect.newBuilder().setPcId(10L).setMachineId("m-1").setRoundId(101L).setUse(0).setGeneration(2L).build())
            assertEquals(2, createdPcs.size)
            val (pc1, listener1) = createdPcs[0]
            val beforeLate = events.size

            // Fire late callbacks on replaced pc1 -> zero events should be emitted.
            listener1.onIceCandidate(RtcIceCandidate("candidate:9 1 udp 1 192.0.2.9 9999 typ host", "0", 0, false))
            listener1.onConnectionStateChange(RtcPeerState.FAILED)
            pc1.channels.getValue(0).close()
            pc1.channels.getValue(0).open()
            kotlinx.coroutines.delay(50)
            assertEquals(beforeLate, events.size)

            // Replacement pc2 still handles describe normally.
            executor.pcDescribe(DirectPcDescribeEffect.newBuilder().setPcId(10L).setOffer(true).build())
            assertTrue(events.drop(beforeLate).any { it.hasDirectPcDescription() && it.directPcDescription.pcId == 10L && it.directPcDescription.ok })
        } finally {
            executor.close()
        }
    }

    @Test
    fun remoteStartPrewarmsRtcFactoryOffCallerThread() = runBlocking {
        val callerThread = Thread.currentThread()
        val prewarmedOn = CompletableDeferred<Thread>()
        val account = io.github.lewin671.remoteagent.core.Account(
            server = "https://example.com",
            root = ByteArray(32) { 1 },
            deviceSeed = ByteArray(32) { 2 },
            deviceId = "0123456789abcdef0123456789abcdef",
        )
        val remote = io.github.lewin671.remoteagent.core.Remote(
            account = account,
            http = okhttp3.OkHttpClient(),
            rtcFactory = object : RtcFactory {
                override fun create(stunUrls: List<String>, listener: RtcPeerListener): RtcPeerConnection =
                    error("unused")

                override fun prewarm() {
                    prewarmedOn.complete(Thread.currentThread())
                }
            },
        )
        try {
            remote.start()
            val workerThread = kotlinx.coroutines.withTimeout(3_000L) { prewarmedOn.await() }
            assertTrue(workerThread !== callerThread)
        } finally {
            remote.close()
        }
    }

    private fun sha256Bytes(data: ByteArray): ByteArray =
        java.security.MessageDigest.getInstance("SHA-256").digest(data)
}
