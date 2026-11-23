package io.github.lewin671.remoteagent.core

import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.core.proto.AccountPrefsFetchedEvent
import io.github.lewin671.remoteagent.core.proto.AccountPrefsPutResultEvent
import io.github.lewin671.remoteagent.core.proto.AttachmentsSettledEffect
import io.github.lewin671.remoteagent.core.proto.CacheBootstrapLoadedEvent
import io.github.lewin671.remoteagent.core.proto.CacheEventsLoadedEvent
import io.github.lewin671.remoteagent.core.proto.CacheReadEffect
import io.github.lewin671.remoteagent.core.proto.CommandOutcomeEvent
import io.github.lewin671.remoteagent.core.proto.ConnState as CoreConnState
import io.github.lewin671.remoteagent.core.proto.DispatchCommandEffect
import io.github.lewin671.remoteagent.core.proto.EnsureTokenEffect
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.core.proto.EventsPageFetchedEvent
import io.github.lewin671.remoteagent.core.proto.ListsFetchedEvent
import io.github.lewin671.remoteagent.core.proto.NavigateEffect
import io.github.lewin671.remoteagent.core.proto.NavigateRoute
import io.github.lewin671.remoteagent.core.proto.NewSessionOpenAction
import io.github.lewin671.remoteagent.core.proto.NewSessionSetPromptAction
import io.github.lewin671.remoteagent.core.proto.NewSessionSubmitAction
import io.github.lewin671.remoteagent.core.proto.OpenStreamEffect
import io.github.lewin671.remoteagent.core.proto.OutcomeSource
import io.github.lewin671.remoteagent.core.proto.PlainAccountPrefs
import io.github.lewin671.remoteagent.core.proto.PlainEvent
import io.github.lewin671.remoteagent.core.proto.PlainMachine
import io.github.lewin671.remoteagent.core.proto.PlainSession
import io.github.lewin671.remoteagent.core.proto.ProjectKeyAction
import io.github.lewin671.remoteagent.core.proto.RpcEffect
import io.github.lewin671.remoteagent.core.proto.DemoSecurity
import io.github.lewin671.remoteagent.core.proto.ScreenTarget
import io.github.lewin671.remoteagent.core.proto.SeedDemoWorldEvent
import io.github.lewin671.remoteagent.core.proto.SessionWatchEvent
import io.github.lewin671.remoteagent.core.proto.StreamClosedEvent
import io.github.lewin671.remoteagent.core.proto.StreamReadyEvent
import io.github.lewin671.remoteagent.core.proto.SyncLifecycleAction
import io.github.lewin671.remoteagent.core.proto.SyncLifecycleKind
import io.github.lewin671.remoteagent.core.proto.TokenResultEvent
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentInfo
import io.github.lewin671.remoteagent.proto.AgentMessage
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.CommandResult
import io.github.lewin671.remoteagent.proto.CreateSessionResponse
import io.github.lewin671.remoteagent.proto.DaemonSettings
import io.github.lewin671.remoteagent.proto.Event as WireEvent
import io.github.lewin671.remoteagent.proto.GetSettingsResponse
import io.github.lewin671.remoteagent.proto.ListModelsResponse
import io.github.lewin671.remoteagent.proto.MachineCapabilities
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.ProjectPrefs
import io.github.lewin671.remoteagent.proto.ProjectSummary
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionStatus
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.asCoroutineDispatcher
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.ZoneId

class CoreHostSmokeTest {
    @get:org.junit.Rule val hangGuard = hangGuard()
    private val nowMs = 1_760_000_000_000L

    private fun host(clock: () -> Long = { nowMs }, mono: () -> Long = { 1_000L }): CoreHost =
        CoreHost(
            workerDispatcher = Dispatchers.Unconfined,
            clock = clock,
            monotonic = mono,
            zone = { ZoneId.of("UTC") },
        )

    @Test
    fun localCalendarPopulatesRecentMonths() {
        val cal = buildLocalCalendar(nowMs, ZoneId.of("UTC"))
        assertEquals(36, cal.monthsCount)
        assertEquals(2025, cal.monthsList.last().year)
        assertEquals(10, cal.monthsList.last().month)
    }

