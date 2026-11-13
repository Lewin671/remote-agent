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
        let view = CutText(text, style: .subheadline, color: DS.ink, lines: 6, cut: Binding(get: { flag.on }, set: { flag.on = $0 }))
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

    private func dock(more: Int, size: DynamicTypeSize, brief: String, open: Bool = false, note: String = "", title: String? = nil) -> some View {
        let question = PBQuestionAsked.with {
            $0.questionID = "Q"
            $0.questions = [.with { $0.text = brief }]
        }
        return PendingDock(
            .question(question), agent: "Claude Code", more: more, title: title, note: DockNote(text: note), open: .constant(open), onOpen: {}
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

    private struct ComposerHint: View {
        let hint: String
        var text = ""
        @FocusState private var focus: Bool

        var body: some View {
            MessageCapsule(text: .constant(text), placeholder: hint, label: hint, focus: $focus) {
                EmptyView()
            } plus: {
                PlusCircle()
            } trailing: {
                InkCircle(symbol: "arrow.up")
            }
        }
    }

    @Test func emptyComposerShowsTheWholeHintAndTypingReleasesItsSpace() async throws {
        let typed = try await fitted(ComposerHint(hint: "", text: "x"), width: 320)
        for hint in [L10n.composerQueueAfterAnswer(locale: "en"), L10n.composerPlanPlaceholder(locale: "en")] {
            let empty = try await fitted(ComposerHint(hint: hint), width: 320, settle: true)
            #expect(empty.width <= 320.5)
            #expect(empty.height > typed.height + 15)
            #expect(abs(try await fitted(ComposerHint(hint: hint, text: "x"), width: 320).height - typed.height) < 0.5)
        }
        let wideTyped = try await fitted(ComposerHint(hint: "", text: "x"), width: 390)
        for hint in [L10n.composerQueueAfterAnswer(locale: "zh-CN"), L10n.composerPlanPlaceholder(locale: "zh-CN")] {
            #expect(abs(try await fitted(ComposerHint(hint: hint), width: 390).height - wideTyped.height) < 0.5)
        }
    }

    @Test func questionProgressAndTerminalLinkGrowWhenEnglishNeedsAnotherLine() async throws {
        // SwiftUI renders outside the caller's TaskLocal context: pass the fixed title/note explicitly.
        let title = L10n.listStateAgentAsking(agent: "Claude Code", locale: "en")
        let progress = MessageLocale.$overrideForTesting.withValue("en") {
            questionProgress(count: 2, at: 0, multi: false)
        }
        let narrow = try await fitted(dock(more: 0, size: .large, brief: "", open: true, note: progress, title: title), width: 320, settle: true)
        let wide = try await fitted(dock(more: 0, size: .large, brief: "", open: true, note: progress, title: title), width: 600, settle: true)
        #expect(narrow.width <= 320.5 && narrow.height > wide.height + 1)
        var footHeights: [CGFloat] = []
        for locale in ["zh-CN", "en"] {
            footHeights.append(try await fitted(DockFoot(L10n.dockNoneFit(locale: locale),
                link: L10n.dockOtherTerminal(locale: locale), arrow: true) {}, width: 260).height)
        }
        #expect(footHeights[1] > footHeights[0] + 10)
    }

    @Test func scheduleHintsWrapWhileTheNameRemainsASingleLine() async throws {
        for hint in [L10n.scheduleIosWhatToCheckNotifyMeWhenThereAreResults(locale: "en"),
                     L10n.scheduleLeaveBlankToUseTheMessageSFirstSentence(locale: "en")] {
            let empty = try await fitted(InputField(text: .constant(""), placeholder: hint, wrapPlaceholder: true), width: 358, settle: true)
            let typed = try await fitted(InputField(text: .constant("x"), placeholder: hint, wrapPlaceholder: true), width: 358)
            let longName = try await fitted(InputField(text: .constant(String(repeating: "name ", count: 40)), placeholder: hint, wrapPlaceholder: true), width: 358)
            #expect(empty.height > typed.height + 15)
            #expect(abs(longName.height - typed.height) < 0.5)
        }
        for hint in [L10n.scheduleIosWhatToCheckNotifyMeWhenThereAreResults(locale: "zh-CN"),
                     L10n.scheduleLeaveBlankToUseTheMessageSFirstSentence(locale: "zh-CN")] {
            let original = try await fitted(InputField(text: .constant(""), placeholder: hint), width: 358)
            let wrapped = try await fitted(InputField(text: .constant(""), placeholder: hint, wrapPlaceholder: true), width: 358, settle: true)
            #expect(abs(original.height - wrapped.height) < 0.5)
        }
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

    @Test func anOpenItemKeepsShortNotesInlineAndWrapsCompleteLongNotes() async throws {
        // Open, the line holds the type, how many more wait and the note, and 收起: the summary is not repeated under it.
        let open = dock(more: 3, size: .large, brief: "换成哪一种缓存？", open: true, note: "第 1 题，共 2 题 · 可多选")
        let took = try await fitted(open, width: 390, settle: true)
        // Long fixed progress is now complete rather than truncated to preserve one line.
        let bare = try await fitted(dock(more: 0, size: .large, brief: "", open: true), width: 390, settle: true)
        let short = try await fitted(dock(more: 0, size: .large, brief: "", open: true, note: "第 1 题，共 2 题"), width: 390, settle: true)
        #expect(took.width <= 390.5 && took.height > bare.height + 1)
        #expect(abs(short.height - bare.height) < 0.5)
        let metrics = ["short": short.height, "bare": bare.height, "long": took.height]
        let directory = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
        try JSONEncoder().encode(metrics).write(to: directory.appendingPathComponent("layout-progress.json"))
        for size in [DynamicTypeSize.accessibility1, .accessibility5] {
            let large = dock(more: 12, size: size, brief: "", open: true, note: "第 1 题，共 2 题 · 可多选")
            #expect(try await fitted(large, width: 320).width <= 320.5, "\(size): 收起 is pushed off the screen")
        }
    }

    @Test func choicesHeadingsAndQuotaRowsStayWithinTheScreenAtAccessibilitySizes() async throws {
        // The computer page used to grow wider than the screen at these sizes: its CLI picker has a fixed width.
        let names = ["Codex", "Claude Code", "Antigravity"]
        for size in [DynamicTypeSize.accessibility3, .accessibility5] {
            for width in [320.0, 390] {
                let pick = SegPick(names, marks: [.codex, .claude, .antigravity], selected: .constant(0)).environment(\.dynamicTypeSize, size)
                let picked = try await fitted(pick, width: width)
                #expect(picked.width <= width + 0.5, "\(size) in \(width): the CLI picker is wider than the screen")
                let heading = GroupLabel("Computer", action: "All devices") {}.environment(\.dynamicTypeSize, size)
                #expect(try await fitted(heading, width: width).width <= width + 0.5, "\(size) in \(width): the heading")
                let row = GRow("Claude Code", value: "· 5 hr", chevron: true, valueLines: 1) {}
                    .valueLead { Text("8% left").font(.subheadline) }
                    .environment(\.dynamicTypeSize, size)
                #expect(try await fitted(row, width: width).width <= width + 0.5, "\(size) in \(width): the quota row")
            }
        }
        // At the regular size it is still one line.
        let regular = try await fitted(SegPick(names, marks: [.codex, .claude, .antigravity], selected: .constant(0)), width: 390)
        let one = try await fitted(SegPick(["Codex"], marks: [.codex], selected: .constant(0)), width: 390)
        #expect(abs(regular.height - one.height) < 0.5)
    }
}
