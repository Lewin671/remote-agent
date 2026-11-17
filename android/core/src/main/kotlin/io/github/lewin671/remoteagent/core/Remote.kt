package io.github.lewin671.remoteagent.core

import com.connectrpc.Code
import com.connectrpc.ConnectException
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.core.transport.*
import io.github.lewin671.remoteagent.core.transport.Link as DeviceLink
import io.github.lewin671.remoteagent.proto.*
import io.github.lewin671.remoteagent.proto.Event
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import okhttp3.OkHttpClient
import okhttp3.WebSocket
import java.io.IOException
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong

/** Where envelopes are cached between runs. Only ciphertext is stored. */
interface CipherCache {
    suspend fun setSessionOpen(sessionId: String, open: Boolean) {}
    fun close() {}
    suspend fun machines(): List<Machine>
    suspend fun sessions(): List<RemoteSession>
    suspend fun newestEvents(sessionId: String): List<StoredEvent>
    suspend fun eventPage(sessionId: String, afterSeq: Long): List<StoredEvent>
    suspend fun putMachine(m: Machine)
    suspend fun putSession(s: RemoteSession)
    suspend fun putEvents(sessionId: String, events: List<StoredEvent>)
    suspend fun dropEvents(sessionId: String)
    suspend fun removeMachine(id: String)
    suspend fun removeSession(id: String)
    suspend fun unlistSession(id: String)
    suspend fun keepOwner(s: RemoteSession) {}
    suspend fun image(key: String): ByteArray? = null
    suspend fun putImage(key: String, envelope: ByteArray) {}
    suspend fun blob(name: String): ByteArray? = null
    suspend fun putBlob(name: String, data: ByteArray) {}
    suspend fun removeBlob(name: String) {}
}

/** A sealed command ready to send; resending it reuses its request ID and deadline. */
class PreparedCommand(
    val machineId: String,
    val sessionId: String,
    val requestId: String,
    val deadlineMs: Long,
    val envelope: Envelope,
    command: Command? = null,
    serializedCommand: ByteArray? = null,
    prior: Boolean,
) {
    @Volatile internal var authPhases = LongArray(4)
    private val payload = serializedCommand?.copyOf() ?: command?.toByteArray()
    internal val command: Command? = payload?.let(Command::parseFrom)
    fun commandBytes(): ByteArray? = payload?.copyOf()
    internal val sealMutex = Mutex()
    internal var authorizedEnvelope: Envelope? = null
    internal val dispatch = DispatchState(prior)
    val mayHaveRun: Boolean get() = dispatch.unresolved

    override fun toString() = "PreparedCommand(request=$requestId)"
}

interface TermListener {
    fun onFrame(frame: TerminalFrame)
    fun onClose(reason: MessageText)
}

class TermConn internal constructor(
    private val ws: FrameStream,
    private val sendFrame: (TerminalFrame) -> Unit,
    private val closeLocal: () -> Unit,
) {
    fun send(frame: TerminalFrame) = sendFrame(frame)
    fun close() = cleanup(closeLocal, { ws.close() })
}

internal fun ucmp(a: Long, b: Long): Int = java.lang.Long.compareUnsigned(a, b)
internal fun umax(a: Long, b: Long): Long = if (ucmp(a, b) >= 0) a else b

const val COMMAND_TTL_MS = 60_000L

private const val STREAM_PING_SECONDS = 20L
private const val STREAM_OPEN_MS = 15_000L
private const val STREAM_FRAME_BYTES = 2 * 1024 * 1024
private const val STREAM_QUEUE_BYTES = 8L * 1024 * 1024
private const val POLICY_VIOLATION = 1008
private const val CACHE_REF_LIMIT = 4096

private class AccountRemoved : Exception()

