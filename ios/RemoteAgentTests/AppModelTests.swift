import Foundation
import RACore
import RAProto
import Security
import Testing
import os

@testable import RemoteAgent

/// A network watcher that says only what the test tells it.
final class FakeNetwork: NetworkWatching, @unchecked Sendable {
    func start(_ onChange: @escaping @Sendable (NetworkPath) -> Void) {}
    func stop() {}
}

/// Minimal `CoreHostExecutor` recording lifecycle & network events forwarded by `CoreHost`.
final class RecordingExecutor: CoreHostExecutor, @unchecked Sendable {
    let account: Account
    let remote: Remote
    private let lock = NSLock()
    private var log: [String] = []

    init(account: Account) throws {
        self.account = account
        self.remote = try Remote(account: account, http: HTTP(), allowPlainHttp: true)
    }

    var calls: [String] { lock.withLock { log } }
    private func note(_ s: String) { lock.withLock { log.append(s) } }
    func forget() { lock.withLock { log.removeAll() } }

    func installCoreHooks(_ hooks: Remote.CoreHooks) async { note("installHooks") }
    func openStream(legId: UInt64, helloFrame: Data, callbacks: Remote.StreamCallbacks) async { note("openStream") }
    func closeStream(legId: UInt64) async { note("closeStream") }
    func executeCacheRead(_ effect: PBCoreCacheReadEffect) async -> PBCoreEvent {
        switch effect.kind {
        case .bootstrap(let b):
            return .with { $0.cacheBootstrapLoaded = .with { $0.opID = b.opID } }
        case .events(let e):
            return .with { $0.cacheEventsLoaded = .with { $0.opID = e.opID; $0.sessionID = e.sessionID } }
        case nil:
            return PBCoreEvent()
        }
    }
    func executeCacheWrite(_ effect: PBCoreCacheWriteEffect) async {}
    func executeCacheDelete(_ effect: PBCoreCacheDeleteEffect, onSessionRemoved: (@Sendable (String) async -> Void)?) async {}
    func dispatchCommandAttempt(_ effect: PBCoreDispatchCommandEffect) async -> PBCoreCommandOutcomeEvent {
        PBCoreCommandOutcomeEvent.with {
            $0.requestID = effect.requestID
            $0.attemptID = effect.attemptID
            $0.source = .local
            $0.notForwarded = true
            $0.transportCode = "unavailable"
        }
    }
    func close() async {
        note("close")
        remote.close()
    }
}

/// An AppModel on an isolated Keychain service, cache directory, and UserDefaults suite.
@MainActor
final class Harness {
    let root = FileManager.default.temporaryDirectory.appending(
        path: "ra-app-\(UUID().uuidString)", directoryHint: .isDirectory)
    let suite = "ra-app-\(UUID().uuidString)"
    let service = "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)"
    let defaults: UserDefaults
    let settings: SettingsStore
    let vault: Vault
    let pending: PendingStore
    let account = Account(
        server: "http://127.0.0.1:1",
        deviceId: "00112233445566778899aabbccddeeff",
        accountId: "11111111111111111111111111111111")
    private(set) var executors: [RecordingExecutor] = []
    var cleared = 0
    var unregisteredPush = 0
    var erase: (@Sendable (AccountStorage) throws -> Void)?

    lazy var model: AppModel = makeModel()

    init(keychain: KeychainOps = .live) {
        defaults = UserDefaults(suiteName: suite)!
        settings = SettingsStore(defaults: defaults)
        let cacheRoot = root.appending(path: "cache")
        vault = Vault(
            service: service,
            onLegacyWipe: { try? CipherStore.eraseAll(root: cacheRoot) },
            keychain: keychain
        )
        pending = PendingStore(directory: root.appending(path: "pending"), vault: vault)
    }

    func makeModel() -> AppModel {
        var parts = AppModel.Parts()
        parts.storage = AccountStorage(
            vault: vault, cacheRoot: root.appending(path: "cache"), pending: pending)
        parts.network = FakeNetwork()
        parts.defaults = defaults
        parts.allowPlainHttp = true
        parts.makeExecutor = { [unowned self] account, _, _ in
            let exec = try RecordingExecutor(account: account)
            self.executors.append(exec)
            return (exec, exec.remote)
        }
        parts.deviceDescription = { "iOS 26.0 · iPhone" }
        parts.unregisterRemoteNotifications = { [unowned self] in self.unregisteredPush += 1 }
        parts.clearNotifications = { [unowned self] in self.cleared += 1 }
        if let erase { parts.erase = erase }
        return AppModel(settings: settings, parts: parts)
    }

