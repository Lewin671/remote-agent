package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.proto.SessionViewModel
import io.github.lewin671.remoteagent.core.proto.SessionWindowRequest
import io.github.lewin671.remoteagent.core.assertMessageEquals as assertEquals

import io.github.lewin671.remoteagent.proto.*
import io.github.lewin671.remoteagent.proto.AgentMessage
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.Machine
import io.github.lewin671.remoteagent.proto.RemoteSession
import io.github.lewin671.remoteagent.proto.SendRequest
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionStatus
import io.github.lewin671.remoteagent.proto.StoredEvent
import io.github.lewin671.remoteagent.proto.TerminalFrame
import io.github.lewin671.remoteagent.proto.UserMessage
import kotlinx.serialization.json.*
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import kotlinx.serialization.json.jsonObject
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.ResponseBody.Companion.toResponseBody
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Before
import org.junit.Test

/** The sync engine against the real Go server and a fake daemon. */
class RemoteTest {
    @get:org.junit.Rule val hangGuard = hangGuard()
    private lateinit var server: TestServer
    private val http = Api.defaultHttp()
    private val root = randomBytes(32)
    private lateinit var daemon: FakeDaemon
    private val closers = mutableListOf<AutoCloseable>()

    @Before
    fun setUp() =
        runBlocking<Unit> {
            server = TestServer()
            daemon = FakeDaemon.create(root, server.url, http, server.setupToken)
            daemon.putMachine("fake-mac.local")
            daemon.putSession("s1", session("/Users/me/Code/foo", "修复登录超时"))
            daemon.upload("s1", user("你好"), agent("你好，有什么要做的？"), agent("第三条"))
            daemon.connect()
        }

    @After
    fun tearDown() {
        try {
            closers.forEach { runCatching { it.close() } }
            if (this::daemon.isInitialized) runCatching { daemon.close() }
        } finally {
            if (this::server.isInitialized) server.close()
        }
    }

    private fun session(cwd: String, title: String) =
        Session.newBuilder()
            .setCwd(cwd)
            .setTitle(title)
            .setStatus(SessionStatus.SESSION_STATUS_IDLE)
            .build()

