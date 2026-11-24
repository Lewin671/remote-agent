import Connect
import Foundation
import RACore
import RAProto
import Testing
import os

@testable import RemoteAgent

/// The engine as the lifecycle sees it: a Remote that is never started shows
/// what the test tells it to, and every call is written down.
final class FakeEngine: SyncEngine, @unchecked Sendable {
    let remote: Remote
    private let lock = NSLock()
    private var log: [String] = []
    private var signatures: [String] = []
    var networkSignatures: [String] { lock.withLock { signatures } }

    init(account: Account) throws {
        remote = try Remote(account: account, http: HTTP(), allowPlainHttp: true)
    }

    var account: Account { remote.account }
    var state: Feed<RemoteState> { remote.state }
    var calls: [String] { lock.withLock { log } }
    private func note(_ call: String) { lock.withLock { log.append(call) } }
    func forget() { lock.withLock { log.removeAll() } }

    func start() { note("start") }
    func stop() { note("stop") }
    func setNetwork(_ available: Bool) { note("network \(available)") }
    func networkChanged(_ signature: String, available: Bool) {
        lock.withLock {
            let previous = signatures.last
            guard previous != signature else { return }
            signatures.append(signature)
            log.append("network \(available)")
            if previous != nil && available { log.append("reconnect") }
        }
    }
    func reconnectNow() { note("reconnect") }
    func retryLogin() { note("retryLogin") }
    func close() { note("close") }

    var describeFailure: (any Error)? {
        get { lock.withLock { failure } }
        set { lock.withLock { failure = newValue } }
    }
    private var failure: (any Error)?

    func describeSelf(platform: String, description: String) async throws {
        note("describe \(platform) \(description)")
        if let describeFailure { throw describeFailure }
    }
}

/// A network that says only what the test says.
final class FakeNetwork: NetworkWatching, @unchecked Sendable {
    func start(_ onChange: @escaping @Sendable (NetworkPath) -> Void) {}
    func stop() {}
}

