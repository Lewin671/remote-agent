package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.*
import java.io.File
import java.util.Base64
import kotlinx.coroutines.*
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.TestScope
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.*
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.ResponseBody.Companion.toResponseBody
import okio.Buffer
import okio.ByteString.Companion.toByteString

/** Immutable signed corpus, built once per test class from Go's vector fixture. */
internal class ColdOpenFixture(val historySize: Int = 501) {
    private val vectors = Json.parseToJsonElement(File(System.getProperty("domainVectors")).readText()).jsonObject
    private val genesisRaw = Base64.getDecoder().decode(vectors["genesis"]!!.jsonObject["state"]!!.jsonPrimitive.content)
    private val addedRaw = Base64.getDecoder().decode(vectors["added"]!!.jsonObject["state"]!!.jsonPrimitive.content)
    private val leaseBytes = Base64.getDecoder().decode(vectors["lease"]!!.jsonPrimitive.content)
    val now: Long = parseLeaseIssuedAt(leaseBytes)
    private val domain = vectors["domain"]!!.jsonPrimitive.content.unhex()
    private val genesisHash = vectors["genesis"]!!.jsonObject["hash"]!!.jsonPrimitive.content.unhex()
    private val accountId = vectors["account"]!!.jsonPrimitive.content
    private val contentSecret = vectors["content_secret_2"]!!.jsonPrimitive.content.unhex()
    private val phoneSeed = vectors["phone_seed"]!!.jsonPrimitive.content.unhex()
    private val phoneX = vectors["phone_x"]!!.jsonPrimitive.content.unhex()
    private val machineSeed = vectors["machine_seed"]!!.jsonPrimitive.content.unhex()
    private val machineX = vectors["machine_x"]!!.jsonPrimitive.content.unhex()
    private val seed = ByteArray(32) { 6 }
    private val contentEpoch = parseStateContentEpochAndParent(addedRaw).first

    val phoneEdPub = Corebind.ed25519PublicKey(phoneSeed)
    val phoneId = deviceIdForEdPublic(phoneEdPub)
    val machineId = deviceIdForEdPublic(Corebind.ed25519PublicKey(machineSeed))
    val account = Account("https://example.com", phoneId, accountId)

    val recordBytes = encodeV4RecordBytes(
        account = accountId,
        domain = domain,
        genesis = genesisHash,
        memberId = phoneId,
        member = true,
        server = "https://example.com",
        states = listOf(genesisRaw, addedRaw),
        lease = leaseBytes,
        floorMs = now,
        contentKeys = listOf(contentEpoch to contentSecret),
    )

    private val machineRecordBytes = encodeV4RecordBytes(
        account = accountId,
        domain = domain,
        genesis = genesisHash,
        memberId = machineId,
        member = true,
        server = "https://example.com",
        states = listOf(genesisRaw, addedRaw),
        lease = leaseBytes,
        floorMs = now,
        contentKeys = listOf(contentEpoch to contentSecret),
    )

    fun newPhoneStore(): MemorySecurityStoreV4 = MemorySecurityStoreV4().apply {
        seedTestRecord(
            edPrivate = phoneSeed,
            edPublic = phoneEdPub,
            xPrivate = phoneX,
            xPublic = Corebind.x25519PublicKey(phoneX),
            recordBytes = recordBytes,
        )
    }

    private fun newMachineStore(): MemorySecurityStoreV4 = MemorySecurityStoreV4().apply {
        seedTestRecord(
            edPrivate = machineSeed,
            edPublic = Corebind.ed25519PublicKey(machineSeed),
            xPrivate = machineX,
            xPublic = Corebind.x25519PublicKey(machineX),
            recordBytes = machineRecordBytes,
        )
    }

    private fun sealMachine(purpose: PayloadKind, sid: String, seq: Long, plain: ByteArray): Envelope =
        CoreMemoryBridge(newMachineStore(), "https://example.com", machineId, accountId, clock = { now }).use { b ->
            b.seal(
                purpose = purpose.number,
                machine = machineId,
                session = sid,
                seq = seq,
                plain = plain,
                overrideSessionSeed = if (purpose == PayloadKind.PAYLOAD_KIND_EVENT) seed else null,
            )
        }

    fun machine(revision: Long, hostname: String): Machine = Machine.newBuilder()
        .setId(machineId)
        .setOnline(true)
        .setMeta(
            sealMachine(
                PayloadKind.PAYLOAD_KIND_MACHINE_META,
                "",
                revision,
                MachineMeta.newBuilder().setHostname(hostname).build().toByteArray(),
            ),
        )
        .build()

