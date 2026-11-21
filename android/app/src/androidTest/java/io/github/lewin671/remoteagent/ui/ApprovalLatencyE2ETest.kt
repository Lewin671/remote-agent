package io.github.lewin671.remoteagent.ui

import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onLast
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextReplacement
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.MainActivity
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.core.proto.DirectPathChoice
import io.github.lewin671.remoteagent.core.proto.DirectRouteState
import io.github.lewin671.remoteagent.core.proto.SessionDockKind
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.CreateSessionRequest
import io.github.lewin671.remoteagent.proto.GetLiveSnapshotRequest
import io.github.lewin671.remoteagent.proto.ResolveApprovalRequest
import io.github.lewin671.remoteagent.transport.AndroidRtcFactory
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Rule
import org.junit.Test
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicLong
import kotlin.math.ceil

/**
 * Approval latency measurement for the transport doc's p99 figure. It reports, in one run on one host and one relay path,
 * the numbers that make the figure interpretable: the harness's own relay round trip (plain HTTP and one relay command),
 * then the full approval command (its fresh read of the latest state, then the resolve) over relay only, with the direct
 * path up and idle, and with the direct path saturated by bulk. It asserts nothing about the figure; it only reports.
 *
 * Needs the same isolated host as [DirectTransportE2ETest] (host.py); `rounds` (default 100) sets the sample count per case.
 */
class ApprovalLatencyE2ETest {
    @get:Rule val ui = createAndroidComposeRule<MainActivity>()

    private val tag = "ApprovalLat"

    private fun ctrlGet(base: String, path: String): JSONObject {
        val conn = (URL("$base$path").openConnection() as HttpURLConnection).apply {
            requestMethod = "GET"
            connectTimeout = 10_000
            readTimeout = 30_000
        }
        val text = conn.inputStream.bufferedReader().readText()
        check(conn.responseCode in 200..299) { "GET $path failed (${conn.responseCode}): $text" }
        return JSONObject(text)
    }

    private fun ctrlPost(base: String, path: String, body: JSONObject): JSONObject {
        val conn = (URL("$base$path").openConnection() as HttpURLConnection).apply {
            requestMethod = "POST"
            doOutput = true
            setRequestProperty("Content-Type", "application/json")
            connectTimeout = 10_000
            readTimeout = 90_000
        }
        conn.outputStream.use { it.write(body.toString().toByteArray()) }
        val stream = if (conn.responseCode in 200..299) conn.inputStream else conn.errorStream
        val text = stream.bufferedReader().readText()
        check(conn.responseCode in 200..299) { "POST $path failed (${conn.responseCode}): $text" }
        return JSONObject(text)
    }

    private fun waitText(text: String, timeout: Long = 30_000) {
        ui.waitUntil(timeout) { ui.onAllNodesWithText(text, substring = true).fetchSemanticsNodes().isNotEmpty() }
    }

    private fun pct(sorted: List<Double>, p: Double) = sorted[(ceil(sorted.size * p).toInt() - 1).coerceIn(0, sorted.lastIndex)]

    private fun stats(xs: List<Double>): JSONObject {
        val s = xs.sorted()
        return JSONObject().put("n", s.size).put("min", s.first()).put("p50", pct(s, 0.50)).put("p95", pct(s, 0.95))
            .put("p99", pct(s, 0.99)).put("max", s.last()).put("samples", JSONArray(xs))
    }

