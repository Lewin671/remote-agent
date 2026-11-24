// Pure M2 Executor & bridge between SwiftUI screens (`CoreScreens`) and Go `CoreClient`.
// Owns CoreClient, host clock/calendar, entropy, single-shot timers, stream & RPC
// task execution, and sequential CipherCache persistence. Zero sync rules live here.
import AVFoundation
import CryptoKit
import Foundation
import Observation
import RACore
import RAProto
import Security

/// In-memory Core proto snapshot for `-demo` / preview launches (no `RemoteState` reverse-conversion).
struct DemoWorld: Equatable, Sendable {
    var machines: [String: PBCorePlainMachine] = [:]
    var sessions: [String: PBCorePlainSession] = [:]
    var devices: [String: PBCorePlainDevice] = [:]
    var prefs: PBCorePlainAccountPrefs = .with { $0.prefs.schema = 1 }
    var connection: PBCoreConnState = .connected
    var error = ""
    var removed = false
    var authorityDeviceId = ""
    var leaseEndMs: Int64 = 0
    var recoveryPending = false
    var recoveryReadyMs: Int64 = 0
    var clockSkewed = false
}

/// Main-actor observable snapshots published by `CoreHost` for the migrated screens.
@MainActor
@Observable
final class CoreScreens {
    struct RestoredComposer: Identifiable, Equatable, Sendable {
        let id = UUID()
        let effect: PBSessionRestoreComposerEffect
    }

    struct PendingCleanup: Identifiable, Equatable, Sendable {
        let id = UUID()
        let effect: PBPrepareCleanupSessionEffect
    }

    var home = PBCoreHomeViewModel()
    var syncState = PBCoreSyncStateView()
    var projectDetail = PBCoreProjectDetailView()
    var manageProjects = PBCoreManageProjectsView()
    var dirPickers: [String: PBCoreDirPickerView] = [:]
    var newSession = PBCoreNewSessionViewModel()
    var sessions: [String: PBSessionViewModel] = [:]
    var schedules = PBSchedulesViewModel()
    var scheduleDetail = PBScheduleDetailViewModel()
    var scheduleForm = PBScheduleFormViewModel()
    var scheduleStrips: [String: PBScheduleSessionStripViewModel] = [:]
    var settings = PBSettingsViewModel()
    var machineStatus = PBMachineStatusViewModel()
    var machineSettings = PBMachineSettingsViewModel()
    var usageView = PBCoreUsageViewModel()
    var quota = PBQuotaViewModel()
    var devices = PBDevicesViewModel()
    var clientDetail = PBClientDetailViewModel()
    var directDiagnostics = PBDirectDiagnosticsViewModel()
    var pendingComposerRestores: [RestoredComposer] = []
    var pendingCleanup: PendingCleanup?

    enum RenderSlot: Hashable, Sendable {
        case home
        case syncState
        case projectDetail
        case manageProjects
        case dirPicker(String)
        case newSession
        case schedules
        case scheduleDetail
        case scheduleForm
        case settings
        case machineStatus
        case machineSettings
        case quota
        case usageView
        case devices
        case clientDetail
        case directDiagnostics
        case session(String)
        case scheduleStrip(String)
    }

    @ObservationIgnored private var appliedRenderSeq: [RenderSlot: UInt64] = [:]

    /// Rejects stale snapshots that resumed after a newer CoreHost render already published.
    func acceptRender(_ slot: RenderSlot, seq: UInt64) -> Bool {
        if let prev = appliedRenderSeq[slot], seq < prev {
            return false
        }
        appliedRenderSeq[slot] = seq
        return true
    }

    func consumeComposerRestores(_ ids: Set<UUID>) {
        guard !ids.isEmpty else { return }
        pendingComposerRestores.removeAll { ids.contains($0.id) }
    }

    func consumeCleanup(_ id: UUID) {
        if pendingCleanup?.id == id {
            pendingCleanup = nil
        }
    }
}

protocol CoreHostExecutor: AnyObject, Sendable {
    var account: Account { get }
    var directSupported: Bool { get }
    var directServerHost: String { get }
    var directConfiguredStun: String { get }
    func installCoreHooks(_ hooks: Remote.CoreHooks) async
    func applyCoreSyncState(_ snapshot: PBCoreSyncStateView) async
    func setNetworkGeneration(_ gen: UInt64) async
    func ensureToken(opId: UInt64, forceLogin: Bool, timeoutMs: Int64) async -> PBCoreTokenResultEvent
    func discardToken(_ rejected: String) async
    func openStream(legId: UInt64, token: String, callbacks: Remote.StreamCallbacks) async
    func closeStream(legId: UInt64) async
    func executeCacheRead(_ effect: PBCoreCacheReadEffect) async -> PBCoreEvent
    func executeCacheWrite(_ effect: PBCoreCacheWriteEffect) async
    func executeCacheDelete(_ effect: PBCoreCacheDeleteEffect, onSessionRemoved: (@Sendable (String) async -> Void)?) async
    func executeRpc(_ effect: PBCoreRpcEffect) async -> PBCoreEvent
    func dispatchCommandAttempt(_ effect: PBCoreDispatchCommandEffect) async -> PBCoreCommandOutcomeEvent
    func setWatchedSessions(_ sessionIds: [String]) async
    func describeSelf(platform: String, description: String) async throws
    func cachedScheduleResultJson(_ machineId: String) async -> Data?
    func deleteScheduleCache(_ machineId: String) async
    func renameDevice(_ id: String, name: String) async throws
    func loadDirectPreferences() -> [String: Bool]
    func persistDirectPreference(_ machineId: String, enabled: Bool)
    func detachDirectStreams()
    var optionalSecurityExecutor: SecurityExecutor? { get }
    var optionalSecurityClient: SecurityClient? { get }
    func ensureSecurityBootstrapped() async throws
    func setSecurityForeground(_ foreground: Bool) async
    func applySecurityTokenUpdated(_ eff: PBSecurityTokenUpdatedEffect) async
    func applySecurityStatusSnapshot(_ status: PBSecurityWorkflowStatus) async
    @discardableResult func syncSecurity(touch: Bool) async throws -> PBSecurityWorkflowStatus
    func handleDirectSignalSend(_ eff: PBCoreDirectSignalSendEffect) -> Bool
    func handleDirectPcCreate(_ eff: PBCoreDirectPcCreateEffect)
    func handleDirectPcDescribe(_ eff: PBCoreDirectPcDescribeEffect) async
    func handleDirectPcSetRemote(_ eff: PBCoreDirectPcSetRemoteEffect) async
    func handleDirectPcAddCandidates(_ eff: PBCoreDirectPcAddCandidatesEffect) async
    func handleDirectPcConfirm(_ eff: PBCoreDirectPcConfirmEffect)
    func handleDirectPcClose(_ eff: PBCoreDirectPcCloseEffect)
    func handleDirectDcReadStats(_ eff: PBCoreDirectDcReadStatsEffect) async
    func close() async
}

extension CoreHostExecutor {
    var directSupported: Bool { false }
    var directServerHost: String { "" }
    var directConfiguredStun: String { "" }
    var optionalSecurityExecutor: SecurityExecutor? { nil }
    var optionalSecurityClient: SecurityClient? { nil }
    func ensureSecurityBootstrapped() async throws {}
    func setSecurityForeground(_ foreground: Bool) async {}
    func applySecurityTokenUpdated(_ eff: PBSecurityTokenUpdatedEffect) async {}
    func applySecurityStatusSnapshot(_ status: PBSecurityWorkflowStatus) async {}
    @discardableResult func syncSecurity(touch: Bool) async throws -> PBSecurityWorkflowStatus { PBSecurityWorkflowStatus() }
    func cachedScheduleResultJson(_ machineId: String) async -> Data? { nil }
    func deleteScheduleCache(_ machineId: String) async {}
    func loadDirectPreferences() -> [String: Bool] { [:] }
    func persistDirectPreference(_ machineId: String, enabled: Bool) {}
    func detachDirectStreams() {}
    func handleDirectSignalSend(_ eff: PBCoreDirectSignalSendEffect) -> Bool { false }
    func handleDirectPcCreate(_ eff: PBCoreDirectPcCreateEffect) {}
    func handleDirectPcDescribe(_ eff: PBCoreDirectPcDescribeEffect) async {}
    func handleDirectPcSetRemote(_ eff: PBCoreDirectPcSetRemoteEffect) async {}
    func handleDirectPcAddCandidates(_ eff: PBCoreDirectPcAddCandidatesEffect) async {}
    func handleDirectPcConfirm(_ eff: PBCoreDirectPcConfirmEffect) {}
    func handleDirectPcClose(_ eff: PBCoreDirectPcCloseEffect) {}
    func handleDirectDcReadStats(_ eff: PBCoreDirectDcReadStatsEffect) async {}
    func discardToken(_ rejected: String) async {}
    func setWatchedSessions(_ sessionIds: [String]) async {}
    func renameDevice(_ id: String, name: String) async throws {}
}

extension Remote: CoreHostExecutor {
    nonisolated var optionalSecurityExecutor: SecurityExecutor? { securityExecutor }
    nonisolated var optionalSecurityClient: SecurityClient? { securityClient }
}

private let monotonicOrigin = ContinuousClock.now
private func hostMonoMs() -> Int64 {
    let parts = monotonicOrigin.duration(to: .now).components
    return parts.seconds * 1000 + parts.attoseconds / 1_000_000_000_000_000
}

struct ScopeDefaults: @unchecked Sendable {
    let raw: UserDefaults
    let scope: [String]
    init(_ raw: UserDefaults = .standard, scope: [String] = []) {
        self.raw = raw
        self.scope = scope
    }
    func homeScopeToken() -> String { raw.string(forKey: AccountDefaults.key("homeScopeToken")) ?? "" }
    func setHomeScopeToken(_ token: String) { raw.set(token, forKey: AccountDefaults.key("homeScopeToken")) }

    private func scheduleSeenKey(_ machineID: String) -> String {
        let arr = scope + [machineID]
        let encoded = (try? JSONEncoder().encode(arr)).flatMap { String(data: $0, encoding: .utf8) } ?? "[]"
        return "ra-schedule-seen:" + encoded
    }

    func loadScheduleSeen(machineID: String) -> [PBScheduleSeenToken]? {
        guard let list = raw.stringArray(forKey: scheduleSeenKey(machineID)) else { return nil }
        return list.compactMap { entry in
            guard let data = entry.data(using: .utf8),
                  let pair = try? JSONDecoder().decode([String].self, from: data),
                  pair.count >= 2
            else { return nil }
            return PBScheduleSeenToken.with {
                $0.taskID = pair[0]
                $0.runID = pair[1]
            }
        }
    }

    func saveScheduleSeen(machineID: String, tokens: [PBScheduleSeenToken]) {
        let encoded = tokens.compactMap { tok -> String? in
            guard let data = try? JSONEncoder().encode([tok.taskID, tok.runID]) else { return nil }
            return String(data: data, encoding: .utf8)
        }
        raw.set(encoded, forKey: scheduleSeenKey(machineID))
    }

    func deleteScheduleSeen(machineID: String) {
        raw.removeObject(forKey: scheduleSeenKey(machineID))
    }

    private func usageCacheKey(machineID: String, preset: PBUsageRangePreset) -> String {
        let arr = scope + [machineID, String(preset.rawValue)]
        let encoded = (try? JSONEncoder().encode(arr)).flatMap { String(data: $0, encoding: .utf8) } ?? "[]"
        return AccountDefaults.key("usageCache:" + encoded)
    }

    func loadUsageCache(machineID: String) -> [PBUsageStatsResponse] {
        let presets: [PBUsageRangePreset] = [.today, .sevenDays, .thirtyDays]
        return presets.compactMap { preset in
            guard let data = raw.data(forKey: usageCacheKey(machineID: machineID, preset: preset)) else { return nil }
            return try? PBUsageStatsResponse(serializedBytes: data)
        }
    }

    func saveUsageCache(machineID: String, snapshot: PBUsageStatsResponse) {
        let preset = snapshot.preset
        guard preset == .today || preset == .sevenDays || preset == .thirtyDays,
              let data = try? snapshot.serializedData() else { return }
        raw.set(data, forKey: usageCacheKey(machineID: machineID, preset: preset))
    }
}

/// Process-wide single FIFO queue serializing all shared-file disk operations across
/// CoreHost lifetimes (e.g., logout ClearAll followed by a new account's ScanLocal).
final class SharedFileStorageQueue: Sendable {
    static let shared = SharedFileStorageQueue()

    private let continuation: AsyncStream<@Sendable () async -> Void>.Continuation

    init() {
        let (stream, continuation) = AsyncStream.makeStream(
            of: (@Sendable () async -> Void).self,
            bufferingPolicy: .unbounded
        )
        self.continuation = continuation
        Task.detached(priority: .utility) {
            for await op in stream {
                await op()
            }
        }
    }

    func enqueue(_ op: @escaping @Sendable () async -> Void) {
        continuation.yield(op)
    }

    func drain() async {
        await withCheckedContinuation { (c: CheckedContinuation<Void, Never>) in
            let yielded = continuation.yield { c.resume() }
            if case .terminated = yielded {
                c.resume()
            }
        }
    }
}

/// Process-wide single FIFO queue serializing all draft/outbox storage and local-ref
/// release operations across CoreHost lifetimes (e.g., logout ClearAll followed by a
/// new account's startup DraftStorageList).
final class DraftStorageQueue: Sendable {
    static let shared = DraftStorageQueue()

    private let continuation: AsyncStream<@Sendable () async -> Void>.Continuation

    init() {
        let (stream, continuation) = AsyncStream.makeStream(
            of: (@Sendable () async -> Void).self,
            bufferingPolicy: .unbounded
        )
        self.continuation = continuation
        Task.detached(priority: .utility) {
            for await op in stream {
                await op()
            }
        }
    }

    func enqueue(_ op: @escaping @Sendable () async -> Void) {
        continuation.yield(op)
    }

    func drain() async {
        await withCheckedContinuation { (c: CheckedContinuation<Void, Never>) in
            let yielded = continuation.yield { c.resume() }
            if case .terminated = yielded {
                c.resume()
            }
        }
    }
}

