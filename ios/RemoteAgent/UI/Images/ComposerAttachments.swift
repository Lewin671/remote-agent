import Observation
import RACore
import RAProto
import SwiftUI
import UIKit

/// Serializes composer keystrokes to `CoreHost.setDraftText` in strict FIFO order
/// without spawning unordered per-keystroke tasks.
@MainActor
@Observable
final class DraftTextRelay {
    private(set) var pendingCount = 0
    @ObservationIgnored private var tail: Task<Void, Never>?

    func enqueue(_ action: @escaping @Sendable () async -> Void) {
        pendingCount += 1
        let prev = tail
        tail = Task { @MainActor [weak self] in
            if let prev { await prev.value }
            await action()
            if let self {
                self.pendingCount = max(0, self.pendingCount - 1)
            }
        }
    }

    func send(scope: PBDraftScope, text: String, host: CoreHost) {
        enqueue {
            await host.setDraftText(scope: scope, text: text)
        }
    }

    func flushAndRun(_ action: @escaping @Sendable () async -> Void) {
        let prev = tail
        tail = Task { @MainActor in
            if let prev { await prev.value }
            await action()
        }
    }

    func flush() async {
        while let current = tail {
            await current.value
            if tail == current { break }
        }
    }
}

/// Thin adapter passing Core's `PBAttachmentDraftView` and `CoreHost` / `ImageCenter`
/// to `AttachMenu` and `ImageStrip` for both session and new-session composers.
@MainActor
struct ComposerAttachments {
    let center: ImageCenter
    let host: CoreHost
    let scope: PBDraftScope
    let draft: PBAttachmentDraftView

    init(context: AccountContext, scope: PBDraftScope, draft: PBAttachmentDraftView) {
        self.center = ImageCenter.of(context.remote)
        self.host = context.coreHost
        self.scope = scope
        self.draft = draft
    }

    var hasAttachments: Bool { !draft.items.isEmpty || draft.hasPendingPicks_p }
    var ready: Bool { draft.ready }
    var canSend: Bool { draft.canSend && !draft.sending && !draft.restoring }

    var blocked: String? {
        guard !draft.canPickGallery && !draft.canPickFiles else { return nil }
        return draft.hasGalleryBlockedReason ? draft.galleryBlockedReason.rendered() : ""
    }

    /// The computer accepts files: ＋ offers Files and the photo library includes videos.
    var filesOffered: Bool { draft.galleryAllowsVideo }

    var fileMetadataNotice: String {
        draft.hasFileMetadataNotice ? draft.fileMetadataNotice.rendered() : ""
    }

    /// Checking whether the clipboard has an image does not read it, so no system prompt appears yet.
    var clipboardHasImage: Bool { UIPasteboard.general.hasImages }

    func pick() { center.plus(scope: scope, draft: draft) }
    func pickLibrary() { center.library(scope: scope, draft: draft) }
    func pickFiles() { center.documents(scope: scope, draft: draft) }
    func paste() { center.paste(scope: scope) }

    func strip(focus: FocusState<Bool>.Binding? = nil) -> some View {
        ImageStrip(center: center, host: host, scope: scope, draft: draft, focus: focus)
            .imageDraftDebug(self)
    }
}
