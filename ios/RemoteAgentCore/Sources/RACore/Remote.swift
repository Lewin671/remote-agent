// Transport & Security Gate executor for iOS.
// Sync state (connection machine, directory/paging, event buffers/LRU, live polling,
// prefs CAS, command retries/outcome classification, login/token refresh) lives in Go `clientcore`.
// `Remote` provides WebSocket stream frames, DirectExecutor bypass, CipherCache I/O,
// and SecurityExecutor unary byte-pipe execution.
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
        public let onRawFrame: @Sendable (UInt64, Data) async -> Void
        public let onClosed: @Sendable (PBCoreStreamClosedEvent) async -> Void
        public init(
            onRawFrame: @escaping @Sendable (UInt64, Data) async -> Void,
            onClosed: @escaping @Sendable (PBCoreStreamClosedEvent) async -> Void
        ) {
            self.onRawFrame = onRawFrame
            self.onClosed = onClosed
        }
    }

    // MARK: - Properties

    public nonisolated let account: Account
    public nonisolated let origin: RelayOrigin
    nonisolated let http: HTTP
    nonisolated let allowPlainHttp: Bool
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

    nonisolated let deviceLink: Link

    private let frameQueue: Int
    private let frameQueueBytes: Int

    // Token cache owned by Core's security engine (updated solely via SecurityTokenUpdatedEffect).
    var cachedToken: SavedLogin?
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
    var foregroundActive = true
    var fallbackSecurityDriver: CoreSecurityDriver?
    var securityConfiguredInCore = false
    var securityBootstrapped = false
    var securityBootstrapTask: Task<Void, Error>?

    // Hooks installed by CoreHost so Core receives executor events and drives direct transport.
    var hostHooks: CoreHooks?
    public struct CoreHooks: Sendable {
        public var onRefresh: @Sendable () async -> Void
        public var onRefreshDevices: @Sendable () async -> Void
        public var onSecurityOp: (@Sendable (PBSecurityOpRequestEvent) async -> PBSecurityOpResultEffect)?
        public var onCoreEvent: @Sendable (PBCoreEvent) async -> Void
        public var onRunMachineCommand: (@Sendable (String, String, PBCommand, Bool) async throws -> PBCommandResult)?
        public init(
            onRefresh: @escaping @Sendable () async -> Void,
            onRefreshDevices: @escaping @Sendable () async -> Void,
            onSecurityOp: (@Sendable (PBSecurityOpRequestEvent) async -> PBSecurityOpResultEffect)? = nil,
            onCoreEvent: @escaping @Sendable (PBCoreEvent) async -> Void = { _ in },
            onRunMachineCommand: (@Sendable (String, String, PBCommand, Bool) async throws -> PBCommandResult)? = nil
        ) {
            self.onRefresh = onRefresh
            self.onRefreshDevices = onRefreshDevices
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
        monotonic: (@Sendable () -> Int64)? = nil,
        rtc: (any RtcFactory)? = nil,
        directPreferences: any DirectPreferenceStore = UserDefaultsDirectPreferences(),
        directStun: [String]? = nil
    ) throws {
        self.account = account
        self.allowPlainHttp = allowPlainHttp
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
        let builtOrigin = try RelayOrigin(server: account.server)
        self.origin = builtOrigin
        let holder = WeakRemote()
        let closedFlag = self.closedLock
        let gate = DirectEventQueueGate()
        let eventChain = SerialChain()
        self.securityExecutor = SecurityExecutor(
            store: store,
            http: http,
            allowPlainHttp: allowPlainHttp,
            defaultServer: builtOrigin.server,
            defaultDeviceId: account.deviceId,
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
        deviceLink = Link(relay: RelayPath(origin: builtOrigin, http: http, sleep: sleep))
        holder.remote = self
    }

    // MARK: - Wiring with CoreHost

    public func installCoreHooks(_ hooks: CoreHooks) {
        self.hostHooks = hooks
    }

    public func setWatchedSessions(_ routes: [PBWatchedSessionRoute]) {
        watchedSessionIds = routes.map(\.sessionID)
        var nextMachines = sessionMachines
        for route in routes where !route.machineID.isEmpty {
            nextMachines[route.sessionID] = route.machineID
        }
        sessionMachines = nextMachines
    }

    func emitCoreEvent(_ ev: PBCoreEvent) async {
        guard !closed else { return }
        if let onCore = hostHooks?.onCoreEvent {
            await onCore(ev)
            return
        }
        ensureFallbackSecurityDriver()?.enqueueEvent(ev)
    }

    func machineIdForSession(_ sid: String) -> String {
        sessionMachines[sid] ?? ""
    }

    func machineIdForSessionIfKnown(_ sid: String) -> String? {
        let id = machineIdForSession(sid)
        return id.isEmpty ? nil : id
    }

    // MARK: - Stream Executor (OpenStreamEffect / CloseStreamEffect)

    public func openStream(legId: UInt64, helloFrame: Data, callbacks: StreamCallbacks) {
        closeStream(legId: legId)
        guard !helloFrame.isEmpty else {
            Task {
                var closed = PBCoreStreamClosedEvent()
                closed.legID = legId
                closed.error = L10n.errorsDisconnectedFromServer()
                await callbacks.onClosed(closed)
            }
            return
        }
        let socket = deviceLink.relay.openStream("/v1/stream", hello: helloFrame)
        let leg = StreamLeg(legId: legId, socket: socket)
        streamLegs[legId] = leg

        let (frames, queue) = AsyncStream<Data>.makeStream(
            bufferingPolicy: .bufferingOldest(max(frameQueue, 1)))
        leg.queue = queue
        let budget = StreamByteBudget(limit: frameQueueBytes)

        leg.receiveTask = Task.detached {
            while let bytes = try? await socket.receive() {
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

    private func processStream(
        legId: UInt64,
        frames: AsyncStream<Data>,
        budget: StreamByteBudget,
        callbacks: StreamCallbacks
    ) async {
        guard let leg = streamLegs[legId] else { return }
        if await leg.socket.opened(), !closed, !leg.closed, streamLegs[legId] === leg {
            activeLegId = legId
            directExecutor.attachLeg(leg.socket)
        }
        for await bytes in frames {
            defer { budget.release(bytes.count) }
            guard !closed, !leg.closed, streamLegs[legId] === leg else { break }
            await callbacks.onRawFrame(legId, bytes)
        }
        leg.socket.cancel()
        directExecutor.detachLeg(leg.socket)
        guard streamLegs[legId] === leg else { return }
        streamLegs.removeValue(forKey: legId)
        if activeLegId == legId { activeLegId = nil }
        let end = await leg.socket.ended()
        guard !closed && !leg.closed else { return }
        var closedEvent = PBCoreStreamClosedEvent()
        closedEvent.legID = legId
        closedEvent.policyViolation = end.closeCode == policyViolation
        if let err = end.error {
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
