// The iOS client's entry. This is the shell the screens are added to: the
// root shows whether this device has joined an account, and the window is
// covered whenever the app is not the one in use.
import SwiftUI

@main
struct RemoteAgentApp: App {
    @State private var settings = SettingsStore()
    private let vault = Vault()

    init() {
        PrivacyShield.shared.install()
    }

    var body: some Scene {
        WindowGroup {
            RootView(vault: vault)
                .environment(settings)
                .preferredColorScheme(settings.settings.theme.colorScheme)
        }
    }
}
