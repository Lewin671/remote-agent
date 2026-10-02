package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.CommandResult
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.ForwardReply
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.MachineServiceClient
import io.github.lewin671.remoteagent.proto.PayloadKind
import io.github.lewin671.remoteagent.proto.PeerFrame
import io.github.lewin671.remoteagent.proto.PutMachineRequest
import io.github.lewin671.remoteagent.proto.PutSessionRequest
import io.github.lewin671.remoteagent.proto.ServerFrame
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.StoredEvent
import io.github.lewin671.remoteagent.proto.StreamHello
import io.github.lewin671.remoteagent.proto.TermHello
import io.github.lewin671.remoteagent.proto.TerminalFrame
import io.github.lewin671.remoteagent.proto.UploadEventsRequest
import com.connectrpc.ProtocolClientConfig
import com.connectrpc.impl.ProtocolClient
import com.connectrpc.okhttp.ConnectOkHttpClient
import com.connectrpc.protocols.NetworkProtocol
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString.Companion.toByteString
import org.junit.Assume
import java.io.File
import java.net.ServerSocket
import java.nio.file.Files
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit

/** A remote-agent-server process on a free local port. */
class TestServer(private val dbDir: File = Files.createTempDirectory("ra-server").toFile(), val port: Int = freePort()) : AutoCloseable {
    val url = "http://127.0.0.1:$port"
    val setupToken = "setup-token-for-tests"
    private var proc: Process? = null

    init {
        start()
    }

    fun start() {
        val bin = File(System.getProperty("raServer") ?: "")
        Assume.assumeTrue("the Go server was not built", bin.canExecute())
        val pb = ProcessBuilder(bin.absolutePath, "-listen", "127.0.0.1:$port", "-db", File(dbDir, "ra.db").absolutePath)
        pb.environment()["RA_SETUP_TOKEN"] = setupToken
        pb.redirectErrorStream(true).redirectOutput(File(dbDir, "server.log"))
        val started = pb.start()
        proc = started
        // Ready means this process answers HTTP. A bare TCP connect is not
        // enough: with no listener yet, a client socket can connect to
        // itself when the kernel picks the same port for it, and the first
        // request then fails with "unexpected end of stream".
        val until = System.currentTimeMillis() + 10_000
        while (System.currentTimeMillis() < until) {
            check(started.isAlive) { "server exited: " + File(dbDir, "server.log").readText().take(500) }
            try {
                val c = java.net.URL("$url/").openConnection() as java.net.HttpURLConnection
                c.connectTimeout = 500
                c.readTimeout = 500
                c.responseCode
                c.disconnect()
                return
            } catch (e: Exception) {
                Thread.sleep(50)
            }
        }
        error("server did not start")
    }

    fun stop() {
        proc?.destroy()
        proc?.waitFor(5, TimeUnit.SECONDS)
        proc = null
    }

    override fun close() {
        stop()
        dbDir.deleteRecursively()
    }

    companion object {
        fun freePort(): Int = ServerSocket(0).use { it.localPort }
    }
}

/**
 * A stand-in for the daemon: it uploads a machine, sessions and events the
 * way internal/relay does, answers forwarded commands, and serves terminals.
 */
class FakeDaemon(val account: Account, private val http: OkHttpClient) : AutoCloseable {
    private val keys = Keys.derive(account.root)
    val id = account.deviceId
    private val api = MachineServiceClient(
        ProtocolClient(
            ConnectOkHttpClient(http),
            ProtocolClientConfig(host = account.server, serializationStrategy = LiteProtobufStrategy, networkProtocol = NetworkProtocol.CONNECT),
        ),
    )
    private var token = ""
    private var ws: WebSocket? = null
    private var metaRev = 0L
    val sessionKeys = HashMap<String, ByteArray>()
    private val sessionRevs = HashMap<String, Long>()
    private val lastSeq = HashMap<String, Long>()
    val commands = LinkedBlockingQueue<Pair<Header, Command>>()
    val termInput = LinkedBlockingQueue<TerminalFrame>()

