// The attachment strip rendered purely from Go Core's `PBAttachmentDraftView`
// (docs/design-system.md §6 「输入框」): thumbnails, original toggle, counts,
// file chips, notices, storage errors, and the native picker sheets.
import PhotosUI
import RACore
import RAProto
import SwiftUI
import UniformTypeIdentifiers

struct ImageStrip: View {
    let center: ImageCenter
    let host: CoreHost
    let scope: PBDraftScope
    let draft: PBAttachmentDraftView
    var focus: FocusState<Bool>.Binding? = nil

    @ScaledMetric(relativeTo: .caption) private var scaledSide: CGFloat = 56

    private var scopeKey: String { scope.debugKey }

    var body: some View {
        let localNotice = center.notices[scopeKey]?.text
        let coreNotice = draft.hasNotice ? draft.notice.rendered() : ""
        let failureSummary = draft.hasFailureSummary ? draft.failureSummary.rendered() : ""
        let storageError = draft.hasStorageError ? draft.storageError.rendered() : ""
        let originalHint = draft.hasOriginalHint ? draft.originalHint.rendered() : ""
        let metadataNotice = draft.hasFileMetadataNotice ? draft.fileMetadataNotice.rendered() : ""
        let displayedNotice: String? = {
            if !failureSummary.isEmpty { return failureSummary }
            if let localNotice, !localNotice.isEmpty { return localNotice }
            if !coreNotice.isEmpty { return coreNotice }
            return nil
        }()

        VStack(alignment: .leading, spacing: 0) {
            if !storageError.isEmpty {
                Banner(storageError, tone: .error, action: L10n.commonRetry()) {
                    Task { await host.openDraft(scope: scope) }
                }
            }
            if draft.images.isEmpty {
                if let text = displayedNotice {
                    line(text, DS.orangeInk).padding(.horizontal, 2).padding(.vertical, 2)
                }
            } else {
                tray
                meta
                if let text = displayedNotice {
                    line(text, DS.orangeInk)
                }
                if draft.original, !originalHint.isEmpty {
                    line(originalHint, DS.ink2)
                }
            }
            if !draft.files.isEmpty {
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(draft.files, id: \.itemID) { item in
                        let itemID = item.itemID
                        FileChip(
                            item: item,
                            pause: item.canPause ? { Task<Void, Never> { await host.pauseAttachmentUpload(scope: scope, itemID: itemID) } } : nil,
                            resume: item.canResume ? { Task<Void, Never> { await host.resumeAttachmentUpload(scope: scope, itemID: itemID) } } : nil,
                            retry: item.canRetry ? { Task<Void, Never> { await host.retryDraftItem(scope: scope, itemID: itemID) } } : nil,
                            remove: item.canRemove ? { Task<Void, Never> { await host.removeDraftItem(scope: scope, itemID: itemID) } } : nil
                        )
                    }
                    if !metadataNotice.isEmpty {
                        Text(metadataNotice).font(.footnote).ink(DS.ink2)
                    }
                    let countText = draft.hasFileCountText ? draft.fileCountText.rendered() : ""
                    if !countText.isEmpty {
                        Text(countText).font(.footnote).ink(DS.ink2)
                    }
                }
                .padding(.horizontal, 6).padding(.vertical, 6)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .modifier(ImagePickerHost(center: center, scope: scope, draft: draft, focus: focus))
        .fullScreenCover(isPresented: Binding(
            get: { draft.hasImageViewer && draft.imageViewer.open },
            set: { if !$0 { Task { await host.closeDraftImageViewer(scope: scope) } } }
        )) {
            DraftImageViewer(center: center, host: host, scope: scope, draft: draft)
        }
        .debugSignal("images.preview") {
            if !draft.images.isEmpty {
                Task { await host.openDraftImageViewer(scope: scope, index: 0) }
            }
        }
        .task(id: center.notices[scopeKey]?.id) {
            guard let noticeID = center.notices[scopeKey]?.id else { return }
            try? await Task.sleep(for: .seconds(4))
            if !Task.isCancelled { center.clearNotice(scopeKey, id: noticeID) }
        }
        .task(id: coreNotice) {
            guard !coreNotice.isEmpty else { return }
            try? await Task.sleep(for: .seconds(4))
            if !Task.isCancelled { await host.dismissDraftNotice(scope: scope) }
        }
    }

    private func line(_ text: String, _ ink: Color) -> some View {
        Text(text).font(.footnote).ink(ink)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 10).padding(.vertical, 2)
    }

