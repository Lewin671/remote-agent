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
    @Test fun longPressAtTheTargetEdgeOpensQueueWithoutSending() {
        show()
        val send = ui.onNodeWithContentDescription("发送")
        send.assertWidthIsAtLeast(44.dp).assertHeightIsAtLeast(44.dp)
        val edge = with(ui.density) { 21.dp.toPx() }
        send.performTouchInput { longClick(androidx.compose.ui.geometry.Offset(center.x + edge, center.y)) }
        ui.onNodeWithText("这一轮结束后再发").assertIsDisplayed().performClick()
        assertEquals(0, sent)
        assertEquals(1, queued)
    }
    @Test fun cancelingAPressDoesNotOpenOrSendAndNextTapStillSends() {
        show()
        val send = ui.onNodeWithContentDescription("发送")
        send.performTouchInput { down(center) }
        ui.mainClock.advanceTimeBy(100)
        send.performTouchInput { cancel() }
        ui.onNodeWithText("这一轮结束后再发").assertDoesNotExist()
        assertEquals(0, sent)
        send.performTouchInput { click() }
        assertEquals(1, sent)
        assertEquals(0, queued)
    }
    @Test fun accessibilityLongClickOpensImmediately() {
        show()
        ui.onNodeWithContentDescription("发送").performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.OnLongClick) { it() }
        ui.onNodeWithText("这一轮结束后再发").assertIsDisplayed()
        assertEquals(0, sent)
    }
}
