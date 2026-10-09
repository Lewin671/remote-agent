import Foundation
import Testing

@testable import RemoteAgent

@MainActor
@Suite struct SettingsStoreTests {
    let suite = "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)"

    private func defaults() -> UserDefaults { UserDefaults(suiteName: suite)! }

    @Test func startsFromTheDefaults() {
        defer { defaults().removePersistentDomain(forName: suite) }
        let s = SettingsStore(defaults: defaults()).settings
        #expect(s == LocalSettings())
        #expect(s.theme == .system)
        #expect(s.notifyAttention)
        #expect(!s.notifyTurnEnded)
        // Content in notifications is something the user turns on.
        #expect(!s.showContent)
        #expect(s.webOrigin.isEmpty)
    }

    @Test func aChangeIsStoredAndReadByTheNextStore() {
        defer { defaults().removePersistentDomain(forName: suite) }
        let store = SettingsStore(defaults: defaults())
        store.update {
            $0.theme = .dark
            $0.notifyAttention = false
            $0.notifyTurnEnded = true
            $0.showContent = true
            $0.webOrigin = "https://web.example.com"
        }
        #expect(store.settings.theme == .dark)
        let next = SettingsStore(defaults: defaults()).settings
        #expect(next == LocalSettings(theme: .dark, notifyAttention: false, notifyTurnEnded: true, showContent: true, webOrigin: "https://web.example.com"))
    }

    @Test func clearGoesBackToTheDefaults() {
        defer { defaults().removePersistentDomain(forName: suite) }
        let store = SettingsStore(defaults: defaults())
        store.update { $0.theme = .light; $0.showContent = true }
        store.clear()
        #expect(store.settings == LocalSettings())
        #expect(SettingsStore(defaults: defaults()).settings == LocalSettings())
    }

    @Test func aStoredValueThatIsNotAThemeFallsBackToTheSystems() {
        defer { defaults().removePersistentDomain(forName: suite) }
        defaults().set("sepia", forKey: "settings.theme")
        #expect(SettingsStore(defaults: defaults()).settings.theme == .system)
    }

    @Test func themeModesMapToColorSchemes() {
        #expect(ThemeMode.system.colorScheme == nil)
        #expect(ThemeMode.light.colorScheme == .light)
        #expect(ThemeMode.dark.colorScheme == .dark)
    }
}
