package io.github.lewin671.remoteagent.ui

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onLast
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextReplacement
import androidx.compose.ui.test.printToString
import androidx.compose.ui.test.onRoot
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.MainActivity
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.proto.DirectPathChoice
import io.github.lewin671.remoteagent.core.proto.DirectRouteState
import io.github.lewin671.remoteagent.core.proto.SessionDockKind
import io.github.lewin671.remoteagent.core.transport.RtcDataChannel
import io.github.lewin671.remoteagent.core.transport.RtcDataChannelListener
import io.github.lewin671.remoteagent.core.transport.RtcFactory
import io.github.lewin671.remoteagent.core.transport.RtcIceCandidate
import io.github.lewin671.remoteagent.core.transport.RtcPeerConnection
import io.github.lewin671.remoteagent.core.transport.RtcPeerListener
import io.github.lewin671.remoteagent.core.transport.RtcPeerState
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.CreateSessionRequest
import io.github.lewin671.remoteagent.proto.GetLiveSnapshotRequest
import io.github.lewin671.remoteagent.proto.GetSettingsRequest
import io.github.lewin671.remoteagent.proto.ResolveApprovalRequest
import io.github.lewin671.remoteagent.transport.AndroidRtcFactory
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Rule
import org.junit.Test
import java.io.ByteArrayOutputStream
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest
import java.util.Collections
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger
import kotlin.math.ceil

/** Test-only wrapper around real [AndroidRtcFactory] to trigger mid-stream drop and UDP blackhole negative cases. */
private class E2ERtcWrapper(private val delegate: RtcFactory) : RtcFactory {
    @Volatile var blockUdp = false
    private val active = Collections.synchronizedList(ArrayList<Pair<RtcPeerConnection, MutableList<RtcDataChannel>>>())

    fun dropActiveConnections() {
        val snapshot = synchronized(active) { active.toList() }
        for ((pc, dcs) in snapshot) {
            for (dc in dcs) runCatching { dc.close() }
            runCatching { pc.close() }
        }
    }

    override fun prewarm() = delegate.prewarm()

    override fun create(stunUrls: List<String>, listener: RtcPeerListener): RtcPeerConnection {
        val channels = Collections.synchronizedList(ArrayList<RtcDataChannel>())
        lateinit var wrapperPc: RtcPeerConnection
        val realPc = delegate.create(
            stunUrls,
            object : RtcPeerListener {
                override fun onIceCandidate(candidate: RtcIceCandidate) {
                    if (blockUdp) {
                        val parts = candidate.candidate.split(" ").toMutableList()
                        if (parts.size >= 6) {
                            parts[4] = "192.0.2.9"
                            parts[5] = "9"
                        }
                        listener.onIceCandidate(candidate.copy(candidate = parts.joinToString(" ")))
                    } else {
                        listener.onIceCandidate(candidate)
                    }
                }

                override fun onIceGatheringComplete() = listener.onIceGatheringComplete()
                override fun onConnectionStateChange(state: RtcPeerState) = listener.onConnectionStateChange(state)
                override fun onUnexpectedDataChannel() = listener.onUnexpectedDataChannel()
            },
        )
        wrapperPc = object : RtcPeerConnection {
            override fun createDataChannel(
                label: String,
                id: Int,
                ordered: Boolean,
                maxRetransmits: Int?,
                listener: RtcDataChannelListener,
            ): RtcDataChannel = realPc.createDataChannel(label, id, ordered, maxRetransmits, listener).also { channels.add(it) }

            override suspend fun createOffer(iceRestart: Boolean) = realPc.createOffer(iceRestart)
            override suspend fun createAnswer() = realPc.createAnswer()
            override suspend fun setLocalDescription(offer: Boolean, sdp: String) = realPc.setLocalDescription(offer, sdp)
            override suspend fun setRemoteDescription(offer: Boolean, sdp: String) = realPc.setRemoteDescription(offer, sdp)
            override suspend fun addIceCandidate(candidate: String, sdpMid: String, sdpMLineIndex: Int, usernameFragment: String) {
                if (blockUdp) return
                realPc.addIceCandidate(candidate, sdpMid, sdpMLineIndex, usernameFragment)
            }

            override fun maxMessageSize(): Int? = realPc.maxMessageSize()

            override fun close() {
                synchronized(active) { active.removeIf { it.first === wrapperPc } }
                realPc.close()
            }
        }
        synchronized(active) { active.add(wrapperPc to channels) }
        return wrapperPc
    }
}

/** Opt-in isolated emulator E2E against host Go server + STUN + daemon. */
class DirectTransportE2ETest {
    @get:Rule val ui = createAndroidComposeRule<MainActivity>()

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

