package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performScrollToNode
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ImageFormat
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class ProjectTest {
    @get:Rule(order = 0) val appFixture = io.github.lewin671.remoteagent.RaAppTestRule(preparePendingStore = true)
    @get:Rule(order = 1) val ui = createComposeRule()
    @get:Rule(order = 2) val appJobs = appFixture.stopJobs { done ->
        ui.waitUntil(5000) { ui.mainClock.advanceTimeByFrame(); ui.waitForIdle(); done() }
    }

    private val remote = Remote(Account("http://127.0.0.1:1", "test-client"), Api.defaultHttp())
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})

    @After
    fun tearDown() {
        remote.close()
    }

    @Test
    fun rendersHomeTimelineGoldenView() {
        val home = ScreenGoldenLoader.homeView("home_timeline", "homeInitial")
        remote.coreHost.showViewsForTest(home = home)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    Box(Modifier.fillMaxSize()) { HomeScreen() }
                }
            }
        }
        ui.onNodeWithText("Run tests", substring = true).assertIsDisplayed()
        ui.onNodeWithTag("home-list").performScrollToNode(hasText("Refactor cache", substring = true))
        ui.onNodeWithText("Refactor cache", substring = true).assertIsDisplayed()
    }

    @Test
    fun rendersHomeEmptyManagerGoldenView() {
        val home = ScreenGoldenLoader.homeView("home_empty_errors", "emptyManager")
        remote.coreHost.showViewsForTest(home = home)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    Box(Modifier.fillMaxSize()) { HomeScreen() }
                }
            }
        }
        ui.onNodeWithTag("home-list").assertExists()
    }

    @Test
    fun rendersProjectManageGoldenView() {
        val project = ScreenGoldenLoader.projectView("project_manage_detail", "managePinning")
        remote.coreHost.showViewsForTest(project = project)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    Box(Modifier.fillMaxSize()) { ManageProjectsScreen() }
                }
            }
        }
        ui.onNodeWithTag("manage-projects-list").assertExists()
    }

    @Test
    fun rendersNewSessionLifecycleGoldenViews() {
        val ready = ScreenGoldenLoader.newSessionView("new_session_lifecycle", "readyToSubmit")
        val unknown = ScreenGoldenLoader.newSessionView("new_session_lifecycle", "unknownRetry")
        remote.coreHost.showViewsForTest(newSession = ready)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    val vm by remote.newSessionView.collectAsState()
                    NewSessionBox(vm, RemoteState(), focus = false)
                }
            }
        }
        ui.onNodeWithTag("new-composer").assertIsDisplayed()
        ui.runOnIdle { remote.coreHost.showViewsForTest(newSession = unknown) }
        assertTrue(unknown.frozen)
        assertTrue(unknown.noticesCount > 0)
    }

    @Test
    fun sessionAndPendingRowsRenderCoreFieldsWithoutShellReassembly() {
        val row = io.github.lewin671.remoteagent.core.proto.SessionRowView.newBuilder()
            .setSessionId("s1")
            .setMachineId("m1")
            .setTitle(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("Session A"))
            .setStatusText(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("UNUSED_STATUS"))
            .setPreview(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("UNUSED_PREVIEW"))
            .setSecondLine(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("core second line"))
            .build()
        val pending = io.github.lewin671.remoteagent.core.proto.AttentionRowView.newBuilder()
            .setSessionId("s2")
            .setMachineId("m1")
            .setTitle(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("Pending B"))
            .setKindText(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("Approval"))
            .setSummary(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("UNUSED_SUMMARY"))
            .setDetail(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("Approval"))
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    Column {
                        SessionRow(row) {}
                        PendingRow(pending, first = true, last = true) {}
                    }
                }
            }
        }
        ui.onNodeWithText("core second line").assertIsDisplayed()
        ui.onNodeWithText("UNUSED_STATUS", substring = true).assertDoesNotExist()
        ui.onNodeWithText("Approval · Approval").assertIsDisplayed()
        ui.onNodeWithText("UNUSED_SUMMARY", substring = true).assertDoesNotExist()
    }

    @Test
    fun projectDetailGoldenPopulatesHiddenNoticeAndMachineIds() {
        val detail = ScreenGoldenLoader.projectView("project_manage_detail", "detailInitial").detail
        assertTrue(detail.machineIdsList.isNotEmpty())
        assertTrue(detail.machineNamesList.isNotEmpty())
        assertTrue(detail.hiddenNotice.text.key.isNotEmpty())
        assertTrue(detail.hiddenNotice.actionEnabled)
    }

    @Test
    fun removeProjectConfirmUsesCoreProjectNameDirectly() {
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    RemoveProjectConfirm(key = "/work/raw-key", onDismiss = {}, projectName = "Core Display Name") {}
                }
            }
        }
        ui.onNodeWithText("Core Display Name", substring = true).assertIsDisplayed()
        ui.onNodeWithText("raw-key", substring = true).assertDoesNotExist()
    }
}
