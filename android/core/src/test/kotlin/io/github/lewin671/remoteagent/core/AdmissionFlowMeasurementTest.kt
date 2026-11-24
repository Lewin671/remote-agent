package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.*
import java.io.File
import java.net.URI
import java.util.Base64
import kotlin.math.ceil
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.*
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody.Companion.toRequestBody
import org.junit.Assert.*
import org.junit.Assume.assumeTrue
import org.junit.Test

class AdmissionFlowMeasurementTest {
    @get:org.junit.Rule val hangGuard = hangGuard()

    @Test
    fun fullSnapshotApprovalUnderDaemonSaturation() = runBlocking<Unit> {
        val path = System.getenv("RA_1B_FLOW_ROOT")
        assumeTrue(path != null)
        val root = File(path!!)
        check(root.canonicalPath.startsWith("/private/tmp/") || root.canonicalPath.startsWith("/tmp/"))
        val config = Json.parseToJsonElement(File(root, "bootstrap.json").readText()).jsonObject
        fun text(key: String) = config.getValue(key).jsonPrimitive.content
        fun bytes(value: JsonElement) = Base64.getDecoder().decode(value.jsonPrimitive.content)
        check(URI(text("server")).host == "127.0.0.1")
        val r = config.getValue("record").jsonObject
        val edSeed = bytes(config.getValue("seed"))
        val xPriv = bytes(r.getValue("x"))
        val memberId = r.getValue("member").jsonPrimitive.content
        val accountId = r.getValue("account").jsonPrimitive.content
        val recordBytes = encodeV4RecordBytes(
            account = accountId,
            domain = bytes(r.getValue("domain")),
            genesis = bytes(r.getValue("genesis")),
            memberId = memberId,
            server = text("server"),
            states = r.getValue("states").jsonArray.map(::bytes),
            proofs = r.getValue("proofs").jsonArray.map { if (it is JsonNull) null else bytes(it) },
            lease = bytes(r.getValue("lease")),
        )
        val store = MemorySecurityStoreV4().apply {
            seedTestRecord(
                edPrivate = edSeed,
                edPublic = Corebind.ed25519PublicKey(edSeed),
                xPrivate = xPriv,
                xPublic = Corebind.x25519PublicKey(xPriv),
                recordBytes = recordBytes,
            )
        }
        val http = Api.defaultHttp()
        val account = Account(text("server"), memberId, accountId)
        val remote = Remote(account, http, securityStore = store)
        fun control(action: String): JsonObject {
            val body = buildJsonObject { put("action", action); put("client", "android") }.toString()
            val req = okhttp3.Request.Builder().url(text("control")).header("X-1b-Measure", text("controlToken"))
                .post(body.toRequestBody("application/json".toMediaType())).build()
            return http.newCall(req).execute().use { check(it.isSuccessful); Json.parseToJsonElement(it.body.string()).jsonObject }
        }
        val sid = "flow-android"
        fun approval(i: Int) = Command.newBuilder()
            .setResolveApproval(ResolveApprovalRequest.newBuilder().setSessionId(sid).setApprovalId("$sid-approval-${i.toString().padStart(3, '0')}").setAllow(true))
            .build()
        var loaded = false
        var follow: Job? = null
        try {
            control("reset")
            remote.start()
            withTimeout(10000) { remote.state.first { it.conn == ConnState.ONLINE && it.sessions[sid] != null } }
            val feed = remote.watchSession(sid)
            follow = launch(start = CoroutineStart.UNDISPATCHED) { feed.collect {} }
            withTimeout(10000) { feed.first { it.header.title.literal.startsWith("owner-read-") } }
            val mid = remote.state.value.sessions[sid]?.machineId.orEmpty()
            repeat(20) { remote.coreHost.runMachineCommand(mid, sid, approval(it)) }
            val load = control("start"); loaded = true
            assertEquals(4, load.getValue("normal").jsonPrimitive.int)
            assertEquals(2, load.getValue("bulk").jsonPrimitive.int)
            val timings = ArrayList<Double>()
            var completed = 0
            var failed = 0
            for (i in 20 until 120) {
                val start = System.nanoTime()
                try { withTimeout(1500) { remote.coreHost.runMachineCommand(mid, sid, approval(i)) }; completed++ } catch (e: Exception) { failed++ }
                timings.add((System.nanoTime() - start) / 1000000.0)
                if (failed > 0) break
            }
            val counters = control("stop"); loaded = false; timings.sort()
            fun q(p: Double) = timings[(ceil(timings.size * p).toInt() - 1).coerceAtLeast(0)]
            val report = buildJsonObject {
                put("client", "android"); put("attempts", timings.size); put("warmup", 20)
                put("completed", completed); put("failed", failed)
                put("completed_fraction", completed.toDouble() / timings.size)
                put("full_flow_ms", buildJsonObject { put("p50", q(.5)); put("p95", q(.95)); put("p99", q(.99)) })
                for ((k, v) in counters) put(k, v)
            }
            File(root, "android-flow.json").writeText(report.toString())
            assertEquals(100, completed); assertEquals(0, failed)
            assertEquals(4, counters.getValue("normal").jsonPrimitive.int)
            assertEquals(2, counters.getValue("bulk").jsonPrimitive.int)
            assertEquals(1, counters.getValue("input").jsonPrimitive.int)
            assertTrue(counters.getValue("input_overdue").jsonPrimitive.boolean)
            assertEquals(4, counters.getValue("flood_workers_before_release").jsonPrimitive.int)
            assertTrue(counters.getValue("flood_calls").jsonPrimitive.int > 0)
            assertTrue(counters.getValue("snapshot_calls").jsonPrimitive.int >= 100)
            assertEquals(100, counters.getValue("approval_calls").jsonPrimitive.int)
            assertTrue("full flow p99=${q(.99)} ms", q(.99) <= 100)
        } finally {
            try { if (loaded) control("stop") } finally {
                follow?.cancelAndJoin()
                remote.close()
                http.dispatcher.executorService.shutdown()
                http.connectionPool.evictAll()
            }
        }
    }
}
