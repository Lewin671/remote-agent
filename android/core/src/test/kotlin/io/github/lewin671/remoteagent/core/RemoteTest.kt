package io.github.lewin671.remoteagent.core

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
import kotlinx.coroutines.async
import kotlinx.coroutines.launch
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Before
import org.junit.Test
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit

/** The sync engine against the real Go server and a fake daemon. */
class RemoteTest {
    private lateinit var server: TestServer
    private val http = Api.defaultHttp()
    private val root = randomBytes(32)
    private lateinit var daemon: FakeDaemon
    private val closers = mutableListOf<AutoCloseable>()

    @Before
    fun setUp() = runBlocking<Unit> {
        server = TestServer()
        daemon = FakeDaemon.create(root, server.url, http, server.setupToken)
        daemon.putMachine("fake-mac.local")
        daemon.putSession("s1", session("/Users/me/Code/foo", "修复登录超时"))
        daemon.upload("s1", user("你好"), agent("你好，有什么要做的？"), agent("第三条"))
        daemon.connect()
    }

    @After
    fun tearDown() {
        closers.forEach { it.close() }
        daemon.close()
        server.close()
    }

    private fun session(cwd: String, title: String) =
        Session.newBuilder().setCwd(cwd).setTitle(title).setStatus(SessionStatus.SESSION_STATUS_IDLE).build()

