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
import RAProto
import SwiftUI
import UniformTypeIdentifiers

struct ImageStrip: View {
    let center: ImageCenter
    /// The draft's key (`ImageCenter.sessionKey`, `.projectKey`).
    let key: String
    /// The computer the message goes to; nil while none is chosen.
    let machineId: String?
    /// Core-supplied attachment capabilities for the current target.
    var caps: AttachmentCapabilities = AttachmentCapabilities()
    var focus: FocusState<Bool>.Binding? = nil

    private var blocked: String? { caps.imageUploadBlocked }
    private var max: Int { caps.maxImages }

    @Environment(\.scenePhase) private var scenePhase
    @State private var viewing: DraftImageSelection?

    @ScaledMetric(relativeTo: .caption) private var scaledSide: CGFloat = 56

    /// What the images are sent to and when: a change of any of it sends the ones not there yet.
    private struct Aim: Equatable {
        var machine: String?
        var blocked: Bool
        var count: Int
        var filesSupported: Bool
        var fileLimit: Int
        var byteLimit: Int64
    }

    private struct FailureEvalKey: Equatable {
        var machine: String?
        var locale: String
        var failed: [FailedEntry]
        struct FailedEntry: Equatable {
            var id: String
            var input: PBImageProblemInput
        }
    }

    var body: some View {
        let draft = center.draft(key)
        let notice = center.notices[key]?.text
        let failedEntries: [FailureEvalKey.FailedEntry] = draft.photos.compactMap { img in
            if case .failed(let why) = img.state {
                return FailureEvalKey.FailedEntry(id: img.id, input: why.input)
            }
            return nil
        }
        let failures = failedEntries.isEmpty ? "" : (center.failureSummaries[key] ?? "")
        let originalHint = caps.originalImageHint.isEmpty ? L10n.imageOriginalHint() : caps.originalImageHint
        let metadataNotice = caps.fileMetadataNotice.isEmpty ? L10n.attachmentMetadata() : caps.fileMetadataNotice
        VStack(alignment: .leading, spacing: 0) {
            if let error = draft.restoreError {
                Banner(error, tone: .error, action: L10n.commonRetry()) { center.retryDraft(key) }
            }
            if draft.photos.isEmpty {
                if let text = notice ?? (draft.notice.isEmpty ? nil : draft.notice) { line(text, DS.orangeInk).padding(.horizontal, 2).padding(.vertical, 2) }
            } else {
                tray(draft)
                meta(draft)
                if let text = failures.isEmpty ? notice ?? (draft.notice.isEmpty ? nil : draft.notice) : failures
                {
                    line(text, DS.orangeInk)
                }
                let notes = draft.photos.compactMap { $0.processed?.note }.filter { !$0.isEmpty }
                if draft.original, !notes.isEmpty { line(unique(notes).joined(separator: L10n.commonIosNoteSeparator()), DS.ink2) }
                if draft.original { line(originalHint, DS.ink2) }
            }
            if !draft.documents.isEmpty {
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(draft.documents) { item in
                        if let file = item.file {
                            FileChip(
                                name: file.displayName ?? file.name,
                                sizeText: file.formattedSize ?? "",
                                state: item.state,
                                retry: { center.retry(key, item) },
                                remove: { center.remove(key, item) }
                            )
                        }
                    }
                    Text(metadataNotice).font(.footnote).ink(DS.ink2)
                    Text(L10n.attachmentIosFileCount(count: Int(draft.documents.count), limit: Int(caps.maxFiles))).font(.footnote).ink(DS.ink2)
                }
                .padding(.horizontal, 6).padding(.vertical, 6)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .modifier(ImagePickerHost(center: center, key: key, machineId: machineId, caps: caps, focus: focus))
        .fullScreenCover(item: $viewing) { pick in DraftImageViewer(center: center, key: key, start: pick.index) { viewing = nil } }
        .debugSignal("images.preview") { if !draft.photos.isEmpty { viewing = DraftImageSelection(index: 0) } }
        .task(id: FailureEvalKey(machine: machineId, locale: MessageLocale.current, failed: failedEntries)) {
            await center.refreshProblems(key, machineId: machineId)
        }
        .onChange(of: Aim(machine: machineId, blocked: blocked != nil, count: draft.images.count, filesSupported: caps.supportsFiles, fileLimit: caps.maxFiles, byteLimit: caps.maxFileBytes), initial: true) { _, aim in
            center.aim(key, at: aim.machine, blocked: aim.blocked, caps: caps)
        }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { center.aim(key, at: machineId, blocked: blocked != nil, caps: caps) }
        }
        // A reason is said for a moment, not kept under the box.
        .task(id: center.notices[key]?.id) {
            guard let noticeID = center.notices[key]?.id else { return }
            try? await Task.sleep(for: .seconds(4))
            if !Task.isCancelled { center.clearNotice(key, id: noticeID) }
        }
    }

