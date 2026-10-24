#if DEBUG
import Foundation
import RACore

/// Explicit loopback-only harness for simulator acceptance when native UI automation is unavailable.
/// Uses the same durable bootstrap and pairing models as the UI; never enabled in Release.
@MainActor enum ScheduleAcceptance {
    private static var namespace: String? {
        guard let name = HomeLaunch.value("-scheduleAcceptanceNamespace"), !name.isEmpty,
              name.allSatisfy({ $0.isASCII && ($0.isLetter || $0.isNumber || $0 == "-") }) else { return nil }
        return name
    }
    static var defaults: UserDefaults? { namespace.flatMap { UserDefaults(suiteName: "io.github.lewin671.remoteagent.acceptance." + $0) } }
    static var parts: AppModel.Parts? {
        guard let name = namespace, let defaults else { return nil }
        let vault = Vault(service: "io.github.lewin671.remoteagent.acceptance." + name)
        let root = URL.applicationSupportDirectory.appendingPathComponent("schedule-acceptance-" + name)
        var parts = AppModel.Parts()
        parts.defaults = defaults
        parts.clearNotifications = {}
        parts.storage = AccountStorage(vault: vault, cacheRoot: root.appendingPathComponent("cache"), pending: PendingStore(directory: root.appendingPathComponent("pending"), vault: vault, defaults: defaults))
        return parts
    }

    static func bootstrap(_ model: AppModel) async {
        guard let server = HomeLaunch.value("-scheduleAcceptanceServer"),
              let url = URL(string: server), url.host == "127.0.0.1",
              namespace != nil, model.stage == .join, !model.isDemo,
              let token = HomeLaunch.value("-scheduleAcceptanceToken") else { return }
        do {
            let bootstrap = model.authorityBootstrap
            let code = try await bootstrap.prepareCreation(server: server)
            let path = URL.temporaryDirectory.appendingPathComponent("schedule-acceptance-recovery")
            try Data(code.utf8).write(to: path, options: [.atomic, .completeFileProtection])
            try await bootstrap.confirmRecoveryCodeSaved()
            _ = try await bootstrap.continueCreation(setupToken: token)
            try await bootstrap.finishCreation()
            model.reloadAfterAuthorityBootstrap()
        } catch { write("bootstrap-error", errorText(error)) }
    }
    static func write(_ name: String, _ value: String) {
        try? Data(value.utf8).write(to: URL.temporaryDirectory.appendingPathComponent("schedule-acceptance-" + name), options: [.atomic, .completeFileProtection])
    }
}
#endif
