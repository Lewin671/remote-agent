import Foundation
import Observation
@testable import RACore
import RAProto
import Security
import Testing
import os

@testable import RemoteAgent

/// Explicit completion signal counter for deterministic async waits without wall-clock polling.
final class SignalCounter: Sendable {
    private let state = OSAllocatedUnfairLock<(count: Int, waiters: [(id: UUID, target: Int, cont: CheckedContinuation<Void, Error>)])>(
        initialState: (0, [])
    )

    var count: Int { state.withLock { $0.count } }

    func signal() {
        let ready: [CheckedContinuation<Void, Error>] = state.withLock { s in
            s.count += 1
            var remaining: [(id: UUID, target: Int, cont: CheckedContinuation<Void, Error>)] = []
            var done: [CheckedContinuation<Void, Error>] = []
            for w in s.waiters {
                if s.count >= w.target { done.append(w.cont) }
                else { remaining.append(w) }
            }
            s.waiters = remaining
            return done
        }
        for c in ready { c.resume() }
    }

    func wait(atLeast target: Int) async throws {
        if count >= target { return }
        try Task.checkCancellation()
        let id = UUID()
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
                let immediate: Result<Void, Error>? = state.withLock { s in
                    if s.count >= target { return .success(()) }
                    if Task.isCancelled { return .failure(CancellationError()) }
                    s.waiters.append((id, target, cont))
                    return nil
                }
                if let immediate { cont.resume(with: immediate) }
            }
        } onCancel: {
            let cancelled: CheckedContinuation<Void, Error>? = state.withLock { s in
                guard let idx = s.waiters.firstIndex(where: { $0.id == id }) else { return nil }
                return s.waiters.remove(at: idx).cont
            }
            cancelled?.resume(throwing: CancellationError())
        }
    }
}

/// Suspends on `@Observable` mutations until `condition()` holds, with no wall-clock polling.
/// Hang protection is delegated to Swift Testing `.timeLimit` and xcodebuild's test time allowance.
@MainActor
private func awaitObservation(_ condition: @MainActor () -> Bool) async throws {
    while !condition() {
        try Task.checkCancellation()
        let gate = OSAllocatedUnfairLock<CheckedContinuation<Void, Error>?>(initialState: nil)
        let resumeOnce: @Sendable (Result<Void, Error>) -> Void = { result in
            let cont = gate.withLock { c -> CheckedContinuation<Void, Error>? in
                let taken = c
                c = nil
                return taken
            }
            cont?.resume(with: result)
        }
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
                let ready = OSAllocatedUnfairLock(initialState: false)
                let notify: @Sendable () -> Void = {
                    let first = ready.withLock { seen -> Bool in
                        guard !seen else { return false }
                        seen = true
                        return true
                    }
                    if first { resumeOnce(.success(())) }
                }
                gate.withLock { $0 = cont }
                withObservationTracking {
                    _ = condition()
                } onChange: {
                    notify()
                }
                if Task.isCancelled {
                    resumeOnce(.failure(CancellationError()))
                } else if condition() {
                    notify()
                }
            }
        } onCancel: {
            resumeOnce(.failure(CancellationError()))
        }
    }
}

/// Tracks the current phase of an async test so Swift Testing `.timeLimit`
/// cancellation immediately reports where the test stalled.
private final class PhaseTracker: Sendable {
    private let current = OSAllocatedUnfairLock<String>(initialState: "init")

    func enter(_ phase: String) {
        current.withLock { $0 = phase }
    }

    var phase: String {
        current.withLock { $0 }
    }
}