    var executor: RecordingExecutor { executors.last! }

    /// Activates an account on the single CoreHost by emitting `AccountRuntimeChangedEffect`.
    func activateSyntheticAccount() async throws {
        model.setForeground(true)
        try await model.waitForLoadForTest()
        let host = model.coreHost
        await host.processEffectBatchForTesting([
            PBCoreEffect.with {
                $0.accountRuntimeChanged = .with {
                    $0.active = true
                    $0.removed = false
                    $0.server = account.server
                    $0.deviceID = account.deviceId
                    $0.accountID = account.accountId
                }
            }
        ])
        await eventually("context attached") { self.model.context != nil }
    }

    func cleanUp() {
        model.closeSyncForTest()
        try? vault.clear()
        try? FileManager.default.removeItem(at: root)
        defaults.removePersistentDomain(forName: suite)
    }
}

@MainActor
@Suite struct AppModelTests {
    @Test func withoutAnAccountOnboardingIsShownAndNoExecutorIsCreated() async throws {
        let h = Harness(); defer { h.cleanUp() }
        #expect(h.model.screens.appRoot.stage == .unspecified)
        h.model.setForeground(true)
        try await h.model.waitForLoadForTest()
        #expect(h.model.screens.appRoot.stage == .onboarding)
        #expect(h.model.screens.onboarding.mode == .entry)
        #expect(h.model.context == nil)
        #expect(h.executors.isEmpty)
    }

    @Test func nothingIsReadInTheBackground() async {
        let h = Harness(); defer { h.cleanUp() }
        h.model.setForeground(false)
        h.model.loadAccount()
        try? await Task.sleep(nanoseconds: 50_000_000)
        #expect(h.model.screens.appRoot.stage == .unspecified)
        #expect(h.executors.isEmpty)
    }

    @Test func corruptRegistrationIntentSurfacesBootstrapErrorAndSetupResetRecoversInPlace() async throws {
        let h = Harness(); defer { h.cleanUp() }
        _ = h.vault.executeStorage(.with {
            $0.op = .writeCas
            $0.bucket = .registrationIntent
            $0.key = "intent"
            $0.createOnly = true
            $0.nextRevision = "r1"
            $0.data = Data("synthetic-invalid-receipt".utf8)
        })
        h.model.setForeground(true)
        try await h.model.waitForLoadForTest()
        #expect(h.model.screens.appRoot.stage == .bootstrapError)
        #expect(h.model.screens.appRoot.bootstrapError.canReset)
        #expect(h.vault.hasPersistedState())

        await h.model.coreHost.exitRequest(intent: .setupReset)
        await eventually("exit confirm shown") { h.model.screens.exitConfirm.step == .awaitingConfirm }
        await h.model.coreHost.exitConfirm(true)
        await eventually("reset to onboarding") { h.model.screens.appRoot.stage == .onboarding }

        #expect(!h.vault.hasPersistedState())
        #expect(h.unregisteredPush == 1)
        #expect(h.cleared == 1)
    }

    @Test func corruptRecordSurfacesBootstrapErrorAndSetupResetClearsState() async throws {
        let h = Harness(); defer { h.cleanUp() }
        _ = h.vault.executeStorage(.with {
            $0.op = .writeCas
            $0.bucket = .record
            $0.key = "record"
            $0.createOnly = true
            $0.nextRevision = "r1"
            $0.data = Data([1, 2, 3, 4])
        })
        h.model.setForeground(true)
        try await h.model.waitForLoadForTest()
        #expect(h.model.screens.appRoot.stage == .bootstrapError)
        #expect(h.model.screens.appRoot.bootstrapError.canReset)

        await h.model.coreHost.exitRequest(intent: .setupReset)
        await eventually("exit confirm shown") { h.model.screens.exitConfirm.step == .awaitingConfirm }
        await h.model.coreHost.exitConfirm(true)
        await eventually("onboarding after reset") { h.model.screens.appRoot.stage == .onboarding }
        #expect(h.unregisteredPush == 1)
        #expect(!h.vault.hasPersistedState())
    }

