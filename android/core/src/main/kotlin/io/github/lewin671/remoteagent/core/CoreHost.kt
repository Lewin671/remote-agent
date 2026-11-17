package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.corebind.Core
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.*
import java.security.SecureRandom
import java.time.Instant
import java.time.ZoneId
import java.util.concurrent.atomic.AtomicBoolean

const val CORE_ENDED_PAGE = 30
private const val CALENDAR_MONTH_HISTORY = 35
private const val INITIAL_ENTROPY_IDS = 32
private const val INITIAL_ENTROPY_FLOATS = 32

fun uiMessage(key: String, args: Map<String, Any> = emptyMap()): UiMessage = UiMessage(key, args)

interface HomeScopeStore {
    fun readToken(): String?
    fun writeToken(token: String)
    fun clear()
}

fun encodeHomeScopeToken(root: ByteArray, scopeKey: String): String =
    if (scopeKey.isEmpty()) "all"
    else "project:" + hmacSha256(Keys.deriveKey(root, "home-scope"), scopeKey.toByteArray()).hex()

fun buildLocalCalendar(nowMs: Long, zone: ZoneId = ZoneId.systemDefault()): LocalCalendar {
    val instant = Instant.ofEpochMilli(nowMs)
    val zdt = instant.atZone(zone)
    val today = zdt.toLocalDate()
    val todayStartMs = today.atStartOfDay(zone).toInstant().toEpochMilli()
    val yesterdayStartMs = today.minusDays(1).atStartOfDay(zone).toInstant().toEpochMilli()
    val weekStartMs = today.minusDays(6).atStartOfDay(zone).toInstant().toEpochMilli()
    val firstOfMonth = today.withDayOfMonth(1)
    val b = LocalCalendar.newBuilder()
        .setNowMs(nowMs)
        .setTodayStartMs(todayStartMs)
        .setYesterdayStartMs(yesterdayStartMs)
        .setWeekStartMs(weekStartMs)
        .setNowYear(today.year)
    for (i in CALENDAR_MONTH_HISTORY downTo 0) {
        val m = firstOfMonth.minusMonths(i.toLong())
        val startMs = m.atStartOfDay(zone).toInstant().toEpochMilli()
        b.addMonths(
            MonthStart.newBuilder()
                .setYear(m.year)
                .setMonth(m.monthValue)
                .setStartMs(startMs),
        )
    }
    return b.build()
}

/**
 * Single-threaded host around the shared Go client core (`Corebind.new_()`).
 * Serializes all `Dispatch` and `View` calls on [workerDispatcher], owns timer
 * scheduling and entropy replenishment, executes M2 effects through registered
 * callbacks, and exposes screen and transitional sync views as reactive flows.
 */
