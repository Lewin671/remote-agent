import ImageIO
import RACore
import SwiftUI
import UIKit

struct DraftImageSelection: Identifiable { let index: Int; var id: Int { index } }

struct DraftImageViewer: View {
    let center: ImageCenter
    let key: String
    let onClose: () -> Void
    @State private var page: Int
    @State private var pokes = ViewerPokes()

    init(center: ImageCenter, key: String, start: Int, onClose: @escaping () -> Void) {
        self.center = center; self.key = key; self.onClose = onClose
        _page = State(initialValue: start)
    }

    var body: some View {
        let images = center.draft(key).photos
        PagedImageViewer(count: images.count, selection: $page, empty: L10n.imageIosEmpty(), onClose: onClose,
            picture: { i, drag in
                DraftViewerPage(center: center, image: images[i], current: i == page, pokes: pokes,
                    onDrag: { drag.wrappedValue = $0 }, onClose: onClose)
                    .id(images[i].id)
            }, info: { i in
                let img = images[i]
                let failure: ImageProblem? = { if case .failed(let error) = img.state { return error.problem }; return nil }()
                return VStack(alignment: .leading, spacing: 8) {
                    Text(L10n.imagePosition(count: Int(images.count), index: Int(i + 1))).font(.subheadline.weight(.semibold))
                    Text(failure?.message ?? draftImageState(img)).font(.body)
                    HStack(spacing: 24) {
                        if failure?.retryable == true { action(L10n.commonRetry()) { center.retry(key, img) } }
                        action(L10n.commonRemove()) { center.remove(key, img); if center.draft(key).photos.isEmpty { onClose() } }
                    }
                }
                .foregroundStyle(ViewerInk.text).frame(maxWidth: .infinity, alignment: .leading)
                .padding(16).background(ViewerInk.panel, ignoresSafeAreaEdges: .bottom)
            })
            .debugSignal("viewer.close", onClose)
            .debugSignal("viewer.next") { if page + 1 < images.count { page += 1 } }
            .debugSignal("viewer.zoom") { pokes.zoom += 1 }
            .debugSignal("viewer.retry") {
                guard images.indices.contains(page), case .failed(let why) = images[page].state, why.problem.retryable else { return }
                center.retry(key, images[page])
            }
            .onChange(of: images.count) { _, count in if count == 0 { onClose() } }
    }

    private func action(_ text: String, _ run: @escaping () -> Void) -> some View {
        Button(action: run) { Text(text).font(.body).foregroundStyle(ViewerInk.action).frame(minWidth: DS.minHit, minHeight: DS.minHit) }
            .buttonStyle(PressStyle())
    }
}

@MainActor func draftImageState(_ image: PendingImage) -> String {
    switch image.state {
    case .processing: L10n.imagePreparing()
    case .uploading(let progress): L10n.imageIosUploadProgress(percent: Int(Int(progress * 100)))
    case .ready: L10n.imageUploaded()
    case .failed(let error): L10n.attachmentFailure(reason: error.problem.message)
    }
}

private struct DraftViewerPage: View {
    let center: ImageCenter
    let image: PendingImage
    let current: Bool
    let pokes: ViewerPokes
    let onDrag: (CGFloat) -> Void
    let onClose: () -> Void
    @State private var sharp: UIImage?
    @State private var zoom = 0
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        ZStack {
            if let shown = sharp ?? center.thumb(image) {
                ZoomableImage(image: shown, zoomToggles: zoom, reduceMotion: reduceMotion, onDrag: onDrag, onClose: onClose)
                    .ignoresSafeArea().accessibilityLabel(L10n.imageDraft())
            } else { Text(image.state == .processing ? L10n.imagePreparing() : L10n.imageIosNoPreview()).foregroundStyle(ViewerInk.text) }
        }
        .task(id: "\(image.id)/\(image.generation)/\(image.processed?.bytes.count ?? 0)") {
            guard let processed = image.processed else { return }
            let viewed = await Task.detached { () -> ViewedImage? in
                guard let source = CGImageSourceCreateWithData(processed.bytes as CFData, nil),
                    let cg = CGImageSourceCreateThumbnailAtIndex(source, 0, [
                        kCGImageSourceCreateThumbnailFromImageAlways: true,
                        kCGImageSourceCreateThumbnailWithTransform: true,
                        kCGImageSourceThumbnailMaxPixelSize: 2048,
                    ] as CFDictionary) else { return nil }
                return ViewedImage(image: UIImage(cgImage: cg), bytes: processed.bytes, format: processed.format)
            }.value
            if !Task.isCancelled { sharp = viewed?.image }
        }
        .onChange(of: pokes.zoom) { if current { zoom += 1 } }
    }
}
