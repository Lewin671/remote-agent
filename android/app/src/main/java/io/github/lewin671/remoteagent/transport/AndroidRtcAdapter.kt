package io.github.lewin671.remoteagent.transport

import android.content.Context
import android.content.SharedPreferences
import io.github.lewin671.remoteagent.core.proto.DirectCandFamily
import io.github.lewin671.remoteagent.core.proto.DirectCandType
import io.github.lewin671.remoteagent.core.proto.DirectStatsPair
import io.github.lewin671.remoteagent.core.proto.DirectStatsReport
import io.github.lewin671.remoteagent.core.transport.DirectPreferenceStore
import io.github.lewin671.remoteagent.core.transport.RtcChannelState
import io.github.lewin671.remoteagent.core.transport.RtcDataChannel
import io.github.lewin671.remoteagent.core.transport.RtcDataChannelListener
import io.github.lewin671.remoteagent.core.transport.RtcFactory
import io.github.lewin671.remoteagent.core.transport.RtcIceCandidate
import io.github.lewin671.remoteagent.core.transport.RtcPeerConnection
import io.github.lewin671.remoteagent.core.transport.RtcPeerListener
import io.github.lewin671.remoteagent.core.transport.RtcPeerState
import kotlinx.coroutines.CancellableContinuation
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withTimeoutOrNull
import org.webrtc.AddIceObserver
import org.webrtc.DataChannel
import org.webrtc.IceCandidate
import org.webrtc.IceCandidateErrorEvent
import org.webrtc.Logging
import org.webrtc.MediaConstraints
import org.webrtc.MediaStream
import org.webrtc.PeerConnection
import org.webrtc.PeerConnectionFactory
import org.webrtc.RTCStats
import org.webrtc.RTCStatsCollectorCallback
import org.webrtc.RtpReceiver
import org.webrtc.RtpTransceiver
import org.webrtc.SdpObserver
import org.webrtc.SessionDescription
import org.webrtc.audio.JavaAudioDeviceModule
import java.nio.ByteBuffer
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.locks.ReentrantReadWriteLock
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

private val IPV4_REGEX = Regex("""^\d{1,3}(?:\.\d{1,3}){3}$""")
private val V4_MAPPED_REGEX = Regex("""^::ffff:\d{1,3}(?:\.\d{1,3}){3}$""")

private fun statsCandType(value: Any?): DirectCandType = when (value as? String) {
    "host" -> DirectCandType.DIRECT_CAND_TYPE_HOST
    "srflx" -> DirectCandType.DIRECT_CAND_TYPE_SRFLX
    "prflx" -> DirectCandType.DIRECT_CAND_TYPE_PRFLX
    "relay" -> DirectCandType.DIRECT_CAND_TYPE_RELAY
    null -> DirectCandType.DIRECT_CAND_TYPE_UNSPECIFIED
    else -> DirectCandType.DIRECT_CAND_TYPE_OTHER
}

private fun statsCandFamily(address: Any?): DirectCandFamily {
    val raw = (address as? String)?.trim()?.lowercase()?.substringBefore('%') ?: return DirectCandFamily.DIRECT_CAND_FAMILY_UNSPECIFIED
    if (raw.isEmpty() || raw.endsWith(".local")) return DirectCandFamily.DIRECT_CAND_FAMILY_UNSPECIFIED
    if (IPV4_REGEX.matches(raw) || V4_MAPPED_REGEX.matches(raw)) return DirectCandFamily.DIRECT_CAND_FAMILY_V4
    return if (':' in raw) DirectCandFamily.DIRECT_CAND_FAMILY_V6 else DirectCandFamily.DIRECT_CAND_FAMILY_UNSPECIFIED
}

private fun statsInt(value: Any?): Int = (value as? Number)?.toInt() ?: 0

internal fun normalizeCandidateSdp(candidate: String, usernameFragment: String): String {
    val ufrag = usernameFragment.trim()
    if (ufrag.isEmpty() || ufrag.any { it.isWhitespace() }) return candidate
    val trimmed = candidate.trimEnd()
    if (trimmed.isEmpty()) return candidate
    val tokens = trimmed.split(' ')
    if (tokens.any { it == "ufrag" }) return candidate
    return buildString(trimmed.length + ufrag.length + 7) {
        append(trimmed)
        append(' ')
        append("ufrag")
        append(' ')
        append(ufrag)
    }
}

private const val DIRECT_PREFS_NAME = "direct-path"

