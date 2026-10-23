package io.github.lewin671.remoteagent.core

import com.connectrpc.StreamResult
import com.connectrpc.http.*
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.*
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.StandardTestDispatcher
import okhttp3.*
import okio.Buffer
import okio.ByteString.Companion.toByteString

/** Immutable signed corpus, built once per test class. 501 is the minimum multi-page history. */
internal class ColdOpenFixture(val historySize: Int = 501) {
    val now = 1_700_000_000_000L
    private val signer = SigningKey(ByteArray(32) { 1 })
    private val x = ByteArray(32) { 2 }
    val phone = Authority.newMember(signer, x, 2u)
    private val machineSigner = SigningKey(ByteArray(32) { 3 })
    val machine = Authority.newMember(machineSigner, ByteArray(32) { 4 }, 1u)
    private val secret = ByteArray(32) { 5 }
    private val seed = ByteArray(32) { 6 }
    private val genesis = Authority.genesis("a".repeat(32), ByteArray(32) { 7 }, phone,
        SigningKey(ByteArray(32) { 8 }).publicKey, secret, signer, now)
    private val head = Authority.pair(genesis, machine, secret, secret, signer, now)
    val record = DeviceSecurity.Record.fromChain(
        head.core.account, head.core.domain, DeviceSecurity.stateHash(genesis), phone.id, x,
        listOf(genesis, head).map(DeviceSecurity::stateBytes), listOf(null, null),
    ).installed(emptyList(), emptyList(), HeadLease.sign(head, signer, now))
    val account = Account("https://example.com", byteArrayOf(), ByteArray(32) { 1 }, phone.id, record.pin.account)

    fun removeMachine(): DeviceSecurity.State = Authority.remove(record.head, listOf(machine.id),
        secret, ByteArray(32) { 12 }, signer, now)

    private fun seal(purpose: DeviceSecurity.Purpose, sid: String, seq: Long, plain: ByteArray): Envelope {
        val header = DeviceSecurity.Header(record.pin.account, record.pin.domain, head.core.epoch,
            DeviceSecurity.stateHash(head), head.core.contentEpoch, machine.id, purpose.kind, purpose,
            machine.id, sid, "", seq.toULong(), 0uL, 0u)
        return DeviceSecurity.envelope(DeviceSecurity.Wire(DeviceSecurity.form(secret,
            if (purpose.sessionKey) seed else null, header, plain, machineSigner)))
    }

    val sessions = listOf("long", "short", "empty").associateWith { sid ->
        RemoteSession.newBuilder().setSessionId(sid).setMachineId(machine.id)
            .setKey(seal(DeviceSecurity.Purpose.SESSION_KEY, sid, 1, seed))
            .setMeta(seal(DeviceSecurity.Purpose.SESSION_META, sid, 1,
                Session.newBuilder().setId(sid).setTitle(sid).build().toByteArray()))
            .setLastSeq(when (sid) { "long" -> historySize; "short" -> 2; else -> 0 }.toLong()).build()
    }
    val events = listOf("long", "short", "empty").associateWith { sid ->
        (1..when (sid) { "long" -> historySize + 1; "short" -> 2; else -> 0 }).map { seq ->
            val event = Event.newBuilder().setSeq(seq.toLong()).setSessionId(sid)
                .setAgentMessage(AgentMessage.newBuilder().setText("Message $seq")).build()
            StoredEvent.newBuilder().setSeq(seq.toLong())
                .setEnvelope(seal(DeviceSecurity.Purpose.EVENT, sid, seq.toLong(), event.toByteArray())).build()
        }
    }
}

/** The real Connect codec, Remote, security verification and cache; only I/O and time are fake. */
@OptIn(ExperimentalCoroutinesApi::class)
internal class ColdOpenPeer(val fixture: ColdOpenFixture, val test: TestScope) : HTTPClientInterface, WebSocket.Factory {
    val calls = mutableListOf<String>()
    val after = mutableListOf<Long>()
    var securityRecord = fixture.record
    var machineRows = listOf(Machine.newBuilder().setId(fixture.machine.id).setOnline(true).build())
    var response: (String, UnaryHTTPRequest) -> com.google.protobuf.MessageLite? = { _, _ -> null }
    var count = fixture.historySize
    var missing = 0L
    var latencyMs = 0L
    var before: suspend (String, UnaryHTTPRequest) -> Unit = { _, _ -> }
    var rejectNextHistory = false
    var offline = false
    var anonymous = 0
    var refused = 0
    private var minted = 0
    private var token = "saved-credential"
    private val sockets = mutableListOf<Socket>()
    private val http = Api.defaultHttp()
    fun saved() = MemoryTokens().apply { saved = LoginToken(token, fixture.now + 3 * LOGIN_REFRESH_MARGIN_MS) }
    fun remote(cache: CipherCache? = null, tokens: LoginTokenStore? = saved(),
        sockets: WebSocket.Factory = this) = Remote(
        fixture.account, http, cache,
        clock = { fixture.now + test.testScheduler.currentTime },
        securityStorage = TestSecurityStore(fixture.record), tokenStorage = tokens,
        dispatcher = StandardTestDispatcher(test.testScheduler), socketFactory = sockets, rpcTransport = this,
    )

