package io.github.lewin671.remoteagent.core.transport

import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.value
import io.github.lewin671.remoteagent.proto.ForwardRequest
import io.github.lewin671.remoteagent.proto.ForwardResponse
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString

internal class RelayPath(
    private val api: Api,
    private val sockets: () -> WebSocket.Factory,
    private val observe: (String) -> PeerState,
) : Path {
    override fun status(machineId: String) = observe(machineId)
    override suspend fun epoch() = api.relayEpoch()
    override suspend fun forward(frame: ForwardRequest, headers: Map<String, List<String>>): ForwardResponse =
        api.client.forward(frame, headers).value()
    override fun openStream(address: String, listener: FrameListener): FrameStream = SocketStream(
        sockets().newWebSocket(Request.Builder().url(address).build(), object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) = listener.onOpen(SocketStream(webSocket))
            override fun onMessage(webSocket: WebSocket, bytes: ByteString) = listener.onFrame(SocketStream(webSocket), bytes.toByteArray())
            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) = listener.onClosing(SocketStream(webSocket), code)
            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = listener.onEnd(SocketStream(webSocket), code)
            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) = listener.onFailure(SocketStream(webSocket), t)
        }),
    )

    private class SocketStream(private val socket: WebSocket) : FrameStream {
        override fun send(frame: ByteArray) = socket.send(frame.toByteString())
        override fun cancel() = socket.cancel()
        override fun close() = socket.close(1000, null)
    }

    companion object {
        /** Current relay discovery also projects cached/demo values before a
         * live Link snapshot exists. It grants no execution authorization. */
        fun discovery(machine: MachineView?, ready: Boolean = true, authorized: Boolean = true, unlinked: Boolean = false): PeerState = PeerState(
            reachable = machine?.online == true,
            ready = ready,
            authorized = authorized,
            unlinked = unlinked,
            knownOffline = machine?.online == false,
            protocolVersion = machine?.meta?.protocolVersion,
            capabilities = machine?.meta?.takeIf { it.hasCapabilities() }?.capabilities,
        )
    }
}
