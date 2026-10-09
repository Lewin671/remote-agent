import SwiftUI

/// A client's details: its name, when it was last used, removing it (Android's ClientScreen).
///
/// A placeholder: the screen itself is not written yet. Whoever writes it
/// replaces this file's body and keeps the initialiser, which
/// `Route.destination` calls.
struct ClientScreen: View {
    let id: String

    var body: some View {
        ScreenPlaceholder("客户端", detail: [id], gray: true)
            .navBar("客户端")
    }
}
