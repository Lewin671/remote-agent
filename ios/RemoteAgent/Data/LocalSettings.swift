// This device's preferences, the counterpart of Android's data/Prefs.kt
// (DataStore). None of them is secret or content, so they are in
// UserDefaults. Android's `skippedUpdate` has no place here: that app
// updates itself because it is side-loaded, this one does not.
import Foundation
import Observation
import SwiftUI

/// The appearance the user chose in settings; `system` follows the device.
enum ThemeMode: String, Sendable, CaseIterable {
    case system, light, dark

    /// For `preferredColorScheme`: nil leaves it to the system.
    var colorScheme: ColorScheme? {
        switch self {
        case .system: nil
        case .light: .light
        case .dark: .dark
        }
    }
}

struct LocalSettings: Equatable, Sendable {
    var theme: ThemeMode = .system
    /// Push when a session waits for the user (approvals, questions, plans, popups).
    var notifyAttention = true
    /// Push when a turn ends.
    var notifyTurnEnded = false
    /// Show approval content in notifications and allow deciding there. Off by default.
    var showContent = false
    /// Web client address used in pairing QR codes; empty means derived from the server.
    var webOrigin = ""
}

/// Reads and writes `LocalSettings`; views that read `settings` are redrawn when it changes.
@MainActor
@Observable
final class SettingsStore {
    private(set) var settings: LocalSettings
    @ObservationIgnored private let defaults: UserDefaults

    private enum Key {
        static let theme = "settings.theme"
        static let attention = "settings.notify_attention"
        static let turnEnded = "settings.notify_turn_ended"
        static let content = "settings.notify_show_content"
        static let web = "settings.web_origin"
        static let all = [theme, attention, turnEnded, content, web]
    }

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        settings = Self.read(defaults)
    }

    /// Changes the settings and stores them.
    func update(_ change: (inout LocalSettings) -> Void) {
        var next = settings
        change(&next)
        guard next != settings else { return }
        settings = next
        defaults.set(next.theme.rawValue, forKey: Key.theme)
        defaults.set(next.notifyAttention, forKey: Key.attention)
        defaults.set(next.notifyTurnEnded, forKey: Key.turnEnded)
        defaults.set(next.showContent, forKey: Key.content)
        defaults.set(next.webOrigin, forKey: Key.web)
    }

    /// Back to the defaults, as on logging out.
    func clear() {
        for key in Key.all { defaults.removeObject(forKey: key) }
        settings = LocalSettings()
    }

    private static func read(_ d: UserDefaults) -> LocalSettings {
        let base = LocalSettings()
        return LocalSettings(
            theme: d.string(forKey: Key.theme).flatMap(ThemeMode.init(rawValue:)) ?? base.theme,
            notifyAttention: d.object(forKey: Key.attention) as? Bool ?? base.notifyAttention,
            notifyTurnEnded: d.object(forKey: Key.turnEnded) as? Bool ?? base.notifyTurnEnded,
            showContent: d.object(forKey: Key.content) as? Bool ?? base.showContent,
            webOrigin: d.string(forKey: Key.web) ?? base.webOrigin)
    }
}
