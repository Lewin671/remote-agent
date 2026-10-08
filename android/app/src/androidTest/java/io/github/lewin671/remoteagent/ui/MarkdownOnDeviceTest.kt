package io.github.lewin671.remoteagent.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Reading that depends on the device's own regular expressions. Android's
 * Pattern takes \w, \d and \b from Unicode where the JVM the unit tests run
 * on takes them from ASCII, so the shared cases passing there does not show
 * that a phone reads these the way the web client does.
 */
class MarkdownOnDeviceTest {
    private fun link(url: String) = Span.Link(url, url)

    @Test fun boundariesAreAsciiWordCharacters() {
        assertEquals(listOf(Span.Text("见"), link("https://example.com/a")), inlineSpans("见https://example.com/a"))
        assertEquals(listOf(Span.Text("见"), Span.Italic(listOf(Span.Text("说明"))), Span.Text("。")), inlineSpans("见*说明*。"))
        assertEquals(listOf(Span.Text("a*b*。 和 x_https://example.com")), inlineSpans("a*b*。 和 x_https://example.com"))
        assertEquals(listOf(Span.Text("中文_x_中文 和 ("), Span.Italic(listOf(Span.Text("y"))), Span.Text(") 和 _z_w")), inlineSpans("中文_x_中文 和 (_y_) 和 _z_w"))
    }

    @Test fun listNumbersAndQuantitiesAreAsciiDigits() {
        assertEquals(listOf(Block.Para("１. 全角数字不是序号")), parseMarkdown("１. 全角数字不是序号"))
        assertTrue(parseMarkdown("1. a\n2. b").single() is Block.ItemList)
        assertFalse(numericColumn(listOf("１２")))
        assertTrue(numericColumn(listOf("180 ms", "5.2 s", "+84 −61")))
    }

    @Test fun bareUrlsEndWhereTheSentenceDoes() {
        assertEquals(
            listOf(Span.Text("("), link("https://en.wikipedia.org/wiki/Go_(game)"), Span.Text(")")),
            inlineSpans("(https://en.wikipedia.org/wiki/Go_(game))"),
        )
        assertEquals(listOf(Span.Text("见 "), link("https://example.com/a"), Span.Text("，然后")), inlineSpans("见 https://example.com/a，然后"))
    }
}
