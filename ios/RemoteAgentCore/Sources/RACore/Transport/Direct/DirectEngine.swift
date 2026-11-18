import Connect
import CryptoKit
import Foundation
import RAProto
import SwiftProtobuf

public protocol DirectPreferenceStore: Sendable {
    func isEnabled(server: String, deviceId: String, machineId: String) -> Bool
    func setEnabled(server: String, deviceId: String, machineId: String, enabled: Bool) throws
}

/// "Direct: automatic / off" per this device, account and computer; automatic by default.
public struct UserDefaultsDirectPreferences: DirectPreferenceStore, @unchecked Sendable {
    private let defaults: UserDefaults
    public init(_ defaults: UserDefaults = .standard) { self.defaults = defaults }
    private func key(_ s: String, _ d: String, _ m: String) -> String { "remote-agent-direct:\(s):\(d):\(m)" }
    public func isEnabled(server: String, deviceId: String, machineId: String) -> Bool { defaults.string(forKey: key(server, deviceId, machineId)) != "off" }
    public func setEnabled(server: String, deviceId: String, machineId: String, enabled: Bool) throws { defaults.set(enabled ? "auto" : "off", forKey: key(server, deviceId, machineId)) }
}

public enum DirectState: Sendable, Equatable { case unsupported, connected, connecting, none }
/// Only causes this side can establish. `noResponse` means the computer never answered the
/// handshake: its direct setting may be off, or it may not support it.
public enum DirectReason: Sendable, Equatable { case disabled, peerUnsupported, noResponse, noTraversal, failed, atCap, securityPending, starting }
public struct DirectStatus: Sendable, Equatable {
    public var state: DirectState
    public var reason: DirectReason?
    public var fileDirect = false, readDirect = false
    public var fileMiBps: [Int: Double] = [:], readMs: [Int: Double] = [:]   // keyed by DirectPath.relay / .direct
    /// Connected, and the warm-up that decides the read path is running or waiting out its stability period.
    public var warming = false
    /// Connected with a ready read path, and the live window holds enough samples on both paths for a comparison.
    public var compared = false
    public var retryScheduled = false
    public init(state: DirectState, reason: DirectReason? = nil) { self.state = state; self.reason = reason }
}
public enum DirectPath { public static let relay = 0, direct = 1 }

/// Delay before the next round with a computer after one failed or ended: full jitter, uniform
/// below 1 s, 2 s, 4 s ... up to 30 s (docs/transport.md §4), the same as the relay's own reconnect.
/// A path that stayed up for `stableMs` starts the sequence again.
enum DirectRetry { static let initialMs = 1000.0, maxMs = 30_000.0, stableMs: UInt64 = 30_000 }

/// Account owner for optional direct paths. It never owns a logical request or its outcome:
/// Link and Remote retain authorization and delivery evidence. State is guarded by one lock; SDK
/// objects are closed, observers called and frames sent only after it is released.
final class DirectEngine: @unchecked Sendable {
    typealias W = PeerWire
    private final class Peer: @unchecked Sendable {
        let id: String, head: String
        var session: DirectSession!
        var paths: [Int: DirectChannel] = [:]
        var live = true, selected = false
        var capabilities: PBMachineCapabilities?
        var snapshotUntil: UInt64 = 0, downloadUntil: UInt64 = 0, snapshotSeq: UInt64 = 0, downloadSeq: UInt64 = 0
        var supportsEventPush = false
        var subscribedChannel: DirectChannel?
        var redialAt: UInt64?, readyAt: UInt64?, retryDelay: UInt64?
        init(id: String, head: String) { self.id = id; self.head = head }
    }
    private final class Owner: @unchecked Sendable {}
    private final class Pending: @unchecked Sendable {
        nonisolated(unsafe) let peer: AnyObject; let use: Int
        private let lock = NSLock()
        private var result: Result<PBEnvelope, Error>?
        private var waiter: CheckedContinuation<PBEnvelope, Error>?
        init(peer: AnyObject, use: Int) { self.peer = peer; self.use = use }
        func settle(_ r: Result<PBEnvelope, Error>) {
            lock.lock(); if result != nil { lock.unlock(); return }; result = r; let w = waiter; waiter = nil; lock.unlock()
            w?.resume(with: r)
        }
        func wait() async throws -> PBEnvelope {
            try await withCheckedThrowingContinuation { (c: CheckedContinuation<PBEnvelope, Error>) in
                lock.lock()
                if let r = result { lock.unlock(); c.resume(with: r) } else { waiter = c; lock.unlock() }
            }
        }
    }

    private let server: String, deviceId: String, deviceSeed: Data
    private let factory: (any RtcFactory)?
    private let preferenceStore: any DirectPreferenceStore
    private let authorize: @Sendable () async throws -> Void
    private let changed: @Sendable () -> Void
    private let clock: DirectClock
    private let stun: [String]
    private let random: @Sendable () -> Double
    private let budget = DirectBudget()
    private let instance = W.random(16), boot = W.random(32)
    private let lock = NSLock()