    @Test
    fun approvalLatency() {
        val args = InstrumentationRegistry.getArguments()
        val ctrlPort = args.getString("directCtrlPort").orEmpty()
        assumeTrue("requires isolated host orchestrator", ctrlPort.isNotEmpty())
        val rounds = args.getString("rounds")?.toIntOrNull() ?: 100
        val ctrlBase = "http://${args.getString("directCtrlHost") ?: "10.0.2.2"}:$ctrlPort"
        val info = ctrlGet(ctrlBase, "/info")
        val stunPort = info.getInt("stunPort")
        val stunHost = info.optString("stunHost", "10.0.2.2")
        val setupToken = info.getString("setupToken")
        val projectDir = info.getString("projectDir")
        val serverUrl = info.getString("serverUrl")

        RaApp.instance.testRtcFactory = AndroidRtcFactory(RaApp.instance)
        RaApp.instance.testStunUrls = listOf("stun:$stunHost:$stunPort")

        // Account and pairing, as in the full direct suite.
        ui.waitUntil(30_000) { RaApp.instance.remote.value != null || ui.onAllNodesWithText("创建账户").fetchSemanticsNodes().isNotEmpty() }
        if (RaApp.instance.remote.value == null) {
            ui.onNodeWithText("创建账户").performClick()
            ui.onAllNodes(hasSetTextAction())[0].performTextReplacement(serverUrl)
            ui.onAllNodes(hasSetTextAction())[1].performTextReplacement(setupToken)
            ui.onAllNodesWithText("创建账户").onLast().performScrollTo().performClick()
            waitText("我已保存，开始使用")
            ui.onNodeWithText("我已保存，开始使用").performScrollTo().performClick()
        }
        ui.waitUntil(30_000) { RaApp.instance.remote.value != null }
        val remote = requireNotNull(RaApp.instance.remote.value)
        ui.waitUntil(30_000) { ui.onAllNodes(androidx.compose.ui.test.hasContentDescription("设置")).fetchSemanticsNodes().isNotEmpty() }
        ui.onNodeWithContentDescription("设置").performClick()
        waitText("添加设备")
        ui.onNodeWithText("添加设备").performScrollTo().performClick()
        waitText("等待新设备")
        val rawCode = ui.onAllNodes(hasText("", substring = true))
            .fetchSemanticsNodes()
            .flatMap { it.config.getOrElse(androidx.compose.ui.semantics.SemanticsProperties.Text) { emptyList() } }
            .map { it.text }
            .first { Regex("[A-Z0-9]{4} [A-Z0-9]{4}").matches(it) }
            .replace(" ", "")
        ctrlPost(ctrlBase, "/pair", JSONObject().put("code", rawCode))
        waitText("允许加入", 60_000)
        ui.onNodeWithText("允许加入").performClick()
        waitText("设备已加入", 60_000)
        ui.onNodeWithText("完成").performClick()

        var machineId = ""
        runBlocking(Dispatchers.IO) {
            val deadline = System.currentTimeMillis() + 30_000
            while (System.currentTimeMillis() < deadline && machineId.isEmpty()) {
                val st = ctrlGet(ctrlBase, "/pair-status")
                check(st.isNull("error")) { "Pair failed on host: ${st.optString("error")}" }
                if (!st.isNull("machineId")) machineId = st.optString("machineId") else delay(150)
            }
        }
        check(machineId.isNotEmpty()) { "Timed out waiting for paired machineId" }
        // Keep the direct path out of the relay-only cases from the start.
        remote.setDirectEnabled(machineId, false)
        ui.waitUntil(60_000) { remote.state.value.machines[machineId]?.pathState?.reachable == true }

        val metrics = JSONObject().put("rounds", rounds)
        runBlocking(Dispatchers.IO) {
            val created = remote.machineCommand(
                machineId,
                Command.newBuilder().setCreateSession(
                    CreateSessionRequest.newBuilder().setCwd(projectDir).setAgent(Agent.AGENT_CLAUDE).setMode(ApprovalMode.APPROVAL_MODE_ASK),
                ).build(),
            )
            val sessionId = created.createSession.session.id
            val snapshotCmd = Command.newBuilder().setGetLiveSnapshot(GetLiveSnapshotRequest.newBuilder().setSessionId(sessionId)).build()

            suspend fun <T> retryNotSent(block: suspend () -> T): T {
                repeat(8) {
                    try {
                        return block()
                    } catch (e: CommandException) {
                        if (e.code != "network" && e.code != "unavailable") throw e
                        delay(1_000)
                    }
                }
                return block()
            }

            fun mark(what: String) = android.util.Log.i("DirectE2E", "marker $what")

            // The client learns of the new session asynchronously; wait until commands for it are accepted.
            run {
                val deadline = System.currentTimeMillis() + 20_000
                while (true) {
                    try {
                        remote.machineCommand(machineId, snapshotCmd)
                        break
                    } catch (e: CommandException) {
                        if (System.currentTimeMillis() > deadline) throw e
                        delay(250)
                    }
                }
            }

            // One approval: trigger it on the host, wait (untimed) until it is visible, then time the ResolveApproval command.
            suspend fun approvalRound(caseName: String, i: Int, extra: () -> String): Double {
                ctrlPost(ctrlBase, "/trigger-approval", JSONObject().put("sessionId", sessionId).put("command", "echo $caseName-$i"))
                var approvalId: String? = null
                val waitHook = System.currentTimeMillis() + 10_000
                while (approvalId == null && System.currentTimeMillis() < waitHook) {
                    val snap = remote.machineCommand(machineId, snapshotCmd).liveSnapshot
                    approvalId = snap.actionsList.firstOrNull { it.hasApprovalRequested() }?.approvalRequested?.approvalId
                    if (approvalId == null) delay(15)
                }
                val id = checkNotNull(approvalId) { "Hook approval $caseName-$i never appeared" }
                remote.openSession(sessionId)
                val evJob = launch { remote.watchSession(sessionId).collect { } }
                val liveVm = withTimeout(5_000) {
                    remote.watchSession(sessionId).first { vm ->
                        vm.timeline.loaded && vm.dock.hasItem && vm.dock.kind == SessionDockKind.SESSION_DOCK_KIND_APPROVAL && vm.dock.approval.approvalId == id
                    }
                }
                delay(30) // let the first loadEvents / live pollers settle before timing
                val observedSeq = liveVm.dock.observedSeq
                mark("t0 $caseName $i")
                val t0 = System.nanoTime()
                remote.machineCommand(
                    machineId,
                    Command.newBuilder().setResolveApproval(
                        ResolveApprovalRequest.newBuilder().setSessionId(sessionId).setApprovalId(id).setAllow(true).setObservedEventSeq(observedSeq),
                    ).build(),
                )
                val dt = (System.nanoTime() - t0) / 1_000_000.0
                mark("t1 $caseName $i")
                android.util.Log.i("DirectE2E", "approval case=$caseName i=$i dtMs=$dt ${extra()}")
                evJob.cancelAndJoin()
                return dt
            }

            // Harness relay latency, 1: a plain HTTP round trip to the relay proxy on a kept-alive connection (emulator, adb reverse, proxy, server).
            mark("case http")
            val httpMs = ArrayList<Double>()
            repeat(rounds + 5) { n ->
                val t0 = System.nanoTime()
                val c = URL("$serverUrl/healthz").openConnection() as HttpURLConnection
                c.inputStream.use { it.readBytes() }
                val dt = (System.nanoTime() - t0) / 1_000_000.0
                if (n >= 5) httpMs.add(dt)
                delay(20)
            }
            metrics.put("httpRttMs", stats(httpMs))

            // Control: scheduling jitter of this process with no relay and no app work. A coroutine sleeps 5 ms and hops to the
            // default dispatcher for a small hash, as the lease check and sealing do; the figure is the time beyond the 5 ms.
            mark("case jitter")
            val jitterMs = ArrayList<Double>()
            repeat(1000) {
                val t0 = System.nanoTime()
                kotlinx.coroutines.withContext(Dispatchers.Default) { java.security.MessageDigest.getInstance("SHA-256").digest(ByteArray(2048)) }
                delay(5)
                jitterMs.add((System.nanoTime() - t0) / 1_000_000.0 - 5.0)
            }
            metrics.put("jitterMs", stats(jitterMs))
            // The same hop after idle gaps, as between approval rounds: the time of the hop and a ~1 ms hash, not counting the gap.
            mark("case cold_hop")
            val coldMs = ArrayList<Double>()
            repeat(300) {
                delay(200)
                val t0 = System.nanoTime()
                kotlinx.coroutines.withContext(Dispatchers.Default) { java.security.MessageDigest.getInstance("SHA-256").digest(ByteArray(256 * 1024)) }
                coldMs.add((System.nanoTime() - t0) / 1_000_000.0)
            }
            metrics.put("coldHopMs", stats(coldMs))
            // Storage: a 4 KiB write flushed to the emulator's disk, which the client's security store does on its commands.
            mark("case fsync")
            val syncMs = ArrayList<Double>()
            val probe = java.io.File(RaApp.instance.filesDir, "fsync-probe.bin")
            repeat(300) {
                delay(200)
                val t0 = System.nanoTime()
                java.io.FileOutputStream(probe).use { out -> out.write(ByteArray(4096)); out.fd.sync() }
                syncMs.add((System.nanoTime() - t0) / 1_000_000.0)
            }
            probe.delete()
            metrics.put("fsyncMs", stats(syncMs))

            // Harness relay latency, 2: one relay command (GetLiveSnapshot, emulator to daemon and back), direct path off.
            mark("case relay_snapshot")
            repeat(5) { remote.machineCommand(machineId, snapshotCmd) }
            val snapMs = ArrayList<Double>()
            repeat(rounds) {
                val t0 = System.nanoTime()
                remote.machineCommand(machineId, snapshotCmd)
                snapMs.add((System.nanoTime() - t0) / 1_000_000.0)
                delay(20)
            }
            metrics.put("relaySnapshotMs", stats(snapMs))

            // Harness relay latency, 3: the cheapest relay command (GetMachineStatus), so that daemon work is as small as a command can be.
            mark("case relay_ping")
            val statusCmd = Command.newBuilder().setGetMachineStatus(io.github.lewin671.remoteagent.proto.GetMachineStatusRequest.getDefaultInstance()).build()
            repeat(5) { remote.machineCommand(machineId, statusCmd) }
            val pingMs = ArrayList<Double>()
            repeat(rounds) {
                val t0 = System.nanoTime()
                remote.machineCommand(machineId, statusCmd)
                pingMs.add((System.nanoTime() - t0) / 1_000_000.0)
                delay(20)
            }
            metrics.put("relayPingMs", stats(pingMs))

            // The full approval command over relay only (direct off).
            mark("case relay_approval")
            val relayApproval = (1..rounds).map { approvalRound("relay", it) { "path=${remote.filePath(machineId)}" } }
            metrics.put("approvalRelayOnlyMs", stats(relayApproval))

            // Direct path up, set up as the full suite does: 150 ms relay delay while the warm-up and bulk upgrade run, then removed.
            mark("case upgrade")
            ctrlPost(ctrlBase, "/set-relay-delay", JSONObject().put("ms", 150).put("resetCount", true))
            val connectStart = System.currentTimeMillis()
            remote.setDirectEnabled(machineId, true)
            ui.waitUntil(60_000) { remote.directRouteReport(machineId).state == DirectRouteState.DIRECT_ROUTE_STATE_CONNECTED }
            delay(700)
            ui.waitUntil(30_000) { remote.directRouteReport(machineId).readRoute == DirectPathChoice.DIRECT_PATH_CHOICE_DIRECT }
            val chunkSize = 768 * 1024
            val f1 = ctrlPost(ctrlBase, "/create-shared-file", JSONObject().put("sessionId", sessionId).put("sizeBytes", 24 * 1024 * 1024).put("seed", 11))
            val fid1 = f1.getString("fileId")
            for (warmIdx in 0 until 96) {
                if (remote.filePath(machineId) == DirectPathChoice.DIRECT_PATH_CHOICE_DIRECT) break
                retryNotSent { remote.getSharedFileChunk(sessionId, fid1, (warmIdx % 32).toLong() * chunkSize) }
            }
            assertEquals("bulk path must upgrade to direct", DirectPathChoice.DIRECT_PATH_CHOICE_DIRECT, remote.filePath(machineId))
            metrics.put("directReadyMs", System.currentTimeMillis() - connectStart)
            ctrlPost(ctrlBase, "/set-relay-delay", JSONObject().put("ms", 0).put("bulkMs", 350).put("resetCount", true))
            delay(3_000) // let the transport settle

            fun isReadDirect() = remote.directRouteReport(machineId).readRoute == DirectPathChoice.DIRECT_PATH_CHOICE_DIRECT
            // Direct path up, no bulk load.
            mark("case direct_idle")
            val idle = (1..rounds).map { approvalRound("idle", it) { "file=${remote.filePath(machineId)} readDirect=${isReadDirect()}" } }
            metrics.put("approvalDirectIdleMs", stats(idle))

            // Direct path saturated by one bulk worker (the full suite's load).
            mark("case saturated")
            val attempted = AtomicInteger(0)
            val succeeded = AtomicInteger(0)
            val bytes = AtomicLong(0)
            val saturating = AtomicBoolean(true)
            val satStart = System.nanoTime()
            val worker = launch {
                var idx = 0
                while (isActive && saturating.get()) {
                    val o = ((idx % 32).toLong()) * chunkSize
                    idx += 1
                    attempted.incrementAndGet()
                    try {
                        val res = retryNotSent { remote.getSharedFileChunk(sessionId, fid1, o) }
                        succeeded.incrementAndGet()
                        bytes.addAndGet(res.data.size().toLong())
                    } catch (c: CancellationException) {
                        throw c
                    } catch (_: Exception) {
                    }
                }
            }
            delay(300)
            val sat = (1..rounds).map {
                approvalRound("sat", it) { "file=${remote.filePath(machineId)} readDirect=${isReadDirect()} bulk=${succeeded.get()}/${attempted.get()}" }
            }
            saturating.set(false)
            worker.cancelAndJoin()
            val satSec = (System.nanoTime() - satStart) / 1e9
            metrics.put("approvalSaturatedMs", stats(sat))
            metrics.put("bulkAttempted", attempted.get())
            metrics.put("bulkSucceeded", succeeded.get())
            metrics.put("bulkMiBPerSec", bytes.get() / 1048576.0 / satSec)
            mark("case done")
            ctrlPost(ctrlBase, "/report-metrics", metrics)
            assertTrue("saturated bulk must have moved data", succeeded.get() > 0)
        }
    }
}
