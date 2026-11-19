package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.core.transport.DirectDiag
import io.github.lewin671.remoteagent.core.transport.DirectFailReason
import io.github.lewin671.remoteagent.core.transport.DirectStage
import io.github.lewin671.remoteagent.core.transport.DirectStatus
import io.github.lewin671.remoteagent.corebind.Core
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.UsageRangePreset
import io.github.lewin671.remoteagent.proto.UsageStatsResponse
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.*
import java.security.SecureRandom
import java.time.Instant
import java.time.ZoneId
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.math.roundToInt

const val CORE_ENDED_PAGE = 30
const val CORE_SESSION_WINDOW_LIMIT = 80
private val DEFAULT_SESSION_WINDOW: SessionWindowRequest =
    SessionWindowRequest.newBuilder().setLimit(CORE_SESSION_WINDOW_LIMIT).build()
private const val CALENDAR_MONTH_HISTORY = 35
private const val INITIAL_ENTROPY_IDS = 32
private const val INITIAL_ENTROPY_FLOATS = 32

fun uiMessage(key: String, args: Map<String, Any> = emptyMap()): UiMessage = UiMessage(key, args)

interface HomeScopeStore {
    fun readToken(): String?
    fun writeToken(token: String)
    fun clear()
}

interface ScheduleSeenStore {
    fun load(machineId: String): List<ScheduleSeenToken>?
    fun save(machineId: String, tokens: List<ScheduleSeenToken>)
    fun delete(machineId: String)
}

