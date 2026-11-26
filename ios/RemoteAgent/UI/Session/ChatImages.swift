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

    /// The preview embedded in an image view; nil without one or when it does not decode.
    static func image(_ image: PBSessionImageView) -> UIImage? {
        if image.preview.isEmpty { return nil }
        let key = "\(image.imageID)/\(image.preview.count)/\(image.displayName)" as NSString
        if let held = cache.object(forKey: key) { return held }
        guard let decoded = UIImage(data: image.preview) else { return nil }
        cache.setObject(decoded, forKey: key)
        return decoded
    }
}

private func clampedAspect(_ image: PBSessionImageView) -> CGFloat {
    image.clampedAspectRatio > 0 ? CGFloat(image.clampedAspectRatio) : 1
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
    private let images: [PBSessionImageView]
    private let height: CGFloat
    private let onOpen: (Int) -> Void

    init(_ images: [PBSessionImageView], height: CGFloat = 88, onOpen: @escaping (Int) -> Void) {
        self.images = images
        self.height = height
        self.onOpen = onOpen
    }

    var body: some View {
        let shown = Array(images.prefix(4))
        ThumbStrip(aspects: shown.map(clampedAspect), height: height, maxWidth: 180, spacing: 6) {
            ForEach(Array(shown.enumerated()), id: \.offset) { i, image in
                ChatThumb(image: image, more: i == 3 && images.count > 4 ? images.count - 4 : 0) { onOpen(i) }
            }
        }
    }
}

private struct ChatThumb: View {
    let image: PBSessionImageView
    let more: Int
    let onOpen: () -> Void

    private func badge(_ text: String) -> some View {
        Text(text).font(.caption2).ink(Color.white).lineLimit(1)
            .padding(.horizontal, 5)
            .background(Color.black.opacity(0.6), in: RoundedRectangle(cornerRadius: DS.Radius.inline, style: .continuous))
            .padding(4)
    }

    var body: some View {
        let preview = ChatPreviews.image(image)
        Button(action: onOpen) {
            DS.fill
                .overlay {
                    if let preview {
                        Image(uiImage: preview).resizable().scaledToFill()
                    } else {
                        // Not even a preview: the file's name is all there is.
                        VStack(spacing: 0) {
                            Text(image.syncState == .nameOnly ? L10n.imageUnsynced() : L10n.imageIosNoPreview()).font(.caption2).ink(DS.ink2)
                            if !image.displayName.isEmpty { Text(image.displayName).font(.caption2).ink(DS.ink3).lineLimit(2) }
                        }
                        .multilineTextAlignment(.center).padding(6)
                    }
                }
                .overlay(alignment: .topLeading) { if image.cliIndex > 0 { badge("\(image.cliIndex)") } }
                .overlay(alignment: .bottomLeading) {
                    // On a thumbnail too narrow for the words the viewer says it instead.
                    if image.syncState == .previewOnly && more == 0 {
                        ViewThatFits(in: .horizontal) {
                            badge(L10n.imagePreviewOnly()).fixedSize()
                            Color.clear.frame(width: 0, height: 0)
                        }
                    }
                }
                .overlay {
                    if more > 0 {
                        Color.black.opacity(0.53).overlay { Text("+\(more)").font(.headline).ink(Color.white) }
                    }
                }
                .clipShape(RoundedRectangle(cornerRadius: DS.Radius.small, style: .continuous))
                .contentShape(Rectangle())
        }
        .buttonStyle(PressStyle())
        .accessibilityLabel(more > 0 ? L10n.imageIosMoreImages(count: Int(more)) : image.cliIndex > 0 ? L10n.imageNumbered(index: Int(image.cliIndex)) : L10n.imageTitle())
        .accessibilityValue(image.displayName)
        .accessibilityHint(L10n.imageIosOpen())
    }
}

/// A generated image: its own card across the chat, with the prompt the CLI rewrote behind 提示词.
struct GeneratedCard: View {
    let key: String
    let item: PBSessionGeneratedImagesItem
    let onOpen: (Int) -> Void
    @Environment(ChatOpen.self) private var open

    var body: some View {
        let isOpen = open.isOpen(key)
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(item.imageViews.enumerated()), id: \.offset) { i, image in
                let preview = ChatPreviews.image(image)
                Button {
                    onOpen(i)
                } label: {
                    DS.fill
                        .aspectRatio(clampedAspect(image), contentMode: .fit)
                        .overlay {
                            if let preview {
                                Image(uiImage: preview).resizable().scaledToFit()
                            } else {
                                Text(L10n.imageIosNoPreview()).font(.subheadline).ink(DS.ink2)
                            }
                        }
                        .clipShape(RoundedRectangle(cornerRadius: DS.Radius.small, style: .continuous))
                        // A picture taller than the screen would push the chat away.
                        .frame(maxHeight: 420)
                }
                .buttonStyle(PressStyle())
                .accessibilityLabel(L10n.imageGenerated())
                .accessibilityHint(L10n.imageIosOpen())
            }
            HStack(spacing: 8) {
                let first = item.imageViews.first
                let size = first.flatMap { $0.sizeText.isEmpty ? nil : " · \($0.sizeText)" } ?? ""
                Text(L10n.imageIosGeneratedSize(size: size)).font(.footnote).ink(DS.ink2).frame(maxWidth: .infinity, alignment: .leading)
                if !item.revisedPrompt.isEmpty {
                    Button {
                        open.toggle(key)
                    } label: {
                        ArrowText(L10n.imagePrompt(), style: .footnote, direction: isOpen ? .up : .down)
                            .padding(.vertical, 14).contentShape(Rectangle()).padding(.vertical, -14)
                    }
                    .buttonStyle(PressStyle())
                }
            }
            if isOpen { Text(item.revisedPrompt).font(.subheadline).ink(DS.ink2) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, chatMargin).padding(.vertical, 8)
    }
}
