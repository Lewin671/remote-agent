package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
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
    @Test fun switchingSingleAndTwoRowLayoutKeepsTextFieldFocused() {
        val draft = mutableStateOf("")
        val focusRequester = FocusRequester()
        val focusHistory = mutableListOf<Boolean>()
        ui.setContent {
            RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) {
                ComposeBox(
                    text = draft.value,
                    onText = { draft.value = it },
                    placeholder = "消息",
                    canSend = draft.value.isNotBlank(),
                    onSend = {},
                    onPlus = {},
                    focusRequester = focusRequester,
                    onFocus = { focusHistory += it },
                )
            }
        }
        ui.runOnIdle { focusRequester.requestFocus() }
        ui.waitForIdle()
        assertTrue(focusHistory.lastOrNull() == true)
        focusHistory.clear()

        // Cross 20-char threshold (single-row -> two-row).
        ui.runOnIdle { draft.value = "gemini-flash-worker-agy" }
        ui.waitForIdle()
        assertTrue("Focus dropped when expanding to two rows: $focusHistory", focusHistory.none { !it })

        // Drop back to 20 chars (two-row -> single-row).
        ui.runOnIdle { draft.value = "gemini-flash-worker-" }
        ui.waitForIdle()
        assertTrue("Focus dropped when collapsing to single row: $focusHistory", focusHistory.none { !it })

        // Cross via newline and back.
        ui.runOnIdle { draft.value = "a\nb" }
        ui.waitForIdle()
        ui.runOnIdle { draft.value = "ab" }
        ui.waitForIdle()
        assertTrue("Focus dropped when toggling newline layout: $focusHistory", focusHistory.none { !it })
    }
}