    private fun sha256Hex(bytes: ByteArray): String =
        MessageDigest.getInstance("SHA-256").digest(bytes).joinToString("") { "%02x".format(it) }

    private fun waitText(text: String, timeout: Long = 30_000) {
        try {
            ui.waitUntil(timeout) { ui.onAllNodesWithText(text, substring = true).fetchSemanticsNodes().isNotEmpty() }
        } catch (e: Throwable) {
            val tree = runCatching { ui.onRoot().printToString() }.getOrElse { it.toString() }
            throw AssertionError("Timed out waiting for '$text'. Semantics tree:\n$tree", e)
        }
    }

    /** The test bench reaches the host through adb reverse, which an adb restart on this Mac drops for a second or two: a command refused as not sent is tried again. Not used on timed operations. */
    private suspend fun <T> retryNotSent(block: suspend () -> T): T {
        repeat(8) {
            try {
                return block()
            } catch (e: io.github.lewin671.remoteagent.core.CommandException) {
                if (e.code != "network" && e.code != "unavailable") throw e
                delay(1_000)
            }
        }
        return block()
    }

    @Test
    fun fullDirectPathE2ESuite() {
        val args = InstrumentationRegistry.getArguments()
        val ctrlPort = args.getString("directCtrlPort").orEmpty()
        assumeTrue("requires isolated host orchestrator", ctrlPort.isNotEmpty())
        val ctrlBase = "http://${args.getString("directCtrlHost") ?: "10.0.2.2"}:$ctrlPort"
        val info = ctrlGet(ctrlBase, "/info")
        val proxyPort = info.getInt("proxyPort")
        val stunPort = info.getInt("stunPort")
        val stunHost = info.optString("stunHost", "10.0.2.2")
        val setupToken = info.getString("setupToken")
        val projectDir = info.getString("projectDir")
        val serverUrl = info.optString("serverUrl", "").ifEmpty { "http://127.0.0.1:$proxyPort" }

        val rtcWrapper = E2ERtcWrapper(AndroidRtcFactory(RaApp.instance))
        RaApp.instance.testRtcFactory = rtcWrapper
        RaApp.instance.testStunUrls = listOf("stun:$stunHost:$stunPort")

        // 1. UI account creation & pairing
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
        check(remote.account.server == serverUrl)

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
            while (System.currentTimeMillis() < deadline) {
                val st = ctrlGet(ctrlBase, "/pair-status")
                val hostErr = if (st.isNull("error")) "" else st.optString("error")
                check(hostErr.isEmpty()) { "Pair failed on host: $hostErr" }
                val id = if (st.isNull("machineId")) "" else st.optString("machineId")
                if (id.isNotEmpty()) {
                    machineId = id
                    break
                }
                delay(150)
            }
        }
        check(machineId.isNotEmpty()) { "Timed out waiting for paired machineId" }
        ui.waitUntil(60_000) { remote.coreHost.snapshotSettingsView().machinesList.firstOrNull { it.machineId == machineId }?.let { it.online || it.directReady } == true }

