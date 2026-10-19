package io.github.lewin671.remoteagent.core

import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.proto.AgentMessage
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.ProjectSummary
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionStatus
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Before
import org.junit.Test

/**
 * The session listing a page at a time (architecture §8 「会话列表分页」), against the real Go server: 120
 * ended sessions and two that have not ended, so the first page and two more.
 */
class SessionPagingTest {
    private lateinit var server: TestServer
    private val http = Api.defaultHttp()
    private val root = randomBytes(32)
    private lateinit var daemon: FakeDaemon
    private val closers = mutableListOf<AutoCloseable>()

    private val base = 1_700_000_000_000L

    private fun endedId(i: Int) = "e%03d".format(i)

    private fun endedAt(i: Int) = base + i

    private fun live(cwd: String, title: String) =
        Session.newBuilder()
            .setCwd(cwd)
            .setTitle(title)
            .setStatus(SessionStatus.SESSION_STATUS_IDLE)
            .build()

    private fun ended(cwd: String, title: String, at: Long) =
        Session.newBuilder()
            .setCwd(cwd)
            .setTitle(title)
            .setStatus(SessionStatus.SESSION_STATUS_EXITED)
            .setEndedAt(
                Timestamp.newBuilder()
                    .setSeconds(at / 1000)
                    .setNanos((at % 1000).toInt() * 1_000_000)
            )
            .build()

    @Before
    fun setUp() =
        runBlocking<Unit> {
            server = TestServer()
            daemon = FakeDaemon.create(root, server.url, http, server.setupToken)
            daemon.putMachine("fake-mac.local")
            daemon.putSession("live1", live("/Users/me/Code/foo", "进行中一"))
            daemon.putSession("live2", live("/Users/me/Code/bar", "进行中二"))
            // e120 ended last: the first page is e120 down to e071.
            for (i in 1..120) daemon.putSession(
                endedId(i),
                ended("/Users/me/Code/foo", "结束 $i", endedAt(i)),
                endedMs = endedAt(i),
            )
            daemon.connect()
        }

    @After
    fun tearDown() {
        closers.forEach { it.close() }
        if (this::daemon.isInitialized) daemon.close()
        server.close()
    }

    private suspend fun client(
        cache: CipherCache? = null,
        http: okhttp3.OkHttpClient = this.http,
    ): Remote {
        val r = fixtureRemote(approvedClient(daemon.phone, this.http), http, cache)
        closers += AutoCloseable { r.close() }
        return r
    }

    private suspend fun Remote.awaitState(what: String, ok: (RemoteState) -> Boolean): RemoteState =
        try {
            withTimeout(15_000) { state.first(ok) }
        } catch (e: Exception) {
            val st = state.value
            fail(
                "timed out waiting for $what: ${st.sessions.size} sessions, next ${st.sessionsNext}, ${st.conn}, error ${st.error}"
            )
            throw e
        }

    private fun RemoteState.endedIds() =
        sessions.values.filter { exited(it) }.sortedWith(endedOrder).map { it.id }

    private fun RemoteState.liveIds() =
        sessions.values.filter { !exited(it) }.map { it.id }.sorted()

    private val firstEnd = SessionPlace(endedAt(71), endedId(71))

    /**
     * Connected, with the first page: every session that has not ended and the fifty ended last.
     */
    private suspend fun Remote.started(): RemoteState {
        start()
        return awaitState("the first page") {
            it.conn == ConnState.ONLINE &&
                it.loaded &&
                it.sessionsNext == firstEnd &&
                it.sessions.size == 52
        }
    }

