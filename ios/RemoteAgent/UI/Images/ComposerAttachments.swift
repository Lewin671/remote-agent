import RACore
import SwiftUI

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
    func refresh() { center.aim(key, at: machineId, blocked: blocked != nil, machine: machine) }
    func used(_ images: [PendingImage]) { center.used(key, images, machineId: machineId) }

    func strip(session: (id: String, store: SessionStore)? = nil) -> some View {
        ImageStrip(center: center, key: key, machineId: machineId, blocked: blocked, max: limit, machine: machine)
            .imageDraftDebug(self, session: session)
    }
}