    fun device(revision: Long, name: String): Device {
        val env = CoreMemoryBridge(newPhoneStore(), "https://example.com", phoneId, accountId, clock = { now }).use { b ->
            b.seal(
                purpose = PayloadKind.PAYLOAD_KIND_DEVICE_META.number,
                request = phoneId,
                seq = revision,
                plain = DeviceMeta.newBuilder().setName(name).build().toByteArray(),
            )
        }
        return Device.newBuilder().setId(phoneId).setKind(DeviceKind.DEVICE_KIND_CLIENT).setMeta(env).build()
    }

    val sessions: Map<String, RemoteSession> =
        CoreMemoryBridge(newMachineStore(), "https://example.com", machineId, accountId, clock = { now }).use { b ->
            listOf("long", "short", "empty").associateWith { sid ->
                RemoteSession.newBuilder()
                    .setSessionId(sid)
                    .setMachineId(machineId)
                    .setKey(b.seal(PayloadKind.PAYLOAD_KIND_SESSION_KEY.number, machine = machineId, session = sid, seq = 1L, plain = seed))
                    .setMeta(
                        b.seal(
                            PayloadKind.PAYLOAD_KIND_SESSION_META.number,
                            machine = machineId,
                            session = sid,
                            seq = 1L,
                            plain = Session.newBuilder().setId(sid).setTitle(sid).build().toByteArray(),
                        ),
                    )
                    .setLastSeq(when (sid) { "long" -> historySize; "short" -> 2; else -> 0 }.toLong())
                    .build()
            }
        }

    fun session(sid: String, revision: Long, status: SessionStatus): RemoteSession =
        sessions.getValue(sid).toBuilder().setMeta(
            sealMachine(
                PayloadKind.PAYLOAD_KIND_SESSION_META,
                sid,
                revision,
                Session.newBuilder().setId(sid).setStatus(status).build().toByteArray(),
            ),
        ).build()

    fun taskApproval(): RemoteSession = sessions.getValue("empty").toBuilder().setMeta(
        sealMachine(
            PayloadKind.PAYLOAD_KIND_SESSION_META,
            "empty",
            2L,
            Session.newBuilder()
                .setId("empty")
                .setCwd("/scheduled")
                .setTaskOwned(true)
                .setPendingApprovals(1)
                .setStatus(SessionStatus.SESSION_STATUS_BLOCKED)
                .build()
                .toByteArray(),
        ),
    ).build()

    val events: Map<String, List<StoredEvent>> =
        CoreMemoryBridge(newMachineStore(), "https://example.com", machineId, accountId, clock = { now }).use { b ->
            listOf("long", "short", "empty").associateWith { sid ->
                (1..when (sid) { "long" -> historySize + 1; "short" -> 2; else -> 0 }).map { seq ->
                    val event = Event.newBuilder()
                        .setSeq(seq.toLong())
                        .setSessionId(sid)
                        .setAgentMessage(AgentMessage.newBuilder().setText("Message $seq"))
                        .build()
                    StoredEvent.newBuilder()
                        .setSeq(seq.toLong())
                        .setEnvelope(
                            b.seal(
                                PayloadKind.PAYLOAD_KIND_EVENT.number,
                                machine = machineId,
                                session = sid,
                                seq = seq.toLong(),
                                plain = event.toByteArray(),
                                overrideSessionSeed = seed,
                            ),
                        )
                        .build()
                }
            }
        }
}

@OptIn(ExperimentalCoroutinesApi::class)
internal class ColdOpenPeer(val fixture: ColdOpenFixture, val test: TestScope) : WebSocket.Factory {
    val calls = mutableListOf<String>()
    val after = mutableListOf<Long>()
    var machineRows = listOf(Machine.newBuilder().setId(fixture.machineId).setOnline(true).build())
    var response: (String, Request) -> com.google.protobuf.MessageLite? = { _, _ -> null }
    var count = fixture.historySize
    var missing = 0L
    var latencyMs = 0L
    var before: suspend (String, Request) -> Unit = { _, _ -> }
    var rejectNextHistory = false
    var offline = false
    var denyLogin = false
    var unavailableMethods = setOf("Forward")
    var anonymous = 0
    var refused = 0
    private var minted = 0
    private var token = "saved-credential"
    private val sockets = mutableListOf<Socket>()
    private val workerPool = java.util.concurrent.Executors.newCachedThreadPool { r ->
        Thread(r, "cold-open-http").apply { isDaemon = true }
    }
    private val http: OkHttpClient = buildVirtualHttp()