    override fun unary(request: UnaryHTTPRequest, onResult: (HTTPResponse) -> Unit): () -> Unit {
        val job = test.backgroundScope.launch {
            val name = request.methodSpec.path.substringAfterLast('/')
            calls += name
            before(name, request)
            delay(latencyMs)
            val bearer = request.headers.entries.firstOrNull { it.key.equals("Authorization", true) }?.value?.firstOrNull()
            val auth = name in setOf("GetChallenge", "Login")
            if (!auth && bearer.isNullOrEmpty()) anonymous++
            val unavailable = offline || name == "Forward"
            if (unavailable || (!auth && bearer != "Bearer $token") || (name == "GetEvents" && rejectNextHistory)) {
                rejectNextHistory = false
                onResult(HTTPResponse(trailers = emptyMap(), status = if (unavailable) 503 else 401,
                    headers = mapOf("content-type" to listOf("application/json")),
                    message = Buffer().writeUtf8(if (unavailable) "{\"code\":\"unavailable\"}" else "{\"code\":\"unauthenticated\"}")))
                return@launch
            }
            val response: com.google.protobuf.MessageLite = response(name, request) ?: when (name) {
                "GetChallenge" -> GetChallengeResponse.newBuilder().setChallenge(ByteString.copyFrom(ByteArray(32) { 9 })).build()
                "Login" -> {
                    val login = LoginRequest.parseFrom(request.message.clone().readByteArray())
                    check(login.deviceId == fixture.phone.id && login.protocolVersion == PROTOCOL_VERSION)
                    check(verifyEd25519(fixture.phone.ed, loginMessage(login.challenge.toByteArray()), login.signature.toByteArray()))
                    token = "credential-${++minted}"
                    LoginResponse.newBuilder().setToken(token).setExpiresMs(fixture.now + 3 * LOGIN_REFRESH_MARGIN_MS).build()
                }
                "ListMachines" -> ListMachinesResponse.newBuilder().addAllMachines(machineRows).build()
                "GetSecurityStates" -> {
                    val after = GetSecurityStatesRequest.parseFrom(request.message.clone().readByteArray()).afterEpoch.toULong()
                    GetSecurityStatesResponse.newBuilder().setHeadEpoch(securityRecord.head.core.epoch.toLong())
                        .addAllStates(securityRecord.chain.filter { it.core.epoch > after }.map {
                            SecurityState.newBuilder().setState(ByteString.copyFrom(DeviceSecurity.stateBytes(it))).build()
                        }).setLease(ByteString.copyFrom(securityRecord.lease?.bytes() ?: byteArrayOf())).build()
                }
                "ListDevices" -> ListDevicesResponse.getDefaultInstance()
                "ListRemoteSessions" -> ListRemoteSessionsResponse.newBuilder().addAllSessions(fixture.sessions.values).build()
                "GetAccountPrefs" -> GetAccountPrefsResponse.getDefaultInstance()
                "GetEvents" -> {
                    val page = GetEventsRequest.parseFrom(request.message.clone().readByteArray())
                    after += page.afterSeq
                    val available = fixture.events.getValue(page.sessionId)
                        .take(if (page.sessionId == "long") count else Int.MAX_VALUE)
                    val selected = available.filter { it.seq > page.afterSeq }.take(page.limit)
                    GetEventsResponse.newBuilder().addAllEvents(selected.filter { it.seq != missing })
                        .setMore(available.any { it.seq > (selected.lastOrNull()?.seq ?: page.afterSeq) }).build()
                }
                else -> error("Unexpected RPC: $name")
            }
            onResult(HTTPResponse(trailers = emptyMap(), status = 200, headers = mapOf("content-type" to listOf("application/proto")),
                message = Buffer().write(response.toByteArray())))
        }
        return { job.cancel() }
    }

    override fun stream(request: HTTPRequest, duplex: Boolean, onResult: suspend (StreamResult<Buffer>) -> Unit): Stream =
        error("No streaming RPCs in this fixture")

    override fun newWebSocket(request: Request, listener: WebSocketListener): WebSocket = Socket(request, listener).also {
        sockets += it
        test.backgroundScope.launch {
            delay(latencyMs)
            listener.onOpen(it, Response.Builder().request(request).protocol(Protocol.HTTP_1_1).code(101).message("Switching Protocols").build())
        }
    }

    fun frame(frame: ServerFrame) { sockets.last().emit(frame) }

    fun push(event: StoredEvent) {
        sockets.last().emit(ServerFrame.newBuilder().setEventsAppended(EventsAppended.newBuilder()
            .setSessionId("long").addEvents(event)).build())
    }

    private inner class Socket(val req: Request, val listener: WebSocketListener) : WebSocket {
        var canceled = false
        override fun request() = req
        override fun queueSize() = 0L
        override fun send(text: String) = error("Binary frames only")
        override fun send(bytes: okio.ByteString): Boolean {
            if (PeerFrame.parseFrom(bytes.toByteArray()).hello.token != token) {
                refused++
                listener.onClosing(this, 1008, "Rejected")
            } else emit(ServerFrame.newBuilder().setReady(Ready.newBuilder().setServerTimeMs(fixture.now)).build())
            return true
        }
        fun emit(frame: ServerFrame) { if (!canceled) listener.onMessage(this, frame.toByteArray().toByteString()) }
        override fun close(code: Int, reason: String?): Boolean { listener.onClosed(this, code, reason.orEmpty()); return true }
        override fun cancel() { canceled = true }
    }
}
