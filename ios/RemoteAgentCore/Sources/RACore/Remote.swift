// Remote keeps the app in sync with the server, as web/src/remote.ts and
// Android's core/Remote.kt do: it logs in, holds the decrypted machines,
// sessions and events, follows the push stream, and sends sealed commands.
//
// It is an actor: all state changes run one at a time and interleave only
// where they await, like the page's event loop. So after every await the
// code checks again that what it was doing is still wanted: each connection
// has a generation, and a step of a replaced one changes nothing. What it
// knows is published as value snapshots (`state`, `events`), which a screen
// may read from any thread.
//
// The rest of the actor is in RemoteLists.swift (machines, sessions, pages,
// preferences, devices), RemoteEvents.swift (histories), RemoteCommands.swift
// and RemoteTerminal.swift.
import Connect
import Foundation
import RAProto
import os

public let commandTTLMs: Int64 = 60_000

/// How long after the lists could not be fetched they are asked for again; twice as long each time, up to half a minute.
let listRetryMs: Int64 = 2_000

/// A sealed command ready to send; resending it reuses its request ID and deadline.
public final class PreparedCommand: @unchecked Sendable {
    public let machineId: String
    public let sessionId: String
    public let requestId: String
    public let deadlineMs: Int64
    public let envelope: PBEnvelope
    let command: PBCommand?
    private let sealLock = NSLock()
    private var sealJob: Task<PBEnvelope, Error>?
    private var sealID: UUID?
    private var sealState: Data?
    private var everAuthorized = false
    private func clearAuthorization(_ id: UUID) {
        sealLock.lock()
        defer { sealLock.unlock() }
        if sealID == id {
            sealJob = nil
            sealID = nil
            sealState = nil
        }
    }
    private func authorized(_ id: UUID) {
        sealLock.lock()
        defer { sealLock.unlock() }
        if sealID == id { everAuthorized = true }
    }
    func authorizationTask(
        stateHash: Data, _ make: @escaping @Sendable (Bool) -> Task<PBEnvelope, Error>
    ) -> Task<PBEnvelope, Error> {
        sealLock.lock()
        defer { sealLock.unlock() }
        if let sealJob, sealState == stateHash { return sealJob }
        let id = UUID()
        sealID = id
        sealState = stateHash
        let verifyObservation = !everAuthorized
        let task = Task {
            do {
                let envelope = try await make(verifyObservation).value
                self.authorized(id)
                return envelope
            } catch {
                self.clearAuthorization(id)
                throw error
            }
        }
        sealJob = task
        return task
    }

    public init(
        machineId: String, sessionId: String, requestId: String, deadlineMs: Int64,
        envelope: PBEnvelope
    ) {
        self.machineId = machineId
        self.sessionId = sessionId
        self.requestId = requestId
        self.deadlineMs = deadlineMs
        self.envelope = envelope
        self.command = nil
    }
    init(
        machineId: String, sessionId: String, requestId: String, deadlineMs: Int64,
        command: PBCommand
    ) {
        self.machineId = machineId
        self.sessionId = sessionId
        self.requestId = requestId
        self.deadlineMs = deadlineMs
        self.command = command
        self.envelope = PBEnvelope()
    }
}

