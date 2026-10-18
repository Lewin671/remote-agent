package io.github.lewin671.remoteagent.core

import com.connectrpc.ProtocolClientConfig
import com.connectrpc.impl.ProtocolClient
import com.connectrpc.okhttp.ConnectOkHttpClient
import com.connectrpc.protocols.NetworkProtocol
import com.google.protobuf.ByteString
import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.proto.*
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
import java.io.File
import java.net.ServerSocket
import java.nio.file.Files
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString.Companion.toByteString
import org.junit.Assume

/** A remote-agent-server process on a free local port. */
class TestServer(
    private val dbDir: File = Files.createTempDirectory("ra-server").toFile(),
    val port: Int = freePort(),
) : AutoCloseable {
    val url = "http://127.0.0.1:$port"
    val setupToken = "setup-token-for-tests"
    private var proc: Process? = null

    init {
        start()
    }

    fun start() {
        val bin = File(System.getProperty("raServer") ?: "")
        Assume.assumeTrue("the Go server was not built", bin.canExecute())
        val pb =
            ProcessBuilder(
                bin.absolutePath,
                "-listen",
                "127.0.0.1:$port",
                "-db",
                File(dbDir, "ra.db").absolutePath,
            )
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
            check(started.isAlive) {
                "server exited: " + File(dbDir, "server.log").readText().take(500)
            }
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
        TestSecurity.remove(url)
        dbDir.deleteRecursively()
    }

    companion object {
        fun freePort(): Int = ServerSocket(0).use { it.localPort }
    }
}

/**
 * A stand-in for the daemon: it uploads a machine, sessions and events the way internal/relay does,
 * answers forwarded commands, and serves terminals.
 */
class FakeDaemon(val account: Account, private val http: OkHttpClient) : AutoCloseable {
    private val keys = Keys.derive(account.root)
    private val security
        get() = TestSecurity.get(account.server)

