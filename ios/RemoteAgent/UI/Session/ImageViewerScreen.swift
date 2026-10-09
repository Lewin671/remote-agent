import SwiftUI

/// The image viewer at an event's image (Android's ImageViewer).
///
/// A placeholder: the screen itself is not written yet. Whoever writes it
/// replaces this file's body and keeps the initialiser, which
/// `Route.destination` calls.
struct ImageViewerScreen: View {
    let session: String
    let seq: UInt64
    let index: Int

    var body: some View {
        ScreenPlaceholder("图片", detail: [session, "\(seq) · \(index)"])
            .navBar("图片")
    }
}
