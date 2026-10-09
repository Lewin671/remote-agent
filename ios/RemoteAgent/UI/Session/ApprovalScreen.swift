import SwiftUI

/// An approval on a page of its own, as opened from a notification (Android's ApprovalPage).
///
/// A placeholder: the screen itself is not written yet. Whoever writes it
/// replaces this file's body and keeps the initialiser, which
/// `Route.destination` calls.
struct ApprovalScreen: View {
    let session: String
    let approval: String

    var body: some View {
        ScreenPlaceholder("审批", detail: [session, approval])
            .navBar("审批")
    }
}
