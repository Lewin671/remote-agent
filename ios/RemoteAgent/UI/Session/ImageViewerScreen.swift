import RACore
import RAProto
import SwiftUI

/// The image viewer driven by Core's `timeline.viewer_images` and `image_viewer` state:
/// opens via `SessionImageViewerOpenAction`, settles swipe/step navigation via
/// `SessionImageViewerNavigateAction`, and always closes via `SessionImageViewerCloseAction`.
struct ImageViewerScreen: View {
    let session: String
    let seq: UInt64
    let index: Int

    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.dynamicTypeSize) private var typeSize
    /// Local page index held during swipe gestures; settled changes dispatch `SessionImageViewerNavigateAction`.
    @State private var localPage = 0
    @State private var openedInCore = false
    @State private var closedViewer = false
    @State private var message = ""
    @State private var saving = false
    @State private var sharing: LeavingImage?
    @State private var pokes = ViewerPokes()

    private var window: PBSessionWindowRequest {
        .with {
            $0.limit = 200
            if seq > 0 { $0.aroundSeq = seq }
        }
    }

    private func closeViewer() {
        notifyCoreClosed()
        nav.back()
    }

    private func notifyCoreClosed() {
        guard !closedViewer else { return }
        closedViewer = true
        let host = context.coreHost
        let sid = session
        Task { await host.closeSessionImageViewer(sessionID: sid) }
    }

    var body: some View {
        let state = model.state
        let vm = context.core.sessions[session]
        let header = vm?.header
        let tl = vm?.timeline
        let viewer = vm?.imageViewer
        let all = tl?.viewerImages ?? []
        let previews = previewLookup(from: tl?.items ?? [])
        let loader = ViewerLoader(center: ImageCenter.of(context.remote), session: session)
        let activeCount = openedInCore ? all.count : 0
        let shown: PBSessionViewerImageView? = all.indices.contains(localPage) ? all[localPage] : (viewer?.hasCurrentImage == true ? viewer?.currentImage : nil)
        let emptyNote: String = {
            if let viewer, viewer.hasEmptyText {
                let text = viewer.emptyText.rendered()
                if !text.isEmpty { return text }
            }
            if header?.missing == true { return header?.missingText.rendered() ?? L10n.sessionIosMissing() }
            return L10n.commonLoading()
        }()
        let positionText = viewer?.positionText.rendered() ?? ""

        PagedImageViewer(
            count: activeCount,
            selection: $localPage,
            positionText: positionText,
            empty: emptyNote,
            onClose: closeViewer,
            picture: { i, drag in
                let img = all[i]
                return ViewerPage(
                    loader: loader,
                    image: img,
                    previewRef: previews[ViewerPreviewKey(seq: img.seq, index: img.eventImageIndex)],
                    online: state.conn == .online,
                    current: i == localPage,
                    pokes: pokes,
                    onDrag: { drag.wrappedValue = $0 },
                    onClose: closeViewer
                )
            },
            info: { i in
                let img = all[i]
                return info(
                    img,
                    previewRef: previews[ViewerPreviewKey(seq: img.seq, index: img.eventImageIndex)],
                    header: header,
                    loader: loader
                )
            }
        )
        .sheet(item: $sharing) { picture in
            ShareSheet(picture: picture).presentationDetents([.medium, .large])
        }
        .onChange(of: localPage) { _, next in
            message = ""
            guard openedInCore, let viewer, viewer.open, !all.isEmpty, next != Int(viewer.currentIndex) else { return }
            let host = context.coreHost
            let sid = session
            Task { await host.navigateSessionImageViewer(sessionID: sid, setIndex: true, index: Int32(next)) }
        }
        .onChange(of: viewer?.currentIndex, initial: true) { _, coreIdx in
            guard let coreIdx, let viewer, viewer.open, !all.isEmpty else { return }
            let clamped = min(max(Int(coreIdx), 0), all.count - 1)
            if localPage != clamped {
                localPage = clamped
            }
        }
        .task(id: "\(session)/\(seq)/\(index)") {
            closedViewer = false
            openedInCore = false
            await context.coreHost.openSessionImageViewer(sessionID: session, seq: seq, imageIndex: Int32(index))
            if let v = context.core.sessions[session]?.imageViewer, v.totalCount > 0 {
                localPage = min(max(Int(v.currentIndex), 0), Int(v.totalCount) - 1)
            }
            openedInCore = true
        }
        .onDisappear { notifyCoreClosed() }
        .followCoreSession(session, window: window)
        .sessionSamples(session)
        .debugSignal("viewer.close") { closeViewer() }
        .debugSignal("viewer.next") { pokes.next += 1 }
        .debugSignal("viewer.zoom") { pokes.zoom += 1 }
        .debugSignal("viewer.retry") { pokes.retry += 1 }
        .debugSignal("viewer.save") { pokes.save += 1 }
        .debugSignal("viewer.share") { pokes.share += 1 }
        .onChange(of: pokes.next) {
            Task { await context.coreHost.navigateSessionImageViewer(sessionID: session, delta: 1) }
        }
        .onChange(of: pokes.save) {
            if let shown {
                save(shown, previewRef: previews[ViewerPreviewKey(seq: shown.seq, index: shown.eventImageIndex)], loader)
            }
        }
        .onChange(of: pokes.share) {
            if let shown {
                sharing = ImageLeaving.picture(
                    shown,
                    previewRef: previews[ViewerPreviewKey(seq: shown.seq, index: shown.eventImageIndex)],
                    loader: loader
                )
            }
        }
    }

    private func info(
        _ image: PBSessionViewerImageView,
        previewRef: PBImageRef?,
        header: PBSessionHeaderView?,
        loader: ViewerLoader
    ) -> some View {
        let sessionTitle = header?.found == true ? header?.title.rendered() : nil
        let sizeText = image.sizeText.isEmpty ? nil : image.sizeText
        let details = [sizeText, sessionTitle].compactMap { $0 }.joined(separator: " · ")
        let large = typeSize.isAccessibilitySize
        let canLeave = ImageLeaving.picture(image, previewRef: previewRef, loader: loader) != nil
        return VStack(alignment: .leading, spacing: 2) {
            Text(image.sourceLabel.rendered()).font(.subheadline.weight(.semibold)).ink(ViewerInk.text)
            if !image.displayPath.isEmpty {
                Text(image.displayPath).font(.footnote.monospaced()).ink(ViewerInk.path).lineLimit(large ? 4 : 2).truncationMode(.middle)
            }
            if !details.isEmpty { Text(details).font(.footnote).ink(ViewerInk.meta).lineLimit(large ? 3 : 1) }
            let actions = Group {
                action(saving ? L10n.commonIosSaving() : L10n.commonSave(), enabled: !saving) { save(image, previewRef: previewRef, loader) }
                action(L10n.imageShare(), enabled: true) {
                    if let picture = ImageLeaving.picture(image, previewRef: previewRef, loader: loader) { sharing = picture }
                }
                if !message.isEmpty { Text(message).font(.footnote).ink(ViewerInk.text).padding(.vertical, 4) }
            }
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 20) { actions }
                VStack(alignment: .leading, spacing: 0) { actions }
            }
            .disabled(!canLeave)
            .opacity(canLeave ? 1 : 0.4)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 16).padding(.top, 10)
        .background(ViewerInk.panel, ignoresSafeAreaEdges: .bottom)
        .accessibilityElement(children: .contain)
    }

    private func action(_ title: String, enabled: Bool, _ run: @escaping () -> Void) -> some View {
        Button(action: run) {
            Text(title).font(.body).ink(ViewerInk.action).frame(minWidth: DS.minHit, minHeight: DS.minHit, alignment: .leading)
                .contentShape(Rectangle())
        }
        .buttonStyle(PressStyle())
        .disabled(!enabled)
    }

    private func save(_ image: PBSessionViewerImageView, previewRef: PBImageRef?, _ loader: ViewerLoader) {
        guard !saving, let picture = ImageLeaving.picture(image, previewRef: previewRef, loader: loader) else { return }
        saving = true
        message = ""
        Task {
            do {
                try await ImageLeaving.save(picture)
                message = L10n.imageIosSaved()
                Haptics.confirm()
            } catch is PhotoAccessDenied {
                message = L10n.imageIosSavePermission()
            } catch {
                message = L10n.commonIosSaveFailed(reason: errorText(error))
            }
            saving = false
        }
    }
}