    private var tray: some View {
        let side = min(scaledSide, 80)
        return ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 0) {
                ForEach(draft.images, id: \.itemID) { item in
                    let thumb = center.localImage(for: item.thumbRef.isEmpty ? item.preparedRef : item.thumbRef)
                    DraftThumb(
                        item: item,
                        thumb: thumb,
                        side: side,
                        showProgress: draft.original,
                        onOpen: {
                            Task { await host.openDraftImageViewer(scope: scope, index: item.imageIndex) }
                        },
                        onRemove: {
                            Haptics.selection()
                            Task { await host.removeDraftItem(scope: scope, itemID: item.itemID) }
                        }
                    )
                }
            }
            .padding(.horizontal, 6).padding(.bottom, 8)
        }
        .scrollBounceBehavior(.basedOnSize, axes: .horizontal)
    }

    private var meta: some View {
        let canChange = draft.canToggleOriginal
        let totalSuffix = draft.original && draft.hasOriginalTotalSizeText ? draft.originalTotalSizeText.rendered() : ""
        let toggleText = L10n.imageOriginal() + totalSuffix
        let disabledReason = draft.hasOriginalToggleDisabledReason ? draft.originalToggleDisabledReason.rendered() : ""
        let toggle = Button {
            Haptics.selection()
            Task { await host.setDraftOriginal(scope: scope, original: !draft.original) }
        } label: {
            HStack(spacing: 5) {
                Circle()
                    .strokeBorder(draft.original ? DS.blue : DS.ink2, lineWidth: draft.original ? 4 : 1.5)
                    .frame(width: 14, height: 14)
                Text(toggleText).font(.footnote).ink(DS.ink2)
            }
            .frame(minHeight: DS.minHit)
            .contentShape(Rectangle())
        }
        .buttonStyle(PressStyle())
        .disabled(!canChange)
        .opacity(canChange ? 1 : 0.4)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(L10n.imageOriginal())
        .accessibilityValue(draft.original ? toggleText : L10n.imageIosUnselected())
        .accessibilityHint(canChange ? L10n.imageIosOnlyMessage() : (disabledReason.isEmpty ? L10n.imageIosRetainedOriginal() : disabledReason))
        .accessibilityAddTraits(.isToggle)

        let countDisplay = draft.hasImageCountText ? draft.imageCountText.rendered() : "\(draft.imageCount) / \(draft.maxImages)"
        let count = Text(countDisplay).font(.footnote).ink(DS.ink2)
            .accessibilityLabel(L10n.imageIosSelectedCount(count: Int(draft.imageCount), limit: Int(draft.maxImages)))

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

/// One picked image in the draft strip.
private struct DraftThumb: View {
    let item: PBAttachmentDraftItemView
    let thumb: UIImage?
    let side: CGFloat
    let showProgress: Bool
    let onOpen: () -> Void
    let onRemove: () -> Void

    private var failed: Bool { item.state == .failed }

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: DS.Radius.small, style: .continuous)
        let geometry = DraftThumbGeometry(side: side)
        let posLabel = item.hasPositionA11Y ? item.positionA11Y.rendered() : item.positionText.rendered()
        let statusVal = item.hasStatusText ? item.statusText.rendered() : ""
        let removeLabel = item.hasRemoveA11Y ? item.removeA11Y.rendered() : L10n.commonRemove()
        ZStack(alignment: .topLeading) {
            Button(action: onOpen) {
                DS.fill
                    .overlay { if let thumb { Image(uiImage: thumb).resizable().scaledToFill() } }
                    .overlay { stateOverlay }
                    .frame(width: side, height: side).clipShape(shape)
                    .overlay { shape.strokeBorder(Color.black.opacity(0.1), lineWidth: 1) }
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain).padding(.top, geometry.thumbnail.minY)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(posLabel)
            .accessibilityValue(statusVal)
            .accessibilityHint(failed ? L10n.imageIosActionsHint() : L10n.imageIosViewFull())
            .accessibilityAction(named: removeLabel, onRemove)
            if item.canRemove {
                Button(action: onRemove) {
                    SymbolImage("xmark", size: 12, color: Color.white)
                        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                        .frame(width: 18, height: 18).background(Color.black.opacity(0.6), in: Circle())
                        .offset(x: -13, y: 13).frame(width: geometry.hit, height: geometry.hit).contentShape(Rectangle())
                }
                .buttonStyle(PressStyle()).offset(x: geometry.remove.minX)
                .accessibilityLabel(removeLabel)
            }
        }
        .frame(width: geometry.cell.width, height: geometry.cell.height, alignment: .topLeading)
    }

    @ViewBuilder private var stateOverlay: some View {
        switch item.state {
        case .preparing:
            Color.black.opacity(0.25)
            Mark(Mark.progress, color: Color.white, size: 24, relativeTo: .body).dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        case .uploading, .waitingReconnect:
            Color.black.opacity(0.25)
            Mark(Mark.progress, color: Color.white, size: 24, relativeTo: .body).dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            if showProgress || item.progress > 0 {
                VStack(spacing: 0) {
                    Spacer(minLength: 0)
                    Color.black.opacity(0.35).frame(height: 4)
                        .overlay(alignment: .leading) {
                            DS.blue.frame(width: side * CGFloat(Swift.min(Swift.max(item.progress, 0), 1)), height: 4)
                        }
                }
            }
        case .paused:
            Color.black.opacity(0.45)
            SymbolImage("pause.fill", size: 16, color: Color.white)
        case .failed:
            Color.black.opacity(0.6)
            VStack(spacing: 0) {
                Spacer(minLength: 0)
                Text(L10n.attachmentFailed()).font(.caption2).ink(Color.white).lineLimit(1).minimumScaleFactor(0.7).padding(.bottom, 4)
            }
        case .ready, .unspecified, .UNRECOGNIZED:
            EmptyView()
        }
    }
}

