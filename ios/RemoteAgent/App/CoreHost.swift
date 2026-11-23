// Pure M2 Executor & bridge between SwiftUI screens (`CoreScreens`) and Go `CoreClient`.
// Owns CoreClient, host clock/calendar, entropy, single-shot timers, stream & RPC
// task execution, and sequential CipherCache persistence. Zero sync rules live here.
import Foundation
import Observation
import RACore
import RAProto
import Security

/// Main-actor observable snapshots published by `CoreHost` for the migrated screens.
@MainActor
@Observable
final class CoreScreens {
    struct RestoredComposer: Identifiable, Equatable, Sendable {
        let id = UUID()
        let effect: PBSessionRestoreComposerEffect
    }

    struct ResendAttachments: Identifiable, Equatable, Sendable {
        let id = UUID()
        let effect: PBSessionResendAttachmentsEffect
    }

    struct PendingCleanup: Identifiable, Equatable, Sendable {
        let id = UUID()
        let effect: PBPrepareCleanupSessionEffect
    }

    var home = PBCoreHomeViewModel()
    var projectDetail = PBCoreProjectDetailView()
    var manageProjects = PBCoreManageProjectsView()
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
    var pendingResendAttachments: [ResendAttachments] = []
    var pendingCleanup: PendingCleanup?

    func consumeComposerRestores(_ ids: Set<UUID>) {
        guard !ids.isEmpty else { return }
        pendingComposerRestores.removeAll { ids.contains($0.id) }
    }

    func consumeResendAttachments(_ ids: Set<UUID>) {
        guard !ids.isEmpty else { return }
        pendingResendAttachments.removeAll { ids.contains($0.id) }
    }

    func consumeCleanup(_ id: UUID) {
        if pendingCleanup?.id == id {
            pendingCleanup = nil
        }
    }
}

/// Captures MainActor-owned attachment references when a new-session start is submitted.
@MainActor
struct NewSessionAttachmentsSubmission {
    var draftKey: String
    var machineId: String?
    var taken: [PendingImage]
    var imageIds: [String]
    var fileIds: [String]
    var center: ImageCenter?

    func protect(requestId: String) {
        guard let machineId else { return }
        for img in taken {
            img.protectUpload(machineId: machineId, transferId: img.transferId, requestId: requestId)
        }
    }

    func release(requestId: String) {
        for img in taken {
            img.releaseUploadProtection(requestId: requestId)
        }
    }

    func markUsed() {
        guard !taken.isEmpty else { return }
        center?.used(draftKey, taken, machineId: machineId)
    }
}

