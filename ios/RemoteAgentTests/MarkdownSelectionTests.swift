import RAMarkdownUI
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

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
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
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

/// Test the actual selection page's viewport and UIKit copy operation,
/// rather than the presence of private SwiftUI selection views alone.
@MainActor
@Suite(.serialized) struct MessageTextSelectionTests {
    private let message = "第一段：中文 👩🏽‍💻 e\u{301}。\n\n- 列表 **粗体**\n- `restartIce()`\n\n" +
        String(repeating: "下一段包含多行文字，可以跨段落选择并复制。\n", count: 150) + "最后一段。"

    private func withPage(
        text: String, width: CGFloat = 390, size: DynamicTypeSize = .large,
        check: (UIHostingController<AnyView>, UITextView) async throws -> Void
    ) async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let host = UIHostingController(rootView: AnyView(MessageTextSelection(text: text, onDone: {}).dynamicTypeSize(size).environment(\.colorScheme, .light)))
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: width, height: 700)
        window.rootViewController = host
        window.isHidden = false
        defer { window.isHidden = true }
        try await Task.sleep(for: .milliseconds(300))
        func find(_ view: UIView) -> UITextView? {
            if let text = view as? UITextView { return text }
            return view.subviews.lazy.compactMap { find($0) }.first
        }
        let textView = try #require(find(host.view))
        try await check(host, textView)
    }

    @Test(arguments: [CGFloat(320), 390, 744])
    func longTextHasItsOwnBoundedViewport(width: CGFloat) async throws {
        try await withPage(text: message, width: width) { host, view in
            #expect(view.isSelectable && !view.isEditable && view.isScrollEnabled)
            #expect(view.bounds.height > 200 && view.bounds.height < host.view.bounds.height)
            #expect(view.bounds.width <= width)
            #expect(view.contentSize.height > view.bounds.height * 2)
            var parent = view.superview
            while let ancestor = parent {
                #expect(!(ancestor is UIScrollView), "An outer scroll view steals selection scrolling")
                parent = ancestor.superview
            }
            let end = NSRange(location: (message as NSString).length - 6, length: 6)
            view.selectedRange = end
            view.scrollRangeToVisible(end)
            try await Task.sleep(for: .milliseconds(300))
            #expect(view.contentOffset.y > 0, "A selection at the end must scroll the text viewport")
            #expect(view.selectedRange == end)
        }
    }

    @Test func aSelectionCopiesExactUnicodeAndParagraphs() async throws {
        try await withPage(text: message) { _, view in
            let selected = "中文 👩🏽‍💻 e\u{301}。\n\n- 列表 **粗体**\n- `restartIce()`"
            let range = (message as NSString).range(of: selected)
            #expect(range.location != NSNotFound)
            view.becomeFirstResponder()
            view.selectedRange = range
            let old = UIPasteboard.general.items
            defer { UIPasteboard.general.items = old }
            view.copy(nil)
            #expect(UIPasteboard.general.string == selected)
            #expect(view.text == message)
        }
    }

    @Test func updatingThePageKeepsTheSelectionAndScrollPosition() async throws {
        try await withPage(text: message) { host, view in
            let range = (message as NSString).range(of: "最后一段。")
            view.selectedRange = range
            view.scrollRangeToVisible(range)
            try await Task.sleep(for: .milliseconds(300))
            let offset = view.contentOffset.y
            #expect(offset > 100)
            host.rootView = AnyView(MessageTextSelection(text: message, onDone: {}).dynamicTypeSize(.large).environment(\.colorScheme, .dark))
            try await Task.sleep(for: .milliseconds(300))
            #expect(view.isDescendant(of: host.view), "The test must inspect the text view still on screen")
            #expect(view.selectedRange == range)
            // TextKit can round layout during a trait update. It must
            // keep the same visible lines rather than jump to the top.
            let font = try #require(view.font)
            #expect(abs(view.contentOffset.y - offset) < font.lineHeight)
        }
    }

    @Test func largeTextStillFitsAndScales() async throws {
        try await withPage(text: message, width: 320, size: .accessibility5) { host, view in
            #expect(view.bounds.width <= 320 && view.bounds.height > 100)
            #expect(view.bounds.height < host.view.bounds.height)
            let font = try #require(view.font)
            #expect(font.pointSize > 17)
        }
    }

    @Test func theMenuPreviewDoesNotGrowToTheLengthOfAReply() async throws {
        let host = UIHostingController(rootView: MessageMenuPreview(text: message))
        let size = host.sizeThatFits(in: CGSize(width: 390, height: CGFloat.greatestFiniteMagnitude))
        #expect(size.height < 160, "The menu must not preview a screenful of the reply")
        #expect(size.width <= 390)
    }
}
