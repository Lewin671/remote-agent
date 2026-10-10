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
}
