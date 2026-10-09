import SwiftUI

/// A project's page: its sessions and the box a new one is started from (Android's ProjectScreen). `key` is the project's path; `focusNew` brings the keyboard up in the box.
///
/// A placeholder: the screen itself is not written yet. Whoever writes it
/// replaces this file's body and keeps the initialiser, which
/// `Route.destination` calls.
struct ProjectScreen: View {
    let key: String
    let focusNew: Bool

    var body: some View {
        ScreenPlaceholder("项目", detail: [key, focusNew ? "新会话" : ""])
            .navBar("项目")
    }
}
