import RACore
import SwiftUI
import UIKit

/// The attachment draft shared by new-session and session composers.
@MainActor
struct ComposerAttachments {
    let center: ImageCenter
    let key: String
    let machineId: String?
    let blocked: String?
    let limit: Int
    var machine: MachineView?

    init(remote: Remote, key: String, machineId: String?, blocked: String?, limit: Int, machine: MachineView? = nil) {
        center = ImageCenter.of(remote)
        self.key = key
        self.machineId = machineId
        self.blocked = blocked
        self.limit = limit
        self.machine = machine
    }

    init(state: RemoteState, key: String, machineId: String?, remote: Remote) {
        let machine = machineId.flatMap { state.machines[$0] }
        self.init(remote: remote, key: key, machineId: machineId,
            blocked: uploadBlocked(machine, online: state.conn == .online, name: machineId.map { machineLabel(state, id: $0) } ?? ""),
            limit: imageLimit(for: machine), machine: machine)
    }

    var draft: ImageDraft { center.draft(key) }
    var hasImages: Bool { !draft.images.isEmpty }
    var ready: Bool {
        !draft.restoring && draft.filesAllowed(for: machine) && (draft.images.isEmpty || draft.ready(machineId: machineId))
    }

    func pick() { center.plus(key, blocked: blocked, max: limit, filesSupported: fileSupported(machine)) }

    /// The computer takes files: ＋ offers them, and the library holds videos too.
    var filesOffered: Bool { fileSupported(machine) }
    /// Asking whether the clipboard has an image does not read it, so nothing is asked of the user yet.
    var clipboardHasImage: Bool { UIPasteboard.general.hasImages }

    /// 照片和视频 (照片 without files): the system's picker, while anything fits.
    func pickLibrary() { center.library(key, blocked: blocked, max: limit, machine: machine) }
    /// 文件: the system's document picker.
    func pickFiles() { center.documents(key, blocked: blocked, machine: machine) }
    func paste() { center.paste(key, max: limit) }
    func refresh() { center.aim(key, at: machineId, blocked: blocked != nil, machine: machine) }
    func used(_ images: [PendingImage]) { center.used(key, images, machineId: machineId) }

    func strip(session: (id: String, store: SessionStore)? = nil) -> some View {
        ImageStrip(center: center, key: key, machineId: machineId, blocked: blocked, max: limit, machine: machine)
            .imageDraftDebug(self, session: session)
    }
}
