package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.core.proto.AttachmentConfigEvent
import io.github.lewin671.remoteagent.core.proto.CacheBootstrapLoadedEvent
import io.github.lewin671.remoteagent.core.proto.CacheEventsLoadedEvent
import io.github.lewin671.remoteagent.core.proto.CacheReadEffect
import io.github.lewin671.remoteagent.core.proto.CommandOutcomeEvent
import io.github.lewin671.remoteagent.core.proto.ConnState as CoreConnState
import io.github.lewin671.remoteagent.core.proto.DispatchCommandEffect
import io.github.lewin671.remoteagent.core.proto.DraftOpenAction
import io.github.lewin671.remoteagent.core.proto.DraftSetTextAction
import io.github.lewin671.remoteagent.core.proto.Effect
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.core.proto.NavigateEffect
import io.github.lewin671.remoteagent.core.proto.NavigateRoute
import io.github.lewin671.remoteagent.core.proto.NewSessionOpenAction
import io.github.lewin671.remoteagent.core.proto.NewSessionSubmitAction
import io.github.lewin671.remoteagent.core.proto.OpenStreamEffect
import io.github.lewin671.remoteagent.core.proto.OutcomeSource
import io.github.lewin671.remoteagent.core.proto.ProjectKeyAction
import io.github.lewin671.remoteagent.core.proto.RelayRpcEffect
import io.github.lewin671.remoteagent.core.proto.RelayRpcMethod
import io.github.lewin671.remoteagent.core.proto.RelayRpcResultEvent
import io.github.lewin671.remoteagent.core.proto.DemoSecurity
import io.github.lewin671.remoteagent.core.proto.ScreenTarget
import io.github.lewin671.remoteagent.core.proto.SeedDemoWorldEvent
import io.github.lewin671.remoteagent.core.proto.SessionWatchEvent
import io.github.lewin671.remoteagent.core.proto.StreamClosedEvent
import io.github.lewin671.remoteagent.core.proto.StreamReadyEvent
import io.github.lewin671.remoteagent.core.proto.SyncLifecycleAction
import io.github.lewin671.remoteagent.core.proto.SyncLifecycleKind
import io.github.lewin671.remoteagent.proto.AccountPrefsObject
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentInfo
import io.github.lewin671.remoteagent.proto.AgentMessage
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.CommandResult
import io.github.lewin671.remoteagent.proto.CreateSessionResponse
import io.github.lewin671.remoteagent.proto.DaemonSettings
import io.github.lewin671.remoteagent.proto.Event as WireEvent
import io.github.lewin671.remoteagent.proto.GetAccountPrefsResponse
import io.github.lewin671.remoteagent.proto.GetEventsResponse
import io.github.lewin671.remoteagent.proto.GetSettingsResponse
import io.github.lewin671.remoteagent.proto.ListDevicesResponse
import io.github.lewin671.remoteagent.proto.ListMachinesResponse
import io.github.lewin671.remoteagent.proto.ListModelsResponse
import io.github.lewin671.remoteagent.proto.ListRemoteSessionsResponse
import io.github.lewin671.remoteagent.proto.Machine
import io.github.lewin671.remoteagent.proto.MachineCapabilities
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.ProjectPrefs
import io.github.lewin671.remoteagent.proto.ProjectSummary
import io.github.lewin671.remoteagent.proto.PutAccountPrefsRequest
import io.github.lewin671.remoteagent.proto.PutAccountPrefsResponse
import io.github.lewin671.remoteagent.proto.RemoteSession
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionStatus
import io.github.lewin671.remoteagent.proto.StoredEvent
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
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
        val streams = ArrayList<OpenStreamEffect>()
        val cmds = ArrayList<DispatchCommandEffect>()
        val navigated = ArrayList<NavigateEffect>()

        h.onSaveHomeScope = { savedScope = it.scopeKey }
        h.onCacheRead = { cacheReads += it }
        h.onOpenStream = { streams += it }
        h.onDispatchCommand = { cmds += it }
        h.onNavigate = { navigated += it }

        val machineMeta = MachineMeta.newBuilder()
            .setHostname("Laptop")
            .setProtocolVersion(PEER_PROTOCOL_VERSION)
            .setCapabilities(MachineCapabilities.newBuilder().setFileUpload(true))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX))
            .addProjects(ProjectSummary.newBuilder().setPath("/work/app"))
            .build()
        val sessionMeta = Session.newBuilder()
            .setId("s1")
            .setCwd("/work/app")
            .setTitle("Fix tests")
            .setAgent(Agent.AGENT_CODEX)
            .setStatus(SessionStatus.SESSION_STATUS_RUNNING)
            .setCreatedAt(Timestamp.newBuilder().setSeconds(now / 1000 - 60))
            .build()
        val sessionEvent1 = WireEvent.newBuilder()
            .setSeq(1L)
            .setAgentMessage(AgentMessage.newBuilder().setText("catchup reply"))
            .build()

        fun drainFakeTransport() {
            while (cacheReads.isNotEmpty() || streams.isNotEmpty()) {
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
            }
        }

        try {
            // 1. Start -> Connected -> Lists -> Home rows
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setSeedDemoWorld(
                        SeedDemoWorldEvent.newBuilder()
                            .addMachines(
                                io.github.lewin671.remoteagent.core.proto.DemoMachine.newBuilder()
                                    .setMachineId("m1")
                                    .setOnline(true)
                                    .setLastSeenMs(now)
                                    .setRawMeta(machineMeta.toByteString()),
                            )
                            .addSessions(
                                io.github.lewin671.remoteagent.core.proto.DemoSession.newBuilder()
                                    .setSessionId("s1")
                                    .setMachineId("m1")
                                    .setLastSeq(1L)
                                    .setUpdatedMs(now - 10_000L)
                                    .setRawMeta(sessionMeta.toByteString()),
                            )
                            .addSessionEvents(
                                io.github.lewin671.remoteagent.core.proto.DemoSessionEvents.newBuilder()
                                    .setSessionId("s1")
                                    .addRawEvents(sessionEvent1.toByteString())
                                    .setReplace(true),
                            )
                            .setReplaceMachines(true)
                            .setReplaceSessions(true)
                            .setSecurity(
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

            // 4. NewSession open + Core draft text + submit -> Navigate
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setAttachmentConfig(AttachmentConfigEvent.newBuilder().setPersistDrafts(false).setPersistOutbox(false).setScanStorage(false))
                    .build(),
            )
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setNewSessionOpen(NewSessionOpenAction.newBuilder().setProjectKey("/work/app"))
                    .build(),
            )
            val draftScope = newSessionDraftScope("/work/app")
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setDraftOpen(DraftOpenAction.newBuilder().setScope(draftScope).setMachineId("m1"))
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
                                    .setRawResult(res.toByteString()),
                            )
                            .build(),
                    )
                }
            }
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setDraftSetText(DraftSetTextAction.newBuilder().setScope(draftScope).setText("hello"))
                    .build(),
            )
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setNewSessionSubmit(
                        NewSessionSubmitAction.newBuilder()
                            .setProjectKey("/work/app")
                            .setAutoModeSupported(true),
                    )
                    .build(),
            )
            val createCmd = cmds.single { Command.parseFrom(it.commandBytes).hasCreateSession() }
            val createdReq = Command.parseFrom(createCmd.commandBytes).createSession
            assertEquals("hello", createdReq.prompt)

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
                            .setRawResult(
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
                                    )
                                    .build()
                                    .toByteString(),
                            ),
                    )
                    .build(),
            )
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
                                    .setRawMeta(
                                        MachineMeta.newBuilder()
                                            .setHostname("Mac Studio")
                                            .setHome("/Users/dev")
                                            .setProtocolVersion(PEER_PROTOCOL_VERSION)
                                            .build()
                                            .toByteString(),
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

    @Test
    fun cancelPingMachineCancelsOnlyMatchingRequestId() = kotlinx.coroutines.runBlocking {
        val nowMs = 1_776_000_000_000L
        val h = CoreHost(
            workerDispatcher = Dispatchers.Unconfined,
            clock = { nowMs },
            zone = { ZoneId.of("UTC") },
        )
        try {
            h.onOpenStream = { eff ->
                h.dispatchOnWorker(
                    CoreEvent.newBuilder()
                        .setStreamReady(
                            StreamReadyEvent.newBuilder()
                                .setLegId(eff.legId)
                                .setServerTimeMs(nowMs),
                        )
                        .build(),
                )
            }
            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setSeedDemoWorld(
                        SeedDemoWorldEvent.newBuilder()
                            .addMachines(
                                io.github.lewin671.remoteagent.core.proto.DemoMachine.newBuilder()
                                    .setMachineId("m1")
                                    .setOnline(true)
                                    .setRawMeta(
                                        MachineMeta.newBuilder()
                                            .setHostname("Mac Studio")
                                            .setHome("/Users/dev")
                                            .setProtocolVersion(PEER_PROTOCOL_VERSION)
                                            .build()
                                            .toByteString(),
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
            h.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_START)
            val dispatched = mutableListOf<DispatchCommandEffect>()
            val canceledRequestIds = mutableListOf<String>()
            h.onDispatchCommand = { eff -> dispatched.add(eff) }
            h.onCancelDispatch = { eff -> canceledRequestIds.add(eff.requestId) }

            val first = launch(Dispatchers.Unconfined) { h.pingMachine("m1") }
            val second = async(Dispatchers.Unconfined) { h.pingMachine("m1") }

            assertEquals(2, dispatched.size)
            val d1 = dispatched[0]
            val d2 = dispatched[1]
            org.junit.Assert.assertNotEquals(d1.requestId, d2.requestId)

            first.cancel()
            first.join()
            assertEquals(listOf(d1.requestId), canceledRequestIds)

            h.dispatchOnWorker(
                CoreEvent.newBuilder()
                    .setCommandOutcome(
                        CommandOutcomeEvent.newBuilder()
                            .setRequestId(d2.requestId)
                            .setAttemptId(d2.attemptId)
                            .setSource(OutcomeSource.OUTCOME_SOURCE_DAEMON)
                            .setSigned(true)
                            .setHashesMatch(true)
                            .setAttemptBound(true)
                            .setOuterMatchesInner(true)
                            .setOuterOutcome("executed")
                            .setOuterReason("ok")
                            .setRawResult(
                                io.github.lewin671.remoteagent.proto.CommandResult.newBuilder()
                                    .setOutcome("executed")
                                    .setReason("ok")
                                    .setResultAvailable(true)
                                    .setGetSettings(io.github.lewin671.remoteagent.proto.GetSettingsResponse.getDefaultInstance())
                                    .build()
                                    .toByteString(),
                            ),
                    )
                    .build(),
            )
            second.await()
        } finally {
            h.close()
        }
    }

    @Test
    fun lazySubscreensSkipUnsubscribedRendersAndRefreshOnFirstSubscribe() = runBlocking<Unit> {
        val nowMs = 1_700_000_000_000L
        val h = CoreHost(clock = { nowMs }, monotonic = { 10_000L })
        try {
            h.dispatchAndWait(
                CoreEvent.newBuilder()
                    .setSeedDemoWorld(
                        SeedDemoWorldEvent.newBuilder()
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
            // With 0 subscribers, RenderEffect for lazy sub-screens (e.g. DEVICES) must skip FFI rendering.
            assertFalse(h.devicesView.value.isManager)
            assertEquals(0L, h.devicesView.value.leaseEndMs)

            // Subscribing for the first time must proactively refresh and deliver the up-to-date ViewModel.
            val devicesVm = withTimeout(5_000L) {
                h.devicesView.first { it.isManager }
            }
            assertEquals(nowMs + 3_600_000L, devicesVm.leaseEndMs)
        } finally {
            h.close()
        }
    }

    @Test
    fun runOnWorkerBlockingSerializesAfterInitAndIsReentrant() {
        val h = CoreHost(clock = { nowMs }, monotonic = { 10_000L }, initialLocale = "en")
        try {
            // Executes on the background mailbox worker strictly after CoreHost.init completes,
            // and supports nested re-entrant calls without deadlocking.
            val result = h.runOnWorkerBlocking {
                val inner = h.runOnWorkerBlocking { "nested-ok" }
                // Init dispatched locale + initial screen refreshes before this block ran.
                assertTrue(h.connectionView.value.hasServerDetails())
                inner
            }
            assertEquals("nested-ok", result)
            h.flushMailboxBlocking()
        } finally {
            h.close()
        }
    }

    @Test
    fun runOnWorkerBlockingUnblocksOnCloseAndFailsAfterClose() {
        val h = CoreHost(clock = { nowMs }, monotonic = { 10_000L })
        val workerEntered = java.util.concurrent.CountDownLatch(1)
        val releaseWorker = java.util.concurrent.CountDownLatch(1)
        val barrierError = java.util.concurrent.atomic.AtomicReference<Throwable?>()
        val callerDone = java.util.concurrent.CountDownLatch(1)
        try {
            // Occupy the single mailbox worker so a queued barrier blocks waiting for it.
            // Anti-hang protection comes from the class-level hangGuard rule; no short wall-clock
            // timeouts are used here so slow machines cannot release early or fail spuriously.
            h.onCheckUpdates = {
                workerEntered.countDown()
                releaseWorker.await()
            }
            h.checkForUpdates()
            workerEntered.await()

            val caller = Thread {
                try {
                    h.runOnWorkerBlocking { "unreachable" }
                } catch (t: Throwable) {
                    barrierError.set(t)
                } finally {
                    callerDone.countDown()
                }
            }
            caller.start()

            // Wait on explicit barrier registration rather than thread state or wall clock.
            while (h.pendingWorkerBarriersForTest() < 1) {
                Thread.yield()
            }

            // Closing the host must immediately fail the waiting barrier rather than hanging.
            h.close()
            callerDone.await()
            assertTrue(
                "expected IllegalStateException on close, got ${barrierError.get()}",
                barrierError.get() is IllegalStateException,
            )

            // Subsequent barrier calls after close fail/return immediately.
            assertTrue(runCatching { h.runOnWorkerBlocking { Unit } }.exceptionOrNull() is IllegalStateException)
            h.flushMailboxBlocking()
        } finally {
            releaseWorker.countDown()
            h.close()
        }
    }
}
