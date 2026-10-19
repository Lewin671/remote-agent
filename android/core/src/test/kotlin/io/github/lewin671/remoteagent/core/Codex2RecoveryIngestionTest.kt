package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import java.io.File
import java.nio.file.Files
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.*
import org.junit.Test

class Codex2RecoveryIngestionTest {
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
            val r = fixtureRemote(account, http)
            remote = r
            if (damage == null) {
                r.start()
                withTimeout(5000) {
                    r.state.first { it.conn == ConnState.ONLINE && it.sessions["s"]?.info != null }
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
                prepareIngestion(r, machine.deviceId, d.sessionKeys.getValue("s"))
                invokeIngestion(r, stored)
                val failed = eventSnapshot(r)
                assertNotNull(failed.error)
                assertEquals(if (damage == "current-cipher") 1 else 0, failed.events.size)
                assertTrue(failed.verifiedEvents.isEmpty())
                assertEquals(if (damage == "current-cipher") 1 else 0, failed.unavailableCount)
                return
            }
            val events = r.events("s")
            run {
                val suffix = withTimeout(5000) { events.first { it.events.size == 2 } }
                assertEquals(1, suffix.unavailableCount)
                assertEquals(Event.KindCase.KIND_NOT_SET, suffix.events[0].kindCase)
                assertEquals(listOf(2L), suffix.verifiedEvents.map { it.seq })
                d.upload("s", message("live message"))
                val live = withTimeout(5000) { events.first { it.events.size == 3 } }
                assertEquals("live message", live.events[2].agentMessage.text)
                assertNull(live.error)
            }
        } finally {
            remote?.close()
            daemon?.close()
            server.close()
        }
    }

    @Suppress("UNCHECKED_CAST")
    private fun <T> field(remote: Remote, name: String): T =
        Remote::class.java.getDeclaredField(name).apply { isAccessible = true }.get(remote) as T

    private fun prepareIngestion(remote: Remote, machine: String, seed: ByteArray) {
        field<MutableMap<String, SessionView>>(remote, "sessions")["s"] = SessionView("s", machine)
        field<MutableMap<String, ByteArray>>(remote, "sessionKeys")["s"] = seed
        val type = Remote::class.java.declaredClasses.first { it.simpleName == "Buffer" }
        val buffer = type.getDeclaredConstructor().apply { isAccessible = true }.newInstance()
        type
            .getDeclaredField("usedAt")
            .apply { isAccessible = true }
            .setLong(buffer, System.currentTimeMillis())
        field<MutableMap<String, Any>>(remote, "buffers")["s"] = buffer
    }

    private suspend fun invokeIngestion(remote: Remote, stored: List<StoredEvent>): Unit =
        kotlin.coroutines.intrinsics.suspendCoroutineUninterceptedOrReturn { continuation ->
            val method =
                Remote::class
                    .java
                    .declaredMethods
                    .first { it.name == "ingest" }
                    .apply { isAccessible = true }
            try {
                method.invoke(remote, "s", stored, false, false, false, continuation)
            } catch (e: java.lang.reflect.InvocationTargetException) {
                throw e.targetException
            }
        }

    private fun eventSnapshot(remote: Remote) =
        field<MutableMap<String, kotlinx.coroutines.flow.MutableStateFlow<SessionEvents>>>(
                remote,
                "eventFlows",
            )
            .getValue("s")
            .value

    private fun message(text: String) =
        Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText(text))
}
