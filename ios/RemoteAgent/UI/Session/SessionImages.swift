// The seam for images in a session's message box: picking them, preparing
// and uploading them, keeping them as a draft, and sending a message with
// them (architecture §17, docs/session-images.md; Android's ui/ImagePick.kt,
// ui/ImageProcess.kt, ui/ImageDrafts.kt and `Outbox.sendWithImages`).
//
// Everything the message box needs of that feature goes through this one
// file. What is behind it:
//
//   - `ImageCenter` (UI/Images) holds the account's drafts and the messages
//     on their way, and drives preparing and uploading (`ImageUploads` and
//     `prepareImage` in the package);
//   - `ImageStrip` draws a draft and hosts the picker;
//   - the chat draws the previews events carry (ChatImages.swift) and opens
//     `Route.image` on a tap; `SessionStore.deliver` sends an `Outgoing`'s
//     `imageIds`.
//
// What it gives the box:
//
//   1. `plusTapped`      the ＋ of the message box was tapped: shows the picker
//                        and adds what was picked to the session's draft.
//   2. `DraftStrip`      the strip over the text, inside the box, with the
//                        attached images and their upload states.
//   3. `hasImages`,      whether the draft holds images, and whether every
//      `ready`           one of them is on the computer: the send button
//                        follows them (never a message with some of them).
//   4. `send`            sends the text with the draft's images. Returns
//                        false when there are none, and the box then sends
//                        the text alone; returns true once it has taken the
//                        message over, and calls `done` with the outcome of
//                        keeping the images, for the box to clear the text.
//                        Meanwhile `SessionStore.isKeeping` is true and a
//                        second send does nothing.
//   5. `OutgoingThumbs`  the thumbnails over a message that is on its way.
//   6. `putBack`         a message that was not submitted goes back into
//                        the box with its images; false when they do not
//                        fit next to the ones already there.
import RACore
import RAProto
import SwiftUI

/// What the image code is told about the message box it serves.
@MainActor
struct SessionImageContext {
    let sessionId: String
    let machineId: String
    let remote: Remote
    let store: SessionStore
    /// Why this computer takes no images now, in words (`uploadBlocked`); nil when it does.
    let blocked: String?
    /// The most images one message may carry there (`imageLimit`).
    let limit: Int
}

@MainActor
enum SessionImages {
    private static func center(_ context: SessionImageContext) -> ImageCenter { ImageCenter.of(context.remote) }
    private static func draft(_ context: SessionImageContext) -> ImageDraft {
        center(context).draft(ImageCenter.sessionKey(context.sessionId))
    }

    /// 1. The ＋ was tapped: shows the picker, or says why this computer takes no images now (it is dimmed then).
    static func plusTapped(_ context: SessionImageContext) {
        center(context).plus(ImageCenter.sessionKey(context.sessionId), blocked: context.blocked, max: context.limit)
    }

    /// 3. The session's draft holds images.
    static func hasImages(_ context: SessionImageContext) -> Bool { !draft(context).images.isEmpty }

    static func refresh(_ context: SessionImageContext) {
        center(context).aim(ImageCenter.sessionKey(context.sessionId), at: context.machineId, blocked: context.blocked != nil)
    }

    /// 3. Every image of the draft is on the computer.
    static func ready(_ context: SessionImageContext) -> Bool {
        let d = draft(context)
        return !d.restoring && (d.images.isEmpty || d.ready(machineId: context.machineId))
    }

    /// 4. Sends `text` with the draft's images, or returns false when the draft has none.
    /// `commands` and `keep` stand in for the computer and the disk in tests.
    static func send(
        _ context: SessionImageContext, text: String, mode: PBSendMode, commands: (any SessionCommands)? = nil,
        keep: (@Sendable (String, OutgoingRecord, Bool) throws -> Void)? = nil,
        done: @escaping @MainActor (Result<Void, Error>) -> Void
    ) -> Bool {
        let (store, sid) = (context.store, context.sessionId)
        // The images of the message being kept are still in the draft: another send would take them a second time.
        if store.isKeeping(sid) { return true }
        let taken = center(context).send(
            sid: sid, machineId: context.machineId, store: store, text: text, mode: mode, commands: commands, keep: keep
        ) { kept in
            store.setKeeping(sid, false)
            done(kept)
        }
        if taken { store.setKeeping(sid, true) }
        return taken
    }

    /// 6. Puts a message's images back into the draft; false when they do not fit, and then nothing moved.
    static func putBack(_ context: SessionImageContext, _ message: Outgoing) -> Bool {
        center(context).putBack(sid: context.sessionId, max: context.limit, message)
    }

    /// 6. A message that was not submitted goes back into its session's box, text and images, for the user to
    /// send again as a new message. When its images do not fit next to the ones already there nothing moves,
    /// and the strip says why.
    @discardableResult
    static func putBack(_ message: Outgoing, session: String, state: RemoteState, remote: Remote, store: SessionStore) -> Bool {
        let max = imageLimit(for: state.sessions[session].flatMap { state.machines[$0.machineId] })
        guard ImageCenter.of(remote).putBack(sid: session, max: max, message) else { return false }
        store.putBack(session, message)
        return true
    }

    /// Messages with images an earlier run left on their way come back with
    /// the session's page, whether or not it has a message box now (an ended
    /// session and one on a computer that is offline have none).
    static func restore(session: String, remote: Remote, store: SessionStore) {
        ImageCenter.of(remote).restoreOutgoing(session, store: store)
    }

    /// 2. The attached images, over the text inside the message box.
    struct DraftStrip: View {
        let context: SessionImageContext

        var body: some View {
            let center = ImageCenter.of(context.remote)
            ImageStrip(
                center: center, key: ImageCenter.sessionKey(context.sessionId), machineId: context.machineId, blocked: context.blocked,
                max: context.limit
            )
            .sessionImageDebug(context)
        }
    }

    /// 5. The thumbnails of a message's images while it is on its way.
    struct OutgoingThumbs: View {
        let message: Outgoing
        @Environment(AccountContext.self) private var context

        var body: some View {
            OutgoingImageThumbs(center: ImageCenter.of(context.remote), message: message)
        }
    }
}