/// Owns `CoreClient`, feeds system/UI/transport events into Core, and executes every
/// `Effect` emitted by Core (`OpenStream`, `EnsureToken`, `SyncSecurity`, `ScheduleTimer`,
/// `CacheRead/Write/Delete`, `Rpc`, `DispatchCommand`, `Render`, `Navigate`,
/// `SaveHomeScope`, `RequestEntropy`, and draft/attachment storage & I/O Effects).
actor CoreHost: SyncEngine {
    private struct TimerKey: Hashable {
        let kind: PBCoreTimerKind
        let key: String
    }

    private struct ScheduledTimer {
        let id: UInt64
        let task: Task<Void, Never>
    }

    struct SharedFileHandlers: Sendable {
        var onCompleted: @MainActor @Sendable (PBSharedFileCompletedEffect, Bool) -> Void = { _, _ in }
        var onClearThumbnail: @MainActor @Sendable (SharedFileKey) -> Void = { _ in }
        var onClearSessionThumbnails: @MainActor @Sendable (String) -> Void = { _ in }
        var onClearAllThumbnails: @MainActor @Sendable () -> Void = {}
    }

    struct AttachmentHandlers: Sendable {
        var onReleaseLocalRefs: @MainActor @Sendable ([String]) -> Void = { _ in }
        var onClearAllLocal: @MainActor @Sendable () -> Void = {}
    }

    private let client: CoreClient
    private let executor: any CoreHostExecutor
    private let screens: CoreScreens
    private let defaults: ScopeDefaults
    private let scopeHmacKey: Data
    private let sharedFileStore: SharedFileStore
    private let sharedFileQueue: SharedFileStorageQueue
    private var sharedFileHandlers: SharedFileHandlers?
    private let pendingStore: PendingScope
    private let draftStorageQueue: DraftStorageQueue
    private var attachmentHandlers: AttachmentHandlers?
    private var attachmentConfigured = false
    private var lastDraftClearError: (any Error)?
    private var visibleSessions: [(token: UUID, sessionID: String)] = []
    private var currentVisibleSessionID = ""
    private let clock: @Sendable () -> Int64
    private let monoClock: @Sendable () -> Int64
    private let sleep: Sleeper
    private var demoDirectories: DirectorySource?
    private var demoModels: [String: PBListModelsResponse]
    private var demoWorld = DemoWorld()
    private var demoEvents: [String: [PBEvent]] = [:]
    private var demoSchedules: [String: Data] = [:]
    private var demoScheduleSeen: [String: [PBScheduleSeenToken]] = [:]
    private var demoLegID: UInt64 = 0
    private var onNavigate: @MainActor @Sendable (PBCoreNavigateEffect) -> Void
    private var onScheduleNavigate: @MainActor @Sendable (PBScheduleNavigateEffect) -> Void
    private var onSessionRemoved: (@Sendable (String) async -> Void)?

    private var closed = false
    private var demoMode: Bool
    private var started = false
    private var foregrounded = false
    private var networkGen: UInt64 = 0
    private var lastNetworkAvailable = true
    private var lastNetworkSignature = ""

    private struct SessionWatchEntry {
        let token: UUID
        let sessionID: String
        var window: PBSessionWindowRequest
    }

    private struct MachineStatusWatchEntry {
        let token: UUID
        let machineID: String
        let mode: PBMachinePollMode
    }

    private var activeProjectKey = ""
    private var activeNewSessionProjectKey = ""
    private var activeManageMachineID = ""
    private var manageSubscribers = 0
    private var activeMachineStatusID = ""
    private var activeMachineSettingsID = ""
    private var activeQuotaMachineID = ""
    private var activeQuotaFilterAgent: PBAgent = .unspecified
    private var activeClientDetailID = ""
    private var usageMachineID = ""
    private var usageFocusAgent: PBAgent = .unspecified
    private var usageFocusModel = ""
    private var usagePreset: PBUsageRangePreset = .sevenDays
    private var usageCustomStartMs: Int64 = 0
    private var usageCustomEndMs: Int64 = 0
    private var usageHydratedMachines: Set<String> = []
    private var sessionWatches: [UUID: SessionWatchEntry] = [:]
    private var sessionWatchOrder: [UUID] = []
    private var activeScheduleDetail: (machineID: String, taskID: String)?
    private var activeScheduleFormKey = ""
    private var scheduleStripWatches: [UUID: String] = [:]
    private var activeDirPickers: [UUID: String] = [:]
    private var machineStatusWatches: [UUID: MachineStatusWatchEntry] = [:]
    private var activeDirectDiagnosticsID = ""
    private var hooksInstalled = false
    private var nextDirectCallID: UInt64 = 1
    private var bulkRouteWaiters: [UInt64: CheckedContinuation<PBCoreDirectBulkRouteEffect, Never>] = [:]
    private var nextSecurityOpID: UInt64 = 1
    private var securityOpWaiters: [UInt64: CheckedContinuation<PBSecurityOpResultEffect, Never>] = [:]
    private var retryNowWaiters: [String: [CheckedContinuation<Bool, Never>]] = [:]
    private var terminalReopenListeners: [String: (token: UUID, handler: @MainActor @Sendable () -> Void)] = [:]
    private var terminalHistoryRequestListeners: [String: (token: UUID, handler: @MainActor @Sendable (PBCoreTerminalHistorySendRequestEffect) -> Void)] = [:]
    private var terminalHistoryUpdatedListeners: [String: (token: UUID, handler: @MainActor @Sendable (PBCoreTerminalHistoryUpdatedEffect) -> Void)] = [:]
    private var onPersistClientPrefs: (@MainActor @Sendable (PBPersistClientPrefsEffect) -> Void)?
    private var deviceDescription: @MainActor @Sendable () -> String = { "" }
    private var describedKey: String?
    #if DEBUG
    enum DemoCommandAction: Sendable {
        case hold
        case respond(PBCoreCommandOutcomeEvent)
        case asyncRespond(@Sendable () async -> PBCoreCommandOutcomeEvent)
    }
    private var demoCommandResponder: (@Sendable (PBCoreDispatchCommandEffect, PBCommand) -> DemoCommandAction?)?
    private var demoDaemonSettings: PBDaemonSettings?
    private var demoMachineStatusCalls = 0
    #endif

    private var randomIdPool: [String] = []
    private var unitFloatPool: [Double] = []
    private var timers: [TimerKey: ScheduledTimer] = [:]
    private var machinePollTimers: [String: ScheduledTimer] = [:]
    private var ioTasks: [UUID: Task<Void, Never>] = [:]
    private var cacheChain: Task<Void, Never>?
    private var clockTask: Task<Void, Never>?
    private var renderSeq: UInt64 = 0
    private var lastAppliedSyncStateSeq: UInt64 = 0

    private func nextRenderSeq() -> UInt64 {
        renderSeq &+= 1
        return renderSeq
    }

    nonisolated var account: Account { executor.account }

    static func live(
        remote: Remote,
        account: Account,
        defaults: UserDefaults,
        screens: CoreScreens,
        sharedFileStore: SharedFileStore = SharedFileStore(root: SharedFileCenter.defaultRoot),
        sharedFileQueue: SharedFileStorageQueue = .shared,
        pendingStore: PendingScope = PendingStore.ephemeral().scoped(),
        draftStorageQueue: DraftStorageQueue = .shared,
        demoDirectories: DirectorySource? = nil,
        demoModels: [String: PBListModelsResponse] = [:],
        demoMode: Bool = false,
        clock: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) },
        sleep: @escaping Sleeper = realSleep,
        onNavigate: @escaping @MainActor @Sendable (PBCoreNavigateEffect) -> Void,
        onScheduleNavigate: @escaping @MainActor @Sendable (PBScheduleNavigateEffect) -> Void = { _ in }
    ) throws -> CoreHost {
        let client = try CoreClient()
        let host = CoreHost(
            client: client,
            executor: remote,
            screens: screens,
            defaults: ScopeDefaults(defaults, scope: [account.server, account.deviceId]),
            scopeHmacKey: RACore.scopeHmacKey(localKey: account.localKey),
            sharedFileStore: sharedFileStore,
            sharedFileQueue: sharedFileQueue,
            pendingStore: pendingStore,
            draftStorageQueue: draftStorageQueue,
            clock: clock,
            monoClock: hostMonoMs,
            sleep: sleep,
            demoDirectories: demoDirectories,
            demoModels: demoModels,
            demoMode: demoMode,
            onNavigate: onNavigate,
            onScheduleNavigate: onScheduleNavigate
        )
        Task {
            await host.wireExecutorHooks()
        }
        return host
    }

    init(
        client: CoreClient,
        executor: any CoreHostExecutor,
        screens: CoreScreens,
        defaults: ScopeDefaults = ScopeDefaults(),
        scopeHmacKey: Data = Data(),
        sharedFileStore: SharedFileStore = SharedFileStore(root: SharedFileCenter.defaultRoot),
        sharedFileQueue: SharedFileStorageQueue = .shared,
        pendingStore: PendingScope = PendingStore.ephemeral().scoped(),
        draftStorageQueue: DraftStorageQueue = .shared,
        clock: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) },
        monoClock: @escaping @Sendable () -> Int64 = hostMonoMs,
        sleep: @escaping Sleeper = realSleep,
        demoDirectories: DirectorySource? = nil,
        demoModels: [String: PBListModelsResponse] = [:],
        demoMode: Bool = false,
        onNavigate: @escaping @MainActor @Sendable (PBCoreNavigateEffect) -> Void = { _ in },
        onScheduleNavigate: @escaping @MainActor @Sendable (PBScheduleNavigateEffect) -> Void = { _ in }
    ) {
        self.client = client
        self.executor = executor
        self.screens = screens
        self.defaults = defaults
        self.scopeHmacKey = scopeHmacKey
        self.sharedFileStore = sharedFileStore
        self.sharedFileQueue = sharedFileQueue
        self.pendingStore = pendingStore
        self.draftStorageQueue = draftStorageQueue
        self.clock = clock
        self.monoClock = monoClock
        self.sleep = sleep
        self.demoDirectories = demoDirectories
        self.demoModels = demoModels
        self.demoMode = demoMode
        self.onNavigate = onNavigate
        self.onScheduleNavigate = onScheduleNavigate
        self.randomIdPool = (0..<16).map { _ in newRequestId() }
        self.unitFloatPool = (0..<16).map { _ in Double.random(in: 0..<1) }
    }

    func wireExecutorHooks() async {
        guard !hooksInstalled else { return }
        hooksInstalled = true
        await executor.installCoreHooks(Remote.CoreHooks(
            onRefresh: { [weak self] in
                await self?.reconnectNowAsync()
            },
            onRefreshDevices: { [weak self] in
                _ = try? await self?.executor.syncSecurity(touch: false)
            },
            onQuerySyncState: { [weak self] in
                await self?.querySyncStateView()
            },
            onSecurityOp: { [weak self] req in
                await self?.runSecurityOp(req) ?? .with {
                    $0.opID = req.opID
                    $0.error = "host closed"
                    $0.structuredErrorCode = .storage
                }
            },
            onCoreEvent: { [weak self] ev in
                await self?.dispatch(ev)
            },
            onBeginDirectBulkTransfer: { [weak self] ev in
                await self?.beginDirectBulkTransfer(ev) ?? PBCoreDirectBulkRouteEffect()
            },
            onFinishDirectBulkTransfer: { [weak self] ev in
                await self?.finishDirectBulkTransfer(ev)
            }
        ))
        await syncWatchedSessionsToExecutor()
    }

    // MARK: - SyncEngine Conformance

    nonisolated func start() {
        Task { await self.startAsync() }
    }

    nonisolated func stop() {
        Task { await self.stopAsync() }
    }

    nonisolated func setNetwork(_ available: Bool) {
        Task { await self.handleNetworkChange(signature: lastNetworkSignatureFor(available), available: available) }
    }

    nonisolated func networkChanged(_ signature: String, available: Bool) {
        Task { await self.handleNetworkChange(signature: signature, available: available) }
    }

    nonisolated func reconnectNow() {
        Task { await self.reconnectNowAsync() }
    }

    nonisolated func retryLogin() {
        Task { await self.retryLoginAsync() }
    }

    nonisolated func close() {
        Task { await self.closeAsync() }
    }

    func setDeviceDescription(_ provider: @escaping @MainActor @Sendable () -> String) {
        deviceDescription = provider
    }

    /// Says what this client is, sealed, so other clients can tell it apart.
    func maybeDescribeSelf(_ v: PBCoreSyncStateView) async {
        guard v.connection == .connected else {
            // A try made on the connection that is gone may not have arrived: the next one tries again.
            describedKey = nil
            return
        }
        guard !demoMode,
              let me = v.devices.first(where: { $0.deviceID == account.deviceId && $0.trusted }) else { return }
        let description = await deviceDescription()
        guard !description.isEmpty else { return }
        guard !me.hasMeta || me.meta.platform != "ios" || me.meta.description_p != description else { return }
        // One try per revision and connection: a failure waits for the next change instead of spinning.
        let key = "\(me.metaRev):\(v.leaseEndMs)"
        guard describedKey != key else { return }
        describedKey = key
        let exec = executor
        Task {
            try? await exec.describeSelf(platform: "ios", description: description)
        }
    }

    private nonisolated func lastNetworkSignatureFor(_ available: Bool) -> String {
        available ? "up" : "down"
    }

    // MARK: - Lifecycle Actions

    func startAsync() async {
        guard !closed else { return }
        await wireExecutorHooks()
        started = true
        foregrounded = true
        startClockTicker()
        replenishEntropy(minIds: 16, minFloats: 16)
        let initIds = randomIdPool
        let initFloats = unitFloatPool
        randomIdPool.removeAll(keepingCapacity: true)
        unitFloatPool.removeAll(keepingCapacity: true)
        await dispatch {
            $0.entropyProvided = .with {
                $0.randomIds = initIds
                $0.unitFloats = initFloats
                $0.randomBytes = Self.randomBytes(4096)
            }
        }
        if !demoMode {
            let acct = executor.account
            let supported = executor.directSupported
            let serverHost = executor.directServerHost
            let configuredStun = executor.directConfiguredStun
            await dispatch {
                $0.transportConfig = .with {
                    $0.supported = supported
                    $0.account = acct.accountId
                    $0.protocolVersion = UInt32(peerProtocolVersion)
                    $0.serverHost = serverHost
                    $0.configuredStun = configuredStun
                    $0.platformAgent = "ios"
                }
            }
            let prefs = executor.loadDirectPreferences()
            if !prefs.isEmpty {
                await dispatch {
                    $0.directPreferencesLoaded = .with {
                        $0.entries = prefs.map { key, value in
                            PBCoreDirectPreferenceEntry.with {
                                $0.machineID = key
                                $0.enabled = value
                            }
                        }
                    }
                }
            }
            if executor.optionalSecurityExecutor != nil {
                await dispatch {
                    $0.securityConfig = .with {
                        $0.account = acct.accountId
                        $0.deviceID = acct.deviceId
                        $0.server = acct.server
                        $0.inlineCrypto = true
                        $0.foreground = self.foregrounded
                    }
                }
                try? await executor.ensureSecurityBootstrapped()
            }
        }
        let savedScope = defaults.homeScopeToken()
        await dispatch { [scopeHmacKey] ev in
            ev.syncLifecycle = .with {
                $0.kind = .start
                $0.homeScopeToken = savedScope
                $0.scopeHmacKey = scopeHmacKey
            }
        }
        if !demoMode {
            await configureAttachments(persistDrafts: true, persistOutbox: true, scanStorage: true)
            await dispatch {
                $0.sharedFileConfig = .with {
                    $0.localCacheMaxBytes = 0
                    $0.storageFullKind = .phoneStorageFull
                    $0.scanLocal = true
                }
            }
        }
        await setUILocale(MessageLocale.current)
        await dispatchClockUpdated()
    }

    /// Sends `AttachmentConfigEvent` once per host lifetime before any `Draft*`/`Attachment*` event.
    func configureAttachments(
        persistDrafts: Bool? = nil,
        persistOutbox: Bool? = nil,
        scanStorage: Bool? = nil
    ) async {
        guard !closed, !attachmentConfigured else { return }
        attachmentConfigured = true
        let pd = persistDrafts ?? !demoMode
        let po = persistOutbox ?? !demoMode
        let scan = scanStorage ?? !demoMode
        await dispatch {
            $0.attachmentConfig = .with {
                $0.persistDrafts = pd
                $0.persistOutbox = po
                $0.scanStorage = scan
                $0.maxChunkBytes = 0
                $0.hasPersistedPreparedImages_p = true
            }
        }
    }

    func setUILocale(_ uiLocale: String) async {
        guard !closed else { return }
        await dispatch {
            $0.localeChanged = .with { $0.uiLocale = uiLocale }
        }
    }

    func stopAsync() async {
        guard !closed else { return }
        started = false
        foregrounded = false
        clockTask?.cancel()
        clockTask = nil
        cancelAllTimers()
        cancelAllMachinePollTimers()
        cancelIOTasks()
        executor.detachDirectStreams()
        await executor.setSecurityForeground(false)
        await dispatch { $0.securityForeground = .with { $0.foreground = false } }
        await dispatch { $0.syncLifecycle.kind = .stop }
    }

    func setForeground(_ foreground: Bool) async {
        guard !closed, started else { return }
        foregrounded = foreground
        if foreground {
            await dispatchClockUpdated()
        }
        await executor.setSecurityForeground(foreground)
        await dispatch { $0.securityForeground = .with { $0.foreground = foreground } }
        await dispatch { $0.syncLifecycle.kind = foreground ? .foreground : .background }
    }

    func reconnectNowAsync() async {
        guard !closed, started else { return }
        await dispatch {
            $0.syncLifecycle.kind = .reconnectNow
        }
    }

    func retryLoginAsync() async {
        guard !closed, started else { return }
        await dispatch {
            $0.syncLifecycle.kind = .retryLogin
        }
    }

    private func runSecurityOp(_ req: PBSecurityOpRequestEvent) async -> PBSecurityOpResultEffect {
        guard !closed, !demoMode else {
            return .with {
                $0.opID = req.opID
                $0.error = "security host unavailable"
                $0.structuredErrorCode = .storage
            }
        }
        let opID = nextSecurityOpID
        nextSecurityOpID &+= 1
        var outgoing = req
        outgoing.opID = opID
        let captured = outgoing
        return await withCheckedContinuation { continuation in
            securityOpWaiters[opID] = continuation
            Task { await self.sendSecurityOp(opID: opID, request: captured) }
        }
    }

    private func sendSecurityOp(opID: UInt64, request: PBSecurityOpRequestEvent) async {
        guard !closed, !demoMode else {
            securityOpWaiters.removeValue(forKey: opID)?.resume(returning: .with {
                $0.opID = opID
                $0.error = "host closed"
                $0.structuredErrorCode = .storage
            })
            return
        }
        await dispatch { $0.securityOpRequest = request }
    }

    private func querySyncStateView() -> PBCoreSyncStateView? {
        guard !closed else { return nil }
        return try? client.view(PBCoreScreen.with {
            $0.syncState = PBCoreSyncStateScreenRequest()
        }).syncState
    }

    private func handleNetworkChange(signature: String, available: Bool) async {
        guard !closed else { return }
        let changed = available != lastNetworkAvailable || signature != lastNetworkSignature
        lastNetworkAvailable = available
        lastNetworkSignature = signature
        if changed {
            networkGen &+= 1
            await executor.setNetworkGeneration(networkGen)
        }
        await dispatch {
            $0.networkChanged = .with {
                $0.available = available
                $0.signature = signature
            }
        }
    }

    func closeAsync() async {
        guard !closed else { return }
        closed = true
        started = false
        clockTask?.cancel()
        clockTask = nil
        cancelAllTimers()
        cancelAllMachinePollTimers()
        cancelIOTasks()
        cacheChain?.cancel()
        cacheChain = nil
        sharedFileHandlers = nil
        attachmentHandlers = nil
        visibleSessions.removeAll()
        currentVisibleSessionID = ""
        let pendingBulk = bulkRouteWaiters
        bulkRouteWaiters.removeAll()
        for continuation in pendingBulk.values {
            continuation.resume(returning: PBCoreDirectBulkRouteEffect())
        }
        let pendingOps = securityOpWaiters
        securityOpWaiters.removeAll()
        for (opID, continuation) in pendingOps {
            continuation.resume(returning: .with {
                $0.opID = opID
                $0.error = "host closed"
                $0.structuredErrorCode = .storage
            })
        }
        let pendingRetry = retryNowWaiters
        retryNowWaiters.removeAll()
        for list in pendingRetry.values {
            for continuation in list {
                continuation.resume(returning: false)
            }
        }
        terminalReopenListeners.removeAll()
        terminalHistoryRequestListeners.removeAll()
        terminalHistoryUpdatedListeners.removeAll()
        executor.detachDirectStreams()
        await executor.close()
        client.close()
    }

    func openDirectDiagnostics(machineID: String) async {
        guard !closed, !machineID.isEmpty else { return }
        activeDirectDiagnosticsID = machineID
        if !demoMode {
            await dispatch {
                $0.directRefreshDiagnostics = .with { $0.machineID = machineID }
            }
        }
        await renderTargets([.directDiagnostics])
    }

    func closeDirectDiagnostics() async {
        activeDirectDiagnosticsID = ""
        let seq = nextRenderSeq()
        await MainActor.run { [screens, seq] in
            if screens.acceptRender(.directDiagnostics, seq: seq) {
                screens.directDiagnostics = PBDirectDiagnosticsViewModel()
            }
        }
    }

    func refreshDirectDiagnostics() async {
        guard !closed, !activeDirectDiagnosticsID.isEmpty else { return }
        let machineID = activeDirectDiagnosticsID
        if !demoMode {
            await dispatch {
                $0.directRefreshDiagnostics = .with { $0.machineID = machineID }
            }
        }
        await renderTargets([.directDiagnostics])
    }

    @discardableResult
    func retryDirectNow(machineID: String) async -> Bool {
        guard !closed, !demoMode, !machineID.isEmpty else { return false }
        return await withCheckedContinuation { continuation in
            retryNowWaiters[machineID, default: []].append(continuation)
            Task { await self.sendDirectRetryNow(machineID: machineID) }
        }
    }

    private func sendDirectRetryNow(machineID: String) async {
        guard !closed, !demoMode else {
            resumeRetryNowWaiters(machineID: machineID, started: false)
            return
        }
        await dispatch {
            $0.directRetryNow = .with { $0.machineID = machineID }
        }
    }

    private func resumeRetryNowWaiters(machineID: String, started: Bool) {
        guard let list = retryNowWaiters.removeValue(forKey: machineID) else { return }
        for continuation in list {
            continuation.resume(returning: started)
        }
    }

    func beginDirectBulkTransfer(_ ev: PBCoreDirectBulkTransferBeginEvent) async -> PBCoreDirectBulkRouteEffect {
        guard !closed, !demoMode else { return PBCoreDirectBulkRouteEffect() }
        let callID = nextDirectCallID
        nextDirectCallID &+= 1
        var req = ev
        req.callID = callID
        let capturedReq = req
        return await withCheckedContinuation { continuation in
            bulkRouteWaiters[callID] = continuation
            Task { await self.sendBeginDirectBulkTransfer(callID: callID, event: capturedReq) }
        }
    }

    private func sendBeginDirectBulkTransfer(callID: UInt64, event: PBCoreDirectBulkTransferBeginEvent) async {
        guard !closed, !demoMode else {
            bulkRouteWaiters.removeValue(forKey: callID)?.resume(returning: PBCoreDirectBulkRouteEffect())
            return
        }
        await dispatch {
            $0.directBulkTransferBegin = event
        }
    }

    func finishDirectBulkTransfer(_ ev: PBCoreDirectBulkTransferDoneEvent) async {
        guard !closed, !demoMode else { return }
        await dispatch {
            $0.directBulkTransferDone = ev
        }
    }

    func sendTerminalStream(terminalID: String, kind: PBCoreTerminalStreamEventKind, opened: Bool) async {
        guard !closed, !demoMode, !terminalID.isEmpty else { return }
        await dispatch {
            $0.terminalStream = .with {
                $0.terminalID = terminalID
                $0.kind = kind
                $0.opened = opened
            }
        }
    }

    func registerTerminalReopen(
        terminalID: String,
        token: UUID = UUID(),
        handler: @escaping @MainActor @Sendable () -> Void
    ) {
        guard !closed, !terminalID.isEmpty else { return }
        terminalReopenListeners[terminalID] = (token: token, handler: handler)
    }

    func unregisterTerminalReopen(terminalID: String, token: UUID? = nil) {
        guard !terminalID.isEmpty else { return }
        if let token, let existing = terminalReopenListeners[terminalID], existing.token != token {
            return
        }
        terminalReopenListeners.removeValue(forKey: terminalID)
    }

    func sendTerminalHistory(
        terminalID: String,
        kind: PBCoreTerminalHistoryEventKind,
        page: PBTerminalHistoryPage? = nil
    ) async {
        guard !closed, !terminalID.isEmpty else { return }
        await dispatch {
            $0.terminalHistory = .with {
                $0.terminalID = terminalID
                $0.kind = kind
                if let page { $0.page = page }
            }
        }
    }

    func registerTerminalHistory(
        terminalID: String,
        token: UUID = UUID(),
        onRequest: @escaping @MainActor @Sendable (PBCoreTerminalHistorySendRequestEffect) -> Void,
        onUpdated: @escaping @MainActor @Sendable (PBCoreTerminalHistoryUpdatedEffect) -> Void
    ) {
        guard !closed, !terminalID.isEmpty else { return }
        terminalHistoryRequestListeners[terminalID] = (token: token, handler: onRequest)
        terminalHistoryUpdatedListeners[terminalID] = (token: token, handler: onUpdated)
    }

    func unregisterTerminalHistory(terminalID: String, token: UUID? = nil) {
        guard !terminalID.isEmpty else { return }
        if let token {
            if let existing = terminalHistoryRequestListeners[terminalID], existing.token == token {
                terminalHistoryRequestListeners.removeValue(forKey: terminalID)
            }
            if let existing = terminalHistoryUpdatedListeners[terminalID], existing.token == token {
                terminalHistoryUpdatedListeners.removeValue(forKey: terminalID)
            }
            return
        }
        terminalHistoryRequestListeners.removeValue(forKey: terminalID)
        terminalHistoryUpdatedListeners.removeValue(forKey: terminalID)
    }

    // MARK: - Screen & Subscription Events

    func setNavigateHandler(_ handler: @escaping @MainActor @Sendable (PBCoreNavigateEffect) -> Void) {
        onNavigate = handler
    }

    func setScheduleNavigateHandler(_ handler: @escaping @MainActor @Sendable (PBScheduleNavigateEffect) -> Void) {
        onScheduleNavigate = handler
    }

    func setSessionRemovedHandler(_ handler: @escaping @Sendable (String) async -> Void) {
        onSessionRemoved = handler
    }

    func showHome() async {
        guard !closed else { return }
        await dispatchClockUpdated()
        await renderTargets([.home, .syncState])
    }

    func showProject(key: String) async {
        guard !closed else { return }
        activeProjectKey = key
        await renderTargets([.project, .newSession])
    }

    func showNewSession(projectKey: String) async {
        guard !closed else { return }
        activeNewSessionProjectKey = projectKey
        await dispatch {
            $0.newSessionOpen.projectKey = projectKey
        }
        await renderTargets([.newSession])
    }

    func setPersistClientPrefsHandler(_ handler: @escaping @MainActor @Sendable (PBPersistClientPrefsEffect) -> Void) {
        onPersistClientPrefs = handler
    }

    func reportAppearancePref(_ theme: PBThemePreference) async {
        guard !closed else { return }
        await dispatch {
            $0.setAppearancePref.theme = theme
        }
    }

    func reportNotificationPref(supported: Bool) async {
        guard !closed else { return }
        await dispatch {
            $0.setNotificationPref.supported = supported
        }
    }

    func openSettings() async {
        guard !closed else { return }
        await renderTargets([.settings])
    }

    func openDevices() async {
        guard !closed else { return }
        await renderTargets([.devices])
    }

    func openClientDetail(deviceID: String) async {
        guard !closed, !deviceID.isEmpty else { return }
        activeClientDetailID = deviceID
        await renderTargets([.clientDetail])
    }

    func openQuota(machineID: String, filterAgent: PBAgent = .unspecified) async {
        guard !closed, !machineID.isEmpty else { return }
        activeQuotaMachineID = machineID
        activeQuotaFilterAgent = filterAgent
        await renderTargets([.quota])
    }

    func openMachineSettings(machineID: String) async {
        guard !closed, !machineID.isEmpty else { return }
        activeMachineSettingsID = machineID
        await hydrateUsageCache(machineID: machineID)
        await dispatch {
            $0.machineSettingsOpen.machineID = machineID
        }
        await renderTargets([.machineSettings])
    }

    static let usageRetentionDays = 90

    struct UsageOpenParams: Equatable, Sendable {
        let preset: PBUsageRangePreset
        let customStartMs: Int64
        let customEndMs: Int64
        let timezone: String
        let tzOffsetMinutes: Int32
        let bucketBoundsMs: [Int64]
        let prevStartMs: Int64
    }

    nonisolated static func clampUsageCustomRange(
        startMs: Int64,
        endMs: Int64,
        nowMs: Int64 = Int64(Date().timeIntervalSince1970 * 1000)
    ) -> (startMs: Int64, endMs: Int64) {
        let cal = Calendar.current
        let nowDate = Date(timeIntervalSince1970: Double(nowMs) / 1000.0)
        let todayStartDate = cal.startOfDay(for: nowDate)
        let minDayDate = cal.date(byAdding: .day, value: -(usageRetentionDays - 1), to: todayStartDate) ?? todayStartDate
        let defaultStartDate = cal.date(byAdding: .day, value: -6, to: todayStartDate) ?? todayStartDate
        let rawStartDate = startMs > 0 ? cal.startOfDay(for: Date(timeIntervalSince1970: Double(startMs) / 1000.0)) : defaultStartDate
        let rawEndDate = endMs > 0 ? cal.startOfDay(for: Date(timeIntervalSince1970: Double(endMs) / 1000.0)) : todayStartDate
        var sDay = rawStartDate
        var eDay = rawEndDate < sDay ? sDay : rawEndDate
        if sDay < minDayDate { sDay = minDayDate }
        if eDay > todayStartDate { eDay = todayStartDate }
        if eDay < sDay { sDay = eDay }
        return (
            Int64(sDay.timeIntervalSince1970 * 1000),
            Int64(eDay.timeIntervalSince1970 * 1000)
        )
    }

    nonisolated static func buildUsageOpenParams(
        preset: PBUsageRangePreset,
        customStartMs: Int64 = 0,
        customEndMs: Int64 = 0,
        nowMs: Int64 = Int64(Date().timeIntervalSince1970 * 1000)
    ) -> UsageOpenParams {
        let cal = Calendar.current
        let tz = TimeZone.current
        let effectivePreset = preset == .unspecified ? .sevenDays : preset
        let nowDate = Date(timeIntervalSince1970: Double(nowMs) / 1000.0)
        let todayStartDate = cal.startOfDay(for: nowDate)
        let tomorrowStartDate = cal.date(byAdding: .day, value: 1, to: todayStartDate) ?? todayStartDate.addingTimeInterval(86_400)

        let startDayDate: Date
        let endExclDate: Date
        var normCustomStart: Int64 = 0
        var normCustomEnd: Int64 = 0

        switch effectivePreset {
        case .today:
            startDayDate = todayStartDate
            endExclDate = tomorrowStartDate
        case .thirtyDays:
            startDayDate = cal.date(byAdding: .day, value: -29, to: todayStartDate) ?? todayStartDate
            endExclDate = tomorrowStartDate
        case .custom:
            let clamped = clampUsageCustomRange(startMs: customStartMs, endMs: customEndMs, nowMs: nowMs)
            normCustomStart = clamped.startMs
            normCustomEnd = clamped.endMs
            startDayDate = cal.startOfDay(for: Date(timeIntervalSince1970: Double(clamped.startMs) / 1000.0))
            let endIncDate = cal.startOfDay(for: Date(timeIntervalSince1970: Double(clamped.endMs) / 1000.0))
            endExclDate = cal.date(byAdding: .day, value: 1, to: endIncDate) ?? endIncDate.addingTimeInterval(86_400)
        default:
            startDayDate = cal.date(byAdding: .day, value: -6, to: todayStartDate) ?? todayStartDate
            endExclDate = tomorrowStartDate
        }

        var numDays = 0
        var cursor = startDayDate
        while cursor < endExclDate && numDays <= usageRetentionDays + 2 {
            numDays += 1
            guard let next = cal.date(byAdding: .day, value: 1, to: cursor), next > cursor else { break }
            cursor = next
        }

        var bounds: [Int64] = []
        if numDays <= 1 {
            let startMs = Int64(startDayDate.timeIntervalSince1970 * 1000)
            let endMs = Int64(endExclDate.timeIntervalSince1970 * 1000)
            var cur = startMs
            while cur < endMs {
                bounds.append(cur)
                cur += 3_600_000
            }
            bounds.append(endMs)
        } else if numDays <= 7 {
            var day = startDayDate
            while day < endExclDate {
                guard let nextDay = cal.date(byAdding: .day, value: 1, to: day), nextDay > day else { break }
                let dayMs = day.timeIntervalSince1970 * 1000.0
                let spanMs = nextDay.timeIntervalSince1970 * 1000.0 - dayMs
                for q in 0..<4 {
                    bounds.append(Int64((dayMs + (spanMs * Double(q)) / 4.0).rounded()))
                }
                day = nextDay
            }
            bounds.append(Int64(endExclDate.timeIntervalSince1970 * 1000))
        } else {
            var day = startDayDate
            while day < endExclDate {
                bounds.append(Int64(day.timeIntervalSince1970 * 1000))
                guard let nextDay = cal.date(byAdding: .day, value: 1, to: day), nextDay > day else { break }
                day = nextDay
            }
            bounds.append(Int64(endExclDate.timeIntervalSince1970 * 1000))
        }

        let prevStartDate = cal.date(byAdding: .day, value: -max(1, numDays), to: startDayDate) ?? startDayDate
        return UsageOpenParams(
            preset: effectivePreset,
            customStartMs: normCustomStart,
            customEndMs: normCustomEnd,
            timezone: tz.identifier,
            tzOffsetMinutes: Int32(tz.secondsFromGMT(for: nowDate) / 60),
            bucketBoundsMs: bounds,
            prevStartMs: Int64(prevStartDate.timeIntervalSince1970 * 1000)
        )
    }

    private func hydrateUsageCache(machineID: String) async {
        guard !machineID.isEmpty, usageHydratedMachines.insert(machineID).inserted else { return }
        let snapshots = defaults.loadUsageCache(machineID: machineID)
        for snap in snapshots {
            await dispatch {
                $0.usageCacheLoaded = .with {
                    $0.machineID = machineID
                    $0.snapshot = snap
                }
            }
        }
    }

    func openUsageScreen(
        machineID: String,
        focusAgent: PBAgent = .unspecified,
        focusModel: String = "",
        preset: PBUsageRangePreset? = nil,
        customStartMs: Int64? = nil,
        customEndMs: Int64? = nil
    ) async {
        guard !closed, !machineID.isEmpty else { return }
        let switchingMachine = usageMachineID != machineID
        usageMachineID = machineID
        usageFocusAgent = focusAgent
        usageFocusModel = focusModel
        if let preset {
            usagePreset = preset == .unspecified ? .sevenDays : preset
        } else if switchingMachine {
            usagePreset = .sevenDays
        }
        if let customStartMs { usageCustomStartMs = customStartMs }
        if let customEndMs { usageCustomEndMs = customEndMs }
        await dispatchUsageScreenOpen(applyFocus: true)
    }

    func usageSelectRange(_ preset: PBUsageRangePreset) async {
        guard !closed, !usageMachineID.isEmpty else { return }
        let next = preset == .unspecified ? .sevenDays : preset
        usagePreset = next
        if next == .custom {
            let clamped = Self.clampUsageCustomRange(startMs: usageCustomStartMs, endMs: usageCustomEndMs, nowMs: clock())
            usageCustomStartMs = clamped.startMs
            usageCustomEndMs = clamped.endMs
        }
        await dispatchUsageScreenOpen(applyFocus: false)
    }

    func usageSelectCustomDates(startMs: Int64, endMs: Int64) async {
        guard !closed, !usageMachineID.isEmpty else { return }
        let clamped = Self.clampUsageCustomRange(startMs: startMs, endMs: endMs, nowMs: clock())
        usagePreset = .custom
        usageCustomStartMs = clamped.startMs
        usageCustomEndMs = clamped.endMs
        await dispatchUsageScreenOpen(applyFocus: false)
    }

    func usageSelectSort(machineID: String, sortBy: PBCoreUsageSortColumn) async {
        guard !closed, !machineID.isEmpty else { return }
        await dispatch {
            $0.usageScreenSelectSort = .with {
                $0.machineID = machineID
                $0.sortBy = sortBy
            }
        }
    }

    func usageSelectSeries(machineID: String, seriesKey: String, toggle: Bool = true) async {
        guard !closed, !machineID.isEmpty else { return }
        await dispatch {
            $0.usageScreenSelectSeries = .with {
                $0.machineID = machineID
                $0.seriesKey = seriesKey
                $0.toggle = toggle
            }
        }
    }

    func usageScrub(machineID: String, bucketIndex: Int32, clear: Bool = false) async {
        guard !closed, !machineID.isEmpty else { return }
        await dispatch {
            $0.usageScreenScrub = .with {
                $0.machineID = machineID
                $0.bucketIndex = bucketIndex
                $0.clear = clear
            }
        }
    }

    func retryUsageScreen(machineID: String) async {
        guard !closed, !machineID.isEmpty else { return }
        await dispatch {
            $0.usageScreenRetry = .with { $0.machineID = machineID }
        }
    }

    private func dispatchUsageScreenOpen(applyFocus: Bool) async {
        guard !usageMachineID.isEmpty else { return }
        await hydrateUsageCache(machineID: usageMachineID)
        let params = Self.buildUsageOpenParams(
            preset: usagePreset,
            customStartMs: usageCustomStartMs,
            customEndMs: usageCustomEndMs,
            nowMs: clock()
        )
        let machineID = usageMachineID
        let fAgent = usageFocusAgent
        let fModel = usageFocusModel
        await dispatch {
            $0.usageScreenOpen = .with {
                $0.machineID = machineID
                $0.preset = params.preset
                $0.customStartMs = params.customStartMs
                $0.customEndMs = params.customEndMs
                $0.timezone = params.timezone
                $0.tzOffsetMinutes = params.tzOffsetMinutes
                $0.bucketBoundsMs = params.bucketBoundsMs
                $0.prevStartMs = params.prevStartMs
                $0.applyFocus = applyFocus
                $0.focusAgent = fAgent
                $0.focusModel = fModel
            }
        }
        await renderTargets([.usage])
    }

    func acquireMachineStatusWatch(_ machineID: String, mode: PBMachinePollMode) async -> UUID? {
        guard !closed, !machineID.isEmpty else { return nil }
        let token = UUID()
        machineStatusWatches[token] = MachineStatusWatchEntry(token: token, machineID: machineID, mode: mode)
        if mode == .live {
            activeMachineStatusID = machineID
            await hydrateUsageCache(machineID: machineID)
        }
        await syncMachineStatusWatch(machineID: machineID)
        if mode == .live {
            await renderTargets([.machineStatus])
        }
        return token
    }

    func releaseMachineStatusWatch(_ token: UUID?) async {
        guard let token, let entry = machineStatusWatches.removeValue(forKey: token) else { return }
        guard !closed else { return }
        await syncMachineStatusWatch(machineID: entry.machineID)
    }

    private func syncMachineStatusWatch(machineID: String) async {
        let active = machineStatusWatches.values.filter { $0.machineID == machineID }
        let visible = !active.isEmpty
        let mode: PBMachinePollMode = active.contains(where: { $0.mode == .live }) ? .live : .overview
        await dispatch {
            $0.machineStatusWatch = .with {
                $0.machineID = machineID
                $0.visible = visible
                $0.mode = mode
            }
        }
    }

    func renameClient(deviceID: String, name: String, restoreDefault: Bool = false) async throws {
        guard !closed else { throw CoreClientError.unavailable }
        var envelope = PBCoreEvent()
        envelope.updateClientName = .with {
            $0.deviceID = deviceID
            $0.name = name
            $0.restoreDefault = restoreDefault
        }
        envelope.nowMs = clock()
        envelope.monoMs = monoClock()
        if envelope.randomIds.isEmpty && !randomIdPool.isEmpty {
            envelope.randomIds = randomIdPool
            randomIdPool.removeAll(keepingCapacity: true)
        }
        if envelope.unitFloats.isEmpty && !unitFloatPool.isEmpty {
            envelope.unitFloats = unitFloatPool
            unitFloatPool.removeAll(keepingCapacity: true)
        }
        replenishEntropy(minIds: 8, minFloats: 8)
        let batch = try client.dispatch(envelope)
        var renameEffect: PBRenameClientEffect?
        var remaining: [PBCoreEffect] = []
        for eff in batch.effects {
            if case .renameClient(let r) = eff.effect {
                renameEffect = r
            } else {
                remaining.append(eff)
            }
        }
        enqueueSharedFileStorageEffects(remaining)
        enqueueDraftStorageEffects(remaining)
        await handleEffects(remaining)
        if let r = renameEffect {
            try await executor.renameDevice(r.deviceID, name: r.name)
        }
    }

    func setManageActive(_ active: Bool, machineID: String? = nil) async {
        guard !closed else { return }
        manageSubscribers = max(0, manageSubscribers + (active ? 1 : -1))
        if active {
            if let machineID {
                activeManageMachineID = machineID
            }
            await renderTargets([.project])
        } else if manageSubscribers == 0 {
            activeManageMachineID = ""
        }
    }

    func selectProjectsMachine(_ machineID: String) async {
        guard !closed else { return }
        activeManageMachineID = machineID
        await renderTargets([.project])
    }

    func acquireDirPicker(key: String) async -> UUID? {
        guard !closed, !key.isEmpty else { return nil }
        let token = UUID()
        activeDirPickers[token] = key
        await renderTargets([.project])
        return token
    }

    func releaseDirPicker(_ token: UUID?) async {
        guard let token, let key = activeDirPickers.removeValue(forKey: token) else { return }
        guard !closed else { return }
        if !activeDirPickers.values.contains(key) {
            let screens = self.screens
            let seq = nextRenderSeq()
            await MainActor.run {
                if screens.acceptRender(.dirPicker(key), seq: seq) {
                    _ = screens.dirPickers.removeValue(forKey: key)
                }
            }
        }
    }

    func acquireSessionWatch(
        _ sid: String,
        initialWindow: PBSessionWindowRequest = .with { $0.limit = 80 }
    ) async -> UUID? {
        guard !closed, !sid.isEmpty else { return nil }
        let token = UUID()
        let wasWatched = sessionWatches.values.contains { $0.sessionID == sid }
        sessionWatches[token] = SessionWatchEntry(token: token, sessionID: sid, window: initialWindow)
        sessionWatchOrder.append(token)
        if !wasWatched {
            await syncWatchedSessionsToExecutor()
            await dispatch {
                $0.sessionWatch = .with {
                    $0.sessionID = sid
                    $0.watched = true
                }
            }
        }
        await refreshSessionViews(for: [sid])
        return token
    }

    private func syncWatchedSessionsToExecutor() async {
        var seen = Set<String>()
        var ordered: [String] = []
        for token in sessionWatchOrder.reversed() {
            guard let sid = sessionWatches[token]?.sessionID, !sid.isEmpty, seen.insert(sid).inserted else { continue }
            ordered.append(sid)
        }
        await executor.setWatchedSessions(ordered)
    }

    func updateSessionWindow(token: UUID?, window: PBSessionWindowRequest) async {
        guard !closed, let token, var entry = sessionWatches[token] else { return }
        if entry.window != window {
            entry.window = window
            sessionWatches[token] = entry
        }
        await refreshSessionViews(for: [entry.sessionID])
    }

    func releaseSessionWatch(_ token: UUID?) async {
        guard let token, let entry = sessionWatches.removeValue(forKey: token) else { return }
        sessionWatchOrder.removeAll { $0 == token }
        guard !closed else { return }
        let stillWatched = sessionWatches.values.contains { $0.sessionID == entry.sessionID }
        if !stillWatched {
            await syncWatchedSessionsToExecutor()
            await dispatch {
                $0.sessionWatch = .with {
                    $0.sessionID = entry.sessionID
                    $0.watched = false
                }
            }
        }
    }

    func openSession(_ sid: String, refreshModels: Bool = false, refreshSettings: Bool = false) async {
        guard !closed, !sid.isEmpty else { return }
        await dispatch {
            $0.sessionOpen = .with {
                $0.sessionID = sid
                $0.refreshModels = refreshModels
                $0.refreshSettings = refreshSettings
            }
        }
    }

    func retryLoadSession(_ sid: String) async {
        guard !closed, !sid.isEmpty else { return }
        await dispatch {
            $0.sessionRetryLoad.sessionID = sid
        }
    }

    func setSharedFileHandlers(_ handlers: SharedFileHandlers) {
        sharedFileHandlers = handlers
    }

    func startSharedFileDownload(sessionID: String, fileID: String, autoOpenIfStaying: Bool) async {
        guard !closed, !sessionID.isEmpty, !fileID.isEmpty else { return }
        await dispatch {
            $0.sharedFileStartDownload = .with {
                $0.sessionID = sessionID
                $0.fileID = fileID
                $0.autoOpenIfStaying = autoOpenIfStaying
            }
        }
    }

    func pauseSharedFileDownload(sessionID: String, fileID: String) async {
        guard !closed, !sessionID.isEmpty, !fileID.isEmpty else { return }
        await dispatch {
            $0.sharedFilePauseDownload = .with {
                $0.sessionID = sessionID
                $0.fileID = fileID
            }
        }
    }

    func cancelSharedFileDownload(sessionID: String, fileID: String) async {
        guard !closed, !sessionID.isEmpty, !fileID.isEmpty else { return }
        await dispatch {
            $0.sharedFileCancelDownload = .with {
                $0.sessionID = sessionID
                $0.fileID = fileID
            }
        }
    }

    func deleteSharedFileLocal(sessionID: String, fileID: String) async {
        guard !closed, !sessionID.isEmpty, !fileID.isEmpty else { return }
        await dispatch {
            $0.sharedFileDeleteLocal = .with {
                $0.sessionID = sessionID
                $0.fileID = fileID
            }
        }
    }

    func enterVisibleSession(_ sid: String) async -> UUID {
        let token = UUID()
        guard !closed, !sid.isEmpty else { return token }
        visibleSessions.append((token: token, sessionID: sid))
        await syncVisibleSession()
        return token
    }

    func leaveVisibleSession(_ token: UUID) async {
        visibleSessions.removeAll { $0.token == token }
        guard !closed else { return }
        await syncVisibleSession()
    }

    private func syncVisibleSession() async {
        let next = visibleSessions.last?.sessionID ?? ""
        guard next != currentVisibleSessionID else { return }
        currentVisibleSessionID = next
        await dispatch {
            $0.sharedFileSetVisibleSession = .with { $0.sessionID = next }
        }
    }

    func forgetSharedFileSession(_ sessionID: String) async {
        guard !closed, !sessionID.isEmpty else { return }
        await dispatch {
            $0.sharedFileForgetSession = .with { $0.sessionID = sessionID }
        }
    }

    func clearAllSharedFiles() async {
        guard !closed else { return }
        await dispatch {
            $0.sharedFileClearAll = PBSharedFileClearAllAction()
        }
        await drainSharedFileQueue()
    }

    func recordSharedFileDuration(sessionID: String, fileID: String, durationMs: Int64) async {
        guard !closed, !sessionID.isEmpty, !fileID.isEmpty, durationMs > 0 else { return }
        let store = self.sharedFileStore
        let key = SharedFileKey(sessionId: sessionID, fileId: fileID)
        enqueueSharedFileOp {
            store.recordDuration(key, ms: durationMs)
        }
        await dispatch {
            $0.sharedFileDurationProbed = .with {
                $0.sessionID = sessionID
                $0.fileID = fileID
                $0.durationMs = durationMs
            }
        }
    }

    func reportSharedFileLocalMissing(sessionID: String, fileID: String) async {
        guard !closed, !sessionID.isEmpty, !fileID.isEmpty else { return }
        await dispatch {
            $0.sharedFileLocalMissing = .with {
                $0.sessionID = sessionID
                $0.fileID = fileID
            }
        }
    }

    func drainSharedFileQueue() async {
        await sharedFileQueue.drain()
    }

    private func enqueueSharedFileOp(_ op: @escaping @Sendable () async -> Void) {
        sharedFileQueue.enqueue(op)
    }

    func openSessionImageViewer(sessionID: String, seq: UInt64, imageIndex: Int32 = 0, viewerIndex: Int32 = 0) async {
        guard !closed, !sessionID.isEmpty else { return }
        await dispatch {
            $0.sessionImageViewerOpen = .with {
                $0.sessionID = sessionID
                $0.seq = seq
                $0.imageIndex = imageIndex
                $0.viewerIndex = viewerIndex
            }
        }
    }

    func navigateSessionImageViewer(sessionID: String, delta: Int32 = 0, setIndex: Bool = false, index: Int32 = 0) async {
        guard !closed, !sessionID.isEmpty else { return }
        await dispatch {
            $0.sessionImageViewerNavigate = .with {
                $0.sessionID = sessionID
                $0.delta = delta
                $0.setIndex = setIndex
                $0.index = index
            }
        }
    }

    func closeSessionImageViewer(sessionID: String) async {
        guard !closed, !sessionID.isEmpty else { return }
        await dispatch {
            $0.sessionImageViewerClose = .with {
                $0.sessionID = sessionID
            }
        }
    }

    func evaluateAttachments(_ request: PBAttachmentEvalScreenRequest) -> PBAttachmentEvalViewModel {
        guard !closed else { return PBAttachmentEvalViewModel() }
        return (try? client.view(PBCoreScreen.with {
            $0.attachmentEval = request
        }).attachmentEval) ?? PBAttachmentEvalViewModel()
    }

    /// Queries Core's authoritative per-file byte limit for `scope` (0 when unavailable).
    func maxFileBytes(for scope: PBDraftScope) -> Int64 {
        guard !closed else { return 0 }
        switch scope.kind {
        case .session:
            return evaluateAttachments(.with { $0.sessionID = scope.id }).maxFileBytes
        case .newSession:
            return (try? client.view(PBCoreScreen.with {
                $0.newSession = .with { $0.projectKey = scope.id }
            }).newSession.maxFileBytes) ?? 0
        default:
            return 0
        }
    }

    func submitSessionCommand(
        sessionID: String,
        command: PBCommand,
        machineID: String = "",
        slotKey: String = ""
    ) async {
        guard !closed else { return }
        await dispatch {
            $0.sessionSubmitCommand = .with {
                $0.slotKey = slotKey
                $0.machineID = machineID
                $0.sessionID = sessionID
                $0.command = command
            }
        }
    }

    func abandonSessionCommand(sessionID: String, slotKey: String) async {
        guard !closed else { return }
        await dispatch {
            $0.sessionAbandonCommand = .with {
                $0.slotKey = slotKey
                $0.sessionID = sessionID
            }
        }
    }

    func cancelTrayItem(sessionID: String, requestID: String) async {
        guard !closed else { return }
        await dispatch {
            $0.sessionCancelTrayItem = .with {
                $0.sessionID = sessionID
                $0.requestID = requestID
            }
        }
    }

    func ignoreTrayItem(sessionID: String, requestID: String) async {
        guard !closed else { return }
        await dispatch {
            $0.sessionIgnoreTrayItem = .with {
                $0.sessionID = sessionID
                $0.requestID = requestID
            }
        }
    }

    func promoteTrayItem(sessionID: String, requestID: String) async {
        guard !closed else { return }
        await dispatch {
            $0.sessionPromoteTrayItem = .with {
                $0.sessionID = sessionID
                $0.requestID = requestID
            }
        }
    }

    func restoreTrayItem(sessionID: String, requestID: String) async {
        guard !closed else { return }
        await dispatch {
            $0.sessionRestoreTrayItem = .with {
                $0.sessionID = sessionID
                $0.requestID = requestID
            }
        }
    }

    func retryTrayItem(sessionID: String, requestID: String) async {
        guard !closed else { return }
        await dispatch {
            $0.sessionRetryTrayItem = .with {
                $0.sessionID = sessionID
                $0.requestID = requestID
            }
        }
    }

    func resumeTrayItem(sessionID: String, requestID: String) async {
        guard !closed else { return }
        await dispatch {
            $0.sessionResumeTrayItem = .with {
                $0.sessionID = sessionID
                $0.requestID = requestID
            }
        }
    }

    func cancelAllPaused(sessionID: String) async {
        guard !closed else { return }
        await dispatch {
            $0.sessionCancelAllPaused = .with {
                $0.sessionID = sessionID
            }
        }
    }

    func undoCancel(sessionID: String) async {
        guard !closed else { return }
        await dispatch {
            $0.sessionUndoCancel = .with {
                $0.sessionID = sessionID
            }
        }
    }

    func setSessionQuestionIndex(sessionID: String, questionID: String, questionIndex: Int32) async {
        guard !closed else { return }
        await dispatch {
            $0.sessionSetQuestionIndex = .with {
                $0.sessionID = sessionID
                $0.questionID = questionID
                $0.questionIndex = questionIndex
            }
        }
    }

    func loadEarlierSessionHistory(_ sid: String) async {
        guard !closed, !sid.isEmpty else { return }
        await dispatch {
            $0.sessionLoadEarlier = .with {
                $0.sessionID = sid
            }
        }
    }

    func openScheduleList(_ open: Bool = true) async {
        guard !closed else { return }
        await dispatch {
            $0.scheduleListOpen = .with { $0.value = open }
        }
        await renderTargets([.schedules])
    }

    func openScheduleDetail(machineID: String, taskID: String) async {
        guard !closed, !machineID.isEmpty, !taskID.isEmpty else { return }
        activeScheduleDetail = (machineID, taskID)
        await dispatch {
            $0.scheduleDetailOpen = .with {
                $0.machineID = machineID
                $0.taskID = taskID
            }
        }
        await renderTargets([.scheduleDetail])
    }

    func hideScheduleDetail(machineID: String, taskID: String) async {
        guard !closed, !machineID.isEmpty, !taskID.isEmpty else { return }
        await dispatch {
            $0.scheduleDetailHide = .with {
                $0.machineID = machineID
                $0.taskID = taskID
            }
        }
    }

    func closeScheduleDetail(machineID: String, taskID: String) async {
        guard !closed, !machineID.isEmpty, !taskID.isEmpty else { return }
        if activeScheduleDetail?.machineID == machineID && activeScheduleDetail?.taskID == taskID {
            activeScheduleDetail = nil
        }
        await dispatch {
            $0.scheduleDetailClose = .with {
                $0.machineID = machineID
                $0.taskID = taskID
            }
        }
    }

    func openScheduleForm(
        mode: PBScheduleFormMode,
        machineID: String = "",
        taskID: String = "",
        sessionID: String = "",
        projectKey: String = ""
    ) async {
        guard !closed else { return }
        activeScheduleFormKey = ""
        let tz = TimeZone.current.identifier
        await dispatch {
            $0.scheduleFormOpen = .with {
                $0.mode = mode
                $0.machineID = machineID
                $0.taskID = taskID
                $0.sessionID = sessionID
                $0.projectKey = projectKey
                $0.localTimezone = tz
            }
        }
        await renderTargets([.scheduleForm])
    }

    func acquireScheduleStripWatch(_ sid: String) async -> UUID? {
        guard !closed, !sid.isEmpty else { return nil }
        let token = UUID()
        let wasWatched = scheduleStripWatches.values.contains(sid)
        scheduleStripWatches[token] = sid
        if !wasWatched {
            await renderTargets([.scheduleSessionStrip])
        }
        return token
    }

    func releaseScheduleStripWatch(_ token: UUID?) async {
        guard let token, let sid = scheduleStripWatches.removeValue(forKey: token) else { return }
        guard !closed else { return }
        if !scheduleStripWatches.values.contains(sid) {
            let seq = nextRenderSeq()
            await MainActor.run { [screens, seq] in
                if screens.acceptRender(.scheduleStrip(sid), seq: seq) {
                    _ = screens.scheduleStrips.removeValue(forKey: sid)
                }
            }
        }
    }

    // MARK: - Attachment & Draft Actions

    func setAttachmentHandlers(_ handlers: AttachmentHandlers) {
        attachmentHandlers = handlers
    }

    func drainDraftStorageQueue() async {
        await draftStorageQueue.drain()
    }

    private func enqueueDraftStorageOp(_ op: @escaping @Sendable () async -> Void) {
        draftStorageQueue.enqueue(op)
    }

    func openDraft(scope: PBDraftScope, machineID: String = "") async {
        guard !closed else { return }
        await dispatch {
            $0.draftOpen = .with {
                $0.scope = scope
                $0.machineID = machineID
            }
        }
    }

    func setDraftText(scope: PBDraftScope, text: String) async {
        guard !closed else { return }
        await dispatch {
            $0.draftSetText = .with {
                $0.scope = scope
                $0.text = text
            }
        }
    }

    func appendDraftText(scope: PBDraftScope, text: String, machineID: String = "") async {
        guard !closed, !text.isEmpty else { return }
        await dispatch {
            $0.draftAppendText = .with {
                $0.scope = scope
                $0.text = text
                $0.machineID = machineID
            }
        }
    }

    func setDraftTargetMachine(scope: PBDraftScope, machineID: String) async {
        guard !closed else { return }
        await dispatch {
            $0.draftSetTargetMachine = .with {
                $0.scope = scope
                $0.machineID = machineID
            }
        }
    }

    func reserveDraftSlots(scope: PBDraftScope, reservationID: String, imageSlots: Int32, fileSlots: Int32) async {
        guard !closed else { return }
        await dispatch {
            $0.draftReserveSlots = .with {
                $0.scope = scope
                $0.reservationID = reservationID
                $0.imageSlots = imageSlots
                $0.fileSlots = fileSlots
            }
        }
    }

    func releaseDraftSlots(scope: PBDraftScope, reservationID: String) async {
        guard !closed else { return }
        await dispatch {
            $0.draftReleaseSlots = .with {
                $0.scope = scope
                $0.reservationID = reservationID
            }
        }
    }

    func addDraftItems(scope: PBDraftScope, reservationID: String = "", items: [PBDraftPickedItem]) async {
        guard !closed else { return }
        await dispatch {
            $0.draftAddItems = .with {
                $0.scope = scope
                $0.reservationID = reservationID
                $0.items = items
            }
        }
    }

    func removeDraftItem(scope: PBDraftScope, itemID: String) async {
        guard !closed, !itemID.isEmpty else { return }
        await dispatch {
            $0.draftRemoveItem = .with {
                $0.scope = scope
                $0.itemID = itemID
            }
        }
    }

    func retryDraftItem(scope: PBDraftScope, itemID: String) async {
        guard !closed, !itemID.isEmpty else { return }
        await dispatch {
            $0.draftRetryItem = .with {
                $0.scope = scope
                $0.itemID = itemID
            }
        }
    }

    func setDraftOriginal(scope: PBDraftScope, original: Bool) async {
        guard !closed else { return }
        await dispatch {
            $0.draftSetOriginal = .with {
                $0.scope = scope
                $0.original = original
            }
        }
    }

    func clearDraft(scope: PBDraftScope) async {
        guard !closed else { return }
        await dispatch {
            $0.draftClear = .with { $0.scope = scope }
        }
    }

    func forgetDraftSession(_ sessionID: String) async {
        guard !closed, !sessionID.isEmpty else { return }
        await dispatch {
            $0.draftForgetSession = .with { $0.sessionID = sessionID }
        }
    }

    @discardableResult
    func clearAllDrafts() async -> (any Error)? {
        guard !closed else { return nil }
        lastDraftClearError = nil
        await dispatch {
            $0.draftClearAll = PBDraftClearAllAction()
        }
        await drainDraftStorageQueue()
        return lastDraftClearError
    }

    private func recordDraftClearError(_ error: any Error) {
        if lastDraftClearError == nil {
            lastDraftClearError = error
        }
    }

    func dismissDraftNotice(scope: PBDraftScope) async {
        guard !closed else { return }
        await dispatch {
            $0.draftDismissNotice = .with { $0.scope = scope }
        }
    }

    func pauseAttachmentUpload(scope: PBDraftScope, itemID: String) async {
        guard !closed, !itemID.isEmpty else { return }
        await dispatch {
            $0.attachmentPauseUpload = .with {
                $0.scope = scope
                $0.itemID = itemID
            }
        }
    }

    func resumeAttachmentUpload(scope: PBDraftScope, itemID: String) async {
        guard !closed, !itemID.isEmpty else { return }
        await dispatch {
            $0.attachmentResumeUpload = .with {
                $0.scope = scope
                $0.itemID = itemID
            }
        }
    }

    func sendSessionDraft(sessionID: String, mode: PBSendMode) async {
        guard !closed, !sessionID.isEmpty else { return }
        await dispatch {
            $0.draftSendSession = .with {
                $0.sessionID = sessionID
                $0.mode = mode
            }
        }
    }

    func putBackInput(sessionID: String, requestID: String) async {
        guard !closed, !sessionID.isEmpty, !requestID.isEmpty else { return }
        await dispatch {
            $0.draftPutBackInput = .with {
                $0.sessionID = sessionID
                $0.requestID = requestID
            }
        }
    }

    func openDraftImageViewer(scope: PBDraftScope, index: Int32) async {
        guard !closed else { return }
        await dispatch {
            $0.draftImageViewerOpen = .with {
                $0.scope = scope
                $0.index = index
            }
        }
    }

    func navigateDraftImageViewer(scope: PBDraftScope, delta: Int32 = 0, setIndex: Bool = false, index: Int32 = 0) async {
        guard !closed else { return }
        await dispatch {
            $0.draftImageViewerNavigate = .with {
                $0.scope = scope
                $0.delta = delta
                $0.setIndex = setIndex
                $0.index = index
            }
        }
    }

    func closeDraftImageViewer(scope: PBDraftScope) async {
        guard !closed else { return }
        await dispatch {
            $0.draftImageViewerClose = .with { $0.scope = scope }
        }
    }

    func submitNewSession(
        projectKey: String,
        autoModeSupported: Bool
    ) async {
        guard !closed else { return }
        await dispatch {
            $0.newSessionSubmit = .with {
                $0.projectKey = projectKey
                $0.autoModeSupported = autoModeSupported
            }
        }
    }

    // MARK: - Dispatch & Effect Execution

    func dispatch(_ build: (inout PBCoreEvent) -> Void) async {
        var ev = PBCoreEvent()
        build(&ev)
        await dispatch(ev)
    }

    func dispatch(_ event: PBCoreEvent) async {
        guard !closed else { return }
        var envelope = event
        envelope.nowMs = clock()
        envelope.monoMs = monoClock()
        if envelope.randomIds.isEmpty && !randomIdPool.isEmpty {
            envelope.randomIds = randomIdPool
            randomIdPool.removeAll(keepingCapacity: true)
        }
        if envelope.unitFloats.isEmpty && !unitFloatPool.isEmpty {
            envelope.unitFloats = unitFloatPool
            unitFloatPool.removeAll(keepingCapacity: true)
        }
        replenishEntropy(minIds: 8, minFloats: 8)
        do {
            let batch = try client.dispatch(envelope)
            enqueueSharedFileStorageEffects(batch.effects)
            enqueueDraftStorageEffects(batch.effects)
            await handleEffects(batch.effects)
        } catch {
            // Core rejects malformed events; keep host loop alive.
        }
    }

    #if DEBUG
    private var navigateSuspendHookForTesting: (@Sendable () async -> Void)?

    func setNavigateSuspendHookForTesting(_ hook: (@Sendable () async -> Void)?) {
        navigateSuspendHookForTesting = hook
    }

    func processEffectBatchForTesting(_ effects: [PBCoreEffect]) async {
        guard !closed else { return }
        enqueueSharedFileStorageEffects(effects)
        enqueueDraftStorageEffects(effects)
        await handleEffects(effects)
    }
    #endif

    /// Synchronously enqueues every draft/outbox storage and local-ref release Effect in `effects`
    /// onto the process-wide single FIFO queue before any `await` in `handleEffects` can allow
    /// Swift actor reentrancy to reorder a later dispatch ahead of this batch.
    private func enqueueDraftStorageEffects(_ effects: [PBCoreEffect]) {
        guard !closed else { return }
        for eff in effects {
            switch eff.effect {
            case .draftStorageList(let eff):
                let store = self.pendingStore
                enqueueDraftStorageOp { [weak self] in
                    let result = store.list(opID: eff.opID)
                    await self?.dispatch { $0.draftStorageListed = result }
                }

            case .draftStorageRead(let eff):
                let store = self.pendingStore
                enqueueDraftStorageOp { [weak self] in
                    let result = store.read(opID: eff.opID, bucket: eff.bucket, key: eff.key)
                    await self?.dispatch { $0.draftStorageReadResult = result }
                }

            case .draftStorageWrite(let eff):
                let store = self.pendingStore
                enqueueDraftStorageOp { [weak self] in
                    let result = store.write(opID: eff.opID, bucket: eff.bucket, key: eff.key, data: eff.data)
                    await self?.dispatch { $0.draftStorageWriteResult = result }
                }

            case .draftStorageDelete(let eff):
                let store = self.pendingStore
                enqueueDraftStorageOp {
                    store.delete(bucket: eff.bucket, key: eff.key)
                }

            case .draftStorageClearAll:
                let store = self.pendingStore
                let clearAll = self.attachmentHandlers?.onClearAllLocal
                enqueueDraftStorageOp { [weak self] in
                    do {
                        try store.clearAll()
                    } catch {
                        await self?.recordDraftClearError(error)
                    }
                    if let clearAll {
                        await MainActor.run { clearAll() }
                    }
                }

            case .attachmentReleaseLocal(let eff):
                let store = self.pendingStore
                let refs = eff.refs
                let onRelease = self.attachmentHandlers?.onReleaseLocalRefs
                enqueueDraftStorageOp {
                    store.releaseLocal(refs: refs)
                    if let onRelease, !refs.isEmpty {
                        await MainActor.run { onRelease(refs) }
                    }
                }

            default:
                break
            }
        }
    }

    /// Synchronously enqueues every shared-file storage Effect in `effects` onto the single
    /// global FIFO queue before any `await` in `handleEffects` can allow actor reentrancy to
    /// interleave a later dispatch's storage Effects ahead of this batch.
    private func enqueueSharedFileStorageEffects(_ effects: [PBCoreEffect]) {
        guard !closed else { return }
        for eff in effects {
            switch eff.effect {
            case .sharedFileScanLocal(let eff):
                let store = self.sharedFileStore
                enqueueSharedFileOp { [weak self] in
                    let result = store.scanLocal(opID: eff.opID)
                    await self?.dispatch { $0.sharedFileLocalScanned = result }
                }

            case .sharedFileInspectDisk(let eff):
                let store = self.sharedFileStore
                enqueueSharedFileOp { [weak self] in
                    let result = store.inspectDisk(eff)
                    await self?.dispatch { $0.sharedFileDiskInspected = result }
                }

            case .sharedFileWriteChunk(let eff):
                let store = self.sharedFileStore
                enqueueSharedFileOp { [weak self] in
                    let result = store.writeChunk(eff)
                    await self?.dispatch { $0.sharedFileWriteChunkResult = result }
                }

            case .sharedFileVerifyAndCommit(let eff):
                let store = self.sharedFileStore
                enqueueSharedFileOp { [weak self] in
                    let result = store.verifyAndCommit(eff)
                    await self?.dispatch { $0.sharedFileVerifyAndCommitResult = result }
                }

            case .sharedFileDeletePartial(let eff):
                let store = self.sharedFileStore
                enqueueSharedFileOp {
                    store.deletePartial(sessionID: eff.sessionID, fileID: eff.fileID)
                }

            case .sharedFileDeleteLocal(let eff):
                let store = self.sharedFileStore
                let clearThumb = self.sharedFileHandlers?.onClearThumbnail
                let key = SharedFileKey(sessionId: eff.sessionID, fileId: eff.fileID)
                enqueueSharedFileOp {
                    store.deleteLocal(sessionID: eff.sessionID, fileID: eff.fileID)
                    if let clearThumb {
                        await MainActor.run { clearThumb(key) }
                    }
                }

            case .sharedFileDeleteSession(let eff):
                let store = self.sharedFileStore
                let clearSession = self.sharedFileHandlers?.onClearSessionThumbnails
                enqueueSharedFileOp {
                    store.deleteSession(sessionID: eff.sessionID)
                    if let clearSession {
                        await MainActor.run { clearSession(eff.sessionID) }
                    }
                }

            case .sharedFileClearAll:
                let store = self.sharedFileStore
                let clearAll = self.sharedFileHandlers?.onClearAllThumbnails
                enqueueSharedFileOp {
                    store.clearAll()
                    if let clearAll {
                        await MainActor.run { clearAll() }
                    }
                }

            default:
                break
            }
        }
    }

    private func handleEffects(_ effects: [PBCoreEffect]) async {
        var renderTargets: Set<PBCoreScreenTarget> = []
        var restoredComposers: [PBSessionRestoreComposerEffect] = []
        var completedSharedFiles: [PBSharedFileCompletedEffect] = []
        var demoReplies: [PBCoreEvent] = []
        for eff in effects {
            guard !closed else { return }
            switch eff.effect {
            case .render(let r):
                renderTargets.formUnion(r.screens)

            case .openStream(let s):
                guard !demoMode else { continue }
                let legId = s.legID
                let tok = s.token
                await executor.openStream(
                    legId: legId,
                    token: tok,
                    callbacks: Remote.StreamCallbacks(
                        onReady: { [weak self] id, serverTimeMs in
                            await self?.dispatch {
                                $0.streamReady = .with {
                                    $0.legID = id
                                    $0.serverTimeMs = serverTimeMs
                                }
                            }
                        },
                        onFrame: { [weak self] frameEvent in
                            await self?.dispatch { $0.streamFrame = frameEvent }
                        },
                        onClosed: { [weak self] closedEvent in
                            await self?.dispatch { $0.streamClosed = closedEvent }
                        }
                    )
                )

            case .closeStream(let s):
                guard !demoMode else { continue }
                await executor.closeStream(legId: s.legID)

            case .ensureToken(let req):
                guard !demoMode else { continue }
                spawnIO { host in
                    let res = await host.executor.ensureToken(
                        opId: req.opID,
                        forceLogin: req.forceLogin,
                        timeoutMs: req.timeoutMs
                    )
                    await host.dispatch { $0.tokenResult = res }
                }

            case .discardToken(let d):
                guard !demoMode else { continue }
                let tok = d.token
                spawnIO { host in
                    await host.executor.discardToken(tok)
                }

            case .securityNotifyOpened:
                break

            case .scheduleTimer(let t):
                guard !demoMode || Self.isDemoAllowedTimer(t.kind) else { continue }
                scheduleTimer(timerId: t.timerID, kind: t.kind, key: t.key, delayMs: t.delayMs)

            case .cancelTimer(let t):
                guard !demoMode || Self.isDemoAllowedTimer(t.kind) else { continue }
                cancelTimer(timerId: t.timerID, kind: t.kind, key: t.key)

            case .cacheRead(let r):
                if demoMode {
                    if let ev = demoCacheReadReply(r) { demoReplies.append(ev) }
                    continue
                }
                spawnIO { host in
                    let event = await host.executor.executeCacheRead(r)
                    await host.dispatch(event)
                }

            case .cacheWrite(let w):
                guard !demoMode else { continue }
                enqueueCacheOp { executor in
                    await executor.executeCacheWrite(w)
                }

            case .cacheDelete(let d):
                guard !demoMode else { continue }
                let callback = self.onSessionRemoved
                enqueueCacheOp { executor in
                    await executor.executeCacheDelete(d, onSessionRemoved: callback)
                }

            case .rpc(let r):
                if demoMode {
                    if let ev = demoRpcReply(r) { demoReplies.append(ev) }
                    continue
                }
                spawnIO { host in
                    let event = await host.executor.executeRpc(r)
                    await host.dispatch(event)
                }

            case .dispatchCommand(let c):
                await handleDispatchCommand(c)

            case .saveHomeScope(let s):
                let token = encodeScopeToken(scopeKey: s.scopeKey, hmacKey: scopeHmacKey)
                defaults.setHomeScopeToken(token)

            case .navigate(let n):
                let handler = onNavigate
                await MainActor.run { handler(n) }
                #if DEBUG
                if let hook = navigateSuspendHookForTesting {
                    await hook()
                }
                #endif

            case .requestEntropy(let req):
                replenishEntropy(
                    minIds: max(Int(req.minIds), 8),
                    minFloats: max(Int(req.minFloats), 8)
                )
                let ids = randomIdPool
                let floats = unitFloatPool
                randomIdPool.removeAll(keepingCapacity: true)
                unitFloatPool.removeAll(keepingCapacity: true)
                let randomBytes = req.minRandomBytes > 0 ? Self.randomBytes(max(Int(req.minRandomBytes), 4096)) : Data()
                await dispatch {
                    $0.entropyProvided = .with {
                        $0.randomIds = ids
                        $0.unitFloats = floats
                        if !randomBytes.isEmpty { $0.randomBytes = randomBytes }
                    }
                }

            case .sessionRestoreComposer(let r):
                restoredComposers.append(r)

            case .scheduleReadCache(let r):
                if demoMode {
                    demoReplies.append(.with {
                        $0.scheduleCacheLoaded = .with {
                            $0.opID = r.opID
                            $0.machineID = r.machineID
                            if let data = demoSchedules[r.machineID] { $0.resultJson = data }
                        }
                    })
                    continue
                }
                spawnIO { host in
                    let data = await host.executor.cachedScheduleResultJson(r.machineID)
                    await host.dispatch {
                        $0.scheduleCacheLoaded = .with {
                            $0.opID = r.opID
                            $0.machineID = r.machineID
                            if let data { $0.resultJson = data }
                        }
                    }
                }

            case .scheduleDeleteCache(let d):
                guard !demoMode else { continue }
                let mid = d.machineID
                defaults.deleteScheduleSeen(machineID: mid)
                enqueueCacheOp { executor in
                    await executor.deleteScheduleCache(mid)
                }

            case .scheduleLoadSeen(let l):
                if demoMode {
                    let toks = demoScheduleSeen[l.machineID]
                    demoReplies.append(.with {
                        $0.scheduleSeenLoaded = .with {
                            $0.machineID = l.machineID
                            $0.tokens = toks ?? []
                            $0.found = toks != nil
                        }
                    })
                    continue
                }
                let loaded = defaults.loadScheduleSeen(machineID: l.machineID)
                await dispatch {
                    $0.scheduleSeenLoaded = .with {
                        $0.machineID = l.machineID
                        $0.tokens = loaded ?? []
                        $0.found = loaded != nil
                    }
                }

            case .scheduleSaveSeen(let s):
                guard !demoMode else { continue }
                defaults.saveScheduleSeen(machineID: s.machineID, tokens: s.tokens)

            case .scheduleNavigate(let n):
                if n.route == .detail, !n.machineID.isEmpty, !n.taskID.isEmpty {
                    activeScheduleDetail = (n.machineID, n.taskID)
                }
                let handler = onScheduleNavigate
                await MainActor.run { handler(n) }

            case .scheduleDismissNotifications:
                break

            case .scheduleMachinePoll(let eff):
                scheduleMachinePoll(eff)

            case .directPreference(let eff):
                executor.persistDirectPreference(eff.machineID, enabled: eff.enabled)

            case .directSignalSend(let eff):
                _ = executor.handleDirectSignalSend(eff)

            case .directPcCreate(let eff):
                executor.handleDirectPcCreate(eff)

            case .directPcDescribe(let eff):
                spawnIO { host in
                    await host.executor.handleDirectPcDescribe(eff)
                }

            case .directPcSetRemote(let eff):
                spawnIO { host in
                    await host.executor.handleDirectPcSetRemote(eff)
                }

            case .directPcAddCandidates(let eff):
                spawnIO { host in
                    await host.executor.handleDirectPcAddCandidates(eff)
                }

            case .directPcConfirm(let eff):
                executor.handleDirectPcConfirm(eff)

            case .directPcClose(let eff):
                executor.handleDirectPcClose(eff)

            case .directDcReadStats(let eff):
                spawnIO { host in
                    await host.executor.handleDirectDcReadStats(eff)
                }

            case .securityKeyGenerate, .securityKeySign, .securityKeyAgree,
                 .securityKeyEncapsulate, .securityKeyDiscard, .securityStorage, .securityRpc:
                guard !demoMode else { continue }
                _ = executor.optionalSecurityExecutor?.handleEffect(eff)

            case .securityTokenUpdated(let eff):
                guard !demoMode else { continue }
                await executor.applySecurityTokenUpdated(eff)

            case .securityWorkflowResult(let eff):
                guard !demoMode else { continue }
                executor.optionalSecurityClient?.handleWorkflowResult(eff)
                if eff.hasStatus {
                    let st = eff.status
                    spawnIO { host in
                        await host.executor.applySecurityStatusSnapshot(st)
                    }
                }

            case .securityStatusChanged(let eff):
                guard !demoMode else { continue }
                if eff.hasStatus {
                    let st = eff.status
                    executor.optionalSecurityClient?.handleStatusChanged(st)
                    spawnIO { host in
                        await host.executor.applySecurityStatusSnapshot(st)
                    }
                }

            case .securityCryptoBatch(let eff):
                await dispatch {
                    $0.securityCryptoBatchDone = .with {
                        $0.opID = eff.opID
                        $0.error = "crypto batch unavailable on iOS"
                    }
                }

            case .securityOpResult(let eff):
                securityOpWaiters.removeValue(forKey: eff.opID)?.resume(returning: eff)

            case .directBulkRoute(let eff):
                bulkRouteWaiters.removeValue(forKey: eff.callID)?.resume(returning: eff)

            case .terminalReopen(let eff):
                if let entry = terminalReopenListeners[eff.terminalID] {
                    let cb = entry.handler
                    await MainActor.run { cb() }
                }

            case .directRetryNowResult(let eff):
                resumeRetryNowWaiters(machineID: eff.machineID, started: eff.started)

            case .renameClient(let eff):
                spawnIO { host in
                    try? await host.executor.renameDevice(eff.deviceID, name: eff.name)
                }

            case .checkUpdates:
                break

            case .persistClientPrefs(let eff):
                if let handler = onPersistClientPrefs {
                    await MainActor.run { handler(eff) }
                }

            case .prepareCleanupSession(let eff):
                await MainActor.run { [screens, eff] in
                    screens.pendingCleanup = CoreScreens.PendingCleanup(effect: eff)
                }

            case .usageCacheWrite(let eff):
                defaults.saveUsageCache(machineID: eff.machineID, snapshot: eff.snapshot)

            case .sharedFileScanLocal,
                 .sharedFileInspectDisk,
                 .sharedFileWriteChunk,
                 .sharedFileVerifyAndCommit,
                 .sharedFileDeletePartial,
                 .sharedFileDeleteLocal,
                 .sharedFileDeleteSession,
                 .sharedFileClearAll:
                // Already enqueued synchronously in batch order before any await.
                break

            case .sharedFileCompleted(let eff):
                completedSharedFiles.append(eff)

            case .draftStorageList,
                 .draftStorageRead,
                 .draftStorageWrite,
                 .draftStorageDelete,
                 .draftStorageClearAll,
                 .attachmentReleaseLocal:
                // Already enqueued synchronously in batch order before any await.
                break

            case .terminalHistorySendRequest(let eff):
                if let entry = terminalHistoryRequestListeners[eff.terminalID] {
                    let cb = entry.handler
                    await MainActor.run { cb(eff) }
                }

            case .terminalHistoryUpdated(let eff):
                if let entry = terminalHistoryUpdatedListeners[eff.terminalID] {
                    let cb = entry.handler
                    await MainActor.run { cb(eff) }
                }

            case .attachmentPrepareImage(let eff):
                spawnIO { host in
                    await host.executePrepareImage(eff)
                }

            case .attachmentHashFile(let eff):
                spawnIO { host in
                    await host.executeHashFile(eff)
                }

            case .attachmentReadChunk(let eff):
                spawnIO { host in
                    await host.executeReadChunk(eff)
                }

            case nil:
                break
            }
        }
        if !restoredComposers.isEmpty {
            await MainActor.run { [screens, restoredComposers] in
                for r in restoredComposers {
                    screens.pendingComposerRestores.append(CoreScreens.RestoredComposer(effect: r))
                }
            }
        }
        for reply in demoReplies {
            guard !closed else { return }
            await dispatch(reply)
        }
        if !renderTargets.isEmpty {
            await self.renderTargets(renderTargets)
        }
        if !completedSharedFiles.isEmpty, let onCompleted = self.sharedFileHandlers?.onCompleted {
            let visibleSessionID = self.currentVisibleSessionID
            await MainActor.run {
                for eff in completedSharedFiles {
                    onCompleted(eff, !visibleSessionID.isEmpty && eff.sessionID == visibleSessionID)
                }
            }
        }
    }

    private static func isDemoAllowedTimer(_ kind: PBCoreTimerKind) -> Bool {
        switch kind {
        case .sharedFileReconnect,
             .attachmentUploadPoll,
             .attachmentUploadReconnect,
             .draftExpiry,
             .terminalHistoryTimeout:
            return true
        default:
            return false
        }
    }

    // MARK: - Attachment I/O Executors (guaranteed op_id replies)

    private static func runCancellableDetached<T: Sendable>(
        priority: TaskPriority = .userInitiated,
        operation: @escaping @Sendable (_ preCancelled: Bool) async -> T
    ) async -> T {
        let preCancelled = Task.isCancelled
        let worker = Task.detached(priority: priority) {
            await operation(preCancelled)
        }
        if preCancelled { worker.cancel() }
        return await withTaskCancellationHandler {
            await worker.value
        } onCancel: {
            worker.cancel()
        }
    }

    private func executePrepareImage(_ eff: PBAttachmentPrepareImageEffect) async {
        let result = await runPrepareImage(eff)
        await dispatch { $0.attachmentPrepareImageResult = result }
    }

    private func runPrepareImage(_ eff: PBAttachmentPrepareImageEffect) async -> PBAttachmentPrepareImageResultEvent {
        let store = self.pendingStore
        return await Self.runCancellableDetached { preCancelled in
            var ev = PBAttachmentPrepareImageResultEvent.with {
                $0.opID = eff.opID
                $0.scope = eff.scope
                $0.outboxRequestID = eff.outboxRequestID
                $0.itemID = eff.itemID
            }
            var readingSource = true
            var storedPreparedRef: String?
            var storedThumbRef: String?
            do {
                if preCancelled { throw CancellationError() }
                try Task.checkCancellation()
                let raw = try store.readBlob(eff.sourceRef)
                readingSource = false
                try Task.checkCancellation()
                let prepared = try await prepareImage(
                    raw,
                    original: eff.original,
                    originalMaxBytes: eff.maxOriginalBytes,
                    maxDimensionPx: Int(eff.maxDimensionPx),
                    maxCompressedBytes: Int(eff.maxCompressedBytes)
                )
                try Task.checkCancellation()
                let digest = Data(SHA256.hash(data: prepared.bytes))
                let prepRef = try store.storeBlob(prepared.bytes, prefix: "prep")
                storedPreparedRef = prepRef
                var thumbRef = ""
                if let thumbBytes = thumbnailData(prepared.bytes, maxLong: 256) {
                    thumbRef = try store.storeBlob(thumbBytes, prefix: "thumb")
                    storedThumbRef = thumbRef
                }
                ev.ok = true
                ev.preparedRef = prepRef
                ev.thumbRef = thumbRef
                ev.format = prepared.format
                ev.width = Int32(prepared.width)
                ev.height = Int32(prepared.height)
                ev.sizeBytes = Int64(prepared.bytes.count)
                ev.sha256 = digest
            } catch {
                var toClean: [String] = []
                if let r = storedPreparedRef { toClean.append(r) }
                if let r = storedThumbRef { toClean.append(r) }
                if !toClean.isEmpty { store.releaseLocal(refs: toClean) }
                ev.ok = false
                // Locked/unavailable Keychain is a transient storage failure, not a corrupt image decode.
                ev.isReadingSource = readingSource && !(error is VaultError)
                ev.errorKind = error is CancellationError ? .other : attachmentIOErrorKind(for: error)
                ev.errorDetail = PendingStore.asciiErrorDetail(error)
            }
            return ev
        }
    }

    private func executeHashFile(_ eff: PBAttachmentHashFileEffect) async {
        let result = await runHashFile(eff)
        await dispatch { $0.attachmentHashFileResult = result }
    }

    private func runHashFile(_ eff: PBAttachmentHashFileEffect) async -> PBAttachmentHashFileResultEvent {
        let store = self.pendingStore
        return await Self.runCancellableDetached { preCancelled in
            var ev = PBAttachmentHashFileResultEvent.with {
                $0.opID = eff.opID
                $0.scope = eff.scope
                $0.outboxRequestID = eff.outboxRequestID
                $0.itemID = eff.itemID
            }
            do {
                if preCancelled { throw CancellationError() }
                try Task.checkCancellation()
                let ref = eff.sourceRef
                if ref.hasPrefix("bmk:") {
                    let bmk = try store.readBookmark(ref)
                    let hashed = try PickedFile.hashBookmark(bmk, maxBytes: eff.maxBytes)
                    if let refreshed = hashed.refreshedBookmark {
                        try? store.updateBookmark(ref, bookmark: refreshed)
                    }
                    ev.ok = true
                    ev.sizeBytes = hashed.sizeBytes
                    ev.sha256 = hashed.sha256
                    if eff.isVideo {
                        let dur = await Self.probeVideoDurationMs(at: hashed.url, securityScoped: true)
                        if dur > 0 {
                            ev.hasDuration_p = true
                            ev.durationMs = dur
                        }
                    }
                } else if ref.hasPrefix("blob:") {
                    let hashed = try store.hashBlob(ref, maxBytes: eff.maxBytes)
                    ev.ok = true
                    ev.sizeBytes = hashed.sizeBytes
                    ev.sha256 = hashed.sha256
                } else {
                    throw AttachmentFileIOError(.sourceMissing)
                }
            } catch {
                ev.ok = false
                ev.errorKind = error is CancellationError ? .other : attachmentIOErrorKind(for: error)
                ev.errorDetail = PendingStore.asciiErrorDetail(error)
            }
            return ev
        }
    }

    private func executeReadChunk(_ eff: PBAttachmentReadChunkEffect) async {
        let result = await runReadChunk(eff)
        await dispatch { $0.attachmentReadChunkResult = result }
    }

    private func runReadChunk(_ eff: PBAttachmentReadChunkEffect) async -> PBAttachmentReadChunkResultEvent {
        let store = self.pendingStore
        return await Self.runCancellableDetached { preCancelled in
            var ev = PBAttachmentReadChunkResultEvent.with {
                $0.opID = eff.opID
                $0.scope = eff.scope
                $0.outboxRequestID = eff.outboxRequestID
                $0.itemID = eff.itemID
                $0.offset = eff.offset
            }
            do {
                if preCancelled { throw CancellationError() }
                try Task.checkCancellation()
                let ref: String
                switch eff.kind {
                case .image:
                    ref = eff.preparedRef
                case .file:
                    ref = eff.sourceRef
                case .unspecified, .UNRECOGNIZED:
                    throw AttachmentFileIOError(.sourceMissing)
                }
                let data: Data
                if ref.hasPrefix("blob:") {
                    data = try store.readBlobChunk(
                        ref,
                        offset: eff.offset,
                        length: Int(eff.length),
                        expectedSizeBytes: eff.expectedSizeBytes
                    )
                } else if ref.hasPrefix("bmk:") {
                    let bmk = try store.readBookmark(ref)
                    data = try PickedFile.readBookmarkChunk(
                        bmk,
                        offset: eff.offset,
                        length: Int(eff.length),
                        expectedSizeBytes: eff.expectedSizeBytes
                    )
                } else {
                    throw AttachmentFileIOError(.sourceMissing)
                }
                ev.ok = true
                ev.data = data
            } catch {
                ev.ok = false
                ev.errorKind = error is CancellationError ? .other : attachmentIOErrorKind(for: error)
                ev.errorDetail = PendingStore.asciiErrorDetail(error)
            }
            return ev
        }
    }

    #if DEBUG
    func prepareImageForTesting(_ eff: PBAttachmentPrepareImageEffect) async -> PBAttachmentPrepareImageResultEvent {
        await runPrepareImage(eff)
    }

    func hashFileForTesting(_ eff: PBAttachmentHashFileEffect) async -> PBAttachmentHashFileResultEvent {
        await runHashFile(eff)
    }

    func readChunkForTesting(_ eff: PBAttachmentReadChunkEffect) async -> PBAttachmentReadChunkResultEvent {
        await runReadChunk(eff)
    }
    #endif

    static func probeVideoDurationMs(at url: URL, securityScoped: Bool) async -> Int64 {
        let scoped = securityScoped ? url.startAccessingSecurityScopedResource() : false
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }
        let asset = AVURLAsset(url: url)
        guard let duration = try? await asset.load(.duration) else { return 0 }
        let seconds = CMTimeGetSeconds(duration)
        guard seconds.isFinite && seconds > 0 else { return 0 }
        return Int64((seconds * 1000).rounded())
    }

    private func handleDispatchCommand(_ c: PBCoreDispatchCommandEffect) async {
        if let cmd = try? PBCommand(serializedBytes: c.commandBytes) {
            #if DEBUG
            if demoMode, let action = demoCommandResponder?(c, cmd) {
                switch action {
                case .hold:
                    return
                case .respond(let outcome):
                    await dispatch { $0.commandOutcome = outcome }
                    return
                case .asyncRespond(let make):
                    spawnIO { host in
                        let outcome = await make()
                        await host.dispatch { $0.commandOutcome = outcome }
                    }
                    return
                }
            }
            #endif
            if demoMode, case .listModels(let req)? = cmd.kind {
                let key = "\(c.machineID)/\(req.agent.rawValue)"
                let models = demoModels[key] ?? demoModels.first(where: { $0.key.hasSuffix("/\(req.agent.rawValue)") })?.value ?? PBListModelsResponse()
                let outcome = Self.demoExecutedOutcome(for: c) { $0.listModels = models }
                await dispatch { $0.commandOutcome = outcome }
                return
            }
            if let demoDirectories, case .listDirectories(let req)? = cmd.kind {
                spawnIO { host in
                    do {
                        let list = try await demoDirectories.list(c.machineID, req.path)
                        let outcome = Self.demoExecutedOutcome(for: c) { $0.listDirectories = list }
                        await host.dispatch { $0.commandOutcome = outcome }
                    } catch {
                        await host.dispatch {
                            $0.commandOutcome = .with {
                                $0.requestID = c.requestID; $0.attemptID = c.attemptID
                                $0.source = .local; $0.notForwarded = true
                                $0.transportCode = "unavailable"; $0.transportDetail = errorText(error)
                            }
                        }
                    }
                }
                return
            }
            if demoMode, case .schedule(let req)? = cmd.kind, req.operation == "list" {
                let json = demoSchedules[c.machineID] ?? Data("{\"tasks\":[]}".utf8)
                let outcome = Self.demoExecutedOutcome(for: c) { $0.schedule.resultJson = json }
                await dispatch { $0.commandOutcome = outcome }
                return
            }
            #if DEBUG
            if demoMode, case .getMachineStatus? = cmd.kind {
                let mode = await MachineStatusDemo.mode
                if mode == "loading" { return }
                demoMachineStatusCalls += 1
                if mode == "failed" || (mode == "stale" && demoMachineStatusCalls > 2) {
                    await dispatch {
                        $0.commandOutcome = .with {
                            $0.requestID = c.requestID; $0.attemptID = c.attemptID
                            $0.source = .local; $0.notForwarded = true
                            $0.transportCode = CommandError.unavailable
                            $0.transportDetail = L10n.machineStatusUnavailable()
                        }
                    }
                    return
                }
                let status = MachineStatusDemo.status(mode: mode, now: self.clock())
                let outcome = Self.demoExecutedOutcome(for: c) { $0.machineStatus = status }
                await dispatch { $0.commandOutcome = outcome }
                return
            }
            if demoMode, case .getSettings? = cmd.kind {
                let demoState = SettingsDemo.state
                if demoState == "loading" { return }
                if demoState == "failed" {
                    await dispatch {
                        $0.commandOutcome = .with {
                            $0.requestID = c.requestID; $0.attemptID = c.attemptID
                            $0.source = .local; $0.notForwarded = true
                            $0.transportCode = CommandError.unavailable
                            $0.transportDetail = L10n.errorsTheComputerIsOfflineTheCommandWasNotSent()
                        }
                    }
                    return
                }
                let settings = self.demoDaemonSettings ?? SettingsDemo.daemonSettings
                let storage = SettingsDemo.imageStorage
                let outcome = Self.demoExecutedOutcome(for: c) {
                    $0.getSettings = .with {
                        $0.settings = settings
                        $0.imageStorage = storage
                    }
                }
                await dispatch { $0.commandOutcome = outcome }
                return
            }
            if demoMode, case .updateSettings(let req)? = cmd.kind {
                let demoState = SettingsDemo.state
                if demoState == "saveFailed" {
                    await dispatch {
                        $0.commandOutcome = .with {
                            $0.requestID = c.requestID; $0.attemptID = c.attemptID
                            $0.source = .local; $0.notForwarded = true
                            $0.transportCode = CommandError.unavailable
                            $0.transportDetail = L10n.errorsTheComputerIsOfflineTheCommandWasNotSent()
                        }
                    }
                    return
                }
                if demoState == "saveUnknown" {
                    await dispatch {
                        $0.commandOutcome = .with {
                            $0.requestID = c.requestID; $0.attemptID = c.attemptID
                            $0.source = .daemon; $0.attemptBound = true
                            $0.outerOutcome = "unknown"; $0.outerReason = "timeout"
                        }
                    }
                    return
                }
                self.demoDaemonSettings = req.settings
                let storage = SettingsDemo.imageStorage
                let outcome = Self.demoExecutedOutcome(for: c) {
                    $0.updateSettings = .with {
                        $0.settings = req.settings
                        $0.imageStorage = storage
                    }
                }
                await dispatch { $0.commandOutcome = outcome }
                return
            }
            if demoMode, case .getUsageStats(let req)? = cmd.kind {
                let demoState = SettingsDemo.state
                if demoState == "usageLoading" { return }
                if demoState == "usageFailed" {
                    await dispatch {
                        $0.commandOutcome = .with {
                            $0.requestID = c.requestID; $0.attemptID = c.attemptID
                            $0.source = .local; $0.notForwarded = true
                            $0.transportCode = CommandError.unavailable
                            $0.transportDetail = L10n.errorsTheComputerIsOfflineTheCommandWasNotSent()
                        }
                    }
                    return
                }
                let stats = SettingsDemo.usageStats(request: req, machineID: c.machineID, now: self.clock())
                let outcome = Self.demoExecutedOutcome(for: c) { $0.usageStats = stats }
                await dispatch { $0.commandOutcome = outcome }
                return
            }
            #endif
            if demoMode {
                await dispatch { $0.commandOutcome = Self.demoExecutedOutcome(for: c) }
                return
            }
        }

        spawnIO { host in
            let outcome = await host.executor.dispatchCommandAttempt(c)
            await host.dispatch { $0.commandOutcome = outcome }
        }
    }

    // MARK: - Render & Screen View Fetching

    private func renderTargets(_ targets: Set<PBCoreScreenTarget>) async {
        guard !closed else { return }
        var homeVM: PBCoreHomeViewModel?
        var projectVM: PBCoreProjectViewModel?
        var newSessionVM: PBCoreNewSessionViewModel?
        var syncStateVM: PBCoreSyncStateView?
        var schedulesVM: PBSchedulesViewModel?
        var scheduleDetailVM: PBScheduleDetailViewModel?
        var scheduleFormVM: PBScheduleFormViewModel?
        var settingsVM: PBSettingsViewModel?
        var machineStatusVM: PBMachineStatusViewModel?
        var machineSettingsVM: PBMachineSettingsViewModel?
        var quotaVM: PBQuotaViewModel?
        var usageVM: PBCoreUsageViewModel?
        var devicesVM: PBDevicesViewModel?
        var clientDetailVM: PBClientDetailViewModel?
        var directDiagnosticsVM: PBDirectDiagnosticsViewModel?
        var dirPickerVMs: [String: PBCoreDirPickerView] = [:]
        var sessionVMs: [String: PBSessionViewModel] = [:]
        var scheduleStripVMs: [String: PBScheduleSessionStripViewModel] = [:]

        if targets.contains(.home) {
            homeVM = try? client.view(PBCoreScreen.with {
                $0.home = .with { $0.endedLimit = 50 }
            }).home
        }
        if targets.contains(.project) {
            let key = activeProjectKey
            let manage = manageSubscribers > 0
            if !key.isEmpty || manage {
                projectVM = try? client.view(PBCoreScreen.with {
                    $0.project = .with {
                        $0.projectKey = key
                        $0.manage = manage
                        $0.endedLimit = 50
                        $0.machineID = activeManageMachineID
                    }
                }).project
            }
            for pickerKey in Set(activeDirPickers.values) {
                if let vm = try? client.view(PBCoreScreen.with {
                    $0.project = .with {
                        $0.manage = true
                        $0.endedLimit = 50
                        $0.machineID = activeManageMachineID
                        $0.pickerKey = pickerKey
                    }
                }).project, vm.hasManage {
                    dirPickerVMs[pickerKey] = vm.manage.dirPicker
                }
            }
        }
        if targets.contains(.newSession) || (targets.contains(.attachmentDraft) && !activeNewSessionProjectKey.isEmpty) {
            newSessionVM = try? client.view(PBCoreScreen.with {
                $0.newSession = .with { $0.projectKey = activeNewSessionProjectKey }
            }).newSession
        }
        if targets.contains(.schedules) {
            schedulesVM = try? client.view(PBCoreScreen.with {
                $0.schedules = PBSchedulesScreenRequest()
            }).schedules
        }
        if targets.contains(.scheduleDetail), let active = activeScheduleDetail {
            scheduleDetailVM = try? client.view(PBCoreScreen.with {
                $0.scheduleDetail = .with {
                    $0.machineID = active.machineID
                    $0.taskID = active.taskID
                }
            }).scheduleDetail
        }
        if targets.contains(.scheduleForm) {
            scheduleFormVM = try? client.view(PBCoreScreen.with {
                $0.scheduleForm = .with { $0.formKey = activeScheduleFormKey }
            }).scheduleForm
        }
        if targets.contains(.scheduleSessionStrip) {
            for sid in Set(scheduleStripWatches.values) {
                if let vm = try? client.view(PBCoreScreen.with {
                    $0.scheduleSessionStrip = .with { $0.sessionID = sid }
                }).scheduleSessionStrip {
                    scheduleStripVMs[sid] = vm
                }
            }
        }
        if targets.contains(.syncState) {
            syncStateVM = try? client.view(PBCoreScreen.with {
                $0.syncState = PBCoreSyncStateScreenRequest()
            }).syncState
        }
        if targets.contains(.settings) || targets.contains(.home) || targets.contains(.syncState) {
            settingsVM = try? client.view(PBCoreScreen.with {
                $0.settings = PBSettingsScreenRequest()
            }).settings
        }
        if targets.contains(.devices) {
            devicesVM = try? client.view(PBCoreScreen.with {
                $0.devices = PBDevicesScreenRequest()
            }).devices
        }
        if targets.contains(.machineStatus), !activeMachineStatusID.isEmpty {
            machineStatusVM = try? client.view(PBCoreScreen.with {
                $0.machineStatus = .with { $0.machineID = activeMachineStatusID }
            }).machineStatus
        }
        if targets.contains(.machineSettings), !activeMachineSettingsID.isEmpty {
            machineSettingsVM = try? client.view(PBCoreScreen.with {
                $0.machineSettings = .with { $0.machineID = activeMachineSettingsID }
            }).machineSettings
        }
        if targets.contains(.quota), !activeQuotaMachineID.isEmpty {
            quotaVM = try? client.view(PBCoreScreen.with {
                $0.quota = .with {
                    $0.machineID = activeQuotaMachineID
                    $0.filterAgent = activeQuotaFilterAgent
                }
            }).quota
        }
        if targets.contains(.usage), !usageMachineID.isEmpty {
            let clamped = usagePreset == .custom
                ? Self.clampUsageCustomRange(startMs: usageCustomStartMs, endMs: usageCustomEndMs, nowMs: clock())
                : (startMs: Int64(0), endMs: Int64(0))
            usageVM = try? client.view(PBCoreScreen.with {
                $0.usage = .with {
                    $0.machineID = usageMachineID
                    $0.focusAgent = usageFocusAgent
                    $0.focusModel = usageFocusModel
                    $0.preset = usagePreset
                    $0.customStartMs = clamped.startMs
                    $0.customEndMs = clamped.endMs
                }
            }).usage
        }
        if targets.contains(.clientDetail), !activeClientDetailID.isEmpty {
            clientDetailVM = try? client.view(PBCoreScreen.with {
                $0.clientDetail = .with { $0.deviceID = activeClientDetailID }
            }).clientDetail
        }
        if targets.contains(.directDiagnostics), !activeDirectDiagnosticsID.isEmpty {
            directDiagnosticsVM = try? client.view(PBCoreScreen.with {
                $0.directDiagnostics = .with { $0.machineID = activeDirectDiagnosticsID }
            }).directDiagnostics
        }
        let refreshSessions = targets.contains(.session)
            || targets.contains(.sessionData)
            || (targets.contains(.attachmentDraft) && !sessionWatches.isEmpty)
            || (targets.contains(.home) && !sessionWatches.isEmpty)
        if refreshSessions {
            let watchedIDs = Set(sessionWatches.values.map(\.sessionID))
            for sid in watchedIDs {
                guard let window = effectiveSessionWindow(for: sid) else { continue }
                if let vm = try? client.view(PBCoreScreen.with {
                    $0.session = .with {
                        $0.sessionID = sid
                        $0.window = window
                    }
                }).session, !vm.sessionID.isEmpty {
                    sessionVMs[sid] = vm
                }
            }
        }

        let seq = nextRenderSeq()

        await MainActor.run { [
            screens, seq, homeVM, syncStateVM, projectVM, dirPickerVMs, newSessionVM,
            schedulesVM, scheduleDetailVM, scheduleFormVM,
            settingsVM, machineStatusVM, machineSettingsVM, quotaVM, usageVM, devicesVM, clientDetailVM,
            directDiagnosticsVM, sessionVMs, scheduleStripVMs
        ] in
            if let homeVM, screens.acceptRender(.home, seq: seq) { screens.home = homeVM }
            if let syncStateVM, screens.acceptRender(.syncState, seq: seq) { screens.syncState = syncStateVM }
            if let projectVM {
                if projectVM.hasDetail, screens.acceptRender(.projectDetail, seq: seq) { screens.projectDetail = projectVM.detail }
                if projectVM.hasManage, screens.acceptRender(.manageProjects, seq: seq) { screens.manageProjects = projectVM.manage }
            }
            for (pk, vm) in dirPickerVMs where screens.acceptRender(.dirPicker(pk), seq: seq) {
                screens.dirPickers[pk] = vm
            }
            if let newSessionVM, screens.acceptRender(.newSession, seq: seq) { screens.newSession = newSessionVM }
            if let schedulesVM, screens.acceptRender(.schedules, seq: seq) { screens.schedules = schedulesVM }
            if let scheduleDetailVM, screens.acceptRender(.scheduleDetail, seq: seq) { screens.scheduleDetail = scheduleDetailVM }
            if let scheduleFormVM, screens.acceptRender(.scheduleForm, seq: seq) { screens.scheduleForm = scheduleFormVM }
            if let settingsVM, screens.acceptRender(.settings, seq: seq) { screens.settings = settingsVM }
            if let machineStatusVM, screens.acceptRender(.machineStatus, seq: seq) { screens.machineStatus = machineStatusVM }
            if let machineSettingsVM, screens.acceptRender(.machineSettings, seq: seq) { screens.machineSettings = machineSettingsVM }
            if let quotaVM, screens.acceptRender(.quota, seq: seq) { screens.quota = quotaVM }
            if let usageVM, screens.acceptRender(.usageView, seq: seq) { screens.usageView = usageVM }
            if let devicesVM, screens.acceptRender(.devices, seq: seq) { screens.devices = devicesVM }
            if let clientDetailVM, screens.acceptRender(.clientDetail, seq: seq) { screens.clientDetail = clientDetailVM }
            if let directDiagnosticsVM, screens.acceptRender(.directDiagnostics, seq: seq) { screens.directDiagnostics = directDiagnosticsVM }
            for (sid, vm) in sessionVMs where screens.acceptRender(.session(sid), seq: seq) {
                screens.sessions[sid] = vm
            }
            for (sid, vm) in scheduleStripVMs where screens.acceptRender(.scheduleStrip(sid), seq: seq) {
                screens.scheduleStrips[sid] = vm
            }
        }

        if let syncStateVM, seq >= lastAppliedSyncStateSeq {
            lastAppliedSyncStateSeq = seq
            await executor.applyCoreSyncState(syncStateVM)
            await maybeDescribeSelf(syncStateVM)
        }
    }

    private func effectiveSessionWindow(for sid: String) -> PBSessionWindowRequest? {
        let entries = sessionWatchOrder.compactMap { sessionWatches[$0] }.filter { $0.sessionID == sid }
        guard !entries.isEmpty else { return nil }
        var merged = PBSessionWindowRequest()
        var maxLimit: Int32 = 0
        var expanded: [String] = []
        var seenExpanded = Set<String>()
        for entry in entries {
            let w = entry.window
            if w.limit > maxLimit { maxLimit = w.limit }
            if w.beforeSeq > 0 { merged.beforeSeq = w.beforeSeq }
            if w.aroundSeq > 0 { merged.aroundSeq = w.aroundSeq }
            if w.revealSeq > 0 { merged.revealSeq = w.revealSeq }
            if !w.revealRunID.isEmpty { merged.revealRunID = w.revealRunID }
            for gid in w.expandedGroupIds where seenExpanded.insert(gid).inserted {
                expanded.append(gid)
            }
        }
        merged.limit = maxLimit > 0 ? maxLimit : 80
        merged.expandedGroupIds = expanded
        return merged
    }

    private func refreshSessionViews(for sessionIDs: Set<String>) async {
        guard !closed else { return }
        var updates: [String: PBSessionViewModel] = [:]
        for sid in sessionIDs {
            guard let window = effectiveSessionWindow(for: sid) else { continue }
            if let vm = try? client.view(PBCoreScreen.with {
                $0.session = .with {
                    $0.sessionID = sid
                    $0.window = window
                }
            }).session, !vm.sessionID.isEmpty {
                updates[sid] = vm
            }
        }
        guard !updates.isEmpty else { return }
        let seq = nextRenderSeq()
        await MainActor.run { [screens, seq, updates] in
            for (sid, vm) in updates where screens.acceptRender(.session(sid), seq: seq) {
                screens.sessions[sid] = vm
            }
        }
    }

    // MARK: - Clock, Timers, Entropy, Task Tracking

    private func startClockTicker() {
        clockTask?.cancel()
        clockTask = Task { [weak self] in
            while let self, !Task.isCancelled {
                try? await self.sleep(60_000)
                guard !Task.isCancelled else { return }
                await self.dispatchClockUpdated()
            }
        }
    }

    private func dispatchClockUpdated() async {
        let nowMs = clock()
        let cal = Self.makeLocalCalendar(nowMs: nowMs)
        let sched = Self.makeScheduleLocalContext(nowMs: nowMs)
        await dispatch {
            $0.clockUpdated = cal
            $0.scheduleLocalContext = sched
        }
    }

    static func makeScheduleLocalContext(nowMs: Int64, timeZone: TimeZone = .current) -> PBScheduleLocalContextEvent {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = timeZone
        let now = Date(timeIntervalSince1970: Double(nowMs) / 1000)
        let today = cal.startOfDay(for: now)
        let tomorrow = cal.date(byAdding: .day, value: 1, to: today) ?? today
        let dayAfterTomorrow = cal.date(byAdding: .day, value: 2, to: today) ?? tomorrow
        return PBScheduleLocalContextEvent.with {
            $0.localTimezone = timeZone.identifier
            $0.tomorrowStartMs = Int64(tomorrow.timeIntervalSince1970 * 1000)
            $0.dayAfterTomorrowStartMs = Int64(dayAfterTomorrow.timeIntervalSince1970 * 1000)
        }
    }

    static func makeLocalCalendar(nowMs: Int64) -> PBCoreLocalCalendar {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = .current
        cal.firstWeekday = 2 // Monday-start week matching clientcore
        let now = Date(timeIntervalSince1970: Double(nowMs) / 1000)
        let today = cal.startOfDay(for: now)
        let yesterday = cal.date(byAdding: .day, value: -1, to: today) ?? today
        let weekComponents = cal.dateComponents([.yearForWeekOfYear, .weekOfYear], from: today)
        let weekStart = cal.date(from: weekComponents) ?? today
        let nowYear = Int32(cal.component(.year, from: now))

        // Populate ~48 past months + next month so historical date headers resolve in local time.
        var months: [PBCoreMonthStart] = []
        months.reserveCapacity(50)
        if let thisMonthStart = cal.date(from: cal.dateComponents([.year, .month], from: today)) {
            for offset in -48...1 {
                if let monthDate = cal.date(byAdding: .month, value: offset, to: thisMonthStart) {
                    let comps = cal.dateComponents([.year, .month], from: monthDate)
                    months.append(PBCoreMonthStart.with {
                        $0.year = Int32(comps.year ?? Int(nowYear))
                        $0.month = Int32(comps.month ?? 1)
                        $0.startMs = Int64(monthDate.timeIntervalSince1970 * 1000)
                    })
                }
            }
        }

        return PBCoreLocalCalendar.with {
            $0.nowMs = nowMs
            $0.todayStartMs = Int64(today.timeIntervalSince1970 * 1000)
            $0.yesterdayStartMs = Int64(yesterday.timeIntervalSince1970 * 1000)
            $0.weekStartMs = Int64(weekStart.timeIntervalSince1970 * 1000)
            $0.nowYear = nowYear
            $0.months = months
        }
    }

    private static func randomBytes(_ count: Int) -> Data {
        let n = max(count, 1)
        var data = Data(count: n)
        let rc = data.withUnsafeMutableBytes { buf in
            SecRandomCopyBytes(kSecRandomDefault, n, buf.baseAddress!)
        }
        if rc == errSecSuccess {
            return data
        }
        var rng = SystemRandomNumberGenerator()
        return Data((0..<n).map { _ in UInt8.random(in: UInt8.min...UInt8.max, using: &rng) })
    }

    private func replenishEntropy(minIds: Int, minFloats: Int) {
        while randomIdPool.count < minIds {
            randomIdPool.append(newRequestId())
        }
        while unitFloatPool.count < minFloats {
            unitFloatPool.append(Double.random(in: 0..<1))
        }
    }

    private func scheduleTimer(timerId: UInt64, kind: PBCoreTimerKind, key: String, delayMs: Int64) {
        let mapKey = TimerKey(kind: kind, key: key)
        if let existing = timers[mapKey] {
            existing.task.cancel()
        }
        let sleep = self.sleep
        let task = Task { [weak self] in
            if delayMs > 0 {
                try? await sleep(delayMs)
            }
            guard let self, !Task.isCancelled else { return }
            await self.handleTimerFired(timerId: timerId, kind: kind, key: key)
        }
        timers[mapKey] = ScheduledTimer(id: timerId, task: task)
    }

    private func cancelTimer(timerId: UInt64, kind: PBCoreTimerKind, key: String) {
        let mapKey = TimerKey(kind: kind, key: key)
        guard let existing = timers[mapKey] else { return }
        if timerId == 0 || existing.id == timerId {
            existing.task.cancel()
            timers.removeValue(forKey: mapKey)
        }
    }

    private func handleTimerFired(timerId: UInt64, kind: PBCoreTimerKind, key: String) async {
        let mapKey = TimerKey(kind: kind, key: key)
        guard let existing = timers[mapKey], existing.id == timerId else { return }
        timers.removeValue(forKey: mapKey)
        guard !closed else { return }
        await dispatch {
            $0.timerFired = .with {
                $0.timerID = timerId
                $0.kind = kind
                $0.key = key
            }
        }
    }

    private func cancelAllTimers() {
        for entry in timers.values {
            entry.task.cancel()
        }
        timers.removeAll()
    }

    private func scheduleMachinePoll(_ eff: PBScheduleMachinePollEffect) {
        let id = eff.machineID
        if eff.cancel || id.isEmpty {
            machinePollTimers.removeValue(forKey: id)?.task.cancel()
            return
        }
        machinePollTimers.removeValue(forKey: id)?.task.cancel()
        let gen = eff.generation
        let delayMs = max(0, eff.delayMs)
        let sleep = self.sleep
        let task = Task { [weak self] in
            if delayMs > 0 {
                try? await sleep(delayMs)
            }
            guard let self, !Task.isCancelled else { return }
            await self.handleMachinePollFired(machineID: id, generation: gen)
        }
        machinePollTimers[id] = ScheduledTimer(id: gen, task: task)
    }

    private func handleMachinePollFired(machineID: String, generation: UInt64) async {
        guard let existing = machinePollTimers[machineID], existing.id == generation else { return }
        machinePollTimers.removeValue(forKey: machineID)
        guard !closed else { return }
        await dispatch {
            $0.machineStatusPollTick = .with {
                $0.machineID = machineID
                $0.generation = generation
            }
        }
    }

    private func cancelAllMachinePollTimers() {
        for entry in machinePollTimers.values {
            entry.task.cancel()
        }
        machinePollTimers.removeAll()
    }

    private func spawnIO(_ work: @escaping @Sendable (CoreHost) async -> Void) {
        let id = UUID()
        let task = Task { [weak self] in
            guard let self else { return }
            await work(self)
            await self.finishIOTask(id)
        }
        ioTasks[id] = task
    }

    private func finishIOTask(_ id: UUID) {
        ioTasks.removeValue(forKey: id)
    }

    private func cancelIOTasks() {
        for task in ioTasks.values {
            task.cancel()
        }
        ioTasks.removeAll()
    }

    private func enqueueCacheOp(_ work: @escaping @Sendable (any CoreHostExecutor) async -> Void) {
        let previous = cacheChain
        let executor = self.executor
        cacheChain = Task {
            await previous?.value
            guard !Task.isCancelled else { return }
            await work(executor)
        }
    }

    private func adjustedDemoSession(_ s: PBCorePlainSession) -> PBCorePlainSession {
        var copy = s
        copy.lastSeq = max(s.lastSeq, demoEvents[s.sessionID]?.last?.seq ?? 0)
        return copy
    }

    private static func plainEvents(_ list: [PBEvent]) -> [PBCorePlainEvent] {
        list.map { ev in
            PBCorePlainEvent.with { $0.seq = ev.seq; $0.event = ev; $0.verified = true; $0.approxBytes = 512 }
        }
    }

    private func demoCacheReadReply(_ r: PBCoreCacheReadEffect) -> PBCoreEvent? {
        switch r.kind {
        case .bootstrap(let b):
            return .with {
                $0.cacheBootstrapLoaded = .with {
                    $0.opID = b.opID
                    $0.machines = Array(demoWorld.machines.values)
                    $0.sessions = demoWorld.sessions.values.map(adjustedDemoSession)
                    $0.prefs = demoWorld.prefs
                }
            }
        case .events(let e):
            return .with {
                $0.cacheEventsLoaded = .with {
                    $0.opID = e.opID
                    $0.sessionID = e.sessionID
                    $0.events = []
                }
            }
        case nil:
            return nil
        }
    }

    private func demoRpcReply(_ r: PBCoreRpcEffect) -> PBCoreEvent? {
        switch r.kind {
        case .fetchLists:
            return .with {
                $0.listsFetched = .with {
                    $0.opID = r.opID
                    $0.machines = Array(demoWorld.machines.values)
                    $0.sessions = demoWorld.sessions.values.map(adjustedDemoSession)
                    $0.devices = Array(demoWorld.devices.values)
                }
            }
        case .getAccountPrefs:
            return .with {
                $0.accountPrefsFetched = .with {
                    $0.opID = r.opID
                    $0.prefs = demoWorld.prefs
                }
            }
        case .fetchSessionByID(let s):
            return .with {
                $0.sessionByIDFetched = .with {
                    $0.opID = r.opID
                    if let sess = demoWorld.sessions[s.sessionID] {
                        $0.session = adjustedDemoSession(sess)
                    } else {
                        $0.error = "not_found"
                    }
                }
            }
        case .getEvents(let g):
            let all = demoEvents[g.sessionID] ?? []
            let remaining = all.filter { $0.seq > g.afterSeq }
            let limit = g.limit > 0 ? Int(g.limit) : remaining.count
            let page = Array(remaining.prefix(limit))
            return .with {
                $0.eventsPageFetched = .with {
                    $0.opID = r.opID
                    $0.sessionID = g.sessionID
                    $0.events = Self.plainEvents(page)
                    $0.more = remaining.count > page.count
                }
            }
        case .fetchDevices:
            return .with {
                $0.devicesFetched = .with {
                    $0.opID = r.opID
                    $0.devices = Array(demoWorld.devices.values)
                }
            }
        case .fetchSessionsPage:
            return .with {
                $0.sessionsPageFetched = .with {
                    $0.opID = r.opID
                }
            }
        case .putAccountPrefs, nil:
            return nil
        }
    }

    nonisolated static func demoExecutedOutcome(
        for c: PBCoreDispatchCommandEffect,
        fill: (inout PBCommandResult) -> Void = { _ in }
    ) -> PBCoreCommandOutcomeEvent {
        var result = PBCommandResult()
        result.outcome = "executed"; result.reason = "ok"; result.resultAvailable = true
        fill(&result)
        return .with {
            $0.requestID = c.requestID; $0.attemptID = c.attemptID
            $0.source = .daemon; $0.attemptBound = true
            $0.hashesMatch = true; $0.outerMatchesInner = true; $0.signed = true
            $0.outerOutcome = "executed"; $0.outerReason = "ok"
            $0.result = result
        }
    }

    #if DEBUG
    func currentDemoWorld() -> DemoWorld { demoWorld }
    func isDemoSeeded() -> Bool { demoLegID > 0 }

    private func stepDemo(_ build: (inout PBCoreEvent) -> Void) async -> [PBCoreEffect] {
        guard !closed else { return [] }
        var envelope = PBCoreEvent()
        build(&envelope)
        envelope.nowMs = clock(); envelope.monoMs = monoClock()
        if envelope.randomIds.isEmpty && !randomIdPool.isEmpty {
            envelope.randomIds = randomIdPool; randomIdPool.removeAll(keepingCapacity: true)
        }
        if envelope.unitFloats.isEmpty && !unitFloatPool.isEmpty {
            envelope.unitFloats = unitFloatPool; unitFloatPool.removeAll(keepingCapacity: true)
        }
        replenishEntropy(minIds: 8, minFloats: 8)
        guard let batch = try? client.dispatch(envelope) else { return [] }
        enqueueSharedFileStorageEffects(batch.effects)
        enqueueDraftStorageEffects(batch.effects)
        await handleEffects(batch.effects)
        return batch.effects
    }

    private func mergeDemo(world shown: DemoWorld, events: [String: [PBEvent]]) {
        demoWorld.connection = shown.connection
        demoWorld.removed = shown.removed
        demoWorld.recoveryPending = shown.recoveryPending
        demoWorld.recoveryReadyMs = shown.recoveryReadyMs
        demoWorld.leaseEndMs = shown.leaseEndMs
        demoWorld.clockSkewed = shown.clockSkewed
        if !shown.authorityDeviceId.isEmpty { demoWorld.authorityDeviceId = shown.authorityDeviceId }
        if !shown.error.isEmpty { demoWorld.error = shown.error }
        if shown.prefs.revision > 0 || shown.prefs.hasPrefs { demoWorld.prefs = shown.prefs }
        for (id, m) in shown.machines { demoWorld.machines[id] = m }
        for (id, s) in shown.sessions { demoWorld.sessions[id] = s }
        for (id, d) in shown.devices { demoWorld.devices[id] = d }
        for (sid, list) in events { demoEvents[sid] = list }
    }

    private func demoSecuritySeed() -> PBDemoSecurity {
        .with {
            $0.isMember = !demoWorld.removed
            $0.removed = demoWorld.removed
            $0.authorityDeviceID = demoWorld.authorityDeviceId
            $0.trustedMachineIds = demoWorld.machines.keys.sorted()
            $0.trustedClientIds = demoWorld.devices.values.filter { $0.kind == .client }.map(\.deviceID)
            $0.archiveMachineIds = demoWorld.machines.keys.sorted()
            $0.headEpoch = 1
            $0.leaseEndMs = demoWorld.leaseEndMs
            $0.recoveryPending = demoWorld.recoveryPending
            $0.recoveryReadyMs = demoWorld.recoveryReadyMs
            $0.recoveryObjectsPending = false
            $0.clockSkewed = demoWorld.clockSkewed
            $0.selfDeviceID = self.account.deviceId
        }
    }

    /// Populates Core directly from an in-memory `DemoWorld` for `-demo` / preview launches.
    func seedDemo(
        world shown: DemoWorld,
        events: [String: [PBEvent]] = [:],
        schedules: [String: Data] = [:],
        scheduleSeen: [String: [PBScheduleSeenToken]] = [:]
    ) async {
        guard !closed else { return }
        demoMode = true; started = true; foregrounded = true
        demoWorld = shown
        for (sid, list) in events { demoEvents[sid] = list }
        for (mid, data) in schedules { demoSchedules[mid] = data }
        for (mid, tokens) in scheduleSeen { demoScheduleSeen[mid] = tokens }
        var tokenOpID: UInt64 = 0
        func scanPre(_ effects: [PBCoreEffect]) {
            for eff in effects {
                if case .ensureToken(let t)? = eff.effect { tokenOpID = t.opID }
            }
        }
        scanPre(await stepDemo {
            $0.entropyProvided = .with {
                $0.randomIds = (0..<16).map { _ in newRequestId() }
                $0.unitFloats = (0..<16).map { _ in Double.random(in: 0..<1) }
                $0.randomBytes = Self.randomBytes(4096)
            }
        })
        scanPre(await stepDemo {
            $0.transportConfig = .with {
                $0.supported = false
                $0.account = account.accountId
                $0.protocolVersion = UInt32(peerProtocolVersion)
                $0.platformAgent = "ios"
            }
        })
        scanPre(await stepDemo { $0.syncLifecycle = .with { $0.kind = .start; $0.scopeHmacKey = self.scopeHmacKey } })
        if !attachmentConfigured {
            attachmentConfigured = true
            scanPre(await stepDemo {
                $0.attachmentConfig = .with {
                    $0.persistDrafts = false
                    $0.persistOutbox = false
                    $0.scanStorage = false
                    $0.maxChunkBytes = 0
                    $0.hasPersistedPreparedImages_p = true
                }
            })
        }
        scanPre(await stepDemo {
            $0.sharedFileConfig = .with {
                $0.localCacheMaxBytes = 0
                $0.storageFullKind = .phoneStorageFull
                $0.scanLocal = SharedFileDebug.fake
            }
        })
        scanPre(await stepDemo { $0.clockUpdated = Self.makeLocalCalendar(nowMs: clock()) })
        scanPre(await stepDemo { $0.scheduleLocalContext = Self.makeScheduleLocalContext(nowMs: clock()) })
        scanPre(await stepDemo { $0.localeChanged = .with { $0.uiLocale = MessageLocale.current } })
        scanPre(await stepDemo { $0.networkChanged = .with { $0.signature = "demo-wifi"; $0.available = true } })
        let secSeed = demoSecuritySeed()
        _ = await stepDemo { $0.seedDemoWorld.security = secSeed }
        var legID: UInt64 = 0
        if tokenOpID > 0 {
            let effs = await stepDemo {
                $0.tokenResult = .with { $0.opID = tokenOpID; $0.token = "demo-token"; $0.expiresMs = clock() + 3_600_000 }
            }
            legID = effs.compactMap { if case .openStream(let s)? = $0.effect { return s.legID }; return nil }.last ?? 0
        }
        demoLegID = legID
        if legID > 0 {
            // `streamReady` triggers `fetchLists` & `getAccountPrefs` RPCs, answered by `demoRpcReply`.
            _ = await stepDemo { $0.streamReady = .with { $0.legID = legID; $0.serverTimeMs = self.clock() } }
        }
        switch demoWorld.connection {
        case .noNetwork:
            _ = await stepDemo { $0.networkChanged = .with { $0.signature = "none"; $0.available = false } }
        case .offline:
            let dropEffs = await stepDemo { $0.streamClosed = .with { $0.legID = legID; $0.unresolvedHost = true; $0.error = self.demoWorld.error } }
            for eff in dropEffs {
                if case .scheduleTimer(let t)? = eff.effect, t.kind == .linkGrace {
                    _ = await stepDemo { $0.timerFired = .with { $0.timerID = t.timerID; $0.kind = t.kind; $0.key = t.key } }
                }
            }
        case .connecting, .reconnecting:
            _ = await stepDemo { $0.streamClosed = .with { $0.legID = legID } }
        case .connected, .unspecified, .UNRECOGNIZED:
            break
        }
        await renderTargets([.syncState, .home, .project, .newSession, .session, .schedules, .settings, .devices, .machineSettings, .quota, .clientDetail])
        if !activeMachineSettingsID.isEmpty {
            await openMachineSettings(machineID: activeMachineSettingsID)
        }
    }

    func setDemoCommandResponder(
        _ responder: (@Sendable (PBCoreDispatchCommandEffect, PBCommand) -> DemoCommandAction?)?
    ) {
        demoCommandResponder = responder
    }

    func appendDemoSessionData(
        world shown: DemoWorld = DemoWorld(),
        events: [String: [PBEvent]] = [:],
        modelLists: [(machineId: String, agent: PBAgent, models: PBListModelsResponse)] = []
    ) async {
        guard !closed else { return }
        demoMode = true
        for m in modelLists {
            demoModels["\(m.machineId)/\(m.agent.rawValue)"] = m.models
        }
        let previousEvents = demoEvents
        mergeDemo(world: shown, events: events)
        guard demoLegID > 0 else { return }
        let secSeed = demoSecuritySeed()
        _ = await stepDemo { $0.seedDemoWorld.security = secSeed }
        for m in shown.machines.values {
            _ = await stepDemo {
                $0.streamFrame = .with {
                    $0.legID = self.demoLegID
                    $0.machineUpdated = m
                }
            }
        }
        for s in shown.sessions.values.map(adjustedDemoSession) {
            _ = await stepDemo {
                $0.streamFrame = .with {
                    $0.legID = self.demoLegID
                    $0.sessionUpdated = s
                }
            }
        }
        for d in shown.devices.values {
            _ = await stepDemo {
                $0.streamFrame = .with {
                    $0.legID = self.demoLegID
                    $0.deviceUpdated = d
                }
            }
        }
        if shown.prefs.revision > 0 {
            _ = await stepDemo {
                $0.streamFrame = .with {
                    $0.legID = self.demoLegID
                    $0.accountPrefsUpdated = shown.prefs
                }
            }
        }
        for (sid, list) in events where !list.isEmpty {
            let prevMax = previousEvents[sid]?.last?.seq ?? 0
            let added = list.filter { $0.seq > prevMax }
            if !added.isEmpty {
                _ = await stepDemo {
                    $0.streamFrame = .with {
                        $0.legID = self.demoLegID
                        $0.eventsAppended = .with {
                            $0.sessionID = sid
                            $0.events = Self.plainEvents(added)
                        }
                    }
                }
            }
        }
        await renderTargets([.syncState, .home, .project, .newSession, .session, .schedules, .settings, .devices, .machineSettings, .quota, .clientDetail])
        if !activeMachineSettingsID.isEmpty {
            await openMachineSettings(machineID: activeMachineSettingsID)
        }
    }
    #endif
}
