import RAMarkdown
import SwiftUI

#if canImport(UIKit)
import UIKit
#elseif canImport(AppKit)
import AppKit
#endif

/// Puts `text` on the system's clipboard; false when it was refused.
@MainActor private func copyToClipboard(_ text: String) -> Bool {
    #if canImport(UIKit)
    UIPasteboard.general.string = text
    return true
    #elseif canImport(AppKit)
    NSPasteboard.general.clearContents()
    return NSPasteboard.general.setString(text, forType: .string)
    #else
    return false
    #endif
}

/// A code block in a reply: soft, no line around it, radius 6, mono 12,
/// never wrapped: a line too long scrolls sideways inside the block. A long
/// block shows its first lines until it is opened in place.
struct CodeBlockView: View, Equatable {
    let lang: String
    let text: String
    let look: Look

    private enum Copied {
        case no, ok, failed
    }

    private var theme: MarkdownTheme { look.theme }
    private var scale: CGFloat { look.scale }
    // Kept with the view, which is identified by the block's place in the reply: a block stays open while the reply grows.
    @State private var open = false
    @State private var copied = Copied.no

    nonisolated static func == (a: CodeBlockView, b: CodeBlockView) -> Bool {
        a.lang == b.lang && a.text == b.text && a.look == b.look
    }

    /// Lines of code as one text; in a diff the added and removed lines are colored, the file headers are not.
    private func code(_ lines: ArraySlice<Substring>) -> Text {
        // Only a diff is colored: added and removed lines mean something.
        guard lang == "diff" || lang == "patch" else { return Text(verbatim: lines.joined(separator: "\n")) }
        var out = AttributedString()
        for (i, l) in lines.enumerated() {
            var a = AttributedString((i > 0 ? "\n" : "") + l)
            switch Markdown.diffLine(String(l)) {
            case .added: a.swiftUI.foregroundColor = theme.added
            case .removed: a.swiftUI.foregroundColor = theme.removed
            case .context: break
            }
            out += a
        }
        return Text(out)
    }

    var body: some View {
        let lines = text.split(separator: "\n", omittingEmptySubsequences: false)
        let long = lines.count > Markdown.foldOver
        VStack(alignment: .leading, spacing: 0) {
            ScrollView(.horizontal, showsIndicators: false) {
                code(long && !open ? lines.prefix(Markdown.foldShown) : lines[...])
                    .font(.system(size: theme.codeSize * scale, design: .monospaced))
                    .foregroundStyle(theme.secondary)
                    .lineBox(height: theme.codeSize * theme.codeLineHeight * scale, fontSize: theme.codeSize * scale)
                    // Its own width, however long a line is: the block scrolls, the code does not wrap.
                    .fixedSize()
                    // Room at the end for the copy button: scrolled all the way, no code is left under it.
                    .padding(EdgeInsets(top: 8, leading: 12, bottom: 8, trailing: 44))
            }
            .scrollBounceBehavior(.basedOnSize, axes: .horizontal)
            if long { fold(lines.count) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(theme.soft)
        .clipShape(RoundedRectangle(cornerRadius: 6))
        // Over the corner without adding to the block's height: one line of code is lower than the button.
        .overlay(alignment: .topTrailing) { copyButton }
    }

    private func fold(_ count: Int) -> some View {
        Button {
            open.toggle()
        } label: {
            HStack(spacing: 2) {
                Text(verbatim: open ? theme.strings.collapse : theme.strings.expand)
                    .font(.system(size: 13 * scale, weight: .semibold))
                    .foregroundStyle(theme.link)
                IconView(icon: open ? .chevronUp : .chevronDown, size: 13 * scale, color: theme.link)
                Spacer(minLength: 8)
                Text(verbatim: String(format: theme.strings.lineCount, count))
                    .font(.system(size: 12 * scale))
                    .foregroundStyle(theme.muted)
                    .lineLimit(1)
            }
            .padding(.horizontal, 12)
            .frame(maxWidth: .infinity, minHeight: 44)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    /// Copies the block; the icon says for a moment how it went.
    private var copyButton: some View {
        Button {
            copied = copyToClipboard(text) ? .ok : .failed
        } label: {
            IconView(icon: copied == .ok ? .check : copied == .failed ? .x : .copy, size: 16, color: theme.muted)
                .padding(EdgeInsets(top: 9, leading: 0, bottom: 0, trailing: 12))
                .frame(width: 44, height: 44, alignment: .topTrailing)
                // Code that runs under the icon fades out before it.
                .background(alignment: .top) {
                    LinearGradient(stops: [.init(color: theme.soft.opacity(0), location: 0), .init(color: theme.soft, location: 0.4)], startPoint: .leading, endPoint: .trailing)
                        .frame(height: 34)
                        .clipShape(UnevenRoundedRectangle(topTrailingRadius: 6))
                }
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(copied == .ok ? theme.strings.copied : copied == .failed ? theme.strings.copyFailed : theme.strings.copy)
        .task(id: copied) {
            guard copied != .no else { return }
            try? await Task.sleep(for: .seconds(1.5))
            if !Task.isCancelled { copied = .no }
        }
    }
}
