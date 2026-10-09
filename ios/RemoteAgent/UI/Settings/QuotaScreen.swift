import SwiftUI

/// A computer's account quotas (Android's QuotaScreen).
///
/// A placeholder: the screen itself is not written yet. Whoever writes it
/// replaces this file's body and keeps the initialiser, which
/// `Route.destination` calls.
struct QuotaScreen: View {
    let machine: String

    var body: some View {
        ScreenPlaceholder("额度", detail: [machine], gray: true)
            .navBar("额度")
    }
}
