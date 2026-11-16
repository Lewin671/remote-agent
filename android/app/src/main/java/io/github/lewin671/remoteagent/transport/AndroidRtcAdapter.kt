package io.github.lewin671.remoteagent.transport

import android.content.Context
import io.github.lewin671.remoteagent.core.transport.DirectPreferenceStore
import io.github.lewin671.remoteagent.core.transport.RtcChannelState
import io.github.lewin671.remoteagent.core.transport.RtcDataChannel
import io.github.lewin671.remoteagent.core.transport.RtcDataChannelListener
import io.github.lewin671.remoteagent.core.transport.RtcFactory
import io.github.lewin671.remoteagent.core.transport.RtcIceCandidate
import io.github.lewin671.remoteagent.core.transport.RtcPeerConnection
import io.github.lewin671.remoteagent.core.transport.RtcPeerListener
import io.github.lewin671.remoteagent.core.transport.RtcPeerState
import kotlinx.coroutines.suspendCancellableCoroutine
import org.webrtc.AddIceObserver
import org.webrtc.DataChannel
import org.webrtc.IceCandidate
import org.webrtc.Logging
import org.webrtc.MediaConstraints
import org.webrtc.MediaStream
import org.webrtc.PeerConnection
import org.webrtc.PeerConnectionFactory
import org.webrtc.RtpReceiver
import org.webrtc.RtpTransceiver
import org.webrtc.SdpObserver
import org.webrtc.SessionDescription
import java.nio.ByteBuffer
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

internal class SharedPrefsDirectPreferenceStore(context: Context) : DirectPreferenceStore {
    private val prefs = context.getSharedPreferences("direct-path", Context.MODE_PRIVATE)

    override fun isEnabled(server: String, selfDeviceId: String, machineId: String): Boolean =
        prefs.getBoolean("$server|$selfDeviceId|$machineId", true)

    override fun setEnabled(server: String, selfDeviceId: String, machineId: String, enabled: Boolean) {
        check(prefs.edit().putBoolean("$server|$selfDeviceId|$machineId", enabled).commit()) {
            "direct_pref_write_failed"
        }
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
            return PeerConnectionFactory.builder()
                .setOptions(PeerConnectionFactory.Options())
                .createPeerConnectionFactory()
                .also { existing = it }
        }
    }

    override fun create(stunUrls: List<String>, listener: RtcPeerListener): RtcPeerConnection {
        val iceServers = stunUrls.map { PeerConnection.IceServer.builder(it).createIceServer() }
        val config = PeerConnection.RTCConfiguration(iceServers).apply {
            sdpSemantics = PeerConnection.SdpSemantics.UNIFIED_PLAN
            iceTransportsType = PeerConnection.IceTransportsType.ALL
        }
        return AndroidRtcPeerConnection(factory(), config, listener)
    }

    companion object {
        private val initLock = Any()
        @Volatile private var existing: PeerConnectionFactory? = null
    }
}