internal class SharedPrefsDirectPreferenceStore internal constructor(
    private val prefs: SharedPreferences,
) : DirectPreferenceStore {
    constructor(context: Context) : this(context.getSharedPreferences(DIRECT_PREFS_NAME, Context.MODE_PRIVATE))

    override fun isEnabled(server: String, selfDeviceId: String, machineId: String): Boolean =
        prefs.getBoolean("$server|$selfDeviceId|$machineId", true)

    override fun setEnabled(server: String, selfDeviceId: String, machineId: String, enabled: Boolean) {
        // apply() updates the in-memory SharedPreferences map immediately and persists off-thread,
        // avoiding synchronous disk fsync on CoreHost's actor thread.
        prefs.edit().putBoolean("$server|$selfDeviceId|$machineId", enabled).apply()
    }

    override fun allEnabled(server: String, selfDeviceId: String): Map<String, Boolean> {
        val prefix = "$server|$selfDeviceId|"
        val out = LinkedHashMap<String, Boolean>()
        for ((k, v) in prefs.all) {
            if (k.startsWith(prefix) && v is Boolean) {
                val id = k.removePrefix(prefix)
                if (id.isNotEmpty()) out[id] = v
            }
        }
        return out
    }

    fun clear() {
        prefs.edit().clear().apply()
    }
}

/** DataChannel-only PeerConnection factory; no audio/video devices or tracks are created. */
internal class AndroidRtcFactory(context: Context) : RtcFactory {
    private val appContext = context.applicationContext

    private fun factory(): PeerConnectionFactory {
        existing?.let { return it }
        synchronized(initLock) {
            existing?.let { return it }
            PeerConnectionFactory.initialize(
                PeerConnectionFactory.InitializationOptions.builder(appContext)
                    .setInjectableLogger({ _, _, _ -> }, Logging.Severity.LS_NONE)
                    .createInitializationOptions(),
            )
            // webrtc-sdk M150's native WebRtcVoiceEngine enforces RTC_CHECK(adm_) during
            // CreatePeerConnectionFactory, so a zero-pointer stub ADM cannot be used; configure
            // JavaAudioDeviceModule in minimal data-only mode (no HW AEC/NS or volume logger)
            // and release our caller reference once the factory retains its own scoped_refptr.
            val adm = JavaAudioDeviceModule.builder(appContext)
                .setUseHardwareAcousticEchoCanceler(false)
                .setUseHardwareNoiseSuppressor(false)
                .setEnableVolumeLogger(false)
                .createAudioDeviceModule()
            return try {
                PeerConnectionFactory.builder()
                    .setAudioDeviceModule(adm)
                    .setOptions(PeerConnectionFactory.Options())
                    .createPeerConnectionFactory()
                    .also { existing = it }
            } finally {
                adm.release()
            }
        }
    }

    override fun prewarm() {
        factory()
    }

    override fun create(stunUrls: List<String>, listener: RtcPeerListener): RtcPeerConnection {
        val iceServers = stunUrls.map { PeerConnection.IceServer.builder(it).createIceServer() }
        val config = PeerConnection.RTCConfiguration(iceServers).apply {
            sdpSemantics = PeerConnection.SdpSemantics.UNIFIED_PLAN
            iceTransportsType = PeerConnection.IceTransportsType.ALL
        }
        return AndroidRtcPeerConnection(listener) { observer ->
            RealNativePeer(
                checkNotNull(factory().createPeerConnection(config, observer)) {
                    "peer_connection_null"
                },
            )
        }
    }

    companion object {
        private val initLock = Any()
        @Volatile private var existing: PeerConnectionFactory? = null
    }
}

internal interface NativeDataChannel {
    fun registerObserver(observer: DataChannel.Observer)
    fun unregisterObserver()
    fun state(): DataChannel.State
    fun bufferedAmount(): Long
    fun send(buffer: DataChannel.Buffer): Boolean
    fun close()
    fun dispose()
}

internal interface NativePeer {
    fun createDataChannel(label: String, init: DataChannel.Init): NativeDataChannel?
    fun createOffer(observer: SdpObserver, constraints: MediaConstraints)
    fun createAnswer(observer: SdpObserver, constraints: MediaConstraints)
    fun setLocalDescription(observer: SdpObserver, sdp: SessionDescription)
    fun setRemoteDescription(observer: SdpObserver, sdp: SessionDescription)
    fun addIceCandidate(candidate: IceCandidate, observer: AddIceObserver)
    fun iceConnectionState(): PeerConnection.IceConnectionState?
    fun getStats(callback: RTCStatsCollectorCallback)
    fun close()
    fun dispose()
}

