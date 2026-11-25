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
    private val remote = Remote(Account("http://127.0.0.1:1", "preview-client"), Api.defaultHttp())
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private val stamp = System.currentTimeMillis()
    private val now get() = Instant.ofEpochMilli(stamp)
    private var language by mutableStateOf(locale)
    private val machine = MachineView("m", true, stamp, MachineMeta.newBuilder().setHostname("Example computer").setProtocolVersion(PEER_PROTOCOL_VERSION)
        .setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true)).addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE))
        .addQuotas(AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_OK).setObservedMs(stamp)
            .addWindows(QuotaWindow.newBuilder().setName("five_hour").setWindowMinutes(300).setUsedPercent(62.0).setResetsAtMs(stamp + 3700_000))).build())
    private val client = DeviceView("preview-client", DeviceKind.DEVICE_KIND_CLIENT, stamp, DeviceMeta.newBuilder().setName("Example phone").setPlatform("android").build())
    private val session = SessionView("s", "m", Session.newBuilder().setId("s").setCwd("/example/demo").setTitle("Example conversation")
        .setAgent(Agent.AGENT_CLAUDE).setMode(ApprovalMode.APPROVAL_MODE_ASK).setStatus(SessionStatus.SESSION_STATUS_IDLE).build())
    private val state get() = RemoteState(conn = ConnState.ONLINE, loaded = true, synced = true, link = Link.ONLINE, isAuthority = true,
        authorityDeviceId = client.id, leaseEndMs = stamp + 86_400_000, prefs = PrefsObject.EMPTY, machines = mapOf("m" to machine),
        devices = mapOf(client.id to client, "m" to DeviceView("m", DeviceKind.DEVICE_KIND_MACHINE, stamp, DeviceMeta.newBuilder().setName("Example computer").build())), sessions = mapOf("s" to session))
    private val sampleSchedulesVm: io.github.lewin671.remoteagent.core.proto.SchedulesViewModel
        get() = io.github.lewin671.remoteagent.core.proto.SchedulesViewModel.newBuilder()
            .addOthers(
                io.github.lewin671.remoteagent.core.proto.ScheduleTaskItemView.newBuilder()
                    .setMachineId("m")
                    .setTaskId("t")
                    .setTitle("Example scheduled task")
                    .setPreview(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("Original agent notification"))
                    .setMetadata(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("Every 1 hour · Example computer")),
            )
            .build()
    private val sampleDetailVm: io.github.lewin671.remoteagent.core.proto.ScheduleDetailViewModel
        get() = io.github.lewin671.remoteagent.core.proto.ScheduleDetailViewModel.newBuilder()
            .setFound(true)
            .setMachineId("m")
            .setTaskId("t")
            .setTitle("Example scheduled task")
            .setTaskName("Example scheduled task")
            .setMessage("Keep the original user message unchanged.")
            .setNotifyOnFinish(true)
            .setTimingText(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("Every 1 hour"))
            .setTargetLabel(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setKey("schedule.run_in"))
            .setTargetValue(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("demo · Claude Code"))
            .setPermissionsText(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setKey("modes.ask"))
            .setResultCard(
                io.github.lewin671.remoteagent.core.proto.ScheduleResultCardView.newBuilder()
                    .setHasResult(true)
                    .setRunId("r")
                    .setSessionId("s")
                    .setResultTitle("Original agent notification")
                    .setMarkdownBody("Original CLI summary")
                    .setShowConversationAction(true),
            )
            .build()
    private val sampleFormVm: io.github.lewin671.remoteagent.core.proto.ScheduleFormViewModel
        get() = io.github.lewin671.remoteagent.core.proto.ScheduleFormViewModel.newBuilder()
            .setFormKey("create:session:s")
            .setMode(io.github.lewin671.remoteagent.core.proto.ScheduleFormMode.SCHEDULE_FORM_MODE_CREATE_FOR_SESSION)
            .setOpen(true)
            .setMachineId("m")
            .setSessionId("s")
            .setEditable(true)
            .setNotifyOnFinish(true)
            .setSubmitLabel(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setKey("schedule.create"))
            .setTiming(
                io.github.lewin671.remoteagent.core.proto.ScheduleTimingDraftView.newBuilder()
                    .setKind(io.github.lewin671.remoteagent.core.proto.ScheduleTimingKind.SCHEDULE_TIMING_KIND_INTERVAL)
                    .setInterval("1")
                    .setUnit(io.github.lewin671.remoteagent.core.proto.ScheduleIntervalUnit.SCHEDULE_INTERVAL_UNIT_HOURS)
                    .setTimingDoneEnabled(true),
            )
            .build()
    @Before fun prepare() {
        remote.showForTest(state)
        remote.coreHost.showViewsForTest(
            schedules = sampleSchedulesVm,
            scheduleDetail = sampleDetailVm,
            scheduleForm = sampleFormVm,
        )
    }
    @After fun close() { remote.close(); Drafts.clear() }
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
        ui.onNodeWithText(text(UiMessages.settingsOpenSourceLicenses())).performScrollTo(); shot("about")
    }
    private fun machineSettingsFixture(): io.github.lewin671.remoteagent.core.proto.MachineSettingsViewModel {
        fun keyText(msg: UiMessage) = io.github.lewin671.remoteagent.core.proto.Text.newBuilder().apply {
            key = msg.key
            msg.args.forEach { (k, v) ->
                val arg = io.github.lewin671.remoteagent.core.proto.TextArg.newBuilder().setName(k)
                when (v) {
                    is Int -> arg.setIntValue(v.toLong())
                    is Long -> arg.setIntValue(v)
                    else -> arg.setStrValue(v.toString())
                }
                addArgs(arg)
            }
        }.build()
        fun lit(s: String) = io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral(s).build()
        return io.github.lewin671.remoteagent.core.proto.MachineSettingsViewModel.newBuilder()
            .setMachineId("m")
            .setName("Example computer")
            .setFound(true)
            .setMember(true)
            .setOnline(true)
            .setEditable(true)
            .addAvailableAgents(Agent.AGENT_CLAUDE)
            .setSelectedAgent(Agent.AGENT_CLAUDE)
            .setAgentDefaults(
                io.github.lewin671.remoteagent.core.proto.MachineAgentDefaultsView.newBuilder()
                    .setAgent(Agent.AGENT_CLAUDE)
                    .setAgentName("Claude Code")
                    .setSelectedModel("example-model")
                    .setSelectedModelLabel(lit("example-model"))
                    .setSelectedMode(ApprovalMode.APPROVAL_MODE_ASK)
                    .addModeChoices(
                        io.github.lewin671.remoteagent.core.proto.ModeChoiceView.newBuilder()
                            .setMode(ApprovalMode.APPROVAL_MODE_ASK)
                            .setLabel(keyText(UiMessages.modesAsk()))
                            .setSelected(true),
                    ),
            )
            .addApprovalTimeoutChoices(
                io.github.lewin671.remoteagent.core.proto.TimeoutChoiceView.newBuilder()
                    .setSeconds(600)
                    .setLabel(keyText(UiMessages.quotaMin(10)))
                    .setSelected(true),
            )
            .addIdleCloseChoices(
                io.github.lewin671.remoteagent.core.proto.IdleCloseChoiceView.newBuilder()
                    .setMinutes(360)
                    .setLabel(keyText(UiMessages.quotaHr(6)))
                    .setSelected(true),
            )
            .setAttachmentStorage(
                io.github.lewin671.remoteagent.core.proto.AttachmentStorageView.newBuilder()
                    .setLoaded(true)
                    .setUploadedText(lit("14.0 MiB"))
                    .setUploadedDetail(keyText(UiMessages.settingsCanClear("2.0 MiB")))
                    .setPreviewCacheText(lit("3.0 MiB"))
                    .setClearButtonLabel(keyText(UiMessages.settingsCanFree("5.0 MiB")))
                    .setCanClear(true)
                    .addQuotaChoices(
                        io.github.lewin671.remoteagent.core.proto.QuotaSizeChoiceView.newBuilder()
                            .setBytes(1L shl 30)
                            .setLabel(lit("1 GiB"))
                            .setSelected(true),
                    ),
            )
            .build()
    }
    @Test fun machine() {
        show { DeviceScreenContent(machineSettingsFixture()) }; shot("computer-settings")
        ui.onNodeWithText(text(UiMessages.settingsApprovalTimeout())).performScrollTo(); shot("daemon-settings")
        ui.onNodeWithText(text(UiMessages.settingsAttachmentStorage())).performScrollTo(); shot("computer-storage-row")
    }
    @Test fun machineStorage() {
        show { MachineAttachmentStorageScreenContent(machineSettingsFixture()) }
        ui.onAllNodesWithText(text(UiMessages.settingsPreviewCache())).onLast().performScrollTo(); shot("computer-storage")
    }
    @Test fun approvalTimeout() {
        show { DeviceScreenContent(machineSettingsFixture()) }
        ui.onNodeWithText(text(UiMessages.settingsApprovalTimeout())).performScrollTo().performClick(); shot("approval-timeout")
    }
    @Test fun idleClose() {
        show { DeviceScreenContent(machineSettingsFixture()) }
        ui.onNodeWithText(text(UiMessages.settingsAutomaticallyEndIdleSessions())).performScrollTo().performClick(); shot("idle-close")
    }
    @Test fun quota() { show { QuotaScreen("m") }; shot("quota") }
    @Test fun licenses() {
        show { LicensesPage {} }; shot("licenses")
        ui.onNodeWithText(text(UiMessages.settingsAndroidCoreLibrariesBsd3Clause())).performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("The Go Programming Language").performScrollTo().performClick()
        ui.onNodeWithText("Copyright 2009 The Go Authors.", substring = true).performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("Protocol Buffers for Go (google.golang.org/protobuf)").performScrollTo().performClick()
        ui.onNodeWithText("Copyright (c) 2018 The Go Authors.", substring = true).performScrollTo().assertIsDisplayed()
    }
    @Test fun storage() { show { PhoneImageStoragePage {} }; shot("phone-storage") }
    @Test fun devices() { show { DevicesScreen() }; shot("devices") }
    @Test fun client() { show { ClientScreen(client.id) }; shot("client") }
    @Test fun security() { show { Column { AccountSecurityContent(state.copy(recoveryPending = true, recoveryReadyMs = stamp + 172_800_000).toTestSecuritySummaryView(client.id)) } }; shot("security") }
    @Test fun securityMember() { show { Column { AccountSecurityContent(state.copy(isAuthority = false).toTestSecuritySummaryView(client.id)) } }; shot("security-member") }
    @Test fun recover() { show { RecoverScreen(appFixture.app) }; shot("recover") }
    @Test fun createAccount() { show { CreateAccountScreen(appFixture.app) }; shot("create-account") }
    @Test fun rotateRecovery() { show { RotateRecoveryContent(prepareCode = { error("Preview does not publish") }, publishCode = {}, onBack = {}) }; shot("rotate-recovery") }
    @Test fun recoveryStatus() { show { Column(Modifier.padding(20.dp)) { RecoveryStatus(GetRecoveryContextResponse.Phase.PHASE_READY, false, "", {}, {}, {}) } }; shot("recovery-ready") }
    @Test fun handoff() { show { HandoffPreparationContent(prepareCode = { "EXAMPLE RECOVERY CODE" }, publishOffer = {}) }; shot("handoff") }
    @Test fun join() { show { JoinScreen(appFixture.app) }; shot("join") }
    @Test fun removed() { show { RemovedScreen(appFixture.app, remote) }; shot("removed") }
    @Test fun pair() { show { PairScreen() }; shot("pair") }
    @Test fun startup() { show { AccountLoadFailure(text(UiMessages.errorsDisconnectedFromServer()), {}, {}) }; shot("startup-error") }
    @Test fun connection() {
        val vm = io.github.lewin671.remoteagent.core.proto.SettingsViewModel.newBuilder()
            .setConnCheck(
                io.github.lewin671.remoteagent.core.proto.ConnCheckSummaryView.newBuilder()
                    .setConnState(io.github.lewin671.remoteagent.core.proto.ConnState.CONN_STATE_OFFLINE)
                    .setNetworkAvailable(true),
            )
            .addMachines(
                io.github.lewin671.remoteagent.core.proto.MachineListCardView.newBuilder()
                    .setMachineId("m")
                    .setName("studio-dev")
                    .setOnline(true)
                    .setMember(true),
            )
            .build()
        show { ConnCheckSheet(vm, {}, "https://example.com", { listOf("m") }, { 750 }, { 1800 }, {}) }
        shot("connection")
    }
    @Test fun schedules() { show { SchedulesScreen() }; shot("schedules") }
    @Test fun scheduleDetail() { show { ScheduleDetailScreen("m", "t") }; shot("schedule-detail") }
    @Test fun scheduleEditor() { show { ScheduleEditorContent(sampleFormVm) }; shot("schedule-editor") }
    @Test fun scheduleMessage() {
        show {
            ScheduleMessageEditor(
                vm = io.github.lewin671.remoteagent.core.proto.ScheduleFormViewModel.newBuilder()
                    .setFormKey("f")
                    .setMessage("Original user message")
                    .setEditable(true)
                    .setCanSubmit(true)
                    .build(),
            )
        }
        shot("schedule-message")
    }
    @Test fun scheduleTime() {
        show { ScheduleTimeSheet(draft = sampleFormVm.timing, formKey = sampleFormVm.formKey) }
        shot("schedule-time")
    }
    @Test fun scheduleRuns() {
        val group = io.github.lewin671.remoteagent.core.proto.ScheduleHistoryGroupView.newBuilder()
            .setGroupKey("r")
            .addRuns(
                io.github.lewin671.remoteagent.core.proto.ScheduleRunRowView.newBuilder()
                    .setRunId("r")
                    .setUnreadOnEntry(true)
                    .setTimeText(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("09:00"))
                    .setSummaryText(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("Original agent notification")),
            )
            .build()
        show { Column { ScheduleRunGroup(group, onToggleQuiet = {}, openRun = {}) } }
        shot("schedule-results")
    }
    @Test fun scheduleStopped() {
        show { Column(Modifier.padding(20.dp)) { ScheduleAttentionBanner(text(UiMessages.scheduleAttentionUncertain("restart"))) } }
        shot("schedule-stopped")
    }
    @Test fun update() {
        @Suppress("UNCHECKED_CAST")
        val updates = Updater::class.java.getDeclaredField("_state").apply { isAccessible = true }.get(Updater) as MutableStateFlow<UpdateState>
        updates.value = UpdateState.Available(Release("99.0.0", 999999, PEER_PROTOCOL_VERSION + 1, "example.apk", "00".repeat(32), 42L shl 20, "Original release notes remain unchanged."))
        remote.coreHost.reportClientUpdateState(
            io.github.lewin671.remoteagent.core.proto.ClientUpdateStateEvent.newBuilder()
                .setAvailableVersion("99.0.0")
                .addReleases(
                    io.github.lewin671.remoteagent.core.proto.ReleaseCandidateInfo.newBuilder()
                        .setVersion("99.0.0")
                        .setProtocolChanged(true),
                )
                .build(),
        )
        remote.coreHost.openSettings()
        try {
            show { UpdateSheet {} }
            ui.waitUntil(3000) { remote.settingsView.value.update.hasUpdate }
            shot("update")
        } finally { updates.value = UpdateState.Unknown }
    }
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
        show {
            ScheduleMessageEditor(
                vm = io.github.lewin671.remoteagent.core.proto.ScheduleFormViewModel.newBuilder()
                    .setFormKey("f")
                    .setMessage("Original draft stays unchanged")
                    .setEditable(true)
                    .setCanSubmit(true)
                    .build(),
            )
        }
        ui.runOnIdle { language = if (locale == "en") "zh-CN" else "en" }
        ui.onAllNodes(hasSetTextAction()).onLast().assertTextEquals("Original draft stays unchanged")
    }
    @Test fun deferredErrorsAndPluralCounts() {
        val error = CommandException(CommandException.UNKNOWN, UiMessages.errorsTheRelayDidNotProvideAVerifiableExecutionResult().messageText(), failureKind = CommandFailureKind.CONNECTION_DROPPED)
        Assert.assertEquals(text(UiMessages.errorsTheRelayDidNotProvideAVerifiableExecutionResult()), displayError(error, copy = ::text))
        for (count in listOf(0, 1, 2, 1000)) {
            Assert.assertTrue(text(UiMessages.scheduleRunsTotal(count)).contains(count.toString()))
            Assert.assertTrue(text(UiMessages.scheduleEveryDays(count)).contains(count.toString()))
        }
        val large = Int.MAX_VALUE.toLong() + 1
        Assert.assertTrue(text(UiMessages.scheduleAndroidLargeEverySeconds(2, large.toString())).contains(large.toString()))
        Assert.assertEquals("原始 CLI / original CLI", displayError(Exception("原始 CLI / original CLI"), copy = ::text))
    }
}
