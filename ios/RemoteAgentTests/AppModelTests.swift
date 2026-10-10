import Foundation
import RACore
import RAProto
import Testing
import os

@testable import RemoteAgent

/// Waits until `condition` holds, for as long as a slow simulator may need.
@MainActor
func eventually(_ what: String = "", _ condition: @MainActor () -> Bool) async {
    for _ in 0..<500 {
        if condition() { return }
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
    Issue.record("never happened: \(what)")
}

/// The engine as the lifecycle sees it: a Remote that is never started shows
/// what the test tells it to, and every call is written down.
final class FakeEngine: SyncEngine, @unchecked Sendable {
    let remote: Remote
    private let lock = NSLock()
    private var log: [String] = []

    init(account: Account) throws {
        remote = try Remote(account: account, http: HTTP())
    }

    var account: Account { remote.account }
    var state: Feed<RemoteState> { remote.state }
    var calls: [String] { lock.withLock { log } }
    private func note(_ call: String) { lock.withLock { log.append(call) } }
    func forget() { lock.withLock { log.removeAll() } }

    func start() { note("start") }
    func stop() { note("stop") }
    func setNetwork(_ available: Bool) { note("network \(available)") }
    func reconnectNow() { note("reconnect") }
    func retryLogin() { note("retryLogin") }
    func close() { note("close") }
    /// What `describeSelf` throws, for a description that does not arrive.
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
    let root = FileManager.default.temporaryDirectory.appending(path: "ra-app-\(UUID().uuidString)", directoryHint: .isDirectory)
    let suite = "ra-app-\(UUID().uuidString)"
    let defaults: UserDefaults
    let settings: SettingsStore
    let pending: PendingStore
    let account = Account(
        server: "http://127.0.0.1:1", root: Data((0..<32).map { UInt8($0) }), deviceSeed: Data((0..<32).map { UInt8(90 - $0) }),
        deviceId: "00112233445566778899aabbccddeeff")
    private(set) var engines: [FakeEngine] = []
    var cleared = 0
    /// What the erase of a logout does instead of erasing, when set: called off the main actor.
    var erase: (@Sendable (AccountStorage) throws -> Void)?
    lazy var model: AppModel = {
        var parts = AppModel.Parts()
        parts.storage = AccountStorage(vault: vault, cacheRoot: root.appending(path: "cache"), pending: pending)
        parts.network = FakeNetwork()
        parts.defaults = defaults
        parts.makeEngine = { [unowned self] account, _ in
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
        pending = PendingStore(directory: root.appending(path: "pending"), vault: vault, defaults: defaults)
    }

    var engine: FakeEngine { engines.last! }

    func store() throws {
        try vault.save(account.stored, notifyKey: Data(repeating: 5, count: 32))
    }

    /// An account on the device, read and in use, in the foreground.
    func ready() async throws {
        try store()
        model.setForeground(true)
        await eventually("ready") { self.model.stage == .ready }
    }

    func cleanUp() {
        try? vault.clear()
        try? FileManager.default.removeItem(at: root)
        defaults.removePersistentDomain(forName: suite)
    }
}

@MainActor
@Suite struct AppModelTests {
    let h = Harness()

    @Test func withoutAnAccountTheJoinFlowIsShown() async {
        defer { h.cleanUp() }
        #expect(h.model.stage == .loading)
        h.model.setForeground(true)
        await eventually("join") { h.model.stage == .join }
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
        #expect(FileManager.default.fileExists(atPath: h.root.appending(path: "cache/\(key)/cache.sqlite").path))
    }

    @Test func nothingIsReadInTheBackground() async throws {
        defer { h.cleanUp() }
        try h.store()
        h.model.setForeground(false)
        h.model.loadAccount()
        try await Task.sleep(nanoseconds: 100_000_000)
        #expect(h.model.stage == .loading)
        #expect(h.engines.isEmpty)
    }

    @Test func aKeychainThatCannotBeReadIsSaidAndTriedAgain() async throws {
        defer { h.cleanUp() }
        try h.vault.write(.account, Data("not an account".utf8))
        h.model.setForeground(true)
        await eventually("failed") { if case .failed = h.model.stage { true } else { false } }
        #expect(h.model.stage == .failed(VaultError.unreadable.localizedDescription))
        try h.store()
        h.model.loadAccount()
        await eventually("ready") { h.model.stage == .ready }
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
        #expect(h.engine.calls.isEmpty)
        h.model.networkChanged(NetworkPath(available: false, interface: ""))
        #expect(h.engine.calls == ["network false"])
        h.model.networkChanged(NetworkPath(available: true, interface: "pdp_ip0"))
        #expect(h.engine.calls == ["network false", "network true", "reconnect"])
        h.model.networkChanged(wifi)
        #expect(h.engine.calls.suffix(2) == ["network true", "reconnect"])
        #expect(h.engine.calls.count == 5)
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
        // The keys stay until the user says otherwise.
        #expect(try h.vault.hasAccount())
        h.model.retryLogin()
        #expect(h.engine.calls.last == "retryLogin")
    }

    @Test func thisClientIsDescribedOnceItIsListedAndOnline() async throws {
        defer { h.cleanUp() }
        try await h.ready()
        var s = RemoteState()
        s.devices[h.account.deviceId] = DeviceView(id: h.account.deviceId, kind: .client, createdMs: 1)
        await h.engine.remote.showForTest(state: s)
        await eventually("state") { h.model.state == s }
        // Not while offline.
        #expect(!h.engine.calls.contains { $0.hasPrefix("describe") })
        s.conn = .online
        s.link = .online
        await h.engine.remote.showForTest(state: s)
        await eventually("described") { h.engine.calls.contains("describe ios iOS 26.0 · iPhone") }
        // Stored as wanted: nothing more to say.
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

    @Test func joiningStoresTheAccountAndStartsSyncing() async throws {
        defer { h.cleanUp() }
        h.model.setForeground(true)
        await eventually("join") { h.model.stage == .join }
        try await h.model.joined(h.account)
        #expect(h.model.stage == .ready)
        #expect(try h.vault.load() == h.account.stored)
        #expect(try h.vault.notifyKey() == Keys.derive(root: h.account.root).notify)
        #expect(h.engine.calls == ["network true", "start"])
    }

    @Test func loggingOutWipesTheKeysTheCacheAndWhatWasKeptForTheAccount() async throws {
        defer { h.cleanUp() }
        try await h.ready()
        try h.pending.put(folder: "s", name: "a", data: Data([1, 2, 3]))
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
        #expect(try !h.vault.hasAccount())
        #expect(try h.vault.notifyKey() == nil)
        #expect(try h.vault.read(.pendingKey) == nil)
        #expect(!FileManager.default.fileExists(atPath: cache.path))
        #expect(!FileManager.default.fileExists(atPath: h.root.appending(path: "pending").path))
        #expect(h.settings.settings == LocalSettings())
        #expect(h.defaults.object(forKey: AccountDefaults.key("home-scope")) == nil)
        #expect(h.defaults.string(forKey: "other") == "kept")
        #expect(h.cleared == 1)
        // A state the old engine publishes afterwards is nobody's.
        var late = RemoteState()
        late.machines["m"] = MachineView(id: "m", online: true, lastSeenMs: 1)
        await engine.remote.showForTest(state: late)
        try await Task.sleep(nanoseconds: 50_000_000)
        #expect(h.model.state == RemoteState())
        // The next account starts from nothing kept in memory.
        try await h.model.joined(h.account)
        let next = try #require(h.model.context)
        #expect(next !== context)
        #expect(next.memory.value(String.self) { "" } == "")
    }

    @Test func loggingOutReleasesAnAccountRetainedByAFailedPreferenceCallback() async throws {
        defer { h.cleanUp() }
        try await h.ready()
        weak var oldContext = h.model.context
        func failRemoval(_ context: AccountContext) async {
            context.prefsChanges.run(.hide(path: "/work/app"), onDone: { _ = context.account }) { _ in
                throw URLError(.timedOut)
            }
            await eventually("preference write failed") { context.prefsChanges.states.values.contains { !$0.saving } }
        }
        try await failRemoval(#require(h.model.context))
        #expect(oldContext != nil)
        await h.model.logout()
        #expect(oldContext == nil)
    }

    @Test func nothingIsReadWhileALogoutIsErasing() async throws {
        defer { h.cleanUp() }
        // The erase is slow: it has not reached the keys when the app goes to the background and comes back.
        let started = OSAllocatedUnfairLock(initialState: false)
        let go = DispatchSemaphore(value: 0)
        h.erase = { storage in
            started.withLock { $0 = true }
            go.wait()
            try LocalAccountData.erase(vault: storage.vault, cacheRoot: storage.cacheRoot, pending: storage.pending)
        }
        try await h.ready()
        let leaving = Task { await h.model.logout() }
        await eventually("erasing") { started.withLock { $0 } }
        h.model.scenePhase(.background)
        h.model.scenePhase(.active)
        h.model.loadAccount()
        try await Task.sleep(nanoseconds: 200_000_000)
        // The account being removed is not found and put back in use.
        #expect(h.model.stage == .loading)
        #expect(h.model.context == nil)
        #expect(h.engines.count == 1)
        go.signal()
        await leaving.value
        #expect(h.model.stage == .join)
        #expect(h.model.context == nil && h.engines.count == 1)
        #expect(try !h.vault.hasAccount())
    }

    @Test func anEraseThatFailedIsFinishedBeforeAnythingIsReadAgain() async throws {
        defer { h.cleanUp() }
        struct Stuck: LocalizedError { var errorDescription: String? { "没能删除缓存" } }
        let tries = OSAllocatedUnfairLock(initialState: 0)
        h.erase = { storage in
            // The first try gets as far as the keys.
            if tries.withLock({ $0 += 1; return $0 }) == 1 {
                try storage.vault.clear()
                throw Stuck()
            }
            try LocalAccountData.erase(vault: storage.vault, cacheRoot: storage.cacheRoot, pending: storage.pending)
        }
        try await h.ready()
        try h.pending.put(folder: "s", name: "a", data: Data([1, 2, 3]))
        await h.model.logout()
        #expect(h.model.stage == .failed("没能删除缓存"))
        #expect(FileManager.default.fileExists(atPath: h.root.appending(path: "pending").path))
        // 重试 erases again, and only then is there no account to find.
        h.model.loadAccount()
        await eventually("join") { h.model.stage == .join }
        #expect(tries.withLock { $0 } == 2)
        #expect(!FileManager.default.fileExists(atPath: h.root.appending(path: "pending").path))
        #expect(!FileManager.default.fileExists(atPath: h.root.appending(path: "cache").path))
        // Erased once: the next read is only a read.
        h.model.scenePhase(.background)
        h.model.scenePhase(.active)
        try await Task.sleep(nanoseconds: 100_000_000)
        #expect(tries.withLock { $0 } == 2)
    }

    @Test func aDescriptionThatDidNotArriveIsSentAgainOnTheNextConnection() async throws {
        defer { h.cleanUp() }
        try await h.ready()
        h.engine.describeFailure = URLError(.networkConnectionLost)
        var s = RemoteState()
        s.devices[h.account.deviceId] = DeviceView(id: h.account.deviceId, kind: .client, createdMs: 1)
        s.conn = .online
        s.link = .online
        await h.engine.remote.showForTest(state: s)
        await eventually("tried") { h.engine.calls.contains { $0.hasPrefix("describe") } }
        // Still the same connection and the same revision: the failure is not tried over and over.
        s.machines["m1"] = MachineView(id: "m1", online: true, lastSeenMs: 1)
        await h.engine.remote.showForTest(state: s)
        await eventually("state") { h.model.state == s }
        #expect(h.engine.calls.filter { $0.hasPrefix("describe") }.count == 1)
        // The connection drops and comes back. The server stored nothing, so the revision is the same.
        var off = s
        off.conn = .offline
        off.link = .waiting
        await h.engine.remote.showForTest(state: off)
        await eventually("offline") { h.model.state == off }
        h.engine.describeFailure = nil
        await h.engine.remote.showForTest(state: s)
        await eventually("tried again") { h.engine.calls.filter { $0.hasPrefix("describe") }.count == 2 }
    }
}
