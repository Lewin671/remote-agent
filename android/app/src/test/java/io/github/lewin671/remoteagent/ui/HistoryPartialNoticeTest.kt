package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.data.ThemeMode
import kotlinx.coroutines.CompletableDeferred
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class HistoryPartialNoticeTest {
    @get:Rule val ui = createComposeRule()

    @Test fun briefCatchupCancelsTheTimerAndPersistentGapsWaitForIt() {
        val partial = mutableStateOf(true)
        val session = mutableStateOf("one")
        var timer = CompletableDeferred<Unit>()
        ui.setContent { RaTheme(ThemeMode.LIGHT) {
            HistoryPartialNotice(session.value, partial.value, waitForNotice = { timer.await() })
        } }
        ui.onNodeWithText("历史记录尚未读全。").assertDoesNotExist()
        ui.runOnIdle { partial.value = false }
        ui.runOnIdle { timer.complete(Unit) }
        ui.onNodeWithText("历史记录尚未读全。").assertDoesNotExist()
        ui.runOnIdle { timer = CompletableDeferred(); partial.value = true }
        ui.onNodeWithText("历史记录尚未读全。").assertDoesNotExist()
        ui.runOnIdle { timer.complete(Unit) }
        ui.onNodeWithText("历史记录尚未读全。").assertExists()
        ui.runOnIdle { timer = CompletableDeferred(); session.value = "two" }
        ui.onNodeWithText("历史记录尚未读全。").assertDoesNotExist()
        ui.runOnIdle { partial.value = false; timer.complete(Unit) }
        ui.onNodeWithText("历史记录尚未读全。").assertDoesNotExist()
    }
}
