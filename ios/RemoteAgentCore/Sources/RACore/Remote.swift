// Transport & Security Gate executor for iOS.
// Sync state (connection machine, directory/paging, event buffers/LRU, live polling,
// prefs CAS, command retries/outcome classification) lives in Go `clientcore/sync`.
// `Remote` only provides token login, WebSocket stream frames, RPC execution,
// DirectEngine bypass, CipherCache I/O, and cryptographic opening/sealing, plus
// transitional Step 4 `Feed` mirror pushed from Core's `SyncStateView`.
import Connect
import Foundation
import RAProto
import SwiftProtobuf
import os

public let commandTTLMs: Int64 = 60_000

private let streamFrameBytes = 2 << 20

/// An immutable local intent for Step 3-4 transitional commands until those screens migrate to Core.
public final class PreparedCommand: @unchecked Sendable {
    public let machineId: String
    public let sessionId: String
    public let requestId: String
    public let deadlineMs: Int64
    let command: PBCommand
    private let payload: Result<Data, Error>
    public func commandBytes() throws -> Data { try payload.get() }

    private struct Stored: Codable {
        let machineId, sessionId, requestId: String
        let deadlineMs: Int64
        let payload: Data
    }

    public func recordBytes() throws -> Data {
        try JSONEncoder().encode(Stored(
            machineId: machineId, sessionId: sessionId, requestId: requestId,
            deadlineMs: deadlineMs, payload: commandBytes()))
    }

    public init(recordBytes: Data, prior: Bool) throws {
        self.mayHaveRunFlag = OSAllocatedUnfairLock(initialState: prior)
        let record = try JSONDecoder().decode(Stored.self, from: recordBytes)
        machineId = record.machineId
        sessionId = record.sessionId
        requestId = record.requestId
        deadlineMs = record.deadlineMs
        payload = .success(record.payload)
        command = try PBCommand(serializedBytes: record.payload)
    }

    private let mayHaveRunFlag: OSAllocatedUnfairLock<Bool>
    public var mayHaveRun: Bool { mayHaveRunFlag.withLock { $0 } }
    func markMayHaveRun() { mayHaveRunFlag.withLock { $0 = true } }

    private let sealLock = NSLock()
    private var sealResult: SharedResult<PBEnvelope>?
    private var sealID: UUID?
    private var sealState: Data?
    private var everAuthorized = false

    private func clearAuthorization(_ id: UUID) {
        sealLock.lock()
        defer { sealLock.unlock() }
        if sealID == id {
            sealResult = nil
            sealID = nil
            sealState = nil
        }
    }

    private func authorized(_ id: UUID) {
        sealLock.lock()
        defer { sealLock.unlock() }
        if sealID == id { everAuthorized = true }
    }

    func authorizationResult(
        stateHash: Data, _ make: @escaping @Sendable (Bool) -> Task<PBEnvelope, Error>
    ) -> SharedResult<PBEnvelope> {
        sealLock.lock()
        defer { sealLock.unlock() }
        if let sealResult, sealState == stateHash { return sealResult }
        let id = UUID()
        sealID = id
        sealState = stateHash
        let verifyObservation = !everAuthorized
        let result = SharedResult<PBEnvelope>()
        Task {
            do {
                let envelope = try await make(verifyObservation).value
                self.authorized(id)
                result.resolve(.success(envelope))
            } catch {
                self.clearAuthorization(id)
                result.resolve(.failure(error))
            }
        }
        sealResult = result
        return result
    }

