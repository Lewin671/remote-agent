// Session-specific sending, restoration and outgoing attachment previews.
import RACore
import RAProto
import SwiftUI

@MainActor
enum SessionImages {
    /// Sends `text` with the draft's images, or returns false when the draft has none.
    /// `submit` hands the sealed request off to Core (or a test spy); `keep` stands in for disk in tests.
    static func send(
        _ remote: Remote, session sid: String, store: SessionStore, machineId: String, text: String, mode: PBSendMode,
        keep: (@Sendable (String, OutgoingRecord, Bool) throws -> Void)? = nil,
        submit: @escaping @MainActor @Sendable (Outgoing) -> Void,
        done: @escaping @MainActor (Result<Void, Error>) -> Void
    ) -> Bool {
        // The images of the message being kept are still in the draft: another send would take them a second time.
        if store.isKeeping(sid) { return true }
        let taken = ImageCenter.of(remote).send(
            sid: sid, machineId: machineId, text: text, mode: mode, keep: keep, submit: submit
        ) { kept in
            store.setKeeping(sid, false)
            done(kept)
        }
        if taken { store.setKeeping(sid, true) }
        return taken
    }

    static func send(
        _ context: AccountContext, session sid: String, machineId: String, text: String, mode: PBSendMode,
        done: @escaping @MainActor (Result<Void, Error>) -> Void
    ) -> Bool {
        send(
            context.remote, session: sid, store: context.sessionStore, machineId: machineId, text: text, mode: mode,
            submit: { o in
                Task {
                    await context.coreHost.submitSessionCommand(
                        sessionID: sid,
                        command: .with {
                            $0.send = .with {
                                $0.sessionID = sid
                                $0.requestID = o.requestId
                                $0.text = o.text
                                $0.imageIds = o.imageIds
                                $0.fileIds = o.fileIds
                                $0.mode = o.mode
                            }
                        },
                        machineID: machineId
                    )
                }
            },
            done: done
        )
    }

    static func resendAttachments(
        _ effect: PBSessionResendAttachmentsEffect,
        context: AccountContext,
        onError: @escaping @MainActor (String) -> Void = { _ in }
    ) {
        let submit: @MainActor @Sendable ([String], [String]) -> Void = { imageIds, fileIds in
            Task {
                await context.coreHost.submitSessionCommand(
                    sessionID: effect.sessionID,
                    command: .with {
                        $0.send = .with {
                            $0.sessionID = effect.sessionID
                            $0.requestID = effect.newRequestID
                            $0.text = effect.text
                            $0.imageIds = imageIds
                            $0.fileIds = fileIds
                            $0.mode = effect.mode
                        }
                    },
                    machineID: effect.machineID
                )
            }
        }
        if effect.imageIds.isEmpty && effect.fileIds.isEmpty {
            submit([], [])
            return
        }
        ImageCenter.of(context.remote).resend(
            sid: effect.sessionID,
            machineId: effect.machineID,
            sourceRequestId: effect.sourceRequestID,
            newRequestId: effect.newRequestID,
            text: effect.text,
            mode: effect.mode,
            submit: { o in
                submit(o.imageIds, o.fileIds)
            },
            done: { result in
                if case .failure(let error) = result {
                    onError(error.localizedDescription)
                }
            }
        )
    }

    /// Checks before asking Core to restore an outgoing/canceled message whether its images fit back into the draft.
    static func canPutBack(
        requestId: String, files: [PBSessionFileRefView] = [], session: String, state: RemoteState, remote: Remote
    ) -> Bool {
        let center = ImageCenter.of(remote)
        let machine = state.sessions[session].flatMap { state.machines[$0.machineId] }
        if let o = center.heldOutgoing(session, requestId: requestId) {
            return center.canPutBack(sid: session, max: imageLimit(for: machine), maxFiles: fileLimit(for: machine), o)
        }
        guard !files.isEmpty else { return true }
        let hasFileCap: Bool = {
            let machineId = state.sessions[session]?.machineId ?? ""
            return (try? remote.requireFileCapability(machineId: machineId, hasFiles: true)) != nil
        }()
        guard hasFileCap else { return false }
        return center.draft(ImageCenter.sessionKey(session)).documents.count + files.count <= fileLimit(for: machine)
    }

    /// A message that was not submitted goes back into its session's box, text and images, for the user to
    /// send again as a new message. When its images do not fit next to the ones already there nothing moves,
    /// and the strip says why.
    @discardableResult
    static func putBack(
        _ message: Outgoing, session: String, state: RemoteState, remote: Remote, store: SessionStore,
        releaseFailure: Bool = false
    ) -> Bool {
        let machine = state.sessions[session].flatMap { state.machines[$0.machineId] }
        let max = imageLimit(for: machine)
        guard ImageCenter.of(remote).putBack(
            sid: session, max: max, maxFiles: fileLimit(for: machine), message, releaseFailure: releaseFailure
        ) else { return false }
        store.appendToDraft(session, message.text)
        return true
    }

    /// Applies Core's `SessionRestoreComposerEffect` after `restoreOutgoing` (or recovery), restoring
    /// local images held for `requestID` and appending `text` to the session draft.
    @discardableResult
    static func applyRestore(
        _ effect: PBSessionRestoreComposerEffect, session: String, state: RemoteState, remote: Remote, store: SessionStore
    ) -> Bool {
        let center = ImageCenter.of(remote)
        if let o = center.heldOutgoing(session, requestId: effect.requestID) {
            let machine = state.sessions[session].flatMap { state.machines[$0.machineId] }
            guard center.putBack(
                sid: session, max: imageLimit(for: machine), maxFiles: fileLimit(for: machine), o, releaseFailure: true
            ) else { return false }
        }
        if !effect.text.isEmpty {
            store.appendToDraft(session, effect.text)
        }
        return true
    }

    /// Messages with images an earlier run left on their way come back with
    /// the session's page, whether or not it has a message box now (an ended
    /// session and one on a computer that is offline have none).
    static func restore(session: String, remote: Remote) {
        ImageCenter.of(remote).restoreOutgoing(session)
    }

    /// The thumbnails of a message's images while it is on its way.
    struct OutgoingThumbs: View {
        let session: String
        let requestId: String
        @Environment(AccountContext.self) private var context

        var body: some View {
            if let message = ImageCenter.of(context.remote).heldOutgoing(session, requestId: requestId) {
                OutgoingImageThumbs(center: ImageCenter.of(context.remote), message: message)
            }
        }
    }

    /// Queue events replace the outgoing bubble but its local pictures are
    /// still held until submission. Keep drawing them beside the queue entry.
    struct PendingThumbs: View {
        let session: String
        let requestId: String
        @Environment(AccountContext.self) private var context

        var body: some View {
            if let message = ImageCenter.of(context.remote).heldOutgoing(session, requestId: requestId) {
                OutgoingImageThumbs(center: ImageCenter.of(context.remote), message: message, showFiles: false)
            }
        }
    }
}