protocol CoreHostExecutor: AnyObject, Sendable {
    var account: Account { get }
    var state: Feed<RemoteState> { get }
    var directSupported: Bool { get }
    var directServerHost: String { get }
    var directConfiguredStun: String { get }
    func installCoreHooks(_ hooks: Remote.CoreHooks) async
    func publishSyncState(_ snapshot: RemoteState) async
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

/// Owns `CoreClient`, feeds system/UI/transport events into Core, and executes every
/// `Effect` emitted by Core (`OpenStream`, `EnsureToken`, `SyncSecurity`, `ScheduleTimer`,
/// `CacheRead/Write/Delete`, `Rpc`, `DispatchCommand`, `Render`, `Navigate`, `AttachmentsSettled`,
/// `SaveHomeScope`, `RequestEntropy`).
actor CoreHost: SyncEngine {
    private struct TimerKey: Hashable {
        let kind: PBCoreTimerKind
        let key: String
    }

    private struct ScheduledTimer {
        let id: UInt64
        let task: Task<Void, Never>
    }

    private let client: CoreClient
    private let executor: any CoreHostExecutor
    private let screens: CoreScreens
    private let defaults: ScopeDefaults
    private let scopeHmacKey: Data
    private let clock: @Sendable () -> Int64
    private let monoClock: @Sendable () -> Int64
    private let sleep: Sleeper
    private var demoDirectories: DirectorySource?
    private var demoModels: [String: PBListModelsResponse]
    private var demoState = RemoteState()
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
    private var machineStatusWatches: [UUID: MachineStatusWatchEntry] = [:]
    private var activeDirectDiagnosticsID = ""
    private var hooksInstalled = false
    private var nextDirectCallID: UInt64 = 1
    private var bulkRouteWaiters: [UInt64: CheckedContinuation<PBCoreDirectBulkRouteEffect, Never>] = [:]
    private var nextSecurityOpID: UInt64 = 1
    private var securityOpWaiters: [UInt64: CheckedContinuation<PBSecurityOpResultEffect, Never>] = [:]
    private var retryNowWaiters: [String: [CheckedContinuation<Bool, Never>]] = [:]
    private var terminalReopenListeners: [String: (token: UUID, handler: @MainActor @Sendable () -> Void)] = [:]
    private var syncStateRenderNeeded = false
    private var onPersistClientPrefs: (@MainActor @Sendable (PBPersistClientPrefsEffect) -> Void)?
    #if DEBUG
    enum DemoCommandAction: Sendable {
        case hold
        case respond(PBCoreCommandOutcomeEvent)
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

    private var queuedAttachments: [String: NewSessionAttachmentsSubmission] = [:]
    private var protectedStarts: [String: NewSessionAttachmentsSubmission] = [:]

    nonisolated var account: Account { executor.account }
    nonisolated var state: Feed<RemoteState> { executor.state }

    static func live(
        remote: Remote,
        account: Account,
        defaults: UserDefaults,
        screens: CoreScreens,
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
            onFetchSession: { [weak self] sid in
                await self?.dispatch { $0.fetchSession.sessionID = sid }
            },
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
                    $0.errorCode = "storage"
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

    func describeSelf(platform: String, description: String) async throws {
        try await executor.describeSelf(platform: platform, description: description)
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
        await dispatchClockUpdated()
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
                $0.errorCode = "storage"
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
                $0.errorCode = "storage"
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
                $0.errorCode = "storage"
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
        await MainActor.run { [screens] in
            screens.directDiagnostics = PBDirectDiagnosticsViewModel()
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
            await MainActor.run { [screens] in
                _ = screens.scheduleStrips.removeValue(forKey: sid)
            }
        }
    }

    func submitNewSession(
        projectKey: String,
        prompt: String,
        attachmentCount: Int32,
        attachmentsMoved: Bool,
        autoModeSupported: Bool,
        attachments: NewSessionAttachmentsSubmission?
    ) async {
        guard !closed else { return }
        if let attachments {
            queuedAttachments[projectKey] = attachments
        }
        await dispatch {
            $0.newSessionSetPrompt = .with {
                $0.prompt = prompt
                $0.attachmentCount = attachmentCount
                $0.attachmentsMoved = attachmentsMoved
            }
        }
        await dispatch {
            $0.newSessionSubmit = .with {
                $0.projectKey = projectKey
                $0.autoModeSupported = autoModeSupported
                $0.imageIds = attachments?.imageIds ?? []
                $0.fileIds = attachments?.fileIds ?? []
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
            await handleEffects(batch.effects)
        } catch {
            // Core rejects malformed events; keep host loop alive.
        }
    }

    private func handleEffects(_ effects: [PBCoreEffect]) async {
        var renderTargets: Set<PBCoreScreenTarget> = []
        var restoredComposers: [PBSessionRestoreComposerEffect] = []
        var resendAttachments: [PBSessionResendAttachmentsEffect] = []
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
                guard !demoMode else { continue }
                scheduleTimer(timerId: t.timerID, kind: t.kind, key: t.key, delayMs: t.delayMs)

            case .cancelTimer(let t):
                guard !demoMode else { continue }
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

            case .attachmentsSettled(let a):
                await handleAttachmentsSettled(a)

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

            case .sessionResendAttachments(let r):
                resendAttachments.append(r)

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

            case nil:
                break
            }
        }
        if !restoredComposers.isEmpty || !resendAttachments.isEmpty {
            await MainActor.run { [screens, restoredComposers, resendAttachments] in
                for r in restoredComposers {
                    screens.pendingComposerRestores.append(CoreScreens.RestoredComposer(effect: r))
                }
                for r in resendAttachments {
                    screens.pendingResendAttachments.append(CoreScreens.ResendAttachments(effect: r))
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
    }

    private func handleDispatchCommand(_ c: PBCoreDispatchCommandEffect) async {
        // Track attachment upload protection when Core emits CreateSession.
        if let cmd = try? PBCommand(serializedBytes: c.commandBytes),
           case .createSession? = cmd.kind,
           let submission = queuedAttachments.removeValue(forKey: activeNewSessionProjectKey)
            ?? queuedAttachments.values.first
        {
            protectedStarts[c.requestID] = submission
            await MainActor.run {
                submission.protect(requestId: c.requestID)
            }
        }

        if let cmd = try? PBCommand(serializedBytes: c.commandBytes) {
            #if DEBUG
            if demoMode, let action = demoCommandResponder?(c, cmd) {
                switch action {
                case .hold:
                    return
                case .respond(let outcome):
                    await dispatch { $0.commandOutcome = outcome }
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

    private func handleAttachmentsSettled(_ a: PBCoreAttachmentsSettledEffect) async {
        let submission = protectedStarts.removeValue(forKey: a.requestID)
            ?? queuedAttachments.removeValue(forKey: a.projectKey)
        guard let submission else { return }
        let consumed = a.consumed
        let reqId = a.requestID
        await MainActor.run {
            if consumed {
                submission.markUsed()
            }
            submission.release(requestId: reqId)
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
        }
        if targets.contains(.newSession) {
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
        if !demoMode && targets.contains(.syncState) {
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

        if let syncStateVM {
            let adapted = SyncStateAdapter.toRemoteState(syncStateVM)
            await executor.publishSyncState(adapted)
        }

        await MainActor.run { [
            screens, homeVM, projectVM, newSessionVM,
            schedulesVM, scheduleDetailVM, scheduleFormVM,
            settingsVM, machineStatusVM, machineSettingsVM, quotaVM, usageVM, devicesVM, clientDetailVM,
            directDiagnosticsVM, sessionVMs, scheduleStripVMs
        ] in
            if let homeVM { screens.home = homeVM }
            if let projectVM {
                if projectVM.hasDetail { screens.projectDetail = projectVM.detail }
                if projectVM.hasManage { screens.manageProjects = projectVM.manage }
            }
            if let newSessionVM { screens.newSession = newSessionVM }
            if let schedulesVM { screens.schedules = schedulesVM }
            if let scheduleDetailVM { screens.scheduleDetail = scheduleDetailVM }
            if let scheduleFormVM { screens.scheduleForm = scheduleFormVM }
            if let settingsVM { screens.settings = settingsVM }
            if let machineStatusVM { screens.machineStatus = machineStatusVM }
            if let machineSettingsVM { screens.machineSettings = machineSettingsVM }
            if let quotaVM { screens.quota = quotaVM }
            if let usageVM { screens.usageView = usageVM }
            if let devicesVM { screens.devices = devicesVM }
            if let clientDetailVM { screens.clientDetail = clientDetailVM }
            if let directDiagnosticsVM { screens.directDiagnostics = directDiagnosticsVM }
            for (sid, vm) in sessionVMs {
                screens.sessions[sid] = vm
            }
            for (sid, vm) in scheduleStripVMs {
                screens.scheduleStrips[sid] = vm
            }
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
        await MainActor.run { [screens, updates] in
            for (sid, vm) in updates {
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

    private static func plainMachines(_ state: RemoteState) -> [PBCorePlainMachine] {
        state.machines.values.map { m in
            PBCorePlainMachine.with {
                $0.machineID = m.id; $0.online = m.online; $0.lastSeenMs = m.lastSeenMs
                if let meta = m.meta { $0.meta = meta }; $0.metaRev = m.metaRev
            }
        }
    }

    private func plainSession(_ s: SessionView) -> PBCorePlainSession {
        let maxSeq = max(s.lastSeq, demoEvents[s.id]?.last?.seq ?? 0)
        return PBCorePlainSession.with {
            $0.sessionID = s.id; $0.machineID = s.machineId
            if let info = s.info { $0.meta = info }; $0.metaRev = s.metaRev
            $0.lastSeq = maxSeq; $0.updatedMs = s.updatedMs; $0.listedEndedMs = s.listedEndedMs
            if let cp = s.checkpoint { $0.checkpoint = cp }
        }
    }

    private func plainSessions(_ state: RemoteState) -> [PBCorePlainSession] {
        state.sessions.values.map(plainSession)
    }

    private static func plainDevices(_ state: RemoteState) -> [PBCorePlainDevice] {
        state.devices.values.map { d in
            PBCorePlainDevice.with {
                $0.deviceID = d.id; $0.kind = d.kind; $0.createdMs = d.createdMs
                $0.lastSeenMs = d.lastSeenMs; if let meta = d.meta { $0.meta = meta }; $0.metaRev = d.metaRev
            }
        }
    }

    private static func plainEvents(_ list: [PBEvent]) -> [PBCorePlainEvent] {
        list.map { ev in
            PBCorePlainEvent.with { $0.seq = ev.seq; $0.event = ev; $0.verified = true; $0.approxBytes = 512 }
        }
    }

    private func demoCacheReadReply(_ r: PBCoreCacheReadEffect) -> PBCoreEvent? {
        switch r.kind {
        case .bootstrap(let b):
            let p = demoState.prefs ?? PrefsObject(revision: 0, prefs: PBProjectPrefs())
            return .with {
                $0.cacheBootstrapLoaded = .with {
                    $0.opID = b.opID
                    $0.machines = Self.plainMachines(demoState)
                    $0.sessions = plainSessions(demoState)
                    $0.prefs = .with { $0.revision = p.revision; $0.prefs = p.prefs }
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
                    $0.machines = Self.plainMachines(demoState)
                    $0.sessions = plainSessions(demoState)
                    $0.devices = Self.plainDevices(demoState)
                }
            }
        case .getAccountPrefs:
            let p = demoState.prefs ?? PrefsObject(revision: 0, prefs: PBProjectPrefs())
            return .with {
                $0.accountPrefsFetched = .with {
                    $0.opID = r.opID
                    $0.prefs = .with { $0.revision = p.revision; $0.prefs = p.prefs }
                }
            }
        case .fetchSessionByID(let s):
            return .with {
                $0.sessionByIDFetched = .with {
                    $0.opID = r.opID
                    if let sess = demoState.sessions[s.sessionID] {
                        $0.session = plainSession(sess)
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
                    $0.devices = Self.plainDevices(demoState)
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
        await handleEffects(batch.effects)
        return batch.effects
    }

    private func mergeDemo(state shown: RemoteState, events: [String: [PBEvent]]) {
        demoState.conn = shown.conn
        demoState.link = shown.link
        demoState.removed = shown.removed
        demoState.recoveryPending = shown.recoveryPending
        demoState.recoveryReadyMs = shown.recoveryReadyMs
        demoState.leaseEndMs = shown.leaseEndMs
        if !shown.authorityDeviceId.isEmpty { demoState.authorityDeviceId = shown.authorityDeviceId }
        if !shown.clockWarning.isEmpty { demoState.clockWarning = shown.clockWarning }
        if !shown.error.isEmpty { demoState.error = shown.error }
        if let prefs = shown.prefs { demoState.prefs = prefs }
        for (id, m) in shown.machines { demoState.machines[id] = m }
        for (id, s) in shown.sessions { demoState.sessions[id] = s }
        for (id, d) in shown.devices { demoState.devices[id] = d }
        for (sid, list) in events { demoEvents[sid] = list }
    }

    /// Populates Core from an in-memory `RemoteState` for `-demo` / preview launches.
    func seedDemo(
        state shown: RemoteState,
        events: [String: [PBEvent]] = [:],
        schedules: [String: Data] = [:],
        scheduleSeen: [String: [PBScheduleSeenToken]] = [:]
    ) async {
        guard !closed else { return }
        demoMode = true; started = true; foregrounded = true
        mergeDemo(state: shown, events: events)
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
        scanPre(await stepDemo { $0.clockUpdated = Self.makeLocalCalendar(nowMs: clock()) })
        scanPre(await stepDemo { $0.scheduleLocalContext = Self.makeScheduleLocalContext(nowMs: clock()) })
        scanPre(await stepDemo { $0.networkChanged = .with { $0.signature = "demo-wifi"; $0.available = true } })
        _ = await stepDemo {
            $0.seedDemoWorld.security = .with {
                $0.isMember = !demoState.removed
                $0.removed = demoState.removed
                $0.authorityDeviceID = demoState.authorityDeviceId
                $0.trustedMachineIds = demoState.machines.keys.sorted()
                $0.trustedClientIds = demoState.devices.values.filter { $0.kind == .client }.map(\.id)
                $0.archiveMachineIds = demoState.machines.keys.sorted()
                $0.headEpoch = 1
                $0.leaseEndMs = Int64(demoState.leaseEndMs)
                $0.recoveryPending = demoState.recoveryPending
                $0.recoveryReadyMs = Int64(demoState.recoveryReadyMs)
                $0.recoveryObjectsPending = false
                $0.clockSkewed = !demoState.clockWarning.isEmpty
                $0.selfDeviceID = self.account.deviceId
            }
        }
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
        switch demoState.link {
        case .noNetwork:
            _ = await stepDemo { $0.networkChanged = .with { $0.signature = "none"; $0.available = false } }
        case .noServer:
            let dropEffs = await stepDemo { $0.streamClosed = .with { $0.legID = legID; $0.unresolvedHost = true; $0.error = self.demoState.error } }
            for eff in dropEffs {
                if case .scheduleTimer(let t)? = eff.effect, t.kind == .linkGrace {
                    _ = await stepDemo { $0.timerFired = .with { $0.timerID = t.timerID; $0.kind = t.kind; $0.key = t.key } }
                }
            }
        case .waiting:
            _ = await stepDemo { $0.streamClosed = .with { $0.legID = legID } }
        case .online:
            break
        }
        await renderTargets([.home, .project, .newSession, .session, .schedules, .settings, .devices, .machineSettings, .quota, .clientDetail])
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
        state shown: RemoteState,
        events: [String: [PBEvent]] = [:],
        modelLists: [(machineId: String, agent: PBAgent, models: PBListModelsResponse)] = []
    ) async {
        guard !closed else { return }
        demoMode = true
        for m in modelLists {
            demoModels["\(m.machineId)/\(m.agent.rawValue)"] = m.models
        }
        let previousEvents = demoEvents
        mergeDemo(state: shown, events: events)
        guard demoLegID > 0 else { return }
        _ = await stepDemo {
            $0.seedDemoWorld.security = .with {
                $0.isMember = !self.demoState.removed
                $0.removed = self.demoState.removed
                $0.authorityDeviceID = self.demoState.authorityDeviceId
                $0.trustedMachineIds = self.demoState.machines.keys.sorted()
                $0.trustedClientIds = self.demoState.devices.values.filter { $0.kind == .client }.map(\.id)
                $0.archiveMachineIds = self.demoState.machines.keys.sorted()
                $0.headEpoch = 1
                $0.leaseEndMs = Int64(self.demoState.leaseEndMs)
                $0.recoveryPending = self.demoState.recoveryPending
                $0.recoveryReadyMs = Int64(self.demoState.recoveryReadyMs)
                $0.clockSkewed = !self.demoState.clockWarning.isEmpty
                $0.selfDeviceID = self.account.deviceId
            }
        }
        for m in Self.plainMachines(shown) {
            _ = await stepDemo {
                $0.streamFrame = .with {
                    $0.legID = self.demoLegID
                    $0.machineUpdated = m
                }
            }
        }
        for s in plainSessions(shown) {
            _ = await stepDemo {
                $0.streamFrame = .with {
                    $0.legID = self.demoLegID
                    $0.sessionUpdated = s
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
        await renderTargets([.home, .project, .newSession, .session, .schedules, .settings, .devices, .machineSettings, .quota, .clientDetail])
        if !activeMachineSettingsID.isEmpty {
            await openMachineSettings(machineID: activeMachineSettingsID)
        }
    }
    #endif
}
