package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class SchedulesGoldenUiTest {
    @get:Rule val messages = SessionMessageContextRule()
    @get:Rule val ui = createComposeRule()

    @Test fun inboxGoldensRenderAttentionFreshAndMarkReadState() {
        val list1 = ScreenGoldenLoader.schedulesView("schedules_inbox_detail", "schedules_1")
        val list3 = ScreenGoldenLoader.schedulesView("schedules_inbox_detail", "schedules_3")

        var markedAll = 0
        var openedTasks = 0
        var currentList by mutableStateOf(list1)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalMessageLocale provides "zh-CN") {
                    SchedulesScreenContent(
                        vm = currentList,
                        onEvent = { block ->
                            val ev = CoreEvent.newBuilder().apply(block).build()
                            if (ev.hasScheduleMarkRead() && ev.scheduleMarkRead.allFresh) markedAll++
                        },
                        onOpenTask = { openedTasks++ },
                    )
                }
            }
        }
        ui.onNodeWithText("1 个任务停下", substring = true).assertExists()
        ui.onNodeWithText("Daily summary", substring = true).assertExists()
        ui.onNodeWithText("One-shot deploy check", substring = true).performClick()
        assertEquals(1, openedTasks)

        ui.runOnIdle { currentList = list3 }
        ui.onNodeWithText("全部标为已读").assertDoesNotExist()
        ui.onNodeWithText("1 个任务停下", substring = true).assertExists()
        assertEquals(0, markedAll)
    }

    @Test fun detailGoldenRendersFinalAnswerAndDispatchesActions() {
        val detail4 = ScreenGoldenLoader.scheduleDetailView("schedules_inbox_detail", "scheduleDetail_4")
        var currentDetail by mutableStateOf(detail4)
        var openedSession: Pair<String, Long>? = null
        val ops = mutableListOf<ScheduleOperationKind>()
        var renamed: String? = null
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalMessageLocale provides "zh-CN") {
                    ScheduleDetailScreenContent(
                        vm = currentDetail,
                        onOpenSession = { sid, seq -> openedSession = sid to seq },
                        onEvent = { block ->
                            val ev = CoreEvent.newBuilder().apply(block).build()
                            if (ev.hasScheduleOperate()) ops += ev.scheduleOperate.operation
                            if (ev.hasScheduleRename()) renamed = ev.scheduleRename.value
                        },
                    )
                }
            }
        }
        // While resultLoading is true, interim summary is held behind Skeleton so card height never jumps.
        ui.onNodeWithText("2 PRs merged").assertDoesNotExist()

        ui.runOnIdle {
            currentDetail = detail4.toBuilder()
                .setResultCard(detail4.resultCard.toBuilder().setResultLoading(false))
                .build()
        }
        ui.onAllNodesWithText("Daily summary").onFirst().assertExists()
        ui.onNodeWithText("2 PRs merged").assertExists()
        ui.onAllNodesWithText(messageTextForTest(UiMessages.scheduleConversation())).onFirst().performClick()
        assertEquals("s-res" to 2L, openedSession)

        ui.onNodeWithContentDescription("更多").performClick()
        ui.onNodeWithText("立即运行一次").performClick()
        assertEquals(listOf(ScheduleOperationKind.SCHEDULE_OPERATION_KIND_RUN), ops)

        ui.onNodeWithText("任务名").performScrollTo().performClick()
        ui.onNode(hasSetTextAction()).performTextReplacement("Renamed task")
        ui.onNodeWithText("保存").performClick()
        assertEquals("Renamed task", renamed)
    }

    @Test fun sessionStripGoldenOpensAssociatedTask() {
        val strip2 = ScreenGoldenLoader.scheduleSessionStripView("schedules_inbox_detail", "scheduleSessionStrip_2")
        var stripClicked: Pair<String, String>? = null
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalMessageLocale provides "zh-CN") {
                    SessionScheduleStripContent(strip2) { m, id -> stripClicked = m to id }
                }
            }
        }
        ui.onNodeWithText("管理").performClick()
        assertEquals("m1" to "t1", stripClicked)
    }

    @Test fun formLifecycleGoldensRenderPlanBlockFrozenRetryAndFullAccessConfirm() {
        val form1 = ScreenGoldenLoader.scheduleFormView("schedules_form_lifecycle", "scheduleForm_1")
        val form2 = ScreenGoldenLoader.scheduleFormView("schedules_form_lifecycle", "scheduleForm_2")
        val form3 = ScreenGoldenLoader.scheduleFormView("schedules_form_lifecycle", "scheduleForm_3")

        var currentForm by mutableStateOf(form1)
        var submitted: Boolean? = null
        var retries = 0
        var abandons = 0

        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalMessageLocale provides "zh-CN") {
                    ScheduleEditorContent(
                        vm = currentForm,
                        onEvent = { block ->
                            val ev = CoreEvent.newBuilder().apply(block).build()
                            if (ev.hasScheduleFormSubmit()) submitted = ev.scheduleFormSubmit.value
                            if (ev.hasScheduleRetryMutation()) retries++
                            if (ev.hasScheduleAbandonMutation()) abandons++
                        },
                    )
                }
            }
        }
        ui.onNodeWithText(messageTextForTest(UiMessages.scheduleCreate())).performScrollTo().assertIsNotEnabled()

        ui.runOnIdle { currentForm = form2 }
        ui.onNodeWithText(messageTextForTest(UiMessages.scheduleCreate())).performScrollTo().assertIsEnabled().performClick()
        assertEquals(false, submitted)

        ui.runOnIdle { currentForm = form3 }
        ui.onNodeWithText(messageTextForTest(UiMessages.commonRetry())).performScrollTo().performClick()
        ui.onNodeWithText(messageTextForTest(UiMessages.newSessionAndroidAbandon())).performScrollTo().performClick()
        assertEquals(1, retries)
        assertEquals(1, abandons)

        submitted = null
        ui.runOnIdle {
            currentForm = form2.toBuilder()
                .setRequiresFullAccessConfirm(true)
                .setCanSubmit(true)
                .build()
        }
        ui.onNodeWithText(messageTextForTest(UiMessages.scheduleCreate())).performScrollTo().performClick()
        ui.onNodeWithText(messageTextForTest(UiMessages.scheduleRunWithFullAccess())).assertExists()
        ui.onNodeWithText(messageTextForTest(UiMessages.modesFullAccess()), useUnmergedTree = true).performClick()
        assertEquals(true, submitted)
    }

    @Test fun detailCreatedTaskGoldenRendersLimitsMessageAndEmptyHistory() {
        val detail5 = ScreenGoldenLoader.scheduleDetailView("schedules_form_lifecycle", "scheduleDetail_5")
        val withConflict = detail5.toBuilder()
            .setMutation(
                ScheduleMutationView.newBuilder()
                    .setError(
                        Text.newBuilder()
                            .setKey("errors.the_task_was_just_changed_updated_to_the_latest"),
                    ),
            )
            .build()
        var current by mutableStateOf(detail5)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalMessageLocale provides "zh-CN") {
                    ScheduleDetailScreenContent(vm = current)
                }
            }
        }
        ui.onAllNodesWithText("Weekly audit").onFirst().assertExists()
        ui.onNodeWithText("Weekly audit. Run linters.").performScrollTo().assertExists()
        ui.onNodeWithText(messageTextForTest(UiMessages.scheduleHasNotRunYet())).performScrollTo().assertExists()
        ui.runOnIdle { current = withConflict }
        ui.onNodeWithText(messageTextForTest(UiMessages.errorsTheTaskWasJustChangedUpdatedToTheLatest())).assertExists()
    }
}