    private fun buildVirtualHttp(): OkHttpClient {
        val callLatches = ThreadLocal<Pair<java.util.concurrent.CountDownLatch, java.util.concurrent.CountDownLatch>>()
        val inFlight = java.util.concurrent.ConcurrentHashMap<Call, Pair<Job, java.util.concurrent.CompletableFuture<Response>>>()
        val dispatcher = Dispatcher(
            object : java.util.concurrent.AbstractExecutorService() {
                override fun shutdown() {}
                override fun shutdownNow(): MutableList<Runnable> = mutableListOf()
                override fun isShutdown() = false
                override fun isTerminated() = false
                override fun awaitTermination(timeout: Long, unit: java.util.concurrent.TimeUnit) = true
                override fun execute(command: Runnable) {
                    val entered = java.util.concurrent.CountDownLatch(1)
                    val finished = java.util.concurrent.CountDownLatch(1)
                    workerPool.execute {
                        callLatches.set(entered to finished)
                        try {
                            command.run()
                        } finally {
                            callLatches.remove()
                            entered.countDown()
                            finished.countDown()
                        }
                    }
                    entered.await(5, java.util.concurrent.TimeUnit.SECONDS)
                }
            },
        ).apply {
            maxRequests = 128
            maxRequestsPerHost = 64
        }
        return Api.defaultHttp().newBuilder()
            .dispatcher(dispatcher)
            .eventListenerFactory {
                object : EventListener() {
                    override fun canceled(call: Call) {
                        inFlight.remove(call)?.let { (job, done) ->
                            job.cancel()
                            done.completeExceptionally(java.io.IOException("Canceled"))
                        }
                    }
                }
            }
            .addInterceptor { chain ->
                val request = chain.request()
                val call = chain.call()
                val bodyBytes = request.body?.let { b -> Buffer().also { b.writeTo(it) }.readByteArray() } ?: ByteArray(0)
                val (entered, finished) = callLatches.get() ?: (null to null)
                val done = java.util.concurrent.CompletableFuture<Response>()
                val job = test.backgroundScope.launch {
                    try {
                        val res = handleRpc(request, bodyBytes)
                        done.complete(res)
                        finished?.await(5, java.util.concurrent.TimeUnit.SECONDS)
                    } catch (t: Throwable) {
                        done.completeExceptionally(t)
                        if (t is CancellationException) throw t
                    }
                }
                inFlight[call] = job to done
                if (call.isCanceled()) {
                    inFlight.remove(call)
                    job.cancel()
                    done.completeExceptionally(java.io.IOException("Canceled"))
                }
                entered?.countDown()
                try {
                    done.get()
                } catch (e: java.util.concurrent.ExecutionException) {
                    val cause = e.cause
                    if (cause is java.io.IOException) throw cause
                    throw java.io.IOException(cause?.message ?: "interrupted", cause)
                } finally {
                    inFlight.remove(call)
                }
            }
            .build()
    }

    fun saved(): MemoryTokens = MemoryTokens(fixture.newPhoneStore()).apply {
        saved = LoginToken(token, fixture.now + 3 * LOGIN_REFRESH_MARGIN_MS)
    }

    fun remote(
        cache: CipherCache? = null,
        store: SecurityStoreV4 = saved(),
        sockets: WebSocket.Factory = this,
    ) = Remote(
        account = fixture.account,
        http = http,
        cache = cache,
        clock = { fixture.now + test.testScheduler.currentTime },
        securityStore = store,
        dispatcher = StandardTestDispatcher(test.testScheduler),
        socketFactory = sockets,
        monotonic = { test.testScheduler.currentTime },
    )

