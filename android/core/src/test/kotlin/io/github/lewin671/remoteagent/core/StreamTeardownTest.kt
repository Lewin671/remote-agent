package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.transport.Link as DeviceLink
import io.github.lewin671.remoteagent.core.transport.FrameStream
import io.github.lewin671.remoteagent.proto.TerminalFrame
import kotlinx.coroutines.*
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference

@OptIn(ExperimentalCoroutinesApi::class)
class StreamTeardownTest {
    private fun scope(remote: Remote) = Remote::class.java.getDeclaredField("scope").apply { isAccessible = true }.get(remote) as CoroutineScope

    @Test fun closeBetweenRegistrationAndReturnSeesOnlyInitializedResources() = runTest {
        val peer = ColdOpenPeer(ColdOpenFixture(2), this)
        var socketCancelled = false
        val remote = peer.remote(sockets = okhttp3.WebSocket.Factory { request, listener ->
            if (request.url.encodedPath != "/v1/term") peer.newWebSocket(request, listener)
            else object : okhttp3.WebSocket {
                override fun request() = request
                override fun queueSize() = 0L
                override fun send(text: String) = true
                override fun send(bytes: okio.ByteString) = true
                override fun close(code: Int, reason: String?) = true
                override fun cancel() { socketCancelled = true }
            }
        })
        remote.start(); runCurrent()
        val published = CountDownLatch(1)
        val closeFailure = AtomicReference<Throwable?>()
        var completed = false
        // Release the actual registry monitor while insertion is on-stack:
        // the closer sees the registered stream before openStream can return.
        val registry = object : LinkedHashSet<DeviceLink.Stream>() {
            override fun add(element: DeviceLink.Stream): Boolean {
                val added = super.add(element)
                published.countDown()
                (this as java.lang.Object).wait(5000)
                assertTrue("concurrent close did not finish", completed)
                return added
            }
        }
        DeviceLink::class.java.getDeclaredField("streams").apply { isAccessible = true }.set(remote.deviceLink, registry)
        val closer = Thread {
            try {
                check(published.await(5, TimeUnit.SECONDS))
                remote.close()
            } catch (error: Throwable) { closeFailure.set(error) }
            finally {
                synchronized(registry) {
                    completed = true
                    (registry as java.lang.Object).notifyAll()
                }
            }
        }
        closer.start()
        try {
            val opening = async { runCatching { remote.openTerminal("short", object : TermListener {
                override fun onFrame(frame: TerminalFrame) {}
                override fun onClose(reason: MessageText) {}
            }) } }
            runCurrent()
            closer.join(5000)
            assertFalse("closer retained", closer.isAlive)
            assertNull("registered stop read an uninitialized handle", closeFailure.get())
            assertTrue(opening.await().isFailure)
            runCurrent()
            assertFalse("Remote scope was not torn down", scope(remote).isActive)
            assertTrue(registry.isEmpty())
            assertTrue(socketCancelled)
        } finally { remote.close(); runCurrent(); closer.join(5000) }
    }

    @Test fun throwingStreamAndCacheStillCloseOtherStreamsSecurityAndScope() = runTest {
        val peer = ColdOpenPeer(ColdOpenFixture(2), this)
        val failure = IllegalStateException("injected stream cleanup")
        var cached = false
        val remote = peer.remote(cache = object : CipherCache by MemoryCache() {
            override fun close() { cached = true; throw IllegalStateException("injected cache close") }
        })
        var second = false
        remote.deviceLink.openStream(stop = { throw failure }, onClose = {})
        remote.deviceLink.openStream(stop = { second = true }, onClose = {})
        assertSame(failure, runCatching { remote.close() }.exceptionOrNull())
        runCurrent()
        assertTrue(second)
        assertTrue(cached)
        assertFalse(scope(remote).isActive)
        assertTrue(runCatching { remote.security.record() }.isFailure)
        assertEquals(1, failure.suppressed.size)
    }

    @Test fun mainStreamCloseFailureStillReleasesReaderAndRetries() = runTest {
        val peer = ColdOpenPeer(ColdOpenFixture(2), this)
        lateinit var events: okhttp3.WebSocketListener
        lateinit var socket: okhttp3.WebSocket
        var created = 0
        val failure = IllegalStateException("injected main close")
        val factory = okhttp3.WebSocket.Factory { request, listener ->
            created++
            events = listener
            val underlying = peer.newWebSocket(request, listener)
            object : okhttp3.WebSocket by underlying {
                override fun close(code: Int, reason: String?): Boolean { throw failure }
            }.also { socket = it }
        }
        val remote = peer.remote(sockets = factory)
        try {
            remote.start(); runCurrent()
            assertEquals(ConnState.ONLINE, remote.state.value.conn)
            assertSame(failure, runCatching { events.onClosing(socket, 1000, "closed") }.exceptionOrNull())
            runCurrent()
            assertEquals(ConnState.OFFLINE, remote.state.value.conn)
            advanceTimeBy(1251); runCurrent()
            assertEquals(2, created)
        } finally { remote.close(); runCurrent() }
    }