/// Key for matching embedded 320px preview bytes from visible timeline items onto Core's stripped `viewer_images`.
private struct ViewerPreviewKey: Hashable {
    let seq: UInt64
    let index: Int32
}

private func previewLookup(from items: [PBSessionTimelineItem]) -> [ViewerPreviewKey: PBImageRef] {
    var out: [ViewerPreviewKey: PBImageRef] = [:]
    func visit(_ item: PBSessionTimelineItem) {
        switch item.kind {
        case .userMessage(let m)?:
            for (i, r) in m.images.enumerated() where !r.preview.isEmpty {
                out[ViewerPreviewKey(seq: item.seq, index: Int32(i))] = r
            }
        case .toolCall(let t)?:
            let s = t.seq > 0 ? t.seq : item.seq
            for (i, r) in t.images.enumerated() where !r.preview.isEmpty {
                out[ViewerPreviewKey(seq: s, index: Int32(i))] = r
            }
        case .toolGroup(let g)?:
            for c in g.calls {
                let s = c.seq > 0 ? c.seq : item.seq
                for (i, r) in c.images.enumerated() where !r.preview.isEmpty {
                    out[ViewerPreviewKey(seq: s, index: Int32(i))] = r
                }
            }
        case .generatedImages(let g)?:
            for (i, r) in g.images.enumerated() where !r.preview.isEmpty {
                out[ViewerPreviewKey(seq: item.seq, index: Int32(i))] = r
            }
        case .quietScheduledGroup(let g)?:
            for child in g.items { visit(child) }
        default:
            break
        }
    }
    for item in items { visit(item) }
    return out
}