    private fun user(t: String) =
        Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText(t))

    private fun agent(t: String) =
        Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText(t))

    private suspend fun client(
        cache: CipherCache? = null,
        frameQueue: Int = 1024,
        httpClient: okhttp3.OkHttpClient = http,
    ): Remote {
        val acct = approvedClient(daemon.phone, http)
        val r =
            fixtureRemote(
                acct,
                httpClient,
                cache,
                frameQueue = frameQueue,
            )
        closers += AutoCloseable { r.close() }
        return r
    }

    @Test
    fun retiredSessionAndCachedEventShowTheSameExplicitError() = runBlocking<Unit> {
        val cache = MemoryCache()
        val old = Envelope.newBuilder().setHeader(Header.newBuilder().setVersion(1)
            .setKind(PayloadKind.PAYLOAD_KIND_SESSION_KEY).build().toByteString()).build()
        cache.putSession(RemoteSession.newBuilder().setSessionId("retired")
            .setMachineId(daemon.account.deviceId).setKey(old).build())
        cache.putEvents("s1", listOf(StoredEvent.newBuilder().setSeq(1).setEnvelope(old.toBuilder()
            .setHeader(Header.newBuilder().setVersion(1).setKind(PayloadKind.PAYLOAD_KIND_EVENT).build().toByteString())).build()))
        val r = client(cache = cache)
        r.start()
        val retiredVm = kotlinx.coroutines.withTimeout(10_000) {
            r.watchSession("retired").first { it.timeline.retired }
        }
        assertTrue(retiredVm.timeline.retired)
        assertTrue(retiredVm.timeline.hasHistoryNotice())
        val s1Vm = kotlinx.coroutines.withTimeout(10_000) {
            r.watchSession("s1").first { it.header.found && it.timeline.retired }
        }
        assertTrue(s1Vm.timeline.retired)
        assertTrue(s1Vm.timeline.hasHistoryNotice())
        assertTrue(s1Vm.timeline.itemsList.isEmpty())
        assertEquals(1, cache.events("s1").size)
        cache.putImage("s1/retired", old.toBuilder().setHeader(Header.newBuilder().setVersion(1)
            .setKind(PayloadKind.PAYLOAD_KIND_IMAGE).build().toByteString()).build().toByteArray())
        assertEquals(Remote.ImageResult.Corrupt(RETIRED_HISTORY_ERROR), r.image("s1", "retired"))
    }

    private val Remote.conn: io.github.lewin671.remoteagent.core.proto.ConnectionViewModel
        get() = coreHost.connectionView.value
    private val io.github.lewin671.remoteagent.core.proto.ConnectionViewModel.isOnline: Boolean
        get() = connected
    private val io.github.lewin671.remoteagent.core.proto.ConnectionViewModel.isOffline: Boolean
        get() = state == io.github.lewin671.remoteagent.core.proto.ConnState.CONN_STATE_OFFLINE
    private fun Remote.machineCard(id: String) =
        coreHost.snapshotSettingsView().machinesList.firstOrNull { it.machineId == id }
    private fun Remote.clientRow(id: String) =
        coreHost.snapshotDevicesView().clientsList.firstOrNull { it.deviceId == id }
    private fun Remote.projectListing() =
        coreHost.snapshotProjectView(manage = true).manage.listing
    private fun Remote.pinnedPaths(): List<String> =
        projectListing().pinnedList.map { it.key }
    private fun Remote.isProjectPinned(path: String): Boolean =
        coreHost.snapshotProjectView(manage = false, projectKey = path).detail.isPinned
    private fun Remote.isProjectHidden(path: String): Boolean =
        coreHost.snapshotProjectView(manage = false, projectKey = path).detail.isHidden

    private suspend fun Remote.awaitUntil(what: String, ok: () -> Boolean) {
        kotlinx.coroutines.withTimeout(15_000) {
            while (!ok()) delay(15)
        }
    }

    private suspend fun Remote.awaitSessionTimeline(sid: String, minItems: Int): SessionViewModel =
        watchSession(sid).first { it.timeline.itemsCount >= minItems }

    @Test
    fun storedPrefsSurviveRemovalOfTheirWriter() =
        runBlocking<Unit> {
            val writer = client()
            val reader = client()
            writer.start()
            writer.coreHost.connectionView.first { it.isOnline }
            writer.coreHost.toggleProjectPin("/stored")
            writer.awaitUntil("writer pinned") { !writer.projectListing().loading && writer.isProjectPinned("/stored") }
            val admin = fixtureRemote(daemon.phone, http)
            closers += AutoCloseable { admin.close() }
            admin.removeDevice(writer.account.deviceId)
            reader.start()
            reader.coreHost.connectionView.first { it.isOnline }
            reader.coreHost.toggleProjectHide("/next")
            reader.awaitUntil("reader hidden") {
                !reader.projectListing().loading && reader.isProjectPinned("/stored") && reader.isProjectHidden("/next")
            }
            assertTrue(reader.isProjectPinned("/stored"))
            assertTrue(reader.isProjectHidden("/next"))
        }

    @Test
    fun ordinaryPhoneHandoffUsesTheSavedOfferAndKeepsHistory() =
        runBlocking<Unit> {
            val incoming = approvedClient(daemon.phone, http)
            val old = fixtureRemote(daemon.phone, http)
            val next = fixtureRemote(incoming, http)
            closers += AutoCloseable { old.close() }
            closers += AutoCloseable { next.close() }
            old.start()
            next.start()
            old.awaitUntil("old phone ready") {
                old.conn.isOnline && old.clientRow(incoming.deviceId) != null
            }
            next.awaitUntil("new phone ready") {
                next.conn.isOnline && next.clientRow(incoming.deviceId) != null
            }
            val code = next.prepareHandoffCode(storagePersisted = true)
            assertTrue(code.isNotBlank())
            next.publishHandoffOffer()
            old.refreshDevices()
            old.handoffAuthority(incoming.deviceId)
            next.syncSecurity()
            next.renewLeaseBlocking(force = true)
            next.awaitUntil("new authority") {
                val sec = next.coreHost.snapshotSettingsView().security
                sec.isManager && sec.leaseEndMs > System.currentTimeMillis()
            }
            assertTrue(next.coreHost.snapshotSettingsView().security.isManager)
            assertNull(next.clientRow(daemon.phone.deviceId))
        }

    @Test
    fun aBlockedStreamReaderDropsAnExcessiveByteBacklog() =
        blockedStreamDrops(ByteArray(1024 * 1024), 9)

    @Test
    fun aBlockedStreamReaderDropsAnOversizedFrame() =
        blockedStreamDrops(ByteArray(2 * 1024 * 1024 + 1), 1)

    private fun blockedStreamDrops(frame: ByteArray, copies: Int) =
        runBlocking<Unit> {
            val blocked = java.util.concurrent.CountDownLatch(1)
            val gate = java.util.concurrent.CountDownLatch(1)
            val acct = approvedClient(daemon.phone, http)
            val hole = StreamBlackhole { socket ->
                socket.getOutputStream().let { out ->
                    val ready =
                        io.github.lewin671.remoteagent.proto.ServerFrame.newBuilder()
                            .setReady(
                                io.github.lewin671.remoteagent.proto.Ready.getDefaultInstance(),
                            )
                            .build()
                            .toByteArray()
                    StreamBlackhole.writeFrame(out, ready)
                    blocked.await()
                    repeat(copies) { StreamBlackhole.writeFrame(out, frame) }
                }
            }
            val routed =
                http
                    .newBuilder()
                    .addInterceptor { chain ->
                        if (chain.request().url.encodedPath == "/v1/stream")
                            return@addInterceptor chain.proceed(chain.request())
                        if (chain.request().url.encodedPath.endsWith("/ListMachines")) {
                            blocked.countDown()
                            gate.await()
                        }
                        chain.proceed(
                            chain
                                .request()
                                .newBuilder()
                                .url(server.url + chain.request().url.encodedPath)
                                .build(),
                        )
                    }
                    .build()
            val r =
                Remote(
                    Account(
                        hole.url,
                        acct.deviceId,
                        acct.accountId,
                    ),
                    routed,
                    securityStore = TestSecurity.get(server.url).stores.getValue(acct.deviceId),
                )
            closers += AutoCloseable { r.close() }
            try {
                r.start()
                while (hole.disconnections.get() == 0) delay(10)
                assertEquals(0L, blocked.count)
                gate.countDown()
                r.coreHost.connectionView.first {
                    hole.attempts.get() > 1 && it.isOnline && it.liveLoaded
                }
            } finally {
                gate.countDown()
                r.close()
                hole.close()
            }
        }

    @Test
    fun aRemovedClientLearnsItAndStopsConnecting() =
        runBlocking<Unit> {
            val holdRetryLogin = java.util.concurrent.atomic.AtomicBoolean(false)
            val retryEntered = CompletableDeferred<Unit>()
            val retryRelease = java.util.concurrent.CountDownLatch(1)
            val gatedHttp =
                http
                    .newBuilder()
                    .addInterceptor { chain ->
                        if (chain.request().url.encodedPath.endsWith("/GetChallenge") &&
                            holdRetryLogin.compareAndSet(true, false)
                        ) {
                            retryEntered.complete(Unit)
                            retryRelease.await()
                        }
                        chain.proceed(chain.request())
                    }
                    .build()
            val r = client(httpClient = gatedHttp)
            val other = client()
            try {
                r.start()
                other.start()
                r.awaitUntil("online") {
                    r.conn.isOnline && r.clientRow(r.account.deviceId) != null
                }
                other.awaitUntil("online") {
                    other.conn.isOnline && other.clientRow(r.account.deviceId) != null
                }
                other.refreshDevices()
                removeAsAuthority(daemon.phone, r.account.deviceId, http)
                val st = r.coreHost.connectionView.first { it.removed }
                assertTrue(st.isOffline)
                // It stays put rather than reconnecting, also when asked to start.
                r.start()
                delay(1500)
                assertTrue(r.conn.removed)
                assertTrue(r.conn.isOffline)
                // Asked to try again, hold the retry login RPC so trying=!removed is observed
                // before the conflated StateFlow returns to removed=true on rejection.
                holdRetryLogin.set(true)
                r.retryLogin()
                retryEntered.await()
                r.coreHost.connectionView.first { !it.removed }
                retryRelease.countDown()
                r.coreHost.connectionView.first { it.removed }
            } finally {
                retryRelease.countDown()
            }
        }

    @Test
    fun syncsListsEventsAndPushes() =
        runBlocking<Unit> {
            val r = client()
            r.start()
            val s1Feed = r.watchSession("s1")
            val s1 = s1Feed.first { r.conn.isOnline && it.header.found }
            assertEquals("fake-mac", r.machineCard(daemon.id)?.name)
            assertTrue(r.machineCard(daemon.id)!!.online)
            assertEquals("修复登录超时", s1.header.title.literal)
            assertEquals("/Users/me/Code/foo", s1.header.cwd)

            val vm = r.awaitSessionTimeline("s1", 3)
            assertEquals(listOf(1L, 2L, 3L), vm.timeline.itemsList.map { it.seq })
            // A pushed event arrives in order, once.
            daemon.upload("s1", agent("第四条"))
            val vm4 = r.awaitSessionTimeline("s1", 4)
            assertEquals(listOf(1L, 2L, 3L, 4L), vm4.timeline.itemsList.map { it.seq })
            // A newer meta revision replaces the older one.
            daemon.putSession("s1", session("/Users/me/Code/foo", "新的标题"))
            s1Feed.first { it.header.title.literal == "新的标题" }
        }

    @Test
    fun rejectedSavedTokenTriggersForceLoginAndReconnects() =
        runBlocking<Unit> {
            val acct = approvedClient(daemon.phone, http)
            val stale = LoginToken("stale-token", System.currentTimeMillis() + 30L * 24 * 60 * 60 * 1000)
            var clears = 0
            var savedToken: LoginToken? = stale
            val baseStore = TestSecurity.get(server.url).stores.getValue(acct.deviceId)
            val tokenStore = object : SecurityStoreV4 by baseStore {
                override fun loadLoginToken(server: String, deviceId: String): LoginToken? = savedToken
                override fun saveLoginToken(server: String, deviceId: String, token: LoginToken) { savedToken = token }
                override fun clearLoginToken(server: String, deviceId: String) {
                    clears++
                }
            }
            val loginCalls = java.util.concurrent.atomic.AtomicInteger()
            val trackedHttp = http.newBuilder().addInterceptor { chain ->
                if (chain.request().url.encodedPath.endsWith("/Login")) loginCalls.incrementAndGet()
                chain.proceed(chain.request())
            }.build()
            val r = fixtureRemote(acct, trackedHttp, securityStore = tokenStore)
            closers += AutoCloseable { r.close() }
            r.start()
            r.watchSession("s1").first { r.conn.isOnline && it.header.found }
            assertTrue(r.conn.isOnline)
            assertTrue("expected fresh Login RPC after 1008 rejection, got ${loginCalls.get()}", loginCalls.get() >= 1)
            assertTrue("expected clearLoginToken called", clears >= 1)
            assertTrue("expected fresh token saved", savedToken != null && savedToken?.value != stale.value)
        }

    @Test
    fun initialSyncOver4096EventsNeverDropsCacheWrites() =
        runBlocking<Unit> {
            val sessionCount = 10
            val eventsPerSession = 460 // 4,600 events total > previous 4,096 CACHE_REF_LIMIT
            val sids = (0 until sessionCount).map { "bulk-$it" }
            for (sid in sids) {
                daemon.putSession(sid, session("/Users/me/Code/bulk", "Bulk $sid"))
                val batch = (1..eventsPerSession).map { seq -> agent("msg-$sid-$seq") }.toTypedArray()
                daemon.upload(sid, *batch)
            }
            val memory = MemoryCache()
            val putGate = CompletableDeferred<Unit>()
            val allCached = CompletableDeferred<Unit>()
            val cache = object : CipherCache by memory {
                override suspend fun putEvents(sessionId: String, events: List<StoredEvent>) {
                    putGate.await()
                    memory.putEvents(sessionId, events)
                    if (sids.all { memory.events(it).size >= eventsPerSession }) {
                        allCached.complete(Unit)
                    }
                }
            }
            val r = client(cache = cache)
            r.start()
            r.coreHost.connectionView.first { it.isOnline && it.liveLoaded }
            // Watch all 10 sessions concurrently while cache disk writes are gated,
            // so >4096 event objects pass through Core before cache writes drain.
            val win = SessionWindowRequest.newBuilder().setLimit(eventsPerSession).build()
            val jobs = sids.map { sid ->
                async {
                    r.watchSession(sid, win).first { it.timeline.itemsCount == eventsPerSession }
                }
            }
            jobs.forEach { it.await() }
            putGate.complete(Unit)
            if (sids.any { memory.events(it).size < eventsPerSession }) {
                allCached.await()
            }
            for (sid in sids) {
                assertEquals("cache missing events for $sid", eventsPerSession, memory.events(sid).size)
            }

            // Cold start against the populated cache restores every event without network.
            val cold = fixtureRemote(r.account, http, memory)
            closers += AutoCloseable { cold.close() }
            cold.start()
            for (sid in sids) {
                val restored = cold.watchSession(sid, win).first { it.timeline.itemsCount == eventsPerSession }
                assertEquals(eventsPerSession, restored.timeline.itemsCount)
            }
        }

    @Test fun verifiedReadTimeoutAndFinalFailuresDoNotRetry() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.awaitUntil("online machine") { r.machineCard(daemon.id)?.online == true }
        for (code in listOf("canceled", "deadline_exceeded", "unknown", "generation_conflict")) {
            var calls = 0
            daemon.answer = { _, _ ->
                calls++
                CommandResult.newBuilder().setErrorCode(code).setErrorMessage("failure").build()
            }
            val command = if (code == "generation_conflict")
                Command.newBuilder().setCompleteImageUpload(CompleteImageUploadRequest.newBuilder().setTransferId("test-transfer-id")).build()
            else Command.newBuilder().setListModels(ListModelsRequest.getDefaultInstance()).build()
            val error = runCatching { r.coreHost.runMachineCommand(daemon.id, "", command) }.exceptionOrNull() as CommandException
            assertEquals(code, error.code)
            assertEquals(1, calls)
        }
    }

    @Test
    fun machineCommandCarriesSessionIdOnlyForSessionScopedCommands() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.awaitUntil("online machine") { r.machineCard(daemon.id)?.online == true }

        val headers = java.util.concurrent.CopyOnWriteArrayList<String>()
        daemon.answer = { header, cmd ->
            headers.add(header.sessionId)
            when (cmd.kindCase) {
                Command.KindCase.OPEN_TERMINAL -> CommandResult.getDefaultInstance()
                else -> CommandResult.newBuilder().setSchedule(ScheduleResponse.getDefaultInstance()).build()
            }
        }
        r.coreHost.runMachineCommand(
            daemon.id,
            "s1",
            Command.newBuilder().setOpenTerminal(
                OpenTerminalRequest.newBuilder().setSessionId("s1").setStreamId("stream-1"),
            ).build(),
        )
        r.coreHost.runMachineCommand(
            daemon.id,
            "",
            Command.newBuilder().setSchedule(ScheduleRequest.newBuilder().setOperation("list").setSessionId("s1")).build(),
        )
        assertEquals(listOf("s1", ""), headers.toList())
    }

    @Test
    fun forwardsCommandsAndReportsOffline() =
        runBlocking<Unit> {
            val r = client()
            r.start()
            val vmFlow = r.watchSession("s1")
            vmFlow.first { r.conn.isOnline && it.header.found && it.header.computerOnline && it.timeline.loaded }
            r.submitSessionCommand(
                "s1",
                Command.newBuilder()
                    .setSend(
                        SendRequest.newBuilder()
                            .setSessionId("s1")
                            .setText("跑一下测试")
                            .setRequestId("q1"),
                    )
                    .build(),
                machineId = daemon.id,
            )
            val (h, cmd) = withContext(Dispatchers.IO) { daemon.commands.take() }
            assertEquals("s1", h.sessionId)
            assertEquals("跑一下测试", cmd.send.text)
            assertEquals(PEER_PROTOCOL_VERSION, cmd.protocolVersion)
            assertEquals(r.account.deviceId, h.sender)
            assertTrue(h.deadlineMs > System.currentTimeMillis())

            // The daemon's own error comes back in the session command slot.
            r.submitSessionCommand(
                "s1",
                Command.newBuilder()
                    .setCompact(
                        io.github.lewin671.remoteagent.proto.CompactRequest.newBuilder()
                            .setSessionId("s1"),
                    )
                    .build(),
                machineId = daemon.id,
            )
            val compactFailed = vmFlow.first {
                it.actions.compact.status == io.github.lewin671.remoteagent.core.proto.SessionCommandStatus.SESSION_COMMAND_STATUS_FAILED
            }
            assertEquals("unimplemented", compactFailed.actions.compact.errorCode)

            daemon.disconnect()
            r.awaitUntil("offline machine") { r.machineCard(daemon.id)?.online == false }
            r.submitSessionCommand(
                "s1",
                Command.newBuilder()
                    .setInterrupt(
                        io.github.lewin671.remoteagent.proto.InterruptRequest.newBuilder()
                            .setSessionId("s1"),
                    )
                    .build(),
                machineId = daemon.id,
            )
            val interruptFailed = vmFlow.first {
                it.actions.interrupt.status == io.github.lewin671.remoteagent.core.proto.SessionCommandStatus.SESSION_COMMAND_STATUS_FAILED
            }
            assertEquals(CommandException.UNAVAILABLE, interruptFailed.actions.interrupt.errorCode)
        }

    private suspend fun refused(protocol: Int): CommandException {
        val r = client()
        r.start()
        r.watchSession("s1").first { r.conn.isOnline && it.header.found && it.header.computerOnline }
        daemon.putMachine("fake-mac.local", protocol)
        val wantMismatch = protocol.compareTo(PEER_PROTOCOL_VERSION)
        r.awaitUntil("machine protocol") {
            r.machineCard(daemon.id)?.protocolMismatch == wantMismatch
        }
        try {
            val command=Command.newBuilder().setGetSettings(io.github.lewin671.remoteagent.proto.GetSettingsRequest.getDefaultInstance()).build()
            r.coreHost.runMachineCommand(daemon.id, "", command)
            throw AssertionError("expected a version error")
        } catch (e: CommandException) {
            // Nothing was sent.
            assertEquals(null, daemon.commands.poll(300, TimeUnit.MILLISECONDS))
            assertFalse(e.fromDaemon)
            return e
        }
    }

    @Test
    fun sendsNothingToAMachineOfAnOlderProtocol() =
        runBlocking<Unit> {
            val e = refused(PEER_PROTOCOL_VERSION - 1)
            assertEquals("failed_precondition", e.code)
            assertEquals("fake-mac 上的 remote-agent 版本较旧，请在电脑上更新后再试", errorText(e).testText())
        }

    @Test
    fun sendsNothingToAMachineOfANewerProtocol() =
        runBlocking<Unit> {
            val e = refused(PEER_PROTOCOL_VERSION + 1)
            assertEquals("failed_precondition", e.code)
            assertEquals(messageTextForTest(UiMessages.errorsClientOutOfDate()), errorText(e).testText())
        }

    @Test
    fun streamsTheTerminal() =
        runBlocking<Unit> {
            val r = client()
            r.start()
            r.watchSession("s1").first { r.conn.isOnline && it.header.found && it.header.computerOnline }
            val frames = LinkedBlockingQueue<TerminalFrame>()
            val conn =
                r.openTerminal(
                    "s1",
                    object : TermListener {
                        override fun onFrame(frame: TerminalFrame) {
                            frames += frame
                        }

                        override fun onClose(reason: MessageText) {}
                    },
                )
            val snap = withContext(Dispatchers.IO) { frames.take() }
            assertEquals(120, snap.snapshot.cols)
            assertEquals(" world", withContext(Dispatchers.IO) { frames.take() }.output.toStringUtf8())
            conn.send(
                TerminalFrame.newBuilder()
                    .setInput(com.google.protobuf.ByteString.copyFromUtf8("中文\r"))
                    .build(),
            )
            conn.send(
                TerminalFrame.newBuilder()
                    .setInput(com.google.protobuf.ByteString.copyFromUtf8("\u001b[A"))
                    .build(),
            )
            assertEquals("中文\r", withContext(Dispatchers.IO) { daemon.termInput.take() }.input.toStringUtf8())
            assertEquals(
                "\u001b[A",
                withContext(Dispatchers.IO) { daemon.termInput.take() }.input.toStringUtf8(),
            )
            conn.close()
        }

    @Test
    fun pairsANewDevice() =
        runBlocking<Unit> {
            val r = fixtureRemote(daemon.phone, http)
            closers += AutoCloseable { r.close() }
            r.start()
            r.coreHost.connectionView.first { it.isOnline }
            val started = r.securityClient.startPairing("https://app.example.com")
            assertTrue(started.url.contains("code=${started.code}"))

            val joinStore = MemorySecurityStoreV4()
            val joinHost = CoreHost()
            val joinExecScope = kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.SupervisorJob() + Dispatchers.IO)
            val joinExec = SecurityExecutor(store = joinStore, http = http, emit = joinHost::dispatch, parentScope = joinExecScope)
            joinHost.onSecurityEffect = joinExec::handleEffect
            joinHost.sendSecurityConfig(account = "", deviceId = "", inlineCrypto = true, foreground = true)
            closers += AutoCloseable {
                joinExec.close()
                joinExecScope.coroutineContext[kotlinx.coroutines.Job]?.cancel()
                joinHost.close()
            }
            joinHost.securityClient.bootstrap()
            val joinDeferred = async {
                joinHost.securityClient.startJoinPairing(rawLink = started.url)
            }
            var polled = started
            while (!polled.joined) {
                delay(20)
                polled = r.securityClient.pollPairing(started.pairingId)
            }
            val joinInfo = joinDeferred.await()
            assertEquals(polled.sasEmojiList, joinInfo.sasEmojiList)
            val approved = async {
                var res = r.securityClient.approvePairing(started.pairingId, sasConfirmed = true)
                while (!res.delivered) {
                    delay(20)
                    res = r.securityClient.finishPairingDelivery(started.pairingId)
                }
                res
            }
            val joined = joinHost.securityClient.confirmJoinSas(joinInfo.pairingId)
            approved.await()
            assertEquals(server.url, joined.status.server)
            assertEquals(daemon.phone.accountId, joined.status.accountId)
            assertFalse(joined.status.isAuthority)
            assertTrue(joined.status.isMember)
        }

    @Test
    fun fetchesAnImageAndCachesOnlyItsEnvelope() =
        runBlocking<Unit> {
            val cache = MemoryCache()
            val r = client(cache)
            r.start()
            r.watchSession("s1").first { r.conn.isOnline && it.header.found }
            val png =
                byteArrayOf(
                    0x89.toByte(),
                    'P'.code.toByte(),
                    'N'.code.toByte(),
                    'G'.code.toByte(),
                    13,
                    10,
                    26,
                    10,
                    1,
                    2,
                    3,
                )
            daemon.putImage(
                "s1",
                "00000000000000000000000000000001",
                io.github.lewin671.remoteagent.proto.ImagePayload.newBuilder()
                    .setFormat(io.github.lewin671.remoteagent.proto.ImageFormat.IMAGE_FORMAT_PNG)
                    .setWidth(1)
                    .setHeight(1)
                    .setData(com.google.protobuf.ByteString.copyFrom(png))
                    .build(),
            )
            val got = r.image("s1", "00000000000000000000000000000001")
            assertTrue(got is Remote.ImageResult.Loaded)
            assertTrue(
                (got as Remote.ImageResult.Loaded).payload.data.toByteArray().contentEquals(png),
            )

            // What the cache holds is the sealed envelope, not the picture.
            val stored = cache.images.getValue("s1/00000000000000000000000000000001")
            assertFalse(
                String(stored, Charsets.ISO_8859_1).contains(String(png, Charsets.ISO_8859_1)),
            )
            assertEquals(
                Remote.ImageResult.Missing,
                r.image("s1", "ffffffffffffffffffffffffffffffff"),
            )
            // A format that does not match its bytes is refused.
            daemon.putImage(
                "s1",
                "00000000000000000000000000000002",
                io.github.lewin671.remoteagent.proto.ImagePayload.newBuilder()
                    .setFormat(io.github.lewin671.remoteagent.proto.ImageFormat.IMAGE_FORMAT_JPEG)
                    .setData(com.google.protobuf.ByteString.copyFrom(png))
                    .build(),
            )
            assertTrue(
                r.image("s1", "00000000000000000000000000000002") is Remote.ImageResult.Corrupt,
            )
        }

    @Test
    fun twoClientsChangingProjectsKeepBothChanges() =
        runBlocking<Unit> {
            val a = client()
            val b = client()
            a.start()
            b.start()
            a.awaitUntil("a prefs") { a.conn.isOnline && !a.projectListing().loading }
            b.awaitUntil("b prefs") { b.conn.isOnline && !b.projectListing().loading }
            a.coreHost.toggleProjectPin("/Users/me/Code/foo")
            a.awaitUntil("a pinned") { a.isProjectPinned("/Users/me/Code/foo") }
            b.coreHost.toggleProjectHide("/Users/me/Code/billing")
            a.awaitUntil("a sees both") {
                a.isProjectHidden("/Users/me/Code/billing")
            }
            assertEquals(listOf("/Users/me/Code/foo"), a.pinnedPaths())
            assertTrue(a.isProjectHidden("/Users/me/Code/billing"))
        }

    @Test
    fun preferencesThatCouldNotBeReadAreReadAgainWhileTheConnectionHolds() =
        runBlocking<Unit> {
            val reads = java.util.concurrent.atomic.AtomicInteger()
            val flaky =
                http
                    .newBuilder()
                    .addInterceptor { chain ->
                        if (
                            chain.request().url.encodedPath.endsWith("/GetAccountPrefs") &&
                                reads.incrementAndGet() == 1
                        )
                            throw java.io.IOException("connection lost")
                        chain.proceed(chain.request())
                    }
                    .build()
            val r = fixtureRemote(approvedClient(daemon.phone, http), flaky)
            closers += AutoCloseable { r.close() }
            r.start()
            // The stream stays up throughout: nothing but the read itself brings them.
            r.awaitUntil("preferences") { r.conn.isOnline && !r.projectListing().loading && reads.get() >= 2 }
            assertTrue(r.conn.isOnline)
            assertEquals(2, reads.get())
        }

    @Test
    fun aClientsNameReachesTheOtherClients() =
        runBlocking<Unit> {
            val a = client()
            val b = client()
            a.start()
            b.start()
            a.awaitUntil("a devices") {
                a.conn.isOnline && a.clientRow(a.account.deviceId) != null
            }
            b.awaitUntil("b sees a") { b.clientRow(a.account.deviceId) != null }
            a.renameDevice(a.account.deviceId, "我的手机")
            b.awaitUntil("b sees the name") {
                b.clientRow(a.account.deviceId)?.displayName == "我的手机"
            }
            assertEquals("我的手机", b.clientRow(a.account.deviceId)?.displayName)
        }

    @Test fun scheduledSnapshotsKeepSignedRepliesAndVerifyThemAgainOffline() = runBlocking<Unit> {
        daemon.putMachine("fake-mac.local", scheduledTasks = true)
        val originalAnswer = daemon.answer
        val json = """{"tasks":[{"id":"task","spec":{"title":"Private scheduled content"}}]}""".toByteArray()
        daemon.answer = { h, c -> if (c.hasSchedule()) io.github.lewin671.remoteagent.proto.CommandResult.newBuilder().setSchedule(io.github.lewin671.remoteagent.proto.ScheduleResponse.newBuilder().setResultJson(com.google.protobuf.ByteString.copyFrom(json))).build() else originalAnswer(h, c) }
        val memory = MemoryCache(); val r = client(cache = memory); r.start()
        r.awaitUntil("connected") { r.conn.isOnline && r.machineCard(daemon.account.deviceId)?.online == true }
        r.coreHost.refreshSchedules(daemon.account.deviceId)
        val key = "schedule:${daemon.account.deviceId}"
        r.awaitUntil("schedule cache written") { memory.blobs[key] != null }
        val stored = memory.blob(key)!!
        assertFalse(stored.decodeToString().contains("Private scheduled content"))
        val blob = io.github.lewin671.remoteagent.core.proto.ScheduleCacheBlob.parseFrom(stored)
        assertTrue(blob.requestId.isNotEmpty())
        val env = io.github.lewin671.remoteagent.proto.Envelope.parseFrom(blob.sealedResult)
        assertFalse(env.signature.isEmpty); assertFalse(env.securityHeader.isEmpty)

        fun allTasks(vm: io.github.lewin671.remoteagent.core.proto.SchedulesViewModel) =
            vm.attention14List + vm.freshList + vm.othersList + vm.endedList

        // Make daemon schedule.list fail so cold instances can only restore from the verified opaque cache.
        daemon.answer = { h, c -> if (c.hasSchedule()) throw java.io.IOException("offline") else originalAnswer(h, c) }

        // Wrong request_id or tampered signature must not restore cached schedules on cold start.
        memory.putBlob(key, blob.toBuilder().setRequestId("wrong-request").build().toByteArray())
        val badReq = fixtureRemote(r.account, http, memory); closers += AutoCloseable { badReq.close() }
        badReq.start()
        badReq.awaitUntil("badReq online") { badReq.conn.isOnline && badReq.machineCard(daemon.account.deviceId) != null }
        val badReqSub = launch { badReq.coreHost.schedulesView.collect {} }
        badReq.coreHost.openScheduleList(true)
        delay(250)
        assertTrue(allTasks(badReq.coreHost.schedulesView.value).isEmpty())
        badReqSub.cancel()
        badReq.close()

        memory.putBlob(
            key,
            blob.toBuilder()
                .setSealedResult(env.toBuilder().setSignature(com.google.protobuf.ByteString.copyFrom(ByteArray(64))).build().toByteString())
                .build()
                .toByteArray(),
        )
        val badSig = fixtureRemote(r.account, http, memory); closers += AutoCloseable { badSig.close() }
        badSig.start()
        badSig.awaitUntil("badSig online") { badSig.conn.isOnline && badSig.machineCard(daemon.account.deviceId) != null }
        val badSigSub = launch { badSig.coreHost.schedulesView.collect {} }
        badSig.coreHost.openScheduleList(true)
        delay(250)
        assertTrue(allTasks(badSig.coreHost.schedulesView.value).isEmpty())
        badSigSub.cancel()
        badSig.close()

        // Valid opaque blob restores schedule task offline on cold start without hitting the daemon.
        // Subscribe only AFTER openScheduleList(true) to prove lazy sub-screens proactively refresh on first subscribe.
        memory.putBlob(key, stored)
        val cold = fixtureRemote(r.account, http, memory); closers += AutoCloseable { cold.close() }
        cold.start()
        cold.awaitUntil("cold online") { cold.conn.isOnline && cold.machineCard(daemon.account.deviceId) != null }
        cold.coreHost.openScheduleList(true)
        withTimeout(10_000L) {
            cold.coreHost.schedulesView.first { vm -> allTasks(vm).any { it.taskId == "task" && it.title == "Private scheduled content" } }
        }
        cold.close()

        // Removing the machine prunes its persisted schedule cache blob.
        removeAsAuthority(daemon.phone, daemon.account.deviceId, http)
        r.syncSecurity()
        r.awaitUntil("schedule cache pruned") { memory.blobs[key] == null }
        assertNull(memory.blob(key))
    }

    @Test fun coreRejectsUnsequencedZeroOpDevicesEvent() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.coreHost.connectionView.first { it.isOnline }
        r.awaitUntil("devices loaded") { r.clientRow(r.account.deviceId) != null }

        // Shell refreshDevices sends a monotonic push_seq and keeps devices populated.
        r.refreshDevices()
        assertTrue(r.clientRow(r.account.deviceId) != null)

        // Dispatching op_id=0, push_seq=0 DevicesFetched must be ignored by core.
        r.coreHost.dispatchAndWait(
            io.github.lewin671.remoteagent.core.proto.Event.newBuilder()
                .setDevicesFetched(
                    io.github.lewin671.remoteagent.core.proto.DevicesFetchedEvent.newBuilder()
                        .setOpId(0L)
                        .setPushSeq(0L),
                )
                .build(),
        )
        assertTrue(r.clientRow(r.account.deviceId) != null)
    }

}

