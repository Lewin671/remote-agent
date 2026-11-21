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
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.put
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
    private val scheduleSeenStore: ScheduleSeenStore? = null,
    private val onDismissScheduleNotifications: (List<ScheduleNotificationRef>) -> Unit = {},
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

    private val peerJobs = java.util.concurrent.atomic.AtomicInteger()
    private val device = SigningKey(account.deviceSeed)
    val api = Api(account.server, http, ::rejectToken, rpcTransport)
    @Volatile private var relayObservation = RemoteState()
    private val directServerHost: String =
        if (stunUrls != null) "" else runCatching { java.net.URI(account.server).host.orEmpty() }.getOrDefault("")
    private val directConfiguredStun: String = stunUrls?.joinToString(",") ?: ""
    private val scope = CoroutineScope(SupervisorJob() + this.dispatcher)
    private val usageHydratedMachines = ConcurrentHashMap.newKeySet<String>()

    private fun usageBlobKey(machineId: String, preset: UsageRangePreset): String =
        "usage:$machineId:${preset.number}"

    private suspend fun removeUsageBlobs(machineId: String) {
        usageHydratedMachines.remove(machineId)
        for (preset in USAGE_CACHE_PRESETS) {
            cache?.removeBlob(usageBlobKey(machineId, preset))
        }
    }

    // --- Token & stream runtime state ---

    private val tokenMutex = Mutex()
    @Volatile private var cachedToken: LoginToken? = null
    private var inFlightToken: CompletableDeferred<LoginToken>? = null
    @Volatile private var inFlightLoginJob: Job? = null
    @Volatile private var networkAvailable = true
    @Volatile private var lastNetworkSignature: String? = null
    @Volatile private var networkGeneration = 0L
    private val securityPushSeq = AtomicLong(1L)
    private val devicesPushSeq = AtomicLong(1L)
    private val coreSyncDepth = java.util.concurrent.atomic.AtomicInteger(0)
    private val securitySnapshotLock = Any()
    @Volatile private var running = false
    @Volatile private var closed = false
    @Volatile private var serverAhead = false

    private val streamMutex = Mutex()
    private var activeLegId = 0L
    @Volatile private var activeSocket: FrameStream? = null
    internal val activeStreamForTest: FrameStream? get() = activeSocket
    private var activeLegJob: Job? = null

    private sealed interface CacheWritePayload {
        class MachineWrite(val m: Machine) : CacheWritePayload
        class SessionWrite(val s: RemoteSession) : CacheWritePayload
        class KeepOwnerWrite(val s: RemoteSession) : CacheWritePayload
        class EventsWrite(val sessionId: String, val events: List<StoredEvent>) : CacheWritePayload
        class PrefsWrite(val p: AccountPrefsObject) : CacheWritePayload
        class SessionOpenWrite(val sessionId: String, val open: Boolean) : CacheWritePayload
    }

    private val pendingCacheOps = ArrayDeque<suspend () -> Unit>()
    private var cacheWriterActive = false

    private fun enqueueCacheOp(op: suspend () -> Unit) {
        val start = synchronized(pendingCacheOps) {
            pendingCacheOps.addLast(op)
            if (!cacheWriterActive) {
                cacheWriterActive = true
                true
            } else {
                false
            }
        }
        if (!start) return
        scope.launch {
            while (true) {
                val next = synchronized(pendingCacheOps) {
                    if (pendingCacheOps.isEmpty()) {
                        cacheWriterActive = false
                        null
                    } else {
                        pendingCacheOps.removeFirst()
                    }
                } ?: break
                try {
                    next()
                } catch (e: Exception) {
                    if (e is CancellationException) throw e
                    report(e)
                }
            }
        }
    }

    // --- Direct peer executor & Security ---

    @Volatile private var latestXPrivate: ByteArray? = null
    private val securityConfigured = AtomicBoolean(false)
    @Volatile private var securityRecordLoaded = false

    private val securityExecutor = SecurityExecutor(
        signer = { SigningKey(account.deviceSeed) },
        xPrivate = { latestXPrivate ?: runCatching { security.record().peerBoxKey() }.getOrNull() },
    )

    internal val directExecutor: DirectExecutor = DirectExecutor(
        emit = { ev -> coreHost.dispatch(ev) },
        onPushedEvent = { machineId, pcId, env, header, size ->
            handleDirectEvent(machineId, pcId, env, header, size)
        },
        factory = rtcFactory,
        clock = clock,
        monotonic = monotonic,
        dispatcher = directDispatcher,
        bulkReceiveDispatcher = bulkCpuDispatcher,
    )

    internal var deviceLink = DeviceLink(RelayPath(
        api = api,
        sockets = { socketFactory ?: streamHttp },
        token = { currentToken() },
        renew = { used, _ -> if (used != null) discardToken(used); currentToken() },
        observe = { id ->
            relayObservation.let {
                val directReady = coreHost.syncStateView.value.machinesList.firstOrNull { m -> m.machineId == id }?.directReady == true
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
                    protocolVersion = if (directReady) PEER_PROTOCOL_VERSION else base.protocolVersion,
                    capabilities = base.capabilities,
                )
            }
        },
    ))

    fun peer(machineId: String?) = state.value.peer(machineId)
    fun directEnabled(machineId: String): Boolean =
        runCatching { directPrefs.isEnabled(account.server, account.deviceId, machineId) }.getOrDefault(true)
    fun setDirectEnabled(machineId: String, enabled: Boolean) = coreHost.updateDirectPreference(machineId, enabled)
    fun directRouteReport(machineId: String): DirectRouteReport = coreHost.snapshotDirectDiagnosticsView(machineId).report
    fun filePath(machineId: String): DirectPathChoice =
        if (directRouteReport(machineId).fileRoute == DirectPathChoice.DIRECT_PATH_CHOICE_DIRECT) DirectPathChoice.DIRECT_PATH_CHOICE_DIRECT
        else DirectPathChoice.DIRECT_PATH_CHOICE_RELAY
    fun monotonicMillis(): Long = monotonic()
    @Volatile internal var parseServerFrame: (ByteArray) -> ServerFrame = ServerFrame::parseFrom

    private fun ensureSecurityConfigured() {
        if (closed) return
        if (securityConfigured.compareAndSet(false, true)) {
            coreHost.sendSecurityConfig(account.accountId, account.deviceId, inlineCrypto = true)
        }
    }

    val security = AuthoritySecurity(
        securityStorage ?: MissingDeviceSecurityStorage,
        account.deviceId,
        expectedAccount = account.accountId.ifEmpty { null },
        clock = clock,
        onSync = { record, _ ->
            runCatching { latestXPrivate = record.peerBoxKey() }
            pushSecurityRecordFromRecord(record, skipIfCoreSync = true)
        },
        onInstall = { record ->
            runCatching { latestXPrivate = record.peerBoxKey() }
            pushSecurityRecordFromRecord(record, skipIfCoreSync = false)
        },
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
    @Volatile private var watchedSessionIds: List<String> = emptyList()

    private val archiveMachines = ConcurrentHashMap.newKeySet<String>()
    private val trustedMembers = ConcurrentHashMap<String, DeviceSecurity.Member>()
    @Volatile private var recoveryObjectsPending = true
    private var authorityJob = SupervisorJob(scope.coroutineContext[Job])

    @Volatile var homeScopeStore: HomeScopeStore? = null
    @Volatile var uiMessageResolver: MessageResolver = { it.key }

    private val _bootstrapped = MutableStateFlow(cache == null)
    /** True once the initial encrypted cache bootstrap has been loaded and rendered into Core views. */
    val bootstrapped: StateFlow<Boolean> = _bootstrapped.asStateFlow()

    val coreHost = CoreHost(
        workerDispatcher = if (dispatcher != null) this.dispatcher else null,
        clock = clock,
        monotonic = monotonic,
    )
    val homeView: StateFlow<HomeViewModel> get() = coreHost.homeView
    val projectView: StateFlow<ProjectViewModel> get() = coreHost.projectView
    val newSessionView: StateFlow<NewSessionViewModel> get() = coreHost.newSessionView
    val settingsView: StateFlow<SettingsViewModel> get() = coreHost.settingsView
    val machineStatusView: StateFlow<MachineStatusViewModel> get() = coreHost.machineStatusView
    val machineSettingsView: StateFlow<MachineSettingsViewModel> get() = coreHost.machineSettingsView
    val directDiagnosticsView: StateFlow<DirectDiagnosticsViewModel> get() = coreHost.directDiagnosticsView
    val quotaView: StateFlow<QuotaViewModel> get() = coreHost.quotaView
    val devicesView: StateFlow<DevicesViewModel> get() = coreHost.devicesView
    val clientDetailView: StateFlow<ClientDetailViewModel> get() = coreHost.clientDetailView
    val usageView: StateFlow<UsageViewModel> get() = coreHost.usageView
    val schedulesView: StateFlow<SchedulesViewModel> get() = coreHost.schedulesView
    val scheduleDetailView: StateFlow<ScheduleDetailViewModel> get() = coreHost.scheduleDetailView
    val scheduleFormView: StateFlow<ScheduleFormViewModel> get() = coreHost.scheduleFormView
    val coreNavigation: SharedFlow<NavigateEffect> get() = coreHost.navigationEvents
    val scheduleNavigationEvents: SharedFlow<ScheduleNavigateEffect> get() = coreHost.scheduleNavigationEvents
    val restoreComposerEvents: SharedFlow<SessionRestoreComposerEffect> get() = coreHost.restoreComposerEvents
    val resendAttachmentsEvents: SharedFlow<SessionResendAttachmentsEffect> get() = coreHost.resendAttachmentsEvents

    fun watchSession(sid: String, initialWindow: SessionWindowRequest? = null): Flow<SessionViewModel> {
        state.value.sessions[sid]?.machineId?.takeIf { it.isNotEmpty() }?.let { coreHost.hydrateUsageCache(it) }
        return coreHost.watchSession(sid, initialWindow)
    }

    fun currentSessionView(sid: String): SessionViewModel? =
        coreHost.currentSessionView(sid)

    fun updateSessionWindow(sid: String, window: SessionWindowRequest) =
        coreHost.updateSessionWindow(sid, window)

    fun openSession(sid: String, refreshModels: Boolean = false, refreshSettings: Boolean = false) =
        coreHost.openSession(sid, refreshModels, refreshSettings)

    fun submitSessionCommand(sid: String, cmd: Command, machineId: String = "", slotKey: String = "") =
        coreHost.submitSessionCommand(sid, cmd, machineId, slotKey)

    fun abandonSessionCommand(sid: String, slotKey: String) =
        coreHost.abandonSessionCommand(sid, slotKey)

    fun cancelTrayItem(sid: String, requestId: String) =
        coreHost.cancelTrayItem(sid, requestId)

    fun ignoreTrayItem(sid: String, requestId: String) =
        coreHost.ignoreTrayItem(sid, requestId)

    fun promoteTrayItem(sid: String, requestId: String) =
        coreHost.promoteTrayItem(sid, requestId)

    fun restoreTrayItem(sid: String, requestId: String) =
        coreHost.restoreTrayItem(sid, requestId)

    fun retryTrayItem(sid: String, requestId: String) =
        coreHost.retryTrayItem(sid, requestId)

    fun resumeTrayItem(sid: String, requestId: String) =
        coreHost.resumeTrayItem(sid, requestId)

    fun cancelAllPaused(sid: String) =
        coreHost.cancelAllPaused(sid)

    fun undoCancel(sid: String) =
        coreHost.undoCancel(sid)

    fun setSessionQuestionIndex(sid: String, questionId: String, index: Int) =
        coreHost.setSessionQuestionIndex(sid, questionId, index)

    fun loadEarlierSessionHistory(sid: String) =
        coreHost.loadEarlierSessionHistory(sid)

    // --- Transitional Step 4 view & schedule projection ---

    @Volatile private var testStateOverride: RemoteState? = null
    private val _testStateFlow = MutableStateFlow<RemoteState?>(null)
    private val pendingSessionLookups = ConcurrentHashMap<String, java.util.concurrent.CopyOnWriteArrayList<CompletableDeferred<Boolean>>>()
    private val _networkAvailableFlow = MutableStateFlow(true)
    private val _state = MutableStateFlow(RemoteState())
    val state: StateFlow<RemoteState> = _state.asStateFlow()

    private fun updateProjectedStateNow() {
        val next = testStateOverride ?: coreHost.syncStateView.value.toRemoteState(networkAvailable)
        relayObservation = next
        _state.value = next
    }

    init {
        coreHost.onDirectPreference = { eff ->
            runCatching { directPrefs.setEnabled(account.server, account.deviceId, eff.machineId, eff.enabled) }
        }
        coreHost.onRender = { targets ->
            if (io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_SYNC_STATE in targets) {
                updateProjectedStateNow()
            }
        }
        scope.launch {
            combine(
                coreHost.syncStateView,
                _networkAvailableFlow,
                _testStateFlow,
            ) { syncView, net, override ->
                (override ?: syncView.toRemoteState(net)).also { relayObservation = it }
            }.collect { _state.value = it }
        }
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
        val envText = (snapshot["envelope"] as? JsonPrimitive)?.contentOrNull.orEmpty()
        val reqText = (snapshot["request"] as? JsonPrimitive)?.contentOrNull.orEmpty()
        val (_, reply) = openSecureMessage(
            Envelope.parseFrom(java.util.Base64.getDecoder().decode(envText)),
            Place(PayloadKind.PAYLOAD_KIND_COMMAND_RESULT, machineId = machine, requestId = reqText),
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
        coreHost.onSecurityKeySign = securityExecutor::keySign
        coreHost.onSecurityKeyAgree = securityExecutor::keyAgree
        coreHost.onDirectSignalSend = { eff -> directExecutor.signalSend(eff) }
        coreHost.onDirectPcCreate = { eff -> scope.launch(directDispatcher) { directExecutor.pcCreate(eff) } }
        coreHost.onDirectPcDescribe = { eff -> scope.launch(directDispatcher) { directExecutor.pcDescribe(eff) } }
        coreHost.onDirectPcSetRemote = { eff -> scope.launch(directDispatcher) { directExecutor.pcSetRemote(eff) } }
        coreHost.onDirectPcAddCandidates = { eff -> scope.launch(directDispatcher) { directExecutor.pcAddCandidates(eff) } }
        coreHost.onDirectPcConfirm = { eff -> directExecutor.pcConfirm(eff) }
        coreHost.onDirectPcClose = { eff -> scope.launch(directDispatcher) { directExecutor.pcClose(eff) } }
        coreHost.onDirectDcReadStats = { eff -> scope.launch(directDispatcher) { directExecutor.dcReadStats(eff) } }
        coreHost.onWatchedSessionsChanged = { ids ->
            watchedSessionIds = ids
        }
        watchedSessionIds = coreHost.watchedSessionIds()
        coreHost.onSaveHomeScope = { eff ->
            homeScopeStore?.writeToken(eff.scopeToken.ifEmpty { encodeHomeScopeToken(account.localKey, eff.scopeKey) })
        }
        coreHost.onEnsureToken = { eff -> scope.launch { ensureTokenForCore(eff.opId, eff.forceLogin) } }
        coreHost.onDiscardToken = { eff -> scope.launch { discardToken(eff.token) } }
        coreHost.onSyncSecurity = { eff -> scope.launch { syncSecurityForCore(eff.opId, eff.networkGeneration) } }
        coreHost.onOpenStream = { eff -> scope.launch { openStreamForCore(eff.legId, eff.token) } }
        coreHost.onCloseStream = { eff -> scope.launch { closeStreamLeg(eff.legId) } }
        coreHost.onCacheRead = { eff ->
            if (cache == null) scope.launch { executeCacheRead(eff) }
            else enqueueCacheOp { executeCacheRead(eff) }
        }
        coreHost.onCacheWrite = { eff ->
            extractCacheWrite(eff)?.let { payload -> enqueueCacheOp { writeCachePayload(payload) } }
        }
        coreHost.onCacheDelete = { eff ->
            if (cache != null || eff.kindCase == CacheDeleteEffect.KindCase.REMOVE_SESSION_ID || eff.kindCase == CacheDeleteEffect.KindCase.REMOVE_MACHINE_ID) {
                enqueueCacheOp { executeCacheDelete(eff) }
            }
        }
        coreHost.onRpc = { eff -> scope.launch { executeRpc(eff) } }
        coreHost.onDispatchCommand = { eff -> scope.launch { executeDispatchCommand(eff) } }
        coreHost.onScheduleReadCache = { eff ->
            enqueueCacheOp {
                try {
                    val bytes = cachedSchedules(eff.machineId)
                    coreHost.deliverScheduleCacheLoaded(eff.opId, eff.machineId, bytes ?: ByteArray(0))
                } catch (e: Exception) {
                    if (e is CancellationException) throw e
                    coreHost.deliverScheduleCacheLoaded(
                        eff.opId,
                        eff.machineId,
                        error = e.message ?: e.javaClass.simpleName,
                    )
                }
            }
        }
        coreHost.onScheduleDeleteCache = { eff ->
            if (cache != null) enqueueCacheOp { cache.removeBlob("schedule:${eff.machineId}") }
        }
        coreHost.onScheduleLoadSeen = { eff ->
            scope.launch {
                val (tokens, err) = try {
                    scheduleSeenStore?.load(eff.machineId) to ""
                } catch (e: Exception) {
                    if (e is CancellationException) throw e
                    null to (e.message ?: e.javaClass.simpleName)
                }
                coreHost.deliverScheduleSeenLoaded(
                    eff.machineId,
                    tokens ?: emptyList(),
                    found = tokens != null && err.isEmpty(),
                    error = err,
                )
            }
        }
        coreHost.onScheduleSaveSeen = { eff ->
            scope.launch { scheduleSeenStore?.save(eff.machineId, eff.tokensList) }
        }
        coreHost.onScheduleDismissNotifications = { eff ->
            onDismissScheduleNotifications(eff.runsList)
        }
        coreHost.onUsageCacheWrite = { eff ->
            val mid = eff.machineId
            val snap = eff.snapshot
            if (mid.isNotEmpty() && eff.hasSnapshot() && snap.preset in USAGE_CACHE_PRESETS && cache != null) {
                enqueueCacheOp {
                    if (currentScheduleMember(mid)) {
                        cache?.putBlob(usageBlobKey(mid, snap.preset), snap.toByteArray())
                        if (!currentScheduleMember(mid)) cache?.removeBlob(usageBlobKey(mid, snap.preset))
                    } else {
                        removeUsageBlobs(mid)
                    }
                }
            }
        }
        coreHost.onHydrateUsageCache = { mid ->
            if (mid.isNotEmpty() && cache != null && usageHydratedMachines.add(mid)) {
                enqueueCacheOp {
                    if (!currentScheduleMember(mid)) {
                        removeUsageBlobs(mid)
                    } else {
                        for (preset in USAGE_CACHE_PRESETS) {
                            val bytes = runCatching { cache?.blob(usageBlobKey(mid, preset)) }.getOrNull() ?: continue
                            val snap = runCatching { UsageStatsResponse.parseFrom(bytes) }.getOrNull() ?: continue
                            coreHost.deliverUsageCacheLoaded(mid, snap)
                        }
                    }
                }
            }
        }
    }

    // --- Token Gate ---

    private suspend fun ensureTokenForCore(opId: Long, forceLogin: Boolean) {
        val b = TokenResultEvent.newBuilder().setOpId(opId)
        try {
            val tok = resolveLoginToken(forceLogin = forceLogin)
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
                } else {
                    cachedToken = null
                    tokenStorage?.clearLoginToken(account)
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
        withContext(authorityJob) {
            dispatchSyncedSecurityRecord(opId, networkGeneration)
        }

    private fun toSecurityRecordEvent(
        record: DeviceSecurity.Record,
        opId: Long = 0L,
        pushSeq: Long = 0L,
        networkGeneration: Long = this.networkGeneration,
        error: String = "",
    ): SecurityRecordEvent {
        refreshSecuritySets(record)
        latestXPrivate = runCatching { record.peerBoxKey() }.getOrNull()
        val head = record.head
        val headHash = DeviceSecurity.stateHash(head)
        val b = SecurityRecordEvent.newBuilder()
            .setOpId(opId)
            .setNetworkGeneration(networkGeneration)
            .setAccount(record.pin.account)
            .setDomain(ByteString.copyFrom(record.pin.domain))
            .setGenesis(ByteString.copyFrom(record.pin.genesis))
            .setFloorMs(record.floor.toLong())
            .setRecoveryPending(security.recoveryPending)
            .setRecoveryReadyMs(security.recoveryReadyMs)
            .setRecoveryObjectsPending(recoveryObjectsPending)
        if (pushSeq != 0L) b.setPushSeq(pushSeq)
        if (error.isNotEmpty()) b.setError(error)
        for (s in record.chain) {
            b.addStates(ByteString.copyFrom(DeviceSecurity.stateBytes(s)))
            val proof = record.proofs[s.core.epoch]
            b.addRecoveryProofs(if (proof == null) ByteString.EMPTY else ByteString.copyFrom(proof))
        }
        record.lease?.takeIf { it.head.contentEquals(headHash) }?.let {
            b.setLease(ByteString.copyFrom(it.bytes()))
        }
        val headContentEpoch = head.core.contentEpoch
        for ((ep, sec) in record.cachedKeys()) {
            if (ep > 0uL && ep < headContentEpoch && sec.size == KEY_SIZE) {
                b.addContentKeys(
                    SecurityContentKey.newBuilder()
                        .setContentEpoch(ep.toLong())
                        .setSecret(ByteString.copyFrom(sec)),
                )
            }
        }
        return b.build()
    }

    private fun dispatchLatestSecurityRecord(
        fallback: DeviceSecurity.Record? = null,
        onProcessed: (() -> Unit)? = null,
    ) {
        if (closed) {
            onProcessed?.invoke()
            return
        }
        ensureSecurityConfigured()
        synchronized(securitySnapshotLock) {
            if (closed) {
                onProcessed?.invoke()
                return
            }
            val pushSeq = securityPushSeq.getAndIncrement()
            val ev = try {
                val latest = runCatching { security.record() }.getOrElse { fallback ?: throw it }
                toSecurityRecordEvent(latest, opId = 0L, pushSeq = pushSeq, networkGeneration = networkGeneration)
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                report(e)
                SecurityRecordEvent.newBuilder()
                    .setOpId(0L)
                    .setPushSeq(pushSeq)
                    .setNetworkGeneration(networkGeneration)
                    .setError(e.message ?: e.javaClass.simpleName)
                    .build()
            }
            coreHost.dispatch(CoreEvent.newBuilder().setSecurityRecord(ev).build()) {
                securityRecordLoaded = true
                onProcessed?.invoke()
            }
        }
    }

    private fun pushSecurityRecordFromRecord(record: DeviceSecurity.Record, skipIfCoreSync: Boolean = true) {
        if (closed || (skipIfCoreSync && coreSyncDepth.get() > 0)) return
        dispatchLatestSecurityRecord(fallback = record)
    }

    private suspend fun ensureSecurityRecordLoaded() {
        ensureSecurityConfigured()
        if (securityRecordLoaded) return
        val rec = runCatching { security.record() }.getOrNull() ?: return
        val pushSeq = securityPushSeq.getAndIncrement()
        val ev = toSecurityRecordEvent(rec, opId = 0L, pushSeq = pushSeq, networkGeneration = networkGeneration)
        coreHost.dispatchSecurityRecordAndWait(ev)
        securityRecordLoaded = true
    }

    private suspend fun dispatchSyncedSecurityRecord(
        opId: Long,
        networkGeneration: Long,
    ) {
        ensureSecurityConfigured()
        val pushSeq = if (opId == 0L) securityPushSeq.getAndIncrement() else 0L
        val ev =
            try {
                coreSyncDepth.incrementAndGet()
                val record =
                    try {
                        security.sync().also { r ->
                            runCatching { resumeSecurityOperation(dispatchSnapshot = false) }.onFailure { if (it !is DeviceSecurity.Failure) throw it }
                            val preEv = toSecurityRecordEvent(
                                r,
                                opId = 0L,
                                pushSeq = securityPushSeq.getAndIncrement(),
                                networkGeneration = networkGeneration,
                            )
                            coreHost.dispatchSecurityRecordAndWait(preEv)
                            securityRecordLoaded = true
                            try {
                                recoveryObjectsPending = true
                                republishRecoveryObjects(r)
                                recoveryObjectsPending = false
                            } catch (e: DeviceSecurity.Failure) {
                                if (e.code != "lease") throw e
                            }
                        }
                    } finally {
                        coreSyncDepth.decrementAndGet()
                    }
                synchronized(securitySnapshotLock) {
                    if (closed) return
                    val latest = runCatching { security.record() }.getOrNull() ?: record
                    val built = toSecurityRecordEvent(latest, opId = opId, pushSeq = pushSeq, networkGeneration = networkGeneration)
                    sendEvent { setSecurityRecord(built) }
                    securityRecordLoaded = true
                }
                return
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                report(e)
                SecurityRecordEvent.newBuilder()
                    .setOpId(opId)
                    .setPushSeq(pushSeq)
                    .setNetworkGeneration(networkGeneration)
                    .setError(e.message ?: e.javaClass.simpleName)
                    .build()
            }
        sendEvent { setSecurityRecord(ev) }
    }

    private fun refreshSecuritySets(record: DeviceSecurity.Record) {
        val members = record.head.core.members.associateBy { it.id }
        trustedMembers.clear()
        trustedMembers.putAll(members)
        val arch = record.chain.flatMap { it.core.members }.filter { it.kind == 1u }.map { it.id }.toSet()
        archiveMachines.clear()
        archiveMachines.addAll(arch)
        arch.filter { it !in members }.forEach { scheduleSeenStore?.delete(it) }
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
                            if (deliveryBody != null) {
                                coreHost.dispatchDirectSignal(deliveryBody)
                            } else if (resultBody != null) {
                                val res = PeerFrameResult.parseFrom(resultBody)
                                coreHost.dispatchDirectSignalDelivery(
                                    res.frameId.toByteArray(),
                                    res.statusValue,
                                    res.retryAfterMs.toLong(),
                                )
                            }
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

        val socket = deviceLink.path.openStream(url, listener)
        legJob = scope.launch {
            var notifyClose = true
            try {
                for (raw in frames) {
                    ensureActive()
                    val frame = parseServerFrame(raw)
                    when (frame.kindCase) {
                        ServerFrame.KindCase.READY -> {
                            serverAhead = clock() - frame.ready.serverTimeMs > DeviceSecurity.MAX_CLOCK_SKEW.toLong()
                            directExecutor.attachLeg(socket)
                            sendEvent { setStreamReady(StreamReadyEvent.newBuilder().setLegId(legId).setServerTimeMs(frame.ready.serverTimeMs)) }
                        }
                        ServerFrame.KindCase.HEARTBEAT -> sendFrame { setHeartbeat(true) }
                        ServerFrame.KindCase.MACHINE_UPDATED -> {
                            sendFrame { setWireMachineUpdated(frame.machineUpdated.machine) }
                        }
                        ServerFrame.KindCase.MACHINE_REMOVED -> {
                            sendFrame { setMachineRemovedId(frame.machineRemoved.machineId) }
                        }
                        ServerFrame.KindCase.SESSION_UPDATED -> {
                            sendFrame { setWireSessionUpdated(frame.sessionUpdated.session) }
                        }
                        ServerFrame.KindCase.EVENTS_APPENDED -> {
                            val ea = frame.eventsAppended
                            sendFrame {
                                setEventsAppended(
                                    EventsAppendedFrame.newBuilder()
                                        .setSessionId(ea.sessionId)
                                        .addAllWireEvents(ea.eventsList),
                                )
                            }
                        }
                        ServerFrame.KindCase.ACCOUNT_PREFS_UPDATED -> {
                            val obj = frame.accountPrefsUpdated.`object`
                            if (obj.objectId == PROJECT_PREFS) {
                                sendFrame { setWireAccountPrefsUpdated(obj) }
                            }
                        }
                        ServerFrame.KindCase.DEVICE_UPDATED -> {
                            sendFrame { setWireDeviceUpdated(frame.deviceUpdated.device) }
                        }
                        ServerFrame.KindCase.DEVICE_REMOVED -> {
                            sendFrame { setDeviceRemovedId(frame.deviceRemoved.deviceId) }
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
                    directExecutor.detachLeg(socket)
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
        toClose.first?.let { directExecutor.detachLeg(it) }
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

    private fun handleDirectEvent(machineId: String, pcId: Long, env: Envelope, header: Header, @Suppress("UNUSED_PARAMETER") size: Int) {
        if (closed) return
        val sid = header.sessionId
        if (sid.isEmpty() || header.seq <= 0L) return
        if (sid !in watchedSessionIds) return
        if (sessionMachineId(sid) != machineId || header.sender != machineId) return
        coreHost.dispatch(
            CoreEvent.newBuilder()
                .setDirectEventsReceived(
                    DirectEventsReceivedEvent.newBuilder()
                        .setSessionId(sid)
                        .setMachineId(machineId)
                        .setPcId(pcId)
                        .setSender(machineId)
                        .addWireEnvelopes(env),
                )
                .build(),
        )
    }

    // --- Storage Executor ---

    private suspend fun executeCacheRead(eff: CacheReadEffect) {
        when (eff.kindCase) {
            CacheReadEffect.KindCase.BOOTSTRAP -> {
                val b = CacheBootstrapLoadedEvent.newBuilder().setOpId(eff.bootstrap.opId)
                try {
                    runCatching { security.record() }.getOrNull()?.let { record ->
                        refreshSecuritySets(record)
                        archiveMachines.filter { trustedMembers[it]?.kind != 1u }.forEach {
                            cache?.removeBlob("schedule:$it")
                            removeUsageBlobs(it)
                        }
                        ensureSecurityConfigured()
                        val secEv = toSecurityRecordEvent(
                            record,
                            opId = 0L,
                            pushSeq = securityPushSeq.getAndIncrement(),
                            networkGeneration = networkGeneration,
                        )
                        coreHost.dispatchSecurityRecordAndWait(secEv)
                        securityRecordLoaded = true
                    }
                    b.addAllWireMachines(cache?.machines().orEmpty())
                        .addAllWireSessions(cache?.sessions().orEmpty())
                    cache?.blob(PROJECT_PREFS)?.let { b.setWirePrefs(AccountPrefsObject.parseFrom(it)) }
                    homeScopeStore?.readToken()?.takeIf { it.isNotEmpty() }?.let { b.setHomeScopeToken(it) }
                    Keys.deriveKey(account.localKey, "home-scope").takeIf { it.isNotEmpty() }?.let { b.setScopeHmacKey(ByteString.copyFrom(it)) }
                } catch (e: Exception) {
                    if (e is CancellationException) throw e
                    report(e)
                    b.setError(e.message ?: e.javaClass.simpleName)
                }
                try {
                    coreHost.dispatchAndWait(CoreEvent.newBuilder().setCacheBootstrapLoaded(b).build())
                } finally {
                    _bootstrapped.value = true
                }
            }
            CacheReadEffect.KindCase.EVENTS -> {
                val req = eff.events
                val sid = req.sessionId
                val b = CacheEventsLoadedEvent.newBuilder().setOpId(req.opId).setSessionId(sid)
                try {
                    val raw = if (req.newest) cache?.newestEvents(sid).orEmpty() else cache?.eventPage(sid, req.afterSeq).orEmpty()
                    val limited = if (req.limit > 0 && raw.size > req.limit) raw.take(req.limit) else raw
                    b.addAllWireEvents(limited)
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

    private fun extractCacheWrite(eff: CacheWriteEffect): CacheWritePayload? {
        if (cache == null) return null
        return when {
            eff.hasWriteMachine() -> CacheWritePayload.MachineWrite(eff.writeMachine)
            eff.hasWriteSession() -> CacheWritePayload.SessionWrite(eff.writeSession)
            eff.hasKeepSessionOwner() -> CacheWritePayload.KeepOwnerWrite(eff.keepSessionOwner)
            eff.hasWriteStoredEvents() -> {
                val we = eff.writeStoredEvents
                if (we.eventsList.isNotEmpty()) CacheWritePayload.EventsWrite(we.sessionId, we.eventsList) else null
            }
            eff.hasWritePrefs() -> CacheWritePayload.PrefsWrite(eff.writePrefs)
            eff.hasSetSessionOpen() -> CacheWritePayload.SessionOpenWrite(eff.setSessionOpen.sessionId, eff.setSessionOpen.open)
            else -> null
        }
    }

    private suspend fun writeCachePayload(payload: CacheWritePayload) {
        when (payload) {
            is CacheWritePayload.MachineWrite -> cache?.putMachine(payload.m)
            is CacheWritePayload.SessionWrite -> cache?.putSession(payload.s)
            is CacheWritePayload.KeepOwnerWrite -> cache?.keepOwner(payload.s)
            is CacheWritePayload.EventsWrite -> cache?.putEvents(payload.sessionId, payload.events)
            is CacheWritePayload.PrefsWrite -> cache?.putBlob(PROJECT_PREFS, payload.p.toByteArray())
            is CacheWritePayload.SessionOpenWrite -> cache?.setSessionOpen(payload.sessionId, payload.open)
        }
    }

    private suspend fun executeCacheDelete(eff: CacheDeleteEffect) {
        try {
            when (eff.kindCase) {
                CacheDeleteEffect.KindCase.DROP_EVENTS_SESSION_ID ->
                    cache?.dropEvents(eff.dropEventsSessionId)
                CacheDeleteEffect.KindCase.REMOVE_SESSION_ID -> {
                    val sid = eff.removeSessionId
                    cache?.removeSession(sid)
                    runCatching { onSessionRemoved(sid) }
                }
                CacheDeleteEffect.KindCase.UNLIST_SESSION_ID ->
                    cache?.unlistSession(eff.unlistSessionId)
                CacheDeleteEffect.KindCase.REMOVE_MACHINE_ID -> {
                    val mid = eff.removeMachineId
                    cache?.removeBlob("schedule:$mid")
                    removeUsageBlobs(mid)
                    cache?.removeMachine(mid)
                }
                else -> {}
            }
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            report(e)
        }
    }

    // --- RPC Executor ---

    private inline fun sendEvent(
        block: CoreEvent.Builder.() -> Unit,
    ) = coreHost.dispatch(CoreEvent.newBuilder().apply(block).build())

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
                    addAllWireMachines(machinesRes.machinesList)
                    addAllWireSessions(sessionsRes.sessionsList)
                    if (sessionsRes.hasNext()) setNext(sessionsRes.next)
                    addAllWireDevices(devicesRes.devicesList)
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
                addAllWireSessions(res.sessionsList)
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
                        b.setWireSession(found)
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
                setMore(res.more).addAllWireEvents(res.eventsList)
            }
            RpcEffect.KindCase.GET_ACCOUNT_PREFS -> runRpc(
                make = { AccountPrefsFetchedEvent.newBuilder().setOpId(opId) },
                onError = { err, _ -> setError(err) },
                emit = { setAccountPrefsFetched(it) },
            ) {
                val objectId = eff.getAccountPrefs.objectId.ifEmpty { PROJECT_PREFS }
                val obj = api.client.getAccountPrefs(GetAccountPrefsRequest.newBuilder().setObjectId(objectId).build(), Api.headers(currentToken())).value().`object`
                setWirePrefs(obj)
            }
            RpcEffect.KindCase.PUT_ACCOUNT_PREFS -> {
                val putRpc = eff.putAccountPrefs
                val objectId = putRpc.objectId.ifEmpty { PROJECT_PREFS }
                val b = AccountPrefsPutResultEvent.newBuilder().setOpId(opId)
                try {
                    val env = if (putRpc.hasSealedEnvelope()) {
                        putRpc.sealedEnvelope
                    } else {
                        sealEnvelopeOp(
                            DeviceSecurity.Purpose.PREFS,
                            request = objectId,
                            seq = (putRpc.expectedRevision + 1).toULong(),
                            plain = putRpc.nextPrefs.toByteArray(),
                        ).envelope
                    }
                    val res = api.client.putAccountPrefs(
                        PutAccountPrefsRequest.newBuilder().setObjectId(objectId).setExpectedRevision(putRpc.expectedRevision).setEnvelope(env).build(),
                        Api.headers(currentToken()),
                    ).value()
                    b.setApplied(res.applied)
                    if (res.hasObject()) b.setWireCurrent(res.`object`)
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
                addAllWireDevices(ds.devicesList)
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

    private class AttemptOutcome(
        val outcome: CommandOutcomeEvent,
        val trusted: Boolean,
        val elapsedMs: Double,
        val observedBytes: Long,
        val ok: Boolean,
        val msg: CommandResult? = null,
        val connectError: ConnectException? = null,
        val refusal: PathRefusal? = null,
        val otherError: Exception? = null,
        val handedOff: Boolean = false,
    )

    private suspend fun executeCommandLeg(
        machineId: String,
        sessionId: String,
        requestId: String,
        attemptId: String,
        parsedCmd: Command?,
        authorized: Envelope,
        binding: Pair<ByteArray, ByteArray>?,
        prior: Boolean,
        bulk: Boolean,
        useDirect: Boolean,
        directPcId: Long,
        directTimeoutMs: Long,
    ): AttemptOutcome {
        var handedOff = false
        val handoff: () -> Unit = { handedOff = true }
        val relayAttempt = if (useDirect) null else deviceLink.path.attempt(attemptId, handoff)
        val startedNs = System.nanoTime()
        fun elapsedMs(): Double = maxOf((System.nanoTime() - startedNs) / 1_000_000.0, 0.01)
        fun baseOutcome(source: OutcomeSource): CommandOutcomeEvent.Builder =
            CommandOutcomeEvent.newBuilder()
                .setRequestId(requestId)
                .setAttemptId(attemptId)
                .setSource(source)
                .setRouteWasDirect(useDirect)

        return try {
            val res = if (useDirect) {
                val env = directExecutor.sendRpc(directPcId, requestId, authorized.toByteArray(), directTimeoutMs, handoff)
                ForwardResponse.newBuilder().setResult(env).setAttemptId(attemptId).build()
            } else {
                relayAttempt!!.prepare(prior)
                relayAttempt.send(machineId, authorized)
            }
            val dtMs = elapsedMs()
            val attemptBound = res.attemptId == attemptId
            if (binding == null) {
                if (
                    cache != null &&
                    parsedCmd?.hasSchedule() == true &&
                    parsedCmd.schedule.operation == "list" &&
                    attemptBound &&
                    (useDirect || res.outcome == "executed") &&
                    currentScheduleMember(machineId)
                ) {
                    val msg = runCatching {
                        openSecureMessage(
                            res.result,
                            Place(PayloadKind.PAYLOAD_KIND_COMMAND_RESULT, machineId = machineId, sessionId = sessionId, requestId = requestId),
                            CommandResult::parseFrom,
                            bulk = bulk,
                        ).second
                    }.getOrNull()
                    if (msg != null && msg.hasSchedule() && msg.errorCode.isEmpty() && msg.outcome == "executed") {
                        cache.putBlob("schedule:$machineId", scheduleSnapshot(requestId, res.result))
                        if (!currentScheduleMember(machineId)) cache.removeBlob("schedule:$machineId")
                    }
                }
                return AttemptOutcome(
                    outcome = baseOutcome(OutcomeSource.OUTCOME_SOURCE_DAEMON)
                        .setAttemptBound(attemptBound)
                        .apply { if (res.hasResult()) setSealedResult(res.result) }
                        .setOuterOutcome(res.outcome)
                        .setOuterReason(res.reason)
                        .setHandedOff(handedOff)
                        .setElapsedMs(dtMs)
                        .build(),
                    trusted = false,
                    elapsedMs = dtMs,
                    observedBytes = 0L,
                    ok = false,
                    handedOff = handedOff,
                )
            }
            val msg = try {
                openSecureMessage(
                    res.result,
                    Place(PayloadKind.PAYLOAD_KIND_COMMAND_RESULT, machineId = machineId, sessionId = sessionId, requestId = requestId),
                    CommandResult::parseFrom,
                    bulk = bulk,
                ).second
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {
                return AttemptOutcome(
                    outcome = baseOutcome(OutcomeSource.OUTCOME_SOURCE_DAEMON)
                        .setAttemptBound(attemptBound)
                        .setSigned(false)
                        .setHashesMatch(false)
                        .setHandedOff(handedOff)
                        .setElapsedMs(dtMs)
                        .build(),
                    trusted = false,
                    elapsedMs = dtMs,
                    observedBytes = 0L,
                    ok = false,
                    handedOff = handedOff,
                )
            }
            val hashesMatch = msg.requestKeyHash.toByteArray().contentEquals(binding.first) &&
                msg.payloadHash.toByteArray().contentEquals(binding.second) &&
                (msg.outcome == "conflict" || msg.conflictKind.isEmpty())
            val outerMatchesInner = useDirect || (res.outcome == msg.outcome && res.reason == msg.reason)
            val outerOutcome = if (useDirect) msg.outcome else res.outcome
            val outerReason = if (useDirect) msg.reason else res.reason
            val obsBytes = when {
                parsedCmd?.hasPutImageChunk() == true -> parsedCmd.putImageChunk.data.size().toLong()
                msg.hasGetSharedFileChunk() -> msg.getSharedFileChunk.data.size().toLong()
                else -> 0L
            }
            val category = verifiedOutcome(msg, binding.first, binding.second, outerOutcome, outerReason)
            val trusted = hashesMatch && attemptBound && outerMatchesInner && category != Outcome.UNKNOWN
            val ok = trusted && msg.errorCode.isEmpty() && category == Outcome.EXECUTED
            if (ok &&
                parsedCmd?.hasSchedule() == true &&
                parsedCmd.schedule.operation == "list" &&
                msg.hasSchedule() &&
                currentScheduleMember(machineId)
            ) {
                cache?.putBlob("schedule:$machineId", scheduleSnapshot(requestId, res.result))
                if (!currentScheduleMember(machineId)) cache?.removeBlob("schedule:$machineId")
            }
            AttemptOutcome(
                outcome = baseOutcome(OutcomeSource.OUTCOME_SOURCE_DAEMON)
                    .setAttemptBound(attemptBound)
                    .setSigned(true)
                    .setHashesMatch(hashesMatch)
                    .setOuterMatchesInner(outerMatchesInner)
                    .setOuterOutcome(outerOutcome)
                    .setOuterReason(outerReason)
                    .setResult(msg)
                    .setHandedOff(handedOff)
                    .setElapsedMs(dtMs)
                    .setObservedBytes(obsBytes)
                    .build(),
                trusted = trusted,
                elapsedMs = dtMs,
                observedBytes = obsBytes,
                ok = ok,
                msg = msg,
                handedOff = handedOff,
            )
        } catch (e: CancellationException) {
            throw e
        } catch (_: PathChanged) {
            val dtMs = elapsedMs()
            AttemptOutcome(
                outcome = baseOutcome(if (handedOff) OutcomeSource.OUTCOME_SOURCE_TRANSPORT else OutcomeSource.OUTCOME_SOURCE_LOCAL)
                    .setNotForwarded(!handedOff)
                    .setHandedOff(handedOff)
                    .setTransportCode("path_changed")
                    .setElapsedMs(dtMs)
                    .build(),
                trusted = false,
                elapsedMs = dtMs,
                observedBytes = 0L,
                ok = false,
                otherError = PathChanged(),
                handedOff = handedOff,
            )
        } catch (ce: ConnectException) {
            val dtMs = elapsedMs()
            val refusal = relayAttempt?.refusal(ce)
            val ev = if (refusal != null) {
                baseOutcome(OutcomeSource.OUTCOME_SOURCE_SERVER).setNotForwarded(true).setAttemptBound(true)
                    .setOuterReason(refusal.reason).setTransportCode(mapConnectCode(ce.code))
                    .setTransportDetail(ce.message ?: "").setElapsedMs(dtMs).build()
            } else {
                baseOutcome(OutcomeSource.OUTCOME_SOURCE_TRANSPORT).setNotForwarded(!handedOff).setHandedOff(handedOff)
                    .setTransportCode(if (unresolvedHost(ce)) "unresolved_host" else mapConnectCode(ce.code))
                    .setTransportDetail(ce.message ?: "").setElapsedMs(dtMs).build()
            }
            AttemptOutcome(
                outcome = ev,
                trusted = false,
                elapsedMs = dtMs,
                observedBytes = 0L,
                ok = false,
                connectError = ce,
                refusal = refusal,
                handedOff = handedOff,
            )
        } catch (e: IOException) {
            val dtMs = elapsedMs()
            AttemptOutcome(
                outcome = baseOutcome(OutcomeSource.OUTCOME_SOURCE_TRANSPORT).setNotForwarded(!handedOff).setHandedOff(handedOff)
                    .setTransportCode(if (unresolvedHost(e)) "unresolved_host" else "network")
                    .setTransportDetail(e.message ?: e.javaClass.simpleName).setElapsedMs(dtMs).build(),
                trusted = false,
                elapsedMs = dtMs,
                observedBytes = 0L,
                ok = false,
                otherError = e,
                handedOff = handedOff,
            )
        } catch (e: Exception) {
            val dtMs = elapsedMs()
            AttemptOutcome(
                outcome = baseOutcome(if (handedOff) OutcomeSource.OUTCOME_SOURCE_TRANSPORT else OutcomeSource.OUTCOME_SOURCE_LOCAL)
                    .setNotForwarded(!handedOff)
                    .setHandedOff(handedOff)
                    .setTransportCode("failed_precondition")
                    .setTransportDetail(e.message ?: e.javaClass.simpleName)
                    .setElapsedMs(dtMs)
                    .build(),
                trusted = false,
                elapsedMs = dtMs,
                observedBytes = 0L,
                ok = false,
                otherError = e,
                handedOff = handedOff,
            )
        }
    }

    internal suspend fun dispatchSingleCommand(
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
        val bulk = eff.bulkUpload || eff.routeHint == CommandRouteHint.COMMAND_ROUTE_HINT_BULK

        if (serverAhead || security.clockSkew) return localFailure("failed_precondition", "clock skew")
        if (!eff.hasSealedEnvelope()) return localFailure("failed_precondition", "missing sealed command")

        val useDirect = eff.routeLeg == CommandRouteLeg.COMMAND_ROUTE_LEG_DIRECT && eff.directPcId > 0L
        return executeCommandLeg(
            machineId = eff.machineId,
            sessionId = eff.sessionId,
            requestId = eff.requestId,
            attemptId = eff.attemptId,
            parsedCmd = parsedCmd,
            authorized = eff.sealedEnvelope,
            binding = null,
            prior = eff.retryOfUnknown,
            bulk = bulk,
            useDirect = useDirect,
            directPcId = eff.directPcId,
            directTimeoutMs = eff.directTimeoutMs,
        ).outcome
    }

    private fun mapConnectCode(code: Code): String = when (code) {
        Code.UNAUTHENTICATED -> "unauthenticated"
        Code.FAILED_PRECONDITION -> "failed_precondition"
        Code.UNAVAILABLE, Code.DEADLINE_EXCEEDED -> "unavailable"
        else -> code.name.lowercase()
    }

    // --- Lifecycle ---

    fun start() {
        running = true
        if (!authorityJob.isActive) authorityJob = SupervisorJob(scope.coroutineContext[Job])
        if (rtcFactory != null) {
            scope.launch(Dispatchers.Default) { runCatching { rtcFactory.prewarm() } }
        }
        coreHost.sendTransportConfig(
            supported = rtcFactory != null,
            account = account.accountId,
            protocolVersion = PEER_PROTOCOL_VERSION,
            serverHost = directServerHost,
            configuredStun = directConfiguredStun,
            platformAgent = "android",
        )
        coreHost.sendDirectPreferencesLoaded(
            runCatching { directPrefs.allEnabled(account.server, account.deviceId) }.getOrDefault(emptyMap()),
        )
        ensureSecurityConfigured()
        runCatching { security.record() }.getOrNull()?.let {
            pushSecurityRecordFromRecord(it, skipIfCoreSync = false)
        }
        val token = homeScopeStore?.readToken().orEmpty()
        val hmacKey = Keys.deriveKey(account.localKey, "home-scope")
        coreHost.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_START, token, hmacKey)
        renewLeaseIfNeeded()
    }

    fun stop() {
        running = false
        authorityJob.cancel()
        directExecutor.detachAll()
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
            security.networkChanged()
        }
        coreHost.networkChanged(available, signature)
    }

    suspend fun retryDirect(machineId: String): Boolean = coreHost.retryDirectNow(machineId)

    fun refreshDirectDiagnostics(machineId: String) = coreHost.refreshDirectDiagnostics(machineId)

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
        closed = true
        running = false
        cleanup(
            { directExecutor.close() },
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
                            synchronized(pendingCacheOps) {
                                pendingCacheOps.clear()
                                cacheWriterActive = false
                            }
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
        modelLists: Map<Pair<String, Agent>, ListModelsResponse> = emptyMap(),
        sessions: Map<String, SessionViewModel> = emptyMap(),
    ) {
        check(!running) { "showForTest on a started Remote" }
        _bootstrapped.value = true
        relayObservation = state
        testStateOverride = state
        _testStateFlow.value = state
        _state.value = state
        for ((key, response) in modelLists) {
            models["${key.first}/${key.second.number}"] = CompletableDeferred(response)
        }
        coreHost.dispatchOnWorker(
            CoreEvent.newBuilder()
                .setTransportConfig(
                    TransportConfigEvent.newBuilder()
                        .setSupported(rtcFactory != null)
                        .setAccount(account.accountId)
                        .setProtocolVersion(PEER_PROTOCOL_VERSION)
                        .setServerHost(directServerHost)
                        .setConfiguredStun(directConfiguredStun)
                        .setPlatformAgent("android"),
                )
                .build(),
        )
        coreHost.dispatchOnWorker(
            CoreEvent.newBuilder()
                .setDirectPreferencesLoaded(
                    DirectPreferencesLoadedEvent.newBuilder().apply {
                        for ((id, en) in runCatching { directPrefs.allEnabled(account.server, account.deviceId) }.getOrDefault(emptyMap())) {
                            addEntries(DirectPreferenceEntry.newBuilder().setMachineId(id).setEnabled(en))
                        }
                    },
                )
                .build(),
        )
        coreHost.showViewsForTest(sessions = sessions)
        seedCoreForTest(coreHost, account.deviceId, state, modelLists)
    }

    // --- Security Management & Recovery (preserved for Security screens & DeviceSecurity tests) ---

    suspend fun syncSecurity(budgetMs: Long = Long.MAX_VALUE) {
        ensureSecurityConfigured()
        try {
            val r = withContext(dispatcher) {
                coreSyncDepth.incrementAndGet()
                try {
                    security.sync(budgetMs).also { synced ->
                        resumeSecurityOperation(dispatchSnapshot = false)
                        val preEv = toSecurityRecordEvent(
                            synced,
                            opId = 0L,
                            pushSeq = securityPushSeq.getAndIncrement(),
                            networkGeneration = networkGeneration,
                        )
                        coreHost.dispatchSecurityRecordAndWait(preEv)
                        securityRecordLoaded = true
                        try {
                            recoveryObjectsPending = true
                            republishRecoveryObjects(synced)
                            recoveryObjectsPending = false
                        } catch (e: DeviceSecurity.Failure) {
                            if (e.code != "lease") throw e
                        }
                    }
                } finally {
                    coreSyncDepth.decrementAndGet()
                }
            }
            val prunedArchive =
                synchronized(securitySnapshotLock) {
                    val latest = runCatching { security.record() }.getOrNull() ?: r
                    refreshSecuritySets(latest)
                    archiveMachines.filter { trustedMembers[it]?.kind != 1u }
                }
            prunedArchive.forEach {
                cache?.removeBlob("schedule:$it")
                removeUsageBlobs(it)
            }
        } catch (e: DeviceSecurity.Failure) {
            if (e.code != "clock") throw e
        }
        val done = CompletableDeferred<Unit>()
        dispatchLatestSecurityRecord { done.complete(Unit) }
        done.await()
    }

    suspend fun refreshDevices() {
        val pushSeq = devicesPushSeq.getAndIncrement()
        val ds = api.client.listDevices(ListDevicesRequest.getDefaultInstance(), Api.headers(currentToken())).value()
        val ev = DevicesFetchedEvent.newBuilder().setOpId(0L).setPushSeq(pushSeq).addAllWireDevices(ds.devicesList).build()
        coreHost.dispatchAndWait(CoreEvent.newBuilder().setDevicesFetched(ev).build())
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
            val (opened, snapshot) = try {
                openSecureMessageWithMeta(
                    env,
                    Place(purpose.payloadKind(), requestId = request, seq = seq),
                    parse,
                    live = false,
                )
            } catch (e: DeviceSecurity.Failure) {
                if (e.code == "unavailable") return true else throw e
            }
            if (purpose == DeviceSecurity.Purpose.DEVICE_META && opened.sender != account.deviceId) {
                throw DeviceSecurity.Failure("untrusted")
            }
            if (opened.contentEpoch.toULong() >= boundary) return true
            return upload(
                sealEnvelopeOp(
                    purpose,
                    request = request,
                    seq = (seq + 1).toULong(),
                    plain = snapshot.toByteArray(),
                ).envelope,
            )
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

    suspend fun resumeSecurityOperation(dispatchSnapshot: Boolean = true) =
        withContext(authorityJob) {
            securityOperationLock.withLock {
                resumeSecurityOperationLocked(dispatchSnapshot)
            }
        }

    private suspend fun resumeSecurityOperationLocked(dispatchSnapshot: Boolean) {
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
            authorityForeground()
            api.client.putSecurityState(req, Api.headers(currentToken())).value()
            security.install(emptyList(), lease = cert)
            storage.clearSecurityOperation()
            if (dispatchSnapshot) dispatchLatestSecurityRecord()
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
                authorityForeground()
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
            authorityForeground()
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
            authorityForeground()
            api.pairing.approvePairing(delivery, Api.headers(currentToken())).value()
            val status = api.pairing.getPairing(
                GetPairingRequest.newBuilder().setPairingId(delivery.pairingId).build(),
                Api.headers(currentToken()),
            ).value()
            if (status.state != PairingState.PAIRING_STATE_DELIVERED) return
        }
        storage.writeSecurityOperation(op.advanced(SecurityOperation.Phase.DELIVERED))
        storage.clearSecurityOperation()
        if (dispatchSnapshot) dispatchLatestSecurityRecord()
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

    suspend fun finishPairingOperation(pairingId: String) {
        val cleared = securityOperationLock.withLock {
            val storage = securityStorage ?: throw DeviceSecurity.Failure("storage")
            val op = storage.readSecurityOperation() ?: return@withLock null
            if (op.kind == "state" && op.delivery.isNotEmpty() && ApprovePairingRequest.parseFrom(op.delivery).pairingId == pairingId) {
                storage.writeSecurityOperation(op.advanced(SecurityOperation.Phase.DELIVERED))
                storage.clearSecurityOperation()
                security.record()
            } else null
        }
        if (cleared != null) pushSecurityRecordFromRecord(cleared, skipIfCoreSync = false)
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

    private fun renewLease(force: Boolean): Job =
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
                val env = sealEnvelopeOp(
                    DeviceSecurity.Purpose.DEVICE_META,
                    request = id,
                    seq = (current.metaRev + 1).toULong(),
                    plain = next.toByteArray(),
                ).envelope
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

    // --- Infrastructure Machine Commands (uploads, downloads, terminal, settings, schedules) ---

    suspend fun getMachineStatus(machineId: String): io.github.lewin671.remoteagent.proto.MachineStatus {
        val result = machineCommand(machineId, Command.newBuilder().setGetMachineStatus(io.github.lewin671.remoteagent.proto.GetMachineStatusRequest.getDefaultInstance()).build())
        check(result.hasMachineStatus()) { UiMessages.machineStatusUnavailable().messageText() }
        return result.machineStatus
    }

    private fun commandSessionId(cmd: Command): String = when (cmd.kindCase) {
        Command.KindCase.SEND -> cmd.send.sessionId
        Command.KindCase.INTERRUPT -> cmd.interrupt.sessionId
        Command.KindCase.RESOLVE_APPROVAL -> cmd.resolveApproval.sessionId
        Command.KindCase.ANSWER_QUESTION -> cmd.answerQuestion.sessionId
        Command.KindCase.STOP_SESSION -> cmd.stopSession.sessionId
        Command.KindCase.CANCEL_INPUT -> cmd.cancelInput.sessionId
        Command.KindCase.SET_MODEL -> cmd.setModel.sessionId
        Command.KindCase.SET_APPROVAL_MODE -> cmd.setApprovalMode.sessionId
        Command.KindCase.COMPACT -> cmd.compact.sessionId
        Command.KindCase.ANSWER_PLAN -> cmd.answerPlan.sessionId
        Command.KindCase.OPEN_TERMINAL -> cmd.openTerminal.sessionId
        Command.KindCase.RESUME_SESSION -> cmd.resumeSession.sessionId
        Command.KindCase.RESUME_INPUT -> cmd.resumeInput.sessionId
        Command.KindCase.RENAME_SESSION -> cmd.renameSession.sessionId
        Command.KindCase.FORK_SESSION -> cmd.forkSession.sessionId
        Command.KindCase.GET_LIVE_SNAPSHOT -> cmd.getLiveSnapshot.sessionId
        Command.KindCase.GET_SHARED_FILE_CHUNK -> cmd.getSharedFileChunk.sessionId
        else -> ""
    }

    suspend fun machineCommand(machineId: String, cmd: Command): CommandResult =
        sendMachineCommand(machineId, cmd, sessionId = commandSessionId(cmd))

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

    fun requireFileSupport(machineId: String?, hasFiles: Boolean, mayHaveRun: Boolean = false) {
        if (hasFiles && !filesSupported(state.value.machines[machineId])) {
            throw CommandException(
                if (mayHaveRun) CommandException.UNKNOWN else "failed_precondition",
                if (mayHaveRun) localized { copy -> copy(UiMessages.errorsAndroidValue1ThePreviousOutcomeIsStillUnknownRetryTheSame(FILE_UPLOAD_UPDATE_REQUIRED.resolve(copy))) } else FILE_UPLOAD_UPDATE_REQUIRED,
                outcome = if (mayHaveRun) Outcome.UNKNOWN else Outcome.NOT_SENT,
            )
        }
    }

    private suspend fun sendMachineCommand(
        machineId: String,
        cmd: Command,
        sessionId: String = "",
        requestId: String = newRequestId(),
        deadlineMs: Long = clock() + COMMAND_TTL_MS,
    ): CommandResult = withContext(dispatcher) {
        val versioned = cmd.toBuilder().setProtocolVersion(PEER_PROTOCOL_VERSION).build()
        val hasFiles = when (versioned.kindCase) {
            Command.KindCase.SEND -> versioned.send.fileIdsCount > 0
            Command.KindCase.CREATE_SESSION -> versioned.createSession.fileIdsCount > 0
            else -> false
        }
        var uncertain = false
        requireFileSupport(machineId, hasFiles, uncertain)
        protocolMismatch(state.value.machines[machineId])?.let {
            throw CommandException("failed_precondition", it, outcome = Outcome.NOT_SENT)
        }
        if (clock() >= deadlineMs) {
            throw CommandException(
                CommandException.EXPIRED,
                UiMessages.errorsAndroidThisCommandExpiredAndWasNotSent().messageText(),
                outcome = Outcome.NOT_SENT,
            )
        }
        if (serverAhead || security.clockSkew) {
            throw CommandException(
                "failed_precondition",
                UiMessages.errorsAndroidThePhoneClockIsAheadOfTheServerRemoteControl().messageText(),
            )
        }
        val bulk = versioned.hasPutImageChunk() || versioned.hasGetSharedFileChunk()
        val routeHintValue = when {
            versioned.hasGetSharedFileChunk() || versioned.hasPutImageChunk() ->
                CommandRouteHint.COMMAND_ROUTE_HINT_BULK_VALUE
            versioned.isReadOnlyCommand() && !versioned.hasGetImageUploadStatus() ->
                CommandRouteHint.COMMAND_ROUTE_HINT_READ_VALUE
            else -> CommandRouteHint.COMMAND_ROUTE_HINT_UNSPECIFIED_VALUE
        }
        val bulkFileKey = when {
            versioned.hasGetSharedFileChunk() -> "$sessionId/${versioned.getSharedFileChunk.fileId}"
            versioned.hasPutImageChunk() -> versioned.putImageChunk.transferId
            else -> ""
        }
        val bulkBytes = if (versioned.hasPutImageChunk()) versioned.putImageChunk.data.size().toLong() else 0L
        val bulkUpload = versioned.hasPutImageChunk()
        val uploadTransferId = if (versioned.hasBeginImageUpload()) versioned.beginImageUpload.transferId else ""
        val uploadHasFilename = versioned.hasBeginImageUpload() && versioned.beginImageUpload.fileName.isNotEmpty()
        val finishUploadTransferId = when {
            versioned.hasCompleteImageUpload() -> versioned.completeImageUpload.transferId
            versioned.hasAbortImageUpload() -> versioned.abortImageUpload.transferId
            else -> ""
        }

        val (authorized, binding) = try {
            val sealed = sealEnvelopeOp(
                DeviceSecurity.Purpose.COMMAND,
                machineId = machineId,
                sessionId = sessionId,
                request = requestId,
                deadline = deadlineMs.toULong(),
                plain = versioned.toByteArray(),
            )
            sealed.envelope to (sealed.requestKeyHash.toByteArray() to sealed.payloadHash.toByteArray())
        } catch (e: DeviceSecurity.Failure) {
            when (e.code) {
                "lease" -> throw CommandException(
                    "failed_precondition",
                    localized { copy -> copy(UiMessages.errorsAndroidRemoteControlPausedValue1AndRetry(state.value.renewalAdvice.resolve(copy))) },
                )
                "clock" -> throw CommandException(
                    "failed_precondition",
                    UiMessages.errorsAndroidThePhoneClockIsAheadOfTheServerRemoteControl().messageText(),
                )
                else -> throw CommandException(
                    "authorization_unavailable",
                    UiMessages.errorsAndroidSecurityAuthorizationOrTheCurrentRequestIsUnverifiedNothingWas().messageText(),
                )
            }
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            throw CommandException(
                "authorization_unavailable",
                UiMessages.errorsAndroidSecurityAuthorizationOrTheCurrentRequestIsUnverifiedNothingWas().messageText(),
            )
        }

        var forceRelay = false
        var attempt = 0
        while (true) {
            requireFileSupport(machineId, hasFiles, uncertain)
            val attemptId = newRequestId()
            val dec = if (routeHintValue != 0 || uploadTransferId.isNotEmpty()) {
                coreHost.beginDirectBulkTransfer(
                    machineId = machineId,
                    routeHint = routeHintValue,
                    bulkFileKey = bulkFileKey,
                    bulkBytes = bulkBytes,
                    bulkUpload = bulkUpload,
                    deadlineMs = deadlineMs,
                    forceRelay = forceRelay,
                    uploadTransferId = uploadTransferId,
                    uploadHasFilename = uploadHasFilename,
                )
            } else {
                DirectBulkRouteEffect.getDefaultInstance()
            }
            val useDirect = dec.useDirect && !forceRelay && dec.pcId > 0L
            val res = try {
                executeCommandLeg(
                    machineId = machineId,
                    sessionId = sessionId,
                    requestId = requestId,
                    attemptId = attemptId,
                    parsedCmd = versioned,
                    authorized = authorized,
                    binding = binding,
                    prior = uncertain,
                    bulk = bulk,
                    useDirect = useDirect,
                    directPcId = dec.pcId,
                    directTimeoutMs = dec.timeoutMs,
                )
            } finally {
                // Report completion asynchronously to Core Coordinator without blocking caller.
            }
            coreHost.finishDirectBulkTransfer(
                ticketId = dec.ticketId,
                routeWasDirect = useDirect,
                ok = res.ok,
                elapsedMs = res.elapsedMs,
                observedBytes = res.observedBytes,
                finishUploadTransferId = finishUploadTransferId,
                machineId = machineId,
            )
            if (res.handedOff) uncertain = true

            val ev = res.outcome
            val msg = res.msg
            if (ev.source == OutcomeSource.OUTCOME_SOURCE_DAEMON) {
                if (!ev.signed || !ev.hashesMatch || !ev.attemptBound || !ev.outerMatchesInner || msg == null) {
                    throw CommandException(
                        CommandException.UNKNOWN,
                        UiMessages.errorsAndroidCannotOpenTheComputerSReplyCommandOutcomeUnknown().messageText(),
                        failureKind = if (!ev.signed) CommandFailureKind.REPLY_UNVERIFIED else CommandFailureKind.RAW,
                        fromDaemon = true,
                        outcome = Outcome.UNKNOWN,
                    )
                }
                val category = verifiedOutcome(msg, binding.first, binding.second, ev.outerOutcome, ev.outerReason)
                if (category == Outcome.UNKNOWN) {
                    throw CommandException(
                        CommandException.UNKNOWN,
                        UiMessages.errorsAndroidCannotOpenTheComputerSReplyCommandOutcomeUnknown().messageText(),
                        fromDaemon = true,
                        outcome = Outcome.UNKNOWN,
                    )
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
                        readOnly = versioned.isReadOnlyCommand(),
                        outcome = category,
                        conflictKind = msg.conflictKind,
                        resultAvailable = msg.resultAvailable,
                    )
                }
                return@withContext msg
            }

            if (useDirect && routeHintValue != 0 && !forceRelay) {
                forceRelay = true
                if (versioned.hasPutImageChunk()) {
                    machineCommand(
                        machineId,
                        Command.newBuilder()
                            .setGetImageUploadStatus(
                                io.github.lewin671.remoteagent.proto.GetImageUploadStatusRequest.newBuilder()
                                    .setTransferId(versioned.putImageChunk.transferId)
                                    .build(),
                            )
                            .build(),
                    )
                }
                continue
            }

            if (res.otherError is PathChanged) {
                if (uncertain) {
                    throw CommandException(CommandException.UNKNOWN, errorText(res.otherError, state.value), outcome = Outcome.UNKNOWN)
                }
                throw res.otherError
            }

            val ce = res.connectError
            if (ce != null) {
                val refusal = res.refusal
                if (refusal != null && !uncertain) {
                    val msgText = if (refusal.reason == "machine_offline") UiMessages.errorsTheComputerIsOfflineTheCommandWasNotSent().messageText()
                    else if (ce.code == Code.UNAVAILABLE) (ce.message ?: ce.code.name.lowercase()).messageText()
                    else errorText(ce)
                    throw CommandException(ce.code.name.lowercase(), msgText, outcome = Outcome.NOT_SENT)
                }
                val cause = ce.exception
                if (cause is java.net.UnknownHostException || cause is java.net.ConnectException) {
                    throw if (uncertain) {
                        CommandException(CommandException.UNKNOWN, errorText(ce, state.value), outcome = Outcome.UNKNOWN)
                    } else {
                        CommandException("network", errorText(ce), outcome = Outcome.NOT_SENT)
                    }
                }
                if (ce.code == Code.UNAUTHENTICATED && attempt < 3) {
                    attempt++
                    discardToken(currentToken())
                    continue
                }
                if (ce.code in setOf(Code.DEADLINE_EXCEEDED, Code.UNKNOWN, Code.INTERNAL_ERROR, Code.CANCELED, Code.ABORTED) &&
                    attempt < 3 &&
                    clock() + 2_000L < deadlineMs
                ) {
                    attempt++
                    kotlinx.coroutines.delay(1_000L * attempt)
                    continue
                }
                if (ce.code == Code.UNAUTHENTICATED) discardToken(currentToken())
                throw CommandException(CommandException.UNKNOWN, errorText(ce, state.value), outcome = Outcome.UNKNOWN)
            }

            val err = res.otherError
            if (err is CommandException) throw err
            if (uncertain && err != null) {
                throw CommandException(CommandException.UNKNOWN, errorText(err, state.value), outcome = Outcome.UNKNOWN)
            }
            if (err != null) throw err
            throw CommandException(
                CommandException.UNKNOWN,
                if (ev.transportDetail.isNotEmpty()) ev.transportDetail.messageText()
                else UiMessages.errorsAndroidCannotOpenTheComputerSReplyCommandOutcomeUnknown().messageText(),
                outcome = Outcome.UNKNOWN,
            )
        }
        throw IllegalStateException()
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
        if (!hasSessionSeed(sid)) throw CommandException("not_found", UiMessages.sessionAndroidUnknown().messageText())
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
        } catch (e: DeviceSecurity.Failure) {
            return@withContext ImageResult.Corrupt(errorText(e, state.value))
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
        if (!hasSessionSeed(sid)) throw CommandException("not_found", UiMessages.sessionAndroidUnknown().messageText())
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
            val ws = deviceLink.path.openStream(
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
            sendMachineCommand(
                s.machineId,
                Command.newBuilder().setOpenTerminal(OpenTerminalRequest.newBuilder().setSessionId(sid).setStreamId(streamId)).build(),
                sessionId = sid,
            )
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
        val s = state.value.sessions[sessionId]
            ?: throw CommandException("not_found", UiMessages.sessionAndroidUnknown().messageText())
        val result = sendMachineCommand(
            s.machineId,
            Command.newBuilder()
                .setGetSharedFileChunk(GetSharedFileChunkRequest.newBuilder().setSessionId(sessionId).setFileId(fileId).setOffset(offset))
                .build(),
            sessionId = sessionId,
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

    private fun DeviceSecurity.Purpose.payloadKind(): PayloadKind =
        PayloadKind.forNumber(kind.toInt()) ?: PayloadKind.PAYLOAD_KIND_UNSPECIFIED

    private fun throwSecurityOpError(res: SecurityOpResultEffect): Nothing {
        when (res.errorCode) {
            "unsupported_history" -> throw UnsupportedHistoryException()
            "recovery_unavailable" -> throw DeviceSecurity.Failure("unavailable")
            "stale" ->
                if (res.error.contains("lease expired")) throw DeviceSecurity.Failure("lease")
                else throw DeviceSecurity.Failure("stale")
            "clock", "untrusted", "capacity" -> throw DeviceSecurity.Failure(res.errorCode)
            else -> throw DeviceSecurity.Failure(res.errorCode.ifEmpty { "invalid" })
        }
    }

    private suspend fun hasSessionSeed(sid: String): Boolean {
        ensureSecurityRecordLoaded()
        val res = coreHost.securityOp {
            setGetSessionSeed(SecurityGetSessionSeedOp.newBuilder().setSessionId(sid))
        }
        return res.error.isEmpty() && res.hasSessionSeed() && res.sessionSeed.size() == KEY_SIZE
    }

    private suspend fun <T : com.google.protobuf.MessageLite> openSecureMessageWithMeta(
        env: Envelope,
        want: Place,
        parse: (ByteArray) -> T,
        live: Boolean = true,
    ): Pair<SecurityOpenEnvelopeResult, T> {
        ensureSecurityRecordLoaded()
        val purpose = DeviceSecurity.Purpose.entries.firstOrNull { it.value == want.kind.number.toUInt() }
            ?: throw DeviceSecurity.Failure("invalid")
        val history = !live || want.kind in setOf(
            PayloadKind.PAYLOAD_KIND_ACCOUNT_PREFS,
            PayloadKind.PAYLOAD_KIND_DEVICE_META,
            PayloadKind.PAYLOAD_KIND_MACHINE_META,
            PayloadKind.PAYLOAD_KIND_SESSION_META,
            PayloadKind.PAYLOAD_KIND_IMAGE,
        )
        val res = coreHost.securityOp {
            setOpenEnvelope(
                SecurityOpenEnvelopeOp.newBuilder()
                    .setEnvelope(env)
                    .setExpectedPurpose(purpose.value.toInt())
                    .setMachineId(want.machineId)
                    .setSessionId(want.sessionId)
                    .setRequestId(want.requestId)
                    .setSeq(want.seq)
                    .setHistory(history),
            )
        }
        if (res.error.isNotEmpty() || !res.hasOpened()) {
            throwSecurityOpError(res)
        }
        val opened = res.opened
        val msg = try {
            parse(opened.plaintext.toByteArray())
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            throw OpenException("malformed content")
        }
        return opened to msg
    }

    private suspend fun <T : com.google.protobuf.MessageLite> openSecureMessage(
        env: Envelope,
        want: Place,
        parse: (ByteArray) -> T,
        live: Boolean = true,
        @Suppress("UNUSED_PARAMETER") bulk: Boolean = false,
    ): Pair<Header, T> {
        val (opened, msg) = openSecureMessageWithMeta(env, want, parse, live = live)
        val projected = Header.newBuilder()
            .setVersion(DeviceSecurity.VERSION)
            .setKind(want.kind)
            .setMachineId(opened.machineId)
            .setSessionId(opened.sessionId)
            .setRequestId(opened.requestId)
            .setSeq(opened.seq)
            .setDeadlineMs(opened.deadlineMs)
            .setSender(opened.sender)
            .build()
        return projected to msg
    }

    private suspend fun sealEnvelopeOp(
        purpose: DeviceSecurity.Purpose,
        machineId: String = "",
        sessionId: String = "",
        request: String = "",
        seq: ULong = 0uL,
        deadline: ULong = 0uL,
        plain: ByteArray,
        useSessionSeed: Boolean = purpose.sessionKey,
    ): SecuritySealEnvelopeResult {
        ensureSecurityRecordLoaded()
        val res = coreHost.securityOp {
            setSealEnvelope(
                SecuritySealEnvelopeOp.newBuilder()
                    .setPurpose(purpose.value.toInt())
                    .setMachineId(machineId)
                    .setSessionId(sessionId)
                    .setRequestId(request)
                    .setSeq(seq.toLong())
                    .setDeadlineMs(deadline.toLong())
                    .setPlaintext(ByteString.copyFrom(plain))
                    .setUseSessionSeed(useSessionSeed),
            )
        }
        if (res.error.isNotEmpty() || !res.hasSealed()) {
            throwSecurityOpError(res)
        }
        return res.sealed
    }

    private suspend fun sealSecureMessage(h: Header, m: com.google.protobuf.MessageLite): Envelope {
        var purpose = DeviceSecurity.Purpose.entries.firstOrNull { it.value == h.kindValue.toUInt() }
            ?: throw DeviceSecurity.Failure("invalid")
        if (m is TerminalFrame) {
            purpose = when (m.kindCase) {
                TerminalFrame.KindCase.INPUT -> DeviceSecurity.Purpose.TERMINAL_INPUT
                TerminalFrame.KindCase.RESIZE -> DeviceSecurity.Purpose.TERMINAL_RESIZE
                TerminalFrame.KindCase.HISTORY_REQUEST -> DeviceSecurity.Purpose.TERMINAL_HISTORY
                else -> purpose
            }
        }
        return sealEnvelopeOp(
            purpose = purpose,
            machineId = h.machineId,
            sessionId = h.sessionId,
            request = h.requestId,
            seq = h.seq.toULong(),
            deadline = h.deadlineMs.toULong(),
            plain = m.toByteArray(),
            useSessionSeed = purpose.sessionKey,
        ).envelope
    }
}

internal fun Command.isReadOnlyCommand(): Boolean =
    hasGetMachineStatus() || hasGetUsageStats() || hasGetSettings() || hasListModels() || hasListDirectories() ||
        hasGetLiveSnapshot() || hasGetImageUploadStatus() || hasGetSharedFileChunk() ||
        (hasSchedule() && schedule.operation in setOf("list", "get"))

/** The exact signed response plus its original request binding, never locally re-encrypted JSON. */
internal fun scheduleSnapshot(request: String, envelope: Envelope): ByteArray = buildJsonObject {
    put("request", request)
    put("envelope", java.util.Base64.getEncoder().encodeToString(envelope.toByteArray()))
}.toString().toByteArray()
