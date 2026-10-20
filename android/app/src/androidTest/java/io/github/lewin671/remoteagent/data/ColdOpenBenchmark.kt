package io.github.lewin671.remoteagent.data

import android.os.Bundle
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.ResponseBody.Companion.toResponseBody
import okio.Buffer
import org.junit.Assert.*
import org.junit.Assume.assumeTrue
import org.junit.Test

/**
 * Controlled old/new path measurement: fake Connect API with 50 ms per call, real signed envelopes
 * and Android Vault/Keystore. Two shared RTTs model the WS upgrade and parallel session listing.
 * This measures first-page verification, not Compose layout or a real CLI round trip.
 */
class ColdOpenBenchmark {
    @Test fun newestPageVisible() = runBlocking {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val context = instrumentation.targetContext
        assumeTrue(context.packageName.endsWith(".securityaudit"))
        val vault = Vault(context)
        val signer = SigningKey(ByteArray(32) { 1 })
        val x = ByteArray(32) { 2 }
        val phone = Authority.newMember(signer, x, 2u)
        val secret = ByteArray(32) { 3 }
        val genesis = Authority.genesis(
            "a".repeat(32), ByteArray(32) { 4 }, phone,
            SigningKey(ByteArray(32) { 5 }).publicKey, secret, signer, 1_700_000_000_000,
        )
        val machineSigner = SigningKey(ByteArray(32) { 7 })
        val machine = Authority.newMember(machineSigner, ByteArray(32) { 8 }, 1u)
        val state = Authority.pair(genesis, machine, secret, secret, signer, 1_700_000_000_001)
        val record = DeviceSecurity.Record.fromChain(
            state.core.account, state.core.domain, DeviceSecurity.stateHash(genesis), phone.id, x,
            listOf(genesis, state).map { DeviceSecurity.stateBytes(it) }, listOf(null, null),
        )
        val account = Account("https://example.com", ByteArray(0), ByteArray(32) { 1 }, phone.id, record.pin.account)
        val seed = ByteArray(32) { 6 }
        val stored = (1..1100).map { seq ->
            val event = Event.newBuilder().setSeq(seq.toLong()).setSessionId("test-session")
                .setAgentMessage(AgentMessage.newBuilder().setText("Synthetic message $seq")).build()
            val header = DeviceSecurity.Header(
                record.pin.account, record.pin.domain, state.core.epoch,
                DeviceSecurity.stateHash(state), state.core.contentEpoch, machine.id,
                DeviceSecurity.Purpose.EVENT.kind, DeviceSecurity.Purpose.EVENT,
                machine.id, "test-session", "", seq.toULong(), 0uL, 0u,
            )
            StoredEvent.newBuilder().setSeq(seq.toLong()).setEnvelope(
                DeviceSecurity.envelope(DeviceSecurity.Wire(DeviceSecurity.form(secret, seed, header, event.toByteArray(), machineSigner)))
            ).build()
        }
        try {
            for (sample in 0..3) for (newestFirst in listOf(false, true)) {
                vault.clear()
                vault.initializeSecurity(record)
                vault.save(account, DeviceSecurity.NotifyRecord.fromRecord(record).bytes())
                vault.saveLoginToken(account, LoginToken("synthetic-token", System.currentTimeMillis() + 60000))
                vault.clearSecurityCache()
                var calls = 0
                val http = Api.defaultHttp().newBuilder().addInterceptor { chain ->
                    calls++
                    Thread.sleep(50)
                    val response: com.google.protobuf.MessageLite = when (chain.request().url.encodedPath.substringAfterLast('/')) {
                        "GetChallenge" -> GetChallengeResponse.getDefaultInstance()
                        "Login" -> LoginResponse.newBuilder().setToken("synthetic-token").setExpiresMs(System.currentTimeMillis() + 60000).build()
                        "GetEvents" -> {
                            val bytes = Buffer().also { chain.request().body!!.writeTo(it) }.readByteArray()
                            val after = GetEventsRequest.parseFrom(bytes).afterSeq
                            val page = stored.filter { it.seq > after }.take(500)
                            GetEventsResponse.newBuilder().addAllEvents(page)
                                .setMore((page.lastOrNull()?.seq ?: after) < 1100).build()
                        }
                        else -> error("unexpected fake RPC")
                    }
                    okhttp3.Response.Builder().request(chain.request()).protocol(okhttp3.Protocol.HTTP_1_1)
                        .code(200).message("OK").header("Content-Type", "application/proto").body(response.toByteArray().toResponseBody("application/proto".toMediaType())).build()
                }.build()
                val api = Api(account.server, http)
                val security = DeviceSecurityClient(vault, phone.id, signer, fetchStates = { error("no real network") })
                try {
                    val start = System.nanoTime()
                    val token = if (newestFirst) vault.loadLoginToken(account)!!.value else {
                        api.auth.getChallenge(GetChallengeRequest.getDefaultInstance()).value()
                        api.auth.login(LoginRequest.getDefaultInstance()).value().token
                    }
                    delay(100) // Shared WS + list RTTs, deliberately equal for both paths.
                    var after = if (newestFirst) 600L else 0L
                    var visible = 0
                    do {
                        val page = api.client.getEvents(GetEventsRequest.newBuilder().setSessionId("test-session")
                            .setAfterSeq(after).setLimit(500).build(), Api.headers(token)).value()
                        for (event in page.eventsList) {
                            val opened = Event.parseFrom(security.openHistory(event.envelope, seed).second)
                            assertEquals(event.seq, opened.seq)
                        }
                        after = page.eventsList.last().seq
                        visible += page.eventsCount
                    } while (after < 1100)
                    val elapsed = (System.nanoTime() - start) / 1_000_000
                    assertEquals(if (newestFirst) 500 else 1100, visible)
                    assertEquals(if (newestFirst) 1 else 5, calls)
                    val result = "newestFirst=$newestFirst sample=$sample warmup=${sample == 0} newestVerifiedMs=$elapsed rpcCalls=$calls sharedModeledRtts=2 visible=$visible"
                    instrumentation.sendStatus(0, Bundle().apply { putString("stream", "\nColdOpenBenchmark $result\n") })
                    android.util.Log.i("ColdOpenBenchmark", result)
                } finally { security.close() }
            }
        } finally { vault.clear() }
    }
}