    @Test func lockedKeychainDisablesResetAndAutoRetriesOnForegroundResume() async throws {
        let locked = OSAllocatedUnfairLock(initialState: true)
        let ops = KeychainOps(
            copyMatching: { query, out in
                if locked.withLock({ $0 }) { return errSecInteractionNotAllowed }
                return KeychainOps.live.copyMatching(query, out)
            },
            add: { query, out in
                if locked.withLock({ $0 }) { return errSecInteractionNotAllowed }
                return KeychainOps.live.add(query, out)
            },
            update: { query, attrs in
                if locked.withLock({ $0 }) { return errSecInteractionNotAllowed }
                return KeychainOps.live.update(query, attrs)
            },
            delete: { query in
                if locked.withLock({ $0 }) { return errSecInteractionNotAllowed }
                return KeychainOps.live.delete(query)
            }
        )
        let h = Harness(keychain: ops); defer {
            locked.withLock { $0 = false }
            h.cleanUp()
        }
        h.model.setForeground(true)
        try await h.model.waitForLoadForTest()
        #expect(h.model.screens.appRoot.stage == .bootstrapError)
        #expect(!h.model.screens.appRoot.bootstrapError.canReset)
        #expect(h.model.screens.appRoot.bootstrapError.canRetry)

        // Unlock device and resume foreground: AppModel automatically retries bootstrap.
        locked.withLock { $0 = false }
        h.model.setForeground(false)
        h.model.setForeground(true)
        await eventually("recovered to onboarding") { h.model.screens.appRoot.stage == .onboarding }
    }

    @Test func legacyV3WipeClearsCipherCachePreservesPendingKeyAndPersistsRepairNoticeAcrossColdRestart() async throws {
        let h = Harness(); defer { h.cleanUp() }
        try h.vault.writeRawKeychainItemForTest(account: "account", data: Data("v3-acct".utf8))
        try h.vault.writeRawKeychainItemForTest(account: "security-v3", data: Data("v3-sec".utf8))
        try h.vault.write(.pendingKey, Data(repeating: 9, count: 32))
        let cache = try CipherStore.open(root: h.root.appending(path: "cache"), account: "legacy")
        await cache.putBlob(name: "stale", data: Data([1]))
        try cache.close()

        h.model.setForeground(true)
        try await h.model.waitForLoadForTest()
        #expect(h.model.screens.appRoot.stage == .onboarding)
        #expect(h.model.screens.onboarding.showLegacyRepairBanner)
        #expect(try h.vault.read(.pendingKey) == Data(repeating: 9, count: 32))
        #expect(!FileManager.default.fileExists(atPath: h.root.appending(path: "cache").path))

        // Cold restart still sees the banner until dismissed through Core.
        let second = h.makeModel(); defer { second.closeSyncForTest() }
        second.setForeground(true)
        try await second.waitForLoadForTest()
        #expect(second.screens.appRoot.stage == .onboarding)
        #expect(second.screens.onboarding.showLegacyRepairBanner)

        await second.coreHost.appRootDismissUpgradeNotice()
        await eventually("banner dismissed") { !second.screens.onboarding.showLegacyRepairBanner }
        await eventually("upgrade notice deleted in vault") { !h.vault.hasUpgradeNotice() }
    }

    @Test func accountRuntimeChangedAttachesToSingleCoreAndOpensCipherCache() async throws {
        let h = Harness(); defer { h.cleanUp() }
        try await h.activateSyntheticAccount()
        #expect(h.model.account == h.account)
        #expect(h.model.context?.coreHost === h.model.coreHost)
        #expect(h.model.context?.core === h.model.screens)
        let key = CipherStore.accountKey(server: h.account.server, deviceId: h.account.deviceId)
        #expect(FileManager.default.fileExists(atPath: h.root.appending(path: "cache/\(key)/cache.sqlite").path))
    }

