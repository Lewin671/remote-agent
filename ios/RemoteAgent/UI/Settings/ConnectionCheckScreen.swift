import SwiftUI

/// 连接检测: a round trip on each stretch of the way, measured when opened (Android's ConnCheckSheet).
///
/// A placeholder: the screen itself is not written yet. Whoever writes it
/// replaces this file's body and keeps the initialiser, which
/// `Route.destination` calls.
struct ConnectionCheckScreen: View {

    var body: some View {
        ScreenPlaceholder("连接检测", detail: [], gray: true)
            .navBar("连接检测")
    }
}
