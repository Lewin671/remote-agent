package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.transport.DispatchState

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
import kotlinx.coroutines.async
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
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
        closers.forEach { it.close() }
        if (this::daemon.isInitialized) daemon.close()
        server.close()
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
    ): Remote {
        val acct = approvedClient(daemon.phone, http)
        val r =
            fixtureRemote(
                acct,
                http,
                cache,
                frameQueue = frameQueue,
                historyIdleMs = historyIdleMs,
                historyIdleCount = historyIdleCount,
                linkGraceMs = linkGraceMs,
            )
        closers += AutoCloseable { r.close() }
        return r
    }

    @Test fun rpcTransportPreventsUncountedPostReplay() = runBlocking<Unit> {
        val r=client();r.start();r.awaitState("online") {it.conn==ConnState.ONLINE}
        val prepared=r.prepare(daemon.id,"s1",Command.newBuilder().setInterrupt(InterruptRequest.newBuilder().setSessionId("s1")).build())
        val envelope=r.authorize(prepared)
        for(mode in listOf("disconnect","retry-after","request-timeout","h2-refused","redirect","authentication","interceptor").filter { System.getenv("RA_WIRE_CASE").isNullOrEmpty() || System.getenv("RA_WIRE_CASE") == it }) {
            for(protected in listOf(false,true)) {
                RetryWireServer(mode).use { wire ->
                    val transport=http.newBuilder().retryOnConnectionFailure(true).apply {
                        if(mode=="h2-refused") protocols(listOf(okhttp3.Protocol.H2_PRIOR_KNOWLEDGE))
                        if(mode=="redirect") followRedirects(true)
                        if(mode=="authentication") authenticator { _,response -> response.request.newBuilder().header("Authorization","fixture").build() }
                        if(mode=="interceptor") addInterceptor { chain ->
                            val first=chain.proceed(chain.request());first.close();chain.proceed(chain.request())
                        }
                    }.build()
                    if(mode!="interceptor") transport.newCall(okhttp3.Request.Builder().url(wire.url+"/warm").build()).execute().use {it.body.string()}
                    val api=if(protected) Api(wire.url,transport).client else ClientServiceClient(com.connectrpc.impl.ProtocolClient(com.connectrpc.okhttp.ConnectOkHttpClient(transport),com.connectrpc.ProtocolClientConfig(host=wire.url,serializationStrategy=LiteProtobufStrategy,networkProtocol=com.connectrpc.protocols.NetworkProtocol.CONNECT)))
                    val state=DispatchState(false);val call=state.open(false);val delivery=call.delivery()
                    val request=ForwardRequest.newBuilder().setMachineId(daemon.id).setCommand(envelope).setAttemptId("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").setRelayEpoch("wire-epoch").build()
                    delivery.dispatch()
                    val error=runCatching {api.forward(request,emptyMap()).value()}.exceptionOrNull() as com.connectrpc.ConnectException
                    val refusal=error.details.mapNotNull {runCatching {NotForwarded.parseFrom(it.payload.toByteArray())}.getOrNull()}.any {it.attemptId==request.attemptId&&it.relayEpoch==request.relayEpoch}
                    if(refusal)delivery.notForwarded()
                    org.junit.Assert.assertEquals("$mode protected=$protected error=$error",if(protected)1 else 2,wire.posts.get())
                    org.junit.Assert.assertEquals(protected,call.uncertain)
                    org.junit.Assert.assertTrue(wire.attempts.all {it==request.attemptId})
                    call.close()
                }
            }
        }
    }

    @Test fun aDnsErrorAfterDeliveryCannotClaimNotSent() = runBlocking<Unit> {
        val delivered = java.util.concurrent.atomic.AtomicInteger()
        val transport = http.newBuilder().addInterceptor { chain ->
            val response = chain.proceed(chain.request())
            if (chain.request().url.encodedPath.endsWith("/Forward")) {
                response.close(); delivered.incrementAndGet()
                throw java.net.UnknownHostException("after delivery")
            }
            response
        }.build()
        val account = approvedClient(daemon.phone, transport)
        val r = fixtureRemote(account, transport)
        closers += AutoCloseable { r.close() }
        r.start();r.awaitState("online") { it.conn == ConnState.ONLINE }
        val p = r.prepare(daemon.id, "s1", Command.newBuilder().setInterrupt(InterruptRequest.newBuilder().setSessionId("s1")).build())
        try { r.forward(p); fail("lost response returned success") } catch (e:Exception) { assertTrue(uncertain(e)) }
        assertEquals(1,delivered.get());assertTrue(p.mayHaveRun)
    }

    @Test fun loginDnsBeforeDispatchCannotSpendThePreviousAttempt() = runBlocking<Unit> {
        val now=java.util.concurrent.atomic.AtomicLong(System.currentTimeMillis())
        val failLogin=java.util.concurrent.atomic.AtomicBoolean()
        val gated=http.newBuilder().addInterceptor { chain ->
            if(failLogin.get()&&chain.request().url.encodedPath.endsWith("/GetChallenge"))throw java.net.UnknownHostException("fixture login DNS")
            chain.proceed(chain.request())
        }.build()
        val r=fixtureRemote(approvedClient(daemon.phone,http),gated,clock=now::get);closers+=AutoCloseable{r.close()}
        r.start();r.awaitState("online") {it.conn==ConnState.ONLINE}
        val p=r.prepare(daemon.id,"s1",Command.newBuilder().setInterrupt(InterruptRequest.newBuilder().setSessionId("s1")).build())
        daemon.garbleResults=true
        org.junit.Assert.assertTrue(uncertain(runCatching {r.forward(p)}.exceptionOrNull()!!))
        daemon.garbleResults=false
        r.stop();r.awaitState("stopped") {it.conn==ConnState.OFFLINE}
        failLogin.set(true);now.addAndGet(31L*24*60*60*1000)
        val error=runCatching {r.forward(p)}.exceptionOrNull()
        org.junit.Assert.assertTrue("prior was lost: $error",error is CommandException && error.unknown)
        org.junit.Assert.assertTrue(p.dispatch.hasPrior)
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
        assertEquals(RETIRED_HISTORY_ERROR, withTimeout(5000) { r.events("retired").first { it.error != null } }.error)
        r.awaitState("current session") { it.sessions["s1"]?.info != null }
        val events = withTimeout(5000) { r.events("s1").first { it.error != null } }
        assertEquals(RETIRED_HISTORY_ERROR, events.error)
        assertTrue(events.events.isEmpty())
        assertEquals(1, cache.events("s1").size)
        cache.putImage("s1/retired", old.toBuilder().setHeader(Header.newBuilder().setVersion(1)
            .setKind(PayloadKind.PAYLOAD_KIND_IMAGE).build().toByteString()).build().toByteArray())
        assertEquals(Remote.ImageResult.Corrupt(RETIRED_HISTORY_ERROR), r.image("s1", "retired"))
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


    @Test fun createRetryKeepsRawPayloadAcrossRekeyAndClientRestart() = runBlocking<Unit> {
        val fixture=kotlinx.serialization.json.Json.parseToJsonElement(java.io.File(java.io.File(System.getProperty("outcomeVectors")).parentFile,"prepared-command.json").readText()).jsonObject
        val raw=java.util.Base64.getDecoder().decode(fixture.getValue("plain").jsonPrimitive.content)
        val command=Command.parseFrom(raw);assertEquals(3,command.createSession.envCount)
        assertFalse(raw.contentEquals(command.toByteArray()))
        val r=client();r.start();r.awaitState("online") {it.conn==ConnState.ONLINE&&it.machines[daemon.id]?.online==true}
        daemon.answer={_,cmd -> if(cmd.hasCreateSession()) CommandResult.newBuilder().setCreateSession(CreateSessionResponse.newBuilder().setSession(Session.newBuilder().setId("fixture-created"))).build() else FakeDaemon.defaultResult(cmd)}
        val p=PreparedCommand(daemon.id,"",newRequestId(),System.currentTimeMillis()+60000,Envelope.getDefaultInstance(),serializedCommand=raw, prior=false)
        r.forward(p)
        val before=p.authorizedEnvelope!!
        p.commandBytes()!!.fill(0)
        approvedClient(daemon.phone,http);r.syncSecurity()
        r.forward(p,retryOfUnknown=true)
        assertFalse(before==p.authorizedEnvelope)
        val saved=checkNotNull(p.commandBytes())
        r.close()
        val next=fixtureRemote(r.account,http);closers+=AutoCloseable{next.close()};next.start()
        next.awaitState("restarted") {it.conn==ConnState.ONLINE&&it.machines[daemon.id]?.online==true}
        val restored=PreparedCommand(p.machineId,p.sessionId,p.requestId,p.deadlineMs,Envelope.getDefaultInstance(),serializedCommand=saved, prior=true)
        next.forward(restored,retryOfUnknown=true)
        val rows=mutableListOf<Pair<Header,ByteArray>>();daemon.commandPayloads.drainTo(rows)
        val matching=rows.filter{it.first.requestId==p.requestId};assertEquals(3,matching.size)
        matching.forEach {org.junit.Assert.assertArrayEquals(raw,it.second);assertEquals(p.deadlineMs,it.first.deadlineMs)}
    }

    @Test
    fun authorizedActionsResealWithoutReobservingCompletedActions() =
        runBlocking<Unit> {
            val r = client()
            r.start()
            r.awaitState("session") {
                it.conn == ConnState.ONLINE && it.sessions["s1"]?.info != null
            }
            val hash = com.google.protobuf.ByteString.copyFrom(ByteArray(32) { 9 })
            val actions =
                listOf(
                    Command.newBuilder()
                        .setResolveApproval(
                            ResolveApprovalRequest.newBuilder()
                                .setSessionId("s1")
                                .setApprovalId("a")
                                .setObservedEventSeq(7),
                        )
                        .build(),
                    Command.newBuilder()
                        .setAnswerQuestion(
                            AnswerQuestionRequest.newBuilder()
                                .setSessionId("s1")
                                .setQuestionId("q")
                                .setObservedEventSeq(8),
                        )
                        .build(),
                    Command.newBuilder()
                        .setAnswerPlan(
                            AnswerPlanRequest.newBuilder()
                                .setSessionId("s1")
                                .setObservedEventSeq(9)
                                .setObservedScreenHash(hash),
                        )
                        .build(),
                )
            for (cmd in actions) {
                var completed = false
                daemon.liveSnapshot = {
                    val snapshot =
                        LiveSnapshot.newBuilder().setSession(Session.newBuilder().setId("s1"))
                    if (!completed)
                        snapshot
                            .addActions(
                                Event.newBuilder()
                                    .setSeq(7)
                                    .setApprovalRequested(
                                        ApprovalRequested.newBuilder().setApprovalId("a"),
                                    ),
                            )
                            .addActions(
                                Event.newBuilder()
                                    .setSeq(8)
                                    .setQuestionAsked(
                                        QuestionAsked.newBuilder().setQuestionId("q"),
                                    ),
                            )
                            .setPlanEventSeq(9)
                            .setPlanScreenHash(hash)
                    snapshot.build()
                }
                daemon.answer = { _, _ ->
                    completed = true
                    CommandResult.getDefaultInstance()
                }
                val p = r.prepare(daemon.id, "s1", cmd)
                r.forward(p)
                assertTrue("first authorization must execute before retry", completed)
                val original = p.authorizedEnvelope!!
                approvedClient(daemon.phone, http)
                r.syncSecurity()
                r.forward(p, retryOfUnknown = true)
                assertFalse(original == p.authorizedEnvelope)
                val received =
                    mutableListOf<Pair<io.github.lewin671.remoteagent.proto.Header, Command>>()
                while (true) received += daemon.commands.poll() ?: break
                val sent = received.filter { it.first.requestId == p.requestId }
                assertEquals(2, sent.size)
                assertEquals(sent[0], sent[1])
            }
        }

    @Test
    fun backgroundCancelsRenewalAndRetainsItsPendingOperation() =
        runBlocking<Unit> {
            val hold = java.util.concurrent.atomic.AtomicBoolean(false)
            val entered = java.util.concurrent.CountDownLatch(1)
            val release = java.util.concurrent.CountDownLatch(1)
            val published = java.util.concurrent.atomic.AtomicInteger()
            val slow =
                http
                    .newBuilder()
                    .addInterceptor { chain ->
                        if (
                            chain.request().url.encodedPath.endsWith("/GetSecurityStates") &&
                                hold.compareAndSet(true, false)
                        ) {
                            entered.countDown()
                            check(release.await(10, TimeUnit.SECONDS))
                        }
                        if (chain.request().url.encodedPath.endsWith("/PutSecurityState"))
                            published.incrementAndGet()
                        chain.proceed(chain.request())
                    }
                    .build()
            val phone = fixtureRemote(daemon.phone, slow)
            closers += AutoCloseable { phone.close() }
            phone.start()
            phone.awaitState("online") { it.conn == ConnState.ONLINE }
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
                phone.renewLeaseNow()
                assertTrue(
                    kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) {
                        entered.await(5, TimeUnit.SECONDS)
                    },
                )
                phone.stop()
                phone.awaitState("background") { it.conn == ConnState.OFFLINE }
                release.countDown()
                delay(500)
                assertEquals("background renewal published", 0, published.get())
                assertArrayEquals(pending.signed, storage.readSecurityOperation()!!.signed)
                phone.start()
                phone.renewLeaseNow()
                withTimeout(5000) {
                    while (storage.readSecurityOperation() != null || published.get() == 0) delay(
                        20,
                    )
                }
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
                withTimeout(5000) { checked.await() },
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
                    check(blocked.await(10, TimeUnit.SECONDS))
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
                            gate.await(10, TimeUnit.SECONDS)
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
                withTimeout(5000) { while (hole.disconnections.get() == 0) delay(10) }
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
            val r = client()
            val other = client()
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
            // Asked to try again, it is turned away again.
            r.retryLogin()
            r.awaitState("trying") { !it.removed }
            r.awaitState("removed again") { it.removed }
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
            val error = runCatching { r.forward(r.prepare(daemon.id, "", command)) }.exceptionOrNull() as CommandException
            assertTrue(error.fromDaemon)
            assertEquals(code, error.code)
            assertFalse(error.unknown)
            assertEquals(1, calls)
        }
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
            val res =
                r.command(
                    "s1",
                    Command.newBuilder()
                        .setSend(
                            SendRequest.newBuilder()
                                .setSessionId("s1")
                                .setText("跑一下测试")
                                .setRequestId("q1"),
                        )
                        .build(),
                )
            assertEquals(1, res.send.queuePosition)
            val (h, cmd) = daemon.commands.poll(5, TimeUnit.SECONDS)!!
            assertEquals("s1", h.sessionId)
            assertEquals("跑一下测试", cmd.send.text)
            assertEquals(PROTOCOL_VERSION, cmd.protocolVersion)
            assertEquals(r.account.deviceId, h.sender)
            assertTrue(h.deadlineMs > System.currentTimeMillis())

            // The daemon's own error comes back as it is.
            try {
                r.command(
                    "s1",
                    Command.newBuilder()
                        .setCompact(
                            io.github.lewin671.remoteagent.proto.CompactRequest.newBuilder()
                                .setSessionId("s1"),
                        )
                        .build(),
                )
                fail("expected an error")
            } catch (e: CommandException) {
                assertEquals("unimplemented", e.code)
            }

            daemon.disconnect()
            r.awaitState("offline machine") { it.machines[daemon.id]?.online == false }
            try {
                r.command(
                    "s1",
                    Command.newBuilder()
                        .setInterrupt(
                            io.github.lewin671.remoteagent.proto.InterruptRequest.newBuilder()
                                .setSessionId("s1"),
                        )
                        .build(),
                )
                fail("expected unavailable")
            } catch (e: CommandException) {
                assertEquals(CommandException.UNAVAILABLE, e.code)
            }
        }

    private suspend fun refused(protocol: Int, prior: Boolean = false): CommandException {
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
            val command=Command.newBuilder().setInterrupt(io.github.lewin671.remoteagent.proto.InterruptRequest.newBuilder().setSessionId("s1")).build()
            r.forward(r.prepare(daemon.id,"s1",command),retryOfUnknown=prior)
            throw AssertionError("expected a version error")
        } catch (e: CommandException) {
            // Nothing was sent.
            assertEquals(null, daemon.commands.poll(300, TimeUnit.MILLISECONDS))
            assertFalse(e.fromDaemon)
            return e
        }
    }

    @Test fun protocolRefusalCannotSettleAnEarlierAttempt() = runBlocking<Unit> {
        val error=refused(PROTOCOL_VERSION-1,prior=true)
        assertEquals(Outcome.UNKNOWN,error.outcome)
        assertTrue(error.unknown)
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
            val snap = frames.poll(5, TimeUnit.SECONDS)!!
            assertEquals(120, snap.snapshot.cols)
            assertEquals(" world", frames.poll(5, TimeUnit.SECONDS)!!.output.toStringUtf8())
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
            assertEquals("中文\r", daemon.termInput.poll(5, TimeUnit.SECONDS)!!.input.toStringUtf8())
            assertEquals(
                "\u001b[A",
                daemon.termInput.poll(5, TimeUnit.SECONDS)!!.input.toStringUtf8(),
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
        val env=io.github.lewin671.remoteagent.proto.Envelope.parseFrom(java.util.Base64.getDecoder().decode(snapshot.text("envelope")))
        assertFalse(env.signature.isEmpty);assertFalse(env.securityHeader.isEmpty)
        memory.putBlob(key,scheduleSnapshot("wrong-request",env))
        assertTrue(runCatching { r.cachedSchedules(daemon.account.deviceId) }.isFailure)
        memory.putBlob(key,scheduleSnapshot(snapshot.text("request"),env.toBuilder().setSignature(com.google.protobuf.ByteString.copyFrom(ByteArray(64))).build()))
        assertTrue(runCatching { r.cachedSchedules(daemon.account.deviceId) }.isFailure)
        memory.putBlob(key,stored)
        val cold=fixtureRemote(r.account,http,memory);closers+=AutoCloseable { cold.close() }
        assertArrayEquals(json,cold.cachedSchedules(daemon.account.deviceId))
        removeAsAuthority(daemon.phone,daemon.account.deviceId,http)
        r.syncSecurity()
        assertNull(memory.blob(key));assertNull(r.cachedSchedules(daemon.account.deviceId))
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
