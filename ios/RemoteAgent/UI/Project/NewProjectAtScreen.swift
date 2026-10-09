import SwiftUI

/// A folder chosen for a new project: its message box, focused, with that
/// machine's defaults (Android's NewProjectAtScreen in ui/Project.kt). The
/// project is stored with the machine chosen, so it is listed before its
/// first session, here and on the account's other clients; a folder that
/// is a project already keeps its own settings and shows its sessions.
struct NewProjectAtScreen: View {
    let machine: String
    let path: String

    var body: some View {
        ProjectPageView(key: path, machine: machine, focus: true)
    }
}
