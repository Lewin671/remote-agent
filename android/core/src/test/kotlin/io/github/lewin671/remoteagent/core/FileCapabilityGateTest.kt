package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.runBlocking
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.ResponseBody.Companion.toResponseBody
import org.junit.Assert.*
import org.junit.Test
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger

/** Real relay and signed fake-daemon replies; count actual Forward calls at the HTTP boundary. */
class FileCapabilityGateTest {
    private val send = Command.newBuilder().setSend(SendRequest.newBuilder().setSessionId("s").setRequestId("message").setText("keep text").addFileIds("original-file")).build()
    private val create = Command.newBuilder().setCreateSession(CreateSessionRequest.newBuilder().setPrompt("keep text").addFileIds("original-file")).build()
    private fun state(id: String, support: Boolean?) = RemoteState(machines = mapOf(id to MachineView(id, true, 0,
        MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).apply {
            if (support != null) setCapabilities(MachineCapabilities.newBuilder().setFileUpload(support))
        }.build())))

    @Test fun everyFileBearingCommandRefusesFalseMissingOrUnknownCapabilityWithoutSendingTextAlone() = runBlocking {
        val server = TestServer(); val http = Api.defaultHttp()
        val daemon = FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken)
        val r = fixtureRemote(approvedClient(daemon.phone, http), http)
        try {
            daemon.answer = { _, _ -> CommandResult.getDefaultInstance() }
            daemon.connect()
            for (cmd in listOf(send, create)) for (support in listOf(false, null)) {
                r.showForTest(state(daemon.id, support))
                val p = r.prepare(daemon.id, if (cmd.hasSend()) "s" else "", cmd)
                val e = runCatching { r.forward(p) }.exceptionOrNull()
                assertTrue("must reject locally: $e", e is CommandException && !e.unknown && !e.fromDaemon)
                assertNotNull("file-bearing operation must be blocked", e)
                assertTrue(e!!.message.orEmpty().contains("更新电脑上的 remote-agent"))
            }
            r.showForTest(RemoteState())
            val e = runCatching { r.forward(r.prepare(daemon.id, "s", send)) }.exceptionOrNull()
            assertNotNull("file-bearing operation must be blocked", e)
                assertTrue(e!!.message.orEmpty().contains("更新电脑上的 remote-agent"))
            assertTrue(daemon.commands.isEmpty())
            // Text and images remain valid on an older daemon.
            r.showForTest(state(daemon.id, null))
            r.forward(r.prepare(daemon.id, "s", send.toBuilder().setSend(send.send.toBuilder().clearFileIds().addImageIds("image")).build()))
            assertEquals(listOf("image"), daemon.commands.poll(5, TimeUnit.SECONDS)!!.second.send.imageIdsList)
        } finally { r.close(); daemon.close(); server.close() }
    }

    @Test fun signingCannotUseCapabilityThatDisappearedAfterTheInitialCheck() = runBlocking {
        val server = TestServer(); val http = Api.defaultHttp()
        val daemon = FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken)
        val account = approvedClient(daemon.phone, http)
        val store = TestSecurity.get(server.url).stores.getValue(account.deviceId)
        val armed = AtomicBoolean(false)
        lateinit var r: Remote
        val guarded = object : DeviceSecurity.Storage by store {
            override fun readSecurity(): DeviceSecurity.Record {
                if (armed.compareAndSet(true, false)) r.showForTest(state(daemon.id, false))
                return store.readSecurity()
            }
        }
        r = Remote(account, http, securityStorage = guarded)
        try {
            daemon.answer = { _, _ -> CommandResult.getDefaultInstance() }
            daemon.connect(); r.currentToken()
            for (cmd in listOf(send, create)) {
                r.showForTest(state(daemon.id, true))
                val p = r.prepare(daemon.id, if (cmd.hasSend()) "s" else "", cmd)
                armed.set(true)
                val e = runCatching { r.forward(p) }.exceptionOrNull()
                assertNotNull("file-bearing operation must be blocked", e)
                assertTrue(e!!.message.orEmpty().contains("更新电脑上的 remote-agent"))
                assertFalse((e as CommandException).unknown)
            }
            assertTrue(daemon.commands.isEmpty())
        } finally { r.close(); daemon.close(); server.close() }
    }

    @Test fun unknownSendAndCreateStayFrozenDuringDowngradeAndResumeTheExactRequest() = runBlocking {
        val server = TestServer(); val http = Api.defaultHttp()
        val daemon = FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken)
        val r = fixtureRemote(approvedClient(daemon.phone, http), http)
        try {
            daemon.answer = { _, _ -> CommandResult.getDefaultInstance() }
            daemon.connect()
            for (cmd in listOf(send, create)) {
                r.showForTest(state(daemon.id, true))
                val a = Attempt(); val p = a.next { r.prepare(daemon.id, if (cmd.hasSend()) "s" else "", cmd) }
                r.forward(p)
                val first = daemon.commands.poll(5, TimeUnit.SECONDS)!!
                a.settle(CommandException(CommandException.UNKNOWN, "lost reply"))
                r.showForTest(state(daemon.id, false))
                val e = runCatching { r.forward(p, retryOfUnknown = true) }.exceptionOrNull()
                a.settle(e)
                assertTrue(a.frozen); assertSame(p, a.pending)
                assertTrue(e is CommandException && e.unknown)
                assertNotNull("file-bearing operation must be blocked", e)
                assertTrue(e!!.message.orEmpty().contains("更新电脑上的 remote-agent"))
                assertTrue(daemon.commands.isEmpty())
                r.showForTest(state(daemon.id, true))
                r.forward(a.next { error("must retain identity") }, retryOfUnknown = true)
                val again = daemon.commands.poll(5, TimeUnit.SECONDS)!!
                assertEquals(first, again)
                assertEquals(p.deadlineMs, again.first.deadlineMs)
            }
        } finally { r.close(); daemon.close(); server.close() }
    }

    @Test fun automaticNetworkRetryRechecksCapabilityAndKeepsTheEarlierOutcomeUnknown() = runBlocking {
        val server = TestServer(); val http = Api.defaultHttp(); val calls = AtomicInteger()
        val daemon = FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken)
        lateinit var r: Remote
        val measured = http.newBuilder().addInterceptor { chain ->
            if (chain.request().url.encodedPath.endsWith("/Forward")) {
                calls.incrementAndGet()
                r.showForTest(state(daemon.id, false))
                okhttp3.Response.Builder().request(chain.request()).protocol(okhttp3.Protocol.HTTP_1_1)
                    .code(500).message("lost response")
                    .body("{\"code\":\"internal\",\"message\":\"lost reply\"}".toResponseBody("application/json".toMediaType())).build()
            } else chain.proceed(chain.request())
        }.build()
        r = fixtureRemote(approvedClient(daemon.phone, http), measured)
        try {
            for (cmd in listOf(send, create)) {
                calls.set(0); r.showForTest(state(daemon.id, true))
                val e = runCatching { r.forward(r.prepare(daemon.id, if (cmd.hasSend()) "s" else "", cmd)) }.exceptionOrNull()
                assertEquals(1, calls.get())
                assertTrue(e is CommandException && e.unknown)
                assertNotNull("file-bearing operation must be blocked", e)
                assertTrue(e!!.message.orEmpty().contains("更新电脑上的 remote-agent"))
            }
        } finally { r.close(); daemon.close(); server.close() }
    }
}
