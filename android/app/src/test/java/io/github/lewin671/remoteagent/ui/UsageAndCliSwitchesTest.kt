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
import io.github.lewin671.remoteagent.core.proto.UsageBarBucketView
import io.github.lewin671.remoteagent.core.proto.UsageBarChartView
import io.github.lewin671.remoteagent.core.proto.UsageCompositionView
import io.github.lewin671.remoteagent.core.proto.UsageHeroView
import io.github.lewin671.remoteagent.core.proto.UsageMetricStripView
import io.github.lewin671.remoteagent.core.proto.UsageModelRowView
import io.github.lewin671.remoteagent.core.proto.UsageSortColumn
import io.github.lewin671.remoteagent.core.proto.UsageStripPointView
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

    private fun sampleRow(
        agent: Agent,
        agentName: String,
        model: String,
        totalTokens: Long,
        shareRatio: Double,
        selected: Boolean = false,
        hasCacheHit: Boolean = true,
        cacheHitRate: Double = 0.88,
        hasSpeed: Boolean = true,
        speedP50: Double = 72.0,
        hasTtft: Boolean = true,
        ttftP50Ms: Double = 520.0,
        hasDelta: Boolean = true,
        deltaPct: Double = 18.0,
    ): UsageModelRowView = UsageModelRowView.newBuilder()
        .setAgent(agent)
        .setAgentName(agentName)
        .setModel(model)
        .setSeriesKey("${agent.number}:$model")
        .setSelected(selected)
        .setShareRatio(shareRatio)
        .setTotalTokens(totalTokens)
        .setHasCacheHit(hasCacheHit)
        .setCacheHitRate(cacheHitRate)
        .setHasSpeed(hasSpeed)
        .setSpeedP50(speedP50)
        .setHasTtft(hasTtft)
        .setTtftP50Ms(ttftP50Ms)
        .setHasTokensDelta(hasDelta)
        .setTokensDeltaPercent(deltaPct)
        .setUsageRequestCount(22L)
        .setSpeedRequestCount(if (hasSpeed) 18L else 0L)
        .build()

    private fun sampleStrip(cur: Double, min: Double, max: Double, hasSelected: Boolean = false): UsageMetricStripView {
        val b = UsageMetricStripView.newBuilder()
            .setHasValue(true)
            .setCurrentValue(cur)
            .setHasRange(true)
            .setMinValue(min)
            .setMaxValue(max)
            .setScaleMin(min * 0.9)
            .setScaleMax(max * 1.1)
            .setLastValidIndex(1)
            .addAllPoints(UsageStripPointView.newBuilder().setHasValue(true).setValue(min))
            .addAllPoints(UsageStripPointView.newBuilder().setHasValue(true).setValue(max))
        if (hasSelected) {
            b.addSelectedPoints(UsageStripPointView.newBuilder().setHasValue(true).setValue(cur))
            b.addSelectedPoints(UsageStripPointView.newBuilder().setHasValue(true).setValue(cur))
        }
        return b.build()
    }

    @Test
    fun pureFormattersCustomRangeClampingTzOffsetAndSevenDayBuckets() {
        // Locale formatting: zh uses 万/亿 (keeping 1 decimal < 1000亿), en uses k/M/B
        assertEquals("9,999", formatUsageTokens(9_999L, "万", "亿", chinese = true))
        assertEquals("12.3万", formatUsageTokens(123_456L, "万", "亿", chinese = true))
        assertEquals("1亿", formatUsageTokens(99_990_000L, "万", "亿", chinese = true))
        assertEquals("1.5亿", formatUsageTokens(150_000_000L, "万", "亿", chinese = true))
        assertEquals("128.6亿", formatUsageTokens(12_860_000_000L, "万", "亿", chinese = true))
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
        assertEquals("0.4s", formatUsageTtftSeconds(420.0))
        assertEquals("1.2s", formatUsageTtftSeconds(1_240.0))
        assertEquals("12s", formatUsageTtftSeconds(12_100.0))

        val flat = messageTextForTest(UiMessages.usageFlat(), "zh-CN")
        assertEquals("▲1,240%", formatUsageDeltaTriangle(1240.0, flat))
        assertEquals("▼72%", formatUsageDeltaTriangle(-72.2, flat))
        assertEquals(flat, formatUsageDeltaTriangle(0.2, flat))

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

        // 7-day range produces 28 6-hour buckets (29 bounds)
        val sevenDays = buildUsageOpenParams(UsageRangePreset.USAGE_RANGE_PRESET_SEVEN_DAYS, nowMs = nowMs, zoneId = shanghai)
        assertEquals(29, sevenDays.bucketBoundsMs.size)

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
    fun usageScreenRendersAllFourMetricsFocusSortScrubEmptyAndEdgeStates() {
        val now = 1_760_000_000_000L
        val vm = UsageViewModel.newBuilder()
            .setMachineId("m1")
            .setMachineName("Mac Studio")
            .setFound(true)
            .setSupported(true)
            .setOnline(true)
            .setGranularity(UsageBucketGranularity.USAGE_BUCKET_GRANULARITY_HOUR)
            .setPreset(UsageRangePreset.USAGE_RANGE_PRESET_TODAY)
            .setRangeDays(1)
            .setSortBy(UsageSortColumn.USAGE_SORT_COLUMN_TOKENS)
            .setHasAnyUsage(true)
            .setHero(
                UsageHeroView.newBuilder()
                    .setTotalModelCount(3)
                    .setTotalTokens(720_000_000L)
                    .setRequestCount(862L)
                    .setHasTokensDelta(true)
                    .setTokensDeltaPercent(-72.0)
                    .setPeriodCompositionRows(5)
                    .setComposition(
                        UsageCompositionView.newBuilder()
                            .setUncachedInputTokens(42_260_000L)
                            .setCacheReadTokens(670_000_000L)
                            .setCacheWriteTokens(4_400_000L)
                            .setOutputTokens(3_240_000L)
                            .setReasoningOutputTokens(1_360_000L),
                    ),
            )
            .setBarChart(
                UsageBarChartView.newBuilder()
                    .setYMax(100_000_000L)
                    .setYMid(50_000_000L)
                    .addBuckets(
                        UsageBarBucketView.newBuilder()
                            .setStartMs(now - 7_200_000L)
                            .setEndMs(now - 3_600_000L)
                            .setAllTokens(45_000_000L)
                            .setHasData(true),
                    )
                    .addBuckets(
                        UsageBarBucketView.newBuilder()
                            .setStartMs(now - 3_600_000L)
                            .setEndMs(now)
                            .setAllTokens(85_000_000L)
                            .setHasData(true),
                    ),
            )
            .setCacheHitStrip(sampleStrip(0.94, 0.92, 0.96))
            .setOutputSpeedStrip(sampleStrip(95.0, 86.0, 112.0))
            .setTtftStrip(sampleStrip(900.0, 600.0, 1000.0))
            .addRows(sampleRow(Agent.AGENT_ANTIGRAVITY, "Antigravity", "gemini-flash-workhorse", 380_000_000L, 1.0, speedP50 = 118.0, ttftP50Ms = 400.0))
            .addRows(sampleRow(Agent.AGENT_CLAUDE, "Claude Code", "claude-opus-4-6-thinking", 16_800_000L, 0.04, speedP50 = 62.0, ttftP50Ms = 2300.0, deltaPct = -12.0))
            .addRows(sampleRow(Agent.AGENT_CODEX, "Codex", "gpt-5.4-codex-mini", 2_000_000L, 0.005, hasCacheHit = false, hasSpeed = false, hasTtft = false, hasDelta = false))
            .build()

        val vmState = mutableStateOf(vm)
        val pickedSorts = mutableListOf<UsageSortColumn>()
        val pickedSeries = mutableListOf<Pair<String, Boolean>>()
        val pickedPresets = mutableListOf<UsageRangePreset>()
        var retried = 0

        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalChartFrozen provides true) {
                    UsageScreenContent(
                        vm = vmState.value,
                        onSelectPreset = { pickedPresets += it },
                        onSelectSort = { pickedSorts += it },
                        onSelectSeries = { key, toggle -> pickedSeries += key to toggle },
                        onRetry = { retried++ },
                    )
                }
            }
        }
        ui.waitForIdle()

        // Hero & all 4 simultaneous strips rendered
        ui.onNodeWithText(messageTextForTest(UiMessages.usageScopeAllModels(3))).assertIsDisplayed()
        ui.onNodeWithText(messageTextForTest(UiMessages.usageStripCacheHit())).performScrollTo().assertIsDisplayed()
        ui.onNodeWithText(messageTextForTest(UiMessages.usageStripSpeed())).performScrollTo().assertIsDisplayed()
        ui.onNodeWithText(messageTextForTest(UiMessages.usageStripTtft())).performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("95").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("0.9s").performScrollTo().assertIsDisplayed()

        // Sort header & row selection callbacks
        ui.onNodeWithText(messageTextForTest(UiMessages.usageColTtft())).performScrollTo().performClick()
        ui.onNodeWithText("claude-opus-4-6-thinking").performScrollTo().performClick()
        ui.onNodeWithText(messageTextForTest(UiMessages.usageRange30D())).performScrollTo().performClick()
        ui.waitForIdle()
        assertEquals(listOf(UsageSortColumn.USAGE_SORT_COLUMN_TTFT), pickedSorts)
        assertEquals(listOf("${Agent.AGENT_CLAUDE_VALUE}:claude-opus-4-6-thinking" to true), pickedSeries)
        assertEquals(listOf(UsageRangePreset.USAGE_RANGE_PRESET_THIRTY_DAYS), pickedPresets)

        // Single-model focus + clear × button
        val focusedVm = vm.toBuilder()
            .setHero(
                vm.hero.toBuilder()
                    .setHasSelectedModel(true)
                    .setSelectedAgent(Agent.AGENT_CLAUDE)
                    .setSelectedAgentName("Claude Code")
                    .setSelectedModel("claude-opus-4-6-thinking")
                    .setSelectedSeriesKey("${Agent.AGENT_CLAUDE_VALUE}:claude-opus-4-6-thinking")
                    .setTotalTokens(16_800_000L)
                    .setHasSharePercent(true)
                    .setShareRatio(0.04),
            )
            .setCacheHitStrip(sampleStrip(0.90, 0.88, 0.92, hasSelected = true))
            .build()
        vmState.value = focusedVm
        ui.waitForIdle()
        ui.onNodeWithContentDescription(messageTextForTest(UiMessages.usageScopeClearA11Y()), useUnmergedTree = true).performClick()
        ui.waitForIdle()
        assertEquals("" to false, pickedSeries.last())

        // Minimal empty records state (s7)
        vmState.value = UsageViewModel.newBuilder()
            .setMachineId("m1")
            .setMachineName("VPS-dev")
            .setFound(true)
            .setSupported(true)
            .setOnline(true)
            .setPreset(UsageRangePreset.USAGE_RANGE_PRESET_TODAY)
            .setGranularity(UsageBucketGranularity.USAGE_BUCKET_GRANULARITY_HOUR)
            .setHasAnyUsage(false)
            .build()
        ui.waitForIdle()
        ui.onNodeWithText(messageTextForTest(UiMessages.usageScopeAllEmpty())).assertIsDisplayed()
        ui.onNodeWithText(messageTextForTest(UiMessages.usageEmptyNoRecords())).assertIsDisplayed()

        // Edge state: offline & error retry
        vmState.value = UsageViewModel.newBuilder()
            .setMachineId("m1")
            .setMachineName("Mac Studio")
            .setFound(true)
            .setSupported(true)
            .setOnline(true)
            .setErrorText(coreLit("boom"))
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
            .setOnline(true)
            .addSources(UsageAgentSource.newBuilder().setAgent(Agent.AGENT_ANTIGRAVITY).setState(UsageSourceState.USAGE_SOURCE_STATE_UNAVAILABLE))
            .build()
        ui.waitForIdle()
        assertTrue(ui.onAllNodesWithText(messageTextForTest(UiMessages.usageOnlyUnavailableTitle("Antigravity")), substring = true).fetchSemanticsNodes().isNotEmpty())
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
            assertTrue(view.hasAnyUsage)
            assertEquals(1, view.rowsCount)
            assertEquals("claude-sonnet-4-6", view.getRows(0).model)
        } finally {
            remote.close()
        }
    }
}