    @Test
    fun theFirstPageIsEverySessionNotEndedAndTheLatestEndedThenPagesMerge() =
        runBlocking<Unit> {
            val r = client()
            val st = r.started()
            assertEquals(listOf("live1", "live2"), st.liveIds())
            assertEquals((120 downTo 71).map(::endedId), st.endedIds())
            assertTrue(st.moreSessions)
            assertFalse(st.loadingSessions)
            // What is shown and sorted by is the sealed metadata; the server's time only places it.
            val e120 = st.sessions.getValue(endedId(120))
            assertEquals(endedAt(120), e120.listedEndedMs)
            assertEquals(endedAt(120), endedMs(e120))
            assertEquals(0L, st.sessions.getValue("live1").listedEndedMs)

            assertTrue(r.loadMoreSessions())
            var next = r.state.value
            assertEquals((120 downTo 21).map(::endedId), next.endedIds())
            assertEquals(SessionPlace(endedAt(21), endedId(21)), next.sessionsNext)
            assertEquals(listOf("live1", "live2"), next.liveIds())

            // The last page: nothing is left, and asking again fetches nothing.
            assertFalse(r.loadMoreSessions())
            next = r.state.value
            assertEquals((120 downTo 1).map(::endedId), next.endedIds())
            assertNull(next.sessionsNext)
            assertFalse(next.moreSessions)
            assertFalse(next.loadingSessions)
            assertFalse(r.loadMoreSessions())
            assertEquals(122, r.state.value.sessions.size)
        }

    @Test
    fun callsForAPageWhileOneIsInFlightAreOneFetch() =
        runBlocking<Unit> {
            val r = client()
            r.started()
            // Three calls at once fetch one page, not three: fifty more, with one page left.
            val answers =
                listOf(
                        async { r.loadMoreSessions() },
                        async { r.loadMoreSessions() },
                        async { r.loadMoreSessions() },
                    )
                    .awaitAll()
            assertEquals(listOf(true, true, true), answers)
            val st = r.state.value
            assertEquals((120 downTo 21).map(::endedId), st.endedIds())
            assertEquals(SessionPlace(endedAt(21), endedId(21)), st.sessionsNext)
            assertFalse(st.loadingSessions)
        }

    @Test
    fun everyPageLeftIsFetchedForAFilter() =
        runBlocking<Unit> {
            val r = client()
            r.started()
            // Asked twice at once, with a single page asked for as well: one run, every page once.
            listOf(
                    async { r.loadAllSessions() },
                    async { r.loadAllSessions() },
                    async { r.loadMoreSessions() },
                )
                .awaitAll()
            val st = r.state.value
            assertEquals((120 downTo 1).map(::endedId), st.endedIds())
            assertFalse(st.moreSessions)
            assertFalse(st.loadingSessions)
            assertEquals("", st.sessionsError)
            // With nothing left it returns at once.
            r.loadAllSessions()
            assertEquals(122, r.state.value.sessions.size)
        }