    val id = account.deviceId
    private val api =
        MachineServiceClient(
            ProtocolClient(
                ConnectOkHttpClient(http),
                ProtocolClientConfig(
                    host = account.server,
                    serializationStrategy = LiteProtobufStrategy,
                    networkProtocol = NetworkProtocol.CONNECT,
                ),
            ),
        )
    private var token = ""
    private var ws: WebSocket? = null
    private val ready = java.util.concurrent.atomic.AtomicBoolean(false)
    private var metaRev = 0L
    val sessionKeys = HashMap<String, ByteArray>()
    private val info = java.util.concurrent.ConcurrentHashMap<String, Session>()
    private val history =
        java.util.concurrent.ConcurrentHashMap<String, CopyOnWriteArrayList<Event>>()
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
        if (TestSecurity.find(account.server) == null) {
            val (_, genesis) = TestSecurity.initialize(account)
            api.pinSecurityDomain(
                    PinSecurityDomainRequest.newBuilder()
                        .setGenesisState(ByteString.copyFrom(genesis))
                        .build(),
                    Api.headers(token),
                )
                .value()
        }
    }

    suspend fun putMachine(
        hostname: String,
        protocol: Int = PROTOCOL_VERSION,
        projects: List<io.github.lewin671.remoteagent.proto.ProjectSummary> = emptyList(),
    ) {
        metaRev++
        val meta =
            MachineMeta.newBuilder()
                .setHostname(hostname)
                .setOs("darwin")
                .setHome("/Users/me")
                .setProtocolVersion(protocol)
                .addAllProjects(projects)
                .build()
        val env =
            security.seal(
                account,
                header(PayloadKind.PAYLOAD_KIND_MACHINE_META).setSeq(metaRev).build(),
                meta,
            )
        api.putMachine(PutMachineRequest.newBuilder().setMeta(env).build(), Api.headers(token))
            .value()
    }

    /**
     * [endedMs] is what the daemon tells the server in the clear to page the listing by: 0 while
     * the session has not ended.
     */
    suspend fun putSession(sid: String, info: Session, endedMs: Long = 0) {
        this.info[sid] = info.toBuilder().setId(sid).build()
        val key = sessionKeys.getOrPut(sid) { randomBytes(32) }
        val rev = (sessionRevs[sid] ?: 0) + 1
        sessionRevs[sid] = rev
        val keyEnv =
            security.seal(
                account,
                header(PayloadKind.PAYLOAD_KIND_SESSION_KEY).setSessionId(sid).setSeq(1).build(),
                key,
            )
        val meta =
            security.seal(
                account,
                header(PayloadKind.PAYLOAD_KIND_SESSION_META).setSessionId(sid).setSeq(rev).build(),
                info.toBuilder().setId(sid).build(),
            )
        api.putSession(
                PutSessionRequest.newBuilder()
                    .setSessionId(sid)
                    .setKey(keyEnv)
                    .setMeta(meta)
                    .setEndedMs(endedMs)
                    .build(),
                Api.headers(token),
            )
            .value()
    }

    /**
     * Stores an image's display copy for a session, as the daemon does before the event that refers
     * to it.
     */
    suspend fun putImage(
        sid: String,
        imageId: String,
        payload: io.github.lewin671.remoteagent.proto.ImagePayload,
    ) {
        val env =
            security.seal(
                account,
                header(PayloadKind.PAYLOAD_KIND_IMAGE)
                    .setSessionId(sid)
                    .setRequestId(imageId)
                    .build(),
                payload,
                sessionKeys.getValue(sid),
            )
        api.putImage(
                io.github.lewin671.remoteagent.proto.PutImageRequest.newBuilder()
                    .setSessionId(sid)
                    .setImageId(imageId)
                    .setImage(env)
                    .build(),
                Api.headers(token),
            )
            .value()
    }

    /** Appends events to a session, numbering them. */
    suspend fun upload(sid: String, vararg events: Event.Builder) {
        val key = sessionKeys.getValue(sid)
        val stored = events.map { b ->
            val seq = (lastSeq[sid] ?: 0) + 1
            lastSeq[sid] = seq
            val ev =
                b.setSeq(seq)
                    .setSessionId(sid)
                    .setTime(Timestamp.newBuilder().setSeconds(System.currentTimeMillis() / 1000))
                    .build()
            history.getOrPut(sid) { CopyOnWriteArrayList() }.add(ev)
            StoredEvent.newBuilder()
                .setSeq(seq)
                .setEnvelope(
                    security.seal(
                        account,
                        header(PayloadKind.PAYLOAD_KIND_EVENT)
                            .setSessionId(sid)
                            .setSeq(seq)
                            .build(),
                        ev,
                        key,
                    ),
                )
                .build()
        }
        api.uploadEvents(
                UploadEventsRequest.newBuilder().setSessionId(sid).addAllEvents(stored).build(),
                Api.headers(token),
            )
            .value()
    }

    /** Connects the stream and answers forwarded commands. */
    suspend fun waitReady() {
        kotlinx.coroutines.withTimeout(5000) { while (!ready.get()) kotlinx.coroutines.delay(10) }
    }

    fun connect() {
        ready.set(false)
        val url = account.server.replaceFirst("http", "ws") + "/v1/stream"
        ws =
            http.newWebSocket(
                Request.Builder().url(url).build(),
                object : WebSocketListener() {
                    override fun onOpen(webSocket: WebSocket, response: Response) {
                        webSocket.send(
                            PeerFrame.newBuilder()
                                .setHello(StreamHello.newBuilder().setToken(token))
                                .build()
                                .toByteArray()
                                .toByteString(),
                        )
                    }

                    override fun onMessage(webSocket: WebSocket, bytes: okio.ByteString) {
                        val f = ServerFrame.parseFrom(bytes.toByteArray())
                        if (f.hasReady()) {
                            ready.set(true)
                            return
                        }
                        if (!f.hasForwardCall()) return
                        val call = f.forwardCall
                        if (!call.securityRpc.isEmpty) {
                            val reply = ForwardReply.newBuilder().setCallId(call.callId)
                            try {
                                reply.setSecurityRpc(
                                    ByteString.copyFrom(
                                        security.rpc(call.securityRpc.toByteArray()),
                                    ),
                                )
                            } catch (_: Exception) {
                                reply.setError("synthetic authority refused request")
                            }
                            webSocket.send(
                                PeerFrame.newBuilder()
                                    .setForwardReply(reply)
                                    .build()
                                    .toByteArray()
                                    .toByteString(),
                            )
                            return
                        }
                        val (h, pt) = security.open(account, call.command)
                        val cmd = Command.parseFrom(pt)
                        if (!cmd.hasGetLiveSnapshot()) commands += h to cmd
                        if (cmd.hasOpenTerminal()) serveTerminal(h, cmd)
                        val result =
                            if (cmd.hasGetLiveSnapshot()) {
                                val sid = cmd.getLiveSnapshot.sessionId
                                val current = history[sid]?.toList() ?: emptyList()
                                val d = derive(current)
                                val actions = current.filter {
                                    it.hasApprovalRequested() &&
                                        d.approvals.containsKey(it.approvalRequested.approvalId) ||
                                        it.hasQuestionAsked() &&
                                            d.questions.containsKey(it.questionAsked.questionId) ||
                                        it.hasInputState() &&
                                            d.inputs[it.inputState.requestId] in
                                                setOf(
                                                    InputStatus.INPUT_STATUS_QUEUED,
                                                    InputStatus.INPUT_STATUS_HELD_BY_CLI,
                                                )
                                }
                                CommandResult.newBuilder()
                                    .setLiveSnapshot(
                                        LiveSnapshot.newBuilder()
                                            .setRunning(d.running)
                                            .setSession(
                                                info[sid]
                                                    ?: Session.newBuilder().setId(sid).build(),
                                            )
                                            .addAllActions(actions),
                                    )
                                    .build()
                            } else answer(h, cmd)
                        var env =
                            security.seal(
                                account,
                                header(PayloadKind.PAYLOAD_KIND_COMMAND_RESULT)
                                    .setSessionId(h.sessionId)
                                    .setRequestId(h.requestId)
                                    .build(),
                                result,
                            )
                        if (garbleResults)
                            env =
                                env.toBuilder()
                                    .setCiphertext(
                                        ByteString.copyFrom(randomBytes(env.ciphertext.size())),
                                    )
                                    .build()
                        webSocket.send(
                            PeerFrame.newBuilder()
                                .setForwardReply(
                                    ForwardReply.newBuilder().setCallId(call.callId).setResult(env),
                                )
                                .build()
                                .toByteArray()
                                .toByteString(),
                        )
                    }
                },
            )
    }

    fun disconnect() {
        ready.set(false)
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
        http.newWebSocket(
            Request.Builder().url(url).build(),
            object : WebSocketListener() {
                fun send(ws: WebSocket, f: TerminalFrame) {
                    seq++
                    val env =
                        security.seal(
                            account,
                            header(PayloadKind.PAYLOAD_KIND_TERMINAL_OUTPUT)
                                .setSessionId(sid)
                                .setRequestId(stream)
                                .setSeq(seq)
                                .build(),
                            f,
                            key,
                        )
                    ws.send(env.toByteArray().toByteString())
                    termFramesSent += f
                }

                override fun onOpen(webSocket: WebSocket, response: Response) {
                    webSocket.send(
                        TermHello.newBuilder()
                            .setToken(token)
                            .setStreamId(stream)
                            .build()
                            .toByteArray()
                            .toByteString(),
                    )
                    send(
                        webSocket,
                        TerminalFrame.newBuilder()
                            .setSnapshot(
                                io.github.lewin671.remoteagent.proto.TerminalSnapshot.newBuilder()
                                    .setCols(120)
                                    .setRows(40)
                                    .setData(ByteString.copyFromUtf8("\u001b[Hhello")),
                            )
                            .build(),
                    )
                    send(
                        webSocket,
                        TerminalFrame.newBuilder()
                            .setOutput(ByteString.copyFromUtf8(" world"))
                            .build(),
                    )
                }

                override fun onMessage(webSocket: WebSocket, bytes: okio.ByteString) {
                    val (hh, raw) =
                        security.open(account, Envelope.parseFrom(bytes.toByteArray()), key)
                    Envelopes.expect(
                        hh,
                        Place(
                            PayloadKind.PAYLOAD_KIND_TERMINAL_INPUT,
                            sessionId = sid,
                            requestId = stream,
                        ),
                    )
                    val frame = TerminalFrame.parseFrom(raw)
                    check(hh.seq > 0)
                    termInput += frame
                }
            },
        )
    }

    private fun header(kind: PayloadKind) =
        Header.newBuilder().setKind(kind).setMachineId(id).setSender(id)

    override fun close() = disconnect()

    companion object {
        suspend fun create(
            root: ByteArray,
            server: String,
            http: OkHttpClient,
            setupToken: String,
        ): FakeDaemon {
            val acct =
                joinWithRoot(
                    root,
                    server,
                    http,
                    setupToken = setupToken,
                    kind = DeviceKind.DEVICE_KIND_MACHINE,
                )
            return FakeDaemon(acct, http).also { it.login() }
        }

        fun defaultResult(c: Command): CommandResult {
            val b = CommandResult.newBuilder()
            when (c.kindCase) {
                Command.KindCase.SEND ->
                    b.setSend(
                        io.github.lewin671.remoteagent.proto.SendResponse.newBuilder()
                            .setQueuePosition(1),
                    )
                Command.KindCase.OPEN_TERMINAL ->
                    b.setOpenTerminal(
                        io.github.lewin671.remoteagent.proto.OpenTerminalResponse
                            .getDefaultInstance(),
                    )
                Command.KindCase.INTERRUPT ->
                    b.setInterrupt(
                        io.github.lewin671.remoteagent.proto.InterruptResponse.newBuilder()
                            .setInterrupted(true),
                    )
                else -> b.setErrorCode("unimplemented").setErrorMessage("fake daemon")
            }
            return b.build()
        }
    }
}