/** A CipherCache in memory, as the Room one behaves. */
class MemoryCache : CipherCache {
    val blobs = ConcurrentHashMap<String, ByteArray>()
    override suspend fun blob(name: String) = blobs[name]
    override suspend fun putBlob(name: String, data: ByteArray) { blobs[name] = data }
    override suspend fun removeBlob(name: String) { blobs.remove(name) }
    val images = ConcurrentHashMap<String, ByteArray>()

    override suspend fun image(key: String) = images[key]

    override suspend fun putImage(key: String, envelope: ByteArray) {
        images[key] = envelope
    }

    private val machines = ConcurrentHashMap<String, Machine>()
    private val sessions = ConcurrentHashMap<String, RemoteSession>()
    private val events = ConcurrentHashMap<String, java.util.TreeMap<Long, StoredEvent>>()

    override suspend fun machines() = machines.values.toList()

    override suspend fun sessions() = sessions.values.toList()

    /** Every stored event of a session, for checking what the cache holds. */
    fun events(sessionId: String): List<StoredEvent> =
        events[sessionId]?.let { m -> synchronized(m) { m.values.toList() } }.orEmpty()

    override suspend fun newestEvents(sessionId: String): List<StoredEvent> =
        events[sessionId]?.let { m ->
            synchronized(m) { m.descendingMap().values.take(CACHE_EVENT_PAGE).reversed() }
        }.orEmpty()

