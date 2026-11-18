import CryptoKit
import Foundation
import RAProto
import Testing
@testable import RACore

/// Purely virtual clock controlled by the test: waiting never drifts with wall-clock load.
final class FakeClock: @unchecked Sendable {
    private let lock = NSRecursiveLock()
    private let deliveryChain = SerialChain()
    private let machineDeliveryChain = SerialChain(); private var offset: UInt64 = 0
    private var waiters: [(until: UInt64, id: UUID, cont: CheckedContinuation<Void, any Error>)] = []
    private let base: UInt64
    init(_ ms: UInt64) { base = ms }
    var now: UInt64 { lock.withLock { base + offset } }
    func advance(_ by: UInt64) {
        let ready: [CheckedContinuation<Void, any Error>] = lock.withLock {
            offset += by
            let cur = base + offset
            let ready = waiters.filter { $0.until <= cur }.map(\.cont)
            waiters.removeAll { $0.until <= cur }
            return ready
        }
        for c in ready { c.resume() }
    }
    func sleep(_ ms: UInt64) async throws {
        try Task.checkCancellation()
        if ms == 0 { return }
        let id = UUID()
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, any Error>) in
                let cancelNow = lock.withLock { () -> Bool in
                    if Task.isCancelled { return true }
                    waiters.append((base + offset + ms, id, c))
                    return false
                }
                if cancelNow { c.resume(throwing: CancellationError()) }
            }
        } onCancel: {
            let cont: CheckedContinuation<Void, any Error>? = lock.withLock {
                guard let i = waiters.firstIndex(where: { $0.id == id }) else { return nil }
                return waiters.remove(at: i).cont
            }
            cont?.resume(throwing: CancellationError())
        }
    }
    var clock: DirectClock { DirectClock(now: { [self] in now }, monotonic: { [self] in Double(now) }, sleep: { [self] ms in try await sleep(ms) }) }
}

final class FakeLeg: FrameStream, @unchecked Sendable {
    let onSend: @Sendable (Data) -> Void
    init(_ onSend: @escaping @Sendable (Data) -> Void) { self.onSend = onSend }
    func opened() async -> Bool { true }
    func receive() async throws -> Data { try await Task.sleep(nanoseconds: .max); return Data() }
    func ended() async -> PathEnd { PathEnd() }
    func send(_ data: Data, done: @escaping @Sendable (Error?) -> Void) { onSend(data); done(nil) }
    func cancel() {}
    func close() {}
}

/// The engine against a computer that is a second DirectSession, with real crypto and a fake SDK.
final class EngineRig: @unchecked Sendable {
    typealias W = PeerWire
    let net = FakeRtcNet(), time: FakeClock, machineBudget = DirectBudget()
    let record: DeviceSecurity.Record, machineId: String, machine: W.Identity, scope: W.Scope, codec: PeerCodec, clientId: String
    var engine: DirectEngine!
    var leg: FakeLeg!
    private let lock = NSRecursiveLock()
    private let deliveryChain = SerialChain()
    private let machineDeliveryChain = SerialChain()
    private var sessions: [String: DirectSession] = [:], _connections: [W.Bytes] = [], _frames: [(Int, W.Bytes)] = [], _control15Subs: [[String]] = [], controlSeq: UInt64 = 0
    private var stateWaiters: [(id: UUID, predicate: @Sendable () -> Bool, cont: CheckedContinuation<Void, Never>)] = []
    private var pendingDeliveries = 0, deliveryCounter = 0
    private var _holdUse1Confirm = false
    private var heldUse1Confirm: [W.Bytes] = []
    var silent = false, replies = true
    var holdUse1Confirm: Bool { get { lock.withLock { _holdUse1Confirm } } set { lock.withLock { _holdUse1Confirm = newValue } } }
    var heldUse1ConfirmCount: Int { lock.withLock { heldUse1Confirm.count } }
    var probes: Int { lock.withLock { _connections.count } }
    var machineFrames: [(Int, W.Bytes)] { lock.withLock { _frames } }
    var control15Subs: [[String]] { lock.withLock { _control15Subs } }
    var changes = 0
    var prefs = MemoryDirectPreferences()

