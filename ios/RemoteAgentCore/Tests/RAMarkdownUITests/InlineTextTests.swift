import SwiftUI
import Testing

@testable import RAMarkdownUI

@Suite @MainActor struct InlineTextTests {
    @Test func destinationsRenderWithoutStrayParenthesesAndUnsafeTargetsStayInert() {
        let source = "[x](<https://example.com/a(b(c))> \"title\") [y](javascript:alert(1)) [z](https://en.wikipedia.org/wiki/Foo_(bar))"
        for scheme in [ColorScheme.light, .dark] {
            let theme = MarkdownTheme.standard(for: scheme)
            let spec = theme.body(.primary)
            let look = Look(theme: theme, scale: 1)
            for chips in [false, true] {
                let builder = InlineText(source: source, lines: true, spec: spec, look: look).build(chips: chips)
                var rendered = AttributedString()
                for piece in builder.pieces {
                    switch piece {
                    case .plain(let a), .code(let a): rendered += a
                    }
                }
                #expect(String(rendered.characters) == "x y z")
                let links = rendered.runs.filter { $0.link != nil }
                #expect(links.count == 2)
                #expect(links.map { $0.link?.absoluteString } == ["https://example.com/a(b(c))", "https://en.wikipedia.org/wiki/Foo_(bar)"])
                #expect(links.map { String(rendered[$0.range].characters) } == ["x", "z"])
            }
        }
    }

    @Test func paragraphLinesKeepCodeSeparateAndMultilineMathTogether() {
        for scheme in [ColorScheme.light, .dark] {
            let theme = MarkdownTheme.standard(for: scheme)
            let spec = theme.body(.primary)
            let look = Look(theme: theme, scale: 1)
            for chips in [false, true] {
                let source = "a `b\nc` d\na **b\nc** d\nbefore `\n$$\nx_1 + y^2\n$$\nafter `"
                let builder = InlineText(source: source, lines: true, spec: spec, look: look).build(chips: chips)
                var rendered = AttributedString()
                for piece in builder.pieces {
                    switch piece {
                    case .plain(let a), .code(let a): rendered += a
                    }
                }
                #expect(String(rendered.characters) == "a `b\nc` d\na **b\nc** d\nbefore `\nx₁ + y²\nafter `")
                #expect(!builder.hasChips)
                let bodyFont = rendered.runs.first?.swiftUI.font
                #expect(bodyFont != nil)
                #expect(rendered.runs.allSatisfy { $0.swiftUI.font == bodyFont })
                let formulas = rendered.runs.filter { $0.swiftUI.backgroundColor == theme.chip }
                #expect(formulas.count == 1)
                #expect(formulas.map { String(rendered[$0.range].characters) } == ["x₁ + y²"])
            }
            #expect(InlineText(source: "a `b` d", lines: true, spec: spec, look: look).build(chips: true).hasChips)
        }
    }
}
