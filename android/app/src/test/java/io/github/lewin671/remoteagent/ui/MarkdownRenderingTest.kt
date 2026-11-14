package io.github.lewin671.remoteagent.ui

import androidx.compose.ui.text.LinkAnnotation
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class MarkdownRenderingTest {
    @Test fun destinationsRenderWithoutStrayParenthesesAndUnsafeTargetsStayInert() {
        val source = "[x](<https://example.com/a(b(c))> \"title\") [y](javascript:alert(1)) [z](https://en.wikipedia.org/wiki/Foo_(bar))"
        for (palette in listOf(LightPalette, DarkPalette)) {
            val rendered = richLines(source, palette)
            assertEquals("x y z", rendered.text)
            val links = rendered.getLinkAnnotations(0, rendered.length)
            assertEquals(2, links.size)
            assertEquals("https://example.com/a(b(c))", (links[0].item as LinkAnnotation.Url).url)
            assertEquals("x", rendered.text.substring(links[0].start, links[0].end))
            assertEquals("https://en.wikipedia.org/wiki/Foo_(bar)", (links[1].item as LinkAnnotation.Url).url)
            assertEquals("z", rendered.text.substring(links[1].start, links[1].end))
        }
    }

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
            val formulas = rendered.spanStyles.filter { it.item.background == palette.fill2 }
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
