package io.github.lewin671.remoteagent.transport

import io.github.lewin671.remoteagent.core.transport.RtcChannelState
import io.github.lewin671.remoteagent.core.transport.RtcDataChannelListener
import io.github.lewin671.remoteagent.core.transport.RtcIceCandidate
import io.github.lewin671.remoteagent.core.transport.RtcPeerListener
import io.github.lewin671.remoteagent.core.transport.RtcPeerState
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.webrtc.AddIceObserver
import org.webrtc.DataChannel
import org.webrtc.IceCandidate
import org.webrtc.MediaConstraints
import org.webrtc.PeerConnection
import org.webrtc.RTCStats
import org.webrtc.RTCStatsCollectorCallback
import org.webrtc.RTCStatsReport
import org.webrtc.SdpObserver
import org.webrtc.SessionDescription
import java.nio.ByteBuffer
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference

class AndroidRtcAdapterTest {
    private val noopPeerListener = object : RtcPeerListener {
        override fun onIceCandidate(candidate: RtcIceCandidate) {}
        override fun onIceGatheringComplete() {}
        override fun onConnectionStateChange(state: RtcPeerState) {}
        override fun onUnexpectedDataChannel() {}
    }

    private val noopDcListener = object : RtcDataChannelListener {
        override fun onStateChange() {}
        override fun onBufferedAmountChange(previousAmount: Long) {}
        override fun onMessage(data: ByteArray) {}
    }

    private class GuardedNativeDataChannel(
        private val uafErrors: CopyOnWriteArrayList<String>,
        private val onSendEntered: (() -> Unit)? = null,
        private val onUnregister: (() -> Unit)? = null,
    ) : NativeDataChannel {
        val disposed = AtomicBoolean(false)
        val inFlight = AtomicInteger(0)
        val sentBuffers = CopyOnWriteArrayList<ByteBuffer>()
        @Volatile var observer: DataChannel.Observer? = null

        private inline fun <T> guard(op: String, block: () -> T): T {
            inFlight.incrementAndGet()
            try {
                if (disposed.get()) {
                    uafErrors.add("dc.$op called after dispose")
                    throw IllegalStateException("UAF dc.$op")
                }
                return block()
            } finally {
                inFlight.decrementAndGet()
            }
        }

        override fun registerObserver(observer: DataChannel.Observer) = guard("registerObserver") {
            this.observer = observer
        }

        override fun unregisterObserver() = guard("unregisterObserver") {
            onUnregister?.invoke()
            this.observer = null
        }

        override fun state(): DataChannel.State = guard("state") { DataChannel.State.OPEN }

        override fun bufferedAmount(): Long = guard("bufferedAmount") { 0L }

        override fun send(buffer: DataChannel.Buffer): Boolean = guard("send") {
            onSendEntered?.invoke()
            sentBuffers.add(buffer.data)
            true
        }

        override fun close() = guard("close") {}

        override fun dispose() {
            if (inFlight.get() > 0) {
                uafErrors.add("dc.dispose ran concurrently with ${inFlight.get()} JNI calls")
            }
            if (!disposed.compareAndSet(false, true)) {
                uafErrors.add("dc.dispose called twice")
            }
            if (inFlight.get() > 0) {
                uafErrors.add("dc.dispose finished while ${inFlight.get()} JNI calls were still in-flight")
            }
        }
    }