    private suspend fun handleRpc(request: Request, bodyBytes: ByteArray): Response {
        val name = request.url.encodedPath.substringAfterLast('/')
        calls += name
        before(name, request)
        delay(latencyMs)
        val bearer = request.header("Authorization")
        val auth = name in setOf("GetChallenge", "Login")
        if (!auth && bearer.isNullOrEmpty()) anonymous++
        val unavailable = offline || name in unavailableMethods
        val denied = denyLogin && name == "Login"
        if (denied || unavailable || (!auth && bearer != "Bearer $token") || (name == "GetEvents" && rejectNextHistory)) {
            rejectNextHistory = false
            val errJson = if (denied) "{\"code\":\"permission_denied\"}"
                else if (unavailable) "{\"code\":\"unavailable\"}"
                else "{\"code\":\"unauthenticated\"}"
            return Response.Builder()
                .request(request)
                .protocol(Protocol.HTTP_1_1)
                .code(if (denied) 403 else if (unavailable) 503 else 401)
                .message("Error")
                .header("content-type", "application/json")
                .header("Remote-Agent-Relay-Epoch", "fixture-epoch")
                .body(errJson.toResponseBody("application/json".toMediaType()))
                .build()
        }
        val response: com.google.protobuf.MessageLite = response(name, request) ?: when (name) {
            "GetChallenge" -> GetChallengeResponse.newBuilder().setChallenge(ByteString.copyFrom(ByteArray(32) { 9 })).build()
            "Login" -> {
                val login = LoginRequest.parseFrom(bodyBytes)
                check(login.deviceId == fixture.phoneId && login.protocolVersion == RELAY_PROTOCOL_VERSION)
                check(Corebind.ed25519Verify(fixture.phoneEdPub, loginMessage(login.challenge.toByteArray()), login.signature.toByteArray()))
                token = "credential-${++minted}"
                LoginResponse.newBuilder().setToken(token).setExpiresMs(fixture.now + 3 * LOGIN_REFRESH_MARGIN_MS).build()
            }
            "ListMachines" -> ListMachinesResponse.newBuilder().addAllMachines(machineRows).build()
            "GetSecurityStates" -> GetSecurityStatesResponse.newBuilder().setHeadEpoch(2L).build()
            "ListDevices" -> ListDevicesResponse.getDefaultInstance()
            "ListRemoteSessions" -> ListRemoteSessionsResponse.newBuilder().addAllSessions(fixture.sessions.values).build()
            "GetAccountPrefs" -> GetAccountPrefsResponse.getDefaultInstance()
            "GetEvents" -> {
                val page = GetEventsRequest.parseFrom(bodyBytes)
                after += page.afterSeq
                val available = fixture.events.getValue(page.sessionId)
                    .take(if (page.sessionId == "long") count else Int.MAX_VALUE)
                val selected = available.filter { it.seq > page.afterSeq }.take(page.limit)
                GetEventsResponse.newBuilder()
                    .addAllEvents(selected.filter { it.seq != missing })
                    .setMore(available.any { it.seq > (selected.lastOrNull()?.seq ?: page.afterSeq) })
                    .build()
            }
            else -> error("Unexpected RPC: $name")
        }
        return Response.Builder()
            .request(request)
            .protocol(Protocol.HTTP_1_1)
            .code(200)
            .message("OK")
            .header("content-type", "application/proto")
            .header("Remote-Agent-Relay-Epoch", "fixture-epoch")
            .body(response.toByteArray().toResponseBody("application/proto".toMediaType()))
            .build()
    }

    override fun newWebSocket(request: Request, listener: WebSocketListener): WebSocket = Socket(request, listener).also {
        sockets += it
        test.backgroundScope.launch {
            delay(latencyMs)
            if (offline) listener.onFailure(it, java.io.IOException("offline"), null)
            else listener.onOpen(
                it,
                Response.Builder()
                    .request(request)
                    .protocol(Protocol.HTTP_1_1)
                    .code(101)
                    .message("Switching Protocols")
                    .build(),
            )
        }
    }

    fun frame(frame: ServerFrame) { sockets.last().emit(frame) }
    fun disconnect() { sockets.toList().forEach { it.close(1001, "disconnected"); it.cancel() } }

    fun push(event: StoredEvent) {
        sockets.last().emit(
            ServerFrame.newBuilder()
                .setEventsAppended(EventsAppended.newBuilder().setSessionId("long").addEvents(event))
                .build(),
        )
    }

    private inner class Socket(val req: Request, val listener: WebSocketListener) : WebSocket {
        var canceled = false
        private var heartbeat: Job? = null
        override fun request() = req
        override fun queueSize() = 0L
        override fun send(text: String) = error("Binary frames only")
        override fun send(bytes: okio.ByteString): Boolean {
            if (PeerFrame.parseFrom(bytes.toByteArray()).hello.token != token) {
                refused++
                listener.onClosing(this, 1008, "Rejected")
            } else {
                emit(ServerFrame.newBuilder().setReady(Ready.newBuilder().setServerTimeMs(fixture.now + test.testScheduler.currentTime)).build())
                heartbeat = test.backgroundScope.launch {
                    while (isActive && !canceled) {
                        delay(1000)
                        emit(ServerFrame.newBuilder().setHeartbeat(Heartbeat.getDefaultInstance()).build())
                    }
                }
            }
            return true
        }
        fun emit(frame: ServerFrame) { if (!canceled) listener.onMessage(this, frame.toByteArray().toByteString()) }
        override fun close(code: Int, reason: String?): Boolean { listener.onClosed(this, code, reason.orEmpty()); return true }
        override fun cancel() { canceled = true; heartbeat?.cancel() }
    }
}
