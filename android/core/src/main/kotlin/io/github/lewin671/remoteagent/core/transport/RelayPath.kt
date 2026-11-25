package io.github.lewin671.remoteagent.core.transport

import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString

internal class RelayPath(
    private val sockets: () -> WebSocket.Factory,
) : Path {
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