fun buildScheduleLocalContext(nowMs: Long, zone: ZoneId = ZoneId.systemDefault()): ScheduleLocalContextEvent {
    val today = Instant.ofEpochMilli(nowMs).atZone(zone).toLocalDate()
    val tomorrowStartMs = today.plusDays(1).atStartOfDay(zone).toInstant().toEpochMilli()
    val dayAfterTomorrowStartMs = today.plusDays(2).atStartOfDay(zone).toInstant().toEpochMilli()
    return ScheduleLocalContextEvent.newBuilder()
        .setLocalTimezone(zone.id)
        .setTomorrowStartMs(tomorrowStartMs)
        .setDayAfterTomorrowStartMs(dayAfterTomorrowStartMs)
        .build()
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
    private val machinePollTimers = HashMap<String, Job>()

    private class KeyedWatcher<V, K>(
        val flow: MutableStateFlow<V>,
        var meta: K,
        var refCount: Int = 0,
    )

    private class ScreenSlot<V>(
        initial: V,
        val build: () -> Screen?,
        private val extract: (ViewModel) -> V?,
    ) {
        val flow = MutableStateFlow(initial)
        fun update(vm: ViewModel) { extract(vm)?.let { flow.value = it } }
    }

    private val screenSlots = LinkedHashMap<ScreenTarget, ScreenSlot<*>>()
    private fun <V> registerScreen(
        target: ScreenTarget,
        initial: V,
        build: () -> Screen?,
        extract: (ViewModel) -> V?,
    ): ScreenSlot<V> = ScreenSlot(initial, build, extract).also { screenSlots[target] = it }

    private val sessionWatchers = LinkedHashMap<String, KeyedWatcher<SessionViewModel, SessionWindowRequest>>()
    private val testSessionViews = HashMap<String, SessionViewModel>()
    private val scheduleStripWatchers = HashMap<String, KeyedWatcher<ScheduleSessionStripViewModel, Unit>>()
    private val testScheduleStripViews = HashMap<String, ScheduleSessionStripViewModel>()

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
    var onDirectPreference: (DirectPreferenceEffect) -> Unit = {}
    var onRenameClient: (RenameClientEffect) -> Unit = {}
    var onCheckUpdates: (CheckUpdatesEffect) -> Unit = {}
    var onPersistClientPrefs: (PersistClientPrefsEffect) -> Unit = {}
    var onWatchedSessionsChanged: (List<String>) -> Unit = {}

    fun watchedSessionIds(): List<String> = synchronized(sessionWatchers) {
        sessionWatchers.keys.toList().reversed()
    }
    var onPrepareCleanupSession: (PrepareCleanupSessionEffect) -> Unit = {}
    var onScheduleReadCache: (ScheduleReadCacheEffect) -> Unit = {}
    var onScheduleDeleteCache: (ScheduleDeleteCacheEffect) -> Unit = {}
    var onScheduleLoadSeen: (ScheduleLoadSeenEffect) -> Unit = {}
    var onScheduleSaveSeen: (ScheduleSaveSeenEffect) -> Unit = {}
    var onScheduleDismissNotifications: (ScheduleDismissNotificationsEffect) -> Unit = {}
    var onScheduleNavigate: (ScheduleNavigateEffect) -> Unit = {}
    var onRender: (List<ScreenTarget>) -> Unit = {}
    var onUsageCacheWrite: (UsageCacheWriteEffect) -> Unit = {}
    var onHydrateUsageCache: (String) -> Unit = {}

    private var homeEndedLimit: Int = CORE_ENDED_PAGE
    private var projectKey: String = ""
    private var projectManage: Boolean = true
    private var projectMachineId: String = ""
    private var projectEndedLimit: Int = CORE_ENDED_PAGE
    private var newSessionProjectKey: String = ""
    private var machineStatusId: String = ""
    private var machineSettingsId: String = ""
    private var quotaMachineId: String = ""
    private var quotaFilterAgent: Agent = Agent.AGENT_UNSPECIFIED
    private var clientDetailId: String = ""
    private var usageMachineId: String = ""
    private var usageFocusAgent: Agent = Agent.AGENT_UNSPECIFIED
    private var usageFocusModel: String = ""
    private var usagePreset: UsageRangePreset = UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS
    private var usageCustomStartMs: Long = 0L
    private var usageCustomEndMs: Long = 0L
    private var usageMetric: UsageMetricTab = UsageMetricTab.USAGE_METRIC_TAB_TOKENS
    private var scheduleDetailMachineId: String = ""
    private var scheduleDetailTaskId: String = ""
    private var scheduleFormKey: String = ""
    private var testOverrideViews = false
    private val testOverriddenTargets = java.util.Collections.synchronizedSet(HashSet<ScreenTarget>())

    private val homeSlot = registerScreen(
        ScreenTarget.SCREEN_TARGET_HOME, HomeViewModel.getDefaultInstance(),
        { Screen.newBuilder().setHome(HomeScreenRequest.newBuilder().setEndedLimit(homeEndedLimit)).build() },
        { if (it.viewCase == ViewModel.ViewCase.HOME) it.home else null },
    )
    val homeView: StateFlow<HomeViewModel> = homeSlot.flow.asStateFlow()

    private val projectSlot = registerScreen(
        ScreenTarget.SCREEN_TARGET_PROJECT, ProjectViewModel.getDefaultInstance(),
        {
            Screen.newBuilder().setProject(
                ProjectScreenRequest.newBuilder()
                    .setProjectKey(projectKey)
                    .setManage(projectManage)
                    .setEndedLimit(projectEndedLimit)
                    .setMachineId(projectMachineId),
            ).build()
        },
        { if (it.viewCase == ViewModel.ViewCase.PROJECT) it.project else null },
    )
    val projectView: StateFlow<ProjectViewModel> = projectSlot.flow.asStateFlow()

    private val newSessionSlot = registerScreen(
        ScreenTarget.SCREEN_TARGET_NEW_SESSION, NewSessionViewModel.getDefaultInstance(),
        { Screen.newBuilder().setNewSession(NewSessionScreenRequest.newBuilder().setProjectKey(newSessionProjectKey)).build() },
        { if (it.viewCase == ViewModel.ViewCase.NEW_SESSION) it.newSession else null },
    )
    val newSessionView: StateFlow<NewSessionViewModel> = newSessionSlot.flow.asStateFlow()

    private val syncStateSlot = registerScreen(
        ScreenTarget.SCREEN_TARGET_SYNC_STATE, SyncStateView.getDefaultInstance(),
        { Screen.newBuilder().setSyncState(SyncStateScreenRequest.getDefaultInstance()).build() },
        { if (it.viewCase == ViewModel.ViewCase.SYNC_STATE) it.syncState else null },
    )
    val syncStateView: StateFlow<SyncStateView> = syncStateSlot.flow.asStateFlow()

    private val settingsSlot = registerScreen(
        ScreenTarget.SCREEN_TARGET_SETTINGS, SettingsViewModel.getDefaultInstance(),
        { Screen.newBuilder().setSettings(SettingsScreenRequest.getDefaultInstance()).build() },
        { if (it.viewCase == ViewModel.ViewCase.SETTINGS) it.settings else null },
    )
    val settingsView: StateFlow<SettingsViewModel> = settingsSlot.flow.asStateFlow()

    private val machineStatusSlot = registerScreen(
        ScreenTarget.SCREEN_TARGET_MACHINE_STATUS, MachineStatusViewModel.getDefaultInstance(),
        { machineStatusId.takeIf { it.isNotEmpty() }?.let { Screen.newBuilder().setMachineStatus(MachineStatusScreenRequest.newBuilder().setMachineId(it)).build() } },
        { if (it.viewCase == ViewModel.ViewCase.MACHINE_STATUS) it.machineStatus else null },
    )
    val machineStatusView: StateFlow<MachineStatusViewModel> = machineStatusSlot.flow.asStateFlow()

    private val machineSettingsSlot = registerScreen(
        ScreenTarget.SCREEN_TARGET_MACHINE_SETTINGS, MachineSettingsViewModel.getDefaultInstance(),
        { machineSettingsId.takeIf { it.isNotEmpty() }?.let { Screen.newBuilder().setMachineSettings(MachineSettingsScreenRequest.newBuilder().setMachineId(it)).build() } },
        { if (it.viewCase == ViewModel.ViewCase.MACHINE_SETTINGS) it.machineSettings else null },
    )
    val machineSettingsView: StateFlow<MachineSettingsViewModel> = machineSettingsSlot.flow.asStateFlow()

    private val quotaSlot = registerScreen(
        ScreenTarget.SCREEN_TARGET_QUOTA, QuotaViewModel.getDefaultInstance(),
        { Screen.newBuilder().setQuota(QuotaScreenRequest.newBuilder().setMachineId(quotaMachineId).setFilterAgent(quotaFilterAgent)).build() },
        { if (it.viewCase == ViewModel.ViewCase.QUOTA) it.quota else null },
    )
    val quotaView: StateFlow<QuotaViewModel> = quotaSlot.flow.asStateFlow()

    private val devicesSlot = registerScreen(
        ScreenTarget.SCREEN_TARGET_DEVICES, DevicesViewModel.getDefaultInstance(),
        { Screen.newBuilder().setDevices(DevicesScreenRequest.getDefaultInstance()).build() },
        { if (it.viewCase == ViewModel.ViewCase.DEVICES) it.devices else null },
    )
    val devicesView: StateFlow<DevicesViewModel> = devicesSlot.flow.asStateFlow()

    private val clientDetailSlot = registerScreen(
        ScreenTarget.SCREEN_TARGET_CLIENT_DETAIL, ClientDetailViewModel.getDefaultInstance(),
        { clientDetailId.takeIf { it.isNotEmpty() }?.let { Screen.newBuilder().setClientDetail(ClientDetailScreenRequest.newBuilder().setDeviceId(it)).build() } },
        { if (it.viewCase == ViewModel.ViewCase.CLIENT_DETAIL) it.clientDetail else null },
    )
    val clientDetailView: StateFlow<ClientDetailViewModel> = clientDetailSlot.flow.asStateFlow()

    private val usageSlot = registerScreen(
        ScreenTarget.SCREEN_TARGET_USAGE, UsageViewModel.getDefaultInstance(),
        {
            usageMachineId.takeIf { it.isNotEmpty() }?.let { mid ->
                Screen.newBuilder().setUsage(
                    UsageScreenRequest.newBuilder()
                        .setMachineId(mid)
                        .setFocusAgent(usageFocusAgent)
                        .setFocusModel(usageFocusModel)
                        .setPreset(usagePreset)
                        .setCustomStartMs(usageCustomStartMs)
                        .setCustomEndMs(usageCustomEndMs)
                        .setMetric(usageMetric),
                ).build()
            }
        },
        { if (it.viewCase == ViewModel.ViewCase.USAGE) it.usage else null },
    )
    val usageView: StateFlow<UsageViewModel> = usageSlot.flow.asStateFlow()

    private val schedulesSlot = registerScreen(
        ScreenTarget.SCREEN_TARGET_SCHEDULES, SchedulesViewModel.getDefaultInstance(),
        { Screen.newBuilder().setSchedules(SchedulesScreenRequest.getDefaultInstance()).build() },
        { if (it.viewCase == ViewModel.ViewCase.SCHEDULES) it.schedules else null },
    )
    val schedulesView: StateFlow<SchedulesViewModel> = schedulesSlot.flow.asStateFlow()

    private val scheduleDetailSlot = registerScreen(
        ScreenTarget.SCREEN_TARGET_SCHEDULE_DETAIL, ScheduleDetailViewModel.getDefaultInstance(),
        {
            if (scheduleDetailMachineId.isEmpty() || scheduleDetailTaskId.isEmpty()) null
            else Screen.newBuilder().setScheduleDetail(
                ScheduleDetailScreenRequest.newBuilder().setMachineId(scheduleDetailMachineId).setTaskId(scheduleDetailTaskId),
            ).build()
        },
        { if (it.viewCase == ViewModel.ViewCase.SCHEDULE_DETAIL) it.scheduleDetail else null },
    )
    val scheduleDetailView: StateFlow<ScheduleDetailViewModel> = scheduleDetailSlot.flow.asStateFlow()

    private val scheduleFormSlot = registerScreen(
        ScreenTarget.SCREEN_TARGET_SCHEDULE_FORM, ScheduleFormViewModel.getDefaultInstance(),
        { Screen.newBuilder().setScheduleForm(ScheduleFormScreenRequest.newBuilder().setFormKey(scheduleFormKey)).build() },
        { if (it.viewCase == ViewModel.ViewCase.SCHEDULE_FORM) it.scheduleForm else null },
    )
    val scheduleFormView: StateFlow<ScheduleFormViewModel> = scheduleFormSlot.flow.asStateFlow()

    private val _navigationEvents = MutableSharedFlow<NavigateEffect>(extraBufferCapacity = 16)
    val navigationEvents: SharedFlow<NavigateEffect> = _navigationEvents.asSharedFlow()

    private val _scheduleNavigationEvents = MutableSharedFlow<ScheduleNavigateEffect>(extraBufferCapacity = 16)
    val scheduleNavigationEvents: SharedFlow<ScheduleNavigateEffect> = _scheduleNavigationEvents.asSharedFlow()

    private val _attachmentsSettledEvents = MutableSharedFlow<AttachmentsSettledEffect>(extraBufferCapacity = 16)
    val attachmentsSettledEvents: SharedFlow<AttachmentsSettledEffect> = _attachmentsSettledEvents.asSharedFlow()

    private val _renameClientEvents = MutableSharedFlow<RenameClientEffect>(extraBufferCapacity = 8)
    val renameClientEvents: SharedFlow<RenameClientEffect> = _renameClientEvents.asSharedFlow()

    private val _prepareCleanupEvents = MutableSharedFlow<PrepareCleanupSessionEffect>(extraBufferCapacity = 8)
    val prepareCleanupEvents: SharedFlow<PrepareCleanupSessionEffect> = _prepareCleanupEvents.asSharedFlow()

    private val _restoreComposerEvents = MutableSharedFlow<SessionRestoreComposerEffect>(extraBufferCapacity = 16)
    val restoreComposerEvents: SharedFlow<SessionRestoreComposerEffect> = _restoreComposerEvents.asSharedFlow()

    init {
        if (!closed.get()) {
            // Queue initial calendar, entropy pool, and default view models onto the serial mailbox
            // before starting the worker loop so initialization never runs on the caller thread.
            mailbox.trySend {
                if (!closed.get()) {
                    dispatchOnWorker(
                        CoreEvent.newBuilder()
                            .setEntropyProvided(buildEntropyProvided(INITIAL_ENTROPY_IDS, INITIAL_ENTROPY_FLOATS))
                            .build(),
                    )
                    val nowMs = clock()
                    val tz = zone()
                    dispatchOnWorker(
                        CoreEvent.newBuilder()
                            .setClockUpdated(buildLocalCalendar(nowMs, tz))
                            .build(),
                    )
                    dispatchOnWorker(
                        CoreEvent.newBuilder()
                            .setScheduleLocalContext(buildScheduleLocalContext(nowMs, tz))
                            .build(),
                    )
                    refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_HOME)
                    refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_PROJECT)
                    refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_NEW_SESSION)
                    refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_SYNC_STATE)
                    refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_SCHEDULES)
                }
            }
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

    fun dispatch(event: CoreEvent, onProcessed: (() -> Unit)? = null) {
        if (closed.get()) {
            onProcessed?.invoke()
            return
        }
        val res = mailbox.trySend {
            try {
                dispatchOnWorker(event)
            } finally {
                onProcessed?.invoke()
            }
        }
        if (res.isFailure) onProcessed?.invoke()
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
        val targets = synchronized(pendingRenders) {
            if (pendingRenders.isEmpty() || closed.get()) {
                pendingRenders.clear()
                return
            }
            pendingRenders.toList().also { pendingRenders.clear() }
        }
        if (!testOverrideViews) {
            for (target in targets) refreshScreenOnWorker(target)
        }
        onRender(targets)
    }

    private fun handleEffectOnWorker(effect: Effect) {
        when (effect.effectCase) {
            Effect.EffectCase.RENDER -> {
                val targets = effect.render.screensList.map {
                    if (it == ScreenTarget.SCREEN_TARGET_SESSION_DATA) ScreenTarget.SCREEN_TARGET_SESSION else it
                }.distinct()
                if (coalescingRenders) {
                    synchronized(pendingRenders) {
                        pendingRenders.addAll(targets)
                    }
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
            Effect.EffectCase.SCHEDULE_MACHINE_POLL -> scheduleMachinePoll(effect.scheduleMachinePoll)
            Effect.EffectCase.DIRECT_PREFERENCE -> onDirectPreference(effect.directPreference)
            Effect.EffectCase.RENAME_CLIENT -> {
                _renameClientEvents.tryEmit(effect.renameClient)
                onRenameClient(effect.renameClient)
            }
            Effect.EffectCase.CHECK_UPDATES -> onCheckUpdates(effect.checkUpdates)
            Effect.EffectCase.PERSIST_CLIENT_PREFS -> onPersistClientPrefs(effect.persistClientPrefs)
            Effect.EffectCase.PREPARE_CLEANUP_SESSION -> {
                _prepareCleanupEvents.tryEmit(effect.prepareCleanupSession)
                onPrepareCleanupSession(effect.prepareCleanupSession)
            }
            Effect.EffectCase.SESSION_RESTORE_COMPOSER -> {
                _restoreComposerEvents.tryEmit(effect.sessionRestoreComposer)
            }
            Effect.EffectCase.SCHEDULE_READ_CACHE -> onScheduleReadCache(effect.scheduleReadCache)
            Effect.EffectCase.SCHEDULE_DELETE_CACHE -> onScheduleDeleteCache(effect.scheduleDeleteCache)
            Effect.EffectCase.SCHEDULE_LOAD_SEEN -> onScheduleLoadSeen(effect.scheduleLoadSeen)
            Effect.EffectCase.SCHEDULE_SAVE_SEEN -> onScheduleSaveSeen(effect.scheduleSaveSeen)
            Effect.EffectCase.SCHEDULE_DISMISS_NOTIFICATIONS ->
                onScheduleDismissNotifications(effect.scheduleDismissNotifications)
            Effect.EffectCase.SCHEDULE_NAVIGATE -> {
                val nav = effect.scheduleNavigate
                if (nav.route == ScheduleNavigateRoute.SCHEDULE_NAVIGATE_ROUTE_DETAIL &&
                    nav.machineId.isNotEmpty() && nav.taskId.isNotEmpty()
                ) {
                    scheduleDetailMachineId = nav.machineId
                    scheduleDetailTaskId = nav.taskId
                }
                _scheduleNavigationEvents.tryEmit(nav)
                onScheduleNavigate(nav)
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
            Effect.EffectCase.USAGE_CACHE_WRITE -> {
                onUsageCacheWrite(effect.usageCacheWrite)
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

    private fun scheduleMachinePoll(eff: ScheduleMachinePollEffect) {
        val id = eff.machineId
        if (eff.cancel || id.isEmpty()) {
            synchronized(machinePollTimers) {
                machinePollTimers.remove(id)?.cancel()
            }
            return
        }
        val gen = eff.generation
        val delayMs = eff.delayMs.coerceAtLeast(0L)
        lateinit var job: Job
        job = scope.launch {
            if (delayMs > 0L) delay(delayMs)
            if (!isActive || closed.get()) return@launch
            dispatchAction {
                val active = synchronized(machinePollTimers) {
                    (machinePollTimers[id] === job).also { if (it) machinePollTimers.remove(id) }
                }
                if (active) {
                    sendOnWorker {
                        setMachineStatusPollTick(
                            MachineStatusPollTickEvent.newBuilder()
                                .setMachineId(id)
                                .setGeneration(gen),
                        )
                    }
                }
            }
        }
        synchronized(machinePollTimers) {
            machinePollTimers.remove(id)?.cancel()
            machinePollTimers[id] = job
        }
    }

    fun refreshScreenOnWorker(target: ScreenTarget) {
        if (closed.get() || testOverrideViews || target in testOverriddenTargets) return
        when (target) {
            ScreenTarget.SCREEN_TARGET_SESSION -> refreshAllKeyed(sessionWatchers, ::refreshSessionOnWorker)
            ScreenTarget.SCREEN_TARGET_SCHEDULE_SESSION_STRIP -> refreshAllKeyed(scheduleStripWatchers, ::refreshScheduleStripOnWorker)
            else -> {
                val slot = screenSlots[target] ?: return
                val screen = slot.build() ?: return
                val vm = viewOnWorker(screen) ?: return
                slot.update(vm)
            }
        }
    }

    private fun <V, K> refreshAllKeyed(
        watchers: HashMap<String, KeyedWatcher<V, K>>,
        refresh: (String) -> Unit,
    ) {
        for (id in synchronized(watchers) { watchers.keys.toList() }) refresh(id)
    }

    private fun refreshScheduleStripOnWorker(sessionId: String) {
        if (sessionId.isEmpty() || closed.get() || testOverrideViews) return
        if (synchronized(scheduleStripWatchers) { sessionId in testScheduleStripViews }) return
        val watcher = synchronized(scheduleStripWatchers) { scheduleStripWatchers[sessionId] ?: return }
        val screen = Screen.newBuilder().setScheduleSessionStrip(ScheduleSessionStripScreenRequest.newBuilder().setSessionId(sessionId)).build()
        val vm = viewOnWorker(screen) ?: return
        if (vm.viewCase == ViewModel.ViewCase.SCHEDULE_SESSION_STRIP) watcher.flow.value = vm.scheduleSessionStrip
    }

    private fun refreshSessionOnWorker(sessionId: String) {
        if (sessionId.isEmpty() || closed.get() || testOverrideViews) return
        if (synchronized(sessionWatchers) { sessionId in testSessionViews }) return
        val (watcher, window) = synchronized(sessionWatchers) {
            val w = sessionWatchers[sessionId] ?: return
            w to w.meta
        }
        val screen = Screen.newBuilder()
            .setSession(SessionScreenRequest.newBuilder().setSessionId(sessionId).setWindow(window))
            .build()
        val vm = viewOnWorker(screen) ?: return
        if (vm.viewCase == ViewModel.ViewCase.SESSION) watcher.flow.value = vm.session
    }

    private fun viewOnWorker(screen: Screen): ViewModel? {
        val rawVm: ByteArray? = synchronized(coreLock) {
            if (closed.get() || testOverrideViews) return null
            core.view(screen.toByteArray())
        }
        if (rawVm == null || rawVm.isEmpty()) return null
        return ViewModel.parseFrom(rawVm)
    }

    private class KeyedWatchFlow<V, K>(
        private val lock: Any,
        private val key: Any,
        private val acquire: () -> KeyedWatcher<V, K>,
        private val release: () -> Unit,
    ) : Flow<V> {
        override suspend fun collect(collector: FlowCollector<V>) {
            val watcher = acquire()
            try {
                watcher.flow.collect { collector.emit(it) }
            } finally {
                release()
            }
        }

        override fun equals(other: Any?): Boolean =
            other is KeyedWatchFlow<*, *> && lock === other.lock && key == other.key

        override fun hashCode(): Int = 31 * System.identityHashCode(lock) + key.hashCode()
    }

    fun watchSession(sessionId: String, initialWindow: SessionWindowRequest? = null): Flow<SessionViewModel> =
        KeyedWatchFlow(sessionWatchers, sessionId to initialWindow, { acquireSessionWatcher(sessionId, initialWindow) }) {
            releaseSessionWatcher(sessionId)
        }

    fun watchScheduleSessionStrip(sessionId: String): Flow<ScheduleSessionStripViewModel> =
        KeyedWatchFlow(scheduleStripWatchers, sessionId, { acquireScheduleStripWatcher(sessionId) }) {
            synchronized(scheduleStripWatchers) {
                val existing = scheduleStripWatchers[sessionId] ?: return@synchronized
                if (--existing.refCount <= 0) scheduleStripWatchers.remove(sessionId)
            }
        }

    fun updateSessionWindow(sessionId: String, window: SessionWindowRequest) {
        val updated = synchronized(sessionWatchers) {
            val w = sessionWatchers[sessionId] ?: return@synchronized false
            if (w.meta == window) return@synchronized false
            w.meta = window
            true
        }
        if (updated) dispatchAction { refreshSessionOnWorker(sessionId) }
    }

    fun currentSessionView(sessionId: String): SessionViewModel? =
        currentSessionView(sessionId, initialWindow = null)

    fun currentSessionView(sessionId: String, initialWindow: SessionWindowRequest?): SessionViewModel? {
        synchronized(sessionWatchers) {
            sessionWatchers[sessionId]?.flow?.value?.let { return it }
            testSessionViews[sessionId]?.let { return it }
        }
        return computeSessionView(sessionId, initialWindow)
    }

    private fun computeSessionView(sessionId: String, initialWindow: SessionWindowRequest?): SessionViewModel? {
        if (testOverrideViews || closed.get()) return null
        val screen = Screen.newBuilder()
            .setSession(
                SessionScreenRequest.newBuilder()
                    .setSessionId(sessionId)
                    .setWindow(initialWindow ?: DEFAULT_SESSION_WINDOW),
            )
            .build()
        val vm = viewOnWorker(screen) ?: return null
        return if (vm.viewCase == ViewModel.ViewCase.SESSION) vm.session else null
    }

    private fun <V> snapshotSlot(
        target: ScreenTarget,
        slot: ScreenSlot<V>,
        matches: (V) -> Boolean = { false },
        screen: () -> Screen,
        extract: (ViewModel) -> V?,
    ): V {
        val current = slot.flow.value
        if (testOverrideViews || target in testOverriddenTargets || matches(current)) return current
        val vm = viewOnWorker(screen()) ?: return current
        return extract(vm) ?: current
    }

    fun snapshotNewSessionView(projectKey: String): NewSessionViewModel = snapshotSlot(
        ScreenTarget.SCREEN_TARGET_NEW_SESSION,
        newSessionSlot,
        matches = { projectKey.isNotEmpty() && it.projectKey == projectKey },
        screen = { Screen.newBuilder().setNewSession(NewSessionScreenRequest.newBuilder().setProjectKey(projectKey)).build() },
        extract = { if (it.viewCase == ViewModel.ViewCase.NEW_SESSION) it.newSession else null },
    )

    fun snapshotSettingsView(): SettingsViewModel = snapshotSlot(
        ScreenTarget.SCREEN_TARGET_SETTINGS,
        settingsSlot,
        screen = { Screen.newBuilder().setSettings(SettingsScreenRequest.getDefaultInstance()).build() },
        extract = { if (it.viewCase == ViewModel.ViewCase.SETTINGS) it.settings else null },
    )

    fun snapshotMachineStatusView(machineId: String): MachineStatusViewModel = snapshotSlot(
        ScreenTarget.SCREEN_TARGET_MACHINE_STATUS,
        machineStatusSlot,
        matches = { machineId.isNotEmpty() && it.machineId == machineId },
        screen = { Screen.newBuilder().setMachineStatus(MachineStatusScreenRequest.newBuilder().setMachineId(machineId)).build() },
        extract = { if (it.viewCase == ViewModel.ViewCase.MACHINE_STATUS) it.machineStatus else null },
    )

    fun snapshotMachineSettingsView(machineId: String): MachineSettingsViewModel = snapshotSlot(
        ScreenTarget.SCREEN_TARGET_MACHINE_SETTINGS,
        machineSettingsSlot,
        matches = { machineId.isNotEmpty() && it.machineId == machineId },
        screen = { Screen.newBuilder().setMachineSettings(MachineSettingsScreenRequest.newBuilder().setMachineId(machineId)).build() },
        extract = { if (it.viewCase == ViewModel.ViewCase.MACHINE_SETTINGS) it.machineSettings else null },
    )

    fun snapshotQuotaView(machineId: String, filterAgent: Agent = Agent.AGENT_UNSPECIFIED): QuotaViewModel = snapshotSlot(
        ScreenTarget.SCREEN_TARGET_QUOTA,
        quotaSlot,
        matches = { machineId.isNotEmpty() && it.machineId == machineId && quotaFilterAgent == filterAgent },
        screen = { Screen.newBuilder().setQuota(QuotaScreenRequest.newBuilder().setMachineId(machineId).setFilterAgent(filterAgent)).build() },
        extract = { if (it.viewCase == ViewModel.ViewCase.QUOTA) it.quota else null },
    )

    fun snapshotDevicesView(): DevicesViewModel = snapshotSlot(
        ScreenTarget.SCREEN_TARGET_DEVICES,
        devicesSlot,
        screen = { Screen.newBuilder().setDevices(DevicesScreenRequest.getDefaultInstance()).build() },
        extract = { if (it.viewCase == ViewModel.ViewCase.DEVICES) it.devices else null },
    )

    fun snapshotClientDetailView(deviceId: String): ClientDetailViewModel = snapshotSlot(
        ScreenTarget.SCREEN_TARGET_CLIENT_DETAIL,
        clientDetailSlot,
        matches = { deviceId.isNotEmpty() && it.deviceId == deviceId },
        screen = { Screen.newBuilder().setClientDetail(ClientDetailScreenRequest.newBuilder().setDeviceId(deviceId)).build() },
        extract = { if (it.viewCase == ViewModel.ViewCase.CLIENT_DETAIL) it.clientDetail else null },
    )

    fun snapshotUsageView(
        machineId: String,
        focusAgent: Agent = Agent.AGENT_UNSPECIFIED,
        focusModel: String = "",
        preset: UsageRangePreset = UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS,
        customStartMs: Long = 0L,
        customEndMs: Long = 0L,
        metric: UsageMetricTab = UsageMetricTab.USAGE_METRIC_TAB_TOKENS,
    ): UsageViewModel = snapshotSlot(
        ScreenTarget.SCREEN_TARGET_USAGE,
        usageSlot,
        matches = {
            machineId.isNotEmpty() &&
                it.machineId == machineId &&
                usageFocusAgent == focusAgent &&
                usageFocusModel == focusModel &&
                usagePreset == preset &&
                usageCustomStartMs == customStartMs &&
                usageCustomEndMs == customEndMs &&
                usageMetric == metric
        },
        screen = {
            Screen.newBuilder().setUsage(
                UsageScreenRequest.newBuilder()
                    .setMachineId(machineId)
                    .setFocusAgent(focusAgent)
                    .setFocusModel(focusModel)
                    .setPreset(preset)
                    .setCustomStartMs(customStartMs)
                    .setCustomEndMs(customEndMs)
                    .setMetric(metric),
            ).build()
        },
        extract = { if (it.viewCase == ViewModel.ViewCase.USAGE) it.usage else null },
    )

    fun snapshotScheduleDetailView(machineId: String, taskId: String): ScheduleDetailViewModel = snapshotSlot(
        ScreenTarget.SCREEN_TARGET_SCHEDULE_DETAIL,
        scheduleDetailSlot,
        matches = { machineId.isNotEmpty() && taskId.isNotEmpty() && it.machineId == machineId && it.taskId == taskId },
        screen = {
            Screen.newBuilder().setScheduleDetail(
                ScheduleDetailScreenRequest.newBuilder().setMachineId(machineId).setTaskId(taskId),
            ).build()
        },
        extract = { if (it.viewCase == ViewModel.ViewCase.SCHEDULE_DETAIL) it.scheduleDetail else null },
    )

    fun snapshotProjectView(manage: Boolean = true, machineId: String = ""): ProjectViewModel = snapshotSlot(
        ScreenTarget.SCREEN_TARGET_PROJECT,
        projectSlot,
        matches = {
            if (!manage) !it.hasManage()
            else it.hasManage() && it.manage.filterMachineId == machineId
        },
        screen = {
            Screen.newBuilder().setProject(
                ProjectScreenRequest.newBuilder()
                    .setProjectKey("")
                    .setManage(manage)
                    .setEndedLimit(projectEndedLimit)
                    .setMachineId(machineId),
            ).build()
        },
        extract = { if (it.viewCase == ViewModel.ViewCase.PROJECT) it.project else null },
    )

    private fun acquireSessionWatcher(sessionId: String, initialWindow: SessionWindowRequest?): KeyedWatcher<SessionViewModel, SessionWindowRequest> {
        var needsWindowRefresh = false
        val needsSeed = synchronized(sessionWatchers) {
            sessionWatchers[sessionId] == null && testSessionViews[sessionId] == null
        }
        val initialView = if (needsSeed) computeSessionView(sessionId, initialWindow) else null
        var watchedSnapshot: List<String>? = null
        val (watcher, becameActive) = synchronized(sessionWatchers) {
            val existing = sessionWatchers[sessionId]
            if (existing != null) {
                existing.refCount++
                if (initialWindow != null && existing.meta != initialWindow) {
                    existing.meta = initialWindow
                    needsWindowRefresh = true
                }
                existing to false
            } else {
                val created = KeyedWatcher(
                    MutableStateFlow(testSessionViews[sessionId] ?: initialView ?: SessionViewModel.getDefaultInstance()),
                    initialWindow ?: DEFAULT_SESSION_WINDOW,
                    refCount = 1,
                )
                sessionWatchers[sessionId] = created
                watchedSnapshot = sessionWatchers.keys.toList().reversed()
                created to true
            }
        }
        watchedSnapshot?.let(onWatchedSessionsChanged)
        if (becameActive) dispatchAction {
            sendOnWorker { setSessionWatch(SessionWatchEvent.newBuilder().setSessionId(sessionId).setWatched(true)) }
            refreshSessionOnWorker(sessionId)
        } else if (needsWindowRefresh) dispatchAction {
            refreshSessionOnWorker(sessionId)
        }
        return watcher
    }

    private fun releaseSessionWatcher(sessionId: String) {
        var watchedSnapshot: List<String>? = null
        val becameIdle = synchronized(sessionWatchers) {
            val existing = sessionWatchers[sessionId] ?: return
            existing.refCount--
            if (existing.refCount <= 0) {
                sessionWatchers.remove(sessionId)
                watchedSnapshot = sessionWatchers.keys.toList().reversed()
                true
            } else false
        }
        watchedSnapshot?.let(onWatchedSessionsChanged)
        if (becameIdle) send { setSessionWatch(SessionWatchEvent.newBuilder().setSessionId(sessionId).setWatched(false)) }
    }

    private fun acquireScheduleStripWatcher(sessionId: String): KeyedWatcher<ScheduleSessionStripViewModel, Unit> {
        val (watcher, becameActive) = synchronized(scheduleStripWatchers) {
            val existing = scheduleStripWatchers[sessionId]
            if (existing != null) {
                existing.refCount++
                existing to false
            } else {
                val created = KeyedWatcher(
                    MutableStateFlow(testScheduleStripViews[sessionId] ?: ScheduleSessionStripViewModel.getDefaultInstance()),
                    Unit,
                    refCount = 1,
                )
                scheduleStripWatchers[sessionId] = created
                created to true
            }
        }
        if (becameActive) dispatchAction { refreshScheduleStripOnWorker(sessionId) }
        return watcher
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

    fun openManageProjects(machineId: String = "") = dispatchAction {
        projectKey = ""
        projectManage = true
        projectMachineId = machineId
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

    // --- Machine status, quota, machine settings, client & app settings actions ---

    fun openSettings() = dispatchAction {
        refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_SETTINGS)
    }

    fun openDevices() = dispatchAction {
        refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_DEVICES)
    }

    fun openClientDetail(deviceId: String) = dispatchAction {
        clientDetailId = deviceId
        refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_CLIENT_DETAIL)
    }

    fun renameClient(deviceId: String, name: String) =
        send { setUpdateClientName(UpdateClientNameEvent.newBuilder().setDeviceId(deviceId).setName(name)) }

    fun openQuota(machineId: String = "", filterAgent: Agent = Agent.AGENT_UNSPECIFIED) = dispatchAction {
        quotaMachineId = machineId
        quotaFilterAgent = filterAgent
        refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_QUOTA)
    }

    fun watchMachineStatus(machineId: String, visible: Boolean, mode: MachinePollMode) = dispatchAction {
        if (visible && machineId.isNotEmpty()) {
            onHydrateUsageCache(machineId)
        }
        if (visible && mode == MachinePollMode.MACHINE_POLL_MODE_LIVE && machineId.isNotEmpty()) {
            machineStatusId = machineId
        }
        sendOnWorker {
            setMachineStatusWatch(
                MachineStatusWatchEvent.newBuilder()
                    .setMachineId(machineId)
                    .setVisible(visible)
                    .setMode(mode),
            )
        }
        if (visible && mode == MachinePollMode.MACHINE_POLL_MODE_LIVE && machineId.isNotEmpty()) {
            refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_MACHINE_STATUS)
        }
    }

    fun retryMachineStatus(machineId: String) =
        send { setMachineStatusRetry(MachineStatusRetryEvent.newBuilder().setMachineId(machineId)) }

    fun selectMachineStatusTab(machineId: String, tab: MachineUsageTab) =
        send { setMachineStatusTab(MachineStatusTabEvent.newBuilder().setMachineId(machineId).setTab(tab)) }

    fun scrubMachineStatus(machineId: String, kind: MachineChartKind, pointIndex: Int = 0, clear: Boolean = false) = send {
        setMachineStatusScrub(
            MachineStatusScrubEvent.newBuilder()
                .setMachineId(machineId)
                .setKind(kind)
                .setPointIndex(pointIndex)
                .setClear(clear),
        )
    }

    fun prepareCleanupSession(machineId: String) =
        send { setMachineStatusCleanup(MachineStatusCleanupEvent.newBuilder().setMachineId(machineId)) }

    fun openMachineSettings(machineId: String) = dispatchAction {
        machineSettingsId = machineId
        sendOnWorker { setMachineSettingsOpen(MachineSettingsOpenEvent.newBuilder().setMachineId(machineId)) }
        refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_MACHINE_SETTINGS)
    }

    fun toggleMachineAgent(machineId: String, agent: Agent, enabled: Boolean) = send {
        setToggleMachineAgent(
            ToggleMachineAgentEvent.newBuilder()
                .setMachineId(machineId)
                .setAgent(agent)
                .setEnabled(enabled),
        )
    }

    fun hydrateUsageCache(machineId: String) {
        if (machineId.isNotEmpty() && !closed.get()) onHydrateUsageCache(machineId)
    }

    fun deliverUsageCacheLoaded(machineId: String, snapshot: UsageStatsResponse) {
        if (machineId.isEmpty() || closed.get()) return
        send {
            setUsageCacheLoaded(
                UsageCacheLoadedEvent.newBuilder()
                    .setMachineId(machineId)
                    .setSnapshot(snapshot),
            )
        }
    }

    fun openUsageScreen(
        machineId: String,
        focusAgent: Agent = Agent.AGENT_UNSPECIFIED,
        focusModel: String = "",
        preset: UsageRangePreset = UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS,
        customStartMs: Long = 0L,
        customEndMs: Long = 0L,
        metric: UsageMetricTab? = null,
    ) = dispatchAction {
        if (machineId.isEmpty()) return@dispatchAction
        onHydrateUsageCache(machineId)
        val params = buildUsageOpenParams(
            preset = preset,
            customStartMs = customStartMs,
            customEndMs = customEndMs,
            nowMs = clock(),
            zoneId = zone(),
        )
        usageMachineId = machineId
        usageFocusAgent = focusAgent
        usageFocusModel = focusModel
        usagePreset = params.preset
        usageCustomStartMs = params.customStartMs
        usageCustomEndMs = params.customEndMs
        metric?.let { usageMetric = it }
        sendOnWorker {
            setUsageScreenOpen(
                UsageScreenOpenEvent.newBuilder()
                    .setMachineId(machineId)
                    .setPreset(params.preset)
                    .setCustomStartMs(params.customStartMs)
                    .setCustomEndMs(params.customEndMs)
                    .setTimezone(params.timezone)
                    .setTzOffsetMinutes(params.tzOffsetMinutes)
                    .addAllBucketBoundsMs(params.bucketBoundsMs)
                    .setPrevStartMs(params.prevStartMs),
            )
        }
        refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_USAGE)
    }

    fun setUsageMetric(metric: UsageMetricTab) = dispatchAction {
        usageMetric = metric
        refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_USAGE)
    }

    fun retryUsageScreen(machineId: String) {
        if (machineId.isEmpty()) return
        send { setUsageScreenRetry(UsageScreenRetryEvent.newBuilder().setMachineId(machineId)) }
    }

    fun selectMachineSettingsAgent(machineId: String, agent: Agent) = send {
        setMachineSettingsSelectAgent(
            MachineSettingsSelectAgentEvent.newBuilder()
                .setMachineId(machineId)
                .setAgent(agent),
        )
    }

    fun updateMachineAgentDefaults(
        machineId: String,
        agent: Agent,
        model: String? = null,
        effort: String? = null,
        mode: ApprovalMode? = null,
    ) = send {
        val b = UpdateMachineAgentDefaultsEvent.newBuilder()
            .setMachineId(machineId)
            .setAgent(agent)
        model?.let { b.setModel(it) }
        effort?.let { b.setEffort(it) }
        mode?.let { b.setMode(it) }
        setUpdateMachineAgentDefaults(b)
    }

    fun updateMachineDaemonSettings(
        machineId: String,
        approvalTimeoutSeconds: Int? = null,
        idleCloseMinutes: Int? = null,
        imageUploadQuotaBytes: Long? = null,
        cleanImages: Boolean = false,
    ) = send {
        val b = UpdateMachineDaemonSettingsEvent.newBuilder().setMachineId(machineId)
        approvalTimeoutSeconds?.let { b.setApprovalTimeoutSeconds(it) }
        idleCloseMinutes?.let { b.setIdleCloseMinutes(it) }
        imageUploadQuotaBytes?.let { b.setImageUploadQuotaBytes(it) }
        if (cleanImages) b.setCleanImages(true)
        setUpdateMachineDaemonSettings(b)
    }

    fun updateDirectPreference(machineId: String, enabled: Boolean) = send {
        setUpdateDirectPreference(
            UpdateDirectPreferenceEvent.newBuilder()
                .setMachineId(machineId)
                .setEnabled(enabled),
        )
    }

    fun reportMachineDirectStatus(machineId: String, report: DirectRouteReport) = send {
        setMachineDirectStatus(
            MachineDirectStatusEvent.newBuilder()
                .setMachineId(machineId)
                .setReport(report),
        )
    }

    fun reportNotificationPrefs(
        supported: Boolean? = null,
        permission: NotificationPermissionState? = null,
        pushState: PushRegistrationState? = null,
        notifyAttention: Boolean? = null,
        notifyTurnEnded: Boolean? = null,
        notifyScheduled: Boolean? = null,
        showContent: Boolean? = null,
        errorDetail: String? = null,
    ) = send {
        val b = SetNotificationPrefEvent.newBuilder()
        supported?.let { b.setSupported(it) }
        permission?.let { b.setPermission(it) }
        pushState?.let { b.setPushState(it) }
        notifyAttention?.let { b.setNotifyAttention(it) }
        notifyTurnEnded?.let { b.setNotifyTurnEnded(it) }
        notifyScheduled?.let { b.setNotifyScheduled(it) }
        showContent?.let { b.setShowContent(it) }
        errorDetail?.let { b.setErrorDetail(it) }
        setSetNotificationPref(b)
    }

    fun setAppearancePref(theme: ThemePreference) = send {
        setSetAppearancePref(SetAppearancePrefEvent.newBuilder().setTheme(theme))
    }

    fun reportConnCheckStatus(serverState: ConnProbeState, latencyMs: Int = 0, lastFailure: String = "") = send {
        setConnCheckStatus(
            ConnCheckStatusEvent.newBuilder()
                .setServerState(serverState)
                .setLatencyMs(latencyMs)
                .setLastFailure(lastFailure),
        )
    }

    fun reportClientUpdateState(event: ClientUpdateStateEvent) = send {
        setClientUpdateState(event)
    }

    fun checkForUpdates() = send {
        setCheckForUpdates(CheckForUpdatesEvent.getDefaultInstance())
    }

    // --- Session screen actions (200..206) ---

    fun openSession(sessionId: String, refreshModels: Boolean = false, refreshSettings: Boolean = false) = send {
        setSessionOpen(
            SessionOpenAction.newBuilder()
                .setSessionId(sessionId)
                .setRefreshModels(refreshModels)
                .setRefreshSettings(refreshSettings),
        )
    }

    fun submitSessionCommand(sessionId: String, command: Command, machineId: String = "", slotKey: String = "") = send {
        setSessionSubmitCommand(
            SessionSubmitCommandAction.newBuilder()
                .setSessionId(sessionId)
                .setMachineId(machineId)
                .setSlotKey(slotKey)
                .setCommand(command),
        )
    }

    fun abandonSessionCommand(sessionId: String, slotKey: String) = send {
        setSessionAbandonCommand(SessionSlotAction.newBuilder().setSessionId(sessionId).setSlotKey(slotKey))
    }

    fun dismissOutgoing(sessionId: String, requestId: String) = send {
        setSessionDismissOutgoing(SessionOutgoingAction.newBuilder().setSessionId(sessionId).setRequestId(requestId))
    }

    fun restoreOutgoing(sessionId: String, requestId: String) = send {
        setSessionRestoreOutgoing(SessionOutgoingAction.newBuilder().setSessionId(sessionId).setRequestId(requestId))
    }

    fun setSessionQuestionIndex(sessionId: String, questionId: String, questionIndex: Int) = send {
        setSessionSetQuestionIndex(
            SessionQuestionStepAction.newBuilder()
                .setSessionId(sessionId)
                .setQuestionId(questionId)
                .setQuestionIndex(questionIndex),
        )
    }

    fun loadEarlierSessionHistory(sessionId: String) = send {
        setSessionLoadEarlier(SessionRefreshHistoryAction.newBuilder().setSessionId(sessionId))
    }

    // --- Schedule actions (300..349) ---

    fun deliverScheduleCacheLoaded(
        opId: Long,
        machineId: String,
        resultJson: ByteArray = ByteArray(0),
        error: String = "",
    ) = send {
        setScheduleCacheLoaded(
            ScheduleCacheLoadedEvent.newBuilder()
                .setOpId(opId)
                .setMachineId(machineId)
                .setResultJson(ByteString.copyFrom(resultJson))
                .setError(error),
        )
    }

    fun deliverScheduleSeenLoaded(
        machineId: String,
        tokens: List<ScheduleSeenToken> = emptyList(),
        found: Boolean = false,
        error: String = "",
    ) = send {
        setScheduleSeenLoaded(
            ScheduleSeenLoadedEvent.newBuilder()
                .setMachineId(machineId)
                .addAllTokens(tokens)
                .setFound(found)
                .setError(error),
        )
    }

    fun updateScheduleLocalContext(nowMs: Long = clock(), zoneId: ZoneId = zone()) = send {
        setScheduleLocalContext(buildScheduleLocalContext(nowMs, zoneId))
    }

    fun refreshSchedules(machineId: String = "") = send {
        setScheduleRefresh(ScheduleStringAction.newBuilder().setValue(machineId))
    }

    fun setScheduleVisibleSession(sessionId: String) = send {
        setScheduleSetVisibleSession(ScheduleStringAction.newBuilder().setValue(sessionId))
    }

    fun openScheduleList(open: Boolean? = null) = send {
        val b = ScheduleBoolAction.newBuilder()
        open?.let { b.setValue(it) }
        setScheduleListOpen(b)
    }

    fun selectScheduleTask(machineId: String, taskId: String) = dispatchAction {
        scheduleDetailMachineId = machineId
        scheduleDetailTaskId = taskId
        sendOnWorker {
            setScheduleSelectTask(ScheduleTaskRef.newBuilder().setMachineId(machineId).setTaskId(taskId))
        }
    }

    fun sendEvent(block: CoreEvent.Builder.() -> Unit) = send(block)

    fun openScheduleDetail(machineId: String, taskId: String) = dispatchAction {
        scheduleDetailMachineId = machineId
        scheduleDetailTaskId = taskId
        sendOnWorker { setScheduleDetailOpen(ScheduleTaskRef.newBuilder().setMachineId(machineId).setTaskId(taskId)) }
        refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_SCHEDULE_DETAIL)
    }

    fun openScheduleForm(
        mode: ScheduleFormMode,
        machineId: String = "",
        taskId: String = "",
        sessionId: String = "",
        projectKey: String = "",
        localTimezone: String = zone().id,
    ) = dispatchAction {
        scheduleFormKey = ""
        sendOnWorker {
            setScheduleFormOpen(
                ScheduleFormOpenAction.newBuilder()
                    .setMode(mode)
                    .setMachineId(machineId)
                    .setTaskId(taskId)
                    .setSessionId(sessionId)
                    .setProjectKey(projectKey)
                    .setLocalTimezone(localTimezone),
            )
        }
        refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_SCHEDULE_FORM)
    }

    fun tickClock(nowMs: Long = clock()) {
        val tz = zone()
        send { setClockUpdated(buildLocalCalendar(nowMs, tz)) }
        send { setScheduleLocalContext(buildScheduleLocalContext(nowMs, tz)) }
    }

    private fun <V> overrideSlot(target: ScreenTarget, slot: ScreenSlot<V>, v: V?) {
        if (v == null) return
        testOverriddenTargets.add(target)
        slot.flow.value = v
    }

    fun showViewsForTest(
        home: HomeViewModel? = null,
        project: ProjectViewModel? = null,
        newSession: NewSessionViewModel? = null,
        syncState: SyncStateView? = null,
        settings: SettingsViewModel? = null,
        machineStatus: MachineStatusViewModel? = null,
        machineSettings: MachineSettingsViewModel? = null,
        quota: QuotaViewModel? = null,
        devices: DevicesViewModel? = null,
        clientDetail: ClientDetailViewModel? = null,
        schedules: SchedulesViewModel? = null,
        scheduleDetail: ScheduleDetailViewModel? = null,
        scheduleForm: ScheduleFormViewModel? = null,
        scheduleStrips: Map<String, ScheduleSessionStripViewModel> = emptyMap(),
        sessions: Map<String, SessionViewModel> = emptyMap(),
        usage: UsageViewModel? = null,
    ) {
        if (home != null || project != null || newSession != null || syncState != null) {
            testOverrideViews = true
        }
        overrideSlot(ScreenTarget.SCREEN_TARGET_SCHEDULES, schedulesSlot, schedules)
        overrideSlot(ScreenTarget.SCREEN_TARGET_SCHEDULE_DETAIL, scheduleDetailSlot, scheduleDetail)
        overrideSlot(ScreenTarget.SCREEN_TARGET_SCHEDULE_FORM, scheduleFormSlot, scheduleForm)
        home?.let { homeSlot.flow.value = it }
        project?.let { projectSlot.flow.value = it }
        newSession?.let { newSessionSlot.flow.value = it }
        syncState?.let { syncStateSlot.flow.value = it }
        settings?.let { settingsSlot.flow.value = it }
        machineStatus?.let { machineStatusSlot.flow.value = it }
        machineSettings?.let { machineSettingsSlot.flow.value = it }
        quota?.let { quotaSlot.flow.value = it }
        devices?.let { devicesSlot.flow.value = it }
        clientDetail?.let { clientDetailSlot.flow.value = it }
        usage?.let { overrideSlot(ScreenTarget.SCREEN_TARGET_USAGE, usageSlot, it) }
        if (scheduleStrips.isNotEmpty()) {
            synchronized(scheduleStripWatchers) {
                testScheduleStripViews.putAll(scheduleStrips)
                for ((sid, vm) in scheduleStrips) scheduleStripWatchers[sid]?.flow?.value = vm
            }
        }
        if (sessions.isNotEmpty()) {
            synchronized(sessionWatchers) {
                testSessionViews.putAll(sessions)
                for ((sid, vm) in sessions) sessionWatchers[sid]?.flow?.value = vm
            }
        }
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
            synchronized(machinePollTimers) {
                machinePollTimers.values.forEach { it.cancel() }
                machinePollTimers.clear()
            }
            mailbox.close()
            if (ownsScope) scope.cancel()
            synchronized(coreLock) { core.close() }
        }
    }
}

