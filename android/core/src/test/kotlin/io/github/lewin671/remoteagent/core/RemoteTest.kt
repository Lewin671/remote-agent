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
        historyIdleMs: Long = 60_000,
        historyIdleCount: Int = 8,
        linkGraceMs: Long = 10_000,
        httpClient: okhttp3.OkHttpClient = http,
    ): Remote {
        val acct = approvedClient(daemon.phone, http)
        val r =
            fixtureRemote(
                acct,
                httpClient,
                cache,
                frameQueue = frameQueue,
                historyIdleMs = historyIdleMs,
                historyIdleCount = historyIdleCount,
                linkGraceMs = linkGraceMs,
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
        r.awaitState("retired session error") { it.sessions["retired"]?.historyError == RETIRED_HISTORY_ERROR }
        val retiredVm = r.watchSession("retired").first { it.timeline.retired }
        assertTrue(retiredVm.timeline.retired)
        assertTrue(retiredVm.timeline.hasHistoryNotice())
        r.awaitState("current session") { it.sessions["s1"]?.info != null }
        val s1Vm = r.watchSession("s1").first { it.timeline.retired }
        assertTrue(s1Vm.timeline.retired)
        assertTrue(s1Vm.timeline.hasHistoryNotice())
        assertTrue(s1Vm.timeline.itemsList.isEmpty())
        assertEquals(1, cache.events("s1").size)
        cache.putImage("s1/retired", old.toBuilder().setHeader(Header.newBuilder().setVersion(1)
            .setKind(PayloadKind.PAYLOAD_KIND_IMAGE).build().toByteString()).build().toByteArray())
        assertEquals(Remote.ImageResult.Corrupt(RETIRED_HISTORY_ERROR), r.image("s1", "retired"))
    }

    private suspend fun Remote.awaitState(what: String, ok: (RemoteState) -> Boolean): RemoteState =
        state.first(ok)

    private suspend fun Remote.awaitSessionTimeline(sid: String, minItems: Int): SessionViewModel =
        watchSession(sid).first { it.timeline.itemsCount >= minItems }

    @Test
    fun backgroundCancelsRenewalAndRetainsItsPendingOperation() =
        runBlocking<Unit> {
            val hold = java.util.concurrent.atomic.AtomicBoolean(false)
            val entered = java.util.concurrent.CountDownLatch(1)
            val release = java.util.concurrent.CountDownLatch(1)
            val exited = java.util.concurrent.CountDownLatch(1)
            val published = java.util.concurrent.atomic.AtomicInteger()
            val slow =
                http
                    .newBuilder()
                    .addInterceptor { chain ->
                        val held =
                            chain.request().url.encodedPath.endsWith("/GetSecurityStates") &&
                                hold.compareAndSet(true, false)
                        if (held) {
                            entered.countDown()
                            check(release.await(10, TimeUnit.SECONDS))
                        }
                        try {
                            if (chain.request().url.encodedPath.endsWith("/PutSecurityState"))
                                published.incrementAndGet()
                            chain.proceed(chain.request())
                        } finally {
                            if (held) exited.countDown()
                        }
                    }
                    .build()
            val phone = fixtureRemote(daemon.phone, slow)
            closers += AutoCloseable { phone.close() }
            phone.start()
            phone.awaitState("online") { it.conn == ConnState.ONLINE && it.synced }
            phone.renewLeaseIfNeeded().join()
            val storage = TestSecurity.get(server.url).stores.getValue(daemon.phone.deviceId)
            val record = phone.security.record()
            val req =
                PutSecurityStateRequest.newBuilder()
                    .setLease(com.google.protobuf.ByteString.copyFrom(record.lease!!.bytes()))
                    .build()
            val pending =
                SecurityOperation("lease", DeviceSecurity.stateHash(record.head), req.toByteArray())
            storage.writeSecurityOperation(pending)
            try {
                hold.set(true)
                val renewal = phone.renewLeaseNow()
                withContext(Dispatchers.IO) { entered.await() }
                phone.stop()
                phone.awaitState("background") { it.conn == ConnState.OFFLINE }
                release.countDown()
                renewal.join()
                withContext(Dispatchers.IO) { exited.await() }
                assertEquals("background renewal published", 0, published.get())
                assertArrayEquals(pending.signed, storage.readSecurityOperation()!!.signed)
                phone.start()
                phone.renewLeaseNow()
                while (storage.readSecurityOperation() != null || published.get() == 0) delay(20)
            } finally {
                release.countDown()
            }
        }

    @Test
    fun deliveredStateOperationOnlyNeedsLocalCleanup() =
        runBlocking<Unit> {
            val phone = fixtureRemote(daemon.phone, http)
            closers += AutoCloseable { phone.close() }
            val prepared = phone.prepareRecovery()
            phone.publishRecovery(prepared)
            val request =
                io.github.lewin671.remoteagent.proto.PutSecurityStateRequest.newBuilder()
                    .addStates(
                        io.github.lewin671.remoteagent.proto.SecurityState.newBuilder()
                            .setState(
                                com.google.protobuf.ByteString.copyFrom(
                                    DeviceSecurity.stateBytes(prepared.state),
                                ),
                            ),
                    )
                    .build()
            val storage = TestSecurity.get(server.url).stores.getValue(daemon.phone.deviceId)
            storage.writeSecurityOperation(
                SecurityOperation(
                    "state",
                    prepared.state.core.parent,
                    request.toByteArray(),
                    phase = SecurityOperation.Phase.DELIVERED,
                ),
            )
            val offline =
                http
                    .newBuilder()
                    .addInterceptor {
                        throw java.io.IOException("completed operation must not be replayed")
                    }
                    .build()
            val resumed = fixtureRemote(daemon.phone, offline)
            closers += AutoCloseable { resumed.close() }
            resumed.resumeSecurityOperation()
            assertNull(storage.readSecurityOperation())
        }

    @Test
    fun leaseCertificateIsDurableBeforeItIsPublished() =
        runBlocking<Unit> {
            val checked = CompletableDeferred<Boolean>()
            val flaky =
                http
                    .newBuilder()
                    .addInterceptor { chain ->
                        if (chain.request().url.encodedPath.endsWith("/PutSecurityState")) {
                            checked.complete(
                                TestSecurity.get(server.url)
                                    .stores
                                    .getValue(daemon.phone.deviceId)
                                    .readSecurityOperation()
                                    ?.kind == "lease",
                            )
                        }
                        chain.proceed(chain.request())
                    }
                    .build()
            val phone = fixtureRemote(daemon.phone, flaky)
            closers += AutoCloseable { phone.close() }
            phone.renewLeaseNow()
            assertTrue(
                "lease certificate was published before its durable operation",
                checked.await(),
            )
        }

    @Test
    fun storedPrefsSurviveRemovalOfTheirWriter() =
        runBlocking<Unit> {
            val writer = client()
            writer.start()
            writer.awaitState("writer online") { it.conn == ConnState.ONLINE }
            writer.coreHost.toggleProjectPin("/stored")
            writer.awaitState("writer pinned") { it.prefs?.pinned?.contains("/stored") == true }
            val admin = fixtureRemote(daemon.phone, http)
            closers += AutoCloseable { admin.close() }
            admin.removeDevice(writer.account.deviceId)
            val reader = client()
            reader.start()
            reader.awaitState("reader online") { it.conn == ConnState.ONLINE }
            reader.coreHost.toggleProjectHide("/next")
            val st = reader.awaitState("reader hidden") {
                it.prefs?.pinned?.contains("/stored") == true && it.prefs.hidden.contains("/next")
            }
            assertEquals(listOf("/stored"), st.prefs!!.pinned)
            assertEquals(setOf("/next"), st.prefs.hidden)
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
            old.awaitState("old phone ready") {
                it.conn == ConnState.ONLINE && it.devices[incoming.deviceId] != null
            }
            next.awaitState("new phone ready") {
                it.conn == ConnState.ONLINE && it.devices[incoming.deviceId] != null
            }
            val (code, pub) = next.prepareHandoffCode()
            assertArrayEquals(pub, SigningKey(RecoveryCode.decode(code)!!.seed).publicKey)
            // This call is triggered only by the UI's explicit offline-backup confirmation.
            next.offerHandoff(pub)
            old.refreshDevices()
            val epoch = old.security.head().core.contentEpoch
            val historical = old.security.contentKey(epoch)!!
            old.handoffAuthority(incoming.deviceId)
            next.syncSecurity()
            next.renewLeaseNow()
            next.awaitState("new authority") {
                it.isAuthority && it.leaseEndMs > System.currentTimeMillis()
            }
            val head = next.security.head()
            assertNull(head.core.member(daemon.phone.deviceId))
            assertArrayEquals(pub, head.core.recoveryEd)
            assertEquals(60, head.prevSecret.size)
            assertArrayEquals(historical, next.security.contentKey(epoch))
            assertEquals(epoch + 1uL, head.core.contentEpoch)
        }

    @Test
    fun lostSecurityPublicationReplyResumesWithoutACompetingSuccessor() =
        runBlocking<Unit> {
            val victim = approvedClient(daemon.phone, http)
            val drop = java.util.concurrent.atomic.AtomicBoolean(false)
            val flaky =
                http
                    .newBuilder()
                    .retryOnConnectionFailure(false)
                    .addInterceptor { chain ->
                        val response = chain.proceed(chain.request())
                        if (
                            chain.request().url.encodedPath.endsWith("/PutSecurityState") &&
                                response.isSuccessful &&
                                drop.compareAndSet(true, false)
                        ) {
                            response.close()
                            throw java.io.IOException("synthetic lost acknowledgement")
                        }
                        response
                    }
                    .build()
            val r = fixtureRemote(daemon.phone, flaky)
            closers += AutoCloseable { r.close() }
            r.start()
            r.awaitState("online") { it.conn == ConnState.ONLINE }
            val parent = r.security.head().core.epoch
            drop.set(true)
            runCatching { r.removeDevice(victim.deviceId) }
            val store = TestSecurity.get(server.url).stores.getValue(daemon.phone.deviceId)
            assertTrue(store.readSecurityOperation() != null)
            val exact = store.readSecurityOperation()!!.signed.copyOf()
            r.resumeSecurityOperation()
            assertNull(store.readSecurityOperation())
            assertEquals(parent + 1uL, r.security.head().core.epoch)
            val request =
                io.github.lewin671.remoteagent.proto.PutSecurityStateRequest.parseFrom(exact)
            assertArrayEquals(
                request.statesList.last().state.toByteArray(),
                DeviceSecurity.stateBytes(r.security.head()),
            )
        }

    @Test
    fun expiredLeaseInAnUnpublishedOperationIsRenewedWithoutResigningItsState() =
        resumeExpiredCertificate(false)

    @Test
    fun expiredLeaseAfterStateCommitIsRenewedWithoutResigningItsState() =
        resumeExpiredCertificate(true)

    private fun resumeExpiredCertificate(alreadyCommitted: Boolean) =
        runBlocking<Unit> {
            val original = fixtureRemote(daemon.phone, http)
            closers += AutoCloseable { original.close() }
            original.start()
            original.awaitState("online") { it.conn == ConnState.ONLINE }
            val prepared = original.prepareRecovery()
            val state = prepared.state
            val signer = SigningKey(daemon.phone.deviceSeed)
            val unsigned =
                HeadLease.sign(state, signer, state.core.issuedAt.toLong())
                    .copy(notAfter = state.core.issuedAt + 1uL)
            val expired = unsigned.copy(signature = signer.sign(unsigned.signBytes()))
            val request =
                io.github.lewin671.remoteagent.proto.PutSecurityStateRequest.newBuilder()
                    .addStates(
                        io.github.lewin671.remoteagent.proto.SecurityState.newBuilder()
                            .setState(
                                com.google.protobuf.ByteString.copyFrom(
                                    DeviceSecurity.stateBytes(state),
                                ),
                            ),
                    )
                    .setLease(com.google.protobuf.ByteString.copyFrom(expired.bytes()))
                    .build()
            if (alreadyCommitted) {
                original.api.client
                    .putSecurityState(
                        request.toBuilder().clearLease().build(),
                        Api.headers(original.currentToken()),
                    )
                    .value()
            }
            val store = TestSecurity.get(server.url).stores.getValue(daemon.phone.deviceId)
            val op = SecurityOperation("state", state.core.parent, request.toByteArray())
            store.writeSecurityOperation(op)
            delay(5)
            assertTrue(expired.notAfter <= System.currentTimeMillis().toULong())
            original.close()
            val savedBeforeUpload = java.util.concurrent.atomic.AtomicBoolean(true)
            val checking =
                http
                    .newBuilder()
                    .addInterceptor { chain ->
                        val request = if (chain.request().url.encodedPath.endsWith("/PutSecurityState")) chain.request().inspectBody { bytes ->
                            val sent =
                                io.github.lewin671.remoteagent.proto.PutSecurityStateRequest
                                    .parseFrom(bytes)
                            if (!sent.lease.isEmpty) {
                                val saved = store.readSecurityOperation()!!
                                val durable =
                                    io.github.lewin671.remoteagent.proto.PutSecurityStateRequest
                                        .parseFrom(saved.signed)
                                if (durable.lease != sent.lease) savedBeforeUpload.set(false)
                            }
                        } else chain.request()
                        chain.proceed(request)
                    }
                    .build()
            val reopened = fixtureRemote(daemon.phone, checking)
            closers += AutoCloseable { reopened.close() }
            reopened.resumeSecurityOperation()
            assertTrue(
                "replacement certificate was published before being saved",
                savedBeforeUpload.get(),
            )
            assertNull(store.readSecurityOperation())
            assertArrayEquals(
                DeviceSecurity.stateBytes(state),
                DeviceSecurity.stateBytes(reopened.security.head()),
            )
            val certificate = reopened.security.record().lease!!
            certificate.valid(state, System.currentTimeMillis().toULong())
            assertTrue(certificate.notAfter > expired.notAfter)
            assertEquals(state.core.epoch, reopened.security.head().core.epoch)
        }

    @Test
    fun independentRenewalLeavesPreparedRecoveryUnchanged() =
        runBlocking<Unit> {
            val r = fixtureRemote(daemon.phone, http)
            closers += AutoCloseable { r.close() }
            r.start()
            r.awaitState("online") { it.conn == ConnState.ONLINE }
            val prepared = r.prepareRecovery()
            val head = r.security.head()
            val renewed =
                HeadLease.sign(
                    head,
                    SigningKey(daemon.phone.deviceSeed),
                    System.currentTimeMillis(),
                )
            r.api.client
                .putSecurityState(
                    io.github.lewin671.remoteagent.proto.PutSecurityStateRequest.newBuilder()
                        .setLease(com.google.protobuf.ByteString.copyFrom(renewed.bytes()))
                        .build(),
                    Api.headers(r.currentToken()),
                )
                .value()
            r.security.install(emptyList(), lease = renewed)
            r.publishRecovery(prepared)
            assertArrayEquals(
                RecoveryCode.decode(prepared.code)!!.seed.let { SigningKey(it).publicKey },
                r.security.head().core.recoveryEd,
            )
            assertEquals(head.core.epoch + 1uL, r.security.head().core.epoch)
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
                        acct.root,
                        acct.deviceSeed,
                        acct.deviceId,
                        acct.accountId,
                        acct.accountPublicKey,
                    ),
                    routed,
                    securityStorage = TestSecurity.get(server.url).stores.getValue(acct.deviceId),
                )
            closers += AutoCloseable { r.close() }
            try {
                r.start()
                while (hole.disconnections.get() == 0) delay(10)
                assertEquals(0L, blocked.count)
                gate.countDown()
                r.awaitState("reconnected after overflow") {
                    hole.attempts.get() > 1 && it.conn == ConnState.ONLINE && it.loaded
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
                r.awaitState("online") {
                    it.conn == ConnState.ONLINE && it.devices.containsKey(r.account.deviceId)
                }
                other.awaitState("online") {
                    it.conn == ConnState.ONLINE && it.devices.containsKey(r.account.deviceId)
                }
                other.refreshDevices()
                removeAsAuthority(daemon.phone, r.account.deviceId, http)
                val st = r.awaitState("removed") { it.removed }
                assertEquals(ConnState.OFFLINE, st.conn)
                // It stays put rather than reconnecting, also when asked to start.
                r.start()
                delay(1500)
                assertTrue(r.state.value.removed)
                assertEquals(ConnState.OFFLINE, r.state.value.conn)
                // Asked to try again, hold the retry login RPC so trying=!removed is observed
                // before the conflated StateFlow returns to removed=true on rejection.
                holdRetryLogin.set(true)
                r.retryLogin()
                retryEntered.await()
                r.awaitState("trying") { !it.removed }
                retryRelease.countDown()
                r.awaitState("removed again") { it.removed }
            } finally {
                retryRelease.countDown()
            }
        }

    @Test
    fun syncsListsEventsAndPushes() =
        runBlocking<Unit> {
            val r = client()
            r.start()
            val st =
                r.awaitState("session meta") {
                    it.conn == ConnState.ONLINE && it.sessions["s1"]?.info != null
                }
            assertEquals("fake-mac", machineName(st.machines[daemon.id], copy = ::messageTextForTest))
            assertTrue(st.machines[daemon.id]!!.online)
            val s = st.sessions.getValue("s1")
            assertEquals("修复登录超时", titleOf(s, copy = ::messageTextForTest))
            assertEquals("foo", projects(st, copy = ::messageTextForTest).single().name)

            val vm = r.awaitSessionTimeline("s1", 3)
            assertEquals(listOf(1L, 2L, 3L), vm.timeline.itemsList.map { it.seq })
            // A pushed event arrives in order, once.
            daemon.upload("s1", agent("第四条"))
            val vm4 = r.awaitSessionTimeline("s1", 4)
            assertEquals(listOf(1L, 2L, 3L, 4L), vm4.timeline.itemsList.map { it.seq })
            // A newer meta revision replaces the older one.
            daemon.putSession("s1", session("/Users/me/Code/foo", "新的标题"))
            r.awaitState("new title") { it.sessions["s1"]?.info?.title == "新的标题" }
        }

    @Test
    fun rejectedSavedTokenTriggersForceLoginAndReconnects() =
        runBlocking<Unit> {
            val acct = approvedClient(daemon.phone, http)
            val stale = LoginToken("stale-token", System.currentTimeMillis() + 30L * 24 * 60 * 60 * 1000)
            var clears = 0
            var savedToken: LoginToken? = stale
            val tokenStore = object : LoginTokenStore {
                override fun loadLoginToken(account: Account): LoginToken? = savedToken
                override fun saveLoginToken(account: Account, token: LoginToken) { savedToken = token }
                override fun clearLoginToken(account: Account) {
                    clears++
                    // Keep returning stale if clear raced or failed; forceLogin must bypass storage.
                }
            }
            val loginCalls = java.util.concurrent.atomic.AtomicInteger()
            val trackedHttp = http.newBuilder().addInterceptor { chain ->
                if (chain.request().url.encodedPath.endsWith("/Login")) loginCalls.incrementAndGet()
                chain.proceed(chain.request())
            }.build()
            val r = fixtureRemote(acct, trackedHttp, tokenStorage = tokenStore)
            closers += AutoCloseable { r.close() }
            r.start()
            val st = r.awaitState("online after forced relogin") { it.conn == ConnState.ONLINE && it.sessions["s1"]?.info != null }
            assertEquals(ConnState.ONLINE, st.conn)
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
            r.awaitState("all bulk sessions listed") { st -> sids.all { st.sessions[it]?.info != null } }
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
            assertEquals("expected zero leaked pending cache refs", 0, r.pendingCacheSizeForTest())

            // Cold start against the populated cache restores every event without network.
            val cold = fixtureRemote(r.account, http, memory)
            closers += AutoCloseable { cold.close() }
            cold.start()
            for (sid in sids) {
                val restored = cold.watchSession(sid, win).first { it.timeline.itemsCount == eventsPerSession }
                assertEquals(eventsPerSession, restored.timeline.itemsCount)
            }
        }

    @Test fun generationConflictStopsTheFileUploadWithoutResync() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.awaitState("online machine") { it.machines[daemon.id]?.online == true }
        var completes = 0
        var statuses = 0
        daemon.answer = { _, command ->
            val status = ImageUploadStatus.newBuilder().setState(ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING).setMaxChunkBytes(8192).build()
            when {
                command.hasBeginImageUpload() -> CommandResult.newBuilder().setBeginImageUpload(BeginImageUploadResponse.newBuilder().setStatus(status)).build()
                command.hasCompleteImageUpload() -> {
                    completes++
                    CommandResult.newBuilder().setErrorCode("generation_conflict").setErrorMessage("diagnostic changed").build()
                }
                else -> { statuses++; error("unexpected upload retry/resync") }
            }
        }
        val error = runCatching {
            uploadFile(r.uploadChannel(daemon.id), "file-transfer-id", "empty.bin", 0, { java.io.ByteArrayInputStream(byteArrayOf()) })
        }.exceptionOrNull() as CommandException
        assertTrue(error.fromDaemon)
        assertEquals("generation_conflict", error.code)
        assertFalse(error.unknown)
        assertEquals(1, completes)
        assertEquals(0, statuses)
    }

    @Test fun verifiedReadTimeoutAndFinalFailuresDoNotRetry() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.awaitState("online machine") { it.machines[daemon.id]?.online == true }
        for (code in listOf("canceled", "deadline_exceeded", "unknown", "generation_conflict")) {
            var calls = 0
            daemon.answer = { _, _ ->
                calls++
                CommandResult.newBuilder().setErrorCode(code).setErrorMessage("failure").build()
            }
            val command = if (code == "generation_conflict")
                Command.newBuilder().setCompleteImageUpload(CompleteImageUploadRequest.newBuilder().setTransferId("test-transfer-id")).build()
            else Command.newBuilder().setListModels(ListModelsRequest.getDefaultInstance()).build()
            val error = runCatching { r.machineCommand(daemon.id, command) }.exceptionOrNull() as CommandException
            assertTrue(error.fromDaemon)
            assertEquals(code, error.code)
            assertFalse(error.unknown)
            assertEquals(1, calls)
        }
    }

    @Test
    fun machineCommandCarriesSessionIdOnlyForSessionScopedCommands() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.awaitState("online machine") { it.machines[daemon.id]?.online == true }

        val headers = java.util.concurrent.CopyOnWriteArrayList<String>()
        daemon.answer = { header, cmd ->
            headers.add(header.sessionId)
            when (cmd.kindCase) {
                Command.KindCase.RESOLVE_APPROVAL -> CommandResult.newBuilder().setResolveApproval(ResolveApprovalResponse.getDefaultInstance()).build()
                else -> CommandResult.newBuilder().setSchedule(ScheduleResponse.getDefaultInstance()).build()
            }
        }
        r.machineCommand(
            daemon.id,
            Command.newBuilder().setResolveApproval(
                ResolveApprovalRequest.newBuilder().setSessionId("s1").setApprovalId("a1").setAllow(true),
            ).build(),
        )
        r.machineCommand(
            daemon.id,
            Command.newBuilder().setSchedule(ScheduleRequest.newBuilder().setOperation("list").setSessionId("s1")).build(),
        )
        assertEquals(listOf("s1", ""), headers.toList())
    }

    @Test
    fun forwardsCommandsAndReportsOffline() =
        runBlocking<Unit> {
            val r = client()
            r.start()
            r.awaitState("online machine") {
                it.conn == ConnState.ONLINE &&
                    it.machines[daemon.id]?.online == true &&
                    it.sessions["s1"]?.info != null
            }
            val vmFlow = r.watchSession("s1")
            vmFlow.first { it.timeline.loaded }
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
            assertEquals(PROTOCOL_VERSION, cmd.protocolVersion)
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
            r.awaitState("offline machine") { it.machines[daemon.id]?.online == false }
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
        r.awaitState("online machine") {
            it.conn == ConnState.ONLINE &&
                it.machines[daemon.id]?.online == true &&
                it.sessions["s1"]?.info != null
        }
        daemon.putMachine("fake-mac.local", protocol)
        r.awaitState("machine protocol") {
            it.machines[daemon.id]?.meta?.protocolVersion == protocol
        }
        try {
            val command=Command.newBuilder().setGetSettings(io.github.lewin671.remoteagent.proto.GetSettingsRequest.getDefaultInstance()).build()
            r.machineCommand(daemon.id, command)
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
            val e = refused(PROTOCOL_VERSION - 1)
            assertEquals("failed_precondition", e.code)
            assertEquals("fake-mac 上的 remote-agent 版本较旧，请在电脑上更新后再试", errorText(e).testText())
        }

    @Test
    fun sendsNothingToAMachineOfANewerProtocol() =
        runBlocking<Unit> {
            val e = refused(PROTOCOL_VERSION + 1)
            assertEquals("failed_precondition", e.code)
            assertEquals(messageTextForTest(UiMessages.commonAndroidThisAppVersionIsOldUpdateAndRetry()), errorText(e).testText())
        }

    @Test
    fun streamsTheTerminal() =
        runBlocking<Unit> {
            val r = client()
            r.start()
            r.awaitState("session") {
                it.conn == ConnState.ONLINE &&
                    it.sessions["s1"] != null &&
                    it.machines[daemon.id]?.online == true
            }
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
            r.awaitState("online") { it.conn == ConnState.ONLINE }
            val init = Initiation.start(r, pollIntervalMs = 10)
            val link = PairLink.parse(init.link.url("https://app.example.com"))!!
            assertEquals(init.code, link.code)
            var joinerEmoji = emptyList<String>()
            val joining = async {
                joinByPairing(
                    link,
                    http,
                    { joinerEmoji = it },
                    { false },
                    persistRegistration = {},
                    persistIdentity = {},
                    persist = { acct, record ->
                        TestSecurity.get(acct.server).stores[acct.deviceId] =
                            TestSecurityStore(record)
                    },
                    confirm = { true },
                    pollIntervalMs = 10,
                )
            }
            val (emoji, _) = init.joined()
            init.approve()
            val acct = joining.await()
            assertEquals(emoji, joinerEmoji)
            assertFalse(acct.hasRoot)
            assertEquals(server.url, acct.server)
            assertEquals(daemon.phone.accountId, acct.accountId)
        }

    @Test
    fun pairingRejectsAuthorityIdentityBeforePublishingState() = runBlocking<Unit> {
        val writes = java.util.concurrent.atomic.AtomicInteger()
        val measured = http.newBuilder().addInterceptor { chain ->
            if (chain.request().url.encodedPath.endsWith("/PutSecurityState") ||
                chain.request().url.encodedPath.endsWith("/ApprovePairing")) writes.incrementAndGet()
            chain.proceed(chain.request())
        }.build()
        val r = fixtureRemote(daemon.phone, measured)
        closers += AutoCloseable { r.close() }
        val before = r.security.head()
        val init = Initiation.start(r, pollIntervalMs = 10)
        val api = Api(server.url, http)
        api.pairing.joinPairing(
            JoinPairingRequest.newBuilder()
                .setCode(init.code).setProtocolVersion(PROTOCOL_VERSION)
                .setJoinerKind(DeviceKind.DEVICE_KIND_CLIENT)
                .setDevicePublicKey(com.google.protobuf.ByteString.copyFrom(SigningKey(daemon.phone.deviceSeed).publicKey))
                .setDeviceHpkePublicKey(com.google.protobuf.ByteString.copyFrom(PairKey().public))
                .setJoinerPublic(com.google.protobuf.ByteString.copyFrom(PairKey().public)).build(),
            emptyMap(),
        ).value()
        init.joined()
        val failure = runCatching { init.approve() }.exceptionOrNull()
        assertTrue(failure is PairException)
        assertEquals(0, writes.get())
        assertNull(TestSecurity.get(server.url).stores.getValue(daemon.phone.deviceId).readSecurityOperation())
        assertArrayEquals(DeviceSecurity.stateHash(before), DeviceSecurity.stateHash(r.security.head()))
    }

    @Test
    fun fetchesAnImageAndCachesOnlyItsEnvelope() =
        runBlocking<Unit> {
            val cache = MemoryCache()
            val r = client(cache)
            r.start()
            r.awaitState("session") {
                it.conn == ConnState.ONLINE && it.sessions["s1"]?.info != null
            }
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
            a.awaitState("a prefs") { it.conn == ConnState.ONLINE && it.prefs != null }
            b.awaitState("b prefs") { it.conn == ConnState.ONLINE && it.prefs != null }
            a.coreHost.toggleProjectPin("/Users/me/Code/foo")
            a.awaitState("a pinned") { it.prefs?.pinned?.contains("/Users/me/Code/foo") == true }
            b.coreHost.toggleProjectHide("/Users/me/Code/billing")
            val sa =
                a.awaitState("a sees both") {
                    it.prefs?.hidden?.contains("/Users/me/Code/billing") == true
                }
            assertEquals(listOf("/Users/me/Code/foo"), sa.prefs!!.pinned)
            assertEquals(2L, sa.prefs!!.revision)
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
            val st = r.awaitState("preferences") { it.prefs != null }
            assertEquals(ConnState.ONLINE, st.conn)
            assertEquals(2, reads.get())
        }

    @Test
    fun aClientsNameReachesTheOtherClients() =
        runBlocking<Unit> {
            val a = client()
            val b = client()
            a.start()
            b.start()
            a.awaitState("a devices") {
                it.conn == ConnState.ONLINE && it.devices.containsKey(a.account.deviceId)
            }
            b.awaitState("b sees a") { it.devices.containsKey(a.account.deviceId) }
            a.describeSelf("android", "Android 16 · Pixel 9")
            a.renameDevice(a.account.deviceId, "我的手机")
            val sb =
                b.awaitState("b sees the name") {
                    it.devices[a.account.deviceId]?.meta?.name == "我的手机"
                }
            assertEquals("我的手机", clientName(sb.devices[a.account.deviceId], a.account.deviceId, copy = ::messageTextForTest))
        }

    @Test fun scheduledSnapshotCannotReturnAfterRevocationDuringCacheRead() = runBlocking<Unit> {
        val memory=MemoryCache();val entered=CompletableDeferred<Unit>();val release=CompletableDeferred<Unit>();val hold=java.util.concurrent.atomic.AtomicBoolean(false)
        val cache=object:CipherCache by memory {
            override suspend fun blob(name:String):ByteArray? {
                val value=memory.blob(name)
                if(name.startsWith("schedule:") && hold.get()) {entered.complete(Unit);release.await()}
                return value
            }
        }
        val r=client(cache=cache);r.start()
        try {
            r.awaitState("connected") {it.conn==ConnState.ONLINE && it.machines[daemon.account.deviceId]?.online==true}
            daemon.answer={_,_->CommandResult.newBuilder().setSchedule(ScheduleResponse.newBuilder().setResultJson(com.google.protobuf.ByteString.copyFromUtf8("{\"tasks\":[]}"))).build()}
            r.machineCommand(daemon.account.deviceId,Command.newBuilder().setSchedule(ScheduleRequest.newBuilder().setOperation("list")).build())
            hold.set(true);val reading=async {r.cachedSchedules(daemon.account.deviceId)}
            entered.await();removeAsAuthority(daemon.phone,daemon.account.deviceId,http);r.syncSecurity()
            release.complete(Unit);assertNull(reading.await());assertNull(memory.blob("schedule:${daemon.account.deviceId}"))
        } finally {release.complete(Unit)}
    }
    @Test fun scheduledSnapshotCannotReturnAfterRevocationDuringCacheWrite() = runBlocking<Unit> {
        val memory=MemoryCache();val entered=CompletableDeferred<Unit>();val release=CompletableDeferred<Unit>()
        val cache=object:CipherCache by memory {
            override suspend fun putBlob(name:String,data:ByteArray) {
                if(name.startsWith("schedule:")) {entered.complete(Unit);release.await()}
                memory.putBlob(name,data)
            }
        }
        val r=client(cache=cache);r.start()
        try {
            r.awaitState("connected") {it.conn==ConnState.ONLINE && it.machines[daemon.account.deviceId]?.online==true}
            daemon.answer={_,_->CommandResult.newBuilder().setSchedule(ScheduleResponse.newBuilder().setResultJson(com.google.protobuf.ByteString.copyFromUtf8("{\"tasks\":[]}"))).build()}
            val writing=async {r.machineCommand(daemon.account.deviceId,Command.newBuilder().setSchedule(ScheduleRequest.newBuilder().setOperation("list")).build())}
            entered.await();removeAsAuthority(daemon.phone,daemon.account.deviceId,http);r.syncSecurity()
            release.complete(Unit);writing.await()
            assertNull(memory.blob("schedule:${daemon.account.deviceId}"))
        } finally {release.complete(Unit)}
    }
    @Test fun scheduledSnapshotsKeepSignedRepliesAndVerifyThemAgainOffline() = runBlocking<Unit> {
        val memory=MemoryCache();val r=client(cache=memory);r.start()
        r.awaitState("connected") { it.conn==ConnState.ONLINE && it.machines[daemon.account.deviceId]?.online==true }
        val originalAnswer=daemon.answer
        val json="""{"tasks":[{"id":"task","spec":{"title":"Private scheduled content"}}]}""".toByteArray()
        daemon.answer={ h,c -> if(c.hasSchedule()) io.github.lewin671.remoteagent.proto.CommandResult.newBuilder().setSchedule(io.github.lewin671.remoteagent.proto.ScheduleResponse.newBuilder().setResultJson(com.google.protobuf.ByteString.copyFrom(json))).build() else originalAnswer(h,c) }
        r.machineCommand(daemon.account.deviceId,io.github.lewin671.remoteagent.proto.Command.newBuilder().setSchedule(io.github.lewin671.remoteagent.proto.ScheduleRequest.newBuilder().setOperation("list")).build())
        val key="schedule:${daemon.account.deviceId}";val stored=memory.blob(key)!!
        assertFalse(stored.decodeToString().contains("Private scheduled content"))
        assertArrayEquals(json,r.cachedSchedules(daemon.account.deviceId))
        val snapshot=kotlinx.serialization.json.Json.parseToJsonElement(stored.decodeToString()).jsonObject
        val envText=(snapshot["envelope"] as kotlinx.serialization.json.JsonPrimitive).content
        val reqText=(snapshot["request"] as kotlinx.serialization.json.JsonPrimitive).content
        val env=io.github.lewin671.remoteagent.proto.Envelope.parseFrom(java.util.Base64.getDecoder().decode(envText))
        assertFalse(env.signature.isEmpty);assertFalse(env.securityHeader.isEmpty)
        memory.putBlob(key,scheduleSnapshot("wrong-request",env))
        assertTrue(runCatching { r.cachedSchedules(daemon.account.deviceId) }.isFailure)
        memory.putBlob(key,scheduleSnapshot(reqText,env.toBuilder().setSignature(com.google.protobuf.ByteString.copyFrom(ByteArray(64))).build()))
        assertTrue(runCatching { r.cachedSchedules(daemon.account.deviceId) }.isFailure)
        memory.putBlob(key,stored)
        val cold=fixtureRemote(r.account,http,memory);closers+=AutoCloseable { cold.close() }
        assertArrayEquals(json,cold.cachedSchedules(daemon.account.deviceId))
        removeAsAuthority(daemon.phone,daemon.account.deviceId,http)
        r.syncSecurity()
        assertNull(memory.blob(key));assertNull(r.cachedSchedules(daemon.account.deviceId))
    }

    @Test fun coreRejectsUnsequencedZeroOpSecurityAndDevicesEvents() = runBlocking<Unit> {
        val r = client()
        r.start()
        r.awaitState("connected") { it.conn == ConnState.ONLINE && r.coreHost.syncStateView.value.securityLoaded }
        val expectedAuthority = r.coreHost.syncStateView.value.authorityDeviceId
        assertTrue(r.coreHost.syncStateView.value.securityLoaded)
        assertTrue(r.coreHost.syncStateView.value.isMember)
        assertTrue(expectedAuthority.isNotEmpty())

        // Dispatching op_id=0, push_seq=0 SecuritySnapshot must be ignored by core.
        r.coreHost.dispatchAndWait(
            io.github.lewin671.remoteagent.core.proto.Event.newBuilder()
                .setSecuritySnapshot(
                    io.github.lewin671.remoteagent.core.proto.SecuritySnapshotEvent.newBuilder()
                        .setOpId(0L)
                        .setPushSeq(0L)
                        .setIsMember(false)
                        .setAuthorityDeviceId("spoofed")
                        .setHeadEpoch(999L),
                )
                .build(),
        )
        assertEquals(expectedAuthority, r.coreHost.syncStateView.value.authorityDeviceId)
        assertTrue(r.coreHost.syncStateView.value.isMember)

        // Shell refreshDevices sends a monotonic push_seq and keeps devices populated.
        r.refreshDevices()
        assertTrue(r.state.value.devices.containsKey(r.account.deviceId))

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
        assertTrue(r.state.value.devices.containsKey(r.account.deviceId))
    }

    @Test fun securityInstallAndFinishPairingPushSnapshotToCore() = runBlocking<Unit> {
        val r = fixtureRemote(daemon.phone, http)
        closers += AutoCloseable { r.close() }
        r.start()
        r.awaitState("connected") { it.conn == ConnState.ONLINE && r.coreHost.syncStateView.value.securityLoaded }

        val phoneStore = TestSecurity.get(server.url)!!.stores.getValue(daemon.phone.deviceId)
        val seed1 = randomBytes(32)
        val cp1 = SigningKey(seed1).publicKey
        val m1Id = deviceId(cp1)
        val xp1 = PairKey().public
        r.coreHost.dispatchAndWait(
            io.github.lewin671.remoteagent.core.proto.Event.newBuilder()
                .setStreamFrame(
                    io.github.lewin671.remoteagent.core.proto.StreamFrameEvent.newBuilder()
                        .setLegId(1L)
                        .setSessionUpdated(io.github.lewin671.remoteagent.core.proto.PlainSession.newBuilder().setSessionId("s-pair-1").setMachineId(m1Id)),
                )
                .build(),
        )
        r.awaitState("s-pair-1 listed") { it.sessions.containsKey("s-pair-1") }
        val head1 = r.security.head()
        val key1 = r.security.contentKey(head1.core.contentEpoch)!!
        val state1 = Authority.pair(
            head1,
            DeviceSecurity.Member(m1Id, cp1, xp1, DeviceKind.DEVICE_KIND_MACHINE.number.toUInt()),
            key1,
            Authority.newSecret(),
            SigningKey(daemon.phone.deviceSeed),
            r.security.signingTime(System.currentTimeMillis()),
        )
        r.security.install(listOf(state1))
        val flow1 = r.watchSession("s-pair-1")
        flow1.first { it.timeline.loaded }
        r.coreHost.dispatchAndWait(
            io.github.lewin671.remoteagent.core.proto.Event.newBuilder()
                .setStreamFrame(
                    io.github.lewin671.remoteagent.core.proto.StreamFrameEvent.newBuilder()
                        .setLegId(1L)
                        .setDeviceUpdated(
                            io.github.lewin671.remoteagent.core.proto.PlainDevice.newBuilder()
                                .setDeviceId(m1Id)
                                .setKind(DeviceKind.DEVICE_KIND_MACHINE),
                        ),
                )
                .build(),
        )
        kotlinx.coroutines.withTimeout(5_000L) {
            r.coreHost.syncStateView.first { view -> view.devicesList.any { it.deviceId == m1Id && it.trusted } }
        }
        r.coreHost.dispatchAndWait(
            io.github.lewin671.remoteagent.core.proto.Event.newBuilder()
                .setStreamFrame(
                    io.github.lewin671.remoteagent.core.proto.StreamFrameEvent.newBuilder()
                        .setLegId(1L)
                        .setEventsAppended(
                            io.github.lewin671.remoteagent.core.proto.EventsAppendedFrame.newBuilder()
                                .setSessionId("s-pair-1")
                                .addEvents(
                                    io.github.lewin671.remoteagent.core.proto.PlainEvent.newBuilder()
                                        .setSeq(1L)
                                        .setVerified(true)
                                        .setEvent(agent("relay-after-install").setSessionId("s-pair-1").setSeq(1).build()),
                                ),
                        ),
                )
                .build(),
        )
        val vm1 = flow1.first { it.timeline.itemsCount >= 1 }
        org.junit.Assert.assertEquals(1, vm1.timeline.itemsCount)

        // finishPairingOperation must also push the updated security snapshot even when
        // storage already has the new state (e.g. synced/stored before pairing op cleared).
        val seed2 = randomBytes(32)
        val cp2 = SigningKey(seed2).publicKey
        val m2Id = deviceId(cp2)
        val xp2 = PairKey().public
        r.coreHost.dispatchAndWait(
            io.github.lewin671.remoteagent.core.proto.Event.newBuilder()
                .setStreamFrame(
                    io.github.lewin671.remoteagent.core.proto.StreamFrameEvent.newBuilder()
                        .setLegId(1L)
                        .setSessionUpdated(io.github.lewin671.remoteagent.core.proto.PlainSession.newBuilder().setSessionId("s-pair-2").setMachineId(m2Id)),
                )
                .build(),
        )
        r.awaitState("s-pair-2 listed") { it.sessions.containsKey("s-pair-2") }
        val head2 = r.security.head()
        val key2 = r.security.contentKey(head2.core.contentEpoch)!!
        val s2 = Authority.pair(
            head2,
            DeviceSecurity.Member(m2Id, cp2, xp2, DeviceKind.DEVICE_KIND_MACHINE.number.toUInt()),
            key2,
            Authority.newSecret(),
            SigningKey(daemon.phone.deviceSeed),
            r.security.signingTime(System.currentTimeMillis()),
        )
        phoneStore.installSecurity(DeviceSecurity.stateHash(head2), listOf(s2), listOf(null))
        val approveReq = ApprovePairingRequest.newBuilder().setPairingId("pair-finish").build().toByteArray()
        phoneStore.writeSecurityOperation(SecurityOperation("state", s2.core.parent, byteArrayOf(), delivery = approveReq))
        r.finishPairingOperation("pair-finish")
        val flow2 = r.watchSession("s-pair-2")
        flow2.first { it.timeline.loaded }
        r.coreHost.dispatchAndWait(
            io.github.lewin671.remoteagent.core.proto.Event.newBuilder()
                .setStreamFrame(
                    io.github.lewin671.remoteagent.core.proto.StreamFrameEvent.newBuilder()
                        .setLegId(1L)
                        .setDeviceUpdated(
                            io.github.lewin671.remoteagent.core.proto.PlainDevice.newBuilder()
                                .setDeviceId(m2Id)
                                .setKind(DeviceKind.DEVICE_KIND_MACHINE),
                        ),
                )
                .build(),
        )
        kotlinx.coroutines.withTimeout(5_000L) {
            r.coreHost.syncStateView.first { view -> view.devicesList.any { it.deviceId == m2Id && it.trusted } }
        }
        r.coreHost.dispatchAndWait(
            io.github.lewin671.remoteagent.core.proto.Event.newBuilder()
                .setStreamFrame(
                    io.github.lewin671.remoteagent.core.proto.StreamFrameEvent.newBuilder()
                        .setLegId(1L)
                        .setEventsAppended(
                            io.github.lewin671.remoteagent.core.proto.EventsAppendedFrame.newBuilder()
                                .setSessionId("s-pair-2")
                                .addEvents(
                                    io.github.lewin671.remoteagent.core.proto.PlainEvent.newBuilder()
                                        .setSeq(1L)
                                        .setVerified(true)
                                        .setEvent(agent("relay-after-finish").setSessionId("s-pair-2").setSeq(1).build()),
                                ),
                        ),
                )
                .build(),
        )
        val vm2 = flow2.first { it.timeline.itemsCount >= 1 }
        org.junit.Assert.assertEquals(1, vm2.timeline.itemsCount)
    }

    @Test fun securityInstallDuringInFlightSyncIsNotDroppedOrOverwritten() = runBlocking<Unit> {
        val holdSync = java.util.concurrent.atomic.AtomicBoolean(false)
        val syncEntered = CompletableDeferred<Unit>()
        val syncRelease = java.util.concurrent.CountDownLatch(1)
        val gatedHttp =
            http
                .newBuilder()
                .addInterceptor { chain ->
                    val response = chain.proceed(chain.request())
                    if (chain.request().url.encodedPath.endsWith("/GetSecurityStates") &&
                        holdSync.compareAndSet(true, false)
                    ) {
                        syncEntered.complete(Unit)
                        syncRelease.await()
                    }
                    response
                }
                .build()
        val r = fixtureRemote(daemon.phone, gatedHttp)
        closers += AutoCloseable { r.close() }
        try {
            r.start()
            r.awaitState("connected") { it.conn == ConnState.ONLINE && r.coreHost.syncStateView.value.securityLoaded }

            val seed1 = randomBytes(32)
            val cp1 = SigningKey(seed1).publicKey
            val m1Id = deviceId(cp1)
            val xp1 = PairKey().public
            r.coreHost.dispatchAndWait(
                io.github.lewin671.remoteagent.core.proto.Event.newBuilder()
                    .setStreamFrame(
                        io.github.lewin671.remoteagent.core.proto.StreamFrameEvent.newBuilder()
                            .setLegId(1L)
                            .setSessionUpdated(
                                io.github.lewin671.remoteagent.core.proto.PlainSession.newBuilder()
                                    .setSessionId("s-pair-sync-race")
                                    .setMachineId(m1Id),
                            ),
                    )
                    .build(),
            )
            r.awaitState("s-pair-sync-race listed") { it.sessions.containsKey("s-pair-sync-race") }

            holdSync.set(true)
            val syncJob = launch(kotlinx.coroutines.Dispatchers.Default) { runCatching { r.syncSecurity() } }
            syncEntered.await()

            val head1 = r.security.head()
            val key1 = r.security.contentKey(head1.core.contentEpoch)!!
            val state1 = Authority.pair(
                head1,
                DeviceSecurity.Member(m1Id, cp1, xp1, DeviceKind.DEVICE_KIND_MACHINE.number.toUInt()),
                key1,
                Authority.newSecret(),
                SigningKey(daemon.phone.deviceSeed),
                r.security.signingTime(System.currentTimeMillis()),
            )
            r.security.install(listOf(state1))
            r.coreHost.dispatchAndWait(
                io.github.lewin671.remoteagent.core.proto.Event.newBuilder()
                    .setStreamFrame(
                        io.github.lewin671.remoteagent.core.proto.StreamFrameEvent.newBuilder()
                            .setLegId(1L)
                            .setDeviceUpdated(
                                io.github.lewin671.remoteagent.core.proto.PlainDevice.newBuilder()
                                    .setDeviceId(m1Id)
                                    .setKind(DeviceKind.DEVICE_KIND_MACHINE),
                            ),
                    )
                    .build(),
            )
            kotlinx.coroutines.withTimeout(5_000L) {
                r.coreHost.syncStateView.first { view -> view.devicesList.any { it.deviceId == m1Id && it.trusted } }
            }

            val flow1 = r.watchSession("s-pair-sync-race")
            flow1.first { it.timeline.loaded }
            r.coreHost.dispatchAndWait(
                io.github.lewin671.remoteagent.core.proto.Event.newBuilder()
                    .setStreamFrame(
                        io.github.lewin671.remoteagent.core.proto.StreamFrameEvent.newBuilder()
                            .setLegId(1L)
                            .setEventsAppended(
                                io.github.lewin671.remoteagent.core.proto.EventsAppendedFrame.newBuilder()
                                    .setSessionId("s-pair-sync-race")
                                    .addEvents(
                                        io.github.lewin671.remoteagent.core.proto.PlainEvent.newBuilder()
                                            .setSeq(1L)
                                            .setVerified(true)
                                            .setEvent(agent("relay-during-sync").setSessionId("s-pair-sync-race").setSeq(1).build()),
                                    ),
                            ),
                    )
                    .build(),
            )
            val vm1 = flow1.first { it.timeline.itemsCount >= 1 }
            org.junit.Assert.assertEquals(1, vm1.timeline.itemsCount)

            syncRelease.countDown()
            syncJob.join()
            org.junit.Assert.assertTrue(r.coreHost.syncStateView.value.devicesList.any { it.deviceId == m1Id && it.trusted })

            r.coreHost.dispatchAndWait(
                io.github.lewin671.remoteagent.core.proto.Event.newBuilder()
                    .setStreamFrame(
                        io.github.lewin671.remoteagent.core.proto.StreamFrameEvent.newBuilder()
                            .setLegId(1L)
                            .setEventsAppended(
                                io.github.lewin671.remoteagent.core.proto.EventsAppendedFrame.newBuilder()
                                    .setSessionId("s-pair-sync-race")
                                    .addEvents(
                                        io.github.lewin671.remoteagent.core.proto.PlainEvent.newBuilder()
                                            .setSeq(2L)
                                            .setVerified(true)
                                            .setEvent(agent("relay-after-sync").setSessionId("s-pair-sync-race").setSeq(2).build()),
                                    ),
                            ),
                    )
                    .build(),
            )
            val vm2 = flow1.first { it.timeline.itemsCount >= 2 }
            org.junit.Assert.assertEquals(2, vm2.timeline.itemsCount)
        } finally {
            syncRelease.countDown()
        }
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
