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
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var all: [SessionImage] = []
    /// The page shown; nil until the session's images are known.
    @State private var page: Int?
    @State private var finder = SessionFinder()
    /// How far the picture is dragged down to close.
    @State private var drag: CGFloat = 0
    @State private var message = ""
    @State private var saving = false
    @State private var sharing: LeavingImage?
    @State private var pokes = ViewerPokes()

    var body: some View {
        let state = model.state
        let s = state.sessions[session]
        let loader = ViewerLoader(center: ImageCenter.of(context.remote), session: session)
        let shown = page.flatMap { all.indices.contains($0) ? all[$0] : nil }
        // The page behind shows through as the picture is dragged away.
        let fade = 1 - min(drag / 300, 1)
        ZStack {
            Color.black.opacity(0.4 + 0.6 * fade).ignoresSafeArea()
            if let page, !all.isEmpty {
                TabView(selection: Binding(get: { page }, set: { self.page = $0 })) {
                    ForEach(Array(all.enumerated()), id: \.offset) { i, image in
                        ViewerPage(
                            loader: loader, image: image, online: state.conn == .online, current: i == page, pokes: pokes,
                            onDrag: { y in
                                if y > 0 || reduceMotion { drag = y } else { withAnimation(.easeOut(duration: 0.2)) { drag = 0 } }
                            },
                            onClose: { nav.back() }
                        )
                        .tag(i)
                    }
                }
                .tabViewStyle(.page(indexDisplayMode: .never))
                .ignoresSafeArea()
                .offset(y: drag)
            } else {
                Text(s == nil ? lookupNote(finder.lookup) ?? "读取中…" : "读取中…").type(T.body, ViewerInk.text)
                    .multilineTextAlignment(.center).padding(24)
            }
        }
        .overlay(alignment: .top) { bar.opacity(fade) }
        .overlay(alignment: .bottom) {
            if let shown { info(shown, session: s, loader: loader).opacity(fade) }
        }
        .toolbar(.hidden, for: .navigationBar)
        .statusBarHidden()
        .accessibilityAction(.escape) { nav.back() }
        .sheet(item: $sharing) { picture in
            ShareSheet(picture: picture).presentationDetents([.medium, .large])
        }
        .onChange(of: page) { message = "" }
        .task(id: session) {
            // Every image of the session, as its events come; the page asked for once they are known.
            for await events in context.remote.events(session).updates() {
                let list = await Task.detached { sessionImages(events.events) }.value
                if Task.isCancelled { return }
                if list != all { all = list }
                if page == nil, !list.isEmpty { page = viewerStart(list, seq: seq, index: index) }
            }
        }
        // Opened from a link to a session that is not held here: it is fetched by its ID.
        .task(id: s == nil) {
            if s == nil { await finder.run(context.remote, session, sample: model.isDemo) }
        }
        .sessionSamples(session)
        .debugSignal("viewer.close") { nav.back() }
        .debugSignal("viewer.next") { if let page, page + 1 < all.count { self.page = page + 1 } }
        .debugSignal("viewer.zoom") { pokes.zoom += 1 }
        .debugSignal("viewer.retry") { pokes.retry += 1 }
        .debugSignal("viewer.save") { if let shown { save(shown, loader) } }
        .debugSignal("viewer.share") { if let shown { sharing = ImageLeaving.picture(shown.ref, loader: loader) } }
    }

    private var bar: some View {
        ZStack {
            if let page, !all.isEmpty {
                Text("\(page + 1) / \(all.count)").type(T.body, ViewerInk.text)
                    .accessibilityLabel("第 \(page + 1) 张，共 \(all.count) 张")
            }
            HStack {
                Button {
                    nav.back()
                } label: {
                    Ic(Lucide.x, size: 22, color: ViewerInk.text).hitTarget()
                }
                .buttonStyle(PressStyle())
                .accessibilityLabel("关闭")
                Spacer()
            }
        }
        // The bar has one line's room; the caption below says the rest at any size.
        .dynamicTypeSize(...DynamicTypeSize.accessibility1)
        .frame(height: Theme.minHit)
        .padding(.horizontal, 4)
    }

    private func info(_ image: SessionImage, session s: SessionView?, loader: ViewerLoader) -> some View {
        let ref = image.ref
        let details = [viewerSize(ref), s.map { titleOf($0) }].compactMap { $0 }.joined(separator: " · ")
        let large = typeSize.isAccessibilitySize
        return VStack(alignment: .leading, spacing: 2) {
            Text(viewerSource(ref, agent: s?.agent ?? .codex)).type(T.smB, ViewerInk.text)
            if !ref.path.isEmpty {
                Text(ref.path).type(T.mono, ViewerInk.path).lineLimit(large ? 4 : 2).truncationMode(.middle)
            }
            if !details.isEmpty { Text(details).type(T.meta, ViewerInk.meta).lineLimit(large ? 3 : 1) }
            let actions = Group {
                action(saving ? "保存中…" : "保存", enabled: !saving) { save(image, loader) }
                action("分享", enabled: true) {
                    if let picture = ImageLeaving.picture(ref, loader: loader) { sharing = picture }
                }
                if !message.isEmpty { Text(message).type(T.meta, ViewerInk.text).padding(.vertical, 4) }
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
            Text(title).type(T.body, ViewerInk.action).frame(minWidth: Theme.minHit, minHeight: Theme.minHit, alignment: .leading)
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
