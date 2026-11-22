#if DEBUG
import Foundation
import RACore
import RAProto

/// Explicit loopback-only harness for simulator acceptance when native UI automation is unavailable.
/// Uses the same durable Go clientcore bootstrap and pairing flows as the UI; never enabled in Release.
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
        parts.allowPlainHttp = true
        parts.clearNotifications = {}
        parts.makeEngine = { account, cache, securityStore in
            let remote = try Remote(
                account: account, http: AppModel.http, cache: cache,
                securityStore: securityStore, allowPlainHttp: true
            )
            let screens = CoreScreens()
            let host = try CoreHost.live(
                remote: remote, account: account, defaults: defaults,
                screens: screens, onNavigate: { _ in }
            )
            return EngineParts(engine: host, remote: remote, coreHost: host, coreScreens: screens)
        }
        parts.bootstrapSecurity = { vault in
            let host = try StandaloneSecurityHost(store: vault, http: AppModel.http, allowPlainHttp: true)
            defer { host.close() }
            return try await host.client.bootstrap()
        }
        parts.checkExitAdvice = { vault in
            let host = try StandaloneSecurityHost(store: vault, http: AppModel.http, allowPlainHttp: true)
            defer { host.close() }
            _ = try await host.client.bootstrap()
            return try await host.client.checkExitAdvice()
        }
        parts.storage = AccountStorage(vault: vault, cacheRoot: root.appendingPathComponent("cache"), pending: PendingStore(directory: root.appendingPathComponent("pending"), vault: vault, defaults: defaults))
        return parts
    }

    static func bootstrap(_ model: AppModel) async {
        guard let server = HomeLaunch.value("-scheduleAcceptanceServer"),
              let url = URL(string: server), url.host == "127.0.0.1",
              namespace != nil, model.stage == .join, !model.isDemo,
              let token = HomeLaunch.value("-scheduleAcceptanceToken") else { return }
        var host: StandaloneSecurityHost?
        do {
            let h = try model.makeStandaloneSecurityHost()
            host = h
            defer { h.close() }
            _ = try await h.client.bootstrap()
            let prep = try await h.client.prepareCreation(server: server)
            let path = URL.temporaryDirectory.appendingPathComponent("schedule-acceptance-recovery")
            try Data(prep.recoveryCode.utf8).write(to: path, options: [.atomic, .completeFileProtection])
            try await h.client.confirmCreationCodeSaved()
            let res = try await h.client.continueCreation(setupToken: token)
            let st = res.status
            let acct = Account(
                server: !res.accountAdopted.server.isEmpty ? res.accountAdopted.server : st.server,
                deviceId: !res.accountAdopted.deviceID.isEmpty ? res.accountAdopted.deviceID : st.deviceID,
                accountId: !res.accountAdopted.accountID.isEmpty ? res.accountAdopted.accountID : st.accountID
            )
            host = nil
            h.close()
            await model.reloadAfterSecuritySetup(account: acct.deviceId.isEmpty ? nil : acct)
        } catch {
            host?.close()
            write("bootstrap-error", errorText(error))
        }
    }

    static func write(_ name: String, _ value: String) {
        try? Data(value.utf8).write(to: URL.temporaryDirectory.appendingPathComponent("schedule-acceptance-" + name), options: [.atomic, .completeFileProtection])
    }
}
#endif