    init(random: @escaping @Sendable () -> Double = { 1.0 }) throws {
        let fixture = try DeviceSecurityTests(), genesis = try fixture.state("genesis"), state = try fixture.state("added"), pin = try fixture.pin()
        time = FakeClock(state.core.issuedAt)
        typealias S = DeviceSecurity
        var lease = S.HeadLease(domain: state.core.domain, head: try S.stateHash(state), issuedAt: state.core.issuedAt, notAfter: state.core.issuedAt + S.lease, signature: Data())
        lease.signature = try SigningKey(seed: fixture.hex("phone_seed")).sign(lease.signBytes())
        record = try S.Record(pin: pin, states: [1: genesis, 2: state], memberID: genesis.core.authorityID, xPrivate: fixture.hex("phone_x"), member: true, secret: fixture.hex("content_secret_2"), lease: lease)
        let sign = try Curve25519.Signing.PrivateKey(rawRepresentation: fixture.hex("machine_seed")), box = try fixture.machineX(), member = state.core.members.first { $0.kind == 1 }!
        machineId = member.id; clientId = genesis.core.authorityID
        machine = W.Identity(peer: W.Public(id: W.utf8(member.id), instance: W.random(16), boot: W.random(32), role: 2, sign: sign.publicKey, box: box.publicKey), sign: sign, box: box)
        scope = W.Scope(account: W.utf8(pin.account), accountDomain: Array(pin.domain), head: Array(try S.stateHash(state)), epoch: state.core.epoch)
        codec = PeerCodec(scope: scope)
        let seed = try fixture.hex("phone_seed")
        engine = DirectEngine(server: "http://127.0.0.1:1", deviceId: genesis.core.authorityID, deviceSeed: seed, factory: FakeRtcFactory(net: net), preferences: prefs, stun: ["stun:127.0.0.1:3478"],
                              clock: time.clock, random: random, authorize: {}, changed: { [weak self] in self?.lock.withLock { self?.changes += 1 }; self?.signalStateChange() })
        leg = FakeLeg { [weak self] data in self?.fromClient(data) }
        net.onEvent = { [weak self] in self?.signalStateChange() }
    }
    private func signalStateChange() {
        let ready: [CheckedContinuation<Void, Never>] = lock.withLock {
            var fulfilled: [CheckedContinuation<Void, Never>] = []
            var remaining: [(id: UUID, predicate: @Sendable () -> Bool, cont: CheckedContinuation<Void, Never>)] = []
            for w in stateWaiters {
                if w.predicate() { fulfilled.append(w.cont) } else { remaining.append(w) }
            }
            stateWaiters = remaining
            return fulfilled
        }
        for c in ready { c.resume() }
    }
    func waitForState(_ predicate: @escaping @Sendable () -> Bool) async {
        let waiterID = UUID()
        await withTaskCancellationHandler {
            await withCheckedContinuation { (cont: CheckedContinuation<Void, Never>) in
                let resumeNow = lock.withLock { () -> Bool in
                    if predicate() || Task.isCancelled { return true }
                    stateWaiters.append((waiterID, predicate, cont))
                    return false
                }
                if resumeNow { cont.resume() }
            }
        } onCancel: {
            let cont = lock.withLock { () -> CheckedContinuation<Void, Never>? in
                guard let idx = stateWaiters.firstIndex(where: { $0.id == waiterID }) else { return nil }
                return stateWaiters.remove(at: idx).cont
            }
            cont?.resume()
        }
    }
    func drainClient() async {
        while true {
            await machineDeliveryChain.drain()
            await waitForState { self.lock.withLock { self.pendingDeliveries == 0 } }
            let before = lock.withLock { deliveryCounter }
            await engine.drainForTest()
            await machineDeliveryChain.drain()
            let after = lock.withLock { (deliveryCounter, pendingDeliveries) }
            if after.0 == before && after.1 == 0 { break }
        }
    }
    func releaseUse1Confirm() {
        let frames = lock.withLock { () -> [W.Bytes] in
            _holdUse1Confirm = false
            let out = heldUse1Confirm
            heldUse1Confirm.removeAll()
            return out
        }
        for raw in frames {
            engine.receive(raw, from: leg)
        }
        signalStateChange()
    }
    private func tickAll() async {
        await engine.drainForTest()
        await machineDeliveryChain.drain()
        await engine.tickSessionsForTest()
        await machineDeliveryChain.drain()
        let active = lock.withLock { Array(sessions.values) }
        for s in active { await s.tickForTest() }
        for s in active { await s.drainForTest() }
        await machineDeliveryChain.drain()
        await engine.drainForTest()
    }
    func waitForBudgetEmpty() async {
        await engine.waitBudgetEmptyForTest()
        await machineBudget.waitUntilEmptyForTest()
    }
    func start() async { engine.attach(leg); await engine.synced(record, started: time.now); await tickAll() }
    /// Re-proves the head, as the 15 second security sync does; the lifetime lasts 20 seconds.
    func resync() async { await engine.synced(record, started: time.now) }
    /// Time passing under an established path: small steps, so its liveness probes are answered between them.
    func pass(_ ms: UInt64) async {
        var left = ms
        while left > 0 {
            let step = min(left, 500)
            time.advance(step)
            left -= step
            if left % 10_000 < 500 { await resync() }
            await engine.timerStep()
            await tickAll()
        }
    }
    /// Drives upkeep and waits deterministically for `condition` via state/network events.
    func settle(_ condition: @escaping @Sendable () -> Bool) async -> Bool {
        await engine.timerStep()
        await tickAll()
        if condition() { return true }
        await waitForState(condition)
        return condition()
    }
    /// Moves the clock in steps the sync can follow, giving the engine its upkeep each time.
    func advance(_ ms: UInt64) async {
        var left = ms
        while left > 0 {
            let step = min(left, 10_000)
            time.advance(step)
            left -= step
            await resync()
            await engine.timerStep()
            await tickAll()
        }
    }
    private func client() -> W.Public { try! W.Public(id: W.utf8(clientId), instance: [], boot: [], role: 1, sign: .init(rawRepresentation: record.state.core.member(clientId)!.ed), box: .init(rawRepresentation: record.state.core.member(clientId)!.x)) }
    private func fromClient(_ data: Data) {
        guard let outer = try? PBPeerFrame(serializedBytes: data), case .directedPeerFrame(let d)? = outer.kind else { return }
        let delivery = W.pb([W.b(1, W.utf8(clientId)), W.b(2, d.toDevice.map { $0 }), W.u(3, UInt64(d.frameClass.rawValue)), W.b(4, Array(d.frameID)), W.b(5, Array(d.sealed))])
        var peer = client()
        if let activePeer = lock.withLock({ sessions.values.first?.peer }),
           let o = try? codec.open(delivery, activePeer, machine, time.now), o.ctx.cls == 2, o.type == 15,
           let sub = try? PBDirectEventSubscription(serializedBytes: Data(o.payload)) {
            lock.withLock { _control15Subs.append(sub.sessionIds) }
            signalStateChange()
        }
        if let o = try? codec.open(delivery, peer, machine, time.now), o.ctx.cls == 1, o.type == 1 {
            let key = W.hex(o.ctx.connection)
            let existing = lock.withLock { sessions[key] }
            if existing == nil && silent {
                lock.withLock { if !_connections.contains(where: { $0 == o.ctx.connection }) { _connections.append(o.ctx.connection) } }
                signalStateChange()
                return
            }
            if existing == nil {
                peer.instance = o.ctx.senderInstance; peer.boot = o.ctx.senderBoot
                let session = makeSession(peer, o.ctx.connection, o.ctx.deadline)
                lock.withLock { sessions[key] = session; _connections.append(o.ctx.connection) }
                signalStateChange()
                machineDeliveryChain.run { [weak session] in
                    guard let session else { return }
                    session.receive(delivery)
                    await session.drainForTest()
                }
                return
            }
        }
        let targets = lock.withLock { Array(sessions.values) }
        machineDeliveryChain.run {
            for s in targets { s.receive(delivery) }
            for s in targets { await s.drainForTest() }
        }
    }
    private func makeSession(_ peer: W.Public, _ connection: W.Bytes, _ deadline: UInt64) -> DirectSession {
        let key = W.hex(connection)
        let retireOld: @Sendable () -> Void = { [weak self] in
            guard let self else { return }
            let old = self.lock.withLock { self.sessions.filter { $0.key != key }.map(\.value) }
            for s in old { s.retire() }
        }
        let o = DirectSessionOptions(
            selfID: machine, peer: peer, codec: codec, connection: connection, deadline: min(deadline, time.now + 10_000), budget: machineBudget, stun: ["stun:127.0.0.1:3478"], factory: FakeRtcFactory(net: net),
            valid: { true }, authorize: {},
            send: { [weak self] out in
                guard let self else { return false }
                let bytes = out.raw
                let hold = self.lock.withLock { () -> Bool in
                    guard self._holdUse1Confirm,
                          let o = try? self.codec.openSent(bytes, self.machine, peer, self.time.now),
                          o.ctx.cls == 1, o.ctx.use == 1, o.type == 7
                    else { return false }
                    self.heldUse1Confirm.append(bytes)
                    return true
                }
                if hold {
                    self.signalStateChange()
                    return true
                }
                self.lock.withLock { self.pendingDeliveries += 1; self.deliveryCounter += 1 }
                self.deliveryChain.run { [weak self] in
                    guard let self else { return }
                    self.engine.receive(bytes, from: self.leg)
                    await self.engine.drainForTest()
                    self.lock.withLock { self.pendingDeliveries -= 1 }
                    self.signalStateChange()
                }
                return true
            },
            chosen: retireOld, beforeOffer: retireOld, ready: { [weak self] use, path in self?.register(use, path) }, closed: { _ in },
            frame: { [weak self] raw, use in
                guard let self else { return }
                self.lock.withLock { self._frames.append((use, raw)) }
                if self.replies, let path = self.machinePath(use) { self.reply(raw, path) }
            },
            capability: { _ in }, clock: time.clock)
        return try! DirectSession(o)
    }
    private var _paths: [Int: DirectChannel] = [:]
    var paths: [Int: DirectChannel] { lock.withLock { _paths } }
    private func machinePath(_ use: Int) -> DirectChannel? { lock.withLock { _paths[use] } }
    func register(_ use: Int, _ path: DirectChannel) { lock.withLock { _paths[use] = path }; signalStateChange() }
    private func reply(_ raw: W.Bytes, _ path: DirectChannel) {
        guard let env = try? PBEnvelope(serializedBytes: raw), var header = try? DeviceSecurity.parseHeader(env.securityHeader) else { return }
        header.purpose = .result; header.kind = DeviceSecurity.Purpose.result.kind; header.sender = machineId
        var out = PBEnvelope(); out.securityHeader = (try? DeviceSecurity.headerBytes(header)) ?? Data()
        Task.detached { try? await path.write(Array((try? out.serializedData()) ?? Data()), handoff: {}) }
    }
    /// A signed Control 14 from the computer, as the daemon announces what it can do.
    func announce(maxChunk: Int32 = 786_432, upload: Bool = true, download: Bool = true) {
        guard let session = lock.withLock({ sessions.values.first(where: { $0.live }) ?? sessions.values.first }) else { return }
        controlSeq += 1
        var caps = PBMachineCapabilities(); caps.maxChunkBytes = maxChunk; caps.fileUpload = upload; caps.imageUpload = upload
        var snapshot = PBCapabilitySnapshot(); snapshot.capabilityID = Data(W.random(16)); snapshot.capabilities = caps; snapshot.expiresAtMs = Int64(time.now + 50_000)
        let peer = session.peer
        var context = W.Context(machine.peer, peer, session.options.connection, 0, 1, controlSeq, time.now, 2)
        guard let raw = try? codec.seal(machine, peer, context, 14, Array((try? snapshot.serializedData()) ?? Data())) else { return }
        context.cls = 2
        engine.receive(raw, from: leg)
    }
    /// A signed Control 1 Capabilities advertising Control 15 event push.
    func announceEventCapabilities() {
        guard let session = lock.withLock({ sessions.values.first }) else { return }
        controlSeq += 1
        let payload = W.pb([
            W.b(1, W.random(16)),
            W.u(2, 15),
            W.u(3, 1),
            W.u(4, time.now + 50_000),
            W.u(5, 1),
            W.u(6, 1_048_576),
        ])
        let peer = session.peer
        let context = W.Context(machine.peer, peer, session.options.connection, 0, 1, controlSeq, time.now, 2)
        guard let raw = try? codec.seal(machine, peer, context, 1, payload) else { return }
        engine.receive(raw, from: leg)
    }
    func envelope(_ request: String = newRequestId(), deadline: UInt64? = nil) -> PBEnvelope {
        let h = DeviceSecurity.Header(account: record.pin.account, domain: record.pin.domain, epoch: 2, state: Data(repeating: 1, count: 32), contentEpoch: 1, sender: clientId, kind: DeviceSecurity.Purpose.command.kind, purpose: .command, machine: machineId, session: "", request: request, seq: 0, deadline: deadline ?? (time.now + 60_000), compression: 0)
        var e = PBEnvelope(); e.securityHeader = (try? DeviceSecurity.headerBytes(h)) ?? Data(); return e
    }
    /// A Probe from the computer on a connection of its own that never goes on: no Challenge answer, no Offer.
    func lateProbe() async {
        let peer = client(), connection = W.random(16)
        var context = W.Context(machine.peer, W.member(peer), connection, 0, 0, 1, time.now, 1, true); context.recipientUnknown = 1
        let body = W.pb([W.b(1, connection), W.b(2, machine.instance), W.b(3, machine.boot)])
        guard let raw = try? codec.seal(machine, W.member(peer), context, 1, body) else { return }
        engine.receive(raw, from: leg)
    }
    func close() {
        engine.close()
        for s in lock.withLock({ Array(sessions.values) }) { s.close() }
        let leftover = lock.withLock { () -> [CheckedContinuation<Void, Never>] in
            let out = stateWaiters.map(\.cont)
            stateWaiters.removeAll()
            return out
        }
        for c in leftover { c.resume() }
    }
}

final class MemoryDirectPreferences: DirectPreferenceStore, @unchecked Sendable {
    private let lock = NSRecursiveLock()
    private let deliveryChain = SerialChain()
    private let machineDeliveryChain = SerialChain(); private var values: [String: Bool] = [:]
    var failWrites = false
    func isEnabled(server: String, deviceId: String, machineId: String) -> Bool { lock.withLock { values[machineId] ?? true } }
    func setEnabled(server: String, deviceId: String, machineId: String, enabled: Bool) throws {
        if failWrites { throw DeviceSecurity.Failure.storage }
        lock.withLock { values[machineId] = enabled }
    }
}
