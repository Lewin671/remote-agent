import SwiftUI

/// A session: its chat and its terminal (Android's SessionScreen). `terminal` opens it on the terminal.
///
/// A placeholder: the screen itself is not written yet. Whoever writes it
/// replaces this file's body and keeps the initialiser, which
/// `Route.destination` calls.
struct SessionScreen: View {
    let id: String
    let terminal: Bool

    var body: some View {
        ScreenPlaceholder("会话", detail: [id, terminal ? "终端" : "聊天"])
            .navBar("会话")
    }
}