/// An AppModel on a Keychain service, a cache directory and defaults of its own.
@MainActor
final class Harness {
    let vault = Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)")
    let root = FileManager.default.temporaryDirectory.appending(
        path: "ra-app-\(UUID().uuidString)", directoryHint: .isDirectory)
    let suite = "ra-app-\(UUID().uuidString)"
    let defaults: UserDefaults
    let settings: SettingsStore
    let pending: PendingStore
    let account = Account(
        server: "http://127.0.0.1:1",
        deviceId: "00112233445566778899aabbccddeeff",
        accountId: "11111111111111111111111111111111")
    private(set) var engines: [FakeEngine] = []
    var cleared = 0
    var bootstrapStatus: PBSecurityWorkflowStatus?
    var bootstrapError: (any Error)?
    var exitAdvice = PBSecurityExitAdvice()

    /// What the erase of a logout does instead of erasing, when set: called off the main actor.
    var erase: (@Sendable (AccountStorage) throws -> Void)?
    lazy var model: AppModel = {
        var parts = AppModel.Parts()
        parts.storage = AccountStorage(
            vault: vault, cacheRoot: root.appending(path: "cache"), pending: pending)
        parts.network = FakeNetwork()
        parts.defaults = defaults
        parts.allowPlainHttp = true
        parts.bootstrapSecurity = { [unowned self] vault in
            let (err, status) = await MainActor.run { (self.bootstrapError, self.bootstrapStatus) }
            if let err { throw err }
            if let status { return status }
            let host = try StandaloneSecurityHost(store: vault, http: AppModel.http, allowPlainHttp: true)
            defer { host.close() }
            return try await host.client.bootstrap()
        }
        parts.checkExitAdvice = { [unowned self] _ in await MainActor.run { self.exitAdvice } }
        parts.makeEngine = { [unowned self] account, _, _, _ in
            let engine = try FakeEngine(account: account)
            self.engines.append(engine)
            return EngineParts(engine: engine, remote: engine.remote)
        }
        parts.frame = { _ in }
        parts.deviceDescription = { "iOS 26.0 · iPhone" }
        parts.clearNotifications = { [unowned self] in self.cleared += 1 }
        if let erase { parts.erase = erase }
        return AppModel(settings: settings, parts: parts)
    }()

    init() {
        defaults = UserDefaults(suiteName: suite)!
        settings = SettingsStore(defaults: defaults)
        pending = PendingStore(
            directory: root.appending(path: "pending"), vault: vault)
    }

    var engine: FakeEngine { engines.last! }

    func storeReady() throws {
        _ = vault.executeStorage(.with {
            $0.op = .writeCas
            $0.bucket = .record
            $0.key = "record"
            $0.createOnly = true
            $0.nextRevision = "r1"
            $0.data = Data("ready".utf8)
        })
        bootstrapStatus = .with {
            $0.bootstrap = .ready
            $0.server = account.server
            $0.deviceID = account.deviceId
            $0.accountID = account.accountId
            $0.isMember = true
        }
        bootstrapError = nil
    }

    /// An account on the device, read and in use, in the foreground.
    func ready() async throws {
        try storeReady()
        model.setForeground(true)
        await model.waitForLoadForTest()
        #expect(model.stage == .ready)
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
    let h = Harness()

    @Test func failedPendingRegistrationCanBeExplicitlyDiscardedWithoutAnAccount() async throws {
        defer { h.cleanUp() }
        // Seed malformed registration intent bytes so real Go bootstrap reports .invalid cleanly.
        _ = h.vault.executeStorage(.with {
            $0.op = .writeCas
            $0.bucket = .registrationIntent
            $0.key = "intent"
            $0.createOnly = true
            $0.nextRevision = "r1"
            $0.data = Data("synthetic-invalid-receipt".utf8)
        })
        h.model.setForeground(true)
        await h.model.waitForLoadForTest()
        guard case .failed = h.model.stage else {
            Issue.record("expected failed pending registration, got \(h.model.stage)")
            return
        }
        #expect(h.vault.hasPersistedState())
        await h.model.logout()
        #expect(h.model.stage == .join)
        #expect(!h.vault.hasPersistedState())
        h.model.loadAccount()
        await h.model.waitForLoadForTest()
        #expect(h.model.stage == .join)
    }

    @Test func withoutAnAccountTheJoinFlowIsShown() async {
        defer { h.cleanUp() }
        #expect(h.model.stage == .loading)
        h.model.setForeground(true)
        await h.model.waitForLoadForTest()
        #expect(h.model.stage == .join)
        #expect(h.model.context == nil)
        #expect(h.engines.isEmpty)
    }

    @Test func theStoredAccountIsReadAndItsEngineStarted() async throws {
        defer { h.cleanUp() }
        try await h.ready()
        #expect(h.model.account?.deviceId == h.account.deviceId)
        #expect(h.model.context?.remote === h.engine.remote)
        // Told of the network before it connects.
        #expect(h.engine.calls == ["network true", "start"])
        // The cache is the account's own directory, named by neither the server nor the device.
        let key = CipherStore.accountKey(server: h.account.server, deviceId: h.account.deviceId)
        #expect(
            FileManager.default.fileExists(
                atPath: h.root.appending(path: "cache/\(key)/cache.sqlite").path))
    }

    @Test func nothingIsReadInTheBackground() async throws {
        defer { h.cleanUp() }
        try h.storeReady()
        h.model.setForeground(false)
        h.model.loadAccount()
        await h.model.waitForLoadForTest()
        await Task.yield()
        #expect(h.model.stage == .loading)
        #expect(h.engines.isEmpty)
    }

    @Test func aKeychainThatCannotBeReadIsSaidAndTriedAgain() async throws {
        defer { h.cleanUp() }
        h.bootstrapError = VaultError.unreadable
        h.model.setForeground(true)
        await h.model.waitForLoadForTest()
        #expect(h.model.stage == .failed(VaultError.unreadable.localizedDescription))
        try h.storeReady()
        h.model.loadAccount()
        await h.model.waitForLoadForTest()
        #expect(h.model.stage == .ready)
    }

    @Test func theBackgroundStopsTheStreamAndTheForegroundStartsItAgain() async throws {
        defer { h.cleanUp() }
        try await h.ready()
        h.engine.forget()
        h.model.scenePhase(.background)
        #expect(!h.model.foreground)
        #expect(h.engine.calls == ["stop"])
        // Inactive is still in front: the app switcher does not drop the stream.
        h.model.scenePhase(.inactive)
        h.model.scenePhase(.active)
        #expect(h.model.foreground)
        #expect(h.engine.calls == ["stop", "start"])
        #expect(h.engines.count == 1)
    }

    @Test func aNetworkThatComesOrChangesReconnectsAndOneThatGoesIsOnlyTold() async throws {
        defer { h.cleanUp() }
        try await h.ready()
        h.engine.forget()
        let wifi = NetworkPath(available: true, interface: "en0")
        h.model.networkChanged(wifi)
        h.model.networkChanged(wifi)
        #expect(h.engine.calls == ["network true"])
        h.engine.forget()
        h.model.networkChanged(NetworkPath(available: false, interface: ""))
        #expect(h.engine.calls == ["network false"])
        h.model.networkChanged(NetworkPath(available: true, interface: "pdp_ip0"))
        #expect(h.engine.calls == ["network false", "network true", "reconnect"])
        h.model.networkChanged(wifi)
        #expect(h.engine.calls.suffix(2) == ["network true", "reconnect"])
        #expect(h.engine.calls.count == 5)
    }

    @Test func engineReceivesTheNetworkObservedBeforeAccountLoading() async throws {
        defer { h.cleanUp() }
        let wifi = NetworkPath(available: true, interface: "en0")
        let cellular = NetworkPath(available: true, interface: "pdp_ip0")
        h.model.networkChanged(wifi)
        try await h.ready()
        #expect(h.engine.networkSignatures == [wifi.signature])
        #expect(h.engine.calls == ["network true", "start"])
        h.model.networkChanged(cellular)
        #expect(h.engine.networkSignatures == [wifi.signature, cellular.signature])
        #expect(h.engine.calls.suffix(2) == ["network true", "reconnect"])
    }

    @Test func firstObservationAfterEngineAttachmentIsForwardedAsItsBaseline() async throws {
        defer { h.cleanUp() }
        try await h.ready()
        #expect(h.engine.networkSignatures.isEmpty)
        let wifi = NetworkPath(available: true, interface: "en0")
        h.model.networkChanged(wifi)
        #expect(h.engine.networkSignatures == [wifi.signature])
        #expect(!h.engine.calls.contains("reconnect"))
        let cellular = NetworkPath(available: true, interface: "pdp_ip0")
        h.model.networkChanged(cellular)
        #expect(h.engine.calls.filter { $0 == "reconnect" }.count == 1)
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

    @Test func anAccountOfflineAtStartIsToldSo() async throws {
        defer { h.cleanUp() }
        h.model.networkChanged(NetworkPath(available: false, interface: ""))
        try await h.ready()
        #expect(h.engine.calls == ["network false", "start"])
    }

    @Test func theEnginesStateIsCopiedToTheModel() async throws {
        defer { h.cleanUp() }
        try await h.ready()
        var s = RemoteState()
        s.machines["m1"] = MachineView(id: "m1", online: true, lastSeenMs: 1)
        s.conn = .online
        s.link = .online
        await h.engine.remote.showForTest(state: s)
        await eventually("state") { h.model.state == s }
        #expect(!h.model.removed)
    }

    @Test func aDeviceTheServerTurnedAwayShowsOnlyTheNoticeAndCanTryAgain() async throws {
        defer { h.cleanUp() }
        try await h.ready()
        var s = RemoteState()
        s.removed = true
        await h.engine.remote.showForTest(state: s)
        await eventually("removed") { h.model.removed }
        #expect(h.vault.hasPersistedState())
        h.model.retryLogin()
        #expect(h.engine.calls.last == "retryLogin")
    }

    @Test func thisClientIsDescribedOnceItIsListedAndOnline() async throws {
        defer { h.cleanUp() }
        try await h.ready()
        var s = RemoteState()
        s.devices[h.account.deviceId] = DeviceView(
            id: h.account.deviceId, kind: .client, createdMs: 1)
        await h.engine.remote.showForTest(state: s)
        await eventually("state") { h.model.state == s }
        #expect(!h.engine.calls.contains { $0.hasPrefix("describe") })
        s.conn = .online
        s.link = .online
        await h.engine.remote.showForTest(state: s)
        await eventually("described") { h.engine.calls.contains("describe ios iOS 26.0 · iPhone") }
        h.engine.forget()
        s.devices[h.account.deviceId]?.meta = .with {
            $0.platform = "ios"
            $0.description_p = "iOS 26.0 · iPhone"
        }
        s.devices[h.account.deviceId]?.metaRev = 1
        await h.engine.remote.showForTest(state: s)
        await eventually("state") { h.model.state == s }
        #expect(h.engine.calls.isEmpty)
    }

    @Test func joiningStartsSyncingForTheJoinedAccount() async throws {
        defer { h.cleanUp() }
        h.model.setForeground(true)
        await h.model.waitForLoadForTest()
        #expect(h.model.stage == .join)
        try await h.model.joined(h.account)
        #expect(h.model.stage == .ready)
        #expect(h.model.account == h.account)
        #expect(h.engine.calls == ["network true", "start"])
    }

    @Test func loggingOutWipesTheKeysTheCacheAndWhatWasKeptForTheAccount() async throws {
        defer { h.cleanUp() }
        try await h.ready()
        try h.vault.writeRawKeychainItemForTest(account: "security-v4-notify", data: Data(repeating: 3, count: 32), afterFirstUnlock: true)
        #expect(h.pending.write(opID: 1, bucket: .draft, key: "draft:session:s", data: Data([1, 2, 3])).ok)
        h.settings.update { $0.theme = .dark }
        h.defaults.set("x", forKey: AccountDefaults.key("home-scope"))
        h.defaults.set("kept", forKey: "other")
        let engine = h.engine
        let context = try #require(h.model.context)
        _ = context.memory.value(String.self) { "draft" }
        let cache = h.root.appending(path: "cache")
        #expect(FileManager.default.fileExists(atPath: cache.path))

        await h.model.logout()

        #expect(h.model.stage == .join)
        #expect(h.model.context == nil)
        #expect(h.model.account == nil)
        #expect(h.model.state == RemoteState())
        #expect(h.model.lastServer == h.account.server)
        #expect(engine.calls.last == "close")
        #expect(!h.vault.hasPersistedState())
        #expect(h.vault.readNotifyBlob() == nil)
        #expect(try h.vault.read(.pendingKey) == nil)
        #expect(!FileManager.default.fileExists(atPath: cache.path))
        #expect(!FileManager.default.fileExists(atPath: h.root.appending(path: "pending").path))
        #expect(h.settings.settings == LocalSettings())
        #expect(h.defaults.object(forKey: AccountDefaults.key("home-scope")) == nil)
        #expect(h.defaults.string(forKey: "other") == "kept")
        #expect(h.cleared == 1)
        var late = RemoteState()
        late.machines["m"] = MachineView(id: "m", online: true, lastSeenMs: 1)
        await engine.remote.showForTest(state: late)
        await Task.yield()
        #expect(h.model.state == RemoteState())
        try await h.model.joined(h.account)
        let next = try #require(h.model.context)
        #expect(next !== context)
        #expect(next.memory.value(String.self) { "" } == "")
    }

    @Test func nothingIsReadWhileALogoutIsErasing() async throws {
        defer { h.cleanUp() }
        let started = OSAllocatedUnfairLock(initialState: false)
        let go = DispatchSemaphore(value: 0)
        h.erase = { storage in
            started.withLock { $0 = true }
            go.wait()
            try LocalAccountData.erase(
                vault: storage.vault, cacheRoot: storage.cacheRoot, pending: storage.pending)
        }
        try await h.ready()
        let leaving = Task { await h.model.logout() }
        await eventually("erasing") { started.withLock { $0 } }
        h.model.scenePhase(.background)
        h.model.scenePhase(.active)
        h.model.loadAccount()
        await h.model.waitForLoadForTest()
        #expect(h.model.stage == .loading)
        #expect(h.model.context == nil)
        #expect(h.engines.count == 1)
        go.signal()
        await leaving.value
        #expect(h.model.stage == .join)
        #expect(h.model.context == nil && h.engines.count == 1)
        #expect(!h.vault.hasPersistedState())
    }

    @Test func anEraseThatFailedIsFinishedBeforeAnythingIsReadAgain() async throws {
        defer { h.cleanUp() }
        struct Stuck: LocalizedError { var errorDescription: String? { "没能删除缓存" } }
        let tries = OSAllocatedUnfairLock(initialState: 0)
        h.erase = { storage in
            if tries.withLock({ $0 += 1; return $0 }) == 1 {
                try storage.vault.clear()
                throw Stuck()
            }
            try LocalAccountData.erase(
                vault: storage.vault, cacheRoot: storage.cacheRoot, pending: storage.pending)
        }
        try await h.ready()
        #expect(h.pending.write(opID: 1, bucket: .draft, key: "draft:session:s", data: Data([1, 2, 3])).ok)
        await h.model.logout()
        #expect(h.model.stage == .failed("没能删除缓存"))
        #expect(FileManager.default.fileExists(atPath: h.root.appending(path: "cache").path))
        h.bootstrapStatus = nil
        h.model.loadAccount()
        await h.model.waitForLoadForTest()
        #expect(h.model.stage == .join)
        #expect(tries.withLock { $0 } == 2)
        #expect(!FileManager.default.fileExists(atPath: h.root.appending(path: "pending").path))
        #expect(!FileManager.default.fileExists(atPath: h.root.appending(path: "cache").path))
        h.model.scenePhase(.background)
        h.model.scenePhase(.active)
        await h.model.waitForLoadForTest()
        #expect(tries.withLock { $0 } == 2)
    }

    @Test func aDescriptionThatDidNotArriveIsSentAgainOnTheNextConnection() async throws {
        defer { h.cleanUp() }
        try await h.ready()
        h.engine.describeFailure = URLError(.networkConnectionLost)
        var s = RemoteState()
        s.devices[h.account.deviceId] = DeviceView(
            id: h.account.deviceId, kind: .client, createdMs: 1)
        s.conn = .online
        s.link = .online
        await h.engine.remote.showForTest(state: s)
        await eventually("tried") { h.engine.calls.contains { $0.hasPrefix("describe") } }
        s.machines["m1"] = MachineView(id: "m1", online: true, lastSeenMs: 1)
        await h.engine.remote.showForTest(state: s)
        await eventually("state") { h.model.state == s }
        #expect(h.engine.calls.filter { $0.hasPrefix("describe") }.count == 1)
        var off = s
        off.conn = .offline
        off.link = .waiting
        await h.engine.remote.showForTest(state: off)
        await eventually("offline") { h.model.state == off }
        h.engine.describeFailure = nil
        await h.engine.remote.showForTest(state: s)
        await eventually("tried again") {
            h.engine.calls.filter { $0.hasPrefix("describe") }.count == 2
        }
    }

    @Test func aLeaseRenewalRetriesAnAutomaticDescriptionWithoutAConnectionOrRevisionChange() async throws {
        let h = Harness()
        defer { h.cleanUp() }
        try await h.ready()
        h.engine.describeFailure = SecurityFailure.lease
        var s = RemoteState()
        s.devices[h.account.deviceId] = DeviceView(id: h.account.deviceId, kind: .client, createdMs: 1)
        s.conn = .online; s.link = .online; s.leaseEndMs = 1
        await h.engine.remote.showForTest(state: s)
        await eventually("first description") { h.engine.calls.filter { $0.hasPrefix("describe") }.count == 1 }
        h.engine.describeFailure = nil
        s.leaseEndMs = 2
        await h.engine.remote.showForTest(state: s)
        await eventually("description after renewal") { h.engine.calls.filter { $0.hasPrefix("describe") }.count == 2 }
    }

    @Test func legacyV3WipeClearsCipherCachePreservesPendingKeyAndPersistsRepairNoticeAcrossColdRestart() async throws {
        defer { h.cleanUp() }
        try h.vault.writeRawKeychainItemForTest(account: "account", data: Data("v3-acct".utf8))
        try h.vault.writeRawKeychainItemForTest(account: "security-v3", data: Data("v3-sec".utf8))
        try h.vault.write(.pendingKey, Data(repeating: 9, count: 32))
        let cache = try CipherStore.open(root: h.root.appending(path: "cache"), account: "legacy")
        await cache.putBlob(name: "stale", data: Data([1]))
        try cache.close()

        h.model.setForeground(true)
        await h.model.waitForLoadForTest()
        #expect(h.model.stage == .join)
        #expect(h.model.legacyRepairNotice)
        #expect(try h.vault.read(.pendingKey) == Data(repeating: 9, count: 32))
        #expect(!FileManager.default.fileExists(atPath: h.root.appending(path: "cache").path))

        // Cold restart still sees the banner until dismissed.
        var parts = AppModel.Parts()
        parts.storage = AccountStorage(vault: h.vault, cacheRoot: h.root.appending(path: "cache"), pending: h.pending)
        parts.network = FakeNetwork()
        parts.defaults = h.defaults
        parts.allowPlainHttp = true
        let second = AppModel(settings: h.settings, parts: parts)
        second.setForeground(true)
        await second.waitForLoadForTest()
        #expect(second.stage == .join)
        #expect(second.legacyRepairNotice)

        second.dismissUpgradeNotice()
        await Task.yield()
        #expect(!second.legacyRepairNotice)
        try await Task.sleep(nanoseconds: 50_000_000)
        #expect(!h.vault.hasUpgradeNotice())
    }

    @Test func logoutClearsPendingBootstrapAndDoesNotResumeIt() async throws {
        let h = Harness(); defer { h.cleanUp() }
        let host = try StandaloneSecurityHost(store: h.vault, http: AppModel.http, allowPlainHttp: true)
        _ = try await host.client.prepareCreation(server: "https://relay.example.com")
        host.close()
        h.model.setForeground(true)
        await h.model.waitForLoadForTest()
        #expect(h.model.securityStatus?.bootstrap == .pendingCreate)
        h.exitAdvice = .with { $0.authority = true; $0.unresolved = true }
        let adv = await h.model.rejoinAdvice()
        #expect(adv?.text.contains(L10n.securityIosASecurityOperationIsUnfinishedRetryOrVerifyIts()) == true)
        await h.model.logout()
        #expect(h.model.securityStatus == nil)
        #expect(!h.vault.hasPersistedState())
        h.model.loadAccount()
        await h.model.waitForLoadForTest()
        #expect(h.model.stage == .join)
        #expect(h.model.securityStatus?.bootstrap == .empty)
    }

    @Test func unreadableSecurityRequiresConfirmationButDoesNotTrapTheUser() async throws {
        let h = Harness(); defer { h.cleanUp() }
        try h.vault.writeRawKeychainItemForTest(account: "security-v4-buckets", data: Data([0]))
        var parts = AppModel.Parts()
        parts.storage = AccountStorage(vault: h.vault, cacheRoot: h.root.appending(path: "cache"), pending: h.pending)
        parts.network = FakeNetwork()
        parts.defaults = h.defaults
        parts.allowPlainHttp = true
        let realModel = AppModel(settings: h.settings, parts: parts)
        let adv = await realModel.rejoinAdvice()
        #expect(adv?.text.contains("没能读取") == true)
        #expect(adv?.text.contains("电脑上的会话不受影响") == true)
    }
}
