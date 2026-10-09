import SwiftUI

/// A computer's details and daemon settings (Android's DeviceScreen).
///
/// A placeholder: the screen itself is not written yet. Whoever writes it
/// replaces this file's body and keeps the initialiser, which
/// `Route.destination` calls.
struct MachineScreen: View {
    let id: String

    var body: some View {
        ScreenPlaceholder("电脑", detail: [id], gray: true)
            .navBar("电脑")
    }
}
