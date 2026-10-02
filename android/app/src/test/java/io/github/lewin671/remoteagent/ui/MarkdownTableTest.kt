package io.github.lewin671.remoteagent.ui

import org.junit.Assert.*
import org.junit.Test

class MarkdownTableTest {
    @Test(timeout = 2000) fun unclosedLinkOpenersDoNotRescanEverySuffix() {
        for (text in listOf("[".repeat(100_000), "[x](".repeat(25_000))) {
            assertEquals(text, inline(text, androidx.compose.ui.graphics.Color.Gray, androidx.compose.ui.graphics.Color.Blue).text)
        }
    }

    @Test fun cellsDoNotSplitEscapedPipesOrCodeSpans() {
        assertEquals(listOf("a", "`x|y`", "c | d"), tableCells("| a | `x|y` | c \\| d |"))
        assertEquals(listOf("``x`|y``", "z"), tableCells("| ``x`|y`` | z |"))
        assertEquals(listOf("unfinished `", "z"), tableCells("| unfinished ` | z |"))
        assertEquals(listOf("a\\\\", "b"), tableCells("| a\\\\| b |"))
        assertEquals(listOf("a\\\\|", "b"), tableCells("| a\\\\\\| | b |"))
        assertEquals(listOf("\\`not code", "middle \\`", "final"), tableCells("| \\`not code | middle \\` | final |"))
        assertEquals(listOf("", "value", ""), tableCells("| | value | |"))
    }
    @Test fun multiBacktickCodeRendersItsWholeContentOnce() {
        val text = inline("``x`|y``", androidx.compose.ui.graphics.Color.Gray, androidx.compose.ui.graphics.Color.Blue)
        assertEquals("x`|y", text.text)
        assertEquals("`not code | middle `", inline("\\`not code | middle \\`", androidx.compose.ui.graphics.Color.Gray, androidx.compose.ui.graphics.Color.Blue).text)
        assertEquals(1, text.getStringAnnotations("code", 0, text.length).size)
        assertEquals("unfinished `", inline("unfinished `", androidx.compose.ui.graphics.Color.Gray, androidx.compose.ui.graphics.Color.Blue).text)
    }
    @Test fun tablesKeepAlignmentAndAcceptOptionalOuterPipes() {
        val table = parseMarkdown("口径 | EPS | PE\n:--- | :---: | ---:\nGAAP | **74.33** | 14.5\n短行 | 75.52").single() as Block.Table
        assertEquals(listOf("口径", "EPS", "PE"), table.header)
        assertEquals(listOf(TableAlign.START, TableAlign.CENTER, TableAlign.END), table.align)
        assertEquals(listOf(listOf("GAAP", "**74.33**", "14.5"), listOf("短行", "75.52")), table.rows)
        assertFalse(parseMarkdown("| a | b |\n| --- |\n| 1 | 2 |").any { it is Block.Table })
    }
    @Test fun compactTablesFitWithoutShrinkingNumbersAndWideTablesRemainScrollable() {
        assertEquals(listOf(210, 90, 60), tableColumnWidths(listOf(240, 90, 60), 360, 96))
        assertEquals(listOf(140, 140, 80), tableColumnWidths(listOf(240, 240, 80), 360, 96))
        assertEquals(listOf(60, 70), tableColumnWidths(listOf(60, 70), 360, 96))
        assertEquals(listOf(96, 96, 80), tableColumnWidths(listOf(240, 240, 80), 272, 96))
        assertEquals(List(6) { 120 }, tableColumnWidths(List(6) { 120 }, 320, 96))
        assertEquals(listOf(240, 160, 120), tableColumnWidths(listOf(240, 160, 120), 320, 192))
        val uneven = tableColumnWidths(listOf(239, 238, 120), 361, 96)
        assertEquals(361, uneven.sum()); assertTrue(uneven.all { it >= 96 })
    }
}
