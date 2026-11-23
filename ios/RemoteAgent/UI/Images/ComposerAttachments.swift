import RACore
import RAProto
import SwiftUI
import UIKit

/// The attachment draft shared by new-session and session composers.
@MainActor
struct ComposerAttachments {
    let center: ImageCenter
    let key: String
    let machineId: String?
    let caps: AttachmentCapabilities

    var blocked: String? { caps.imageUploadBlocked }
    var limit: Int { caps.maxImages }

    init(remote: Remote, key: String, machineId: String?, caps: AttachmentCapabilities) {
        center = ImageCenter.of(remote)
        self.key = key
        self.machineId = machineId
        self.caps = caps
    }

    init(remote: Remote, key: String, machineId: String?, blocked: String?, limit: Int) {
        self.init(
            remote: remote,
            key: key,
            machineId: machineId,
            caps: AttachmentCapabilities(
                maxImages: limit,
                imageUploadBlocked: blocked
            )
        )
    }

    init(sessionID: String, header: PBSessionHeaderView, remote: Remote) {
        self.init(
            remote: remote,
            key: ImageCenter.sessionKey(sessionID),
            machineId: header.machineID.isEmpty ? nil : header.machineID,
            caps: AttachmentCapabilities(header.capabilities)
        )
    }

    init(newSession vm: PBCoreNewSessionViewModel, key: String, remote: Remote) {
        let machineId = vm.config.machineID.isEmpty ? nil : vm.config.machineID
        self.init(
            remote: remote,
            key: key,
            machineId: machineId,
            caps: AttachmentCapabilities(vm)
        )
    }

    var draft: ImageDraft { center.draft(key) }
    var hasImages: Bool { !draft.images.isEmpty }
    var ready: Bool {
        !draft.restoring && draft.filesAllowed(for: caps) && (draft.images.isEmpty || draft.ready(machineId: machineId))
    }

    func pick() { center.plus(key, machineId: machineId, caps: caps) }

    /// The computer takes files: ＋ offers them, and the library holds videos too.
    var filesOffered: Bool { caps.galleryAllowsVideo }
    /// Asking whether the clipboard has an image does not read it, so nothing is asked of the user yet.
    var clipboardHasImage: Bool { UIPasteboard.general.hasImages }

    /// 照片和视频 (照片 without files): the system's picker, while anything fits.
    func pickLibrary() { center.library(key, machineId: machineId, caps: caps) }
    /// 文件: the system's document picker.
    func pickFiles() { center.documents(key, machineId: machineId, caps: caps) }
    func paste() { center.paste(key, machineId: machineId) }
    func refresh() { center.aim(key, at: machineId, blocked: blocked != nil, caps: caps) }
    func used(_ images: [PendingImage]) { center.used(key, images, machineId: machineId) }

    func strip(focus: FocusState<Bool>.Binding? = nil, session: (id: String, store: SessionStore)? = nil) -> some View {
        ImageStrip(center: center, key: key, machineId: machineId, caps: caps, focus: focus)
            .imageDraftDebug(self, session: session)
    }
}