    private class GuardedNativePeer(
        private val uafErrors: CopyOnWriteArrayList<String> = CopyOnWriteArrayList(),
        private val onSendEntered: (() -> Unit)? = null,
        private val onUnregister: (() -> Unit)? = null,
    ) : NativePeer {
        val disposed = AtomicBoolean(false)
        val inFlight = AtomicInteger(0)
        val addedCandidates = CopyOnWriteArrayList<IceCandidate>()
        val createdChannels = CopyOnWriteArrayList<GuardedNativeDataChannel>()
        @Volatile var autoCompleteCallbacks = false
        @Volatile var delayJniMs = 0L
        @Volatile var statsReport: RTCStatsReport? = null

        private inline fun <T> guard(op: String, block: () -> T): T {
            inFlight.incrementAndGet()
            try {
                if (disposed.get()) {
                    uafErrors.add("pc.$op called after dispose")
                    throw IllegalStateException("UAF pc.$op")
                }
                val d = delayJniMs
                if (d > 0) Thread.sleep(d)
                val res = block()
                if (disposed.get()) {
                    uafErrors.add("pc.$op overlapped with dispose")
                }
                return res
            } finally {
                inFlight.decrementAndGet()
            }
        }

        override fun createDataChannel(label: String, init: DataChannel.Init): NativeDataChannel =
            guard("createDataChannel") {
                GuardedNativeDataChannel(uafErrors, onSendEntered, onUnregister).also { createdChannels.add(it) }
            }

        override fun createOffer(observer: SdpObserver, constraints: MediaConstraints) = guard("createOffer") {
            if (autoCompleteCallbacks) observer.onCreateSuccess(SessionDescription(SessionDescription.Type.OFFER, "v=0\r\n"))
        }

        override fun createAnswer(observer: SdpObserver, constraints: MediaConstraints) = guard("createAnswer") {
            if (autoCompleteCallbacks) observer.onCreateSuccess(SessionDescription(SessionDescription.Type.ANSWER, "v=0\r\n"))
        }

        override fun setLocalDescription(observer: SdpObserver, sdp: SessionDescription) = guard("setLocalDescription") {
            if (autoCompleteCallbacks) observer.onSetSuccess()
        }

        override fun setRemoteDescription(observer: SdpObserver, sdp: SessionDescription) = guard("setRemoteDescription") {
            if (autoCompleteCallbacks) observer.onSetSuccess()
        }

        override fun addIceCandidate(candidate: IceCandidate, observer: AddIceObserver) = guard("addIceCandidate") {
            addedCandidates.add(candidate)
            if (autoCompleteCallbacks) observer.onAddSuccess()
        }

        override fun iceConnectionState(): PeerConnection.IceConnectionState = guard("iceConnectionState") {
            PeerConnection.IceConnectionState.CONNECTED
        }

        override fun getStats(callback: RTCStatsCollectorCallback) = guard("getStats") {
            statsReport?.let { callback.onStatsDelivered(it) }
        }

        override fun close() = guard("close") {}

        override fun dispose() {
            if (inFlight.get() > 0) {
                uafErrors.add("pc.dispose ran concurrently with ${inFlight.get()} JNI calls")
            }
            if (!disposed.compareAndSet(false, true)) {
                uafErrors.add("pc.dispose called twice")
            }
            if (inFlight.get() > 0) {
                uafErrors.add("pc.dispose finished while ${inFlight.get()} JNI calls were still in-flight")
            }
        }
    }

    @Test
    fun readStatsAfterCloseDoesNotInvokeNativeIceConnectionState() = runBlocking {
        val errors = CopyOnWriteArrayList<String>()
        val nativePeer = GuardedNativePeer(errors)
        val pc = AndroidRtcPeerConnection(noopPeerListener, statsTimeoutMs = 100L) { nativePeer }
        pc.close()
        val report = pc.readStats()
        assertEquals("", report.iceState)
        assertTrue("expected no post-dispose JNI calls, got: $errors", errors.isEmpty())
    }

    @Test
    fun concurrentJniCallsAndCloseNeverOverlapWithDispose() {
        val errors = CopyOnWriteArrayList<String>()
        val inSend = CountDownLatch(1)
        val releaseSend = CountDownLatch(1)
        val nativePeer = GuardedNativePeer(
            uafErrors = errors,
            onSendEntered = {
                inSend.countDown()
                releaseSend.await(2, TimeUnit.SECONDS)
            },
        )
        val pc = AndroidRtcPeerConnection(noopPeerListener, statsTimeoutMs = 100L) { nativePeer }
        val dc = pc.createDataChannel("remote-agent", 0, ordered = true, maxRetransmits = null, noopDcListener)

        val exec = Executors.newFixedThreadPool(3)
        try {
            val sendFuture = exec.submit { dc.send(byteArrayOf(1, 2, 3), true) }
            assertTrue("send did not enter native JNI", inSend.await(2, TimeUnit.SECONDS))
            val closeEntered = CountDownLatch(1)
            val closeFuture = exec.submit {
                closeEntered.countDown()
                pc.close()
            }
            assertTrue(closeEntered.await(2, TimeUnit.SECONDS))
            // Give close() a moment to try to dispose while send() is still inside JNI.
            Thread.sleep(40)
            releaseSend.countDown()
            sendFuture.get(2, TimeUnit.SECONDS)
            closeFuture.get(2, TimeUnit.SECONDS)
        } finally {
            releaseSend.countDown()
            exec.shutdownNow()
        }
        assertTrue("native UAF detected: $errors", errors.isEmpty())
    }

