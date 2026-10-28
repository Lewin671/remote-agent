// Images in a new session's first message (docs/session-images.md 「从客户端
// 发图片」: 新建会话的第一条消息和之后的消息都一样).
//
// The new-session box has the design's ＋ at its left (design-system §6
// 输入框: it does one thing, picks images). Everything behind it is this
// file and `ImageCenter`: the images are sent to the machine the session
// will start on, again when that machine changes, and a start names the
// uploaded ones in `CreateSessionRequest.image_ids`. On Android the same
// job is done by `ImageDrafts.forProject`, `rememberImagePicker`,
// `uploadBlocked` and the `ImageStrip` above the text (ui/ImageDrafts.kt,
// ui/ImagePick.kt).
import RACore
import SwiftUI

/// What the image code is told about the new-session box it serves.
@MainActor
struct ProjectImageContext {
    /// The project's path, which its draft is kept under.
    let key: String
    /// The machine the session will start on; nil while none is chosen.
    let machineId: String?
    let remote: Remote
    /// Why that machine takes no images now, in words (`uploadBlocked`); nil when it does.
    let blocked: String?
    /// The most images the first message may carry there (`imageLimit`).
    let limit: Int
    let machine: MachineView?

    init(state: RemoteState, key: String, config: NewConfig?, remote: Remote) {
        self.key = key
        self.remote = remote
        machineId = config?.machineId
        machine = machineId.flatMap { state.machines[$0] }
        blocked = uploadBlocked(machine, online: state.conn == .online, name: machineId.map { machineLabel(state, id: $0) } ?? "")
        limit = imageLimit(for: machine)
    }
}

@MainActor
enum ProjectImages {
    private static func center(_ context: ProjectImageContext) -> ImageCenter { ImageCenter.of(context.remote) }

    /// The images of the project's draft.
    static func draft(_ context: ProjectImageContext) -> ImageDraft { center(context).draft(ImageCenter.projectKey(context.key)) }

    /// Whether ＋ can add images now. When it cannot the button is drawn dimmed, and a tap says why.
    static func available(_ context: ProjectImageContext) -> Bool { context.blocked == nil }

    /// What a tap on ＋ does: picks images for the draft, or says why none can be added.
    static func pick(_ context: ProjectImageContext) {
        center(context).plus(ImageCenter.projectKey(context.key), blocked: context.blocked, max: context.limit, filesSupported: fileSupported(context.machine))
    }

    static func hasImages(_ context: ProjectImageContext) -> Bool { !draft(context).images.isEmpty }

    static func refresh(_ context: ProjectImageContext) {
        center(context).aim(ImageCenter.projectKey(context.key), at: context.machineId, blocked: context.blocked != nil, machine: context.machine)
    }

    /// Every image of the draft is on the machine the session starts on: never a start with some of them.
    static func ready(_ context: ProjectImageContext) -> Bool {
        let d = draft(context)
        return !d.restoring && d.filesAllowed(for: context.machine) && (d.images.isEmpty || d.ready(machineId: context.machineId))
    }

    /// A session started with these images: they leave the draft, and what was held for them goes.
    static func used(_ context: ProjectImageContext, _ taken: [PendingImage]) {
        center(context).used(ImageCenter.projectKey(context.key), taken)
    }

    /// The attached images, over the text inside the new-session box.
    struct Strip: View {
        let context: ProjectImageContext

        var body: some View {
            ImageStrip(
                center: ImageCenter.of(context.remote), key: ImageCenter.projectKey(context.key), machineId: context.machineId,
                blocked: context.blocked, max: context.limit, machine: context.machine
            )
            .projectImageDebug(context)
        }
    }
}
