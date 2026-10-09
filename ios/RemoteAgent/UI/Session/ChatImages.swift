// Images in the chat: the previews the events carry, drawn in place
// (architecture §17, docs/session-images.md). Android's ui/ChatImages.kt.
//
// Only the preview bytes inside an event are drawn here: nothing is
// fetched, so a row with images costs what its few kilobytes cost to
// decode, once. A tap opens the viewer (`Route.image`), which is where the
// display copy is fetched and which is not this file's.
import RACore
import RAProto
import SwiftUI
import UIKit

/// The previews, decoded once and kept while there is memory for them.
@MainActor
private enum ChatPreviews {
    private static let cache: NSCache<NSString, UIImage> = {
        let c = NSCache<NSString, UIImage>()
        c.countLimit = 200
        return c
    }()

    /// The preview embedded in an image ref; nil without one or when it does not decode.
    static func image(_ ref: PBImageRef) -> UIImage? {
        if ref.preview.isEmpty { return nil }
        let key = "\(ref.imageID)/\(ref.preview.count)/\(ref.path)" as NSString
        if let held = cache.object(forKey: key) { return held }
        guard let image = UIImage(data: ref.preview) else { return nil }
        cache.setObject(image, forKey: key)
        return image
    }
}

/// Thumbnails side by side at one height, each as wide as its picture; when
/// they do not fit the width they all shrink together.
private struct ThumbStrip: Layout {
    let aspects: [CGFloat]
    let height: CGFloat
    let maxWidth: CGFloat
    let spacing: CGFloat

    private func sizes(_ width: CGFloat?) -> [CGSize] {
        let ideal = aspects.map { min(maxWidth, height * $0) }
        let gaps = spacing * CGFloat(max(0, ideal.count - 1))
        let total = ideal.reduce(0, +)
        guard let width, total + gaps > width, total > 0 else { return ideal.map { CGSize(width: $0, height: height) } }
        let k = max(0.1, (width - gaps) / total)
        return ideal.map { CGSize(width: $0 * k, height: height * k) }
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let s = sizes(proposal.width)
        return CGSize(width: s.reduce(0) { $0 + $1.width } + spacing * CGFloat(max(0, s.count - 1)), height: s.first?.height ?? 0)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX
        for (view, size) in zip(subviews, sizes(bounds.width)) {
            view.place(at: CGPoint(x: x, y: bounds.minY), proposal: ProposedViewSize(size))
            x += size.width + spacing
        }
    }
}

/// A message's or a tool's images: previews at their own proportions, four
/// to a row at most, the rest folded into +N. The corner number is the
/// CLI's [Image #N], so it matches the text and the terminal. A tap opens
/// the viewer at that image.
struct ChatImageRow: View {
    private let refs: [PBImageRef]
    private let height: CGFloat
    private let onOpen: (Int) -> Void

    init(_ refs: [PBImageRef], height: CGFloat = 88, onOpen: @escaping (Int) -> Void) {
        self.refs = refs
        self.height = height
        self.onOpen = onOpen
    }

    var body: some View {
        let shown = Array(refs.prefix(4))
        ThumbStrip(aspects: shown.map { CGFloat(aspect($0)) }, height: height, maxWidth: 180, spacing: 6) {
            ForEach(Array(shown.enumerated()), id: \.offset) { i, ref in
                ChatThumb(ref: ref, more: i == 3 && refs.count > 4 ? refs.count - 4 : 0) { onOpen(i) }
            }
        }
    }
}

private struct ChatThumb: View {
    let ref: PBImageRef
    let more: Int
    let onOpen: () -> Void

    private func badge(_ text: String) -> some View {
        Text(text).type(T.tab, Theme.onSolid).lineLimit(1)
            .padding(.horizontal, 5)
            .background(Color.black.opacity(0.6), in: RoundedRectangle(cornerRadius: Theme.Radius.xs, style: .continuous))
            .padding(4)
    }

    var body: some View {
        let preview = ChatPreviews.image(ref)
        Button(action: onOpen) {
            Theme.chip
                .overlay {
                    if let preview {
                        Image(uiImage: preview).resizable().scaledToFill()
                    } else {
                        // Not even a preview: the file's name is all there is.
                        VStack(spacing: 0) {
                            Text(ref.sync == .nameOnly ? "图片没有同步" : "没有预览").type(T.tab, Theme.muted)
                            if !ref.path.isEmpty { Text(fileName(ref.path)).type(T.tab, Theme.faint).lineLimit(2) }
                        }
                        .multilineTextAlignment(.center).padding(6)
                    }
                }
                .overlay(alignment: .topLeading) { if ref.cliIndex > 0 { badge("\(ref.cliIndex)") } }
                .overlay(alignment: .bottomLeading) {
                    // On a thumbnail too narrow for the words the viewer says it instead.
                    if ref.sync == .previewOnly && more == 0 {
                        ViewThatFits(in: .horizontal) {
                            badge("只有预览").fixedSize()
                            Color.clear.frame(width: 0, height: 0)
                        }
                    }
                }
                .overlay {
                    if more > 0 {
                        Color.black.opacity(0.53).overlay { Text("+\(more)").type(T.h17, Theme.onSolid) }
                    }
                }
                .clipShape(RoundedRectangle(cornerRadius: Theme.Radius.sm, style: .continuous))
                .contentShape(Rectangle())
        }
        .buttonStyle(PressStyle())
        .accessibilityLabel(more > 0 ? "图片，还有 \(more) 张" : ref.cliIndex > 0 ? "图片 \(ref.cliIndex)" : "图片")
        .accessibilityValue(ref.path.isEmpty ? "" : fileName(ref.path))
        .accessibilityHint("打开图片")
    }
}

/// A generated image: its own card across the chat, with the prompt the CLI rewrote behind 提示词.
struct GeneratedCard: View {
    let item: ChatItem.Generated
    let onOpen: (Int) -> Void
    @Environment(ChatOpen.self) private var open

    var body: some View {
        let isOpen = open.isOpen(item.key)
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(item.images.enumerated()), id: \.offset) { i, ref in
                let preview = ChatPreviews.image(ref)
                Button {
                    onOpen(i)
                } label: {
                    Theme.chip
                        .aspectRatio(CGFloat(aspect(ref)), contentMode: .fit)
                        .overlay {
                            if let preview {
                                Image(uiImage: preview).resizable().scaledToFit()
                            } else {
                                Text("没有预览").type(T.sm, Theme.muted)
                            }
                        }
                        .clipShape(RoundedRectangle(cornerRadius: Theme.Radius.sm, style: .continuous))
                        // A picture taller than the screen would push the chat away.
                        .frame(maxHeight: 420)
                }
                .buttonStyle(PressStyle())
                .accessibilityLabel("生成的图片")
                .accessibilityHint("打开图片")
            }
            HStack(spacing: 8) {
                let first = item.images.first
                let size = first.map { $0.width > 0 ? " · \($0.width) × \($0.height)" : "" } ?? ""
                Text("生成的图片\(size)").type(T.meta, Theme.muted).frame(maxWidth: .infinity, alignment: .leading)
                if !item.prompt.isEmpty {
                    Button {
                        open.toggle(item.key)
                    } label: {
                        ArrowText("提示词", style: T.meta, direction: isOpen ? .up : .down)
                            .padding(.vertical, 14).contentShape(Rectangle()).padding(.vertical, -14)
                    }
                    .buttonStyle(PressStyle())
                }
            }
            if isOpen { Text(item.prompt).type(T.sm, Theme.fg2) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, chatMargin).padding(.vertical, 8)
    }
}