    @Test
    fun aSessionNotHeldIsFetchedByItsIdAndOnlyTheServerSaysItIsMissing() =
        runBlocking<Unit> {
            val r = client()
            val st = r.started()
            assertNull(st.sessions[endedId(3)])
            assertTrue(r.fetchSession(endedId(3)))
            val got = r.state.value.sessions.getValue(endedId(3))
            assertEquals("结束 3", titleOf(got))
            assertEquals(endedAt(3), got.listedEndedMs)
            // The cursor does not move: the pages between are still to come.
            assertEquals(firstEnd, r.state.value.sessionsNext)
            assertEquals(53, r.state.value.sessions.size)
            // One held already is not asked for; one the server does not have is missing.
            assertTrue(r.fetchSession("live1"))
            assertFalse(r.fetchSession("no-such-session"))
            assertNull(r.state.value.sessions["no-such-session"])
            // Asked for twice at once, it is one session.
            assertEquals(
                listOf(true, true),
                listOf(async { r.fetchSession(endedId(5)) }, async { r.fetchSession(endedId(5)) })
                    .awaitAll(),
            )
            // Its events load like any other session's.
            daemon.upload(
                endedId(3),
                Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText("旧会话的内容")),
            )
            val ev = withTimeout(10_000) { r.events(endedId(3)).first { it.events.size == 1 } }
            assertEquals("旧会话的内容", ev.events.single().agentMessage.text)
        }

    @Test
    fun afterAReconnectEndedSessionsPastTheFirstPageStay() =
        runBlocking<Unit> {
            val r = client()
            r.started()
            assertTrue(r.loadMoreSessions())
            assertTrue(r.fetchSession(endedId(2)))
            r.stop()
            r.awaitState("offline") { it.conn == ConnState.OFFLINE }
            // A session ends while this client is away: it leads the next first page, which now
            // ends at e072.
            daemon.putSession(
                "live2",
                ended("/Users/me/Code/bar", "进行中二", endedAt(500)),
                endedMs = endedAt(500),
            )
            r.start()
            val end = SessionPlace(endedAt(72), endedId(72))
            val st =
                r.awaitState("the first page again") {
                    it.conn == ConnState.ONLINE && it.sessionsNext == end
                }
            // The pages start over from the first, and what was fetched of the later ones is kept:
            // e071 to e021 from the second page, and e002 fetched on its own.
            assertEquals(
                listOf("live2") + (120 downTo 21).map(::endedId) + endedId(2),
                st.endedIds(),
            )
            assertEquals(listOf("live1"), st.liveIds())
            // Going on from there ends with every session, each once.
            r.loadAllSessions()
            assertEquals(listOf("live2") + (120 downTo 1).map(::endedId), r.state.value.endedIds())
            assertEquals(122, r.state.value.sessions.size)
        }

    @Test
    fun afterExplicitArchiveDeletionAComputerAndItsSessionsGo() =
        runBlocking<Unit> {
            val other =
                FakeDaemon(
                        approvedClient(
                            daemon.phone,
                            http,
                            io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_MACHINE,
                        ),
                        http,
                        daemon.phone,
                    )
                    .also { it.login() }
            closers += other
            other.putMachine("other-mac.local")
            other.putSession("there", live("/Users/me/Code/baz", "另一台上的"))
            // Ended before everything else: far past the first page, where an ended session would
            // stay.
            other.putSession(
                "old-there",
                ended("/Users/me/Code/baz", "另一台上的旧会话", base - 10),
                endedMs = base - 10,
            )
            other.upload(
                "old-there",
                Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText("旧内容")),
            )
            val cache = MemoryCache()
            val r = client(cache)
            r.start()
            r.awaitState("both machines") {
                it.conn == ConnState.ONLINE &&
                    it.sessionsNext == firstEnd &&
                    it.sessions["there"] != null
            }
            assertTrue(r.fetchSession("old-there"))
            withTimeout(10_000) { r.events("old-there").first { it.events.size == 1 } }
            assertEquals(1, cache.events("old-there").size)
            r.stop()
            r.awaitState("offline") { it.conn == ConnState.OFFLINE }
            // Another client removes that machine meanwhile.
            val admin = client()
            admin.start()
            admin.awaitState("admin online") {
                it.conn == ConnState.ONLINE && it.machines[other.id] != null
            }
            admin.refreshDevices()
            removeAsAuthority(daemon.phone, other.id, http)
            val api=Api(server.url,http)
            api.client.deleteMachineRecords(io.github.lewin671.remoteagent.proto.DeleteMachineRecordsRequest.newBuilder().setMachineId(other.id).build(),Api.headers(login(api,daemon.phone))).value()
            r.start()
            val st =
                r.awaitState("the machine gone") {
                    it.conn == ConnState.ONLINE &&
                        it.machines[other.id] == null &&
                        it.machines[daemon.id]?.online == true
                }
            assertNull(st.sessions["there"])
            assertNull(st.sessions["old-there"])
            assertEquals(52, st.sessions.size)
            // Known not to exist any more: its cached events go too.
            withTimeout(5_000) {
                while (cache.events("old-there").isNotEmpty()) kotlinx.coroutines.delay(20)
            }
            assertFalse(cache.sessions().any { it.machineId == other.id })
        }

    @Test
    fun afterAReconnectASessionThePageLacksAndThatIsNotALaterPagesGoes() =
        runBlocking<Unit> {
            val cache = MemoryCache()
            val r = client(cache)
            r.started()
            daemon.upload(
                "live1",
                Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText("进行中的内容")),
            )
            withTimeout(10_000) { r.events("live1").first { it.events.size == 1 } }
            r.stop()
            r.awaitState("offline") { it.conn == ConnState.OFFLINE }
            // It ended while this client was away, at a time before every other: it is on the last
            // page now. Here it is still held as not ended, so nothing says it is a later page's.
            daemon.putSession(
                "live1",
                ended("/Users/me/Code/foo", "进行中一", base - 5),
                endedMs = base - 5,
            )
            r.start()
            val st =
                r.awaitState("the stale session gone") {
                    it.conn == ConnState.ONLINE && it.sessions["live1"] == null
                }
            assertEquals(listOf("live2"), st.liveIds())
            assertEquals(firstEnd, st.sessionsNext)
            // It was not known to be gone, so its events stay cached; it is back, as ended, with
            // its page.
            assertEquals(1, cache.events("live1").size)
            r.loadAllSessions()
            val back = r.state.value.sessions.getValue("live1")
            assertTrue(exited(back))
            assertEquals(base - 5, back.listedEndedMs)
            assertEquals("live1", r.state.value.endedIds().last())
        }

    @Test
    fun aPageThatFailsLeavesTheListingReadyToGoOn() =
        runBlocking<Unit> {
            val r = client()
            r.started()
            server.stop()
            try {
                r.loadMoreSessions()
                fail("expected the page to fail")
            } catch (e: Exception) {
                if (e is AssertionError) throw e
            }
            var st = r.state.value
            assertFalse(st.loadingSessions)
            assertTrue(st.sessionsError.isNotEmpty())
            // Nothing moved: the same page is still the next one.
            assertEquals(firstEnd, st.sessionsNext)
            assertEquals(52, st.sessions.size)
            // Every page left fails the same way, and does not hang.
            try {
                withTimeout(20_000) { r.loadAllSessions() }
                fail("expected the pages to fail")
            } catch (e: Exception) {
                if (e is AssertionError) throw e
            }
            assertFalse(r.state.value.loadingSessions)
            server.start()
            r.reconnectNow()
            r.awaitState("online again") {
                it.conn == ConnState.ONLINE && it.sessionsError.isEmpty()
            }
            assertTrue(r.loadMoreSessions())
            st = r.state.value
            assertEquals("", st.sessionsError)
            assertEquals((120 downTo 21).map(::endedId), st.endedIds())
        }

    @Test
    fun theCacheKeepsTheFirstPageAndEventsOfAnySessionOpened() =
        runBlocking<Unit> {
            val cache = MemoryCache()
            val r = client(cache)
            r.started()
            r.loadAllSessions()
            assertTrue(r.fetchSession(endedId(1)))
            daemon.upload(
                endedId(1),
                Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText("最早的会话")),
            )
            withTimeout(10_000) { r.events(endedId(1)).first { it.events.size == 1 } }
            // Only the first page is listed in the cache, whatever else was fetched.
            assertEquals(
                (listOf("live1", "live2") + (71..120).map(::endedId)).sorted(),
                cache.sessions().map { it.sessionId }.sorted(),
            )
            assertEquals(1, cache.events(endedId(1)).size)
            // A session that ends now is on the first page; one whose metadata comes again from a
            // later page is not.
            daemon.putSession(
                "live2",
                ended("/Users/me/Code/bar", "进行中二", endedAt(500)),
                endedMs = endedAt(500),
            )
            r.awaitState("live2 ended") { it.sessions["live2"]?.let(::exited) == true }
            daemon.putSession(
                endedId(1),
                ended("/Users/me/Code/foo", "结束 1 改名", endedAt(1)),
                endedMs = endedAt(1),
            )
            r.awaitState("e001 renamed") { it.sessions[endedId(1)]?.info?.title == "结束 1 改名" }
            assertFalse(cache.sessions().any { it.sessionId == endedId(1) })
            assertTrue(cache.sessions().any { it.sessionId == "live2" })
            r.stop()
            r.awaitState("offline") { it.conn == ConnState.OFFLINE }

            // The next run shows the cached first page before it connects, and the events are still
            // there.
            val again = fixtureRemote(r.account, http, cache)
            closers += AutoCloseable { again.close() }
            server.stop()
            again.start()
            val st = again.awaitState("the cached page") { it.loaded && it.sessions.size == 52 }
            // What the cache kept is one page of an earlier run: whether there are more is not
            // known until the first page comes.
            assertFalse(st.sessionsListed)
            assertTrue(st.moreSessions)
            assertNull(st.sessionsNext)
            assertNull(st.sessions[endedId(1)])
            assertEquals(1, cache.events(endedId(1)).size)
            // After reconnecting the cache is the new first page: e071 left it for live2.
            server.start()
            again.reconnectNow()
            again.awaitState("online") {
                it.conn == ConnState.ONLINE &&
                    it.sessionsNext == SessionPlace(endedAt(72), endedId(72))
            }
            withTimeout(5_000) {
                while (cache.sessions().any { it.sessionId == endedId(71) }) kotlinx.coroutines
                    .delay(20)
            }
            assertEquals(
                (listOf("live1", "live2") + (72..120).map(::endedId)).sorted(),
                cache.sessions().map { it.sessionId }.sorted(),
            )
            assertEquals(1, cache.events(endedId(1)).size)
        }

    /**
     * A cache whose machine writes wait while [hold] is set: the first page stays half put in
     * place.
     */
    private class HeldCache(private val inner: MemoryCache = MemoryCache()) : CipherCache by inner {
        @Volatile var hold: kotlinx.coroutines.CompletableDeferred<Unit>? = null
        val reached =
            kotlinx.coroutines.channels.Channel<Unit>(kotlinx.coroutines.channels.Channel.CONFLATED)

        override suspend fun putMachine(m: io.github.lewin671.remoteagent.proto.Machine) {
            hold?.let {
                reached.trySend(Unit)
                it.await()
            }
            inner.putMachine(m)
        }
    }

    @Test
    fun aPageAskedForWhileTheListingStartsOverIsFetchedFromTheNewCursor() =
        runBlocking<Unit> {
            val cache = HeldCache()
            val r = client(cache)
            r.started()
            r.stop()
            r.awaitState("offline") { it.conn == ConnState.OFFLINE }
            // Nothing changed meanwhile: the first page again ends where it did, so the cursor does
            // not move.
            val hold = kotlinx.coroutines.CompletableDeferred<Unit>()
            cache.hold = hold
            r.start()
            withTimeout(15_000) { cache.reached.receive() }
            assertEquals(ConnState.ONLINE, r.state.value.conn)
            val page = async { r.loadMoreSessions() }
            // The request does not end with nothing fetched: it waits for the first page.
            kotlinx.coroutines.delay(500)
            assertFalse(page.isCompleted)
            assertEquals(52, r.state.value.sessions.size)
            cache.hold = null
            hold.complete(Unit)
            assertTrue(withTimeout(15_000) { page.await() })
            val st = r.state.value
            assertEquals((120 downTo 21).map(::endedId), st.endedIds())
            assertEquals(SessionPlace(endedAt(21), endedId(21)), st.sessionsNext)
            assertFalse(st.loadingSessions)
        }

    /**
     * An HTTP client whose listing of machines fails while [down] is set, with the stream and
     * everything else working.
     */
    private class Flaky {
        @Volatile var down = false
        val http: okhttp3.OkHttpClient =
            Api.defaultHttp()
                .newBuilder()
                .addInterceptor { chain ->
                    if (down && chain.request().url.encodedPath.endsWith("ListMachines"))
                        throw java.io.IOException("listing down")
                    chain.proceed(chain.request())
                }
                .build()
    }

    @Test
    fun aFirstPageThatFailsSaysSoAndIsTriedAgainByItself() =
        runBlocking<Unit> {
            val flaky = Flaky().apply { down = true }
            val r = client(http = flaky.http)
            r.start()
            // Connected, with no listing: it is not taken for an empty one, nor for one with
            // nothing more to fetch.
            val failed =
                r.awaitState("the failed listing") {
                    it.conn == ConnState.ONLINE && it.sessionsError.isNotEmpty()
                }
            assertFalse(failed.sessionsListed)
            assertFalse(failed.loaded)
            assertTrue(failed.moreSessions)
            assertNull(failed.sessionsNext)
            flaky.down = false
            // No reconnect and no call from the screen: it comes by itself.
            val st = r.awaitState("the first page") { it.sessionsListed && it.sessions.size == 52 }
            assertEquals("", st.sessionsError)
            assertEquals(firstEnd, st.sessionsNext)
            assertEquals(ConnState.ONLINE, st.conn)
        }

    @Test
    fun askingForAPageBeforeTheFirstOneCameFetchesTheFirst() =
        runBlocking<Unit> {
            val flaky = Flaky().apply { down = true }
            val r = client(http = flaky.http)
            r.start()
            r.awaitState("the failed listing") {
                it.conn == ConnState.ONLINE && it.sessionsError.isNotEmpty()
            }
            // Still failing: the retry says so and leaves nothing loading.
            try {
                r.loadMoreSessions()
                fail("expected the first page to fail")
            } catch (e: Exception) {
                if (e is AssertionError) throw e
            }
            assertFalse(r.state.value.loadingSessions)
            assertFalse(r.state.value.sessionsListed)
            flaky.down = false
            // The tap that tries again: the first page, and only it.
            assertTrue(r.loadMoreSessions())
            val st = r.state.value
            assertTrue(st.sessionsListed)
            assertEquals(firstEnd, st.sessionsNext)
            assertEquals(52, st.sessions.size)
            // Every page left, asked for before the first one came, starts with the first.
            val again =
                client(
                    http =
                        Flaky()
                            .apply { down = true }
                            .also { f -> closers += AutoCloseable { f.down = false } }
                            .http
                )
            again.start()
            again.awaitState("the failed listing") {
                it.conn == ConnState.ONLINE && it.sessionsError.isNotEmpty()
            }
            try {
                again.loadAllSessions()
                fail("expected the pages to fail")
            } catch (e: Exception) {
                if (e is AssertionError) throw e
            }
        }

    @Test
    fun ofTwoFirstPagesInFlightOnlyTheLaterAskedForIsPutInPlace() =
        runBlocking<Unit> {
            val r = client()
            r.started()
            assertTrue(r.loadMoreSessions())
            // As when a stream was replaced while the lists of the one before were still on their
            // way.
            val answers = listOf(async { r.refresh() }, async { r.refresh() }).awaitAll()
            assertEquals(listOf(false, true), answers)
            val st = r.state.value
            assertEquals(firstEnd, st.sessionsNext)
            assertEquals((120 downTo 21).map(::endedId), st.endedIds())
            assertEquals("", st.sessionsError)
            // The listing goes on from there.
            r.loadAllSessions()
            assertEquals(122, r.state.value.sessions.size)
        }

    @Test
    fun noPageIsAskedForWithoutTheConnection() =
        runBlocking<Unit> {
            val r = client()
            r.started()
            r.stop()
            r.awaitState("offline") { it.conn == ConnState.OFFLINE }
            // In the background nothing is fetched, though the server is there; a run of every page
            // stops too.
            for (ask in
                listOf<suspend () -> Any>({ r.loadMoreSessions() }, { r.loadAllSessions() })) {
                try {
                    ask()
                    fail("expected no page without the connection")
                } catch (e: CommandException) {
                    assertEquals(CommandException.UNAVAILABLE, e.code)
                }
            }
            assertEquals(52, r.state.value.sessions.size)
            assertEquals(firstEnd, r.state.value.sessionsNext)
            // Back online the reason is gone and the listing goes on.
            r.start()
            r.awaitState("online again") {
                it.conn == ConnState.ONLINE && it.sessionsError.isEmpty()
            }
            assertTrue(r.loadMoreSessions())
        }

    // --- projects from the machines' summaries and the sessions held ---

    private fun summary(path: String, ended: Int, created: Long = 0, endedAt: Long = 0) =
        ProjectSummary.newBuilder()
            .setPath(path)
            .setEnded(ended)
            .setLatestCreatedMs(created)
            .setLatestEndedMs(endedAt)
            .build()

    private fun machine(id: String, name: String, vararg projects: ProjectSummary) =
        MachineView(
            id,
            online = true,
            lastSeenMs = 0,
            meta =
                MachineMeta.newBuilder().setHostname(name).addAllProjects(projects.toList()).build(),
        )

    private fun view(
        id: String,
        machine: String,
        cwd: String,
        created: Long,
        exited: Boolean = false,
    ) =
        SessionView(
            id,
            machine,
            Session.newBuilder()
                .setCwd(cwd)
                .setCreatedAt(Timestamp.newBuilder().setSeconds(created / 1000))
                .setStatus(
                    if (exited) SessionStatus.SESSION_STATUS_EXITED
                    else SessionStatus.SESSION_STATUS_IDLE
                )
                .build(),
            updatedMs = created,
        )

    @Test
    fun aProjectAddedButNotStartedInYetIsListedFromItsSettings() {
        fun settings(path: String, machine: String, at: Long) =
            io.github.lewin671.remoteagent.proto.ProjectSettings.newBuilder()
                .setPath(path)
                .setMachineId(machine)
                .setUpdatedMs(at)
                .build()
        val st =
            RemoteState(
                machines =
                    mapOf(
                        "m1" to
                            machine(
                                "m1",
                                "mac",
                                summary("/code/old", 9, created = 1_000_000, endedAt = 2_000_000),
                            )
                    ),
                sessions =
                    listOf(view("a", "m1", "/code/foo", 4_000_000, exited = true)).associateBy {
                        it.id
                    },
                prefs =
                    PrefsObject(
                        1,
                        io.github.lewin671.remoteagent.proto.ProjectPrefs.newBuilder()
                            .setSchema(1)
                            .addAllSettings(
                                listOf(
                                    settings("/code/added", "m1", 5_000_000),
                                    // Those with sessions are as their sessions and summaries say.
                                    settings("/code/foo", "m1", 7_000_000),
                                    settings("/code/old", "m1", 7_000_000),
                                    // The machine chosen for it is no longer the account's.
                                    settings("/code/gone", "m9", 8_000_000),
                                )
                            )
                            .build(),
                    ),
            )
        assertEquals(listOf("/code/added", "/code/foo", "/code/old"), projects(st).map { it.key })
        val added = projects(st).first()
        assertEquals(listOf("m1"), added.machineIds)
        assertEquals(5_000_000L, added.latestCreatedMs)
        assertEquals(0, added.ended)
        assertFalse(added.hasLive)
        assertTrue(added.sessions.isEmpty())
        assertEquals(4_000_000L, projects(st)[1].latestCreatedMs)
        assertEquals(
            listOf("/code/added", "/code/foo", "/code/old"),
            listedProjects(st).map { it.key },
        )
    }

    @Test
    fun projectsAreTheSummariesTogetherWithTheSessionsHeld() {
        val st =
            RemoteState(
                machines =
                    mapOf(
                        "m1" to
                            machine(
                                "m1",
                                "mac",
                                summary("/code/foo", 40, created = 5_000_000, endedAt = 6_000_000),
                                summary("/code/old", 9, created = 1_000_000, endedAt = 2_000_000),
                            ),
                        "m2" to
                            machine(
                                "m2",
                                "linux",
                                summary("/code/foo", 3, created = 7_000_000),
                                summary("/code/bar", 0, created = 3_000_000),
                            ),
                        // A machine whose metadata was not read yet has no summary.
                        "m3" to MachineView("m3", online = false, lastSeenMs = 0),
                    ),
                sessions =
                    listOf(
                            view("a", "m1", "/code/foo", 4_000_000),
                            view("b", "m1", "/code/foo", 2_000_000, exited = true),
                            view("c", "m2", "/code/bar", 9_000_000, exited = true),
                            view("d", "m3", "/code/new", 8_000_000),
                        )
                        .associateBy { it.id },
            )
        val ps = projects(st).associateBy { it.key }
        // Live projects first, then by the latest creation, which is the later of the summaries'
        // and the sessions'.
        assertEquals(
            listOf("/code/new", "/code/foo", "/code/bar", "/code/old"),
            projects(st).map { it.key },
        )
        val foo = ps.getValue("/code/foo")
        // Whether a session has not ended is the sessions' to say; how many ended is the
        // summaries', added up.
        assertTrue(foo.hasLive)
        assertEquals(43, foo.ended)
        assertEquals(7_000_000L, foo.latestCreatedMs)
        assertEquals(listOf("a", "b"), foo.sessions.map { it.id })
        // Its machines are those of both: the sessions' first, then the summaries'.
        assertEquals(listOf("m1", "m2"), foo.machineIds)
        assertEquals(listOf("mac", "linux"), foo.machines)
        // Only old sessions, none of them held: the project is still there.
        val old = ps.getValue("/code/old")
        assertFalse(old.hasLive)
        assertEquals(9, old.ended)
        assertEquals(emptyList<SessionView>(), old.sessions)
        assertEquals(listOf("m1"), old.machineIds)
        assertEquals(1_000_000L, old.latestCreatedMs)
        assertEquals(2_000_000L, old.updatedMs)
        assertEquals(listOf("/code/old"), old.paths)
        // A session held that a summary does not count yet is counted, and its creation wins when
        // later.
        val bar = ps.getValue("/code/bar")
        assertEquals(1, bar.ended)
        assertEquals(9_000_000L, bar.latestCreatedMs)
        // A project no summary names comes from its sessions alone.
        val new = ps.getValue("/code/new")
        assertEquals(0, new.ended)
        assertEquals(listOf("m3"), new.machineIds)

        // The lists, the default scope and the pickers all read this list.
        assertEquals(
            listOf("/code/new", "/code/foo", "/code/bar", "/code/old"),
            listedProjects(st).map { it.key },
        )
        assertEquals(HomeScope.All, defaultScope(st))
        assertEquals(old, project(st, "/code/old"))
        assertNull(project(st, "/code/none"))
        assertEquals(listOf("/code/foo", "/code/old"), machineProjects(st, "m1"))
        assertEquals(listOf("/code/foo", "/code/bar"), machineProjects(st, "m2"))
        assertEquals(listOf("/code/new"), machineProjects(st, "m3"))
        // Pinned and removed projects are arranged as before, whichever source they come from.
        val arranged =
            projectGroups(
                st.copy(
                    prefs =
                        PrefsObject(
                            1,
                            io.github.lewin671.remoteagent.proto.ProjectPrefs.newBuilder()
                                .addPinned(
                                    io.github.lewin671.remoteagent.proto.ProjectPref.newBuilder()
                                        .setPath("/code/old")
                                )
                                .addHidden(
                                    io.github.lewin671.remoteagent.proto.ProjectPref.newBuilder()
                                        .setPath("/code/bar")
                                )
                                .build(),
                        )
                )
            )
        assertEquals(listOf("/code/old"), arranged.pinned.map { it.key })
        assertEquals(listOf("/code/new", "/code/foo"), arranged.others.map { it.key })
        assertEquals(listOf("/code/bar"), arranged.hidden.map { it.key })
        // The only project of an account whose sessions are all on later pages is the default
        // scope.
        val only =
            RemoteState(machines = mapOf("m1" to machine("m1", "mac", summary("/code/old", 9))))
        assertEquals(HomeScope.Of("/code/old"), defaultScope(only))
    }

    @Test
    fun aPlaceInTheListingIsByEndTimeThenId() {
        val place = SessionPlace(100, "m")
        assertTrue(afterPlace(99, "a", place))
        assertTrue(afterPlace(100, "n", place))
        assertFalse(afterPlace(100, "m", place))
        assertFalse(afterPlace(100, "a", place))
        assertFalse(afterPlace(101, "z", place))
        // A session that has not ended is on the first page, never after a place.
        assertFalse(afterPlace(0, "z", place))
    }
}
