package io.github.lewin671.remoteagent.core.transport

/** Pure-JVM WebRTC DataChannel abstraction; :app supplies the Android org.webrtc implementation. */
enum class RtcPeerState { NEW, CONNECTING, CONNECTED, DISCONNECTED, FAILED, CLOSED }

enum class RtcChannelState { CONNECTING, OPEN, CLOSING, CLOSED }

data class RtcIceCandidate(
    val candidate: String,
    val sdpMid: String,
    val sdpMLineIndex: Int,
    val isSrflx: Boolean,
)

interface RtcDataChannel {
    fun state(): RtcChannelState
    fun bufferedAmount(): Long
    fun send(data: ByteArray, binary: Boolean): Boolean
    fun close()
}

interface RtcDataChannelListener {
    fun onStateChange()
    fun onBufferedAmountChange(previousAmount: Long)
    fun onMessage(data: ByteArray)
}

interface RtcPeerListener {
    fun onIceCandidate(candidate: RtcIceCandidate)
    fun onIceGatheringComplete()
    fun onConnectionStateChange(state: RtcPeerState)
    fun onUnexpectedDataChannel()
}

interface RtcPeerConnection {
    fun createDataChannel(
        label: String,
        id: Int,
        ordered: Boolean,
        maxRetransmits: Int?,
        listener: RtcDataChannelListener,
    ): RtcDataChannel

    suspend fun createOffer(iceRestart: Boolean): String
    suspend fun createAnswer(): String
    suspend fun setLocalDescription(offer: Boolean, sdp: String)
    suspend fun setRemoteDescription(offer: Boolean, sdp: String)
    suspend fun addIceCandidate(candidate: String, sdpMid: String, sdpMLineIndex: Int, usernameFragment: String)
    fun maxMessageSize(): Int? = null
    fun close()
}

fun interface RtcFactory {
    fun create(stunUrls: List<String>, listener: RtcPeerListener): RtcPeerConnection

    /**
     * Loads and initialises the SDK without making a connection, so the first
     * connection of a round does not pay for it inside the 10 s round budget.
     * Called off the session's actor; may be slow.
     */
    fun prewarm() {}
}
