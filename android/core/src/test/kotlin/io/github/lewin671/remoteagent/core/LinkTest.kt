package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.transport.*
import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.runBlocking
import okhttp3.*
import org.junit.Assert.*
import org.junit.Test
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger

class LinkTest {
    private fun projected(id: String) = RemoteState(
        conn = ConnState.OFFLINE,
        machines = mapOf(id to MachineView(id, false, 0,
            MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION - 1).build(),
            linkState = PeerState(reachable = true, ready = true, authorized = true, relayRequired = false,
                protocolVersion = PROTOCOL_VERSION,
                capabilities = MachineCapabilities.newBuilder().setScheduledTasks(true).setFileUpload(true).build()),
        )),
    )

    @Test fun linkProjectionControlsForwardAndScheduleGates() = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        val daemon = FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken)
        val remote = fixtureRemote(approvedClient(daemon.phone, http), http)
        try {
            daemon.answer = { _, _ -> CommandResult.getDefaultInstance() }
            daemon.connect(); daemon.waitReady()
            val state = projected(daemon.id)
            remote.showForTest(state)
            assertTrue(scheduleWritable(state, daemon.id))
            assertTrue(state.peer(daemon.id).canProbe(false))
            assertTrue(filesSupported(state.machines[daemon.id]))
            assertNull(protocolMismatch(state.machines[daemon.id]))
            val command = Command.newBuilder().setCreateSession(CreateSessionRequest.newBuilder().setCwd("/work/example").addFileIds("original-file")).build()
            remote.machineCommand(daemon.id, command)
            val received = daemon.commands.poll(5, TimeUnit.SECONDS) ?: error("command blocked by relay discovery")
            assertEquals(listOf("original-file"), received.second.createSession.fileIdsList)
        } finally { remote.close(); daemon.close(); server.close() }
    }

    @Test fun reachabilityCannotReplaceAuthorizationOrClearPrior() = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        val daemon = FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken)
        val account = approvedClient(daemon.phone, http)
        val store = TestSecurity.get(server.url).stores.getValue(account.deviceId)
        val blocked = AtomicBoolean(false)
        val forwards = AtomicInteger()
        val measured = http.newBuilder().addInterceptor { chain ->
            if (chain.request().url.encodedPath.endsWith("/Forward")) forwards.incrementAndGet()
            chain.proceed(chain.request())
        }.build()
        val guarded = object : DeviceSecurity.Storage by store {
            override fun readSecurity(): DeviceSecurity.Record {
                if (blocked.get()) throw DeviceSecurity.Failure("lease")
                return store.readSecurity()
            }
        }
        val remote = Remote(account, measured, securityStorage = guarded)
        try {
            remote.currentToken()
            remote.showForTest(projected(daemon.id))
            blocked.set(true)
            val fresh = runCatching {
                remote.machineCommand(daemon.id, Command.newBuilder().setGetSettings(GetSettingsRequest.getDefaultInstance()).build())
            }.exceptionOrNull()
            assertEquals(0, forwards.get())
            assertTrue("authorization must still run: $fresh", fresh is CommandException && fresh.code == "authorization_unavailable" && fresh.outcome == Outcome.NOT_SENT)
        } finally { remote.close(); daemon.close(); server.close() }
    }

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
