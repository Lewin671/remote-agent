package io.github.lewin671.remoteagent.ui

import org.junit.Assert.*
import org.junit.Test

/** What the shared cases (MarkdownCasesTest) do not say: time, table cells, column widths, spacing and styling. */
class MarkdownTableTest {
    @Test(timeout = 5000) fun hostileTextIsReadInLinearTime() {
        for (text in listOf("[".repeat(100_000), "[x](".repeat(25_000), "<https://".repeat(20_000), "`".repeat(100_000) + "x", "* ".repeat(30_000), "_ ".repeat(30_000), "~~".repeat(30_000))) {
            assertEquals(listOf(Span.Text(text)), inlineSpans(text))
        }
        // A bare URL is cut from its end once, not once per character dropped.
        assertEquals(listOf(Span.Link("https://a.b/", "https://a.b/"), Span.Text(")".repeat(100_000))), inlineSpans("https://a.b/" + ")".repeat(100_000)))
        // Lines that a backtracking pattern would read in quadratic time or worse.
        val n = 100_000
        assertEquals(listOf(Block.Heading(1, "#".repeat(n) + "x")), parseMarkdown("# " + "#".repeat(n) + "x"))
        assertEquals(listOf(Block.Heading(1, "a" + " ".repeat(n) + "b")), parseMarkdown("# a" + " ".repeat(n) + "b " + "#".repeat(n)))
        assertEquals(listOf(Block.Para("a | b\n" + " ".repeat(n) + "x")), parseMarkdown("a | b\n" + " ".repeat(n) + "x"))
        assertEquals(listOf(Block.ItemList(false, 1, listOf(Item("- ".repeat(n - 1) + "x", null, emptyList())))), parseMarkdown("- ".repeat(n) + "x"))
        // Quantities are scanned once, however many a cell strings together.
        assertTrue(numericColumn(listOf("1 ".repeat(n) + "1")))
        assertFalse(numericColumn(listOf("1 ".repeat(n) + "xyzabc")))
        assertFalse(numericColumn(listOf("1" + " ".repeat(n) + "x")))
        assertEquals(n - 1, tableCells("|".repeat(n)).size)
    }

    @Test(timeout = 5000) fun nestingStopsAtAFixedDepth() {
        fun depth(blocks: List<Block>): Int = 1 + (blocks.maxOfOrNull {
            when (it) {
                is Block.Quote -> depth(it.blocks)
                is Block.ItemList -> it.items.maxOf { item -> depth(item.children) }
                else -> 0
            }
        } ?: 0)
        assertEquals(33, depth(parseMarkdown("> ".repeat(5_000) + "x")))
        assertEquals(33, depth(parseMarkdown((0 until 200).joinToString("\n") { " ".repeat(it * 2) + "- x" })))
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

    @Test fun codeIsMarkedForItsChipAndEscapedTicksAreNot() {
        val text = richText(inlineSpans("``x`|y``"), LightPalette)
        assertEquals("x`|y", text.text)
        assertEquals(1, text.getStringAnnotations("code", 0, text.length).size)
        val escaped = richText(inlineSpans("\\`not code | middle \\`"), LightPalette)
        assertEquals("`not code | middle `", escaped.text)
        assertEquals(0, escaped.getStringAnnotations("code", 0, escaped.length).size)
    }

    @Test fun onlyWebLinksCanBeOpened() {
        val text = richText(inlineSpans("[a](https://a.b) [b](/tmp/x) [c](javascript:void) https://c.d."), LightPalette)
        assertEquals("a b c https://c.d.", text.text)
        assertEquals(listOf("https://a.b", "https://c.d"), text.getLinkAnnotations(0, text.length).map { (it.item as androidx.compose.ui.text.LinkAnnotation.Url).url })
        assertEquals("https://a.b", safeHref(" https://a.b "))
        for (target in listOf("https://", "http://a b", "ftp://a.b", "javascript:alert(1)", "/tmp/x")) assertNull(target, safeHref(target))
    }

    @Test fun diffColorsAddedAndRemovedLinesButNotFileHeaders() {
        val add = androidx.compose.ui.graphics.Color.Green
        val del = androidx.compose.ui.graphics.Color.Red
        val lines = listOf("--- a/x", "+++ b/x", "@@ -1 +1 @@", "-old", "+new", " same")
        val text = codeText(lines, diff = true, add, del)
        assertEquals(lines.joinToString("\n"), text.text)
        assertEquals(listOf("-old" to del, "+new" to add), text.spanStyles.map { text.text.substring(it.start, it.end) to it.item.color })
        assertTrue(codeText(lines, diff = false, add, del).spanStyles.isEmpty())
    }

    @Test fun compactTablesFitWithoutShrinkingNumbersAndWideTablesRemainScrollable() {
        assertEquals(listOf(210, 90, 60), tableColumnWidths(listOf(240, 90, 60), 360, 96))
        assertEquals(listOf(140, 140, 80), tableColumnWidths(listOf(240, 240, 80), 360, 96))
        // Narrower than the viewport, a table keeps the width of what it holds.
        assertEquals(listOf(60, 70), tableColumnWidths(listOf(60, 70), 360, 96))
        assertEquals(listOf(96, 96, 80), tableColumnWidths(listOf(240, 240, 80), 272, 96))
        assertEquals(List(6) { 120 }, tableColumnWidths(List(6) { 120 }, 320, 96))
        assertEquals(listOf(240, 160, 120), tableColumnWidths(listOf(240, 160, 120), 320, 192))
        val uneven = tableColumnWidths(listOf(239, 238, 120), 361, 96)
        assertEquals(361, uneven.sum()); assertTrue(uneven.all { it >= 96 })
    }

    private fun gaps(md: String, place: Place = Place.REPLY, afterText: Boolean = false) = spaced(parseMarkdown(md), place, afterText).map { it.first }

    @Test fun spaceBetweenBlocksIsTheFirstRuleThatFits() {
        assertEquals(listOf(0, 12, 8, 12, 12, 12), gaps("a\n\nb\n- c\n\nd\n\n```\nx\n```\n\n> q"))
        // Headings: 24 before the large ones, 16 before the small, 8 after, 12 between two.
        assertEquals(listOf(0, 24, 8, 16, 8, 16, 8), gaps("a\n# h1\nb\n### h3\nc\n\n**bold**\n\nd"))
        assertEquals(listOf(0, 12, 12, 16, 16), gaps("# a\n## b\n**c**\n\n**d**\n\n### e"))
        // A rule is not a block, only 24 where it stood; not before the first block.
        assertEquals(listOf(0, 24, 24), gaps("---\na\n\n---\n\n## b\n***\nc\n\n---"))
        assertEquals(listOf(0, 16, 16), gaps("| a | b |\n| --- | --- |\n| 1 | 2 |\n\nc\n\n### d"))
        // Inside a quote and a list item headings get no space of their own.
        assertEquals(listOf(0, 12, 12, 12), gaps("a\n## h\n- b\n\nc", Place.QUOTE))
        assertEquals(listOf(4, 8, 8, 4), gaps("- a\n\nb\n\n```\nx\n```\n- c", Place.ITEM, afterText = true))
        assertEquals(listOf(0, 8), gaps("```\nx\n```\n\nb", Place.ITEM))
    }
}
