import RAMarkdownUI
import SwiftUI
import Testing
import UIKit

/// A reply in the chat has a long-press menu of its own (复制, 选择文字). Text
/// that can be selected answers the same gesture with views of its own over
/// every piece of text, and theirs wins there. So a reply in the chat is
/// drawn with selection off, and this holds it to having nothing of the
/// kind left: the menu's is then the only long press there is.
///
/// It looks at what SwiftUI builds, by the name of a private class. Should
/// a later system name it otherwise, the first expectation fails and says
/// that this needs another look, rather than the second passing for nothing.
@MainActor
@Suite struct MarkdownSelectionTests {
    private func selectionViews(selectable: Bool) async throws -> Int {
        let reply = MarkdownView("第一段文字。\n\n- 列表一\n- 列表二\n\n`code` 和 **粗体**", selectable: selectable)
            .padding(20)
            .contextMenu { Button("复制") {} }
        let host = UIHostingController(rootView: reply)
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 600)
        window.rootViewController = host
        window.isHidden = false
        defer { window.isHidden = true }
        // The views behind selectable text are made after the first layout.
        try await Task.sleep(for: .milliseconds(500))
        func count(_ view: UIView) -> Int {
            (String(describing: type(of: view)).contains("TextSelection") ? 1 : 0) + view.subviews.reduce(0) { $0 + count($1) }
        }
        return count(host.view)
    }

    @Test func aReplyWithAMenuOfItsOwnLeavesTheLongPressToTheMenu() async throws {
        #expect(try await selectionViews(selectable: true) > 0, "selectable text is no longer found by this name: look again")
        #expect(try await selectionViews(selectable: false) == 0)
    }
}