    private func unique(_ texts: [String]) -> [String] {
        var seen = Set<String>()
        return texts.filter { seen.insert($0).inserted }
    }

    private func line(_ text: String, _ ink: Color) -> some View {
        Text(text).font(.footnote).ink(ink)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 10).padding(.vertical, 2)
    }

    private func tray(_ draft: ImageDraft) -> some View {
        // A thumbnail keeps a size a finger can hit and grows a little with the text, not without end.
        let side = min(scaledSide, 80)
        return ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 0) {
                ForEach(Array(draft.photos.enumerated()), id: \.element.id) { i, img in
                    DraftThumb(
                        image: img, thumb: center.thumb(img), problem: center.evaluatedProblems[img.id],
                        side: side, index: i + 1, count: draft.photos.count,
                        showProgress: draft.original,
                        onOpen: { viewing = DraftImageSelection(index: i) },
                        onRemove: {
                            Haptics.selection()
                            center.remove(key, img)
                        })
                }
            }
            .padding(.horizontal, 6).padding(.bottom, 8)
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
                    .strokeBorder(draft.original ? DS.blue : DS.ink2, lineWidth: draft.original ? 4 : 1.5)
                    .frame(width: 14, height: 14)
                Text(draft.original ? L10n.imageIosOriginalTotal(size: megabytes) : L10n.imageOriginal()).font(.footnote).ink(DS.ink2)
            }
            .frame(minHeight: DS.minHit)
            .contentShape(Rectangle())
        }
        .buttonStyle(PressStyle())
        .disabled(!canChange)
        .opacity(canChange ? 1 : 0.4)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(L10n.imageOriginal())
        .accessibilityValue(draft.original ? L10n.imageIosOriginalSelected(size: megabytes) : L10n.imageIosUnselected())
        .accessibilityHint(canChange ? L10n.imageIosOnlyMessage() : L10n.imageIosRetainedOriginal())
        .accessibilityAddTraits(.isToggle)
        let count = Text("\(draft.photos.count) / \(max)").font(.footnote).ink(DS.ink2)
            .accessibilityLabel(L10n.imageIosSelectedCount(count: Int(draft.photos.count), limit: Int(max)))
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

/// The target is centred on the picture's top-right corner and contained in its cell.
struct DraftThumbGeometry {
    let side: CGFloat
    let hit: CGFloat = DS.minHit
    var thumbnail: CGRect { CGRect(x: 0, y: hit / 2, width: side, height: side) }
    var remove: CGRect { CGRect(x: side - hit / 2, y: 0, width: hit, height: hit) }
    var cell: CGSize { CGSize(width: side + hit / 2, height: side + hit / 2) }
}

/// One picked image in the strip.
private struct DraftThumb: View {
    let image: PendingImage
    let thumb: UIImage?
    let problem: PBImageProblemView?
    let side: CGFloat
    let index: Int
    let count: Int
    /// The bar under the picture: for originals, whose upload takes a while.
    let showProgress: Bool
    let onOpen: () -> Void
    let onRemove: () -> Void

