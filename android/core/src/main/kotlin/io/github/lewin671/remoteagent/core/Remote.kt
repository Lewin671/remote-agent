package io.github.lewin671.remoteagent.core

import com.connectrpc.ConnectException
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.core.transport.*
import io.github.lewin671.remoteagent.core.transport.Link as DeviceLink
import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
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
    private val sendFrame: (TerminalFrame) -> Unit,
    private val closeLocal: () -> Unit,
) {
    internal var confirmRebuildAction: () -> Unit = {}
    fun send(frame: TerminalFrame) = sendFrame(frame)
    fun close() = closeLocal()
    fun confirmRebuild() = confirmRebuildAction()
}

private const val STREAM_PING_SECONDS = 20L
private const val STREAM_FRAME_BYTES = 2 * 1024 * 1024
private const val STREAM_QUEUE_BYTES = 8L * 1024 * 1024
private const val POLICY_VIOLATION = 1008

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
    sharedCoreHost: CoreHost? = null,
) {
    private val dispatcher = dispatcher ?: Dispatchers.IO.limitedParallelism(1)
    private val bulkCpuDispatcher =
        if (dispatcher == null) io.github.lewin671.remoteagent.core.transport.CpuLanes.bulk else this.dispatcher
    private val directDispatcher =
        if (dispatcher == null || dispatcher === Dispatchers.Unconfined) Dispatchers.IO.limitedParallelism(1)
        else this.dispatcher

    private val relayServer = Secret.checkServer(account.server, allowPlainHttp = true)
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

    @Volatile private var cachedToken: LoginToken? = null
    @Volatile private var running = false
    @Volatile private var closed = false

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

    // Only allocated when a standalone Remote creates its own CoreHost (unit tests);
    // production RaApp always passes its lazy singleton CoreHost with SecurityExecutor already wired.
    private val standaloneSecurityExecutor: SecurityExecutor? = if (sharedCoreHost == null) {
        SecurityExecutor(
            store = securityStore,
            http = http,
            emit = { ev -> coreHost.dispatch(ev) },
            parentScope = scope,
            rpcTransport = rpcTransport,
            dispatcher = if (rpcTransport != null) this.dispatcher else Dispatchers.IO,
            defaultServer = account.server,
            defaultDeviceId = account.deviceId,
        )
    } else null

    private class PendingTerminalOpen(
        val onOpenSocket: (TerminalOpenSocketEffect) -> Unit,
        val onStateChanged: (TerminalStateChangedEffect) -> Unit,
    )

    private class ActiveTerminalStream(
        val machineId: String,
        val sendRelay: (ByteArray) -> Unit,
        val onSentOut: () -> Unit,
        val onOpenSocket: (TerminalOpenSocketEffect) -> Unit,
        val onCloseSocket: (TerminalCloseSocketEffect) -> Unit,
        val onDeliver: (TerminalDeliverOutputEffect) -> Unit,
        val onStateChanged: (TerminalStateChangedEffect) -> Unit,
        val onDirectBytes: (String, Long, ByteArray) -> Unit,
    )

    private val nextTerminalOpId = AtomicLong(1L)
    private val pendingTerminalOpens = ConcurrentHashMap<Long, PendingTerminalOpen>()
    private val activeTerminals = ConcurrentHashMap<String, ActiveTerminalStream>()

    internal val directExecutor: DirectExecutor = DirectExecutor(
        emit = { ev -> coreHost.dispatch(ev) },
        onPushedEvent = { machineId, pcId, env, header, size ->
            handleDirectEvent(machineId, pcId, env, header, size)
        },
        onTerminalOutput = { machineId, pcId, _, header, raw ->
            activeTerminals[header.requestId]?.onDirectBytes?.invoke(machineId, pcId, raw)
        },
        factory = rtcFactory,
        clock = clock,
        monotonic = monotonic,
        dispatcher = directDispatcher,
        bulkReceiveDispatcher = bulkCpuDispatcher,
    )

    internal var deviceLink = DeviceLink(RelayPath(sockets = { socketFactory ?: streamHttp }))

    fun directEnabled(machineId: String): Boolean =
        runCatching { directPrefs.isEnabled(account.server, account.deviceId, machineId) }.getOrDefault(true)
    fun setDirectEnabled(machineId: String, enabled: Boolean) = coreHost.updateDirectPreference(machineId, enabled)
    fun directRouteReport(machineId: String): DirectRouteReport = coreHost.snapshotDirectDiagnosticsView(machineId).report
    fun filePath(machineId: String): DirectPathChoice =
        if (directRouteReport(machineId).fileRoute == DirectPathChoice.DIRECT_PATH_CHOICE_DIRECT) DirectPathChoice.DIRECT_PATH_CHOICE_DIRECT
        else DirectPathChoice.DIRECT_PATH_CHOICE_RELAY

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
                    val existing = coreHost.securityClient.status.value
                    val st = if (existing != null &&
                        (existing.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_READY ||
                            existing.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_REMOVED)
                    ) {
                        existing
                    } else {
                        coreHost.securityClient.getStatus().takeIf {
                            it.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_READY ||
                                it.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_REMOVED
                        } ?: coreHost.securityClient.bootstrap()
                    }
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

    val coreHost = sharedCoreHost ?: CoreHost(
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

    fun watchSession(sid: String, initialWindow: SessionWindowRequest? = null): Flow<SessionViewModel> = coreHost.watchSession(sid, initialWindow)
    fun currentSessionView(sid: String): SessionViewModel? = coreHost.currentSessionView(sid)
    fun updateSessionWindow(sid: String, window: SessionWindowRequest) = coreHost.updateSessionWindow(sid, window)
    fun openSession(sid: String, refreshModels: Boolean = false, refreshSettings: Boolean = false) = coreHost.openSession(sid, refreshModels, refreshSettings)
    fun submitSessionCommand(sid: String, cmd: Command, machineId: String = "", slotKey: String = "") = coreHost.submitSessionCommand(sid, cmd, machineId, slotKey)
    fun abandonSessionCommand(sid: String, slotKey: String) = coreHost.abandonSessionCommand(sid, slotKey)
    fun cancelTrayItem(sid: String, requestId: String) = coreHost.cancelTrayItem(sid, requestId)
    fun ignoreTrayItem(sid: String, requestId: String) = coreHost.ignoreTrayItem(sid, requestId)
    fun promoteTrayItem(sid: String, requestId: String) = coreHost.promoteTrayItem(sid, requestId)
    fun restoreTrayItem(sid: String, requestId: String) = coreHost.restoreTrayItem(sid, requestId)
    fun retryTrayItem(sid: String, requestId: String) = coreHost.retryTrayItem(sid, requestId)
    fun resumeTrayItem(sid: String, requestId: String) = coreHost.resumeTrayItem(sid, requestId)
    fun cancelAllPaused(sid: String) = coreHost.cancelAllPaused(sid)
    fun undoCancel(sid: String) = coreHost.undoCancel(sid)
    fun setSessionQuestionIndex(sid: String, questionId: String, index: Int) = coreHost.setSessionQuestionIndex(sid, questionId, index)
    fun loadEarlierSessionHistory(sid: String) = coreHost.loadEarlierSessionHistory(sid)
    fun homeResolveApproval(sid: String, approvalId: String, allow: Boolean, always: Boolean = false, denyReason: String = "") = coreHost.homeResolveApproval(sid, approvalId, allow, always, denyReason)
    fun homeEditApprovalDeny(sid: String, approvalId: String, denyingOpen: Boolean) = coreHost.homeEditApprovalDeny(sid, approvalId, denyingOpen)
    fun homeAnswerQuestion(sid: String, questionId: String, optionNum: Int) = coreHost.homeAnswerQuestion(sid, questionId, optionNum)
    fun homeRetryAttention(sid: String, itemId: String) = coreHost.homeRetryAttention(sid, itemId)
    fun homeAbandonAttention(sid: String, itemId: String) = coreHost.homeAbandonAttention(sid, itemId)
    fun editApprovalDeny(sid: String, approvalId: String, denyingOpen: Boolean) = coreHost.editApprovalDeny(sid, approvalId, denyingOpen)
    fun resolveApproval(sid: String, approvalId: String, allow: Boolean, always: Boolean = false, denyReason: String = "") = coreHost.resolveApproval(sid, approvalId, allow, always, denyReason)
    fun toggleQuestionOption(sid: String, questionId: String, optionNum: Int) = coreHost.toggleQuestionOption(sid, questionId, optionNum)
    fun answerQuestion(sid: String, questionId: String, optionNum: Int = 0, optionNums: List<Int> = emptyList()) = coreHost.answerQuestion(sid, questionId, optionNum, optionNums)
    fun answerPlan(sid: String, choice: PlanChoice) = coreHost.answerPlan(sid, choice)
    fun sessionSimpleCommand(sid: String, kind: SessionSimpleCommandKind) = coreHost.sessionSimpleCommand(sid, kind)
    fun renameSession(sid: String, title: String) = coreHost.renameSession(sid, title)
    fun setApprovalMode(sid: String, mode: ApprovalMode) = coreHost.setApprovalMode(sid, mode)
    fun setSessionModel(sid: String, model: String, effort: String = "") = coreHost.setSessionModel(sid, model, effort)
    fun retrySessionCommand(sid: String, slotKey: String) = coreHost.retrySessionCommand(sid, slotKey)

    init {
        wireCoreHost()
    }

    // --- M2 Effect executors ---

    private fun wireCoreHost() {
        coreHost.onDirectPreference = { eff ->
            runCatching { directPrefs.setEnabled(account.server, account.deviceId, eff.machineId, eff.enabled) }
        }
        coreHost.resolveUiMessage = { uiMessageResolver(it) }
        standaloneSecurityExecutor?.let { exec ->
            coreHost.onDismissUpgradeNotice = { securityStore.dismissUpgradeNotice() }
            coreHost.onSecurityEffect = exec::handleEffect
        }
        coreHost.sendRelayPipeConfig(true)
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
            val term = activeTerminals[eff.streamId]
            term?.onSentOut?.invoke()
            if (eff.sendRelay) term?.sendRelay?.invoke(raw)
            if (eff.directPcId > 0L) {
                scope.launch(directDispatcher) { directExecutor.sendTerminalFrame(eff.directPcId, raw) }
            }
        }
        coreHost.onTerminalOpenSocket = { eff ->
            val active = activeTerminals[eff.streamId]
            if (active != null) active.onOpenSocket(eff)
            else pendingTerminalOpens[eff.opId]?.onOpenSocket?.invoke(eff)
        }
        coreHost.onTerminalCloseSocket = { eff ->
            activeTerminals[eff.streamId]?.onCloseSocket?.invoke(eff)
        }
        coreHost.onTerminalDeliverOutput = { eff ->
            activeTerminals[eff.streamId]?.onDeliver?.invoke(eff)
        }
        coreHost.onTerminalStateChanged = { eff ->
            val active = activeTerminals[eff.streamId]
            if (active != null) active.onStateChanged(eff)
            else pendingTerminalOpens[eff.opId]?.onStateChanged?.invoke(eff)
        }
        coreHost.onWatchedSessions = { eff ->
            watchedSessionRoutes = eff.sessionsList.associate { it.sessionId to it.machineId }
        }
        coreHost.onSaveHomeScope = { eff ->
            homeScopeStore?.writeToken(eff.scopeToken.ifEmpty { encodeHomeScopeToken(account.localKey, eff.scopeKey) })
        }
        coreHost.onOpenStream = { eff ->
            if (eff.token.isNotEmpty() && cachedToken == null) {
                cachedToken = LoginToken(eff.token, 0L)
            }
            scope.launch { openStreamForCore(eff.legId, eff.helloFrame.toByteArray()) }
        }
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
                    val rawBlob = cache?.blob("schedule:${eff.machineId}") ?: ByteArray(0)
                    coreHost.deliverScheduleCacheLoaded(eff.opId, eff.machineId, rawBlob = rawBlob)
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
                    if (eff.machineId.isNotEmpty() && !eff.blob.isEmpty) {
                        cache?.putBlob("schedule:${eff.machineId}", eff.blob.toByteArray())
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
            enqueueCacheOp {
                try {
                    if (mid.isNotEmpty() && !eff.rawSnapshot.isEmpty && eff.preset in USAGE_CACHE_PRESETS) {
                        cache?.putBlob(usageBlobKey(mid, eff.preset), eff.rawSnapshot.toByteArray())
                    }
                } finally {
                    if (mid.isNotEmpty()) coreHost.deliverUsageCacheWritten(mid)
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
                        if (bytes.isNotEmpty()) {
                            coreHost.deliverUsageCacheLoadedRaw(mid, preset, bytes)
                        }
                    }
                }
            }
        }
    }

    // --- Token Cache (owned by Core security engine) ---

    suspend fun currentToken(): String =
        cachedToken?.token
            ?: runCatching { securityStore.loadLoginToken(account.server, account.deviceId)?.token }.getOrNull()
            ?: ""

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

    private suspend fun openStreamForCore(legId: Long, helloFrame: ByteArray) {
        val url = account.server.replaceFirst(Regex("^http"), "ws") + "/v1/stream"
        val queuedBytes = AtomicLong()
        val frames = Channel<ByteArray>(frameQueue, onUndeliveredElement = { queuedBytes.addAndGet(-it.size.toLong()) })
        val closed = AtomicBoolean(false)
        val policyViolation = AtomicBoolean(false)
        val streamError = java.util.concurrent.atomic.AtomicReference<Throwable?>(null)
        lateinit var legJob: Job

        val listener = object : FrameListener() {
            override fun onOpen(stream: FrameStream) {
                if (helloFrame.isNotEmpty()) {
                    stream.send(helloFrame)
                    directExecutor.attachLeg(stream)
                }
            }

            override fun onFrame(stream: FrameStream, bytes: ByteArray) {
                val sink = directSignalSink
                if (sink != null) {
                    val deliveryBody = extractRawFieldBytes(bytes, 11)
                    if (deliveryBody != null) {
                        val cur = activeStreamLeg
                        if (cur != null && cur.legId == legId && cur.socket === stream && running) {
                            runCatching { sink(deliveryBody) }
                        }
                        return
                    }
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

        val socket = deviceLink.path.openStream(url, listener)
        legJob = scope.launch {
            var notifyClose = true
            try {
                for (raw in frames) {
                    ensureActive()
                    queuedBytes.addAndGet(-raw.size.toLong())
                    coreHost.dispatchStreamRawFrame(legId, raw)
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

    private fun extractRawFieldBytes(raw: ByteArray, fieldNumber: Int): ByteArray? =
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

    private inline fun sendEvent(
        block: CoreEvent.Builder.() -> Unit,
    ) = coreHost.dispatch(CoreEvent.newBuilder().apply(block).build())

    // --- Direct DataChannel Command Executor (relay legs run in Core) ---

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

        if (coreHost.securityClient.currentStatus?.clockSkewed == true) return localFailure("failed_precondition", "clock skew")
        if (!eff.hasSealedEnvelope()) return localFailure("failed_precondition", "missing sealed command")

        val useDirect = eff.routeLeg == CommandRouteLeg.COMMAND_ROUTE_LEG_DIRECT && eff.directPcId > 0L
        if (!useDirect) return localFailure("failed_precondition", "relay leg handled by core")

        var handedOff = false
        val handoff: () -> Unit = { handedOff = true }
        val startedNs = System.nanoTime()
        fun elapsedMs(): Double = maxOf((System.nanoTime() - startedNs) / 1_000_000.0, 0.01)
        fun baseOutcome(source: OutcomeSource): CommandOutcomeEvent.Builder =
            CommandOutcomeEvent.newBuilder()
                .setRequestId(eff.requestId)
                .setAttemptId(eff.attemptId)
                .setSource(source)
                .setRouteWasDirect(true)

        return try {
            val env = directExecutor.sendRpc(eff.directPcId, eff.requestId, eff.sealedEnvelope.toByteArray(), eff.directTimeoutMs, handoff)
            baseOutcome(OutcomeSource.OUTCOME_SOURCE_DAEMON)
                .setAttemptBound(true)
                .setSealedResult(env)
                .setHandedOff(handedOff)
                .setElapsedMs(elapsedMs())
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
        } catch (e: IOException) {
            baseOutcome(OutcomeSource.OUTCOME_SOURCE_TRANSPORT)
                .setNotForwarded(!handedOff)
                .setHandedOff(handedOff)
                .setTransportCode(if (unresolvedHost(e)) "unresolved_host" else "network")
                .setTransportDetail(e.message ?: e.javaClass.simpleName)
                .setElapsedMs(elapsedMs())
                .build()
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

    fun networkChanged(signature: String, available: Boolean) = coreHost.networkChanged(available, signature)
    suspend fun retryDirect(machineId: String): Boolean = coreHost.retryDirectNow(machineId)
    fun refreshDirectDiagnostics(machineId: String) = coreHost.refreshDirectDiagnostics(machineId)
    fun reconnectNow() = coreHost.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_RECONNECT_NOW)
    fun retryLogin() = coreHost.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_RETRY_LOGIN)
    fun retrySessionLoad(sid: String) = coreHost.retrySessionLoad(sid)
    fun startSharedFileDownload(sessionId: String, fileId: String, autoOpenIfStaying: Boolean = true) = coreHost.startSharedFileDownload(sessionId, fileId, autoOpenIfStaying)
    fun pauseSharedFileDownload(sessionId: String, fileId: String) = coreHost.pauseSharedFileDownload(sessionId, fileId)
    fun cancelSharedFileDownload(sessionId: String, fileId: String) = coreHost.cancelSharedFileDownload(sessionId, fileId)
    fun deleteSharedFileLocal(sessionId: String, fileId: String) = coreHost.deleteSharedFileLocal(sessionId, fileId)
    fun setSharedFileVisibleSession(sessionId: String) = coreHost.setSharedFileVisibleSession(sessionId)
    fun forgetSharedFileSession(sessionId: String) = coreHost.forgetSharedFileSession(sessionId)
    fun clearAllSharedFiles() = coreHost.clearAllSharedFiles()
    suspend fun clearAllSharedFilesAndWait() = coreHost.clearAllSharedFilesAndWait()
    fun reportSharedFileDurationProbed(sessionId: String, fileId: String, durationMs: Long) = coreHost.reportSharedFileDurationProbed(sessionId, fileId, durationMs)
    fun reportSharedFileLocalMissing(sessionId: String, fileId: String) = coreHost.reportSharedFileLocalMissing(sessionId, fileId)
    fun openDraft(scope: DraftScope, machineId: String = "") = coreHost.openDraft(scope, machineId)
    fun setDraftText(scope: DraftScope, text: String) = coreHost.setDraftText(scope, text)
    fun reserveDraftSlots(scope: DraftScope, reservationId: String, imageSlots: Int, fileSlots: Int) = coreHost.reserveDraftSlots(scope, reservationId, imageSlots, fileSlots)
    fun releaseDraftSlots(scope: DraftScope, reservationId: String) = coreHost.releaseDraftSlots(scope, reservationId)
    fun addDraftItems(scope: DraftScope, items: List<DraftPickedItem>, reservationId: String = "") = coreHost.addDraftItems(scope, items, reservationId)
    fun removeDraftItem(scope: DraftScope, itemId: String) = coreHost.removeDraftItem(scope, itemId)
    fun retryDraftItem(scope: DraftScope, itemId: String) = coreHost.retryDraftItem(scope, itemId)
    fun setDraftOriginal(scope: DraftScope, original: Boolean) = coreHost.setDraftOriginal(scope, original)
    fun forgetDraftSession(sessionId: String) = coreHost.forgetDraftSession(sessionId)
    fun clearAllDrafts() = coreHost.clearAllDrafts()
    fun dismissDraftNotice(scope: DraftScope) = coreHost.dismissDraftNotice(scope)
    fun pauseAttachmentUpload(scope: DraftScope, itemId: String) = coreHost.pauseAttachmentUpload(scope, itemId)
    fun resumeAttachmentUpload(scope: DraftScope, itemId: String) = coreHost.resumeAttachmentUpload(scope, itemId)
    fun sendDraftSession(sessionId: String, mode: SendMode = SendMode.SEND_MODE_STEER) = coreHost.sendDraftSession(sessionId, mode)
    fun putBackDraftInput(sessionId: String, requestId: String) = coreHost.putBackDraftInput(sessionId, requestId)
    fun openDraftImageViewer(scope: DraftScope, index: Int) = coreHost.openDraftImageViewer(scope, index)
    fun navigateDraftImageViewer(scope: DraftScope, delta: Int = 0, setIndex: Boolean = false, index: Int = 0) = coreHost.navigateDraftImageViewer(scope, delta, setIndex, index)
    fun closeDraftImageViewer(scope: DraftScope) = coreHost.closeDraftImageViewer(scope)
    fun openSessionImageViewer(sid: String, seq: Long = 0L, imageIndex: Int = 0, viewerIndex: Int = 0) = coreHost.openSessionImageViewer(sid, seq, imageIndex, viewerIndex)
    fun navigateSessionImageViewer(sid: String, delta: Int = 0, setIndex: Boolean = false, index: Int = 0) = coreHost.navigateSessionImageViewer(sid, delta, setIndex, index)
    fun closeSessionImageViewer(sid: String) = coreHost.closeSessionImageViewer(sid)

    fun close() {
        closed = true
        running = false
        cleanup(
            { directExecutor.close() },
            { deviceLink.closeStreams() },
            { standaloneSecurityExecutor?.close() },
            {
                if (standaloneSecurityExecutor != null) coreHost.securityClient.rejectAll()
                else coreHost.clearRemoteCallbacks()
            },
            { cache?.close() },
            {
                scope.launch {
                    try {
                        closeStreamLeg(0L)
                    } finally {
                        try {
                            if (standaloneSecurityExecutor != null) coreHost.close()
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
        ensureSecurityConfigured()
        coreHost.refreshDevices()
    }

    suspend fun setPushTarget(type: PushType, target: String, kinds: List<NotifyKind>): SetPushTargetResponse {
        ensureSecurityConfigured()
        val res = coreHost.pushRpc {
            setTarget = true
            pushType = type.number
            this.target = target
            addAllKinds(kinds)
        }
        if (!res.ok) throw PushRpcException(res.error.ifEmpty { "push rpc failed" }, res.retryable)
        return SetPushTargetResponse.newBuilder().setEnabled(res.enabled).build()
    }

    fun renewLeaseIfNeeded() = renewLease(force = false)

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

    suspend fun prepareHandoffCode(storagePersisted: Boolean = true): String =
        withSecurity { prepareHandoffCode(storagePersisted) }

    suspend fun publishHandoffOffer() {
        withSecurity { publishHandoffOffer() }
        if (!closed) refreshDevices()
    }

    suspend fun handoffAuthority(incoming: String) { withSecurity { handoffAuthority(incoming) } }

    suspend fun renameDevice(id: String, name: String) {
        if (id != account.deviceId) throw MessageArgumentException(UiMessages.errorsNamesCanOnlyBeChangedOnThatDevice().messageText())
        withSecurity { updateDeviceMeta(deviceId = id, name = name.trim()) }
        if (!closed) refreshDevices()
    }
    suspend fun removeDevice(deviceId: String) { withSecurity { removeDevice(deviceId) }; if (!closed) refreshDevices() }
    suspend fun deleteMachineRecords(machineId: String) {
        withSecurity { deleteMachineRecords(machineId) }
        coreHost.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_RECONNECT_NOW)
    }

    // --- One-shot Machine Probes ---

    suspend fun pingServer(timeoutMs: Long = 8_000): Long = withContext(Dispatchers.IO) {
        val req = okhttp3.Request.Builder().url(relayServer.trimEnd('/') + "/healthz").header("Cache-Control", "no-store").build()
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
        ensureSecurityConfigured()
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
        ensureSecurityConfigured()
        val cacheKey = "$sid/$imageId"
        val cachedBytes = runCatching { cache?.image(cacheKey) }.getOrNull()
        val res = coreHost.fetchSessionImage(
            sessionId = sid,
            imageId = imageId,
            cachedEnvelope = cachedBytes,
            machineId = sessionMachineId(sid),
        )
        when (res.status) {
            SessionImageLoadStatus.SESSION_IMAGE_LOAD_STATUS_OK -> {
                if (!res.cacheEnvelopeBytes.isEmpty) {
                    runCatching { cache?.putImage(cacheKey, res.cacheEnvelopeBytes.toByteArray()) }
                }
                ImageResult.Loaded(
                    ImagePayload.newBuilder()
                        .setFormat(res.format)
                        .setWidth(res.width)
                        .setHeight(res.height)
                        .setData(res.data)
                        .build(),
                )
            }
            SessionImageLoadStatus.SESSION_IMAGE_LOAD_STATUS_MISSING -> ImageResult.Missing
            SessionImageLoadStatus.SESSION_IMAGE_LOAD_STATUS_EXPIRED -> ImageResult.Expired
            SessionImageLoadStatus.SESSION_IMAGE_LOAD_STATUS_BAD -> {
                val msg = when {
                    res.retiredHistory -> RETIRED_HISTORY_ERROR
                    res.hasErrorText() -> coreTextToMessageText(res.errorText)
                    else -> UiMessages.errorsInvalidCiphertextSignatureKeyOrContentVerificationFailed().messageText()
                }
                ImageResult.Corrupt(msg)
            }
            else -> {
                val code = res.errorCode.ifEmpty { "unavailable" }
                val msg = if (res.hasErrorText()) coreTextToMessageText(res.errorText)
                else code.messageText()
                throw CommandException(code, msg)
            }
        }
    }

    suspend fun openTerminal(sid: String, listener: TermListener, terminalId: String = ""): TermConn {
        ensureSecurityConfigured()
        val sessionMachineId = sessionMachineId(sid)
        val opId = nextTerminalOpId.getAndIncrement()
        val opened = CompletableDeferred<Unit>()
        val closed = AtomicBoolean(false)
        val streamIdRef = java.util.concurrent.atomic.AtomicReference("")
        val activeSocket = java.util.concurrent.atomic.AtomicReference<FrameStream?>()
        val allSockets = java.util.Collections.newSetFromMap(ConcurrentHashMap<FrameStream, Boolean>())
        val url = account.server.replaceFirst(Regex("^http"), "ws") + "/v1/term"
        val inPendingFrames = java.util.concurrent.atomic.AtomicInteger(0)
        val inPendingBytes = AtomicLong(0L)
        val outPendingFrames = java.util.concurrent.atomic.AtomicInteger(0)

        val stream = deviceLink.openStream(stop = { reason ->
            if (closed.compareAndSet(false, true)) {
                pendingTerminalOpens.remove(opId)
                val sidStr = streamIdRef.get()
                if (sidStr.isNotEmpty()) activeTerminals.remove(sidStr)
                coreHost.terminalClose(sidStr, opId)
                val sockets = allSockets.toList()
                allSockets.clear()
                activeSocket.set(null)
                val actions = ArrayList<() -> Unit>(sockets.size + 1)
                for (ws in sockets) actions.add { ws.cancel() }
                actions.add {
                    opened.completeExceptionally(MessageException(reason ?: UiMessages.terminalAndroidClosed().messageText()))
                }
                cleanup(*actions.toTypedArray())
            }
        }, onClose = listener::onClose)
        fun finish(reason: MessageText? = null) = stream.end(reason)
        fun reserveInbound(size: Int): Boolean {
            if (size > (1 shl 20) || inPendingFrames.incrementAndGet() > 128 || inPendingBytes.addAndGet(size.toLong()) > (2L shl 20)) {
                finish(UiMessages.errorsAndroidTerminalOutputBacklogReceptionStopped().messageText())
                return false
            }
            return true
        }

        fun openSocket(eff: TerminalOpenSocketEffect) {
            if (closed.get()) return
            val sidStr = eff.streamId
            val legBytes = eff.legId.toByteArray()
            val helloBytes = eff.helloBytes.toByteArray()
            val dialOpened = AtomicBoolean(false)
            val nextWs = try {
                deviceLink.path.openStream(
                    url,
                    object : FrameListener() {
                        override fun onOpen(stream: FrameStream) {
                            if (closed.get()) { stream.cancel(); return }
                            dialOpened.set(true)
                            stream.send(helloBytes)
                            val prev = activeSocket.getAndSet(stream)
                            if (prev != null && prev !== stream) {
                                allSockets.remove(prev)
                                prev.close()
                            }
                            coreHost.terminalSocket(sidStr, legBytes, opened = true)
                        }
                        override fun onFrame(stream: FrameStream, bytes: ByteArray) {
                            if (closed.get() || activeSocket.get() !== stream) return
                            if (!reserveInbound(bytes.size)) return
                            coreHost.terminalInboundBytes(sidStr, bytes, fromDirect = false, pcId = 0L)
                        }
                        private fun handleSocketClose(stream: FrameStream, code: Int, closeFirst: Boolean) {
                            if (closed.get() || (dialOpened.get() && activeSocket.get() !== stream)) return
                            if (closeFirst) stream.close()
                            if (!opened.isCompleted && eff.isInitial) {
                                finish(UiMessages.errorsTerminalConnectionClosed().messageText())
                                return
                            }
                            coreHost.terminalSocket(sidStr, legBytes, closed = true, policyViolation = code == POLICY_VIOLATION)
                        }
                        override fun onClosing(stream: FrameStream, code: Int) = handleSocketClose(stream, code, closeFirst = true)
                        override fun onEnd(stream: FrameStream, code: Int) = handleSocketClose(stream, code, closeFirst = false)
                        override fun onFailure(stream: FrameStream, error: Throwable) {
                            if (closed.get() || (dialOpened.get() && activeSocket.get() !== stream)) return
                            val errMsg = errorText(error, currentLeaseRenewalAdvice())
                            if (!opened.isCompleted && eff.isInitial) {
                                finish(localized { copy -> copy(UiMessages.errorsTerminalConnectionDroppedReason(errMsg.resolve(copy))) })
                                return
                            }
                            coreHost.terminalSocket(sidStr, legBytes, closed = true, error = error.message ?: error.javaClass.simpleName)
                        }
                    },
                )
            } catch (e: Throwable) {
                if (!opened.isCompleted && eff.isInitial) {
                    finish(UiMessages.errorsCannotConnectToTerminal().messageText())
                } else {
                    coreHost.terminalSocket(sidStr, legBytes, closed = true, error = e.message ?: e.javaClass.simpleName)
                }
                return
            }
            allSockets.add(nextWs)
            if (eff.isInitial) activeSocket.set(nextWs)
            if (closed.get()) {
                allSockets.remove(nextWs)
                nextWs.cancel()
            }
        }

        fun onDeliver(eff: TerminalDeliverOutputEffect) {
            if (closed.get()) return
            try {
                for (f in eff.framesList) {
                    if (closed.get()) break
                    when (f.kindCase) {
                        TerminalDeliveredFrame.KindCase.OUTPUT -> listener.onFrame(TerminalFrame.newBuilder().setOutput(f.output).build())
                        TerminalDeliveredFrame.KindCase.SNAPSHOT -> listener.onFrame(
                            TerminalFrame.newBuilder().setSnapshot(
                                TerminalSnapshot.newBuilder().setCols(f.snapshot.cols).setRows(f.snapshot.rows).setData(f.snapshot.data),
                            ).build(),
                        )
                        TerminalDeliveredFrame.KindCase.CLOSED -> listener.onFrame(TerminalFrame.newBuilder().setClosed(f.closed).build())
                        else -> {}
                    }
                }
            } finally {
                if (eff.consumedFrames > 0) inPendingFrames.updateAndGet { maxOf(0, it - eff.consumedFrames) }
                if (eff.consumedBytes > 0) inPendingBytes.updateAndGet { maxOf(0L, it - eff.consumedBytes.toLong()) }
            }
        }

        fun onStateChanged(eff: TerminalStateChangedEffect) {
            if (closed.get()) return
            if (eff.openSettled && !opened.isCompleted) {
                if (eff.openOk) {
                    opened.complete(Unit)
                    return
                }
                val code = eff.reasonCode.ifEmpty { "failed" }
                val msg = if (eff.hasOpenError()) coreTextToMessageText(eff.openError)
                else if (eff.hasReasonText()) coreTextToMessageText(eff.reasonText)
                else UiMessages.errorsTerminalConnectionClosed().messageText()
                opened.completeExceptionally(CommandException(code, msg))
                finish(msg)
                return
            }
            when (eff.state) {
                TerminalLifecycleState.TERMINAL_LIFECYCLE_STATE_CONFIRM_INPUT ->
                    listener.onConfirmRebuild(TermConfirmKind.INPUT)
                TerminalLifecycleState.TERMINAL_LIFECYCLE_STATE_CONFIRM_RESTART ->
                    listener.onConfirmRebuild(TermConfirmKind.RESTART)
                TerminalLifecycleState.TERMINAL_LIFECYCLE_STATE_REBUILD -> {
                    finish()
                    listener.onRebuildConfirmed()
                }
                TerminalLifecycleState.TERMINAL_LIFECYCLE_STATE_CLOSED -> {
                    val msg = if (eff.hasReasonText()) coreTextToMessageText(eff.reasonText)
                    else UiMessages.errorsTerminalConnectionDropped().messageText()
                    finish(msg)
                }
                else -> {}
            }
        }

        pendingTerminalOpens[opId] = PendingTerminalOpen(
            onOpenSocket = { eff ->
                if (!closed.get()) {
                    streamIdRef.set(eff.streamId)
                    pendingTerminalOpens.remove(opId)
                    activeTerminals[eff.streamId] = ActiveTerminalStream(
                        machineId = sessionMachineId,
                        sendRelay = { raw -> if (!closed.get()) activeSocket.get()?.send(raw) },
                        onSentOut = { outPendingFrames.updateAndGet { maxOf(0, it - 1) } },
                        onOpenSocket = ::openSocket,
                        onCloseSocket = {
                            val list = allSockets.toList()
                            allSockets.clear()
                            activeSocket.set(null)
                            for (ws in list) runCatching { ws.cancel() }
                        },
                        onDeliver = ::onDeliver,
                        onStateChanged = ::onStateChanged,
                        onDirectBytes = { machineId, pcId, raw ->
                            if (!closed.get() && (sessionMachineId.isEmpty() || machineId == sessionMachineId) && reserveInbound(raw.size)) {
                                coreHost.terminalInboundBytes(eff.streamId, raw, fromDirect = true, pcId = pcId)
                            }
                        },
                    )
                    openSocket(eff)
                }
            },
            onStateChanged = ::onStateChanged,
        )

        try {
            coreHost.terminalOpen(opId, sid, sessionMachineId, terminalId)
            withTimeout(20_000) { opened.await() }
            kotlinx.coroutines.currentCoroutineContext().ensureActive()
            if (closed.get()) throw MessageException(UiMessages.terminalAndroidClosed().messageText())
            val sidStr = streamIdRef.get()
            return TermConn(
                sendFrame = { frame ->
                    if (!closed.get()) {
                        if (frame.serializedSize > 64 * 1024 || outPendingFrames.incrementAndGet() > 64) {
                            finish(UiMessages.errorsTerminalInputBacklogReconnect().messageText())
                        } else when (frame.kindCase) {
                            TerminalFrame.KindCase.INPUT -> coreHost.terminalInput { streamId = sidStr; input = frame.input }
                            TerminalFrame.KindCase.RESIZE -> coreHost.terminalInput {
                                streamId = sidStr
                                resize = TerminalResizeInput.newBuilder().setCols(frame.resize.cols).setRows(frame.resize.rows).setFit(frame.resize.fit).build()
                            }
                            TerminalFrame.KindCase.HISTORY_REQUEST -> coreHost.terminalInput {
                                streamId = sidStr
                                history = TerminalHistoryInput.newBuilder().setRequestId(frame.historyRequest.requestId).setCursor(frame.historyRequest.cursor).build()
                            }
                            else -> {}
                        }
                    }
                },
                closeLocal = { finish() },
            ).apply {
                confirmRebuildAction = { if (!closed.get() && sidStr.isNotEmpty()) coreHost.terminalConfirmRebuild(sidStr) }
            }
        } catch (e: Throwable) {
            finish()
            throw e
        }
    }

    private fun report(e: Throwable) {
        log("remote-agent: " + e.javaClass.simpleName)
        if (System.getenv("RA_DEBUG_STACK") != null) e.printStackTrace()
    }
}

class PushRpcException(message: String, val retryable: Boolean) : Exception(message)
