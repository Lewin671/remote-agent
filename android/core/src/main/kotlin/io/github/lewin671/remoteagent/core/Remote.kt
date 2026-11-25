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

enum class TermConfirmKind { INPUT, RESTART }

interface TermListener {
    fun onFrame(frame: TerminalFrame)
    fun onClose(reason: MessageText)
    fun onConfirmRebuild(kind: TermConfirmKind) {}
    fun onRebuildConfirmed() {}
}

class TermConn internal constructor(
    private val ws: FrameStream,
    private val sendFrame: (TerminalFrame) -> Unit,
    private val closeLocal: () -> Unit,
) {
    internal var confirmRebuildAction: () -> Unit = {}
    fun send(frame: TerminalFrame) = sendFrame(frame)
    fun close() = cleanup(closeLocal, { ws.close() })
    fun confirmRebuild() = confirmRebuildAction()
}

internal fun ucmp(a: Long, b: Long): Int = java.lang.Long.compareUnsigned(a, b)

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
    private val securityStore: SecurityStoreV4 = MemorySecurityStoreV4(),
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
    private val devicePlatform: String = "",
    private val deviceDescription: String = "",
) {
    private val dispatcher = dispatcher ?: Dispatchers.IO.limitedParallelism(1)
    private val bulkCpuDispatcher =
        if (dispatcher == null) io.github.lewin671.remoteagent.core.transport.CpuLanes.bulk else this.dispatcher
    private val directDispatcher =
        if (dispatcher == null || dispatcher === Dispatchers.Unconfined) Dispatchers.IO.limitedParallelism(1)
        else this.dispatcher

    val api = Api(account.server, http, ::rejectToken, rpcTransport)
    private val directServerHost: String =
        if (stunUrls != null) "" else runCatching { java.net.URI(account.server).host.orEmpty() }.getOrDefault("")
    private val directConfiguredStun: String = stunUrls?.joinToString(",") ?: ""
    private val scope = CoroutineScope(SupervisorJob() + this.dispatcher)
    private val inFlightDispatches = ConcurrentHashMap<String, Pair<DispatchCommandEffect, Job>>()
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
    private val devicesPushSeq = AtomicLong(1L)
    @Volatile private var running = false
    @Volatile private var closed = false
    @Volatile private var serverAhead = false

    private class ActiveStreamLeg(val legId: Long, val socket: FrameStream)

    private val streamMutex = Mutex()
    private var activeLegId = 0L
    @Volatile private var activeSocket: FrameStream? = null
    @Volatile private var activeStreamLeg: ActiveStreamLeg? = null
    internal val activeStreamForTest: FrameStream? get() = activeSocket
    private var activeLegJob: Job? = null
    @Volatile internal var directSignalSink: ((ByteArray) -> Unit)? = null

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

    private val securityConfigured = AtomicBoolean(false)
    @Volatile private var securityBootstrapped = false
    @Volatile private var foregroundActive = true
    private val securityBootstrapMutex = Mutex()
    private var securityBootstrapJob: Deferred<Unit>? = null

    private val securityExecutor = SecurityExecutor(
        store = securityStore,
        http = http,
        emit = { ev -> coreHost.dispatch(ev) },
        parentScope = scope,
        rpcTransport = rpcTransport,
        dispatcher = if (rpcTransport != null) this.dispatcher else Dispatchers.IO,
    )

    private class ActiveTerminalStream(
        val sendRelay: (ByteArray) -> Unit,
        val deliver: (List<ByteArray>) -> Unit,
        val attachRelay: (ByteArray) -> Unit,
        val onState: (Int, String, Boolean) -> Unit,
        val onDirectOutput: (String, Long, Envelope, ByteArray) -> Unit,
    )

    private val activeTerminals = ConcurrentHashMap<String, ActiveTerminalStream>()

    internal val directExecutor: DirectExecutor = DirectExecutor(
        emit = { ev -> coreHost.dispatch(ev) },
        onPushedEvent = { machineId, pcId, env, header, size ->
            handleDirectEvent(machineId, pcId, env, header, size)
        },
        onTerminalOutput = { machineId, pcId, env, header, raw ->
            activeTerminals[header.requestId]?.onDirectOutput?.invoke(machineId, pcId, env, raw)
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
    ))

    fun directEnabled(machineId: String): Boolean =
        runCatching { directPrefs.isEnabled(account.server, account.deviceId, machineId) }.getOrDefault(true)
    fun setDirectEnabled(machineId: String, enabled: Boolean) = coreHost.updateDirectPreference(machineId, enabled)
    fun directRouteReport(machineId: String): DirectRouteReport = coreHost.snapshotDirectDiagnosticsView(machineId).report
    fun filePath(machineId: String): DirectPathChoice =
        if (directRouteReport(machineId).fileRoute == DirectPathChoice.DIRECT_PATH_CHOICE_DIRECT) DirectPathChoice.DIRECT_PATH_CHOICE_DIRECT
        else DirectPathChoice.DIRECT_PATH_CHOICE_RELAY
    @Volatile internal var parseServerFrame: (ByteArray) -> ServerFrame = ServerFrame::parseFrom

    private fun ensureSecurityConfigured() {
        if (closed) return
        if (securityConfigured.compareAndSet(false, true)) {
            coreHost.sendSecurityConfig(
                account = account.accountId,
                deviceId = account.deviceId,
                server = account.server,
                inlineCrypto = true,
                foreground = foregroundActive,
                platform = devicePlatform,
                deviceDescription = deviceDescription,
            )
        }
    }

    private suspend fun ensureSecurityBootstrapped() {
        if (closed || securityBootstrapped) return
        ensureSecurityConfigured()
        val deferred = securityBootstrapMutex.withLock {
            if (securityBootstrapped || closed) null
            else securityBootstrapJob ?: scope.async {
                try {
                    val st = coreHost.securityClient.bootstrap()
                    applySecurityStatusSnapshotAndPrune(st)
                    securityBootstrapped = true
                } catch (t: Throwable) {
                    securityBootstrapMutex.withLock {
                        securityBootstrapJob = null
                    }
                    if (t is CancellationException) throw t
                    throw t
                }
            }.also { securityBootstrapJob = it }
        }
        deferred?.await()
    }

    private val streamHttp by lazy {
        http.newBuilder()
            .pingInterval(STREAM_PING_SECONDS, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.MILLISECONDS)
            .build()
    }

    @Volatile private var watchedSessionRoutes: Map<String, String> = emptyMap()

    private var authorityJob = SupervisorJob(scope.coroutineContext[Job])

    @Volatile var homeScopeStore: HomeScopeStore? = null
    @Volatile var uiMessageResolver: MessageResolver = { it.key }
        set(value) {
            field = value
            coreHost.resolveUiMessage = value
        }

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
    val scheduleNavigationEvents: Flow<ScheduleNavigateEffect> get() = coreHost.scheduleNavigationEvents
    val restoreComposerEvents: SharedFlow<SessionRestoreComposerEffect> get() = coreHost.restoreComposerEvents
    val connectionView: StateFlow<ConnectionViewModel> get() = coreHost.connectionView
    val securityClient: SecurityClient get() = coreHost.securityClient
    val securityStatus: StateFlow<SecurityWorkflowStatus?> get() = coreHost.securityClient.status

    fun watchSession(sid: String, initialWindow: SessionWindowRequest? = null): Flow<SessionViewModel> =
        coreHost.watchSession(sid, initialWindow)

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

    fun homeResolveApproval(
        sid: String,
        approvalId: String,
        allow: Boolean,
        always: Boolean = false,
        denyReason: String = "",
    ) = coreHost.homeResolveApproval(sid, approvalId, allow, always, denyReason)

    fun homeEditApprovalDeny(sid: String, approvalId: String, denyingOpen: Boolean) =
        coreHost.homeEditApprovalDeny(sid, approvalId, denyingOpen)

    fun homeAnswerQuestion(sid: String, questionId: String, optionNum: Int) =
        coreHost.homeAnswerQuestion(sid, questionId, optionNum)

    fun homeRetryAttention(sid: String, itemId: String) =
        coreHost.homeRetryAttention(sid, itemId)

    fun homeAbandonAttention(sid: String, itemId: String) =
        coreHost.homeAbandonAttention(sid, itemId)

    fun editApprovalDeny(sid: String, approvalId: String, denyingOpen: Boolean) =
        coreHost.editApprovalDeny(sid, approvalId, denyingOpen)

    fun resolveApproval(
        sid: String,
        approvalId: String,
        allow: Boolean,
        always: Boolean = false,
        denyReason: String = "",
    ) = coreHost.resolveApproval(sid, approvalId, allow, always, denyReason)

    fun toggleQuestionOption(sid: String, questionId: String, optionNum: Int) =
        coreHost.toggleQuestionOption(sid, questionId, optionNum)

    fun answerQuestion(
        sid: String,
        questionId: String,
        optionNum: Int = 0,
        optionNums: List<Int> = emptyList(),
    ) = coreHost.answerQuestion(sid, questionId, optionNum, optionNums)

    fun answerPlan(sid: String, choice: PlanChoice) =
        coreHost.answerPlan(sid, choice)

    fun sessionSimpleCommand(sid: String, kind: SessionSimpleCommandKind) =
        coreHost.sessionSimpleCommand(sid, kind)

    fun renameSession(sid: String, title: String) =
        coreHost.renameSession(sid, title)

    fun setApprovalMode(sid: String, mode: ApprovalMode) =
        coreHost.setApprovalMode(sid, mode)

    fun setSessionModel(sid: String, model: String, effort: String = "") =
        coreHost.setSessionModel(sid, model, effort)

    fun retrySessionCommand(sid: String, slotKey: String) =
        coreHost.retrySessionCommand(sid, slotKey)

    init {
        coreHost.onDirectPreference = { eff ->
            runCatching { directPrefs.setEnabled(account.server, account.deviceId, eff.machineId, eff.enabled) }
        }
    }

    internal suspend fun cachedSchedules(machine: String): ByteArray? = withContext(dispatcher) {
        ensureSecurityBootstrapped()
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
        reply.schedule.resultJson.toByteArray()
    }

    init {
        wireCoreHost()
    }

    // --- M2 Effect executors ---

    private fun wireCoreHost() {
        coreHost.resolveUiMessage = { uiMessageResolver(it) }
        coreHost.onDismissUpgradeNotice = { securityStore.dismissUpgradeNotice() }
        coreHost.onSecurityEffect = securityExecutor::handleEffect
        coreHost.onSecurityTokenUpdated = ::handleSecurityTokenUpdated
        coreHost.onSecurityStatusChanged = { eff ->
            if (eff.hasStatus()) {
                applySecurityStatusSnapshot(eff.status)
            }
        }
        coreHost.onDirectSignalSend = { eff -> directExecutor.signalSend(eff) }
        coreHost.onDirectPcCreate = { eff -> scope.launch(directDispatcher) { directExecutor.pcCreate(eff) } }
        coreHost.onDirectPcDescribe = { eff -> scope.launch(directDispatcher) { directExecutor.pcDescribe(eff) } }
        coreHost.onDirectPcSetRemote = { eff -> scope.launch(directDispatcher) { directExecutor.pcSetRemote(eff) } }
        coreHost.onDirectPcAddCandidates = { eff -> scope.launch(directDispatcher) { directExecutor.pcAddCandidates(eff) } }
        coreHost.onDirectPcConfirm = { eff -> scope.launch(directDispatcher) { directExecutor.pcConfirm(eff) } }
        coreHost.onDirectPcClose = { eff -> scope.launch(directDispatcher) { directExecutor.pcClose(eff) } }
        coreHost.onDirectPcSendProbe = { eff -> scope.launch(directDispatcher) { directExecutor.pcSendProbe(eff) } }
        coreHost.onDirectDcReadStats = { eff -> scope.launch(directDispatcher) { directExecutor.dcReadStats(eff) } }
        coreHost.onTerminalDirectSend = { eff ->
            val raw = eff.sealedEnvelope.toByteArray()
            if (eff.sendRelay) activeTerminals[eff.streamId]?.sendRelay?.invoke(raw)
            if (eff.directPcId > 0L) {
                scope.launch(directDispatcher) { directExecutor.sendTerminalFrame(eff.directPcId, raw) }
            }
        }
        coreHost.onTerminalDirectDeliver = { eff ->
            activeTerminals[eff.streamId]?.deliver?.invoke(eff.payloadsList.map { it.toByteArray() })
        }
        coreHost.onTerminalDirectAttachRelay = { eff ->
            activeTerminals[eff.streamId]?.attachRelay?.invoke(eff.legId.toByteArray())
        }
        coreHost.onTerminalDirectState = { eff ->
            activeTerminals[eff.streamId]?.onState?.invoke(eff.state, eff.reason, eff.needsConfirmation)
        }
        coreHost.onWatchedSessions = { eff ->
            watchedSessionRoutes = eff.sessionsList.associate { it.sessionId to it.machineId }
        }
        coreHost.onSaveHomeScope = { eff ->
            homeScopeStore?.writeToken(eff.scopeToken.ifEmpty { encodeHomeScopeToken(account.localKey, eff.scopeKey) })
        }
        coreHost.onEnsureToken = { eff -> scope.launch { ensureTokenForCore(eff.opId, eff.forceLogin) } }
        coreHost.onDiscardToken = { eff -> scope.launch { discardToken(eff.token) } }
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
        coreHost.onDispatchCommand = { eff ->
            val job = scope.launch { executeDispatchCommand(eff) }
            inFlightDispatches[eff.attemptId] = eff to job
            job.invokeOnCompletion { inFlightDispatches.remove(eff.attemptId) }
        }
        coreHost.onCancelDispatch = { eff ->
            val rid = eff.requestId
            if (rid.isNotEmpty()) {
                for ((aid, pair) in inFlightDispatches.entries.toList()) {
                    val (dispatched, job) = pair
                    if (dispatched.requestId == rid) {
                        if (inFlightDispatches.remove(aid, pair)) job.cancel()
                    }
                }
            }
        }
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
        coreHost.onScheduleWriteCache = { eff ->
            enqueueCacheOp {
                try {
                    if (eff.machineId.isNotEmpty() && !eff.sealedResultBytes.isEmpty) {
                        val env = Envelope.parseFrom(eff.sealedResultBytes)
                        cache?.putBlob("schedule:${eff.machineId}", scheduleSnapshot(eff.requestId, env))
                    }
                } finally {
                    coreHost.deliverScheduleCacheWritten(eff.machineId)
                }
            }
        }
        coreHost.onScheduleDeleteCache = { eff ->
            enqueueCacheOp {
                cache?.removeBlob("schedule:${eff.machineId}")
                scheduleSeenStore?.delete(eff.machineId)
            }
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
                    try {
                        cache?.putBlob(usageBlobKey(mid, snap.preset), snap.toByteArray())
                    } finally {
                        coreHost.deliverUsageCacheWritten(mid)
                    }
                }
            }
        }
        coreHost.onUsageCacheRequest = { eff ->
            val mid = eff.machineId
            if (mid.isNotEmpty()) {
                if (eff.deleteCache) {
                    usageHydratedMachines.remove(mid)
                    enqueueCacheOp { removeUsageBlobs(mid) }
                } else {
                    coreHost.hydrateUsageCache(mid)
                }
            }
        }
        coreHost.onHydrateUsageCache = { mid ->
            if (mid.isNotEmpty() && cache != null && usageHydratedMachines.add(mid)) {
                enqueueCacheOp {
                    for (preset in USAGE_CACHE_PRESETS) {
                        val bytes = runCatching { cache?.blob(usageBlobKey(mid, preset)) }.getOrNull() ?: continue
                        val snap = runCatching { UsageStatsResponse.parseFrom(bytes) }.getOrNull() ?: continue
                        coreHost.deliverUsageCacheLoaded(mid, snap)
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
                securityStore.loadLoginToken(account.server, account.deviceId)?.let { stored ->
                    if (stored.expiresMs - LOGIN_REFRESH_MARGIN_MS > now) {
                        cachedToken = stored
                        return stored
                    }
                    securityStore.clearLoginToken(account.server, account.deviceId)
                }
            }
            val (deferred, owner) = tokenMutex.withLock {
                val nowLocked = clock()
                if (!forceLogin) {
                    cachedToken?.takeIf { it.expiresMs - LOGIN_REFRESH_MARGIN_MS > nowLocked }?.let { return it }
                } else {
                    cachedToken = null
                    securityStore.clearLoginToken(account.server, account.deviceId)
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
                    val res = loginWithStore(api, account.deviceId, securityStore)
                    val tok = LoginToken(res.token, res.expiresMs)
                    cachedToken = tok
                    securityStore.saveLoginToken(account.server, account.deviceId, tok)
                    deferred.complete(tok)
                } catch (t: Throwable) {
                    if (loginDenied(t)) {
                        cachedToken = null
                        securityStore.clearLoginToken(account.server, account.deviceId)
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
                securityStore.clearLoginToken(account.server, account.deviceId)
            }
        }
    }

    private fun rejectToken(rejected: String) {
        scope.launch {
            discardToken(rejected)
        }
    }

    // --- Security Status & Workflow Hooks ---

    private fun handleSecurityTokenUpdated(eff: SecurityTokenUpdatedEffect) {
        if (eff.cleared) {
            cachedToken = null
            scope.launch { runCatching { securityStore.clearLoginToken(account.server, account.deviceId) } }
            return
        }
        val tok = LoginToken(eff.token, eff.expiresMs)
        cachedToken = tok
        scope.launch { runCatching { securityStore.saveLoginToken(account.server, account.deviceId, tok) } }
    }

    private fun applySecurityStatusSnapshot(status: SecurityWorkflowStatus) {
        if (status.removed) {
            cachedToken = null
            scope.launch { runCatching { securityStore.clearLoginToken(account.server, account.deviceId) } }
        }
    }

    private suspend fun applySecurityStatusSnapshotAndPrune(status: SecurityWorkflowStatus) {
        if (status.removed) {
            cachedToken = null
            runCatching { securityStore.clearLoginToken(account.server, account.deviceId) }
        }
        coreHost.dispatchAndWait(CoreEvent.getDefaultInstance())
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
                    val cur = activeStreamLeg
                    if (cur == null || cur.legId != legId || cur.socket !== stream || !running) return
                    runCatching {
                        if (deliveryBody != null) {
                            directSignalSink?.invoke(deliveryBody) ?: coreHost.dispatchDirectSignal(deliveryBody)
                        } else if (resultBody != null) {
                            val res = PeerFrameResult.parseFrom(resultBody)
                            coreHost.dispatchDirectSignalDelivery(
                                res.frameId.toByteArray(),
                                res.statusValue,
                                res.retryAfterMs.toLong(),
                            )
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
                            serverAhead = clock() - frame.ready.serverTimeMs > MAX_CLOCK_SKEW_MS
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
                            activeStreamLeg = null
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
            activeStreamLeg = ActiveStreamLeg(legId, socket)
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
                activeStreamLeg = null
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
        watchedSessionRoutes[sessionId]?.takeIf { it.isNotEmpty() }
            ?: coreHost.currentSessionView(sessionId)?.header?.machineId.orEmpty()

    private fun handleDirectEvent(machineId: String, pcId: Long, env: Envelope, header: Header, @Suppress("UNUSED_PARAMETER") size: Int) {
        if (closed) return
        val sid = header.sessionId
        if (sid.isEmpty() || header.seq <= 0L) return
        val routeMid = watchedSessionRoutes[sid] ?: return
        if (routeMid != machineId || header.sender != machineId) return
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
                    ensureSecurityBootstrapped()
                    b.addAllWireMachines(cache?.machines().orEmpty())
                        .addAllWireSessions(cache?.sessions().orEmpty())
                    cache?.blob(PROJECT_PREFS)?.let { b.setWirePrefs(AccountPrefsObject.parseFrom(it)) }
                    homeScopeStore?.readToken()?.takeIf { it.isNotEmpty() }?.let { b.setHomeScopeToken(it) }
                    deriveScopeKey(account.localKey, "home-scope").takeIf { it.isNotEmpty() }?.let { b.setScopeHmacKey(ByteString.copyFrom(it)) }
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
                try {
                    val req = ListRemoteSessionsRequest.newBuilder().addSessionIds(sid).setLimit(1).build()
                    val found = api.client.listRemoteSessions(req, Api.headers(currentToken())).value().sessionsList.firstOrNull { it.sessionId == sid }
                    if (found != null) {
                        b.setWireSession(found)
                    }
                } catch (e: Exception) {
                    if (e is CancellationException) throw e
                    report(e)
                    b.setError(rpcErrorString(e))
                }
                coreHost.dispatchAndWait(CoreEvent.newBuilder().setSessionByIdFetched(b).build())
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
                            PURPOSE_PREFS,
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
                } catch (e: SecurityFailure) {
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

    private suspend fun executeCommandLeg(
        machineId: String,
        sessionId: String,
        requestId: String,
        attemptId: String,
        parsedCmd: Command?,
        authorized: Envelope,
        prior: Boolean,
        bulk: Boolean,
        useDirect: Boolean,
        directPcId: Long,
        directTimeoutMs: Long,
    ): CommandOutcomeEvent {
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
            baseOutcome(OutcomeSource.OUTCOME_SOURCE_DAEMON)
                .setAttemptBound(attemptBound)
                .apply { if (res.hasResult()) setSealedResult(res.result) }
                .setOuterOutcome(res.outcome)
                .setOuterReason(res.reason)
                .setHandedOff(handedOff)
                .setElapsedMs(dtMs)
                .build()
        } catch (e: CancellationException) {
            throw e
        } catch (_: PathChanged) {
            baseOutcome(if (handedOff) OutcomeSource.OUTCOME_SOURCE_TRANSPORT else OutcomeSource.OUTCOME_SOURCE_LOCAL)
                .setNotForwarded(!handedOff)
                .setHandedOff(handedOff)
                .setTransportCode("path_changed")
                .setElapsedMs(elapsedMs())
                .build()
        } catch (ce: ConnectException) {
            val dtMs = elapsedMs()
            val refusal = relayAttempt?.refusal(ce)
            if (refusal != null) {
                baseOutcome(OutcomeSource.OUTCOME_SOURCE_SERVER).setNotForwarded(true).setAttemptBound(true)
                    .setOuterReason(refusal.reason).setTransportCode(mapConnectCode(ce.code))
                    .setTransportDetail(ce.message ?: "").setElapsedMs(dtMs).build()
            } else {
                baseOutcome(OutcomeSource.OUTCOME_SOURCE_TRANSPORT).setNotForwarded(!handedOff).setHandedOff(handedOff)
                    .setTransportCode(if (unresolvedHost(ce)) "unresolved_host" else mapConnectCode(ce.code))
                    .setTransportDetail(ce.message ?: "").setElapsedMs(dtMs).build()
            }
        } catch (e: IOException) {
            baseOutcome(OutcomeSource.OUTCOME_SOURCE_TRANSPORT).setNotForwarded(!handedOff).setHandedOff(handedOff)
                .setTransportCode(if (unresolvedHost(e)) "unresolved_host" else "network")
                .setTransportDetail(e.message ?: e.javaClass.simpleName).setElapsedMs(elapsedMs()).build()
        } catch (e: Exception) {
            baseOutcome(if (handedOff) OutcomeSource.OUTCOME_SOURCE_TRANSPORT else OutcomeSource.OUTCOME_SOURCE_LOCAL)
                .setNotForwarded(!handedOff)
                .setHandedOff(handedOff)
                .setTransportCode("failed_precondition")
                .setTransportDetail(e.message ?: e.javaClass.simpleName)
                .setElapsedMs(elapsedMs())
                .build()
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

        if (serverAhead || coreHost.securityClient.currentStatus?.clockSkewed == true) return localFailure("failed_precondition", "clock skew")
        if (!eff.hasSealedEnvelope()) return localFailure("failed_precondition", "missing sealed command")

        val useDirect = eff.routeLeg == CommandRouteLeg.COMMAND_ROUTE_LEG_DIRECT && eff.directPcId > 0L
        return executeCommandLeg(
            machineId = eff.machineId,
            sessionId = eff.sessionId,
            requestId = eff.requestId,
            attemptId = eff.attemptId,
            parsedCmd = parsedCmd,
            authorized = eff.sealedEnvelope,
            prior = eff.retryOfUnknown,
            bulk = bulk,
            useDirect = useDirect,
            directPcId = eff.directPcId,
            directTimeoutMs = eff.directTimeoutMs,
        )
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
        val token = homeScopeStore?.readToken().orEmpty()
        val hmacKey = deriveScopeKey(account.localKey, "home-scope")
        scope.launch {
            runCatching {
                ensureSecurityBootstrapped()
                if (!running || closed) return@launch
                coreHost.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_START, token, hmacKey)
            }.onFailure { err ->
                if (err is CancellationException) throw err
                if (running && !closed) {
                    coreHost.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_START, token, hmacKey)
                }
            }
        }
    }

    fun stop() {
        running = false
        authorityJob.cancel()
        directExecutor.detachAll()
        coreHost.securityClient.rejectAll()
        coreHost.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_STOP)
        scope.launch { closeStreamLeg(0L) }
    }

    fun foregroundChanged(foreground: Boolean) {
        foregroundActive = foreground
        coreHost.sendSecurityForeground(foreground)
        coreHost.syncLifecycle(
            if (foreground) SyncLifecycleKind.SYNC_LIFECYCLE_KIND_FOREGROUND
            else SyncLifecycleKind.SYNC_LIFECYCLE_KIND_BACKGROUND,
        )
    }

    fun networkChanged(signature: String, available: Boolean) {
        val realChange = lastNetworkSignature != null && (lastNetworkSignature != signature || networkAvailable != available)
        lastNetworkSignature = signature
        networkAvailable = available
        if (realChange) {
            inFlightToken?.completeExceptionally(NetworkResetException())
            inFlightToken = null
            inFlightLoginJob?.cancel()
            inFlightLoginJob = null
        }
        coreHost.networkChanged(available, signature)
    }

    suspend fun retryDirect(machineId: String): Boolean = coreHost.retryDirectNow(machineId)

    fun refreshDirectDiagnostics(machineId: String) = coreHost.refreshDirectDiagnostics(machineId)

    fun reconnectNow() {
        coreHost.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_RECONNECT_NOW)
    }

    fun retryLogin() {
        coreHost.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_RETRY_LOGIN)
    }

    fun retrySessionLoad(sid: String) = coreHost.retrySessionLoad(sid)

    fun startSharedFileDownload(sessionId: String, fileId: String, autoOpenIfStaying: Boolean = true) =
        coreHost.startSharedFileDownload(sessionId, fileId, autoOpenIfStaying)

    fun pauseSharedFileDownload(sessionId: String, fileId: String) =
        coreHost.pauseSharedFileDownload(sessionId, fileId)

    fun cancelSharedFileDownload(sessionId: String, fileId: String) =
        coreHost.cancelSharedFileDownload(sessionId, fileId)

    fun deleteSharedFileLocal(sessionId: String, fileId: String) =
        coreHost.deleteSharedFileLocal(sessionId, fileId)

    fun setSharedFileVisibleSession(sessionId: String) =
        coreHost.setSharedFileVisibleSession(sessionId)

    fun forgetSharedFileSession(sessionId: String) =
        coreHost.forgetSharedFileSession(sessionId)

    fun clearAllSharedFiles() =
        coreHost.clearAllSharedFiles()

    suspend fun clearAllSharedFilesAndWait() =
        coreHost.clearAllSharedFilesAndWait()

    fun reportSharedFileDurationProbed(sessionId: String, fileId: String, durationMs: Long) =
        coreHost.reportSharedFileDurationProbed(sessionId, fileId, durationMs)

    fun reportSharedFileLocalMissing(sessionId: String, fileId: String) =
        coreHost.reportSharedFileLocalMissing(sessionId, fileId)

    fun openDraft(scope: io.github.lewin671.remoteagent.core.proto.DraftScope, machineId: String = "") =
        coreHost.openDraft(scope, machineId)

    fun setDraftText(scope: io.github.lewin671.remoteagent.core.proto.DraftScope, text: String) =
        coreHost.setDraftText(scope, text)

    fun reserveDraftSlots(scope: io.github.lewin671.remoteagent.core.proto.DraftScope, reservationId: String, imageSlots: Int, fileSlots: Int) =
        coreHost.reserveDraftSlots(scope, reservationId, imageSlots, fileSlots)

    fun releaseDraftSlots(scope: io.github.lewin671.remoteagent.core.proto.DraftScope, reservationId: String) =
        coreHost.releaseDraftSlots(scope, reservationId)

    fun addDraftItems(
        scope: io.github.lewin671.remoteagent.core.proto.DraftScope,
        items: List<io.github.lewin671.remoteagent.core.proto.DraftPickedItem>,
        reservationId: String = "",
    ) = coreHost.addDraftItems(scope, items, reservationId)

    fun removeDraftItem(scope: io.github.lewin671.remoteagent.core.proto.DraftScope, itemId: String) =
        coreHost.removeDraftItem(scope, itemId)

    fun retryDraftItem(scope: io.github.lewin671.remoteagent.core.proto.DraftScope, itemId: String) =
        coreHost.retryDraftItem(scope, itemId)

    fun setDraftOriginal(scope: io.github.lewin671.remoteagent.core.proto.DraftScope, original: Boolean) =
        coreHost.setDraftOriginal(scope, original)

    fun forgetDraftSession(sessionId: String) =
        coreHost.forgetDraftSession(sessionId)

    fun clearAllDrafts() =
        coreHost.clearAllDrafts()

    fun dismissDraftNotice(scope: io.github.lewin671.remoteagent.core.proto.DraftScope) =
        coreHost.dismissDraftNotice(scope)

    fun pauseAttachmentUpload(scope: io.github.lewin671.remoteagent.core.proto.DraftScope, itemId: String) =
        coreHost.pauseAttachmentUpload(scope, itemId)

    fun resumeAttachmentUpload(scope: io.github.lewin671.remoteagent.core.proto.DraftScope, itemId: String) =
        coreHost.resumeAttachmentUpload(scope, itemId)

    fun sendDraftSession(sessionId: String, mode: io.github.lewin671.remoteagent.proto.SendMode = io.github.lewin671.remoteagent.proto.SendMode.SEND_MODE_STEER) =
        coreHost.sendDraftSession(sessionId, mode)

    fun putBackDraftInput(sessionId: String, requestId: String) =
        coreHost.putBackDraftInput(sessionId, requestId)

    fun openDraftImageViewer(scope: io.github.lewin671.remoteagent.core.proto.DraftScope, index: Int) =
        coreHost.openDraftImageViewer(scope, index)

    fun navigateDraftImageViewer(
        scope: io.github.lewin671.remoteagent.core.proto.DraftScope,
        delta: Int = 0,
        setIndex: Boolean = false,
        index: Int = 0,
    ) = coreHost.navigateDraftImageViewer(scope, delta, setIndex, index)

    fun closeDraftImageViewer(scope: io.github.lewin671.remoteagent.core.proto.DraftScope) =
        coreHost.closeDraftImageViewer(scope)

    fun openSessionImageViewer(sid: String, seq: Long = 0L, imageIndex: Int = 0, viewerIndex: Int = 0) =
        coreHost.openSessionImageViewer(sid, seq, imageIndex, viewerIndex)

    fun navigateSessionImageViewer(sid: String, delta: Int = 0, setIndex: Boolean = false, index: Int = 0) =
        coreHost.navigateSessionImageViewer(sid, delta, setIndex, index)

    fun closeSessionImageViewer(sid: String) =
        coreHost.closeSessionImageViewer(sid)

    fun close() {
        closed = true
        running = false
        cleanup(
            { directExecutor.close() },
            { deviceLink.closeStreams() },
            { securityExecutor.close() },
            { coreHost.securityClient.rejectAll() },
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

    internal fun prepareForTest(sessions: Map<String, SessionViewModel> = emptyMap()) {
        check(!running) { "showForTest on a started Remote" }
        _bootstrapped.value = true
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
    }

    // --- S6 Security Management & Recovery (delegated to Core SecurityClient) ---

    private suspend inline fun <T> withSecurity(block: suspend SecurityClient.() -> T): T {
        ensureSecurityBootstrapped()
        return coreHost.securityClient.block()
    }

    suspend fun syncSecurity(@Suppress("UNUSED_PARAMETER") budgetMs: Long = Long.MAX_VALUE) {
        applySecurityStatusSnapshotAndPrune(withSecurity { syncNow(touch = false, authToken = cachedToken?.token.orEmpty()) })
    }

    suspend fun refreshDevices() {
        val pushSeq = devicesPushSeq.getAndIncrement()
        val ds = api.client.listDevices(ListDevicesRequest.getDefaultInstance(), Api.headers(currentToken())).value()
        val ev = DevicesFetchedEvent.newBuilder().setOpId(0L).setPushSeq(pushSeq).addAllWireDevices(ds.devicesList).build()
        coreHost.dispatchAndWait(CoreEvent.newBuilder().setDevicesFetched(ev).build())
    }

    suspend fun getSecurityStatus(): SecurityWorkflowStatus =
        withSecurity { getStatus() }.also { applySecurityStatusSnapshotAndPrune(it) }

    suspend fun resumeSecurityOperation(@Suppress("UNUSED_PARAMETER") dispatchSnapshot: Boolean = true) {
        withSecurity { resumePendingOp() }
        if (!closed) refreshDevices()
    }

    fun renewLeaseIfNeeded() = renewLease(force = false)
    fun renewLeaseNow() = renewLease(force = true)

    private fun renewLease(force: Boolean): Job =
        scope.launch {
            try {
                withContext(authorityJob) {
                    applySecurityStatusSnapshot(withSecurity { renewLease(force) })
                }
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                report(e)
            }
        }

    suspend fun renewLeaseBlocking(force: Boolean = true): SecurityWorkflowStatus =
        withSecurity { renewLease(force) }.also { applySecurityStatusSnapshotAndPrune(it) }

    suspend fun prepareRecoveryRotation(): String = withSecurity { prepareRecoveryRotation() }

    suspend fun confirmRecoveryRotation() { withSecurity { confirmRecoveryRotation() } }

    suspend fun prepareHandoffCode(storagePersisted: Boolean = true): String =
        withSecurity { prepareHandoffCode(storagePersisted) }

    suspend fun publishHandoffOffer() {
        withSecurity { publishHandoffOffer() }
        if (!closed) refreshDevices()
    }

    suspend fun handoffAuthority(incoming: String) { withSecurity { handoffAuthority(incoming) } }

    suspend fun finishPairingDelivery(pairingId: String): SecurityPairingInfo =
        withSecurity { finishPairingDelivery(pairingId) }.also { if (it.delivered && !closed) refreshDevices() }

    suspend fun discardPendingOp(abandonHandoff: Boolean = false) { withSecurity { discardPendingOp(abandonHandoff) } }

    suspend fun checkExitAdvice(): SecurityExitAdvice = withSecurity { checkExitAdvice() }

    suspend fun renameDevice(id: String, name: String) {
        if (id != account.deviceId) {
            throw MessageArgumentException(UiMessages.errorsNamesCanOnlyBeChangedOnThatDevice().messageText())
        }
        withSecurity { updateDeviceMeta(deviceId = id, name = name.trim()) }
        if (!closed) refreshDevices()
    }

    suspend fun removeDevice(deviceId: String) {
        withSecurity { removeDevice(deviceId) }
        if (!closed) refreshDevices()
    }

    suspend fun deleteMachineRecords(machineId: String) {
        withSecurity { deleteMachineRecords(machineId) }
        coreHost.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_RECONNECT_NOW)
    }

    // --- One-shot Machine Probes ---

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
            withTimeout(timeoutMs) {
                coreHost.runMachineCommand(
                    machineId,
                    "",
                    Command.newBuilder().setGetSettings(GetSettingsRequest.getDefaultInstance()).build(),
                    skipConnectivityPreflight = true,
                )
            }
        } catch (e: kotlinx.coroutines.TimeoutCancellationException) {
            throw MessageException(UiMessages.connectionNoResponse().messageText(), e)
        }
        return (System.nanoTime() - started) / 1_000_000
    }

    // --- Media & Terminal ---

    sealed interface ImageResult {
        class Loaded(val payload: ImagePayload) : ImageResult
        data object Expired : ImageResult
        data object Missing : ImageResult
        data class Corrupt(val why: MessageText) : ImageResult
    }

    private fun currentLeaseRenewalAdvice(): io.github.lewin671.remoteagent.core.proto.Text? =
        coreHost.connectionView.value.takeIf { it.hasLeaseRenewalAdvice() }?.leaseRenewalAdvice

    suspend fun image(sid: String, imageId: String): ImageResult = withContext(dispatcher) {
        val machineId = sessionMachineId(sid).takeIf { it.isNotEmpty() }
            ?: throw CommandException("not_found", UiMessages.sessionAndroidUnknown().messageText())
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
                Place(PayloadKind.PAYLOAD_KIND_IMAGE, machineId = machineId, sessionId = sid, requestId = imageId),
                ImagePayload::parseFrom,
                live = false,
            ).second
        } catch (_: UnsupportedHistoryException) {
            return@withContext ImageResult.Corrupt(RETIRED_HISTORY_ERROR)
        } catch (e: OpenException) {
            return@withContext ImageResult.Corrupt(e.display)
        } catch (e: SecurityFailure) {
            return@withContext ImageResult.Corrupt(errorText(e, currentLeaseRenewalAdvice()))
        }
        if (detectFormat(payload.data.toByteArray()) != payload.format) {
            return@withContext ImageResult.Corrupt(UiMessages.errorsAndroidImageContentDoesNotMatchItsFormat().messageText())
        }
        runCatching { cache?.putImage(cacheKey, env.toByteArray()) }
        ImageResult.Loaded(payload)
    }

    suspend fun openTerminal(sid: String, listener: TermListener): TermConn {
        val sessionMachineId = sessionMachineId(sid).takeIf { it.isNotEmpty() }
            ?: throw CommandException("not_found", UiMessages.sessionAndroidUnknown().messageText())
        if (!hasSessionSeed(sid)) throw CommandException("not_found", UiMessages.sessionAndroidUnknown().messageText())
        val tok = currentToken()
        val streamId = newRequestId()
        val opened = CompletableDeferred<Unit>()
        val closed = AtomicBoolean(false)
        data class InboundItem(val env: Envelope, val fromDirect: Boolean, val pcId: Long, val size: Int)
        val incoming = Channel<InboundItem>(128)
        data class QueuedInput(val frame: TerminalFrame, val deadline: Long)
        val outgoing = Channel<QueuedInput>(64)
        val queuedBytes = AtomicLong(0)
        val terminalJob = SupervisorJob(scope.coroutineContext[Job])
        val terminalScope = CoroutineScope(scope.coroutineContext + terminalJob)
        val socket = java.util.concurrent.atomic.AtomicReference<FrameStream?>()
        val activeLegId = java.util.concurrent.atomic.AtomicReference(streamId.toByteArray(Charsets.UTF_8))
        val lastRelayError = java.util.concurrent.atomic.AtomicReference<Throwable?>(null)
        val stream = deviceLink.openStream(stop = { reason ->
            if (closed.compareAndSet(false, true)) {
                activeTerminals.remove(streamId)
                coreHost.dispatch(
                    CoreEvent.newBuilder()
                        .setTerminalDirectClose(TerminalDirectCloseEvent.newBuilder().setStreamId(streamId))
                        .build(),
                )
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
                for (item in incoming) {
                    try {
                        val (h, frame) = openSecureMessage(
                            item.env,
                            Place(PayloadKind.PAYLOAD_KIND_TERMINAL_OUTPUT, machineId = sessionMachineId, sessionId = sid, requestId = streamId),
                            TerminalFrame::parseFrom,
                        )
                        val payloadBytes = frame.toByteArray()
                        val frameHash = java.security.MessageDigest.getInstance("SHA-256").digest(payloadBytes)
                        coreHost.dispatch(
                            CoreEvent.newBuilder()
                                .setTerminalDirectOutput(
                                    TerminalDirectOutputEvent.newBuilder()
                                        .setStreamId(streamId)
                                        .setSeq(h.seq)
                                        .setFrameHash(ByteString.copyFrom(frameHash))
                                        .setPayload(ByteString.copyFrom(payloadBytes))
                                        .setFromDirect(item.fromDirect)
                                        .setPcId(item.pcId),
                                )
                                .build(),
                        )
                    } catch (e: Exception) {
                        if (!item.fromDirect) throw e
                    } finally {
                        queuedBytes.addAndGet(-item.size.toLong())
                    }
                }
            } catch (e: Exception) {
                finish(errorText(e, currentLeaseRenewalAdvice()))
            }
        }
        val url = account.server.replaceFirst(Regex("^http"), "ws") + "/v1/term"
        activeTerminals[streamId] = ActiveTerminalStream(
            sendRelay = { raw -> if (!closed.get()) socket.get()?.send(raw) },
            deliver = { payloads ->
                for (p in payloads) {
                    if (closed.get()) break
                    runCatching { TerminalFrame.parseFrom(p) }.getOrNull()?.let { listener.onFrame(it) }
                }
            },
            attachRelay = { legBytes ->
                if (!closed.get()) {
                    val legStr = String(legBytes, Charsets.UTF_8)
                    val dialOpened = AtomicBoolean(false)
                    val nextWs = deviceLink.path.openStream(
                        url,
                        object : FrameListener() {
                            override fun onOpen(stream: FrameStream) {
                                if (closed.get()) { stream.cancel(); return }
                                dialOpened.set(true)
                                val hello = TermHello.newBuilder().setToken(tok).setStreamId(legStr).setMachineId(sessionMachineId).build()
                                stream.send(hello.toByteArray())
                                val prev = socket.getAndSet(stream)
                                activeLegId.set(legBytes)
                                if (prev !== stream) prev?.close()
                                coreHost.dispatch(
                                    CoreEvent.newBuilder()
                                        .setTerminalDirectLeg(
                                            TerminalDirectLegEvent.newBuilder()
                                                .setStreamId(streamId)
                                                .setLegId(ByteString.copyFrom(legBytes))
                                                .setDirect(false)
                                                .setOpen(true),
                                        )
                                        .build(),
                                )
                            }
                            override fun onFrame(stream: FrameStream, bytes: ByteArray) {
                                if (closed.get()) return
                                val env = runCatching { Envelope.parseFrom(bytes) }.getOrNull() ?: return
                                if (bytes.size > (1 shl 20) || queuedBytes.addAndGet(bytes.size.toLong()) > 2L * 1024 * 1024 ||
                                    incoming.trySend(InboundItem(env, false, 0L, bytes.size)).isFailure) {
                                    finish(UiMessages.errorsAndroidTerminalOutputBacklogReceptionStopped().messageText())
                                }
                            }
                            override fun onEnd(stream: FrameStream, code: Int) {
                                if (closed.get()) return
                                if (dialOpened.get() && socket.get() !== stream) return
                                coreHost.dispatch(
                                    CoreEvent.newBuilder()
                                        .setTerminalDirectLeg(
                                            TerminalDirectLegEvent.newBuilder()
                                                .setStreamId(streamId)
                                                .setLegId(ByteString.copyFrom(legBytes))
                                                .setDirect(false)
                                                .setOpen(false),
                                        )
                                        .build(),
                                )
                            }
                            override fun onFailure(stream: FrameStream, error: Throwable) {
                                if (closed.get()) return
                                if (dialOpened.get() && socket.get() !== stream) return
                                if (dialOpened.get()) lastRelayError.set(error)
                                coreHost.dispatch(
                                    CoreEvent.newBuilder()
                                        .setTerminalDirectLeg(
                                            TerminalDirectLegEvent.newBuilder()
                                                .setStreamId(streamId)
                                                .setLegId(ByteString.copyFrom(legBytes))
                                                .setDirect(false)
                                                .setOpen(false),
                                        )
                                        .build(),
                                )
                            }
                        },
                    )
                    if (closed.get()) nextWs.cancel()
                }
            },
            onState = { st, reason, needsConfirm ->
                // M4/M5: NeedsConfirmation enters user confirmation UI without auto-closing;
                // rebuild_confirmed closes the old stream silently and triggers a fresh open.
                if (!closed.get()) {
                    if (needsConfirm) {
                        listener.onConfirmRebuild(if (st == 4) TermConfirmKind.RESTART else TermConfirmKind.INPUT)
                    } else if (st == 5 && reason == "rebuild_confirmed") {
                        finish()
                        listener.onRebuildConfirmed()
                    } else if (st == 5) {
                        val err = lastRelayError.getAndSet(null)
                        if (reason == "conflict") {
                            finish(UiMessages.errorsTerminalFramesAreOutOfOrder().messageText())
                        } else if (reason == "replay_overflow") {
                            finish(UiMessages.terminalReplayOverflow().messageText())
                        } else if (err != null) {
                            finish(localized { copy -> copy(UiMessages.errorsAndroidTerminalConnectionDroppedReason(errorText(err, currentLeaseRenewalAdvice()).resolve(copy))) })
                        } else {
                            finish(UiMessages.errorsTerminalConnectionDropped().messageText())
                        }
                    }
                }
            },
            onDirectOutput = { machineId, pcId, env, raw ->
                if (!closed.get() && machineId == sessionMachineId) {
                    if (queuedBytes.addAndGet(raw.size.toLong()) <= 2L * 1024 * 1024) {
                        if (incoming.trySend(InboundItem(env, true, pcId, raw.size)).isFailure) {
                            queuedBytes.addAndGet(-raw.size.toLong())
                        }
                    } else {
                        queuedBytes.addAndGet(-raw.size.toLong())
                    }
                }
            },
        )
        try {
            val ws = deviceLink.path.openStream(
                url,
                object : FrameListener() {
                    override fun onOpen(stream: FrameStream) {
                        val hello = TermHello.newBuilder().setToken(tok).setStreamId(streamId).setMachineId(sessionMachineId).build()
                        stream.send(hello.toByteArray())
                        opened.complete(Unit)
                    }
                    override fun onFrame(stream: FrameStream, bytes: ByteArray) {
                        if (closed.get()) return
                        val env = runCatching { Envelope.parseFrom(bytes) }.getOrNull()
                        if (env == null || bytes.size > (1 shl 20) || queuedBytes.addAndGet(bytes.size.toLong()) > 2L * 1024 * 1024 ||
                            incoming.trySend(InboundItem(env, false, 0L, bytes.size)).isFailure) {
                            finish(UiMessages.errorsAndroidTerminalOutputBacklogReceptionStopped().messageText())
                        }
                    }
                    override fun onClosing(stream: FrameStream, code: Int) {
                        if (code == POLICY_VIOLATION) rejectToken(tok)
                        if (socket.get() !== stream) return
                        stream.close()
                        if (!opened.isCompleted) {
                            finish(UiMessages.errorsTerminalConnectionClosed().messageText())
                            return
                        }
                        coreHost.dispatch(
                            CoreEvent.newBuilder()
                                .setTerminalDirectLeg(
                                    TerminalDirectLegEvent.newBuilder()
                                        .setStreamId(streamId)
                                        .setLegId(ByteString.copyFrom(activeLegId.get()))
                                        .setDirect(false)
                                        .setOpen(false),
                                )
                                .build(),
                        )
                    }
                    override fun onEnd(stream: FrameStream, code: Int) {
                        if (code == POLICY_VIOLATION) rejectToken(tok)
                        if (socket.get() !== stream) return
                        if (!opened.isCompleted) {
                            finish(UiMessages.errorsTerminalConnectionClosed().messageText())
                            return
                        }
                        coreHost.dispatch(
                            CoreEvent.newBuilder()
                                .setTerminalDirectLeg(
                                    TerminalDirectLegEvent.newBuilder()
                                        .setStreamId(streamId)
                                        .setLegId(ByteString.copyFrom(activeLegId.get()))
                                        .setDirect(false)
                                        .setOpen(false),
                                )
                                .build(),
                        )
                    }
                    override fun onFailure(stream: FrameStream, error: Throwable) {
                        val wasOpened = opened.isCompleted
                        opened.completeExceptionally(MessageException(UiMessages.errorsCannotConnectToTerminal().messageText()))
                        if (socket.get() !== stream) return
                        if (!wasOpened) {
                            finish(localized { copy -> copy(UiMessages.errorsAndroidTerminalConnectionDroppedReason(errorText(error, currentLeaseRenewalAdvice()).resolve(copy))) })
                            return
                        }
                        lastRelayError.set(error)
                        coreHost.dispatch(
                            CoreEvent.newBuilder()
                                .setTerminalDirectLeg(
                                    TerminalDirectLegEvent.newBuilder()
                                        .setStreamId(streamId)
                                        .setLegId(ByteString.copyFrom(activeLegId.get()))
                                        .setDirect(false)
                                        .setOpen(false),
                                )
                                .build(),
                        )
                    }
                },
            )
            socket.set(ws)
            if (closed.get()) ws.cancel()
            withTimeout(20_000) { opened.await() }
            // M6: register in Core before sending OpenTerminal RPC so early frames are never lost.
            coreHost.dispatch(
                CoreEvent.newBuilder()
                    .setTerminalDirectOpen(
                        TerminalDirectOpenEvent.newBuilder()
                            .setStreamId(streamId)
                            .setMachineId(sessionMachineId)
                            .setSessionId(sid)
                            .setRelayLegId(ByteString.copyFrom(activeLegId.get())),
                    )
                    .build(),
            )
            coreHost.runMachineCommand(
                sessionMachineId,
                sid,
                Command.newBuilder().setOpenTerminal(OpenTerminalRequest.newBuilder().setSessionId(sid).setStreamId(streamId)).build(),
                skipConnectivityPreflight = true,
            )
            kotlinx.coroutines.currentCoroutineContext().ensureActive()
            if (closed.get()) throw MessageException(UiMessages.terminalAndroidClosed().messageText())
            // H-B: signal Core that daemon confirmed openTerminal so direct AttachPath fires now.
            coreHost.dispatch(
                CoreEvent.newBuilder()
                    .setTerminalDirectLeg(
                        TerminalDirectLegEvent.newBuilder()
                            .setStreamId(streamId)
                            .setLegId(ByteString.copyFrom(activeLegId.get()))
                            .setDirect(false)
                            .setOpen(true),
                    )
                    .build(),
            )
            var seq = 0L
            terminalScope.launch {
                try {
                    for (item in outgoing) {
                        if (closed.get()) break
                        seq++
                        val env = sealSecureMessage(
                            Header.newBuilder()
                                .setKind(PayloadKind.PAYLOAD_KIND_TERMINAL_INPUT)
                                .setMachineId(sessionMachineId)
                                .setSessionId(sid)
                                .setRequestId(streamId)
                                .setSeq(seq)
                                .setDeadlineMs(item.deadline)
                                .setSender(account.deviceId)
                                .build(),
                            item.frame,
                        )
                        if (!closed.get()) {
                            val sealedBytes = env.toByteArray()
                            val frameHash = java.security.MessageDigest.getInstance("SHA-256").digest(item.frame.toByteArray())
                            coreHost.dispatch(
                                CoreEvent.newBuilder()
                                    .setTerminalDirectInput(
                                        TerminalDirectInputEvent.newBuilder()
                                            .setStreamId(streamId)
                                            .setSeq(seq)
                                            .setIsInput(item.frame.hasInput())
                                            .setSealedEnvelope(ByteString.copyFrom(sealedBytes))
                                            .setFrameHash(ByteString.copyFrom(frameHash)),
                                    )
                                    .build(),
                            )
                        }
                    }
                } catch (e: Exception) {
                    finish(errorText(e, currentLeaseRenewalAdvice()))
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
            ).apply {
                confirmRebuildAction = {
                    if (!closed.get()) {
                        coreHost.dispatch(
                            CoreEvent.newBuilder()
                                .setTerminalDirectConfirmRebuild(
                                    TerminalDirectConfirmRebuildAction.newBuilder().setStreamId(streamId),
                                )
                                .build(),
                        )
                    }
                }
            }
        } catch (e: Exception) {
            finish()
            throw e
        }
    }

    // --- Crypto Envelope Helpers ---

    private fun report(e: Throwable) {
        log("remote-agent: " + e.javaClass.simpleName)
        if (System.getenv("RA_DEBUG_STACK") != null) e.printStackTrace()
    }

    private fun throwSecurityOpError(res: SecurityOpResultEffect): Nothing {
        when (res.structuredErrorCode) {
            SecurityErrorCode.SECURITY_ERROR_CODE_UNSUPPORTED_HISTORY -> throw UnsupportedHistoryException()
            else -> throw SecurityFailure(securityErrorCodeKey(res.structuredErrorCode))
        }
    }

    private suspend fun hasSessionSeed(sid: String): Boolean {
        ensureSecurityBootstrapped()
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
        ensureSecurityBootstrapped()
        val purpose = want.kind.number
        if (purpose <= 0) throw SecurityFailure("invalid")
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
                    .setExpectedPurpose(purpose)
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
            .setVersion(ENVELOPE_VERSION)
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
        purpose: Int,
        machineId: String = "",
        sessionId: String = "",
        request: String = "",
        seq: ULong = 0uL,
        deadline: ULong = 0uL,
        plain: ByteArray,
        useSessionSeed: Boolean = purpose in SESSION_KEY_PURPOSES,
    ): SecuritySealEnvelopeResult {
        ensureSecurityBootstrapped()
        val res = coreHost.securityOp {
            setSealEnvelope(
                SecuritySealEnvelopeOp.newBuilder()
                    .setPurpose(purpose)
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
        var purpose = h.kindValue
        if (purpose <= 0) throw SecurityFailure("invalid")
        if (m is TerminalFrame) {
            purpose = when (m.kindCase) {
                TerminalFrame.KindCase.INPUT -> PURPOSE_TERMINAL_INPUT
                TerminalFrame.KindCase.RESIZE -> PURPOSE_TERMINAL_RESIZE
                TerminalFrame.KindCase.HISTORY_REQUEST -> PURPOSE_TERMINAL_HISTORY
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
            useSessionSeed = purpose in SESSION_KEY_PURPOSES,
        ).envelope
    }

    companion object {
        private const val MAX_CLOCK_SKEW_MS = 5L * 60_000L
        private const val PURPOSE_PREFS = 12
        private const val PURPOSE_TERMINAL_INPUT = 8
        private const val PURPOSE_TERMINAL_RESIZE = 14
        private const val PURPOSE_TERMINAL_HISTORY = 15
        private val SESSION_KEY_PURPOSES = setOf(
            PURPOSE_TERMINAL_INPUT,
            PURPOSE_TERMINAL_RESIZE,
            PURPOSE_TERMINAL_HISTORY,
        )
    }
}

/** The exact signed response plus its original request binding, never locally re-encrypted JSON. */
internal fun scheduleSnapshot(request: String, envelope: Envelope): ByteArray = buildJsonObject {
    put("request", request)
    put("envelope", java.util.Base64.getEncoder().encodeToString(envelope.toByteArray()))
}.toString().toByteArray()