    private var record: DeviceSecurity.Record?
    private var selfID: W.Identity?
    private var codec: PeerCodec?
    private var head = ""
    private var freshUntil: UInt64 = 0
    private var lastAuthorizeStartMs: UInt64 = 0
    private var authorizeInFlight = false
    private var syncRevision = 0, lifetime = 0
    private var routingOwners: [String: Owner] = [:]
    private var peers: [String: Peer] = [:], sessions: [String: Peer] = [:]
    private var routes: [String: DirectRouting] = [:]
    private var pending: [String: Pending] = [:]
    private var incoming = 0, incomingBytes = 0
    private var inboundPeers: [String: (frames: Int, bytes: Int)] = [:]
    private var leg: (any FrameStream)?
    private var timer: Task<Void, Never>?
    private var preferences: [String: Bool] = [:]
    private var frameTokens = 256.0, byteTokens = Double(4 << 20)
    private var rateAt: Double
    private var retryAfter: [String: UInt64] = [:], failureCount: [String: Int] = [:]
    private var failures: [String: DirectReason] = [:]
    private let pumpLock = NSLock()
    private var pumpTask: Task<Void, Never>?
    private var warmShown: [String: Bool] = [:]
    private let receiveChain = SerialChain()
    private var discovering = false, discoverPending = false
    /// Sends one authorized background read on the given path and returns when it ends; the owner
    /// of the keys (Remote) provides it. Without it no warm-up sample is taken.
    /// What the engine decided, in words (never keys, content or addresses); the Remote hands its own log.
    var trace: (@Sendable (String) -> Void)?
    var probe: (@Sendable (String, RouteName) async -> Void)? { get { lock.withLock { _probe } } set { lock.withLock { _probe = newValue } } }
    private var _probe: (@Sendable (String, RouteName) async -> Void)?
    var eventHandler: (@Sendable (String, PBEnvelope, DeviceSecurity.Header, Int) -> Void)? {
        get { lock.withLock { _eventHandler } }
        set { lock.withLock { _eventHandler = newValue } }
    }
    private var _eventHandler: (@Sendable (String, PBEnvelope, DeviceSecurity.Header, Int) -> Void)?
    private var watchedByMachine: [String: [String]] = [:]

    func setWatchedSessions(_ subscriptions: [String: [String]]) {
        var next: [String: [String]] = [:]
        for (machineId, ids) in subscriptions where !machineId.isEmpty {
            let capped = Array(ids.filter { !$0.isEmpty }.prefix(4))
            if !capped.isEmpty { next[machineId] = capped }
        }
        let toSend: [(Peer, W.Bytes)] = lock.withLock {
            guard watchedByMachine != next else { return [] }
            watchedByMachine = next
            return peers.values.compactMap { sendSubscriptionLocked($0) }
        }
        for (p, body) in toSend { p.session.sendControl(15, body) }
    }

    private func sendSubscriptionLocked(_ p: Peer, onlyIfChannelChanged: Bool = false) -> (Peer, W.Bytes)? {
        guard validLocked(p), peers[p.id] === p, p.session.live, p.supportsEventPush,
              let path1 = p.paths[1], path1.usable else { return nil }
        if onlyIfChannelChanged && p.subscribedChannel === path1 { return nil }
        let ids = watchedByMachine[p.id] ?? []
        let sub = PBDirectEventSubscription.with { $0.sessionIds = ids }
        guard let data = try? sub.serializedData() else { return nil }
        p.subscribedChannel = path1
        return (p, Array(data))
    }

    init(server: String, deviceId: String, deviceSeed: Data, factory: (any RtcFactory)?, preferences: any DirectPreferenceStore, stun: [String]? = nil,
         clock: DirectClock = DirectClock(), random: @escaping @Sendable () -> Double = { Double.random(in: 0..<1) },
         authorize: @escaping @Sendable () async throws -> Void, changed: @escaping @Sendable () -> Void) {
        self.server = server; self.deviceId = deviceId; self.deviceSeed = deviceSeed; self.factory = factory
        self.preferenceStore = preferences; self.authorize = authorize; self.changed = changed; self.clock = clock; self.random = random
        rateAt = clock.monotonic()
        self.stun = stun ?? [DirectEngine.defaultStun(server)]
    }
    static func defaultStun(_ server: String) -> String {
        let host = URL(string: server)?.host ?? ""
        return "stun:\(host.contains(":") ? "[\(host)]" : host):3478"
    }
    private func notify() { changed() }

    // MARK: preference
    func enabled(_ id: String) -> Bool {
        lock.withLock { enabledLocked(id) }
    }
    private func enabledLocked(_ id: String) -> Bool {
        if let v = preferences[id] { return v }
        let v = preferenceStore.isEnabled(server: server, deviceId: deviceId, machineId: id)
        preferences[id] = v; return v
    }
    func setEnabled(_ id: String, _ value: Bool) throws {
        // Persistence must succeed before reporting a changed preference.
        try preferenceStore.setEnabled(server: server, deviceId: deviceId, machineId: id, enabled: value)
        var close: [DirectSession] = []
        lock.withLock {
            preferences[id] = value; lifetime += 1; routingOwners[id] = Owner()
            for p in sessions.values where p.id == id { p.live = false; close.append(p.session) }
            peers.removeValue(forKey: id); failures.removeValue(forKey: id); retryAfter.removeValue(forKey: id); failureCount.removeValue(forKey: id)
            routingLocked(id).fail()
        }
        close.forEach { $0.close() }
        notify()
        if value { Task { await self.discover() } }
    }

