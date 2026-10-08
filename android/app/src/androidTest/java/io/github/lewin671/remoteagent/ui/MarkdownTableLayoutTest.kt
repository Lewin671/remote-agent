package io.github.lewin671.remoteagent.ui

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import java.io.File

/** Real text measurement on an isolated emulator app, never a live account. */
class MarkdownTableLayoutTest {
    @get:Rule val ui = createComposeRule()
    private val financial = """
        | 估值口径 | 每股收益 EPS | 市盈率 |
        | --- | ---: | ---: |
        | 2026 财年实际，GAAP | $74.33 | **14.5 倍** |
        | 2026 财年实际，调整后 | $75.52 | **14.3 倍** |
        | 下季指引中值 ×4 | $152.60 | **7.1 倍** |
        | 第三方列示的 2027 财年预期 | $159.03 | **6.8 倍** |
    """.trimIndent()
    private fun mount(text: String, dark: Boolean = false, font: Float = 1f) {
        ui.setContent {
            val base = LocalDensity.current
            RaTheme(if (dark) ThemeMode.DARK else ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalDensity provides Density(base.density, font)) {
                    Column(Modifier.width(320.dp).background(pal.card).testTag("frame")) { Markdown(text) }
                }
            }
        }
    }
    private fun bounds(text: String) = ui.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
    private fun sameColumn(header: String, cells: List<String>) {
        val h = bounds(header)
        for (text in cells) {
            val cell = bounds(text)
            assertEquals("left edge of $text", h.left, cell.left, 1f)
            assertEquals("right edge of $text", h.right, cell.right, 1f)
        }
    }
    private fun screenshot(name: String) {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val bitmap = ui.onNodeWithTag("frame").captureToImage().asAndroidBitmap()
        File(context.cacheDir, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }
    @Test fun financialRowsUseTheSameColumnsAndFitThePhoneViewport() {
        mount(financial)
        sameColumn("估值口径", listOf("2026 财年实际，GAAP", "2026 财年实际，调整后", "下季指引中值 ×4", "第三方列示的 2027 财年预期"))
        sameColumn("每股收益 EPS", listOf("$74.33", "$75.52", "$152.60", "$159.03"))
        sameColumn("市盈率", listOf("14.5 倍", "14.3 倍", "7.1 倍", "6.8 倍"))
        val frame = ui.onNodeWithTag("frame").fetchSemanticsNode().boundsInRoot
        assertTrue(bounds("6.8 倍").right <= frame.right)
        ui.onNodeWithText("6.8 倍").assertIsDisplayed()
        screenshot("table-financial-light")
    }
    @Test fun multilineCellsStartAtTheTopOfTheirRowAndDoNotOverlapTheNextRow() {
        val long = "很长的内容不会被截断，".repeat(12)
        mount("| 说明 | 数值 |\n| --- | ---: |\n| $long | 7.1 倍 |\n| 下一行 | 14.3 倍 |", dark = true)
        val a = bounds(long); val b = bounds("7.1 倍")
        assertEquals(a.top, b.top, 1f)
        assertTrue(bounds("下一行").top >= a.bottom)
        sameColumn("数值", listOf("7.1 倍", "14.3 倍"))
        screenshot("table-multiline-dark")
    }
    @Test fun wideTablesScrollHorizontallyAtLargeFont() {
        mount("| 第一列 | 第二列 | 第三列 | 第四列 | 第五列 | 第六列 |\n| --- | --- | --- | --- | --- | --- |\n| 长内容一 | 长内容二 | 长内容三 | 长内容四 | 长内容五 | 长内容六 |", dark = true, font = 2f)
        val scroll = ui.onNode(SemanticsMatcher.keyIsDefined(SemanticsProperties.HorizontalScrollAxisRange))
        val range = scroll.fetchSemanticsNode().config[SemanticsProperties.HorizontalScrollAxisRange]
        assertTrue(range.maxValue() > 0f)
        ui.onNodeWithText("第六列").performScrollTo().assertIsDisplayed()
        assertTrue(range.value() > 0f)
        screenshot("table-wide-dark-large")
    }
    @Test fun fontScaleChangesRemeasureAllRowsTogether() {
        val scale = mutableFloatStateOf(1f)
        ui.setContent {
            val base = LocalDensity.current
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalDensity provides Density(base.density, scale.floatValue)) {
                    Column(Modifier.width(320.dp).background(pal.card)) { Markdown(financial) }
                }
            }
        }
        sameColumn("每股收益 EPS", listOf("$74.33", "$159.03"))
        ui.runOnIdle { scale.floatValue = 2f }
        sameColumn("每股收益 EPS", listOf("$74.33", "$159.03"))
    }
    @Test fun emptyCellsAndInlineCodeKeepColumnPositions() {
        mount("| 标签 | 代码 | 数值 |\n| --- | --- | ---: |\n| 第一行 | `a\\|b` | 14.5 |\n| 第二行 | | 7.1 |\n| 第三行 | `x` | |");
        sameColumn("代码", listOf("a|b", "x"))
        sameColumn("数值", listOf("14.5", "7.1"))
        ui.onNodeWithText("第三行").assertIsDisplayed()
    }
    @Test fun aTableIsAsWideAsWhatItHoldsAndQuantitiesGoRight() {
        mount("| 场景 | P99 |\n| --- | --- |\n| 50 并发 | 42 ms |\n| 200 并发 | 1,180 ms |")
        val frame = ui.onNodeWithTag("frame").fetchSemanticsNode().boundsInRoot
        assertEquals(frame.left, bounds("场景").left, 1f)
        assertTrue(bounds("P99").right < frame.right - frame.width / 3)
        sameColumn("P99", listOf("42 ms", "1,180 ms"))
        screenshot("table-narrow-light")
    }
    @Test fun quoteListAndCodeNestInsideEachOther() {
        mount("> 注意：\n> - 一\n>   ```sh\n>   ls\n>   ```\n> - [x] 二\n\n1. 步骤\n\n   | a | b |\n   | --- | --- |\n   | 1 | 2 |")
        for (text in listOf("注意：", "一", "ls", "二", "步骤", "a", "2")) ui.onNodeWithText(text, useUnmergedTree = true).assertIsDisplayed()
        assertTrue(bounds("一").left > bounds("注意：").left)
        // A code block under an item starts where the item's text does, below it.
        assertEquals(bounds("一").left, bounds("ls").left, 1f)
        assertTrue(bounds("ls").top >= bounds("一").bottom)
        screenshot("nested-light")
    }
    private val reply = """
        找到原因了：`auth/login.go` 每次都新建连接，**没有复用**连接池，~~也没设超时~~。
        见 [auth/login.go:58](/Users/me/foo/auth/login.go:58) 和 https://example.com/docs。

        ## 改了什么 `pool.go`
        1. 复用连接池
           - 默认 8 个连接
           - 可配置
        2. 加超时

        **验证**

        | 场景 | 改动前 | 改动后 |
        | --- | --- | --- |
        | 50 并发 | 180 ms | 42 ms |
        | 200 并发 | 5.2 s | 61 ms |

        ```diff
        --- a/auth/login.go
        +++ b/auth/login.go
        -conn := dial()
        +conn := pool.Get()
        ```

        > 注意：上线前先灰度。
        > - [x] 单测
        > - [ ] 压测

        ---

        #### 后续
        9. 九
        10. 十
    """.trimIndent()
    private fun everything(dark: Boolean) {
        mount(reply, dark = dark)
        for (text in listOf("验证", "改动后", "61 ms", "压测", "后续", "10.")) ui.onNodeWithText(text, useUnmergedTree = true).assertExists()
        // Quantities are set to the right of their column.
        assertEquals(bounds("改动后").right, bounds("42 ms").right, 1f)
        screenshot(if (dark) "reply-dark" else "reply-light")
    }
    @Test fun aReplyOfEverythingDrawsInLight() = everything(dark = false)
    @Test fun aReplyOfEverythingDrawsInDark() = everything(dark = true)
    @Test fun longCodeOpensInPlaceAndCloses() {
        val code = (1..20).joinToString("\n") { "line $it" }
        mount("```\n$code\n```")
        ui.onNodeWithText("共 20 行").assertIsDisplayed()
        ui.onNodeWithText((1..12).joinToString("\n") { "line $it" }).assertIsDisplayed()
        ui.onNodeWithText("展开全文").performClick()
        ui.onNodeWithText(code).assertExists()
        ui.onNodeWithText("收起").performClick()
        ui.onNodeWithText("展开全文").assertExists()
        ui.onNodeWithContentDescription("复制代码").performClick()
        ui.onNodeWithContentDescription("已复制").assertExists()
    }
}
