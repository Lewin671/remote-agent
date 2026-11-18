import RACore
import RAProto
import SwiftUI

/// The image viewer at an event's image: every image of the session's Core
/// timeline, paged, starting from `seq` / `index`.
struct ImageViewerScreen: View {
    let session: String
    let seq: UInt64
    let index: Int

    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var all: [SessionImage] = []
    /// The page shown; nil until the session's images are known.
    @State private var page: Int?
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

    var body: some View {
        let state = model.state
        let vm = context.core.sessions[session]
        let header = vm?.header
        let tl = vm?.timeline
        let loader = ViewerLoader(center: ImageCenter.of(context.remote), session: session)
        let shown = page.flatMap { all.indices.contains($0) ? all[$0] : nil }
        let emptyNote: String = {
            if header?.missing == true { return header?.missingText.rendered() ?? L10n.sessionIosMissing() }
            return L10n.projectsReadingDirectory()
        }()
        PagedImageViewer(
            count: page == nil ? 0 : all.count,
            selection: Binding(get: { page ?? 0 }, set: { page = $0 }),
            empty: emptyNote,
            onClose: { nav.back() },
            picture: { i, drag in
                ViewerPage(
                    loader: loader, image: all[i], online: state.conn == .online, current: i == page, pokes: pokes,
                    onDrag: { drag.wrappedValue = $0 }, onClose: { nav.back() }
                )
            },
            info: { i in info(all[i], header: header, loader: loader) }
        )
        .sheet(item: $sharing) { picture in
            ShareSheet(picture: picture).presentationDetents([.medium, .large])
        }
        .onChange(of: page) { message = "" }
        .onChange(of: tl?.items ?? [], initial: true) { _, items in
            let list = sessionImages(from: items)
            if list != all {
                if let page, all.indices.contains(page) {
                    let current = all[page]
                    let first = all.firstIndex { $0.seq == current.seq } ?? page
                    self.page = viewerStart(list, seq: current.seq, index: page - first)
                }
                all = list
            }
            if page == nil, !list.isEmpty,
               tl?.historyPartial == false || tl?.hasError == true || tl?.hasFetchError == true
                   || list.contains(where: { $0.seq == seq })
            {
                page = viewerStart(list, seq: seq, index: index)
            }
        }
        .followCoreSession(session, window: window)
        .sessionSamples(session)
        .debugSignal("viewer.close") { nav.back() }
        .debugSignal("viewer.next") { pokes.next += 1 }
        .debugSignal("viewer.zoom") { pokes.zoom += 1 }
        .debugSignal("viewer.retry") { pokes.retry += 1 }
        .debugSignal("viewer.save") { pokes.save += 1 }
        .debugSignal("viewer.share") { pokes.share += 1 }
        .onChange(of: pokes.next) { if let page, page + 1 < all.count { self.page = page + 1 } }
        .onChange(of: pokes.save) { if let shown { save(shown, loader) } }
        .onChange(of: pokes.share) { if let shown { sharing = ImageLeaving.picture(shown.ref, loader: loader) } }
    }

    private func info(_ image: SessionImage, header: PBSessionHeaderView?, loader: ViewerLoader) -> some View {
        let ref = image.ref
        let sessionTitle = header?.found == true ? header?.title.rendered() : nil
        let details = [viewerSize(ref), sessionTitle].compactMap { $0 }.joined(separator: " · ")
        let large = typeSize.isAccessibilitySize
        return VStack(alignment: .leading, spacing: 2) {
            Text(viewerSource(ref, agent: header?.agent ?? defaultAgent)).font(.subheadline.weight(.semibold)).ink(ViewerInk.text)
            if !ref.path.isEmpty {
                Text(ref.path).font(.footnote.monospaced()).ink(ViewerInk.path).lineLimit(large ? 4 : 2).truncationMode(.middle)
            }
            if !details.isEmpty { Text(details).font(.footnote).ink(ViewerInk.meta).lineLimit(large ? 3 : 1) }
            let actions = Group {
                action(saving ? L10n.commonIosSaving() : L10n.commonSave(), enabled: !saving) { save(image, loader) }
                action(L10n.imageShare(), enabled: true) {
                    if let picture = ImageLeaving.picture(ref, loader: loader) { sharing = picture }
                }
                if !message.isEmpty { Text(message).font(.footnote).ink(ViewerInk.text).padding(.vertical, 4) }
            }
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

func sessionImages(from items: [PBSessionTimelineItem]) -> [SessionImage] {
    var out: [SessionImage] = []
    func collect(_ item: PBSessionTimelineItem) {
        switch item.kind {
        case .userMessage(let m)?:
            for r in m.images { out.append(SessionImage(ref: r, seq: item.seq, kind: .userMessage)) }
        case .toolCall(let t)?:
            for r in t.images { out.append(SessionImage(ref: r, seq: item.seq, kind: .toolCall, toolTitle: t.title)) }
        case .toolGroup(let g)?:
            for c in g.calls {
                for r in c.images { out.append(SessionImage(ref: r, seq: item.seq, kind: .toolCall, toolTitle: c.title)) }
            }
        case .generatedImages(let g)?:
            for r in g.images { out.append(SessionImage(ref: r, seq: item.seq, kind: .imageGenerated)) }
        case .quietScheduledGroup(let g)?:
            for child in g.items { collect(child) }
        default:
            break
        }
    }
    for item in items { collect(item) }
    return out
}
