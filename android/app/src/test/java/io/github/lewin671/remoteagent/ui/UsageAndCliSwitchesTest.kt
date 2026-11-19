package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performSemanticsAction
import io.github.lewin671.remoteagent.RaAppTestRule
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.Link
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.PROTOCOL_VERSION
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.buildUsageOpenParams
import io.github.lewin671.remoteagent.core.clampUsageCustomRange
import io.github.lewin671.remoteagent.core.localTzOffsetMinutes
import io.github.lewin671.remoteagent.core.proto.CliVersionView
import io.github.lewin671.remoteagent.core.proto.MachineAgentDefaultsView
import io.github.lewin671.remoteagent.core.proto.MachineCpuSection
import io.github.lewin671.remoteagent.core.proto.MachineDiskSection
import io.github.lewin671.remoteagent.core.proto.MachineMemorySection
import io.github.lewin671.remoteagent.core.proto.MachineModelUsagePreview
import io.github.lewin671.remoteagent.core.proto.MachineModelUsageRow
import io.github.lewin671.remoteagent.core.proto.MachinePollState
import io.github.lewin671.remoteagent.core.proto.MachineSettingsViewModel
import io.github.lewin671.remoteagent.core.proto.MachineStatusDot
import io.github.lewin671.remoteagent.core.proto.MachineStatusViewModel
import io.github.lewin671.remoteagent.core.proto.MachineTopUsageSection
import io.github.lewin671.remoteagent.core.proto.NewSessionNoticeView
import io.github.lewin671.remoteagent.core.proto.NoticeActionKind
import io.github.lewin671.remoteagent.core.proto.NoticeSeverity
import io.github.lewin671.remoteagent.core.proto.ScheduleAttentionView
import io.github.lewin671.remoteagent.core.proto.ScheduleDetailViewModel
import io.github.lewin671.remoteagent.core.proto.SessionActionsView
import io.github.lewin671.remoteagent.core.proto.SessionHeaderView
import io.github.lewin671.remoteagent.core.proto.SessionUsageEntryView
import io.github.lewin671.remoteagent.core.proto.SessionViewModel
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.core.proto.TextArg
import io.github.lewin671.remoteagent.core.proto.UsageMetricTab
import io.github.lewin671.remoteagent.core.proto.UsageSeriesView
import io.github.lewin671.remoteagent.core.proto.UsageViewModel
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.MachineCapabilities
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.UsageAgentSource
import io.github.lewin671.remoteagent.proto.UsageBucketGranularity
import io.github.lewin671.remoteagent.proto.UsageBucketPoint
import io.github.lewin671.remoteagent.proto.UsagePeriodSummary
import io.github.lewin671.remoteagent.proto.UsageRangePreset
import io.github.lewin671.remoteagent.proto.UsageSeries
import io.github.lewin671.remoteagent.proto.UsageSourceState
import io.github.lewin671.remoteagent.proto.UsageStatsResponse
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import java.time.ZoneId
import java.time.ZonedDateTime

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class UsageAndCliSwitchesTest {
    @get:Rule(order = 0) val appFixture = RaAppTestRule(preparePendingStore = true)
    @get:Rule(order = 1) val sessionMessages = SessionMessageContextRule()
    @get:Rule(order = 2) val ui = createComposeRule()
    @get:Rule(order = 3) val appJobs = appFixture.stopJobs { done ->
        ui.waitUntil(5000) { ui.mainClock.advanceTimeByFrame(); ui.waitForIdle(); done() }
    }

    private fun coreLit(s: String) = CoreText.newBuilder().setLiteral(s).build()

    private fun sampleSeries(
        agent: Agent,
        agentName: String,
        model: String,
        patternIndex: Int,
        totalOut: Long = 12_000L,
        speedReqs: Long = 10L,
        speedP50: Double = 64.0,
        ttftP50Ms: Double = 520.0,
        focused: Boolean = false,
        cacheWriteSupported: Boolean = true,
    ): UsageSeriesView {
        val now = 1_760_000_000_000L
        val b0 = UsageBucketPoint.newBuilder()
            .setStartMs(now - 2 * 86_400_000L)
            .setEndMs(now - 86_400_000L)
            .setUncachedInputTokens(30_000L)
            .setCacheReadTokens(80_000L)
            .setCacheWriteTokens(if (cacheWriteSupported) 10_000L else 0L)
            .setOutputTokens(totalOut / 2L)
            .setUsageRequestCount(12L)
            .setSpeedRequestCount(speedReqs / 2L)
            .setSpeedP10(speedP50 * 0.7)
            .setSpeedP50(speedP50)
            .setSpeedP90(speedP50 * 1.4)
            .setTtftP10Ms(ttftP50Ms * 0.6)
            .setTtftP50Ms(ttftP50Ms)
            .setTtftP90Ms(ttftP50Ms * 1.8)
            .build()
        val b1 = UsageBucketPoint.newBuilder()
            .setStartMs(now - 86_400_000L)
            .setEndMs(now)
            .setUncachedInputTokens(20_000L)
            .setCacheReadTokens(90_000L)
            .setCacheWriteTokens(if (cacheWriteSupported) 5_000L else 0L)
            .setOutputTokens(totalOut / 2L)
            .setUsageRequestCount(10L)
            .setSpeedRequestCount(speedReqs - speedReqs / 2L)
            .setSpeedP10(speedP50 * 0.75)
            .setSpeedP50(speedP50 + 6.0)
            .setSpeedP90(speedP50 * 1.35)
            .setTtftP10Ms(ttftP50Ms * 0.7)
            .setTtftP50Ms(ttftP50Ms - 40.0)
            .setTtftP90Ms(ttftP50Ms * 1.7)
            .build()
        return UsageSeriesView.newBuilder()
            .setAgent(agent)
            .setAgentName(agentName)
            .setModel(model)
            .setSeriesKey("${agent.number}:$model")
            .setPatternIndex(patternIndex)
            .setFocused(focused)
            .setCacheWriteSupported(cacheWriteSupported)
            .setFewSpeedSamples(speedReqs in 1L until 3L)
            .setCurrent(
                UsagePeriodSummary.newBuilder()
                    .setUncachedInputTokens(50_000L)
                    .setCacheReadTokens(170_000L)
                    .setCacheWriteTokens(if (cacheWriteSupported) 15_000L else 0L)
                    .setOutputTokens(totalOut)
                    .setReasoningOutputTokens(totalOut / 4L)
                    .setUsageRequestCount(22L)
                    .setSpeedRequestCount(speedReqs)
                    .setHasCacheHitRate(true)
                    .setCacheHitRate(0.72)
                    .setHasSpeed(speedReqs >= 3L)
                    .setSpeedP10(speedP50 * 0.7)
                    .setSpeedP50(speedP50)
                    .setSpeedP90(speedP50 * 1.4)
                    .setHasTtft(speedReqs >= 3L)
                    .setTtftP10Ms(ttftP50Ms * 0.65)
                    .setTtftP50Ms(ttftP50Ms)
                    .setTtftP90Ms(ttftP50Ms * 1.75)
                    .setRecent10SpeedCount(speedReqs.coerceAtMost(10L))
                    .setRecent10SpeedP50(speedP50 + 4.0)
                    .build(),
            )
            .setPrevious(
                UsagePeriodSummary.newBuilder()
                    .setUncachedInputTokens(40_000L)
                    .setCacheReadTokens(120_000L)
                    .setOutputTokens(8_000L)
                    .setUsageRequestCount(15L)
                    .setSpeedRequestCount(speedReqs)
                    .setHasCacheHitRate(true)
                    .setCacheHitRate(0.68)
                    .setHasSpeed(speedReqs >= 3L)
                    .setSpeedP50(speedP50 - 5.0)
                    .setHasTtft(speedReqs >= 3L)
                    .setTtftP50Ms(ttftP50Ms + 150.0)
                    .build(),
            )
            .setHasTokensDelta(true)
            .setTokensDeltaPercent(18.0)
            .setHasCacheHitDelta(true)
            .setCacheHitDeltaPoints(4.0)
            .setHasSpeedDelta(speedReqs >= 3L)
            .setSpeedDeltaPercent(9.0)
            .setHasTtftDelta(speedReqs >= 3L)
            .setTtftDeltaPercent(-22.0)
            .addBuckets(b0)
            .addBuckets(b1)
            .build()
    }

    @Test
    fun pureFormattersCustomRangeClampingTzOffsetAndOverlayReplacement() {
        // Locale formatting: zh uses 万/亿, en uses k/M/B
        assertEquals("9,999", formatUsageTokens(9_999L, "万", "亿", chinese = true))
        assertEquals("12.3万", formatUsageTokens(123_456L, "万", "亿", chinese = true))
        assertEquals("1亿", formatUsageTokens(99_990_000L, "万", "亿", chinese = true))
        assertEquals("1.5亿", formatUsageTokens(150_000_000L, "万", "亿", chinese = true))
        assertEquals("999", formatUsageTokens(999L, "10k", "100M", chinese = false))
        assertEquals("12.3k", formatUsageTokens(12_345L, "10k", "100M", chinese = false))
        assertEquals("1.5M", formatUsageTokens(1_500_000L, "10k", "100M", chinese = false))
        assertEquals("2.4B", formatUsageTokens(2_400_000_000L, "10k", "100M", chinese = false))

        assertEquals("0%", formatUsageCacheHit(0.0))
        assertEquals("0.4%", formatUsageCacheHit(0.004))
        assertEquals("84%", formatUsageCacheHit(0.842))
        assertEquals("99.4%", formatUsageCacheHit(0.994))
        assertEquals("100%", formatUsageCacheHit(1.0))

        assertEquals("6.8", formatUsageSpeed(6.82))
        assertEquals("124", formatUsageSpeed(124.2))
        assertEquals("420 ms", formatUsageTtft(420.0))
        assertEquals("1.2 s", formatUsageTtft(1_240.0))
        assertEquals("12 s", formatUsageTtft(12_100.0))

        val flat = messageTextForTest(UiMessages.usageFlat(), "zh-CN")
        assertEquals("+12%", formatUsageSignedPercent(12.4, flat))
        assertEquals("−8%", formatUsageSignedPercent(-8.2, flat))
        assertEquals(flat, formatUsageSignedPercent(0.2, flat))
        assertEquals("+4 pt", formatUsageSignedPoints(3.7, flat))
        assertEquals("−3 pt", formatUsageSignedPoints(-3.2, flat))
        assertEquals("−180 ms", formatUsageTtftDelta(320.0, 500.0, flat))
        assertEquals("+1.4 s", formatUsageTtftDelta(2400.0, 1000.0, flat))
        assertEquals(flat, formatUsageTtftDelta(1020.0, 1000.0, flat))

        // East-of-UTC timezone offset sign
        val shanghai = ZoneId.of("Asia/Shanghai")
        val la = ZoneId.of("America/Los_Angeles")
        val nowMs = ZonedDateTime.of(2026, 7, 1, 14, 0, 0, 0, la).toInstant().toEpochMilli()
        assertEquals(480, localTzOffsetMinutes(nowMs, shanghai))
        assertEquals(-420, localTzOffsetMinutes(nowMs, la))

        // 90-day clamping & reversed range normalization
        val tooOldMs = nowMs - 120L * 86_400_000L
        val futureMs = nowMs + 10L * 86_400_000L
        val (cStart, cEnd) = clampUsageCustomRange(tooOldMs, futureMs, nowMs, shanghai)
        val minExpected = ZonedDateTime.of(2026, 7, 2, 5, 0, 0, 0, shanghai).toLocalDate()
            .minusDays(89).atStartOfDay(shanghai).toInstant().toEpochMilli()
        val maxExpected = ZonedDateTime.of(2026, 7, 2, 5, 0, 0, 0, shanghai).toLocalDate()
            .atStartOfDay(shanghai).toInstant().toEpochMilli()
        assertEquals(minExpected, cStart)
        assertEquals(maxExpected, cEnd)

        // DST transition day bucket generation in America/Los_Angeles (spring forward 2026-03-08)
        val dstNow = ZonedDateTime.of(2026, 3, 8, 12, 0, 0, 0, la).toInstant().toEpochMilli()
        val dstToday = buildUsageOpenParams(UsageRangePreset.USAGE_RANGE_PRESET_TODAY, nowMs = dstNow, zoneId = la)
        assertEquals(24, dstToday.bucketBoundsMs.size) // 23 hours + end bound

        val twoDaysEnd = ZonedDateTime.of(2026, 7, 1, 0, 0, 0, 0, shanghai).toInstant().toEpochMilli()
        val twoDaysStart = twoDaysEnd - 86_400_000L
        val sixHourParams = buildUsageOpenParams(
            UsageRangePreset.USAGE_RANGE_PRESET_CUSTOM,
            customStartMs = twoDaysStart,
            customEndMs = twoDaysEnd,
            nowMs = twoDaysEnd + 3_600_000L,
            zoneId = shanghai,
        )
        assertEquals(9, sixHourParams.bucketBoundsMs.size) // 2 days * 4 quarters + 1

        // Max-4 overlay replacement
        var sel = listOf("a", "b", "c")
        sel = toggleUsageOverlaySelection(sel, "d")
        assertEquals(listOf("a", "b", "c", "d"), sel)
        sel = toggleUsageOverlaySelection(sel, "e")
        assertEquals(listOf("a", "b", "c", "e"), sel)
        sel = toggleUsageOverlaySelection(sel, "e")
        assertEquals(listOf("a", "b", "c"), sel)
        assertEquals(listOf("a"), toggleUsageOverlaySelection(listOf("a"), "a"))
    }

    @Test
    fun cliSwitchesRenderToggleLockConflictAndLegacyFallback() {
        val toggles = mutableListOf<Pair<Agent, Boolean>>()
        val conflictBanner = "该电脑设置刚被其他端更新，已同步最新状态。"
        val claudeDefaults = MachineAgentDefaultsView.newBuilder()
            .setAgent(Agent.AGENT_CLAUDE)
            .setAgentName("Claude Code")
            .setSelectedModel("claude-sonnet-4-6")
            .setSelectedModelLabel(coreLit("Sonnet 4.6"))
            .build()
        val cliVm = MachineSettingsViewModel.newBuilder()
            .setMachineId("m1")
            .setName("Mac Studio")
            .setFound(true)
            .setOnline(true)
            .setEditable(true)
            .setShowCliSwitches(true)
            .setCliSwitchesEditable(true)
            .setCliSwitchesConflictBanner(coreLit(conflictBanner))
            .addCliVersions(
                CliVersionView.newBuilder()
                    .setAgent(Agent.AGENT_CLAUDE)
                    .setName("Claude Code")
                    .setVersion("1.0.50")
                    .setVerified(true)
                    .setEnabled(true)
                    .setLockedLastEnabled(true)
                    .setDefaults(claudeDefaults),
            )
            .addCliVersions(
                CliVersionView.newBuilder()
                    .setAgent(Agent.AGENT_CODEX)
                    .setName("Codex")
                    .setVersion("0.20.0")
                    .setVerified(true)
                    .setEnabled(false)
                    .setLockedLastEnabled(false),
            )
            .addAvailableAgents(Agent.AGENT_CLAUDE)
            .setSelectedAgent(Agent.AGENT_CLAUDE)
            .setAgentDefaults(claudeDefaults)
            .build()
        val legacyVm = MachineSettingsViewModel.newBuilder()
            .setMachineId("m2")
            .setName("Old Daemon")
            .setFound(true)
            .setOnline(true)
            .setShowCliSwitches(false)
            .addCliVersions(
                CliVersionView.newBuilder()
                    .setAgent(Agent.AGENT_CLAUDE)
                    .setName("Claude Code")
                    .setVersion("1.0.10")
                    .setVerified(true)
                    .setEnabled(true),
            )
            .build()

        val stateVm = mutableStateOf(cliVm)
        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalNav provides nav, LocalChartFrozen provides true) {
                    DeviceScreenContent(
                        vm = stateVm.value,
                        onToggleAgent = { agent, enabled -> toggles += agent to enabled },
                    )
                }
            }
        }
        ui.waitForIdle()

        val enableLabel = messageTextForTest(UiMessages.settingsCliEnableOnComputer())
        val keepOneText = messageTextForTest(UiMessages.settingsCliSwitchesKeepOne())
        val installedLabel = messageTextForTest(UiMessages.settingsCliInstalledVersion())
        val defaultsSection = messageTextForTest(UiMessages.settingsNewProjectDefaults())
        val lockedA11y = messageTextForTest(UiMessages.settingsCliSwitchesA11YLocked("Claude Code", "1.0.50"))
        val codexA11y = messageTextForTest(
            UiMessages.settingsCliSwitchesA11YRow(
                name = "Codex",
                state = messageTextForTest(UiMessages.settingsCliSwitchesStateOff()),
                version = "0.20.0",
            ),
        )

        ui.onNodeWithText(conflictBanner).performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("Claude Code").performScrollTo().performClick()
        ui.waitForIdle()
        ui.onNodeWithContentDescription(lockedA11y).assertIsNotEnabled()
        ui.onNodeWithText(keepOneText).assertIsDisplayed()
        ui.onNodeWithText(defaultsSection).assertIsDisplayed()
        ui.onNodeWithText(messageTextForTest(UiMessages.commonDone())).performClick()
        ui.waitForIdle()

        ui.onNodeWithText("Codex").performScrollTo().performClick()
        ui.waitForIdle()
        ui.onNodeWithText(messageTextForTest(UiMessages.settingsCliDisabledExplanation())).assertIsDisplayed()
        ui.onNodeWithContentDescription(codexA11y).performClick()
        ui.waitForIdle()
        assertEquals(listOf(Agent.AGENT_CODEX to true), toggles)
        ui.onNodeWithText(messageTextForTest(UiMessages.commonDone())).performClick()
        ui.waitForIdle()

        // Single-CLI or legacy pre-v17 daemon hides the enable switch inside CLI settings sheet.
        stateVm.value = legacyVm
        ui.waitForIdle()
        ui.onNodeWithText("Claude Code").performScrollTo().performClick()
        ui.waitForIdle()
        ui.onNodeWithText(installedLabel).assertIsDisplayed()
        ui.onNodeWithText("1.0.10").assertIsDisplayed()
        assertEquals(0, ui.onAllNodesWithText(enableLabel).fetchSemanticsNodes().size)
    }

    @Test
    fun newSessionEnableAgentAndScheduleEnableCliRouteToMachineSettings() {
        val noticeActions = mutableListOf<NoticeActionKind>()
        var openedSettingsMachine = ""
        val notice = NewSessionNoticeView.newBuilder()
            .setSeverity(NoticeSeverity.NOTICE_SEVERITY_WARN)
            .setText(coreLit("Codex 已在这台电脑关闭"))
            .setAction(NoticeActionKind.NOTICE_ACTION_KIND_ENABLE_AGENT)
            .setTargetMachineId("m1")
            .build()
        val schedVm = ScheduleDetailViewModel.newBuilder()
            .setFound(true)
            .setMachineId("m1")
            .setTaskId("t1")
            .setTitle("日报")
            .setAttention(
                ScheduleAttentionView.newBuilder()
                    .setShow(true)
                    .setMessage(
                        CoreText.newBuilder()
                            .setKey("schedule.agent_disabled_pre_run")
                            .addArgs(TextArg.newBuilder().setName("agent").setStrValue("Codex"))
                            .addArgs(TextArg.newBuilder().setName("machine").setStrValue("Mac Studio"))
                            .build(),
                    ),
            )
            .build()

        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Column(Modifier.verticalScroll(rememberScrollState())) {
                    NewSessionNoticeRow(
                        n = notice,
                        onAction = { noticeActions += it },
                        onChooseOfflineMachine = {},
                    )
                    ScheduleAttentionBanner(
                        text = "Codex 已在这台电脑关闭",
                        action = messageTextForTest(UiMessages.scheduleEnableCli()),
                        onAction = { openedSettingsMachine = schedVm.machineId },
                    )
                }
            }
        }
        ui.waitForIdle()

        ui.onNodeWithText(messageTextForTest(UiMessages.newSessionEnableAgent())).performClick()
        ui.onNodeWithText(messageTextForTest(UiMessages.scheduleEnableCli())).performClick()
        ui.waitForIdle()

        assertEquals(listOf(NoticeActionKind.NOTICE_ACTION_KIND_ENABLE_AGENT), noticeActions)
        assertEquals("m1", openedSettingsMachine)
    }

    @Test
    fun machineStatusModelUsagePreviewAndSessionUsageEntryNavigateToUsage() {
        val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "test"), Api.defaultHttp())
        val openedFromStatus = mutableListOf<Pair<Agent, String>>()
        var closedSheet = false
        var openedFromSession = false
        try {
            val statusVm = MachineStatusViewModel.newBuilder()
                .setMachineId("m1")
                .setMachineName("Mac Studio")
                .setFound(true)
                .setSupported(true)
                .setOnline(true)
                .setPollState(MachinePollState.MACHINE_POLL_STATE_LIVE)
                .setStatusDot(MachineStatusDot.MACHINE_STATUS_DOT_GREEN_LIVE)
                .setCpu(MachineCpuSection.getDefaultInstance())
                .setMemory(MachineMemorySection.getDefaultInstance())
                .setDisk(MachineDiskSection.getDefaultInstance())
                .setTopUsage(MachineTopUsageSection.getDefaultInstance())
                .setModelUsage(
                    MachineModelUsagePreview.newBuilder()
                        .setSupported(true)
                        .setHasData(true)
                        .addRows(
                            MachineModelUsageRow.newBuilder()
                                .setAgent(Agent.AGENT_CLAUDE)
                                .setAgentName("Claude Code")
                                .setModel("claude-sonnet-4-6")
                                .setTotalTokens(125_000L)
                                .setHasCacheHit(true)
                                .setCacheHitRate(0.82)
                                .setHasSpeed(true)
                                .setSpeedP50(68.0)
                                .setUsageRequestCount(14L)
                                .setSpeedRequestCount(12L),
                        ),
                )
                .build()
            val sessionVm = SessionViewModel.newBuilder()
                .setSessionId("s1")
                .setHeader(SessionHeaderView.newBuilder().setMachineId("m1").setAgent(Agent.AGENT_CLAUDE).setModel("claude-sonnet-4-6"))
                .setActions(
                    SessionActionsView.newBuilder()
                        .setCanSwitchModel(false)
                        .setUsageEntry(
                            SessionUsageEntryView.newBuilder()
                                .setVisible(true)
                                .setSupported(true)
                                .setHasData(true)
                                .setSubtitle(coreLit("最近 7 天"))
                                .setValueText(coreLit("12.5万 · 82% · 68 tok/s"))
                                .setFootnote(coreLit("统计所有本地对话")),
                        ),
                )
                .build()

            val showSheet = mutableStateOf(false)
            ui.setContent {
                RaTheme(ThemeMode.LIGHT) {
                    CompositionLocalProvider(LocalRemote provides remote, LocalChartFrozen provides true) {
                        if (!showSheet.value) {
                            MachineStatusContent(
                                view = statusVm,
                                main = StatusMetric.CPU,
                                onMain = {},
                                onScrub = { _, _, _ -> },
                                onSelectTab = {},
                                onBack = {},
                                onRetry = {},
                                onOpenStorage = {},
                                onCleanup = {},
                                onSession = {},
                                onOpenUsage = { agent, model -> openedFromStatus += agent to model },
                            )
                        } else {
                            ModelSheet(
                                sid = "s1",
                                vm = sessionVm,
                                onClose = { closedSheet = true },
                                onOpenUsage = { openedFromSession = true },
                            )
                        }
                    }
                }
            }
            ui.waitForIdle()

            ui.onNodeWithText(messageTextForTest(UiMessages.machineStatusModelUsageTitle())).performScrollTo().assertIsDisplayed()
            ui.onNodeWithText("Claude Code · claude-sonnet-4-6").performScrollTo().performClick()
            ui.onNodeWithText(messageTextForTest(UiMessages.machineStatusModelUsageViewTrends())).performScrollTo().performClick()
            ui.waitForIdle()
            assertEquals(
                listOf(Agent.AGENT_CLAUDE to "claude-sonnet-4-6", Agent.AGENT_UNSPECIFIED to ""),
                openedFromStatus,
            )

            showSheet.value = true
            ui.waitForIdle()
            val entryTitle = messageTextForTest(UiMessages.sessionUsageEntryTitle())
            ui.onNodeWithContentDescription("$entryTitle · 最近 7 天 · 12.5万 · 82% · 68 tok/s").performScrollTo().performClick()
            ui.waitForIdle()
            assertTrue(closedSheet)
            assertTrue(openedFromSession)
        } finally {
            remote.close()
        }
    }

    @Test
    fun usageScreenRendersMetricsSeriesOverlayCapAndEdgeStates() {
        val fiveSeries = UsageViewModel.newBuilder()
            .setMachineId("m1")
            .setMachineName("Mac Studio")
            .setFound(true)
            .setSupported(true)
            .setOnline(true)
            .setGranularity(UsageBucketGranularity.USAGE_BUCKET_GRANULARITY_DAY)
            .setPreset(UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS)
            .setMetric(UsageMetricTab.USAGE_METRIC_TAB_TOKENS)
            .addSeries(sampleSeries(Agent.AGENT_CLAUDE, "Claude Code", "claude-sonnet-4-6", 0, totalOut = 10_000L, focused = true))
            .addSeries(sampleSeries(Agent.AGENT_CODEX, "Codex", "gpt-5", 1, totalOut = 9_000L, cacheWriteSupported = false))
            .addSeries(sampleSeries(Agent.AGENT_CLAUDE, "Claude Code", "claude-opus-4-6", 2, totalOut = 8_000L))
            .addSeries(sampleSeries(Agent.AGENT_CODEX, "Codex", "o4-mini", 3, totalOut = 7_000L, speedReqs = 2L))
            .addSeries(sampleSeries(Agent.AGENT_ANTIGRAVITY, "Antigravity", "gemini-2.5-pro", 0, totalOut = 6_000L))
            .build()

        val vmState = mutableStateOf(fiveSeries)
        val pickedMetrics = mutableListOf<UsageMetricTab>()
        val pickedPresets = mutableListOf<UsageRangePreset>()
        var retried = 0

        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalChartFrozen provides true) {
                    UsageScreenContent(
                        vm = vmState.value,
                        onSelectMetric = {
                            pickedMetrics += it
                            vmState.value = vmState.value.toBuilder().setMetric(it).build()
                        },
                        onSelectPreset = { pickedPresets += it },
                        onRetry = { retried++ },
                    )
                }
            }
        }
        ui.waitForIdle()

        // Initially 3 of 5 selected
        ui.onNodeWithText(messageTextForTest(UiMessages.usagePairsSummary(count = 5, name = "Mac Studio"))).assertIsDisplayed()

        // Selecting 4th series reaches 4; selecting 5th series replaces 4th and triggers capped summary
        ui.onNodeWithText("Codex · o4-mini").performScrollTo().performClick()
        ui.onNodeWithText("Antigravity · gemini-2.5-pro").performScrollTo().performClick()
        ui.waitForIdle()
        ui.onNodeWithText(messageTextForTest(UiMessages.usagePairsCappedSummary(5))).performScrollTo().assertIsDisplayed()

        // Switch through metric tabs
        ui.onNodeWithText(messageTextForTest(UiMessages.usageTabCacheHit())).performScrollTo().performClick()
        ui.onNodeWithText(messageTextForTest(UiMessages.usageTabSpeed())).performScrollTo().performClick()
        ui.onNodeWithText(messageTextForTest(UiMessages.usageTabTtft())).performScrollTo().performClick()
        ui.onNodeWithText(messageTextForTest(UiMessages.usageRange30D())).performScrollTo().performClick()
        ui.waitForIdle()
        assertEquals(
            listOf(
                UsageMetricTab.USAGE_METRIC_TAB_CACHE_HIT,
                UsageMetricTab.USAGE_METRIC_TAB_OUTPUT_SPEED,
                UsageMetricTab.USAGE_METRIC_TAB_TTFT,
            ),
            pickedMetrics,
        )
        assertEquals(listOf(UsageRangePreset.USAGE_RANGE_PRESET_THIRTY_DAYS), pickedPresets)

        // Edge states: offline empty & retry
        vmState.value = UsageViewModel.newBuilder()
            .setMachineId("m1")
            .setMachineName("Mac Studio")
            .setFound(true)
            .setSupported(true)
            .setOnline(false)
            .setCachedOffline(true)
            .build()
        ui.waitForIdle()
        ui.onNodeWithText(messageTextForTest(UiMessages.commonRetry())).performClick()
        ui.waitForIdle()
        assertEquals(1, retried)

        // Edge state: unsupported daemon
        vmState.value = UsageViewModel.newBuilder()
            .setMachineId("m1")
            .setMachineName("Mac Studio")
            .setFound(true)
            .setSupported(false)
            .build()
        ui.waitForIdle()
        assertTrue(ui.onAllNodesWithText(messageTextForTest(UiMessages.usageUnsupportedTitle()), substring = true).fetchSemanticsNodes().isNotEmpty())

        // Edge state: all sources unavailable
        vmState.value = UsageViewModel.newBuilder()
            .setMachineId("m1")
            .setMachineName("Mac Studio")
            .setFound(true)
            .setSupported(true)
            .addSources(UsageAgentSource.newBuilder().setAgent(Agent.AGENT_ANTIGRAVITY).setState(UsageSourceState.USAGE_SOURCE_STATE_UNAVAILABLE))
            .build()
        ui.waitForIdle()
        assertTrue(ui.onAllNodesWithText(messageTextForTest(UiMessages.usageOnlyUnavailableTitle("Antigravity")), substring = true).fetchSemanticsNodes().isNotEmpty())

        // Zero speed samples (e.g. Antigravity): Speed tab shows "—" cleanly without few-samples notice
        vmState.value = UsageViewModel.newBuilder()
            .setMachineId("m1")
            .setMachineName("Mac Studio")
            .setFound(true)
            .setSupported(true)
            .setOnline(true)
            .setGranularity(UsageBucketGranularity.USAGE_BUCKET_GRANULARITY_DAY)
            .setPreset(UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS)
            .setMetric(UsageMetricTab.USAGE_METRIC_TAB_OUTPUT_SPEED)
            .addSeries(
                sampleSeries(
                    Agent.AGENT_ANTIGRAVITY,
                    "Antigravity",
                    "gemini-4-hydrogen",
                    0,
                    speedReqs = 0L,
                    speedP50 = 0.0,
                    ttftP50Ms = 0.0,
                    cacheWriteSupported = false,
                ).toBuilder().apply {
                    current = current.toBuilder().setHasSpeed(false).setHasTtft(false).build()
                }.build(),
            )
            .build()
        ui.waitForIdle()
        assertEquals(0, ui.onAllNodesWithText(messageTextForTest(UiMessages.usageFewSamplesCount(0)), substring = true).fetchSemanticsNodes().size)
        assertEquals(0, ui.onAllNodesWithText(messageTextForTest(UiMessages.usageFewSamplesHint()), substring = true).fetchSemanticsNodes().size)
    }

    @Test
    fun coreHostDeliversCachedUsageSnapshotWhenOffline() {
        val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "test"), Api.defaultHttp())
        try {
            val offlineMachine = MachineView(
                id = "m1",
                online = false,
                lastSeenMs = 0L,
                meta = MachineMeta.newBuilder()
                    .setHostname("Mac Studio")
                    .setProtocolVersion(PROTOCOL_VERSION)
                    .setCapabilities(
                        MachineCapabilities.newBuilder()
                            .setUsageStats(true)
                            .setCliSwitches(true),
                    )
                    .build(),
            )
            remote.showForTest(
                RemoteState(
                    conn = ConnState.ONLINE,
                    link = Link.ONLINE,
                    loaded = true,
                    synced = true,
                    machines = mapOf("m1" to offlineMachine),
                ),
            )
            val snap = UsageStatsResponse.newBuilder()
                .setPreset(UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS)
                .setGranularity(UsageBucketGranularity.USAGE_BUCKET_GRANULARITY_DAY)
                .setGeneratedAtMs(1_760_000_000_000L)
                .addSeries(
                    UsageSeries.newBuilder()
                        .setAgent(Agent.AGENT_CLAUDE)
                        .setModel("claude-sonnet-4-6")
                        .setCacheWriteSupported(true)
                        .setCurrent(
                            UsagePeriodSummary.newBuilder()
                                .setOutputTokens(12_000L)
                                .setUsageRequestCount(6L),
                        ),
                )
                .build()

            remote.coreHost.deliverUsageCacheLoaded("m1", snap)
            val view = remote.coreHost.snapshotUsageView(
                machineId = "m1",
                preset = UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS,
            )
            assertTrue(view.found)
            assertTrue(view.cachedOffline)
            assertFalse(view.online)
            assertEquals(1, view.seriesCount)
            assertEquals("claude-sonnet-4-6", view.getSeries(0).model)
        } finally {
            remote.close()
        }
    }
}
