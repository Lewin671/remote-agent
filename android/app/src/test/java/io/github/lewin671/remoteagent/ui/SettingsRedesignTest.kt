package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.assertIsOff
import androidx.compose.ui.test.assertIsOn
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onLast
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.AttachmentStorageView
import io.github.lewin671.remoteagent.core.proto.CliVersionView
import io.github.lewin671.remoteagent.core.proto.ClientRowView
import io.github.lewin671.remoteagent.core.proto.ConnCheckSummaryView
import io.github.lewin671.remoteagent.core.proto.ConnProbeState
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupStage
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupTone
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupView
import io.github.lewin671.remoteagent.core.proto.DevicesViewModel
import io.github.lewin671.remoteagent.core.proto.DiskReminderView
import io.github.lewin671.remoteagent.core.proto.EffortChoiceView
import io.github.lewin671.remoteagent.core.proto.IdleCloseChoiceView
import io.github.lewin671.remoteagent.core.proto.MachineAgentDefaultsView
import io.github.lewin671.remoteagent.core.proto.MachineAlertKind
import io.github.lewin671.remoteagent.core.proto.MachineCardAlert
import io.github.lewin671.remoteagent.core.proto.MachineCardQuotaItem
import io.github.lewin671.remoteagent.core.proto.MachineListCardView
import io.github.lewin671.remoteagent.core.proto.MachineManagementActionsView
import io.github.lewin671.remoteagent.core.proto.MachineRingKind
import io.github.lewin671.remoteagent.core.proto.MachineRingTone
import io.github.lewin671.remoteagent.core.proto.MachineRingView
import io.github.lewin671.remoteagent.core.proto.MachineSettingsViewModel
import io.github.lewin671.remoteagent.core.proto.MachineStatusDot
import io.github.lewin671.remoteagent.core.proto.ManageProjectsView
import io.github.lewin671.remoteagent.core.proto.ModeChoiceView
import io.github.lewin671.remoteagent.core.proto.ModelChoiceView
import io.github.lewin671.remoteagent.core.proto.NewSessionNoticeView
import io.github.lewin671.remoteagent.core.proto.NoticeActionKind
import io.github.lewin671.remoteagent.core.proto.NoticeSeverity
import io.github.lewin671.remoteagent.core.proto.NotificationsView
import io.github.lewin671.remoteagent.core.proto.ProjectListingView
import io.github.lewin671.remoteagent.core.proto.ProjectRowView
import io.github.lewin671.remoteagent.core.proto.QuotaCliSection
import io.github.lewin671.remoteagent.core.proto.QuotaSizeChoiceView
import io.github.lewin671.remoteagent.core.proto.SecuritySummaryView
import io.github.lewin671.remoteagent.core.proto.SettingsViewModel
import io.github.lewin671.remoteagent.core.proto.Text
import io.github.lewin671.remoteagent.core.proto.TimeoutChoiceView
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.push.PushStatus
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class SettingsRedesignTest {
    @get:Rule val resources = SessionMessageContextRule()
    @get:Rule val ui = createComposeRule()

    private fun lit(s: String): Text = Text.newBuilder().setLiteral(s).build()

    @Test
    fun settingsFourGroupsBannersAndUnreachablePushOverride() {
        val vm = SettingsViewModel.newBuilder()
            .setOnline(true)
            .setIsManager(true)
            .setRecoveryPending(true)
            .setRecoveryBannerText(lit("待处理的管理权恢复请求"))
            .setClockSkewed(true)
            .setClockSkewBannerText(lit("设备时钟落后"))
            .setUnlinked(true)
            .setUnlinkedBannerText(lit("此客户端已解除配对"))
            .setCanAddDevice(true)
            .setMachineCountText(lit("3 台"))
            .addMachines(
                MachineListCardView.newBuilder()
                    .setMachineId("m1")
                    .setName("StudioBox")
                    .setOnline(true)
                    .setMember(true)
                    .setDirectActive(true)
                    .setStatusDot(MachineStatusDot.MACHINE_STATUS_DOT_GREEN_LIVE)
                    .setStatusText(lit("在线"))
                    .addAlerts(
                        MachineCardAlert.newBuilder()
                            .setKind(MachineAlertKind.MACHINE_ALERT_KIND_DISK_LOW)
                            .setText(lit("磁盘剩 4.2 GB"))
                            .setWarn(true),
                    )
                    .addQuotaItems(
                        MachineCardQuotaItem.newBuilder()
                            .setAgent(Agent.AGENT_CODEX)
                            .setAgentName("Codex")
                            .setRemainingPercent(8)
                            .setWarn(true)
                            .setValueText(lit("剩 8%")),
                    ),
            )
            .setNotifications(
                NotificationsView.newBuilder()
                    .setNotifyAttention(true)
                    .setSummaryText(lit("开启"))
                    .setSummaryWarn(false),
            )
            .setClientsSummaryText(lit("2 台 · 1 台待完成"))
            .setClientsSummaryWarn(true)
            .setSecurity(SecuritySummaryView.newBuilder().setSummaryText(lit("这台设备管理账户")))
            .setConnCheck(ConnCheckSummaryView.newBuilder().setServerState(ConnProbeState.CONN_PROBE_STATE_WORKING))
            .setCanExitAccount(true)
            .build()

        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalChartFrozen provides true) {
                    SettingsScreenContent(
                        vm = vm,
                        push = PushStatus(problem = "系统通知权限已关闭", action = "去开启"),
                        serverDisplay = "example.com",
                        localCacheText = "8.4 MiB",
                    )
                }
            }
        }

        // Top banners present; long push troubleshooting absent from main Settings screen.
        ui.onNodeWithText("待处理的管理权恢复请求").assertIsDisplayed()
        ui.onNodeWithText("设备时钟落后").assertIsDisplayed()
        ui.onNodeWithText("此客户端已解除配对").assertIsDisplayed()
        ui.onAllNodesWithText("系统通知权限已关闭", substring = true).assertCountEquals(0)

        // Group 1: 电脑 (Plan B standalone card + direct + alert + quota bar + standalone add row)
        ui.onNodeWithText("电脑").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("3 台").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("StudioBox").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("直连").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("磁盘剩 4.2 GB").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("Codex").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("剩 8%").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("添加设备").performScrollTo().assertIsDisplayed()

        // Group 2: 本机 with unreachable push override
        ui.onNodeWithText("本机").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("收不到推送").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("8.4 MiB").performScrollTo().assertIsDisplayed()

        // Group 3: 账户与设备
        ui.onNodeWithText("账户与设备").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("2 台 · 1 台待完成").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("example.com").performScrollTo().assertIsDisplayed()

        // Group 4: 关于 + Separated Exit Card
        ui.onNodeWithText("关于").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("开源许可").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("退出并删除本机密钥…").performScrollTo().assertIsDisplayed()
    }

    @Test
    fun settingsUnlinkedWithoutRemovalBannerDoesNotClaimRemovedFromAccount() {
        val vm = SettingsViewModel.newBuilder()
            .setOnline(false)
            .setUnlinked(true)
            .setCanExitAccount(true)
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalChartFrozen provides true) {
                    SettingsScreenContent(vm = vm)
                }
            }
        }
        ui.onAllNodesWithText("本机已被移出账户", substring = true).assertCountEquals(0)
    }

    @Test
    @Config(qualifiers = "w960dp-h720dp")
    fun settingsCentersReadable640DpColumnOnTablet() {
        val vm = SettingsViewModel.newBuilder()
            .setOnline(true)
            .setCanAddDevice(false)
            .setEmptyMachinesText(lit("还没有电脑"))
            .setEmptyMachinesBody(lit("在管理端配对"))
            .setNotifications(NotificationsView.newBuilder().setSummaryText(lit("未开启")))
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalChartFrozen provides true) {
                    SettingsScreenContent(vm = vm, serverDisplay = "example.com")
                }
            }
        }
        val cardBounds = ui.onNodeWithText("还没有电脑").fetchSemanticsNode().boundsInRoot
        assertTrue("Expected readable centered card on 960dp tablet, got $cardBounds", cardBounds.left > 100f)
    }

    @Test
    @Config(qualifiers = "w960dp-h720dp")
    fun planBMachineCardsRenderTwoColumnsOnWideAndAllEdgeStates() {
        val vm = SettingsViewModel.newBuilder()
            .setOnline(true)
            .setCanAddDevice(false)
            .addMachines(
                MachineListCardView.newBuilder()
                    .setMachineId("m1")
                    .setName("MacBook")
                    .setDisambiguator("74c7")
                    .setOnline(true)
                    .setMember(true)
                    .setDirectActive(true)
                    .setStatusDot(MachineStatusDot.MACHINE_STATUS_DOT_GREEN_LIVE)
                    .setStatusText(lit("在线"))
                    .addQuotaItems(
                        MachineCardQuotaItem.newBuilder()
                            .setAgent(Agent.AGENT_CLAUDE)
                            .setAgentName("Claude Code")
                            .setRemainingPercent(0)
                            .setExhausted(true)
                            .setWarn(true)
                            .setValueText(lit("已用尽")),
                    ),
            )
            .addMachines(
                MachineListCardView.newBuilder()
                    .setMachineId("m2")
                    .setName("MacBook")
                    .setDisambiguator("a91e")
                    .setOnline(true)
                    .setMember(true)
                    .setStatusDot(MachineStatusDot.MACHINE_STATUS_DOT_ORANGE_STATIC)
                    .setStatusText(lit("需更新"))
                    .addAlerts(
                        MachineCardAlert.newBuilder()
                            .setKind(MachineAlertKind.MACHINE_ALERT_KIND_PROTOCOL_MISMATCH)
                            .setText(lit("协议版本不一致，需更新此应用"))
                            .setWarn(true),
                    ),
            )
            .addMachines(
                MachineListCardView.newBuilder()
                    .setMachineId("m3")
                    .setName("Old-iMac")
                    .setOnline(false)
                    .setMember(false)
                    .setDimmed(true)
                    .setStatusDot(MachineStatusDot.MACHINE_STATUS_DOT_UNSPECIFIED)
                    .setStatusText(lit("已移除"))
                    .setSecondaryNote(lit("保留历史会话记录")),
            )
            .build()

        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalChartFrozen provides true) {
                    SettingsScreenContent(vm = vm, serverDisplay = "example.com")
                }
            }
        }

        val leftTag = ui.onNodeWithText("74c7").fetchSemanticsNode().boundsInRoot
        val rightTag = ui.onNodeWithText("a91e").fetchSemanticsNode().boundsInRoot
        assertTrue("Expected 2-column grid side-by-side on 960dp wide screen: left=$leftTag right=$rightTag", rightTag.left > leftTag.right)
        ui.onNodeWithText("已用尽").assertIsDisplayed()
        ui.onNodeWithText("协议版本不一致，需更新此应用").assertIsDisplayed()
        ui.onNodeWithText("已移除").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("保留历史会话记录").performScrollTo().assertIsDisplayed()
    }

    @Test
    fun notificationsSecondaryScreenDispatchesAllSwitchesAndPushFix() {
        var attention: Boolean? = null
        var turnEnded: Boolean? = null
        var scheduled: Boolean? = null
        var showContent: Boolean? = null
        var fixed = 0
        val prefs = NotificationsView.newBuilder()
            .setNotifyAttention(true)
            .setNotifyTurnEnded(false)
            .setNotifyScheduled(true)
            .setShowContent(false)
            .build()
        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalNav provides nav) {
                    NotificationsScreenContent(
                        notifications = prefs,
                        push = PushStatus(problem = "系统通知权限已关闭", action = "去开启"),
                        onNotifyAttention = { attention = it },
                        onNotifyTurnEnded = { turnEnded = it },
                        onNotifyScheduled = { scheduled = it },
                        onShowContent = { showContent = it },
                        onFixPush = { fixed++ },
                    )
                }
            }
        }

        ui.onNodeWithText("只影响这台设备").assertIsDisplayed()
        ui.onNodeWithText("去开启").performClick()
        assertEquals(1, fixed)

        ui.onNodeWithText("等我处理时").assertIsOn().performClick()
        assertEquals(false, attention)
        ui.onNodeWithText("一轮结束时").assertIsOff().performClick()
        assertEquals(true, turnEnded)
        ui.onNodeWithText("定时任务结果").performScrollTo().assertIsOn().performClick()
        assertEquals(false, scheduled)
        ui.onNodeWithText("通知中显示内容并直接审批").performScrollTo().assertIsOff().performClick()
        assertEquals(true, showContent)
        ui.onNodeWithText("安全通知", substring = true).performScrollTo().assertIsDisplayed()
    }

    @Test
    fun pureClientsScreenShowsCurrentPairedAndUnfinishedWithoutComputers() {
        val vm = DevicesViewModel.newBuilder()
            .setIsManager(true)
            .setCanAddDevice(true)
            .setClientCountText(lit("2 台"))
            .addClients(
                ClientRowView.newBuilder()
                    .setDeviceId("c-self")
                    .setDisplayName("Pixel Test")
                    .setDescription("Android Phone")
                    .setShortId("…self")
                    .setIsSelf(true)
                    .setLastUsedText(lit("现在")),
            )
            .addClients(
                ClientRowView.newBuilder()
                    .setDeviceId("c-desk")
                    .setDisplayName("Desk Browser")
                    .setDescription("Chrome")
                    .setShortId("…desk")
                    .setLastUsedText(lit("5 分钟前")),
            )
            .addUnfinishedPairings(
                ClientRowView.newBuilder()
                    .setDeviceId("c-pending")
                    .setDisplayName("Tablet Pending")
                    .setShortId("…pend")
                    .setPairingIncomplete(true),
            )
            .addComputers(
                MachineListCardView.newBuilder().setMachineId("m1").setName("HiddenMachine"),
            )
            .build()
        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalNav provides nav) {
                    DevicesScreenContent(vm = vm)
                }
            }
        }
        ui.onAllNodesWithText("HiddenMachine").assertCountEquals(0)
        ui.onNodeWithText("已配对的客户端").assertIsDisplayed()
        ui.onNodeWithText("2 台").assertIsDisplayed()
        ui.onNodeWithText("Pixel Test").assertIsDisplayed()
        ui.onNodeWithText("Desk Browser").assertIsDisplayed()
        ui.onNodeWithText("Tablet Pending").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("待完成").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("添加新客户端").performScrollTo().assertIsDisplayed()
    }

    @Test
    @Config(qualifiers = "w960dp-h720dp")
    fun computerScreenModulesCliSheetUnifiedDirectAndStorageOnTablet() {
        var openedStatus = 0
        var openedQuota = 0
        var openedUsage = 0
        var openedStorage = 0
        var openedProjectsFor = ""
        var toggledAgent: Pair<Agent, Boolean>? = null
        var updatedMode: ApprovalMode? = null
        val vm = MachineSettingsViewModel.newBuilder()
            .setMachineId("m1")
            .setName("StudioBox")
            .setShortId("abcd1234")
            .setFound(true)
            .setMember(true)
            .setOnline(true)
            .setEditable(true)
            .setHeaderDot(MachineStatusDot.MACHINE_STATUS_DOT_ORANGE_STATIC)
            .setHeaderSubtitle(lit("在线 · daemon 0.94.0"))
            .setShowStatusEntry(true)
            .setStatusCardDot(MachineStatusDot.MACHINE_STATUS_DOT_ORANGE_STATIC)
            .setStatusCardBadge(lit("磁盘紧张"))
            .setStatusCardWarn(true)
            .addStatusRings(
                MachineRingView.newBuilder()
                    .setKind(MachineRingKind.MACHINE_RING_KIND_CPU)
                    .setHasValue(true)
                    .setFraction(0.35)
                    .setValueText(lit("35%"))
                    .setLabel(lit("CPU"))
                    .setTone(MachineRingTone.MACHINE_RING_TONE_NORMAL),
            )
            .addQuotaRows(
                QuotaCliSection.newBuilder()
                    .setAgent(Agent.AGENT_CODEX)
                    .setAgentName("Codex")
                    .setSummaryPrimaryText(lit("剩 12%"))
                    .setSummaryPrimaryWarn(true)
                    .setSummaryWindowText(lit("5 小时窗口")),
            )
            .setShowUsageEntry(true)
            .setUsageTotalTokens(2_400_000L)
            .setConnection(
                ConnectionGroupView.newBuilder()
                    .setToggleVisible(true)
                    .setToggleEnabled(true)
                    .setToggleChecked(true)
                    .setStage(ConnectionGroupStage.CONNECTION_GROUP_STAGE_RELAY_FAILED)
                    .setTone(ConnectionGroupTone.CONNECTION_GROUP_TONE_MUTED)
                    .setRowDot(MachineStatusDot.MACHINE_STATUS_DOT_ORANGE_STATIC)
                    .setRowValueText(lit("中继 · 48 ms"))
                    .setRowSubtitleText(lit("未能建立直连"))
                    .setShowDiagnosticsEntry(true),
            )
            .setShowCliSwitches(true)
            .setCliSwitchesEditable(true)
            .addCliVersions(
                CliVersionView.newBuilder()
                    .setAgent(Agent.AGENT_CODEX)
                    .setName("Codex")
                    .setVersion("0.22.0")
                    .setVerified(true)
                    .setEnabled(true)
                    .setLockedLastEnabled(false)
                    .setDefaultModelLabel(lit("gpt-5"))
                    .setDefaultModeLabel(lit("完全放行"))
                    .setDefaultModeDanger(true)
                    .setDefaults(
                        MachineAgentDefaultsView.newBuilder()
                            .setAgent(Agent.AGENT_CODEX)
                            .setAgentName("Codex")
                            .setSelectedModel("gpt-5")
                            .setSelectedModelLabel(lit("gpt-5"))
                            .setSelectedMode(ApprovalMode.APPROVAL_MODE_FULL_ACCESS)
                            .addModelChoices(ModelChoiceView.newBuilder().setId("gpt-5").setLabel(lit("gpt-5")).setSelected(true))
                            .addEffortChoices(EffortChoiceView.newBuilder().setId("high").setLabel(lit("高")).setSelected(true))
                            .addEffortChoices(EffortChoiceView.newBuilder().setId("medium").setLabel(lit("中")))
                            .addModeChoices(ModeChoiceView.newBuilder().setMode(ApprovalMode.APPROVAL_MODE_ASK).setLabel(lit("每次询问")))
                            .addModeChoices(ModeChoiceView.newBuilder().setMode(ApprovalMode.APPROVAL_MODE_FULL_ACCESS).setLabel(lit("完全放行")).setSelected(true).setDanger(true)),
                    ),
            )
            .addCliVersions(
                CliVersionView.newBuilder()
                    .setAgent(Agent.AGENT_CLAUDE)
                    .setName("Claude Code")
                    .setVersion("1.0.50")
                    .setVerified(false)
                    .setEnabled(true)
                    .setLockedLastEnabled(false)
                    .setDefaultModelLabel(lit("Sonnet"))
                    .setDefaultModeLabel(lit("每次询问")),
            )
            .addApprovalTimeoutChoices(TimeoutChoiceView.newBuilder().setSeconds(300).setLabel(lit("5 分钟")).setSelected(true))
            .addIdleCloseChoices(IdleCloseChoiceView.newBuilder().setMinutes(60).setLabel(lit("1 小时")).setSelected(true))
            .setAttachmentStorage(
                AttachmentStorageView.newBuilder()
                    .setLoaded(true)
                    .setSummaryText(lit("17 MiB / 1 GiB · 预留启用"))
                    .setSummaryWarn(true),
            )
            .setProjectCount(4)
            .setProjectCountText(lit("4 个"))
            .setManagement(
                MachineManagementActionsView.newBuilder()
                    .setCanRemove(true)
                    .setExplanationText(lit("")),
            )
            .build()

        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalChartFrozen provides true, LocalNav provides nav) {
                    DeviceScreenContent(
                        vm = vm,
                        onOpenStatus = { openedStatus++ },
                        onOpenQuota = { openedQuota++ },
                        onOpenUsage = { openedUsage++ },
                        onOpenStorage = { openedStorage++ },
                        onOpenProjects = { openedProjectsFor = it },
                        onToggleAgent = { a, en -> toggledAgent = a to en },
                        onUpdateAgentDefaults = { _, _, _, mode -> if (mode != null) updatedMode = mode },
                    )
                }
            }
        }

        // Header + Module 0
        ui.onNodeWithText("StudioBox").assertIsDisplayed()
        ui.onNodeWithText("在线 · daemon 0.94.0").assertIsDisplayed()
        ui.onNodeWithText("磁盘紧张").performClick()
        assertEquals(1, openedStatus)

        // Module 1: Quota & Usage
        ui.onNodeWithText("剩 12% · 5 小时窗口").performScrollTo().performClick()
        assertEquals(1, openedQuota)
        ui.onNodeWithText("用量与速度").performScrollTo().performClick()
        assertEquals(1, openedUsage)

        // Module 2: CLI row + full-access single line + CLI sheet on tablet
        val fullAccessNode = ui.onNodeWithText("完全放行").performScrollTo()
        fullAccessNode.assertIsDisplayed()
        ui.onNodeWithText("gpt-5").assertIsDisplayed()
        ui.onNodeWithText("未验证 · 1.0.50").performScrollTo().assertIsDisplayed()

        ui.onAllNodesWithText("Codex").onLast().performScrollTo().performClick()
        ui.waitForIdle()
        ui.onNodeWithText("在此电脑启用").performClick()
        assertEquals(Agent.AGENT_CODEX to false, toggledAgent)
        ui.onNodeWithText("审批模式").performClick()
        ui.onAllNodesWithText("每次询问").onLast().performClick()
        assertEquals(ApprovalMode.APPROVAL_MODE_ASK, updatedMode)
        ui.onNodeWithText("完成").performClick()
        ui.waitForIdle()

        // Module 3: Runtime & Storage
        ui.onNodeWithText("附件存储").performScrollTo().performClick()
        assertEquals(1, openedStorage)

        // Module 4: Management (no flat project list)
        ui.onNodeWithText("4 个").performScrollTo().performClick()
        assertEquals("m1", openedProjectsFor)
        ui.onNodeWithText("abcd1234").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("移除这台电脑…").performScrollTo().assertIsDisplayed()
    }

    @Test
    fun attachmentStorageScreenStackedBarAndActionsWork() {
        var pickedQuota: Long? = null
        var cleared = 0
        val vm = MachineSettingsViewModel.newBuilder()
            .setMachineId("m1")
            .setName("StudioBox")
            .setFound(true)
            .setOnline(true)
            .setEditable(true)
            .setAttachmentStorage(
                AttachmentStorageView.newBuilder()
                    .setLoaded(true)
                    .setUsedTotalText(lit("640 MiB"))
                    .setLimitLabel(lit("1 GiB"))
                    .setUploadedPercent(45)
                    .setPreviewPercent(15)
                    .setReservePercent(10)
                    .setUploadedText(lit("500 MiB"))
                    .setPreviewCacheText(lit("140 MiB"))
                    .setShowEmergencyReserve(true)
                    .setEmergencyReserveText(lit("64 MiB"))
                    .setClearButtonLabel(lit("可释放 220 MiB"))
                    .setCanClear(true)
                    .addQuotaChoices(QuotaSizeChoiceView.newBuilder().setBytes(1L shl 30).setLabel(lit("1 GiB")).setSelected(true))
                    .addQuotaChoices(QuotaSizeChoiceView.newBuilder().setBytes(2L shl 30).setLabel(lit("2 GiB"))),
            )
            .build()
        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalNav provides nav) {
                    MachineAttachmentStorageScreenContent(
                        vm = vm,
                        onQuota = { pickedQuota = it },
                        onClean = { cleared++ },
                    )
                }
            }
        }
        ui.onNodeWithText("已占用 640 MiB").assertIsDisplayed()
        ui.onNodeWithText("上限 1 GiB").assertIsDisplayed()
        ui.onNodeWithText("上传附件").assertIsDisplayed()
        ui.onAllNodesWithText("预览缓存")[0].assertIsDisplayed()
        ui.onAllNodesWithText("应急预留")[0].assertIsDisplayed()
        ui.onNodeWithText("存储上限").performScrollTo().performClick()
        ui.onNodeWithText("2 GiB").performClick()
        assertEquals(2L shl 30, pickedQuota)

        ui.onNodeWithText("清理可释放空间…").performScrollTo().performClick()
        ui.onNodeWithText("清理").performClick()
        assertEquals(1, cleared)
    }

    @Test
    fun filteredManageProjectsShowsMachineSubtitleAndResets() {
        var resetCalled = 0
        val manage = ManageProjectsView.newBuilder()
            .setFilterMachineId("m1")
            .setFilterMachineName("StudioBox")
            .setCanEdit(true)
            .setListing(
                ProjectListingView.newBuilder()
                    .addOthers(
                        ProjectRowView.newBuilder()
                            .setKey("m1:/repo")
                            .setName(lit("repo"))
                            .setSubtitle(lit("StudioBox · /repo")),
                    ),
            )
            .build()
        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalNav provides nav) {
                    ManageProjectsScreenContent(
                        manage = manage,
                        onShowAllMachines = { resetCalled++ },
                    )
                }
            }
        }
        ui.onAllNodesWithText("仅显示 StudioBox 的项目")[0].assertIsDisplayed()
        ui.onNodeWithText("查看全部项目").performClick()
        assertEquals(1, resetCalled)
    }

    @Test
    fun crossPageJumpsOpenStorageDirectAndTargetCliSheet() {
        // Jump 1: Home DiskReminderCards navigates to deviceStorage(machineId)
        var storageTarget = ""
        var directTarget = ""
        var cliJump: Pair<String, Agent>? = null
        val nav = Nav(
            back = {}, session = {}, project = {}, projectNew = {}, home = {},
            settings = {}, device = {}, pair = {}, approval = { _, _ -> }, plan = {}, newProject = {}, pickProject = {},
            deviceStorage = { storageTarget = it },
            deviceConnection = { directTarget = it },
            deviceForAgent = { id, ag -> cliJump = id to ag },
        )
        val reminders = listOf(
            DiskReminderView.newBuilder()
                .setMachineId("m-disk")
                .setMachineName("StudioBox")
                .setCritical(true)
                .setTitle(lit("StudioBox 磁盘空间不足"))
                .setDetail(lit("轻点清理附件"))
                .build(),
        )
        var step by mutableStateOf(0)
        val initialVm = MachineSettingsViewModel.newBuilder()
            .setMachineId("m1")
            .setName("StudioBox")
            .setFound(true)
            .setMember(true)
            .setOnline(true)
            .setEditable(true)
            .setShowCliSwitches(true)
            .setCliSwitchesEditable(true)
            .addCliVersions(
                CliVersionView.newBuilder().setAgent(Agent.AGENT_CLAUDE).setName("Claude Code").setVersion("1.0").setVerified(true).setEnabled(true),
            )
            .addCliVersions(
                CliVersionView.newBuilder().setAgent(Agent.AGENT_CODEX).setName("Codex").setVersion("0.2").setVerified(true).setEnabled(false),
            )
            .setConnection(
                ConnectionGroupView.newBuilder()
                    .setToggleVisible(true)
                    .setToggleEnabled(true)
                    .setToggleChecked(true)
                    .setStage(ConnectionGroupStage.CONNECTION_GROUP_STAGE_RELAY_FAILED)
                    .setShowDiagnosticsEntry(true),
            )
            .build()
        val newSessionNotice = NewSessionNoticeView.newBuilder()
            .setSeverity(NoticeSeverity.NOTICE_SEVERITY_WARN)
            .setText(lit("Codex 已在这台电脑关闭"))
            .setAction(NoticeActionKind.NOTICE_ACTION_KIND_ENABLE_AGENT)
            .setTargetMachineId("m1")
            .setTargetAgent(Agent.AGENT_CODEX)
            .build()

        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalNav provides nav) {
                    when (step) {
                        0 -> Column {
                            DiskReminderCards(reminders)
                            NewSessionNoticeRow(
                                n = newSessionNotice,
                                onAction = { act ->
                                    if (act == NoticeActionKind.NOTICE_ACTION_KIND_ENABLE_AGENT) {
                                        nav.deviceForAgent(newSessionNotice.targetMachineId, newSessionNotice.targetAgent)
                                    }
                                },
                                onChooseOfflineMachine = {},
                            )
                        }
                        1 -> DeviceScreenContent(vm = initialVm, openDirect = true)
                        else -> DeviceScreenContent(vm = initialVm, initialAgent = Agent.AGENT_CODEX)
                    }
                }
            }
        }
        ui.onNodeWithText("StudioBox 磁盘空间不足").performClick()
        assertEquals("m-disk", storageTarget)
        ui.onNodeWithText(sessionText(UiMessages.newSessionEnableAgent())).performClick()
        assertEquals("m1" to Agent.AGENT_CODEX, cliJump)

        // Jump 2: deviceConnection -> openDirect = true opens unified Direct sheet immediately
        ui.runOnUiThread { step = 1 }
        ui.waitForIdle()
        ui.onNodeWithText("允许直连").assertIsDisplayed()
        ui.onNodeWithText("自动排障检查").assertIsDisplayed()

        // Jump 3: deviceForAgent -> initialAgent opens target CLI's settings sheet immediately
        ui.runOnUiThread { step = 2 }
        ui.waitForIdle()
        ui.onNodeWithText("在此电脑启用").assertIsDisplayed()
        ui.onNodeWithText("停用后不出现在这台电脑的新任务、新定时、额度和用量与速度里；停用前已创建的会话和历史照常可用。").assertIsDisplayed()
    }

    @Test
    fun attachmentStorageErrorRetryHidesZeroProjectsAndSingleEffort() {
        var retried = 0
        var storageClicks = 0
        val vm = MachineSettingsViewModel.newBuilder()
            .setMachineId("m-err")
            .setName("DevBox")
            .setFound(true)
            .setMember(true)
            .setOnline(true)
            .setEditable(true)
            .setShowCliSwitches(true)
            .setCliSwitchesEditable(true)
            .setCliSwitchesConflictBanner(lit("开关配置在其他设备修改过"))
            .addCliVersions(
                CliVersionView.newBuilder()
                    .setAgent(Agent.AGENT_CODEX)
                    .setName("Codex")
                    .setVersion("1.0.0")
                    .setVerified(true)
                    .setEnabled(true)
                    .setDefaults(
                        MachineAgentDefaultsView.newBuilder()
                            .setAgent(Agent.AGENT_CODEX)
                            .setAgentName("Codex")
                            .setSelectedModel("gpt-5")
                            .addEffortChoices(EffortChoiceView.newBuilder().setId("medium").setLabel(lit("中")).setSelected(true)),
                    ),
            )
            .addCliVersions(
                CliVersionView.newBuilder().setAgent(Agent.AGENT_CLAUDE).setName("Claude Code").setVersion("2.0").setVerified(true).setEnabled(true),
            )
            .setAttachmentStorage(AttachmentStorageView.newBuilder().setLoaded(false))
            .setSettingsError(lit("无法读取存储设置"))
            .setProjectCount(0)
            .build()
        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
        var showStorage by mutableStateOf(false)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalNav provides nav) {
                    if (showStorage) {
                        MachineAttachmentStorageScreenContent(vm = vm, onRetry = { retried++ })
                    } else {
                        DeviceScreenContent(vm = vm, onOpenStorage = { storageClicks++ })
                    }
                }
            }
        }
        ui.onNodeWithText("项目").assertDoesNotExist()
        ui.onNodeWithText("附件存储").performScrollTo().performClick()
        assertEquals(1, storageClicks)

        ui.onNodeWithText("Codex").performScrollTo().performClick()
        ui.waitForIdle()
        ui.onAllNodesWithText("开关配置在其他设备修改过").onLast().assertIsDisplayed()
        ui.onNodeWithText("推理强度").assertDoesNotExist()

        ui.runOnUiThread { showStorage = true }
        ui.waitForIdle()
        ui.onNodeWithText("无法读取存储设置").assertIsDisplayed()
        ui.onNodeWithText(sessionText(UiMessages.commonRetry())).performClick()
        assertEquals(1, retried)
    }
}
