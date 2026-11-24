package io.github.lewin671.remoteagent.core.transport

import com.connectrpc.ConnectException
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.newRequestId
import io.github.lewin671.remoteagent.core.value
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.ForwardRequest
import io.github.lewin671.remoteagent.proto.ForwardResponse
import io.github.lewin671.remoteagent.proto.NotForwarded
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString

internal class RelayPath(
    private val api: Api,
    private val sockets: () -> WebSocket.Factory,
    private val token: suspend () -> String = { "" },
) : Path {
    override fun attempt(handoff: () -> Unit): PathAttempt = attempt(newRequestId(), handoff)
    override fun attempt(attemptId: String, handoff: () -> Unit): PathAttempt {
        val id = attemptId
        return object : PathAttempt {
            override val attemptId = id
            private var usedToken: String? = null
            private var epoch = ""
            override suspend fun prepare(prior: Boolean) { usedToken = token() }
            override suspend fun send(machineId: String, command: Envelope): ForwardResponse {
                val sentToken = checkNotNull(usedToken)
                val current = api.relayEpoch()
                handoff()
                epoch = current
                val frame = ForwardRequest.newBuilder().setMachineId(machineId).setCommand(command).setAttemptId(attemptId).setRelayEpoch(current).build()
                return api.client.forward(frame, Api.headers(sentToken) + ("Remote-Agent-Attempt" to listOf(attemptId))).value()
            }
            override fun refusal(error: ConnectException): PathRefusal? {
                val proof = error.details.mapNotNull { detail ->
                    if (!detail.type.endsWith("remoteagent.v1.NotForwarded")) null
                    else runCatching { NotForwarded.parseFrom(detail.payload.toByteArray()) }.getOrNull()
                }.firstOrNull { it.attemptId == attemptId && it.relayEpoch == epoch && epoch.isNotEmpty() }
                return proof?.let { PathRefusal(it.reason) }
            }
        }
    }
    override fun openStream(address: String, listener: FrameListener): FrameStream {
        val stream = SocketStream()
        val socket = sockets().newWebSocket(Request.Builder().url(address).build(), object : WebSocketListener() {
            private fun ensure(webSocket: WebSocket): SocketStream = stream.also { it.bind(webSocket) }
            override fun onOpen(webSocket: WebSocket, response: Response) = listener.onOpen(ensure(webSocket))
            override fun onMessage(webSocket: WebSocket, bytes: ByteString) = listener.onFrame(ensure(webSocket), bytes.toByteArray())
            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) = listener.onClosing(ensure(webSocket), code)
            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = listener.onEnd(ensure(webSocket), code)
            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) = listener.onFailure(ensure(webSocket), t)
        })
        stream.bind(socket)
        return stream
    }

    private class SocketStream : FrameStream {
        @Volatile private var socket: WebSocket? = null
        fun bind(webSocket: WebSocket) {
            if (socket == null) socket = webSocket
        }
        override fun send(frame: ByteArray) = checkNotNull(socket).send(frame.toByteString())
        override fun cancel() { socket?.cancel() }
        override fun close(): Boolean = socket?.close(1000, null) ?: false
    }
}
