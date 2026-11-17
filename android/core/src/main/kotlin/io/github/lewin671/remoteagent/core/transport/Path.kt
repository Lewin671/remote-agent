package io.github.lewin671.remoteagent.core.transport

import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.ForwardResponse
import io.github.lewin671.remoteagent.proto.MachineCapabilities

/** Connection and route telemetry for per-machine UI presentation. */
enum class DirectStage {
    /** This device's switch for the computer is off. */
    OFF,
    /** A direct path is being established. */
    TRYING,
    /** Connected, and both purposes use the relay. */
    CONNECTED_RELAY,
    /** Connected, and at least one purpose uses the direct path. */
    DIRECT_ACTIVE,
    /** No direct path; the relay is in use, with a reason when one is known. */
    UNCONNECTED,
}

enum class DirectFailReason {
    PEER_UNSUPPORTED,
    NO_RESPONSE,
    NO_TRAVERSAL,
    FAILED,
}

data class DirectStatus(
    val supported: Boolean = true,
    val enabled: Boolean = true,
    val stage: DirectStage = DirectStage.OFF,
    val readDirect: Boolean = false,
    val fileDirect: Boolean = false,
    /** Connected read channel temporarily fell back to relay under rule 4. */
    val readSlow: Boolean = false,
    /** Connected file channel temporarily fell back to relay under rule 4. */
    val fileSlow: Boolean = false,
    val reason: DirectFailReason? = null,
    val bulkDirectMibPerSec: Double? = null,
    val bulkRelayMibPerSec: Double? = null,
    val readDirectMs: Double? = null,
    val readRelayMs: Double? = null,
) {
    val hasSpeeds: Boolean
        get() = bulkDirectMibPerSec != null || bulkRelayMibPerSec != null || readDirectMs != null || readRelayMs != null
}

/** Permission and authenticated capabilities are separate from reachability. */
data class PeerState(
    val reachable: Boolean = false,
    val ready: Boolean = false,
    val authorized: Boolean = false,
    val unlinked: Boolean = false,
    val relayRequired: Boolean = true,
    val knownOffline: Boolean = false,
    val protocolVersion: Int? = null,
    val capabilities: MachineCapabilities? = null,
    val fileDirect: Boolean = false,
    val directStatus: DirectStatus = DirectStatus(),
) {
    val available: Boolean get() = reachable && ready
    fun canProbe(serverReachable: Boolean): Boolean = serverReachable || !relayRequired
}

internal interface FrameStream {
    fun send(frame: ByteArray): Boolean
    fun cancel()
    fun close(): Boolean
}

internal open class FrameListener {
    open fun onOpen(stream: FrameStream) {}
    open fun onFrame(stream: FrameStream, bytes: ByteArray) {}
    open fun onClosing(stream: FrameStream, code: Int) {}
    open fun onEnd(stream: FrameStream, code: Int) {}
    open fun onFailure(stream: FrameStream, error: Throwable) {}
}

internal data class PathRefusal(val reason: String)
internal interface PathAttempt {
    val attemptId: String
    val direct: Boolean get() = false
    suspend fun prepare(prior: Boolean)
    suspend fun send(machineId: String, command: Envelope): ForwardResponse
    fun refusal(error: com.connectrpc.ConnectException): PathRefusal?
    suspend fun refreshAuth(prior: Boolean)
}

/** Routing and opaque envelopes only; no command decoding or authorization. */
internal interface Path {
    fun status(machineId: String): PeerState
    fun attempt(handoff: () -> Unit): PathAttempt
    fun attempt(attemptId: String, handoff: () -> Unit): PathAttempt = attempt(handoff)
    fun openStream(address: String, listener: FrameListener): FrameStream
}