fun DirectStatus.toRouteReport(): DirectRouteReport {
    val s = this
    return DirectRouteReport.newBuilder().apply {
        enabled = s.enabled
        capable = s.supported
        state = when {
            !s.supported -> DirectRouteState.DIRECT_ROUTE_STATE_UNSUPPORTED
            !s.enabled || s.stage == DirectStage.OFF -> DirectRouteState.DIRECT_ROUTE_STATE_NONE
            s.stage == DirectStage.TRYING -> DirectRouteState.DIRECT_ROUTE_STATE_CONNECTING
            s.stage == DirectStage.CONNECTED_RELAY || s.stage == DirectStage.DIRECT_ACTIVE ->
                DirectRouteState.DIRECT_ROUTE_STATE_CONNECTED
            s.reason == DirectFailReason.PEER_UNSUPPORTED -> DirectRouteState.DIRECT_ROUTE_STATE_UNSUPPORTED
            else -> DirectRouteState.DIRECT_ROUTE_STATE_NONE
        }
        setReason(
            when {
                !s.enabled || s.stage == DirectStage.OFF -> DirectRouteReason.DIRECT_ROUTE_REASON_DISABLED
                s.reason == DirectFailReason.PEER_UNSUPPORTED -> DirectRouteReason.DIRECT_ROUTE_REASON_PEER_UNSUPPORTED
                s.reason == DirectFailReason.NO_RESPONSE -> DirectRouteReason.DIRECT_ROUTE_REASON_NO_RESPONSE
                s.reason == DirectFailReason.NO_TRAVERSAL -> DirectRouteReason.DIRECT_ROUTE_REASON_NO_TRAVERSAL
                s.reason == DirectFailReason.FAILED -> DirectRouteReason.DIRECT_ROUTE_REASON_FAILED
                s.reason == DirectFailReason.AT_CAP -> DirectRouteReason.DIRECT_ROUTE_REASON_AT_CAP
                s.reason == DirectFailReason.SECURITY_PENDING -> DirectRouteReason.DIRECT_ROUTE_REASON_SECURITY_PENDING
                s.reason == DirectFailReason.STARTING -> DirectRouteReason.DIRECT_ROUTE_REASON_STARTING
                else -> DirectRouteReason.DIRECT_ROUTE_REASON_UNSPECIFIED
            },
        )
        fileRoute = if (s.fileDirect) DirectPathChoice.DIRECT_PATH_CHOICE_DIRECT else DirectPathChoice.DIRECT_PATH_CHOICE_RELAY
        readRoute = if (s.readDirect) DirectPathChoice.DIRECT_PATH_CHOICE_DIRECT else DirectPathChoice.DIRECT_PATH_CHOICE_RELAY
        warming = false
        compared = s.readSlow || s.fileSlow
        readSlow = s.readSlow
        fileSlow = s.fileSlow
        hasDiagnostics = DirectDiag.lines().isNotEmpty() ||
            s.reason == DirectFailReason.NO_RESPONSE ||
            s.reason == DirectFailReason.NO_TRAVERSAL ||
            s.reason == DirectFailReason.FAILED
        s.bulkDirectMibPerSec?.let {
            hasDirectFileMibps = true
            directFileMibps = it
        }
        s.bulkRelayMibPerSec?.let {
            hasRelayFileMibps = true
            relayFileMibps = it
        }
        s.readDirectMs?.let {
            hasDirectReadMs = true
            directReadMs = it.roundToInt()
        }
        s.readRelayMs?.let {
            hasRelayReadMs = true
            relayReadMs = it.roundToInt()
        }
        val activeMs = when {
            s.readDirect -> s.readDirectMs
            s.stage == DirectStage.CONNECTED_RELAY || s.stage == DirectStage.DIRECT_ACTIVE -> s.readRelayMs
            else -> null
        }
        activeMs?.let {
            hasActiveRttMs = true
            activeRttMs = it.roundToInt()
        }
    }.build()
}

