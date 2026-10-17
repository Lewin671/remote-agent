// The picked images over the text of a message box (docs/design-system.md
// §6 「输入框」, design mock 「从客户端发图片」; Android's `ImageStrip` in
// ui/ImagePick.kt): each removable, a spinner while it is prepared or on its
// way to the computer, a retry when that failed; 原图 for the whole message
// and the count. Sending waits until all of them are on the computer.
//
// The same strip serves a session's box and a project's new-session box.
// It also hosts what ＋ opens, so the boxes need nothing but the tap.
import PhotosUI
import RACore
import SwiftUI

struct ImageStrip: View {
    let center: ImageCenter
    /// The draft's key (`ImageCenter.sessionKey`, `.projectKey`).
    let key: String
    /// The computer the message goes to; nil while none is chosen.
    let machineId: String?
    /// Why that computer takes no images now, in words; nil when it does.
    let blocked: String?
    let max: Int

    @Environment(\.scenePhase) private var scenePhase

    @ScaledMetric(relativeTo: .caption) private var scaledSide: CGFloat = 56

    /// What the images are sent to and when: a change of any of it sends the ones not there yet.
    private struct Aim: Equatable {
        var machine: String?
        var blocked: Bool
        var count: Int
    }

    var body: some View {
        let draft = center.draft(key)
        let notice = center.notices[key]
        VStack(alignment: .leading, spacing: 0) {
            if draft.images.isEmpty {
                if let notice { line(notice, Theme.orangeInk).padding(.horizontal, 2).padding(.vertical, 2) }
            } else {
                tray(draft)
                meta(draft)
                let failed = draft.images.lazy.compactMap { img -> String? in
                    if case .failed(let why) = img.state { return why }
                    return nil
                }.first
                if let text = notice ?? (draft.notice.isEmpty ? nil : draft.notice)
                    ?? failed.map({ "有图没能传到电脑：\($0)，点按重试或移除" })
                {
                    line(text, Theme.orangeInk)
                }
                let notes = draft.images.compactMap { $0.processed?.note }.filter { !$0.isEmpty }
                if draft.original, !notes.isEmpty { line(unique(notes).joined(separator: "；"), Theme.muted) }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .modifier(ImagePickerHost(center: center, key: key, max: max))
        .onChange(of: Aim(machine: machineId, blocked: blocked != nil, count: draft.images.count), initial: true) { _, aim in
            center.aim(key, at: aim.machine, blocked: aim.blocked)
        }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { center.aim(key, at: machineId, blocked: blocked != nil) }
        }
        // A reason is said for a moment, not kept under the box.
        .task(id: notice) {
            guard let notice else { return }
            try? await Task.sleep(for: .seconds(4))
            if !Task.isCancelled { center.clearNotice(key, was: notice) }
        }
    }

    private func unique(_ texts: [String]) -> [String] {
        var seen = Set<String>()
        return texts.filter { seen.insert($0).inserted }
    }

    private func line(_ text: String, _ ink: Color) -> some View {
        Text(text).type(T.meta, ink)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 10).padding(.vertical, 2)
    }

    private func tray(_ draft: ImageDraft) -> some View {
        // A thumbnail keeps a size a finger can hit and grows a little with the text, not without end.
        let side = min(scaledSide, 80)
        return ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 6) {
                ForEach(Array(draft.images.enumerated()), id: \.element.id) { i, img in
                    DraftThumb(
                        image: img, thumb: center.thumb(img), side: side, index: i + 1, count: draft.images.count,
                        showProgress: draft.original,
                        onRetry: { center.retry(key, img) },
                        onRemove: {
                            Haptics.selection()
                            center.remove(key, img)
                        })
                }
            }
            // Room for the remove buttons, which reach 4 pt past their pictures.
            .padding(.top, 6).padding(.horizontal, 6).padding(.bottom, 8)
        }
        .scrollBounceBehavior(.basedOnSize, axes: .horizontal)
    }

    private func meta(_ draft: ImageDraft) -> some View {
        let canChange = center.uploads.canReprepare(draft)
        let megabytes = String(format: "%.1f", Double(draft.totalBytes) / 1024 / 1024)
        let toggle = Button {
            Haptics.selection()
            center.setOriginal(key, !draft.original)
        } label: {
            HStack(spacing: 5) {
                Circle()
                    .strokeBorder(draft.original ? Theme.blueBg : Theme.muted, lineWidth: draft.original ? 4 : 1.5)
                    .frame(width: 14, height: 14)
                Text(draft.original ? "原图 · 共 \(megabytes) MB" : "原图").type(T.meta, Theme.fg2)
            }
            .frame(minHeight: Theme.minHit)
            .contentShape(Rectangle())
        }
        .buttonStyle(PressStyle())
        .disabled(!canChange)
        .opacity(canChange ? 1 : 0.4)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("原图")
        .accessibilityValue(draft.original ? "已选，共 \(megabytes) MB" : "未选")
        .accessibilityHint(canChange ? "只对这一条消息生效" : "这些图片是上次留下的，不能再换成原图或压缩图")
        .accessibilityAddTraits(.isToggle)
        let count = Text("\(draft.images.count) / \(max)").type(T.meta, Theme.muted)
            .accessibilityLabel("已选 \(draft.images.count) 张，最多 \(max) 张")
        return ViewThatFits(in: .horizontal) {
            HStack(spacing: 8) {
                toggle
                Spacer(minLength: 8)
                count
            }
            VStack(alignment: .leading, spacing: 0) {
                toggle
                count.padding(.bottom, 6)
            }
        }
        .padding(.horizontal, 10)
    }
}

