import RACore
import RAProto
import SwiftUI

/// The image viewer at an event's image (Android's ImageViewer; design mock
/// 「会话里的图片 · 查看大图」): every image of the session, paged, from the
/// one tapped. Pinch or double-tap to zoom, swipe down or ✕ to close; 保存
/// and 分享 hand the picture to the system. It is black in both
/// appearances. The parts are in UI/Images/ImageViewer.swift.
struct ImageViewerScreen: View {
    let session: String
    let seq: UInt64
    let index: Int

    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var all: [SessionImage] = []
    /// The page shown; nil until the session's images are known.
    @State private var page: Int?
    @State private var finder = SessionFinder()
    @State private var message = ""
    @State private var saving = false
    @State private var sharing: LeavingImage?
    @State private var pokes = ViewerPokes()

    var body: some View {
        let state = model.state
        let s = state.sessions[session]
        let loader = ViewerLoader(center: ImageCenter.of(context.remote), session: session)
        let shown = page.flatMap { all.indices.contains($0) ? all[$0] : nil }
        PagedImageViewer(count: page == nil ? 0 : all.count,
            selection: Binding(get: { page ?? 0 }, set: { page = $0 }),
            empty: s == nil ? lookupNote(finder.lookup) ?? "读取中…" : "读取中…", onClose: { nav.back() },
            picture: { i, drag in
                ViewerPage(loader: loader, image: all[i], online: state.conn == .online, current: i == page, pokes: pokes,
                    onDrag: { drag.wrappedValue = $0 }, onClose: { nav.back() })
            }, info: { i in info(all[i], session: s, loader: loader) })
        .sheet(item: $sharing) { picture in
            ShareSheet(picture: picture).presentationDetents([.medium, .large])
        }
        .onChange(of: page) { message = "" }
        .task(id: session) {
            // Every image of the session, as its events come; the page asked for once they are known.
            for await events in context.remote.events(session).updates() {
                let list = await Task.detached { sessionImages(events.events) }.value
                if Task.isCancelled { return }
                if list != all {
                    // Older history joining in front moves every index: stay on the image shown.
                    if let page, all.indices.contains(page) {
                        let shown = all[page]
                        let first = all.firstIndex { $0.seq == shown.seq } ?? page
                        self.page = viewerStart(list, seq: shown.seq, index: page - first)
                    }
                    all = list
                }
                // A partial history may not hold the tapped event yet.
                if page == nil, !list.isEmpty,
                    !events.historyPartial || events.error != nil || events.fetchError != nil
                        || list.contains(where: { $0.seq == seq })
                {
                    page = viewerStart(list, seq: seq, index: index)
                }
            }
        }
        // Opened from a link to a session that is not held here: it is fetched by its ID.
        .task(id: s == nil) {
            if s == nil { await finder.run(context.remote, session, sample: model.isDemo) }
        }
        .sessionSamples(session)
        .debugSignal("viewer.close") { nav.back() }
        // A signal's handler is the one of the first frame: it only counts, and the frame of now acts.
        .debugSignal("viewer.next") { pokes.next += 1 }
        .debugSignal("viewer.zoom") { pokes.zoom += 1 }
        .debugSignal("viewer.retry") { pokes.retry += 1 }
        .debugSignal("viewer.save") { pokes.save += 1 }
        .debugSignal("viewer.share") { pokes.share += 1 }
        .onChange(of: pokes.next) { if let page, page + 1 < all.count { self.page = page + 1 } }
        .onChange(of: pokes.save) { if let shown { save(shown, loader) } }
        .onChange(of: pokes.share) { if let shown { sharing = ImageLeaving.picture(shown.ref, loader: loader) } }
    }

    private func info(_ image: SessionImage, session s: SessionView?, loader: ViewerLoader) -> some View {
        let ref = image.ref
        let details = [viewerSize(ref), s.map { titleOf($0) }].compactMap { $0 }.joined(separator: " · ")
        let large = typeSize.isAccessibilitySize
        return VStack(alignment: .leading, spacing: 2) {
            Text(viewerSource(ref, agent: s?.agent ?? defaultAgent)).font(.subheadline.weight(.semibold)).ink(ViewerInk.text)
            if !ref.path.isEmpty {
                Text(ref.path).font(.footnote.monospaced()).ink(ViewerInk.path).lineLimit(large ? 4 : 2).truncationMode(.middle)
            }
            if !details.isEmpty { Text(details).font(.footnote).ink(ViewerInk.meta).lineLimit(large ? 3 : 1) }
            let actions = Group {
                action(saving ? "保存中…" : "保存", enabled: !saving) { save(image, loader) }
                action("分享", enabled: true) {
                    if let picture = ImageLeaving.picture(ref, loader: loader) { sharing = picture }
                }
                if !message.isEmpty { Text(message).font(.footnote).ink(ViewerInk.text).padding(.vertical, 4) }
            }
            // Side by side while they fit; one under the other at the largest sizes.
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 20) { actions }
                VStack(alignment: .leading, spacing: 0) { actions }
            }
            .disabled(ImageLeaving.picture(ref, loader: loader) == nil)
            .opacity(ImageLeaving.picture(ref, loader: loader) == nil ? 0.4 : 1)
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

    private func save(_ image: SessionImage, _ loader: ViewerLoader) {
        guard !saving, let picture = ImageLeaving.picture(image.ref, loader: loader) else { return }
        saving = true
        message = ""
        Task {
            do {
                try await ImageLeaving.save(picture)
                message = "已保存到相册"
                Haptics.confirm()
            } catch is PhotoAccessDenied {
                message = "没有相册权限，没能保存"
            } catch {
                message = "没能保存：\(errorText(error))"
            }
            saving = false
        }
    }
}
