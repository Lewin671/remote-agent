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
import io.github.lewin671.remoteagent.core.PairingUiState
import io.github.lewin671.remoteagent.core.proto.SessionDockKind
import io.github.lewin671.remoteagent.core.transport.DirectDiag
import io.github.lewin671.remoteagent.core.transport.DirectStage
import io.github.lewin671.remoteagent.core.transport.RouteName
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
        ui.waitUntil(60_000) { remote.state.value.machines[machineId]?.pathState?.reachable == true }

        val metrics = JSONObject()
        runBlocking(Dispatchers.IO) {
            // Create an isolated Claude session on the host daemon
            val created = remote.machineCommand(
                machineId,
                Command.newBuilder().setCreateSession(
                    CreateSessionRequest.newBuilder()
                        .setCwd(projectDir)
                        .setAgent(Agent.AGENT_CLAUDE)
                        .setMode(ApprovalMode.APPROVAL_MODE_ASK),
                ).build(),
            )
            val sessionId = created.createSession.session.id
            metrics.put("sessionId", sessionId)

            // Wait until Direct path establishes initially
            fun isDirectConnected(): Boolean =
                remote.peer(machineId)?.directStatus?.stage in setOf(DirectStage.CONNECTED_RELAY, DirectStage.DIRECT_ACTIVE)

            suspend fun waitDirectReady(timeoutMs: Long = 30_000) {
                DirectDiag.sink = { android.util.Log.i("DirectDiag", it) }
                android.util.Log.i("DirectE2E", "waitDirectReady start")
                val deadline = System.currentTimeMillis() + timeoutMs
                while (System.currentTimeMillis() < deadline) {
                    if (isDirectConnected()) {
                        // Allow the second PeerConnection (bulk use=2) and Control 1/14 capabilities to settle.
                        delay(700)
                        return
                    }
                    delay(100)
                }
                error("Direct path did not become ready within ${timeoutMs}ms; state=${remote.peer(machineId)?.directStatus}")
            }
            waitDirectReady()

            // Inject 150ms relay /Forward delay, then set a fresh direct path up under it: the warm-up
            // (not user reads) must make the read path upgrade by itself, within seconds of connecting.
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
                    val st = remote.peer(machineId)?.directStatus
                    val now = System.currentTimeMillis() - connectStart
                    if (connectedMs < 0 && st?.stage in setOf(DirectStage.CONNECTED_RELAY, DirectStage.DIRECT_ACTIVE)) connectedMs = now
                    // Direct read path is ready immediately under direct-first.
                    if (warmStartMs < 0 && st?.stage == DirectStage.DIRECT_ACTIVE) warmStartMs = now
                    if (st?.readDirect == true) {
                        readUpgradeMs = now - maxOf(warmStartMs, 0)
                        break
                    }
                    delay(20)
                }
            }
            assertTrue("Read path should upgrade to Direct by warm-up alone; status=${remote.peer(machineId)?.directStatus}", readUpgradeMs >= 0)
            assertTrue("Read upgrade must happen within 7 s of the warm-up starting (took ${readUpgradeMs}ms)", readUpgradeMs <= 7_000)
            metrics.put("directConnectedMs", connectedMs)
            metrics.put("warmUpStartMs", warmStartMs)
            metrics.put("readUpgradeMs", readUpgradeMs)
            metrics.put("readDirectMs", remote.peer(machineId)?.directStatus?.readDirectMs)
            metrics.put("readRelayMs", remote.peer(machineId)?.directStatus?.readRelayMs)
            waitDirectReady()

            android.util.Log.i("DirectE2E", "step // 3. >16 MiB")
            // 3. >16 MiB shared file download (24 MiB = 32 x 768 KiB chunks):
            //    verify 0 accepted-log growth after Direct upgrade, SHA-256 match, relay vs direct throughput, bulk upgrade time.
            val totalBytes24M = 24 * 1024 * 1024
            val f1 = ctrlPost(ctrlBase, "/create-shared-file", JSONObject().put("sessionId", sessionId).put("sizeBytes", totalBytes24M).put("seed", 11))
            val fid1 = f1.getString("fileId")
            val wantSha1 = f1.getString("sha256Hex")
            val chunkSize = 768 * 1024
            val dlStart = System.currentTimeMillis()
            val startedDirect = remote.filePath(machineId) == RouteName.DIRECT
            var upgradedAtMs = if (startedDirect) 0L else -1L
            var relayChunksBytes = 0L
            var relayChunksMs = 0L
            var directChunksBytes = 0L
            var directChunksMs = 0L
            var acceptedAtUpgrade = if (startedDirect) ctrlGet(ctrlBase, "/accepted-count").getInt("count") else -1
            val out1 = ByteArrayOutputStream(totalBytes24M)
            var offset = 0L
            var relayStartMs = System.currentTimeMillis()
            var directStartMs = if (startedDirect) dlStart else -1L
            while (offset < totalBytes24M) {
                val isDirectNow = remote.filePath(machineId) == RouteName.DIRECT
                val cStart = System.currentTimeMillis()
                val res = retryNotSent { remote.getSharedFileChunk(sessionId, fid1, offset) }
                val cElapsed = System.currentTimeMillis() - cStart
                out1.write(res.data.toByteArray())
                offset += res.data.size()
                if (!isDirectNow && remote.filePath(machineId) == RouteName.DIRECT) {
                    upgradedAtMs = System.currentTimeMillis() - dlStart
                    acceptedAtUpgrade = ctrlGet(ctrlBase, "/accepted-count").getInt("count")
                    directStartMs = System.currentTimeMillis()
                }
                if (isDirectNow) {
                    directChunksBytes += res.data.size()
                    directChunksMs += cElapsed
                } else {
                    relayChunksBytes += res.data.size()
                    relayChunksMs += cElapsed
                }
            }
            val dlTotalMs = System.currentTimeMillis() - dlStart
            val acceptedAfterDl = ctrlGet(ctrlBase, "/accepted-count").getInt("count")
            assertEquals("24 MiB download SHA-256 mismatch", wantSha1, sha256Hex(out1.toByteArray()))
            assertEquals("accepted SQLite rows must NOT grow during Direct bulk download", acceptedAtUpgrade, acceptedAfterDl)
            metrics.put("bulk24M_totalMs", dlTotalMs)
            metrics.put("bulkUpgradeMs", upgradedAtMs)
            metrics.put("acceptedAtUpgrade", acceptedAtUpgrade)
            metrics.put("acceptedAfterDirectDl", acceptedAfterDl)
            metrics.put("relayBulkMibPerSec", remote.peer(machineId)?.directStatus?.bulkRelayMibPerSec)
            metrics.put("directBulkMibPerSec", remote.peer(machineId)?.directStatus?.bulkDirectMibPerSec)

            // Verify redesigned UI displays on DeviceScreen and ConnCheckSheet while upgraded
            ui.runOnUiThread { }
            // Navigate into DeviceScreen from Settings machine row
            if (ui.onAllNodesWithText("连接检测").fetchSemanticsNodes().isEmpty() &&
                ui.onAllNodes(androidx.compose.ui.test.hasContentDescription("设置")).fetchSemanticsNodes().isNotEmpty()
            ) {
                ui.onNodeWithContentDescription("设置").performClick()
            }
            val hostLabel = machineLabel(remote.state.value, machineId)
            waitText(hostLabel, 10_000)
            ui.onAllNodesWithText(hostLabel).onFirst().performScrollTo().performClick()
            waitText("正在用直连", 15_000)
            ui.onNodeWithText("连接").performScrollTo().assertIsDisplayed()
            ui.onNodeWithText("发消息和审批同时经直连和中继", substring = true).performScrollTo().assertIsDisplayed()
            ui.onNodeWithContentDescription("返回").performClick()

            // Also verify ConnCheckSheet
            ui.onNodeWithText("连接检测").performScrollTo().performClick()
            waitText("已连接", 15_000)
            waitText("ms", 15_000)
            ui.onNodeWithText("完成").performClick()

            android.util.Log.i("DirectE2E", "step // 4. Mid-download")
            // 4. Mid-download disconnect -> fallback -> resume (24 MiB file)
            // The same 24 MiB file again: its per-file state is already upgraded, so the drop lands on a direct download.
            val fid2 = fid1
            val wantSha2 = wantSha1
            val out2 = ByteArrayOutputStream(totalBytes24M)
            var off2 = 0L
            var dropped = false
            var fallbackMs = -1L
            while (off2 < totalBytes24M) {
                android.util.Log.i("DirectE2E", "dl2 off=${off2 / chunkSize} path=${remote.filePath(machineId)} dropped=$dropped")
                if (!dropped && remote.filePath(machineId) == RouteName.DIRECT && off2 >= 8L * chunkSize) {
                    dropped = true
                    val tDrop = System.currentTimeMillis()
                    val chunkDeferred = async { retryNotSent { remote.getSharedFileChunk(sessionId, fid2, off2) } }
                    delay(15)
                    rtcWrapper.dropActiveConnections()
                    val resumedChunk = chunkDeferred.await()
                    fallbackMs = System.currentTimeMillis() - tDrop
                    assertEquals(RouteName.RELAY, remote.filePath(machineId))
                    out2.write(resumedChunk.data.toByteArray())
                    off2 += resumedChunk.data.size()
                    continue
                }
                val res = retryNotSent { remote.getSharedFileChunk(sessionId, fid2, off2) }
                out2.write(res.data.toByteArray())
                off2 += res.data.size()
            }
            assertTrue("Should have triggered mid-download Direct disconnect", dropped)
            assertEquals("Fallback resumed 24 MiB SHA-256 mismatch", wantSha2, sha256Hex(out2.toByteArray()))
            metrics.put("fallbackMs", fallbackMs)

            // Wait for Direct to reconnect cleanly after disconnect before Upload test
            waitDirectReady(30_000)

            android.util.Log.i("DirectE2E", "step // 5. Durable-offset")
            // 5. Durable-offset Upload resume over Direct/Relay
            val uploadTotal = 6 * 1024 * 1024
            val uploadBytes = ByteArray(uploadTotal) { i -> ((i * 17 + 5) and 0xFF).toByte() }
            val uploadShaBytes = MessageDigest.getInstance("SHA-256").digest(uploadBytes)
            val uploadShaHex = sha256Hex(uploadBytes)
            val transferId = "ab" + "0".repeat(30).take(30).let { "%030x".format(System.nanoTime()) }.take(30)
            val channel = remote.uploadChannel(machineId)
            val st0 = retryNotSent { channel.beginFile(transferId, "e2e-upload.bin", uploadTotal.toLong(), uploadShaBytes) }
            var upOff = st0.persistedBytes
            var uploadedCount = 0
            while (upOff < uploadTotal && uploadedCount < 3) {
                val end = minOf(uploadTotal.toLong(), upOff + chunkSize).toInt()
                val st = retryNotSent { channel.put(transferId, upOff, uploadBytes.copyOfRange(upOff.toInt(), end)) }
                upOff = st.persistedBytes
                uploadedCount++
            }
            // Simulate interruption & check durable persistedBytes via GetImageUploadStatus
            val stPaused = retryNotSent { channel.status(transferId) }
            assertEquals(3L * chunkSize, stPaused.persistedBytes)
            upOff = stPaused.persistedBytes
            while (upOff < uploadTotal) {
                val end = minOf(uploadTotal.toLong(), upOff + chunkSize).toInt()
                val st = retryNotSent { channel.put(transferId, upOff, uploadBytes.copyOfRange(upOff.toInt(), end)) }
                upOff = st.persistedBytes
            }
            val stDone = retryNotSent { channel.complete(transferId) }
            assertEquals(uploadTotal.toLong(), stDone.persistedBytes)
            val verifyRes = ctrlPost(ctrlBase, "/verify-upload", JSONObject().put("expectedSha256Hex", uploadShaHex))
            assertTrue("Uploaded file SHA-256 must match on daemon disk", verifyRes.getBoolean("matched"))
            metrics.put("uploadResumeOffset", stPaused.persistedBytes)
            metrics.put("uploadVerified", true)

            // 6. Four Negative Cases (disable relay delay for crisp functional timing)
            ctrlPost(ctrlBase, "/set-relay-delay", JSONObject().put("ms", 0).put("bulkMs", 150).put("resetCount", true))

            android.util.Log.i("DirectE2E", "step // 6a)")
            // 6a) Local switch Off
            remote.setDirectEnabled(machineId, false)
            delay(500)
            assertEquals(DirectStage.OFF, remote.peer(machineId)?.directStatus?.stage)
            assertEquals(RouteName.RELAY, remote.filePath(machineId))
            val probeOff = retryNotSent { remote.getSharedFileChunk(sessionId, fid1, 0L) }
            assertEquals(chunkSize, probeOff.data.size())
            remote.setDirectEnabled(machineId, true)
            waitDirectReady(25_000)

            android.util.Log.i("DirectE2E", "step // 6b)")
            // 6b) Remote DirectPolicy Off
            remote.setDirectEnabled(machineId, false)
            delay(200)
            ctrlPost(ctrlBase, "/set-direct-policy", JSONObject().put("machineId", machineId).put("enabled", false))
            remote.setDirectEnabled(machineId, true)
            // Wait for restarted daemon to reconnect back online with policy=false
            val policyDeadline = System.currentTimeMillis() + 30_000
            while (System.currentTimeMillis() < policyDeadline) {
                val mv = remote.state.value.machines[machineId]
                if (mv?.online == true && !mv.pathState.knownOffline) break
                delay(150)
            }
            delay(6_000)
            assertFalse("Remote DirectPolicy=Off must keep Direct disabled", isDirectConnected())
            assertEquals(RouteName.RELAY, remote.filePath(machineId))
            val probePolicy = retryNotSent { remote.getSharedFileChunk(sessionId, fid1, 0L) }
            assertEquals(chunkSize, probePolicy.data.size())
            ctrlPost(ctrlBase, "/set-direct-policy", JSONObject().put("machineId", machineId).put("enabled", true))
            remote.setDirectEnabled(machineId, false)
            delay(200)
            remote.setDirectEnabled(machineId, true)
            waitDirectReady(30_000)

            android.util.Log.i("DirectE2E", "step // 6c)")
            // 6c) STUN unreachable (does not block)
            remote.setDirectEnabled(machineId, false)
            ctrlPost(ctrlBase, "/stun-control", JSONObject().put("enabled", false))
            delay(300)
            remote.setDirectEnabled(machineId, true)
            // A connected and confirmed path is used without a reflexive candidate (as the daemon does since
            // v0.80): an unreachable STUN no longer keeps a working pair on the relay.
            waitDirectReady(25_000)
            assertTrue("STUN unreachable must not block a path that connects", isDirectConnected())
            assertEquals(chunkSize, retryNotSent { remote.getSharedFileChunk(sessionId, fid1, 0L) }.data.size())
            ctrlPost(ctrlBase, "/stun-control", JSONObject().put("enabled", true))
            remote.setDirectEnabled(machineId, false)
            delay(300)
            remote.setDirectEnabled(machineId, true)
            waitDirectReady(30_000)

            android.util.Log.i("DirectE2E", "step // 6d)")
            // 6d) UDP dropped
            remote.setDirectEnabled(machineId, false)
            rtcWrapper.blockUdp = true
            delay(300)
            remote.setDirectEnabled(machineId, true)
            delay(12_000)
            assertFalse("UDP dropped must stay on relay", isDirectConnected())
            assertEquals(RouteName.RELAY, remote.filePath(machineId))
            assertEquals(chunkSize, retryNotSent { remote.getSharedFileChunk(sessionId, fid1, 0L) }.data.size())
            rtcWrapper.blockUdp = false
            remote.setDirectEnabled(machineId, false)
            delay(300)
            remote.setDirectEnabled(machineId, true)
            waitDirectReady(30_000)
            metrics.put("negativeCasesPassed", 4)

            android.util.Log.i("DirectE2E", "step // 7. Saturated")
            // 7. Saturated bulk Direct download + Interactive Approval (GetLiveSnapshot -> ResolveApproval) p99 <= 100 ms
            // Upgrade fid1 back to DIRECT under relay 150ms injection, then clear relay delay for raw approval RTT.
            ctrlPost(ctrlBase, "/set-relay-delay", JSONObject().put("ms", 150).put("resetCount", true))
            for (warmIdx in 0 until 64) {
                if (remote.filePath(machineId) == RouteName.DIRECT) break
                retryNotSent { remote.getSharedFileChunk(sessionId, fid1, (warmIdx % 32).toLong() * chunkSize) }
            }
            assertEquals(RouteName.DIRECT, remote.filePath(machineId))
            ctrlPost(ctrlBase, "/set-relay-delay", JSONObject().put("ms", 0).put("bulkMs", 350).put("resetCount", true))

            // Measure baseline (0 bulk load) approval latency first
            val baselineApprovalMs = run {
                ctrlPost(ctrlBase, "/trigger-approval", JSONObject().put("sessionId", sessionId).put("command", "echo baseline"))
                var pendingId: String? = null
                val waitHook = System.currentTimeMillis() + 10_000
                while (pendingId == null && System.currentTimeMillis() < waitHook) {
                    val snap = remote.machineCommand(machineId, Command.newBuilder().setGetLiveSnapshot(GetLiveSnapshotRequest.newBuilder().setSessionId(sessionId)).build()).liveSnapshot
                    pendingId = snap.actionsList.firstOrNull { it.hasApprovalRequested() }?.approvalRequested?.approvalId
                    if (pendingId == null) delay(15)
                }
                val bid = checkNotNull(pendingId)
                remote.openSession(sessionId)
                val evJob = launch { remote.watchSession(sessionId).collect { } }
                val liveVm = withTimeout(5_000) {
                    remote.watchSession(sessionId).first { vm ->
                        vm.timeline.loaded && vm.dock.hasItem && vm.dock.kind == SessionDockKind.SESSION_DOCK_KIND_APPROVAL && vm.dock.approval.approvalId == bid
                    }
                }
                delay(30)
                val observedSeq = liveVm.dock.observedSeq
                val t0 = System.nanoTime()
                remote.machineCommand(machineId, Command.newBuilder().setResolveApproval(ResolveApprovalRequest.newBuilder().setSessionId(sessionId).setApprovalId(bid).setAllow(true).setObservedEventSeq(observedSeq)).build())
                val baseMs = (System.nanoTime() - t0) / 1_000_000.0
                android.util.Log.i("DirectE2E", "baseline_approval_ms=$baseMs")
                evJob.cancelAndJoin()
                baseMs
            }

            val bulkAttempted = AtomicInteger(0)
            val bulkSucceeded = AtomicInteger(0)
            val saturating = AtomicBoolean(true)
            val satJobs = List(1) { worker ->
                launch {
                    var idx = worker
                    while (isActive && saturating.get()) {
                        val o = ((idx % 32).toLong()) * chunkSize
                        idx += 1
                        bulkAttempted.incrementAndGet()
                        try {
                            retryNotSent { remote.getSharedFileChunk(sessionId, fid1, o) }
                            bulkSucceeded.incrementAndGet()
                        } catch (c: CancellationException) {
                            throw c
                        } catch (_: Exception) {
                            // count attempt failure cleanly
                        }
                    }
                }
            }
            delay(300) // Ensure bulk streams are actively saturating Direct channel

            val approvalLatenciesMs = ArrayList<Double>()
            for (i in 1..20) {
                ctrlPost(ctrlBase, "/trigger-approval", JSONObject().put("sessionId", sessionId).put("command", "echo $i"))
                // Wait outside measured RTT until hook arrives on daemon
                var pendingApproval: Pair<String, Long>? = null
                val waitHook = System.currentTimeMillis() + 10_000
                while (pendingApproval == null && System.currentTimeMillis() < waitHook) {
                    val snapProbe = remote.machineCommand(
                        machineId,
                        Command.newBuilder().setGetLiveSnapshot(
                            GetLiveSnapshotRequest.newBuilder().setSessionId(sessionId),
                        ).build(),
                    ).liveSnapshot
                    val act = snapProbe.actionsList.firstOrNull { it.hasApprovalRequested() }
                    if (act != null) {
                        pendingApproval = act.approvalRequested.approvalId to act.seq
                    } else {
                        delay(15)
                    }
                }
                val (approvalId, _) = checkNotNull(pendingApproval) { "Hook approval $i never appeared" }

                // Subscribe outside timing window so SessionViewModel updates are verified just like SessionScreen.
                remote.openSession(sessionId)
                val evJob = launch { remote.watchSession(sessionId).collect { } }
                val liveVm = withTimeout(5_000) {
                    remote.watchSession(sessionId).first { vm ->
                        vm.timeline.loaded && vm.dock.hasItem && vm.dock.kind == SessionDockKind.SESSION_DOCK_KIND_APPROVAL && vm.dock.approval.approvalId == approvalId
                    }
                }
                delay(30) // Let initial loadEvents/updateLivePollers settle before timing ResolveApproval
                val observedSeq = liveVm.dock.observedSeq

                // Measure full Android ResolveApproval under saturated Direct bulk load
                val t0 = System.nanoTime()
                remote.machineCommand(
                    machineId,
                    Command.newBuilder().setResolveApproval(
                        ResolveApprovalRequest.newBuilder()
                            .setSessionId(sessionId)
                            .setApprovalId(approvalId)
                            .setAllow(true)
                            .setObservedEventSeq(observedSeq),
                    ).build(),
                )
                val dtMs = (System.nanoTime() - t0) / 1_000_000.0
                android.util.Log.i("DirectE2E", "approval_round=$i dtMs=$dtMs path=${remote.filePath(machineId)} bulkDone=${bulkSucceeded.get()}/${bulkAttempted.get()}")
                approvalLatenciesMs.add(dtMs)
                evJob.cancelAndJoin()
            }
            saturating.set(false)
            satJobs.forEach { it.cancelAndJoin() }

            val sortedLat = approvalLatenciesMs.sorted()
            val p50 = sortedLat[(ceil(sortedLat.size * 0.50).toInt() - 1).coerceIn(0, sortedLat.lastIndex)]
            val p95 = sortedLat[(ceil(sortedLat.size * 0.95).toInt() - 1).coerceIn(0, sortedLat.lastIndex)]
            val p99 = sortedLat[(ceil(sortedLat.size * 0.99).toInt() - 1).coerceIn(0, sortedLat.lastIndex)]
            val totalBulk = bulkAttempted.get()
            val doneBulk = bulkSucceeded.get()
            metrics.put("baselineApprovalMs", baselineApprovalMs)
            metrics.put("approvalP50Ms", p50)
            metrics.put("approvalP95Ms", p95)
            metrics.put("approvalP99Ms", p99)
            metrics.put("saturatedBulkAttempted", totalBulk)
            metrics.put("saturatedBulkSucceeded", doneBulk)
            metrics.put("saturatedBulkCompletionRatio", doneBulk.toDouble() / totalBulk.coerceAtLeast(1))
            ctrlPost(ctrlBase, "/report-metrics", metrics)
            assertTrue("Expected saturated bulk chunks > 0", doneBulk > 0)
            assertTrue("Approval p99 ${p99}ms (p50=${p50}ms p95=${p95}ms latencies=$sortedLat) exceeded 100ms limit", p99 <= 100.0)
        }
    }
}