@MainActor
private func withPhaseDiagnostics(
    _ body: @MainActor (PhaseTracker) async throws -> Void
) async throws {
    let tracker = PhaseTracker()
    try await withTaskCancellationHandler {
        try await body(tracker)
    } onCancel: {
        Issue.record("timed out in phase: \(tracker.phase)")
    }
}

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
    let clearedSignal = SignalCounter()
    let unregisteredPushSignal = SignalCounter()
    let upgradeNoticeDismissedSignal = SignalCounter()
    var cleared: Int { clearedSignal.count }
    var unregisteredPush: Int { unregisteredPushSignal.count }
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
            vault: vault,
            cacheRoot: root.appending(path: "cache"),
            pending: pending,
            sharedFileRoot: root.appending(path: "shared-files")
        )
        parts.sharedFileQueue = SharedFileStorageQueue()
        parts.draftStorageQueue = DraftStorageQueue()
        parts.discardLibraryVideos = {}
        let upgradeSignal = upgradeNoticeDismissedSignal
        parts.dismissUpgradeNotice = { vault in
            vault.dismissLegacyRepairNotice()
            upgradeSignal.signal()
        }
        parts.network = FakeNetwork()
        parts.defaults = defaults
        parts.allowPlainHttp = true
        parts.makeExecutor = { [unowned self] account, _, _ in
            let exec = try RecordingExecutor(account: account)
            self.executors.append(exec)
            return (exec, exec.remote)
        }
        parts.deviceDescription = { "iOS 26.0 · iPhone" }
        parts.unregisterRemoteNotifications = { [unowned self] in self.unregisteredPushSignal.signal() }
        parts.clearNotifications = { [unowned self] in self.clearedSignal.signal() }
        if let erase { parts.erase = erase }
        return AppModel(settings: settings, parts: parts)
    }

    func awaitLoaded(_ target: AppModel? = nil) async throws {
        let m = target ?? model
        try await awaitObservation {
            let stage = m.screens.appRoot.stage
            return stage != .unspecified && stage != .loading && (stage != .ready || m.context != nil)
        }
    }

    var executor: RecordingExecutor { executors.last! }

    /// Activates an account on the single CoreHost by emitting `AccountRuntimeChangedEffect`
    /// and waits until `handleAccountRuntimeChanged` completes `host.startAsync()` on the FIFO chain.
    func activateSyntheticAccount() async throws {
        model.setForeground(true)
        try await awaitLoaded()
        let host = model.coreHost
        await host.lifecycleIdleForTesting()
        let started = OSAllocatedUnfairLock(initialState: false)
        await host.setDebugEventObserver { ev in
            if case .syncLifecycle(let lc)? = ev.event, lc.kind == .start {
                started.withLock { $0 = true }
            }
        }
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
        try await awaitObservation { self.model.context != nil }
        await host.lifecycleIdleForTesting()
        #expect(started.withLock { $0 })
        await host.setDebugEventObserver(nil)
    }

    func cleanUp() {
        model.closeSyncForTest()
        try? vault.clear()
        try? FileManager.default.removeItem(at: root)
        defaults.removePersistentDomain(forName: suite)
    }
}