    public init(
        machineId: String, sessionId: String, requestId: String, deadlineMs: Int64,
        command: PBCommand
    ) {
        self.machineId = machineId
        self.sessionId = sessionId
        self.requestId = requestId
        self.deadlineMs = deadlineMs
        self.command = command
        self.mayHaveRunFlag = OSAllocatedUnfairLock(initialState: false)
        self.payload = Result { try command.serializedData() }
    }
}

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

    struct ModelList {
        let task: Task<PBListModelsResponse, Error>
        var done = false
        var failed = false
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
    nonisolated let device: SigningKey
    nonisolated let cache: (any CipherCache)?
    nonisolated let log: @Sendable (String) -> Void
    nonisolated let clock: @Sendable () -> Int64
    nonisolated let sleep: Sleeper
    public nonisolated let authorityGate = AuthorityGate()
    let securityStorage: (any DeviceSecurity.Storage)?
    var deviceSecurity: DeviceSecurityClient?
    var management: AuthorityCoordinator?

    nonisolated let direct: DirectEngine?
    public nonisolated let directChanges = Feed<Int>(0)
    var directTransfers: [String: UploadKind] = [:]
    private let peerBudget = StreamByteBudget(limit: 8 * 65536)
    private let peerResponder = PeerResponder()

    nonisolated let deviceLink: Link

    private let frameQueue: Int
    private let frameQueueBytes: Int
    private let frameParser = OSAllocatedUnfairLock<@Sendable (Data) throws -> PBServerFrame>(
        initialState: { try PBServerFrame(serializedBytes: $0) }
    )

    // Token / Auth executor state
    var token = ""
    var tokenExpires: Int64 = 0
    private var onLogin: (@Sendable (SavedLogin?) -> Void)?
    private var loginGeneration = 0
    private var loginFlight: LoginFlight?
    private var signingIn = false
    private var signInWaiters: [CheckedContinuation<Void, Never>] = []
    var closed = false

    // Stream executor state (keyed by Core's leg_id)
    private var streamLegs: [UInt64: StreamLeg] = [:]
    private var activeLegId: UInt64?

    // Security Gate & opaque cipher cache refs
    var sessionKeys: [String: Data] = [:]
    var sessionMachines: [String: String] = [:]
    private var watchedSessionIds: [String] = []
    var machineReplacements: [String: [PBMachineReplacementProof]] = [:]
    var trustedMembers: [String: DeviceSecurity.Member] = [:]
    var archiveMachines: Set<String> = []
    var cacheRefs: [String: CachedCipherObject] = [:]
    var cacheRefOrder: [String] = []
    var sealedCommands: [String: SealedCommandEntry] = [:]
    var sealedOrder: [String] = []
    var authorityDeviceId = ""
    var leaseEndMs: UInt64 = 0
    var recoveryPending = false
    var recoveryReadyMs: UInt64 = 0
    var recoveryObjectsPending = false
    var recoveryStatusStale = true
    var clockSkewed = false
    var networkGeneration: UInt64 = 0
    var verifiedNetworkGeneration: UInt64 = 0
    private var securityNetworkReset: (UInt64, Task<Void, Never>)?
    var models: [String: ModelList] = [:]

    // Transitional Step 4 dumb state feed (populated strictly by CoreHost from SyncStateView)
    public nonisolated let state: Feed<RemoteState>
    var sessionRemovalHandler: @Sendable (String) async -> Void = { _ in }

    // Hooks installed by CoreHost so unmigrated Step 4 actions still route into Core
    private var hostHooks: CoreHooks?
    public struct CoreHooks: Sendable {
        public var onFetchSession: @Sendable (String) async -> Void
        public var onRefresh: @Sendable () async -> Void
        public var onRefreshDevices: @Sendable () async -> Void
        public var onSavePrefs: @Sendable (PrefsOp) async throws -> Void
        public var onDirectStatus: @Sendable (String, Bool, PBDirectRouteReport) async -> Void
        public var onLoadMoreSessions: @Sendable () async -> Void
        public var onDirectEvents: @Sendable (String, String, [PBCorePlainEvent]) async -> Void
        public init(
            onFetchSession: @escaping @Sendable (String) async -> Void,
            onRefresh: @escaping @Sendable () async -> Void,
            onRefreshDevices: @escaping @Sendable () async -> Void,
            onSavePrefs: @escaping @Sendable (PrefsOp) async throws -> Void,
            onDirectStatus: @escaping @Sendable (String, Bool, PBDirectRouteReport) async -> Void,
            onLoadMoreSessions: @escaping @Sendable () async -> Void = {},
            onDirectEvents: @escaping @Sendable (String, String, [PBCorePlainEvent]) async -> Void = { _, _, _ in }
        ) {
            self.onFetchSession = onFetchSession
            self.onRefresh = onRefresh
            self.onRefreshDevices = onRefreshDevices
            self.onSavePrefs = onSavePrefs
            self.onDirectStatus = onDirectStatus
            self.onLoadMoreSessions = onLoadMoreSessions
            self.onDirectEvents = onDirectEvents
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
        securityStorage: (any DeviceSecurity.Storage)? = nil,
        monotonic: (@Sendable () -> Int64)? = nil,
        rtc: (any RtcFactory)? = nil,
        directPreferences: any DirectPreferenceStore = UserDefaultsDirectPreferences(),
        directStun: [String]? = nil
    ) throws {
        self.account = account
        let holder = WeakRemote()
        if let rtc {
            direct = DirectEngine(
                server: account.server, deviceId: account.deviceId, deviceSeed: account.deviceSeed,
                factory: rtc, preferences: directPreferences, stun: directStun,
                authorize: { try await holder.remote?.directAuthorize() },
                changed: { holder.remote?.directChanged() }
            )
        } else {
            direct = nil
        }
        self.securityStorage = securityStorage
        self.http = http
        self.cache = cache
        self.log = log
        self.clock = clock
        self.sleep = sleep
        self.frameQueue = frameQueue
        self.frameQueueBytes = frameQueueBytes
        device = try SigningKey(seed: account.deviceSeed)
        let builtApi = try Api(server: account.server, http: http)
        api = builtApi
        state = Feed(RemoteState())
        deviceLink = Link(relay: RelayPath(
            api: builtApi, sleep: sleep,
            prepare: { [holder] prior in
                guard let remote = holder.remote else { throw DeviceSecurity.Failure.storage }
                try await remote.prepareRelayAttempt(prior)
            },
            token: { [holder] in
                guard let remote = holder.remote else { throw DeviceSecurity.Failure.storage }
                return await remote.relayToken()
            },
            renew: { [holder] prior in
                guard let remote = holder.remote else { throw DeviceSecurity.Failure.storage }
                try await remote.renewRelayAttempt(prior)
            }
        ))
        holder.remote = self
        direct?.probe = { [holder] id, path in await holder.remote?.probeRead(id, path) }
        direct?.trace = log
        let directEvents = SerialChain()
        let directGate = DirectEventQueueGate()
        direct?.eventHandler = { [holder] machineId, env, header, size in
            let sid = header.session
            guard directGate.admit(sid, bytes: size) else { return }
            directEvents.run {
                await holder.remote?.handleDirectEvent(machineId: machineId, envelope: env, header: header, size: size)
                directGate.release(sid, bytes: size)
            }
        }
    }

    public nonisolated var current: RemoteState { state.value }
    public nonisolated func peer(_ machineId: String?) -> PeerState { state.value.peer(machineId) }

    // MARK: - Wiring with CoreHost

    public func installCoreHooks(_ hooks: CoreHooks) {
        self.hostHooks = hooks
    }

    public func publishSyncState(_ snapshot: RemoteState) {
        self.clockSkewed = !snapshot.clockWarning.isEmpty
        var updated = snapshot
        let peers = deviceLink.observe(updated)
        updated.machines = updated.machines.mapValues { machine in
            var projected = machine
            projected.linkState = peers[machine.id]
            return projected
        }
        // Keep sessionMachines in sync so decodeEvents / image() know owner machine IDs.
        for (sid, s) in updated.sessions {
            sessionMachines[sid] = s.machineId
        }
        syncDirectSubscriptions()
        if updated != state.value { state.set(updated) }
    }

    public func setWatchedSessions(_ sessionIds: [String]) {
        watchedSessionIds = sessionIds
        syncDirectSubscriptions()
    }

    private func syncDirectSubscriptions() {
        guard let direct else { return }
        var byMachine: [String: [String]] = [:]
        for sid in watchedSessionIds {
            guard let machineId = sessionMachines[sid], !machineId.isEmpty else { continue }
            var list = byMachine[machineId] ?? []
            if list.count < 4 && !list.contains(sid) {
                list.append(sid)
                byMachine[machineId] = list
            }
        }
        direct.setWatchedSessions(byMachine)
    }

    private func handleDirectEvent(machineId: String, envelope: PBEnvelope, header: DeviceSecurity.Header, size: Int) async {
        guard !closed else { return }
        let sid = header.session
        guard !sid.isEmpty, header.seq > 0, watchedSessionIds.contains(sid) else { return }
        guard sessionMachines[sid] == machineId, header.sender == machineId else { return }
        guard let seed = sessionKeys[sid], !seed.isEmpty else { return }
        do {
            let (h, ev) = try await openSecureMessage(
                envelope: envelope,
                want: Place(.event, machineId: machineId, sessionId: sid, seq: header.seq),
                as: PBEvent.self,
                history: false
            )
            guard !closed, h.sender == machineId, ev.seq == header.seq, ev.sessionID == sid else { return }
            let plain = PBCorePlainEvent.with {
                $0.seq = header.seq
                $0.event = ev
                $0.verified = true
                $0.approxBytes = Int64(max(size, 64))
            }
            await hostHooks?.onDirectEvents(sid, machineId, [plain])
        } catch {
            // Direct push is best-effort; drop errors silently without creating broken/retired events.
        }
    }

    public func setNetworkGeneration(_ gen: UInt64) {
        networkGeneration = gen
    }

    func notifyDirectStatusChanged() async {
        guard let direct, let hooks = hostHooks else { return }
        for machineId in state.value.machines.keys {
            let status = direct.status(machineId)
            let ready = status.state == .connected
            await hooks.onDirectStatus(machineId, ready, status.toCoreRouteReport())
        }
    }

    func machineIdForSession(_ sid: String) -> String {
        sessionMachines[sid] ?? state.value.sessions[sid]?.machineId ?? ""
    }

    func machineIdForSessionIfKnown(_ sid: String) -> String? {
        let id = machineIdForSession(sid)
        return id.isEmpty ? nil : id
    }

    // MARK: - Login & Token Executor

    public func keepLogin(_ saved: SavedLogin?, save: @escaping @Sendable (SavedLogin?) -> Void) {
        self.onLogin = save
        if let saved, token.isEmpty {
            token = saved.token
            tokenExpires = saved.expiresMs
        }
    }

    func prepareRelayAttempt(_ prior: Bool) async throws {
        if token.isEmpty { _ = try await relogin(prior) }
    }

    private func relayToken() -> String { token }

    func renewRelayAttempt(_ prior: Bool) async throws {
        _ = try await relogin(prior)
    }

    public func ensureToken(opId: UInt64, forceLogin: Bool, timeoutMs: Int64) async -> PBCoreTokenResultEvent {
        var ev = PBCoreTokenResultEvent()
        ev.opID = opId
        if forceLogin {
            dropToken()
            retireLogin()
        }
        if !forceLogin, !token.isEmpty, tokenExpires == 0 || tokenExpires >= clock() + 86_400_000 {
            ev.token = token
            ev.expiresMs = tokenExpires
            return ev
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

    func refreshNetworkSecurity() async throws {
        let gen = networkGeneration
        let task: Task<Void, Never>
        if let current = securityNetworkReset, current.0 == gen {
            task = current.1
        } else {
            let client = try securitySession()
            let previous = securityNetworkReset?.1
            task = Task {
                await previous?.value
                await client.networkChanged()
            }
            securityNetworkReset = (gen, task)
        }
        await task.value
        guard gen == networkGeneration else { throw PathChanged() }
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
            let response = try await withTimeout(timeout, sleep: sleep) { [api, account] in
                try await loginResponse(api: api, account: account)
            }
            try Task.checkCancellation()
            return signedIn(response)
        } catch let first as ConnectError where first.code == .permissionDenied {
            let response = try await withTimeout(timeout, sleep: sleep) { [api, account] in
                try await loginResponse(api: api, account: account)
            }
            try Task.checkCancellation()
            return signedIn(response)
        }
    }

    private func signedIn(_ res: PBLoginResponse) -> String {
        if !closed {
            token = res.token
            tokenExpires = res.expiresMs
            onLogin?(SavedLogin(token: res.token, expiresMs: res.expiresMs))
        }
        return res.token
    }

    func rejectCredential(_ rejected: String) {
        if rejected.isEmpty || token == rejected { dropToken() }
    }

    func dropToken() {
        token = ""
        tokenExpires = 0
        if !closed { onLogin?(nil) }
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
        let socket = deviceLink.router.path.openStream("/v1/stream", hello: hello)
        let leg = StreamLeg(legId: legId, socket: socket)
        streamLegs[legId] = leg
        let netGen = networkGeneration

        let (frames, queue) = AsyncStream<Data>.makeStream(
            bufferingPolicy: .bufferingOldest(max(frameQueue, 1)))
        leg.queue = queue
        let budget = StreamByteBudget(limit: frameQueueBytes)
        let parse = frameParser.withLock { $0 }
        let peerBudget = self.peerBudget
        let direct = self.direct

        leg.receiveTask = Task.detached { [weak self] in
            while let bytes = try? await socket.receive() {
                if let direct, let body = PeerResponder.body(bytes) {
                    direct.receive(Array(body), from: socket)
                    continue
                }
                if let direct, let body = PeerResponder.resultBody(bytes) {
                    direct.result(Array(body), from: socket)
                    continue
                }
                if let body = PeerResponder.body(bytes) {
                    if peerBudget.reserve(65536) {
                        Task {
                            if let self {
                                await self.handlePeerFrame(body, socket: socket, netGen: netGen)
                            } else {
                                peerBudget.release(65536)
                            }
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
        leg.closed = true
        leg.queue?.finish()
        leg.receiveTask?.cancel()
        leg.processTask?.cancel()
        leg.socket.cancel()
    }

    private func handlePeerFrame(_ bytes: Data, socket: any FrameStream, netGen: UInt64) async {
        defer { peerBudget.release(65536) }
        guard !closed, networkGeneration == netGen, let activeLegId, streamLegs[activeLegId]?.socket === socket else { return }
        do {
            let record = try await securitySession().record()
            let reply = await peerResponder.receive(
                bytes, record: record, seed: account.deviceSeed, now: UInt64(max(1, clock())))
            guard !closed, networkGeneration == netGen,
                  let legId = self.activeLegId, streamLegs[legId]?.socket === socket,
                  let reply,
                  let current = try? await securitySession().record(),
                  current.member,
                  max(UInt64(max(1, clock())), current.floor) < (try await securitySession().leaseExpiration()),
                  try DeviceSecurity.stateHash(current.state) == DeviceSecurity.stateHash(record.state)
            else { return }
            socket.send(reply)
        } catch { /* Authenticated peer refusal is optional; relay stays available. */ }
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
                    direct?.attach(leg.socket)
                    await callbacks.onReady(legId, ready.serverTimeMs)
                case .heartbeat?:
                    await emit { $0.heartbeat = true }
                case .machineUpdated(let u)?:
                    let decoded = await decodeMachine(u.machine, trackRef: true)
                    await emit { $0.machineUpdated = decoded }
                case .machineRemoved(let u)?:
                    await emit { $0.machineRemovedID = u.machineID }
                case .sessionUpdated(let u)?:
                    let decoded = await decodeSession(u.session, trackRef: true)
                    await emit { $0.sessionUpdated = decoded }
                case .eventsAppended(let a)?:
                    let decoded = await decodeEvents(
                        a.events, machineId: machineIdForSession(a.sessionID),
                        sessionId: a.sessionID, history: false, trackRef: true)
                    await emit {
                        $0.eventsAppended.sessionID = a.sessionID
                        $0.eventsAppended.events = decoded
                    }
                case .accountPrefsUpdated(let u)?:
                    if u.object.objectID == projectPrefsID {
                        let decoded = try await decodeAccountPrefs(u.object, trackRef: true)
                        await emit { $0.accountPrefsUpdated = decoded }
                    }
                case .deviceUpdated(let u)?:
                    let decoded = await decodeDevice(u.device)
                    await emit { $0.deviceUpdated = decoded }
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

    // MARK: - Device Metadata & Lifecycle

    public func renameDevice(_ id: String, name: String) async throws {
        guard id == account.deviceId else {
            throw CommandError("failed_precondition", L10nMessage.errorsIosOnlyTheDeviceItselfCanChangeItsName())
        }
        try await changeDeviceMeta(id) { $0.name = name }
    }

    public func describeSelf(platform: String, description: String) async throws {
        try await changeDeviceMeta(account.deviceId) {
            $0.platform = platform
            $0.description_p = description
        }
    }

    func changeDeviceMeta(_ id: String, _ change: (inout PBDeviceMeta) -> Void) async throws {
        for _ in 0..<5 {
            _ = try await securitySession().sync()
            let listed = try await withAuthRetry { [api] headers in
                try await api.client.listDevices(request: .init(), headers: headers).value()
            }
            guard let raw = listed.devices.first(where: { $0.id == id }) else {
                throw CommandError("not_found", L10nMessage.settingsDeviceNotFound())
            }
            let decoded = await decodeDevice(raw)
            var meta = decoded.hasMeta ? decoded.meta : PBDeviceMeta()
            change(&meta)
            do {
                let env = try await securitySession().seal(
                    purpose: .deviceMeta, request: id, seq: decoded.metaRev + 1,
                    plaintext: meta.serializedData())
                var req = PBPutDeviceMetaRequest()
                req.deviceID = id
                req.expectedRevision = decoded.metaRev
                req.meta = env
                let request = req
                let result = try await withAuthRetry { [api] headers in
                    try await api.client.putDeviceMeta(request: request, headers: headers).value()
                }
                if !result.applied { continue }
                await refreshDevices()
                return
            } catch DeviceSecurity.Failure.conflict {
                continue
            }
        }
        throw CommandError("aborted", L10nMessage.errorsDeviceInformationIsChangingRetry())
    }

    public nonisolated func close() {
        deviceLink.closeStreams()
        direct?.close()
        Task { await self.closeActor() }
    }

    private func closeActor() {
        guard !closed else { return }
        closed = true
        loginGeneration += 1
        loginFlight?.task.cancel()
        loginFlight = nil
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
        direct?.close()
    }

    // MARK: - Transitional Step 3-4 Forwarding to Core

    public func onSessionRemoved(_ action: @escaping @Sendable (String) async -> Void) {
        sessionRemovalHandler = action
    }

    @discardableResult
    public func refresh() async -> Bool {
        if let hooks = hostHooks {
            await hooks.onRefresh()
            return state.value.conn == .online
        }
        return false
    }

    public func refreshDevices() async {
        await hostHooks?.onRefreshDevices()
    }

    public func fetchSession(_ id: String) async throws -> Bool {
        if state.value.sessions[id] != nil { return true }
        if let hooks = hostHooks {
            await hooks.onFetchSession(id)
        }
        return state.value.sessions[id] != nil
    }

    @discardableResult
    public func loadMoreSessions() async throws -> Bool {
        await hostHooks?.onLoadMoreSessions()
        return state.value.sessionsNext == nil
    }

    public func changePrefs(_ op: PrefsOp) async throws {
        guard let hooks = hostHooks else {
            throw CommandError("unavailable", L10nMessage.connectionNoResponse())
        }
        try await hooks.onSavePrefs(op)
    }

    // MARK: - Demo / Layout Fixtures

    public func showForTest(
        state shown: RemoteState,
        modelLists: [(machineId: String, agent: PBAgent, models: PBListModelsResponse)] = []
    ) {
        state.set(shown)
        for (sid, s) in shown.sessions {
            sessionMachines[sid] = s.machineId
            sessionKeys[sid] = Data(repeating: 1, count: keySize)
        }
        for m in modelLists {
            let response = m.models
            models["\(m.machineId)/\(m.agent.rawValue)"] = ModelList(
                task: Task<PBListModelsResponse, Error> { response }, done: true)
        }
    }

    func report(_ e: Error) {
        log("remote-agent: " + errorText(e))
    }
}

private final class DirectEventQueueGate: @unchecked Sendable {
    private let maxPerSession = 16
    private let maxTotalBytes = 512 * 1024
    private let lock = NSLock()
    private var counts: [String: Int] = [:]
    private var totalBytes = 0

    func admit(_ sessionId: String, bytes: Int) -> Bool {
        let cost = max(bytes, 64)
        lock.lock()
        defer { lock.unlock() }
        let count = counts[sessionId, default: 0]
        if count >= maxPerSession || totalBytes + cost > maxTotalBytes {
            return false
        }
        counts[sessionId] = count + 1
        totalBytes += cost
        return true
    }

    func release(_ sessionId: String, bytes: Int) {
        let cost = max(bytes, 64)
        lock.lock()
        defer { lock.unlock() }
        totalBytes = max(0, totalBytes - cost)
        if let count = counts[sessionId] {
            if count <= 1 {
                counts.removeValue(forKey: sessionId)
            } else {
                counts[sessionId] = count - 1
            }
        }
    }
}
