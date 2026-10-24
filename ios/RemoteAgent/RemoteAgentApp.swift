// The iOS client's entry: the app's one store, the window that follows it,
// and the cover over the window whenever the app is not the one in use.
import SwiftUI

@main
struct RemoteAgentApp: App {
    @State private var settings: SettingsStore
    @State private var model: AppModel
    @Environment(\.scenePhase) private var phase

    init() {
        #if DEBUG
        let settings = SettingsStore(defaults: ScheduleAcceptance.defaults ?? .standard)
        let model = AppModel(settings: settings, parts: ScheduleAcceptance.parts ?? AppModel.Parts())
        #else
        let settings = SettingsStore()
        let model = AppModel(settings: settings)
        #endif
        _settings = State(initialValue: settings)
        _model = State(initialValue: model)
        PrivacyShield.shared.install()
        LaunchDriver.prepare(model)
    }

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
                .environment(settings)
                .preferredColorScheme(settings.settings.theme.colorScheme)
                .task { LaunchDriver.orient() }
                // The stream is kept only while the app is in front (architecture §10 「后台和内存」).
                .onChange(of: phase, initial: true) { _, new in
                    // Hosting the tests, the app stays as it starts: no Keychain, no connection.
                    if !LaunchDriver.isTesting { model.scenePhase(new) }
                }
        }
    }
}
