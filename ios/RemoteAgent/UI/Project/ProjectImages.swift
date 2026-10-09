// STUB: images in a new session's first message are not built yet.
//
// The new-session box has the design's ＋ at its left (design-system §6
// 输入框: it does one thing, picks images). Everything behind it is this
// file: whoever builds images replaces the two members below and nothing
// else in the box has to change. On Android the same job is done by
// `ImageDrafts.forProject`, `rememberImagePicker`, `uploadBlocked` and the
// `ImageStrip` above the text (ui/ImageDrafts.kt, ui/ImagePick.kt), and a
// start names the uploaded images in `CreateSessionRequest.image_ids`
// (`createSessionCommand(_:prompt:mode:imageIds:)` already takes them).
import RACore

@MainActor
enum ProjectImages {
    /// Whether ＋ can add images now. False until images are built: the button is drawn dimmed, as it is
    /// on Android while the machine cannot take uploads.
    static let available = false

    /// What a tap on ＋ does: picks images for the draft of the project at `key`, to upload to
    /// `machine`. Not built yet, so it does nothing.
    static func pick(project key: String, machine: String?) {}
}