@MainActor
@Suite(.timeLimit(.minutes(1))) struct AppModelTests {
    @Test func withoutAnAccountOnboardingIsShownAndNoExecutorIsCreated() async throws {
        let h = Harness(); defer { h.cleanUp() }
        #expect(h.model.screens.appRoot.stage == .unspecified)
        h.model.setForeground(true)
        try await h.awaitLoaded()
        #expect(h.model.screens.appRoot.stage == .onboarding)
        #expect(h.model.screens.onboarding.mode == .entry)
        #expect(h.model.context == nil)
        #expect(h.executors.isEmpty)
    }

    @Test func nothingIsReadInTheBackground() async {
        let h = Harness(); defer { h.cleanUp() }
        h.model.setForeground(false)
        h.model.loadAccount()
        await Task.yield()
        #expect(!h.model.hasCoreHostForTest)
        #expect(h.model.screens.appRoot.stage == .unspecified)
        #expect(h.executors.isEmpty)
    }

    @Test func theStoredAccountIsReadAndItsEngineStarted() async throws {
        struct GoFixture: Decodable {
            let server: String
            let accountId: String
            let clientId: String
            let clientEdSeed: Data
            let clientEdPub: Data
            let clientXPriv: Data
            let clientXPub: Data
            let clientRecord: Data
        }
        let h = Harness(); defer { h.cleanUp() }
        let fixtureURL = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .appending(path: "testdata/home-project-v4.json")
        let fix = try JSONDecoder().decode(GoFixture.self, from: Data(contentsOf: fixtureURL))
        try h.vault.seedTestRecord(
            edPrivate: fix.clientEdSeed,
            edPublic: fix.clientEdPub,
            xPrivate: fix.clientXPriv,
            xPublic: fix.clientXPub,
            recordBytes: fix.clientRecord
        )

        h.model.setForeground(true)
        try await h.awaitLoaded()
        #expect(h.model.screens.appRoot.stage == .ready)
        #expect(h.model.account?.deviceId == fix.clientId)
        #expect(h.model.account?.accountId == fix.accountId)
        let ctx = try #require(h.model.context)
        #expect(ctx.remote === h.executor.remote)
        // Media centers must already be installed the instant `context` is published (no inert fallback).
        #expect(ImageCenter.of(ctx) === ImageCenter.of(ctx.remote))
        #expect(!ctx.isUnloaded)
        _ = SharedFileCenter.of(ctx)
        let key = CipherStore.accountKey(server: fix.server, deviceId: fix.clientId)
        #expect(FileManager.default.fileExists(atPath: h.root.appending(path: "cache/\(key)/cache.sqlite").path))
    }

    @Test func theBackgroundStopsTheStreamAndTheForegroundStartsItAgain() async throws {
        let h = Harness(); defer { h.cleanUp() }
        try await h.activateSyntheticAccount()
        let host = h.model.coreHost
        await host.lifecycleIdleForTesting()
        let events = OSAllocatedUnfairLock<[PBCoreEvent]>(initialState: [])
        await host.setDebugEventObserver { ev in events.withLock { $0.append(ev) } }
        defer { Task { await host.setDebugEventObserver(nil) } }

        h.model.scenePhase(.background)
        #expect(!h.model.foreground)
        await host.lifecycleIdleForTesting()
        #expect(events.withLock { list in
            list.contains { if case .syncLifecycle(let lc)? = $0.event { return lc.kind == .stop } else { return false } }
        })
        // Inactive stays in the foreground: the app switcher does not drop the stream.
        events.withLock { $0.removeAll() }
        h.model.scenePhase(.inactive)
        h.model.scenePhase(.active)
        #expect(h.model.foreground)
        await host.lifecycleIdleForTesting()
        #expect(events.withLock { list in
            list.contains { if case .syncLifecycle(let lc)? = $0.event { return lc.kind == .start } else { return false } }
        })
        #expect(h.executors.count == 1)
    }

    @Test func aNetworkThatComesOrChangesIsForwardedAndDuplicatesAreSuppressed() async throws {
        let h = Harness(); defer { h.cleanUp() }
        try await h.activateSyntheticAccount()
        let host = h.model.coreHost
        await host.lifecycleIdleForTesting()
        let netEvents = OSAllocatedUnfairLock<[(Bool, String)]>(initialState: [])
        await host.setDebugEventObserver { ev in
            if case .networkChanged(let n)? = ev.event {
                netEvents.withLock { $0.append((n.available, n.signature)) }
            }
        }
        defer { Task { await host.setDebugEventObserver(nil) } }

        let wifi = NetworkPath(available: true, interface: "en0")
        let none = NetworkPath(available: false, interface: "")
        let cell = NetworkPath(available: true, interface: "pdp_ip0")
        h.model.networkChanged(wifi)
        h.model.networkChanged(wifi) // duplicate suppressed by AppModel
        h.model.networkChanged(none)
        h.model.networkChanged(cell)
        h.model.networkChanged(wifi)
        await host.lifecycleIdleForTesting()
        let recorded = netEvents.withLock { $0 }
        #expect(recorded.map(\.0) == [true, false, true, true])
        #expect(recorded.map(\.1) == [wifi.signature, none.signature, cell.signature, wifi.signature])
    }

    @Test func engineReceivesTheNetworkObservedBeforeAccountLoading() async throws {
        let h = Harness(); defer { h.cleanUp() }
        let wifi = NetworkPath(available: true, interface: "en0")
        let cell = NetworkPath(available: true, interface: "pdp_ip0")
        h.model.networkChanged(wifi)
        h.model.setForeground(true)
        try await h.awaitLoaded()
        let host = h.model.coreHost
        await host.lifecycleIdleForTesting()
        let trace = OSAllocatedUnfairLock<[String]>(initialState: [])
        await host.setDebugEventObserver { ev in
            switch ev.event {
            case .networkChanged(let n)?:
                trace.withLock { $0.append("net:\(n.available):\(n.signature)") }
            case .syncLifecycle(let lc)? where lc.kind == .start:
                trace.withLock { $0.append("start") }
            default:
                break
            }
        }
        defer { Task { await host.setDebugEventObserver(nil) } }

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
        try await awaitObservation { h.model.context != nil }
        await host.lifecycleIdleForTesting()
        // Pre-load wifi must arrive strictly before syncLifecycle(.start).
        let afterAttach = trace.withLock { $0 }
        let wifiIdx = try #require(afterAttach.firstIndex(of: "net:true:\(wifi.signature)"))
        let startIdx = try #require(afterAttach.firstIndex(of: "start"))
        #expect(wifiIdx < startIdx)

        h.model.networkChanged(cell)
        await host.lifecycleIdleForTesting()
        #expect(trace.withLock { $0 }.contains("net:true:\(cell.signature)"))
    }

    @Test func firstObservationAfterEngineAttachmentIsForwardedAsItsBaseline() async throws {
        let h = Harness(); defer { h.cleanUp() }
        try await h.activateSyntheticAccount()
        let host = h.model.coreHost
        await host.lifecycleIdleForTesting()
        let netEvents = OSAllocatedUnfairLock<[(Bool, String)]>(initialState: [])
        await host.setDebugEventObserver { ev in
            if case .networkChanged(let n)? = ev.event {
                netEvents.withLock { $0.append((n.available, n.signature)) }
            }
        }
        defer { Task { await host.setDebugEventObserver(nil) } }

        let wifi = NetworkPath(available: true, interface: "en0")
        let cell = NetworkPath(available: true, interface: "pdp_ip0")
        h.model.networkChanged(wifi)
        await host.lifecycleIdleForTesting()
        #expect(netEvents.withLock { $0.count == 1 && $0[0] == (true, wifi.signature) })
        h.model.networkChanged(cell)
        await host.lifecycleIdleForTesting()
        #expect(netEvents.withLock { $0.count == 2 && $0[1] == (true, cell.signature) })
    }

    @Test func aDeviceTheServerTurnedAwayKeepsLocalKeysAndCanRetryLogin() async throws {
        let h = Harness(); defer { h.cleanUp() }
        try await h.activateSyntheticAccount()
        let host = h.model.coreHost
        await host.lifecycleIdleForTesting()
        let events = OSAllocatedUnfairLock<[PBCoreEvent]>(initialState: [])
        await host.setDebugEventObserver { ev in events.withLock { $0.append(ev) } }
        defer { Task { await host.setDebugEventObserver(nil) } }

        h.model.screens.appRoot = .with {
            $0.stage = .removed
            $0.removed = .with { $0.canRetryLogin = true; $0.server = h.account.server }
        }
        #expect(h.model.screens.appRoot.stage == .removed)
        h.model.retryLogin()
        await host.lifecycleIdleForTesting()
        #expect(events.withLock { list in
            list.contains { if case .syncLifecycle(let lc)? = $0.event { return lc.kind == .retryLogin } else { return false } }
        })
    }

    @Test func corruptRegistrationIntentSurfacesBootstrapErrorAndSetupResetRecoversInPlace() async throws {
        try await withPhaseDiagnostics { phase in
            let h = Harness(); defer { h.cleanUp() }
            phase.enter("seedCorruptIntent")
            let seed = h.vault.executeStorage(.with {
                $0.op = .writeCas
                $0.bucket = .registrationIntent
                $0.key = "intent"
                $0.createOnly = true
                $0.nextRevision = "r1"
                $0.data = Data("synthetic-invalid-receipt".utf8)
            })
            #expect(seed.error.isEmpty && !seed.conflict)
            phase.enter("awaitBootstrapError")
            h.model.setForeground(true)
            try await h.awaitLoaded()
            #expect(h.model.screens.appRoot.stage == .bootstrapError)
            #expect(h.model.screens.appRoot.bootstrapError.canReset)
            #expect(h.vault.hasPersistedState())

            phase.enter("exitRequest")
            await h.model.coreHost.exitRequest(intent: .setupReset)
            phase.enter("awaitAwaitingConfirm")
            try await awaitObservation { h.model.screens.exitConfirm.step == .awaitingConfirm }
            phase.enter("exitConfirm")
            await h.model.coreHost.exitConfirm(true)
            phase.enter("waitUnregisteredPush")
            try await h.unregisteredPushSignal.wait(atLeast: 1)
            phase.enter("waitCleared")
            try await h.clearedSignal.wait(atLeast: 1)
            phase.enter("awaitOnboarding")
            try await awaitObservation { h.model.screens.appRoot.stage == .onboarding }

            phase.enter("verifyCleared")
            #expect(!h.vault.hasPersistedState())
            #expect(h.unregisteredPush == 1)
            #expect(h.cleared == 1)
            phase.enter("done")
        }
    }

    @Test func corruptRecordSurfacesBootstrapErrorAndSetupResetClearsState() async throws {
        try await withPhaseDiagnostics { phase in
            let h = Harness(); defer { h.cleanUp() }
            phase.enter("seedCorruptRecord")
            let seed = h.vault.executeStorage(.with {
                $0.op = .writeCas
                $0.bucket = .record
                $0.key = "record"
                $0.createOnly = true
                $0.nextRevision = "r1"
                $0.data = Data([1, 2, 3, 4])
            })
            #expect(seed.error.isEmpty && !seed.conflict)
            phase.enter("awaitBootstrapError")
            h.model.setForeground(true)
            try await h.awaitLoaded()
            #expect(h.model.screens.appRoot.stage == .bootstrapError)
            #expect(h.model.screens.appRoot.bootstrapError.canReset)

            phase.enter("exitRequest")
            await h.model.coreHost.exitRequest(intent: .setupReset)
            phase.enter("awaitAwaitingConfirm")
            try await awaitObservation { h.model.screens.exitConfirm.step == .awaitingConfirm }
            phase.enter("exitConfirm")
            await h.model.coreHost.exitConfirm(true)
            phase.enter("waitUnregisteredPush")
            try await h.unregisteredPushSignal.wait(atLeast: 1)
            phase.enter("waitCleared")
            try await h.clearedSignal.wait(atLeast: 1)
            phase.enter("awaitOnboarding")
            try await awaitObservation { h.model.screens.appRoot.stage == .onboarding }
            phase.enter("verifyCleared")
            #expect(h.unregisteredPush == 1)
            #expect(!h.vault.hasPersistedState())
            phase.enter("done")
        }
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
        try await h.awaitLoaded()
        #expect(h.model.screens.appRoot.stage == .bootstrapError)
        #expect(!h.model.screens.appRoot.bootstrapError.canReset)
        #expect(h.model.screens.appRoot.bootstrapError.canRetry)

        // Unlock device and resume foreground: AppModel automatically retries bootstrap.
        locked.withLock { $0 = false }
        h.model.setForeground(false)
        h.model.setForeground(true)
        try await awaitObservation { h.model.screens.appRoot.stage == .onboarding }
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
        try await h.awaitLoaded()
        #expect(h.model.screens.appRoot.stage == .onboarding)
        #expect(h.model.screens.onboarding.showLegacyRepairBanner)
        #expect(try h.vault.read(.pendingKey) == Data(repeating: 9, count: 32))
        #expect(!FileManager.default.fileExists(atPath: h.root.appending(path: "cache").path))

        // Cold restart still sees the banner until dismissed through Core.
        let second = h.makeModel(); defer { second.closeSyncForTest() }
        second.setForeground(true)
        try await h.awaitLoaded(second)
        #expect(second.screens.appRoot.stage == .onboarding)
        #expect(second.screens.onboarding.showLegacyRepairBanner)

        await second.coreHost.appRootDismissUpgradeNotice()
        try await awaitObservation { !second.screens.onboarding.showLegacyRepairBanner }
        try await h.upgradeNoticeDismissedSignal.wait(atLeast: 1)
        #expect(!h.vault.hasUpgradeNotice())
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
        try await withPhaseDiagnostics { phase in
            let h = Harness(); defer { h.cleanUp() }
            phase.enter("activateSyntheticAccount")
            try await h.activateSyntheticAccount()
            phase.enter("seedLocalState")
            try h.vault.writeRawKeychainItemForTest(account: "security-v4-notify", data: Data(repeating: 3, count: 32), afterFirstUnlock: true)
            #expect(h.pending.write(opID: 1, bucket: .draft, key: "draft:session:s", data: Data([1, 2, 3])).ok)
            h.settings.update { $0.theme = .dark }
            h.defaults.set("x", forKey: AccountDefaults.key("home-scope"))
            h.defaults.set("kept", forKey: "other")
            let oldContext = try #require(h.model.context)
            let oldExecutor = h.executor
            let cache = h.root.appending(path: "cache")
            #expect(FileManager.default.fileExists(atPath: cache.path))

            phase.enter("resetLocalAccountAndLogout")
            await h.model.resetLocalAccountAndLogout()

            phase.enter("verifyWiped")
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
            phase.enter("reattachProcessEffectBatch")
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
            phase.enter("awaitReattachContext")
            try await awaitObservation { h.model.context != nil }
            let next = try #require(h.model.context)
            #expect(next !== oldContext)
            #expect(next.coreHost === host)
            phase.enter("done")
        }
    }

    @Test func pendingCreateResumesInOnboardingAndSetupResetClearsIt() async throws {
        try await withPhaseDiagnostics { phase in
            let h = Harness(); defer { h.cleanUp() }
            phase.enter("awaitInitialOnboarding")
            h.model.setForeground(true)
            try await h.awaitLoaded()
            #expect(h.model.screens.appRoot.stage == .onboarding)

            phase.enter("onboardingCreateSubmit")
            await h.model.coreHost.onboardingSetMode(.create)
            await h.model.coreHost.onboardingCreateSubmit(serverInput: "https://relay.example.com")
            phase.enter("awaitRecoveryCode")
            try await awaitObservation { !h.model.screens.onboarding.create.recoveryCode.isEmpty }
            #expect(h.vault.hasPersistedState())

            // Cold restart resumes directly in onboarding create mode with pending create.
            phase.enter("coldRestartAwaitLoaded")
            let second = h.makeModel(); defer { second.closeSyncForTest() }
            second.setForeground(true)
            try await h.awaitLoaded(second)
            #expect(second.screens.appRoot.stage == .onboarding)
            #expect(second.screens.onboarding.mode == .create)

            phase.enter("exitRequest")
            await second.coreHost.exitRequest(intent: .setupReset)
            phase.enter("awaitAwaitingConfirm")
            try await awaitObservation { second.screens.exitConfirm.step == .awaitingConfirm }
            phase.enter("exitConfirm")
            await second.coreHost.exitConfirm(true)
            phase.enter("waitUnregisteredPush")
            try await h.unregisteredPushSignal.wait(atLeast: 1)
            phase.enter("waitCleared")
            try await h.clearedSignal.wait(atLeast: 1)
            phase.enter("awaitOnboardingEntry")
            try await awaitObservation {
                second.screens.appRoot.stage == .onboarding && second.screens.onboarding.mode == .entry
            }
            phase.enter("verifyCleared")
            #expect(!h.vault.hasPersistedState())
            phase.enter("done")
        }
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

    @Test func rapidNetworkChangesPreserveExactFifoOrder() async throws {
        let h = Harness(); defer { h.cleanUp() }
        try await h.activateSyntheticAccount()
        let host = h.model.coreHost
        await host.lifecycleIdleForTesting()
        let netEvents = OSAllocatedUnfairLock<[(Bool, String)]>(initialState: [])
        await host.setDebugEventObserver { ev in
            if case .networkChanged(let n)? = ev.event {
                netEvents.withLock { $0.append((n.available, n.signature)) }
            }
        }
        defer { Task { await host.setDebugEventObserver(nil) } }

        let sequence = [
            NetworkPath(available: true, interface: "en0"),
            NetworkPath(available: false, interface: ""),
            NetworkPath(available: true, interface: "pdp_ip0"),
            NetworkPath(available: true, interface: "en0"),
        ]
        var expected: [(Bool, String)] = []
        for round in 0..<5 {
            for item in sequence {
                var p = item
                if round % 2 == 1 { p.ipv6 = true }
                h.model.networkChanged(p)
                expected.append((p.available, p.signature))
            }
        }
        await host.lifecycleIdleForTesting()
        let recorded = netEvents.withLock { $0 }
        #expect(recorded.map(\.0) == expected.map(\.0))
        #expect(recorded.map(\.1) == expected.map(\.1))
    }

    @Test func coldStartAndForegroundTogglesPreserveLifecycleOrder() async throws {
        let h = Harness(); defer { h.cleanUp() }
        let host = h.model.ensureCoreHost()
        await host.lifecycleIdleForTesting()
        let log = OSAllocatedUnfairLock<[String]>(initialState: [])
        await host.setDebugEventObserver { ev in
            switch ev.event {
            case .clientInit?:
                log.withLock { $0.append("clientInit") }
            case .securityForeground(let fg)?:
                log.withLock { $0.append("fg:\(fg.foreground)") }
            default:
                break
            }
        }
        defer { Task { await host.setDebugEventObserver(nil) } }

        h.model.setForeground(true)
        h.model.setForeground(false)
        h.model.setForeground(true)
        await host.lifecycleIdleForTesting()
        #expect(log.withLock { $0 } == ["clientInit", "fg:true", "fg:false", "fg:true"])
    }

    @Test func demoLogoutUnloadsContextAndAllowsInertMediaCenterLookup() async throws {
        let h = Harness(); defer { h.cleanUp() }
        h.model.showDemo(account: Demo.account, remote: Demo.remote(), world: Demo.world(.everyday))
        let ctx = try #require(h.model.context)
        #expect(!ctx.isUnloaded)

        await h.model.logout()

        // Require unloaded state before calling ImageCenter.of/SharedFileCenter.of so a
        // regression fails cleanly here instead of tripping assertionFailure and crashing the host.
        try #require(ctx.isUnloaded)
        #expect(h.model.context == nil)
        _ = ImageCenter.of(ctx)
        _ = SharedFileCenter.of(ctx)
    }
}

