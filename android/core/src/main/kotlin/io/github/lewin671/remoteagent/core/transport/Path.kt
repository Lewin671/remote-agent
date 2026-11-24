package io.github.lewin671.remoteagent.core.transport

import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.ForwardResponse

class PathChanged : Exception()

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
    suspend fun prepare(prior: Boolean)
    suspend fun send(machineId: String, command: Envelope): ForwardResponse
    fun refusal(error: com.connectrpc.ConnectException): PathRefusal?
}

/** Routing and opaque envelopes only; no command decoding or authorization. */
internal interface Path {
    fun attempt(handoff: () -> Unit): PathAttempt
    fun attempt(attemptId: String, handoff: () -> Unit): PathAttempt = attempt(handoff)
    fun openStream(address: String, listener: FrameListener): FrameStream
}