/// A login as the server gave it, for the app to keep between launches
/// (architecture §10 「WebSocket」: both ends keep the last token and open the
/// stream with it). It is a bearer credential: the app stores it with the
/// account's keys, and it describes itself without the token.
public struct SavedLogin: Equatable, Sendable, CustomStringConvertible,
    CustomDebugStringConvertible, CustomReflectable
{
    public let token: String
    /// When the server stops taking it, in milliseconds since 1970.
    public let expiresMs: Int64

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

public actor Remote {
    public nonisolated let account: Account
    public nonisolated let api: Api
    /// Machines, sessions, devices and the connection, as far as they are known.
    public nonisolated let state: Feed<RemoteState>

    nonisolated let http: HTTP
    nonisolated let keys: Keys?
    nonisolated let device: SigningKey
    nonisolated let cache: (any CipherCache)?
    nonisolated let log: @Sendable (String) -> Void
    nonisolated let clock: @Sendable () -> Int64
    nonisolated let sleep: Sleeper
    nonisolated let jitter: @Sendable () -> Double
    /// Stream frames waiting to be handled before the stream is dropped and caught up from history.
    nonisolated let frameQueue: Int
    nonisolated let frameQueueBytes: Int
    nonisolated let historyIdleMs: Int64
    nonisolated let historyIdleCount: Int
    nonisolated let historyIdleBytes: Int64
    nonisolated let streamOpenMs: Int64
    /// How long a lost connection is only being remade, before the server counts as out of reach.
    nonisolated let linkGraceMs: Int64

    var machines: [String: MachineView] = [:]
    var sessions: [String: SessionView] = [:]
    var sessionKeys: [String: Data] = [:]
    var buffers: [String: Buffer] = [:]
    /// The histories handed out, by session. Read from any thread: `events` is not isolated.
    nonisolated let eventFeeds = OSAllocatedUnfairLock<[String: Feed<SessionEvents>]>(
        initialState: [:])
    var conn = ConnState.connecting
    var error = ""
    var clockWarning = ""
    var loaded = false
    var offlineSince: Int64 = 0
    var errorDetail = ""
    // The device has a network, by the system's word; a server whose address
    // does not resolve says the same of a network that only looks up.
    var network = true
    var unresolved = false
    var synced = false
    var grace: Scheduled?
    var waited = false
    var prefs: PrefsObject?
    var devices: [String: DeviceView] = [:]
    var archiveMachines: Set<String> = []
    var trustedMembers: [String: DeviceSecurity.Member] = [:]
    var trustedMetadata: [String: PBEnvelope] = [:]
    struct DirectoryLoad {
        let id = UUID()
        let head: Data
        let version: Int
        let task: Task<Void, Error>
    }
    var directoryLoad: DirectoryLoad?
    var directoryVersion = 0
    var directoryHead: Data?
    var recoveryObjectsPending = true
    var authorityDeviceId = ""
    var leaseEndMs: UInt64 = 0
    var liveSnapshots: [String: PBLiveSnapshot] = [:]
    var liveErrors: [String: String] = [:]
    var liveJobs: [String: Task<Void, Never>] = [:]

    // Pushes seen per machine, session and machine removal, so that a list
    // fetched before a push cannot undo it.
    var machinePushes: [String: Int] = [:]
    var sessionPushes: [String: Int] = [:]
    var removedPushes: [String: Int] = [:]
    // And per device, registered, changed or removed: the same for the list of devices.
    var devicePushes: [String: Int] = [:]

    var token = ""
    var tokenExpires: Int64 = 0
    /// Hears of every login the server gives and of the one in use being refused (`keepLogin`).
    var onLogin: (@Sendable (SavedLogin?) -> Void)?
    var ws: Socket?
    var retry: Int64 = 1000
    var running = false
    var removed = false
    var closed = false
    let signingIn = Gate()
    var reconnect: Scheduled?
    var connecting: Task<Void, Never>?
    var opening: Scheduled?
    // Bumped whenever the connection in use changes, and by stop: a
    // connection attempt suspended in login when the app went to the
    // background must not open the stream afterwards, and a frame of a
    // replaced socket must not be taken.
    var generation = 0
    /// Putting what the cache holds in place, started once; no connection is made before it is done.
    var restoring: Task<Void, Never>?
    var historySweep: Task<Void, Never>?
    var useSeq = 0
    var timerSeq = 0

    // The listing of ended sessions comes a page at a time. next is where the
    // page after the ones fetched starts, firstEnd where the first page ended:
    // what the cache keeps is that page. A fetch of the first page starts the
    // listing over (listing): a page asked for before that continued the old
    // one and is asked for again from the new cursor. While a first page is on
    // its way (listsInFlight) no other page is asked for. Of several first
    // pages in flight, as when a stream was replaced while the one before was
    // still being listed, only the latest asked for is put in place (refreshRun).
    var listsInFlight = 0
    var listWaiters: [CheckedContinuation<Void, Never>] = []
    var refreshRun = 0
    let applying = Gate()
    var listRetry: Scheduled?
    var listBackoff = listRetryMs
    // The same for the preferences while there is no copy of them at all: nothing of them can be changed until one is read.
    var prefsRetry: Scheduled?
    var prefsBackoff = listRetryMs
    var next: SessionPlace?
    var firstEnd: SessionPlace?
    var firstPage = false
    var listing = 0
    var pageJob: Task<Bool, Error>?
    var allJob: Task<Void, Error>?
    var byId: [String: Task<Bool, Error>] = [:]
    var pageError = ""
    var listed: Set<String> = []
    var models: [String: ModelList] = [:]

    /// What the lifecycle calls post. They are not isolated, so that a screen
    /// calls them without awaiting, and they take effect in the order called.
    enum Op: Sendable {
        case start, stop, close, reconnectNow, clearRemoved
        case keepLogin(SavedLogin?, @Sendable (SavedLogin?) -> Void)
        case setNetwork(Bool)
        case touch(String)
        case followers(String)
        case retryEvents(String)
    }

    private nonisolated let ops: AsyncStream<Op>.Continuation

    nonisolated func post(_ op: Op) { ops.yield(op) }

    /// A timer that is someone's to cancel; `id` tells its firing from a later one's in the same place.
    struct Scheduled {
        let id: Int
        let task: Task<Void, Never>
    }

    /// One at a time, for steps that await in the middle. Only touched on the actor.
    final class Gate {
        var held = false
        var waiters: [CheckedContinuation<Void, Never>] = []
    }

    final class Buffer {
        var unverifiedLegacy = false
        var verifiedSeqs = Set<UInt64>()
        var unavailableSeqs = Set<UInt64>()
        var verifiedEvents = PagedList<PBEvent>()
        var events = PagedList<PBEvent>()
        var bytes: Int64 = 0
        var usedAt: Int64 = 0
        var useOrder = 0
        var pending: [UInt64: PBEvent] = [:]
        var fetching = false
        var refetch = false
        var loaded = false
        var error: String?
        // The last fetch failed; cleared by the next one that works.
        var fetchError: String?
    }

    /// A machine's models as last asked for, and whether that is done with.
    struct ModelList {
        let task: Task<PBListModelsResponse, Error>
        var done = false
        var failed = false
    }

    /// - Parameters:
    ///   - http: the app's HTTP stack; tests build one from their own URLSession configuration.
    ///   - log: gets error texts only, never content or keys.
    ///   - clock: the time in milliseconds since 1970.
    ///   - sleep: how every wait here passes, so that tests need not wait.
    ///   - jitter: a number in 0..<1 that spreads reconnects apart.
    public nonisolated let authorityGate = AuthorityGate()
    var management: AuthorityCoordinator?
    var recoveryPending = false
    var recoveryReadyMs: UInt64 = 0
    var recoveryStatusStale = true
    let securityStorage: (any DeviceSecurity.Storage)?
    var deviceSecurity: DeviceSecurityClient?
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
        historyIdleMs: Int64 = 60_000,
        historyIdleCount: Int = 8,
        historyIdleBytes: Int64 = 16 * 1024 * 1024,
        streamOpenMs: Int64 = 15_000,
        linkGraceMs: Int64 = 10_000,
        securityStorage: (any DeviceSecurity.Storage)? = nil
    ) throws {
        self.account = account
        self.securityStorage = securityStorage
        self.http = http
        self.cache = cache
        self.log = log
        self.clock = clock
        self.sleep = sleep
        self.jitter = jitter
        self.frameQueue = frameQueue
        self.frameQueueBytes = frameQueueBytes
        self.historyIdleMs = historyIdleMs
        self.historyIdleCount = historyIdleCount
        self.historyIdleBytes = historyIdleBytes
        self.streamOpenMs = streamOpenMs
        self.linkGraceMs = linkGraceMs
        keys = account.hasRoot ? try? Keys.derive(root: account.root) : nil
        device = try SigningKey(seed: account.deviceSeed)
        api = try Api(server: account.server, http: http)
        state = Feed(RemoteState())
        let (stream, ops) = AsyncStream<Op>.makeStream()
        self.ops = ops
        Task { [weak self] in
            for await op in stream {
                guard let self else { return }
                await self.perform(op)
            }
        }
    }

    deinit {
        ops.finish()
    }

    /// The snapshot now, for code that cannot await.
    public nonisolated var current: RemoteState { state.value }

    // --- lifecycle ---

    /// Connects and keeps reconnecting until `stop`.
    public nonisolated func start() { ops.yield(.start) }

    /// Disconnects, e.g. when the app goes to the background.
    public nonisolated func stop() { ops.yield(.stop) }

    /// Whether the device has a network at all. Losing it ends the connection
    /// at once rather than at the next ping; getting one starts over.
    public nonisolated func setNetwork(_ available: Bool) { ops.yield(.setNetwork(available)) }

    /// Ends this Remote for good.
    public nonisolated func close() {
        authorityGate.set(active: false, protectedDataAvailable: false)
        ops.yield(.close)
    }

    /// Reconnects now instead of waiting for the backoff, e.g. after the network came back.
    public nonisolated func reconnectNow() {
        ops.yield(.reconnectNow)
        Task { await self.authorityForegroundTick() }
    }

    /// Tries the login again after the server turned this device away.
    public nonisolated func retryLogin() {
        ops.yield(.clearRemoved)
        ops.yield(.start)
    }

    /// Hands over the login kept from the last run, and where to keep the
    /// next one. Called before `start`, it saves a cold start the two login
    /// calls: the stream is opened with the kept token, and only the server
    /// refusing it (close code 1008) drops it and logs in again.
    ///
    /// `onChange` gets every login the server gives from then on, and nil
    /// when the one in use stops being any good: the server refused it, or
    /// no longer knows this device. It is called on the actor, in the order
    /// things happened, and must not block for long.
    public nonisolated func keepLogin(
        _ saved: SavedLogin?, onChange: @escaping @Sendable (SavedLogin?) -> Void
    ) {
        ops.yield(.keepLogin(saved, onChange))
    }

    /// One posted call. Nothing here awaits: what does is started and left to
    /// interleave, so the calls keep their order.
    private func perform(_ op: Op) {
        if closed { return }
        switch op {
        case .start:
            if running || removed { return }
            running = true
            beginConnect()
        case .stop:
            halt()
        case .close:
            halt()
            closed = true
            for job in liveJobs.values { job.cancel() }
            liveJobs.removeAll()
            liveSnapshots.removeAll()
            liveErrors.removeAll()
            if let deviceSecurity { Task { await deviceSecurity.close() } }
            onLogin = nil
            historySweep?.cancel()
            historySweep = nil
            ops.finish()
        case .setNetwork(let available):
            if network == available { return }
            network = available
            unresolved = false
            grace?.task.cancel()
            grace = nil
            waited = false
            if running && !removed {
                retry = 1000
                beginConnect()
            } else {
                publish()
            }
        case .reconnectNow:
            if !running || removed { return }
            retry = 1000
            beginConnect()
        case .clearRemoved:
            removed = false
            publish()
        case .keepLogin(let saved, let onChange):
            onLogin = onChange
            // A login made meanwhile is newer than what was kept.
            if token.isEmpty, let saved, !saved.token.isEmpty {
                token = saved.token
                tokenExpires = saved.expiresMs
            }
        case .touch(let sid):
            touch(sid)
        case .followers(let sid):
            followersChanged(sid)
        case .retryEvents(let sid):
            buffers[sid]?.error = nil
            Task { await self.fetchEvents(sid) }
        }
    }

    private func halt() {
        running = false
        if let deviceSecurity { Task { await deviceSecurity.stopPolling() } }
        generation += 1
        reconnect?.task.cancel()
        reconnect = nil
        listRetry?.task.cancel()
        listRetry = nil
        prefsRetry?.task.cancel()
        prefsRetry = nil
        connecting?.cancel()
        connecting = nil
        opening?.task.cancel()
        opening = nil
        ws?.cancel()
        ws = nil
        conn = .offline
        error = ""
        errorDetail = ""
        unresolved = false
        publish()
    }

    /// Runs `run` on the actor after `ms` unless cancelled first. `run` gets
    /// the timer's ID: a timer cancelled just as it fired still gets here, and
    /// must find its place taken or empty.
    func schedule(_ ms: Int64, _ run: @escaping @Sendable (isolated Remote, Int) async -> Void)
        -> Scheduled
    {
        timerSeq += 1
        let id = timerSeq
        let sleep = self.sleep
        let task = Task { [weak self] in
            guard (try? await sleep(ms)) != nil, !Task.isCancelled, let self else { return }
            await run(self, id)
        }
        return Scheduled(id: id, task: task)
    }

    func acquire(_ gate: Gate) async {
        if !gate.held {
            gate.held = true
            return
        }
        await withCheckedContinuation { gate.waiters.append($0) }
    }

    func release(_ gate: Gate) {
        // Handed straight to the next in line, which finds it held.
        if gate.waiters.isEmpty { gate.held = false } else { gate.waiters.removeFirst().resume() }
    }

    func publish() {
        if conn == .online { offlineSince = 0 } else if offlineSince == 0 { offlineSince = clock() }
        if conn == .online || !running {
            grace?.task.cancel()
            grace = nil
            waited = false
        } else if !waited && grace == nil {
            grace = schedule(linkGraceMs) { me, id in
                guard me.grace?.id == id else { return }
                me.grace = nil
                me.waited = true
                me.publish()
            }
        }
        var s = RemoteState()
        s.machines = machines
        s.sessions = sessions
        s.conn = conn
        s.error = error
        s.clockWarning = clockWarning
        s.loaded = loaded
        s.offlineSinceMs = offlineSince
        s.prefs = prefs
        s.devices = devices
        s.authorityDeviceId = authorityDeviceId
        s.leaseEndMs = leaseEndMs
        s.recoveryPending = recoveryPending
        s.recoveryReadyMs = recoveryReadyMs
        s.recoveryStatusStale = recoveryStatusStale
        s.removed = removed
        s.sessionsNext = next
        s.loadingSessions = pageJob != nil
        s.sessionsError = pageError
        s.sessionsListed = firstPage
        if conn == .online {
            s.link = .online
        } else if !network || unresolved {
            s.link = .noNetwork
        } else if !waited {
            s.link = .waiting
        } else {
            s.link = .noServer
        }
        s.synced = synced
        s.errorDetail = errorDetail
        if s != state.value { state.set(s) }
    }

    /// Logs in, one login at a time: the server keeps few challenges per
    /// device, and a denial has to mean what it says. A server restarted
    /// between the two calls forgets the challenge; denied a fresh one too,
    /// it no longer knows this device, and nothing connects until it joins again.
    func signIn() async throws -> String {
        await acquire(signingIn)
        defer { release(signingIn) }
        // Given up on while waiting for its turn.
        try Task.checkCancellation()
        do {
            return signedIn(try await loginResponse(api: api, account: account))
        } catch let first as ConnectError where first.code == .permissionDenied {
            do {
                return signedIn(try await loginResponse(api: api, account: account))
            } catch let e as ConnectError where e.code == .permissionDenied {
                // The server does not know this device: its token is no login either.
                dropToken()
                removed = true
                running = false
                generation += 1
                reconnect?.task.cancel()
                reconnect = nil
                ws?.close()
                ws = nil
                conn = .offline
                publish()
                throw e
            }
        }
    }

    private func signedIn(_ res: PBLoginResponse) -> String {
        tokenExpires = res.expiresMs
        // A Remote closed while the login was on its way keeps nothing: the account may be gone from this device.
        if !closed { onLogin?(SavedLogin(token: res.token, expiresMs: res.expiresMs)) }
        return res.token
    }

    /// Forgets the token in use, here and wherever the app keeps it.
    private func dropToken() {
        token = ""
        tokenExpires = 0
        if !closed { onLogin?(nil) }
    }

    /// Waits until what the cache holds is in place. It is read once, however
    /// many connections wait: one made while it was still being read would
    /// have its lists overwritten by what was cached before them.
    private func restored() async {
        guard let cache else { return }
        if restoring == nil { restoring = Task { await self.readCache(cache) } }
        await restoring?.value
    }

    private func readCache(_ cache: any CipherCache) async {
        do {
            let identity = try securityIdentity()
            trustedMembers = Dictionary(
                uniqueKeysWithValues: identity.1.core.members.map { ($0.id, $0) })
            authorityDeviceId = identity.1.core.authorityID
            leaseEndMs = try await securitySession().leaseExpiration()
            archiveMachines=try await securitySession().historicalMachines()
        } catch {
            report(error)
            return
        }
        for var m in await cache.machines() {
            m.online = false
            await putMachine(m, save: false)
        }
        for s in await cache.sessions() {
            listed.insert(s.sessionID)
            await putSession(s, save: false, publish: false)
        }
        if let blob = await cache.blob(name: projectPrefsID) {
            do {
                await putPrefs(try PBAccountPrefsObject(serializedBytes: blob), save: false)
            } catch {
                report(error)
            }
        }
        if !machines.isEmpty || !sessions.isEmpty { loaded = true }
        publish()
    }

    /// Replaces the whole attempt, including a login suspended before a socket exists.
    func beginConnect() {
        if !running || removed { return }
        generation += 1
        reconnect?.task.cancel()
        reconnect = nil
        connecting?.cancel()
        opening?.task.cancel()
        opening = nil
        let old = ws
        ws = nil
        old?.cancel()
        connecting = Task { await self.connect() }
    }

    private func connect() async {
        if !running { return }
        let gen = generation
        conn = .connecting
        publish()
        await restored()
        if !running || gen != generation { return }
        do {
            // A token kept from the last connection saves the two login calls
            // each time the app comes back. One about to expire is not worth a
            // refused connection; the server refusing it closes the stream,
            // and the next try logs in.
            if token.isEmpty || tokenExpires < clock() + 86_400_000 {
                token = try await withTimeout(streamOpenMs, sleep: sleep) {
                    try await self.signIn()
                }
            }
        } catch {
            // Replaced or stopped meanwhile: that attempt says what happens next.
            if running && gen == generation {
                fail(error is TimedOut ? URLError(.timedOut) as Error : error)
            }
            return
        }
        if !running || gen != generation { return }
        var frame = PBPeerFrame()
        frame.hello.token = token
        guard let hello: Data = try? frame.serializedData() else { return }
        let socket = Socket(
            http: http, url: api.socketURL("/v1/stream"), hello: hello, sleep: sleep)
        ws = socket
        // Bounded: a reader that falls behind drops the stream and catches up
        // from history after reconnecting, instead of queueing without end.
        // What was queued before that is still handled; nothing is skipped.
        let (frames, queue) = AsyncStream<Data>.makeStream(
            bufferingPolicy: .bufferingOldest(max(frameQueue, 1)))
        let budget = StreamByteBudget(limit: frameQueueBytes)
        Task.detached {
            while let bytes = try? await socket.receive() {
                guard budget.reserve(bytes.count) else {
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
        opening = schedule(streamOpenMs) { me, id in
            guard me.opening?.id == id else { return }
            me.opening = nil
            if me.ws === socket && gen == me.generation && me.conn != .online {
                me.ws = nil
                me.generation += 1
                socket.cancel()
                me.fail(UserError("连接超时，正在重连"))
            }
        }
        Task { await self.follow(socket, frames, gen, budget) }
    }

    /// Handles a stream's frames in order until it ends or is replaced.
    private func follow(_ socket: Socket, _ frames: AsyncStream<Data>, _ gen: Int, _ budget: StreamByteBudget) async {
        for await bytes in frames {
            defer { budget.release(bytes.count) }
            if ws !== socket || gen != generation { break }
            do {
                let f = try PBServerFrame(serializedBytes: bytes)
                if case .ready? = f.kind {
                    opening?.task.cancel()
                    opening = nil
                }
                await onFrame(f)
            } catch {
                report(error)
                break
            }
            if ws !== socket || gen != generation { break }
        }
        socket.cancel()
        let end = await socket.ended()
        guard ws === socket else { return }
        ws = nil
        generation += 1
        opening?.task.cancel()
        opening = nil
        // Only an explicit refusal invalidates the token.
        if end.closeCode == policyViolation { dropToken() }
        fail(end.error ?? UserError("与 server 的连接断开"))
    }

    func fail(_ e: Error) {
        conn = .offline
        error = errorText(e)
        errorDetail = detailText(e)
        unresolved = unresolvedHost(e)
        publish()
        if !running { return }
        let delayMs = retry + Int64(jitter() * Double(retry) * 0.25)
        retry = min(retry * 2, 30_000)
        reconnect = schedule(delayMs) { me, id in
            guard me.reconnect?.id == id else { return }
            // Not this timer's to cancel from beginConnect while it runs.
            me.reconnect = nil
            me.beginConnect()
        }
    }

    private func onFrame(_ f: PBServerFrame) async {
        switch f.kind {
        case .ready?:
            let gen = generation
            retry = 1000
            conn = .online
            error = ""
            errorDetail = ""
            unresolved = false
            publish()
            // Subscribed first, then catch up: nothing falls in between.
            _ = await refresh()
            if !running || gen != generation { return }
            let followed = eventFeeds.withLock { feeds in
                feeds.filter { $0.value.followers > 0 }.map(\.key)
            }
            for sid in Set(buffers.keys).union(followed) {
                Task {
                    await self.loadEvents(sid)
                    await self.fetchEvents(sid)
                }
            }
            Task { await self.loadPrefs() }
            Task { await self.loadDevices() }
            startSecurityPolling()
        case .accountPrefsUpdated(let u)?:
            if u.object.objectID == projectPrefsID { await putPrefs(u.object) }
        case .deviceUpdated(let u)?:
            bump(&devicePushes, u.device.id)
            await putDevice(u.device)
            publish()
        case .deviceRemoved?:
            await loadDevices()
        case .machineRemoved?:
            _ = await refresh()
        case .machineUpdated(let u)?:
            await machineUpdated(u.machine)
        case .sessionUpdated(let u)?:
            bump(&sessionPushes, u.session.sessionID)
            do { try await checkDirectory(u.session.machineID, envelopes: [u.session.key, u.session.meta]) } catch { report(error) }
            await putSession(u.session, save: onFirstPage(u.session))
        case .eventsAppended(let a)?:
            let sid = a.sessionID
            if sessions[sid] != nil && buffers[sid] != nil {
                await ingest(sid, a.events, fillGaps: true, save: true, history: false)
            } else {
                if var s = sessions[sid] {
                    for e in a.events { s.lastSeq = max(s.lastSeq, e.seq) }
                    sessions[sid] = s
                }
                publish()
            }
        // A heartbeat, a call meant for a machine, or a frame of a later version.
        default:
            break
        }
    }

    func bump(_ pushes: inout [String: Int], _ id: String) {
        pushes[id, default: 0] += 1
    }

    /// Shows fixed lists and histories without a server, for layout tests and
    /// screenshots of the screens. Only for a Remote that was never started:
    /// nothing is fetched, and commands fail as they would offline.
    public func showForTest(
        state shown: RemoteState,
        events: [String: [PBEvent]] = [:],
        modelLists: [(machineId: String, agent: PBAgent, models: PBListModelsResponse)] = [],
        live: Bool = false
    ) {
        precondition(!running, "showForTest on a started Remote")
        state.set(shown)
        for m in modelLists {
            let response = m.models
            models["\(m.machineId)/\(m.agent.rawValue)"] = ModelList(
                task: Task<PBListModelsResponse, Error> { response }, done: true)
        }
        for (sid, list) in events {
            var shownEvents = SessionEvents(events: PagedList(list), loaded: true)
            if live { shownEvents.live = Self.liveForTest(list, planOpen: shown.sessions[sid]?.info?.planOpen == true) }
            eventFeed(sid).set(shownEvents)
        }
    }

    /// What the computer would say waits in a session whose history is `list`: its pending approvals and
    /// questions, the messages still in its queue, and its plan while the session says one is open.
    /// Sample data has no computer to ask.
    static func liveForTest(_ list: [PBEvent], planOpen: Bool) -> PBLiveSnapshot {
        let d = derive(list)
        var snapshot = PBLiveSnapshot()
        snapshot.running = d.running
        snapshot.actions = list.filter { event in
            switch event.kind {
            case .approvalRequested(let a)?: return d.approvals[a.approvalID] != nil
            case .questionAsked(let q)?: return d.questions[q.questionID] != nil
            case .inputState(let st)?: return d.inputs[st.requestID] == .queued || d.inputs[st.requestID] == .heldByCli
            default: return false
            }
        }
        if planOpen, let plan = d.plan {
            snapshot.planEventSeq = list.last { if case .planProposed? = $0.kind { true } else { false } }?.seq ?? 1
            snapshot.planScreenHash = Data(count: 32)
            snapshot.planScreen = plan.text
        }
        return snapshot
    }

    func report(_ e: Error) {
        // Only the error text: never content or keys.
        if let failure = e as? DeviceSecurity.Failure, failure == .clock {
            clockWarning = "本机时间比安全状态慢 5 分钟以上，控制已暂停；校准系统时间后重试"
        }
        log("remote-agent: " + errorText(e))
        error = errorText(e)
    }
}