    private fun user(t: String) = Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText(t))
    private fun agent(t: String) = Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText(t))

    private suspend fun client(cache: CipherCache? = null, frameQueue: Int = 1024, historyIdleMs: Long = 60_000, historyIdleCount: Int = 8, linkGraceMs: Long = 10_000): Remote {
        val acct = joinWithRoot(root, server.url, http)
        val r = Remote(acct, http, cache, frameQueue = frameQueue, historyIdleMs = historyIdleMs, historyIdleCount = historyIdleCount, linkGraceMs = linkGraceMs)
        closers += AutoCloseable { r.close() }
        return r
    }

    private suspend fun Remote.awaitState(what: String, ok: (RemoteState) -> Boolean): RemoteState =
        try {
            withTimeout(10_000) { state.first(ok) }
        } catch (e: Exception) {
            fail("timed out waiting for $what: ${state.value}")
            throw e
        }

    private suspend fun Remote.awaitEvents(sid: String, n: Int): SessionEvents =
        withTimeout(10_000) { events(sid).first { it.events.size >= n } }

    @Test
    fun aRemovedClientLearnsItAndStopsConnecting() = runBlocking<Unit> {
        val r = client()
        val other = client()
        r.start(); other.start()
        r.awaitState("online") { it.conn == ConnState.ONLINE }
        other.awaitState("online") { it.conn == ConnState.ONLINE }
        other.removeDevice(r.account.deviceId)
        val st = r.awaitState("removed") { it.removed }
        assertEquals(ConnState.OFFLINE, st.conn)
        // It stays put rather than reconnecting, also when asked to start.
        r.start()
        delay(1500)
        assertTrue(r.state.value.removed)
        assertEquals(ConnState.OFFLINE, r.state.value.conn)
        // Asked to try again, it is turned away again.
        r.retryLogin()
        r.awaitState("trying") { !it.removed }
        r.awaitState("removed again") { it.removed }
    }

    @Test
    fun activeHistorySurvivesExpiryAndTheSameFlowReloadsAfterReclamation() = runBlocking<Unit> {
        val r = client(MemoryCache(), historyIdleMs = 100)
        r.start()
        val flow = r.events("s1") // Subscribe before the session list exists.
        val subscriber = launch { flow.collect {} }
        withTimeout(10_000) { flow.first { it.events.size == 3 } }
        val old = flow.value.events
        delay(350)
        assertEquals(3, flow.value.events.size)
        subscriber.cancelAndJoin()
        withTimeout(5_000) { while (flow.value.events.isNotEmpty()) delay(10) }
        val restored = withTimeout(5_000) { flow.first { it.events.size == 3 } }
        assertEquals(listOf(1L, 2L, 3L), restored.events.map { it.seq })
        assertEquals(3, old.size)
    }

    @Test
    fun idleBudgetDoesNotReclaimAnActiveConsumer() = runBlocking<Unit> {
        daemon.putSession("s2", session("/tmp", "two")); daemon.upload("s2", agent("two"))
        daemon.putSession("s3", session("/tmp", "three")); daemon.upload("s3", agent("three"))
        val r = client(MemoryCache(), historyIdleCount = 1)
        r.start()
        val active = r.events("s1")
        val subscriber = launch { active.collect {} }
        try {
            withTimeout(10_000) { active.first { it.events.size == 3 } }
            val second = r.events("s2")
            withTimeout(10_000) { second.first { it.events.size == 1 } }
            val third = r.events("s3")
            withTimeout(10_000) { third.first { it.events.size == 1 } }
            withTimeout(5_000) { while (second.value.events.isNotEmpty()) delay(10) }
            assertEquals(3, active.value.events.size)
        } finally { subscriber.cancelAndJoin() }
    }

    @Test
    fun aReclaimedLoadingFlowCanReloadWithoutWaitingForItsOldCacheRead() = runBlocking<Unit> {
        val base = MemoryCache()
        val source = client(base)
        source.start(); source.awaitEvents("s1", 3); source.stop()
        val gate = kotlinx.coroutines.CompletableDeferred<Unit>()
        val blocked = kotlinx.coroutines.CompletableDeferred<Unit>()
        val calls = java.util.concurrent.atomic.AtomicInteger()
        val cache = object : CipherCache by base {
            override suspend fun events(sessionId: String): List<StoredEvent> {
                if (calls.getAndIncrement() == 1) {
                    blocked.complete(Unit); gate.await()
                    return base.events(sessionId).take(1)
                }
                return base.events(sessionId)
            }
        }
        val r = client(cache, historyIdleMs = 100)
        r.start(); r.awaitState("cached session") { it.sessions["s1"] != null }
        val flow = r.events("s1")
        var subscriber = launch { flow.collect {} }
        try {
            // The eager events(sid) call completes normally. Later loads are
            // triggered solely by subscriptions to this same flow.
            withTimeout(5_000) { flow.first { it.events.size == 3 } }
            subscriber.cancelAndJoin()
            withTimeout(5_000) { while (flow.value.events.isNotEmpty()) delay(10) }
            subscriber = launch { flow.collect {} }
            withTimeout(5_000) { blocked.await() }
            subscriber.cancelAndJoin()
            delay(350) // Reclaim the unobserved, still-suspended buffer.
            subscriber = launch { flow.collect {} }
            withTimeout(5_000) { flow.first { it.events.size == 3 } }
            assertTrue(calls.get() >= 3)
            gate.complete(Unit)
            delay(150)
            assertEquals(listOf(1L, 2L, 3L), flow.value.events.map { it.seq })
        } finally { gate.complete(Unit); subscriber.cancelAndJoin() }
    }

    @Test
    fun syncsListsEventsAndPushes() = runBlocking<Unit> {
        val r = client()
        r.start()
        val st = r.awaitState("session meta") { it.conn == ConnState.ONLINE && it.sessions["s1"]?.info != null }
        assertEquals("fake-mac", machineName(st.machines[daemon.id]))
        assertTrue(st.machines[daemon.id]!!.online)
        val s = st.sessions.getValue("s1")
        assertEquals("修复登录超时", titleOf(s))
        assertEquals("foo", projects(st).single().name)

        val ev = r.awaitEvents("s1", 3)
        assertEquals(listOf(1L, 2L, 3L), ev.events.map { it.seq })
        // A pushed event arrives in order, once.
        daemon.upload("s1", agent("第四条"))
        val ev4 = r.awaitEvents("s1", 4)
        assertEquals(listOf(1L, 2L, 3L, 4L), ev4.events.map { it.seq })
        // A newer meta revision replaces the older one.
        daemon.putSession("s1", session("/Users/me/Code/foo", "新的标题"))
        r.awaitState("new title") { it.sessions["s1"]?.info?.title == "新的标题" }
    }

    @Test
    fun forwardsCommandsAndReportsOffline() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.awaitState("online machine") { it.conn == ConnState.ONLINE && it.machines[daemon.id]?.online == true && it.sessions["s1"]?.info != null }
        val res = r.command("s1", Command.newBuilder().setSend(SendRequest.newBuilder().setSessionId("s1").setText("跑一下测试").setRequestId("q1")).build())
        assertEquals(1, res.send.queuePosition)
        val (h, cmd) = daemon.commands.poll(5, TimeUnit.SECONDS)!!
        assertEquals("s1", h.sessionId)
        assertEquals("跑一下测试", cmd.send.text)
        assertEquals(PROTOCOL_VERSION, cmd.protocolVersion)
        assertEquals(r.account.deviceId, h.sender)
        assertTrue(h.deadlineMs > System.currentTimeMillis())

        // The daemon's own error comes back as it is.
        try {
            r.command("s1", Command.newBuilder().setCompact(io.github.lewin671.remoteagent.proto.CompactRequest.newBuilder().setSessionId("s1")).build())
            fail("expected an error")
        } catch (e: CommandException) {
            assertEquals("unimplemented", e.code)
        }

        daemon.disconnect()
        r.awaitState("offline machine") { it.machines[daemon.id]?.online == false }
        try {
            r.command("s1", Command.newBuilder().setInterrupt(io.github.lewin671.remoteagent.proto.InterruptRequest.newBuilder().setSessionId("s1")).build())
            fail("expected unavailable")
        } catch (e: CommandException) {
            assertEquals(CommandException.UNAVAILABLE, e.code)
        }
    }

    private suspend fun refused(protocol: Int): CommandException {
        val r = client()
        r.start()
        r.awaitState("online machine") { it.conn == ConnState.ONLINE && it.machines[daemon.id]?.online == true && it.sessions["s1"]?.info != null }
        daemon.putMachine("fake-mac.local", protocol)
        r.awaitState("machine protocol") { it.machines[daemon.id]?.meta?.protocolVersion == protocol }
        try {
            r.command("s1", Command.newBuilder().setInterrupt(io.github.lewin671.remoteagent.proto.InterruptRequest.newBuilder().setSessionId("s1")).build())
            throw AssertionError("expected a version error")
        } catch (e: CommandException) {
            // Nothing was sent.
            assertEquals(null, daemon.commands.poll(300, TimeUnit.MILLISECONDS))
            assertFalse(e.fromDaemon)
            return e
        }
    }

    @Test
    fun sendsNothingToAMachineOfAnOlderProtocol() = runBlocking<Unit> {
        val e = refused(PROTOCOL_VERSION - 1)
        assertEquals("failed_precondition", e.code)
        assertEquals("fake-mac 上的 remote-agent 版本较旧，请在电脑上更新后再试", e.message)
    }

    @Test
    fun sendsNothingToAMachineOfANewerProtocol() = runBlocking<Unit> {
        val e = refused(PROTOCOL_VERSION + 1)
        assertEquals("failed_precondition", e.code)
        assertEquals("这个 App 版本较旧，请更新后再试", e.message)
    }

    @Test
    fun streamsTheTerminal() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.awaitState("session") { it.conn == ConnState.ONLINE && it.sessions["s1"] != null && it.machines[daemon.id]?.online == true }
        val frames = LinkedBlockingQueue<TerminalFrame>()
        val conn = r.openTerminal("s1", object : TermListener {
            override fun onFrame(frame: TerminalFrame) {
                frames += frame
            }

            override fun onClose(reason: String) {}
        })
        val snap = frames.poll(5, TimeUnit.SECONDS)!!
        assertEquals(120, snap.snapshot.cols)
        assertEquals(" world", frames.poll(5, TimeUnit.SECONDS)!!.output.toStringUtf8())
        conn.send(TerminalFrame.newBuilder().setInput(com.google.protobuf.ByteString.copyFromUtf8("中文\r")).build())
        conn.send(TerminalFrame.newBuilder().setInput(com.google.protobuf.ByteString.copyFromUtf8("\u001b[A")).build())
        assertEquals("中文\r", daemon.termInput.poll(5, TimeUnit.SECONDS)!!.input.toStringUtf8())
        assertEquals("\u001b[A", daemon.termInput.poll(5, TimeUnit.SECONDS)!!.input.toStringUtf8())
        conn.close()
    }

    @Test
    fun pairsANewDevice() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.awaitState("online") { it.conn == ConnState.ONLINE }
        val init = Initiation.start(r)
        val link = PairLink.parse(init.link.url("https://app.example.com"))!!
        assertEquals(init.code, link.code)
        var joinerEmoji = emptyList<String>()
        val joining = async { joinByPairing(link, http, { joinerEmoji = it }, { false }) }
        val (emoji, _) = init.joined()
        init.approve()
        val acct = joining.await()
        assertEquals(emoji, joinerEmoji)
        assertArrayEquals(root, acct.root)
        assertEquals(server.url, acct.server)
    }

    @Test
    fun comingBackReusesTheLogin() = runBlocking<Unit> {
        val logins = java.util.concurrent.atomic.AtomicInteger()
        val counting = http.newBuilder().addInterceptor { chain ->
            if (chain.request().url.encodedPath.endsWith("/Login")) logins.incrementAndGet()
            chain.proceed(chain.request())
        }.build()
        val r = Remote(joinWithRoot(root, server.url, http), counting)
        closers += AutoCloseable { r.close() }
        r.start()
        r.awaitState("online") { it.conn == ConnState.ONLINE }
        assertEquals(1, logins.get())
        // The app went to the background and came back: no second login.
        r.stop()
        r.awaitState("offline") { it.conn == ConnState.OFFLINE }
        r.start()
        r.awaitState("online again") { it.conn == ConnState.ONLINE }
        assertEquals(1, logins.get())
    }

    @Test
    fun aStreamWithoutReadyIsReplacedEvenThoughTheUpgradeSucceeded() = runBlocking<Unit> {
        val acct = joinWithRoot(root, server.url, http)
        val hole = StreamBlackhole()
        closers += hole
        // Only the stream goes to the fixture; auth and list RPCs use the real server.
        val routed = http.newBuilder().addInterceptor { chain ->
            val request = chain.request()
            val target = if (request.url.encodedPath == "/v1/stream") request else
                request.newBuilder().url(server.url + request.url.encodedPath).build()
            chain.proceed(target)
        }.build()
        val r = Remote(Account(hole.url, acct.root, acct.deviceSeed, acct.deviceId), routed, streamOpenMs = 300)
        closers += AutoCloseable { r.close() }
        r.start()
        r.awaitState("missing Ready timeout") { it.conn == ConnState.OFFLINE && it.error.contains("超时") }
        r.reconnectNow()
        r.awaitState("replacement ready") { it.conn == ConnState.ONLINE }
        assertEquals(2, hole.attempts.get())
    }

    @Test
    fun repeatedNetworkCallbacksDuringLoginLeaveOnlyOneCurrentStream() = runBlocking<Unit> {
        val acct = joinWithRoot(root, server.url, http)
        val gate = java.util.concurrent.CountDownLatch(1)
        val entered = java.util.concurrent.CountDownLatch(1)
        val challenges = java.util.concurrent.atomic.AtomicInteger()
        val streams = java.util.concurrent.atomic.AtomicInteger()
        val blocked = http.newBuilder().addInterceptor { chain ->
            if (chain.request().url.encodedPath.endsWith("/GetChallenge")) {
                challenges.incrementAndGet(); entered.countDown()
                while (!gate.await(10, TimeUnit.MILLISECONDS)) {
                    if (chain.call().isCanceled()) throw java.io.IOException("cancelled")
                }
            }
            if (chain.request().url.encodedPath == "/v1/stream") streams.incrementAndGet()
            chain.proceed(chain.request())
        }.build()
        val r = Remote(acct, blocked)
        closers += AutoCloseable { r.close(); gate.countDown() }
        r.start()
        assertTrue(entered.await(5, TimeUnit.SECONDS))
        repeat(4) { r.reconnectNow() }
        delay(200)
        gate.countDown()
        r.awaitState("one online stream") { it.conn == ConnState.ONLINE && it.loaded }
        delay(200)
        assertEquals(1, streams.get())
        assertTrue(challenges.get() >= 1)
    }

    @Test
    fun aLoginThatDoesNotReturnTimesOutAndCanRecover() = runBlocking<Unit> {
        val acct = joinWithRoot(root, server.url, http)
        val blocked = java.util.concurrent.atomic.AtomicBoolean(true)
        val attempts = java.util.concurrent.atomic.AtomicInteger()
        val slow = http.newBuilder().addInterceptor { chain ->
            if (chain.request().url.encodedPath.endsWith("/GetChallenge")) {
                attempts.incrementAndGet()
                while (blocked.get()) {
                    if (chain.call().isCanceled()) throw java.io.IOException("cancelled")
                    Thread.sleep(10)
                }
            }
            chain.proceed(chain.request())
        }.build()
        val r = Remote(acct, slow, streamOpenMs = 300)
        closers += AutoCloseable { blocked.set(false); r.close() }
        r.start()
        r.awaitState("login timeout") { it.conn == ConnState.OFFLINE }
        assertFalse(r.state.value.removed)
        blocked.set(false); r.reconnectNow()
        r.awaitState("login recovery") { it.conn == ConnState.ONLINE }
        assertTrue(attempts.get() >= 2)
    }

    @Test
    fun networkChangeReplacesAnOnlineStreamAndCatchesUpExactlyOnce() = runBlocking<Unit> {
        val acct = joinWithRoot(root, server.url, http)
        val streams = java.util.concurrent.atomic.AtomicInteger()
        val observed = http.newBuilder().addInterceptor { chain ->
            if (chain.request().url.encodedPath == "/v1/stream") streams.incrementAndGet()
            chain.proceed(chain.request())
        }.build()
        val r = Remote(acct, observed)
        closers += AutoCloseable { r.close() }
        r.start(); r.awaitEvents("s1", 3)
        r.reconnectNow()
        r.awaitState("network replacement") { it.conn == ConnState.CONNECTING }
        daemon.upload("s1", agent("during replacement"))
        val events = r.awaitEvents("s1", 4).events
        assertEquals(listOf(1L, 2L, 3L, 4L), events.map { it.seq })
        r.awaitState("online again") { it.conn == ConnState.ONLINE }
        assertEquals(2, streams.get())
    }

    @Test
    fun reconnectsWithoutLosingOrRepeatingEvents() = runBlocking<Unit> {
        val cache = MemoryCache()
        val r = client(cache)
        r.start()
        r.awaitEvents("s1", 3)
        server.stop()
        r.awaitState("offline") { it.conn != ConnState.ONLINE }
        server.start()
        daemon.login()
        daemon.connect()
        daemon.upload("s1", agent("断线期间"), agent("又一条"))
        withTimeout(40_000) { r.state.first { it.conn == ConnState.ONLINE } }
        val ev = withTimeout(10_000) { r.events("s1").first { it.events.size >= 5 } }
        assertEquals(listOf(1L, 2L, 3L, 4L, 5L), ev.events.map { it.seq })

        // A new run shows the cached history before it is online.
        r.stop()
        server.stop()
        val again = Remote(r.account, http, cache)
        closers += AutoCloseable { again.close() }
        again.start()
        again.awaitState("cached session") { it.sessions["s1"]?.info != null }
        val cached = withTimeout(10_000) { again.events("s1").first { it.events.size >= 5 } }
        assertEquals(5, cached.events.size)
        assertEquals(false, again.state.value.machines[daemon.id]!!.online)
    }

    @Test
    fun aSlowCacheWriteDoesNotRollBackNewerMetadata() = runBlocking<Unit> {
        val gate = kotlinx.coroutines.CompletableDeferred<Unit>()
        val blocked = kotlinx.coroutines.CompletableDeferred<Unit>()
        val cache = object : CipherCache by MemoryCache() {
            override suspend fun putEvents(sessionId: String, events: List<StoredEvent>) {
                blocked.complete(Unit)
                gate.await()
            }
        }
        val r = client(cache)
        r.start()
        r.awaitState("session") { it.conn == ConnState.ONLINE && it.sessions["s1"]?.info != null }
        // Loading the history writes it to the cache, which hangs here.
        r.events("s1")
        withTimeout(10_000) { blocked.await() }
        // Meanwhile newer metadata arrives through the stream.
        daemon.putSession("s1", session("/Users/me/Code/foo", "新的标题"))
        r.awaitState("new title") { it.sessions["s1"]?.info?.title == "新的标题" }
        gate.complete(Unit)
        r.awaitEvents("s1", 3)
        kotlinx.coroutines.delay(200)
        assertEquals("新的标题", r.state.value.sessions["s1"]?.info?.title)
    }

    @Test
    fun aSlowCacheReadDoesNotOverwriteTheListsOfALaterConnection() = runBlocking<Unit> {
        val base = MemoryCache()
        val source = client(base)
        source.start()
        source.awaitState("lists") { it.conn == ConnState.ONLINE && it.sessions["s1"]?.info != null }
        source.stop()
        val gate = kotlinx.coroutines.CompletableDeferred<Unit>()
        val blocked = kotlinx.coroutines.CompletableDeferred<Unit>()
        val cache = object : CipherCache by base {
            override suspend fun machines(): List<Machine> {
                blocked.complete(Unit)
                gate.await()
                return base.machines()
            }
        }
        val r = client(cache)
        try {
            r.start()
            withTimeout(10_000) { blocked.await() }
            // The app went to the background and came back, and the network changed, while the cache was being read.
            r.stop()
            r.start()
            r.setNetwork(false)
            r.setNetwork(true)
            delay(1_000)
            assertFalse(r.state.value.conn == ConnState.ONLINE)
            gate.complete(Unit)
            r.awaitState("the machine online") { it.conn == ConnState.ONLINE && it.machines[daemon.id]?.online == true }
            delay(200)
            assertEquals(true, r.state.value.machines[daemon.id]?.online)
        } finally { gate.complete(Unit) }
    }

    @Test
    fun aFrozenAttemptResendsTheSameRequest() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.awaitState("online machine") { it.conn == ConnState.ONLINE && it.machines[daemon.id]?.online == true }
        val a = Attempt()
        val cmd = Command.newBuilder().setInterrupt(io.github.lewin671.remoteagent.proto.InterruptRequest.newBuilder().setSessionId("s1")).build()
        val first = a.next { r.prepare(daemon.id, "s1", cmd) }
        r.forward(first)
        // Pretend the answer was lost: the retry carries the same request ID and deadline.
        a.settle(CommandException(CommandException.UNKNOWN, "lost"))
        r.forward(a.next { r.prepare(daemon.id, "s1", cmd) })
        val (h1, _) = daemon.commands.poll(5, TimeUnit.SECONDS)!!
        val (h2, _) = daemon.commands.poll(5, TimeUnit.SECONDS)!!
        assertEquals(h1.requestId, h2.requestId)
        assertEquals(h1.deadlineMs, h2.deadlineMs)
    }

    @Test
    fun anUnknownOutcomeStaysUnknownWhenTheMachineThenGoesOffline() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.awaitState("online machine") { it.conn == ConnState.ONLINE && it.machines[daemon.id]?.online == true }
        // The daemon takes the command and drops off before answering: it may
        // have run. The retry then finds the machine offline, which says
        // nothing about the first try.
        daemon.answer = { _, c ->
            daemon.disconnect()
            FakeDaemon.defaultResult(c)
        }
        val cmd = Command.newBuilder().setInterrupt(io.github.lewin671.remoteagent.proto.InterruptRequest.newBuilder().setSessionId("s1")).build()
        try {
            r.forward(r.prepare(daemon.id, "s1", cmd))
            fail("expected an error")
        } catch (e: CommandException) {
            assertEquals(CommandException.UNKNOWN, e.code)
        }
    }

    @Test
    fun aFailedLoginWhileRetryingAnUnknownCommandKeepsItUnknown() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.awaitState("online") { it.conn == ConnState.ONLINE }
        val cmd = Command.newBuilder().setInterrupt(io.github.lewin671.remoteagent.proto.InterruptRequest.newBuilder().setSessionId("s1")).build()
        val p = r.prepare(daemon.id, "s1", cmd)
        // The login is refused from here on: the retry is told to log in
        // again, and that fails. The command it retries may have run.
        r.removeDevice(r.account.deviceId)
        try {
            r.forward(p, retryOfUnknown = true)
            fail("expected an error")
        } catch (e: CommandException) {
            assertEquals(CommandException.UNKNOWN, e.code)
        }
    }

    @Test
    fun aResultThatDoesNotOpenLeavesTheOutcomeUnknown() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.awaitState("online machine") { it.conn == ConnState.ONLINE && it.machines[daemon.id]?.online == true }
        daemon.garbleResults = true
        val a = Attempt()
        val cmd = Command.newBuilder().setInterrupt(io.github.lewin671.remoteagent.proto.InterruptRequest.newBuilder().setSessionId("s1")).build()
        val first = a.next { r.prepare(daemon.id, "s1", cmd) }
        val shown = try {
            r.forward(first)
            null
        } catch (e: Exception) {
            a.settle(e)
        }
        // The daemon did get it; its answer could not be read. Retry the same.
        assertTrue(shown is CommandException && shown.unknown)
        assertTrue(a.frozen)
        assertSame(first, a.next { r.prepare(daemon.id, "s1", cmd) })
    }

    @Test
    fun aServerOutOfReachIsNamedOnlyAfterAMoment() = runBlocking<Unit> {
        val r = client(linkGraceMs = 300)
        r.start()
        val on = r.awaitState("session") { it.link == Link.ONLINE && it.sessions["s1"]?.info != null }
        assertFalse(on.unlinked)
        val known = shownStatus(on, on.sessions["s1"]!!)
        server.stop()
        // Just lost: it is being remade, and what was known still shows.
        val lost = r.awaitState("lost") { it.conn != ConnState.ONLINE }
        assertEquals(Link.WAITING, lost.link)
        assertEquals(known, shownStatus(lost, lost.sessions["s1"]!!))
        val out = r.awaitState("out of reach") { it.link == Link.NO_SERVER }
        assertEquals(Status.UNLINKED, shownStatus(out, out.sessions["s1"]!!))
        server.start()
        r.reconnectNow()
        r.awaitState("back") { it.link == Link.ONLINE && !it.unlinked }
    }

    @Test
    fun aDeviceWithoutANetworkSaysSoAtOnce() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.awaitState("online") { it.link == Link.ONLINE }
        // The server is still there: the system's word alone ends the connection.
        server.stop()
        r.setNetwork(false)
        val off = r.awaitState("no network") { it.link == Link.NO_NETWORK }
        assertTrue(off.unlinked)
        server.start()
        r.setNetwork(true)
        r.awaitState("back") { it.link == Link.ONLINE }
    }

    @Test
    fun aHistoryThatFailsToLoadSaysSoOnTheSession() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.awaitState("session") { it.conn == ConnState.ONLINE && it.sessions["s1"]?.info != null }
        server.stop()
        // Which comes first is a race the client does not decide: the read failing, which is
        // said on the session, or the stream dropping, after which a failed read is not said
        // apart (architecture 「断线时显示什么」) and the next connection reads again.
        val events = r.events("s1")
        withTimeout(20_000) {
            kotlinx.coroutines.flow.combine(events, r.state) { ev, st -> ev.fetchError != null || st.conn != ConnState.ONLINE }.first { it }
        }
        events.first().fetchError?.let { assertTrue(it.startsWith("没能读取历史")) }
        // Back again, a retry loads it and clears the error.
        server.start()
        r.retryEvents("s1")
        val ok = withTimeout(20_000) { r.events("s1").first { it.events.size == 3 && it.fetchError == null } }
        assertEquals(3, ok.events.size)
    }

    @Test
    fun stoppingDuringLoginLeavesTheStreamClosed() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.stop()
        kotlinx.coroutines.delay(3000)
        assertTrue(r.state.value.conn != ConnState.ONLINE)
    }

    @Test
    fun aReaderThatFallsBehindCatchesUpFromHistory() = runBlocking<Unit> {
        val r = client(frameQueue = 1)
        r.start()
        r.awaitEvents("s1", 3)
        repeat(40) { daemon.upload("s1", agent("快速的第 $it 条")) }
        val ev = withTimeout(40_000) { r.events("s1").first { it.events.size >= 43 } }
        assertEquals((1L..43L).toList(), ev.events.map { it.seq })
    }

    @Test
    fun fetchesAnImageAndCachesOnlyItsEnvelope() = runBlocking<Unit> {
        val cache = MemoryCache()
        val r = client(cache)
        r.start()
        r.awaitState("session") { it.conn == ConnState.ONLINE && it.sessions["s1"]?.info != null }
        val png = byteArrayOf(0x89.toByte(), 'P'.code.toByte(), 'N'.code.toByte(), 'G'.code.toByte(), 13, 10, 26, 10, 1, 2, 3)
        daemon.putImage("s1", "00000000000000000000000000000001", io.github.lewin671.remoteagent.proto.ImagePayload.newBuilder().setFormat(io.github.lewin671.remoteagent.proto.ImageFormat.IMAGE_FORMAT_PNG)
            .setWidth(1).setHeight(1).setData(com.google.protobuf.ByteString.copyFrom(png)).build())
        val got = r.image("s1", "00000000000000000000000000000001")
        assertTrue(got is Remote.ImageResult.Loaded)
        assertTrue((got as Remote.ImageResult.Loaded).payload.data.toByteArray().contentEquals(png))
        // What the cache holds is the sealed envelope, not the picture.
        val stored = cache.images.getValue("s1/00000000000000000000000000000001")
        assertFalse(String(stored, Charsets.ISO_8859_1).contains(String(png, Charsets.ISO_8859_1)))
        assertEquals(Remote.ImageResult.Missing, r.image("s1", "ffffffffffffffffffffffffffffffff"))
        // A format that does not match its bytes is refused.
        daemon.putImage("s1", "00000000000000000000000000000002", io.github.lewin671.remoteagent.proto.ImagePayload.newBuilder().setFormat(io.github.lewin671.remoteagent.proto.ImageFormat.IMAGE_FORMAT_JPEG)
            .setData(com.google.protobuf.ByteString.copyFrom(png)).build())
        assertTrue(r.image("s1", "00000000000000000000000000000002") is Remote.ImageResult.Corrupt)
    }

    @Test
    fun twoClientsChangingProjectsKeepBothChanges() = runBlocking<Unit> {
        val a = client()
        val b = client()
        a.start()
        b.start()
        a.awaitState("a prefs") { it.conn == ConnState.ONLINE && it.prefs != null }
        b.awaitState("b prefs") { it.conn == ConnState.ONLINE && it.prefs != null }
        a.changePrefs(PrefsOp.Pin("/Users/me/Code/foo"))
        // b has not seen a's write yet when it writes: the server refuses, b redoes its change on a's.
        b.changePrefs(PrefsOp.Hide("/Users/me/Code/billing"))
        val sa = a.awaitState("a sees both") { it.prefs?.hidden?.contains("/Users/me/Code/billing") == true }
        assertEquals(listOf("/Users/me/Code/foo"), sa.prefs!!.pinned)
        assertEquals(2L, sa.prefs!!.revision)
    }

    @Test
    fun aClientsNameReachesTheOtherClients() = runBlocking<Unit> {
        val a = client()
        val b = client()
        a.start()
        b.start()
        a.awaitState("a devices") { it.conn == ConnState.ONLINE && it.devices.containsKey(a.account.deviceId) }
        b.awaitState("b sees a") { it.devices.containsKey(a.account.deviceId) }
        a.describeSelf("android", "Android 16 · Pixel 9")
        a.renameDevice(a.account.deviceId, "我的手机")
        val sb = b.awaitState("b sees the name") { it.devices[a.account.deviceId]?.meta?.name == "我的手机" }
        assertEquals("我的手机", clientName(sb.devices[a.account.deviceId], a.account.deviceId))
    }

    @Test
    fun aDeviceListInFlightDoesNotUndoWhatWasPushedMeanwhile() = runBlocking<Unit> {
        val hold = java.util.concurrent.atomic.AtomicBoolean(false)
        val held = kotlinx.coroutines.CompletableDeferred<Unit>()
        val gate = java.util.concurrent.CountDownLatch(1)
        // The server has answered; the answer waits here before it is handed on.
        val slow = http.newBuilder().addInterceptor { chain ->
            val res = chain.proceed(chain.request())
            if (hold.get() && chain.request().url.encodedPath.endsWith("/ListDevices")) {
                held.complete(Unit)
                gate.await(10, TimeUnit.SECONDS)
            }
            res
        }.build()
        val a = Remote(joinWithRoot(root, server.url, http), slow)
        closers += AutoCloseable { gate.countDown(); a.close() }
        val other = client()
        val leaving = joinWithRoot(root, server.url, http)
        a.start()
        other.start()
        a.awaitState("a devices") { it.conn == ConnState.ONLINE && it.devices.containsKey(leaving.deviceId) }
        other.awaitState("other online") { it.conn == ConnState.ONLINE }
        hold.set(true)
        val reading = launch { a.refreshDevices() }
        withTimeout(10_000) { held.await() }
        other.removeDevice(leaving.deviceId)
        val joined = joinWithRoot(root, server.url, http)
        a.awaitState("both pushes") { !it.devices.containsKey(leaving.deviceId) && it.devices.containsKey(joined.deviceId) }
        gate.countDown()
        reading.join()
        val devices = a.state.value.devices
        assertFalse(devices.containsKey(leaving.deviceId))
        assertTrue(devices.containsKey(joined.deviceId))
        assertTrue(devices.containsKey(a.account.deviceId))
    }
}

/** A CipherCache in memory, as the Room one behaves. */
class MemoryCache : CipherCache {
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
    override suspend fun events(sessionId: String) = events[sessionId]?.values?.toList().orEmpty()
    override suspend fun putMachine(m: Machine) {
        machines[m.id] = m
    }

    override suspend fun putSession(s: RemoteSession) {
        sessions[s.sessionId] = s
    }

    override suspend fun putEvents(sessionId: String, events: List<StoredEvent>) {
        val m = this.events.getOrPut(sessionId) { java.util.TreeMap() }
        synchronized(m) { for (e in events) m[e.seq] = e }
    }

    override suspend fun removeMachine(id: String) {
        machines.remove(id)
        sessions.values.filter { it.machineId == id }.forEach { sessions.remove(it.sessionId); events.remove(it.sessionId) }
    }

    override suspend fun removeSession(id: String) {
        sessions.remove(id)
        events.remove(id)
    }

    override suspend fun unlistSession(id: String) {
        sessions.remove(id)
    }

}
