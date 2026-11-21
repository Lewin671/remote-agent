package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.unit.Density
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

private fun corePlainText(value: String): Text = Text.newBuilder().setLiteral(value).build()
private fun coreMsgText(key: String, vararg args: Pair<String, Any>): Text {
    val b = Text.newBuilder().setKey(key)
    for ((k, v) in args) {
        val ab = TextArg.newBuilder().setName(k)
        when (v) {
            is Int -> ab.setIntValue(v.toLong())
            is Long -> ab.setIntValue(v)
            else -> ab.setStrValue(v.toString())
        }
        b.addArgs(ab)
    }
    return b.build()
}

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class ScheduleComponentsTest {
    @get:Rule val batchCResources = SessionMessageContextRule()
    @get:Rule val ui = createComposeRule()

    @Test fun taskRowOpensItsDetailAndShowsAllThreeLines() {
        var clicked = false
        val item = ScheduleTaskItemView.newBuilder()
            .setMachineId("computer")
            .setTaskId("quiet")
            .setTitle("每小时巡检")
            .setTimeText(corePlainText("09:00"))
            .setPreview(corePlainText("今天 2 次，都没有通知"))
            .setMetadata(corePlainText("每小时 · project"))
            .build()
        ui.setContent { RaTheme(ThemeMode.LIGHT) { TaskRow(item) { clicked = true } } }
        ui.onNodeWithText("每小时", substring = true).assertIsDisplayed()
        ui.onNodeWithText("今天 2 次，都没有通知").assertIsDisplayed()
        ui.onNodeWithText("每小时巡检").performClick()
        assertTrue(clicked)
    }

    @Test fun newResultRowAnnouncesUnreadAndOfflineState() {
        val item = ScheduleTaskItemView.newBuilder()
            .setMachineId("computer")
            .setTaskId("task")
            .setTitle("很长的任务名".repeat(30))
            .setHasUnread(true)
            .setMachineOffline(true)
            .setPreview(corePlainText("最近摘要".repeat(100)))
            .setMetadata(coreMsgText("schedule.the_computer_is_offline_connect_to_create_tasks"))
            .build()
        ui.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, 2f)) {
                RaTheme(ThemeMode.DARK) { TaskRow(item) {} }
            }
        }
        ui.onNodeWithText("99+").assertDoesNotExist()
        ui.onNodeWithContentDescription("有新结果").assertExists()
        ui.onNodeWithText(messageTextForTest(UiMessages.scheduleTheComputerIsOfflineConnectToCreateTasks()), substring = true, useUnmergedTree = true).assertExists()
    }

    @Test fun visitDotsAppearOnlyForTheUnreadRunsCapturedOnEntry() {
        fun groupWith(r1Unread: Boolean) = ScheduleHistoryGroupView.newBuilder()
            .setGroupKey("runs")
            .setFoldedQuiet(false)
            .addRuns(
                ScheduleRunRowView.newBuilder()
                    .setRunId("r1")
                    .setUnreadOnEntry(r1Unread)
                    .setTimeText(corePlainText("08:00"))
                    .setSummaryText(corePlainText("摘要 1")),
            )
            .addRuns(
                ScheduleRunRowView.newBuilder()
                    .setRunId("r2")
                    .setUnreadOnEntry(false)
                    .setTimeText(corePlainText("07:00"))
                    .setSummaryText(corePlainText("摘要 2")),
            )
            .build()
        var group by mutableStateOf(groupWith(true))
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Column { ScheduleRunGroup(group, onToggleQuiet = {}, openRun = {}) }
            }
        }
        ui.onAllNodesWithContentDescription("进入时未读", useUnmergedTree = true).assertCountEquals(1)
        ui.runOnIdle { group = groupWith(false) }
        ui.onAllNodesWithContentDescription("进入时未读", useUnmergedTree = true).assertCountEquals(0)
    }

    @Composable
    private fun Bar(active: ShellTab, waiting: Int, scheduled: Int, newResults: Boolean = false, onSelect: (ShellTab) -> Unit = {}) =
        BottomBar(active, waiting, scheduled, newResults, onSelect, "新任务", Lucide.SquarePen, {}, true)

    @Test fun tabBadgeShowsTheSharedScheduleCountAndNavigates() {
        var selected: ShellTab? = null
        ui.setContent { RaTheme(ThemeMode.LIGHT) { Bar(ShellTab.SESSIONS, 4, 2) { selected = it } } }
        ui.onNodeWithText("2", useUnmergedTree = true).assertExists()
        ui.onNodeWithContentDescription("定时", substring = true).performClick()
        assertEquals(ShellTab.SCHEDULES, selected)
    }

    @Test fun tabBadgeCapsAttentionAndOnlyShowsBlueWhenThereIsNoAttention() {
        val attention = mutableIntStateOf(120)
        ui.setContent { RaTheme(ThemeMode.DARK) { Bar(ShellTab.SCHEDULES, 0, attention.intValue, true) } }
        ui.onNodeWithText("99+", useUnmergedTree = true).assertExists()
        ui.onNodeWithContentDescription("120 件需要处理", substring = true).assertExists()
        ui.onNodeWithContentDescription("有新结果", substring = true).assertDoesNotExist()
        ui.runOnIdle { attention.intValue = 0 }
        ui.onNodeWithText("99+", useUnmergedTree = true).assertDoesNotExist()
        ui.onNodeWithContentDescription("有新结果", substring = true).assertExists()
    }

    @Test fun rowExposesReadActionAndSwipeInvokesIt() {
        val item = ScheduleTaskItemView.newBuilder()
            .setMachineId("m")
            .setTaskId("t")
            .setTitle("测试任务")
            .setHasUnread(true)
            .setActions(ScheduleTaskActionsView.newBuilder().setCanMarkRead(true))
            .build()
        var marks = 0
        ui.setContent { RaTheme(ThemeMode.LIGHT) { TaskRow(item, onMark = { marks++ }) {} } }
        val actions = ui.onNodeWithText("测试任务").fetchSemanticsNode().config[SemanticsActions.CustomActions]
        ui.runOnIdle {
            assertEquals(messageTextForTest(UiMessages.scheduleMarkAsRead()), actions.single().label)
            actions.single().action()
        }
        ui.onNodeWithText("测试任务").performTouchInput { swipeLeft() }
        ui.runOnIdle { assertEquals(2, marks) }
    }

    @Test fun markdownPreviewPreservesWholeBlocksAndExpandsTablesAndCode() {
        val text = "开头一段。\n\n" + (1..10).joinToString("\n\n") { "第 $it 段。" } + "\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\nfmt.Println(1)\n```"
        ui.setContent { RaTheme(ThemeMode.LIGHT) { Column(Modifier) { ScheduleResultMarkdown(text) } } }
        ui.onNodeWithText("开头一段。").assertExists()
        ui.onNodeWithText("第 10 段。").assertDoesNotExist()
        ui.onNodeWithText("展开全文").performClick()
        ui.onNodeWithText("第 10 段。").assertExists()
        ui.onNodeWithText("fmt.Println(1)", substring = true).assertExists()
    }

    @Test fun quietTurnsExpandToTheirOriginalText() {
        val group = SessionTimelineItem.newBuilder()
            .setId("quiet")
            .setQuietScheduledGroup(
                SessionQuietScheduledGroupItem.newBuilder()
                    .setGroupId("quiet")
                    .setRunCount(2)
                    .setTitle(coreMsgText("schedule.quiet_runs", "count" to 2, "day" to "今天"))
                    .addItems(
                        SessionTimelineItem.newBuilder()
                            .setId("u1")
                            .setUserMessage(SessionUserMessageItem.newBuilder().setText("第一次检查。").setIsScheduled(true)),
                    )
                    .addItems(
                        SessionTimelineItem.newBuilder()
                            .setId("a1")
                            .setAgentMessage(SessionAgentMessageItem.newBuilder().setMarkdown("无事发生")),
                    ),
            )
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                TimelineItemView(
                    item = group,
                    onRestoreInput = { _, _ -> },
                    onTerminalPopup = {},
                )
            }
        }
        ui.onNodeWithText("无事发生").assertDoesNotExist()
        ui.onNodeWithText("2 次，都没有通知", substring = true).performClick()
        ui.onNodeWithText("无事发生").assertExists()
    }

    @Test fun renameShowsSelectedTitleAndSkipsUnchanged() {
        var closed = false
        var saves = 0
        val title = "项目早报"
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                ScheduleRenameDialog(
                    initialTitle = title,
                    mutation = ScheduleMutationView.getDefaultInstance(),
                    enabled = true,
                    onClose = { closed = true },
                    onSave = { saves++ },
                )
            }
        }
        ui.onNode(hasSetTextAction()).assert(SemanticsMatcher.expectValue(SemanticsProperties.TextSelectionRange, TextRange(0, title.length)))
        ui.onNodeWithText("留空保存会用消息的第一句").assertExists()
        ui.onNodeWithText("保存").performClick()
        ui.runOnIdle { assertTrue(closed); assertEquals(0, saves) }
    }

    @Test fun frozenRenameCannotBeSubmittedAgain() {
        var saves = 0
        var mutation by mutableStateOf(ScheduleMutationView.getDefaultInstance())
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                ScheduleRenameDialog(
                    initialTitle = "项目早报",
                    mutation = mutation,
                    enabled = true,
                    onClose = {},
                    onSave = {
                        saves++
                        mutation = ScheduleMutationView.newBuilder()
                            .setFrozen(true)
                            .setError(corePlainText("结果未确认"))
                            .build()
                    },
                )
            }
        }
        ui.onNode(hasSetTextAction()).performTextReplacement("新任务名")
        ui.onNodeWithText("保存").performClick()
        ui.onNodeWithText("保存").assertIsNotEnabled().performClick()
        ui.runOnIdle { assertEquals(1, saves) }
    }

    @Test fun weeklySelectionDisabledWithoutDaysAndIntervalHidesTimezone() {
        var draft by mutableStateOf(
            ScheduleTimingDraftView.newBuilder()
                .setKind(ScheduleTimingKind.SCHEDULE_TIMING_KIND_WEEKLY)
                .addWeekdays(1)
                .setTime("13:24")
                .setZone("UTC")
                .setEnd(ScheduleTimingEnd.SCHEDULE_TIMING_END_COUNT)
                .setCount("8")
                .setTimingDoneEnabled(true)
                .setShowJitter(true)
                .setJitterLabel(coreMsgText("schedule.jitter_label"))
                .setJitterHint(coreMsgText("schedule.jitter_hint"))
                .addJitterOptions(ScheduleJitterOptionView.newBuilder().setMinutes(0).setLabel(coreMsgText("schedule.jitter_none")).setSelected(true))
                .addJitterOptions(ScheduleJitterOptionView.newBuilder().setMinutes(30).setLabel(corePlainText("30 分钟内")).setSelected(false))
                .build(),
        )
        val updates = mutableListOf<ScheduleFormUpdateTimingAction>()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                ScheduleTimeSheet(
                    draft = draft,
                    formKey = "f1",
                    enabled = true,
                    submitOnApply = false,
                    onEvent = { block ->
                        val ev = CoreEvent.newBuilder().apply(block).build()
                        if (!ev.hasScheduleFormUpdateTiming()) return@ScheduleTimeSheet
                        val act = ev.scheduleFormUpdateTiming
                        updates += act
                        if (act.replaceWeekdays) {
                            draft = draft.toBuilder()
                                .clearWeekdays()
                                .addAllWeekdays(act.weekdaysList)
                                .setTimingDoneEnabled(act.weekdaysCount > 0)
                                .build()
                        } else if (act.hasJitterMinutes()) {
                            draft = draft.toBuilder()
                                .clearJitterOptions()
                                .addAllJitterOptions(
                                    draft.jitterOptionsList.map { it.toBuilder().setSelected(it.minutes == act.jitterMinutes).build() },
                                )
                                .build()
                        } else if (act.hasKind()) {
                            val showJ = act.kind == ScheduleTimingKind.SCHEDULE_TIMING_KIND_DAILY || act.kind == ScheduleTimingKind.SCHEDULE_TIMING_KIND_WEEKLY
                            draft = draft.toBuilder()
                                .setKind(act.kind)
                                .setShowJitter(showJ)
                                .setShowIntervalHint(act.kind == ScheduleTimingKind.SCHEDULE_TIMING_KIND_INTERVAL)
                                .setIntervalHint(coreMsgText("schedule.the_first_run_is_after_a_full_interval_to"))
                                .build()
                        }
                    },
                )
            }
        }
        ui.onNodeWithText(messageTextForTest(UiMessages.scheduleJitterLabel())).performClick()
        ui.onNodeWithText("30 分钟内").performClick()
        assertTrue(updates.any { it.hasJitterMinutes() && it.jitterMinutes == 30L })
        ui.onNodeWithText("一").assertIsToggleable().assertIsOn().performClick()
        ui.onNodeWithText("完成").assertIsNotEnabled()
        ui.onNodeWithText("五").performClick().assertIsOn()
        ui.onNodeWithText("完成").assertIsEnabled().performClick()
        ui.onNodeWithText("每隔").performClick()
        ui.onNodeWithText("时区").assertDoesNotExist()
        ui.onNodeWithText(messageTextForTest(UiMessages.scheduleJitterLabel())).assertDoesNotExist()
        ui.onNodeWithText(messageTextForTest(UiMessages.scheduleTheFirstRunIsAfterAFullIntervalTo())).assertExists()
        assertTrue(updates.any { it.apply })
    }

    @Test fun descriptionFoldsInPlaceAndEditorConfirmsDiscard() {
        val long = (1..12).joinToString("\n") { "第 $it 行，检查项目并报告结果。" }
        ui.setContent { RaTheme(ThemeMode.LIGHT) { ScheduleDescription(long) } }
        ui.onNodeWithText("展开全文").performClick()
        ui.onNodeWithText("收起").assertExists()
    }

    @Test fun editorRejectsEmptyOrOversizeAndCancelConfirmsChanges() {
        var closed = false
        var formVm by mutableStateOf(
            ScheduleFormViewModel.newBuilder()
                .setFormKey("f1")
                .setMessage("原文")
                .setEditable(true)
                .setCanSubmit(true)
                .build(),
        )
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                ScheduleMessageEditor(
                    vm = formVm,
                    onEvent = { block ->
                        val ev = CoreEvent.newBuilder().apply(block).build()
                        if (ev.hasScheduleFormSetMessage()) {
                            val msg = ev.scheduleFormSetMessage.value
                            val tooLong = msg.toByteArray(Charsets.UTF_8).size > 32768
                            formVm = formVm.toBuilder()
                                .setMessage(msg)
                                .setMessageTooLong(tooLong)
                                .setMessageError(if (tooLong) coreMsgText("schedule.too_long_maximum_32_kb") else Text.getDefaultInstance())
                                .setCanSubmit(msg.isNotBlank() && !tooLong)
                                .build()
                        }
                    },
                    close = { closed = true },
                )
            }
        }
        ui.onNode(hasSetTextAction()).performTextReplacement("")
        ui.onNodeWithText("保存").assertIsNotEnabled()
        ui.onNode(hasSetTextAction()).performTextReplacement("中".repeat(10923))
        ui.onNodeWithText("太长了，最多 32 KB").assertExists()
        ui.onNodeWithText("保存").assertIsNotEnabled()
        ui.onNodeWithText("取消").performClick()
        ui.onNodeWithText("放弃修改？").assertExists()
        assertFalse(closed)
        ui.onNodeWithText("放弃", useUnmergedTree = true).performClick()
        assertTrue(closed)
    }

    @Test fun scheduledChatFoldsOnlyScheduledMessages() {
        val long = (1..12).joinToString("\n") { "描述第 $it 行" }
        ui.setContent { RaTheme(ThemeMode.LIGHT) { MeBubble(long, caption = "定时任务 · 08:00", foldScheduled = true) } }
        ui.onNodeWithText("定时任务 · 08:00 · 展开全文").performClick()
        ui.onNodeWithText("定时任务 · 08:00 · 收起").assertExists()
    }

    @Test fun offlineEditorCanCancelWhileSavingIsDisabled() {
        var closed = false
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                ScheduleMessageEditor(
                    vm = ScheduleFormViewModel.newBuilder()
                        .setFormKey("f1")
                        .setMessage("原文")
                        .setEditable(false)
                        .setCanSubmit(false)
                        .build(),
                    close = { closed = true },
                )
            }
        }
        ui.onNodeWithText("保存").assertIsNotEnabled()
        ui.onNodeWithText("取消").assertIsEnabled().performClick()
        assertTrue(closed)
    }

    @Test fun conversationTabCapsItsBadgeAndSaysTheWholeCount() {
        ui.setContent { RaTheme(ThemeMode.LIGHT) { Bar(ShellTab.SESSIONS, 120, 120) } }
        ui.onAllNodesWithText("99+", useUnmergedTree = true).assertCountEquals(2)
        ui.onAllNodesWithContentDescription("120 件等你处理", substring = true).assertCountEquals(1)
        ui.onAllNodesWithContentDescription("120 件需要处理", substring = true).assertCountEquals(1)
    }

    @Test fun firstRunningTaskShowsNoResultWithoutRepeatingRunningInItsPreview() {
        val item = ScheduleTaskItemView.newBuilder()
            .setMachineId("computer")
            .setTaskId("first")
            .setTitle("首次检查")
            .setInProgress(true)
            .setPreview(corePlainText("还没有结果"))
            .setPreviewIsPlaceholder(true)
            .build()
        ui.setContent { RaTheme(ThemeMode.LIGHT) { TaskRow(item) {} } }
        ui.onNodeWithText("还没有结果", useUnmergedTree = true).assertExists()
        ui.onAllNodesWithText("正在运行", substring = true, useUnmergedTree = true).assertCountEquals(1)
    }

    @Test fun attentionReasonUsesMutedTextRatherThanTheOrangeStatusColor() {
        val item = ScheduleTaskItemView.newBuilder()
            .setMachineId("computer")
            .setTaskId("attention")
            .setGroup(ScheduleDisplayGroup.SCHEDULE_DISPLAY_GROUP_ATTENTION)
            .setTitle("每日总结")
            .setPreview(corePlainText("已停下 · 权限需要确认"))
            .build()
        var muted = Color.Unspecified
        ui.setContent { RaTheme(ThemeMode.LIGHT) { muted = pal.ink2; TaskRow(item) {} } }
        val layouts = mutableListOf<TextLayoutResult>()
        ui.onNodeWithText("已停下 · 权限需要确认", useUnmergedTree = true)
            .performSemanticsAction(SemanticsActions.GetTextLayoutResult) { it(layouts) }
        assertEquals(muted, layouts.single().layoutInput.style.color)
    }
}
