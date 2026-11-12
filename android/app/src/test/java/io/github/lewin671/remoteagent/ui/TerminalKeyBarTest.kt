package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.width
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.term.KeyBar
import org.junit.*
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], qualifiers = "w700dp-h360dp-land", application = Application::class)
class TerminalKeyBarTest {
    @get:Rule val ui = createComposeRule()
    @Test fun landscapeKeyboardKeepsAllKeysInOneAccessibleRow() {
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Box(Modifier.width(640.dp)) { KeyBar(true, {}, {}, compact = true) }
            }
        }
        val keys = listOf("Esc", "左", "上", "下", "右", "回车", "Tab", "Ctrl+C", "键盘").map { label ->
            ui.onNodeWithText(label).takeIf { label in listOf("Esc", "Tab", "Ctrl+C") }
                ?: ui.onNodeWithContentDescription(label)
        }
        val bounds = keys.map { it.assertIsDisplayed().fetchSemanticsNode().boundsInRoot }
        Assert.assertEquals(1, bounds.map { it.top }.distinct().size)
        val density = ui.density.density
        bounds.forEach { Assert.assertTrue(it.width / density >= 44f); Assert.assertTrue(it.height / density >= 44f) }
    }
}