@OptIn(ExperimentalCoroutinesApi::class)
class CoreHost(
    workerDispatcher: CoroutineDispatcher? = null,
    externalScope: CoroutineScope? = null,
    private val clock: () -> Long = System::currentTimeMillis,
    private val monotonic: () -> Long = { System.nanoTime() / 1_000_000L },
    private val zone: () -> ZoneId = ZoneId::systemDefault,
    private val random: SecureRandom = SecureRandom(),
) {
    private val ownsScope = externalScope == null
    private val dispatcher: CoroutineDispatcher = workerDispatcher ?: Dispatchers.IO.limitedParallelism(1)
    private val scope: CoroutineScope = externalScope ?: CoroutineScope(SupervisorJob() + dispatcher)
    private val core: Core = Corebind.new_()
    private val coreLock = Any()
    private val closed = AtomicBoolean(false)
    private val mailbox = Channel<() -> Unit>(Channel.UNLIMITED)
    private val pendingRenders = LinkedHashSet<ScreenTarget>()
    private var coalescingRenders = false

    private val timers = HashMap<Long, Job>()

    private class SessionWatcher(
        val flow: MutableStateFlow<SessionDataView> = MutableStateFlow(SessionDataView.getDefaultInstance()),
        var refCount: Int = 0,
    )

    private val sessionWatchers = HashMap<String, SessionWatcher>()

    var onOpenStream: (OpenStreamEffect) -> Unit = {}
    var onCloseStream: (CloseStreamEffect) -> Unit = {}
    var onEnsureToken: (EnsureTokenEffect) -> Unit = {}
    var onDiscardToken: (DiscardTokenEffect) -> Unit = {}
    var onSyncSecurity: (SyncSecurityEffect) -> Unit = {}
    var onCacheRead: (CacheReadEffect) -> Unit = {}
    var onCacheWrite: (CacheWriteEffect) -> Unit = {}
    var onCacheDelete: (CacheDeleteEffect) -> Unit = {}
    var onRpc: (RpcEffect) -> Unit = {}
    var onDispatchCommand: (DispatchCommandEffect) -> Unit = {}
    var onSaveHomeScope: (SaveHomeScopeEffect) -> Unit = {}
    var onNavigate: (NavigateEffect) -> Unit = {}
    var onAttachmentsSettled: (AttachmentsSettledEffect) -> Unit = {}
    var onRender: (List<ScreenTarget>) -> Unit = {}

    private var homeEndedLimit: Int = CORE_ENDED_PAGE
    private var projectKey: String = ""
    private var projectManage: Boolean = true
    private var projectEndedLimit: Int = CORE_ENDED_PAGE
    private var newSessionProjectKey: String = ""
    private var testOverrideViews = false

    private val _homeView = MutableStateFlow(HomeViewModel.getDefaultInstance())
    val homeView: StateFlow<HomeViewModel> = _homeView.asStateFlow()

    private val _projectView = MutableStateFlow(ProjectViewModel.getDefaultInstance())
    val projectView: StateFlow<ProjectViewModel> = _projectView.asStateFlow()

    private val _newSessionView = MutableStateFlow(NewSessionViewModel.getDefaultInstance())
    val newSessionView: StateFlow<NewSessionViewModel> = _newSessionView.asStateFlow()

    private val _syncStateView = MutableStateFlow(SyncStateView.getDefaultInstance())
    val syncStateView: StateFlow<SyncStateView> = _syncStateView.asStateFlow()

    private val _navigationEvents = MutableSharedFlow<NavigateEffect>(extraBufferCapacity = 16)
    val navigationEvents: SharedFlow<NavigateEffect> = _navigationEvents.asSharedFlow()

    private val _attachmentsSettledEvents = MutableSharedFlow<AttachmentsSettledEffect>(extraBufferCapacity = 16)
    val attachmentsSettledEvents: SharedFlow<AttachmentsSettledEffect> = _attachmentsSettledEvents.asSharedFlow()

    init {
        if (!closed.get()) {
            // Seed initial calendar, entropy pool, and default view models synchronously.
            dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setEntropyProvided(buildEntropyProvided(INITIAL_ENTROPY_IDS, INITIAL_ENTROPY_FLOATS))
                    .build(),
            )
            dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setClockUpdated(buildLocalCalendar(clock(), zone()))
                    .build(),
            )
            refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_HOME)
            refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_PROJECT)
            refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_NEW_SESSION)
            refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_SYNC_STATE)
            scope.launch(dispatcher) {
                for (item in mailbox) {
                    if (closed.get()) break
                    coalescingRenders = true
                    try {
                        item()
                        while (!closed.get()) {
                            val next = mailbox.tryReceive().getOrNull() ?: break
                            next()
                        }
                    } finally {
                        flushPendingRenders()
                        coalescingRenders = false
                    }
                }
            }
        }
    }

    fun dispatch(event: CoreEvent) {
        if (closed.get()) return
        mailbox.trySend { dispatchOnWorker(event) }
    }

    suspend fun dispatchAndWait(event: CoreEvent): EffectBatch {
        if (closed.get()) return EffectBatch.getDefaultInstance()
        val deferred = kotlinx.coroutines.CompletableDeferred<EffectBatch>()
        val result = mailbox.trySend {
            if (closed.get()) {
                deferred.complete(EffectBatch.getDefaultInstance())
            } else {
                try {
                    val batch = dispatchOnWorker(event)
                    flushPendingRenders()
                    deferred.complete(batch)
                } catch (t: Throwable) {
                    deferred.completeExceptionally(t)
                }
            }
        }
        if (result.isFailure) return EffectBatch.getDefaultInstance()
        return deferred.await()
    }

    fun dispatchOnWorker(event: CoreEvent): EffectBatch {
        val stamped = if (event.nowMs > 0L && event.monoMs > 0L) {
            event
        } else {
            val b = event.toBuilder()
            if (b.nowMs <= 0L) b.setNowMs(clock())
            if (b.monoMs <= 0L) b.setMonoMs(monotonic())
            b.build()
        }
        val batch = synchronized(coreLock) {
            if (closed.get()) return EffectBatch.getDefaultInstance()
            val rawEffects: ByteArray? = core.dispatch(stamped.toByteArray())
            if (rawEffects == null || rawEffects.isEmpty()) EffectBatch.getDefaultInstance()
            else EffectBatch.parseFrom(rawEffects)
        }
        for (effect in batch.effectsList) {
            handleEffectOnWorker(effect)
        }
        return batch
    }

    private fun flushPendingRenders() {
        if (pendingRenders.isEmpty() || closed.get()) {
            pendingRenders.clear()
            return
        }
        val targets = pendingRenders.toList()
        pendingRenders.clear()
        if (!testOverrideViews) {
            for (target in targets) refreshScreenOnWorker(target)
        }
        onRender(targets)
    }

    private fun handleEffectOnWorker(effect: Effect) {
        when (effect.effectCase) {
            Effect.EffectCase.RENDER -> {
                val targets = effect.render.screensList
                if (coalescingRenders) {
                    pendingRenders.addAll(targets)
                } else {
                    if (!testOverrideViews) {
                        for (target in targets) refreshScreenOnWorker(target)
                    }
                    onRender(targets)
                }
            }
            Effect.EffectCase.OPEN_STREAM -> onOpenStream(effect.openStream)
            Effect.EffectCase.CLOSE_STREAM -> onCloseStream(effect.closeStream)
            Effect.EffectCase.ENSURE_TOKEN -> onEnsureToken(effect.ensureToken)
            Effect.EffectCase.DISCARD_TOKEN -> onDiscardToken(effect.discardToken)
            Effect.EffectCase.SYNC_SECURITY -> onSyncSecurity(effect.syncSecurity)
            Effect.EffectCase.SCHEDULE_TIMER -> scheduleTimer(effect.scheduleTimer)
            Effect.EffectCase.CANCEL_TIMER -> cancelTimer(effect.cancelTimer)
            Effect.EffectCase.CACHE_READ -> onCacheRead(effect.cacheRead)
            Effect.EffectCase.CACHE_WRITE -> onCacheWrite(effect.cacheWrite)
            Effect.EffectCase.CACHE_DELETE -> onCacheDelete(effect.cacheDelete)
            Effect.EffectCase.RPC -> onRpc(effect.rpc)
            Effect.EffectCase.DISPATCH_COMMAND -> onDispatchCommand(effect.dispatchCommand)
            Effect.EffectCase.SAVE_HOME_SCOPE -> onSaveHomeScope(effect.saveHomeScope)
            Effect.EffectCase.NAVIGATE -> {
                _navigationEvents.tryEmit(effect.navigate)
                onNavigate(effect.navigate)
            }
            Effect.EffectCase.ATTACHMENTS_SETTLED -> {
                _attachmentsSettledEvents.tryEmit(effect.attachmentsSettled)
                onAttachmentsSettled(effect.attachmentsSettled)
            }
            Effect.EffectCase.REQUEST_ENTROPY -> {
                val req = effect.requestEntropy
                val ids = maxOf(req.minIds, INITIAL_ENTROPY_IDS / 2)
                val floats = maxOf(req.minFloats, INITIAL_ENTROPY_FLOATS / 2)
                dispatch(
                    CoreEvent.newBuilder()
                        .setEntropyProvided(buildEntropyProvided(ids, floats))
                        .build(),
                )
            }
            else -> {}
        }
    }

    private fun buildEntropyProvided(minIds: Int, minFloats: Int): EntropyProvided {
        val b = EntropyProvided.newBuilder()
        val idBytes = ByteArray(16)
        repeat(minIds.coerceAtLeast(0)) {
            random.nextBytes(idBytes)
            b.addRandomIds(idBytes.hex())
        }
        repeat(minFloats.coerceAtLeast(0)) {
            b.addUnitFloats(random.nextDouble())
        }
        return b.build()
    }

    private fun scheduleTimer(eff: ScheduleTimerEffect) {
        val id = eff.timerId
        val kind = eff.kind
        val key = eff.key
        val delayMs = eff.delayMs.coerceAtLeast(0L)
        lateinit var job: Job
        job = scope.launch {
            if (delayMs > 0L) delay(delayMs)
            if (!isActive || closed.get()) return@launch
            dispatchAction {
                val active = synchronized(timers) { (timers[id] === job).also { if (it) timers.remove(id) } }
                if (active) sendOnWorker { setTimerFired(TimerFiredEvent.newBuilder().setTimerId(id).setKind(kind).setKey(key)) }
            }
        }
        synchronized(timers) {
            timers.remove(id)?.cancel()
            timers[id] = job
        }
    }

    private fun cancelTimer(eff: CancelTimerEffect) {
        synchronized(timers) {
            timers.remove(eff.timerId)?.cancel()
        }
    }

    fun refreshScreenOnWorker(target: ScreenTarget) {
        if (closed.get() || testOverrideViews) return
        if (target == ScreenTarget.SCREEN_TARGET_SESSION_DATA) {
            val activeIds = synchronized(sessionWatchers) { sessionWatchers.keys.toList() }
            for (sid in activeIds) {
                refreshSessionDataOnWorker(sid)
            }
            return
        }
        val screen = when (target) {
            ScreenTarget.SCREEN_TARGET_HOME ->
                Screen.newBuilder().setHome(HomeScreenRequest.newBuilder().setEndedLimit(homeEndedLimit)).build()
            ScreenTarget.SCREEN_TARGET_PROJECT ->
                Screen.newBuilder().setProject(
                    ProjectScreenRequest.newBuilder()
                        .setProjectKey(projectKey)
                        .setManage(projectManage)
                        .setEndedLimit(projectEndedLimit),
                ).build()
            ScreenTarget.SCREEN_TARGET_NEW_SESSION ->
                Screen.newBuilder().setNewSession(
                    NewSessionScreenRequest.newBuilder().setProjectKey(newSessionProjectKey),
                ).build()
            ScreenTarget.SCREEN_TARGET_SYNC_STATE ->
                Screen.newBuilder().setSyncState(SyncStateScreenRequest.getDefaultInstance()).build()
            else -> return
        }
        val vm = viewOnWorker(screen) ?: return
        when (vm.viewCase) {
            ViewModel.ViewCase.HOME -> _homeView.value = vm.home
            ViewModel.ViewCase.PROJECT -> _projectView.value = vm.project
            ViewModel.ViewCase.NEW_SESSION -> _newSessionView.value = vm.newSession
            ViewModel.ViewCase.SYNC_STATE -> _syncStateView.value = vm.syncState
            else -> {}
        }
    }

    private fun refreshSessionDataOnWorker(sessionId: String) {
        if (sessionId.isEmpty() || closed.get() || testOverrideViews) return
        val watcher = synchronized(sessionWatchers) { sessionWatchers[sessionId] } ?: return
        val screen = Screen.newBuilder()
            .setSessionData(SessionDataScreenRequest.newBuilder().setSessionId(sessionId))
            .build()
        val vm = viewOnWorker(screen) ?: return
        if (vm.viewCase == ViewModel.ViewCase.SESSION_DATA) {
            watcher.flow.value = vm.sessionData
        }
    }

    private fun viewOnWorker(screen: Screen): ViewModel? {
        val rawVm: ByteArray? = synchronized(coreLock) {
            if (closed.get() || testOverrideViews) return null
            core.view(screen.toByteArray())
        }
        if (rawVm == null || rawVm.isEmpty()) return null
        return ViewModel.parseFrom(rawVm)
    }

    fun watchSession(sessionId: String): Flow<SessionDataView> = flow {
        val watcher = acquireSessionWatcher(sessionId)
        try {
            watcher.flow.collect { emit(it) }
        } finally {
            releaseSessionWatcher(sessionId)
        }
    }

    fun currentSessionData(sessionId: String): SessionDataView? =
        synchronized(sessionWatchers) { sessionWatchers[sessionId]?.flow?.value }

    private fun acquireSessionWatcher(sessionId: String): SessionWatcher {
        val (watcher, becameActive) = synchronized(sessionWatchers) {
            val existing = sessionWatchers[sessionId]
            if (existing != null) {
                existing.refCount++
                existing to false
            } else {
                val created = SessionWatcher(refCount = 1)
                sessionWatchers[sessionId] = created
                created to true
            }
        }
        if (becameActive) dispatchAction {
            sendOnWorker { setSessionWatch(SessionWatchEvent.newBuilder().setSessionId(sessionId).setWatched(true)) }
            refreshSessionDataOnWorker(sessionId)
        }
        return watcher
    }

    private fun releaseSessionWatcher(sessionId: String) {
        val becameIdle = synchronized(sessionWatchers) {
            val existing = sessionWatchers[sessionId] ?: return
            existing.refCount--
            if (existing.refCount <= 0) {
                sessionWatchers.remove(sessionId)
                true
            } else {
                false
            }
        }
        if (becameIdle) send { setSessionWatch(SessionWatchEvent.newBuilder().setSessionId(sessionId).setWatched(false)) }
    }

    private inline fun send(block: CoreEvent.Builder.() -> Unit) = dispatch(CoreEvent.newBuilder().apply(block).build())

    private inline fun sendOnWorker(block: CoreEvent.Builder.() -> Unit) {
        dispatchOnWorker(CoreEvent.newBuilder().apply(block).build())
    }

    // --- Sync lifecycle & environment events ---

    fun syncLifecycle(kind: SyncLifecycleKind, homeScopeToken: String = "", scopeHmacKey: ByteArray? = null) = send {
        val act = SyncLifecycleAction.newBuilder().setKind(kind)
        if (homeScopeToken.isNotEmpty()) act.setHomeScopeToken(homeScopeToken)
        if (scopeHmacKey != null && scopeHmacKey.isNotEmpty()) act.setScopeHmacKey(ByteString.copyFrom(scopeHmacKey))
        setSyncLifecycle(act)
    }

    fun networkChanged(available: Boolean, signature: String) =
        send { setNetworkChanged(NetworkChangedEvent.newBuilder().setAvailable(available).setSignature(signature)) }

    fun retrySessionEvents(sessionId: String) =
        send { setRetrySessionEvents(RetrySessionEventsAction.newBuilder().setSessionId(sessionId)) }

    fun fetchSession(sessionId: String) =
        send { setFetchSession(FetchSessionAction.newBuilder().setSessionId(sessionId)) }

    fun notifyMachineRouteStatus(machineId: String, directReady: Boolean) =
        send { setMachineRouteStatus(MachineRouteStatusEvent.newBuilder().setMachineId(machineId).setDirectReady(directReady)) }

    fun updateScheduleHints(hints: List<SessionScheduleHint>) =
        send { setSessionScheduleHints(SessionScheduleHintsEvent.newBuilder().addAllHints(hints)) }

    fun pinSession(sessionId: String, pinned: Boolean) =
        send { setSessionPin(SessionPinEvent.newBuilder().setSessionId(sessionId).setPinned(pinned)) }

    // --- Screen actions ---

    fun setHomeScope(scopeKey: String) = dispatchAction {
        homeEndedLimit = CORE_ENDED_PAGE
        sendOnWorker { setHomeSetScope(HomeSetScopeAction.newBuilder().setScopeKey(scopeKey)) }
    }

    fun setHomeFilter(query: String) =
        send { setHomeSetFilter(HomeSetFilterAction.newBuilder().setQuery(query)) }

    fun loadMoreHomeEnded(step: Int = CORE_ENDED_PAGE) = dispatchAction {
        homeEndedLimit += step
        sendOnWorker { setHomeLoadMoreEnded(HomeLoadMoreEndedAction.newBuilder().setStep(step)) }
    }

    fun retryHomeLoad() = send { setHomeRetryLoad(HomeRetryLoadAction.getDefaultInstance()) }

    fun selectHomeSession(sessionId: String) =
        send { setHomeSelectSession(HomeSelectSessionAction.newBuilder().setSessionId(sessionId)) }

    fun openManageProjects() = dispatchAction {
        projectKey = ""
        projectManage = true
        refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_PROJECT)
    }

    fun loadMoreProjectEnded(step: Int = CORE_ENDED_PAGE) = dispatchAction {
        projectEndedLimit += step
        sendOnWorker { setProjectLoadMoreEnded(ProjectKeyAction.newBuilder().setProjectKey(projectKey)) }
    }

    fun setProjectSearch(query: String) =
        send { setProjectSetSearch(ProjectSetSearchAction.newBuilder().setQuery(query)) }

    fun toggleProjectPin(key: String) =
        send { setProjectTogglePin(ProjectKeyAction.newBuilder().setProjectKey(key)) }

    fun toggleProjectHide(key: String) =
        send { setProjectToggleHide(ProjectKeyAction.newBuilder().setProjectKey(key)) }

    fun addProject(path: String) = dispatchAction {
        newSessionProjectKey = path
        sendOnWorker { setProjectAdd(ProjectAddAction.newBuilder().setPath(path)) }
    }

    fun openDirPicker(machineId: String = "", path: String = "", forNewTask: Boolean = false) =
        send { setProjectOpenDirPicker(DirPickerOpenAction.newBuilder().setMachineId(machineId).setPath(path).setForNewTask(forNewTask)) }

    fun navigateDirPicker(machineId: String = "", path: String = "") =
        send { setProjectDirNavigate(DirNavigateAction.newBuilder().setMachineId(machineId).setPath(path)) }

    fun closeDirPicker() = send { setProjectCloseDirPicker(DirPickerCloseAction.getDefaultInstance()) }

    fun openNewSession(projectKey: String) = dispatchAction {
        if (projectKey.isNotEmpty()) newSessionProjectKey = projectKey
        sendOnWorker { setNewSessionOpen(NewSessionOpenAction.newBuilder().setProjectKey(projectKey)) }
    }

    fun selectNewSessionProject(projectKey: String) = dispatchAction {
        if (projectKey.isNotEmpty()) newSessionProjectKey = projectKey
        sendOnWorker { setNewSessionSelectProject(NewSessionSelectProjectAction.newBuilder().setProjectKey(projectKey)) }
    }

    fun selectNewSessionMachine(machineId: String) =
        send { setNewSessionSelectMachine(NewSessionSelectMachineAction.newBuilder().setMachineId(machineId)) }

    fun selectNewSessionAgent(agent: Agent) =
        send { setNewSessionSelectAgent(NewSessionSelectAgentAction.newBuilder().setAgent(agent)) }

    fun selectNewSessionModel(model: String, effort: String, standardContext: Boolean? = null, mode: ApprovalMode? = null) = send {
        val b = NewSessionSelectModelAction.newBuilder().setModel(model).setEffort(effort)
        standardContext?.let { b.setStandardContext(it) }
        mode?.let { b.setMode(it) }
        setNewSessionSelectModel(b)
    }

    fun setNewSessionPrompt(prompt: String, attachmentCount: Int = 0, attachmentsMoved: Boolean = false) = send {
        setNewSessionSetPrompt(
            NewSessionSetPromptAction.newBuilder()
                .setPrompt(prompt)
                .setAttachmentCount(attachmentCount)
                .setAttachmentsMoved(attachmentsMoved),
        )
    }

    fun retryNewSessionModels(projectKey: String = "") =
        send { setNewSessionRetryModels(NewSessionProjectAction.newBuilder().setProjectKey(projectKey)) }

    fun retryNewSessionSaveConfig(projectKey: String = "") =
        send { setNewSessionRetrySaveConfig(NewSessionProjectAction.newBuilder().setProjectKey(projectKey)) }

    fun submitNewSession(
        projectKey: String = "",
        autoModeSupported: Boolean = false,
        imageIds: List<String> = emptyList(),
        fileIds: List<String> = emptyList(),
    ) = send {
        setNewSessionSubmit(
            NewSessionSubmitAction.newBuilder()
                .setProjectKey(projectKey)
                .setAutoModeSupported(autoModeSupported)
                .addAllImageIds(imageIds)
                .addAllFileIds(fileIds),
        )
    }

    fun abandonNewSessionStart(projectKey: String = "") =
        send { setNewSessionAbandonStart(NewSessionProjectAction.newBuilder().setProjectKey(projectKey)) }

    fun tickClock(nowMs: Long = clock()) =
        send { setClockUpdated(buildLocalCalendar(nowMs, zone())) }

    fun showViewsForTest(
        home: HomeViewModel? = null,
        project: ProjectViewModel? = null,
        newSession: NewSessionViewModel? = null,
        syncState: SyncStateView? = null,
    ) {
        testOverrideViews = true
        home?.let { _homeView.value = it }
        project?.let { _projectView.value = it }
        newSession?.let { _newSessionView.value = it }
        syncState?.let { _syncStateView.value = it }
    }

    private inline fun dispatchAction(crossinline block: () -> Unit) {
        if (closed.get()) return
        mailbox.trySend {
            if (!closed.get()) block()
        }
    }

    fun close() {
        if (closed.compareAndSet(false, true)) {
            synchronized(timers) {
                timers.values.forEach { it.cancel() }
                timers.clear()
            }
            mailbox.close()
            if (ownsScope) scope.cancel()
            synchronized(coreLock) { core.close() }
        }
    }
}
