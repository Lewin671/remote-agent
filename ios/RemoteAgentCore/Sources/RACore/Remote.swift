// Transport & Security Gate executor for iOS.
// Sync state (connection machine, directory/paging, event buffers/LRU, live polling,
// prefs CAS, command retries/outcome classification) lives in Go `clientcore/sync`.
// `Remote` only provides token login, WebSocket stream frames, RPC execution,
// DirectExecutor bypass, CipherCache I/O, and cryptographic opening/sealing.
import Connect
import Foundation
import RAProto
import SwiftProtobuf
import os

public let projectPrefsID = "project-prefs"

private let streamFrameBytes = 2 << 20

public struct SavedLogin: Codable, Equatable, Sendable, CustomStringConvertible, CustomDebugStringConvertible, CustomReflectable {
    public var token: String
    public var expiresMs: Int64
    public init(token: String, expiresMs: Int64) {
        self.token = token
        self.expiresMs = expiresMs
    }
    public var description: String { "SavedLogin(expiresMs=\(expiresMs))" }
    public var debugDescription: String { description }
    public var customMirror: Mirror { Mirror(self, children: ["expiresMs": expiresMs]) }
}

/// A step that took longer than it was given.
struct TimedOut: Error {}

/// Runs `body`, giving up after `ms`: it is cancelled then, and `TimedOut` thrown.
func withTimeout<T: Sendable>(
    _ ms: Int64, sleep: @escaping Sleeper, _ body: @escaping @Sendable () async throws -> T
) async throws -> T {
    try await withThrowingTaskGroup(of: T.self) { group in
        group.addTask { try await body() }
        group.addTask {
            try await sleep(ms)
            throw TimedOut()
        }
        defer { group.cancelAll() }
        return try await group.next()!
    }
}