    @Test fun failingSecurityCacheClearStillStopsSocketAndPublishesOffline() = runTest {
        val fixture = ColdOpenFixture(2)
        val peer = ColdOpenPeer(fixture, this)
        var broken = false
        var cancelled = false
        val store = object : DeviceSecurity.Storage by TestSecurityStore(fixture.record) {
            override fun clearSecurityCache() { if (broken) throw IllegalStateException("injected cache clear") }
        }
        val factory = okhttp3.WebSocket.Factory { request, listener ->
            val underlying = peer.newWebSocket(request, listener)
            object : okhttp3.WebSocket by underlying {
                override fun cancel() { cancelled = true; underlying.cancel() }
            }
        }
        val remote = Remote(fixture.account, Api.defaultHttp(), clock = { fixture.now },
            securityStorage = store, tokenStorage = peer.saved(), dispatcher = StandardTestDispatcher(testScheduler),
            socketFactory = factory, rpcTransport = peer)
        try {
            remote.start(); runCurrent()
            assertEquals(ConnState.ONLINE, remote.state.value.conn)
            broken = true
            val stop = Remote::class.java.getDeclaredMethod("stopOnWorker").apply { isAccessible = true }
            assertTrue(runCatching { stop.invoke(remote) }.isFailure)
            assertTrue(cancelled)
            assertEquals(ConnState.OFFLINE, remote.state.value.conn)
        } finally { broken = false; remote.close(); runCurrent() }
    }

    @Test fun closedLinkImmediatelyDisposesLateRegistration() = runTest {
        val remote = ColdOpenPeer(ColdOpenFixture(2), this).remote()
        remote.close(); runCurrent()
        var stopped = 0
        var notified = 0
        val stream = remote.deviceLink.openStream(stop = { stopped++ }, onClose = { notified++ })
        assertEquals(1, stopped)
        stream.end("late end".messageText())
        assertEquals(1, stopped)
        assertEquals(0, notified)
    }

    @Test fun stopFailureStillNotifiesOnceAndLocalCloseStillClosesSocket() = runTest {
        val remote = ColdOpenPeer(ColdOpenFixture(2), this).remote()
        val failure = IllegalStateException("injected stop")
        var notified = 0
        val stream = remote.deviceLink.openStream(stop = { throw failure }, onClose = { notified++ })
        assertSame(failure, runCatching { stream.end("closed".messageText()) }.exceptionOrNull())
        stream.end("again".messageText())
        assertEquals(1, notified)
        var physicalClose = 0
        val socket = object : FrameStream {
            override fun send(frame: ByteArray) = true
            override fun cancel() {}
            override fun close(): Boolean { physicalClose++; return true }
        }
        val conn = TermConn(socket, {}, { throw failure })
        assertSame(failure, runCatching { conn.close() }.exceptionOrNull())
        assertEquals(1, physicalClose)
        remote.close(); runCurrent()
    }

    @Test fun oversizedMainStreamFrameIsRejectedBeforeParsingProtobuf() = runTest {
        val peer = ColdOpenPeer(ColdOpenFixture(2), this)
        lateinit var events: okhttp3.WebSocketListener
        lateinit var socket: okhttp3.WebSocket
        var cancelled = false
        val factory = okhttp3.WebSocket.Factory { request, listener ->
            events = listener
            val underlying = peer.newWebSocket(request, listener)
            object : okhttp3.WebSocket by underlying {
                override fun cancel() { cancelled = true; underlying.cancel() }
            }.also { socket = it }
        }
        val remote = peer.remote(sockets = factory)
        var parseCalls = 0
        remote.parseServerFrame = { bytes ->
            parseCalls++
            io.github.lewin671.remoteagent.proto.ServerFrame.parseFrom(bytes)
        }
        try {
            remote.start(); runCurrent()
            assertEquals(ConnState.ONLINE, remote.state.value.conn)
            parseCalls = 0
            val oversized = ByteArray((2 * 1024 * 1024) + 1)
            events.onMessage(socket, okio.ByteString.of(*oversized))
            runCurrent()
            assertTrue("oversized frame should cancel stream", cancelled)
            assertEquals("oversized frame must not be parsed", 0, parseCalls)
        } finally { remote.close(); runCurrent() }
    }
}
