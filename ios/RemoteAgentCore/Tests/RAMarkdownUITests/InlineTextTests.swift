import SwiftUI
import Testing

@testable import RAMarkdownUI

@Suite @MainActor struct InlineTextTests {
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