public final actor Remote {
    // MARK: - Types

    private struct LoginFlight {
        let id: UUID
        let generation: Int
        let task: Task<Void, Never>
        let result: SharedResult<String>
    }

    final class StreamLeg {
        let legId: UInt64
        let socket: any FrameStream
        var receiveTask: Task<Void, Never>?
        var processTask: Task<Void, Never>?
        var queue: AsyncStream<Data>.Continuation?
        var closed = false
        init(legId: UInt64, socket: any FrameStream) {
            self.legId = legId
            self.socket = socket
        }
    }

    public struct StreamCallbacks: Sendable {
        public let onReady: @Sendable (UInt64, Int64) async -> Void
        public let onFrame: @Sendable (PBCoreStreamFrameEvent) async -> Void
        public let onClosed: @Sendable (PBCoreStreamClosedEvent) async -> Void
        public init(
            onReady: @escaping @Sendable (UInt64, Int64) async -> Void,
            onFrame: @escaping @Sendable (PBCoreStreamFrameEvent) async -> Void,
            onClosed: @escaping @Sendable (PBCoreStreamClosedEvent) async -> Void
        ) {
            self.onReady = onReady
            self.onFrame = onFrame
            self.onClosed = onClosed
        }
    }

    // MARK: - Properties

    public nonisolated let account: Account
    public nonisolated let api: Api
    nonisolated let http: HTTP
    nonisolated let cache: (any CipherCache)?
    nonisolated let log: @Sendable (String) -> Void
    nonisolated let clock: @Sendable () -> Int64
    nonisolated let sleep: Sleeper
    public nonisolated let securityStore: any SecurityStoreV4
    public nonisolated let securityExecutor: SecurityExecutor
    public nonisolated let securityClient: SecurityClient
    private nonisolated let closedLock = OSAllocatedUnfairLock(initialState: false)
    nonisolated let directPreferences: any DirectPreferenceStore
    public nonisolated let directServerHost: String
    public nonisolated let directConfiguredStun: String
    public nonisolated let directSupported: Bool
    private nonisolated let coreEventChain: SerialChain
    nonisolated let directExecutor: DirectExecutor
    nonisolated let activeTerminals = ActiveTerminalRegistry()
    private let peerBudget = StreamByteBudget(limit: 8 * 65536)

    nonisolated let deviceLink: Link

    private let frameQueue: Int
    private let frameQueueBytes: Int
    private let frameParser = OSAllocatedUnfairLock<@Sendable (Data) throws -> PBServerFrame>(
        initialState: { try PBServerFrame(serializedBytes: $0) }
    )

    // Token / Auth executor state
    var token = ""
    var tokenExpires: Int64 = 0
    private var loginGeneration = 0
    private var loginFlight: LoginFlight?
    private var signingIn = false
    private var signInWaiters: [CheckedContinuation<Void, Never>] = []
    var closed = false

    // Stream executor state (keyed by Core's leg_id)
    private var streamLegs: [UInt64: StreamLeg] = [:]
    private var activeLegId: UInt64?

    // Security Workflow & Direct Gate state
    let directGate: DirectEventQueueGate
    var sessionMachines: [String: String] = [:] {
        didSet {
            if sessionMachines != oldValue { directGate.updateMachines(sessionMachines) }
        }
    }
    var watchedSessionIds: [String] = [] {
        didSet {
            if watchedSessionIds != oldValue { directGate.updateWatched(watchedSessionIds) }
        }
    }
    var clockSkewed = false
    var foregroundActive = true
    var networkGeneration: UInt64 = 0
    var fallbackSecurityDriver: CoreSecurityDriver?
    var securityConfiguredInCore = false
    var securityBootstrapped = false
    var securityBootstrapTask: Task<Void, Error>?

    // Hooks installed by CoreHost so Core receives executor events and drives direct transport.
    var hostHooks: CoreHooks?
    public struct CoreHooks: Sendable {
        public var onRefresh: @Sendable () async -> Void
        public var onRefreshDevices: @Sendable () async -> Void
        public var onQuerySyncState: (@Sendable () async -> PBCoreSyncStateView?)?
        public var onSecurityOp: (@Sendable (PBSecurityOpRequestEvent) async -> PBSecurityOpResultEffect)?
        public var onCoreEvent: @Sendable (PBCoreEvent) async -> Void
        public var onRunMachineCommand: (@Sendable (String, String, PBCommand, Bool) async throws -> PBCommandResult)?
        public init(
            onRefresh: @escaping @Sendable () async -> Void,
            onRefreshDevices: @escaping @Sendable () async -> Void,
            onQuerySyncState: (@Sendable () async -> PBCoreSyncStateView?)? = nil,
            onSecurityOp: (@Sendable (PBSecurityOpRequestEvent) async -> PBSecurityOpResultEffect)? = nil,
            onCoreEvent: @escaping @Sendable (PBCoreEvent) async -> Void = { _ in },
            onRunMachineCommand: (@Sendable (String, String, PBCommand, Bool) async throws -> PBCommandResult)? = nil
        ) {
            self.onRefresh = onRefresh
            self.onRefreshDevices = onRefreshDevices
            self.onQuerySyncState = onQuerySyncState
            self.onSecurityOp = onSecurityOp
            self.onCoreEvent = onCoreEvent
            self.onRunMachineCommand = onRunMachineCommand
        }
    }

    // MARK: - Init

    public init(
        account: Account,
        http: HTTP,
        cache: (any CipherCache)? = nil,
        log: @escaping @Sendable (String) -> Void = { _ in },
        clock: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) },
        sleep: @escaping Sleeper = realSleep,
        jitter: @escaping @Sendable () -> Double = { Double.random(in: 0..<1) },
        frameQueue: Int = 1024,
        frameQueueBytes: Int = 32 << 20,
        streamOpenMs: Int64 = 15_000,
        securityStore: (any SecurityStoreV4)? = nil,
        allowPlainHttp: Bool = false,
        rpcOverride: SecurityExecutor.RpcOverride? = nil,
        monotonic: (@Sendable () -> Int64)? = nil,
        rtc: (any RtcFactory)? = nil,
        directPreferences: any DirectPreferenceStore = UserDefaultsDirectPreferences(),
        directStun: [String]? = nil
    ) throws {
        self.account = account
        self.directPreferences = directPreferences
        if let overrideStun = directStun {
            self.directServerHost = ""
            self.directConfiguredStun = overrideStun.joined(separator: ",")
        } else {
            self.directServerHost = URL(string: account.server)?.host ?? ""
            self.directConfiguredStun = ""
        }
        self.directSupported = rtc != nil
        let store = securityStore ?? MemorySecurityStoreV4()
        self.securityStore = store
        self.http = http
        self.cache = cache
        self.log = log
        self.clock = clock
        self.sleep = sleep
        self.frameQueue = frameQueue
        self.frameQueueBytes = frameQueueBytes
        let builtApi = try Api(server: account.server, http: http)
        api = builtApi
        let holder = WeakRemote()
        let closedFlag = self.closedLock
        let gate = DirectEventQueueGate()
        let eventChain = SerialChain()
        self.securityExecutor = SecurityExecutor(
            store: store,
            http: http,
            allowPlainHttp: allowPlainHttp,
            rpcOverride: rpcOverride,
            emit: { ev in
                await holder.remote?.emitSecurityCoreEvent(ev)
            }
        )
        self.securityClient = SecurityClient(
            dispatchEvent: { ev in
                Task { await holder.remote?.emitSecurityCoreEvent(ev) }
            },
            setForegroundFn: { fg in
                Task { await holder.remote?.setSecurityForeground(fg) }
            },
            isClosedFn: { closedFlag.withLock { $0 } },
            onDismissUpgradeNotice: { store.dismissLegacyRepairNotice() }
        )
        self.directGate = gate
        self.coreEventChain = eventChain
        let terminals = activeTerminals
        directExecutor = DirectExecutor(
            emit: { ev in
                eventChain.run {
                    await holder.remote?.emitCoreEvent(ev)
                }
            },
            onPushedEvent: { machineId, pcId, env, header, size in
                let sid = header.sessionID
                guard let chain = gate.admit(
                    sid,
                    machineId: machineId,
                    sender: header.sender,
                    seq: header.seq,
                    bytes: size
                ) else { return }
                chain.run {
                    await holder.remote?.handleDirectEvent(machineId: machineId, pcId: pcId, envelope: env, header: header, size: size)
                    gate.release(sid, bytes: size)
                }
            },
            onTerminalOutput: { machineId, pcId, env, header, raw in
                terminals.onDirectOutput(streamId: header.requestID, machineId: machineId, pcId: pcId, env: env, raw: raw)
            },
            factory: rtc,
            clock: { UInt64(max(0, clock())) },
            monotonic: {
                if let monotonic {
                    return Double(monotonic())
                }
                return Double(DispatchTime.now().uptimeNanoseconds) / 1e6
            }
        )
        deviceLink = Link(relay: RelayPath(
            api: builtApi, sleep: sleep,
            prepare: { [holder] prior in
                guard let remote = holder.remote else { throw SecurityFailure.storage }
                try await remote.prepareRelayAttempt(prior)
            },
            token: { [holder] in
                guard let remote = holder.remote else { throw SecurityFailure.storage }
                return await remote.relayToken()
            }
        ))
        holder.remote = self
    }

    // MARK: - Wiring with CoreHost

    public func installCoreHooks(_ hooks: CoreHooks) {
        self.hostHooks = hooks
    }

    public func applyCoreSyncState(_ v: PBCoreSyncStateView) {
        self.clockSkewed = v.accountClockSkewed
        // Keep sessionMachines in sync so directGate / terminal know owner machine IDs.
        if v.sessions.contains(where: { sessionMachines[$0.sessionID] != $0.machineID }) {
            var nextMachines = sessionMachines
            for s in v.sessions {
                nextMachines[s.sessionID] = s.machineID
            }
            sessionMachines = nextMachines
        }
    }

    public func setWatchedSessions(_ sessionIds: [String]) {
        watchedSessionIds = sessionIds
    }

    func emitCoreEvent(_ ev: PBCoreEvent) async {
        guard !closed else { return }
        if let onCore = hostHooks?.onCoreEvent {
            await onCore(ev)
            return
        }
        ensureFallbackSecurityDriver()?.enqueueEvent(ev)
    }

    public func setNetworkGeneration(_ gen: UInt64) {
        networkGeneration = gen
    }

    func machineIdForSession(_ sid: String) -> String {
        sessionMachines[sid] ?? ""
    }

    func machineIdForSessionIfKnown(_ sid: String) -> String? {
        let id = machineIdForSession(sid)
        return id.isEmpty ? nil : id
    }

    // MARK: - Login & Token Executor

    func prepareRelayAttempt(_ prior: Bool) async throws {
        if token.isEmpty { _ = try await relogin(prior) }
    }

    private func relayToken() -> String { token }

    public func ensureToken(opId: UInt64, forceLogin: Bool, timeoutMs: Int64) async -> PBCoreTokenResultEvent {
        var ev = PBCoreTokenResultEvent()
        ev.opID = opId
        if forceLogin {
            dropToken()
            retireLogin()
        }
        if !forceLogin {
            if !token.isEmpty, tokenExpires == 0 || tokenExpires >= clock() + 86_400_000 {
                ev.token = token
                ev.expiresMs = tokenExpires
                return ev
            }
            if let saved = securityStore.loadLoginToken(server: account.server, deviceId: account.deviceId),
               saved.expiresMs == 0 || saved.expiresMs >= clock() + 86_400_000
            {
                token = saved.token
                tokenExpires = saved.expiresMs
                ev.token = saved.token
                ev.expiresMs = saved.expiresMs
                return ev
            }
        }
        do {
            let tok: String
            if timeoutMs > 0 {
                tok = try await withTimeout(timeoutMs, sleep: sleep) {
                    try await self.signIn()
                }
            } else {
                tok = try await signIn()
            }
            ev.token = tok
            ev.expiresMs = tokenExpires
            return ev
        } catch let e as ConnectError where e.code == .permissionDenied {
            dropToken()
            ev.removed = true
            ev.errorCode = "permission_denied"
            ev.error = errorText(e)
            return ev
        } catch {
            ev.errorCode = (error as? ConnectError)?.code.name ?? (error is TimedOut ? "deadline_exceeded" : "unavailable")
            ev.error = errorText(error)
            ev.unresolvedHost = unresolvedHost(error)
            return ev
        }
    }

    public func discardToken(_ rejected: String) {
        rejectCredential(rejected)
    }

    func signIn() async throws -> String {
        while true {
            try Task.checkCancellation()
            let flight: LoginFlight
            if let existing = loginFlight {
                flight = existing
            } else {
                let generation = loginGeneration
                let id = UUID(), result = SharedResult<String>()
                let task = Task {
                    let outcome: Result<String, Error>
                    do {
                        outcome = .success(try await self.performSignIn())
                    } catch {
                        outcome = .failure(error)
                    }
                    if self.loginFlight?.id == id { self.loginFlight = nil }
                    result.resolve(outcome)
                }
                flight = LoginFlight(id: id, generation: generation, task: task, result: result)
                loginFlight = flight
            }
            do {
                return try await flight.result.value
            } catch {
                if flight.generation != loginGeneration && !Task.isCancelled && !closed { continue }
                throw error
            }
        }
    }

    func retireLogin() {
        loginGeneration += 1
        loginFlight?.task.cancel()
        loginFlight = nil
    }

    private func performSignIn() async throws -> String {
        while signingIn {
            await withCheckedContinuation { signInWaiters.append($0) }
        }
        signingIn = true
        defer {
            signingIn = false
            if !signInWaiters.isEmpty { signInWaiters.removeFirst().resume() }
        }
        try Task.checkCancellation()
        let timeout: Int64 = networkGeneration > 0 ? 3000 : 15_000
        do {
            let response = try await withTimeout(timeout, sleep: sleep) { [api, account, securityStore] in
                try await loginWithStore(api: api, deviceId: account.deviceId, store: securityStore)
            }
            try Task.checkCancellation()
            return signedIn(response)
        } catch let first as ConnectError where first.code == .permissionDenied {
            let response = try await withTimeout(timeout, sleep: sleep) { [api, account, securityStore] in
                try await loginWithStore(api: api, deviceId: account.deviceId, store: securityStore)
            }
            try Task.checkCancellation()
            return signedIn(response)
        }
    }

    private func signedIn(_ res: PBLoginResponse) -> String {
        if !closed {
            token = res.token
            tokenExpires = res.expiresMs
            let saved = SavedLogin(token: res.token, expiresMs: res.expiresMs)
            securityStore.saveLoginToken(saved, server: account.server, deviceId: account.deviceId)
        }
        return res.token
    }

    func rejectCredential(_ rejected: String) {
        if rejected.isEmpty || token == rejected { dropToken() }
    }

    func dropToken() {
        token = ""
        tokenExpires = 0
        securityStore.clearLoginToken(server: account.server, deviceId: account.deviceId)
    }

    // MARK: - Stream Executor (OpenStreamEffect / CloseStreamEffect)

    public func openStream(legId: UInt64, token credential: String, callbacks: StreamCallbacks) {
        closeStream(legId: legId)
        var frame = PBPeerFrame()
        frame.hello.token = credential
        guard let hello: Data = try? frame.serializedData() else {
            Task {
                var closed = PBCoreStreamClosedEvent()
                closed.legID = legId
                closed.error = L10n.errorsDisconnectedFromServer()
                await callbacks.onClosed(closed)
            }
            return
        }
        let socket = deviceLink.relay.openStream("/v1/stream", hello: hello)
        let leg = StreamLeg(legId: legId, socket: socket)
        streamLegs[legId] = leg
        let netGen = networkGeneration

        let (frames, queue) = AsyncStream<Data>.makeStream(
            bufferingPolicy: .bufferingOldest(max(frameQueue, 1)))
        leg.queue = queue
        let budget = StreamByteBudget(limit: frameQueueBytes)
        let parse = frameParser.withLock { $0 }
        let peerBudget = self.peerBudget

        leg.receiveTask = Task.detached { [weak self] in
            while let bytes = try? await socket.receive() {
                if let body = peerFrameBody(bytes, targetField: 11) {
                    if peerBudget.reserve(65536) {
                        Task {
                            await self?.handlePeerDelivery(body, legId: legId, socket: socket, netGen: netGen)
                            peerBudget.release(65536)
                        }
                    }
                    continue
                }
                if let body = peerFrameBody(bytes, targetField: 12) {
                    if peerBudget.reserve(65536) {
                        Task {
                            await self?.handlePeerResult(body, legId: legId, socket: socket, netGen: netGen)
                            peerBudget.release(65536)
                        }
                    }
                    continue
                }
                guard bytes.count <= streamFrameBytes, budget.reserve(bytes.count) else {
                    socket.cancel()
                    break
                }
                if case .dropped = queue.yield(bytes) {
                    budget.release(bytes.count)
                    socket.cancel()
                    break
                }
            }
            queue.finish()
        }

        leg.processTask = Task { [weak self] in
            guard let self else { return }
            await self.processStream(
                legId: legId,
                frames: frames,
                budget: budget,
                parse: parse,
                credential: credential,
                callbacks: callbacks
            )
        }
    }

    public func closeStream(legId: UInt64) {
        guard let leg = streamLegs.removeValue(forKey: legId) else { return }
        if activeLegId == legId {
            activeLegId = nil
        }
        directExecutor.detachLeg(leg.socket)
        leg.closed = true
        leg.queue?.finish()
        leg.receiveTask?.cancel()
        leg.processTask?.cancel()
        leg.socket.cancel()
    }

    private func handlePeerDelivery(_ body: Data, legId: UInt64, socket: any FrameStream, netGen: UInt64) async {
        guard !closed, networkGeneration == netGen, activeLegId == legId, streamLegs[legId]?.socket === socket else { return }
        var ev = PBCoreEvent()
        ev.directSignalReceived = .with { $0.raw = body }
        await hostHooks?.onCoreEvent(ev)
    }

    private func handlePeerResult(_ body: Data, legId: UInt64, socket: any FrameStream, netGen: UInt64) async {
        guard !closed, networkGeneration == netGen, activeLegId == legId, streamLegs[legId]?.socket === socket,
              let res = try? PBPeerFrameResult(serializedBytes: body) else { return }
        var ev = PBCoreEvent()
        ev.directSignalDelivery = .with {
            $0.frameID = res.frameID
            $0.status = UInt32(res.status.rawValue)
            $0.retryAfterMs = UInt64(res.retryAfterMs)
        }
        await hostHooks?.onCoreEvent(ev)
    }

    private func processStream(
        legId: UInt64,
        frames: AsyncStream<Data>,
        budget: StreamByteBudget,
        parse: @Sendable (Data) throws -> PBServerFrame,
        credential: String,
        callbacks: StreamCallbacks
    ) async {
        guard let leg = streamLegs[legId] else { return }
        func emit(_ fill: (inout PBCoreStreamFrameEvent) -> Void) async {
            var ev = PBCoreStreamFrameEvent()
            ev.legID = legId
            fill(&ev)
            await callbacks.onFrame(ev)
        }
        for await bytes in frames {
            defer { budget.release(bytes.count) }
            guard !closed, !leg.closed, streamLegs[legId] === leg else { break }
            do {
                let serverFrame = try parse(bytes)
                switch serverFrame.kind {
                case .ready(let ready)?:
                    activeLegId = legId
                    directExecutor.attachLeg(leg.socket)
                    await callbacks.onReady(legId, ready.serverTimeMs)
                case .heartbeat?:
                    await emit { $0.heartbeat = true }
                case .machineUpdated(let u)?:
                    await emit { $0.wireMachineUpdated = u.machine }
                case .machineRemoved(let u)?:
                    await emit { $0.machineRemovedID = u.machineID }
                case .sessionUpdated(let u)?:
                    await emit { $0.wireSessionUpdated = u.session }
                case .eventsAppended(let a)?:
                    await emit {
                        $0.eventsAppended.sessionID = a.sessionID
                        $0.eventsAppended.wireEvents = a.events
                    }
                case .accountPrefsUpdated(let u)?:
                    if u.object.objectID == projectPrefsID {
                        await emit { $0.wireAccountPrefsUpdated = u.object }
                    }
                case .deviceUpdated(let u)?:
                    await emit { $0.wireDeviceUpdated = u.device }
                case .deviceRemoved(let u)?:
                    await emit { $0.deviceRemovedID = u.deviceID }
                default:
                    break
                }
            } catch {
                if !Task.isCancelled && !leg.closed { report(error) }
                break
            }
        }
        leg.socket.cancel()
        directExecutor.detachLeg(leg.socket)
        guard streamLegs[legId] === leg else { return }
        streamLegs.removeValue(forKey: legId)
        if activeLegId == legId { activeLegId = nil }
        let end = await leg.socket.ended()
        guard !closed && !leg.closed else { return }
        if end.closeCode == policyViolation && token == credential {
            dropToken()
        }
        var closedEvent = PBCoreStreamClosedEvent()
        closedEvent.legID = legId
        closedEvent.policyViolation = end.closeCode == policyViolation
        if let err = end.error {
            closedEvent.errorCode = (err as? ConnectError)?.code.name ?? ""
            closedEvent.error = errorText(err)
            closedEvent.unresolvedHost = unresolvedHost(err)
        } else {
            closedEvent.error = L10n.errorsDisconnectedFromServer()
        }
        await callbacks.onClosed(closedEvent)
    }

    public nonisolated func close() {
        closedLock.withLock { $0 = true }
        directGate.markClosed()
        deviceLink.closeStreams()
        directExecutor.close()
        securityExecutor.close()
        securityClient.rejectAll()
        Task { await self.closeActor() }
    }

    private func closeActor() {
        guard !closed else { return }
        closed = true
        loginGeneration += 1
        loginFlight?.task.cancel()
        loginFlight = nil
        securityBootstrapTask?.cancel()
        securityBootstrapTask = nil
        for leg in streamLegs.values {
            leg.closed = true
            leg.queue?.finish()
            leg.receiveTask?.cancel()
            leg.processTask?.cancel()
            leg.socket.cancel()
        }
        streamLegs.removeAll()
        activeLegId = nil
        deviceLink.closeStreams()
        directExecutor.close()
        securityExecutor.close()
        securityClient.rejectAll()
        fallbackSecurityDriver?.close()
        fallbackSecurityDriver = nil
    }

    // MARK: - Transitional Step 3-4 Forwarding to Core

    @discardableResult
    public func refresh() async -> Bool {
        if let hooks = hostHooks {
            await hooks.onRefresh()
            if let v = await currentCoreSyncStateView() {
                return v.connection == .connected
            }
        }
        return false
    }

    public func refreshDevices() async {
        await hostHooks?.onRefreshDevices()
    }

    func report(_ e: Error) {
        log("remote-agent: " + errorText(e))
    }
}

