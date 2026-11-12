package io.github.lewin671.remoteagent.ui

import android.graphics.Bitmap
import android.view.KeyEvent
import android.view.WindowManager
import androidx.activity.ComponentActivity
import androidx.activity.enableEdgeToEdge
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import java.io.File

/** Real touches and pre-IME Back on a session fixture; no account or CLI commands are sent. */
class ComposerMenuKeyboardTest {
    @get:Rule val ui = createAndroidComposeRule<ComponentActivity>()

    @Test fun sessionMenusLeaveKeyboardAndComposerInPlace() {
        val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "menu-test"), Api.defaultHttp())
        val session = Session.newBuilder().setId("menu-session").setCwd("/example/project")
            .setTitle("Composer menus").setAgent(Agent.AGENT_CODEX).setStatus(SessionStatus.SESSION_STATUS_RUNNING).build()
        val machine = MachineView("machine", true, 0, MachineMeta.newBuilder().setHostname("Test computer")
            .setProtocolVersion(PROTOCOL_VERSION).setCapabilities(MachineCapabilities.newBuilder().setFileUpload(true).setImageUpload(true)).build())
        remote.showForTest(RemoteState(conn = ConnState.ONLINE, link = Link.ONLINE, loaded = true, synced = true,
            machines = mapOf("machine" to machine), sessions = mapOf("menu-session" to SessionView("menu-session", "machine", session))),
            events = mapOf("menu-session" to listOf(
                Event.newBuilder().setSessionId("menu-session").setSeq(1).setUserMessage(UserMessage.newBuilder().setText("Check the composer menus while typing.")).build(),
                Event.newBuilder().setSessionId("menu-session").setSeq(2).setAgentMessage(AgentMessage.newBuilder().setText("Keep the draft and keyboard in place.")).build(),
                Event.newBuilder().setSessionId("menu-session").setSeq(3).setTurnStarted(TurnStarted.getDefaultInstance()).build(),
            )),
            live = mapOf("menu-session" to LiveSnapshot.newBuilder().setSession(session).setRunning(true).build()))
        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val output = File(instrumentation.targetContext.getExternalFilesDir(null), "composer-menus").apply { mkdirs() }
        File(output, "measurements.txt").writeText("")
        ui.activityRule.scenario.onActivity {
            it.enableEdgeToEdge()
            it.window.setSoftInputMode(WindowManager.LayoutParams.SOFT_INPUT_ADJUST_RESIZE)
        }
        try {
            ui.setContent {
                RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) {
                    CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                        AppSafeArea { SessionScreen("menu-session") }
                    }
                }
            }
            val field = ui.onNode(hasSetTextAction())
            field.performTouchInput { click() }
            field.performTextInput("Menu keyboard check")
            fun ime() = ViewCompat.getRootWindowInsets(ui.activity.window.decorView)?.isVisible(WindowInsetsCompat.Type.ime()) == true
            ui.waitUntil(5000) { ime() }
            Thread.sleep(700)
            val baseline = field.fetchSemanticsNode().boundsInWindow.top
            fun capture(name: String) {
                ui.waitForIdle()
                Thread.sleep(700)
                field.assertIsFocused()
                assertTrue("$name keyboard", ime())
                val top = field.fetchSemanticsNode().boundsInWindow.top
                assertEquals("$name composer displacement", baseline, top, 0f)
                File(output, "measurements.txt").appendText("$name fieldTop=$top delta=${top - baseline} ime=true\n")
                File(output, "$name.png").outputStream().use {
                    requireNotNull(instrumentation.uiAutomation.takeScreenshot()).compress(Bitmap.CompressFormat.PNG, 100, it)
                }
            }
            fun plus() = ui.onNodeWithContentDescription("添加附件").performTouchInput { click() }
            fun send() = ui.onNodeWithContentDescription("发送").performTouchInput { longClick() }
            fun back() = instrumentation.sendKeyDownUpSync(KeyEvent.KEYCODE_BACK)
            capture("android-baseline")
            plus(); ui.onNodeWithText("照片和视频").assertIsDisplayed(); capture("android-attach-open")
            plus(); ui.onNodeWithText("照片和视频").assertDoesNotExist(); capture("android-attach-retap")
            plus(); field.performTouchInput { click() }; ui.onNodeWithText("照片和视频").assertDoesNotExist(); capture("android-attach-outside")
            plus(); back(); ui.onNodeWithText("照片和视频").assertDoesNotExist(); capture("android-attach-back")
            plus()
            // This touch belongs to the IME's own window, outside the Compose hierarchy.
            val screen = requireNotNull(instrumentation.uiAutomation.takeScreenshot())
            val height = screen.height; screen.recycle()
            instrumentation.uiAutomation.executeShellCommand("input tap 100 ${height - 260}").close()
            ui.waitUntil(5000) { ui.onAllNodesWithText("照片和视频").fetchSemanticsNodes().isEmpty() }
            capture("android-attach-keyboard")
            send(); ui.onNodeWithText("这一轮结束后再发").assertIsDisplayed(); capture("android-send-open")
            ui.onNodeWithContentDescription("发送").performTouchInput { click() }
            ui.onNodeWithText("这一轮结束后再发").assertDoesNotExist(); capture("android-send-retap")
            send(); field.performTouchInput { click() }; ui.onNodeWithText("这一轮结束后再发").assertDoesNotExist(); capture("android-send-outside")
            send(); back(); ui.onNodeWithText("这一轮结束后再发").assertDoesNotExist(); capture("android-send-back")
        } finally { remote.close(); ImageDrafts.forSession("menu-session").clear() }
    }
}