    /** Seal results with a wrong key, as a corrupted relay would deliver them. */
    @Volatile var garbleResults = false

    /** What to answer; the default is an empty success of the right kind. */
    var answer: (Header, Command) -> CommandResult = { _, c -> defaultResult(c) }

    suspend fun login() {
        token = login(Api(account.server, http), account)
    }

    suspend fun putMachine(hostname: String, protocol: Int = PROTOCOL_VERSION) {
        metaRev++
        val meta = MachineMeta.newBuilder().setHostname(hostname).setOs("darwin").setHome("/Users/me").setProtocolVersion(protocol).build()
        val env = Envelopes.seal(keys.meta, header(PayloadKind.PAYLOAD_KIND_MACHINE_META).setSeq(metaRev).build(), meta)
        api.putMachine(PutMachineRequest.newBuilder().setMeta(env).build(), Api.headers(token)).value()
    }

    suspend fun putSession(sid: String, info: Session) {
        val key = sessionKeys.getOrPut(sid) { randomBytes(32) }
        val rev = (sessionRevs[sid] ?: 0) + 1
        sessionRevs[sid] = rev
        val keyEnv = Envelopes.seal(keys.wrap, header(PayloadKind.PAYLOAD_KIND_SESSION_KEY).setSessionId(sid).build(), key)
        val meta = Envelopes.seal(key, header(PayloadKind.PAYLOAD_KIND_SESSION_META).setSessionId(sid).setSeq(rev).build(), info.toBuilder().setId(sid).build())
        api.putSession(PutSessionRequest.newBuilder().setSessionId(sid).setKey(keyEnv).setMeta(meta).build(), Api.headers(token)).value()
    }

    /** Stores an image's display copy for a session, as the daemon does before the event that refers to it. */
    suspend fun putImage(sid: String, imageId: String, payload: io.github.lewin671.remoteagent.proto.ImagePayload) {
        val env = Envelopes.seal(sessionKeys.getValue(sid), header(PayloadKind.PAYLOAD_KIND_IMAGE).setSessionId(sid).setRequestId(imageId).build(), payload)
        api.putImage(io.github.lewin671.remoteagent.proto.PutImageRequest.newBuilder().setSessionId(sid).setImageId(imageId).setImage(env).build(), Api.headers(token)).value()
    }

    /** Appends events to a session, numbering them. */
    suspend fun upload(sid: String, vararg events: Event.Builder) {
        val key = sessionKeys.getValue(sid)
        val stored = events.map { b ->
            val seq = (lastSeq[sid] ?: 0) + 1
            lastSeq[sid] = seq
            val ev = b.setSeq(seq).setSessionId(sid).setTime(Timestamp.newBuilder().setSeconds(System.currentTimeMillis() / 1000)).build()
            StoredEvent.newBuilder().setSeq(seq)
                .setEnvelope(Envelopes.seal(key, header(PayloadKind.PAYLOAD_KIND_EVENT).setSessionId(sid).setSeq(seq).build(), ev))
                .build()
        }
        api.uploadEvents(UploadEventsRequest.newBuilder().setSessionId(sid).addAllEvents(stored).build(), Api.headers(token)).value()
    }

