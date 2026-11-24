package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.corebind.Core
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.SendMode
import io.github.lewin671.remoteagent.proto.UsageRangePreset
import io.github.lewin671.remoteagent.proto.UsageStatsResponse
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.*
import java.security.SecureRandom
import java.time.Instant
import java.time.ZoneId
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong

const val CORE_ENDED_PAGE = 30
const val CORE_SESSION_WINDOW_LIMIT = 80
private val DEFAULT_SESSION_WINDOW: SessionWindowRequest =
    SessionWindowRequest.newBuilder().setLimit(CORE_SESSION_WINDOW_LIMIT).build()
private const val CALENDAR_MONTH_HISTORY = 35
private const val INITIAL_ENTROPY_IDS = 32
private const val INITIAL_ENTROPY_FLOATS = 32
private const val INITIAL_ENTROPY_RANDOM_BYTES = 4096

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
    else "project:" + hmacSha256(deriveScopeKey(root, "home-scope"), scopeKey.toByteArray()).hex()

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
    private val initialLocale: String = "zh-CN",
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
    private val dirPickerWatchers = HashMap<String, KeyedWatcher<DirPickerView, Unit>>()
    private val draftWatchers = HashMap<String, KeyedWatcher<AttachmentDraftView, DraftScope>>()

    var onOpenStream: (OpenStreamEffect) -> Unit = {}
    var onCloseStream: (CloseStreamEffect) -> Unit = {}
    var onEnsureToken: (EnsureTokenEffect) -> Unit = {}
    var onDiscardToken: (DiscardTokenEffect) -> Unit = {}
    var onCacheRead: (CacheReadEffect) -> Unit = {}
    var onCacheWrite: (CacheWriteEffect) -> Unit = {}
    var onCacheDelete: (CacheDeleteEffect) -> Unit = {}
    var onRpc: (RpcEffect) -> Unit = {}
    var onDispatchCommand: (DispatchCommandEffect) -> Unit = {}
    var onSaveHomeScope: (SaveHomeScopeEffect) -> Unit = {}
    var onNavigate: (NavigateEffect) -> Unit = {}
    var onDirectPreference: (DirectPreferenceEffect) -> Unit = {}
    var onSecurityEffect: (Effect) -> Boolean = { false }
    var onSecurityTokenUpdated: (SecurityTokenUpdatedEffect) -> Unit = {}
    var onSecurityStatusChanged: (SecurityStatusChangedEffect) -> Unit = {}
    var onDismissUpgradeNotice: () -> Unit = {}
    var resolveUiMessage: MessageResolver = { it.key }
    val securityClient: SecurityClient by lazy {
        SecurityClient(
            dispatchEvent = ::dispatch,
            setForegroundFn = ::sendSecurityForeground,
            isClosedFn = { closed.get() },
            onDismissUpgradeNotice = { onDismissUpgradeNotice() },
            messageResolver = { resolveUiMessage(it) },
        )
    }
    var onDirectSignalSend: (DirectSignalSendEffect) -> Unit = {}
    var onDirectPcCreate: (DirectPcCreateEffect) -> Unit = {}
    var onDirectPcDescribe: (DirectPcDescribeEffect) -> Unit = {}
    var onDirectPcSetRemote: (DirectPcSetRemoteEffect) -> Unit = {}
    var onDirectPcAddCandidates: (DirectPcAddCandidatesEffect) -> Unit = {}
    var onDirectPcConfirm: (DirectPcConfirmEffect) -> Unit = {}
    var onDirectPcClose: (DirectPcCloseEffect) -> Unit = {}
    var onDirectPcSendProbe: (DirectPcSendProbeEffect) -> Unit = {}
    var onDirectDcReadStats: (DirectDcReadStatsEffect) -> Unit = {}
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
    var onSharedFileScanLocal: (SharedFileScanLocalEffect) -> Unit = {}
    var onSharedFileInspectDisk: (SharedFileInspectDiskEffect) -> Unit = {}
    var onSharedFileWriteChunk: (SharedFileWriteChunkEffect) -> Unit = {}
    var onSharedFileVerifyAndCommit: (SharedFileVerifyAndCommitEffect) -> Unit = {}
    var onSharedFileDeletePartial: (SharedFileDeletePartialEffect) -> Unit = {}
    var onSharedFileDeleteLocal: (SharedFileDeleteLocalEffect) -> Unit = {}
    var onSharedFileDeleteSession: (SharedFileDeleteSessionEffect) -> Unit = {}
    var onSharedFileClearAll: (SharedFileClearAllEffect) -> Unit = {}
    var onSharedFileCompleted: (SharedFileCompletedEffect) -> Unit = {}
    var onDraftStorageList: (DraftStorageListEffect) -> Unit = {}
    var onDraftStorageRead: (DraftStorageReadEffect) -> Unit = {}
    var onDraftStorageWrite: (DraftStorageWriteEffect) -> Unit = {}
    var onDraftStorageDelete: (DraftStorageDeleteEffect) -> Unit = {}
    var onDraftStorageClearAll: (DraftStorageClearAllEffect) -> Unit = {}
    var onAttachmentPrepareImage: (AttachmentPrepareImageEffect) -> Unit = {}
    var onAttachmentHashFile: (AttachmentHashFileEffect) -> Unit = {}
    var onAttachmentReadChunk: (AttachmentReadChunkEffect) -> Unit = {}
    var onAttachmentReleaseLocal: (AttachmentReleaseLocalEffect) -> Unit = {}

    private val nextDirectCallId = AtomicLong(1L)
    private val nextSecurityOpSeq = AtomicLong(1L)
    private val bulkRouteWaiters = ConcurrentHashMap<Long, CompletableDeferred<DirectBulkRouteEffect>>()
    private val pendingSecurityOps = ConcurrentHashMap<Long, CompletableDeferred<SecurityOpResultEffect>>()
    private val retryNowWaiters = ConcurrentHashMap<String, MutableList<CompletableDeferred<Boolean>>>()
    private val terminalReopenListeners = ConcurrentHashMap<String, () -> Unit>()

    private var homeEndedLimit: Int = CORE_ENDED_PAGE
    private var projectKey: String = ""
    private var projectManage: Boolean = true
    private var projectMachineId: String = ""
    private var projectEndedLimit: Int = CORE_ENDED_PAGE
    private var newSessionProjectKey: String = ""
    private var machineStatusId: String = ""
    private var machineSettingsId: String = ""
    private var directDiagnosticsId: String = ""
    private var quotaMachineId: String = ""
    private var quotaFilterAgent: Agent = Agent.AGENT_UNSPECIFIED
    private var clientDetailId: String = ""
    private var usageMachineId: String = ""
    private var usageFocusAgent: Agent = Agent.AGENT_UNSPECIFIED
    private var usageFocusModel: String = ""
    private var usagePreset: UsageRangePreset = UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS
    private var usageCustomStartMs: Long = 0L
    private var usageCustomEndMs: Long = 0L
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

    private val directDiagnosticsSlot = registerScreen(
        ScreenTarget.SCREEN_TARGET_DIRECT_DIAGNOSTICS, DirectDiagnosticsViewModel.getDefaultInstance(),
        { directDiagnosticsId.takeIf { it.isNotEmpty() }?.let { Screen.newBuilder().setDirectDiagnostics(DirectDiagnosticsScreenRequest.newBuilder().setMachineId(it)).build() } },
        { if (it.viewCase == ViewModel.ViewCase.DIRECT_DIAGNOSTICS) it.directDiagnostics else null },
    )
    val directDiagnosticsView: StateFlow<DirectDiagnosticsViewModel> = directDiagnosticsSlot.flow.asStateFlow()

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
                        .setCustomEndMs(usageCustomEndMs),
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
                            .setEntropyProvided(buildEntropyProvided(INITIAL_ENTROPY_IDS, INITIAL_ENTROPY_FLOATS, INITIAL_ENTROPY_RANDOM_BYTES))
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
                    dispatchOnWorker(
                        CoreEvent.newBuilder()
                            .setLocaleChanged(LocaleChangedEvent.newBuilder().setUiLocale(initialLocale))
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
                        .setEntropyProvided(buildEntropyProvided(ids, floats, req.minRandomBytes))
                        .build(),
                )
            }
            Effect.EffectCase.USAGE_CACHE_WRITE -> {
                onUsageCacheWrite(effect.usageCacheWrite)
            }
            Effect.EffectCase.SECURITY_KEY_GENERATE,
            Effect.EffectCase.SECURITY_KEY_SIGN,
            Effect.EffectCase.SECURITY_KEY_AGREE,
            Effect.EffectCase.SECURITY_KEY_ENCAPSULATE,
            Effect.EffectCase.SECURITY_KEY_DISCARD,
            Effect.EffectCase.SECURITY_STORAGE,
            Effect.EffectCase.SECURITY_RPC -> {
                onSecurityEffect(effect)
            }
            Effect.EffectCase.SECURITY_TOKEN_UPDATED -> onSecurityTokenUpdated(effect.securityTokenUpdated)
            Effect.EffectCase.SECURITY_WORKFLOW_RESULT ->
                securityClient.handleWorkflowResult(effect.securityWorkflowResult)
            Effect.EffectCase.SECURITY_STATUS_CHANGED -> {
                val st = effect.securityStatusChanged
                if (st.hasStatus()) securityClient.handleStatusChanged(st.status)
                onSecurityStatusChanged(st)
            }
            Effect.EffectCase.SECURITY_NOTIFY_OPENED -> {}
            Effect.EffectCase.SECURITY_OP_RESULT -> {
                val res = effect.securityOpResult
                pendingSecurityOps.remove(res.opId)?.complete(res)
            }
            Effect.EffectCase.DIRECT_SIGNAL_SEND -> onDirectSignalSend(effect.directSignalSend)
            Effect.EffectCase.DIRECT_PC_CREATE -> onDirectPcCreate(effect.directPcCreate)
            Effect.EffectCase.DIRECT_PC_DESCRIBE -> onDirectPcDescribe(effect.directPcDescribe)
            Effect.EffectCase.DIRECT_PC_SET_REMOTE -> onDirectPcSetRemote(effect.directPcSetRemote)
            Effect.EffectCase.DIRECT_PC_ADD_CANDIDATES -> onDirectPcAddCandidates(effect.directPcAddCandidates)
            Effect.EffectCase.DIRECT_PC_CONFIRM -> onDirectPcConfirm(effect.directPcConfirm)
            Effect.EffectCase.DIRECT_PC_CLOSE -> onDirectPcClose(effect.directPcClose)
            Effect.EffectCase.DIRECT_PC_SEND_PROBE -> onDirectPcSendProbe(effect.directPcSendProbe)
            Effect.EffectCase.DIRECT_DC_READ_STATS -> onDirectDcReadStats(effect.directDcReadStats)
            Effect.EffectCase.DIRECT_BULK_ROUTE -> {
                val res = effect.directBulkRoute
                bulkRouteWaiters.remove(res.callId)?.complete(res)
            }
            Effect.EffectCase.DIRECT_RETRY_NOW_RESULT -> {
                val res = effect.directRetryNowResult
                val list = synchronized(retryNowWaiters) { retryNowWaiters.remove(res.machineId) }
                list?.forEach { it.complete(res.started) }
            }
            Effect.EffectCase.TERMINAL_REOPEN -> {
                val tid = effect.terminalReopen.terminalId
                terminalReopenListeners[tid]?.invoke()
            }
            Effect.EffectCase.SHARED_FILE_SCAN_LOCAL -> onSharedFileScanLocal(effect.sharedFileScanLocal)
            Effect.EffectCase.SHARED_FILE_INSPECT_DISK -> onSharedFileInspectDisk(effect.sharedFileInspectDisk)
            Effect.EffectCase.SHARED_FILE_WRITE_CHUNK -> onSharedFileWriteChunk(effect.sharedFileWriteChunk)
            Effect.EffectCase.SHARED_FILE_VERIFY_AND_COMMIT -> onSharedFileVerifyAndCommit(effect.sharedFileVerifyAndCommit)
            Effect.EffectCase.SHARED_FILE_DELETE_PARTIAL -> onSharedFileDeletePartial(effect.sharedFileDeletePartial)
            Effect.EffectCase.SHARED_FILE_DELETE_LOCAL -> onSharedFileDeleteLocal(effect.sharedFileDeleteLocal)
            Effect.EffectCase.SHARED_FILE_DELETE_SESSION -> onSharedFileDeleteSession(effect.sharedFileDeleteSession)
            Effect.EffectCase.SHARED_FILE_CLEAR_ALL -> onSharedFileClearAll(effect.sharedFileClearAll)
            Effect.EffectCase.SHARED_FILE_COMPLETED -> onSharedFileCompleted(effect.sharedFileCompleted)
            Effect.EffectCase.DRAFT_STORAGE_LIST -> onDraftStorageList(effect.draftStorageList)
            Effect.EffectCase.DRAFT_STORAGE_READ -> onDraftStorageRead(effect.draftStorageRead)
            Effect.EffectCase.DRAFT_STORAGE_WRITE -> onDraftStorageWrite(effect.draftStorageWrite)
            Effect.EffectCase.DRAFT_STORAGE_DELETE -> onDraftStorageDelete(effect.draftStorageDelete)
            Effect.EffectCase.DRAFT_STORAGE_CLEAR_ALL -> onDraftStorageClearAll(effect.draftStorageClearAll)
            Effect.EffectCase.ATTACHMENT_PREPARE_IMAGE -> onAttachmentPrepareImage(effect.attachmentPrepareImage)
            Effect.EffectCase.ATTACHMENT_HASH_FILE -> onAttachmentHashFile(effect.attachmentHashFile)
            Effect.EffectCase.ATTACHMENT_READ_CHUNK -> onAttachmentReadChunk(effect.attachmentReadChunk)
            Effect.EffectCase.ATTACHMENT_RELEASE_LOCAL -> onAttachmentReleaseLocal(effect.attachmentReleaseLocal)
            else -> {}
        }
    }

    private fun buildEntropyProvided(minIds: Int, minFloats: Int, minRandomBytes: Int = 0): EntropyProvided {
        val b = EntropyProvided.newBuilder()
        val idBytes = ByteArray(16)
        repeat(minIds.coerceAtLeast(0)) {
            random.nextBytes(idBytes)
            b.addRandomIds(idBytes.hex())
        }
        repeat(minFloats.coerceAtLeast(0)) {
            b.addUnitFloats(random.nextDouble())
        }
        if (minRandomBytes > 0) {
            val raw = ByteArray(maxOf(minRandomBytes, INITIAL_ENTROPY_RANDOM_BYTES))
            random.nextBytes(raw)
            b.setRandomBytes(ByteString.copyFrom(raw))
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
            ScreenTarget.SCREEN_TARGET_ATTACHMENT_DRAFT -> refreshAllKeyed(draftWatchers, ::refreshDraftOnWorker)
            ScreenTarget.SCREEN_TARGET_PROJECT -> {
                val slot = screenSlots[target]
                if (slot != null) {
                    val screen = slot.build()
                    if (screen != null) {
                        val vm = viewOnWorker(screen)
                        if (vm != null) slot.update(vm)
                    }
                }
                refreshAllKeyed(dirPickerWatchers, ::refreshDirPickerOnWorker)
            }
            else -> {
                val slot = screenSlots[target] ?: return
                val screen = slot.build() ?: return
                val vm = viewOnWorker(screen) ?: return
                slot.update(vm)
            }
        }
    }

    private fun refreshDirPickerOnWorker(pickerKey: String) {
        if (pickerKey.isEmpty() || closed.get() || testOverrideViews) return
        val watcher = synchronized(dirPickerWatchers) { dirPickerWatchers[pickerKey] ?: return }
        val screen = Screen.newBuilder()
            .setProject(ProjectScreenRequest.newBuilder().setManage(true).setPickerKey(pickerKey))
            .build()
        val vm = viewOnWorker(screen) ?: return
        if (vm.viewCase == ViewModel.ViewCase.PROJECT && vm.project.hasManage()) {
            watcher.flow.value = vm.project.manage.dirPicker
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

    private fun refreshDraftOnWorker(scopeKey: String) {
        if (scopeKey.isEmpty() || closed.get() || testOverrideViews) return
        val (watcher, scope) = synchronized(draftWatchers) {
            val w = draftWatchers[scopeKey] ?: return
            w to w.meta
        }
        val screen = Screen.newBuilder()
            .setAttachmentDraft(AttachmentDraftScreenRequest.newBuilder().setScope(scope))
            .build()
        val vm = viewOnWorker(screen) ?: return
        if (vm.viewCase == ViewModel.ViewCase.ATTACHMENT_DRAFT) watcher.flow.value = vm.attachmentDraft.draft
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

    fun watchDirPicker(pickerKey: String): Flow<DirPickerView> {
        require(pickerKey.isNotEmpty()) { "pickerKey must not be empty" }
        return KeyedWatchFlow(dirPickerWatchers, pickerKey, { acquireDirPickerWatcher(pickerKey) }) {
            synchronized(dirPickerWatchers) {
                val existing = dirPickerWatchers[pickerKey] ?: return@synchronized
                if (--existing.refCount <= 0) dirPickerWatchers.remove(pickerKey)
            }
        }
    }

    fun watchAttachmentDraft(scope: DraftScope): Flow<AttachmentDraftView> {
        val key = draftScopeStorageKey(scope)
        require(key.isNotEmpty()) { "draft scope must not be empty" }
        return KeyedWatchFlow(draftWatchers, key, { acquireDraftWatcher(key, scope) }) {
            synchronized(draftWatchers) {
                val existing = draftWatchers[key] ?: return@synchronized
                if (--existing.refCount <= 0) draftWatchers.remove(key)
            }
        }
    }

    fun snapshotAttachmentDraft(scope: DraftScope): AttachmentDraftView {
        val key = draftScopeStorageKey(scope)
        if (key.isEmpty() || closed.get()) return AttachmentDraftView.getDefaultInstance()
        synchronized(draftWatchers) {
            draftWatchers[key]?.flow?.value?.let { return it }
        }
        val vm = viewOnWorker(
            Screen.newBuilder()
                .setAttachmentDraft(AttachmentDraftScreenRequest.newBuilder().setScope(scope))
                .build(),
        )
        return if (vm != null && vm.viewCase == ViewModel.ViewCase.ATTACHMENT_DRAFT) vm.attachmentDraft.draft
        else AttachmentDraftView.getDefaultInstance()
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

    fun snapshotDirectDiagnosticsView(machineId: String): DirectDiagnosticsViewModel = snapshotSlot(
        ScreenTarget.SCREEN_TARGET_DIRECT_DIAGNOSTICS,
        directDiagnosticsSlot,
        matches = { machineId.isNotEmpty() && it.machineId == machineId },
        screen = { Screen.newBuilder().setDirectDiagnostics(DirectDiagnosticsScreenRequest.newBuilder().setMachineId(machineId)).build() },
        extract = { if (it.viewCase == ViewModel.ViewCase.DIRECT_DIAGNOSTICS) it.directDiagnostics else null },
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
                usageCustomEndMs == customEndMs
        },
        screen = {
            Screen.newBuilder().setUsage(
                UsageScreenRequest.newBuilder()
                    .setMachineId(machineId)
                    .setFocusAgent(focusAgent)
                    .setFocusModel(focusModel)
                    .setPreset(preset)
                    .setCustomStartMs(customStartMs)
                    .setCustomEndMs(customEndMs),
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

    private fun acquireDirPickerWatcher(pickerKey: String): KeyedWatcher<DirPickerView, Unit> {
        val (watcher, becameActive) = synchronized(dirPickerWatchers) {
            val existing = dirPickerWatchers[pickerKey]
            if (existing != null) {
                existing.refCount++
                existing to false
            } else {
                val created = KeyedWatcher(
                    MutableStateFlow(DirPickerView.getDefaultInstance()),
                    Unit,
                    refCount = 1,
                )
                dirPickerWatchers[pickerKey] = created
                created to true
            }
        }
        if (becameActive) dispatchAction { refreshDirPickerOnWorker(pickerKey) }
        return watcher
    }

    private fun acquireDraftWatcher(scopeKey: String, scope: DraftScope): KeyedWatcher<AttachmentDraftView, DraftScope> {
        val (watcher, becameActive) = synchronized(draftWatchers) {
            val existing = draftWatchers[scopeKey]
            if (existing != null) {
                existing.refCount++
                existing to false
            } else {
                val created = KeyedWatcher(
                    MutableStateFlow(AttachmentDraftView.getDefaultInstance()),
                    scope,
                    refCount = 1,
                )
                draftWatchers[scopeKey] = created
                created to true
            }
        }
        if (becameActive) dispatchAction { refreshDraftOnWorker(scopeKey) }
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

    fun setUiLocale(uiLocale: String) =
        send { setLocaleChanged(LocaleChangedEvent.newBuilder().setUiLocale(uiLocale)) }

    fun retrySessionEvents(sessionId: String) =
        send { setRetrySessionEvents(RetrySessionEventsAction.newBuilder().setSessionId(sessionId)) }

    fun retrySessionLoad(sessionId: String) =
        send { setSessionRetryLoad(SessionRetryLoadAction.newBuilder().setSessionId(sessionId)) }

    fun sendSharedFileConfig(
        localCacheMaxBytes: Long = 0L,
        storageFullKind: SharedFileFailureKind = SharedFileFailureKind.SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL,
        scanLocal: Boolean = true,
    ) = send {
        setSharedFileConfig(
            SharedFileConfigEvent.newBuilder()
                .setLocalCacheMaxBytes(localCacheMaxBytes)
                .setStorageFullKind(storageFullKind)
                .setScanLocal(scanLocal),
        )
    }

    fun deliverSharedFileLocalScanned(event: SharedFileLocalScannedEvent) =
        send { setSharedFileLocalScanned(event) }

    fun startSharedFileDownload(sessionId: String, fileId: String, autoOpenIfStaying: Boolean = true) =
        send {
            setSharedFileStartDownload(
                SharedFileStartDownloadAction.newBuilder()
                    .setSessionId(sessionId)
                    .setFileId(fileId)
                    .setAutoOpenIfStaying(autoOpenIfStaying),
            )
        }

    fun pauseSharedFileDownload(sessionId: String, fileId: String) =
        send {
            setSharedFilePauseDownload(
                SharedFilePauseDownloadAction.newBuilder()
                    .setSessionId(sessionId)
                    .setFileId(fileId),
            )
        }

    fun cancelSharedFileDownload(sessionId: String, fileId: String) =
        send {
            setSharedFileCancelDownload(
                SharedFileCancelDownloadAction.newBuilder()
                    .setSessionId(sessionId)
                    .setFileId(fileId),
            )
        }

    fun deleteSharedFileLocal(sessionId: String, fileId: String) =
        send {
            setSharedFileDeleteLocal(
                SharedFileDeleteLocalAction.newBuilder()
                    .setSessionId(sessionId)
                    .setFileId(fileId),
            )
        }

    fun setSharedFileVisibleSession(sessionId: String) =
        send {
            setSharedFileSetVisibleSession(
                SharedFileSetVisibleSessionAction.newBuilder()
                    .setSessionId(sessionId),
            )
        }

    fun forgetSharedFileSession(sessionId: String) =
        send {
            setSharedFileForgetSession(
                SharedFileForgetSessionAction.newBuilder()
                    .setSessionId(sessionId),
            )
        }

    fun clearAllSharedFiles() =
        send { setSharedFileClearAll(SharedFileClearAllAction.getDefaultInstance()) }

    suspend fun clearAllSharedFilesAndWait() {
        dispatchAndWait(
            CoreEvent.newBuilder()
                .setSharedFileClearAll(SharedFileClearAllAction.getDefaultInstance())
                .build(),
        )
    }

    fun reportSharedFileDurationProbed(sessionId: String, fileId: String, durationMs: Long) =
        send {
            setSharedFileDurationProbed(
                SharedFileDurationProbedEvent.newBuilder()
                    .setSessionId(sessionId)
                    .setFileId(fileId)
                    .setDurationMs(durationMs),
            )
        }

    fun reportSharedFileLocalMissing(sessionId: String, fileId: String) =
        send {
            setSharedFileLocalMissing(
                SharedFileLocalMissingEvent.newBuilder()
                    .setSessionId(sessionId)
                    .setFileId(fileId),
            )
        }

    fun deliverSharedFileDiskInspected(event: SharedFileDiskInspectedEvent) =
        send { setSharedFileDiskInspected(event) }

    fun deliverSharedFileWriteChunkResult(event: SharedFileWriteChunkResultEvent) =
        send { setSharedFileWriteChunkResult(event) }

    fun deliverSharedFileVerifyAndCommitResult(event: SharedFileVerifyAndCommitResultEvent) =
        send { setSharedFileVerifyAndCommitResult(event) }

    // --- Attachment upload, draft tray, outbox, and composer draft events (240..266) ---

    fun sendAttachmentConfig(
        persistDrafts: Boolean = true,
        persistOutbox: Boolean = true,
        scanStorage: Boolean = true,
        maxChunkBytes: Int = 0,
        hasPersistedPreparedImages: Boolean = true,
    ) = send {
        setAttachmentConfig(
            AttachmentConfigEvent.newBuilder()
                .setPersistDrafts(persistDrafts)
                .setPersistOutbox(persistOutbox)
                .setScanStorage(scanStorage)
                .setMaxChunkBytes(maxChunkBytes)
                .setHasPersistedPreparedImages(hasPersistedPreparedImages),
        )
    }

    fun deliverDraftStorageListed(event: DraftStorageListedEvent) =
        send { setDraftStorageListed(event) }

    fun deliverDraftStorageReadResult(event: DraftStorageReadResultEvent) =
        send { setDraftStorageReadResult(event) }

    fun deliverDraftStorageWriteResult(event: DraftStorageWriteResultEvent) =
        send { setDraftStorageWriteResult(event) }

    fun openDraft(scope: DraftScope, machineId: String = "") = send {
        setDraftOpen(
            DraftOpenAction.newBuilder()
                .setScope(scope)
                .setMachineId(machineId),
        )
    }

    fun setDraftText(scope: DraftScope, text: String) = send {
        setDraftSetText(
            DraftSetTextAction.newBuilder()
                .setScope(scope)
                .setText(text),
        )
    }

    fun setDraftTargetMachine(scope: DraftScope, machineId: String) = send {
        setDraftSetTargetMachine(
            DraftSetTargetMachineAction.newBuilder()
                .setScope(scope)
                .setMachineId(machineId),
        )
    }

    fun reserveDraftSlots(scope: DraftScope, reservationId: String, imageSlots: Int, fileSlots: Int) = send {
        setDraftReserveSlots(
            DraftReserveSlotsAction.newBuilder()
                .setScope(scope)
                .setReservationId(reservationId)
                .setImageSlots(imageSlots)
                .setFileSlots(fileSlots),
        )
    }

    fun releaseDraftSlots(scope: DraftScope, reservationId: String) = send {
        setDraftReleaseSlots(
            DraftReleaseSlotsAction.newBuilder()
                .setScope(scope)
                .setReservationId(reservationId),
        )
    }

    fun addDraftItems(scope: DraftScope, items: List<DraftPickedItem>, reservationId: String = "") = send {
        setDraftAddItems(
            DraftAddItemsAction.newBuilder()
                .setScope(scope)
                .setReservationId(reservationId)
                .addAllItems(items),
        )
    }

    fun removeDraftItem(scope: DraftScope, itemId: String) = send {
        setDraftRemoveItem(
            DraftRemoveItemAction.newBuilder()
                .setScope(scope)
                .setItemId(itemId),
        )
    }

    fun retryDraftItem(scope: DraftScope, itemId: String) = send {
        setDraftRetryItem(
            DraftRetryItemAction.newBuilder()
                .setScope(scope)
                .setItemId(itemId),
        )
    }

    fun setDraftOriginal(scope: DraftScope, original: Boolean) = send {
        setDraftSetOriginal(
            DraftSetOriginalAction.newBuilder()
                .setScope(scope)
                .setOriginal(original),
        )
    }

    fun clearDraft(scope: DraftScope) = send {
        setDraftClear(DraftClearAction.newBuilder().setScope(scope))
    }

    fun forgetDraftSession(sessionId: String) = send {
        setDraftForgetSession(DraftForgetSessionAction.newBuilder().setSessionId(sessionId))
    }

    fun clearAllDrafts() = send {
        setDraftClearAll(DraftClearAllAction.getDefaultInstance())
    }

    suspend fun clearAllDraftsAndWait() {
        dispatchAndWait(
            CoreEvent.newBuilder()
                .setDraftClearAll(DraftClearAllAction.getDefaultInstance())
                .build(),
        )
    }

    fun dismissDraftNotice(scope: DraftScope) = send {
        setDraftDismissNotice(DraftDismissNoticeAction.newBuilder().setScope(scope))
    }

    fun pauseAttachmentUpload(scope: DraftScope, itemId: String) = send {
        setAttachmentPauseUpload(
            AttachmentPauseUploadAction.newBuilder()
                .setScope(scope)
                .setItemId(itemId),
        )
    }

    fun resumeAttachmentUpload(scope: DraftScope, itemId: String) = send {
        setAttachmentResumeUpload(
            AttachmentResumeUploadAction.newBuilder()
                .setScope(scope)
                .setItemId(itemId),
        )
    }

    fun deliverAttachmentPrepareImageResult(event: AttachmentPrepareImageResultEvent) =
        send { setAttachmentPrepareImageResult(event) }

    fun deliverAttachmentHashFileResult(event: AttachmentHashFileResultEvent) =
        send { setAttachmentHashFileResult(event) }

    fun deliverAttachmentReadChunkResult(event: AttachmentReadChunkResultEvent) =
        send { setAttachmentReadChunkResult(event) }

    fun sendDraftSession(sessionId: String, mode: SendMode = SendMode.SEND_MODE_STEER) = send {
        setDraftSendSession(
            DraftSendSessionAction.newBuilder()
                .setSessionId(sessionId)
                .setMode(mode),
        )
    }

    fun putBackDraftInput(sessionId: String, requestId: String) = send {
        setDraftPutBackInput(
            DraftPutBackInputAction.newBuilder()
                .setSessionId(sessionId)
                .setRequestId(requestId),
        )
    }

    fun openDraftImageViewer(scope: DraftScope, index: Int) = send {
        setDraftImageViewerOpen(
            DraftImageViewerOpenAction.newBuilder()
                .setScope(scope)
                .setIndex(index),
        )
    }

    fun navigateDraftImageViewer(scope: DraftScope, delta: Int = 0, setIndex: Boolean = false, index: Int = 0) = send {
        setDraftImageViewerNavigate(
            DraftImageViewerNavigateAction.newBuilder()
                .setScope(scope)
                .setDelta(delta)
                .setSetIndex(setIndex)
                .setIndex(index),
        )
    }

    fun closeDraftImageViewer(scope: DraftScope) = send {
        setDraftImageViewerClose(DraftImageViewerCloseAction.newBuilder().setScope(scope))
    }

    fun openSessionImageViewer(sessionId: String, seq: Long = 0L, imageIndex: Int = 0, viewerIndex: Int = 0) =
        send {
            setSessionImageViewerOpen(
                SessionImageViewerOpenAction.newBuilder()
                    .setSessionId(sessionId)
                    .setSeq(seq)
                    .setImageIndex(imageIndex)
                    .setViewerIndex(viewerIndex),
            )
        }

    fun navigateSessionImageViewer(sessionId: String, delta: Int = 0, setIndex: Boolean = false, index: Int = 0) =
        send {
            setSessionImageViewerNavigate(
                SessionImageViewerNavigateAction.newBuilder()
                    .setSessionId(sessionId)
                    .setDelta(delta)
                    .setSetIndex(setIndex)
                    .setIndex(index),
            )
        }

    fun closeSessionImageViewer(sessionId: String) =
        send { setSessionImageViewerClose(SessionImageViewerCloseAction.newBuilder().setSessionId(sessionId)) }

    fun fetchSession(sessionId: String) =
        send { setFetchSession(FetchSessionAction.newBuilder().setSessionId(sessionId)) }

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

    fun addProject(path: String, pickerKey: String = "") = dispatchAction {
        newSessionProjectKey = path
        sendOnWorker {
            val b = ProjectAddAction.newBuilder().setPath(path)
            if (pickerKey.isNotEmpty()) b.setPickerKey(pickerKey)
            setProjectAdd(b)
        }
    }

    fun openDirPicker(pickerKey: String, machineId: String = "", path: String = "", forNewTask: Boolean = false) {
        require(pickerKey.isNotEmpty()) { "pickerKey must not be empty" }
        send {
            setProjectOpenDirPicker(
                DirPickerOpenAction.newBuilder()
                    .setPickerKey(pickerKey)
                    .setMachineId(machineId)
                    .setPath(path)
                    .setForNewTask(forNewTask),
            )
        }
    }

    fun navigateDirPicker(pickerKey: String, machineId: String = "", path: String = "") {
        require(pickerKey.isNotEmpty()) { "pickerKey must not be empty" }
        send {
            setProjectDirNavigate(
                DirNavigateAction.newBuilder()
                    .setPickerKey(pickerKey)
                    .setMachineId(machineId)
                    .setPath(path),
            )
        }
    }

    fun closeDirPicker(pickerKey: String) {
        require(pickerKey.isNotEmpty()) { "pickerKey must not be empty" }
        send { setProjectCloseDirPicker(DirPickerCloseAction.newBuilder().setPickerKey(pickerKey)) }
    }

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

    fun retryNewSessionModels(projectKey: String = "") =
        send { setNewSessionRetryModels(NewSessionProjectAction.newBuilder().setProjectKey(projectKey)) }

    fun retryNewSessionSaveConfig(projectKey: String = "") =
        send { setNewSessionRetrySaveConfig(NewSessionProjectAction.newBuilder().setProjectKey(projectKey)) }

    fun submitNewSession(
        projectKey: String = "",
        autoModeSupported: Boolean = false,
    ) = send {
        setNewSessionSubmit(
            NewSessionSubmitAction.newBuilder()
                .setProjectKey(projectKey)
                .setAutoModeSupported(autoModeSupported),
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
        applyFocus: Boolean = false,
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
                    .setPrevStartMs(params.prevStartMs)
                    .setApplyFocus(applyFocus)
                    .setFocusAgent(focusAgent)
                    .setFocusModel(focusModel),
            )
        }
        refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_USAGE)
    }

    fun selectUsageSort(machineId: String, sortBy: UsageSortColumn) {
        if (machineId.isEmpty()) return
        send {
            setUsageScreenSelectSort(
                UsageScreenSelectSortEvent.newBuilder()
                    .setMachineId(machineId)
                    .setSortBy(sortBy),
            )
        }
    }

    fun selectUsageSeries(machineId: String, seriesKey: String, toggle: Boolean = true) {
        if (machineId.isEmpty()) return
        send {
            setUsageScreenSelectSeries(
                UsageScreenSelectSeriesEvent.newBuilder()
                    .setMachineId(machineId)
                    .setSeriesKey(seriesKey)
                    .setToggle(toggle),
            )
        }
    }

    fun scrubUsage(machineId: String, bucketIndex: Int, clear: Boolean = false) {
        if (machineId.isEmpty()) return
        send {
            setUsageScreenScrub(
                UsageScreenScrubEvent.newBuilder()
                    .setMachineId(machineId)
                    .setBucketIndex(bucketIndex)
                    .setClear(clear),
            )
        }
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

    fun openDirectDiagnostics(machineId: String) = dispatchAction {
        directDiagnosticsId = machineId
        if (machineId.isNotEmpty()) {
            sendOnWorker {
                setDirectRefreshDiagnostics(DirectRefreshDiagnosticsAction.newBuilder().setMachineId(machineId))
            }
            refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_DIRECT_DIAGNOSTICS)
        }
    }

    fun closeDirectDiagnostics(machineId: String) = dispatchAction {
        if (directDiagnosticsId == machineId) directDiagnosticsId = ""
    }

    fun refreshDirectDiagnostics(machineId: String) {
        if (machineId.isEmpty() || closed.get()) return
        dispatchAction {
            directDiagnosticsId = machineId
            sendOnWorker {
                setDirectRefreshDiagnostics(DirectRefreshDiagnosticsAction.newBuilder().setMachineId(machineId))
            }
            refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_DIRECT_DIAGNOSTICS)
        }
    }

    suspend fun retryDirectNow(machineId: String): Boolean {
        if (machineId.isEmpty() || closed.get()) return false
        val waiter = CompletableDeferred<Boolean>()
        synchronized(retryNowWaiters) {
            retryNowWaiters.getOrPut(machineId) { ArrayList() }.add(waiter)
        }
        send { setDirectRetryNow(DirectRetryNowAction.newBuilder().setMachineId(machineId)) }
        return try {
            waiter.await()
        } catch (e: CancellationException) {
            synchronized(retryNowWaiters) {
                retryNowWaiters[machineId]?.remove(waiter)
                if (retryNowWaiters[machineId]?.isEmpty() == true) retryNowWaiters.remove(machineId)
            }
            throw e
        }
    }

    fun sendTransportConfig(
        supported: Boolean,
        account: String,
        protocolVersion: Int,
        serverHost: String,
        configuredStun: String,
        platformAgent: String = "android",
    ) = send {
        setTransportConfig(
            TransportConfigEvent.newBuilder()
                .setSupported(supported)
                .setAccount(account)
                .setProtocolVersion(protocolVersion)
                .setServerHost(serverHost)
                .setConfiguredStun(configuredStun)
                .setPlatformAgent(platformAgent),
        )
    }

    fun sendDirectPreferencesLoaded(entries: Map<String, Boolean>) = send {
        val b = DirectPreferencesLoadedEvent.newBuilder()
        for ((id, enabled) in entries) {
            b.addEntries(DirectPreferenceEntry.newBuilder().setMachineId(id).setEnabled(enabled))
        }
        setDirectPreferencesLoaded(b)
    }

    fun sendSecurityConfig(
        account: String,
        deviceId: String,
        server: String = "",
        inlineCrypto: Boolean = true,
        foreground: Boolean = true,
    ) = send {
        setSecurityConfig(
            SecurityConfigEvent.newBuilder()
                .setAccount(account)
                .setDeviceId(deviceId)
                .setServer(server)
                .setInlineCrypto(inlineCrypto)
                .setForeground(foreground),
        )
    }

    fun sendSecurityForeground(foreground: Boolean) = send {
        setSecurityForeground(
            SecurityForegroundEvent.newBuilder().setForeground(foreground),
        )
    }

    suspend fun securityOp(configure: SecurityOpRequestEvent.Builder.() -> Unit): SecurityOpResultEffect {
        if (closed.get()) throw IllegalStateException("core closed")
        val opId = nextSecurityOpSeq.getAndIncrement()
        val deferred = CompletableDeferred<SecurityOpResultEffect>()
        pendingSecurityOps[opId] = deferred
        val req = SecurityOpRequestEvent.newBuilder().setOpId(opId).apply(configure).build()
        dispatch(CoreEvent.newBuilder().setSecurityOpRequest(req).build())
        return try {
            if (closed.get() && !deferred.isCompleted) {
                pendingSecurityOps.remove(opId, deferred)
                throw IllegalStateException("core closed")
            }
            deferred.await()
        } catch (t: Throwable) {
            pendingSecurityOps.remove(opId, deferred)
            throw t
        }
    }

    fun dispatchDirectSignal(raw: ByteArray) {
        if (raw.isEmpty() || closed.get()) return
        send { setDirectSignalReceived(DirectSignalReceivedEvent.newBuilder().setRaw(ByteString.copyFrom(raw))) }
    }

    fun dispatchDirectSignalDelivery(frameId: ByteArray, status: Int, retryAfterMs: Long) {
        if (frameId.isEmpty() || closed.get()) return
        send {
            setDirectSignalDelivery(
                DirectSignalDeliveryEvent.newBuilder()
                    .setFrameId(ByteString.copyFrom(frameId))
                    .setStatus(status)
                    .setRetryAfterMs(maxOf(0L, retryAfterMs)),
            )
        }
    }

    suspend fun beginDirectBulkTransfer(
        machineId: String,
        routeHint: Int = 0,
        bulkFileKey: String = "",
        bulkBytes: Long = 0L,
        bulkUpload: Boolean = false,
        deadlineMs: Long,
        forceRelay: Boolean = false,
        uploadTransferId: String = "",
        uploadHasFilename: Boolean = false,
        interactiveDual: Boolean = false,
    ): DirectBulkRouteEffect {
        if (closed.get()) return DirectBulkRouteEffect.getDefaultInstance()
        val callId = nextDirectCallId.getAndIncrement()
        val waiter = CompletableDeferred<DirectBulkRouteEffect>()
        bulkRouteWaiters[callId] = waiter
        val ev = CoreEvent.newBuilder()
            .setDirectBulkTransferBegin(
                DirectBulkTransferBeginEvent.newBuilder()
                    .setCallId(callId)
                    .setMachineId(machineId)
                    .setRouteHint(routeHint)
                    .setBulkFileKey(bulkFileKey)
                    .setBulkBytes(bulkBytes)
                    .setBulkUpload(bulkUpload)
                    .setDeadlineMs(deadlineMs)
                    .setForceRelay(forceRelay)
                    .setUploadTransferId(uploadTransferId)
                    .setUploadHasFilename(uploadHasFilename)
                    .setInteractiveDual(interactiveDual),
            )
            .build()
        if (mailbox.trySend { if (!closed.get()) dispatchOnWorker(ev) else waiter.complete(DirectBulkRouteEffect.getDefaultInstance()) }.isFailure) {
            bulkRouteWaiters.remove(callId, waiter)
            return DirectBulkRouteEffect.getDefaultInstance()
        }
        return try {
            waiter.await()
        } catch (e: CancellationException) {
            bulkRouteWaiters.remove(callId, waiter)
            throw e
        }
    }

    fun finishDirectBulkTransfer(
        ticketId: Long,
        routeWasDirect: Boolean,
        ok: Boolean,
        elapsedMs: Double,
        observedBytes: Long,
        finishUploadTransferId: String = "",
        machineId: String = "",
    ) {
        if (closed.get()) return
        if (ticketId == 0L && finishUploadTransferId.isEmpty()) return
        send {
            setDirectBulkTransferDone(
                DirectBulkTransferDoneEvent.newBuilder()
                    .setTicketId(ticketId)
                    .setRouteWasDirect(routeWasDirect)
                    .setOk(ok)
                    .setElapsedMs(elapsedMs)
                    .setObservedBytes(observedBytes)
                    .setFinishUploadTransferId(finishUploadTransferId)
                    .setMachineId(machineId),
            )
        }
    }

    fun sendTerminalStream(terminalId: String, kind: TerminalStreamEventKind, opened: Boolean) {
        if (terminalId.isEmpty() || closed.get()) return
        send {
            setTerminalStream(
                TerminalStreamEvent.newBuilder()
                    .setTerminalId(terminalId)
                    .setKind(kind)
                    .setOpened(opened),
            )
        }
    }

    fun onTerminalReopen(terminalId: String, listener: () -> Unit): () -> Unit {
        terminalReopenListeners[terminalId] = listener
        return { terminalReopenListeners.remove(terminalId, listener) }
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

    fun cancelTrayItem(sessionId: String, requestId: String) = send {
        setSessionCancelTrayItem(SessionTrayItemAction.newBuilder().setSessionId(sessionId).setRequestId(requestId))
    }

    fun ignoreTrayItem(sessionId: String, requestId: String) = send {
        setSessionIgnoreTrayItem(SessionTrayItemAction.newBuilder().setSessionId(sessionId).setRequestId(requestId))
    }

    fun promoteTrayItem(sessionId: String, requestId: String) = send {
        setSessionPromoteTrayItem(SessionTrayItemAction.newBuilder().setSessionId(sessionId).setRequestId(requestId))
    }

    fun restoreTrayItem(sessionId: String, requestId: String) = send {
        setSessionRestoreTrayItem(SessionTrayItemAction.newBuilder().setSessionId(sessionId).setRequestId(requestId))
    }

    fun retryTrayItem(sessionId: String, requestId: String) = send {
        setSessionRetryTrayItem(SessionTrayItemAction.newBuilder().setSessionId(sessionId).setRequestId(requestId))
    }

    fun resumeTrayItem(sessionId: String, requestId: String) = send {
        setSessionResumeTrayItem(SessionTrayItemAction.newBuilder().setSessionId(sessionId).setRequestId(requestId))
    }

    fun cancelAllPaused(sessionId: String) = send {
        setSessionCancelAllPaused(SessionTraySessionAction.newBuilder().setSessionId(sessionId))
    }

    fun undoCancel(sessionId: String) = send {
        setSessionUndoCancel(SessionTraySessionAction.newBuilder().setSessionId(sessionId))
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
        directDiagnostics: DirectDiagnosticsViewModel? = null,
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
        overrideSlot(ScreenTarget.SCREEN_TARGET_DIRECT_DIAGNOSTICS, directDiagnosticsSlot, directDiagnostics)
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
        directDiagnostics?.let { directDiagnosticsSlot.flow.value = it }
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
            for ((k, w) in bulkRouteWaiters.entries.toList()) {
                if (bulkRouteWaiters.remove(k, w)) w.complete(DirectBulkRouteEffect.getDefaultInstance())
            }
            for ((k, w) in pendingSecurityOps.entries.toList()) {
                if (pendingSecurityOps.remove(k, w)) {
                    w.completeExceptionally(IllegalStateException("core closed"))
                }
            }
            synchronized(retryNowWaiters) {
                for (list in retryNowWaiters.values) list.forEach { it.complete(false) }
                retryNowWaiters.clear()
            }
            terminalReopenListeners.clear()
            mailbox.close()
            if (ownsScope) scope.cancel()
            synchronized(coreLock) { core.close() }
        }
    }
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
    } else if (numDays <= 7L) {
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
