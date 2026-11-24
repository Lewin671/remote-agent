import RACore
import RAProto
import SwiftUI
import UIKit

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
