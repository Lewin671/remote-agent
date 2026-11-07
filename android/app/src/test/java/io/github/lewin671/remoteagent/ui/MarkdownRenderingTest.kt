package io.github.lewin671.remoteagent.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class MarkdownRenderingTest {
    @Test fun longBoldRunsDoNotOverflowTheJvmStack() {
        val plain = "a".repeat(10_000)
        assertEquals(listOf(Span.Bold(listOf(Span.Text(plain)))), inlineSpans("**$plain**"))
        assertTrue(boldHeading("**$plain**"))
        val italics = " *b* ".repeat(2_000)
        val source = "**$italics**"
        val bold = inlineSpans(source).single() as Span.Bold
        assertEquals(2_000, bold.c.count { it is Span.Italic })
        assertTrue(boldHeading(source))
    }

    @Test fun paragraphLinesKeepCodeSeparateAndMultilineMathTogether() {
        for (palette in listOf(LightPalette, DarkPalette)) {
            val source = "a `b\nc` d\na **b\nc** d\nbefore `\n\$\$\nx_1 + y^2\n\$\$\nafter `"
            val rendered = richLines(source, palette)
            assertEquals("a `b\nc` d\na **b\nc** d\nbefore `\nx₁ + y²\nafter `", rendered.text)
            assertTrue(rendered.getStringAnnotations("code", 0, rendered.length).isEmpty())
            val formulas = rendered.spanStyles.filter { it.item.background == palette.chip }
            assertEquals(1, formulas.size)
            val formula = formulas.single()
            assertEquals("x₁ + y²", rendered.text.substring(formula.start, formula.end))
            assertNull(formula.item.fontFamily)
            assertTrue(rendered.spanStyles.none { it.item.fontWeight != null || it.item.fontStyle != null })
            val code = richLines("a `b` d", palette)
            assertEquals(1, code.getStringAnnotations("code", 0, code.length).size)
        }
    }
}