private class RealNativeDataChannel(private val dc: DataChannel) : NativeDataChannel {
    override fun registerObserver(observer: DataChannel.Observer) = dc.registerObserver(observer)
    override fun unregisterObserver() = dc.unregisterObserver()
    override fun state(): DataChannel.State = dc.state()
    override fun bufferedAmount(): Long = dc.bufferedAmount()
    override fun send(buffer: DataChannel.Buffer): Boolean = dc.send(buffer)
    override fun close() = dc.close()
    override fun dispose() = dc.dispose()
}

private class RealNativePeer(private val pc: PeerConnection) : NativePeer {
    override fun createDataChannel(label: String, init: DataChannel.Init): NativeDataChannel? =
        pc.createDataChannel(label, init)?.let(::RealNativeDataChannel)
    override fun createOffer(observer: SdpObserver, constraints: MediaConstraints) = pc.createOffer(observer, constraints)
    override fun createAnswer(observer: SdpObserver, constraints: MediaConstraints) = pc.createAnswer(observer, constraints)
    override fun setLocalDescription(observer: SdpObserver, sdp: SessionDescription) = pc.setLocalDescription(observer, sdp)
    override fun setRemoteDescription(observer: SdpObserver, sdp: SessionDescription) = pc.setRemoteDescription(observer, sdp)
    override fun addIceCandidate(candidate: IceCandidate, observer: AddIceObserver) = pc.addIceCandidate(candidate, observer)
    override fun iceConnectionState(): PeerConnection.IceConnectionState? = pc.iceConnectionState()
    override fun getStats(callback: RTCStatsCollectorCallback) = pc.getStats(callback)
    override fun close() = pc.close()
    override fun dispose() = pc.dispose()
}