    // MARK: security lifetime
    func install(_ r: DeviceSecurity.Record) {
        let hash = (try? DeviceSecurity.stateHash(r.state)).map { W.hex(Array($0)) }
        let stale = lock.withLock { !r.member || r.pin.account != record?.pin.account || hash != head }
        if stale { invalidate() }
    }
    func invalidate() {
        var close: [DirectSession] = [], failed: [Pending] = []
        lock.withLock {
            syncRevision += 1; lifetime += 1; routingOwners.removeAll(); freshUntil = 0
            close = sessions.values.map { $0.live = false; return $0.session }
            // A new network generation, lifetime or head is a new start: no old failure may delay the next round.
            peers.removeAll(); sessions.removeAll(); failures.removeAll(); retryAfter.removeAll(); failureCount.removeAll()
            for r in routes.values { r.fail() }
            failed = Array(pending.values); pending.removeAll()
        }
        close.forEach { $0.close() }
        failed.forEach { $0.settle(.failure(PathChanged())) }
        notify()
    }
    /// A security sync finished at `started`; the head, member keys and lease it proved start (or
    /// keep) the direct session lifetime for the next 20 seconds.
    func synced(_ r: DeviceSecurity.Record, started: UInt64) async {
        let revision = lock.withLock { () -> Int in syncRevision += 1; return syncRevision }
        do {
            let stateHash = try DeviceSecurity.stateHash(r.state)
            guard let lease = r.lease else { throw PathChanged() }
            try lease.verify(r.state)
            let at = max(clock.now(), r.floor)
            guard r.member, r.memberID == deviceId, clock.now() < started + 20_000, at < lease.notAfter, let member = r.state.core.member(r.memberID) else { throw PathChanged() }
            let sign = try Curve25519.Signing.PrivateKey(rawRepresentation: deviceSeed), box = try r.xKey
            guard sign.publicKey.rawRepresentation == member.ed, box.publicKey.rawRepresentation == member.x else { throw PathChanged() }
            let identity = W.Identity(peer: W.Public(id: W.utf8(member.id), instance: instance, boot: boot, role: 1, sign: sign.publicKey, box: box.publicKey), sign: sign, box: box)
            let scope = W.Scope(account: W.utf8(r.pin.account), accountDomain: Array(r.pin.domain), head: Array(stateHash), epoch: r.headEpoch)
            let hash = W.hex(Array(stateHash))
            var close: [DirectSession] = []
            let ok = lock.withLock { () -> Bool in
                if revision != syncRevision { return false }
                if head != hash {
                    lifetime += 1; routingOwners.removeAll()
                    close = sessions.values.map { $0.live = false; return $0.session }
                    peers.removeAll(); sessions.removeAll(); retryAfter.removeAll(); failureCount.removeAll()
                }
                record = r; head = hash; freshUntil = min(started + 20_000, lease.notAfter)
                if lastAuthorizeStartMs == 0 { lastAuthorizeStartMs = clock.now() }
                selfID = identity; codec = PeerCodec(scope: scope)
                return true
            }
            close.forEach { $0.close() }
            if ok { await discover() }
        } catch {
            
            if lock.withLock({ revision == syncRevision }) { invalidate() }
        }
    }

    // MARK: main connection
    func attach(_ stream: any FrameStream) {
        var all: [DirectSession] = []
        lock.withLock {
            leg = stream
            // The main connection is back: the computer can be reached again, so a retry waiting out its delay need not.
            retryAfter.removeAll(); failureCount.removeAll()
            for p in peers.values { p.redialAt = nil }
            if timer == nil {
                timer = Task.detached { [weak self] in
                    while !Task.isCancelled {
                        try? await Task.sleep(nanoseconds: 1_000_000_000)
                        guard let self else { return }
                        await self.timerStep()
                    }
                }
            }
            all = sessions.values.map(\.session)
        }
        all.forEach { $0.discovered() }
        let toResync: [(Peer, W.Bytes)] = lock.withLock {
            peers.values.compactMap { sendSubscriptionLocked($0) }
        }
        for (p, body) in toResync { p.session.sendControl(15, body) }
        Task { await self.discover() }
    }
    func detach(_ stream: any FrameStream) { lock.withLock { if leg === stream { leg = nil } } }
    /// One second of upkeep (the timer's body; tests call it with a fake clock).
    func timerStep() async {
        let now = clock.now()
        let (current, any, shouldAuth) = lock.withLock { () -> (Bool, Bool, Bool) in
            let c = currentLocked()
            let remaining = freshUntil > now ? freshUntil - now : 0
            let dueByCadence = lastAuthorizeStartMs == 0 || now >= lastAuthorizeStartMs + 12_000
            let dueByExpiry = remaining <= 7_000 && (lastAuthorizeStartMs == 0 || now >= lastAuthorizeStartMs + 2_000)
            let auth = leg != nil && !authorizeInFlight && (dueByCadence || dueByExpiry)
            if auth {
                authorizeInFlight = true
                lastAuthorizeStartMs = now
            }
            return (c, !peers.isEmpty, auth)
        }
        if !current { if any { invalidate() } } else { await discover() }
        if shouldAuth {
            Task.detached { [weak self] in
                guard let self else { return }
                defer { self.lock.withLock { self.authorizeInFlight = false } }
                try? await self.authorize()
                await self.discover()
            }
        }
    }
    /// The network changed (a new generation): an old failure must not delay the new start.
    func networkChanged() {
        lock.withLock { retryAfter.removeAll(); failureCount.removeAll(); for p in peers.values { p.redialAt = nil } }
        Task { await self.discover() }
    }
    private func currentLocked() -> Bool { record?.member == true && selfID != nil && codec != nil && clock.now() < freshUntil }
    private func validLocked(_ p: Peer) -> Bool { p.live && currentLocked() && p.head == head && enabledLocked(p.id) }
    private func valid(_ p: Peer) -> Bool { lock.withLock { validLocked(p) } }
    private func publicIdentityLocked(_ id: String) throws -> W.Public {
        guard let member = record?.state.core.members.first(where: { $0.id == id && $0.kind == 1 }) else { throw PathChanged() }
        return W.Public(id: W.utf8(id), instance: [], boot: [], role: 2, sign: try .init(rawRepresentation: member.ed), box: try .init(rawRepresentation: member.x))
    }