    @Test
    fun signalingCallbackSendDoesNotDeadlockWithCloseBlockingOnSignalingThread() {
        val errors = CopyOnWriteArrayList<String>()
        val signalingThread = Executors.newSingleThreadExecutor()
        val inOnMessage = CountDownLatch(1)
        val closeStartedUnregister = CountDownLatch(1)
        val dcRef = AtomicReference<io.github.lewin671.remoteagent.core.transport.RtcDataChannel>()
        val nativePeer = GuardedNativePeer(
            uafErrors = errors,
            onUnregister = {
                // Simulates libwebrtc BlockingCall onto the signaling thread during dc.unregisterObserver().
                closeStartedUnregister.countDown()
                signalingThread.submit {}.get(2, TimeUnit.SECONDS)
            },
        )
        val pc = AndroidRtcPeerConnection(noopPeerListener, statsTimeoutMs = 100L) { nativePeer }
        val dc = pc.createDataChannel(
            "remote-agent",
            1,
            ordered = false,
            maxRetransmits = 0,
            object : RtcDataChannelListener {
                override fun onStateChange() {}
                override fun onBufferedAmountChange(previousAmount: Long) {}
                override fun onMessage(data: ByteArray) {
                    inOnMessage.countDown()
                    // Wait until close() is holding writeLock and blocked waiting on signalingThread.
                    closeStartedUnregister.await(2, TimeUnit.SECONDS)
                    // Must not deadlock trying to acquire readLock on signalingThread.
                    dcRef.get().send(byteArrayOf(1), true)
                }
            },
        )
        dcRef.set(dc)
        val nativeDc = nativePeer.createdChannels.single()

        val closer = Executors.newSingleThreadExecutor()
        try {
            val sigFuture = signalingThread.submit {
                nativeDc.observer?.onMessage(DataChannel.Buffer(ByteBuffer.wrap(byteArrayOf(0)), true))
            }
            assertTrue(inOnMessage.await(2, TimeUnit.SECONDS))
            val closeFuture = closer.submit { pc.close() }
            closeFuture.get(2, TimeUnit.SECONDS)
            sigFuture.get(2, TimeUnit.SECONDS)
        } finally {
            closer.shutdownNow()
            signalingThread.shutdownNow()
        }
        assertTrue("native UAF detected: $errors", errors.isEmpty())
    }

    @Test
    fun closeWakesAllPendingSuspendOperationsWithoutHanging() = runBlocking<Unit> {
        val nativePeer = GuardedNativePeer()
        val pc = AndroidRtcPeerConnection(noopPeerListener, statsTimeoutMs = 5_000L) { nativePeer }

        val offer = async(Dispatchers.Default) { runCatching { pc.createOffer(false) } }
        val answer = async(Dispatchers.Default) { runCatching { pc.createAnswer() } }
        val setLocal = async(Dispatchers.Default) { runCatching { pc.setLocalDescription(true, "v=0\r\n") } }
        val setRemote = async(Dispatchers.Default) { runCatching { pc.setRemoteDescription(false, "v=0\r\n") } }
        val addCand = async(Dispatchers.Default) { runCatching { pc.addIceCandidate("candidate:1 1 udp 1 192.0.2.1 9999 typ host", "0", 0, "ufrag1") } }
        val stats = async(Dispatchers.Default) { pc.readStats() }

        // Wait briefly for all coroutines to suspend inside native callbacks.
        kotlinx.coroutines.delay(50)
        pc.close()

        withTimeout(1_000L) {
            assertTrue(offer.await().isFailure)
            assertTrue(answer.await().isFailure)
            assertTrue(setLocal.await().isFailure)
            assertTrue(setRemote.await().isFailure)
            assertTrue(addCand.await().isFailure)
            stats.await()
        }
    }

    @Test
    fun readStatsTimesOutWhenNativeCallbackNeverFires() = runBlocking {
        val nativePeer = GuardedNativePeer()
        val pc = AndroidRtcPeerConnection(noopPeerListener, statsTimeoutMs = 60L) { nativePeer }
        try {
            val report = withTimeout(1_000L) { pc.readStats() }
            assertEquals("connected", report.iceState)
            assertEquals(0, report.pairsCount)
        } finally {
            pc.close()
        }
    }

