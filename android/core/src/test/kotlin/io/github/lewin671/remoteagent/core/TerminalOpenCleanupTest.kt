package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.TerminalFrame
import kotlinx.coroutines.*
import kotlinx.coroutines.test.*
import okhttp3.*
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class TerminalOpenCleanupTest {
    private class Socket(private val req: Request) : WebSocket {
        var cancelled = false
        override fun request() = req
        override fun queueSize() = 0L
        override fun send(text: String) = true
        override fun send(bytes: okio.ByteString) = true
        override fun close(code: Int, reason: String?) = true
        override fun cancel() { cancelled = true }
    }

    private fun children(remote: Remote): Int {
        val scope = Remote::class.java.getDeclaredField("scope").apply { isAccessible = true }.get(remote) as CoroutineScope
        return scope.coroutineContext[Job]!!.children.count { it.isActive }
    }

    private fun checkFailure(mode: String) = runTest {
        val fixture = ColdOpenFixture(2)
        val peer = ColdOpenPeer(fixture, this)
        var socket: Socket? = null
        var closes = 0
        val factory = WebSocket.Factory { request, listener ->
            if (request.url.encodedPath != "/v1/term") peer.newWebSocket(request, listener)
            else {
                if (mode == "factory") throw java.io.IOException("injected factory failure")
                Socket(request).also {
                    socket = it
                    when (mode) {
                        "failure" -> listener.onFailure(it, java.io.IOException("injected failure"), null)
                        "closed" -> listener.onClosed(it, 1000, "closed before open")
                        "command", "cancel-command" -> listener.onOpen(it, Response.Builder().request(request)
                            .protocol(Protocol.HTTP_1_1).code(101).message("Switching Protocols").build())
                    }
                }
            }
        }
        val remote = peer.remote(sockets = factory)
        try {
            remote.start(); runCurrent()
            val baseline = children(remote)
            if (mode == "cancel-command") peer.before = { name, _ -> if (name == "Forward") awaitCancellation() }
            val opening = async {
                runCatching { remote.openTerminal("short", object : TermListener {
                    override fun onFrame(frame: TerminalFrame) { fail("unexpected frame") }
                    override fun onClose(reason: String) { closes++ }
                }) }
            }
            runCurrent()
            if (mode.startsWith("cancel")) opening.cancel()
            if (mode == "timeout") { advanceTimeBy(20_001); runCurrent() }
            opening.join(); runCurrent()
            if (!opening.isCancelled) assertTrue(opening.await().isFailure)
            assertEquals("reader retained after $mode", baseline, children(remote))
            socket?.let { assertTrue(it.cancelled) }
            assertTrue(closes <= 1)
        } finally { remote.close(); runCurrent() }
    }

    @Test fun factoryFailureReleasesReader() = checkFailure("factory")
    @Test fun socketFailureReleasesReader() = checkFailure("failure")
    @Test fun earlyClosureReleasesReader() = checkFailure("closed")
    @Test fun rejectedCommandReleasesReader() = checkFailure("command")
    @Test fun cancellationWhileOpeningReleasesReader() = checkFailure("cancel-open")
    @Test fun cancellationDuringCommandReleasesReader() = checkFailure("cancel-command")
    @Test fun openTimeoutReleasesReader() = checkFailure("timeout")
}