    // MARK: discovery
    func discover() async {
        guard factory != nil else { return }
        let enter = lock.withLock { () -> Bool in
            if discovering { discoverPending = true; return false }
            discovering = true
            discoverPending = false
            return true
        }
        guard enter else { return }
        while true {
            await discoverOnce()
            let again = lock.withLock { () -> Bool in
                if discoverPending { discoverPending = false; return true }
                discovering = false
                return false
            }
            if !again { return }
        }
    }

    private func discoverOnce() async {
        guard factory != nil else { return }
        guard let (snapHead, snapLeg, snapLife, members) = lock.withLock({ () -> (String, any FrameStream, Int, [DeviceSecurity.Member])? in
            guard currentLocked(), let leg, let r = record else { return nil }
            return (head, leg, lifetime, r.state.core.members)
        }) else { return }
        let still: @Sendable () -> Bool = { self.lock.withLock { self.currentLocked() && self.head == snapHead && self.leg === snapLeg && self.lifetime == snapLife } }
        for member in members {
            if !still() { return }
            // A failed use starts a new bounded pair round after its cooldown. Closing the old SDK
            // objects stays charged to the shared budget.
            let retire: DirectSession? = lock.withLock {
                if let prior = peers[member.id], let at = prior.redialAt, clock.now() >= at, prior.live {
                    prior.live = false
                    peers.removeValue(forKey: member.id); retryAfter.removeValue(forKey: member.id); return prior.session
                }
                return nil
            }
            retire?.retire()
            let skip = lock.withLock { member.kind != 1 || !enabledLocked(member.id) || peers[member.id]?.live == true || clock.now() < (retryAfter[member.id] ?? 0) }
            if skip { continue }
            if lock.withLock({ peers.count >= 4 }) { break }
            let pub = lock.withLock { try? publicIdentityLocked(member.id) }
            guard let pub, still() else { continue }
            if lock.withLock({ peers[member.id]?.live == true || clock.now() < (retryAfter[member.id] ?? 0) }) { continue }
            if let p = create(pub, W.random(16), clock.now() + 10_000, outbound: true) {
                trace?("direct: starting a round")
                await p.session.start()
            } else {
                trace?("direct: a round could not be made")
            }
        }
    }

