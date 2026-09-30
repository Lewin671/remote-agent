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
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
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

    private suspend fun client(cache: CipherCache? = null, frameQueue: Int = 1024): Remote {
        val acct = joinWithRoot(root, server.url, http)
        val r = Remote(acct, http, cache, frameQueue = frameQueue)
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

}

/** A CipherCache in memory, as the Room one behaves. */
class MemoryCache : CipherCache {
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
}