internal class AndroidRtcPeerConnection internal constructor(
    private val listener: RtcPeerListener,
    private val statsTimeoutMs: Long = DEFAULT_STATS_TIMEOUT_MS,
    createPeer: (PeerConnection.Observer) -> NativePeer,
) : RtcPeerConnection {
    // Readers use readLock().tryLock() and never block waiting for close()'s writeLock, preventing
    // AB-BA deadlocks when libwebrtc's signaling thread enters callbacks while close() runs
    // BlockingCall operations onto the signaling thread inside writeLock.
    private val nativeLock = ReentrantReadWriteLock()
    private val channelsLock = Any()
    private val channels = ArrayList<Pair<NativeDataChannel, DataChannel.Observer>>()
    private val pendingLock = Any()
    private val pending = LinkedHashSet<CancellableContinuation<*>>()
    private val closeStarted = AtomicBoolean(false)
    @Volatile private var closed = false
    private val pc: NativePeer

    init {
        val observer = object : PeerConnection.Observer {
            override fun onSignalingChange(newState: PeerConnection.SignalingState) {}
            override fun onIceConnectionChange(newState: PeerConnection.IceConnectionState) {
                if (!closed) listener.onIceConnectionStateChange(newState.name.lowercase())
            }
            override fun onIceConnectionReceivingChange(receiving: Boolean) {}

            override fun onIceGatheringChange(newState: PeerConnection.IceGatheringState) {
                if (closed) return
                listener.onIceGatheringStateChange(newState.name.lowercase())
                if (newState == PeerConnection.IceGatheringState.COMPLETE) {
                    listener.onIceGatheringComplete()
                }
            }

            override fun onIceCandidate(candidate: IceCandidate) {
                if (closed) return
                val tokens = candidate.sdp.trim().split(' ')
                val isSrflx = (1 until tokens.size).any { tokens[it - 1] == "typ" && tokens[it] == "srflx" }
                listener.onIceCandidate(
                    RtcIceCandidate(
                        candidate = candidate.sdp,
                        sdpMid = candidate.sdpMid ?: "0",
                        sdpMLineIndex = candidate.sdpMLineIndex,
                        isSrflx = isSrflx,
                    ),
                )
            }

            override fun onIceCandidateError(event: IceCandidateErrorEvent) {
                if (!closed) listener.onIceCandidateError(event.errorCode, event.address.orEmpty())
            }

            override fun onIceCandidatesRemoved(candidates: Array<out IceCandidate>) {}
            override fun onAddStream(stream: MediaStream) {}
            override fun onRemoveStream(stream: MediaStream) {}
            override fun onRenegotiationNeeded() {}
            override fun onAddTrack(receiver: RtpReceiver, mediaStreams: Array<out MediaStream>) {}
            override fun onTrack(transceiver: RtpTransceiver) {}

            override fun onDataChannel(dataChannel: DataChannel) {
                runCatching {
                    dataChannel.close()
                    dataChannel.dispose()
                }
                if (!closed) listener.onUnexpectedDataChannel()
            }

            override fun onConnectionChange(newState: PeerConnection.PeerConnectionState) {
                if (closed) return
                val mapped = when (newState) {
                    PeerConnection.PeerConnectionState.NEW -> RtcPeerState.NEW
                    PeerConnection.PeerConnectionState.CONNECTING -> RtcPeerState.CONNECTING
                    PeerConnection.PeerConnectionState.CONNECTED -> RtcPeerState.CONNECTED
                    PeerConnection.PeerConnectionState.DISCONNECTED -> RtcPeerState.DISCONNECTED
                    PeerConnection.PeerConnectionState.FAILED -> RtcPeerState.FAILED
                    PeerConnection.PeerConnectionState.CLOSED -> RtcPeerState.CLOSED
                }
                listener.onConnectionStateChange(mapped)
            }
        }
        pc = createPeer(observer)
    }

    private inline fun <T> withNativeRead(default: T, block: () -> T): T {
        if (closed || !nativeLock.readLock().tryLock()) return default
        try {
            if (closed) return default
            return block()
        } finally {
            nativeLock.readLock().unlock()
        }
    }

    private inline fun <T> withNativeReadOrThrow(block: () -> T): T {
        if (closed || !nativeLock.readLock().tryLock()) {
            throw IllegalStateException("peer_connection_closed")
        }
        try {
            if (closed) throw IllegalStateException("peer_connection_closed")
            return block()
        } finally {
            nativeLock.readLock().unlock()
        }
    }

    private suspend inline fun <T> awaitNativeCallback(
        crossinline start: (
            resolve: (T) -> Unit,
            reject: (Throwable) -> Unit,
        ) -> Unit,
    ): T = suspendCancellableCoroutine { cont ->
        val registered = synchronized(pendingLock) {
            if (closed) {
                false
            } else {
                pending.add(cont)
                true
            }
        }
        if (!registered) {
            cont.resumeWithException(IllegalStateException("peer_connection_closed"))
            return@suspendCancellableCoroutine
        }
        cont.invokeOnCancellation {
            synchronized(pendingLock) { pending.remove(cont) }
        }
        val resolve: (T) -> Unit = { value ->
            synchronized(pendingLock) { pending.remove(cont) }
            if (cont.isActive) cont.resume(value)
        }
        val reject: (Throwable) -> Unit = { err ->
            synchronized(pendingLock) { pending.remove(cont) }
            if (cont.isActive) cont.resumeWithException(err)
        }
        try {
            withNativeReadOrThrow { start(resolve, reject) }
        } catch (t: Throwable) {
            reject(t)
        }
    }

    override fun createDataChannel(
        label: String,
        id: Int,
        ordered: Boolean,
        maxRetransmits: Int?,
        listener: RtcDataChannelListener,
    ): RtcDataChannel {
        val init = DataChannel.Init().apply {
            this.id = id
            this.negotiated = true
            this.ordered = ordered
            if (maxRetransmits != null) this.maxRetransmits = maxRetransmits
        }
        val observer = object : DataChannel.Observer {
            override fun onBufferedAmountChange(previousAmount: Long) {
                if (!closed) listener.onBufferedAmountChange(previousAmount)
            }

            override fun onStateChange() {
                if (!closed) listener.onStateChange()
            }

            override fun onMessage(buffer: DataChannel.Buffer) {
                if (closed) return
                val dup = buffer.data.duplicate()
                val bytes = ByteArray(dup.remaining())
                dup.get(bytes)
                listener.onMessage(bytes)
            }
        }
        val dc = withNativeReadOrThrow {
            val created = checkNotNull(pc.createDataChannel(label, init)) { "data_channel_null" }
            try {
                created.registerObserver(observer)
                synchronized(channelsLock) { channels.add(created to observer) }
            } catch (t: Throwable) {
                runCatching { created.close() }
                runCatching { created.dispose() }
                throw t
            }
            created
        }
        return object : RtcDataChannel {
            override fun state(): RtcChannelState = withNativeRead(RtcChannelState.CLOSED) {
                when (runCatching { dc.state() }.getOrDefault(DataChannel.State.CLOSED)) {
                    DataChannel.State.CONNECTING -> RtcChannelState.CONNECTING
                    DataChannel.State.OPEN -> RtcChannelState.OPEN
                    DataChannel.State.CLOSING -> RtcChannelState.CLOSING
                    DataChannel.State.CLOSED -> RtcChannelState.CLOSED
                }
            }

            override fun bufferedAmount(): Long = withNativeRead(0L) {
                runCatching { dc.bufferedAmount() }.getOrDefault(0L)
            }

            override fun send(data: ByteArray, binary: Boolean): Boolean = withNativeRead(false) {
                // org.webrtc.DataChannel.send() synchronously copies ByteBuffer contents into a fresh
                // byte[] before invoking nativeSend, so wrapping data directly avoids a redundant copy.
                runCatching { dc.send(DataChannel.Buffer(ByteBuffer.wrap(data), binary)) }.getOrDefault(false)
            }

            override fun close() {
                withNativeRead(Unit) {
                    runCatching { dc.close() }
                }
            }
        }
    }

    override suspend fun createOffer(iceRestart: Boolean): String {
        val constraints = MediaConstraints().apply {
            if (iceRestart) mandatory.add(MediaConstraints.KeyValuePair("IceRestart", "true"))
        }
        return awaitNativeCallback { resolve, reject ->
            pc.createOffer(
                object : SdpObserver {
                    override fun onCreateSuccess(sdp: SessionDescription) = resolve(sdp.description)
                    override fun onSetSuccess() {}
                    override fun onCreateFailure(error: String) = reject(IllegalStateException(error))
                    override fun onSetFailure(error: String) = reject(IllegalStateException(error))
                },
                constraints,
            )
        }
    }

    override suspend fun createAnswer(): String = awaitNativeCallback { resolve, reject ->
        pc.createAnswer(
            object : SdpObserver {
                override fun onCreateSuccess(sdp: SessionDescription) = resolve(sdp.description)
                override fun onSetSuccess() {}
                override fun onCreateFailure(error: String) = reject(IllegalStateException(error))
                override fun onSetFailure(error: String) = reject(IllegalStateException(error))
            },
            MediaConstraints(),
        )
    }

    override suspend fun setLocalDescription(offer: Boolean, sdp: String) {
        val type = if (offer) SessionDescription.Type.OFFER else SessionDescription.Type.ANSWER
        awaitNativeCallback<Unit> { resolve, reject ->
            pc.setLocalDescription(
                object : SdpObserver {
                    override fun onCreateSuccess(sdp: SessionDescription) {}
                    override fun onSetSuccess() = resolve(Unit)
                    override fun onCreateFailure(error: String) = reject(IllegalStateException(error))
                    override fun onSetFailure(error: String) = reject(IllegalStateException(error))
                },
                SessionDescription(type, sdp),
            )
        }
    }

    override suspend fun setRemoteDescription(offer: Boolean, sdp: String) {
        val type = if (offer) SessionDescription.Type.OFFER else SessionDescription.Type.ANSWER
        awaitNativeCallback<Unit> { resolve, reject ->
            pc.setRemoteDescription(
                object : SdpObserver {
                    override fun onCreateSuccess(sdp: SessionDescription) {}
                    override fun onSetSuccess() = resolve(Unit)
                    override fun onCreateFailure(error: String) = reject(IllegalStateException(error))
                    override fun onSetFailure(error: String) = reject(IllegalStateException(error))
                },
                SessionDescription(type, sdp),
            )
        }
    }

    override suspend fun addIceCandidate(
        candidate: String,
        sdpMid: String,
        sdpMLineIndex: Int,
        usernameFragment: String,
    ) {
        val normalized = normalizeCandidateSdp(candidate, usernameFragment)
        awaitNativeCallback<Unit> { resolve, reject ->
            pc.addIceCandidate(
                IceCandidate(sdpMid, sdpMLineIndex, normalized),
                object : AddIceObserver {
                    override fun onAddSuccess() = resolve(Unit)
                    override fun onAddFailure(error: String) = reject(IllegalStateException(error))
                },
            )
        }
    }

    override suspend fun endOfCandidates() {
        // Android libwebrtc's Java API (org.webrtc.PeerConnection.addIceCandidate) does not expose an
        // end-of-candidates overload and native CreateIceCandidate rejects empty candidate strings
        // ("Candidate is null."). Core's 12s connect deadline bounds failed ICE rounds.
    }

    override suspend fun readStats(): DirectStatsReport {
        val iceState = withNativeRead("") {
            runCatching { pc.iceConnectionState()?.name?.lowercase().orEmpty() }.getOrDefault("")
        }
        if (closed) return DirectStatsReport.newBuilder().setIceState(iceState).build()
        return runCatching {
            withTimeoutOrNull(statsTimeoutMs) {
                awaitNativeCallback<DirectStatsReport> { resolve, _ ->
                    pc.getStats { report ->
                        var dtlsState = ""
                        var dcState = ""
                        val locals = HashMap<String, Pair<DirectCandType, DirectCandFamily>>()
                        val remotes = HashMap<String, Pair<DirectCandType, DirectCandFamily>>()
                        val rawPairs = ArrayList<RTCStats>()
                        for (s in report.statsMap.values) {
                            val m = s.members
                            when (s.type) {
                                "local-candidate", "remote-candidate" -> {
                                    val address = m["address"] ?: m["ip"]
                                    val info = statsCandType(m["candidateType"]) to statsCandFamily(address)
                                    if (s.type == "local-candidate") locals[s.id] = info else remotes[s.id] = info
                                }
                                "candidate-pair" -> rawPairs.add(s)
                                "transport" -> (m["dtlsState"] as? String)?.takeIf { it.isNotEmpty() }?.let { dtlsState = it }
                                "data-channel" -> {
                                    val dcId = (m["dataChannelIdentifier"] as? Number)?.toInt()
                                    val st = m["state"] as? String
                                    if ((dcId == null || dcId == 0) && !st.isNullOrEmpty()) dcState = st
                                }
                            }
                        }
                        val builder = DirectStatsReport.newBuilder()
                            .setIceState(iceState)
                            .setDtlsState(dtlsState)
                            .setDcState(dcState)
                        for (p in rawPairs) {
                            val m = p.members
                            val localInfo = locals[m["localCandidateId"] as? String]
                                ?: (DirectCandType.DIRECT_CAND_TYPE_UNSPECIFIED to DirectCandFamily.DIRECT_CAND_FAMILY_UNSPECIFIED)
                            val remoteInfo = remotes[m["remoteCandidateId"] as? String]
                                ?: (DirectCandType.DIRECT_CAND_TYPE_UNSPECIFIED to DirectCandFamily.DIRECT_CAND_FAMILY_UNSPECIFIED)
                            val rtt = (m["currentRoundTripTime"] as? Number)?.toDouble()?.takeIf { it.isFinite() }
                            val pairBuilder = DirectStatsPair.newBuilder()
                                .setLocalType(localInfo.first)
                                .setLocalFamily(localInfo.second)
                                .setRemoteType(remoteInfo.first)
                                .setRemoteFamily(remoteInfo.second)
                                .setNominated(m["nominated"] == true)
                                .setState((m["state"] as? String).orEmpty())
                                .setRequestsSent(statsInt(m["requestsSent"]))
                                .setResponsesReceived(statsInt(m["responsesReceived"]))
                            if (rtt != null) {
                                pairBuilder.setHasCurrentRtt(true).setCurrentRttSeconds(rtt)
                            }
                            builder.addPairs(pairBuilder.build())
                        }
                        resolve(builder.build())
                    }
                }
            }
        }.getOrNull() ?: DirectStatsReport.newBuilder().setIceState(iceState).build()
    }

    override fun close() {
        if (!closeStarted.compareAndSet(false, true)) return
        closed = true
        val toFail = synchronized(pendingLock) {
            pending.toList().also { pending.clear() }
        }
        val closeErr = IllegalStateException("peer_connection_closed")
        for (cont in toFail) {
            if (cont.isActive) cont.resumeWithException(closeErr)
        }
        nativeLock.writeLock().lock()
        try {
            val toDispose = synchronized(channelsLock) {
                channels.toList().also { channels.clear() }
            }
            for ((dc, _) in toDispose) {
                runCatching { dc.unregisterObserver() }
                runCatching { dc.close() }
                runCatching { dc.dispose() }
            }
            runCatching { pc.close() }
            runCatching { pc.dispose() }
        } finally {
            nativeLock.writeLock().unlock()
        }
    }

    companion object {
        internal const val DEFAULT_STATS_TIMEOUT_MS = 3_000L
    }
}
