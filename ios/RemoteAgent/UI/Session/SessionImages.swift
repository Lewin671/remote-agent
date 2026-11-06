// Session-specific sending, restoration and outgoing attachment previews.
import RACore
import RAProto
import SwiftUI

@MainActor
enum SessionImages {
    /// Sends `text` with the draft's images, or returns false when the draft has none.
    /// `commands` and `keep` stand in for the computer and the disk in tests.
    static func send(
        _ remote: Remote, session sid: String, store: SessionStore, machineId: String, text: String, mode: PBSendMode, commands: (any SessionCommands)? = nil,
        keep: (@Sendable (String, OutgoingRecord, Bool) throws -> Void)? = nil,
        done: @escaping @MainActor (Result<Void, Error>) -> Void
    ) -> Bool {
        // The images of the message being kept are still in the draft: another send would take them a second time.
        if store.isKeeping(sid) { return true }
        let taken = ImageCenter.of(remote).send(
            sid: sid, machineId: machineId, store: store, text: text, mode: mode, commands: commands, keep: keep
        ) { kept in
            store.setKeeping(sid, false)
            done(kept)
        }
        if taken { store.setKeeping(sid, true) }
        return taken
    }

    /// A message that was not submitted goes back into its session's box, text and images, for the user to
    /// send again as a new message. When its images do not fit next to the ones already there nothing moves,
    /// and the strip says why.
    @discardableResult
    static func putBack(_ message: Outgoing, session: String, state: RemoteState, remote: Remote, store: SessionStore) -> Bool {
        let max = imageLimit(for: state.sessions[session].flatMap { state.machines[$0.machineId] })
        let machine = state.sessions[session].flatMap { state.machines[$0.machineId] }
        guard ImageCenter.of(remote).putBack(sid: session, max: max, maxFiles: fileLimit(for: machine), message) else { return false }
        store.putBack(session, message)
        return true
    }

    /// Messages with images an earlier run left on their way come back with
    /// the session's page, whether or not it has a message box now (an ended
    /// session and one on a computer that is offline have none).
    static func restore(session: String, remote: Remote, store: SessionStore) {
        ImageCenter.of(remote).restoreOutgoing(session, store: store)
    }

    /// The thumbnails of a message's images while it is on its way.
    struct OutgoingThumbs: View {
        let message: Outgoing
        @Environment(AccountContext.self) private var context

        var body: some View {
            OutgoingImageThumbs(center: ImageCenter.of(context.remote), message: message)
        }
    }

    /// Queue events replace the outgoing bubble but its local pictures are
    /// still held until submission. Keep drawing them beside the queue entry.
    struct PendingThumbs: View {
        let session: String
        let requestId: String
        @Environment(AccountContext.self) private var context

        var body: some View {
            if let message = context.sessionStore.find(session, requestId: requestId) {
                OutgoingImageThumbs(center: ImageCenter.of(context.remote), message: message, showFiles: false)
            }
        }
    }
}
