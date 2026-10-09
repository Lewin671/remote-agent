// The seam for images in a session's message box: picking them, preparing
// and uploading them, keeping them as a draft, and sending a message with
// them (architecture §17, docs/session-images.md; Android's ui/ImagePick.kt,
// ui/ImageProcess.kt, ui/ImageDrafts.kt and `Outbox.sendWithImages`).
//
// NOT IMPLEMENTED HERE. Everything the message box needs of that feature
// goes through this one file, so it can be filled in without touching the
// screens. What is already there and stays as it is:
//
//   - the chat draws the previews events carry (ChatImages.swift) and opens
//     `Route.image` on a tap;
//   - the package has the draft and the kept messages (`ImageDraft`,
//     `PendingImage`, `Outgoing`, `OutgoingRecords`, `uploadImage`), and the
//     app has the sealed store (`PendingStore`);
//   - `SessionStore.deliver` already sends an `Outgoing`'s `imageIds`.
//
// What to fill in, and what calls it:
//
//   1. `plusTapped`      the ＋ of the message box was tapped: show the picker
//                        and add what was picked to the session's draft.
//   2. `DraftStrip`      the strip over the text, inside the box, with the
//                        attached images and their upload states. Empty now.
//   3. `hasImages`,      whether the draft holds images, and whether every
//      `ready`           one of them is on the computer: the send button
//                        follows them (never a message with some of them).
//   4. `send`            sends the text with the draft's images. Returns
//                        false when there are none, and the box then sends
//                        the text alone; returns true once it has taken the
//                        message over, and calls `done` with the outcome of
//                        keeping the images, for the box to clear the text.
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
    /// 1. The ＋ was tapped. It is only enabled while `context.blocked` is nil.
    static func plusTapped(_ context: SessionImageContext) {}

    /// 3. The session's draft holds images.
    static func hasImages(_ context: SessionImageContext) -> Bool { false }

    /// 3. Every image of the draft is on the computer.
    static func ready(_ context: SessionImageContext) -> Bool { true }

    /// 4. Sends `text` with the draft's images, or returns false when the draft has none.
    static func send(
        _ context: SessionImageContext, text: String, mode: PBSendMode, done: @escaping @MainActor (Result<Void, Error>) -> Void
    ) -> Bool {
        false
    }

    /// 6. Puts a message's images back into the draft; false when they do not fit.
    static func putBack(_ context: SessionImageContext, _ message: Outgoing) -> Bool { true }

    /// 2. The attached images, over the text inside the message box.
    struct DraftStrip: View {
        let context: SessionImageContext

        var body: some View { EmptyView() }
    }

    /// 5. The thumbnails of a message's images while it is on its way.
    struct OutgoingThumbs: View {
        let message: Outgoing

        var body: some View { EmptyView() }
    }
}