        val metrics = JSONObject()
        runBlocking(Dispatchers.IO) {
            fun homeSessionIds(vm: io.github.lewin671.remoteagent.core.proto.HomeViewModel): Set<String> =
                (vm.runningList.map { it.sessionId } + vm.attentionList.map { it.sessionId }).filter { it.isNotEmpty() }.toSet()
            val beforeIds = homeSessionIds(remote.coreHost.homeView.value)
            remote.coreHost.openNewSession(projectDir)
            remote.coreHost.selectNewSessionMachine(machineId)
            remote.coreHost.selectNewSessionAgent(Agent.AGENT_CLAUDE)
            remote.coreHost.selectNewSessionModel("", "", mode = ApprovalMode.APPROVAL_MODE_ASK)
            remote.coreHost.submitNewSession(projectDir)
            val sessionId: String = withTimeout(20_000) {
                val vm = remote.homeView.first { v -> homeSessionIds(v).any { it !in beforeIds } }
                homeSessionIds(vm).first { it !in beforeIds }
            }
            metrics.put("sessionId", sessionId)

            fun isDirectConnected(): Boolean =
                remote.directRouteReport(machineId).state == DirectRouteState.DIRECT_ROUTE_STATE_CONNECTED

            suspend fun waitDirectReady(timeoutMs: Long = 30_000) {
                android.util.Log.i("DirectE2E", "waitDirectReady start")
                val deadline = System.currentTimeMillis() + timeoutMs
                while (System.currentTimeMillis() < deadline) {
                    if (isDirectConnected()) {
                        delay(700)
                        return
                    }
                    delay(100)
                }
                error("Direct path did not become ready within ${timeoutMs}ms; report=${remote.directRouteReport(machineId)}")
            }
            waitDirectReady()

            ctrlPost(ctrlBase, "/set-relay-delay", JSONObject().put("ms", 150).put("resetCount", true))
            remote.setDirectEnabled(machineId, false)
            delay(500)
            val connectStart = System.currentTimeMillis()
            remote.setDirectEnabled(machineId, true)
            var connectedMs = -1L
            var warmStartMs = -1L
            var readUpgradeMs = -1L
            run {
                val deadline = connectStart + 60_000
                while (System.currentTimeMillis() < deadline) {
                    val st = remote.directRouteReport(machineId)
                    val now = System.currentTimeMillis() - connectStart
                    val connected = st.state == DirectRouteState.DIRECT_ROUTE_STATE_CONNECTED
                    if (connectedMs < 0 && connected) connectedMs = now
                    if (warmStartMs < 0 && connected) warmStartMs = now
                    if (st.readRoute == DirectPathChoice.DIRECT_PATH_CHOICE_DIRECT) {
                        readUpgradeMs = now - maxOf(warmStartMs, 0)
                        break
                    }
                    delay(20)
                }
            }
            val finalReport = remote.directRouteReport(machineId)
            assertTrue("Read path should upgrade to Direct by warm-up alone; report=$finalReport", readUpgradeMs >= 0)
            assertTrue("Read upgrade must happen within 7 s of the warm-up starting (took ${readUpgradeMs}ms)", readUpgradeMs <= 7_000)
            metrics.put("directConnectedMs", connectedMs)
            metrics.put("warmUpStartMs", warmStartMs)
            metrics.put("readUpgradeMs", readUpgradeMs)
            metrics.put("readDirectMs", if (finalReport.hasDirectReadMs) finalReport.directReadMs else null)
            metrics.put("readRelayMs", if (finalReport.hasRelayReadMs) finalReport.relayReadMs else null)
            waitDirectReady()

            // Shared file download over Direct via semantic Core action
            val totalBytes24M = 24 * 1024 * 1024
            val f1 = ctrlPost(ctrlBase, "/create-shared-file", JSONObject().put("sessionId", sessionId).put("sizeBytes", totalBytes24M).put("seed", 11))
            val fid1 = f1.getString("fileId")
            val dlStart = System.currentTimeMillis()
            remote.coreHost.startSharedFileDownload(sessionId, fid1, autoOpenIfStaying = false)
            ui.waitUntil(30_000) { remote.filePath(machineId) == DirectPathChoice.DIRECT_PATH_CHOICE_DIRECT }
            metrics.put("bulk24M_totalMs", System.currentTimeMillis() - dlStart)

            // Verify redesigned UI displays on DeviceScreen and ConnCheckSheet while upgraded
            ui.runOnUiThread { }
            if (ui.onAllNodesWithText("连接检测").fetchSemanticsNodes().isEmpty() &&
                ui.onAllNodes(androidx.compose.ui.test.hasContentDescription("设置")).fetchSemanticsNodes().isNotEmpty()
            ) {
                ui.onNodeWithContentDescription("设置").performClick()
            }
            val hostLabel = remote.settingsView.value.machinesList.firstOrNull { it.machineId == machineId }?.name?.takeIf { it.isNotEmpty() } ?: machineId.take(8)
            waitText(hostLabel, 10_000)
            ui.onAllNodesWithText(hostLabel).onFirst().performScrollTo().performClick()
            waitText("正在用直连", 15_000)
            ui.onNodeWithText("连接").performScrollTo().assertIsDisplayed()
            ui.onNodeWithText("发消息、审批和终端输入同时经直连和中继", substring = true).performScrollTo().assertIsDisplayed()
            ui.onNodeWithContentDescription("返回").performClick()

            ui.onNodeWithText("连接检测").performScrollTo().performClick()
            waitText("已连接", 15_000)
            waitText("ms", 15_000)
            ui.onNodeWithText("完成").performClick()

            // Four Negative Cases via semantic PingMachine
            ctrlPost(ctrlBase, "/set-relay-delay", JSONObject().put("ms", 0).put("bulkMs", 150).put("resetCount", true))
            remote.setDirectEnabled(machineId, false)
            delay(500)
            assertEquals(DirectPathChoice.DIRECT_PATH_CHOICE_RELAY, remote.filePath(machineId))
            remote.coreHost.pingMachine(machineId)
            remote.setDirectEnabled(machineId, true)
            waitDirectReady(25_000)

            remote.setDirectEnabled(machineId, false)
            delay(200)
            ctrlPost(ctrlBase, "/set-direct-policy", JSONObject().put("machineId", machineId).put("enabled", false))
            remote.setDirectEnabled(machineId, true)
            val policyDeadline = System.currentTimeMillis() + 30_000
            while (System.currentTimeMillis() < policyDeadline) {
                val mv = remote.coreHost.snapshotSettingsView().machinesList.firstOrNull { it.machineId == machineId }
                if (mv?.online == true) break
                delay(150)
            }
            delay(6_000)
            assertFalse("Remote DirectPolicy=Off must keep Direct disabled", isDirectConnected())
            remote.coreHost.pingMachine(machineId)
            ctrlPost(ctrlBase, "/set-direct-policy", JSONObject().put("machineId", machineId).put("enabled", true))
            remote.setDirectEnabled(machineId, false)
            delay(200)
            remote.setDirectEnabled(machineId, true)
            waitDirectReady(30_000)

            remote.setDirectEnabled(machineId, false)
            ctrlPost(ctrlBase, "/stun-control", JSONObject().put("enabled", false))
            delay(300)
            remote.setDirectEnabled(machineId, true)
            waitDirectReady(25_000)
            assertTrue("STUN unreachable must not block a path that connects", isDirectConnected())
            remote.coreHost.pingMachine(machineId)
            ctrlPost(ctrlBase, "/stun-control", JSONObject().put("enabled", true))
            remote.setDirectEnabled(machineId, false)
            delay(300)
            remote.setDirectEnabled(machineId, true)
            waitDirectReady(30_000)

            remote.setDirectEnabled(machineId, false)
            rtcWrapper.blockUdp = true
            delay(300)
            remote.setDirectEnabled(machineId, true)
            delay(12_000)
            assertFalse("UDP dropped must stay on relay", isDirectConnected())
            remote.coreHost.pingMachine(machineId)
            rtcWrapper.blockUdp = false
            remote.setDirectEnabled(machineId, false)
            delay(300)
            remote.setDirectEnabled(machineId, true)
            waitDirectReady(30_000)
            metrics.put("negativeCasesPassed", 4)

            // Semantic Interactive Approval latency measurement
            ctrlPost(ctrlBase, "/set-relay-delay", JSONObject().put("ms", 0).put("bulkMs", 350).put("resetCount", true))
            val approvalLatenciesMs = ArrayList<Double>()
            for (i in 1..20) {
                ctrlPost(ctrlBase, "/trigger-approval", JSONObject().put("sessionId", sessionId).put("command", "echo $i"))
                remote.openSession(sessionId)
                val evJob = launch { remote.watchSession(sessionId).collect { } }
                val liveVm = withTimeout(10_000) {
                    remote.watchSession(sessionId).first { vm ->
                        vm.timeline.loaded && vm.dock.hasItem && vm.dock.kind == SessionDockKind.SESSION_DOCK_KIND_APPROVAL && vm.dock.approval.approvalId.isNotEmpty()
                    }
                }
                val approvalId = liveVm.dock.approval.approvalId
                delay(30)
                val observedSeq = liveVm.dock.observedSeq
                val t0 = System.nanoTime()
                remote.coreHost.resolveApproval(sessionId, approvalId, allow = true, observedEventSeq = observedSeq)
                withTimeout(5_000) {
                    remote.watchSession(sessionId).first { vm ->
                        !vm.dock.hasItem || vm.dock.approval.approvalId != approvalId || !vm.dock.command.busy
                    }
                }
                approvalLatenciesMs.add((System.nanoTime() - t0) / 1_000_000.0)
                evJob.cancelAndJoin()
            }
            val sortedLat = approvalLatenciesMs.sorted()
            val p50 = sortedLat[(ceil(sortedLat.size * 0.50).toInt() - 1).coerceIn(0, sortedLat.lastIndex)]
            val p95 = sortedLat[(ceil(sortedLat.size * 0.95).toInt() - 1).coerceIn(0, sortedLat.lastIndex)]
            val p99 = sortedLat[(ceil(sortedLat.size * 0.99).toInt() - 1).coerceIn(0, sortedLat.lastIndex)]
            metrics.put("approvalP50Ms", p50)
            metrics.put("approvalP95Ms", p95)
            metrics.put("approvalP99Ms", p99)
            ctrlPost(ctrlBase, "/report-metrics", metrics)
            assertTrue("Approval p99 ${p99}ms exceeded 100ms limit", p99 <= 100.0)
        }
    }
}