    @Test func clearAccountCachesWipesStorageAndResetsContext() async throws {
        let h = Harness(); defer { h.cleanUp() }
        try await h.activateSyntheticAccount()
        try h.vault.writeRawKeychainItemForTest(account: "security-v4-notify", data: Data(repeating: 3, count: 32), afterFirstUnlock: true)
        #expect(h.pending.write(opID: 1, bucket: .draft, key: "draft:session:s", data: Data([1, 2, 3])).ok)
        h.settings.update { $0.theme = .dark }
        h.defaults.set("x", forKey: AccountDefaults.key("home-scope"))
        h.defaults.set("kept", forKey: "other")
        let oldContext = try #require(h.model.context)
        let oldExecutor = h.executor
        let cache = h.root.appending(path: "cache")
        #expect(FileManager.default.fileExists(atPath: cache.path))

        await h.model.resetLocalAccountAndLogout()

        #expect(h.model.context == nil)
        #expect(h.model.account == nil)
        #expect(h.model.lastServer == h.account.server)
        #expect(oldExecutor.calls.contains("close"))
        #expect(!h.vault.hasPersistedState())
        #expect(h.vault.readNotifyBlob() == nil)
        #expect(try h.vault.read(.pendingKey) == nil)
        #expect(!FileManager.default.fileExists(atPath: cache.path))
        #expect(!FileManager.default.fileExists(atPath: h.root.appending(path: "pending").path))
        #expect(h.settings.settings == LocalSettings())
        #expect(h.defaults.object(forKey: AccountDefaults.key("home-scope")) == nil)
        #expect(h.defaults.string(forKey: "other") == "kept")
        #expect(h.unregisteredPush == 1)
        #expect(h.cleared == 1)

        // Re-attaching an account yields a fresh AccountContext with clean memory on the same CoreHost.
        let host = h.model.coreHost
        await host.processEffectBatchForTesting([
            PBCoreEffect.with {
                $0.accountRuntimeChanged = .with {
                    $0.active = true
                    $0.server = h.account.server
                    $0.deviceID = h.account.deviceId
                    $0.accountID = h.account.accountId
                }
            }
        ])
        await eventually("re-attached") { h.model.context != nil }
        let next = try #require(h.model.context)
        #expect(next !== oldContext)
        #expect(next.coreHost === host)
    }

    @Test func pendingCreateResumesInOnboardingAndSetupResetClearsIt() async throws {
        let h = Harness(); defer { h.cleanUp() }
        h.model.setForeground(true)
        try await h.model.waitForLoadForTest()
        #expect(h.model.screens.appRoot.stage == .onboarding)

        await h.model.coreHost.onboardingSetMode(.create)
        await h.model.coreHost.onboardingCreateSubmit(serverInput: "https://relay.example.com")
        await eventually("recovery code generated") { !h.model.screens.onboarding.create.recoveryCode.isEmpty }
        #expect(h.vault.hasPersistedState())

        // Cold restart resumes directly in onboarding create mode with pending create.
        let second = h.makeModel(); defer { second.closeSyncForTest() }
        second.setForeground(true)
        try await second.waitForLoadForTest()
        #expect(second.screens.appRoot.stage == .onboarding)
        #expect(second.screens.onboarding.mode == .create)

        await second.coreHost.exitRequest(intent: .setupReset)
        await eventually("exit confirm awaits") { second.screens.exitConfirm.step == .awaitingConfirm }
        await second.coreHost.exitConfirm(true)
        await eventually("reset back to entry") {
            second.screens.appRoot.stage == .onboarding && second.screens.onboarding.mode == .entry
        }
        #expect(!h.vault.hasPersistedState())
    }

    @Test func networkChangesAreJudgedByWhatChanged() {
        let none = NetworkPath(available: false, interface: "")
        let wifi = NetworkPath(available: true, interface: "en0")
        let cell = NetworkPath(available: true, interface: "pdp_ip0")
        #expect(NetworkChange.from(.unknown, to: wifi) == .none)
        #expect(NetworkChange.from(.unknown, to: none) == .lost)
        #expect(NetworkChange.from(none, to: none) == .none)
        #expect(NetworkChange.from(none, to: wifi) == .gained)
        #expect(NetworkChange.from(wifi, to: cell) == .gained)
        #expect(NetworkChange.from(wifi, to: wifi) == .none)
    }

    @Test func networkCapabilitiesChangingOnTheSameInterfaceAreNotIgnored() {
        let old = NetworkPath(available: true, interface: "en0", ipv4: true, ipv6: false, dns: true, expensive: false, constrained: false)
        var next = old; next.ipv6 = true
        #expect(NetworkChange.from(old, to: next) == .gained)
        next = old; next.constrained = true
        #expect(NetworkChange.from(old, to: next) == .gained)
        #expect(NetworkChange.from(next, to: next) == .none)
    }
}