    override suspend fun eventPage(sessionId: String, afterSeq: Long): List<StoredEvent> =
        events[sessionId]?.let { m ->
            synchronized(m) { m.tailMap(afterSeq, false).values.take(CACHE_EVENT_PAGE) }
        }.orEmpty()

    override suspend fun putMachine(m: Machine) {
        machines[m.id] = m
    }

    override suspend fun putSession(s: RemoteSession) {
        sessions[s.sessionId] = s
    }

    override suspend fun putEvents(sessionId: String, events: List<StoredEvent>) {
        val m = this.events.getOrPut(sessionId) { java.util.TreeMap(java.lang.Long::compareUnsigned) }
        // An event already stored is left as it is, as in the app's database.
        synchronized(m) { for (e in events) m.putIfAbsent(e.seq, e) }
    }

    override suspend fun dropEvents(sessionId: String) {
        events.remove(sessionId)
    }

    /** Changes a stored event behind the cache's back, as a damaged row would be. */
    fun damage(sessionId: String, seq: Long, change: (StoredEvent) -> StoredEvent) {
        val m = events[sessionId] ?: return
        synchronized(m) { m[seq]?.let { m[seq] = change(it) } }
    }

    override suspend fun removeMachine(id: String) {
        machines.remove(id)
        blobs.remove("schedule:$id")
        sessions.values
            .filter { it.machineId == id }
            .forEach {
                sessions.remove(it.sessionId)
                events.remove(it.sessionId)
            }
    }

    override suspend fun removeSession(id: String) {
        sessions.remove(id)
        events.remove(id)
    }

    override suspend fun unlistSession(id: String) {
        sessions.remove(id)
    }
}
