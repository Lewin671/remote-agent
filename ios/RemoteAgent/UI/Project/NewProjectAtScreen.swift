import SwiftUI

/// The first session of a new project in the folder just chosen (Android's NewProjectAtScreen).
///
/// A placeholder: the screen itself is not written yet. Whoever writes it
/// replaces this file's body and keeps the initialiser, which
/// `Route.destination` calls.
struct NewProjectAtScreen: View {
    let machine: String
    let path: String

    var body: some View {
        ScreenPlaceholder("新项目", detail: [machine, path])
            .navBar("新项目")
    }
}
