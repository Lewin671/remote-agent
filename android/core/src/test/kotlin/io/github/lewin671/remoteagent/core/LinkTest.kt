package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.transport.*
import okhttp3.*
import org.junit.Assert.*
import org.junit.Test

class LinkTest {
    @get:org.junit.Rule val hangGuard = hangGuard()

    @Test fun pathEndIsSeparateFromLogicalStreamClosePolicy() {
        lateinit var events: WebSocketListener
        lateinit var socket: WebSocket
        val factory = WebSocket.Factory { request, listener ->
            events = listener
            object : WebSocket {
                override fun request() = request
                override fun queueSize() = 0L
                override fun send(text: String) = true
                override fun send(bytes: okio.ByteString) = true
                override fun close(code: Int, reason: String?) = true
                override fun cancel() {}
            }.also { socket = it }
        }
        val path = RelayPath(Api("https://example.com", Api.defaultHttp()), { factory }) { PeerState() }
        val link = io.github.lewin671.remoteagent.core.transport.Link(path)
        var endings = 0
        var closes = 0
        var stopped = 0
        path.openStream("wss://example.com/v1/term", object : FrameListener() {
            override fun onEnd(stream: FrameStream, code: Int) { endings++ }
        })
        val stream = link.openStream(stop = { stopped++ }, onClose = { closes++ })
        events.onClosed(socket, 1000, "")
        assertEquals(1, endings)
        assertEquals(0, closes)
        stream.end("closed".messageText())
        stream.end("late callback".messageText())
        assertEquals(1, closes)
        assertEquals(1, stopped)
    }
}