    private func create(_ peer: W.Public, _ connection: W.Bytes, _ deadline: UInt64, outbound: Bool = false) -> Peer? {
        let id = String(decoding: peer.id, as: UTF8.self), now = clock.now()
        var expired: [DirectSession] = []
        let made: Peer? = lock.withLock {
            for (key, p) in sessions where now > p.session.retainUntil { p.live = false; expired.append(p.session); sessions.removeValue(forKey: key) }
            if outbound && (peers[id]?.live == true || now < (retryAfter[id] ?? 0)) { return nil }
            guard currentLocked(), enabledLocked(id), sessions.count < 128, sessions.values.filter({ $0.id == id }).count < 32, peers[id] != nil || peers.count < 4,
                  selfID != nil, codec != nil, factory != nil else { return nil }
            return Peer(id: id, head: head)
        }
        expired.forEach { $0.close() }
        guard let p = made, let selfID = lock.withLock({ self.selfID }), let codec = lock.withLock({ self.codec }), let factory else { return nil }
        let key = W.hex(connection)
        let choose: @Sendable () -> Void = { [weak self, weak p] in
            guard let self, let p else { return }
            var old: [DirectSession] = []
            let chosen = self.lock.withLock { () -> Bool in
                guard self.validLocked(p), p.session.live, self.sessions[key] === p else { return false }
                p.selected = true; self.selectLocked(p)
                old = self.sessions.values.filter { $0 !== p && $0.id == id }.map(\.session); return true
            }
            if chosen { old.forEach { $0.retire() } }
        }
        let options = DirectSessionOptions(
            selfID: selfID, peer: peer, codec: codec, connection: connection, deadline: min(deadline, now + 10_000), budget: budget, stun: stun, factory: factory,
            valid: { [weak self, weak p] in guard let self, let p else { return false }; return self.valid(p) },
            authorize: { [weak self, weak p] in
                try await self?.authorize()
                guard let self, let p, self.valid(p) else { throw PathChanged() }
            },
            send: { [weak self, weak p] out in guard let self, let p else { return false }; return self.send(p, out) },
            chosen: choose, beforeOffer: choose,
            ready: { [weak self, weak p] use, path in self?.pathReady(p, id, use, path) },
            closed: { [weak self, weak p] use in self?.pathClosed(p, id, use) },
            frame: { [weak self, weak p] raw, use in if let self, let p { self.frame(p, raw, use) } },
            capability: { [weak self, weak p] opened in if let self, let p { self.capability(p, opened) } },
            open: { [weak self, weak p] use in if let self, let p { self.channelOpen(p, use) } },
            clock: clock)
        guard let session = try? DirectSession(options) else { return nil }
        p.session = session
        lock.withLock {
            sessions[key] = p
            let prior = peers[id]
            if prior?.selected != true || prior?.session.live != true { selectLocked(p) }
        }
        notify()
        return p
    }
    private func selectLocked(_ p: Peer) {
        if peers[p.id] === p { return }
        if peers[p.id] != nil { routingLocked(p.id).fail() }
        routingOwners[p.id] = Owner(); peers[p.id] = p
    }
    private func pathReady(_ p: Peer?, _ id: String, _ use: Int, _ path: DirectChannel) {
        guard let p else { path.close(); return }
        var syncCall: (Peer, W.Bytes)?
        let accepted = lock.withLock { () -> Bool in
            guard validLocked(p), p.session.live, peers[id] === p else { return false }
            p.paths[use] = path
            if p.readyAt == nil { p.readyAt = clock.now() }
            if p.paths[1]?.usable == true && p.paths[2]?.usable == true { p.redialAt = nil }
            failures.removeValue(forKey: id)
            if use == 1 {
                routingLocked(id).arm(fresh: true)
                syncCall = sendSubscriptionLocked(p, onlyIfChannelChanged: true)
            }
            return true
        }
        if accepted {
            if let (peer, body) = syncCall { peer.session.sendControl(15, body) }
            notify()
        } else {
            path.close()
        }
    }
    private func pathClosed(_ p: Peer?, _ id: String, _ use: Int?) {
        guard let p else { return }
        var retire: DirectSession?
        lock.withLock {
            guard peers[id] === p else { return }
            if use != nil && p.session.live && p.session.established {
                if p.redialAt == nil { p.redialAt = clock.now() + delay(id, p) }
                trace?("direct: use \(use!) closed, redial in \(Int64(bitPattern: (p.redialAt ?? 0) &- clock.now())) ms")
                routingLocked(id).fail()
            }
            if !p.paths.values.contains(where: { $0.usable }) {
                failures[id] = p.session.failure.map(Self.reason) ?? .failed
                routingLocked(id).fail()
                if peers[id] === p { peers.removeValue(forKey: id) }
                p.live = false
                retire = p.session
                retryAfter[id] = p.redialAt ?? (clock.now() + delay(id, p))
                trace?("direct: no usable path left, next round in \(Int64(bitPattern: retryAfter[id]! &- clock.now())) ms (reason \(String(describing: failures[id])))")
            }
        }
        retire?.retire()
        failPending(for: p, use: use)
        notify()
    }
    private static func reason(_ f: DirectFailure) -> DirectReason { switch f { case .peerUnsupported: .peerUnsupported; case .noResponse: .noResponse; case .noTraversal: .noTraversal } }
    /// The delay for this round's end, counted once however many of its paths close. Called under the lock.
    private func delay(_ id: String, _ p: Peer) -> UInt64 {
        if let d = p.retryDelay { return d }
        if let up = p.readyAt, clock.now() - up >= DirectRetry.stableMs { failureCount.removeValue(forKey: id) }
        let failures = failureCount[id] ?? 0; failureCount[id] = failures + 1
        let d = UInt64(random() * min(DirectRetry.maxMs, DirectRetry.initialMs * pow(2, Double(min(failures, 5)))))
        p.retryDelay = d; return d
    }
    private func failPending(for p: Peer, use: Int?) {
        let dead = lock.withLock { () -> [Pending] in
            let keys = pending.filter { $0.value.peer === p && (use == nil || $0.value.use == use!) }.map(\.key)
            return keys.compactMap { pending.removeValue(forKey: $0) }
        }
        dead.forEach { $0.settle(.failure(PathChanged())) }
    }

    // MARK: routing ownership
    private func routingLocked(_ id: String) -> DirectRouting {
        if let r = routes[id] { return r }
        let r = DirectRouting(now: clock.monotonic, armed: { [weak self] in self?.kick() })
        routes[id] = r; return r
    }
    /// Completion ownership survives awaits without borrowing a replacement's routing state.
    /// Callers still verify and return their original result.
    struct Tracking: Sendable {
        let observe: @Sendable (_ direct: Bool, _ bytes: Int, _ ms: Double) -> Void
        let failed: @Sendable () -> Void
    }
    func track(_ id: String, _ hint: RouteHint) -> Tracking {
        let use = hint.isRead ? 1 : 2
        let (peer, path, life, owner) = lock.withLock { () -> (Peer?, DirectChannel?, Int, Owner) in
            let p = peers[id]
            if routingOwners[id] == nil { routingOwners[id] = Owner() }
            return (p, p?.paths[use], lifetime, routingOwners[id]!)
        }
        let current: @Sendable () -> Bool = { self.lifetime == life && self.routingOwners[id] === owner && self.peers[id] === peer && peer?.paths[use] === path }
        return Tracking(
            observe: { [weak self] direct, bytes, ms in
                guard let self else { return }
                let queued = path?.queuedBytes ?? 0
                self.lock.withLock { if current() { self.routingLocked(id).observe(hint, direct ? .direct : .relay, bytes: bytes, ms: ms, queued: queued) } }
                self.notify()
            },
            failed: { [weak self] in
                guard let self else { return }
                self.lock.withLock { if current() { self.routingLocked(id).fail(hint) } }
                self.notify()
            })
    }