    @Test
    fun addIceCandidateAppendsUsernameFragmentWhenMissing() = runBlocking {
        val nativePeer = GuardedNativePeer().apply { autoCompleteCallbacks = true }
        val pc = AndroidRtcPeerConnection(noopPeerListener) { nativePeer }
        try {
            pc.addIceCandidate("candidate:1 1 udp 1 192.0.2.1 9999 typ host", "0", 0, "abcd")
            pc.addIceCandidate("candidate:2 1 udp 1 192.0.2.2 9999 typ host ufrag existing", "0", 0, "abcd")
            assertEquals(2, nativePeer.addedCandidates.size)
            assertEquals("candidate:1 1 udp 1 192.0.2.1 9999 typ host ufrag abcd", nativePeer.addedCandidates[0].sdp)
            assertEquals("candidate:2 1 udp 1 192.0.2.2 9999 typ host ufrag existing", nativePeer.addedCandidates[1].sdp)
        } finally {
            pc.close()
        }
    }

    @Test
    fun dataChannelSendWrapsByteArrayWithoutExtraCopy() {
        val nativePeer = GuardedNativePeer()
        val pc = AndroidRtcPeerConnection(noopPeerListener) { nativePeer }
        try {
            val dc = pc.createDataChannel("remote-agent", 0, ordered = true, maxRetransmits = null, noopDcListener)
            val payload = byteArrayOf(10, 20, 30, 40)
            assertTrue(dc.send(payload, true))
            val nativeDc = nativePeer.createdChannels.single()
            assertEquals(1, nativeDc.sentBuffers.size)
            assertSame("expected ByteBuffer to wrap payload without extra copy", payload, nativeDc.sentBuffers[0].array())
        } finally {
            pc.close()
        }
    }

    @Test
    fun createOfferAndSetDescriptionsAfterCloseFailWithoutTouchingNative() = runBlocking {
        val errors = CopyOnWriteArrayList<String>()
        val nativePeer = GuardedNativePeer(errors)
        val pc = AndroidRtcPeerConnection(noopPeerListener) { nativePeer }
        pc.close()
        assertFalse(runCatching { pc.createOffer(false) }.isSuccess)
        assertFalse(runCatching { pc.createAnswer() }.isSuccess)
        assertFalse(runCatching { pc.setLocalDescription(true, "v=0\r\n") }.isSuccess)
        assertFalse(runCatching { pc.setRemoteDescription(false, "v=0\r\n") }.isSuccess)
        assertFalse(runCatching { pc.addIceCandidate("candidate:1 1 udp 1 192.0.2.1 9999 typ host", "0", 0, "") }.isSuccess)
        assertEquals(RtcChannelState.CLOSED, runCatching { pc.createDataChannel("x", 0, true, null, noopDcListener).state() }.getOrDefault(RtcChannelState.CLOSED))
        assertTrue("expected no post-dispose JNI calls, got: $errors", errors.isEmpty())
    }

    @Test
    fun directPreferenceStoreSetEnabledUsesApplyInsteadOfSyncCommit() {
        var commitCalls = 0
        var applyCalls = 0
        val data = LinkedHashMap<String, Any?>()
        val fakePrefs = object : android.content.SharedPreferences {
            override fun getAll(): MutableMap<String, *> = LinkedHashMap(data)
            override fun getString(key: String?, defValue: String?): String? = data[key] as? String ?: defValue
            override fun getStringSet(key: String?, defValues: MutableSet<String>?): MutableSet<String>? = defValues
            override fun getInt(key: String?, defValue: Int): Int = data[key] as? Int ?: defValue
            override fun getLong(key: String?, defValue: Long): Long = data[key] as? Long ?: defValue
            override fun getFloat(key: String?, defValue: Float): Float = data[key] as? Float ?: defValue
            override fun getBoolean(key: String?, defValue: Boolean): Boolean = data[key] as? Boolean ?: defValue
            override fun contains(key: String?): Boolean = data.containsKey(key)
            override fun registerOnSharedPreferenceChangeListener(listener: android.content.SharedPreferences.OnSharedPreferenceChangeListener?) {}
            override fun unregisterOnSharedPreferenceChangeListener(listener: android.content.SharedPreferences.OnSharedPreferenceChangeListener?) {}
            override fun edit(): android.content.SharedPreferences.Editor {
                val staged = LinkedHashMap<String, Any?>()
                return object : android.content.SharedPreferences.Editor {
                    override fun putString(key: String?, value: String?) = apply { if (key != null) staged[key] = value }
                    override fun putStringSet(key: String?, values: MutableSet<String>?) = apply { if (key != null) staged[key] = values }
                    override fun putInt(key: String?, value: Int) = apply { if (key != null) staged[key] = value }
                    override fun putLong(key: String?, value: Long) = apply { if (key != null) staged[key] = value }
                    override fun putFloat(key: String?, value: Float) = apply { if (key != null) staged[key] = value }
                    override fun putBoolean(key: String?, value: Boolean) = apply { if (key != null) staged[key] = value }
                    override fun remove(key: String?) = apply { if (key != null) staged[key] = null }
                    override fun clear() = apply { staged.clear(); data.clear() }
                    override fun commit(): Boolean {
                        commitCalls++
                        data.putAll(staged)
                        return true
                    }
                    override fun apply() {
                        applyCalls++
                        data.putAll(staged)
                    }
                }
            }
        }
        val store = SharedPrefsDirectPreferenceStore(fakePrefs)
        store.setEnabled("https://example.com", "dev-1", "mac-1", false)
        assertEquals(0, commitCalls)
        assertEquals(1, applyCalls)
        assertFalse(store.isEnabled("https://example.com", "dev-1", "mac-1"))
    }