private class AndroidRtcPeerConnection(
    factory: PeerConnectionFactory,
    config: PeerConnection.RTCConfiguration,
    private val listener: RtcPeerListener,
) : RtcPeerConnection {
    private val lock = Any()
    private val channels = ArrayList<Pair<DataChannel, DataChannel.Observer>>()
    @Volatile private var closed = false
    private val pc: PeerConnection

    init {
        val observer = object : PeerConnection.Observer {
            override fun onSignalingChange(newState: PeerConnection.SignalingState) {}
            override fun onIceConnectionChange(newState: PeerConnection.IceConnectionState) {}
            override fun onIceConnectionReceivingChange(receiving: Boolean) {}

            override fun onIceGatheringChange(newState: PeerConnection.IceGatheringState) {
                if (!closed && newState == PeerConnection.IceGatheringState.COMPLETE) {
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
        pc = checkNotNull(factory.createPeerConnection(config, observer)) {
            "peer_connection_null"
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
        val dc = synchronized(lock) {
            check(!closed) { "peer_connection_closed" }
            checkNotNull(pc.createDataChannel(label, init)) { "data_channel_null" }
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
        synchronized(lock) {
            if (closed) {
                runCatching { dc.close(); dc.dispose() }
                throw IllegalStateException("peer_connection_closed")
            }
            dc.registerObserver(observer)
            channels.add(dc to observer)
        }
        return object : RtcDataChannel {
            override fun state(): RtcChannelState {
                if (closed) return RtcChannelState.CLOSED
                return when (runCatching { dc.state() }.getOrDefault(DataChannel.State.CLOSED)) {
                    DataChannel.State.CONNECTING -> RtcChannelState.CONNECTING
                    DataChannel.State.OPEN -> RtcChannelState.OPEN
                    DataChannel.State.CLOSING -> RtcChannelState.CLOSING
                    DataChannel.State.CLOSED -> RtcChannelState.CLOSED
                }
            }

            override fun bufferedAmount(): Long = if (closed) 0L else runCatching { dc.bufferedAmount() }.getOrDefault(0L)

            override fun send(data: ByteArray, binary: Boolean): Boolean {
                if (closed) return false
                val copy = data.copyOf()
                return runCatching { dc.send(DataChannel.Buffer(ByteBuffer.wrap(copy), binary)) }.getOrDefault(false)
            }

            override fun close() {
                synchronized(lock) {
                    if (closed) return
                    runCatching { dc.close() }
                }
            }
        }
    }

    override suspend fun createOffer(iceRestart: Boolean): String {
        val constraints = MediaConstraints().apply {
            if (iceRestart) mandatory.add(MediaConstraints.KeyValuePair("IceRestart", "true"))
        }
        return suspendCancellableCoroutine { cont ->
            pc.createOffer(
                object : SdpObserver {
                    override fun onCreateSuccess(sdp: SessionDescription) {
                        if (cont.isActive) cont.resume(sdp.description)
                    }

                    override fun onSetSuccess() {}
                    override fun onCreateFailure(error: String) {
                        if (cont.isActive) cont.resumeWithException(IllegalStateException(error))
                    }

                    override fun onSetFailure(error: String) {
                        if (cont.isActive) cont.resumeWithException(IllegalStateException(error))
                    }
                },
                constraints,
            )
        }
    }

    override suspend fun createAnswer(): String = suspendCancellableCoroutine { cont ->
        pc.createAnswer(
            object : SdpObserver {
                override fun onCreateSuccess(sdp: SessionDescription) {
                    if (cont.isActive) cont.resume(sdp.description)
                }

                override fun onSetSuccess() {}
                override fun onCreateFailure(error: String) {
                    if (cont.isActive) cont.resumeWithException(IllegalStateException(error))
                }

                override fun onSetFailure(error: String) {
                    if (cont.isActive) cont.resumeWithException(IllegalStateException(error))
                }
            },
            MediaConstraints(),
        )
    }

    override suspend fun setLocalDescription(offer: Boolean, sdp: String): Unit = suspendCancellableCoroutine { cont ->
        val type = if (offer) SessionDescription.Type.OFFER else SessionDescription.Type.ANSWER
        pc.setLocalDescription(
            object : SdpObserver {
                override fun onCreateSuccess(sdp: SessionDescription) {}
                override fun onSetSuccess() {
                    if (cont.isActive) cont.resume(Unit)
                }

                override fun onCreateFailure(error: String) {
                    if (cont.isActive) cont.resumeWithException(IllegalStateException(error))
                }

                override fun onSetFailure(error: String) {
                    if (cont.isActive) cont.resumeWithException(IllegalStateException(error))
                }
            },
            SessionDescription(type, sdp),
        )
    }

    override suspend fun setRemoteDescription(offer: Boolean, sdp: String): Unit = suspendCancellableCoroutine { cont ->
        val type = if (offer) SessionDescription.Type.OFFER else SessionDescription.Type.ANSWER
        pc.setRemoteDescription(
            object : SdpObserver {
                override fun onCreateSuccess(sdp: SessionDescription) {}
                override fun onSetSuccess() {
                    if (cont.isActive) cont.resume(Unit)
                }

                override fun onCreateFailure(error: String) {
                    if (cont.isActive) cont.resumeWithException(IllegalStateException(error))
                }

                override fun onSetFailure(error: String) {
                    if (cont.isActive) cont.resumeWithException(IllegalStateException(error))
                }
            },
            SessionDescription(type, sdp),
        )
    }

    override suspend fun addIceCandidate(
        candidate: String,
        sdpMid: String,
        sdpMLineIndex: Int,
        usernameFragment: String,
    ): Unit = suspendCancellableCoroutine { cont ->
        pc.addIceCandidate(
            IceCandidate(sdpMid, sdpMLineIndex, candidate),
            object : AddIceObserver {
                override fun onAddSuccess() {
                    if (cont.isActive) cont.resume(Unit)
                }

                override fun onAddFailure(error: String) {
                    if (cont.isActive) cont.resumeWithException(IllegalStateException(error))
                }
            },
        )
    }

    override fun close() {
        val toDispose: List<Pair<DataChannel, DataChannel.Observer>>
        synchronized(lock) {
            if (closed) return
            closed = true
            toDispose = channels.toList()
            channels.clear()
        }
        for ((dc, _) in toDispose) {
            runCatching { dc.unregisterObserver() }
            runCatching { dc.close() }
            runCatching { dc.dispose() }
        }
        runCatching { pc.close() }
        runCatching { pc.dispose() }
    }
}