    private var failed: Bool {
        if case .failed = image.state { return true }
        return false
    }

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: DS.Radius.small, style: .continuous)
        let geometry = DraftThumbGeometry(side: side)
        ZStack(alignment: .topLeading) {
            Button(action: onOpen) {
                DS.fill
                    .overlay { if let thumb { Image(uiImage: thumb).resizable().scaledToFill() } }
                    .overlay { state }
                    .frame(width: side, height: side).clipShape(shape)
                    .overlay { shape.strokeBorder(Color.black.opacity(0.1), lineWidth: 1) }
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain).padding(.top, geometry.thumbnail.minY)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(L10n.imagePosition(count: Int(count), index: Int(index)))
            .accessibilityValue(draftImageState(image, problem: problem))
            .accessibilityHint(failed ? L10n.imageIosActionsHint() : L10n.imageIosViewFull())
            .accessibilityAction(named: L10n.imageRemoveIndex(index: Int(index)), onRemove)
            Button(action: onRemove) {
                SymbolImage("xmark", size: 12, color: Color.white)
                    .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                    .frame(width: 18, height: 18).background(Color.black.opacity(0.6), in: Circle())
                    // Keep the mark at its existing inset inside the picture.
                    .offset(x: -13, y: 13).frame(width: geometry.hit, height: geometry.hit).contentShape(Rectangle())
            }
            .buttonStyle(PressStyle()).offset(x: geometry.remove.minX)
            .accessibilityLabel(L10n.imageRemoveIndex(index: Int(index)))
        }
        .frame(width: geometry.cell.width, height: geometry.cell.height, alignment: .topLeading)
    }

    @ViewBuilder private var state: some View {
        switch image.state {
        case .processing:
            Color.black.opacity(0.25)
            Mark(Mark.progress, color: Color.white, size: 24, relativeTo: .body).dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        case .uploading(let progress):
            Color.black.opacity(0.25)
            Mark(Mark.progress, color: Color.white, size: 24, relativeTo: .body).dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            if showProgress {
                VStack(spacing: 0) {
                    Spacer(minLength: 0)
                    Color.black.opacity(0.35).frame(height: 4)
                        .overlay(alignment: .leading) {
                            DS.blue.frame(width: side * CGFloat(Swift.min(Swift.max(progress, 0), 1)), height: 4)
                        }
                }
            }
        case .failed:
            Color.black.opacity(0.6)
            VStack(spacing: 0) {
                Spacer(minLength: 0)
                Text(L10n.attachmentFailed()).font(.caption2).ink(Color.white).lineLimit(1).minimumScaleFactor(0.7).padding(.bottom, 4)
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
    let machineId: String?
    let caps: AttachmentCapabilities
    let focus: FocusState<Bool>.Binding?
    // Native pickers can resign the editor without reliably restoring it.
    // Keep the pre-presentation intent, including an intentionally hidden keyboard.
    @State private var focusBeforePicker: Bool?
    /// Action to run after the menu cover dismisses.
    @State private var nextAction: (@MainActor () -> Void)?

    private enum NativePicker: String, Identifiable {
        case photos, documents
        var id: String { rawValue }
    }

    private func restoreEditor() {
        guard center.picking[key] == nil else { return }
        let restore = focusBeforePicker == true
        focusBeforePicker = nil
        if restore, let focus { focus.wrappedValue = true }
    }

    func body(content: Content) -> some View {
        // Observe focus during rendering, before the presentation modifier
        // transfers first-responder ownership to a native picker.
        let editorFocused = focus?.wrappedValue ?? false
        let draft = center.draft(key)
        // Videos go as files where the computer takes files (session-files.md 「从相册选视频」).
        let videos = caps.galleryAllowsVideo
        let room = center.libraryRooms[key] ?? Swift.max(caps.maxImages - draft.photos.count + (videos ? Swift.max(caps.maxFiles - draft.documents.count, 0) : 0), 1)
        content
            .actionMenu(
                isPresented: Binding(get: { center.picking[key] == .choose || center.picking[key] == .attachments }, set: { if !$0, center.picking[key] == .choose || center.picking[key] == .attachments { center.picking[key] = nil } }),
                actions: center.picking[key] == .attachments ? [
                    SheetAction(L10n.imageTitle()) {
                        nextAction = { center.library(key, machineId: machineId, caps: caps, offerPaste: true) }
                    },
                    SheetAction(L10n.attachmentFile()) {
                        nextAction = { center.documents(key, machineId: machineId, caps: caps) }
                    },
                ] : [
                    SheetAction(L10n.attachmentIosChoosePhotos()) {
                        nextAction = { center.library(key, machineId: machineId, caps: caps) }
                    },
                    SheetAction(L10n.attachmentIosPasteClipboard()) { center.paste(key, machineId: machineId) },
                ]
            )
            .onChange(of: center.picking[key]) { previous, now in
                if now != nil {
                    if focusBeforePicker == nil, focus != nil {
                        focusBeforePicker = editorFocused
                    }
                    if now == .photos || now == .documents, let focus { focus.wrappedValue = false }
                } else if let action = nextAction {
                    nextAction = nil
                    Task {
                        // One presentation at a time: the menu's cover has to be gone first.
                        try? await Task.sleep(for: .milliseconds(350))
                        action()
                    }
                } else if previous != .photos && previous != .documents {
                    restoreEditor()
                }
            }
            .sheet(item: Binding(
                get: {
                    switch center.picking[key] {
                    case .photos: NativePicker.photos
                    case .documents: NativePicker.documents
                    default: nil
                    }
                },
                set: { if $0 == nil, center.picking[key] == .photos || center.picking[key] == .documents { center.picking[key] = nil } }
            ), onDismiss: restoreEditor) { picker in
                switch picker {
                case .photos:
                    LibraryPicker(limit: room, videos: videos) { items in
                        center.picking[key] = nil
                        if !items.isEmpty {
                            center.addLibraryItems(key, items.map(\.itemProvider), machineId: machineId, caps: caps)
                        }
                    }.ignoresSafeArea()
                case .documents:
                    DocumentPicker { urls in
                        center.picking[key] = nil
                        if !urls.isEmpty { center.addFiles(key, urls, machineId: machineId, caps: caps) }
                    }.ignoresSafeArea()
                }
            }
    }
}

/// The thumbnails of a message's images while it is on its way: four at most, at the bubble's side.
struct OutgoingImageThumbs: View {
    let center: ImageCenter
    let message: Outgoing
    var showFiles = true

    var body: some View {
        let photos = message.images.filter { !$0.isFile }
        VStack(alignment: .leading, spacing: 6) {
            if showFiles {
                ForEach(message.images.filter(\.isFile)) { item in
                    if let file = item.file {
                        FileChip(name: file.displayName ?? file.name, sizeText: file.formattedSize ?? "")
                    }
                }
            }
        }
        if !photos.isEmpty {
            HStack(spacing: 4) {
                ForEach(photos.prefix(4)) { img in
                    ZStack {
                        DS.fill
                        if let thumb = center.thumb(img) {
                            Image(uiImage: thumb).resizable().scaledToFill()
                        } else {
                            Text(L10n.imageTitle()).font(.footnote).ink(DS.ink2)
                        }
                    }
                    .frame(width: 56, height: 56)
                    .clipShape(RoundedRectangle(cornerRadius: DS.Radius.small, style: .continuous))
                }
            }
            .padding(.bottom, 4)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(L10n.imageCount(count: Int(photos.count)))
        }
    }
}

/// Keep native picker contents while owning their dismissal lifecycle. The
/// sheet's onDismiss runs after UIKit releases the picker, unlike its binding.
private struct LibraryPicker: UIViewControllerRepresentable {
    let limit: Int
    let videos: Bool
    let picked: ([PHPickerResult]) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(picked) }
    func makeUIViewController(context: Context) -> PHPickerViewController {
        var configuration = PHPickerConfiguration()
        configuration.selectionLimit = limit
        configuration.selection = .ordered
        configuration.filter = videos ? .any(of: [.images, .videos]) : .images
        configuration.preferredAssetRepresentationMode = .current
        let picker = PHPickerViewController(configuration: configuration)
        picker.delegate = context.coordinator
        return picker
    }
    func updateUIViewController(_ picker: PHPickerViewController, context: Context) {
        context.coordinator.picked = picked
    }
    final class Coordinator: NSObject, PHPickerViewControllerDelegate {
        var picked: ([PHPickerResult]) -> Void
        init(_ picked: @escaping ([PHPickerResult]) -> Void) { self.picked = picked }
        func picker(_ picker: PHPickerViewController, didFinishPicking results: [PHPickerResult]) { picked(results) }
    }
}

private struct DocumentPicker: UIViewControllerRepresentable {
    let picked: ([URL]) -> Void
    func makeCoordinator() -> Coordinator { Coordinator(picked) }
    func makeUIViewController(context: Context) -> UIDocumentPickerViewController {
        let picker = UIDocumentPickerViewController(forOpeningContentTypes: [.item], asCopy: false)
        picker.allowsMultipleSelection = true
        picker.delegate = context.coordinator
        return picker
    }
    func updateUIViewController(_ picker: UIDocumentPickerViewController, context: Context) {
        context.coordinator.picked = picked
    }
    final class Coordinator: NSObject, UIDocumentPickerDelegate {
        var picked: ([URL]) -> Void
        init(_ picked: @escaping ([URL]) -> Void) { self.picked = picked }
        func documentPicker(_ controller: UIDocumentPickerViewController, didPickDocumentsAt urls: [URL]) { picked(urls) }
        func documentPickerWasCancelled(_ controller: UIDocumentPickerViewController) { picked([]) }
    }
}
