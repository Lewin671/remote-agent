package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.ResponseBody.Companion.toResponseBody
import java.io.File
import java.nio.file.Files
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

class Codex2RecoveryIngestionTest {
    @get:org.junit.Rule val hangGuard = hangGuard()
    @Test
    fun unavailablePrefixDoesNotBlockReadableSuffixOrLiveEvents() =
        runBlocking<Unit> { scenario(null) }

    @Test
    fun forgedOldOriginIsNotClassifiedAsUnavailable() = runBlocking<Unit> { scenario("signature") }

    @Test fun changedOldRouteIsNotSkipped() = runBlocking<Unit> { scenario("route") }

    @Test fun currentCipherFailureIsNotSkipped() = runBlocking<Unit> { scenario("current-cipher") }

    private suspend fun scenario(damage: String?) {
        val dir = Files.createTempDirectory("ra-ingestion-recovery").toFile()
        val server = TestServer(dir)
        val http = Api.defaultHttp()
        val recoverySeed = ByteArray(32) { 0x41 }
        var daemon: FakeDaemon? = null
        var remote: Remote? = null
        try {
            val phone =
                newAuthorityAccount(
                    randomBytes(32),
                    server.url,
                    http,
                    server.setupToken,
                    recoverySeed,
                )
            val machine = approvedClient(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
            val d = FakeDaemon(machine, http, phone)
            daemon = d
            d.login()
            d.putMachine("synthetic-machine")
            val info = Session.newBuilder().setStatus(SessionStatus.SESSION_STATUS_IDLE).build()
            d.putSession("s", info)
            d.upload("s", message("old history"))
            val chain =
                Recovery.context(server.url, http, SigningKey(recoverySeed).publicKey, recoverySeed)
            val submitted =
                Recovery.submit(
                    server.url,
                    http,
                    recoverySeed,
                    randomBytes(32),
                    chain.pin.genesis.copyOf(10),
                    persist = {},
                )
            check(
                ProcessBuilder(
                        "sqlite3",
                        File(dir, "ra.db").absolutePath,
                        "UPDATE security_recovery SET ready_ms=0;",
                    )
                    .start()
                    .waitFor() == 0,
            )
            val (account, record) =
                Recovery.complete(
                    server.url,
                    http,
                    submitted.recoveryId,
                    submitted.accountId,
                    submitted.deviceSeed,
                    submitted.xPrivate,
                    submitted.stateHash,
                )
            val stores = TestSecurity.get(server.url).stores
            // Publish the recovered head's independent certificate before any
            // device produces new content, as the foreground authority does.
            val certificate =
                HeadLease.sign(
                    record.head,
                    SigningKey(account.deviceSeed),
                    System.currentTimeMillis(),
                )
            val authorityApi = Api(server.url, http)
            authorityApi.client
                .putSecurityState(
                    PutSecurityStateRequest.newBuilder()
                        .setLease(com.google.protobuf.ByteString.copyFrom(certificate.bytes()))
                        .build(),
                    Api.headers(login(authorityApi, account)),
                )
                .value()
            stores[account.deviceId] =
                TestSecurityStore(record.installed(emptyList(), emptyList(), certificate))
            val old = stores.getValue(machine.deviceId).readSecurity()
            val newStates = record.chain.filter { it.core.epoch > old.head.core.epoch }
            stores
                .getValue(machine.deviceId)
                .installSecurity(
                    DeviceSecurity.stateHash(old.head),
                    newStates,
                    newStates.map { record.proofs[it.core.epoch] },
                    certificate,
                )
            d.putMachine("synthetic-machine")
            d.putSession("s", info)
            d.upload("s", message("readable suffix"))
            d.connect()
            if (damage == "signature") {
                val api = Api(server.url, http)
                val page =
                    api.client
                        .getEvents(
                            GetEventsRequest.newBuilder().setSessionId("s").setLimit(500).build(),
                            Api.headers(login(api, account)),
                        )
                        .value()
                val forged = page.eventsList.first().envelope.toBuilder()
                val signature = forged.signature.toByteArray()
                signature[0] = (signature[0].toInt() xor 1).toByte()
                forged.signature = com.google.protobuf.ByteString.copyFrom(signature)
                val sdk =
                    DeviceSecurityClient(
                        stores.getValue(account.deviceId),
                        account.deviceId,
                        SigningKey(account.deviceSeed),
                        fetchStates = { error("no fetch needed") },
                    )
                val failure = runCatching {
                    sdk.openHistory(forged.build(), d.sessionKeys.getValue("s"))
                }
                    .exceptionOrNull()
                assertTrue(
                    "invalid origin masked as key unavailability: $failure",
                    failure is DeviceSecurity.Failure && failure.code == "untrusted",
                )
            }
            if (damage == null) {
                val r = fixtureRemote(account, http)
                remote = r
                r.start()
                r.state.first { it.conn == ConnState.ONLINE && it.sessions["s"]?.info != null }
                val feed = r.watchSession("s")
                run {
                    val suffix = feed.first { it.timeline.itemsCount == 1 }
                    assertEquals(1, suffix.timeline.unavailableCount)
                    assertEquals("readable suffix", suffix.timeline.itemsList[0].agentMessage.markdown)
                    d.upload("s", message("live message"))
                    val live = feed.first { it.timeline.itemsCount == 2 }
                    assertEquals("live message", live.timeline.itemsList[1].agentMessage.markdown)
                    assertFalse(live.timeline.hasError())
                }
            } else {
                val api = Api(server.url, http)
                val page =
                    api.client
                        .getEvents(
                            GetEventsRequest.newBuilder().setSessionId("s").setLimit(500).build(),
                            Api.headers(login(api, account)),
                        )
                        .value()
                val stored = page.eventsList.toMutableList()
                val index = if (damage == "current-cipher") 1 else 0
                val event = stored[index].toBuilder()
                val env = event.envelope.toBuilder()
                if (damage == "route") event.seq = 2
                else if (damage == "signature") {
                    val bytes = env.signature.toByteArray()
                    bytes[0] = (bytes[0].toInt() xor 1).toByte()
                    env.signature = com.google.protobuf.ByteString.copyFrom(bytes)
                } else {
                    val bytes = env.ciphertext.toByteArray()
                    bytes[0] = (bytes[0].toInt() xor 1).toByte()
                    env.ciphertext = com.google.protobuf.ByteString.copyFrom(bytes)
                }
                event.envelope = env.build()
                stored[index] = event.build()
                val damagedResponse = GetEventsResponse.newBuilder().addAllEvents(stored).setMore(false).build().toByteArray()
                val damagedHttp = http.newBuilder().addInterceptor { chain ->
                    if (chain.request().url.encodedPath.endsWith("/GetEvents")) {
                        okhttp3.Response.Builder()
                            .request(chain.request())
                            .protocol(okhttp3.Protocol.HTTP_1_1)
                            .code(200)
                            .message("OK")
                            .addHeader("content-type", "application/proto")
                            .body(damagedResponse.toResponseBody("application/proto".toMediaType()))
                            .build()
                    } else {
                        chain.proceed(chain.request())
                    }
                }.build()
                val r = fixtureRemote(account, damagedHttp)
                remote = r
                val feed = r.watchSession("s")
                r.start()
                val failed = feed.first { it.timeline.hasError() }
                assertTrue(failed.timeline.hasError())
                assertEquals(0, failed.timeline.itemsCount)
                assertEquals(if (damage == "current-cipher") 1 else 0, failed.timeline.unavailableCount)
                return
            }
        } finally {
            remote?.close()
            daemon?.close()
            server.close()
        }
    }

    private fun message(text: String) =
        Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText(text))
}