    // MARK: capabilities and frames
    private func channelOpen(_ p: Peer, _ use: Int) {
        guard use == 1 else { return }
        let (recognized, syncCall): (Bool, (Peer, W.Bytes)?) = lock.withLock {
            guard peers[p.id] === p else { return (false, nil) }
            return (true, sendSubscriptionLocked(p, onlyIfChannelChanged: true))
        }
        if let (peer, body) = syncCall { peer.session.sendControl(15, body) }
        if recognized { notify() }
    }
    private func capability(_ p: Peer, _ opened: W.Opened) {
        var syncCall: (Peer, W.Bytes)?
        let ok: Bool = lock.withLock {
            guard validLocked(p) else { return false }
            if opened.type == 14 {
                if opened.ctx.seq <= p.snapshotSeq { return false }
                guard let snapshot = try? PBCapabilitySnapshot(serializedBytes: opened.payload), snapshot.hasCapabilities else { return false }
                p.snapshotSeq = opened.ctx.seq; p.capabilities = snapshot.capabilities; p.snapshotUntil = UInt64(max(0, snapshot.expiresAtMs))
            } else {
                if opened.ctx.seq <= p.downloadSeq { return false }
                p.downloadSeq = opened.ctx.seq
                let f = try? PeerSchema.fields(opened.payload, "Capabilities")
                p.downloadUntil = f?[5]?.value == 1 ? (f?[4]?.value ?? 0) : 0
                let caps = try? PBCapabilities(serializedBytes: Data(opened.payload))
                p.supportsEventPush = caps?.controlTypes.contains(15) == true
                if p.supportsEventPush { syncCall = sendSubscriptionLocked(p) }
            }
            return true
        }
        if let (peer, body) = syncCall { peer.session.sendControl(15, body) }
        if ok { notify() }
    }
    private func send(_ p: Peer, _ out: DirectedOutput) -> Bool {
        // The output's own check asks the engine again (valid(p)): never under the lock.
        guard out.valid() else { return false }
        let (ok, stream): (Bool, (any FrameStream)?) = lock.withLock {
            guard validLocked(p), let leg else { return (false, nil) }
            let now = clock.monotonic(), elapsed = max(0, now - rateAt) / 1000; rateAt = now
            frameTokens = min(256, frameTokens + elapsed * 256); byteTokens = min(Double(4 << 20), byteTokens + elapsed * Double(4 << 20))
            if frameTokens < 1 || byteTokens < Double(out.raw.count) { return (false, nil) }
            frameTokens -= 1; byteTokens -= Double(out.raw.count)
            return (true, leg)
        }
        guard ok, let stream, let f = try? W.parse(out.raw, 5) else { return false }
        var directed = PBDirectedPeerFrame()
        directed.toDevice = Data(f[2]?.data ?? []); directed.frameClass = PBFrameClass(rawValue: Int(f[3]?.value ?? 0)) ?? .unspecified
        directed.frameID = Data(f[4]?.data ?? []); directed.sealed = Data(f[5]?.data ?? [])
        var frame = PBPeerFrame(); frame.directedPeerFrame = directed
        // A refused write never closes the main connection: signalling is optional.
        guard let bytes = try? frame.serializedData() else { return false }
        stream.send(bytes)
        return true
    }

    /// A PeerFrameDelivery body from the main connection (already unwrapped from its frame).
    func receive(_ raw: W.Bytes, from stream: any FrameStream) {
        let admitted: (String)? = lock.withLock {
            guard leg === stream, currentLocked(), raw.count <= 65536, incoming < 64, incomingBytes + raw.count <= 4 << 20, let selfID else { return nil }
            guard let f = try? W.parse(raw, 5), f.count == 5, let source = f[1].flatMap({ String(bytes: $0.data, encoding: .utf8) }), f[1]!.data.count == 32,
                  f[2]?.data == selfID.id, f[4]?.data.count == 16, let cls = f[3]?.value, cls >= 1, cls <= 2, !(cls == 2 && raw.count > 16384),
                  record?.state.core.members.contains(where: { $0.id == source && $0.kind == 1 }) == true else { return nil }
            var usage = inboundPeers[source] ?? (0, 0)
            if usage.frames >= 8 || usage.bytes + raw.count > 1 << 20 { return nil }
            usage.frames += 1; usage.bytes += raw.count; inboundPeers[source] = usage
            incoming += 1; incomingBytes += raw.count
            return source
        }
        guard let source = admitted else { return }
        let life = lock.withLock { lifetime }
        receiveChain.run { [self] in
            defer { lock.withLock {
                incoming -= 1; incomingBytes -= raw.count
                if var u = inboundPeers[source] { u.frames -= 1; u.bytes -= raw.count; if u.frames <= 0 { inboundPeers.removeValue(forKey: source) } else { inboundPeers[source] = u } }
            } }
            await route(raw, source: source, stream: stream, life: life)
        }
    }
    private func route(_ raw: W.Bytes, source id: String, stream: any FrameStream, life: Int) async {
        let state = lock.withLock { () -> (String, PeerCodec, W.Identity)? in
            guard currentLocked(), leg === stream, lifetime == life, enabledLocked(id), let codec, let selfID else { return nil }
            return (head, codec, selfID)
        }
        guard let (headNow, codec, selfID) = state else { return }
        let current: @Sendable () -> Bool = { self.lock.withLock { self.currentLocked() && self.head == headNow && self.leg === stream && self.lifetime == life } }
        guard var peer = lock.withLock({ try? publicIdentityLocked(id) }), current() else { return }
        let opened = try? codec.open(raw, peer, selfID, clock.now())
        guard current() else { return }
        var target: Peer?
        if let opened, opened.ctx.cls == 1, opened.type == 1 {
            let key = W.hex(opened.ctx.connection)
            target = lock.withLock { sessions[key] }
            if target == nil {
                peer.instance = opened.ctx.senderInstance; peer.boot = opened.ctx.senderBoot
                var close: [DirectSession] = []
                lock.withLock {
                    for old in sessions.values where old.id == id && !old.session.peer.boot.isEmpty && old.session.peer.boot != peer.boot {
                        old.live = false; close.append(old.session); retryAfter.removeValue(forKey: id); failureCount.removeValue(forKey: id)
                    }
                }
                close.forEach { $0.close() }
                target = create(peer, opened.ctx.connection, opened.ctx.deadline)
            }
        } else {
            let candidates = lock.withLock { sessions.values.filter { $0.id == id && $0.head == headNow } }
            for p in candidates {
                if let o = try? codec.open(raw, p.session.peer, selfID, clock.now()) {
                    if !current() { return }
                    if o.ctx.connection == p.session.options.connection { target = p; break }
                }
            }
        }
        if current(), let target, valid(target) { target.session.receive(raw) }
    }
    /// A PeerFrameResult body: only a delivery receipt, it never authorizes anything.
    func result(_ raw: W.Bytes, from stream: any FrameStream) {
        let (all, life): ([DirectSession], Int) = lock.withLock { leg === stream ? (sessions.values.map(\.session), lifetime) : ([], 0) }
        guard !all.isEmpty, let result = try? PBPeerFrameResult(serializedBytes: raw) else { return }
        for s in all { s.delivery(Array(result.frameID), status: UInt32(result.status.rawValue), retry: result.retryAfterMs, current: { [weak self] in self?.lock.withLock { self?.leg === stream && self?.lifetime == life } ?? false }) }
    }
    private func frame(_ p: Peer, _ raw: W.Bytes, _ use: Int) {
        guard valid(p), let env = try? PBEnvelope(serializedBytes: raw), let header = try? DeviceSecurity.parseHeader(env.securityHeader) else { return }
        if use == 1 && header.purpose == .event {
            let handler: (@Sendable (String, PBEnvelope, DeviceSecurity.Header, Int) -> Void)? = lock.withLock {
                guard validLocked(p), peers[p.id] === p, header.machine == p.id, header.sender == p.id else { return nil }
                return _eventHandler
            }
            handler?(p.id, env, header, raw.count)
            return
        }
        let box = lock.withLock { pending[header.request] }
        if let box, box.peer === p, box.use == use, header.machine == p.id { box.settle(.success(env)) }
    }