    @Test
    fun m2LifecycleListsDisconnectReconnectCatchupAndNewSessionRoundTrip() {
        var now = nowMs
        var mono = 1_000L
        val h = host(clock = { now }, mono = { mono })

        var savedScope: String? = null
        val cacheReads = ArrayList<CacheReadEffect>()
        val tokens = ArrayList<EnsureTokenEffect>()
        val streams = ArrayList<OpenStreamEffect>()
        val rpcs = ArrayList<RpcEffect>()
        val cmds = ArrayList<DispatchCommandEffect>()
        val navigated = ArrayList<NavigateEffect>()
        val settled = ArrayList<AttachmentsSettledEffect>()

        h.onSaveHomeScope = { savedScope = it.scopeKey }
        h.onCacheRead = { cacheReads += it }
        h.onEnsureToken = { tokens += it }
        h.onOpenStream = { streams += it }
        h.onRpc = { rpcs += it }
        h.onDispatchCommand = { cmds += it }
        h.onNavigate = { navigated += it }
        h.onAttachmentsSettled = { settled += it }

        val machine = PlainMachine.newBuilder()
            .setMachineId("m1")
            .setOnline(true)
            .setLastSeenMs(now)
            .setMetaRev(1)
            .setMeta(
                MachineMeta.newBuilder()
                    .setHostname("Laptop")
                    .setProtocolVersion(PEER_PROTOCOL_VERSION)
                    .setCapabilities(MachineCapabilities.newBuilder().setFileUpload(true))
                    .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX))
                    .addProjects(ProjectSummary.newBuilder().setPath("/work/app")),
            )
            .build()
        val session = PlainSession.newBuilder()
            .setSessionId("s1")
            .setMachineId("m1")
            .setLastSeq(1L)
            .setUpdatedMs(now - 10_000L)
            .setMetaRev(1)
            .setMeta(
                Session.newBuilder()
                    .setId("s1")
                    .setCwd("/work/app")
                    .setTitle("Fix tests")
                    .setAgent(Agent.AGENT_CODEX)
                    .setStatus(SessionStatus.SESSION_STATUS_RUNNING)
                    .setCreatedAt(Timestamp.newBuilder().setSeconds(now / 1000 - 60)),
            )
            .build()
        var currentPrefs = ProjectPrefs.newBuilder().setSchema(1).build()
        var prefsRev = 0L

        fun drainFakeTransport() {
            while (
                cacheReads.isNotEmpty() || tokens.isNotEmpty() ||
                streams.isNotEmpty() || rpcs.isNotEmpty()
            ) {
                while (cacheReads.isNotEmpty()) {
                    val cr = cacheReads.removeAt(0)
                    when (cr.kindCase) {
                        CacheReadEffect.KindCase.BOOTSTRAP -> h.dispatchOnWorker(
                            CoreEvent.newBuilder()
                                .setCacheBootstrapLoaded(CacheBootstrapLoadedEvent.newBuilder().setOpId(cr.bootstrap.opId))
                                .build(),
                        )
                        CacheReadEffect.KindCase.EVENTS -> h.dispatchOnWorker(
                            CoreEvent.newBuilder()
                                .setCacheEventsLoaded(
                                    CacheEventsLoadedEvent.newBuilder()
                                        .setOpId(cr.events.opId)
                                        .setSessionId(cr.events.sessionId),
                                )
                                .build(),
                        )
                        else -> {}
                    }
                }
                while (tokens.isNotEmpty()) {
                    val t = tokens.removeAt(0)
                    h.dispatchOnWorker(
                        CoreEvent.newBuilder()
                            .setTokenResult(
                                TokenResultEvent.newBuilder()
                                    .setOpId(t.opId)
                                    .setToken("tok-1")
                                    .setExpiresMs(now + 86_400_000L),
                            )
                            .build(),
                    )
                }
                while (streams.isNotEmpty()) {
                    val st = streams.removeAt(0)
                    h.dispatchOnWorker(
                        CoreEvent.newBuilder()
                            .setStreamReady(
                                StreamReadyEvent.newBuilder()
                                    .setLegId(st.legId)
                                    .setServerTimeMs(now),
                            )
                            .build(),
                    )
                }
                while (rpcs.isNotEmpty()) {
                    val rpc = rpcs.removeAt(0)
                    when (rpc.kindCase) {
                        RpcEffect.KindCase.FETCH_LISTS -> h.dispatchOnWorker(
                            CoreEvent.newBuilder()
                                .setListsFetched(
                                    ListsFetchedEvent.newBuilder()
                                        .setOpId(rpc.opId)
                                        .addMachines(machine)
                                        .addSessions(session),
                                )
                                .build(),
                        )
                        RpcEffect.KindCase.GET_ACCOUNT_PREFS -> h.dispatchOnWorker(
                            CoreEvent.newBuilder()
                                .setAccountPrefsFetched(
                                    AccountPrefsFetchedEvent.newBuilder()
                                        .setOpId(rpc.opId)
                                        .setPrefs(
                                            PlainAccountPrefs.newBuilder()
                                                .setRevision(prefsRev)
                                                .setPrefs(currentPrefs),
                                        ),
                                )
                                .build(),
                        )
                        RpcEffect.KindCase.PUT_ACCOUNT_PREFS -> {
                            currentPrefs = rpc.putAccountPrefs.nextPrefs
                            prefsRev++
                            h.dispatchOnWorker(
                                CoreEvent.newBuilder()
                                    .setAccountPrefsPutResult(
                                        AccountPrefsPutResultEvent.newBuilder()
                                            .setOpId(rpc.opId)
                                            .setApplied(true)
                                            .setCurrent(
                                                PlainAccountPrefs.newBuilder()
                                                    .setRevision(prefsRev)
                                                    .setPrefs(currentPrefs),
                                            ),
                                    )
                                    .build(),
                            )
                        }
                        RpcEffect.KindCase.GET_EVENTS -> h.dispatchOnWorker(
                            CoreEvent.newBuilder()
                                .setEventsPageFetched(
                                    EventsPageFetchedEvent.newBuilder()
                                        .setOpId(rpc.opId)
                                        .setSessionId(rpc.getEvents.sessionId)
                                        .addEvents(
                                            PlainEvent.newBuilder()
                                                .setSeq(1L)
                                                .setVerified(true)
                                                .setEvent(
                                                    WireEvent.newBuilder()
                                                        .setSeq(1L)
                                                        .setAgentMessage(AgentMessage.newBuilder().setText("catchup reply")),
                                                ),
                                        ),
                                )
                                .build(),
                        )
                        else -> {}
                    }
                }
            }
        }

        try {
            // 1. Start -> Connected -> Lists -> Home rows
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setSeedDemoWorld(
                        SeedDemoWorldEvent.newBuilder().setSecurity(
                            DemoSecurity.newBuilder()
                                .setIsMember(true)
                                .setSelfDeviceId("c1")
                                .setAuthorityDeviceId("c1")
                                .addTrustedMachineIds("m1")
                                .setHeadEpoch(1L)
                                .setLeaseEndMs(now + 3_600_000L)
                                .setNetworkGeneration(1L),
                        ),
                    )
                    .build(),
            )
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setSyncLifecycle(SyncLifecycleAction.newBuilder().setKind(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_START))
                    .build(),
            )
            drainFakeTransport()
            h.refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_HOME)

            val home = h.homeView.value
            assertEquals(CoreConnState.CONN_STATE_CONNECTED, home.connection.state)
            assertEquals(1, home.runningCount)
            assertEquals("s1", home.runningList.single().sessionId)
            assertEquals("Fix tests", home.runningList.single().title.literal)

            // 2. Scope & Project Pin round-trip via AccountPrefs PUT
            h.setHomeScope("/work/app")
            drainFakeTransport()
            assertEquals("/work/app", savedScope)
            assertEquals("/work/app", h.homeView.value.scopeKey)

            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setProjectTogglePin(ProjectKeyAction.newBuilder().setProjectKey("/work/app"))
                    .build(),
            )
            drainFakeTransport()
            h.refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_PROJECT)
            assertTrue(h.projectView.value.manage.listing.pinnedCount == 1)

            // 3. Disconnect & reconnect catchup for a watched session
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setSessionWatch(SessionWatchEvent.newBuilder().setSessionId("s1").setWatched(true))
                    .build(),
            )
            drainFakeTransport()
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setStreamClosed(StreamClosedEvent.newBuilder().setLegId(1L).setError("drop"))
                    .build(),
            )
            h.refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_HOME)
            assertEquals(CoreConnState.CONN_STATE_RECONNECTING, h.homeView.value.connection.state)

            now += 2_000L
            mono += 2_000L
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setSyncLifecycle(SyncLifecycleAction.newBuilder().setKind(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_RECONNECT_NOW))
                    .build(),
            )
            drainFakeTransport()
            h.refreshScreenOnWorker(ScreenTarget.SCREEN_TARGET_HOME)
            assertEquals(CoreConnState.CONN_STATE_CONNECTED, h.homeView.value.connection.state)

            // 4. NewSession open + prompt + submit with attachments -> AttachmentsSettled + Navigate
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setNewSessionOpen(NewSessionOpenAction.newBuilder().setProjectKey("/work/app"))
                    .build(),
            )
            while (cmds.isNotEmpty()) {
                val dc = cmds.removeAt(0)
                val parsed = Command.parseFrom(dc.commandBytes)
                val res = when {
                    parsed.hasListModels() -> CommandResult.newBuilder()
                        .setOutcome("executed")
                        .setReason("ok")
                        .setResultAvailable(true)
                        .setListModels(ListModelsResponse.getDefaultInstance())
                        .build()
                    parsed.hasGetSettings() -> CommandResult.newBuilder()
                        .setOutcome("executed")
                        .setReason("ok")
                        .setResultAvailable(true)
                        .setGetSettings(GetSettingsResponse.newBuilder().setSettings(DaemonSettings.getDefaultInstance()))
                        .build()
                    else -> null
                }
                if (res != null) {
                    h.dispatchOnWorker(
                        CoreEvent.newBuilder()
                            .setCommandOutcome(
                                CommandOutcomeEvent.newBuilder()
                                    .setRequestId(dc.requestId)
                                    .setAttemptId(dc.attemptId)
                                    .setSource(OutcomeSource.OUTCOME_SOURCE_DAEMON)
                                    .setAttemptBound(true)
                                    .setSigned(true)
                                    .setHashesMatch(true)
                                    .setOuterMatchesInner(true)
                                    .setOuterOutcome("executed")
                                    .setOuterReason("ok")
                                    .setResult(res),
                            )
                            .build(),
                    )
                }
            }
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setNewSessionSetPrompt(NewSessionSetPromptAction.newBuilder().setPrompt("hello").setAttachmentCount(2))
                    .build(),
            )
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setNewSessionSubmit(
                        NewSessionSubmitAction.newBuilder()
                            .setProjectKey("/work/app")
                            .setAutoModeSupported(true)
                            .addImageIds("img-1")
                            .addFileIds("file-1"),
                    )
                    .build(),
            )
            val createCmd = cmds.single { Command.parseFrom(it.commandBytes).hasCreateSession() }
            val createdReq = Command.parseFrom(createCmd.commandBytes).createSession
            assertEquals(listOf("img-1"), createdReq.imageIdsList)
            assertEquals(listOf("file-1"), createdReq.fileIdsList)

            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setCommandOutcome(
                        CommandOutcomeEvent.newBuilder()
                            .setRequestId(createCmd.requestId)
                            .setAttemptId(createCmd.attemptId)
                            .setSource(OutcomeSource.OUTCOME_SOURCE_DAEMON)
                            .setAttemptBound(true)
                            .setSigned(true)
                            .setHashesMatch(true)
                            .setOuterMatchesInner(true)
                            .setOuterOutcome("executed")
                            .setOuterReason("ok")
                            .setResult(
                                CommandResult.newBuilder()
                                    .setOutcome("executed")
                                    .setReason("ok")
                                    .setResultAvailable(true)
                                    .setCreateSession(
                                        CreateSessionResponse.newBuilder()
                                            .setSession(
                                                Session.newBuilder()
                                                    .setId("s-created")
                                                    .setCwd("/work/app")
                                                    .setAgent(Agent.AGENT_CODEX),
                                            ),
                                    ),
                            ),
                    )
                    .build(),
            )
            assertTrue(settled.any { it.projectKey == "/work/app" && it.consumed })
            assertTrue(navigated.any { it.route == NavigateRoute.NAVIGATE_ROUTE_SESSION && it.sessionId == "s-created" })

            // Synchronous snapshot getters populate first-frame ViewModels before async open/watch events run.
            val snapMachine = h.snapshotMachineSettingsView("m1")
            assertTrue("snapshotMachineSettingsView should find m1", snapMachine.found)
            assertEquals("m1", snapMachine.machineId)
            val snapNew = h.snapshotNewSessionView("/work/app")
            assertEquals("/work/app", snapNew.projectKey)
            val snapSession = h.currentSessionView("s1")
            assertTrue("currentSessionView should return populated header synchronously", snapSession?.header?.found == true)
        } finally {
            h.close()
        }
    }

    @Test
    fun initRunsOnSerialWorkerMailboxInsteadOfConstructorThread() = kotlinx.coroutines.runBlocking<Unit> {
        val callerThread = Thread.currentThread()
        val workerName = "corehost-init-serial-test"
        var workerThread: Thread? = null
        val executor = java.util.concurrent.Executors.newSingleThreadExecutor { r ->
            Thread(r, workerName).also { workerThread = it }
        }
        val dispatcher = executor.asCoroutineDispatcher()
        val clockThreads = java.util.concurrent.CopyOnWriteArrayList<Thread>()
        var h: CoreHost? = null
        try {
            val created = CoreHost(
                workerDispatcher = dispatcher,
                clock = {
                    clockThreads += Thread.currentThread()
                    nowMs
                },
                monotonic = { 1_000L },
                zone = { ZoneId.of("UTC") },
            )
            h = created
            // Constructor must not invoke clock() synchronously on caller thread.
            assertTrue(clockThreads.none { it === callerThread })

            created.dispatchAndWait(
                CoreEvent.newBuilder()
                    .setSyncLifecycle(
                        SyncLifecycleAction.newBuilder().setKind(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_STOP),
                    )
                    .build(),
            )
            assertTrue("clockThreads empty after dispatchAndWait", clockThreads.isNotEmpty())
            // Compare threads, not names: with coroutine debug mode on (as under -ea), a
            // thread's name carries " @coroutine#N" while it runs a coroutine.
            assertTrue("clockThreads should only run on $workerName", clockThreads.all { it === workerThread })
            assertTrue("headline should be populated after init", created.homeView.value.hasHeadline())
        } finally {
            h?.close()
            dispatcher.close()
        }
    }

    @Test
    fun setUiLocaleSwitchesCoreFormattedUsageNumbers() {
        val h = host()
        try {
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setSeedDemoWorld(
                        SeedDemoWorldEvent.newBuilder()
                            .addMachines(
                                io.github.lewin671.remoteagent.core.proto.DemoMachine.newBuilder()
                                    .setMachineId("m1")
                                    .setOnline(false)
                                    .setMeta(
                                        MachineMeta.newBuilder()
                                            .setHostname("Mac Studio")
                                            .setProtocolVersion(PEER_PROTOCOL_VERSION)
                                            .setCapabilities(MachineCapabilities.newBuilder().setUsageStats(true)),
                                    ),
                            )
                            .setSecurity(
                                DemoSecurity.newBuilder()
                                    .setIsMember(true)
                                    .setSelfDeviceId("c1")
                                    .setAuthorityDeviceId("c1")
                                    .addTrustedMachineIds("m1")
                                    .setHeadEpoch(1L)
                                    .setLeaseEndMs(nowMs + 3_600_000L),
                            ),
                    )
                    .build(),
            )
            val snap = io.github.lewin671.remoteagent.proto.UsageStatsResponse.newBuilder()
                .setPreset(io.github.lewin671.remoteagent.proto.UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS)
                .setGranularity(io.github.lewin671.remoteagent.proto.UsageBucketGranularity.USAGE_BUCKET_GRANULARITY_DAY)
                .setGeneratedAtMs(nowMs)
                .addSeries(
                    io.github.lewin671.remoteagent.proto.UsageSeries.newBuilder()
                        .setAgent(Agent.AGENT_CLAUDE)
                        .setModel("claude-sonnet-4-6")
                        .setCurrent(
                            io.github.lewin671.remoteagent.proto.UsagePeriodSummary.newBuilder()
                                .setUncachedInputTokens(1_200_000L)
                                .setOutputTokens(300_000L)
                                .setUsageRequestCount(5L),
                        ),
                )
                .build()
            h.deliverUsageCacheLoaded("m1", snap)
            h.openUsageScreen("m1")
            assertEquals("150万", h.usageView.value.hero.totalTokensText.literal)

            h.setUiLocale("en")
            assertEquals("1.5M", h.usageView.value.hero.totalTokensText.literal)

            h.setUiLocale("zh-CN")
            assertEquals("150万", h.usageView.value.hero.totalTokensText.literal)
        } finally {
            h.close()
        }
    }

    @Test
    fun dirPickerWatchersStayIsolatedByPickerKey() = kotlinx.coroutines.runBlocking {
        val nowMs = 1_776_000_000_000L
        val h = CoreHost(
            workerDispatcher = Dispatchers.Unconfined,
            clock = { nowMs },
            zone = { ZoneId.of("UTC") },
        )
        try {
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setSeedDemoWorld(
                        SeedDemoWorldEvent.newBuilder()
                            .addMachines(
                                io.github.lewin671.remoteagent.core.proto.DemoMachine.newBuilder()
                                    .setMachineId("m1")
                                    .setOnline(true)
                                    .setMeta(
                                        MachineMeta.newBuilder()
                                            .setHostname("Mac Studio")
                                            .setHome("/Users/dev")
                                            .setProtocolVersion(PEER_PROTOCOL_VERSION),
                                    ),
                            )
                            .setSecurity(
                                DemoSecurity.newBuilder()
                                    .setIsMember(true)
                                    .setSelfDeviceId("c1")
                                    .setAuthorityDeviceId("c1")
                                    .addTrustedMachineIds("m1")
                                    .setHeadEpoch(1L)
                                    .setLeaseEndMs(nowMs + 3_600_000L),
                            ),
                    )
                    .build(),
            )
            val manageFlow = h.watchDirPicker("manage")
            val scheduleFlow = h.watchDirPicker("schedule-form")
            var latestManage = io.github.lewin671.remoteagent.core.proto.DirPickerView.getDefaultInstance()
            var latestSchedule = io.github.lewin671.remoteagent.core.proto.DirPickerView.getDefaultInstance()
            val j1 = launch(Dispatchers.Unconfined) { manageFlow.collect { latestManage = it } }
            val j2 = launch(Dispatchers.Unconfined) { scheduleFlow.collect { latestSchedule = it } }
            try {
                h.openDirPicker("manage", machineId = "m1", path = "/Users/dev/a", forNewTask = false)
                assertTrue(latestManage.open)
                assertEquals("manage", latestManage.pickerKey)
                org.junit.Assert.assertFalse(latestSchedule.open)

                h.openDirPicker("schedule-form", machineId = "m1", path = "/Users/dev/b", forNewTask = true)
                assertTrue(latestSchedule.open)
                assertEquals("schedule-form", latestSchedule.pickerKey)
                assertTrue(latestSchedule.forNewTask)
                org.junit.Assert.assertFalse(latestManage.forNewTask)

                h.closeDirPicker("manage")
                org.junit.Assert.assertFalse(latestManage.open)
                assertTrue(latestSchedule.open)
            } finally {
                j1.cancel()
                j2.cancel()
            }
        } finally {
            h.close()
        }
    }
}