    /** Connects the stream and answers forwarded commands. */
    fun connect() {
        val url = account.server.replaceFirst("http", "ws") + "/v1/stream"
        ws = http.newWebSocket(Request.Builder().url(url).build(), object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                webSocket.send(PeerFrame.newBuilder().setHello(StreamHello.newBuilder().setToken(token)).build().toByteArray().toByteString())
            }

            override fun onMessage(webSocket: WebSocket, bytes: okio.ByteString) {
                val f = ServerFrame.parseFrom(bytes.toByteArray())
                if (!f.hasForwardCall()) return
                val call = f.forwardCall
                val (h, pt) = Envelopes.open(keys.command, call.command)
                val cmd = Command.parseFrom(pt)
                commands += h to cmd
                if (cmd.hasOpenTerminal()) serveTerminal(h, cmd)
                val result = answer(h, cmd)
                val env = Envelopes.seal(
                    if (garbleResults) randomBytes(32) else keys.command,
                    header(PayloadKind.PAYLOAD_KIND_COMMAND_RESULT).setSessionId(h.sessionId).setRequestId(h.requestId).build(),
                    result,
                )
                webSocket.send(PeerFrame.newBuilder().setForwardReply(ForwardReply.newBuilder().setCallId(call.callId).setResult(env)).build().toByteArray().toByteString())
            }
        })
    }

    fun disconnect() {
        ws?.close(1000, null)
        ws = null
    }

    val termFramesSent = CopyOnWriteArrayList<TerminalFrame>()

    private fun serveTerminal(h: Header, cmd: Command) {
        val sid = cmd.openTerminal.sessionId
        val stream = cmd.openTerminal.streamId
        val key = sessionKeys.getValue(sid)
        var seq = 0L
        val url = account.server.replaceFirst("http", "ws") + "/v1/term"
        http.newWebSocket(Request.Builder().url(url).build(), object : WebSocketListener() {
            fun send(ws: WebSocket, f: TerminalFrame) {
                seq++
                val env = Envelopes.seal(key, header(PayloadKind.PAYLOAD_KIND_TERMINAL_OUTPUT).setSessionId(sid).setRequestId(stream).setSeq(seq).build(), f)
                ws.send(env.toByteArray().toByteString())
                termFramesSent += f
            }

            override fun onOpen(webSocket: WebSocket, response: Response) {
                webSocket.send(TermHello.newBuilder().setToken(token).setStreamId(stream).build().toByteArray().toByteString())
                send(webSocket, TerminalFrame.newBuilder().setSnapshot(
                    io.github.lewin671.remoteagent.proto.TerminalSnapshot.newBuilder().setCols(120).setRows(40).setData(ByteString.copyFromUtf8("\u001b[Hhello")),
                ).build())
                send(webSocket, TerminalFrame.newBuilder().setOutput(ByteString.copyFromUtf8(" world")).build())
            }

            override fun onMessage(webSocket: WebSocket, bytes: okio.ByteString) {
                val (hh, frame) = Envelopes.openMessage(
                    key, Envelope.parseFrom(bytes.toByteArray()),
                    Place(PayloadKind.PAYLOAD_KIND_TERMINAL_INPUT, sessionId = sid, requestId = stream),
                    TerminalFrame::parseFrom,
                )
                check(hh.seq > 0)
                termInput += frame
            }
        })
    }

    private fun header(kind: PayloadKind) = Header.newBuilder().setKind(kind).setMachineId(id).setSender(id)

    override fun close() = disconnect()

    companion object {
        suspend fun create(root: ByteArray, server: String, http: OkHttpClient, setupToken: String): FakeDaemon {
            val acct = joinWithRoot(root, server, http, setupToken = setupToken, kind = DeviceKind.DEVICE_KIND_MACHINE)
            return FakeDaemon(acct, http).also { it.login() }
        }

        fun defaultResult(c: Command): CommandResult {
            val b = CommandResult.newBuilder()
            when (c.kindCase) {
                Command.KindCase.SEND -> b.setSend(io.github.lewin671.remoteagent.proto.SendResponse.newBuilder().setQueuePosition(1))
                Command.KindCase.OPEN_TERMINAL -> b.setOpenTerminal(io.github.lewin671.remoteagent.proto.OpenTerminalResponse.getDefaultInstance())
                Command.KindCase.INTERRUPT -> b.setInterrupt(io.github.lewin671.remoteagent.proto.InterruptResponse.newBuilder().setInterrupted(true))
                else -> b.setErrorCode("unimplemented").setErrorMessage("fake daemon")
            }
            return b.build()
        }
    }
}
