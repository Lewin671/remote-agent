import Testing

@testable import RAMarkdown

/// What the shared cases (CasesTests) do not say: time, nesting, table cells, column widths and spacing. Android's MarkdownTableTest.kt, less what it says about drawing.
@Suite struct ParserTests {
    private func rep(_ s: String, _ n: Int) -> String { String(repeating: s, count: n) }

    @Test(.timeLimit(.minutes(1))) func hostileTextIsReadInLinearTime() {
        for text in [rep("[", 100_000), rep("[x](", 25_000), rep("<https://", 20_000), rep("`", 100_000) + "x", rep("* ", 30_000), rep("_ ", 30_000), rep("~~", 30_000)] {
            #expect(Markdown.inlineSpans(text) == [.text(text)])
        }
        // Openers that find no closer, each of which could rescan what follows it.
        for text in [rep("**a ", 30_000), rep("*a ", 30_000), rep("_a ", 30_000), rep("~~a ", 30_000), rep("\\", 50_000) + "`" + rep("\\", 50_000) + "`"] {
            #expect(Markdown.shownText(Markdown.inlineSpans(text)).unicodeScalars.count <= text.unicodeScalars.count)
        }
        // A bare URL is cut from its end once, not once per character dropped.
        #expect(Markdown.inlineSpans("https://a.b/" + rep(")", 100_000)) == [.link(text: "https://a.b/", href: "https://a.b/"), .text(rep(")", 100_000))])
        // Lines that a backtracking pattern would read in quadratic time or worse.
        let n = 100_000
        #expect(Markdown.parse("# " + rep("#", n) + "x") == [.heading(level: 1, text: rep("#", n) + "x")])
        #expect(Markdown.parse("# a" + rep(" ", n) + "b " + rep("#", n)) == [.heading(level: 1, text: "a" + rep(" ", n) + "b")])
        #expect(Markdown.parse("a | b\n" + rep(" ", n) + "x") == [.paragraph("a | b\n" + rep(" ", n) + "x")])
        #expect(Markdown.parse(rep("- ", n) + "x") == [.list(ordered: false, start: 1, items: [MarkdownItem(text: rep("- ", n - 1) + "x")])])
        // Many short lines of each kind, and a list whose every blank line looks ahead.
        #expect(Markdown.parse(rep("- a\n\n", 20_000)).count == 1)
        #expect(Markdown.parse(rep("| a |\n", 20_000)).count == 1)
        #expect(Markdown.parse(rep("```\n", 20_001)).count == 10_001)
        // Quantities are scanned once, however many a cell strings together.
        #expect(Markdown.numericColumn([rep("1 ", n) + "1"]))
        #expect(!Markdown.numericColumn([rep("1 ", n) + "xyzabc"]))
        #expect(!Markdown.numericColumn(["1" + rep(" ", n) + "x"]))
        #expect(Markdown.tableCells(rep("|", n)).count == n - 1)
    }

    @Test(.timeLimit(.minutes(1))) func nestingStopsAtAFixedDepth() {
        func depth(_ blocks: [MarkdownBlock]) -> Int {
            1 + (blocks.map { b -> Int in
                switch b {
                case .quote(let inner): depth(inner)
                case .list(_, _, let items): items.map { depth($0.children) }.max() ?? 0
                default: 0
                }
            }.max() ?? 0)
        }
        #expect(depth(Markdown.parse(rep("> ", 5_000) + "x")) == 33)
        #expect(depth(Markdown.parse((0..<200).map { rep(" ", $0 * 2) + "- x" }.joined(separator: "\n"))) == 33)
    }

    @Test func aListNumberHasAtMostNineDigits() {
        #expect(Markdown.parse("999999999. a") == [.list(ordered: true, start: 999_999_999, items: [MarkdownItem(text: "a")])])
        #expect(Markdown.parse("1234567890. a") == [.paragraph("1234567890. a")])
    }

