// Pure M2 Executor & bridge between SwiftUI screens (`CoreScreens`) and Go `CoreClient`.
// Owns CoreClient, host clock/calendar, entropy, single-shot timers, stream & RPC
// task execution, and sequential CipherCache persistence. Zero sync rules live here.
import Foundation
import Observation
import RACore
import RAProto

/// Main-actor observable snapshots published by `CoreHost` for the migrated screens.
@MainActor
@Observable
final class CoreScreens {
    var home = PBCoreHomeViewModel()
    var projectDetail = PBCoreProjectDetailView()
    var manageProjects = PBCoreManageProjectsView()
    var newSession = PBCoreNewSessionViewModel()
    var project = PBCoreProjectViewModel()
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
    func installCoreHooks(_ hooks: Remote.CoreHooks) async
    func publishSyncState(_ snapshot: RemoteState) async
    func publishSessionData(_ sid: String, events: SessionEvents) async
    func setNetworkGeneration(_ gen: UInt64) async
    func ensureToken(opId: UInt64, forceLogin: Bool, timeoutMs: Int64) async -> PBCoreTokenResultEvent
    func discardToken(_ rejected: String) async
    func syncSecuritySnapshot(opId: UInt64, networkGeneration: UInt64, touch: Bool) async -> PBCoreSecuritySnapshotEvent
    func openStream(legId: UInt64, token: String, callbacks: Remote.StreamCallbacks) async
    func closeStream(legId: UInt64) async
    func executeCacheRead(_ effect: PBCoreCacheReadEffect) async -> PBCoreEvent
    func executeCacheWrite(_ effect: PBCoreCacheWriteEffect) async
    func executeCacheDelete(_ effect: PBCoreCacheDeleteEffect, onSessionRemoved: (@Sendable (String) async -> Void)?) async
    func executeRpc(_ effect: PBCoreRpcEffect) async -> PBCoreEvent
    func dispatchCommandAttempt(_ effect: PBCoreDispatchCommandEffect) async -> PBCoreCommandOutcomeEvent
    func applyTransitionalPrefsOp(_ op: PrefsOp) async throws
    func describeSelf(platform: String, description: String) async throws
    func close() async
}

extension Remote: CoreHostExecutor {}

private let monotonicOrigin = ContinuousClock.now
private func hostMonoMs() -> Int64 {
    let parts = monotonicOrigin.duration(to: .now).components
    return parts.seconds * 1000 + parts.attoseconds / 1_000_000_000_000_000
}

struct ScopeDefaults: @unchecked Sendable {
    let raw: UserDefaults
    init(_ raw: UserDefaults = .standard) { self.raw = raw }
    func homeScopeToken() -> String { raw.string(forKey: AccountDefaults.key("homeScopeToken")) ?? "" }
    func setHomeScopeToken(_ token: String) { raw.set(token, forKey: AccountDefaults.key("homeScopeToken")) }
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
    private var onNavigate: @MainActor @Sendable (PBCoreNavigateEffect) -> Void
    private var onSessionRemoved: (@Sendable (String) async -> Void)?

    private var closed = false
    private var demoMode: Bool
    private var started = false
    private var foregrounded = false
    private var networkGen: UInt64 = 0
    private var lastNetworkAvailable = true
    private var lastNetworkSignature = ""

    private var activeProjectKey = ""
    private var activeNewSessionProjectKey = ""
    private var manageSubscribers = 0
    private var watchedSessionID = ""
    private var syncStateRenderNeeded = false