    // MARK: warm-up
    func refresh(_ id: String) {
        let go = lock.withLock { () -> Bool in
            guard readyLocked(id, .read) else { return false }
            let r = routingLocked(id); if r.sparse() { r.arm(); return true }; return false
        }
        if go { kick() }
    }
    private func kick() {
        // Not the engine's lock: the routing calls this from inside it.
        pumpLock.withLock { if pumpTask == nil { pumpTask = Task.detached { [weak self] in await self?.pump() } } }
    }
    /// Drives the warm-up of every computer: one step every 250 ms while any is open. The numbers
    /// (alternation, in-flight, per-path count, rate) are the routing's; this only asks it, and
    /// only while the read path is ready.
    private func pump() async {
        while !Task.isCancelled {
            var more = false
            var starts: [(String, RouteName, DirectRouting)] = []
            var shown = false
            lock.withLock {
                for (id, route) in routes {
                    if route.pending() { more = true }
                    if route.warming() && readyLocked(id, .read), let path = route.nextProbe() { starts.append((id, path, route)) }
                    if warmShown[id] != route.measuring() { warmShown[id] = route.measuring(); shown = true }
                }
            }
            if shown { notify() }
            for (id, path, route) in starts {
                let hook = probe
                Task { [weak self] in
                    await hook?(id, path)
                    self?.lock.withLock { route.probeDone(path) }
                    self?.notify()
                }
            }
            if !more { break }
            try? await Task.sleep(nanoseconds: UInt64(ReadRule.warmupMinGapMs * 1_000_000))
        }
        pumpLock.withLock { pumpTask = nil }
    }

    // MARK: use
    func hasPathForTest(_ id: String, use: Int) -> Bool {
        lock.withLock { peers[id]?.paths[use] != nil }
    }
    func supportsEventPushForTest(_ id: String) -> Bool {
        lock.withLock { peers[id]?.supportsEventPush == true }
    }
    func drainForTest() async {
        await receiveChain.drain()
        let activeSessions: [DirectSession] = lock.withLock { sessions.values.compactMap(\.session) }
        for s in activeSessions { await s.drainForTest() }
        await receiveChain.drain()
    }
    func tickSessionsForTest() async {
        let activeSessions: [DirectSession] = lock.withLock { sessions.values.compactMap(\.session) }
        for s in activeSessions { await s.tickForTest() }
    }
    func waitBudgetEmptyForTest() async { await budget.waitUntilEmptyForTest() }
    func capabilities(_ id: String) -> PBMachineCapabilities? {
        lock.withLock { capabilitiesLocked(id) }
    }
    private func capabilitiesLocked(_ id: String) -> PBMachineCapabilities? {
        guard let p = peers[id], validLocked(p), clock.now() < p.snapshotUntil else { return nil }
        return p.capabilities
    }
    func ready(_ id: String, _ hint: RouteHint) -> Bool { lock.withLock { readyLocked(id, hint) } }
    private func readyLocked(_ id: String, _ hint: RouteHint) -> Bool {
        guard let p = peers[id], validLocked(p), p.session.live, p.paths[hint.isRead ? 1 : 2]?.usable == true else { return false }
        if case .bulk(_, let upload, let bytes) = hint, let upload {
            // Direct upload only with a valid snapshot that allows it; otherwise the relay carries it.
            guard let caps = capabilitiesLocked(id), let bytes, bytes <= Int(caps.maxChunkBytes) else { return false }
            return upload == .file ? caps.fileUpload : caps.imageUpload
        }
        if case .bulk = hint { return clock.now() < p.downloadUntil }
        return true
    }

