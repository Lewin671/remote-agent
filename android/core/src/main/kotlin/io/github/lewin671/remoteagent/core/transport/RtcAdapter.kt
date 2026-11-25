package io.github.lewin671.remoteagent.core.transport

import io.github.lewin671.remoteagent.core.proto.DirectStatsReport
import java.util.concurrent.ConcurrentHashMap

interface DirectPreferenceStore {
    fun isEnabled(server: String, selfDeviceId: String, machineId: String): Boolean
    fun setEnabled(server: String, selfDeviceId: String, machineId: String, enabled: Boolean)
    fun allEnabled(server: String, selfDeviceId: String): Map<String, Boolean> = emptyMap()
}

class InMemoryDirectPreferenceStore : DirectPreferenceStore {
    private val map = ConcurrentHashMap<String, Boolean>()

    override fun isEnabled(server: String, selfDeviceId: String, machineId: String): Boolean =
        map["$server|$selfDeviceId|$machineId"] ?: true

    override fun setEnabled(server: String, selfDeviceId: String, machineId: String, enabled: Boolean) {
        map["$server|$selfDeviceId|$machineId"] = enabled
    }

    override fun allEnabled(server: String, selfDeviceId: String): Map<String, Boolean> {
        val prefix = "$server|$selfDeviceId|"
        val out = LinkedHashMap<String, Boolean>()
        for ((k, v) in map) {
            if (k.startsWith(prefix)) {
                val id = k.removePrefix(prefix)
                if (id.isNotEmpty()) out[id] = v
            }
        }
        return out
    }
}

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
    fun onIceConnectionStateChange(state: String) {}
    fun onIceGatheringStateChange(state: String) {}
    fun onIceCandidateError(errorCode: Int, address: String) {}
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
    suspend fun endOfCandidates() {}
    fun maxMessageSize(): Int? = null
    suspend fun readStats(): DirectStatsReport = DirectStatsReport.getDefaultInstance()
    fun close()
}

fun interface RtcFactory {
    fun create(stunUrls: List<String>, listener: RtcPeerListener): RtcPeerConnection

    /**
     * Loads and initialises the SDK without making a connection, so the first
     * connection of a round does not pay for it inside the round budget.
     * Called off the session's actor; may be slow.
     */
    fun prewarm() {}
}