    @Test func cellsDoNotSplitEscapedPipesOrCodeSpans() {
        #expect(Markdown.tableCells("| a | `x|y` | c \\| d |") == ["a", "`x|y`", "c | d"])
        #expect(Markdown.tableCells("| ``x`|y`` | z |") == ["``x`|y``", "z"])
        #expect(Markdown.tableCells("| unfinished ` | z |") == ["unfinished `", "z"])
        #expect(Markdown.tableCells("| a\\\\| b |") == ["a\\\\", "b"])
        #expect(Markdown.tableCells("| a\\\\\\| | b |") == ["a\\\\|", "b"])
        #expect(Markdown.tableCells("| \\`not code | middle \\` | final |") == ["\\`not code", "middle \\`", "final"])
        #expect(Markdown.tableCells("| | value | |") == ["", "value", ""])
    }

    @Test func codeIsASpanAndEscapedTicksAreNot() {
        #expect(Markdown.inlineSpans("``x`|y``") == [.code("x`|y")])
        #expect(Markdown.inlineSpans("\\`not code | middle \\`") == [.text("`not code | middle `")])
    }

    @Test func onlyWebLinksCanBeOpened() {
        let spans = Markdown.inlineSpans("[a](https://a.b) [b](/tmp/x) [c](javascript:void) https://c.d.")
        #expect(Markdown.shownText(spans) == "a b c https://c.d.")
        #expect(spans == [
            .link(text: "a", href: "https://a.b"), .text(" "), .ref(text: "b", target: "/tmp/x"), .text(" "),
            .ref(text: "c", target: "javascript:void"), .text(" "), .link(text: "https://c.d", href: "https://c.d"), .text("."),
        ])
        #expect(Markdown.safeHref(" https://a.b ") == "https://a.b")
        #expect(Markdown.safeHref("HTTPS://a.b") == "HTTPS://a.b")
        for target in ["https://", "http://a b", "ftp://a.b", "javascript:alert(1)", "/tmp/x", "data:text/html,x", "file:///etc/passwd", "", "https:/a.b", "x-https://a.b"] {
            #expect(Markdown.safeHref(target) == nil, "\(target)")
        }
    }

    /// No span of any input may carry an href that `safeHref` would refuse.
    @Test func everyLinkInTheTreeIsAWebLink() {
        func hrefs(_ spans: [MarkdownSpan]) -> [String] {
            spans.flatMap { s -> [String] in
                switch s {
                case .link(_, let href): [href]
                case .bold(let c), .italic(let c), .strike(let c): hrefs(c)
                default: []
                }
            }
        }
        let text = "**[a](javascript:x)** _<https://a.b>_ ~~[b](HTTP://C.D)~~ *[c](data:x)* <javascript://x> http://e.f [d]( https://g.h )"
        // A target with spaces is no link, but the address inside it still is one.
        #expect(hrefs(Markdown.inlineSpans(text)) == ["https://a.b", "HTTP://C.D", "http://e.f", "https://g.h"])
    }

    @Test func diffColorsAddedAndRemovedLinesButNotFileHeaders() {
        let lines = ["--- a/x", "+++ b/x", "@@ -1 +1 @@", "-old", "+new", " same", "---n;", "+++n;", "---", "+++", ""]
        #expect(lines.map(Markdown.diffLine) == [.context, .context, .context, .removed, .added, .context, .removed, .added, .context, .context, .context])
    }

    @Test func compactTablesFitWithoutShrinkingNumbersAndWideTablesRemainScrollable() {
        func widths(_ preferred: [Int], _ viewport: Int, _ readable: Int) -> [Int] {
            Markdown.tableColumnWidths(preferred: preferred, viewport: viewport, readable: readable)
        }
        #expect(widths([240, 90, 60], 360, 96) == [210, 90, 60])
        #expect(widths([240, 240, 80], 360, 96) == [140, 140, 80])
        // Narrower than the viewport, a table keeps the width of what it holds.
        #expect(widths([60, 70], 360, 96) == [60, 70])
        #expect(widths([240, 240, 80], 272, 96) == [96, 96, 80])
        #expect(widths(Array(repeating: 120, count: 6), 320, 96) == Array(repeating: 120, count: 6))
        #expect(widths([240, 160, 120], 320, 192) == [240, 160, 120])
        let uneven = widths([239, 238, 120], 361, 96)
        #expect(uneven.reduce(0, +) == 361)
        #expect(uneven.allSatisfy { $0 >= 96 })
    }

    private func gaps(_ md: String, _ place: MarkdownPlace = .reply, afterText: Bool = false) -> [Int] {
        Markdown.spaced(Markdown.parse(md), in: place, afterText: afterText).map(\.gap)
    }

    @Test func spaceBetweenBlocksIsTheFirstRuleThatFits() {
        #expect(gaps("a\n\nb\n- c\n\nd\n\n```\nx\n```\n\n> q") == [0, 12, 8, 12, 12, 12])
        // Headings: 24 before the large ones, 16 before the small, 8 after, 12 between two.
        #expect(gaps("a\n# h1\nb\n### h3\nc\n\n**bold**\n\nd") == [0, 24, 8, 16, 8, 16, 8])
        #expect(gaps("# a\n## b\n**c**\n\n**d**\n\n### e") == [0, 12, 12, 16, 16])
        // A rule is not a block, only 24 where it stood; not before the first block.
        #expect(gaps("---\na\n\n---\n\n## b\n***\nc\n\n---") == [0, 24, 24])
        #expect(gaps("| a | b |\n| --- | --- |\n| 1 | 2 |\n\nc\n\n### d") == [0, 16, 16])
        // Inside a quote and a list item headings get no space of their own.
        #expect(gaps("a\n## h\n- b\n\nc", .quote) == [0, 12, 12, 12])
        #expect(gaps("- a\n\nb\n\n```\nx\n```\n- c", .item, afterText: true) == [4, 8, 8, 4])
        #expect(gaps("```\nx\n```\n\nb", .item) == [0, 8])
    }

    /// A block keeps its index while the reply grows after it, which is what its view is identified by.
    @Test func aBlocksIndexDoesNotMoveAsTheReplyGrows() {
        let full = "# 标题\n\n第一段\n\n---\n\n- 一\n- 二\n\n```sh\nls\n```\n\n| a | b |\n| --- | --- |\n| 1 | 2 |\n\n结尾"
        let whole = Markdown.spaced(Markdown.parse(full), in: .reply)
        #expect(whole.map(\.index) == [0, 1, 3, 4, 5, 6])
        var settled = 0
        var text = ""
        for c in full {
            text.append(c)
            let now = Markdown.spaced(Markdown.parse(text), in: .reply)
            // Everything but the last block is already what it will be at the end.
            for b in now.dropLast() where b.index < whole.count {
                if let end = whole.first(where: { $0.index == b.index }), end == b { settled += 1 }
            }
        }
        #expect(settled > full.count)
    }

    @Test(.timeLimit(.minutes(1))) func aLongReplyIsParsedAgainQuickly() {
        let section = "## 标题 `code`\n\n一段**加粗**的文字，带 [链接](https://example.com/a) 和 `行内代码`。\n\n- 一\n- 二\n  - 三\n\n```go\nfunc main() {}\n```\n\n| a | b |\n| --- | ---: |\n| x | 1 ms |\n\n"
        let text = rep(section, 700)
        #expect(text.utf8.count > 100_000)
        let clock = ContinuousClock()
        var blocks = 0
        let took = clock.measure { for _ in 0..<10 { blocks = Markdown.parse(text).count } }
        #expect(blocks == 700 * 5)
        // A debug build on a Mac reads 100 kB in a few milliseconds; the bound only catches a pass that stopped being linear.
        #expect(took < .seconds(5), "\(took) for ten parses")
        print("parse of \(text.utf8.count) bytes: \(took / 10)")
    }
}