    private var randomIdPool: [String] = []
    private var unitFloatPool: [Double] = []
    private var timers: [TimerKey: ScheduledTimer] = [:]
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
        onNavigate: @escaping @MainActor @Sendable (PBCoreNavigateEffect) -> Void
    ) throws -> CoreHost {
        let client = try CoreClient()
        let host = CoreHost(
            client: client,
            executor: remote,
            screens: screens,
            defaults: ScopeDefaults(defaults),
            scopeHmacKey: RACore.scopeHmacKey(root: account.root),
            clock: clock,
            monoClock: hostMonoMs,
            sleep: sleep,
            demoDirectories: demoDirectories,
            demoModels: demoModels,
            demoMode: demoMode,
            onNavigate: onNavigate
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
        onNavigate: @escaping @MainActor @Sendable (PBCoreNavigateEffect) -> Void = { _ in }
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
        self.randomIdPool = (0..<16).map { _ in newRequestId() }
        self.unitFloatPool = (0..<16).map { _ in Double.random(in: 0..<1) }
    }

    func wireExecutorHooks() async {
        await executor.installCoreHooks(Remote.CoreHooks(
            onWatchSession: { [weak self] sid, watched in
                await self?.watchSession(sid, watched: watched)
            },
            onRetryEvents: { [weak self] sid in
                await self?.dispatch { $0.retrySessionEvents.sessionID = sid }
            },
            onFetchSession: { [weak self] sid in
                await self?.dispatch { $0.fetchSession.sessionID = sid }
            },
            onRefresh: { [weak self] in
                await self?.reconnectNowAsync()
            },
            onRefreshDevices: { [weak self] in
                await self?.syncSecurityNow()
            },
            onSavePrefs: { [weak self] op in
                guard let self else { return }
                try await self.executor.applyTransitionalPrefsOp(op)
            },
            onDirectStatus: { [weak self] machineId, ready in
                await self?.dispatch {
                    $0.machineRouteStatus = .with {
                        $0.machineID = machineId
                        $0.directReady = ready
                    }
                }
            },
            onLoadMoreSessions: { [weak self] in
                await self?.dispatch {
                    $0.homeLoadMoreEnded = PBCoreHomeLoadMoreEndedAction()
                }
            }
        ))
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
        started = true
        foregrounded = true
        startClockTicker()
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
        cancelIOTasks()
        await dispatch {
            $0.syncLifecycle.kind = .stop
        }
    }

    func setForeground(_ foreground: Bool) async {
        guard !closed, started else { return }
        foregrounded = foreground
        if foreground {
            await dispatchClockUpdated()
        }
        await dispatch {
            $0.syncLifecycle.kind = foreground ? .foreground : .background
        }
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

    // Unrequested snapshots carry a push sequence taken before the sync
    // starts, so the core drops one that finishes after a newer one.
    private var securityPushSeq: UInt64 = 1

    private func syncSecurityNow() async {
        guard !closed else { return }
        let seq = securityPushSeq
        securityPushSeq += 1
        var snap = await executor.syncSecuritySnapshot(opId: 0, networkGeneration: networkGen, touch: false)
        snap.pushSeq = seq
        await dispatch { $0.securitySnapshot = snap }
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
        cancelIOTasks()
        cacheChain?.cancel()
        cacheChain = nil
        await executor.close()
        client.close()
    }

    // MARK: - Screen & Subscription Events

    func setNavigateHandler(_ handler: @escaping @MainActor @Sendable (PBCoreNavigateEffect) -> Void) {
        onNavigate = handler
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

    func setManageActive(_ active: Bool) async {
        guard !closed else { return }
        manageSubscribers = max(0, manageSubscribers + (active ? 1 : -1))
        if active {
            await renderTargets([.project])
        }
    }

    func watchSession(_ sid: String, watched: Bool) async {
        guard !closed, !sid.isEmpty else { return }
        if watched {
            watchedSessionID = sid
        } else if watchedSessionID == sid {
            watchedSessionID = ""
        }
        await dispatch {
            $0.sessionWatch = .with {
                $0.sessionID = sid
                $0.watched = watched
            }
        }
        if watched && watchedSessionID == sid {
            await renderTargets([.sessionData])
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

            case .syncSecurity(let s):
                guard !demoMode else { continue }
                spawnIO { host in
                    let snap = await host.executor.syncSecuritySnapshot(
                        opId: s.opID,
                        networkGeneration: s.networkGeneration,
                        touch: s.touch
                    )
                    await host.dispatch { $0.securitySnapshot = snap }
                }

            case .scheduleTimer(let t):
                guard !demoMode else { continue }
                scheduleTimer(timerId: t.timerID, kind: t.kind, key: t.key, delayMs: t.delayMs)

            case .cancelTimer(let t):
                guard !demoMode else { continue }
                cancelTimer(timerId: t.timerID, kind: t.kind, key: t.key)

            case .cacheRead(let r):
                guard !demoMode else { continue }
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
                guard !demoMode else { continue }
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
                await dispatch {
                    $0.entropyProvided = .with {
                        $0.randomIds = ids
                        $0.unitFloats = floats
                    }
                }

            // Settings, machine, schedules and session screens still render
            // in Swift on iOS and never send the events behind these effects.
            case .scheduleMachinePoll, .directPreference, .renameClient,
                 .checkUpdates, .persistClientPrefs, .prepareCleanupSession,
                 .sessionRestoreComposer, .scheduleReadCache, .scheduleDeleteCache,
                 .scheduleLoadSeen, .scheduleSaveSeen, .scheduleNavigate,
                 .scheduleDismissNotifications:
                break

            case nil:
                break
            }
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
        var sessionDataVM: PBCoreSessionDataView?

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
                    }
                }).project
            }
        }
        if targets.contains(.newSession) {
            newSessionVM = try? client.view(PBCoreScreen.with {
                $0.newSession = .with { $0.projectKey = activeNewSessionProjectKey }
            }).newSession
        }
        if !demoMode && targets.contains(.syncState) {
            syncStateVM = try? client.view(PBCoreScreen.with {
                $0.syncState = PBCoreSyncStateScreenRequest()
            }).syncState
        }
        if !demoMode && targets.contains(.sessionData), !watchedSessionID.isEmpty {
            let sid = watchedSessionID
            sessionDataVM = try? client.view(PBCoreScreen.with {
                $0.sessionData = .with { $0.sessionID = sid }
            }).sessionData
        }

        if let syncStateVM {
            let adapted = SyncStateAdapter.toRemoteState(syncStateVM)
            await executor.publishSyncState(adapted)
        }
        if let sessionDataVM, !sessionDataVM.sessionID.isEmpty {
            let converted = SyncStateAdapter.toSessionEvents(sessionDataVM)
            await executor.publishSessionData(sessionDataVM.sessionID, events: converted)
        }

        await MainActor.run { [screens, homeVM, projectVM, newSessionVM] in
            if let homeVM { screens.home = homeVM }
            if let projectVM {
                screens.project = projectVM
                if projectVM.hasDetail { screens.projectDetail = projectVM.detail }
                if projectVM.hasManage { screens.manageProjects = projectVM.manage }
            }
            if let newSessionVM { screens.newSession = newSessionVM }
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
        let cal = Self.makeLocalCalendar(nowMs: clock())
        await dispatch { $0.clockUpdated = cal }
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

    private nonisolated static func demoExecutedOutcome(
        for c: PBCoreDispatchCommandEffect,
        fill: (inout PBCommandResult) -> Void
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

    /// Populates Core from an in-memory `RemoteState` for `-demo` / preview launches.
    func seedDemo(state shown: RemoteState, events: [String: [PBEvent]] = [:]) async {
        guard !closed else { return }
        demoMode = true; started = true; foregrounded = true
        var secOpID: UInt64 = 0, tokenOpID: UInt64 = 0
        func scanPre(_ effects: [PBCoreEffect]) {
            for eff in effects {
                if case .syncSecurity(let s)? = eff.effect { secOpID = s.opID }
                if case .ensureToken(let t)? = eff.effect { tokenOpID = t.opID }
            }
        }
        scanPre(await stepDemo { $0.syncLifecycle = .with { $0.kind = .start; $0.scopeHmacKey = self.scopeHmacKey } })
        scanPre(await stepDemo { $0.clockUpdated = Self.makeLocalCalendar(nowMs: clock()) })
        scanPre(await stepDemo { $0.networkChanged = .with { $0.signature = "demo-wifi"; $0.available = true } })
        scanPre(await stepDemo {
            $0.securitySnapshot = .with {
                $0.opID = secOpID > 0 ? secOpID : 1; $0.isMember = !shown.removed
                $0.authorityDeviceID = shown.authorityDeviceId
                $0.trustedMachineIds = shown.machines.keys.sorted()
                $0.trustedClientIds = shown.devices.values.filter { $0.kind == .client }.map(\.id)
                $0.leaseEndMs = Int64(shown.leaseEndMs); $0.recoveryPending = shown.recoveryPending
                $0.recoveryReadyMs = Int64(shown.recoveryReadyMs); $0.recoveryObjectsPending = false
                $0.clockSkewed = !shown.clockWarning.isEmpty; $0.selfDeviceID = self.account.deviceId
            }
        })
        var legID: UInt64 = 0
        if tokenOpID > 0 {
            let effs = await stepDemo {
                $0.tokenResult = .with { $0.opID = tokenOpID; $0.token = "demo-token"; $0.expiresMs = clock() + 3_600_000 }
            }
            legID = effs.compactMap { if case .openStream(let s)? = $0.effect { return s.legID }; return nil }.last ?? 0
        }
        var listsOpID: UInt64 = 0, prefsOpID: UInt64 = 0
        if legID > 0 {
            for eff in await stepDemo({ $0.streamReady = .with { $0.legID = legID; $0.serverTimeMs = self.clock() } }) {
                if case .rpc(let r)? = eff.effect {
                    if case .fetchLists? = r.kind { listsOpID = r.opID }
                    if case .getAccountPrefs? = r.kind { prefsOpID = r.opID }
                }
            }
        }
        _ = await stepDemo {
            $0.listsFetched = .with {
                $0.opID = listsOpID
                $0.machines = shown.machines.values.map { m in
                    PBCorePlainMachine.with {
                        $0.machineID = m.id; $0.online = m.online; $0.lastSeenMs = m.lastSeenMs
                        if let meta = m.meta { $0.meta = meta }; $0.metaRev = m.metaRev
                    }
                }
                $0.sessions = shown.sessions.values.map { s in
                    PBCorePlainSession.with {
                        $0.sessionID = s.id; $0.machineID = s.machineId
                        if let info = s.info { $0.meta = info }; $0.metaRev = s.metaRev
                        $0.lastSeq = s.lastSeq; $0.updatedMs = s.updatedMs; $0.listedEndedMs = s.listedEndedMs
                        if let cp = s.checkpoint { $0.checkpoint = cp }
                    }
                }
                $0.devices = shown.devices.values.map { d in
                    PBCorePlainDevice.with {
                        $0.deviceID = d.id; $0.kind = d.kind; $0.createdMs = d.createdMs
                        $0.lastSeenMs = d.lastSeenMs; if let meta = d.meta { $0.meta = meta }; $0.metaRev = d.metaRev
                    }
                }
            }
        }
        let prefs = shown.prefs ?? PrefsObject(revision: 0, prefs: PBProjectPrefs())
        _ = await stepDemo {
            $0.accountPrefsFetched = .with {
                $0.opID = prefsOpID
                $0.prefs = .with { $0.revision = prefs.revision; $0.prefs = prefs.prefs }
            }
        }
        for (sid, list) in events where !list.isEmpty {
            _ = await stepDemo {
                $0.eventsPageFetched = .with {
                    $0.sessionID = sid
                    $0.events = list.map { ev in
                        PBCorePlainEvent.with { $0.seq = ev.seq; $0.event = ev; $0.verified = true; $0.approxBytes = 512 }
                    }
                }
            }
        }
        switch shown.link {
        case .noNetwork:
            _ = await stepDemo { $0.networkChanged = .with { $0.signature = "none"; $0.available = false } }
        case .noServer:
            let dropEffs = await stepDemo { $0.streamClosed = .with { $0.legID = legID; $0.unresolvedHost = true; $0.error = shown.error } }
            if let grace = dropEffs.compactMap({ if case .scheduleTimer(let t)? = $0.effect, t.kind == .linkGrace { return t.timerID }; return nil }).first {
                _ = await stepDemo { $0.timerFired = .with { $0.timerID = grace; $0.kind = .linkGrace } }
            }
        case .waiting:
            _ = await stepDemo { $0.streamClosed = .with { $0.legID = legID } }
        case .online:
            break
        }
        await renderTargets([.home, .project, .newSession])
    }
    #endif
}