final class DirectEventQueueGate: @unchecked Sendable {
    private let maxPerSession = 16
    private let maxTotalBytes = 512 * 1024
    private let lock = NSLock()
    private var closed = false
    private var watched: Set<String> = []
    private var machines: [String: String] = [:]
    private var chains: [String: SerialChain] = [:]
    private var counts: [String: Int] = [:]
    private var totalBytes = 0

    func updateWatched(_ ids: [String]) {
        lock.lock()
        defer { lock.unlock() }
        watched = Set(ids)
        chains = chains.filter { sid, _ in watched.contains(sid) || (counts[sid] ?? 0) > 0 }
    }

    func updateMachines(_ sessionMachines: [String: String]) {
        lock.lock()
        defer { lock.unlock() }
        machines = sessionMachines
    }

    func markClosed() {
        lock.lock()
        defer { lock.unlock() }
        closed = true
        watched.removeAll()
        machines.removeAll()
        chains.removeAll()
    }

    func admit(_ sessionId: String, machineId: String, sender: String, seq: UInt64, bytes: Int) -> SerialChain? {
        guard !sessionId.isEmpty, seq > 0 else { return nil }
        let cost = max(bytes, 64)
        lock.lock()
        defer { lock.unlock() }
        guard !closed,
              watched.contains(sessionId),
              machines[sessionId] == machineId,
              sender == machineId else {
            return nil
        }
        let count = counts[sessionId, default: 0]
        if count >= maxPerSession || totalBytes + cost > maxTotalBytes {
            return nil
        }
        counts[sessionId] = count + 1
        totalBytes += cost
        if let existing = chains[sessionId] {
            return existing
        }
        let chain = SerialChain()
        chains[sessionId] = chain
        return chain
    }

    func release(_ sessionId: String, bytes: Int) {
        let cost = max(bytes, 64)
        lock.lock()
        defer { lock.unlock() }
        totalBytes = max(0, totalBytes - cost)
        if let count = counts[sessionId] {
            if count <= 1 {
                counts.removeValue(forKey: sessionId)
                if !watched.contains(sessionId) {
                    chains.removeValue(forKey: sessionId)
                }
            } else {
                counts[sessionId] = count - 1
            }
        }
    }
}
