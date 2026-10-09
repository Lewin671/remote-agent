import SwiftUI

/// A proposed plan on a page of its own (Android's PlanPage).
///
/// A placeholder: the screen itself is not written yet. Whoever writes it
/// replaces this file's body and keeps the initialiser, which
/// `Route.destination` calls.
struct PlanScreen: View {
    let session: String

    var body: some View {
        ScreenPlaceholder("计划", detail: [session])
            .navBar("计划")
    }
}
