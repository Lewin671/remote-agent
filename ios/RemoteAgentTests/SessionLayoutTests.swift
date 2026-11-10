import RACore
import RAProto
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

/// What only a layout can tell: whether a text was cut where it wraps, and whether a row still fits when
/// the text is large (docs/design-system.md §6 「长内容、大量内容与大字号」).
@MainActor
@Suite struct SessionLayoutTests {
    @MainActor
    private final class Flag {
        var on = false
    }

    /// Lays `view` out in a window `width` wide and waits until `done` holds, or a second.
    private func show<V: View>(_ view: V, width: CGFloat, until done: @MainActor () -> Bool) async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let host = UIHostingController(rootView: view)
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: width, height: 700)
        window.rootViewController = host
        window.isHidden = false
        defer { window.isHidden = true }
        for _ in 0..<100 where !done() { try await Task.sleep(for: .milliseconds(10)) }
    }

    private func cut(_ text: String, width: CGFloat = 320) async throws -> Bool {
        let flag = Flag()
        let view = CutText(text, style: T.sm.with(lineHeight: 1.6), color: Theme.fg, lines: 6, cut: Binding(get: { flag.on }, set: { flag.on = $0 }))
            .frame(maxWidth: .infinity, alignment: .leading)
        try await show(view, width: width) { flag.on }
        return flag.on
    }

    @Test func aPlanOfFewLinesIsStillCutWhereTheyWrapPastThePreview() async throws {
        // One paragraph: a single line to count, far more than six to draw.
        let paragraph = String(repeating: "先把筛选条件抽成一个 hook，再把预览表格换成虚拟列表，导出按钮在生成期间显示进度。", count: 8)
        #expect(!planFolds(paragraph, lines: 6))
        #expect(try await cut(planPreview(paragraph, lines: 6)))
        // A few long list items are the same.
        let items = (1...3).map { "\($0). " + String(repeating: "补上两万行的性能测试并更新截图和文档，", count: 6) }.joined(separator: "\n")
        #expect(!planFolds(items, lines: 6))
        #expect(try await cut(planPreview(items, lines: 6)))
        // What fits is not offered to be opened.
        #expect(try await !cut(planPreview("1. 抽出 hook\n2. 换成虚拟列表\n3. 更新文档", lines: 6)))
        // More lines than the preview takes is cut however it lays out.
        #expect(planFolds((1...7).map { "\($0). 步骤" }.joined(separator: "\n"), lines: 6))
    }

    private func dock(more: Int, size: DynamicTypeSize, brief: String, open: Bool = false, note: String = "") -> some View {
        let question = PBQuestionAsked.with {
            $0.questionID = "Q"
            $0.questions = [.with { $0.text = brief }]
        }
        return PendingDock(
            .question(question), agent: "Claude Code", more: more, note: DockNote(text: note), open: .constant(open), onOpen: {}
        ) {
            EmptyView()
        } actions: {
            EmptyView()
        }
        .environment(\.dynamicTypeSize, size)
    }

    @MainActor
    private final class Measured {
        var size: CGSize?
    }

    /// The size the dock takes in a window `width` wide: wider than that, something does not fit.
    /// `settle`: for a view that measures itself and then changes, the size a third of a second on.
    private func fitted(_ view: some View, width: CGFloat, settle: Bool = false) async throws -> CGSize {
        let measured = Measured()
        let root = view.onGeometryChange(for: CGSize.self) { $0.size } action: { measured.size = $0 }
            .frame(maxHeight: .infinity, alignment: .top)
        var ticks = 0
        try await show(root, width: width) {
            ticks += 1
            return measured.size != nil && (!settle || ticks > 30)
        }
        return try #require(measured.size)
    }

    @Test func theDockLineStaysWithinANarrowScreenAtTheLargestTextSizes() async throws {
        let brief = "换成哪一种缓存？Redis 已经在用，内存 LRU 最简单"
        for size in [DynamicTypeSize.accessibility1, .accessibility3, .accessibility5] {
            for width in [320.0, 375, 402] {
                let took = try await fitted(dock(more: 12, size: size, brief: brief), width: width)
                #expect(took.width <= width + 0.5, "\(size) in \(width): 处理 is pushed off the screen")
            }
        }
    }

    @Test func theDockLineIsOneLineWhereItFits() async throws {
        let brief = "换成哪一种缓存？Redis 已经在用，内存 LRU 最简单，Memcached 要多一台机器"
        let took = try await fitted(dock(more: 2, size: .large, brief: brief), width: 390)
        // The summary gives way; the line does not grow past the 44 pt it is to the finger (and the 8 under the capsule).
        #expect(took.width <= 390.5 && took.height < 58)
        #expect(try await fitted(dock(more: 0, size: .large, brief: ""), width: 390).height < 58)
    }

    @Test func aCardsCommandKeepsFiveWholeLinesAndSaysHowManyMore() async throws {
        #expect(linesPast(5, whole: 14 + 10 * 18 - 4, one: 14 + 14, two: 14 + 32) == 5)
        #expect(linesPast(5, whole: 14 + 14, one: 14 + 14, two: 14 + 32) == 0)
        #expect(linesPast(5, whole: 100, one: 20, two: 20) == 0)
        let long = (1...10).map { "echo \($0)" }.joined(separator: "\n")
        let short = (1...4).map { "echo \($0)" }.joined(separator: "\n")
        let counts = Counts()
        let held = try await fitted(CountedCommand(text: long, lines: 5, counts: counts), width: 320, settle: true)
        #expect(counts.more == 5 && counts.total == 10)
        let whole = try await fitted(CommandBlock(long), width: 320)
        let five = try await fitted(CommandBlock((1...5).map { "echo \($0)" }.joined(separator: "\n")), width: 320)
        // Five whole lines: as tall as a command of five, shorter than all ten.
        #expect(held.height < whole.height && abs(held.height - five.height) < 0.5)
        // What fits is whole, with nothing past it.
        let fits = Counts()
        _ = try await fitted(CountedCommand(text: short, lines: 5, counts: fits), width: 320, settle: true)
        #expect(fits.more == 0 && fits.total == 4)
        // A command of one line is one line: whole, so 允许 can be offered (a count of 0 would keep it away).
        let one = Counts()
        _ = try await fitted(CountedCommand(text: "go test ./... -count=1", lines: 5, counts: one), width: 320, settle: true)
        #expect(one.total == 1 && one.more == 0)
        // One long command is one line to count and several on the screen.
        let wide = Counts()
        _ = try await fitted(CountedCommand(text: String(repeating: "x", count: 400), lines: 5, counts: wide), width: 320, settle: true)
        #expect(wide.total > 5 && wide.more == wide.total - 5)
    }

    @MainActor private final class Counts {
        var more = 0
        var total = 0
    }

    private struct CountedCommand: View {
        let text: String
        let lines: Int
        let counts: Counts
        @State private var more = 0
        @State private var total = 0

        var body: some View {
            CommandBlock(text, lines: lines, more: $more, total: $total)
                .onChange(of: more, initial: true) { _, n in counts.more = n }
                .onChange(of: total, initial: true) { _, n in counts.total = n }
        }
    }

    @Test func anOpenItemSaysItsTypeOnceOnOneLine() async throws {
        // Open, the line holds the type, how many more wait and the note, and 收起: the summary is not repeated under it.
        let open = dock(more: 3, size: .large, brief: "换成哪一种缓存？", open: true, note: "第 1 题，共 2 题 · 可多选")
        let took = try await fitted(open, width: 390)
        // As tall as an open item with nothing after its type: the line did not wrap.
        let bare = try await fitted(dock(more: 0, size: .large, brief: "", open: true), width: 390)
        #expect(took.width <= 390.5 && abs(took.height - bare.height) < 0.5)
        for size in [DynamicTypeSize.accessibility1, .accessibility5] {
            let large = dock(more: 12, size: size, brief: "", open: true, note: "第 1 题，共 2 题 · 可多选")
            #expect(try await fitted(large, width: 320).width <= 320.5, "\(size): 收起 is pushed off the screen")
        }
    }
}