fun buildDirectViews(online: Boolean, prefEnabled: Boolean, status: DirectStatus): DirectViewsResponse {
    val req = DirectViewsRequest.newBuilder()
        .setOnline(online)
        .setPrefEnabled(prefEnabled)
        .setReport(status.toRouteReport())
        .build()
    return DirectViewsResponse.parseFrom(Corebind.buildDirectViews(req.toByteArray()))
}

const val USAGE_RETENTION_DAYS = 90

internal val USAGE_CACHE_PRESETS = listOf(
    UsageRangePreset.USAGE_RANGE_PRESET_TODAY,
    UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS,
    UsageRangePreset.USAGE_RANGE_PRESET_THIRTY_DAYS,
)

data class UsageOpenParams(
    val preset: UsageRangePreset,
    val customStartMs: Long,
    val customEndMs: Long,
    val timezone: String,
    val tzOffsetMinutes: Int,
    val bucketBoundsMs: List<Long>,
    val prevStartMs: Long,
)

fun localTzOffsetMinutes(
    nowMs: Long = System.currentTimeMillis(),
    zoneId: java.time.ZoneId = java.time.ZoneId.systemDefault(),
): Int = zoneId.rules.getOffset(java.time.Instant.ofEpochMilli(nowMs)).totalSeconds / 60