@OptIn(ExperimentalCoroutinesApi::class)
class Remote(
    val account: Account,
    private val http: OkHttpClient,
    private val cache: CipherCache? = null,
    private val log: (String) -> Unit = {},
    private val clock: () -> Long = System::currentTimeMillis,
    private val frameQueue: Int = 1024,
    @Suppress("unused") private val historyIdleMs: Long = 60_000,
    @Suppress("unused") private val historyIdleCount: Int = 8,
    @Suppress("unused") private val historyIdleBytes: Long = 16L * 1024 * 1024,
    @Suppress("unused") private val streamOpenMs: Long = STREAM_OPEN_MS,
    @Suppress("unused") private val linkGraceMs: Long = 10_000,
    private val securityStorage: DeviceSecurity.Storage? = null,
    private val tokenStorage: LoginTokenStore? = null,
    private val scheduleSeen: ScheduleSeen = ScheduleSeen(),
    dispatcher: kotlinx.coroutines.CoroutineDispatcher? = null,
    private val socketFactory: WebSocket.Factory? = null,
    rpcTransport: com.connectrpc.http.HTTPClientInterface? = null,
    private val onSessionRemoved: suspend (String) -> Unit = {},
    private val monotonic: () -> Long = { System.nanoTime() / 1_000_000 },
    private val rtcFactory: RtcFactory? = null,
    private val directPrefs: DirectPreferenceStore = InMemoryDirectPreferenceStore(),
    private val stunUrls: List<String>? = null,
    private val directJitter: (() -> Double)? = null,
) {
    private val dispatcher = dispatcher ?: Dispatchers.IO.limitedParallelism(1)
    private val cpuDispatcher = if (dispatcher == null) Dispatchers.Default else this.dispatcher
    private val bulkCpuDispatcher =
        if (dispatcher == null) io.github.lewin671.remoteagent.core.transport.CpuLanes.bulk else this.dispatcher
    private val directDispatcher =
        if (dispatcher == null || dispatcher === Dispatchers.Unconfined) Dispatchers.IO.limitedParallelism(1)
        else this.dispatcher

    private fun cpuLane(p: PreparedCommand) =
        if (p.command?.hasGetSharedFileChunk() == true || p.command?.hasPutImageChunk() == true) bulkCpuDispatcher
        else cpuDispatcher

    private val peerJobs = java.util.concurrent.atomic.AtomicInteger()
    private val device = SigningKey(account.deviceSeed)
    val api = Api(account.server, http, ::rejectToken, rpcTransport)
    @Volatile private var relayObservation = RemoteState()
    private val directTransfers = LinkedHashMap<String, UploadKind>()
    private val scope = CoroutineScope(SupervisorJob() + this.dispatcher)

    // --- Token & stream runtime state ---

    private val tokenMutex = Mutex()
    @Volatile private var cachedToken: LoginToken? = null
    private var inFlightToken: CompletableDeferred<LoginToken>? = null
    @Volatile private var inFlightLoginJob: Job? = null
    @Volatile private var networkAvailable = true
    @Volatile private var lastNetworkSignature: String? = null
    @Volatile private var networkGeneration = 0L
    @Volatile private var running = false
    @Volatile private var serverAhead = false

    private val streamMutex = Mutex()
    private var activeLegId = 0L
    @Volatile private var activeSocket: FrameStream? = null
    internal val activeStreamForTest: FrameStream? get() = activeSocket
    private var activeLegJob: Job? = null

    private sealed interface PendingCacheObject {
        class MachineObj(val m: Machine) : PendingCacheObject
        class SessionObj(val s: RemoteSession) : PendingCacheObject
        class EventObj(val e: StoredEvent) : PendingCacheObject
        class PrefsObj(val p: AccountPrefsObject) : PendingCacheObject
    }

    private val nextCacheRefId = AtomicLong(1L)
    private val pendingCacheObjects = object : LinkedHashMap<String, PendingCacheObject>(256) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, PendingCacheObject>?): Boolean =
            size > CACHE_REF_LIMIT
    }

    private fun stashCacheObject(obj: PendingCacheObject): String {
        val ref = "c:${nextCacheRefId.getAndIncrement()}"
        synchronized(pendingCacheObjects) {
            pendingCacheObjects[ref] = obj
        }
        return ref
    }

    private fun takeCacheObject(ref: String): PendingCacheObject? =
        synchronized(pendingCacheObjects) { pendingCacheObjects.remove(ref) }

    // --- Direct peer engine & Security ---

    private val _directPeers = MutableStateFlow<Map<String, PeerState>>(emptyMap())
    private val lastDirectReady = ConcurrentHashMap<String, Boolean>()

    internal val direct: DirectEngine = DirectEngine(
        account = account,
        rtcFactory = rtcFactory,
        preferenceStore = directPrefs,
        authorize = { security.sync(5_000L); Unit },
        changed = { scope.launch { if (running) onDirectChanged() } },
        clock = clock,
        monotonic = monotonic,
        stunUrls = stunUrls ?: DirectEngine.defaultStunUrls(account.server),
        dispatcher = directDispatcher,
        jitter = directJitter ?: { java.util.concurrent.ThreadLocalRandom.current().nextDouble() },
    ).also { engine -> engine.probe = { id, path -> probeRead(id, path) } }

    private fun onDirectChanged() {
        val syncState = coreHost.syncStateView.value
        val updatedPeers = LinkedHashMap<String, PeerState>()
        for (m in syncState.machinesList) {
            val id = m.machineId
            val ready = direct.ready(id, RouteHint.Read)
            if (lastDirectReady.put(id, ready) != ready) {
                coreHost.notifyMachineRouteStatus(id, ready)
            }
            val base = MachineView(id, m.online, m.lastSeenMs, if (m.hasMeta()) m.meta else null, m.metaRev)
            updatedPeers[id] = RelayPath.discovery(base).copy(
                reachable = ready || m.online,
                relayRequired = !ready,
                knownOffline = !ready && !m.online,
                protocolVersion = if (ready) PROTOCOL_VERSION else m.protocolVersion.takeIf { it > 0 },
                capabilities = direct.capabilities(id) ?: m.meta.takeIf { m.hasMeta() && it.hasCapabilities() }?.capabilities,
                fileDirect = direct.filePath(id) == RouteName.DIRECT,
                directStatus = direct.directStatus(id),
            )
        }
        _directPeers.value = updatedPeers
    }

    internal val deviceLink = DeviceLink(RelayPath(
        api = api,
        sockets = { socketFactory ?: streamHttp },
        token = { currentToken() },
        renew = { used, _ -> if (used != null) discardToken(used); currentToken() },
        observe = { id ->
            relayObservation.let {
                val directReady = direct.ready(id, RouteHint.Read)
                val base = RelayPath.discovery(
                    it.machines[id],
                    ready = directReady || it.conn == ConnState.ONLINE,
                    authorized = !it.removed,
                    unlinked = it.unlinked,
                )
                base.copy(
                    reachable = directReady || it.machines[id]?.online == true,
                    relayRequired = !directReady,
                    knownOffline = !directReady && it.machines[id]?.online == false,
                    protocolVersion = if (directReady) PROTOCOL_VERSION else base.protocolVersion,
                    capabilities = direct.capabilities(id) ?: base.capabilities,
                    fileDirect = direct.filePath(id) == RouteName.DIRECT,
                    directStatus = direct.directStatus(id),
                )
            }
        },
    ))

    fun peer(machineId: String?) = state.value.peer(machineId)
    fun directEnabled(machineId: String): Boolean = direct.enabled(machineId)
    fun refreshDirect(machineId: String) = direct.refresh(machineId)
    fun setDirectEnabled(machineId: String, enabled: Boolean) = direct.setEnabled(machineId, enabled)
    fun filePath(machineId: String): RouteName = direct.filePath(machineId)
    fun monotonicMillis(): Long = monotonic()
    @Volatile internal var parseServerFrame: (ByteArray) -> ServerFrame = ServerFrame::parseFrom

    val security = DeviceSecurityClient(
        securityStorage ?: MissingDeviceSecurityStorage,
        account.deviceId,
        device,
        expectedAccount = account.accountId.ifEmpty { null },
        clock = clock,
        onSync = { record, started -> direct.synced(record, started) },
        onSyncFailed = { direct.invalidate() },
        onInstall = { record -> direct.install(record) },
    ) { after ->
        api.client.getSecurityStates(
            GetSecurityStatesRequest.newBuilder().setAfterEpoch(after.toLong()).build(),
            Api.headers(currentToken()),
        ).value()
    }

    private val streamHttp by lazy {
        http.newBuilder()
            .pingInterval(STREAM_PING_SECONDS, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.MILLISECONDS)
            .build()
    }

    private val models = HashMap<String, Deferred<ListModelsResponse>>()
    private val sessionKeys = ConcurrentHashMap<String, ByteArray>()
    private val archiveMachines = ConcurrentHashMap.newKeySet<String>()
    private val trustedMembers = ConcurrentHashMap<String, DeviceSecurity.Member>()
    @Volatile private var recoveryObjectsPending = true
    private var authorityJob = SupervisorJob(scope.coroutineContext[Job])

    @Volatile var homeScopeStore: HomeScopeStore? = null
    @Volatile var uiMessageResolver: MessageResolver = { it.key }

    val coreHost = CoreHost(
        workerDispatcher = if (dispatcher != null) this.dispatcher else null,
        clock = clock,
        monotonic = monotonic,
    )
    val homeView: StateFlow<HomeViewModel> get() = coreHost.homeView
    val projectView: StateFlow<ProjectViewModel> get() = coreHost.projectView
    val newSessionView: StateFlow<NewSessionViewModel> get() = coreHost.newSessionView
    val coreNavigation: SharedFlow<NavigateEffect> get() = coreHost.navigationEvents

    // --- Transitional Step 3/4 view & schedule projection ---

    @Volatile private var testStateOverride: RemoteState? = null
    private val _testStateFlow = MutableStateFlow<RemoteState?>(null)
    private val testEventFlows = ConcurrentHashMap<String, MutableStateFlow<SessionEvents>>()
    private val sessionEventFlows = ConcurrentHashMap<String, StateFlow<SessionEvents>>()
    private val pendingSessionLookups = ConcurrentHashMap<String, java.util.concurrent.CopyOnWriteArrayList<CompletableDeferred<Boolean>>>()
    private val _networkAvailableFlow = MutableStateFlow(true)
    private val _state = MutableStateFlow(RemoteState())
    val state: StateFlow<RemoteState> = _state.asStateFlow()

    private fun updateProjectedStateNow() {
        val next = testStateOverride ?: coreHost.syncStateView.value.toRemoteState(_directPeers.value, networkAvailable)
        relayObservation = next
        _state.value = next
    }

    init {
        coreHost.onRender = { targets ->
            if (io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_SYNC_STATE in targets) {
                updateProjectedStateNow()
            }
        }
        scope.launch {
            combine(
                coreHost.syncStateView,
                _directPeers,
                _networkAvailableFlow,
                _testStateFlow,
            ) { syncView, peers, net, override ->
                (override ?: syncView.toRemoteState(peers, net)).also { relayObservation = it }
            }.collect { _state.value = it }
        }
    }

    private val scheduleRepository = lazy {
        ScheduleRepository(
            scope,
            state,
            send = { machine, request ->
                machineCommand(machine, Command.newBuilder().setSchedule(request).build()).schedule.resultJson.toByteArray()
            },
            read = { cachedSchedules(it) },
            clear = { cache?.removeBlob("schedule:$it"); Unit },
            seen = scheduleSeen,
        ).also { repo ->
            scope.launch {
                combine(repo.state, repo.unread, state) { st, unread, rs ->
                    buildScheduleHints(st, unread, rs)
                }.collect { hints ->
                    coreHost.updateScheduleHints(hints)
                }
            }
        }
    }

    val schedules get() = scheduleRepository.value

    private fun buildScheduleHints(
        scheduleState: Map<String, ScheduleSnapshot>,
        unreadMap: Map<ScheduleTaskKey, Boolean>,
        rs: RemoteState,
    ): List<SessionScheduleHint> {
        val bySession = HashMap<String, MutableList<ScheduledTask>>()
        for (ms in scheduleState.values) {
            for (t in ms.tasks) {
                if (!scheduleSupported(rs.machines[t.machine])) continue
                if (t.target.text("type") != "resume_session") continue
                val sid = t.target.text("session")
                if (sid.isNotEmpty()) bySession.getOrPut(sid) { ArrayList() }.add(t)
            }
        }
        val hints = ArrayList<SessionScheduleHint>(bySession.size)
        for ((sid, list) in bySession) {
            val freq = if (list.size == 1) scheduleFrequency(list[0].trigger, copy = uiMessageResolver) else ""
            val unread = list.any { unreadMap[ScheduleTaskKey(it.machine, it.id)] == true }
            hints.add(
                SessionScheduleHint.newBuilder()
                    .setSessionId(sid)
                    .setScheduleName(freq)
                    .setUnread(unread)
                    .build(),
            )
        }
        return hints
    }

    private fun currentScheduleMember(machine: String): Boolean =
        security.record().head.core.members.any { it.id == machine && it.kind == 1u }

    internal suspend fun cachedSchedules(machine: String): ByteArray? = withContext(dispatcher) {
        if (!currentScheduleMember(machine)) {
            cache?.removeBlob("schedule:$machine")
            return@withContext null
        }
        val raw = cache?.blob("schedule:$machine") ?: return@withContext null
        val snapshot = Json.parseToJsonElement(raw.decodeToString()).jsonObject
        val (_, reply) = openSecureMessage(
            Envelope.parseFrom(java.util.Base64.getDecoder().decode(snapshot.text("envelope"))),
            Place(PayloadKind.PAYLOAD_KIND_COMMAND_RESULT, machineId = machine, requestId = snapshot.text("request")),
            CommandResult::parseFrom,
            live = false,
        )
        require(reply.hasSchedule() && reply.errorCode.isEmpty())
        if (!currentScheduleMember(machine)) {
            cache?.removeBlob("schedule:$machine")
            return@withContext null
        }
        reply.schedule.resultJson.toByteArray()
    }

    init {
        wireCoreHost()
    }

    // --- M2 Effect executors ---

    private fun wireCoreHost() {
        coreHost.onSaveHomeScope = { eff ->
            homeScopeStore?.writeToken(eff.scopeToken.ifEmpty { encodeHomeScopeToken(account.localKey, eff.scopeKey) })
        }
        coreHost.onEnsureToken = { eff -> scope.launch { ensureTokenForCore(eff.opId, eff.forceLogin) } }
        coreHost.onDiscardToken = { eff -> scope.launch { discardToken(eff.token) } }
        coreHost.onSyncSecurity = { eff -> scope.launch { syncSecurityForCore(eff.opId, eff.networkGeneration) } }
        coreHost.onOpenStream = { eff -> scope.launch { openStreamForCore(eff.legId, eff.token) } }
        coreHost.onCloseStream = { eff -> scope.launch { closeStreamLeg(eff.legId) } }
        coreHost.onCacheRead = { eff -> scope.launch { executeCacheRead(eff) } }
        coreHost.onCacheWrite = { eff -> scope.launch { executeCacheWrite(eff) } }
        coreHost.onCacheDelete = { eff -> scope.launch { executeCacheDelete(eff) } }
        coreHost.onRpc = { eff -> scope.launch { executeRpc(eff) } }
        coreHost.onDispatchCommand = { eff -> scope.launch { executeDispatchCommand(eff) } }
    }

    // --- Token Gate ---

    private suspend fun ensureTokenForCore(opId: Long, @Suppress("UNUSED_PARAMETER") forceLogin: Boolean) {
        val b = TokenResultEvent.newBuilder().setOpId(opId)
        try {
            val tok = resolveLoginToken(forceLogin = false)
            b.setToken(tok.token).setExpiresMs(tok.expiresMs)
        } catch (_: AccountRemoved) {
            b.setRemoved(true).setErrorCode("unauthenticated")
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            report(e)
            b.setUnresolvedHost(unresolvedHost(e))
            if (e is ConnectException) b.setErrorCode(e.code.name.lowercase())
            b.setError(e.message ?: e.javaClass.simpleName)
        }
        coreHost.dispatch(CoreEvent.newBuilder().setTokenResult(b).build())
    }

    suspend fun currentToken(): String = resolveLoginToken(forceLogin = false).token

    private class NetworkResetException : CancellationException("network changed during login")

    private suspend fun resolveLoginToken(forceLogin: Boolean): LoginToken {
        while (true) {
            val now = clock()
            if (!forceLogin) {
                cachedToken?.takeIf { it.expiresMs - LOGIN_REFRESH_MARGIN_MS > now }?.let { return it }
                tokenStorage?.loadLoginToken(account)?.let { stored ->
                    if (stored.expiresMs - LOGIN_REFRESH_MARGIN_MS > now) {
                        cachedToken = stored
                        return stored
                    }
                    tokenStorage.clearLoginToken(account)
                }
            }
            val (deferred, owner) = tokenMutex.withLock {
                val nowLocked = clock()
                if (!forceLogin) {
                    cachedToken?.takeIf { it.expiresMs - LOGIN_REFRESH_MARGIN_MS > nowLocked }?.let { return it }
                }
                val cur = inFlightToken
                if (cur != null && !cur.isCompleted) {
                    cur to false
                } else {
                    CompletableDeferred<LoginToken>().also { inFlightToken = it } to true
                }
            }
            if (!owner) {
                try {
                    return deferred.await()
                } catch (_: NetworkResetException) {
                    continue
                }
            }
            val loginJob = scope.launch {
                try {
                    val res = loginResponse(api, account)
                    val tok = LoginToken(res.token, res.expiresMs)
                    cachedToken = tok
                    tokenStorage?.saveLoginToken(account, tok)
                    deferred.complete(tok)
                } catch (t: Throwable) {
                    if (loginDenied(t)) {
                        cachedToken = null
                        tokenStorage?.clearLoginToken(account)
                        deferred.completeExceptionally(AccountRemoved())
                    } else {
                        deferred.completeExceptionally(t)
                    }
                } finally {
                    tokenMutex.withLock {
                        if (inFlightToken === deferred) {
                            inFlightToken = null
                            inFlightLoginJob = null
                        }
                    }
                }
            }
            inFlightLoginJob = loginJob
            try {
                return deferred.await()
            } catch (_: NetworkResetException) {
                continue
            }
        }
    }

    private suspend fun discardToken(token: String) {
        if (token.isEmpty()) return
        tokenMutex.withLock {
            if (cachedToken?.token == token) {
                cachedToken = null
                tokenStorage?.clearLoginToken(account)
            }
        }
    }

    private fun rejectToken(rejected: String) {
        scope.launch {
            discardToken(rejected)
        }
    }

    // --- Security Gate ---

    private suspend fun syncSecurityForCore(opId: Long, networkGeneration: Long) =
        sendEvent { setSecuritySnapshot(buildSecuritySnapshotEvent(opId, networkGeneration, sync = true)) }

    private suspend fun buildSecuritySnapshotEvent(opId: Long, networkGeneration: Long, sync: Boolean): SecuritySnapshotEvent {
        val b = SecuritySnapshotEvent.newBuilder().setOpId(opId).setNetworkGeneration(networkGeneration).setSelfDeviceId(account.deviceId)
        try {
            val record = if (sync) {
                security.sync().also { r ->
                    runCatching { resumeSecurityOperation() }.onFailure { if (it !is DeviceSecurity.Failure) throw it }
                    try {
                        recoveryObjectsPending = true
                        republishRecoveryObjects(r)
                        recoveryObjectsPending = false
                    } catch (e: DeviceSecurity.Failure) {
                        if (e.code != "lease") throw e
                    }
                }
            } else security.record()
            refreshSecuritySets(record)
            val head = record.head
            b.setIsMember(record.member).setAuthorityDeviceId(head.core.authorityID).setHeadEpoch(head.core.epoch.toLong())
                .setLeaseEndMs(record.leaseExpiration()).setClockSkewed(serverAhead || security.clockSkew)
                .setRecoveryPending(security.recoveryPending).setRecoveryReadyMs(security.recoveryReadyMs)
                .setRecoveryObjectsPending(recoveryObjectsPending)
            for (m in head.core.members) when (m.kind) {
                1u -> b.addTrustedMachineIds(m.id)
                2u -> b.addTrustedClientIds(m.id)
            }
            archiveMachines.filter { trustedMembers[it]?.kind != 1u }.forEach { b.addArchiveMachineIds(it) }
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            report(e)
            b.setUnresolvedHost(unresolvedHost(e)).setError(e.message ?: e.javaClass.simpleName).setClockSkewed(serverAhead || security.clockSkew)
        }
        return b.build()
    }

    private fun refreshSecuritySets(record: DeviceSecurity.Record) {
        val members = record.head.core.members.associateBy { it.id }
        trustedMembers.clear()
        trustedMembers.putAll(members)
        val arch = record.chain.flatMap { it.core.members }.filter { it.kind == 1u }.map { it.id }.toSet()
        archiveMachines.clear()
        archiveMachines.addAll(arch)
        if (scheduleRepository.isInitialized()) {
            members.keys.forEach { schedules.allow(it) }
            arch.filter { it !in members }.forEach { schedules.forget(it) }
        }
    }

    // --- Stream Driver ---

    private suspend fun openStreamForCore(legId: Long, token: String) {
        val url = account.server.replaceFirst(Regex("^http"), "ws") + "/v1/stream"
        val hello = PeerFrame.newBuilder().setHello(StreamHello.newBuilder().setToken(token)).build().toByteArray()
        val queuedBytes = AtomicLong()
        val frames = Channel<ByteArray>(frameQueue, onUndeliveredElement = { queuedBytes.addAndGet(-it.size.toLong()) })
        val closed = AtomicBoolean(false)
        val policyViolation = AtomicBoolean(false)
        val streamError = java.util.concurrent.atomic.AtomicReference<Throwable?>(null)
        lateinit var legJob: Job

        val listener = object : FrameListener() {
            override fun onOpen(stream: FrameStream) {
                stream.send(hello)
            }

            override fun onFrame(stream: FrameStream, bytes: ByteArray) {
                val deliveryBody = peerFrameBody(bytes, 11)
                val resultBody = if (deliveryBody == null) peerFrameBody(bytes, 12) else null
                if (deliveryBody != null || resultBody != null) {
                    if (peerJobs.incrementAndGet() > 64) {
                        peerJobs.decrementAndGet()
                        return
                    }
                    scope.launch {
                        try {
                            val current = streamMutex.withLock { activeLegId == legId && activeSocket === stream }
                            if (!current || !running) return@launch
                            if (deliveryBody != null) direct.receive(deliveryBody, stream)
                            else if (resultBody != null) direct.result(resultBody, stream)
                        } catch (_: Exception) {
                        } finally {
                            peerJobs.decrementAndGet()
                        }
                    }
                    return
                }
                fun dropStream() = cleanup({ stream.cancel() }, { frames.close(java.io.IOException("stream overflow")) })
                if (bytes.size > STREAM_FRAME_BYTES) {
                    dropStream()
                    return
                }
                if (queuedBytes.addAndGet(bytes.size.toLong()) > STREAM_QUEUE_BYTES) {
                    queuedBytes.addAndGet(-bytes.size.toLong())
                    dropStream()
                    return
                }
                if (frames.trySend(bytes).isFailure) {
                    queuedBytes.addAndGet(-bytes.size.toLong())
                    dropStream()
                }
            }

            override fun onClosing(stream: FrameStream, code: Int) {
                if (code == POLICY_VIOLATION) policyViolation.set(true)
                cleanup({ stream.close() }, { frames.close() })
            }

            override fun onEnd(stream: FrameStream, code: Int) {
                if (code == POLICY_VIOLATION) policyViolation.set(true)
                frames.close()
            }

            override fun onFailure(stream: FrameStream, error: Throwable) {
                streamError.compareAndSet(null, error)
                frames.close()
            }
        }

        fun sendFrame(block: StreamFrameEvent.Builder.() -> Unit) =
            sendEvent { setStreamFrame(StreamFrameEvent.newBuilder().setLegId(legId).apply(block)) }

        val socket = deviceLink.router.path.openStream(url, listener)
        legJob = scope.launch {
            var notifyClose = true
            try {
                for (raw in frames) {
                    ensureActive()
                    val frame = parseServerFrame(raw)
                    when (frame.kindCase) {
                        ServerFrame.KindCase.READY -> {
                            serverAhead = clock() - frame.ready.serverTimeMs > DeviceSecurity.MAX_CLOCK_SKEW.toLong()
                            direct.attach(socket)
                            sendEvent { setStreamReady(StreamReadyEvent.newBuilder().setLegId(legId).setServerTimeMs(frame.ready.serverTimeMs)) }
                        }
                        ServerFrame.KindCase.HEARTBEAT -> sendFrame { setHeartbeat(true) }
                        ServerFrame.KindCase.MACHINE_UPDATED -> {
                            val pm = openPlainMachine(frame.machineUpdated.machine)
                            sendFrame { setMachineUpdated(pm) }
                        }
                        ServerFrame.KindCase.MACHINE_REMOVED -> {
                            sendFrame { setMachineRemovedId(frame.machineRemoved.machineId) }
                            syncSecurityForCore(0L, 0L)
                        }
                        ServerFrame.KindCase.SESSION_UPDATED -> {
                            val ps = openPlainSession(frame.sessionUpdated.session)
                            sendFrame { setSessionUpdated(ps) }
                        }
                        ServerFrame.KindCase.EVENTS_APPENDED -> {
                            val ea = frame.eventsAppended
                            val sid = ea.sessionId
                            val machineId = ea.machineId.ifEmpty { sessionMachineId(sid) }
                            val opened = ea.eventsList.map { openPlainEvent(sid, machineId, it, live = true) }
                            sendFrame { setEventsAppended(EventsAppendedFrame.newBuilder().setSessionId(sid).addAllEvents(opened)) }
                        }
                        ServerFrame.KindCase.ACCOUNT_PREFS_UPDATED -> {
                            val obj = frame.accountPrefsUpdated.`object`
                            if (obj.objectId == PROJECT_PREFS) {
                                val pp = openPlainPrefs(obj)
                                sendFrame { setAccountPrefsUpdated(pp) }
                            }
                        }
                        ServerFrame.KindCase.DEVICE_UPDATED -> {
                            val pd = openPlainDevice(frame.deviceUpdated.device)
                            sendFrame { setDeviceUpdated(pd) }
                        }
                        ServerFrame.KindCase.DEVICE_REMOVED -> {
                            sendFrame { setDeviceRemovedId(frame.deviceRemoved.deviceId) }
                            syncSecurityForCore(0L, 0L)
                        }
                        else -> {}
                    }
                }
            } catch (e: CancellationException) {
                notifyClose = false
                throw e
            } catch (e: Exception) {
                streamError.compareAndSet(null, e)
            } finally {
                if (closed.compareAndSet(false, true)) {
                    direct.detach(socket)
                    cleanup({ socket.cancel() }, { frames.cancel() })
                    val wasCurrent = streamMutex.withLock {
                        if (activeLegId == legId) {
                            activeSocket = null
                            activeLegJob = null
                            activeLegId = 0L
                            true
                        } else {
                            false
                        }
                    }
                    if (notifyClose && wasCurrent) {
                        val err = streamError.get()
                        val ce = err as? ConnectException
                        sendEvent {
                            setStreamClosed(
                                StreamClosedEvent.newBuilder()
                                    .setLegId(legId)
                                    .setPolicyViolation(policyViolation.get())
                                    .setErrorCode(ce?.code?.name?.lowercase() ?: "")
                                    .setError(err?.message ?: "")
                                    .setUnresolvedHost(err != null && unresolvedHost(err)),
                            )
                        }
                    }
                }
            }
        }

        val stale = streamMutex.withLock {
            val prevSocket = activeSocket
            val prevJob = activeLegJob
            activeLegId = legId
            activeSocket = socket
            activeLegJob = legJob
            prevSocket to prevJob
        }
        stale.second?.cancel()
        stale.first?.cancel()
    }

    private suspend fun closeStreamLeg(legId: Long) {
        val toClose = streamMutex.withLock {
            if (legId == 0L || activeLegId == legId) {
                val s = activeSocket
                val j = activeLegJob
                activeSocket = null
                activeLegJob = null
                if (activeLegId == legId) activeLegId = 0L
                s to j
            } else {
                null to null
            }
        }
        toClose.first?.let { direct.detach(it) }
        toClose.second?.cancel()
        toClose.first?.cancel()
    }

    private fun peerFrameBody(raw: ByteArray, fieldNumber: Int): ByteArray? =
        runCatching {
            val input = com.google.protobuf.CodedInputStream.newInstance(raw)
            while (!input.isAtEnd) {
                val tag = input.readTag()
                if (tag == 0) break
                if (tag ushr 3 == fieldNumber && (tag and 7) == 2) return input.readByteArray()
                input.skipField(tag)
            }
            null
        }.getOrNull()

    // --- Security Gate: Opening wire & cached objects ---

    private fun sessionMachineId(sessionId: String): String =
        coreHost.syncStateView.value.sessionsList.firstOrNull { it.sessionId == sessionId }?.machineId.orEmpty()

    private suspend fun ensureEpochForEnvelope(env: Envelope) {
        if (env.securityHeader.isEmpty) return
        val epoch = runCatching { DeviceSecurity.parseHeader(env.securityHeader.toByteArray()).epoch }.getOrNull() ?: return
        if (security.stateAt(epoch) != null) return
        val ev = buildSecuritySnapshotEvent(0L, 0L, sync = true)
        coreHost.dispatch(CoreEvent.newBuilder().setSecuritySnapshot(ev).build())
    }

    private suspend fun openPlainMachine(m: Machine, fromCache: Boolean = false): PlainMachine {
        val cacheRef = if (!fromCache) stashCacheObject(PendingCacheObject.MachineObj(m)) else ""
        val b = PlainMachine.newBuilder()
            .setMachineId(m.id)
            .setOnline(m.online)
            .setLastSeenMs(m.lastSeenMs)
        if (cacheRef.isNotEmpty()) b.setCacheRef(cacheRef)
        if (!m.hasMeta() || m.meta.securityHeader.isEmpty) return b.build()
        return try {
            if (!fromCache) ensureEpochForEnvelope(m.meta)
            val (h, meta) = openSecureMessage(
                m.meta,
                Place(PayloadKind.PAYLOAD_KIND_MACHINE_META, machineId = m.id),
                MachineMeta::parseFrom,
                live = !fromCache,
            )
            b.setMeta(meta).setMetaRev(h.seq).build()
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            report(e)
            b.setMetaBroken(true).build()
        }
    }

    private suspend fun openPlainSession(rs: RemoteSession, fromCache: Boolean = false): PlainSession {
        val cacheRef = if (!fromCache) stashCacheObject(PendingCacheObject.SessionObj(rs)) else ""
        val b = PlainSession.newBuilder()
            .setSessionId(rs.sessionId)
            .setMachineId(rs.machineId)
            .setLastSeq(rs.lastSeq)
            .setUpdatedMs(rs.updatedMs)
            .setListedEndedMs(rs.endedMs)
        if (cacheRef.isNotEmpty()) b.setCacheRef(cacheRef)

        val hasKey = rs.hasKey() && (!rs.key.securityHeader.isEmpty || !rs.key.header.isEmpty)
        if (!fromCache && rs.hasKey() && !rs.key.securityHeader.isEmpty) {
            ensureEpochForEnvelope(rs.key)
        }
        if (hasKey) {
            try {
                val seed = openSecureKey(
                    rs.key,
                    Place(PayloadKind.PAYLOAD_KIND_SESSION_KEY, machineId = rs.machineId, sessionId = rs.sessionId),
                )
                if (seed.size != KEY_SIZE) throw OpenException("bad session key")
                sessionKeys[rs.sessionId] = seed
            } catch (_: UnsupportedHistoryException) {
                return b.setRetiredHistory(true).build()
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                report(e)
                return b.setOpenFailed(true).build()
            }
        }
        if (sessionKeys[rs.sessionId] == null) {
            return b.setOpenFailed(true).build()
        }
        if (rs.hasMeta() && (!rs.meta.securityHeader.isEmpty || !rs.meta.header.isEmpty)) {
            try {
                if (!fromCache) ensureEpochForEnvelope(rs.meta)
                val (h, info) = openSecureMessage(
                    rs.meta,
                    Place(PayloadKind.PAYLOAD_KIND_SESSION_META, machineId = rs.machineId, sessionId = rs.sessionId),
                    Session::parseFrom,
                    live = !fromCache,
                )
                b.setMeta(info).setMetaRev(h.seq)
                val checkpoint = info.currentCheckpoint.toByteArray()
                    .takeIf { it.isNotEmpty() }
                    ?.let { LiveSnapshot.parseFrom(it) }
                if (checkpoint != null) {
                    val invalid = checkpoint.session.id != rs.sessionId ||
                        ucmp(checkpoint.session.lastSeq, info.lastSeq) > 0 ||
                        checkpoint.actionsList.any { it.sessionId != rs.sessionId || ucmp(it.seq, info.lastSeq) > 0 }
                    if (invalid) b.setCheckpointInvalid(true) else b.setCheckpoint(checkpoint)
                }
            } catch (_: UnsupportedHistoryException) {
                return b.setRetiredHistory(true).build()
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                report(e)
                return b.setOpenFailed(true).build()
            }
        }
        return b.build()
    }

    private suspend fun openPlainEvent(
        sid: String,
        machineId: String,
        se: StoredEvent,
        live: Boolean,
        fromCache: Boolean = false,
    ): PlainEvent {
        if (!se.hasEnvelope() || (se.envelope.securityHeader.isEmpty && se.envelope.header.isEmpty)) {
            return PlainEvent.newBuilder().setSeq(se.seq).setUnavailable(true).build()
        }
        val cacheRef = if (!fromCache) stashCacheObject(PendingCacheObject.EventObj(se)) else ""
        return try {
            if (!fromCache) ensureEpochForEnvelope(se.envelope)
            val (_, ev) = openSecureMessage(
                se.envelope,
                Place(PayloadKind.PAYLOAD_KIND_EVENT, machineId = machineId, sessionId = sid, seq = se.seq),
                Event::parseFrom,
                live = live,
            )
            if (ev.seq != se.seq || ev.sessionId != sid) {
                throw OpenException(UiMessages.errorsTheEventSPositionWasChanged().messageText())
            }
            PlainEvent.newBuilder()
                .setSeq(se.seq)
                .setEvent(ev)
                .setVerified(true)
                .setApproxBytes(se.envelope.serializedSize.toLong())
                .apply { if (cacheRef.isNotEmpty()) setCacheRef(cacheRef) }
                .build()
        } catch (_: RecoveryHistoryUnavailable) {
            PlainEvent.newBuilder()
                .setSeq(se.seq)
                .setUnavailable(true)
                .apply { if (cacheRef.isNotEmpty()) setCacheRef(cacheRef) }
                .build()
        } catch (e: UnsupportedHistoryException) {
            PlainEvent.newBuilder().setSeq(se.seq).setRetired(true).build()
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            report(e)
            PlainEvent.newBuilder().setSeq(se.seq).setBroken(true).build()
        }
    }

    private suspend fun openPlainDevice(d: Device): PlainDevice {
        val b = PlainDevice.newBuilder()
            .setDeviceId(d.id)
            .setKind(d.kind)
            .setCreatedMs(d.createdMs)
            .setLastSeenMs(d.lastSeenMs)
        if (!d.hasMeta() || d.meta.securityHeader.isEmpty) return b.build()
        return try {
            ensureEpochForEnvelope(d.meta)
            val (h, meta) = if (d.kind == DeviceKind.DEVICE_KIND_MACHINE) {
                val (hdr, machineMeta) = openSecureMessage(
                    d.meta,
                    Place(PayloadKind.PAYLOAD_KIND_MACHINE_META, machineId = d.id),
                    MachineMeta::parseFrom,
                    live = false,
                )
                hdr to DeviceMeta.newBuilder().setName(machineMeta.hostname).build()
            } else {
                openSecureMessage(
                    d.meta,
                    Place(PayloadKind.PAYLOAD_KIND_DEVICE_META, requestId = d.id),
                    DeviceMeta::parseFrom,
                    live = false,
                )
            }
            b.setMeta(meta).setMetaRev(h.seq).build()
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            report(e)
            b.setMetaBroken(true).build()
        }
    }

    private suspend fun openPlainPrefs(o: AccountPrefsObject, fromCache: Boolean = false): PlainAccountPrefs {
        val cacheRef = if (!fromCache) stashCacheObject(PendingCacheObject.PrefsObj(o)) else ""
        val b = PlainAccountPrefs.newBuilder().setRevision(o.revision)
        if (cacheRef.isNotEmpty()) b.setCacheRef(cacheRef)
        if (o.revision == 0L || !o.hasEnvelope() || o.envelope.securityHeader.isEmpty) {
            return b.setPrefs(ProjectPrefs.newBuilder().setSchema(1).build()).build()
        }
        return try {
            if (!fromCache) ensureEpochForEnvelope(o.envelope)
            val (h, prefs) = openSecureMessage(
                o.envelope,
                Place(PayloadKind.PAYLOAD_KIND_ACCOUNT_PREFS, requestId = PROJECT_PREFS),
                ProjectPrefs::parseFrom,
                live = !fromCache,
            )
            b.setRevision(h.seq).setPrefs(prefs).build()
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            report(e)
            b.setBroken(true).build()
        }
    }

    // --- Storage Executor ---

    private suspend fun executeCacheRead(eff: CacheReadEffect) {
        when (eff.kindCase) {
            CacheReadEffect.KindCase.BOOTSTRAP -> {
                val b = CacheBootstrapLoadedEvent.newBuilder().setOpId(eff.bootstrap.opId)
                try {
                    runCatching { security.record() }.getOrNull()?.let { record ->
                        refreshSecuritySets(record)
                        archiveMachines.filter { trustedMembers[it]?.kind != 1u }.forEach { cache?.removeBlob("schedule:$it") }
                    }
                    b.addAllMachines(cache?.machines().orEmpty().map { openPlainMachine(it, fromCache = true) })
                        .addAllSessions(cache?.sessions().orEmpty().map { openPlainSession(it, fromCache = true) })
                    cache?.blob(PROJECT_PREFS)?.let { b.setPrefs(openPlainPrefs(AccountPrefsObject.parseFrom(it), fromCache = true)) }
                    homeScopeStore?.readToken()?.takeIf { it.isNotEmpty() }?.let { b.setHomeScopeToken(it) }
                    Keys.deriveKey(account.localKey, "home-scope").takeIf { it.isNotEmpty() }?.let { b.setScopeHmacKey(ByteString.copyFrom(it)) }
                } catch (e: Exception) {
                    if (e is CancellationException) throw e
                    report(e)
                    b.setError(e.message ?: e.javaClass.simpleName)
                }
                sendEvent { setCacheBootstrapLoaded(b) }
            }
            CacheReadEffect.KindCase.EVENTS -> {
                val req = eff.events
                val sid = req.sessionId
                val b = CacheEventsLoadedEvent.newBuilder().setOpId(req.opId).setSessionId(sid)
                try {
                    val raw = if (req.newest) cache?.newestEvents(sid).orEmpty() else cache?.eventPage(sid, req.afterSeq).orEmpty()
                    val limited = if (req.limit > 0 && raw.size > req.limit) raw.take(req.limit) else raw
                    val machineId = sessionMachineId(sid)
                    var broken = false
                    var retired = false
                    val plain = limited.map { se ->
                        openPlainEvent(sid, machineId, se, live = false, fromCache = true).also {
                            if (it.broken) broken = true
                            if (it.retired) retired = true
                        }
                    }
                    b.addAllEvents(plain).setBroken(broken).setRetired(retired)
                } catch (e: Exception) {
                    if (e is CancellationException) throw e
                    report(e)
                    b.setError(e.message ?: e.javaClass.simpleName)
                }
                sendEvent { setCacheEventsLoaded(b) }
            }
            else -> {}
        }
    }

    private suspend fun executeCacheWrite(eff: CacheWriteEffect) {
        try {
            if (eff.hasWriteMachineRef()) (takeCacheObject(eff.writeMachineRef) as? PendingCacheObject.MachineObj)?.let { cache?.putMachine(it.m) }
            if (eff.hasWriteSessionRef()) (takeCacheObject(eff.writeSessionRef) as? PendingCacheObject.SessionObj)?.let { cache?.putSession(it.s) }
            if (eff.hasKeepSessionOwnerRef()) (takeCacheObject(eff.keepSessionOwnerRef) as? PendingCacheObject.SessionObj)?.let { cache?.keepOwner(it.s) }
            if (eff.hasWriteEvents()) {
                val we = eff.writeEvents
                val stored = we.cacheRefsList.mapNotNull { (takeCacheObject(it) as? PendingCacheObject.EventObj)?.e }
                if (stored.isNotEmpty()) cache?.putEvents(we.sessionId, stored)
            }
            if (eff.hasWritePrefsRef()) (takeCacheObject(eff.writePrefsRef) as? PendingCacheObject.PrefsObj)?.let { cache?.putBlob(PROJECT_PREFS, it.p.toByteArray()) }
            if (eff.hasSetSessionOpen()) cache?.setSessionOpen(eff.setSessionOpen.sessionId, eff.setSessionOpen.open)
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            report(e)
        }
    }

    private suspend fun executeCacheDelete(eff: CacheDeleteEffect) {
        try {
            when (eff.kindCase) {
                CacheDeleteEffect.KindCase.DROP_EVENTS_SESSION_ID ->
                    cache?.dropEvents(eff.dropEventsSessionId)
                CacheDeleteEffect.KindCase.REMOVE_SESSION_ID -> {
                    val sid = eff.removeSessionId
                    sessionKeys.remove(sid)
                    cache?.removeSession(sid)
                    runCatching { onSessionRemoved(sid) }
                }
                CacheDeleteEffect.KindCase.UNLIST_SESSION_ID ->
                    cache?.unlistSession(eff.unlistSessionId)
                CacheDeleteEffect.KindCase.REMOVE_MACHINE_ID -> {
                    val mid = eff.removeMachineId
                    cache?.removeBlob("schedule:$mid")
                    cache?.removeMachine(mid)
                    direct.invalidate()
                }
                else -> {}
            }
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            report(e)
        }
    }

    // --- RPC Executor ---

    private inline fun sendEvent(block: CoreEvent.Builder.() -> Unit) =
        coreHost.dispatch(CoreEvent.newBuilder().apply(block).build())

    private suspend inline fun <B> runRpc(
        make: () -> B,
        onError: B.(String, Boolean) -> Unit,
        crossinline emit: CoreEvent.Builder.(B) -> Unit,
        block: B.() -> Unit,
    ) {
        val b = make()
        try {
            b.block()
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            report(e)
            b.onError(rpcErrorString(e), unresolvedHost(e))
        }
        sendEvent { emit(b) }
    }

    private suspend fun executeRpc(eff: RpcEffect) {
        val opId = eff.opId
        when (eff.kindCase) {
            RpcEffect.KindCase.FETCH_LISTS -> runRpc(
                make = { ListsFetchedEvent.newBuilder().setOpId(opId) },
                onError = { err, host -> setUnresolvedHost(host).setError(err) },
                emit = { setListsFetched(it) },
            ) {
                val headers = Api.headers(currentToken())
                coroutineScope {
                    val reqSessions = ListRemoteSessionsRequest.newBuilder().apply {
                        if (eff.fetchLists.limit > 0) setLimit(eff.fetchLists.limit)
                    }.build()
                    val ms = async { api.client.listMachines(ListMachinesRequest.getDefaultInstance(), headers).value() }
                    val ss = async { api.client.listRemoteSessions(reqSessions, headers).value() }
                    val ds = async { api.client.listDevices(ListDevicesRequest.getDefaultInstance(), headers).value() }
                    val machinesRes = ms.await()
                    val sessionsRes = ss.await()
                    val devicesRes = ds.await()
                    addAllMachines(machinesRes.machinesList.map { openPlainMachine(it) })
                    addAllSessions(sessionsRes.sessionsList.map { openPlainSession(it) })
                    if (sessionsRes.hasNext()) setNext(sessionsRes.next)
                    addAllDevices(devicesRes.devicesList.map { openPlainDevice(it) })
                }
            }
            RpcEffect.KindCase.FETCH_SESSIONS_PAGE -> runRpc(
                make = { SessionsPageFetchedEvent.newBuilder().setOpId(opId) },
                onError = { err, _ -> setError(err) },
                emit = { setSessionsPageFetched(it) },
            ) {
                val req = ListRemoteSessionsRequest.newBuilder().apply {
                    if (eff.fetchSessionsPage.hasAfter()) setAfter(eff.fetchSessionsPage.after)
                    if (eff.fetchSessionsPage.limit > 0) setLimit(eff.fetchSessionsPage.limit)
                }.build()
                val res = api.client.listRemoteSessions(req, Api.headers(currentToken())).value()
                addAllSessions(res.sessionsList.map { openPlainSession(it) })
                if (res.hasNext()) setNext(res.next)
            }
            RpcEffect.KindCase.FETCH_SESSION_BY_ID -> {
                val sid = eff.fetchSessionById.sessionId
                val b = SessionByIdFetchedEvent.newBuilder().setOpId(opId)
                var failure: Throwable? = null
                var foundSession = false
                try {
                    val req = ListRemoteSessionsRequest.newBuilder().addSessionIds(sid).setLimit(1).build()
                    val found = api.client.listRemoteSessions(req, Api.headers(currentToken())).value().sessionsList.firstOrNull { it.sessionId == sid }
                    if (found != null) {
                        b.setSession(openPlainSession(found))
                        foundSession = true
                    }
                } catch (e: Exception) {
                    if (e is CancellationException) throw e
                    report(e)
                    failure = e
                    b.setError(rpcErrorString(e))
                }
                coreHost.dispatchAndWait(CoreEvent.newBuilder().setSessionByIdFetched(b).build())
                pendingSessionLookups.remove(sid)?.forEach { w ->
                    if (failure != null) w.completeExceptionally(failure) else w.complete(foundSession)
                }
            }
            RpcEffect.KindCase.GET_EVENTS -> runRpc(
                make = { EventsPageFetchedEvent.newBuilder().setOpId(opId).setSessionId(eff.getEvents.sessionId) },
                onError = { err, _ -> setError(err) },
                emit = { setEventsPageFetched(it) },
            ) {
                val r = eff.getEvents
                val req = GetEventsRequest.newBuilder().setSessionId(r.sessionId).setAfterSeq(r.afterSeq).setLimit(r.limit).build()
                val res = api.client.getEvents(req, Api.headers(currentToken())).value()
                val machineId = sessionMachineId(r.sessionId)
                setMore(res.more).addAllEvents(res.eventsList.map { openPlainEvent(r.sessionId, machineId, it, live = false) })
            }
            RpcEffect.KindCase.GET_ACCOUNT_PREFS -> runRpc(
                make = { AccountPrefsFetchedEvent.newBuilder().setOpId(opId) },
                onError = { err, _ -> setError(err) },
                emit = { setAccountPrefsFetched(it) },
            ) {
                val objectId = eff.getAccountPrefs.objectId.ifEmpty { PROJECT_PREFS }
                val obj = api.client.getAccountPrefs(GetAccountPrefsRequest.newBuilder().setObjectId(objectId).build(), Api.headers(currentToken())).value().`object`
                setPrefs(openPlainPrefs(obj))
            }
            RpcEffect.KindCase.PUT_ACCOUNT_PREFS -> {
                val putRpc = eff.putAccountPrefs
                val objectId = putRpc.objectId.ifEmpty { PROJECT_PREFS }
                val b = AccountPrefsPutResultEvent.newBuilder().setOpId(opId)
                try {
                    val env = withContext(cpuDispatcher) {
                        security.seal(DeviceSecurity.Purpose.PREFS, request = objectId, seq = (putRpc.expectedRevision + 1).toULong(), plain = putRpc.nextPrefs.toByteArray())
                    }
                    val res = api.client.putAccountPrefs(
                        PutAccountPrefsRequest.newBuilder().setObjectId(objectId).setExpectedRevision(putRpc.expectedRevision).setEnvelope(env).build(),
                        Api.headers(currentToken()),
                    ).value()
                    b.setApplied(res.applied)
                    if (res.hasObject()) b.setCurrent(openPlainPrefs(res.`object`))
                } catch (e: DeviceSecurity.Failure) {
                    if (e.code == "conflict") b.setSealConflict(true)
                    b.setError(e.message ?: e.code)
                } catch (e: Exception) {
                    if (e is CancellationException) throw e
                    report(e)
                    b.setError(rpcErrorString(e))
                }
                sendEvent { setAccountPrefsPutResult(b) }
            }
            RpcEffect.KindCase.FETCH_DEVICES -> runRpc(
                make = { DevicesFetchedEvent.newBuilder().setOpId(opId) },
                onError = { err, _ -> setError(err) },
                emit = { setDevicesFetched(it) },
            ) {
                val ds = api.client.listDevices(ListDevicesRequest.getDefaultInstance(), Api.headers(currentToken())).value()
                addAllDevices(ds.devicesList.map { openPlainDevice(it) })
            }
            else -> {}
        }
    }

    private fun rpcErrorString(e: Exception): String =
        if (e is ConnectException) "${e.code.name.lowercase()}: ${e.message ?: ""}".trimEnd(':', ' ')
        else e.message ?: e.javaClass.simpleName

    // --- Stateless single-attempt command executor ---

    private suspend fun executeDispatchCommand(eff: io.github.lewin671.remoteagent.core.proto.DispatchCommandEffect) {
        val outcome = try {
            dispatchSingleCommand(eff)
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            CommandOutcomeEvent.newBuilder()
                .setRequestId(eff.requestId)
                .setAttemptId(eff.attemptId)
                .setSource(OutcomeSource.OUTCOME_SOURCE_LOCAL)
                .setNotForwarded(true)
                .setTransportCode("failed_precondition")
                .setTransportDetail(e.message ?: e.javaClass.simpleName)
                .build()
        }
        coreHost.dispatch(CoreEvent.newBuilder().setCommandOutcome(outcome).build())
    }

    private class UnverifiedReplyException(val attemptBound: Boolean) : Exception()

    private class SealedDeliveryResult(
        val msg: CommandResult,
        val envelope: Envelope,
        val attemptBound: Boolean,
        val hashesMatch: Boolean,
        val outerMatchesInner: Boolean,
        val outerOutcome: String,
        val outerReason: String,
    )

    private suspend fun sendAndOpenCommand(
        machineId: String,
        sessionId: String,
        requestId: String,
        parsedCmd: Command?,
        authorized: Envelope,
        binding: Pair<ByteArray, ByteArray>,
        prior: Boolean,
        delivery: PathAttempt,
        bulk: Boolean,
        tracking: DirectEngine.RouteTracker?,
    ): SealedDeliveryResult {
        val startedMs = monotonic()
        val startedNs = System.nanoTime()
        delivery.prepare(prior)
        val res = delivery.send(machineId, authorized)
        val attemptBound = res.attemptId == delivery.attemptId
        val (openedHeader, msg) = try {
            openSecureMessage(
                res.result,
                Place(PayloadKind.PAYLOAD_KIND_COMMAND_RESULT, machineId = machineId, sessionId = sessionId, requestId = requestId),
                CommandResult::parseFrom,
                bulk = bulk,
            )
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
            throw UnverifiedReplyException(attemptBound)
        }
        val hashesMatch =
            openedHeader.kind == PayloadKind.PAYLOAD_KIND_COMMAND_RESULT &&
                msg.requestKeyHash.toByteArray().contentEquals(binding.first) &&
                msg.payloadHash.toByteArray().contentEquals(binding.second) &&
                (msg.outcome == "conflict" || msg.conflictKind.isEmpty())
        val outerMatchesInner = delivery.direct || (res.outcome == msg.outcome && res.reason == msg.reason)
        val outerOutcome = if (delivery.direct) msg.outcome else res.outcome
        val outerReason = if (delivery.direct) msg.reason else res.reason
        if (tracking != null && hashesMatch && attemptBound && outerMatchesInner && msg.errorCode.isEmpty() &&
            verifiedOutcome(msg, binding.first, binding.second, outerOutcome, outerReason) == Outcome.EXECUTED
        ) {
            val bytes = when {
                parsedCmd?.hasPutImageChunk() == true -> parsedCmd.putImageChunk.data.size()
                msg.hasGetSharedFileChunk() -> msg.getSharedFileChunk.data.size()
                else -> 0
            }
            val monoMs = maxOf((monotonic() - startedMs).toDouble(), (System.nanoTime() - startedNs) / 1_000_000.0, 0.01)
            tracking.observe(delivery.direct, bytes, monoMs)
        }
        return SealedDeliveryResult(msg, res.result, attemptBound, hashesMatch, outerMatchesInner, outerOutcome, outerReason)
    }

    private suspend fun dispatchSingleCommand(
        eff: io.github.lewin671.remoteagent.core.proto.DispatchCommandEffect,
    ): CommandOutcomeEvent {
        fun localFailure(code: String, detail: String = ""): CommandOutcomeEvent =
            CommandOutcomeEvent.newBuilder()
                .setRequestId(eff.requestId)
                .setAttemptId(eff.attemptId)
                .setSource(OutcomeSource.OUTCOME_SOURCE_LOCAL)
                .setNotForwarded(true)
                .setTransportCode(code)
                .setTransportDetail(detail)
                .build()

        val cmdBytes = eff.commandBytes.toByteArray()
        val parsedCmd = runCatching { Command.parseFrom(cmdBytes) }.getOrNull()
        val routeHint = when (eff.routeHint) {
            CommandRouteHint.COMMAND_ROUTE_HINT_READ -> RouteHint.Read
            CommandRouteHint.COMMAND_ROUTE_HINT_BULK ->
                RouteHint.Bulk(eff.bulkFileKey, if (eff.bulkUpload) UploadKind.FILE else null, eff.bulkBytes.toInt())
            else -> null
        }
        val bulk = eff.bulkUpload || routeHint is RouteHint.Bulk
        val cpu = if (bulk) bulkCpuDispatcher else cpuDispatcher

        if (serverAhead || security.clockSkew) return localFailure("failed_precondition", "clock skew")
        val record = security.record()
        if (!withContext(cpu) { Authority.leaseValid(record, clock()) }) return localFailure("failed_precondition", "lease expired")

        val authorized = withContext(cpu) {
            security.seal(DeviceSecurity.Purpose.COMMAND, eff.machineId, eff.sessionId, eff.requestId, deadline = eff.deadlineMs.toULong(), plain = cmdBytes)
        }
        val binding = security.commandIdentity(authorized)
        var handedOff = false
        val handoff: () -> Unit = { handedOff = true }
        val tracking = routeHint?.let { direct.track(eff.machineId, it) }
        val directAttempt =
            if (!eff.forceRelay && routeHint != null) direct.attempt(eff.machineId, routeHint, handoff, attemptId = eff.attemptId) else null
        val delivery: PathAttempt = directAttempt ?: deviceLink.router.path.attempt(eff.attemptId, handoff)
        fun baseOutcome(source: OutcomeSource, directRoute: Boolean = delivery.direct) =
            CommandOutcomeEvent.newBuilder()
                .setRequestId(eff.requestId)
                .setAttemptId(eff.attemptId)
                .setSource(source)
                .setRouteWasDirect(directRoute)

        return try {
            val res = sendAndOpenCommand(
                eff.machineId, eff.sessionId, eff.requestId, parsedCmd, authorized, binding,
                eff.retryOfUnknown, delivery, bulk, tracking,
            )
            baseOutcome(OutcomeSource.OUTCOME_SOURCE_DAEMON)
                .setAttemptBound(res.attemptBound)
                .setSigned(true)
                .setHashesMatch(res.hashesMatch)
                .setOuterMatchesInner(res.outerMatchesInner)
                .setOuterOutcome(res.outerOutcome)
                .setOuterReason(res.outerReason)
                .setResult(res.msg)
                .build()
        } catch (e: UnverifiedReplyException) {
            baseOutcome(OutcomeSource.OUTCOME_SOURCE_DAEMON).setAttemptBound(e.attemptBound).setSigned(false).setHashesMatch(false).build()
        } catch (_: PathChanged) {
            baseOutcome(if (handedOff) OutcomeSource.OUTCOME_SOURCE_TRANSPORT else OutcomeSource.OUTCOME_SOURCE_LOCAL)
                .setNotForwarded(!handedOff).setTransportCode("path_changed").build()
        } catch (ce: ConnectException) {
            val refusal = delivery.refusal(ce)
            if (refusal != null) {
                baseOutcome(OutcomeSource.OUTCOME_SOURCE_SERVER).setNotForwarded(true).setAttemptBound(true)
                    .setOuterReason(refusal.reason).setTransportCode(mapConnectCode(ce.code)).setTransportDetail(ce.message ?: "").build()
            } else {
                baseOutcome(OutcomeSource.OUTCOME_SOURCE_TRANSPORT).setNotForwarded(!handedOff)
                    .setTransportCode(if (unresolvedHost(ce)) "unresolved_host" else mapConnectCode(ce.code))
                    .setTransportDetail(ce.message ?: "").build()
            }
        } catch (e: IOException) {
            baseOutcome(OutcomeSource.OUTCOME_SOURCE_TRANSPORT).setNotForwarded(!handedOff)
                .setTransportCode(if (unresolvedHost(e)) "unresolved_host" else "network")
                .setTransportDetail(e.message ?: e.javaClass.simpleName).build()
        }
    }

    private fun mapConnectCode(code: Code): String = when (code) {
        Code.UNAUTHENTICATED -> "unauthenticated"
        Code.FAILED_PRECONDITION -> "failed_precondition"
        Code.UNAVAILABLE, Code.DEADLINE_EXCEEDED -> "unavailable"
        else -> code.name.lowercase()
    }

    // --- Direct probe ---

    private suspend fun probeRead(machineId: String, path: RouteName) {
        val wantDirect = path == RouteName.DIRECT
        val tracking = direct.track(machineId, RouteHint.Read)
        try {
            val probeCmd = prepare(machineId, "", Command.newBuilder().setGetMachineStatus(io.github.lewin671.remoteagent.proto.GetMachineStatusRequest.getDefaultInstance()).build())
            val authorized = authorize(probeCmd, retryOfUnknown = false)
            val binding = security.commandIdentity(authorized)
            val delivery = if (wantDirect) direct.attempt(machineId, RouteHint.Read, {}, probe = true) ?: return else deviceLink.router.path.attempt {}
            val res = sendAndOpenCommand(machineId, "", probeCmd.requestId, probeCmd.command, authorized, binding, false, delivery, false, tracking = tracking)
            verifiedOutcome(res.msg, binding.first, binding.second, res.outerOutcome, res.outerReason)
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
            if (wantDirect) tracking.failed()
        }
    }

    // --- Lifecycle ---

    fun start() {
        running = true
        if (!authorityJob.isActive) authorityJob = SupervisorJob(scope.coroutineContext[Job])
        val token = homeScopeStore?.readToken().orEmpty()
        val hmacKey = Keys.deriveKey(account.localKey, "home-scope")
        coreHost.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_START, token, hmacKey)
        renewLeaseIfNeeded()
    }

    fun stop() {
        running = false
        authorityJob.cancel()
        lastDirectReady.clear()
        direct.invalidate()
        coreHost.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_STOP)
        scope.launch { closeStreamLeg(0L) }
    }

    fun foregroundChanged(foreground: Boolean) {
        coreHost.syncLifecycle(
            if (foreground) SyncLifecycleKind.SYNC_LIFECYCLE_KIND_FOREGROUND
            else SyncLifecycleKind.SYNC_LIFECYCLE_KIND_BACKGROUND,
        )
    }

    fun networkChanged(signature: String, available: Boolean) {
        val realChange = lastNetworkSignature != null && (lastNetworkSignature != signature || networkAvailable != available)
        lastNetworkSignature = signature
        networkAvailable = available
        _networkAvailableFlow.value = available
        if (realChange) {
            networkGeneration++
            inFlightToken?.completeExceptionally(NetworkResetException())
            inFlightToken = null
            inFlightLoginJob?.cancel()
            inFlightLoginJob = null
        }
        security.networkChanged()
        direct.invalidate()
        coreHost.networkChanged(available, signature)
    }

    fun setNetwork(available: Boolean) = networkChanged(if (available) "up" else "none", available)

    fun reconnectNow() {
        coreHost.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_RECONNECT_NOW)
    }

    fun retryLogin() {
        coreHost.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_RETRY_LOGIN)
    }

    fun retryEvents(sid: String) = coreHost.retrySessionEvents(sid)

    suspend fun fetchSession(sid: String): Boolean {
        if (state.value.sessions[sid] != null) return true
        val deferred = CompletableDeferred<Boolean>()
        pendingSessionLookups.computeIfAbsent(sid) { java.util.concurrent.CopyOnWriteArrayList() }.add(deferred)
        coreHost.fetchSession(sid)
        return deferred.await()
    }

    fun loadMoreSessions(): Boolean {
        coreHost.loadMoreHomeEnded()
        return state.value.moreSessions
    }

    fun loadAllSessions() {
        coreHost.loadMoreHomeEnded(step = 500)
    }

    fun close() {
        running = false
        cleanup(
            { direct.close() },
            { deviceLink.closeStreams() },
            { security.close() },
            { cache?.close() },
            {
                scope.launch {
                    try {
                        closeStreamLeg(0L)
                    } finally {
                        try {
                            coreHost.close()
                        } finally {
                            scope.cancel()
                        }
                    }
                }
            },
        )
    }

    // --- Transitional Step 3/4 UI & Test Helpers ---

    fun showForTest(
        state: RemoteState,
        events: Map<String, List<Event>> = emptyMap(),
        modelLists: Map<Pair<String, Agent>, ListModelsResponse> = emptyMap(),
        live: Map<String, LiveSnapshot> = emptyMap(),
    ) {
        check(!running) { "showForTest on a started Remote" }
        relayObservation = state
        testStateOverride = state
        _testStateFlow.value = state
        _state.value = state
        for ((key, response) in modelLists) {
            models["${key.first}/${key.second.number}"] = CompletableDeferred(response)
        }
        val allSids = events.keys + live.keys
        for (sid in allSids) {
            val list = events[sid].orEmpty()
            testEventFlows.getOrPut(sid) { MutableStateFlow(SessionEvents()) }.value =
                SessionEvents(list, loaded = true, live = live[sid])
        }
        seedCoreForTest(coreHost, account.deviceId, state, modelLists)
    }

    fun events(sid: String): StateFlow<SessionEvents> {
        if (testStateOverride != null) {
            return testEventFlows.getOrPut(sid) { MutableStateFlow(SessionEvents()) }.asStateFlow()
        }
        return sessionEventFlows.getOrPut(sid) {
            coreHost.watchSession(sid)
                .map { it.toSessionEvents() }
                .stateIn(scope, SharingStarted.WhileSubscribed(stopTimeoutMillis = 0), SessionEvents())
        }
    }

    // --- Security Management & Recovery (preserved for Security screens & DeviceSecurity tests) ---

    suspend fun syncSecurity(budgetMs: Long = Long.MAX_VALUE) {
        try {
            val r = withContext(dispatcher) {
                security.sync(budgetMs).also { synced ->
                    resumeSecurityOperation()
                    try {
                        recoveryObjectsPending = true
                        republishRecoveryObjects(synced)
                        recoveryObjectsPending = false
                    } catch (e: DeviceSecurity.Failure) {
                        if (e.code != "lease") throw e
                    }
                }
            }
            refreshSecuritySets(r)
            archiveMachines.filter { trustedMembers[it]?.kind != 1u }.forEach { cache?.removeBlob("schedule:$it") }
        } catch (e: DeviceSecurity.Failure) {
            if (e.code != "clock") throw e
        }
        coreHost.dispatchAndWait(CoreEvent.newBuilder().setSecuritySnapshot(buildSecuritySnapshotEvent(0L, 0L, sync = false)).build())
    }

    suspend fun refreshDevices() {
        val ds = api.client.listDevices(ListDevicesRequest.getDefaultInstance(), Api.headers(currentToken())).value()
        val plain = ds.devicesList.map { openPlainDevice(it) }
        coreHost.dispatchAndWait(CoreEvent.newBuilder().setDevicesFetched(DevicesFetchedEvent.newBuilder().setOpId(0L).addAllDevices(plain)).build())
        updateProjectedStateNow()
    }

    private suspend fun republishRecoveryObjects(r: DeviceSecurity.Record) {
        val boundary = r.proofs.keys.maxOrNull()?.let { r.stateAt(it)?.core?.contentEpoch } ?: return
        val headers = Api.headers(currentToken())
        suspend fun <T : com.google.protobuf.MessageLite> retryReseal(
            purpose: DeviceSecurity.Purpose,
            request: String,
            seq: Long,
            env: Envelope,
            parse: (ByteArray) -> T,
            upload: suspend (Envelope) -> Boolean,
        ): Boolean {
            val h = DeviceSecurity.parseHeader(env.securityHeader.toByteArray())
            if (h.contentEpoch >= boundary) return true
            if (
                h.purpose != purpose ||
                h.seq != seq.toULong() ||
                (purpose == DeviceSecurity.Purpose.PREFS && h.request != request) ||
                (purpose == DeviceSecurity.Purpose.DEVICE_META && h.sender != account.deviceId)
            ) {
                throw DeviceSecurity.Failure("untrusted")
            }
            val snapshot = try {
                security.openMessage(env, true, parse = parse).second
            } catch (e: DeviceSecurity.Failure) {
                if (e.code == "unavailable") return true else throw e
            }
            return upload(security.seal(purpose, request = request, seq = (seq + 1).toULong(), plain = snapshot.toByteArray()))
        }
        retryRecoveryPublication {
            val obj = api.client.getAccountPrefs(GetAccountPrefsRequest.newBuilder().setObjectId(PROJECT_PREFS).build(), headers).value().`object`
            if (!obj.hasEnvelope() || obj.revision == 0L) true
            else retryReseal(DeviceSecurity.Purpose.PREFS, PROJECT_PREFS, obj.revision, obj.envelope, ProjectPrefs::parseFrom) { env ->
                api.client.putAccountPrefs(PutAccountPrefsRequest.newBuilder().setObjectId(PROJECT_PREFS).setExpectedRevision(obj.revision).setEnvelope(env).build(), headers).value().applied
            }
        }
        retryRecoveryPublication {
            val own = api.client.listDevices(ListDevicesRequest.getDefaultInstance(), headers).value().devicesList.firstOrNull { it.id == account.deviceId }
            if (own == null || !own.hasMeta()) true
            else retryReseal(DeviceSecurity.Purpose.DEVICE_META, account.deviceId, own.metaRevision, own.meta, DeviceMeta::parseFrom) { env ->
                api.client.putDeviceMeta(PutDeviceMetaRequest.newBuilder().setDeviceId(account.deviceId).setExpectedRevision(own.metaRevision).setMeta(env).build(), headers).value().applied
            }
        }
    }

    private val securityOperationLock = Mutex()

    suspend fun resumeSecurityOperation() = securityOperationLock.withLock {
        resumeSecurityOperationLocked()
    }

    private suspend fun resumeSecurityOperationLocked() {
        val storage = securityStorage ?: throw DeviceSecurity.Failure("storage")
        var op = storage.readSecurityOperation() ?: return
        if (op.kind == "lease") {
            val r = security.sync()
            val hash = DeviceSecurity.stateHash(r.head)
            if (!hash.contentEquals(op.parent)) {
                if (r.chain.none { DeviceSecurity.stateHash(it).contentEquals(op.parent) }) {
                    throw DeviceSecurity.Failure("conflict")
                }
                storage.clearSecurityOperation()
                return
            }
            var req = PutSecurityStateRequest.parseFrom(op.signed)
            var cert = HeadLease.parse(req.lease.toByteArray())
            cert.verify(r.head)
            if (maxOf(clock().toULong(), r.floor) >= cert.notAfter) {
                cert = authorityForeground().let {
                    HeadLease.sign(r.head, SigningKey(account.deviceSeed), security.signingTime(clock()))
                }
                req = PutSecurityStateRequest.newBuilder().setLease(ByteString.copyFrom(cert.bytes())).build()
                storage.writeSecurityOperation(SecurityOperation("lease", hash, req.toByteArray()))
            }
            api.client.putSecurityState(req, Api.headers(currentToken())).value()
            security.install(emptyList(), lease = cert)
            storage.clearSecurityOperation()
            val ev = buildSecuritySnapshotEvent(0L, 0L, sync = false)
            coreHost.dispatch(CoreEvent.newBuilder().setSecuritySnapshot(ev).build())
            return
        }
        if (op.kind != "state") {
            reconcileCompletedAdoption(account, security.record(), storage)
            return
        }
        val request = PutSecurityStateRequest.parseFrom(op.signed)
        val states = request.statesList.map { DeviceSecurity.parseState(it.state.toByteArray()) }
        val target = states.last()
        if (op.phase == SecurityOperation.Phase.DELIVERED) {
            val installed = security.record().stateAt(target.core.epoch) ?: throw DeviceSecurity.Failure("conflict")
            if (!DeviceSecurity.stateHash(installed).contentEquals(DeviceSecurity.stateHash(target))) {
                throw DeviceSecurity.Failure("conflict")
            }
            storage.clearSecurityOperation()
            return
        }
        fun saveRetryCertificate(certificate: HeadLease?) {
            val saved = request.toBuilder().clearLease().apply {
                certificate?.let { setLease(ByteString.copyFrom(it.bytes())) }
            }.build()
            if (saved.lease == request.lease) return
            val updated = SecurityOperation(op.kind, op.parent, saved.toByteArray(), op.local, op.delivery, op.phase, op.codeSaved)
            storage.writeSecurityOperation(updated)
            op = updated
        }
        if (target.core.authorityID != account.deviceId && op.phase == SecurityOperation.Phase.PUBLISHED) {
            storage.clearSecurityOperation()
            return
        }
        if (target.core.authorityID == account.deviceId) security.sync()
        val verified = security.record()
        val installed = verified.stateAt(target.core.epoch)
        if (installed != null) {
            if (!DeviceSecurity.stateHash(installed).contentEquals(DeviceSecurity.stateHash(target))) {
                throw DeviceSecurity.Failure("conflict")
            }
            if (
                verified.head.core.epoch == target.core.epoch &&
                target.core.authorityID == account.deviceId &&
                runCatching {
                    verified.lease?.valid(target, maxOf(clock().toULong(), verified.floor))
                        ?: throw DeviceSecurity.Failure("lease")
                }.isFailure
            ) {
                val certificate = retryCertificate(request, target, verified.floor) ?: throw DeviceSecurity.Failure("lease")
                val renewal = PutSecurityStateRequest.newBuilder().setLease(ByteString.copyFrom(certificate.bytes())).build()
                saveRetryCertificate(certificate)
                api.client.putSecurityState(renewal, Api.headers(currentToken())).value()
                security.install(emptyList(), lease = certificate)
            }
        } else {
            if (!DeviceSecurity.stateHash(verified.head).contentEquals(op.parent)) {
                throw DeviceSecurity.Failure("conflict")
            }
            val certificate = retryCertificate(request, target, verified.floor)
            val retry = request.toBuilder().clearLease().apply {
                certificate?.let { setLease(ByteString.copyFrom(it.bytes())) }
            }.build()
            saveRetryCertificate(certificate)
            api.client.putSecurityState(retry, Api.headers(currentToken())).value()
            security.install(
                states,
                request.statesList.map { it.recovery.toByteArray().takeIf { raw -> raw.isNotEmpty() } },
                certificate,
            )
        }
        storage.writeSecurityOperation(op.advanced(SecurityOperation.Phase.PUBLISHED))
        if (op.delivery.isNotEmpty()) {
            val delivery = ApprovePairingRequest.parseFrom(op.delivery)
            api.pairing.approvePairing(delivery, Api.headers(currentToken())).value()
            val status = api.pairing.getPairing(
                GetPairingRequest.newBuilder().setPairingId(delivery.pairingId).build(),
                Api.headers(currentToken()),
            ).value()
            if (status.state != PairingState.PAIRING_STATE_DELIVERED) return
        }
        storage.writeSecurityOperation(op.advanced(SecurityOperation.Phase.DELIVERED))
        storage.clearSecurityOperation()
        val ev = buildSecuritySnapshotEvent(0L, 0L, sync = false)
        coreHost.dispatch(CoreEvent.newBuilder().setSecuritySnapshot(ev).build())
    }

    private suspend fun retryCertificate(
        request: PutSecurityStateRequest,
        target: DeviceSecurity.State,
        floor: ULong,
    ): HeadLease? {
        val now = maxOf(clock().toULong(), floor)
        val original = request.lease.toByteArray().takeIf { it.isNotEmpty() }?.let { HeadLease.parse(it) }
        original?.verify(target)
        if (original != null && now < original.notAfter) {
            original.valid(target, now)
            return original
        }
        return if (target.core.authorityID == account.deviceId) {
            authorityForeground().let {
                HeadLease.sign(target, SigningKey(account.deviceSeed), maxOf(now, target.core.issuedAt).toLong())
            }
        } else {
            null
        }
    }

    fun pendingPairingRequest(pairingId: String): ApprovePairingRequest? {
        val op = securityStorage?.readSecurityOperation() ?: return null
        if (op.kind != "state" || op.delivery.isEmpty()) return null
        return ApprovePairingRequest.parseFrom(op.delivery).takeIf { it.pairingId == pairingId }
    }

    suspend fun finishPairingOperation(pairingId: String) = securityOperationLock.withLock {
        val storage = securityStorage ?: throw DeviceSecurity.Failure("storage")
        val op = storage.readSecurityOperation() ?: return@withLock
        if (op.kind == "state" && op.delivery.isNotEmpty() && ApprovePairingRequest.parseFrom(op.delivery).pairingId == pairingId) {
            storage.writeSecurityOperation(op.advanced(SecurityOperation.Phase.DELIVERED))
            storage.clearSecurityOperation()
        }
    }

    private suspend fun cancelPairDeliveryForRemoval(deviceId: String): Boolean = securityOperationLock.withLock {
        val storage = securityStorage ?: return@withLock false
        val op = storage.readSecurityOperation() ?: return@withLock false
        if (op.kind != "state" || op.delivery.isEmpty()) return@withLock false
        val delivery = ApprovePairingRequest.parseFrom(op.delivery)
        val candidate = PairApproval.parseFrom(delivery.approval).candidateDevicePublicKey.toByteArray()
        if (deviceId(candidate) != deviceId) return@withLock false
        val record = security.sync()
        val target = DeviceSecurity.parseState(
            PutSecurityStateRequest.parseFrom(op.signed).statesList.last().state.toByteArray(),
        )
        val installed = record.stateAt(target.core.epoch) ?: return@withLock false
        if (!DeviceSecurity.stateHash(installed).contentEquals(DeviceSecurity.stateHash(target))) {
            throw DeviceSecurity.Failure("conflict")
        }
        storage.clearSecurityOperation()
        true
    }

    suspend fun publishSecurityStates(
        states: List<DeviceSecurity.State>,
        proofs: List<ByteArray?> = emptyList(),
        delivery: ByteArray = byteArrayOf(),
        certificate: HeadLease? = null,
    ) {
        if (serverAhead || security.clockSkew) throw DeviceSecurity.Failure("clock")
        require(states.isNotEmpty())
        val storage = securityStorage ?: throw DeviceSecurity.Failure("storage")
        val req = PutSecurityStateRequest.newBuilder()
        states.forEachIndexed { i, state ->
            req.addStates(
                SecurityState.newBuilder()
                    .setState(ByteString.copyFrom(DeviceSecurity.stateBytes(state)))
                    .apply { proofs.getOrNull(i)?.let { setRecovery(ByteString.copyFrom(it)) } },
            )
        }
        val lease = certificate ?: if (states.last().core.authorityID == account.deviceId) {
            authorityForeground().let {
                HeadLease.sign(states.last(), SigningKey(account.deviceSeed), security.signingTime(clock()))
            }
        } else {
            null
        }
        lease?.let { req.setLease(ByteString.copyFrom(it.bytes())) }
        val pending = storage.readSecurityOperation()
        if (pending != null) {
            if (pending.kind != "state" || !pending.signed.contentEquals(req.build().toByteArray())) {
                throw DeviceSecurity.Failure("pending")
            }
        } else {
            storage.writeSecurityOperation(
                SecurityOperation("state", states.first().core.parent, req.build().toByteArray(), delivery = delivery),
            )
        }
        resumeSecurityOperation()
    }

    internal suspend fun authorityForeground() {
        kotlinx.coroutines.currentCoroutineContext().ensureActive()
        authorityJob.ensureActive()
    }

    fun renewLeaseIfNeeded() = renewLease(force = false)
    fun renewLeaseNow() = renewLease(force = true)

    private fun renewLease(force: Boolean) {
        scope.launch {
            try {
                withContext(authorityJob) {
                    resumeSecurityOperation()
                    val head = security.head()
                    if (head.core.authorityID != account.deviceId) return@withContext
                    if (
                        !force &&
                        !Authority.needsRenewal(
                            security.record().lease?.takeIf { it.head.contentEquals(DeviceSecurity.stateHash(head)) },
                            clock(),
                        )
                    ) {
                        return@withContext
                    }
                    val lease = authorityForeground().let {
                        HeadLease.sign(head, SigningKey(account.deviceSeed), security.signingTime(clock()))
                    }
                    val request = PutSecurityStateRequest.newBuilder().setLease(ByteString.copyFrom(lease.bytes())).build()
                    val storage = securityStorage ?: throw DeviceSecurity.Failure("storage")
                    storage.writeSecurityOperation(
                        SecurityOperation("lease", DeviceSecurity.stateHash(head), request.toByteArray()),
                    )
                    resumeSecurityOperation()
                }
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                report(e)
            }
        }
    }

    fun prepareHandoffCode(): Pair<String, ByteArray> {
        val (seed, pub) = Authority.newRecovery()
        return RecoveryCode.encode(seed, security.identity().first.genesis) to pub
    }

    suspend fun offerHandoff(recoveryEd: ByteArray) {
        require(recoveryEd.size == 32)
        changeDeviceMeta(account.deviceId) { it.setHandoffRecoveryEd(ByteString.copyFrom(recoveryEd)) }
    }

    suspend fun handoffAuthority(incoming: String) {
        resumeSecurityOperation()
        syncSecurity()
        val head = security.head()
        if (head.core.authorityID != account.deviceId) throw DeviceSecurity.Failure("untrusted")
        val offer = state.value.devices[incoming]?.meta?.handoffRecoveryEd?.toByteArray()
            ?: throw DeviceSecurity.Failure("pending")
        if (offer.size != 32) {
            throw CommandException("failed_precondition", UiMessages.securityGenerateAndSaveARecoveryCodeOnTheSuccessor().messageText())
        }
        val old = security.contentKey(head.core.contentEpoch) ?: throw DeviceSecurity.Failure("unavailable")
        publishSecurityStates(
            listOf(
                authorityForeground().let {
                    Authority.handoff(
                        head,
                        incoming,
                        offer,
                        old,
                        Authority.newSecret(),
                        SigningKey(account.deviceSeed),
                        security.signingTime(clock()),
                    )
                },
            ),
        )
    }

    fun chain(): List<DeviceSecurity.State> = security.chain()
    fun chainProofs(): Map<ULong, ByteArray> = security.chainProofs()

    suspend fun prepareRecovery(): PreparedRecovery {
        syncSecurity()
        val head = security.head()
        if (head.core.authorityID != account.deviceId) {
            throw CommandException("failed_precondition", UiMessages.errorsAndroidOnlyTheManagingClientCanVetoRecovery().messageText())
        }
        val secret = security.contentKey(head.core.contentEpoch) ?: throw DeviceSecurity.Failure("unavailable")
        val (seed, pub) = Authority.newRecovery()
        val next = authorityForeground().let {
            Authority.veto(head, pub, secret, SigningKey(account.deviceSeed), security.signingTime(clock()))
        }
        val (pin, _) = security.identity()
        return PreparedRecovery(RecoveryCode.encode(seed, pin.genesis), next, seed)
    }

    suspend fun publishRecovery(prepared: PreparedRecovery) {
        resumeSecurityOperation()
        syncSecurity()
        val head = security.head()
        val recoveryEd = SigningKey(prepared.newRecoverySeed).publicKey
        if (head.core.recoveryEd.contentEquals(recoveryEd)) return
        if (!DeviceSecurity.stateHash(head).contentEquals(prepared.state.core.parent)) {
            throw DeviceSecurity.Failure("stale")
        }
        publishSecurityStates(listOf(prepared.state))
    }

    suspend fun renameDevice(id: String, name: String) = changeDeviceMeta(id) { it.setName(name) }

    suspend fun describeSelf(platform: String, description: String) =
        changeDeviceMeta(account.deviceId) { it.setPlatform(platform).setDescription(description) }

    private suspend fun changeDeviceMeta(id: String, change: (DeviceMeta.Builder) -> Unit) {
        repeat(5) {
            refreshDevices()
            val current = state.value.devices[id]
                ?: throw CommandException("not_found", UiMessages.settingsDeviceNotFound().messageText())
            val next = (current.meta?.toBuilder() ?: DeviceMeta.newBuilder()).also(change).build()
            try {
                val env = security.seal(
                    DeviceSecurity.Purpose.DEVICE_META,
                    request = id,
                    seq = (current.metaRev + 1).toULong(),
                    plain = next.toByteArray(),
                )
                val applied = api.client.putDeviceMeta(
                    PutDeviceMetaRequest.newBuilder()
                        .setDeviceId(id)
                        .setExpectedRevision(current.metaRev)
                        .setMeta(env)
                        .build(),
                    Api.headers(currentToken()),
                ).value().applied
                if (!applied) return@repeat
                refreshDevices()
                return
            } catch (e: DeviceSecurity.Failure) {
                if (e.code != "conflict") throw e
            }
        }
        throw CommandException("aborted", UiMessages.errorsDeviceInformationIsChangingRetry().messageText())
    }

    suspend fun removeDevice(deviceId: String) {
        if (!cancelPairDeliveryForRemoval(deviceId)) resumeSecurityOperation()
        syncSecurity()
        val head = security.head()
        if (head.core.authorityID != account.deviceId) {
            throw CommandException("failed_precondition", UiMessages.errorsAndroidRemoveDevicesOnTheManagingClient().messageText())
        }
        if (deviceId == head.core.authorityID) {
            throw CommandException("failed_precondition", UiMessages.errorsAndroidTheManagingClientCannotBeRemovedNormally().messageText())
        }
        if (head.core.member(deviceId) == null) return
        val old = security.contentKey(head.core.contentEpoch) ?: throw DeviceSecurity.Failure("unavailable")
        val next = authorityForeground().let {
            Authority.remove(
                head,
                listOf(deviceId),
                old,
                Authority.newSecret(),
                SigningKey(account.deviceSeed),
                security.signingTime(clock()),
            )
        }
        publishSecurityStates(listOf(next))
        refreshDevices()
    }

    suspend fun deleteMachineRecords(machineId: String) {
        if (security.head().core.authorityID != account.deviceId) throw DeviceSecurity.Failure("untrusted")
        api.client.deleteMachineRecords(
            DeleteMachineRecordsRequest.newBuilder().setMachineId(machineId).build(),
            Api.headers(currentToken()),
        ).value()
        coreHost.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_RECONNECT_NOW)
    }

    // --- Transitional Step 3/4 Direct Commands (deleted once Session/Schedules/ApprovalWorker migrate to Core) ---

    fun prepare(
        machineId: String,
        sessionId: String,
        cmd: Command,
        requestId: String = newRequestId(),
        deadlineMs: Long = clock() + COMMAND_TTL_MS,
    ): PreparedCommand {
        val versioned = cmd.toBuilder().setProtocolVersion(PROTOCOL_VERSION).build()
        val observed = if (isObservedAction(versioned) && observedSeq(versioned) == 0L) {
            observeAction(versioned, currentLiveSnapshot(sessionId))
        } else {
            versioned
        }
        return PreparedCommand(machineId, sessionId, requestId, deadlineMs, Envelope.getDefaultInstance(), observed, prior = false)
    }

    private fun currentLiveSnapshot(sid: String): LiveSnapshot? =
        coreHost.currentSessionData(sid)?.takeIf { it.hasLiveSnapshot() }?.liveSnapshot
            ?: testEventFlows[sid]?.value?.live
            ?: state.value.sessions[sid]?.checkpoint

    suspend fun command(sid: String, cmd: Command): CommandResult {
        val s = state.value.sessions[sid]
            ?: throw CommandException("not_found", UiMessages.sessionAndroidUnknown().messageText())
        var actual = cmd
        if (cmd.kindCase in setOf(Command.KindCase.RESOLVE_APPROVAL, Command.KindCase.ANSWER_QUESTION, Command.KindCase.ANSWER_PLAN)) {
            val shown = currentLiveSnapshot(sid)
                ?: throw CommandException("failed_precondition", UiMessages.errorsAndroidTheComputerHasNotVerifiedTheCurrentRequest().messageText())
            val approval = cmd.kindCase == Command.KindCase.RESOLVE_APPROVAL
            val plan = cmd.kindCase == Command.KindCase.ANSWER_PLAN
            val id = if (approval) cmd.resolveApproval.approvalId else if (!plan) cmd.answerQuestion.questionId else ""
            val displayed = shown.actionsList.firstOrNull {
                if (approval) it.hasApprovalRequested() && it.approvalRequested.approvalId == id
                else it.hasQuestionAsked() && it.questionAsked.questionId == id
            }
            val seq = if (plan) shown.planEventSeq else displayed?.seq ?: 0L
            if (seq == 0L) throw CommandException("failed_precondition", UiMessages.errorsAndroidThisRequestCanNoLongerBeActedOn().messageText())
            val fresh = fetchLiveSnapshot(sid)
            if (plan) {
                if (fresh.planEventSeq != seq || !fresh.planScreenHash.equals(shown.planScreenHash)) {
                    throw CommandException("failed_precondition", UiMessages.errorsThePlanDisplayChangedReviewItAgain().messageText())
                }
                actual = cmd.toBuilder().setAnswerPlan(
                    cmd.answerPlan.toBuilder().setObservedEventSeq(seq).setObservedScreenHash(shown.planScreenHash),
                ).build()
            } else {
                val current = fresh.actionsList.firstOrNull {
                    if (approval) it.hasApprovalRequested() && it.approvalRequested.approvalId == id
                    else it.hasQuestionAsked() && it.questionAsked.questionId == id
                }
                if (current == null || current != displayed) {
                    throw CommandException("failed_precondition", UiMessages.errorsAndroidTheCurrentRequestChangedReviewItAgain().messageText())
                }
                actual = if (approval) {
                    cmd.toBuilder().setResolveApproval(cmd.resolveApproval.toBuilder().setObservedEventSeq(seq)).build()
                } else {
                    cmd.toBuilder().setAnswerQuestion(cmd.answerQuestion.toBuilder().setObservedEventSeq(seq)).build()
                }
            }
        }
        return forward(prepare(s.machineId, sid, actual))
    }

    private fun isObservedAction(cmd: Command) =
        cmd.kindCase in setOf(Command.KindCase.RESOLVE_APPROVAL, Command.KindCase.ANSWER_QUESTION, Command.KindCase.ANSWER_PLAN)

    private fun observedSeq(cmd: Command): Long = when (cmd.kindCase) {
        Command.KindCase.RESOLVE_APPROVAL -> cmd.resolveApproval.observedEventSeq
        Command.KindCase.ANSWER_QUESTION -> cmd.answerQuestion.observedEventSeq
        Command.KindCase.ANSWER_PLAN -> cmd.answerPlan.observedEventSeq
        else -> 0L
    }

    private fun observeAction(cmd: Command, snapshot: LiveSnapshot?): Command {
        if (!isObservedAction(cmd)) return cmd
        val fresh = snapshot
            ?: throw CommandException("failed_precondition", UiMessages.errorsAndroidTheComputerHasNotVerifiedTheCurrentRequest().messageText())
        return when (cmd.kindCase) {
            Command.KindCase.RESOLVE_APPROVAL -> fresh.actionsList.firstOrNull { it.hasApprovalRequested() && it.approvalRequested.approvalId == cmd.resolveApproval.approvalId }
                ?.let { cmd.toBuilder().setResolveApproval(cmd.resolveApproval.toBuilder().setObservedEventSeq(it.seq)).build() } ?: throw DeviceSecurity.Failure("stale")
            Command.KindCase.ANSWER_QUESTION -> fresh.actionsList.firstOrNull { it.hasQuestionAsked() && it.questionAsked.questionId == cmd.answerQuestion.questionId }
                ?.let { cmd.toBuilder().setAnswerQuestion(cmd.answerQuestion.toBuilder().setObservedEventSeq(it.seq)).build() } ?: throw DeviceSecurity.Failure("stale")
            Command.KindCase.ANSWER_PLAN -> if (fresh.planEventSeq == 0L || fresh.planScreenHash.size() != 32) throw DeviceSecurity.Failure("stale") else
                cmd.toBuilder().setAnswerPlan(cmd.answerPlan.toBuilder().setObservedEventSeq(fresh.planEventSeq).setObservedScreenHash(fresh.planScreenHash)).build()
            else -> cmd
        }
    }

    private suspend fun fetchLiveSnapshot(sid: String): LiveSnapshot {
        val session = state.value.sessions[sid] ?: throw CommandException("not_found", UiMessages.sessionAndroidUnknown().messageText())
        val req = io.github.lewin671.remoteagent.proto.GetLiveSnapshotRequest.newBuilder().setSessionId(sid)
        val result = forward(prepare(session.machineId, sid, Command.newBuilder().setGetLiveSnapshot(req).build()))
        if (!result.hasLiveSnapshot() || result.liveSnapshot.session.id != sid) throw DeviceSecurity.Failure()
        return result.liveSnapshot
    }

    suspend fun getMachineStatus(machineId: String): io.github.lewin671.remoteagent.proto.MachineStatus {
        val result = machineCommand(machineId, Command.newBuilder().setGetMachineStatus(io.github.lewin671.remoteagent.proto.GetMachineStatusRequest.getDefaultInstance()).build())
        check(result.hasMachineStatus()) { UiMessages.machineStatusUnavailable().messageText() }
        return result.machineStatus
    }

    suspend fun machineCommand(machineId: String, cmd: Command): CommandResult = forward(prepare(machineId, "", cmd))

    suspend fun pingServer(timeoutMs: Long = 8_000): Long = withContext(Dispatchers.IO) {
        val req = okhttp3.Request.Builder().url(api.server.trimEnd('/') + "/healthz").header("Cache-Control", "no-store").build()
        val started = System.nanoTime()
        try {
            http.newBuilder().callTimeout(timeoutMs, TimeUnit.MILLISECONDS).build().newCall(req).execute().use { res ->
                res.body?.bytes()
                if (!res.isSuccessful) throw MessageException(UiMessages.errorsServerReturned(res.code.toString()).messageText())
            }
        } catch (e: java.io.InterruptedIOException) {
            throw MessageException(UiMessages.connectionNoResponse().messageText(), e)
        }
        (System.nanoTime() - started) / 1_000_000
    }

    suspend fun pingMachine(machineId: String, timeoutMs: Long = 15_000): Long {
        val started = System.nanoTime()
        try {
            withTimeout(timeoutMs) { machineCommand(machineId, Command.newBuilder().setGetSettings(GetSettingsRequest.getDefaultInstance()).build()) }
        } catch (e: kotlinx.coroutines.TimeoutCancellationException) {
            throw MessageException(UiMessages.connectionNoResponse().messageText(), e)
        }
        return (System.nanoTime() - started) / 1_000_000
    }

    suspend fun authorize(p: PreparedCommand, retryOfUnknown: Boolean = false): Envelope {
        val asked = System.nanoTime()
        return withContext(dispatcher) {
            p.authPhases[0] = System.nanoTime() - asked
            try {
                authorizeIntent(p, retryOfUnknown)
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                throw CommandException(
                    "authorization_unavailable",
                    UiMessages.errorsAndroidSecurityAuthorizationOrTheCurrentRequestIsUnverifiedNothingWas().messageText(),
                )
            }
        }
    }

    private suspend fun authorizeIntent(p: PreparedCommand, retryOfUnknown: Boolean): Envelope = p.sealMutex.withLock {
        val head = security.head()
        val previous = p.authorizedEnvelope
        if (previous != null) {
            val h = DeviceSecurity.parseHeader(previous.securityHeader.toByteArray())
            if (h.state.contentEquals(DeviceSecurity.stateHash(head))) return@withLock previous
        }
        val env = if (p.command != null) {
            if (previous == null && !retryOfUnknown && isObservedAction(p.command)) {
                val fetchStart = System.nanoTime()
                val fresh = fetchLiveSnapshot(p.sessionId)
                p.authPhases[1] = System.nanoTime() - fetchStart
                if (observeAction(p.command, fresh) != p.command) {
                    throw CommandException("failed_precondition", UiMessages.errorsAndroidTheCurrentRequestChangedReviewItAgain().messageText())
                }
            }
            if (serverAhead || security.clockSkew) {
                throw CommandException("failed_precondition", UiMessages.errorsAndroidThePhoneClockIsAheadOfTheServerRemoteControl().messageText())
            }
            val leaseStart = System.nanoTime()
            val leaseValid = withContext(cpuLane(p)) { Authority.leaseValid(security.record(), clock()) }
            p.authPhases[2] = System.nanoTime() - leaseStart
            if (!leaseValid) {
                throw CommandException(
                    "failed_precondition",
                    localized { copy -> copy(UiMessages.errorsAndroidRemoteControlPausedValue1AndRetry(state.value.renewalAdvice.resolve(copy))) },
                )
            }
            val sealStart = System.nanoTime()
            withContext(cpuLane(p)) {
                security.seal(
                    DeviceSecurity.Purpose.COMMAND,
                    p.machineId,
                    p.sessionId,
                    p.requestId,
                    deadline = p.deadlineMs.toULong(),
                    plain = checkNotNull(p.commandBytes()),
                )
            }.also { p.authPhases[3] = System.nanoTime() - sealStart }
        } else {
            withContext(cpuLane(p)) {
                val (h, plain) = security.openOwnCommand(p.envelope)
                if (h.machine != p.machineId || h.session != p.sessionId || h.request != p.requestId || h.deadline != p.deadlineMs.toULong()) {
                    throw DeviceSecurity.Failure()
                }
                security.seal(DeviceSecurity.Purpose.COMMAND, h.machine, h.session, h.request, deadline = h.deadline, plain = plain)
            }
        }
        p.authorizedEnvelope = env
        env
    }

    fun requireFileSupport(machineId: String?, hasFiles: Boolean, mayHaveRun: Boolean = false) {
        if (hasFiles && !filesSupported(state.value.machines[machineId])) {
            throw CommandException(
                if (mayHaveRun) CommandException.UNKNOWN else "failed_precondition",
                if (mayHaveRun) localized { copy -> copy(UiMessages.errorsAndroidValue1ThePreviousOutcomeIsStillUnknownRetryTheSame(FILE_UPLOAD_UPDATE_REQUIRED.resolve(copy))) } else FILE_UPLOAD_UPDATE_REQUIRED,
                outcome = if (mayHaveRun) Outcome.UNKNOWN else Outcome.NOT_SENT,
            )
        }
    }

    private fun checkFileCapability(p: PreparedCommand, mayHaveRun: Boolean) {
        val hasFiles = when (p.command?.kindCase) {
            Command.KindCase.SEND -> p.command.send.fileIdsCount > 0
            Command.KindCase.CREATE_SESSION -> p.command.createSession.fileIdsCount > 0
            else -> false
        }
        requireFileSupport(p.machineId, hasFiles, mayHaveRun)
    }

    private fun directHint(p: PreparedCommand): RouteHint? {
        val cmd = p.command ?: return null
        return when (cmd.kindCase) {
            Command.KindCase.GET_SHARED_FILE_CHUNK ->
                RouteHint.Bulk(file = "${p.sessionId}/${cmd.getSharedFileChunk.fileId}")
            Command.KindCase.PUT_IMAGE_CHUNK -> {
                val id = cmd.putImageChunk.transferId
                val upload = directTransfers["${p.machineId}/$id"] ?: return null
                RouteHint.Bulk(file = id, upload = upload, bytes = cmd.putImageChunk.data.size())
            }
            Command.KindCase.GET_IMAGE_UPLOAD_STATUS -> null
            else -> if (cmd.isReadOnlyCommand()) RouteHint.Read else null
        }
    }

    suspend fun forward(p: PreparedCommand, retryOfUnknown: Boolean = false): CommandResult =
        withContext(dispatcher) {
            deviceLink.call(p, retryOfUnknown, dispatcher) { call ->
                forwardSingleTransitional(p, call)
            }
        }

    private suspend fun forwardSingleTransitional(p: PreparedCommand, call: DispatchState.Call): CommandResult {
        val prior = call.uncertain
        checkFileCapability(p, prior)
        protocolMismatch(state.value.machines[p.machineId])?.let {
            throw CommandException("failed_precondition", it, outcome = if (prior) Outcome.UNKNOWN else Outcome.NOT_SENT)
        }
        if (!call.uncertain && clock() >= p.deadlineMs) {
            throw CommandException(
                CommandException.EXPIRED,
                UiMessages.errorsAndroidThisCommandExpiredAndWasNotSent().messageText(),
                outcome = Outcome.NOT_SENT,
            )
        }
        val authorized = try {
            authorize(p, call.hasPrior)
        } catch (e: CommandException) {
            throw if (!call.uncertain || e.unknown || e.fromDaemon) e
            else CommandException(CommandException.UNKNOWN, UiMessages.errorsAndroidSecurityAuthorizationOrTheCurrentRequestIsUnverifiedNothingWas().messageText(), outcome = Outcome.UNKNOWN)
        }
        checkFileCapability(p, call.uncertain)
        val binding = withContext(cpuLane(p)) { security.commandIdentity(authorized) }
        val routeHint = directHint(p)
        var forceRelay = false
        var attempt = 0
        while (true) {
            checkFileCapability(p, call.uncertain)
            val tracking = routeHint?.let { direct.track(p.machineId, it) }
            val delivery = deviceLink.delivery(
                call,
                if (routeHint != null && !forceRelay) { handoff -> direct.attempt(p.machineId, routeHint, handoff) } else null,
            )
            try {
                val res = sendAndOpenCommand(
                    p.machineId, p.sessionId, p.requestId, p.command, authorized, binding,
                    prior, delivery, routeHint is RouteHint.Bulk, tracking,
                )
                val msg = res.msg
                val category = verifiedOutcome(msg, binding.first, binding.second, res.outerOutcome, res.outerReason)
                val verified = res.hashesMatch && res.attemptBound && res.outerMatchesInner
                if (!verified || category == Outcome.UNKNOWN) {
                    throw CommandException(CommandException.UNKNOWN, UiMessages.errorsAndroidCannotOpenTheComputerSReplyCommandOutcomeUnknown().messageText(), fromDaemon = true, outcome = Outcome.UNKNOWN)
                }
                if ((category == Outcome.EXECUTED && !msg.resultAvailable) || (category == Outcome.CONFLICT && msg.conflictKind == "identity")) {
                    throw CommandException(
                        msg.errorCode,
                        (if (category == Outcome.EXECUTED) UiMessages.errorsExecutedResultUnavailable() else UiMessages.errorsRequestIdentityConflict()).messageText(),
                        fromDaemon = true,
                        outcome = category,
                        conflictKind = msg.conflictKind,
                        resultAvailable = msg.resultAvailable,
                    )
                }
                if (msg.errorCode.isNotEmpty() || category != Outcome.EXECUTED) {
                    throw CommandException(
                        msg.errorCode,
                        msg.errorMessage.ifEmpty { msg.errorCode },
                        fromDaemon = true,
                        readOnly = p.command?.isReadOnlyCommand() == true,
                        outcome = category,
                        conflictKind = msg.conflictKind,
                        resultAvailable = msg.resultAvailable,
                    )
                }
                if (p.command?.hasBeginImageUpload() == true) {
                    if (directTransfers.size >= 128) directTransfers.keys.firstOrNull()?.let { directTransfers.remove(it) }
                    val req = p.command.beginImageUpload
                    directTransfers["${p.machineId}/${req.transferId}"] = if (req.fileName.isNotEmpty()) UploadKind.FILE else UploadKind.IMAGE
                }
                if (p.command?.hasCompleteImageUpload() == true) directTransfers.remove("${p.machineId}/${p.command.completeImageUpload.transferId}")
                if (p.command?.hasAbortImageUpload() == true) directTransfers.remove("${p.machineId}/${p.command.abortImageUpload.transferId}")
                if (p.command?.hasSchedule() == true && p.command.schedule.operation == "list" && msg.hasSchedule() && currentScheduleMember(p.machineId)) {
                    cache?.putBlob("schedule:${p.machineId}", scheduleSnapshot(p.requestId, res.envelope))
                    if (!currentScheduleMember(p.machineId)) cache?.removeBlob("schedule:${p.machineId}")
                }
                return msg
            } catch (_: UnverifiedReplyException) {
                throw CommandException(CommandException.UNKNOWN, UiMessages.errorsAndroidCannotOpenTheComputerSReplyCommandOutcomeUnknown().messageText(), failureKind = CommandFailureKind.REPLY_UNVERIFIED, outcome = Outcome.UNKNOWN)
            } catch (changed: PathChanged) {
                if (delivery.direct && routeHint != null && !forceRelay) {
                    forceRelay = true
                    tracking?.failed()
                    val put = p.command?.takeIf { it.hasPutImageChunk() }?.putImageChunk
                    if (put != null) {
                        machineCommand(
                            p.machineId,
                            Command.newBuilder()
                                .setGetImageUploadStatus(
                                    io.github.lewin671.remoteagent.proto.GetImageUploadStatusRequest.newBuilder()
                                        .setTransferId(put.transferId)
                                        .build(),
                                )
                                .build(),
                        )
                    }
                    continue
                }
                if (call.uncertain) {
                    throw CommandException(CommandException.UNKNOWN, errorText(changed, state.value), outcome = Outcome.UNKNOWN)
                }
                throw changed
            } catch (ce: ConnectException) {
                if (delivery.direct) tracking?.failed()
                val refusal = delivery.refusal(ce)
                if (refusal != null && !call.uncertain) {
                    val msgText = if (refusal.reason == "machine_offline") UiMessages.errorsTheComputerIsOfflineTheCommandWasNotSent().messageText()
                    else if (ce.code == Code.UNAVAILABLE) (ce.message ?: ce.code.name.lowercase()).messageText()
                    else errorText(ce)
                    throw CommandException(ce.code.name.lowercase(), msgText, outcome = Outcome.NOT_SENT)
                }
                val cause = ce.exception
                if (cause is java.net.UnknownHostException || cause is java.net.ConnectException) {
                    throw if (call.uncertain) {
                        CommandException(CommandException.UNKNOWN, errorText(ce, state.value), outcome = Outcome.UNKNOWN)
                    } else {
                        CommandException("network", errorText(ce), outcome = Outcome.NOT_SENT)
                    }
                }
                if (ce.code == Code.UNAUTHENTICATED && attempt < 3) {
                    attempt++
                    delivery.refreshAuth(call.uncertain)
                    continue
                }
                if (ce.code in setOf(Code.DEADLINE_EXCEEDED, Code.UNKNOWN, Code.INTERNAL_ERROR, Code.CANCELED, Code.ABORTED) &&
                    attempt < 3 &&
                    clock() + 2_000L < p.deadlineMs
                ) {
                    attempt++
                    kotlinx.coroutines.delay(1_000L * attempt)
                    continue
                }
                if (ce.code == Code.UNAUTHENTICATED) delivery.refreshAuth(call.uncertain)
                throw CommandException(CommandException.UNKNOWN, errorText(ce, state.value), outcome = Outcome.UNKNOWN)
            } catch (e: Exception) {
                if (delivery.direct && e !is CommandException && e !is CancellationException) tracking?.failed()
                if (e is CommandException) throw e
                if (call.uncertain) {
                    throw CommandException(CommandException.UNKNOWN, errorText(e, state.value), outcome = Outcome.UNKNOWN)
                }
                throw e
            }
        }
    }

    suspend fun listModels(
        machineId: String,
        agent: Agent = Agent.AGENT_UNSPECIFIED,
        fresh: Boolean = false,
    ): ListModelsResponse {
        val d = withContext(dispatcher) {
            val k = "$machineId/${agent.number}"
            models[k]?.takeUnless { it.isCompleted && (fresh || it.getCompletionExceptionOrNull() != null) }
                ?: scope.async {
                    val r = machineCommand(
                        machineId,
                        Command.newBuilder().setListModels(ListModelsRequest.newBuilder().setAgent(agent)).build(),
                    )
                    if (!r.hasListModels()) throw CommandException("internal", UiMessages.newSessionUnexpectedReply().messageText())
                    r.listModels
                }.also { models[k] = it }
        }
        return d.await()
    }

    // --- Media, Files & Terminal ---

    sealed interface ImageResult {
        class Loaded(val payload: ImagePayload) : ImageResult
        data object Expired : ImageResult
        data object Missing : ImageResult
        data class Corrupt(val why: MessageText) : ImageResult
    }

    suspend fun image(sid: String, imageId: String): ImageResult = withContext(dispatcher) {
        val s = state.value.sessions[sid] ?: throw CommandException("not_found", UiMessages.sessionAndroidUnknown().messageText())
        if (sessionKeys[sid] == null) throw CommandException("not_found", UiMessages.sessionAndroidUnknown().messageText())
        val cacheKey = "$sid/$imageId"
        var env = runCatching { cache?.image(cacheKey)?.let { Envelope.parseFrom(it) } }.getOrNull()
        if (env == null) {
            val r = try {
                api.client.getImage(
                    GetImageRequest.newBuilder().setSessionId(sid).setImageId(imageId).build(),
                    Api.headers(currentToken()),
                ).value()
            } catch (e: ConnectException) {
                if (e.code == Code.NOT_FOUND) return@withContext ImageResult.Missing
                throw e
            }
            if (r.expired || !r.hasImage()) return@withContext ImageResult.Expired
            env = r.image
        }
        val payload = try {
            openSecureMessage(
                env,
                Place(PayloadKind.PAYLOAD_KIND_IMAGE, machineId = s.machineId, sessionId = sid, requestId = imageId),
                ImagePayload::parseFrom,
                live = false,
            ).second
        } catch (_: UnsupportedHistoryException) {
            return@withContext ImageResult.Corrupt(RETIRED_HISTORY_ERROR)
        } catch (e: OpenException) {
            return@withContext ImageResult.Corrupt(e.display)
        }
        if (detectFormat(payload.data.toByteArray()) != payload.format) {
            return@withContext ImageResult.Corrupt(UiMessages.errorsAndroidImageContentDoesNotMatchItsFormat().messageText())
        }
        runCatching { cache?.putImage(cacheKey, env.toByteArray()) }
        ImageResult.Loaded(payload)
    }

    fun uploadChannel(machineId: String): UploadChannel = object : UploadChannel {
        private suspend fun run(b: Command.Builder.() -> Unit): CommandResult =
            machineCommand(machineId, Command.newBuilder().apply(b).build())

        override suspend fun begin(
            transferId: String,
            format: io.github.lewin671.remoteagent.proto.ImageFormat,
            total: Long,
            sha256: ByteArray,
        ) = run {
            beginImageUpload = io.github.lewin671.remoteagent.proto.BeginImageUploadRequest.newBuilder()
                .setTransferId(transferId)
                .setFormat(format)
                .setTotalBytes(total)
                .setSha256(ByteString.copyFrom(sha256))
                .build()
        }.beginImageUpload.status

        override suspend fun beginFile(transferId: String, name: String, total: Long, sha256: ByteArray) = run {
            beginImageUpload = io.github.lewin671.remoteagent.proto.BeginImageUploadRequest.newBuilder()
                .setTransferId(transferId)
                .setFileName(name)
                .setFormat(io.github.lewin671.remoteagent.proto.ImageFormat.IMAGE_FORMAT_UNSPECIFIED)
                .setTotalBytes(total)
                .setSha256(ByteString.copyFrom(sha256))
                .build()
        }.beginImageUpload.status

        override suspend fun put(transferId: String, offset: Long, data: ByteArray) = run {
            putImageChunk = io.github.lewin671.remoteagent.proto.PutImageChunkRequest.newBuilder()
                .setTransferId(transferId)
                .setOffset(offset)
                .setData(ByteString.copyFrom(data))
                .build()
        }.putImageChunk.status

        override suspend fun status(transferId: String) = run {
            getImageUploadStatus = io.github.lewin671.remoteagent.proto.GetImageUploadStatusRequest.newBuilder()
                .setTransferId(transferId)
                .build()
        }.getImageUploadStatus.status

        override suspend fun abort(transferId: String) {
            run { abortImageUpload = io.github.lewin671.remoteagent.proto.AbortImageUploadRequest.newBuilder().setTransferId(transferId).build() }
        }

        override suspend fun complete(transferId: String) = run {
            completeImageUpload = io.github.lewin671.remoteagent.proto.CompleteImageUploadRequest.newBuilder()
                .setTransferId(transferId)
                .build()
        }.completeImageUpload.status
    }

    suspend fun openTerminal(sid: String, listener: TermListener): TermConn {
        val s = state.value.sessions[sid] ?: throw CommandException("not_found", UiMessages.sessionAndroidUnknown().messageText())
        if (sessionKeys[sid] == null) throw CommandException("not_found", UiMessages.sessionAndroidUnknown().messageText())
        val tok = currentToken()
        val streamId = newRequestId()
        val opened = CompletableDeferred<Unit>()
        val closed = AtomicBoolean(false)
        var last = 0L
        val incoming = Channel<ByteArray>(64)
        data class QueuedInput(val frame: TerminalFrame, val deadline: Long)
        val outgoing = Channel<QueuedInput>(64)
        val queuedBytes = AtomicLong(0)
        val terminalJob = SupervisorJob(scope.coroutineContext[Job])
        val terminalScope = CoroutineScope(scope.coroutineContext + terminalJob)
        val socket = java.util.concurrent.atomic.AtomicReference<FrameStream?>()
        val stream = deviceLink.openStream(stop = { reason ->
            if (closed.compareAndSet(false, true)) {
                cleanup(
                    { incoming.cancel() },
                    { outgoing.cancel() },
                    { terminalJob.cancel() },
                    { socket.get()?.cancel() },
                    { opened.completeExceptionally(MessageException(reason ?: UiMessages.terminalAndroidClosed().messageText())) },
                )
            }
        }, onClose = listener::onClose)
        fun finish(reason: MessageText? = null) = stream.end(reason)

        terminalScope.launch {
            try {
                for (raw in incoming) {
                    try {
                        val env = Envelope.parseFrom(raw)
                        val (h, frame) = openSecureMessage(
                            env,
                            Place(PayloadKind.PAYLOAD_KIND_TERMINAL_OUTPUT, machineId = s.machineId, sessionId = sid, requestId = streamId),
                            TerminalFrame::parseFrom,
                        )
                        if (ucmp(h.seq, last) <= 0) throw OpenException(UiMessages.errorsTerminalFramesAreOutOfOrder().messageText())
                        last = h.seq
                        listener.onFrame(frame)
                    } finally {
                        queuedBytes.addAndGet(-raw.size.toLong())
                    }
                }
            } catch (e: Exception) {
                finish(errorText(e, state.value))
            }
        }
        val url = account.server.replaceFirst(Regex("^http"), "ws") + "/v1/term"
        try {
            val ws = deviceLink.router.path.openStream(
                url,
                object : FrameListener() {
                    override fun onOpen(stream: FrameStream) {
                        val hello = TermHello.newBuilder().setToken(tok).setStreamId(streamId).setMachineId(s.machineId).build()
                        stream.send(hello.toByteArray())
                        opened.complete(Unit)
                    }
                    override fun onFrame(stream: FrameStream, bytes: ByteArray) {
                        if (closed.get()) return
                        if (bytes.size > (1 shl 20) || queuedBytes.addAndGet(bytes.size.toLong()) > 2L * 1024 * 1024 || incoming.trySend(bytes).isFailure) {
                            finish(UiMessages.errorsAndroidTerminalOutputBacklogReceptionStopped().messageText())
                        }
                    }
                    override fun onClosing(stream: FrameStream, code: Int) {
                        if (code == POLICY_VIOLATION) rejectToken(tok)
                        cleanup({ stream.close() }, { finish(UiMessages.errorsTerminalConnectionDropped().messageText()) })
                    }
                    override fun onEnd(stream: FrameStream, code: Int) {
                        if (code == POLICY_VIOLATION) rejectToken(tok)
                        finish(UiMessages.errorsTerminalConnectionDropped().messageText())
                    }
                    override fun onFailure(stream: FrameStream, error: Throwable) {
                        opened.completeExceptionally(MessageException(UiMessages.errorsCannotConnectToTerminal().messageText()))
                        finish(localized { copy -> copy(UiMessages.errorsAndroidTerminalConnectionDroppedReason(errorText(error, state.value).resolve(copy))) })
                    }
                },
            )
            socket.set(ws)
            if (closed.get()) ws.cancel()
            withTimeout(20_000) { opened.await() }
            command(sid, Command.newBuilder().setOpenTerminal(OpenTerminalRequest.newBuilder().setSessionId(sid).setStreamId(streamId)).build())
            kotlinx.coroutines.currentCoroutineContext().ensureActive()
            if (closed.get()) throw MessageException(UiMessages.terminalAndroidClosed().messageText())
            var seq = 0L
            terminalScope.launch {
                try {
                    for (item in outgoing) {
                        if (closed.get()) break
                        seq++
                        val env = sealSecureMessage(
                            Header.newBuilder()
                                .setKind(PayloadKind.PAYLOAD_KIND_TERMINAL_INPUT)
                                .setMachineId(s.machineId)
                                .setSessionId(sid)
                                .setRequestId(streamId)
                                .setSeq(seq)
                                .setDeadlineMs(item.deadline)
                                .setSender(account.deviceId)
                                .build(),
                            item.frame,
                        )
                        if (!closed.get()) ws.send(env.toByteArray())
                    }
                } catch (e: Exception) {
                    finish(errorText(e, state.value))
                }
            }
            return TermConn(
                ws,
                sendFrame = { frame ->
                    if (frame.serializedSize > 64 * 1024 || outgoing.trySend(QueuedInput(frame, clock() + 60_000)).isFailure) {
                        finish(UiMessages.errorsAndroidTerminalInputPausedWhileWaitingForAuthorization().messageText())
                    }
                },
                closeLocal = { finish() },
            )
        } catch (e: Exception) {
            finish()
            throw e
        }
    }

    suspend fun getSharedFileChunk(sessionId: String, fileId: String, offset: Long): GetSharedFileChunkResponse {
        val result = command(
            sessionId,
            Command.newBuilder()
                .setGetSharedFileChunk(GetSharedFileChunkRequest.newBuilder().setSessionId(sessionId).setFileId(fileId).setOffset(offset))
                .build(),
        )
        if (!result.hasGetSharedFileChunk()) {
            throw CommandException("invalid_response", UiMessages.errorsAndroidCannotOpenTheComputerSReplyCommandOutcomeUnknown().messageText())
        }
        return result.getGetSharedFileChunk()
    }

    // --- Crypto Envelope Helpers ---

    private fun report(e: Throwable) {
        log("remote-agent: " + e.javaClass.simpleName)
        if (System.getenv("RA_DEBUG_STACK") != null) e.printStackTrace()
    }

    private fun projection(h: DeviceSecurity.Header): Header = Header.newBuilder()
        .setVersion(DeviceSecurity.VERSION)
        .setKindValue(h.kind.toInt())
        .setMachineId(h.machine)
        .setSessionId(h.session)
        .setRequestId(h.request)
        .setSeq(h.seq.toLong())
        .setDeadlineMs(h.deadline.toLong())
        .setSender(h.sender)
        .setCompressionValue(h.compression.toInt())
        .build()

    private fun sessionReplacements(sid: String): List<MachineReplacementProof> =
        coreHost.syncStateView.value.sessionsList
            .firstOrNull { it.sessionId == sid && it.hasInfo() }
            ?.info?.machineReplacementsList
            ?: state.value.sessions[sid]?.info?.machineReplacementsList
            ?: emptyList()

    private fun historicalPlace(want: Place, header: Header, historical: Boolean): Place {
        if (
            historical &&
            want.kind in setOf(PayloadKind.PAYLOAD_KIND_EVENT, PayloadKind.PAYLOAD_KIND_IMAGE) &&
            header.machineId != want.machineId &&
            header.machineId in machineReplacementOrigins(security.record(), want.machineId, sessionReplacements(want.sessionId))
        ) {
            return want.copy(machineId = header.machineId)
        }
        return want
    }

    private suspend fun openSecureKey(env: Envelope, want: Place): ByteArray {
        rejectRetiredHistory(env)
        val (header, raw) = withContext(cpuDispatcher) { security.openHistory(env) }
        Envelopes.expect(projection(header), want)
        return raw
    }

    private suspend fun <T : com.google.protobuf.MessageLite> openSecureMessage(
        env: Envelope,
        want: Place,
        parse: (ByteArray) -> T,
        live: Boolean = true,
        bulk: Boolean = false,
    ): Pair<Header, T> {
        val history = !live || want.kind in setOf(
            PayloadKind.PAYLOAD_KIND_ACCOUNT_PREFS,
            PayloadKind.PAYLOAD_KIND_DEVICE_META,
            PayloadKind.PAYLOAD_KIND_MACHINE_META,
            PayloadKind.PAYLOAD_KIND_SESSION_META,
            PayloadKind.PAYLOAD_KIND_IMAGE,
        )
        rejectRetiredHistory(env)
        val h = DeviceSecurity.parseHeader(env.securityHeader.toByteArray())
        val seed = if (h.purpose.sessionKey) sessionKeys[h.session] else null
        val (header, m) = try {
            withContext(if (bulk) bulkCpuDispatcher else cpuDispatcher) {
                security.openMessage(env, history, seed, parse)
            }
        } catch (e: RecoveryHistoryUnavailable) {
            Envelopes.expect(projection(e.header), historicalPlace(want, projection(e.header), history))
            throw e
        }
        val projected = projection(header)
        Envelopes.expect(projected, historicalPlace(want, projected, history))
        return projected to m
    }

    private suspend fun sealSecureMessage(h: Header, m: com.google.protobuf.MessageLite): Envelope {
        var purpose = DeviceSecurity.Purpose.entries.firstOrNull { it.value == h.kindValue.toUInt() }
            ?: throw DeviceSecurity.Failure()
        if (m is TerminalFrame) {
            purpose = when (m.kindCase) {
                TerminalFrame.KindCase.INPUT -> DeviceSecurity.Purpose.TERMINAL_INPUT
                TerminalFrame.KindCase.RESIZE -> DeviceSecurity.Purpose.TERMINAL_RESIZE
                TerminalFrame.KindCase.HISTORY_REQUEST -> DeviceSecurity.Purpose.TERMINAL_HISTORY
                else -> purpose
            }
        }
        val seed = if (purpose.sessionKey) sessionKeys[h.sessionId] else null
        return security.seal(
            purpose,
            h.machineId,
            h.sessionId,
            h.requestId,
            h.seq.toULong(),
            h.deadlineMs.toULong(),
            m.toByteArray(),
            seed,
        )
    }
}

internal fun Command.isReadOnlyCommand(): Boolean =
    hasGetMachineStatus() || hasGetSettings() || hasListModels() || hasListDirectories() ||
        hasGetLiveSnapshot() || hasGetImageUploadStatus() || hasGetSharedFileChunk() ||
        (hasSchedule() && schedule.operation in setOf("list", "get"))

object CommandPhases {
    @Volatile var sink: ((String) -> Unit)? = null

    internal fun note(kind: String, path: String, t0: Long, t1: Long, t2: Long, t3: Long, t4: Long, auth: LongArray) {
        val out = sink ?: return
        fun ms(a: Long, b: Long) = "%.1f".format((b - a) / 1_000_000.0)
        fun m(v: Long) = "%.1f".format(v / 1_000_000.0)
        out("cmd kind=$kind path=$path authorize=${ms(t0, t1)}(queue=${m(auth[0])} observe=${m(auth[1])} lease=${m(auth[2])} seal=${m(auth[3])}) bind=${ms(t1, t2)} send=${ms(t2, t3)} open=${ms(t3, t4)}")
    }
}