@Suite(.timeLimit(.minutes(1))) struct StorageQueuePriorityTests {
    private func verifyDrainEscalation(
        enqueue: @escaping @Sendable (@escaping @Sendable () async -> Void) -> Void,
        drain: @escaping @Sendable () async -> Void,
        setOnDrainWait: @escaping @Sendable ((@Sendable () -> Void)?) -> Void
    ) async throws {
        let recorded = OSAllocatedUnfairLock<[(name: String, priority: TaskPriority)]>(initialState: [])

        // 1. Without a drainer, operations run at .utility.
        let op0Done = SignalCounter()
        enqueue {
            recorded.withLock { $0.append(("op0", Task.currentPriority)) }
            op0Done.signal()
        }
        try await op0Done.wait(atLeast: 1)
        // Wait for op0's post-op bookkeeping to mark the queue idle before starting the next phase.
        await Task.detached(priority: .utility) { await drain() }.value

        // 2. While a .high caller waits in drain(), remaining queued operations and follow-up
        //    operations enqueued during completion run at the escalated priority in strict FIFO order.
        let op1Started = SignalCounter()
        let releaseOp1 = SignalCounter()
        enqueue {
            recorded.withLock { $0.append(("op1", Task.currentPriority)) }
            op1Started.signal()
            try? await releaseOp1.wait(atLeast: 1)
        }
        try await op1Started.wait(atLeast: 1)

        enqueue {
            recorded.withLock { $0.append(("op2", Task.currentPriority)) }
            enqueue {
                recorded.withLock { $0.append(("op3", Task.currentPriority)) }
            }
        }

        setOnDrainWait { releaseOp1.signal() }
        defer { setOnDrainWait(nil) }
        let drainTask = Task.detached(priority: .high) {
            await drain()
        }
        await drainTask.value
        setOnDrainWait(nil)

        // 3. Once idle and drain() has returned, subsequent work restores .utility priority.
        let op4Done = SignalCounter()
        enqueue {
            recorded.withLock { $0.append(("op4", Task.currentPriority)) }
            op4Done.signal()
        }
        try await op4Done.wait(atLeast: 1)
        await Task.detached(priority: .utility) { await drain() }.value

        let entries = recorded.withLock { $0 }
        #expect(entries.map(\.name) == ["op0", "op1", "op2", "op3", "op4"])
        #expect(entries[0].priority == .utility)
        #expect(entries[1].priority == .utility)
        #expect(entries[2].priority >= .high)
        #expect(entries[3].priority >= .high)
        #expect(entries[4].priority == .utility)
    }

    @Test func draftStorageQueueEscalatesRemainingWorkWhileDrainWaitsAndRestoresUtilityWhenIdle() async throws {
        let queue = DraftStorageQueue()
        try await verifyDrainEscalation(
            enqueue: { queue.enqueue($0) },
            drain: { await queue.drain() },
            setOnDrainWait: { queue.setOnDrainWaitForTesting($0) }
        )
    }

    @Test func sharedFileStorageQueueEscalatesRemainingWorkWhileDrainWaitsAndRestoresUtilityWhenIdle() async throws {
        let queue = SharedFileStorageQueue()
        try await verifyDrainEscalation(
            enqueue: { queue.enqueue($0) },
            drain: { await queue.drain() },
            setOnDrainWait: { queue.setOnDrainWaitForTesting($0) }
        )
    }
}