    @Test
    fun readStatsMapsSelectedCandidatePairIdWithoutLeakingAddresses() = runBlocking {
        val nativePeer = GuardedNativePeer()
        val statsMap = linkedMapOf(
            "lc1" to RTCStats(0L, "local-candidate", "lc1", mapOf("candidateType" to "host", "address" to "2001:db8:1::5", "port" to 54321)),
            "rc1" to RTCStats(0L, "remote-candidate", "rc1", mapOf("candidateType" to "prflx", "address" to "2001:db8:2::9", "port" to 41478)),
            "lc2" to RTCStats(0L, "local-candidate", "lc2", mapOf("candidateType" to "srflx", "address" to "198.51.100.7", "port" to 50000)),
            "rc2" to RTCStats(0L, "remote-candidate", "rc2", mapOf("candidateType" to "host", "address" to "192.168.1.20", "port" to 41478)),
            "cp1" to RTCStats(0L, "candidate-pair", "cp1", mapOf("localCandidateId" to "lc1", "remoteCandidateId" to "rc1", "nominated" to true, "state" to "succeeded", "requestsSent" to 4, "responsesReceived" to 4, "currentRoundTripTime" to 0.011)),
            "cp2" to RTCStats(0L, "candidate-pair", "cp2", mapOf("localCandidateId" to "lc2", "remoteCandidateId" to "rc2", "nominated" to false, "state" to "failed", "requestsSent" to 2, "responsesReceived" to 0)),
            "tr1" to RTCStats(0L, "transport", "tr1", mapOf("dtlsState" to "connected", "selectedCandidatePairId" to "cp1")),
            "dc0" to RTCStats(0L, "data-channel", "dc0", mapOf("dataChannelIdentifier" to 0, "state" to "open")),
        )
        nativePeer.statsReport = RTCStatsReport(0L, statsMap)
        val pc = AndroidRtcPeerConnection(noopPeerListener) { nativePeer }
        try {
            val report = pc.readStats()
            assertEquals("connected", report.dtlsState)
            assertEquals("open", report.dcState)
            assertEquals(2, report.pairsCount)
            val selected = report.pairsList.single { it.selected }
            assertEquals(io.github.lewin671.remoteagent.core.proto.DirectCandFamily.DIRECT_CAND_FAMILY_V6, selected.localFamily)
            assertEquals(io.github.lewin671.remoteagent.core.proto.DirectCandType.DIRECT_CAND_TYPE_HOST, selected.localType)
            assertEquals(io.github.lewin671.remoteagent.core.proto.DirectCandFamily.DIRECT_CAND_FAMILY_V6, selected.remoteFamily)
            assertEquals(io.github.lewin671.remoteagent.core.proto.DirectCandType.DIRECT_CAND_TYPE_PRFLX, selected.remoteType)
            assertFalse(report.pairsList.single { !it.selected }.selected)
            val dump = report.toString()
            for (secret in listOf("2001:db8", "198.51.100.7", "192.168.1.20", "54321", "41478")) {
                assertFalse("report must not leak $secret", dump.contains(secret))
            }
        } finally {
            pc.close()
        }
    }
}