    /// `probe` takes a warm-up sample on the direct path whatever the routing prefers; the routing already bounded it.
    func attempt(_ id: String, _ hint: RouteHint, handoff: @escaping @Sendable () throws -> Void, probe: Bool = false) -> (any PathAttempt)? {
        let chosen: (Peer, DirectChannel)? = lock.withLock {
            let ready = readyLocked(id, hint)
            if probe ? (!hint.isRead || !ready) : routingLocked(id).choose(hint, ready: ready) != .direct { return nil }
            guard let p = peers[id], let path = p.paths[hint.isRead ? 1 : 2] else { return nil }
            return (p, path)
        }
        guard let (peer, path) = chosen else { return nil }
        let use = hint.isRead ? 1 : 2
        return DirectAttempt(engine: self, id: id, hint: hint, peer: peer, path: path, use: use, handoff: handoff)
    }
    private final class DirectAttempt: PathAttempt, @unchecked Sendable {
        let attemptId = newRequestId()
        var isDirect: Bool { true }
        private let engine: DirectEngine, id: String, hint: RouteHint, peer: Peer, path: DirectChannel, use: Int
        private let handoff: @Sendable () throws -> Void
        init(engine: DirectEngine, id: String, hint: RouteHint, peer: Peer, path: DirectChannel, use: Int, handoff: @escaping @Sendable () throws -> Void) {
            self.engine = engine; self.id = id; self.hint = hint; self.peer = peer; self.path = path; self.use = use; self.handoff = handoff
        }
        private func current() -> Bool { engine.lock.withLock { engine.peers[id] === peer && peer.paths[use] === path && engine.readyLocked(id, hint) } }
        func prepare(prior: Bool) async throws { if !current() { throw PathChanged() } }
        func refusal(_ error: ConnectError) -> PathRefusal? { nil }
        func refreshAuth(prior: Bool) async throws { try await engine.authorize() }
        func send(machineId: String, command: PBEnvelope) async throws -> PBForwardResponse {
            let header = try DeviceSecurity.parseHeader(command.securityHeader), request = header.request
            let box = Pending(peer: peer, use: use)
            guard current() else { throw PathChanged() }
            let registered = engine.lock.withLock { () -> Bool in
                if engine.pending.count >= 128 || engine.pending[request] != nil { return false }
                engine.pending[request] = box; return true
            }
            guard registered else { throw PathChanged() }
            let remaining = max(1, min(3000, Int64(header.deadline) - Int64(engine.clock.now())))
            let clock = engine.clock
            let timeout = Task { try? await clock.sleep(UInt64(remaining)); box.settle(.failure(PathChanged())) }
            defer { timeout.cancel(); engine.lock.withLock { if engine.pending[request] === box { engine.pending.removeValue(forKey: request) } } }
            do {
                let raw = try command.serializedData()
                let result: PBEnvelope = try await withTaskCancellationHandler {
                    try await withThrowingTaskGroup(of: PBEnvelope?.self) { group in
                        group.addTask { try await self.path.write(Array(raw), handoff: self.handoff); return nil }
                        group.addTask { try await box.wait() }
                        var answer: PBEnvelope?
                        do { while let next = try await group.next() { if let next { answer = next; break } } }
                        catch { box.settle(.failure(error)); group.cancelAll(); throw error }
                        // Whatever is still waiting or writing ends with the answer.
                        box.settle(.failure(PathChanged())); group.cancelAll()
                        guard let answer else { throw PathChanged() }
                        return answer
                    }
                } onCancel: { box.settle(.failure(CancellationError())) }
                var response = PBForwardResponse(); response.result = result; response.attemptID = attemptId
                return response
            } catch is CancellationError { throw CancellationError()
            } catch {
                engine.track(id, hint).failed()
                // The write fell through before the first fragment: a handoff refusal is the caller's.
                if error is DeviceSecurity.Failure { throw error }
                throw PathChanged()
            }
        }
    }

    // MARK: display
    func status(_ id: String) -> DirectStatus {
        let supported = factory != nil
        return lock.withLock {
            let p = peers[id], live = p != nil && validLocked(p!) && p!.session.live
            let measured = routes[id]?.measured() ?? (fileMiBps: [:], readMs: [:])
            func fill(_ s: inout DirectStatus) {
                for (k, v) in measured.fileMiBps { s.fileMiBps[k == .relay ? DirectPath.relay : DirectPath.direct] = v }
                for (k, v) in measured.readMs { s.readMs[k == .relay ? DirectPath.relay : DirectPath.direct] = v }
            }
            var s = DirectStatus(state: .none)
            fill(&s)
            if !supported { s.state = .unsupported; return s }
            if !enabledLocked(id) { s.reason = .disabled; return s }
            if live, p!.paths.values.contains(where: { $0.usable }) {
                let read = readyLocked(id, .read), route = routingLocked(id)
                s.state = .connected
                s.fileDirect = p!.paths[2]?.usable == true && route.filePath == .direct
                s.readDirect = read && route.readPath() == .direct
                s.warming = read && route.measuring(); s.compared = read && route.compared()
                return s
            }
            if live { s.state = .connecting; return s }
            let resolved: DirectReason = failures[id]
                ?? (!currentLocked() ? .securityPending : peers.count >= 4 ? .atCap : .starting)
            s.reason = resolved
            s.retryScheduled = (retryAfter[id] ?? 0) > clock.now()
            return s
        }
    }
    func close() {
        lock.withLock { timer?.cancel(); timer = nil; leg = nil }
        pumpLock.withLock { pumpTask?.cancel(); pumpTask = nil }
        invalidate()
    }
}
