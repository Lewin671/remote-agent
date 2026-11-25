package io.github.lewin671.remoteagent.data

import android.os.Bundle
import androidx.test.platform.app.InstrumentationRegistry
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.LoginToken
import io.github.lewin671.remoteagent.core.MemorySecurityStoreV4
import io.github.lewin671.remoteagent.core.executeRelayRpcUnary
import io.github.lewin671.remoteagent.core.proto.RelayRpcMethod
import io.github.lewin671.remoteagent.core.proto.SecurityStorageEffect
import io.github.lewin671.remoteagent.core.proto.SecurityStorageOp
import io.github.lewin671.remoteagent.core.proto.SecurityStorageWrite
import io.github.lewin671.remoteagent.core.proto.SecurityStoreBucket
import io.github.lewin671.remoteagent.proto.AgentMessage
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.GetChallengeRequest
import io.github.lewin671.remoteagent.proto.GetEventsRequest
import io.github.lewin671.remoteagent.proto.GetEventsResponse
import io.github.lewin671.remoteagent.proto.LoginRequest
import io.github.lewin671.remoteagent.proto.LoginResponse
import io.github.lewin671.remoteagent.proto.StoredEvent
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.ResponseBody.Companion.toResponseBody
import okio.Buffer
import org.junit.Assert.assertEquals
import org.junit.Assume.assumeTrue
import org.junit.Test

/**
 * Controlled old/new path measurement: fake Connect API with 50 ms per call, real signed envelopes
 * and Android Vault/Keystore v4 store. Two shared RTTs model the WS upgrade and parallel session listing.
 * This measures first-page verification, not Compose layout or a real CLI round trip.
 */
class ColdOpenBenchmark {
    @Test fun newestPageVisible() = runBlocking {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val context = instrumentation.targetContext
        assumeTrue(context.packageName.endsWith(".securityaudit"))
        val vault = Vault(context)
        val machineStore = MemorySecurityStoreV4().apply {
            seedTestRecord(
                edPrivate = BenchmarkVectors.machineSeed,
                edPublic = BenchmarkVectors.machineEdPub,
                xPrivate = BenchmarkVectors.machineX,
                xPublic = io.github.lewin671.remoteagent.corebind.Corebind.x25519PublicKey(BenchmarkVectors.machineX),
                recordBytes = BenchmarkVectors.machineRecordBytes,
            )
        }
        val account = Account(BenchmarkVectors.SERVER, BenchmarkVectors.phoneId, BenchmarkVectors.ACCOUNT_ID)
        val seed = ByteArray(32) { 6 }
        val stored = BenchmarkCoreSecurity(
            machineStore,
            BenchmarkVectors.ACCOUNT_ID,
            BenchmarkVectors.machineId,
            BenchmarkVectors.SERVER,
            BenchmarkVectors.nowMs + 1,
        ).use { sealer ->
            (1..1100).map { seq ->
                val event = Event.newBuilder().setSeq(seq.toLong()).setSessionId("test-session")
                    .setAgentMessage(AgentMessage.newBuilder().setText("Synthetic message $seq")).build()
                StoredEvent.newBuilder().setSeq(seq.toLong()).setEnvelope(
                    sealer.sealEvent("test-session", seq.toLong(), seed, event.toByteArray()),
                ).build()
            }
        }
        try {
            for (sample in 0..3) for (newestFirst in listOf(false, true)) {
                vault.clear()
                val seedRes = vault.executeStorage(
                    SecurityStorageEffect.newBuilder()
                        .setOpId(1L)
                        .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_BATCH_COMMIT)
                        .addBatch(
                            SecurityStorageWrite.newBuilder()
                                .setBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD)
                                .setKey("record")
                                .setCreateOnly(true)
                                .setNextRevision("r1")
                                .setData(ByteString.copyFrom(BenchmarkVectors.phoneRecordBytes)),
                        )
                        .addBatch(
                            SecurityStorageWrite.newBuilder()
                                .setBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD)
                                .setKey("notify")
                                .setCreateOnly(true)
                                .setNextRevision("r1")
                                .setData(ByteString.copyFrom(BenchmarkVectors.phoneNotifyBytes)),
                        )
                        .build(),
                )
                assertEquals("", seedRes.error)
                vault.saveLoginToken(account.server, account.deviceId, LoginToken("synthetic-token", System.currentTimeMillis() + 60000))
                vault.clearMemoryCache()
                var calls = 0
                val http = Api.defaultHttp().newBuilder().addInterceptor { chain ->
                    calls++
                    Thread.sleep(50)
                    val response: com.google.protobuf.MessageLite = when (chain.request().url.encodedPath.substringAfterLast('/')) {
                        "GetChallenge" -> io.github.lewin671.remoteagent.proto.GetChallengeResponse.getDefaultInstance()
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
                        .code(200).message("OK").header("Content-Type", "application/proto")
                        .body(response.toByteArray().toResponseBody("application/proto".toMediaType())).build()
                }.build()
                val security = BenchmarkCoreSecurity(
                    vault,
                    BenchmarkVectors.ACCOUNT_ID,
                    BenchmarkVectors.phoneId,
                    BenchmarkVectors.SERVER,
                    BenchmarkVectors.nowMs + 1,
                )
                try {
                    val start = System.nanoTime()
                    val token = if (newestFirst) vault.loadLoginToken(account.server, account.deviceId)!!.token else {
                        val chalRes = executeRelayRpcUnary(
                            http = http,
                            method = RelayRpcMethod.RELAY_RPC_METHOD_GET_CHALLENGE,
                            server = account.server,
                            requestBytes = GetChallengeRequest.getDefaultInstance().toByteArray(),
                        )
                        assertEquals(0, chalRes.connectCode)
                        val loginRes = executeRelayRpcUnary(
                            http = http,
                            method = RelayRpcMethod.RELAY_RPC_METHOD_LOGIN,
                            server = account.server,
                            requestBytes = LoginRequest.getDefaultInstance().toByteArray(),
                        )
                        assertEquals(0, loginRes.connectCode)
                        LoginResponse.parseFrom(loginRes.responseBytes).token
                    }
                    delay(100) // Shared WS + list RTTs, deliberately equal for both paths.
                    var after = if (newestFirst) 600L else 0L
                    var visible = 0
                    do {
                        val pageRes = executeRelayRpcUnary(
                            http = http,
                            method = RelayRpcMethod.RELAY_RPC_METHOD_GET_EVENTS,
                            server = account.server,
                            authToken = token,
                            requestBytes = GetEventsRequest.newBuilder().setSessionId("test-session")
                                .setAfterSeq(after).setLimit(500).build().toByteArray(),
                        )
                        assertEquals(0, pageRes.connectCode)
                        val page = GetEventsResponse.parseFrom(pageRes.responseBytes)
                        for (event in page.eventsList) {
                            val opened = Event.parseFrom(security.openHistory(event.envelope, seed))
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
                } finally {
                    security.close()
                }
            }
        } finally {
            vault.clear()
        }
    }
}