fun clampUsageCustomRange(
    customStartMs: Long,
    customEndMs: Long,
    nowMs: Long = System.currentTimeMillis(),
    zoneId: java.time.ZoneId = java.time.ZoneId.systemDefault(),
): Pair<Long, Long> {
    val today = java.time.Instant.ofEpochMilli(nowMs).atZone(zoneId).toLocalDate()
    val todayStartMs = today.atStartOfDay(zoneId).toInstant().toEpochMilli()
    val minDay = today.minusDays((USAGE_RETENTION_DAYS - 1).toLong())
    val minDayMs = minDay.atStartOfDay(zoneId).toInstant().toEpochMilli()
    val sDate =
        if (customStartMs > 0L) java.time.Instant.ofEpochMilli(customStartMs).atZone(zoneId).toLocalDate()
        else today.minusDays(6)
    val eDate =
        if (customEndMs > 0L) java.time.Instant.ofEpochMilli(customEndMs).atZone(zoneId).toLocalDate()
        else today
    var sDayMs = sDate.atStartOfDay(zoneId).toInstant().toEpochMilli()
    var eDayMs = eDate.atStartOfDay(zoneId).toInstant().toEpochMilli()
    if (eDayMs < sDayMs) eDayMs = sDayMs
    if (sDayMs < minDayMs) sDayMs = minDayMs
    if (eDayMs > todayStartMs) eDayMs = todayStartMs
    if (eDayMs < sDayMs) sDayMs = eDayMs
    return sDayMs to eDayMs
}