/// Hosts native photo/document pickers and action sheets for one draft scope.
private struct ImagePickerHost: ViewModifier {
    let center: ImageCenter
    let scope: PBDraftScope
    let draft: PBAttachmentDraftView
    let focus: FocusState<Bool>.Binding?
    @State private var focusBeforePicker: Bool?
    @State private var nextAction: (@MainActor () -> Void)?

    private var key: String { scope.debugKey }

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
        let editorFocused = focus?.wrappedValue ?? false
        let videos = draft.galleryAllowsVideo
        let room = max(Int(draft.libraryRoom), 1)
        content
            .actionMenu(
                isPresented: Binding(
                    get: { center.picking[key] == .choose || center.picking[key] == .attachments },
                    set: { if !$0, center.picking[key] == .choose || center.picking[key] == .attachments { center.picking[key] = nil } }
                ),
                actions: center.picking[key] == .attachments ? [
                    SheetAction(L10n.imageTitle()) {
                        nextAction = { center.library(scope: scope, draft: draft, offerPaste: true) }
                    },
                    SheetAction(L10n.attachmentFile()) {
                        nextAction = { center.documents(scope: scope, draft: draft) }
                    },
                ] : [
                    SheetAction(L10n.attachmentIosChoosePhotos()) {
                        nextAction = { center.library(scope: scope, draft: draft) }
                    },
                    SheetAction(L10n.attachmentIosPasteClipboard()) { center.paste(scope: scope) },
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
                            center.addLibraryItems(scope: scope, items.map(\.itemProvider))
                        }
                    }.ignoresSafeArea()
                case .documents:
                    DocumentPicker { urls in
                        center.picking[key] = nil
                        if !urls.isEmpty { center.addFiles(scope: scope, urls) }
                    }.ignoresSafeArea()
                }
            }
    }
}

/// Renders Core's `PBSessionTrayItemView.localAttachments` (outbox thumbnails and file chips)
/// inside an expanded pending tray row.
struct OutboxAttachmentPreviews: View {
    let center: ImageCenter
    let attachments: [PBOutboxAttachmentSummary]

    var body: some View {
        let images = attachments.filter { $0.kind == .image }
        let files = attachments.filter { $0.kind == .file }
        VStack(alignment: .leading, spacing: 6) {
            if !images.isEmpty {
                HStack(spacing: 4) {
                    ForEach(images.prefix(4), id: \.itemID) { img in
                        let thumb = center.localImage(for: img.thumbRef)
                        ZStack {
                            DS.fill
                            if let thumb {
                                Image(uiImage: thumb).resizable().scaledToFill()
                            } else {
                                Text(L10n.imageTitle()).font(.footnote).ink(DS.ink2)
                            }
                        }
                        .frame(width: 48, height: 48)
                        .clipShape(RoundedRectangle(cornerRadius: DS.Radius.small, style: .continuous))
                    }
                }
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(L10n.imageCount(count: images.count))
            }
            ForEach(files, id: \.itemID) { file in
                let size = file.hasSizeText ? file.sizeText.rendered() : ""
                FileChip(name: file.displayName, sizeText: size)
            }
        }
    }
}

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
