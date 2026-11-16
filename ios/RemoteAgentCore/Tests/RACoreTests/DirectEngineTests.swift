import CryptoKit
import Foundation
import RAProto
import Testing
@testable import RACore

/// Real time plus an offset the test can jump: waiting still lets timers run, and a 10 second
/// round can be ended without waiting for it.
final class FakeClock: @unchecked Sendable {
    private let lock = NSLock(); private var offset: UInt64 = 0
    private let start = DispatchTime.now().uptimeNanoseconds, base: UInt64
    init(_ ms: UInt64) { base = ms }
    var now: UInt64 { lock.withLock { base + offset + (DispatchTime.now().uptimeNanoseconds - start) / 1_000_000 } }
    func advance(_ by: UInt64) { lock.withLock { offset += by } }
    var clock: DirectClock { DirectClock(now: { [self] in now }, monotonic: { [self] in Double(now) }) }
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
    private let lock = NSLock()
    private var sessions: [String: DirectSession] = [:], _connections: [W.Bytes] = [], _frames: [(Int, W.Bytes)] = [], controlSeq: UInt64 = 0
    var silent = false, replies = true
    var probes: Int { lock.withLock { _connections.count } }
    var machineFrames: [(Int, W.Bytes)] { lock.withLock { _frames } }
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
                              clock: time.clock, random: random, authorize: {}, changed: { [weak self] in self?.lock.withLock { self?.changes += 1 } })
        leg = FakeLeg { [weak self] data in self?.fromClient(data) }
    }
    func start() async { engine.attach(leg); await engine.synced(record, started: time.now) }
    /// Re-proves the head, as the 15 second security sync does; the lifetime lasts 20 seconds.
    func resync() async { await engine.synced(record, started: time.now) }
    /// Time passing under an established path: small steps, so its liveness probes are answered between them.
    func pass(_ ms: UInt64) async {
        var left = ms
        while left > 0 { let step = min(left, 500); time.advance(step); left -= step; if left % 10_000 < 500 { await resync() }; await engine.timerStep(); try? await Task.sleep(nanoseconds: 40_000_000) }
    }
    /// Waits for a condition while giving the engine its upkeep, so a loaded machine delays the
    /// wait instead of failing it: the clock is fake, but the engine's work is scheduled for real.
    func settle(_ seconds: Double = 10, _ condition: @Sendable () -> Bool) async -> Bool {
        let end = Date().addingTimeInterval(seconds)
        while Date() < end {
            if condition() { return true }
            await engine.timerStep()
            try? await Task.sleep(nanoseconds: 20_000_000)
        }
        return condition()
    }
    /// Moves the clock in steps the sync can follow, giving the engine its upkeep each time.
    func advance(_ ms: UInt64) async {
        var left = ms
        while left > 0 { let step = min(left, 10_000); time.advance(step); left -= step; await resync(); await engine.timerStep(); try? await Task.sleep(nanoseconds: 60_000_000) }
    }
    private func client() -> W.Public { try! W.Public(id: W.utf8(clientId), instance: [], boot: [], role: 1, sign: .init(rawRepresentation: record.state.core.member(clientId)!.ed), box: .init(rawRepresentation: record.state.core.member(clientId)!.x)) }
    private func fromClient(_ data: Data) {
        guard let outer = try? PBPeerFrame(serializedBytes: data), case .directedPeerFrame(let d)? = outer.kind else { return }
        let delivery = W.pb([W.b(1, W.utf8(clientId)), W.b(2, d.toDevice.map { $0 }), W.u(3, UInt64(d.frameClass.rawValue)), W.b(4, Array(d.frameID)), W.b(5, Array(d.sealed))])
        var peer = client()
        if let o = try? codec.open(delivery, peer, machine, time.now), o.ctx.cls == 1, o.type == 1 {
            let key = W.hex(o.ctx.connection)
            let existing = lock.withLock { sessions[key] }
            if existing == nil && silent {
                lock.withLock { if !_connections.contains(where: { $0 == o.ctx.connection }) { _connections.append(o.ctx.connection) } }; return
            }
            if existing == nil {
                peer.instance = o.ctx.senderInstance; peer.boot = o.ctx.senderBoot
                let session = makeSession(peer, o.ctx.connection, o.ctx.deadline)
                lock.withLock { sessions[key] = session; _connections.append(o.ctx.connection) }
                session.receive(delivery); return
            }
        }
        for s in lock.withLock({ Array(sessions.values) }) { s.receive(delivery) }
    }
    private func makeSession(_ peer: W.Public, _ connection: W.Bytes, _ deadline: UInt64) -> DirectSession {
        let o = DirectSessionOptions(
            selfID: machine, peer: peer, codec: codec, connection: connection, deadline: min(deadline, time.now + 10_000), budget: machineBudget, stun: ["stun:127.0.0.1:3478"], factory: FakeRtcFactory(net: net),
            valid: { true }, authorize: {},
            send: { [weak self] out in
                let bytes = out.raw
                Task.detached { if let self { self.engine.receive(bytes, from: self.leg) } }; return true
            },
            chosen: {}, beforeOffer: {}, ready: { [weak self] use, path in self?.register(use, path) }, closed: { _ in },
            frame: { [weak self] raw, use in
                guard let self else { return }
                self.lock.withLock { self._frames.append((use, raw)) }
                if self.replies, let path = self.machinePath(use) { self.reply(raw, path) }
            },
            capability: { _ in }, clock: time.clock)
        return try! DirectSession(o)
    }
    var paths: [Int: DirectChannel] = [:]
    private func machinePath(_ use: Int) -> DirectChannel? { lock.withLock { paths[use] } }
    func register(_ use: Int, _ path: DirectChannel) { lock.withLock { paths[use] = path } }
    private func reply(_ raw: W.Bytes, _ path: DirectChannel) {
        guard let env = try? PBEnvelope(serializedBytes: raw), var header = try? DeviceSecurity.parseHeader(env.securityHeader) else { return }
        header.purpose = .result; header.kind = DeviceSecurity.Purpose.result.kind; header.sender = machineId
        var out = PBEnvelope(); out.securityHeader = (try? DeviceSecurity.headerBytes(header)) ?? Data()
        Task.detached { try? await path.write(Array((try? out.serializedData()) ?? Data()), handoff: {}) }
    }
    /// A signed Control 14 from the computer, as the daemon announces what it can do.
    func announce(maxChunk: Int32 = 786_432, upload: Bool = true, download: Bool = true) {
        guard let (key, session) = lock.withLock({ sessions.first }) else { return }
        _ = key
        controlSeq += 1
        var caps = PBMachineCapabilities(); caps.maxChunkBytes = maxChunk; caps.fileUpload = upload; caps.imageUpload = upload
        var snapshot = PBCapabilitySnapshot(); snapshot.capabilityID = Data(W.random(16)); snapshot.capabilities = caps; snapshot.expiresAtMs = Int64(time.now + 50_000)
        let peer = session.peer
        var context = W.Context(machine.peer, peer, session.options.connection, 0, 1, controlSeq, time.now, 2)
        guard let raw = try? codec.seal(machine, peer, context, 14, Array((try? snapshot.serializedData()) ?? Data())) else { return }
        context.cls = 2
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
    func close() { engine.close(); for s in lock.withLock({ Array(sessions.values) }) { s.close() } }
}

final class MemoryDirectPreferences: DirectPreferenceStore, @unchecked Sendable {
    private let lock = NSLock(); private var values: [String: Bool] = [:]
    var failWrites = false
    func isEnabled(server: String, deviceId: String, machineId: String) -> Bool { lock.withLock { values[machineId] ?? true } }
    func setEnabled(server: String, deviceId: String, machineId: String, enabled: Bool) throws {
        if failWrites { throw DeviceSecurity.Failure.storage }
        lock.withLock { values[machineId] = enabled }
    }
}
