package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.graphics.Bitmap
import android.graphics.Canvas
import android.view.inspector.WindowInspector
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.*
import io.github.lewin671.remoteagent.push.*
import io.github.lewin671.remoteagent.update.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.*
import org.junit.*
import org.junit.runner.RunWith
import org.robolectric.ParameterizedRobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.io.File
import java.time.Instant

/** Stopped-engine UI fixtures: fabricated identities/content, no daemon, account, or device. */
@RunWith(ParameterizedRobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class, qualifiers = "w390dp-h844dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class I18nBatchCTest(private val locale: String) {
    companion object {
        @JvmStatic @ParameterizedRobolectricTestRunner.Parameters(name = "{0}")
        fun locales() = listOf(arrayOf("zh-CN"), arrayOf("en"))
    }
    @get:Rule(order = 0) val appFixture = io.github.lewin671.remoteagent.RaAppTestRule(preparePendingStore = true)
    @get:Rule(order = 1) val ui = createComposeRule()
    @get:Rule(order = 2) val appJobs = appFixture.stopJobs { done ->
        ui.waitUntil(5000) { ui.mainClock.advanceTimeByFrame(); ui.waitForIdle(); done() }
    }
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "preview-client"), Api.defaultHttp())
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private val stamp = System.currentTimeMillis()
    private val now get() = Instant.ofEpochMilli(stamp)
    private var language by mutableStateOf(locale)
    private val machine = MachineView("m", true, stamp, MachineMeta.newBuilder().setHostname("Example computer").setProtocolVersion(PROTOCOL_VERSION)
        .setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true)).addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE))
        .addQuotas(AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_OK).setObservedMs(stamp)
            .addWindows(QuotaWindow.newBuilder().setName("five_hour").setWindowMinutes(300).setUsedPercent(62.0).setResetsAtMs(stamp + 3700_000))).build())
    private val client = DeviceView("preview-client", DeviceKind.DEVICE_KIND_CLIENT, stamp, DeviceMeta.newBuilder().setName("Example phone").setPlatform("android").build())
    private val session = SessionView("s", "m", Session.newBuilder().setId("s").setCwd("/example/demo").setTitle("Example conversation")
        .setAgent(Agent.AGENT_CLAUDE).setMode(ApprovalMode.APPROVAL_MODE_ASK).setStatus(SessionStatus.SESSION_STATUS_IDLE).build())
    private val state get() = RemoteState(conn = ConnState.ONLINE, loaded = true, synced = true, link = Link.ONLINE, isAuthority = true,
        authorityDeviceId = client.id, leaseEndMs = stamp + 86_400_000, prefs = PrefsObject.EMPTY, machines = mapOf("m" to machine),
        devices = mapOf(client.id to client, "m" to DeviceView("m", DeviceKind.DEVICE_KIND_MACHINE, stamp, DeviceMeta.newBuilder().setName("Example computer").build())), sessions = mapOf("s" to session))
    private val task get() = ScheduledTask("m", buildJsonObject {
        put("id", "t"); put("status", "scheduled"); put("next_run_at", now.plusSeconds(3600).toString())
        put("spec", buildJsonObject { put("title", "Example scheduled task"); put("message", "Keep the original user message unchanged.")
            put("trigger", buildJsonObject { put("type", "interval"); put("interval_seconds", 3600) })
            put("target", buildJsonObject { put("type", "new_session"); put("cwd", "/example/demo"); put("agent", "claude"); put("mode", "ask") }) })
        put("runs", JsonArray(listOf(buildJsonObject { put("id", "r"); put("session_id", "s"); put("status", "completed"); put("notified", true)
            put("started_at", now.minusSeconds(600).toString()); put("ended_at", now.minusSeconds(300).toString()); put("notification_text", "Original agent notification"); put("final_summary", "Original CLI summary") })))
    })
    @Before fun prepare() {
        remote.showForTest(state, events = mapOf("s" to emptyList()))
        @Suppress("UNCHECKED_CAST")
        val snapshots = ScheduleRepository::class.java.getDeclaredField("_state").apply { isAccessible = true }.get(remote.schedules) as MutableStateFlow<Map<String, ScheduleSnapshot>>
        snapshots.value = mapOf("m" to ScheduleSnapshot(revision = 0, tasks = listOf(task), cached = false))
    }
    @After fun close() { remote.close(); Outbox.clear(); ImageDrafts.clear(); Drafts.clear() }
    private fun text(message: UiMessage) = messageTextForTest(message, language)
    private fun show(content: @Composable () -> Unit) = ui.setContent {
        RaTheme(ThemeMode.LIGHT) { CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalMessageLocale provides language) {
            Box(Modifier.fillMaxSize().background(pal.paper)) { content() }
        } }
    }
    private fun shot(name: String) {
        ui.waitForIdle()
        val output = System.getenv("RA_I18N_C_SHOTS") ?: return
        ui.mainClock.advanceTimeBy(600)
        val nodes = ui.onAllNodes(isRoot(), useUnmergedTree = true).fetchSemanticsNodes()
        ui.runOnIdle {
            val overflow = mutableListOf<String>()
            fun inspect(node: androidx.compose.ui.semantics.SemanticsNode) {
                val layouts = mutableListOf<androidx.compose.ui.text.TextLayoutResult>()
                node.config.getOrNull(androidx.compose.ui.semantics.SemanticsActions.GetTextLayoutResult)?.action?.invoke(layouts)
                for (layout in layouts) if (layout.hasVisualOverflow) overflow += layout.layoutInput.text.text
                node.children.forEach(::inspect)
            }
            nodes.forEach(::inspect)
            File(output, "layout-$locale-$name.txt").writeText(overflow.distinct().joinToString("\n"))
            val view = WindowInspector.getGlobalWindowViews().last()
            val bitmap = Bitmap.createBitmap(view.width, view.height, Bitmap.Config.ARGB_8888)
            view.draw(Canvas(bitmap))
            File(output, "$locale-$name.png").apply { parentFile!!.mkdirs() }.outputStream().use { Assert.assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
    }
    @Test fun settings() {
        show { SettingsScreen() }; shot("settings")
        ui.onNodeWithText(text(UiMessages.settingsAppearance())).performScrollTo(); shot("appearance-notifications")
        ui.onNodeWithText(text(UiMessages.settingsAndroidOpenSourceLicenses())).performScrollTo(); shot("about")
    }
    private fun settingsReply() = GetSettingsResponse.newBuilder().setSettings(DaemonSettings.newBuilder()
        .setApprovalTimeoutSeconds(600).setIdleCloseMinutes(360).setImageUploadQuotaBytes(1L shl 30)
        .setClaude(AgentDefaults.newBuilder().setMode(ApprovalMode.APPROVAL_MODE_ASK).setModel("example-model")))
        .setImageStorage(ImageStorageUsage.newBuilder().setUploadBytes(14L shl 20).setReclaimableBytes(2L shl 20).setCacheBytes(3L shl 20)).build()
    @Test fun machine() {
        show { DeviceScreen("m", { _, _ -> settingsReply() }) }; shot("computer-settings")
        ui.onNodeWithText(text(UiMessages.settingsApprovalTimeout())).performScrollTo(); shot("daemon-settings")
        ui.onNodeWithText(text(UiMessages.settingsPreviewCache())).performScrollTo(); shot("computer-storage")
    }
    @Test fun approvalTimeout() {
        show { DeviceScreen("m", { _, _ -> settingsReply() }) }
        ui.onNodeWithText(text(UiMessages.settingsApprovalTimeout())).performScrollTo().performClick(); shot("approval-timeout")
    }
    @Test fun idleClose() {
        show { DeviceScreen("m", { _, _ -> settingsReply() }) }
        ui.onNodeWithText(text(UiMessages.settingsAutomaticallyEndIdleSessions())).performScrollTo().performClick(); shot("idle-close")
    }
    @Test fun quota() { show { QuotaScreen("m") }; shot("quota") }
    @Test fun licenses() { show { LicensesPage {} }; shot("licenses") }
    @Test fun storage() { show { PhoneImageStoragePage {} }; shot("phone-storage") }
    @Test fun devices() { show { DevicesScreen() }; shot("devices") }
    @Test fun client() { show { ClientScreen(client.id) }; shot("client") }
    @Test fun security() { show { Column { AccountSecurityContent(state.copy(recoveryPending = true, recoveryReadyMs = stamp + 172_800_000)) } }; shot("security") }
    @Test fun securityMember() { show { Column { AccountSecurityContent(state.copy(isAuthority = false)) } }; shot("security-member") }
    @Test fun recover() { show { RecoverScreen(appFixture.app) }; shot("recover") }
    @Test fun createAccount() { show { CreateAccountScreen(appFixture.app) }; shot("create-account") }
    @Test fun rebuild() { show { RebuildSecurityScreen(appFixture.app, null) }; shot("rebuild") }
    @Test fun rotateRecovery() { show { RotateRecoveryContent(state, { error("Preview does not publish") }, {}, {}) }; shot("rotate-recovery") }
    @Test fun recoveryStatus() { show { Column(Modifier.padding(20.dp)) { RecoveryStatus(GetRecoveryContextResponse.Phase.PHASE_READY, false, "", {}, {}, {}) } }; shot("recovery-ready") }
    @Test fun handoff() { show { HandoffPreparationContent(state.copy(isAuthority = false), client.id, { "EXAMPLE RECOVERY CODE" to ByteArray(32) }, {}, {}) }; shot("handoff") }
    @Test fun join() { show { JoinScreen(appFixture.app) }; shot("join") }
    @Test fun removed() { show { RemovedScreen(appFixture.app, remote) }; shot("removed") }
    @Test fun pair() { show { PairScreen() }; shot("pair") }
    @Test fun startup() { show { AccountLoadFailure(text(UiMessages.errorsAndroidCouldNotReadTheSessionList()), {}, {}) }; shot("startup-error") }
    @Test fun connection() { show { ConnCheckSheet(state.copy(conn = ConnState.OFFLINE), {}, "https://example.com", { listOf("m") }, { 750 }, { 1800 }, {}) }; shot("connection") }
    @Test fun schedules() { show { SchedulesScreen() }; shot("schedules") }
    @Test fun scheduleDetail() { show { ScheduleDetailScreen("m", "t") }; shot("schedule-detail") }
    @Test fun scheduleEditor() { show { ScheduleEditor("s", {}) }; shot("schedule-editor") }
    @Test fun scheduleMessage() { show { ScheduleMessageEditor("Original user message", false, "", {}, {}) }; shot("schedule-message") }
    @Test fun scheduleTime() { show { ScheduleTimeSheet(task.trigger, {}, {}) }; shot("schedule-time") }
    @Test fun scheduleRuns() { show { Column { ScheduleRunGroup(RunGroup("r", task.allRuns, false), now, setOf("r")) {} } }; shot("schedule-results") }
    @Test fun scheduleStopped() { show { Column(Modifier.padding(20.dp)) { ScheduleAttentionBanner(scheduleAttentionText(task.copy(json = JsonObject(task.json + mapOf("status" to JsonPrimitive("uncertain"), "reason" to JsonPrimitive("restart_dispatching")))), copy = uiText())) } }; shot("schedule-stopped") }
    @Test fun update() {
        @Suppress("UNCHECKED_CAST")
        val updates = Updater::class.java.getDeclaredField("_state").apply { isAccessible = true }.get(Updater) as MutableStateFlow<UpdateState>
        updates.value = UpdateState.Available(Release("99.0.0", 999999, PROTOCOL_VERSION + 1, "example.apk", "00".repeat(32), 42L shl 20, "Original release notes remain unchanged."))
        try { show { UpdateSheet {} }; shot("update") } finally { updates.value = UpdateState.Unknown }
    }
    @Test fun imageErrors() { show { Column(Modifier.padding(20.dp)) { for (error in listOf("20 MB", "dimensions", "source missing", "decode", "quota", "network", "opaque")) {
        Text(imageProblem(rawAttachmentFailure(error), copy = uiText()).message, style = T.s15, modifier = Modifier.padding(vertical = 10.dp))
    } } }; shot("image-errors") }
    @Test fun nativeNotifications() {
        val context = appFixture.app
        Push.createChannels(context, copy = ::text)
        Notifier.showSecurity(context, copy = ::text)
        Notifier.show(context, "preview-session", NotifyKind.NOTIFY_KIND_TURN_ENDED, null, false, copy = ::text)
        val manager = context.getSystemService(android.app.NotificationManager::class.java)
        val posted = manager.activeNotifications.toList()
        Assert.assertEquals(2, posted.size)
        val output = System.getenv("RA_I18N_C_SHOTS")
        for (entry in posted) {
            val n = entry.notification
            Assert.assertEquals(android.app.Notification.VISIBILITY_PRIVATE, n.visibility)
            Assert.assertFalse(n.extras.getCharSequence(android.app.Notification.EXTRA_TEXT).isNullOrEmpty())
            if (output != null) {
                n.`when` = 0
                val view = android.app.Notification.Builder.recoverBuilder(context, n).createContentView().apply(context, null)
                view.measure(android.view.View.MeasureSpec.makeMeasureSpec(390, android.view.View.MeasureSpec.EXACTLY), android.view.View.MeasureSpec.makeMeasureSpec(844, android.view.View.MeasureSpec.AT_MOST))
                view.layout(0, 0, view.measuredWidth, view.measuredHeight)
                val bitmap = Bitmap.createBitmap(view.width, view.height, Bitmap.Config.ARGB_8888)
                val canvas = Canvas(bitmap).apply { drawColor(android.graphics.Color.WHITE) }
                view.draw(canvas)
                File(output, "$locale-notification-${entry.id}.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
        }
        // Same IDs update channel labels while keeping the user's importance setting.
        Push.createChannels(context, copy = ::text)
        Assert.assertEquals(text(UiMessages.settingsWhenINeedToAct()), manager.getNotificationChannel(Push.CHANNEL_ATTENTION).name)
        manager.cancelAll()
    }
    @Test fun languageChangeKeepsEditor() {
        show { ScheduleMessageEditor("Original draft stays unchanged", false, "", {}, {}) }
        ui.runOnIdle { language = if (locale == "en") "zh-CN" else "en" }
        ui.onAllNodes(hasSetTextAction()).onLast().assertTextEquals("Original draft stays unchanged")
    }
    @Test fun deferredErrorsAndPluralCounts() {
        val error = CommandException(CommandException.UNKNOWN, UiMessages.errorsAndroidComputerOutcomeUnknownTheConnectionDropped().messageText(), failureKind = CommandFailureKind.CONNECTION_DROPPED)
        Assert.assertEquals(text(UiMessages.errorsAndroidComputerOutcomeUnknownTheConnectionDropped()), displayError(error, copy = ::text))
        for (count in listOf(0, 1, 2, 1000)) {
            Assert.assertTrue(text(UiMessages.scheduleRunsTotal(count)).contains(count.toString()))
            Assert.assertTrue(text(UiMessages.scheduleEveryDays(count)).contains(count.toString()))
        }
        val large = Int.MAX_VALUE.toLong() + 1
        val frequency = scheduleFrequency(buildJsonObject { put("type", "interval"); put("interval_seconds", large) }, copy = ::text)
        Assert.assertTrue(frequency.contains(large.toString()))
        Assert.assertEquals("原始 CLI / original CLI", displayError(Exception("原始 CLI / original CLI"), copy = ::text))
        val grouped = imageFailureLine(listOf("decode", "unsupported").map(::rawAttachmentFailure), copy = ::text) { error("Same category must not use count summary") }
        Assert.assertEquals(imageProblem(rawAttachmentFailure("decode"), copy = ::text).message, grouped)
    }
}
