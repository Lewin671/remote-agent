package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class SendGestureTest {
    @get:Rule val ui = createComposeRule()
    private var sent = 0
    private var queued = 0
    private fun show() {
        val draft = mutableStateOf("Long draft\n".repeat(12))
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) {
            ComposeBox(draft.value, { draft.value = it }, "", true, { sent++ }, {}, onQueue = { queued++ })
        } }
    }
    @Test fun sendAfterButtonTriggersQueueWithoutChangingSendButton() {
        show()
        val send = ui.onNodeWithContentDescription("发送")
        send.assertWidthIsAtLeast(44.dp).assertHeightIsAtLeast(44.dp)
        val sendAfter = ui.onNodeWithText("做完再发")
        sendAfter.assertIsDisplayed().assertHeightIsAtLeast(44.dp)
        sendAfter.performClick()
        assertEquals(0, sent)
        assertEquals(1, queued)
    }
    @Test fun defaultSendActionTriggersOnSendWithoutOpeningMenu() {
        show()
        val send = ui.onNodeWithContentDescription("发送")
        ui.onNodeWithText("做完再发").assertIsDisplayed()
        send.performClick()
        assertEquals(1, sent)
        assertEquals(0, queued)
    }
    @Test fun noLegacyModeMenuTextAppearsWhenRunningWithDraft() {
        show()
        ui.onNodeWithText("插入这一轮").assertDoesNotExist()
        ui.onNodeWithText("这一轮结束后再发").assertDoesNotExist()
        ui.onNodeWithText("本轮结束后发").assertDoesNotExist()
        assertEquals(0, sent)
        assertEquals(0, queued)
    }
}