fun buildUsageOpenParams(
    preset: UsageRangePreset,
    customStartMs: Long = 0L,
    customEndMs: Long = 0L,
    nowMs: Long = System.currentTimeMillis(),
    zoneId: java.time.ZoneId = java.time.ZoneId.systemDefault(),
): UsageOpenParams {
    val effectivePreset =
        if (preset == UsageRangePreset.USAGE_RANGE_PRESET_UNSPECIFIED) UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS
        else preset
    val today = java.time.Instant.ofEpochMilli(nowMs).atZone(zoneId).toLocalDate()
    var normCustomStart = 0L
    var normCustomEnd = 0L
    val startDay: java.time.LocalDate
    val endExclDay: java.time.LocalDate
    when (effectivePreset) {
        UsageRangePreset.USAGE_RANGE_PRESET_TODAY -> {
            startDay = today
            endExclDay = today.plusDays(1)
        }
        UsageRangePreset.USAGE_RANGE_PRESET_THIRTY_DAYS -> {
            startDay = today.minusDays(29)
            endExclDay = today.plusDays(1)
        }
        UsageRangePreset.USAGE_RANGE_PRESET_CUSTOM -> {
            val (clampedStart, clampedEnd) = clampUsageCustomRange(customStartMs, customEndMs, nowMs, zoneId)
            normCustomStart = clampedStart
            normCustomEnd = clampedEnd
            startDay = java.time.Instant.ofEpochMilli(clampedStart).atZone(zoneId).toLocalDate()
            val endIncDay = java.time.Instant.ofEpochMilli(clampedEnd).atZone(zoneId).toLocalDate()
            endExclDay = endIncDay.plusDays(1)
        }
        else -> {
            startDay = today.minusDays(6)
            endExclDay = today.plusDays(1)
        }
    }
    var numDays = 0L
    var probe = startDay
    while (probe.isBefore(endExclDay) && numDays <= USAGE_RETENTION_DAYS + 2L) {
        numDays++
        probe = probe.plusDays(1)
    }
    fun dayStartMs(d: java.time.LocalDate): Long = d.atStartOfDay(zoneId).toInstant().toEpochMilli()
    val endExclMs = dayStartMs(endExclDay)
    val bounds = ArrayList<Long>()
    if (numDays <= 1L) {
        var cur = dayStartMs(startDay)
        while (cur < endExclMs) {
            bounds.add(cur)
            cur += 3_600_000L
        }
        bounds.add(endExclMs)
    } else if (numDays <= 3L) {
        var day = startDay
        while (day.isBefore(endExclDay)) {
            val dayMs = dayStartMs(day)
            val nextDay = day.plusDays(1)
            val nextMs = dayStartMs(nextDay)
            val span = nextMs - dayMs
            for (q in 0 until 4) {
                bounds.add(dayMs + kotlin.math.round(span * q / 4.0).toLong())
            }
            day = nextDay
        }
        bounds.add(endExclMs)
    } else {
        var cur = startDay
        while (cur.isBefore(endExclDay)) {
            bounds.add(dayStartMs(cur))
            cur = cur.plusDays(1)
        }
        bounds.add(endExclMs)
    }
    val prevStartMs = dayStartMs(startDay.minusDays(numDays))
    return UsageOpenParams(
        preset = effectivePreset,
        customStartMs = normCustomStart,
        customEndMs = normCustomEnd,
        timezone = zoneId.id,
        tzOffsetMinutes = localTzOffsetMinutes(nowMs, zoneId),
        bucketBoundsMs = bounds,
        prevStartMs = prevStartMs,
    )
}
