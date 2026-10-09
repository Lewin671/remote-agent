// The iOS client's entry. For now it only says which build this is: the
// screens, and what they are built from, come next.
import SwiftUI

@main
struct RemoteAgentApp: App {
    var body: some Scene {
        WindowGroup {
            Text("remote-agent \(AppInfo.versionLine)")
        }
    }
}
