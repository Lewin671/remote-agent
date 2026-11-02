package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.*
import androidx.compose.ui.platform.LocalWindowInfo
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ScheduleResultLayoutTest {
    @get:Rule val ui = createComposeRule()

    @Test fun largeFontPreviewCutsBetweenWholeParagraphsAndExpandsTheOriginalBlocks() {
        val paragraphs = (1..10).map { "第 $it 段，检查项目并报告本次运行的完整结果。" }
        val text = paragraphs.joinToString("\n\n") + "\n\n| 名称 | 数量 |\n|---|---|\n| 项目 | 2 |\n\n```go\nfmt.Println(1)\n```"
        ui.setContent {
            RaTheme(ThemeMode.DARK) {
                val density = LocalDensity.current
                CompositionLocalProvider(LocalDensity provides Density(density.density, 2f)) {
                    Column(Modifier.width(320.dp).verticalScroll(rememberScrollState())) { ScheduleResultMarkdown(text) }
                }
            }
        }
        ui.onNodeWithText(paragraphs.first()).assertExists()
        ui.onNodeWithText(paragraphs.last()).assertDoesNotExist()
        ui.onNodeWithText("展开全文").performScrollTo().performClick()
        ui.onNodeWithText(paragraphs.last()).assertExists()
        ui.onNodeWithText("fmt.Println(1)", substring = true).assertExists()
    }

    @Test fun aSingleOversizeParagraphPreviewsEightWholeLinesBeforeExpanding() {
        val text = "这是同一个完整段落，需要按整段保留。".repeat(60)
        ui.setContent { RaTheme(ThemeMode.LIGHT) { Column(Modifier.width(320.dp).verticalScroll(rememberScrollState())) { ScheduleResultMarkdown("---\n\n$text\n\n---") } } }
        val layouts = mutableListOf<androidx.compose.ui.text.TextLayoutResult>()
        ui.onNodeWithText(text).performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.GetTextLayoutResult) { it(layouts) }
        assertEquals(8, layouts.single().lineCount)
        assertTrue(layouts.single().hasVisualOverflow)
        ui.onNodeWithText("展开全文").performClick()
        layouts.clear()
        ui.onNodeWithText(text).performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.GetTextLayoutResult) { it(layouts) }
        assertTrue(layouts.single().lineCount > 8)
        assertFalse(layouts.single().hasVisualOverflow)
    }
    @Test fun wideWindowDoesNotFoldANarrowCardAtTheShared840DpBoundary() {
        val width = mutableIntStateOf(839)
        val text = "这是一段需要完整阅读的长结果。".repeat(60)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                val window = LocalWindowInfo.current
                val density = LocalDensity.current
                val testWindow = remember(window, width.intValue, density) {
                    object : androidx.compose.ui.platform.WindowInfo by window {
                        override val containerSize = IntSize(with(density) { width.intValue.dp.roundToPx() }, window.containerSize.height)
                    }
                }
                CompositionLocalProvider(LocalWindowInfo provides testWindow) {
                    Column(Modifier.width(320.dp).verticalScroll(rememberScrollState())) { ScheduleResultMarkdown(text) }
                }
            }
        }
        ui.onNodeWithText("展开全文").assertExists()
        ui.runOnIdle { width.intValue = 840 }
        ui.onNodeWithText("展开全文").assertDoesNotExist()
        val layouts = mutableListOf<androidx.compose.ui.text.TextLayoutResult>()
        ui.onNodeWithText(text).performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.GetTextLayoutResult) { it(layouts) }
        assertTrue(layouts.single().lineCount > 8)
        assertFalse(layouts.single().hasVisualOverflow)
    }

}
