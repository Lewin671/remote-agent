// Full-screen viewer for images in an editable draft, driven by Core's
// `PBAttachmentDraftView.imageViewer` (`DraftImageViewerOpen/Navigate/Close`).
import RACore
import RAProto
import SwiftUI
import UIKit

struct DraftImageViewer: View {
    let center: ImageCenter
    let host: CoreHost
    let scope: PBDraftScope
    let draft: PBAttachmentDraftView

    @State private var localPage = 0
    @State private var pokes = ViewerPokes()

    private func close() {
        Task { await host.closeDraftImageViewer(scope: scope) }
    }

    var body: some View {
        let images = draft.images
        let viewer = draft.imageViewer
        let emptyText = viewer.hasEmptyText ? viewer.emptyText.rendered() : ""
        let positionText = viewer.hasPositionText ? viewer.positionText.rendered() : ""

        PagedImageViewer(
            count: images.count,
            selection: $localPage,
            positionText: positionText,
            empty: emptyText,
            onClose: close,
            picture: { i, drag in
                let img = images[i]
                return DraftViewerPage(
                    center: center,
                    item: img,
                    current: i == localPage,
                    pokes: pokes,
                    onDrag: { drag.wrappedValue = $0 },
                    onClose: close
                )
                .id(img.itemID)
            },
            info: { i in
                let img = images[i]
                let pos = img.positionText.rendered()
                let caption: String = {
                    if img.state == .failed, img.hasProblem, img.problem.hasMessage {
                        let msg = img.problem.message.rendered()
                        if !msg.isEmpty { return msg }
                    }
                    return img.hasStatusText ? img.statusText.rendered() : ""
                }()
                return VStack(alignment: .leading, spacing: 8) {
                    Text(pos).font(.subheadline.weight(.semibold))
                    if !caption.isEmpty {
                        Text(caption).font(.body)
                    }
                    HStack(spacing: 24) {
                        if img.canPause {
                            action(L10n.sharedFileActionPause()) {
                                Task { await host.pauseAttachmentUpload(scope: scope, itemID: img.itemID) }
                            }
                        }
                        if img.canResume {
                            action(L10n.sharedFileActionResume()) {
                                Task { await host.resumeAttachmentUpload(scope: scope, itemID: img.itemID) }
                            }
                        }
                        if img.canRetry {
                            action(L10n.commonRetry()) {
                                Task { await host.retryDraftItem(scope: scope, itemID: img.itemID) }
                            }
                        }
                        if img.canRemove {
                            action(L10n.commonRemove()) {
                                Task { await host.removeDraftItem(scope: scope, itemID: img.itemID) }
                            }
                        }
                    }
                }
                .foregroundStyle(ViewerInk.text).frame(maxWidth: .infinity, alignment: .leading)
                .padding(16).background(ViewerInk.panel, ignoresSafeAreaEdges: .bottom)
            }
        )
        .onAppear {
            if !images.isEmpty {
                localPage = min(max(Int(viewer.currentIndex), 0), images.count - 1)
            }
        }
        .onChange(of: localPage) { _, next in
            guard viewer.open, !images.isEmpty, next != Int(viewer.currentIndex) else { return }
            Task { await host.navigateDraftImageViewer(scope: scope, setIndex: true, index: Int32(next)) }
        }
        .onChange(of: viewer.currentIndex) { _, coreIdx in
            guard viewer.open, !images.isEmpty else { return }
            let clamped = min(max(Int(coreIdx), 0), images.count - 1)
            if localPage != clamped { localPage = clamped }
        }
        .debugSignal("viewer.close", close)
        .debugSignal("viewer.next") {
            Task { await host.navigateDraftImageViewer(scope: scope, delta: 1) }
        }
        .debugSignal("viewer.zoom") { pokes.zoom += 1 }
        .debugSignal("viewer.retry") {
            guard images.indices.contains(localPage), images[localPage].canRetry else { return }
            let id = images[localPage].itemID
            Task { await host.retryDraftItem(scope: scope, itemID: id) }
        }
    }

    private func action(_ text: String, _ run: @escaping () -> Void) -> some View {
        Button(action: run) {
            Text(text).font(.body).foregroundStyle(ViewerInk.action).frame(minWidth: DS.minHit, minHeight: DS.minHit)
        }
        .buttonStyle(PressStyle())
    }
}

private struct DraftViewerPage: View {
    let center: ImageCenter
    let item: PBAttachmentDraftItemView
    let current: Bool
    let pokes: ViewerPokes
    let onDrag: (CGFloat) -> Void
    let onClose: () -> Void
    @State private var sharp: UIImage?
    @State private var zoom = 0
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let thumbRef = item.thumbRef.isEmpty ? item.preparedRef : item.thumbRef
        let fullRef = item.preparedRef.isEmpty ? item.sourceRef : item.preparedRef
        ZStack {
            if let shown = sharp ?? center.localImage(for: thumbRef) {
                ZoomableImage(image: shown, zoomToggles: zoom, reduceMotion: reduceMotion, onDrag: onDrag, onClose: onClose)
                    .ignoresSafeArea().accessibilityLabel(L10n.imageDraft())
            } else {
                Text(item.state == .preparing ? L10n.imagePreparing() : L10n.imageIosNoPreview())
                    .foregroundStyle(ViewerInk.text)
            }
        }
        .task(id: "\(item.itemID)/\(fullRef)") {
            guard !fullRef.isEmpty else { return }
            let decoded = await center.loadSharpImage(for: fullRef)
            if !Task.isCancelled { sharp = decoded }
        }
        .onChange(of: pokes.zoom) { if current { zoom += 1 } }
    }
}