/// One picked image in the strip.
private struct DraftThumb: View {
    let image: PendingImage
    let thumb: UIImage?
    let side: CGFloat
    let index: Int
    let count: Int
    /// The bar under the picture: for originals, whose upload takes a while.
    let showProgress: Bool
    let onRetry: () -> Void
    let onRemove: () -> Void

    private var failed: Bool {
        if case .failed = image.state { return true }
        return false
    }

    private var stateWords: String {
        switch image.state {
        case .processing: "正在处理"
        case .uploading(let progress): "正在传到电脑，\(Int((progress * 100).rounded()))%"
        case .ready: "已传到电脑"
        case .failed(let why): "没能传到电脑：\(why)"
        }
    }

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: Theme.Radius.sm, style: .continuous)
        Theme.soft
            .overlay { if let thumb { Image(uiImage: thumb).resizable().scaledToFill() } }
            .overlay { state }
            .frame(width: side, height: side)
            .clipShape(shape)
            .overlay { shape.strokeBorder(Color.black.opacity(0.1), lineWidth: 1) }
            .contentShape(Rectangle())
            .onTapGesture { if failed { onRetry() } }
            .overlay(alignment: .topTrailing) {
                // 18 pt to see, 44 pt to tap.
                Button(action: onRemove) {
                    Ic(Lucide.x, size: 12, color: Theme.onSolid)
                        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                        .frame(width: 18, height: 18)
                        .background(Color.black.opacity(0.6), in: Circle())
                        .padding(7)
                        .frame(width: Theme.minHit, height: Theme.minHit, alignment: .topTrailing)
                        .contentShape(Rectangle())
                }
                .buttonStyle(PressStyle())
                .offset(x: 4, y: -4)
            }
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("图片 \(index)，共 \(count) 张")
            .accessibilityValue(stateWords)
            .accessibilityAddTraits(failed ? .isButton : [])
            .accessibilityHint(failed ? "重试" : "")
            .accessibilityAction { if failed { onRetry() } }
            .accessibilityAction(named: "移除图片", onRemove)
    }

    @ViewBuilder private var state: some View {
        switch image.state {
        case .processing:
            Color.black.opacity(0.25)
            Mark(Lucide.loaderCircle, color: Theme.onSolid, size: 24, relativeTo: .body).dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        case .uploading(let progress):
            Color.black.opacity(0.25)
            Mark(Lucide.loaderCircle, color: Theme.onSolid, size: 24, relativeTo: .body).dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            if showProgress {
                VStack(spacing: 0) {
                    Spacer(minLength: 0)
                    Color.black.opacity(0.35).frame(height: 4)
                        .overlay(alignment: .leading) {
                            Theme.blueBg.frame(width: side * CGFloat(Swift.min(Swift.max(progress, 0), 1)), height: 4)
                        }
                }
            }
        case .failed:
            Color.black.opacity(0.6)
            VStack(spacing: 0) {
                Spacer(minLength: 0)
                Text("重试").type(T.tab, Theme.onSolid).lineLimit(1).minimumScaleFactor(0.7).padding(.bottom, 4)
            }
        case .ready:
            EmptyView()
        }
    }
}

/// What ＋ opens: the system's photo picker, which needs no access to the
/// library; with an image on the clipboard, the choice to paste it instead.
private struct ImagePickerHost: ViewModifier {
    let center: ImageCenter
    let key: String
    let max: Int
    @State private var picked: [PhotosPickerItem] = []
    /// 从相册选择 was chosen in the menu: the picker comes up once the menu is down.
    @State private var photosNext = false

    func body(content: Content) -> some View {
        let room = Swift.max(max - center.draft(key).images.count, 1)
        content
            .actionMenu(
                isPresented: Binding(get: { center.picking[key] == .choose }, set: { if !$0, center.picking[key] == .choose { center.picking[key] = nil } }),
                actions: [
                    SheetAction("从相册选择") { photosNext = true },
                    SheetAction("粘贴剪贴板里的图片") { center.paste(key, max: max) },
                ]
            )
            .onChange(of: center.picking[key]) { _, now in
                guard now == nil, photosNext else { return }
                photosNext = false
                Task {
                    // One presentation at a time: the menu's cover has to be gone first.
                    try? await Task.sleep(for: .milliseconds(350))
                    center.picking[key] = .photos
                }
            }
            .photosPicker(
                isPresented: Binding(get: { center.picking[key] == .photos }, set: { if !$0, center.picking[key] == .photos { center.picking[key] = nil } }),
                selection: $picked, maxSelectionCount: room, selectionBehavior: .ordered, matching: .images,
                // The file as it is: nothing is converted before this app has taken its metadata out.
                preferredItemEncoding: .current
            )
            .onChange(of: picked) { _, items in
                guard !items.isEmpty else { return }
                picked = []
                center.add(key, items.map { .item($0) }, max: max)
            }
    }
}

/// The thumbnails of a message's images while it is on its way: four at most, at the bubble's side.
struct OutgoingImageThumbs: View {
    let center: ImageCenter
    let message: Outgoing

    var body: some View {
        let shown = message.images.prefix(4).compactMap { img in center.thumb(img).map { (img.id, $0) } }
        if !shown.isEmpty {
            HStack(spacing: 4) {
                ForEach(shown, id: \.0) { _, thumb in
                    Image(uiImage: thumb).resizable().scaledToFill()
                        .frame(width: 56, height: 56)
                        .clipShape(RoundedRectangle(cornerRadius: Theme.Radius.sm, style: .continuous))
                }
            }
            .padding(.bottom, 4)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("\(message.images.count) 张图片")
        }
    }
}
