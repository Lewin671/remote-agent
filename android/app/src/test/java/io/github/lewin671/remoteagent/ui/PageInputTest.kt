package io.github.lewin671.remoteagent.ui

import androidx.compose.ui.focus.FocusDirection
import androidx.compose.ui.focus.FocusManager
import androidx.compose.ui.platform.SoftwareKeyboardController
import org.junit.Assert.*
import org.junit.Test

class PageInputTest {
    @Test fun leavingAPageExplicitlyHidesImeAndClearsEvenCapturedFocus() {
        val calls = mutableListOf<String>()
        val focus = object : FocusManager {
            override fun clearFocus(force: Boolean) { calls += "clear:$force" }
            override fun moveFocus(focusDirection: FocusDirection) = false
        }
        val keyboard = object : SoftwareKeyboardController {
            override fun show() { calls += "show" }
            override fun hide() { calls += "hide" }
        }
        dismissPageInput(focus, keyboard)
        assertEquals(listOf("hide", "clear:true"), calls)
        calls.clear()
        dismissPageInput(focus, null)
        assertEquals(listOf("clear:true"), calls)
    }
}
