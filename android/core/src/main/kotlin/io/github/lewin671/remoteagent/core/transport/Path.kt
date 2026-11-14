package io.github.lewin671.remoteagent.core.transport

import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.ForwardRequest
import io.github.lewin671.remoteagent.proto.ForwardResponse
import io.github.lewin671.remoteagent.proto.MachineCapabilities

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

/** Routing and opaque envelopes only; no command decoding or authorization. */
internal interface Path {
    fun status(machineId: String): PeerState
    suspend fun epoch(): String
    suspend fun forward(frame: ForwardRequest, headers: Map<String, List<String>>): ForwardResponse
    fun openStream(address: String, listener: FrameListener): FrameStream
}
