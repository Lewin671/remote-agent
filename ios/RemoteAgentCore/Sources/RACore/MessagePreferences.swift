import Foundation

/// Installation-local preference, never part of account sync. Stage 0 displays
/// Chinese; resolve() is tested separately before a later stage opens settings.
public struct MessagePreferences {
    private let defaults: UserDefaults
    public init(defaults: UserDefaults = .standard) { self.defaults = defaults }
    public var preference: String {
        get { defaults.string(forKey: "message-language") ?? "auto" }
        nonmutating set {
            precondition(newValue == "auto" || MessageLocale.appleTags[newValue] != nil)
            defaults.set(newValue, forKey: "message-language")
        }
    }
    public var current: String { "zh-CN" }
}
